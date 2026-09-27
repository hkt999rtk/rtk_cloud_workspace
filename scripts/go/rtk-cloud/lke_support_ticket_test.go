package main

import (
	"strings"
	"testing"
)

func TestLKESupportTicketsStayPrivateAndOptIn(t *testing.T) {
	if supportSecretRequired(secretStore{Environment: "dev"}, "zammad-postgres-password") {
		t.Fatal("disabled dev must not require a support namespace Secret")
	}
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-test"}
	if got := strings.Join(lkePublicHTTPSNetworkPolicyManifests(env, nil), "\n"); strings.Contains(got, "allow-cloud-admin-zammad-api") {
		t.Fatal("disabled support must not add support network access")
	}
	if got := lkeDeploymentManifest(env, lkeWorkload{Key: "cloud-admin", Name: "cloud-admin"}, nil); strings.Contains(got, "ZAMMAD_BASE_URL") {
		t.Fatal("disabled support must not inject Zammad configuration")
	}
	env["SUPPORT_TICKETS_ENABLED"] = "true"
	env["ZAMMAD_SUPPORT_GROUP_ID"] = "17"
	policies := strings.Join(lkeSupportNetworkPolicyManifests(env), "\n---\n")
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
