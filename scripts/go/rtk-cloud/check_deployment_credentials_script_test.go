package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentCredentialScriptForwardsOptionalPKIFlagsOnBash(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS", calls)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"without optional flags", nil, "secrets verify --environment staging"},
		{"with Product PKI flag", []string{"--require-product-pki"}, "secrets verify --environment staging --require-product-pki"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(calls, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{filepath.Join(workspace, "scripts", "check-deployment-credentials.sh"), "--environment", "staging", "--read-only", "--checks", "linode"}, tc.args...)
			output, err := exec.Command("bash", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v %s", err, output)
			}
			payload, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
			if len(lines) != 2 || !strings.Contains(lines[0], tc.want) || !strings.Contains(lines[1], "deployment credentials-check") || strings.Contains(lines[1], "--require-product-pki") {
				t.Fatalf("unexpected credential script forwarding: %q", lines)
			}
		})
	}
}
