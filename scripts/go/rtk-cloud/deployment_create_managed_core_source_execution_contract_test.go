package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Opt in only after the whole current Core loader has qualified this genuine
// interrupted source, before its forward admission. No constructor invokes this
// helper by default: each adversarial read reuses the actual accepted ancestry
// and restores every input before the connected recovery continues.
func assertHistoricalManagedCoreSourceRefusals(t *testing.T, f *acceptedCoreExecutionContract, qualified *createServiceSessionRecovery) {
	t.Helper()
	if qualified == nil || qualified.ManagedCorePartial == nil || qualified.Candidate == nil {
		t.Fatal("independently qualified current Core source required")
	}
	core := qualified.ManagedCorePartial
	if core.PriorAdmission != nil || core.PriorForward != nil || core.Action != managedCoreRestoreOnly || core.SourceSHA != deploymentplan.Digest(f.e.Journal) || managedCorePartialInitialReview(f.e.Journal) != core.OriginalRepairReview {
		t.Fatal("refusals require the actual original pre-admission Core boundary")
	}
	parentContext := t.Context()
	if err := f.e.Check(parentContext); err != nil {
		t.Fatal("original Core process owner is not held", err)
	}
	originalState := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	_, _, loaded, err := loadManagedCorePartialSource(f.store, f.e)
	if err != nil || deploymentplan.Digest(loaded) != deploymentplan.Digest(core) {
		t.Fatal("positive actual Core source no longer matches the qualified model", err)
	}
	assertRecoveryContractReadOnly(t, f.store, originalState)
	base := filepath.Join(f.store.Root, "deployment/create/openbao-recovery", core.OriginalRepairReview)
	originalObjects := f.protocol["objects"].(map[string]deploymentplan.Object)
	for _, mode := range []string{"captured-owner-changed", "captured-source-hash-changed", "original-ack-candidate-owner-changed", "current-core-owner-changed"} {
		t.Run(mode, func(t *testing.T) {
			filename, want := "", ""
			var changed []byte
			switch mode {
			case "captured-owner-changed":
				filename = filepath.Join(base, "source-owner.json")
				var owner environmentCreateOwner
				if err := readExecutionJSON(filename, &owner); err != nil {
					t.Fatal(err)
				}
				owner.OwnerToken += "-foreign"
				changed, want = mustProviderRecoveryJSON(owner), "stage=original-owner;"
			case "captured-source-hash-changed":
				filename = filepath.Join(base, "source-journal.json")
				var source environmentCreateJournal
				if err := readExecutionJSON(filename, &source); err != nil {
					t.Fatal(err)
				}
				// Keep a valid serialized source, but change a hash-bound field.
				// The original qualification/admission is deliberately untouched.
				source.OwnerToken += "-foreign"
				changed, want = mustProviderRecoveryJSON(source), "stage=original-ack-admission;"
			case "original-ack-candidate-owner-changed":
				filename = filepath.Join(base, "openbao-host-ack-source.json")
				var ack createOpenBaoACKRecovery
				if err := readExecutionJSON(filename, &ack); err != nil || ack.Candidate == nil || len(ack.Candidate.Resources) != 1 {
					t.Fatal("actual archived ACK candidate unavailable", err)
				}
				ack.Candidate.Resources[0].UID += "-foreign"
				changed, want = mustProviderRecoveryJSON(ack), "stage=original-ack-candidate;"
			case "current-core-owner-changed":
				id := core.Plan.Resources[0].ID
				if originalObjects[id] == nil || deploymentplan.Map(originalObjects[id]["metadata"])["uid"] != core.Journal.Owners[id] {
					t.Fatal("actual first accepted Core owner unavailable", id)
				}
				objects := make(map[string]deploymentplan.Object, len(originalObjects))
				for key, object := range originalObjects {
					objects[key] = object
				}
				objects[id] = deploymentplan.Clone(objects[id])
				deploymentplan.Map(objects[id]["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
				f.protocol["objects"] = objects
				f.publish(t)
				t.Cleanup(func() {
					f.protocol["objects"] = originalObjects
					f.publish(t)
				})
				want = "stage=owner-" + id + ";"
			}
			if filename != "" {
				original, err := os.ReadFile(filename)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(filename)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !json.Valid(original) || !json.Valid(changed) {
					t.Fatal("canonical original private JSON required", err)
				}
				t.Cleanup(func() {
					if err := os.WriteFile(filename, original, info.Mode().Perm()); err != nil {
						t.Fatal(err)
					}
				})
				if err := os.WriteFile(filename, changed, info.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.e.Check(t.Context()); err != nil {
				t.Fatal("isolated source mutation changed the actual parent fence", err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if mode == "current-core-owner-changed" {
				p := f.p
				p.Materials = map[string]string{}
				err = verifyManagedCoreObservationWithAction(t.Context(), f.cfg, f.store, f.e, f.backend, qualified, &p, core.Action)
			} else {
				_, _, _, err = loadManagedCorePartialSource(f.store, f.e)
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("changed actual Core source missed its exact refusal", mode, err)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			if deploymentplan.Digest(f.native.writes) != writes {
				t.Fatal("refused Core observation executed native writes")
			}
		})
		assertRecoveryContractReadOnly(t, f.store, originalState)
		if err := f.e.Check(parentContext); err != nil {
			t.Fatal("original Core source was not restored", err)
		}
	}
}
