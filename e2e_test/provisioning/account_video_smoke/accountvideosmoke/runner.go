package accountvideosmoke

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func Plan(cfg Config) Result {
	started := time.Now().UTC()
	result := Result{
		Schema:    ResultSchema,
		RunID:     cfg.RunID,
		StartedAt: started,
		Config:    sanitizedConfig(cfg),
		Artifacts: map[string]string{},
		Metadata:  map[string]string{},
	}
	if cfg.AccountManagerBaseURL == "" {
		result.add("validate_config", StatusBlocked, "ACCOUNT_MANAGER_BASE_URL is not configured", 0, "")
	}
	if cfg.VideoCloudBaseURL == "" {
		result.add("validate_config", StatusBlocked, "VIDEO_CLOUD_BASE_URL is not configured", 0, "")
	}
	if _, err := LoadAccountFixture(cfg.AccountUsersDir); err != nil {
		result.add("load_account_fixture", StatusBlocked, err.Error(), 0, "")
	} else {
		result.add("load_account_fixture", StatusPass, "account fixture loaded", 0, "credentials.json present; secret fields not reported")
	}
	if certset, err := LoadDeviceCertset(cfg.DeviceCertsetDir, cfg.DeviceID); err != nil {
		result.add("load_device_certset", StatusBlocked, err.Error(), 0, "")
	} else {
		result.add("load_device_certset", StatusPass, "device certset loaded", 0, certset.Summary())
	}
	result.EndedAt = time.Now().UTC()
	result.Overall = result.computeOverall()
	return result
}

func Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.ProvisioningPollInterval == 0 {
		cfg.ProvisioningPollInterval = 2 * time.Second
	}
	if cfg.ProvisioningPollAttempts == 0 {
		cfg.ProvisioningPollAttempts = 6
	}
	started := time.Now().UTC()
	result := Result{
		Schema:    ResultSchema,
		RunID:     cfg.RunID,
		StartedAt: started,
		Config:    sanitizedConfig(cfg),
		Artifacts: map[string]string{},
		Metadata:  map[string]string{},
	}
	defer func() {
		result.EndedAt = time.Now().UTC()
		result.Overall = result.computeOverall()
	}()

	if cfg.AccountManagerBaseURL == "" {
		result.add("validate_config", StatusBlocked, "ACCOUNT_MANAGER_BASE_URL is not configured", 0, "")
		return result.finish(), nil
	}
	if cfg.VideoCloudBaseURL == "" {
		result.add("validate_config", StatusBlocked, "VIDEO_CLOUD_BASE_URL is not configured", 0, "")
		return result.finish(), nil
	}
	result.add("validate_config", StatusPass, "required base URLs configured", 0, "")

	accountFixture, err := LoadAccountFixture(cfg.AccountUsersDir)
	if err != nil {
		result.add("load_account_fixture", StatusBlocked, err.Error(), 0, "")
		return result.finish(), nil
	}
	result.add("load_account_fixture", StatusPass, "account fixture loaded", 0, "email and organization id loaded; password redacted")

	certset, err := LoadDeviceCertset(cfg.DeviceCertsetDir, cfg.DeviceID)
	if err != nil {
		result.add("load_device_certset", StatusBlocked, err.Error(), 0, "")
		return result.finish(), nil
	}
	result.add("load_device_certset", StatusPass, "device certset loaded", 0, certset.Summary())
	result.Metadata["video_cloud_devid"] = certset.DeviceID

	httpClient := &http.Client{Timeout: cfg.Timeout}
	am := apiClient{baseURL: strings.TrimRight(cfg.AccountManagerBaseURL, "/"), http: httpClient}
	loginToken, status, evidence, err := login(ctx, am, accountFixture.Email, accountFixture.Password)
	if err != nil {
		result.add("account_login", StatusFail, err.Error(), status, evidence)
		return result.finish(), nil
	}
	result.add("account_login", StatusPass, "test user login succeeded", status, "access token redacted")

	claimToken := cfg.ClaimToken
	if claimToken == "" {
		adminToken, ok, status, evidence := platformAdminToken(ctx, am)
		if !ok {
			result.add("create_claim_token", StatusBlocked, "platform-admin credentials or ACCOUNT_MANAGER_PLATFORM_ADMIN_TOKEN are required to create a Claim Token", status, evidence)
			return result.finish(), nil
		}
		raw, status, evidence, err := createClaimToken(ctx, am, adminToken, accountFixture.OrganizationID, certset.DeviceID)
		if err != nil {
			result.add("create_claim_token", StatusFail, err.Error(), status, evidence)
			return result.finish(), nil
		}
		claimToken = raw
		result.add("create_claim_token", StatusPass, "Claim Token created", status, "raw Claim Token redacted")
	} else {
		result.add("create_claim_token", StatusSkip, "E2E_CLAIM_TOKEN provided; admin create skipped", 0, "raw Claim Token redacted")
	}

	resolve, status, evidence, err := resolveClaim(ctx, am, loginToken, accountFixture.OrganizationID, claimToken, cfg.deviceName())
	if err != nil {
		result.add("resolve_claim_token", StatusFail, err.Error(), status, evidence)
		return result.finish(), nil
	}
	result.add("resolve_claim_token", StatusPass, "Claim Token resolved", status, fmt.Sprintf("device_id=%s video_cloud_devid=%s", resolve.Device.ID, resolve.ProvisionInput.VideoCloudDeviceID))

	status, evidence, err = startProvision(ctx, am, loginToken, accountFixture.OrganizationID, resolve.Device.ID, resolve.ProvisionInput)
	if err != nil {
		result.add("start_account_provisioning", StatusFail, err.Error(), status, evidence)
		return result.finish(), nil
	}
	result.add("start_account_provisioning", StatusPass, "account-side provisioning operation accepted", status, evidence)

	status, evidence, err = pollProvisioning(ctx, am, loginToken, accountFixture.OrganizationID, resolve.Device.ID, cfg.ProvisioningPollAttempts, cfg.ProvisioningPollInterval)
	if err != nil {
		result.add("read_account_provisioning", StatusFail, err.Error(), status, evidence)
		return result.finish(), nil
	}
	result.add("read_account_provisioning", StatusPass, "account-side provisioning state read", status, evidence)

	status, evidence, err = deviceMTLSTokenSmoke(ctx, cfg, certset)
	if errors.Is(err, errBlocked) {
		result.add("device_mtls_token_smoke", StatusBlocked, strings.TrimPrefix(err.Error(), errBlocked.Error()+": "), status, evidence)
		return result.finish(), nil
	}
	if err != nil {
		result.add("device_mtls_token_smoke", StatusFail, err.Error(), status, evidence)
		return result.finish(), nil
	}
	result.add("device_mtls_token_smoke", StatusPass, "device mTLS token request succeeded", status, evidence)

	return result.finish(), nil
}

