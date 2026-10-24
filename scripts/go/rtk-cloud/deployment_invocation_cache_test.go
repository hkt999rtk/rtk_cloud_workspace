package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDeploymentImmutableCICacheKeepsMutableChecksFresh(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		args          []string
		cached        bool
	}{
		{"archive", "gh", []string{"api", "repos/owner/repo/actions/artifacts/123/zip"}, true},
		{"labels", "docker", []string{"image", "inspect", "ghcr.io/owner/repo/image@sha256:" + strings.Repeat("a", 64), "--format", "{{json .Config.Labels}}"}, true},
		{"artifact-expiry", "gh", []string{"api", "repos/owner/repo/actions/runs/123/artifacts?per_page=100"}, false},
		{"run", "gh", []string{"api", "repos/owner/repo/actions/runs/123"}, false},
		{"source", "git", []string{"rev-parse", "HEAD"}, false},
		{"mutable-tag", "docker", []string{"image", "inspect", "ghcr.io/owner/repo/image:dev", "--format", "{{json .Config.Labels}}"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), deploymentInvocationCacheKey{}, &deploymentInvocationCache{values: map[string][]byte{}})
			calls := 0
			run := func() ([]byte, error) { calls++; return []byte("verified bytes"), nil }
			a, err := cachedDeploymentCICommand(ctx, "dev", tc.command, tc.args, run)
			if err != nil {
				t.Fatal(err)
			}
			a[0] = 'x'
			b, err := cachedDeploymentCICommand(ctx, "dev", tc.command, tc.args, run)
			want := 2
			if tc.cached {
				want = 1
			}
			if err != nil || calls != want || string(b) != "verified bytes" {
				t.Fatal("cache did not preserve immutable content or fresh checks", calls, err)
			}
			_, _ = cachedDeploymentCICommand(ctx, "staging", tc.command, tc.args, run)
			if calls != want+1 {
				t.Fatal("cache crossed environments")
			}
		})
	}
}
func TestDeploymentImmutableCICacheDoesNotCacheFailuresOrOutliveInvocation(t *testing.T) {
	args := []string{"api", "repos/owner/repo/actions/artifacts/123/zip"}
	ctx := context.WithValue(context.Background(), deploymentInvocationCacheKey{}, &deploymentInvocationCache{values: map[string][]byte{}})
	calls := 0
	run := func() ([]byte, error) { calls++; return nil, errors.New("private failure") }
	for range 2 {
		_, _ = cachedDeploymentCICommand(ctx, "dev", "gh", args, run)
	}
	if calls != 2 {
		t.Fatal("cached failure")
	}
	success := func() ([]byte, error) { calls++; return []byte("ok"), nil }
	_, _ = cachedDeploymentCICommand(ctx, "dev", "gh", args, success)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := cachedDeploymentCICommand(cancelled, "dev", "gh", args, success); !errors.Is(err, context.Canceled) {
		t.Fatal("cache bypassed cancellation")
	}
	_, _ = cachedDeploymentCICommand(context.Background(), "dev", "gh", args, success)
	if calls != 4 {
		t.Fatal("cache escaped invocation")
	}
}
