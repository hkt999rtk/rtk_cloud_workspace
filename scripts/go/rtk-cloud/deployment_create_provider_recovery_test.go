package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func providerRecoveryJournalFixture() (environmentCreateJournal, environmentCreatePlan) {
	now := time.Now().UTC().Add(-time.Minute)
	p := environmentCreatePlan{Schema: "rtk-cloud.create-plan/v1", Environment: "dev", Stack: "video-cloud-dev"}
	j := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: p.Environment, Stack: p.Stack, PlanSHA256: strings.Repeat("a", 64), AdmittedAt: now, Entries: []environmentCreateJournalEntry{}}
	for index, key := range []string{"lke", "edge", "turn-01", "bucket-runtime-media", "bucket-ota-firmware"} {
		kind := "instance"
		if key == "lke" {
			kind = "lke"
		}
		if strings.HasPrefix(key, "bucket-") {
			kind = "bucket"
		}
		r := environmentCreateProviderResource{Key: key, Kind: kind, Name: p.Stack + "-" + key, Region: "us-sea"}
		p.Provider.Resources = append(p.Provider.Resources, r)
		native := "101"
		if index > 0 {
			native = strconv.Itoa(101 + index)
		}
		if kind == "bucket" {
			native = "us-sea/" + r.Name
		}
		receipt, _ := json.Marshal(environmentCreateProviderReceipt{Key: key, Kind: kind, NativeID: native, Name: r.Name, Region: r.Region})
		j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: key, Phase: "provider", State: "completed", IntentSHA256: strings.Repeat("b", 64), Receipt: receipt, StartedAt: now, CompletedAt: now.Add(time.Second)})
	}
	proof, _ := json.Marshal(map[string]string{"cluster_id": "101", "kubeconfig_sha256": strings.Repeat("c", 64), "provider_state_sha256": strings.Repeat("d", 64)})
	j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "provider-kube-access", Phase: "provider", State: "completed", IntentSHA256: strings.Repeat("e", 64), Receipt: proof, StartedAt: now, CompletedAt: now.Add(time.Second)}, environmentCreateJournalEntry{Key: "activate-storage", Phase: "storage", State: "pending", IntentSHA256: strings.Repeat("f", 64), StartedAt: now})
	return j, p
}

func TestProviderOnlyRecoveryRejectsUncertainAndInitializedExecution(t *testing.T) {
	for _, mode := range []string{"valid", "provider-pending", "provider-allocated", "storage-receipt", "storage-completed", "wrong-storage-phase", "extra-canary", "lease", "already-recovered", "foreign-cluster", "foreign-name", "duplicate-entry", "bad-intent", "complete", "staging"} {
		t.Run(mode, func(t *testing.T) {
			j, p := providerRecoveryJournalFixture()
			switch mode {
			case "provider-pending":
				j.Entries[0].State = "pending"
			case "provider-allocated":
				j.Entries[0].State = "allocated"
			case "storage-receipt":
				j.Entries[6].Receipt = json.RawMessage(`{"unexpected":true}`)
			case "storage-completed":
				j.Entries[6].State = "completed"
			case "wrong-storage-phase":
				j.Entries[6].Phase = "bootstrap"
			case "extra-canary":
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "storage-canary-runtime-media"})
			case "lease":
				j.KubernetesLeases = []deploymentplan.LeaseOwner{{UID: "original-live-lease"}}
			case "already-recovered":
				j.OriginalJournal = "deployment/create/executions/original/journal.json"
			case "foreign-cluster":
				j.Entries[5].Receipt = json.RawMessage(`{"cluster_id":"999","kubeconfig_sha256":"` + strings.Repeat("c", 64) + `","provider_state_sha256":"` + strings.Repeat("d", 64) + `"}`)
			case "foreign-name":
				p.Provider.Resources[0].Name = "other-cluster"
			case "duplicate-entry":
				j.Entries[1] = j.Entries[0]
			case "bad-intent":
				j.Entries[0].IntentSHA256 = "unverified"
			case "complete":
				j.CompletedAt = time.Now().UTC()
			case "staging":
				j.Environment = "staging"
				p.Environment = "staging"
			}
			receipts, kubeSHA, err := validateProviderOnlyCreateJournal(j, p)
			if mode == "valid" {
				if err != nil || len(receipts) != 5 || kubeSHA != strings.Repeat("c", 64) {
					t.Fatal("verified provider-only baseline rejected", err)
				}
			} else if err == nil {
				t.Fatal("unsafe provider-only recovery admitted", mode)
			}
		})
	}
}

