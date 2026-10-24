package main

import (
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedIdentityTransitionPreservesLifecycleAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
			for _, mode := range []string{"unchanged", "ordinary-config", "controller-policy", "new-image", "api-defaults", "fresh-create", "missing-target", "retire", "container-removed", "root-pin", "new-root-pin", "state-path", "env-source", "envfrom-source", "key-secret", "crl-configmap", "state-pvc", "ephemeral-state", "mount-path", "readonly-state", "subpath", "duplicate-setting", "duplicate-mount", "duplicate-volume", "init-state-path", "container-moved-to-init", "claim-template", "claim-template-renamed"} {
				t.Run(environment+"/"+kind+"/"+mode, func(t *testing.T) {
					current := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": "certissuer", "namespace": "video-cloud-" + environment + "-video-cloud", "uid": "original-owner"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": []any{
						deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-identity"}},
						deploymentplan.Object{"name": "tls", "secret": deploymentplan.Object{"secretName": "original-tls"}},
						deploymentplan.Object{"name": "crls", "configMap": deploymentplan.Object{"name": "original-crls"}},
					}, "containers": []any{deploymentplan.Object{"name": "certissuer", "image": "old:version", "env": []any{
						deploymentplan.Object{"name": "CERT_ISSUER_HOST_IDENTITY_STATE", "value": "/state/identity.json"},
						deploymentplan.Object{"name": "CERT_ISSUER_SERVICE_PKI_ROOT_FINGERPRINT", "value": "private-canary-pin"},
						deploymentplan.Object{"name": "PKI_ENVIRONMENT", "value": environment},
						deploymentplan.Object{"name": "PKI_CLIENT_ID", "valueFrom": deploymentplan.Object{"fieldRef": deploymentplan.Object{"fieldPath": "metadata.name"}}},
						deploymentplan.Object{"name": "PKI_DATABASE_URL", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "original-database", "key": "url"}}},
						deploymentplan.Object{"name": "PKI_REQUIRED_CONSUMERS_SERVICE", "value": "consumer"},
						deploymentplan.Object{"name": "NORMAL_SETTING", "value": "before"},
					}, "envFrom": []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "original-runtime"}}}, "volumeMounts": []any{
						deploymentplan.Object{"name": "state", "mountPath": "/state"},
						deploymentplan.Object{"name": "tls", "mountPath": "/tls", "readOnly": true},
						deploymentplan.Object{"name": "crls", "mountPath": "/crls", "readOnly": true},
					}}}}}}}
					wanted := deploymentplan.Clone(current)
					pod := environmentPKIPod(wanted)
					container := deploymentplan.Map(pod["containers"].([]any)[0])
					settings := container["env"].([]any)
					volumes := pod["volumes"].([]any)
					mounts := container["volumeMounts"].([]any)
					definition := deploymentplan.Definition{Object: wanted, Phase: "workload"}
					definitions := []deploymentplan.Definition{definition}
					live := []deploymentplan.Object{current}
					switch mode {
					case "ordinary-config":
						deploymentplan.Map(settings[6])["value"] = "after"
						container["resources"] = deploymentplan.Object{"requests": deploymentplan.Object{"cpu": "1"}}
					case "controller-policy":
						deploymentplan.Map(settings[5])["value"] = "consumer,new-consumer"
					case "new-image":
						container["image"] = "candidate@sha256:" + strings.Repeat("a", 64)
					case "api-defaults":
						deploymentplan.Map(deploymentplan.Map(settings[3])["valueFrom"].(map[string]any)["fieldRef"])["apiVersion"] = "v1"
						deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(settings[4])["valueFrom"])["secretKeyRef"])["optional"] = false
						deploymentplan.Map(deploymentplan.Map(container["envFrom"].([]any)[0])["secretRef"])["optional"] = false
						deploymentplan.Map(deploymentplan.Map(volumes[0])["persistentVolumeClaim"])["readOnly"] = false
						deploymentplan.Map(deploymentplan.Map(volumes[1])["secret"])["defaultMode"] = float64(288)
						deploymentplan.Map(deploymentplan.Map(volumes[2])["configMap"])["optional"] = false
					case "fresh-create":
						live = nil
					case "missing-target":
						definitions = nil
					case "retire":
						definitions[0].Retire = true
					case "container-removed":
						pod["containers"] = []any{}
					case "init-state-path":
						initial := deploymentplan.Clone(deploymentplan.Map(environmentPKIPod(current)["containers"].([]any)[0]))
						initial["name"] = "bootstrap"
						environmentPKIPod(current)["initContainers"] = []any{initial}
						changed := deploymentplan.Clone(initial)
						deploymentplan.Map(changed["env"].([]any)[0])["value"] = "/tmp/new-state.json"
						pod["initContainers"] = []any{changed}
					case "container-moved-to-init":
						pod["initContainers"] = pod["containers"]
						pod["containers"] = []any{deploymentplan.Object{"name": "unrelated"}}
					case "claim-template", "claim-template-renamed":
						current["kind"], wanted["kind"] = "StatefulSet", "StatefulSet"
						environmentPKIPod(current)["volumes"] = environmentPKIPod(current)["volumes"].([]any)[1:]
						pod["volumes"] = volumes[1:]
						deploymentplan.Map(current["spec"])["volumeClaimTemplates"] = []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "state"}}}
						name := "state"
						if mode == "claim-template-renamed" {
							name = "replacement"
						}
						deploymentplan.Map(wanted["spec"])["volumeClaimTemplates"] = []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": name}}}
					case "root-pin":
						deploymentplan.Map(settings[1])["value"] = "replacement-pin"
					case "new-root-pin":
						container["env"] = append(settings, deploymentplan.Object{"name": "PKI_ADDITIONAL_ROOT_ID", "value": "another-root"})
					case "state-path":
						deploymentplan.Map(settings[0])["value"] = "/tmp/new-identity.json"
					case "env-source":
						deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(settings[4])["valueFrom"])["secretKeyRef"])["name"] = "replacement-database"
					case "envfrom-source":
						deploymentplan.Map(deploymentplan.Map(container["envFrom"].([]any)[0])["secretRef"])["name"] = "replacement-runtime"
					case "key-secret":
						deploymentplan.Map(deploymentplan.Map(volumes[1])["secret"])["secretName"] = "replacement-key"
					case "crl-configmap":
						deploymentplan.Map(deploymentplan.Map(volumes[2])["configMap"])["name"] = "replacement-crls"
					case "state-pvc":
						deploymentplan.Map(deploymentplan.Map(volumes[0])["persistentVolumeClaim"])["claimName"] = "replacement-state"
					case "ephemeral-state":
						delete(deploymentplan.Map(volumes[0]), "persistentVolumeClaim")
						deploymentplan.Map(volumes[0])["emptyDir"] = deploymentplan.Object{}
					case "mount-path":
						deploymentplan.Map(mounts[0])["mountPath"] = "/another-state"
					case "readonly-state":
						deploymentplan.Map(mounts[0])["readOnly"] = true
					case "subpath":
						deploymentplan.Map(mounts[0])["subPath"] = "replacement"
					case "duplicate-setting":
						container["env"] = append(settings, settings[0])
					case "duplicate-mount":
						container["volumeMounts"] = append(mounts, mounts[0])
					case "duplicate-volume":
						pod["volumes"] = append(volumes, volumes[0])
					}
					before := deploymentplan.Digest(current)
					target := deploymentplan.Digest(wanted)
					err := validateEnvironmentPKITransition(definitions, live)
					allowed := keySet("unchanged", "ordinary-config", "controller-policy", "new-image", "api-defaults", "fresh-create", "claim-template")[mode]
					if (err == nil) != allowed {
						t.Fatal("incorrect lifecycle transition decision", mode, err)
					}
					if err != nil {
						var blocker *deploymentplan.Blocker
						if !errors.As(err, &blocker) || blocker.ID != "deployment.pki.bindings" || blocker.Resource != deploymentplan.ID(current) || blocker.Next == "" || strings.Contains(err.Error(), "private-canary-pin") {
							t.Fatal("transition blocker is not scoped, actionable or sanitized", err)
						}
					}
					if deploymentplan.Digest(current) != before || deploymentplan.Digest(wanted) != target {
						t.Fatal("qualification mutated live or desired identity")
					}
				})
			}
		}
	}
}

