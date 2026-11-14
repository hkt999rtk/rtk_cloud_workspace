package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// recover-create-stale-identity-pod is a scoped original-generation repair.
// Its preflight performs only native server dry-run; execution retires the
// exact old Pod with UID/resourceVersion preconditions and never deletes its
// replacement. The permanent identity remains in the existing OpenBao scope.
func runCreateStaleIdentityPodRecovery(args []string) error {
	fs := flag.NewFlagSet("recover-create-stale-identity-pod", flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment")
	execute := fs.Bool("execute", false, "perform the qualified scoped Pod restart")
	reconcile := fs.Bool("reconcile-observed", false, "record an already observed same-owner replacement without restarting or resetting its original deadline")
	controller := fs.Bool("controller", false, "repair the recorded Controller identity owner")
	review := fs.String("review-sha256", "", "qualified scoped repair digest")
	confirm := fs.String("confirm", "", "exact selected stack")
	timeout := fs.Duration("timeout", 10*time.Minute, "this repair command timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*env) || *timeout <= 0 || (*execute && *reconcile) || ((*execute || *reconcile) && (!environmentPreflightDigest.MatchString(*review) || *confirm == "")) || (!*execute && !*reconcile && (*review != "" || *confirm != "")) {
		return createProviderRecoveryBlock("select the environment and provide the exact scoped review and stack only for execution")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if (*execute || *reconcile) && *confirm != stack {
		return createProviderRecoveryBlock("scoped repair confirmation differs from the selected environment stack")
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil || !environmentPreflightDigest.MatchString(checkpoint.Owner.PlanSHA256) {
		return createProviderRecoveryBlock("original create generation is unavailable")
	}
	e, original, err := loadActiveTransportExecution(cfg, store, checkpoint.Owner.PlanSHA256)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(e.Journal) != deploymentplan.Digest(checkpoint.Journal) || deploymentplan.Digest(cfg) != original.Plan.Plan.ConfigurationSHA256 || (!*reconcile && deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256) {
		return createProviderRecoveryBlock("original scoped repair settings or owner changed")
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(raw) != original.Plan.KubeconfigSHA256 {
		return createProviderRecoveryBlock("original Kubernetes identity changed")
	}
	native, err := newEnvironmentKubernetesBackend(string(raw), *env)
	if err != nil {
		return err
	}
	native.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store}
	if *reconcile {
		if _, _, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, &original.Plan.Plan); err != nil {
			return err
		}
		q, err := loadStaleIdentityPodProof(store, e, *review)
		if err != nil {
			return err
		}
		if q.Controller != *controller {
			return createProviderRecoveryBlock("selected original identity owner differs")
		}
		if err := runEnvironmentProgress(ctx, os.Stdout, "deployment.recovery.identity-pod.reconcile", func(ctx context.Context) error { return reconcileObservedStaleIdentityPod(ctx, store, e, native, q) }); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "PASS environment="+*env+" scope=observed-original-identity-pod-reconciliation completed=false")
		return nil
	}
	var q2 createInitializerMountForwardProof
	var retained *deploymentplan.Plan
	var journal deploymentplan.Journal
	var target deploymentplan.Resource
	var sourceStage string
	deadline := time.Now().UTC().Add(*timeout)
	if *controller {
		sourceStage, retained, journal, target, err = staleControllerRecordedSource(ctx, store, e, backend)
		if err == nil {
			err = verifyStaleControllerOriginalACK(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: native}, target)
		}
	} else {
		q2, retained, journal, target, err = staleIdentityPodSource(ctx, store, e, backend)
		sourceStage = q2.RetainStage
		deadline = q2.ExecutionDeadline
	}
	if err != nil {
		return err
	}
	if !time.Now().Before(deadline) {
		return createProviderRecoveryBlock("original scoped execution deadline has expired")
	}
	if !*execute {
		objects, err := native.Inventory(ctx, stack)
		if err != nil {
			return err
		}
		candidate, rsID, rsUID, podID, podUID, leases, err := selectStaleIdentityPod(retained, journal, target, objects, time.Now().UTC(), *controller)
		if err != nil {
			return err
		}
		if err = candidate.Admit(ctx, backend); err != nil {
			return err
		}
		now := time.Now().UTC()
		if !now.Before(deadline) {
			return createProviderRecoveryBlock("original create execution deadline has expired")
		}
		mountReview := ""
		if !*controller {
			mountReview = deploymentplan.Digest(q2)
		}
		q := staleIdentityPodProof{Schema: staleIdentityPodProofSchema, Controller: *controller, Environment: *env, ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(e.owner), SourceJournal: e.Journal, SourceJournalSHA: deploymentplan.Digest(e.Journal), MountReview: mountReview, RetainStage: sourceStage, RetainPlanSHA: retained.SHA256(), RetainJournalSHA: deploymentplan.Digest(journal), DeploymentID: target.ID, DeploymentUID: target.UID, ReplicaSetID: rsID, ReplicaSetUID: rsUID, PodID: podID, PodUID: podUID, Leases: leases, Candidate: candidate, QualifiedAt: now, ExecutionDeadline: deadline}
		if err = saveStaleIdentityPodProof(store, e, q); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Scoped identity Pod restart only; original deployment remains incomplete.")
		fmt.Fprintf(os.Stdout, "plan-sha256: %s\nGO environment=%s scope=original-generation-identity-pod-repair completed=false\n", deploymentplan.Digest(q), *env)
		return nil
	}
	q, err := loadStaleIdentityPodProof(store, e, *review)
	if err != nil {
		return err
	}
	if q.Controller != *controller || (!*controller && (q.MountReview != deploymentplan.Digest(q2) || !q.ExecutionDeadline.Equal(q2.ExecutionDeadline))) || q.RetainStage != sourceStage || q.RetainPlanSHA != retained.SHA256() || q.RetainJournalSHA != deploymentplan.Digest(journal) || q.DeploymentID != target.ID || q.DeploymentUID != target.UID || !time.Now().Before(q.ExecutionDeadline) {
		return createProviderRecoveryBlock("original scoped Pod repair evidence changed")
	}
	if err = executeStaleIdentityPodRestart(ctx, store, e, backend, native, q, target); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Scoped identity Pod restart accepted; original deployment remains incomplete.")
	fmt.Fprintf(os.Stdout, "PASS environment=%s scope=original-generation-identity-pod-repair completed=false\n", *env)
	return nil
}

