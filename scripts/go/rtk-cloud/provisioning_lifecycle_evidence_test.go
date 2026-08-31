package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunProvisioningLifecycleEvidenceQualifiesDeactivationAndUnprovision(t *testing.T) {
	envRoot := t.TempDir()
	certPEM, keyPEM := testTLSCertificatePEM(t)
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatal(err)
	}
	users := []map[string]any{
		{
			"email": "deactivate@example.test", "password": "pw-deactivate", "tokens": map[string]any{"access_token": "cached-deactivate-token"},
			"app_credentials": map[string]any{"private_key_pem": keyPEM, "csr_pem": "redacted-csr"},
			"app_certificate": map[string]any{"certificate_chain_pem": certPEM},
		},
		{
			"email": "unprovision@example.test", "password": "pw-unprovision",
			"tokens":          map[string]any{"access_token": "cached-unprovision-token"},
			"app_credentials": map[string]any{"private_key_pem": keyPEM, "csr_pem": "redacted-csr"},
			"app_certificate": map[string]any{"certificate_chain_pem": certPEM},
		},
	}
	if err := store.ReplaceUsers("RTK", "org-1", "rtk", "member", users); err != nil {
		t.Fatal(err)
	}
	devices := []generatedDevice{
		{DeviceID: "device-deactivate", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming"}},
		{DeviceID: "device-unprovision", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming"}},
	}
	credentials := map[string]testDataDeviceCredential{
		"device-deactivate":  {DeviceID: "device-deactivate", CertPEM: certPEM, KeyPEM: keyPEM, ChainPEM: certPEM, FactoryEnrollResponseRedactedJSON: `{"status":"ok"}`},
		"device-unprovision": {DeviceID: "device-unprovision", CertPEM: certPEM, KeyPEM: keyPEM, ChainPEM: certPEM, FactoryEnrollResponseRedactedJSON: `{"status":"ok"}`},
	}
	if err := store.ReplaceDevices("RTK", "run-1", devices, credentials); err != nil {
		t.Fatal(err)
	}
	assignments := []bindAssignment{
		{AssignmentIndex: 0, AssignedEmail: "deactivate@example.test", DeviceID: "device-deactivate", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming"}, AccountDeviceID: "account-deactivate", OperationID: "operation-deactivate", Status: "provision_requested"},
		{AssignmentIndex: 1, AssignedEmail: "unprovision@example.test", DeviceID: "device-unprovision", DeviceType: "camera", ServiceOptions: []string{"mqtt", "video_streaming"}, ClaimID: "claim-unprovision", AccountDeviceID: "account-unprovision", OperationID: "operation-unprovision", Status: "provision_requested"},
	}
	if err := store.ReplaceBindings("RTK", "org-1", "rtk", "run-1", assignments); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	deactivated := false
	unprovisioned := false
	registryDisabled := false
	registryDeleteCalls := 0
	registryUpdateRejected := false
	registryStatusUpdateRejected := false
	transportReady := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		switch {
		case path == "/v1/orgs/org-1/devices/account-deactivate/deactivate":
			deactivated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"status":"accepted"}`)
		case path == "/v1/orgs/org-1/devices/account-deactivate" && req.Method == http.MethodDelete:
			registryDisabled = true
			registryDeleteCalls++
			w.WriteHeader(http.StatusNoContent)
		case path == "/v1/orgs/org-1/devices/account-deactivate" && req.Method == http.MethodGet && registryDisabled:
			_, _ = io.WriteString(w, `{"device":{"id":"account-deactivate","name":"device-deactivate","category":"ip_camera","status":"disabled","disabled_at":"2026-08-03T10:39:49Z"}}`)
		case path == "/v1/orgs/org-1/devices/account-deactivate" && req.Method == http.MethodPatch && registryDisabled:
			registryUpdateRejected = true
			http.Error(w, `{"code":"conflict"}`, http.StatusConflict)
		case path == "/v1/orgs/org-1/devices/account-deactivate/status" && req.Method == http.MethodPatch && registryDisabled:
			registryStatusUpdateRejected = true
			http.Error(w, `{"code":"conflict"}`, http.StatusConflict)
		case path == "/v1/orgs/org-1/devices/account-deactivate/provisioning":
			_ = json.NewEncoder(w).Encode(map[string]any{"operation": map[string]any{"status": "succeeded"}, "readiness": map[string]any{"state": "ready", "product_state": "deactivated", "sources": map[string]any{"video_cloud_activation_status": "deactivated"}}})
		case path == "/v1/orgs/org-1/devices/account-unprovision/unprovision" && !unprovisioned:
			unprovisioned = true
			_, _ = io.WriteString(w, `{"unprovision":{"device_id":"account-unprovision","organization_id":"org-1","video_cloud_devid":"device-unprovision","unprovisioned_at":"2026-08-01T00:00:00Z"}}`)
		case path == "/v1/orgs/org-1/devices" && unprovisioned:
			_, _ = io.WriteString(w, `{"devices":[]}`)
		case strings.HasPrefix(path, "/v1/orgs/org-1/devices/account-unprovision") && unprovisioned:
			http.NotFound(w, req)
		case path == "/api/devices/device-deactivate/lifecycle":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "devid": "device-deactivate", "activated": !deactivated, "provisioned": transportReady, "revoked": deactivated, "transport": map[string]any{}})
		case path == "/api/devices/device-unprovision/lifecycle":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "devid": "device-unprovision", "activated": true, "provisioned": transportReady && !unprovisioned, "revoked": false, "transport": map[string]any{}})
		case path == "/api/devices/device-deactivate/info":
			_, _ = io.WriteString(w, `{"status":"ok","devid":"device-deactivate"}`)
		case path == "/api/devices/device-unprovision/info" && !unprovisioned:
			_, _ = io.WriteString(w, `{"status":"ok","devid":"device-unprovision"}`)
		case unprovisioned && (strings.HasPrefix(path, "/api/devices/device-unprovision/") || path == "/api/request_webrtc/ice"):
			http.NotFound(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	originalAppToken := requestLifecycleAppToken
	originalDeviceToken := requestLifecycleDeviceToken
	originalRelayTest := executeLifecycleVideoRelayTest
	appTokenCalls := 0
	requestLifecycleAppToken = func(_ string, _ tls.Certificate, deviceID string) (videoRelayTokenResponse, error) {
		appTokenCalls++
		if deviceID == "device-deactivate" {
			return videoRelayTokenResponse{AccessToken: "deactivation-owner-app-token"}, nil
		}
		if appTokenCalls == 1 {
			return videoRelayTokenResponse{AccessToken: "former-owner-app-token"}, nil
		}
		return videoRelayTokenResponse{}, errors.New("owner binding absent")
	}
	requestLifecycleDeviceToken = func(string, tls.Certificate) (string, error) {
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"scope":"device","subject_id":"device-unprovision","service_options":["mqtt","video_streaming"]}`))
		return "header." + payload + ".signature", nil
	}
	executeLifecycleVideoRelayTest = func(string, string, string, string, string, string, string, int, int, string) (videoRelayResult, error) {
		transportReady = true
		return videoRelayResult{Status: "PASS", Overall: "pass"}, nil
	}
	defer func() {
		requestLifecycleAppToken = originalAppToken
		requestLifecycleDeviceToken = originalDeviceToken
		executeLifecycleVideoRelayTest = originalRelayTest
	}()
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("VIDEO_CLOUD_BASE_URL", server.URL)
	t.Setenv("VIDEO_CLOUD_TOKEN_BASE_URL", server.URL)
	t.Setenv("VIDEO_CLOUD_LOAD_ADMIN_TOKEN", "admin-token")
	outDir := t.TempDir()
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	err = runProvisioningLifecycleEvidence([]string{
		"--workspace", workspace, "--env-root", envRoot, "--brandname", "RTK", "--run-id", "run-1",
		"--out-dir", outDir, "--timeout", "1s", "--poll", "1ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !deactivated || !registryDisabled || registryDeleteCalls != 2 || !registryUpdateRejected || !registryStatusUpdateRejected || !unprovisioned || appTokenCalls != 3 {
		t.Fatalf("deactivated=%t registry_disabled=%t delete_calls=%d update_rejected=%t status_update_rejected=%t unprovisioned=%t app_token_calls=%d", deactivated, registryDisabled, registryDeleteCalls, registryUpdateRejected, registryStatusUpdateRejected, unprovisioned, appTokenCalls)
	}
	var result map[string]any
	resultBytes, err := os.ReadFile(filepath.Join(outDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		t.Fatal(err)
	}
	readiness, _ := result["transport_readiness"].(map[string]any)
	if readiness["prepared_by_lifecycle"] != true || readiness["status"] != "PASS" {
		t.Fatalf("transport readiness evidence = %#v", readiness)
	}
	for _, name := range []string{"results.json", "junit.xml", "test_report.md", "provisioning-account-workflow.json", "provisioning-claim-workflow.json", "provisioning-deactivation-workflow.json", "provisioning-unprovision-workflow.json", "provisioning-signoff-workflow.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	workflowBytes, err := os.ReadFile(filepath.Join(outDir, "provisioning-account-workflow.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, assertion := range []string{
		"disabled_device_readable", "disabled_status_confirmed", "disabled_at_present",
		"disabled_device_update_rejected", "disabled_device_status_update_rejected", "repeated_delete_idempotent",
	} {
		if !strings.Contains(string(workflowBytes), `"`+assertion+`": "PASS"`) {
			t.Fatalf("provisioning workflow evidence missing %s: %s", assertion, workflowBytes)
		}
	}
}

func TestDisableAccountRegistryDeviceEnforcesSoftDisableContract(t *testing.T) {
	validBody := `{"device":{"id":"device-1","name":"Device 1","category":"mqtt_device","status":"disabled","disabled_at":"2026-08-03T10:39:49Z"}}`
	tests := []struct {
		name               string
		getStatus          int
		getBody            string
		updateStatus       int
		statusUpdateStatus int
		repeatDeleteStatus int
		wantError          string
	}{
		{name: "soft disabled contract", getStatus: http.StatusOK, getBody: validBody, updateStatus: http.StatusConflict, statusUpdateStatus: http.StatusConflict, repeatDeleteStatus: http.StatusNoContent},
		{name: "device must remain readable", getStatus: http.StatusNotFound, getBody: `{"code":"not_found"}`, updateStatus: http.StatusConflict, statusUpdateStatus: http.StatusConflict, repeatDeleteStatus: http.StatusNoContent, wantError: "is not readable"},
		{name: "disabled status required", getStatus: http.StatusOK, getBody: `{"device":{"id":"device-1","name":"Device 1","category":"mqtt_device","status":"online","disabled_at":"2026-08-03T10:39:49Z"}}`, updateStatus: http.StatusConflict, statusUpdateStatus: http.StatusConflict, repeatDeleteStatus: http.StatusNoContent, wantError: "did not persist soft-disabled state"},
		{name: "disabled timestamp required", getStatus: http.StatusOK, getBody: `{"device":{"id":"device-1","name":"Device 1","category":"mqtt_device","status":"disabled"}}`, updateStatus: http.StatusConflict, statusUpdateStatus: http.StatusConflict, repeatDeleteStatus: http.StatusNoContent, wantError: "disabled_at_present=false"},
		{name: "device update rejected", getStatus: http.StatusOK, getBody: validBody, updateStatus: http.StatusOK, statusUpdateStatus: http.StatusConflict, repeatDeleteStatus: http.StatusNoContent, wantError: "update was not rejected"},
		{name: "status update rejected", getStatus: http.StatusOK, getBody: validBody, updateStatus: http.StatusConflict, statusUpdateStatus: http.StatusOK, repeatDeleteStatus: http.StatusNoContent, wantError: "status update was not rejected"},
		{name: "repeated delete idempotent", getStatus: http.StatusOK, getBody: validBody, updateStatus: http.StatusConflict, statusUpdateStatus: http.StatusConflict, repeatDeleteStatus: http.StatusNotFound, wantError: "was not idempotent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleteCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer user-token" {
					t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
				}
				switch {
				case req.Method == http.MethodDelete && req.URL.Path == "/v1/orgs/org-1/devices/device-1":
					deleteCalls++
					if deleteCalls == 1 {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					w.WriteHeader(tt.repeatDeleteStatus)
				case req.Method == http.MethodGet && req.URL.Path == "/v1/orgs/org-1/devices/device-1":
					w.WriteHeader(tt.getStatus)
					_, _ = io.WriteString(w, tt.getBody)
				case req.Method == http.MethodPatch && req.URL.Path == "/v1/orgs/org-1/devices/device-1":
					w.WriteHeader(tt.updateStatus)
				case req.Method == http.MethodPatch && req.URL.Path == "/v1/orgs/org-1/devices/device-1/status":
					w.WriteHeader(tt.statusUpdateStatus)
				default:
					http.NotFound(w, req)
				}
			}))
			defer server.Close()

			err := disableAccountRegistryDevice(accountManagerContext{BaseURL: server.URL}, "org-1", "device-1", "user-token")
			if tt.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				if deleteCalls != 2 {
					t.Fatalf("delete calls = %d, want 2", deleteCalls)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantError)
			}
		})
	}
}

