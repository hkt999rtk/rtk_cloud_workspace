package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedLateCompletedTail struct {
	SourceSHA, OwnerSHA, ProofSHA, ViewSHA, AuthoritySHA, LateReview string
	SourceLeases                                                     int
	OverallDeadline                                                  time.Time
	NativeEvidence                                                   map[string]string
	normal                                                           *createManagedLateNormalTail
	view                                                             *environmentCreateExecution
}

func managedLateCompletedTailSelected(j environmentCreateJournal) bool {
	if len(j.Entries) != 428 && !managedLateNormalTailSelected(j) || !j.CompletedAt.IsZero() || len(j.PlanSHA256) != 64 {
		return false
	}
	entry := j.Entries[395]
	if entry.Key != "service-session-finalize" || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
		return false
	}
	name := "pki-export-account-manager-" + j.PlanSHA256[:12]
	keys := []string{"kubernetes/server-custody-account-manager", "server-export-account-manager", "retire-" + name, "kubernetes/retire-" + name}
	for i, e := range j.Entries[424:428] {
		if e.Key != keys[i] || e.State != "completed" || e.StartedAt.IsZero() || e.CompletedAt.IsZero() || e.CompletedAt.Before(e.StartedAt) || len(e.Receipt) == 0 {
			return false
		}
	}
	return true
}

func loadManagedLateCompletedTailSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, error) {
	bad := func() (*createServiceSessionRecovery, error) {
		return nil, createOpenBaoACKBlock("completed-normal-tail-source")
	}
	if e == nil || e.Check(ctx) != nil || store != e.Store || !managedLateCompletedTailSelected(e.Journal) {
		return bad()
	}
	if managedLateRedisPrePauseSelected(e.Journal) {
		if err := verifyManagedLateRedisPrePause(ctx, store, e); err != nil {
			return nil, err
		}
	}
	var normalApproval *createManagedLateNormalTailApproval
	if managedLateNormalTailSelected(e.Journal) {
		var approvalErr error
		normalApproval, approvalErr = loadManagedLateNormalTailApproval(store, e.Journal.PlanSHA256)
		if approvalErr != nil || normalApproval == nil || normalApproval.SourceSHA != deploymentplan.Digest(managedLateNormalTailOriginalSource(e.Journal)) || normalApproval.OwnerSHA != deploymentplan.Digest(e.owner) {
			return nil, createOpenBaoACKBlock("normal-tail-exact-approved-source")
		}
	}
	raw, err := readManagedLateResidualRemainingPair(store, e.Journal.PlanSHA256)
	var header createManagedLateResidualCleanupAuthority
	var archived createManagedLateRemaining
	if err != nil || raw == nil || decodeEnvironmentJSON(raw, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &archived) != nil || archived.ParentSHA != e.Journal.PlanSHA256 || archived.Review != header.Review || archived.ResidualRemaining == nil {
		return bad()
	}
	if archived.Authority, err = readManagedLateRemainingAuthority(store, &archived); err != nil {
		return nil, err
	}
	if archived.CleanupAuthority, err = readManagedLateInstalledCleanupAuthority(store, &archived); err != nil {
		return nil, err
	}
	if archived.FinalizationAuthority, err = readManagedLateFinalizationAuthority(store, &archived); err != nil {
		return nil, err
	}
	if archived.TerminalAuthority, err = readManagedLateTerminalFinalizerAuthority(store, &archived); err != nil {
		return nil, err
	}
	if archived.PostNativeAuthority, err = readManagedLatePostNativeCleanupAuthority(store, &archived); err != nil {
		return nil, err
	}
	if archived.ResidualAuthority, err = readManagedLateResidualCleanupAuthority(store, &archived); err != nil {
		return nil, err
	}
	if archived.ResidualRemainingAuthority, err = readManagedLateResidualRemainingAuthority(store, &archived); err != nil || archived.ResidualRemainingAuthority == nil || deploymentplan.Digest(archived.ResidualRemainingAuthority) != deploymentplan.Digest(header) || normalApproval == nil && !time.Now().Before(header.OverallDeadline) {
		return bad()
	}
	var late createManagedLateSessionRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", archived.OriginalReview, "managed-late-session-source.json"), &late) != nil || late.Review != archived.OriginalReview || managedLateSessionSourceDigest(&late) != archived.DescriptorSHA || validateManagedLateTerminalFinalizerCompletion(store, e, &late) != nil {
		return bad()
	}
	if err := validateManagedLateCompletedNormalStages(store, e, &archived); err != nil {
		return nil, err
	}
	if normalApproval != nil {
		if err := validateManagedLateNormalTailStages(store, e, &archived); err != nil {
			return nil, err
		}
	}
	view, err := managedLateCompletedTailPureSourceView(ctx, e, &archived)
	if err != nil {
		return nil, err
	}
	r, err := loadManagedLateRemainingSource(ctx, cfg, store, view)
	if err != nil {
		return nil, err
	}
	if r.ManagedLateRemaining == nil || managedLateRemainingDigest(r.ManagedLateRemaining) != managedLateRemainingDigest(&archived) || !managedLateQualifiedAncestryMatches(r.ManagedLateSession, &late) {
		return bad()
	}
	r.ManagedLateRemaining = &archived
	r.Ceremony = archived.Ceremony
	r.Review = late.Review
	r.ManagedLateSession = &late
	if err := restoreManagedLateRemainingHistoricalCleanup(r, archived.HistoricalSeedCleanup); err != nil {
		return nil, err
	}
	proofPath := managedLateTerminalFinalizerCompletedPath(archived.ParentSHA)
	proof, err := readBoundedEnvironmentFile(filepath.Join(store.Root, proofPath), true)
	if err != nil {
		return bad()
	}
	original, _, originalErr := loadAdmittedCreateRecovery(store, view, late.Review)
	if originalErr != nil {
		return nil, originalErr
	}
	reader, _, readerErr := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[424].Key)
	if readerErr != nil || len(reader.Resources) != 1 || reader.Resources[0].Action != "create" || reader.Resources[0].UID != "" || reader.Resources[0].Baseline != nil || !managedLateCompletedExportReaderMatches(reader.Resources[0].Desired, original.Plan.Plan.Bootstrap, archived.Ceremony, e.Journal.Stack+"-account-manager", "account-manager-service-state", environmentCreateServiceState, environmentCreateServiceState+"/private/client.json", "account-manager."+e.Journal.Stack+"-account-manager.svc.cluster.local") {
		return bad()
	}
	if normalApproval != nil {
		var pendingErr error
		if managedLateNormalTailControllerSelected(e.Journal) {
			_, _, pendingErr = loadEnvironmentCreateCompletedIngressAdmission(ctx, cfg, original.Plan.Plan.Bootstrap, original.Plan.Plan.Release, store, e)
		} else {
			_, _, pendingErr = loadEnvironmentCreatePendingIngressAdmission(ctx, cfg, original.Plan.Plan.Bootstrap, original.Plan.Plan.Release, store, e)
		}
		if pendingErr != nil {
			return nil, pendingErr
		}
	}
	r.completedTail = &createManagedLateCompletedTail{SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), ProofSHA: environmentPreflightBytesSHA256(proof), ViewSHA: deploymentplan.Digest(view.Journal), AuthoritySHA: deploymentplan.Digest(header), SourceLeases: len(e.Journal.KubernetesLeases), OverallDeadline: header.OverallDeadline, LateReview: late.Review, view: view}
	r.ManagedLateRemaining.completedTail = r.completedTail
	if normalApproval != nil {
		identity, _, identityErr := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[432].Key)
		if identityErr != nil {
			return nil, identityErr
		}
		n := &createManagedLateNormalTail{Schema: managedLateNormalTailSchema, CurrentSHA: deploymentplan.Digest(e.Journal), KubeconfigSHA: original.Plan.KubeconfigSHA, SourceSHA: deploymentplan.Digest(managedLateNormalTailOriginalSource(e.Journal)), OwnerSHA: deploymentplan.Digest(e.owner), OriginalAuthoritySHA: deploymentplan.Digest(header), CompletedProofSHA: r.completedTail.ProofSHA, PendingSHA: deploymentplan.Digest(managedLateNormalTailOriginalSource(e.Journal).Entries[431]), IdentityPlanSHA: identity.SHA256(), Approval: normalApproval}
		if normalApproval.OriginalAuthoritySHA != n.OriginalAuthoritySHA || normalApproval.CompletedProofSHA != n.CompletedProofSHA || normalApproval.PendingSHA != n.PendingSHA || normalApproval.IdentityPlanSHA != n.IdentityPlanSHA {
			return bad()
		}
		if oldCredential := r.ManagedLateRemaining.ResidualCleanup; oldCredential != nil {
			n.CredentialMode, n.CredentialSHA, n.CredentialExpires = oldCredential.CredentialMode, oldCredential.CredentialSHA, oldCredential.CredentialExpires
		}
		r.normalTail = n
		r.completedTail.normal = n
		existing, phaseErr := readManagedLateNormalTailCurrentAuthority(store, n)
		if phaseErr != nil {
			return nil, phaseErr
		}
		n.Authority = existing
		if err := prepareManagedLateNormalTailSupplement(ctx, cfg, store, e, r); err != nil {
			return nil, err
		}
		if existing != nil && !time.Now().Before(managedLateNormalTailEffectiveDeadline(r)) {
			return nil, createOpenBaoACKBlock("normal-tail-original-first-clock")
		}
		if managedLateNormalTailControllerSelected(e.Journal) {
			entry := e.Journal.Entries[433]
			if existing == nil || entry.StartedAt.Before(existing.AdmittedAt) || entry.CompletedAt.After(existing.Deadline) || e.Journal.Entries[431].CompletedAt.After(existing.Deadline) {
				return nil, createOpenBaoACKBlock("normal-tail-controller-outside-original-phase")
			}
		}
	}
	// Publish only successful immutable ancestry on this exact held source.
	// Normal journal writes invalidate the shared capability as before.
	if view.lateSourceReuse != nil {
		if err := checkManagedLateSourceReuse(view); err != nil {
			return nil, err
		}
		e.lateSourceReuse = view.lateSourceReuse
		e.lateSourceReuse.mu.Lock()
		e.lateSourceReuse.boundedSHA = deploymentplan.Digest(view.Journal)
		e.lateSourceReuse.currentSHA = deploymentplan.Digest(e.Journal)
		e.lateSourceReuse.mu.Unlock()
		if err := checkManagedLateSourceReuse(e); err != nil {
			return nil, err
		}
	}
	return r, e.Check(ctx)
}

