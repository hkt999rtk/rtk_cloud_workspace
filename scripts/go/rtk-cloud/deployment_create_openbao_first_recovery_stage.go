package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// openBaoFirstRecordedStage retains the original child and native journal.
// ParentState and Journal.Accepted are deliberately separate: an accepted child
// does not by itself prove that its original parent receipt was completed.
type openBaoFirstRecordedStage struct {
	Plan        *deploymentplan.Plan
	Journal     deploymentplan.Journal
	HasJournal  bool
	ParentState string
}

// Read-only admission needs the same original OnDelete owner handling as
// create, without the create backend's volume-ledger mutation on Wait.
type openBaoFirstReadOnlyStageBackend struct {
	deploymentplan.Backend
	original environmentCreateKubernetesBackend
}

func (b openBaoFirstReadOnlyStageBackend) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	checked, err := b.original.initialOpenBaoWaitResource(ctx, resource)
	if err != nil {
		return err
	}
	return b.Backend.Wait(ctx, checked)
}

// inspectOpenBaoFirstRecordedStage is read-only. It never adopts an owner or
// rewrites a plan. An orphan stage input lacks authenticated parent intent and
// therefore remains blocked until independently reconciled.
func inspectOpenBaoFirstRecordedStage(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, stage string, native deploymentplan.Backend) (*openBaoFirstRecordedStage, error) {
	bad := func() (*openBaoFirstRecordedStage, error) {
		return nil, createProviderRecoveryBlock("original OpenBao-first stage intent, journal or owner cannot be verified")
	}
	if e == nil || e.Store != store || cfg.Environment != store.Environment || native == nil || (stage != "foundation" && stage != "foundation-services") || e.Check(ctx) != nil {
		return bad()
	}
	parent, configSHA, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil || !parent.OpenBaoFirst || parent.Environment != cfg.Environment || parent.Stack != e.Journal.Stack || configSHA != parent.ConfigurationSHA256 || configSHA != deploymentplan.Digest(cfg) {
		return bad()
	}
	p, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return bad()
	}
	var entry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if e.Journal.Entries[i].Key == "kubernetes/"+stage {
			if entry != nil {
				return bad()
			}
			entry = &e.Journal.Entries[i]
		}
	}
	if p == nil && entry == nil {
		return nil, e.Check(ctx)
	}
	if p == nil || entry == nil || p.ConfigurationSHA256 != parent.ConfigurationSHA256 || p.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return bad()
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, p)
	if err != nil {
		return bad()
	}
	// ReadExecution/AdmitResumable reject uncertain InFlight writes and verify
	// each recorded original UID/body before qualifying unstarted resources.
	readonly := openBaoFirstReadOnlyStageBackend{Backend: native, original: environmentCreateKubernetesBackend{execution: e, store: store, stagePlanSHA256: p.SHA256()}}
	if err := p.AdmitResumable(ctx, readonly, store.Root); err != nil {
		return nil, err
	}
	if err := e.Check(ctx); err != nil {
		return nil, err
	}
	return &openBaoFirstRecordedStage{Plan: p, Journal: journal, HasJournal: exists, ParentState: entry.State}, nil
}
