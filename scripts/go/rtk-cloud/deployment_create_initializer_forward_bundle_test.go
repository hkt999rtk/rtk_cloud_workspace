package main

import (
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestInitializerForwardBundleRetainsOriginalDeadlineAndPlans(t *testing.T) {
	store, e, plan, _ := freshStageFixture(t)
	retained := *plan
	retained.BindingsSHA256 = deploymentplan.Digest("retain")
	digest := deploymentplan.Digest("original fixture")
	now := time.Now().UTC()
	q := createInitializerForwardProof{Schema: initializerForwardProofSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), ParentPlanSHA256: e.Journal.PlanSHA256, SourceJournal: e.Journal, SourceJournalSHA256: deploymentplan.Digest(e.Journal), OriginalAdmissionSHA256: digest, FailedForwardSHA256: digest, OriginalPlanSHA256: digest, OriginalJournalSHA256: digest, CRLStage: "initial-crls", CRLPlanSHA256: digest, FailedRetainStage: "old-retain", FailedRetainPlanSHA256: digest, FailedRetainJournalSHA256: digest, UpdateStage: "fix-initializer", UpdatePlanSHA256: plan.SHA256(), RetainStage: "new-retain", RetainPlanSHA256: retained.SHA256(), QualifiedAt: now, ExecutionDeadline: now.Add(time.Hour)}
	review := deploymentplan.Digest(q)
	if err := saveInitializerForwardBundle(store, e, q, plan, &retained); err != nil {
		t.Fatal(err)
	}
	if err := saveInitializerForwardBundle(store, e, q, plan, &retained); err != nil {
		t.Fatal("idempotent retry", err)
	}
	got, u, r, err := loadInitializerForwardBundle(store, e, review)
	if err != nil || deploymentplan.Digest(got) != review || u.SHA256() != plan.SHA256() || r.SHA256() != retained.SHA256() || !got.ExecutionDeadline.Equal(q.ExecutionDeadline) {
		t.Fatal("bundle mismatch", err)
	}
	dir := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "initializer-forward", review)
	for _, name := range []string{"qualification.json", "update-plan.json", "retain-plan.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private evidence missing", name, err)
		}
	}
	changed := q
	changed.ExecutionDeadline = q.ExecutionDeadline.Add(time.Hour)
	if _, _, _, err := loadInitializerForwardBundle(store, e, deploymentplan.Digest(changed)); err == nil {
		t.Fatal("unapproved deadline accepted")
	}
	changed = q
	changed.ParentPlanSHA256 = strings.Repeat("f", 64)
	if err := saveInitializerForwardBundle(store, e, changed, plan, &retained); err == nil {
		t.Fatal("foreign generation accepted")
	}
	path := filepath.Join(dir, "update-plan.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadInitializerForwardBundle(store, e, review); err == nil {
		t.Fatal("changed plan accepted")
	}
	if err := saveInitializerForwardBundle(store, e, q, plan, &retained); err == nil {
		t.Fatal("changed original overwritten")
	}
}
