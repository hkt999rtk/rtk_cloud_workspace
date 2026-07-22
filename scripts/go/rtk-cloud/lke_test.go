package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func TestLKEGHCRPullCredentialsFallsBackToHomeEnv(t *testing.T) {
	home := t.TempDir()
	writeTestFile(t, filepath.Join(home, ".env"), "GHCR_PULL_USERNAME=home-user\nGHCR_PULL_TOKEN=home-token\n")
	t.Setenv("HOME", home)
	t.Setenv("GHCR_PULL_USERNAME", "")
	t.Setenv("GHCR_PULL_TOKEN", "")

	username, token := lkeGHCRPullCredentials(nil)
	if username != "home-user" || token != "home-token" {
		t.Fatalf("lkeGHCRPullCredentials() = (%q, %q), want home .env values", username, token)
	}
}

func TestLKEGHCRPullCredentialsPrefersProcessEnv(t *testing.T) {
	home := t.TempDir()
	writeTestFile(t, filepath.Join(home, ".env"), "GHCR_PULL_USERNAME=home-user\nGHCR_PULL_TOKEN=home-token\n")
	t.Setenv("HOME", home)
	t.Setenv("GHCR_PULL_USERNAME", "process-user")
	t.Setenv("GHCR_PULL_TOKEN", "process-token")

	username, token := lkeGHCRPullCredentials(nil)
	if username != "process-user" || token != "process-token" {
		t.Fatalf("lkeGHCRPullCredentials() = (%q, %q), want process env values", username, token)
	}
}

func TestRunProvisionLKEApplyUsesKubectl(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	if count := strings.Count(log, "apply -f -"); count != len(lkeNamespaces(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}))+1 {
		t.Fatalf("unexpected kubectl apply count in log:\n%s", log)
	}
	if !strings.Contains(log, "kind: Namespace") || !strings.Contains(log, "kind: ConfigMap") {
		t.Fatalf("expected namespace and configmap manifests, got:\n%s", log)
	}
}

func TestWriteLKEDeviceClientCABundle(t *testing.T) {
	envRoot := t.TempDir()
	paths := provisionPaths{EnvRoot: envRoot}
	dir := filepath.Join(envRoot, "state", "secrets")
	path := filepath.Join(dir, "device-client-ca-bundle.pem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootCA := "-----BEGIN CERTIFICATE-----\nroot\n-----END CERTIFICATE-----"
	deviceCA := "-----BEGIN CERTIFICATE-----\ndevice\n-----END CERTIFICATE-----"
	appCA := "-----BEGIN CERTIFICATE-----\napp\n-----END CERTIFICATE-----"

	if err := writeLKEDeviceClientCABundle(paths, rootCA, deviceCA, appCA); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := lkeClientCABundle(rootCA, deviceCA, appCA)
	if string(got) != want {
		t.Fatalf("unexpected CA bundle:\n%s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("CA bundle permissions = %o, want 600", got)
	}
	parentInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := parentInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("CA bundle directory permissions = %o, want 700", got)
	}
}

func TestWriteLKEDeviceClientCABundleRejectsIncompleteMaterial(t *testing.T) {
	err := writeLKEDeviceClientCABundle(provisionPaths{EnvRoot: t.TempDir()}, "root", "", "app")
	if err == nil || !strings.Contains(err.Error(), "root, device, and app CA certificates are required") {
		t.Fatalf("expected incomplete CA material error, got %v", err)
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
		"apply -f https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.8.1/components.yaml",
		"rollout status deployment/metrics-server --timeout 5m",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected metrics-server install to include %q, got:\n%s", want, log)
		}
	}
}

func TestRunProvisionLKEApplyFetchesKubeconfigWhenNoContext(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	writeTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID=12345\n")
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters/12345/kubeconfig": `{"kubeconfig":"` + base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nclusters: []\n")) + `"}`,
		"/lke/clusters/12345/pools":      `{"data":[{"id":907616,"type":"g6-standard-2","count":5,"labels":{"rtk.io/node-class":"broker"}}]}`,
	})
	kubectlLog := fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")
	t.Setenv("LKE_NODE_COUNT", "5")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if !strings.Contains(curlCalls, "GET /lke/clusters/12345/kubeconfig") {
		t.Fatalf("expected kubeconfig fetch, got:\n%s", curlCalls)
	}
	kubectlCalls := readTestFile(t, kubectlLog)
	if !strings.Contains(kubectlCalls, "ARGS --kubeconfig "+filepath.Join(envRoot, "state", "kubeconfig.yaml")) || !strings.Contains(kubectlCalls, "apply -f -") {
		t.Fatalf("expected kubectl to use env-root kubeconfig, got:\n%s", kubectlCalls)
	}
	kubeconfigInfo, err := os.Stat(filepath.Join(envRoot, "state", "kubeconfig.yaml"))
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
	envRootKubeconfig := filepath.Join(envRoot, "state", "kubeconfig.yaml")
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
		"/lke/clusters/67890/pools":      `{"data":[{"id":907616,"type":"g6-standard-2","count":5,"labels":{"rtk.io/node-class":"broker"}}]}`,
	})
	kubectlLog := fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")
	t.Setenv("LKE_NODE_COUNT", "5")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if !strings.Contains(curlCalls, "GET /lke/clusters?page_size=500") || !strings.Contains(curlCalls, "GET /lke/clusters/67890/kubeconfig") {
		t.Fatalf("expected cluster list and kubeconfig fetch, got:\n%s", curlCalls)
	}
	state := readTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"))
	if !strings.Contains(state, "LKE_CLUSTER_ID=67890") || !strings.Contains(state, "LKE_CLUSTER_LABEL=video-cloud-staging-lke") {
		t.Fatalf("expected discovered cluster state, got:\n%s", state)
	}
	kubectlCalls := readTestFile(t, kubectlLog)
	if !strings.Contains(kubectlCalls, "ARGS --kubeconfig "+filepath.Join(envRoot, "state", "kubeconfig.yaml")) || !strings.Contains(kubectlCalls, "apply -f -") {
		t.Fatalf("expected kubectl to use fetched kubeconfig")
	}
}

func TestRunProvisionLKEApplyCreatesClusterWhenMissing(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	encodedKubeconfig := base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nclusters: []\n"))
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500":      `{"data":[]}`,
		"/lke/versions":                    `{"data":[{"id":"1.33"}]}`,
		"/lke/clusters":                    `{"id":24680,"label":"video-cloud-staging-lke","region":"us-sea","k8s_version":"1.33"}`,
		"/lke/clusters/24680/kubeconfig":   `{"kubeconfig":"` + encodedKubeconfig + `"}`,
		"/lke/clusters/24680/pools":        `{"data":[{"id":907616,"type":"g6-standard-2","count":3}]}`,
		"/lke/clusters/24680/pools/907616": `{"id":907616,"type":"g6-standard-2","count":5}`,
	})
	fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")
	t.Setenv("LKE_NODE_TYPE", "g6-standard-2")
	t.Setenv("LKE_NODE_COUNT", "5")

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
	state := readTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"))
	if !strings.Contains(state, "LKE_CLUSTER_ID=24680") || !strings.Contains(state, "LKE_CLUSTER_VERSION=1.33") {
		t.Fatalf("expected created cluster state, got:\n%s", state)
	}
}

func TestRunProvisionLKEApplyRecoversStaleClusterState(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	writeTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID=12345\nLKE_CLUSTER_LABEL=video-cloud-staging-lke\n")
	writeTestFile(t, filepath.Join(envRoot, "state", "kubeconfig.yaml"), "stale kubeconfig\n")
	encodedKubeconfig := base64.StdEncoding.EncodeToString([]byte("apiVersion: v1\nclusters: []\n"))
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters/12345/pools":        "__ERROR_404__",
		"/lke/clusters?page_size=500":      `{"data":[]}`,
		"/lke/versions":                    `{"data":[{"id":"1.33"}]}`,
		"/lke/clusters":                    `{"id":24680,"label":"video-cloud-staging-lke","region":"us-sea","k8s_version":"1.33"}`,
		"/lke/clusters/24680/kubeconfig":   `{"kubeconfig":"` + encodedKubeconfig + `"}`,
		"/lke/clusters/24680/pools":        `{"data":[{"id":907616,"type":"g6-standard-2","count":3}]}`,
		"/lke/clusters/24680/pools/907616": `{"id":907616,"type":"g6-standard-2","count":5}`,
	})
	fakeKubectl(t)
	t.Setenv("LINODE_TOKEN", "test-token")
	t.Setenv("LKE_NODE_TYPE", "g6-standard-2")
	t.Setenv("LKE_NODE_COUNT", "5")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	for _, want := range []string{
		"GET /lke/clusters/12345/pools",
		"GET /lke/clusters?page_size=500",
		"POST /lke/clusters",
		"GET /lke/clusters/24680/kubeconfig",
		"GET /lke/clusters/24680/pools",
	} {
		if !strings.Contains(curlCalls, want) {
			t.Fatalf("expected %q in curl log, got:\n%s", want, curlCalls)
		}
	}
	state := readTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"))
	if !strings.Contains(state, "LKE_CLUSTER_ID=24680") || !strings.Contains(state, "LKE_CLUSTER_VERSION=1.33") {
		t.Fatalf("expected stale cluster state to be refreshed, got:\n%s", state)
	}
	kubeconfig := readTestFile(t, filepath.Join(envRoot, "state", "kubeconfig.yaml"))
	if strings.Contains(kubeconfig, "stale kubeconfig") {
		t.Fatalf("expected stale kubeconfig to be overwritten, got:\n%s", kubeconfig)
	}
}

