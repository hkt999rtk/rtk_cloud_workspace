package deploymentplan

import (
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func diagnosticContainer(object Object) Object {
	return Map(Map(Map(Map(object["spec"])["template"])["spec"])["containers"].([]any)[0])
}

func diagnosticAdmissionPair(t *testing.T, environment string) (Object, Object) {
	t.Helper()
	_, definition, live := fixture(t, environment, "Deployment")
	body := Clone(definition.Object)
	Map(body["metadata"])["uid"] = Map(live["metadata"])["uid"]
	Map(body["metadata"])["resourceVersion"] = Map(live["metadata"])["resourceVersion"]
	Map(body["metadata"])["annotations"] = Object{"policy.example/private-name": "private-original", "kubectl.kubernetes.io/last-applied-configuration": "private-original-bookkeeping"}
	diagnosticContainer(body)["env"] = []any{Object{"name": "PRIVATE_ENV_NAME", "value": ""}}
	diagnosticContainer(body)["securityContext"] = Object{"allowPrivilegeEscalation": false}
	return body, Clone(body)
}

func TestAdmissionDiagnosticPathsAreExactAndPrivateForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, test := range []struct {
			name   string
			mutate func(Object)
			path   string
		}{
			{"empty-env-value-removed", func(o Object) { delete(Map(diagnosticContainer(o)["env"].([]any)[0]), "value") }, "$.spec.template.spec.containers[0].env[0].value"},
			{"env-value", func(o Object) { Map(diagnosticContainer(o)["env"].([]any)[0])["value"] = "private-marker" }, "$.spec.template.spec.containers[0].env[0].value"},
			{"env-name", func(o Object) { Map(diagnosticContainer(o)["env"].([]any)[0])["name"] = "private-marker" }, "$.spec.template.spec.containers[0].env[0].name"},
			{"image", func(o Object) { diagnosticContainer(o)["image"] = "private-marker" }, "$.spec.template.spec.containers[0].image"},
			{"security", func(o Object) { Map(diagnosticContainer(o)["securityContext"])["allowPrivilegeEscalation"] = true }, "$.spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation"},
			{"replicas", func(o Object) { Map(o["spec"])["replicas"] = float64(2) }, "$.spec.replicas"},
			{"array-length", func(o Object) { diagnosticContainer(o)["env"] = []any{} }, "$.spec.template.spec.containers[0].env"},
			{"name", func(o Object) { Map(o["metadata"])["name"] = "private-marker" }, "$.metadata.name"},
			{"uid", func(o Object) { Map(o["metadata"])["uid"] = "private-marker" }, "$.metadata.uid"},
			{"labels", func(o Object) { Map(Map(o["metadata"])["labels"])["rtk.realtek.com/stack"] = "private-marker" }, "$.metadata.labels.*"},
			{"annotations", func(o Object) {
				Map(Map(o["metadata"])["annotations"])["policy.example/private-name"] = "private-marker"
			}, "$.metadata.annotations.*"},
			{"selector", func(o Object) { Map(Map(Map(o["spec"])["selector"])["matchLabels"])["app"] = "private-marker" }, "$.spec.selector.matchLabels.*"},
		} {
			t.Run(environment+"/"+test.name, func(t *testing.T) {
				body, admitted := diagnosticAdmissionPair(t, environment)
				test.mutate(admitted)
				bodyDigest, admittedDigest := Digest(body), Digest(admitted)
				before := matchesAdmission(body, admitted)
				paths := admissionMismatchPaths(body, admitted)
				if before || !reflect.DeepEqual(paths, []string{test.path}) || before != matchesAdmission(body, admitted) {
					t.Fatalf("diagnostic changed the verdict or lost the exact schema path: %v", paths)
				}
				cause := admissionMismatchCause("rejected", body, admitted)
				for _, private := range []string{"private-marker", "private-name", "PRIVATE_ENV_NAME", "private-original", "registry.example", "sha256:", "resourceVersion"} {
					if strings.Contains(cause, private) {
						t.Fatalf("diagnostic leaked private data: %q", private)
					}
				}
				if Digest(body) != bodyDigest || Digest(admitted) != admittedDigest {
					t.Fatal("diagnostic mutated original bodies")
				}
			})
		}
	}
}

