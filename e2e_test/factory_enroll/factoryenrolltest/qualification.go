package factoryenrolltest

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

const QualificationSchema = "rtk-factory-enroll-qualification/v1"

type QualificationConfig struct {
	AccountManagerURL   string
	AdminEmail          string
	AdminPassword       string
	FactoryURL          string
	DeviceBaseURL       string
	DeviceCAFile        string
	RunID               string
	ArtifactDir         string
	Timeout             time.Duration
	AllowLoopbackTunnel bool
}

type QualificationResult struct {
	Schema              string            `json:"schema"`
	RunID               string            `json:"run_id"`
	StartedAt           time.Time         `json:"started_at"`
	EndedAt             time.Time         `json:"ended_at"`
	AccountManagerURL   string            `json:"account_manager_url"`
	FactoryURL          string            `json:"factory_url"`
	DeviceBaseURL       string            `json:"device_base_url"`
	BrandCloudID        string            `json:"brand_cloud_id"`
	DeviceItemProfileID string            `json:"device_item_profile_id"`
	ProductionRunID     string            `json:"production_run_id"`
	DeviceID            string            `json:"device_id"`
	IssuerRequestID     string            `json:"issuer_request_id"`
	TokenHTTPStatus     int               `json:"token_http_status"`
	Steps               map[string]string `json:"steps"`
}

type TokenProbe func(context.Context, string, string, string, string, time.Duration) (int, error)

type QualificationRunner struct {
	client     *http.Client
	tokenProbe TokenProbe
}

func NewQualificationRunner(client *http.Client, tokenProbe TokenProbe) *QualificationRunner {
	if client == nil {
		client = &http.Client{}
	}
	if tokenProbe == nil {
		tokenProbe = ProbeDeviceToken
	}
	return &QualificationRunner{client: client, tokenProbe: tokenProbe}
}

func DefaultQualificationConfigFromEnv() QualificationConfig {
	return QualificationConfig{
		AccountManagerURL:   os.Getenv("ACCOUNT_MANAGER_BASE_URL"),
		AdminEmail:          os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"),
		AdminPassword:       os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"),
		FactoryURL:          os.Getenv("FACTORY_ENROLL_TEST_URL"),
		DeviceBaseURL:       os.Getenv("VIDEO_CLOUD_DEVICE_BASE_URL"),
		DeviceCAFile:        os.Getenv("VIDEO_CLOUD_DEVICE_CA_FILE"),
		RunID:               envDefault("FACTORY_ENROLL_TEST_RUN_ID", time.Now().UTC().Format("20060102T150405Z")),
		ArtifactDir:         os.Getenv("FACTORY_ENROLL_TEST_ARTIFACT_DIR"),
		Timeout:             envDuration("FACTORY_ENROLL_TEST_TIMEOUT", 30*time.Second),
		AllowLoopbackTunnel: envBool("FACTORY_ENROLL_TEST_ALLOW_LOOPBACK_TUNNEL"),
	}
}

