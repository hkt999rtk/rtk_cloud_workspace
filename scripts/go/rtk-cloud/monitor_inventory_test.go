package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/internal/cloudmonitor"
)

func TestMonitorInventoryCommandReadOnlyWithoutRuntime(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "")
	workspace := writeDeploymentFixture(t, "dev", "lke")
	t.Setenv("KUBECONFIG", filepath.Join(workspace, "missing-kubeconfig"))
	t.Setenv("PATH", "") // Any accidental subprocess is a test failure.
	before := monitorFixtureTree(t, workspace)
	var output bytes.Buffer
	args := []string{"monitor-inventory", "--environment", "dev", "--workspace", workspace, "--json"}
	normalized, err := normalizeEnvironmentArgs(args)
	if err != nil || !reflect.DeepEqual(args, normalized) || commands["monitor-inventory"].run == nil {
		t.Fatalf("command registration/environment routing incorrect: %v", err)
	}
	if err := recoveryMutationGuard(args); err != nil {
		t.Fatal(err)
	}
	if err := runMonitorInventoryTo([]string{"--environment", "dev", "--workspace", workspace, "--json"}, &output); err != nil {
		t.Fatal(err)
	}
	var inventory cloudmonitor.Inventory
	if err := json.Unmarshal(output.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Environment != "dev" || inventory.Stack != "video-cloud-dev" || inventory.SchemaVersion != 1 || len(inventory.SourceFingerprint) != 64 {
		t.Fatalf("bad inventory identity: %+v", inventory)
	}
	if !reflect.DeepEqual(before, monitorFixtureTree(t, workspace)) {
		t.Fatal("inventory changed the environment files or created runtime state")
	}
	if _, err := os.Stat(filepath.Join(workspace, "cloud_env", "dev", "runtime")); !os.IsNotExist(err) {
		t.Fatalf("inventory materialized runtime: %v", err)
	}
	wanted := []string{"postgresql", "redis", "fleet-valkey", "mqtt", "openbao", "certissuer", "factoryenroll", "video-cloud-cleaner", "account-manager-email-worker", "account-manager-outbox-worker", "billing-payment-worker", "video-cloud-log-collector", "ingress-nginx-controller", "haproxy", "pkiturn", "pki-controller"}
	for _, name := range wanted {
		if _, ok := monitorTarget(inventory, name); !ok {
			t.Errorf("missing expected target %s", name)
		}
	}
	shadow, _ := monitorTarget(inventory, "video-cloud-shadowworker")
	if !shadow.Enabled || !shadow.Required || !shadow.IntentUnknown || len(inventory.CoverageNotes) == 0 {
		t.Fatal("missing rollout intent must remain unknown, not silently disabled")
	}
	for _, e := range inventory.Endpoints {
		if e.Service == "cloud-logger" && !reflect.DeepEqual(e.SuccessCodes, []int{204}) {
			t.Fatal("Logger success code must be 204")
		}
		if !strings.Contains(e.URL, "video-cloud-dev.example.test") {
			t.Errorf("endpoint escaped selected environment: %s", e.URL)
		}
	}
}

func TestMonitorInventoryRejectsAmbientOverrides(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "")
	for _, key := range []string{"LKE_NAMESPACE_PLATFORM", "LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "VIDEO_CLOUD_DOMAIN", "PAYMENT_SIMULATOR_DOMAIN", "CLOUD_STACK_NAME"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "poisoned-secret-value")
			var out bytes.Buffer
			err := runMonitorInventoryTo([]string{"--environment", "dev", "--json"}, &out)
			if err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "poisoned-secret-value") || out.Len() != 0 {
				t.Fatalf("override was accepted or exposed: %v", err)
			}
		})
	}
}

