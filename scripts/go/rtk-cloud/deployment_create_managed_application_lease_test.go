package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func managedApplicationLeaseFixture(t *testing.T) (*environmentCreateExecution, environmentKubernetesBackend, *createServiceSessionRecovery, deploymentplan.Object) {
	return managedApplicationLeaseCheckpointFixture(t, false)
}

func managedApplicationLeaseCheckpointFixture(t *testing.T, latest bool) (*environmentCreateExecution, environmentKubernetesBackend, *createServiceSessionRecovery, deploymentplan.Object) {
	depth := 1
	if latest {
		depth = 2
	}
	return managedApplicationLeaseDepthFixture(t, depth)
}

func managedApplicationLeaseDepthFixture(t *testing.T, depth int) (*environmentCreateExecution, environmentKubernetesBackend, *createServiceSessionRecovery, deploymentplan.Object) {
	t.Helper()
	fixture := managedApplicationInterruptedFixture
	if depth == 2 {
		fixture = managedApplicationLatestInterruptedFixture
	}
	if depth == 3 {
		fixture = managedApplicationThirdInterruptedFixture
	}
	store, e, partial, forward := fixture(t)
	e.Store = store
	uid := "3fb6bc62-0643-4795-9bde-e3c96ca08967"
	if depth > 1 {
		uid = "9bd490f6-6dfa-46cc-87fb-49e9e50bc09b"
	}
	e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].UID = uid
	partial.Interrupted = forward
	relative := filepath.Join("deployment/create/executions", e.Journal.PlanSHA256, "execution-isolated-lease", "journal.json")
	e.JournalPath = filepath.Join(store.Root, relative)
	e.Directory = filepath.Dir(e.JournalPath)
	e.owner = environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: store.Environment, Stack: e.Journal.Stack, PlanSHA256: e.Journal.PlanSHA256, OwnerToken: e.Journal.OwnerToken, Journal: filepath.ToSlash(relative), StartedAt: time.Now().UTC()}
	if err := store.write(relative, mustProviderRecoveryJSON(e.Journal), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(e.owner), false); err != nil {
		t.Fatal(err)
	}
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	var err error
	e.running, err = deploymentplan.LockExecutionOwner(fence, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		t.Fatal(err)
	}
	e.runningInfo, err = e.running.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Check(context.Background()); err != nil {
		t.Fatal("exclusive isolated parent unavailable", err)
	}
	owner := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
	lease := environmentExecutionLeaseBody(forward.Plan, owner.Holder)
	deploymentplan.Map(lease["metadata"])["uid"] = owner.UID
	deploymentplan.Map(lease["metadata"])["resourceVersion"] = "12345"
	path := filepath.Join(t.TempDir(), "lease.json")
	if err = os.WriteFile(path, mustProviderRecoveryJSON(lease), 0600); err != nil {
		t.Fatal(err)
	}
	commandDir := t.TempDir()
	script := `#!/bin/sh
if test "$RTK_TEST_INTERRUPTED_LEASE_FAIL" = yes; then exit 1; fi
if test -s "$RTK_TEST_INTERRUPTED_LEASE_FILE"; then cat "$RTK_TEST_INTERRUPTED_LEASE_FILE"; fi
`
	if err = os.WriteFile(filepath.Join(commandDir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TEST_INTERRUPTED_LEASE_FILE", path)
	t.Setenv("RTK_TEST_INTERRUPTED_LEASE_FAIL", "")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	return e, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-frozen-kubeconfig")}, &createServiceSessionRecovery{ManagedApplicationPartial: partial}, lease
}

func TestManagedApplicationInterruptedLeaseRequiresExactRecordedNativeOwner(t *testing.T) {
	_, _, _, forward := managedApplicationInterruptedFixture(t)
	owner := deploymentplan.LeaseOwner{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "3fb6bc62-0643-4795-9bde-e3c96ca08967", Holder: forward.Plan.SHA256() + ":original"}
	original := environmentExecutionLeaseBody(forward.Plan, owner.Holder)
	deploymentplan.Map(original["metadata"])["uid"] = owner.UID
	deploymentplan.Map(original["metadata"])["resourceVersion"] = "12345"
	for _, mode := range []string{"original", "absent", "wrong-uid", "wrong-holder", "wrong-plan", "wrong-environment", "wrong-stack", "wrong-manager", "wrong-namespace", "wrong-name", "wrong-api", "missing-version", "terminating", "ancient-acquire"} {
		t.Run(mode, func(t *testing.T) {
			actual := deploymentplan.Clone(original)
			meta := deploymentplan.Map(actual["metadata"])
			switch mode {
			case "absent":
				actual = nil
			case "wrong-uid":
				meta["uid"] = "88888888-8888-4888-8888-888888888888"
			case "wrong-holder":
				deploymentplan.Map(actual["spec"])["holderIdentity"] = forward.Plan.SHA256() + ":foreign"
			case "wrong-plan":
				deploymentplan.Map(meta["annotations"])["rtk.realtek.com/plan-sha256"] = strings.Repeat("f", 64)
			case "wrong-environment":
				deploymentplan.Map(meta["annotations"])["rtk.realtek.com/environment"] = "staging"
			case "wrong-stack":
				deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] = "video-cloud-staging"
			case "wrong-manager":
				deploymentplan.Map(meta["labels"])["app.kubernetes.io/managed-by"] = "other"
			case "wrong-namespace":
				meta["namespace"] = "video-cloud-staging-video-cloud"
			case "wrong-name":
				meta["name"] = "another-lease"
			case "wrong-api":
				actual["apiVersion"] = "v1"
			case "missing-version":
				delete(meta, "resourceVersion")
			case "terminating":
				meta["deletionTimestamp"] = time.Now().Format(time.RFC3339)
			case "ancient-acquire":
				deploymentplan.Map(actual["spec"])["acquireTime"] = "2000-01-01T00:00:00Z"
				meta["uid"] = "88888888-8888-4888-8888-888888888888"
			}
			if (validateManagedApplicationInterruptedLease(forward.Plan, owner, actual) == nil) != (mode == "original" || mode == "absent") {
				t.Fatal("foreign owner or elapsed time authorized release", mode)
			}
		})
	}
}

