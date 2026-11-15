package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func failedForwardStageNames(q createFailedForwardProof) (string, string) {
	if len(q.FailedStage.Child.PlanSHA256) < 12 || len(q.ToolImage.SourceRevision) < 12 {
		return "", ""
	}
	suffix := q.FailedStage.Child.PlanSHA256[:12] + "-" + q.ToolImage.SourceRevision[:12]
	return "failed-stage-crl-" + suffix, "failed-stage-retain-" + suffix
}

// executeCreateFailedForward resumes exactly the original qualified plan. The
// two native child plans own their own journals. Every retry rechecks original
// admission, publication, renderer, owner state and parent fence; it never
// resets a prior admission's start or mutates the original failed child.
func executeCreateFailedForward(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, q createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, render func(context.Context) (*deploymentplan.Plan, *deploymentplan.Plan, error), run environmentReleaseCommand, execute func(context.Context, string, *deploymentplan.Plan) error, accept func(context.Context, *deploymentplan.Plan) error, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("failed-stage repair requires its exact admitted original generation, qualification and native plans")
	}
	if e == nil || e.Store != store || b == nil || render == nil || run == nil || execute == nil || accept == nil || now.IsZero() || q.ParentOwnerSHA256 != deploymentplan.Digest(e.owner) || q.FailedStage.ParentSHA256 != e.Journal.PlanSHA256 || !partialSessionJournalExtends(q.SourceJournal, e.Journal) || e.Check(ctx) != nil {
		return bad()
	}
	review := deploymentplan.Digest(q)
	repairStage, retainStage := failedForwardStageNames(q)
	if !environmentPreflightDigest.MatchString(review) || repairStage == "" || retainStage == "" {
		return bad()
	}
	key := "failed-stage-forward-" + review
	intent := deploymentplan.Object{"qualification_sha256": review, "original_admission_sha256": admissionSHA, "parent_plan_sha256": q.FailedStage.ParentSHA256, "original_failed_plan_sha256": q.FailedStage.Child.PlanSHA256, "original_failed_journal_sha256": q.FailedStage.Child.JournalSHA256, "repair_plan_sha256": q.CandidateSHA256, "retain_plan_sha256": q.RetainCandidateSHA256}
	rawIntent := mustProviderRecoveryJSON(intent)
	intentSHA, err := environmentCreatePlanSHA256(rawIntent)
	if err != nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	proofTime := now
	if entry != nil {
		if entry.Phase != "failed-stage-forward" || entry.IntentSHA256 != intentSHA || entry.StartedAt.Before(q.QualifiedAt) || !entry.StartedAt.Before(q.ValidUntil) || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
			return bad()
		}
		// A retry revalidates live evidence but does not invent a fresh expiry or
		// discard the already admitted fixed execution window.
		proofTime = q.QualifiedAt
	} else if deploymentplan.Digest(e.Journal) != q.FailedStage.ParentJournalSHA256 {
		return bad()
	}
	var repair, retained *deploymentplan.Plan
	capture := func(ctx context.Context) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
		var err error
		repair, retained, err = render(ctx)
		return repair, retained, err
	}
	token, err := verifyCreateFailedForwardLiveEvidence(ctx, q, admitted, admissionSHA, q.FailedStage, proofTime, store.Root, capture, b, run)
	if err != nil {
		return err
	}
	if err = writeFailedForwardQualification(store, e, review, q); err != nil {
		return err
	}
	if entry == nil {
		if err = e.BeginEntry(key, "failed-stage-forward", rawIntent, now); err != nil {
			return err
		}
	}
	if err = execute(ctx, repairStage, repair); err != nil {
		return err
	}
	if err = execute(ctx, retainStage, retained); err != nil {
		return err
	}
	completedAt := time.Now().UTC()
	if completedAt.Before(now) {
		completedAt = now
	}
	if err = completeCreateFailedForwardAlias(ctx, store, e, b, q, admitted, admissionSHA, repairStage, retainStage, token, accept, completedAt); err != nil {
		return err
	}
	receipt := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": review, "original_stage": "kubernetes/" + q.FailedStage.Stage, "repair_stage": "kubernetes/" + repairStage, "retain_stage": "kubernetes/" + retainStage, "original_failed_journal_sha256": q.FailedStage.Child.JournalSHA256})
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return bad()
	}
	if entry.State == "completed" {
		if deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(jsonRawObject(receipt)) {
			return bad()
		}
		return e.Check(ctx)
	}
	if err = e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	completedAt = time.Now().UTC()
	if completedAt.Before(now) {
		completedAt = now
	}
	return e.CompleteEntry(key, completedAt)
}

// Qualifying proof contains the complete prior parent journal. It uses the
// normal 32 MiB deployment artifact bound, not the 256 KiB ceremony metadata
// bound, and a retry may only reuse byte-identical private evidence.
func writeFailedForwardQualification(store secretStore, e *environmentCreateExecution, review string, q createFailedForwardProof) error {
	bad := func() error {
		return createProviderRecoveryBlock("bounded original failed-stage qualification cannot be safely preserved")
	}
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || !environmentPreflightDigest.MatchString(review) || deploymentplan.Digest(q) != review {
		return bad()
	}
	raw, err := json.Marshal(q)
	if err != nil || len(raw) == 0 || len(raw) > environmentPreflightArtifactLimit {
		return bad()
	}
	relative := filepath.Join("deployment", "create", e.Journal.PlanSHA256, "failed-stage-forward", review, "qualification.json")
	path, err := store.safePath(relative)
	if err != nil {
		return bad()
	}
	if _, err = os.Lstat(path); err == nil {
		prior, readErr := readBoundedDerivedArtifact(path)
		if readErr != nil || !bytes.Equal(prior, raw) {
			return bad()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return bad()
	} else if err = store.write(relative, raw, false); err != nil {
		return err
	}
	return e.Check(context.Background())
}
