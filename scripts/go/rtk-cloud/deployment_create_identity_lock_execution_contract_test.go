package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newIdentityLockRecoveryExecutionContract(t *testing.T) (*acceptedCoreExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	f := newAcceptedCoreExecutionContractWithListeners(t, false, true)
	image := f.installFormalRepairPublication(t)
	runtime := f.runtime
	var err error
	runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(f.cfg, runtime.InitialListeners)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	account := "Deployment/" + f.p.Plan.Stack + "-account-manager/account-manager"
	for i := range definitions {
		d := &definitions[i]
		d.Phase = "configuration"
		d.Dependencies = nil
		if d.Object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pki-controller" || c["name"] == "certissuer" {
					c["image"] = image.Reference
				}
			}
		}
		if deploymentplan.ID(d.Object) == account {
			d.Phase = "migration"
			spec := deploymentplan.Map(d.Object["spec"])
			spec["strategy"] = deploymentplan.Object{"type": "RollingUpdate"}
			metadata := deploymentplan.Map(deploymentplan.Map(spec["template"])["metadata"])
			delete(deploymentplan.Map(metadata["annotations"]), "rtk.realtek.com/pki-identity-owner-mode")
		}
	}
	pending, journal := f.interruptedStage(t, "kubernetes/service-managed-core-identity-lock", definitions, account)
	if len(journal.Completed) != 14 || len(journal.Owners) != 15 {
		t.Fatalf("missing original failed rollout boundary: completed %d owners %d", len(journal.Completed), len(journal.Owners))
	}
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.protocol["logs"] = map[string]string{"account-manager-pod": "Service identity already owned: resource temporarily unavailable"}
	f.protocol["issued"] = []string{"service:account-manager", "service:certissuer", "service:pki-controller"}
	f.publish(t)
	return f, &createServiceSessionRecovery{PendingManagedKey: "kubernetes/service-managed-core-identity-lock", PendingManagedPlan: pending, PendingManagedJournal: journal, Ceremony: f.service}
}

