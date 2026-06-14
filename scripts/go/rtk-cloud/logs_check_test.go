package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsCheckIsRetiredForK8sStaging(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "env"))
	writeFile(t, filepath.Join(root, "env", "stack.env"), "CLOUD_STACK_NAME=video-cloud-staging\n")
	outDir := filepath.Join(root, "artifacts", "logs-check")
	err := runLogsCheck([]string{"--env-root", root, "--out-dir", outDir, "--json"})
	if err == nil || !strings.Contains(err.Error(), logsCheckRetiredMessage) {
		t.Fatalf("expected retired error, got %v", err)
	}
	report := readFile(t, filepath.Join(outDir, "report.md"))
	if !strings.Contains(report, logsCheckRetiredMessage) || !strings.Contains(report, "status: retired") {
		t.Fatalf("retired report missing expected content:\n%s", report)
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
