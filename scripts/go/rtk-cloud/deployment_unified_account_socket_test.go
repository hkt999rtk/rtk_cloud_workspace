package main

import (
	"encoding/base64"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
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
			app := mqttFoundationPrimary(current)
			app["volumeMounts"] = []any{map[string]any{"name": "original-socket", "mountPath": "/run/account-pki"}}
			app["env"] = mergeEnvironmentNamedFields(app["env"], []any{map[string]any{"name": "APP_CERT_ISSUER_SOCKET", "value": "/run/account-pki/private/controller.sock"}, map[string]any{"name": "APP_CERT_ISSUER_CLIENT_CERT"}, map[string]any{"name": "APP_CERT_ISSUER_CLIENT_KEY"}, map[string]any{"name": "APP_CERT_ISSUER_CA_FILE"}})
			manager, _ := environmentManagedSidecar("pkimanagement")
			manager["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
			manager["env"] = []any{map[string]any{"name": "PKI_MANAGEMENT_SOCKET", "value": "/run/account-pki/private/controller.sock"}}
			manager["volumeMounts"] = []any{map[string]any{"name": "original-socket", "mountPath": "/run/account-pki"}}
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
		})
	}
}