func TestPrepareLifecycleTransportReadinessReportsRunnerFailures(t *testing.T) {
	original := executeLifecycleVideoRelayTest
	defer func() { executeLifecycleVideoRelayTest = original }()

	executeLifecycleVideoRelayTest = func(string, string, string, string, string, string, string, int, int, string) (videoRelayResult, error) {
		return videoRelayResult{}, errors.New("runner unavailable")
	}
	if err := prepareLifecycleTransportReadiness("workspace", "env", "RTK", t.TempDir()); err == nil || !strings.Contains(err.Error(), "runner unavailable") {
		t.Fatalf("runner error = %v", err)
	}

	executeLifecycleVideoRelayTest = func(string, string, string, string, string, string, string, int, int, string) (videoRelayResult, error) {
		return videoRelayResult{Status: "FAIL"}, nil
	}
	if err := prepareLifecycleTransportReadiness("workspace", "env", "RTK", t.TempDir()); err == nil || !strings.Contains(err.Error(), "status=FAIL") {
		t.Fatalf("status error = %v", err)
	}
}

func TestSelectReadyLifecycleAssignmentsRequiresClaimCorrelatedVideoUnprovision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		deviceID := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/api/devices/"), "/lifecycle")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "devid": deviceID, "activated": true, "provisioned": true, "revoked": false,
			"transport": map[string]any{},
		})
	}))
	defer server.Close()
	assignments := []bindAssignment{
		{DeviceID: "bulk-light", AccountDeviceID: "account-light", OperationID: "op-light", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "claimed-camera", ClaimID: "claim-1", AccountDeviceID: "account-claim", OperationID: "op-claim", ServiceOptions: []string{"mqtt", "video_streaming"}},
	}
	deactivation, unprovision, _, _, err := selectReadyLifecycleAssignments(server.URL, "token", assignments, 0, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if deactivation.DeviceID != "bulk-light" || unprovision.DeviceID != "claimed-camera" {
		t.Fatalf("deactivation=%+v unprovision=%+v", deactivation, unprovision)
	}
}

