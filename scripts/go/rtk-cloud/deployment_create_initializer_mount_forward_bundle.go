package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const initializerMountForwardSchema = "rtk-cloud.create-initializer-mount-forward/v1"

// The second successor is scoped to the same original q0/q1 execution; these
// immutable bindings preserve both failed native journals and their owners.
type createInitializerMountForwardProof struct {
	Schema                         string                   `json:"schema"`
	Environment                    string                   `json:"environment"`
	ParentOwnerSHA256              string                   `json:"parent_owner_sha256"`
	ParentPlanSHA256               string                   `json:"parent_plan_sha256"`
	SourceJournal                  environmentCreateJournal `json:"source_journal"`
	SourceJournalSHA256            string                   `json:"source_journal_sha256"`
	InitializerQualificationSHA256 string                   `json:"initializer_qualification_sha256"`
	OriginalPlanSHA256             string                   `json:"original_plan_sha256"`
	OriginalJournalSHA256          string                   `json:"original_journal_sha256"`
	PreviousUpdateStage            string                   `json:"previous_update_stage"`
	PreviousUpdatePlanSHA256       string                   `json:"previous_update_plan_sha256"`
	PreviousUpdateJournalSHA256    string                   `json:"previous_update_journal_sha256"`
	UpdateStage                    string                   `json:"update_stage"`
	UpdatePlanSHA256               string                   `json:"update_plan_sha256"`
	RetainStage                    string                   `json:"retain_stage"`
	RetainPlanSHA256               string                   `json:"retain_plan_sha256"`
	QualifiedAt                    time.Time                `json:"qualified_at"`
	ExecutionDeadline              time.Time                `json:"execution_deadline"`
}

func validInitializerMountForwardBundle(store secretStore, e *environmentCreateExecution, q createInitializerMountForwardProof, update, retain *deploymentplan.Plan) bool {
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || q.Schema != initializerMountForwardSchema || q.Environment != store.Environment || q.ParentOwnerSHA256 != deploymentplan.Digest(e.owner) || q.ParentPlanSHA256 != e.Journal.PlanSHA256 || q.SourceJournal.PlanSHA256 != q.ParentPlanSHA256 || q.SourceJournalSHA256 != deploymentplan.Digest(q.SourceJournal) || !partialSessionJournalExtends(q.SourceJournal, e.Journal) || update == nil || retain == nil || update.Environment != store.Environment || retain.Environment != store.Environment || update.Stack != e.Journal.Stack || retain.Stack != e.Journal.Stack || update.Operation != "scoped" || retain.Operation != "scoped" || q.UpdatePlanSHA256 != update.SHA256() || q.RetainPlanSHA256 != retain.SHA256() || q.QualifiedAt.IsZero() || !q.QualifiedAt.Before(q.ExecutionDeadline) {
		return false
	}
	for _, sha := range []string{q.ParentOwnerSHA256, q.ParentPlanSHA256, q.SourceJournalSHA256, q.InitializerQualificationSHA256, q.OriginalPlanSHA256, q.OriginalJournalSHA256, q.PreviousUpdatePlanSHA256, q.PreviousUpdateJournalSHA256, q.UpdatePlanSHA256, q.RetainPlanSHA256} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	if !secretEnvironmentPattern.MatchString(q.PreviousUpdateStage) || !secretEnvironmentPattern.MatchString(q.UpdateStage) || !secretEnvironmentPattern.MatchString(q.RetainStage) || q.UpdateStage == q.RetainStage || q.UpdateStage == q.PreviousUpdateStage || q.RetainStage == q.PreviousUpdateStage {
		return false
	}
	return true
}

func initializerMountForwardDir(store secretStore, e *environmentCreateExecution, review string, create bool) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("original initializer mount forward bundle is unavailable")
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
	for _, part := range []string{"deployment", "create", e.Journal.PlanSHA256, "initializer-mount-forward", review} {
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

func saveInitializerMountForwardBundle(store secretStore, e *environmentCreateExecution, q createInitializerMountForwardProof, update, retain *deploymentplan.Plan) error {
	if !validInitializerMountForwardBundle(store, e, q, update, retain) {
		return createProviderRecoveryBlock("initializer mount forward bundle does not match its original execution")
	}
	dir, err := initializerMountForwardDir(store, e, deploymentplan.Digest(q), true)
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
		if err = writeFailedForwardBundleImmutable(filepath.Join(dir, item.name), item.value); err != nil {
			return err
		}
	}
	return e.Check(context.Background())
}

func loadInitializerMountForwardBundle(store secretStore, e *environmentCreateExecution, review string) (createInitializerMountForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
	var q createInitializerMountForwardProof
	var update, retain deploymentplan.Plan
	bad := func() (createInitializerMountForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
		return createInitializerMountForwardProof{}, nil, nil, createProviderRecoveryBlock("initializer mount forward evidence is unavailable or changed")
	}
	dir, err := initializerMountForwardDir(store, e, review, false)
	if err != nil {
		return bad()
	}
	if readEnvironmentPreflightJSON(filepath.Join(dir, "qualification.json"), &q) != nil || readEnvironmentPreflightJSON(filepath.Join(dir, "update-plan.json"), &update) != nil || readEnvironmentPreflightJSON(filepath.Join(dir, "retain-plan.json"), &retain) != nil || deploymentplan.Digest(q) != review || !validInitializerMountForwardBundle(store, e, q, &update, &retain) {
		return bad()
	}
	return q, &update, &retain, nil
}