func validateManagedLateCompletedNormalStages(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if e != nil && managedLateRedisPrePauseSelected(e.Journal) {
		if _, _, err := readManagedLateRedisPrePauseSource(store, e); err != nil {
			return err
		}
		view := *e
		view.Journal = managedLateRedisPrePauseOriginal(e.Journal)
		e = &view
	}
	bad := func() error { return createOpenBaoACKBlock("completed-normal-tail-four-stages") }
	if e == nil || r == nil || !managedLateCompletedTailSelected(e.Journal) || r.ResidualRemainingAuthority == nil || !managedLateResidualRemainingRetirementsComplete(e.Journal, r) {
		return bad()
	}
	ns := e.Journal.Stack + "-account-manager"
	name := "pki-export-account-manager-" + e.Journal.PlanSHA256[:12]
	id := "Deployment/" + ns + "/" + name
	created, cj, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[424].Key)
	if err != nil || len(created.Resources) != 1 || created.Resources[0].ID != id || !environmentBootstrapOwnerUUID.MatchString(cj.Owners[id]) {
		return bad()
	}
	retired, rj, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[427].Key)
	if err != nil || len(retired.Resources) != 1 || retired.Resources[0].ID != id || retired.Resources[0].Action != "retire" || retired.Resources[0].UID != cj.Owners[id] || rj.Owners[id] != cj.Owners[id] {
		return bad()
	}
	var export struct {
		Certificate string `json:"certificate_sha256"`
		Claim       string `json:"claim_uid"`
		Pod         string `json:"pod_uid"`
		Reader      string `json:"reader_uid"`
		ReplicaSet  string `json:"replicaset_uid"`
		Server      string `json:"server_name"`
	}
	var absent struct {
		ID     string `json:"resource_id"`
		UID    string `json:"uid"`
		Absent bool   `json:"absent"`
	}
	ex := e.Journal.Entries[425]
	rm := e.Journal.Entries[426]
	if decodeEnvironmentJSON(ex.Receipt, &export) != nil || !environmentPreflightDigest.MatchString(export.Certificate) || export.Reader != cj.Owners[id] || !environmentBootstrapOwnerUUID.MatchString(export.Claim) || !environmentBootstrapOwnerUUID.MatchString(export.Pod) || !environmentBootstrapOwnerUUID.MatchString(export.ReplicaSet) || export.Server != "account-manager."+ns+".svc.cluster.local" || decodeEnvironmentJSON(rm.Receipt, &absent) != nil || absent.ID != id || absent.UID != cj.Owners[id] || !absent.Absent || rm.IntentSHA256 != activeTransportJournalDigest(deploymentplan.Object{"resource_id": id, "uid": cj.Owners[id]}) {
		return bad()
	}
	exportIntent := deploymentplan.Object{"owner": "account-manager", "namespace": ns, "claim_uid": export.Claim, "reader_uid": export.Reader, "pod_uid": export.Pod, "replicaset_uid": export.ReplicaSet, "server_name": export.Server, "root_sha256": r.Ceremony.Root.RootFingerprint}
	if ex.IntentSHA256 != activeTransportJournalDigest(exportIntent) {
		return bad()
	}
	// Exactly two accepted Kubernetes children extend the old cleanup lease
	// history. Their immutable descriptors must match the recorded leases.
	base := r.FinalizationAuthority.SourceLeases
	if len(e.Journal.KubernetesLeases) != base+2 && !(managedLateNormalTailSelected(e.Journal) && (len(e.Journal.KubernetesLeases) == base+5 || managedLateNormalTailControllerSelected(e.Journal) && len(e.Journal.KubernetesLeases) == base+6)) {
		return bad()
	}
	for i, plan := range []*deploymentplan.Plan{created, retired} {
		lease := e.Journal.KubernetesLeases[base+i]
		if !managedLateLeaseMatches(lease, e.Journal.Stack, plan.SHA256()) || i == 1 && lease.UID == e.Journal.KubernetesLeases[base].UID {
			return bad()
		}
	}
	return nil
}

func verifyManagedLateCompletedTailSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r != nil && r.normalTail != nil {
		return verifyManagedLateNormalTailSource(ctx, cfg, store, e, r)
	}
	if r == nil || r.completedTail == nil || cfg.Environment != store.Environment {
		return createOpenBaoACKBlock("completed-normal-tail-current-source")
	}
	return verifyManagedLateCompletedTailFrame(ctx, store, e, r.completedTail)
}

func verifyManagedLateCompletedTailFrame(ctx context.Context, store secretStore, e *environmentCreateExecution, t *createManagedLateCompletedTail) error {
	if t != nil && t.normal != nil {
		return verifyManagedLateNormalTailFrame(ctx, store, e, t, t.normal)
	}
	if t == nil || e == nil || e.Check(ctx) != nil || store != e.Store || deploymentplan.Digest(e.owner) != t.OwnerSHA || deploymentplan.Digest(e.Journal) != t.SourceSHA || !managedLateCompletedTailSelected(e.Journal) || !time.Now().Before(t.OverallDeadline) {
		return createOpenBaoACKBlock("completed-normal-tail-current-source")
	}
	path := filepath.Join(store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256))
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || environmentPreflightBytesSHA256(raw) != t.ProofSHA {
		return createOpenBaoACKBlock("completed-normal-tail-completed-proof")
	}
	return validateManagedLateTerminalFinalizerCompletion(store, e, &createManagedLateSessionRecovery{Review: t.LateReview})
}

