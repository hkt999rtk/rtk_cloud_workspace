package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// An extension changes only the absolute deadline: the generation, original
// admission and its start remain unchanged. Older approvals are never edited.
type issuerDeadlineSupplement struct {
	Parent, Owner, Review, Original    string
	StartedAt, PriorDeadline, Deadline time.Time
}

const issuerDeadlineSupplementPrefix = "issuer-deadline-supplement-"

func issuerDeadlineSupplementKey(review string, s issuerDeadlineSupplement) string {
	return issuerDeadlineSupplementPrefix + review + "-" + deploymentplan.Digest(s)
}

func effectiveIssuerDeadline(ctx context.Context, store secretStore, e *environmentCreateExecution, review string, original environmentCreateJournalEntry) (time.Time, bool, error) {
	return resolveIssuerDeadline(ctx, store, e, review, original, "", time.Time{})
}

func effectiveIssuerDeadlineAt(ctx context.Context, store secretStore, e *environmentCreateExecution, review string, original environmentCreateJournalEntry, at time.Time) (time.Time, bool, error) {
	return resolveIssuerDeadline(ctx, store, e, review, original, "", at)
}

func resolveIssuerDeadline(ctx context.Context, store secretStore, e *environmentCreateExecution, review string, original environmentCreateJournalEntry, unfinishedKey string, at time.Time) (time.Time, bool, error) {
	bad := func() (time.Time, bool, error) {
		return time.Time{}, false, createProviderRecoveryBlock("original issuer deadline supplement differs from its admitted chain")
	}
	deadline, found, err := readIssuerResumeDeadline(store, e, review, original)
	if err != nil || !found {
		return deadline, found, err
	}
	seen := map[string]bool{}
	for _, entry := range e.Journal.Entries {
		if !strings.HasPrefix(entry.Key, issuerDeadlineSupplementPrefix+review+"-") {
			continue
		}
		if entry.Phase != "issuer-deadline-supplement" {
			return bad()
		}
		if !at.IsZero() && entry.StartedAt.After(at) {
			continue
		}
		// A pending or allocated record is not authority; the writer must finish
		// that exact entry before any later extension can take effect.
		if entry.State != "completed" {
			if unfinishedKey == entry.Key && (entry.State == "pending" || entry.State == "allocated") {
				continue
			}
			return bad()
		}
		if !at.IsZero() && entry.CompletedAt.After(at) {
			continue
		}
		var s issuerDeadlineSupplement
		if json.Unmarshal(entry.Receipt, &s) != nil {
			return bad()
		}
		digest := deploymentplan.Digest(s)
		intent, ierr := environmentCreatePlanSHA256(mustProviderRecoveryJSON(s))
		if ierr != nil || entry.Key != issuerDeadlineSupplementKey(review, s) || seen[digest] || entry.IntentSHA256 != intent || s.Parent != e.Journal.PlanSHA256 || s.Owner != deploymentplan.Digest(e.owner) || s.Review != review || s.Original != original.Key || !s.StartedAt.Equal(original.StartedAt) || !s.PriorDeadline.Equal(deadline) || !s.Deadline.After(s.PriorDeadline) || !s.Deadline.After(s.StartedAt) || entry.StartedAt.Before(s.StartedAt) || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.IsZero() {
			return bad()
		}
		seen[digest] = true
		deadline = s.Deadline
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return deadline, true, nil
}

// recordIssuerDeadlineSupplement is idempotent for the exact same original
// start and total duration; pending/allocated intent resumes without resetting
// the deadline. It never touches provider, Kubernetes or identities.
func recordIssuerDeadlineSupplement(ctx context.Context, store secretStore, e *environmentCreateExecution, review string, original environmentCreateJournalEntry, total time.Duration, now time.Time) (time.Time, error) {
	bad := func() (time.Time, error) {
		return time.Time{}, createProviderRecoveryBlock("issuer deadline extension requires the exact original admission and a later absolute deadline")
	}
	if e == nil || e.Store != store || total <= 0 || now.IsZero() || e.Check(ctx) != nil {
		return bad()
	}
	desired := original.StartedAt.Add(total)
	if !now.Before(desired) {
		return bad()
	}
	// A crashed writer can have only its own matching pending/allocated entry.
	var unfinished *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		entry := &e.Journal.Entries[i]
		if strings.HasPrefix(entry.Key, issuerDeadlineSupplementPrefix+review+"-") && entry.State != "completed" {
			if unfinished != nil {
				return bad()
			}
			unfinished = entry
		}
	}
	keyToSkip := ""
	if unfinished != nil {
		keyToSkip = unfinished.Key
	}
	prior, found, err := resolveIssuerDeadline(ctx, store, e, review, original, keyToSkip, time.Time{})
	if err != nil || !found {
		return bad()
	}
	if unfinished != nil {
		s := issuerDeadlineSupplement{Parent: e.Journal.PlanSHA256, Owner: deploymentplan.Digest(e.owner), Review: review, Original: original.Key, StartedAt: original.StartedAt, PriorDeadline: prior, Deadline: desired}
		raw := mustProviderRecoveryJSON(s)
		intent, ierr := environmentCreatePlanSHA256(raw)
		if !desired.After(prior) || ierr != nil || unfinished.Phase != "issuer-deadline-supplement" || unfinished.Key != issuerDeadlineSupplementKey(review, s) || unfinished.IntentSHA256 != intent {
			return bad()
		}
		if unfinished.State == "pending" {
			if len(unfinished.Receipt) != 0 {
				return bad()
			}
			if err := e.RecordReceipt(unfinished.Key, raw); err != nil {
				return time.Time{}, err
			}
		} else if unfinished.State != "allocated" {
			return bad()
		} else {
			canonical, err := canonicalEnvironmentCreateJSON(raw)
			if err != nil || string(unfinished.Receipt) != string(canonical) {
				return bad()
			}
		}
		if err := e.CompleteEntry(unfinished.Key, now); err != nil {
			return time.Time{}, err
		}
		return desired, nil
	}
	if desired.Equal(prior) {
		return prior, nil
	}
	if !desired.After(prior) {
		return bad()
	}
	s := issuerDeadlineSupplement{Parent: e.Journal.PlanSHA256, Owner: deploymentplan.Digest(e.owner), Review: review, Original: original.Key, StartedAt: original.StartedAt, PriorDeadline: prior, Deadline: desired}
	key := issuerDeadlineSupplementKey(review, s)
	raw := mustProviderRecoveryJSON(s)
	if err := e.BeginEntry(key, "issuer-deadline-supplement", raw, now); err != nil {
		return time.Time{}, err
	}
	if err := e.RecordReceipt(key, raw); err != nil {
		return time.Time{}, err
	}
	if err := e.CompleteEntry(key, now); err != nil {
		return time.Time{}, err
	}
	got, found, err := effectiveIssuerDeadline(ctx, store, e, review, original)
	if err != nil || !found || !got.Equal(desired) {
		return bad()
	}
	return got, nil
}

// A previously frozen candidate can start after a supplemental authorization
// only when the supplement records are the sole changes to its source journal.
func issuerSupplementOnlyJournalAdvance(source, current environmentCreateJournal, review string) bool {
	if len(current.Entries) < len(source.Entries) {
		return false
	}
	for i := range source.Entries {
		if deploymentplan.Digest(source.Entries[i]) != deploymentplan.Digest(current.Entries[i]) {
			return false
		}
	}
	for _, entry := range current.Entries[len(source.Entries):] {
		if entry.Phase != "issuer-deadline-supplement" || entry.State != "completed" || !strings.HasPrefix(entry.Key, issuerDeadlineSupplementPrefix+review+"-") {
			return false
		}
	}
	copy := current
	copy.Entries = source.Entries
	return deploymentplan.Digest(copy) == deploymentplan.Digest(source)
}
