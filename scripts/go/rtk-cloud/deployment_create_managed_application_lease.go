package main

import (
	"context"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedApplicationInterruptedLeaseMaterial = "accepted-service-core-managed-application-interrupted-lease"

type managedApplicationInterruptedLeaseIntent struct {
	Review       string                    `json:"qualified_review_sha256"`
	SourceReview string                    `json:"interrupted_review_sha256"`
	SourceSHA    string                    `json:"interrupted_source_sha256"`
	PlanSHA      string                    `json:"interrupted_plan_sha256"`
	Lease        deploymentplan.LeaseOwner `json:"original_lease"`
	LeaseSHA     string                    `json:"original_native_lease_sha256"`
}

type managedApplicationInterruptedLeaseReceipt struct {
	Lease    deploymentplan.LeaseOwner `json:"original_lease"`
	LeaseSHA string                    `json:"original_native_lease_sha256"`
	Absent   bool                      `json:"original_lease_absent"`
}

func managedApplicationInterruptedLeaseBlock() error {
	return createProviderRecoveryBlock("interrupted application lease requires its authenticated original source, exclusive parent, exact recorded owner and unchanged native version; elapsed time never authorizes release")
}

// Authenticate the bounded original checkpoint independently of the in-memory typed
// profile. The release caller supplies only the newly admitted immutable source.
func managedApplicationInterruptedLeaseSource(e *environmentCreateExecution, source environmentCreateJournal, r *createServiceSessionRecovery) (*createManagedApplicationInterruptedForward, deploymentplan.LeaseOwner, error) {
	var empty deploymentplan.LeaseOwner
	if e == nil || r == nil || r.ManagedApplicationPartial == nil || r.ManagedApplicationPartial.Interrupted == nil || e.Store.Environment != "dev" || source.Environment != "dev" || source.Stack != "video-cloud-dev" || len(source.KubernetesLeases) == 0 {
		return nil, empty, managedApplicationInterruptedLeaseBlock()
	}
	interrupted := r.ManagedApplicationPartial.Interrupted
	if validateManagedApplicationInterruptedForward(r.ManagedApplicationPartial, interrupted) != nil {
		return nil, empty, managedApplicationInterruptedLeaseBlock()
	}
	original := *e
	original.Journal = source
	before, _, loaded, err := loadManagedApplicationInterruptedSource(e.Store, &original)
	if err != nil || !(managedApplicationPartialNativeCheckpoint(e.Store, before) || managedApplicationInterruptedNativeCheckpoint(e.Store, before)) {
		return nil, empty, managedApplicationInterruptedLeaseBlock()
	}
	qualified := *interrupted
	qualified.LeaseSHA = loaded.LeaseSHA // Qualification changes only the current snapshot.
	if deploymentplan.Digest(qualified) != deploymentplan.Digest(loaded) {
		return nil, empty, managedApplicationInterruptedLeaseBlock()
	}
	owner := source.KubernetesLeases[len(source.KubernetesLeases)-1]
	if owner.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(owner.UID) || len(owner.Holder) <= len(interrupted.Plan.SHA256())+1 || len(owner.Holder) > 256 || owner.Holder[:len(interrupted.Plan.SHA256())+1] != interrupted.Plan.SHA256()+":" {
		return nil, empty, managedApplicationInterruptedLeaseBlock()
	}
	return interrupted, owner, nil
}

func validateManagedApplicationInterruptedLease(plan *deploymentplan.Plan, owner deploymentplan.LeaseOwner, actual deploymentplan.Object) error {
	if actual == nil {
		return nil
	}
	metadata := deploymentplan.Map(actual["metadata"])
	labels, annotations := deploymentplan.Map(metadata["labels"]), deploymentplan.Map(metadata["annotations"])
	if plan == nil || actual["apiVersion"] != "coordination.k8s.io/v1" || deploymentplan.ID(actual) != owner.ResourceID || metadata["uid"] != owner.UID || metadata["deletionTimestamp"] != nil || deploymentplan.String(metadata["resourceVersion"]) == "" || labels["rtk.realtek.com/stack"] != plan.Stack || labels["app.kubernetes.io/managed-by"] != "rtk-cloud" || annotations["rtk.realtek.com/plan-sha256"] != plan.SHA256() || annotations["rtk.realtek.com/environment"] != plan.Environment || deploymentplan.Map(actual["spec"])["holderIdentity"] != owner.Holder {
		return managedApplicationInterruptedLeaseBlock()
	}
	return nil
}

func qualifyManagedApplicationInterruptedLease(ctx context.Context, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r != nil && r.ManagedApplicationPartial != nil && r.ManagedApplicationPartial.Interrupted == nil {
		return nil
	}
	if e == nil || p == nil || p.Materials == nil || b.Store.Root != e.Store.Root || e.Check(ctx) != nil {
		return managedApplicationInterruptedLeaseBlock()
	}
	interrupted, owner, err := managedApplicationInterruptedLeaseSource(e, e.Journal, r)
	if err != nil {
		return err
	}
	actual, err := b.Read(ctx, owner.ResourceID)
	if err != nil || validateManagedApplicationInterruptedLease(interrupted.Plan, owner, actual) != nil {
		return managedApplicationInterruptedLeaseBlock()
	}
	interrupted.LeaseSHA = deploymentplan.Digest(actual)
	p.Materials[managedApplicationInterruptedLeaseMaterial] = interrupted.LeaseSHA
	return e.Check(ctx)
}

func verifyManagedApplicationInterruptedLease(ctx context.Context, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) error {
	if r != nil && r.ManagedApplicationPartial != nil && r.ManagedApplicationPartial.Interrupted == nil {
		return nil
	}
	if e == nil || b.Store.Root != e.Store.Root || e.Check(ctx) != nil {
		return managedApplicationInterruptedLeaseBlock()
	}
	interrupted, owner, err := managedApplicationInterruptedLeaseSource(e, e.Journal, r)
	if err != nil || !environmentPreflightDigest.MatchString(interrupted.LeaseSHA) {
		return managedApplicationInterruptedLeaseBlock()
	}
	actual, err := b.Read(ctx, owner.ResourceID)
	if err != nil || validateManagedApplicationInterruptedLease(interrupted.Plan, owner, actual) != nil || deploymentplan.Digest(actual) != interrupted.LeaseSHA {
		return managedApplicationInterruptedLeaseBlock()
	}
	return e.Check(ctx)
}

// The existing release implementation re-reads the owner and supplies native
// UID+resourceVersion delete preconditions. Bind its final read to qualification
// too, so a same-owner version change cannot become a newly authorized snapshot.
type managedApplicationInterruptedLeaseAPI struct {
	environmentExecutionAPI
	sha string
}

func (a managedApplicationInterruptedLeaseAPI) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	actual, err := a.environmentExecutionAPI.Read(ctx, id)
	if err != nil || actual != nil && deploymentplan.Digest(actual) != a.sha {
		return nil, managedApplicationInterruptedLeaseBlock()
	}
	return actual, nil
}

