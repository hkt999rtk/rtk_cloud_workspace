package main

import (
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestAdmittedProviderRecoveryRetainsItsOwnGateContract(t *testing.T) {
	_, store, original, _, plan := providerExecutionContractFixture(t)
	execution, artifact := providerExecutionContractFork(t, original, plan, "")
	relative := filepath.Join("deployment/create/recovery", execution.Journal.PlanSHA256, "qualification.json")
	for _, mode := range []string{"create-family", "missing", "failed", "optional", "missing-proof", "wrong-order"} {
		t.Run(mode, func(t *testing.T) {
			changed := artifact
			changed.Qualification.Gates = append([]deploymentplan.GateResult(nil), artifact.Qualification.Gates...)
			switch mode {
			case "create-family":
				for i, id := range environmentCreateRequiredGates {
					changed.Qualification.Gates[i].ID = id
				}
			case "missing":
				changed.Qualification.Gates = changed.Qualification.Gates[:5]
			case "failed":
				changed.Qualification.Gates[0].Status = "FAIL"
			case "optional":
				changed.Qualification.Gates[0].Required = false
			case "missing-proof":
				changed.Qualification.Gates[0].EvidenceSHA256 = ""
			case "wrong-order":
				changed.Qualification.Gates[0], changed.Qualification.Gates[1] = changed.Qualification.Gates[1], changed.Qualification.Gates[0]
			}
			if err := store.write(relative, mustProviderRecoveryJSON(changed), true); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadEnvironmentCreateAdmittedGeneration(store, execution.Journal.PlanSHA256, deploymentplan.Digest(changed)); err == nil {
				t.Fatal("invalid provider admission accepted", mode)
			}
			if err := store.write(relative, mustProviderRecoveryJSON(artifact), true); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := loadEnvironmentCreateAdmittedGeneration(store, execution.Journal.PlanSHA256, execution.Journal.ArtifactSHA256)
			if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(plan) {
				t.Fatal("original provider admission did not remain reusable", err)
			}
		})
	}
}
