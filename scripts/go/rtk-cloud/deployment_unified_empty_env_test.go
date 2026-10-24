package main

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func emptyEnvWorkload(kind string) deploymentplan.Object {
	settings := []any{
		deploymentplan.Object{"name": "EMPTY", "value": ""},
		deploymentplan.Object{"name": "EMPTY_NULL_SOURCE", "value": "", "valueFrom": nil},
		deploymentplan.Object{"name": "SOURCE_BOUND_EMPTY", "value": "", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "original-private-reference", "key": "material"}}},
		deploymentplan.Object{"name": "NONEMPTY", "value": "qualified"},
		deploymentplan.Object{"name": "SOURCE_ONLY", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "original-private-reference", "key": "material"}}},
		deploymentplan.Object{"name": "ABSENT"},
		deploymentplan.Object{"name": "NULL", "value": nil},
		deploymentplan.Object{"name": "NUMBER", "value": float64(0)},
		deploymentplan.Object{"name": "BOOLEAN", "value": false},
		deploymentplan.Object{"name": "OBJECT", "value": deploymentplan.Object{}},
		deploymentplan.Object{"name": "WHITESPACE", "value": " "},
	}
	container := deploymentplan.Object{"name": "app", "image": "registry.example/owned@sha256:" + strings.Repeat("a", 64), "env": settings, "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "runtime"}}}, "volumeMounts": []any{deploymentplan.Object{"name": "original-state", "mountPath": "/state"}}, "securityContext": deploymentplan.Object{"runAsNonRoot": true}}
	pod := deploymentplan.Object{"containers": []any{deploymentplan.Clone(container)}, "initContainers": []any{deploymentplan.Clone(container)}, "volumes": []any{deploymentplan.Object{"name": "original-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-claim"}}}, "securityContext": deploymentplan.Object{"fsGroup": float64(1001)}}
	spec := deploymentplan.Object{"template": deploymentplan.Object{"spec": pod}}
	api := "apps/v1"
	if kind == "Job" || kind == "CronJob" {
		api = "batch/v1"
	}
	if kind == "CronJob" {
		spec = deploymentplan.Object{"schedule": "0 * * * *", "jobTemplate": deploymentplan.Object{"spec": spec}}
	}
	return deploymentplan.Object{"apiVersion": api, "kind": kind, "metadata": deploymentplan.Object{"name": "owned", "namespace": "video-cloud-dev-video-cloud"}, "spec": spec}
}

func TestCanonicalEnvironmentEmptyEnvLiteralsPreservesAllOtherFields(t *testing.T) {
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"} {
		t.Run(kind, func(t *testing.T) {
			object := emptyEnvWorkload(kind)
			expected := deploymentplan.Clone(object)
			for _, group := range []string{"containers", "initContainers"} {
				container := deploymentplan.Map(certIssuerObjectList(environmentZammadPod(expected)[group])[0])
				for _, index := range []int{0, 1} {
					entry := deploymentplan.Map(certIssuerObjectList(container["env"])[index])
					entry["value"], entry["valueFrom"] = nil, nil
				}
			}
			canonicalEnvironmentEmptyEnvLiterals(object)
			if deploymentplan.Digest(object) != deploymentplan.Digest(expected) {
				t.Fatal("empty literal conversion changed types, source-bound settings, identity, storage or security")
			}
			before := deploymentplan.Digest(object)
			canonicalEnvironmentEmptyEnvLiterals(object)
			if deploymentplan.Digest(object) != before {
				t.Fatal("empty literal conversion is not idempotent")
			}
		})
	}
	for _, kind := range []string{"Service", "ConfigMap", "Secret"} {
		object := emptyEnvWorkload(kind)
		before := deploymentplan.Digest(object)
		canonicalEnvironmentEmptyEnvLiterals(object)
		if deploymentplan.Digest(object) != before {
			t.Fatal("empty literal conversion touched an unrelated resource kind")
		}
	}
	for _, mode := range []string{"missing-pod", "missing-env", "empty-env", "wrong-env-type"} {
		t.Run(mode, func(t *testing.T) {
			object := emptyEnvWorkload("Deployment")
			if mode == "missing-pod" {
				delete(object, "spec")
			} else {
				for _, group := range []string{"containers", "initContainers"} {
					container := deploymentplan.Map(certIssuerObjectList(environmentZammadPod(object)[group])[0])
					switch mode {
					case "missing-env":
						delete(container, "env")
					case "empty-env":
						container["env"] = []any{}
					case "wrong-env-type":
						container["env"] = "unsupported"
					}
				}
			}
			before := deploymentplan.Digest(object)
			canonicalEnvironmentEmptyEnvLiterals(object)
			if deploymentplan.Digest(object) != before {
				t.Fatal("empty literal conversion created a missing list or changed an unrelated type")
			}
		})
	}
}

