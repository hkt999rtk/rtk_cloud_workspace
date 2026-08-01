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
	"testing"
	"time"
)

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
		if workflow.ID != provisioningDeactivationWorkflowID && workflow.ID != provisioningUnprovisionWorkflowID && workflow.ID != provisioningSignoffWorkflowID {
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

func TestProvisioningLifecycleEvidenceRequiresExplicitInputs(t *testing.T) {
	if err := runProvisioningLifecycleEvidence(nil); err == nil {
		t.Fatal("missing lifecycle inputs unexpectedly passed")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "results.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected evidence file: %v", err)
	}
}
