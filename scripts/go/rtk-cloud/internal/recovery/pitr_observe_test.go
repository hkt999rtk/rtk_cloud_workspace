package recovery

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func observeFixture(t *testing.T) (BaseBackupEngine, string, PITRConnection, pitrRuntime) {
	t.Helper()
	c := baseConfigFixture(t)
	dest := privateTemp(t)
	conn := PITRConnection{SocketDirectory: privateTemp(t), Port: 5432, User: "postgres", Database: "postgres"}
	p := PITRPlan{Version: 1, TargetLSN: "0/3000100", TargetTimeline: 2, Executable: "/opt/rtk/bin/rtk-cloud", WALConfigFile: "/private/wal.json"}
	m := Manifest{Version: Version, Scope: physicalScope, ID: "fixture", Environment: c.WAL.Environment, Stack: c.WAL.Stack, ConfigurationSHA256: Digest(c)}
	if err := WriteJSON(filepath.Join(dest, "verified.json"), m); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(filepath.Join(dest, "pitr.json"), pitrPreparation{"prepared-not-replayed", "fixture", p}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	runtime := pitrRuntime{SystemIdentifier: c.WAL.SystemIdentifier, DataDirectory: filepath.Join(dest, "pgdata"), ServerVersion: 160015, InRecovery: true, PauseState: "paused", ReplayLSN: "0/3000140", TargetLSN: p.TargetLSN, TargetTimeline: "2", TargetAction: "pause", TargetInclusive: "on", ListenAddresses: &empty, ArchiveMode: "off", ReadOnly: "on"}
	return BaseBackupEngine{Config: c}, dest, conn, runtime
}
func TestPITRObserveLocalReadOnly(t *testing.T) {
	e, dest, conn, runtime := observeFixture(t)
	e.Exec = func(ctx context.Context, argv []string, _ io.Reader, out io.Writer) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing bounded deadline")
		}
		for _, arg := range []string{"--no-psqlrc", "--no-password", "--host=" + conn.SocketDirectory, "--username=postgres", "--dbname=postgres", "--command=" + pitrObservationSQL} {
			found := false
			for _, got := range argv {
				if got == arg {
					found = true
				}
			}
			if !found {
				t.Fatal("missing protected invocation", arg)
			}
		}
		if filepath.Base(argv[0]) != "psql" || !strings.HasPrefix(pitrObservationSQL, "BEGIN READ ONLY;") {
			t.Fatal("unexpected query")
		}
		return json.NewEncoder(out).Encode(runtime)
	}
	before, err := os.ReadFile(filepath.Join(dest, "pitr.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.ObservePITR(context.Background(), "fixture", dest, conn)
	if err != nil || got.Status != "paused-target-observed" || got.BackupID != "fixture" || got.TargetTimeline != 2 || got.ObservedAt.IsZero() {
		t.Fatalf("%+v %v", got, err)
	}
	after, _ := os.ReadFile(filepath.Join(dest, "pitr.json"))
	if string(before) != string(after) {
		t.Fatal("observation changed preparation evidence")
	}
}
func TestPITRObserveRejectsRuntimeMismatch(t *testing.T) {
	for name, mutate := range map[string]func(*pitrRuntime){
		"cluster":           func(r *pitrRuntime) { r.SystemIdentifier = "wrong" },
		"directory":         func(r *pitrRuntime) { r.DataDirectory = "/source" },
		"version":           func(r *pitrRuntime) { r.ServerVersion = 170000 },
		"promoted":          func(r *pitrRuntime) { r.InRecovery = false },
		"pause-requested":   func(r *pitrRuntime) { r.PauseState = "pause requested" },
		"replay-short":      func(r *pitrRuntime) { r.ReplayLSN = "0/3000000" },
		"target":            func(r *pitrRuntime) { r.TargetLSN = "0/3000000" },
		"timeline":          func(r *pitrRuntime) { r.TargetTimeline = "latest" },
		"action":            func(r *pitrRuntime) { r.TargetAction = "promote" },
		"exclusive":         func(r *pitrRuntime) { r.TargetInclusive = "off" },
		"tcp":               func(r *pitrRuntime) { tcp := "localhost"; r.ListenAddresses = &tcp },
		"missing-tcp-state": func(r *pitrRuntime) { r.ListenAddresses = nil },
		"archiving":         func(r *pitrRuntime) { r.ArchiveMode = "on" },
		"writable":          func(r *pitrRuntime) { r.ReadOnly = "off" },
	} {
		t.Run(name, func(t *testing.T) {
			e, dest, conn, r := observeFixture(t)
			mutate(&r)
			e.Exec = func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
				return json.NewEncoder(out).Encode(r)
			}
			if _, err := e.ObservePITR(context.Background(), "fixture", dest, conn); err == nil {
				t.Fatal("invalid runtime accepted")
			}
		})
	}
}
func TestPITRObserveRejectsInputsBeforeConnecting(t *testing.T) {
	for _, name := range []string{"tcp", "connection-string", "wrong-id", "public-metadata", "missing-directory", "symlink-metadata", "bad-plan", "wrong-scope"} {
		t.Run(name, func(t *testing.T) {
			e, dest, conn, _ := observeFixture(t)
			id := "fixture"
			switch name {
			case "tcp":
				conn.SocketDirectory = "127.0.0.1"
			case "connection-string":
				conn.Database = "host=source"
			case "wrong-id":
				id = "other"
			case "public-metadata":
				if err := os.Chmod(filepath.Join(dest, "pitr.json"), 0644); err != nil {
					t.Fatal(err)
				}
			case "missing-directory":
				dest = filepath.Join(dest, "absent")
			case "symlink-metadata":
				path := filepath.Join(dest, "pitr.json")
				if err := os.Rename(path, path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".real", path); err != nil {
					t.Fatal(err)
				}
			case "bad-plan":
				if err := WriteJSON(filepath.Join(dest, "pitr.json"), pitrPreparation{Status: "prepared-not-replayed", BackupID: "fixture"}); err != nil {
					t.Fatal(err)
				}
			case "wrong-scope":
				e.Config.WAL.Stack = "different"
			}
			e.Exec = func(context.Context, []string, io.Reader, io.Writer) error {
				t.Fatal("connected with invalid input")
				return nil
			}
			if _, err := e.ObservePITR(context.Background(), id, dest, conn); err == nil {
				t.Fatal("invalid input accepted")
			}
			if name == "missing-directory" {
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatal("observation created directory")
				}
			}
		})
	}
}

func TestPITRObserveRejectsInvalidOutputAndCancellation(t *testing.T) {
	for _, body := range []string{"{}", "null", "{} {}", strings.Repeat(" ", 17<<10)} {
		e, dest, conn, _ := observeFixture(t)
		e.Exec = func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
			_, err := io.WriteString(out, body)
			return err
		}
		if got, err := e.ObservePITR(context.Background(), "fixture", dest, conn); err == nil || got.Status != "" {
			t.Fatalf("invalid output accepted: %+v %v", got, err)
		}
	}
	e, dest, conn, r := observeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.Exec = func(_ context.Context, _ []string, _ io.Reader, out io.Writer) error {
		cancel()
		return json.NewEncoder(out).Encode(r)
	}
	if got, err := e.ObservePITR(ctx, "fixture", dest, conn); err == nil || got.Status != "" {
		t.Fatalf("cancelled observation accepted: %+v %v", got, err)
	}
}
