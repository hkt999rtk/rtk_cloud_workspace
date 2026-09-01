package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMulticloudStagingWorkflowUsesFormalOwnerAndScopedEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "multicloud-staging-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"runs-on: [self-hosted, Linux, X64]",
		"group: staging-mutating-tests",
		"--email-activate-owners",
		"test-multicloud",
		"--confirm \"$MULTICLOUD_CONFIRM\"",
		"mqtt-test",
		"RTK_CLOUD_SECRET_BUNDLE",
		"materialize-rtk-cloud-secret-store.sh staging",
		"brand-plan-multicloud-staging.json",
		"multicloud-staging/**",
		"owner-activation/**",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("multi-cloud staging workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{"ubuntu-latest", "initialize_billing_inbox", "**/*.sqlite", "data/logs/**"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("multi-cloud staging workflow contains forbidden boundary %q", forbidden)
		}
	}
}

func TestFormalLoadOwnerUsesPublicSignupNotAdminOwnerProvisioning(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "scripts", "go", "rtk-cloud", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func runActivateLoadOwner(")
	if start < 0 {
		t.Fatal("could not isolate formal owner activation implementation")
	}
	end := strings.Index(source[start:], "func rewriteLoadOwnerEvidenceBrandName(")
	if end < 0 {
		t.Fatal("could not isolate formal owner activation implementation")
	}
	implementation := source[start : start+end]
	if !strings.Contains(implementation, `ctx.BaseURL+"/v1/auth/signup"`) {
		t.Fatal("formal owner activation does not use public signup")
	}
	for _, forbidden := range []string{"/v1/admin/brand-clouds/", `payload := map[string]any{"email": ownerEmail, "role": "owner"}`, "accountPostgresFallback"} {
		if strings.Contains(implementation, forbidden) {
			t.Fatalf("formal owner activation retains forbidden shortcut %q", forbidden)
		}
	}
}
