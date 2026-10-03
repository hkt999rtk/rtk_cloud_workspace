package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDeploymentCredentialScriptForwardsOptionalPKIFlagsOnBash(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(`#!/bin/sh
printf 'go:%s\n' "$*" >> "$CALLS"
test "$1" = build && test "$2" = -o || exit 99
cat > "$3" <<'CHECKER'
#!/bin/sh
printf 'check:%s\n' "$*" >> "$CALLS"
for arg do printf 'arg:%s\n' "$arg" >> "$ARGS"; done
if [ -n "${SIGNAL_READY:-}" ]; then
  trap 'printf cleaned > "$SIGNAL_CLEANED"; exit 1' TERM
  printf ready > "$SIGNAL_READY"
  while :; do sleep 0.05; done
fi
exit "${FAKE_CHECK_EXIT:-0}"
CHECKER
chmod +x "$3"
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS", calls)
	arguments := filepath.Join(bin, "args")
	t.Setenv("ARGS", arguments)
	for _, tc := range []struct {
		name string
		args []string
		want string
		exit int
	}{
		{"without optional flags", nil, "--environment staging --read-only --checks linode", 0},
		{"with Product PKI flag", []string{"--require-product-pki"}, "--require-product-pki", 0},
		{"with migration flag", []string{"--require-pki-migration"}, "--require-pki-migration", 0},
		{"with fast/report", []string{"--fast", "--report", "result with spaces.json"}, "--fast --report result with spaces.json", 0},
		{"preserves argument exit code", []string{"--unknown"}, "--unknown", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(calls, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_CHECK_EXIT", string(rune('0'+tc.exit)))
			if err := os.WriteFile(arguments, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{filepath.Join(workspace, "scripts", "check-deployment-credentials.sh"), "--environment", "staging", "--read-only", "--checks", "linode"}, tc.args...)
			output, err := exec.Command("bash", args...).CombinedOutput()
			if tc.exit == 0 && err != nil {
				t.Fatalf("script failed: %v %s", err, output)
			}
			if tc.exit != 0 {
				failure, ok := err.(*exec.ExitError)
				if !ok || failure.ExitCode() != tc.exit {
					t.Fatalf("exit error=%v want=%d", err, tc.exit)
				}
			}
			payload, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[0], "go:build -o ") || !strings.Contains(lines[1], tc.want) || !strings.Contains(lines[1], "check:deployment check --workspace") {
				t.Fatalf("unexpected credential script forwarding: %q", lines)
			}
			if tc.name == "with fast/report" {
				data, err := os.ReadFile(arguments)
				if err != nil || !strings.Contains(string(data), "arg:result with spaces.json\n") {
					t.Fatalf("arguments=%s err=%v", data, err)
				}
			}
		})
	}
	t.Run("forwards cancellation and waits for cleanup", func(t *testing.T) {
		ready, cleaned := filepath.Join(bin, "ready"), filepath.Join(bin, "cleaned")
		t.Setenv("SIGNAL_READY", ready)
		t.Setenv("SIGNAL_CLEANED", cleaned)
		cmd := exec.Command("bash", filepath.Join(workspace, "scripts", "check-deployment-credentials.sh"), "--environment", "staging")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("checker did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			failure, ok := err.(*exec.ExitError)
			if !ok || failure.ExitCode() != 1 {
				t.Fatalf("cancellation exit=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("wrapper did not finish cancellation")
		}
		if _, err := os.Stat(cleaned); err != nil {
			t.Fatal("wrapper did not wait for checker cleanup")
		}
	})
}
