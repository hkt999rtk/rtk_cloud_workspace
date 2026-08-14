package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeploymentCredentialCheckerValidatesAllRequiredServices(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	server := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{})
	defer server.Close()
	envFile := writeDeploymentCredentialEnv(t, server.URL, "valid-ghcr-token")
	var output bytes.Buffer
	checker := deploymentCredentialChecker{
		client:           server.Client(),
		out:              &output,
		linodeAPIRoot:    server.URL + "/v4",
		ghcrTokenRoot:    server.URL + "/token",
		ghcrRegistryRoot: server.URL,
		goDaddyAPIRoot:   server.URL,
	}
	if err := checker.check(testDeploymentCredentialConfig(), envFile); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[PASS] credential env file",
		"[PASS] Linode API",
		"[PASS] GHCR pull hkt999rtk/rtk_video_cloud/video-cloud-api",
		"[PASS] GHCR pull hkt999rtk/rtk_cloud_logger/rtk-cloud-logger",
		"[PASS] GoDaddy DNS",
		"[PASS] Linode Object Storage",
		"overall: PASS (9 checks)",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestDeploymentCredentialCheckerCreatesMissingObjectStorageBucketAndRevalidates(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	var bucketExists atomic.Bool
	server := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{bucketExists: &bucketExists})
	defer server.Close()
	envFile := writeDeploymentCredentialEnv(t, server.URL, "valid-ghcr-token")
	var output bytes.Buffer
	checker := deploymentCredentialChecker{
		client:           server.Client(),
		out:              &output,
		linodeAPIRoot:    server.URL + "/v4",
		ghcrTokenRoot:    server.URL + "/token",
		ghcrRegistryRoot: server.URL,
		goDaddyAPIRoot:   server.URL,
	}
	if err := checker.checkWithOptions(testDeploymentCredentialConfig(), envFile, deploymentCredentialCheckOptions{createMissingObjectStorageBucket: true}); err != nil {
		t.Fatal(err)
	}
	if !bucketExists.Load() {
		t.Fatal("missing Object Storage bucket was not created")
	}
	if !strings.Contains(output.String(), "configured bucket created with Object Storage access key; signed read access revalidated") ||
		!strings.Contains(output.String(), "overall: PASS (9 checks)") {
		t.Fatalf("unexpected bootstrap output:\n%s", output.String())
	}
}

func TestDeploymentCredentialCheckerDoesNotCreateMissingObjectStorageBucketByDefault(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	var bucketExists atomic.Bool
	server := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{bucketExists: &bucketExists})
	defer server.Close()
	envFile := writeDeploymentCredentialEnv(t, server.URL, "valid-ghcr-token")
	var output bytes.Buffer
	checker := deploymentCredentialChecker{
		client:           server.Client(),
		out:              &output,
		linodeAPIRoot:    server.URL + "/v4",
		ghcrTokenRoot:    server.URL + "/token",
		ghcrRegistryRoot: server.URL,
		goDaddyAPIRoot:   server.URL,
	}
	if err := checker.check(testDeploymentCredentialConfig(), envFile); err == nil {
		t.Fatal("read-only credential check unexpectedly passed")
	}
	if bucketExists.Load() {
		t.Fatal("read-only credential check created the bucket")
	}
	if !strings.Contains(output.String(), "Object Storage GET bucket failed: HTTP 404") {
		t.Fatalf("missing safe 404 result:\n%s", output.String())
	}
}

func TestDeploymentCredentialCheckerCreatesScopedObjectStorageKeyAndUpdatesEnvAfterValidation(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	server := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{rejectOriginalObjectKey: true})
	defer server.Close()
	envFile := writeDeploymentCredentialEnv(t, server.URL, "valid-ghcr-token")
	var output bytes.Buffer
	checker := deploymentCredentialChecker{
		client:           server.Client(),
		out:              &output,
		linodeAPIRoot:    server.URL + "/v4",
		ghcrTokenRoot:    server.URL + "/token",
		ghcrRegistryRoot: server.URL,
		goDaddyAPIRoot:   server.URL,
	}
	options := deploymentCredentialCheckOptions{grantObjectStorageBucketAccess: true, envFile: envFile}
	if err := checker.checkWithOptions(testDeploymentCredentialConfig(), envFile, options); err != nil {
		t.Fatal(err)
	}
	values, err := readEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if values["LINODE_OBJ_ACCESS_KEY_ID"] != "replacement-object-access" || values["LINODE_OBJ_SECRET_ACCESS_KEY"] != "replacement-object-secret" {
		t.Fatalf("operator env did not receive replacement credentials")
	}
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("operator env mode = %v", info.Mode().Perm())
	}
	if !strings.Contains(output.String(), "replacement limited access key created for configured bucket; signed read access revalidated; operator env updated") {
		t.Fatalf("unexpected grant output:\n%s", output.String())
	}
	for _, secret := range []string{"object-secret", "replacement-object-access", "replacement-object-secret"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("credential material leaked in output")
		}
	}
}

