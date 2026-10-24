package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type foundationExecutionContract struct {
	cfg      deploymentConfig
	store    secretStore
	e        *environmentCreateExecution
	artifact environmentCreateProviderRecoveryArtifact
	child    *deploymentplan.Plan
	journal  deploymentplan.Journal
	backend  *normalTailE2EBackend
	reviews  []string
}

// The historical recovery contract has 33 original writes and stops at the
// final OpenBao readiness check. The real executor records every owner, wait
// outcome and lease; only the Kubernetes effects are synthetic.
type foundationExecutionContractInputs struct {
	provider providerExecutionContractInputs
	bindings func(secretStore, *environmentCreateExecution) []deploymentplan.Definition
}

func foundationExecutionContractFixture(t *testing.T, retries int) *foundationExecutionContract {
	t.Helper()
	return foundationExecutionContractFixtureWithInputs(t, retries)
}

func foundationExecutionContractFixtureWithInputs(t *testing.T, retries int, inputs ...foundationExecutionContractInputs) *foundationExecutionContract {
	t.Helper()
	if len(inputs) > 1 {
		t.Fatal("one original foundation input required")
	}
	var selected []providerExecutionContractInputs
	if len(inputs) == 1 {
		selected = append(selected, inputs[0].provider)
	}
	cfg, store, e, _, plan := providerExecutionContractFixture(t, selected...)
	e, _ = providerExecutionContractFork(t, e, plan, "")
	providerExecutionContractCanaries(t, cfg, e)
	e, artifact := providerExecutionContractFork(t, e, plan, "completed-storage-canary")
	f := &foundationExecutionContract{cfg: cfg, store: store, e: e, artifact: artifact}
	var definitions []deploymentplan.Definition
	ns := plan.Stack + "-secrets"
	for i := 0; i < 32; i++ {
		definitions = append(definitions, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": fmt.Sprintf("original-foundation-%02d", i)}, "data": deploymentplan.Object{"owner": "original"}}})
	}
	definitions = append(definitions, deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": ns, "name": "openbao"}, "spec": deploymentplan.Object{"replicas": float64(1), "serviceName": "openbao", "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "openbao"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "openbao"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": "docker.io/openbao/openbao@sha256:" + deploymentplan.Digest("isolated image")}}}}}}})
	if len(inputs) == 1 && inputs[0].bindings != nil {
		bindings := inputs[0].bindings(store, e)
		if len(bindings) != 7 {
			t.Fatal("original transport and signer require exactly seven Secret bindings")
		}
		// Keep the explicit historical inventory boundary at33 while selecting
		// actual original transport/signer bodies before any original write.
		set := definitions[len(definitions)-1]
		definitions = append(definitions[:25], bindings...)
		definitions = append(definitions, set)
	}
	f.backend = &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, routes: map[string]string{}}
	producer := &acceptedCoreExecutionContract{cfg: cfg, store: store, e: e, p: createFoundationRecoveryPlan{Plan: plan}, native: f.backend}
	f.child, f.journal = producer.historicalSerialInterruptedStage(t, "kubernetes/foundation", definitions, "StatefulSet/"+ns+"/openbao")
	if f.journal.Failed != "StatefulSet/"+ns+"/openbao" || len(f.journal.Completed) != 32 || len(f.journal.Owners) != 33 || f.backend.held {
		t.Fatalf("historical producer did not preserve actual serial readiness outcome: failed=%s completed=%d owners=%d held=%v", f.journal.Failed, len(f.journal.Completed), len(f.journal.Owners), f.backend.held)
	}
	for i := 0; i < retries; i++ {
		f.addRetainedReadinessAttempt(t, i)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *foundationExecutionContract) write(t *testing.T, path string, v any) {
	t.Helper()
	if err := f.store.write(path, mustProviderRecoveryJSON(v), true); err != nil {
		t.Fatal(err)
	}
}

func (f *foundationExecutionContract) addRetainedReadinessAttempt(t *testing.T, attempt int) {
	t.Helper()
	source := f.e.Journal
	now := time.Now().UTC()
	p := createFoundationRecoveryPlan{Schema: "rtk-cloud.create-foundation-recovery-plan/v1", ParentSHA: f.e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(f.e.owner), ParentJournalSHA: deploymentplan.Digest(source), FoundationSHA: f.child.SHA256(), FoundationJournalSHA: deploymentplan.Digest(f.journal), Plan: f.artifact.Plan.Plan, Materials: map[string]string{"attempt": deploymentplan.Digest(attempt)}}
	review := deploymentplan.Digest(p)
	artifact := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-foundation-recovery-artifact/v1", Plan: p, Qualification: deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	base := filepath.Join("deployment/create/foundation-recovery", review)
	f.write(t, filepath.Join(base, "qualification.json"), artifact)
	f.write(t, filepath.Join(base, "source-journal.json"), source)
	var definitions []deploymentplan.Definition
	var observed []deploymentplan.Object
	for _, resource := range f.child.Resources {
		definitions = append(definitions, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Retain: true})
		observed = append(observed, deploymentplan.Clone(f.backend.objects[resource.ID]))
	}
	in := f.child.Inputs
	in.Operation = "scoped"
	in.BindingsSHA256 = deploymentplan.Digest([]any{review, observed})
	retained, err := deploymentplan.Compile(in, definitions, observed)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry("foundation-recovery-admission-"+review, "bootstrap", mustProviderRecoveryJSON(artifact), now); err != nil {
		t.Fatal(err)
	}
	intent, err := environmentCreateKubernetesStageIntent(retained)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry("kubernetes/foundation-recovery-"+review, "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	beforeWrites := deploymentplan.Digest(f.backend.writes)
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	acceptance := func(context.Context, *deploymentplan.Plan) error {
		return &deploymentplan.Blocker{ID: "deployment.post-deploy.workloads", Cause: "isolated retained readiness interruption"}
	}
	backend := historicalAcceptedScopedBackend{normalTailE2EBackend: f.backend, parent: environmentCreateKubernetesBackend{execution: f.e, cfg: f.cfg, store: f.store}, parentSHA: f.e.Journal.PlanSHA256}
	err = retained.Execute(context.Background(), backend, retained.SHA256(), f.store.Root, check, check, acceptance)
	var refusal *deploymentplan.Blocker
	if !errors.As(err, &refusal) || refusal.ID != "deployment.post-deploy.workloads" {
		t.Fatal("retained retry did not reach its actual readiness boundary", err)
	}
	if deploymentplan.Digest(f.backend.writes) != beforeWrites {
		t.Fatal("retained readiness retry rewrote original Kubernetes owners")
	}
	f.reviews = append(f.reviews, review)
}

func TestFoundationExecutionContractReopensRecordedWriteAndRetainedReadinessFailures(t *testing.T) {
	for _, attempts := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(attempts), func(t *testing.T) {
			f := foundationExecutionContractFixture(t, attempts)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			loaded, artifact, child, journal, err := loadStoppedFoundationCreate(f.cfg, f.store, f.e.Journal.PlanSHA256)
			if err != nil {
				t.Fatal("authentic stopped foundation could not be qualified", err)
			}
			if deploymentplan.Digest(artifact) != deploymentplan.Digest(f.artifact) || child.SHA256() != f.child.SHA256() || deploymentplan.Digest(journal) != deploymentplan.Digest(f.journal) || deploymentplan.Digest(loaded.Journal) != deploymentplan.Digest(f.e.Journal) {
				t.Fatal("recovery changed the original write boundary")
			}
			if _, _, _, _, err := loadStoppedFoundationCreate(f.cfg, f.store, f.e.Journal.PlanSHA256); err == nil {
				t.Fatal("another process acquired foundation owner during recovery")
			}
			_ = loaded.Close()
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

func TestFoundationExecutionContractRejectsUnknownWritesAndAlteredRetainedHistory(t *testing.T) {
	for _, mode := range []string{"owner-token", "completed-parent", "unknown-original-write", "current-phase-barrier", "changed-completed-order", "changed-original-intent", "foreign-stage", "unknown-prior-write", "prior-accepted", "prior-owner", "prior-source", "prior-admission-time", "prior-intent", "too-many-retries"} {
		t.Run(mode, func(t *testing.T) {
			retries := 1
			if mode == "too-many-retries" {
				retries = 3
			}
			f := foundationExecutionContractFixture(t, retries)
			originalJournalPath := filepath.Join("deployment/rollback", f.child.SHA256(), "journal.json")
			switch mode {
			case "owner-token":
				owner := f.e.owner
				owner.OwnerToken = "foreign"
				f.write(t, "deployment/.execution-active/owner.json", owner)
			case "completed-parent":
				f.e.Journal.CompletedAt = time.Now().UTC()
			case "current-phase-barrier":
				j := f.journal
				j.Completed = append(append([]string(nil), j.Completed...), j.Failed)
				f.write(t, originalJournalPath, j)
			case "unknown-original-write":
				j := f.journal
				j.InFlight = j.Failed
				f.write(t, originalJournalPath, j)
			case "changed-completed-order":
				j := f.journal
				j.Completed = append([]string(nil), j.Completed...)
				j.Completed[0], j.Completed[1] = j.Completed[1], j.Completed[0]
				f.write(t, originalJournalPath, j)
			case "changed-original-intent":
				for i := range f.e.Journal.Entries {
					if f.e.Journal.Entries[i].Key == "kubernetes/foundation" {
						f.e.Journal.Entries[i].IntentSHA256 = deploymentplan.Digest("foreign")
					}
				}
			case "foreign-stage":
				f.e.Journal.Entries[0].Phase = "unplanned-bootstrap"
			case "prior-source":
				var j environmentCreateJournal
				path := filepath.Join("deployment/create/foundation-recovery", f.reviews[0], "source-journal.json")
				if err := readExecutionJSON(filepath.Join(f.store.Root, path), &j); err != nil {
					t.Fatal(err)
				}
				j.Entries[0].IntentSHA256 = deploymentplan.Digest("foreign")
				f.write(t, path, j)
			case "prior-admission-time":
				f.e.Journal.Entries[len(f.e.Journal.Entries)-2].StartedAt = time.Now().UTC().Add(-2 * time.Hour)
			case "prior-intent":
				f.e.Journal.Entries[len(f.e.Journal.Entries)-1].IntentSHA256 = deploymentplan.Digest("foreign")
			case "unknown-prior-write", "prior-accepted", "prior-owner":
				sha := f.e.Journal.KubernetesLeases[1].Holder[:64]
				path := filepath.Join("deployment/rollback", sha, "journal.json")
				var j deploymentplan.Journal
				if err := readExecutionJSON(filepath.Join(f.store.Root, path), &j); err != nil {
					t.Fatal(err)
				}
				if mode == "unknown-prior-write" {
					j.InFlight = j.Completed[0]
				}
				if mode == "prior-accepted" {
					j.Accepted = true
				}
				if mode == "prior-owner" {
					j.Owners[j.Completed[0]] = "replacement"
				}
				f.write(t, path, j)
			}
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if loaded, _, _, _, err := loadStoppedFoundationCreate(f.cfg, f.store, f.e.Journal.PlanSHA256); err == nil {
				_ = loaded.Close()
				t.Fatal("changed or unknown foundation history was admitted")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
			if err != nil {
				t.Fatal("rejected recovery retained process lock", err)
			}
			_ = lock.Close()
		})
	}
}

func TestFoundationExecutionContractMaterialReadPreservesOriginalAuthorities(t *testing.T) {
	for _, mode := range []string{"original", "expired-transport", "missing-ca", "changed-ca", "missing-signer", "foreign-inventory", "missing-recorded-runtime", "missing-allocation"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			parent := deploymentplan.Digest("isolated foundation parent")
			now := time.Now().UTC()
			if mode == "expired-transport" {
				now = now.Add(-5 * time.Hour)
			}
			var management environmentCreateTransport
			for _, role := range []string{"management", "openbao"} {
				material, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now)
				if err != nil {
					t.Fatal(err)
				}
				if role == "management" {
					management = material
				}
			}
			if _, err := ensureEnvironmentCreateAccountSigner(store); err != nil {
				t.Fatal(err)
			}
			inventory := secretInventory{SchemaVersion: secretInventorySchemaVersion, Environment: "dev"}
			if err := store.write("inventory.json", mustProviderRecoveryJSON(inventory), true); err != nil {
				t.Fatal(err)
			}
			if err := store.write("deployment/storage/create-allocation.json", []byte(`{"isolated":"original allocation"}`), false); err != nil {
				t.Fatal(err)
			}
			path := ""
			switch mode {
			case "missing-ca":
				path = filepath.Join("deployment/create/material", parent, "openbao-transport/ca.crt")
			case "changed-ca":
				if err := store.write(filepath.Join("deployment/create/material", parent, "openbao-transport/ca.crt"), []byte(management.CACert), true); err != nil {
					t.Fatal(err)
				}
			case "missing-signer":
				path = "pki/account-manager-jwt/access.key"
			case "foreign-inventory":
				inventory.Environment = "staging"
				if err := store.write("inventory.json", mustProviderRecoveryJSON(inventory), true); err != nil {
					t.Fatal(err)
				}
			case "missing-recorded-runtime":
				inventory.Entries = append(inventory.Entries, secretInventoryEntry{ID: "postgres", Path: "runtime/postgres", Category: "runtime"})
				if err := store.write("inventory.json", mustProviderRecoveryJSON(inventory), true); err != nil {
					t.Fatal(err)
				}
			case "missing-allocation":
				path = "deployment/storage/create-allocation.json"
			}
			if path != "" {
				if err := os.Remove(filepath.Join(store.Root, path)); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			hashes, actual, err := foundationRecoveryMaterials(store, parent, time.Minute)
			if mode == "original" {
				if err != nil || len(hashes) != 11 || deploymentplan.Digest(actual) != deploymentplan.Digest(management) {
					t.Fatal("original foundation materials rejected", len(hashes), err)
				}
				for relative, digest := range hashes {
					if relative == "runtime-snapshot" {
						continue
					}
					raw, err := os.ReadFile(filepath.Join(store.Root, relative))
					if err != nil || environmentPreflightBytesSHA256(raw) != digest {
						t.Fatal("material digest did not bind original private bytes", relative, err)
					}
				}
			} else if err == nil {
				t.Fatal("invalid/missing original foundation material was accepted")
			}
			assertRecoveryContractReadOnly(t, store, before)
			if path != "" {
				if _, err := os.Lstat(filepath.Join(store.Root, path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing original material was regenerated")
				}
			}
		})
	}
}
