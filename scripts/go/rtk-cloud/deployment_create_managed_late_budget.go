package main

import (
	"context"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const (
	managedLatePreparationLimit   = 30 * time.Minute
	managedLateQualificationLimit = 45 * time.Minute
	managedLateNativeLimit        = 50 * time.Minute
	managedLateExpiryReserve      = 5 * time.Minute
	managedLateFinalizerLimit     = 15 * time.Minute
	managedLateOverallLimit       = 90 * time.Minute
)

type managedLateBudgetKey struct{}
type managedLateRuntimeBudget struct {
	ResidualAuthority         *createManagedLateResidualCleanupAuthority
	PostNativeAuthority       *createManagedLatePostNativeCleanupAuthority
	TerminalAuthority         *createManagedLateTerminalFinalizerAuthority
	FinalizationAuthority     *createManagedLateFinalizationAuthority
	InstalledCleanupAuthority *createManagedLateInstalledCleanupAuthority
	StartedAt, Deadline       time.Time
	Overall                   context.Context
	Limit                     time.Duration
	PostMint                  bool
	InstallationClock         *createManagedLateInstallationClock
	RemainingAuthority        *createManagedLateRemainingAuthority
}

func managedLateBudgetLimit(budget managedLateRuntimeBudget) (time.Duration, bool) {
	limit := budget.Limit
	if limit == 0 {
		limit = managedLateNativeLimit
	}
	if budget.ResidualAuthority != nil {
		return limit, limit == managedLateResidualCleanupLimit
	}
	if budget.PostNativeAuthority != nil {
		return limit, limit == managedLatePostNativeCleanupLimit
	}
	if budget.TerminalAuthority != nil {
		return limit, limit == managedLateTerminalFinalizerLimit
	}
	if budget.FinalizationAuthority != nil {
		return limit, limit == managedLateNativeLimit
	}
	if budget.InstalledCleanupAuthority != nil {
		return limit, limit == managedLateInstalledCleanupLimit
	}
	if budget.PostMint {
		return limit, limit == managedLateOverallLimit
	}
	if budget.RemainingAuthority != nil {
		return limit, limit == managedLateRemainingLimit
	}
	return limit, limit == managedLateNativeLimit
}

func managedLateRuntimeBudgetValid(budget managedLateRuntimeBudget) bool {
	if a := budget.ResidualAuthority; a != nil {
		return (a.Schema == managedLateResidualCleanupSchema || a.Schema == managedLateResidualRemainingSchema) && budget.Limit == managedLateResidualCleanupLimit && budget.PostNativeAuthority == nil && budget.TerminalAuthority == nil && budget.FinalizationAuthority == nil && budget.InstalledCleanupAuthority == nil && budget.RemainingAuthority == nil && !budget.PostMint && budget.InstallationClock == nil && budget.Deadline.Equal(a.Deadline) && budget.StartedAt.Before(a.StartedAt) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit))
	}
	if a := budget.PostNativeAuthority; a != nil {
		return a.Schema == managedLatePostNativeCleanupSchema && budget.Limit == managedLatePostNativeCleanupLimit && budget.TerminalAuthority == nil && budget.FinalizationAuthority == nil && budget.InstalledCleanupAuthority == nil && budget.RemainingAuthority == nil && !budget.PostMint && budget.InstallationClock == nil && budget.Deadline.Equal(a.Deadline) && budget.StartedAt.Before(a.StartedAt) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit))
	}
	if a := budget.TerminalAuthority; a != nil {
		return a.Schema == managedLateTerminalFinalizerSchema && budget.Limit == managedLateTerminalFinalizerLimit && budget.FinalizationAuthority == nil && budget.InstalledCleanupAuthority == nil && budget.RemainingAuthority == nil && !budget.PostMint && budget.InstallationClock == nil && budget.Deadline.Equal(a.Deadline) && budget.StartedAt.Before(a.StartedAt) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit))
	}
	if budget.FinalizationAuthority != nil {
		return !budget.PostMint && budget.InstallationClock == nil && budget.RemainingAuthority == nil && budget.InstalledCleanupAuthority == nil && budget.Limit == managedLateNativeLimit && managedLateFinalizationAuthorityValid(budget.FinalizationAuthority) && budget.StartedAt.Equal(budget.FinalizationAuthority.StartedAt) && budget.Deadline.Equal(budget.FinalizationAuthority.Deadline)
	}
	if budget.InstalledCleanupAuthority != nil {
		return !budget.PostMint && budget.InstallationClock == nil && budget.RemainingAuthority == nil && budget.Limit == managedLateInstalledCleanupLimit && managedLateInstalledCleanupAuthorityValid(budget.InstalledCleanupAuthority) && budget.StartedAt.Equal(budget.InstalledCleanupAuthority.StartedAt) && budget.Deadline.Equal(budget.InstalledCleanupAuthority.Deadline)
	}
	if budget.PostMint && budget.RemainingAuthority != nil {
		return false
	}
	limit, valid := managedLateBudgetLimit(budget)
	if !valid || budget.StartedAt.IsZero() {
		return false
	}
	if !budget.PostMint {
		if budget.RemainingAuthority != nil {
			return budget.InstallationClock == nil && managedLateRemainingAuthorityValid(budget.RemainingAuthority) && budget.StartedAt.Equal(budget.RemainingAuthority.StartedAt) && budget.Deadline.Equal(budget.RemainingAuthority.Deadline)
		}
		return budget.InstallationClock == nil && budget.Deadline.Equal(budget.StartedAt.Add(limit))
	}
	clock := budget.InstallationClock
	return managedLateInstallationClockValid(clock) && budget.StartedAt.Equal(clock.StartedAt) && budget.Deadline.Equal(clock.Deadline)
}

