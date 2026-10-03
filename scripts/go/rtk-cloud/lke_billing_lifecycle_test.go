package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"filippo.io/age"
	"gopkg.in/yaml.v3"
)

func billingLifecycleDeployFixture(t *testing.T) map[string]string {
	t.Helper()
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore, lkeRuntimeSecretCache = false, map[string]string{}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	for index, entry := range billingLifecycleSecretCatalog() {
		lkeRuntimeSecretCache[entry.ID] = strings.Repeat(string(rune('A'+index)), 40)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	recipients, _ := json.Marshal([]string{identity.Recipient().String()})
	return map[string]string{
		"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev",
		"LKE_BILLING_BACKUP_ENABLED": "true", "LKE_BILLING_RAW_RETIREMENT_ENABLED": "false", "LKE_BILLING_INBOX_COMPACTION_ENABLED": "false", "LKE_BILLING_RAW_RETENTION_AUTHORITY_ENABLED": "true",
		"LKE_BILLING_BACKUP_INTERVAL": "12h", "LKE_BILLING_BACKUP_ENDPOINT": "https://us-sea-1.linodeobjects.com", "LKE_BILLING_BACKUP_REGION": "us-sea", "LKE_BILLING_BACKUP_SIGNING_REGION": "us-east-1",
		"LKE_BILLING_BACKUP_BUCKET": "rtk-cloud-dev-billing-backup-us-sea", "LKE_BILLING_BACKUP_UPLOADER_BUCKET_SCOPE": "rtk-cloud-dev-billing-backup-us-sea",
		"LKE_BILLING_BACKUP_ENCRYPTION_KEY_ID": "age-2026", "LKE_BILLING_BACKUP_RECIPIENTS_JSON": string(recipients),
		"LKE_BILLING_BACKUP_VERIFIER_KEY_ID": "verifier-2026", "LKE_BILLING_BACKUP_VERIFIER_PUBLIC_KEY": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("v", 32))),
		"LKE_BILLING_BACKUP_SCRATCH_STORAGE": "128Gi", "LKE_BILLING_BACKUP_SCRATCH_CAPACITY_BYTES": "137438953472", "LKE_BILLING_RAW_RETENTION_CONSUMER_ID": "video-cloud-mqttusage/staging",
		"LKE_CLOUD_LOGGER_IMAGE": "registry.invalid/logger:candidate", "LKE_VIDEO_CLOUD_IMAGE": "registry.invalid/video:candidate", "LKE_BILLING_IMAGE": "registry.invalid/billing:candidate",
	}
}

func TestBillingLifecycleDisabledDoesNotRequireOrRenderCredentials(t *testing.T) {
	oldCache, oldCanonical := lkeRuntimeSecretCache, activeCanonicalSecretStore
	lkeRuntimeSecretCache, activeCanonicalSecretStore = map[string]string{}, false
	t.Cleanup(func() { lkeRuntimeSecretCache, activeCanonicalSecretStore = oldCache, oldCanonical })
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}
	if err := validateLKEBillingLifecycleInputs(env); err != nil {
		t.Fatal(err)
	}
	for _, manifest := range []string{lkeBillingRawRetentionSecretManifest(env), lkeVideoBillingLifecycleSecretManifest(env), lkeBillingLifecycleConfigMapManifest(env), lkeBillingLifecycleScratchPVCManifest(env), lkeBillingLifecycleServiceManifest(env, "cloud-logger"), lkeVideoBillingLifecycleEnvManifest(env), lkeBillingLifecycleLoggerSecretData(env)} {
		if manifest != "" {
			t.Fatal("default disabled lifecycle created runtime requirement", manifest)
		}
	}
	for _, component := range []string{"billing", "cloud-logger", "video-cloud"} {
		if len(lkeBillingLifecycleNetworkPolicyManifests(env, component)) != 0 {
			t.Fatal("disabled network grant")
		}
	}
	for _, entry := range billingLifecycleSecretCatalog() {
		if _, exists := lkeRuntimeSecretCache[entry.ID]; exists {
			t.Fatal("disabled rendering generated credential", entry.ID)
		}
	}
}

