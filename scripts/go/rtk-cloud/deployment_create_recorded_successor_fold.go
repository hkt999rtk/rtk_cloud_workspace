package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
)

type completedRecordedSuccessorStage struct {
	Plan        *deploymentplan.Plan
	Journal     deploymentplan.Journal
	Written     bool                            // authenticated, recorded writes from an incomplete stage; not acceptance
	NativeWrite *activeTransportRenewalResource // exact completed native receipt, without a synthetic child journal
	Retired     map[string]string               // original UID-bound, completed native retirements
}

// Collapse authenticated recorded writes and accepted updates into each source's
// original before/latest desired pair without treating writes as acceptance.
func foldCompletedRecordedSuccessors(base []activeTransportRenewalResource, stages []completedRecordedSuccessorStage) ([]activeTransportRenewalResource, error) {
	boundary, resource, child := "base", "", ""
	bad := func() ([]activeTransportRenewalResource, error) {
		return nil, createProviderRecoveryBlock("completed owner successor lacks an accepted same-UID baseline chain; boundary=" + boundary + " resource=" + resource + " child=" + child)
	}
	out := append([]activeTransportRenewalResource(nil), base...)
	index := map[string]int{}
	for i, r := range out {
		resource = r.ID
		if r.ID == "" || r.UID == "" || r.Before == nil || r.Desired == nil || deploymentplan.ID(r.Desired) != r.ID {
			return bad()
		}
		if _, exists := index[r.ID]; exists {
			return bad()
		}
		index[r.ID] = i
	}
	for _, stage := range stages {
		if write := stage.NativeWrite; write != nil {
			boundary, resource, child = "native-transition", write.ID, "native"
			if stage.Plan != nil || write.ID == "" || write.UID == "" || write.Before == nil || write.Desired == nil || deploymentplan.ID(write.Before) != write.ID || deploymentplan.ID(write.Desired) != write.ID {
				return bad()
			}
			i, tracked := index[write.ID]
			if !tracked {
				continue
			}
			if out[i].UID != write.UID || !deploymentplan.Matches(out[i].Desired, write.Before) {
				return bad()
			}
			out[i].Desired = deploymentplan.Clone(write.Desired)
			continue
		}
		p, j := stage.Plan, stage.Journal
		boundary, resource, child = "child-journal", "", ""
		if p != nil {
			child = p.SHA256()[:12]
		}
		if p == nil || j.PlanSHA256 != p.SHA256() || j.InFlight != "" || len(j.Completed) > len(p.Resources) {
			return bad()
		}
		if !stage.Written && (!j.Accepted || j.Failed != "" || len(j.Completed) != len(p.Resources) || len(j.Owners) != len(p.Resources)) {
			return bad()
		}
		for n, r := range p.Resources {
			boundary, resource = "baseline", r.ID
			if n >= len(j.Completed) {
				// A failed resource can be folded only when its recorded owner is
				// independently authenticated by the caller.
				if !stage.Written || j.Failed != r.ID || j.Owners[r.ID] == "" {
					continue
				}
			} else if j.Completed[n] != r.ID {
				return bad()
			}
			i, ok := index[r.ID]
			if !ok {
				continue
			}
			current := out[i]
			if r.Action == "retire" && stage.Retired[r.ID] == current.UID && r.UID == current.UID {
				// A retirement is not an update to an empty desired body. Keep the
				// last live body; the caller verifies the authenticated absence.
				continue
			}
			if j.Owners[r.ID] != current.UID || r.UID != current.UID || r.Baseline == nil || !deploymentplan.Matches(current.Desired, r.Baseline) {
				return bad()
			}
			if r.Action == "retain" {
				boundary = "retained-body"
				// A no-write retain may explicitly capture Kubernetes defaults
				// missing from earlier intent. Its authenticated baseline already
				// preserves every previously owned field. Compare with that actual
				// baseline, retaining the accumulated original intent unchanged.
				if !deploymentplan.Matches(r.Desired, r.Baseline) {
					return bad()
				}
				continue
			}
			if r.Action != "update" || deploymentplan.ID(r.Desired) != r.ID {
				return bad()
			}
			out[i].Desired = deploymentplan.Clone(r.Desired)
		}
	}
	return out, nil
}