func TestLKENodeCountDefaultsToFiveForLoadTestHeadroom(t *testing.T) {
	got, err := lkeNodeCount(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if got != 5 {
		t.Fatalf("node count default got %d want 5", got)
	}

	t.Setenv("LKE_NODE_COUNT", "6")
	got, err = lkeNodeCount(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if got != 6 {
		t.Fatalf("node count override got %d want 6", got)
	}
}

func TestEnsureLKENodePoolResizesExistingPoolToDesiredCount(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters/12345/pools":        `{"data":[{"id":907616,"type":"g6-standard-2","count":3}]}`,
		"/lke/clusters/12345/pools/907616": `{"id":907616,"type":"g6-standard-2","count":4}`,
	})
	writeTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID=12345\n")
	t.Setenv("LINODE_TOKEN", "test-token")

	err := ensureLKENodePool(provisionPaths{Workspace: workspace, EnvRoot: envRoot}, map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging",
		"LKE_NODE_COUNT":   "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	curlCalls := readTestFile(t, curlLog)
	for _, want := range []string{
		"GET /lke/clusters/12345/pools",
		"PUT /lke/clusters/12345/pools/907616",
	} {
		if !strings.Contains(curlCalls, want) {
			t.Fatalf("expected %q in curl log, got:\n%s", want, curlCalls)
		}
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

func TestRunProvisionLKEPlanShowsCoturnVMIdentity(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)

	out := captureStdout(t, func() {
		if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--plan"}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"public TURN: external coturn VM data-plane exception, not HAProxy-backed",
		"coturn_vms: count=1 type=g6-nanode-1 image=linode/ubuntu24.04 ports=3478/udp,3478/tcp relay_udp=49152-65535",
		"coturn_vm: name=turn01 label=video-cloud-staging-turn01 domain=turn.video-cloud-staging.realtekconnect.com",
		"type=g6-nanode-1",
		"image=linode/ubuntu24.04",
		"relay_udp=49152-65535",
		"turn_registry: domain=turnregistry.video-cloud-staging.realtekconnect.com registrar_node_id=turn01",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in provision plan, got:\n%s", want, out)
		}
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

func TestRunLKEBuildImagesBuildsSelectedServiceImage(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	dockerLog := fakeDocker(t)
	out := filepath.Join(t.TempDir(), "lke-images.json")

	if err := runLKEBuildImages([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--workloads", "video-cloud",
		"--image", "ttl.sh/rtk-video-cloud-local-test:24h",
		"--out", out,
	}); err != nil {
		t.Fatal(err)
	}

	dockerCalls := readTestFile(t, dockerLog)
	if !strings.Contains(dockerCalls, " -t ttl.sh/rtk-video-cloud-local-test:24h ") {
		t.Fatalf("expected docker build for exact video-cloud image, got:\n%s", dockerCalls)
	}
	if strings.Contains(dockerCalls, "postgresql") {
		t.Fatalf("did not expect postgres build when selecting video-cloud only, got:\n%s", dockerCalls)
	}
	body := readTestFile(t, out)
	for _, want := range []string{
		`"key": "video-cloud"`,
		`"LKE_VIDEO_CLOUD_IMAGE": "ttl.sh/rtk-video-cloud-local-test:24h"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "LKE_POSTGRES_IMAGE") {
		t.Fatalf("selected service image manifest should not include postgres image:\n%s", body)
	}
}

func TestRunLKEBuildImagesBuildsSelectedCloudLoggerImage(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	dockerLog := fakeDocker(t)
	out := filepath.Join(t.TempDir(), "lke-images.json")

	if err := runLKEBuildImages([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--workloads", "cloud-logger",
		"--image", "ttl.sh/rtk-cloud-logger-local-test:24h",
		"--out", out,
	}); err != nil {
		t.Fatal(err)
	}

	dockerCalls := readTestFile(t, dockerLog)
	if !strings.Contains(dockerCalls, " -t ttl.sh/rtk-cloud-logger-local-test:24h ") {
		t.Fatalf("expected docker build for exact cloud-logger image, got:\n%s", dockerCalls)
	}
	body := readTestFile(t, out)
	for _, want := range []string{
		`"key": "cloud-logger"`,
		`"LKE_CLOUD_LOGGER_IMAGE": "ttl.sh/rtk-cloud-logger-local-test:24h"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("manifest missing %q:\n%s", want, body)
		}
	}
}

func TestRunLKEBuildImagesRejectsExactImageForMultipleWorkloads(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeDocker(t)

	err := runLKEBuildImages([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--workloads", "postgres,video-cloud",
		"--image", "ttl.sh/rtk-video-cloud-local-test:24h",
	})
	if err == nil || !strings.Contains(err.Error(), "--image requires exactly one selected workload") {
		t.Fatalf("expected exact image validation error, got %v", err)
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

func TestRunLKEResolveImagesUsesProvidedServiceImageWithoutInspect(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	makeLKEServiceRepos(t, workspace)
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "ttl.sh/rtk-cloud-logger-local-test:24h")
	out := filepath.Join(t.TempDir(), "lke-images.json")
	oldInspect := inspectLKEImage
	inspectLKEImage = func(image string) error {
		if strings.Contains(image, "rtk_cloud_logger") {
			return errLKEImageNotFound
		}
		return nil
	}
	t.Cleanup(func() { inspectLKEImage = oldInspect })

	if err := runLKEResolveImages([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--owner", "hkt999rtk",
		"--out", out,
	}); err != nil {
		t.Fatal(err)
	}

	body := readTestFile(t, out)
	if !strings.Contains(body, `"LKE_CLOUD_LOGGER_IMAGE": "ttl.sh/rtk-cloud-logger-local-test:24h"`) {
		t.Fatalf("manifest missing provided logger image:\n%s", body)
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
		"ACCOUNT_MANAGER_USER_CACHE_ENABLED: \"true\"",
		"ACCOUNT_MANAGER_USER_CACHE_ADDR: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"ACCOUNT_MANAGER_USER_CACHE_PREFIX: \"account_manager:user\"",
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
		"VIDEO_CLOUD_AUTH_SECRET: \"test-seed-video-auth\"",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN",
		"VIDEO_CLOUD_DB_DSN",
		"VIDEO_CLOUD_AUTH_SECRET\n              valueFrom:\n                secretKeyRef:\n                  name: video-cloud-runtime\n                  key: VIDEO_CLOUD_AUTH_SECRET",
		"VIDEO_CLOUD_API_ADDR\n              value: \":8080\"",
		"VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS\n              value: \"true\"",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL\n              value: \"http://account-manager.video-cloud-staging-account-manager.svc.cluster.local:80\"",
		"VIDEO_CLOUD_MQTT_ENABLED\n              value: \"true\"",
		"VIDEO_CLOUD_MQTT_ADDR\n              value: \"mqtt.video-cloud-staging-video-cloud.svc.cluster.local:1883\"",
		"POD_NAME\n              valueFrom:",
		"fieldPath: metadata.name",
		"POD_IP\n              valueFrom:",
		"fieldPath: status.podIP",
		"VIDEO_CLOUD_MQTT_CLIENT_ID\n              value: \"video-cloud-api-$(POD_NAME)\"",
		"VIDEO_CLOUD_MQTT_TOPIC_ROOT\n              value: \"devices\"",
		"VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY\n              value: \"64\"",
		"VIDEO_CLOUD_MQTT_SHADOW_HANDLER_CONCURRENCY\n              value: \"64\"",
		"VIDEO_CLOUD_MQTT_SHADOW_QUEUE_SIZE\n              value: \"8192\"",
		"VIDEO_CLOUD_MQTT_MESSAGE_HANDLER_CONCURRENCY\n              value: \"128\"",
		"VIDEO_CLOUD_MQTT_MESSAGE_QUEUE_SIZE\n              value: \"16384\"",
		"VIDEO_CLOUD_MQTT_LOG_HANDLER_CONCURRENCY\n              value: \"32\"",
		"VIDEO_CLOUD_MQTT_LOG_QUEUE_SIZE\n              value: \"8192\"",
		"VIDEO_CLOUD_MQTT_OUTBOUND_CONNECTIONS\n              value: \"16\"",
		"VIDEO_CLOUD_MQTT_OUTBOUND_QUEUE_SIZE\n              value: \"8192\"",
		"VIDEO_CLOUD_MQTT_OUTBOUND_WRITE_TIMEOUT\n              value: \"10s\"",
		"VIDEO_CLOUD_SHADOW_CACHE_ENABLED\n              value: \"true\"",
		"VIDEO_CLOUD_SHADOW_CACHE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"VIDEO_CLOUD_SHADOW_CACHE_WRITE_BEHIND_ENABLED\n              value: \"true\"",
		"VIDEO_CLOUD_SHADOW_CACHE_FLUSH_INTERVAL\n              value: \"1s\"",
		"VIDEO_CLOUD_SHADOW_CACHE_FLUSH_BATCH_SIZE\n              value: \"500\"",
		"VIDEO_CLOUD_SHADOW_CACHE_BUFFER_MAX_DOCS\n              value: \"10000\"",
		"VIDEO_CLOUD_SHADOW_CACHE_RECOVERY_INTERVAL\n              value: \"5s\"",
		"VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED\n              value: \"true\"",
		"VIDEO_CLOUD_TURN_REGISTRY_ADDR\n              value: \"http://video-cloud-turnregistry.video-cloud-staging-video-cloud.svc.cluster.local:18190\"",
		"VIDEO_CLOUD_TURN_REGISTRY_CLIENT_NODE_ID\n              value: \"video-cloud-api\"",
		"VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY\n              valueFrom:",
		"VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"kind: Secret\nmetadata:\n  name: mqtt-runtime",
		"cert.pem:",
		"key.pem:",
		"cacert.pem:",
		"kind: ConfigMap\nmetadata:\n  name: mqtt-config",
		"broker: emqx",
		"kind: StatefulSet\nmetadata:\n  name: mqtt",
		"serviceName: mqtt-headless",
		"replicas: 1",
		"podManagementPolicy: Parallel",
		"updateStrategy:",
		"image: emqx/emqx:",
		"EMQX_NODE__NAME",
		"EMQX_CLUSTER__DISCOVERY_STRATEGY",
		`value: "static"`,
		"EMQX_CLUSTER__STATIC__SEEDS",
		"emqx@mqtt-0.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local",
		"whenUnsatisfiable: DoNotSchedule",
		"requiredDuringSchedulingIgnoredDuringExecution:",
		"EMQX_LISTENERS__SSL__DEFAULT__ACCEPTORS",
		`value: "128"`,
		"EMQX_LISTENERS__SSL__DEFAULT__TCP_OPTIONS__BACKLOG",
		`value: "8192"`,
		"emqx ctl cluster status",
		"EMQX_LISTENERS__TCP__DEFAULT__BIND",
		"EMQX_LISTENERS__SSL__DEFAULT__BIND",
		"EMQX_LISTENERS__SSL__DEFAULT__SSL_OPTIONS__CERTFILE",
		"mountPath: /opt/emqx/etc/certs",
		"containerPort: 8883",
		"kind: Service\nmetadata:\n  name: mqtt",
		"kind: Service\nmetadata:\n  name: mqtt-headless",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-emqx-cluster",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-video-cloud-mqtt-clients",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-video-cloud-api-internal",
		"port: 4369",
		"port: 4370",
		"port: 5369",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
	clientPolicyIndex := strings.Index(log, "kind: NetworkPolicy\nmetadata:\n  name: allow-video-cloud-mqtt-clients")
	mqttStatefulSetIndex := strings.Index(log, "kind: StatefulSet\nmetadata:\n  name: mqtt")
	if clientPolicyIndex < 0 || mqttStatefulSetIndex < 0 || clientPolicyIndex > mqttStatefulSetIndex {
		t.Fatalf("MQTT client policy must be applied before the broker starts:\n%s", log)
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

func TestRunProvisionLKEDeployCanExposePublicMQTTNodePort(t *testing.T) {
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
		"type: NodePort",
		"externalTrafficPolicy: Local",
		"name: mqtts\n      port: 8883",
		"nodePort: 31883",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-public-mqtt-loadtest",
		"port: 8883",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
	if strings.Contains(log, "type: LoadBalancer") {
		t.Fatalf("public MQTT must not render a LoadBalancer service, got:\n%s", log)
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

func TestLKEMQTTRewritePreservesSharedPhysicalSubscriptions(t *testing.T) {
	config := lkeEMQXTenantBaseHOCON(map[string]string{})
	if !strings.Contains(config, `\\$share/[^/]+/_bc/`) {
		t.Fatalf("tenant rewrite must exclude shared subscriptions that already contain a physical namespace:\n%s", config)
	}
}

func TestRunProvisionLKEDeployRejectsMultiplePublicMQTTNodePortsForV1(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("LKE_VIDEO_CLOUD_IMAGE", "registry.example.test/rtk/video-cloud:test")
	t.Setenv("LKE_ACCOUNT_MANAGER_IMAGE", "registry.example.test/rtk/account-manager:test")
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "registry.example.test/rtk/cloud-admin:test")
	t.Setenv("LKE_FRONTEND_IMAGE", "registry.example.test/rtk/frontend:test")
	t.Setenv("LKE_CLOUD_LOGGER_IMAGE", "registry.example.test/rtk/cloud-logger:test")
	t.Setenv("LKE_PUBLIC_MQTT_LOADBALANCER", "1")
	t.Setenv("LKE_PUBLIC_MQTT_LOADBALANCER_COUNT", "3")

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--deploy"})
	if err == nil || !strings.Contains(err.Error(), "LKE_PUBLIC_MQTT_LOADBALANCER_COUNT>1 is not supported") {
		t.Fatalf("expected unsupported multiple public MQTT NodePorts error, got %v", err)
	}
}

func TestRunProvisionLKEDNSAppliesPublicHTTPSEdge(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	kubectlLog := fakeKubectl(t)
	helmLog := fakeHelm(t)
	goLog := fakeGoDaddyAPI(t)
	certbotLog := fakeCertbot(t)
	digLog := fakeDig(t, "198.51.100.10")
	t.Setenv("LKE_PUBLIC_HTTPS_ISSUE_EMAIL", "ops@example.test")
	t.Setenv("LKE_PUBLIC_HTTPS_ACME_SERVER", "https://acme-staging-v02.api.letsencrypt.org/directory")
	t.Setenv("LKE_EDGE_HAPROXY_PUBLIC_IP", "198.51.100.10")
	t.Setenv("LKE_EDGE_HAPROXY_PRIVATE_IP", "10.2.1.5")
	t.Setenv("LKE_COTURN_VM_PUBLIC_IP", "198.51.100.20")
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
		"--set controller.service.type=NodePort",
		"--set controller.service.ports.https=443",
		"--set controller.service.targetPorts.https=https",
		"--set controller.service.nodePorts.https=30443",
		"--set controller.service.enableHttp=false",
		"--set controller.allowSnippetAnnotations=true",
		"--set controller.config.annotations-risk-level=Critical",
		"--set controller.replicaCount=1",
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
		"kind: Service\nmetadata:\n  name: public-video-cloud-turnregistry-video-cloud",
		"externalName: video-cloud-turnregistry.video-cloud-staging-video-cloud.svc.cluster.local",
		"kind: Ingress\nmetadata:\n  name: video-cloud-staging-public",
		"kind: Ingress\nmetadata:\n  name: video-cloud-staging-device-mtls",
		"kind: Ingress\nmetadata:\n  name: video-cloud-staging-certissuer",
		"nginx.ingress.kubernetes.io/proxy-connect-timeout: \"60\"",
		"nginx.ingress.kubernetes.io/proxy-read-timeout: \"3600\"",
		"nginx.ingress.kubernetes.io/proxy-send-timeout: \"3600\"",
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
		"host: turnregistry.video-cloud-staging.realtekconnect.com",
		"host: account-manager.video-cloud-staging.realtekconnect.com",
		"host: admin.video-cloud-staging.realtekconnect.com",
		"host: frontend.video-cloud-staging.realtekconnect.com",
		"name: public-video-cloud-api-video-cloud\n                port:\n                  number: 80",
		"name: public-certissuer-video-cloud\n                port:\n                  number: 9443",
		"name: public-video-cloud-turnregistry-video-cloud\n                port:\n                  number: 18190",
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
		"kind: NetworkPolicy\nmetadata:\n  name: allow-video-cloud-api-internal",
		"app.kubernetes.io/name: video-cloud-api",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-video-cloud-api-turnregistry",
		"app.kubernetes.io/name: video-cloud-turnregistry",
		"port: 18190",
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
		"controller.service.ports.http=80",
		"port: 3478",
		"status.loadBalancer.ingress",
	} {
		if strings.Contains(kubectlCalls, forbidden) {
			t.Fatalf("public HTTPS edge must not expose %q, got:\n%s", forbidden, kubectlCalls)
		}
	}

	goCalls := readTestFile(t, goLog)
	for _, want := range []string{
		"--name video-cloud-staging --data 198.51.100.10 --ttl 600",
		"--name device.video-cloud-staging --data 198.51.100.10 --ttl 600",
		"--name certissuer.video-cloud-staging --data 198.51.100.10 --ttl 600",
		"--name turnregistry.video-cloud-staging --data 198.51.100.10 --ttl 600",
		"--name account-manager.video-cloud-staging --data 198.51.100.10 --ttl 600",
		"--name admin.video-cloud-staging --data 198.51.100.10 --ttl 600",
		"--name frontend.video-cloud-staging --data 198.51.100.10 --ttl 600",
		"--name turn.video-cloud-staging --data 198.51.100.20 --ttl 600",
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
	if !strings.Contains(readTestFile(t, digLog), "turn.video-cloud-staging.realtekconnect.com") {
		t.Fatalf("expected TURN DNS convergence checks, got:\n%s", readTestFile(t, digLog))
	}
	if !strings.Contains(readTestFile(t, digLog), "turnregistry.video-cloud-staging.realtekconnect.com") {
		t.Fatalf("expected TURN registry DNS convergence checks, got:\n%s", readTestFile(t, digLog))
	}
	edgeDir := filepath.Join(envRoot, "artifacts", "edge-haproxy")
	for _, name := range []string{"edge-vms.json", "upstreams.json", "haproxy.cfg", "install.sh", "validation.json"} {
		if _, err := os.Stat(filepath.Join(edgeDir, name)); err != nil {
			t.Fatalf("expected edge HAProxy artifact %s: %v", name, err)
		}
	}
	var edgeVMs struct {
		SSHAccess lkeEdgeHAProxySSHAccess `json:"ssh_access"`
	}
	if err := json.Unmarshal([]byte(readTestFile(t, filepath.Join(edgeDir, "edge-vms.json"))), &edgeVMs); err != nil {
		t.Fatalf("read edge-vms.json: %v", err)
	}
	if edgeVMs.SSHAccess.User != "root" || edgeVMs.SSHAccess.KeyPath == "" || edgeVMs.SSHAccess.PublicKeyPath == "" {
		t.Fatalf("expected edge-vms.json to include SSH access paths, got: %+v", edgeVMs.SSHAccess)
	}
	cfg := readTestFile(t, filepath.Join(edgeDir, "haproxy.cfg"))
	for _, want := range []string{
		"maxconn 400000",
		"frontend public_https_443",
		"bind *:443",
		"backend k8s_ingress_https",
		"backend k8s_ingress_https\n    balance roundrobin",
		"server lke-node-1 10.2.1.10:30443 check",
		"server lke-node-2 10.2.1.11:30443 check",
		"server lke-node-3 10.2.1.12:30443 check",
		"frontend public_mqtts_8883",
		"bind *:8883",
		"backend k8s_mqtts",
		"backend k8s_mqtts\n    balance roundrobin",
		"server mqtt-node-1 10.2.1.10:31883 check",
		"server mqtt-node-2 10.2.1.12:31883 check",
	} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("expected %q in HAProxy config, got:\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "mqtt-node-3 10.2.1.11:31883") {
		t.Fatalf("MQTT HAProxy backend must only include nodes with MQTT pods, got:\n%s", cfg)
	}

	coturnDir := filepath.Join(envRoot, "artifacts", "coturn-vm")
	for _, name := range []string{"coturn-vm.json", "turnserver.conf.redacted", "install.sh", "validation.json"} {
		if _, err := os.Stat(filepath.Join(coturnDir, name)); err != nil {
			t.Fatalf("expected coturn VM artifact %s: %v", name, err)
		}
	}
	coturnVM := readTestFile(t, filepath.Join(coturnDir, "coturn-vm.json"))
	for _, want := range []string{
		`"public_ip": "198.51.100.20"`,
		`"domain": "turn.video-cloud-staging.realtekconnect.com"`,
		`"name": "turn01"`,
		`"role": "coturn-vm"`,
	} {
		if !strings.Contains(coturnVM, want) {
			t.Fatalf("expected %q in coturn-vm.json, got:\n%s", want, coturnVM)
		}
	}
	redactedConf := readTestFile(t, filepath.Join(coturnDir, "turnserver.conf.redacted"))
	for _, want := range []string{"use-auth-secret", "static-auth-secret=<redacted>", "realm=video_cloud", "min-port=49152", "max-port=65535", "verbose", "cli-ip=127.0.0.1", "cli-port=5766", "cli-password=<redacted>"} {
		if !strings.Contains(redactedConf, want) {
			t.Fatalf("expected %q in redacted coturn config, got:\n%s", want, redactedConf)
		}
	}
	installScript := readTestFile(t, filepath.Join(coturnDir, "install.sh"))
	for _, want := range []string{
		"install -m 0755 /tmp/video-cloud-turnregistrar /opt/video_cloud/bin/turnregistrar",
		"VIDEO_CLOUD_TURN_REGISTRY_ADDR=%s",
		"https://turnregistry.video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_TURN_NODE_ID=%s",
		"turn01",
		"VIDEO_CLOUD_TURN_NODE_PUBLIC_HOST=%s",
		"turn.video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_TURN_NODE_UDP_PORT=3478",
		"VIDEO_CLOUD_TURN_NODE_TCP_PORT=3478",
		"ss -lntp | grep -E '127\\.0\\.0\\.1:5766\\b'",
		"video-cloud-turnregistrar.service",
		"turn registry register succeeded",
	} {
		if !strings.Contains(installScript, want) {
			t.Fatalf("expected %q in coturn install script, got:\n%s", want, installScript)
		}
	}
	for _, forbidden := range []string{"test-seed-turn-registry-node-auth", "test-seed-turn-shared", "COTURN_HEAD"} {
		if strings.Contains(installScript, forbidden) {
			t.Fatalf("coturn install script must not contain %q, got:\n%s", forbidden, installScript)
		}
	}
}

func TestLKECoturnVMNamingUsesTurnNNWithinStackLabel(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if got := lkeCoturnVMName(env); got != "turn01" {
		t.Fatalf("default coturn VM name = %q, want turn01", got)
	}
	if got := lkeCoturnVMLabel(env); got != "video-cloud-staging-turn01" {
		t.Fatalf("default coturn VM label = %q, want video-cloud-staging-turn01", got)
	}

	env["LKE_COTURN_VM_INDEX"] = "2"
	if got := lkeCoturnVMName(env); got != "turn02" {
		t.Fatalf("indexed coturn VM name = %q, want turn02", got)
	}
	if got := lkeCoturnVMLabel(env); got != "video-cloud-staging-turn02" {
		t.Fatalf("indexed coturn VM label = %q, want video-cloud-staging-turn02", got)
	}

	env["LKE_COTURN_VM_NAME"] = "custom-turn"
	if got := lkeCoturnVMName(env); got != "custom-turn" {
		t.Fatalf("explicit coturn VM name = %q, want custom-turn", got)
	}
	if got := lkeCoturnVMLabel(env); got != "video-cloud-staging-custom-turn" {
		t.Fatalf("explicit coturn VM label = %q, want video-cloud-staging-custom-turn", got)
	}
}

func TestLKECoturnMultiVMUsesPerNodeDomainsAndURLs(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"CLOUD_DNS_ROOT_DOMAIN": "realtekconnect.com",
		"LKE_COTURN_VM_COUNT":   "2",
	}
	first := lkeCoturnVMEnvForIndex(env, 1)
	second := lkeCoturnVMEnvForIndex(env, 2)
	if got := lkeCoturnVMName(first); got != "turn01" {
		t.Fatalf("first name = %q, want turn01", got)
	}
	if got := lkeCoturnVMName(second); got != "turn02" {
		t.Fatalf("second name = %q, want turn02", got)
	}
	if got := lkeCoturnDomain(first); got != "turn01.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("first domain = %q", got)
	}
	if got := lkeCoturnDomain(second); got != "turn02.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("second domain = %q", got)
	}
	if got := lkeCoturnSTUNURLs(env); got != "stun:turn01.video-cloud-staging.realtekconnect.com:3478,stun:turn02.video-cloud-staging.realtekconnect.com:3478" {
		t.Fatalf("STUN URLs = %q", got)
	}
	if got := lkeCoturnTURNURLs(env); got != "turn:turn01.video-cloud-staging.realtekconnect.com:3478?transport=udp,turn:turn01.video-cloud-staging.realtekconnect.com:3478?transport=tcp,turn:turn02.video-cloud-staging.realtekconnect.com:3478?transport=udp,turn:turn02.video-cloud-staging.realtekconnect.com:3478?transport=tcp" {
		t.Fatalf("TURN URLs = %q", got)
	}
}

func TestLKECoturnMultiVMProvidedIPsWritesArtifacts(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	env := map[string]string{
		"CLOUD_STACK_NAME":           "video-cloud-staging",
		"CLOUD_DNS_ROOT_DOMAIN":      "realtekconnect.com",
		"CLOUD_REGION":               "us-sea",
		"LKE_COTURN_VM_COUNT":        "2",
		"LKE_COTURN_VM_PUBLIC_IPS":   "198.51.100.20,198.51.100.21",
		"LKE_COTURN_VM_TYPE":         "g6-standard-2",
		"LKE_COTURN_MAX_SESSIONS":    "10000",
		"LKE_COTURN_MIN_PORT":        "49152",
		"LKE_COTURN_MAX_PORT":        "65535",
		"LKE_COTURN_VM_BOOT_TIMEOUT": "1s",
	}
	vms, err := lkeEnsureExternalCoturnVMs(provisionPaths{Workspace: workspace, EnvRoot: envRoot}, env, provisionOptions{})
	if err != nil {
		t.Fatalf("lkeEnsureExternalCoturnVMs() error = %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("vms = %d, want 2", len(vms))
	}
	if vms[0].Domain != "turn01.video-cloud-staging.realtekconnect.com" || vms[1].Domain != "turn02.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("domains = %+v", vms)
	}
	for _, name := range []string{
		"coturn-vm/turn01/coturn-vm.json",
		"coturn-vm/turn02/coturn-vm.json",
		"coturn-vm/coturn-vms.json",
		"coturn-vm/coturn-vm.json",
	} {
		if _, err := os.Stat(filepath.Join(envRoot, "artifacts", name)); err != nil {
			t.Fatalf("missing artifact %s: %v", name, err)
		}
	}
	summary := readTestFile(t, filepath.Join(envRoot, "artifacts", "coturn-vm", "coturn-vms.json"))
	for _, want := range []string{`"count": 2`, `"name": "turn01"`, `"name": "turn02"`, `"public_ip": "198.51.100.21"`} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected %q in coturn-vms summary:\n%s", want, summary)
		}
	}
}

func TestLKEPruneExtraCoturnVMsDeletesOnlyNodesAboveDesiredCount(t *testing.T) {
	_, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/linode/instances?page_size=500": `{"data":[
		  {"id":201,"label":"video-cloud-staging-edge-haproxy-01","region":"us-sea","status":"running","ipv4":["192.0.2.10"],"tags":["rtk-cloud","video-cloud-staging","edge-haproxy"]},
		  {"id":301,"label":"video-cloud-staging-turn01","region":"us-sea","status":"running","ipv4":["192.0.2.20"],"tags":["rtk-cloud","video-cloud-staging","coturn-vm"]},
		  {"id":302,"label":"video-cloud-staging-turn02","region":"us-sea","status":"running","ipv4":["192.0.2.21"],"tags":["rtk-cloud","video-cloud-staging","coturn-vm"]},
		  {"id":303,"label":"other-stack-turn02","region":"us-sea","status":"running","ipv4":["192.0.2.22"],"tags":["rtk-cloud","other-stack","coturn-vm"]}
		]}`,
		"/linode/instances/302": `{}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")
	err := lkePruneExtraCoturnVMs(provisionPaths{EnvRoot: envRoot}, map[string]string{
		"CLOUD_STACK_NAME":    "video-cloud-staging",
		"LKE_COTURN_VM_COUNT": "1",
	})
	if err != nil {
		t.Fatalf("lkePruneExtraCoturnVMs() error = %v", err)
	}
	log := readTestFile(t, curlLog)
	if !strings.Contains(log, "DELETE /linode/instances/302") {
		t.Fatalf("expected turn02 delete, got:\n%s", log)
	}
	for _, forbidden := range []string{"DELETE /linode/instances/201", "DELETE /linode/instances/301", "DELETE /linode/instances/303"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("unexpected delete %q, got:\n%s", forbidden, log)
		}
	}
}

