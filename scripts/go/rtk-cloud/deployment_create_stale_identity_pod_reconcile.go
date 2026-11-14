package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Reconcile a previously admitted restart by observation only. The expired
// deletion deadline remains historical; this does not authorize another delete.
func reconcileObservedStaleIdentityPod(ctx context.Context, store secretStore, e *environmentCreateExecution, native staleIdentityPodInventory, q staleIdentityPodProof) error {
	bad := func() error {
		return createProviderRecoveryBlock("observed identity Pod reconciliation lacks original intent and accepted replacement evidence")
	}
	if !validStaleIdentityPodProof(store, e, q) || e.Check(ctx) != nil {
		return bad()
	}
	key := "stale-identity-pod-" + deploymentplan.Digest(q)
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	intentSHA, err := environmentCreatePlanSHA256(staleIdentityPodIntent(q))
	if err != nil || entry == nil || entry.Phase != "stale-identity-pod" || entry.IntentSHA256 != intentSHA || entry.StartedAt.Before(q.QualifiedAt) || !entry.StartedAt.Before(q.ExecutionDeadline) {
		return bad()
	}
	if entry.State == "completed" {
		var receipt struct {
			Schema        string    `json:"schema"`
			Qualification string    `json:"qualification_sha256"`
			Deployment    string    `json:"deployment_id"`
			DeploymentUID string    `json:"deployment_uid"`
			OriginalPod   string    `json:"original_pod_uid"`
			Replacement   string    `json:"replacement_pod_uid"`
			LeaseSHA      string    `json:"lease_ids_sha256"`
			Deadline      time.Time `json:"original_deadline"`
			Observed      time.Time `json:"observed_at"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Qualification != deploymentplan.Digest(q) || receipt.DeploymentUID != q.DeploymentUID || receipt.OriginalPod != q.PodUID || !environmentBootstrapOwnerUUID.MatchString(receipt.Replacement) || receipt.Replacement == q.PodUID {
			return bad()
		}
		if receipt.Schema == "rtk-cloud.stale-pod-observed-reconciliation/v1" {
			if !receipt.Deadline.Equal(q.ExecutionDeadline) || receipt.Observed.IsZero() || receipt.Observed.After(entry.CompletedAt) {
				return bad()
			}
		} else if receipt.Schema != "" || receipt.Deployment != q.DeploymentID || receipt.LeaseSHA != deploymentplan.Digest(q.Leases) {
			return bad()
		}
		return e.Check(ctx)
	}
	source, err := readEnvironmentCreateStageInput(store, e, q.RetainStage)
	if err != nil || source == nil || source.SHA256() != q.RetainPlanSHA {
		return bad()
	}
	j, exists, err := readEnvironmentCreateStageProgress(store, e, q.RetainStage, source)
	if err != nil || !exists || deploymentplan.Digest(j) != q.RetainJournalSHA || len(j.Completed) != len(source.Resources) {
		return bad()
	}
	var base []activeTransportRenewalResource
	for _, r := range source.Resources {
		if j.Owners[r.ID] == "" || r.UID != j.Owners[r.ID] {
			return bad()
		}
		base = append(base, sessionRecordedOwnerBase(r.ID, r.UID, r.Desired))
	}
	latest, err := completedSessionRecordedSuccessors(ctx, store, e, base)
	if err != nil {
		return err
	}
	if !j.Accepted {
		head, review, err := sessionLineageHead(store, e)
		if err != nil || head == nil {
			return bad()
		}
		done, err := completedServiceSessionBatch(store, e, *head, review)
		if err != nil || !done {
			return bad()
		}
	}
	var target deploymentplan.Resource
	for i, r := range source.Resources {
		if r.ID == q.DeploymentID {
			target = r
			target.Desired = latest[i].Desired
		}
	}
	if target.ID != q.DeploymentID || target.UID != q.DeploymentUID {
		return bad()
	}
	objects, err := native.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	for _, o := range objects {
		if deploymentplan.Map(o["metadata"])["uid"] == q.PodUID {
			return bad()
		}
	}
	now := time.Now().UTC()
	replacement, err := checkStaleIdentityReplacement(q, target, objects, now)
	if err != nil {
		return err
	}
	receipt := deploymentplan.Object{"schema": "rtk-cloud.stale-pod-observed-reconciliation/v1", "qualification_sha256": deploymentplan.Digest(q), "original_deadline": q.ExecutionDeadline, "original_pod_uid": q.PodUID, "replacement_pod_uid": replacement, "deployment_uid": q.DeploymentUID, "recorded_current_body_sha256": deploymentplan.Digest(target.Desired), "observed_at": now, "outcome": "original UID absent; accepted same-owner replacement ready; no restart performed"}
	raw := mustProviderRecoveryJSON(receipt)
	if entry.State == "allocated" {
		var previous deploymentplan.Object
		if json.Unmarshal(entry.Receipt, &previous) != nil {
			return bad()
		}
		receipt["observed_at"] = previous["observed_at"]
		raw = mustProviderRecoveryJSON(receipt)
		if !samePersistedJSONReceipt(entry.Receipt, raw) {
			return bad()
		}
	} else if entry.State == "pending" {
		if e.Check(ctx) != nil {
			return bad()
		}
		if err := e.RecordReceipt(key, raw); err != nil {
			return err
		}
	} else {
		return bad()
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
