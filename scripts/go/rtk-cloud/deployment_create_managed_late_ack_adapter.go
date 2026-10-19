package main

// Private integration draft, unlinked until genuine Core restoration exists.

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateOriginalACKReview(r *createServiceSessionRecovery) string {
	if r != nil && r.ManagedLateSession != nil {
		return r.ManagedLateSession.OriginalRepairReview
	}
	if r == nil {
		return ""
	}
	return r.Review
}

// Historical role publication remains bound to the original accepted one-ST
// repair. Only an independently admitted, genuinely accepted SID-only owner
// forward can provide its current body; source/prepared candidates do not.
func managedLateOpenBaoCurrentDesired(ctx context.Context, cfg deploymentConfig,
	runtime environmentCreateBootstrapRuntime,
	original deploymentplan.Resource) (deploymentplan.Object, deploymentplan.Object, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || e == nil || e.Check(ctx) != nil {
		return nil, nil, createOpenBaoACKBlock("late-current-owner")
	}
	late := r.ManagedLateSession
	if late == nil || r.Ceremony.BootstrapSessionID != late.NewSessionID {
		return original.Desired, nil, nil
	}
	if cfg.Environment != "dev" || late.OwnerCandidate == nil || r.Review != late.Review || !environmentPreflightDigest.MatchString(late.Review) || late.OriginalRepairReview == late.Review || late.ExpiredRenewal == nil && late.OriginalEnrollmentSession != late.OriginalSession || late.NewSessionID == late.OriginalSession {
		return nil, nil, createOpenBaoACKBlock("late-distinct-original-session")
	}
	a, source, err := loadAdmittedCreateRecovery(e.Store, e, late.Review)
	if err != nil || !managedLateSchemaMatches(a.Plan.Schema, late) || a.Plan.ParentJournalSHA != late.SourceSHA || deploymentplan.Digest(source) != late.SourceSHA || a.Plan.Materials["accepted-service-core-late-session-owners-plan"] != late.OwnerCandidate.SHA256() {
		return nil, nil, createOpenBaoACKBlock("late-admitted-original-owners")
	}
	if late.ExpiredRenewal != nil && createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-late-session-owners-"+late.Review) == nil {
		if _, _, err := managedLateSessionRuntimeProgress(e.Store, e, late); err != nil {
			return nil, nil, err
		}
		return original.Desired, nil, nil
	}
	if r.ManagedLatePostMint != nil && createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-late-session-owners-"+late.Review) == nil {
		framed, post, frameErr := managedLatePostMintRuntimeFrame(e.Store, e, late)
		if frameErr != nil {
			return nil, nil, frameErr
		}
		if _, _, progressErr := managedLateSessionRuntimeProgress(e.Store, e, late); progressErr != nil {
			return nil, nil, progressErr
		}
		currentSourceSHA := deploymentplan.Digest(e.Journal)
		if post == nil && currentSourceSHA != r.ManagedLatePostMint.SourceSHA {
			appView, _, appErr := managedLateAppRootCRLFrame(e.Store, e, false)
			if appErr != nil {
				return nil, nil, appErr
			}
			currentSourceSHA = deploymentplan.Digest(appView.Journal)
		}
		if post == nil && currentSourceSHA != r.ManagedLatePostMint.SourceSHA || post != nil && framed == nil {
			return nil, nil, createOpenBaoACKBlock("late-post-mint-original-host-source")
		}
		return original.Desired, nil, nil
	}
	plan, j, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/managed-late-session-owners-"+late.Review)
	if err != nil || !managedCoreSameQualifiedPlan(plan, managedLateExpectedOwnerCandidate(r)) {
		return nil, nil, createOpenBaoACKBlock("late-accepted-current-owners")
	}
	var selected *deploymentplan.Resource
	for i := range plan.Resources {
		resource := &plan.Resources[i]
		if resource.ID == original.ID {
			if selected != nil || resource.UID != original.UID || j.Owners[resource.ID] != original.UID || resource.Action != "update" || !deploymentplan.Matches(original.Desired, resource.Baseline) {
				return nil, nil, createOpenBaoACKBlock("late-same-statefulset")
			}
			selected = resource
		}
	}
	if selected == nil {
		return nil, nil, createOpenBaoACKBlock("late-current-statefulset-missing")
	}
	// Reconstruct the sanctioned SID-only recipe from its original source,
	// preserving every unrelated env, provider command/image, state and mount.
	expected := deploymentplan.Clone(original.Desired)
	changed := 0
	for _, group := range []string{"containers", "initContainers"} {
		for _, raw := range certIssuerObjectList(environmentPKIPod(expected)[group]) {
			container := deploymentplan.Map(raw)
			fields, err := environmentPKINamedFields(container["env"])
			if err != nil {
				return nil, nil, createOpenBaoACKBlock("late-unambiguous-session-env")
			}
			if field := fields["PKI_BOOTSTRAP_SESSION_ID"]; field != nil {
				if field["value"] != managedLateOriginalWorkloadSession(late) || field["valueFrom"] != nil {
					return nil, nil, createOpenBaoACKBlock("late-original-session-env")
				}
				field["value"] = late.NewSessionID
				changed++
			}
		}
	}
	if changed == 0 || deploymentplan.Digest(expected) != deploymentplan.Digest(selected.Desired) {
		return nil, nil, createOpenBaoACKBlock("late-only-session-env-delta")
	}
	proof := managedLateACKSourceProof(late, plan)
	if r.ManagedLateRemaining != nil {
		if err := verifyManagedLateRemainingACKReadSource(e.Store, e, r.ManagedLateRemaining); err != nil {
			return nil, nil, err
		}
		if r.ManagedLateRemaining.Authority != nil {
			proof["late_remaining_authority_sha256"] = deploymentplan.Digest(r.ManagedLateRemaining.Authority)
			proof["late_remaining_review_sha256"] = r.ManagedLateRemaining.Authority.Review
			if r.ManagedLateRemaining.CleanupOnly && r.ManagedLateRemaining.CleanupAuthority != nil {
				phase := r.ManagedLateRemaining.CleanupAuthority
				if !managedLateInstalledCleanupMatches(r.ManagedLateRemaining, phase) {
					return nil, nil, createOpenBaoACKBlock("installed-cleanup-native-proof")
				}
				proof["installed_cleanup_review_sha256"] = phase.Review
				proof["installed_cleanup_authority_sha256"] = deploymentplan.Digest(phase)
				if r.ManagedLateRemaining.FinalizationOnly && r.ManagedLateRemaining.FinalizationAuthority != nil {
					final := r.ManagedLateRemaining.FinalizationAuthority
					if !managedLateFinalizationMatches(r.ManagedLateRemaining, final) {
						return nil, nil, createOpenBaoACKBlock("finalization-only-native-proof")
					}
					proof["finalization_only_review_sha256"] = final.Review
					proof["finalization_only_authority_sha256"] = deploymentplan.Digest(final)
				}
			}
		}
	}
	return selected.Desired, proof, e.Check(ctx)
}

func managedLateACKSourceProof(late *createManagedLateSessionRecovery, plan *deploymentplan.Plan) deploymentplan.Object {
	return deploymentplan.Object{"late_descriptor_sha256": managedLateSessionSourceDigest(late), "late_review_sha256": late.Review, "late_source_sha256": late.SourceSHA, "late_owners_plan_sha256": plan.SHA256(), "original_session_id": late.OriginalSession}
}