func TestBillingLifecycleDeployFailsClosedWithoutScopePublicKeysAndDedicatedCredentials(t *testing.T) {
	env := billingLifecycleDeployFixture(t)
	if err := validateLKEBillingLifecycleInputs(env); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ key, value string }{
		{"LKE_BILLING_BACKUP_ENABLED", "TRUE"}, {"LKE_BILLING_BACKUP_ENABLED", "false"},
		{"CLOUD_ENV_NAME", ""}, {"CLOUD_STACK_NAME", ""}, {"LKE_BILLING_BACKUP_BUCKET", "rtk-cloud-prod-billing-backup-us-sea"},
		{"LKE_BILLING_BACKUP_UPLOADER_BUCKET_SCOPE", ""}, {"LKE_BILLING_BACKUP_ENDPOINT", "https://attacker.example.invalid"},
		{"LKE_BILLING_BACKUP_ENDPOINT", "http://us-sea-1.linodeobjects.com"}, {"LKE_BILLING_BACKUP_ENDPOINT", "https://user:secret@us-sea-1.linodeobjects.com"},
		{"LKE_BILLING_BACKUP_SIGNING_REGION", ""}, {"LKE_BILLING_BACKUP_RECIPIENTS_JSON", "[]"},
		{"LKE_BILLING_BACKUP_RECIPIENTS_JSON", `["AGE-SECRET-KEY-PRIVATE"]`}, {"LKE_BILLING_BACKUP_VERIFIER_PUBLIC_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("p", 64)))},
		{"LKE_BILLING_BACKUP_SOURCE_EVENT_ENVIRONMENTS_JSON", "[]"}, {"LKE_BILLING_BACKUP_SOURCE_EVENT_ENVIRONMENTS_JSON", `["dev","dev"]`},
		{"LKE_BILLING_BACKUP_SCRATCH_CAPACITY_BYTES", "4294967295"}, {"LKE_BILLING_BACKUP_SCRATCH_STORAGE", "4Gi"},
		{"LKE_BILLING_BACKUP_INTERVAL", "24h"}, {"LKE_BILLING_RAW_RETENTION_CONSUMER_ID", "video-cloud-mqttusage/dev"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			copy := map[string]string{}
			for key, value := range env {
				copy[key] = value
			}
			copy[test.key] = test.value
			if test.key == "LKE_BILLING_BACKUP_ENABLED" && test.value == "false" {
				copy["LKE_BILLING_RAW_RETIREMENT_ENABLED"] = "true"
			}
			if err := validateLKEBillingLifecycleInputs(copy); err == nil {
				t.Fatal("unsafe deploy intent accepted")
			}
		})
	}
	for _, entry := range billingLifecycleSecretCatalog() {
		value := lkeRuntimeSecretCache[entry.ID]
		delete(lkeRuntimeSecretCache, entry.ID)
		if err := validateLKEBillingLifecycleInputs(env); err == nil || strings.Contains(err.Error(), value) {
			t.Fatal("missing credential not rejected safely", entry.ID, err)
		}
		lkeRuntimeSecretCache[entry.ID] = value
	}
	lkeRuntimeSecretCache["billing-raw-retention-financial"] = lkeRuntimeSecretCache["billing-raw-retention-controller"]
	if err := validateLKEBillingLifecycleInputs(env); err == nil {
		t.Fatal("roles reused same credential")
	}
}

func TestBillingLifecycleRendererPublicConfigurationPrivateScratchAndScope(t *testing.T) {
	env := billingLifecycleDeployFixture(t)
	env["LKE_BILLING_BACKUP_SOURCE_EVENT_ENVIRONMENTS_JSON"] = `["staging"]`
	cfg, err := lkeBillingPublicConfiguration(env)
	if err != nil || cfg.Lifecycle.Environment != "dev" || cfg.Lifecycle.SourceEventEnvironments[0] != "staging" || cfg.ObjectStore.Region != "us-sea" || cfg.ObjectStore.SigningRegion != "us-east-1" || cfg.Lifecycle.RetentionDays != 90 || cfg.Lifecycle.PartBytes != 256<<20 {
		t.Fatal(cfg, err)
	}
	config := lkeBillingLifecycleConfigMapManifest(env)
	for _, value := range lkeRuntimeSecretCache {
		if strings.Contains(config, value) {
			t.Fatal("public config included secret")
		}
	}
	if strings.Contains(config, "AGE-SECRET-KEY") || strings.Contains(config, "private_key") {
		t.Fatal("private custody entered runtime config")
	}
	deployment := lkeCloudLoggerDeploymentManifest(env)
	for _, want := range []string{"billing-backup-scratch", "prepare-billing-backup-scratch", "chmod 0700", "chmod 0600", "/var/lib/rtk-billing-backup/lifecycle.json", "RTK_CLOUD_LOGGER_BILLING_BACKUP_INTERVAL", "value: \"12h\"", "containerPort: 8081"} {
		if !strings.Contains(deployment, want) {
			t.Fatal("missing private backup wiring", want)
		}
	}
	if strings.Contains(deployment, "RECOVERY_MODE") || strings.Contains(deployment, "private_key") {
		t.Fatal("normal deployment granted restore/private key authority")
	}
	var decoded map[string]any
	for _, manifest := range []string{deployment, config, lkeBillingLifecycleScratchPVCManifest(env), lkeBillingRawRetentionSecretManifest(env), lkeVideoBillingLifecycleSecretManifest(env)} {
		if err := yaml.Unmarshal([]byte(manifest), &decoded); err != nil {
			t.Fatal("invalid rendered YAML", err)
		}
	}
}