func TestLifecycleUserTokenUsesRunScopedSessionBeforePasswordLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Fatalf("cached lifecycle session unexpectedly called %s", req.URL.Path)
	}))
	defer server.Close()

	token, err := lifecycleUserToken(
		accountManagerContext{BaseURL: server.URL},
		"rtk-test",
		map[string]userCredential{
			"member@example.test": {
				Email:  "member@example.test",
				Tokens: accountPlatformSession{AccessToken: "run-scoped-access-token"},
			},
		},
		bindAssignment{AssignedEmail: "member@example.test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if token != "run-scoped-access-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLifecycleUserTokenFallsBackToTenantScopedLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/auth/login" {
			t.Fatalf("login path = %q", req.URL.Path)
		}
		_, _ = io.WriteString(w, `{"tokens":{"access_token":"tenant-access-token","refresh_token":"tenant-refresh-token"}}`)
	}))
	defer server.Close()

	token, err := lifecycleUserToken(
		accountManagerContext{BaseURL: server.URL},
		"rtk-test",
		map[string]userCredential{
			"member@example.test": {Email: "member@example.test", Password: "password"},
		},
		bindAssignment{AssignedEmail: "member@example.test"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if token != "tenant-access-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestSelectReadyLifecycleAssignmentsSkipsUnprovisionedDevices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		deviceID := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/api/devices/"), "/lifecycle")
		state := map[string]any{"status": "ok", "devid": deviceID, "activated": true, "provisioned": true, "revoked": false, "transport": map[string]any{}}
		if deviceID == "camera-not-connected" {
			state["provisioned"] = false
		}
		_ = json.NewEncoder(w).Encode(state)
	}))
	defer server.Close()

	assignments := []bindAssignment{
		{DeviceID: "camera-not-connected", ServiceOptions: []string{"mqtt", "video_streaming"}},
		{DeviceID: "light-connected", AccountDeviceID: "account-light", OperationID: "op-light", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "camera-connected", ClaimID: "claim-camera", AccountDeviceID: "account-camera", OperationID: "op-camera", ServiceOptions: []string{"mqtt", "video_streaming", "video_storage"}},
	}
	deactivation, unprovision, deactivationState, unprovisionState, err := selectReadyLifecycleAssignments(server.URL, "admin-token", assignments, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if deactivation.DeviceID != "light-connected" || unprovision.DeviceID != "camera-connected" {
		t.Fatalf("selected deactivation=%s unprovision=%s", deactivation.DeviceID, unprovision.DeviceID)
	}
	if !deactivationState.Provisioned || !unprovisionState.Provisioned {
		t.Fatalf("selected states = %+v / %+v", deactivationState, unprovisionState)
	}
}

