package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type consoleRoundTripFunc func(*http.Request) (*http.Response, error)

func (f consoleRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConsoleCheckCLIUsesOnlySelectedStoreAndNeverMaterializesRuntime(t *testing.T) {
	const cloud = "00000000-0000-0000-0000-000000000001"
	for _, scenario := range []string{"pass", "email-override", "missing-password", "bad-email-mode", "failed-feature", "redirect", "bad-config", "bad-origin", "extra-argument"} {
		t.Run(scenario, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "staging", "lke")
			store := makeIsolatedTestSecretStore(t, "staging")
			if scenario != "missing-password" {
				if err := store.write("runtime/platform-admin", []byte("private-fixture-password"), false); err != nil {
					t.Fatal(err)
				}
			}
			wantEmail := "platform-admin@video-cloud-staging.local"
			if scenario == "email-override" || scenario == "bad-email-mode" {
				wantEmail = "selected-operator@example.test"
				if err := store.write("operator/env/ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", []byte(wantEmail), false); err != nil {
					t.Fatal(err)
				}
				if scenario == "bad-email-mode" {
					if err := os.Chmod(filepath.Join(store.Root, "operator/env/ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "bad-config" {
				writeTestFile(t, filepath.Join(workspace, "cloud_env/staging/environment.env"), "UNSUPPORTED_KEY=x\n")
			}
			if scenario == "bad-origin" {
				writeTestFile(t, filepath.Join(workspace, "cloud_env/staging/environment.env"), "CLOUD_STACK_NAME=video-cloud-staging\nCLOUD_DNS_ROOT_DOMAIN=example.test/invalid\nDEPLOYMENT_LOCATION=us-west\n")
			}
			oldTransport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = oldTransport })
			requests := 0
			http.DefaultTransport = consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Scheme != "https" || r.URL.Host != "admin.video-cloud-staging.example.test" {
					t.Fatal("unexpected credential destination")
				}
				header := http.Header{"Content-Type": {"application/json"}}
				status, body := 200, `{}`
				if scenario == "redirect" {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://wrong.example.test"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				switch r.URL.Path {
				case "/api/auth/login":
					var credentials map[string]string
					if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
						t.Fatal(err)
					}
					if credentials["password"] != "private-fixture-password" || credentials["email"] != wantEmail {
						t.Fatal("did not use selected canonical credentials")
					}
					header.Set("Set-Cookie", "private=fixture; Secure; Path=/")
				case "/api/auth/social/providers":
					body = `{"providers":[]}`
				case "/api/admin/chipset-providers":
					if _, err := r.Cookie("private"); err != nil {
						t.Fatal("missing private session")
					}
					body = fmt.Sprintf(`{"providers":[{"id":"fixture","status":"published","manifest_url":"https://admin.video-cloud-staging.example.test/assets/chipset-packages/realtek-amebapro2.json","manifest_sha256":"%x"}]}`, sha256.Sum256([]byte(`{}`)))
				case "/assets/chipset-packages/realtek-amebapro2.json":
				case "/api/admin/chipset-providers/fixture":
					body = `{"chipsets":[{"boards":[{"model":{"asset_path":"/assets/boards/fixture.glb"},"resources":[{"type":"video"}]}]}]}`
				case "/assets/boards/fixture.glb":
					if r.Method != http.MethodHead {
						t.Fatal("must not download Board model")
					}
				case "/api/developer/sdk-releases/latest":
					body = `{"source_status":"available","catalog":{"packages":[{}]}}`
					if scenario == "failed-feature" {
						status = 503
					}
				default:
					if !strings.HasPrefix(r.URL.Path, "/api/developer/brand-clouds/"+cloud+"/billing/") {
						t.Fatal("unexpected route")
					}
					header.Set("X-Cloud-Ownership-Version", "v1")
					if strings.HasSuffix(r.URL.Path, "/account") {
						body = fmt.Sprintf(`{"account":{"organization_id":%q}}`, cloud)
					}
				}
				if r.Method != http.MethodGet && r.Method != http.MethodHead && !(r.Method == http.MethodPost && r.URL.Path == "/api/auth/login") {
					t.Fatal("unexpected write")
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			args := []string{"console-check", "--workspace", workspace, "--environment", "staging", "--cloud-id", cloud}
			if scenario == "extra-argument" {
				args = append(args, "unrecognized")
			}
			var runErr error
			out := captureStdout(t, func() { runErr = runDeploymentWithOperations(args, deploymentOperations{}) })
			pass := scenario == "pass" || scenario == "email-override"
			if (runErr == nil) != pass {
				t.Fatalf("unexpected result: %v", runErr)
			}
			if strings.Contains(out, "private-fixture-password") || strings.Contains(out, wantEmail) {
				t.Fatal("credentials in report")
			}
			if pass && (!strings.Contains(out, `"status":"PASS"`) || !strings.Contains(out, `"unverified"`)) {
				t.Fatal("missing structured evidence")
			}
			if _, err := os.Stat(filepath.Join(workspace, "cloud_env/staging/runtime")); !os.IsNotExist(err) {
				t.Fatal("read check materialized runtime")
			}
			if scenario == "missing-password" && requests != 0 {
				t.Fatal("missing canonical secret must block all requests")
			}
		})
	}
}

type consoleBrokenBody struct{}

func (consoleBrokenBody) Read([]byte) (int, error) { return 0, errors.New("private-body-detail") }
func (consoleBrokenBody) Close() error             { return nil }

func TestConsoleCheckHTTPFailuresAreBoundedAndRedacted(t *testing.T) {
	for _, mode := range []string{"transport", "broken-body", "large-body", "bad-json", "non-json"} {
		t.Run(mode, func(t *testing.T) {
			client := consoleCheckClient{"https://fixture.test", &http.Client{Transport: consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if mode == "transport" {
					return nil, errors.New("private-transport-detail")
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader("{"))
				ct := "application/json"
				switch mode {
				case "broken-body":
					body = consoleBrokenBody{}
				case "large-body":
					body = io.NopCloser(strings.NewReader(strings.Repeat("x", 4*1024*1024+1)))
				case "non-json":
					ct = "text/html"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: body, Request: r}, nil
			})}}
			var out any
			_, err := client.json(context.Background(), "/probe", &out)
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatalf("expected redacted failure: %v", err)
			}
		})
	}
	c := consoleCheckClient{origin: "https://fixture.test"}
	if _, _, err := c.request(context.Background(), "POST", "/probe", make(chan int)); err == nil {
		t.Fatal("invalid payload accepted")
	}
	if _, _, err := c.request(context.Background(), "bad\nmethod", "/probe", nil); err == nil {
		t.Fatal("invalid method accepted")
	}
}
