package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeNodeCapacityUsesActualSelectedClusterForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "missing-class", "unready", "ambiguous-ready", "cordoned", "terminating", "wrong-platform", "small-cpu", "small-memory", "invalid-quantity", "duplicate-name", "duplicate-uid", "partial-list", "missing-items", "denied", "cancelled", "missing-intent", "foreign-plan", "spread-floor", "total-cpu", "total-memory"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				config := frozenKubernetesFixture(t)
				store := makeIsolatedTestSecretStore(t, environment)
				raw, _ := json.Marshal(config)
				backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
				if err != nil {
					t.Fatal(err)
				}
				cfg := deploymentConfig{Environment: environment, Values: map[string]string{"NODE_CLASS_LABEL_KEY": "rtk.io/node-class"}, Capacity: sharedCapacityPlan{NodeClasses: map[string]capacityNodeClassPlan{}}}
				var nodes []deploymentplan.Object
				for _, class := range []string{"general", "broker", "database"} {
					cfg.Capacity.NodeClasses[class] = capacityNodeClassPlan{EffectiveCount: 1, UsableCPUMilli: 3000, UsableMemoryMiB: 6144, TotalRequestCPU: 1000, TotalRequestMemory: 2048}
					nodes = append(nodes, deploymentplan.Object{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": class + "-original", "uid": class + "-uid", "labels": map[string]any{"rtk.io/node-class": class, "kubernetes.io/arch": "amd64", "kubernetes.io/os": "linux"}}, "spec": map[string]any{}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}, "allocatable": map[string]any{"cpu": "3900m", "memory": "8000000Ki"}}})
				}
				first := nodes[0]
				metadata, status := deploymentplan.Map(first["metadata"]), deploymentplan.Map(first["status"])
				switch mode {
				case "missing-class":
					delete(deploymentplan.Map(metadata["labels"]), "rtk.io/node-class")
				case "unready":
					deploymentplan.Map(status["conditions"].([]any)[0])["status"] = "False"
				case "ambiguous-ready":
					status["conditions"] = append(status["conditions"].([]any), map[string]any{"type": "Ready", "status": "False"})
				case "cordoned":
					deploymentplan.Map(first["spec"])["unschedulable"] = true
				case "terminating":
					metadata["deletionTimestamp"] = "now"
				case "wrong-platform":
					deploymentplan.Map(metadata["labels"])["kubernetes.io/arch"] = "arm64"
				case "small-cpu":
					deploymentplan.Map(status["allocatable"])["cpu"] = "2"
				case "small-memory":
					deploymentplan.Map(status["allocatable"])["memory"] = "4Gi"
				case "invalid-quantity":
					deploymentplan.Map(status["allocatable"])["cpu"] = "NaN"
				case "duplicate-name":
					deploymentplan.Map(nodes[1]["metadata"])["name"] = metadata["name"]
				case "duplicate-uid":
					deploymentplan.Map(nodes[1]["metadata"])["uid"] = metadata["uid"]
				case "missing-intent":
					delete(cfg.Capacity.NodeClasses, "database")
				case "spread-floor", "total-cpu", "total-memory":
					intent := cfg.Capacity.NodeClasses["general"]
					if mode == "spread-floor" {
						intent.EffectiveCount = 2
					} else if mode == "total-cpu" {
						intent.TotalRequestCPU = 5000
					} else {
						intent.TotalRequestMemory = 9000
					}
					cfg.Capacity.NodeClasses["general"] = intent
				}
				list := map[string]any{"items": nodes}
				if mode == "partial-list" {
					list["metadata"] = map[string]any{"continue": "another-page"}
				} else if mode == "missing-items" {
					delete(list, "items")
				}
				dir := t.TempDir()
				raw, _ = json.Marshal(list)
				writeTestFile(t, filepath.Join(dir, "nodes.json"), string(raw))
				writeTestFile(t, filepath.Join(dir, "kubectl"), `#!/bin/sh
shift 3
test "$*" = 'get nodes -o json' || exit 1
if test "$RTK_NODE_CAPACITY_DENIED" = yes; then printf 'private-provider-output' >&2; exit 1; fi
cat "$RTK_NODE_CAPACITY_LIST"
`)
				if err := os.Chmod(filepath.Join(dir, "kubectl"), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_NODE_CAPACITY_LIST", filepath.Join(dir, "nodes.json"))
				t.Setenv("RTK_NODE_CAPACITY_DENIED", map[bool]string{true: "yes", false: "no"}[mode == "denied"])
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}}
				compiled := &compiledEnvironmentDeployment{Plan: plan, Configuration: cfg, Store: store, Backend: backend}
				requested := plan
				if mode == "foreign-plan" {
					copy := *plan
					copy.Environment = "other"
					requested = &copy
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				err = compiled.CheckNodeCapacity(ctx, requested)
				if (err == nil) != (mode == "valid") {
					t.Fatal("incorrect native capacity decision", mode, err)
				}
				if err != nil {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.node-capacity" || blocker.Next == "" || strings.Contains(err.Error(), "private-provider-output") {
						t.Fatal("capacity failure lost its stable repair or leaked private input", err)
					}
				}
			})
		}
	}
}
