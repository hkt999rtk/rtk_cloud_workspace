package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeploymentCheckOptionsCompatibility(t *testing.T) {
	image := "ghcr.io/owner/app@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name           string
		args           []string
		fast, readOnly bool
		timeout        time.Duration
	}{
		{"legacy", nil, false, false, 10 * time.Minute},
		{"read-only still pulls", []string{"--read-only", "--image", image}, false, true, 10 * time.Minute},
		{"fast", []string{"--fast", "--image", image}, true, true, 2 * time.Minute},
		{"custom timeout", []string{"--fast", "--timeout", "30s"}, true, true, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseDeploymentCheckOptions(append([]string{"--environment", "staging"}, tc.args...), io.Discard)
			if err != nil || o.qualification.fast != tc.fast || o.qualification.readOnly != tc.readOnly || o.timeout != tc.timeout {
				t.Fatalf("options=%+v err=%v", o, err)
			}
		})
	}
}

func TestDeploymentCheckRejectsArgumentsBeforeExternalWork(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--environment", "staging", "--unknown"},
		{"--environment", "staging", "--checks", "typo"},
		{"--environment", "staging", "--fast", "--read-only=false"},
		{"--environment", "staging", "--fast", "--create-missing-object-storage-bucket"},
		{"--environment", "staging", "--fast", "--grant-object-storage-bucket-access"},
		{"--environment", "staging", "--timeout", "0s"},
		{"--environment", "staging", "--timeout", "-1s"},
		{"--environment", "staging", "--checks", "tls"},
		{"--environment", "staging", "--checks", "mounts", "--manifest", "/definitely-missing-manifest"},
		{"--environment", "staging", "--image", "ghcr.io/owner/app:latest"},
		{"--environment", "staging", "--read-only", "false"},
		{"--environment", "staging", "--env-file", "obsolete"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			deps := deploymentCheckDependencies{guard: func([]string) error {
				t.Fatal("consulted operator state before rejecting invalid arguments")
				return nil
			}, resolve: func(string, string, string) (deploymentConfig, error) {
				t.Fatal("resolved configuration before rejecting invalid arguments")
				return deploymentConfig{}, nil
			}}
			var diagnostics bytes.Buffer
			err := runDeploymentCheckWithDependencies(context.Background(), args, io.Discard, &diagnostics, deps)
			var code exitCode
			if !errors.As(err, &code) || code != 2 {
				t.Fatalf("error=%v diagnostics=%s", err, &diagnostics)
			}
		})
	}
	if err := runDeploymentCheckWithDependencies(context.Background(), []string{"--help"}, io.Discard, io.Discard, deploymentCheckDependencies{}); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentCheckPreservesMaintenanceFence(t *testing.T) {
	deps := deploymentCheckDependencies{
		guard: func(args []string) error {
			if len(args) < 2 || args[0] != "deployment" || args[1] != "check" {
				t.Fatalf("guard args=%v", args)
			}
			return errors.New("maintenance is active")
		},
		resolve: func(string, string, string) (deploymentConfig, error) {
			t.Fatal("external checks after maintenance fence")
			return deploymentConfig{}, nil
		},
	}
	err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--fast"}, io.Discard, io.Discard, deps)
	var code exitCode
	if !errors.As(err, &code) || code != 1 {
		t.Fatalf("maintenance fence exit=%v", err)
	}
}

func facadeFixture(t *testing.T, kube bool) (deploymentCheckDependencies, secretStore) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "staging")
	if kube {
		if err := store.write("kube/kubeconfig.yaml", []byte("test-only-kubeconfig"), true); err != nil {
			t.Fatal(err)
		}
	}
	workspace := t.TempDir()
	return deploymentCheckDependencies{
		resolve: func(_, env, _ string) (deploymentConfig, error) {
			return deploymentConfig{Environment: env, Workspace: workspace}, nil
		},
		store: func(string) (secretStore, error) { return store, nil },
		local: func(secretStore) error { return nil }, bindings: func(secretStore) error { return nil },
		runtime: func(secretStore, time.Time) error { return nil }, migration: func(secretStore) error { return nil }, product: func(secretStore) error { return nil }, workloads: func(secretStore) error { return nil },
		public: func(context.Context, deploymentConfig, secretStore) error { return nil },
		collect: func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
			c := deploymentCredentialCheck{ID: "provider.fixture", Name: "fixture", Required: true, Status: "PASS", Passed: true, Detail: "verified"}
			emit(c)
			return []deploymentCredentialCheck{c}
		},
	}, store
}

func readFacadeReport(t *testing.T, path string) deploymentCheckReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report deploymentCheckReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func reportCheck(t *testing.T, r deploymentCheckReport, id string) deploymentCheckResult {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("missing check %s: %+v", id, r)
	return deploymentCheckResult{}
}

func TestDeploymentCheckMissingKubeconfigBlocksLiveButCollectsIndependentResults(t *testing.T) {
	deps, _ := facadeFixture(t, false)
	deps.bindings = func(secretStore) error { t.Fatal("bindings without kubeconfig"); return nil }
	deps.runtime = func(secretStore, time.Time) error { t.Fatal("runtime without kubeconfig"); return nil }
	called := false
	base := deps.collect
	deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
		called = true
		if allow {
			t.Fatal("mutations allowed without live prerequisites")
		}
		return base(ctx, cfg, path, o, allow, emit)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--report", path}, io.Discard, io.Discard, deps)
	if err == nil || !called {
		t.Fatalf("err=%v called=%t", err, called)
	}
	report := readFacadeReport(t, path)
	if c := reportCheck(t, report, "k8s.access-input"); c.Status != "BLOCKED" || c.Code != "KUBECONFIG_UNAVAILABLE" {
		t.Fatalf("%+v", c)
	}
	if c := reportCheck(t, report, "pki.runtime"); c.Status != "BLOCKED" {
		t.Fatalf("%+v", c)
	}
	if c := reportCheck(t, report, "provider.fixture"); c.Status != "PASS" {
		t.Fatalf("%+v", c)
	}
}

