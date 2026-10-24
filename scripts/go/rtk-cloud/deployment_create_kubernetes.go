package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCreateKubernetesBackend struct {
	environmentKubernetesBackend
	execution       *environmentCreateExecution
	cfg             deploymentConfig
	store           secretStore
	stagePlanSHA256 string
}

func (b environmentCreateKubernetesBackend) initialOpenBaoWaitResource(ctx context.Context, resource deploymentplan.Resource) (deploymentplan.Resource, error) {
	if resource.Action == "create" && environmentOnDeleteOpenBao(resource) {
		if err := b.execution.Check(ctx); err != nil {
			return resource, err
		}
		var journal deploymentplan.Journal
		path := filepath.Join(b.store.Root, "deployment", "rollback", b.stagePlanSHA256, "journal.json")
		if !environmentPreflightDigest.MatchString(b.stagePlanSHA256) || readEnvironmentPreflightJSON(path, &journal) != nil || journal.PlanSHA256 != b.stagePlanSHA256 || journal.InFlight != "" || journal.Owners[resource.ID] == "" {
			return resource, environmentCreateBlocker("deployment.create.openbao", "initial OnDelete readiness requires the original successful StatefulSet write owner")
		}
		// Initial OnDelete has no native rollout-status operation. Verify its
		// captured owner and current Pod through the read-only retained path.
		resource.Action, resource.UID = "retain", journal.Owners[resource.ID]
	}
	return resource, nil
}

func (b environmentCreateKubernetesBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if !dry {
		if err := b.beforeSchemaOwnerWrite(ctx, body); err != nil {
			return nil, err
		}
	}
	return b.environmentKubernetesBackend.Write(ctx, action, body, dry)
}

func (b environmentCreateKubernetesBackend) Wait(ctx context.Context, resource deploymentplan.Resource) error {
	// A retained owner creates no new allocation. Keep its native readiness
	// checks and the phase/final storage proofs without replaying the complete
	// allocation producer for each recorded PVC. Decide before OnDelete turns
	// an original create into a retained readiness check.
	captureAllocation := resource.Action != "retain" && (resource.Desired["kind"] == "PersistentVolumeClaim" || resource.Desired["kind"] == "StatefulSet")
	resource, err := b.initialOpenBaoWaitResource(ctx, resource)
	if err != nil {
		return err
	}
	if deploymentplan.Map(deploymentplan.Map(resource.Desired["metadata"])["annotations"])[environmentBillingSchemaRevision] != nil && resource.UID == "" {
		var journal deploymentplan.Journal
		path := filepath.Join(b.store.Root, "deployment", "rollback", b.stagePlanSHA256, "journal.json")
		if !environmentPreflightDigest.MatchString(b.stagePlanSHA256) || readEnvironmentPreflightJSON(path, &journal) != nil || journal.PlanSHA256 != b.stagePlanSHA256 || journal.InFlight != "" || journal.Owners[resource.ID] == "" {
			return environmentSchemaBlocker("billing", "schema Job readiness lacks its original successful child write UID")
		}
		resource.UID = journal.Owners[resource.ID]
	}
	b.environmentKubernetesBackend.SchemaWorkspace = b.cfg.Workspace
	if err := b.environmentKubernetesBackend.Wait(ctx, resource); err != nil {
		return err
	}
	if captureAllocation {
		if err := captureEnvironmentCreateVolumeOwners(ctx, b.cfg, b.environmentKubernetesBackend, b.execution); err != nil {
			return err
		}
		ledger, err := readEnvironmentCreateLedger(b.execution)
		if err != nil {
			return err
		}
		compiled := &compiledEnvironmentDeployment{Store: b.store, Backend: b.environmentKubernetesBackend}
		if err := saveEnvironmentCreateRemovalVolumeProof(ctx, b.cfg, compiled, ledger, b.execution); err != nil {
			return err
		}
	}
	return nil
}

type environmentCreateKubernetesLease struct {
	deploymentplan.ExecutionLease
	execution *environmentCreateExecution
}

func (lease environmentCreateKubernetesLease) Check(ctx context.Context) error {
	if err := lease.execution.Check(ctx); err != nil {
		return err
	}
	return lease.ExecutionLease.Check(ctx)
}

func (b environmentCreateKubernetesBackend) AcquireExecution(ctx context.Context, plan *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	if err := b.CheckParentExecution(ctx, plan, b.store.Root); err != nil {
		return nil, err
	}
	lease, err := b.environmentKubernetesBackend.AcquireExecution(ctx, plan)
	if err != nil {
		return nil, err
	}
	return environmentCreateKubernetesLease{lease, b.execution}, nil
}

