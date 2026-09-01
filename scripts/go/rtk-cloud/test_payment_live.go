package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
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
const paymentLiveBootstrapCustomerEmail = "billing-qualification-customer@users.local"
const paymentLiveBootstrapCustomerSecret = "billing-qualification-customer"

type paymentLiveConfig struct {
	RunID, AccountManagerBaseURL, BillingBaseURL, CloudAdminBaseURL, OrgID string
	OwnerUserID                                                            string
	OwnershipVersion                                                       int64
	AccountTokenFile, BillingTokenFile, InternalTokenFile, DebitTokenFile  string
	CustomerSessionFile                                                    string
	Confirm, ConfirmTestOrg                                                string
	EnvRoot                                                                string
	Run, Plan, BootstrapTestOrg                                            bool
	Timeout                                                                time.Duration
}

type paymentLiveState struct {
	MethodID, AutoIntentID, ManualIntentID, HostedIntentID, DebitLedgerEntryID, InvoiceID      string
	PolicyVersion                                                                              int64
	HostedSetupPassed, NewebPayHostedPassed, AutoTopUpPassed, ManualTopUpPassed, InvoicePassed bool
}

var paymentLiveScreenshot = func(workdir, target, targetURL, output string) error {
	viewport := "1280,720"
	if target == "mobile" {
		viewport = "390,844"
	}
	cmd := exec.Command("npx", paymentLiveScreenshotArgs(viewport, targetURL, output)...)
	cmd.Dir = workdir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func paymentLiveScreenshotArgs(viewport, targetURL, output string) []string {
	return []string{"playwright", "screenshot", "--browser=chromium", "--viewport-size=" + viewport, "--wait-for-timeout=1000", targetURL, output}
}

var paymentLiveHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}

var (
	paymentLiveAccountManagerContext = accountManagerContextFromFlags
	paymentLiveAccountLoginSession   = accountLoginSession
	paymentLiveRuntimeSecretValue    = lkeRuntimeSecretValueFromFlags
	paymentLiveAccountListClouds     = accountListBrandClouds
	paymentLiveAccountCreateUser     = accountCreateUser
	paymentLiveGeneratePassword      = paymentLiveRandomPassword
	paymentLiveLoadCustomer          = loadPaymentLiveQualificationCustomer
	paymentLiveSaveCustomer          = savePaymentLiveQualificationCustomer
	paymentLiveEnsureCustomer        = ensurePaymentLiveQualificationCustomer
	paymentLiveCreateCustomerOrg     = createPaymentLiveQualificationOrganization
	paymentLiveListCustomerClouds    = listPaymentLiveQualificationClouds
)

type paymentLiveQualificationCustomer struct {
	Email, Password, OrganizationID, AccessToken string
}

type paymentLiveBrandCloud struct {
	ID               string
	Name             string
	MyRole           string
	OwnerUserID      string
	OwnershipVersion int64
}

type paymentLiveHTTPError struct {
	Method, Endpoint string
	Status           int
}

func (e *paymentLiveHTTPError) Error() string {
	return fmt.Sprintf("%s %s returned HTTP %d", e.Method, e.Endpoint, e.Status)
}