func TestCertIssuerAdoptionPreservesServingAndSigningProviderReferences(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, _, release := completeEnvironmentRenderFixture(environment)
			settings := map[string]string{"CERT_ISSUER_SERVER_CERT": "/identity/server.pem", "CERT_ISSUER_SERVER_KEY": "/identity/server-key.pem", "CERT_ISSUER_CLIENT_CA": "/trust/client-ca.pem", "CERT_ISSUER_CA_PROVIDER": "openbao"}
			values := []any{}
			for _, name := range sortedMapKeys(settings) {
				values = append(values, deploymentplan.Object{"name": name, "value": settings[name]})
			}
			current := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "certissuer", "namespace": env["CLOUD_STACK_NAME"] + "-video-cloud", "uid": "original-certissuer"}, "spec": deploymentplan.Object{"replicas": float64(999), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "certissuer", "image": release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, "env": values, "command": []any{"legacy-do-not-import"}}}}}}}
			secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "original-issuer-runtime", "namespace": env["CLOUD_STACK_NAME"] + "-video-cloud", "uid": "original-issuer-runtime-owner"}, "data": deploymentplan.Object{"POSTGRES_PASSWORD": "fixture-password", "device-ca.crt": "fixture-device-ca", "client-ca.crt": "fixture-client-ca"}}
			pod := environmentPKIPod(current)
			pod["volumes"] = []any{deploymentplan.Object{"name": "issuer-runtime", "secret": deploymentplan.Object{"secretName": "original-issuer-runtime"}}}
			deploymentplan.Map(pod["containers"].([]any)[0])["volumeMounts"] = []any{deploymentplan.Object{"name": "issuer-runtime", "mountPath": "/issuer", "readOnly": true}}
			adopted, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{current, secret})
			if err != nil {
				t.Fatal("serving identity references could not be adopted", err)
			}
			wanted, err := renderManagedEnvironmentComponent(env, adopted.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(wanted, adopted, release); err != nil {
				t.Fatal(err)
			}
			if adopted.Pods[0].IssuerRuntimeSecret != "original-issuer-runtime" {
				t.Fatal("adoption lost the original issuer runtime Secret")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: wanted, Phase: "workload"}}, []deploymentplan.Object{current}); err != nil {
				t.Fatal("adoption lost serving or signing provider references", err)
			}
			if deploymentplan.Map(wanted["spec"])["replicas"] == float64(999) || deploymentplan.Digest(deploymentplan.Map(environmentPKIPod(wanted)["containers"].([]any)[0])["command"]) != deploymentplan.Digest([]any{"/app/certissuer"}) {
				t.Fatal("adoption imported old workload behavior")
			}
		})
	}
}
