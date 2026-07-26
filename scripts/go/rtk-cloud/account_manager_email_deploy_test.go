package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidateAccountManagerEmailDeployEnv(t *testing.T) {
	for _, key := range append([]string{"LKE_ACCOUNT_MANAGER_IMAGE"}, accountManagerEmailSecretKeys...) {
		t.Setenv(key, "")
	}
	env := map[string]string{
		"LKE_ACCOUNT_MANAGER_IMAGE": "example.test/account-manager:sha-abc",
		"AUTH_TOKEN_DELIVERY":       "smtp",
		"AUTH_TOKEN_BASE_URL":       "https://admin.staging.example.test",
		"SMTP_HOST":                 "smtp.example.test",
		"SMTP_PORT":                 "587",
		"SMTP_USERNAME":             "no-reply@example.test",
		"SMTP_PASSWORD":             "secret",
		"SMTP_FROM":                 "no-reply@example.test",
	}
	if err := validateAccountManagerEmailDeployEnv(env); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
	for name, value := range map[string]string{
		"insecure":    "http://admin.staging.example.test",
		"credentials": "https://user:pass@admin.staging.example.test",
		"path":        "https://admin.staging.example.test/signup",
	} {
		t.Run(name, func(t *testing.T) {
			bad := map[string]string{}
			for key, item := range env {
				bad[key] = item
			}
			bad["AUTH_TOKEN_BASE_URL"] = value
			if err := validateAccountManagerEmailDeployEnv(bad); err == nil {
				t.Fatal("unsafe AUTH_TOKEN_BASE_URL accepted")
			}
		})
	}
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
		"AUTH_TOKEN_DELIVERY": "smtp",
		"AUTH_TOKEN_BASE_URL": "https://admin.staging.example.test",
		"SMTP_HOST":           "smtp.example.test",
		"SMTP_PORT":           "587",
		"SMTP_USERNAME":       "no-reply@example.test",
		"SMTP_PASSWORD":       "secret",
		"SMTP_FROM":           "no-reply@example.test",
		"SMTP_ENCRYPTION":     "starttls",
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
	for _, key := range accountManagerEmailSecretKeys {
		if strings.TrimSpace(data[key].(string)) == "" {
			t.Fatalf("%s was not populated", key)
		}
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