func runTestPaymentLive(args []string) error {
	fs := flag.NewFlagSet("test-payment staging-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	profile := fs.String("profile", "staging-live", "payment profile")
	runID := fs.String("run-id", "", "artifact run ID")
	accountManagerBaseURL := fs.String("base-url", os.Getenv("ACCOUNT_MANAGER_PUBLIC_URL"), "staging Account Manager base URL (deprecated alias)")
	fs.StringVar(accountManagerBaseURL, "account-manager-base-url", os.Getenv("ACCOUNT_MANAGER_PUBLIC_URL"), "staging Account Manager base URL")
	billingBaseURL := fs.String("billing-base-url", firstNonEmpty(os.Getenv("BILLING_PUBLIC_URL"), "https://billing.video-cloud-staging.realtekconnect.com"), "staging Billing service base URL")
	cloudAdminBaseURL := fs.String("cloud-admin-base-url", "", "staging Cloud Admin base URL used to mint an ephemeral customer session")
	customerSessionFile := fs.String("customer-session-file", "", "0600 output file for the ephemeral Cloud Admin customer session")
	orgID := fs.String("org-id", os.Getenv("PAYMENT_TEST_ORG_ID"), "dedicated staging test organization")
	ownerUserID := fs.String("owner-user-id", os.Getenv("PAYMENT_TEST_OWNER_USER_ID"), "global owner user ID for the dedicated staging test organization")
	ownershipVersion := fs.Int64("ownership-version", 0, "current ownership version for the dedicated staging test organization")
	accountTokenFile := fs.String("access-token-file", os.Getenv("PAYMENT_TEST_ACCESS_TOKEN_FILE"), "file containing the dedicated Account Manager test access token")
	billingTokenFile := fs.String("billing-token-file", os.Getenv("PAYMENT_TEST_BILLING_TOKEN_FILE"), "file containing the dedicated Billing service token")
	internalTokenFile := fs.String("internal-token-file", os.Getenv("PAYMENT_TEST_INTERNAL_TOKEN_FILE"), "file containing the dedicated Billing internal token")
	debitTokenFile := fs.String("debit-token-file", os.Getenv("PAYMENT_TEST_DEBIT_TOKEN_FILE"), "file containing the dedicated Billing debit-source token")
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
	cfg := paymentLiveConfig{RunID: *runID, AccountManagerBaseURL: strings.TrimRight(strings.TrimSpace(*accountManagerBaseURL), "/"), BillingBaseURL: strings.TrimRight(strings.TrimSpace(*billingBaseURL), "/"), CloudAdminBaseURL: strings.TrimRight(strings.TrimSpace(*cloudAdminBaseURL), "/"), OrgID: strings.TrimSpace(*orgID), OwnerUserID: strings.TrimSpace(*ownerUserID), OwnershipVersion: *ownershipVersion, AccountTokenFile: strings.TrimSpace(*accountTokenFile), BillingTokenFile: strings.TrimSpace(*billingTokenFile), InternalTokenFile: strings.TrimSpace(*internalTokenFile), DebitTokenFile: strings.TrimSpace(*debitTokenFile), CustomerSessionFile: strings.TrimSpace(*customerSessionFile), Confirm: strings.TrimSpace(*confirm), ConfirmTestOrg: strings.TrimSpace(*confirmTestOrg), EnvRoot: strings.TrimSpace(*envRoot), Run: *run, Plan: *plan, BootstrapTestOrg: *bootstrapTestOrg, Timeout: *timeout}
	if cfg.Plan {
		fmt.Printf("Payment staging-live plan (%s): preflight -> dedicated organization -> hosted setup -> desktop/mobile screenshots -> activate method -> enable approved defaults -> debit threshold crossing -> idempotent replay -> one automatic charge/credit -> separate manual TWD 300 top-up -> NewebPay hosted TWD 500 checkout/callback/query/credit -> disable policy -> revoke method -> redaction/cleanup reports\n", cfg.RunID)
		return nil
	}
	if err := validatePaymentLiveConfig(cfg); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	billingToken, internalToken, debitToken := "", "", ""
	if cfg.BootstrapTestOrg {
		cfg, _, billingToken, internalToken, debitToken, err = bootstrapPaymentLiveOrganization(workspace, cfg)
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
		internalRaw, readErr := os.ReadFile(cfg.InternalTokenFile)
		if readErr != nil {
			return fmt.Errorf("read dedicated Billing internal token: %w", readErr)
		}
		internalToken = strings.TrimSpace(string(internalRaw))
		if len(internalToken) < 24 {
			return errors.New("dedicated Billing internal token is empty or implausibly short")
		}
		debitRaw, readErr := os.ReadFile(cfg.DebitTokenFile)
		if readErr != nil {
			return fmt.Errorf("read dedicated Billing debit token: %w", readErr)
		}
		debitToken = strings.TrimSpace(string(debitRaw))
		if len(debitToken) < 24 {
			return errors.New("dedicated Billing debit token is empty or implausibly short")
		}
	}
	outDir := filepath.Join(workspace, ".artifacts", "test-runs", cfg.RunID, "payments", "staging-live")
	if err := os.MkdirAll(filepath.Join(outDir, "evidence"), 0o755); err != nil {
		return err
	}
	started := time.Now().UTC()
	state := paymentLiveState{}
	client := paymentLiveHTTPClient()
	runErr := executePaymentLive(context.Background(), client, workspace, outDir, cfg, billingToken, internalToken, debitToken, &state)
	cleanupErr := cleanupPaymentLive(context.Background(), client, cfg, billingToken, state)
	if runErr == nil && cleanupErr == nil {
		state.InvoiceID, runErr = seedPaymentLiveInvoice(context.Background(), client, cfg, billingToken, internalToken)
		if runErr != nil {
			runErr = fmt.Errorf("invoice qualification: %w", runErr)
		} else {
			state.InvoicePassed = true
		}
	}
	completed := time.Now().UTC()
	cases := paymentLiveCases(started, completed, state, runErr, cleanupErr)
	status, assessment := "PASS", "Deployed hosted setup, debit-triggered automatic top-up, idempotency, manual top-up, ledger reconciliation, and cleanup passed."
	if runErr != nil || cleanupErr != nil {
		status = "FAIL"
		assessment = joinPaymentLiveErrors(runErr, cleanupErr)
	}
	workspaceCommit, _ := gitOutput(workspace, "rev-parse", "HEAD")
	serviceCommit, _ := gitOutput(filepath.Join(workspace, "repos", "rtk_billing"), "rev-parse", "HEAD")
	report := paymentEvidenceReport{SchemaVersion: 1, RunID: cfg.RunID, Profile: "staging-live", Environment: "staging", WorkspaceCommit: strings.TrimSpace(workspaceCommit), ServiceCommit: strings.TrimSpace(serviceCommit), StartedAt: started.Format(time.RFC3339), CompletedAt: completed.Format(time.RFC3339), DurationMS: completed.Sub(started).Milliseconds(), Status: status, Assessment: assessment, CoverageGate: "N/A", Cases: cases}
	if err := writeJSON(filepath.Join(outDir, "qualification-context.json"), map[string]any{
		"schema_version": 1, "run_id": cfg.RunID, "organization_id": cfg.OrgID,
		"hosted_setup_passed": state.HostedSetupPassed, "auto_topup_passed": state.AutoTopUpPassed,
		"manual_topup_passed": state.ManualTopUpPassed, "invoice_passed": state.InvoicePassed,
		"invoice_id": state.InvoiceID, "ephemeral_customer_session_created": cfg.CustomerSessionFile != "",
	}); err != nil {
		return err
	}
	if err := writePaymentLiveReports(outDir, report, cleanupErr); err != nil {
		return err
	}
	if report.Status != "PASS" {
		return fmt.Errorf("payment staging-live qualification failed: %s", report.Assessment)
	}
	fmt.Printf("Payment staging-live report: %s\n", filepath.Join(outDir, "test_report.md"))
	return nil
}

func validatePaymentLiveConfig(cfg paymentLiveConfig) error {
	if cfg.Confirm != paymentLiveConfirmation {
		return fmt.Errorf("--run requires --confirm %s", paymentLiveConfirmation)
	}
	if cfg.BootstrapTestOrg {
		if cfg.OrgID != "" || cfg.OwnerUserID != "" || cfg.OwnershipVersion != 0 || cfg.AccountTokenFile != "" || cfg.BillingTokenFile != "" || cfg.InternalTokenFile != "" || cfg.DebitTokenFile != "" {
			return errors.New("--bootstrap-test-org cannot be combined with explicit organization, ownership, or token inputs")
		}
		if cfg.ConfirmTestOrg != paymentLiveBootstrapConfirmation {
			return fmt.Errorf("--bootstrap-test-org requires --confirm-test-org %s", paymentLiveBootstrapConfirmation)
		}
		if cfg.EnvRoot == "" {
			return errors.New("--bootstrap-test-org requires --env-root")
		}
	} else if cfg.OrgID == "" || cfg.ConfirmTestOrg != cfg.OrgID {
		return errors.New("--run requires a dedicated --org-id and an exact matching --confirm-test-org")
	} else if cfg.OwnerUserID == "" || cfg.OwnershipVersion < 1 {
		return errors.New("--run requires --owner-user-id and a positive --ownership-version")
	}
	parsed, err := url.Parse(cfg.AccountManagerBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || strings.ToLower(parsed.Hostname()) != "account-manager.video-cloud-staging.realtekconnect.com" {
		return errors.New("--base-url must be the approved HTTPS Account Manager staging endpoint")
	}
	billingParsed, billingErr := url.Parse(cfg.BillingBaseURL)
	if billingErr != nil || billingParsed.Scheme != "https" || billingParsed.Host == "" || billingParsed.User != nil || strings.ToLower(billingParsed.Hostname()) != "billing.video-cloud-staging.realtekconnect.com" {
		return errors.New("--billing-base-url must be the approved HTTPS Billing staging endpoint")
	}
	if (cfg.CloudAdminBaseURL == "") != (cfg.CustomerSessionFile == "") {
		return errors.New("--cloud-admin-base-url and --customer-session-file must be provided together")
	}
	if cfg.CloudAdminBaseURL != "" {
		cloudAdminParsed, cloudAdminErr := url.Parse(cfg.CloudAdminBaseURL)
		if cloudAdminErr != nil || cloudAdminParsed.Scheme != "https" || cloudAdminParsed.Host == "" || cloudAdminParsed.User != nil || strings.ToLower(cloudAdminParsed.Hostname()) != "admin.video-cloud-staging.realtekconnect.com" {
			return errors.New("--cloud-admin-base-url must be the approved HTTPS Cloud Admin staging endpoint")
		}
		if !cfg.BootstrapTestOrg {
			return errors.New("ephemeral Cloud Admin customer sessions require --bootstrap-test-org")
		}
	}
	if !cfg.BootstrapTestOrg && (cfg.BillingTokenFile == "" || cfg.InternalTokenFile == "" || cfg.DebitTokenFile == "") {
		return errors.New("--billing-token-file, --internal-token-file, and --debit-token-file are required; raw token command-line arguments are intentionally unsupported")
	}
	if !cfg.BootstrapTestOrg {
		paths := []string{cfg.BillingTokenFile, cfg.InternalTokenFile, cfg.DebitTokenFile}
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

func bootstrapPaymentLiveOrganization(workspace string, cfg paymentLiveConfig) (paymentLiveConfig, string, string, string, string, error) {
	billingToken, err := paymentLiveRuntimeSecretValue(workspace, cfg.EnvRoot, "-billing", "billing-runtime", "BILLING_SERVICE_TOKEN")
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("load LKE Billing service credential: %w", err)
	}
	internalToken, err := paymentLiveRuntimeSecretValue(workspace, cfg.EnvRoot, "-billing", "billing-runtime", "BILLING_INTERNAL_TOKEN")
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("load LKE Billing internal credential: %w", err)
	}
	debitToken, err := paymentLiveRuntimeSecretValue(workspace, cfg.EnvRoot, "-billing", "billing-runtime", "BILLING_DEBIT_TOKEN")
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("load LKE Billing debit credential: %w", err)
	}
	manager, err := paymentLiveAccountManagerContext(workspace, cfg.EnvRoot)
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("load staging platform-admin credentials: %w", err)
	}
	defer manager.Close()
	manager.BaseURL = cfg.AccountManagerBaseURL
	platformSession, err := paymentLiveAccountLoginSession(manager, func(string, ...any) {})
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("staging platform-admin login: %w", err)
	}
	client := paymentLiveHTTPClient()
	clouds, err := paymentLiveAccountListClouds(manager, platformSession.AccessToken, 200)
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("list qualification bootstrap clouds: %w", err)
	}
	bootstrapCloudID := ""
	for _, item := range paymentLiveAnySlice(clouds["brand_clouds"]) {
		cloud := nestedMap(item)
		if cloud["name"] == paymentLiveBootstrapOrgName {
			bootstrapCloudID, _ = cloud["id"].(string)
			break
		}
	}
	if bootstrapCloudID == "" {
		bootstrapCloud, createErr := paymentLiveCreateCustomerOrg(context.Background(), client, cfg.AccountManagerBaseURL, platformSession.AccessToken, paymentLiveBootstrapOrgName)
		err = createErr
		if err != nil {
			return cfg, "", "", "", "", fmt.Errorf("create qualification bootstrap cloud: %w", err)
		}
		bootstrapCloudID = bootstrapCloud.ID
	}
	customer, found, err := paymentLiveLoadCustomer(workspace, cfg.EnvRoot)
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("load dedicated qualification customer: %w", err)
	}
	if !found {
		customer.Email = paymentLiveBootstrapCustomerEmail
		customer.Password, err = paymentLiveGeneratePassword()
		if err != nil {
			return cfg, "", "", "", "", err
		}
		if err := paymentLiveSaveCustomer(workspace, cfg.EnvRoot, customer); err != nil {
			return cfg, "", "", "", "", fmt.Errorf("persist qualification customer bootstrap credential: %w", err)
		}
	}
	if _, err := paymentLiveAccountCreateUser(manager, &platformSession, func(string, ...any) {}, bootstrapCloudID, customer.Email, "Billing Qualification", customer.Password, "member", true); err != nil {
		return cfg, "", "", "", "", fmt.Errorf("create audited staging qualification member: %w", err)
	}
	customer, err = paymentLiveEnsureCustomer(context.Background(), client, cfg.AccountManagerBaseURL, customer)
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("ensure dedicated qualification customer: %w", err)
	}
	if customer.AccessToken == "" {
		return cfg, "", "", "", "", errors.New("dedicated qualification customer has no access token")
	}
	ownedClouds, err := paymentLiveListCustomerClouds(context.Background(), client, cfg.AccountManagerBaseURL, customer.AccessToken)
	if err != nil {
		return cfg, "", "", "", "", fmt.Errorf("list dedicated qualification customer clouds: %w", err)
	}
	runCloud, found := selectPaymentLiveQualificationCloud(ownedClouds, customer.OrganizationID)
	if !found {
		runCloud, err = paymentLiveCreateCustomerOrg(context.Background(), client, cfg.AccountManagerBaseURL, customer.AccessToken, paymentLiveBootstrapOrgName+" "+cfg.RunID)
		if err != nil {
			var responseErr *paymentLiveHTTPError
			if !errors.As(err, &responseErr) || responseErr.Status != http.StatusConflict {
				return cfg, "", "", "", "", fmt.Errorf("create dedicated qualification organization: %w", err)
			}
			ownedClouds, err = paymentLiveListCustomerClouds(context.Background(), client, cfg.AccountManagerBaseURL, customer.AccessToken)
			if err != nil {
				return cfg, "", "", "", "", fmt.Errorf("recover qualification organization after create conflict: %w", err)
			}
			runCloud, found = selectPaymentLiveQualificationCloud(ownedClouds, "")
			if !found {
				return cfg, "", "", "", "", errors.New("qualification organization create conflicted without a reusable owned cloud")
			}
		}
	}
	customer.OrganizationID = runCloud.ID
	if err := paymentLiveSaveCustomer(workspace, cfg.EnvRoot, customer); err != nil {
		return cfg, "", "", "", "", fmt.Errorf("persist qualification customer credential: %w", err)
	}
	cfg.OrgID = runCloud.ID
	cfg.OwnerUserID = runCloud.OwnerUserID
	cfg.OwnershipVersion = runCloud.OwnershipVersion
	if cfg.CustomerSessionFile != "" {
		if loginErr := writePaymentLiveCustomerSession(context.Background(), client, cfg.CloudAdminBaseURL, customer.Email, customer.Password, cfg.CustomerSessionFile); loginErr != nil {
			return cfg, "", "", "", "", loginErr
		}
		if activateErr := activatePaymentLiveCustomerOrganization(context.Background(), client, cfg.CloudAdminBaseURL, cfg.OrgID, cfg.CustomerSessionFile); activateErr != nil {
			return cfg, "", "", "", "", activateErr
		}
	}
	return cfg, customer.AccessToken, billingToken, internalToken, debitToken, nil
}

func paymentLiveRandomPassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate qualification customer password: %w", err)
	}
	return "Q!" + hex.EncodeToString(raw), nil
}

func paymentLiveQualificationKubeTarget(workspace, envRootFlag string) (string, string, error) {
	envRoot, err := resolveEnvRoot(workspace, envRootFlag)
	if err != nil {
		return "", "", err
	}
	stackEnv, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), stackEnv["CLOUD_PROVIDER"]) != "lke" {
		return "", "", errors.New("qualification customer credentials require CLOUD_PROVIDER=lke")
	}
	stack := firstNonEmpty(stackEnv["CLOUD_STACK_NAME"], "video-cloud-staging")
	kubeconfig, err := lkeRuntimeKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return "", "", err
	}
	return kubeconfig, stack + "-billing", nil
}

func loadPaymentLiveQualificationCustomer(workspace, envRoot string) (paymentLiveQualificationCustomer, bool, error) {
	kubeconfig, namespace, err := paymentLiveQualificationKubeTarget(workspace, envRoot)
	if err != nil {
		return paymentLiveQualificationCustomer{}, false, err
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", namespace, "get", "secret", paymentLiveBootstrapCustomerSecret, "--ignore-not-found=true", "-o", "json")
	raw, err := cmd.Output()
	if err != nil {
		return paymentLiveQualificationCustomer{}, false, fmt.Errorf("read K8s secret %s/%s: %w", namespace, paymentLiveBootstrapCustomerSecret, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return paymentLiveQualificationCustomer{}, false, nil
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &secret); err != nil {
		return paymentLiveQualificationCustomer{}, false, err
	}
	decode := func(key string) (string, error) {
		value, err := base64.StdEncoding.DecodeString(secret.Data[key])
		if err != nil {
			return "", fmt.Errorf("decode qualification customer secret key %s: %w", key, err)
		}
		return strings.TrimSpace(string(value)), nil
	}
	email, err := decode("EMAIL")
	if err != nil {
		return paymentLiveQualificationCustomer{}, false, err
	}
	password, err := decode("PASSWORD")
	if err != nil {
		return paymentLiveQualificationCustomer{}, false, err
	}
	organizationID, err := decode("ORGANIZATION_ID")
	if err != nil {
		return paymentLiveQualificationCustomer{}, false, err
	}
	if email == "" || password == "" {
		return paymentLiveQualificationCustomer{}, false, errors.New("qualification customer secret is missing EMAIL or PASSWORD")
	}
	return paymentLiveQualificationCustomer{Email: email, Password: password, OrganizationID: organizationID}, true, nil
}

func savePaymentLiveQualificationCustomer(workspace, envRoot string, customer paymentLiveQualificationCustomer) error {
	if customer.Email == "" || customer.Password == "" {
		return errors.New("qualification customer email and password are required")
	}
	kubeconfig, namespace, err := paymentLiveQualificationKubeTarget(workspace, envRoot)
	if err != nil {
		return err
	}
	secret := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": paymentLiveBootstrapCustomerSecret, "namespace": namespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "rtk-cloud", "app.kubernetes.io/part-of": "billing-qualification"}},
		"type":     "Opaque", "stringData": map[string]string{"EMAIL": customer.Email, "PASSWORD": customer.Password, "ORGANIZATION_ID": customer.OrganizationID},
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-f", "-")
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apply K8s secret %s/%s: %w", namespace, paymentLiveBootstrapCustomerSecret, err)
	}
	return nil
}

func ensurePaymentLiveQualificationCustomer(ctx context.Context, client *http.Client, baseURL string, customer paymentLiveQualificationCustomer) (paymentLiveQualificationCustomer, error) {
	login := func() (paymentLiveQualificationCustomer, int, error) {
		var result map[string]any
		status, err := paymentLiveJSONStatus(ctx, client, http.MethodPost, baseURL+"/v1/auth/login", "", map[string]string{"email": customer.Email, "password": customer.Password}, &result)
		if err != nil || status != http.StatusOK {
			return customer, status, err
		}
		customer.AccessToken, _ = nestedMap(result["tokens"])["access_token"].(string)
		return customer, status, nil
	}
	loggedIn, status, err := login()
	if err != nil {
		return customer, err
	}
	if status == http.StatusOK {
		customer = loggedIn
	} else {
		return customer, fmt.Errorf("login audited staging qualification member: HTTP %d", status)
	}
	if customer.AccessToken == "" {
		return customer, errors.New("qualification customer login returned no access token")
	}
	return customer, nil
}

func paymentLiveJSONStatus(ctx context.Context, client *http.Client, method, endpoint, token string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if out != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			return response.StatusCode, err
		}
	} else {
		_, _ = io.Copy(io.Discard, response.Body)
	}
	return response.StatusCode, nil
}