func beginManagedLateRemainingBudget(ctx context.Context, materials map[string]string, remaining *createManagedLateRemaining) (context.Context, context.CancelFunc, error) {
	if remaining != nil && remaining.ResidualRemaining != nil {
		return beginManagedLateResidualRemainingBudget(ctx, materials, remaining)
	}
	if remaining != nil && remaining.ResidualCleanup != nil {
		return beginManagedLateResidualCleanupBudget(ctx, materials, remaining)
	}
	if remaining != nil && remaining.PostNativeCleanup != nil {
		return beginManagedLatePostNativeCleanupBudget(ctx, materials, remaining)
	}
	if remaining != nil && remaining.TerminalFinalizerCompletion != nil {
		return beginManagedLateTerminalFinalizerBudget(ctx, materials, remaining)
	}
	if remaining != nil && remaining.FinalizationOnly {
		return beginManagedLateFinalizationBudget(ctx, materials, remaining)
	}
	if remaining != nil && remaining.CleanupOnly {
		return beginManagedLateInstalledCleanupBudget(ctx, materials, remaining)
	}
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || remaining == nil || !managedLateRemainingAuthorityMatches(remaining, remaining.Authority) || time.Until(remaining.Authority.Deadline) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("late-remaining-fixed-budget")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("late-remaining-fixed-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	overall, cancel := context.WithDeadline(ctx, remaining.Authority.Deadline)
	budget := managedLateRuntimeBudget{StartedAt: remaining.Authority.StartedAt, Deadline: remaining.Authority.Deadline, Overall: overall, Limit: managedLateRemainingLimit, RemainingAuthority: remaining.Authority}
	return context.WithValue(overall, managedLateBudgetKey{}, budget), cancel, nil
}

func managedLateBudgetMaterials() map[string]string {
	return map[string]string{
		"accepted-service-core-late-budget-schema":      deploymentplan.Digest("rtk-cloud.create-managed-late-budget/v1"),
		"accepted-service-core-late-budget-preparation": deploymentplan.Digest(managedLatePreparationLimit.String()),
		"accepted-service-core-late-budget-native":      deploymentplan.Digest(managedLateNativeLimit.String()),
		"accepted-service-core-late-budget-reserve":     deploymentplan.Digest(managedLateExpiryReserve.String()),
		"accepted-service-core-late-budget-finalizer":   deploymentplan.Digest(managedLateFinalizerLimit.String()),
		"accepted-service-core-late-budget-overall":     deploymentplan.Digest(managedLateOverallLimit.String()),
	}
}

func bindManagedLateBudgetMaterials(materials map[string]string) error {
	if materials == nil {
		return createOpenBaoACKBlock("late-qualified-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if prior := materials[key]; prior != "" && prior != value {
			return createOpenBaoACKBlock("late-qualified-budget")
		}
		materials[key] = value
	}
	return nil
}

// Read-only qualification has its own bounded window. Native preparation keeps
// the original 30-minute policy and all admitted native clocks remain unchanged.
func managedLatePreparationContextForOperation(ctx context.Context, commandStarted time.Time, execute bool) (context.Context, context.CancelFunc, error) {
	if execute {
		return managedLatePreparationContext(ctx, commandStarted)
	}
	if ctx == nil || ctx.Err() != nil || commandStarted.IsZero() || commandStarted.After(time.Now()) || !time.Now().Before(commandStarted.Add(managedLateQualificationLimit)) {
		return nil, nil, createOpenBaoACKBlock("late-qualification-budget")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, nil, createOpenBaoACKBlock("late-qualification-budget")
	}
	qualified, cancel := context.WithDeadline(ctx, commandStarted.Add(managedLateQualificationLimit))
	return qualified, cancel, nil
}

func managedLatePreparationContext(ctx context.Context, commandStarted time.Time) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || commandStarted.IsZero() || commandStarted.After(time.Now()) || !time.Now().Before(commandStarted.Add(managedLatePreparationLimit)) {
		return nil, nil, createOpenBaoACKBlock("late-preparation-budget")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, nil, createOpenBaoACKBlock("late-preparation-budget")
	}
	prepared, cancel := context.WithDeadline(ctx, commandStarted.Add(managedLatePreparationLimit))
	return prepared, cancel, nil
}

