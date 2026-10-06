package postgresbackup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPITRNativeVerificationRequiresPauseDataChecksAndShutdown(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c, _, identity := workerFixture(t)
			c.Environment, c.Stack, c.Remote.Prefix = environment, "video-cloud-"+environment, environment+"/postgres-physical"
			preparer := pitrFixtureEngine(t, c)
			base, err := preparer.Capture(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			wal := "000000010000000000000002"
			source := filepath.Join(filepath.Dir(identity), wal)
			workerWrite(t, source, []byte("authenticated WAL"))
			if _, err := preparer.ArchiveWAL(context.Background(), c, wal, source); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"success", "configuration-drift", "startup-failed", "promoted", "writable", "replay-time-missing", "after-target", "bad-lsn", "data-failed", "shutdown-failed", "query-failed", "no-data-adapter"} {
				t.Run(mode, func(t *testing.T) {
					at := time.Now().UTC()
					root := filepath.Join(filepath.Dir(c.Directory), "verify-"+mode)
					prepared, err := preparer.PreparePITR(context.Background(), c, base.File, identity, root, PITRTarget{at, 1, 1024})
					if err != nil {
						t.Fatal(err)
					}
					started, stopped, checked := false, false, false
					verifier := Engine{Exec: func(ctx context.Context, args, env []string, out io.Writer) error {
						failure := errors.New("private backend output must be suppressed")
						if args[0] == "pg_ctl" {
							if args[len(args)-1] == "start" {
								started = true
								if mode == "startup-failed" {
									return failure
								}
								return nil
							}
							stopped = true
							if mode == "shutdown-failed" {
								return failure
							}
							return nil
						}
						if args[0] != "psql" {
							t.Fatalf("unexpected native command %v", args)
						}
						value := ""
						if mode == "query-failed" {
							return failure
						}
						switch sql := args[len(args)-1]; {
						case strings.HasPrefix(sql, "SELECT pg_is_in_recovery"):
							value = "t|t|on|on"
							if mode == "promoted" {
								value = "f|f|on|on"
							}
							if mode == "writable" {
								value = "t|t|off|off"
							}
						case strings.Contains(sql, "pg_last_xact_replay_timestamp"):
							value = at.Add(-time.Second).Format(time.RFC3339Nano)
							if mode == "replay-time-missing" {
								value = ""
							}
							if mode == "after-target" {
								value = at.Add(time.Second).Format(time.RFC3339Nano)
							}
						case sql == "SELECT pg_last_wal_replay_lsn()":
							value = "0/1AFD00"
							if mode == "bad-lsn" {
								value = "missing"
							}
						case sql == "SELECT maintained_data_check":
							value = "verified"
						}
						_, err := io.WriteString(out, value)
						return err
					}}
					if mode == "configuration-drift" {
						if err := os.WriteFile(filepath.Join(root, "pgdata", "postgresql.conf"), []byte("shared_preload_libraries='unreviewed'"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					var check func(RestoredQuery) error = func(query RestoredQuery) error {
						checked = true
						if mode == "data-failed" {
							return errors.New("private data")
						}
						value, err := query("postgres", "SELECT maintained_data_check")
						if err != nil || value != "verified" {
							return errors.New("invalid restored data")
						}
						return nil
					}
					if mode == "no-data-adapter" {
						check = nil
					}
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					observation, err := verifier.VerifyPreparedPITR(ctx, c, root, prepared, check)
					if (err == nil) != (mode == "success") {
						t.Fatalf("unexpected %s outcome: %v", mode, err)
					}
					if err != nil && strings.Contains(err.Error(), "private") {
						t.Fatal("private native output escaped")
					}
					if started && !stopped {
						t.Fatal("failed native verification left target running")
					}
					if (mode == "configuration-drift" || mode == "no-data-adapter") && started {
						t.Fatal("unqualified target started")
					}
					if mode == "success" && (!checked || observation.ReplayLSN == "" || observation.FinishedAt.Before(observation.StartedAt)) {
						t.Fatal("native completion evidence missing")
					}
				})
			}
		})
	}
}
