package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is a fresh qualification of an existing, fully applied helper. It does
// not alter the original migration descriptor, source434 or admission clocks.
type managedLateRedisPrePause struct {
	CurrentSHA, SourceSHA, SourceRawSHA, VolumeRawSHA string
	ChildPlanSHA, ChildJournalSHA, HelperUID          string
}

func managedLateRedisPrePauseSelected(j environmentCreateJournal) bool {
	if len(j.Entries) != 437 || len(j.KubernetesLeases) != 151 || !j.CompletedAt.IsZero() {
		return false
	}
	prefix := managedLateRedisPrePauseOriginal(j)
	if !managedLateNormalTailControllerSelected(prefix) || prefix.Entries[431].State != "completed" {
		return false
	}
	keys := []string{managedLateRedisDurableKey, "kubernetes/normal-redis-durable-claim", "kubernetes/normal-redis-durable-target"}
	for i, entry := range j.Entries[434:] {
		phase := "redis-durable"
		if i > 0 {
			phase = keys[i][11:]
		}
		if entry.Key != keys[i] || entry.Phase != phase || entry.StartedAt.IsZero() || entry.StartedAt.After(time.Now().UTC()) || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return false
		}
		if i == 1 {
			if entry.State != "completed" || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC()) || len(entry.Receipt) == 0 || entry.StartedAt.Before(j.Entries[434].StartedAt) {
				return false
			}
		} else if i == 2 && entry.State == "completed" {
			if entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC()) || len(entry.Receipt) == 0 {
				return false
			}
		} else if entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
			return false
		}
	}
	return !j.Entries[436].StartedAt.Before(j.Entries[435].CompletedAt)
}

func managedLateRedisPrePauseOriginal(j environmentCreateJournal) environmentCreateJournal {
	if len(j.Entries) < 434 || len(j.KubernetesLeases) < 149 {
		return environmentCreateJournal{}
	}
	j.Entries = slices.Clone(j.Entries[:434])
	j.KubernetesLeases = slices.Clone(j.KubernetesLeases[:149])
	return j
}

func managedLateRedisPrePauseSourceSHA(store secretStore, e *environmentCreateExecution) (string, error) {
	if e == nil {
		return "", managedLateRedisDurableError("pre-pause-source")
	}
	if !managedLateRedisPrePauseSelected(e.Journal) {
		return deploymentplan.Digest(e.Journal), nil
	}
	resume, _, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil {
		return "", err
	}
	return resume.SourceSHA, nil
}

