package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePKIMigrationDatabaseURL(t *testing.T) {
	password := "p@ss:/word"
	good := (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("postgres", password),
		Host:     "postgresql.video-cloud-staging-platform.svc.cluster.local:5432",
		Path:     "/video_cloud",
		RawQuery: "sslmode=disable",
	}).String()
	if err := validatePKIMigrationDatabaseURL(good, "staging", password); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"other environment": strings.Replace(good, "staging-platform", "dev-platform", 1),
		"runtime role":      strings.Replace(good, "postgres:", "rtk_pki_controller_staging:", 1),
		"other database":    strings.Replace(good, "/video_cloud?", "/postgres?", 1),
		"other password":    strings.Replace(good, "p%40ss", "wrong", 1),
		"unsafe TLS mode":   strings.Replace(good, "sslmode=disable", "sslmode=prefer", 1),
		"extra option":      good + "&application_name=other",
		"missing password":  "postgres://postgres@postgresql.video-cloud-staging-platform.svc.cluster.local:5432/video_cloud?sslmode=disable",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePKIMigrationDatabaseURL(raw, "staging", password); err == nil {
				t.Fatal("accepted wrong migration Secret")
			}
		})
	}
}

func TestVerifyPKIMigrationDatabaseSecret(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := verifyPKIMigrationDatabaseSecret(store); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("missing canonical credential: %v", err)
	}
	if err := store.write("runtime/postgres", []byte("migration-secret\n"), true); err != nil {
		t.Fatal(err)
	}
	if err := verifyPKIMigrationDatabaseSecret(store); err == nil || !strings.Contains(err.Error(), "kubeconfig") {
		t.Fatalf("missing kubeconfig: %v", err)
	}
	if err := store.write("kube/kubeconfig.yaml", []byte("apiVersion: v1\n"), true); err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "postgres", User: url.UserPassword("postgres", "migration-secret"),
		Host: "postgresql.video-cloud-staging-platform.svc.cluster.local:5432",
		Path: "/video_cloud", RawQuery: "sslmode=disable"}
	good, err := json.Marshal(map[string]any{"data": map[string]string{
		"url": base64.StdEncoding.EncodeToString([]byte(u.String()))}})
	if err != nil {
		t.Fatal(err)
	}
	badURL, err := json.Marshal(map[string]any{"data": map[string]string{
		"url": base64.StdEncoding.EncodeToString([]byte("not-a-url"))}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, want string
	}{
		{"unreadable", "exit 1", "requires Kubernetes Secret"},
		{"invalid metadata", "printf '%s' 'not-json'", "invalid metadata"},
		{"missing key", "printf '%s' '{\"data\":{}}'", "no usable url"},
		{"wrong URL", fmt.Sprintf("printf '%%s' '%s'", badURL), "URL is invalid"},
		{"matching", fmt.Sprintf("printf '%%s' '%s'", good), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kubectl := filepath.Join(t.TempDir(), "kubectl")
			if err := os.WriteFile(kubectl, []byte("#!/bin/sh\n"+tc.body+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
			err := verifyPKIMigrationDatabaseSecret(store)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("migration Secret check = %v, want %q", err, tc.want)
			}
		})
	}
	if err := runSecrets([]string{"plan", "--environment", "staging", "--config-root", store.ConfigRoot,
		"--workspace", t.TempDir(), "--require-pki-migration"}); err == nil || !strings.Contains(err.Error(), "only valid") {
		t.Fatalf("invalid action accepted: %v", err)
	}
	if err := runSecrets([]string{"verify", "--environment", "staging", "--config-root", store.ConfigRoot,
		"--workspace", t.TempDir(), "--require-pki-migration"}); err == nil {
		t.Fatal("verify accepted a missing migration binding")
	}
}