func TestDeploymentCredentialCheckerDoesNotUpdateEnvWhenReplacementKeyValidationFails(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	server := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{
		rejectOriginalObjectKey:    true,
		rejectReplacementObjectKey: true,
	})
	defer server.Close()
	envFile := writeDeploymentCredentialEnv(t, server.URL, "valid-ghcr-token")
	checker := deploymentCredentialChecker{
		client:           server.Client(),
		linodeAPIRoot:    server.URL + "/v4",
		ghcrTokenRoot:    server.URL + "/token",
		ghcrRegistryRoot: server.URL,
		goDaddyAPIRoot:   server.URL,
	}
	options := deploymentCredentialCheckOptions{grantObjectStorageBucketAccess: true, envFile: envFile}
	if err := checker.checkWithOptions(testDeploymentCredentialConfig(), envFile, options); err == nil {
		t.Fatal("invalid replacement key unexpectedly passed")
	}
	values, err := readEnvFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if values["LINODE_OBJ_ACCESS_KEY_ID"] != "object-access" || values["LINODE_OBJ_SECRET_ACCESS_KEY"] != "object-secret" {
		t.Fatal("operator env changed before replacement key passed validation")
	}
}

func TestUpdateDeploymentCredentialEnvFileValidatesAndPreservesLayout(t *testing.T) {
	for _, tc := range []struct {
		name         string
		path         string
		replacements map[string]string
	}{
		{name: "empty path", replacements: map[string]string{"KEY": "value"}},
		{name: "empty key", path: "unused", replacements: map[string]string{"": "value"}},
		{name: "empty value", path: "unused", replacements: map[string]string{"KEY": ""}},
		{name: "multiline value", path: "unused", replacements: map[string]string{"KEY": "first\nsecond"}},
		{name: "missing file", path: filepath.Join(t.TempDir(), "missing.env"), replacements: map[string]string{"KEY": "value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := updateDeploymentCredentialEnvFile(tc.path, tc.replacements); err == nil {
				t.Fatal("invalid credential env update unexpectedly passed")
			}
		})
	}

	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("export LINODE_OBJ_ACCESS_KEY_ID=old-access\nUNCHANGED=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateDeploymentCredentialEnvFile(envFile, map[string]string{
		"LINODE_OBJ_ACCESS_KEY_ID":     "new-access",
		"LINODE_OBJ_SECRET_ACCESS_KEY": "new-secret",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	contents := string(data)
	for _, want := range []string{
		"export LINODE_OBJ_ACCESS_KEY_ID=new-access",
		"UNCHANGED=value",
		"LINODE_OBJ_SECRET_ACCESS_KEY=new-secret",
	} {
		if !strings.Contains(contents, want) {
			t.Fatalf("updated env does not contain %q:\n%s", want, contents)
		}
	}
}

func TestDeploymentCredentialCheckerRejectsInvalidGHCRWithoutLeakingSecrets(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	server := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{rejectGHCR: true})
	defer server.Close()
	const secret = "must-never-appear-in-output"
	envFile := writeDeploymentCredentialEnv(t, server.URL, secret)
	var output bytes.Buffer
	checker := deploymentCredentialChecker{
		client:           server.Client(),
		out:              &output,
		linodeAPIRoot:    server.URL + "/v4",
		ghcrTokenRoot:    server.URL + "/token",
		ghcrRegistryRoot: server.URL,
		goDaddyAPIRoot:   server.URL,
	}
	err := checker.check(testDeploymentCredentialConfig(), envFile)
	if err == nil || !strings.Contains(err.Error(), "credential preflight failed") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(output.String(), "[FAIL] GHCR pull") || !strings.Contains(output.String(), "HTTP 403") {
		t.Fatalf("missing safe GHCR failure: %s", output.String())
	}
	for _, forbidden := range []string{secret, "linode-secret", "godaddy-secret", "object-secret"} {
		if strings.Contains(output.String(), forbidden) || strings.Contains(err.Error(), forbidden) {
			t.Fatalf("credential %q leaked in output or error", forbidden)
		}
	}
}

