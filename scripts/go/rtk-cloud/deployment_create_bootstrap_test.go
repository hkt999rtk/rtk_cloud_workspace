package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreatePKICeremonyUsesAuthenticatedOperatorAndExactRegistryDigests(t *testing.T) {
	const user = "11111111-1111-4111-8111-111111111111"
	const operation = "22222222-2222-4222-8222-222222222222"
	const issuer = "33333333-3333-4333-8333-333333333333"
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			var calls []string
			request := environmentCreateIssuerRequest{Environment: environment, Domain: "service", Kind: "root"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				if r.URL.Path == "/v1/auth/login" {
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["email"] != "operator@example.test" || body["password"] != "private-password" || r.Header.Get("Authorization") != "" {
						t.Error("login did not use genuine password inputs")
					}
					fmt.Fprintf(w, `{"user":{"id":%q,"email":"operator@example.test"},"tokens":{"access_token":"private-real-user-session"}}`, user)
					return
				}
				if r.Header.Get("Authorization") != "Bearer private-real-user-session" {
					t.Error("issuer request bypassed the authenticated user session")
				}
				switch r.URL.Path {
				case "/v1/platform/pki/operations":
					var actual environmentCreateIssuerRequest
					if json.NewDecoder(r.Body).Decode(&actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(request) || r.Header.Get("Idempotency-Key") != "reviewed-parent-service-root" {
						t.Error("issuer request drifted")
					}
					fmt.Fprintf(w, `{"operation_id":%q,"issuer_id":%q,"request_sha256":%q,"status":"requested"}`, operation, issuer, deploymentplan.Digest(request))
				case "/v1/platform/pki/issuers/" + issuer:
					json.NewEncoder(w).Encode(map[string]string{"issuer_id": issuer, "environment": environment, "status": "approved"})
				case "/v1/platform/pki/operations/" + operation + "/authorize":
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["request_sha256"] != deploymentplan.Digest(request) {
						t.Error("authorization did not bind the exact policy")
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), environment, user)
			if err != nil {
				t.Fatal(err)
			}
			if actual, err := client.Authenticate(context.Background(), "operator@example.test", "private-password"); err != nil || actual != user {
				t.Fatal(actual, err)
			}
			op, err := client.ReserveIssuer(context.Background(), "reviewed-parent-service-root", request)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.AuthorizeIssuer(context.Background(), op); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 4 {
				t.Fatal("unexpected mutation or reauthentication", calls)
			}
		})
	}
}

func TestCreatePKICeremonyRejectsWrongUsersDigestsAndRedirectsWithoutRetry(t *testing.T) {
	const user = "11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"wrong-user", "disabled-user", "wrong-digest", "redirect", "denied", "invalid-public-response"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "redirect" {
					w.Header().Set("Location", "/private-token-redirect")
					w.WriteHeader(307)
					return
				}
				if mode == "denied" {
					w.WriteHeader(403)
					fmt.Fprint(w, "private-provider-diagnostic")
					return
				}
				if r.URL.Path == "/v1/auth/login" {
					id := user
					disabled := ""
					if mode == "wrong-user" {
						id = "44444444-4444-4444-8444-444444444444"
					}
					if mode == "disabled-user" {
						disabled = `,"disabled_at":"2026-10-01T00:00:00Z"`
					}
					fmt.Fprintf(w, `{"user":{"id":%q,"email":"operator@example.test"%s},"tokens":{"access_token":"private-token"}}`, id, disabled)
					return
				}
				if mode == "invalid-public-response" {
					fmt.Fprint(w, "private-invalid-public-output")
					return
				}
				fmt.Fprint(w, `{"operation_id":"22222222-2222-4222-8222-222222222222","issuer_id":"33333333-3333-4333-8333-333333333333","request_sha256":"`+strings.Repeat("f", 64)+`","status":"requested"}`)
			}))
			defer server.Close()
			client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", user)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Authenticate(context.Background(), "operator@example.test", "private-password")
			if err == nil {
				_, err = client.ReserveIssuer(context.Background(), "parent-root", environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "root"})
			}
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("invalid ceremony succeeded or leaked private diagnostics", err)
			}
			want := 1
			if mode == "wrong-digest" || mode == "invalid-public-response" {
				want = 2
			}
			if calls != want {
				t.Fatal("mutation retried or redirected", calls, want)
			}
		})
	}
}

func TestCreatePKICeremonyRefusesForeignOrInsecureOrigins(t *testing.T) {
	for _, origin := range []string{"http://account-manager.dev.example", "https://user:pass@example.test", "https://example.test/path", "https://example.test?token=private", "https://example.test#fragment"} {
		if _, err := newEnvironmentCreatePKIClient(origin, nil, "dev", ""); err == nil {
			t.Fatal("invalid initial ceremony origin accepted", origin)
		}
	}
	if _, err := newEnvironmentCreatePKIClient("https://account-manager.example.test", nil, "prod", ""); err == nil {
		t.Fatal("production inherited non-production initial PKI qualification")
	}
}