func releaseManagedApplicationInterruptedLease(ctx context.Context, runtime environmentCreateBootstrapRuntime) error {
	return releaseManagedApplicationInterruptedLeaseWithBackend(ctx, runtime, runtime.Backend)
}

func releaseManagedApplicationInterruptedLeaseWithBackend(ctx context.Context, runtime environmentCreateBootstrapRuntime, api environmentExecutionAPI) error {
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if r != nil && r.ManagedApplicationPartial != nil && r.ManagedApplicationPartial.Interrupted == nil {
		return nil
	}
	if e == nil || api == nil || runtime.Backend.Store.Root != e.Store.Root || e.Check(ctx) != nil || r == nil || !environmentPreflightDigest.MatchString(r.Review) {
		return managedApplicationInterruptedLeaseBlock()
	}
	artifact, source, err := loadAdmittedCreateRecovery(e.Store, e, r.Review)
	schema := "rtk-cloud.create-managed-application-interrupted-recovery/v1"
	if r.ManagedApplicationPartial != nil && r.ManagedApplicationPartial.Interrupted != nil && r.ManagedApplicationPartial.Interrupted.Previous != nil {
		schema = "rtk-cloud.create-managed-application-app-role-recovery/v1"
		if r.ManagedApplicationPartial.Interrupted.Previous.Previous != nil {
			schema = "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1"
		}
	}
	if err != nil || artifact.Plan.Schema != schema {
		return managedApplicationInterruptedLeaseBlock()
	}
	interrupted, owner, err := managedApplicationInterruptedLeaseSource(e, source, r)
	if err != nil || !environmentPreflightDigest.MatchString(interrupted.LeaseSHA) || artifact.Plan.Materials[managedApplicationInterruptedLeaseMaterial] != interrupted.LeaseSHA {
		return managedApplicationInterruptedLeaseBlock()
	}
	key := "managed-application-interrupted-lease-release-" + r.Review
	n := len(source.Entries)
	if len(e.Journal.Entries) < n+1 || len(e.Journal.Entries) > n+2 || e.Journal.Entries[n].Key != "openbao-recovery-admission-"+r.Review || e.Journal.Entries[n].State != "completed" {
		return managedApplicationInterruptedLeaseBlock()
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:n]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) {
		return managedApplicationInterruptedLeaseBlock()
	}
	intent := managedApplicationInterruptedLeaseIntent{Review: r.Review, SourceReview: interrupted.Review, SourceSHA: interrupted.SourceSHA, PlanSHA: interrupted.Plan.SHA256(), Lease: owner, LeaseSHA: interrupted.LeaseSHA}
	intentRaw := mustProviderRecoveryJSON(intent)
	intentSHA, err := environmentCreatePlanSHA256(intentRaw)
	if err != nil {
		return err
	}
	receipt := managedApplicationInterruptedLeaseReceipt{Lease: owner, LeaseSHA: interrupted.LeaseSHA, Absent: true}
	receiptRaw := mustProviderRecoveryJSON(receipt)
	var entry *environmentCreateJournalEntry
	if len(e.Journal.Entries) == n+2 {
		existing := e.Journal.Entries[n+1]
		if existing.Key != key || existing.Phase != "managed-application-interrupted-lease-release" || existing.IntentSHA256 != intentSHA || (existing.State != "pending" && existing.State != "allocated" && existing.State != "completed") {
			return managedApplicationInterruptedLeaseBlock()
		}
		if existing.State == "pending" && (len(existing.Receipt) != 0 || !existing.CompletedAt.IsZero()) {
			return managedApplicationInterruptedLeaseBlock()
		}
		if existing.State != "pending" {
			var saved managedApplicationInterruptedLeaseReceipt
			if decodeEnvironmentJSON(existing.Receipt, &saved) != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(receipt) || existing.State == "allocated" && !existing.CompletedAt.IsZero() || existing.State == "completed" && existing.CompletedAt.IsZero() {
				return managedApplicationInterruptedLeaseBlock()
			}
		}
		entry = &existing
	}
	actual, err := api.Read(ctx, owner.ResourceID)
	if err != nil {
		return managedApplicationInterruptedLeaseBlock()
	}
	if interrupted.LeaseSHA == deploymentplan.Digest(deploymentplan.Object(nil)) {
		if actual != nil || entry != nil {
			return managedApplicationInterruptedLeaseBlock()
		}
		return e.Check(ctx) // Qualified absence is observed without a release attempt.
	}
	if actual != nil {
		if entry != nil && entry.State != "pending" || validateManagedApplicationInterruptedLease(interrupted.Plan, owner, actual) != nil || deploymentplan.Digest(actual) != interrupted.LeaseSHA {
			return managedApplicationInterruptedLeaseBlock()
		}
		if entry == nil {
			if err := e.BeginEntry(key, "managed-application-interrupted-lease-release", intentRaw, time.Now().UTC()); err != nil {
				return err
			}
		}
		if e.Check(ctx) != nil {
			return managedApplicationInterruptedLeaseBlock()
		}
		lease := &environmentExecutionLease{backend: managedApplicationInterruptedLeaseAPI{environmentExecutionAPI: api, sha: interrupted.LeaseSHA}, id: owner.ResourceID, uid: owner.UID, holder: owner.Holder}
		if err := lease.Release(ctx); err != nil {
			return err
		}
	} else if entry == nil {
		return managedApplicationInterruptedLeaseBlock() // Absence alone cannot invent a past attempt.
	}
	if e.Check(ctx) != nil {
		return managedApplicationInterruptedLeaseBlock()
	}
	absent, err := api.Read(ctx, owner.ResourceID)
	if err != nil || absent != nil {
		return managedApplicationInterruptedLeaseBlock()
	}
	if entry != nil && entry.State == "completed" {
		return nil
	}
	if entry == nil || entry.State == "pending" {
		if err := e.RecordReceipt(key, receiptRaw); err != nil {
			return err
		}
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