func TestIdentityLockRecoveryExecutionContractChangesOnlyOriginalAccountRollout(t *testing.T) {
	f, r := newIdentityLockRecoveryExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := deploymentplan.Digest(f.native.writes)
	p := f.p
	p.Materials = map[string]string{}
	if err := prepareApplicationIdentityLockRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
		t.Fatal("genuine retained identity lock rejected", err)
	}
	if r.Candidate == nil || len(r.Candidate.Resources) != 15 || p.Materials["accepted-service-core-identity-lock-forward-plan"] != r.Candidate.SHA256() {
		t.Fatal("repair lost exact original cohort binding")
	}
	updates := 0
	for _, resource := range r.Candidate.Resources {
		if resource.Action == "update" {
			updates++
			if resource.ID != r.PendingManagedJournal.Failed {
				t.Fatal("repair changed another owner")
			}
			var expected deploymentplan.Object
			for _, old := range r.PendingManagedPlan.Resources {
				if old.ID == resource.ID {
					expected = deploymentplan.Clone(old.Desired)
				}
			}
			setEnvironmentCreateExclusiveAccountOwner(expected)
			if deploymentplan.Digest(expected) != deploymentplan.Digest(resource.Desired) {
				t.Fatal("repair changed more than exclusive rollout strategy")
			}
		} else if resource.Action != "retain" {
			t.Fatal("repair recreated an existing core identity")
		}
	}
	if updates != 1 || writes != deploymentplan.Digest(f.native.writes) {
		t.Fatal("qualification performed writes or lost exclusive change")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestIdentityLockRecoveryExecutionContractRejectsChangedNativeOwnerAndFailure(t *testing.T) {
	for _, mode := range []string{"foreign-pod-owner", "foreign-replicaset", "different-failure", "duplicate-original-pod", "unknown-issued-subject", "other-core-drift"} {
		t.Run(mode, func(t *testing.T) {
			f, r := newIdentityLockRecoveryExecutionContract(t)
			objects := f.protocol["objects"].(map[string]deploymentplan.Object)
			podID := "Pod/" + f.p.Plan.Stack + "-account-manager/account-manager-pod"
			pod := objects[podID]
			switch mode {
			case "foreign-pod-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "foreign-replicaset"
			case "foreign-replicaset":
				rs := objects["ReplicaSet/"+f.p.Plan.Stack+"-account-manager/account-manager-isolated"]
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(rs["metadata"])["ownerReferences"])[0])["uid"] = "foreign-deployment"
			case "different-failure":
				f.protocol["logs"].(map[string]string)["account-manager-pod"] = "database connection refused"
			case "duplicate-original-pod":
				extra := deploymentplan.Clone(pod)
				deploymentplan.Map(extra["metadata"])["name"] = "account-manager-second-pod"
				deploymentplan.Map(extra["metadata"])["uid"] = "isolated-other-pod"
				objects[deploymentplan.ID(extra)] = extra
				f.protocol["logs"].(map[string]string)["account-manager-second-pod"] = "Service identity already owned: resource temporarily unavailable"
			case "unknown-issued-subject":
				f.protocol["issued"] = []string{"service:account-manager", "service:api-service", "service:certissuer", "service:pki-controller"}
			case "other-core-drift":
				controller := objects["Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller"]
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(controller)["containers"])[0])
				c["image"] = "ghcr.io/isolated/changed@sha256:" + strings.Repeat("a", 64)
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			if err := prepareApplicationIdentityLockRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err == nil {
				t.Fatal("changed original identity lock source accepted", mode)
			}
			if p.Materials["accepted-service-core-identity-lock-forward-plan"] != "" {
				t.Fatal("rejected source produced bound executable plan")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

func TestIdentityLockRecoveryExecutionContractExecutesAndResumesOriginalOwner(t *testing.T) {
	for _, interruption := range []string{"none", "after-write-workload"} {
		t.Run(interruption, func(t *testing.T) {
			f, r := newIdentityLockRecoveryExecutionContract(t)
			p := f.p
			p.Materials = map[string]string{}
			if err := prepareApplicationIdentityLockRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
				t.Fatal(err)
			}
			r.CoreAccepted, r.ApplicationForward, r.IdentityLockForward = true, true, true
			r.Review = deploymentplan.Digest(p)
			runtime := f.runtime
			var err error
			runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(f.cfg, runtime.InitialListeners)
			if err != nil {
				t.Fatal(err)
			}
			runtime.ServiceSessionRecovery = r
			runtime.Backend = f.backend
			originalOwners := map[string]string{}
			originalWrites := map[string]int{}
			for _, resource := range r.Candidate.Resources {
				originalOwners[resource.ID] = deploymentplan.String(deploymentplan.Map(f.native.objects[resource.ID]["metadata"])["uid"])
				originalWrites[resource.ID] = f.native.writes[resource.ID]
			}
			expectedStage := "service-managed-core-identity-recovery-" + r.Review
			var executed *deploymentplan.Plan
			apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
				if stage != expectedStage {
					return fmt.Errorf("unexpected recovery stage: %s", stage)
				}
				// Qualify the renderer's normalized desired cohort, then keep the
				// original compiled execution input across readiness interruptions.
				var live []deploymentplan.Object
				for _, o := range f.native.objects {
					live = append(live, o)
				}
				fresh, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, live)
				if err != nil {
					return err
				}
				wanted := map[string]string{}
				for _, resource := range r.Candidate.Resources {
					wanted[resource.ID] = deploymentplan.Digest(resource.Desired)
				}
				for _, resource := range fresh.Resources {
					if wanted[resource.ID] != deploymentplan.Digest(resource.Desired) {
						return fmt.Errorf("qualified recipe changed: %s", resource.ID)
					}
					delete(wanted, resource.ID)
				}
				if len(wanted) != 0 {
					return fmt.Errorf("qualified owners omitted")
				}
				executed, err = readEnvironmentCreateStageInput(f.store, f.e, stage)
				if err != nil {
					return err
				}
				if executed == nil {
					executed = fresh
				}
				check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
				accept := func(ctx context.Context, child *deploymentplan.Plan) error {
					if err := f.e.Check(ctx); err != nil {
						return err
					}
					for _, resource := range child.Resources {
						current, err := f.native.Read(ctx, resource.ID)
						if err != nil || current == nil || !deploymentplan.Matches(resource.Desired, current) || deploymentplan.Map(current["metadata"])["uid"] != originalOwners[resource.ID] {
							return fmt.Errorf("original owner or desired body changed: %s", resource.ID)
						}
					}
					return nil
				}
				return executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, executed, f.native, check, check, accept, f.e.Check, io.Discard)
			}
			if interruption != "none" {
				f.native.failure = interruption
			}
			actual, err := executeEnvironmentCreateServiceBootstrap(context.Background(), f.cfg, p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
			if interruption != "none" {
				if err == nil || !f.native.failed || f.native.held {
					t.Fatal("expected readiness interruption with released lease", err)
				}
				if entry := createOpenBaoACKCompletionEntry(f.e.Journal, r.PendingManagedKey); entry == nil || entry.State != "pending" {
					t.Fatal("failed forward prematurely completed original stage")
				}
				actual, err = executeEnvironmentCreateServiceBootstrap(context.Background(), f.cfg, p.Plan.Bootstrap, f.store, f.operator, runtime, apply)
			}
			if err != nil {
				t.Fatal("identity recovery did not complete original stage", err)
			}
			if deploymentplan.Digest(actual) != deploymentplan.Digest(f.service) {
				t.Fatal("identity recovery changed original session or authority")
			}
			_, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+expectedStage)
			if err != nil || !journal.Accepted || len(journal.Owners) != 15 {
				t.Fatal("forward child lacks accepted original owners", err)
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, r.PendingManagedKey)
			var receipt map[string]string
			if entry == nil || entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt["actual_qualified_forward_stage"] != "kubernetes/"+expectedStage || receipt["original_failed_plan_sha256"] != r.PendingManagedPlan.SHA256() || receipt["renewed_session_id"] != f.session {
				t.Fatal("original failure lost exact forward completion receipt")
			}
			for id, writes := range originalWrites {
				expected := writes
				if id == r.PendingManagedJournal.Failed {
					expected++
				}
				if f.native.writes[id] != expected || journal.Owners[id] != originalOwners[id] {
					t.Fatal("resume replayed a write or replaced a permanent owner", id)
				}
			}
		})
	}
}
