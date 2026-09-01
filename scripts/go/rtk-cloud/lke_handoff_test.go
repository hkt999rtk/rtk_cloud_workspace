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
		"CLOUD_ADMIN_DOMAIN":                         "admin.video-cloud-staging.example.test",
		"LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED": enabledValue,
		"LKE_ACCOUNT_MANAGER_IMAGE":                  "registry.example.test/account-manager:test",
		"LKE_BILLING_IMAGE":                          "registry.example.test/billing:test",
		"LKE_VIDEO_CLOUD_IMAGE":                      "registry.example.test/video-cloud:test",
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
	for _, forbidden := range []string{"VIDEO_CLOUD_BILLING_USAGE_ENDPOINT", "VIDEO_CLOUD_EMQX_API_URL", "mqtt-usage-checkpoint", "type: Recreate"} {
		if strings.Contains(mqttUsage, forbidden) {
			t.Fatalf("disabled handoff deployment unexpectedly contains %q", forbidden)
		}
	}
	if bootstrap := lkeEMQXHandoffAPIKeyBootstrap(env); bootstrap != "" {
		t.Fatalf("disabled handoff EMQX bootstrap = %q, want empty", bootstrap)
	}
	if strings.Contains(lkeEMQXTenantBaseHOCON(env), "bootstrap_file") {
		t.Fatal("disabled handoff EMQX config unexpectedly enables API-key bootstrap")
	}
}

func TestLKEHandoffRuntimeWiresDedicatedCredentialsAndEndpoints(t *testing.T) {
	t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "handoff-test")
	env := handoffLKETestEnv(true)

	account := lkeAccountManagerSecretManifest(env)
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

	material := lkeCertIssuerMaterial{}
	consumers := strings.Join([]string{
		lkeFactoryEnrollDeploymentManifest(env, material),
		lkeDeploymentManifest(env, lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Image: env["LKE_VIDEO_CLOUD_IMAGE"]}, nil),
		lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400}),
	}, "\n---\n")
	for _, want := range []string{
		"name: FACTORY_ENROLL_RECOVERY_TOKEN",
		"key: FACTORY_ENROLL_RECOVERY_TOKEN",
		"name: VIDEO_CLOUD_CONTROL_HANDOFF_TOKEN",
		"key: VIDEO_CLOUD_CONTROL_HANDOFF_TOKEN",
		"name: VIDEO_CLOUD_MQTT_USAGE_HANDOFF_TOKEN",
		"key: VIDEO_CLOUD_MQTT_USAGE_HANDOFF_TOKEN",
		"name: VIDEO_CLOUD_BILLING_USAGE_TOKEN",
		"name: VIDEO_CLOUD_EMQX_API_KEY",
		"name: VIDEO_CLOUD_EMQX_API_SECRET",
		"name: mqtt-usage-checkpoint",
		"claimName: video-cloud-mqttusage-checkpoint",
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

func TestLKEHandoffNetworkPoliciesAllowOnlyCoordinatorPorts(t *testing.T) {
	env := handoffLKETestEnv(true)
	manifests := strings.Join(lkeAccountManagerHandoffNetworkPolicyManifests(env), "\n---\n")
	for _, want := range []string{
		"app.kubernetes.io/name: account-manager-handoff-worker",
		"values: [video-cloud-api, factoryenroll, video-cloud-mqttusage]",
		"app.kubernetes.io/name: video-cloud-mqttusage",
		"name: allow-mqttusage-emqx-management",
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
		"account-manager-secret": lkeAccountManagerSecretManifest(env),
		"handoff-worker":         lkeAccountManagerHandoffWorkerManifest(env),
		"mqtt-secret":            lkeMQTTRuntimeSecretManifest(env, lkeMQTTMaterial{}),
		"mqtt-config":            lkeMQTTConfigManifest(env),
		"mqtt-usage-secret":      lkeVideoCloudWorkersSecretManifest(env),
		"mqtt-usage-deployment": lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{
			Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400,
		}),
		"mqtt-usage-pvc": lkeVideoCloudMQTTUsageCheckpointPVCManifest(env),
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
