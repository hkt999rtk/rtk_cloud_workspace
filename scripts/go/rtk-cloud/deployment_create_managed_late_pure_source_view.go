package main

import (
	"context"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"time"
)

// A terminal parsing view borrows the held actual executor solely for pure
// historical checks. It is never accepted by Execution.Check or native code.
type managedLatePureSourceView struct {
	parent             *environmentCreateExecution
	parentSHA, viewSHA string
	completedProofSHA  string
	sourceLeases       int
}

func managedLatePureSourceViewMatches(actual, view *environmentCreateExecution) bool {
	if actual == nil || view == nil || view.latePureSource == nil || view.latePureSource.parent != actual || actual.latePureSource != nil || len(actual.Journal.Entries) < 396 || len(view.Journal.Entries) != 392 || actual.Store != view.Store || actual.JournalPath != view.JournalPath || actual.Directory != view.Directory || actual.running != view.running || actual.ownerInfo != view.ownerInfo || actual.runningInfo != view.runningInfo || deploymentplan.Digest(actual.owner) != deploymentplan.Digest(view.owner) || deploymentplan.Digest(actual.Journal) != view.latePureSource.parentSHA || deploymentplan.Digest(view.Journal) != view.latePureSource.viewSHA {
		return false
	}
	expected := actual.Journal
	if view.latePureSource.completedProofSHA != "" {
		if !managedLateCompletedTailSelected(actual.Journal) || view.latePureSource.sourceLeases < 0 || len(expected.KubernetesLeases) < view.latePureSource.sourceLeases {
			return false
		}
		path := filepath.Join(actual.Store.Root, managedLateTerminalFinalizerCompletedPath(actual.Journal.PlanSHA256))
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || validateEnvironmentPreflightDirectories(actual.Store, filepath.Dir(path)) != nil || environmentPreflightBytesSHA256(raw) != view.latePureSource.completedProofSHA {
			return false
		}
		expected.CompletedAt = time.Time{}
		expected.KubernetesLeases = expected.KubernetesLeases[:view.latePureSource.sourceLeases]
	} else {
		terminal := actual.Journal
		terminal.Entries = terminal.Entries[:396]
		if !managedLateTerminalFinalizerSelected(terminal) {
			return false
		}
	}
	expected.Entries = expected.Entries[:392]
	return deploymentplan.Digest(expected) == deploymentplan.Digest(view.Journal)
}

func managedLateTerminalPureSourceView(ctx context.Context, actual *environmentCreateExecution) (*environmentCreateExecution, error) {
	if actual == nil || actual.Check(ctx) != nil || len(actual.Journal.Entries) < 396 {
		return nil, managedLateSourceReuseBlock()
	}
	view := *actual
	view.Journal = actual.Journal
	view.Journal.Entries = slices.Clone(view.Journal.Entries[:392])
	for i := range view.Journal.Entries {
		view.Journal.Entries[i].Receipt = slices.Clone(view.Journal.Entries[i].Receipt)
	}
	view.Journal.KubernetesLeases = slices.Clone(view.Journal.KubernetesLeases)
	view.latePureSource = &managedLatePureSourceView{parent: actual, parentSHA: deploymentplan.Digest(actual.Journal), viewSHA: deploymentplan.Digest(view.Journal)}
	if !managedLatePureSourceViewMatches(actual, &view) {
		return nil, managedLateSourceReuseBlock()
	}
	return &view, nil
}

func checkManagedLatePureSourceExecution(ctx context.Context, e *environmentCreateExecution) error {
	if e == nil {
		return managedLateSourceReuseBlock()
	}
	if e.latePureSource == nil {
		return e.Check(ctx)
	}
	parent := e.latePureSource.parent
	if !managedLatePureSourceViewMatches(parent, e) {
		return managedLateSourceReuseBlock()
	}
	return parent.Check(ctx)
}

// A completed tail can authenticate the same immutable pre-finalizer history.
// The actual executor and its completed396 receipt remain literal; only this
// private parsing view trims later entries/leases after the durable proof passes.
func managedLateCompletedTailPureSourceView(ctx context.Context, actual *environmentCreateExecution, archived *createManagedLateRemaining) (*environmentCreateExecution, error) {
	if actual == nil || archived == nil || actual.Check(ctx) != nil || !managedLateCompletedTailSelected(actual.Journal) || archived.ParentSHA != actual.Journal.PlanSHA256 || archived.OwnerSHA != deploymentplan.Digest(actual.owner) || !environmentPreflightDigest.MatchString(archived.CurrentSourceSHA) || !environmentPreflightDigest.MatchString(archived.OriginalReview) {
		return nil, managedLateSourceReuseBlock()
	}
	entry := createOpenBaoACKCompletionEntry(actual.Journal, "service-session-finalize")
	if entry == nil || entry.State != "completed" || entry.CompletedAt.IsZero() || validateManagedLateTerminalFinalizerCompletion(actual.Store, actual, &createManagedLateSessionRecovery{Review: archived.OriginalReview}) != nil {
		return nil, managedLateSourceReuseBlock()
	}
	authority, err := readManagedLateFinalizationAuthority(actual.Store, archived)
	if err != nil || authority == nil || authority.SourceSHA != archived.CurrentSourceSHA || len(actual.Journal.KubernetesLeases) < authority.SourceLeases {
		return nil, managedLateSourceReuseBlock()
	}
	path := filepath.Join(actual.Store.Root, managedLateTerminalFinalizerCompletedPath(actual.Journal.PlanSHA256))
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || validateEnvironmentPreflightDirectories(actual.Store, filepath.Dir(path)) != nil {
		return nil, managedLateSourceReuseBlock()
	}
	view := *actual
	view.Journal = actual.Journal
	view.Journal.CompletedAt = time.Time{}
	view.Journal.Entries = slices.Clone(actual.Journal.Entries[:392])
	for i := range view.Journal.Entries {
		view.Journal.Entries[i].Receipt = slices.Clone(view.Journal.Entries[i].Receipt)
	}
	view.Journal.KubernetesLeases = slices.Clone(actual.Journal.KubernetesLeases[:authority.SourceLeases])
	if deploymentplan.Digest(view.Journal) != archived.CurrentSourceSHA {
		return nil, managedLateSourceReuseBlock()
	}
	view.latePureSource = &managedLatePureSourceView{parent: actual, parentSHA: deploymentplan.Digest(actual.Journal), viewSHA: deploymentplan.Digest(view.Journal), completedProofSHA: environmentPreflightBytesSHA256(raw), sourceLeases: authority.SourceLeases}
	if !managedLatePureSourceViewMatches(actual, &view) || actual.Check(ctx) != nil {
		return nil, managedLateSourceReuseBlock()
	}
	return &view, nil
}
