package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const secondSessionAdmissionSchema = "rtk-cloud.create-second-session/v1"

type secondSessionAdmission struct {
	Schema              string                     `json:"schema"`
	Environment         string                     `json:"environment"`
	ParentOwnerSHA256   string                     `json:"parent_owner_sha256"`
	SourceJournal       environmentCreateJournal   `json:"source_journal"`
	SourceJournalSHA256 string                     `json:"source_journal_sha256"`
	QualifiedAt         time.Time                  `json:"qualified_at"`
	ExecutionDeadline   time.Time                  `json:"execution_deadline"`
	Renewal             secondSessionQualification `json:"renewal"`
	Batches             []serviceSessionBatch      `json:"batches,omitempty"`
	Reference           *serviceSessionReference   `json:"reference,omitempty"`
	RequiredACK         []string                   `json:"required_ack,omitempty"`
}

// Only immutable evidence is persisted. Native state, Job owners and original
// custody must be freshly re-observed by the caller before any write.
func validSecondSessionAdmission(store secretStore, e *environmentCreateExecution, q secondSessionAdmission) bool {
	return validSecondSessionAdmissionWithLoader(store, e, q, newSecondSessionAdmissionReader(store, e))
}

func validSecondSessionAdmissionWithLoader(store secretStore, e *environmentCreateExecution, q secondSessionAdmission, load func(string) (secondSessionAdmission, error)) bool {
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || q.Schema != secondSessionAdmissionSchema || q.Environment != store.Environment || q.ParentOwnerSHA256 != deploymentplan.Digest(e.owner) || q.SourceJournal.PlanSHA256 != e.Journal.PlanSHA256 || q.SourceJournalSHA256 != deploymentplan.Digest(q.SourceJournal) || !partialSessionJournalExtends(q.SourceJournal, e.Journal) || q.QualifiedAt.IsZero() || !q.QualifiedAt.Before(q.ExecutionDeadline) {
		return false
	}
	r := q.Renewal
	if valid, _ := regexp.MatchString(`^[0-9]+:[0-9]+$`, r.CustodyOwner); !valid {
		return false
	}
	if r.SourceJobID == "" || r.PreviousJobID == "" || r.PreviousCustodyID == "" || !environmentBootstrapOwnerUUID.MatchString(r.PreviousCustodyUID) || r.SourceStage == "" || r.PreviousStage == "" || !environmentBootstrapOwnerUUID.MatchString(r.SourceJobUID) || !environmentBootstrapOwnerUUID.MatchString(r.PreviousJobUID) {
		return false
	}
	if r.PreviousState.Session != r.Intent.SessionID || r.PreviousState.Issuer != r.Intent.IssuerID || r.PreviousState.Root != r.Intent.RootSHA || r.PreviousState.Expires.IsZero() || r.PreviousState.Expires.After(q.QualifiedAt) {
		return false
	}
	if r.ParentSHA != e.Journal.PlanSHA256 || r.Candidate == nil || r.Candidate.Environment != store.Environment || r.Candidate.Stack != e.Journal.Stack || r.Candidate.Operation != "scoped" || len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" || r.Candidate.Resources[0].Desired["kind"] != "Job" || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) || !environmentBootstrapOwnerUUID.MatchString(r.RenewedIntent.SessionID) || r.Intent.SessionID == r.RenewedIntent.SessionID {
		return false
	}
	for _, sha := range []string{q.ParentOwnerSHA256, q.SourceJournalSHA256, r.ParentSHA, r.OriginalReview, r.PreviousReview, r.PreviousBindingSHA, r.SourcePlanSHA, r.SourceJournalSHA, r.PreviousPlanSHA, r.PreviousJournalSHA, r.PreviousCredentialPlanSHA, r.PreviousCredentialJournalSHA, r.PreviousCustodyPlanSHA, r.PreviousCustodyJournalSHA, r.ClientsSHA, r.KeySHA, r.CSRSHA, r.PreviousCertSHA, r.OpenBaoTrustSHA, r.BindingSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	if len(r.Intent.Namespaces) == 0 || len(r.PreviousCredentialUIDs) != len(r.Intent.Namespaces) {
		return false
	}
	for _, ns := range r.Intent.Namespaces {
		if !secretEnvironmentPattern.MatchString(ns) || !environmentBootstrapOwnerUUID.MatchString(r.PreviousCredentialUIDs[ns]) {
			return false
		}
	}
	if secondSessionBinding(&r) != r.BindingSHA || r.RenewedIntent.SessionID != sessionRenewalUUID(r.ParentSHA, r.BindingSHA, r.Intent.SessionID) {
		return false
	}
	if (q.Batches == nil) != (q.Reference == nil) || q.Batches != nil && (!validServiceSessionBatches(q) || !validServiceSessionReference(q)) {
		return false
	}
	if len(q.Batches) > 2 {
		ids, history, err := serviceSessionPriorLineageWithLoader(store, e, q, load)
		if err != nil || !sameSessionList(ids, serviceSessionBatchPrevious(q)) {
			return false
		}
		if !sameSessionList(q.RequiredACK, serviceSessionRequiredACKs(q, history)) {
			return false
		}
	}
	same := r.Intent
	same.SessionID = r.RenewedIntent.SessionID
	return deploymentplan.Digest(same) == deploymentplan.Digest(r.RenewedIntent)
}

