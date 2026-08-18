package main

import (
	"context"
	"encoding/json"
	"net"
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
	debitTokenFile := filepath.Join(t.TempDir(), "debit-token")
	if err := os.WriteFile(debitTokenFile, []byte(strings.Repeat("z", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	internalTokenFile := filepath.Join(t.TempDir(), "internal-token")
	if err := os.WriteFile(internalTokenFile, []byte(strings.Repeat("i", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := paymentLiveConfig{Run: true, AccountManagerBaseURL: "https://account-manager.video-cloud-staging.realtekconnect.com", BillingBaseURL: "https://billing.video-cloud-staging.realtekconnect.com", OrgID: "org-test", AccountTokenFile: tokenFile, BillingTokenFile: billingTokenFile, InternalTokenFile: internalTokenFile, DebitTokenFile: debitTokenFile, Timeout: time.Minute}
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
	cfg.InternalTokenFile = filepath.Join(t.TempDir(), "missing-internal")
	cfg.DebitTokenFile = filepath.Join(t.TempDir(), "missing-debit")
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

func TestExecuteAndCleanupPaymentLiveCompletesSimulatorQualification(t *testing.T) {
	methodActive := false
	debitPosted := false
	manualPosted := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/internal/billing/access/org-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/internal/billing/debits" && r.Header.Get("Authorization") != "Bearer "+strings.Repeat("d", 32) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/internal/billing/") && r.URL.Path != "/v1/internal/billing/debits" && r.Header.Get("Authorization") != "Bearer "+strings.Repeat("i", 32) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/orgs/org-test/") && r.Header.Get("Authorization") != "Bearer "+strings.Repeat("b", 32) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/orgs/org-test/billing/account":
			_, _ = w.Write([]byte(`{"account":{"available_balance_minor":0},"payment_providers":[{"name":"simulator","environment":"simulated","capabilities":{"hosted_setup":true,"merchant_initiated_charge":true}}],"auto_topup":null}`))
		case r.URL.Path == "/v1/orgs/org-test/billing/ledger":
			entries := `[]`
			if debitPosted {
				entries = `[{"id":"credit-auto","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":299},{"id":"debit-1","direction":"debit","reason":"usage_adjustment_debit","amount_minor":1,"balance_after_minor":-1}]`
			}
			if manualPosted {
				entries = `[{"id":"credit-manual","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":599},{"id":"credit-auto","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":299},{"id":"debit-1","direction":"debit","reason":"usage_adjustment_debit","amount_minor":1,"balance_after_minor":-1}]`
			}
			_, _ = w.Write([]byte(`{"ledger_entries":` + entries + `}`))
		case r.URL.Path == "/v1/orgs/org-test/payment-methods" && r.Method == http.MethodGet:
			status := "revoked"
			if methodActive {
				status = "active"
			}
			_, _ = w.Write([]byte(`{"payment_methods":[{"id":"method-1","status":"` + status + `"}]}`))
		case r.URL.Path == "/v1/orgs/org-test/payment-methods/setup":
			_, _ = w.Write([]byte(`{"hosted_url":"https://payment-simulator.staging.realtekconnect.com","payment_method":{"id":"method-1"}}`))
		case r.URL.Path == "/complete":
			methodActive = true
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/orgs/org-test/auto-topup" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"auto_topup":{"version":1}}`))
		case r.URL.Path == "/v1/orgs/org-test/auto-topup" && r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"auto_topup":{"version":2}}`))
		case r.URL.Path == "/v1/orgs/org-test/auto-topup" && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"auto_topup":{"enabled":false,"version":3}}`))
		case r.URL.Path == "/v1/internal/billing/debits":
			if got := r.Header.Get("Authorization"); got != "Bearer "+strings.Repeat("d", 32) {
				t.Fatalf("debit authorization = %q", got)
			}
			duplicate := debitPosted
			debitPosted = true
			_, _ = w.Write([]byte(`{"ledger_entry_id":"debit-1","payment_intent_id":"intent-auto","duplicate":` + map[bool]string{true: "true", false: "false"}[duplicate] + `}`))
		case r.URL.Path == "/v1/orgs/org-test/payment-intents/intent-auto":
			_, _ = w.Write([]byte(`{"payment_intent":{"state":"succeeded"},"attempts":[{"operation":"charge","status":"succeeded"}]}`))
		case r.URL.Path == "/v1/orgs/org-test/topups":
			manualPosted = true
			_, _ = w.Write([]byte(`{"payment_intent":{"id":"intent-manual"}}`))
		case r.URL.Path == "/v1/orgs/org-test/payment-intents/intent-manual":
			_, _ = w.Write([]byte(`{"payment_intent":{"state":"succeeded"},"attempts":[{"operation":"charge","status":"succeeded"}]}`))
		case r.URL.Path == "/v1/orgs/org-test/payment-methods/method-1" && r.Method == http.MethodDelete:
			methodActive = false
			_, _ = w.Write([]byte(`{"payment_method":{"status":"revoked"}}`))
		case r.URL.Path == "/v1/internal/billing/pricing-versions" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"pricing_version":{"id":"pricing-qualification"}}`))
		case r.URL.Path == "/v1/internal/billing/pricing-versions/pricing-qualification/activate" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"pricing_version":{"id":"pricing-qualification","status":"active"}}`))
		case r.URL.Path == "/v1/internal/billing/usage-facts" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"usage_fact":{"id":"usage-qualification"}}`))
		case r.URL.Path == "/v1/internal/billing/periods/close" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"invoice":{"id":"invoice-qualification","state":"settled"}}`))
		default:
			t.Fatalf("unexpected payment qualification request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Test transport is pinned to the local TLS listener.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldScreenshot := paymentLiveScreenshot
	paymentLiveScreenshot = func(_, _, _, output string) error {
		return os.WriteFile(output, []byte("synthetic png"), 0o644)
	}
	t.Cleanup(func() { paymentLiveScreenshot = oldScreenshot })
	cfg := paymentLiveConfig{RunID: "live-success", BillingBaseURL: server.URL, OrgID: "org-test", Timeout: time.Second}
	state := paymentLiveState{}
	if err := executePaymentLive(context.Background(), client, t.TempDir(), outDir, cfg, strings.Repeat("b", 32), strings.Repeat("i", 32), strings.Repeat("d", 32), &state); err != nil {
		t.Fatal(err)
	}
	if state.MethodID != "method-1" || state.AutoIntentID != "intent-auto" || state.ManualIntentID != "intent-manual" || state.PolicyVersion != 2 || !state.HostedSetupPassed || !state.AutoTopUpPassed || !state.ManualTopUpPassed {
		t.Fatalf("unexpected qualification state: %+v", state)
	}
	if err := cleanupPaymentLive(context.Background(), client, cfg, strings.Repeat("b", 32), state); err != nil {
		t.Fatal(err)
	}
	debitPosted = false
	manualPosted = false

	tokenFile := filepath.Join(t.TempDir(), "billing-token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("b", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	debitTokenFile := filepath.Join(t.TempDir(), "debit-token")
	if err := os.WriteFile(debitTokenFile, []byte(strings.Repeat("d", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	internalTokenFile := filepath.Join(t.TempDir(), "internal-token")
	if err := os.WriteFile(internalTokenFile, []byte(strings.Repeat("i", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	oldClientFactory := paymentLiveHTTPClient
	paymentLiveHTTPClient = func() *http.Client { return client }
	t.Cleanup(func() { paymentLiveHTTPClient = oldClientFactory })
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	runID := "unit-payment-live-success"
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(workspace, ".artifacts", "test-runs", runID)) })
	if err := runTestPaymentLive([]string{
		"--profile", "staging-live", "--run-id", runID, "--run",
		"--account-manager-base-url", "https://account-manager.video-cloud-staging.realtekconnect.com",
		"--billing-base-url", "https://billing.video-cloud-staging.realtekconnect.com",
		"--org-id", "org-test", "--billing-token-file", tokenFile, "--internal-token-file", internalTokenFile, "--debit-token-file", debitTokenFile,
		"--confirm", paymentLiveConfirmation, "--confirm-test-org", "org-test", "--timeout", "10s",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentLiveCommandAndHTTPFailuresFailClosed(t *testing.T) {
	if err := runTestPaymentLive([]string{"--profile", "wrong"}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported profile error = %v", err)
	}
	if err := runTestPaymentLive([]string{"--run", "--plan"}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("conflicting mode error = %v", err)
	}
	shortToken := filepath.Join(t.TempDir(), "billing-token")
	if err := os.WriteFile(shortToken, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	validDebitToken := filepath.Join(t.TempDir(), "debit-token")
	if err := os.WriteFile(validDebitToken, []byte(strings.Repeat("d", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	validInternalToken := filepath.Join(t.TempDir(), "internal-token")
	if err := os.WriteFile(validInternalToken, []byte(strings.Repeat("i", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runTestPaymentLive([]string{
		"--run", "--run-id", "short-token",
		"--account-manager-base-url", "https://account-manager.video-cloud-staging.realtekconnect.com",
		"--billing-base-url", "https://billing.video-cloud-staging.realtekconnect.com",
		"--org-id", "org-test", "--billing-token-file", shortToken, "--internal-token-file", validInternalToken, "--debit-token-file", validDebitToken,
		"--confirm", paymentLiveConfirmation, "--confirm-test-org", "org-test", "--timeout", "10s",
	})
	if err == nil || !strings.Contains(err.Error(), "implausibly short") {
		t.Fatalf("short token error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			http.Error(w, "unsafe provider detail", http.StatusServiceUnavailable)
		case "/invalid":
			_, _ = w.Write([]byte("not-json"))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()
	client := server.Client()
	if err := paymentLiveJSON(context.Background(), client, http.MethodPost, server.URL, "token", nil, make(chan int), nil); err == nil {
		t.Fatal("unencodable request body passed")
	}
	if err := paymentLiveJSON(context.Background(), client, http.MethodGet, ":", "token", nil, nil, nil); err == nil {
		t.Fatal("invalid endpoint passed")
	}
	if err := paymentLiveJSON(context.Background(), client, http.MethodGet, server.URL+"/status", "token", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("HTTP status error = %v", err)
	}
	var output map[string]any
	if err := paymentLiveJSON(context.Background(), client, http.MethodGet, server.URL+"/invalid", "token", nil, nil, &output); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("invalid JSON error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := paymentLiveJSON(canceled, client, http.MethodGet, server.URL, "token", nil, nil, nil); err == nil {
		t.Fatal("canceled request passed")
	}
}

func TestExecutePaymentLiveRejectsUnsafePreflightStates(t *testing.T) {
	provider := `{"name":"simulator","environment":"simulated","capabilities":{"hosted_setup":true,"merchant_initiated_charge":true}}`
	tests := []struct {
		name    string
		account string
		methods string
		setup   string
		want    string
	}{
		{name: "provider unavailable", account: `{"payment_providers":[]}`, want: "simulator provider"},
		{name: "policy already enabled", account: `{"payment_providers":[` + provider + `],"auto_topup":{"enabled":true}}`, want: "already has an enabled"},
		{name: "active method", account: `{"payment_providers":[` + provider + `]}`, methods: `{"payment_methods":[{"id":"method-1","status":"active"}]}`, want: "non-revoked"},
		{name: "unsafe hosted URL", account: `{"payment_providers":[` + provider + `]}`, methods: `{"payment_methods":[{"id":"old","status":"revoked"}]}`, setup: `{"hosted_url":"https://evil.example.test","payment_method":{"id":"method-1"}}`, want: "unsafe URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/internal/billing/access/org-test" || r.URL.Path == "/v1/internal/billing/debits" || (strings.HasPrefix(r.URL.Path, "/v1/orgs/org-test/") && r.Header.Get("Authorization") != "Bearer "+strings.Repeat("b", 32)) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/v1/orgs/org-test/billing/account":
					_, _ = w.Write([]byte(tt.account))
				case "/v1/orgs/org-test/billing/ledger":
					_, _ = w.Write([]byte(`{"ledger_entries":[]}`))
				case "/v1/orgs/org-test/payment-methods":
					_, _ = w.Write([]byte(tt.methods))
				case "/v1/orgs/org-test/payment-methods/setup":
					_, _ = w.Write([]byte(tt.setup))
				default:
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			cfg := paymentLiveConfig{RunID: "unsafe", BillingBaseURL: server.URL, OrgID: "org-test", Timeout: time.Second}
			err := executePaymentLive(context.Background(), server.Client(), t.TempDir(), t.TempDir(), cfg, strings.Repeat("b", 32), strings.Repeat("i", 32), strings.Repeat("d", 32), &paymentLiveState{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPaymentLiveCleanupAndRedactionFailuresAreReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := cleanupPaymentLive(context.Background(), server.Client(), paymentLiveConfig{BillingBaseURL: server.URL, OrgID: "org-test", RunID: "cleanup"}, "token", paymentLiveState{MethodID: "method-1", PolicyVersion: 2})
	if err == nil || !strings.Contains(err.Error(), "disable policy") || !strings.Contains(err.Error(), "revoke method") {
		t.Fatalf("cleanup error = %v", err)
	}

	outDir := t.TempDir()
	evidenceDir := filepath.Join(outDir, "evidence")
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"desktop", "mobile"} {
		if err := os.WriteFile(filepath.Join(evidenceDir, "LIVE-STG-SIMULATOR-001@"+target+".png"), []byte("safe png"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "unsafe.log"), []byte("Authorization: Bearer "+strings.Repeat("s", 40)), 0o644); err != nil {
		t.Fatal(err)
	}
	report := paymentEvidenceReport{RunID: "redaction", Profile: "staging-live", Environment: "staging", Status: "PASS", Cases: []paymentEvidenceCase{{TestID: "LIVE-STG-SIMULATOR-001", Status: "PASS"}}}
	if err := writePaymentLiveReports(outDir, report, nil); err == nil || !strings.Contains(err.Error(), "redaction") {
		t.Fatalf("redaction error = %v", err)
	}
}
