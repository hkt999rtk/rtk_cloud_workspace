package deploymentplan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

type retainedRevalidationBackend struct {
	phaseReadBackend
	releaseErr       error
	leaseErr         error
	leaseChecks      int
	beforeLeaseCheck func(int)
}

type retainedRevalidationLease struct {
	ExecutionLease
	backend *retainedRevalidationBackend
}

func (b *retainedRevalidationBackend) AcquireExecution(ctx context.Context, p *Plan) (ExecutionLease, error) {
	lease, err := b.memoryBackend.AcquireExecution(ctx, p)
	if err != nil {
		return nil, err
	}
	return &retainedRevalidationLease{lease, b}, nil
}

func (l *retainedRevalidationLease) Check(ctx context.Context) error {
	l.backend.leaseChecks++
	if l.backend.beforeLeaseCheck != nil {
		l.backend.beforeLeaseCheck(l.backend.leaseChecks)
	}
	if l.backend.leaseErr != nil {
		return l.backend.leaseErr
	}
	return l.ExecutionLease.Check(ctx)
}

func (l *retainedRevalidationLease) Release(ctx context.Context) error {
	if l.backend.releaseErr != nil {
		return l.backend.releaseErr
	}
	return l.ExecutionLease.Release(ctx)
}

func acceptedRetainedFixture(t *testing.T, environment string) (*Plan, *retainedRevalidationBackend, string, Journal) {
	t.Helper()
	in, definition, live := fixture(t, environment, "Deployment")
	plan, err := Compile(in, []Definition{definition}, []Object{live})
	if err != nil || plan.Resources[0].Action != "retain" {
		t.Fatal("accepted retained target fixture unavailable", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "deployment", "rollback", plan.SHA256())
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	journal := Journal{PlanSHA256: plan.SHA256(), Completed: []string{plan.Resources[0].ID}, Owners: map[string]string{plan.Resources[0].ID: plan.Resources[0].UID}, Accepted: true, AcceptanceStarted: true}
	if err := writeExclusiveJSON(filepath.Join(directory, "plan.json"), plan); err != nil {
		t.Fatal(err)
	}
	if err := saveJournal(directory, journal); err != nil {
		t.Fatal(err)
	}
	backend := &retainedRevalidationBackend{phaseReadBackend: phaseReadBackend{memoryBackend: memoryBackend{objects: map[string]Object{ID(live): Clone(live)}}}}
	return plan, backend, root, journal
}

func retainedEvidenceBytes(t *testing.T, root, sha string) [2][]byte {
	t.Helper()
	var raw [2][]byte
	for i, name := range []string{"plan.json", "journal.json"} {
		var err error
		raw[i], err = os.ReadFile(filepath.Join(root, "deployment", "rollback", sha, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	return raw
}

func retainedVerificationPaths(t *testing.T, root string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(root, "deployment", "recovery", "revalidated-*", "verification.json"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestAcceptedRetainedPlanRevalidationPreservesOriginalEvidenceAndPerformsFreshAcceptance(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			plan, backend, root, _ := acceptedRetainedFixture(t, environment)
			sha := plan.SHA256()
			original := retainedEvidenceBytes(t, root, sha)
			qualifications, fences, accepts := 0, 0, 0
			qualify := func(context.Context, *Plan) error { qualifications++; return nil }
			fence := func(context.Context, *Plan) error { fences++; return nil }
			accept := func(context.Context, *Plan) error { accepts++; return nil }
			for attempt := 1; attempt <= 2; attempt++ {
				backend.progressResults = nil
				if err := plan.Execute(context.Background(), backend, sha, root, qualify, fence, accept); err != nil {
					t.Fatal("complete accepted retained target could not be revalidated", err)
				}
				want := []executionPhaseResult{{"", "qualification", true}, {"", "admission", true}, {"", "lease", true}, {"", "recovery", true}, {"", "acceptance", true}, {"", "recovery", true}, {"", "cleanup", true}}
				if !reflect.DeepEqual(backend.progressResults, want) || backend.leaseHeld || len(backend.written) != 0 || len(backend.dry) != 0 || len(backend.waits) != attempt*2 || qualifications != attempt || fences != attempt*2 || accepts != attempt {
					t.Fatal("revalidation skipped checks, changed resources, or misreported ownership", backend.progressResults, qualifications, fences, accepts)
				}
				if after := retainedEvidenceBytes(t, root, sha); !bytes.Equal(original[0], after[0]) || !bytes.Equal(original[1], after[1]) {
					t.Fatal("revalidation overwrote the original execution evidence")
				}
				if paths := retainedVerificationPaths(t, root); len(paths) != attempt {
					t.Fatal("revalidation did not preserve a separate receipt for each attempt", paths)
				}
			}
			for _, path := range retainedVerificationPaths(t, root) {
				for _, directory := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
					info, err := os.Lstat(directory)
					if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
						t.Fatal("verification directory is not private", err)
					}
				}
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("verification receipt is not private", err)
				}
				raw, err := os.ReadFile(path)
				var receipt struct {
					PlanSHA256, OriginalJournalSHA256, Outcome string
					ResourceCount                              int
					Lease                                      LeaseOwner
				}
				// Inspect JSON names independently of the private implementation type.
				var fields map[string]json.RawMessage
				if err != nil || json.Unmarshal(raw, &fields) != nil || json.Unmarshal(fields["plan_sha256"], &receipt.PlanSHA256) != nil || json.Unmarshal(fields["original_journal_sha256"], &receipt.OriginalJournalSHA256) != nil || json.Unmarshal(fields["outcome"], &receipt.Outcome) != nil || json.Unmarshal(fields["resource_count"], &receipt.ResourceCount) != nil || json.Unmarshal(fields["lease"], &receipt.Lease) != nil || len(fields["verified_at"]) == 0 {
					t.Fatal("verification receipt is incomplete", err)
				}
				if receipt.PlanSHA256 != sha || receipt.OriginalJournalSHA256 != fmt.Sprintf("%x", sha256.Sum256(original[1])) || receipt.Outcome != "accepted-retained-target-revalidated" || receipt.ResourceCount != len(plan.Resources) || receipt.Lease.UID != "fixture-owner" {
					t.Fatal("verification receipt lost its original evidence binding", receipt)
				}
			}
			if len(executionOwnerArchives(t, root, sha)) != 2 {
				t.Fatal("new execution ownership was not independently archived")
			}
		})
	}
}