// The clock belongs only to this admitted attempt. Rechecks retain its fixed
// deadline; neither the immutable source descriptor nor the signing TTL changes.
func beginManagedLateNativeBudget(ctx context.Context, materials map[string]string) (context.Context, context.CancelFunc, error) {
	now := time.Now().UTC()
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil {
		return nil, nil, createOpenBaoACKBlock("late-native-budget")
	}
	overall, ok := ctx.Deadline()
	if !ok || overall.Before(now.Add(managedLateNativeLimit)) || overall.After(now.Add(managedLateOverallLimit)) {
		return nil, nil, createOpenBaoACKBlock("late-native-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	budget := managedLateRuntimeBudget{StartedAt: now, Deadline: now.Add(managedLateNativeLimit), Overall: ctx}
	native, cancel := context.WithDeadline(context.WithValue(ctx, managedLateBudgetKey{}, budget), budget.Deadline)
	return native, cancel, nil
}

func managedLateActiveBudget(ctx context.Context) (managedLateRuntimeBudget, error) {
	var empty managedLateRuntimeBudget
	if ctx == nil || ctx.Err() != nil {
		return empty, createOpenBaoACKBlock("late-native-budget")
	}
	budget, ok := ctx.Value(managedLateBudgetKey{}).(managedLateRuntimeBudget)
	deadline, bounded := ctx.Deadline()
	if !ok || !managedLateRuntimeBudgetValid(budget) || !bounded || budget.Overall == nil || budget.Overall.Err() != nil || budget.StartedAt.IsZero() || budget.StartedAt.After(time.Now()) || deadline.After(budget.Deadline) || !time.Now().Before(budget.Deadline) {
		return empty, createOpenBaoACKBlock("late-native-budget")
	}
	return budget, nil
}

func verifyManagedLateSeedBudget(ctx context.Context, certificateExpires, sessionExpires time.Time) error {
	budget, err := managedLateActiveBudget(ctx)
	if err != nil {
		return err
	}
	cutoff := budget.Deadline.Add(managedLateExpiryReserve)
	if certificateExpires.Before(cutoff) || sessionExpires.Before(cutoff) {
		return createOpenBaoACKBlock("late-native-certificate-session-reserve")
	}
	return nil
}

func verifyManagedLateFinalizerBudget(ctx context.Context) error {
	budget, err := managedLateActiveBudget(ctx)
	if err != nil {
		return err
	}
	// This phase has already completed the native finalizer. Its original
	// paired admission established the reserve; retries only consume the
	// positive remainder and recheck that same authority before cleanup.
	if budget.PostNativeAuthority != nil || budget.ResidualAuthority != nil {
		return nil
	}
	deadline, _ := ctx.Deadline()
	if time.Until(deadline) < managedLateFinalizerLimit {
		return createOpenBaoACKBlock("late-finalizer-budget")
	}
	return nil
}

// Only a genuine completed native finalizer permits the post-bootstrap workload
// and acceptance tail to use the original command's unchanged overall deadline.
func managedLateContextAfterFinalization(ctx context.Context, runtime environmentCreateBootstrapRuntime) (context.Context, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r != nil && r.completedTail != nil {
		return managedLateCompletedTailContext(ctx, runtime)
	}
	if r == nil || r.ManagedLateSession == nil {
		return ctx, nil
	}
	budget, ok := ctx.Value(managedLateBudgetKey{}).(managedLateRuntimeBudget)
	if !ok || !managedLateRuntimeBudgetValid(budget) || budget.Overall == nil || budget.Overall.Err() != nil || e == nil || budget.StartedAt.IsZero() {
		return nil, createOpenBaoACKBlock("late-finalized-context")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	intent := deploymentplan.Object{"session_id": r.Ceremony.BootstrapSessionID, "issuer_id": r.Ceremony.Intermediate.ID, "cleanup_binding": r.Ceremony.CleanupBinding}
	var receipt struct {
		Native environmentCreateBootstrapFinalization `json:"native_finalization"`
		SHA    string                                 `json:"custody_tombstone_sha256"`
		Absent bool                                   `json:"copies_absent"`
	}
	if entry == nil || entry.State != "completed" || entry.Phase != "bootstrap" || entry.StartedAt.Before(budget.StartedAt) || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(budget.Deadline) || entry.CompletedAt.After(time.Now()) || entry.IntentSHA256 != activeTransportJournalDigest(intent) || r.Ceremony.BootstrapSessionID != r.ManagedLateSession.NewSessionID || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !receipt.Absent || !environmentPreflightDigest.MatchString(receipt.SHA) || receipt.Native.Environment != e.Journal.Environment || receipt.Native.SessionID != r.ManagedLateSession.NewSessionID || !environmentPreflightDigest.MatchString(receipt.Native.LeafSHA256) || !environmentPreflightDigest.MatchString(receipt.Native.CRLSHA256) || !environmentPreflightDigest.MatchString(receipt.Native.CleanupSHA256) || receipt.Native.FinalizedAt.Before(entry.StartedAt) || receipt.Native.FinalizedAt.After(entry.CompletedAt) {
		return nil, createOpenBaoACKBlock("late-finalized-context")
	}
	if a := budget.TerminalAuthority; a != nil && (receipt.Native.FinalizedAt.Before(a.StartedAt) || entry.CompletedAt.Before(a.StartedAt) || entry.CompletedAt.After(a.Deadline)) {
		return nil, createOpenBaoACKBlock("terminal-finalizer-completed-context")
	}
	if budget.ResidualAuthority != nil {
		deadline, bounded := ctx.Deadline()
		remaining := r.ManagedLateRemaining
		if !bounded || !deadline.Equal(budget.Deadline) || remaining == nil || remaining.ResidualCleanup == nil || deploymentplan.Digest(managedLateResidualEffectiveAuthority(remaining)) != deploymentplan.Digest(budget.ResidualAuthority) {
			return nil, createOpenBaoACKBlock("residual-cleanup-completed-context")
		}
		if ctx.Err() != nil {
			if ctx.Err() != context.DeadlineExceeded || time.Now().Before(deadline) {
				return nil, ctx.Err()
			}
			if err := e.Check(budget.Overall); err != nil {
				return nil, err
			}
			// Cleanup is already complete. Recheck its existing durable proof
			// under the still-live overall context without publishing any file.
			var completed createManagedLateTerminalFinalizerCompleted
			var terminal createManagedLateTerminalFinalizerAuthority
			if createOpenBaoACKCompletionRead(e.Store, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256), &completed) != nil || !managedLateResidualCompletedBudgetMatches(completed, budget.ResidualAuthority) || createOpenBaoACKCompletionRead(e.Store, managedLateTerminalFinalizerPath(e.Journal.PlanSHA256), &terminal) != nil {
				return nil, createOpenBaoACKBlock("residual-cleanup-existing-completed-proof")
			}
			if err := validateManagedLateTerminalFinalizerCompletion(e.Store, e, r.ManagedLateSession); err != nil {
				return nil, err
			}
			if err := e.Check(budget.Overall); err != nil {
				return nil, err
			}
			return budget.Overall, nil
		}
	}
	if err := completeManagedLateTerminalFinalizer(ctx, runtime); err != nil {
		return nil, err
	}
	return budget.Overall, nil
}

// Read-only qualification precedes the first installation admission. The
// immutable clock committed by that admission is reused without resetting its
// epoch, original caller bound, or genuine certificate/session expiry reserve.
func beginManagedLatePostMintBudget(ctx context.Context, materials map[string]string, post *createManagedLatePostMint) (context.Context, context.CancelFunc, error) {
	now := time.Now().UTC()
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || post == nil || post.InstallationPolicy != managedLateInstallationPolicy || !post.NativeStartedAt.IsZero() || !post.NativeDeadline.IsZero() {
		return nil, nil, createOpenBaoACKBlock("late-post-mint-fixed-budget")
	}
	clock := post.InstallationClock
	if !managedLateInstallationClockValid(clock) || clock.StartedAt.After(now) || !now.Before(clock.Deadline) || clock.Review != post.Review || clock.SourceSHA != post.SourceSHA || clock.OwnerSHA != post.SourceOwnerSHA || clock.DescriptorSHA != managedLatePostMintDigest(post) || clock.SessionID != post.Ceremony.BootstrapSessionID || clock.Fingerprint != post.Fingerprint || !clock.SessionExpires.Equal(post.SessionExpires) || !clock.LeafNotAfter.Equal(post.LeafNotAfter) {
		return nil, nil, createOpenBaoACKBlock("late-post-mint-fixed-budget")
	}
	overall, bounded := ctx.Deadline()
	if !bounded || time.Until(clock.Deadline) < managedLateFinalizerLimit || time.Until(overall) < managedLateFinalizerLimit {
		return nil, nil, createOpenBaoACKBlock("late-post-mint-fixed-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	if materials["accepted-service-core-late-post-mint-native-budget"] != managedLatePostMintBudgetPin(post) {
		return nil, nil, createOpenBaoACKBlock("late-post-mint-fixed-budget-pin")
	}
	installationOverall, cancel := context.WithDeadline(ctx, clock.Deadline)
	budget := managedLateRuntimeBudget{StartedAt: clock.StartedAt, Deadline: clock.Deadline, Overall: installationOverall, Limit: managedLateOverallLimit, PostMint: true, InstallationClock: clock}
	native := context.WithValue(installationOverall, managedLateBudgetKey{}, budget)
	return native, cancel, nil
}