func TestDeploymentCredentialCheckerRejectsInsecureEnvFileBeforeNetwork(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	var requests atomic.Int32
	server := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{requests: &requests})
	defer server.Close()
	envFile := writeDeploymentCredentialEnv(t, server.URL, "valid-ghcr-token")
	if err := os.Chmod(envFile, 0o644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	checker := deploymentCredentialChecker{client: server.Client(), out: &output}
	if err := checker.check(testDeploymentCredentialConfig(), envFile); err == nil {
		t.Fatal("expected insecure credential env file to fail")
	}
	if requests.Load() != 0 {
		t.Fatalf("network requests = %d, want 0", requests.Load())
	}
	if !strings.Contains(output.String(), "chmod 600") {
		t.Fatalf("missing permission remediation: %s", output.String())
	}
}

func TestDeploymentCredentialDefaultsHonorEnvironmentOverrides(t *testing.T) {
	t.Setenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE", "  /tmp/operator.env  ")
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", "https://linode.example.test/v4")
	t.Setenv("RTK_CLOUD_GHCR_TOKEN_ROOT", "https://registry.example.test/token")
	t.Setenv("RTK_CLOUD_GHCR_REGISTRY_ROOT", "https://registry.example.test")
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", "https://dns.example.test/")

	if got := defaultDeploymentCredentialEnvFile(); got != "/tmp/operator.env" {
		t.Fatalf("credential env file = %q", got)
	}
	checker := defaultDeploymentCredentialChecker()
	if checker.client == nil || checker.out == nil ||
		checker.linodeAPIRoot != "https://linode.example.test/v4" ||
		checker.ghcrTokenRoot != "https://registry.example.test/token" ||
		checker.ghcrRegistryRoot != "https://registry.example.test" ||
		checker.goDaddyAPIRoot != "https://dns.example.test" {
		t.Fatalf("checker defaults = %#v", checker)
	}
}

func TestDeploymentCredentialCheckerDefaultsNilDependencies(t *testing.T) {
	checker := deploymentCredentialChecker{}
	if err := checker.check(deploymentConfig{}, ""); err == nil {
		t.Fatal("expected empty credential path to fail")
	}
}

func TestDeploymentCredentialValuesRejectsInvalidPathsAndPrefersProcessEnvironment(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "empty", want: "path is empty"},
		{name: "missing", path: filepath.Join(t.TempDir(), "missing.env"), want: "does not exist"},
		{name: "directory", path: t.TempDir(), want: "not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, check := deploymentCredentialValues(tc.path)
			if check.Passed || !strings.Contains(check.Detail, tc.want) {
				t.Fatalf("check = %#v, want detail containing %q", check, tc.want)
			}
		})
	}

	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("LINODE_TOKEN=file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINODE_TOKEN", "process-token")
	values, check := deploymentCredentialValues(envFile)
	if !check.Passed || values["LINODE_TOKEN"] != "process-token" {
		t.Fatalf("values = %#v, check = %#v", values, check)
	}
}

func TestDeploymentCredentialChecksReportMissingInputs(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	checker := deploymentCredentialChecker{}
	if check := checker.checkLinode(nil); check.Passed || check.Detail != "LINODE_TOKEN is missing" {
		t.Fatalf("Linode check = %#v", check)
	}
	if checks := checker.checkGHCR(nil); len(checks) != 1 || checks[0].Passed ||
		checks[0].Detail != "GHCR_PULL_USERNAME and GHCR_PULL_TOKEN are missing" {
		t.Fatalf("GHCR checks = %#v", checks)
	}
	if checks := checker.checkGHCR(map[string]string{"GHCR_PULL_USERNAME": "operator"}); len(checks) != 1 || checks[0].Detail != "GHCR_PULL_TOKEN is missing" {
		t.Fatalf("GHCR token check = %#v", checks)
	}
	if check := checker.checkGoDaddy(deploymentConfig{}, nil); check.Passed ||
		check.Detail != "GODADDY_KEY and GODADDY_SECRET are required" {
		t.Fatalf("GoDaddy check = %#v", check)
	}
	if check := checker.checkObjectStorage(nil); check.Passed || check.Detail != "LINODE_OBJ_BUCKET is required" {
		t.Fatalf("Object Storage check = %#v", check)
	}
}

