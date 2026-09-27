package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLKEEvaluateLegacyOTADrain(t *testing.T) {
	checked := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	quiet := checked.Add(-49 * time.Hour)
	recent := checked.Add(-47 * time.Hour)
	base := otaLegacyDrainSnapshot{CheckedAt: checked, LegacyReleases: 2, LastActivityAt: &quiet}
	for _, tc := range []struct {
		name     string
		change   func(*otaLegacyDrainSnapshot)
		wantFail string
	}{
		{name: "drained"},
		{name: "no legacy work", change: func(s *otaLegacyDrainSnapshot) { s.LegacyReleases = 0; s.LastActivityAt = nil }},
		{name: "bad key", change: func(s *otaLegacyDrainSnapshot) { s.InvalidKeys = 1 }, wantFail: "noncanonical"},
		{name: "open campaign", change: func(s *otaLegacyDrainSnapshot) { s.OpenCampaigns = 1 }, wantFail: "campaigns=1"},
		{name: "open deployment", change: func(s *otaLegacyDrainSnapshot) { s.OpenDeployments = 1 }, wantFail: "deployments=1"},
		{name: "live grant", change: func(s *otaLegacyDrainSnapshot) { s.UnexpiredGrants = 1 }, wantFail: "unexpired"},
		{name: "late report window", change: func(s *otaLegacyDrainSnapshot) { s.LastActivityAt = &recent }, wantFail: "retry after"},
		{name: "missing database time", change: func(s *otaLegacyDrainSnapshot) { s.CheckedAt = time.Time{} }, wantFail: "incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := base
			if tc.change != nil {
				tc.change(&snapshot)
			}
			err := lkeEvaluateLegacyOTADrain(snapshot)
			if tc.wantFail == "" && err != nil || tc.wantFail != "" && (err == nil || !strings.Contains(err.Error(), tc.wantFail)) {
				t.Fatalf("drain decision = %v, want %q", err, tc.wantFail)
			}
		})
	}
}

func TestLKERequireLegacyOTADrainUsesLiveReadOnlyDatabaseAndFailsClosed(t *testing.T) {
	script := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
case "$*" in
  *"-n video-cloud-dev-platform exec postgresql-0 -- psql -X -A -t -v ON_ERROR_STOP=1 -U postgres -d video_cloud -c WITH legacy_releases AS"*)
    if [ "$FAKE_LEGACY_DB_FAIL" = "yes" ]; then echo 'relation missing' >&2; exit 1; fi
    printf '%s\n' "$FAKE_LEGACY_DB_JSON"
    ;;
  *) echo 'unexpected kubectl command' >&2; exit 2;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", script)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	checked := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	last := checked.Add(-50 * time.Hour)
	snapshot := otaLegacyDrainSnapshot{CheckedAt: checked, LegacyReleases: 1, LastActivityAt: &last}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LEGACY_DB_JSON", string(encoded))
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	if err := lkeRequireLegacyOTADrain(env); err != nil {
		t.Fatalf("drained live DB was rejected: %v", err)
	}
	t.Setenv("FAKE_LEGACY_DB_JSON", "not JSON")
	if err := lkeRequireLegacyOTADrain(env); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed DB result was accepted: %v", err)
	}
	t.Setenv("FAKE_LEGACY_DB_FAIL", "yes")
	if err := lkeRequireLegacyOTADrain(env); err == nil || !strings.Contains(err.Error(), "query live") {
		t.Fatalf("failed DB query was accepted: %v", err)
	}
}

func TestLKEOTACoreCutoverStopsBeforeWorkloadMutationWhenLegacyWorkRemains(t *testing.T) {
	script := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
case "$*" in
  *"-n video-cloud-dev-platform exec postgresql-0 -- psql -X -A -t -v ON_ERROR_STOP=1 -U postgres -d video_cloud -c WITH legacy_releases AS"*) printf '%s\n' "$FAKE_LEGACY_DB_JSON";;
  *) echo 'unexpected Kubernetes mutation before OTA drain' >&2; exit 2;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", script)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "true")
	checked := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	last := checked.Add(-50 * time.Hour)
	snapshot := otaLegacyDrainSnapshot{CheckedAt: checked, LegacyReleases: 1, OpenCampaigns: 1, LastActivityAt: &last}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LEGACY_DB_JSON", string(encoded))
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}
	err = lkeDeployWorkloads(provisionPaths{}, env, provisionOptions{workloads: []string{"video-cloud"}})
	if err == nil || !strings.Contains(err.Error(), "campaigns=1") {
		t.Fatalf("core cutover did not stop on live legacy work before workload apply: %v", err)
	}
}
