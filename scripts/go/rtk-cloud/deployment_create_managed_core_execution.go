package main

import (
	"context"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func executeManagedCorePartialForward(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedCorePartial == nil || e == nil || apply == nil || e.Check(ctx) != nil {
		return managedCorePartialBlock("exclusive-forward-owner")
	}
	partial := r.ManagedCorePartial
	if partial.Action != managedCoreRestoreOnly || !environmentPreflightDigest.MatchString(partial.ForwardReview) || r.Review != partial.OriginalRepairReview || validateManagedCorePartialCandidate(partial, r.Candidate) != nil {
		return managedCorePartialBlock("qualified-core-candidate")
	}
	admitted, source, err := loadAdmittedCreateRecovery(e.Store, e, partial.ForwardReview)
	if err != nil || admitted.Plan.Schema != managedCorePartialSchema || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || admitted.Plan.Materials["accepted-service-core-managed-complete-observation-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || managedCorePartialReview(source) != partial.OriginalRepairReview || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] != r.Candidate.SHA256() || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-source-plan"] != partial.Plan.SHA256() || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-source-journal"] != deploymentplan.Digest(partial.Journal) {
		return managedCorePartialBlock("actual-complete-go-admission")
	}
	defs, err := managedCorePartialDefinitions(partial)
	if err != nil {
		return err
	}
	live, err := runtime.Backend.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	fresh, err := deploymentplan.Compile(r.Candidate.Inputs, defs, live)
	if err != nil || fresh.SHA256() != r.Candidate.SHA256() {
		return managedCorePartialBlock("unchanged-exact-current-baselines")
	}
	stage := "managed-complete-issuer-forward-" + partial.ForwardReview
	if err := apply(ctx, stage, defs); err != nil {
		return err
	}
	actual, j, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/"+stage)
	if err != nil || !managedCoreSameQualifiedPlan(actual, r.Candidate) {
		return managedCorePartialBlock("genuine-accepted-forward-child")
	}
	for _, resource := range r.Candidate.Resources {
		if resource.UID == "" || j.Owners[resource.ID] != resource.UID {
			return managedCorePartialBlock("unchanged-original-core-uids")
		}
	}
	proof := map[string]string{"original_plan_sha256": partial.Plan.SHA256(), "original_failed_journal_sha256": deploymentplan.Digest(partial.Journal), "actual_qualified_forward_stage": "kubernetes/" + stage, "actual_qualified_forward_plan_sha256": actual.SHA256(), "original_repair_review_sha256": partial.OriginalRepairReview}
	if previous := partial.PriorForward; previous != nil {
		alias := map[string]string{"original_plan_sha256": previous.Plan.SHA256(), "original_failed_journal_sha256": deploymentplan.Digest(previous.Journal), "actual_qualified_forward_stage": "kubernetes/" + stage, "actual_qualified_forward_plan_sha256": actual.SHA256(), "original_repair_review_sha256": partial.OriginalRepairReview}
		key := "kubernetes/managed-complete-issuer-forward-" + previous.Review
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(alias)); err != nil {
			return err
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := e.RecordReceipt("kubernetes/managed-complete-issuer", mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	if err := e.CompleteEntry("kubernetes/managed-complete-issuer", time.Now().UTC()); err != nil {
		return err
	}
	if partial.CertIssuerPrepare != nil {
		return publishCreateCertIssuerPrepareCompletion(ctx, cfg, runtime)
	}
	return nil
}

func verifyManagedCorePartialCompletedStages(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedCorePartial == nil || e == nil || e.Check(ctx) != nil {
		return managedCorePartialBlock("completed-core-owner")
	}
	partial := r.ManagedCorePartial
	route, rj, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/native-app-routing-profile")
	if err != nil || route.SHA256() != partial.Routing.SHA256() || deploymentplan.Digest(rj) != deploymentplan.Digest(partial.RoutingJournal) {
		return managedCorePartialBlock("accepted-original-routing")
	}
	core, j, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/managed-complete-issuer-forward-"+partial.ForwardReview)
	if err != nil || !managedCoreSameQualifiedPlan(core, r.Candidate) {
		return managedCorePartialBlock("accepted-repaired-core")
	}
	for _, resource := range r.Candidate.Resources {
		body, err := runtime.Backend.Read(ctx, resource.ID)
		if err != nil || body == nil || j.Owners[resource.ID] != resource.UID || deploymentplan.String(deploymentplan.Map(body["metadata"])["uid"]) != resource.UID || !deploymentplan.Matches(resource.Desired, body) {
			return managedCorePartialBlock("current-repaired-core-body")
		}
	}
	for _, resource := range route.Resources {
		body, err := runtime.Backend.Read(ctx, resource.ID)
		if err != nil || body == nil || deploymentplan.String(deploymentplan.Map(body["metadata"])["uid"]) != rj.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, body) {
			return managedCorePartialBlock("current-original-routing-body")
		}
	}
	var stored environmentAppRoutingBinding
	// The dedicated routing loader and accepted child remain the authority; a
	// file's mere presence can never substitute for that native owned ConfigMap.
	path := filepath.Join(e.Store.Root, "pki/app/routing.json")
	if err := readExecutionJSON(path, &stored); err != nil || stored.CreatePlanSHA256 != e.Journal.PlanSHA256 {
		return managedCorePartialBlock("original-routing-parent")
	}
	return e.Check(ctx)
}

func completeManagedCorePartialBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) (environmentIdentityBindings, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedCorePartial == nil || r.ManagedCorePartial.Action == managedCoreRestoreOnly || r.OpenBaoACK == nil || r.ManagedTransportRecovery == nil {
		return environmentIdentityBindings{}, managedCorePartialBlock("completed-original-ceremony")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"service": service.Root, "device": r.ManagedTransportRecovery.Device, "app": r.ManagedTransportRecovery.AppRoot, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	issuers := map[string]environmentCreateRegisteredIssuer{"service": service.Intermediate, "app": r.ManagedTransportRecovery.AppIntermediate, "mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, issuers, nil); err != nil {
		return environmentIdentityBindings{}, err
	}
	listeners, err := configureCreateBrokerServiceConsumer(p, runtime.InitialListeners)
	if err != nil {
		return environmentIdentityBindings{}, err
	}
	runtime.InitialListeners = listeners
	if err := executeManagedCorePartialForward(ctx, cfg, runtime, apply); err != nil {
		return environmentIdentityBindings{}, err
	}
	return completeEnvironmentCreateAfterApplicationOwners(ctx, cfg, p, release, store, operator, runtime, service, roots, issuers, apply)
}

func managedCoreSameQualifiedPlan(actual, qualified *deploymentplan.Plan) bool {
	if actual == nil || qualified == nil {
		return false
	}
	normalized := *actual
	normalized.BindingsSHA256 = qualified.BindingsSHA256
	return normalized.SHA256() == qualified.SHA256()
}

// Reconcile a real admitted read-only interruption with the intended native
// audit/authentication receipt. This never changes the old intent or time and
// never reports the old admission as a successful Kubernetes forward.
func managedCorePriorAdmissionProof(j environmentCreateJournal, core *createManagedCorePartialRecovery, fresh createFoundationRecoveryArtifact, audit []byte, now time.Time) (string, map[string]string, error) {
	if core == nil {
		return "", nil, managedCorePartialBlock("prior-admission-source")
	}
	prior := core.PriorAdmission
	if prior == nil {
		return "", nil, nil
	}
	if core.Action != managedCoreRestoreOnly || !environmentPreflightDigest.MatchString(core.ForwardReview) || prior.Review == core.ForwardReview || prior.SourceSHA != core.SourceSHA || deploymentplan.Digest(j) != prior.SourceSHA || fresh.Plan.Schema != managedCorePartialSchema || fresh.Plan.ParentJournalSHA != core.SourceSHA || deploymentplan.Digest(fresh.Plan) != core.ForwardReview || fresh.Qualification.PlanSHA256 != core.ForwardReview || fresh.Qualification.Decision != "GO" || fresh.QualifiedAt.After(now) || !fresh.ValidUntil.Equal(fresh.QualifiedAt.Add(15*time.Minute)) || !now.Before(fresh.ValidUntil) || len(fresh.Qualification.Gates) != 6 || fresh.Plan.Materials["accepted-service-core-complete-issuer-partial-prior-admission"] != deploymentplan.Digest(prior) || fresh.Plan.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] != prior.CandidateSHA || fresh.Plan.Materials["accepted-service-core-complete-issuer-partial-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || fresh.Plan.Materials["accepted-service-core-managed-complete-observation-action"] != deploymentplan.Digest(managedCoreRestoreOnly) || len(audit) == 0 || !environmentPreflightDigest.MatchString(prior.AuditSHA) || environmentPreflightBytesSHA256(audit) != prior.AuditSHA || len(j.Entries) == 0 {
		return "", nil, managedCorePartialBlock("fresh-prior-admission-native-proof")
	}
	ids := []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"}
	for index, gate := range fresh.Qualification.Gates {
		if gate.ID != ids[index] || !gate.Required || gate.Status != "PASS" || gate.EvidenceSHA256 != core.ForwardReview {
			return "", nil, managedCorePartialBlock("fresh-prior-admission-six-gates")
		}
	}
	entry := j.Entries[len(j.Entries)-1]
	if entry.Key != "openbao-recovery-admission-"+prior.Review || entry.Phase != "recovery" || entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 || deploymentplan.Digest(entry) != deploymentplan.Digest(prior.Entry) {
		return "", nil, managedCorePartialBlock("exact-original-pending-admission")
	}
	return entry.Key, map[string]string{"audit_sha256": prior.AuditSHA, "qualified_recovery_sha256": prior.Review}, nil
}

