package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Completed ceremonies are history. This handoff authenticates their durable
// evidence once; ordinary target execution keeps its live parent fence and
// fresh native checks without reopening an old bootstrap operation or clock.
type createNormalTailHandoff struct {
	OriginalPlan    *environmentCreatePlan
	Release         environmentRelease
	Bindings        environmentIdentityBindings
	Receipts        map[string]environmentCreateProviderReceipt
	Source          environmentCreateJournal
	OriginalVolumes map[string]environmentCreateVolumeOwner
	RedisVolume     environmentCreateVolumeOwner
	Pins            map[string]string
	Execution       *environmentCreateExecution `json:"-"`
}

type createNormalTailHandoffKey struct{}

func withCreateNormalTailHandoff(ctx context.Context, handoff *createNormalTailHandoff) context.Context {
	return context.WithValue(ctx, createNormalTailHandoffKey{}, handoff)
}

func createNormalTailHandoffFromContext(ctx context.Context) (*createNormalTailHandoff, bool) {
	h, ok := ctx.Value(createNormalTailHandoffKey{}).(*createNormalTailHandoff)
	return h, ok && h != nil && h.Execution != nil
}

// Acquire only the original process lock. No historical checkpoint selector or
// expired ceremony deadline participates in loading an ordinary continuation.
func loadCreateNormalTailExecution(ctx context.Context, cfg deploymentConfig, store secretStore, parent string) (*environmentCreateExecution, error) {
	bad := func() (*environmentCreateExecution, error) {
		return nil, createProviderRecoveryBlock("original completed-bootstrap execution owner is unavailable")
	}
	if ctx.Err() != nil || cfg.Environment != store.Environment || cfg.Environment != "dev" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	if validateEnvironmentPreflightDirectories(store, fence) != nil {
		return bad()
	}
	lock, err := deploymentplan.LockExecutionOwner(fence, false)
	if err != nil {
		return bad()
	}
	e := &environmentCreateExecution{Store: store, running: lock}
	loaded := false
	defer func() {
		if !loaded {
			_ = e.Close()
		}
	}()
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		return bad()
	}
	e.runningInfo, err = lock.Stat()
	if err != nil || readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &e.owner) != nil || e.owner.Schema != environmentCreateOwnerSchema || e.owner.Environment != cfg.Environment || e.owner.Stack != cfg.Values["CLOUD_STACK_NAME"] || e.owner.PlanSHA256 != parent || e.owner.OwnerToken == "" || !strings.HasPrefix(e.owner.Journal, "deployment/create/executions/"+parent+"/execution-") || filepath.Base(e.owner.Journal) != "journal.json" {
		return bad()
	}
	e.JournalPath, err = privateDeploymentStatePath(cfg.Environment, e.owner.Journal)
	if err != nil || e.JournalPath != filepath.Join(store.Root, e.owner.Journal) || validateEnvironmentPreflightDirectories(store, filepath.Dir(e.JournalPath)) != nil {
		return bad()
	}
	e.Directory = filepath.Dir(e.JournalPath)
	if readEnvironmentPreflightJSON(e.JournalPath, &e.Journal) != nil || e.Journal.PlanSHA256 != parent || e.Check(ctx) != nil {
		return bad()
	}
	loaded = true
	return e, nil
}

