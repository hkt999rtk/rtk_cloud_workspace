package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConsoleDriftBlocksProvisionBeforeProviderCalls(t *testing.T) {
	workspace, runtime := makeLKETestEnv(t)
	writeTestFile(t, filepath.Join(filepath.Dir(runtime), "environment.env"), "GOOGLE_LOGIN_ENABLED=false\n")
	log := fakeKubectl(t)
	err := runProvision([]string{"--workspace", workspace, "--env-root", runtime, "--deploy", "--workloads", "account-manager", "--confirm", "video-cloud-staging"})
	if err == nil || !strings.Contains(err.Error(), "stale or conflicting Console") {
		t.Fatalf("expected configuration failure before deploy: %v", err)
	}
	if calls, err := os.ReadFile(log); err == nil && len(calls) != 0 {
		t.Fatal("Kubernetes called before configuration gate")
	}
}

func TestConsoleRuntimeConfigRejectsRestoredSettings(t *testing.T) {
	t.Setenv("GOOGLE_LOGIN_ENABLED", "")
	t.Setenv("TEST_LAB_ENABLED", "")
	want := map[string]string{"GOOGLE_LOGIN_ENABLED": "true", "TEST_LAB_ENABLED": "true"}
	actual := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := compareConsoleRuntimeConfig(want, actual, provisionOptions{workloads: []string{"account-manager"}}); err == nil || !strings.Contains(err.Error(), "GOOGLE_LOGIN_ENABLED") {
		t.Fatalf("stale runtime must fail: %v", err)
	}
	if err := compareConsoleRuntimeConfig(want, actual, provisionOptions{workloads: []string{"frontend"}}); err != nil {
		t.Fatalf("unrelated image update must not need Console credentials: %v", err)
	}
	actual["GOOGLE_LOGIN_ENABLED"], actual["TEST_LAB_ENABLED"] = "true", "true"
	if err := compareConsoleRuntimeConfig(want, actual, provisionOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_LOGIN_ENABLED", "false")
	if err := compareConsoleRuntimeConfig(want, actual, provisionOptions{}); err == nil {
		t.Fatal("process override hid drift")
	}
}

func TestConsoleRuntimeConfigReadsDesiredWithoutWriting(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "runtime")
	if err := validateConsoleRuntimeConfig(runtime, nil, provisionOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "environment.env"), "GOOGLE_LOGIN_ENABLED=true\n")
	if err := validateConsoleRuntimeConfig(runtime, nil, provisionOptions{}); err == nil {
		t.Fatal("stale runtime accepted")
	}
	writeTestFile(t, filepath.Join(root, "environment.env"), "not an env line\n")
	if err := validateConsoleRuntimeConfig(runtime, nil, provisionOptions{}); err == nil {
		t.Fatal("malformed contract accepted")
	}
}

func TestConsoleDeployInputs(t *testing.T) {
	for _, key := range []string{"GOOGLE_LOGIN_ENABLED", "GOOGLE_OAUTH_CLIENT_ID", "GITHUB_LOGIN_ENABLED", "GITHUB_OAUTH_CLIENT_ID", "SOCIAL_LOGIN_CALLBACK_URL", "TEST_LAB_ENABLED", "LKE_MQTT_TENANT_NAMESPACE_ENABLED"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct{ name, field, value, secretID, missing string }{
		{name: "valid"},
		{name: "missing shared authorization", secretID: "job-authorization-token", missing: "job-authorization-token"},
		{name: "missing Google ID", field: "GOOGLE_OAUTH_CLIENT_ID", missing: "GOOGLE"},
		{name: "missing Google secret", secretID: "google-oauth-client-secret", missing: "GOOGLE"},
		{name: "missing GitHub secret", secretID: "github-oauth-client-secret", missing: "GITHUB"},
		{name: "short state", secretID: "social-oauth-state-secret", missing: "social-oauth-state-secret"},
		{name: "other environment callback", field: "SOCIAL_LOGIN_CALLBACK_URL", value: "https://admin.dev.test/api/auth/social/callback", missing: "CALLBACK"},
		{name: "production Test Lab", field: "CLOUD_STACK_NAME", value: "video-cloud-production", missing: "Test Lab"},
		{name: "disabled tenant listener", field: "LKE_MQTT_TENANT_NAMESPACE_ENABLED", value: "false", missing: "Test Lab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_ADMIN_DOMAIN": "admin.staging.test", "GOOGLE_LOGIN_ENABLED": "true", "GOOGLE_OAUTH_CLIENT_ID": "fixture-public-id", "SOCIAL_LOGIN_CALLBACK_URL": "https://admin.staging.test/api/auth/social/callback", "TEST_LAB_ENABLED": "true"}
			env["GITHUB_LOGIN_ENABLED"], env["GITHUB_OAUTH_CLIENT_ID"] = "true", "fixture-github-id"
			if tc.field != "" {
				env[tc.field] = tc.value
			}
			read := func(id string) string {
				if id == tc.secretID {
					return ""
				}
				return strings.Repeat("s", 32)
			}
			err := validateLKEConsoleDeployInputs(env, provisionOptions{}, read)
			if tc.missing == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("want %s, got %v", tc.missing, err)
			}
			if strings.Contains(err.Error(), strings.Repeat("s", 32)) {
				t.Fatal("secret leaked")
			}
		})
	}
	if err := validateLKEConsoleDeployInputs(nil, provisionOptions{workloads: []string{"frontend"}}, func(string) string { t.Fatal("unrelated secret read"); return "" }); err != nil {
		t.Fatal(err)
	}
}

func TestJobAuthorizationSurvivesRenderingAndRotatesBothConsumers(t *testing.T) {
	oldCanonical, oldCache := activeCanonicalSecretStore, lkeRuntimeSecretCache
	activeCanonicalSecretStore = false
	lkeRuntimeSecretCache = map[string]string{"job-authorization-token": "fixture-job-a"}
	t.Cleanup(func() { activeCanonicalSecretStore, lkeRuntimeSecretCache = oldCanonical, oldCache })
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "job-auth-render-test")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	for _, render := range []func(map[string]string) string{lkeAccountManagerSecretManifest, lkeCloudAdminBillingSecretManifest} {
		for i := 0; i < 2; i++ {
			if !strings.Contains(render(env), `ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN: "fixture-job-a"`) {
				t.Fatal("re-render lost job authorization")
			}
		}
	}
	want := []secretK8SBinding{{"-account-manager", "account-manager-runtime", "ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN"}, {"-admin", "cloud-admin-billing-client", "ACCOUNT_MANAGER_JOB_AUTHORIZATION_TOKEN"}}
	if !reflect.DeepEqual(catalogK8SBindings("job-authorization-token"), want) {
		t.Fatal("canonical bindings disagree with rendered namespaces")
	}
	am := lkeDeploymentManifest(env, lkeWorkload{Key: "account-manager", Name: "account-manager"}, nil)
	admin := lkeCloudAdminRuntimeChecksum()
	lkeRuntimeSecretCache["job-authorization-token"] = "fixture-job-b"
	if admin == lkeCloudAdminRuntimeChecksum() || am == lkeDeploymentManifest(env, lkeWorkload{Key: "account-manager", Name: "account-manager"}, nil) {
		t.Fatal("both consumers must roll when shared token changes")
	}
}
