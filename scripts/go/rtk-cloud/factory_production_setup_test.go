package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareFactoryProductionCredentialUsesAccountManagerIssuance(t *testing.T) {
	const productionJWT = "secret.header.payload"
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "admin-token"}})
		case "GET /v1/admin/brand-clouds":
			_ = json.NewEncoder(w).Encode(map[string]any{"brand_clouds": []map[string]any{{"id": "brand-001", "name": "RTK"}}})
		case "GET /v1/admin/brand-clouds/brand-001/device-item-profiles":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profiles": []any{}, "pagination": map[string]int{"total": 0}})
		case "POST /v1/admin/brand-clouds/brand-001/device-item-profiles":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profile": map[string]string{"id": "profile-001"}})
		case "POST /v1/admin/brand-clouds/brand-001/device-item-profiles/profile-001/production-runs":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["allowed_quantity"] != float64(12) || payload["batch_id"] != "runtime-123-1" {
				t.Fatalf("production payload = %#v", payload)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"production_run": map[string]string{"id": "production-001"}, "factory_jwt": productionJWT})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "admin@example.test")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "test-password")
	t.Setenv("CLOUD_PROVIDER", "local")
	credential, err := prepareFactoryProductionCredential(root, root, "RTK", "runtime-123-1", 12, time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if credential.JWT != productionJWT || credential.BrandCloudID != "brand-001" || credential.DeviceItemProfileID != "profile-001" || credential.ProductionRunID != "production-001" {
		t.Fatalf("credential = %#v", credential)
	}
	joined := strings.Join(requests, "\n")
	for _, required := range []string{"POST /v1/auth/login", "POST /v1/admin/brand-clouds/brand-001/device-item-profiles", "POST /v1/admin/brand-clouds/brand-001/device-item-profiles/profile-001/production-runs"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("requests missing %s:\n%s", required, joined)
		}
	}
}

func TestFactoryProductionEvidenceRedactsJWT(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory-production.json")
	credential := factoryProductionCredential{JWT: "must-never-be-written", BrandCloudID: "brand", DeviceItemProfileID: "profile", ProductionRunID: "run"}
	if err := writeFactoryProductionSetupEvidence(path, "runtime-1", "RTK", credential); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, credential.JWT) || strings.Contains(strings.ToLower(text), "factory_jwt\"") {
		t.Fatalf("evidence leaked production JWT: %s", text)
	}
	if !strings.Contains(text, `"production_jwt_issued": true`) {
		t.Fatalf("evidence missing redacted issuance marker: %s", text)
	}
}

func TestPrepareFactoryProductionStepPassesCredentialOnlyToChildEnv(t *testing.T) {
	root := t.TempDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const secretJWT = "ephemeral-production-jwt"
	env, step, err := prepareFactoryProductionStep("workspace", "env", root, logsDir, "RTK", "runtime-1", 12, time.Now().UTC(), func(_, _, brand, run string, quantity int, _ time.Time) (factoryProductionCredential, error) {
		if brand != "RTK" || run != "runtime-1" || quantity != 12 {
			t.Fatalf("prepare args brand=%s run=%s quantity=%d", brand, run, quantity)
		}
		return factoryProductionCredential{JWT: secretJWT, BrandCloudID: "brand", DeviceItemProfileID: "profile", ProductionRunID: "production"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != "PASS" || step.ExitCode != 0 || envListValue(env, "FACTORY_ENROLL_PRODUCTION_JWT") != secretJWT || envListValue(env, "FACTORY_ENROLL_RUN_ID") != "runtime-1" {
		t.Fatalf("env=%v step=%+v", env, step)
	}
	for _, path := range []string{step.LogFile, filepath.Join(root, "factory-production.json")} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(raw), secretJWT) {
			t.Fatalf("%s leaked production JWT", path)
		}
	}
}

func TestPrepareFactoryProductionStepStopsOnIssuanceFailure(t *testing.T) {
	env, step, err := prepareFactoryProductionStep("workspace", "env", t.TempDir(), t.TempDir(), "RTK", "runtime-1", 12, time.Now().UTC(), func(string, string, string, string, int, time.Time) (factoryProductionCredential, error) {
		return factoryProductionCredential{}, http.ErrServerClosed
	})
	if err == nil || len(env) != 0 || step.Status != "FAIL" || step.ExitCode != 1 {
		t.Fatalf("env=%v step=%+v err=%v", env, step, err)
	}
}

func TestUseProvidedFactoryProductionCredentialDoesNotReissueOrLeak(t *testing.T) {
	logsDir := t.TempDir()
	const jwt = "caller-issued-secret-jwt"
	t.Setenv("FACTORY_ENROLL_BATCH_ID", "caller-batch")
	env, step, err := useProvidedFactoryProductionCredential(logsDir, "runtime-1", jwt)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != "PASS" || envListValue(env, "FACTORY_ENROLL_PRODUCTION_JWT") != jwt || envListValue(env, "FACTORY_ENROLL_BATCH_ID") != "caller-batch" {
		t.Fatalf("env=%v step=%+v", env, step)
	}
	raw, err := os.ReadFile(step.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), jwt) || !strings.Contains(string(raw), "production_jwt_source=caller_issued") {
		t.Fatalf("caller-issued credential log is unsafe or incomplete: %s", raw)
	}
	if env, step, err := useProvidedFactoryProductionCredential(logsDir, "", jwt); err == nil || len(env) != 0 || step.Status != "FAIL" {
		t.Fatalf("invalid credential env=%v step=%+v err=%v", env, step, err)
	}
	if env, _, err := useProvidedFactoryProductionCredential(filepath.Join(t.TempDir(), "missing", "logs"), "runtime-1", jwt); err == nil || len(env) != 0 {
		t.Fatalf("unwritable log path env=%v err=%v", env, err)
	}
}

