package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const stagingWorkloadRuntimePrefix = "workload-runtime-"
const stagingLoggerInitPrefix = "staging-first-logger-inbox-"

func bindPrimaryAPIEnvironment(object deploymentplan.Object, stack, environment string) {
	if stack == "" || environment == "" || deploymentplan.ID(object) != "Deployment/"+stack+"-video-cloud/video-cloud-api" {
		return
	}
	container := mqttFoundationPrimary(object)
	if container != nil {
		container["env"] = mergeEnvironmentNamedFields(container["env"], []any{deploymentplan.Object{"name": "VIDEO_CLOUD_ENV", "value": environment}})
	}
}

// The original workload completed every write through the workload phase.
// Only its known runtime readiness failures and the untouched routing tail
// may be superseded by this stage.
func stagingWorkloadRuntimeInterrupted(p *deploymentplan.Plan, j deploymentplan.Journal) bool {
	api := "Deployment/" + p.Stack + "-video-cloud/video-cloud-api"
	logger := "Deployment/" + p.Stack + "-logger/cloud-logger"
	if p.Environment != "staging" || j.Accepted || j.AcceptanceStarted || j.InFlight != "" ||
		(j.Failed != api && j.Failed != logger) || len(j.Completed) != len(j.Owners) || len(j.Completed) >= len(p.Resources) {
		return false
	}
	for i, r := range p.Resources {
		if i < len(j.Completed) {
			if r.ID != j.Completed[i] || !environmentBootstrapOwnerUUID.MatchString(j.Owners[r.ID]) || r.Phase == "routing" || r.Action == "retire" {
				return false
			}
		} else if r.Phase != "routing" {
			return false
		}
	}
	return j.Owners[api] != "" && j.Owners[logger] != ""
}

// A new claim's marker is created atomically before opening an empty inbox.
// A restart with a missing database after the marker was written fails closed.
// The next stage removes the temporary launcher and keeps the initialized PVC.
const stagingLoggerInitializeInbox = `dir=/var/lib/rtk-cloud-logger
db=$dir/billing-inbox.db
if [ -e "$db" ]; then
  exec /usr/local/bin/rtk-cloud-logger "$@"
fi
for file in "$dir"/* "$dir"/.[!.]* "$dir"/..?*; do
  if [ -e "$file" ] || [ -L "$file" ]; then
    [ "$file" = "$dir/lost+found" ] && [ -d "$file" ] && [ ! -L "$file" ] || exit 1
  fi
done
mkdir -m 0700 "$dir/.rtk-first-initialization"
exec /usr/local/bin/rtk-cloud-logger "$@" -initialize-billing-inbox`

func stagingWorkloadRuntimeDefinitions(p *deploymentplan.Plan, j deploymentplan.Journal) (initial, steady []deploymentplan.Definition, err error) {
	bad := createProviderRecoveryBlock("staging workload runtime correction cannot prove its original API, Logger or new inbox claim")
	api := "Deployment/" + p.Stack + "-video-cloud/video-cloud-api"
	logger := "Deployment/" + p.Stack + "-logger/cloud-logger"
	claim := "PersistentVolumeClaim/" + p.Stack + "-logger/cloud-logger-billing-inbox"
	newClaim := false
	steady = environmentCreateDefinitions(p)
	for i, d := range steady {
		r := p.Resources[i]
		if uid := j.Owners[r.ID]; uid != "" {
			deploymentplan.Map(d.Object["metadata"])["uid"] = uid
			steady[i].Retain = r.ID != logger
		}
		switch r.ID {
		case claim:
			newClaim = r.Action == "create" && r.Baseline == nil && j.Owners[claim] != ""
		case api, logger:
			if j.Owners[r.ID] == "" || r.ID == logger && (r.Action != "create" || r.Baseline != nil) {
				return nil, nil, bad
			}
			c := mqttFoundationPrimary(d.Object)
			if c == nil {
				return nil, nil, bad
			}
			if r.ID == api {
				if mqttFoundationEnv(c, "PKI_ENVIRONMENT") != p.Environment || mqttFoundationEnv(c, "VIDEO_CLOUD_ENV") != "" {
					return nil, nil, bad
				}
				bindPrimaryAPIEnvironment(d.Object, p.Stack, p.Environment)
			}
			repair := deploymentplan.Definition{Object: deploymentplan.Clone(d.Object), Phase: r.Phase}
			if r.ID == logger {
				c := mqttFoundationPrimary(repair.Object)
				args := certIssuerObjectList(c["args"])
				volume := managedNamedField(environmentPKIPod(d.Object)["volumes"], "billing-inbox")
				mount := managedNamedField(c["volumeMounts"], "billing-inbox")
				if c["command"] != nil || len(args) != 4 || args[2] != "-billing-inbox" || args[3] != "/var/lib/rtk-cloud-logger/billing-inbox.db" ||
					mount["mountPath"] != "/var/lib/rtk-cloud-logger" || deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] != "cloud-logger-billing-inbox" ||
					mount["readOnly"] == true || deploymentplan.Map(d.Object["spec"])["replicas"] != float64(1) {
					return nil, nil, bad
				}
				c["command"] = []any{"/bin/sh", "-ec"}
				c["args"] = append([]any{stagingLoggerInitializeInbox, "rtk-cloud-logger"}, args...)
			}
			initial = append(initial, repair)
		}
	}
	if !newClaim || len(initial) != 2 {
		return nil, nil, bad
	}
	return initial, steady, nil
}

func applyStagingWorkloadRuntimeForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, priorStage string, original, prior *deploymentplan.Plan, j deploymentplan.Journal, backend environmentKubernetesBackend) error {
	initStage := stagingLoggerInitPrefix + prior.SHA256()[:12]
	stage := stagingWorkloadRuntimePrefix + prior.SHA256()[:12]
	bad := createProviderRecoveryBlock("staging runtime recovery lost its original completed writes or bound inbox")
	initial, steady, err := stagingWorkloadRuntimeDefinitions(prior, j)
	if err != nil {
		return err
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+initStage) == nil {
		for _, d := range initial {
			id := deploymentplan.ID(d.Object)
			actual, err := backend.Read(ctx, id)
			if err != nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[id] {
				return bad
			}
			for _, r := range prior.Resources {
				if r.ID == id && !deploymentplan.Matches(r.Desired, actual) {
					return bad
				}
			}
		}
		id := "PersistentVolumeClaim/" + parent.Stack + "-logger/cloud-logger-billing-inbox"
		claim, err := backend.Read(ctx, id)
		if err != nil || deploymentplan.Map(claim["metadata"])["uid"] != j.Owners[id] ||
			deploymentplan.Map(claim["status"])["phase"] != "Bound" || deploymentplan.String(deploymentplan.Map(claim["spec"])["volumeName"]) == "" {
			return bad
		}
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+initStage)
	if entry == nil || entry.State != "completed" {
		if err := applyEnvironmentCreateStage(ctx, cfg, store, parent, e, initStage, initial, os.Stdout); err != nil {
			return err
		}
	}
	if _, _, err := acceptedCreateStage(store, e, "kubernetes/"+initStage); err != nil {
		return err
	}
	entry = createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if entry == nil || entry.State != "completed" {
		if err := applyEnvironmentCreateStage(ctx, cfg, store, parent, e, stage, steady, os.Stdout); err != nil {
			return err
		}
	}
	accepted, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil {
		return err
	}
	for _, source := range []struct{ stage, plan string }{{priorStage, prior.SHA256()}, {"workload", original.SHA256()}} {
		key := "kubernetes/" + source.stage
		previous := createOpenBaoACKCompletionEntry(e.Journal, key)
		receipt := stagingWorkloadAllocationReceipt{accepted.SHA256(), journal.Owners, "kubernetes/" + stage, source.plan}
		if previous.State == "completed" {
			continue
		}
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
			return err
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.workload api-scope-and-original-logger-inbox")
	return nil
}

func verifyStagingWorkloadSupersession(store secretStore, e *environmentCreateExecution, stage string, prior *deploymentplan.Plan) error {
	bad := createProviderRecoveryBlock("staging runtime successor does not bind the interrupted workload and its original owners")
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	var receipt stagingWorkloadAllocationReceipt
	j, exists, err := prior.ReadExecution(store.Root)
	if err != nil || !exists || !stagingWorkloadRuntimeInterrupted(prior, j) || entry == nil ||
		decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Original != prior.SHA256() ||
		receipt.Forward != "kubernetes/"+stagingWorkloadRuntimePrefix+prior.SHA256()[:12] {
		return bad
	}
	accepted, progress, err := acceptedCreateStage(store, e, receipt.Forward)
	if err != nil || accepted.SHA256() != receipt.Plan || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(progress.Owners) || len(accepted.Resources) != len(prior.Resources) {
		return bad
	}
	for id, uid := range j.Owners {
		if progress.Owners[id] != uid {
			return bad
		}
	}
	return nil
}

// A deferred OnDelete restart can remain pending after all StatefulSet writes
// finished. Complete that exact action before the new child verifies retained
// readiness. The persisted child body and permanent state remain in place.
func resumeStagingOriginalOpenBaoRollout(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend) (resultErr error) {
	prior, j, err := readStagingWorkloadAllocationCheckpoint(store, e)
	if err != nil || prior == nil || !stagingWorkloadRuntimeInterrupted(prior, j) {
		return err
	}
	stage := stagingWorkloadRuntimePrefix + prior.SHA256()[:12]
	child, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || child == nil {
		return err
	}
	bad := createProviderRecoveryBlock("OpenBao pending rollout does not bind the original StatefulSet, target and custody")
	id := "StatefulSet/" + e.Journal.Stack + "-secrets/openbao"
	var target deploymentplan.Resource
	for _, r := range prior.Resources {
		if r.ID == id {
			target = r
		}
	}
	if !environmentOnDeleteUpdate(target) || target.UID != j.Owners[id] || envObjectReplicas(target.Desired) != 1 {
		return bad
	}
	retained := false
	for _, r := range child.Resources {
		if r.ID == id {
			retained = r.Action == "retain" && r.UID == target.UID && deploymentplan.Digest(r.Desired) == deploymentplan.Digest(target.Desired)
		}
	}
	if !retained {
		return bad
	}
	controller, err := native.Read(ctx, id)
	meta, status := deploymentplan.Map(controller["metadata"]), deploymentplan.Map(controller["status"])
	revision := deploymentplan.String(status["updateRevision"])
	if err != nil || meta["uid"] != target.UID || revision == "" || status["observedGeneration"] != meta["generation"] || !deploymentplan.Matches(target.Desired, controller) {
		return bad
	}
	key := "openbao/workload-native-rollout-" + prior.SHA256()[:12]
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"original_child": prior.SHA256(), "controller_uid": target.UID, "update_revision": revision})
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil || entry != nil && (entry.Phase != "workload" || entry.IntentSHA256 != intentSHA) {
		return bad
	}
	podID := "Pod/" + e.Journal.Stack + "-secrets/openbao-0"
	pod, err := native.Read(ctx, podID)
	if err != nil {
		return err
	}
	observed := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(pod["metadata"])["labels"])["controller-revision-hash"])
	if entry == nil && observed == revision {
		return nil
	}
	if entry != nil && entry.State == "completed" {
		if observed != revision || validateOpenBaoPodOwner(controller, pod) != nil {
			return bad
		}
		return e.Check(ctx)
	}
	if _, _, err := readOriginalCreateOpenBaoRestartCustody(store, e, target.UID); err != nil {
		return err
	}
	guard := environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store, stagePlanSHA256: prior.SHA256()}
	lease, err := guard.AcquireExecution(ctx, prior)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(cleanup))
	}()
	if err := lease.Check(ctx); err != nil {
		return err
	}
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	if entry == nil {
		if observed == "" || observed != status["currentRevision"] || validateOpenBaoPodOwner(controller, pod) != nil {
			return bad
		}
		health, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, native)
		if err != nil || !health.Initialized || health.Sealed {
			return bad
		}
		if _, err := native.retire(ctx, pod, true); err != nil {
			return err
		}
		if err := e.BeginEntry(key, "workload", intent, time.Now().UTC()); err != nil {
			return err
		}
	}
	native.OpenBaoReplacementReady = func(ctx context.Context, controller, pod deploymentplan.Object) error {
		if err := lease.Check(ctx); err != nil {
			return err
		}
		return unsealEnvironmentCreateOpenBaoRestart(ctx, cfg, store, environmentCreateBootstrapRuntime{Execution: e, Backend: native}, pod)
	}
	fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.staging.openbao completing recorded OnDelete revision")
	if err := native.waitOnDeleteOpenBao(ctx, target); err != nil {
		return err
	}
	if err := lease.Check(ctx); err != nil {
		return err
	}
	if err := captureEnvironmentCreateVolumeOwners(ctx, cfg, native, e); err != nil {
		return err
	}
	pod, err = native.Read(ctx, podID)
	podMeta := deploymentplan.Map(pod["metadata"])
	if err != nil || validateOpenBaoPodOwner(controller, pod) != nil || deploymentplan.Map(podMeta["labels"])["controller-revision-hash"] != revision {
		return bad
	}
	receipt := mustProviderRecoveryJSON(deploymentplan.Object{"pod_uid": podMeta["uid"], "controller_uid": target.UID, "revision": revision})
	if err := e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.openbao original-owner-and-pvc-revision-ready")
	return nil
}