func (r *Result) add(name string, status Status, reason string, code int, evidence string) {
	r.Steps = append(r.Steps, StepResult{Name: name, Status: status, Reason: Redact(reason), StatusCode: code, Evidence: Redact(evidence)})
}

func (r Result) computeOverall() Status {
	overall := StatusPass
	for _, step := range r.Steps {
		switch step.Status {
		case StatusFail:
			return StatusFail
		case StatusBlocked:
			overall = StatusBlocked
		}
	}
	return overall
}

func (r Result) finish() Result {
	r.EndedAt = time.Now().UTC()
	r.Overall = r.computeOverall()
	return r
}

func sanitizedConfig(cfg Config) Config {
	cfg.ClaimToken = ""
	return cfg
}

func (cfg Config) deviceName() string {
	if cfg.DeviceName != "" {
		return cfg.DeviceName
	}
	return "Factory Enrolled Device"
}

type apiClient struct {
	baseURL string
	http    *http.Client
}

func (c apiClient) postJSON(ctx context.Context, path, bearer string, req any, out any) (int, string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	return c.do(httpReq, out)
}

func (c apiClient) getJSON(ctx context.Context, path, bearer string, out any) (int, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, "", err
	}
	if bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	return c.do(httpReq, out)
}

func (c apiClient) do(req *http.Request, out any) (int, string, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	evidence := Redact(string(b))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, evidence, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, evidence, fmt.Errorf("parse response: %w", err)
		}
	}
	return resp.StatusCode, evidence, nil
}

func login(ctx context.Context, client apiClient, email, password string) (string, int, string, error) {
	var resp struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	status, evidence, err := client.postJSON(ctx, "/v1/auth/login", "", map[string]string{"email": email, "password": password}, &resp)
	if err != nil {
		return "", status, evidence, err
	}
	if resp.Tokens.AccessToken == "" {
		return "", status, evidence, errors.New("login response missing access token")
	}
	return resp.Tokens.AccessToken, status, evidence, nil
}

func platformAdminToken(ctx context.Context, client apiClient) (string, bool, int, string) {
	if token := os.Getenv("ACCOUNT_MANAGER_PLATFORM_ADMIN_TOKEN"); token != "" {
		return token, true, 0, "ACCOUNT_MANAGER_PLATFORM_ADMIN_TOKEN configured"
	}
	email := os.Getenv("ACCOUNT_MANAGER_PLATFORM_ADMIN_EMAIL")
	password := os.Getenv("ACCOUNT_MANAGER_PLATFORM_ADMIN_PASSWORD")
	if email == "" || password == "" {
		return "", false, 0, ""
	}
	token, status, evidence, err := login(ctx, client, email, password)
	if err != nil {
		return "", false, status, evidence
	}
	return token, true, status, "platform admin login succeeded"
}

func createClaimToken(ctx context.Context, client apiClient, bearer, orgID, devid string) (string, int, string, error) {
	expires := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	req := map[string]any{
		"organization_id":   orgID,
		"category":          "ip_camera",
		"video_cloud_devid": devid,
		"activity_id":       "activity-" + time.Now().UTC().Format("20060102T150405Z"),
		"clip_public_key":   "e2e-smoke-clip-public-key",
		"expires_at":        expires,
		"metadata": map[string]string{
			"source": "workspace-account-video-smoke",
		},
	}
	var resp struct {
		ClaimToken string `json:"claim_token"`
	}
	status, evidence, err := client.postJSON(ctx, "/v1/admin/device-claim-tokens", bearer, req, &resp)
	if err != nil {
		return "", status, evidence, err
	}
	if resp.ClaimToken == "" {
		return "", status, evidence, errors.New("claim token create response did not include generated raw token")
	}
	return resp.ClaimToken, status, evidence, nil
}