func TestPrepareFactoryProductionStepRejectsUnwritableLogPath(t *testing.T) {
	root := t.TempDir()
	logsPath := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(logsPath, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, _, err := prepareFactoryProductionStep("workspace", "env", root, logsPath, "RTK", "runtime-1", 1, time.Now().UTC(), func(string, string, string, string, int, time.Time) (factoryProductionCredential, error) {
		return factoryProductionCredential{JWT: "secret", BrandCloudID: "brand", DeviceItemProfileID: "profile", ProductionRunID: "production"}, nil
	})
	if err == nil || len(env) != 0 {
		t.Fatalf("unwritable log path env=%v err=%v", env, err)
	}
}

func TestEnsureFactoryProductionProfileReusesActiveRunProfile(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profiles": []map[string]string{{"id": "profile-existing", "profile_key": "e2e-key", "status": "active"}}})
	}))
	defer server.Close()
	id, err := ensureFactoryProductionProfile(accountManagerContext{BaseURL: server.URL}, "token", "brand", "e2e-key", "run")
	if err != nil || id != "profile-existing" {
		t.Fatalf("id=%s err=%v", id, err)
	}
	if len(methods) != 1 || methods[0] != http.MethodGet {
		t.Fatalf("methods=%v", methods)
	}
}

func TestFactoryProductionAPIErrorsDoNotReturnCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profiles": []map[string]string{{"id": "disabled", "profile_key": "disabled-key", "status": "disabled"}}})
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
		}
	}))
	defer server.Close()
	ctx := accountManagerContext{BaseURL: server.URL}
	if _, err := ensureFactoryProductionProfile(ctx, "token", "brand", "disabled-key", "run"); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("disabled profile error=%v", err)
	}
	if _, err := ensureFactoryProductionProfile(ctx, "token", "brand", "new-key", "run"); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("create profile error=%v", err)
	}
	if run, jwt, err := createFactoryProductionRun(ctx, "token", "brand", "profile", "run", 1, time.Now()); err == nil || run != "" || jwt != "" || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("run=%s jwt=%s err=%v", run, jwt, err)
	}
}

func TestFactoryProductionRejectsInvalidAndIncompleteResponses(t *testing.T) {
	if _, err := prepareFactoryProductionCredential("", "", "RTK", "", 1, time.Now()); err == nil {
		t.Fatal("missing run ID was accepted")
	}
	if _, err := prepareFactoryProductionCredential("", "", "RTK", "run", 0, time.Now()); err == nil {
		t.Fatal("non-positive quantity was accepted")
	}

	tests := []struct {
		name    string
		handler http.HandlerFunc
		invoke  func(accountManagerContext) error
		want    string
	}{
		{
			name: "profile list status", want: "list factory production profiles failed",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
			invoke: func(ctx accountManagerContext) error {
				_, err := ensureFactoryProductionProfile(ctx, "token", "brand", "key", "run")
				return err
			},
		},
		{
			name: "profile list malformed", want: "invalid character",
			handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not-json")) },
			invoke: func(ctx accountManagerContext) error {
				_, err := ensureFactoryProductionProfile(ctx, "token", "brand", "key", "run")
				return err
			},
		},
		{
			name: "profile create incomplete", want: "returned no ID",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"device_item_profiles":[]}`))
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"device_item_profile":{}}`))
			},
			invoke: func(ctx accountManagerContext) error {
				_, err := ensureFactoryProductionProfile(ctx, "token", "brand", "key", "run")
				return err
			},
		},
		{
			name: "production run malformed", want: "invalid character",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("not-json"))
			},
			invoke: func(ctx accountManagerContext) error {
				_, _, err := createFactoryProductionRun(ctx, "token", "brand", "profile", "run", 1, time.Now())
				return err
			},
		},
		{
			name: "production run incomplete", want: "incomplete credential",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"production_run":{}}`))
			},
			invoke: func(ctx accountManagerContext) error {
				_, _, err := createFactoryProductionRun(ctx, "token", "brand", "profile", "run", 1, time.Now())
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			err := tc.invoke(accountManagerContext{BaseURL: server.URL})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want substring %q", err, tc.want)
			}
		})
	}
}

func TestGenerateLoadDevicesPrefersProductionJWT(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, `if strings.TrimSpace(in.ProductionJWT) != ""`) || !strings.Contains(text, `req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(in.ProductionJWT))`) {
		t.Fatal("device generator no longer prefers the production JWT")
	}
}
