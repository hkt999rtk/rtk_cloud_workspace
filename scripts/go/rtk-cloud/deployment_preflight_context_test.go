package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/route53"
)

func TestDeploymentPreflightCanceledContextDoesNotStartChecks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checks := deploymentPreflightChecks{lookPath: func(string) (string, error) {
		t.Fatal("canceled preflight started tool lookup")
		return "", nil
	}}
	if err := runDeploymentPreflightWithChecksContext(ctx, deploymentConfig{}, "provision", checks, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight error=%v", err)
	}
}

func TestDeploymentPreflightContextKillsStalledProviderDiscovery(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	makeIsolatedTestSecretStore(t, "staging")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LINODE_TOKEN", "test-provider-token")
	t.Setenv("GHCR_PULL_USERNAME", "test-operator")
	t.Setenv("GHCR_PULL_TOKEN", "test-registry-token")
	writeTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "account.env"), "LKE_ACTIVE_SERVICE_LIMIT=20\n")
	bin := t.TempDir()
	started := filepath.Join(bin, "provider-started")
	t.Setenv("PREFLIGHT_PROVIDER_STARTED", started)
	writeTestFile(t, filepath.Join(bin, "curl"), "#!/bin/sh\nprintf started > \"$PREFLIGHT_PROVIDER_STARTED\"\nexec sleep 30\n")
	if err := os.Chmod(filepath.Join(bin, "curl"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks := defaultDeploymentPreflightChecksContext(ctx)
	checks.lookPath = func(string) (string, error) { return "/test/tool", nil }
	checks.validateDNS = func(deploymentConfig) error { return nil }
	var out bytes.Buffer
	result := make(chan error, 1)
	go func() { result <- runDeploymentPreflightWithChecksContext(ctx, cfg, "provision", checks, &out) }()
	requirePreflightCommandCancellation(t, started, cancel, result)
	if strings.Contains(out.String(), "Preflight result: PASS") {
		t.Fatal("timed-out preflight reported success")
	}
}

func TestDeploymentPreflightContextKillsStalledKubernetesProbe(t *testing.T) {
	makeIsolatedTestSecretStore(t, "staging")
	bin := t.TempDir()
	path := filepath.Join(bin, "kubectl")
	started := filepath.Join(bin, "kube-started")
	t.Setenv("PREFLIGHT_KUBE_STARTED", started)
	writeTestFile(t, path, "#!/bin/sh\nprintf started > \"$PREFLIGHT_KUBE_STARTED\"\nexec sleep 30\n")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- defaultDeploymentPreflightChecksContext(ctx).validateKube(deploymentConfig{Environment: "staging"})
	}()
	requirePreflightCommandCancellation(t, started, cancel, result)
}

func requirePreflightCommandCancellation(t *testing.T, started string, cancel context.CancelFunc, result <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("preflight probe ended before its command started: %v", err)
		case <-deadline.C:
			cancel()
			t.Fatal("preflight command did not start within 5 seconds")
		case <-poll.C:
		}
	}
	// Cancel only after the external command confirms it started, so process
	// startup under CI load cannot turn this into a no-op cancellation test.
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("preflight command did not propagate cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("preflight command remained running after cancellation")
	}
}

type canceledPreflightRoute53 struct {
	fakeRoute53API
	called bool
}

func (api *canceledPreflightRoute53) ListHostedZonesByName(ctx context.Context, _ *route53.ListHostedZonesByNameInput, _ ...func(*route53.Options)) (*route53.ListHostedZonesByNameOutput, error) {
	api.called = true
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDeploymentPreflightContextCancelsDNSDiscovery(t *testing.T) {
	previous := loadRoute53Client
	t.Cleanup(func() { loadRoute53Client = previous })
	api := &canceledPreflightRoute53{}
	loadRoute53Client = func(context.Context, string) (route53API, error) { return api, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cfg := deploymentConfig{Values: map[string]string{"CLOUD_DNS_ROOT_DOMAIN": "example.test"}, DNSValues: map[string]string{"DNS_ADAPTER": "route53"}}
	err := defaultDeploymentPreflightChecksContext(ctx).validateDNS(cfg)
	if !api.called || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DNS discovery did not receive the preflight deadline: called=%t err=%v", api.called, err)
	}
	if len(api.changes) != 0 {
		t.Fatal("DNS preflight sent a mutation")
	}
}
