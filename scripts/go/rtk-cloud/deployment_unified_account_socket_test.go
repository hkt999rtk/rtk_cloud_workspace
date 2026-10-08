package main

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestAccountManagerSocketRetainsIdentityWithoutInventingClientSecret(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-account-socket")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "original-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			base := mqttFoundationTarget(definitions, "Deployment", "account-manager")
			current := deploymentplan.Clone(base)
			deploymentplan.Map(current["metadata"])["uid"] = "original-account"
			pod := environmentPKIPod(current)
			var volumes []any
			for _, v := range certIssuerObjectList(pod["volumes"]) {
				if deploymentplan.Map(v)["name"] != "account-manager-certissuer-client" {
					volumes = append(volumes, v)
				}
			}
			pod["volumes"] = append(volumes, map[string]any{"name": "original-socket", "emptyDir": map[string]any{}})
			pod["securityContext"] = map[string]any{"fsGroup": float64(10001), "runAsGroup": float64(10001), "runAsUser": float64(10001), "fsGroupChangePolicy": "OnRootMismatch"}
			pod["initContainers"] = []any{map[string]any{"name": "management-socket-directory", "image": "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64), "command": []any{"sh", "-c", accountManagerSocketPreparation}, "volumeMounts": []any{map[string]any{"name": "original-socket", "mountPath": "/socket"}}}}
			app := mqttFoundationPrimary(current)
			app["volumeMounts"] = []any{map[string]any{"name": "original-socket", "mountPath": "/run/account-pki"}}
			app["env"] = mergeEnvironmentNamedFields(app["env"], []any{map[string]any{"name": "APP_CERT_ISSUER_SOCKET", "value": "/run/account-pki/private/controller.sock"}, map[string]any{"name": "APP_CERT_ISSUER_CLIENT_CERT"}, map[string]any{"name": "APP_CERT_ISSUER_CLIENT_KEY"}, map[string]any{"name": "APP_CERT_ISSUER_CA_FILE"}})
			manager, _ := environmentManagedSidecar("pkimanagement")
			manager["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
			manager["env"] = []any{map[string]any{"name": "PKI_MANAGEMENT_SOCKET", "value": "/run/account-pki/private/controller.sock"}}
			manager["volumeMounts"] = []any{map[string]any{"name": "original-socket", "mountPath": "/run/account-pki"}}
			manager["securityContext"] = map[string]any{"runAsUser": float64(10001), "runAsGroup": float64(10001), "allowPrivilegeEscalation": false}
			manager["env"] = append(certIssuerObjectList(manager["env"]), map[string]any{"name": "PKI_MANAGEMENT_IDENTITY_STATE", "value": "/identity/private/identity.json"})
			manager["volumeMounts"] = append(certIssuerObjectList(manager["volumeMounts"]), map[string]any{"name": "original-identity", "mountPath": "/identity"})
			pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), map[string]any{"name": "original-identity", "persistentVolumeClaim": map[string]any{"claimName": "original-managed-identity"}})
			pod["containers"] = append(certIssuerObjectList(pod["containers"]), manager)
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{current})
			if err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentIdentityBindings(observed, environment); err != nil {
				t.Fatal(err)
			}
			target := deploymentplan.Clone(base)
			if err := applyEnvironmentPodBindings(target, observed, release); err != nil {
				t.Fatal(err)
			}
			for _, ref := range environmentResourceReferences(target) {
				if ref.Name == "account-manager-certissuer-client" {
					t.Fatal("invented static client dependency")
				}
			}
			if mqttFoundationEnv(mqttFoundationPrimary(target), "APP_CERT_ISSUER_SOCKET") != "/run/account-pki/private/controller.sock" {
				t.Fatal("original socket lost")
			}
			if deploymentplan.Map(deploymentplan.Map(target["spec"])["strategy"])["type"] != "Recreate" {
				t.Fatal("concurrent identity writers allowed")
			}
			wantedPod := environmentPKIPod(target)
			initializer := deploymentplan.Map(certIssuerObjectList(wantedPod["initContainers"])[0])
			if initializer["image"] != release.Images["LKE_ALPINE_IMAGE"].Reference || deploymentplan.Digest(initializer["command"]) != deploymentplan.Digest([]any{"/bin/sh", "-ec", accountManagerSocketPreparation}) || deploymentplan.Digest(initializer["volumeMounts"]) != deploymentplan.Digest(deploymentplan.Map(certIssuerObjectList(pod["initContainers"])[0])["volumeMounts"]) {
				t.Fatal("socket directory lost its maintained recipe, original mount or selected provider role")
			}
			if deploymentplan.Digest(wantedPod["securityContext"]) != deploymentplan.Digest(pod["securityContext"]) || deploymentplan.Digest(managedNamedField(wantedPod["containers"], "pkimanagement")["securityContext"]) != deploymentplan.Digest(manager["securityContext"]) {
				t.Fatal("socket owner UID or security context changed")
			}
			if deploymentplan.Map(managedNamedField(wantedPod["volumes"], "original-identity")["persistentVolumeClaim"])["claimName"] != "original-managed-identity" {
				t.Fatal("original private identity state lost")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{current}); err != nil {
				t.Fatal(err)
			}
			bad := deploymentplan.Clone(current)
			mqttFoundationPrimary(bad)["env"] = mergeEnvironmentNamedFields(mqttFoundationPrimary(bad)["env"], []any{map[string]any{"name": "APP_CERT_ISSUER_SOCKET", "value": "/run/foreign/socket"}})
			mismatched, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{bad})
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(deploymentplan.Clone(base), mismatched, release); err == nil {
				t.Fatal("unmatched socket accepted")
			}
			for _, mode := range []string{"unknown-name", "unknown-recipe", "unknown-image", "missing-initializer", "wrong-role", "wrong-mount", "subpath", "missing-provider-image"} {
				t.Run(mode, func(t *testing.T) {
					bad := deploymentplan.Clone(current)
					init := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(bad)["initContainers"])[0])
					switch mode {
					case "unknown-name":
						init["name"] = "unmaintained-initializer"
					case "unknown-recipe":
						init["command"] = []any{"/bin/sh", "-ec", "rm -rf /identity; private-canary"}
					case "unknown-image":
						init["image"] = "unknown.example/private@sha256:" + strings.Repeat("d", 64)
					case "missing-initializer":
						delete(environmentPKIPod(bad), "initContainers")
					case "wrong-mount":
						deploymentplan.Map(certIssuerObjectList(init["volumeMounts"])[0])["mountPath"] = "/different"
					case "subpath":
						deploymentplan.Map(certIssuerObjectList(init["volumeMounts"])[0])["subPath"] = "other"
					}
					observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{bad})
					if err == nil {
						candidate := release
						if mode == "wrong-role" {
							observed.Pods[0].InitContainers[0].ImageKey = "LKE_VIDEO_CLOUD_IMAGE"
						}
						if mode == "missing-provider-image" {
							candidate.Images = map[string]environmentReleaseImage{}
						}
						err = applyEnvironmentPodBindings(deploymentplan.Clone(base), observed, candidate)
					}
					var blocker *deploymentplan.Blocker
					if err == nil || !errors.As(err, &blocker) || blocker.ID != "deployment.inventory.account-initializer" || strings.Contains(err.Error(), "private-canary") {
						t.Fatal("unsupported Account Manager initializer was accepted or exposed private input", err)
					}
				})
			}
		})
	}
}

func TestAccountManagerSocketPreparationPreservesExistingContentsOnRetry(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(private, "existing-state.json")
	if err := os.WriteFile(state, []byte("original-private-state"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := exec.Command("/bin/sh", "-ec", strings.ReplaceAll(accountManagerSocketPreparation, "/socket", root)).Run(); err != nil {
			t.Fatal("isolated socket-directory preparation failed", err)
		}
	}
	raw, err := os.ReadFile(state)
	info, statErr := os.Stat(private)
	if err != nil || string(raw) != "original-private-state" || statErr != nil || info.Mode().Perm() != 0700 {
		t.Fatal("retry replaced state or lost private socket-directory permissions")
	}
}