type claimResolveResponse struct {
	ClaimID string `json:"claim_id"`
	Device  struct {
		ID string `json:"id"`
	} `json:"device"`
	ProvisionInput provisionInput `json:"provision_input"`
}

type provisionInput struct {
	VideoCloudDeviceID string `json:"video_cloud_devid"`
	ActivityID         string `json:"activity_id"`
	ClipPublicKey      string `json:"clip_public_key"`
}

func resolveClaim(ctx context.Context, client apiClient, bearer, orgID, claimToken, deviceName string) (claimResolveResponse, int, string, error) {
	var resp claimResolveResponse
	path := fmt.Sprintf("/v1/orgs/%s/devices/claim/resolve", url.PathEscape(orgID))
	status, evidence, err := client.postJSON(ctx, path, bearer, map[string]string{"claim_token": claimToken, "device_name": deviceName}, &resp)
	if err != nil {
		return resp, status, evidence, err
	}
	if resp.Device.ID == "" || resp.ProvisionInput.VideoCloudDeviceID == "" {
		return resp, status, evidence, errors.New("claim resolve response missing device or provisioning input")
	}
	return resp, status, evidence, nil
}

func startProvision(ctx context.Context, client apiClient, bearer, orgID, deviceID string, input provisionInput) (int, string, error) {
	path := fmt.Sprintf("/v1/orgs/%s/devices/%s/provision", url.PathEscape(orgID), url.PathEscape(deviceID))
	req := map[string]string{
		"video_cloud_devid": input.VideoCloudDeviceID,
		"activity_id":       input.ActivityID,
		"clip_public_key":   input.ClipPublicKey,
		"operation_id":      "workspace-smoke-" + time.Now().UTC().Format("20060102T150405Z"),
	}
	return client.postJSON(ctx, path, bearer, req, nil)
}

func pollProvisioning(ctx context.Context, client apiClient, bearer, orgID, deviceID string, attempts int, interval time.Duration) (int, string, error) {
	path := fmt.Sprintf("/v1/orgs/%s/devices/%s/provisioning", url.PathEscape(orgID), url.PathEscape(deviceID))
	var lastStatus int
	var lastEvidence string
	for i := 0; i < attempts; i++ {
		var resp struct {
			Readiness struct {
				State        string `json:"state"`
				ProductState string `json:"product_state"`
			} `json:"readiness"`
		}
		status, evidence, err := client.getJSON(ctx, path, bearer, &resp)
		lastStatus, lastEvidence = status, evidence
		if err != nil {
			return status, evidence, err
		}
		if resp.Readiness.State != "" {
			return status, fmt.Sprintf("readiness=%s product_state=%s", resp.Readiness.State, resp.Readiness.ProductState), nil
		}
		time.Sleep(interval)
	}
	return lastStatus, lastEvidence, errors.New("provisioning state did not expose readiness before poll attempts ended")
}

var errBlocked = errors.New("blocked")

func deviceMTLSTokenSmoke(ctx context.Context, cfg Config, certset DeviceCertset) (int, string, error) {
	base := strings.TrimRight(cfg.VideoCloudDeviceBaseURL, "/")
	if base == "" {
		base = strings.TrimRight(os.Getenv("VIDEO_CLOUD_DEVICE_BASE_URL"), "/")
	}
	if base == "" {
		return 0, "", fmt.Errorf("%w: VIDEO_CLOUD_DEVICE_BASE_URL not configured for device-facing mTLS endpoint", errBlocked)
	}
	cert, err := tls.LoadX509KeyPair(certset.DeviceChainPath, certset.DeviceKeyPath)
	if err != nil {
		return 0, "", fmt.Errorf("%w: load device mTLS keypair: %v", errBlocked, err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if certset.DeviceCAPath != "" {
		if b, err := os.ReadFile(certset.DeviceCAPath); err == nil {
			roots.AppendCertsFromPEM(b)
		}
	}
	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
			RootCAs:      roots,
		}},
	}
	body := strings.NewReader(`{"scope":"device"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/request_token", body)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("%w: device mTLS token request failed: %v", errBlocked, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	evidence := Redact(string(b))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, evidence, fmt.Errorf("device mTLS token request returned HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, "token response received; token redacted", nil
}

func WriteArtifacts(result Result, artifactDir string) error {
	if artifactDir == "" {
		return nil
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return err
	}
	jsonPath := filepath.Join(artifactDir, "account-video-smoke-results.json")
	reportPath := filepath.Join(artifactDir, "account-video-smoke-report.md")
	if err := WriteJSON(jsonPath, result); err != nil {
		return err
	}
	if err := WriteMarkdown(reportPath, result); err != nil {
		return err
	}
	return nil
}
