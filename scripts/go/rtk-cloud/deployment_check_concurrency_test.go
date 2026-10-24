package main

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeploymentHealthGroupsShareQueryLimitAndJoinBeforeProviders(t *testing.T) {
	deps, _ := facadeFixture(t, true)
	resolve := deps.resolve
	deps.resolve = func(workspace, environment, environmentRoot string) (deploymentConfig, error) {
		cfg, err := resolve(workspace, environment, environmentRoot)
		cfg.Values = map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "CLOUD_DNS_ROOT_DOMAIN": "fixture.invalid"}
		return cfg, err
	}
	var localDone atomic.Bool
	deps.local = func(secretStore) error { localDone.Store(true); return nil }
	started := make(chan string, 6)
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	var active, peak, completed atomic.Int32
	live := func(id string, store secretStore) error {
		if !localDone.Load() || store.checkRuntime == nil {
			t.Error("live health query preceded its local prerequisite or lacked shared runtime")
		}
		count := active.Add(1)
		for old := peak.Load(); count > old && !peak.CompareAndSwap(old, count); old = peak.Load() {
		}
		defer active.Add(-1)
		defer completed.Add(1)
		started <- id
		select {
		case <-release:
			return nil
		case <-store.checkRuntime.ctx.Done():
			return store.checkRuntime.ctx.Err()
		}
	}
	deps.bindings = func(store secretStore) error { return live("secrets.bindings", store) }
	deps.runtime = func(store secretStore, _ time.Time) error { return live("pki.runtime", store) }
	deps.workloads = func(store secretStore) error { return live("workloads.runtime", store) }
	deps.public = func(_ context.Context, _ deploymentConfig, store secretStore) error {
		return live("pki.public-certissuer", store)
	}
	deps.migration = func(store secretStore) error { return live("pki.migration", store) }
	deps.product = func(store secretStore) error { return live("pki.product-root", store) }
	collect := deps.collect
	deps.collect = func(ctx context.Context, cfg deploymentConfig, path string, options deploymentCredentialCheckOptions, allow bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
		if active.Load() != 0 || completed.Load() != 6 || allow {
			t.Error("provider collection overlapped unfinished live groups or allowed writes")
		}
		return collect(ctx, cfg, path, options, allow, emit)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reportPath := filepath.Join(t.TempDir(), "report.json")
	done := make(chan error, 1)
	go func() {
		done <- runDeploymentCheckWithDependencies(ctx, []string{"--environment", "staging", "--read-only", "--require-pki-migration", "--require-product-pki", "--report", reportPath}, io.Discard, io.Discard, deps)
	}()
	seen := map[string]bool{}
	for len(seen) < 6 {
		select {
		case id := <-started:
			if seen[id] {
				t.Error("live health group ran twice", id)
			}
			seen[id] = true
		case <-ctx.Done():
			finish()
			<-done
			t.Fatal("eligible health groups could not overlap under the shared limit", seen)
		}
	}
	if peak.Load() != 6 || peak.Load() > int32(environmentDeploymentCheckConcurrency) {
		t.Fatal("live health queries exceeded the shared limit or retained the old four-group cap", peak.Load())
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal("parallel health groups failed", err)
	}
	report := readFacadeReport(t, reportPath)
	for id := range seen {
		if result := reportCheck(t, report, id); result.Status != "PASS" {
			t.Fatal("joined successful live health query did not pass", result)
		}
	}
}
