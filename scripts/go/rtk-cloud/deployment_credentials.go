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

type deploymentCredentialCheckOptions struct {
	createMissingObjectStorageBucket bool
	grantObjectStorageBucketAccess   bool
	envFile                          string
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
	return filepath.Join(home, ".config", "rtk-cloud", "shared.env")
}

func defaultDeploymentEnvironmentCredentialFile(environment string) string {
	if path := strings.TrimSpace(os.Getenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || strings.TrimSpace(environment) == "" {
		return ""
	}
	return filepath.Join(home, ".config", "rtk-cloud", "environments", environment+".env")
}

func defaultDeploymentSharedCredentialFile() string {
	if path := strings.TrimSpace(os.Getenv("RTK_CLOUD_SHARED_CREDENTIAL_ENV_FILE")); path != "" {
		return path
	}
	return defaultDeploymentCredentialEnvFile()
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

func validateAndBootstrapDeploymentCredentials(cfg deploymentConfig, envFile string) error {
	if cfg.Storage.RuntimeMedia.Bucket != "" {
		values, check := deploymentCredentialProfileValues(cfg.Environment, envFile, defaultDeploymentSharedCredentialFile())
		if !check.Passed {
			return errors.New(check.Detail)
		}
		return defaultDeploymentCredentialChecker().bootstrapRuntimeStorage(cfg, values, envFile)
	}
	return defaultDeploymentCredentialChecker().checkWithOptions(cfg, envFile, deploymentCredentialCheckOptions{
		createMissingObjectStorageBucket: true,
		envFile:                          envFile,
	})
}

func validateAndGrantDeploymentObjectStorageAccess(cfg deploymentConfig, envFile string) error {
	if cfg.Storage.RuntimeMedia.Bucket != "" {
		return validateAndBootstrapDeploymentCredentials(cfg, envFile)
	}
	return defaultDeploymentCredentialChecker().checkWithOptions(cfg, envFile, deploymentCredentialCheckOptions{
		grantObjectStorageBucketAccess: true,
		envFile:                        envFile,
	})
}

func (c deploymentCredentialChecker) check(cfg deploymentConfig, envFile string) error {
	return c.checkWithOptions(cfg, envFile, deploymentCredentialCheckOptions{})
}

func (c deploymentCredentialChecker) checkWithOptions(cfg deploymentConfig, envFile string, options deploymentCredentialCheckOptions) error {
	if c.client == nil {
		c.client = &http.Client{Timeout: 15 * time.Second}
	}
	if c.out == nil {
		c.out = io.Discard
	}
	values, fileCheck := deploymentCredentialProfileValues(cfg.Environment, envFile, defaultDeploymentSharedCredentialFile())
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
		checks = append(checks, c.checkObjectStorageWithOptions(cfg, values, options))
		if cfg.Storage.ReleaseArtifacts.Bucket != "" {
			checks = append(checks, c.checkResolvedArtifactStorage(cfg, values))
		}
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

func deploymentCredentialProfileValues(environment, environmentFile, sharedFile string) (map[string]string, deploymentCredentialCheck) {
	environmentFile = strings.TrimSpace(environmentFile)
	sharedFile = strings.TrimSpace(sharedFile)
	if environmentFile == "" && strings.TrimSpace(environment) == "" {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: "path is empty"}
	}
	if environmentFile == "" {
		environmentFile = defaultDeploymentEnvironmentCredentialFile(environment)
	}
	defaultEnvironmentFile := defaultDeploymentEnvironmentCredentialFile(environment)
	paths := []string{}
	if sharedFile != "" && sharedFile != environmentFile {
		if _, err := os.Stat(sharedFile); err == nil {
			paths = append(paths, sharedFile)
		} else if os.IsNotExist(err) {
			if environmentFile == defaultEnvironmentFile {
				return nil, deploymentCredentialCheck{Name: "credential env file", Detail: sharedFile + " does not exist"}
			}
		} else if !os.IsNotExist(err) {
			return nil, deploymentCredentialCheck{Name: "credential env file", Detail: "shared profile cannot be inspected"}
		}
	}
	if _, err := os.Stat(environmentFile); err == nil {
		paths = append(paths, environmentFile)
	} else if os.IsNotExist(err) {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: environmentFile + " does not exist"}
	} else {
		return nil, deploymentCredentialCheck{Name: "credential env file", Detail: "environment profile cannot be inspected"}
	}
	values := map[string]string{}
	for _, path := range paths {
		part, check := deploymentCredentialValuesFromFile(path)
		if !check.Passed {
			check.Name = "credential env file"
			return nil, check
		}
		for key, value := range part {
			values[key] = value
		}
	}
	for _, key := range deploymentCredentialKeys() {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			values[key] = value
		}
	}
	// Scoped credentials are canonical. Legacy names exist only in this in-memory
	// child/runtime view so older deployment code never needs to read profile files.
	if value := values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"]; value != "" {
		values["LINODE_OBJ_ACCESS_KEY_ID"] = value
	}
	if value := values["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"]; value != "" {
		values["LINODE_OBJ_SECRET_ACCESS_KEY"] = value
	}
	detail := strings.Join(paths, ", ") + " loaded with secure permissions (process environment > environment profile > shared profile)"
	return values, deploymentCredentialCheck{Name: "credential env file", Passed: true, Detail: detail}
}

