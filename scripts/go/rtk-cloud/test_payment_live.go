package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const paymentLiveConfirmation = "video-cloud-staging-lke"
const paymentLiveBootstrapConfirmation = "rtk-payment-simulator-qualification"
const paymentLiveBootstrapOrgName = "RTK Payment Simulator Qualification"

type paymentLiveConfig struct {
	RunID, AccountManagerBaseURL, BillingBaseURL, OrgID         string
	AccountTokenFile, BillingTokenFile, Confirm, ConfirmTestOrg string
	EnvRoot                                                     string
	Run, Plan, BootstrapTestOrg                                 bool
	Timeout                                                     time.Duration
}

type paymentLiveState struct {
	MethodID      string
	PolicyVersion int64
	IntentID      string
}

var paymentLiveScreenshot = func(workdir, target, targetURL, output string) error {
	device := "Desktop Chrome"
	if target == "mobile" {
		device = "iPhone 13"
	}
	cmd := exec.Command("npx", "playwright", "screenshot", "--device="+device, "--wait-for-timeout=1000", targetURL, output)
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var paymentLiveHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

func runTestPaymentLive(args []string) error {
	fs := flag.NewFlagSet("test-payment staging-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	profile := fs.String("profile", "staging-live", "payment profile")
	runID := fs.String("run-id", "", "artifact run ID")
	accountManagerBaseURL := fs.String("base-url", os.Getenv("ACCOUNT_MANAGER_PUBLIC_URL"), "staging Account Manager base URL (deprecated alias)")
	fs.StringVar(accountManagerBaseURL, "account-manager-base-url", os.Getenv("ACCOUNT_MANAGER_PUBLIC_URL"), "staging Account Manager base URL")
	billingBaseURL := fs.String("billing-base-url", firstNonEmpty(os.Getenv("BILLING_PUBLIC_URL"), "https://billing.video-cloud-staging.realtekconnect.com"), "staging Billing service base URL")
	orgID := fs.String("org-id", os.Getenv("PAYMENT_TEST_ORG_ID"), "dedicated staging test organization")
	accountTokenFile := fs.String("access-token-file", os.Getenv("PAYMENT_TEST_ACCESS_TOKEN_FILE"), "file containing the dedicated Account Manager test access token")
	billingTokenFile := fs.String("billing-token-file", os.Getenv("PAYMENT_TEST_BILLING_TOKEN_FILE"), "file containing the dedicated Billing service token")
	confirm := fs.String("confirm", "", "required staging stack confirmation")
	confirmTestOrg := fs.String("confirm-test-org", "", "must exactly match --org-id")
	bootstrapTestOrg := fs.Bool("bootstrap-test-org", false, "create or reuse the fixed dedicated qualification organization using LKE platform-admin credentials")
	envRoot := fs.String("env-root", "", "staging environment root used only by --bootstrap-test-org")
	run := fs.Bool("run", false, "execute the staging mutation")
	plan := fs.Bool("plan", false, "print the execution plan without mutation")
	timeout := fs.Duration("timeout", 2*time.Minute, "maximum reconciliation wait")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *profile != "staging-live" {
		return fmt.Errorf("unsupported live payment profile %q", *profile)
	}
	if !*run {
		*plan = true
	}
	if *run && *plan {
		return errors.New("choose exactly one of --plan or --run")
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z") + "-payment-live"
	}
	cfg := paymentLiveConfig{RunID: *runID, AccountManagerBaseURL: strings.TrimRight(strings.TrimSpace(*accountManagerBaseURL), "/"), BillingBaseURL: strings.TrimRight(strings.TrimSpace(*billingBaseURL), "/"), OrgID: strings.TrimSpace(*orgID), AccountTokenFile: strings.TrimSpace(*accountTokenFile), BillingTokenFile: strings.TrimSpace(*billingTokenFile), Confirm: strings.TrimSpace(*confirm), ConfirmTestOrg: strings.TrimSpace(*confirmTestOrg), EnvRoot: strings.TrimSpace(*envRoot), Run: *run, Plan: *plan, BootstrapTestOrg: *bootstrapTestOrg, Timeout: *timeout}
	if cfg.Plan {
		fmt.Printf("Payment staging-live plan (%s): preflight -> dedicated organization -> hosted setup -> desktop/mobile screenshots -> activate method -> enable approved defaults -> TWD 300 charge -> reconcile ledger -> disable policy -> revoke method -> redaction/cleanup reports\n", cfg.RunID)
		return nil
	}
	if err := validatePaymentLiveConfig(cfg); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	billingToken := ""
	if cfg.BootstrapTestOrg {
		cfg, _, billingToken, err = bootstrapPaymentLiveOrganization(workspace, cfg)
		if err != nil {
			return err
		}
	} else {
		billingRaw, readErr := os.ReadFile(cfg.BillingTokenFile)
		if readErr != nil {
			return fmt.Errorf("read dedicated Billing service token: %w", readErr)
		}
		billingToken = strings.TrimSpace(string(billingRaw))
		if len(billingToken) < 24 {
			return errors.New("dedicated Billing token is empty or implausibly short")
		}
	}
	outDir := filepath.Join(workspace, ".artifacts", "test-runs", cfg.RunID, "payments", "staging-live")
	if err := os.MkdirAll(filepath.Join(outDir, "evidence"), 0o755); err != nil {
		return err
	}
	started := time.Now().UTC()
	state := paymentLiveState{}
	caseResult := paymentEvidenceCase{TestID: "LIVE-STG-SIMULATOR-001", Purpose: "Qualify the deployed virtual payment flow with hosted UI evidence and a reconciled TWD charge.", Method: "Dedicated staging organization performs simulator hosted setup, desktop/mobile viewport capture, policy activation with approved defaults, a TWD 300 manual charge, ledger reconciliation, and mandatory cleanup.", StartedAt: started.Format(time.RFC3339), Status: "PASS"}
	client := paymentLiveHTTPClient()
	runErr := executePaymentLive(context.Background(), client, workspace, outDir, cfg, billingToken, &state)
	cleanupErr := cleanupPaymentLive(context.Background(), client, cfg, billingToken, state)
	completed := time.Now().UTC()
	caseResult.CompletedAt = completed.Format(time.RFC3339)
	caseResult.DurationMS = completed.Sub(started).Milliseconds()
	caseResult.Assessment = "Deployed simulator setup, responsive hosted UI, safe policy defaults, payment execution, and ledger reconciliation passed."
	if runErr != nil {
		caseResult.Status = "FAIL"
		caseResult.Assessment = runErr.Error()
	}
	if cleanupErr != nil {
		caseResult.Status = "FAIL"
		caseResult.Assessment = strings.TrimSpace(caseResult.Assessment + "; cleanup: " + cleanupErr.Error())
	}
	workspaceCommit, _ := gitOutput(workspace, "rev-parse", "HEAD")
	serviceCommit, _ := gitOutput(filepath.Join(workspace, "repos", "rtk_billing"), "rev-parse", "HEAD")
	report := paymentEvidenceReport{SchemaVersion: 1, RunID: cfg.RunID, Profile: "staging-live", Environment: "staging", WorkspaceCommit: strings.TrimSpace(workspaceCommit), ServiceCommit: strings.TrimSpace(serviceCommit), StartedAt: started.Format(time.RFC3339), CompletedAt: completed.Format(time.RFC3339), DurationMS: completed.Sub(started).Milliseconds(), Status: caseResult.Status, Assessment: caseResult.Assessment, CoverageGate: "N/A", Cases: []paymentEvidenceCase{caseResult}}
	if err := writePaymentLiveReports(outDir, report, cleanupErr); err != nil {
		return err
	}
	if report.Status != "PASS" {
		return fmt.Errorf("payment staging-live qualification failed: %s", report.Assessment)
	}
	fmt.Printf("Payment staging-live report: %s\n", filepath.Join(outDir, "TEST_REPORT.md"))
	return nil
}

func validatePaymentLiveConfig(cfg paymentLiveConfig) error {
	if cfg.Confirm != paymentLiveConfirmation {
		return fmt.Errorf("--run requires --confirm %s", paymentLiveConfirmation)
	}
	if cfg.BootstrapTestOrg {
		if cfg.OrgID != "" || cfg.AccountTokenFile != "" || cfg.BillingTokenFile != "" {
			return errors.New("--bootstrap-test-org cannot be combined with --org-id or --access-token-file")
		}
		if cfg.ConfirmTestOrg != paymentLiveBootstrapConfirmation {
			return fmt.Errorf("--bootstrap-test-org requires --confirm-test-org %s", paymentLiveBootstrapConfirmation)
		}
		if cfg.EnvRoot == "" {
			return errors.New("--bootstrap-test-org requires --env-root")
		}
	} else if cfg.OrgID == "" || cfg.ConfirmTestOrg != cfg.OrgID {
		return errors.New("--run requires a dedicated --org-id and an exact matching --confirm-test-org")
	}
	parsed, err := url.Parse(cfg.AccountManagerBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || strings.ToLower(parsed.Hostname()) != "account-manager.video-cloud-staging.realtekconnect.com" {
		return errors.New("--base-url must be the approved HTTPS Account Manager staging endpoint")
	}
	billingParsed, billingErr := url.Parse(cfg.BillingBaseURL)
	if billingErr != nil || billingParsed.Scheme != "https" || billingParsed.Host == "" || billingParsed.User != nil || strings.ToLower(billingParsed.Hostname()) != "billing.video-cloud-staging.realtekconnect.com" {
		return errors.New("--billing-base-url must be the approved HTTPS Billing staging endpoint")
	}
	if !cfg.BootstrapTestOrg && cfg.BillingTokenFile == "" {
		return errors.New("--billing-token-file is required; raw token command-line arguments are intentionally unsupported")
	}
	if !cfg.BootstrapTestOrg {
		paths := []string{cfg.BillingTokenFile}
		if cfg.AccountTokenFile != "" {
			paths = append(paths, cfg.AccountTokenFile)
		}
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() || info.Mode().Perm()&0o077 != 0 {
				return errors.New("token files must exist, be regular files, and not be group/world accessible")
			}
		}
	}
	if cfg.Timeout < 10*time.Second || cfg.Timeout > 10*time.Minute {
		return errors.New("--timeout must be between 10s and 10m")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		return errors.New("npx is required for desktop/mobile screenshot evidence")
	}
	return nil
}

func bootstrapPaymentLiveOrganization(workspace string, cfg paymentLiveConfig) (paymentLiveConfig, string, string, error) {
	ctx, err := accountManagerContextFromFlags(workspace, cfg.EnvRoot)
	if err != nil {
		return cfg, "", "", fmt.Errorf("load LKE platform-admin credentials: %w", err)
	}
	defer ctx.Close()
	ctx.BaseURL = cfg.AccountManagerBaseURL
	token, err := accountLogin(ctx, func(string, ...any) {})
	if err != nil {
		return cfg, "", "", fmt.Errorf("staging platform-admin login: %w", err)
	}
	billingToken, err := lkeRuntimeSecretValueFromFlags(workspace, cfg.EnvRoot, "-billing", "billing-runtime", "BILLING_SERVICE_TOKEN")
	if err != nil {
		return cfg, "", "", fmt.Errorf("load LKE Billing service credential: %w", err)
	}
	client := paymentLiveHTTPClient()
	var organizations map[string]any
	if err := paymentLiveJSON(context.Background(), client, http.MethodGet, cfg.AccountManagerBaseURL+"/v1/orgs?limit=100", token, nil, nil, &organizations); err != nil {
		return cfg, "", "", fmt.Errorf("list qualification organizations: %w", err)
	}
	for _, item := range paymentLiveAnySlice(organizations["organizations"]) {
		organization := nestedMap(item)
		name, _ := organization["name"].(string)
		if name != paymentLiveBootstrapOrgName {
			continue
		}
		cfg.OrgID, _ = organization["id"].(string)
		if cfg.OrgID == "" {
			return cfg, "", "", errors.New("dedicated qualification organization has no ID")
		}
		return cfg, token, billingToken, nil
	}
	var created map[string]any
	if err := paymentLiveJSON(context.Background(), client, http.MethodPost, cfg.AccountManagerBaseURL+"/v1/orgs", token, nil, map[string]any{"name": paymentLiveBootstrapOrgName}, &created); err != nil {
		return cfg, "", "", fmt.Errorf("create dedicated qualification organization: %w", err)
	}
	cfg.OrgID, _ = nestedMap(created["organization"])["id"].(string)
	if cfg.OrgID == "" {
		return cfg, "", "", errors.New("created qualification organization has no ID")
	}
	return cfg, token, billingToken, nil
}

func executePaymentLive(ctx context.Context, client *http.Client, workspace, outDir string, cfg paymentLiveConfig, token string, state *paymentLiveState) error {
	base := cfg.BillingBaseURL + "/v1/orgs/" + url.PathEscape(cfg.OrgID)
	var account map[string]any
	if err := paymentLiveBillingJSON(ctx, client, http.MethodGet, base+"/billing/account", token, "billing_account.read", nil, nil, &account); err != nil {
		return fmt.Errorf("billing preflight: %w", err)
	}
	if !paymentSimulatorAvailable(account) {
		return errors.New("billing preflight: simulator provider with hosted_setup and merchant_initiated_charge is unavailable")
	}
	if policy := nestedMap(account["auto_topup"]); len(policy) > 0 && policy["enabled"] == true {
		return errors.New("billing preflight: dedicated test organization already has an enabled automatic top-up policy")
	}
	var existingMethods map[string]any
	if err := paymentLiveBillingJSON(ctx, client, http.MethodGet, base+"/payment-methods", token, "payment_method.read", nil, nil, &existingMethods); err != nil {
		return fmt.Errorf("payment method preflight: %w", err)
	}
	for _, item := range paymentLiveAnySlice(existingMethods["payment_methods"]) {
		if nestedMap(item)["status"] != "revoked" {
			return errors.New("payment method preflight: dedicated test organization contains a non-revoked method")
		}
	}
	setupBody := map[string]any{"provider": "simulator", "consent": map[string]any{"accepted": true, "text_version": "payment-simulator-live-v1", "text_sha256": strings.Repeat("a", 64), "locale": "zh-TW"}}
	var setup map[string]any
	headers := map[string]string{"Idempotency-Key": "setup-" + cfg.RunID, "X-Request-Id": "setup-" + cfg.RunID}
	if err := paymentLiveBillingJSON(ctx, client, http.MethodPost, base+"/payment-methods/setup", token, "payment_method.manage", headers, setupBody, &setup); err != nil {
		return fmt.Errorf("hosted setup: %w", err)
	}
	hostedURL, _ := setup["hosted_url"].(string)
	method, _ := setup["payment_method"].(map[string]any)
	state.MethodID, _ = method["id"].(string)
	hostedParsed, hostedErr := url.Parse(hostedURL)
	if hostedErr != nil || state.MethodID == "" || hostedParsed.Scheme != "https" ||
		!strings.HasPrefix(strings.ToLower(hostedParsed.Hostname()), "payment-simulator.") ||
		!strings.HasSuffix(strings.ToLower(hostedParsed.Hostname()), ".realtekconnect.com") || hostedParsed.User != nil {
		return errors.New("hosted setup returned an unsafe URL or missing payment method")
	}
	webDir := filepath.Join(workspace, "repos", "rtk_cloud_admin", "web")
	for _, target := range []string{"desktop", "mobile"} {
		path := filepath.Join(outDir, "evidence", "LIVE-STG-SIMULATOR-001@"+target+".png")
		if err := paymentLiveScreenshot(webDir, target, hostedURL, path); err != nil {
			return fmt.Errorf("%s screenshot: %w", target, err)
		}
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			return fmt.Errorf("%s screenshot evidence is missing", target)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, hostedURL+"/complete", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("complete hosted setup: %w", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("complete hosted setup: HTTP %d", response.StatusCode)
	}
	if err := pollPaymentLive(ctx, cfg.Timeout, func() (bool, error) {
		var methods map[string]any
		if err := paymentLiveBillingJSON(ctx, client, http.MethodGet, base+"/payment-methods", token, "payment_method.read", nil, nil, &methods); err != nil {
			return false, err
		}
		for _, item := range paymentLiveAnySlice(methods["payment_methods"]) {
			method := nestedMap(item)
			if method["id"] == state.MethodID && method["status"] == "active" {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("payment method activation: %w", err)
	}
	var current map[string]any
	if err := paymentLiveBillingJSON(ctx, client, http.MethodGet, base+"/auto-topup", token, "auto_topup.read", nil, nil, &current); err != nil {
		return err
	}
	state.PolicyVersion = nestedInt64(current["auto_topup"], "version")
	policyBody := map[string]any{"enabled": true, "threshold_minor": 300, "top_up_amount_minor": 300, "currency": "TWD", "payment_method_id": state.MethodID, "daily_attempt_limit": 2, "daily_amount_limit_minor": 1000, "cooldown_seconds": 3600, "consent": map[string]any{"accepted": true, "text_version": "auto-topup-live-v1", "text_sha256": strings.Repeat("b", 64), "locale": "zh-TW"}}
	var policy map[string]any
	if err := paymentLiveBillingJSON(ctx, client, http.MethodPut, base+"/auto-topup", token, "auto_topup.manage", map[string]string{"If-Match": strconv.Quote(strconv.FormatInt(state.PolicyVersion, 10)), "X-Request-Id": "policy-" + cfg.RunID}, policyBody, &policy); err != nil {
		return fmt.Errorf("enable automatic top-up: %w", err)
	}
	state.PolicyVersion = nestedInt64(policy["auto_topup"], "version")
	var topup map[string]any
	if err := paymentLiveBillingJSON(ctx, client, http.MethodPost, base+"/topups", token, "payment_intent.create", map[string]string{"Idempotency-Key": "topup-" + cfg.RunID, "X-Request-Id": "topup-" + cfg.RunID}, map[string]any{"amount_minor": 300, "currency": "TWD", "payment_method_id": state.MethodID}, &topup); err != nil {
		return fmt.Errorf("create TWD 300 top-up: %w", err)
	}
	state.IntentID, _ = nestedMap(topup["payment_intent"])["id"].(string)
	if state.IntentID == "" {
		return errors.New("create TWD 300 top-up: missing intent ID")
	}
	if err := pollPaymentLive(ctx, cfg.Timeout, func() (bool, error) {
		var intent map[string]any
		if err := paymentLiveBillingJSON(ctx, client, http.MethodGet, base+"/payment-intents/"+url.PathEscape(state.IntentID), token, "payment_intent.read", nil, nil, &intent); err != nil {
			return false, err
		}
		status, _ := nestedMap(intent["payment_intent"])["state"].(string)
		if status == "failed" || status == "requires_action" {
			return false, fmt.Errorf("intent reached %s", status)
		}
		return status == "succeeded", nil
	}); err != nil {
		return fmt.Errorf("payment reconciliation: %w", err)
	}
	var ledger map[string]any
	if err := paymentLiveBillingJSON(ctx, client, http.MethodGet, base+"/billing/ledger", token, "billing_ledger.read", nil, nil, &ledger); err != nil {
		return err
	}
	for _, item := range paymentLiveAnySlice(ledger["ledger_entries"]) {
		entry := nestedMap(item)
		if entry["reason"] == "payment_top_up_credit" && int64Value(entry["amount_minor"]) == 300 {
			return nil
		}
	}
	return errors.New("reconciled payment has no matching TWD 300 ledger credit")
}

func cleanupPaymentLive(ctx context.Context, client *http.Client, cfg paymentLiveConfig, token string, state paymentLiveState) error {
	base := cfg.BillingBaseURL + "/v1/orgs/" + url.PathEscape(cfg.OrgID)
	var issues []string
	if state.PolicyVersion > 0 {
		var disabled map[string]any
		err := paymentLiveBillingJSON(ctx, client, http.MethodDelete, base+"/auto-topup", token, "auto_topup.manage", map[string]string{"If-Match": strconv.Quote(strconv.FormatInt(state.PolicyVersion, 10))}, map[string]any{"reason": "staging qualification cleanup " + cfg.RunID}, &disabled)
		if err != nil {
			issues = append(issues, "disable policy: "+err.Error())
		}
	}
	if state.MethodID != "" {
		var revoked map[string]any
		if err := paymentLiveBillingJSON(ctx, client, http.MethodDelete, base+"/payment-methods/"+url.PathEscape(state.MethodID), token, "payment_method.manage", nil, map[string]any{"reason": "staging qualification cleanup " + cfg.RunID}, &revoked); err != nil {
			issues = append(issues, "revoke method: "+err.Error())
		}
	}
	if len(issues) > 0 {
		return errors.New(strings.Join(issues, "; "))
	}
	return nil
}

func paymentLiveBillingJSON(ctx context.Context, client *http.Client, method, endpoint, token, permission string, headers map[string]string, body any, output any) error {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["X-Billing-Permissions"] = permission
	headers["X-Billing-Actor-Type"] = "service_test"
	headers["X-Billing-Actor-ID"] = "staging-payment-qualification"
	return paymentLiveJSON(ctx, client, method, endpoint, token, headers, body, output)
}

func paymentLiveJSON(ctx context.Context, client *http.Client, method, endpoint, token string, headers map[string]string, body any, output any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned HTTP %d", method, endpoint, response.StatusCode)
	}
	if output != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, output); err != nil {
			return fmt.Errorf("decode %s: %w", endpoint, err)
		}
	}
	return nil
}

func paymentSimulatorAvailable(account map[string]any) bool {
	for _, item := range paymentLiveAnySlice(account["payment_providers"]) {
		provider := nestedMap(item)
		capabilities := nestedMap(provider["capabilities"])
		if provider["name"] == "simulator" && provider["environment"] == "simulated" && capabilities["hosted_setup"] == true && capabilities["merchant_initiated_charge"] == true {
			return true
		}
	}
	return false
}

func pollPaymentLive(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := check()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func paymentLiveAnySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func nestedMap(value any) map[string]any {
	item, _ := value.(map[string]any)
	return item
}

func nestedInt64(value any, key string) int64 { return int64Value(nestedMap(value)[key]) }

func int64Value(value any) int64 {
	number, _ := value.(float64)
	return int64(number)
}

func writePaymentLiveReports(outDir string, report paymentEvidenceReport, cleanupErr error) error {
	missingResponsiveEvidence := false
	if report.Status == "PASS" {
		for _, target := range []string{"desktop", "mobile"} {
			path := filepath.Join(outDir, "evidence", "LIVE-STG-SIMULATOR-001@"+target+".png")
			if info, err := os.Stat(path); err != nil || info.Size() == 0 {
				missingResponsiveEvidence = true
			}
		}
		if missingResponsiveEvidence {
			report.Status = "INCOMPLETE"
			report.Assessment = "Successful staging behavior lacks required desktop/mobile screenshot evidence."
			if len(report.Cases) > 0 {
				report.Cases[0].Status = "INCOMPLETE"
				report.Cases[0].Assessment = report.Assessment
			}
		}
	}
	cleanupLabel := "PASS"
	if cleanupErr != nil {
		cleanupLabel = "FAIL"
	}
	logLine := fmt.Sprintf("%s run_id=%s test_id=LIVE-STG-SIMULATOR-001 status=%s cleanup=%s\n", time.Now().UTC().Format(time.RFC3339), report.RunID, report.Status, cleanupLabel)
	if err := os.WriteFile(filepath.Join(outDir, "execution.log"), []byte(logLine), 0o644); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), report); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "TEST_REPORT.md"), renderPaymentEvidenceReport(report), 0o644); err != nil {
		return err
	}
	cleanupStatus := "PASS"
	cleanupAssessment := "Automatic top-up policy disabled and simulator payment method revoked; no infrastructure resources were created."
	if cleanupErr != nil {
		cleanupStatus, cleanupAssessment = "FAIL", cleanupErr.Error()
	}
	if err := writeJSON(filepath.Join(outDir, "cleanup-report.json"), map[string]any{"schema_version": 1, "run_id": report.RunID, "status": cleanupStatus, "generated_at": time.Now().UTC().Format(time.RFC3339), "remaining_resources": []string{}, "assessment": cleanupAssessment}); err != nil {
		return err
	}
	issues := findUnredactedFeatureEvidence(outDir)
	redactionStatus := "PASS"
	if len(issues) > 0 {
		redactionStatus = "FAIL"
		report.Status = "FAIL"
		report.Assessment = "Credential-like material was detected in staging-live evidence."
		if len(report.Cases) > 0 {
			report.Cases[0].Status = "FAIL"
			report.Cases[0].Assessment = report.Assessment
		}
		if err := writeJSON(filepath.Join(outDir, "results.json"), report); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, "TEST_REPORT.md"), renderPaymentEvidenceReport(report), 0o644); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(outDir, "redaction-report.json"), map[string]any{"schema_version": 1, "run_id": report.RunID, "status": redactionStatus, "generated_at": time.Now().UTC().Format(time.RFC3339), "findings": issues}); err != nil {
		return err
	}
	evidenceNames := []string{"results.json", "TEST_REPORT.md", "execution.log", "cleanup-report.json", "redaction-report.json", "evidence/LIVE-STG-SIMULATOR-001@desktop.png", "evidence/LIVE-STG-SIMULATOR-001@mobile.png"}
	evidence := make([]paymentEvidenceFile, 0, len(evidenceNames))
	for _, name := range evidenceNames {
		path := filepath.Join(outDir, filepath.FromSlash(name))
		if _, err := os.Stat(path); err != nil {
			continue
		}
		sha, err := fileSHA256(path)
		if err != nil {
			return err
		}
		evidence = append(evidence, paymentEvidenceFile{Path: name, SHA256: sha})
	}
	manifest := paymentEvidenceManifest{SchemaVersion: 1, RunID: report.RunID, Profile: report.Profile, Environment: report.Environment, Status: report.Status, GeneratedAt: time.Now().UTC().Format(time.RFC3339), WorkspaceCommit: report.WorkspaceCommit, ServiceCommit: report.ServiceCommit, Cases: report.Cases, Evidence: evidence}
	if err := writeJSON(filepath.Join(outDir, "evidence-manifest.json"), manifest); err != nil {
		return err
	}
	if redactionStatus != "PASS" {
		return errors.New("payment staging-live evidence failed redaction")
	}
	if missingResponsiveEvidence {
		return errors.New("payment staging-live evidence is incomplete: desktop/mobile screenshots are required")
	}
	return nil
}