func verifyManagedLateCompletedTailCurrent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if err := verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	if err := verifyManagedLateTerminalFinalizerNative(ctx, cfg, store, e, b, r, p); err != nil {
		return err
	}
	p.Schema = managedLateExpiredRenewalSchema
	p.Materials[managedLateRemainingMaterial+"completed-normal-tail"] = managedLateCompletedTailDigest(r.completedTail)
	if r.normalTail != nil {
		p.Schema = managedLateNormalTailSchema
		p.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(r.normalTail)
		p.Materials[managedLateNormalTailMaterial+"policy"] = managedLateNormalTailPolicySHA()
		if r.normalTail.Authority != nil {
			p.Materials[managedLateNormalTailMaterial+"first-authority"] = deploymentplan.Digest(r.normalTail.Authority)
		}
	}
	if r.normalSupplement != nil {
		if r.normalTail == nil || r.normalSupplement.CredentialSHA != managedLateNormalTailCredentialSHA(r.normalTail) || r.normalTail.NativeEvidence["current-credential"] != r.normalSupplement.CredentialSHA || !r.normalSupplement.CredentialExpires.Equal(r.normalTail.CredentialExpires) {
			return createOpenBaoACKBlock("normal-tail-supplement-current-credential")
		}
		p.Materials[managedLateNormalTailSupplementMaterial+"intent"] = managedLateNormalTailSupplementIntent(r)
		p.Materials[managedLateNormalTailSupplementMaterial+"policy"] = managedLateNormalTailSupplementPolicySHA()
		if x := r.normalSupplement.Extension; x != nil {
			if x.CredentialSHA != managedLateNormalTailCredentialSHA(r.normalTail) || r.normalTail.NativeEvidence["current-credential"] != x.CredentialSHA || !x.CredentialExpires.Equal(r.normalTail.CredentialExpires) {
				return createOpenBaoACKBlock("normal-tail-extension-current-credential")
			}
			p.Materials[managedLateNormalTailExtensionMaterial+"intent"] = managedLateNormalTailExtensionIntent(r)
			p.Materials[managedLateNormalTailExtensionMaterial+"policy"] = managedLateNormalTailExtensionPolicySHA()
		}
	}
	if err := qualifyManagedLateRedisDurable(ctx, cfg, store, e, b, r, p); err != nil {
		return err
	}
	return nil
}

func managedLateCompletedTailDigest(t *createManagedLateCompletedTail) string {
	if t == nil {
		return ""
	}
	c := *t
	c.view = nil
	return deploymentplan.Digest(c)
}

func prepareManagedLateCompletedTail(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	r, err := loadManagedLateCompletedTailSource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	return r, verifyManagedLateCompletedTailCurrent(ctx, cfg, store, e, b, r, p)
}

func managedLateCompletedTailContext(ctx context.Context, runtime environmentCreateBootstrapRuntime) (context.Context, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r != nil && r.normalTail != nil {
		return managedLateNormalTailContext(ctx, runtime)
	}
	if r == nil || r.completedTail == nil || e == nil || e.Check(ctx) != nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.ResidualRemainingAuthority == nil || deploymentplan.Digest(r.ManagedLateRemaining.ResidualRemainingAuthority) != r.completedTail.AuthoritySHA || !time.Now().Before(r.completedTail.OverallDeadline) {
		return nil, createOpenBaoACKBlock("completed-normal-tail-overall")
	}
	if validateManagedLateTerminalFinalizerCompletion(e.Store, e, r.ManagedLateSession) != nil {
		return nil, createOpenBaoACKBlock("completed-normal-tail-durable-proof")
	}
	bounded, cancel := context.WithDeadline(ctx, r.completedTail.OverallDeadline)
	context.AfterFunc(ctx, cancel)
	if err := e.Check(bounded); err != nil {
		return nil, err
	}
	return bounded, nil
}

