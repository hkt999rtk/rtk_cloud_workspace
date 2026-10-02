package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func clearDNSNamingOverrides(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LKE_DEVICE_DOMAIN", "LKE_FRONTEND_DOMAIN", "BILLING_DOMAIN", "PAYMENT_SIMULATOR_DOMAIN",
		"LKE_TURN_REGISTRY_PUBLIC_DOMAIN", "LKE_COTURN_DOMAIN", "LKE_COTURN_DOMAIN_PREFIX", "LKE_COTURN_VM_COUNT", "LKE_COTURN_VM_INDEX",
	} {
		t.Setenv(key, "")
	}
}

func TestDeploymentDNSIdentityMatchesRuntimeForNamedEnvironments(t *testing.T) {
	clearDNSNamingOverrides(t)
	for _, environment := range []string{"dev", "staging", "prod", "qa", "stg", "production"} {
		t.Run(environment, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, environment, "lke")
			cfg, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := materializeDeploymentRuntime(cfg); err != nil {
				t.Fatal(err)
			}
			runtime, err := envroot.Load(cfg.RuntimeRoot, "")
			if err != nil {
				t.Fatal(err)
			}
			base := "video-cloud-" + environment + ".example.test"
			if runtime.Values["VIDEO_CLOUD_DOMAIN"] != base || runtime.Values["VIDEO_CLOUD_BASE_URL"] != "https://"+base || runtime.Values["VIDEO_CLOUD_MQTT_ADDR"] != base+":8883" {
				t.Fatalf("runtime lost selected environment: %#v", runtime.Values)
			}
			if runtime.Values["VIDEO_CLOUD_MTLS_BASE_URL"] != "https://device."+base {
				t.Fatalf("mTLS URL does not match selected namespace: %s", runtime.Values["VIDEO_CLOUD_MTLS_BASE_URL"])
			}
		})
	}
}

func TestDeploymentDNSRejectsInvalidEnvironmentAndStackBeforeResolution(t *testing.T) {
	for _, environment := range []string{"../prod", "Staging", "qa_blue", "qa-", "-qa", strings.Repeat("a", 52)} {
		t.Run(environment, func(t *testing.T) {
			_, err := resolveDeploymentConfig(t.TempDir(), environment, "")
			if err == nil || !strings.Contains(err.Error(), "invalid DNS environment name") {
				t.Fatalf("expected identity failure before reading config, got %v", err)
			}
		})
	}
	for _, stack := range []string{"video-cloud-stg", "customer-stack", "coverage-123-1"} {
		t.Run(stack, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "staging", "lke")
			writeTestFile(t, filepath.Join(workspace, "cloud_env", "staging", "environment.env"), "CLOUD_STACK_NAME="+stack+"\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=us-west\n")
			_, err := resolveDeploymentConfig(workspace, "staging", "")
			if err == nil || !strings.Contains(err.Error(), "CLOUD_STACK_NAME must be video-cloud-staging") {
				t.Fatalf("normal stack override accepted: %v", err)
			}
		})
	}
}

