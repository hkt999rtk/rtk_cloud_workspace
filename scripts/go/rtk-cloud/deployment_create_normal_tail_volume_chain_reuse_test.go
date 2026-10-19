package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func workloadVolumeHistoryDigest(t *testing.T, root string) string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			t.Fatal("unexpected synthetic history file type")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = environmentPreflightBytesSHA256(raw)
		return nil
	}); err != nil {
		t.Fatal("synthetic history digest", err)
	}
	return deploymentplan.Digest(files)
}

// Both histories were retained from the genuine ordinary producer. This invokes
// the allocation reader used by capture, including product and literal failed
// workload ancestors, without constructing any positive journal or receipt.
func TestWorkloadVolumeReadEpochGenuineHistory(t *testing.T) {
	for _, item := range []struct{ name, path string }{
		{"interrupted", os.Getenv("RTK_NESTED_RUNTIME_READER_EXPORT")},
		{"completed", os.Getenv("RTK_NESTED_RUNTIME_READER_COMPLETE_EXPORT")},
	} {
		t.Run(item.name, func(t *testing.T) {
			if item.path == "" {
				t.Fatal("both genuine synthetic exports required")
			}
			t.Setenv("RTK_NESTED_RUNTIME_READER_EXPORT", item.path)
			f := loadNestedRuntimeReadFixture(t)
			before := workloadVolumeHistoryDigest(t, f.store.Root)
			parent := deploymentplan.Digest(f.e.Journal)
			sources, err := readCreateNormalTailWorkloadSources(f.store, f.e, f.original)
			if err != nil || len(sources) != 5 {
				t.Fatal("genuine five-source workload history", len(sources), err)
			}
			names := []string{"workload", createNormalTailProductSessionForwardStage, createNormalTailRuntimeStage(2), createNormalTailRuntimeStage(3), createNormalTailRuntimeStage(4)}
			for i, stage := range sources {
				if stage.Stage != names[i] || stage.Plan == nil {
					t.Fatal("literal workload stage order", i)
				}
				actual, exists, err := stage.Plan.ReadExecution(f.store.Root)
				if err != nil || !exists || deploymentplan.Digest(actual) != deploymentplan.Digest(stage.Journal) {
					t.Fatal("literal persisted child journal", i, err)
				}
				if stage.Journal.Accepted != (i == len(sources)-1) {
					t.Fatal("failed ancestor acceptance was fabricated", i)
				}
				if i == 0 && stage.originalPlanSHA256 != "" || i > 0 && stage.originalPlanSHA256 != sources[i-1].Plan.SHA256() {
					t.Fatal("literal allocation predecessor lost", i)
				}
			}
			index, claims, err := managedLateVolumeStageClaims(sources)
			if err != nil || len(index) != 6 {
				t.Fatal("six workload allocation authorities", len(index), err)
			}
			owned := make([]int, len(sources))
			for id, n := range index {
				if sources[n].Journal.Owners[claims[n][id].ResourceID] == "" {
					t.Fatal("planned allocation write owner absent")
				}
				owned[n]++
			}
			if owned[0] != 3 || owned[1] != 2 || owned[3] != 1 || owned[4] != 0 {
				t.Fatal("original direct, intermediate support and final Fleet owners changed", owned)
			}
			ledger, err := readEnvironmentCreateLedger(f.e)
			if err != nil || len(ledger.Volumes) != 20 {
				t.Fatal("genuine twenty-volume ledger", len(ledger.Volumes), err)
			}
			matched := 0
			for _, volume := range ledger.Volumes {
				if _, ok := index["PersistentVolumeClaim/"+volume.Namespace+"/"+volume.PVCName]; ok {
					matched++
				}
			}
			if matched != 6 {
				t.Fatal("recorded workload allocation lost its literal authority", matched)
			}
			if deploymentplan.Digest(f.e.Journal) != parent || workloadVolumeHistoryDigest(t, f.store.Root) != before {
				t.Fatal("pure allocation read changed immutable history")
			}
			t.Logf("genuine %s history retained five literal workload sources and six owned allocations in the twenty-volume ledger", item.name)
		})
	}
}

func TestWorkloadVolumeReadEpochRechecksNextInvocation(t *testing.T) {
	f := loadNestedRuntimeReadFixture(t)
	read := func() ([]createNormalTailVolumeStage, error) {
		return readCreateNormalTailWorkloadSources(f.store, f.e, f.original)
	}
	sources, err := read()
	if err != nil || len(sources) != 5 {
		t.Fatal("initial genuine allocation proof", err)
	}
	before := workloadVolumeHistoryDigest(t, f.store.Root)
	first, err := readCreateNormalTailWorkloadForwardDescriptor(f.store, f.e, f.original, f.journal)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := readCreateNormalTailProductSessionForwardSource(f.store, f.e, f.original, f.journal, first)
	if err != nil {
		t.Fatal(err)
	}
	path := createNormalTailRuntimeDescriptorPath(f.e, 4)
	descriptor, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d createNormalTailRuntimeForward
	if json.Unmarshal(descriptor, &d) != nil {
		t.Fatal("genuine runtime descriptor")
	}
	for _, mode := range []string{"changed-source", "changed-logical-response", "changed-winner-owner"} {
		t.Run(mode, func(t *testing.T) {
			entries := append([]environmentCreateJournalEntry(nil), f.e.Journal.Entries...)
			journalPath := filepath.Join(f.store.Root, "deployment/rollback", sources[4].Plan.SHA256(), "journal.json")
			journal, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				f.e.Journal.Entries = entries
				if err := os.WriteFile(path, descriptor, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journalPath, journal, 0600); err != nil {
					t.Fatal(err)
				}
			}()
			switch mode {
			case "changed-source":
				changed := d
				changed.SourceSHA256 = deploymentplan.Digest("different after previous read")
				if err := os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600); err != nil {
					t.Fatal(err)
				}
				// The independent entrypoint must still read a fresh chain rather than
				// borrowing an earlier caller's authenticated descriptor.
				if _, err := readCreateNormalTailRuntimeWorkloadSources(f.store, f.e, f.original, f.original, f.journal, first, previous, &d); err == nil {
					t.Fatal("independent allocation wrapper reused stale source")
				}
			case "changed-logical-response":
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+createNormalTailProductSessionForwardStage)
				if entry == nil {
					t.Fatal("genuine product alias absent")
				}
				entry.Receipt = json.RawMessage(`{"foreign":true}`)
			case "changed-winner-owner":
				var changed deploymentplan.Journal
				if json.Unmarshal(journal, &changed) != nil {
					t.Fatal("genuine winner journal")
				}
				changed.Owners[sources[4].Plan.Resources[0].ID] = "99999999-9999-4999-8999-999999999999"
				if err := os.WriteFile(journalPath, mustProviderRecoveryJSON(changed), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := read(); err == nil {
				t.Fatal("next allocation invocation accepted changed ancestry or logical response")
			}
		})
	}
	if restored, err := read(); err != nil || len(restored) != len(sources) {
		t.Fatal("restored genuine allocation history", err)
	}
	if workloadVolumeHistoryDigest(t, f.store.Root) != before {
		t.Fatal("tamper fixture failed to restore literal history")
	}
}
