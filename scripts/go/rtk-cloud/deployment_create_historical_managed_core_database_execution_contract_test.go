package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This producer establishes the original database credential failure and exact
// retained repair before any Service source captures its immutable prefix.
func historicalManagedCoreDatabaseHook(t *testing.T, hooks coreExecutionContractHooks) coreExecutionContractHooks {
	t.Helper()
	before := hooks.BeforeSource
	hooks.BeforeSource = func(f *acceptedCoreExecutionContract, runtime *environmentCreateBootstrapRuntime) {
		if before != nil {
			before(f, runtime)
		}
		ctx := context.Background()
		var defs []deploymentplan.Definition
		for _, role := range []string{"controller", "issuer", "verifier"} {
			password := "isolated-original-" + role + ":p@ss"
			if err := f.store.write(filepath.Join("deployment/create/material", f.e.Journal.PlanSHA256, "database", role+".password"), []byte(password), false); err != nil {
				t.Fatal(err)
			}
			dsn := "postgres://" + url.UserPassword("rtk_pki_"+role+"_login", password).String() + "@postgresql." + f.p.Plan.Stack + "-platform.svc:5432/video_cloud?sslmode=disable"
			defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-" + role + "-database", "namespace": f.p.Plan.Stack + "-video-cloud"}, "data": deploymentplan.Object{"url": base64.StdEncoding.EncodeToString([]byte(dsn))}}})
		}
		input := deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("database-runtime-secrets", defs), CreateParentSHA256: f.e.Journal.PlanSHA256}
		original, err := deploymentplan.Compile(input, defs, nil)
		if err != nil {
			t.Fatal(err)
		}
		intent, err := environmentCreateKubernetesStageIntent(original)
		if err != nil || f.e.BeginEntry("kubernetes/database-runtime-secrets", "bootstrap", intent, time.Now().UTC()) != nil {
			t.Fatal("original database intent could not begin", err)
		}
		backend := freshStageBackend{f.native}
		qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.AdmitResumable(ctx, backend, f.store.Root)
		}
		fence := func(ctx context.Context, p *deploymentplan.Plan) error {
			return backend.CheckParentExecution(ctx, p, f.store.Root)
		}
		interruption := func(context.Context, *deploymentplan.Plan) error {
			return &deploymentplan.Blocker{ID: "deployment.post-deploy.workloads", Cause: "isolated original acceptance interruption after all three credential writes"}
		}
		if err := original.Execute(ctx, backend, original.SHA256(), f.store.Root, qualify, fence, interruption); err == nil {
			t.Fatal("original database writes did not stop at the acceptance boundary")
		}
		p := f.p
		p.Materials = map[string]string{}
		checkpoint, journal, passwords, err := loadCreateDatabaseSecretsCheckpoint(f.store, f.e, &p)
		if err != nil || checkpoint.SHA256() != original.SHA256() || len(passwords) != 3 || f.native.held {
			t.Fatal("actual original database outcome did not qualify", err)
		}
		review, _ := f.admitRecovery(t, "rtk-cloud.create-database-secrets-recovery/v1", p.Materials)
		var retained []deploymentplan.Definition
		for _, resource := range checkpoint.Resources {
			retained = append(retained, deploymentplan.Definition{Phase: resource.Phase, Object: deploymentplan.Clone(resource.Desired), Retain: true})
		}
		var live []deploymentplan.Object
		for _, object := range f.native.objects {
			live = append(live, object)
		}
		stage := "database-secrets-recovery-" + review
		input.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, retained)
		plan, err := deploymentplan.Compile(input, retained, live)
		if err != nil {
			t.Fatal(err)
		}
		accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }
		if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, plan, backend, qualify, fence, accept, f.e.Check, io.Discard); err != nil {
			t.Fatal("original database retained repair failed", err)
		}
		accepted, proof, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage)
		if err != nil || len(accepted.Resources) != 3 {
			t.Fatal("original database retained acceptance unavailable", err)
		}
		for _, resource := range accepted.Resources {
			if resource.Action != "retain" || resource.UID != journal.Owners[resource.ID] || proof.Owners[resource.ID] != resource.UID || f.native.writes[resource.ID] != 1 {
				t.Fatal("database repair rewrote or replaced an original credential owner", resource.ID)
			}
		}
		alias := map[string]string{"original_plan_sha256": checkpoint.SHA256(), "original_failed_journal_sha256": deploymentplan.Digest(journal), "actual_retained_verification_stage": "kubernetes/" + stage}
		if err := f.e.RecordReceipt("kubernetes/database-runtime-secrets", mustProviderRecoveryJSON(alias)); err != nil {
			t.Fatal(err)
		}
		if err := f.e.CompleteEntry("kubernetes/database-runtime-secrets", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	return hooks
}

