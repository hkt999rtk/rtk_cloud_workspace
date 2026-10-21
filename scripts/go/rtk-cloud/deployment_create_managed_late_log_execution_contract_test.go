package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The fake implements only Kubernetes storage. Plans, execution locks, parent
// receipts, acceptance and replay validation all use production code.
type lateLogContractBackend struct{ freshStageBackend }

func (b lateLogContractBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action != "retire" {
		return b.freshStageBackend.Write(ctx, action, body, dry)
	}
	if err := b.e.Check(ctx); err != nil {
		return nil, err
	}
	id := deploymentplan.ID(body)
	if current := b.objects[id]; current == nil || deploymentplan.Map(current["metadata"])["uid"] != deploymentplan.Map(body["metadata"])["uid"] {
		return nil, errors.New("foreign retirement")
	}
	if !dry {
		delete(b.objects, id)
		b.writes[id]++
	}
	return deploymentplan.Clone(body), nil
}
func (b lateLogContractBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.Action == "retire" {
		if b.objects[r.ID] != nil {
			return errors.New("retirement incomplete")
		}
		return ctx.Err()
	}
	return b.freshStageBackend.Wait(ctx, r)
}

type lateLogExecutionContract struct {
	store     secretStore
	e         *environmentCreateExecution
	late      *createManagedLateSessionRecovery
	backend   lateLogContractBackend
	helperUID string
}