func TestDeploymentDNSCoverageStackRemainsScopedToRuntime(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "staging", "environment.env"), "CLOUD_RUNTIME_COVERAGE_STACK=coverage-123-1\n")
	if _, err := resolveDeploymentConfig(workspace, "staging", ""); err == nil || !strings.Contains(err.Error(), "unknown environment key CLOUD_RUNTIME_COVERAGE_STACK") {
		t.Fatalf("coverage marker accepted in normal tracked config: %v", err)
	}
	runtimeRoot := filepath.Join(t.TempDir(), "staging", "runtime")
	writeTestFile(t, filepath.Join(runtimeRoot, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_PROVIDER=lke\nCLOUD_REGION=us-sea\nCLOUD_DNS_ROOT_DOMAIN=example.test\nCLOUD_RUNTIME_COVERAGE_STACK=coverage-123-1\nCLOUD_STACK_NAME=coverage-123-1\n")
	runtime, err := envroot.Load(runtimeRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Values["VIDEO_CLOUD_DOMAIN"] != "coverage-123-1.example.test" {
		t.Fatalf("coverage namespace was overwritten: %s", runtime.Values["VIDEO_CLOUD_DOMAIN"])
	}
}

func TestDeploymentDNSPlanIncludesEffectivePublicRolesAndBrandOverride(t *testing.T) {
	clearDNSNamingOverrides(t)
	workspace := writeDeploymentFixture(t, "prod", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "prod", "environment.env"), "FRONTEND_DOMAIN=www.example.test\nPUBLIC_BASE_URL=https://www.example.test\nSENDMAIL_HTTP_BASE_URL=https://mail.external.test\n")
	cfg, err := resolveDeploymentConfig(workspace, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"video-cloud-prod.example.test", "device.video-cloud-prod.example.test", "certissuer.video-cloud-prod.example.test",
		"turnregistry.video-cloud-prod.example.test", "account-manager.video-cloud-prod.example.test", "admin.video-cloud-prod.example.test",
		"www.example.test", "logger.video-cloud-prod.example.test", "billing.video-cloud-prod.example.test",
		"payment-simulator.video-cloud-prod.example.test", "turn.video-cloud-prod.example.test",
	}
	assertDNSPlanNames(t, buildGenericDNSPlan(cfg), want)
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	values, err := readStrictEnv(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if lkeFrontendPublicDomain(values) != "www.example.test" {
		t.Fatal("production frontend override was lost in runtime")
	}
}

func TestDeploymentDNSProductionBrowserAliasesPreserveBackendNamespace(t *testing.T) {
	clearDNSNamingOverrides(t)
	t.Setenv("SERVICE_LOGIN_URL", "")
	workspace := writeDeploymentFixture(t, "prod", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "prod", "environment.env"), "FRONTEND_DOMAIN=www.example.test\nCONSOLE_DOMAIN=console.example.test\nPUBLIC_BASE_URL=https://www.example.test\n")
	cfg, err := resolveDeploymentConfig(workspace, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	assertDNSPlanNames(t, buildGenericDNSPlan(cfg), []string{
		"video-cloud-prod.example.test", "device.video-cloud-prod.example.test", "certissuer.video-cloud-prod.example.test",
		"turnregistry.video-cloud-prod.example.test", "account-manager.video-cloud-prod.example.test", "console.example.test",
		"www.example.test", "logger.video-cloud-prod.example.test", "billing.video-cloud-prod.example.test",
		"payment-simulator.video-cloud-prod.example.test", "turn.video-cloud-prod.example.test",
	})
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := envroot.Load(cfg.RuntimeRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"CONSOLE_DOMAIN": "console.example.test", "CLOUD_ADMIN_DOMAIN": "console.example.test",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-prod.example.test", "ACCOUNT_MANAGER_DOMAIN": "account-manager.video-cloud-prod.example.test",
		"VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-prod.example.test", "CLOUD_LOGGER_DOMAIN": "logger.video-cloud-prod.example.test",
		"VIDEO_CLOUD_BASE_URL": "https://video-cloud-prod.example.test", "VIDEO_CLOUD_MTLS_BASE_URL": "https://device.video-cloud-prod.example.test",
	} {
		if runtime.Values[key] != want {
			t.Fatalf("browser alias changed %s: %q, want %q", key, runtime.Values[key], want)
		}
	}
	manifest := lkeDeploymentManifest(runtime.Values, lkeWorkload{Key: "frontend", Name: "frontend", Namespace: "video-cloud-prod-frontend", Port: 8080}, nil)
	if !strings.Contains(manifest, "- name: SERVICE_LOGIN_URL\n              value: \"https://console.example.test/login\"") {
		t.Fatalf("frontend login does not use Console alias:\n%s", manifest)
	}
	stackFile := filepath.Join(cfg.RuntimeRoot, "env", "stack.env")
	body := readTestFile(t, stackFile)
	writeTestFile(t, stackFile, strings.Replace(body, "CLOUD_ADMIN_DOMAIN=console.example.test", "CLOUD_ADMIN_DOMAIN=wrong.example.test", 1))
	if _, err := envroot.Load(cfg.RuntimeRoot, ""); err == nil || !strings.Contains(err.Error(), "CLOUD_ADMIN_DOMAIN mismatch") {
		t.Fatalf("Console alias bypassed generated runtime mismatch protection: %v", err)
	}
}

func TestDeploymentDNSConsoleAliasIsProdOnly(t *testing.T) {
	clearDNSNamingOverrides(t)
	for _, environment := range []string{"dev", "staging", "qa", "production"} {
		t.Run(environment, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, environment, "lke")
			appendFile(t, filepath.Join(workspace, "cloud_env", environment, "environment.env"), "CONSOLE_DOMAIN=console.example.test\n")
			if _, err := resolveDeploymentConfig(workspace, environment, ""); err == nil || !strings.Contains(err.Error(), "CONSOLE_DOMAIN is supported only in the prod") {
				t.Fatalf("nonprod Console alias accepted: %v", err)
			}
			runtimeRoot := filepath.Join(workspace, "cloud_env", environment, "runtime")
			writeTestFile(t, filepath.Join(runtimeRoot, "env", "stack.env"), "CLOUD_ENV_NAME="+environment+"\nCLOUD_PROVIDER=lke\nCLOUD_REGION=us-sea\nCLOUD_DNS_ROOT_DOMAIN=example.test\nCONSOLE_DOMAIN=console.example.test\n")
			if _, err := envroot.Load(runtimeRoot, ""); err == nil || !strings.Contains(err.Error(), "CONSOLE_DOMAIN is supported only in the prod") {
				t.Fatalf("nonprod runtime Console alias accepted: %v", err)
			}
		})
	}
	workspace := writeDeploymentFixture(t, "prod", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "prod", "environment.env"), "CONSOLE_DOMAIN=console.external.test\n")
	if _, err := resolveDeploymentConfig(workspace, "prod", ""); err == nil || !strings.Contains(err.Error(), "outside root domain") {
		t.Fatalf("out-of-zone Console alias accepted: %v", err)
	}
	coverageRoot := filepath.Join(t.TempDir(), "prod", "runtime")
	writeTestFile(t, filepath.Join(coverageRoot, "env", "stack.env"), "CLOUD_ENV_NAME=prod\nCLOUD_PROVIDER=lke\nCLOUD_REGION=us-sea\nCLOUD_DNS_ROOT_DOMAIN=example.test\nCLOUD_RUNTIME_COVERAGE_STACK=coverage-123-1\nCONSOLE_DOMAIN=console.example.test\n")
	if _, err := envroot.Load(coverageRoot, ""); err == nil || !strings.Contains(err.Error(), "CONSOLE_DOMAIN cannot be used by a runtime-coverage stack") {
		t.Fatalf("coverage stack could take ownership of production Console: %v", err)
	}
	derived := envroot.Derive(map[string]string{"CLOUD_ENV_NAME": "prod", "CLOUD_DNS_ROOT_DOMAIN": "example.test", "CLOUD_RUNTIME_COVERAGE_STACK": "coverage-123-1", "CONSOLE_DOMAIN": "console.example.test"})
	if derived["CLOUD_ADMIN_DOMAIN"] != "admin.coverage-123-1.example.test" {
		t.Fatal("coverage name derivation reused the production Console hostname")
	}
}