func secondSessionAdmissionDir(store secretStore, e *environmentCreateExecution, review string, create bool) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("original second-session admission is unavailable")
	}
	if e == nil || e.Store != store || !environmentPreflightDigest.MatchString(e.Journal.PlanSHA256) || !environmentPreflightDigest.MatchString(review) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) {
		return bad()
	}
	for _, d := range []string{store.ConfigRoot, store.Root} {
		if validateEnvironmentPreflightDirectory(d) != nil {
			return bad()
		}
	}
	dir := store.Root
	for _, part := range []string{"deployment", "create", e.Journal.PlanSHA256, "second-session", review} {
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
func saveSecondSessionAdmission(store secretStore, e *environmentCreateExecution, q secondSessionAdmission) error {
	if !validSecondSessionAdmission(store, e, q) {
		return createProviderRecoveryBlock("second-session admission differs from the original execution")
	}
	dir, err := secondSessionAdmissionDir(store, e, deploymentplan.Digest(q), true)
	if err != nil {
		return err
	}
	if err = writeFailedForwardBundleImmutable(filepath.Join(dir, "qualification.json"), q); err != nil {
		return err
	}
	return e.Check(context.Background())
}
func loadSecondSessionAdmission(store secretStore, e *environmentCreateExecution, review string) (secondSessionAdmission, error) {
	return newSecondSessionAdmissionReader(store, e)(review)
}

func newSecondSessionAdmissionReader(store secretStore, e *environmentCreateExecution) func(string) (secondSessionAdmission, error) {
	return secondSessionAdmissionReader(store, e, func(review string) (secondSessionAdmission, error) {
		return readSecondSessionAdmissionRecord(store, e, review)
	})
}

func secondSessionAdmissionReader(store secretStore, e *environmentCreateExecution, read func(string) (secondSessionAdmission, error)) func(string) (secondSessionAdmission, error) {
	// One immutable observation of this admission graph. No state survives the
	// caller, a write or a later observation; live owner checks are never cached.
	verified := map[string][]byte{}
	visiting := map[string]bool{}
	epoch := ""
	if e != nil {
		epoch = deploymentplan.Digest(e.Journal)
	}
	var load func(string) (secondSessionAdmission, error)
	load = func(review string) (secondSessionAdmission, error) {
		if e == nil || e.Store != store || deploymentplan.Digest(e.Journal) != epoch {
			return secondSessionAdmission{}, createProviderRecoveryBlock("original second-session observation advanced; start a new observation")
		}
		if raw, ok := verified[review]; ok {
			var q secondSessionAdmission
			if e == nil || e.Check(context.Background()) != nil || json.Unmarshal(raw, &q) != nil {
				return q, createProviderRecoveryBlock("original second-session observation owner changed")
			}
			return q, nil // independent objects prevent caller mutation of authority
		}
		if visiting[review] {
			return secondSessionAdmission{}, createProviderRecoveryBlock("original second-session admission contains a cycle")
		}
		visiting[review] = true
		defer delete(visiting, review)
		q, err := read(review)
		if err != nil || deploymentplan.Digest(q) != review || !validSecondSessionAdmissionWithLoader(store, e, q, load) {
			return secondSessionAdmission{}, createProviderRecoveryBlock("original second-session admission is unavailable or changed")
		}
		verified[review] = mustProviderRecoveryJSON(q)
		return q, nil
	}
	return load
}

func readSecondSessionAdmissionRecord(store secretStore, e *environmentCreateExecution, review string) (secondSessionAdmission, error) {
	var q secondSessionAdmission
	dir, err := secondSessionAdmissionDir(store, e, review, false)
	if err != nil {
		return q, err
	}
	if readEnvironmentPreflightJSON(filepath.Join(dir, "qualification.json"), &q) != nil {
		return secondSessionAdmission{}, createProviderRecoveryBlock("original second-session admission is unavailable or changed")
	}
	return q, nil
}