func createPaymentLiveQualificationOrganization(ctx context.Context, client *http.Client, baseURL, token, name string) (paymentLiveBrandCloud, error) {
	cloudName := strings.TrimSpace(name)
	body, err := json.Marshal(map[string]string{"name": cloudName, "description": "Run-scoped staging Billing qualification"})
	if err != nil {
		return paymentLiveBrandCloud{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/developer/brand-clouds", bytes.NewReader(body))
	if err != nil {
		return paymentLiveBrandCloud{}, err
	}
	keyHash := sha256.Sum256([]byte(cloudName))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "billing-qualification-cloud-"+hex.EncodeToString(keyHash[:12]))
	response, err := client.Do(request)
	if err != nil {
		return paymentLiveBrandCloud{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, response.Body)
		return paymentLiveBrandCloud{}, &paymentLiveHTTPError{Method: http.MethodPost, Endpoint: request.URL.String(), Status: response.StatusCode}
	}
	var created map[string]any
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		return paymentLiveBrandCloud{}, err
	}
	cloud := nestedMap(created["brand_cloud"])
	result := paymentLiveBrandCloud{ID: stringValue(cloud["id"]), Name: stringValue(cloud["name"]), MyRole: "owner", OwnerUserID: stringValue(cloud["owner_user_id"]), OwnershipVersion: int64Value(cloud["ownership_version"])}
	if result.ID == "" || result.OwnerUserID == "" || result.OwnershipVersion < 1 {
		return paymentLiveBrandCloud{}, errors.New("organization response has no exact owner and ownership version")
	}
	return result, nil
}

func listPaymentLiveQualificationClouds(ctx context.Context, client *http.Client, baseURL, token string) ([]paymentLiveBrandCloud, error) {
	var payload map[string]any
	status, err := paymentLiveJSONStatus(ctx, client, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/developer/brand-clouds?view=owned&limit=100", token, nil, &payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, &paymentLiveHTTPError{Method: http.MethodGet, Endpoint: strings.TrimRight(baseURL, "/") + "/v1/developer/brand-clouds", Status: status}
	}
	clouds := make([]paymentLiveBrandCloud, 0)
	for _, item := range paymentLiveAnySlice(payload["brand_clouds"]) {
		cloud := nestedMap(item)
		candidate := paymentLiveBrandCloud{ID: stringValue(cloud["id"]), Name: stringValue(cloud["name"]), MyRole: stringValue(cloud["my_role"]), OwnerUserID: stringValue(cloud["owner_user_id"]), OwnershipVersion: int64Value(cloud["ownership_version"])}
		if candidate.ID != "" && candidate.MyRole == "owner" && candidate.OwnerUserID != "" && candidate.OwnershipVersion > 0 && strings.HasPrefix(candidate.Name, paymentLiveBootstrapOrgName+" billing-staging-") {
			clouds = append(clouds, candidate)
		}
	}
	return clouds, nil
}

func selectPaymentLiveQualificationCloud(clouds []paymentLiveBrandCloud, preferredID string) (paymentLiveBrandCloud, bool) {
	for _, cloud := range clouds {
		if preferredID != "" && cloud.ID == preferredID {
			return cloud, true
		}
	}
	if len(clouds) == 0 {
		return paymentLiveBrandCloud{}, false
	}
	return clouds[0], true
}

func activatePaymentLiveCustomerOrganization(ctx context.Context, client *http.Client, baseURL, organizationID, sessionFile string) error {
	session, err := os.ReadFile(sessionFile)
	if err != nil {
		return fmt.Errorf("read qualification customer session: %w", err)
	}
	body, _ := json.Marshal(map[string]string{"organization_id": organizationID})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/me/active-org", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "rtk_admin_session", Value: strings.TrimSpace(string(session))})
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("activate qualification customer organization: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("activate qualification customer organization: HTTP %d", response.StatusCode)
	}
	return nil
}

func writePaymentLiveCustomerSession(ctx context.Context, client *http.Client, baseURL, email, password, output string) error {
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Cloud Admin qualification customer login: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Cloud Admin qualification customer login: HTTP %d", response.StatusCode)
	}
	value := ""
	for _, cookie := range response.Cookies() {
		if cookie.Name == "rtk_admin_session" {
			value = cookie.Value
			break
		}
	}
	if len(value) < 16 {
		return errors.New("Cloud Admin qualification customer login returned no session cookie")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return fmt.Errorf("create customer session directory: %w", err)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create customer session file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect customer session file: %w", err)
	}
	if _, err := file.WriteString(value); err != nil {
		_ = file.Close()
		return fmt.Errorf("write customer session file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close customer session file: %w", err)
	}
	return nil
}