func TestMonitorInventoryFeatureIntentAndFingerprint(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "")
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "DEPLOYMENT_REQUIRE_PKITURN=true\n")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	before, err := buildMonitorInventory(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	stackPath := filepath.Join(cfg.RuntimeRoot, "env", "stack.env")
	writeTestFile(t, stackPath, "CLOUD_STACK_NAME=video-cloud-dev\nCLOUD_ENV_NAME=dev\nLKE_SHADOW_WORKER_REGISTRATION_ENABLED=true\nLKE_WEBRTC_SERVICE_REGISTRATION_ENABLED=false\nPRIVATE_SECRET=do-not-export\n")
	beforeFiles := monitorFixtureTree(t, workspace)
	after, err := buildMonitorInventory(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	shadow, _ := monitorTarget(after, "video-cloud-shadowworker")
	webrtc, _ := monitorTarget(after, "video-cloud-webrtcservice")
	pkiturn, _ := monitorTarget(after, "pkiturn")
	if !shadow.Enabled || shadow.IntentUnknown || webrtc.Enabled || webrtc.Required || webrtc.IntentUnknown || webrtc.DisabledReason == "" || !pkiturn.Required || pkiturn.IntentUnknown {
		t.Fatal("explicit feature gates or PKI requirement incorrectly resolved")
	}
	if before.SourceFingerprint == after.SourceFingerprint {
		t.Fatal("feature intent must affect source fingerprint")
	}
	body, _ := json.Marshal(after)
	if strings.Contains(string(body), "do-not-export") || strings.Contains(string(body), "PRIVATE_SECRET") {
		t.Fatal("private runtime fields were exported")
	}
	if !reflect.DeepEqual(beforeFiles, monitorFixtureTree(t, workspace)) {
		t.Fatal("inventory changed existing runtime files")
	}
	writeTestFile(t, stackPath, "CLOUD_STACK_NAME=video-cloud-dev\nCLOUD_ENV_NAME=dev\nLKE_SHADOW_WORKER_REGISTRATION_ENABLED=true\nLKE_WEBRTC_SERVICE_REGISTRATION_ENABLED=false\nPRIVATE_SECRET=different-secret\n")
	secretChanged, err := buildMonitorInventory(cfg, now)
	if err != nil || secretChanged.SourceFingerprint != after.SourceFingerprint {
		t.Fatalf("secret field influenced desired-config fingerprint: %v", err)
	}
	writeTestFile(t, stackPath, "CLOUD_STACK_NAME=video-cloud-prod\n")
	if _, err := buildMonitorInventory(cfg, now); err == nil {
		t.Fatal("mismatched runtime stack accepted")
	}
	writeTestFile(t, stackPath, "LKE_SHADOW_WORKER_REGISTRATION_ENABLED=perhaps\n")
	if _, err := buildMonitorInventory(cfg, now); err == nil {
		t.Fatal("invalid feature declaration accepted")
	}
}

func TestMonitorInventoryDisabledVerifierAndJSONRequirement(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "")
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "workloads.env"), "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED=true\n")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "overrides", "architecture.env"), "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED=false\n")
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := buildMonitorInventory(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	target, _ := monitorTarget(inventory, "video-cloud-clipverifier")
	if target.Enabled || target.DisabledReason == "" {
		t.Fatal("disabled clip verifier reported enabled")
	}
	for _, metric := range inventory.MetricTargets {
		if metric.Service == "video-cloud-clipverifier" {
			t.Fatal("disabled verifier is a required scrape target")
		}
	}
	var output bytes.Buffer
	if err := runMonitorInventoryTo([]string{"--workspace", workspace, "--environment", "dev"}, &output); err == nil {
		t.Fatal("missing JSON flag accepted")
	}
	if err := runMonitorInventoryTo([]string{"--workspace", workspace, "--environment", "dev", "--json", "unexpected"}, &output); err == nil {
		t.Fatal("positional argument accepted")
	}
}

