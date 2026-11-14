package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// completedApplicationOwnerWrites is evidence of actual completed writes, not
// evidence that the later application stage has passed readiness or acceptance.
// The interrupted child remains untouched. A completed forward alias must be
// tied to its exact original qualification and accepted native repair children.
func completedApplicationOwnerWrites(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend) ([]activeTransportRenewalResource, error) {
	return completedApplicationOwnerWritesWithAdmission(ctx, store, e, b, authenticatedInitializerForwardAdmission)
}

func completedApplicationOwnerWritesWithAdmission(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, load initializerForwardAdmissionLoader) ([]activeTransportRenewalResource, error) {
	bad := func() ([]activeTransportRenewalResource, error) {
		return nil, createProviderRecoveryBlock("original application write history or forward alias differs")
	}
	if e == nil || e.Store != store || b == nil || load == nil || e.Check(ctx) != nil {
		return bad()
	}
	const stage = "managed-application-owners"
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil {
		return nil, nil
	}
	plan, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || plan == nil {
		return bad()
	}
	journal, exists, err := plan.ReadExecution(store.Root)
	if err != nil {
		return nil, err
	}
	if !exists {
		if entry.State != "pending" {
			return bad()
		}
		return nil, nil
	}
	if journal.InFlight != "" || len(journal.Owners) != len(journal.Completed) {
		return bad()
	}
	var successor *deploymentplan.Plan
	var pendingUpdate *deploymentplan.Plan
	if journal.Accepted && entry.State == "completed" {
		accepted, acceptedJournal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
		checked, exists, progressErr := readEnvironmentCreateStageProgress(store, e, stage, plan)
		if err != nil || progressErr != nil || !exists || accepted.SHA256() != plan.SHA256() || deploymentplan.Digest(acceptedJournal) != deploymentplan.Digest(journal) || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
			return bad()
		}
	} else if entry.State == "pending" || journal.Accepted && entry.State == "allocated" {
		checked, ok, err := readEnvironmentCreateStageProgress(store, e, stage, plan)
		if err != nil || !ok || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
			return bad()
		}
		if journal.Failed != "" && !journal.AcceptanceStarted && len(journal.Completed) == len(plan.Resources) {
			pendingUpdate, err = earlyInitializerForwardSuccessorWithAdmission(ctx, store, e, b, plan, journal, load)
			if err != nil {
				return bad()
			}
			if pendingUpdate == nil {
				if _, err := qualifyCreateFailedFullyWrittenStage(ctx, store, e, stage, b); err != nil {
					return nil, err
				}
			}
		}
	} else {
		if journal.Failed == "" || journal.AcceptanceStarted || len(journal.Completed) != len(plan.Resources) {
			return bad()
		}
		var alias createFailedForwardAlias
		if (entry.State != "allocated" && entry.State != "completed") || decodeEnvironmentJSON(entry.Receipt, &alias) != nil || !environmentPreflightDigest.MatchString(alias.QualificationSHA256) {
			return bad()
		}
		q, repair, retained, err := loadFailedForwardBundle(store, e, alias.QualificationSHA256)
		if err != nil || q.ParentOwnerSHA256 != deploymentplan.Digest(e.owner) || q.FailedStage.ParentSHA256 != e.Journal.PlanSHA256 || q.FailedStage.Stage != stage || q.FailedStage.ParentIntentSHA256 != entry.IntentSHA256 || q.FailedStage.Child.PlanSHA256 != plan.SHA256() || q.FailedStage.Child.JournalSHA256 != deploymentplan.Digest(journal) || q.FailedStage.Child.FailedResource != journal.Failed || !partialSessionJournalExtends(q.SourceJournal, e.Journal) {
			return bad()
		}
		repairStage, retainStage := failedForwardStageNames(q)
		if alias.OriginalPlanSHA256 != plan.SHA256() || alias.OriginalJournalSHA256 != q.FailedStage.Child.JournalSHA256 || alias.FailedResource != journal.Failed || alias.RepairStage != "kubernetes/"+repairStage || alias.RepairPlanSHA256 != repair.SHA256() {
			return bad()
		}
		if alias.InitializerMountQualificationSHA256 != "" {
			successor, err = verifyInitializerMountSuccessorAliasWithAdmission(ctx, store, e, b, alias, plan, journal, q, repair, retained, load)
			if err != nil {
				return bad()
			}
			if alias.ServiceSessionQualificationSHA256 != "" {
				// The accepted current-owner child already includes this session.
				// Start the shared session fold from the original q2 body once.
				_, _, successor, err = loadInitializerMountForwardBundle(store, e, alias.InitializerMountQualificationSHA256)
				if err != nil {
					return bad()
				}
			}
		} else if alias.InitializerQualificationSHA256 != "" {
			successor, err = verifyInitializerSuccessorAlias(ctx, store, e, b, alias, plan, journal, q, repair, retained)
			if err != nil {
				return bad()
			}
		} else {
			if alias.RetainStage != "kubernetes/"+retainStage || alias.RetainPlanSHA256 != retained.SHA256() {
				return bad()
			}
			acceptedRepair, _, err := acceptedCreateStage(store, e, alias.RepairStage)
			if err != nil || acceptedRepair.SHA256() != repair.SHA256() {
				return bad()
			}
			acceptedRetain, _, err := acceptedCreateStage(store, e, alias.RetainStage)
			if err != nil || acceptedRetain.SHA256() != retained.SHA256() {
				return bad()
			}
			observed, err := plan.QualifyFullyWrittenFailedForward(ctx, b, store.Root, plan.SHA256(), journal.Failed)
			if err != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(q.FailedStage.Child) {
				return bad()
			}
		}
	}
	var bases []activeTransportRenewalResource
	for i, resource := range plan.Resources[:len(journal.Completed)] {
		if journal.Completed[i] != resource.ID || journal.Owners[resource.ID] == "" {
			return bad()
		}
		desired := resource.Desired
		if successor != nil && entry.State != "completed" {
			desired = successor.Resources[i].Desired
		}
		if pendingUpdate != nil && pendingUpdate.Resources[0].ID == resource.ID {
			desired = pendingUpdate.Resources[0].Desired
		}
		bases = append(bases, sessionRecordedOwnerBase(resource.ID, journal.Owners[resource.ID], desired))
	}
	var latest []activeTransportRenewalResource
	if entry.State == "completed" {
		// The completed logical source is history. Fold all later accepted and
		// known ordinary writes before comparing its current native owners.
		var pending map[string]completedRecordedSuccessorStage
		pending, err = authenticatedRecordedPendingWritesWithAdmission(ctx, store, e, b, load)
		if err == nil {
			latest, err = completedRecordedOwnerSuccessorsAfterHistory(ctx, store, e, b, "kubernetes/"+stage, bases, pending)
		}
	} else {
		latest, err = completedSessionRecordedSuccessors(ctx, store, e, bases)
	}
	if err != nil {
		return bad()
	}
	var writes []activeTransportRenewalResource
	for i, resource := range plan.Resources[:len(journal.Completed)] {
		uid := journal.Owners[resource.ID]
		if journal.Completed[i] != resource.ID || uid == "" || resource.UID != "" && resource.UID != uid {
			return bad()
		}
		desired := latest[i].Desired
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(desired, actual) {
			return bad()
		}
		if resource.Action != "update" {
			continue
		}
		if resource.UID != uid || resource.Baseline == nil {
			return bad()
		}
		writes = append(writes, activeTransportRenewalResource{ID: resource.ID, UID: uid, Before: resource.Baseline, Desired: desired})
	}
	if err := validateApplicationWriteLineage(store, e, writes); err != nil {
		return nil, err
	}
	fresh, exists, err := plan.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(fresh) != deploymentplan.Digest(journal) || e.Check(ctx) != nil {
		return bad()
	}
	return writes, nil
}

