package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestApplicationTrustRecoveryRequiresExactPartialOwnersAndIntent(t *testing.T) {
	stack := "video-cloud-dev"
	ns := stack + "-video-cloud"
	var defs []deploymentplan.Definition
	for _, owner := range environmentCreateApplicationOwners() {
		defs = append(defs, deploymentplan.Definition{Phase: "storage", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": owner.Claim, "namespace": ns}, "spec": deploymentplan.Object{}}}, deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "pki-enroll-" + owner.Name + "-client-original", "namespace": ns}, "spec": deploymentplan.Object{}}})
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("app")}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Owners: map[string]string{}, Failed: "Job/" + ns + "/pki-enroll-factoryenroll-client-original"}
	for _, r := range plan.Resources {
		if r.Desired["kind"] == "PersistentVolumeClaim" {
			j.Completed = append(j.Completed, r.ID)
			j.Owners[r.ID] = "original-" + r.ID
		}
	}
	j.Owners[j.Failed] = "original-failed-owner"
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"original", "missing-lease", "different-intent", "missing-owner", "additional-write", "in-flight", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			store := secretStore{Root: t.TempDir()}
			state := j
			state.Owners = map[string]string{}
			for k, v := range j.Owners {
				state.Owners[k] = v
			}
			state.Completed = slices.Clone(j.Completed)
			e := &environmentCreateExecution{Journal: environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "kubernetes/application-client-enrollment", State: "pending", IntentSHA256: hash}}, KubernetesLeases: []deploymentplan.LeaseOwner{{Holder: plan.SHA256() + ":original"}}}}
			switch mode {
			case "missing-lease":
				e.Journal.KubernetesLeases = nil
			case "different-intent":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "missing-owner":
				delete(state.Owners, state.Failed)
			case "additional-write":
				state.Owners["Job/"+ns+"/unqualified"] = "foreign"
			case "in-flight":
				state.InFlight = state.Failed
			case "accepted":
				state.Accepted = true
			}
			dir := filepath.Join(store.Root, "deployment/rollback", plan.SHA256())
			os.MkdirAll(dir, 0700)
			for name, value := range map[string]any{"plan.json": plan, "journal.json": state} {
				raw, _ := json.Marshal(value)
				os.WriteFile(filepath.Join(dir, name), raw, 0600)
			}
			selected, _, err := originalPendingApplicationPlan(store, e, stack)
			if mode == "original" {
				if err != nil || selected.SHA256() != plan.SHA256() {
					t.Fatal("original partial app not selected", err)
				}
			} else if err == nil {
				t.Fatal("unqualified app writes admitted", mode)
			}
		})
	}
	journal := environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "kubernetes/application-client-enrollment", State: "pending", IntentSHA256: hash}}}
	if profile, flag := createRecoveryCheckpointProfile(journal); profile != "application-enrollment-trust" || flag != "--after-application-enrollment-trust" {
		t.Fatal("formal create did not recognize original app checkpoint")
	}
	journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/unknown", State: "pending", IntentSHA256: hash, StartedAt: time.Now()})
	if profile, _ := createRecoveryCheckpointProfile(journal); profile != "" {
		t.Fatal("unknown concurrent writes selected app recovery")
	}
	journal.Entries[1].Key = "kubernetes/service-managed-core-trust-recovery-" + strings.Repeat("b", 64)
	if profile, flag := createRecoveryCheckpointProfile(journal); profile != "application-core-identity-lock" || flag != "--after-application-enrollment-trust" {
		t.Fatal("formal create failed to select original core identity lock recovery")
	}
	journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/unreviewed", State: "pending", IntentSHA256: hash})
	if profile, _ := createRecoveryCheckpointProfile(journal); profile != "" {
		t.Fatal("additional unknown write bypassed identity lock recovery")
	}
	// Missing publication cannot fall back to a mutable image or another store.
	if _, _, err := loadServiceTrustRepairImage(context.Background(), deploymentConfig{Environment: "dev"}, secretStore{Root: t.TempDir()}, strings.Repeat("a", 64)); err == nil {
		t.Fatal("missing formal publication guessed")
	}
}
