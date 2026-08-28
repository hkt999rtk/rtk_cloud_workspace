package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAccountManagerSendMailHTTPDeployScopesExistingWorkloads(t *testing.T) {
	t.Setenv("RTK_CLOUD_KUBECONFIG", "")
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
LKE_ACCOUNT_MANAGER_IMAGE=example.test/account-manager:sha-abc
AUTH_TOKEN_DELIVERY=sendmail_http
AUTH_TOKEN_BASE_URL=https://admin.video-cloud-staging.realtekconnect.com
SENDMAIL_HTTP_BASE_URL=https://sm.realtekconnect.com
SENDMAIL_HTTP_BEARER_TOKEN=opaque-token
SENDMAIL_HTTP_TIMEOUT=15s
`)
	logPath := fakeKubectlForAccountManagerEmailDeploy(t)
	kubeconfig := filepath.Join(workspace, "kubeconfig")
	writeTestFile(t, kubeconfig, "test")

	if err := runAccountManagerEmailDeploy([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--kubeconfig", kubeconfig,
		"--confirm", "video-cloud-staging",
	}); err != nil {
		t.Fatal(err)
	}

	log := readTestFile(t, logPath)
	for _, want := range []string{
		"get deployment/account-manager -o name",
		"get secret/account-manager-runtime -o name",
		"get secret/account-manager-certissuer-client -o name",
		"kind: Job",
		"name: account-manager-migrate",
		"kind: Deployment",
		"name: account-manager-email-worker",
		"rollout status deployment/account-manager --timeout 10m",
		"rollout status deployment/account-manager-email-worker --timeout 10m",
		`"image":"example.test/account-manager:sha-abc"`,
		`"rtk.realtek.com/runtime-checksum"`,
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("kubectl log missing %q:\n%s", want, log)
		}
	}
	for _, forbidden := range []string{
		"video-cloud-api",
		"cloud-admin",
		"openbao",
	} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("scoped Send Mail deploy touched %q:\n%s", forbidden, log)
		}
	}
}

func TestRunAccountManagerEmailDeployRejectsInvalidInputs(t *testing.T) {
	if err := runAccountManagerEmailDeploy([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runAccountManagerEmailDeploy(nil); err == nil {
		t.Fatal("missing env root accepted")
	}

	workspace := t.TempDir()
	if err := runAccountManagerEmailDeploy([]string{
		"--workspace", workspace,
		"--env-root", filepath.Join(workspace, "missing"),
		"--confirm", "video-cloud-staging",
	}); err == nil {
		t.Fatal("missing environment accepted")
	}

	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
`)
	if err := runAccountManagerEmailDeploy([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--confirm", "wrong-stack",
	}); err == nil {
		t.Fatal("incorrect stack confirmation accepted")
	}
}