func TestExchangeGHCRTokenAcceptsAccessTokenAndRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{name: "access token", body: `{"access_token":"registry-token"}`, want: "registry-token"},
		{name: "invalid JSON", body: `{`, wantErr: "invalid JSON"},
		{name: "missing token", body: `{}`, wantErr: "no registry token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("scope"); got != "repository:hkt999rtk/example:pull" {
					t.Errorf("scope = %q", got)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			checker := deploymentCredentialChecker{client: server.Client(), ghcrTokenRoot: server.URL}
			got, err := checker.exchangeGHCRToken("operator", "secret", "hkt999rtk/example")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("token = %q, error = %v", got, err)
			}
		})
	}
}

func testDeploymentCredentialConfig() deploymentConfig {
	return deploymentConfig{
		Adapter:    "lke",
		DNSAdapter: "godaddy",
		Values: map[string]string{
			"CLOUD_DNS_ROOT_DOMAIN":                  "example.test",
			"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "true",
		},
		DNSValues: map[string]string{"GODADDY_ENV": "prod"},
	}
}

func clearDeploymentCredentialEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range deploymentCredentialKeys() {
		t.Setenv(key, "")
	}
}

func writeDeploymentCredentialEnv(t *testing.T, endpoint, ghcrToken string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	body := fmt.Sprintf(`LINODE_TOKEN=linode-secret
GHCR_PULL_USERNAME=test-user
GHCR_PULL_TOKEN=%s
GODADDY_KEY=godaddy-key
GODADDY_SECRET=godaddy-secret
LINODE_OBJ_ACCESS_KEY_ID=object-access
LINODE_OBJ_SECRET_ACCESS_KEY=object-secret
LINODE_OBJ_ENDPOINT=%s
LINODE_OBJ_BUCKET=test-bucket
LINODE_OBJ_REGION=us-test-1
`, ghcrToken, endpoint)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type deploymentCredentialTestServerOptions struct {
	rejectGHCR                 bool
	requests                   *atomic.Int32
	bucketExists               *atomic.Bool
	rejectOriginalObjectKey    bool
	rejectReplacementObjectKey bool
}

func newDeploymentCredentialTestServer(t *testing.T, options deploymentCredentialTestServerOptions) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if options.requests != nil {
			options.requests.Add(1)
		}
		switch {
		case r.URL.Path == "/v4/profile":
			if r.Header.Get("Authorization") != "Bearer linode-secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"username":"operator"}`))
		case r.URL.Path == "/v4/lke/clusters":
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/token":
			username, token, ok := r.BasicAuth()
			if options.rejectGHCR || !ok || username != "test-user" || token != "valid-ghcr-token" {
				http.Error(w, "denied", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"token":"registry-token"}`))
		case strings.HasPrefix(r.URL.Path, "/v2/") && strings.HasSuffix(r.URL.Path, "/tags/list"):
			if r.Header.Get("Authorization") != "Bearer registry-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"name":"test","tags":["sha-test"]}`))
		case r.URL.Path == "/v1/domains/example.test/records":
			if r.Header.Get("Authorization") != "sso-key godaddy-key:godaddy-secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/v4/object-storage/buckets":
			if r.Header.Get("Authorization") != "Bearer linode-secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"label":"test-bucket","region":"us-test-1","cluster":"us-test-1"}]}`))
		case r.URL.Path == "/v4/object-storage/keys" && r.Method == http.MethodPost:
			if r.Header.Get("Authorization") != "Bearer linode-secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var payload struct {
				BucketAccess []struct {
					BucketName  string `json:"bucket_name"`
					Permissions string `json:"permissions"`
					Region      string `json:"region"`
				} `json:"bucket_access"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.BucketAccess) != 1 ||
				payload.BucketAccess[0].BucketName != "test-bucket" || payload.BucketAccess[0].Permissions != "read_write" ||
				payload.BucketAccess[0].Region != "us-test-1" {
				http.Error(w, "invalid grant", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"access_key":"replacement-object-access","secret_key":"replacement-object-secret"}`))
		case r.URL.Path == "/test-bucket":
			authorization := r.Header.Get("Authorization")
			originalKey := strings.Contains(authorization, "Credential=object-access/")
			replacementKey := strings.Contains(authorization, "Credential=replacement-object-access/")
			if !originalKey && !replacementKey {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if (options.rejectOriginalObjectKey && originalKey) || (options.rejectReplacementObjectKey && replacementKey) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if options.bucketExists != nil {
				if r.Method == http.MethodPut {
					options.bucketExists.Store(true)
					w.WriteHeader(http.StatusOK)
					return
				}
				if !options.bucketExists.Load() {
					http.NotFound(w, r)
					return
				}
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
		default:
			http.NotFound(w, r)
		}
	}))
}