func TestAcceptedRetainedPlanRevalidationRejectsUncertainOrChangedEvidence(t *testing.T) {
	for _, mode := range []string{"unaccepted", "acceptance-not-started", "in-flight", "failed", "missing-completion", "duplicate-completion", "missing-owner", "unknown-owner", "non-retain", "different-plan", "unknown-field", "duplicate-field", "symlink-journal", "public-journal", "drift-before", "drift-after", "replaced-owner", "unready", "input-drift", "lease-changed", "acceptance-failed", "acceptance-plan-mutated", "acceptance-cancelled", "receipt-plan-mutated", "receipt-cancelled", "evidence-changed-during-acceptance", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			plan, backend, root, journal := acceptedRetainedFixture(t, "dev")
			sha := plan.SHA256()
			directory := filepath.Join(root, "deployment", "rollback", sha)
			accepts := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pass := func(context.Context, *Plan) error { return nil }
			fence := pass
			accept := func(context.Context, *Plan) error { accepts++; return nil }
			switch mode {
			case "unaccepted":
				journal.Accepted = false
			case "acceptance-not-started":
				journal.AcceptanceStarted = false
			case "in-flight":
				journal.InFlight = plan.Resources[0].ID
			case "failed":
				journal.Failed = "deployment.post-deploy"
			case "missing-completion":
				journal.Completed = nil
			case "duplicate-completion":
				journal.Completed = []string{plan.Resources[0].ID, plan.Resources[0].ID}
			case "missing-owner":
				journal.Owners = map[string]string{}
			case "unknown-owner":
				journal.Owners = map[string]string{"unknown": "owner"}
			case "non-retain":
				plan.Resources[0].Action = "update"
				sha = plan.SHA256()
				directory = filepath.Join(root, "deployment", "rollback", sha)
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := writeExclusiveJSON(filepath.Join(directory, "plan.json"), plan); err != nil {
					t.Fatal(err)
				}
				journal.PlanSHA256 = sha
			case "different-plan":
				raw := []byte(`{"schema_version":2}`)
				if err := os.WriteFile(filepath.Join(directory, "plan.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "drift-before", "replaced-owner", "unready", "lease-changed", "cancelled":
				backend.progressHook = func(_, step string) {
					if step != "recovery" {
						return
					}
					switch mode {
					case "drift-before":
						Map(backend.objects[plan.Resources[0].ID]["spec"])["replicas"] = float64(7)
					case "replaced-owner":
						Map(backend.objects[plan.Resources[0].ID]["metadata"])["uid"] = "replacement-owner"
					case "unready":
						backend.failWait = true
					case "lease-changed":
						backend.leaseErr = errors.New("private lease changed")
					case "cancelled":
						cancel()
					}
				}
			case "drift-after":
				accept = func(context.Context, *Plan) error {
					accepts++
					Map(backend.objects[plan.Resources[0].ID]["spec"])["replicas"] = float64(7)
					return nil
				}
			case "input-drift":
				fence = func(context.Context, *Plan) error { return errors.New("private inputs changed") }
			case "acceptance-failed":
				accept = func(context.Context, *Plan) error { accepts++; return errors.New("private endpoint failure") }
			case "acceptance-plan-mutated", "acceptance-cancelled":
				accept = func(context.Context, *Plan) error {
					accepts++
					if mode == "acceptance-plan-mutated" {
						plan.BindingsSHA256 = Digest("mutated bindings")
					} else {
						cancel()
					}
					return nil
				}
			case "receipt-plan-mutated", "receipt-cancelled":
				backend.beforeLeaseCheck = func(count int) {
					if count != 5 {
						return
					}
					if mode == "receipt-plan-mutated" {
						plan.BindingsSHA256 = Digest("mutated bindings")
					} else {
						cancel()
					}
				}
			case "evidence-changed-during-acceptance":
				accept = func(context.Context, *Plan) error {
					accepts++
					changed := journal
					changed.Failed = "concurrent-evidence"
					return saveJournal(directory, changed)
				}
			}
			if err := saveJournal(directory, journal); err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(directory, "journal.json")
			if mode == "unknown-field" || mode == "duplicate-field" {
				raw, err := os.ReadFile(journalPath)
				if err != nil {
					t.Fatal(err)
				}
				field := `,"unknown":true}`
				if mode == "duplicate-field" {
					field = `,"accepted":true}`
				}
				if err := os.WriteFile(journalPath, append(raw[:len(raw)-1], field...), 0600); err != nil {
					t.Fatal(err)
				}
			} else if mode == "symlink-journal" {
				original := filepath.Join(directory, "original-journal.json")
				if err := os.Rename(journalPath, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, journalPath); err != nil {
					t.Fatal(err)
				}
			} else if mode == "public-journal" {
				if err := os.Chmod(journalPath, 0644); err != nil {
					t.Fatal(err)
				}
			}
			original := retainedEvidenceBytes(t, root, sha)
			err := plan.Execute(ctx, backend, sha, root, pass, fence, accept)
			if err == nil || len(backend.written) != 0 || backend.leaseHeld || len(retainedVerificationPaths(t, root)) != 0 {
				t.Fatal("uncertain execution was continued, modified, or accepted", mode, err)
			}
			after := retainedEvidenceBytes(t, root, sha)
			if !bytes.Equal(original[0], after[0]) || mode != "evidence-changed-during-acceptance" && !bytes.Equal(original[1], after[1]) {
				t.Fatal("rejected revalidation overwrote original evidence")
			}
			wantAccepts := 0
			if mode == "drift-after" || mode == "acceptance-failed" || mode == "acceptance-plan-mutated" || mode == "acceptance-cancelled" || mode == "receipt-plan-mutated" || mode == "receipt-cancelled" || mode == "evidence-changed-during-acceptance" {
				wantAccepts = 1
			}
			if accepts != wantAccepts {
				t.Fatal("acceptance ran before evidence checks or was omitted", accepts)
			}
			if mode == "unaccepted" {
				want := []executionPhaseResult{{"", "qualification", true}, {"", "admission", true}, {"", "lease", true}, {"", "recovery", false}, {"", "cleanup", true}}
				if !reflect.DeepEqual(backend.progressResults, want) {
					t.Fatal("historical evidence failure was reported as lease failure", backend.progressResults)
				}
			}
		})
	}
}

