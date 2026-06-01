package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildLogCheckTargetsFromLatestProvisionArtifact(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "env"))
	mkdirAll(t, filepath.Join(root, "state"))
	mkdirAll(t, filepath.Join(root, "artifacts", "provision-20260101T000000Z"))
	mkdirAll(t, filepath.Join(root, "artifacts", "provision-20260102T000000Z"))
	writeFile(t, filepath.Join(root, "env", "stack.env"), "VIDEO_CLOUD_DOMAIN=vc.example.test\nACCOUNT_MANAGER_DOMAIN=am.example.test\nCLOUD_ADMIN_DOMAIN=admin.example.test\n")
	writeFile(t, filepath.Join(root, "state", "account-manager-staging.env"), "ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=198.51.100.50\n")
	writeFile(t, filepath.Join(root, "state", "cloud-admin-staging.env"), "ADMIN_LINODE_PUBLIC_IPV4=198.51.100.60\n")
	writeFile(t, filepath.Join(root, "artifacts", "provision-20260101T000000Z", "deployment-targets.json"), `{"targets":{"edge":{"host":"old","user":"root"}}}`)
	writeFile(t, filepath.Join(root, "artifacts", "provision-20260102T000000Z", "deployment-targets.json"), `{
		"targets": {
			"edge": {"host": "203.0.113.5", "user": "root"},
			"api": {"host": "10.42.1.10", "user": "root", "proxy_jump": "root@203.0.113.5"},
			"infra": {"host": "10.42.1.30", "user": "root", "proxy_jump": "root@203.0.113.5"},
			"mqtt": {"host": "10.42.1.40", "user": "root", "proxy_jump": "root@203.0.113.5"},
			"coturn": {"host": "203.0.113.40", "user": "root"}
		}
	}`)

	cfg, err := newLogCheckConfig(root, "", "15m", 300, filepath.Join(root, "id_ed25519"), false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Domains.VideoCloud != "vc.example.test" || cfg.Domains.AccountManager != "am.example.test" || cfg.Domains.CloudAdmin != "admin.example.test" {
		t.Fatalf("domains not loaded: %+v", cfg.Domains)
	}
	if cfg.Targets["edge"].Host != "203.0.113.5" {
		t.Fatalf("did not use latest provision target: %+v", cfg.Targets["edge"])
	}
	if cfg.Targets["api"].ProxyJump != "root@203.0.113.5" {
		t.Fatalf("api ProxyJump missing: %+v", cfg.Targets["api"])
	}
	if cfg.Targets["account-manager"].Host != "198.51.100.50" {
		t.Fatalf("account-manager target missing: %+v", cfg.Targets["account-manager"])
	}
	if cfg.Targets["cloud-admin"].Host != "198.51.100.60" {
		t.Fatalf("cloud-admin target missing: %+v", cfg.Targets["cloud-admin"])
	}
}

func TestLogSecretScanner(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantHit bool
	}{
		{name: "plain bearer token", body: "authorization: Bearer abc.def.ghi", wantHit: true},
		{name: "private key", body: "-----BEGIN PRIVATE KEY-----\nabc", wantHit: true},
		{name: "redacted token", body: "authorization: Bearer <redacted>", wantHit: false},
		{name: "empty secret", body: "SECRET=", wantHit: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := scanLogSecrets(tc.body)
			if gotHit := len(hits) > 0; gotHit != tc.wantHit {
				t.Fatalf("scanLogSecrets hit=%v want %v hits=%v", gotHit, tc.wantHit, hits)
			}
		})
	}
}

func TestRenderLogsCheckReportIncludesFailuresAndArtifacts(t *testing.T) {
	result := logsCheckResult{
		GeneratedAt: "2026-06-01T00:00:00Z",
		Status:      "failed",
		ArtifactDir: "/tmp/logs-check",
		Checks: []logsCheckItem{
			{Name: "video-cloud-healthz", Target: "operator", Status: "pass", Detail: "HTTP 200"},
			{Name: "api-journal", Target: "api", Status: "fail", Detail: "secret pattern matched", Artifact: "raw/api-journal.log"},
		},
	}
	report := renderLogsCheckReport(result)
	for _, want := range []string{"# Logs Check Report", "status: failed", "video-cloud-healthz", "api-journal", "raw/api-journal.log"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
