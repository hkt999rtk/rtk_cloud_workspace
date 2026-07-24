package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDNSChallengeStateLifecycle(t *testing.T) {
	ctx := dnsAdapterContext{RuntimeRoot: t.TempDir()}
	if values, err := readDNSChallenges(ctx, "godaddy"); err != nil || len(values) != 0 {
		t.Fatalf("initial values = %#v, error = %v", values, err)
	}
	if err := recordDNSChallenge(ctx, "godaddy", "one.example.test", "value-1"); err != nil {
		t.Fatal(err)
	}
	if err := recordDNSChallenge(ctx, "godaddy", "one.example.test", "value-1"); err != nil {
		t.Fatal(err)
	}
	if err := recordDNSChallenge(ctx, "godaddy", "two.example.test", "value-2"); err != nil {
		t.Fatal(err)
	}
	values, err := readDNSChallenges(ctx, "godaddy")
	if err != nil || len(values) != 2 {
		t.Fatalf("recorded values = %#v, error = %v", values, err)
	}
	path := dnsChallengeStatePath(ctx, "godaddy")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("challenge state mode = %v, error = %v", info.Mode().Perm(), err)
	}
	if err := forgetDNSChallenge(ctx, "godaddy", "one.example.test", "value-1"); err != nil {
		t.Fatal(err)
	}
	values, err = readDNSChallenges(ctx, "godaddy")
	if err != nil || len(values) != 1 || values[0].Domain != "two.example.test" {
		t.Fatalf("remaining values = %#v, error = %v", values, err)
	}
	if err := forgetDNSChallenge(ctx, "godaddy", "two.example.test", "value-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty challenge state still exists: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDNSChallenges(ctx, "godaddy"); err == nil {
		t.Fatal("malformed challenge state unexpectedly passed")
	}
}

func TestDNSOperationTimeoutAndTXTConvergence(t *testing.T) {
	if got := dnsOperationTimeout(map[string]string{"DNS_PROPAGATION_TIMEOUT_SECONDS": "40"}); got != 100*time.Second {
		t.Fatalf("timeout = %s", got)
	}
	original := execCommandContext
	t.Cleanup(func() { execCommandContext = original })
	execCommandContext = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("\"validation-token\"\n"), nil
	}
	if err := waitDNSTXT(context.Background(), "_acme.example.test", "validation-token", map[string]string{"DNS_PROPAGATION_INTERVAL_SECONDS": "1"}); err != nil {
		t.Fatal(err)
	}

	execCommandContext = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("\"other\"\n"), errors.New("dig failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitDNSTXT(ctx, "_acme.example.test", "validation-token", map[string]string{"DNS_PROPAGATION_INTERVAL_SECONDS": "1"})
	if err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestDNSHookRejectsInvalidInvocation(t *testing.T) {
	if err := runDNSHook(nil); err == nil {
		t.Fatal("missing action unexpectedly passed")
	}
	if err := runDNSHook([]string{"invalid"}); err == nil {
		t.Fatal("invalid action unexpectedly passed")
	}
	if err := runDNSHook([]string{"present"}); err == nil || !strings.Contains(err.Error(), "--env-root") {
		t.Fatalf("missing env root error = %v", err)
	}
}