func deploymentCredentialValuesFromFile(path string) (map[string]string, deploymentCredentialCheck) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, deploymentCredentialCheck{Detail: "path cannot be resolved"}
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, deploymentCredentialCheck{Detail: abs + " does not exist"}
		}
		return nil, deploymentCredentialCheck{Detail: "cannot be inspected"}
	}
	if !info.Mode().IsRegular() {
		return nil, deploymentCredentialCheck{Detail: abs + " is not a regular file"}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, deploymentCredentialCheck{Detail: abs + " must not be readable or writable by group/others (run chmod 600)"}
	}
	values, err := readEnvFile(abs)
	if err != nil {
		return nil, deploymentCredentialCheck{Detail: "cannot be read"}
	}
	return values, deploymentCredentialCheck{Passed: true, Detail: abs + " loaded with secure permissions"}
}

func deploymentCredentialKeys() []string {
	return []string{
		"LINODE_TOKEN",
		"GHCR_PULL_USERNAME", "GHCR_PULL_TOKEN",
		"GODADDY_KEY", "GODADDY_SECRET",
		"LINODE_OBJ_ACCESS_KEY_ID", "LINODE_OBJ_SECRET_ACCESS_KEY", "LINODE_OBJ_ENDPOINT", "LINODE_OBJ_BUCKET", "LINODE_OBJ_REGION",
		"LINODE_MEDIA_OBJ_ACCESS_KEY_ID", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY",
		"LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID", "LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
	}
}

func installDeploymentChildCredentialEnvironment(values map[string]string) func() {
	keys := deploymentCredentialKeys()
	previous := make(map[string]*string, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			copy := value
			previous[key] = &copy
		} else {
			previous[key] = nil
		}
		if value := strings.TrimSpace(values[key]); value != "" {
			_ = os.Setenv(key, value)
		}
	}
	return func() {
		for _, key := range keys {
			if previous[key] == nil {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, *previous[key])
			}
		}
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
	return c.checkObjectStorageWithOptions(deploymentConfig{}, values, deploymentCredentialCheckOptions{})
}

func (c deploymentCredentialChecker) checkObjectStorageWithOptions(cfg deploymentConfig, values map[string]string, options deploymentCredentialCheckOptions) deploymentCredentialCheck {
	if cfg.Storage.RuntimeMedia.Bucket != "" {
		return c.checkResolvedObjectStorage(cfg, values)
	}
	store, err := provisionObjectStoreFromEnv(values)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: err.Error()}
	}
	query := url.Values{}
	query.Set("list-type", "2")
	query.Set("max-keys", "1")
	query.Set("prefix", "__rtk_cloud_deployment_preflight__/")
	body, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", query, nil)
	created := false
	granted := false
	if err != nil {
		var storageErr *provisionObjectStorageHTTPError
		if options.createMissingObjectStorageBucket && errors.As(err, &storageErr) && storageErr.StatusCode == http.StatusNotFound {
			if err := provisionCreateObjectBucketWithClient(c.client, store); err != nil {
				return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: "configured bucket is missing and creation failed: " + err.Error()}
			}
			created = true
			body, err = provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", query, nil)
			if err != nil {
				return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: "configured bucket was created but read revalidation failed: " + err.Error()}
			}
		} else if options.grantObjectStorageBucketAccess && errors.As(err, &storageErr) && storageErr.StatusCode == http.StatusForbidden {
			accessKey, secretKey, keyErr := c.createLimitedObjectStorageKey(cfg, values, store)
			if keyErr != nil {
				return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: "create replacement limited access key failed: " + keyErr.Error()}
			}
			replacement := store
			replacement.accessKey = accessKey
			replacement.secretKey = secretKey
			body, err = provisionSignedObjectRequestWithClient(c.client, replacement, http.MethodGet, "", query, nil)
			if err != nil {
				return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: "replacement limited access key read validation failed: " + err.Error()}
			}
			if err := updateDeploymentCredentialEnvFile(options.envFile, map[string]string{
				"LINODE_OBJ_ACCESS_KEY_ID":     accessKey,
				"LINODE_OBJ_SECRET_ACCESS_KEY": secretKey,
			}); err != nil {
				return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: "replacement limited access key passed validation but operator env update failed"}
			}
			granted = true
		} else {
			return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: err.Error()}
		}
	}
	var response provisionListBucketResult
	if err := xml.Unmarshal(body, &response); err != nil {
		return deploymentCredentialCheck{Name: "Linode Object Storage", Detail: "bucket list returned invalid XML"}
	}
	detail := "signed read access for configured bucket verified"
	if created {
		detail = "configured bucket created with Object Storage access key; signed read access revalidated"
	}
	if granted {
		detail = "replacement limited access key created for configured bucket; signed read access revalidated; operator env updated"
	}
	return deploymentCredentialCheck{Name: "Linode Object Storage", Passed: true, Detail: detail}
}

