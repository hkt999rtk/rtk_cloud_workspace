package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentWorkerName struct {
	PoolID     int    `json:"pool_id"`
	InstanceID int    `json:"instance_id"`
	NodeClass  string `json:"node_class"`
	Ordinal    int    `json:"ordinal"`
	Label      string `json:"label"`
}
type environmentWorkerNames struct {
	Schema      string                  `json:"schema"`
	Environment string                  `json:"environment"`
	Stack       string                  `json:"stack"`
	ClusterID   string                  `json:"cluster_id"`
	Entries     []environmentWorkerName `json:"entries"`
}

func environmentWorkerDisplayName(stack, class string, ordinal int) (string, error) {
	if !keySet("general", "broker", "database")[class] || ordinal < 1 || ordinal > 9999 {
		return "", createProviderBlocker("exact logical class and unique positive worker ordinal are required")
	}
	name := fmt.Sprintf("%s-lke-%s-%02d", stack, class, ordinal)
	if len(name) > 64 {
		return "", createProviderBlocker("readable worker name exceeds provider limit")
	}
	return name, nil
}

// Fresh allocation owns its exact pool members. Persist the ID/ordinal binding
// before renaming display labels; a partial naming intent is never auto-replayed.
func initializeEnvironmentCreateWorkerNames(ctx context.Context, cfg deploymentConfig, store secretStore, operator map[string]string, cluster environmentCreateProviderReceipt, e *environmentCreateExecution) error {
	if cluster.Kind != "lke" || cluster.Name != cfg.Values["CLOUD_STACK_NAME"]+"-lke" {
		return createProviderBlocker("original selected cluster receipt required for worker names")
	}
	path, err := store.safePath("deployment/adapters/lke/worker-labels.json")
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return createProviderBlocker("worker ordinal bindings already exist; explicit naming recovery required")
	}
	pools, err := environmentCreateProviderList(ctx, operator["LINODE_TOKEN"], "/lke/clusters/"+cluster.NativeID+"/pools")
	if err != nil {
		return err
	}
	bindings := environmentWorkerNames{Schema: "rtk-cloud.lke-worker-labels/v1", Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ClusterID: cluster.NativeID}
	seen := map[int]bool{}
	classes := map[string]bool{}
	for _, rawPool := range pools {
		var pool deploymentplan.Object
		if json.Unmarshal(rawPool, &pool) != nil {
			return createProviderBlocker("native pool identity unavailable")
		}
		class := deploymentplan.String(deploymentplan.Map(pool["labels"])["rtk.io/node-class"])
		poolID := int(workerNativeIDNumber(pool["id"]))
		if poolID <= 0 || classes[class] || pool["label"] != bindings.Stack+"-"+class {
			return createProviderBlocker("fresh named pool is ambiguous or lacks environment identity")
		}
		classes[class] = true
		var ids []int
		for _, node := range createProviderObjectList(pool["nodes"]) {
			id := int(workerNativeIDNumber(node["instance_id"]))
			if id <= 0 || seen[id] {
				return createProviderBlocker("fresh worker native identity is invalid or duplicated")
			}
			seen[id] = true
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for index, id := range ids {
			name, err := environmentWorkerDisplayName(bindings.Stack, class, index+1)
			if err != nil {
				return err
			}
			bindings.Entries = append(bindings.Entries, environmentWorkerName{poolID, id, class, index + 1, name})
		}
	}
	if len(bindings.Entries) == 0 {
		return createProviderBlocker("fresh cluster has no verified worker members")
	}
	all, err := environmentCreateProviderList(ctx, operator["LINODE_TOKEN"], "/linode/instances")
	if err != nil {
		return err
	}
	for _, item := range bindings.Entries {
		for _, rawInstance := range all {
			var instance deploymentplan.Object
			if json.Unmarshal(rawInstance, &instance) != nil {
				return createProviderBlocker("native instance inventory unavailable")
			}
			if instance["label"] == item.Label && int(workerNativeIDNumber(instance["id"])) != item.InstanceID {
				return createProviderBlocker("readable worker label is already owned by another instance")
			}
		}
	}
	intent, _ := json.Marshal(bindings)
	if err := e.BeginEntry("provider-worker-names", "provider", intent, time.Now().UTC()); err != nil {
		return err
	}
	if err := store.write("deployment/adapters/lke/worker-labels.json", intent, false); err != nil {
		return err
	}
	for _, item := range bindings.Entries {
		if err := e.Check(ctx); err != nil {
			return err
		}
		path := "/linode/instances/" + strconv.Itoa(item.InstanceID)
		raw, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], "GET", path, nil)
		if err != nil {
			return err
		}
		var before deploymentplan.Object
		if json.Unmarshal(raw, &before) != nil || int(workerNativeIDNumber(before["id"])) != item.InstanceID {
			return createProviderBlocker("original allocated worker cannot be read")
		}
		key := fmt.Sprintf("worker-label-%d", item.InstanceID)
		request := map[string]any{"instance_id": item.InstanceID, "old_label": before["label"], "label": item.Label, "pool_id": item.PoolID}
		if err := e.BeginEntry(key, "provider", mustProviderRecoveryJSON(request), time.Now().UTC()); err != nil {
			return err
		}
		if before["label"] != item.Label {
			if _, err := environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], "PUT", path, map[string]string{"label": item.Label}); err != nil {
				return err
			}
		}
		raw, err = environmentCreateProviderRequest(ctx, operator["LINODE_TOKEN"], "GET", path, nil)
		if err != nil {
			return err
		}
		var after deploymentplan.Object
		if json.Unmarshal(raw, &after) != nil || int(workerNativeIDNumber(after["id"])) != item.InstanceID || after["label"] != item.Label || deploymentplan.Digest(after["ipv4"]) != deploymentplan.Digest(before["ipv4"]) {
			return createProviderBlocker("worker display-label readback changed original allocation")
		}
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(request)); err != nil {
			return err
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := e.RecordReceipt("provider-worker-names", intent); err != nil {
		return err
	}
	return e.CompleteEntry("provider-worker-names", time.Now().UTC())
}

func workerNativeIDNumber(v any) float64 {
	n, ok := v.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 9007199254740991 || math.Trunc(n) != n {
		return 0
	}
	return n
}
