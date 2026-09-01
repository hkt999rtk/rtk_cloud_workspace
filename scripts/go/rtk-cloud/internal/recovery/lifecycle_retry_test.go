package recovery

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func TestInspectAndRetryOriginalOrSafetyBackup(t *testing.T) {
	e, f := newFake(t)
	ctx := context.Background()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	e.Config.Recipients = []string{identity.Recipient().String()}
	f.config = e.Config
	key := filepath.Join(privateTemp(t), "identity")
	writeTest(t, key, []byte(identity.String()+"\n"))
	if err := os.Mkdir(filepath.Join(e.SecretRoot, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(e.SecretRoot, "runtime", "credential")
	writeTest(t, credential, []byte("original"))
	id, err := e.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(e.Config.Directory, id+".age")
	if m, err := e.Inspect(ctx, file, key); err != nil || m.ID != id {
		t.Fatalf("inspect: %v", err)
	}
	if _, err := e.Inspect(ctx, file, "missing"); err == nil {
		t.Fatal("inspect without identity")
	}
	if e.Reapply(ctx, file, key) == nil {
		t.Fatal("retry accepted backup operation")
	}
	if err := e.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Resume(ctx, ""); err != nil {
		t.Fatal(err)
	}
	writeTest(t, credential, []byte("target-before-restore"))
	if _, err := e.Restore(ctx, file, key); err != nil {
		t.Fatal(err)
	}
	j, err := e.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e.Reapply(ctx, file, "missing") == nil {
		t.Fatal("retry without identity")
	}
	if err := e.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Reapply(ctx, file, key); err != nil {
		t.Fatal(err)
	}
	if f.replicas["api"] != 0 || f.replicas["openbao"] != 0 {
		t.Fatal("retry left writers running")
	}
	if err := e.Reapply(ctx, filepath.Join(e.Config.Directory, j.SafetyBackupID+".age"), key); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(credential)
	if err != nil || string(b) != "target-before-restore" {
		t.Fatal("safety rollback did not restore target state")
	}
	j, err = e.Load(ctx)
	if err != nil || j.Phase != "restored" || j.VerifiedAt != nil {
		t.Fatal("retry retained stale verification")
	}
	if e.Resume(ctx, "") == nil {
		t.Fatal("retry bypassed verify")
	}
	// An otherwise valid archive from another operation must never be applied.
	dir := privateTemp(t)
	if err := e.Capture(ctx, dir); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(e.Config.Directory, "unrelated.age")
	m := Manifest{Version: Version, Scope: "core", ID: "unrelated", Environment: e.Config.Environment, Stack: e.Config.Stack, Components: e.Config.Components, ConfigurationSHA256: InventoryFingerprint(e.Config)}
	if err := Pack(dir, unrelated, m, e.Config.Recipients); err != nil {
		t.Fatal(err)
	}
	if e.Reapply(ctx, unrelated, key) == nil {
		t.Fatal("accepted unrelated backup")
	}
	e.Config.Stack = "other"
	if _, err := e.Inspect(ctx, file, key); err == nil {
		t.Fatal("inspected cross-environment archive as compatible")
	}
}

func TestInvocationLocksAndCleanupFailure(t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"", "create", "delete"} {
		t.Run(failure, func(t *testing.T) {
			e, f := newFake(t)
			e.Exec = func(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
				if failure != "" && strings.Contains(strings.Join(argv, " "), " "+failure+" ") {
					return errors.New("fixture lock failure")
				}
				return f.exec(ctx, argv, in, out)
			}
			release, err := e.InvocationLock(ctx)
			p := filepath.Join(e.SecretRoot, "recovery-command.lock")
			if failure == "create" {
				if err == nil {
					t.Fatal("ignored cluster lock conflict")
				}
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("failed acquisition leaked local lock")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.InvocationLock(ctx); err == nil {
				t.Fatal("accepted concurrent command")
			}
			release()
			_, err = os.Stat(p)
			if failure == "delete" {
				if err != nil {
					t.Fatal("removed local lock after failed cleanup")
				}
			} else if !os.IsNotExist(err) {
				t.Fatal("retained released lock")
			}
		})
	}
}

func TestHelperRefusesMountedPVCAndRetainsCleanupFailure(t *testing.T) {
	for _, failure := range []string{"mounted", "create", "wait", "delete", "callback"} {
		t.Run(failure, func(t *testing.T) {
			e, f := newFake(t)
			e.Exec = func(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
				line := strings.Join(argv, " ")
				if failure == "mounted" && strings.Contains(line, " get pods ") {
					_, err := io.WriteString(out, `{"items":[{"spec":{"volumes":[{"persistentVolumeClaim":{"claimName":"openbao-data"}}]}}]}`)
					return err
				}
				if strings.Contains(line, " "+failure+" ") {
					return errors.New("fixture failure")
				}
				return f.exec(ctx, argv, in, out)
			}
			called := false
			err := e.helper(context.Background(), e.Config.Components[2], func(Component) error {
				called = true
				if failure == "callback" {
					return errors.New("callback failed")
				}
				return nil
			})
			if err == nil {
				t.Fatal("ignored helper failure")
			}
			if (failure == "mounted" || failure == "create" || failure == "wait") && called {
				t.Fatal("used unavailable PVC helper")
			}
		})
	}
}