func TestBillingLifecycleAuthorityOnlyAPIAndPrivatePorts(t *testing.T) {
	env := billingLifecycleDeployFixture(t)
	billing := lkeDeploymentManifest(env, lkeWorkload{Key: "billing", Name: "billing", Namespace: lkeNamespaceName(env, "billing"), Image: env["LKE_BILLING_IMAGE"], Port: 8080}, nil)
	if !strings.Contains(billing, "name: billing-raw-retention-runtime") || !strings.Contains(billing, "containerPort: 8081") {
		t.Fatal("API missing dedicated authority config")
	}
	for _, manifest := range []string{lkeBillingSecretManifest(env), lkePaymentSimulatorDeploymentManifest(env), lkeVideoCloudWorkersSecretManifest(env)} {
		if strings.Contains(manifest, "RAW_RETENTION") || strings.Contains(manifest, "RAW_LIFECYCLE") || strings.Contains(manifest, "billing-raw-retention-runtime") {
			t.Fatal("authority leaked to ordinary shared worker config")
		}
	}
	for _, service := range lkeVideoCloudAuxiliaryServices() {
		manifest := lkeVideoCloudAuxiliaryDeploymentManifest(env, service)
		if service.Name == "video-cloud-mqttusage" {
			if !strings.Contains(manifest, "VIDEO_CLOUD_BILLING_RAW_LIFECYCLE_ENVIRONMENT") || !strings.Contains(manifest, "value: \"dev\"") || !strings.Contains(manifest, "VIDEO_CLOUD_ENV\n              value: \"staging\"") || !strings.Contains(manifest, "containerPort: 19401") {
				t.Fatal("actual scope or legacy identity lost")
			}
		} else if strings.Contains(manifest, "RAW_LIFECYCLE") {
			t.Fatal("reconciliation credential leaked to unrelated worker", service.Name)
		}
	}
	for _, component := range []string{"billing", "cloud-logger", "video-cloud"} {
		service := lkeBillingLifecycleServiceManifest(env, component)
		if !strings.Contains(service, "type: ClusterIP") || strings.Contains(service, "NodePort") || strings.Contains(service, "LoadBalancer") {
			t.Fatal("lifecycle service is public")
		}
		for _, policy := range lkeBillingLifecycleNetworkPolicyManifests(env, component) {
			if !strings.Contains(policy, "podSelector:") || !strings.Contains(policy, "namespaceSelector:") || strings.Contains(policy, "0.0.0.0") || strings.Contains(policy, "port: 8080") || strings.Contains(policy, "port: 18090") || strings.Contains(policy, "port: 19400") {
				t.Fatal("private port isolation absent")
			}
		}
	}
	for _, route := range lkePublicHTTPSRoutes(env) {
		if strings.Contains(route.Service, "lifecycle") || strings.Contains(route.Service, "raw-retention") || ((route.Service == "billing" || route.Service == "cloud-logger") && route.TargetPort == 8081) || route.TargetPort == 19401 {
			t.Fatal("private lifecycle route entered public ingress", route)
		}
	}
	publicPolicy := lkeAllowPublicIngressNetworkPolicyManifest(env, lkeNamespaceName(env, "billing"), []int{8080, 8081})
	parts := strings.Split(publicPolicy, "---\n")
	if len(parts) != 2 || !strings.Contains(parts[0], "name: allow-public-ingress\n") || !strings.Contains(parts[0], "app.kubernetes.io/name: billing\n") || strings.Contains(parts[0], "port: 8081") || !strings.Contains(parts[1], "app.kubernetes.io/name: payment-simulator\n") {
		t.Fatal("legacy public ingress policy still grants Billing private port", publicPolicy)
	}
}

func TestBillingLifecycleDedicatedKeysNeverFallBackToGeneralOrSeededCredentials(t *testing.T) {
	env := billingLifecycleDeployFixture(t)
	delete(lkeRuntimeSecretCache, "billing-backup-access-key-id")
	t.Setenv("LINODE_OBJ_ACCESS_KEY_ID", "general-media-credential")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "fixture-seed-that-must-not-create-backup-keys")
	if err := validateLKEBillingLifecycleInputs(env); err == nil || !strings.Contains(err.Error(), "billing-backup-access-key-id") {
		t.Fatal("dedicated uploader silently used fallback", err)
	}
	if got := lkeBillingLifecycleSecret("billing-backup-access-key-id"); got != "" {
		t.Fatal("missing dedicated access key was synthesized")
	}
	lkeRuntimeSecretCache["billing-backup-access-key-id"] = strings.Repeat("M", 40)
	t.Setenv("LINODE_OBJ_ACCESS_KEY_ID", lkeRuntimeSecretCache["billing-backup-access-key-id"])
	if err := validateLKEBillingLifecycleInputs(env); err == nil || !strings.Contains(err.Error(), "must not reuse general") {
		t.Fatal("explicitly copied media key was accepted as dedicated uploader", err)
	}
}