func TestAdmissionDiagnosticKeepsDefaultsNullAndKnownExceptions(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, test := range []struct {
			name string
			pair func() (Object, Object)
		}{
			{"bookkeeping", func() (Object, Object) {
				body, admitted := diagnosticAdmissionPair(t, environment)
				Map(Map(admitted["metadata"])["annotations"])["kubectl.kubernetes.io/last-applied-configuration"] = "private-changed"
				return body, admitted
			}},
			{"daemonset-counter", func() (Object, Object) { return daemonSetGenerationPair(t, environment) }},
			{"unrelated-metadata-and-defaults", func() (Object, Object) {
				body, admitted := diagnosticAdmissionPair(t, environment)
				Map(Map(admitted["metadata"])["labels"])["private-default-label"] = "private-default-value"
				Map(admitted["metadata"])["resourceVersion"] = "5"
				Map(admitted["metadata"])["generation"] = float64(99)
				Map(admitted["spec"])["private-default-field"] = "private-default-value"
				return body, admitted
			}},
			{"null-versus-absent", func() (Object, Object) {
				body, admitted := diagnosticAdmissionPair(t, environment)
				diagnosticContainer(body)["value"] = nil
				return body, admitted
			}},
		} {
			t.Run(environment+"/"+test.name, func(t *testing.T) {
				body, admitted := test.pair()
				beforeBody, beforeAdmitted := Digest(body), Digest(admitted)
				if !matchesAdmission(body, admitted) || len(admissionMismatchPaths(body, admitted)) != 0 || !matchesAdmission(body, admitted) {
					t.Fatal("diagnostic changed allowed admission defaults or native exceptions")
				}
				if Digest(body) != beforeBody || Digest(admitted) != beforeAdmitted {
					t.Fatal("diagnostic normalized in place")
				}
			})
		}
		body, admitted := diagnosticAdmissionPair(t, environment)
		diagnosticContainer(body)["value"] = Object{}
		if matchesAdmission(body, admitted) || !reflect.DeepEqual(admissionMismatchPaths(body, admitted), []string{"$.spec.template.spec.containers[0].value"}) {
			t.Fatal("diagnostic flattened the existing empty-object versus absent rule")
		}
		body, admitted = daemonSetGenerationPair(t, environment)
		Map(Map(admitted["metadata"])["annotations"])[daemonSetGenerationAnnotation] = "3"
		if matchesAdmission(body, admitted) || !reflect.DeepEqual(admissionMismatchPaths(body, admitted), []string{"$.metadata.annotations.*", "$.metadata.generation"}) {
			t.Fatal("invalid native counter did not retain its rejection and safe diagnostic")
		}
	}
}

func TestAdmissionDiagnosticBoundsAndMasksCallerDefinedKeys(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			body, _ := diagnosticAdmissionPair(t, environment)
			for _, field := range []string{"data", "binaryData", "stringData"} {
				body[field] = Object{"PRIVATE_KEY_NAME\n=secret": "PRIVATE_VALUE", "image": "PRIVATE_VALUE"}
			}
			body["PRIVATE_UNKNOWN_KEY\n=secret"] = Object{"PRIVATE_NESTED_KEY": "PRIVATE_VALUE"}
			admitted := Clone(body)
			for _, field := range []string{"data", "binaryData", "stringData"} {
				Map(admitted[field])["PRIVATE_KEY_NAME\n=secret"] = "PRIVATE_OTHER_VALUE"
			}
			Map(admitted["PRIVATE_UNKNOWN_KEY\n=secret"])["PRIVATE_NESTED_KEY"] = "PRIVATE_OTHER_VALUE"
			paths := admissionMismatchPaths(body, admitted)
			if !reflect.DeepEqual(paths, []string{"$.*", "$.binaryData.*", "$.data.*", "$.stringData.*"}) {
				t.Fatalf("dynamic keys were not masked: %v", paths)
			}
			if cause := admissionMismatchCause("rejected", body, admitted); strings.Contains(cause, "PRIVATE_") || strings.Contains(cause, "\n") {
				t.Fatal("caller-defined key or value reached the public cause")
			}
			body, admitted = diagnosticAdmissionPair(t, environment)
			env := make([]any, 100)
			for index := range env {
				env[index] = Object{"name": "PRIVATE_NAME", "value": "PRIVATE_VALUE"}
			}
			diagnosticContainer(body)["env"] = env
			admitted = Clone(body)
			for _, value := range diagnosticContainer(admitted)["env"].([]any) {
				Map(value)["value"] = "PRIVATE_CHANGED"
			}
			paths = admissionMismatchPaths(body, admitted)
			if len(paths) != admissionDiagnosticLimit || !reflect.DeepEqual(paths, admissionMismatchPaths(body, admitted)) {
				t.Fatal("diagnostic path count or ordering is unbounded or unstable")
			}
			for _, path := range paths {
				if len(path) > admissionDiagnosticPathLimit || strings.Contains(path, "PRIVATE_") {
					t.Fatal("diagnostic path length or content is unsafe")
				}
			}
			for _, unsupported := range []any{math.Inf(1), make(chan int)} {
				body, admitted = diagnosticAdmissionPair(t, environment)
				diagnosticContainer(admitted)["value"] = unsupported
				if matchesAdmission(body, admitted) || len(admissionMismatchPaths(body, admitted)) != 0 || admissionMismatchCause("rejected", body, admitted) != "rejected; mismatch paths unavailable" {
					t.Fatal("unsupported JSON did not keep a generic private-safe rejection")
				}
			}
			body, admitted = diagnosticAdmissionPair(t, environment)
			branch := Map(body["spec"])
			for index := 0; index < 40; index++ {
				next := Object{}
				branch["template"] = next
				branch = next
			}
			branch["image"] = "PRIVATE_VALUE"
			admitted = Clone(body)
			branch = Map(admitted["spec"])
			for index := 0; index < 40; index++ {
				branch = Map(branch["template"])
			}
			branch["image"] = "PRIVATE_CHANGED"
			if len(admissionMismatchPaths(body, admitted)) != 0 {
				t.Fatal("deep unsupported diagnostic should return a bounded generic result")
			}
		})
	}
}