func completeManagedCorePriorAdmission(ctx context.Context, e *environmentCreateExecution, core *createManagedCorePartialRecovery, fresh createFoundationRecoveryArtifact, audit []byte) error {
	if e == nil || e.Check(ctx) != nil {
		return managedCorePartialBlock("prior-admission-owner")
	}
	if err := managedCoreObservationSource(e.Store, e, core); err != nil {
		return err
	}
	if core != nil && core.PriorForward != nil {
		if core.PriorAdmission == nil || deploymentplan.Digest(e.Journal) != core.SourceSHA || fresh.Plan.Schema != managedCorePartialSchema || deploymentplan.Digest(fresh.Plan) != core.ForwardReview || fresh.Plan.ParentJournalSHA != core.SourceSHA || fresh.Qualification.Decision != "GO" || fresh.Qualification.PlanSHA256 != core.ForwardReview || time.Now().UTC().Before(fresh.QualifiedAt) || !time.Now().UTC().Before(fresh.ValidUntil) || !fresh.ValidUntil.Equal(fresh.QualifiedAt.Add(15*time.Minute)) || fresh.Plan.Materials["accepted-service-core-complete-issuer-partial-prior-forward"] != deploymentplan.Digest(core.PriorForward) || environmentPreflightBytesSHA256(audit) != core.PriorAdmission.AuditSHA {
			return managedCorePartialBlock("completed-prior-admission-source")
		}
		ids := []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"}
		if len(fresh.Qualification.Gates) != len(ids) {
			return managedCorePartialBlock("completed-prior-admission-six-gates")
		}
		for i, gate := range fresh.Qualification.Gates {
			if gate.ID != ids[i] || !gate.Required || gate.Status != "PASS" || gate.EvidenceSHA256 != core.ForwardReview {
				return managedCorePartialBlock("completed-prior-admission-six-gates")
			}
		}
		prior := createOpenBaoACKCompletionEntry(e.Journal, core.PriorAdmission.Entry.Key)
		var receipt map[string]string
		if prior == nil || prior.State != "completed" || prior.CompletedAt.IsZero() || prior.IntentSHA256 != core.PriorAdmission.Entry.IntentSHA256 || !prior.StartedAt.Equal(core.PriorAdmission.Entry.StartedAt) || decodeEnvironmentJSON(prior.Receipt, &receipt) != nil || len(receipt) != 2 || receipt["audit_sha256"] != core.PriorAdmission.AuditSHA || receipt["qualified_recovery_sha256"] != core.PriorAdmission.Review {
			return managedCorePartialBlock("genuine-already-completed-prior-admission")
		}
		return nil
	}
	key, proof, err := managedCorePriorAdmissionProof(e.Journal, core, fresh, audit, time.Now().UTC())
	if err != nil || key == "" {
		return err
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