// Only this exact pre-pause state may borrow the original434 parsing ancestry.
// A resource write with an uncertain result, a different failed acceptance, any
// snapshot/cutover evidence, or a missing recorded owner remains closed.
func readManagedLateRedisPrePauseSource(store secretStore, e *environmentCreateExecution) (*managedLateRedisPrePause, *managedLateRedisDurableSaved, error) {
	bad := func() (*managedLateRedisPrePause, *managedLateRedisDurableSaved, error) {
		return nil, nil, managedLateRedisDurableError("pre-pause-source")
	}
	if e == nil || e.Store != store || !managedLateRedisPrePauseSelected(e.Journal) {
		return bad()
	}
	base := managedLateRedisDurableBase(e.Journal.PlanSHA256)
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, "source.json"), true)
	var saved managedLateRedisDurableSaved
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Join(store.Root, base)) != nil || decodeEnvironmentJSON(raw, &saved) != nil {
		return bad()
	}
	d := &saved.Descriptor
	if d.Resume != nil || d.Schema != managedLateRedisDurableSchema || d.ParentSHA != e.Journal.PlanSHA256 || d.OwnerSHA != deploymentplan.Digest(e.owner) || d.SourceLeases != 149 || d.SourceSHA != deploymentplan.Digest(saved.Source) || d.SourceSHA != deploymentplan.Digest(managedLateRedisPrePauseOriginal(e.Journal)) || d.OriginalVolumesSHA != deploymentplan.Digest(d.OriginalVolumes) || len(d.OriginalVolumes) != 18 {
		return bad()
	}
	var qualification createFoundationRecoveryArtifact
	qraw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", d.QualifiedReview, "qualification.json"), true)
	if err != nil || decodeEnvironmentJSON(qraw, &qualification) != nil || environmentPreflightBytesSHA256(qraw) != saved.QualifiedBytesSHA || deploymentplan.Digest(qualification) != saved.QualificationSHA || deploymentplan.Digest(qualification.Plan) != d.QualifiedReview || qualification.Plan.ParentJournalSHA != d.SourceSHA || qualification.Plan.ParentOwnerSHA != d.OwnerSHA || qualification.Plan.Materials[managedLateRedisDurableMaterial+"intent"] != managedLateRedisDurableDigest(d) {
		return bad()
	}
	normalRaw, err := readManagedLateNormalTailPair(store, d.ParentSHA)
	var normal createManagedLateNormalTailAuthority
	if err != nil || decodeEnvironmentJSON(normalRaw, &normal) != nil || deploymentplan.Digest(normal) != d.NormalAuthoritySHA {
		return bad()
	}
	var first createManagedLateNormalTail
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", normal.Review, managedLateNormalTailFilename), &first) != nil {
		return bad()
	}
	if authority, err := readManagedLateNormalTailAuthority(store, &first); err != nil || authority == nil || deploymentplan.Digest(authority) != d.NormalAuthoritySHA {
		return bad()
	}
	supplement, err := readManagedLateNormalTailSupplementAuthority(store, d.ParentSHA)
	if err != nil || supplement == nil || deploymentplan.Digest(supplement) != saved.SupplementAuthoritySHA || supplement.IntentSHA != d.SupplementIntentSHA || supplement.SourceSHA != d.SourceSHA || supplement.OwnerSHA != d.OwnerSHA || supplement.OriginalAuthoritySHA != d.NormalAuthoritySHA {
		return bad()
	}
	extension, err := readManagedLateNormalTailExtensionAuthority(store, d.ParentSHA)
	if err != nil || extension == nil || deploymentplan.Digest(extension) != saved.ExtensionAuthoritySHA || extension.IntentSHA != d.ExtensionIntentSHA || extension.SourceSHA != d.SourceSHA || extension.OwnerSHA != d.OwnerSHA || extension.OriginalAuthoritySHA != d.NormalAuthoritySHA || extension.BaseAuthoritySHA != saved.SupplementAuthoritySHA {
		return bad()
	}
	outer := e.Journal.Entries[434]
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"descriptor_sha256": managedLateRedisDurableDigest(d), "review_sha256": d.QualifiedReview, "authority_sha256": d.NormalAuthoritySHA})
	if outer.IntentSHA256 != activeTransportJournalDigest(jsonRawObject(intent)) {
		return bad()
	}
	for _, entry := range e.Journal.Entries[434:] {
		if entry.StartedAt.Before(extension.AdmittedAt) || !entry.StartedAt.Before(extension.Deadline) || !entry.CompletedAt.IsZero() && entry.CompletedAt.After(extension.Deadline) {
			return bad()
		}
	}
	claim, cj, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[435].Key)
	if err != nil || len(claim.Resources) != 1 || claim.Resources[0].ID != deploymentplan.ID(d.Claim) || claim.Resources[0].Action != "create" || claim.Resources[0].UID != "" || claim.Resources[0].Baseline != nil || !deploymentplan.Matches(d.Claim, claim.Resources[0].Desired) || !managedLateLeaseMatches(e.Journal.KubernetesLeases[149], e.Journal.Stack, claim.SHA256()) {
		return bad()
	}
	claimExpected, compileErr := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: qualification.Plan.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(qualification.Plan.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("normal-redis-durable-claim", []deploymentplan.Definition{{Object: d.Claim, Phase: "storage"}})}, []deploymentplan.Definition{{Object: d.Claim, Phase: "storage"}}, nil)
	if compileErr != nil || claimExpected.SHA256() != claim.SHA256() {
		return bad()
	}
	volumeRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, "volume.json"), true)
	var volume environmentCreateVolumeOwner
	if err != nil || decodeEnvironmentJSON(volumeRaw, &volume) != nil || volume.PVCUID != cj.Owners[claim.Resources[0].ID] || volume.PVCName != "redis-data" || volume.Namespace != e.Journal.Stack+"-platform" || !environmentBootstrapOwnerUUID.MatchString(volume.PVUID) || volume.PVName == "" {
		return bad()
	}
	if _, err := positiveIntValue("volume_id", volume.NativeID); err != nil {
		return bad()
	}
	lease := e.Journal.KubernetesLeases[150]
	planSHA, _, _ := strings.Cut(lease.Holder, ":")
	// The recorded parent intent is independently rederived from the child plan.
	// The lease holder provides its exact plan digest; no directory scan occurs.
	var child deploymentplan.Plan
	var journal deploymentplan.Journal
	childRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/rollback", planSHA, "journal.json"), true)
	if !environmentPreflightDigest.MatchString(planSHA) || !managedLateLeaseMatches(lease, e.Journal.Stack, planSHA) || err != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", planSHA, "plan.json"), &child) != nil || decodeEnvironmentJSON(childRaw, &journal) != nil || child.SHA256() != planSHA || child.Operation != "scoped" || child.Environment != e.Journal.Environment || child.Stack != e.Journal.Stack || len(child.Resources) != 1 {
		return bad()
	}
	resource := child.Resources[0]
	childExpected, compileErr := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: qualification.Plan.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(qualification.Plan.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("normal-redis-durable-target", []deploymentplan.Definition{{Object: d.Helper, Phase: "workload"}})}, []deploymentplan.Definition{{Object: d.Helper, Phase: "workload"}}, nil)
	if compileErr != nil || childExpected.SHA256() != child.SHA256() {
		return bad()
	}
	childIntent, err := environmentCreateKubernetesStageIntent(&child)
	if err != nil || e.Journal.Entries[436].IntentSHA256 != activeTransportJournalDigest(jsonRawObject(childIntent)) || resource.ID != deploymentplan.ID(d.Helper) || resource.Action != "create" || resource.UID != "" || resource.Baseline != nil || !deploymentplan.Matches(d.Helper, resource.Desired) || journal.PlanSHA256 != planSHA || journal.InFlight != "" || !journal.AcceptanceStarted || len(journal.Completed) != 1 || journal.Completed[0] != resource.ID || len(journal.Owners) != 1 || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) || journal.Accepted && journal.Failed != "" || !journal.Accepted && journal.Failed != "deployment.post-deploy.workloads" || lease.UID == e.Journal.KubernetesLeases[149].UID {
		return bad()
	}
	if journal.Accepted && deploymentplan.VerifyAppliedPodAcceptanceReceipt(store.Root, planSHA, journal) != nil {
		return bad()
	}
	if e.Journal.Entries[436].State == "completed" {
		accepted, acceptedJournal, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[436].Key)
		if err != nil || !journal.Accepted || accepted.SHA256() != planSHA || deploymentplan.Digest(acceptedJournal) != deploymentplan.Digest(journal) {
			return bad()
		}
	}
	for _, name := range []string{"source.rdb", "completed.json", "bindings.json"} {
		if _, err := os.Lstat(filepath.Join(store.Root, base, name)); !errors.Is(err, os.ErrNotExist) {
			return bad()
		}
	}
	return &managedLateRedisPrePause{CurrentSHA: deploymentplan.Digest(e.Journal), SourceSHA: d.SourceSHA, SourceRawSHA: environmentPreflightBytesSHA256(raw), VolumeRawSHA: environmentPreflightBytesSHA256(volumeRaw), ChildPlanSHA: planSHA, ChildJournalSHA: environmentPreflightBytesSHA256(childRaw), HelperUID: journal.Owners[resource.ID]}, &saved, nil
}

