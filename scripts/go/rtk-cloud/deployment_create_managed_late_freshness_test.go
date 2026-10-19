package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateFreshnessRefusesBeforeChangingOriginal357Parent(t *testing.T) {
	for _, name := range []string{"fresh", "measured 926-second rechecks", "exact expiry", "future GO", "qualification replaced", "qualification bytes changed", "wrong checker", "failed gate", "wrong source", "wrong intent", "cancelled", "public qualification", "qualification symlink", "post-mint fresh", "post-mint exact expiry", "post-mint qualification bytes changed", "expired-unused fresh", "expired-unused exact expiry", "expired-unused qualification bytes changed"} {
		t.Run(name, func(t *testing.T) {
			action := strings.TrimPrefix(strings.TrimPrefix(name, "post-mint "), "expired-unused ")
			cfg, store, operator, original := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, original, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			for index := 0; index < 357; index++ {
				at := e.Journal.AdmittedAt.Add(time.Duration(index+1) * time.Nanosecond)
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("isolated-completed-%03d", index), Phase: "bootstrap", State: "completed", StartedAt: at, CompletedAt: at, IntentSHA256: strings.Repeat("e", 64)})
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			checker, err := environmentPreflightCheckerSHA256()
			if err != nil {
				t.Fatal(err)
			}
			a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: managedLateSessionSchema, ParentSHA: e.Journal.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(e.Journal), ParentOwnerSHA: deploymentplan.Digest(e.owner), CheckerSHA: checker}, QualifiedAt: time.Now().UTC().Add(-time.Second)}
			a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			if strings.HasPrefix(name, "post-mint ") {
				a.Plan.Schema = managedLatePostMintSchema
			}
			if strings.HasPrefix(name, "expired-unused ") {
				a.Plan.Schema = managedLateExpiredRenewalSchema
			}
			if name == "future GO" {
				a.QualifiedAt = time.Now().UTC().Add(time.Minute)
				a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
			}
			if name == "wrong checker" {
				a.Plan.CheckerSHA = strings.Repeat("f", 64)
			}
			if name == "wrong source" {
				a.Plan.ParentJournalSHA = strings.Repeat("f", 64)
			}
			review := deploymentplan.Digest(a.Plan)
			a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
			for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
				a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
			}
			if name == "failed gate" {
				a.Qualification.Gates[3].Status = "BLOCKED"
			}
			relative := filepath.Join("deployment/create/openbao-recovery", review, "qualification.json")
			path := filepath.Join(store.Root, relative)
			if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			raw := mustProviderRecoveryJSON(a)
			if err := store.write(relative, raw, false); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := readManagedLateQualificationFingerprint(store, a)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			intent := mustProviderRecoveryJSON(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA})
			ctx := context.Background()
			switch action {
			case "measured 926-second rechecks":
				// The real preparation+four rechecks total926.716s. The
				// audit/readback boundary must refuse without a parent intent.
				now = a.QualifiedAt.Add(926716 * time.Millisecond)
			case "exact expiry":
				now = a.ValidUntil
			case "qualification replaced":
				changed := a
				changed.QualifiedAt = changed.QualifiedAt.Add(time.Nanosecond)
				changed.ValidUntil = changed.ValidUntil.Add(time.Nanosecond)
				if err := store.write(relative, mustProviderRecoveryJSON(changed), true); err != nil {
					t.Fatal(err)
				}
			case "qualification bytes changed":
				if err := store.write(relative, append(raw, '\n'), true); err != nil {
					t.Fatal(err)
				}
			case "wrong intent":
				intent = mustProviderRecoveryJSON(map[string]string{"review_sha256": review})
			case "cancelled":
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				stop()
			case "public qualification":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "qualification symlink":
				target := filepath.Join(t.TempDir(), "foreign.json")
				if os.WriteFile(target, raw, 0600) != nil || os.Remove(path) != nil || os.Symlink(target, path) != nil {
					t.Fatal("could not prepare isolated symlink negative")
				}
			}
			err = beginManagedLateParentAdmission(ctx, store, e, a, fingerprint, intent, now)
			if action == "fresh" {
				if err != nil || len(e.Journal.Entries) != 358 || e.Journal.Entries[357].State != "pending" || e.Journal.Entries[357].Key != "openbao-recovery-admission-"+review {
					t.Fatal("genuine fresh unchanged qualification refused", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expired or changed qualification admitted")
			}
			after, readErr := os.ReadFile(e.JournalPath)
			if readErr != nil || string(before) != string(after) || len(e.Journal.Entries) != 357 {
				t.Fatal("refusal changed original357 parent or durable journal", err)
			}
		})
	}
}