func TestRetainedPlanWithMissingJournalPreservesOrphanExecutionEvidence(t *testing.T) {
	plan, backend, root, _ := acceptedRetainedFixture(t, "dev")
	sha := plan.SHA256()
	directory := filepath.Join(root, "deployment", "rollback", sha)
	before, err := os.ReadFile(filepath.Join(directory, "plan.json"))
	if err != nil || os.Remove(filepath.Join(directory, "journal.json")) != nil {
		t.Fatal("orphan plan fixture unavailable", err)
	}
	pass := func(context.Context, *Plan) error { return nil }
	accepts := 0
	err = plan.Execute(context.Background(), backend, sha, root, pass, pass, func(context.Context, *Plan) error { accepts++; return nil })
	var failure *Blocker
	if !errors.As(err, &failure) || failure.ID != "deployment.recovery" || len(backend.written) != 0 || backend.leaseHeld || accepts != 0 || len(retainedVerificationPaths(t, root)) != 0 {
		t.Fatal("missing journal was treated as an unstarted execution", err)
	}
	after, err := os.ReadFile(filepath.Join(directory, "plan.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("orphan original plan was changed", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing historical journal was fabricated", err)
	}
	want := []executionPhaseResult{{"", "qualification", true}, {"", "admission", true}, {"", "lease", true}, {"", "recovery", false}, {"", "cleanup", true}}
	if !reflect.DeepEqual(backend.progressResults, want) {
		t.Fatal("orphan evidence failure was reported as lease failure", backend.progressResults)
	}
}

