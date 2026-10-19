package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateRedisMaintainedRecipePreservesOriginalOwner(t *testing.T) {
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}
	defs, err := decodeEnvironmentCreateManifests(lkeRedisDeploymentManifest(env))
	if err != nil || len(defs) != 1 {
		t.Fatal("maintained source recipe unavailable", err)
	}
	original := defs[0].Object
	image := "valkey/valkey@sha256:" + strings.Repeat("a", 64)
	deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0])["image"] = image
	before := deploymentplan.Digest(original)
	d := &createManagedLateRedisDurable{ParentSHA: strings.Repeat("b", 64), ResourceID: deploymentplan.ID(original), Image: image, UID: "11111111-1111-4111-8111-111111111111", Original: deploymentplan.Resource{ID: deploymentplan.ID(original), UID: "11111111-1111-4111-8111-111111111111", Desired: original}}
	cfg := deploymentConfig{Values: map[string]string{"REDIS_DATA_STORAGE": "5Gi", "DURABLE_DATA_STORAGE_CLASS": "selected-retain"}}
	claim, helper, switched, err := managedLateRedisRecipes(d, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(original) != before || deploymentplan.ID(switched) != d.ResourceID || d.Original.UID != d.UID {
		t.Fatal("native recipe replaced original owner/body")
	}
	if deploymentplan.Map(switched["spec"])["replicas"] != deploymentplan.Map(original["spec"])["replicas"] || deploymentplan.Map(deploymentplan.Map(switched["spec"])["strategy"])["type"] != "Recreate" {
		t.Fatal("migration changed replica identity or allowed concurrent PVC writers")
	}
	pod := environmentPKIPod(switched)
	volume := deploymentplan.Map(certIssuerObjectList(pod["volumes"])[0])
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	if volume["emptyDir"] != nil || deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] != "redis-data" || container["image"] != image || deploymentplan.Digest(container["args"]) != deploymentplan.Digest([]any{"--dir", "/data", "--appendonly", "yes", "--appendfsync", "everysec"}) {
		t.Fatal("target lost exact data claim/image/AOF restart contract")
	}
	if deploymentplan.Map(claim["metadata"])["namespace"] != "video-cloud-dev-platform" || deploymentplan.Map(claim["metadata"])["name"] != "redis-data" || deploymentplan.Map(claim["spec"])["storageClassName"] != "selected-retain" {
		t.Fatal("target claim left selected namespace/storage")
	}
	hp := deploymentplan.Map(helper["spec"])
	hc := deploymentplan.Map(certIssuerObjectList(hp["containers"])[0])
	if hp["automountServiceAccountToken"] != false || hc["name"] != "redis-migration" || hc["image"] != image || len(certIssuerObjectList(hp["volumes"])) != 1 || deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(hp["volumes"])[0])["persistentVolumeClaim"])["claimName"] != "redis-data" {
		t.Fatal("helper is not isolated on exact target claim/image")
	}
	// Reverse only the documented durable fields; all remaining desired state
	// must still be the independently rendered accepted foundation recipe.
	restored := deploymentplan.Clone(switched)
	delete(deploymentplan.Map(restored["metadata"]), "uid") // explicit reviewed switch owner pin
	delete(deploymentplan.Map(restored["spec"]), "strategy")
	rp := environmentPKIPod(restored)
	rp["volumes"] = environmentPKIPod(original)["volumes"]
	rc := deploymentplan.Map(certIssuerObjectList(rp["containers"])[0])
	delete(rc, "command")
	delete(rc, "args")
	if deploymentplan.Digest(restored) != before {
		t.Fatal("migration introduced unrelated source recipe drift")
	}

	inputs := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("1", 64), ReleaseSHA256: strings.Repeat("2", 64), BindingsSHA256: strings.Repeat("3", 64)}
	plan, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: helper, Phase: "workload"}}, nil)
	if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "create" {
		t.Fatal("actual scoped helper recipe cannot compile", err)
	}
	actual := deploymentplan.Clone(plan.Resources[0].Desired)
	deploymentplan.Map(actual["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
	retired, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: deploymentplan.Projection(actual), Phase: "retirement", Retire: true}}, []deploymentplan.Object{actual})
	if err != nil || len(retired.Resources) != 1 || retired.Resources[0].Action != "retire" || retired.Resources[0].UID != "22222222-2222-4222-8222-222222222222" {
		t.Fatal("exact helper owner cannot compile controlled retirement", err)
	}
	for _, op := range []string{"create", "upgrade"} {
		ordinary := inputs
		ordinary.Operation = op
		if _, err := deploymentplan.Compile(ordinary, []deploymentplan.Definition{{Object: helper, Phase: "workload"}}, nil); err == nil {
			t.Fatal("ordinary target admitted ad-hoc Pod", op)
		}
	}

}

func TestManagedLateRedisZeroReplicaStopRejectsRemainingOwnedPod(t *testing.T) {
	defs, err := decodeEnvironmentCreateManifests(lkeRedisDeploymentManifest(map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}))
	if err != nil {
		t.Fatal(err)
	}
	desired := defs[0].Object
	deploymentplan.Map(desired["spec"])["replicas"] = float64(0)
	uid := "11111111-1111-4111-8111-111111111111"
	rsUID := "22222222-2222-4222-8222-222222222222"
	live := deploymentplan.Clone(desired)
	meta := deploymentplan.Map(live["metadata"])
	meta["uid"] = uid
	meta["generation"] = float64(2)
	live["status"] = deploymentplan.Object{"observedGeneration": float64(2), "replicas": float64(0), "readyReplicas": float64(0), "updatedReplicas": float64(0), "availableReplicas": float64(0)}
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: "video-cloud-dev"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(desired), UID: uid, Action: "retain", Desired: desired}}}
	resolver := func(context.Context, string) (map[string]bool, error) {
		t.Fatal("zero-replica proof queried OCI")
		return nil, nil
	}
	if err := verifyEnvironmentTargetWorkloads(t.Context(), plan, []deploymentplan.Object{live}, resolver); err != nil {
		t.Fatal("genuine stopped owner rejected", err)
	}
	rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-platform", "name": "redis-old", "uid": rsUID, "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "uid": uid, "controller": true}}}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-platform", "name": "redis-old-pod", "uid": "33333333-3333-4333-8333-333333333333", "labels": deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(desired["spec"])["template"])["metadata"])["labels"], "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "uid": rsUID, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(desired)), "status": deploymentplan.Object{"phase": "Running"}}
	if err := verifyEnvironmentTargetWorkloads(t.Context(), plan, []deploymentplan.Object{live, rs, pod}, resolver); err == nil {
		t.Fatal("remaining old process accepted as stopped")
	}
}
