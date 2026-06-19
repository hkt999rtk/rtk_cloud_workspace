package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestRunProvisionLKEApplyInstallsMetricsServer(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"ARGS apply -f https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.8.1/components.yaml",
		"ARGS -n kube-system rollout status deployment/metrics-server --timeout 5m",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected metrics-server install to include %q, got:\n%s", want, log)
		}
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

func TestEnsureK8SKubeconfigPrefersEnvRootState(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	t.Setenv("CLOUD_STAGING_K8S_KUBECONFIG", "")
	t.Setenv("KUBECONFIG", "")
	t.Setenv("LINODE_TOKEN", "")
	envRootKubeconfig := filepath.Join(envRoot, "state", "lke-kubeconfig.yaml")
	workspaceKubeconfig := filepath.Join(workspace, ".artifacts", "kube", "video-cloud-staging-lke.kubeconfig")
	writeTestFile(t, envRootKubeconfig, "env-root kubeconfig\n")
	writeTestFile(t, workspaceKubeconfig, "stale workspace kubeconfig\n")

	got, err := ensureK8SKubeconfig(workspace, envRoot, "video-cloud-staging")
	if err != nil {
		t.Fatal(err)
	}
	if got != envRootKubeconfig {
		t.Fatalf("kubeconfig got %s want %s", got, envRootKubeconfig)
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

func TestRunProvisionLKEDeployUsesImageManifestDefaults(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "lke-images", "lke-image-manifest.json"), `{
  "env": {
    "LKE_POSTGRES_IMAGE": "postgres:16-alpine",
    "LKE_VIDEO_CLOUD_IMAGE": "registry.example.test/rtk/video-cloud:manifest",
    "LKE_ACCOUNT_MANAGER_IMAGE": "registry.example.test/rtk/account-manager:manifest",
    "LKE_CLOUD_ADMIN_IMAGE": "registry.example.test/rtk/cloud-admin:manifest",
    "LKE_FRONTEND_IMAGE": "registry.example.test/rtk/frontend:manifest",
    "LKE_CLOUD_LOGGER_IMAGE": "registry.example.test/rtk/cloud-logger:manifest"
  }
}`)

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"image: registry.example.test/rtk/video-cloud:manifest",
		"image: registry.example.test/rtk/account-manager:manifest",
		"image: registry.example.test/rtk/cloud-admin:manifest",
		"image: registry.example.test/rtk/frontend:manifest",
		"image: registry.example.test/rtk/cloud-logger:manifest",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q from image manifest defaults, got:\n%s", want, log)
		}
	}
}

func TestRunProvisionLKEDeployDoesNotBuildServiceImagesWhenRegistryConfigured(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	dockerLog := fakeDocker(t)
	t.Setenv("LKE_IMAGE_REGISTRY", "registry.example.test/rtk")
	t.Setenv("LKE_IMAGE_TAG", "testtag")

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"})
	if err == nil || !strings.Contains(err.Error(), "LKE deploy requires container image environment variables") {
		t.Fatalf("expected explicit image requirement error, got %v", err)
	}
	dockerCalls, err := os.ReadFile(dockerLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if string(dockerCalls) != "" {
		t.Fatalf("expected no docker calls during deploy image validation, got:\n%s", dockerCalls)
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
	} {
		if !strings.Contains(dockerCalls, " -t "+image+" ") {
			t.Fatalf("expected docker build for %s, got:\n%s", image, dockerCalls)
		}
	}
	for _, image := range []string{
		"registry.example.test/rtk/lke/video-cloud-api:ci-1234",
		"registry.example.test/rtk/lke/account-manager:ci-1234",
		"registry.example.test/rtk/lke/cloud-admin:ci-1234",
		"registry.example.test/rtk/lke/frontend:ci-1234",
	} {
		if strings.Contains(dockerCalls, " -t "+image+" ") {
			t.Fatalf("did not expect service image build for %s, got:\n%s", image, dockerCalls)
		}
	}
	body := readTestFile(t, out)
	for _, want := range []string{
		`"schema": "rtk-cloud-workspace.lke-image-artifacts/v1"`,
		`"LKE_POSTGRES_IMAGE": "registry.example.test/rtk/lke/postgresql:ci-1234"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest missing %q:\n%s", want, body)
		}
	}
	for _, notWant := range []string{
		"LKE_VIDEO_CLOUD_IMAGE",
		"LKE_ACCOUNT_MANAGER_IMAGE",
		"LKE_CLOUD_ADMIN_IMAGE",
		"LKE_FRONTEND_IMAGE",
		"LKE_CLOUD_LOGGER_IMAGE",
	} {
		if strings.Contains(body, notWant) {
			t.Fatalf("legacy build manifest should not include %s:\n%s", notWant, body)
		}
	}
}

func TestRunLKEResolveImagesWritesPinnedSubmoduleManifest(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	commits := makeLKEServiceRepos(t, workspace)
	out := filepath.Join(t.TempDir(), "lke-images.json")

	if err := runLKEResolveImages([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--owner", "hkt999rtk",
		"--skip-verify",
		"--out", out,
	}); err != nil {
		t.Fatal(err)
	}

	body := readTestFile(t, out)
	for _, want := range []string{
		`"schema": "rtk-cloud-workspace.lke-image-manifest/v1"`,
		`"LKE_POSTGRES_IMAGE": "postgres:16-alpine"`,
		`"LKE_VIDEO_CLOUD_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api:sha-` + commits["rtk_video_cloud"] + `"`,
		`"LKE_ACCOUNT_MANAGER_IMAGE": "ghcr.io/hkt999rtk/rtk_account_manager/account-manager:sha-` + commits["rtk_account_manager"] + `"`,
		`"LKE_CLOUD_ADMIN_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-` + commits["rtk_cloud_admin"] + `"`,
		`"LKE_FRONTEND_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend:sha-` + commits["rtk_cloud_frontend"] + `"`,
		`"LKE_CLOUD_LOGGER_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_logger/rtk-cloud-logger:sha-` + commits["rtk_cloud_logger"] + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest missing %q:\n%s", want, body)
		}
	}
}

func TestRunLKEResolveImagesFailsWhenServiceImageIsMissing(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	commits := makeLKEServiceRepos(t, workspace)
	inspected := []string{}
	oldInspect := inspectLKEImage
	inspectLKEImage = func(image string) error {
		inspected = append(inspected, image)
		if strings.Contains(image, "/frontend:") {
			return errLKEImageNotFound
		}
		return nil
	}
	t.Cleanup(func() { inspectLKEImage = oldInspect })

	err := runLKEResolveImages([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--owner", "hkt999rtk",
	})
	if err == nil {
		t.Fatal("expected missing image error")
	}
	wantImage := "ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend:sha-" + commits["rtk_cloud_frontend"]
	if !strings.Contains(err.Error(), wantImage) || !strings.Contains(err.Error(), "repos/rtk_cloud_frontend") {
		t.Fatalf("expected missing image and repo path in error, got %v", err)
	}
	if len(inspected) != 4 {
		t.Fatalf("expected four service image inspections before frontend failure, got %d: %v", len(inspected), inspected)
	}
}

func TestRunProvisionLKEDeployAppliesRuntimeDependencies(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"kind: StatefulSet\nmetadata:\n  name: postgresql",
		"kind: Deployment\nmetadata:\n  name: redis",
		"image: valkey/valkey:8-alpine",
		"containerPort: 6379",
		"kind: Service\nmetadata:\n  name: redis",
		"kind: Deployment\nmetadata:\n  name: redis-exporter",
		"image: oliver006/redis_exporter:v1.74.0",
		"REDIS_ADDR\n              value: \"redis://redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"containerPort: 9121",
		"kind: Service\nmetadata:\n  name: redis-exporter",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-redis-clients",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-prometheus-scrape",
		"kind: Secret\nmetadata:\n  name: openbao-tls",
		"namespace: video-cloud-staging-secrets",
		"ca.crt:",
		"ARGS -n video-cloud-staging-secrets get pod/openbao-0 -o jsonpath={.status.phase}",
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
		"root-ca.crt: \"-----BEGIN CERTIFICATE-----\\ntest-root-ca\\n-----END CERTIFICATE-----\\n\"",
		"device-ca.crt: \"-----BEGIN CERTIFICATE-----\\ntest-device-ca\\n-----END CERTIFICATE-----\\n\"",
		"app-ca.crt: \"-----BEGIN CERTIFICATE-----\\ntest-app-ca\\n-----END CERTIFICATE-----\\n\"",
		"kind: Secret\nmetadata:\n  name: certissuer-openbao-auth",
		"role_id: \"test-role-id\"",
		"secret_id: \"test-secret-id\"",
		"kind: Secret\nmetadata:\n  name: account-manager-certissuer-client",
		"kind: Deployment\nmetadata:\n  name: certissuer",
		"command: [\"/app/certissuer\"]",
		"containerPort: 9443",
		"CERT_ISSUER_SIGNER_PROVIDER\n              value: openbao",
		"CERT_ISSUER_OPENBAO_PKI_MOUNT\n              value: pki/device",
		"CERT_ISSUER_OPENBAO_PKI_ROLE\n              value: factory-device",
		"CERT_ISSUER_APP_SIGNER_PROVIDER\n              value: openbao",
		"OPENBAO_ADDR\n              value: \"https://openbao.video-cloud-staging-secrets.svc.cluster.local:8200\"",
		"OPENBAO_ROLE_ID_FILE\n              value: /etc/video-cloud/openbao/role_id",
		"name: certissuer-openbao-auth",
		"APP_CERT_ISSUER_BASE_URL: \"https://certissuer.video-cloud-staging-video-cloud.svc.cluster.local:9443\"",
		"APP_CERT_ISSUER_CLIENT_CERT: \"/etc/rtk-account-manager/certissuer/client.crt\"",
		"name: account-manager-certissuer-client",
		"kind: Secret\nmetadata:\n  name: factoryenroll-runtime",
		"kind: Secret\nmetadata:\n  name: factoryenroll-certissuer-client",
		"kind: Deployment\nmetadata:\n  name: factoryenroll",
		"rtk.realtek.com/runtime-checksum",
		"command: [\"/app/factoryenroll\"]",
		"FACTORY_ENROLL_CERT_ISSUER_URL\n              value: \"https://certissuer.video-cloud-staging-video-cloud.svc.cluster.local:9443\"",
		"FACTORY_ENROLL_AUTH_KEY",
		"kind: Secret\nmetadata:\n  name: video-cloud-runtime",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN",
		"VIDEO_CLOUD_DB_DSN",
		"VIDEO_CLOUD_API_ADDR\n              value: \":8080\"",
		"VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS\n              value: \"true\"",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL\n              value: \"http://account-manager.video-cloud-staging-account-manager.svc.cluster.local:80\"",
		"VIDEO_CLOUD_MQTT_ENABLED\n              value: \"true\"",
		"VIDEO_CLOUD_MQTT_ADDR\n              value: \"mqtt.video-cloud-staging-video-cloud.svc.cluster.local:1883\"",
		"POD_NAME\n              valueFrom:",
		"fieldPath: metadata.name",
		"VIDEO_CLOUD_MQTT_CLIENT_ID\n              value: \"video-cloud-api-$(POD_NAME)\"",
		"VIDEO_CLOUD_MQTT_TOPIC_ROOT\n              value: \"devices\"",
		"VIDEO_CLOUD_SHADOW_CACHE_ENABLED\n              value: \"true\"",
		"VIDEO_CLOUD_SHADOW_CACHE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"kind: Secret\nmetadata:\n  name: mqtt-runtime",
		"cert.pem:",
		"key.pem:",
		"cacert.pem:",
		"kind: ConfigMap\nmetadata:\n  name: mqtt-config",
		"broker: emqx",
		"kind: Deployment\nmetadata:\n  name: mqtt",
		"replicas: 1",
		"maxSurge: 0",
		"maxUnavailable: 1",
		"image: emqx/emqx:",
		"EMQX_NODE__NAME",
		"EMQX_CLUSTER__DISCOVERY_STRATEGY",
		`value: "manual"`,
		"EMQX_CLUSTER__DNS__NAME",
		"mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local",
		"whenUnsatisfiable: DoNotSchedule",
		"requiredDuringSchedulingIgnoredDuringExecution:",
		"EMQX_LISTENERS__SSL__DEFAULT__ACCEPTORS",
		`value: "128"`,
		"EMQX_LISTENERS__SSL__DEFAULT__TCP_OPTIONS__BACKLOG",
		`value: "8192"`,
		"emqx ctl cluster join emqx@10.2.0.1",
		"emqx ctl cluster status",
		"EMQX_LISTENERS__TCP__DEFAULT__BIND",
		"EMQX_LISTENERS__SSL__DEFAULT__BIND",
		"EMQX_LISTENERS__SSL__DEFAULT__SSL_OPTIONS__CERTFILE",
		"mountPath: /opt/emqx/etc/certs",
		"containerPort: 8883",
		"kind: Service\nmetadata:\n  name: mqtt",
		"kind: Service\nmetadata:\n  name: mqtt-headless",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-emqx-cluster",
		"port: 4369",
		"port: 4370",
		"port: 5369",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
	if strings.Contains(log, "device-ca.key") || strings.Contains(log, "app-ca.key") {
		t.Fatalf("certissuer runtime must not mount CA private keys, got:\n%s", log)
	}
	for _, want := range []string{
		"ARGS -n video-cloud-staging-platform rollout status deployment/redis",
		"ARGS -n video-cloud-staging-platform rollout status deployment/redis-exporter",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected rollout check %q in kubectl calls, got:\n%s", want, log)
		}
	}
	openBaoIndex := strings.Index(log, "name: openbao-tls")
	certIssuerIndex := strings.Index(log, "name: certissuer-runtime")
	if openBaoIndex < 0 || certIssuerIndex < 0 || openBaoIndex > certIssuerIndex {
		t.Fatalf("expected OpenBao resources before certissuer runtime secret, got:\n%s", log)
	}
}

