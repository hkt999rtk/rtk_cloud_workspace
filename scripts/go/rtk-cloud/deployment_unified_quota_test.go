package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeProviderInventoryRequiresAllPagesAndStableUniqueIDs(t *testing.T) {
	for _, mode := range []string{"valid", "missing-pages", "missing-count", "duplicate", "changed-count", "missing-items", "denied"} {
		t.Run(mode, func(t *testing.T) {
			first := `{"data":[{"id":1}],"page":1,"pages":2,"results":2}`
			second := `{"data":[{"id":2}],"page":2,"pages":2,"results":2}`
			switch mode {
			case "missing-pages":
				first = `{"data":[{"id":1}],"page":1,"results":1}`
			case "missing-count":
				first = `{"data":[],"page":1,"pages":1}`
			case "duplicate":
				second = `{"data":[{"id":1}],"page":2,"pages":2,"results":2}`
			case "changed-count":
				second = `{"data":[{"id":2}],"page":2,"pages":2,"results":3}`
			case "missing-items":
				second = `{"data":[],"page":2,"pages":2,"results":2}`
			case "denied":
				first = "__ERROR_403__"
			}
			log := fakeDeploymentPreflightLKERead(t, map[string]string{"/volumes?page_size=500&page=1": first, "/volumes?page_size=500&page=2": second})
			count, err := completeProviderResourceCount(context.Background(), "private-provider-token", "/volumes")
			if (err == nil) != (mode == "valid") || mode == "valid" && count != 2 {
				t.Fatal("incomplete native inventory was counted", count, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-provider-token") {
				t.Fatal("provider token leaked")
			}
			if mode == "valid" && !strings.Contains(readTestFile(t, log), "page=2") {
				t.Fatal("later native pages were not inspected")
			}
		})
	}
}

func TestCompleteQuotaCountsCandidateClaimsAndNeverCreditsUnexecutedDeletion(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "too-small", "unlimited", "missing-quota", "later-page-denied"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				if err := store.write("operator/env/LINODE_TOKEN", []byte("private-provider-token"), true); err != nil {
					t.Fatal(err)
				}
				quota := "7"
				if mode == "too-small" {
					quota = "6"
				}
				if mode == "unlimited" {
					quota = "unlimited"
				}
				if mode != "missing-quota" {
					writeTestLKEAccount(t, environment, "LKE_ACTIVE_SERVICE_LIMIT="+quota+"\n")
				}
				namespace := "video-cloud-" + environment + "-platform"
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}, Resources: []deploymentplan.Resource{
					{ID: "PersistentVolumeClaim/" + namespace + "/created", Action: "create", Desired: deploymentplan.Object{"kind": "PersistentVolumeClaim", "spec": deploymentplan.Object{}}},
					{ID: "PersistentVolumeClaim/" + namespace + "/data-db-0", Action: "retain", Desired: deploymentplan.Object{"kind": "PersistentVolumeClaim"}},
					{ID: "StatefulSet/" + namespace + "/db", Action: "update", Desired: deploymentplan.Object{"kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": namespace, "name": "db"}, "spec": deploymentplan.Object{"replicas": float64(2), "volumeClaimTemplates": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "data"}}}}}},
					{ID: "Service/" + namespace + "/edge", Action: "create", Desired: deploymentplan.Object{"kind": "Service", "spec": deploymentplan.Object{"type": "LoadBalancer"}}},
					{ID: "Service/" + namespace + "/retired", Action: "retire", Desired: deploymentplan.Object{"kind": "Service", "spec": deploymentplan.Object{"type": "LoadBalancer"}}},
				}}
				c := &compiledEnvironmentDeployment{Plan: plan, Store: store}
				responses := map[string]string{}
				for _, endpoint := range []string{"/linode/instances", "/volumes", "/nodebalancers"} {
					responses[endpoint+"?page_size=500&page=1"] = `{"data":[{"id":1}],"page":1,"pages":1,"results":1}`
				}
				responses["/linode/instances?page_size=500&page=1"] = `{"data":[{"id":1}],"page":1,"pages":2,"results":2}`
				responses["/linode/instances?page_size=500&page=2"] = `{"data":[{"id":2}],"page":2,"pages":2,"results":2}`
				if mode == "later-page-denied" {
					responses["/linode/instances?page_size=500&page=2"] = "__ERROR_403__"
				}
				log := fakeDeploymentPreflightLKERead(t, responses)
				err := c.CheckProviderQuota(context.Background(), plan)
				allowed := mode == "valid" || mode == "unlimited"
				if (err == nil) != allowed {
					t.Fatal("wrong exact-plan quota decision", err)
				}
				if err != nil && strings.Contains(err.Error(), "private-provider-token") {
					t.Fatal("provider token exposed")
				}
				if raw, _ := os.ReadFile(log); strings.Contains(string(raw), "DELETE ") || strings.Contains(string(raw), "POST ") {
					t.Fatal("read-only quota qualification changed native resources")
				}
			})
		}
	}
}

func TestKubernetesUpgradeIgnoresProviderStateAndQuotaFiles(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, _, _ := maintainedDeploymentSnapshotFixture(t, environment)
			before, err := readEnvironmentDeploymentSnapshot(cfg, store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(store.Root, "deployment", "adapters", "lke", "state.env")); !os.IsNotExist(err) {
				t.Fatal("snapshot required provider state")
			}
			for name, content := range map[string]string{"state.env": "invalid obsolete cluster metadata", "account.env": "invalid obsolete quota metadata"} {
				if err := store.write(filepath.Join("deployment", "adapters", "lke", name), []byte(content), true); err != nil {
					t.Fatal(err)
				}
			}
			after, err := readEnvironmentDeploymentSnapshot(cfg, store)
			if err != nil || before.Digest(cfg) != after.Digest(cfg) {
				t.Fatal("Kubernetes inputs depend on unrelated provider metadata", err)
			}
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Operation: "upgrade"}}
			c := &compiledEnvironmentDeployment{Plan: plan, Store: store}
			log := fakeDeploymentPreflightLKERead(t, map[string]string{})
			if err := c.CheckProviderQuota(context.Background(), plan); err != nil {
				t.Fatal("Kubernetes upgrade required Linode quota", err)
			}
			if data, _ := os.ReadFile(log); len(data) != 0 {
				t.Fatal("Kubernetes update contacted provider inventory")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if c.CheckProviderQuota(ctx, plan) == nil {
				t.Fatal("cancelled qualification passed")
			}
		})
	}
}
