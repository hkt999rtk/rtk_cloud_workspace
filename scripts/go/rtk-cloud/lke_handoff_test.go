package main

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func handoffLKETestEnv(enabled bool) map[string]string {
	enabledValue := "false"
	if enabled {
		enabledValue = "true"
	}
	return map[string]string{
		"CLOUD_STACK_NAME":                           "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":                         "video-cloud-staging.example.test",
		"ACCOUNT_MANAGER_DOMAIN":                     "account-manager.video-cloud-staging.example.test",
		"CLOUD_ADMIN_DOMAIN":                         "admin.video-cloud-staging.example.test",
		"LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED": enabledValue,
		"LKE_ACCOUNT_MANAGER_IMAGE":                  "registry.example.test/account-manager:test",
		"LKE_BILLING_IMAGE":                          "registry.example.test/billing:test",
		"LKE_VIDEO_CLOUD_IMAGE":                      "registry.example.test/video-cloud:test",
		"LKE_CLOUD_LOGGER_IMAGE":                     "registry.example.test/cloud-logger:test",
	}
}

func TestLKEHandoffRuntimeIsExplicitlyOptIn(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "handoff-test")
	env := handoffLKETestEnv(false)
	if lkeAccountManagerHandoffWorkerEnabled(env) {
		t.Fatal("handoff worker must be disabled by default")
	}
	for name, manifest := range map[string]string{
		"account-manager": lkeAccountManagerSecretManifest(env),
		"billing":         lkeBillingSecretManifest(env),
		"factory":         lkeFactoryEnrollRuntimeSecretManifest(env),
		"video-control":   lkeVideoCloudRuntimeSecretManifest(env),
		"mqtt-usage":      lkeVideoCloudWorkersSecretManifest(env),
	} {
		if !strings.Contains(manifest, `HANDOFF_TOKEN: ""`) && !strings.Contains(manifest, `RECOVERY_TOKEN: ""`) {
			t.Fatalf("%s manifest must keep handoff credentials empty while disabled:\n%s", name, manifest)
		}
	}
	mqttUsage := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400})
	for _, forbidden := range []string{"VIDEO_CLOUD_BILLING_USAGE_ENDPOINT", "VIDEO_CLOUD_EMQX_API_URL", "VIDEO_CLOUD_MQTT_USAGE_SETTLEMENT_TOKEN"} {
		if strings.Contains(mqttUsage, forbidden) {
			t.Fatalf("disabled handoff deployment unexpectedly contains %q", forbidden)
		}
	}
	for _, required := range []string{"mqtt-usage-checkpoint", "type: Recreate", "prepare-mqtt-usage-checkpoint"} {
		if !strings.Contains(mqttUsage, required) {
			t.Fatalf("disabled handoff deployment must retain runtime checkpoint storage %q", required)
		}
	}
	if bootstrap := lkeEMQXHandoffAPIKeyBootstrap(env); bootstrap != "" {
		t.Fatalf("disabled handoff EMQX bootstrap = %q, want empty", bootstrap)
	}
	if strings.Contains(lkeEMQXTenantBaseHOCON(env), "bootstrap_file") {
		t.Fatal("disabled handoff EMQX config unexpectedly enables API-key bootstrap")
	}
	if secret := lkeBillingSecretManifest(env); !strings.Contains(secret, `MQTT_USAGE_SETTLEMENT_TOKEN: ""`) {
		t.Fatalf("disabled settlement collector credential must be empty:\n%s", secret)
	}
}

