package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPaymentLiveBillingRequestUsesServiceIdentityAndExactPermission(t *testing.T) {
	owner := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+strings.Repeat("b", 32) {
			t.Errorf("authorization = %q", got)
		}
		if got := request.Header.Get("X-Billing-Permissions"); got != "billing_account.read" {
			t.Errorf("permission = %q", got)
		}
		if request.Header.Get("X-Billing-Actor-Type") != "user" || request.Header.Get("X-Billing-Actor-ID") != owner {
			t.Error("missing trusted brand-cloud actor identity")
		}
		if request.Header.Get("X-Billing-Ownership-Version") != "7" {
			t.Errorf("ownership version = %q", request.Header.Get("X-Billing-Ownership-Version"))
		}
		if request.Header.Get("X-Request-ID") == "" {
			t.Error("missing trusted request context")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	var output map[string]any
	if err := paymentLiveBillingJSON(context.Background(), server.Client(), paymentLiveConfig{OwnerUserID: owner, OwnershipVersion: 7}, http.MethodGet, server.URL, strings.Repeat("b", 32), "billing_account.read", nil, nil, &output); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentLiveBillingAccountWaitsOnlyForProjectionAvailability(t *testing.T) {
	t.Run("eventual projection", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			if requests == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"account":{"available_balance_minor":0}}`))
		}))
		defer server.Close()
		cfg := paymentLiveConfig{OwnerUserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OwnershipVersion: 1, Timeout: 3 * time.Second}
		var account map[string]any
		if err := waitPaymentLiveBillingAccount(context.Background(), server.Client(), cfg, server.URL, "token", &account); err != nil {
			t.Fatal(err)
		}
		if requests != 2 || nestedInt64(account["account"], "available_balance_minor") != 0 {
			t.Fatalf("requests=%d account=%+v", requests, account)
		}
	})

	t.Run("invalid ownership is not retried", func(t *testing.T) {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer server.Close()
		cfg := paymentLiveConfig{OwnerUserID: "invalid", OwnershipVersion: 1, Timeout: 3 * time.Second}
		var account map[string]any
		err := waitPaymentLiveBillingAccount(context.Background(), server.Client(), cfg, server.URL, "token", &account)
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") || requests != 1 {
			t.Fatalf("requests=%d err=%v", requests, err)
		}
	})
}

func TestPaymentLiveScreenshotUsesInstalledChromiumAndExplicitViewport(t *testing.T) {
	args := strings.Join(paymentLiveScreenshotArgs("390,844", "https://example.test", "mobile.png"), " ")
	if !strings.Contains(args, "--browser=chromium") || !strings.Contains(args, "--viewport-size=390,844") || strings.Contains(args, "--device=") {
		t.Fatalf("unexpected screenshot args: %s", args)
	}
}

func TestPaymentLiveQualificationUsageIDIsStableAndRunScoped(t *testing.T) {
	first := paymentLiveQualificationUsageID("run-1", "org-1")
	if first != paymentLiveQualificationUsageID("run-1", "org-1") {
		t.Fatal("qualification usage ID must be stable within one run")
	}
	if first == paymentLiveQualificationUsageID("run-2", "org-1") {
		t.Fatal("qualification usage ID must differ across runs")
	}
	if first == paymentLiveQualificationUsageID("run-1", "org-2") {
		t.Fatal("qualification usage ID must differ across organizations")
	}
	if !strings.HasPrefix(first, "qualification-") || len(first) != len("qualification-")+24 {
		t.Fatalf("unexpected qualification usage ID shape %q", first)
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
	cfg.CloudAdminBaseURL = "https://admin.video-cloud-staging.realtekconnect.com"
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), "provided together") {
		t.Fatalf("Cloud Admin URL without a session file must fail, got %v", err)
	}
	cfg.CustomerSessionFile = filepath.Join(t.TempDir(), "customer-session")
	if err := validatePaymentLiveConfig(cfg); err != nil {
		t.Fatalf("safe ephemeral session configuration rejected: %v", err)
	}
	cfg.OrgID = "unexpected"
	if err := validatePaymentLiveConfig(cfg); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("bootstrap with arbitrary org must fail, got %v", err)
	}
}

func TestPaymentLiveWritesProtectedEphemeralCustomerSession(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		http.SetCookie(w, &http.Cookie{Name: "rtk_admin_session", Value: strings.Repeat("s", 32), Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "private", "customer-session")
	if err := writePaymentLiveCustomerSession(context.Background(), server.Client(), server.URL, "billing@example.com", "secret", path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode = %o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != strings.Repeat("s", 32) {
		t.Fatal("session file did not contain the session cookie")
	}
}

func TestPaymentLiveGeneratesStrongTemporaryPassword(t *testing.T) {
	password, err := paymentLiveRandomPassword()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(password, "Q!") || len(password) != 50 {
		t.Fatalf("unexpected generated password shape: prefix=%v length=%d", strings.HasPrefix(password, "Q!"), len(password))
	}
}

func TestPaymentLiveQualificationCustomerSecretRoundTripContract(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "runtime")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(envRoot, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte("CLOUD_PROVIDER=lke\nCLOUD_STACK_NAME=qualification-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "state", "kubeconfig.yaml"), []byte("safe-test-kubeconfig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	applyPath := filepath.Join(binDir, "applied.json")
	kubectl := filepath.Join(binDir, "kubectl")
	writeExecutable(t, kubectl, `#!/bin/sh
if [ "$*" = "--kubeconfig `+filepath.Join(envRoot, "state", "kubeconfig.yaml")+` --request-timeout=5s get --raw=/readyz" ]; then
  exit 0
fi
if echo "$*" | grep -q "get secret billing-qualification-customer"; then
  printf '%s' "$PAYMENT_LIVE_TEST_SECRET"
  exit "${PAYMENT_LIVE_TEST_GET_EXIT:-0}"
fi
if echo "$*" | grep -q "apply -f -"; then
  cat > "$PAYMENT_LIVE_TEST_APPLY_PATH"
  exit "${PAYMENT_LIVE_TEST_APPLY_EXIT:-0}"
fi
exit 1
`)
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PAYMENT_LIVE_TEST_APPLY_PATH", applyPath)
	t.Setenv("CLOUD_PROVIDER", "lke")

	encode := func(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }
	secret := fmt.Sprintf(`{"data":{"EMAIL":%q,"PASSWORD":%q,"ORGANIZATION_ID":%q}}`, encode("billing@example.test"), encode("Q!qualification-password"), encode("org-qualified"))
	t.Setenv("PAYMENT_LIVE_TEST_SECRET", secret)
	customer, found, err := loadPaymentLiveQualificationCustomer(workspace, envRoot)
	if err != nil || !found {
		t.Fatalf("load qualification customer: found=%t err=%v", found, err)
	}
	if customer.Email != "billing@example.test" || customer.Password != "Q!qualification-password" || customer.OrganizationID != "org-qualified" {
		t.Fatalf("unexpected qualification customer: %+v", customer)
	}

	t.Setenv("PAYMENT_LIVE_TEST_SECRET", "")
	if _, found, err := loadPaymentLiveQualificationCustomer(workspace, envRoot); err != nil || found {
		t.Fatalf("missing qualification customer: found=%t err=%v", found, err)
	}

	t.Setenv("PAYMENT_LIVE_TEST_SECRET", secret)
	if err := savePaymentLiveQualificationCustomer(workspace, envRoot, customer); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(applyPath)
	if err != nil {
		t.Fatal(err)
	}
	var applied map[string]any
	if err := json.Unmarshal(raw, &applied); err != nil {
		t.Fatalf("saved secret is not JSON: %v", err)
	}
	metadata := nestedMap(applied["metadata"])
	stringData := nestedMap(applied["stringData"])
	if metadata["namespace"] != "qualification-test-billing" || stringData["EMAIL"] != customer.Email || stringData["ORGANIZATION_ID"] != customer.OrganizationID {
		t.Fatalf("unexpected saved qualification secret: %+v", applied)
	}
	if err := savePaymentLiveQualificationCustomer(workspace, envRoot, paymentLiveQualificationCustomer{}); err == nil {
		t.Fatal("empty qualification customer unexpectedly saved")
	}
}

func TestPaymentLiveQualificationCustomerSecretFailsClosed(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := paymentLiveQualificationKubeTarget(workspace, ""); err == nil || !strings.Contains(err.Error(), "--env-root") {
		t.Fatalf("missing env root error = %v", err)
	}
	t.Setenv("CLOUD_PROVIDER", "docker")
	if _, _, err := paymentLiveQualificationKubeTarget(workspace, envRoot); err == nil || !strings.Contains(err.Error(), "CLOUD_PROVIDER=lke") {
		t.Fatalf("non-LKE provider error = %v", err)
	}
}

func TestPaymentLiveCustomerSessionRejectsLoginFailures(t *testing.T) {
	if err := writePaymentLiveCustomerSession(context.Background(), http.DefaultClient, "://invalid", "billing@example.com", "secret", filepath.Join(t.TempDir(), "session")); err == nil {
		t.Fatal("invalid Cloud Admin login URL must fail")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writePaymentLiveCustomerSession(canceled, http.DefaultClient, "https://admin.example.invalid", "billing@example.com", "secret", filepath.Join(t.TempDir(), "session")); err == nil || !strings.Contains(err.Error(), "customer login") {
		t.Fatalf("canceled Cloud Admin login must fail safely, got %v", err)
	}

	for _, test := range []struct {
		name       string
		status     int
		wantStatus bool
	}{
		{name: "HTTP rejection", status: http.StatusUnauthorized, wantStatus: true},
		{name: "missing session cookie", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			err := writePaymentLiveCustomerSession(context.Background(), server.Client(), server.URL, "billing@example.com", "secret", filepath.Join(t.TempDir(), "session"))
			if err == nil {
				t.Fatal("login without a valid session must fail")
			}
			if test.wantStatus && !strings.Contains(err.Error(), "HTTP 401") {
				t.Fatalf("HTTP rejection was not preserved: %v", err)
			}
			if !test.wantStatus && !strings.Contains(err.Error(), "no session cookie") {
				t.Fatalf("missing-cookie rejection was not preserved: %v", err)
			}
		})
	}
}

func TestPaymentLiveBootstrapReusesDedicatedBrandCloudAndMintsCustomerSession(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "rtk_admin_session", Value: strings.Repeat("s", 32)})
			w.WriteHeader(http.StatusOK)
		case "/api/me/active-org":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected login path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	oldContext := paymentLiveAccountManagerContext
	oldLogin := paymentLiveAccountLoginSession
	oldSecret := paymentLiveRuntimeSecretValue
	oldList := paymentLiveAccountListClouds
	oldCreateUser := paymentLiveAccountCreateUser
	oldPassword := paymentLiveGeneratePassword
	oldLoadCustomer := paymentLiveLoadCustomer
	oldSaveCustomer := paymentLiveSaveCustomer
	oldEnsureCustomer := paymentLiveEnsureCustomer
	oldCreateCustomerOrg := paymentLiveCreateCustomerOrg
	oldClient := paymentLiveHTTPClient
	t.Cleanup(func() {
		paymentLiveAccountManagerContext = oldContext
		paymentLiveAccountLoginSession = oldLogin
		paymentLiveRuntimeSecretValue = oldSecret
		paymentLiveAccountListClouds = oldList
		paymentLiveAccountCreateUser = oldCreateUser
		paymentLiveGeneratePassword = oldPassword
		paymentLiveLoadCustomer = oldLoadCustomer
		paymentLiveSaveCustomer = oldSaveCustomer
		paymentLiveEnsureCustomer = oldEnsureCustomer
		paymentLiveCreateCustomerOrg = oldCreateCustomerOrg
		paymentLiveHTTPClient = oldClient
	})

	closed := false
	paymentLiveAccountManagerContext = func(workspace, envRoot string) (accountManagerContext, error) {
		if workspace != "/workspace" || envRoot != "/staging" {
			t.Fatalf("unexpected bootstrap roots %q %q", workspace, envRoot)
		}
		return accountManagerContext{cleanup: func() { closed = true }}, nil
	}
	paymentLiveAccountLoginSession = func(ctx accountManagerContext, logf func(string, ...any)) (accountPlatformSession, error) {
		if ctx.BaseURL != "https://account-manager.video-cloud-staging.realtekconnect.com" {
			t.Fatalf("bootstrap did not pin Account Manager URL: %q", ctx.BaseURL)
		}
		return accountPlatformSession{AccessToken: "platform-token"}, nil
	}
	paymentLiveRuntimeSecretValue = func(_, _, namespaceSuffix, secretName, key string) (string, error) {
		if namespaceSuffix != "-billing" || secretName != "billing-runtime" {
			t.Fatalf("unexpected runtime secret source %q %q", namespaceSuffix, secretName)
		}
		return "secret-" + key, nil
	}
	paymentLiveAccountListClouds = func(accountManagerContext, string, int) (map[string]any, error) {
		return map[string]any{"brand_clouds": []any{
			map[string]any{"id": "ignored", "name": "Another Brand Cloud"},
			map[string]any{"id": "qualification-org", "name": paymentLiveBootstrapOrgName},
		}}, nil
	}
	paymentLiveGeneratePassword = func() (string, error) { return "Q!temporary-password", nil }
	paymentLiveAccountCreateUser = func(_ accountManagerContext, session *accountPlatformSession, _ func(string, ...any), orgID, email, displayName, password, role string, rotate bool) (accountCreateUserResult, error) {
		if session.AccessToken != "platform-token" || orgID != "qualification-org" || email != paymentLiveBootstrapCustomerEmail || displayName != "Billing Qualification" || password != "Q!temporary-password" || role != "member" || !rotate {
			t.Fatalf("unexpected qualification customer request: org=%q email=%q display=%q role=%q rotate=%v", orgID, email, displayName, role, rotate)
		}
		return accountCreateUserResult{Action: "rotated"}, nil
	}
	paymentLiveHTTPClient = func() *http.Client { return server.Client() }
	paymentLiveLoadCustomer = func(workspace, envRoot string) (paymentLiveQualificationCustomer, bool, error) {
		return paymentLiveQualificationCustomer{Email: paymentLiveBootstrapCustomerEmail, Password: "Q!temporary-password", OrganizationID: "qualification-org"}, true, nil
	}
	paymentLiveSaveCustomer = func(string, string, paymentLiveQualificationCustomer) error { return nil }
	paymentLiveEnsureCustomer = func(_ context.Context, _ *http.Client, _ string, customer paymentLiveQualificationCustomer) (paymentLiveQualificationCustomer, error) {
		customer.AccessToken = "customer-token"
		return customer, nil
	}
	paymentLiveCreateCustomerOrg = func(_ context.Context, _ *http.Client, _ string, token, name string) (paymentLiveBrandCloud, error) {
		if token != "customer-token" || name != paymentLiveBootstrapOrgName+" unit-reuse" {
			t.Fatalf("unexpected run cloud request: token=%q name=%q", token, name)
		}
		return paymentLiveBrandCloud{ID: "run-org", OwnerUserID: "owner-user", OwnershipVersion: 3}, nil
	}

	sessionFile := filepath.Join(t.TempDir(), "session", "customer")
	cfg := paymentLiveConfig{
		RunID:   "unit-reuse",
		EnvRoot: "/staging", AccountManagerBaseURL: "https://account-manager.video-cloud-staging.realtekconnect.com",
		CloudAdminBaseURL: server.URL, CustomerSessionFile: sessionFile,
	}
	got, accountToken, billingToken, internalToken, debitToken, err := bootstrapPaymentLiveOrganization("/workspace", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("qualification bootstrap did not close platform-admin context")
	}
	if got.OrgID != "run-org" || got.OwnerUserID != "owner-user" || got.OwnershipVersion != 3 || accountToken != "customer-token" || billingToken != "secret-BILLING_SERVICE_TOKEN" || internalToken != "secret-BILLING_INTERNAL_TOKEN" || debitToken != "secret-BILLING_DEBIT_TOKEN" {
		t.Fatalf("unexpected bootstrap result: org=%q account=%q billing=%q internal=%q debit=%q", got.OrgID, accountToken, billingToken, internalToken, debitToken)
	}
	if raw, err := os.ReadFile(sessionFile); err != nil || string(raw) != strings.Repeat("s", 32) {
		t.Fatalf("ephemeral customer session was not written safely: value=%q err=%v", raw, err)
	}
}

func TestPaymentLiveBootstrapCreatesMissingDedicatedBrandCloud(t *testing.T) {
	oldContext := paymentLiveAccountManagerContext
	oldLogin := paymentLiveAccountLoginSession
	oldSecret := paymentLiveRuntimeSecretValue
	oldList := paymentLiveAccountListClouds
	oldCreateUser := paymentLiveAccountCreateUser
	oldLoadCustomer := paymentLiveLoadCustomer
	oldSaveCustomer := paymentLiveSaveCustomer
	oldEnsureCustomer := paymentLiveEnsureCustomer
	oldPassword := paymentLiveGeneratePassword
	oldCreateCustomerOrg := paymentLiveCreateCustomerOrg
	t.Cleanup(func() {
		paymentLiveAccountManagerContext = oldContext
		paymentLiveAccountLoginSession = oldLogin
		paymentLiveRuntimeSecretValue = oldSecret
		paymentLiveAccountListClouds = oldList
		paymentLiveAccountCreateUser = oldCreateUser
		paymentLiveLoadCustomer = oldLoadCustomer
		paymentLiveSaveCustomer = oldSaveCustomer
		paymentLiveEnsureCustomer = oldEnsureCustomer
		paymentLiveGeneratePassword = oldPassword
		paymentLiveCreateCustomerOrg = oldCreateCustomerOrg
	})
	paymentLiveAccountManagerContext = func(string, string) (accountManagerContext, error) { return accountManagerContext{}, nil }
	paymentLiveAccountLoginSession = func(accountManagerContext, func(string, ...any)) (accountPlatformSession, error) {
		return accountPlatformSession{AccessToken: "platform-token"}, nil
	}
	paymentLiveRuntimeSecretValue = func(_, _, _, _, key string) (string, error) { return key, nil }
	paymentLiveAccountListClouds = func(accountManagerContext, string, int) (map[string]any, error) {
		return map[string]any{"brand_clouds": []any{}}, nil
	}
	paymentLiveLoadCustomer = func(string, string) (paymentLiveQualificationCustomer, bool, error) {
		return paymentLiveQualificationCustomer{}, false, nil
	}
	saves := 0
	paymentLiveSaveCustomer = func(string, string, paymentLiveQualificationCustomer) error { saves++; return nil }
	paymentLiveGeneratePassword = func() (string, error) { return "Q!temporary-password", nil }
	paymentLiveEnsureCustomer = func(_ context.Context, _ *http.Client, _ string, customer paymentLiveQualificationCustomer) (paymentLiveQualificationCustomer, error) {
		customer.OrganizationID = "created-org"
		customer.AccessToken = "customer-token"
		return customer, nil
	}
	paymentLiveAccountCreateUser = func(_ accountManagerContext, session *accountPlatformSession, _ func(string, ...any), orgID, email, displayName, password, role string, rotate bool) (accountCreateUserResult, error) {
		if session.AccessToken != "platform-token" || orgID != "bootstrap-org" || email != paymentLiveBootstrapCustomerEmail || displayName != "Billing Qualification" || password != "Q!temporary-password" || role != "member" || !rotate {
			t.Fatalf("unexpected audited member creation: org=%q email=%q role=%q", orgID, email, role)
		}
		return accountCreateUserResult{Action: "created"}, nil
	}
	paymentLiveCreateCustomerOrg = func(_ context.Context, _ *http.Client, _ string, token, name string) (paymentLiveBrandCloud, error) {
		switch {
		case token == "platform-token" && name == paymentLiveBootstrapOrgName:
			return paymentLiveBrandCloud{ID: "bootstrap-org", OwnerUserID: "platform-owner", OwnershipVersion: 1}, nil
		case token == "customer-token" && name == paymentLiveBootstrapOrgName+" unit-create":
			return paymentLiveBrandCloud{ID: "run-org", OwnerUserID: "customer-owner", OwnershipVersion: 1}, nil
		default:
			t.Fatalf("unexpected cloud creation: token=%q name=%q", token, name)
			return paymentLiveBrandCloud{}, nil
		}
	}

	cfg, _, _, _, _, err := bootstrapPaymentLiveOrganization("/workspace", paymentLiveConfig{RunID: "unit-create", EnvRoot: "/staging", AccountManagerBaseURL: "https://account-manager.video-cloud-staging.realtekconnect.com"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OrgID != "run-org" || cfg.OwnerUserID != "customer-owner" || cfg.OwnershipVersion != 1 {
		t.Fatalf("created Brand Cloud scope = org:%q owner:%q version:%d", cfg.OrgID, cfg.OwnerUserID, cfg.OwnershipVersion)
	}
	if saves != 2 {
		t.Fatalf("qualification customer secret saves = %d, want 2", saves)
	}
}

func TestPaymentLiveBootstrapFailsClosedAtEveryCredentialAndIdentityStage(t *testing.T) {
	oldContext := paymentLiveAccountManagerContext
	oldLogin := paymentLiveAccountLoginSession
	oldSecret := paymentLiveRuntimeSecretValue
	oldList := paymentLiveAccountListClouds
	oldCreateUser := paymentLiveAccountCreateUser
	oldPassword := paymentLiveGeneratePassword
	oldLoadCustomer := paymentLiveLoadCustomer
	oldSaveCustomer := paymentLiveSaveCustomer
	oldEnsureCustomer := paymentLiveEnsureCustomer
	oldCreateCustomerOrg := paymentLiveCreateCustomerOrg
	t.Cleanup(func() {
		paymentLiveAccountManagerContext = oldContext
		paymentLiveAccountLoginSession = oldLogin
		paymentLiveRuntimeSecretValue = oldSecret
		paymentLiveAccountListClouds = oldList
		paymentLiveAccountCreateUser = oldCreateUser
		paymentLiveGeneratePassword = oldPassword
		paymentLiveLoadCustomer = oldLoadCustomer
		paymentLiveSaveCustomer = oldSaveCustomer
		paymentLiveEnsureCustomer = oldEnsureCustomer
		paymentLiveCreateCustomerOrg = oldCreateCustomerOrg
	})

	for _, test := range []struct {
		stage string
		want  string
	}{
		{stage: "BILLING_SERVICE_TOKEN", want: "Billing service credential"},
		{stage: "BILLING_INTERNAL_TOKEN", want: "Billing internal credential"},
		{stage: "BILLING_DEBIT_TOKEN", want: "Billing debit credential"},
		{stage: "context", want: "load staging platform-admin credentials"},
		{stage: "login", want: "staging platform-admin login"},
		{stage: "list", want: "list qualification bootstrap clouds"},
		{stage: "create-bootstrap", want: "create qualification bootstrap cloud"},
		{stage: "load-customer", want: "load dedicated qualification customer"},
		{stage: "password", want: "password generation failed"},
		{stage: "save-bootstrap", want: "persist qualification customer bootstrap credential"},
		{stage: "user", want: "create audited staging qualification member"},
		{stage: "ensure-customer", want: "ensure dedicated qualification customer"},
		{stage: "save-org", want: "persist qualification customer credential"},
		{stage: "create-org", want: "create run-scoped qualification organization"},
		{stage: "session", want: "missing protocol scheme"},
	} {
		t.Run(test.stage, func(t *testing.T) {
			paymentLiveAccountManagerContext = func(string, string) (accountManagerContext, error) {
				if test.stage == "context" {
					return accountManagerContext{}, errors.New("context failed")
				}
				return accountManagerContext{}, nil
			}
			paymentLiveAccountLoginSession = func(accountManagerContext, func(string, ...any)) (accountPlatformSession, error) {
				if test.stage == "login" {
					return accountPlatformSession{}, errors.New("login failed")
				}
				return accountPlatformSession{AccessToken: "platform-token"}, nil
			}
			paymentLiveRuntimeSecretValue = func(_, _, _, _, key string) (string, error) {
				if test.stage == key {
					return "", errors.New("secret failed")
				}
				return "secret-" + key, nil
			}
			paymentLiveAccountListClouds = func(accountManagerContext, string, int) (map[string]any, error) {
				if test.stage == "list" {
					return nil, errors.New("list failed")
				}
				if test.stage == "create-bootstrap" {
					return map[string]any{"brand_clouds": []any{}}, nil
				}
				organization := map[string]any{"id": "qualification-org", "name": paymentLiveBootstrapOrgName}
				if test.stage == "missing-id" {
					delete(organization, "id")
				}
				return map[string]any{"brand_clouds": []any{organization}}, nil
			}
			paymentLiveGeneratePassword = func() (string, error) {
				if test.stage == "password" {
					return "", errors.New("password generation failed")
				}
				return "Q!temporary-password", nil
			}
			paymentLiveLoadCustomer = func(string, string) (paymentLiveQualificationCustomer, bool, error) {
				if test.stage == "load-customer" {
					return paymentLiveQualificationCustomer{}, false, errors.New("load failed")
				}
				if test.stage == "password" || test.stage == "save-bootstrap" {
					return paymentLiveQualificationCustomer{}, false, nil
				}
				return paymentLiveQualificationCustomer{Email: paymentLiveBootstrapCustomerEmail, Password: "Q!temporary-password", OrganizationID: "qualification-org"}, true, nil
			}
			saveCalls := 0
			paymentLiveSaveCustomer = func(string, string, paymentLiveQualificationCustomer) error {
				saveCalls++
				if test.stage == "save-bootstrap" || (test.stage == "save-org" && saveCalls == 1) {
					return errors.New("save failed")
				}
				return nil
			}
			paymentLiveEnsureCustomer = func(context.Context, *http.Client, string, paymentLiveQualificationCustomer) (paymentLiveQualificationCustomer, error) {
				if test.stage == "ensure-customer" {
					return paymentLiveQualificationCustomer{}, errors.New("ensure failed")
				}
				return paymentLiveQualificationCustomer{Email: paymentLiveBootstrapCustomerEmail, Password: "Q!temporary-password", OrganizationID: "qualification-org", AccessToken: "customer-token"}, nil
			}
			paymentLiveCreateCustomerOrg = func(_ context.Context, _ *http.Client, _ string, _ string, name string) (paymentLiveBrandCloud, error) {
				if test.stage == "create-bootstrap" && name == paymentLiveBootstrapOrgName {
					return paymentLiveBrandCloud{}, errors.New("bootstrap failed")
				}
				if test.stage == "create-org" && name != paymentLiveBootstrapOrgName {
					return paymentLiveBrandCloud{}, errors.New("create failed")
				}
				return paymentLiveBrandCloud{ID: "qualification-org", OwnerUserID: "owner-user", OwnershipVersion: 1}, nil
			}
			paymentLiveAccountCreateUser = func(accountManagerContext, *accountPlatformSession, func(string, ...any), string, string, string, string, string, bool) (accountCreateUserResult, error) {
				if test.stage == "user" {
					return accountCreateUserResult{}, errors.New("user failed")
				}
				return accountCreateUserResult{}, nil
			}

			cfg := paymentLiveConfig{EnvRoot: "/staging", AccountManagerBaseURL: "https://account-manager.video-cloud-staging.realtekconnect.com"}
			if test.stage == "session" {
				cfg.CloudAdminBaseURL = "://invalid"
				cfg.CustomerSessionFile = filepath.Join(t.TempDir(), "session")
			}
			_, _, _, _, _, err := bootstrapPaymentLiveOrganization("/workspace", cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("stage %q: expected %q failure, got %v", test.stage, test.want, err)
			}
		})
	}
}

func TestEnsurePaymentLiveQualificationCustomerRejectsUnauditedPublicSignup(t *testing.T) {
	registerCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			w.WriteHeader(http.StatusUnauthorized)
		case "/v1/auth/register":
			registerCalled = true
			w.WriteHeader(http.StatusTeapot)
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	_, err := ensurePaymentLiveQualificationCustomer(context.Background(), server.Client(), server.URL, paymentLiveQualificationCustomer{Email: paymentLiveBootstrapCustomerEmail, Password: "Q!temporary-password"})
	if err == nil || !strings.Contains(err.Error(), "login audited staging qualification member: HTTP 401") {
		t.Fatalf("unexpected login result: %v", err)
	}
	if registerCalled {
		t.Fatal("qualification bootstrap must not call public register")
	}
}

func TestEnsurePaymentLiveQualificationCustomerUsesGlobalLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/auth/login":
			_, _ = w.Write([]byte(`{"tokens":{"access_token":"customer-access-token"}}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	customer, err := ensurePaymentLiveQualificationCustomer(context.Background(), server.Client(), server.URL, paymentLiveQualificationCustomer{Email: paymentLiveBootstrapCustomerEmail, Password: "Q!temporary-password"})
	if err != nil {
		t.Fatal(err)
	}
	if customer.OrganizationID != "" || customer.AccessToken != "customer-access-token" {
		t.Fatalf("unexpected global customer login: %+v", customer)
	}
}

func TestPaymentLiveQualificationOrganizationAPIFailsClosed(t *testing.T) {
	t.Run("create success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/developer/brand-clouds" || r.Header.Get("Authorization") != "Bearer customer-token" {
				t.Fatalf("unexpected organization request: %s %s", r.Method, r.URL.Path)
			}
			if !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "billing-qualification-cloud-") {
				t.Fatalf("missing stable idempotency key: %q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "Qualification run-1" {
				t.Fatalf("organization name = %q", body["name"])
			}
			if body["description"] == "" {
				t.Fatal("run-scoped cloud description is missing")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"brand_cloud":{"id":"run-org","owner_user_id":"owner-user","ownership_version":7}}`))
		}))
		defer server.Close()
		organization, err := createPaymentLiveQualificationOrganization(context.Background(), server.Client(), server.URL, "customer-token", " Qualification run-1 ")
		if err != nil || organization.ID != "run-org" || organization.OwnerUserID != "owner-user" || organization.OwnershipVersion != 7 {
			t.Fatalf("create organization: cloud=%+v err=%v", organization, err)
		}
	})

	t.Run("create response missing ownership evidence", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"brand_cloud":{"id":"run-org"}}`))
		}))
		defer server.Close()
		_, err := createPaymentLiveQualificationOrganization(context.Background(), server.Client(), server.URL, "customer-token", "Qualification run-invalid")
		if err == nil || !strings.Contains(err.Error(), "exact owner and ownership version") {
			t.Fatalf("missing ownership evidence error = %v", err)
		}
	})

	for _, test := range []struct {
		name, login, organizations       string
		loginStatus, organizationsStatus int
		want                             string
	}{
		{name: "login rejected", loginStatus: http.StatusForbidden, want: "login audited staging qualification member: HTTP 403"},
		{name: "login missing token", loginStatus: http.StatusOK, login: `{"tokens":{}}`, want: "no access token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/auth/login":
					w.WriteHeader(test.loginStatus)
					_, _ = w.Write([]byte(test.login))
				case "/v1/orgs":
					w.WriteHeader(test.organizationsStatus)
					_, _ = w.Write([]byte(test.organizations))
				default:
					t.Fatalf("unexpected path %q", r.URL.Path)
				}
			}))
			defer server.Close()
			_, err := ensurePaymentLiveQualificationCustomer(context.Background(), server.Client(), server.URL, paymentLiveQualificationCustomer{Email: "billing@example.test", Password: "Q!qualification-password"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
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
	cfg := paymentLiveConfig{Run: true, AccountManagerBaseURL: "https://account-manager.video-cloud-staging.realtekconnect.com", BillingBaseURL: "https://billing.video-cloud-staging.realtekconnect.com", OrgID: "org-test", OwnerUserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OwnershipVersion: 1, AccountTokenFile: tokenFile, BillingTokenFile: billingTokenFile, InternalTokenFile: internalTokenFile, DebitTokenFile: debitTokenFile, Timeout: time.Minute}
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
	cfg := paymentLiveConfig{Run: true, Confirm: paymentLiveConfirmation, OrgID: "org-test", OwnerUserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OwnershipVersion: 1, ConfirmTestOrg: "org-test", AccountManagerBaseURL: "http://account-manager.video-cloud-staging.realtekconnect.com", BillingBaseURL: "https://billing.video-cloud-staging.realtekconnect.com", Timeout: time.Minute}
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
		if err := os.WriteFile(filepath.Join(evidenceDir, "LIVE-STG-SIMULATOR-001@newebpay-"+target+".png"), []byte("safe NewebPay synthetic png "+target), 0o644); err != nil {
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
	for _, required := range []string{"execution.log", "evidence/LIVE-STG-SIMULATOR-001@desktop.png", "evidence/LIVE-STG-SIMULATOR-001@mobile.png", "evidence/LIVE-STG-SIMULATOR-001@newebpay-desktop.png", "evidence/LIVE-STG-SIMULATOR-001@newebpay-mobile.png"} {
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

func TestVerifyPaymentLiveVisibleAutoTopUpCreditRejectsUnexpectedDelta(t *testing.T) {
	ledger := map[string]any{"ledger_entries": []any{map[string]any{
		"id":                  "credit-auto",
		"direction":           "credit",
		"reason":              "payment_top_up_credit",
		"amount_minor":        float64(300),
		"balance_after_minor": float64(298),
	}}}
	err := verifyPaymentLiveVisibleAutoTopUpCredit(ledger, map[string]bool{}, 300, 299)
	if err == nil || !strings.Contains(err.Error(), "visible ledger delta invalid") {
		t.Fatalf("unexpected visible automatic top-up delta must fail, got %v", err)
	}
}

func TestExecuteAndCleanupPaymentLiveCompletesSimulatorQualification(t *testing.T) {
	methodActive := false
	debitPosted := false
	manualPosted := false
	hostedPosted := false
	policyVersion := int64(1)
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
		if strings.HasPrefix(r.URL.Path, "/v1/orgs/org-test/") && (r.Header.Get("X-Billing-Actor-ID") != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" || r.Header.Get("X-Billing-Ownership-Version") != "1") {
			t.Fatalf("missing exact owner scope: actor=%q version=%q", r.Header.Get("X-Billing-Actor-ID"), r.Header.Get("X-Billing-Ownership-Version"))
		}
		switch {
		case r.URL.Path == "/v1/orgs/org-test/billing/account":
			_, _ = w.Write([]byte(`{"account":{"available_balance_minor":0},"payment_providers":[{"name":"simulator","environment":"simulated","capabilities":{"hosted_setup":true,"merchant_initiated_charge":true}},{"name":"newebpay","environment":"simulated","capabilities":{"hosted_charge":true,"status_query":true,"webhook":true}}],"auto_topup":null}`))
		case r.URL.Path == "/v1/orgs/org-test/billing/ledger":
			entries := `[]`
			if debitPosted {
				entries = `[{"id":"credit-auto","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":299}]`
			}
			if manualPosted {
				entries = `[{"id":"credit-manual","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":599},{"id":"credit-auto","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":299}]`
			}
			if hostedPosted {
				entries = `[{"id":"credit-hosted","direction":"credit","reason":"payment_top_up_credit","amount_minor":500,"balance_after_minor":1099},{"id":"credit-manual","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":599},{"id":"credit-auto","direction":"credit","reason":"payment_top_up_credit","amount_minor":300,"balance_after_minor":299}]`
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
			_, _ = w.Write([]byte(fmt.Sprintf(`{"auto_topup":{"enabled":true,"version":%d}}`, policyVersion)))
		case r.URL.Path == "/v1/orgs/org-test/auto-topup" && r.Method == http.MethodPut:
			policyVersion = 2
			_, _ = w.Write([]byte(`{"auto_topup":{"version":2}}`))
		case r.URL.Path == "/v1/orgs/org-test/auto-topup" && r.Method == http.MethodDelete:
			if got := r.Header.Get("If-Match"); got != strconv.Quote(strconv.FormatInt(policyVersion, 10)) {
				t.Fatalf("cleanup If-Match = %q, want current version %d", got, policyVersion)
			}
			policyVersion++
			_, _ = w.Write([]byte(`{"auto_topup":{"enabled":false,"version":3}}`))
		case r.URL.Path == "/v1/internal/billing/debits":
			if got := r.Header.Get("Authorization"); got != "Bearer "+strings.Repeat("d", 32) {
				t.Fatalf("debit authorization = %q", got)
			}
			duplicate := debitPosted
			debitPosted = true
			policyVersion = 3
			_, _ = w.Write([]byte(`{"ledger_entry_id":"debit-1","payment_intent_id":"intent-auto","duplicate":` + map[bool]string{true: "true", false: "false"}[duplicate] + `}`))
		case r.URL.Path == "/v1/orgs/org-test/payment-intents/intent-auto":
			_, _ = w.Write([]byte(`{"payment_intent":{"state":"succeeded"},"attempts":[{"operation":"charge","status":"succeeded"}]}`))
		case r.URL.Path == "/v1/orgs/org-test/topups":
			manualPosted = true
			_, _ = w.Write([]byte(`{"payment_intent":{"id":"intent-manual"}}`))
		case r.URL.Path == "/v1/orgs/org-test/payment-intents/intent-manual":
			_, _ = w.Write([]byte(`{"payment_intent":{"state":"succeeded"},"attempts":[{"operation":"charge","status":"succeeded"}]}`))
		case r.URL.Path == "/v1/orgs/org-test/topups/checkout":
			_, _ = w.Write([]byte(`{"payment_intent":{"id":"intent-hosted","state":"processing"},"payment_action":{"method":"POST","url":"https://payment-simulator.staging.realtekconnect.com/MPG/mpg_gateway","fields":{"MerchantID":"RTKSIMULATOR","TradeInfo":"encrypted","TradeSha":"digest","Version":"2.3"}}}`))
		case r.URL.Path == "/MPG/mpg_gateway":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<!doctype html><meta http-equiv="refresh" content="0;url=https://payment-simulator.staging.realtekconnect.com/newebpay/pay/token-1">`))
		case r.URL.Path == "/newebpay/pay/token-1" && r.Method == http.MethodPost:
			hostedPosted = true
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<!doctype html><p>TEST payment success</p>`))
		case r.URL.Path == "/v1/orgs/org-test/payment-intents/intent-hosted":
			_, _ = w.Write([]byte(`{"payment_intent":{"state":"succeeded"},"attempts":[{"operation":"query","status":"succeeded"}]}`))
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
	cfg := paymentLiveConfig{RunID: "live-success", BillingBaseURL: server.URL, OrgID: "org-test", OwnerUserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OwnershipVersion: 1, Timeout: time.Second}
	state := paymentLiveState{}
	if err := executePaymentLive(context.Background(), client, t.TempDir(), outDir, cfg, strings.Repeat("b", 32), strings.Repeat("i", 32), strings.Repeat("d", 32), &state); err != nil {
		t.Fatal(err)
	}
	if !hostedPosted {
		t.Fatal("hosted NewebPay checkout was not completed")
	}
	if state.MethodID != "method-1" || state.AutoIntentID != "intent-auto" || state.ManualIntentID != "intent-manual" || state.HostedIntentID != "intent-hosted" || state.PolicyVersion != 2 || !state.HostedSetupPassed || !state.NewebPayHostedPassed || !state.AutoTopUpPassed || !state.ManualTopUpPassed {
		t.Fatalf("unexpected qualification state: %+v", state)
	}
	if err := cleanupPaymentLive(context.Background(), client, cfg, strings.Repeat("b", 32), state); err != nil {
		t.Fatal(err)
	}
	debitPosted = false
	manualPosted = false
	hostedPosted = false

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
		"--org-id", "org-test", "--owner-user-id", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "--ownership-version", "1", "--billing-token-file", tokenFile, "--internal-token-file", internalTokenFile, "--debit-token-file", debitTokenFile,
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
		"--org-id", "org-test", "--owner-user-id", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "--ownership-version", "1", "--billing-token-file", shortToken, "--internal-token-file", validInternalToken, "--debit-token-file", validDebitToken,
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
			cfg := paymentLiveConfig{RunID: "unsafe", BillingBaseURL: server.URL, OrgID: "org-test", OwnerUserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OwnershipVersion: 1, Timeout: time.Second}
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
	err := cleanupPaymentLive(context.Background(), server.Client(), paymentLiveConfig{BillingBaseURL: server.URL, OrgID: "org-test", OwnerUserID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OwnershipVersion: 1, RunID: "cleanup"}, "token", paymentLiveState{MethodID: "method-1", PolicyVersion: 2})
	if err == nil || !strings.Contains(err.Error(), "read policy for cleanup") || !strings.Contains(err.Error(), "revoke method") {
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