func (c deploymentCredentialChecker) createLimitedObjectStorageKey(cfg deploymentConfig, values map[string]string, store provisionObjectStore) (string, string, error) {
	token := strings.TrimSpace(values["LINODE_TOKEN"])
	if token == "" {
		return "", "", errors.New("LINODE_TOKEN is required")
	}
	bucketsURL := strings.TrimRight(c.linodeAPIRoot, "/") + "/object-storage/buckets?page_size=500"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, bucketsURL, nil)
	if err != nil {
		return "", "", errors.New("bucket inventory request could not be created")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	body, err := c.request(req)
	if err != nil {
		return "", "", fmt.Errorf("bucket inventory request failed: %w", err)
	}
	var inventory struct {
		Data []struct {
			Label   string `json:"label"`
			Region  string `json:"region"`
			Cluster string `json:"cluster"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &inventory); err != nil {
		return "", "", errors.New("bucket inventory returned invalid JSON")
	}
	region := ""
	for _, bucket := range inventory.Data {
		if bucket.Label != store.bucket {
			continue
		}
		if bucket.Cluster == store.region || bucket.Region == store.region {
			region = firstNonEmpty(bucket.Region, bucket.Cluster)
			break
		}
		if region == "" {
			region = firstNonEmpty(bucket.Region, bucket.Cluster)
		}
	}
	if region == "" {
		return "", "", errors.New("configured bucket was not found in Linode Object Storage inventory")
	}
	labelEnvironment := strings.TrimSpace(cfg.Environment)
	if labelEnvironment == "" {
		labelEnvironment = "deployment"
	}
	purpose := "runtime-media"
	if store.bucket == cfg.Storage.ReleaseArtifacts.Bucket {
		purpose = "release-artifacts"
	}
	payload, err := json.Marshal(map[string]any{
		"label": fmt.Sprintf("rtk-cloud-%s-%s-%d", labelEnvironment, purpose, time.Now().UTC().Unix()),
		"bucket_access": []map[string]string{{
			"bucket_name": store.bucket,
			"permissions": "read_write",
			"region":      region,
		}},
	})
	if err != nil {
		return "", "", errors.New("replacement key request could not be encoded")
	}
	keysURL := strings.TrimRight(c.linodeAPIRoot, "/") + "/object-storage/keys"
	req, err = http.NewRequestWithContext(context.Background(), http.MethodPost, keysURL, bytes.NewReader(payload))
	if err != nil {
		return "", "", errors.New("replacement key request could not be created")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	body, err = c.request(req)
	if err != nil {
		return "", "", fmt.Errorf("replacement key request failed: %w", err)
	}
	var created struct {
		AccessKey string `json:"access_key"`
		SecretKey string `json:"secret_key"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return "", "", errors.New("replacement key response returned invalid JSON")
	}
	if strings.TrimSpace(created.AccessKey) == "" || strings.TrimSpace(created.SecretKey) == "" {
		return "", "", errors.New("replacement key response omitted credential material")
	}
	return created.AccessKey, created.SecretKey, nil
}

func updateDeploymentCredentialEnvFile(path string, replacements map[string]string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("credential env path is empty")
	}
	for key, value := range replacements {
		if strings.TrimSpace(key) == "" || value == "" || strings.ContainsAny(value, "\r\n") {
			return errors.New("replacement credential is invalid")
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	updated := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		prefix := ""
		if strings.HasPrefix(trimmed, "export ") {
			prefix = "export "
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
		}
		key, _, ok := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		value, wanted := replacements[key]
		if !ok || !wanted {
			continue
		}
		lines[i] = prefix + key + "=" + value
		updated[key] = true
	}
	for key, value := range replacements {
		if !updated[key] {
			lines = append(lines, key+"="+value)
		}
	}
	body := []byte(strings.Join(lines, "\n"))
	temp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".tmp-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, abs); err != nil {
		return err
	}
	committed = true
	return nil
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
