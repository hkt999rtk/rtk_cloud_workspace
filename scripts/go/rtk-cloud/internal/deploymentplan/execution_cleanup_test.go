package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type executionCleanupContextKey struct{}

type cleanupBackend struct {
	memoryBackend
	root             string
	t                *testing.T
	releaseCalls     int
	acquireErr       error
	releaseErr       error
	replacementLease bool
	writeErr         error
	waitHook         func(context.Context, Resource) error
}

type cleanupLease struct {
	ExecutionLease
	backend *cleanupBackend
}

func (b *cleanupBackend) AcquireExecution(ctx context.Context, p *Plan) (ExecutionLease, error) {
	if b.acquireErr != nil {
		return nil, b.acquireErr
	}
	lease, err := b.memoryBackend.AcquireExecution(ctx, p)
	if err != nil {
		return nil, err
	}
	return &cleanupLease{lease, b}, nil
}

func (l *cleanupLease) Release(ctx context.Context) error {
	b := l.backend
	b.releaseCalls++
	deadline, bounded := ctx.Deadline()
	remaining := time.Until(deadline)
	if ctx.Err() != nil || !bounded || remaining <= 0 || remaining > 30*time.Second || ctx.Value(executionCleanupContextKey{}) != nil {
		b.t.Fatal("lease cleanup did not use a fresh, live, bounded background context")
	}
	active := filepath.Join(b.root, "deployment", ".execution-active")
	if displaced := filepath.Join(b.root, "displaced-owner"); pathExistsForCleanup(displaced) {
		active = displaced
	}
	if running, err := LockExecutionOwner(active, false); err == nil {
		running.Close()
		b.t.Fatal("the OS ownership lock was released before lease cleanup")
	}
	if b.releaseErr != nil {
		return b.releaseErr
	}
	if b.replacementLease {
		return errors.New("private-replacement-lease must not be deleted")
	}
	return l.ExecutionLease.Release(ctx)
}

func pathExistsForCleanup(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (b *cleanupBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	actual, err := b.memoryBackend.Write(ctx, action, body, dry)
	if !dry && err == nil && b.writeErr != nil {
		return actual, b.writeErr // Simulate a persisted API write with a failed response.
	}
	return actual, err
}

func (b *cleanupBackend) Wait(ctx context.Context, r Resource) error {
	if b.waitHook != nil {
		if err := b.waitHook(ctx, r); err != nil {
			return err
		}
	}
	return b.memoryBackend.Wait(ctx, r)
}

func executionOwnerArchives(t *testing.T, root, sha string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "deployment", "rollback", sha, "execution-owner-*", "owner"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func readExecutionJournal(t *testing.T, root, sha string) Journal {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "deployment", "rollback", sha, "journal.json"))
	var journal Journal
	if err != nil || json.Unmarshal(raw, &journal) != nil || journal.PlanSHA256 != sha {
		t.Fatal("original private execution journal was lost or changed", err)
	}
	return journal
}

