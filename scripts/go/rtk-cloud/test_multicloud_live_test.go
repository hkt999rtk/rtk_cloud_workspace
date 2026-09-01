package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunTestMulticloudPlanAndValidationFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"--unknown"}, want: "flag provided but not defined"},
		{name: "profile", args: []string{"--profile", "production"}, want: "unsupported multicloud profile"},
		{name: "run id", args: []string{"--run-id", "INVALID"}, want: "run-id must use"},
		{name: "confirmation", args: []string{"--run", "--confirm", "wrong"}, want: "--confirm must equal"},
		{name: "required live flags", args: []string{"--run", "--confirm", multicloudLiveConfirmation}, want: "requires --brandname and --env-root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runTestMulticloud(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runTestMulticloud(%v) error = %v, want %q", tc.args, err, tc.want)
			}
		})
	}
	if err := runTestMulticloud([]string{"--run-id", "plan-test-001"}); err != nil {
		t.Fatal(err)
	}
	if err := runTestMulticloud(nil); err != nil {
		t.Fatal(err)
	}
	productionRoot := filepath.Join(t.TempDir(), "runtime")
	writeTestFile(t, filepath.Join(productionRoot, "env", "stack.env"), "CLOUD_ENV_NAME=production\nCLOUD_PROVIDER=test\n")
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", "http://127.0.0.1:1")
	if err := runTestMulticloud([]string{"--run", "--confirm", multicloudLiveConfirmation, "--brandname", "cloud", "--env-root", productionRoot}); err == nil || !strings.Contains(err.Error(), "refuses a non-staging environment") {
		t.Fatalf("non-staging live error = %v", err)
	}
	stagingRoot := filepath.Join(t.TempDir(), "runtime")
	writeTestFile(t, filepath.Join(stagingRoot, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_PROVIDER=test\n")
	if err := runTestMulticloud([]string{"--run", "--confirm", multicloudLiveConfirmation, "--brandname", "cloud", "--env-root", stagingRoot}); err == nil || !strings.Contains(err.Error(), "no owner credential") {
		t.Fatalf("missing cached owner error = %v", err)
	}
}

func TestRunTestMulticloudExecutesQualificationFromCachedGlobalOwner(t *testing.T) {
	const (
		brandName = "RTK-LOAD-CANARY-live-cli-001-B01"
		ownerID   = "22222222-2222-4222-8222-222222222222"
		viewerID  = "33333333-3333-4333-8333-333333333333"
		cloudID   = "11111111-1111-4111-8111-111111111111"
	)
	ownerEmail := "owner@example.test"
	viewerEmail := "viewer@example.test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(status int, value any) {
			w.WriteHeader(status)
			if value != nil {
				_ = json.NewEncoder(w).Encode(value)
			}
		}
		auth := r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch body["email"] {
			case "platform@example.test":
				write(http.StatusOK, map[string]any{"tokens": map[string]string{"access_token": "platform-token", "refresh_token": "platform-refresh"}})
			case ownerEmail:
				write(http.StatusOK, map[string]any{"user": map[string]string{"id": ownerID, "email": ownerEmail}, "tokens": map[string]string{"access_token": "owner-token", "refresh_token": "owner-refresh"}})
			case viewerEmail:
				write(http.StatusOK, map[string]any{"user": map[string]string{"id": viewerID, "email": viewerEmail}, "tokens": map[string]string{"access_token": "viewer-token", "refresh_token": "viewer-refresh"}})
			default:
				write(http.StatusUnauthorized, map[string]string{"error": "unknown user"})
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/brand-clouds" && auth == "Bearer platform-token":
			write(http.StatusOK, map[string]any{"brand_clouds": []any{map[string]any{"id": "original-cloud", "name": brandName, "tenant_slug": "original"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/brand-clouds/original-cloud/users" && auth == "Bearer platform-token":
			write(http.StatusCreated, map[string]any{"action": "created", "user": map[string]string{"id": viewerID}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/developer/brand-clouds" && auth == "Bearer owner-token":
			write(http.StatusCreated, map[string]any{"brand_cloud": map[string]any{"id": cloudID, "owner_user_id": ownerID, "my_role": "owner"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds" && auth == "Bearer owner-token" && r.URL.Query().Get("view") == "owned":
			write(http.StatusOK, map[string]any{"brand_clouds": []any{map[string]any{"id": cloudID, "my_role": "owner"}}, "owned_count": 2, "owned_limit": 8})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && auth == "Bearer owner-token":
			write(http.StatusOK, map[string]any{"brand_cloud": map[string]string{"id": cloudID}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/members/invitations") && auth == "Bearer owner-token":
			write(http.StatusAccepted, map[string]any{"invitation": map[string]any{"status": "pending", "target_user_id": viewerID, "access_scope": map[string]string{"kind": "all_products"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/developer/brand-cloud-member-invitations/accept" && auth == "Bearer viewer-token":
			write(http.StatusOK, map[string]any{"member": map[string]string{"role": "viewer"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds" && auth == "Bearer viewer-token" && r.URL.Query().Get("view") == "shared":
			write(http.StatusOK, map[string]any{"brand_clouds": []any{map[string]any{"id": cloudID, "my_role": "viewer"}}})
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && auth == "Bearer viewer-token":
			write(http.StatusForbidden, map[string]string{"error": "forbidden"})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/members/"+viewerID) && auth == "Bearer owner-token":
			write(http.StatusNoContent, nil)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && auth == "Bearer viewer-token":
			write(http.StatusNotFound, map[string]string{"error": "not found"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deletion-preflight") && auth == "Bearer owner-token":
			write(http.StatusOK, map[string]any{"eligible": true, "blockers": []any{}})
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/developer/brand-clouds/"+cloudID && auth == "Bearer owner-token":
			write(http.StatusAccepted, map[string]any{"operation": map[string]string{"id": "44444444-4444-4444-8444-444444444444"}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/operations/") && auth == "Bearer owner-token":
			write(http.StatusOK, map[string]any{"operation": map[string]string{"state": "succeeded"}})
		default:
			write(http.StatusNotFound, map[string]string{"error": fmt.Sprintf("unexpected %s %s", r.Method, r.URL.String())})
		}
	}))
	defer server.Close()

	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_PROVIDER=test\n")
	store, err := openTestDataStore(envRoot, brandName)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceUsers(brandName, "original-cloud", "original", "owner", []map[string]any{{"id": ownerID, "email": ownerEmail, "password": "owner-password", "role": "owner"}}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "platform@example.test")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "platform-password")
	originalEmailResolver := multicloudViewerEmailResolver
	originalWaiterFactory := multicloudInvitationWaiterFactory
	multicloudViewerEmailResolver = func(accountManagerContext, string) (string, error) { return viewerEmail, nil }
	multicloudInvitationWaiterFactory = func(string, accountManagerContext, string) (func() (string, error), error) {
		return func() (string, error) { return "email-token", nil }, nil
	}
	t.Cleanup(func() {
		multicloudViewerEmailResolver = originalEmailResolver
		multicloudInvitationWaiterFactory = originalWaiterFactory
	})
	output := filepath.Join(workspace, "evidence")
	if err := runTestMulticloud([]string{
		"--profile", "staging-live", "--workspace", workspace, "--env-root", envRoot,
		"--brandname", brandName, "--run-id", "live-cli-001", "--output-dir", output,
		"--run", "--confirm", multicloudLiveConfirmation,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(output, "evidence.json"))
	if err != nil || !bytes.Contains(raw, []byte(`"status": "PASS"`)) {
		t.Fatalf("qualification evidence error=%v body=%s", err, raw)
	}
}

func TestMulticloudMailboxHelpersResolveAliasAndInvitationToken(t *testing.T) {
	configRoot := filepath.Join(t.TempDir(), "config")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	operatorEnv := filepath.Join(t.TempDir(), "operator.env")
	writeTestFile(t, operatorEnv, strings.Join([]string{
		"IMAP_SERVER=imap.example.test",
		"IMAP_EMAIL_ADDR=mailbox@example.test",
		"IMAP_EMAIL_PASSWORD=password",
		"IMAP_EMAIL_PORT=993",
		"IMAP_EMAIL_SECURITY=tls",
		"IMAP_EMAIL_FOLDER=INBOX",
	}, "\n")+"\n")
	if err := importEnvFileToStore(store, operatorEnv); err != nil {
		t.Fatal(err)
	}
	ctx := accountManagerContext{EnvRoot: filepath.Join(t.TempDir(), "runtime"), StackValues: map[string]string{"CLOUD_ENV_NAME": "staging"}}
	writeTestFile(t, filepath.Join(ctx.EnvRoot, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_STACK_NAME=video-cloud-staging\nCLOUD_DNS_ROOT_DOMAIN=realtekconnect.com\nCLOUD_ADMIN_DOMAIN=admin.video-cloud-staging.realtekconnect.com\n")
	email, err := multicloudRunScopedViewerEmail(ctx, "mail-test-001")
	if err != nil || email != "mailbox+multicloud-mail-test-001@example.test" {
		t.Fatalf("viewer alias=%q error=%v", email, err)
	}

	workspace := t.TempDir()
	helper := filepath.Join(workspace, "repos", "rtk_account_manager", "scripts", "email_signup_imap.py")
	writeTestFile(t, helper, `#!/usr/bin/env python3
import json
import sys
if sys.argv[1] == "snapshot":
    print(json.dumps({"uid_next": 17}))
else:
    print(json.dumps({"url": "https://admin.example.test/brand-cloud-member-invitation/accept?token=mail-token"}))
`)
	waiter, err := multicloudInvitationTokenWaiter(workspace, ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	token, err := waiter()
	if err != nil || token != "mail-token" {
		t.Fatalf("invitation token=%q error=%v", token, err)
	}
	if _, err := multicloudRunScopedViewerEmail(accountManagerContext{StackValues: map[string]string{"CLOUD_ENV_NAME": "INVALID"}}, "run"); err == nil {
		t.Fatal("invalid secret environment was accepted")
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", filepath.Join(t.TempDir(), "missing"))
	if _, err := multicloudRunScopedViewerEmail(ctx, "run"); err == nil || !strings.Contains(err.Error(), "read canonical operator settings") {
		t.Fatalf("missing operator store error = %v", err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	if err := store.write(filepath.Join("operator", "env", "IMAP_EMAIL_ADDR"), []byte("mailbox+existing@example.test\n"), true); err != nil {
		t.Fatal(err)
	}
	if got, err := multicloudRunScopedViewerEmail(ctx, "run"); err != nil || got != "mailbox+multicloud-run@example.test" {
		t.Fatalf("existing plus alias result = %q, error = %v", got, err)
	}
}

func TestMulticloudSmallHelpersFailClosed(t *testing.T) {
	for name, in := range map[string]multicloudLiveScenarioInput{
		"missing identity": {runID: "run", ownerToken: "owner", ownerUserID: "owner", viewerToken: "viewer", viewerID: "viewer"},
		"missing waiter":   {runID: "run", ownerToken: "owner", ownerUserID: "owner", viewerToken: "viewer", viewerID: "viewer", viewerEmail: "viewer@example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runMulticloudLiveScenario(context.Background(), multicloudLiveHTTPClient{}, in)
			if err == nil {
				t.Fatal("invalid scenario input was accepted")
			}
		})
	}
	if cloudPageContains(map[string]any{"brand_clouds": []any{map[string]any{"id": "other", "my_role": "owner"}}}, "cloud", "owner") {
		t.Fatal("cloudPageContains matched the wrong cloud")
	}
	if got := apiStatusError("create", http.StatusConflict, nil).Error(); got != "create: HTTP 409" {
		t.Fatalf("status error = %q", got)
	}
	if got := apiStatusError("create", 0, context.Canceled).Error(); !strings.Contains(got, "context canceled") {
		t.Fatalf("wrapped status error = %q", got)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()
	var output map[string]any
	status, err := (multicloudLiveHTTPClient{baseURL: server.URL, client: server.Client()}).json(context.Background(), http.MethodGet, "/", "token", nil, "key", &output)
	if status != http.StatusOK || err == nil || !strings.Contains(err.Error(), "decode HTTP 200 response") {
		t.Fatalf("invalid JSON status=%d error=%v", status, err)
	}
}

func TestResolveIMAPConnectHost(t *testing.T) {
	lookup := func(host string) ([]string, error) {
		switch host {
		case "imap.example.test":
			return nil, errors.New("not resolvable")
		case "sm.realtekconnect.com":
			return []string{"192.0.2.1"}, nil
		default:
			return nil, errors.New("unexpected host")
		}
	}
	if got, err := resolveIMAPConnectHost("127.0.0.1", "imap.example.test", lookup); err != nil || got != "127.0.0.1" {
		t.Fatalf("configured host = %q, error = %v", got, err)
	}
	if got, err := resolveIMAPConnectHost("", "imap.example.test", lookup); err != nil || got != "sm.realtekconnect.com" {
		t.Fatalf("fallback host = %q, error = %v", got, err)
	}
	if got, err := resolveIMAPConnectHost("", "sm.realtekconnect.com", lookup); err != nil || got != "" {
		t.Fatalf("direct host = %q, error = %v", got, err)
	}
	if _, err := resolveIMAPConnectHost("", "imap.example.test", func(string) ([]string, error) {
		return nil, errors.New("not resolvable")
	}); err == nil {
		t.Fatal("missing primary and fallback DNS was accepted")
	}
}

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
