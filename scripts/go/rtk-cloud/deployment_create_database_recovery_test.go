package main

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestScopedConfigurationAcceptanceRequiresActualContentsAndOwners(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "changed", "foreign-owner", "complete-environment"} {
		t.Run(mode, func(t *testing.T) {
			object := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "runtime", "namespace": "video-cloud-dev-video-cloud", "uid": "original-secret"}, "data": deploymentplan.Object{"url": "original-bound-value"}}
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Operation: "scoped"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(object), Action: "retain", UID: "original-secret", Desired: deploymentplan.Clone(object)}}}
			objects := []deploymentplan.Object{object}
			switch mode {
			case "missing":
				objects = nil
			case "changed":
				deploymentplan.Map(object["data"])["url"] = "different"
			case "foreign-owner":
				deploymentplan.Map(object["metadata"])["uid"] = "foreign"
			case "complete-environment":
				plan.Operation = "upgrade"
			}
			err := verifyEnvironmentTargetWorkloads(context.Background(), plan, objects, func(context.Context, string) (map[string]bool, error) {
				t.Fatal("configuration stage attempted image verification")
				return nil, nil
			})
			if mode == "valid" && err != nil {
				t.Fatal(err)
			}
			if mode != "valid" && err == nil {
				t.Fatal("missing/changed owner or incomplete full environment admitted")
			}
		})
	}
}

func TestPartialDatabaseCheckpointPreservesOriginalPasswordsAndFailedJob(t *testing.T) {
	for _, mode := range []string{"valid", "missing-password", "changed-password", "uncertain-write", "wrong-failure", "unrecorded-owner"} {
		t.Run(mode, func(t *testing.T) {
			_, store, _, _ := createStateFixture(t, "dev")
			parent := strings.Repeat("a", 64)
			p := createFoundationRecoveryPlan{ParentSHA: parent, Materials: map[string]string{}, Plan: environmentCreatePlan{Stack: "video-cloud-dev"}}
			passwords := map[string]string{"controller": "original-c", "issuer": "original-i", "verifier": "original-v"}
			secret := environmentCreateSecret("video-cloud-dev-video-cloud", "pki-create-database-logins", passwords)
			data := deploymentplan.Object{}
			for role, password := range passwords {
				data[role] = base64.StdEncoding.EncodeToString([]byte(password))
			}
			delete(secret.Object, "stringData")
			secret.Object["data"] = data
			secretID := "Secret/video-cloud-dev-video-cloud/pki-create-database-logins"
			jobID := "Job/video-cloud-dev-video-cloud/pki-create-database-logins"
			child := deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: secretID, Action: "create", Desired: secret.Object}, {ID: jobID, Action: "create", Desired: deploymentplan.Object{"kind": "Job"}}}}
			sha := child.SHA256()
			journal := deploymentplan.Journal{PlanSHA256: sha, Failed: jobID, Owners: map[string]string{secretID: "secret-original", jobID: "job-original"}, Completed: []string{secretID}}
			switch mode {
			case "uncertain-write":
				journal.InFlight = jobID
			case "wrong-failure":
				journal.Failed = "deployment.post-deploy.workloads"
			case "unrecorded-owner":
				delete(journal.Owners, jobID)
			}
			intent, err := environmentCreateKubernetesStageIntent(&child)
			if err != nil {
				t.Fatal(err)
			}
			intentSHA, err := environmentCreatePlanSHA256(intent)
			if err != nil {
				t.Fatal(err)
			}
			e := &environmentCreateExecution{Journal: environmentCreateJournal{KubernetesLeases: []deploymentplan.LeaseOwner{{Holder: sha + ":original-token"}}, Entries: []environmentCreateJournalEntry{{IntentSHA256: intentSHA}}}}
			for _, item := range []struct {
				name  string
				value any
			}{{"plan.json", child}, {"journal.json", journal}} {
				if err := store.write(filepath.Join("deployment/rollback", sha, item.name), mustProviderRecoveryJSON(item.value), false); err != nil {
					t.Fatal(err)
				}
			}
			for role, password := range passwords {
				if mode == "missing-password" && role == "verifier" {
					continue
				}
				if mode == "changed-password" && role == "verifier" {
					password = "replacement-prohibited"
				}
				if err := store.write(filepath.Join("deployment/create/material", parent, "database", role+".password"), []byte(password), false); err != nil {
					t.Fatal(err)
				}
			}
			_, gotJournal, gotPasswords, err := loadCreateDatabaseCheckpoint(store, e, &p)
			if mode == "valid" {
				if err != nil || deploymentplan.Digest(gotJournal) != deploymentplan.Digest(journal) || deploymentplan.Digest(gotPasswords) != deploymentplan.Digest(passwords) || len(p.Materials) != 3 {
					t.Fatal("original checkpoint or material lost", err)
				}
			} else if err == nil {
				t.Fatal("unsafe partial checkpoint admitted")
			}
		})
	}
}