func TestLKEHandoffRuntimeWiresDedicatedCredentialsAndEndpoints(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "handoff-test")
	env := handoffLKETestEnv(true)

	account := lkeAccountManagerSecretManifest(env)
	if !strings.Contains(account, "ACCOUNT_MANAGER_FACTORY_ENROLLMENT_TOKEN:") || !strings.Contains(lkeFactoryEnrollRuntimeSecretManifest(env), "FACTORY_ENROLL_ACCOUNT_MANAGER_TOKEN:") {
		t.Fatal("factory admission must use one dedicated credential on both sides")
	}
	cases := []struct {
		name        string
		token       string
		accountKey  string
		consumer    string
		consumerKey string
	}{
		{"billing", lkeBillingHandoffToken(), "BILLING_HANDOFF_TOKEN", lkeBillingSecretManifest(env), "BILLING_HANDOFF_TOKEN"},
		{"factory", lkeFactoryHandoffToken(), "FACTORY_HANDOFF_TOKEN", lkeFactoryEnrollRuntimeSecretManifest(env), "FACTORY_ENROLL_RECOVERY_TOKEN"},
		{"video-control", lkeVideoControlHandoffToken(), "VIDEO_CONTROL_PLANE_HANDOFF_TOKEN", lkeVideoCloudRuntimeSecretManifest(env), "VIDEO_CLOUD_CONTROL_HANDOFF_TOKEN"},
		{"mqtt-usage", lkeMQTTUsageHandoffToken(), "MQTT_USAGE_HANDOFF_TOKEN", lkeVideoCloudWorkersSecretManifest(env), "VIDEO_CLOUD_MQTT_USAGE_HANDOFF_TOKEN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(account, fmt.Sprintf("%s: %q", tc.accountKey, tc.token)) {
				t.Fatalf("account-manager secret missing %s", tc.accountKey)
			}
			if !strings.Contains(tc.consumer, fmt.Sprintf("%s: %q", tc.consumerKey, tc.token)) {
				t.Fatalf("consumer secret missing matching %s", tc.consumerKey)
			}
		})
	}
	for _, want := range []string{
		"BILLING_HANDOFF_BASE_URL: \"http://billing.video-cloud-staging-billing.svc.cluster.local:80\"",
		"FACTORY_HANDOFF_BASE_URL: \"http://factoryenroll.video-cloud-staging-video-cloud.svc.cluster.local:80\"",
		"VIDEO_CONTROL_PLANE_HANDOFF_BASE_URL: \"http://video-cloud-api.video-cloud-staging-video-cloud.svc.cluster.local:80\"",
		"MQTT_USAGE_HANDOFF_BASE_URL: \"http://video-cloud-mqttusage.video-cloud-staging-video-cloud.svc.cluster.local:19400\"",
	} {
		if !strings.Contains(account, want) {
			t.Fatalf("account-manager secret missing %q:\n%s", want, account)
		}
	}
	for name, manifest := range map[string]string{
		"billing":     lkeAllowAccountManagerHandoffBillingNetworkPolicyManifest(env),
		"video-cloud": lkeAllowAccountManagerHandoffVideoCloudNetworkPolicyManifest(env),
	} {
		if !strings.Contains(manifest, "values: [account-manager, account-manager-handoff-worker, account-manager-cloud-deletion-worker]") {
			t.Fatalf("%s handoff policy does not allow synchronous API preflight:\n%s", name, manifest)
		}
	}
}

func TestLKEBillingCloudCreationWiresDedicatedCredentialWithoutHandoff(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "billing-creation-test")
	env := handoffLKETestEnv(false)
	account := lkeAccountManagerSecretManifest(env)
	billing := lkeBillingSecretManifest(env)
	token := lkeBillingCloudCreationToken()
	if !strings.Contains(account, `BILLING_CLOUD_CREATION_BASE_URL: "https://billing.video-cloud-staging.example.test"`) {
		t.Fatalf("account-manager secret missing trusted Billing creation origin:\n%s", account)
	}
	for name, manifest := range map[string]string{"account-manager": account, "billing": billing} {
		if !strings.Contains(manifest, fmt.Sprintf("BILLING_CLOUD_CREATION_TOKEN: %q", token)) {
			t.Fatalf("%s secret missing dedicated Billing cloud-creation credential", name)
		}
	}
	for _, other := range []string{lkeBillingServiceToken(), lkeBillingInternalToken(), lkeBillingDebitToken(), lkeBillingHandoffToken()} {
		if token == other {
			t.Fatal("Billing cloud-creation credential must be distinct")
		}
	}
}

