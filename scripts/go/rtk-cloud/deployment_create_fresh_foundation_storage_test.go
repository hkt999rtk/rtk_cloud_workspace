package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFreshFoundationRedisStartsWithTheMaintainedDurableRecipe(t *testing.T) {
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev", "REDIS_DATA_STORAGE": "5Gi", "PROMETHEUS_DATA_STORAGE": "20Gi", "DURABLE_DATA_STORAGE_CLASS": "linode-block-storage-retain"}
	// Only storage/identity shape is inspected; this isolated static renderer does
	// not initialize a canonical store or ask a Kubernetes API to allocate data.
	historical, err := renderEnvironmentCreateFoundationBase(env, false)
	if err != nil {
		t.Fatal(err)
	}
	oldSHA := deploymentplan.Digest(historical)
	fresh, err := renderEnvironmentCreateFoundationBase(env, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = bindEnvironmentNewStorageClasses(fresh, env["DURABLE_DATA_STORAGE_CLASS"], nil); err != nil {
		t.Fatal(err)
	}
	byID := map[string]deploymentplan.Definition{}
	for _, d := range fresh {
		byID[deploymentplan.ID(d.Object)] = d
	}
	deployment := byID["Deployment/video-cloud-dev-platform/redis"].Object
	spec := deploymentplan.Map(deployment["spec"])
	pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
	if deploymentplan.Map(spec["strategy"])["type"] != "Recreate" {
		t.Fatal("first Redis start did not use one-writer strategy")
	}
	var claimFound, argsFound bool
	for _, v := range certIssuerObjectList(pod["volumes"]) {
		volume := deploymentplan.Map(v)
		if volume["name"] == "data" {
			claimFound = deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] == "redis-data" && volume["emptyDir"] == nil
		}
	}
	for _, v := range certIssuerObjectList(pod["containers"]) {
		c := deploymentplan.Map(v)
		if c["name"] == "redis" {
			argsFound = deploymentplan.Digest(c["args"]) == deploymentplan.Digest([]any{"--dir", "/data", "--appendonly", "yes", "--appendfsync", "everysec"})
		}
	}
	claim := byID["PersistentVolumeClaim/video-cloud-dev-platform/redis-data"]
	if !claimFound || !argsFound || claim.Object == nil || deploymentplan.Map(claim.Object["spec"])["storageClassName"] != env["DURABLE_DATA_STORAGE_CLASS"] {
		t.Fatal("fresh Redis could write ephemeral state before its maintained durable allocation")
	}
	if deploymentplan.Digest(historical) != oldSHA {
		t.Fatal("fresh rendering changed original recipe")
	}
	again, err := renderEnvironmentCreateFoundationBase(env, false)
	if err != nil || deploymentplan.Digest(again) != oldSHA {
		t.Fatal("historical recipe changed", err)
	}
	// Omitted false preserves historical wire bytes; true changes reviewed intent.
	legacy := environmentCreatePlan{Schema: "rtk-cloud.create-plan/v1"}
	raw, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(raw), "durable_foundation") {
		t.Fatal("old create plan wire changed", err)
	}
	legacy.DurableFoundation = true
	raw, err = json.Marshal(legacy)
	if err != nil || !strings.Contains(string(raw), `"durable_foundation":true`) {
		t.Fatal("new foundation intent was not frozen", err)
	}
}
