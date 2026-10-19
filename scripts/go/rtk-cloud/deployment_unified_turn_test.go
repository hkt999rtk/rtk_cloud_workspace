package main

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Observed three-container shape, with only synthetic identities and endpoints.
// The legacy initializer image is deliberately different from the formal image.
func turnDeploymentFixture(environment string, release environmentRelease) deploymentplan.Object {
	security := map[string]any{"runAsUser": 10001, "runAsGroup": 10001, "runAsNonRoot": true}
	mount := func(name, path string, readOnly bool) any {
		return map[string]any{"name": name, "mountPath": path, "readOnly": readOnly}
	}
	setting := func(name, value string) any { return map[string]any{"name": name, "value": value} }
	image := release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "pkiturn", "namespace": "video-cloud-" + environment + "-video-cloud", "uid": "original-turn-owner", "resourceVersion": "1"}, "spec": map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/name": "pkiturn"}}, "template": map[string]any{"spec": map[string]any{
		"volumes": []any{
			map[string]any{"name": "cli-source", "secret": map[string]any{"secretName": "pkiturn-cli", "defaultMode": 288}},
			map[string]any{"name": "ssh-source", "secret": map[string]any{"secretName": "pkiturn-ssh", "defaultMode": 288}},
			map[string]any{"name": "cli-private", "emptyDir": map[string]any{}}, map[string]any{"name": "ssh-private", "emptyDir": map[string]any{}}, map[string]any{"name": "health", "emptyDir": map[string]any{}},
			map[string]any{"name": "pki-state", "persistentVolumeClaim": map[string]any{"claimName": "pkiturn-app-trust"}},
			map[string]any{"name": "pki-app", "configMap": map[string]any{"name": "pki-app-trust"}},
			map[string]any{"name": "pki-management", "secret": map[string]any{"secretName": "pki-app-consumer-pkiturn"}},
		}, "containers": []any{
			map[string]any{"name": "pkiturn", "image": image, "command": []any{"/app/pkiturn", "watch"}, "securityContext": security, "env": []any{
				setting("PKI_ENVIRONMENT", environment), setting("PKI_TURN_DEDICATED_RELAY", "true"), setting("PKI_TURN_CREDENTIAL_TTL", "1m"), setting("PKI_TURN_CLI_ADDR", "127.0.0.1:5766"), setting("PKI_TURN_CLI_PASSWORD_FILE", "/run/pkiturn-cli/password"), setting("PKI_TURN_HEALTH_FILE", "/run/pkiturn-health/health.json"), setting("PKI_TURN_REDIS_ADDR", "redis.video-cloud-"+environment+"-platform.svc.cluster.local:6379"), setting("PKI_TURN_REDIS_PREFIX", "video_cloud:webrtc"), setting("PKI_TURN_APP_BUNDLE_ACK_ENABLED", "false"),
				map[string]any{"name": "PKI_DATABASE_URL", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "pkiturn-database", "key": "dsn"}}},
			}, "volumeMounts": []any{mount("cli-private", "/run/pkiturn-cli", true), mount("health", "/run/pkiturn-health", false), mount("pki-state", "/run/pki-state", false), mount("pki-app", "/run/pki-app", true), mount("pki-management", "/run/pki-management", true)}},
			map[string]any{"name": "coturn-cli-tunnel", "image": image, "command": turnSSHCommand("root@192.0.2.10"), "securityContext": security, "volumeMounts": []any{mount("ssh-private", "/run/pkiturn-ssh", true)}},
		}, "initContainers": []any{map[string]any{"name": "prepare-pkiturn-private", "image": "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64), "command": []any{"/bin/sh", "-ec"}, "args": []any{strings.ReplaceAll(turnPrivatePreparation, "; cp -f ", "; cp ")}, "securityContext": security, "volumeMounts": []any{mount("cli-source", "/source/cli", true), mount("ssh-source", "/source/ssh", true), mount("cli-private", "/private/cli", false), mount("ssh-private", "/private/ssh", false), mount("pki-state", "/run/pki-state", false)}}},
	}}}}
	return deploymentplan.Clone(object)
}

