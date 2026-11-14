package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestDeploymentPullRetriesOnlyTransientAndSanitizes(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, mode, wantCode string
		attempts                     int
		success                      bool
	}{
		{"timeout-then-success", "dial tcp: i/o timeout secret-canary https://example.test/?token=private", "first", "", 2, true},
		{"timeout-exhausted", "TLS handshake timeout secret-canary", "always", "NETWORK_TIMEOUT", 3, false},
		{"network-reset", "connection reset by peer secret-canary", "always", "NETWORK_TRANSIENT", 3, false},
		{"registry-503", "503 Service Unavailable secret-canary", "always", "REGISTRY_TRANSIENT", 3, false},
		{"unauthorized-no-retry", "unauthorized: secret-canary i/o timeout", "always", "AUTH", 1, false},
		{"manifest-no-retry", "manifest unknown secret-canary", "always", "MANIFEST", 1, false},
		{"space-no-retry", "no space left on device secret-canary", "always", "NO_SPACE", 1, false},
		{"platform-no-retry", "no matching manifest for linux/amd64 secret-canary", "always", "PLATFORM", 1, false},
		{"rate-limit-no-retry", "too many requests secret-canary", "always", "RATE_LIMIT", 1, false},
		{"unknown-no-retry", "private error secret-canary", "always", "UNKNOWN", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			makeIsolatedTestSecretStore(t, "dev")
			bin, counter := t.TempDir(), filepath.Join(t.TempDir(), "attempts")
			t.Setenv("PATH", bin)
			t.Setenv("RTK_PULL_RETRY_COUNTER", counter)
			t.Setenv("RTK_PULL_RETRY_MODE", tc.mode)
			t.Setenv("RTK_PULL_RETRY_DIAGNOSTIC", tc.stderr)
			rolloutWrite(t, filepath.Join(bin, "docker"), `#!/bin/sh
n=0
[ ! -f "$RTK_PULL_RETRY_COUNTER" ] || n=$(/bin/cat "$RTK_PULL_RETRY_COUNTER")
n=$((n + 1))
printf '%s' "$n" > "$RTK_PULL_RETRY_COUNTER"
if [ "$RTK_PULL_RETRY_MODE" = first ] && [ "$n" -gt 1 ]; then exit 0; fi
printf '%s\n' "$RTK_PULL_RETRY_DIAGNOSTIC" >&2
exit 1
`, 0700)
			var out, log bytes.Buffer
			progress := &environmentProgress{out: &out, log: &log}
			ctx := context.WithValue(context.Background(), environmentProgressKey{}, progress)
			waits := 0
			wait := func(ctx context.Context, attempt int) error { waits++; return ctx.Err() }
			err := pullRolloutImageContextWithRetry(ctx, "dev", "private-user", "private-token", "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64), wait)
			if (err == nil) != tc.success {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if got := readTestFile(t, counter); got != fmt.Sprint(tc.attempts) {
				t.Fatalf("attempts=%s", got)
			}
			if waits != tc.attempts-1 {
				t.Fatalf("waits=%d", waits)
			}
			if err != nil {
				var classified *deploymentImagePullError
				if !errors.As(err, &classified) || classified.code != tc.wantCode || classified.attempts != tc.attempts {
					t.Fatalf("classification=%v", err)
				}
				blocker := environmentReleaseFailure(err, "deployment.release.pull", "postgres", "unavailable", "retry")
				var b *deploymentplan.Blocker
				if !errors.As(blocker, &b) || !strings.Contains(b.Cause, tc.wantCode) || !strings.Contains(b.Cause, fmt.Sprintf("attempts=%d", tc.attempts)) {
					t.Fatalf("missing safe report: %v", blocker)
				}
				if strings.Contains(blocker.Error(), "secret-canary") || strings.Contains(blocker.Error(), "private-token") {
					t.Fatal("secret leaked in report")
				}
			}
			combined := out.String() + log.String()
			for _, secret := range []string{"secret-canary", "private-token", "example.test", "private-user"} {
				if strings.Contains(combined, secret) {
					t.Fatal("secret leaked in progress")
				}
			}
			if tc.attempts > 1 && (!strings.Contains(out.String(), "[RETRY]") || !strings.Contains(log.String(), "[RETRY]")) {
				t.Fatal("retry missing from user progress or private log")
			}
		})
	}
}

