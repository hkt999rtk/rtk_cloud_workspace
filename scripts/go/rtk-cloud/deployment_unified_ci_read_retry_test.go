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

func ciReadRetryFixture(t *testing.T) (deploymentConfig, string) {
	t.Helper()
	_, dir := releaseDockerAuthFixture(t, "dev")
	t.Setenv("GH_TOKEN", "foreign-ambient-token")
	t.Setenv("GITHUB_TOKEN", "foreign-ambient-token")
	t.Setenv("RTK_CI_READ_CALLS", filepath.Join(dir, "calls"))
	t.Setenv("RTK_CI_READ_FAILURES", "2")
	t.Setenv("RTK_CI_READ_DIAGNOSTIC", "gh: temporarily unavailable (HTTP 503)")
	rolloutWrite(t, filepath.Join(dir, "gh"), `#!/bin/sh
set -eu
test "${GH_TOKEN:-}" = private-release-token-canary || exit 71
printf 'call\n' >> "$RTK_CI_READ_CALLS"
if [ "${RTK_CI_READ_SLEEP:-}" = yes ]; then exec /bin/sleep 5; fi
count=$(wc -l < "$RTK_CI_READ_CALLS")
if [ "$count" -le "$RTK_CI_READ_FAILURES" ]; then
 printf 'untrusted partial private-canary'
 printf '%s\nprivate-canary private-release-token-canary\n' "$RTK_CI_READ_DIAGNOSTIC" >&2
 exit 1
fi
printf 'exact-public-artifact'
`, 0700)
	return deploymentConfig{Environment: "dev"}, dir
}

func ciReadRetryCalls(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "calls"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "call\n")
}

func TestEnvironmentCIReadRetryUsesSelectedCredentialAndInvocationCache(t *testing.T) {
	cfg, dir := ciReadRetryFixture(t)
	run := environmentCIImageCommand(cfg)
	ctx := context.WithValue(t.Context(), deploymentInvocationCacheKey{}, &deploymentInvocationCache{values: map[string][]byte{}})
	args := []string{"api", "repos/owner/repo/actions/artifacts/123/zip"}
	raw, err := run(ctx, "gh", args...)
	if err != nil || string(raw) != "exact-public-artifact" || ciReadRetryCalls(t, dir) != 3 {
		t.Fatal("third bounded GET did not recover using the selected environment credential", err)
	}
	raw[0] = 'x'
	raw, err = run(ctx, "gh", args...)
	if err != nil || string(raw) != "exact-public-artifact" || ciReadRetryCalls(t, dir) != 3 {
		t.Fatal("successful immutable content was not invocation-local and copied", err)
	}
	if _, err := run(t.Context(), "gh", args...); err != nil || ciReadRetryCalls(t, dir) != 4 {
		t.Fatal("successful content escaped its invocation", err)
	}
}

func TestEnvironmentCIReadRetryExhaustionIsSafeAndNotCached(t *testing.T) {
	cfg, dir := ciReadRetryFixture(t)
	t.Setenv("RTK_CI_READ_FAILURES", "3")
	run := environmentCIImageCommand(cfg)
	ctx := context.WithValue(t.Context(), deploymentInvocationCacheKey{}, &deploymentInvocationCache{values: map[string][]byte{}})
	args := []string{"api", "repos/owner/repo/actions/artifacts/123/zip"}
	raw, err := run(ctx, "gh", args...)
	if err == nil || raw != nil || ciReadRetryCalls(t, dir) != 3 || !isDeploymentCIReadError(err) || strings.Contains(err.Error(), "private-canary") || strings.Contains(err.Error(), "token-canary") {
		t.Fatal("bounded transient failure was cached, unclassified or unsafe", err)
	}
	var failure *deploymentCIReadError
	if !errors.As(err, &failure) || failure.Code != "HTTP_503" || errors.Unwrap(err) != nil {
		t.Fatal("HTTP failure lost its safe class or retained a raw process cause", err)
	}
	raw, err = run(ctx, "gh", args...)
	if err != nil || string(raw) != "exact-public-artifact" || ciReadRetryCalls(t, dir) != 4 {
		t.Fatal("exhausted failure prevented a fresh subsequent invocation read", err)
	}
}

