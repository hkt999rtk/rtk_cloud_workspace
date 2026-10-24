package main

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type lateACKOwnerExecutionContract struct {
	store    secretStore
	e        *environmentCreateExecution
	late     *createManagedLateSessionRecovery
	recovery *createServiceSessionRecovery
	original deploymentplan.Resource
	accepted *deploymentplan.Plan
	backend  freshStageBackend
}

// Exercise the current-owner projection at its admitted-owner-plan boundary.
// The accepted child is executed with the real parent fence, durable receipts
// and final verification. This does not assert the entire earlier bootstrap
// source has been reconstructed by this small owner-specific fixture.
func newLateACKOwnerExecutionContract(t *testing.T, extraDelta bool) *lateACKOwnerExecutionContract {
	t.Helper()
	store, e, _, backend := freshStageFixture(t)
	late, preview := managedLateOwnersFixture(t)
	var definitions []deploymentplan.Definition
	var observed []deploymentplan.Object
	for _, resource := range preview.Resources {
		obj := deploymentplan.Clone(resource.Desired)
		if extraDelta && obj["kind"] == "StatefulSet" {
			managedNamedField(environmentPKIPod(obj)["containers"], "native")["image"] = "foreign@sha256:" + deploymentplan.Digest("unrelated-image")
		}
		definitions = append(definitions, deploymentplan.Definition{Phase: resource.Phase, Object: obj, Retain: resource.Action == "retain"})
		old := late.Owners[resource.ID]
		actual := deploymentplan.Clone(old.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = old.UID
		deploymentplan.Map(actual["metadata"])["resourceVersion"] = "1"
		backend.objects[resource.ID] = actual
		observed = append(observed, actual)
	}
	candidate, err := deploymentplan.Compile(preview.Inputs, definitions, observed)
	if err != nil {
		t.Fatal(err)
	}
	late.OwnerCandidate = candidate
	late.SourceSHA = deploymentplan.Digest(e.Journal)
	late.OriginalRepairReview = deploymentplan.Digest("original-retained-openbao-role")
	source := e.Journal
	plan := createFoundationRecoveryPlan{Schema: managedLateSessionSchema, ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(e.owner), ParentJournalSHA: late.SourceSHA, Materials: map[string]string{"accepted-service-core-late-session-owners-plan": candidate.SHA256()}}
	artifact := lateSeedForwardSixGO(plan, time.Now().UTC())
	late.Review = deploymentplan.Digest(plan)
	for relative, value := range map[string]any{"qualification.json": artifact, "source-journal.json": source} {
		if err := store.write(filepath.Join("deployment/create/openbao-recovery", late.Review, relative), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	key := "openbao-recovery-admission-" + late.Review
	intent := map[string]string{"review_sha256": late.Review, "qualification_sha256": deploymentplan.Digest(artifact), "original_parent_journal_sha256": late.SourceSHA}
	if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": late.Review})); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stage := "managed-late-session-owners-" + late.Review
	inputs := candidate.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, definitions)
	actual, err := deploymentplan.Compile(inputs, definitions, observed)
	if err != nil {
		t.Fatal(err)
	}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, backend, store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return backend.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }
	if err := executeEnvironmentCreateStagePlan(context.Background(), store, e, stage, actual, backend, qualify, fence, accept, e.Check, io.Discard); err != nil {
		t.Fatal(err)
	}
	original := late.Owners["StatefulSet/"+e.Journal.Stack+"-secrets/openbao"]
	if original.ID == "" {
		t.Fatal("original native OpenBao StatefulSet missing")
	}
	recovery := &createServiceSessionRecovery{Review: late.Review, Ceremony: environmentCreateServiceCeremony{BootstrapSessionID: late.NewSessionID}, ManagedLateSession: late}
	return &lateACKOwnerExecutionContract{store, e, late, recovery, original, actual, backend}
}
func (f *lateACKOwnerExecutionContract) read() (deploymentplan.Object, deploymentplan.Object, error) {
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, ServiceSessionRecovery: f.recovery}
	return managedLateOpenBaoCurrentDesired(context.Background(), deploymentConfig{Environment: "dev"}, runtime, f.original)
}
func TestManagedLateACKCurrentOwnerExecutionContract(t *testing.T) {
	for _, mode := range []string{"valid", "admitted-extra-image", "changed-child-acceptance", "changed-original-owner", "changed-admission", "changed-original-session", "wrong-current-session", "ordinary-no-late"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateACKOwnerExecutionContract(t, mode == "admitted-extra-image")
			switch mode {
			case "changed-child-acceptance":
				var j deploymentplan.Journal
				path := filepath.Join("deployment/rollback", f.accepted.SHA256(), "journal.json")
				if err := readExecutionJSON(filepath.Join(f.store.Root, path), &j); err != nil {
					t.Fatal(err)
				}
				j.Accepted = false
				if err := f.store.write(path, mustProviderRecoveryJSON(j), true); err != nil {
					t.Fatal(err)
				}
			case "changed-original-owner":
				f.original.UID = "99999999-9999-4999-8999-999999999999"
			case "changed-admission":
				path := filepath.Join("deployment/create/openbao-recovery", f.late.Review, "qualification.json")
				var a createFoundationRecoveryArtifact
				if err := readExecutionJSON(filepath.Join(f.store.Root, path), &a); err != nil {
					t.Fatal(err)
				}
				a.Qualification.Gates[0].Status = "FAIL"
				if err := f.store.write(path, mustProviderRecoveryJSON(a), true); err != nil {
					t.Fatal(err)
				}
			case "changed-original-session":
				f.late.OriginalEnrollmentSession = "99999999-9999-4999-8999-999999999999"
			case "wrong-current-session":
				f.recovery.Ceremony.BootstrapSessionID = f.late.OriginalSession
			case "ordinary-no-late":
				f.recovery.ManagedLateSession = nil
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			nativeBefore := deploymentplan.Digest([]any{f.backend.objects, f.backend.writes})
			desired, proof, err := f.read()
			switch mode {
			case "valid":
				if err != nil || desired == nil || proof == nil || proof["late_review_sha256"] != f.late.Review || proof["late_owners_plan_sha256"] != f.accepted.SHA256() {
					t.Fatal("genuine accepted SID-only owner rejected", err)
				}
				expected := deploymentplan.Clone(f.original.Desired)
				managedNamedField(environmentPKIPod(expected)["containers"], "native")["env"] = mergeEnvironmentNamedFields(managedNamedField(environmentPKIPod(expected)["containers"], "native")["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": f.late.NewSessionID}))
				if deploymentplan.Digest(desired) != deploymentplan.Digest(expected) {
					t.Fatal("current native owner changed beyond the qualified session ID")
				}
				again, againProof, err := f.read()
				if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(desired) || deploymentplan.Digest(againProof) != deploymentplan.Digest(proof) {
					t.Fatal("read-only replay changed accepted proof", err)
				}
			case "wrong-current-session", "ordinary-no-late":
				if err != nil || proof != nil || deploymentplan.Digest(desired) != deploymentplan.Digest(f.original.Desired) {
					t.Fatal("ordinary original owner selection changed", err)
				}
			default:
				if err == nil {
					t.Fatal("accepted altered admission or native owner", mode)
				}
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if nativeBefore != deploymentplan.Digest([]any{f.backend.objects, f.backend.writes}) {
				t.Fatal("current-owner read mutated native resources")
			}
		})
	}
}
