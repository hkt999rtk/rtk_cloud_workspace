package main

import (
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestDevicePrivateMemoryPlacementAcceptsOnlyOriginalProjection(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	defs, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	defs, err = projectEnvironmentCustodyResources(cfg.Values, "dev", binding, strings.Repeat("b", 64), defs)
	if err != nil {
		t.Fatal(err)
	}
	original := createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki")
	const file = "/run/pki-state/device-root-crl.json"
	for _, tc := range []struct {
		name   string
		mutate func(deploymentplan.Object)
	}{
		{"valid", func(deploymentplan.Object) {}},
		{"foreign subpath", func(o deploymentplan.Object) {
			pod := environmentPKIPod(o)
			c := managedNamedField(pod["containers"], "app")
			managedNamedField(c["volumeMounts"], "pki-state")["subPath"] = "other"
		}},
		{"foreign init command", func(o deploymentplan.Object) {
			pod := environmentPKIPod(o)
			managedNamedField(pod["initContainers"], "initialize-trust-state")["command"] = []any{"/bin/sh", "-ec", "true"}
		}},
		{"foreign init mount", func(o deploymentplan.Object) {
			pod := environmentPKIPod(o)
			init := managedNamedField(pod["initContainers"], "initialize-trust-state")
			managedNamedField(init["volumeMounts"], "pki-state")["subPath"] = "private"
		}},
		{"different recorded state", func(o deploymentplan.Object) {
			pod := environmentPKIPod(o)
			c := managedNamedField(pod["containers"], "app")
			for _, raw := range certIssuerObjectList(c["env"]) {
				v := deploymentplan.Map(raw)
				if v["name"] == "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE" {
					v["value"] = "/run/pki-state/root-trust-11111111-2222-3333-4444-555555555555.json"
				}
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := deploymentplan.Clone(original)
			tc.mutate(o)
			c := managedNamedField(environmentPKIPod(o)["containers"], "app")
			relative, ok := environmentCustodyMemoryRelativeFile(o, c, "pki-state", file)
			var observed liveDeployment
			raw, err := json.Marshal(o)
			if err != nil || json.Unmarshal(raw, &observed) != nil {
				t.Fatal("invalid observed workload", err)
			}
			if livePKIStateOnStorage(observed, "app", file) != (tc.name == "valid") {
				t.Error("observed storage must retain native initializer, security context and private mount validation")
			}
			if tc.name == "valid" {
				if !ok || relative != "device-root-crl.json" {
					t.Fatal("valid projection rejected", relative, ok)
				}
			} else if ok {
				t.Fatal("invalid projection accepted", relative)
			}
		})
	}
}
