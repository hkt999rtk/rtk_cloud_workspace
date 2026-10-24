package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedApplicationServiceCRLRepairAddsOnlyMissingNativeReference(t *testing.T) {
	cfg, p, release, bootstrap, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, bootstrap, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"video-cloud-api", "video-cloud-api-pki", "video-cloud-api-app-pki"} {
		t.Run(name, func(t *testing.T) {
			wanted := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+name)
			original := deploymentplan.Clone(wanted)
			app := managedNamedField(environmentPKIPod(original)["containers"], "app")
			app["env"] = environmentCreateServiceWithoutEnv(app["env"], "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST")
			before := deploymentplan.Digest(original)
			repaired, err := managedApplicationServiceCRLConfigurationRepair(original)
			if err != nil || deploymentplan.Digest(original) != before || deploymentplan.Digest(repaired) != deploymentplan.Digest(wanted) {
				t.Fatal("repair changed more than the missing native public reference", err)
			}
			again, err := managedApplicationServiceCRLConfigurationRepair(repaired)
			if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(repaired) {
				t.Fatal("native public configuration repair is not repeat-safe", err)
			}
		})
	}
	for _, problem := range []string{"foreign-source", "missing-role", "writable-role", "wrong-item", "foreign-value", "credential-ref", "foreign-owner"} {
		t.Run(problem, func(t *testing.T) {
			object := deploymentplan.Clone(createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api"))
			app := managedNamedField(environmentPKIPod(object)["containers"], "app")
			volumes, _ := environmentPKINamedFields(environmentPKIPod(object)["volumes"])
			mounts, _ := environmentPKINamedFields(app["volumeMounts"])
			settings, _ := environmentPKINamedFields(app["env"])
			switch problem {
			case "foreign-source":
				deploymentplan.Map(volumes["account-manager-crls"]["configMap"])["name"] = "other-generation"
			case "missing-role":
				app["volumeMounts"] = environmentCreateServiceWithoutNamed(app["volumeMounts"], "account-manager-crls")
			case "writable-role":
				mounts["account-manager-crls"]["readOnly"] = false
			case "wrong-item":
				mounts["account-manager-crls"]["subPath"] = "another.json"
			case "foreign-value":
				settings["VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST"]["value"] = "/another-generation/crls.json"
			case "credential-ref":
				settings["VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST"]["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "private", "key": "value"}}
			case "foreign-owner":
				deploymentplan.Map(object["metadata"])["name"] = "ordinary-worker"
			}
			before := deploymentplan.Digest(object)
			if repaired, err := managedApplicationServiceCRLConfigurationRepair(object); err == nil || repaired != nil || deploymentplan.Digest(object) != before {
				t.Fatal("changed source escaped the pure scoped repair guard")
			}
		})
	}
}

func TestCreateFinalAPIsSatisfyActualNativeServiceCRLPrefix(t *testing.T) {
	cfg, p, release, bootstrap, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, bootstrap, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]map[string]string{}
	for _, name := range []string{"video-cloud-api", "video-cloud-api-pki", "video-cloud-api-app-pki"} {
		object := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+name)
		cases[name] = createRecipeEnv(managedNamedField(environmentPKIPod(object)["containers"], "app"))
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "public-settings.json")
	raw, _ := json.Marshal(cases)
	if err := os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "repos/rtk_video_cloud")
	virtual := filepath.Join(repo, "internal/apiapp/zz_deployment_renderer_crl_overlay_test.go")
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("isolated native test would shadow a maintained source file")
	}
	physical := filepath.Join(dir, "native_contract_test.go")
	if err := os.WriteFile(physical, []byte(createApplicationNativeCRLContractTest), 0600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	raw, _ = json.Marshal(map[string]any{"Replace": map[string]string{virtual: physical}})
	if err := os.WriteFile(overlay, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin/go"), "test", "-overlay", overlay, "-vet=off", "./internal/apiapp", "-run", "^TestDeploymentRenderedServiceCRLPrefix$", "-count=1")
	command.Dir = repo
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "VIDEO_CLOUD_TEST_DSN=", "RTK_DEPLOYMENT_CRL_FIXTURE="+fixture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual native API CRL constructor rejected renderer settings: %v\n%s", err, output)
	}
	if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
		t.Fatal("native contract test modified the product checkout")
	}
}

const createApplicationNativeCRLContractTest = `package apiapp

import (
 "crypto/tls"
 "encoding/json"
 "net/http"
 "os"
 "strings"
 "testing"
 "video_cloud/internal/config"
 "video_cloud/internal/pkitrust"
)

func TestDeploymentRenderedServiceCRLPrefix(t *testing.T) {
 raw, err := os.ReadFile(os.Getenv("RTK_DEPLOYMENT_CRL_FIXTURE"))
 var cases map[string]map[string]string
 if err != nil || json.Unmarshal(raw, &cases) != nil || len(cases) != 3 { t.Fatal("isolated renderer fixture unavailable") }
 for name, fields := range cases {
  t.Run(name, func(t *testing.T) {
   get := func(key string) string { return fields[key] }
   cfg := config.App{Env: "dev", Auth: config.Auth{DeviceRootTrustControllerURL: get("VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_CONTROLLER_URL"), ControllerIdentityTrust: config.LoadServerHTTPTrust("VIDEO_CLOUD_CONTROLLER_IDENTITY", get)}}
   if err := cfg.Auth.ControllerIdentityTrust.ValidateWithManagedIdentity(cfg.Auth.DeviceRootTrustControllerURL); err != nil { t.Fatal(err) }
   identity := func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &tls.Certificate{}, nil }
   t.Setenv("VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST", "")
   if _, _, err := buildControllerServiceCRLSync(cfg, nil, identity, &http.Client{}); err == nil || !strings.Contains(err.Error(), "managed Service client CRL configuration incomplete") { t.Fatal("historical missing-prefix control did not reproduce the exact startup error", err) }
   manifest := get("VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST")
   if manifest == "" { t.Fatal("renderer omitted the standalone native Service CRL field") }
   t.Setenv("VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST", manifest)
   native := func(key string) string {
    switch key {
    case "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST": return os.Getenv(key)
    case "VIDEO_CLOUD_CONTROLLER_PKI_CONTROLLER_URL": return cfg.Auth.DeviceRootTrustControllerURL
    case "VIDEO_CLOUD_CONTROLLER_MANAGEMENT_CA": return cfg.Auth.ControllerIdentityTrust.CAFile
    default: return ""
    }
   }
   if err := pkitrust.ManagedServiceClientConsumerSettings(native, "VIDEO_CLOUD_CONTROLLER"); err != nil { t.Fatal("actual native public configuration guard refused the corrected renderer", err) }
   // A nil registry deliberately stops the real constructor before any file,
   // database or network activity. Reaching that next guard proves the exact
   // API path accepted its formerly missing CRL configuration.
   if _, _, err := buildControllerServiceCRLSync(cfg, nil, identity, &http.Client{}); err == nil || !strings.Contains(err.Error(), "registry server consumer configuration incomplete") { t.Fatal("corrected native constructor did not pass its public configuration guard", err) }
  })
 }
}
`
