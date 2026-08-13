package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
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

type deploymentCredentialCheck struct {
	Name   string
	Passed bool
	Detail string
}

type deploymentCredentialChecker struct {
	client           *http.Client
	out              io.Writer
	linodeAPIRoot    string
	ghcrTokenRoot    string
	ghcrRegistryRoot string
	goDaddyAPIRoot   string
}

func defaultDeploymentCredentialEnvFile() string {
	if path := strings.TrimSpace(os.Getenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".env")
}

func defaultDeploymentCredentialChecker() deploymentCredentialChecker {
	return deploymentCredentialChecker{
		client:           &http.Client{Timeout: 15 * time.Second},
		out:              os.Stdout,
		linodeAPIRoot:    firstNonEmpty(os.Getenv("RTK_CLOUD_LINODE_API_ROOT"), "https://api.linode.com/v4"),
		ghcrTokenRoot:    firstNonEmpty(os.Getenv("RTK_CLOUD_GHCR_TOKEN_ROOT"), "https://ghcr.io/token"),
		ghcrRegistryRoot: firstNonEmpty(os.Getenv("RTK_CLOUD_GHCR_REGISTRY_ROOT"), "https://ghcr.io"),
		goDaddyAPIRoot:   strings.TrimRight(os.Getenv("RTK_CLOUD_GODADDY_API_ROOT"), "/"),
	}
}

func validateDeploymentCredentials(cfg deploymentConfig, envFile string) error {
	return defaultDeploymentCredentialChecker().check(cfg, envFile)
}

func (c deploymentCredentialChecker) check(cfg deploymentConfig, envFile string) error {
	if c.client == nil {
		c.client = &http.Client{Timeout: 15 * time.Second}
	}
	if c.out == nil {
		c.out = io.Discard
	}
	values, fileCheck := deploymentCredentialValues(envFile)
	checks := []deploymentCredentialCheck{fileCheck}
	if !fileCheck.Passed {
		return c.render(checks)
	}

	if cfg.Adapter == "lke" {
		checks = append(checks, c.checkLinode(values))
		checks = append(checks, c.checkGHCR(values)...)
	}
	if cfg.DNSAdapter == "godaddy" {
		checks = append(checks, c.checkGoDaddy(cfg, values))
	}
	if strings.EqualFold(strings.TrimSpace(cfg.Values["VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED"]), "true") {
		checks = append(checks, c.checkObjectStorage(values))
	}
	return c.render(checks)
}

func deploymentCredentialValues(envFile string) (map[string]string, deploymentCredentialCheck) {
	if strings.TrimSpace(envFile) == "" {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: "path is empty"}
	}
	abs, err := filepath.Abs(envFile)
	if err != nil {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: "path cannot be resolved"}
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, deploymentCredentialCheck{Name: "credential env file", Detail: abs + " does not exist"}
		}
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: "cannot be inspected"}
	}
	if !info.Mode().IsRegular() {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: abs + " is not a regular file"}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: abs + " must not be readable or writable by group/others (run chmod 600)"}
	}
	values, err := readEnvFile(abs)
	if err != nil {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: "cannot be read"}
	}
	for _, key := range deploymentCredentialKeys() {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values[key] = value
		}
	}
	return values, deploymentCredentialCheck{Name: "credential env file", Passed: true, Detail: abs + " loaded with secure permissions"}
}

func deploymentCredentialKeys() []string {
	return []string{
		"LINODE_TOKEN",
		"GHCR_PULL_USERNAME", "GHCR_PULL_TOKEN",
		"GODADDY_KEY", "GODADDY_SECRET",
		"LINODE_OBJ_ACCESS_KEY_ID", "LINODE_OBJ_SECRET_ACCESS_KEY", "LINODE_OBJ_ENDPOINT", "LINODE_OBJ_BUCKET", "LINODE_OBJ_REGION",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
	}
}

func (c deploymentCredentialChecker) checkLinode(values map[string]string) deploymentCredentialCheck {
	token := strings.TrimSpace(values["LINODE_TOKEN"])
	if token == "" {
		return deploymentCredentialCheck{Name: "Linode API", Detail: "LINODE_TOKEN is missing"}
	}
	for _, path := range []string{"/profile", "/lke/clusters?page_size=25"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, strings.TrimRight(c.linodeAPIRoot, "/")+path, nil)
		if err != nil {
			return deploymentCredentialCheck{Name: "Linode API", Detail: "request could not be created"}
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if _, err := c.request(req); err != nil {
			return deploymentCredentialCheck{Name: "Linode API", Detail: err.Error()}
		}
	}
	return deploymentCredentialCheck{Name: "Linode API", Passed: true, Detail: "authentication, profile, and LKE read access verified"}
}

