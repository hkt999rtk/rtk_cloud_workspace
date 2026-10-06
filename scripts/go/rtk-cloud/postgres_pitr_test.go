package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
)

func TestPostgresPITRDrillUsesSameIsolatedProfileAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c := postgresBackupTestConfig(t)
			c.Environment, c.Stack, c.Namespace = environment, "video-cloud-"+environment, "video-cloud-"+environment+"-platform"
			c.Worker.Environment, c.Worker.Stack, c.Worker.Remote.Prefix = environment, c.Stack, environment+"/postgres"
			c.Worker.Source.Host = "postgresql." + c.Namespace + ".svc.cluster.local"
			if err := c.validate(); err != nil {
				t.Fatal(err)
			}
			target := postgresbackup.PITRTarget{At: time.Now().UTC().Add(-time.Minute), Timeline: 1, MaxWALBytes: 1 << 30}
			if err := c.validatePITRTarget(target); err != nil {
				t.Fatal(err)
			}
			objects := postgresBackupDrillObjectsWithPITR(c, c.Stack+"-pgdrill-test", "backup-1", map[string]string{"identity.agekey": "private-fixture"}, &target)
			var job map[string]any
			for _, object := range objects {
				if object["metadata"].(map[string]any)["namespace"] != c.Stack+"-pgdrill-test" {
					t.Fatal("drill escaped isolated namespace")
				}
				if object["kind"] == "Job" {
					job = object
				}
			}
			if job == nil {
				t.Fatal("native PITR Job missing")
			}
			pod := job["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			if pod["automountServiceAccountToken"] != false {
				t.Fatal("restore inherited an API token")
			}
			raw, _ := json.Marshal(pod)
			for _, forbidden := range []string{c.SourcePVC, "source-password", "rtk-postgres-backup-credentials", "serviceAccountName"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("restore Pod contains %s", forbidden)
				}
			}
			for _, expected := range []string{"pitr-drill", "--recovery-time", target.At.Format(time.RFC3339Nano), "--recovery-timeline", "--max-wal-bytes"} {
				if !strings.Contains(string(raw), expected) {
					t.Fatalf("missing exact target argument %s", expected)
				}
			}
			tooLarge := target
			tooLarge.MaxWALBytes = c.Worker.MaxPlaintextBytes
			if c.validatePITRTarget(tooLarge) == nil {
				t.Fatal("PITR exceeded scratch headroom")
			}
		})
	}
}

func TestPostgresPITRControllerRejectsMissingOrMismatchedNativeEvidence(t *testing.T) {
	for _, mode := range []string{"success", "missing", "different-target", "after-target", "missing-lsn", "unbounded-rto", "future-receipt", "job-failure"} {
		t.Run(mode, func(t *testing.T) {
			m, f, _ := postgresControllerFixture(t)
			c := m.Config
			identity := filepath.Join(t.TempDir(), "identity.agekey")
			if err := os.WriteFile(identity, []byte("fixture-private-key"), 0600); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			target := postgresbackup.PITRTarget{At: now.Add(-time.Minute), Timeline: 1, MaxWALBytes: 1 << 30}
			observation := &postgresbackup.PITRObservation{Target: target, ReplayTime: target.At.Add(-time.Second), ReplayLSN: "0/1A000", StartedAt: now.Add(-time.Millisecond), FinishedAt: now, RTOms: 1}
			result := postgresbackup.Drill{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, BackupID: "backup-1", SystemIdentifier: c.Worker.Source.SystemIdentifier, PostgresImage: c.Worker.Source.PostgresImage, FinishedAt: now, Success: true, PITR: observation}
			switch mode {
			case "missing":
				result.PITR = nil
			case "different-target":
				observation.Target.Timeline = 2
			case "after-target":
				observation.ReplayTime = target.At.Add(time.Second)
			case "missing-lsn":
				observation.ReplayLSN = ""
			case "unbounded-rto":
				observation.RTOms = (5 * time.Hour).Milliseconds()
			case "future-receipt":
				observation.FinishedAt = now.Add(time.Hour)
			case "job-failure":
				f.job = `{"status":{"conditions":[{"type":"Failed","status":"True"}]}}`
			}
			raw, _ := json.Marshal(result)
			f.logs = string(raw)
			var recorded *postgresbackup.Drill
			m.RecordDrill = func(_ context.Context, _ postgresbackup.Config, d postgresbackup.Drill) error {
				recorded = &d
				return nil
			}
			m.ReadRemoteStatus = func(context.Context, postgresbackup.Config) (postgresbackup.Status, error) {
				return postgresbackup.Status{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, LatestDrill: recorded}, nil
			}
			err := m.drillWithPITR(context.Background(), "backup-1", "pitr-test", identity, &target)
			if (err == nil) != (mode == "success") {
				t.Fatalf("unexpected %s outcome: %v", mode, err)
			}
			if recorded == nil || recorded.Success != (mode == "success") || recorded.PITR == nil {
				t.Fatal("completion/failure evidence did not describe requested drill")
			}
			if !f.secretRemoved {
				t.Fatal("restore credentials remained after native evidence check")
			}
		})
	}
}