func executePaymentLive(ctx context.Context, client *http.Client, workspace, outDir string, cfg paymentLiveConfig, token, internalToken, debitToken string, state *paymentLiveState) error {
	base := cfg.BillingBaseURL + "/v1/orgs/" + url.PathEscape(cfg.OrgID)
	if err := verifyPaymentLiveCredentialIsolation(ctx, client, cfg, token, internalToken, debitToken); err != nil {
		return fmt.Errorf("credential isolation preflight: %w", err)
	}
	var account map[string]any
	if err := waitPaymentLiveBillingAccount(ctx, client, cfg, base, token, &account); err != nil {
		return fmt.Errorf("billing preflight: %w", err)
	}
	if !paymentSimulatorAvailable(account) {
		return errors.New("billing preflight: simulator provider with hosted_setup and merchant_initiated_charge is unavailable")
	}
	if policy := nestedMap(account["auto_topup"]); len(policy) > 0 && policy["enabled"] == true {
		return errors.New("billing preflight: dedicated test organization already has an enabled automatic top-up policy")
	}
	initialBalance := nestedInt64(account["account"], "available_balance_minor")
	initialLedger, err := readPaymentLiveLedger(ctx, client, cfg, base, token)
	if err != nil {
		return fmt.Errorf("ledger preflight: %w", err)
	}
	initialLedgerIDs := paymentLiveLedgerIDs(initialLedger)
	var existingMethods map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, base+"/payment-methods", token, "payment_method.read", nil, nil, &existingMethods); err != nil {
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
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodPost, base+"/payment-methods/setup", token, "payment_method.manage", headers, setupBody, &setup); err != nil {
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
		if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, base+"/payment-methods", token, "payment_method.read", nil, nil, &methods); err != nil {
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
	state.HostedSetupPassed = true
	var current map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, base+"/auto-topup", token, "auto_topup.read", nil, nil, &current); err != nil {
		return err
	}
	state.PolicyVersion = nestedInt64(current["auto_topup"], "version")
	policyBody := map[string]any{"enabled": true, "threshold_minor": 300, "top_up_amount_minor": 300, "currency": "TWD", "payment_method_id": state.MethodID, "daily_attempt_limit": 2, "daily_amount_limit_minor": 1000, "cooldown_seconds": 3600, "consent": map[string]any{"accepted": true, "text_version": "auto-topup-live-v1", "text_sha256": strings.Repeat("b", 64), "locale": "zh-TW"}}
	var policy map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodPut, base+"/auto-topup", token, "auto_topup.manage", map[string]string{"If-Match": strconv.Quote(strconv.FormatInt(state.PolicyVersion, 10)), "X-Request-Id": "policy-" + cfg.RunID}, policyBody, &policy); err != nil {
		return fmt.Errorf("enable automatic top-up: %w", err)
	}
	state.PolicyVersion = nestedInt64(policy["auto_topup"], "version")
	debitAmount := initialBalance - 300 + 1
	if debitAmount < 1 {
		debitAmount = 1
	}
	debitHeaders := map[string]string{"Idempotency-Key": "debit-" + cfg.RunID, "X-Request-Id": "debit-" + cfg.RunID}
	debitBody := map[string]any{"organization_id": cfg.OrgID, "amount_minor": debitAmount, "currency": "TWD", "reason": "usage_adjustment_debit", "external_id": "qualification-" + cfg.RunID}
	var debit map[string]any
	if err := paymentLiveJSON(ctx, client, http.MethodPost, cfg.BillingBaseURL+"/v1/internal/billing/debits", debitToken, debitHeaders, debitBody, &debit); err != nil {
		return fmt.Errorf("debit threshold crossing: %w", err)
	}
	state.AutoIntentID, _ = debit["payment_intent_id"].(string)
	state.DebitLedgerEntryID, _ = debit["ledger_entry_id"].(string)
	if state.AutoIntentID == "" || state.DebitLedgerEntryID == "" || debit["duplicate"] == true {
		return errors.New("debit threshold crossing did not create one new payment intent and ledger entry")
	}
	var replay map[string]any
	if err := paymentLiveJSON(ctx, client, http.MethodPost, cfg.BillingBaseURL+"/v1/internal/billing/debits", debitToken, debitHeaders, debitBody, &replay); err != nil {
		return fmt.Errorf("debit idempotent replay: %w", err)
	}
	if replay["duplicate"] != true || replay["payment_intent_id"] != state.AutoIntentID || replay["ledger_entry_id"] != state.DebitLedgerEntryID {
		return errors.New("debit idempotent replay changed the monetary result")
	}
	if err := waitPaymentLiveIntent(ctx, client, cfg, base, token, state.AutoIntentID, cfg.Timeout, true); err != nil {
		return fmt.Errorf("automatic top-up reconciliation: %w", err)
	}
	autoLedger, err := readPaymentLiveLedger(ctx, client, cfg, base, token)
	if err != nil {
		return fmt.Errorf("automatic top-up ledger: %w", err)
	}
	if err := verifyPaymentLiveVisibleAutoTopUpCredit(autoLedger, initialLedgerIDs, 300, initialBalance-debitAmount+300); err != nil {
		return err
	}
	state.AutoTopUpPassed = true

	manualLedgerIDs := paymentLiveLedgerIDs(autoLedger)
	var topup map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodPost, base+"/topups", token, "payment_intent.create", map[string]string{"Idempotency-Key": "manual-topup-" + cfg.RunID, "X-Request-Id": "manual-topup-" + cfg.RunID}, map[string]any{"amount_minor": 300, "currency": "TWD", "payment_method_id": state.MethodID}, &topup); err != nil {
		return fmt.Errorf("create separate manual TWD 300 top-up: %w", err)
	}
	state.ManualIntentID, _ = nestedMap(topup["payment_intent"])["id"].(string)
	if state.ManualIntentID == "" || state.ManualIntentID == state.AutoIntentID {
		return errors.New("manual TWD 300 top-up did not create a distinct intent")
	}
	if err := waitPaymentLiveIntent(ctx, client, cfg, base, token, state.ManualIntentID, cfg.Timeout, true); err != nil {
		return fmt.Errorf("manual top-up reconciliation: %w", err)
	}
	manualLedger, err := readPaymentLiveLedger(ctx, client, cfg, base, token)
	if err != nil {
		return fmt.Errorf("manual top-up ledger: %w", err)
	}
	newCredits := 0
	for _, item := range paymentLiveAnySlice(manualLedger["ledger_entries"]) {
		entry := nestedMap(item)
		id, _ := entry["id"].(string)
		if !manualLedgerIDs[id] && entry["direction"] == "credit" && entry["reason"] == "payment_top_up_credit" && int64Value(entry["amount_minor"]) == 300 {
			newCredits++
		}
	}
	if newCredits != 1 {
		return fmt.Errorf("manual top-up created %d new TWD 300 credits; want exactly one", newCredits)
	}
	state.ManualTopUpPassed = true

	hostedBefore := paymentLiveLedgerIDs(manualLedger)
	var hosted map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodPost, base+"/topups/checkout", token, "payment_intent.create", map[string]string{"Idempotency-Key": "newebpay-hosted-" + cfg.RunID, "X-Request-Id": "newebpay-hosted-" + cfg.RunID}, map[string]any{"amount_minor": 500, "currency": "TWD", "provider": "newebpay"}, &hosted); err != nil {
		return fmt.Errorf("create NewebPay hosted top-up: %w", err)
	}
	state.HostedIntentID, _ = nestedMap(hosted["payment_intent"])["id"].(string)
	action := nestedMap(hosted["payment_action"])
	actionMethod, _ := action["method"].(string)
	actionURL, _ := action["url"].(string)
	actionParsed, actionErr := url.Parse(actionURL)
	if actionErr != nil || state.HostedIntentID == "" || actionMethod != http.MethodPost || actionParsed.Scheme != "https" || actionParsed.User != nil || actionParsed.RawQuery != "" || actionParsed.Fragment != "" || !strings.HasPrefix(strings.ToLower(actionParsed.Hostname()), "payment-simulator.") || !strings.HasSuffix(strings.ToLower(actionParsed.Hostname()), ".realtekconnect.com") {
		return errors.New("NewebPay checkout returned an unsafe hosted action")
	}
	form := url.Values{}
	for name, raw := range nestedMap(action["fields"]) {
		value, ok := raw.(string)
		if !ok || name == "" || len(name) > 64 || len(value) > 1<<20 {
			return errors.New("NewebPay checkout returned invalid hosted fields")
		}
		form.Set(name, value)
	}
	for _, required := range []string{"MerchantID", "TradeInfo", "TradeSha", "Version"} {
		if form.Get(required) == "" {
			return fmt.Errorf("NewebPay checkout omitted %s", required)
		}
	}
	redirectBody, err := paymentLivePostForm(ctx, client, actionURL, form)
	if err != nil {
		return fmt.Errorf("submit NewebPay MPG action: %w", err)
	}
	newebPayHostedURL, err := paymentLiveHostedRedirectURL(redirectBody, actionParsed)
	if err != nil {
		return err
	}
	for _, target := range []string{"desktop", "mobile"} {
		path := filepath.Join(outDir, "evidence", "LIVE-STG-SIMULATOR-001@newebpay-"+target+".png")
		if err := paymentLiveScreenshot(webDir, target, newebPayHostedURL, path); err != nil {
			return fmt.Errorf("NewebPay %s screenshot: %w", target, err)
		}
	}
	if _, err := paymentLivePostForm(ctx, client, newebPayHostedURL, url.Values{"scenario": {"success"}}); err != nil {
		return fmt.Errorf("complete NewebPay hosted top-up: %w", err)
	}
	if err := waitPaymentLiveIntentOperation(ctx, client, cfg, base, token, state.HostedIntentID, cfg.Timeout, "query"); err != nil {
		return fmt.Errorf("NewebPay hosted reconciliation: %w", err)
	}
	hostedLedger, err := readPaymentLiveLedger(ctx, client, cfg, base, token)
	if err != nil {
		return fmt.Errorf("NewebPay hosted ledger: %w", err)
	}
	newHostedCredits := 0
	for _, item := range paymentLiveAnySlice(hostedLedger["ledger_entries"]) {
		entry := nestedMap(item)
		id, _ := entry["id"].(string)
		if !hostedBefore[id] && entry["direction"] == "credit" && entry["reason"] == "payment_top_up_credit" && int64Value(entry["amount_minor"]) == 500 {
			newHostedCredits++
		}
	}
	if newHostedCredits != 1 {
		return fmt.Errorf("NewebPay hosted top-up created %d new TWD 500 credits; want exactly one", newHostedCredits)
	}
	state.NewebPayHostedPassed = true
	return nil
}

