package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLatestProgressLogLinePrefersMostRecentCountedProgress(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "step.log")
	content := `[cloud-create-users 11:00:29 +000s] bootstrapping app certificate: email=rtk+034@users.local
[cloud-create-users 11:00:35 +000s] user creation progress: done=34/2000 created=34 assigned=0 app_certificates=34
[cloud-create-users 11:00:59 +000s] ensuring brand user: email=rtk+094@users.local role=member
`
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	line := latestProgressLogLine(logPath, time.Minute)
	if want := "user creation progress: done=34/2000"; !strings.Contains(line, want) {
		t.Fatalf("latestProgressLogLine() = %q, want line containing %q", line, want)
	}
}

func TestE2EProgressMetricsParsesCountedProgress(t *testing.T) {
	line := "[cloud-create-users 11:00:35 +000s] user creation progress: done=34/2000 created=34 assigned=0 app_certificates=34"

	got := e2eProgressMetrics(line, time.Minute)
	if want := " done=34/2000 "; !strings.Contains(got, want) {
		t.Fatalf("e2eProgressMetrics() = %q, want containing %q", got, want)
	}
}

func TestRunE2ECommandWithProgressPrintsHeartbeatForUnchangedLatestLine(t *testing.T) {
	t.Setenv("CLOUD_STAGING_E2E_PROGRESS_INTERVAL", "50ms")

	dir := t.TempDir()
	logPath := filepath.Join(dir, "step.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	cmd := exec.Command("sh", "-c", "printf '%s\n' 'Waiting for deployment \"ingress-nginx-controller\" rollout to finish: 2 of 3 updated replicas are available...'; sleep 0.22")
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	stderr := captureStderr(t, func() {
		if err := runE2ECommandWithProgress(cmd, "provision_k8s", logPath, time.Now(), false); err != nil {
			t.Fatalf("runE2ECommandWithProgress() error = %v", err)
		}
	})

	if got := strings.Count(stderr, "[cloud-staging-e2e] progress: provision_k8s"); got < 3 {
		t.Fatalf("expected at least 3 heartbeat progress lines, got %d:\n%s", got, stderr)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	defer func() {
		os.Stderr = old
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