func TestExecutionCleanupReleasesStoppedOwnersAndPreservesEveryOutcome(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"success", "prewrite-failure", "persisted-write-error", "timeout", "cancelled", "failed-acceptance"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				in, workload, live := fixture(t, environment, "Deployment")
				Map(workload.Object["spec"])["replicas"] = float64(2)
				namespace := Object{"apiVersion": "v1", "kind": "Namespace", "metadata": Object{"name": in.Stack + "-video-cloud", "uid": "namespace-owner", "resourceVersion": "1", "labels": Object{"rtk.realtek.com/stack": in.Stack}}}
				namespaceID := ID(namespace)
				workload.Dependencies = []string{namespaceID}
				plan, err := Compile(in, []Definition{{Object: Clone(namespace), Phase: "namespace"}, workload}, []Object{namespace, live})
				if err != nil || plan.Resources[0].Action != "update" {
					t.Fatal("namespace update fixture could not be compiled", err)
				}
				sha := plan.SHA256()
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				backend := &cleanupBackend{memoryBackend: memoryBackend{objects: map[string]Object{namespaceID: Clone(namespace), ID(live): Clone(live)}}, root: root, t: t}
				ctx := context.WithValue(context.Background(), executionCleanupContextKey{}, "original-context")
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				if mode == "timeout" {
					var expire context.CancelFunc
					ctx, expire = context.WithTimeout(ctx, 250*time.Millisecond)
					defer expire()
				}
				qualified := func(context.Context, *Plan) error { return nil }
				fence := qualified
				acceptCalls := 0
				accept := func(context.Context, *Plan) error { acceptCalls++; return nil }
				writeFailure := errors.New("write response failed after persistence")
				switch mode {
				case "prewrite-failure":
					fence = func(context.Context, *Plan) error { return errors.New("input fence failed") }
				case "persisted-write-error":
					backend.writeErr = writeFailure
				case "timeout", "cancelled":
					backend.waitHook = func(waitCtx context.Context, r Resource) error {
						if r.ID == namespaceID {
							if mode == "cancelled" {
								cancel()
							}
							<-waitCtx.Done()
						}
						return nil
					}
				case "failed-acceptance":
					accept = func(context.Context, *Plan) error { acceptCalls++; return errors.New("private acceptance failure") }
				}
				err = plan.Execute(ctx, backend, sha, root, qualified, fence, accept)
				if (err == nil) != (mode == "success") || backend.leaseHeld || backend.releaseCalls != 1 {
					t.Fatal("execution outcome or exact lease cleanup is incorrect", err, backend.releaseCalls)
				}
				if _, statErr := os.Stat(filepath.Join(root, "deployment", ".execution-active")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("stopped execution retained its active local lock", statErr)
				}
				journal := readExecutionJournal(t, root, sha)
				archives := executionOwnerArchives(t, root, sha)
				if len(archives) != 1 {
					t.Fatal("original private execution owner was not archived")
				}
				var owner ExecutionOwner
				raw, readErr := os.ReadFile(filepath.Join(archives[0], "owner.json"))
				if readErr != nil || json.Unmarshal(raw, &owner) != nil || owner.PlanSHA256 != sha || owner.Environment != environment || owner.Lease.UID != "fixture-owner" {
					t.Fatal("archived owner does not describe the original execution", readErr)
				}
				lock, lockErr := LockExecutionOwner(archives[0], false)
				if lockErr != nil {
					t.Fatal("stopped execution retained its OS lock", lockErr)
				}
				lock.Close()
				if journal.Accepted != (mode == "success") || journal.AcceptanceStarted != (mode == "success" || mode == "failed-acceptance") {
					t.Fatal("cleanup changed recorded acceptance")
				}
				switch mode {
				case "success", "failed-acceptance":
					if len(backend.written) != 2 || len(journal.Completed) != 2 || acceptCalls != 1 {
						t.Fatal("completed resources or acceptance evidence were lost")
					}
				case "prewrite-failure":
					if len(backend.written) != 0 || len(journal.Completed) != 0 || journal.Failed != namespaceID || acceptCalls != 0 {
						t.Fatal("prewrite failure changed resources or lost its journal")
					}
				case "persisted-write-error":
					if !errors.Is(err, writeFailure) || len(backend.written) != 1 || journal.InFlight != namespaceID || journal.Failed != namespaceID || len(journal.Completed) != 0 {
						t.Fatal("uncertain persisted write was erased or marked complete", err)
					}
				case "timeout", "cancelled":
					interruption := context.Canceled
					if mode == "timeout" {
						interruption = context.DeadlineExceeded
					}
					if !errors.Is(err, interruption) || len(backend.written) != 1 || len(journal.Completed) != 1 || journal.Completed[0] != namespaceID || journal.Owners[namespaceID] != "namespace-owner" {
						t.Fatal("interruption lost completed namespace progress", err)
					}
				}
			})
		}
	}
}

