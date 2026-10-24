package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestHistoricalNativeObservationPreservesControllerLineageAcrossRollouts(t *testing.T) {
	controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": "isolated", "name": "certissuer", "uid": "77777777-7777-4777-8777-777777777777"}, "spec": deploymentplan.Object{"replicas": float64(2), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "certissuer"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "certissuer", "image": "isolated:v1"}}}}}}
	original := map[string]deploymentplan.Object{deploymentplan.ID(controller): controller}
	before := deploymentplan.Digest(original)
	observed := historicalNativeInventory(original)
	if deploymentplan.Digest(original) != before || deploymentplan.Digest(observed) != deploymentplan.Digest(historicalNativeInventory(original)) {
		t.Fatal("observing native controllers mutated or randomized original owners")
	}
	rs := observed["ReplicaSet/isolated/certissuer-isolated"]
	pod := observed["Pod/isolated/certissuer-pod"]
	second := observed["Pod/isolated/certissuer-pod-1"]
	seen := map[string]bool{}
	for _, object := range []deploymentplan.Object{controller, rs, pod, second} {
		uid := deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
		if !environmentBootstrapOwnerUUID.MatchString(uid) || seen[uid] {
			t.Fatal("native observation lacks an independent UUID", deploymentplan.ID(object))
		}
		seen[uid] = true
	}
	owner := func(object deploymentplan.Object) string {
		return deploymentplan.String(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(object["metadata"])["ownerReferences"])[0])["uid"])
	}
	if owner(rs) != deploymentplan.Map(controller["metadata"])["uid"] || owner(pod) != deploymentplan.Map(rs["metadata"])["uid"] || owner(second) != owner(pod) {
		t.Fatal("native observation lost Pod to ReplicaSet to Deployment custody")
	}
	rollout := deploymentplan.Clone(controller)
	deploymentplan.Map(certIssuerObjectList(environmentPKIPod(rollout)["containers"])[0])["image"] = "isolated:v2"
	next := historicalNativeInventory(map[string]deploymentplan.Object{deploymentplan.ID(rollout): rollout})
	if deploymentplan.Map(next[deploymentplan.ID(rs)]["metadata"])["uid"] == deploymentplan.Map(rs["metadata"])["uid"] || deploymentplan.Map(next[deploymentplan.ID(pod)]["metadata"])["uid"] == deploymentplan.Map(pod["metadata"])["uid"] || owner(next[deploymentplan.ID(rs)]) != owner(rs) {
		t.Fatal("rollout did not replace only its derived native owners")
	}
	deploymentplan.Map(rollout["spec"])["replicas"] = float64(0)
	zero := historicalNativeInventory(map[string]deploymentplan.Object{deploymentplan.ID(rollout): rollout})
	for _, object := range zero {
		if object["kind"] == "Pod" {
			t.Fatal("scaled-down controller retained a ready Pod observation")
		}
	}
}

