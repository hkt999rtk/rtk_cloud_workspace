package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPaymentLiveBillingRequestUsesServiceIdentityAndExactPermission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+strings.Repeat("b", 32) {
			t.Errorf("authorization = %q", got)
		}
		if got := request.Header.Get("X-Billing-Permissions"); got != "billing_account.read" {
			t.Errorf("permission = %q", got)
		}
		if request.Header.Get("X-Billing-Actor-Type") != "service_test" || request.Header.Get("X-Billing-Actor-ID") == "" {
			t.Error("missing service-test actor identity")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	var output map[string]any
	if err := paymentLiveBillingJSON(context.Background(), server.Client(), http.MethodGet, server.URL, strings.Repeat("b", 32), "billing_account.read", nil, nil, &output); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentLiveDefaultsToNonMutatingPlan(t *testing.T) {
	output := captureStdout(t, func() {
		if err := runTestPayment([]string{"--profile", "staging-live", "--run-id", "plan-test"}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "preflight -> dedicated organization -> hosted setup") {
		t.Fatalf("missing staging-live plan: %s", output)
	}
}

func TestPaymentLiveBootstrapRequiresFixedOrganizationConfirmation(t *testing.T) {
	cfg := paymentLiveConfig{Run: true, BootstrapTestOrg: true, Confirm: paymentLiveConfirmation, ConfirmTestOrg: "wrong", EnvRoot: t.TempDir(), AccountManagerBaseURL: "https://account-manager.video-cloud-staging.realtekconnect.com", BillingBaseURL: "https://billing.video-cloud-staging.realtekconnect.com", Timeout: time.Minute}
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), paymentLiveBootstrapConfirmation) {
		t.Fatalf("bootstrap must require fixed organization confirmation, got %v", err)
	}
	cfg.ConfirmTestOrg = paymentLiveBootstrapConfirmation
	if err := validatePaymentLiveConfig(cfg); err != nil {
		t.Fatalf("safe bootstrap configuration rejected: %v", err)
	}
	cfg.OrgID = "unexpected"
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("bootstrap with arbitrary org must fail, got %v", err)
	}
}

func TestPaymentLiveRequiresExactSafetyConfirmations(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("x", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	billingTokenFile := filepath.Join(t.TempDir(), "billing-token")
	if err := os.WriteFile(billingTokenFile, []byte(strings.Repeat("y", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := paymentLiveConfig{Run: true, AccountManagerBaseURL: "https://account-manager.video-cloud-staging.realtekconnect.com", BillingBaseURL: "https://billing.video-cloud-staging.realtekconnect.com", OrgID: "org-test", AccountTokenFile: tokenFile, BillingTokenFile: billingTokenFile, Timeout: time.Minute}
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("wrong stack confirmation must fail, got %v", err)
	}
	cfg.Confirm = paymentLiveConfirmation
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), "--confirm-test-org") {
		t.Fatalf("missing test organization confirmation must fail, got %v", err)
	}
	cfg.ConfirmTestOrg = cfg.OrgID
	if err := validatePaymentLiveConfig(cfg); err != nil {
		t.Fatalf("valid safe configuration rejected: %v", err)
	}
}

func TestPaymentLiveRejectsRawOrInsecureCredentials(t *testing.T) {
	cfg := paymentLiveConfig{Run: true, Confirm: paymentLiveConfirmation, OrgID: "org-test", ConfirmTestOrg: "org-test", AccountManagerBaseURL: "http://account-manager.video-cloud-staging.realtekconnect.com", BillingBaseURL: "https://billing.video-cloud-staging.realtekconnect.com", Timeout: time.Minute}
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), "approved HTTPS") {
		t.Fatalf("insecure URL must fail, got %v", err)
	}
	cfg.AccountManagerBaseURL = "https://account-manager.video-cloud-staging.realtekconnect.com"
	cfg.AccountTokenFile = filepath.Join(t.TempDir(), "missing")
	cfg.BillingTokenFile = filepath.Join(t.TempDir(), "missing-billing")
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), "token files") {
		t.Fatalf("missing token file must fail, got %v", err)
	}
}

func TestPaymentLiveReportRequiresAndHashesResponsiveEvidence(t *testing.T) {
	outDir := t.TempDir()
	evidenceDir := filepath.Join(outDir, "evidence")
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"desktop", "mobile"} {
		if err := os.WriteFile(filepath.Join(evidenceDir, "LIVE-STG-SIMULATOR-001@"+target+".png"), []byte("safe synthetic png "+target), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := paymentEvidenceReport{RunID: "report-test", Profile: "staging-live", Environment: "staging", Status: "PASS", Cases: []paymentEvidenceCase{{TestID: "LIVE-STG-SIMULATOR-001", Status: "PASS"}}}
	if err := writePaymentLiveReports(outDir, report, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "evidence-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest paymentEvidenceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, evidence := range manifest.Evidence {
		if evidence.SHA256 == "" {
			t.Fatalf("evidence missing digest: %+v", evidence)
		}
		paths[evidence.Path] = true
	}
	for _, required := range []string{"execution.log", "evidence/LIVE-STG-SIMULATOR-001@desktop.png", "evidence/LIVE-STG-SIMULATOR-001@mobile.png"} {
		if !paths[required] {
			t.Fatalf("manifest missing %s: %+v", required, manifest.Evidence)
		}
	}
}

func TestPaymentLiveReportFailsClosedWithoutResponsiveEvidence(t *testing.T) {
	report := paymentEvidenceReport{RunID: "missing-evidence", Profile: "staging-live", Environment: "staging", Status: "PASS", Cases: []paymentEvidenceCase{{TestID: "LIVE-STG-SIMULATOR-001", Status: "PASS"}}}
	err := writePaymentLiveReports(t.TempDir(), report, nil)
	if err == nil || !strings.Contains(err.Error(), "screenshots are required") {
		t.Fatalf("missing screenshot evidence must fail, got %v", err)
	}
}
