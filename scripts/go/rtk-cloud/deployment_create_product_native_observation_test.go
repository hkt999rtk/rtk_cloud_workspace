package main

import (
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

// Model the API/controller observations of the immutable synthetic StatefulSet
// target. This changes only in-memory inventory, never desired definitions,
// ownership receipts or stored journals.
func completeProductNativeStatefulSetObservations(t *testing.T, f *productProtocolFixture) {
	t.Helper()
	for _, o := range f.b.objects {
		m := deploymentplan.Map(o["metadata"])
		if m["resourceVersion"] == nil {
			m["resourceVersion"] = "1"
		}
		if o["kind"] != "StatefulSet" {
			continue
		}
		m["generation"] = float64(1)
		revision := deploymentplan.String(m["name"]) + "-fixture-native-current"
		o["status"] = deploymentplan.Object{"observedGeneration": float64(1), "updateRevision": revision, "currentRevision": revision}
		for ordinal := 0; ordinal < envObjectReplicas(o); ordinal++ {
			name := fmt.Sprintf("%s-%d", m["name"], ordinal)
			id := "Pod/" + deploymentplan.String(m["namespace"]) + "/" + name
			pod := f.b.objects[id]
			if pod == nil {
				pod = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": name, "namespace": m["namespace"], "uid": "16161616-aaaa-4aaa-8aaa-" + deploymentplan.Digest(id)[:12]}, "spec": deploymentplan.Clone(environmentPKIPod(o))}
				f.b.objects[id] = pod
			}
			pm := deploymentplan.Map(pod["metadata"])
			pm["resourceVersion"] = "1"
			labels := deploymentplan.Map(pm["labels"])
			if labels == nil {
				labels = deploymentplan.Object{}
				pm["labels"] = labels
			}
			labels["controller-revision-hash"] = revision
			pm["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": m["name"], "uid": m["uid"], "controller": true}}
			pod["status"] = deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}
		}
	}
}
