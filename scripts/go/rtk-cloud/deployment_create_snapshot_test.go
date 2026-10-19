package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestAcceptedCreateSnapshotRejectsOwnershipDriftAndUnboundPVC(t *testing.T) {
	desired := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "original", "namespace": "test"}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "1Gi"}}}}
	id := deploymentplan.ID(desired)
	latest := map[string]deploymentplan.Resource{id: {ID: id, Desired: desired}}
	owners := map[string]string{id: "original-uid"}
	for _, mutation := range []string{"none", "uid", "body", "unbound", "absent", "duplicate", "receiptless"} {
		t.Run(mutation, func(t *testing.T) {
			actual := deploymentplan.Clone(desired)
			deploymentplan.Map(actual["metadata"])["uid"] = "original-uid"
			actual["status"] = deploymentplan.Object{"phase": "Bound"}
			live := []deploymentplan.Object{actual}
			owned := map[string]string{id: owners[id]}
			switch mutation {
			case "uid":
				deploymentplan.Map(actual["metadata"])["uid"] = "replacement"
			case "body":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(actual["spec"])["resources"])["requests"])["storage"] = "2Gi"
			case "unbound":
				deploymentplan.Map(actual["status"])["phase"] = "Pending"
			case "absent":
				live = nil
			case "duplicate":
				live = append(live, actual)
			case "receiptless":
				owned[id] = ""
			}
			err := verifyAcceptedCreateSnapshot(latest, owned, live)
			if (err == nil) != (mutation == "none") {
				t.Fatal("snapshot ownership/body/PVC guard differs", mutation, err)
			}
		})
	}
}