func (b environmentCreateKubernetesBackend) CheckParentExecution(ctx context.Context, plan *deploymentplan.Plan, privateRoot string) error {
	if b.execution == nil || plan == nil || plan.Environment != b.cfg.Environment || plan.Stack != b.cfg.Values["CLOUD_STACK_NAME"] || privateRoot != b.store.Root || !filepath.IsAbs(privateRoot) {
		return environmentCreateBlocker("deployment.create.ownership", "Kubernetes stage does not belong to the original selected create execution")
	}
	return b.execution.Check(ctx)
}

func (b environmentCreateKubernetesBackend) RecordParentExecutionLease(ctx context.Context, plan *deploymentplan.Plan, owner deploymentplan.LeaseOwner) error {
	if err := b.CheckParentExecution(ctx, plan, b.store.Root); err != nil {
		return err
	}
	return b.execution.RecordKubernetesLease(owner)
}

// A Namespace must exist before the first real cluster Lease and namespaced
// dry-run. This tiny foundation write uses exact absence/admission/readback
// fences and durable root journal evidence, never kubectl apply or adoption.
func createEnvironmentExecutionNamespaces(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, execution *environmentCreateExecution) error {
	for _, namespace := range lkeNamespaces(environmentCreateProviderValues(cfg)) {
		body := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace.Name, "labels": deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}}}
		id := deploymentplan.ID(body)
		if err := execution.Check(ctx); err != nil {
			return err
		}
		current, err := backend.Read(ctx, id)
		if err != nil || current != nil {
			return environmentCreateBlocker("deployment.create.namespace", "a new namespace is occupied or cannot be read: "+id)
		}
		admitted, err := backend.Write(ctx, "create", body, true)
		if err != nil || admitted == nil || !deploymentplan.Matches(deploymentplan.Projection(body), deploymentplan.Projection(admitted)) {
			return environmentCreateBlocker("deployment.create.namespace", "exact namespace admission failed: "+id)
		}
		intent, _ := json.Marshal(body)
		if err := execution.BeginEntry(id, "namespace", intent, time.Now().UTC()); err != nil {
			return err
		}
		actual, err := backend.Write(ctx, "create", body, false)
		if actual != nil && deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != "" {
			receipt, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": deploymentplan.Map(actual["metadata"])["uid"]})
			if saveErr := execution.RecordReceipt(id, receipt); saveErr != nil {
				return saveErr
			}
		}
		if err != nil || actual == nil {
			return environmentCreateBlocker("deployment.create.namespace", "namespace write outcome requires original-journal reconciliation: "+id)
		}
		readback, err := backend.Read(ctx, id)
		if err != nil || readback == nil || deploymentplan.Map(readback["metadata"])["uid"] != deploymentplan.Map(actual["metadata"])["uid"] || !deploymentplan.Matches(body, readback) {
			return environmentCreateBlocker("deployment.create.namespace", "namespace owner or exact body could not be verified: "+id)
		}
		if err := execution.CompleteEntry(id, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func createEnvironmentRetainStorageClass(ctx context.Context, cfg deploymentConfig, backend environmentKubernetesBackend, execution *environmentCreateExecution) error {
	name := cfg.Values["DURABLE_DATA_STORAGE_CLASS"]
	if name != "linode-block-storage-retain" {
		return environmentCreateBlocker("deployment.create.storage-class", "maintained fresh LKE durable allocation requires the reviewed Retain CSI class")
	}
	body := deploymentplan.Object{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": deploymentplan.Object{"name": name, "labels": deploymentplan.Object{"rtk.realtek.com/stack": cfg.Values["CLOUD_STACK_NAME"], "app.kubernetes.io/managed-by": "rtk-cloud"}}, "provisioner": "linodebs.csi.linode.com", "reclaimPolicy": "Retain", "volumeBindingMode": "Immediate", "allowVolumeExpansion": true}
	id := deploymentplan.ID(body)
	if err := execution.Check(ctx); err != nil {
		return err
	}
	current, err := backend.Read(ctx, id)
	if err != nil {
		return err
	}
	intent, _ := json.Marshal(body)
	if err := execution.BeginEntry(id, "storage", intent, time.Now().UTC()); err != nil {
		return err
	}
	if current == nil {
		admitted, err := backend.Write(ctx, "create", body, true)
		if err != nil || admitted == nil || !deploymentplan.Matches(body, admitted) {
			return environmentCreateBlocker("deployment.create.storage-class", "exact Retain CSI class admission failed")
		}
		actual, writeErr := backend.Write(ctx, "create", body, false)
		if actual != nil && deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != "" {
			receipt, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": deploymentplan.Map(actual["metadata"])["uid"], "policy_sha256": deploymentplan.Digest(deploymentplan.Object{"provisioner": body["provisioner"], "reclaimPolicy": body["reclaimPolicy"], "volumeBindingMode": body["volumeBindingMode"], "allowVolumeExpansion": body["allowVolumeExpansion"]})})
			if err := execution.RecordReceipt(id, receipt); err != nil {
				return err
			}
		}
		if writeErr != nil {
			return environmentCreateBlocker("deployment.create.storage-class", "Retain CSI class write outcome requires original-journal reconciliation")
		}
		current, err = backend.Read(ctx, id)
		if err != nil {
			return err
		}
	}
	if current == nil || current["provisioner"] != "linodebs.csi.linode.com" || current["reclaimPolicy"] != "Retain" || current["volumeBindingMode"] != "Immediate" || current["allowVolumeExpansion"] != true || deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) == "" {
		return environmentCreateBlocker("deployment.create.storage-class", "actual CSI class does not preserve the maintained durable allocation policy")
	}
	// An already supplied class is verified and retained, never relabeled or
	// adopted. Its original native UID is evidence of the capability actually used.
	receipt, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": deploymentplan.Map(current["metadata"])["uid"], "policy_sha256": deploymentplan.Digest(deploymentplan.Object{"provisioner": current["provisioner"], "reclaimPolicy": current["reclaimPolicy"], "volumeBindingMode": current["volumeBindingMode"], "allowVolumeExpansion": current["allowVolumeExpansion"]})})
	if current != nil {
		if err := execution.RecordReceipt(id, receipt); err != nil {
			return err
		}
	}
	return execution.CompleteEntry(id, time.Now().UTC())
}

func applyEnvironmentCreateStage(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, execution *environmentCreateExecution, stage string, definitions []deploymentplan.Definition, out io.Writer) error {
	if len(definitions) == 0 {
		return environmentCreateBlocker("deployment.create.stage", "an enabled Kubernetes stage has no concrete exact resource bodies: "+stage)
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		return err
	}
	native, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return err
	}
	native.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	if runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); ok && runtime.ExecutionSHA256 == execution.Journal.PlanSHA256 {
		native.OpenBaoReplacementReady = func(ctx context.Context, controller, pod deploymentplan.Object) error {
			if err := validateOpenBaoPodOwner(controller, pod); err != nil {
				return err
			}
			return unsealEnvironmentCreateOpenBaoRestart(ctx, cfg, store, runtime, pod)
		}
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: execution, cfg: cfg, store: store}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, definitions)}
	plan, err := readEnvironmentCreateStageInput(store, execution, stage)
	if err != nil {
		return err
	}
	entry := createOpenBaoACKCompletionEntry(execution.Journal, "kubernetes/"+stage)
	if plan == nil && entry != nil {
		plan, _, err = originalCreateChildAtIntent(store, execution, *entry)
		if err != nil {
			return err
		}
	}
	if plan == nil {
		live, err := native.Inventory(ctx, parent.Stack)
		if err != nil {
			return err
		}
		plan, err = deploymentplan.Compile(inputs, definitions, live)
		if err != nil {
			return err
		}
	}
	if plan.Inputs != inputs {
		return environmentCreateBootstrapBlocker("stage changed its qualified original input or definitions")
	}
	backend.stagePlanSHA256 = plan.SHA256()
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := backend.CheckParentExecution(ctx, p, store.Root); err != nil {
			return err
		}
		fresh, err := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot)
		_, operator, readErr := readEnvironmentCreateOperator(cfg)
		if err != nil || readErr != nil || deploymentplan.Digest(fresh) != parent.ConfigurationSHA256 || deploymentplan.Digest(operator) != parent.OperatorSHA256 {
			return environmentCreateBlocker("deployment.create.inputs", "maintained intent or operator credentials changed during the admitted create execution")
		}
		return nil
	}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error { return fence(ctx, p) }
	accept := func(ctx context.Context, p *deploymentplan.Plan) error {
		if err := backend.CheckParentExecution(ctx, p, store.Root); err != nil {
			return err
		}
		if strings.HasPrefix(stage, "current-owner-quiesce-") || strings.HasPrefix(stage, "used-installation-owner-quiesce-") {
			if len(p.Resources) != 2 {
				return environmentCreateBootstrapBlocker("quiescence requires exactly two original identity owners")
			}
			if err := waitCurrentIdentityOwnersStopped(ctx, p.Stack, native, p); err != nil {
				return err
			}
		}
		objects, err := native.Inventory(ctx, p.Stack)
		if err != nil {
			return targetWorkloadBlocker("", "selected native stage inventory is unavailable")
		}
		var ownerProof []map[string]string
		if stage == "normal-redis-durable-target" {
			owners, err := environmentRedisTargetPodOwners(store, p)
			if err != nil {
				return err
			}
			ownerProof = []map[string]string{owners}
		}
		return verifyEnvironmentTargetWorkloads(ctx, p, environmentCreateStageInventory(p, objects), func(ctx context.Context, image string) (map[string]bool, error) {
			return environmentRuntimeImageDigests(ctx, store, image)
		}, ownerProof...)
	}
	return executeEnvironmentCreateStagePlan(ctx, store, execution, stage, plan, backend, qualify, fence, accept, func(ctx context.Context) error {
		return captureEnvironmentCreateVolumeOwners(ctx, cfg, native, execution)
	}, out)
}

