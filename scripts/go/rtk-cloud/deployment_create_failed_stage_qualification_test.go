package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestQualifyCreateFailedFullyWrittenStageUsesOriginalParentAndNoReadiness(t *testing.T) {
	store, e, p, b := freshStageFixture(t)
	b.failure = "after-write-configuration"
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "workload", p, b, check, check, check, func(context.Context) error { return nil }, io.Discard)
	if err == nil {
		t.Fatal("fixture did not fail after original write")
	}
	j, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists || j.Failed == "" {
		t.Fatal(j, exists, err)
	}
	j.Completed = []string{p.Resources[0].ID}
	if j.Owners[p.Resources[0].ID] == "" {
		t.Fatal("test lacks original written owner")
	}
	raw, _ := json.Marshal(j)
	if err := os.WriteFile(filepath.Join(store.Root, "deployment", "rollback", p.SHA256(), "journal.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	evidence, err := qualifyCreateFailedFullyWrittenStage(t.Context(), store, e, "workload", b)
	if err != nil || evidence.ParentSHA256 != e.Journal.PlanSHA256 || evidence.Child.PlanSHA256 != p.SHA256() || evidence.Child.FailedResource != j.Failed {
		t.Fatal(evidence, err)
	}
	if _, err := qualifyCreateFailedFullyWrittenStage(t.Context(), store, e, "foreign", b); err == nil {
		t.Fatal("foreign parent stage accepted")
	}
}
