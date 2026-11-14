package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFailedForwardBundlePersistsOriginalPlansAndRejectsDrift(t *testing.T) {
	store, e, plan, _ := freshStageFixture(t)
	other := *plan
	other.BindingsSHA256 = deploymentplan.Digest("retain fixture")
	q := createFailedForwardProof{Schema: failedForwardProofSchema, Environment: e.Journal.Environment, FailedStage: createFailedStageQualification{ParentSHA256: e.Journal.PlanSHA256}, CandidateSHA256: plan.SHA256(), RetainCandidateSHA256: other.SHA256()}
	review := deploymentplan.Digest(q)
	if err := saveFailedForwardBundle(store, e, q, plan, &other); err != nil {
		t.Fatal(err)
	}
	if err := saveFailedForwardBundle(store, e, q, plan, &other); err != nil {
		t.Fatal("retry", err)
	}
	got, repair, retain, err := loadFailedForwardBundle(store, e, review)
	if err != nil || deploymentplan.Digest(got) != review || repair.SHA256() != plan.SHA256() || retain.SHA256() != other.SHA256() {
		t.Fatal("load", err)
	}
	base := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "failed-stage-forward", review)
	for _, name := range []string{"qualification.json", "repair-plan.json", "retain-plan.json"} {
		info, err := os.Lstat(filepath.Join(base, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("not private", name, err)
		}
	}
	changed := q
	changed.CandidateSHA256 = other.SHA256()
	if err := saveFailedForwardBundle(store, e, changed, plan, &other); err == nil {
		t.Fatal("accepted changed candidate")
	}
	if _, _, _, err := loadFailedForwardBundle(store, e, strings.Repeat("f", 64)); err == nil {
		t.Fatal("loaded missing evidence")
	}
	path := filepath.Join(base, "repair-plan.json")
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadFailedForwardBundle(store, e, review); err == nil {
		t.Fatal("accepted tampered plan")
	}
	if err := saveFailedForwardBundle(store, e, q, plan, &other); err == nil {
		t.Fatal("overwrote tampered evidence")
	}
}

func TestFailedForwardBundleRequiresCompletePrivateOriginalEvidence(t *testing.T) {
	for _, mode := range []string{"missing-plan", "missing-proof", "invalid-mode", "symlink", "foreign-parent", "noncanonical"} {
		t.Run(mode, func(t *testing.T) {
			store, e, plan, _ := freshStageFixture(t)
			retain := *plan
			retain.BindingsSHA256 = deploymentplan.Digest("other")
			q := createFailedForwardProof{Schema: failedForwardProofSchema, Environment: e.Journal.Environment, FailedStage: createFailedStageQualification{ParentSHA256: e.Journal.PlanSHA256}, CandidateSHA256: plan.SHA256(), RetainCandidateSHA256: retain.SHA256()}
			review := deploymentplan.Digest(q)
			if err := saveFailedForwardBundle(store, e, q, plan, &retain); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "failed-stage-forward", review)
			file := filepath.Join(dir, "repair-plan.json")
			switch mode {
			case "missing-plan":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "missing-proof":
				if err := os.Remove(filepath.Join(dir, "qualification.json")); err != nil {
					t.Fatal(err)
				}
			case "invalid-mode":
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(dir, "retain-plan.json"), file); err != nil {
					t.Fatal(err)
				}
			case "foreign-parent":
				q.FailedStage.ParentSHA256 = strings.Repeat("f", 64)
				if err := saveFailedForwardBundle(store, e, q, plan, &retain); err == nil {
					t.Fatal("foreign generation accepted")
				}
				return
			case "noncanonical":
				b, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, append(b, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, _, err := loadFailedForwardBundle(store, e, review); err == nil {
				t.Fatal("accepted", mode)
			}
			if mode == "missing-proof" || mode == "missing-plan" {
				if err := saveFailedForwardBundle(store, e, q, plan, &retain); err != nil {
					t.Fatal("could not resume interrupted save", err)
				}
				if _, _, _, err := loadFailedForwardBundle(store, e, review); err != nil {
					t.Fatal("resumed bundle invalid", err)
				}
			} else if err := saveFailedForwardBundle(store, e, q, plan, &retain); err == nil {
				t.Fatal("replaced existing invalid evidence", mode)
			}
		})
	}
}
