package recovery

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestClusterLockExcludesCoreAndOnlineBackups(t *testing.T) {
	e, f := newFake(t)
	release, err := acquireClusterLock(context.Background(), e.kube, e.Config.LockNamespace, "scheduled", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.InvocationLock(context.Background()); err == nil {
		t.Fatal("core competed with scheduled backup")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal("idempotent release", err)
	}
	core, err := e.InvocationLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireClusterLock(context.Background(), e.kube, e.Config.LockNamespace, "scheduled", true); err == nil {
		t.Fatal("scheduled backup competed with core")
	}
	core()
	if f.commandLock != nil {
		t.Fatal("command lock leaked")
	}
}

func TestOnlineBackupRejectsMaintenanceAndUnreadableState(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		e, f := newFake(t)
		f.journal = "{}"
		if unreadable {
			original := e.Exec
			e.Exec = func(ctx context.Context, argv []string, in io.Reader, out io.Writer) error {
				if strings.Contains(strings.Join(argv, " "), " get configmap ") {
					return errors.New("forbidden")
				}
				return original(ctx, argv, in, out)
			}
		}
		if _, err := acquireClusterLock(context.Background(), e.kube, e.Config.LockNamespace, "scheduled", true); err == nil {
			t.Fatal("maintenance state ignored")
		}
		if f.commandLock != nil {
			t.Fatal("refused job retained lock")
		}
		if f.journal != "{}" {
			t.Fatal("changed maintenance journal")
		}
	}
}

func TestClusterLockReleaseUsesUIDPreconditionAndSurvivesCancel(t *testing.T) {
	e, f := newFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	release, err := acquireClusterLock(ctx, e.kube, e.Config.LockNamespace, "scheduled", true)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if f.commandLock != nil {
		t.Fatal("cancel prevented lock release")
	}
	found := false
	for _, line := range f.events {
		if strings.Contains(line, "delete --raw /api/v1/namespaces/") {
			found = true
		}
	}
	if !found {
		t.Fatal("release must use atomic UID-preconditioned delete")
	}
}

func TestClusterLockInClusterModeNeverUsesDefaultControllerContext(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if _, err := AcquireClusterLock(context.Background(), "kubectl", "", "platform", "worker", true); err == nil {
		t.Fatal("missing explicit cluster accepted")
	}
}