func TestSelectReadyLifecycleAssignmentsReportsReadinessFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "unavailable") {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","devid":"camera-not-ready","activated":true,"provisioned":false,"revoked":false,"transport":{}}`)
	}))
	defer server.Close()

	_, _, _, _, err := selectReadyLifecycleAssignments(server.URL, "admin-token", []bindAssignment{
		{DeviceID: "camera-not-ready", ServiceOptions: []string{"mqtt", "video_streaming"}},
		{DeviceID: "unavailable", ServiceOptions: []string{"mqtt"}},
	}, time.Nanosecond, time.Nanosecond)
	if err == nil || !strings.Contains(err.Error(), "camera-not-ready=activated:true,provisioned:false,revoked:false") || !strings.Contains(err.Error(), "unavailable=error(") {
		t.Fatalf("error = %v", err)
	}
}

func TestCanonicalLifecycleAndDeviceInfoRequireMatchingIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer admin-token" {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		switch req.URL.Path {
		case "/api/devices/device-1/lifecycle":
			_, _ = io.WriteString(w, `{"status":"ok","devid":"device-1","activated":true,"provisioned":false,"revoked":false,"transport":{}}`)
		case "/api/devices/device-1/info":
			_, _ = io.WriteString(w, `{"status":"ok","devid":"device-1","info":{}}`)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	lifecycle, err := readCanonicalVideoLifecycle(server.URL, "admin-token", "device-1")
	if err != nil {
		t.Fatal(err)
	}
	if !lifecycle.Activated || lifecycle.Provisioned || lifecycle.Revoked {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
	if err := readCanonicalDeviceInfo(server.URL, "admin-token", "device-1"); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForVideoLifecycleObservesConvergence(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		provisioned := calls < 2
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "devid": "device-1", "activated": true, "provisioned": provisioned, "revoked": false, "transport": map[string]any{}})
	}))
	defer server.Close()

	state, err := waitForVideoLifecycle(server.URL, "admin-token", "device-1", time.Second, time.Millisecond, func(state canonicalVideoLifecycle) bool {
		return state.Activated && !state.Provisioned && !state.Revoked
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Provisioned || calls < 2 {
		t.Fatalf("state=%+v calls=%d", state, calls)
	}
}

func TestAccessTokenServiceOptionsRequiresDeviceClaims(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"scope": "device", "subject_id": "device-1", "service_options": []string{"mqtt", "video_streaming"}})
	if err != nil {
		t.Fatal(err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	options, err := accessTokenServiceOptions(token)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStringSet(options, []string{"mqtt", "video_streaming"}) {
		t.Fatalf("service options = %v", options)
	}
	if _, err := accessTokenServiceOptions("not-a-token"); err == nil {
		t.Fatal("malformed token unexpectedly passed")
	}
}

func TestAccountDeviceStillVisibleUsesAuthenticatedInventory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/orgs/org-1/devices" || req.URL.Query().Get("limit") != "1000" {
			t.Fatalf("request = %s", req.URL.String())
		}
		_, _ = io.WriteString(w, `{"devices":[{"id":"account-device-1"}]}`)
	}))
	defer server.Close()
	visible, err := accountDeviceStillVisible(accountManagerContext{BaseURL: server.URL}, "org-1", "account-device-1", "user-token")
	if err != nil {
		t.Fatal(err)
	}
	if !visible {
		t.Fatal("account device was not found")
	}
}