func loadManagedLateCompletedTailQualified(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string) (*createServiceSessionRecovery, error) {
	var a createFoundationRecoveryArtifact
	schema := managedLateExpiredRenewalSchema
	if managedLateNormalTailSelected(e.Journal) {
		schema = managedLateNormalTailSchema
	}
	if p == nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), &a) != nil || !managedLateQualifiedArtifactValid(a, e, *p, review, time.Now().UTC(), schema) {
		return nil, createOpenBaoACKBlock("completed-normal-tail-qualified-source")
	}
	r, err := loadManagedLateCompletedTailSource(ctx, cfg, store, e)
	if err != nil {
		return nil, err
	}
	if r.normalTail != nil {
		var archived createManagedLateNormalTail
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateNormalTailFilename), &archived) != nil || archived.Review != review {
			return nil, createOpenBaoACKBlock("normal-tail-qualified-descriptor")
		}
		current := *r.normalTail
		current.NativeEvidence = archived.NativeEvidence
		if managedLateNormalTailDigest(&current) != managedLateNormalTailDigest(&archived) || a.Plan.Materials[managedLateNormalTailMaterial+"source"] != managedLateNormalTailDigest(&archived) || a.Plan.Materials[managedLateNormalTailMaterial+"policy"] != managedLateNormalTailPolicySHA() {
			return nil, createOpenBaoACKBlock("normal-tail-qualified-pins")
		}
		archived.Authority = r.normalTail.Authority
		archived.Supplement = r.normalSupplement
		r.normalTail = &archived
		r.completedTail.normal = &archived
	}
	r.completedTail.NativeEvidence = map[string]string{}
	prefix := managedLateRemainingMaterial + "completed-normal-tail-native-"
	for key, value := range a.Plan.Materials {
		if strings.HasPrefix(key, prefix) {
			r.completedTail.NativeEvidence[strings.TrimPrefix(key, prefix)] = value
		}
	}
	if !managedLateResidualCleanupEvidenceValid(r.completedTail.NativeEvidence) || a.Plan.Materials[managedLateRemainingMaterial+"completed-normal-tail"] != managedLateCompletedTailDigest(r.completedTail) {
		return nil, createOpenBaoACKBlock("completed-normal-tail-qualified-evidence")
	}
	if r.normalSupplement != nil {
		var saved createManagedLateNormalTailSupplement
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateNormalTailSupplementFilename), &saved) != nil || deploymentplan.Digest(saved) != managedLateNormalTailSupplementIntent(r) || a.Plan.Materials[managedLateNormalTailSupplementMaterial+"intent"] != managedLateNormalTailSupplementIntent(r) || a.Plan.Materials[managedLateNormalTailSupplementMaterial+"policy"] != managedLateNormalTailSupplementPolicySHA() {
			return nil, createOpenBaoACKBlock("normal-tail-qualified-supplement-intent")
		}
		if r.normalSupplement.Extension != nil {
			var extension createManagedLateNormalTailExtension
			if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateNormalTailExtensionFilename), &extension) != nil || deploymentplan.Digest(extension) != managedLateNormalTailExtensionIntent(r) || a.Plan.Materials[managedLateNormalTailExtensionMaterial+"intent"] != managedLateNormalTailExtensionIntent(r) || a.Plan.Materials[managedLateNormalTailExtensionMaterial+"policy"] != managedLateNormalTailExtensionPolicySHA() {
				return nil, createOpenBaoACKBlock("normal-tail-qualified-extension-intent")
			}
		}
	}
	if a.Plan.Materials[managedLateRedisDurableMaterial+"intent"] != "" {
		sourceSHA, sourceErr := managedLateRedisPrePauseSourceSHA(store, e)
		if sourceErr != nil {
			return nil, sourceErr
		}
		var redis createManagedLateRedisDurable
		if r.normalTail == nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLateRedisDurableFilename), &redis) != nil || redis.QualifiedReview != review || redis.Schema != managedLateRedisDurableSchema || redis.SourceSHA != sourceSHA || redis.OwnerSHA != deploymentplan.Digest(e.owner) || redis.ParentSHA != e.Journal.PlanSHA256 || redis.NormalAuthoritySHA != deploymentplan.Digest(r.normalTail.Authority) || redis.SupplementIntentSHA != managedLateNormalTailSupplementIntent(r) || redis.ExtensionIntentSHA != managedLateNormalTailExtensionIntent(r) || a.Plan.Materials[managedLateRedisDurableMaterial+"intent"] != managedLateRedisDurableDigest(&redis) || a.Plan.Materials[managedLateRedisDurableMaterial+"original-volumes"] != redis.OriginalVolumesSHA {
			return nil, managedLateRedisDurableError("qualified-archive")
		}
		if managedLateRedisPrePauseSelected(e.Journal) {
			resume, saved, err := readManagedLateRedisPrePauseSource(store, e)
			if err != nil || deploymentplan.Digest(redis.Resume) != deploymentplan.Digest(resume) || a.Plan.Materials[managedLateRedisDurableMaterial+"resume"] != deploymentplan.Digest(resume) {
				return nil, managedLateRedisDurableError("qualified-resume")
			}
			redis.source = saved.Source
		} else {
			if redis.Resume != nil {
				return nil, managedLateRedisDurableError("unexpected-qualified-resume")
			}
			redis.source = e.Journal
		}
		r.redisDurable = &redis
	}
	*p = a.Plan
	return r, nil
}

