package main

import (
	"testing"
	"time"
)

func TestOpenBaoFirstRecoveryCheckpointSelectsOnlyOriginalEarlyGeneration(t *testing.T) {
	for _, mode := range []string{"early", "later", "later-before-transport", "existing-profile"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e := openBaoFirstTransportFixture(t)
			if mode == "later-before-transport" {
				if err := e.BeginEntry("kubernetes/pki-migrate", "pki-migrate", mustProviderRecoveryJSON(map[string]string{"fixture": "later"}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			recordOpenBaoFirstTestTransport(t, cfg, store, e, lkeOpenBaoDNSNames(environmentCreateProviderValues(cfg)), "completed")
			switch mode {
			case "later":
				if err := e.BeginEntry("kubernetes/pki-migrate", "pki-migrate", mustProviderRecoveryJSON(map[string]string{"fixture": "later"}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "existing-profile":
				if err := e.BeginEntry("kubernetes/service-session", "service-session", mustProviderRecoveryJSON(map[string]string{"fixture": "existing"}), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "early":
				if checkpoint.Profile != "openbao-first-foundation" || checkpoint.Owner.PlanSHA256 != e.Journal.PlanSHA256 {
					t.Fatal("original early OpenBao-first checkpoint was not selected", checkpoint.Profile)
				}
			case "later", "later-before-transport":
				if checkpoint.Profile == "openbao-first-foundation" {
					t.Fatal("early recovery intercepted a later native stage")
				}
			case "existing-profile":
				if checkpoint.Profile != "service-session-order" {
					t.Fatal("existing recovery profile lost priority", checkpoint.Profile)
				}
			}
		})
	}
}

func TestOpenBaoFirstEarlyCheckpointRecognizesMaterialWithoutAcceptingLaterStages(t *testing.T) {
	j := environmentCreateJournal{Entries: []environmentCreateJournalEntry{
		{Key: "openbao-transport-material", Phase: "bootstrap"},
		{Key: "kubernetes/foundation"},
		{Key: "management-transport-material", Phase: "bootstrap"},
		{Key: "account-signer-material", Phase: "bootstrap"},
		{Key: "kubernetes/foundation-services"},
	}}
	if !isOpenBaoFirstEarlyCheckpoint(j) {
		t.Fatal("recorded early material checkpoints were not selected")
	}
	j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "kubernetes/pki-migrate"})
	if isOpenBaoFirstEarlyCheckpoint(j) {
		t.Fatal("a later stage was selected as an early checkpoint")
	}
	j.Entries[len(j.Entries)-1] = environmentCreateJournalEntry{Key: "account-signer-material"}
	if isOpenBaoFirstEarlyCheckpoint(j) {
		t.Fatal("a repeated material checkpoint was selected")
	}
}

func TestOpenBaoFirstRecoveryEnvironmentScope(t *testing.T) {
	for _, tc := range []struct {
		environment string
		want        bool
	}{{"dev", true}, {"staging", false}, {"prod", false}, {"production", false}, {"", false}} {
		if got := openBaoFirstRecoverySupportedEnvironment(tc.environment); got != tc.want {
			t.Errorf("environment %q: got %v, want %v", tc.environment, got, tc.want)
		}
	}
}
