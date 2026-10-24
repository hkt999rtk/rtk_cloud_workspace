package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Each refusal begins with the same genuinely completed first session, whose
// certificate has naturally expired. Only the selected test input is corrupted;
// the recovery reader must leave every private artifact and native write intact.
func assertHistoricalOriginalSessionSourceRefusals(t *testing.T, f *acceptedCoreExecutionContract) {
	t.Helper()
	parentContext := t.Context()
	const stage = "kubernetes/service-session"
	boot, journal, err := acceptedCreateStage(f.store, f.e, stage)
	if err != nil {
		t.Fatal(err)
	}
	originalEntries := append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
	originalJournal, err := os.ReadFile(f.e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	originalObjects := f.protocol["objects"].(map[string]deploymentplan.Object)
	entryIndex := -1
	var foreign environmentCreateJournalEntry
	for i, entry := range originalEntries {
		if entry.Key == stage {
			entryIndex = i
		}
		if entry.Key == "kubernetes/service-enrollment-issuer" {
			foreign = entry
		}
	}
	if entryIndex < 0 || foreign.State != "completed" {
		t.Fatal("real accepted original and enrollment sources required")
	}
	planPath := filepath.Join(f.store.Root, "deployment/rollback", boot.SHA256(), "plan.json")
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing-source", "foreign-source", "parent-intent-changed", "missing-plan", "replaced-plan", "ambiguous-ordering", "malformed-ordering", "job-session-changed", "job-key-path-changed", "service-account-owner-changed", "claim-body-changed"} {
		t.Run(mode, func(t *testing.T) {
			f.e.Journal.Entries = append([]environmentCreateJournalEntry(nil), originalEntries...)
			objects := make(map[string]deploymentplan.Object, len(originalObjects))
			for id, object := range originalObjects {
				objects[id] = object
			}
			f.protocol["objects"] = objects
			t.Cleanup(func() {
				f.e.Journal.Entries = append([]environmentCreateJournalEntry(nil), originalEntries...)
				if err := os.WriteFile(f.e.JournalPath, originalJournal, 0600); err != nil {
					t.Fatal(err)
				}
				f.protocol["objects"] = originalObjects
				f.publish(t)
				if err := f.e.Check(parentContext); err != nil {
					t.Fatal("original held source was not restored", err)
				}
			})
			switch mode {
			case "missing-source":
				f.e.Journal.Entries = append(f.e.Journal.Entries[:entryIndex], f.e.Journal.Entries[entryIndex+1:]...)
			case "foreign-source":
				foreign.Key = stage
				f.e.Journal.Entries[entryIndex] = foreign
			case "parent-intent-changed":
				f.e.Journal.Entries[entryIndex].IntentSHA256 = strings.Repeat("a", 64)
			case "missing-plan", "replaced-plan":
				t.Cleanup(func() {
					if err := os.WriteFile(planPath, planBytes, 0600); err != nil {
						t.Fatal(err)
					}
				})
				if mode == "missing-plan" {
					err = os.Remove(planPath)
				} else {
					err = os.WriteFile(planPath, []byte("{}\n"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "ambiguous-ordering", "malformed-ordering":
				// Keep the genuine pending enrollment as the final parent entry.
				at := len(f.e.Journal.Entries) - 1
				added := []environmentCreateJournalEntry{originalEntries[entryIndex]}
				added[0].Key = "kubernetes/service-session-order-recovery-" + strings.Repeat("a", 64)
				if mode == "ambiguous-ordering" {
					second := added[0]
					second.Key = "kubernetes/service-session-order-recovery-" + strings.Repeat("b", 64)
					added = append(added, second)
				} else {
					added[0].Receipt = json.RawMessage(`{"plan_sha256":"invalid"}`)
				}
				last := f.e.Journal.Entries[at]
				f.e.Journal.Entries = append(append(f.e.Journal.Entries[:at], added...), last)
			case "job-session-changed", "job-key-path-changed":
				// Rebind a self-consistent test archive to isolate semantic identity
				// checks from the separate corrupt-archive and parent-hash checks.
				var changed deploymentplan.Plan
				if json.Unmarshal(planBytes, &changed) != nil {
					t.Fatal("original plan decode")
				}
				for i := range changed.Resources {
					if changed.Resources[i].Desired["kind"] != "Job" {
						continue
					}
					container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Resources[i].Desired)["containers"])[0])
					field, value := "PKI_BOOTSTRAP_SESSION_ID", "99999999-1111-4111-8111-111111111111"
					if mode == "job-key-path-changed" {
						field, value = "PKI_BOOTSTRAP_KEY_PATH", "/run/pki-bootstrap/foreign.key"
					}
					container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{field: value}))
				}
				sha := changed.SHA256()
				changedJournal := journal
				changedJournal.PlanSHA256 = sha
				f.write(t, filepath.Join("deployment/rollback", sha, "plan.json"), &changed)
				f.write(t, filepath.Join("deployment/rollback", sha, "journal.json"), &changedJournal)
				t.Cleanup(func() {
					if err := os.RemoveAll(filepath.Join(f.store.Root, "deployment/rollback", sha)); err != nil {
						t.Fatal(err)
					}
				})
				intent, err := environmentCreateKubernetesStageIntent(&changed)
				if err != nil {
					t.Fatal(err)
				}
				hash, err := environmentCreatePlanSHA256(intent)
				if err != nil {
					t.Fatal(err)
				}
				f.e.Journal.Entries[entryIndex].IntentSHA256 = hash
				f.e.Journal.Entries[entryIndex].Receipt = mustProviderRecoveryJSON(map[string]any{"plan_sha256": sha, "owners": journal.Owners})
			case "service-account-owner-changed":
				id := "ServiceAccount/" + f.p.Plan.Stack + "-video-cloud/pki-service-bootstrap"
				objects[id] = deploymentplan.Clone(objects[id])
				deploymentplan.Map(objects[id]["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
			case "claim-body-changed":
				id := "PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/pki-service-bootstrap-state"
				objects[id] = deploymentplan.Clone(objects[id])
				deploymentplan.Map(objects[id]["spec"])["storageClassName"] = "foreign-storage-class"
			}
			// Persist only this isolated adversarial journal input so the original
			// execution fence stays valid and the source selector itself must
			// reject it, rather than an earlier memory/disk mismatch doing so.
			changedJournal, err := json.Marshal(f.e.Journal)
			if err != nil || os.WriteFile(f.e.JournalPath, changedJournal, 0600) != nil {
				t.Fatal("write isolated adversarial journal", err)
			}
			if err := f.e.Check(t.Context()); err != nil {
				t.Fatal("adversarial source did not preserve the held execution boundary", err)
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			writes := deploymentplan.Digest(f.native.writes)
			p := f.p
			p.Materials = map[string]string{}
			want := "original accepted bootstrap executor unavailable"
			switch mode {
			case "foreign-source":
				want = "exact original accepted bootstrap executor required"
			case "parent-intent-changed":
				want = "original bootstrap executor parent binding changed"
			case "ambiguous-ordering":
				want = "ambiguous accepted bootstrap executors"
			case "job-session-changed":
				want = "original bootstrap executor session changed"
			case "job-key-path-changed":
				want = "exact original-key bootstrap recipe required"
			case "service-account-owner-changed", "claim-body-changed":
				want = "original bootstrap claim or ServiceAccount changed"
			}
			if _, err := loadServiceSessionRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("changed source did not reach its exact guarded refusal", mode, err)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("refused qualification wrote native state")
			}
		})
	}
}