func TestLKEBillingCloudCreationCredentialRotatesBothWorkloads(t *testing.T) {
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{"billing-cloud-creation": "creation-token-one"}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	env := handoffLKETestEnv(false)

	var account, billing lkeWorkload
	for _, workload := range lkeWorkloads(env) {
		switch workload.Key {
		case "account-manager":
			account = workload
		case "billing":
			billing = workload
		}
	}
	if account.Key == "" || billing.Key == "" {
		t.Fatal("account-manager and billing workloads are required")
	}
	accountBefore := lkeDeploymentManifest(env, account, nil)
	billingBefore := lkeDeploymentManifest(env, billing, nil)

	lkeRuntimeSecretCache["billing-cloud-creation"] = "creation-token-two"
	if changed := lkeDeploymentManifest(env, account, nil); changed == accountBefore {
		t.Fatal("Billing cloud-creation credential rotation must update the Account Manager pod checksum")
	}
	if changed := lkeDeploymentManifest(env, billing, nil); changed == billingBefore {
		t.Fatal("Billing cloud-creation credential rotation must update the Billing pod checksum")
	}
}

func TestLKEHandoffWorkerAndConsumersUseRuntimeSecrets(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "handoff-test")
	env := handoffLKETestEnv(true)
	worker := lkeAccountManagerHandoffWorkerManifest(env)
	for _, want := range []string{
		"name: account-manager-handoff-worker",
		`command: ["/app/rtk-account-manager-handoff-worker"]`,
		"name: account-manager-runtime",
	} {
		if !strings.Contains(worker, want) {
			t.Fatalf("handoff worker manifest missing %q:\n%s", want, worker)
		}
	}
	deletionWorker := lkeAccountManagerCloudDeletionWorkerManifest(env)
	for _, want := range []string{
		"name: account-manager-cloud-deletion-worker",
		`command: ["/app/rtk-account-manager-cloud-deletion-worker"]`,
		"name: account-manager-runtime",
	} {
		if !strings.Contains(deletionWorker, want) {
			t.Fatalf("cloud deletion worker manifest missing %q:\n%s", want, deletionWorker)
		}
	}

	material := lkeCertIssuerMaterial{}
	consumers := strings.Join([]string{
		lkeFactoryEnrollDeploymentManifest(env, material),
		lkeDeploymentManifest(env, lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Image: env["LKE_VIDEO_CLOUD_IMAGE"]}, nil),
		lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400}),
		lkeBillingSettlementCollectorManifest(env),
	}, "\n---\n")
	for _, want := range []string{
		"name: FACTORY_ENROLL_RECOVERY_TOKEN",
		"name: FACTORY_ENROLL_ACCOUNT_MANAGER_URL",
		`value: "https://account-manager.video-cloud-staging.example.test"`,
		"name: FACTORY_ENROLL_ACCOUNT_MANAGER_TOKEN",
		"key: FACTORY_ENROLL_RECOVERY_TOKEN",
		"name: VIDEO_CLOUD_CONTROL_HANDOFF_TOKEN",
		"key: VIDEO_CLOUD_CONTROL_HANDOFF_TOKEN",
		"name: VIDEO_CLOUD_MQTT_USAGE_HANDOFF_TOKEN",
		"key: VIDEO_CLOUD_MQTT_USAGE_HANDOFF_TOKEN",
		"name: VIDEO_CLOUD_MQTT_USAGE_SETTLEMENT_TOKEN",
		"key: VIDEO_CLOUD_MQTT_USAGE_SETTLEMENT_TOKEN",
		`command: ["/rtk-billing-settlement-collector"]`,
		"name: MQTT_USAGE_SETTLEMENT_BASE_URL",
		"name: VIDEO_CLOUD_BILLING_USAGE_TOKEN",
		"name: VIDEO_CLOUD_EMQX_API_KEY",
		"name: VIDEO_CLOUD_EMQX_API_SECRET",
		"name: mqtt-usage-checkpoint",
		"claimName: video-cloud-mqttusage-checkpoint",
		"name: prepare-mqtt-usage-checkpoint",
		"chmod 0700 /var/lib/video-cloud/mqtt-usage",
		"type: Recreate",
	} {
		if !strings.Contains(consumers, want) {
			t.Fatalf("consumer manifests missing %q", want)
		}
	}
}

