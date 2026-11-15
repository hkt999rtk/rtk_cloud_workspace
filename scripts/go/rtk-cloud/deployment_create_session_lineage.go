package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const sessionLineagePrefix = "session-lineage-"

func sessionLineageIntent(review string, q secondSessionAdmission) deploymentplan.Object {
	return deploymentplan.Object{"qualification_sha256": review, "parent": q.Renewal.ParentSHA, "previous_review": q.Renewal.PreviousReview, "previous_session": q.Renewal.Intent.SessionID, "successor_session": q.Renewal.RenewedIntent.SessionID, "candidate_sha256": q.Renewal.Candidate.SHA256()}
}

func sessionLineageLinkValid(q secondSessionAdmission, firstReview string, root *partialSessionContinuationProof, previousReview string, previousIntent createServiceSessionIntent, previousBinding string, previousClients []string) bool {
	if root == nil || q.Renewal.OriginalReview != firstReview || q.Renewal.PreviousReview != previousReview || q.Renewal.PreviousBindingSHA != previousBinding || deploymentplan.Digest(q.Renewal.Intent) != deploymentplan.Digest(previousIntent) || q.Renewal.KeySHA != root.KeySHA || q.Renewal.CSRSHA != root.CSRSHA || q.Renewal.SourceStage != root.Recovery.SourceStage || q.Renewal.SourcePlanSHA != root.Recovery.SourcePlanSHA256 || q.Renewal.SourceJournalSHA != root.Recovery.SourceJournalSHA256 {
		return false
	}
	for _, digest := range previousClients {
		if !slices.Contains(q.Renewal.ClientDigests, digest) {
			return false
		}
	}
	return true
}

// Read one unbranched chain from the original owner journal. Qualification
// files that were never admitted are deliberately not considered.
func sessionLineageHead(store secretStore, e *environmentCreateExecution) (*secondSessionAdmission, string, error) {
	return sessionLineageHeadWithLoader(store, e, newSecondSessionAdmissionReader(store, e))
}

func sessionLineageHeadWithLoader(store secretStore, e *environmentCreateExecution, load func(string) (secondSessionAdmission, error)) (*secondSessionAdmission, string, error) {
	bad := func() (*secondSessionAdmission, string, error) {
		return nil, "", createProviderRecoveryBlock("session renewal lineage differs from the original parent journal")
	}
	first, firstReview, ok, err := loadPartialSessionAdmission(store, e)
	if err != nil || !ok || first.PartialSession == nil {
		return bad()
	}
	root := first.PartialSession
	previousReview, previousIntent, previousBinding := firstReview, root.RenewedIntent, root.Recovery.BindingSHA256
	previousClients := root.Recovery.OriginalClientDigests
	var head *secondSessionAdmission
	seen := map[string]bool{firstReview: true}
	for _, entry := range e.Journal.Entries {
		const batchPrefix = "service-session-batches-"
		lineage, batch := strings.HasPrefix(entry.Key, sessionLineagePrefix), strings.HasPrefix(entry.Key, batchPrefix)
		if !lineage && !batch {
			continue
		}
		prefix, phase := sessionLineagePrefix, "session-lineage"
		if batch {
			prefix, phase = batchPrefix, "service-session-batches"
		}
		review := strings.TrimPrefix(entry.Key, prefix)
		if !environmentPreflightDigest.MatchString(review) || entry.Phase != phase || !partialSessionEntryAdvances(entry, entry) {
			return bad()
		}
		q, err := load(review)
		if err != nil || entry.StartedAt.Before(q.QualifiedAt) || batch && q.Batches == nil {
			return bad()
		}
		deadline, deadlineErr := sessionEffectiveDeadline(context.Background(), store, e, q, entry.StartedAt)
		if deadlineErr != nil || !entry.StartedAt.Before(deadline) {
			return bad()
		}
		intent := mustProviderRecoveryJSON(sessionLineageIntent(review, q))
		if batch {
			intent = serviceSessionBatchIntent(review, q)
		}
		intentSHA, err := environmentCreatePlanSHA256(intent)
		if err != nil || entry.IntentSHA256 != intentSHA {
			return bad()
		}
		// A native batch intent is already execution admission. If the process
		// stopped before mint registration, recover that same qualified intent.
		// The later lineage marker is a second record of it, not a new branch.
		if seen[review] {
			if head == nil || review != previousReview || deploymentplan.Digest(*head) != deploymentplan.Digest(q) {
				return bad()
			}
			continue
		}
		if !sessionLineageLinkValid(q, firstReview, root, previousReview, previousIntent, previousBinding, previousClients) {
			return bad()
		}
		seen[review] = true
		copy := q
		head, previousReview, previousIntent, previousBinding = &copy, review, q.Renewal.RenewedIntent, q.Renewal.BindingSHA
		previousClients = q.Renewal.ClientDigests
	}
	return head, previousReview, nil
}