func TestRunProvisionLKEDeployCanExposePublicMQTTLoadBalancer(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
	t.Setenv("LKE_PUBLIC_MQTT_LOADBALANCER", "1")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"kind: Service\nmetadata:\n  name: mqtt-public",
		"type: LoadBalancer",
		"externalTrafficPolicy: Local",
		"name: mqtts\n      port: 8883",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-public-mqtt-loadtest",
		"port: 8883",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
}

func TestLKEAllowEMQXClusterNetworkPolicyManifestIsValidYAMLShape(t *testing.T) {
	manifest := lkeAllowEMQXClusterNetworkPolicyManifest(map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging",
	})
	if strings.Contains(manifest, "\t") {
		t.Fatalf("EMQX cluster network policy must not contain tabs:\n%s", manifest)
	}
	for _, want := range []string{
		"        - protocol: TCP\n          port: 4369",
		"        - protocol: TCP\n          port: 4370",
		"        - protocol: TCP\n          port: 5369",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected EMQX cluster port entry %q, got:\n%s", want, manifest)
		}
	}
}

func TestRunProvisionLKEDeployCanExposeMultiplePublicMQTTLoadBalancers(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
	t.Setenv("LKE_PUBLIC_MQTT_LOADBALANCER", "1")
	t.Setenv("LKE_PUBLIC_MQTT_LOADBALANCER_COUNT", "3")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"kind: Service\nmetadata:\n  name: mqtt-public",
		"kind: Service\nmetadata:\n  name: mqtt-public-01",
		"kind: Service\nmetadata:\n  name: mqtt-public-02",
		"externalTrafficPolicy: Local",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
}

func TestRunProvisionLKEDNSAppliesPublicHTTPSEdge(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	kubectlLog := fakeKubectl(t)
	helmLog := fakeHelm(t)
	goLog := fakeGoForDNS(t)
	certbotLog := fakeCertbot(t)
	digLog := fakeDig(t, "203.0.113.42")
	t.Setenv("LKE_PUBLIC_HTTPS_ISSUE_EMAIL", "ops@example.test")
	t.Setenv("LKE_PUBLIC_HTTPS_ACME_SERVER", "https://acme-staging-v02.api.letsencrypt.org/directory")
	t.Setenv("GODADDY_KEY", "test-key")
	t.Setenv("GODADDY_SECRET", "test-secret")
	t.Setenv("GODADDY_ENV", "prod")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"}); err != nil {
		t.Fatal(err)
	}

	helmCalls := readTestFile(t, helmLog)
	for _, want := range []string{
		"upgrade --install ingress-nginx ingress-nginx",
		"--namespace video-cloud-staging-ingress",
		"--set controller.service.type=LoadBalancer",
		"--set controller.service.ports.https=443",
		"--set controller.service.targetPorts.https=https",
		"--set controller.service.enableHttp=false",
		"--set controller.allowSnippetAnnotations=true",
		"--set controller.config.annotations-risk-level=Critical",
		"--set controller.replicaCount=3",
		"--set controller.resources.requests.cpu=500m",
		"--set controller.resources.requests.memory=512Mi",
		"--set controller.resources.limits.memory=1Gi",
		"--set-json controller.topologySpreadConstraints=",
	} {
		if !strings.Contains(helmCalls, want) {
			t.Fatalf("expected %q in helm calls, got:\n%s", want, helmCalls)
		}
	}

	kubectlCalls := readTestFile(t, kubectlLog)
	for _, want := range []string{
		"kind: Secret\nmetadata:\n  name: video-cloud-staging-public-tls\n  namespace: video-cloud-staging-ingress",
		"kind: Service\nmetadata:\n  name: public-video-cloud-api-video-cloud",
		"type: ExternalName",
		"externalName: video-cloud-api.video-cloud-staging-video-cloud.svc.cluster.local",
		"kind: Service\nmetadata:\n  name: public-certissuer-video-cloud",
		"externalName: certissuer.video-cloud-staging-video-cloud.svc.cluster.local",
		"kind: Ingress\nmetadata:\n  name: video-cloud-staging-public",
		"kind: Ingress\nmetadata:\n  name: video-cloud-staging-device-mtls",
		"kind: Ingress\nmetadata:\n  name: video-cloud-staging-certissuer",
		"nginx.ingress.kubernetes.io/auth-tls-secret: \"video-cloud-staging-ingress/video-cloud-staging-app-client-ca\"",
		"nginx.ingress.kubernetes.io/auth-tls-verify-client: \"on\"",
		"proxy_set_header X-Client-Verify $ssl_client_verify;",
		"proxy_set_header X-Client-S-DN $ssl_client_s_dn_legacy;",
		"ca.crt: \"-----BEGIN CERTIFICATE-----\\ntest-root-ca\\n-----END CERTIFICATE-----\\n-----BEGIN CERTIFICATE-----\\ntest-device-ca\\n-----END CERTIFICATE-----\\n-----BEGIN CERTIFICATE-----\\ntest-app-ca\\n-----END CERTIFICATE-----\\n\"",
		"nginx.ingress.kubernetes.io/backend-protocol: \"HTTPS\"",
		"ingressClassName: nginx",
		"host: video-cloud-staging.realtekconnect.com",
		"host: device.video-cloud-staging.realtekconnect.com",
		"host: certissuer.video-cloud-staging.realtekconnect.com",
		"host: account-manager.video-cloud-staging.realtekconnect.com",
		"host: admin.video-cloud-staging.realtekconnect.com",
		"host: frontend.video-cloud-staging.realtekconnect.com",
		"name: public-video-cloud-api-video-cloud\n                port:\n                  number: 80",
		"name: public-certissuer-video-cloud\n                port:\n                  number: 9443",
		"name: public-account-manager-account-manager\n                port:\n                  number: 80",
		"name: public-cloud-admin-admin\n                port:\n                  number: 80",
		"name: public-frontend-frontend\n                port:\n                  number: 80",
		"kind: NetworkPolicy\nmetadata:\n  name: default-deny-ingress",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-public-ingress",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-postgres-clients",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-account-manager-certissuer",
		"app.kubernetes.io/name: certissuer",
		"app.kubernetes.io/name: factoryenroll",
		"kubernetes.io/metadata.name: video-cloud-staging-account-manager",
		"port: 9443",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-video-cloud-account-manager",
		"app.kubernetes.io/name: account-manager",
		"kubernetes.io/metadata.name: video-cloud-staging-video-cloud",
		"port: 8080",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-video-cloud-mqtt-clients",
		"app.kubernetes.io/name: mqtt",
		"app.kubernetes.io/name: video-cloud-api",
		"app.kubernetes.io/name: video-cloud-logingester",
		"app.kubernetes.io/name: video-cloud-mqttusage",
		"port: 1883",
	} {
		if !strings.Contains(kubectlCalls, want) {
			t.Fatalf("expected %q in kubectl calls, got:\n%s", want, kubectlCalls)
		}
	}
	publicIdx := strings.Index(kubectlCalls, "kind: Ingress\nmetadata:\n  name: video-cloud-staging-public")
	deviceIdx := strings.Index(kubectlCalls, "kind: Ingress\nmetadata:\n  name: video-cloud-staging-device-mtls")
	if publicIdx < 0 || deviceIdx < 0 || publicIdx >= deviceIdx {
		t.Fatalf("expected public ingress before separate device mTLS ingress, got:\n%s", kubectlCalls)
	}
	publicIngress := kubectlCalls[publicIdx:deviceIdx]
	if strings.Contains(publicIngress, "host: device.video-cloud-staging.realtekconnect.com") {
		t.Fatalf("general public ingress must not include mTLS device host:\n%s", publicIngress)
	}
	for _, forbidden := range []string{
		"nodePort:",
		"controller.service.ports.http=80",
		"port: 8883",
		"port: 3478",
	} {
		if strings.Contains(kubectlCalls, forbidden) {
			t.Fatalf("public HTTPS edge must not expose %q, got:\n%s", forbidden, kubectlCalls)
		}
	}

	goCalls := readTestFile(t, goLog)
	for _, want := range []string{
		"--name video-cloud-staging --data 203.0.113.42 --ttl 600",
		"--name device.video-cloud-staging --data 203.0.113.42 --ttl 600",
		"--name certissuer.video-cloud-staging --data 203.0.113.42 --ttl 600",
		"--name account-manager.video-cloud-staging --data 203.0.113.42 --ttl 600",
		"--name admin.video-cloud-staging --data 203.0.113.42 --ttl 600",
		"--name frontend.video-cloud-staging --data 203.0.113.42 --ttl 600",
	} {
		if !strings.Contains(goCalls, want) {
			t.Fatalf("expected %q in GoDaddy calls, got:\n%s", want, goCalls)
		}
	}
	if strings.Contains(goCalls, "logger.video-cloud-staging") {
		t.Fatalf("logger DNS must be skipped when no logger LKE service exists, got:\n%s", goCalls)
	}
	if !strings.Contains(readTestFile(t, certbotLog), "-d video-cloud-staging.realtekconnect.com") {
		t.Fatalf("expected certbot DNS-01 certificate issuance, got:\n%s", readTestFile(t, certbotLog))
	}
	if strings.Contains(readTestFile(t, certbotLog), "--manual-public-ip-logging-ok") {
		t.Fatalf("certbot args must not include removed --manual-public-ip-logging-ok flag, got:\n%s", readTestFile(t, certbotLog))
	}
	if !strings.Contains(readTestFile(t, digLog), "video-cloud-staging.realtekconnect.com") {
		t.Fatalf("expected DNS convergence checks, got:\n%s", readTestFile(t, digLog))
	}
}

