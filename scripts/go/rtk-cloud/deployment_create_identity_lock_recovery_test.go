package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestIdentityLockRecoveryRejectsUnownedOrUnadmittedPartialCore(t *testing.T) {
	stack := "video-cloud-dev"
	var defs []deploymentplan.Definition
	for i := 0; i < 14; i++ {
		defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "core-" + string(rune('a'+i)), "namespace": stack + "-video-cloud"}, "data": deploymentplan.Object{"public": "fixture"}}})
	}
	failed := "Deployment/" + stack + "-account-manager/account-manager"
	defs = append(defs, deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": stack + "-account-manager"}, "spec": deploymentplan.Object{}}})
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	parent := strings.Repeat("a", 64)
	source := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-application-trust-recovery/v1", ParentSHA: parent}}
	review := deploymentplan.Digest(source.Plan)
	source.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		source.Qualification.Gates = append(source.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	for _, mode := range []string{"original", "missing-owner", "unadmitted", "accepted", "wrong-intent", "other-write", "missing-gate"} {
		t.Run(mode, func(t *testing.T) {
			store := secretStore{Root: t.TempDir()}
			j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: failed, Owners: map[string]string{}}
			for _, r := range plan.Resources {
				j.Owners[r.ID] = "original-" + r.ID
				if r.ID != failed {
					j.Completed = append(j.Completed, r.ID)
				}
			}
			receipt, _ := json.Marshal(map[string]string{"qualified_recovery_sha256": review})
			e := &environmentCreateExecution{Journal: environmentCreateJournal{Stack: stack, PlanSHA256: parent, Entries: []environmentCreateJournalEntry{{Key: "openbao-recovery-admission-" + review, State: "completed", Receipt: receipt}, {Key: "kubernetes/service-managed-core-trust-recovery-" + review, State: "pending", IntentSHA256: hash}}, KubernetesLeases: []deploymentplan.LeaseOwner{{Holder: plan.SHA256() + ":original"}}}}
			a := source
			a.Qualification.Gates = append([]deploymentplan.GateResult(nil), source.Qualification.Gates...)
			switch mode {
			case "missing-owner":
				delete(j.Owners, failed)
			case "unadmitted":
				e.Journal.Entries[0].Key = "other-admission"
			case "accepted":
				j.Accepted = true
			case "wrong-intent":
				e.Journal.Entries[1].IntentSHA256 = strings.Repeat("f", 64)
			case "other-write":
				j.Owners["Secret/foreign/unknown"] = "foreign"
			case "missing-gate":
				a.Qualification.Gates = a.Qualification.Gates[:5]
			}
			write := func(path string, value any) {
				t.Helper()
				os.MkdirAll(filepath.Dir(path), 0700)
				b, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(store.Root, "deployment/rollback", plan.SHA256(), "plan.json"), plan)
			write(filepath.Join(store.Root, "deployment/rollback", plan.SHA256(), "journal.json"), j)
			write(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "qualification.json"), a)
			r := &createServiceSessionRecovery{}
			_, err := loadApplicationIdentityLockSource(store, e, r)
			if mode == "original" {
				if err != nil || !r.IdentityLockForward || r.PendingManagedPlan.SHA256() != plan.SHA256() {
					t.Fatal("original admitted lock boundary rejected", err)
				}
			} else if err == nil {
				t.Fatal("unsafe lock boundary admitted", mode)
			}
		})
	}
}