func verifyManagedLateRedisPrePause(ctx context.Context, store secretStore, e *environmentCreateExecution) error {
	if e == nil || e.Check(ctx) != nil {
		return managedLateRedisDurableError("pre-pause-fence")
	}
	_, _, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil {
		return err
	}
	return e.Check(ctx)
}

func qualifyManagedLateRedisPrePause(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	resume, saved, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil {
		return err
	}
	d := saved.Descriptor
	d.source = saved.Source
	d.Resume = resume
	if prior := r.redisDurable; prior != nil {
		if managedLateRedisDurableDigest(prior) != managedLateRedisDurableDigest(&d) || deploymentplan.Digest(prior.Resume) != deploymentplan.Digest(resume) {
			return managedLateRedisDurableError("resume-qualified-drift")
		}
		d.QualifiedReview, d.admitted, d.artifactSHA, d.qualifiedBytesSHA = prior.QualifiedReview, prior.admitted, prior.artifactSHA, prior.qualifiedBytesSHA
	}
	r.redisDurable = &d
	current, err := b.Read(ctx, d.ResourceID)
	if err != nil || current == nil || deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) != d.UID || !deploymentplan.Matches(d.Original.Desired, current) {
		return managedLateRedisDurableError("resume-original-writer")
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	pod, rs, err := managedLateRedisSourcePod(current, objects)
	if err != nil || rs != d.ReplicaSetUID || deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]) != d.PodUID {
		return managedLateRedisDurableError("resume-source-pod")
	}
	volume, err := managedLateRedisVolume(ctx, b, &d, readPrePauseVolumeUID(saved, e))
	var recorded environmentCreateVolumeOwner
	if err != nil || createOpenBaoACKCompletionRead(store, filepath.Join(managedLateRedisDurableBase(d.ParentSHA), "volume.json"), &recorded) != nil || volume != recorded {
		return managedLateRedisDurableError("resume-native-volume")
	}
	d.Volume = &recorded
	if err := managedLateRedisVerifyOriginalVolumes(ctx, b, d.OriginalVolumes); err != nil {
		return err
	}
	var child deploymentplan.Plan
	if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", resume.ChildPlanSHA, "plan.json"), &child) != nil {
		return managedLateRedisDurableError("resume-child-plan")
	}
	if err := child.Verify(ctx, b); err != nil {
		return err
	}
	if err := verifyEnvironmentTargetWorkloads(ctx, &child, environmentCreateStageInventory(&child, objects), func(ctx context.Context, image string) (map[string]bool, error) {
		return environmentRuntimeImageDigests(ctx, store, image)
	}, map[string]string{deploymentplan.ID(d.Helper): resume.HelperUID}); err != nil {
		return err
	}
	release, err := effectiveCreateOpenBaoACKRelease(ctx, cfg, store, e.Journal.PlanSHA256, p.Plan.Release, e)
	if err != nil {
		return err
	}
	bindings, err := observeEnvironmentBindings(cfg.Environment, release, objects)
	if err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: b, ServiceSessionRecovery: r}
	if err := applyManagedLateBootstrapBindings(ctx, cfg, runtime, &bindings, objects); err != nil {
		return err
	}
	if _, err := readManagedLateRedisPrePauseBindings(ctx, store, e, bindings); err != nil {
		return err
	}
	state, err := readManagedLateRedisNative(ctx, managedLateRedisRunner(cfg, store, e, b, r, &d, resume.HelperUID), "source")
	if err != nil || state.AOF != 0 || state.RDBChanges != 0 {
		return managedLateRedisDurableError("resume-unpaused-source")
	}
	p.Materials[managedLateRedisDurableMaterial+"intent"] = managedLateRedisDurableDigest(&d)
	p.Materials[managedLateRedisDurableMaterial+"original-volumes"] = d.OriginalVolumesSHA
	p.Materials[managedLateRedisDurableMaterial+"resume"] = deploymentplan.Digest(resume)
	return e.Check(ctx)
}