func TestRunProvisionLKEPublicHTTPSStartsDNSUpsertsBeforeWaiting(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	fakeHelm(t)
	eventLog := fakeDNSCommandsWithGoDelay(t, "203.0.113.42", "0.2")
	fakeCertbot(t)
	t.Setenv("LKE_PUBLIC_HTTPS_ISSUE_EMAIL", "ops@example.test")
	t.Setenv("LKE_PUBLIC_HTTPS_ACME_SERVER", "https://acme-staging-v02.api.letsencrypt.org/directory")
	t.Setenv("GODADDY_KEY", "test-key")
	t.Setenv("GODADDY_SECRET", "test-secret")
	t.Setenv("GODADDY_ENV", "prod")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"}); err != nil {
		t.Fatal(err)
	}

	events := readTestFile(t, eventLog)
	firstDig := strings.Index(events, "DIG ARGS NS realtekconnect.com")
	if firstDig < 0 {
		t.Fatalf("expected DNS wait calls, got:\n%s", events)
	}
	lastUpsert := -1
	for _, want := range []string{
		"--name video-cloud-staging",
		"--name device.video-cloud-staging",
		"--name certissuer.video-cloud-staging",
		"--name account-manager.video-cloud-staging",
		"--name admin.video-cloud-staging",
		"--name frontend.video-cloud-staging",
	} {
		idx := strings.Index(events, "GO ARGS run ./cmd/godaddy-dns")
		if idx < 0 {
			t.Fatalf("expected GoDaddy upsert, got:\n%s", events)
		}
		idx = strings.Index(events, want)
		if idx < 0 {
			t.Fatalf("expected GoDaddy upsert %q, got:\n%s", want, events)
		}
		if idx > lastUpsert {
			lastUpsert = idx
		}
	}
	if firstDig < lastUpsert {
		t.Fatalf("DNS waits should start after all public HTTPS A-record upserts have started, got:\n%s", events)
	}
}

func TestLKEPublicHTTPSNetworkPolicyAllowsBackendTargetPorts(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":              "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":            "video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":        "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":            "admin.video-cloud-staging.realtekconnect.com",
		"FRONTEND_DOMAIN":               "frontend.video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_DEVICE_DOMAIN":     "device.video-cloud-staging.realtekconnect.com",
	}
	manifests := strings.Join(lkePublicHTTPSNetworkPolicyManifests(env, lkePublicHTTPSRoutes(env)), "\n---\n")
	for _, namespace := range []string{
		"video-cloud-staging-video-cloud",
		"video-cloud-staging-account-manager",
		"video-cloud-staging-admin",
		"video-cloud-staging-frontend",
	} {
		needle := "name: allow-public-ingress\n  namespace: " + namespace
		idx := strings.Index(manifests, needle)
		if idx < 0 {
			t.Fatalf("expected allow-public-ingress policy for %s, got:\n%s", namespace, manifests)
		}
		chunk := manifests[idx:]
		if next := strings.Index(chunk, "\n---\n"); next >= 0 {
			chunk = chunk[:next]
		}
		if !strings.Contains(chunk, "port: 8080") {
			t.Fatalf("public ingress policy for %s must allow backend pod port 8080, got:\n%s", namespace, chunk)
		}
	}
}

func TestLKERedisAndExporterManifestsUsePrivatePlatformServices(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	redisDeployment := lkeRedisDeploymentManifest(env)
	for _, want := range []string{
		"kind: Deployment\nmetadata:\n  name: redis",
		"namespace: video-cloud-staging-platform",
		"app.kubernetes.io/name: redis",
		"image: valkey/valkey:8-alpine",
		"containerPort: 6379",
		"emptyDir: {}",
	} {
		if !strings.Contains(redisDeployment, want) {
			t.Fatalf("expected %q in Redis deployment manifest, got:\n%s", want, redisDeployment)
		}
	}

	redisService := lkeRedisServiceManifest(env)
	for _, want := range []string{
		"kind: Service\nmetadata:\n  name: redis",
		"type: ClusterIP",
		"port: 6379",
		"targetPort: 6379",
	} {
		if !strings.Contains(redisService, want) {
			t.Fatalf("expected %q in Redis service manifest, got:\n%s", want, redisService)
		}
	}

	exporterDeployment := lkeRedisExporterDeploymentManifest(env)
	for _, want := range []string{
		"kind: Deployment\nmetadata:\n  name: redis-exporter",
		"namespace: video-cloud-staging-platform",
		"app.kubernetes.io/name: redis-exporter",
		"image: oliver006/redis_exporter:v1.74.0",
		"REDIS_ADDR\n              value: \"redis://redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"containerPort: 9121",
	} {
		if !strings.Contains(exporterDeployment, want) {
			t.Fatalf("expected %q in Redis exporter deployment manifest, got:\n%s", want, exporterDeployment)
		}
	}

	exporterService := lkeRedisExporterServiceManifest(env)
	for _, want := range []string{
		"kind: Service\nmetadata:\n  name: redis-exporter",
		"type: ClusterIP",
		"port: 9121",
		"targetPort: 9121",
	} {
		if !strings.Contains(exporterService, want) {
			t.Fatalf("expected %q in Redis exporter service manifest, got:\n%s", want, exporterService)
		}
	}
}

func TestLKENetworkPoliciesAllowRedisAndPrometheusScrapes(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	redisPolicy := lkeAllowRedisClientsNetworkPolicyManifest(env)
	for _, want := range []string{
		"name: allow-redis-clients",
		"namespace: video-cloud-staging-platform",
		"app.kubernetes.io/name: redis",
		"kubernetes.io/metadata.name: video-cloud-staging-video-cloud",
		"kubernetes.io/metadata.name: video-cloud-staging-account-manager",
		"app.kubernetes.io/name: redis-exporter",
		"port: 6379",
	} {
		if !strings.Contains(redisPolicy, want) {
			t.Fatalf("expected %q in Redis client NetworkPolicy, got:\n%s", want, redisPolicy)
		}
	}

	scrapePolicy := lkeAllowPrometheusScrapeNetworkPolicyManifest(env)
	for _, want := range []string{
		"name: allow-prometheus-scrape",
		"kubernetes.io/metadata.name: video-cloud-staging-observability",
		"app.kubernetes.io/name: video-cloud-prometheus",
		"- redis-exporter",
		"port: 9121",
		"port: 8080",
		"port: 19200",
		"port: 19300",
	} {
		if !strings.Contains(scrapePolicy, want) {
			t.Fatalf("expected %q in Prometheus scrape NetworkPolicy, got:\n%s", want, scrapePolicy)
		}
	}
}

func TestLKEEnsureOpenBaoSkipsRestartWhenTLSSecretUnchanged(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	_ = workspace
	kubectlLog := fakeKubectl(t)
	fakeHelm(t)
	material := lkeOpenBaoTLSMaterial{
		CACert:     "test-ca",
		ServerCert: "test-cert",
		ServerKey:  "test-key",
	}
	t.Setenv("FAKE_OPENBAO_TLS_SECRET_JSON", lkeOpenBaoTLSSecretTestJSON(material))

	_, err := lkeEnsureOpenBao(provisionPaths{EnvRoot: envRoot}, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}, material)
	if err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, kubectlLog)
	if strings.Contains(log, "delete pod/openbao-0") {
		t.Fatalf("OpenBao pod should not restart when TLS secret is unchanged, got:\n%s", log)
	}
	if !strings.Contains(log, "wait --for=jsonpath={.status.phase}=Running pod/openbao-0") {
		t.Fatalf("expected OpenBao running wait when restart is skipped, got:\n%s", log)
	}
}

func TestLKEEnsureOpenBaoSkipsHelmRepoUpdateWhenTemplateWorks(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	_ = workspace
	fakeKubectl(t)
	helmLog := fakeHelm(t)
	material := lkeOpenBaoTLSMaterial{
		CACert:     "test-ca",
		ServerCert: "test-cert",
		ServerKey:  "test-key",
	}
	t.Setenv("FAKE_OPENBAO_TLS_SECRET_JSON", lkeOpenBaoTLSSecretTestJSON(material))

	_, err := lkeEnsureOpenBao(provisionPaths{EnvRoot: envRoot}, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}, material)
	if err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, helmLog)
	if strings.Contains(log, "ARGS repo update openbao") {
		t.Fatalf("OpenBao repo update should be lazy when helm template succeeds, got:\n%s", log)
	}
	if !strings.Contains(log, "ARGS template openbao openbao/openbao") {
		t.Fatalf("expected helm template validation, got:\n%s", log)
	}
}

func TestRenderCertbotDNS01EnvFileQuotesShellValues(t *testing.T) {
	body := renderCertbotDNS01EnvFile(certbotDNS01EnvValues{
		Key:                "test key",
		Secret:             "test'secret",
		Env:                "prod",
		RootDomain:         "realtekconnect.com",
		TTL:                "600",
		WaitSeconds:        "300",
		PropagationSeconds: "60",
		Resolvers:          "8.8.8.8 1.1.1.1 9.9.9.9",
	})

	for _, want := range []string{
		"GODADDY_KEY='test key'",
		"GODADDY_SECRET='test'\"'\"'secret'",
		"GODADDY_DNS_RESOLVERS='8.8.8.8 1.1.1.1 9.9.9.9'",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in certbot DNS env file, got:\n%s", want, body)
		}
	}
}

func TestCertbotDNSHooksUseBoundedNetworkCalls(t *testing.T) {
	auth := certbotDNSAuthHookScript()
	for _, want := range []string{
		"curl --connect-timeout 10 --max-time 30 -fsS -X PUT",
		"dig +time=5 +tries=1 +short TXT",
	} {
		if !strings.Contains(auth, want) {
			t.Fatalf("expected auth hook to contain %q, got:\n%s", want, auth)
		}
	}

	cleanup := certbotDNSCleanupHookScript()
	if !strings.Contains(cleanup, "curl --connect-timeout 10 --max-time 30 -fsS -X DELETE") {
		t.Fatalf("expected cleanup hook to use bounded curl, got:\n%s", cleanup)
	}
}

func TestRunProvisionLKEDNSRequiresGoDaddyCredentialsBeforeDNSMutation(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	fakeHelm(t)
	goLog := fakeGoForDNS(t)
	fakeCertbot(t)

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"})
	if err == nil || !strings.Contains(err.Error(), "GoDaddy DNS-01 credentials missing") {
		t.Fatalf("expected missing GoDaddy credentials error, got %v", err)
	}
	if _, statErr := os.Stat(goLog); !os.IsNotExist(statErr) {
		t.Fatalf("GoDaddy A record upsert must not run without DNS-01 credentials, log=%q body=%q", goLog, readTestFile(t, goLog))
	}
}

func TestRunProvisionLKEDNSStopsBeforeDNSWhenCertificateIssuanceFails(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	fakeHelm(t)
	goLog := fakeGoForDNS(t)
	fakeFailingCertbot(t)
	t.Setenv("GODADDY_KEY", "test-key")
	t.Setenv("GODADDY_SECRET", "test-secret")

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"})
	if err == nil || !strings.Contains(err.Error(), "public HTTPS ACME DNS-01 issuance failed") {
		t.Fatalf("expected certificate issuance failure, got %v", err)
	}
	if _, statErr := os.Stat(goLog); !os.IsNotExist(statErr) {
		t.Fatalf("GoDaddy A record upsert must not run after certbot failure, log=%q body=%q", goLog, readTestFile(t, goLog))
	}
}

