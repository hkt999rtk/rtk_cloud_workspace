package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeploymentTimingStaysInLogForPlainAndTerminalOutput(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "terminal"}[terminal], func(t *testing.T) {
			var screen, log bytes.Buffer
			p := &environmentProgress{out: &screen, log: &log, stage: "deployment.execution", started: time.Now().Add(-32 * time.Second), resources: -1, total: -1, terminal: terminal, columns: 120, color: terminal}
			ctx := context.WithValue(context.Background(), environmentProgressKey{}, p)
			end := environmentDeploymentOperation(ctx, "read", "Service/ns/api")
			p.write(false)
			end()
			environmentDeploymentProgressResult(ctx, "deployment.execution", "PASS")
			visible := visibleTerminalProgress(screen.String())
			if strings.Contains(visible, "elapsed=") || strings.Contains(visible, "0m") || strings.Contains(visible, " (0s)") || strings.Contains(visible, "[OK]") {
				t.Fatal("timing diagnostics or success rows leaked into the user display", screen.String())
			}
			if !strings.Contains(log.String(), "[OK] deployment.execution elapsed=32s") || !strings.Contains(log.String(), "[RUNNING] deployment.execution elapsed=32s") || strings.ContainsAny(log.String(), "\x1b\r") {
				t.Fatal("timing was lost or terminal controls entered the log", log.String())
			}
			lines := strings.Split(log.String(), "\n")
			if len(lines) < 3 || !strings.HasPrefix(lines[0], "[RUNNING] deployment.execution elapsed=32s active=1 | Read current resource Service/ns/api (") || lines[1] != "  Verify deployment can execute" || strings.Contains(log.String(), "task=") {
				t.Fatal("private log lost header timing or the plain indented task", log.String())
			}
		})
	}
}

func TestUnifiedDeploymentCreatesPrivateSanitizedLogForEachAttempt(t *testing.T) {
	for _, action := range []string{"preflight", "upgrade"} {
		t.Run(action, func(t *testing.T) {
			c, deps, _ := unifiedCLIProfile(t, "dev")
			var screen bytes.Buffer
			args := []string{"--environment", "dev"}
			if action == "upgrade" {
				args = append(args, "--confirm", "video-cloud-dev", "--plan-sha256", c.Plan.SHA256())
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), action, args, &screen, &screen, deps); err != nil {
					t.Fatal(err, screen.String())
				}
			}
			store, _ := newSecretStore("", "dev")
			directory := filepath.Join(store.Root, "deployment", "logs")
			files, err := filepath.Glob(filepath.Join(directory, action+"-*.log"))
			if err != nil || len(files) != 2 {
				t.Fatal("attempts overwrote or lost their logs", files, err)
			}
			for _, path := range files {
				info, err := os.Stat(path)
				raw, readErr := os.ReadFile(path)
				if err != nil || readErr != nil || info.Mode().Perm() != 0600 || !bytes.Contains(raw, []byte("elapsed=")) || !bytes.Contains(raw, []byte(`"decision":"GO"`)) || bytes.Contains(raw, []byte("never-public")) || bytes.Contains(raw, []byte("task=")) || bytes.ContainsAny(raw, "\x1b\r") {
					t.Fatal("log lost timing, final report, privacy or permissions", path, err, readErr)
				}
			}
			info, _ := os.Stat(directory)
			if info.Mode().Perm() != 0700 || strings.Contains(screen.String(), "elapsed=") || strings.Contains(screen.String(), "[OK]") || !strings.Contains(screen.String(), "GO") {
				t.Fatal("private log permissions or concise display contract failed")
			}
		})
	}
}

func TestDeploymentLogRejectsSymlinkAndReportsWriteFailure(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	directory := filepath.Join(store.Root, "deployment")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(directory, "logs")); err != nil {
		t.Fatal(err)
	}
	if log, err := openEnvironmentProgressLog("dev", "upgrade"); err == nil || log != nil {
		t.Fatal("deployment log followed a symlink")
	}
	if err := os.Remove(filepath.Join(directory, "logs")); err != nil {
		t.Fatal(err)
	}
	log, err := openEnvironmentProgressLog("dev", "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	_ = log.file.Close()
	if _, err := log.Write([]byte("synthetic timing row")); err == nil || log.Close() == nil {
		t.Fatal("deployment log silently discarded a write failure")
	}
}

func TestDeploymentHealthTimingUsesSameLog(t *testing.T) {
	var screen, log bytes.Buffer
	r := newDeploymentCheckReporter(&screen)
	r.log = &log
	r.emit(deploymentCredentialCheck{ID: "health.ready", Status: "PASS", Passed: true, DurationMS: 32000, Attempts: 2})
	if screen.Len() != 0 || !strings.Contains(log.String(), "[OK] health.ready duration=32000ms") || r.results["health.ready"].DurationMS != 32000 || r.results["health.ready"].Status != "PASS" {
		t.Fatal("health display or JSON evidence lost the timing contract", screen.String(), log.String())
	}
	r.emit(deploymentCredentialCheck{ID: "health.failed", Status: "FAIL", DurationMS: 1100})
	if !strings.Contains(screen.String(), "[FAIL] health.failed") || strings.Contains(screen.String(), "duration=") || !strings.Contains(log.String(), "[FAIL] health.failed duration=1100ms") || r.results["health.failed"].Status != "FAIL" {
		t.Fatal("health failure disappeared from the display or lost log evidence", screen.String(), log.String())
	}
}
