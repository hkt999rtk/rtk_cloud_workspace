package main

import (
	"bytes"
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
	server := newDeploymentCredentialTestServer(t, false, nil)
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

func TestDeploymentCredentialCheckerRejectsInvalidGHCRWithoutLeakingSecrets(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	server := newDeploymentCredentialTestServer(t, true, nil)
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
	server := newDeploymentCredentialTestServer(t, false, &requests)
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

func newDeploymentCredentialTestServer(t *testing.T, rejectGHCR bool, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			requests.Add(1)
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
			if rejectGHCR || !ok || username != "test-user" || token != "valid-ghcr-token" {
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
		case r.URL.Path == "/test-bucket":
			if !strings.Contains(r.Header.Get("Authorization"), "Credential=object-access/") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
		default:
			http.NotFound(w, r)
		}
	}))
}