func TestLKEOpenBaoBootstrapRolesAllowEd25519AndP256CSRs(t *testing.T) {
	script := lkeOpenBaoBootstrapScript(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	for _, want := range []string{
		"bao write pki/device/roles/factory-device",
		"bao write pki/device/roles/gateway-server",
		"bao write pki/app/roles/app-user",
		"key_type=any key_usage=DigitalSignature ext_key_usage=ClientAuth",
		"key_type=any key_usage=DigitalSignature ext_key_usage=ServerAuth",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("expected %q in OpenBao bootstrap script, got:\n%s", want, script)
		}
	}
	if strings.Contains(script, "key_type=rsa") || strings.Contains(script, "key_usage=KeyEncipherment") {
		t.Fatalf("OpenBao device/app roles must not be constrained to RSA or key encipherment, got:\n%s", script)
	}
}

func TestLKEGeneratedCertificatesPreferEd25519(t *testing.T) {
	caCert, caKey, caCertPEM, caKeyPEM, err := newLKECertificateAuthority("test-ca")
	if err != nil {
		t.Fatal(err)
	}
	assertPEMPrivateKeyIsEd25519(t, caKeyPEM)
	if _, ok := caKey.(ed25519.PrivateKey); !ok {
		t.Fatalf("expected Ed25519 CA signer, got %T", caKey)
	}
	caBlock, _ := pem.Decode([]byte(caCertPEM))
	if caBlock == nil {
		t.Fatalf("expected CA certificate PEM, got %q", caCertPEM)
	}
	parsedCA, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := parsedCA.PublicKey.(ed25519.PublicKey); !ok {
		t.Fatalf("expected Ed25519 CA certificate public key, got %T", parsedCA.PublicKey)
	}

	certPEM, keyPEM, err := newLKESignedCertificate(caCert, caKey, "svc", []string{"svc"}, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if err != nil {
		t.Fatal(err)
	}
	assertPEMPrivateKeyIsEd25519(t, keyPEM)
	certBlock, _ := pem.Decode([]byte(certPEM))
	if certBlock == nil {
		t.Fatalf("expected certificate PEM, got %q", certPEM)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		t.Fatalf("expected Ed25519 certificate public key, got %T", cert.PublicKey)
	}
	if cert.KeyUsage&x509.KeyUsageKeyEncipherment != 0 {
		t.Fatalf("expected no key encipherment usage, got %v", cert.KeyUsage)
	}
}

func TestLKEOpenBaoTLSMaterialRotatesLegacyP256State(t *testing.T) {
	envRoot := t.TempDir()
	stateDir := filepath.Join(envRoot, "state", "openbao")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(stateDir, "tls-ca.crt"), "legacy-ca")
	writeTestFile(t, filepath.Join(stateDir, "tls.crt"), "legacy-cert")
	writeP256PrivateKey(t, filepath.Join(stateDir, "tls.key"))
	writeTestFile(t, filepath.Join(stateDir, "root-token"), "keep-root-token")

	material, err := loadOrCreateLKEOpenBaoTLSMaterial(provisionPaths{EnvRoot: envRoot}, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	if err != nil {
		t.Fatal(err)
	}

	assertPEMPrivateKeyIsEd25519(t, material.ServerKey)
	assertPEMPrivateKeyIsEd25519(t, readTestFile(t, filepath.Join(stateDir, "tls.key")))
	if got := readTestFile(t, filepath.Join(stateDir, "root-token")); got != "keep-root-token" {
		t.Fatalf("expected root token state to survive TLS rotation, got %q", got)
	}
}

func TestLKECertIssuerMaterialRotatesLegacyP256State(t *testing.T) {
	envRoot := t.TempDir()
	stateDir := filepath.Join(envRoot, "state", "certissuer")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server.crt", "service-ca.crt", "client.crt", "factory.crt"} {
		writeTestFile(t, filepath.Join(stateDir, name), "legacy-cert")
	}
	for _, name := range []string{"server.key", "client.key", "factory.key"} {
		writeP256PrivateKey(t, filepath.Join(stateDir, name))
	}

	material, err := loadOrCreateLKECertIssuerMaterial(provisionPaths{EnvRoot: envRoot}, map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})
	if err != nil {
		t.Fatal(err)
	}

	assertPEMPrivateKeyIsEd25519(t, material.ServerKey)
	assertPEMPrivateKeyIsEd25519(t, material.ClientKey)
	assertPEMPrivateKeyIsEd25519(t, material.FactoryKey)
}

