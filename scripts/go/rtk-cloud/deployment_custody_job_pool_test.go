package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyJobPoolReusesScopeAndReadsFreshNativeState(t *testing.T) {
	_, store, _, _ := createStateFixture(t, "dev")
	fixture := t.TempDir()
	installCustodyInspectionProcessFixture(t, fixture, "public-success")
	pool := &environmentCustodyJobPool{}
	backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("original-isolated-context"), RolloutTimeout: 2 * time.Second, custodyJobs: pool}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	ns := "video-cloud-dev-video-cloud"
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "public-custody", "namespace": ns}, "spec": deploymentplan.Object{"activeDeadlineSeconds": float64(2), "ttlSecondsAfterFinished": float64(2), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{}}, "spec": deploymentplan.Object{"serviceAccountName": "original-reader", "restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "inspect", "image": "ghcr.io/fixture/native@sha256:" + strings.Repeat("a", 64), "command": []any{"/bin/sh", "-ec", "sleep 60"}}}}}}}
	args := []string{"inspect-public-crl", "/memory/root.json", "11111111-1111-4111-8111-111111111111", strings.Repeat("b", 64)}
	for _, value := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(fixture, "public.json"), []byte(`{"native_observation":"`+value+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
		raw, err := runEnvironmentCustodyJob(ctx, backend, job, ns, args, nil)
		if err != nil || !strings.Contains(string(raw), value) {
			t.Fatal("reuse substituted a cached result for the current native RPC", err)
		}
	}
	created, err := os.ReadFile(filepath.Join(fixture, "creates"))
	if err != nil || string(created) != "1" {
		t.Fatalf("same-scope RPCs created %s Jobs instead of reusing one: %v", created, err)
	}
	recipes, err := os.ReadFile(filepath.Join(fixture, "created-recipes.jsonl"))
	var actual deploymentplan.Object
	if err != nil || json.Unmarshal([]byte(strings.TrimSpace(string(recipes))), &actual) != nil {
		t.Fatal("one native helper recipe required", err)
	}
	if deploymentplan.Map(actual["spec"])["activeDeadlineSeconds"].(float64) <= 2 {
		t.Fatal("pooled helper lifetime used the startup timeout instead of the caller deadline")
	}
	cancel()
	if _, err := runEnvironmentCustodyJob(context.Background(), backend, job, ns, args, nil); err == nil {
		t.Fatal("another context reset the original operation's cancelled lifetime")
	}
	if err := pool.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(fixture, "job.json")); !os.IsNotExist(err) {
		t.Fatal("operation retained its original helper after cleanup", err)
	}
}

func TestCustodyJobPoolSeparatesScopesAndProtectsReplacementUIDs(t *testing.T) {
	_, store, _, _ := createStateFixture(t, "dev")
	fixture := t.TempDir()
	installCustodyInspectionProcessFixture(t, fixture, "public-pooled")
	if err := os.WriteFile(filepath.Join(fixture, "public.json"), []byte(`{"native_observation":"current"}`), 0600); err != nil {
		t.Fatal(err)
	}
	pool := &environmentCustodyJobPool{}
	backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("original-isolated-context"), RolloutTimeout: 3 * time.Second, custodyJobs: pool}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	ns := "video-cloud-dev-video-cloud"
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "public-custody", "namespace": ns}, "spec": deploymentplan.Object{"activeDeadlineSeconds": float64(3), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{}}, "spec": deploymentplan.Object{"serviceAccountName": "original-reader", "restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "inspect", "image": "ghcr.io/fixture/native@sha256:" + strings.Repeat("a", 64), "command": []any{"/bin/sh", "-ec", "sleep 60"}}}}}}}
	args := []string{"inspect-public-crl", "/memory/root.json", "11111111-1111-4111-8111-111111111111", strings.Repeat("b", 64)}
	if _, err := runEnvironmentCustodyJob(ctx, backend, job, ns, args, nil); err != nil {
		t.Fatal(err)
	}
	original := deploymentplan.Clone(job)
	environmentPKIPod(job)["serviceAccountName"] = "another-reader"
	if _, err := runEnvironmentCustodyJob(ctx, backend, job, ns, args, nil); err != nil {
		t.Fatal("separate scope", err)
	}
	if _, err := runEnvironmentCustodyJob(ctx, backend, original, ns, args, nil); err != nil {
		t.Fatal("original scope was overwritten by another helper", err)
	}
	created, err := os.ReadFile(filepath.Join(fixture, "creates"))
	if err != nil || string(created) != "2" {
		t.Fatal("different service accounts reused the same native scope", err)
	}
	recipes, err := os.ReadFile(filepath.Join(fixture, "created-recipes.jsonl"))
	var first deploymentplan.Object
	if err != nil || json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(recipes)), "\n")[0]), &first) != nil {
		t.Fatal(err)
	}
	name := deploymentplan.String(deploymentplan.Map(first["metadata"])["name"])
	path := filepath.Join(fixture, "pooled-"+name+"-job.json")
	metadata := deploymentplan.Map(first["metadata"])
	metadata["uid"] = "99999999-9999-4999-8999-999999999999"
	if err := os.WriteFile(path, mustProviderRecoveryJSON(first), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runEnvironmentCustodyJob(ctx, backend, original, ns, args, nil); err == nil {
		t.Fatal("reuse adopted a replacement Job UID")
	}
	if err := pool.Close(context.Background()); err == nil {
		t.Fatal("cleanup reported success for a replacement owner")
	}
	retained, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(retained), "99999999-9999-4999-8999-999999999999") {
		t.Fatal("cleanup removed the replacement Job", err)
	}
	remaining, err := filepath.Glob(filepath.Join(fixture, "pooled-*-job.json"))
	if err != nil || len(remaining) != 1 {
		t.Fatal("cleanup failed to remove the other original owned helper", err)
	}
}