func TestHistoricalNativeObservationReconcilesStatefulSetGeneration(t *testing.T) {
	set := deploymentplan.Object{
		"apiVersion": "apps/v1",
		"kind":       "StatefulSet",
		"metadata":   deploymentplan.Object{"namespace": "isolated", "name": "openbao", "uid": "77777777-7777-4777-8777-777777777777", "generation": float64(4)},
		"spec":       deploymentplan.Object{"replicas": float64(1), "updateStrategy": deploymentplan.Object{"type": "OnDelete"}},
		"status":     deploymentplan.Object{"observedGeneration": float64(2), "readyReplicas": float64(1)},
	}
	pod := deploymentplan.Object{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": deploymentplan.Object{
			"namespace": "isolated", "name": "openbao-0", "uid": "88888888-8888-4888-8888-888888888888",
			"ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": "77777777-7777-4777-8777-777777777777", "controller": true}},
		},
		"status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}},
	}
	original := map[string]deploymentplan.Object{deploymentplan.ID(set): set, deploymentplan.ID(pod): pod}
	observed := historicalNativeInventory(original)
	if deploymentplan.Map(deploymentplan.Map(original[deploymentplan.ID(set)])["status"])["observedGeneration"] != float64(2) {
		t.Fatal("native observation mutated the original StatefulSet")
	}
	actual := observed[deploymentplan.ID(set)]
	status := deploymentplan.Map(actual["status"])
	if deploymentplan.Map(actual["metadata"])["generation"] != float64(4) || status["observedGeneration"] != float64(4) || status["replicas"] != float64(1) || status["currentReplicas"] != float64(1) || status["readyReplicas"] != float64(1) || status["availableReplicas"] != float64(1) {
		t.Fatal("native StatefulSet controller did not reconcile generation and Ready Pod cardinality")
	}
	cloneInventory := func() map[string]deploymentplan.Object {
		cloned := map[string]deploymentplan.Object{}
		for id, object := range original {
			cloned[id] = deploymentplan.Clone(object)
		}
		return cloned
	}
	pending := cloneInventory()
	deploymentplan.Map(deploymentplan.Map(pending[deploymentplan.ID(pod)])["status"])["phase"] = "Pending"
	pendingStatus := deploymentplan.Map(historicalNativeInventory(pending)[deploymentplan.ID(set)]["status"])
	if pendingStatus["replicas"] != float64(1) || pendingStatus["readyReplicas"] != float64(0) || pendingStatus["availableReplicas"] != float64(0) {
		t.Fatal("native StatefulSet observer manufactured readiness for a Pending Pod")
	}
	scaled := cloneInventory()
	delete(scaled, deploymentplan.ID(pod))
	scaledStatus := deploymentplan.Map(historicalNativeInventory(scaled)[deploymentplan.ID(set)]["status"])
	if scaledStatus["replicas"] != float64(0) || scaledStatus["currentReplicas"] != float64(0) || scaledStatus["readyReplicas"] != float64(0) || scaledStatus["availableReplicas"] != float64(0) {
		t.Fatal("native StatefulSet observer retained replicas after the owned Pod disappeared")
	}
}

func TestHistoricalSerialInterruptionRetainsOnlyObservedNativeProgress(t *testing.T) {
	for _, beforeWait := range []bool{false, true} {
		name := "readiness-failure"
		if beforeWait {
			name = "process-loss-before-readiness"
		}
		t.Run(name, func(t *testing.T) {
			cfg, store, e, _, original := providerExecutionContractFixture(t)
			f := &acceptedCoreExecutionContract{cfg: cfg, store: store, e: e, p: createFoundationRecoveryPlan{Plan: original}}
			f.native = &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}
			defs := []deploymentplan.Definition{}
			for _, resource := range []struct{ name, phase string }{{"before", "configuration"}, {"failed", "workload"}, {"later", "workload"}} {
				defs = append(defs, deploymentplan.Definition{Phase: resource.phase, Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": resource.name}, "data": deploymentplan.Object{"observed": resource.name}}})
			}
			failed := deploymentplan.ID(defs[1].Object)
			plan, journal := f.historicalSerialInterruptedStage(t, "kubernetes/isolated-historical-interruption", defs, failed, beforeWait)
			if journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || len(journal.Completed) != 1 || len(journal.Owners) != 2 || journal.Completed[0] != deploymentplan.ID(defs[0].Object) || f.native.writes[failed] != 1 || f.native.objects[deploymentplan.ID(defs[2].Object)] != nil {
				t.Fatal("interruption changed actual durable progress", journal)
			}
			if (journal.Failed == "") != beforeWait || !beforeWait && journal.Failed != failed {
				t.Fatal("unobserved readiness outcome was recorded", journal.Failed)
			}
			entry := e.Journal.Entries[len(e.Journal.Entries)-1]
			if entry.State != "pending" || len(entry.Receipt) != 0 || len(e.Journal.KubernetesLeases) != 1 || e.Journal.KubernetesLeases[0].Holder[:64] != plan.SHA256() {
				t.Fatal("interruption manufactured acceptance or lost actual lease")
			}
		})
	}
}
