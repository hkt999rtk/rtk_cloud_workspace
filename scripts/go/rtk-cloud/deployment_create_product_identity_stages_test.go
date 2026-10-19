package main

import (
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductVolumeStageSelectionDoesNotDuplicateWorkloadOrSteadyClaims(t *testing.T) {
	l := &createNormalTailProductLifecycle{Outputs: []environmentCreateProductIdentity{{Subject: "service:shadow-worker"}}}
	prefix := "product-identities-" + productIdentityScope(l)[:12] + "-"
	for _, stage := range []string{prefix + "service-session", prefix + "enroll-shadow-worker", "workload", prefix + "steady", prefix + "enroll-foreign", prefix + "service-custody-reader", "product-identities-" + strings.Repeat("f", 12) + "-service-session"} {
		want := stage == prefix+"service-session" || stage == prefix+"enroll-shadow-worker"
		if productVolumeStageSelected(l, stage) != want {
			t.Fatal("allocation sources duplicated or exceeded the frozen product scope", stage)
		}
	}
}

func TestProductFinalizationReceiptAcceptsRecordedOutcomeBeforeParentCompletion(t *testing.T) {
	now := time.Now().UTC()
	for _, mode := range []string{"allocated", "completed", "foreign-session", "foreign-environment", "before-intent", "after-completion", "future", "missing-tombstone", "copies-remain"} {
		t.Run(mode, func(t *testing.T) {
			entry := environmentCreateJournalEntry{State: "allocated", StartedAt: now.Add(-time.Minute)}
			native := environmentCreateBootstrapFinalization{Environment: "dev", SessionID: "11111111-1111-4111-8111-111111111111", FinalizedAt: now.Add(-30 * time.Second)}
			tombstone, absent := deploymentplan.Digest("genuine-tombstone"), true
			switch mode {
			case "completed":
				entry.State, entry.CompletedAt = "completed", now.Add(-time.Second)
			case "foreign-session":
				native.SessionID = "22222222-2222-4222-8222-222222222222"
			case "foreign-environment":
				native.Environment = "staging"
			case "before-intent":
				native.FinalizedAt = entry.StartedAt.Add(-time.Second)
			case "after-completion":
				entry.State, entry.CompletedAt = "completed", native.FinalizedAt.Add(-time.Second)
			case "future":
				native.FinalizedAt = now.Add(time.Minute)
			case "missing-tombstone":
				tombstone = ""
			case "copies-remain":
				absent = false
			}
			entry.Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"native_finalization": native, "custody_tombstone_sha256": tombstone, "copies_absent": absent})
			if productFinalizationReceiptMatches(entry, "dev", "11111111-1111-4111-8111-111111111111") != (mode == "allocated" || mode == "completed") {
				t.Fatal("native outcome did not preserve its original parent receipt boundary")
			}
		})
	}
}