func TestVerifyFormerOwnerAccessRevokedChecksEveryProductSurface(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Authorization") == "" {
			t.Fatal("former-owner request missing authorization")
		}
		http.Error(w, `{"error":"not_found"}`, http.StatusNotFound)
	}))
	defer server.Close()
	original := requestLifecycleAppToken
	requestLifecycleAppToken = func(string, tls.Certificate, string) (videoRelayTokenResponse, error) {
		return videoRelayTokenResponse{}, errors.New("owner binding absent")
	}
	defer func() { requestLifecycleAppToken = original }()
	assignment := bindAssignment{DeviceID: "device-1", AccountDeviceID: "account-device-1", ServiceOptions: []string{"mqtt"}}
	if err := verifyFormerOwnerAccessRevoked(accountManagerContext{BaseURL: server.URL}, server.URL, server.URL, "org-1", assignment, "account-token", "app-token", tls.Certificate{}, "run-1"); err != nil {
		t.Fatal(err)
	}
	if calls != 8 {
		t.Fatalf("former-owner checks = %d, want 8", calls)
	}
}

func TestVerifyFormerOwnerAccessRevokedRejectsSuccessfulFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()
	original := requestLifecycleAppToken
	requestLifecycleAppToken = func(string, tls.Certificate, string) (videoRelayTokenResponse, error) {
		return videoRelayTokenResponse{}, errors.New("owner binding absent")
	}
	defer func() { requestLifecycleAppToken = original }()
	assignment := bindAssignment{DeviceID: "device-1", AccountDeviceID: "account-device-1", ServiceOptions: []string{"mqtt"}}
	if err := verifyFormerOwnerAccessRevoked(accountManagerContext{BaseURL: server.URL}, server.URL, server.URL, "org-1", assignment, "account-token", "app-token", tls.Certificate{}, "run-1"); err == nil {
		t.Fatal("successful former-owner access unexpectedly passed")
	}
}