func fakeKubectlForAccountManagerEmailDeploy(t *testing.T) string {
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
if [[ "$*" == *"get deployment/account-manager -o name"* ]]; then
  printf 'deployment.apps/account-manager\n'
  exit 0
fi
if [[ "$*" == *"get secret/account-manager-runtime -o name"* ]]; then
  printf 'secret/account-manager-runtime\n'
  exit 0
fi
if [[ "$*" == *"get secret/account-manager-certissuer-client -o name"* ]]; then
  printf 'secret/account-manager-certissuer-client\n'
  exit 0
fi
if [[ "$*" == *"get secret account-manager-runtime -o json"* ]]; then
  printf '{"data":{"DATABASE_URL":"cHJlc2VydmU="}}\n'
  exit 0
fi
if [[ "$*" == *"get deployment account-manager -o json"* ]]; then
  printf '{"spec":{"replicas":1,"template":{"metadata":{"annotations":{"keep":"yes"}},"spec":{"containers":[{"name":"app","image":"old","env":[{"name":"KEEP","value":"yes"}]}]}}}}\n'
  exit 0
fi
if [[ "$*" == *"apply -f -"* || "$*" == *"replace -f -"* ]]; then
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

func TestKubectlResourceJSONRejectsInvalidJSON(t *testing.T) {
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nprintf 'not-json'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	if _, err := kubectlResourceJSON("test", "secret", "runtime"); err == nil {
		t.Fatal("invalid Kubernetes JSON accepted")
	}
}

func TestKubectlResourceJSONReturnsCommandFailure(t *testing.T) {
	kubectl := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(kubectl, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	if _, err := kubectlResourceJSON("test", "secret", "runtime"); err == nil {
		t.Fatal("kubectl command failure accepted")
	}
}

func TestValidateAccountManagerSendMailHTTPDeployEnv(t *testing.T) {
	for _, key := range append([]string{"LKE_ACCOUNT_MANAGER_IMAGE"}, accountManagerEmailSecretKeys...) {
		t.Setenv(key, "")
	}
	env := map[string]string{
		"LKE_ACCOUNT_MANAGER_IMAGE":  "example.test/account-manager:sha-abc",
		"AUTH_TOKEN_DELIVERY":        "sendmail_http",
		"AUTH_TOKEN_BASE_URL":        "https://admin.staging.example.test",
		"SENDMAIL_HTTP_BASE_URL":     "https://sm.realtekconnect.com",
		"SENDMAIL_HTTP_BEARER_TOKEN": "opaque-token",
		"SENDMAIL_HTTP_TIMEOUT":      "15s",
	}
	if err := validateAccountManagerEmailDeployEnv(env); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
	for name, value := range map[string]string{
		"insecure":   "http://sm.realtekconnect.com",
		"wrong host": "https://sm.example.test",
		"path":       "https://sm.realtekconnect.com/send",
		"port":       "https://sm.realtekconnect.com:8443",
	} {
		t.Run(name, func(t *testing.T) {
			bad := map[string]string{}
			for key, item := range env {
				bad[key] = item
			}
			bad["SENDMAIL_HTTP_BASE_URL"] = value
			if err := validateAccountManagerEmailDeployEnv(bad); err == nil {
				t.Fatal("unsafe Send Mail URL accepted")
			}
		})
	}
	for name, mutate := range map[string]func(map[string]string){
		"missing token": func(candidate map[string]string) {
			candidate["SENDMAIL_HTTP_BEARER_TOKEN"] = ""
		},
		"invalid timeout": func(candidate map[string]string) {
			candidate["SENDMAIL_HTTP_TIMEOUT"] = "never"
		},
		"non-positive timeout": func(candidate map[string]string) {
			candidate["SENDMAIL_HTTP_TIMEOUT"] = "0s"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneStringMap(env)
			mutate(candidate)
			if err := validateAccountManagerEmailDeployEnv(candidate); err == nil {
				t.Fatal("invalid Send Mail setting accepted")
			}
		})
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func TestMergeAccountManagerEmailSecretPreservesExistingData(t *testing.T) {
	for _, key := range accountManagerEmailSecretKeys {
		t.Setenv(key, "")
	}
	keep := base64.StdEncoding.EncodeToString([]byte("preserve-me"))
	secret := map[string]any{
		"data": map[string]any{
			"DATABASE_URL": keep,
		},
	}
	env := map[string]string{
		"AUTH_TOKEN_DELIVERY":        "sendmail_http",
		"AUTH_TOKEN_BASE_URL":        "https://admin.staging.example.test",
		"SENDMAIL_HTTP_BASE_URL":     "https://sm.realtekconnect.com",
		"SENDMAIL_HTTP_BEARER_TOKEN": "opaque-token",
		"SENDMAIL_HTTP_TIMEOUT":      "15s",
	}
	checksum, err := mergeAccountManagerEmailSecret(secret, env)
	if err != nil {
		t.Fatal(err)
	}
	if checksum == "" {
		t.Fatal("empty checksum")
	}
	data := secret["data"].(map[string]any)
	if data["DATABASE_URL"] != keep {
		t.Fatal("unrelated runtime secret was changed")
	}
	for _, key := range []string{
		"AUTH_TOKEN_DELIVERY", "AUTH_TOKEN_BASE_URL", "SENDMAIL_HTTP_BASE_URL",
		"SENDMAIL_HTTP_BEARER_TOKEN", "SENDMAIL_HTTP_TIMEOUT",
		"EMAIL_OUTBOX_ENCRYPTION_KEY",
	} {
		if strings.TrimSpace(data[key].(string)) == "" {
			t.Fatalf("%s was not populated", key)
		}
	}
}

func TestMergeAccountManagerSendMailHTTPSecret(t *testing.T) {
	for _, key := range accountManagerEmailSecretKeys {
		t.Setenv(key, "")
	}
	secret := map[string]any{"data": map[string]any{
		"DATABASE_URL": base64.StdEncoding.EncodeToString([]byte("preserve")),
	}}
	env := map[string]string{
		"AUTH_TOKEN_DELIVERY":        "sendmail_http",
		"AUTH_TOKEN_BASE_URL":        "https://admin.example.test",
		"SENDMAIL_HTTP_BASE_URL":     "https://sm.realtekconnect.com",
		"SENDMAIL_HTTP_BEARER_TOKEN": "opaque-token",
		"SENDMAIL_HTTP_TIMEOUT":      "15s",
	}
	if _, err := mergeAccountManagerEmailSecret(secret, env); err != nil {
		t.Fatal(err)
	}
	data := secret["data"].(map[string]any)
	for key, want := range map[string]string{
		"AUTH_TOKEN_DELIVERY":        "sendmail_http",
		"SENDMAIL_HTTP_BASE_URL":     "https://sm.realtekconnect.com",
		"SENDMAIL_HTTP_BEARER_TOKEN": "opaque-token",
		"SENDMAIL_HTTP_TIMEOUT":      "15s",
	} {
		decoded, err := base64.StdEncoding.DecodeString(data[key].(string))
		if err != nil || string(decoded) != want {
			t.Fatalf("%s = %q, err=%v", key, decoded, err)
		}
	}
}

func TestMergeAccountManagerEmailSecretReusesAndValidatesEncryptionKey(t *testing.T) {
	for _, key := range accountManagerEmailSecretKeys {
		t.Setenv(key, "")
	}
	existingKey := base64.StdEncoding.EncodeToString([]byte("existing-key"))
	env := map[string]string{}
	secret := map[string]any{"data": map[string]any{
		"EMAIL_OUTBOX_ENCRYPTION_KEY": existingKey,
	}}
	if _, err := mergeAccountManagerEmailSecret(secret, env); err != nil {
		t.Fatal(err)
	}
	if env["EMAIL_OUTBOX_ENCRYPTION_KEY"] != "existing-key" {
		t.Fatal("existing encryption key was not reused")
	}

	invalid := map[string]any{"data": map[string]any{
		"EMAIL_OUTBOX_ENCRYPTION_KEY": "not-base64!",
	}}
	if _, err := mergeAccountManagerEmailSecret(invalid, map[string]string{}); err == nil {
		t.Fatal("invalid existing encryption key accepted")
	}
	if _, err := mergeAccountManagerEmailSecret(map[string]any{}, map[string]string{}); err == nil {
		t.Fatal("Secret without data accepted")
	}
}

func TestUpdateAccountManagerDeploymentOnlyChangesImageAndChecksum(t *testing.T) {
	deployment := map[string]any{
		"spec": map[string]any{
			"replicas": float64(3),
			"template": map[string]any{
				"metadata": map[string]any{"annotations": map[string]any{"keep": "yes"}},
				"spec": map[string]any{
					"containers": []any{
						map[string]any{"name": "app", "image": "old", "env": []any{"keep"}},
					},
				},
			},
		},
	}
	if err := updateAccountManagerDeployment(deployment, "new", "checksum"); err != nil {
		t.Fatal(err)
	}
	spec := deployment["spec"].(map[string]any)
	if spec["replicas"] != float64(3) {
		t.Fatal("replica count changed")
	}
	template := spec["template"].(map[string]any)
	annotations := template["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations["keep"] != "yes" || annotations["rtk.realtek.com/runtime-checksum"] != "checksum" {
		t.Fatalf("annotations = %#v", annotations)
	}
	container := template["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if container["image"] != "new" || len(container["env"].([]any)) != 1 {
		t.Fatalf("container = %#v", container)
	}
}

func TestUpdateAccountManagerDeploymentInitializesMetadataAndRejectsInvalidShapes(t *testing.T) {
	deployment := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "app", "image": "old"}},
				},
			},
		},
	}
	if err := updateAccountManagerDeployment(deployment, "new", "checksum"); err != nil {
		t.Fatal(err)
	}
	template := deployment["spec"].(map[string]any)["template"].(map[string]any)
	annotations := template["metadata"].(map[string]any)["annotations"].(map[string]any)
	if annotations["rtk.realtek.com/runtime-checksum"] != "checksum" {
		t.Fatalf("annotations = %#v", annotations)
	}

	for name, candidate := range map[string]map[string]any{
		"missing spec": {},
		"missing template": {
			"spec": map[string]any{},
		},
		"missing pod spec": {
			"spec": map[string]any{"template": map[string]any{}},
		},
		"missing containers": {
			"spec": map[string]any{"template": map[string]any{
				"spec": map[string]any{},
			}},
		},
		"missing app container": {
			"spec": map[string]any{"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "sidecar"}},
				},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := updateAccountManagerDeployment(candidate, "new", "checksum"); err == nil {
				t.Fatal("invalid Deployment shape accepted")
			}
		})
	}
}

func TestAccountManagerEmailWorkerManifestUsesProvidedChecksum(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"LKE_ACCOUNT_MANAGER_IMAGE": "example.test/account-manager:sha-abc",
	}
	manifest := lkeAccountManagerEmailWorkerManifestWithChecksum(env, "exact-checksum")
	for _, want := range []string{
		"name: account-manager-email-worker",
		`rtk.realtek.com/runtime-checksum: "exact-checksum"`,
		"example.test/account-manager:sha-abc",
		`command: ["/app/rtk-account-manager-email-worker"]`,
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest missing %q:\n%s", want, manifest)
		}
	}
}