func readCreateNormalTailHandoff(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createNormalTailHandoff, error) {
	bad := func(stage string) (*createNormalTailHandoff, error) {
		return nil, createProviderRecoveryBlock("completed bootstrap handoff: " + stage)
	}
	if e == nil || store != e.Store || e.Check(ctx) != nil {
		return bad("actual owner")
	}
	base := managedLateRedisDurableBase(e.Journal.PlanSHA256)
	var saved managedLateRedisDurableSaved
	var completed managedLateRedisDurableReceipt
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "source.json"), &saved) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "completed.json"), &completed) != nil {
		return bad("recorded migration")
	}
	original, redis, err := readManagedLateRedisDurableVolumeAuthority(store, e)
	if err != nil {
		return nil, err
	}
	if redis == nil || completed.Volume != *redis || completed.DeploymentUID != saved.Descriptor.UID || !environmentBootstrapOwnerUUID.MatchString(completed.PodUID) || completed.Snapshot.SHA != completed.RDBSHA || completed.RDBSHA == "" {
		return bad("migration identity")
	}
	if len(completed.TargetEvidence) != 4 || completed.TargetEvidence["rdb"] != completed.RDBSHA || completed.TargetEvidence["dataset"] != managedLateRedisNativeStateSHA(completed.Snapshot.State) || !environmentPreflightDigest.MatchString(completed.TargetEvidence["aof-manifest"]) || !environmentPreflightDigest.MatchString(completed.TargetEvidence["aof-files"]) {
		return bad("durable AOF proof")
	}
	if _, err := readManagedLateRedisArchive(store, e.Journal.PlanSHA256, completed.RDBSHA); err != nil {
		return nil, err
	}
	// Every Kubernetes migration effect must have an accepted original child,
	// not merely a nonempty outer receipt or an object found in live inventory.
	for _, stage := range []string{"normal-redis-durable-claim", "normal-redis-durable-target", "normal-redis-durable-retire", "normal-redis-durable-stop", "normal-redis-durable-switch"} {
		child, _, err := managedTransportPostAcceptedStage(store, e, "kubernetes/"+stage)
		if err != nil || len(child.Resources) != 1 {
			return bad("accepted " + stage)
		}
	}
	if err := validateCreateNormalTailRedisChildren(store, e, &saved.Descriptor, &completed); err != nil {
		return nil, err
	}
	var artifact createFoundationRecoveryArtifact
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", saved.Descriptor.QualifiedReview, "qualification.json"), &artifact) != nil || artifact.Plan.ParentSHA != e.Journal.PlanSHA256 || artifact.Plan.Plan.Environment != cfg.Environment || artifact.Plan.Plan.Stack != e.Journal.Stack {
		return bad("original plan")
	}
	proof, err := readCreateOpenBaoACKCompletion(store, e.Journal.PlanSHA256)
	if err != nil || proof == nil {
		return bad("ACK completion")
	}
	lateReview := deploymentplan.String(proof.Native["late_review_sha256"])
	var late createManagedLateSessionRecovery
	if !environmentPreflightDigest.MatchString(lateReview) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", lateReview, "managed-late-session-source.json"), &late) != nil {
		return bad("terminal descriptor")
	}
	late.Review = lateReview
	entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	terminalPair, pairErr := readManagedLateTerminalFinalizerAuthorityPair(store, e.Journal.PlanSHA256)
	if pairErr != nil || terminalPair == nil || entry == nil || entry.State != "completed" || entry.CompletedAt.IsZero() || validateManagedLateTerminalFinalizerCompletion(store, e, &late) != nil || validateManagedLateRemainingCompletion(store, e, &late, proof) != nil {
		return bad("durable terminal completion")
	}
	if err := validateCreateOpenBaoACKCompletionSource(ctx, cfg, store, e.Journal.PlanSHA256, artifact.Plan.Plan.Release, proof, environmentCIImageCommand(cfg), true, e); err != nil {
		return nil, err
	}
	release := artifact.Plan.Plan.Release
	release.Images = map[string]environmentReleaseImage{}
	for role, image := range artifact.Plan.Plan.Release.Images {
		release.Images[role] = image
	}
	release.Images["LKE_OPENBAO_PKI_IMAGE"] = proof.Recovery.Image
	release, err = effectiveCreateCertIssuerPrepareReleaseWithCommand(ctx, cfg, store, e.Journal.PlanSHA256, release, environmentCIImageCommand(cfg))
	if err != nil {
		return nil, err
	}
	var bindings environmentIdentityBindings
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "bindings.json"), &bindings) != nil || bindings.Environment != cfg.Environment || bindings.Stack != e.Journal.Stack || bindings.Owners["PersistentVolumeClaim/"+redis.Namespace+"/"+redis.PVCName] != redis.PVCUID || bindings.Owners[saved.Descriptor.ResourceID] != saved.Descriptor.UID {
		return bad("completed bindings")
	}
	h := &createNormalTailHandoff{OriginalPlan: &artifact.Plan.Plan, Release: release, Bindings: bindings, Receipts: artifact.Plan.Receipts, Source: saved.Source, OriginalVolumes: original, RedisVolume: *redis, Execution: e, Pins: map[string]string{"source": deploymentplan.Digest(saved), "migration": deploymentplan.Digest(completed), "archive": completed.RDBSHA, "ack": deploymentplan.Digest(proof), "owner": deploymentplan.Digest(e.owner)}}
	for key, relative := range map[string]string{"terminal": managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256), "bindings": filepath.Join(base, "bindings.json")} {
		path, err := privateDeploymentStatePath(cfg.Environment, relative)
		if err != nil {
			return bad(key)
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil {
			return bad(key)
		}
		h.Pins[key] = environmentPreflightBytesSHA256(raw)
	}
	if err := validateCreateNormalTailStages(cfg, h); err != nil {
		return nil, err
	}
	if err := e.Check(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

func validateCreateNormalTailRedisChildren(store secretStore, e *environmentCreateExecution, d *createManagedLateRedisDurable, completed *managedLateRedisDurableReceipt) error {
	bad := func() error {
		return createProviderRecoveryBlock("completed Redis child differs from its immutable migration recipe")
	}
	stopped := deploymentplan.Clone(d.Original.Desired)
	deploymentplan.Map(stopped["metadata"])["uid"] = d.UID
	deploymentplan.Map(stopped["spec"])["replicas"] = float64(0)
	var helperUID string
	for i, want := range []struct {
		stage, action string
		body          deploymentplan.Object
	}{
		{"claim", "create", d.Claim}, {"target", "create", d.Helper}, {"retire", "retire", d.Helper}, {"stop", "update", stopped}, {"switch", "update", d.Switch},
	} {
		p, j, err := managedTransportPostAcceptedStage(store, e, "kubernetes/normal-redis-durable-"+want.stage)
		if err != nil || len(p.Resources) != 1 {
			return bad()
		}
		r := p.Resources[0]
		if r.Action != want.action || r.ID != deploymentplan.ID(want.body) || !deploymentplan.Matches(want.body, r.Desired) {
			return bad()
		}
		if i == 0 && j.Owners[r.ID] != completed.Volume.PVCUID || i == 1 && !environmentBootstrapOwnerUUID.MatchString(j.Owners[r.ID]) || i == 2 && (r.UID != helperUID || j.Owners[r.ID] != helperUID) || i >= 3 && (r.UID != d.UID || j.Owners[r.ID] != d.UID) {
			return bad()
		}
		if i == 1 {
			helperUID = j.Owners[r.ID]
		}
	}
	verified := createOpenBaoACKCompletionEntry(e.Journal, "normal-redis-durable-verified")
	outer := createOpenBaoACKCompletionEntry(e.Journal, managedLateRedisDurableKey)
	if verified == nil || outer == nil || verified.State != "completed" || verified.IntentSHA256 != activeTransportJournalDigest(jsonRawObject(verified.Receipt)) || activeTransportJournalDigest(jsonRawObject(verified.Receipt)) != activeTransportJournalDigest(jsonRawObject(outer.Receipt)) || completed.CompletedAt.After(verified.CompletedAt) {
		return bad()
	}
	snapshot := createOpenBaoACKCompletionEntry(e.Journal, "normal-redis-durable-snapshot")
	target := createOpenBaoACKCompletionEntry(e.Journal, "normal-redis-durable-target-verified")
	var recorded struct {
		Snapshot managedLateRedisSnapshot
		Volume   environmentCreateVolumeOwner
	}
	var evidence map[string]string
	if snapshot == nil || target == nil || decodeEnvironmentJSON(snapshot.Receipt, &recorded) != nil || decodeEnvironmentJSON(target.Receipt, &evidence) != nil || deploymentplan.Digest(recorded.Snapshot) != deploymentplan.Digest(completed.Snapshot) || recorded.Volume != completed.Volume || deploymentplan.Digest(evidence) != deploymentplan.Digest(completed.TargetEvidence) || completed.Snapshot.SavedAt.Before(snapshot.StartedAt) || completed.Snapshot.SavedAt.After(snapshot.CompletedAt) || target.IntentSHA256 != activeTransportJournalDigest(jsonRawObject(target.Receipt)) {
		return bad()
	}
	return nil
}

// The workload journal, including a written next resource waiting for readiness,
// is the allocation proof. Whole-plan acceptance is deliberately not required.
func readCreateNormalTailWorkload(store secretStore, e *environmentCreateExecution, frozenTarget *deploymentplan.Plan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	return readCreateNormalTailWorkloadEffective(store, e, frozenTarget)
}

func readCreateNormalTailStage(store secretStore, e *environmentCreateExecution, stage string, frozenTarget *deploymentplan.Plan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	if e == nil || e.Store != store || frozenTarget == nil {
		return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("frozen normal target unavailable")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil {
		return nil, deploymentplan.Journal{}, nil
	}
	if entry.Phase != stage || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
		return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("recorded original stage has invalid lifecycle state")
	}
	intent, intentErr := environmentCreateKubernetesStageIntent(frozenTarget)
	sha, shaErr := environmentCreatePlanSHA256(intent)
	if intentErr == nil && shaErr == nil && entry.IntentSHA256 == sha && entry.Phase == stage && entry.State == "pending" && len(entry.Receipt) == 0 {
		_, exists, readErr := frozenTarget.ReadExecution(store.Root)
		if readErr != nil {
			return nil, deploymentplan.Journal{}, readErr
		}
		if !exists {
			for _, lease := range e.Journal.KubernetesLeases {
				if strings.HasPrefix(lease.Holder, frozenTarget.SHA256()+":") {
					return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("workload lease exists without its execution evidence")
				}
			}
			return nil, deploymentplan.Journal{}, nil // admission failed before a child lease or write
		}
	}
	p, j, err := originalCreateChildAtIntent(store, e, *entry)
	if err != nil {
		return nil, j, err
	}
	if p.SHA256() != frozenTarget.SHA256() || p.Environment != frozenTarget.Environment || p.Stack != frozenTarget.Stack || len(p.Resources) != len(frozenTarget.Resources) {
		return nil, j, createProviderRecoveryBlock("recorded workload scope changed")
	}
	for i, resource := range p.Resources {
		want := frozenTarget.Resources[i]
		if resource.ID != want.ID || resource.Phase != want.Phase || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(want.Desired) || deploymentplan.Digest(resource.Dependencies) != deploymentplan.Digest(want.Dependencies) {
			return nil, j, createProviderRecoveryBlock("recorded workload differs from frozen target")
		}
	}
	verified, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(verified) != deploymentplan.Digest(j) {
		return nil, j, createProviderRecoveryBlock("recorded workload journal is incomplete or uncertain")
	}
	if entry.State == "allocated" || entry.State == "completed" {
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if !j.Accepted || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Plan != p.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(j.Owners) {
			return nil, j, createProviderRecoveryBlock("workload parent response differs from the genuinely accepted child")
		}
	}
	return p, j, nil
}