func TestWriteProvisioningWorkflowEvidenceProducesAllCanonicalWorkflows(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	wantSteps := map[string]map[string]bool{}
	for _, workflow := range inventory.Workflows {
		if workflow.ID != "WF-PROV-ACCOUNT-001" && workflow.ID != "WF-PROV-CLAIM-001" && workflow.ID != provisioningDeactivationWorkflowID && workflow.ID != provisioningUnprovisionWorkflowID && workflow.ID != provisioningSignoffWorkflowID {
			continue
		}
		wantSteps[workflow.ID] = map[string]bool{}
		for _, step := range workflow.Steps {
			wantSteps[workflow.ID][step.ID] = true
		}
	}
	outDir := t.TempDir()
	if err := writeProvisioningWorkflowEvidence(outDir); err != nil {
		t.Fatal(err)
	}
	for file, workflowID := range map[string]string{
		"provisioning-account-workflow.json":      "WF-PROV-ACCOUNT-001",
		"provisioning-claim-workflow.json":        "WF-PROV-CLAIM-001",
		"provisioning-deactivation-workflow.json": provisioningDeactivationWorkflowID,
		"provisioning-unprovision-workflow.json":  provisioningUnprovisionWorkflowID,
		"provisioning-signoff-workflow.json":      provisioningSignoffWorkflowID,
	} {
		var payload struct {
			Workflow struct {
				WorkflowID string                       `json:"workflow_id"`
				Steps      map[string]string            `json:"steps"`
				Assertions map[string]map[string]string `json:"assertions"`
			} `json:"workflow"`
		}
		if err := readJSONFile(filepath.Join(outDir, file), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Workflow.WorkflowID != workflowID || len(payload.Workflow.Steps) == 0 || len(payload.Workflow.Assertions) != len(payload.Workflow.Steps) {
			t.Fatalf("%s = %+v", file, payload.Workflow)
		}
		if len(payload.Workflow.Steps) != len(wantSteps[workflowID]) {
			t.Fatalf("%s steps = %v, spec wants %v", workflowID, payload.Workflow.Steps, wantSteps[workflowID])
		}
		for stepID := range payload.Workflow.Steps {
			if !wantSteps[workflowID][stepID] {
				t.Fatalf("%s emitted unknown step %s", workflowID, stepID)
			}
		}
	}
}

func TestProvisioningLiveCasesProduceRequirementAndWorkflowEvidence(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := writeProvisioningWorkflowEvidence(outDir); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(outDir, "bulk-provisioning-workflow.json"), map[string]any{
		"schema_version": "rtk-live-workflow-evidence/v1",
		"workflow": map[string]any{
			"workflow_id": "WF-PROV-BULK-001",
			"steps":       map[string]string{"provision_registry_device": "PASS", "wait_for_provisioning": "PASS"},
			"assertions": map[string]map[string]string{
				"provision_registry_device": {"operation_created": "PASS"},
				"wait_for_provisioning":     {"all_devices_ready": "PASS"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), map[string]any{"status": "PASS"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "test_report.md"), []byte("# Provisioning PASS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "junit.xml"), []byte(`<testsuite tests="1"><testcase name="provisioning"/></testsuite>`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, testID := range []string{"E2E-PROV-ACCOUNT-001", "E2E-PROV-BULK-001"} {
		if err := writeCaseFeatureEvidence(workspace, outDir, testID, "provision-live-evidence", "staging", "", now, now.Add(time.Second)); err != nil {
			t.Fatalf("write %s evidence: %v", testID, err)
		}
	}
	var manifest featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(outDir, "feature-evidence.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Cases) != 2 {
		t.Fatalf("cases = %d, want 2", len(manifest.Cases))
	}
	for _, item := range manifest.Cases {
		if item.Status != "PASS" || len(item.Requirements) == 0 || len(item.Workflows) == 0 {
			t.Fatalf("incomplete provisioning evidence: %+v", item)
		}
	}
}

func TestProvisioningLifecycleEvidenceRequiresExplicitInputs(t *testing.T) {
	if err := runProvisioningLifecycleEvidence(nil); err == nil {
		t.Fatal("missing lifecycle inputs unexpectedly passed")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "results.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected evidence file: %v", err)
	}
}
