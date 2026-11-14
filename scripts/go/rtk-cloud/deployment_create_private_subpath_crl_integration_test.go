package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPrivateSubPathRealApplicationCRLAndCallbackRenderer(t *testing.T) {
	for _, env := range []string{"dev", "staging"} {
		t.Run(env, func(t *testing.T) {
			cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, env)
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
			if err != nil {
				t.Fatal(err)
			}
			projected, err := projectEnvironmentCustodyResources(cfg.Values, env, binding, strings.Repeat("b", 64), final)
			if err != nil {
				t.Fatal(err)
			}
			id := "Deployment/" + p.Stack + "-video-cloud/video-cloud-api-pki"
			obj := createRecipeObject(t, projected, id)
			app := managedNamedField(environmentPKIPod(obj)["containers"], "app")
			mount := managedNamedField(app["volumeMounts"], "pki-state")
			if mount["subPath"] != "private" {
				t.Fatal("private Device state mount absent")
			}
			if _, err = environmentDeviceMQTTCallbackClientName(obj, app); err != nil {
				t.Fatal("callback rejected private projection", err)
			}
			if relative, ok := environmentCustodyMemoryRelativeFile(obj, app, "pki-state", "/run/pki-state/device-root-crl.json"); !ok || relative != "device-root-crl.json" {
				t.Fatal("CRL maintenance rejected Device private projection", relative, ok)
			}
			count := 0
			err = initializeFreshApplicationCRLs(t.Context(), cfg, p, release, runtime, service, roots, intermediates, projected, func(_ context.Context, stage string, defs []deploymentplan.Definition) error {
				if stage != "application-initial-crls" {
					t.Fatal("unexpected stage", stage)
				}
				count = len(defs)
				return nil
			})
			if err != nil || count == 0 {
				t.Fatal("CRL initialization rejected projected applications", count, err)
			}
		})
	}
}