func TestProviderRecoveryPreservesOriginalJournalAndContinuousProcessLock(t *testing.T) {
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	old, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	j, p := providerRecoveryJournalFixture()
	j.PlanSHA256, j.OwnerToken, j.ArtifactSHA256 = old.Journal.PlanSHA256, old.Journal.OwnerToken, old.Journal.ArtifactSHA256
	old.Journal = j
	if err := old.save(); err != nil {
		t.Fatal(err)
	}
	original := readTestFile(t, old.JournalPath)
	originalOwner := readTestFile(t, filepath.Join(store.Root, "deployment/.execution-active/owner.json"))
	receipts, kubeSHA, err := validateProviderOnlyCreateJournal(j, p)
	if err != nil {
		t.Fatal(err)
	}
	recovery := environmentCreateProviderRecoveryPlan{OriginalPlanSHA256: artifact.PlanSHA256, OriginalJournal: old.owner.Journal, OriginalJournalSHA256: deploymentplan.Digest(j), OriginalOwnerSHA256: deploymentplan.Digest(old.owner), KubeconfigSHA256: kubeSHA, Plan: p, Receipts: receipts}
	sha := deploymentplan.Digest(recovery)
	e, err := forkProviderCreateRecovery(old, recovery, sha, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if readTestFile(t, old.JournalPath) != original || readTestFile(t, filepath.Join(old.Directory, "owner-recovery-handoff/owner.json")) != originalOwner {
		t.Fatal("original execution evidence was rewritten")
	}
	if old.running != nil || e.running == nil || len(e.Journal.Entries) != 6 || e.Journal.OriginalJournalSHA256 != recovery.OriginalJournalSHA256 || environmentCreateHostMaterialPlan(e) != artifact.PlanSHA256 {
		t.Fatal("recovery lost lock, lineage, or original host-key reference")
	}
	if err := e.Check(context.Background()); err != nil {
		t.Fatal("new owner is not verified", err)
	}
	if other, err := deploymentplan.LockExecutionOwner(filepath.Join(store.Root, "deployment/.execution-active"), false); err == nil {
		other.Close()
		t.Fatal("recovery released its process lock during handoff")
	}
	if _, err := os.Stat(filepath.Join(old.Directory, "owner-recovery-handoff/running.lock")); err != nil {
		t.Fatal("empty original process-lock file was not preserved", err)
	}
	if err := e.BeginEntry("activate-storage", "storage", json.RawMessage(`{"verified_new_recovery":true}`), time.Now().UTC()); err != nil {
		t.Fatal("new recovery cannot journal its independently qualified storage stage", err)
	}
	if readTestFile(t, old.JournalPath) != original {
		t.Fatal("new stage appended to failed original journal")
	}
}

func TestProviderRecoveryRejectsChangedOriginalOwnerBeforeHandoff(t *testing.T) {
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	old, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	p := environmentCreateProviderRecoveryPlan{OriginalPlanSHA256: artifact.PlanSHA256, OriginalJournal: old.owner.Journal, OriginalJournalSHA256: deploymentplan.Digest(old.Journal), OriginalOwnerSHA256: strings.Repeat("0", 64)}
	ownerPath := filepath.Join(store.Root, "deployment/.execution-active/owner.json")
	before := readTestFile(t, ownerPath)
	if recovered, err := forkProviderCreateRecovery(old, p, deploymentplan.Digest(p), strings.Repeat("1", 64)); err == nil {
		recovered.Close()
		t.Fatal("changed original owner qualified")
	}
	if readTestFile(t, ownerPath) != before {
		t.Fatal("failed qualification changed active owner")
	}
}
