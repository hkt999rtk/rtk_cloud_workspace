package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type lateAuthorityExecutionContract struct {
	store                          secretStore
	e                              *environmentCreateExecution
	remaining                      *createManagedLateRemaining
	artifact                       createFoundationRecoveryArtifact
	qualificationPath, fingerprint string
	deadline                       time.Time
}

func newLateAuthorityExecutionContract(t *testing.T) *lateAuthorityExecutionContract {
	t.Helper()
	r, a, now := managedLateRemainingNegativeFixture(t)
	store, e, _, _ := freshStageFixture(t)
	r.ParentSHA, r.SourceSHA, r.OwnerSHA = e.Journal.PlanSHA256, deploymentplan.Digest(e.Journal), deploymentplan.Digest(e.owner)
	a.Plan.ParentSHA, a.Plan.ParentJournalSHA, a.Plan.ParentOwnerSHA = r.ParentSHA, r.SourceSHA, r.OwnerSHA
	var err error
	a.Plan.CheckerSHA, err = environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	a.Plan.KubeconfigSHA, a.Plan.FoundationSHA, a.Plan.FoundationJournalSHA = strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64)
	a.Plan.Plan = environmentCreatePlan{Environment: e.Journal.Environment, Stack: e.Journal.Stack, ConfigurationSHA256: strings.Repeat("4", 64)}
	managedLateRemainingNegativeBind(t, r, &a)
	rel := filepath.Join("deployment/create/openbao-recovery", r.Review, "qualification.json")
	raw := mustProviderRecoveryJSON(a)
	if err := writeEnvironmentCreateDerivedPrivateFile(store, rel, raw); err != nil {
		t.Fatal(err)
	}
	return &lateAuthorityExecutionContract{store: store, e: e, remaining: r, artifact: a, qualificationPath: filepath.Join(store.Root, rel), fingerprint: environmentPreflightBytesSHA256(raw), deadline: now.Add(90 * time.Minute)}
}
func (f *lateAuthorityExecutionContract) admit(ctx context.Context) (*createManagedLateRemainingAuthority, error) {
	return admitManagedLateRemaining(ctx, f.store, f.e, f.artifact, f.fingerprint, f.remaining, f.deadline)
}
func TestManagedLateRemainingAuthorityExecutionContract(t *testing.T) {
	f := newLateAuthorityExecutionContract(t)
	before := deploymentplan.Digest([]any{f.e.Journal, f.e.owner})
	first, err := f.admit(t.Context())
	if err != nil {
		t.Fatal("first admission", err)
	}
	path := filepath.Join(f.store.Root, managedLateRemainingAuthorityPath(f.remaining.ParentSHA))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !managedLateRemainingAuthorityMatches(f.remaining, first) || first.CheckerSHA != f.artifact.Plan.CheckerSHA || first.QualifiedBytesSHA != f.fingerprint {
		t.Fatal("admission lost original authority bindings")
	}
	f.remaining.Authority = nil
	second, err := f.admit(t.Context())
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(second) {
		t.Fatal("retry replaced original fixed clock", err)
	}
	loaded, err := readManagedLateRemainingAuthority(f.store, f.remaining)
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(loaded) {
		t.Fatal("canonical reader rejected genuine admission", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(raw, after) || before != deploymentplan.Digest([]any{f.e.Journal, f.e.owner}) {
		t.Fatal("authority retry mutated original journal, owner or clock")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("authority is not private", err)
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("authority parent is not private", dir, err)
		}
		if dir == f.store.Root {
			break
		}
	}
}
func TestManagedLateRemainingAuthorityExecutionRefusesChangedAdmission(t *testing.T) {
	for _, mode := range []string{"cancelled", "source", "owner", "qualification-bytes", "checker", "expired", "new-source-without-clock"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateAuthorityExecutionContract(t)
			ctx := t.Context()
			switch mode {
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "source":
				f.remaining.SourceSHA = strings.Repeat("7", 64)
			case "owner":
				f.remaining.OwnerSHA = strings.Repeat("8", 64)
			case "qualification-bytes":
				raw, _ := os.ReadFile(f.qualificationPath)
				if os.WriteFile(f.qualificationPath, append(raw, '\n'), 0600) != nil {
					t.Fatal("change qualification")
				}
			case "checker":
				f.artifact.Plan.CheckerSHA = strings.Repeat("9", 64)
				managedLateRemainingNegativeBind(t, f.remaining, &f.artifact)
				rel := filepath.Join("deployment/create/openbao-recovery", f.remaining.Review, "qualification.json")
				raw := mustProviderRecoveryJSON(f.artifact)
				if writeEnvironmentCreateDerivedPrivateFile(f.store, rel, raw) != nil {
					t.Fatal("checker fixture")
				}
				f.fingerprint = environmentPreflightBytesSHA256(raw)
			case "expired":
				f.artifact.QualifiedAt = time.Now().Add(-time.Hour)
				f.artifact.ValidUntil = f.artifact.QualifiedAt.Add(15 * time.Minute)
			case "new-source-without-clock":
				f.remaining.CurrentSourceSHA = deploymentplan.Digest(f.e.Journal)
			}
			before := deploymentplan.Digest([]any{f.e.Journal, f.e.owner})
			if _, err := f.admit(ctx); err == nil {
				t.Fatal("changed admission obtained a clock")
			}
			if _, err := os.Stat(filepath.Join(f.store.Root, managedLateRemainingAuthorityPath(f.remaining.ParentSHA))); !os.IsNotExist(err) {
				t.Fatal("refusal published authority", err)
			}
			if before != deploymentplan.Digest([]any{f.e.Journal, f.e.owner}) {
				t.Fatal("refusal changed execution")
			}
		})
	}
}
func TestManagedLateRemainingAuthorityExecutionRechecksDurableEvidence(t *testing.T) {
	for _, mode := range []string{"clock-whitespace", "clock-owner", "clock-mode", "qualification-bytes", "qualification-missing", "directory-mode"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateAuthorityExecutionContract(t)
			a, err := f.admit(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.store.Root, managedLateRemainingAuthorityPath(f.remaining.ParentSHA))
			raw, _ := os.ReadFile(path)
			switch mode {
			case "clock-whitespace":
				err = os.WriteFile(path, append(raw, '\n'), 0600)
			case "clock-owner":
				copy := *a
				copy.OwnerSHA = strings.Repeat("9", 64)
				changed, _ := json.Marshal(copy)
				err = os.WriteFile(path, changed, 0600)
			case "clock-mode":
				err = os.Chmod(path, 0644)
			case "qualification-bytes":
				q, _ := os.ReadFile(f.qualificationPath)
				err = os.WriteFile(f.qualificationPath, append(q, '\n'), 0600)
			case "qualification-missing":
				err = os.Remove(f.qualificationPath)
			case "directory-mode":
				err = os.Chmod(filepath.Dir(path), 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if _, err := readManagedLateRemainingAuthority(f.store, f.remaining); err == nil {
				t.Fatal("mutated durable authority passed")
			}
			if _, err := f.admit(t.Context()); err == nil {
				t.Fatal("retry overwrote or accepted mutated authority")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal rewrote evidence")
			}
		})
	}
}