func TestDeploymentCheckFastRetainsSecretPKIAndProducesPrivateReport(t *testing.T) {
	deps, _ := facadeFixture(t, true)
	seen := map[string]int{}
	var seenMu sync.Mutex
	record := func(name string) { seenMu.Lock(); defer seenMu.Unlock(); seen[name]++ }
	deps.local = func(secretStore) error { record("local"); return nil }
	deps.bindings = func(store secretStore) error {
		record("bindings")
		if store.checkRuntime == nil {
			t.Fatal("no bounded runtime")
		}
		return nil
	}
	deps.runtime = func(secretStore, time.Time) error { record("runtime"); return nil }
	deps.migration = func(secretStore) error { record("migration"); return nil }
	deps.product = func(secretStore) error { record("product"); return nil }
	base := deps.collect
	deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
		if !o.fast || !o.readOnly || !allow {
			t.Fatalf("fast=%t readonly=%t prerequisites=%t", o.fast, o.readOnly, allow)
		}
		return base(ctx, cfg, path, o, allow, emit)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	var out bytes.Buffer
	err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--fast", "--require-pki-migration", "--require-product-pki", "--report", path}, &out, io.Discard, deps)
	if err != nil {
		t.Fatalf("%v: %s", err, &out)
	}
	for _, key := range []string{"local", "bindings", "runtime", "migration", "product"} {
		if seen[key] != 1 {
			t.Fatalf("%s=%d", key, seen[key])
		}
	}
	report := readFacadeReport(t, path)
	if report.Overall != "PASS" || report.Mode != "fast" || report.SchemaVersion != 1 || !strings.Contains(report.Scope, "unverified") {
		t.Fatalf("%+v", report)
	}
	for i, c := range report.Checks {
		if i > 0 && report.Checks[i-1].ID > c.ID {
			t.Fatal("unstable report ordering")
		}
		if c.EvidenceTime == "" {
			t.Fatal("missing evidence time")
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode %v", info.Mode())
	}
}

func TestDeploymentCheckDeadlineRetainsPartialResultsAndPreventsWrites(t *testing.T) {
	deps, _ := facadeFixture(t, true)
	deps.bindings = func(store secretStore) error { <-store.checkRuntime.ctx.Done(); return store.checkRuntime.ctx.Err() }
	base := deps.collect
	deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
		if allow {
			t.Fatal("writes after timeout")
		}
		return base(ctx, cfg, path, o, allow, emit)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	started := time.Now()
	err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--timeout", "20ms", "--report", path}, io.Discard, io.Discard, deps)
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
	report := readFacadeReport(t, path)
	if report.Overall != "FAIL" || reportCheck(t, report, "secrets.local").Status != "PASS" || reportCheck(t, report, "secrets.bindings").Code != "CHECK_TIMEOUT" {
		t.Fatalf("%+v", report)
	}
}

func TestDeploymentCheckSecretFailurePreventsWrites(t *testing.T) {
	deps, _ := facadeFixture(t, true)
	deps.local = func(secretStore) error { return errors.New("required runtime secrets are missing: postgres") }
	base := deps.collect
	deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
		if allow {
			t.Fatal("provider mutations with failed SecretStore")
		}
		return base(ctx, cfg, path, o, allow, emit)
	}
	if err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging"}, io.Discard, io.Discard, deps); err == nil {
		t.Fatal("expected failure")
	}
}

func TestDeploymentCheckReporterNeverPassesIncompleteRequiredChecks(t *testing.T) {
	r := newDeploymentCheckReporter(io.Discard)
	r.emit(deploymentCredentialCheck{ID: "waiting", Status: "PENDING", Required: true})
	report := r.snapshot("staging", deploymentCheckPostDeploy, true, true)
	if report.Overall != "FAIL" || reportCheck(t, report, "waiting").Status != "BLOCKED" {
		t.Fatalf("%+v", report)
	}
}

func TestDeploymentCheckFinalSuccessAfterDeadlineCannotPass(t *testing.T) {
	deps, _ := facadeFixture(t, true)
	base := deps.collect
	deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
		<-ctx.Done()
		return base(ctx, cfg, path, o, allow, emit)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging", "--timeout", "20ms", "--report", path}, io.Discard, io.Discard, deps)
	if err == nil {
		t.Fatal("success after deadline")
	}
	report := readFacadeReport(t, path)
	if c := reportCheck(t, report, "execution.deadline"); c.Status != "ERROR" || c.Code != "CHECK_TIMEOUT" {
		t.Fatalf("%+v", c)
	}
}

func TestDeploymentCheckPlansBeforeExternalWork(t *testing.T) {
	deps, _ := facadeFixture(t, true)
	planned := false
	deps.plan = func(_ context.Context, _ deploymentConfig, _ string, _ deploymentCredentialCheckOptions, emit func(deploymentCredentialCheck)) {
		planned = true
		emit(deploymentCredentialCheck{ID: "provider.fixture", Status: "PENDING", Required: true})
	}
	deps.local = func(secretStore) error {
		if !planned {
			t.Error("started before complete plan")
		}
		return nil
	}
	if err := runDeploymentCheckWithDependencies(context.Background(), []string{"--environment", "staging"}, io.Discard, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
}