func newLateLogExecutionContract(t *testing.T) *lateLogExecutionContract {
	t.Helper()
	late, _, _, _ := managedLateAcceptedHelperAdmissionFixture(t)
	store, e, _, backend := freshStageFixture(t)
	original := deploymentplan.Clone(late.LogProducer.Original.Desired)
	deploymentplan.Map(original["metadata"])["uid"] = late.LogProducer.Original.UID
	deploymentplan.Map(original["metadata"])["resourceVersion"] = "initial-1"
	backend.objects[late.LogProducer.Original.ID] = original
	return &lateLogExecutionContract{store: store, e: e, late: late, backend: lateLogContractBackend{backend}}
}
func (f *lateLogExecutionContract) run(t *testing.T, kind string) (*deploymentplan.Plan, error) {
	t.Helper()
	var p *deploymentplan.Plan
	switch kind {
	case "stop":
		p = f.late.LogStopCandidate
	case "existing-ack":
		p = f.late.LogHelperCandidate
	case "restore":
		p = f.late.LogRestoreCandidate
	case "retire":
		helper, err := renderLogACKDraftHelper(f.late.LogProducer)
		if err != nil {
			return nil, err
		}
		deploymentplan.Map(helper.Object["metadata"])["uid"] = f.helperUID
		definition := deploymentplan.Definition{Object: helper.Object, Phase: "retirement", Retire: true}
		p, err = deploymentplan.Compile(f.late.LogRetireCandidate.Inputs, []deploymentplan.Definition{definition}, []deploymentplan.Object{f.backend.objects[deploymentplan.ID(helper.Object)]})
		if err != nil {
			return nil, err
		}
	default:
		t.Fatal("unknown stage", kind)
	}
	stage := "managed-late-log-" + kind + "-" + f.late.Review
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, f.backend, f.store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return f.backend.CheckParentExecution(ctx, p, f.store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, f.backend) }
	err := executeEnvironmentCreateStagePlan(t.Context(), f.store, f.e, stage, p, f.backend, qualify, fence, accept, f.e.Check, io.Discard)
	if err != nil {
		return p, err
	}
	_, j, err := managedLateLogAccepted(f.store, f.e, f.late, kind, f.helperUID)
	if err == nil && kind == "existing-ack" {
		f.helperUID = j.Owners[p.Resources[0].ID]
	}
	return p, err
}
func TestManagedLateLogAcceptedExecutionContract(t *testing.T) {
	f := newLateLogExecutionContract(t)
	original := deploymentplan.Digest(f.late)
	for _, kind := range []string{"stop", "existing-ack", "retire", "restore"} {
		p, err := f.run(t, kind)
		if err != nil {
			t.Fatal(kind, err)
		}
		before := deploymentplan.Digest([]any{f.e.Journal, f.backend.objects, f.backend.writes})
		// An accepted child is authenticated from durable receipts without calling
		// a mutation adapter or rebuilding its original absent-owner baseline.
		forbidden := func(context.Context, string, []deploymentplan.Definition) error {
			t.Fatal("accepted child replayed", kind)
			return errors.New("replayed")
		}
		runtime := environmentCreateBootstrapRuntime{Execution: f.e}
		got, j, err := applyManagedLateLogExact(t.Context(), runtime, f.late, kind, deploymentplan.Definition{}, p, f.helperUID, forbidden, f.e.Check)
		if err != nil || got.SHA256() != p.SHA256() || !j.Accepted {
			t.Fatal("accepted retry", kind, err)
		}
		if before != deploymentplan.Digest([]any{f.e.Journal, f.backend.objects, f.backend.writes}) {
			t.Fatal("retry changed accepted history", kind)
		}
	}
	if f.backend.objects[f.late.LogHelperCandidate.Resources[0].ID] != nil {
		t.Fatal("retired helper survived")
	}
	if f.backend.writes[f.late.LogProducer.Original.ID] != 2 || f.backend.writes[f.late.LogHelperCandidate.Resources[0].ID] != 2 {
		t.Fatal("lifecycle did not perform exactly stop/create/retire/restore", f.backend.writes)
	}
	restored := f.backend.objects[f.late.LogProducer.Original.ID]
	if deploymentplan.Map(restored["metadata"])["uid"] != f.late.LogProducer.Original.UID || !deploymentplan.Matches(f.late.LogRestoreCandidate.Resources[0].Desired, restored) || deploymentplan.Digest(f.late) != original {
		t.Fatal("identity or original descriptor changed")
	}
}
func TestManagedLateLogInterruptedExecutionRequiresRequalification(t *testing.T) {
	f := newLateLogExecutionContract(t)
	f.backend.failure = "after-write-bootstrap"
	if _, err := f.run(t, "stop"); err == nil || !strings.Contains(err.Error(), "isolated interruption") {
		t.Fatal("write interruption not reached", err)
	}
	before := deploymentplan.Digest([]any{f.e.Journal, f.backend.objects, f.backend.writes})
	called := false
	apply := func(context.Context, string, []deploymentplan.Definition) error { called = true; return nil }
	if _, _, err := applyManagedLateLogExact(t.Context(), environmentCreateBootstrapRuntime{Execution: f.e}, f.late, "stop", renderLogACKDraftQuiesce(f.late.LogProducer), f.late.LogStopCandidate, "", apply, f.e.Check); err == nil || called {
		t.Fatal("uncertain lifecycle was replayed", err, called)
	}
	if before != deploymentplan.Digest([]any{f.e.Journal, f.backend.objects, f.backend.writes}) {
		t.Fatal("refusal changed interrupted evidence")
	}
}
func TestManagedLateLogAcceptedExecutionRejectsTamperedHistory(t *testing.T) {
	for _, mode := range []string{"receipt-owner", "child-owner", "child-acceptance", "child-inflight", "child-plan", "duplicate-entry", "unknown-kind"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateLogExecutionContract(t)
			p, err := f.run(t, "stop")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json")
			var j deploymentplan.Journal
			if readExecutionJSON(path, &j) != nil {
				t.Fatal("journal")
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/managed-late-log-stop-"+f.late.Review)
			kind := "stop"
			switch mode {
			case "receipt-owner":
				entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": p.SHA256(), "owners": map[string]string{p.Resources[0].ID: "foreign"}})
			case "child-owner":
				j.Owners[p.Resources[0].ID] = "99999999-9999-4999-8999-999999999999"
			case "child-acceptance":
				j.Accepted = false
			case "child-inflight":
				j.InFlight = p.Resources[0].ID
			case "child-plan":
				j.PlanSHA256 = "foreign"
			case "duplicate-entry":
				f.e.Journal.Entries = append(f.e.Journal.Entries, *entry)
			case "unknown-kind":
				kind = "foreign"
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(j), 0600); err != nil {
				t.Fatal(err)
			}
			before := deploymentplan.Digest([]any{f.e.Journal, j, f.backend.objects})
			if _, _, err := managedLateLogAccepted(f.store, f.e, f.late, kind, ""); err == nil {
				t.Fatal("tampered accepted history passed")
			}
			var after deploymentplan.Journal
			if readExecutionJSON(path, &after) != nil || before != deploymentplan.Digest([]any{f.e.Journal, after, f.backend.objects}) {
				t.Fatal("reader changed refused evidence")
			}
		})
	}
}