// Read only completed child plans that actually mention a tracked owner, in
// parent journal order after the admitted transport successor. Parent intent,
// receipt, child journal, UID and before/after linkage are all verified.
func completedRecordedTransportSuccessors(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, proof initialTransportSuccessorProof, base []activeTransportRenewalResource) ([]activeTransportRenewalResource, error) {
	boundary := "original-execution"
	bad := func() ([]activeTransportRenewalResource, error) {
		return nil, createProviderRecoveryBlock("completed recorded transport successor cannot be authenticated; boundary=" + boundary)
	}
	if e == nil || e.Store != store || b == nil || e.Check(ctx) != nil {
		return bad()
	}
	journalPath, err := store.safePath(proof.Journal)
	if err != nil || filepath.Clean(journalPath) != filepath.Clean(e.JournalPath) {
		return bad()
	}
	return completedRecordedOwnerSuccessorsAfter(ctx, store, e, b, "initial-transport-successor-"+proof.Review, base)
}

// Reuse the same authenticated timeline for any accepted native child. Its
// caller supplies that child's exact completed key and verified owner bodies.
func completedRecordedOwnerSuccessorsAfter(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, completedKey string, base []activeTransportRenewalResource, retirements ...map[string]string) ([]activeTransportRenewalResource, error) {
	pending, err := authenticatedRecordedPendingWrites(ctx, store, e, b)
	if err != nil {
		return nil, err
	}
	return completedRecordedOwnerSuccessorsAfterHistory(ctx, store, e, b, completedKey, base, pending, retirements...)
}

// The caller may share one authenticated history across owners during a single
// read-only observation. No result survives that observation or any write.
func completedRecordedOwnerSuccessorsAfterHistory(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, completedKey string, base []activeTransportRenewalResource, pending map[string]completedRecordedSuccessorStage, retirements ...map[string]string) ([]activeTransportRenewalResource, error) {
	return completedRecordedOwnerSuccessorsAfterHistoryBefore(ctx, store, e, b, completedKey, base, pending, "", retirements...)
}

