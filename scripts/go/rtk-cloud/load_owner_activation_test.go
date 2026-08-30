package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunActivateLoadOwnerCompletesFormalEmailFlowAndStoresCredentials(t *testing.T) {
	const (
		brandName   = "RTK-LOAD-CANARY-run-20260726-B01"
		tenantSlug  = "load-canary-b01"
		ownerEmail  = "imap-test01+load-run-20260726-b01@realtekconnect.com"
		displayName = "RTK Load CANARY run-20260726 Brand 01 Owner"
	)
	var createdPayload, ownerLoginPayload map[string]any
	creationStatus := http.StatusCreated
	creationCalls := 0
	pendingOwner := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			var loginPayload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&loginPayload); err != nil {
				t.Errorf("decode login payload: %v", err)
			}
			if loginPayload["email"] != ownerEmail {
				fmt.Fprint(w, `{"tokens":{"access_token":"platform-access","refresh_token":"platform-refresh"}}`)
				return
			}
			ownerLoginPayload = loginPayload
			fmt.Fprintf(w, `{
				"user":{"id":"owner-id","email":%q},
				"tokens":{"access_token":"owner-access","refresh_token":"owner-refresh"},
				"app_certificate":{"status":"issued","subject":"app-user:owner-id","certificate_pem":"certificate","fingerprint_sha256":"abc123"}
			}`, ownerEmail)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/brand-clouds":
			fmt.Fprintf(w, `{"brand_clouds":[{"id":"brand-id","name":%q,"tenant_slug":%q}]}`, brandName, tenantSlug)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/brand-clouds/brand-id/users":
			creationCalls++
			if err := json.NewDecoder(r.Body).Decode(&createdPayload); err != nil {
				t.Errorf("decode owner payload: %v", err)
			}
			w.WriteHeader(creationStatus)
			fmt.Fprintf(w, `{"action":"pending_activation","user":{"id":"owner-id","email":%q,"signup_pending_verification":%t,"email_verified":%t}}`, ownerEmail, pendingOwner, !pendingOwner)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=test
CLOUD_REGION=us-sea
CLOUD_STACK_NAME=video-cloud-staging
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-staging.realtekconnect.com
CERTIFICATE_APP_CSR_KEY_ALGORITHMS=ed25519,p256
`)
	helper := filepath.Join(workspace, "repos", "rtk_account_manager", "scripts", "email_signup_imap.py")
	writeTestFile(t, helper, `#!/usr/bin/env python3
import json
import sys
if sys.argv[1] == "snapshot":
    print(json.dumps({"uid_next": 42}))
else:
    print(json.dumps({
        "uid": 42,
        "url": "https://admin.video-cloud-staging.realtekconnect.com/signup/verify?token=opaque"
    }))
`)
	webDir := filepath.Join(workspace, "repos", "rtk_cloud_admin", "web")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	npm := filepath.Join(binDir, "npm")
	writeTestFile(t, npm, `#!/bin/sh
set -eu
cat > "$LOAD_OWNER_EVIDENCE_PATH" <<EOF
{"schema":"rtk.load-owner-activation.evidence.v1","status":"PASS","run_id":"$LOAD_OWNER_RUN_ID","brand_name":"$LOAD_OWNER_BRAND_NAME","recipient_alias":"$LOAD_OWNER_EMAIL","tenant_slug":"$LOAD_OWNER_TENANT_SLUG","imap_uid":$LOAD_OWNER_IMAP_UID}
EOF
`)
	if err := os.Chmod(npm, 0o755); err != nil {
		t.Fatal(err)
	}
	operatorEnv := filepath.Join(workspace, "operator.env")
	writeTestFile(t, operatorEnv, `IMAP_SERVER=imap.example.test
