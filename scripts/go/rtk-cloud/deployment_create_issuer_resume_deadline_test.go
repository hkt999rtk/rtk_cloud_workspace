package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestIssuerResumeDeadlineUsesExactExistingAdmission(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "wrong-review", "wrong-original", "expired", "future-resume", "tampered", "original-mismatch"} {
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
			deadline := start.Add(24 * time.Hour)
			relative := filepath.Join("deployment", "create", "issuer-enrollment-continuation", review, "resume-admission.json")
			record := issuerResumeAdmission{Review: review, Original: original.Key, ResumedAt: start.Add(time.Minute), Deadline: deadline}
			switch mode {
			case "wrong-review":
				record.Review = deploymentplan.Digest("foreign")
			case "wrong-original":
				record.Original = issuerEnrollmentContinuationPrefix + deploymentplan.Digest("foreign")
			case "expired":
				record.Deadline = start.Add(time.Hour)
			case "future-resume":
				record.ResumedAt = now.Add(time.Hour)
			case "original-mismatch":
				original.StartedAt = original.StartedAt.Add(time.Second)
			}
			if mode != "missing" {
				raw := mustProviderRecoveryJSON(record)
				if mode == "tampered" {
					raw = []byte(`{"broken":`)
				}
				if err := store.write(relative, raw, false); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, err := readIssuerResumeDeadline(store, e, review, original)
			if mode == "valid" {
				if err != nil || !ok || !got.Equal(deadline) {
					t.Fatal("did not retain original 24h deadline", got, ok, err)
				}
				// A short per-preflight limit never becomes the program authority.
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Minute)
				defer cancel()
				if transient, _ := ctx.Deadline(); !transient.Before(got) {
					t.Fatal("fixture does not separate check timeout and execution deadline")
				}
			} else if mode == "missing" {
				if err != nil || ok {
					t.Fatal("missing admission was fabricated", ok, err)
				}
			} else if mode == "expired" {
				if err != nil || !ok || !got.Equal(record.Deadline) || !got.Before(now) {
					t.Fatal("expired original deadline was replaced or lost", got, ok, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid original admission", mode, ok)
			}
		})
	}
}