func TestManagedApplicationInterruptedLeaseQualificationAndReadbackAreReadOnly(t *testing.T) {
	for _, mode := range []string{"present", "absent", "unreadable", "replaced", "version-changed", "appeared-after-absence", "source-changed", "typed-child-changed", "no-exclusive-owner"} {
		t.Run(mode, func(t *testing.T) {
			e, b, r, lease := managedApplicationLeaseFixture(t)
			p := &createFoundationRecoveryPlan{Materials: map[string]string{}}
			if mode == "absent" || mode == "appeared-after-absence" {
				if err := os.WriteFile(os.Getenv("RTK_TEST_INTERRUPTED_LEASE_FILE"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unreadable" {
				t.Setenv("RTK_TEST_INTERRUPTED_LEASE_FAIL", "yes")
			}
			if mode == "source-changed" {
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "typed-child-changed" {
				r.ManagedApplicationPartial.Interrupted.Journal.Failed = r.ManagedApplicationPartial.Interrupted.Plan.Resources[22].ID
			}
			if mode == "no-exclusive-owner" {
				if err := e.running.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := deploymentplan.Digest(e.Journal)
			err := qualifyManagedApplicationInterruptedLease(context.Background(), e, b, r, p)
			pass := mode == "present" || mode == "absent" || mode == "replaced" || mode == "version-changed" || mode == "appeared-after-absence"
			if (err == nil) != pass {
				t.Fatal("qualification authority differs", mode, err)
			}
			if before != deploymentplan.Digest(e.Journal) {
				t.Fatal("qualification persisted release intent")
			}
			if !pass {
				return
			}
			sha := r.ManagedApplicationPartial.Interrupted.LeaseSHA
			if p.Materials[managedApplicationInterruptedLeaseMaterial] != sha || !environmentPreflightDigest.MatchString(sha) {
				t.Fatal("snapshot not material bound")
			}
			switch mode {
			case "replaced":
				deploymentplan.Map(lease["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
			case "version-changed":
				deploymentplan.Map(lease["metadata"])["resourceVersion"] = "12346"
			}
			if mode == "replaced" || mode == "version-changed" || mode == "appeared-after-absence" {
				if err := os.WriteFile(os.Getenv("RTK_TEST_INTERRUPTED_LEASE_FILE"), mustProviderRecoveryJSON(lease), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyManagedApplicationInterruptedLease(context.Background(), e, b, r)
			if (err == nil) != (mode == "present" || mode == "absent") {
				t.Fatal("changed snapshot accepted", mode, err)
			}
		})
	}
}

func admitManagedApplicationLeaseFixture(t *testing.T, e *environmentCreateExecution, r *createServiceSessionRecovery) {
	schema := "rtk-cloud.create-managed-application-interrupted-recovery/v1"
	if r.ManagedApplicationPartial.Interrupted.Previous != nil {
		schema = "rtk-cloud.create-managed-application-app-role-recovery/v1"
		if r.ManagedApplicationPartial.Interrupted.Previous.Previous != nil {
			schema = "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1"
		}
	}
	admitManagedApplicationLeaseFixtureWithSchema(t, e, r, schema)
}

func admitManagedApplicationLeaseFixtureWithSchema(t *testing.T, e *environmentCreateExecution, r *createServiceSessionRecovery, schema string) {
	t.Helper()
	source := e.Journal
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: schema, ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{managedApplicationInterruptedLeaseMaterial: r.ManagedApplicationPartial.Interrupted.LeaseSHA}}}
	if proof := r.ManagedHistoricalSeedCleanup; proof != nil {
		a.Plan.Materials[managedHistoricalSeedCleanupMaterial] = deploymentplan.Digest(proof)
		a.Plan.Materials["accepted-service-core-managed-application-current-clients"] = proof.ClientsSHA
	}
	r.Review = deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: r.Review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: r.Review})
	}
	dir := filepath.Join("deployment/create/openbao-recovery", r.Review)
	if err := e.Store.write(filepath.Join(dir, "qualification.json"), mustProviderRecoveryJSON(a), false); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.write(filepath.Join(dir, "source-journal.json"), mustProviderRecoveryJSON(source), false); err != nil {
		t.Fatal(err)
	}
	key := "openbao-recovery-admission-" + r.Review
	if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(map[string]string{"review_sha256": r.Review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": r.Review})); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

type managedApplicationLeaseRaceAPI struct {
	executionLeaseAPIFixture
	reads                   int
	changeVersion, reappear bool
}

func (a *managedApplicationLeaseRaceAPI) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	a.reads++
	if a.changeVersion && a.reads == 2 {
		deploymentplan.Map(a.object["metadata"])["resourceVersion"] = "12346"
	}
	if a.reappear && a.deletes == 1 && a.object == nil {
		a.object = deploymentplan.Object{"kind": "Lease", "metadata": deploymentplan.Object{"name": "rtk-deployment-execution", "namespace": "video-cloud-dev-video-cloud", "uid": "concurrent-owner"}, "spec": deploymentplan.Object{"holderIdentity": "concurrent-holder"}}
	}
	return a.executionLeaseAPIFixture.Read(ctx, id)
}

func TestManagedApplicationInterruptedLeaseReleaseIsDurableAndNeverDeletesChangedOwner(t *testing.T) {
	for _, mode := range []string{"success", "qualified-absent", "absent-without-intent", "wrong-owner", "version-race", "delete-race", "reappeared", "unreadable", "extra-suffix", "wrong-material"} {
		t.Run(mode, func(t *testing.T) {
			e, b, r, lease := managedApplicationLeaseFixture(t)
			r.ManagedApplicationPartial.Interrupted.LeaseSHA = deploymentplan.Digest(lease)
			if mode == "qualified-absent" {
				r.ManagedApplicationPartial.Interrupted.LeaseSHA = deploymentplan.Digest(deploymentplan.Object(nil))
			}
			admitManagedApplicationLeaseFixture(t, e, r)
			api := &managedApplicationLeaseRaceAPI{executionLeaseAPIFixture: executionLeaseAPIFixture{object: deploymentplan.Clone(lease)}}
			switch mode {
			case "qualified-absent", "absent-without-intent":
				api.object = nil
			case "wrong-owner":
				deploymentplan.Map(api.object["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
			case "version-race":
				api.changeVersion = true
			case "delete-race":
				api.replaceOnDelete = true
			case "reappeared":
				api.reappear = true
			case "unreadable":
				api.fail = true
			case "extra-suffix":
				if err := e.BeginEntry("unrelated", "other", json.RawMessage(`{"other":true}`), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			case "wrong-material":
				r.ManagedApplicationPartial.Interrupted.LeaseSHA = strings.Repeat("f", 64)
			}
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}
			before := len(e.Journal.Entries)
			err := releaseManagedApplicationInterruptedLeaseWithBackend(context.Background(), runtime, api)
			pass := mode == "success" || mode == "qualified-absent"
			if (err == nil) != pass {
				t.Fatal("release authority differs", mode, err)
			}
			if api.writes != 0 || api.dry != 0 || !pass && mode != "reappeared" && api.deletes != 0 {
				t.Fatal("changed or unqualified owner deleted", mode)
			}
			if mode == "qualified-absent" && (len(e.Journal.Entries) != before || api.deletes != 0) {
				t.Fatal("qualified absence fabricated release attempt")
			}
			if mode == "success" {
				if api.deletes != 1 || api.object != nil || len(e.Journal.Entries) != before+1 {
					t.Fatal("exact release not confirmed")
				}
				last := e.Journal.Entries[len(e.Journal.Entries)-1]
				var receipt managedApplicationInterruptedLeaseReceipt
				if last.State != "completed" || decodeEnvironmentJSON(last.Receipt, &receipt) != nil || !receipt.Absent || receipt.LeaseSHA != r.ManagedApplicationPartial.Interrupted.LeaseSHA || receipt.Lease.UID != deploymentplan.Map(lease["metadata"])["uid"] {
					t.Fatal("missing original UID absence receipt")
				}
				if err := releaseManagedApplicationInterruptedLeaseWithBackend(context.Background(), runtime, api); err != nil || api.deletes != 1 {
					t.Fatal("completed cleanup replayed delete", err)
				}
			}
			if mode == "version-race" || mode == "delete-race" || mode == "reappeared" {
				if len(e.Journal.Entries) != before+1 || e.Journal.Entries[len(e.Journal.Entries)-1].State != "pending" || api.object == nil {
					t.Fatal("uncertain outcome marked completed or concurrent owner lost")
				}
			}
		})
	}
}

func TestManagedApplicationLatestInterruptedLeaseUsesAbsentCurrentOwnerAndExactPreviousProof(t *testing.T) {
	for _, mode := range []string{"original", "old-schema", "appeared", "old-lease-reused", "changed-previous", "changed-previous-lease"} {
		t.Run(mode, func(t *testing.T) {
			e, b, r, lease := managedApplicationLeaseCheckpointFixture(t, true)
			if err := os.WriteFile(os.Getenv("RTK_TEST_INTERRUPTED_LEASE_FILE"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			p := &createFoundationRecoveryPlan{Materials: map[string]string{}}
			if err := qualifyManagedApplicationInterruptedLease(context.Background(), e, b, r, p); err != nil {
				t.Fatal("genuine latest absent snapshot refused", err)
			}
			current := r.ManagedApplicationPartial.Interrupted
			if current.LeaseSHA != deploymentplan.Digest(deploymentplan.Object(nil)) || p.Materials[managedApplicationInterruptedLeaseMaterial] != current.LeaseSHA {
				t.Fatal("absence not qualified")
			}
			if mode == "changed-previous" {
				current.Previous.SourceSHA = strings.Repeat("f", 64)
			}
			if mode == "changed-previous-lease" {
				current.Previous.LeaseSHA = strings.Repeat("f", 64)
			}
			if mode == "changed-previous" || mode == "changed-previous-lease" {
				if err := verifyManagedApplicationInterruptedLease(context.Background(), e, b, r); err == nil {
					t.Fatal("unbound Previous metadata accepted")
				}
				return
			}
			if err := verifyManagedApplicationInterruptedLease(context.Background(), e, b, r); err != nil {
				t.Fatal(err)
			}
			if mode == "old-schema" {
				admitManagedApplicationLeaseFixtureWithSchema(t, e, r, "rtk-cloud.create-managed-application-interrupted-recovery/v1")
			} else {
				admitManagedApplicationLeaseFixture(t, e, r)
			}
			api := &executionLeaseAPIFixture{}
			if mode == "appeared" {
				api.object = deploymentplan.Clone(lease)
			}
			if mode == "old-lease-reused" {
				api.object = deploymentplan.Clone(lease)
				deploymentplan.Map(api.object["metadata"])["uid"] = "3fb6bc62-0643-4795-9bde-e3c96ca08967"
				deploymentplan.Map(api.object["spec"])["holderIdentity"] = current.Previous.Plan.SHA256() + ":old-original"
			}
			before := deploymentplan.Digest(e.Journal)
			err := releaseManagedApplicationInterruptedLeaseWithBackend(context.Background(), environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, api)
			if (err == nil) != (mode == "original") {
				t.Fatal("latest schema/lease authority differs", mode, err)
			}
			if api.deletes != 0 || api.writes != 0 || api.dry != 0 || before != deploymentplan.Digest(e.Journal) {
				t.Fatal("latest absence attempted release or reused old owned lease")
			}
		})
	}
}

func TestManagedApplicationThirdInterruptedLeaseRequiresExactTrustACKSchemaAndAbsence(t *testing.T) {
	for _, mode := range []string{"original", "old-app-schema", "appeared", "changed-deepest-proof"} {
		t.Run(mode, func(t *testing.T) {
			e, b, r, lease := managedApplicationLeaseDepthFixture(t, 3)
			r.ManagedApplicationPartial.Interrupted.LeaseSHA = deploymentplan.Digest(deploymentplan.Object(nil))
			if mode == "old-app-schema" {
				admitManagedApplicationLeaseFixtureWithSchema(t, e, r, "rtk-cloud.create-managed-application-app-role-recovery/v1")
			} else {
				admitManagedApplicationLeaseFixture(t, e, r)
			}
			if mode == "changed-deepest-proof" {
				r.ManagedApplicationPartial.Interrupted.Previous.Previous.SourceSHA = strings.Repeat("f", 64)
			}
			api := &executionLeaseAPIFixture{}
			if mode == "appeared" {
				api.object = lease
			}
			before := deploymentplan.Digest(e.Journal)
			err := releaseManagedApplicationInterruptedLeaseWithBackend(context.Background(), environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, api)
			if (err == nil) != (mode == "original") || api.deletes != 0 || before != deploymentplan.Digest(e.Journal) {
				t.Fatal("third authority reused old schema/proof or wrote absence cleanup", mode, err)
			}
		})
	}
}
