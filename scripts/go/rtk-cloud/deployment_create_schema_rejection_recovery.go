package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const initialBillingScopeRejection = "Billing database is empty and has no applied history; this plan does not declare its initial owner migration"

type schemaRejectedCreateBackend struct {
	environmentCreateKubernetesBackend
	observed map[string]deploymentplan.Object
}

func (b schemaRejectedCreateBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	if current := b.observed[id]; current != nil {
		return deploymentplan.Clone(current), ctx.Err()
	}
	return b.environmentCreateKubernetesBackend.Read(ctx, id)
}

// No cloud write occurs: only settle the old guard's known pre-API rejection.
// The ordinary create executor still owns all subsequent admission and writes.
func reconcileInitialBillingScopeRejection(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, sourceSHA, sourceLog string) error {
	bad := func() error {
		return environmentSchemaBlocker("billing", "original scoped initial-schema rejection and unchanged empty database must be independently verified")
	}
	if e == nil || e.Store != store || e.Check(ctx) != nil || filepath.Dir(sourceLog) != filepath.Join(store.Root, "deployment", "logs") {
		return bad()
	}
	p, err := readEnvironmentCreateStageInput(store, e, "workload")
	if err != nil || p == nil || p.Operation != "scoped" {
		return bad()
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	intent, err := environmentCreateKubernetesStageIntent(p)
	intentSHA, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || entry == nil || entry.State != "pending" || entry.Phase != "workload" || entry.IntentSHA256 != intentSHA || len(entry.Receipt) != 0 {
		return bad()
	}
	path := filepath.Join(store.Root, "deployment", "rollback", p.SHA256(), "journal.json")
	raw, err := readBoundedEnvironmentFile(path, true)
	var j deploymentplan.Journal
	if err != nil || environmentPreflightBytesSHA256(raw) != sourceSHA || json.Unmarshal(raw, &j) != nil || len(j.Completed) >= len(p.Resources) {
		return bad()
	}
	target := p.Resources[len(j.Completed)]
	if target.Phase != "migration" || deploymentplan.Map(deploymentplan.Map(target.Desired["metadata"])["annotations"])[environmentBillingSchemaRevision] == nil {
		return bad()
	}
	log, err := readBoundedEnvironmentFile(sourceLog, true)
	if err != nil || validateInitialBillingScopeRejectionLog(log, cfg.Environment, target.ID) != nil {
		return bad()
	}
	logSHA := environmentPreflightBytesSHA256(log)
	live, err := native.Inventory(ctx, p.Stack)
	if err != nil {
		return err
	}
	observed := map[string]deploymentplan.Object{}
	for _, object := range live {
		observed[deploymentplan.ID(object)] = object
	}
	backend := schemaRejectedCreateBackend{environmentCreateKubernetesBackend{environmentKubernetesBackend: native, execution: e, cfg: cfg, store: store, stagePlanSHA256: p.SHA256()}, observed}
	evidence := deploymentplan.Object{"schema": "rtk-cloud.initial-schema-pre-api-rejection/v1", "parent_plan_sha256": e.Journal.PlanSHA256, "child_plan_sha256": p.SHA256(), "source_log_sha256": logSHA, "source_journal_sha256": sourceSHA, "resource_id": target.ID}
	verify := func(ctx context.Context) error {
		fresh, err := readBoundedEnvironmentFile(sourceLog, true)
		if err != nil || environmentPreflightBytesSHA256(fresh) != logSHA {
			return bad()
		}
		// Direct native reads are required for absence. Inventory omission is
		// never proof that an earlier request or its Pod did not execute.
		current, err := native.Read(ctx, target.ID)
		if err != nil || current != nil {
			return bad()
		}
		currentLive, err := native.Inventory(ctx, p.Stack)
		if err != nil {
			return err
		}
		if err := verifyInitialBillingMigrationPodsAbsent(currentLive, target.Desired); err != nil {
			return err
		}
		if err := backend.beforeSchemaOwnerWrite(ctx, target.Desired); err != nil {
			return err
		}
		stateStore := store
		stateStore.checkRuntime = newFrozenDeploymentCheckRuntime(ctx, store.Environment, native)
		state, err := readEnvironmentSchemaState(stateStore, "billing")
		if err != nil || state.History || state.Objects != 0 {
			return bad()
		}
		evidence["checked_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		evidence["database_state"] = state
		evidence["job_absent"], evidence["migration_pods_absent"] = true, true
		return e.Check(ctx)
	}
	return p.ReconcileRejectedCreate(ctx, backend, store.Root, sourceSHA, target.ID, evidence, verify)
}

func validateInitialBillingScopeRejectionLog(raw []byte, environment, id string) error {
	text := string(raw)
	exactApply := false
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		exactApply = exactApply || slices.Contains(fields, "action=create") && slices.Contains(fields, "step=apply") && slices.Contains(fields, "resource="+id)
	}
	if strings.Count(text, "  cause: "+initialBillingScopeRejection+"\n") != 1 || !strings.Contains(text, "[BLOCKED] deployment.compatibility.schema\n") || !exactApply || !strings.Contains(text, "NO-GO environment="+environment+" operation=create scope=original-generation-recovery completed=false") || strings.Contains(text, "[PASS] deployment.create.kubernetes.workload") {
		return environmentSchemaBlocker("billing", "failure log does not prove the exact original pre-API scope rejection")
	}
	return nil
}

func verifyInitialBillingMigrationPodsAbsent(objects []deploymentplan.Object, job deploymentplan.Object) error {
	m := deploymentplan.Map(job["metadata"])
	for _, object := range objects {
		meta := deploymentplan.Map(object["metadata"])
		if object["kind"] != "Pod" || meta["namespace"] != m["namespace"] {
			continue
		}
		labels := deploymentplan.Map(meta["labels"])
		if labels["job-name"] == m["name"] || labels["batch.kubernetes.io/job-name"] == m["name"] {
			return environmentSchemaBlocker("billing", "original migration Pod exists; its write outcome requires separate investigation")
		}
		for _, item := range certIssuerObjectList(meta["ownerReferences"]) {
			owner := deploymentplan.Map(item)
			if owner["kind"] == "Job" && owner["name"] == m["name"] {
				return environmentSchemaBlocker("billing", "original migration Pod owner exists; its write outcome requires separate investigation")
			}
		}
	}
	return nil
}

func runCreateSchemaRejectionRecovery(args []string) error {
	fs := flag.NewFlagSet("recover-create-schema-rejection", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	parent := fs.String("parent-plan-sha256", "", "original create generation")
	journalSHA := fs.String("journal-sha256", "", "exact original failed child journal")
	sourceLog := fs.String("source-log", "", "canonical original failed create log")
	confirm := fs.String("confirm", "", "selected stack")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*environment) || !environmentPreflightDigest.MatchString(*parent) || !environmentPreflightDigest.MatchString(*journalSHA) || *confirm != "video-cloud-"+*environment || !filepath.IsAbs(*sourceLog) {
		return environmentCreateBootstrapBlocker("select exact original create, failed journal, canonical log and stack")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *environment, "")
	if err != nil {
		return err
	}
	store, _, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	e, original, err := loadCreateRecoveryExecutionOwner(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return environmentCreateBootstrapBlocker("original Kubernetes identity changed")
	}
	native, err := newEnvironmentKubernetesBackend(string(kube), *environment)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := runEnvironmentProgress(ctx, os.Stdout, "deployment.create.schema-rejection.reconcile", func(ctx context.Context) error {
		return reconcileInitialBillingScopeRejection(ctx, cfg, store, e, native, *journalSHA, *sourceLog)
	}); err != nil {
		return err
	}
	fmt.Printf("PASS environment=%s scope=original-initial-schema-rejection-reconciled cloud-writes=false deployment-completed=false\n", *environment)
	return nil
}
