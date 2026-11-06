package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const stagingGeneralCapacityEntry = "provider/staging-general-volume-capacity"

type stagingGeneralCapacityIntent struct {
	ClusterID string `json:"cluster_id"`
	PoolID    int    `json:"pool_id"`
	NodeType  string `json:"node_type"`
	Members   []int  `json:"original_member_ids"`
	Count     int    `json:"count"`
}

type stagingGeneralCapacityPoolNode struct {
	ID int `json:"instance_id"`
}

type stagingGeneralCapacityPool struct {
	lkeNodePool
	Nodes []stagingGeneralCapacityPoolNode `json:"nodes"`
}

func (p stagingGeneralCapacityPool) matches(want stagingGeneralCapacityIntent, stack string) bool {
	if p.ID != want.PoolID || p.Type != want.NodeType || p.Label != stack+"-general" ||
		p.Labels["rtk.io/node-class"] != "general" || len(p.Taints) != 0 || p.Autoscaler.Enabled ||
		(p.Count != len(want.Members) && p.Count != want.Count) || len(p.Nodes) > want.Count {
		return false
	}
	for _, id := range want.Members {
		if !slices.ContainsFunc(p.Nodes, func(node stagingGeneralCapacityPoolNode) bool {
			return node.ID == id
		}) {
			return false
		}
	}
	return true
}

func stagingFleetValkeyAttachmentBlocked(ctx context.Context, backend environmentKubernetesBackend, stack, owner string) (bool, error) {
	ns := stack + "-platform"
	pod, err := backend.Read(ctx, "Pod/"+ns+"/fleet-valkey-0")
	if err != nil {
		return false, err
	}
	if pod == nil || deploymentplan.Map(pod["spec"])["nodeName"] != nil {
		return false, nil
	}
	for _, condition := range certIssuerObjectList(deploymentplan.Map(pod["status"])["conditions"]) {
		c := deploymentplan.Map(condition)
		if c["type"] != "PodScheduled" || c["status"] != "False" || c["reason"] != "Unschedulable" || !strings.Contains(deploymentplan.String(c["message"]), "2 node(s) exceed max volume count") {
			continue
		}
		claim, err := backend.Read(ctx, "PersistentVolumeClaim/"+ns+"/data-fleet-valkey-0")
		if err != nil || deploymentplan.Map(claim["status"])["phase"] != "Bound" || deploymentplan.String(deploymentplan.Map(claim["spec"])["volumeName"]) == "" {
			return false, createProviderRecoveryBlock("Fleet Valkey capacity repair requires its bound original claim")
		}
		for _, reference := range certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"]) {
			ref := deploymentplan.Map(reference)
			if ref["kind"] == "StatefulSet" && ref["name"] == "fleet-valkey" && ref["controller"] == true && ref["uid"] == owner {
				return true, nil
			}
		}
		return false, createProviderRecoveryBlock("Fleet Valkey capacity repair requires the original Pod owner")
	}
	return false, nil
}

