package main

import (
	"context"
	"encoding/json"
	"math"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The maintained class plan is a necessary lower bound. Native Ready nodes and
// allocatable resources must support it; this does not simulate the scheduler,
// credit planned provider changes, or qualify rollout surge/new-volume placement.
func (c *compiledEnvironmentDeployment) CheckNodeCapacity(ctx context.Context, plan *deploymentplan.Plan) error {
	if c == nil || plan == nil || c.Plan == nil || c.Plan.SHA256() != plan.SHA256() || c.Store.Environment != plan.Environment {
		return nodeCapacityBlocker("", "exact selected plan and environment required")
	}
	raw, err := c.Backend.command(ctx, nil, "get", "nodes", "-o", "json")
	var list struct {
		Items    []deploymentplan.Object `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	if err != nil || json.Unmarshal(raw, &list) != nil || list.Items == nil || list.Metadata.Continue != "" {
		return nodeCapacityBlocker("", "complete native node inventory is unavailable")
	}
	return verifyNativeNodeClassCapacity(c.Configuration, list.Items)
}

func verifyNativeNodeClassCapacity(cfg deploymentConfig, nodes []deploymentplan.Object) error {
	label := cfg.Values["NODE_CLASS_LABEL_KEY"]
	if label == "" || len(cfg.Capacity.NodeClasses) == 0 {
		return nodeCapacityBlocker("", "maintained node class capacity intent is unavailable")
	}
	type capacity struct{ count, cpu, memory int }
	available := map[string]capacity{}
	names, uids := map[string]bool{}, map[string]bool{}
	for _, node := range nodes {
		metadata := deploymentplan.Map(node["metadata"])
		name, uid := deploymentplan.String(metadata["name"]), deploymentplan.String(metadata["uid"])
		if node["kind"] != "Node" || name == "" || uid == "" || names[name] || uids[uid] {
			return nodeCapacityBlocker("", "native node identity is absent or duplicated")
		}
		names[name], uids[uid] = true, true
		labels := deploymentplan.Map(metadata["labels"])
		class := deploymentplan.String(labels[label])
		intent, selected := cfg.Capacity.NodeClasses[class]
		if !selected || intent.EffectiveCount == 0 {
			continue
		}
		if metadata["deletionTimestamp"] != nil || deploymentplan.Map(node["spec"])["unschedulable"] == true || labels["kubernetes.io/arch"] != "amd64" || labels["kubernetes.io/os"] != "linux" {
			continue
		}
		ready, readyConditions := false, 0
		for _, value := range certIssuerObjectList(deploymentplan.Map(node["status"])["conditions"]) {
			condition := deploymentplan.Map(value)
			if condition["type"] == "Ready" {
				readyConditions++
				ready = condition["status"] == "True"
			}
		}
		if readyConditions > 1 {
			return nodeCapacityBlocker("", "native node Ready condition is ambiguous")
		}
		if !ready {
			continue
		}
		allocatable := deploymentplan.Map(deploymentplan.Map(node["status"])["allocatable"])
		cpu, cpuErr := parseCPUQuantity(deploymentplan.String(allocatable["cpu"]))
		memory, memErr := parseMemoryMi(deploymentplan.String(allocatable["memory"]))
		// Bounds also reject non-finite/overflowed quantities from native input.
		if cpuErr != nil || memErr != nil || cpu <= 0 || memory <= 0 || cpu > math.MaxInt32 || memory > math.MaxInt32 {
			return nodeCapacityBlocker("", "native node allocatable CPU or memory is invalid")
		}
		if cpu < intent.UsableCPUMilli || memory < intent.UsableMemoryMiB {
			continue
		}
		current := available[class]
		if current.cpu > math.MaxInt-cpu || current.memory > math.MaxInt-memory {
			return nodeCapacityBlocker(class, "native node capacity exceeds its supported bound")
		}
		current.count++
		current.cpu += cpu
		current.memory += memory
		available[class] = current
	}
	for _, class := range []string{"general", "broker", "database"} {
		intent, exists := cfg.Capacity.NodeClasses[class]
		if !exists || intent.EffectiveCount < 0 || intent.UsableCPUMilli <= 0 || intent.UsableMemoryMiB <= 0 {
			return nodeCapacityBlocker(class, "maintained class capacity is incomplete")
		}
		current := available[class]
		if current.count < intent.EffectiveCount || current.cpu < intent.TotalRequestCPU || current.memory < intent.TotalRequestMemory {
			return nodeCapacityBlocker(class, "native Ready linux/amd64 node count or allocatable resources do not meet the maintained class plan")
		}
	}
	return nil
}

func nodeCapacityBlocker(class, cause string) error {
	resource := ""
	if class != "" {
		resource = "node-class/" + strings.TrimSpace(class)
	}
	return &deploymentplan.Blocker{ID: "deployment.execution.node-capacity", Resource: resource, Cause: cause, Next: "restore selected-cluster node list access; reconcile the maintained node classes, Ready state and required linux/amd64 resources through the reviewed provider plan, then repeat preflight; configured SKU/count alone is not live capacity evidence"}
}