func (c *QualificationConfig) Normalize() error {
	c.AccountManagerURL = strings.TrimRight(strings.TrimSpace(c.AccountManagerURL), "/")
	c.FactoryURL = strings.TrimRight(strings.TrimSpace(c.FactoryURL), "/")
	c.DeviceBaseURL = strings.TrimRight(strings.TrimSpace(c.DeviceBaseURL), "/")
	for name, raw := range map[string]string{
		"account manager URL": c.AccountManagerURL,
		"factory URL":         c.FactoryURL,
		"device base URL":     c.DeviceBaseURL,
	} {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil {
			return fmt.Errorf("%s must be a valid URL", name)
		}
		loopbackTunnel := c.AllowLoopbackTunnel && parsed.Scheme == "http" &&
			(parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost") && name != "device base URL"
		if parsed.Scheme != "https" && !loopbackTunnel {
			return fmt.Errorf("%s must be HTTPS or an explicitly enabled Account Manager/factory loopback tunnel", name)
		}
	}
	if strings.TrimSpace(c.AdminEmail) == "" || strings.TrimSpace(c.AdminPassword) == "" {
		return errors.New("platform admin email and password are required")
	}
	if strings.TrimSpace(c.RunID) == "" {
		return errors.New("run ID is required")
	}
	if strings.TrimSpace(c.ArtifactDir) == "" {
		return errors.New("artifact directory is required")
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return nil
}

func (r *QualificationRunner) Run(ctx context.Context, cfg QualificationConfig) (*QualificationResult, error) {
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	result := &QualificationResult{
		Schema: QualificationSchema, RunID: cfg.RunID, StartedAt: started,
		AccountManagerURL: cfg.AccountManagerURL, FactoryURL: cfg.FactoryURL, DeviceBaseURL: cfg.DeviceBaseURL,
		Steps: map[string]string{},
	}
	accessToken, err := r.login(ctx, cfg)
	if err != nil {
		return nil, err
	}
	brandID, err := r.createBrandCloud(ctx, cfg, accessToken)
	if err != nil {
		return nil, err
	}
	result.BrandCloudID = brandID
	result.Steps["create_device_item_profile"] = "PENDING"
	profileID, err := r.createProfile(ctx, cfg, accessToken, brandID)
	if err != nil {
		return nil, err
	}
	result.DeviceItemProfileID = profileID
	result.Steps["create_device_item_profile"] = "PASS"
	productionRunID, productionJWT, err := r.createProductionRun(ctx, cfg, accessToken, brandID, profileID)
	if err != nil {
		return nil, err
	}
	result.ProductionRunID = productionRunID
	result.Steps["issue_production_jwt"] = "PASS"

	factoryArtifactDir := filepath.Join(cfg.ArtifactDir, "factory-runner")
	factoryResult, err := NewRunner(r.client).Run(ctx, Config{
		FactoryURL: cfg.FactoryURL, ProductionJWT: productionJWT, Count: 1, Concurrency: 1,
		RunID: cfg.RunID, FactoryID: "factory-e2e", LineID: "line-e2e", StationID: "station-e2e",
		FixtureID: "fixture-e2e", OperatorID: "operator-e2e", BatchID: "batch-" + cfg.RunID,
		Timeout: cfg.Timeout, ArtifactDir: factoryArtifactDir, SerialPrefix: "FQ", WriteKeyFiles: true,
	})
	productionJWT = ""
	if err != nil {
		return nil, err
	}
	if factoryResult.Summary.Successes != 1 || len(factoryResult.Devices) != 1 || !factoryResult.Devices[0].Success {
		return nil, errors.New("factory enrollment did not produce one successful device")
	}
	if err := os.MkdirAll(factoryArtifactDir, 0o700); err != nil {
		return nil, err
	}
	if err := WriteJSON(filepath.Join(factoryArtifactDir, "factory-enroll-results.json"), factoryResult); err != nil {
		return nil, err
	}
	if err := WriteMarkdown(filepath.Join(factoryArtifactDir, "factory-enroll-report.md"), factoryResult); err != nil {
		return nil, err
	}
	device := factoryResult.Devices[0]
	result.DeviceID = device.DeviceID
	result.IssuerRequestID = device.IssuerRequestID
	if result.IssuerRequestID == "" {
		return nil, errors.New("factory enrollment response missing issuer request ID")
	}
	result.Steps["generate_device_csr"] = "PASS"
	result.Steps["enroll_factory_identity"] = "PASS"
	result.Steps["verify_certissuer_mtls"] = "PASS"
	result.Steps["validate_device_certificate"] = "PASS"
	deviceMaterialRoot := filepath.Join(factoryArtifactDir, "device-material")
	defer func() { _ = os.RemoveAll(deviceMaterialRoot) }()
	deviceDir := filepath.Join(deviceMaterialRoot, "device-001")
	status, err := r.tokenProbe(ctx, cfg.DeviceBaseURL, filepath.Join(deviceDir, "device-bundle.crt"), filepath.Join(deviceDir, "device.key"), cfg.DeviceCAFile, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	if err := os.RemoveAll(deviceMaterialRoot); err != nil {
		return nil, fmt.Errorf("remove ephemeral device private material: %w", err)
	}
	result.TokenHTTPStatus = status
	result.Steps["bootstrap_device_token"] = "PASS"
	result.EndedAt = time.Now().UTC()
	if err := os.MkdirAll(cfg.ArtifactDir, 0o700); err != nil {
		return nil, err
	}
	if err := writeQualificationJSON(filepath.Join(cfg.ArtifactDir, "factory-qualification-results.json"), result); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(cfg.ArtifactDir, "factory-qualification-report.md"), []byte(renderQualificationMarkdown(result)), 0o644); err != nil {
		return nil, err
	}
	junit := fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?><testsuite name=\"factory-production-qualification\" tests=\"1\" failures=\"0\"><testcase classname=\"factory-production\" name=\"%s\"/></testsuite>\n", result.RunID)
	if err := os.WriteFile(filepath.Join(cfg.ArtifactDir, "factory-qualification-junit.xml"), []byte(junit), 0o644); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *QualificationRunner) login(ctx context.Context, cfg QualificationConfig) (string, error) {
	var out struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := r.postJSON(ctx, cfg.AccountManagerURL+"/v1/auth/login", "", map[string]string{"email": cfg.AdminEmail, "password": cfg.AdminPassword}, http.StatusOK, &out); err != nil {
		return "", fmt.Errorf("platform admin login: %w", err)
	}
	if out.Tokens.AccessToken == "" {
		return "", errors.New("platform admin login returned no access token")
	}
	return out.Tokens.AccessToken, nil
}

func (r *QualificationRunner) createBrandCloud(ctx context.Context, cfg QualificationConfig, token string) (string, error) {
	var out struct {
		BrandCloud struct {
			ID string `json:"id"`
		} `json:"brand_cloud"`
	}
	name := "RTK-FQ-FACTORY-" + cfg.RunID
	if err := r.postJSON(ctx, cfg.AccountManagerURL+"/v1/admin/brand-clouds", token, map[string]any{"name": name, "metadata": map[string]string{"e2e_run_id": cfg.RunID}}, http.StatusCreated, &out); err != nil {
		return "", fmt.Errorf("create brand cloud: %w", err)
	}
	if out.BrandCloud.ID == "" {
		return "", errors.New("create brand cloud returned no ID")
	}
	return out.BrandCloud.ID, nil
}

func (r *QualificationRunner) createProfile(ctx context.Context, cfg QualificationConfig, token, brandID string) (string, error) {
	var out struct {
		DeviceItemProfile struct {
			ID string `json:"id"`
		} `json:"device_item_profile"`
	}
	payload := map[string]any{
		"profile_key": "fq-factory-" + strings.ToLower(cfg.RunID), "display_name": "Factory Qualification " + cfg.RunID,
		"category": "ip_camera", "ca_profile": "factory-device", "issuer_profile": "factory-e2e",
		"service_options": []string{"mqtt", "video_streaming", "video_storage"},
	}
	endpoint := fmt.Sprintf("%s/v1/admin/brand-clouds/%s/device-item-profiles", cfg.AccountManagerURL, url.PathEscape(brandID))
	if err := r.postJSON(ctx, endpoint, token, payload, http.StatusCreated, &out); err != nil {
		return "", fmt.Errorf("create device item profile: %w", err)
	}
	if out.DeviceItemProfile.ID == "" {
		return "", errors.New("create device item profile returned no ID")
	}
	return out.DeviceItemProfile.ID, nil
}

func (r *QualificationRunner) createProductionRun(ctx context.Context, cfg QualificationConfig, token, brandID, profileID string) (string, string, error) {
	var out struct {
		ProductionRun struct {
			ID string `json:"id"`
		} `json:"production_run"`
		FactoryJWT string `json:"factory_jwt"`
	}
	now := time.Now().UTC()
	payload := map[string]any{
		"factory_id": "factory-e2e", "batch_id": "batch-" + cfg.RunID, "allowed_quantity": 1,
		"valid_from": now.Add(-time.Minute).Format(time.RFC3339), "valid_until": now.Add(time.Hour).Format(time.RFC3339),
	}
	endpoint := fmt.Sprintf("%s/v1/admin/brand-clouds/%s/device-item-profiles/%s/production-runs", cfg.AccountManagerURL, url.PathEscape(brandID), url.PathEscape(profileID))
	if err := r.postJSON(ctx, endpoint, token, payload, http.StatusCreated, &out); err != nil {
		return "", "", fmt.Errorf("create production run: %w", err)
	}
	if out.ProductionRun.ID == "" || out.FactoryJWT == "" {
		return "", "", errors.New("create production run returned incomplete production JWT data")
	}
	return out.ProductionRun.ID, out.FactoryJWT, nil
}

func (r *QualificationRunner) postJSON(ctx context.Context, endpoint, bearer string, payload any, expected int, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != expected {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

func ProbeDeviceToken(ctx context.Context, baseURL, certFile, keyFile, caFile string, timeout time.Duration) (int, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return 0, fmt.Errorf("load device mTLS identity: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if strings.TrimSpace(caFile) != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return 0, fmt.Errorf("read device CA: %w", err)
		}
		if !roots.AppendCertsFromPEM(raw) {
			return 0, errors.New("device CA file contains no certificates")
		}
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, RootCAs: roots,
	}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/request_token", strings.NewReader(`{"scope":"device"}`))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("device mTLS token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("device mTLS token request returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokenResponse map[string]any
	if err := json.Unmarshal(body, &tokenResponse); err != nil {
		return resp.StatusCode, errors.New("device mTLS token response is not JSON")
	}
	if token, _ := tokenResponse["access_token"].(string); token == "" {
		return resp.StatusCode, errors.New("device mTLS token response missing access token")
	}
	return resp.StatusCode, nil
}

func writeQualificationJSON(path string, result *QualificationResult) error {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func renderQualificationMarkdown(result *QualificationResult) string {
	var body strings.Builder
	fmt.Fprintf(&body, "# Factory Production Qualification\n\n")
	fmt.Fprintf(&body, "- Run ID: `%s`\n", result.RunID)
	fmt.Fprintf(&body, "- Result: **PASS**\n")
	fmt.Fprintf(&body, "- Brand Cloud: `%s`\n", result.BrandCloudID)
	fmt.Fprintf(&body, "- Device item profile: `%s`\n", result.DeviceItemProfileID)
	fmt.Fprintf(&body, "- Production run: `%s`\n", result.ProductionRunID)
	fmt.Fprintf(&body, "- Device: `%s`\n", result.DeviceID)
	fmt.Fprintf(&body, "- Issuer request: `%s`\n", result.IssuerRequestID)
	fmt.Fprintf(&body, "- Device token HTTP status: `%d`\n\n", result.TokenHTTPStatus)
	fmt.Fprintf(&body, "| Workflow step | Status |\n| --- | --- |\n")
	steps := []string{"create_device_item_profile", "issue_production_jwt", "generate_device_csr", "enroll_factory_identity", "verify_certissuer_mtls", "validate_device_certificate", "bootstrap_device_token"}
	for _, step := range steps {
		fmt.Fprintf(&body, "| `%s` | **%s** |\n", step, result.Steps[step])
	}
	return body.String()
}
