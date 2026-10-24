package main

import (
	"crypto/sha256"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestIssuerReservationKeepsJournalAndNativeRequestDigestsDistinct(t *testing.T) {
	r := environmentCreateIssuerRequest{Environment: "dev", Domain: "openbao_tls", Kind: "intermediate", ParentID: "5874046d-4088-4a40-a27c-04f7fd9b0ef0", ServerDNSNames: []string{"openbao"}}
	canonical := `{"environment":"dev","kind":"intermediate","parent_issuer_id":"5874046d-4088-4a40-a27c-04f7fd9b0ef0","server_dns_names":["openbao"],"trust_domain":"openbao_tls"}`
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	got, err := environmentCreateIssuerIntentSHA(r)
	if err != nil || got != want || got == deploymentplan.Digest(r) {
		t.Fatal("reservation confused canonical journal intent with native field-order request digest", err)
	}
}

func TestTransportRootProofsRemainBoundAtRecoveryInputGate(t *testing.T) {
	proofs := map[string]string{"original-mqtt-root-active-document": "mqtt-proof", "original-openbao_tls-root-custody-key": "custody-proof", "original-service-root-custody-key": "service-proof", "openbao/root-token": "operator-proof"}
	bindAcceptedTransportRootMaterials(proofs)
	if proofs["accepted-service-core-original-mqtt-root-active-document"] != "mqtt-proof" || proofs["accepted-service-core-original-openbao_tls-root-custody-key"] != "custody-proof" || proofs["original-service-root-custody-key"] != "service-proof" || proofs["openbao/root-token"] != "operator-proof" || len(proofs) != 4 {
		t.Fatal("Root proofs were dropped or moved outside accepted core input bindings")
	}
	bindAcceptedTransportRootMaterials(proofs)
	if len(proofs) != 4 {
		t.Fatal("binding is not stable across rechecks")
	}
}

func TestOpenBaoDNSRecoveryPreservesDottedAliases(t *testing.T) {
	original := lkeOpenBaoDNSNames(map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"})
	slices.Sort(original)
	corrected, err := correctedOpenBaoDNSPolicy(original)
	if err != nil {
		t.Fatal(err)
	}
	if len(corrected) != 7 || slices.Contains(corrected, "openbao") || !slices.IsSorted(corrected) {
		t.Fatal("invalid managed DNS policy")
	}
	for _, name := range corrected {
		if !strings.Contains(name, ".") || !slices.Contains(original, name) {
			t.Fatal("aliases changed")
		}
	}
	if len(original) != 8 {
		t.Fatal("mutated original request")
	}
	if _, err := correctedOpenBaoDNSPolicy(corrected); err == nil {
		t.Fatal("accepted already corrected request as original rejection")
	}
}

func TestCreateSelectsOnlyOpenBaoIntermediateNoReceiptCheckpoint(t *testing.T) {
	j := environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "pki-reserve-openbao_tls-intermediate", State: "pending", IntentSHA256: strings.Repeat("a", 64)}}}
	profile, flag := createRecoveryCheckpointProfile(j)
	if profile != "openbao-intermediate-dns-policy" || flag != "--after-application-enrollment-trust" {
		t.Fatal("original create cannot select recovery")
	}
	j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "unknown", State: "pending", IntentSHA256: strings.Repeat("b", 64)})
	if profile, _ := createRecoveryCheckpointProfile(j); profile != "" {
		t.Fatal("unknown pending mutation accepted")
	}
	j.Entries = j.Entries[:1]
	j.Entries[0].Receipt = []byte(`{"operation_id":"unverified"}`)
	if profile, _ := createRecoveryCheckpointProfile(j); profile != "" {
		t.Fatal("uncertain mutation receipt accepted")
	}
	j.Entries[0].Receipt = nil
	j.Entries[0].State = "completed"
	j.Entries[0].CompletedAt = time.Now()
	if profile, _ := createRecoveryCheckpointProfile(j); profile != "" {
		t.Fatal("completed reservation selected as failed")
	}
}