func waitPaymentLiveBillingAccount(ctx context.Context, client *http.Client, cfg paymentLiveConfig, base, token string, account *map[string]any) error {
	return pollPaymentLive(ctx, cfg.Timeout, func() (bool, error) {
		err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, base+"/billing/account", token, "billing_account.read", nil, nil, account)
		if err == nil {
			return true, nil
		}
		var responseErr *paymentLiveHTTPError
		if errors.As(err, &responseErr) && (responseErr.Status == http.StatusNotFound || responseErr.Status == http.StatusServiceUnavailable) {
			return false, nil
		}
		return false, err
	})
}

func paymentLivePostForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if readErr != nil || len(body) > 64*1024 {
		return nil, errors.New("hosted response is unreadable or too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return body, nil
}

func paymentLiveHostedRedirectURL(body []byte, action *url.URL) (string, error) {
	lower := strings.ToLower(string(body))
	marker := "url="
	start := strings.Index(lower, marker)
	if start < 0 {
		return "", errors.New("NewebPay simulator returned no hosted redirect")
	}
	raw := string(body[start+len(marker):])
	if end := strings.IndexAny(raw, "\"'>"); end >= 0 {
		raw = raw[:end]
	}
	parsed, err := url.Parse(strings.TrimSpace(html.UnescapeString(raw)))
	if err != nil || parsed.Scheme != action.Scheme || !strings.EqualFold(parsed.Host, action.Host) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasPrefix(parsed.Path, "/newebpay/pay/") {
		return "", errors.New("NewebPay simulator returned an unsafe hosted redirect")
	}
	return parsed.String(), nil
}

func verifyPaymentLiveCredentialIsolation(ctx context.Context, client *http.Client, cfg paymentLiveConfig, tenantToken, internalToken, debitToken string) error {
	if tenantToken == internalToken || tenantToken == debitToken || internalToken == debitToken {
		return errors.New("tenant, internal, and debit credentials are not distinct")
	}
	tenantEndpoint := cfg.BillingBaseURL + "/v1/orgs/" + url.PathEscape(cfg.OrgID) + "/billing/account"
	internalEndpoint := cfg.BillingBaseURL + "/v1/internal/billing/access/" + url.PathEscape(cfg.OrgID)
	debitEndpoint := cfg.BillingBaseURL + "/v1/internal/billing/debits"
	checks := []struct {
		method, endpoint, token string
		body                    any
	}{
		{http.MethodGet, tenantEndpoint, internalToken, nil},
		{http.MethodGet, tenantEndpoint, debitToken, nil},
		{http.MethodGet, internalEndpoint, tenantToken, nil},
		{http.MethodGet, internalEndpoint, debitToken, nil},
		{http.MethodPost, debitEndpoint, tenantToken, map[string]any{}},
		{http.MethodPost, debitEndpoint, internalToken, map[string]any{}},
	}
	for _, check := range checks {
		status, err := paymentLiveHTTPStatus(ctx, client, check.method, check.endpoint, check.token, check.body)
		if err != nil {
			return err
		}
		if status != http.StatusUnauthorized {
			return fmt.Errorf("%s %s with the wrong credential returned HTTP %d; want 401", check.method, urlPathOnly(check.endpoint), status)
		}
	}
	return nil
}

func paymentLiveHTTPStatus(ctx context.Context, client *http.Client, method, endpoint, token string, body any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func urlPathOnly(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "invalid-endpoint"
	}
	return parsed.Path
}

func readPaymentLiveLedger(ctx context.Context, client *http.Client, cfg paymentLiveConfig, base, token string) (map[string]any, error) {
	var ledger map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, base+"/billing/ledger?limit=100", token, "billing_ledger.read", nil, nil, &ledger); err != nil {
		return nil, err
	}
	return ledger, nil
}