func assertPEMPrivateKeyIsEd25519(t *testing.T, keyPEM string) {
	t.Helper()
	block, rest := pem.Decode([]byte(keyPEM))
	if block == nil || len(rest) != 0 {
		t.Fatalf("expected single private key PEM, got block=%v rest=%d", block, len(rest))
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := key.(ed25519.PrivateKey); !ok {
		t.Fatalf("expected Ed25519 private key, got %T", key)
	}
}

func writeP256PrivateKey(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	body := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLKEPostgresStatefulSetDefaultsToPVCStorage(t *testing.T) {
	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	for _, want := range []string{
		"volumeClaimTemplates:",
		"storage: 20Gi",
		"volumeMode: Filesystem",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in default PostgreSQL PVC manifest, got:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "emptyDir: {}") {
		t.Fatalf("default PostgreSQL manifest should not use emptyDir, got:\n%s", manifest)
	}
}

func TestLKEPostgresStatefulSetSupportsExplicitEphemeralStorage(t *testing.T) {
	t.Setenv("LKE_POSTGRES_STORAGE_MODE", "emptydir")

	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	if !strings.Contains(manifest, "emptyDir: {}") {
		t.Fatalf("expected explicit ephemeral PostgreSQL manifest to use emptyDir, got:\n%s", manifest)
	}
	if strings.Contains(manifest, "volumeClaimTemplates") {
		t.Fatalf("explicit ephemeral PostgreSQL manifest should not create PVCs, got:\n%s", manifest)
	}
}

func TestLKEPostgresStatefulSetSupportsExplicitPVCStorage(t *testing.T) {
	t.Setenv("LKE_POSTGRES_STORAGE_MODE", "pvc")
	t.Setenv("LKE_POSTGRES_STORAGE", "20Gi")

	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	for _, want := range []string{
		"volumeClaimTemplates:",
		"storage: 20Gi",
		"volumeMode: Filesystem",
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

func TestLKEPostgresStatefulSetSupportsEnvRootPVCStorage(t *testing.T) {
	manifest := lkePostgresStatefulSetManifest(map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"LKE_POSTGRES_STORAGE_MODE": "pvc",
		"LKE_POSTGRES_STORAGE":      "20Gi",
	})

	for _, want := range []string{
		"volumeClaimTemplates:",
		"storage: 20Gi",
		"volumeMode: Filesystem",
		"name: data",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in env-root PVC manifest, got:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "emptyDir: {}") {
		t.Fatalf("env-root PVC manifest should not use emptyDir, got:\n%s", manifest)
	}
}

func TestLKEWaitForPostgresPVCUsesBoundGate(t *testing.T) {
	logPath := fakeKubectl(t)

	if err := lkeWaitForPostgresPVC(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	want := "ARGS -n video-cloud-staging-platform wait --for=jsonpath={.status.phase}=Bound pvc/data-postgresql-0"
	if !strings.Contains(log, want) {
		t.Fatalf("expected %q in kubectl calls, got:\n%s", want, log)
	}
}

func TestLKEPostgresStatefulSetUsesPostgresImageOverride(t *testing.T) {
	t.Setenv("LKE_POSTGRES_IMAGE", "registry.example.test/rtk/lke/postgresql:ci-1234")

	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	if !strings.Contains(manifest, "image: registry.example.test/rtk/lke/postgresql:ci-1234") {
		t.Fatalf("expected LKE_POSTGRES_IMAGE in PostgreSQL manifest, got:\n%s", manifest)
	}
}

func TestLKELoadTestCapacityManifestsSetResourcesAndPlacement(t *testing.T) {
	t.Setenv("LKE_POSTGRES_NODE_POOL_ID", "906225")
	t.Setenv("LKE_MQTT_NODE_POOL_ID", "906225")
	env := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	}

	postgres := lkePostgresStatefulSetManifest(env)
	for _, want := range []string{
		`lke.linode.com/pool-id: "906225"`,
		`value: "postgres"`,
		`cpu: "4"`,
		`memory: "6Gi"`,
	} {
		if !strings.Contains(postgres, want) {
			t.Fatalf("expected %q in postgres manifest, got:\n%s", want, postgres)
		}
	}

	account := lkeDeploymentManifest(env, lkeWorkload{
		Key:       "account-manager",
		Name:      "account-manager",
		Namespace: lkeNamespaceName(env, "account-manager"),
		Image:     "account-manager:test",
		Port:      8080,
		Host:      "account-manager.video-cloud-staging.realtekconnect.com",
	}, nil)
	for _, want := range []string{
		"replicas: 1",
		"topologySpreadConstraints:",
		`cpu: "250m"`,
		`memory: "1Gi"`,
	} {
		if !strings.Contains(account, want) {
			t.Fatalf("expected %q in account-manager manifest, got:\n%s", want, account)
		}
	}

	video := lkeDeploymentManifest(env, lkeWorkload{
		Key:       "video-cloud",
		Name:      "video-cloud-api",
		Namespace: lkeNamespaceName(env, "video-cloud"),
		Image:     "video-cloud:test",
		Port:      8080,
		Host:      "video-cloud-staging.realtekconnect.com",
	}, nil)
	for _, want := range []string{
		"replicas: 1",
		"topologySpreadConstraints:",
		`cpu: "250m"`,
		`memory: "1Gi"`,
		"name: VIDEO_CLOUD_DB_MAX_OPEN_CONNS\n              value: \"20\"",
		"name: VIDEO_CLOUD_DB_MAX_IDLE_CONNS\n              value: \"10\"",
		"name: VIDEO_CLOUD_DB_CONN_MAX_LIFETIME\n              value: \"5m\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
	} {
		if !strings.Contains(video, want) {
			t.Fatalf("expected %q in video-cloud-api manifest, got:\n%s", want, video)
		}
	}

	worker := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-logingester", Binary: "logingester"})
	for _, want := range []string{
		"name: VIDEO_CLOUD_DB_MAX_OPEN_CONNS\n              value: \"4\"",
		"name: VIDEO_CLOUD_DB_MAX_IDLE_CONNS\n              value: \"2\"",
		"name: VIDEO_CLOUD_DB_CONN_MAX_LIFETIME\n              value: \"5m\"",
		"name: VIDEO_CLOUD_MQTT_CLEAN_SESSION\n              value: \"false\"",
	} {
		if !strings.Contains(worker, want) {
			t.Fatalf("expected %q in video-cloud worker manifest, got:\n%s", want, worker)
		}
	}
	mqttUsageWorker := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-mqttusage", Binary: "mqttusage"})
	if !strings.Contains(mqttUsageWorker, "name: VIDEO_CLOUD_MQTT_CLEAN_SESSION\n              value: \"true\"") {
		t.Fatalf("expected mqttusage worker to keep clean MQTT session, got:\n%s", mqttUsageWorker)
	}

	mqtt := lkeMQTTDeploymentManifest(env)
	for _, want := range []string{
		"replicas: 1",
		"maxSurge: 0",
		"maxUnavailable: 1",
		`lke.linode.com/pool-id: "906225"`,
		"EMQX_NODE__NAME",
		`value: "emqx@$(POD_IP)"`,
		"EMQX_CLUSTER__DISCOVERY_STRATEGY",
		`value: "manual"`,
		"EMQX_CLUSTER__DNS__NAME",
		"mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local",
		"topologySpreadConstraints:",
		"whenUnsatisfiable: DoNotSchedule",
		"podAntiAffinity:",
		"requiredDuringSchedulingIgnoredDuringExecution:",
		"EMQX_LISTENERS__SSL__DEFAULT__ACCEPTORS",
		`value: "128"`,
		"EMQX_LISTENERS__SSL__DEFAULT__TCP_OPTIONS__BACKLOG",
		`value: "8192"`,
		"EMQX_FORCE_SHUTDOWN__MAX_MAILBOX_SIZE",
		`value: "16384"`,
		"EMQX_FORCE_SHUTDOWN__MAX_HEAP_SIZE",
		`value: "256MB"`,
		`cpu: "1"`,
		`memory: "6Gi"`,
	} {
		if !strings.Contains(mqtt, want) {
			t.Fatalf("expected %q in mqtt manifest, got:\n%s", want, mqtt)
		}
	}
}

func TestLKEMQTTReplicasCanBeOverridden(t *testing.T) {
	t.Setenv("LKE_MQTT_REPLICAS", "5")
	env := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	}
	manifest := lkeMQTTDeploymentManifest(env)
	if !strings.Contains(manifest, "replicas: 5") {
		t.Fatalf("expected MQTT replica override in manifest, got:\n%s", manifest)
	}
}

func TestLKEVideoCloudReplicasCanBeOverridden(t *testing.T) {
	t.Setenv("LKE_VIDEO_CLOUD_REPLICAS", "5")
	env := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	}

	manifest := lkeDeploymentManifest(env, lkeWorkload{
		Key:       "video-cloud",
		Name:      "video-cloud-api",
		Namespace: lkeNamespaceName(env, "video-cloud"),
		Image:     "video-cloud:test",
		Port:      8080,
		Host:      "video-cloud-staging.realtekconnect.com",
	}, nil)
	if !strings.Contains(manifest, "replicas: 5") {
		t.Fatalf("video-cloud-api replicas override missing:\n%s", manifest)
	}
}

func TestLKEMQTTResourcesCanBeOverridden(t *testing.T) {
	t.Setenv("LKE_MQTT_REQUEST_CPU", "3")
	t.Setenv("LKE_MQTT_REQUEST_MEMORY", "4Gi")
	t.Setenv("LKE_MQTT_LIMIT_MEMORY", "8Gi")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	manifest := lkeMQTTDeploymentManifest(env)

	for _, want := range []string{
		`cpu: "3"`,
		`memory: "4Gi"`,
		`memory: "8Gi"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in mqtt manifest, got:\n%s", want, manifest)
		}
	}
}

func TestRunProvisionLKEDeployAppliesVideoCloudAuxiliaryServices(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
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
		"rtk.realtek.com/config-checksum",
		"targets: [\"video-cloud-api.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
		"targets: [\"account-manager.video-cloud-staging-account-manager.svc.cluster.local:80\"]",
		"targets: [\"cloud-admin.video-cloud-staging-admin.svc.cluster.local:80\"]",
		"targets: [\"frontend.video-cloud-staging-frontend.svc.cluster.local:80\"]",
		"targets: [\"video-cloud-metricsexporter.video-cloud-staging-video-cloud.svc.cluster.local:19200\"]",
		"targets: [\"factoryenroll.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
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

func TestLKEPrometheusDeploymentChecksumTracksScrapeConfig(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	allConfig := lkeVideoCloudPrometheusConfigManifest(env, provisionOptions{})
	videoOnlyConfig := lkeVideoCloudPrometheusConfigManifest(env, provisionOptions{videoOnly: true})
	allWorkloads := lkeVideoCloudPrometheusDeploymentManifest(env, provisionOptions{})
	videoOnly := lkeVideoCloudPrometheusDeploymentManifest(env, provisionOptions{videoOnly: true})

	if !strings.Contains(allWorkloads, "rtk.realtek.com/config-checksum") {
		t.Fatalf("expected Prometheus deployment to include config checksum annotation, got:\n%s", allWorkloads)
	}
	if !strings.Contains(allConfig, "job_name: account-manager") {
		t.Fatalf("expected all-workload config to include account-manager scrape config, got:\n%s", allConfig)
	}
	if strings.Contains(videoOnlyConfig, "job_name: account-manager") {
		t.Fatalf("expected video-only config to exclude account-manager scrape config, got:\n%s", videoOnlyConfig)
	}
	if allWorkloads == videoOnly {
		t.Fatalf("expected Prometheus deployment checksum to change with scrape targets")
	}
	if strings.Contains(allWorkloads, "job_name: account-manager") || strings.Contains(videoOnly, "job_name: account-manager") {
		t.Fatalf("expected Prometheus deployment to carry only checksum, not embedded scrape config")
	}
}

func TestRunProvisionLKEDeployAppliesPrivateGrafana(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "test-seed")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"kind: Secret\nmetadata:\n  name: video-cloud-grafana-admin",
		"GF_SECURITY_ALLOW_EMBEDDING\n              value: \"true\"",
		"GF_AUTH_ANONYMOUS_ENABLED\n              value: \"false\"",
		"GF_AUTH_PROXY_ENABLED\n              value: \"true\"",
		"GF_SERVER_ROOT_URL\n              value: \"%(protocol)s://%(domain)s/api/admin/grafana/\"",
		"kind: ConfigMap\nmetadata:\n  name: video-cloud-grafana-datasources",
		"url: http://video-cloud-prometheus.video-cloud-staging-observability.svc.cluster.local:9090",
		"kind: ConfigMap\nmetadata:\n  name: video-cloud-grafana-dashboards",
		"RTK LKE Staging Overview",
		"sum by(brand_cloud_id) (rate(mqtt_brand_publish_total[$__rate_interval]))",
		"kind: Deployment\nmetadata:\n  name: video-cloud-grafana",
		"image: grafana/grafana:13.0.2",
		"emptyDir: {}",
		"kind: Service\nmetadata:\n  name: video-cloud-grafana",
		"type: ClusterIP",
		"CLOUD_ADMIN_GRAFANA_BASE_URL\n              value: \"http://video-cloud-grafana.video-cloud-staging-observability.svc.cluster.local:3000\"",
		"CLOUD_ADMIN_GRAFANA_DASHBOARD_PATH\n              value: \"/d/rtk-lke-staging/rtk-lke-staging-overview\"",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-cloud-admin-grafana",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-grafana-prometheus",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-prometheus-grafana",
		"ARGS -n video-cloud-staging-observability rollout status deployment/video-cloud-grafana",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in Grafana manifests, got:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{
		"kind: PersistentVolumeClaim\nmetadata:\n  name: video-cloud-grafana-data",
		"host: grafana.video-cloud-staging.realtekconnect.com",
		"name: public-video-cloud-grafana",
		"video-cloud-staging-public-tls",
	} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("private Grafana must not create public resource %q, got:\n%s", forbidden, log)
		}
	}
}

func TestLKEGrafanaPersistenceCanBeEnabled(t *testing.T) {
	t.Setenv("LKE_GRAFANA_PERSISTENCE", "true")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	pvc := lkeGrafanaPVCManifest(env)
	if !strings.Contains(pvc, "kind: PersistentVolumeClaim\nmetadata:\n  name: video-cloud-grafana-data") {
		t.Fatalf("expected Grafana PVC manifest, got:\n%s", pvc)
	}
	deployment := lkeGrafanaDeploymentManifest(env)
	for _, want := range []string{
		"persistentVolumeClaim:",
		"claimName: video-cloud-grafana-data",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("expected %q in persistent Grafana deployment, got:\n%s", want, deployment)
		}
	}
}

func TestLKEPrometheusConfigIsGeneratedFromMetricsRegistry(t *testing.T) {
	manifest := lkeVideoCloudPrometheusConfigManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}, provisionOptions{})

	for _, want := range []string{
		"job_name: video-cloud-api",
		"targets: [\"video-cloud-api.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
		"job_name: account-manager",
		"targets: [\"account-manager.video-cloud-staging-account-manager.svc.cluster.local:80\"]",
		"job_name: cloud-admin",
		"targets: [\"cloud-admin.video-cloud-staging-admin.svc.cluster.local:80\"]",
		"job_name: frontend",
		"targets: [\"frontend.video-cloud-staging-frontend.svc.cluster.local:80\"]",
		"job_name: video-cloud-turnregistry",
		"targets: [\"video-cloud-turnregistry.video-cloud-staging-video-cloud.svc.cluster.local:18190\"]",
		"job_name: video-cloud-metrics-exporter",
		"targets: [\"video-cloud-metricsexporter.video-cloud-staging-video-cloud.svc.cluster.local:19200\"]",
		"job_name: video-cloud-logingester",
		"targets: [\"video-cloud-logingester.video-cloud-staging-video-cloud.svc.cluster.local:19300\"]",
		"job_name: video-cloud-mqttusage",
		"targets: [\"video-cloud-mqttusage.video-cloud-staging-video-cloud.svc.cluster.local:19400\"]",
		"job_name: video-cloud-factoryenroll",
		"targets: [\"factoryenroll.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
		"job_name: redis-exporter",
		"targets: [\"redis-exporter.video-cloud-staging-platform.svc.cluster.local:9121\"]",
		"job_name: video-cloud-prometheus",
		"targets: [\"video-cloud-prometheus.video-cloud-staging-observability.svc.cluster.local:9090\"]",
		"job_name: video-cloud-grafana",
		"targets: [\"video-cloud-grafana.video-cloud-staging-observability.svc.cluster.local:3000\"]",
		"metrics_path: /metrics",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in Prometheus config manifest, got:\n%s", want, manifest)
		}
	}
	if got, want := strings.Count(manifest, "metrics_path: /metrics/prometheus"), 9; got != want {
		t.Fatalf("metrics_path count = %d, want %d in manifest:\n%s", got, want, manifest)
	}
	if got, want := strings.Count(manifest, "metrics_path: /metrics"), 12; got != want {
		t.Fatalf("all metrics_path count = %d, want %d in manifest:\n%s", got, want, manifest)
	}
}

func TestLKEPrometheusConfigHonorsSelectedWorkloads(t *testing.T) {
	manifest := lkeVideoCloudPrometheusConfigManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}, provisionOptions{videoOnly: true})

	for _, want := range []string{
		"targets: [\"video-cloud-api.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
		"targets: [\"video-cloud-metricsexporter.video-cloud-staging-video-cloud.svc.cluster.local:19200\"]",
		"targets: [\"factoryenroll.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in video-only Prometheus config manifest, got:\n%s", want, manifest)
		}
	}
	for _, notWant := range []string{
		"account-manager.video-cloud-staging-account-manager.svc.cluster.local:80",
		"cloud-admin.video-cloud-staging-admin.svc.cluster.local:80",
		"frontend.video-cloud-staging-frontend.svc.cluster.local:80",
	} {
		if strings.Contains(manifest, notWant) {
			t.Fatalf("video-only Prometheus config should not include %q:\n%s", notWant, manifest)
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
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
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

func TestLKEVideoCloudAuxiliaryRolloutsWaitAfterAllApplies(t *testing.T) {
	logPath := fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	if err := lkeApplyVideoCloudAuxiliaryServices(env, provisionOptions{}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	firstRollout := strings.Index(log, "rollout status deployment/video-cloud-cleaner")
	lastWorkerApply := strings.LastIndex(log, "kind: Deployment\nmetadata:\n  name: video-cloud-mqttusage")
	if firstRollout < 0 {
		t.Fatalf("expected worker rollout wait, got:\n%s", log)
	}
	if lastWorkerApply < 0 {
		t.Fatalf("expected mqttusage deployment apply, got:\n%s", log)
	}
	if firstRollout < lastWorkerApply {
		t.Fatalf("worker rollout waits should start after all worker deployments are applied, got:\n%s", log)
	}
}

func TestRunProvisionLKEDeployWritesLegacyStackAndVideoState(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")

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
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
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
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
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
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
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
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")

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
		"VIDEO_CLOUD_BASE_URL=http://127.0.0.1:",
		"ACCOUNT_MANAGER_BASE_URL=http://127.0.0.1:",
	} {
		if !strings.Contains(goCalls, want) {
			t.Fatalf("expected %q in fake go env, got:\n%s", want, goCalls)
		}
	}
}

func TestRunMQTTTestPassesLoadModelToChildScript(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	childScript := filepath.Join(t.TempDir(), "cloud-mqtt-test")
	writeTestFile(t, childScript, `#!/usr/bin/env bash
set -euo pipefail
printf 'ARGS' >> "`+childLog+`"
for arg in "$@"; do
  printf ' %s' "$arg" >> "`+childLog+`"
done
printf '\n' >> "`+childLog+`"
`)
	if err := os.Chmod(childScript, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT", childScript)

	if err := runMQTTTest([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--duration-seconds", "1",
		"--load-model", "home-100k-sustained",
	}); err != nil {
		t.Fatal(err)
	}

	got := readTestFile(t, childLog)
	if !strings.Contains(got, "--load-model home-100k-sustained") {
		t.Fatalf("child args missing load model:\n%s", got)
	}
}

func TestRunMQTTTestPassesStagedSustainedFlagsToChildScript(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	childLog := filepath.Join(t.TempDir(), "child.log")
	childScript := filepath.Join(t.TempDir(), "cloud-mqtt-test")
	writeTestFile(t, childScript, `#!/usr/bin/env bash
set -euo pipefail
printf 'ARGS' >> "`+childLog+`"
for arg in "$@"; do
  printf ' %s' "$arg" >> "`+childLog+`"
done
printf '\n' >> "`+childLog+`"
`)
	if err := os.Chmod(childScript, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT", childScript)

	err := runMQTTTest([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--duration-seconds", "300",
		"--load-model", "home-100k-sustained",
		"--stage-names", "25k,50k,75k,100k",
		"--stage-connected-devices", "2500,5000,7500,10000",
		"--stage-durations-seconds", "75,75,75,75",
		"--stage-min-commands", "125,250,375,500",
		"--device-traffic-profile", "home-diverse-v1",
		"--stage-usage-windows", "morning,away,return_home,evening_peak",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := readTestFile(t, childLog)
	for _, want := range []string{
		"--stage-names 25k,50k,75k,100k",
		"--stage-connected-devices 2500,5000,7500,10000",
		"--stage-durations-seconds 75,75,75,75",
		"--stage-min-commands 125,250,375,500",
		"--device-traffic-profile home-diverse-v1",
		"--stage-usage-windows morning,away,return_home,evening_peak",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("child args missing %q:\n%s", want, got)
		}
	}
}

func TestStartK8SE2EPortForwardsStartsAllBeforeWaiting(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectlForK8SE2EPortForwards(t)
	writeTestFile(t, filepath.Join(envRoot, "state", "lke-kubeconfig.yaml"), "apiVersion: v1\n")
	accountPort := freeTCPPort(t)
	videoPort := freeTCPPort(t)
	factoryPort := freeTCPPort(t)
	mqttPort := freeTCPPort(t)
	t.Setenv("CLOUD_STAGING_E2E_ACCOUNT_MANAGER_PORT", accountPort)
	t.Setenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_PORT", videoPort)
	t.Setenv("CLOUD_STAGING_E2E_FACTORY_ENROLL_PORT", factoryPort)
	t.Setenv("CLOUD_STAGING_E2E_MQTT_PORT", mqttPort)

	_, cleanup, err := startK8SE2EPortForwards(workspace, envRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	log := readTestFile(t, kubectlLog)
	lastStart := -1
	for _, service := range []string{"svc/account-manager", "svc/video-cloud-api", "svc/factoryenroll", "svc/mqtt"} {
		idx := strings.Index(log, "PF_START "+service)
		if idx < 0 {
			t.Fatalf("expected port-forward start for %s, got:\n%s", service, log)
		}
		if idx > lastStart {
			lastStart = idx
		}
	}
	firstReady := strings.Index(log, "PF_READY ")
	if firstReady < 0 {
		t.Fatalf("expected port-forward readiness log, got:\n%s", log)
	}
	if firstReady < lastStart {
		t.Fatalf("port-forward readiness waits should happen after all forwards start, got:\n%s", log)
	}
}

func TestRunStagingE2EDataSetupForLKEStartsPortForwards(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectlForK8SE2EPortForwards(t)
	commandLog := filepath.Join(t.TempDir(), "commands.log")
	writeTestFile(t, filepath.Join(envRoot, "state", "lke-kubeconfig.yaml"), "apiVersion: v1\n")
	t.Setenv("CLOUD_PROVIDER", "lke")
	t.Setenv("CLOUD_STAGING_E2E_ACCOUNT_MANAGER_PORT", freeTCPPort(t))
	t.Setenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_PORT", freeTCPPort(t))
	t.Setenv("CLOUD_STAGING_E2E_FACTORY_ENROLL_PORT", freeTCPPort(t))
	t.Setenv("CLOUD_STAGING_E2E_MQTT_PORT", freeTCPPort(t))
	t.Setenv("CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-brand", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-users", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "generate-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "bind-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT", fakeE2EDataCommand(t, commandLog, "validate-bind", envRoot))

	if err := runStagingE2EDataSetup([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--user-count", "2",
		"--device-count", "4",
		"--out-dir", filepath.Join(t.TempDir(), "out"),
		"--quiet",
	}); err != nil {
		t.Fatal(err)
	}

	kubectlCalls := readTestFile(t, kubectlLog)
	if !strings.Contains(kubectlCalls, "PF_START svc/factoryenroll") {
		t.Fatalf("expected standalone data setup to start factoryenroll port-forward, got:\n%s", kubectlCalls)
	}
	for _, service := range []string{"svc/account-manager", "svc/video-cloud-api", "svc/factoryenroll"} {
		if got := strings.Count(kubectlCalls, "PF_START "+service); got != 1 {
			t.Fatalf("expected one port-forward for %s, got %d:\n%s", service, got, kubectlCalls)
		}
	}
	commands := readTestFile(t, commandLog)
	if !strings.Contains(commands, "generate-devices FACTORY_ENROLL_URL=http://127.0.0.1:") {
		t.Fatalf("expected generate-devices to receive FACTORY_ENROLL_URL, got:\n%s", commands)
	}
	if !strings.Contains(commands, "generate-devices FACTORY_ENROLL_AUTH_KEY=set") {
		t.Fatalf("expected generate-devices to receive FACTORY_ENROLL_AUTH_KEY, got:\n%s", commands)
	}
}

func TestRunStagingE2EDataSetupDefaultsToResumeCompleteArtifacts(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "linode")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=linode\nCLOUD_STACK_NAME=video-cloud-staging\n")
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "users", "rtk-users-complete.json"), `{"brandname":"RTK","users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}`)
	writeTestFile(t, filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json"), `[
{"device_id":"load-device-0001","device_type":"camera"},
{"device_id":"load-device-0002","device_type":"camera"},
{"device_id":"load-device-0003","device_type":"light"},
{"device_id":"load-device-0004","device_type":"air_conditioner"}
]`)
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-complete.json"), `{"brandname":"RTK","assignments":[
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0002","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"light","service_options":["mqtt"]},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0004","device_type":"air_conditioner","service_options":["mqtt"]}
]}`)
	commandLog := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-brand", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-users", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "generate-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "bind-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT", fakeE2EDataCommand(t, commandLog, "validate-bind", envRoot))

	if err := runStagingE2EDataSetup([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--user-count", "2",
		"--device-count", "4",
		"--out-dir", filepath.Join(t.TempDir(), "out"),
		"--quiet",
	}); err != nil {
		t.Fatal(err)
	}

	commands := readTestFile(t, commandLog)
	for _, unexpected := range []string{"create-users ", "generate-devices ", "bind-devices "} {
		if strings.Contains(commands, unexpected) {
			t.Fatalf("default data setup should reuse complete artifacts, got:\n%s", commands)
		}
	}
	for _, expected := range []string{"create-brand ", "validate-bind "} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("expected %s command, got:\n%s", expected, commands)
		}
	}
}

func TestRunStagingE2EDataSetupNoResumeDisablesLocalUserReuse(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "linode")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=linode\nCLOUD_STACK_NAME=video-cloud-staging\n")
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "users", "rtk-users-complete.json"), `{"brandname":"RTK","users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}`)
	writeTestFile(t, filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json"), `[
{"device_id":"load-device-0001","device_type":"camera"},
{"device_id":"load-device-0002","device_type":"camera"},
{"device_id":"load-device-0003","device_type":"light"},
{"device_id":"load-device-0004","device_type":"air_conditioner"}
]`)
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-complete.json"), `{"brandname":"RTK","assignments":[
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0002","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"light","service_options":["mqtt"]},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0004","device_type":"air_conditioner","service_options":["mqtt"]}
]}`)
	commandLog := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("FACTORY_ENROLL_URL", "http://127.0.0.1:1")
	t.Setenv("FACTORY_ENROLL_AUTH_KEY", "test-key")
	t.Setenv("CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-brand", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-users", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "generate-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "bind-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT", fakeE2EDataCommand(t, commandLog, "validate-bind", envRoot))

	if err := runStagingE2EDataSetup([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--user-count", "2",
		"--device-count", "4",
		"--out-dir", filepath.Join(t.TempDir(), "out"),
		"--no-resume",
		"--quiet",
	}); err != nil {
		t.Fatal(err)
	}

	commands := readTestFile(t, commandLog)
	if !strings.Contains(commands, "create-users ARGS=") || !strings.Contains(commands, "--no-reuse-local-users") {
		t.Fatalf("expected create-users to disable local user reuse on --no-resume, got:\n%s", commands)
	}
}

func TestRunStagingE2EDataSetupDoesNotResumeDeviceManifestWithWrongMix(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "linode")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=linode\nCLOUD_STACK_NAME=video-cloud-staging\n")
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "users", "rtk-users-complete.json"), `{"brandname":"RTK","users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}`)
	writeTestFile(t, filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json"), `[
{"device_id":"load-device-0001","device_type":"camera"},
{"device_id":"load-device-0002","device_type":"camera"},
{"device_id":"load-device-0003","device_type":"light"},
{"device_id":"load-device-0004","device_type":"smart_meter"}
]`)
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-complete.json"), `{"brandname":"RTK","assignments":[
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"camera","service_options":["mqtt"]},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0002","device_type":"camera","service_options":["mqtt"]},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"light","service_options":["mqtt"]},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0004","device_type":"smart_meter","service_options":["mqtt"]}
]}`)
	commandLog := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("FACTORY_ENROLL_URL", "http://127.0.0.1:1")
	t.Setenv("FACTORY_ENROLL_AUTH_KEY", "test-key")
	t.Setenv("CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-brand", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-users", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "generate-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "bind-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT", fakeE2EDataCommand(t, commandLog, "validate-bind", envRoot))

	if err := runStagingE2EDataSetup([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--user-count", "2",
		"--device-count", "4",
		"--device-mix", "light=2,smart_meter=2",
		"--out-dir", filepath.Join(t.TempDir(), "out"),
		"--quiet",
	}); err != nil {
		t.Fatal(err)
	}

	commands := readTestFile(t, commandLog)
	for _, expected := range []string{"generate-devices ", "bind-devices "} {
		if !strings.Contains(commands, expected) {
			t.Fatalf("expected %s command when device mix mismatches, got:\n%s", expected, commands)
		}
	}
}

func TestRunStagingE2EDataSetupDoesNotResumeBindArtifactWithWrongUsers(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "linode")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=linode\nCLOUD_STACK_NAME=video-cloud-staging\n")
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "users", "rtk-users-complete.json"), `{"brandname":"RTK","users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}`)
	writeTestFile(t, filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json"), `[
{"device_id":"load-device-0001","device_type":"light"},
{"device_id":"load-device-0002","device_type":"light"},
{"device_id":"load-device-0003","device_type":"smart_meter"},
{"device_id":"load-device-0004","device_type":"smart_meter"}
]`)
	writeTestFile(t, filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-complete.json"), `{"brandname":"RTK","assignments":[
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"light","service_options":["mqtt"]},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0002","device_type":"light","service_options":["mqtt"]},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"smart_meter","service_options":["mqtt"]},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0004","device_type":"smart_meter","service_options":["mqtt"]}
]}`)
	commandLog := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-brand", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT", fakeE2EDataCommand(t, commandLog, "create-users", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "generate-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT", fakeE2EDataCommand(t, commandLog, "bind-devices", envRoot))
	t.Setenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT", fakeE2EDataCommand(t, commandLog, "validate-bind", envRoot))

	if err := runStagingE2EDataSetup([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--user-count", "2",
		"--device-count", "4",
		"--device-mix", "light=2,smart_meter=2",
		"--out-dir", filepath.Join(t.TempDir(), "out"),
		"--quiet",
	}); err != nil {
		t.Fatal(err)
	}

	commands := readTestFile(t, commandLog)
	if strings.Contains(commands, "generate-devices ") {
		t.Fatalf("device manifest mix matches; generate-devices should be skipped, got:\n%s", commands)
	}
	if !strings.Contains(commands, "bind-devices ") {
		t.Fatalf("expected bind-devices when bind artifact does not cover all users, got:\n%s", commands)
	}
}

func TestBuildBindAssignmentsIncludesHomeDiverseDeviceTypes(t *testing.T) {
	devices := []bindDeviceManifest{
		{DeviceID: "dev-light", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-switch", DeviceType: "switch", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-plug", DeviceType: "smart_plug", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-env", DeviceType: "environment_sensor", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-security", DeviceType: "security_sensor", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-camera-status", DeviceType: "camera_status", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-lock", DeviceType: "door_lock", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-appliance", DeviceType: "appliance", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "dev-gateway", DeviceType: "gateway", ServiceOptions: []string{"mqtt"}},
	}
	users := []userCredential{{Email: "u1@example.test"}, {Email: "u2@example.test"}}

	assignments := buildBindAssignments(devices, users)
	byID := map[string]bindAssignment{}
	for _, assignment := range assignments {
		byID[assignment.DeviceID] = assignment
	}
	for _, device := range devices {
		assignment, ok := byID[device.DeviceID]
		if !ok {
			t.Fatalf("missing assignment for %s in %#v", device.DeviceID, assignments)
		}
		if assignment.Category != "mqtt_device" {
			t.Fatalf("%s category = %q, want mqtt_device", device.DeviceType, assignment.Category)
		}
		if assignment.AssignedEmail == "" {
			t.Fatalf("%s missing assigned email", device.DeviceType)
		}
	}
}

func TestBindArtifactDoesNotResumeAllAlreadyBoundWithoutProvisionEvidence(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	bindPath := filepath.Join(dir, "bind.json")
	writeTestFile(t, usersPath, `{"users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}`)
	writeTestFile(t, bindPath, `{"assignments":[
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"light","service_options":["mqtt"],"status":"already_bound"},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0002","device_type":"light","service_options":["mqtt"],"status":"already_bound"},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"smart_meter","service_options":["mqtt"],"status":"already_bound"},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0004","device_type":"smart_meter","service_options":["mqtt"],"status":"already_bound"}
]}`)

	if bindArtifactMatchesSetup(bindPath, usersPath, 2, 4, "light=2,smart_meter=2") {
		t.Fatal("all already_bound artifact without operation evidence must not be resumed")
	}
}

func TestExistingBoundDeviceMustMatchCurrentAssignment(t *testing.T) {
	assignment := bindAssignment{
		DeviceID:       "load-device-0001",
		DeviceType:     "light",
		Category:       "mqtt_device",
		ServiceOptions: []string{"mqtt"},
	}
	existingDevice := map[string]any{
		"id":       "account-device-1",
		"category": "ip_camera",
		"metadata": map[string]any{
			"video_cloud_devid": "load-device-0001",
			"device_type":       "camera",
			"service_options":   []any{"mqtt", "video_streaming", "video_storage"},
		},
	}

	if err := validateExistingBoundDeviceCompatible(existingDevice, assignment); err == nil {
		t.Fatal("expected existing camera binding to be incompatible with current light assignment")
	}
}

func TestExistingBoundDeviceCanBeReusedWhenItMatchesCurrentAssignment(t *testing.T) {
	assignment := bindAssignment{
		DeviceID:       "load-device-0001",
		DeviceType:     "light",
		Category:       "mqtt_device",
		ServiceOptions: []string{"mqtt"},
	}
	existingDevice := map[string]any{
		"id":       "account-device-1",
		"category": "mqtt_device",
		"metadata": map[string]any{
			"video_cloud_devid": "load-device-0001",
			"device_type":       "light",
			"service_options":   []any{"mqtt"},
		},
	}

	if err := validateExistingBoundDeviceCompatible(existingDevice, assignment); err != nil {
		t.Fatalf("expected matching existing binding to be reusable: %v", err)
	}
}

func TestK8SE2EPortForwardOutputFilterSuppressesKubectlNoise(t *testing.T) {
	for _, line := range []string{
		"Forwarding from 127.0.0.1:18080 -> 8080",
		"Forwarding from [::1]:18080 -> 8080",
		"Handling connection for 18080",
		"",
		"   ",
	} {
		if shouldLogK8SPortForwardLine(line) {
			t.Fatalf("expected port-forward noise to be suppressed: %q", line)
		}
	}
	for _, line := range []string{
		"error: unable to listen on any of the requested ports",
		"E0614 portforward.go: lost connection to pod",
	} {
		if !shouldLogK8SPortForwardLine(line) {
			t.Fatalf("expected actionable port-forward output to be logged: %q", line)
		}
	}
}

func writeDataSetupStub(t *testing.T, name, commandLog, envRoot string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name+".sh")
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf '%s\t%s\n' "` + name + `" "$*"
  env | grep -E '^(ACCOUNT_MANAGER_BASE_URL|VIDEO_CLOUD_BASE_URL|FACTORY_ENROLL_URL|FACTORY_ENROLL_AUTH_KEY)=' | sort || true
} >> "` + commandLog + `"
case "` + name + `" in
  create-users)
    mkdir -p "` + envRoot + `/artifacts/users"
    printf '{"brandname":"RTK","users":[{"email":"rtk+001@users.local"}]}\n' > "` + envRoot + `/artifacts/users/rtk-users-test.json"
    ;;
  generate-devices)
    test -n "${FACTORY_ENROLL_URL:-}"
    mkdir -p "` + envRoot + `/devices/test_device/manifests"
    printf '[{"device_id":"dev-1","device_type":"camera","display_name":"Device 1","service_options":["mqtt"]}]\n' > "` + envRoot + `/devices/test_device/manifests/devices.json"
    ;;
  bind-devices)
    mkdir -p "` + envRoot + `/artifacts/device-bind"
    printf '{"brandname":"RTK","count":1,"assignments":[{"device_id":"dev-1"}]}\n' > "` + envRoot + `/artifacts/device-bind/rtk-device-bind-test.json"
    ;;
  validate-bind)
    printf '{"overall":"pass","report_file":"validate-report.md"}\n'
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
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
	fakeHelm(t)
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

