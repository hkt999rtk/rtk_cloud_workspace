package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cloudAdminOldImage = "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-aaaaaaaaaaaa"
	cloudAdminNewImage = "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-bbbbbbbbbbbb"
)

func selectedCloudAdminImageFixture(t *testing.T, image string) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
LKE_CLOUD_ADMIN_IMAGE=ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-aaaaaaaaaaaa
`)
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(workspace, "config"))
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", "")
	store, err := newSecretStore("", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/LKE_CLOUD_ADMIN_IMAGE", []byte(image), true); err != nil {
		t.Fatal(err)
	}
	return workspace, envRoot
}

func cloudAdminDeploymentFixture() map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "cloud-admin", "resourceVersion": "17"},
		"spec": map[string]any{
			"replicas": float64(1),
			"strategy": map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"maxSurge": "25%", "maxUnavailable": "25%"}},
			"template": map[string]any{"spec": map[string]any{"containers": []any{
				map[string]any{"name": "app", "image": cloudAdminOldImage, "env": []any{map[string]any{"name": "UNCHANGED", "value": "keep"}}},
				map[string]any{"name": "sidecar", "image": "keep"},
			}}},
		},
	}
}

func TestRunCloudAdminImageDeployScopesExistingDeployment(t *testing.T) {
	t.Setenv("RTK_CLOUD_KUBECONFIG", "")
	workspace, envRoot := selectedCloudAdminImageFixture(t, cloudAdminNewImage)
	logPath := fakeKubectlForCloudAdminImageDeploy(t, cloudAdminDeploymentFixture())
	kubeconfig := filepath.Join(workspace, "kubeconfig")
	writeTestFile(t, kubeconfig, "test")

	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--kubeconfig", kubeconfig,
		"--confirm", "video-cloud-staging",
		"--expected-old-image", cloudAdminOldImage,
	}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"get deployment/cloud-admin -o name",
		"get deployment cloud-admin -o json",
		`"image":"` + cloudAdminNewImage + `"`,
		`"resourceVersion":"17"`,
		`"type":"RollingUpdate"`,
		`"image":"keep","name":"sidecar"`,
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

func TestRunCloudAdminImageDeployAcceptsExactOldDigest(t *testing.T) {
	workspace, envRoot := selectedCloudAdminImageFixture(t, cloudAdminNewImage)
	oldDigest := "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin@sha256:" + strings.Repeat("a", 64)
	deployment := cloudAdminDeploymentFixture()
	containers := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	containers[0].(map[string]any)["image"] = oldDigest
	logPath := fakeKubectlForCloudAdminImageDeploy(t, deployment)
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "video-cloud-staging", "--expected-old-image", oldDigest,
	}); err != nil {
		t.Fatal(err)
	}
	log := readTestFile(t, logPath)
	if !strings.Contains(log, `"image":"`+cloudAdminNewImage+`"`) || strings.Count(log, "replace -f -") != 1 {
		t.Fatalf("exact digest old image did not receive one guarded replacement: %s", log)
	}
}

func fakeKubectlForCloudAdminImageDeploy(t *testing.T, deployment map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "kubectl.log")
	fixturePath := filepath.Join(dir, "deployment.json")
	body, err := json.Marshal(deployment)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, fixturePath, string(body))
	t.Setenv("FAKE_CLOUD_ADMIN_DEPLOYMENT", fixturePath)
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
  cat "$FAKE_CLOUD_ADMIN_DEPLOYMENT"
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

func TestUpdateCloudAdminDeploymentImage(t *testing.T) {
	deployment := cloudAdminDeploymentFixture()
	if already, err := updateCloudAdminDeploymentImage(deployment, cloudAdminOldImage, cloudAdminNewImage); err != nil || already {
		t.Fatal(err)
	}
	containers := deployment["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	if got := containers[0].(map[string]any)["image"]; got != cloudAdminNewImage {
		t.Fatalf("app image = %v", got)
	}
	if got := containers[0].(map[string]any)["env"].([]any)[0].(map[string]any)["value"]; got != "keep" {
		t.Fatalf("app environment changed to %v", got)
	}
	if got := containers[1].(map[string]any)["image"]; got != "keep" {
		t.Fatalf("sidecar image changed to %v", got)
	}
	if got := deployment["metadata"].(map[string]any)["resourceVersion"]; got != "17" {
		t.Fatalf("resourceVersion changed to %v", got)
	}
	if already, err := updateCloudAdminDeploymentImage(cloudAdminDeploymentFixture(), cloudAdminOldImage, cloudAdminOldImage); err != nil || !already {
		t.Fatalf("same image should be a safe no-op: already=%t err=%v", already, err)
	}
	for name, candidate := range map[string]map[string]any{
		"spec":       {},
		"template":   {"spec": map[string]any{}},
		"pod spec":   {"spec": map[string]any{"template": map[string]any{}}},
		"containers": {"spec": map[string]any{"template": map[string]any{"spec": map[string]any{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			candidate["metadata"] = map[string]any{"resourceVersion": "17"}
			if spec, ok := candidate["spec"].(map[string]any); ok {
				spec["replicas"] = float64(1)
			}
			if _, err := updateCloudAdminDeploymentImage(candidate, cloudAdminOldImage, cloudAdminNewImage); err == nil {
				t.Fatal("malformed deployment accepted")
			}
		})
	}
}

func TestRunCloudAdminImageDeployRejectsUnsafeCurrentDeploymentBeforeReplace(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"old image": func(d map[string]any) {
			containers := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
			containers[0].(map[string]any)["image"] = cloudAdminNewImage
		},
		"resource version": func(d map[string]any) { delete(d["metadata"].(map[string]any), "resourceVersion") },
		"two replicas":     func(d map[string]any) { d["spec"].(map[string]any)["replicas"] = float64(2) },
	} {
		t.Run(name, func(t *testing.T) {
			workspace, envRoot := selectedCloudAdminImageFixture(t, cloudAdminNewImage)
			deployment := cloudAdminDeploymentFixture()
			change(deployment)
			logPath := fakeKubectlForCloudAdminImageDeploy(t, deployment)
			if err := runCloudAdminImageDeploy([]string{
				"--workspace", workspace, "--env-root", envRoot, "--confirm", "video-cloud-staging",
				"--expected-old-image", cloudAdminOldImage,
			}); err == nil {
				t.Fatal("unsafe current Deployment accepted")
			}
			if strings.Contains(readTestFile(t, logPath), " replace -f -") {
				t.Fatal("unsafe current Deployment was replaced")
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
	oldDigest := "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin@sha256:" + strings.Repeat("a", 64)
	if !cloudAdminCurrentImagePattern.MatchString(oldDigest) || cloudAdminCommitImagePattern.MatchString(oldDigest) {
		t.Fatal("exact old digest must be valid only as a current image")
	}
	for _, invalid := range []string{
		"ghcr.io/other/rtk_cloud_admin/cloud-admin@sha256:" + strings.Repeat("a", 64),
		"ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin@sha256:" + strings.Repeat("a", 63),
	} {
		if cloudAdminCurrentImagePattern.MatchString(invalid) {
			t.Fatalf("invalid current image accepted: %s", invalid)
		}
	}
}

func TestRunCloudAdminImageDeployRejectsInvalidInputs(t *testing.T) {
	if err := runCloudAdminImageDeploy([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runCloudAdminImageDeploy(nil); err == nil {
		t.Fatal("missing env root accepted")
	}
	workspace, envRoot := selectedCloudAdminImageFixture(t, "cloud-admin:latest")
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "wrong",
		"--expected-old-image", cloudAdminOldImage,
	}); err == nil {
		t.Fatal("incorrect stack confirmation accepted")
	}
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "video-cloud-staging", "--expected-old-image", cloudAdminOldImage,
	}); err == nil {
		t.Fatal("non-commit image accepted")
	}
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "video-cloud-staging",
	}); err == nil {
		t.Fatal("missing expected old image accepted")
	}
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "video-cloud-staging", "--expected-old-image", "old",
	}); err == nil {
		t.Fatal("unqualified expected old image accepted")
	}
}

func TestRunCloudAdminImageDeployRefusesProcessImageOverride(t *testing.T) {
	workspace, envRoot := selectedCloudAdminImageFixture(t, cloudAdminNewImage)
	t.Setenv("LKE_CLOUD_ADMIN_IMAGE", cloudAdminOldImage)
	if err := runCloudAdminImageDeploy([]string{
		"--workspace", workspace, "--env-root", envRoot, "--confirm", "video-cloud-staging", "--expected-old-image", cloudAdminOldImage,
	}); err == nil || !strings.Contains(err.Error(), "differs from the selected operator pin") {
		t.Fatalf("stale process image override accepted: %v", err)
	}
}