func TestDeploymentDNSPlanTURNCountAndDisabledLogger(t *testing.T) {
	clearDNSNamingOverrides(t)
	for _, count := range []int{0, 1, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "qa", "lke")
			writeTestFile(t, filepath.Join(workspace, "cloud_env", "qa", "overrides", "architecture.env"), fmt.Sprintf("TURN_REPLICAS=%d\n", count))
			cfg, err := resolveDeploymentConfig(workspace, "qa", "")
			if err != nil {
				t.Fatal(err)
			}
			// Normal architecture requires a positive logger minimum; a scoped
			// runtime can still omit its public route when its effective count is zero.
			cfg.Values["CLOUD_LOGGER_EFFECTIVE_REPLICAS"] = "0"
			turnNames := []string{}
			for _, record := range buildGenericDNSPlan(cfg).Records {
				if record.Name == "logger.video-cloud-qa.example.test" {
					t.Fatal("disabled logger received a DNS record")
				}
				if record.Purpose == "turn" {
					turnNames = append(turnNames, record.Name)
				}
			}
			want := []string{}
			if count == 1 {
				want = append(want, "turn.video-cloud-qa.example.test")
			} else if count > 1 {
				want = []string{"turn01.video-cloud-qa.example.test", "turn02.video-cloud-qa.example.test", "turn03.video-cloud-qa.example.test"}
			}
			if !reflect.DeepEqual(turnNames, want) {
				t.Fatalf("TURN count %d produced %v, want %v", count, turnNames, want)
			}
			values := deploymentEndpointValues(cfg)
			if route := lkeCloudLoggerRoute(values); route.Host != "" {
				t.Fatalf("disabled logger exposed a runtime route: %#v", route)
			}
			if count == 0 && (lkeCoturnSTUNURLs(values) != "" || lkeCoturnTURNURLs(values) != "") {
				t.Fatal("zero TURN nodes produced client discovery URLs")
			}
		})
	}
}

