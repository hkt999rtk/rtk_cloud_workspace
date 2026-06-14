package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunProvisionLKEApplyUsesKubectl(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if count := strings.Count(log, "ARGS apply -f -"); count != len(lkeNamespaces(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}))+1 {
		t.Fatalf("unexpected kubectl apply count in log:\n%s", log)
	}
	if !strings.Contains(log, "kind: Namespace") || !strings.Contains(log, "kind: ConfigMap") {
		t.Fatalf("expected namespace and configmap manifests, got:\n%s", log)
	}
}

func TestRunProvisionLKEApplyFetchesKubeconfigWhenNoContext(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	writeTestFile(t, filepath.Join(envRoot, "state", "lke.env"), "LKE_CLUSTER_ID=12345\n")
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters/12345/kubeconfig": `{"kubeconfig":"` + base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nclusters: []\n")) + `"}`,
	})
	kubectlLog := fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if !strings.Contains(curlCalls, "GET /lke/clusters/12345/kubeconfig") {
		t.Fatalf("expected kubeconfig fetch, got:\n%s", curlCalls)
	}
	kubectlCalls := readTestFile(t, kubectlLog)
	if !strings.Contains(kubectlCalls, "ARGS --kubeconfig "+filepath.Join(envRoot, "state", "lke-kubeconfig.yaml")+" apply -f -") {
		t.Fatalf("expected kubectl to use env-root kubeconfig, got:\n%s", kubectlCalls)
	}
	kubeconfigInfo, err := os.Stat(filepath.Join(envRoot, "state", "lke-kubeconfig.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if kubeconfigInfo.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig permissions got %o want 600", kubeconfigInfo.Mode().Perm())
	}
}

func TestRunProvisionLKEApplyDiscoversClusterByLabel(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	encodedKubeconfig := base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nclusters: []\n"))
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":    `{"data":[{"id":67890,"label":"video-cloud-staging-lke","region":"us-sea"}]}`,
		"/lke/clusters/67890/kubeconfig": `{"kubeconfig":"` + encodedKubeconfig + `"}`,
	})
	kubectlLog := fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if !strings.Contains(curlCalls, "GET /lke/clusters?page_size=500") || !strings.Contains(curlCalls, "GET /lke/clusters/67890/kubeconfig") {
		t.Fatalf("expected cluster list and kubeconfig fetch, got:\n%s", curlCalls)
	}
	state := readTestFile(t, filepath.Join(envRoot, "state", "lke.env"))
	if !strings.Contains(state, "LKE_CLUSTER_ID=67890") || !strings.Contains(state, "LKE_CLUSTER_LABEL=video-cloud-staging-lke") {
		t.Fatalf("expected discovered cluster state, got:\n%s", state)
	}
	if !strings.Contains(readTestFile(t, kubectlLog), "ARGS --kubeconfig "+filepath.Join(envRoot, "state", "lke-kubeconfig.yaml")+" apply -f -") {
		t.Fatalf("expected kubectl to use fetched kubeconfig")
	}
}

func TestRunProvisionLKEApplyCreatesClusterWhenMissing(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	encodedKubeconfig := base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nclusters: []\n"))
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":    `{"data":[]}`,
		"/lke/versions":                  `{"data":[{"id":"1.33"}]}`,
		"/lke/clusters":                  `{"id":24680,"label":"video-cloud-staging-lke","region":"us-sea","k8s_version":"1.33"}`,
		"/lke/clusters/24680/kubeconfig": `{"kubeconfig":"` + encodedKubeconfig + `"}`,
	})
	fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")
	t.Setenv("LKE_NODE_TYPE", "g6-standard-2")
	t.Setenv("LKE_NODE_COUNT", "3")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	for _, want := range []string{
		"GET /lke/clusters?page_size=500",
		"GET /lke/versions",
		"POST /lke/clusters",
		"GET /lke/clusters/24680/kubeconfig",
	} {
		if !strings.Contains(curlCalls, want) {
			t.Fatalf("expected %q in curl log, got:\n%s", want, curlCalls)
		}
	}
	state := readTestFile(t, filepath.Join(envRoot, "state", "lke.env"))
	if !strings.Contains(state, "LKE_CLUSTER_ID=24680") || !strings.Contains(state, "LKE_CLUSTER_VERSION=1.33") {
		t.Fatalf("expected created cluster state, got:\n%s", state)
	}
}

