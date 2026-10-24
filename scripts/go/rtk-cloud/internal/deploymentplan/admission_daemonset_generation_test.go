package deploymentplan

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

const daemonSetGenerationAnnotation = "deprecated.daemonset.template.generation"

func daemonSetGenerationFixture(t *testing.T, environment, counter string) (Inputs, Definition, Object) {
	t.Helper()
	in, definition, live := fixture(t, environment, "DaemonSet")
	for _, object := range []Object{definition.Object, live} {
		delete(Map(object["spec"]), "replicas")
		Map(object["metadata"])["annotations"] = Object{"policy.example/identity": "qualified-policy"}
		template := Map(Map(object["spec"])["template"])
		template["metadata"] = Object{"annotations": Object{daemonSetGenerationAnnotation: "qualified-template-annotation"}}
		pod := Map(template["spec"])
		pod["securityContext"] = Object{"runAsNonRoot": true}
		container := Map(pod["containers"].([]any)[0])
		container["env"] = []any{Object{"name": "RUNTIME_MODE", "value": "qualified"}}
		container["securityContext"] = Object{"allowPrivilegeEscalation": false, "capabilities": Object{"drop": []any{"ALL"}}}
	}
	Map(Map(live["metadata"])["annotations"])[daemonSetGenerationAnnotation] = counter
	Map(live["metadata"])["generation"] = float64(1)
	pod := Map(Map(Map(definition.Object["spec"])["template"])["spec"])
	Map(pod["containers"].([]any)[0])["image"] = "registry.example/owned@sha256:" + strings.Repeat("b", 64)
	return in, definition, live
}

