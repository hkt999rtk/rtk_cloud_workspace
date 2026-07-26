package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCloudAdminImageDeployScopesExistingDeployment(t *testing.T) {
	t.Setenv("RTK_CLOUD_KUBECONFIG", "")
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
LKE_CLOUD_ADMIN_IMAGE=ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-0123456789ab
`)
	logPath := fakeKubectlForCloudAdminImageDeploy(t)
	kubeconfig := filepath.Join(workspace, "kubeconfig")
	writeTestFile(t, kubeconfig, "test")

	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--kubeconfig", kubeconfig,
		"--confirm", "video-cloud-staging",
	}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"get deployment/cloud-admin -o name",
		"get deployment cloud-admin -o json",
		`"image":"ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-0123456789ab"`,
		"rollout status deployment/cloud-admin --timeout 10m",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("kubectl log missing %q:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{"account-manager", "video-cloud-api", "openbao"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("scoped Cloud Admin deploy touched %q:\n%s", forbidden, log)
		}
	}
}

func fakeKubectlForCloudAdminImageDeploy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	kubectl := filepath.Join(dir, "kubectl")
	script := `#!/usr/bin/env bash
set -euo pipefail
line='ARGS'
for arg in "$@"; do
  line="$line $arg"
done
printf '%s\n' "$line" >> "` + logPath + `"
if [[ "$*" == *"get --raw=/readyz"* ]]; then
  printf 'ok\n'
  exit 0
fi
if [[ "$*" == *"get nodes -o name"* ]]; then
  printf 'node/test\n'
  exit 0
fi
if [[ "$*" == *"get deployment/cloud-admin -o name"* ]]; then
  printf 'deployment.apps/cloud-admin\n'
  exit 0
fi
if [[ "$*" == *"get deployment cloud-admin -o json"* ]]; then
  printf '{"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"app","image":"old"}]}}}}\n'
  exit 0
fi
if [[ "$*" == *"replace -f -"* ]]; then
  cat >> "` + logPath + `"
  printf '\n---\n' >> "` + logPath + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_POLL", "1ms")
	t.Setenv("RTK_CLOUD_KUBE_API_READY_STABLE_CHECKS", "1")
	return logPath
}

func TestUpdateDeploymentContainerImage(t *testing.T) {
	deployment := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "app", "image": "old"},
						map[string]any{"name": "sidecar", "image": "keep"},
					},
				},
			},
		},
	}
	const image = "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-0123456789ab"
	if err := updateDeploymentContainerImage(deployment, "app", image); err != nil {
		t.Fatal(err)
	}
	containers := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	if got := containers[0].(map[string]any)["image"]; got != image {
		t.Fatalf("app image = %v", got)
	}
	if got := containers[1].(map[string]any)["image"]; got != "keep" {
		t.Fatalf("sidecar image changed to %v", got)
	}
	if err := updateDeploymentContainerImage(deployment, "missing", image); err == nil {
		t.Fatal("missing container accepted")
	}
	for name, candidate := range map[string]map[string]any{
		"spec":       {},
		"template":   {"spec": map[string]any{}},
		"pod spec":   {"spec": map[string]any{"template": map[string]any{}}},
		"containers": {"spec": map[string]any{"template": map[string]any{"spec": map[string]any{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := updateDeploymentContainerImage(candidate, "app", image); err == nil {
				t.Fatal("malformed deployment accepted")
			}
		})
	}
}

func TestCloudAdminCommitImagePattern(t *testing.T) {
	for _, image := range []string{
		"cloud-admin:latest",
		"ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:main",
		"ghcr.io/other/rtk_cloud_admin/cloud-admin:sha-0123456789ab",
	} {
		if cloudAdminCommitImagePattern.MatchString(image) {
			t.Fatalf("unsafe image accepted: %s", image)
		}
	}
	if image := "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-0123456789ab"; !cloudAdminCommitImagePattern.MatchString(image) {
		t.Fatalf("commit image rejected: %s", image)
	}
}

func TestRunCloudAdminImageDeployRejectsInvalidInputs(t *testing.T) {
	if err := runCloudAdminImageDeploy([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runCloudAdminImageDeploy(nil); err == nil {
		t.Fatal("missing env root accepted")
	}
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
LKE_CLOUD_ADMIN_IMAGE=cloud-admin:latest
`)
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "wrong",
	}); err == nil {
		t.Fatal("incorrect stack confirmation accepted")
	}
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "video-cloud-staging",
	}); err == nil {
		t.Fatal("non-commit image accepted")
	}
}