func paymentLiveLedgerIDs(ledger map[string]any) map[string]bool {
	ids := map[string]bool{}
	for _, item := range paymentLiveAnySlice(ledger["ledger_entries"]) {
		if id, _ := nestedMap(item)["id"].(string); id != "" {
			ids[id] = true
		}
	}
	return ids
}

func verifyPaymentLiveVisibleAutoTopUpCredit(ledger map[string]any, before map[string]bool, creditAmount, finalBalance int64) error {
	newEntries, creditMatches, finalBalanceMatches := 0, 0, 0
	for _, item := range paymentLiveAnySlice(ledger["ledger_entries"]) {
		entry := nestedMap(item)
		id, _ := entry["id"].(string)
		if id == "" || before[id] {
			continue
		}
		newEntries++
		if entry["direction"] == "credit" && entry["reason"] == "payment_top_up_credit" && int64Value(entry["amount_minor"]) == creditAmount {
			creditMatches++
			if int64Value(entry["balance_after_minor"]) == finalBalance {
				finalBalanceMatches++
			}
		}
	}
	if newEntries != 1 || creditMatches != 1 || finalBalanceMatches != 1 {
		return fmt.Errorf("automatic top-up visible ledger delta invalid: entries=%d credit=%d final_balance=%d", newEntries, creditMatches, finalBalanceMatches)
	}
	return nil
}

func waitPaymentLiveIntent(ctx context.Context, client *http.Client, cfg paymentLiveConfig, base, token, intentID string, timeout time.Duration, requireSingleCharge bool) error {
	expectedOperation := ""
	if requireSingleCharge {
		expectedOperation = "charge"
	}
	return waitPaymentLiveIntentOperation(ctx, client, cfg, base, token, intentID, timeout, expectedOperation)
}

func waitPaymentLiveIntentOperation(ctx context.Context, client *http.Client, cfg paymentLiveConfig, base, token, intentID string, timeout time.Duration, expectedOperation string) error {
	return pollPaymentLive(ctx, timeout, func() (bool, error) {
		var detail map[string]any
		if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, base+"/payment-intents/"+url.PathEscape(intentID), token, "payment_intent.read", nil, nil, &detail); err != nil {
			return false, err
		}
		status, _ := nestedMap(detail["payment_intent"])["state"].(string)
		if status == "failed" || status == "requires_action" {
			return false, fmt.Errorf("intent reached %s", status)
		}
		if status != "succeeded" {
			return false, nil
		}
		if expectedOperation != "" {
			attempts := 0
			for _, item := range paymentLiveAnySlice(detail["attempts"]) {
				if nestedMap(item)["operation"] == expectedOperation {
					attempts++
				}
			}
			if attempts != 1 {
				return false, fmt.Errorf("succeeded intent has %d %s attempts; want exactly one", attempts, expectedOperation)
			}
		}
		return true, nil
	})
}

func paymentLiveCases(started, completed time.Time, state paymentLiveState, runErr, cleanupErr error) []paymentEvidenceCase {
	duration := completed.Sub(started).Milliseconds()
	definitions := []struct {
		id, purpose, method, pass string
		completed                 bool
	}{
		{"LIVE-STG-SIMULATOR-001", "Qualify deployed simulator hosted setup, NewebPay hosted charge, and responsive customer-safe UI.", "Dedicated staging organization activates one simulator method, completes one encrypted NewebPay hosted checkout through callback/query reconciliation, proves exactly one TWD 500 credit, and captures desktop/mobile evidence for both hosted pages.", "Hosted setup, NewebPay charge reconciliation, and responsive evidence passed.", state.HostedSetupPassed && state.NewebPayHostedPassed},
		{"LIVE-STG-AUTOTOPUP-001", "Prove a deployed debit threshold crossing produces exactly one automatic charge and credit.", "Dedicated debit credential posts and replays one usage-adjustment debit; Billing must create one intent, one charge attempt, and one ledger credit.", "Debit-triggered automatic top-up and idempotent replay passed.", state.AutoTopUpPassed},
		{"LIVE-STG-MANUAL-TOPUP-001", "Keep manual top-up qualification distinct from automatic top-up.", "Tenant service identity creates one explicit TWD 300 top-up and verifies a distinct intent, one charge attempt, and one ledger credit.", "Separate manual top-up passed.", state.ManualTopUpPassed},
		{"LIVE-STG-BILLING-DOCUMENT-001", "Create immutable deployed invoice and PDF evidence for the real Cloud Admin smoke.", "Internal Billing identity activates qualification pricing, ingests digest-anchored usage, closes one dedicated organization period after payment cleanup, and records the resulting invoice ID.", "Staging invoice and document seed passed.", state.InvoicePassed},
	}
	cases := make([]paymentEvidenceCase, 0, len(definitions))
	for _, definition := range definitions {
		status, assessment := "PASS", definition.pass
		if !definition.completed || cleanupErr != nil {
			status = "FAIL"
			assessment = joinPaymentLiveErrors(runErr, cleanupErr)
			if assessment == "" {
				assessment = "Qualification did not reach this case."
			}
		}
		cases = append(cases, paymentEvidenceCase{TestID: definition.id, Purpose: definition.purpose, Method: definition.method, StartedAt: started.Format(time.RFC3339), CompletedAt: completed.Format(time.RFC3339), DurationMS: duration, Status: status, Assessment: assessment})
	}
	return cases
}

func joinPaymentLiveErrors(runErr, cleanupErr error) string {
	parts := make([]string, 0, 2)
	if runErr != nil {
		parts = append(parts, runErr.Error())
	}
	if cleanupErr != nil {
		parts = append(parts, "cleanup: "+cleanupErr.Error())
	}
	return strings.Join(parts, "; ")
}

