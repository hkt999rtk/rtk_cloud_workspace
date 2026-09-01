package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunMulticloudLiveScenarioUsesGlobalOwnerInvitationAndDeletionAPIs(t *testing.T) {
	const (
		cloudID  = "11111111-1111-4111-8111-111111111111"
		ownerID  = "22222222-2222-4222-8222-222222222222"
		viewerID = "33333333-3333-4333-8333-333333333333"
	)
	var mu sync.Mutex
	created, accepted, revoked, deleted := false, false, false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		owner := r.Header.Get("Authorization") == "Bearer owner-token"
		viewer := r.Header.Get("Authorization") == "Bearer viewer-token"
		write := func(status int, body any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if body != nil {
				_ = json.NewEncoder(w).Encode(body)
			}
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/developer/brand-clouds" && owner:
			if r.Header.Get("Idempotency-Key") == "" {
				t.Fatal("create omitted Idempotency-Key")
			}
			created = true
			write(http.StatusCreated, map[string]any{"brand_cloud": map[string]any{"id": cloudID, "owner_user_id": ownerID, "my_role": "owner"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds" && owner && r.URL.Query().Get("view") == "owned":
			write(http.StatusOK, map[string]any{"brand_clouds": []any{map[string]any{"id": cloudID, "my_role": "owner"}}, "owned_count": 2, "owned_limit": 8})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && owner:
			write(http.StatusOK, map[string]any{"brand_cloud": map[string]any{"id": cloudID, "my_role": "owner"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID+"/members/invitations" && owner:
			write(http.StatusAccepted, map[string]any{"invitation": map[string]any{"status": "pending", "target_user_id": viewerID, "access_scope": map[string]any{"kind": "all_products"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/developer/brand-cloud-member-invitations/accept" && viewer:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["token"] != "email-token" {
				t.Fatalf("accept token = %v", body["token"])
			}
			accepted = true
			write(http.StatusOK, map[string]any{"member": map[string]any{"user_id": viewerID, "role": "viewer"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds" && viewer && r.URL.Query().Get("view") == "shared":
			write(http.StatusOK, map[string]any{"brand_clouds": []any{map[string]any{"id": cloudID, "my_role": "viewer"}}, "owned_count": 0, "owned_limit": 8})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && viewer:
			write(http.StatusForbidden, map[string]any{"error": map[string]any{"code": "forbidden"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID+"/members/"+viewerID && owner:
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && viewer && revoked:
			write(http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID+"/deletion-preflight" && owner:
			write(http.StatusOK, map[string]any{"eligible": true, "blockers": []any{}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && owner:
			deleted = true
			write(http.StatusAccepted, map[string]any{"operation": map[string]any{"id": "44444444-4444-4444-8444-444444444444", "state": "running"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID+"/operations/44444444-4444-4444-8444-444444444444" && owner:
			write(http.StatusOK, map[string]any{"operation": map[string]any{"state": "succeeded"}})
		default:
			write(http.StatusNotFound, map[string]any{"method": r.Method, "path": r.URL.Path})
		}
	}))
	defer server.Close()

	result, err := runMulticloudLiveScenario(context.Background(), multicloudLiveHTTPClient{
		baseURL: server.URL, client: server.Client(),
	}, multicloudLiveScenarioInput{
		runID: "live-test-001", ownerToken: "owner-token", ownerUserID: ownerID,
		viewerToken: "viewer-token", viewerID: viewerID, viewerEmail: "viewer@example.test",
		inviteToken: func() (string, error) { return "email-token", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !created || !accepted || !revoked || !deleted || result.DeletionState != "succeeded" {
		t.Fatalf("scenario state created=%t accepted=%t revoked=%t deleted=%t result=%+v", created, accepted, revoked, deleted, result)
	}
	if len(result.Lifecycle) != 6 || len(result.Sharing) != 6 || result.OwnedCount != 2 || result.OwnedLimit != 8 {
		t.Fatalf("incomplete workflow result: %+v", result)
	}
}

func TestRunMulticloudLiveScenarioFailsClosedWhenViewerCanWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/developer/brand-clouds":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"brand_cloud":{"id":"cloud","owner_user_id":"owner","my_role":"owner"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds" && r.URL.Query().Get("view") == "owned":
			_, _ = w.Write([]byte(`{"brand_clouds":[{"id":"cloud","my_role":"owner"}],"owned_count":2,"owned_limit":8}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/developer/brand-clouds/cloud" && r.Header.Get("Authorization") == "Bearer owner":
			_, _ = w.Write([]byte(`{"brand_cloud":{"id":"cloud"}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/members/invitations"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"invitation":{"status":"pending","target_user_id":"viewer","access_scope":{"kind":"all_products"}}}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/accept"):
			_, _ = w.Write([]byte(`{"member":{"role":"viewer"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds":
			_, _ = w.Write([]byte(`{"brand_clouds":[{"id":"cloud","my_role":"viewer"}]}`))
		case r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"brand_cloud":{"id":"cloud"}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/developer/brand-clouds/cloud":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation":{"id":"cleanup"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	_, err := runMulticloudLiveScenario(context.Background(), multicloudLiveHTTPClient{baseURL: server.URL, client: server.Client()}, multicloudLiveScenarioInput{
		runID: "live-test-002", ownerToken: "owner", ownerUserID: "owner", viewerToken: "viewer-token", viewerID: "viewer", viewerEmail: "viewer@example.test", inviteToken: func() (string, error) { return "token", nil },
	})
	if err == nil || !strings.Contains(err.Error(), "viewer cloud mutation was not rejected") {
		t.Fatalf("viewer write must fail closed: %v", err)
	}
}

func TestWriteMulticloudLiveEvidenceIsRedactedAndWorkflowBound(t *testing.T) {
	dir := t.TempDir()
	result := multicloudLiveScenarioResult{
		Lifecycle: map[string]string{"create_cloud": "PASS"}, Sharing: map[string]string{"invite_viewer": "PASS"},
		OwnedCount: 2, OwnedLimit: 8,
	}
	if err := writeMulticloudLiveEvidence(dir, "live-test-003", time.Unix(1, 0).UTC(), time.Unix(2, 0).UTC(), result); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"rtk.multicloud-staging-qualification.v1", "WF-MULTICLOUD-LIFECYCLE-001", "WF-MULTICLOUD-SHARING-001#cloud-membership", `"owned_limit": 8`} {
		if !strings.Contains(text, want) {
			t.Fatalf("evidence missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"password", "access_token", "refresh_token", "private_key", "invitation_token"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("evidence contains credential field %q", forbidden)
		}
	}
}
