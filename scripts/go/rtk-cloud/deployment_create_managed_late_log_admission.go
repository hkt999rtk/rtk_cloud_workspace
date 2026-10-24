package main

import (
	"context"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Preserve the historical create plan. This local admission view binds only
// the allocation already accepted under that exact plan and its terminal proof.
func managedLateRetainedLogHelperPlan(late *createManagedLateSessionRecovery, accepted *deploymentplan.Plan, journal deploymentplan.Journal, observation createManagedLateLogObservation) (*deploymentplan.Plan, error) {
	if late == nil || validateManagedLateLogCandidates(late) != nil || !createOpenBaoACKCompletedChild(accepted, journal) || !journal.AcceptanceStarted || len(accepted.Resources) != 1 || !managedCoreSameQualifiedPlan(accepted, late.LogHelperCandidate) {
		return nil, logACKDraftBlock("accepted helper admission source")
	}
	resource := accepted.Resources[0]
	uid := journal.Owners[resource.ID]
	meta := deploymentplan.Map(observation.Job["metadata"])
	if !environmentBootstrapOwnerUUID.MatchString(uid) || resource.Action != "create" || resource.UID != "" || resource.Baseline != nil || meta["uid"] != uid || meta["deletionTimestamp"] != nil || deploymentplan.ID(observation.Job) != resource.ID || !deploymentplan.Matches(resource.Desired, observation.Job) || validateManagedLateLogObservation(late, observation, uid) != nil {
		return nil, logACKDraftBlock("accepted helper terminal owner admission")
	}
	view := *accepted
	resource.Action, resource.UID = "retain", uid
	resource.Desired, resource.Baseline = deploymentplan.Clone(resource.Desired), deploymentplan.Projection(observation.Job)
	view.Resources = []deploymentplan.Resource{resource}
	return &view, nil
}

func admitManagedLateRemainingLogHelper(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) error {
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateSession == nil || e == nil || r.ManagedLateSession.ParentSHA != e.Journal.PlanSHA256 || validateManagedLateLogCandidates(r.ManagedLateSession) != nil {
		return logACKDraftBlock("remaining helper admission source")
	}
	late, remaining := r.ManagedLateSession, r.ManagedLateRemaining
	entry, err := managedLateLogEntry(e, "kubernetes/managed-late-log-existing-ack-"+late.Review)
	if err != nil {
		return err
	}
	if entry == nil {
		if remaining.CurrentSourceSHA != "" {
			return logACKDraftBlock("remaining helper acceptance absent")
		}
		return late.LogHelperCandidate.Admit(ctx, b)
	}
	accepted, journal, err := managedLateLogAccepted(store, e, late, "existing-ack", "")
	if err != nil {
		return err
	}
	uid := journal.Owners[accepted.Resources[0].ID]
	retired, err := managedLateLogEntry(e, "kubernetes/managed-late-log-retire-"+late.Review)
	if err != nil {
		return err
	}
	var observation createManagedLateLogObservation
	if retired == nil {
		producer := *late.LogProducer
		state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, late.NewSessionID)
		if err != nil {
			return err
		}
		producer.FreshReview, producer.NewSession = late.Review, state
		observation, err = readManagedLateLogObservation(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, late, &producer, uid)
		if err != nil {
			return err
		}
		if !environmentPreflightDigest.MatchString(remaining.HelperObservationSHA) || deploymentplan.Digest(observation) != remaining.HelperObservationSHA {
			return logACKDraftBlock("qualified terminal helper admission changed")
		}
		view, err := managedLateRetainedLogHelperPlan(late, accepted, journal, observation)
		if err != nil {
			return err
		}
		return view.Admit(ctx, b)
	}
	// A separately accepted retirement cannot resurrect the historical create.
	// Its prior terminal observation and actual descendant absence remain proof.
	if _, _, err := managedLateLogAccepted(store, e, late, "retire", uid); err != nil {
		return err
	}
	path := managedLateLogFile(store, late.Review, "managed-late-log-observation.json")
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readExecutionJSON(path, &observation) != nil || validateManagedLateLogObservation(late, observation, uid) != nil || !environmentPreflightDigest.MatchString(remaining.HelperObservationSHA) || deploymentplan.Digest(observation) != remaining.HelperObservationSHA {
		return logACKDraftBlock("retired helper terminal admission proof")
	}
	job, err := b.Read(ctx, accepted.Resources[0].ID)
	if err != nil || job != nil {
		return logACKDraftBlock("retired helper admission absence")
	}
	namespace := deploymentplan.String(deploymentplan.Map(observation.Job["metadata"])["namespace"])
	pods, err := readManagedLateLogClaimPods(ctx, b, namespace)
	if err != nil {
		return err
	}
	for _, pod := range pods {
		for _, value := range certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"]) {
			if deploymentplan.Map(value)["uid"] == uid {
				return logACKDraftBlock("retired helper descendant remains")
			}
		}
	}
	return nil
}
