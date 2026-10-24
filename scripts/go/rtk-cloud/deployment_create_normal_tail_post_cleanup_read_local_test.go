package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestPostCleanupReadLocalOriginalSerialization(t *testing.T) {
	r := createNormalTailPostCleanupCorrection{
		Source:       createNormalTailPostCleanupCorrectionSource{Schema: createNormalTailPostCleanupSchema},
		SourceSHA256: deploymentplan.Digest("literal source"),
		Plans:        map[string]*deploymentplan.Plan{"apply": {}},
		Journals:     map[string]deploymentplan.Journal{"apply": {Accepted: true}},
	}
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	r.originalProgram = &createNormalTailProgram{Schema: createNormalTailProgramSchema}
	r.originalPlan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Operation: "scoped"}}
	r.originalJournal = deploymentplan.Journal{Owners: map[string]string{"private-proof-only": "original-owner"}}
	after, err := json.Marshal(r)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-local proof changed durable correction JSON", err)
	}
	var restored createNormalTailPostCleanupCorrection
	if json.Unmarshal(after, &restored) != nil || restored.originalProgram != nil || restored.originalPlan != nil || deploymentplan.Digest(restored.originalJournal) != deploymentplan.Digest(deploymentplan.Journal{}) {
		t.Fatal("read-local proof survived serialization")
	}
	if deploymentplan.Digest(r) != deploymentplan.Digest(restored) {
		t.Fatal("read-local proof changed the durable correction digest")
	}
}

// Load only a copied genuine synthetic F2 export, including its interrupted
// parent. Only the correction children/completion must be genuine. The original maintained
// configuration is carried by its immutable runtime source; no current live
// environment, rewritten history or new qualification supplies these inputs.
func postCleanupReadLocalFixture(t *testing.T) (*nestedRuntimeReadFixture, deploymentConfig) {
	t.Helper()
	path := os.Getenv("RTK_POSTCLEANUP_READLOCAL_EXPORT")
	if path == "" {
		t.Skip("optional genuine retained F2 export not selected")
	}
	t.Setenv("RTK_NESTED_RUNTIME_READER_EXPORT", path)
	f := loadNestedRuntimeReadFixture(t)
	var source createNormalTailRuntimeSource
	if readEnvironmentPreflightJSON(createNormalTailRuntimeSourcePath(f.e), &source) != nil {
		t.Fatal("literal original runtime configuration unavailable")
	}
	return f, source.Configuration
}

func TestPostCleanupReadLocalOriginalGenuineCorrectionHistory(t *testing.T) {
	f, cfg := postCleanupReadLocalFixture(t)
	before := workloadVolumeHistoryDigest(t, f.store.Root)
	parent := deploymentplan.Digest(f.e.Journal)
	r, err := readCreateNormalTailPostCleanupCorrection(cfg, f.store, f.e)
	if err != nil || r == nil || r.Completion == nil || r.originalProgram == nil || r.originalPlan == nil {
		t.Fatal("genuine completed correction lost its read-local original proof", err)
	}
	if deploymentplan.Digest(r.originalProgram) != r.Source.ProgramSHA256 || r.originalPlan.SHA256() != r.Source.WorkloadPlanSHA256 || deploymentplan.Digest(r.originalJournal) != r.Source.WorkloadJournalSHA256 || !r.originalJournal.Accepted {
		t.Fatal("read-local original proof differs from the authenticated immutable source")
	}
	proof := deploymentplan.Digest(r.originalPlan)
	journal := deploymentplan.Digest(r.originalJournal)
	for _, part := range []string{"prepare", "apply", "retire"} {
		p, j := r.Plans[part], r.Journals[part]
		if p == nil || !j.Accepted {
			t.Fatal("genuine accepted finite child absent", part)
		}
		defs, err := postCleanupCorrectionDefinitions(cfg, r.Source, r.originalPlan, r.originalJournal, part, r.Plans["prepare"], r.Journals["prepare"])
		if err != nil {
			t.Fatal("finite recipe from retained original proof", part, err)
		}
		if err := postCleanupCorrectionStagePlan(cfg, f.e, r.Source, r.originalProgram, r.originalPlan, r.originalJournal, part, defs, p); err != nil {
			t.Fatal("genuine finite child validation", part, err)
		}
	}
	// Exercise the changed public validator entrypoint. It must still start a
	// new authenticated read, then reuse only that invocation's original proof.
	if err := validatePostCleanupStageBeforeExecution(cfg, f.store, f.e, createNormalTailPostCleanupStage+"-apply", r.Plans["apply"]); err != nil {
		t.Fatal("ordinary finite-stage validator", err)
	}
	if proof != deploymentplan.Digest(r.originalPlan) || journal != deploymentplan.Digest(r.originalJournal) || parent != deploymentplan.Digest(f.e.Journal) || before != workloadVolumeHistoryDigest(t, f.store.Root) {
		t.Fatal("read-local finite validation changed original proof or history")
	}
}

func TestPostCleanupReadLocalOriginalRechecksNextInvocation(t *testing.T) {
	f, cfg := postCleanupReadLocalFixture(t)
	read := func() (*createNormalTailPostCleanupCorrection, error) {
		return readCreateNormalTailPostCleanupCorrection(cfg, f.store, f.e)
	}
	r, err := read()
	if err != nil || r == nil || r.Completion == nil || r.originalPlan == nil {
		t.Fatal("initial genuine correction proof", err)
	}
	before := workloadVolumeHistoryDigest(t, f.store.Root)
	sourcePath := postCleanupCorrectionPath(f.e, "source")
	journalPath := filepath.Join(f.store.Root, "deployment/rollback", r.originalPlan.SHA256(), "journal.json")
	for _, item := range []struct{ name, path string }{
		{"changed-source-after-valid-read", sourcePath},
		{"changed-workload-history-after-valid-read", journalPath},
	} {
		t.Run(item.name, func(t *testing.T) {
			raw, err := os.ReadFile(item.path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.WriteFile(item.path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}()
			changed := []byte("{")
			if item.path == journalPath {
				var j deploymentplan.Journal
				if json.Unmarshal(raw, &j) != nil || len(r.originalPlan.Resources) == 0 {
					t.Fatal("genuine workload journal")
				}
				j.Owners[r.originalPlan.Resources[0].ID] = "99999999-9999-4999-8999-999999999999"
				changed, err = json.Marshal(j)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(item.path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := read(); err == nil {
				t.Fatal("new invocation reused a stale original proof")
			}
		})
	}
	if restored, err := read(); err != nil || restored == nil || restored.Completion == nil || restored.originalPlan.SHA256() != r.originalPlan.SHA256() {
		t.Fatal("restored genuine original history", err)
	}
	if before != workloadVolumeHistoryDigest(t, f.store.Root) {
		t.Fatal("negative fixture did not restore exact original history")
	}
}