func TestDeploymentPullRetryCancellationPreservesDeadline(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	rolloutWrite(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf 'dial tcp: i/o timeout private-token' >&2\nexit 1\n", 0700)
	ctx, cancel := context.WithCancel(context.Background())
	wait := func(ctx context.Context, _ int) error { cancel(); return ctx.Err() }
	start := time.Now()
	err := pullRolloutImageContextWithRetry(ctx, "dev", "user", "private-token", "ghcr.io/a/b@sha256:"+strings.Repeat("b", 64), wait)
	var classified *deploymentImagePullError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &classified) || classified.attempts != 1 || strings.Contains(err.Error(), "private-token") || time.Since(start) > time.Second {
		t.Fatal("cancellation lost or leaked", err)
	}
	if !errors.Is(environmentReleaseFailure(err, "deployment.release.pull", "image", "failed", "retry"), context.Canceled) {
		t.Fatal("release wrapped away cancellation")
	}
}

func TestDeploymentPullDiagnosticIsBounded(t *testing.T) {
	var d deploymentPullDiagnostic
	payload := bytes.Repeat([]byte("secret-canary"), deploymentPullDiagnosticLimit)
	n, err := d.Write(payload)
	if err != nil || n != len(payload) || len(d.data) != deploymentPullDiagnosticLimit {
		t.Fatal("diagnostic capture not bounded")
	}
	if code, retry := classifyDeploymentPullDiagnostic(d.data); code != "UNKNOWN" || retry {
		t.Fatal("unknown diagnostics were retried")
	}
}

func TestDeploymentPullProductionBackoffHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitDeploymentPullRetry(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("backoff ignored cancellation", err)
	}
}

func TestDeploymentPullClassifiedErrorReachesReleaseReport(t *testing.T) {
	_, dir := releaseDockerAuthFixture(t, "dev")
	rolloutWrite(t, filepath.Join(dir, "docker"), "#!/bin/sh\nprintf 'unauthorized: private-release-token-canary' >&2\nexit 1\n", 0700)
	_, err := environmentCIImageCommand(deploymentConfig{Environment: "dev"})(context.Background(), "docker", "pull", "--platform", "linux/amd64", "docker.io/library/postgres@sha256:"+strings.Repeat("b", 64))
	var classified *deploymentImagePullError
	if !errors.As(err, &classified) || classified.code != "AUTH" || classified.attempts != 1 {
		t.Fatal("formal image command lost sanitized diagnosis", err)
	}
	failure := environmentReleaseFailure(err, "deployment.release.pull", "postgres", "failed", "inspect credentials")
	var blocker *deploymentplan.Blocker
	if !errors.As(failure, &blocker) || blocker.Resource != "postgres" || !strings.Contains(blocker.Cause, "AUTH") || strings.Contains(blocker.Error(), "private-release-token-canary") {
		t.Fatal("formal release report lost category or leaked diagnostics", failure)
	}
}

func TestDeploymentProviderPullPreservesSafeDiagnosisAndCancellation(t *testing.T) {
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "POSTGRES_IMAGE": "docker.io/library/postgres:16-alpine"}}
	for _, tc := range []struct {
		name  string
		input error
		code  string
	}{
		{"network", &deploymentImagePullError{code: "NETWORK_TIMEOUT", detail: "linux/amd64 pull failed (NETWORK_TIMEOUT; attempts=3)", attempts: 3}, "NETWORK_TIMEOUT"},
		{"cancel", context.Canceled, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(context.Context, string, ...string) ([]byte, error) { return nil, tc.input }
			_, err := loadEnvironmentCIImages(context.Background(), cfg, run)
			if tc.code == "" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("provider swallowed cancellation", err)
				}
				return
			}
			var b *deploymentplan.Blocker
			if !errors.As(err, &b) || b.ID != "deployment.provider.images" || b.Resource != "POSTGRES_IMAGE" || !strings.Contains(b.Cause, tc.code) {
				t.Fatal("provider lost safe pull diagnosis", err)
			}
		})
	}
}
