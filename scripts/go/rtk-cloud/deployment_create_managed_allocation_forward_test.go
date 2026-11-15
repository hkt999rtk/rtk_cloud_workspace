package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedAllocationForwardPreservesExistingClaimTemplates(t *testing.T) {
	_, _, live := existingFileOpenBaoFixture("dev", false)
	actual := deploymentplan.Clone(live[0])
	deploymentplan.Map(actual["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "app.kubernetes.io/managed-by": "rtk-cloud"}
	claims := certIssuerObjectList(deploymentplan.Map(actual["spec"])["volumeClaimTemplates"])
	for _, raw := range claims {
		deploymentplan.Map(deploymentplan.Map(raw)["spec"])["storageClassName"] = "linode-block-storage-retain"
	}
	want := deploymentplan.Clone(actual)
	for _, raw := range certIssuerObjectList(deploymentplan.Map(want["spec"])["volumeClaimTemplates"]) {
		delete(deploymentplan.Map(deploymentplan.Map(raw)["spec"]), "storageClassName")
	}
	deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(want["spec"])["template"])["spec"])["terminationGracePeriodSeconds"] = float64(60)
	original := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(want), Phase: "workload", Action: "update", Desired: want}}}
	forward, err := compileManagedAllocationForward(original, []deploymentplan.Object{actual})
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Resources) != 1 || forward.Resources[0].Action != "update" {
		t.Fatal("mutable OpenBao workload update was lost")
	}
	got := deploymentplan.Map(forward.Resources[0].Desired["spec"])["volumeClaimTemplates"]
	if deploymentplan.Digest(got) != deploymentplan.Digest(deploymentplan.Map(actual["spec"])["volumeClaimTemplates"]) {
		t.Fatal("original storage allocation was not preserved")
	}
	if deploymentplan.Digest(deploymentplan.Map(want["spec"])["volumeClaimTemplates"]) == deploymentplan.Digest(got) {
		t.Fatal("test did not exercise defaulted storage class")
	}
}