func TestLKEHandoffMQTTUsageHasDurableCheckpointAndEMQXBootstrap(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "handoff-test")
	env := handoffLKETestEnv(true)

	pvc := lkeVideoCloudMQTTUsageCheckpointPVCManifest(env)
	for _, want := range []string{
		"kind: PersistentVolumeClaim",
		"name: video-cloud-mqttusage-checkpoint",
		`accessModes: ["ReadWriteOnce"]`,
		"storage: 5Gi",
	} {
		if !strings.Contains(pvc, want) {
			t.Fatalf("MQTT usage checkpoint PVC missing %q:\n%s", want, pvc)
		}
	}

	bootstrap := lkeEMQXHandoffAPIKeyBootstrap(env)
	if !strings.HasSuffix(bootstrap, ":administrator\n") {
		t.Fatalf("EMQX API-key bootstrap missing administrator role: %q", bootstrap)
	}
	if !strings.Contains(lkeEMQXTenantBaseHOCON(env), `bootstrap_file = "/opt/emqx/etc/certs/handoff-api-keys.conf"`) {
		t.Fatal("EMQX config does not mount the handoff API-key bootstrap file")
	}
	secret := lkeMQTTRuntimeSecretManifest(env, lkeMQTTMaterial{})
	if !strings.Contains(secret, "handoff-api-keys.conf:") || !strings.Contains(secret, fmt.Sprintf("%q", bootstrap)) {
		t.Fatalf("MQTT secret missing EMQX API-key bootstrap:\n%s", secret)
	}
}

func TestLKEHandoffNetworkPoliciesAllowRequiredCallersOnlyOnCoordinatorPorts(t *testing.T) {
	env := handoffLKETestEnv(true)
	manifests := strings.Join(lkeAccountManagerHandoffNetworkPolicyManifests(env), "\n---\n")
	for _, want := range []string{
		"values: [account-manager, account-manager-handoff-worker, account-manager-cloud-deletion-worker]",
		"values: [video-cloud-api, factoryenroll, video-cloud-mqttusage]",
		"app.kubernetes.io/name: video-cloud-mqttusage",
		"name: allow-mqttusage-emqx-management",
		"name: allow-billing-settlement-checkpoint",
		"app.kubernetes.io/name: billing-settlement-collector",
		"port: 8080",
		"port: 18083",
		"port: 18443",
		"port: 19400",
	} {
		if !strings.Contains(manifests, want) {
			t.Fatalf("handoff NetworkPolicies missing %q:\n%s", want, manifests)
		}
	}
}

func TestLKEHandoffRequiresCoordinatedFullDeploy(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	env := handoffLKETestEnv(true)
	if err := validateK8SWorkloadSelection(env, provisionOptions{workloads: []string{"account-manager", "billing", "video-cloud"}}); err == nil || !strings.Contains(err.Error(), "coordinated full deploy") {
		t.Fatalf("targeted handoff deploy validation error = %v", err)
	}
	if err := validateK8SWorkloadSelection(env, provisionOptions{}); err != nil {
		t.Fatalf("full handoff deploy rejected: %v", err)
	}
}