IMAP_EMAIL_ADDR=imap-test01@realtekconnect.com
IMAP_EMAIL_PASSWORD=imap-password
IMAP_EMAIL_PORT=993
IMAP_EMAIL_SECURITY=tls
IMAP_EMAIL_FOLDER=INBOX
`)
	evidencePath := filepath.Join(workspace, "evidence", "owner.json")

	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "admin@example.test")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "admin-password")
	t.Setenv("IMAP_CONNECT_HOST", "127.0.0.1")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := []string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", brandName,
		"--email", ownerEmail,
		"--display-name", displayName,
		"--run-id", "run-20260726",
		"--operator-env-file", operatorEnv,
		"--evidence-path", evidencePath,
	}
	if err := runActivateLoadOwner(args); err != nil {
		t.Fatal(err)
	}

	if createdPayload["activation_mode"] != "email" ||
		createdPayload["role"] != "owner" ||
		createdPayload["email"] != ownerEmail {
		t.Fatalf("pending owner payload = %+v", createdPayload)
	}
	csrPEM, _ := ownerLoginPayload["app_csr_pem"].(string)
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		t.Fatal("owner login did not include a valid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if csr.Subject.CommonName != "app-user:owner-id" {
		t.Fatalf("owner CSR subject = %q", csr.Subject.CommonName)
	}
	if _, err := os.Stat(evidencePath); err != nil {
		t.Fatalf("browser evidence missing: %v", err)
	}
	store, err := openTestDataStore(envRoot, brandName)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var storedUserID, storedEmail, password, role string
	if err := store.DB.QueryRow(
		`SELECT user_id, email, password, role FROM users WHERE brandname = ?`, brandName,
	).Scan(&storedUserID, &storedEmail, &password, &role); err != nil {
		t.Fatal(err)
	}
	if storedUserID != "owner-id" || !strings.EqualFold(storedEmail, ownerEmail) || password == "" || role != "owner" {
		t.Fatalf("stored owner user_id=%q email=%q password_set=%v role=%q", storedUserID, storedEmail, password != "", role)
	}
	if err := os.Remove(evidencePath); err != nil {
		t.Fatal(err)
	}
	creationStatus = http.StatusOK
	if err := runActivateLoadOwner(append(args, "--resume")); err != nil {
		t.Fatalf("retry matching pending owner: %v", err)
	}
	if creationCalls != 2 {
		t.Fatalf("owner creation calls = %d, want 2", creationCalls)
	}
	if err := os.Remove(evidencePath); err != nil {
		t.Fatal(err)
	}
	pendingOwner = false
	if err := runActivateLoadOwner(append(args, "--resume")); err == nil || !strings.Contains(err.Error(), "matching pending, unverified owner") {
		t.Fatalf("verified owner retry error = %v", err)
	}
}

func TestLoadOwnerAdminBaseURLDerivesAndGuardsTheRunStack(t *testing.T) {
	base := map[string]string{
		"CLOUD_ENV_NAME":        "staging",
		"CLOUD_STACK_NAME":      "video-cloud-staging",
		"CLOUD_DNS_ROOT_DOMAIN": "realtekconnect.com",
	}
	got, err := loadOwnerAdminBaseURL(base)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://admin.video-cloud-staging.realtekconnect.com" {
		t.Fatalf("derived admin URL = %q", got)
	}

	validExplicit := make(map[string]string, len(base)+1)
	for key, value := range base {
		validExplicit[key] = value
	}
	validExplicit["CLOUD_ADMIN_DOMAIN"] = "admin.video-cloud-staging.realtekconnect.com"
	if _, err := loadOwnerAdminBaseURL(validExplicit); err != nil {
		t.Fatalf("valid explicit domain rejected: %v", err)
	}

	for name, mutate := range map[string]func(map[string]string){
		"production environment": func(env map[string]string) { env["CLOUD_ENV_NAME"] = "production" },
		"production stack":       func(env map[string]string) { env["CLOUD_STACK_NAME"] = "video-cloud" },
		"localhost":              func(env map[string]string) { env["CLOUD_ADMIN_DOMAIN"] = "localhost" },
		"wrong stack":            func(env map[string]string) { env["CLOUD_ADMIN_DOMAIN"] = "admin.other-staging.realtekconnect.com" },
		"URL instead of host": func(env map[string]string) {
			env["CLOUD_ADMIN_DOMAIN"] = "http://admin.video-cloud-staging.realtekconnect.com"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := make(map[string]string, len(base))
			for key, value := range base {
				candidate[key] = value
			}
			mutate(candidate)
			if got, err := loadOwnerAdminBaseURL(candidate); err == nil {
				t.Fatalf("unsafe runtime accepted: %q", got)
			}
		})
	}
}

func TestRunActivateLoadOwnerRejectsInvalidArgumentsAndMissingIMAP(t *testing.T) {
	if err := runActivateLoadOwner([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runActivateLoadOwner(nil); err == nil {
		t.Fatal("missing required arguments accepted")
	}
	if err := runActivateLoadOwner([]string{
		"--brandname", "brand",
		"--email", "owner@example.test",
		"--display-name", "Owner",
		"--run-id", "BAD",
	}); err == nil {
		t.Fatal("unsafe run ID accepted")
	}

	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "runtime")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=test
CLOUD_REGION=us-sea
CLOUD_STACK_NAME=video-cloud-staging
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-staging.realtekconnect.com
`)
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", "https://account.example.test")
	operatorEnv := filepath.Join(workspace, "operator.env")
	writeTestFile(t, operatorEnv, "IMAP_SERVER=imap.example.test\n")
	err := runActivateLoadOwner([]string{
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", "RTK-LOAD-CANARY-run-20260726-B01",
		"--email", "owner@example.test",
		"--display-name", "Owner",
		"--run-id", "run-20260726",
		"--operator-env-file", operatorEnv,
	})
	if err == nil || !strings.Contains(err.Error(), "missing operator IMAP setting") {
		t.Fatalf("missing IMAP settings error = %v", err)
	}
}