func makeLKEServiceRepos(t *testing.T, workspace string) map[string]string {
	t.Helper()
	repos := []string{
		"rtk_video_cloud",
		"rtk_account_manager",
		"rtk_cloud_admin",
		"rtk_cloud_frontend",
		"rtk_cloud_logger",
	}
	commits := map[string]string{}
	for _, repo := range repos {
		repoDir := filepath.Join(workspace, "repos", repo)
		if err := os.MkdirAll(repoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		runTestCommand(t, repoDir, "git", "init", "-q")
		runTestCommand(t, repoDir, "git", "config", "user.email", "test@example.invalid")
		runTestCommand(t, repoDir, "git", "config", "user.name", "Test User")
		writeTestFile(t, filepath.Join(repoDir, "README.md"), repo+"\n")
		runTestCommand(t, repoDir, "git", "add", "README.md")
		runTestCommand(t, repoDir, "git", "commit", "-q", "-m", "initial")
		commits[repo] = strings.TrimSpace(runTestCommand(t, repoDir, "git", "rev-parse", "--short=12", "HEAD"))
	}
	return commits
}

func runTestCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
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
if [[ "$*" == *"exec openbao-0 -- env "* && "$*" == *" bao status -format=json"* ]]; then
  printf '{"initialized":false,"sealed":true}\n'
  exit 0
fi
if [[ "$*" == *"exec openbao-0 -- env "* && "$*" == *" bao operator init "* ]]; then
  printf '{"unseal_keys_b64":["test-unseal-key"],"root_token":"test-root-token"}\n'
  exit 0
fi
if [[ "$*" == *"get pod/openbao-0 -o jsonpath={.status.phase}"* ]]; then
  {
    printf 'ARGS'
    for arg in "$@"; do
      printf ' %s' "$arg"
    done
    printf '\n'
  } >> "` + logPath + `"
  printf 'Running'
  exit 0
fi
if [[ "$*" == *"get service ingress-nginx-controller -o jsonpath={.status.loadBalancer.ingress[0].ip}"* ]]; then
  printf '203.0.113.42'
  exit 0
fi
if [[ "$*" == *"get secret openbao-tls -o json"* && -n "${FAKE_OPENBAO_TLS_SECRET_JSON:-}" ]]; then
  printf '%s\n' "$FAKE_OPENBAO_TLS_SECRET_JSON"
  exit 0
fi
if [[ "$*" == *"get secret certissuer-runtime -o json"* ]]; then
  python3 - <<'PY'
import base64, json
data = {
  "root-ca.crt": "-----BEGIN CERTIFICATE-----\ntest-root-ca\n-----END CERTIFICATE-----\n",
  "device-ca.crt": "-----BEGIN CERTIFICATE-----\ntest-device-ca\n-----END CERTIFICATE-----\n",
  "app-ca.crt": "-----BEGIN CERTIFICATE-----\ntest-app-ca\n-----END CERTIFICATE-----\n",
}
print(json.dumps({"data": {k: base64.b64encode(v.encode()).decode() for k, v in data.items()}}))
PY
  exit 0
fi
if [[ "$*" == *"get pods -l app.kubernetes.io/name=mqtt -o jsonpath="* ]]; then
  printf 'mqtt-aaa\t10.2.0.1\nmqtt-bbb\t10.2.0.2\nmqtt-ccc\t10.2.0.3\n'
  exit 0
fi
if [[ "$*" == *"exec mqtt-"* && "$*" == *" emqx ctl cluster join emqx@10.2.0.1"* ]]; then
  line='ARGS'
  for arg in "$@"; do
    line="$line $arg"
  done
  printf '%s\n' "$line" >> "` + logPath + `"
  printf 'Join the cluster successfully.\n'
  exit 0
fi
if [[ "$*" == *"exec mqtt-aaa -- emqx ctl cluster status"* ]]; then
  line='ARGS'
  for arg in "$@"; do
    line="$line $arg"
  done
  printf '%s\n' "$line" >> "` + logPath + `"
  printf 'Cluster status: #{running_nodes => [emqx@10.2.0.1,emqx@10.2.0.2,emqx@10.2.0.3]}\n'
  exit 0
fi
if [[ "$*" == *"rollout status"* ]]; then
  line='ARGS'
  for arg in "$@"; do
    line="$line $arg"
  done
  printf '%s\n' "$line" >> "` + logPath + `"
  exit 0
fi
if [[ "$*" == *"exec -i openbao-0 -- sh -s"* ]]; then
  body="$(cat)"
  if [[ "$body" == *"bao operator unseal"* ]]; then
    exit 0
  fi
  printf 'ROLE_ID=test-role-id\n'
  printf 'SECRET_ID=test-secret-id\n'
  printf 'ROOT_CA_CERT_B64=%s\n' "$(printf '%s' '-----BEGIN CERTIFICATE-----
test-root-ca
-----END CERTIFICATE-----
' | base64 | tr -d '\n')"
  printf 'DEVICE_CA_CERT_B64=%s\n' "$(printf '%s' '-----BEGIN CERTIFICATE-----
test-device-ca
-----END CERTIFICATE-----
' | base64 | tr -d '\n')"
  printf 'APP_CA_CERT_B64=%s\n' "$(printf '%s' '-----BEGIN CERTIFICATE-----
test-app-ca
-----END CERTIFICATE-----
' | base64 | tr -d '\n')"
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
if [[ "$*" == *"exec openbao-0 -- env "* && "$*" == *" bao status -format=json"* ]]; then
  printf '{"initialized":false,"sealed":true}\n'
  exit 0
fi
if [[ "$*" == *"exec openbao-0 -- env "* && "$*" == *" bao operator init "* ]]; then
  printf '{"unseal_keys_b64":["test-unseal-key"],"root_token":"test-root-token"}\n'
  exit 0
fi
if [[ "$*" == *"get pod/openbao-0 -o jsonpath={.status.phase}"* ]]; then
  {
    printf 'ARGS'
    for arg in "$@"; do
      printf ' %s' "$arg"
    done
    printf '\n'
  } >> "` + logPath + `"
  printf 'Running'
  exit 0
fi
if [[ "$*" == *"get service ingress-nginx-controller -o jsonpath={.status.loadBalancer.ingress[0].ip}"* ]]; then
  printf '203.0.113.42'
  exit 0
fi
if [[ "$*" == *"get secret openbao-tls -o json"* && -n "${FAKE_OPENBAO_TLS_SECRET_JSON:-}" ]]; then
  printf '%s\n' "$FAKE_OPENBAO_TLS_SECRET_JSON"
  exit 0
fi
if [[ "$*" == *"get secret certissuer-runtime -o json"* ]]; then
  python3 - <<'PY'
import base64, json
data = {
  "root-ca.crt": "-----BEGIN CERTIFICATE-----\ntest-root-ca\n-----END CERTIFICATE-----\n",
  "device-ca.crt": "-----BEGIN CERTIFICATE-----\ntest-device-ca\n-----END CERTIFICATE-----\n",
  "app-ca.crt": "-----BEGIN CERTIFICATE-----\ntest-app-ca\n-----END CERTIFICATE-----\n",
}
print(json.dumps({"data": {k: base64.b64encode(v.encode()).decode() for k, v in data.items()}}))
PY
  exit 0
fi
if [[ "$*" == *"rollout status"* ]]; then
  line='ARGS'
  for arg in "$@"; do
    line="$line $arg"
  done
  printf '%s\n' "$line" >> "` + logPath + `"
  exit 0
fi
if [[ "$*" == *"exec -i openbao-0 -- sh -s"* ]]; then
  body="$(cat)"
  if [[ "$body" == *"bao operator unseal"* ]]; then
    exit 0
  fi
  printf 'ROLE_ID=test-role-id\n'
  printf 'SECRET_ID=test-secret-id\n'
  printf 'ROOT_CA_CERT_B64=%s\n' "$(printf '%s' '-----BEGIN CERTIFICATE-----
test-root-ca
-----END CERTIFICATE-----
' | base64 | tr -d '\n')"
  printf 'DEVICE_CA_CERT_B64=%s\n' "$(printf '%s' '-----BEGIN CERTIFICATE-----
test-device-ca
-----END CERTIFICATE-----
' | base64 | tr -d '\n')"
  printf 'APP_CA_CERT_B64=%s\n' "$(printf '%s' '-----BEGIN CERTIFICATE-----
test-app-ca
-----END CERTIFICATE-----
' | base64 | tr -d '\n')"
  exit 0
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

func fakeKubectlForK8SE2EPortForwards(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	secretData := `"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL":"YWRtaW5AZXhhbXBsZS50ZXN0","ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD":"cGFzc3dvcmQxMjM=","ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN":"dGVzdC10b2tlbg==","VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN":"dGVzdC10b2tlbg==","VIDEO_CLOUD_LOGGER_TOKEN":"dGVzdC1sb2dnZXItdG9rZW4=","FACTORY_ENROLL_AUTH_KEY":"dGVzdC1mYWN0b3J5LWtleQ=="`
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
} >> "` + logPath + `"
if [[ "$*" == *" get svc "* ]]; then
  service="${5:-}"
  case "$service" in
    account-manager|video-cloud-api) port=8080 ;;
    factoryenroll) port=18443 ;;
    mqtt) port=8883 ;;
    *) port=80 ;;
  esac
  printf '{"spec":{"ports":[{"name":"http","port":%s},{"name":"mqtts","port":8883}]}}\n' "$port"
  exit 0
