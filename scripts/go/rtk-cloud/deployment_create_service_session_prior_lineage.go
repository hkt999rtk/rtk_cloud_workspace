package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
)

// Derive every potentially installed session from authenticated frozen
// lineage, including predecessors whose workload batch was interrupted.
func serviceSessionPriorLineage(store secretStore, e *environmentCreateExecution, q secondSessionAdmission) ([]string, []secondSessionAdmission, error) {
	return serviceSessionPriorLineageWithLoader(store, e, q, newSecondSessionAdmissionReader(store, e))
}

func serviceSessionPriorLineageWithLoader(store secretStore, e *environmentCreateExecution, q secondSessionAdmission, load func(string) (secondSessionAdmission, error)) ([]string, []secondSessionAdmission, error) {
	bad := func() ([]string, []secondSessionAdmission, error) {
		return nil, nil, createProviderRecoveryBlock("Service session predecessor lineage changed")
	}
	r := q.Renewal
	if e == nil || !environmentBootstrapOwnerUUID.MatchString(r.OriginalState.Session) || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) {
		return bad()
	}
	reverse := []string{r.Intent.SessionID}
	var history []secondSessionAdmission
	review := r.PreviousReview
	expectedIntent, expectedBinding := r.Intent, r.PreviousBindingSHA
	seen := map[string]bool{r.OriginalState.Session: true, r.Intent.SessionID: true}
	for review != r.OriginalReview {
		if !environmentPreflightDigest.MatchString(review) {
			return bad()
		}
		h, err := load(review)
		if err != nil || h.Renewal.OriginalReview != r.OriginalReview || h.Renewal.RenewedIntent.SessionID != reverse[len(reverse)-1] || deploymentplan.Digest(h.Renewal.RenewedIntent) != deploymentplan.Digest(expectedIntent) || h.Renewal.BindingSHA != expectedBinding || h.Renewal.KeySHA != r.KeySHA || h.Renewal.CSRSHA != r.CSRSHA {
			return bad()
		}
		entry := createOpenBaoACKCompletionEntry(q.SourceJournal, sessionLineagePrefix+review)
		intent, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(sessionLineageIntent(review, h)))
		if err != nil || entry == nil || entry.Phase != "session-lineage" || entry.State != "completed" || entry.IntentSHA256 != intent || !samePersistedJSONReceipt(entry.Receipt, mustProviderRecoveryJSON(sessionLineageIntent(review, h))) {
			return bad()
		}
		prior := h.Renewal.Intent.SessionID
		if !environmentBootstrapOwnerUUID.MatchString(prior) || seen[prior] {
			return bad()
		}
		seen[prior] = true
		reverse = append(reverse, prior)
		history = append(history, h)
		expectedIntent, expectedBinding = h.Renewal.Intent, h.Renewal.PreviousBindingSHA
		review = h.Renewal.PreviousReview
	}
	slices.Reverse(reverse)
	return append([]string{r.OriginalState.Session}, reverse...), history, nil
}

func serviceSessionBatchPrevious(q secondSessionAdmission) []string {
	ids := make([]string, 0, len(q.Batches))
	for _, b := range q.Batches {
		ids = append(ids, b.Previous)
	}
	return ids
}

func sameSessionList(a, b []string) bool { return deploymentplan.Digest(a) == deploymentplan.Digest(b) }

// Preserve all observed installations even if an intermediate batch expired
// before it collected every prior consumer's ACK.
func serviceSessionRequiredACKs(q secondSessionAdmission, history []secondSessionAdmission) []string {
	var out []string
	out = append(out, q.Renewal.OriginalState.Installed...)
	out = append(out, q.Renewal.PreviousState.Installed...)
	for _, prior := range history {
		out = append(out, prior.Renewal.PreviousState.Installed...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