func TestExecutionCleanupFailureIsSafeAndDoesNotRemoveReplacementOwners(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"release-failed", "replacement-lease", "replacement-local-lock"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				Map(definition.Object["spec"])["replicas"] = float64(2)
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				backend := &cleanupBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}}, root: root, t: t}
				primary := errors.New("rollout did not become ready")
				backend.waitHook = func(context.Context, Resource) error {
					switch mode {
					case "release-failed":
						backend.releaseErr = errors.New("private-lease-release-output")
					case "replacement-lease":
						backend.replacementLease = true
					case "replacement-local-lock":
						active := filepath.Join(root, "deployment", ".execution-active")
						if err := os.Rename(active, filepath.Join(root, "displaced-owner")); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(active, 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(active, "owner.json"), []byte("concurrent-owner"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return primary
				}
				qualified := func(context.Context, *Plan) error { return nil }
				err = plan.Execute(context.Background(), backend, plan.SHA256(), root, qualified, qualified, qualified)
				var failure *Blocker
				if !errors.As(err, &failure) || failure.ID != "deployment.execution.cleanup" || !errors.Is(err, primary) || strings.Contains(err.Error(), "private-") || backend.releaseCalls != 1 {
					t.Fatal("cleanup failure lost the original error, classification, or privacy", err)
				}
				journal := readExecutionJournal(t, root, plan.SHA256())
				if journal.Accepted || journal.Failed != ID(live) || journal.Owners[ID(live)] != "owner" || len(backend.written) != 1 {
					t.Fatal("cleanup failure discarded or changed the partial target")
				}
				active := filepath.Join(root, "deployment", ".execution-active")
				if mode == "replacement-local-lock" {
					raw, readErr := os.ReadFile(filepath.Join(active, "owner.json"))
					if readErr != nil || string(raw) != "concurrent-owner" || backend.leaseHeld {
						t.Fatal("cleanup removed a replacement local owner or retained its own released lease", readErr)
					}
				} else if !backend.leaseHeld {
					t.Fatal("cleanup reported a failed or replacement lease as deleted")
				}
				if _, statErr := os.Stat(active); statErr != nil {
					t.Fatal("failed cleanup removed its inspection evidence", statErr)
				}
			})
		}
	}
}

func TestExecutionCleanupDoesNotReportSuccessWhenLeaseReleaseFails(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			in, definition, live := fixture(t, environment, "Deployment")
			Map(definition.Object["spec"])["replicas"] = float64(2)
			plan, err := Compile(in, []Definition{definition}, []Object{live})
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			backend := &cleanupBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}}, root: root, t: t, releaseErr: errors.New("private-lease-release-output")}
			qualified := func(context.Context, *Plan) error { return nil }
			err = plan.Execute(context.Background(), backend, plan.SHA256(), root, qualified, qualified, qualified)
			var failure *Blocker
			if !errors.As(err, &failure) || failure.ID != "deployment.execution.cleanup" || strings.Contains(err.Error(), "private-") || backend.releaseCalls != 1 || !backend.leaseHeld {
				t.Fatal("accepted rollout reported success despite failed lease cleanup", err)
			}
			journal := readExecutionJournal(t, root, plan.SHA256())
			if !journal.Accepted || !journal.AcceptanceStarted || len(journal.Completed) != 1 || journal.Owners[ID(live)] != "owner" {
				t.Fatal("failed cleanup discarded already accepted resource evidence")
			}
			if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active", "owner.json")); err != nil {
				t.Fatal("unreleased lease lost its original local inspection evidence", err)
			}
		})
	}
}

