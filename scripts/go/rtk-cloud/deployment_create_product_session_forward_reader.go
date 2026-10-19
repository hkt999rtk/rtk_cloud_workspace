package main

import (
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A terminal original Job remains failed history. Only its exact recorded
// custody/forward/retirement children can complete the logical session stage.
func (r *productStageReader) sessionTrustForward(original *deploymentplan.Plan, intent json.RawMessage) (bool, bool) {
	entry := r.entries[r.n]
	var alias environmentProductSessionForwardReceipt
	_ = decodeEnvironmentJSON(entry.Receipt, &alias)
	selected := alias.ActualForwardStage != ""
	for _, suffix := range []string{"custody", "custody-retire", "forward"} {
		selected = selected || r.n+1 < len(r.entries) && r.entries[r.n+1].Key == "kubernetes/"+productSessionForwardStage(r.l, suffix)
	}
	if !selected {
		return false, false
	}
	originalStage := "product-identities-" + productIdentityScope(r.l)[:12] + "-service-session"
	if entry.Key != "kubernetes/"+originalStage || entry.Phase != originalStage {
		r.bad("forward is outside its original logical session stage")
		return true, false
	}
	p, journal, service, err := readProductSessionForwardOriginal(r.store, r.e, r.l)
	if err != nil || p == nil || p.SHA256() != original.SHA256() {
		r.bad("forward lost its original failed session child")
		return true, false
	}
	var f *environmentProductSessionForward
	for _, next := range r.entries[r.n+1:] {
		if next.Key == "kubernetes/"+productSessionForwardStage(r.l, "forward") || next.Key == "kubernetes/"+productSessionForwardStage(r.l, "custody-retire") {
			f, err = readProductSessionForward(r.store, r.e, r.l)
			if err != nil || f == nil {
				r.bad("forward has no immutable original key and trust proof")
				return true, false
			}
			break
		}
	}
	r.children = append(r.children, p) // original completed PVC/SA owners remain authoritative
	consumed := 0
	previous := entry.StartedAt
	var forward *deploymentplan.Plan
	allComplete := true
	for _, suffix := range []string{"custody", "custody-retire"} {
		at := r.n + 1 + consumed
		if at == len(r.entries) {
			allComplete = false
			break
		}
		stage := productSessionForwardStage(r.l, suffix)
		if r.entries[at].Key != "kubernetes/"+stage {
			r.bad("unexpected session forward suffix")
			return true, false
		}
		var definitions []deploymentplan.Definition
		switch suffix {
		case "custody":
			definitions, err = productSessionForwardCustodyDefinitions(r.l, service, "", false)
		case "forward":
			if f == nil {
				r.bad("forward has no immutable original key and trust proof")
				return true, false
			}
			definitions, err = productSessionForwardDefinitions(r.l, p, f)
		case "custody-retire":
			if f == nil {
				r.bad("custody retirement has no original owner proof")
				return true, false
			}
			definitions, err = productSessionForwardCustodyDefinitions(r.l, service, f.CustodyUID, true)
		}
		if err != nil {
			r.err = err
			return true, false
		}
		child, complete, err := r.sessionForwardChild(at, stage, definitions, previous, entry)
		if err != nil {
			r.err = err
			return true, false
		}
		consumed++
		if child != nil {
			r.children = append(r.children, child)
		}
		if suffix == "forward" {
			forward = child
		}
		if !complete {
			allComplete = false
			break
		}
		previous = r.entries[at].CompletedAt
	}
	winnerStage := productSessionForwardStage(r.l, "forward")
	if allComplete && consumed == 2 {
		initial, initialErr := readCreateNormalTailPreparationPlan(r.store, r.e, winnerStage)
		definitions, definitionErr := productSessionInitialDefinitions(r.l, p, f, initial)
		if initialErr != nil {
			r.err = initialErr
			return true, false
		}
		type failedParent struct {
			entry environmentCreateJournalEntry
			p     *deploymentplan.Plan
			j     deploymentplan.Journal
		}
		var failures []failedParent
		for remaining := len(r.entries) + 1; remaining > 0; remaining-- {
			at := r.n + 1 + consumed
			if at == len(r.entries) {
				allComplete = false
				break
			}
			if definitionErr != nil || r.entries[at].Key != "kubernetes/"+winnerStage {
				r.bad("unexpected original attempt suffix")
				return true, false
			}
			saved, readErr := readCreateNormalTailPreparationPlan(r.store, r.e, winnerStage)
			if readErr != nil || saved == nil {
				r.bad("attempt lost original preparation")
				return true, false
			}
			j, exists, readErr := saved.ReadExecution(r.store.Root)
			failedNext := false
			var attempt *environmentProductSessionAttempt
			if readErr == nil && exists && productSessionAttemptFailed(saved, j) && at+1 < len(r.entries) {
				attempt, readErr = readProductSessionAttempt(r.store, r.e, r.l, f, winnerStage, saved, j)
				if readErr != nil || attempt == nil {
					r.bad("failed attempt has no original pre-mint proof")
					return true, false
				}
				failedNext = true
				failures = append(failures, failedParent{r.entries[at], saved, j})
			}
			child, complete, readErr := r.sessionForwardChild(at, winnerStage, definitions, previous, entry, failedNext)
			if readErr != nil {
				r.err = readErr
				return true, false
			}
			consumed++
			if child != nil {
				r.children = append(r.children, child)
			}
			if failedNext {
				previous = attempt.ObservedAt
				definitions, definitionErr = productSessionAttemptDefinitions(r.l, p, f, saved)
				winnerStage = productSessionAttemptStage(r.l, saved)
				continue
			}
			forward = child
			allComplete = complete
			if complete {
				previous = r.entries[at].CompletedAt
			}
			break
		}
		for _, failed := range failures {
			receipt, checkErr := productSessionSuperseded(failed.entry, failed.p, failed.j, f)
			if checkErr != nil {
				r.err = checkErr
				return true, false
			}
			if entry.State != "pending" && failed.entry.State != "completed" {
				r.bad("logical session closed before failed parent supersession")
				return true, false
			}
			if receipt.Outcome != "" {
				if !allComplete || forward == nil || receipt.WinnerStage != winnerStage || receipt.WinnerPlanSHA256 != forward.SHA256() || failed.entry.State == "completed" && failed.entry.CompletedAt.Before(previous) {
					r.bad("superseded failure lost its genuine accepted winner")
					return true, false
				}
				if failed.entry.State == "completed" && failed.entry.CompletedAt.After(previous) {
					previous = failed.entry.CompletedAt
				}
			}
		}
	}

	if entry.State != "pending" {
		if !allComplete || consumed < 3 || f == nil || forward == nil {
			r.bad("logical session completion precedes genuine forward cleanup")
			return true, false
		}
		want := environmentProductSessionForwardReceipt{
			OriginalPlanSHA256: p.SHA256(), OriginalFailedJournalSHA256: deploymentplan.Digest(journal),
			ActualForwardStage: winnerStage, ActualForwardPlanSHA256: forward.SHA256(), SourceSHA256: deploymentplan.Digest(f),
		}
		if decodeEnvironmentJSON(entry.Receipt, &alias) != nil || alias != want || entry.State == "completed" && entry.CompletedAt.Before(previous) {
			r.bad("logical session receipt changed the original failed or actual forward child")
			return true, false
		}
	}
	return true, r.takeNested(entry.Key, entry.Phase, intent, nil, consumed)
}

func (r *productStageReader) sessionForwardChild(at int, stage string, definitions []deploymentplan.Definition, previous time.Time, outer environmentCreateJournalEntry, knownFailedSuccessor ...bool) (*deploymentplan.Plan, bool, error) {
	bad := func() (*deploymentplan.Plan, bool, error) {
		return nil, false, productSessionForwardBlock("nested child recipe, owner, chronology or outcome changed")
	}
	entry := r.entries[at]
	p, err := readCreateNormalTailPreparationPlan(r.store, r.e, stage)
	if err != nil || p == nil || p.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, definitions) || len(p.Resources) != len(definitions) {
		return bad()
	}
	wanted := map[string]deploymentplan.Definition{}
	for _, d := range definitions {
		wanted[deploymentplan.ID(d.Object)] = d
	}
	for _, resource := range p.Resources {
		d, ok := wanted[resource.ID]
		if !ok || resource.Phase != d.Phase || !deploymentplan.Matches(deploymentplan.Projection(d.Object), resource.Desired) || d.Retire && (resource.Action != "retire" || resource.UID != deploymentplan.Map(d.Object["metadata"])["uid"]) || !d.Retire && (resource.Action != "create" || resource.UID != "" || resource.Baseline != nil) {
			return bad()
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil || entry.IntentSHA256 != activeTransportJournalDigest(jsonRawObject(intent)) || entry.Phase != stage || entry.StartedAt.Before(previous) || entry.StartedAt.Before(r.admission.AdmittedAt) || !entry.StartedAt.Before(r.admission.Deadline) || entry.StartedAt.After(time.Now().UTC()) {
		return bad()
	}
	view := r.e
	if len(knownFailedSuccessor) == 1 && knownFailedSuccessor[0] {
		copyE := *r.e
		copyE.Journal = r.e.Journal
		copyE.Journal.Entries = append([]environmentCreateJournalEntry(nil), r.e.Journal.Entries...)
		for i := range copyE.Journal.Entries {
			if copyE.Journal.Entries[i].Key == entry.Key {
				copyE.Journal.Entries[i].State = "pending"
				copyE.Journal.Entries[i].Receipt = nil
				copyE.Journal.Entries[i].CompletedAt = time.Time{}
			}
		}
		view = &copyE
	}
	child, _, err := readCreateNormalTailStage(r.store, view, stage, p)
	if err != nil {
		return bad()
	}
	if len(knownFailedSuccessor) == 1 && knownFailedSuccessor[0] {
		// The typed failed journal/source is checked by the caller. Its parent may
		// truthfully close only as superseded; it never becomes an accepted child.
		if entry.State == "completed" && (!entry.CompletedAt.Before(r.admission.Deadline) || entry.CompletedAt.After(time.Now().UTC()) || outer.State == "completed" && entry.CompletedAt.After(outer.CompletedAt)) {
			return bad()
		}
		return child, false, nil
	}
	switch entry.State {
	case "pending":
		if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() || at+1 != len(r.entries) && !(len(knownFailedSuccessor) == 1 && knownFailedSuccessor[0]) {
			return bad()
		}
	case "allocated":
		if child == nil || len(entry.Receipt) == 0 || !entry.CompletedAt.IsZero() || at+1 != len(r.entries) {
			return bad()
		}
	case "completed":
		if child == nil || len(entry.Receipt) == 0 || entry.CompletedAt.Before(entry.StartedAt) || !entry.CompletedAt.Before(r.admission.Deadline) || entry.CompletedAt.After(time.Now().UTC()) || outer.State == "completed" && entry.CompletedAt.After(outer.CompletedAt) {
			return bad()
		}
		return child, true, nil
	default:
		return bad()
	}
	return child, false, nil
}