func TestMonitorInventoryUsesCanonicalEndpointsAndProdAliases(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "")
	t.Setenv("LKE_FRONTEND_DOMAIN", "")
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	workspace := writeDeploymentFixture(t, "prod", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "prod", "environment.env"), "CLOUD_STACK_NAME=video-cloud-prod\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=us-west\nFRONTEND_DOMAIN=www.example.test\nCONSOLE_DOMAIN=console.example.test\nPUBLIC_BASE_URL=https://www.example.test\n")
	cfg, err := resolveDeploymentConfig(workspace, "prod", "")
	if err != nil {
		t.Fatal(err)
	}
	beforeValues := appendMap(cfg.Values, nil)
	beforeFiles := monitorFixtureTree(t, workspace)
	values := monitorEndpointValues(cfg)
	for key, want := range map[string]string{
		"VIDEO_CLOUD_DOMAIN":            "video-cloud-prod.example.test",
		"ACCOUNT_MANAGER_DOMAIN":        "account-manager.video-cloud-prod.example.test",
		"CLOUD_ADMIN_DOMAIN":            "console.example.test",
		"CLOUD_LOGGER_DOMAIN":           "logger.video-cloud-prod.example.test",
		"FRONTEND_DOMAIN":               "www.example.test",
		"VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-prod.example.test",
	} {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := lkeDeviceDomain(values); got != "device.video-cloud-prod.example.test" {
		t.Errorf("device domain = %q", got)
	}
	if got := deploymentRuntimeEndpoints(values)["VIDEO_CLOUD_MTLS_BASE_URL"]; got != "https://device.video-cloud-prod.example.test" {
		t.Errorf("mTLS endpoint = %q", got)
	}
	inventory, err := buildMonitorInventory(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Stack != "video-cloud-prod" {
		t.Fatalf("inventory stack = %q", inventory.Stack)
	}
	for _, target := range inventory.Targets {
		if target.Namespace != "" && !strings.HasPrefix(target.Namespace, inventory.Stack+"-") {
			t.Errorf("target namespace uses another stack: %s", target.Namespace)
		}
	}
	endpoints := map[string]string{}
	for _, endpoint := range inventory.Endpoints {
		endpoints[endpoint.Service] = endpoint.URL
	}
	for service, want := range map[string]string{
		"video-cloud":     "https://video-cloud-prod.example.test",
		"account-manager": "https://account-manager.video-cloud-prod.example.test",
		"cloud-admin":     "https://console.example.test",
		"frontend":        "https://www.example.test",
		"cloud-logger":    "https://logger.video-cloud-prod.example.test",
	} {
		if got := endpoints[service]; got != want {
			t.Errorf("%s endpoint = %q, want %q", service, got, want)
		}
	}
	if !reflect.DeepEqual(beforeValues, cfg.Values) || !reflect.DeepEqual(beforeFiles, monitorFixtureTree(t, workspace)) {
		t.Fatal("resolving endpoint defaults mutated configuration")
	}
}

func TestMonitorInventoryUsesCanonicalTURNReplicaCount(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "")
	for _, count := range []int{0, 1, 3} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "dev", "lke")
			writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "overrides", "architecture.env"), "TURN_REPLICAS="+strconv.Itoa(count)+"\n")
			cfg, err := resolveDeploymentConfig(workspace, "dev", "")
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := buildMonitorInventory(cfg, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var domains []string
			for _, target := range inventory.Targets {
				if target.Service == "turn" && target.Kind == "external" {
					domains = append(domains, target.Name)
				}
			}
			if len(domains) != count {
				t.Fatalf("TURN count=%d, wanted %d: %v", len(domains), count, domains)
			}
			for index, domain := range domains {
				want := "turn.video-cloud-dev.example.test"
				if count > 1 {
					want = "turn0" + strconv.Itoa(index+1) + ".video-cloud-dev.example.test"
				}
				if domain != want {
					t.Errorf("TURN domain=%s, wanted %s", domain, want)
				}
			}
		})
	}
}

func monitorTarget(inventory cloudmonitor.Inventory, name string) (cloudmonitor.WorkloadTarget, bool) {
	for _, target := range inventory.Targets {
		if target.Name == name {
			return target, true
		}
	}
	return cloudmonitor.WorkloadTarget{}, false
}

func monitorFixtureTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files[rel] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
