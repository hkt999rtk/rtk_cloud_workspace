package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