// An immutable successor derives its source before its own first intent. Its
// later recorded writes remain outcomes, never replacement source authority.
func completedRecordedOwnerSuccessorsAfterHistoryBefore(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, completedKey string, base []activeTransportRenewalResource, pending map[string]completedRecordedSuccessorStage, stopBefore string, retirements ...map[string]string) ([]activeTransportRenewalResource, error) {
	boundary := "original-execution"
	bad := func() ([]activeTransportRenewalResource, error) {
		return nil, createProviderRecoveryBlock("completed recorded owner successor cannot be authenticated; boundary=" + boundary)
	}
	if e == nil || e.Store != store || b == nil || completedKey == "" || e.Check(ctx) != nil {
		return bad()
	}
	journal := e.Journal
	tracked := map[string]bool{}
	for _, r := range base {
		tracked[r.ID] = true
	}
	start := -1
	for i, entry := range journal.Entries {
		if entry.Key == completedKey && entry.State == "completed" {
			if start >= 0 {
				return bad()
			}
			start = i
		}
	}
	if start < 0 {
		return bad()
	}
	var stages []completedRecordedSuccessorStage
	for _, entry := range journal.Entries[start+1:] {
		if entry.Key == stopBefore {
			break
		}
		if strings.HasPrefix(entry.Key, "custody-controller-restore-") {
			write, err := completedRecordedCustodyControllerRestore(store, e, entry)
			if err != nil {
				return nil, err
			}
			stages = append(stages, completedRecordedSuccessorStage{NativeWrite: write})
			continue
		}
		if candidate, ok := pending[entry.Key]; ok {
			stages = append(stages, candidate)
			continue
		}
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		boundary = entry.Key
		var receipt struct {
			Plan            string            `json:"plan_sha256"`
			Owners          map[string]string `json:"owners"`
			Forward         string            `json:"actual_qualified_forward_stage"`
			Original        string            `json:"original_failed_plan_sha256"`
			OriginalJournal string            `json:"original_failed_journal_sha256"`
			ForwardPlan     string            `json:"actual_qualified_forward_plan_sha256"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil {
			return bad()
		}
		if productCRLForwardReceiptMatches(entry.Receipt) {
			var resolved productCRLForwardReceipt
			if json.Unmarshal(entry.Receipt, &resolved) != nil || entry.Key != "kubernetes/"+productPublicCRLStage {
				return bad()
			}
			source, err := readEnvironmentCreateStageInput(store, e, productPublicCRLStage)
			if err != nil || source == nil {
				return bad()
			}
			j, ok, err := source.ReadExecution(store.Root)
			if err != nil || !ok || validateProductCRLForwardReceipt(store, e, source, j, entry.Receipt) != nil {
				return bad()
			}
			receipt.Forward, receipt.Original, receipt.OriginalJournal, receipt.ForwardPlan = resolved.ForwardStage, resolved.OriginalPlan, resolved.OriginalJournal, resolved.ForwardPlan
		}
		if receipt.Forward != "" {
			// An alias is not another write or an accepted source. Verify its
			// original request and actual accepted child, then fold only any
			// native source writes recorded before that child ran.
			if receipt.Plan != "" || len(receipt.Owners) != 0 || !environmentPreflightDigest.MatchString(receipt.Original) || !strings.HasPrefix(receipt.Forward, "kubernetes/") || receipt.Forward == entry.Key {
				return bad()
			}
			source, err := readEnvironmentCreateStageInput(store, e, strings.TrimPrefix(entry.Key, "kubernetes/"))
			if err != nil || source == nil || source.SHA256() != receipt.Original {
				return bad()
			}
			intent, err := environmentCreateKubernetesStageIntent(source)
			if err != nil {
				return bad()
			}
			sha, err := environmentCreatePlanSHA256(intent)
			if err != nil || sha != entry.IntentSHA256 || entry.Phase != strings.TrimPrefix(entry.Key, "kubernetes/") {
				return bad()
			}
			child, childJournal, err := acceptedCreateStage(store, e, receipt.Forward)
			if err != nil || receipt.ForwardPlan != "" && receipt.ForwardPlan != child.SHA256() {
				return bad()
			}
			forwardEntry := createOpenBaoACKCompletionEntry(journal, receipt.Forward)
			var accepted struct {
				Plan   string            `json:"plan_sha256"`
				Owners map[string]string `json:"owners"`
			}
			if forwardEntry == nil || forwardEntry.State != "completed" || json.Unmarshal(forwardEntry.Receipt, &accepted) != nil || accepted.Plan != child.SHA256() || deploymentplan.Digest(accepted.Owners) != deploymentplan.Digest(childJournal.Owners) {
				return bad()
			}
			sourceJournal, exists, err := source.ReadExecution(store.Root)
			leased := false
			for _, lease := range journal.KubernetesLeases {
				leased = leased || managedLateLeaseMatches(lease, journal.Stack, source.SHA256())
			}
			if err != nil || exists != leased || sourceJournal.InFlight != "" || receipt.OriginalJournal != "" && (!exists || receipt.OriginalJournal != deploymentplan.Digest(sourceJournal)) {
				return bad()
			}
			if exists {
				stages = append(stages, completedRecordedSuccessorStage{Plan: source, Journal: sourceJournal, Written: true})
			}
			continue
		}
		if !environmentPreflightDigest.MatchString(receipt.Plan) || len(receipt.Owners) == 0 {
			return bad()
		}
		intersects := false
		for id := range receipt.Owners {
			if tracked[id] {
				intersects = true
				break
			}
		}
		if !intersects {
			continue
		}
		plan, j, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil || plan == nil || receipt.Plan != plan.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(j.Owners) || entry.Phase != strings.TrimPrefix(entry.Key, "kubernetes/") {
			return bad()
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		if err != nil {
			return bad()
		}
		sha, err := environmentCreatePlanSHA256(intent)
		if err != nil || sha != entry.IntentSHA256 {
			return bad()
		}
		retired := map[string]string{}
		for _, resource := range plan.Resources {
			if resource.Action != "retire" || !tracked[resource.ID] || !slices.Contains(j.Completed, resource.ID) {
				continue
			}
			original := slices.IndexFunc(base, func(r activeTransportRenewalResource) bool { return r.ID == resource.ID })
			if original < 0 || resource.UID != base[original].UID || !environmentBootstrapOwnerUUID.MatchString(resource.UID) || entry.StartedAt.Before(journal.Entries[start].CompletedAt) {
				return bad()
			}
			retired[resource.ID] = resource.UID
			if len(retirements) == 1 {
				retirements[0][resource.ID] = resource.UID
			}
		}
		stages = append(stages, completedRecordedSuccessorStage{Plan: plan, Journal: j, Retired: retired})
	}
	out, err := foldCompletedRecordedSuccessors(base, stages)
	if err != nil {
		return nil, err
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return out, nil
}
