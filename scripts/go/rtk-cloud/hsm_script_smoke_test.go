package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateUsersUsesAccountManagerBaseURLOverrideAndWritesArtifact(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	envRoot := filepath.Join(root, "env")
	mkdirAll(t, filepath.Join(envRoot, "services", "account-manager"))
	writeFile(t, filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env"), "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=admin@example.test\nACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=password123\n")

	var sawLogin bool
	var sawBrandCloudList bool
	var sawCreateUser bool
	var sawCSRBootstrap bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode login payload: %v", err)
			}
			if payload["email"] == "admin@example.test" {
				sawLogin = true
				_ = json.NewEncoder(w).Encode(map[string]any{
					"tokens": map[string]string{"access_token": "platform-token"},
				})
				return
			}
			t.Fatalf("brand-cloud user login used platform auth endpoint")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/brand-clouds/rtk-test/auth/login":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode brand-cloud login payload: %v", err)
			}
			if payload["app_csr_pem"] == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"user":            map[string]string{"id": "user-1", "email": payload["email"]},
					"tokens":          map[string]string{"access_token": "user-token"},
					"app_certificate": map[string]string{"status": "csr_required"},
				})
				return
			}
			sawCSRBootstrap = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":   map[string]string{"id": "user-1", "email": payload["email"]},
				"tokens": map[string]string{"access_token": "user-token"},
				"app_certificate": map[string]string{
					"status":                "issued",
					"subject":               "app-user:user-1",
					"certificate_pem":       "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
					"certificate_chain_pem": "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n",
					"fingerprint_sha256":    "abc123",
					"serial_number":         "01",
					"issuer_request_id":     "issuer-1",
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/brand-clouds":
			sawBrandCloudList = true
			if got := r.Header.Get("Authorization"); got != "Bearer platform-token" {
				t.Fatalf("Authorization = %q, want bearer token", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"brand_clouds": []map[string]any{{
					"id":          "brand-1",
					"name":        "RTK",
					"tenant_slug": "rtk-test",
					"metadata":    map[string]any{"brandname": "RTK"},
				}},
				"pagination": map[string]any{"total": 1},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/brand-clouds/brand-1/users":
			sawCreateUser = true
			if got := r.Header.Get("Authorization"); got != "Bearer platform-token" {
				t.Fatalf("Authorization = %q, want bearer token", got)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"action": "created"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("RTK_CLOUD_CURL_CONNECT_TIMEOUT", "2")
	t.Setenv("RTK_CLOUD_CURL_MAX_TIME", "5")
	if err := runCreateUsers([]string{"--workspace", workspace, "--env-root", envRoot, "--brandname", "RTK", "--count", "1", "--skip-bootstrap"}); err != nil {
		t.Fatalf("runCreateUsers() error = %v", err)
	}
	if !sawLogin || !sawBrandCloudList || !sawCreateUser || !sawCSRBootstrap {
		t.Fatalf("expected full create user flow, login=%v list=%v create=%v csr=%v", sawLogin, sawBrandCloudList, sawCreateUser, sawCSRBootstrap)
	}
	matches, err := filepath.Glob(filepath.Join(envRoot, "artifacts", "users", "rtk-users-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one users artifact, matches=%v err=%v", matches, err)
	}
}

func TestGenerateLoadDevicesGenerateOnlyWritesArtifacts(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	outDir := filepath.Join(root, "devices")
	if err := runGenerateLoadDevices([]string{
		"--workspace", workspace,
		"--env-root", root,
		"--count", "2",
		"--mix", "camera=1,light=1",
		"--prefix", "hsm-script",
		"--generate-only",
		"--out-dir", outDir,
		"--force",
	}); err != nil {
		t.Fatalf("runGenerateLoadDevices() error = %v", err)
	}

	for _, rel := range []string{
		"summary.json",
		"manifests/devices.json",
		"manifests/devices.csv",
		"devices/camera/hsm-script-0001/device.cert.pem",
		"devices/light/hsm-script-0002/device.cert.pem",
	} {
		if _, err := os.Stat(filepath.Join(outDir, rel)); err != nil {
			t.Fatalf("expected generated artifact %s: %v", rel, err)
		}
	}
}