// Admit an already-qualified successor once. Retrying the same durable entry
// is idempotent; a stale or competing branch cannot replace the latest owner.
func beginSessionLineage(store secretStore, e *environmentCreateExecution, q secondSessionAdmission, review string, now time.Time) error {
	if e == nil || e.Store != store || deploymentplan.Digest(q) != review || !validSecondSessionAdmission(store, e, q) || now.Before(q.QualifiedAt) || e.Check(context.Background()) != nil {
		return createProviderRecoveryBlock("qualified session lineage admission is required")
	}
	deadline, err := sessionEffectiveDeadline(context.Background(), store, e, q, now)
	if err != nil || !now.Before(deadline) {
		return createProviderRecoveryBlock("qualified session lineage deadline is unavailable")
	}
	prior, head, err := sessionLineageHead(store, e)
	if err != nil {
		return err
	}
	if head != review && q.Renewal.PreviousReview != head {
		return createProviderRecoveryBlock("a different session lineage successor is already admitted")
	}
	if head != review {
		first, firstReview, ok, err := loadPartialSessionAdmission(store, e)
		if err != nil || !ok || first.PartialSession == nil {
			return createProviderRecoveryBlock("original session lineage is unavailable")
		}
		root := first.PartialSession
		intent, binding, clients := root.RenewedIntent, root.Recovery.BindingSHA256, root.Recovery.OriginalClientDigests
		if prior != nil {
			intent, binding, clients = prior.Renewal.RenewedIntent, prior.Renewal.BindingSHA, prior.Renewal.ClientDigests
		}
		if !sessionLineageLinkValid(q, firstReview, root, head, intent, binding, clients) {
			return createProviderRecoveryBlock("successor differs from the original session lineage")
		}
	}
	if createOpenBaoACKCompletionEntry(e.Journal, sessionLineagePrefix+review) == nil {
		if err := e.BeginEntry(sessionLineagePrefix+review, "session-lineage", mustProviderRecoveryJSON(sessionLineageIntent(review, q)), now); err != nil {
			return err
		}
	}
	// This entry records only admission of the exact qualified intent. Native
	// Job, credential and ACK outcomes retain their own independent receipts.
	entry := createOpenBaoACKCompletionEntry(e.Journal, sessionLineagePrefix+review)
	if entry == nil {
		return createProviderRecoveryBlock("session lineage admission entry is unavailable")
	}
	receipt := mustProviderRecoveryJSON(sessionLineageIntent(review, q))
	if entry.State == "pending" {
		if err := e.RecordReceipt(entry.Key, receipt); err != nil {
			return err
		}
		entry = createOpenBaoACKCompletionEntry(e.Journal, entry.Key)
	}
	if entry == nil || !samePersistedJSONReceipt(entry.Receipt, receipt) {
		return createProviderRecoveryBlock("session lineage admission receipt changed")
	}
	if entry.State == "allocated" {
		return e.CompleteEntry(entry.Key, now)
	}
	if entry.State != "completed" {
		return createProviderRecoveryBlock("session lineage admission is incomplete")
	}
	return nil
}
