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
		"lke-resolve-images",
		"get secret ghcr-pull",
		"GHCR_PULL_USERNAME",
		"GHCR_PULL_TOKEN",
		"LKE_ACCOUNT_MANAGER_IMAGE",
		"LKE_BILLING_IMAGE",
		"LKE_CLOUD_ADMIN_IMAGE",
		"Deploy the coordinated multicloud stack without rotating shared PKI",
		"packages: read",
		"rollout status deployment/billing",
		"rollout status deployment/account-manager-handoff-worker",
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
	if strings.Contains(body, "lke-build-images") || strings.Contains(body, "docker build") || strings.Contains(body, "docker push") {
		t.Fatal("staging qualification must use official CI-published service images")
	}
	if strings.Contains(body, "secrets.GHCR_PULL_USERNAME") || strings.Contains(body, "secrets.GHCR_PULL_TOKEN") {
		t.Fatal("staging qualification must reuse the existing masked cluster pull identity")
	}
	if strings.Contains(body, "--workloads ") {
		t.Fatal("enabled ownership handoff requires one coordinated full-stack deploy")
	}
	if strings.Contains(body, "            --dns \\") {
		t.Fatal("recurring Billing qualification must not reconcile shared public edge infrastructure")
	}
}

func TestLKEImageResolverUsesTheExistingMaskedStagingPullIdentity(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, ".github", "workflows", "lke-image-artifacts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		"environment: staging",
		"secrets.LINODE_TOKEN",
		"get secret ghcr-pull",
		"::add-mask::",
		"docker login ghcr.io",
		"lke-resolve-images",
		"Remove temporary staging credentials",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("LKE resolver workflow missing %q", required)
		}
	}
	if strings.Contains(body, "secrets.GHCR_PULL_USERNAME") || strings.Contains(body, "secrets.GHCR_PULL_TOKEN") {
		t.Fatal("LKE resolver must not depend on absent repository package secrets")
	}
	if strings.Contains(body, "lke-build-images") || strings.Contains(body, "docker build") || strings.Contains(body, "docker push") {
		t.Fatal("LKE resolver must not publish staging images")
	}
}

func TestBillingStagingQualificationWorkflowRunbookIsAgentReady(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "docs", "billing-staging-qualification.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, required := range []string{
		"billing-staging-qualification.yml",
		"mode=plan",
		"mode=run",
		"video-cloud-staging-lke",
		"rtk-payment-simulator-qualification",
		"LINODE_TOKEN",
		"CI_RUNNER_GITHUB_WORK_KEY",
		"RTK_CLOUD_SECRET_BUNDLE",
		"BILLING_STAGING_OTHER_ORG_ID",
		"BILLING_STAGING_QUALIFICATION_ENABLED",
		"LIVE-STG-SIMULATOR-001",
		"LIVE-STG-AUTOTOPUP-001",
		"LIVE-STG-MANUAL-TOPUP-001",
		"LIVE-STG-BILLING-DOCUMENT-001",
		"UI-CA-BILLING-STG-001",
		"UI-CA-BILLING-STG-002",
		"UI-CA-BILLING-STG-003",
		"Revoke ephemeral Cloud Admin customer session",
		"Upload sanitized qualification evidence",
		"90 days",
		"Do not retry blindly",
		"Do not rotate shared PKI",
		"Do not cancel another run",
		"never build, push or retag a staging image",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("Billing staging qualification runbook missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"cloud_env/staging/linode",
		"linode_deploy",
		"deploy/linode",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("Billing staging qualification runbook contains retired operator path %q", forbidden)
		}
	}

	testingDoc, err := os.ReadFile(filepath.Join(workspace, "docs", "testing.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(testingDoc), "--bootstrap-test-org --env-root cloud_env/staging/linode") {
		t.Fatal("testing guide still recommends the retired staging runtime path")
	}
	if !strings.Contains(string(testingDoc), "--bootstrap-test-org --env-root cloud_env/staging/runtime") {
		t.Fatal("testing guide does not use the canonical staging runtime path")
	}
}

func TestStagingEnablesTheCoordinatedOwnershipHandoffWorker(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(workspace, "cloud_env", "staging", "linode", "env", "stack.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED=true") {
		t.Fatal("staging must explicitly enable the ownership handoff coordinator")
	}
}
