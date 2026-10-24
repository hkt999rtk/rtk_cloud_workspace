package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateCompletedDispatchOnlyArchivesOriginalLocalOwner(t *testing.T) {
	cfg, store, operator, a := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	if e.BeginEntry("final-acceptance", "workload", mustProviderRecoveryJSON(deploymentplan.Object{"intent": "original"}), time.Now().UTC()) != nil || e.RecordReceipt("final-acceptance", mustProviderRecoveryJSON(deploymentplan.Object{"accepted": "original"})) != nil || e.CompleteEntry("final-acceptance", time.Now().UTC()) != nil {
		t.Fatal("fixture")
	}
	e.Journal.CompletedAt = time.Now().UTC()
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(e.JournalPath)
	activeBefore, _ := readEnvironmentPreflightActive(store)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	report := unifiedDeploymentReport{Environment: "dev", Operation: "create"}
	var out bytes.Buffer
	ready, err := tryUnifiedCreateNormalTail(t.Context(), cfg, unifiedDeploymentOptions{Action: "preflight", ParentPlanSHA256: e.Journal.PlanSHA256}, &report, &out, io.Discard)
	if err != nil || !ready || report.Completed || report.Decision != "GO" {
		t.Fatal("read-only archive qualification claimed released ownership", report, err)
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); err != nil {
		t.Fatal("read-only archive qualification released original owner")
	}
	handled, err := tryUnifiedCreateNormalTail(t.Context(), cfg, unifiedDeploymentOptions{Action: "create", ParentPlanSHA256: e.Journal.PlanSHA256, PlanSHA256: e.Journal.PlanSHA256}, &report, &out, io.Discard)
	if err != nil || !handled || !report.Completed || report.Scope != "original-create-completion" || len(report.Gates) != 0 {
		t.Fatal("completed dispatch entered bootstrap/qualification instead of local archival", report, err)
	}
	after, _ := os.ReadFile(e.JournalPath)
	activeAfter, _ := readEnvironmentPreflightActive(store)
	if string(before) != string(after) || deploymentplan.Digest(activeBefore) != deploymentplan.Digest(activeAfter) {
		t.Fatal("completed dispatch rewrote original history or qualification")
	}
	if _, err := os.Lstat(filepath.Join(e.Directory, "owner/owner.json")); err != nil {
		t.Fatal("original owner was not archived", err)
	}
}

func TestCreateCompletionRecoversDurableMarkerBeforeOwnerArchive(t *testing.T) {
	for _, stop := range []string{"after-capture", "after-completion-save", "archive-path-blocked"} {
		t.Run(stop, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
			defer e.Close()
			if e.BeginEntry("routing-final", "routing", mustProviderRecoveryJSON(deploymentplan.Object{"intent": "original"}), time.Now().UTC()) != nil || e.RecordReceipt("routing-final", mustProviderRecoveryJSON(deploymentplan.Object{"native": "genuinely-recorded"})) != nil || e.CompleteEntry("routing-final", time.Now().UTC()) != nil {
				t.Fatal("completion fixture")
			}
			ledger, err := readEnvironmentCreateLedger(e)
			if err != nil || saveEnvironmentCreateLedger(t.Context(), e, ledger) != nil {
				t.Fatal("final capture fixture", err)
			}
			if stop == "after-capture" {
				if err := e.CompleteAndReleaseContext(t.Context(), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			} else {
				if stop == "after-completion-save" {
					e.Journal.CompletedAt = time.Now().UTC()
					if err := e.save(); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Mkdir(filepath.Join(e.Directory, "owner"), 0700); err != nil {
						t.Fatal(err)
					}
					if e.CompleteAndReleaseContext(t.Context(), time.Now().UTC()) == nil || e.Journal.CompletedAt.IsZero() {
						t.Fatal("filesystem archive failure was not preserved")
					}
					if err := os.Remove(filepath.Join(e.Directory, "owner")); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := os.ReadFile(e.JournalPath)
				completed := e.Journal.CompletedAt
				if err := e.Close(); err != nil {
					t.Fatal(err)
				}
				original, err := loadCreateNormalTailExecution(t.Context(), cfg, store, e.Journal.PlanSHA256)
				if err != nil {
					t.Fatal("durably completed original owner could not reopen", err)
				}
				defer original.Close()
				if err := resumeCreateCompletedOwnerArchive(t.Context(), original, unifiedDeploymentOptions{Action: "preflight", ParentPlanSHA256: e.Journal.PlanSHA256}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); err != nil {
					t.Fatal("read-only preflight archived the owner")
				}
				if err := resumeCreateCompletedOwnerArchive(t.Context(), original, unifiedDeploymentOptions{Action: "create", ParentPlanSHA256: e.Journal.PlanSHA256}); err != nil {
					t.Fatal(err)
				}
				after, _ := os.ReadFile(e.JournalPath)
				if string(before) != string(after) || !original.Journal.CompletedAt.Equal(completed) {
					t.Fatal("archive retry rewrote native history or completion time")
				}
			}
			if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
				t.Fatal("original completed fence was not archived")
			}
			var owner environmentCreateOwner
			if readEnvironmentPreflightJSON(filepath.Join(e.Directory, "owner/owner.json"), &owner) != nil || owner.OwnerToken != e.owner.OwnerToken {
				t.Fatal("exact original owner archive missing")
			}
		})
	}
}

func TestCreateCompletedArchiveRefusesOwnerTargetOrOutcomeDrift(t *testing.T) {
	for _, mode := range []string{"owner", "target", "parent", "pending", "future-completion", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			_, e, _, _ := freshStageFixture(t)
			now := time.Now().UTC()
			if e.BeginEntry("original", "workload", mustProviderRecoveryJSON(deploymentplan.Object{"intent": "original"}), now) != nil || e.RecordReceipt("original", mustProviderRecoveryJSON(deploymentplan.Object{"native": "original"})) != nil || e.CompleteEntry("original", now) != nil {
				t.Fatal("fixture")
			}
			e.Journal.CompletedAt = now
			o := unifiedDeploymentOptions{Action: "create", ParentPlanSHA256: e.Journal.PlanSHA256}
			ctx := t.Context()
			switch mode {
			case "owner":
				e.owner.OwnerToken = "foreign-owner"
			case "target":
				o.PlanSHA256 = deploymentplan.Digest("foreign-target")
			case "parent":
				o.ParentPlanSHA256 = deploymentplan.Digest("foreign-parent")
			case "pending":
				e.Journal.Entries[0].State = "pending"
			case "future-completion":
				e.Journal.CompletedAt = now.Add(time.Hour)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if resumeCreateCompletedOwnerArchive(ctx, e, o) == nil {
				t.Fatal("unproved original owner was released")
			}
			if _, err := os.Lstat(filepath.Join(e.Store.Root, "deployment/.execution-active")); err != nil {
				t.Fatal("failed reconciliation changed original fence")
			}
		})
	}
}
