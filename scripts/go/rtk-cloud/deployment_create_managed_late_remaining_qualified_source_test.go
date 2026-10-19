package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateRemainingQualifiedSourceFixture(t *testing.T) (*createServiceSessionRecovery, *createManagedLateRemaining, map[string]string, string) {
	t.Helper()
	r, proof := managedLateRemainingCleanupFixture(t)
	if err := restoreManagedLateRemainingHistoricalCleanup(r, proof); err != nil {
		t.Fatal(err)
	}
	remaining := r.ManagedLateRemaining
	remaining.CurrentSourceSHA = strings.Repeat("1", 64)
	remaining.CleanupOnly, remaining.FinalizationOnly = true, true
	remaining.LogProofSHA, remaining.CleanupSourceSHA = strings.Repeat("2", 64), strings.Repeat("3", 64)
	remaining.TerminalFinalizerCompletion = &createManagedLateTerminalFinalizerCompletion{Schema: managedLateTerminalFinalizerSchema, SourceSHA: strings.Repeat("4", 64), PrefixSHA: remaining.CurrentSourceSHA}
	var archived createManagedLateRemaining
	if err := json.Unmarshal(mustProviderRecoveryJSON(remaining), &archived); err != nil {
		t.Fatal(err)
	}
	review := strings.Repeat("5", 64)
	archived.Review = review
	// Reproduce terminal qualification's original e2 material hydration. It is
	// a real, separately source-bound old proof, not the Remaining388 proof.
	inherited := *proof
	inherited.SourceSHA = strings.Repeat("6", 64)
	materials := map[string]string{managedHistoricalSeedCleanupMaterial: deploymentplan.Digest(inherited), "accepted-service-core-managed-application-current-clients": proof.ClientsSHA}
	return r, &archived, materials, review
}

func TestManagedLateRemainingQualifiedSourceRebindsAuthenticatedHistoricalProof(t *testing.T) {
	r, archived, materials, review := managedLateRemainingQualifiedSourceFixture(t)
	oldMaterial := materials[managedHistoricalSeedCleanupMaterial]
	oldArchive := deploymentplan.Digest(archived)
	if err := restoreManagedLateRemainingQualifiedSource(r, archived, materials, review); err == nil {
		t.Fatal("stale inherited e2 material passed exact Remaining proof comparison")
	}
	if err := bindManagedLateRemainingHistoricalCleanupMaterial(r, materials); err != nil {
		t.Fatal("authenticated Remaining388 proof could not re-emit its material", err)
	}
	if materials[managedHistoricalSeedCleanupMaterial] == oldMaterial || materials[managedHistoricalSeedCleanupMaterial] != deploymentplan.Digest(archived.HistoricalSeedCleanup) {
		t.Fatal("producer copied the old e2 pin or changed the accepted Remaining proof")
	}
	if err := restoreManagedLateRemainingQualifiedSource(r, archived, materials, review); err != nil {
		t.Fatal("fresh terminal material could not pass unchanged qualified-loader checks", err)
	}
	if deploymentplan.Digest(archived) != oldArchive || materials["accepted-service-core-managed-application-current-clients"] != archived.HistoricalSeedCleanup.ClientsSHA {
		t.Fatal("repair rewrote archival proof or permanent-client pin")
	}
}

func TestManagedLateRemainingQualifiedSourceStillRefusesEveryImmutableDrift(t *testing.T) {
	for _, mode := range []string{"review", "current-source", "cleanup-mode", "finalization-mode", "log-proof", "cleanup-source", "source", "credential-observation", "proof-source", "proof-session", "proof-clients", "proof-observation", "proof-state", "material", "nil-proof"} {
		t.Run(mode, func(t *testing.T) {
			r, archived, materials, review := managedLateRemainingQualifiedSourceFixture(t)
			if err := bindManagedLateRemainingHistoricalCleanupMaterial(r, materials); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "review":
				archived.Review = strings.Repeat("9", 64)
			case "current-source":
				archived.CurrentSourceSHA = strings.Repeat("9", 64)
			case "cleanup-mode":
				archived.CleanupOnly = false
			case "finalization-mode":
				archived.FinalizationOnly = false
			case "log-proof":
				archived.LogProofSHA = strings.Repeat("9", 64)
			case "cleanup-source":
				archived.CleanupSourceSHA = strings.Repeat("9", 64)
			case "source":
				archived.SourceSHA = strings.Repeat("9", 64)
			case "credential-observation":
				archived.CredentialObservationSHA = strings.Repeat("9", 64)
			case "proof-source":
				archived.HistoricalSeedCleanup.SourceSHA = strings.Repeat("9", 64)
			case "proof-session":
				archived.HistoricalSeedCleanup.CurrentSession = "99999999-9999-4999-8999-999999999999"
			case "proof-clients":
				archived.HistoricalSeedCleanup.ClientsSHA = strings.Repeat("9", 64)
			case "proof-observation":
				archived.HistoricalSeedCleanup.ObservationSHA = strings.Repeat("9", 64)
			case "proof-state":
				archived.HistoricalSeedCleanup.State.Installed = archived.HistoricalSeedCleanup.State.Installed[:4]
			case "material":
				materials[managedHistoricalSeedCleanupMaterial] = strings.Repeat("9", 64)
			case "nil-proof":
				archived.HistoricalSeedCleanup = nil
			}
			before := deploymentplan.Digest([]any{archived, materials})
			if err := restoreManagedLateRemainingQualifiedSource(r, archived, materials, review); err == nil {
				t.Fatal("qualified-loader source check accepted changed field", mode)
			}
			if before != deploymentplan.Digest([]any{archived, materials}) {
				t.Fatal("source validation modified archive or material evidence")
			}
		})
	}
}

func TestManagedLateRemainingCleanupMaterialProducerRejectsForeignProof(t *testing.T) {
	r, _, materials, _ := managedLateRemainingQualifiedSourceFixture(t)
	r.ManagedLateRemaining.HistoricalSeedCleanup.SourceSHA = strings.Repeat("9", 64)
	before := deploymentplan.Digest(materials)
	if bindManagedLateRemainingHistoricalCleanupMaterial(r, materials) == nil || before != deploymentplan.Digest(materials) {
		t.Fatal("producer replaced accepted material with an unbound proof")
	}
}

func TestManagedLateRemainingQualifiedSourceReconstructsMissingHistoricalMaterial(t *testing.T) {
	r, archived, materials, review := managedLateRemainingQualifiedSourceFixture(t)
	delete(materials, managedHistoricalSeedCleanupMaterial)
	if restoreManagedLateRemainingQualifiedSource(r, archived, materials, review) == nil {
		t.Fatal("missing historical material passed immutable source comparison")
	}
	if err := bindManagedLateRemainingHistoricalCleanupMaterial(r, materials); err != nil {
		t.Fatal(err)
	}
	if err := restoreManagedLateRemainingQualifiedSource(r, archived, materials, review); err != nil || materials[managedHistoricalSeedCleanupMaterial] != deploymentplan.Digest(archived.HistoricalSeedCleanup) {
		t.Fatal("terminal producer did not reconstruct exact authenticated missing proof pin", err)
	}
}
