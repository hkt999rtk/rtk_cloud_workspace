package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBillingStagingQualificationWorkflowIsControlledAndEvidenceBacked(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "billing-staging-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		"workflow_dispatch:",
		"schedule:",
		"environment: staging",
		"group: staging-mutating-tests",
		"cancel-in-progress: false",
		"BILLING_STAGING_QUALIFICATION_ENABLED",
		"options: [plan, run]",
		"video-cloud-staging-lke",
		"test-payment",
		"--profile staging-live",
		"--bootstrap-test-org",
		"--customer-session-file",
		"::add-mask::",
		"/api/auth/logout",
		"BILLING_STAGING_ENV_ROOT",
		"$RUNNER_TEMP/billing-staging-runtime",
		"lke-build-images",
		"--workloads billing,cloud-admin",
		"LKE_CLOUD_ADMIN_IMAGE",
		"Deploy only Billing and Cloud Admin without rotating shared PKI",
		"packages: write",
		"rollout status deployment/billing",
		"e2e:billing:staging",
		"retention-days: 90",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("workflow missing %q", required)
		}
	}
	if strings.Contains(body, "pull_request:") || strings.Contains(body, "cancel-in-progress: true") {
		t.Fatal("Billing staging mutation must not run for pull requests or cancel an in-progress cleanup")
	}
	if strings.Contains(body, "BILLING_STAGING_CUSTOMER_SESSION_ID") {
		t.Fatal("Billing staging qualification must mint an ephemeral customer session instead of storing one in repository secrets")
	}
	if strings.Contains(body, "linode_deploy") || strings.Contains(body, "deploy/linode") {
		t.Fatal("Billing staging qualification must remain K8s-only")
	}
	if strings.Contains(body, "            --dns \\") {
		t.Fatal("recurring Billing qualification must not reconcile shared public edge infrastructure")
	}
}
