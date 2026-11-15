package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const staleIdentityPodProofSchema = "rtk-cloud.create-stale-identity-pod/v1"

type staleIdentityLeaseEvidence struct {
	ID              string    `json:"id"`
	UID             string    `json:"uid"`
	Holder          string    `json:"holder"`
	RenewedAt       time.Time `json:"renewed_at"`
	DurationSeconds int64     `json:"duration_seconds"`
}
type staleIdentityPodProof struct {
	Schema            string                       `json:"schema"`
	Controller        bool                         `json:"controller,omitempty"`
	Environment       string                       `json:"environment"`
	ParentSHA         string                       `json:"parent_sha256"`
	ParentOwnerSHA    string                       `json:"parent_owner_sha256"`
	SourceJournal     environmentCreateJournal     `json:"source_journal"`
	SourceJournalSHA  string                       `json:"source_journal_sha256"`
	MountReview       string                       `json:"mount_review_sha256"`
	RetainStage       string                       `json:"retain_stage"`
	RetainPlanSHA     string                       `json:"retain_plan_sha256"`
	RetainJournalSHA  string                       `json:"retain_journal_sha256"`
	DeploymentID      string                       `json:"deployment_id"`
	DeploymentUID     string                       `json:"deployment_uid"`
	ReplicaSetID      string                       `json:"replicaset_id"`
	ReplicaSetUID     string                       `json:"replicaset_uid"`
	PodID             string                       `json:"pod_id"`
	PodUID            string                       `json:"pod_uid"`
	Leases            []staleIdentityLeaseEvidence `json:"leases"`
	Candidate         *deploymentplan.Plan         `json:"candidate"`
	QualifiedAt       time.Time                    `json:"qualified_at"`
	ExecutionDeadline time.Time                    `json:"execution_deadline"`
}

func validStaleIdentityPodProof(store secretStore, e *environmentCreateExecution, q staleIdentityPodProof) bool {
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || q.Schema != staleIdentityPodProofSchema || q.Environment != store.Environment || q.ParentSHA != e.Journal.PlanSHA256 || q.ParentOwnerSHA != deploymentplan.Digest(e.owner) || q.SourceJournal.PlanSHA256 != q.ParentSHA || q.SourceJournalSHA != deploymentplan.Digest(q.SourceJournal) || !partialSessionJournalExtends(q.SourceJournal, e.Journal) || q.QualifiedAt.IsZero() || !q.QualifiedAt.Before(q.ExecutionDeadline) || q.Candidate == nil || q.Candidate.Environment != store.Environment || q.Candidate.Stack != e.Journal.Stack || q.Candidate.Operation != "scoped" || len(q.Candidate.Resources) != 1 {
		return false
	}
	for _, s := range []string{q.ParentSHA, q.ParentOwnerSHA, q.SourceJournalSHA, q.RetainPlanSHA, q.RetainJournalSHA} {
		if !environmentPreflightDigest.MatchString(s) {
			return false
		}
	}
	if (!q.Controller && !environmentPreflightDigest.MatchString(q.MountReview)) || (q.Controller && (q.MountReview != "" || q.DeploymentID != "Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller")) {
		return false
	}
	if !secretEnvironmentPattern.MatchString(q.RetainStage) || !environmentBootstrapOwnerUUID.MatchString(q.DeploymentUID) || !environmentBootstrapOwnerUUID.MatchString(q.ReplicaSetUID) || !environmentBootstrapOwnerUUID.MatchString(q.PodUID) || len(q.Leases) == 0 {
		return false
	}
	r := q.Candidate.Resources[0]
	if r.ID != q.PodID || r.Action != "retire" || r.UID != q.PodUID || r.Desired["kind"] != "Pod" {
		return false
	}
	seen := map[string]bool{}
	for _, l := range q.Leases {
		if l.ID == "" || seen[l.ID] || !environmentBootstrapOwnerUUID.MatchString(l.UID) || l.Holder == "" || l.RenewedAt.IsZero() || l.DurationSeconds <= 0 || l.RenewedAt.Add(time.Duration(l.DurationSeconds)*time.Second).After(q.QualifiedAt) {
			return false
		}
		seen[l.ID] = true
	}
	return true
}
func staleIdentityPodProofDir(store secretStore, e *environmentCreateExecution, review string, create bool) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("original stale identity Pod repair evidence is unavailable")
	}
	if e == nil || e.Store != store || !environmentPreflightDigest.MatchString(e.Journal.PlanSHA256) || !environmentPreflightDigest.MatchString(review) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) {
		return bad()
	}
	for _, p := range []string{store.ConfigRoot, store.Root} {
		if validateEnvironmentPreflightDirectory(p) != nil {
			return bad()
		}
	}
	dir := store.Root
	for _, part := range []string{"deployment", "create", e.Journal.PlanSHA256, "stale-identity-pod", review} {
		dir = filepath.Join(dir, part)
		if create {
			if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return bad()
			}
		}
		if validateEnvironmentPreflightDirectory(dir) != nil {
			return bad()
		}
	}
	return dir, nil
}
func saveStaleIdentityPodProof(store secretStore, e *environmentCreateExecution, q staleIdentityPodProof) error {
	if !validStaleIdentityPodProof(store, e, q) {
		return createProviderRecoveryBlock("stale identity Pod proof differs from its original owner")
	}
	dir, err := staleIdentityPodProofDir(store, e, deploymentplan.Digest(q), true)
	if err != nil {
		return err
	}
	if err = writeFailedForwardBundleImmutable(filepath.Join(dir, "qualification.json"), q); err != nil {
		return err
	}
	return e.Check(context.Background())
}
func loadStaleIdentityPodProof(store secretStore, e *environmentCreateExecution, review string) (staleIdentityPodProof, error) {
	var q staleIdentityPodProof
	dir, err := staleIdentityPodProofDir(store, e, review, false)
	if err != nil {
		return q, err
	}
	if readEnvironmentPreflightJSON(filepath.Join(dir, "qualification.json"), &q) != nil || deploymentplan.Digest(q) != review || !validStaleIdentityPodProof(store, e, q) {
		return staleIdentityPodProof{}, createProviderRecoveryBlock("stale identity Pod proof is unavailable or changed")
	}
	return q, nil
}