func TestDeploymentDNSPlanReflectsRendererOverridesAndRuntimeDeviceURL(t *testing.T) {
	clearDNSNamingOverrides(t)
	for key, prefix := range map[string]string{
		"LKE_DEVICE_DOMAIN": "devices", "LKE_FRONTEND_DOMAIN": "website", "BILLING_DOMAIN": "invoices",
		"PAYMENT_SIMULATOR_DOMAIN": "payments-test", "LKE_TURN_REGISTRY_PUBLIC_DOMAIN": "turn-discovery", "LKE_COTURN_DOMAIN": "relay",
	} {
		t.Setenv(key, prefix+".video-cloud-qa.example.test")
	}
	workspace := writeDeploymentFixture(t, "qa", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "qa", "")
	if err != nil {
		t.Fatal(err)
	}
	assertDNSPlanNames(t, buildGenericDNSPlan(cfg), []string{
		"video-cloud-qa.example.test", "devices.video-cloud-qa.example.test", "certissuer.video-cloud-qa.example.test",
		"turn-discovery.video-cloud-qa.example.test", "account-manager.video-cloud-qa.example.test", "admin.video-cloud-qa.example.test",
		"website.video-cloud-qa.example.test", "logger.video-cloud-qa.example.test", "invoices.video-cloud-qa.example.test",
		"payments-test.video-cloud-qa.example.test", "relay.video-cloud-qa.example.test",
	})
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	values, err := readStrictEnv(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if values["VIDEO_CLOUD_MTLS_BASE_URL"] != "https://devices.video-cloud-qa.example.test" || values["VIDEO_CLOUD_TOKEN_BASE_URL"] != values["VIDEO_CLOUD_MTLS_BASE_URL"] {
		t.Fatal("device URL differs from planned ingress override")
	}
	deviceRoute := lkePublicHTTPSRoute{Host: "devices.video-cloud-qa.example.test"}
	if !lkeIsDeviceMTLSRoute(values, deviceRoute) {
		t.Fatal("overridden device hostname lost its mTLS boundary")
	}
	t.Setenv("LKE_COTURN_DOMAIN_PREFIX", "relay")
	t.Setenv("LKE_COTURN_VM_COUNT", "2")
	plan := buildGenericDNSPlan(cfg)
	for _, want := range []string{"relay01.video-cloud-qa.example.test", "relay02.video-cloud-qa.example.test"} {
		found := false
		for _, record := range plan.Records {
			found = found || record.Name == want
		}
		if !found {
			t.Fatalf("multi-node escape hatch missing from plan: %s", want)
		}
	}
}

func TestDeploymentDNSRejectsOutsideZoneOverrideBeforeMaterializing(t *testing.T) {
	clearDNSNamingOverrides(t)
	workspace := writeDeploymentFixture(t, "qa", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "qa", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LKE_FRONTEND_DOMAIN", "www.external.test")
	if _, err := resolveDeploymentConfig(workspace, "qa", ""); err == nil || !strings.Contains(err.Error(), "outside root domain") {
		t.Fatalf("out-of-zone override accepted during resolution: %v", err)
	}
	if err := materializeDeploymentRuntime(cfg); err == nil || !strings.Contains(err.Error(), "outside root domain") {
		t.Fatalf("late override bypassed root-zone validation: %v", err)
	}
	if _, err := os.Stat(cfg.RuntimeRoot); !os.IsNotExist(err) {
		t.Fatalf("invalid managed exception created runtime state: %v", err)
	}
}

func TestDeploymentDNSRejectsConflictingEdgeAndTURNTargets(t *testing.T) {
	clearDNSNamingOverrides(t)
	t.Setenv("LKE_COTURN_DOMAIN", "video-cloud-qa.example.test")
	workspace := writeDeploymentFixture(t, "qa", "lke")
	if _, err := resolveDeploymentConfig(workspace, "qa", ""); err == nil || !strings.Contains(err.Error(), "conflicting targets runtime:public-edge and runtime:turn") {
		t.Fatalf("TURN may overwrite the edge IP: %v", err)
	}
}

func TestDeploymentDNSRejectsMalformedRootAndFrontendOrigin(t *testing.T) {
	clearDNSNamingOverrides(t)
	for _, root := range []string{"Example.test", "https://example.test", "example.test:443", "bad_.test", "-bad.test", "bad-.test", "example..test", "example.test.", strings.Repeat("a", 64) + ".test"} {
		t.Run(root, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "qa", "lke")
			writeTestFile(t, filepath.Join(workspace, "cloud_env", "qa", "environment.env"), "CLOUD_STACK_NAME=video-cloud-qa\nCLOUD_DNS_ROOT_DOMAIN="+root+"\nDEPLOYMENT_LOCATION=us-west\n")
			if _, err := resolveDeploymentConfig(workspace, "qa", ""); err == nil || !strings.Contains(err.Error(), "CLOUD_DNS_ROOT_DOMAIN") {
				t.Fatalf("malformed root accepted: %v", err)
			}
		})
	}
	for _, origin := range []string{"https://frontend.video-cloud-prod.example.test", "https://www.example.test/some-path", "https://www.example.test?query=1", "https://www.example.test#fragment"} {
		workspace := writeDeploymentFixture(t, "prod", "lke")
		appendFile(t, filepath.Join(workspace, "cloud_env", "prod", "environment.env"), "FRONTEND_DOMAIN=www.example.test\nPUBLIC_BASE_URL="+origin+"\n")
		if _, err := resolveDeploymentConfig(workspace, "prod", ""); err == nil || !strings.Contains(err.Error(), "effective frontend origin https://www.example.test") {
			t.Fatalf("frontend/client origin mismatch accepted: %v", err)
		}
	}
}

func assertDNSPlanNames(t *testing.T, plan genericDNSPlan, want []string) {
	t.Helper()
	got := []string{}
	for _, record := range plan.Records {
		got = append(got, record.Name)
		if record.Type != "A" || record.TTL != 600 || len(record.Values) != 1 || !strings.HasPrefix(record.Values[0], "runtime:") {
			t.Fatalf("invalid DNS planning intent: %#v", record)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DNS plan names = %v, want %v", got, want)
	}
}