func environmentRedisTargetPodOwners(store secretStore, plan *deploymentplan.Plan) (map[string]string, error) {
	if !environmentRedisTargetPodPlan(plan) {
		return nil, targetWorkloadBlocker("", "standalone Redis helper plan is not the maintained target stage")
	}
	journal, exists, err := plan.ReadExecution(store.Root)
	if err != nil || !exists || journal.Failed != "" || !journal.AcceptanceStarted || len(journal.Completed) != 1 || journal.Completed[0] != plan.Resources[0].ID || len(journal.Owners) != 1 || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[plan.Resources[0].ID]) {
		return nil, targetWorkloadBlocker(plan.Resources[0].ID, "standalone Redis helper original write owner is unavailable")
	}
	return journal.Owners, nil
}

func environmentCreateStageBindingsSHA(stage string, definitions []deploymentplan.Definition) string {
	return deploymentplan.Digest(deploymentplan.Object{"stage": stage, "definitions": definitions})
}

// Stage acceptance verifies planned controllers and their actual descendants.
// It cannot certify the whole environment; full acceptance follows bootstrap.
func environmentCreateStageInventory(plan *deploymentplan.Plan, objects []deploymentplan.Object) []deploymentplan.Object {
	selected := map[string]bool{}
	live := map[string]deploymentplan.Object{}
	for _, resource := range plan.Resources {
		selected[resource.ID] = true
	}
	for _, object := range objects {
		live[deploymentplan.ID(object)] = object
	}
	var belongs func(string, map[string]bool) bool
	belongs = func(id string, seen map[string]bool) bool {
		if selected[id] {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		object := live[id]
		if object == nil {
			return false
		}
		metadata := deploymentplan.Map(object["metadata"])
		for _, raw := range certIssuerObjectList(metadata["ownerReferences"]) {
			ref := deploymentplan.Map(raw)
			if ref["controller"] != true {
				continue
			}
			owner := deploymentplan.String(ref["kind"]) + "/" + deploymentplan.String(metadata["namespace"]) + "/" + deploymentplan.String(ref["name"])
			if belongs(owner, seen) {
				return true
			}
		}
		return false
	}
	var result []deploymentplan.Object
	for _, object := range objects {
		if belongs(deploymentplan.ID(object), map[string]bool{}) {
			result = append(result, object)
		}
	}
	return result
}

func compileCreatedEnvironmentTarget(ctx context.Context, cfg deploymentConfig, parent *environmentCreatePlan, store secretStore, bindings environmentIdentityBindings, parentSHAs ...string) (*compiledEnvironmentDeployment, error) {
	parentSHA, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(parent))
	if err != nil {
		return nil, err
	}
	if len(parentSHAs) > 1 || len(parentSHAs) == 1 && !environmentPreflightDigest.MatchString(parentSHAs[0]) {
		return nil, createOpenBaoACKBlock("completed-parent")
	}
	if len(parentSHAs) == 1 {
		parentSHA = parentSHAs[0]
	}
	var commands []*environmentCreateExecution
	if runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); ok && runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.normalTail != nil {
		if runtime.ExecutionSHA256 != parentSHA {
			return nil, createOpenBaoACKBlock("completed-parent")
		}
		commands, err = managedLateNormalTailReleaseCommands(ctx, cfg, store, runtime)
		if err != nil {
			return nil, err
		}
	}
	release, err := effectiveCreateOpenBaoACKRelease(ctx, cfg, store, parentSHA, parent.Release, commands...)
	if err != nil {
		return nil, err
	}
	compiled, err := compileEnvironmentDeploymentWithImagesTarget(ctx, cfg, "create", release, nil)
	if err != nil {
		return nil, err
	}
	if deploymentplan.Digest(compiled.Bindings) != deploymentplan.Digest(bindings) {
		return nil, environmentCreateBlocker("deployment.create.bindings", "freshly completed bootstrap ownership or identities changed before workload compilation")
	}
	return compiled, nil
}