func TestAdmissionDiagnosticStopsAtBudgetBeforeLargeMatchingPrefix(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, dynamic := range []bool{false, true} {
			t.Run(environment+map[bool]string{false: "/env-array", true: "/private-map"}[dynamic], func(t *testing.T) {
				body, _ := diagnosticAdmissionPair(t, environment)
				// The matching prefix fits the immediate array/map width bound,
				// but exceeds the total node budget before the final mismatch.
				length := admissionDiagnosticNodeLimit / 2
				values := make([]any, length)
				for index := range values {
					values[index] = Object{"name": "PRIVATE_NAME", "value": "PRIVATE_VALUE"}
				}
				if dynamic {
					body["data"] = Object{"PRIVATE_KEY": values}
				} else {
					diagnosticContainer(body)["env"] = values
				}
				admitted := Clone(body)
				var changed []any
				if dynamic {
					changed = Map(admitted["data"])["PRIVATE_KEY"].([]any)
				} else {
					changed = diagnosticContainer(admitted)["env"].([]any)
				}
				Map(changed[len(changed)-1])["value"] = "PRIVATE_CHANGED"
				beforeBody, beforeAdmitted := Digest(body), Digest(admitted)
				if matchesAdmission(body, admitted) || len(admissionMismatchPaths(body, admitted)) != 0 || admissionMismatchCause("rejected", body, admitted) != "rejected; mismatch paths unavailable" {
					t.Fatal("diagnostic did not stop conservatively at its shared node budget")
				}
				if Digest(body) != beforeBody || Digest(admitted) != beforeAdmitted || matchesAdmission(body, admitted) {
					t.Fatal("bounded diagnostics altered the exact verdict or original objects")
				}
			})
		}
	}
}

type diagnosticAdmissionBackend struct {
	memoryBackend
	dryCalls int
	stage    bool
}

func (b *diagnosticAdmissionBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	actual, err := b.memoryBackend.Write(ctx, action, body, dry)
	if err != nil || !dry {
		return actual, err
	}
	b.dryCalls++
	actual = Clone(actual)
	if !b.stage || b.dryCalls > 1 {
		delete(Map(diagnosticContainer(actual)["env"].([]any)[0]), "value")
	}
	return actual, nil
}

func TestAdmissionDiagnosticPreservesBlockersPlanAndExecutionFences(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, stage := range []bool{false, true} {
			t.Run(environment+map[bool]string{false: "/preflight", true: "/execution-stage"}[stage], func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				diagnosticContainer(definition.Object)["env"] = []any{Object{"name": "PRIVATE_NAME", "value": ""}}
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				backend := &diagnosticAdmissionBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}}, stage: stage}
				planDigest, liveDigest := plan.SHA256(), Digest(live)
				if stage {
					root := t.TempDir()
					if err := os.Chmod(root, 0700); err != nil {
						t.Fatal(err)
					}
					qualified := func(context.Context, *Plan) error { return nil }
					err = plan.Execute(context.Background(), backend, planDigest, root, qualified, qualified, qualified)
				} else {
					err = plan.Admit(context.Background(), backend)
				}
				var blocker *Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.admission" || blocker.Resource != ID(live) || !strings.Contains(blocker.Cause, "mismatch paths: $.spec.template.spec.containers[0].env[0].value") || strings.Contains(blocker.Cause, "PRIVATE_") {
					t.Fatal("diagnostic did not preserve the blocker and safe exact path", err)
				}
				if plan.SHA256() != planDigest || Digest(live) != liveDigest || Digest(backend.objects[ID(live)]) != liveDigest || len(backend.written) != 0 {
					t.Fatal("diagnostic modified the reviewed plan or persisted rejected admission")
				}
				for _, body := range backend.dry {
					if Map(body["metadata"])["uid"] != "owner" || Map(body["metadata"])["resourceVersion"] != "4" || Map(diagnosticContainer(body)["env"].([]any)[0])["value"] != "" {
						t.Fatal("diagnostic rewrote the original UID/RV-bound request")
					}
				}
				changed := Clone(live)
				Map(changed["metadata"])["uid"] = "replacement"
				if _, err := plan.Resources[0].Body(changed); err == nil {
					t.Fatal("diagnostic weakened original UID fence")
				}
				changed = Clone(live)
				Map(changed["spec"])["replicas"] = float64(99)
				if _, err := plan.Resources[0].Body(changed); err == nil {
					t.Fatal("diagnostic weakened original baseline fence")
				}
				changed = Clone(live)
				Map(changed["metadata"])["resourceVersion"] = "5"
				fresh, err := plan.Resources[0].Body(changed)
				if err != nil || Map(fresh["metadata"])["resourceVersion"] != "5" || plan.SHA256() != planDigest {
					t.Fatal("diagnostic changed fresh RV fencing", err)
				}
			})
		}
	}
}
