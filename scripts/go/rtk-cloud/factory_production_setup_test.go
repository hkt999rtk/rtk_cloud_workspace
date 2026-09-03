package main

import (
	"encoding/json"
	"fmt"
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
			if payload["allowed_quantity"] != float64(12) || payload["batch_id"] != "runtime-123-1" || payload["factory_id"] != runtimeFactoryProductionID {
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

func TestFactoryProductionAccountContextFallsBackToOperatorEnvironment(t *testing.T) {
	root := t.TempDir()
	platformDir := filepath.Join(root, "services", "account-manager")
	if err := os.MkdirAll(filepath.Join(root, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "env", "stack.env"), []byte("CLOUD_PROVIDER=local\nACCOUNT_MANAGER_DOMAIN=accounts.example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformDir, "account-manager-platform-admin.env"), []byte("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=admin@example.test\nACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=test-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", "")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "")
	t.Setenv("CLOUD_PROVIDER", "")

	ctx, err := factoryProductionAccountContext(root, root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Close()
	if ctx.BaseURL != "https://accounts.example.test" || ctx.AdminEmail != "admin@example.test" || ctx.AdminPassword != "test-password" {
		t.Fatalf("fallback context = %+v", ctx)
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
	if step.Status != "PASS" || step.ExitCode != 0 || envListValue(env, "FACTORY_ENROLL_PRODUCTION_JWT") != secretJWT || envListValue(env, "FACTORY_ENROLL_RUN_ID") != "runtime-1" || envListValue(env, "FACTORY_ENROLL_FACTORY_ID") != runtimeFactoryProductionID || envListValue(env, "FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID") != "profile" {
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

func TestPrepareFactoryProductionBundleStepPassesPerTypeCredentialsOnlyToChildEnv(t *testing.T) {
	root := t.TempDir()
	logsDir := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	credentials := factoryProductionCredentialBundle{
		"camera": {JWT: "camera-secret", BrandCloudID: "brand", DeviceItemProfileID: "camera-product", ProductionRunID: "camera-run", BatchID: "batch-camera"},
		"light":  {JWT: "light-secret", BrandCloudID: "brand", DeviceItemProfileID: "light-product", ProductionRunID: "light-run", BatchID: "batch-light"},
	}
	env, step, err := prepareFactoryProductionBundleStep("workspace", "env", root, logsDir, "RTK", "runtime-1", 2, "camera=1,light=1", time.Now().UTC(), func(_, _, brand, run string, quantity int, mix string, _ time.Time) (factoryProductionCredentialBundle, error) {
		if brand != "RTK" || run != "runtime-1" || quantity != 2 || mix != "camera=1,light=1" {
			t.Fatalf("prepare args brand=%s run=%s quantity=%d mix=%s", brand, run, quantity, mix)
		}
		return credentials, nil
	})
	if err != nil || step.Status != "PASS" {
		t.Fatalf("env=%v step=%+v err=%v", env, step, err)
	}
	jwtByType := map[string]string{}
	if err := json.Unmarshal([]byte(envListValue(env, "FACTORY_ENROLL_PRODUCTION_JWT_BY_DEVICE_TYPE")), &jwtByType); err != nil {
		t.Fatal(err)
	}
	profileByType := map[string]string{}
	if err := json.Unmarshal([]byte(envListValue(env, "FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID_BY_DEVICE_TYPE")), &profileByType); err != nil {
		t.Fatal(err)
	}
	if jwtByType["camera"] != "camera-secret" || jwtByType["light"] != "light-secret" || profileByType["camera"] != "camera-product" || profileByType["light"] != "light-product" {
		t.Fatalf("jwt=%v profile=%v", jwtByType, profileByType)
	}
	for _, path := range []string{step.LogFile, filepath.Join(root, "factory-production.json")} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(raw), "camera-secret") || strings.Contains(string(raw), "light-secret") {
			t.Fatalf("%s leaked a production JWT", path)
		}
	}
	assertFactoryProductionBundleRejectsInvalidBundlesAndPaths(t)
}

func assertFactoryProductionBundleRejectsInvalidBundlesAndPaths(t *testing.T) {
	valid := factoryProductionCredentialBundle{
		"camera": {JWT: "camera-secret", BrandCloudID: "brand", DeviceItemProfileID: "camera-product", ProductionRunID: "camera-run", BatchID: "batch-camera"},
	}
	tests := []struct {
		name      string
		prepare   factoryProductionBundlePreparer
		configure func(t *testing.T, root string) (string, string)
	}{
		{
			name: "preparer error",
			prepare: func(string, string, string, string, int, string, time.Time) (factoryProductionCredentialBundle, error) {
				return nil, http.ErrServerClosed
			},
		},
		{
			name: "empty bundle",
			prepare: func(string, string, string, string, int, string, time.Time) (factoryProductionCredentialBundle, error) {
				return factoryProductionCredentialBundle{}, nil
			},
		},
		{
			name: "incomplete credential",
			prepare: func(string, string, string, string, int, string, time.Time) (factoryProductionCredentialBundle, error) {
				return factoryProductionCredentialBundle{"camera": {BrandCloudID: "brand"}}, nil
			},
		},
		{
			name: "unwritable evidence path",
			prepare: func(string, string, string, string, int, string, time.Time) (factoryProductionCredentialBundle, error) {
				return valid, nil
			},
			configure: func(t *testing.T, root string) (string, string) {
				out := filepath.Join(root, "out-file")
				if err := os.WriteFile(out, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				return out, filepath.Join(root, "logs")
			},
		},
		{
			name: "unwritable log path",
			prepare: func(string, string, string, string, int, string, time.Time) (factoryProductionCredentialBundle, error) {
				return valid, nil
			},
			configure: func(t *testing.T, root string) (string, string) {
				logs := filepath.Join(root, "logs-file")
				if err := os.WriteFile(logs, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
				return root, logs
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			outDir, logsDir := root, filepath.Join(root, "logs")
			if tt.configure != nil {
				outDir, logsDir = tt.configure(t, root)
			}
			if err := os.MkdirAll(logsDir, 0o755); err != nil && tt.name != "unwritable log path" {
				t.Fatal(err)
			}
			env, step, err := prepareFactoryProductionBundleStep("workspace", "env", outDir, logsDir, "RTK", "runtime-1", 1, "camera=1", time.Now().UTC(), tt.prepare)
			if err == nil || len(env) != 0 {
				t.Fatalf("env=%v step=%+v err=%v", env, step, err)
			}
			if (tt.name == "preparer error" || tt.name == "empty bundle") && (step.Status != "FAIL" || step.ExitCode != 1) {
				t.Fatalf("step=%+v", step)
			}
		})
	}
}

func assertFactoryProductionCredentialsRejectInvalidInputs(t *testing.T) {
	for _, test := range []struct {
		name, runID, mix string
		quantity         int
	}{
		{name: "missing run", quantity: 1, mix: "camera=1"},
		{name: "non-positive quantity", runID: "run", quantity: 0, mix: "camera=1"},
		{name: "invalid mix", runID: "run", quantity: 1, mix: "camera=invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := prepareFactoryProductionCredentials(t.TempDir(), t.TempDir(), "RTK", test.runID, test.quantity, test.mix, time.Now().UTC()); err == nil {
				t.Fatal("invalid factory production input was accepted")
			}
		})
	}
}

func TestPrepareFactoryProductionCredentialsCreatesProductPerDeviceType(t *testing.T) {
	profileID := 0
	categories := []string{}
	runBatches := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "admin-token"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/brand-clouds":
			_ = json.NewEncoder(w).Encode(map[string]any{"brand_clouds": []map[string]any{{"id": "brand-001", "name": "RTK"}}})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/device-item-profiles"):
			_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profiles": []any{}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/device-item-profiles"):
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			categories = append(categories, payload["category"].(string))
			profileID++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"device_item_profile": map[string]string{"id": fmt.Sprintf("profile-%d", profileID)}})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/production-runs"):
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			runBatches = append(runBatches, payload["batch_id"].(string))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"production_run": map[string]string{"id": "run-" + payload["batch_id"].(string)}, "factory_jwt": "jwt-" + payload["batch_id"].(string)})
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "admin@example.test")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "test-password")
	t.Setenv("CLOUD_PROVIDER", "local")
	credentials, err := prepareFactoryProductionCredentials(t.TempDir(), t.TempDir(), "RTK", "runtime-1", 2, "camera=1,light=1", time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 2 || credentials["camera"].BatchID != "runtime-1-camera" || credentials["light"].BatchID != "runtime-1-light" {
		t.Fatalf("credentials=%+v", credentials)
	}
	if strings.Join(categories, ",") != "ip_camera,mqtt_device" || strings.Join(runBatches, ",") != "runtime-1-camera,runtime-1-light" {
		t.Fatalf("categories=%v batches=%v", categories, runBatches)
	}
	assertFactoryProductionCredentialsRejectInvalidInputs(t)
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