type emptyEnvAdmissionBackend struct {
	current, request deploymentplan.Object
	requestSHA, mode string
	dry, persisted   int
}

func (b *emptyEnvAdmissionBackend) Read(context.Context, string) (deploymentplan.Object, error) {
	return deploymentplan.Clone(b.current), nil
}
func (b *emptyEnvAdmissionBackend) Wait(context.Context, deploymentplan.Resource) error { return nil }
func (b *emptyEnvAdmissionBackend) Write(_ context.Context, _ string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if !dry {
		b.persisted++
		return nil, errors.New("admission fixture must not persist resources")
	}
	b.dry++
	b.request, b.requestSHA = body, deploymentplan.Digest(body)
	admitted := deploymentplan.Clone(body)
	container := deploymentplan.Map(certIssuerObjectList(environmentZammadPod(admitted)["containers"])[0])
	var empty, private deploymentplan.Object
	for _, value := range certIssuerObjectList(container["env"]) {
		entry := deploymentplan.Map(value)
		if entry["value"] == nil {
			delete(entry, "value")
		}
		if entry["valueFrom"] == nil {
			delete(entry, "valueFrom")
		}
		if entry["name"] == "SERVICE_PUBLIC_HOST" {
			empty = entry
		} else if entry["name"] == "PKI_PRIVATE_REFERENCE" {
			private = entry
		}
	}
	switch b.mode {
	case "nonempty":
		empty["value"] = "credential=unapproved"
	case "injected-source":
		empty["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "credential=unapproved", "key": "material"}}
	case "env-name":
		empty["name"] = "REPLACED_PUBLIC_HOST"
	case "private-reference":
		deploymentplan.Map(deploymentplan.Map(private["valueFrom"])["secretKeyRef"])["name"] = "credential=unapproved"
	case "uid":
		deploymentplan.Map(admitted["metadata"])["uid"] = "replacement-owner"
	}
	return admitted, nil
}

func TestCommonBillingRendererEmptyEnvAdmissionForEveryEnvironment(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-empty-env-render-fixture")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			env["BILLING_REQUEST_CPU"], env["BILLING_REQUEST_MEMORY"] = "250m", "256Mi"
			release.Images["LKE_ZAMMAD_IMAGE"] = environmentReleaseImage{Reference: "ghcr.io/zammad/zammad@sha256:" + strings.Repeat("a", 64)}
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			bindings.Pods = append(bindings.Pods, environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "billing", Name: "billing", Component: "existing-workload", Containers: []environmentContainerBindings{{Name: "app", ImageKey: "LKE_BILLING_IMAGE", Fields: map[string]any{
				"envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "billing-runtime"}}},
				"env":     []any{deploymentplan.Object{"name": "PKI_OPTIONAL_HINT", "value": ""}, deploymentplan.Object{"name": "PKI_PRIVATE_REFERENCE", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "original-private-reference", "key": "material"}}}},
			}}}})
			restore := installAllCredentialEnvironment(env)
			defer restore()
			inputSHA := deploymentplan.Digest([]any{env, bindings, release})
			support := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "zammad-initialization-content-bound", "namespace": bindings.Stack + "-support"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "initialize", "image": release.Images["LKE_ZAMMAD_IMAGE"].Reference, "env": []any{deploymentplan.Object{"name": "SUPPORT_EMPTY_LITERAL", "value": ""}}}}}}}}}
			supportSHA := deploymentplan.Digest(support)
			definitions, err := renderEnvironmentResources(env, bindings, release, []deploymentplan.Definition{support})
			if err != nil {
				t.Fatal("common renderer failed", err)
			}
			if deploymentplan.Digest([]any{env, bindings, release}) != inputSHA || deploymentplan.Digest(support) != supportSHA {
				t.Fatal("canonical conversion mutated observed binding references, selected inputs or content-bound support")
			}
			var billing deploymentplan.Definition
			claims, identities, supportFound := 0, 0, false
			for _, definition := range definitions {
				object := definition.Object
				if deploymentplan.ID(object) == "Deployment/"+bindings.Stack+"-billing/billing" {
					billing = definition
				}
				if object["kind"] == "PersistentVolumeClaim" {
					claims++
				}
				if deploymentplan.ID(object) == deploymentplan.ID(support.Object) {
					supportFound = deploymentplan.Digest(definition) == supportSHA
				}
				for _, container := range certIssuerObjectList(environmentZammadPod(object)["containers"]) {
					for _, setting := range certIssuerObjectList(deploymentplan.Map(container)["env"]) {
						entry := deploymentplan.Map(setting)
						if entry["name"] == "PKI_ENVIRONMENT" && entry["value"] == environment {
							identities++
						}
					}
				}
			}
			if billing.Object == nil || claims == 0 || identities == 0 || !supportFound {
				t.Fatal("common conversion lost the billing target, existing PVC/identity roles or unchanged support Job")
			}
			container := deploymentplan.Map(certIssuerObjectList(environmentZammadPod(billing.Object)["containers"])[0])
			if container["image"] != release.Images["LKE_BILLING_IMAGE"].Reference || deploymentplan.Digest(container["envFrom"]) != deploymentplan.Digest([]any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "billing-runtime"}}}) {
				t.Fatal("empty override changed the selected image or inherited billing runtime source")
			}
			for _, name := range []string{"SERVICE_PUBLIC_HOST", "PKI_OPTIONAL_HINT"} {
				found := false
				for _, setting := range certIssuerObjectList(container["env"]) {
					entry := deploymentplan.Map(setting)
					if entry["name"] != name {
						continue
					}
					value, hasValue := entry["value"]
					source, hasSource := entry["valueFrom"]
					found = hasValue && value == nil && hasSource && source == nil
				}
				if !found {
					t.Fatal("empty override lost its env item or explicit null constraints")
				}
			}
			for _, mode := range []string{"omitted", "nonempty", "injected-source", "env-name", "private-reference", "uid"} {
				t.Run(mode, func(t *testing.T) {
					current := deploymentplan.Clone(billing.Object)
					metadata := deploymentplan.Map(current["metadata"])
					metadata["uid"], metadata["resourceVersion"] = "original-owner", "27"
					deploymentplan.Map(certIssuerObjectList(environmentZammadPod(current)["containers"])[0])["image"] = "registry.example/old-billing@sha256:" + strings.Repeat("b", 64)
					plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: bindings.Stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest(env), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}, []deploymentplan.Definition{billing}, []deploymentplan.Object{current})
					if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "update" {
						t.Fatal("fixture did not prepare an existing Billing update", err)
					}
					planSHA, currentSHA := plan.SHA256(), deploymentplan.Digest(current)
					backend := &emptyEnvAdmissionBackend{current: current, mode: mode}
					err = plan.Admit(context.Background(), backend)
					if (err == nil) != (mode == "omitted") {
						t.Fatal("empty override admission rejected API omission or accepted an unauthorized source/value", err)
					}
					if err != nil {
						var blocker *deploymentplan.Blocker
						if !errors.As(err, &blocker) || blocker.ID != "deployment.admission" || strings.Contains(err.Error(), "credential=") {
							t.Fatal("admission did not preserve its private-safe stable blocker")
						}
					}
					if plan.SHA256() != planSHA || deploymentplan.Digest(current) != currentSHA || deploymentplan.Digest(backend.request) != backend.requestSHA || backend.persisted != 0 || backend.dry != 1 || deploymentplan.Digest([]any{env, bindings, release}) != inputSHA {
						t.Fatal("admission changed reviewed inputs, request or current state, or persisted a resource")
					}
					if deploymentplan.Map(backend.request["metadata"])["uid"] != "original-owner" || deploymentplan.Map(backend.request["metadata"])["resourceVersion"] != "27" {
						t.Fatal("empty override admission lost exact UID/RV preconditions")
					}
				})
			}
		})
	}
}
