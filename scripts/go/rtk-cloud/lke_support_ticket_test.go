package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportTicketDeploymentRequiresValidGroupAndOwner(t *testing.T) {
	for _, tc := range []struct{ config, want string }{
		{"SUPPORT_TICKETS_ENABLED=true\n", "ZAMMAD_SUPPORT_GROUP_ID"},
		{"SUPPORT_TICKETS_ENABLED=true\nZAMMAD_SUPPORT_GROUP_ID=7\nZAMMAD_UNASSIGNED_OWNER_ID=invalid\n", "ZAMMAD_UNASSIGNED_OWNER_ID"},
		{"SUPPORT_TICKETS_ENABLED=true\nZAMMAD_SUPPORT_GROUP_ID=7\nZAMMAD_UNASSIGNED_OWNER_ID=1\n", ""},
	} {
		workspace := writeDeploymentFixture(t, "dev", "lke")
		appendFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), tc.config)
		cfg, err := resolveDeploymentConfig(workspace, "dev", "")
		if tc.want != "" {
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("config %q: got %v, want %s validation", tc.config, err, tc.want)
			}
			continue
		}
		if err != nil || cfg.Values["ZAMMAD_SUPPORT_GROUP_ID"] != "7" {
			t.Fatalf("valid support deployment rejected: %v", err)
		}
	}
}

func TestLKESupportDependencySecretsFailClosed(t *testing.T) {
	oldCanonical, oldCache, oldDir := activeCanonicalSecretStore, lkeRuntimeSecretCache, lkeRuntimeSecretStateDir
	activeCanonicalSecretStore = true
	lkeRuntimeSecretCache = map[string]string{}
	lkeRuntimeSecretStateDir = t.TempDir()
	t.Cleanup(func() {
		activeCanonicalSecretStore, lkeRuntimeSecretCache, lkeRuntimeSecretStateDir = oldCanonical, oldCache, oldDir
	})
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-test", "SUPPORT_TICKETS_ENABLED": "true"}
	put := func(id, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(lkeRuntimeSecretStateDir, id), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := lkeSupportDependencySecretManifests(env); err == nil || !strings.Contains(err.Error(), "zammad-postgres-password") {
		t.Fatalf("missing PostgreSQL password: %v", err)
	}
	put("zammad-postgres-password", "pg-fixture")
	if _, err := lkeSupportDependencySecretManifests(env); err == nil || !strings.Contains(err.Error(), "zammad-redis-password") {
		t.Fatalf("missing Redis password: %v", err)
	}
	put("zammad-redis-password", "redis-fixture")
	if _, err := lkeSupportDependencySecretManifests(env); err == nil || !strings.Contains(err.Error(), "integration token") {
		t.Fatalf("missing integration token: %v", err)
	}
	put("zammad-integration-token", "token-fixture")
	manifests, err := lkeSupportDependencySecretManifests(env)
	if err != nil || len(manifests) != 2 || !strings.Contains(manifests[0], "namespace: video-cloud-test-support") || !strings.Contains(manifests[1], "redis-password") {
		t.Fatalf("support dependency Secret manifests invalid: %v", err)
	}
}

func TestLKESupportTicketsStayPrivateAndOptIn(t *testing.T) {
	if supportSecretRequired(secretStore{Environment: "dev"}, "zammad-postgres-password") {
		t.Fatal("disabled dev must not require a support namespace Secret")
	}
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-test"}
	if lkeCloudAdminRuntimeChecksumWithSupport(env) != lkeCloudAdminRuntimeChecksum() {
		t.Fatal("disabled support must not change the Admin runtime checksum")
	}
	if got := strings.Join(lkePublicHTTPSNetworkPolicyManifests(env, nil), "\n"); strings.Contains(got, "allow-cloud-admin-zammad-api") {
		t.Fatal("disabled support must not add support network access")
	}
	if got := lkeDeploymentManifest(env, lkeWorkload{Key: "cloud-admin", Name: "cloud-admin"}, nil); strings.Contains(got, "ZAMMAD_BASE_URL") {
		t.Fatal("disabled support must not inject Zammad configuration")
	}
	env["SUPPORT_TICKETS_ENABLED"] = "true"
	env["ZAMMAD_SUPPORT_GROUP_ID"] = "17"
	policies := strings.Join(lkeSupportNetworkPolicyManifests(env), "\n---\n")
	if got := strings.Join(lkePublicHTTPSNetworkPolicyManifests(env, nil), "\n---\n"); !strings.Contains(got, "allow-cloud-admin-zammad-api") {
		t.Fatal("enabled support policy is missing from the public-edge policy reconciliation set")
	}
	for _, expected := range []string{"default-deny-ingress", "allow-support-internal", "allow-cloud-admin-zammad-api", "app.kubernetes.io/component: zammad-nginx", "kubernetes.io/metadata.name: video-cloud-test-admin", "port: 8080"} {
		if !strings.Contains(policies, expected) {
			t.Fatalf("missing support policy requirement %q", expected)
		}
	}
	manifest := lkeDeploymentManifest(env, lkeWorkload{Key: "cloud-admin", Name: "cloud-admin"}, nil)
	for _, expected := range []string{"ZAMMAD_BASE_URL", "http://zammad-nginx.video-cloud-test-support.svc.cluster.local:8080", "ZAMMAD_SUPPORT_GROUP_ID", "ZAMMAD_UNASSIGNED_OWNER_ID"} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("missing admin runtime setting %q", expected)
		}
	}
	if got := lkeCloudAdminBillingSecretManifest(env); !strings.Contains(got, "ZAMMAD_API_TOKEN") {
		t.Fatal("enabled support must inject the server-only API token")
	}
}
