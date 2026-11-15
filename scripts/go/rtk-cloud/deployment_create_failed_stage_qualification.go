package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// createFailedStageQualification is a read-only snapshot. It has no authority to
// repair the stage: a create continuation must bind it and the exact repair
// plan into its own preflight qualification before any Kubernetes mutation.
type createFailedStageQualification struct {
	ParentSHA256        string                               `json:"parent_sha256"`
	ParentJournalSHA256 string                               `json:"parent_journal_sha256"`
	ParentIntentSHA256  string                               `json:"parent_intent_sha256"`
	Stage               string                               `json:"stage"`
	Child               deploymentplan.FailedForwardEvidence `json:"child"`
}

func qualifyCreateFailedFullyWrittenStage(ctx context.Context, store secretStore, e *environmentCreateExecution, stage string, b deploymentplan.Backend) (createFailedStageQualification, error) {
	var empty createFailedStageQualification
	bad := func() (createFailedStageQualification, error) {
		return empty, environmentCreateBootstrapBlocker("fully written failed stage requires the exact original parent and child journal")
	}
	if e == nil || e.Store != store || b == nil || !secretEnvironmentPattern.MatchString(stage) {
		return bad()
	}
	if err := e.Check(ctx); err != nil {
		return empty, err
	}
	parentDigest := deploymentplan.Digest(e.Journal)
	p, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || p == nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 || entry.Phase != stage {
		return bad()
	}
	intent := entry.IntentSHA256
	j, exists, err := readEnvironmentCreateStageProgress(store, e, stage, p)
	if err != nil || !exists || j.Failed == "" {
		return bad()
	}
	child, err := p.QualifyFullyWrittenFailedForward(ctx, b, store.Root, p.SHA256(), j.Failed)
	if err != nil {
		return empty, err
	}
	if err = e.Check(ctx); err != nil || deploymentplan.Digest(e.Journal) != parentDigest {
		return bad()
	}
	return createFailedStageQualification{ParentSHA256: e.Journal.PlanSHA256, ParentJournalSHA256: parentDigest, ParentIntentSHA256: intent, Stage: stage, Child: child}, nil
}