fi
if [[ "$*" == *" get secret "* ]]; then
  printf '{"data":{` + secretData + `}}\n'
  exit 0
fi
if [[ "${3:-}" == "port-forward" ]]; then
  service="${4:-}"
  mapping="${5:-}"
  local_port="${mapping%%:*}"
  printf 'PF_START %s\n' "$service" >> "` + logPath + `"
  exec python3 - "$local_port" "` + logPath + `" "$service" <<'PY'
import socket
import sys

port = int(sys.argv[1])
log_path = sys.argv[2]
service = sys.argv[3]
sock = socket.socket()
sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
sock.bind(("127.0.0.1", port))
sock.listen()
with open(log_path, "a", encoding="utf-8") as log:
    log.write(f"PF_READY {service}\n")
while True:
    conn, _ = sock.accept()
    conn.close()
PY
fi
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func fakeE2EDataCommand(t *testing.T, logPath, name, envRoot string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	script := `#!/usr/bin/env bash
set -euo pipefail
case "` + name + `" in
  create-users)
    mkdir -p "` + envRoot + `/artifacts/users"
    printf '{"brandname":"RTK","users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}\n' > "` + envRoot + `/artifacts/users/rtk-users-test.json"
    ;;
  generate-devices)
    if [[ -z "${FACTORY_ENROLL_URL:-}" ]]; then
      echo "missing FACTORY_ENROLL_URL" >&2
      exit 1
    fi
    if [[ -z "${FACTORY_ENROLL_AUTH_KEY:-}" ]]; then
      echo "missing FACTORY_ENROLL_AUTH_KEY" >&2
      exit 1
    fi
    mkdir -p "` + envRoot + `/devices/test_device/manifests"
    printf '[]\n' > "` + envRoot + `/devices/test_device/manifests/devices.json"
    ;;
  bind-devices)
    mkdir -p "` + envRoot + `/artifacts/device-bind"
    printf '{"brandname":"RTK","count":4,"assignments":[{"device_id":"dev-1"}]}\n' > "` + envRoot + `/artifacts/device-bind/rtk-device-bind-test.json"
    ;;
  validate-bind)
    printf '{"overall":"pass","report_file":"validate-report.md"}\n'
    ;;
esac
factory_key_state=unset
if [[ -n "${FACTORY_ENROLL_AUTH_KEY:-}" ]]; then
  factory_key_state=set
fi
{
  printf '%s ARGS=%s\n' "` + name + `" "$*"
  printf '%s FACTORY_ENROLL_URL=%s\n' "` + name + `" "${FACTORY_ENROLL_URL:-}"
  printf '%s FACTORY_ENROLL_AUTH_KEY=%s\n' "` + name + `" "$factory_key_state"
} >> "` + logPath + `"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func freeTCPPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func lkeOpenBaoTLSSecretTestJSON(material lkeOpenBaoTLSMaterial) string {
	return fmt.Sprintf(`{"data":{"ca.crt":%q,"tls.crt":%q,"tls.key":%q}}`,
		base64.StdEncoding.EncodeToString([]byte(material.CACert)),
		base64.StdEncoding.EncodeToString([]byte(material.ServerCert)),
		base64.StdEncoding.EncodeToString([]byte(material.ServerKey)))
}

func fakeHelm(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "helm.log")
	helmPath := filepath.Join(dir, "helm")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf 'ARGS' >> "` + logPath + `"
for arg in "$@"; do
  printf ' %s' "$arg" >> "` + logPath + `"
done
printf '\n' >> "` + logPath + `"
`
	if err := os.WriteFile(helmPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_HELM", helmPath)
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
  env | grep -E '^(RTK_CLOUD_MQTT_TEST_|VIDEO_CLOUD_BASE_URL=|ACCOUNT_MANAGER_BASE_URL=)' | sort
} >> "` + logPath + `"
`
	if err := os.WriteFile(goPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func fakeGoForDNS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "go.log")
	goPath := filepath.Join(dir, "go")
	script := `#!/usr/bin/env bash
set -euo pipefail
line='ARGS'
for arg in "$@"; do
  line="$line $arg"
done
printf '%s\n' "$line" >> "` + logPath + `"
`
	if err := os.WriteFile(goPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_GO", goPath)
	return logPath
}

func fakeDNSCommandsWithGoDelay(t *testing.T, ip, delay string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "dns-events.log")
	goPath := filepath.Join(dir, "go")
	goScript := `#!/usr/bin/env bash
set -euo pipefail
line='GO ARGS'
for arg in "$@"; do
  line="$line $arg"
done
printf '%s\n' "$line" >> "` + logPath + `"
sleep "` + delay + `"
`
	if err := os.WriteFile(goPath, []byte(goScript), 0o755); err != nil {
		t.Fatal(err)
	}
	digPath := filepath.Join(dir, "dig")
	digScript := `#!/usr/bin/env bash
set -euo pipefail
line='DIG ARGS'
for arg in "$@"; do
  line="$line $arg"
done
printf '%s\n' "$line" >> "` + logPath + `"
if [[ "$*" == *" NS "* || "${1:-}" == "NS" ]]; then
  printf 'ns23.domaincontrol.com.\n'
  exit 0
fi
printf '` + ip + `\n'
`
	if err := os.WriteFile(digPath, []byte(digScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_GO", goPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func fakeCertbot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "certbot.log")
	certbotPath := filepath.Join(dir, "certbot")
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
} >> "` + logPath + `"
config_dir=""
domain=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config-dir)
      config_dir="$2"
      shift 2
      ;;
    -d)
      if [[ -z "$domain" ]]; then
        domain="$2"
      fi
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
live_dir="$config_dir/live/$domain"
mkdir -p "$live_dir"
cat > "$live_dir/fullchain.pem" <<'EOF'
-----BEGIN CERTIFICATE-----
test-cert
-----END CERTIFICATE-----
EOF
cat > "$live_dir/privkey.pem" <<'EOF'
-----BEGIN PRIVATE KEY-----
test-key
-----END PRIVATE KEY-----
EOF
`
	if err := os.WriteFile(certbotPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CERTBOT", certbotPath)
	return logPath
}

func fakeFailingCertbot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "certbot.log")
	certbotPath := filepath.Join(dir, "certbot")
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
} >> "` + logPath + `"
exit 42
`
	if err := os.WriteFile(certbotPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CERTBOT", certbotPath)
	return logPath
}

func fakeDig(t *testing.T, ip string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "dig.log")
	digPath := filepath.Join(dir, "dig")
	script := `#!/usr/bin/env bash
set -euo pipefail
{
  printf 'ARGS'
  for arg in "$@"; do
    printf ' %s' "$arg"
  done
  printf '\n'
} >> "` + logPath + `"
if [[ "$*" == *" NS "* || "${1:-}" == "NS" ]]; then
  printf 'ns23.domaincontrol.com.\n'
  exit 0
fi
printf '` + ip + `\n'
`
	if err := os.WriteFile(digPath, []byte(script), 0o755); err != nil {
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
