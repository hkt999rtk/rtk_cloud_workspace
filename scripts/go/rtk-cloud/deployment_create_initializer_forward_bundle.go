package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const initializerForwardProofSchema = "rtk-cloud.create-initializer-forward/v1"

// Content bindings only; the caller independently qualifies journal ancestry,
// original owners and the original admitted execution deadline.
type createInitializerForwardProof struct {
	// Runtime-only selected mount successor; never alters the q1 digest.
	mount                     *createInitializerMountForwardProof
	Schema                    string                   `json:"schema"`
	Environment               string                   `json:"environment"`
	ParentOwnerSHA256         string                   `json:"parent_owner_sha256"`
	ParentPlanSHA256          string                   `json:"parent_plan_sha256"`
	SourceJournal             environmentCreateJournal `json:"source_journal"`
	SourceJournalSHA256       string                   `json:"source_journal_sha256"`
	OriginalAdmissionSHA256   string                   `json:"original_admission_sha256"`
	FailedForwardSHA256       string                   `json:"failed_forward_sha256"`
	OriginalPlanSHA256        string                   `json:"original_plan_sha256"`
	OriginalJournalSHA256     string                   `json:"original_journal_sha256"`
	CRLStage                  string                   `json:"crl_stage"`
	CRLPlanSHA256             string                   `json:"crl_plan_sha256"`
	FailedRetainStage         string                   `json:"failed_retain_stage"`
	FailedRetainPlanSHA256    string                   `json:"failed_retain_plan_sha256"`
	FailedRetainJournalSHA256 string                   `json:"failed_retain_journal_sha256"`
	UpdateStage               string                   `json:"update_stage"`
	UpdatePlanSHA256          string                   `json:"update_plan_sha256"`
	RetainStage               string                   `json:"retain_stage"`
	RetainPlanSHA256          string                   `json:"retain_plan_sha256"`
	QualifiedAt               time.Time                `json:"qualified_at"`
	ExecutionDeadline         time.Time                `json:"execution_deadline"`
}

func validInitializerForwardBundle(store secretStore, e *environmentCreateExecution, q createInitializerForwardProof, update, retain *deploymentplan.Plan) bool {
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || q.Schema != initializerForwardProofSchema || q.Environment != store.Environment || q.ParentOwnerSHA256 != deploymentplan.Digest(e.owner) || q.ParentPlanSHA256 != e.Journal.PlanSHA256 || q.SourceJournal.PlanSHA256 != q.ParentPlanSHA256 || q.SourceJournalSHA256 != deploymentplan.Digest(q.SourceJournal) || !partialSessionJournalExtends(q.SourceJournal, e.Journal) || update == nil || retain == nil || update.Environment != store.Environment || retain.Environment != store.Environment || update.Stack != e.Journal.Stack || retain.Stack != e.Journal.Stack || update.Operation != "scoped" || retain.Operation != "scoped" || q.UpdatePlanSHA256 != update.SHA256() || q.RetainPlanSHA256 != retain.SHA256() || q.QualifiedAt.IsZero() || !q.QualifiedAt.Before(q.ExecutionDeadline) {
		return false
	}
	for _, sha := range []string{q.ParentOwnerSHA256, q.ParentPlanSHA256, q.SourceJournalSHA256, q.OriginalAdmissionSHA256, q.FailedForwardSHA256, q.OriginalPlanSHA256, q.OriginalJournalSHA256, q.CRLPlanSHA256, q.FailedRetainPlanSHA256, q.FailedRetainJournalSHA256, q.UpdatePlanSHA256, q.RetainPlanSHA256} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	seen := map[string]bool{}
	for _, name := range []string{q.CRLStage, q.FailedRetainStage, q.UpdateStage, q.RetainStage} {
		if !secretEnvironmentPattern.MatchString(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func initializerForwardBundleDir(store secretStore, e *environmentCreateExecution, review string, create bool) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("original initializer forward bundle is unavailable")
	}
	if e == nil || e.Store != store || !environmentPreflightDigest.MatchString(e.Journal.PlanSHA256) || !environmentPreflightDigest.MatchString(review) || store.Root != filepath.Join(store.ConfigRoot, store.Environment) {
		return bad()
	}
	for _, path := range []string{store.ConfigRoot, store.Root} {
		if validateEnvironmentPreflightDirectory(path) != nil {
			return bad()
		}
	}
	dir := store.Root
	for _, part := range []string{"deployment", "create", e.Journal.PlanSHA256, "initializer-forward", review} {
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

func saveInitializerForwardBundle(store secretStore, e *environmentCreateExecution, q createInitializerForwardProof, update, retain *deploymentplan.Plan) error {
	if !validInitializerForwardBundle(store, e, q, update, retain) {
		return createProviderRecoveryBlock("initializer forward bundle does not match original qualified execution")
	}
	dir, err := initializerForwardBundleDir(store, e, deploymentplan.Digest(q), true)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		name  string
		value any
	}{{"update-plan.json", update}, {"retain-plan.json", retain}, {"qualification.json", q}} {
		if e.Check(context.Background()) != nil {
			return createProviderRecoveryBlock("original execution fence is unavailable")
		}
		if err := writeFailedForwardBundleImmutable(filepath.Join(dir, item.name), item.value); err != nil {
			return err
		}
	}
	return e.Check(context.Background())
}

func loadInitializerForwardBundle(store secretStore, e *environmentCreateExecution, review string) (createInitializerForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
	var q createInitializerForwardProof
	var update, retain deploymentplan.Plan
	bad := func() (createInitializerForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
		return createInitializerForwardProof{}, nil, nil, createProviderRecoveryBlock("initializer forward evidence is unavailable or changed")
	}
	dir, err := initializerForwardBundleDir(store, e, review, false)
	if err != nil {
		return bad()
	}
	if readEnvironmentPreflightJSON(filepath.Join(dir, "qualification.json"), &q) != nil || readEnvironmentPreflightJSON(filepath.Join(dir, "update-plan.json"), &update) != nil || readEnvironmentPreflightJSON(filepath.Join(dir, "retain-plan.json"), &retain) != nil || deploymentplan.Digest(q) != review || !validInitializerForwardBundle(store, e, q, &update, &retain) {
		return bad()
	}
	return q, &update, &retain, nil
}
