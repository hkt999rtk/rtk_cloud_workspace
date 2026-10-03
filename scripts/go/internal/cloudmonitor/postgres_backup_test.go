package cloudmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type backupStatusRunner struct {
	t     *testing.T
	raw   []byte
	err   error
	calls int
}

func (r *backupStatusRunner) Run(_ context.Context, command string, args []string, stdin []byte, env []string) ([]byte, int, error) {
	r.t.Helper()
	r.calls++
	if command != "kubectl" || strings.Join(args, " ") != "--kubeconfig /fixture/kubeconfig --request-timeout=5s -n video-cloud-dev-platform get configmap rtk-postgres-backup-status -o json" || len(stdin) != 0 || len(env) != 0 {
		r.t.Fatalf("unexpected operation: %s %v", command, args)
	}
	if r.err != nil {
		return r.raw, 1, r.err
	}
	return r.raw, 0, nil
}

func backupStatusFixture(now time.Time) map[string]any {
	return map[string]any{
		"version": 1, "environment": "dev", "stack": "video-cloud-dev", "cluster_id": "main", "generated_at": now, "completed_count": 1,
		"latest":       map[string]any{"manifest": map[string]any{"version": 1, "scope": "postgres-physical", "environment": "dev", "stack": "video-cloud-dev", "cluster_id": "main", "backup_id": "backup-1", "finished_at": now.Add(-time.Hour)}, "encrypted_artifact": map[string]any{"path": "backup-1.age", "size": 1024, "sha256": strings.Repeat("a", 64)}},
		"last_attempt": map[string]any{"status": "succeeded", "started_at": now.Add(-2 * time.Hour), "finished_at": now.Add(-time.Hour)},
		"latest_drill": map[string]any{"version": 1, "environment": "dev", "stack": "video-cloud-dev", "cluster_id": "main", "backup_id": "backup-1", "finished_at": now.Add(-30 * time.Minute), "success": true},
	}
}