func readPrePauseVolumeUID(saved *managedLateRedisDurableSaved, e *environmentCreateExecution) string {
	if saved == nil || e == nil {
		return ""
	}
	_, j, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/normal-redis-durable-claim")
	if err != nil {
		return ""
	}
	return j.Owners[deploymentplan.ID(saved.Descriptor.Claim)]
}

func resumeManagedLateRedisHelperAcceptance(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, a createFoundationRecoveryArtifact) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.redisDurable == nil || r.redisDurable.Resume == nil {
		return managedLateRedisDurableError("resume-profile")
	}
	d := r.redisDurable
	resume, _, err := readManagedLateRedisPrePauseSource(store, e)
	if err != nil || deploymentplan.Digest(resume) != deploymentplan.Digest(d.Resume) || a.Plan.Materials[managedLateRedisDurableMaterial+"resume"] != deploymentplan.Digest(resume) || a.Plan.ParentJournalSHA != deploymentplan.Digest(e.Journal) {
		return managedLateRedisDurableError("resume-fresh-qualified-source")
	}
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", resume.ChildPlanSHA, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", resume.ChildPlanSHA, "journal.json"), &journal) != nil {
		return managedLateRedisDurableError("resume-applied-child")
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: e, cfg: cfg, store: store, stagePlanSHA256: plan.SHA256()}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := managedLateRedisRuntime(ctx, cfg, store, e, r, d); err != nil {
			return err
		}
		fresh, err := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot)
		_, operator, readErr := readEnvironmentCreateOperator(cfg)
		if err != nil || readErr != nil || deploymentplan.Digest(fresh) != a.Plan.Plan.ConfigurationSHA256 || deploymentplan.Digest(operator) != a.Plan.Plan.OperatorSHA256 {
			return managedLateRedisDurableError("resume-current-inputs")
		}
		return backend.CheckParentExecution(ctx, p, store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan, owners map[string]string) error {
		if err := fence(ctx, p); err != nil {
			return err
		}
		objects, err := runtime.Backend.Inventory(ctx, p.Stack)
		if err != nil {
			return err
		}
		return verifyEnvironmentTargetWorkloads(ctx, p, environmentCreateStageInventory(p, objects), func(ctx context.Context, image string) (map[string]bool, error) {
			return environmentRuntimeImageDigests(ctx, store, image)
		}, owners)
	}
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return err
		}
	}
	// The failed child journal and new acceptance-attempt receipt are manifest
	// inputs. Discard the old capability before either can be published.
	invalidateManagedLateSourceReuse(e)
	if !journal.Accepted {
		journal, err = plan.ResumeAppliedPodAcceptance(ctx, backend, plan.SHA256(), resume.ChildJournalSHA, store.Root, fence, accept)
		if err != nil {
			return err
		}
	} else {
		if err := accept(ctx, &plan, journal.Owners); err != nil {
			return err
		}
		if err := plan.Verify(ctx, backend); err != nil {
			return err
		}
	}
	if err := fence(ctx, &plan); err != nil {
		return err
	}
	if !journal.Accepted || journal.Failed != "" || journal.InFlight != "" || journal.Owners[deploymentplan.ID(d.Helper)] != resume.HelperUID {
		return managedLateRedisDurableError("resume-genuine-acceptance")
	}
	if !time.Now().Before(managedLateNormalTailEffectiveDeadline(r)) {
		return managedLateRedisDurableError("resume-original-deadline")
	}
	if e.Journal.Entries[436].State == "completed" {
		return nil
	}
	if err := e.RecordReceipt("kubernetes/normal-redis-durable-target", mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": plan.SHA256(), "owners": journal.Owners})); err != nil {
		return err
	}
	if err := e.CompleteEntry("kubernetes/normal-redis-durable-target", time.Now().UTC()); err != nil {
		return err
	}
	// Publication ended the pending437 parsing phase. Derive a new immutable
	// scope for the genuine completed437 before the first source INFO or pause.
	fresh, err := loadManagedLateCompletedTailSource(ctx, cfg, store, e)
	if err != nil {
		return err
	}
	if fresh.completedTail == nil || fresh.normalTail == nil || fresh.normalTail.Authority == nil || deploymentplan.Digest(fresh.normalTail.Authority) != deploymentplan.Digest(r.normalTail.Authority) {
		return managedLateRedisDurableError("resume-completed-source")
	}
	r.normalTail.CurrentSHA = deploymentplan.Digest(e.Journal)
	fresh.completedTail.NativeEvidence = r.completedTail.NativeEvidence
	fresh.completedTail.normal = r.normalTail
	r.completedTail = fresh.completedTail
	r.ManagedLateRemaining.completedTail = r.completedTail
	return e.Check(ctx)
}