func TestRunProvisionLKEPlanWithoutStackUsesProviderEnv(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	t.Setenv("CLOUD_PROVIDER", "lke")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--plan"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunProvisionLKEDeployRequiresImages(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"})
	if err == nil || !strings.Contains(err.Error(), "LKE deploy requires container image environment variables") {
		t.Fatalf("expected image requirement error, got %v", err)
	}
}

func TestRunProvisionLKEDeployBuildsMissingImagesWhenRegistryConfigured(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectl(t)
	dockerLog := fakeDocker(t)
	t.Setenv("LKE_IMAGE_REGISTRY", "registry.example.test/rtk")
	t.Setenv("LKE_IMAGE_TAG", "testtag")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	dockerCalls := readTestFile(t, dockerLog)
	for _, image := range []string{
		"registry.example.test/rtk/postgresql:testtag",
		"registry.example.test/rtk/video-cloud-api:testtag",
		"registry.example.test/rtk/account-manager:testtag",
		"registry.example.test/rtk/cloud-admin:testtag",
		"registry.example.test/rtk/frontend:testtag",
	} {
		if !strings.Contains(dockerCalls, " -t "+image+" ") {
			t.Fatalf("expected docker build for %s, got:\n%s", image, dockerCalls)
		}
	}
	kubectlCalls := readTestFile(t, kubectlLog)
	if !strings.Contains(kubectlCalls, "image: registry.example.test/rtk/video-cloud-api:testtag") {
		t.Fatalf("expected built image in kubectl manifest, got:\n%s", kubectlCalls)
	}
	if !strings.Contains(kubectlCalls, "image: registry.example.test/rtk/postgresql:testtag") {
		t.Fatalf("expected built PostgreSQL image in kubectl manifest, got:\n%s", kubectlCalls)
	}
}