func backupConfigMap(t *testing.T, state map[string]any) []byte {
	t.Helper()
	status, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"metadata": map[string]string{"name": "rtk-postgres-backup-status", "namespace": "video-cloud-dev-platform"}, "data": map[string]string{"status.json": string(status)}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPostgresBackupEvidence(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	cfg := Config{Environment: "dev", Stack: "video-cloud-dev", PostgresBackups: []PostgresBackupCheck{{Name: "main", Required: true, Enabled: true, Namespace: "video-cloud-dev-platform", ClusterID: "main", StatusConfigMap: "rtk-postgres-backup-status"}}}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		want   [3]Status
	}{
		{"verified", func(map[string]any) {}, [3]Status{Pass, Pass, Pass}},
		{"26h-boundary", func(s map[string]any) {
			s["latest"].(map[string]any)["manifest"].(map[string]any)["finished_at"] = now.Add(-26 * time.Hour)
		}, [3]Status{Pass, Pass, Pass}},
		{"warning", func(s map[string]any) {
			s["latest"].(map[string]any)["manifest"].(map[string]any)["finished_at"] = now.Add(-27 * time.Hour)
		}, [3]Status{Warn, Pass, Pass}},
		{"28h-boundary", func(s map[string]any) {
			s["latest"].(map[string]any)["manifest"].(map[string]any)["finished_at"] = now.Add(-28 * time.Hour)
		}, [3]Status{Warn, Pass, Pass}},
		{"capture-not-publication", func(s map[string]any) {
			s["latest"].(map[string]any)["manifest"].(map[string]any)["finished_at"] = now.Add(-29 * time.Hour)
		}, [3]Status{Fail, Pass, Pass}},
		{"no-success", func(s map[string]any) { delete(s, "latest"); s["completed_count"] = 0 }, [3]Status{Fail, Pass, Pass}},
		{"inconsistent-success", func(s map[string]any) { delete(s, "latest") }, [3]Status{Unknown, Pass, Pass}},
		{"no-completion-artifact", func(s map[string]any) { delete(s["latest"].(map[string]any), "encrypted_artifact") }, [3]Status{Unknown, Pass, Pass}},
		{"wrong-backup-scope", func(s map[string]any) { s["latest"].(map[string]any)["manifest"].(map[string]any)["scope"] = "core" }, [3]Status{Unknown, Pass, Pass}},
		{"wrong-cluster", func(s map[string]any) { s["cluster_id"] = "other" }, [3]Status{Unknown, Unknown, Unknown}},
		{"wrong-environment", func(s map[string]any) { s["environment"] = "prod" }, [3]Status{Unknown, Unknown, Unknown}},
		{"wrong-version", func(s map[string]any) { s["version"] = 2 }, [3]Status{Unknown, Unknown, Unknown}},
		{"future-status", func(s map[string]any) { s["generated_at"] = now.Add(time.Hour) }, [3]Status{Unknown, Unknown, Unknown}},
		{"negative-count", func(s map[string]any) { s["completed_count"] = -1 }, [3]Status{Unknown, Unknown, Unknown}},
		{"missing-count", func(s map[string]any) { delete(s, "completed_count") }, [3]Status{Unknown, Unknown, Unknown}},
		{"future-capture", func(s map[string]any) {
			s["latest"].(map[string]any)["manifest"].(map[string]any)["finished_at"] = now.Add(time.Hour)
		}, [3]Status{Unknown, Pass, Pass}},
		{"foreign-manifest", func(s map[string]any) {
			s["latest"].(map[string]any)["manifest"].(map[string]any)["environment"] = "prod"
		}, [3]Status{Unknown, Pass, Pass}},
		{"failed-latest-attempt", func(s map[string]any) { s["last_attempt"].(map[string]any)["status"] = "failed" }, [3]Status{Pass, Fail, Pass}},
		{"no-attempt", func(s map[string]any) { delete(s, "last_attempt") }, [3]Status{Pass, Unknown, Pass}},
		{"unknown-attempt", func(s map[string]any) { s["last_attempt"].(map[string]any)["status"] = "secret-must-not-appear" }, [3]Status{Pass, Unknown, Pass}},
		{"running", func(s map[string]any) {
			a := s["last_attempt"].(map[string]any)
			a["status"] = "running"
			delete(a, "finished_at")
		}, [3]Status{Pass, Pass, Pass}},
		{"timed-out", func(s map[string]any) {
			a := s["last_attempt"].(map[string]any)
			a["status"] = "running"
			a["started_at"] = now.Add(-5 * time.Hour)
			delete(a, "finished_at")
		}, [3]Status{Pass, Fail, Pass}},
		{"skipped", func(s map[string]any) { s["last_attempt"].(map[string]any)["status"] = "skipped" }, [3]Status{Pass, Warn, Pass}},
		{"invalid-attempt-end", func(s map[string]any) { s["last_attempt"].(map[string]any)["finished_at"] = now.Add(-3 * time.Hour) }, [3]Status{Pass, Unknown, Pass}},
		{"no-drill", func(s map[string]any) { delete(s, "latest_drill") }, [3]Status{Pass, Pass, Unknown}},
		{"no-drill-result", func(s map[string]any) { delete(s["latest_drill"].(map[string]any), "success") }, [3]Status{Pass, Pass, Unknown}},
		{"failed-drill", func(s map[string]any) { s["latest_drill"].(map[string]any)["success"] = false }, [3]Status{Pass, Pass, Fail}},
		{"foreign-drill", func(s map[string]any) { s["latest_drill"].(map[string]any)["cluster_id"] = "other" }, [3]Status{Pass, Pass, Unknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := backupStatusFixture(now)
			tc.change(state)
			runner := &backupStatusRunner{t: t, raw: backupConfigMap(t, state)}
			out := CollectPostgresBackups(context.Background(), cfg, Runtime{Kubeconfig: "/fixture/kubeconfig"}, runner, now)
			if runner.calls != 1 || len(out.Results) != 3 {
				t.Fatal("missing evidence checks", out)
			}
			for i, expected := range tc.want {
				if out.Results[i].Status != expected {
					t.Fatalf("check %d: got %s, want %s (%s)", i, out.Results[i].Status, expected, out.Results[i].Reason)
				}
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "secret-must-not-appear") {
				t.Fatal("untrusted status leaked")
			}
		})
	}
	t.Run("unreadable-and-malformed", func(t *testing.T) {
		for _, runner := range []*backupStatusRunner{{t: t, err: errors.New("secret-must-not-appear")}, {t: t, raw: []byte("secret-must-not-appear")}, {t: t, raw: []byte(`{"data":{"status.json":"{}"}}`)}, {t: t, raw: make([]byte, 1<<20+1)}} {
			out := CollectPostgresBackups(context.Background(), cfg, Runtime{Kubeconfig: "/fixture/kubeconfig"}, runner, now)
			for _, result := range out.Results {
				if result.Status != Unknown || strings.Contains(result.Reason, "secret-must-not-appear") {
					t.Fatal(result)
				}
			}
		}
	})
	t.Run("disabled-no-read", func(t *testing.T) {
		cfg.PostgresBackups[0].Enabled = false
		runner := &backupStatusRunner{t: t}
		out := CollectPostgresBackups(context.Background(), cfg, Runtime{}, runner, now)
		if runner.calls != 0 {
			t.Fatal("disabled probe read cluster")
		}
		for _, result := range out.Results {
			if result.Status != NotApplicable {
				t.Fatal(result)
			}
		}
	})
}