func TestRunProvisionLKEIngressHelmTimesOut(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	t.Setenv("GODADDY_KEY", "test-key")
	t.Setenv("GODADDY_SECRET", "test-secret")
	helm := filepath.Join(t.TempDir(), "helm")
	if err := os.WriteFile(helm, []byte("#!/usr/bin/env bash\nsleep 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_HELM", helm)
	t.Setenv("LKE_INGRESS_HELM_TIMEOUT", "1s")

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"})
	if err == nil || !strings.Contains(err.Error(), "helm command timed out after 1s") {
		t.Fatalf("expected ingress helm timeout error, got %v", err)
	}
}

func TestLKECreateEdgeHAProxyVMClassifiesActiveServicesLimit(t *testing.T) {
	sshKey := filepath.Join(t.TempDir(), "id_ed25519_rtkcloud")
	writeTestFile(t, sshKey+".pub", "ssh-ed25519 AAAATEST edge@test\n")
	fakeLinodeCurl(t, map[string]string{
		"/linode/instances": "__ERROR_400_ACCOUNT_LIMIT__",
	})

	_, err := lkeCreateEdgeHAProxyVM("test-token", map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging",
		"CLOUD_REGION":     "us-sea",
	}, provisionOptions{sshKey: sshKey})
	if err == nil || !strings.Contains(err.Error(), "Linode active services limit reached") {
		t.Fatalf("expected active services limit classification, got %v", err)
	}
}