func TestEnvironmentCIReadRetryOnlyTransientDefaultGETs(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		args             []string
		calls            int
	}{
		{"reset", "read: connection reset by peer", []string{"api", "repos/o/r"}, 2},
		{"unexpected-eof", "gh: unexpected EOF", []string{"api", "repos/o/r"}, 2},
		{"bare-eof", "EOF", []string{"api", "repos/o/r"}, 2},
		{"wrapped-eof", "Get https://api.github.com/repos/o/r: EOF", []string{"api", "repos/o/r"}, 2},
		{"timeout", "net/http: TLS handshake timeout", []string{"api", "repos/o/r"}, 2},
		{"rate-limit", "gh: rate limited (HTTP 429)", []string{"api", "repos/o/r"}, 2},
		{"bad-gateway", "gh: unavailable (HTTP 502)", []string{"api", "repos/o/r"}, 2},
		{"unavailable", "gh: unavailable (HTTP 503)", []string{"api", "repos/o/r"}, 2},
		{"gateway-timeout", "gh: unavailable (HTTP 504)", []string{"api", "repos/o/r"}, 2},
		{"unauthorized", "gh: Bad credentials (HTTP 401) connection reset by peer", []string{"api", "repos/o/r"}, 1},
		{"forbidden", "gh: Forbidden (HTTP 403) TLS handshake timeout", []string{"api", "repos/o/r"}, 1},
		{"missing", "gh: Not Found (HTTP 404) unexpected EOF", []string{"api", "repos/o/r"}, 1},
		{"request-timeout-status", "gh: timed out (HTTP 408)", []string{"api", "repos/o/r"}, 1},
		{"invalid-request", "gh: invalid request (HTTP 422)", []string{"api", "repos/o/r"}, 1},
		{"other-http", "gh: server failed (HTTP 500) connection reset by peer", []string{"api", "repos/o/r"}, 1},
		{"unknown", "malformed private diagnostic", []string{"api", "repos/o/r"}, 1},
		{"malformed-json-eof", "gh: error parsing JSON: unexpected EOF", []string{"api", "repos/o/r"}, 1},
		{"post", "gh: unavailable (HTTP 503)", []string{"api", "repos/o/r", "--method", "POST"}, 1},
		{"body", "gh: unavailable (HTTP 503)", []string{"api", "repos/o/r", "-f", "key=value"}, 1},
		{"input", "gh: unavailable (HTTP 503)", []string{"api", "repos/o/r", "--input", "payload.json"}, 1},
		{"explicit-get-option", "gh: unavailable (HTTP 503)", []string{"api", "repos/o/r", "--method", "GET"}, 1},
		{"graphql", "gh: unavailable (HTTP 503)", []string{"api", "graphql"}, 1},
		{"absolute-graphql", "gh: unavailable (HTTP 503)", []string{"api", "https://api.github.com/graphql"}, 1},
		{"workflow", "gh: unavailable (HTTP 503)", []string{"workflow", "run", "release.yml"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, dir := ciReadRetryFixture(t)
			t.Setenv("RTK_CI_READ_FAILURES", "1")
			t.Setenv("RTK_CI_READ_DIAGNOSTIC", tc.diagnostic)
			raw, err := environmentCIImageCommand(cfg)(t.Context(), "gh", tc.args...)
			if (err == nil) != (tc.calls == 2) || ciReadRetryCalls(t, dir) != tc.calls || err != nil && (raw != nil || strings.Contains(err.Error(), "private-canary") || strings.Contains(err.Error(), "token-canary")) {
				t.Fatal("transient GET whitelist or fail-fast boundary changed", err)
			}
		})
	}
}

func TestEnvironmentCIReadRetryAttemptDeadlineAndFastFirstSuccess(t *testing.T) {
	t.Run("per-attempt-deadline", func(t *testing.T) {
		_, dir := ciReadRetryFixture(t)
		t.Setenv("RTK_CI_READ_SLEEP", "yes")
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		raw, err := runDeploymentCIRead(ctx, []string{"api", "repos/o/r"}, "private-release-token-canary", 150*time.Millisecond, time.Millisecond)
		var failure *deploymentCIReadError
		if !errors.As(err, &failure) || failure.Code != "TIMEOUT" || !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || raw != nil || ciReadRetryCalls(t, dir) != 3 {
			t.Fatal("attempt deadline was not bounded/retried independently of the live caller", err)
		}
	})
	t.Run("first-success", func(t *testing.T) {
		_, dir := ciReadRetryFixture(t)
		t.Setenv("RTK_CI_READ_FAILURES", "0")
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		// The retry delay exceeds the entire caller budget. A successful first
		// process must return immediately without entering that delay.
		raw, err := runDeploymentCIRead(ctx, []string{"api", "repos/o/r"}, "private-release-token-canary", 200*time.Millisecond, time.Second)
		if err != nil || string(raw) != "exact-public-artifact" || ciReadRetryCalls(t, dir) != 1 {
			t.Fatal("successful first GET incurred a retry or delay", err)
		}
	})
}

func TestEnvironmentCIReadRetryPreservesOverallDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"during-process", "during-delay", "already-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, dir := ciReadRetryFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			want := context.DeadlineExceeded
			if mode == "during-process" {
				t.Setenv("RTK_CI_READ_SLEEP", "yes")
			} else if mode == "already-cancelled" {
				cancel()
				want = context.Canceled
			}
			start := time.Now()
			raw, err := environmentCIImageCommand(cfg)(ctx, "gh", "api", "repos/o/r")
			if !errors.Is(err, want) || raw != nil || ciReadRetryCalls(t, dir) > 1 || time.Since(start) > 2*time.Second {
				t.Fatal("caller deadline/cancellation was reset or another GET started", err)
			}
			if mode == "already-cancelled" && ciReadRetryCalls(t, dir) != 0 {
				t.Fatal("cancelled caller started a command")
			}
		})
	}
}