func TestRunLKEBuildImagesWritesManifest(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	dockerLog := fakeDocker(t)
	out := filepath.Join(t.TempDir(), "lke-images.json")

	if err := runLKEBuildImages([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--registry", "registry.example.test/rtk/lke",
		"--tag", "ci-1234",
		"--out", out,
	}); err != nil {
		t.Fatal(err)
	}

	dockerCalls := readTestFile(t, dockerLog)
	for _, image := range []string{
		"registry.example.test/rtk/lke/postgresql:ci-1234",
		"registry.example.test/rtk/lke/video-cloud-api:ci-1234",
		"registry.example.test/rtk/lke/account-manager:ci-1234",
		"registry.example.test/rtk/lke/cloud-admin:ci-1234",
		"registry.example.test/rtk/lke/frontend:ci-1234",
	} {
		if !strings.Contains(dockerCalls, " -t "+image+" ") {
			t.Fatalf("expected docker build for %s, got:\n%s", image, dockerCalls)
		}
	}
	body := readTestFile(t, out)
	for _, want := range []string{
		`"schema": "rtk-cloud-workspace.lke-image-artifacts/v1"`,
		`"LKE_POSTGRES_IMAGE": "registry.example.test/rtk/lke/postgresql:ci-1234"`,
		`"LKE_VIDEO_CLOUD_IMAGE": "registry.example.test/rtk/lke/video-cloud-api:ci-1234"`,
		`"LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/rtk/lke/account-manager:ci-1234"`,
		`"LKE_CLOUD_ADMIN_IMAGE": "registry.example.test/rtk/lke/cloud-admin:ci-1234"`,
		`"LKE_FRONTEND_IMAGE": "registry.example.test/rtk/lke/frontend:ci-1234"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest missing %q:\n%s", want, body)
		}
	}
}

func TestRunProvisionLKEDeployAppliesRuntimeDependencies(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"kind: StatefulSet\nmetadata:\n  name: postgresql",
		"kind: Secret\nmetadata:\n  name: account-manager-runtime",
		"kind: Job\nmetadata:\n  name: account-manager-migrate",
		"envFrom:\n            - secretRef:\n                name: account-manager-runtime",
		"postgres://postgres:test-seed-postgres@postgresql.video-cloud-staging-platform.svc.cluster.local:5432/rtk_account_manager?sslmode=disable",
		"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL: \"platform-admin@video-cloud-staging.local\"",
		"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD: \"test-seed-platform-admin\"",
		"command: [\"/app/rtk-account-manager-migrate\"]",
		"PGDATA\n              value: /var/lib/postgresql/data/pgdata",
		"name: postgresql-runtime\n                  key: POSTGRES_PASSWORD",
		"kind: Secret\nmetadata:\n  name: certissuer-runtime",
		"kind: Secret\nmetadata:\n  name: account-manager-certissuer-client",
		"kind: Deployment\nmetadata:\n  name: certissuer",
		"command: [\"/app/certissuer\"]",
		"containerPort: 9443",
		"APP_CERT_ISSUER_BASE_URL: \"https://certissuer.video-cloud-staging-video-cloud.svc.cluster.local:9443\"",
		"APP_CERT_ISSUER_CLIENT_CERT: \"/etc/rtk-account-manager/certissuer/client.crt\"",
		"name: account-manager-certissuer-client",
		"kind: Secret\nmetadata:\n  name: factoryenroll-runtime",
		"kind: Secret\nmetadata:\n  name: factoryenroll-certissuer-client",
		"kind: Deployment\nmetadata:\n  name: factoryenroll",
		"command: [\"/app/factoryenroll\"]",
		"FACTORY_ENROLL_CERT_ISSUER_URL\n              value: \"https://certissuer.video-cloud-staging-video-cloud.svc.cluster.local:9443\"",
		"FACTORY_ENROLL_AUTH_KEY",
		"kind: Secret\nmetadata:\n  name: video-cloud-runtime",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN",
		"VIDEO_CLOUD_DB_DSN",
		"VIDEO_CLOUD_API_ADDR\n              value: \":8080\"",
		"VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS\n              value: \"true\"",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL\n              value: \"http://account-manager.video-cloud-staging-account-manager.svc.cluster.local:80\"",
		"kind: Secret\nmetadata:\n  name: mqtt-runtime",
		"kind: ConfigMap\nmetadata:\n  name: mqtt-config",
		"listener 8883 0.0.0.0",
		"allow_anonymous true",
		"kind: Deployment\nmetadata:\n  name: mqtt",
		"image: eclipse-mosquitto:",
		"containerPort: 8883",
		"kind: Service\nmetadata:\n  name: mqtt",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
}

func TestLKEPostgresStatefulSetDefaultsToEphemeralStorageForStagingBridge(t *testing.T) {
	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	if !strings.Contains(manifest, "emptyDir: {}") {
		t.Fatalf("expected default staging PostgreSQL manifest to use emptyDir, got:\n%s", manifest)
	}
	if strings.Contains(manifest, "volumeClaimTemplates") {
		t.Fatalf("default staging PostgreSQL manifest should not create Linode Block Storage PVCs, got:\n%s", manifest)
	}
}

func TestLKEPostgresStatefulSetSupportsExplicitPVCStorage(t *testing.T) {
	t.Setenv("LKE_POSTGRES_STORAGE_MODE", "pvc")
	t.Setenv("LKE_POSTGRES_STORAGE", "20Gi")

	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	for _, want := range []string{
		"volumeClaimTemplates:",
		"storage: 20Gi",
		"name: data",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in explicit PVC manifest, got:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "emptyDir: {}") {
		t.Fatalf("explicit PVC manifest should not use emptyDir, got:\n%s", manifest)
	}
}

func TestLKEPostgresStatefulSetUsesPostgresImageOverride(t *testing.T) {
	t.Setenv("LKE_POSTGRES_IMAGE", "registry.example.test/rtk/lke/postgresql:ci-1234")

	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	if !strings.Contains(manifest, "image: registry.example.test/rtk/lke/postgresql:ci-1234") {
		t.Fatalf("expected LKE_POSTGRES_IMAGE in PostgreSQL manifest, got:\n%s", manifest)
	}
}

func TestRunProvisionLKEDeployAppliesVideoCloudAuxiliaryServices(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"kind: Secret\nmetadata:\n  name: video-cloud-workers-runtime",
		"VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY: \"test-seed-turn-registry-node-auth\"",
		"VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN: \"test-seed-mqtt-usage-ingest\"",
		"kind: Deployment\nmetadata:\n  name: video-cloud-cleaner",
		"command: [\"/app/cleaner\"]",
		"kind: Deployment\nmetadata:\n  name: video-cloud-statistics",
		"command: [\"/app/statistics\"]",
		"kind: Deployment\nmetadata:\n  name: video-cloud-metricsexporter",
		"command: [\"/app/metricsexporter\"]",
		"containerPort: 19200",
		"kind: Deployment\nmetadata:\n  name: video-cloud-turnregistry",
		"command: [\"/app/turnregistry\"]",
		"containerPort: 18190",
		"kind: Deployment\nmetadata:\n  name: video-cloud-logingester",
		"command: [\"/app/logingester\"]",
		"containerPort: 19300",
		"VIDEO_CLOUD_MQTT_ADDR\n              value: \"mqtt.video-cloud-staging-video-cloud.svc.cluster.local:1883\"",
		"kind: Deployment\nmetadata:\n  name: video-cloud-mqttusage",
		"command: [\"/app/mqttusage\"]",
		"containerPort: 19400",
		"kind: Service\nmetadata:\n  name: video-cloud-turnregistry",
		"kind: Service\nmetadata:\n  name: video-cloud-logingester",
		"kind: ConfigMap\nmetadata:\n  name: video-cloud-prometheus-config",
		"kind: Deployment\nmetadata:\n  name: video-cloud-prometheus",
		"image: prom/prometheus:",
		"targets: [\"video-cloud-api.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
		"targets: [\"video-cloud-metricsexporter.video-cloud-staging-video-cloud.svc.cluster.local:19200\"]",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
	for _, want := range []string{
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-cleaner",
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-statistics",
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-metricsexporter",
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-turnregistry",
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-logingester",
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-mqttusage",
		"ARGS -n video-cloud-staging-observability rollout status deployment/video-cloud-prometheus",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected rollout check %q in kubectl calls, got:\n%s", want, log)
		}
	}
}

func TestRunProvisionLKEDeployAppliesCoturnRuntime(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"kind: Secret\nmetadata:\n  name: coturn-runtime",
		"VIDEO_CLOUD_TURN_SHARED_SECRET: \"test-seed-turn-shared\"",
		"kind: ConfigMap\nmetadata:\n  name: coturn-config",
		"use-auth-secret",
		"static-auth-secret=$(VIDEO_CLOUD_TURN_SHARED_SECRET)",
		"realm=video_cloud",
		"kind: Deployment\nmetadata:\n  name: coturn",
		"image: coturn/coturn:",
		"initContainers:",
		"command: [\"/usr/bin/turnserver\", \"-c\", \"/tmp/coturn/turnserver.conf\"]",
		"containerPort: 3478\n              protocol: UDP",
		"kind: Service\nmetadata:\n  name: coturn",
		"type: ClusterIP",
		"port: 3478\n      targetPort: 3478\n      protocol: UDP",
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/coturn",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}

	state := readTestFile(t, filepath.Join(envRoot, "state", "video-cloud.state.json"))
	for _, want := range []string{
		`"coturn"`,
		`"private_ip": "coturn.video-cloud-staging-video-cloud.svc.cluster.local"`,
		`"role": "deployment/coturn"`,
	} {
		if !strings.Contains(state, want) {
			t.Fatalf("expected %q in video state, got:\n%s", want, state)
		}
	}
}

func TestRunProvisionLKEDeployWritesLegacyStackAndVideoState(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	stack := readTestFile(t, filepath.Join(envRoot, "env", "stack.env"))
	for _, want := range []string{
		"CLOUD_PROVIDER=lke",
		"CLOUD_STACK_NAME=video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN=video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com",
	} {
		if !strings.Contains(stack, want) {
			t.Fatalf("expected %q in stack.env, got:\n%s", want, stack)
		}
	}

	state := readTestFile(t, filepath.Join(envRoot, "state", "video-cloud.state.json"))
	for _, want := range []string{
		`"provider": "lke"`,
		`"stack": "video-cloud-staging"`,
		`"mqtt"`,
		`"private_ip": "mqtt.video-cloud-staging-video-cloud.svc.cluster.local"`,
	} {
		if !strings.Contains(state, want) {
			t.Fatalf("expected %q in video state, got:\n%s", want, state)
		}
	}
}

func TestRunProvisionLKEDeployWritesPlatformAdminEnv(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	body := readTestFile(t, filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env"))
	if !strings.Contains(body, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=platform-admin@video-cloud-staging.local") ||
		!strings.Contains(body, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=test-seed-platform-admin") {
		t.Fatalf("unexpected platform admin env:\n%s", body)
	}
	info, err := os.Stat(filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("platform admin env permissions got %o want 600", info.Mode().Perm())
	}
}

func TestRunProvisionLKEDeployWritesVideoCloudRuntimeEnv(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	body := readTestFile(t, filepath.Join(envRoot, "services", "video-cloud", "video-cloud.env"))
	if !strings.Contains(body, "FACTORY_ENROLL_AUTH_KEY=test-seed-factory-enroll-auth") ||
		!strings.Contains(body, "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN=test-seed-internal-auth") {
		t.Fatalf("unexpected video cloud runtime env:\n%s", body)
	}
	info, err := os.Stat(filepath.Join(envRoot, "services", "video-cloud", "video-cloud.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("video cloud runtime env permissions got %o want 600", info.Mode().Perm())
	}
}

func TestRunProvisionLKEDeployWritesAccountManagerRuntimeEnv(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	body := readTestFile(t, filepath.Join(envRoot, "services", "account-manager", "account-manager.env"))
	if !strings.Contains(body, "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=test-seed-internal-auth") {
		t.Fatalf("unexpected account-manager runtime env:\n%s", body)
	}
	info, err := os.Stat(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("account-manager runtime env permissions got %o want 600", info.Mode().Perm())
	}
}

func TestGeneratedGoServiceDockerfileUsesGoModVersion(t *testing.T) {
	contextDir := t.TempDir()
	writeTestFile(t, filepath.Join(contextDir, "go.mod"), "module example.test/service\n\ngo 1.26.3\n")

	_, dockerfile, cleanup, err := generatedGoServiceDockerfile(contextDir, "./cmd/server", "service")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	body := readTestFile(t, dockerfile)
	if !strings.Contains(body, "FROM golang:1.26-bookworm AS builder") {
		t.Fatalf("expected Dockerfile to use go.mod major.minor builder image, got:\n%s", body)
	}
}

func TestVideoCloudDockerfileIncludesCertIssuerBinary(t *testing.T) {
	contextDir := t.TempDir()
	writeTestFile(t, filepath.Join(contextDir, "go.mod"), "module video_cloud\n\ngo 1.25.1\n")

	_, dockerfile, cleanup, err := generatedVideoCloudDockerfile(contextDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	body := readTestFile(t, dockerfile)
	for _, want := range []string{
		"FROM golang:1.25-bookworm AS builder",
		"go build -trimpath -o /out/api ./cmd/api",
		"go build -trimpath -o /out/certissuer ./cmd/certissuer",
		"go build -trimpath -o /out/factoryenroll ./cmd/factoryenroll",
		"go build -trimpath -o /out/cleaner ./cmd/cleaner",
		"go build -trimpath -o /out/statistics ./cmd/statistics",
		"go build -trimpath -o /out/metricsexporter ./cmd/metricsexporter",
		"go build -trimpath -o /out/turnregistry ./cmd/turnregistry",
		"go build -trimpath -o /out/logingester ./cmd/logingester",
		"go build -trimpath -o /out/mqttusage ./cmd/mqttusage",
		"COPY --from=builder /out/certissuer /app/certissuer",
		"COPY --from=builder /out/factoryenroll /app/factoryenroll",
		"COPY --from=builder /out/cleaner /app/cleaner",
		"COPY --from=builder /out/statistics /app/statistics",
		"COPY --from=builder /out/metricsexporter /app/metricsexporter",
		"COPY --from=builder /out/turnregistry /app/turnregistry",
		"COPY --from=builder /out/logingester /app/logingester",
		"COPY --from=builder /out/mqttusage /app/mqttusage",
		"ENTRYPOINT [\"/app/api\"]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in Dockerfile, got:\n%s", want, body)
		}
	}
}

func TestAccountManagerDockerfileIncludesMigrateBinaryAndMigrations(t *testing.T) {
	contextDir := t.TempDir()
	writeTestFile(t, filepath.Join(contextDir, "go.mod"), "module rtk_account_manager\n\ngo 1.24.4\n")

	_, dockerfile, cleanup, err := generatedAccountManagerDockerfile(contextDir)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	body := readTestFile(t, dockerfile)
	for _, want := range []string{
		"go build -trimpath -o /out/rtk-account-manager ./cmd/server",
		"go build -trimpath -o /out/rtk-account-manager-migrate ./cmd/migrate",
		"COPY --from=builder /out/rtk-account-manager-migrate /app/rtk-account-manager-migrate",
		"COPY --from=builder /src/migrations /app/migrations",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in Dockerfile, got:\n%s", want, body)
		}
	}
}

func TestRunDeployLKEVideoOnlyUsesVideoImage(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")

	if err := runDeploy([]string{"--workspace", workspace, "--env-root", envRoot, "--video-only"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if !strings.Contains(log, "ARGS version --client") {
		t.Fatalf("expected preflight kubectl version, got:\n%s", log)
	}
	if !strings.Contains(log, "name: video-cloud-api") {
		t.Fatalf("expected video-cloud deployment manifest, got:\n%s", log)
	}
	if strings.Contains(log, "name: account-manager") || strings.Contains(log, "name: cloud-admin") {
		t.Fatalf("video-only deploy should not apply account-manager/admin manifests:\n%s", log)
	}
}

func TestRunRemoveAllVMLKERemovesNamespaces(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)

	if err := runRemoveAllVM([]string{"--workspace", workspace, "--env-root", envRoot, "--yes"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if !strings.Contains(log, "ARGS delete namespace --ignore-not-found") {
		t.Fatalf("expected namespace delete, got:\n%s", log)
	}
	if strings.Contains(log, "/linode/instances") {
		t.Fatalf("LKE removal should not call Linode VM APIs:\n%s", log)
	}
}

func TestRunRemoveAllVMLKEDoesNotCreateMissingCluster(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500": `{"data":[]}`,
	})
	fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")

	if err := runRemoveAllVM([]string{"--workspace", workspace, "--env-root", envRoot, "--yes"}); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if strings.Contains(curlCalls, "POST /lke/clusters") {
		t.Fatalf("remove should not create LKE clusters, got:\n%s", curlCalls)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "state", "lke.env")); !os.IsNotExist(err) {
		t.Fatalf("remove should not create LKE state when cluster is missing, stat err=%v", err)
	}
}

func TestAccountManagerContextForLKEUsesPortForward(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectl(t)
	t.Setenv("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", "0s")
	writeTestFile(t, filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env"), "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=admin@example.test\nACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=password123\n")

	ctx, err := accountManagerContextFromFlags(workspace, envRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()

	if !strings.HasPrefix(ctx.BaseURL, "http://127.0.0.1:") {
		t.Fatalf("expected local port-forward base URL, got %q", ctx.BaseURL)
	}
	if ctx.Host != "" {
		t.Fatalf("LKE context should not require VM host, got %q", ctx.Host)
	}
	time.Sleep(100 * time.Millisecond)
	ctx.Close()
	log := readTestFile(t, kubectlLog)
	if !strings.Contains(log, "ARGS -n video-cloud-staging-account-manager port-forward svc/account-manager") {
		t.Fatalf("expected account-manager port-forward, got:\n%s", log)
	}
}

func TestLKEFactoryEnrollPortForward(t *testing.T) {
	_, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectl(t)
	t.Setenv("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", "0s")

	baseURL, cleanup, err := lkeFactoryEnrollPortForward(envRoot, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		t.Fatalf("expected local port-forward base URL, got %q", baseURL)
	}
	time.Sleep(100 * time.Millisecond)
	cleanup()
	log := readTestFile(t, kubectlLog)
	if !strings.Contains(log, "ARGS -n video-cloud-staging-video-cloud port-forward svc/factoryenroll") {
		t.Fatalf("expected factoryenroll port-forward, got:\n%s", log)
	}
}

func TestStagingProvisionBridgeForLKEUsesVideoCloudPortForward(t *testing.T) {
	_, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectl(t)
	t.Setenv("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", "0s")
	writeTestFile(t, filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=test-token\n")
	writeTestFile(t, filepath.Join(envRoot, "services", "video-cloud", "video-cloud.env"), "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN=test-token\n")

	bridge, err := stagingProvisionBridgeFromEnvRoot(accountManagerContext{
		EnvRoot: envRoot,
		BaseURL: "http://127.0.0.1:12345",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bridge.VideoBaseURL, "http://127.0.0.1:") {
		t.Fatalf("expected video-cloud-api port-forward base URL, got %q", bridge.VideoBaseURL)
	}
	time.Sleep(100 * time.Millisecond)
	bridge.Close()
	log := readTestFile(t, kubectlLog)
	if !strings.Contains(log, "ARGS -n video-cloud-staging-video-cloud port-forward svc/video-cloud-api") {
		t.Fatalf("expected video-cloud-api port-forward, got:\n%s", log)
	}
}

func TestRunMQTTTestForLKEUsesServicePortForwards(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectl(t)
	goLog := fakeGoForMQTTTest(t)
	t.Setenv("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", "0s")
	if err := os.MkdirAll(filepath.Join(workspace, "scripts", "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
`)

	if err := runMQTTTest([]string{"--workspace", workspace, "--env-root", envRoot, "--brandname", "RTK", "--duration-seconds", "1"}); err != nil {
		t.Fatal(err)
	}

	kubectlCalls := readTestFile(t, kubectlLog)
	for _, want := range []string{
		"ARGS -n video-cloud-staging-video-cloud port-forward svc/mqtt",
		"ARGS -n video-cloud-staging-video-cloud port-forward svc/video-cloud-api",
		"ARGS -n video-cloud-staging-account-manager port-forward svc/account-manager",
	} {
		if !strings.Contains(kubectlCalls, want) {
			t.Fatalf("expected %q in kubectl calls, got:\n%s", want, kubectlCalls)
		}
	}
	goCalls := readTestFile(t, goLog)
	for _, want := range []string{
		"RTK_CLOUD_MQTT_TEST_MQTT_HOST=127.0.0.1",
		"RTK_CLOUD_MQTT_TEST_MQTT_PORT=",
		"RTK_CLOUD_MQTT_TEST_VIDEO_BASE_URL=http://127.0.0.1:",
		"RTK_CLOUD_MQTT_TEST_ACCOUNT_BASE_URL=http://127.0.0.1:",
	} {
		if !strings.Contains(goCalls, want) {
			t.Fatalf("expected %q in fake go env, got:\n%s", want, goCalls)
		}
	}
}