func TestRunProvisionLKEDNSIncludesCloudLoggerWhenServiceExists(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	kubectlLog := fakeKubectl(t)
	goLog := fakeGoDaddyAPI(t)
	fakeHelm(t)
	fakeCertbot(t)
	fakeDig(t, "198.51.100.10")
	t.Setenv("FAKE_CLOUD_LOGGER_SERVICE", "1")
	t.Setenv("LKE_EDGE_HAPROXY_PUBLIC_IP", "198.51.100.10")
	t.Setenv("LKE_EDGE_HAPROXY_PRIVATE_IP", "10.2.1.5")
	t.Setenv("LKE_COTURN_VM_PUBLIC_IP", "198.51.100.20")
	t.Setenv("GODADDY_KEY", "test-key")
	t.Setenv("GODADDY_SECRET", "test-secret")
	t.Setenv("GODADDY_ENV", "prod")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"}); err != nil {
		t.Fatal(err)
	}

	kubectlCalls := readTestFile(t, kubectlLog)
	for _, want := range []string{
		"kind: Service\nmetadata:\n  name: public-cloud-logger-logger",
		"externalName: cloud-logger.video-cloud-staging-logger.svc.cluster.local",
		"host: logger.video-cloud-staging.realtekconnect.com",
		"namespace: video-cloud-staging-logger",
		"port: 18090",
	} {
		if !strings.Contains(kubectlCalls, want) {
			t.Fatalf("expected %q in kubectl calls, got:\n%s", want, kubectlCalls)
		}
	}
	goCalls := readTestFile(t, goLog)
	if !strings.Contains(goCalls, "--name logger.video-cloud-staging --data 198.51.100.10 --ttl 600") {
		t.Fatalf("expected logger DNS upsert, got:\n%s", goCalls)
	}
}