func TestCreatePKICeremonyCannotReserveFutureOrForeignIssuerBeforeAuthentication(t *testing.T) {
	client, err := newEnvironmentCreatePKIClient("https://account-manager.example.test", nil, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []environmentCreateIssuerRequest{{Environment: "dev", Domain: "service", Kind: "root"}, {Environment: "staging", Domain: "service", Kind: "root"}, {Environment: "dev", Domain: "service", Kind: "intermediate", ParentID: "future-root"}} {
		if _, err := client.ReserveIssuer(context.Background(), "parent", request); err == nil {
			t.Fatal("issuer reservation bypassed genuine authenticated authority")
		}
	}
}

func TestCreateAccountManagerSigningReferencesRemainLifecycleBound(t *testing.T) {
	for _, name := range []string{"JWT_SIGNER_PROVIDER", "JWT_ACCESS_PRIVATE_KEY_PATH", "JWT_ACCESS_PUBLIC_KEY_PATH", "JWT_REFRESH_PRIVATE_KEY_PATH", "JWT_REFRESH_PUBLIC_KEY_PATH"} {
		if !environmentIdentitySetting(name) {
			t.Fatal("original Account Manager signing identity reference would be dropped", name)
		}
	}
	for _, name := range []string{"JWT_ACCESS_TTL", "JWT_REFRESH_SECRET", "JWT_ACCESS_PRIVATE_KEY"} {
		if environmentIdentitySetting(name) {
			t.Fatal("process policy or inline private key was treated as an identity reference", name)
		}
	}
}

func TestCreateRootCeremonyUsesNativeEncryptedKeysAndKeepsOriginalCustody(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("native encrypted-custody fixture needs OpenSSL")
	}
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("operator/env/PKI_OPERATOR_SIGNER_REF", []byte("offline:dev/service-root"), false); err != nil {
		t.Fatal(err)
	}
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "pkiceremony")
	build := exec.Command("go", "build", "-o", binary, "./cmd/pkiceremony")
	build.Dir = filepath.Join(workspace, "repos", "rtk_video_cloud")
	build.Env = append(os.Environ(), "GOWORK=off", "GOCACHE="+filepath.Join(t.TempDir(), "go-cache"), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native ceremony fixture build failed: %v %s", err, output)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nset -eu\nwhile [ $# -gt 0 ]; do case \"$1\" in generate|sign|crl) break;; esac; shift; done\npython3 - \"$@\" <<'PY'\nimport os, subprocess, sys\nargs=[(" + fmt.Sprintf("%q", store.Root) + "+a[len('/store'):]) if a.startswith('/store/') else a for a in sys.argv[1:]]\nsys.exit(subprocess.call([" + fmt.Sprintf("%q", binary) + "]+args))\nPY\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	const user = "11111111-1111-4111-8111-111111111111"
	const opID = "22222222-2222-4222-8222-222222222222"
	const issuerID = "33333333-3333-4333-8333-333333333333"
	issuer := map[string]any{"issuer_id": issuerID, "issuer_version": 1, "environment": "dev", "trust_domain": "service", "kind": "root", "status": "approved", "signer_provider": "offline", "created_by": user}
	request := environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "root"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer real-authenticated-fixture" {
			t.Error("registry mutation bypassed authentic user session")
		}
		switch r.URL.Path {
		case "/v1/platform/pki/operations":
			fmt.Fprintf(w, `{"operation_id":%q,"issuer_id":%q,"request_sha256":%q,"status":"requested"}`, opID, issuerID, deploymentplan.Digest(request))
		case "/v1/platform/pki/operations/" + opID + "/authorize":
			w.WriteHeader(204)
		case "/v1/platform/pki/issuers/" + issuerID:
			json.NewEncoder(w).Encode(issuer)
		case "/v1/platform/pki/operations/" + opID + "/provision":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid provision request")
			}
			issuer["csr_pem"], issuer["status"] = body["csr_pem"], "provisioning"
			json.NewEncoder(w).Encode(issuer)
		case "/v1/platform/pki/operations/" + opID + "/import":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid signed import")
			}
			block, _ := pem.Decode([]byte(body["certificate_pem"]))
			if block == nil {
				t.Fatal("invalid certificate")
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil || cert.CheckSignatureFrom(cert) != nil {
				t.Fatal("native Root certificate was not signed")
			}
			sum := sha256.Sum256(cert.Raw)
			issuer["certificate_pem"], issuer["status"], issuer["certificate_fingerprint_sha256"] = body["certificate_pem"], "ready", hex.EncodeToString(sum[:])
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected initial ceremony route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), "dev", user)
	if err != nil {
		t.Fatal(err)
	}
	client.token = "real-authenticated-fixture"
	p := environmentCreateBootstrapPlan{Environment: "dev", Stack: "video-cloud-dev", Image: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64)}
	_, ready, err := executeEnvironmentCreateRootCeremony(context.Background(), client, deploymentConfig{Environment: "dev"}, p, store, strings.Repeat("b", 64), "service")
	if err != nil || ready["status"] != "ready" {
		t.Fatal("native initial Root ceremony failed", err)
	}
	keyPath := filepath.Join(store.Root, "pki", "custody", "roots", issuerID, "ca-key.encrypted.pem")
	key, err := os.ReadFile(keyPath)
	if err != nil || !strings.Contains(string(key), "BEGIN ENCRYPTED PRIVATE KEY") {
		t.Fatal("unencrypted or missing custody key", err)
	}
	manifestPath := filepath.Join(filepath.Dir(keyPath), "manifest.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil || strings.Contains(string(manifest), "BEGIN") || !strings.Contains(string(manifest), "rtk-root-custody/v1") {
		t.Fatal("invalid secret-free custody manifest", err)
	}
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("custody key permissions", err)
	}
	if _, _, err := executeEnvironmentCreateRootCeremony(context.Background(), client, deploymentConfig{Environment: "dev"}, p, store, strings.Repeat("b", 64), "service"); err == nil {
		t.Fatal("an earlier native Root ceremony was overwritten")
	}
	after, err := os.ReadFile(keyPath)
	if err != nil || string(after) != string(key) {
		t.Fatal("earlier encrypted key changed during retry", err)
	}
}