// Increase just the existing general pool after native scheduling proves both
// original workers exhausted their CSI attachment slots. The absolute target
// and original membership are journaled before the one provider update.
func resumeStagingGeneralVolumeCapacity(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, operator map[string]string, e *environmentCreateExecution, backend environmentKubernetesBackend) error {
	prior, progress, err := readStagingWorkloadAllocationCheckpoint(store, e)
	if err != nil || prior == nil {
		return err
	}
	bad := createProviderRecoveryBlock("staging general pool capacity differs from its original verified allocation")
	id := "StatefulSet/" + plan.Stack + "-platform/fleet-valkey"
	owner := progress.Owners[id]
	if owner == "" {
		return nil
	}
	original := createOpenBaoACKCompletionEntry(e.Journal, stagingGeneralCapacityEntry)
	if original == nil {
		live, err := backend.Read(ctx, id)
		if err != nil || deploymentplan.Map(live["metadata"])["uid"] != owner ||
			deploymentplan.Map(environmentPKIPod(live)["nodeSelector"])["rtk.io/node-class"] != "general" {
			return bad
		}
		blocked, err := stagingFleetValkeyAttachmentBlocked(ctx, backend, plan.Stack, owner)
		if err != nil || !blocked {
			return err
		}
	}
	cluster := receipts["lke"]
	var workers environmentWorkerNames
	if _, err := positiveIntValue("cluster ID", cluster.NativeID); err != nil || cluster.Name != plan.Stack+"-lke" ||
		readExecutionJSON(filepath.Join(store.Root, "deployment/adapters/lke/worker-labels.json"), &workers) != nil ||
		workers.Schema != "rtk-cloud.lke-worker-labels/v1" || workers.Environment != "staging" || workers.Stack != plan.Stack || workers.ClusterID != cluster.NativeID {
		return bad
	}
	want := stagingGeneralCapacityIntent{ClusterID: cluster.NativeID, Count: 3}
	for _, worker := range workers.Entries {
		if worker.NodeClass == "general" {
			if worker.PoolID <= 0 || worker.InstanceID <= 0 || want.PoolID != 0 && want.PoolID != worker.PoolID || slices.Contains(want.Members, worker.InstanceID) {
				return bad
			}
			want.PoolID = worker.PoolID
			want.Members = append(want.Members, worker.InstanceID)
		}
	}
	slices.Sort(want.Members)
	for _, resource := range plan.Provider.Resources {
		if resource.Key == "lke" {
			for _, pool := range createProviderObjectList(resource.Body["node_pools"]) {
				if pool["label"] == plan.Stack+"-general" && workerNativeIDNumber(pool["count"]) == 2 {
					want.NodeType = deploymentplan.String(pool["type"])
				}
			}
		}
	}
	if len(want.Members) != 2 || want.NodeType == "" {
		return bad
	}
	path := fmt.Sprintf("/lke/clusters/%s/pools/%d", want.ClusterID, want.PoolID)
	token := operator["LINODE_TOKEN"]
	readPool := func(ctx context.Context) (stagingGeneralCapacityPool, error) {
		var pool stagingGeneralCapacityPool
		raw, err := environmentCreateProviderRequest(ctx, token, http.MethodGet, path, nil)
		if err != nil || json.Unmarshal(raw, &pool) != nil || !pool.matches(want, plan.Stack) {
			return pool, bad
		}
		return pool, nil
	}
	current, err := readPool(ctx)
	if err != nil {
		return err
	}
	intent := mustProviderRecoveryJSON(want)
	intentSHA, err := environmentCreatePlanSHA256(intent)
	if err != nil || original != nil && (original.IntentSHA256 != intentSHA || original.Phase != "provider") {
		return bad
	}
	if original != nil && original.State != "pending" {
		_, err := createNormalTailRecordedStage(ctx, e, stagingGeneralCapacityEntry, "provider", intent, intent, func(ctx context.Context) error {
			if current.Count != want.Count {
				return bad
			}
			return e.Check(ctx)
		})
		return err
	}
	if original == nil {
		if current.Count != len(want.Members) || len(current.Nodes) != len(want.Members) {
			return bad
		}
		if err := qualifyStagingAdditionalWorker(ctx, cfg, token); err != nil {
			return err
		}
		if err := e.BeginEntry(stagingGeneralCapacityEntry, "provider", intent, time.Now().UTC()); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.staging.capacity adding one general worker to the original pool")
		if _, err := environmentCreateProviderRequest(ctx, token, http.MethodPut, path, map[string]int{"count": want.Count}); err != nil {
			return err
		}
		current, err = readPool(ctx)
		if err != nil {
			return err
		}
	}
	if current.Count != want.Count {
		return createProviderRecoveryBlock("staging capacity update requires native readback before the same request can resume")
	}
	if err := e.RecordReceipt(stagingGeneralCapacityEntry, intent); err != nil {
		return err
	}
	if err := e.CompleteEntry(stagingGeneralCapacityEntry, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.capacity original general pool target=3")
	return nil
}

func qualifyStagingAdditionalWorker(ctx context.Context, cfg deploymentConfig, token string) error {
	account, err := readLKEAccountState(cfg.Environment, true)
	limit := account["LKE_ACTIVE_SERVICE_LIMIT"]
	if err != nil || validateActiveServiceLimit(limit) != nil {
		return createProviderRecoveryBlock("staging one-worker capacity increase requires its recorded account quota")
	}
	if limit == "unlimited" {
		return nil
	}
	maximum, _ := strconv.Atoi(limit)
	used := 0
	for _, path := range []string{"/linode/instances", "/volumes", "/nodebalancers"} {
		items, err := environmentCreateProviderList(ctx, token, path)
		if err != nil {
			return err
		}
		used += len(items)
	}
	if used >= maximum {
		return createProviderRecoveryBlock("staging account capacity cannot cover one additional worker")
	}
	return nil
}