func admitManagedLateCompletedTail(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, a createFoundationRecoveryArtifact, bytesSHA string) error {
	if err := verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	review := deploymentplan.Digest(a.Plan)
	schema := managedLateExpiredRenewalSchema
	if r.normalTail != nil {
		schema = managedLateNormalTailSchema
	}
	if !managedLateQualifiedArtifactValid(a, e, a.Plan, review, time.Now().UTC(), schema) || a.Plan.Materials[managedLateRemainingMaterial+"completed-normal-tail"] != managedLateCompletedTailDigest(r.completedTail) {
		return createOpenBaoACKBlock("completed-normal-tail-fresh-six-gates")
	}
	actual, err := readManagedLateQualificationFingerprint(store, a)
	checker, checkerErr := environmentPreflightCheckerSHA256()
	if err != nil || actual != bytesSHA || checkerErr != nil || checker != a.Plan.CheckerSHA {
		return createOpenBaoACKBlock("completed-normal-tail-qualified-bytes-checker")
	}
	if r.redisDurable != nil {
		d := r.redisDurable
		sourceSHA, sourceErr := managedLateRedisPrePauseSourceSHA(store, e)
		if sourceErr != nil || d.QualifiedReview != review || a.Plan.Materials[managedLateRedisDurableMaterial+"intent"] != managedLateRedisDurableDigest(d) || d.SourceSHA != sourceSHA {
			return managedLateRedisDurableError("admitted-qualified-intent")
		}
		if managedLateRedisPrePauseSelected(e.Journal) {
			resume, _, err := readManagedLateRedisPrePauseSource(store, e)
			if err != nil || deploymentplan.Digest(d.Resume) != deploymentplan.Digest(resume) || a.Plan.Materials[managedLateRedisDurableMaterial+"resume"] != deploymentplan.Digest(resume) {
				return managedLateRedisDurableError("admitted-resume")
			}
		}
		d.admitted = true
		d.artifactSHA, d.qualifiedBytesSHA = deploymentplan.Digest(a), bytesSHA
	}
	return e.Check(ctx)
}