func staleIdentityPodIntent(q staleIdentityPodProof) []byte {
	return mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": deploymentplan.Digest(q), "original_mount_review_sha256": q.MountReview, "pod_id": q.PodID, "pod_uid": q.PodUID, "candidate_sha256": q.Candidate.SHA256()})
}

type staleIdentityPodExecutionBackend interface {
	deploymentplan.Backend
	CheckParentExecution(context.Context, *deploymentplan.Plan, string) error
	RecordParentExecutionLease(context.Context, *deploymentplan.Plan, deploymentplan.LeaseOwner) error
	AcquireExecution(context.Context, *deploymentplan.Plan) (deploymentplan.ExecutionLease, error)
}

type staleIdentityPodInventory interface {
	Read(context.Context, string) (deploymentplan.Object, error)
	Inventory(context.Context, string) ([]deploymentplan.Object, error)
}

func executeStaleIdentityPodRestart(ctx context.Context, store secretStore, e *environmentCreateExecution, backend staleIdentityPodExecutionBackend, native staleIdentityPodInventory, q staleIdentityPodProof, target deploymentplan.Resource) (result error) {
	ctx, cancel := context.WithDeadline(ctx, q.ExecutionDeadline)
	defer cancel()
	bad := func() error {
		return createProviderRecoveryBlock("stale identity Pod repair requires the original fenced owner, UID and expired Leases")
	}
	if !validStaleIdentityPodProof(store, e, q) || !time.Now().Before(q.ExecutionDeadline) || target.ID != q.DeploymentID || target.UID != q.DeploymentUID {
		return bad()
	}
	key := "stale-identity-pod-" + deploymentplan.Digest(q)
	intent := staleIdentityPodIntent(q)
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		if deploymentplan.Digest(e.Journal) != q.SourceJournalSHA {
			return bad()
		}
	} else if entry.Phase != "stale-identity-pod" || entry.IntentSHA256 != intentSHA || entry.StartedAt.Before(q.QualifiedAt) || !entry.StartedAt.Before(q.ExecutionDeadline) || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
		return bad()
	}
	if err = backend.CheckParentExecution(ctx, q.Candidate, store.Root); err != nil {
		return err
	}
	lease, err := backend.AcquireExecution(ctx, q.Candidate)
	if err != nil {
		return err
	}
	if lease == nil {
		return bad()
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := lease.Release(cleanup); err != nil && result == nil {
			result = err
		}
	}()
	owner := lease.Owner()
	fenced := func() bool {
		return time.Now().Before(q.ExecutionDeadline) && e.Check(ctx) == nil && lease.Check(ctx) == nil && lease.Owner() == owner && backend.CheckParentExecution(ctx, q.Candidate, store.Root) == nil
	}
	if !fenced() || backend.RecordParentExecutionLease(ctx, q.Candidate, owner) != nil {
		return bad()
	}
	old, err := native.Read(ctx, q.PodID)
	if err != nil {
		return err
	}
	if entry == nil {
		if old == nil {
			return bad()
		}
		if err = verifyStaleIdentityPodBeforeDelete(ctx, native, q, target); err != nil {
			return err
		}
		if err = q.Candidate.Admit(ctx, backend); err != nil {
			return err
		}
		if !fenced() {
			return bad()
		}
		if err = e.BeginEntry(key, "stale-identity-pod", intent, time.Now().UTC()); err != nil {
			return err
		}
	} else if old != nil {
		if err = verifyStaleIdentityPodBeforeDelete(ctx, native, q, target); err != nil {
			return err
		}
	}
	if old != nil {
		if err = verifyStaleIdentityPodBeforeDelete(ctx, native, q, target); err != nil {
			return err
		}
		body, bodyErr := q.Candidate.Resources[0].Body(old)
		if bodyErr != nil || body == nil {
			return bad()
		}
		if !fenced() {
			return bad()
		}
		if _, err = backend.Write(ctx, "retire", body, false); err != nil {
			return err
		}
		if err = backend.Wait(ctx, q.Candidate.Resources[0]); err != nil {
			return err
		}
	}
	if !fenced() {
		return bad()
	}
	if err = backend.Wait(ctx, target); err != nil {
		return err
	}
	newUID, err := verifyStaleIdentityPodReplacement(ctx, native, q, target)
	if err != nil {
		return err
	}
	if !fenced() {
		return bad()
	}
	receipt := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": deploymentplan.Digest(q), "deployment_id": q.DeploymentID, "deployment_uid": q.DeploymentUID, "original_pod_uid": q.PodUID, "replacement_pod_uid": newUID, "lease_ids_sha256": deploymentplan.Digest(q.Leases)})
	entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return bad()
	}
	if entry.State == "completed" {
		if !samePersistedJSONReceipt(entry.Receipt, json.RawMessage(receipt)) {
			return bad()
		}
		return e.Check(ctx)
	}
	if err = e.RecordReceipt(key, json.RawMessage(receipt)); err != nil {
		return err
	}
	if !fenced() {
		return bad()
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

func verifyStaleIdentityPodBeforeDelete(ctx context.Context, native staleIdentityPodInventory, q staleIdentityPodProof, target deploymentplan.Resource) error {
	objects, err := native.Inventory(ctx, q.Candidate.Stack)
	if err != nil {
		return err
	}
	return verifyStaleIdentityPodSnapshot(q, target, objects, time.Now().UTC())
}

func verifyStaleIdentityPodReplacement(ctx context.Context, native staleIdentityPodInventory, q staleIdentityPodProof, target deploymentplan.Resource) (string, error) {
	objects, err := native.Inventory(ctx, q.Candidate.Stack)
	if err != nil {
		return "", err
	}
	return checkStaleIdentityReplacement(q, target, objects, time.Now().UTC())
}
