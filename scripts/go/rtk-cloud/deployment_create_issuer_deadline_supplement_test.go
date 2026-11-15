package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestIssuerDeadlineSupplementPreservesStartAndResumes(t *testing.T) {
	for _, mode := range []string{"new", "pending", "allocated", "tampered"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _, _ := freshStageFixture(t)
			now := time.Now().UTC()
			start := now.Add(-2 * time.Hour)
			original := environmentCreateJournalEntry{Key: issuerEnrollmentContinuationPrefix + deploymentplan.Digest("original"), Phase: "continuation", State: "pending", StartedAt: start, IntentSHA256: deploymentplan.Digest("intent")}
			e.Journal.AdmittedAt = start.Add(-time.Second)
			e.Journal.Entries = append(e.Journal.Entries, original)
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			review := deploymentplan.Digest("review")
			prior := start.Add(90 * time.Minute)
			desired := start.Add(4 * time.Hour)
			path := filepath.Join("deployment", "create", "issuer-enrollment-continuation", review, "resume-admission.json")
			if err := store.write(path, mustProviderRecoveryJSON(issuerResumeAdmission{Review: review, Original: original.Key, ResumedAt: start.Add(time.Minute), Deadline: prior}), false); err != nil {
				t.Fatal(err)
			}
			if mode != "new" {
				s := issuerDeadlineSupplement{Parent: e.Journal.PlanSHA256, Owner: deploymentplan.Digest(e.owner), Review: review, Original: original.Key, StartedAt: start, PriorDeadline: prior, Deadline: desired}
				if mode == "tampered" {
					s.Parent = deploymentplan.Digest("foreign")
				}
				key := issuerDeadlineSupplementKey(review, s)
				raw := mustProviderRecoveryJSON(s)
				if err := e.BeginEntry(key, "issuer-deadline-supplement", raw, now.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				if mode == "allocated" {
					if err := e.RecordReceipt(key, raw); err != nil {
						t.Fatal(err)
					}
				}
				if _, _, err := effectiveIssuerDeadline(t.Context(), store, e, review, original); err == nil {
					t.Fatal("unfinished supplement accepted")
				}
			}
			got, err := recordIssuerDeadlineSupplement(t.Context(), store, e, review, original, 4*time.Hour, now)
			if mode == "tampered" {
				if err == nil {
					t.Fatal("foreign pending intent accepted")
				}
				return
			}
			if err != nil || !got.Equal(desired) {
				t.Fatal("extension or interrupted resume failed", got, err)
			}
			historical, found, historicalErr := effectiveIssuerDeadlineAt(t.Context(), store, e, review, original, now.Add(-time.Second))
			if historicalErr != nil || !found || !historical.Equal(prior) {
				t.Fatal("later extension retroactively changed the original deadline", historical, historicalErr)
			}
			old, found, err := readIssuerResumeDeadline(store, e, review, original)
			if err != nil || !found || !old.Equal(prior) {
				t.Fatal("original admission changed", old, err)
			}
			again, err := recordIssuerDeadlineSupplement(t.Context(), store, e, review, original, 4*time.Hour, now.Add(time.Minute))
			if err != nil || !again.Equal(desired) {
				t.Fatal("retry reset deadline", again, err)
			}
			latest, err := recordIssuerDeadlineSupplement(t.Context(), store, e, review, original, 5*time.Hour, now.Add(2*time.Minute))
			if err != nil || !latest.Equal(start.Add(5*time.Hour)) {
				t.Fatal("successor extension lost start", latest, err)
			}
			if _, err := recordIssuerDeadlineSupplement(t.Context(), store, e, review, original, 4*time.Hour, now.Add(3*time.Minute)); err == nil {
				t.Fatal("deadline regressed")
			}
		})
	}
}

func TestIssuerDeadlineSupplementOwnerUsesSelectedEnvironment(t *testing.T) {
	for _, environment := range []string{"dev"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, environment)
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			var plan environmentCreatePlan
			if err := json.Unmarshal(artifact.Plan, &plan); err != nil {
				t.Fatal(err)
			}
			if err := e.BeginEntry("provider-fixture", "provider", mustProviderRecoveryJSON(deploymentplan.Object{"id": "fixture"}), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := e.RecordReceipt("provider-fixture", []byte(`{"fixture":true}`)); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry("provider-fixture", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			next, recovery := providerExecutionContractFork(t, e, plan, "issuer-deadline-owner-fixture")
			if err := next.Close(); err != nil {
				t.Fatal(err)
			}
			loaded, got, err := loadCreateRecoveryExecutionOwner(cfg, store, recovery.Qualification.PlanSHA256)
			if err != nil {
				t.Fatal("selected environment owner not loaded", err)
			}
			defer loaded.Close()
			if loaded.Journal.Environment != environment || deploymentplan.Digest(got) != deploymentplan.Digest(recovery) {
				t.Fatal("wrong environment owner")
			}
		})
	}
}

func TestIssuerDeadlineSupplementOnlySourceAdvance(t *testing.T) {
	review := deploymentplan.Digest("review")
	source := environmentCreateJournal{Environment: "dev", Entries: []environmentCreateJournalEntry{{Key: "original", Phase: "original", State: "pending"}}}
	current := source
	current.Entries = append(append([]environmentCreateJournalEntry(nil), source.Entries...), environmentCreateJournalEntry{Key: issuerDeadlineSupplementPrefix + review + "-" + deploymentplan.Digest("supplement"), Phase: "issuer-deadline-supplement", State: "completed"})
	if !issuerSupplementOnlyJournalAdvance(source, current, review) {
		t.Fatal("valid supplement advance rejected")
	}
	cases := []environmentCreateJournal{current, current, current}
	cases[0].Entries = append([]environmentCreateJournalEntry(nil), current.Entries...)
	cases[0].Entries[1].State = "pending"
	cases[1].Entries = append([]environmentCreateJournalEntry(nil), current.Entries...)
	cases[1].Entries[1].Key = issuerDeadlineSupplementPrefix + deploymentplan.Digest("foreign") + "-" + deploymentplan.Digest("supplement")
	cases[2].Entries = append([]environmentCreateJournalEntry(nil), current.Entries...)
	cases[2].Entries[0].State = "completed"
	for _, bad := range cases {
		if issuerSupplementOnlyJournalAdvance(source, bad, review) {
			t.Fatal("unauthorized source advance accepted")
		}
	}
}
