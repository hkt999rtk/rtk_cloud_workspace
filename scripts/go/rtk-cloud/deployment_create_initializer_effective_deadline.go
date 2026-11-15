package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Preserve the original frozen proof deadline for its historical validation.
// A later runtime deadline is usable only when the original issuer admission
// and its explicit, monotonic supplement belong to this same generation.
func initializerEffectiveDeadline(ctx context.Context, store secretStore, e *environmentCreateExecution, q createInitializerForwardProof, now time.Time) (time.Time, error) {
	bad := func() (time.Time, error) {
		return time.Time{}, createProviderRecoveryBlock("initializer continuation deadline lacks its original issuer admission")
	}
	if e == nil || now.IsZero() || q.ExecutionDeadline.IsZero() {
		return bad()
	}
	original, found, err := initialSuccessorContinuationEntry(store, e)
	if err != nil {
		return bad()
	}
	if !found {
		if now.Before(q.ExecutionDeadline) {
			return q.ExecutionDeadline, nil
		}
		return bad()
	}
	frozen, found, err := readIssuerResumeDeadline(store, e, q.OriginalAdmissionSHA256, original)
	if err != nil {
		return bad()
	}
	if !found {
		if now.Before(q.ExecutionDeadline) {
			return q.ExecutionDeadline, nil
		}
		return bad()
	}
	effective, found, err := effectiveIssuerDeadlineAt(ctx, store, e, q.OriginalAdmissionSHA256, original, now)
	if err != nil || !found || effective.Before(frozen) || q.ExecutionDeadline.Before(frozen) || q.ExecutionDeadline.After(effective) {
		return bad()
	}
	if !frozen.Equal(q.ExecutionDeadline) {
		// A new q can be qualified after an extension, but its original source
		// snapshot must already contain the authenticated earlier supplement.
		if !partialSessionJournalExtends(q.SourceJournal, e.Journal) {
			return bad()
		}
		matched := false
		for _, entry := range q.SourceJournal.Entries {
			if entry.Phase != "issuer-deadline-supplement" || entry.State != "completed" || entry.CompletedAt.After(q.QualifiedAt) {
				continue
			}
			var s issuerDeadlineSupplement
			if json.Unmarshal(entry.Receipt, &s) == nil && s.Review == q.OriginalAdmissionSHA256 && s.Deadline.Equal(q.ExecutionDeadline) && entry.Key == issuerDeadlineSupplementKey(s.Review, s) {
				matched = true
			}
		}
		if !matched {
			return bad()
		}
	}
	return effective, nil
}

// Resolve from the admitted initializer, not from the current session's
// immutable historical deadline. Its source admission identifies the single
// supplement chain shared by preflight and execution.
func currentInitializerEffectiveDeadline(ctx context.Context, store secretStore, e *environmentCreateExecution, now time.Time) (time.Time, error) {
	var review string
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "initializer-forward" && strings.HasPrefix(entry.Key, "initializer-forward-") {
			if review != "" {
				return time.Time{}, createProviderRecoveryBlock("initializer deadline admission is ambiguous")
			}
			review = strings.TrimPrefix(entry.Key, "initializer-forward-")
		}
	}
	if review == "" {
		return time.Time{}, createProviderRecoveryBlock("original initializer deadline admission is unavailable")
	}
	q, _, _, err := loadInitializerForwardBundle(store, e, review)
	if err != nil {
		return time.Time{}, err
	}
	return initializerEffectiveDeadline(ctx, store, e, q, now)
}

func sessionEffectiveDeadline(ctx context.Context, store secretStore, e *environmentCreateExecution, q secondSessionAdmission, now time.Time) (time.Time, error) {
	if now.IsZero() || q.ExecutionDeadline.IsZero() {
		return time.Time{}, createProviderRecoveryBlock("original session deadline is unavailable")
	}
	if limit, ok := ctx.Deadline(); now.Before(q.ExecutionDeadline) && (!ok || !limit.After(q.ExecutionDeadline)) {
		return q.ExecutionDeadline, nil
	}
	deadline, err := currentInitializerEffectiveDeadline(ctx, store, e, now)
	if err != nil || deadline.Before(q.ExecutionDeadline) {
		return time.Time{}, createProviderRecoveryBlock("session execution deadline lacks its original supplement")
	}
	return deadline, nil
}