func (c deploymentCredentialChecker) checkGHCR(values map[string]string) []deploymentCredentialCheck {
	username := strings.TrimSpace(values["GHCR_PULL_USERNAME"])
	token := strings.TrimSpace(values["GHCR_PULL_TOKEN"])
	if username == "" || token == "" {
		missing := []string{}
		if username == "" {
			missing = append(missing, "GHCR_PULL_USERNAME")
		}
		if token == "" {
			missing = append(missing, "GHCR_PULL_TOKEN")
		}
		verb := " is missing"
		if len(missing) > 1 {
			verb = " are missing"
		}
		return []deploymentCredentialCheck{{Name: "GHCR pull", Detail: strings.Join(missing, " and ") + verb}}
	}
	checks := make([]deploymentCredentialCheck, 0, len(lkeServiceImageSources()))
	for _, source := range lkeServiceImageSources() {
		repository := "hkt999rtk/" + source.RepoName + "/" + source.Name
		name := "GHCR pull " + repository
		registryToken, err := c.exchangeGHCRToken(username, token, repository)
		if err != nil {
			checks = append(checks, deploymentCredentialCheck{Name: name, Detail: err.Error()})
			continue
		}
		tagsURL := strings.TrimRight(c.ghcrRegistryRoot, "/") + "/v2/" + repository + "/tags/list?n=1"
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, tagsURL, nil)
		if err != nil {
			checks = append(checks, deploymentCredentialCheck{Name: name, Detail: "registry request could not be created"})
			continue
		}
		req.Header.Set("Authorization", "Bearer "+registryToken)
		if _, err := c.request(req); err != nil {
			checks = append(checks, deploymentCredentialCheck{Name: name, Detail: err.Error()})
			continue
		}
		checks = append(checks, deploymentCredentialCheck{Name: name, Passed: true, Detail: "token exchange and repository read access verified"})
	}
	return checks
}

func (c deploymentCredentialChecker) exchangeGHCRToken(username, token, repository string) (string, error) {
	u, err := url.Parse(c.ghcrTokenRoot)
	if err != nil {
		return "", errors.New("token endpoint is invalid")
	}
	query := u.Query()
	query.Set("service", "ghcr.io")
	query.Set("scope", "repository:"+repository+":pull")
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("token request could not be created")
	}
	req.SetBasicAuth(username, token)
	body, err := c.request(req)
	if err != nil {
		return "", err
	}
	var response struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", errors.New("token endpoint returned invalid JSON")
	}
	registryToken := firstNonEmpty(response.Token, response.AccessToken)
	if strings.TrimSpace(registryToken) == "" {
		return "", errors.New("token endpoint returned no registry token")
	}
	return registryToken, nil
}

func (c deploymentCredentialChecker) checkGoDaddy(cfg deploymentConfig, values map[string]string) deploymentCredentialCheck {
	key := strings.TrimSpace(values["GODADDY_KEY"])
	secret := strings.TrimSpace(values["GODADDY_SECRET"])
	if key == "" || secret == "" {
		return deploymentCredentialCheck{Name: "GoDaddy DNS", Detail: "GODADDY_KEY and GODADDY_SECRET are required"}
	}
	root := c.goDaddyAPIRoot
	if root == "" {
		root = "https://api.godaddy.com"
		if cfg.DNSValues["GODADDY_ENV"] == "ote" {
			root = "https://api.ote-godaddy.com"
		}
	}
	domain := strings.TrimSuffix(cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], ".")
	endpoint := root + "/v1/domains/" + url.PathEscape(domain) + "/records?limit=1"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint, nil)
	if err != nil {
		return deploymentCredentialCheck{Name: "GoDaddy DNS", Detail: "request could not be created"}
	}
	req.Header.Set("Authorization", "sso-key "+key+":"+secret)
	if _, err := c.request(req); err != nil {
		return deploymentCredentialCheck{Name: "GoDaddy DNS", Detail: err.Error()}
	}
	return deploymentCredentialCheck{Name: "GoDaddy DNS", Passed: true, Detail: "authentication and read access for " + domain + " verified"}
}

func (c deploymentCredentialChecker) checkObjectStorage(values map[string]string) deploymentCredentialCheck {
	store, err := provisionObjectStoreFromEnv(values)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: err.Error()}
	}
	query := url.Values{}
	query.Set("list-type", "2")
	query.Set("max-keys", "1")
	query.Set("prefix", "__rtk_cloud_deployment_preflight__/")
	body, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", query, nil)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: err.Error()}
	}
	var response provisionListBucketResult
	if err := xml.Unmarshal(body, &response); err != nil {
		return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: "bucket list returned invalid XML"}
	}
	return deploymentCredentialCheck{Name: "Linode Object Storage", Passed: true, Detail: "signed read access for configured bucket verified"}
}

func (c deploymentCredentialChecker) request(req *http.Request) ([]byte, error) {
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (c deploymentCredentialChecker) render(checks []deploymentCredentialCheck) error {
	failed := 0
	var output bytes.Buffer
	fmt.Fprintln(&output, "deployment credential preflight")
	for _, check := range checks {
		status := "PASS"
		if !check.Passed {
			status = "FAIL"
			failed++
		}
		fmt.Fprintf(&output, "[%s] %s: %s\n", status, check.Name, check.Detail)
	}
	if failed > 0 {
		fmt.Fprintf(&output, "overall: FAIL (%d/%d checks failed)\n", failed, len(checks))
		_, _ = io.Copy(c.out, &output)
		return fmt.Errorf("deployment credential preflight failed: %d check(s) failed", failed)
	}
	fmt.Fprintf(&output, "overall: PASS (%d checks)\n", len(checks))
	_, _ = io.Copy(c.out, &output)
	return nil
}