func daemonSetGenerationPair(t *testing.T, environment string) (Object, Object) {
	t.Helper()
	in, definition, live := daemonSetGenerationFixture(t, environment, "1")
	plan, err := Compile(in, []Definition{definition}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	body, err := plan.Resources[0].Body(live)
	if err != nil {
		t.Fatal(err)
	}
	admitted := Clone(body)
	Map(Map(admitted["metadata"])["annotations"])[daemonSetGenerationAnnotation] = "2"
	Map(admitted["metadata"])["generation"] = float64(2)
	return body, admitted
}

func TestDaemonSetGenerationAdmissionAllowsOnlyNativeCounterIncrement(t *testing.T) {
	type mutation func(Object, Object)
	counter := func(old, next any) mutation {
		return func(body, admitted Object) {
			Map(Map(body["metadata"])["annotations"])[daemonSetGenerationAnnotation] = old
			Map(Map(admitted["metadata"])["annotations"])[daemonSetGenerationAnnotation] = next
		}
	}
	generation := func(old, next any) mutation {
		return func(body, admitted Object) {
			Map(body["metadata"])["generation"] = old
			Map(admitted["metadata"])["generation"] = next
		}
	}
	cases := []struct {
		name   string
		mutate mutation
		allow  bool
	}{
		{"increment", func(Object, Object) {}, true},
		{"unchanged", counter("1", "1"), true},
		{"unchanged-invalid-is-strict", counter("legacy", "legacy"), true},
		{"max-counter", counter("9223372036854775806", "9223372036854775807"), true},
		{"max-safe-generation", generation(float64(9007199254740990), float64(9007199254740991)), true},
		{"counter-jump", counter("1", "3"), false},
		{"counter-decrease", counter("2", "1"), false},
		{"counter-overflow", counter("9223372036854775807", "9223372036854775808"), false},
		{"counter-zero", counter("0", "1"), false},
		{"counter-negative", counter("-1", "0"), false},
		{"counter-null-original", counter(nil, "2"), false},
		{"counter-null-admitted", counter("1", nil), false},
		{"counter-numeric-original", counter(float64(1), "2"), false},
		{"counter-numeric-admitted", counter("1", float64(2)), false},
		{"counter-sign-original", counter("+1", "2"), false},
		{"counter-sign-admitted", counter("1", "+2"), false},
		{"counter-leading-zero-original", counter("01", "2"), false},
		{"counter-leading-zero-admitted", counter("1", "02"), false},
		{"counter-space-original", counter(" 1", "2"), false},
		{"counter-space-admitted", counter("1", "2 "), false},
		{"counter-malformed", counter("invalid", "2"), false},
		{"counter-removed", func(_, admitted Object) {
			delete(Map(Map(admitted["metadata"])["annotations"]), daemonSetGenerationAnnotation)
		}, false},
		{"counter-original-absent-uses-defaults", func(body, _ Object) { delete(Map(Map(body["metadata"])["annotations"]), daemonSetGenerationAnnotation) }, true},
		{"generation-same", generation(float64(1), float64(1)), false},
		{"generation-jump", generation(float64(1), float64(3)), false},
		{"generation-decrease", generation(float64(2), float64(1)), false},
		{"generation-zero", generation(float64(0), float64(1)), false},
		{"generation-negative", generation(float64(-1), float64(0)), false},
		{"generation-fraction-original", generation(1.5, 2.5), false},
		{"generation-fraction-admitted", generation(float64(1), 2.5), false},
		{"generation-numeric-type", generation(int64(1), int64(2)), false},
		{"generation-string", generation("1", "2"), false},
		{"generation-null", generation(nil, float64(2)), false},
		{"generation-missing-original", func(body, _ Object) { delete(Map(body["metadata"]), "generation") }, false},
		{"generation-missing-admitted", func(_, admitted Object) { delete(Map(admitted["metadata"]), "generation") }, false},
		{"generation-unsafe-original", generation(float64(9007199254740992), float64(9007199254740994)), false},
		{"generation-unsafe-admitted", generation(float64(9007199254740991), float64(9007199254740992)), false},
		{"generation-infinite-original", generation(math.Inf(1), float64(2)), false},
		{"generation-infinite-admitted", generation(float64(1), math.Inf(1)), false},
		{"generation-nan-original", generation(math.NaN(), float64(2)), false},
		{"generation-nan-admitted", generation(float64(1), math.NaN()), false},
		{"invalid-json-pod-field", func(_, admitted Object) {
			Map(Map(Map(admitted["spec"])["template"])["spec"])["invalidNonfiniteValue"] = math.Inf(1)
		}, false},
		{"other-kind", func(body, admitted Object) { body["kind"], admitted["kind"] = "Deployment", "Deployment" }, false},
		{"admitted-kind", func(_, admitted Object) { admitted["kind"] = "Deployment" }, false},
		{"other-api", func(body, admitted Object) {
			body["apiVersion"], admitted["apiVersion"] = "apps/v1beta2", "apps/v1beta2"
		}, false},
		{"admitted-api", func(_, admitted Object) { admitted["apiVersion"] = "apps/v1beta2" }, false},
		{"replaced-uid", func(_, admitted Object) { Map(admitted["metadata"])["uid"] = "replacement-owner" }, false},
		{"no-original-uid", func(body, admitted Object) {
			delete(Map(body["metadata"]), "uid")
			delete(Map(admitted["metadata"]), "uid")
		}, false},
		{"no-admitted-uid", func(_, admitted Object) { delete(Map(admitted["metadata"]), "uid") }, false},
		{"template-annotation", func(_, admitted Object) {
			Map(Map(Map(Map(admitted["spec"])["template"])["metadata"])["annotations"])[daemonSetGenerationAnnotation] = "changed-template-annotation"
		}, false},
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, test := range cases {
			t.Run(environment+"/"+test.name, func(t *testing.T) {
				body, admitted := daemonSetGenerationPair(t, environment)
				test.mutate(body, admitted)
				beforeBody, beforeAdmitted := Digest(body), Digest(admitted)
				if matchesAdmission(body, admitted) != test.allow {
					t.Fatal("native counter admission decision differs from the bounded rule")
				}
				if Digest(body) != beforeBody || Digest(admitted) != beforeAdmitted {
					t.Fatal("counter admission changed an input object")
				}
			})
		}
	}
}

type daemonSetGenerationBackend struct {
	memoryBackend
	mutate func(Object)
}

func (b *daemonSetGenerationBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	actual, err := b.memoryBackend.Write(ctx, action, body, dry)
	if err != nil {
		return actual, err
	}
	actual = Clone(actual)
	b.mutate(actual)
	if !dry {
		b.objects[ID(actual)] = Clone(actual)
	}
	return actual, nil
}

func TestDaemonSetGenerationAdmissionAndExecutionForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"unchanged", "unchanged-invalid", "increment", "max-counter", "kubectl-bookkeeping", "jump", "generation-missing", "image", "env", "pod-security", "container-security", "selector", "other-annotation", "uid"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				original := "1"
				if mode == "max-counter" {
					original = "9223372036854775806"
				} else if mode == "unchanged-invalid" {
					original = "legacy"
				}
				in, definition, live := daemonSetGenerationFixture(t, environment, original)
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "update" {
					t.Fatal("fixture did not prepare an existing DaemonSet update", err)
				}
				body, err := plan.Resources[0].Body(live)
				if err != nil {
					t.Fatal(err)
				}
				bodyDigest, planDigest, liveDigest := Digest(body), plan.SHA256(), Digest(live)
				backend := &daemonSetGenerationBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}}}
				backend.mutate = func(actual Object) {
					if strings.HasPrefix(mode, "unchanged") {
						return
					}
					metadata := Map(actual["metadata"])
					annotations := Map(metadata["annotations"])
					annotations[daemonSetGenerationAnnotation] = "2"
					metadata["generation"] = float64(2)
					pod := Map(Map(Map(actual["spec"])["template"])["spec"])
					container := Map(pod["containers"].([]any)[0])
					switch mode {
					case "max-counter":
						annotations[daemonSetGenerationAnnotation] = "9223372036854775807"
					case "kubectl-bookkeeping":
						annotations["kubectl.kubernetes.io/last-applied-configuration"] = "client-bookkeeping"
					case "jump":
						annotations[daemonSetGenerationAnnotation] = "3"
					case "generation-missing":
						delete(metadata, "generation")
					case "image":
						container["image"] = "registry.example/unapproved:latest"
					case "env":
						Map(container["env"].([]any)[0])["value"] = "credential=unapproved"
					case "pod-security":
						Map(pod["securityContext"])["runAsNonRoot"] = false
					case "container-security":
						Map(container["securityContext"])["allowPrivilegeEscalation"] = true
					case "selector":
						Map(Map(Map(actual["spec"])["selector"])["matchLabels"])["app"] = "foreign-owner"
					case "other-annotation":
						annotations["policy.example/identity"] = "credential=unapproved"
					case "uid":
						metadata["uid"] = "replacement-owner"
					}
				}
				allowed := strings.HasPrefix(mode, "unchanged") || mode == "increment" || mode == "max-counter" || mode == "kubectl-bookkeeping"
				err = plan.Admit(context.Background(), backend)
				if (err == nil) != allowed {
					t.Fatal("admission accepted unrelated drift or rejected the native counter", err)
				}
				if plan.SHA256() != planDigest || Digest(body) != bodyDigest || Digest(live) != liveDigest || Digest(backend.objects[ID(live)]) != liveDigest || len(backend.written) != 0 {
					t.Fatal("admission mutated the exact plan, request or original owner")
				}
				if err != nil {
					var blocker *Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.admission" || strings.Contains(err.Error(), "credential=") {
						t.Fatal("admission failure did not retain its private-safe stable blocker")
					}
				}
				privateRoot := t.TempDir()
				if err := os.Chmod(privateRoot, 0700); err != nil {
					t.Fatal(err)
				}
				qualified := func(context.Context, *Plan) error { return nil }
				err = plan.Execute(context.Background(), backend, planDigest, privateRoot, qualified, qualified, qualified)
				if (err == nil) != allowed || plan.SHA256() != planDigest || Digest(body) != bodyDigest || Digest(live) != liveDigest {
					t.Fatal("execution did not share the admission rule or changed reviewed inputs", err)
				}
				if allowed && len(backend.written) != 1 || !allowed && len(backend.written) != 0 {
					t.Fatal("unapproved mutation persisted or approved update did not execute")
				}
				for _, request := range append(append([]Object{}, backend.dry...), backend.written...) {
					if Digest(request) != bodyDigest || Map(Map(request["metadata"])["annotations"])[daemonSetGenerationAnnotation] != original || Map(request["metadata"])["uid"] != "owner" || Map(request["metadata"])["resourceVersion"] != "4" {
						t.Fatal("admission normalization rewrote the UID/RV-bound request or removed its native counter")
					}
				}
				changed := Clone(live)
				Map(Map(changed["metadata"])["annotations"])[daemonSetGenerationAnnotation] = "external-counter-change"
				if _, err := plan.Resources[0].Body(changed); err == nil {
					t.Fatal("native admission exception weakened the original baseline drift fence")
				}
				changed = Clone(live)
				Map(changed["metadata"])["uid"] = "replacement-owner"
				if _, err := plan.Resources[0].Body(changed); err == nil {
					t.Fatal("native admission exception weakened the original owner fence")
				}
				changed = Clone(live)
				Map(changed["metadata"])["resourceVersion"] = "5"
				freshBody, err := plan.Resources[0].Body(changed)
				if err != nil || Map(freshBody["metadata"])["resourceVersion"] != "5" || Map(Map(freshBody["metadata"])["annotations"])[daemonSetGenerationAnnotation] != original || plan.SHA256() != planDigest {
					t.Fatal("RV-only drift did not produce a fresh exact request with the original counter", err)
				}
			})
		}
	}
}