func TestAccountBootstrapNoopsForLKEPortForwardContext(t *testing.T) {
	err := accountBootstrap(accountManagerContext{
		EnvRoot: "/tmp/env-root",
		BaseURL: "http://127.0.0.1:12345",
	})
	if err != nil {
		t.Fatalf("LKE port-forward bootstrap should not require VM SSH: %v", err)
	}
}

func TestLinodeRequestRawIncludesEndpointOnCurlFailure(t *testing.T) {
	dir := t.TempDir()
	curlPath := filepath.Join(dir, "curl")
	if err := os.WriteFile(curlPath, []byte(`#!/usr/bin/env bash
set -euo pipefail
printf 'upstream unavailable\n' >&2
exit 22
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := linodeRequestRaw("test-token", "GET", "/lke/versions", "")
	if err == nil {
		t.Fatal("expected curl failure")
	}
	message := err.Error()
	if !strings.Contains(message, "GET /lke/versions") || !strings.Contains(message, "upstream unavailable") {
		t.Fatalf("expected endpoint and stderr in error, got %q", message)
	}
}

func TestFetchLKEKubeconfigRetriesTransientUnavailable(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	curlPath := filepath.Join(dir, "curl")
	encoded := base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nclusters: []\n"))
	if err := os.WriteFile(curlPath, []byte(`#!/usr/bin/env bash
set -euo pipefail
count_file="`+counter+`"
count=0
if [[ -f "$count_file" ]]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s' "$count" > "$count_file"
if [[ "$count" == "1" ]]; then
  printf 'curl: (22) The requested URL returned error: 503\n' >&2
  exit 22
fi
cat <<'JSON'
{"kubeconfig":"`+encoded+`"}
JSON
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LKE_KUBECONFIG_RETRY_DELAY", "0s")
	t.Setenv("LKE_KUBECONFIG_RETRY_ATTEMPTS", "2")

	kubeconfig, err := fetchLKEKubeconfig("test-token", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if string(kubeconfig) != "apiVersion: v1\nclusters: []\n" {
		t.Fatalf("unexpected kubeconfig %q", string(kubeconfig))
	}
	if calls := readTestFile(t, counter); calls != "2" {
		t.Fatalf("expected two curl attempts, got %s", calls)
	}
}

func makeLKETestEnv(t *testing.T) (string, string) {
	t.Helper()
	t.Cleanup(func() {
		_ = os.Unsetenv("RTK_CLOUD_LKE_KUBECONFIG")
	})
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte(`CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return workspace, envRoot
}

func fakeKubectl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "config" && "${2:-}" == "current-context" ]]; then
  printf 'test-context\n'
  exit 0
fi
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
  if [[ "${1:-}" == "apply" ]]; then
    cat
    printf '\n---\n'
  fi
} >> "` + logPath + `"
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	return logPath
}

func fakeKubectlWithoutCurrentContext(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "config" && "${2:-}" == "current-context" ]]; then
  exit 1
fi
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
  if [[ "${*: -2}" == "-f -" ]]; then
    cat
    printf '\n---\n'
  fi
} >> "` + logPath + `"
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	return logPath
}

func fakeGoForMQTTTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "go.log")
	goPath := filepath.Join(dir, "go")
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
  env | grep '^RTK_CLOUD_MQTT_TEST_' | sort
} >> "` + logPath + `"
`
	if err := os.WriteFile(goPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func fakeLinodeCurl(t *testing.T, responses map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "curl.log")
	curlPath := filepath.Join(dir, "curl")
	script := `#!/usr/bin/env bash
set -euo pipefail
method="GET"
url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -X)
      method="$2"
      shift 2
      ;;
    http*)
      url="$1"
      shift
      ;;
    *)
      shift
      ;;
  esac
done
path="${url#https://api.linode.com/v4}"
printf '%s %s\n' "$method" "$path" >> "` + logPath + `"
case "$path" in
`
	for path, body := range responses {
		script += path + `)
  cat <<'JSON'
` + body + `
JSON
  ;;
`
	}
	script += `*)
  printf 'unexpected path: %s\n' "$path" >&2
  exit 22
  ;;
esac
`
	if err := os.WriteFile(curlPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func fakeDocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	dockerPath := filepath.Join(dir, "docker")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf 'ARGS' >> "` + logPath + `"
for arg in "$@"; do
  printf ' %s' "$arg" >> "` + logPath + `"
done
printf '\n' >> "` + logPath + `"
`
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
