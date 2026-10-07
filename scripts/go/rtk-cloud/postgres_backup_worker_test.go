package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/postgresbackup"
)

func TestPostgresBackupWorkerEntryFailsClosed(t *testing.T) {
	c := postgresBackupTestConfig(t)
	config := filepath.Join(t.TempDir(), "config.json")
	b, _ := json.Marshal(c)
	if err := os.WriteFile(config, b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_POSTGRES_RUNNER_SOURCE_IMAGE", c.Worker.Source.PostgresImage)
	for _, args := range [][]string{nil, {"run", "--unknown"}, {"run", "--config", config, "unexpected"}, {"run", "--config", "/nonexistent-backup-config"}, {"unsupported", "--config", config}, {"drill", "--config", config, "--id", "bad/id"}, {"run", "--config", config}} {
		if err := runPostgresBackupWorker(args); err == nil {
			t.Fatalf("unsafe entry accepted: %v", args)
		}
	}
	t.Setenv("RTK_POSTGRES_RUNNER_SOURCE_IMAGE", "postgres:16")
	if err := runPostgresBackupWorker([]string{"run", "--config", config}); err == nil || !strings.Contains(err.Error(), "image digest") {
		t.Fatalf("unpinned runner accepted: %v", err)
	}
	if err := os.WriteFile(config, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if runPostgresBackupWorker([]string{"run", "--config", config}) == nil {
		t.Fatal("malformed config accepted")
	}
}

func TestPostgresRestoreStartupIsolationAndFailureCleanup(t *testing.T) {
	for _, mode := range []string{"success", "control-failed", "control-invalid", "start-failed", "recovery-incomplete", "missing-database", "missing-role", "stop-failed"} {
		t.Run(mode, func(t *testing.T) {
			target := t.TempDir()
			pgdata := filepath.Join(target, "pgdata")
			if err := os.Mkdir(pgdata, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(pgdata, "postgresql.auto.conf"), []byte("shared_preload_libraries='unsafe'"), 0600); err != nil {
				t.Fatal(err)
			}
			started, stopped := false, false
			run := func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
				fail := errors.New("private fixture detail must remain redacted")
				switch args[0] {
				case "env":
					if mode == "control-failed" {
						return fail
					}
					if mode == "control-invalid" {
						return nil
					}
					_, err := io.WriteString(out, "max_connections setting: 800\nmax_worker_processes setting: 16\nmax_wal_senders setting: 12\nmax_prepared_xacts setting: 10\nmax_locks_per_xact setting: 128\n")
					return err
				case "pg_ctl":
					if args[len(args)-1] == "start" {
						if mode == "start-failed" {
							return fail
						}
						started = true
						return nil
					}
					stopped = true
					if mode == "stop-failed" {
						return fail
					}
					return nil
				case "psql":
					sql := args[len(args)-1]
					value := ""
					if sql == "SELECT pg_is_in_recovery();" {
						value = "f"
						if mode == "recovery-incomplete" {
							value = "t"
						}
					} else if strings.Contains(sql, "pg_catalog.pg_class") {
						value = "3"
						if mode == "missing-database" {
							return fail
						}
					} else {
						for _, check := range postgresBackupDataChecks() {
							if check.SQL == sql {
								value = check.Expected
							}
						}
						if mode == "missing-role" {
							value = ""
						}
					}
					_, err := io.WriteString(out, value)
					return err
				default:
					t.Fatalf("unexpected startup command %v", args)
					return nil
				}
			}
			err := postgresBackupVerifyRunningClusterWithExecutor(context.Background(), target, run)
			if (err == nil) != (mode == "success") {
				t.Fatalf("unexpected outcome %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "private fixture") {
				t.Fatal("native output exposed")
			}
			if started && !stopped {
				t.Fatal("failed verification left restored server running")
			}
			if started {
				conf, e := os.ReadFile(filepath.Join(target, "postgresql.conf"))
				if e != nil {
					t.Fatal(e)
				}
				for _, setting := range []string{"listen_addresses = ''", "archive_mode = off", "primary_conninfo = ''", "max_logical_replication_workers = 0", "default_transaction_read_only = on", "max_connections = 800"} {
					if !strings.Contains(string(conf), setting) {
						t.Fatalf("missing startup isolation %s", setting)
					}
				}
				auto, _ := os.ReadFile(filepath.Join(pgdata, "postgresql.auto.conf"))
				if len(auto) != 0 {
					t.Fatal("source runtime override retained")
				}
			}
		})
	}
}

func TestPostgresBackupWorkerPublicationAndFailureState(t *testing.T) {
	for _, mode := range []string{"run", "retry-upload", "prune", "lock-conflict", "capture-failed", "upload-failed", "prune-failed", "status-failed", "source-changed", "pending-upload", "release-failed", "status-write-failed"} {
		t.Run(mode, func(t *testing.T) {
			c := postgresBackupTestConfig(t)
			c.Worker.Directory = t.TempDir()
			now := time.Now().UTC().Truncate(time.Second)
			manifest := postgresbackup.Manifest{Version: 1, Scope: postgresbackup.Scope, ID: "postgres-new", Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, PostgresMajor: 16, PostgresImage: c.Worker.Source.PostgresImage, SystemIdentifier: c.Worker.Source.SystemIdentifier, StartedAt: now.Add(-time.Minute), FinishedAt: now, NativeManifestSHA256: strings.Repeat("a", 64)}
			old := manifest
			old.ID = "postgres-old"
			old.StartedAt = old.StartedAt.Add(-24 * time.Hour)
			old.FinishedAt = old.FinishedAt.Add(-24 * time.Hour)
			status := postgresbackup.Status{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.Worker.ClusterID, GeneratedAt: old.FinishedAt, Latest: &postgresbackup.Completion{Manifest: old}, CompletedCount: 1}
			locked, released, captured, uploaded, pruned, writes, calls := false, false, false, false, false, 0, 0
			fail := errors.New("fixture operation failed")
			writeJSON := func(path string, v any) {
				t.Helper()
				b, e := json.Marshal(v)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			writePending := func() {
				writeJSON(filepath.Join(c.Worker.Directory, manifest.ID+".manifest.json"), manifest)
				if err := os.WriteFile(filepath.Join(c.Worker.Directory, manifest.ID+".age"), []byte("encrypted fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "retry-upload" || mode == "pending-upload" {
				writePending()
			}
			m := postgresBackupManager{Config: c, Kubeconfig: "fixture-kubeconfig", Exec: func(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
				calls++
				if !locked {
					t.Fatal("worker accessed shared cluster state outside lock")
				}
				command := " " + strings.Join(args, " ") + " "
				switch {
				case strings.Contains(command, " get configmap "):
					b, _ := json.Marshal(status)
					return json.NewEncoder(out).Encode(map[string]any{"data": map[string]string{"status.json": string(b)}})
				case strings.Contains(command, " get pod "):
					image := c.Worker.Source.PostgresImage
					if mode == "source-changed" {
						image = "postgres@sha256:" + strings.Repeat("c", 64)
					}
					return json.NewEncoder(out).Encode(map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"persistentVolumeClaim": map[string]string{"claimName": c.SourcePVC}}}}, "status": map[string]any{"containerStatuses": []any{map[string]any{"name": c.SourceContainer, "imageID": image, "ready": true}}}})
				case strings.Contains(command, " apply "):
					writes++
					if mode == "status-write-failed" {
						return fail
					}
					var obj struct{ Data map[string]string }
					if json.NewDecoder(in).Decode(&obj) != nil || json.Unmarshal([]byte(obj.Data["status.json"]), &status) != nil {
						t.Fatal("invalid status publication")
					}
					if status.LastAttempt == nil || status.GeneratedAt.Before(status.LastAttempt.StartedAt) {
						t.Fatal("attempt timestamp exceeds status timestamp")
					}
					return nil
				default:
					t.Fatalf("unexpected worker command %s", command)
					return nil
				}
			}}
			ops := postgresBackupWorkerOperations{
				AcquireLock: func(context.Context) (func() error, error) {
					if mode == "lock-conflict" {
						return nil, fail
					}
					locked = true
					return func() error {
						locked = false
						released = true
						if mode == "release-failed" {
							return fail
						}
						return nil
					}, nil
				},
				Capture: func(context.Context, postgresbackup.Config) (postgresbackup.CaptureResult, error) {
					if !locked {
						t.Fatal("capture without lock")
					}
					captured = true
					if mode == "capture-failed" {
						return postgresbackup.CaptureResult{}, fail
					}
					writePending()
					return postgresbackup.CaptureResult{Manifest: manifest, File: filepath.Join(c.Worker.Directory, manifest.ID+".age")}, nil
				},
				Upload: func(_ context.Context, _ postgresbackup.Config, m postgresbackup.Manifest, file string) error {
					if !locked {
						t.Fatal("upload without lock")
					}
					uploaded = true
					if mode == "upload-failed" {
						return fail
					}
					if _, err := os.Stat(file); err != nil {
						t.Fatal("missing retry artifact")
					}
					writeJSON(filepath.Join(c.Worker.Directory, m.ID+".complete.json"), postgresbackup.Completion{Manifest: m})
					return nil
				},
				Prune: func(context.Context, postgresbackup.Config, bool) (postgresbackup.PrunePlan, error) {
					if !locked {
						t.Fatal("prune without lock")
					}
					pruned = true
					if mode == "prune-failed" {
						return postgresbackup.PrunePlan{}, fail
					}
					return postgresbackup.PrunePlan{}, nil
				},
				Status: func(context.Context, postgresbackup.Config) (postgresbackup.Status, error) {
					if mode == "status-failed" {
						return postgresbackup.Status{}, fail
					}
					if uploaded {
						status.Latest = &postgresbackup.Completion{Manifest: manifest}
						status.CompletedCount = 2
					}
					return status, nil
				},
			}
			action := "run"
			if mode == "retry-upload" || mode == "prune" {
				action = mode
			}
			err := m.runWorker(context.Background(), action, manifest.ID, ops)
			wantSuccess := mode == "run" || mode == "retry-upload" || mode == "prune"
			if (err == nil) != wantSuccess {
				t.Fatalf("error=%v success=%v", err, wantSuccess)
			}
			if mode == "lock-conflict" {
				if calls != 0 || writes != 0 || released {
					t.Fatal("competing worker changed shared status")
				}
				return
			}
			if !released || locked {
				t.Fatal("worker leaked lock")
			}
			if mode == "status-write-failed" {
				return
			}
			if writes != 2 || status.LastAttempt == nil || status.LastAttempt.FinishedAt.IsZero() {
				t.Fatalf("missing terminal status: %#v", status)
			}
			if wantSuccess || mode == "release-failed" {
				if status.LastAttempt.Status != "succeeded" {
					t.Fatal("successful publication marked failed")
				}
			} else if status.LastAttempt.Status != "failed" {
				t.Fatal("failed operation marked successful")
			}
			if mode == "source-changed" || mode == "pending-upload" {
				if captured {
					t.Fatal("capture began despite failed preflight")
				}
			}
			if mode == "upload-failed" {
				if _, err := os.Stat(filepath.Join(c.Worker.Directory, manifest.ID+".age")); err != nil {
					t.Fatal("failed upload lost encrypted retry artifact")
				}
			}
			published := uploaded && mode != "upload-failed"
			if published {
				if status.Latest.Manifest.ID != manifest.ID {
					t.Fatal("later error discarded successfully published backup")
				}
				if _, err := os.Stat(filepath.Join(c.Worker.Directory, manifest.ID+".age")); !os.IsNotExist(err) {
					t.Fatal("published artifact not cleaned from scratch")
				}
			} else if status.Latest.Manifest.ID != old.ID {
				t.Fatal("failure discarded previous successful backup")
			}
			if mode == "run" && !pruned {
				t.Fatal("daily successful backup did not run retention")
			}
		})
	}
}
