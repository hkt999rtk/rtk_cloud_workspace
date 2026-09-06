package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fake-server qualification must catch content/configuration regressions even
// when every workload and every HTTP endpoint would otherwise appear healthy.
func TestConsoleChecksCatchStagingRegressions(t *testing.T) {
	for _, scenario := range []string{"healthy", "missing-google", "stale-snapshot", "empty-board", "missing-model", "model-html", "empty-sdk", "wrong-ownership", "test-lab-disabled", "no-product", "login-failed", "html-fallback"} {
		t.Run(scenario, func(t *testing.T) {
			manifest := []byte(`{"manifest_version":"1"}`)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					if r.URL.Path != "/api/auth/login" {
						t.Errorf("unexpected write: %s", r.URL.Path)
						w.WriteHeader(500)
						return
					}
					if scenario == "login-failed" {
						http.Error(w, "never print credential", 401)
						return
					}
					http.SetCookie(w, &http.Cookie{Name: "fixture", Value: "secret-cookie", Path: "/"})
					fmt.Fprint(w, `{}`)
					return
				}
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					t.Errorf("unexpected mutation: %s", r.Method)
				}
				switch r.URL.Path {
				case "/api/auth/social/providers":
					if scenario == "missing-google" {
						fmt.Fprint(w, `{"providers":[{"id":"github"}]}`)
					} else {
						fmt.Fprint(w, `{"providers":[{"id":"google"},{"id":"github"}]}`)
					}
				case "/api/admin/chipset-providers":
					if _, err := r.Cookie("fixture"); err != nil {
						t.Error("login cookie missing")
					}
					hash := fmt.Sprintf("%x", sha256.Sum256(manifest))
					if scenario == "stale-snapshot" {
						hash = "old"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"providers": []any{map[string]string{"id": "provider", "status": "published", "manifest_url": server.URL + "/assets/chipset-packages/realtek-amebapro2.json", "manifest_sha256": hash}}})
				case "/assets/chipset-packages/realtek-amebapro2.json":
					_, _ = w.Write(manifest)
				case "/api/admin/chipset-providers/provider":
					if scenario == "empty-board" {
						fmt.Fprint(w, `{"chipsets":[]}`)
					} else {
						fmt.Fprint(w, `{"chipsets":[{"boards":[{"model":{"asset_path":"/assets/boards/fixture.glb"},"resources":[{"type":"video"}]}]}]}`)
					}
				case "/assets/boards/fixture.glb":
					if scenario == "model-html" {
						w.Header().Set("Content-Type", "text/html")
					}
					if scenario == "missing-model" {
						w.WriteHeader(404)
					}
				case "/api/developer/sdk-releases/latest":
					if scenario == "empty-sdk" {
						fmt.Fprint(w, `{"source_status":"available","catalog":{"packages":[]}}`)
					} else if scenario == "html-fallback" {
						w.Header().Set("Content-Type", "text/html")
						fmt.Fprint(w, `<html>login</html>`)
					} else {
						fmt.Fprint(w, `{"source_status":"available","catalog":{"packages":[{}]}}`)
					}
				case "/api/developer/brand-clouds/cloud/test-lab/manage/devices":
					if r.URL.Query().Get("product_id") != "product" {
						t.Error("wrong product scope")
					}
					if scenario == "test-lab-disabled" {
						w.WriteHeader(404)
					} else {
						fmt.Fprint(w, `{"devices":[]}`)
					}
				default:
					if !strings.HasPrefix(r.URL.Path, "/api/developer/brand-clouds/cloud/billing/") {
						t.Errorf("unexpected request %s", r.URL.Path)
						w.WriteHeader(404)
						return
					}
					w.Header().Set("X-Cloud-Ownership-Version", "v1")
					if strings.HasSuffix(r.URL.Path, "/account") {
						id := "cloud"
						if scenario == "wrong-ownership" {
							id = "other"
						}
						fmt.Fprintf(w, `{"account":{"organization_id":%q}}`, id)
					} else {
						fmt.Fprint(w, `{}`)
					}
				}
			}))
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			httpClient := server.Client()
			httpClient.Jar = jar
			product := "product"
			if scenario == "no-product" {
				product = ""
			}
			results := collectConsoleChecks(context.Background(), consoleCheckClient{server.URL, httpClient}, map[string]string{"GOOGLE_LOGIN_ENABLED": "true", "GITHUB_LOGIN_ENABLED": "true", "TEST_LAB_ENABLED": "true"}, "fixture@test.invalid", "never-print-password", "cloud", product)
			failed := false
			for _, r := range results {
				if r.Status != "PASS" {
					failed = true
				}
				if strings.Contains(r.Detail, "never-print") || strings.Contains(r.Detail, "secret-cookie") {
					t.Fatal("credential leaked")
				}
			}
			if failed == (scenario == "healthy") {
				t.Fatalf("unexpected result for %s: %+v", scenario, results)
			}
		})
	}
}

func TestConsoleCheckDoesNotFollowRedirectOrProviderURL(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("credentials sent to unexpected origin") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	client := source.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := consoleCheckClient{source.URL, client}
	if _, _, err := c.request(context.Background(), "GET", "/test", nil); err == nil {
		t.Fatal("redirect passed")
	}
	if _, _, err := c.request(context.Background(), "GET", "//untrusted.invalid", nil); err == nil {
		t.Fatal("absolute host accepted")
	}
}

func TestConsoleCheckRequiresExplicitValidFixture(t *testing.T) {
	for _, args := range [][]string{nil, {"--cloud-id", "../other"}, {"--cloud-id", "00000000-0000-0000-0000-000000000000", "--product-id", "bad"}, {"--unknown"}} {
		if err := runDeploymentWithOperations(append([]string{"console-check"}, args...), deploymentOperations{}); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}