func TestManagedTurnObservationAndSharedRenderingPreserveLifecycle(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			original := turnDeploymentFixture(environment, release)
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{original})
			if err != nil || len(observed.Pods) != 1 || len(observed.Pods[0].InitContainers) != 1 {
				t.Fatal("complete existing TURN lifecycle was not observed", err)
			}
			bindings.Pods = append(bindings.Pods, observed.Pods...)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			store := makeIsolatedTestSecretStore(t, environment)
			err = withEnvironmentRenderInputs(deploymentConfig{Environment: environment, Values: env}, store, release, environmentDeploymentSnapshot{Secrets: map[string]string{"emqx-dashboard-password": "isolated-dashboard", "mqtt-broker-auth": "isolated-callback", "emqx-handoff-api-key": "isolated-api-key", "emqx-handoff-api-secret": "isolated-api-secret", "payment-reference-encryption": base64.StdEncoding.EncodeToString(make([]byte, 32))}}, func(values map[string]string) error {
				definitions, err := renderEnvironmentResources(values, bindings, release)
				if err != nil {
					return err
				}
				var target deploymentplan.Object
				for _, definition := range definitions {
					if deploymentplan.ID(definition.Object) == deploymentplan.ID(original) {
						target = definition.Object
					}
					if definition.Object["kind"] == "Service" && deploymentplan.Map(definition.Object["metadata"])["name"] == "pkiturn" {
						t.Fatal("loopback CLI exposed through a Service")
					}
				}
				if target == nil {
					return errors.New("existing TURN consumer omitted from shared target")
				}
				if err := validateEnvironmentPKITransition(definitions, []deploymentplan.Object{original}); err != nil {
					return err
				}
				containers, err := environmentPKIPodContainers(environmentPKIPod(target))
				if err != nil || len(containers) != 3 {
					t.Fatal("target lost a TURN container", err)
				}
				for _, container := range containers {
					if container["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || deploymentplan.Map(container["securityContext"])["runAsUser"] != float64(10001) {
						t.Fatal("TURN image role or private-file owner changed")
					}
				}
				if deploymentplan.Digest(containers["containers/coturn-cli-tunnel"]["command"]) != deploymentplan.Digest(turnSSHCommand("root@192.0.2.10")) || deploymentplan.Digest(containers["containers/pkiturn"]["readinessProbe"]) == deploymentplan.Digest(nil) {
					t.Fatal("restricted forwarding or health probe omitted")
				}
				if deploymentplan.Digest(environmentPKIPod(target)["volumes"]) != deploymentplan.Digest(environmentPKIPod(original)["volumes"]) || deploymentplan.Digest(deploymentplan.Map(target["spec"])["selector"]) != deploymentplan.Digest(deploymentplan.Map(original["spec"])["selector"]) {
					t.Fatal("TURN references or immutable selector changed")
				}
				// The executable plan retains the exact original dependency
				// payloads/owners; missing originals cannot generate replacements.
				var dependencies []deploymentplan.Object
				for _, reference := range environmentResourceReferences(target) {
					dependency := deploymentplan.Object{"apiVersion": "v1", "kind": reference.Kind, "metadata": map[string]any{"name": reference.Name, "namespace": reference.Namespace, "uid": "original-" + reference.Name, "resourceVersion": "1"}}
					if reference.Kind == "Secret" || reference.Kind == "ConfigMap" {
						dependency["data"] = map[string]any{"original": "synthetic-original-payload"}
					}
					dependencies = append(dependencies, dependency)
					observed.Owners[reference.ID] = "original-" + reference.Name
				}
				bound, err := bindRetainedEnvironmentDependencies([]deploymentplan.Definition{{Object: target, Phase: "workload"}}, dependencies, observed)
				if err != nil {
					return err
				}
				bound = append(bound, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "video-cloud-" + environment + "-video-cloud"}}, Phase: "namespace"})
				plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: observed.Stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest(values), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(observed)}, bound, append(append([]deploymentplan.Object{}, dependencies...), original), observed.Owners)
				if err != nil {
					return err
				}
				for _, dependency := range dependencies {
					found := false
					for _, resource := range plan.Resources {
						if resource.ID == deploymentplan.ID(dependency) {
							found = resource.Action == "retain" && resource.UID == observed.Owners[resource.ID] && deploymentplan.Digest(resource.Baseline) == deploymentplan.Digest(deploymentplan.Projection(dependency))
						}
					}
					if !found {
						t.Fatal("original TURN dependency payload/owner was not retained")
					}
				}
				if _, err := bindRetainedEnvironmentDependencies([]deploymentplan.Definition{{Object: target, Phase: "workload"}}, dependencies[1:], observed); err == nil {
					t.Fatal("missing original TURN dependency was fabricated")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedTurnRejectsUnqualifiedShapeWithoutPrivatePayloads(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"unknown-container", "missing-init", "weakened-host-pins", "different-forwarding", "unreviewed-init", "unknown-image", "root-user", "credential-remap", "missing-state", "wrong-image-role"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				_, _, release := completeEnvironmentRenderFixture(environment)
				object := turnDeploymentFixture(environment, release)
				pod := environmentPKIPod(object)
				main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				tunnel := deploymentplan.Map(certIssuerObjectList(pod["containers"])[1])
				init := deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])
				switch mode {
				case "unknown-container":
					tunnel["name"] = "unreviewed-sidecar"
				case "missing-init":
					delete(pod, "initContainers")
				case "weakened-host-pins":
					command := certIssuerObjectList(tunnel["command"])
					for i, value := range command {
						if value == "StrictHostKeyChecking=yes" {
							command[i] = "StrictHostKeyChecking=no"
						}
					}
				case "different-forwarding":
					command := certIssuerObjectList(tunnel["command"])
					command[len(command)-2] = "0.0.0.0:5766:127.0.0.1:5766"
				case "unreviewed-init":
					init["args"] = []any{"echo private-error-marker"}
				case "unknown-image":
					init["image"] = "unreviewed.example/image:latest"
				case "root-user":
					deploymentplan.Map(init["securityContext"])["runAsUser"] = float64(0)
				case "credential-remap":
					deploymentplan.Map(certIssuerObjectList(main["volumeMounts"])[0])["mountPath"] = "/private-error-marker"
				case "missing-state":
					delete(deploymentplan.Map(certIssuerObjectList(pod["volumes"])[5]), "persistentVolumeClaim")
				}
				observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{object})
				if mode == "wrong-image-role" && err == nil {
					observed.Pods[0].InitContainers[0].ImageKey = "LKE_ALPINE_IMAGE"
					err = validateEnvironmentIdentityBindings(observed, environment)
				}
				if err == nil || strings.Contains(err.Error(), "private-error-marker") {
					t.Fatal("unsupported TURN profile accepted or private payload leaked", err)
				}
			})
		}
	}
}

func TestTurnPrivatePreparationCopiesSecretsAndPreservesTrustOnRetry(t *testing.T) {
	root := t.TempDir()
	for path, data := range map[string]string{"source/cli/password": "synthetic-cli", "source/ssh/id_ed25519": "synthetic-key", "source/ssh/known_hosts": "synthetic-public-pin", "state/app/revocations.json": "original-revocation-state"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("/source/", root+"/source/", "/private/", root+"/private/", "/run/pki-state/", root+"/state/").Replace(turnPrivatePreparation)
	for i := 0; i < 2; i++ {
		if err := exec.Command("/bin/sh", "-ec", script).Run(); err != nil {
			t.Fatal("private file preparation failed", err)
		}
		for path, mode := range map[string]os.FileMode{"private/cli/password": 0400, "private/ssh/id_ed25519": 0400, "private/ssh/known_hosts": 0444, "state/app/revocations.json": 0600} {
			info, err := os.Stat(filepath.Join(root, path))
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal("runtime private-file permissions were not enforced", err)
			}
		}
		if raw, err := os.ReadFile(filepath.Join(root, "state/app/revocations.json")); err != nil || string(raw) != "original-revocation-state" {
			t.Fatal("initialization reset original revocation state")
		}
	}
}
