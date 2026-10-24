package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestCreateNormalTailRuntimeStoragePreservesEveryKnownPartialClaim(t *testing.T) {
	for _, mode := range []string{"valid", "pvc-replaced", "claim-ref", "csi", "controller-replaced", "pod-owner", "pod-claim", "missing-ledger", "duplicate-ledger", "wrong-retain-owner", "changed-retained-body"} {
		t.Run(mode, func(t *testing.T) {
			ledger, _, _, frozen, previous, j := plannedVolumeFixture(t)
			observed := plannedVolumeNativeObjects(t, ledger, frozen, previous, j)
			next := *previous
			next.Resources = append([]deploymentplan.Resource(nil), previous.Resources...)
			for i, r := range next.Resources {
				r.Action = "retain"
				r.UID = j.Owners[r.ID]
				r.Baseline = deploymentplan.Projection(observed[r.ID])
				next.Resources[i] = r
			}
			volume := ledger.Volumes[len(ledger.Volumes)-1]
			claimID := "PersistentVolumeClaim/" + volume.Namespace + "/" + volume.PVCName
			stsID := previous.Resources[len(previous.Resources)-1].ID
			podID := "Pod/" + volume.Namespace + "/support-db-2"
			before := deploymentplan.Digest([]any{previous, j})
			switch mode {
			case "pvc-replaced":
				deploymentplan.Map(observed[claimID]["metadata"])["uid"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "claim-ref":
				deploymentplan.Map(deploymentplan.Map(observed["PersistentVolume//"+volume.PVName]["spec"])["claimRef"])["uid"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "csi":
				deploymentplan.Map(deploymentplan.Map(observed["PersistentVolume//"+volume.PVName]["spec"])["csi"])["volumeHandle"] = "foreign-pvc-original"
			case "controller-replaced":
				deploymentplan.Map(observed[stsID]["metadata"])["uid"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "pod-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(observed[podID]["metadata"])["ownerReferences"])[0])["uid"] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "pod-claim":
				deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(observed[podID]["spec"])["volumes"])[0])["persistentVolumeClaim"])["claimName"] = "foreign"
			case "missing-ledger":
				ledger.Volumes = ledger.Volumes[:len(ledger.Volumes)-1]
			case "duplicate-ledger":
				ledger.Volumes = append(ledger.Volumes, volume)
			case "wrong-retain-owner":
				next.Resources[len(next.Resources)-1].UID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			case "changed-retained-body":
				deploymentplan.Map(observed[stsID]["spec"])["replicas"] = float64(9)
			}
			err := checkCreateNormalTailRuntimeAllocatedClaims(t.Context(), &compiledEnvironmentDeployment{}, &next, previous, j, ledger, observed)
			if (err == nil) != (mode == "valid") {
				t.Fatal("partial-controller claim proof result", err)
			}
			if before != deploymentplan.Digest([]any{previous, j}) {
				t.Fatal("literal source allocation evidence changed")
			}
		})
	}
}
