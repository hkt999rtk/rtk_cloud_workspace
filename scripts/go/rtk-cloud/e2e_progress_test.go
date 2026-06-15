package main

import (
	"os"
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