func seedPaymentLiveInvoice(ctx context.Context, client *http.Client, cfg paymentLiveConfig, billingToken, internalToken string) (string, error) {
	if err := ensurePaymentLiveBillingProfile(ctx, client, cfg, billingToken); err != nil {
		return "", fmt.Errorf("configure qualification billing profile: %w", err)
	}
	now := time.Now().UTC()
	periodStart := now.Truncate(time.Hour).Add(-2 * time.Hour)
	periodEnd := periodStart.Add(time.Hour)
	version := now.Unix()
	internalBase := cfg.BillingBaseURL + "/v1/internal/billing"
	headers := map[string]string{"X-Request-Id": "invoice-seed-" + cfg.RunID}
	pricingBody := map[string]any{
		"plan_key": "qualification", "version": version, "currency": "TWD", "effective_from": periodStart,
		"rates": []map[string]any{{
			"service_code": "qualification", "metric_code": "staging_units", "description": "Staging qualification units",
			"unit": "unit", "unit_price_minor": 2, "unit_price_scale": 0, "rounding_mode": "half_up", "tax_rate_basis_points": 0,
		}},
	}
	var pricing map[string]any
	if err := paymentLiveJSON(ctx, client, http.MethodPost, internalBase+"/pricing-versions", internalToken, headers, pricingBody, &pricing); err != nil {
		return "", fmt.Errorf("create qualification pricing: %w", err)
	}
	pricingID, _ := nestedMap(pricing["pricing_version"])["id"].(string)
	if pricingID == "" {
		return "", errors.New("create qualification pricing returned no ID")
	}
	if err := paymentLiveJSON(ctx, client, http.MethodPost, internalBase+"/pricing-versions/"+url.PathEscape(pricingID)+"/activate", internalToken, headers, nil, nil); err != nil {
		return "", fmt.Errorf("activate qualification pricing: %w", err)
	}
	usageID := paymentLiveQualificationUsageID(cfg.RunID, cfg.OrgID)
	digest := sha256.Sum256([]byte(usageID + ":" + cfg.OrgID))
	usageBody := map[string]any{
		"usage_id": usageID, "organization_id": cfg.OrgID,
		"service_code": "qualification", "metric_code": "staging_units", "quantity": 100,
		"quantity_scale": 0, "unit": "unit", "window_start": periodStart,
		"window_end": periodEnd, "source": "staging-qualification", "source_sha256": fmt.Sprintf("%x", digest),
	}
	if err := paymentLiveJSON(ctx, client, http.MethodPost, internalBase+"/usage-facts", internalToken, headers, usageBody, nil); err != nil {
		return "", fmt.Errorf("create qualification usage: %w", err)
	}
	var closed map[string]any
	if err := paymentLiveJSON(ctx, client, http.MethodPost, internalBase+"/periods/close", internalToken, headers, map[string]any{
		"organization_id": cfg.OrgID, "period_start": periodStart, "period_end": periodEnd, "due_at": periodEnd.Add(15 * 24 * time.Hour),
	}, &closed); err != nil {
		return "", fmt.Errorf("close qualification period: %w", err)
	}
	invoiceID, _ := nestedMap(closed["invoice"])["id"].(string)
	if invoiceID == "" {
		return "", errors.New("close qualification period returned no invoice ID")
	}
	return invoiceID, nil
}

func ensurePaymentLiveBillingProfile(ctx context.Context, client *http.Client, cfg paymentLiveConfig, billingToken string) error {
	endpoint := cfg.BillingBaseURL + "/v1/orgs/" + url.PathEscape(cfg.OrgID) + "/billing/profile"
	var current map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, endpoint, billingToken, "billing_profile.read", nil, nil, &current); err != nil {
		return fmt.Errorf("read billing profile: %w", err)
	}
	profile := nestedMap(current["billing_profile"])
	version := int64Value(profile["version"])
	if version < 1 {
		return errors.New("billing profile has no version")
	}
	requiresConfiguration, _ := profile["requires_configuration"].(bool)
	if !requiresConfiguration {
		return nil
	}
	headers := map[string]string{"If-Match": strconv.FormatInt(version, 10)}
	body := map[string]any{
		"legal_name": paymentLiveBootstrapOrgName, "locale": "zh-TW", "timezone": "Asia/Taipei",
		"delivery_preference": "portal", "version": version,
	}
	var updated map[string]any
	if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodPut, endpoint, billingToken, "billing_profile.manage", headers, body, &updated); err != nil {
		return fmt.Errorf("update billing profile: %w", err)
	}
	updatedProfile := nestedMap(updated["billing_profile"])
	configured, _ := updatedProfile["requires_configuration"].(bool)
	if configured || int64Value(updatedProfile["version"]) <= version {
		return errors.New("billing profile remains unconfigured")
	}
	return nil
}

func paymentLiveQualificationUsageID(runID, organizationID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(runID) + ":" + strings.TrimSpace(organizationID)))
	return "qualification-" + hex.EncodeToString(digest[:12])
}

func cleanupPaymentLive(ctx context.Context, client *http.Client, cfg paymentLiveConfig, token string, state paymentLiveState) error {
	base := cfg.BillingBaseURL + "/v1/orgs/" + url.PathEscape(cfg.OrgID)
	var issues []string
	if state.PolicyVersion > 0 {
		var current map[string]any
		if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodGet, base+"/auto-topup", token, "auto_topup.read", nil, nil, &current); err != nil {
			issues = append(issues, "read policy for cleanup: "+err.Error())
		} else if policy := nestedMap(current["auto_topup"]); policy["enabled"] != false {
			version := int64Value(policy["version"])
			if version <= 0 {
				issues = append(issues, "disable policy: current policy has no version")
			} else {
				var disabled map[string]any
				err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodDelete, base+"/auto-topup", token, "auto_topup.manage", map[string]string{"If-Match": strconv.Quote(strconv.FormatInt(version, 10))}, map[string]any{"reason": "staging qualification cleanup " + cfg.RunID}, &disabled)
				if err != nil {
					issues = append(issues, "disable policy: "+err.Error())
				}
			}
		}
	}
	if state.MethodID != "" {
		var revoked map[string]any
		if err := paymentLiveBillingJSON(ctx, client, cfg, http.MethodDelete, base+"/payment-methods/"+url.PathEscape(state.MethodID), token, "payment_method.manage", nil, map[string]any{"reason": "staging qualification cleanup " + cfg.RunID}, &revoked); err != nil {
			issues = append(issues, "revoke method: "+err.Error())
		}
	}
	if len(issues) > 0 {
		return errors.New(strings.Join(issues, "; "))
	}
	return nil
}

func paymentLiveBillingJSON(ctx context.Context, client *http.Client, cfg paymentLiveConfig, method, endpoint, token, permission string, headers map[string]string, body any, output any) error {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["X-Billing-Permissions"] = permission
	headers["X-Billing-Actor-Type"] = "user"
	headers["X-Billing-Actor-ID"] = cfg.OwnerUserID
	headers["X-Billing-Ownership-Version"] = strconv.FormatInt(cfg.OwnershipVersion, 10)
	if headers["X-Request-Id"] == "" {
		digest := sha256.Sum256([]byte(method + " " + endpoint))
		headers["X-Request-Id"] = fmt.Sprintf("payment-qualification-%x", digest[:8])
	}
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
		return &paymentLiveHTTPError{Method: method, Endpoint: endpoint, Status: response.StatusCode}
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
		for _, name := range []string{"LIVE-STG-SIMULATOR-001@desktop.png", "LIVE-STG-SIMULATOR-001@mobile.png", "LIVE-STG-SIMULATOR-001@newebpay-desktop.png", "LIVE-STG-SIMULATOR-001@newebpay-mobile.png"} {
			path := filepath.Join(outDir, "evidence", name)
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
	if err := os.WriteFile(filepath.Join(outDir, "test_report.md"), renderPaymentEvidenceReport(report), 0o644); err != nil {
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
		if err := os.WriteFile(filepath.Join(outDir, "test_report.md"), renderPaymentEvidenceReport(report), 0o644); err != nil {
			return err
		}
	}
	if err := writeJSON(filepath.Join(outDir, "redaction-report.json"), map[string]any{"schema_version": 1, "run_id": report.RunID, "status": redactionStatus, "generated_at": time.Now().UTC().Format(time.RFC3339), "findings": issues}); err != nil {
		return err
	}
	evidenceNames := []string{"results.json", "test_report.md", "execution.log", "cleanup-report.json", "redaction-report.json", "evidence/LIVE-STG-SIMULATOR-001@desktop.png", "evidence/LIVE-STG-SIMULATOR-001@mobile.png", "evidence/LIVE-STG-SIMULATOR-001@newebpay-desktop.png", "evidence/LIVE-STG-SIMULATOR-001@newebpay-mobile.png"}
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