func TestPostgresBackupConfigBoundaries(t *testing.T) {
	dir := t.TempDir()
	inv := Inventory{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", SourceFingerprint: "fixture", Targets: []WorkloadTarget{{ID: "pg", Name: "postgresql", Service: "postgres", Namespace: "video-cloud-dev-platform", Kind: "statefulset", Enabled: true, Required: true}}}
	raw, _ := json.Marshal(inv)
	if err := os.WriteFile(filepath.Join(dir, "inventory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*PostgresBackupCheck)
		valid  bool
	}{
		{"defaults", func(*PostgresBackupCheck) {}, true},
		{"foreign-namespace", func(c *PostgresBackupCheck) { c.Namespace = "video-cloud-prod-platform" }, false},
		{"missing-cluster", func(c *PostgresBackupCheck) { c.ClusterID = "" }, false},
		{"missing-source", func(c *PostgresBackupCheck) { c.StatusConfigMap = "" }, false},
		{"invalid-source", func(c *PostgresBackupCheck) { c.StatusConfigMap = "../../secrets" }, false},
		{"threshold-order", func(c *PostgresBackupCheck) { c.WarnAgeHours = 30 }, false},
		{"negative-threshold", func(c *PostgresBackupCheck) { c.FailAgeHours = -1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := PostgresBackupCheck{Name: "main", Required: true, Enabled: true, Namespace: "video-cloud-dev-platform", ClusterID: "main", StatusConfigMap: "rtk-postgres-backup-status"}
			tc.change(&p)
			cfg := Config{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", InventoryFile: "inventory.json", PostgresBackups: []PostgresBackupCheck{p}}
			raw, _ := json.Marshal(cfg)
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := LoadConfig(path, "dev")
			if (err == nil) != tc.valid {
				t.Fatal("unexpected config result", err)
			}
			if tc.valid && (loaded.PostgresBackups[0].WarnAgeHours != 26 || loaded.PostgresBackups[0].FailAgeHours != 28) {
				t.Fatal("missing thresholds")
			}
		})
	}
	coverage := ConfigurationCoverage(Config{}, inv, time.Now())
	found := false
	for _, r := range coverage.Results {
		if r.CheckID == "coverage/postgres-backup" && r.Status == Unknown {
			found = true
		}
	}
	if !found {
		t.Fatal("missing backup configuration silently passed")
	}
}