func TestAcceptedRetainedPlanRevalidationStillRequiresSuccessfulCleanup(t *testing.T) {
	plan, backend, root, _ := acceptedRetainedFixture(t, "dev")
	sha := plan.SHA256()
	original := retainedEvidenceBytes(t, root, sha)
	backend.releaseErr = errors.New("private release failure")
	pass := func(context.Context, *Plan) error { return nil }
	err := plan.Execute(context.Background(), backend, sha, root, pass, pass, pass)
	var failure *Blocker
	if !errors.As(err, &failure) || failure.ID != "deployment.execution.cleanup" || !backend.leaseHeld || len(backend.written) != 0 || len(retainedVerificationPaths(t, root)) != 1 {
		t.Fatal("a fresh verification receipt bypassed failed ownership cleanup", err)
	}
	if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active", "owner.json")); err != nil {
		t.Fatal("failed cleanup lost current ownership evidence", err)
	}
	if after := retainedEvidenceBytes(t, root, sha); !bytes.Equal(original[0], after[0]) || !bytes.Equal(original[1], after[1]) {
		t.Fatal("failed cleanup overwrote original execution evidence")
	}
	last := backend.progressResults[len(backend.progressResults)-1]
	if last.Step != "cleanup" || last.Passed {
		t.Fatal("failed cleanup was reported successful", backend.progressResults)
	}
}

func TestRetainedExecutionEvidenceRejectsFIFOWithoutWaitingForAWriter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := readPrivateExecutionEvidence(path)
		finished <- err
	}()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("nonregular execution evidence was accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("reading nonregular evidence waited for a FIFO writer")
	}
}

func TestAcceptedRetainedPlanRevalidationFencesOwnershipAfterReadiness(t *testing.T) {
	for _, moment := range []string{"before-acceptance", "before-receipt"} {
		t.Run(moment, func(t *testing.T) {
			plan, backend, root, _ := acceptedRetainedFixture(t, "dev")
			sha := plan.SHA256()
			original := retainedEvidenceBytes(t, root, sha)
			accepts, waits := 0, 0
			backend.beforeRead = func(string) {
				// Admission performs one read. Each complete reconciliation then
				// waits and reads the target under the lease.
				waits = len(backend.waits)
				if waits == 1 && moment == "before-acceptance" || waits == 2 && moment == "before-receipt" {
					backend.leaseErr = errors.New("private lease replaced during readiness")
				}
			}
			pass := func(context.Context, *Plan) error { return nil }
			accept := func(context.Context, *Plan) error { accepts++; return nil }
			err := plan.Execute(context.Background(), backend, sha, root, pass, pass, accept)
			var failure *Blocker
			if !errors.As(err, &failure) || failure.ID != "deployment.execution.lease" || len(retainedVerificationPaths(t, root)) != 0 || len(backend.written) != 0 || backend.leaseHeld {
				t.Fatal("readiness lost its lease fence before acceptance or receipt", moment, err)
			}
			wantAccepts := 0
			if moment == "before-receipt" {
				wantAccepts = 1
			}
			if accepts != wantAccepts {
				t.Fatal("acceptance continued after readiness lost execution ownership", accepts)
			}
			if after := retainedEvidenceBytes(t, root, sha); !bytes.Equal(original[0], after[0]) || !bytes.Equal(original[1], after[1]) {
				t.Fatal("ownership failure changed original evidence")
			}
		})
	}
}