func TestReuseVerifiedLoadOwnerRequiresMatchingEvidenceCredentialAndLogin(t *testing.T) {
	const (
		brandName  = "RTK-LOAD-CANARY-run-20260726-B01"
		tenantSlug = "load-canary-b01"
		email      = "imap-test01+load-run-20260726-b01@realtekconnect.com"
		runID      = "run-20260726"
	)
	envRoot := t.TempDir()
	evidencePath := filepath.Join(envRoot, "evidence", "owner.json")
	ctx := accountManagerContext{EnvRoot: envRoot}

	reused, err := reuseVerifiedLoadOwner(ctx, brandName, email, runID, evidencePath)
	if err != nil || reused {
		t.Fatalf("missing evidence reused=%v err=%v", reused, err)
	}
	if err := os.MkdirAll(filepath.Dir(evidencePath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, evidencePath, `{"status":"PASS","run_id":"foreign-run","brand_name":"foreign","recipient_alias":"other@example.test","tenant_slug":"other"}`)
	if _, err := reuseVerifiedLoadOwner(ctx, brandName, email, runID, evidencePath); err == nil {
		t.Fatal("foreign resume evidence accepted")
	}
	writeTestFile(t, evidencePath, fmt.Sprintf(
		`{"status":"PASS","run_id":%q,"brand_name":%q,"recipient_alias":%q,"tenant_slug":%q}`,
		runID, brandName, email, tenantSlug,
	))
	if _, err := reuseVerifiedLoadOwner(ctx, brandName, email, runID, evidencePath); err == nil ||
		!strings.Contains(err.Error(), "credential is absent") {
		t.Fatalf("missing owner credential error = %v", err)
	}

	store, err := openTestDataStore(envRoot, brandName)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceUsers(
		brandName, "brand-id", tenantSlug, "owner",
		[]map[string]any{{
			"id": "owner-id", "email": email, "display_name": "Owner",
			"role": "owner", "password": "verified-password",
		}},
	); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/login" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		w.Header().Set("content-type", "application/json")
		fmt.Fprintf(w, `{
			"user":{"id":"owner-id","email":%q},
			"tokens":{"access_token":"owner-access","refresh_token":"owner-refresh"},
			"app_certificate":{"status":"issued"}
		}`, email)
	}))
	defer server.Close()
	ctx.BaseURL = server.URL
	reused, err = reuseVerifiedLoadOwner(ctx, brandName, email, runID, evidencePath)
	if err != nil || !reused {
		t.Fatalf("verified matching owner reused=%v err=%v", reused, err)
	}
}
