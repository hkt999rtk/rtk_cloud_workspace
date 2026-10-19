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

func TestCreateFinalControllerRetainsFixedOriginalRootAndNativeConfiguration(t *testing.T) {
	public := map[string]map[string]string{}
	for _, environment := range []string{"dev", "staging"} {
		cfg, p, _, bootstrap, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, environment)
		roots["service"], intermediates["service"] = service.Root, service.Intermediate
		definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, bootstrap, service)
		if err != nil {
			t.Fatal(err)
		}
		before := deploymentplan.Digest(definitions)
		final, err := configureEnvironmentCreateFinalIssuer(p, roots, intermediates, definitions)
		if err != nil || deploymentplan.Digest(definitions) != before {
			t.Fatal("final configuration changed original Service evidence", err)
		}
		controller := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/pki-controller")
		settings := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(controller)["containers"])[0]))
		if settings["PKI_FIXED_DEVICE_ROOT_TRUST"] != "true" || settings["PKI_DEVICE_ROOT_ID"] != roots["device"].ID || settings["PKI_DEVICE_ROOT_SHA256"] != roots["device"].RootFingerprint || settings["PKI_DEV_FIXED_DEVICE_ROOT_TRUST"] != "" {
			t.Fatal("final Controller lost its same pinned native Device Root")
		}
		issuer := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/certissuer")
		if createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(issuer)["containers"])[0]))["PKI_FIXED_DEVICE_ROOT_TRUST"] != "" {
			t.Fatal("Controller-only repair changed the unrelated CertIssuer environment")
		}
		if !environmentIdentitySetting("PKI_FIXED_DEVICE_ROOT_TRUST") {
			t.Fatal("normal observed bindings would discard the fixed Root contract")
		}
		public[environment] = settings
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "public-controller-settings.json")
	raw, _ := json.Marshal(public)
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repos/rtk_video_cloud")
	virtual := filepath.Join(repo, "internal/pkicontrollerapp/zz_deployment_fixed_root_overlay_test.go")
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("isolated native fixture shadows a product source")
	}
	physical := filepath.Join(dir, "native_controller_test.go")
	if err := os.WriteFile(physical, []byte(createFinalControllerNativeTest), 0600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	raw, _ = json.Marshal(map[string]any{"Replace": map[string]string{virtual: physical}})
	if err := os.WriteFile(overlay, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin/go"), "test", "-overlay", overlay, "-vet=off", "./internal/pkicontrollerapp", "-run", "^(TestDeploymentRenderedFixedRoot|TestFixedDeviceRootTrustRequiresNonProductionAndPin)$", "-count=1")
	command.Dir = repo
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "VIDEO_CLOUD_TEST_DSN=", "RTK_DEPLOYMENT_CONTROLLER_FIXTURE="+fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual native Controller rejected rendered fixed Root configuration: %v\n%s", err, output)
	}
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("native fixture modified product checkout")
	}
}

const createFinalControllerNativeTest = `package pkicontrollerapp
import (
 "encoding/json"
 "os"
 "testing"
 "video_cloud/internal/pki"
)
func TestDeploymentRenderedFixedRoot(t *testing.T) {
 raw, err := os.ReadFile(os.Getenv("RTK_DEPLOYMENT_CONTROLLER_FIXTURE"))
 var cases map[string]map[string]string
 if err != nil || json.Unmarshal(raw,&cases)!=nil || len(cases)!=2 { t.Fatal("isolated public Controller fixture unavailable") }
 for environment, values := range cases {
  t.Run(environment,func(t *testing.T) {
   controller := &pki.Controller{DeviceRootID:values["PKI_DEVICE_ROOT_ID"],DeviceRootSHA256:values["PKI_DEVICE_ROOT_SHA256"]}
   if err := configureFixedDeviceRootTrust(controller,environment,"false",""); err==nil || err.Error()!="fixed Device Root trust must be true or unset" { t.Fatal("historical false control did not reproduce original startup failure",err) }
   if err := configureFixedDeviceRootTrust(controller,environment,values["PKI_FIXED_DEVICE_ROOT_TRUST"],values["PKI_DEV_FIXED_DEVICE_ROOT_TRUST"]); err!=nil || !controller.FixedDeviceRootTrust || controller.DeviceRootID!=values["PKI_DEVICE_ROOT_ID"] || controller.DeviceRootSHA256!=values["PKI_DEVICE_ROOT_SHA256"] { t.Fatal("native correction changed or refused original fixed Root",err) }
  })
 }
}
`