func TestRunProvisionLKEPublicHTTPSRestoresCachedCertificateBeforeACME(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	kubectlLog := fakeKubectl(t)
	fakeHelm(t)
	failingCertbot := filepath.Join(t.TempDir(), "certbot")
	if err := os.WriteFile(failingCertbot, []byte("#!/usr/bin/env bash\nprintf 'certbot must not run\\n' >&2\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CERTBOT", failingCertbot)
	fakeDNSCommandsWithGoDelay(t, "198.51.100.10", "0", "198.51.100.20")
	fakeDig(t, "198.51.100.10")
	t.Setenv("LKE_EDGE_HAPROXY_PUBLIC_IP", "198.51.100.10")
	t.Setenv("LKE_EDGE_HAPROXY_PRIVATE_IP", "10.2.1.5")
	t.Setenv("LKE_COTURN_VM_PUBLIC_IP", "198.51.100.20")
	t.Setenv("GODADDY_KEY", "test-key")
	t.Setenv("GODADDY_SECRET", "test-secret")
	t.Setenv("GODADDY_ENV", "prod")

	loadedEnv, err := envroot.Load(envRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	env := loadedEnv.Values
	hosts := lkePublicHTTPSHosts(lkePublicHTTPSRoutes(env))
	caCert, caKey, _, _, err := newLKECertificateAuthority("test-public-cache-ca")
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := newLKESignedCertificate(caCert, caKey, hosts[0], hosts, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if err != nil {
		t.Fatal(err)
	}
	if err := lkeWritePublicHTTPSCertificateCache(provisionPaths{EnvRoot: envRoot}, env, hosts, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := lkeLoadPublicHTTPSCertificateCache(provisionPaths{EnvRoot: envRoot}, env, hosts); err != nil || !ok {
		t.Fatalf("expected public HTTPS cache to cover test hosts: ok=%v err=%v hosts=%v", ok, err, hosts)
	}

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"}); err != nil {
		t.Fatal(err)
	}

	kubectlCalls := readTestFile(t, kubectlLog)
	if !strings.Contains(kubectlCalls, "kind: Secret\nmetadata:\n  name: video-cloud-staging-public-tls\n  namespace: video-cloud-staging-ingress") {
		t.Fatalf("expected cached TLS secret to be applied, got:\n%s", kubectlCalls)
	}
	if !strings.Contains(kubectlCalls, base64.StdEncoding.EncodeToString([]byte(certPEM))) {
		t.Fatalf("expected applied TLS secret to use cached certificate, got:\n%s", kubectlCalls)
	}
}

func TestRunProvisionLKEPublicHTTPSStartsDNSUpsertsBeforeWaiting(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	fakeHelm(t)
	eventLog := fakeDNSCommandsWithGoDelay(t, "198.51.100.10", "0.2", "198.51.100.20")
	fakeCertbot(t)
	t.Setenv("LKE_PUBLIC_HTTPS_ISSUE_EMAIL", "ops@example.test")
	t.Setenv("LKE_PUBLIC_HTTPS_ACME_SERVER", "https://acme-staging-v02.api.letsencrypt.org/directory")
	t.Setenv("LKE_COTURN_VM_PUBLIC_IP", "198.51.100.20")
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
		"--name turnregistry.video-cloud-staging",
		"--name account-manager.video-cloud-staging",
		"--name admin.video-cloud-staging",
		"--name frontend.video-cloud-staging",
	} {
		idx := strings.Index(events, "GO ARGS records upsert")
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
	fakeKubectl(t)
	t.Setenv("FAKE_CLOUD_LOGGER_SERVICE", "1")
	env := map[string]string{
		"CLOUD_STACK_NAME":              "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":            "video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":        "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":            "admin.video-cloud-staging.realtekconnect.com",
		"FRONTEND_DOMAIN":               "frontend.video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_DEVICE_DOMAIN":     "device.video-cloud-staging.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":           "logger.video-cloud-staging.realtekconnect.com",
	}
	manifests := strings.Join(lkePublicHTTPSNetworkPolicyManifests(env, lkePublicHTTPSRoutes(env)), "\n---\n")
	for namespace, wantPort := range map[string]string{
		"video-cloud-staging-video-cloud":     "8080",
		"video-cloud-staging-account-manager": "8080",
		"video-cloud-staging-admin":           "8080",
		"video-cloud-staging-frontend":        "8080",
		"video-cloud-staging-logger":          "18090",
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
		if !strings.Contains(chunk, "port: "+wantPort) {
			t.Fatalf("public ingress policy for %s must allow backend pod port %s, got:\n%s", namespace, wantPort, chunk)
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

func TestLKENetworkPoliciesAllowVideoCloudAPIInternalRouting(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	policy := lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env)
	for _, want := range []string{
		"name: allow-video-cloud-api-internal",
		"namespace: video-cloud-staging-video-cloud",
		"podSelector:\n    matchLabels:\n      app.kubernetes.io/name: video-cloud-api",
		"from:\n        - podSelector:\n            matchLabels:\n              app.kubernetes.io/name: video-cloud-api",
		"port: 8080",
	} {
		if !strings.Contains(policy, want) {
			t.Fatalf("expected %q in video-cloud API internal NetworkPolicy, got:\n%s", want, policy)
		}
	}
}

func TestLKENetworkPoliciesAllowVideoCloudAPITurnRegistryRouting(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	policy := lkeAllowVideoCloudAPITurnRegistryNetworkPolicyManifest(env)
	for _, want := range []string{
		"name: allow-video-cloud-api-turnregistry",
		"namespace: video-cloud-staging-video-cloud",
		"podSelector:\n    matchLabels:\n      app.kubernetes.io/name: video-cloud-turnregistry",
		"from:\n        - podSelector:\n            matchLabels:\n              app.kubernetes.io/name: video-cloud-api",
		"port: 18190",
	} {
		if !strings.Contains(policy, want) {
			t.Fatalf("expected %q in video-cloud API turnregistry NetworkPolicy, got:\n%s", want, policy)
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

func TestRunProvisionLKEDNSRequiresGoDaddyCredentialsBeforeDNSMutation(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	if err := os.MkdirAll(filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	fakeHelm(t)
	goLog := fakeGoDaddyAPI(t)
	fakeCertbot(t)

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--dns"})
	if err == nil || !strings.Contains(err.Error(), "GoDaddy DNS credentials missing") {
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
	goLog := fakeGoDaddyAPI(t)
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

func TestLKEApplyPostgresStatefulSetReplacesStaleRevisionPod(t *testing.T) {
	logPath := fakeKubectl(t)
	t.Setenv("FAKE_POSTGRES_STATEFULSET_REVISIONS", "postgresql-old postgresql-new")

	if err := lkeApplyPostgresStatefulSet(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	want := "ARGS -n video-cloud-staging-platform delete pod/postgresql-0 --ignore-not-found=true"
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

func TestLKEPostgresStatefulSetSetsMaxConnections(t *testing.T) {
	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	for _, want := range []string{
		`- "-c"`,
		`- "max_connections=800"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in PostgreSQL manifest, got:\n%s", want, manifest)
		}
	}
}

func TestLKEPostgresStatefulSetSupportsMaxConnectionsOverride(t *testing.T) {
	t.Setenv("LKE_POSTGRES_MAX_CONNECTIONS", "500")

	manifest := lkePostgresStatefulSetManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"})

	if !strings.Contains(manifest, `- "max_connections=500"`) {
		t.Fatalf("expected LKE_POSTGRES_MAX_CONNECTIONS override in PostgreSQL manifest, got:\n%s", manifest)
	}
}
func TestLKELoadTestCapacityManifestsSetResourcesAndPlacement(t *testing.T) {
	t.Setenv("LKE_POSTGRES_NODE_POOL_ID", "906225")
	t.Setenv("LKE_MQTT_NODE_POOL_ID", "906225")
	env := map[string]string{
		"CLOUD_STACK_NAME":        "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":      "video-cloud-staging.realtekconnect.com",
		"MQTT_EFFECTIVE_REPLICAS": "4",
	}
	env = withArchitectureWorkloadDefaults(t, env)

	postgres := lkePostgresStatefulSetManifest(env)
	for _, want := range []string{
		`rtk.io/node-class: "database"`,
		`value: "database"`,
		`cpu: "1"`,
		`memory: "2Gi"`,
		`memory: "4Gi"`,
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
		"whenUnsatisfiable: DoNotSchedule",
		`cpu: "250m"`,
		`memory: "256Mi"`,
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
		"replicas: 2",
		"strategy:",
		"maxSurge: 0",
		"maxUnavailable: 1",
		"topologySpreadConstraints:",
		"whenUnsatisfiable: DoNotSchedule",
		`cpu: "500m"`,
		`memory: "512Mi"`,
		`memory: "1536Mi"`,
		"name: VIDEO_CLOUD_DB_MAX_OPEN_CONNS\n              value: \"40\"",
		"name: VIDEO_CLOUD_DB_MAX_IDLE_CONNS\n              value: \"20\"",
		"name: VIDEO_CLOUD_DB_CONN_MAX_LIFETIME\n              value: \"5m\"",
		"name: VIDEO_CLOUD_DB_ENSURE_SCHEMA\n              value: \"false\"",
		"readinessProbe:\n            httpGet:\n              path: /healthz\n              port: http",
		"livenessProbe:\n            httpGet:\n              path: /healthz\n              port: http",
		"name: VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY\n              value: \"64\"",
		"name: VIDEO_CLOUD_MQTT_SHADOW_HANDLER_CONCURRENCY\n              value: \"64\"",
		"name: VIDEO_CLOUD_MQTT_SHADOW_QUEUE_SIZE\n              value: \"8192\"",
		"name: VIDEO_CLOUD_MQTT_MESSAGE_HANDLER_CONCURRENCY\n              value: \"128\"",
		"name: VIDEO_CLOUD_MQTT_MESSAGE_QUEUE_SIZE\n              value: \"16384\"",
		"name: VIDEO_CLOUD_MQTT_LOG_HANDLER_CONCURRENCY\n              value: \"32\"",
		"name: VIDEO_CLOUD_MQTT_LOG_QUEUE_SIZE\n              value: \"8192\"",
		"name: VIDEO_CLOUD_MQTT_OUTBOUND_CONNECTIONS\n              value: \"16\"",
		"name: VIDEO_CLOUD_MQTT_OUTBOUND_QUEUE_SIZE\n              value: \"8192\"",
		"name: VIDEO_CLOUD_MQTT_OUTBOUND_WRITE_TIMEOUT\n              value: \"10s\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_WRITE_BEHIND_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_FLUSH_INTERVAL\n              value: \"1s\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_FLUSH_BATCH_SIZE\n              value: \"500\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_BUFFER_MAX_DOCS\n              value: \"10000\"",
		"name: VIDEO_CLOUD_SHADOW_CACHE_RECOVERY_INTERVAL\n              value: \"5s\"",
		"name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ADDR\n              value: \"redis.video-cloud-staging-platform.svc.cluster.local:6379\"",
		"name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_PREFIX\n              value: \"video_cloud:webrtc\"",
		"name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_TTL_GRACE\n              value: \"30s\"",
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
		"name: VIDEO_CLOUD_MQTT_LOG_HANDLER_CONCURRENCY\n              value: \"1\"",
		"name: VIDEO_CLOUD_LOG_INGESTER_WORKER_COUNT\n              value: \"1\"",
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
		"kind: StatefulSet",
		"serviceName: mqtt-headless",
		"replicas: 4",
		"podManagementPolicy: Parallel",
		"updateStrategy:",
		`rtk.io/node-class: "broker"`,
		"fieldPath: metadata.name",
		"EMQX_NODE__NAME",
		`value: "emqx@$(POD_NAME).mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local"`,
		"EMQX_CLUSTER__DISCOVERY_STRATEGY",
		`value: "static"`,
		"EMQX_CLUSTER__STATIC__SEEDS",
		"emqx@mqtt-0.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local",
		"emqx@mqtt-1.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local",
		"emqx@mqtt-2.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local",
		"emqx@mqtt-3.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local",
		"topologySpreadConstraints:",
		"whenUnsatisfiable: DoNotSchedule",
		"podAntiAffinity:",
		"requiredDuringSchedulingIgnoredDuringExecution:",
		"EMQX_LISTENERS__SSL__DEFAULT__ACCEPTORS",
		`value: "128"`,
		"EMQX_LISTENERS__SSL__DEFAULT__TCP_OPTIONS__BACKLOG",
		`value: "8192"`,
		"EMQX_FORCE_SHUTDOWN__MAX_MAILBOX_SIZE",
		`value: "131072"`,
		"EMQX_FORCE_SHUTDOWN__MAX_HEAP_SIZE",
		`value: "512MB"`,
		`cpu: "250m"`,
		`memory: "512Mi"`,
		`memory: "1536Mi"`,
	} {
		if !strings.Contains(mqtt, want) {
			t.Fatalf("expected %q in mqtt manifest, got:\n%s", want, mqtt)
		}
	}
	if strings.Contains(mqtt, "kind: Deployment") {
		t.Fatalf("MQTT must be a StatefulSet, got Deployment:\n%s", mqtt)
	}
}

func TestLKEPostgresDedicatedNodePoolDefaultsFor100K(t *testing.T) {
	env := map[string]string{
		"LKE_TARGET_CONNECTS": "100000",
		"LKE_NODE_TYPE":       "g6-standard-4",
	}
	if !lkePostgresDedicatedNodePoolEnabled(env) {
		t.Fatal("expected 100K target to enable dedicated postgres node pool")
	}
	payload := lkePostgresNodePoolPayload(env)
	if got := payload["type"]; got != "g6-standard-8" {
		t.Fatalf("postgres node pool type = %v, want g6-standard-8", got)
	}
	if got := payload["count"]; got != 1 {
		t.Fatalf("postgres node pool count = %v, want 1", got)
	}
	if got := payload["label"]; got != "postgres" {
		t.Fatalf("postgres node pool label = %v, want postgres", got)
	}
	labels, ok := payload["labels"].(map[string]string)
	if !ok || labels["rtk.io/node-class"] != "database" {
		t.Fatalf("postgres node pool labels = %#v", payload["labels"])
	}
	taints, ok := payload["taints"].([]map[string]string)
	if !ok || len(taints) != 1 {
		t.Fatalf("postgres node pool taints = %#v", payload["taints"])
	}
	if taints[0]["key"] != "rtk.io/node-class" || taints[0]["value"] != "database" || taints[0]["effect"] != "NoSchedule" {
		t.Fatalf("postgres node pool taint = %#v", taints[0])
	}
}

func TestLKENodePoolHasPostgresPlacement(t *testing.T) {
	pool := lkeNodePool{
		ID:    906225,
		Type:  "g6-standard-4",
		Count: 1,
		Label: "postgres",
		Labels: map[string]string{
			"rtk.io/node-class": "database",
		},
		Taints: []lkeNodePoolTaint{{
			Key:    "rtk.io/node-class",
			Value:  "database",
			Effect: "NoSchedule",
		}},
	}
	if !lkeNodePoolHasPostgresPlacement(pool) {
		t.Fatalf("expected postgres placement match")
	}
	pool.Taints = nil
	if lkeNodePoolHasPostgresPlacement(pool) {
		t.Fatalf("expected missing taint to fail postgres placement match")
	}
}

func TestLKEPostgresPlacementUsesWorkloadSelector(t *testing.T) {
	manifest := lkePostgresPlacementManifest(map[string]string{
		"LKE_POSTGRES_NODE_POOL_ID": "918100",
	})
	for _, want := range []string{
		`rtk.io/node-class: "database"`,
		`effect: "NoSchedule"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected postgres placement to contain %q, got:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "lke.linode.com/pool-id") {
		t.Fatalf("postgres placement must not depend on Linode pool-id node labels, got:\n%s", manifest)
	}
}

func TestEnsureLKEPostgresNodePoolCreatesReplacementForImmutableTypeChange(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters/12345/pools": `{"id":918100,"type":"g6-standard-8","count":1,"label":"postgres","labels":{"rtk.io/node-class":"database"},"taints":[{"key":"rtk.io/node-class","value":"database","effect":"NoSchedule"}]}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")
	env := map[string]string{
		"LKE_POSTGRES_NODE_POOL_ID": "917710",
		"LKE_POSTGRES_NODE_TYPE":    "g6-standard-8",
		"LKE_POSTGRES_NODE_COUNT":   "1",
	}
	pools := []lkeNodePool{{
		ID:    917710,
		Type:  "g6-standard-4",
		Count: 1,
		Label: "postgres",
		Labels: map[string]string{
			"rtk.io/node-class": "database",
		},
		Taints: []lkeNodePoolTaint{{
			Key:    "rtk.io/node-class",
			Value:  "database",
			Effect: "NoSchedule",
		}},
	}}

	if err := ensureLKEPostgresNodePool(provisionPaths{Workspace: workspace, EnvRoot: envRoot}, env, "test-token", "12345", pools); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if !strings.Contains(curlCalls, "POST /lke/clusters/12345/pools") {
		t.Fatalf("expected replacement pool create, got:\n%s", curlCalls)
	}
	if strings.Contains(curlCalls, "PUT /lke/clusters/12345/pools/917710") {
		t.Fatalf("postgres pool type is immutable; must not PUT old pool, got:\n%s", curlCalls)
	}
	if got := env["LKE_POSTGRES_NODE_POOL_ID"]; got != "918100" {
		t.Fatalf("postgres pool id = %s, want replacement id 918100", got)
	}
	adapterState := readTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"))
	if !strings.Contains(adapterState, "LKE_POSTGRES_NODE_POOL_ID=918100") {
		t.Fatalf("expected replacement pool id persisted in adapter state, got:\n%s", adapterState)
	}
}

func TestEnsureLKEPostgresNodePoolFallsBackFromStaleIDAndPrunesDuplicates(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters/12345/pools/918804": `{}`,
	})
	env := map[string]string{
		"LKE_POSTGRES_NODE_POOL_ID": "999999",
		"LKE_POSTGRES_NODE_TYPE":    "g6-standard-6",
		"LKE_POSTGRES_NODE_COUNT":   "1",
	}
	pools := []lkeNodePool{
		{
			ID:    918802,
			Type:  "g6-standard-6",
			Count: 1,
			Label: "postgres",
			Labels: map[string]string{
				"rtk.io/node-class": "database",
			},
			Taints: []lkeNodePoolTaint{{
				Key:    "rtk.io/node-class",
				Value:  "database",
				Effect: "NoSchedule",
			}},
		},
		{
			ID:    918804,
			Type:  "g6-standard-6",
			Count: 1,
			Label: "postgres",
			Labels: map[string]string{
				"rtk.io/node-class": "database",
			},
			Taints: []lkeNodePoolTaint{{
				Key:    "rtk.io/node-class",
				Value:  "database",
				Effect: "NoSchedule",
			}},
		},
	}

	if err := ensureLKEPostgresNodePool(provisionPaths{Workspace: workspace, EnvRoot: envRoot}, env, "test-token", "12345", pools); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if strings.Contains(curlCalls, "POST /lke/clusters/12345/pools") {
		t.Fatalf("stale postgres pool id should fall back to existing postgres pool, got:\n%s", curlCalls)
	}
	if !strings.Contains(curlCalls, "DELETE /lke/clusters/12345/pools/918804") {
		t.Fatalf("expected duplicate postgres pool prune, got:\n%s", curlCalls)
	}
	if got := env["LKE_POSTGRES_NODE_POOL_ID"]; got != "918802" {
		t.Fatalf("postgres pool id = %s, want 918802", got)
	}
	adapterState := readTestFile(t, filepath.Join(envRoot, "adapters", "lke", "state.env"))
	if !strings.Contains(adapterState, "LKE_POSTGRES_NODE_POOL_ID=918802") {
		t.Fatalf("expected fallback pool id persisted in adapter state, got:\n%s", adapterState)
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

func TestLKEMQTTReplicasAreConfigurableCapacity(t *testing.T) {
	t.Setenv("LKE_MQTT_REPLICAS", "6")
	env := map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-staging.realtekconnect.com",
	}
	manifest := lkeMQTTDeploymentManifest(env)
	if !strings.Contains(manifest, "replicas: 6") {
		t.Fatalf("expected MQTT replicas to follow LKE_MQTT_REPLICAS, got:\n%s", manifest)
	}
	if !strings.Contains(manifest, "mqtt-5.mqtt-headless") {
		t.Fatalf("expected EMQX seeds to scale through mqtt-5, got:\n%s", manifest)
	}
	if strings.Contains(manifest, "mqtt-6.mqtt-headless") {
		t.Fatalf("EMQX seeds must not exceed requested replicas, got:\n%s", manifest)
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

func TestLKEVideoCloudMQTTHandlerConcurrencyCanBeOverridden(t *testing.T) {
	t.Setenv("LKE_VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY", "1024")
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
	if !strings.Contains(manifest, "name: VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY\n              value: \"1024\"") {
		t.Fatalf("video-cloud-api MQTT handler concurrency override missing:\n%s", manifest)
	}
}

func TestLKEMQTTResourcesCanBeOverridden(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "MQTT_REQUEST_CPU": "3", "MQTT_REQUEST_MEMORY": "4Gi", "MQTT_LIMIT_MEMORY": "8Gi"}

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

func TestLKEMQTTStatefulSetKeepsChecksumOutOfLabels(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	manifest := lkeMQTTStatefulSetManifest(env)

	if !strings.Contains(manifest, `rtk.realtek.com/mqtt-config-checksum: "`) {
		t.Fatalf("expected MQTT config checksum annotation, got:\n%s", manifest)
	}
	if got := strings.Count(manifest, "rtk.realtek.com/stack: video-cloud-staging"); got != 2 {
		t.Fatalf("expected stack label on StatefulSet and pod template, got %d:\n%s", got, manifest)
	}
	if strings.Contains(manifest, "rtk.realtek.com/stack: 12efc48f") {
		t.Fatalf("MQTT checksum must not be rendered as a stack label:\n%s", manifest)
	}
}

func TestLKEEMQXHTTPAuthenticationUsesSupportedFields(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	auth := lkeEMQXHTTPAuthentication(env)

	for _, want := range []string{
		"mechanism=password_based",
		"backend=http",
		"pool_size=32",
		`body={listener="${listener}",username="${username}",password="${password}",clientid="${clientid}"}`,
	} {
		if !strings.Contains(auth, want) {
			t.Fatalf("expected %q in EMQX HTTP auth config, got:\n%s", want, auth)
		}
	}
	if strings.Contains(auth, "pipelining") {
		t.Fatalf("EMQX 5.8 HTTP auth schema rejects pipelining, got:\n%s", auth)
	}
}

func TestLKEVideoCloudAuxiliaryDeploymentManifestHasNoTabOnlyLine(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}

	manifest := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-mqttusage", Binary: "mqttusage"})

	if strings.Contains(manifest, "\n\t") {
		t.Fatalf("auxiliary deployment manifest must not contain tab-indented YAML lines:\n%s", manifest)
	}
	if !strings.Contains(manifest, "emptyDir: {}\n") {
		t.Fatalf("expected logger spool volume in auxiliary deployment manifest:\n%s", manifest)
	}
	for _, want := range []string{
		"name: VIDEO_CLOUD_MQTT_USAGE_LOG_INTERVAL\n              value: \"5s\"",
		"name: VIDEO_CLOUD_MQTT_USAGE_PERSIST_INTERVAL\n              value: \"5s\"",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in mqttusage deployment manifest:\n%s", want, manifest)
		}
	}
	cleanerManifest := lkeVideoCloudAuxiliaryDeploymentManifest(env, lkeVideoCloudAuxiliaryService{Name: "video-cloud-cleaner", Binary: "cleaner"})
	if strings.Contains(cleanerManifest, "VIDEO_CLOUD_MQTT_USAGE_LOG_INTERVAL") || strings.Contains(cleanerManifest, "VIDEO_CLOUD_MQTT_USAGE_PERSIST_INTERVAL") {
		t.Fatalf("mqttusage intervals must not be rendered for unrelated workers:\n%s", cleanerManifest)
	}
}

func TestLKECloudLoggerResourceDefaultsCoverLoadTestEvidence(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging",
	}
	env = withArchitectureWorkloadDefaults(t, env)

	manifest := lkeCloudLoggerDeploymentManifest(env)

	for _, want := range []string{
		`cpu: "100m"`,
		`memory: "1Gi"`,
		`memory: "2Gi"`,
		`name: RTK_CLOUD_LOGGER_LOKI_URL`,
		`value: "http://video-cloud-loki.video-cloud-staging-observability.svc.cluster.local:3100"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("expected %q in cloud-logger manifest, got:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, "_STORE") || strings.Contains(manifest, "store:") {
		t.Fatalf("cloud-logger manifest must not expose a store selector:\n%s", manifest)
	}
}

func TestLKELokiManifestSupportsCloudLoggerPersistence(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging",
	}
	env = withArchitectureWorkloadDefaults(t, env)

	config := lkeLokiConfigManifest(env)
	deployment := lkeLokiDeploymentManifest(env)
	service := lkeLokiServiceManifest(env)
	policy := lkeAllowCloudLoggerLokiNetworkPolicyManifest(env)
	collectorConfig := lkeLogCollectorConfigManifest(env)
	collector := lkeLogCollectorDaemonSetManifest(env)
	collectorPolicy := lkeAllowLogCollectorLokiNetworkPolicyManifest(env)

	for _, tc := range []struct {
		name     string
		manifest string
		want     []string
	}{
		{
			name:     "config",
			manifest: config,
			want: []string{
				"kind: ConfigMap",
				"name: video-cloud-loki-config",
				"auth_enabled: false",
				"schema: v13",
				"retention_period: 24h",
			},
		},
		{
			name:     "deployment",
			manifest: deployment,
			want: []string{
				"kind: Deployment",
				"name: video-cloud-loki",
				"image: grafana/loki:3.5.1",
				"- -config.file=/etc/loki/config.yaml",
				`cpu: "250m"`,
				`memory: "512Mi"`,
				`memory: "2Gi"`,
			},
		},
		{
			name:     "service",
			manifest: service,
			want: []string{
				"kind: Service",
				"name: video-cloud-loki",
				"port: 3100",
			},
		},
		{
			name:     "policy",
			manifest: policy,
			want: []string{
				"kind: NetworkPolicy",
				"name: allow-cloud-logger-loki",
				"kubernetes.io/metadata.name: video-cloud-staging-logger",
				"app.kubernetes.io/name: cloud-logger",
				"port: 3100",
			},
		},
		{
			name:     "collector config",
			manifest: collectorConfig,
			want: []string{
				"kind: ConfigMap",
				"name: video-cloud-log-collector-config",
				"pipeline_stages:",
				"- cri: {}",
				"url: http://video-cloud-loki.video-cloud-staging-observability.svc.cluster.local:3100/loki/api/v1/push",
				"job_name: kubernetes-pod-files",
				"__path__: /var/log/pods/**/*.log",
			},
		},
		{
			name:     "collector daemonset",
			manifest: collector,
			want: []string{
				"kind: DaemonSet",
				"name: video-cloud-log-collector",
				"image: grafana/promtail:3.5.1",
				"mountPath: /var/log/pods",
				"path: /var/log/pods",
			},
		},
		{
			name:     "collector policy",
			manifest: collectorPolicy,
			want: []string{
				"kind: NetworkPolicy",
				"name: allow-log-collector-loki",
				"app.kubernetes.io/name: video-cloud-log-collector",
				"port: 3100",
			},
		},
	} {
		for _, want := range tc.want {
			if !strings.Contains(tc.manifest, want) {
				t.Fatalf("%s manifest missing %q:\n%s", tc.name, want, tc.manifest)
			}
		}
	}
}

func TestLKEDeploymentResourcesCanBeOverriddenFromEnvRoot(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":                      "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":                    "video-cloud-staging.realtekconnect.com",
		"VIDEO_CLOUD_API_REQUEST_CPU":           "250m",
		"VIDEO_CLOUD_API_REQUEST_MEMORY":        "384Mi",
		"VIDEO_CLOUD_API_LIMIT_MEMORY":          "1Gi",
		"ACCOUNT_MANAGER_REQUEST_CPU":           "150m",
		"ACCOUNT_MANAGER_REQUEST_MEMORY":        "192Mi",
		"ACCOUNT_MANAGER_LIMIT_MEMORY":          "768Mi",
		"VIDEO_CLOUD_LOG_INGESTER_REQUEST_CPU":  "200m",
		"VIDEO_CLOUD_LOG_INGESTER_LIMIT_MEMORY": "768Mi",
	}

	api := lkeDeploymentManifest(env, lkeWorkload{
		Key:       "video-cloud",
		Name:      "video-cloud-api",
		Namespace: lkeNamespaceName(env, "video-cloud"),
		Image:     "video-cloud:test",
		Port:      8080,
		Host:      "video-cloud-staging.realtekconnect.com",
	}, nil)
	for _, want := range []string{`cpu: "250m"`, `memory: "384Mi"`, `memory: "1Gi"`} {
		if !strings.Contains(api, want) {
			t.Fatalf("expected %q in video-cloud-api manifest, got:\n%s", want, api)
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
	for _, want := range []string{`cpu: "150m"`, `memory: "192Mi"`, `memory: "768Mi"`} {
		if !strings.Contains(account, want) {
			t.Fatalf("expected %q in account-manager manifest, got:\n%s", want, account)
		}
	}
}

func TestLKEEMQXClusterStatusNodesSeparatesRunningAndStoppedNodes(t *testing.T) {
	status := `Cluster status: #{running_nodes =>
                      ['emqx@10.2.2.153','emqx@10.2.3.163','emqx@10.2.3.33',
                       'emqx@10.2.5.28'],
                  stopped_nodes => ['emqx@10.2.3.162']}`

	running, stopped := lkeEMQXClusterStatusNodes(status)

	if running != 4 {
		t.Fatalf("running = %d, want 4", running)
	}
	if len(stopped) != 1 || stopped[0] != "emqx@10.2.3.162" {
		t.Fatalf("stopped = %#v, want emqx@10.2.3.162", stopped)
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
		"kind: Deployment\nmetadata:\n  name: video-cloud-clipverifier",
		"name: video-cloud-clipverifier\n  namespace: video-cloud-staging-video-cloud\n  labels:",
		"command: [\"/app/clipverifier\"]",
		"containerPort: 19500",
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
		"targets: [\"video-cloud-clipverifier.video-cloud-staging-video-cloud.svc.cluster.local:19500\"]",
		"targets: [\"video-cloud-metricsexporter.video-cloud-staging-video-cloud.svc.cluster.local:19200\"]",
		"targets: [\"factoryenroll.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
	for _, want := range []string{
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-cleaner",
		"ARGS -n video-cloud-staging-video-cloud rollout status deployment/video-cloud-clipverifier",
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

func TestLKEClipVerifierDefaultsToFourReplicas(t *testing.T) {
	t.Setenv("LKE_VIDEO_CLOUD_CLIPVERIFIER_REPLICAS", "")
	manifest := lkeVideoCloudAuxiliaryDeploymentManifest(map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"LKE_VIDEO_CLOUD_IMAGE": "registry.example.test/video-cloud:test",
	}, lkeVideoCloudAuxiliaryService{Name: "video-cloud-clipverifier", Binary: "clipverifier", Port: 19500, PortName: "http"})
	if !strings.Contains(manifest, "spec:\n  replicas: 4\n") {
		t.Fatalf("expected four verifier replicas, got:\n%s", manifest)
	}

	t.Setenv("LKE_VIDEO_CLOUD_CLIPVERIFIER_REPLICAS", "6")
	manifest = lkeVideoCloudAuxiliaryDeploymentManifest(map[string]string{
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"LKE_VIDEO_CLOUD_IMAGE": "registry.example.test/video-cloud:test",
	}, lkeVideoCloudAuxiliaryService{Name: "video-cloud-clipverifier", Binary: "clipverifier", Port: 19500, PortName: "http"})
	if !strings.Contains(manifest, "spec:\n  replicas: 6\n") {
		t.Fatalf("expected configured verifier replicas, got:\n%s", manifest)
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
		"kind: ConfigMap\nmetadata:\n  name: video-cloud-loki-config",
		"kind: Deployment\nmetadata:\n  name: video-cloud-loki",
		"image: grafana/loki:3.5.1",
		"kind: Service\nmetadata:\n  name: video-cloud-loki",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-cloud-logger-loki",
		"kind: DaemonSet\nmetadata:\n  name: video-cloud-log-collector",
		"kind: NetworkPolicy\nmetadata:\n  name: allow-log-collector-loki",
		"ARGS -n video-cloud-staging-observability rollout status deployment/video-cloud-loki",
		"ARGS -n video-cloud-staging-observability rollout status daemonset/video-cloud-log-collector",
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
		"job_name: video-cloud-clip-verifier",
		"targets: [\"video-cloud-clipverifier.video-cloud-staging-video-cloud.svc.cluster.local:19500\"]",
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
	if got, want := strings.Count(manifest, "metrics_path: /metrics/prometheus"), 10; got != want {
		t.Fatalf("metrics_path count = %d, want %d in manifest:\n%s", got, want, manifest)
	}
	if got, want := strings.Count(manifest, "metrics_path: /metrics"), 13; got != want {
		t.Fatalf("all metrics_path count = %d, want %d in manifest:\n%s", got, want, manifest)
	}
}

func TestLKEPrometheusConfigHonorsSelectedWorkloads(t *testing.T) {
	manifest := lkeVideoCloudPrometheusConfigManifest(map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}, provisionOptions{videoOnly: true})

	for _, want := range []string{
		"targets: [\"video-cloud-api.video-cloud-staging-video-cloud.svc.cluster.local:80\"]",
		"targets: [\"video-cloud-clipverifier.video-cloud-staging-video-cloud.svc.cluster.local:19500\"]",
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

func TestRunProvisionLKEDeployUsesExternalCoturnVMConfig(t *testing.T) {
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
		"ARGS -n video-cloud-staging-video-cloud delete deployment/coturn --ignore-not-found=true",
		"ARGS -n video-cloud-staging-video-cloud delete service/coturn --ignore-not-found=true",
		"ARGS -n video-cloud-staging-video-cloud delete configmap/coturn-config --ignore-not-found=true",
		"ARGS -n video-cloud-staging-video-cloud delete secret/coturn-runtime --ignore-not-found=true",
		"name: VIDEO_CLOUD_WEBRTC_STUN_URLS\n              value: \"stun:turn.video-cloud-staging.realtekconnect.com:3478\"",
		"name: VIDEO_CLOUD_WEBRTC_TURN_URLS\n              value: \"turn:turn.video-cloud-staging.realtekconnect.com:3478?transport=udp,turn:turn.video-cloud-staging.realtekconnect.com:3478?transport=tcp\"",
		"name: VIDEO_CLOUD_TURN_REALM\n              value: \"video_cloud\"",
		"name: VIDEO_CLOUD_TURN_SHARED_SECRET\n              valueFrom:\n                secretKeyRef:\n                  name: video-cloud-runtime\n                  key: VIDEO_CLOUD_TURN_SHARED_SECRET",
		"name: VIDEO_CLOUD_TURN_CREDENTIAL_TTL\n              value: \"10m\"",
		"name: VIDEO_CLOUD_WEBRTC_ICE_POLICY\n              value: \"relay\"",
		"name: VIDEO_CLOUD_TURN_REGISTRY_ADDR\n              value: \"http://video-cloud-turnregistry.video-cloud-staging-video-cloud.svc.cluster.local:18190\"",
		"name: VIDEO_CLOUD_TURN_REGISTRY_CLIENT_NODE_ID\n              value: \"video-cloud-api\"",
		"name: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY\n              valueFrom:\n                secretKeyRef:\n                  name: video-cloud-workers-runtime\n                  key: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("expected %q in kubectl manifests, got:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{
		"kind: Deployment\nmetadata:\n  name: coturn",
		"kind: Service\nmetadata:\n  name: coturn",
		"kind: ConfigMap\nmetadata:\n  name: coturn-config",
		"kind: Secret\nmetadata:\n  name: coturn-runtime",
		"rollout status deployment/coturn",
	} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("did not expect K8s coturn runtime %q, got:\n%s", forbidden, log)
		}
	}

	state := readTestFile(t, filepath.Join(envRoot, "state", "video-cloud.state.json"))
	for _, want := range []string{
		`"coturn"`,
		`"public_host": "turn.video-cloud-staging.realtekconnect.com"`,
		`"role": "coturn-vm"`,
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

func TestRunProvisionLKEDeployPreservesOperatorStackOverrides(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
LKE_INGRESS_REPLICAS=1
LKE_MQTT_REPLICAS=1
LKE_ACCOUNT_MANAGER_REPLICAS=1
LKE_VIDEO_CLOUD_REPLICAS=1
LKE_POSTGRES_REQUEST_CPU=500m
LKE_POSTGRES_REQUEST_MEMORY=512Mi
LKE_POSTGRES_LIMIT_MEMORY=1Gi
LKE_MQTT_REQUEST_CPU=250m
LKE_MQTT_REQUEST_MEMORY=512Mi
LKE_MQTT_LIMIT_MEMORY=1Gi
LKE_EDGE_HAPROXY_MAXCONN=200000
LKE_EDGE_HAPROXY_TOKEN=must-not-be-written
`)
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
		"LKE_INGRESS_REPLICAS=1",
		"LKE_MQTT_REPLICAS=1",
		"LKE_ACCOUNT_MANAGER_REPLICAS=1",
		"LKE_VIDEO_CLOUD_REPLICAS=1",
		"LKE_POSTGRES_REQUEST_CPU=500m",
		"LKE_POSTGRES_REQUEST_MEMORY=512Mi",
		"LKE_POSTGRES_LIMIT_MEMORY=1Gi",
		"LKE_MQTT_REQUEST_CPU=250m",
		"LKE_MQTT_REQUEST_MEMORY=512Mi",
		"LKE_MQTT_LIMIT_MEMORY=1Gi",
		"LKE_EDGE_HAPROXY_MAXCONN=200000",
	} {
		if !strings.Contains(stack, want) {
			t.Fatalf("expected %q in stack.env, got:\n%s", want, stack)
		}
	}
	if strings.Contains(stack, "must-not-be-written") || strings.Contains(stack, "LKE_EDGE_HAPROXY_TOKEN") {
		t.Fatalf("stack.env should not persist secret-like override keys, got:\n%s", stack)
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

func TestVideoCloudDockerfileIncludesRuntimeAndClipStorageBinaries(t *testing.T) {
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
		"apt-get install -y --no-install-recommends ca-certificates",
		"go build -trimpath -o /out/api ./cmd/api",
		"go build -trimpath -o /out/certissuer ./cmd/certissuer",
		"go build -trimpath -o /out/factoryenroll ./cmd/factoryenroll",
		"go build -trimpath -o /out/cleaner ./cmd/cleaner",
		"go build -trimpath -o /out/statistics ./cmd/statistics",
		"go build -trimpath -o /out/metricsexporter ./cmd/metricsexporter",
		"go build -trimpath -o /out/turnregistry ./cmd/turnregistry",
		"go build -trimpath -o /out/logingester ./cmd/logingester",
		"go build -trimpath -o /out/mqttusage ./cmd/mqttusage",
		"go build -trimpath -o /out/clipverifier ./cmd/clipverifier",
		"go build -trimpath -o /out/clipuploadpreflight ./cmd/clipuploadpreflight",
		"go build -trimpath -o /out/clipreconcile ./cmd/clipreconcile",
		"COPY --from=builder /out/certissuer /app/certissuer",
		"COPY --from=builder /out/factoryenroll /app/factoryenroll",
		"COPY --from=builder /out/cleaner /app/cleaner",
		"COPY --from=builder /out/statistics /app/statistics",
		"COPY --from=builder /out/metricsexporter /app/metricsexporter",
		"COPY --from=builder /out/turnregistry /app/turnregistry",
		"COPY --from=builder /out/logingester /app/logingester",
		"COPY --from=builder /out/mqttusage /app/mqttusage",
		"COPY --from=builder /out/clipverifier /app/clipverifier",
		"COPY --from=builder /out/clipuploadpreflight /app/clipuploadpreflight",
		"COPY --from=builder /out/clipreconcile /app/clipreconcile",
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
		"go build -trimpath -o /out/rtk-account-manager-user-cache ./cmd/user-cache",
		"COPY --from=builder /out/rtk-account-manager-migrate /app/rtk-account-manager-migrate",
		"COPY --from=builder /out/rtk-account-manager-user-cache /app/rtk-account-manager-user-cache",
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

func TestRunRemoveAllLKEDeletesNamespaces(t *testing.T) {
	_, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	envValues, err := envroot.Load(envRoot, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := runRemoveAllLKE(envRoot, envValues.Values, true); err != nil {
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

func TestRunRemoveAllLKEDoesNotCreateMissingCluster(t *testing.T) {
	_, envRoot := makeLKETestEnv(t)
	curlLog := fakeLinodeCurl(t, map[string]string{
		"/lke/clusters?page_size=500": `{"data":[]}`,
	})
	fakeKubectlWithoutCurrentContext(t)
	t.Setenv("LINODE_TOKEN", "test-token")
	envValues, err := envroot.Load(envRoot, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := runRemoveAllLKE(envRoot, envValues.Values, true); err != nil {
		t.Fatal(err)
	}

	curlCalls := readTestFile(t, curlLog)
	if strings.Contains(curlCalls, "POST /lke/clusters") {
		t.Fatalf("remove should not create LKE clusters, got:\n%s", curlCalls)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "adapters", "lke", "state.env")); !os.IsNotExist(err) {
		t.Fatalf("remove should not create LKE state when cluster is missing, stat err=%v", err)
	}
}

func TestRunRemoveAllVMRetired(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)

	err := runRemoveAllVM([]string{"--workspace", workspace, "--env-root", envRoot, "--yes"})
	if err == nil {
		t.Fatal("expected retired remove-all-vm error")
	}
	if !strings.Contains(err.Error(), "remove-all-vm is retired") {
		t.Fatalf("unexpected error: %v", err)
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

func TestRunMQTTTestForLKERefusesPortForwardWhenDisabled(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD", "0")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
`)

	err := runMQTTTest([]string{"--workspace", workspace, "--env-root", envRoot, "--brandname", "RTK", "--duration-seconds", "1"})
	if err == nil || !strings.Contains(err.Error(), "external endpoints are required when CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0") {
		t.Fatalf("runMQTTTest error = %v, want disabled port-forward endpoint error", err)
	}
}

func TestRunMQTTTestPassesLoadModelToChildScript(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", "0s")
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
		"--mqtt-probe",
		"--load-model", "home-100k-sustained",
	}); err != nil {
		t.Fatal(err)
	}

	got := readTestFile(t, childLog)
	if !strings.Contains(got, "--mqtt-probe true") {
		t.Fatalf("child args missing mqtt probe bool:\n%s", got)
	}
	if !strings.Contains(got, "--load-model home-100k-sustained") {
		t.Fatalf("child args missing load model:\n%s", got)
	}
}

func TestRunMQTTTestPassesTargetWindowSustainedFlagsToChildScript(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", "0s")
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
		"--stage-names", "target",
		"--stage-connected-devices", "10000",
		"--stage-durations-seconds", "300",
		"--stage-min-commands", "500",
		"--device-traffic-profile", "home-diverse-v1",
		"--command-concurrency", "100",
		"--shadow-command-timeout", "30s",
		"--device-token-request-timeout", "10s",
		"--device-token-request-retries", "1",
		"--runtime-logs=false",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := readTestFile(t, childLog)
	for _, want := range []string{
		"--stage-names target",
		"--stage-connected-devices 10000",
		"--stage-durations-seconds 300",
		"--stage-min-commands 500",
		"--device-traffic-profile home-diverse-v1",
		"--command-concurrency 100",
		"--shadow-command-timeout 30s",
		"--device-token-request-timeout 10s",
		"--device-token-request-retries 1",
		"--runtime-logs=false",
		"--load-model home-100k-sustained",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("child args missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--stage-ramp-seconds") {
		t.Fatalf("child args still include retired stage ramp flag:\n%s", got)
	}
}

func TestRunMQTTTestOmitsEmptyStageUsageWindowsForTargetRuns(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeKubectl(t)
	t.Setenv("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", "0s")
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
		"--stage-names", "target",
		"--stage-connected-devices", "20000",
		"--stage-durations-seconds", "1845",
		"--stage-min-commands", "1000",
		"--device-traffic-profile", "home-diverse-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := readTestFile(t, childLog)
	if strings.Contains(got, "--stage-usage-windows") {
		t.Fatalf("child args should omit empty stage usage windows for target runs:\n%s", got)
	}
}

func TestStartK8SE2EPortForwardsStartsAllBeforeWaiting(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectlForK8SE2EPortForwards(t)
	writeTestFile(t, filepath.Join(envRoot, "state", "kubeconfig.yaml"), "apiVersion: v1\n")
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
	writeTestFile(t, filepath.Join(envRoot, "state", "kubeconfig.yaml"), "apiVersion: v1\n")
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

func TestRunStagingE2EDataSetupForLKESupportsMultipleFactoryPortForwards(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	kubectlLog := fakeKubectlForK8SE2EPortForwards(t)
	commandLog := filepath.Join(t.TempDir(), "commands.log")
	writeTestFile(t, filepath.Join(envRoot, "state", "kubeconfig.yaml"), "apiVersion: v1\n")
	factoryPort1 := freeTCPPort(t)
	factoryPort2 := freeTCPPort(t)
	t.Setenv("CLOUD_PROVIDER", "lke")
	t.Setenv("CLOUD_STAGING_E2E_ACCOUNT_MANAGER_PORT", freeTCPPort(t))
	t.Setenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_PORT", freeTCPPort(t))
	t.Setenv("CLOUD_STAGING_E2E_FACTORY_ENROLL_PORTS", factoryPort1+","+factoryPort2)
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
	if got := strings.Count(kubectlCalls, "PF_START svc/factoryenroll"); got != 2 {
		t.Fatalf("expected two factoryenroll port-forwards, got %d:\n%s", got, kubectlCalls)
	}
	expectedURL := "generate-devices FACTORY_ENROLL_URL=http://127.0.0.1:" + factoryPort1 + ",http://127.0.0.1:" + factoryPort2
	commands := readTestFile(t, commandLog)
	if !strings.Contains(commands, expectedURL) {
		t.Fatalf("expected generate-devices to receive multiple FACTORY_ENROLL_URL endpoints %q, got:\n%s", expectedURL, commands)
	}
}

func seedStagingE2ETestData(t *testing.T, envRoot string, emails []string, assignments []bindAssignment) {
	t.Helper()
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	users := make([]map[string]any, 0, len(emails))
	for _, email := range emails {
		users = append(users, map[string]any{"email": email, "password": "password"})
	}
	if err := store.ReplaceUsers("RTK", "brand-1", "rtk", "member", users); err != nil {
		t.Fatal(err)
	}
	devices := make([]generatedDevice, 0, len(assignments))
	credentials := map[string]testDataDeviceCredential{}
	for _, assignment := range assignments {
		deviceType := assignment.DeviceType
		category := "mqtt_device"
		if contains(assignment.ServiceOptions, "video_streaming") || contains(assignment.ServiceOptions, "video_storage") {
			category = "ip_camera"
		}
		devices = append(devices, generatedDevice{
			DeviceID:       assignment.DeviceID,
			DeviceType:     deviceType,
			DisplayName:    assignment.DeviceID,
			MQTTCapability: "shadow",
			ServiceOptions: assignment.ServiceOptions,
			Model:          "test-model",
		})
		credentials[assignment.DeviceID] = testDataDeviceCredential{DeviceID: assignment.DeviceID, MetadataJSON: mustMarshalJSONString(map[string]any{"device_id": assignment.DeviceID, "device_type": deviceType, "category": category})}
	}
	if err := store.ReplaceDevices("RTK", "test-run", devices, credentials); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceBindings("RTK", "brand-1", "rtk", "test-run", assignments); err != nil {
		t.Fatal(err)
	}
}

func TestRunStagingE2EDataSetupDefaultsToResumeCompleteArtifacts(t *testing.T) {
	t.Setenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD", "0")
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\n")
	seedStagingE2ETestData(t, envRoot, []string{"rtk+001@users.local", "rtk+002@users.local"}, []bindAssignment{
		{AssignmentIndex: 0, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0001", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming", "video_storage"}},
		{AssignmentIndex: 1, AssignedEmail: "rtk+002@users.local", DeviceID: "load-device-0002", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming", "video_storage"}},
		{AssignmentIndex: 2, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0003", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 3, AssignedEmail: "rtk+002@users.local", DeviceID: "load-device-0004", DeviceType: "air_conditioner", ServiceOptions: []string{"mqtt"}},
	})
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
	t.Setenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD", "0")
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\n")
	seedStagingE2ETestData(t, envRoot, []string{"rtk+001@users.local", "rtk+002@users.local"}, []bindAssignment{
		{AssignmentIndex: 0, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0001", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming", "video_storage"}},
		{AssignmentIndex: 1, AssignedEmail: "rtk+002@users.local", DeviceID: "load-device-0002", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming", "video_storage"}},
		{AssignmentIndex: 2, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0003", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 3, AssignedEmail: "rtk+002@users.local", DeviceID: "load-device-0004", DeviceType: "air_conditioner", ServiceOptions: []string{"mqtt"}},
	})
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
	t.Setenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD", "0")
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\n")
	seedStagingE2ETestData(t, envRoot, []string{"rtk+001@users.local", "rtk+002@users.local"}, []bindAssignment{
		{AssignmentIndex: 0, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0001", DeviceType: "camera", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 1, AssignedEmail: "rtk+002@users.local", DeviceID: "load-device-0002", DeviceType: "camera", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 2, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0003", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 3, AssignedEmail: "rtk+002@users.local", DeviceID: "load-device-0004", DeviceType: "smart_meter", ServiceOptions: []string{"mqtt"}},
	})
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
	t.Setenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD", "0")
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=video-cloud-staging\n")
	seedStagingE2ETestData(t, envRoot, []string{"rtk+001@users.local", "rtk+002@users.local"}, []bindAssignment{
		{AssignmentIndex: 0, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0001", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 1, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0002", DeviceType: "light", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 2, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0003", DeviceType: "smart_meter", ServiceOptions: []string{"mqtt"}},
		{AssignmentIndex: 3, AssignedEmail: "rtk+001@users.local", DeviceID: "load-device-0004", DeviceType: "smart_meter", ServiceOptions: []string{"mqtt"}},
	})
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
	devicesDir := filepath.Join(dir, "devices")
	writeTestFile(t, usersPath, `{"users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}`)
	writeTestFile(t, filepath.Join(devicesDir, "manifests", "devices.json"), `[
{"device_id":"load-device-0001","device_type":"light"},
{"device_id":"load-device-0002","device_type":"light"},
{"device_id":"load-device-0003","device_type":"smart_meter"},
{"device_id":"load-device-0004","device_type":"smart_meter"}
]`)
	writeTestFile(t, bindPath, `{"assignments":[
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"light","service_options":["mqtt"],"status":"already_bound"},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0002","device_type":"light","service_options":["mqtt"],"status":"already_bound"},
{"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"smart_meter","service_options":["mqtt"],"status":"already_bound"},
{"assigned_email":"rtk+002@users.local","device_id":"load-device-0004","device_type":"smart_meter","service_options":["mqtt"],"status":"already_bound"}
]}`)

	if bindArtifactMatchesSetup(bindPath, usersPath, devicesDir, 2, 4, "light=2,smart_meter=2") {
		t.Fatal("all already_bound artifact without operation evidence must not be resumed")
	}
}

func TestBindArtifactDoesNotResumeWhenDeviceIDsDoNotMatchManifest(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.json")
	bindPath := filepath.Join(dir, "bind.json")
	devicesDir := filepath.Join(dir, "devices")
	writeTestFile(t, usersPath, `{"users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}`)
	writeTestFile(t, filepath.Join(devicesDir, "manifests", "devices.json"), `[
{"device_id":"new-run-device-0001","device_type":"light"},
{"device_id":"new-run-device-0002","device_type":"light"},
{"device_id":"new-run-device-0003","device_type":"smart_meter"},
{"device_id":"new-run-device-0004","device_type":"smart_meter"}
]`)
	writeTestFile(t, bindPath, `{"assignments":[
{"assigned_email":"rtk+001@users.local","device_id":"old-run-device-0001","device_type":"light","service_options":["mqtt"],"status":"provisioned","operation_id":"op-1"},
{"assigned_email":"rtk+002@users.local","device_id":"old-run-device-0002","device_type":"light","service_options":["mqtt"],"status":"provisioned","operation_id":"op-2"},
{"assigned_email":"rtk+001@users.local","device_id":"old-run-device-0003","device_type":"smart_meter","service_options":["mqtt"],"status":"provisioned","operation_id":"op-3"},
{"assigned_email":"rtk+002@users.local","device_id":"old-run-device-0004","device_type":"smart_meter","service_options":["mqtt"],"status":"provisioned","operation_id":"op-4"}
]}`)

	if bindArtifactMatchesSetup(bindPath, usersPath, devicesDir, 2, 4, "light=2,smart_meter=2") {
		t.Fatal("bind artifact with old device IDs must not be resumed for the current manifest")
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
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LKE_EDGE_HAPROXY_PUBLIC_IP", "198.51.100.10")
	t.Setenv("LKE_EDGE_HAPROXY_PRIVATE_IP", "10.2.1.5")
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
	t.Setenv("RTK_CLOUD_KUBE_API_READY_POLL", "1ms")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_STABLE_CHECKS", "1")
	script := `#!/usr/bin/env bash
set -euo pipefail
args=()
for arg in "$@"; do
  if [[ "$arg" != --request-timeout=* ]]; then
    args+=("$arg")
  fi
done
set -- "${args[@]}"
if [[ "${1:-}" == "config" && "${2:-}" == "current-context" ]]; then
  printf 'test-context\n'
  exit 0
fi
if [[ "$*" == *"get --raw=/readyz"* ]]; then
  printf 'ok\n'
  exit 0
fi
if [[ "$*" == *"get nodes -o name"* ]]; then
  printf 'node/lke-test-1\n'
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
if [[ "$*" == *"get statefulset/postgresql -o jsonpath={.status.currentRevision} {.status.updateRevision}"* && -n "${FAKE_POSTGRES_STATEFULSET_REVISIONS:-}" ]]; then
  printf '%s' "$FAKE_POSTGRES_STATEFULSET_REVISIONS"
  exit 0
fi
if [[ "$*" == *"get service cloud-logger -o name"* && "${FAKE_CLOUD_LOGGER_SERVICE:-}" == "1" ]]; then
  printf 'service/cloud-logger\n'
  exit 0
fi
if [[ "$*" == *"get nodes -o json"* ]]; then
  printf '{"items":[{"metadata":{"name":"lke-node-a"},"status":{"addresses":[{"type":"InternalIP","address":"10.2.1.10"},{"type":"Hostname","address":"lke-node-1"}]}},{"metadata":{"name":"lke-node-b"},"status":{"addresses":[{"type":"InternalIP","address":"10.2.1.11"}]}},{"metadata":{"name":"lke-node-c"},"status":{"addresses":[{"type":"InternalIP","address":"10.2.1.12"}]}}]}\n'
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
if [[ "$*" == *"get pods -l app.kubernetes.io/name=mqtt -o json"* ]]; then
  printf '{"items":[{"metadata":{"name":"mqtt-0"},"spec":{"nodeName":"lke-node-a"},"status":{"phase":"Running"}},{"metadata":{"name":"mqtt-1"},"spec":{"nodeName":"lke-node-c"},"status":{"phase":"Running"}},{"metadata":{"name":"mqtt-pending"},"spec":{"nodeName":"lke-node-b"},"status":{"phase":"Pending"}}]}\n'
  exit 0
fi
if [[ "$*" == *"exec mqtt-0 -- emqx ctl cluster status"* ]]; then
  line='ARGS'
  for arg in "$@"; do
    line="$line $arg"
  done
  printf '%s\n' "$line" >> "` + logPath + `"
  printf 'Cluster status: #{running_nodes => [emqx@mqtt-0.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local,emqx@mqtt-1.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local,emqx@mqtt-2.mqtt-headless.video-cloud-staging-video-cloud.svc.cluster.local]}\n'
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
  if [[ "$*" == *"apply -f"* ]]; then
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
	t.Setenv("RTK_CLOUD_KUBE_API_READY_POLL", "1ms")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_STABLE_CHECKS", "1")
	script := `#!/usr/bin/env bash
set -euo pipefail
args=()
for arg in "$@"; do
  if [[ "$arg" != --request-timeout=* ]]; then
    args+=("$arg")
  fi
done
set -- "${args[@]}"
if [[ "${1:-}" == "config" && "${2:-}" == "current-context" ]]; then
  exit 1
fi
if [[ "$*" == *"get --raw=/readyz"* ]]; then
  printf 'ok\n'
  exit 0
fi
if [[ "$*" == *"get nodes -o name"* ]]; then
  printf 'node/lke-test-1\n'
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
if [[ "$*" == *"get nodes -o json"* ]]; then
  printf '{"items":[{"status":{"addresses":[{"type":"InternalIP","address":"10.2.1.10"}]}}]}\n'
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
args=()
for arg in "$@"; do
  if [[ "$arg" != --request-timeout=* ]]; then
    args+=("$arg")
  fi
done
set -- "${args[@]}"
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

func withArchitectureWorkloadDefaults(t *testing.T, env map[string]string) map[string]string {
	t.Helper()
	defaults, err := readStrictEnv(filepath.Join("..", "..", "..", "cloud_deploy", "architectures", "kubernetes", "workloads.env"))
	if err != nil {
		t.Fatal(err)
	}
	return appendMap(defaults, env)
}

func fakeGoDaddyAPI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "go.log")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
			return
		}
		if r.Method == http.MethodPut && len(parts) >= 6 {
			var payload []struct {
				Data string `json:"data"`
				TTL  int    `json:"ttl"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			for _, record := range payload {
				f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				fmt.Fprintf(f, "ARGS records upsert --name %s --data %s --ttl %d\n", parts[len(parts)-1], record.Data, record.TTL)
				_ = f.Close()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
	return logPath
}

func fakeDNSCommandsWithGoDelay(t *testing.T, ip, delay string, turnIP ...string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "dns-events.log")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, "[]")
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodPut && len(parts) >= 6 {
			f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			fmt.Fprintf(f, "GO ARGS records upsert --name %s\n", parts[len(parts)-1])
			_ = f.Close()
			if wait, err := time.ParseDuration(delay + "s"); err == nil {
				time.Sleep(wait)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
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
	turnRecordIP := ""
	if len(turnIP) > 0 {
		turnRecordIP = turnIP[0]
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
if [[ -n "` + turnRecordIP + `" && "$*" == *"turn.video-cloud-staging.realtekconnect.com"* ]]; then
  printf '` + turnRecordIP + `\n'
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
if [[ "$*" == *"turn.video-cloud-staging.realtekconnect.com"* ]]; then
  printf '198.51.100.20\n'
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
		if body == "__ERROR_404__" {
			script += path + `)
    printf 'curl: (22) The requested URL returned error: 404\n' >&2
    exit 22
  ;;
`
			continue
		}
		if body == "__ERROR_400_ACCOUNT_LIMIT__" {
			script += path + `)
    printf '{"errors": [{"reason": "You'\''ve reached a limit for the number of active services on your account. Please contact Support to request an increase and provide the total number of services you may need."}]}'
    printf 'curl: (22) The requested URL returned error: 400\n' >&2
    exit 22
  ;;
`
			continue
		}
		script += path + `)
  cat <<'JSON'
` + body + `
JSON
  ;;
`
	}
	script += `/nodebalancers?page_size=500)
  printf '{"data":[]}'
  ;;
*)
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