// Bind every later update to its most recent accepted predecessor. The
// separately completed transport successor may be the intervening writer only
// when that predecessor was already present in its authenticated source journal.
func validateApplicationWriteLineage(store secretStore, e *environmentCreateExecution, writes []activeTransportRenewalResource) error {
	bad := func() error {
		return createProviderRecoveryBlock("completed application write has no continuous original owner history")
	}
	if e == nil || e.Store != store || e.Check(context.Background()) != nil {
		return bad()
	}
	end := -1
	for i, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/managed-application-owners" {
			end = i
			break
		}
	}
	if end < 0 {
		return bad()
	}
	var successor []activeTransportRenewalResource
	var successorSource environmentCreateJournal
	successorLoaded := false
	for _, write := range writes {
		found := false
		for i := end - 1; i >= 0; i-- {
			entry := e.Journal.Entries[i]
			if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
				continue
			}
			var receipt struct {
				Plan   string            `json:"plan_sha256"`
				Owners map[string]string `json:"owners"`
			}
			if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Owners[write.ID] == "" || !environmentPreflightDigest.MatchString(receipt.Plan) {
				continue
			}
			plan, journal, err := acceptedCreateStage(store, e, entry.Key)
			if err != nil || plan.SHA256() != receipt.Plan || journal.Owners[write.ID] != write.UID || receipt.Owners[write.ID] != write.UID {
				return bad()
			}
			var prior *deploymentplan.Resource
			for n := range plan.Resources {
				if plan.Resources[n].ID == write.ID {
					prior = &plan.Resources[n]
					break
				}
			}
			if prior == nil || prior.Action == "retire" {
				return bad()
			}
			found = true
			if deploymentplan.Matches(prior.Desired, write.Before) {
				break
			}
			if !successorLoaded {
				successor, err = completedInitialTransportSuccessorOriginalResources(store, e.Journal.PlanSHA256)
				if err != nil {
					return err
				}
				var proof initialTransportSuccessorProof
				pointer := filepath.Join(store.Root, "deployment", "create", "material", e.Journal.PlanSHA256, "initial-transport-successor.json")
				if readExecutionJSON(pointer, &proof) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment", "create", "transport-successor", proof.Review, "source-journal.json"), &successorSource) != nil {
					return bad()
				}
				successorLoaded = true
			}
			if !applicationWriteMatchesSuccessor(*prior, entry, write, successor, successorSource) {
				return bad()
			}
			break
		}
		if !found {
			return bad()
		}
	}
	return e.Check(context.Background())
}

func applicationWriteMatchesSuccessor(prior deploymentplan.Resource, entry environmentCreateJournalEntry, write activeTransportRenewalResource, successor []activeTransportRenewalResource, source environmentCreateJournal) bool {
	beforeSuccessor := false
	for _, original := range source.Entries {
		if original.Key == entry.Key && deploymentplan.Digest(original) == deploymentplan.Digest(entry) {
			beforeSuccessor = true
			break
		}
	}
	for _, next := range successor {
		if beforeSuccessor && next.ID == write.ID && next.UID == write.UID && deploymentplan.Matches(prior.Desired, next.Before) && deploymentplan.Matches(next.Desired, write.Before) {
			return true
		}
	}
	return false
}
