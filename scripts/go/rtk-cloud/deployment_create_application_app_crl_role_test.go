package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedApplicationAppCRLRoleRepairPreservesNativeTrust(t *testing.T) {
	cfg, p, release, bootstrap, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, bootstrap, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	wanted := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-app-pki")
	control := deploymentplan.Clone(wanted)
	app := managedNamedField(environmentPKIPod(control)["containers"], "app")
	fields, _ := environmentPKINamedFields(app["env"])
	fields["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"]["value"] = "true"
	before := deploymentplan.Digest(control)
	repaired, err := managedApplicationAppCRLRoleRepair(control)
	if err != nil || deploymentplan.Digest(control) != before || deploymentplan.Digest(repaired) != deploymentplan.Digest(wanted) {
		t.Fatal("App role repair changed more than its Device-only flag", err)
	}
	again, err := managedApplicationAppCRLRoleRepair(repaired)
	if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(repaired) {
		t.Fatal("App role repair is not repeat-safe", err)
	}
	device := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki")
	if createRecipeEnv(managedNamedField(environmentPKIPod(device)["containers"], "app"))["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"] != "true" {
		t.Fatal("App-only correction weakened the Device CRL requirement")
	}
	for _, problem := range []string{"wrong-owner", "product-enabled", "app-disabled", "mtls-disabled", "wrong-manifest", "wrong-source", "writable-trust", "foreign-policy", "foreign-claim", "unknown-flag", "flag-reference"} {
		t.Run(problem, func(t *testing.T) {
			object := deploymentplan.Clone(control)
			container := managedNamedField(environmentPKIPod(object)["containers"], "app")
			settings, _ := environmentPKINamedFields(container["env"])
			volumes, _ := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
			mounts, _ := environmentPKINamedFields(container["volumeMounts"])
			switch problem {
			case "wrong-owner":
				deploymentplan.Map(object["metadata"])["name"] = "video-cloud-api-pki"
			case "product-enabled":
				settings["VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"]["value"] = "true"
			case "app-disabled":
				settings["VIDEO_CLOUD_AUTH_APP_PKI_ENABLED"]["value"] = "false"
			case "mtls-disabled":
				settings["VIDEO_CLOUD_AUTH_MTLS_REQUIRED"]["value"] = "false"
			case "wrong-manifest":
				settings["VIDEO_CLOUD_AUTH_CRL_MANIFEST"]["value"] = "/another/crls.json"
			case "wrong-source":
				deploymentplan.Map(volumes["pki-app"]["configMap"])["name"] = "another-generation"
			case "writable-trust":
				mounts["pki-app"]["readOnly"] = false
			case "foreign-policy":
				settings["VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_STATE"]["value"] = "/another/policy.json"
			case "foreign-claim":
				deploymentplan.Map(volumes["pki-state"]["persistentVolumeClaim"])["claimName"] = "another-generation"
			case "unknown-flag":
				settings["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"]["value"] = "unknown"
			case "flag-reference":
				settings["VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS"]["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "private", "key": "flag"}}
			}
			before := deploymentplan.Digest(object)
			if result, err := managedApplicationAppCRLRoleRepair(object); err == nil || result != nil || deploymentplan.Digest(object) != before {
				t.Fatal("changed App trust source escaped the one-flag repair guard")
			}
		})
	}
}

func TestCreateFinalPKIRolesPassActualNativeAPIValidation(t *testing.T) {
	cfg, p, release, bootstrap, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, bootstrap, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]string{}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		object := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+name)
		cases[name] = createRecipeEnv(managedNamedField(environmentPKIPod(object)["containers"], "app"))
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "public-role-settings.json")
	raw, _ := json.Marshal(cases)
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repos/rtk_video_cloud")
	virtual := filepath.Join(repo, "internal/config/zz_deployment_renderer_pki_role_overlay_test.go")
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("native fixture would shadow a maintained source")
	}
	physical := filepath.Join(dir, "native_role_test.go")
	if err := os.WriteFile(physical, []byte(createApplicationNativePKIRoleTest), 0600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	raw, _ = json.Marshal(map[string]any{"Replace": map[string]string{virtual: physical}})
	if err := os.WriteFile(overlay, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin/go"), "test", "-overlay", overlay, "-vet=off", "./internal/config", "-run", "^TestDeploymentRenderedPKIRoleAPIValidation$", "-count=1")
	command.Dir = repo
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "VIDEO_CLOUD_TEST_DSN=", "RTK_DEPLOYMENT_PKI_ROLE_FIXTURE="+fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual native API validation rejected role-specific CRL configuration: %v\n%s", err, output)
	}
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("native fixture modified the product checkout")
	}
}

const createApplicationNativePKIRoleTest = `package config
import (
 "encoding/json"
 "os"
 "strings"
 "testing"
)
func TestDeploymentRenderedPKIRoleAPIValidation(t *testing.T) {
 raw, err := os.ReadFile(os.Getenv("RTK_DEPLOYMENT_PKI_ROLE_FIXTURE"))
 var cases map[string]map[string]string
 if err != nil || json.Unmarshal(raw, &cases) != nil || len(cases) != 2 { t.Fatal("isolated native role fixture unavailable") }
 for name, fields := range cases {
  t.Run(name, func(t *testing.T) {
   for key, value := range fields { t.Setenv(key, value) }
   // The fixture does not resolve envFrom Secret values. Supply only the
   // isolated existing-runtime broker key needed by the Device callback.
   t.Setenv("VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY", "isolated-renderer-runtime-broker-key")
   cfg := Load()
   app := name == "video-cloud-api-app-pki"
   if cfg.Auth.ProductPKIRequireCRLs == app || cfg.Auth.ProductPKIEnabled == app || cfg.Auth.AppPKIEnabled != app || !cfg.Auth.MTLSRequired || cfg.Auth.CRLManifest == "" { t.Fatal("renderer lost the exact selected incoming PKI/CRL role") }
   if err := cfg.ValidateAPI(); err != nil { t.Fatal("actual config.Load/ValidateAPI refused finalized incoming trust", err) }
   if app {
    t.Setenv("VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS", "true")
    if err := Load().ValidateAPI(); err == nil || !strings.Contains(err.Error(), "mandatory device CRLs require Product PKI") { t.Fatal("historical App flag control did not reproduce the actual refusal", err) }
   }
  })
 }
}
`
