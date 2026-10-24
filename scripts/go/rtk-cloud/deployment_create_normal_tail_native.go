package main

import (
	"context"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Migration proof concerns the historical snapshot. Current health observes
// the live durable writer and never demands that legitimate later writes still
// equal the old three-key dataset, RunID, or source Pod.
func verifyCreateNormalTailRedis(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, b environmentKubernetesBackend) error {
	bad := func(stage string) error {
		return createProviderRecoveryBlock("completed Redis current health: " + stage)
	}
	if h == nil || h.Execution == nil || h.Execution.Store != store || b.Store != store || h.Execution.Check(ctx) != nil {
		return bad("owner")
	}
	e := h.Execution
	var saved managedLateRedisDurableSaved
	if createOpenBaoACKCompletionRead(store, filepath.Join(managedLateRedisDurableBase(e.Journal.PlanSHA256), "source.json"), &saved) != nil || deploymentplan.Digest(saved) != h.Pins["source"] {
		return bad("immutable source")
	}
	d := &saved.Descriptor
	current, err := b.Read(ctx, d.ResourceID)
	if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != d.UID {
		return bad("Deployment owner")
	}
	want := d.Switch
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload"); entry != nil {
		frozen, err := readCreateNormalTailFrozenTarget(store, e)
		if err != nil {
			return err
		}
		child, j, err := readCreateNormalTailWorkload(store, e, frozen)
		if err != nil {
			return err
		}
		if child != nil && j.Owners[d.ResourceID] != "" {
			if j.Owners[d.ResourceID] != d.UID {
				return bad("workload replacement")
			}
			for _, resource := range child.Resources {
				if resource.ID == d.ResourceID {
					want = resource.Desired
				}
			}
		}
	}
	if !deploymentplan.Matches(want, current) {
		return bad("Deployment desired state")
	}
	volume, err := managedLateRedisVolume(ctx, b, d, h.RedisVolume.PVCUID)
	if err != nil || volume != h.RedisVolume {
		return bad("PVC/PV identity")
	}
	helper, err := b.Read(ctx, deploymentplan.ID(d.Helper))
	if err != nil || helper != nil {
		return bad("retired helper presence")
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return err
	}
	pod, _, err := managedLateRedisSourcePod(current, objects)
	if err != nil || !managedLateRedisDataPodMatches(pod, d.Image) {
		return bad("current PVC-backed Pod")
	}
	podUID := deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	podID := deploymentplan.ID(pod)
	if err := verifyEnvironmentTargetWorkloads(ctx, &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped"}, Resources: []deploymentplan.Resource{{ID: d.ResourceID, UID: d.UID, Action: "retain", Desired: want}}}, objects, func(ctx context.Context, image string) (map[string]bool, error) {
		return environmentRuntimeImageDigests(ctx, store, image)
	}); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := e.Check(bounded); err != nil {
		return err
	}
	raw, err := b.command(bounded, nil, "exec", "--namespace", volume.Namespace, deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), "-c", "redis", "--", "valkey-cli", "--raw", "INFO", "persistence")
	if err != nil {
		return bad("persistence read")
	}
	info, err := managedLateRedisInfo(raw)
	if err != nil || info["loading"] != "0" || info["aof_enabled"] != "1" || info["aof_last_bgrewrite_status"] != "ok" || info["aof_last_write_status"] != "ok" {
		return bad("AOF durability")
	}
	actualPod, err := b.Read(bounded, podID)
	if err != nil || actualPod == nil || deploymentplan.Map(actualPod["metadata"])["uid"] != podUID {
		return bad("Pod changed during observation")
	}
	return e.Check(bounded)
}