func TestLKEHandoffGeneratedManifestsAreValidYAML(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "handoff-test")
	env := handoffLKETestEnv(true)
	manifests := map[string]string{
		"account-manager-secret":  lkeAccountManagerSecretManifest(env),
		"factory-secret":          lkeFactoryEnrollRuntimeSecretManifest(env),
		"factory-deployment":      lkeFactoryEnrollDeploymentManifest(env, lkeCertIssuerMaterial{}),
		"handoff-worker":          lkeAccountManagerHandoffWorkerManifest(env),
		"cloud-deletion-worker":   lkeAccountManagerCloudDeletionWorkerManifest(env),
		"cloud-logger-deployment": lkeCloudLoggerDeploymentManifest(env),
		"cloud-logger-pvc":        lkeCloudLoggerBillingInboxPVCManifest(env),
		"mqtt-secret":             lkeMQTTRuntimeSecretManifest(env, lkeMQTTMaterial{}),
		"mqtt-config":             lkeMQTTConfigManifest(env),
		"mqtt-usage-secret":       lkeVideoCloudWorkersSecretManifest(env),
		"mqtt-usage-deployment": lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{
			Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400,
		}),
		"mqtt-usage-pvc":               lkeVideoCloudMQTTUsageCheckpointPVCManifest(env),
		"billing-secret":               lkeBillingSecretManifest(env),
		"billing-settlement-collector": lkeBillingSettlementCollectorManifest(env),
	}
	for index, manifest := range lkeAccountManagerHandoffNetworkPolicyManifests(env) {
		manifests[fmt.Sprintf("network-policy-%d", index)] = manifest
	}
	for name, manifest := range manifests {
		var document yaml.Node
		if err := yaml.Unmarshal([]byte(manifest), &document); err != nil {
			t.Fatalf("%s manifest is invalid YAML: %v\n%s", name, err, manifest)
		}
	}
}

func TestLKEBillingSettlementCollectorUsesOneIsolatedCredential(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "handoff-test")
	env := handoffLKETestEnv(true)
	token := lkeMQTTUsageSettlementToken()
	if token == lkeMQTTUsageHandoffToken() || token == lkeBillingHandoffToken() || token == lkeBillingInternalToken() {
		t.Fatal("settlement collector credential must be isolated from handoff and fact-delivery authority")
	}
	for name, binding := range map[string]string{
		"billing":     lkeBillingSecretManifest(env),
		"video-cloud": lkeVideoCloudWorkersSecretManifest(env),
	} {
		if !strings.Contains(binding, fmt.Sprintf("SETTLEMENT_TOKEN: %q", token)) {
			t.Fatalf("%s secret does not bind the shared settlement credential", name)
		}
	}
	manifest := lkeBillingSettlementCollectorManifest(env)
	for _, want := range []string{
		`command: ["/rtk-billing-settlement-collector"]`,
		`value: "http://video-cloud-mqttusage.video-cloud-staging-video-cloud.svc.cluster.local:19400"`,
		"name: billing-runtime",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("collector deployment missing %q:\n%s", want, manifest)
		}
	}
}

func TestLKEBillingSettlementCredentialRotationRollsBothConsumers(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	env := handoffLKETestEnv(true)
	oldCache := lkeRuntimeSecretCache
	lkeRuntimeSecretCache = map[string]string{"mqtt-usage-settlement": "settlement-token-one-0123456789abcdef"}
	t.Cleanup(func() { lkeRuntimeSecretCache = oldCache })
	mqttBefore := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400})
	collectorBefore := lkeBillingSettlementCollectorManifest(env)
	lkeRuntimeSecretCache["mqtt-usage-settlement"] = "settlement-token-two-0123456789abcdef"
	if mqttBefore == lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400}) {
		t.Fatal("settlement credential rotation must roll the MQTT usage producer")
	}
	if collectorBefore == lkeBillingSettlementCollectorManifest(env) {
		t.Fatal("settlement credential rotation must roll the Billing collector")
	}
}