func TestExecutionCleanupNeverReleasesALeaseItDidNotAcquire(t *testing.T) {
	in, definition, live := fixture(t, "dev", "Deployment")
	Map(definition.Object["spec"])["replicas"] = float64(2)
	plan, err := Compile(in, []Definition{definition}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	backend := &cleanupBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}, leaseHeld: true}, root: t.TempDir(), t: t}
	before := Digest(backend.objects)
	qualified := func(context.Context, *Plan) error { return nil }
	err = plan.Execute(context.Background(), backend, plan.SHA256(), backend.root, qualified, qualified, qualified)
	if err == nil || !backend.leaseHeld || backend.releaseCalls != 0 || len(backend.written) != 0 || Digest(backend.objects) != before {
		t.Fatal("rejected execution released or mutated another controller's owner", err)
	}
}

func TestExecutionPreservesSafeAcquisitionCleanupBlocker(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			in, definition, live := fixture(t, environment, "Deployment")
			Map(definition.Object["spec"])["replicas"] = float64(2)
			plan, err := Compile(in, []Definition{definition}, []Object{live})
			if err != nil {
				t.Fatal(err)
			}
			failure := &Blocker{ID: "deployment.execution.cleanup", Resource: "Lease/" + in.Stack + "-video-cloud/rtk-deployment-execution", Cause: "the exact acquisition outcome could not be reconciled", Next: "inspect the original lease owner"}
			backend := &cleanupBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}}, root: t.TempDir(), t: t, acquireErr: fmt.Errorf("private-acquisition-output: %w", failure)}
			before := Digest(backend.objects)
			qualified := func(context.Context, *Plan) error { return nil }
			err = plan.Execute(context.Background(), backend, plan.SHA256(), backend.root, qualified, qualified, qualified)
			var returned *Blocker
			if !errors.As(err, &returned) || *returned != *failure || strings.Contains(err.Error(), "private-acquisition-output") {
				t.Fatal("acquisition cleanup lost its safe diagnosis or exposed the private wrapper", err)
			}
			if backend.leaseHeld || backend.releaseCalls != 0 || len(backend.written) != 0 || Digest(backend.objects) != before {
				t.Fatal("failed acquisition started or cleaned up an unacquired execution")
			}
			if _, err := os.Stat(filepath.Join(backend.root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed acquisition left an active local owner", err)
			}
		})
	}
}

func TestExecutionCleanupPreservesPanicAndPersistedWriteEvidence(t *testing.T) {
	in, definition, live := fixture(t, "dev", "Deployment")
	Map(definition.Object["spec"])["replicas"] = float64(2)
	plan, err := Compile(in, []Definition{definition}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), executionCleanupContextKey{}, "original-context"))
	defer cancel()
	backend := &cleanupBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}}, root: root, t: t}
	panicFailure := errors.New("panic after persisted write")
	backend.waitHook = func(context.Context, Resource) error {
		cancel()
		panic(panicFailure)
	}
	qualified := func(context.Context, *Plan) error { return nil }
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = plan.Execute(ctx, backend, plan.SHA256(), root, qualified, qualified, qualified)
	}()
	if recovered != panicFailure || backend.leaseHeld || backend.releaseCalls != 1 || len(backend.written) != 1 {
		t.Fatal("panic cleanup swallowed the original panic or retained execution ownership", recovered)
	}
	journal := readExecutionJournal(t, root, plan.SHA256())
	if journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || len(journal.Owners) != 1 || journal.Owners[ID(live)] != "owner" || len(journal.Completed) != 1 || journal.Completed[0] != ID(live) {
		t.Fatal("panic cleanup lost the persisted but unready owner")
	}
	if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("panic unwinding retained its active local owner", err)
	}
	archives := executionOwnerArchives(t, root, plan.SHA256())
	if len(archives) != 1 {
		t.Fatal("panic cleanup lost the private execution owner")
	}
	running, err := LockExecutionOwner(archives[0], false)
	if err != nil {
		t.Fatal("panic cleanup retained its OS lock", err)
	}
	running.Close()
}