func TestHistoricalManagedCoreDatabasePrefixRetainsOriginalOwnersBeforeSource(t *testing.T) {
	var captured environmentCreateJournal
	hooks := historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t))
	before := hooks.BeforeSource
	hooks.BeforeSource = func(f *acceptedCoreExecutionContract, runtime *environmentCreateBootstrapRuntime) {
		before(f, runtime)
		captured = f.e.Journal
		captured.Entries = slices.Clone(captured.Entries)
		captured.KubernetesLeases = slices.Clone(captured.KubernetesLeases)
	}
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, hooks, historicalCompleteDomainPolicy(t))
	p := f.p
	p.Materials = map[string]string{}
	if _, err := loadAcceptedServiceCore(t.Context(), f.cfg, f.store, f.e, f.backend, f.operator, &p); err != nil {
		t.Fatal("original database repair changed Service source admission", err)
	}
	var source createFoundationRecoveryArtifact
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create/openbao-recovery", f.review, "qualification.json"), &source); err != nil || source.Plan.ParentJournalSHA != deploymentplan.Digest(captured) {
		t.Fatal("first Service source did not bind original database prefix", err)
	}
	var alias map[string]string
	found := 0
	for _, entry := range captured.Entries {
		if entry.Key == "kubernetes/database-runtime-secrets" {
			found++
			if entry.State != "completed" || entry.CompletedAt.IsZero() || json.Unmarshal(entry.Receipt, &alias) != nil {
				t.Fatal("immutable source omitted original completed database receipt")
			}
		}
	}
	if found != 1 {
		t.Fatal("database prefix was appended after source capture")
	}
	plan, journal, err := acceptedCreateStage(f.store, f.e, alias["actual_retained_verification_stage"])
	if err != nil || len(plan.Resources) != 3 {
		t.Fatal(err)
	}
	primary := f.native.objects["Secret/"+f.p.Plan.Stack+"-video-cloud/pki-verifier-database"]
	for _, namespace := range f.service.BootstrapCredentialNamespaces[1:] {
		copy := f.native.objects["Secret/"+namespace+"/pki-verifier-database"]
		if copy == nil || deploymentplan.Digest(copy["data"]) != deploymentplan.Digest(primary["data"]) {
			t.Fatal("Service bootstrap copied a different original verifier credential", namespace)
		}
	}
	for _, resource := range plan.Resources {
		actual := f.native.objects[resource.ID]
		if resource.Action != "retain" || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			t.Fatal("accepted original database identity changed during Core bootstrap", resource.ID)
		}
	}
}

func TestHistoricalUsedMQTTExecutionContractPreservesOriginalDatabasePrefix(t *testing.T) {
	mqtt, _, _ := newHistoricalUsedMQTTRenewalExecutionContract(t, historicalManagedCoreDatabaseHook(t, historicalOriginalFoundationHooks(t)), "complete")
	f := mqtt.acceptedCoreExecutionContract
	var alias map[string]string
	for _, entry := range f.e.Journal.Entries {
		if entry.Key == "kubernetes/database-runtime-secrets" {
			if err := json.Unmarshal(entry.Receipt, &alias); err != nil {
				t.Fatal(err)
			}
		}
	}
	plan, journal, err := acceptedCreateStage(f.store, f.e, alias["actual_retained_verification_stage"])
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range plan.Resources {
		actual := f.native.objects[resource.ID]
		if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) || f.native.writes[resource.ID] != 1 {
			t.Fatal("whole used renewal replaced or rewrote original database identity", resource.ID)
		}
	}
}
