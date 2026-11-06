package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const stagingWorkloadAllocationPrefix = "workload-postgres-allocation-"

type stagingWorkloadAllocationReceipt struct {
	Plan     string            `json:"plan_sha256"`
	Owners   map[string]string `json:"owners"`
	Forward  string            `json:"actual_qualified_forward_stage"`
	Original string            `json:"original_failed_plan_sha256"`
}

func freshCreateWorkloadStage(store secretStore, e *environmentCreateExecution) (string, error) {
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	var receipt stagingWorkloadAllocationReceipt
	if entry == nil || entry.State != "completed" && entry.State != "allocated" || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Forward == "" {
		return "workload", nil
	}
	original, err := readEnvironmentCreateStageInput(store, e, "workload")
	bad := environmentCreateBootstrapBlocker("accepted workload allocation does not bind its exact unwritten predecessor")
	if err != nil || original == nil || e.Journal.Environment != "staging" || original.SHA256() != receipt.Original {
		return "", bad
	}
	if _, written, err := original.ReadExecution(store.Root); err != nil || written {
		return "", bad
	}
	stage := stagingWorkloadAllocationPrefix + receipt.Original[:12]
	if strings.HasPrefix(receipt.Forward, "kubernetes/"+stagingWorkloadRuntimePrefix) {
		prior, err := readEnvironmentCreateStageInput(store, e, stage)
		if err != nil || prior == nil || verifyStagingWorkloadSupersession(store, e, stage, prior) != nil {
			return "", bad
		}
		stage = stagingWorkloadRuntimePrefix + prior.SHA256()[:12]
	}
	if receipt.Forward != "kubernetes/"+stage {
		return "", bad
	}
	plan, progress, err := acceptedCreateStage(store, e, receipt.Forward)
	if err != nil || plan.SHA256() != receipt.Plan || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(progress.Owners) {
		return "", bad
	}
	return stage, nil
}

// An interrupted forward stage keeps its exact persisted child. The normal
// executor resumes that child under the same original parent instead of
// rendering fresh intent from partially applied owners.
func readStagingWorkloadAllocationCheckpoint(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var empty deploymentplan.Journal
	original, err := readEnvironmentCreateStageInput(store, e, "workload")
	if err != nil || original == nil {
		return nil, empty, err
	}
	stage := stagingWorkloadAllocationPrefix + original.SHA256()[:12]
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage) == nil {
		return nil, empty, nil
	}
	p, err := readEnvironmentCreateStageInput(store, e, stage)
	bad := createProviderRecoveryBlock("staging workload continuation lost its exact original child")
	if err != nil || p == nil || p.ConfigurationSHA256 != original.ConfigurationSHA256 || p.ReleaseSHA256 != original.ReleaseSHA256 || len(p.Resources) != len(original.Resources) {
		return nil, empty, bad
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
	if strings.Contains(string(entry.Receipt), "actual_qualified_forward_stage") {
		if err := verifyStagingWorkloadSupersession(store, e, stage, p); err != nil {
			return nil, empty, err
		}
		j, _, err := p.ReadExecution(store.Root)
		return p, j, err
	}
	journal, _, err := readEnvironmentCreateStageProgress(store, e, stage, p)
	return p, journal, err
}

// The historical scoped plan rejected its initial Billing Job locally
// before sending it. Revalidate the exact empty database and absent Job
// under the original parent, then archive the untouched interrupted journal.
func recoverStagingInitialBillingWrite(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, backend environmentKubernetesBackend) error {
	original, err := readEnvironmentCreateStageInput(store, e, "workload")
	if err != nil || original == nil {
		return err
	}
	stage := stagingWorkloadAllocationPrefix + original.SHA256()[:12]
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage) == nil {
		return nil
	}
	p, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || p == nil {
		return createProviderRecoveryBlock("staging Billing recovery lacks its exact original workload child")
	}
	path := filepath.Join(store.Root, "deployment", "rollback", p.SHA256(), "journal.json")
	var j deploymentplan.Journal
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	if readExecutionJSON(path, &j) != nil || j.PlanSHA256 != p.SHA256() {
		return createProviderRecoveryBlock("staging Billing recovery lost its private execution journal")
	}
	if j.InFlight == "" {
		return nil
	}
	image := parent.Release.Images["LKE_BILLING_IMAGE"]
	want := environmentBillingSchemaJob(cfg.Environment, parent.Stack, image, cfg.Values["LKE_IMAGE_PULL_SECRET_NAME"])
	id := deploymentplan.ID(want.Object)
	if j.InFlight != id || p.Operation != "scoped" || p.ConfigurationSHA256 != parent.ConfigurationSHA256 || p.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return createProviderRecoveryBlock("staging original workload has a different unresolved native write")
	}
	if e.Check(ctx) != nil {
		return createProviderRecoveryBlock("staging Billing recovery lacks the held original parent")
	}
	guard := environmentCreateKubernetesBackend{environmentKubernetesBackend: backend, execution: e, cfg: cfg, store: store, stagePlanSHA256: p.SHA256(), initialBillingCreate: true}
	err = p.RecoverUnwrittenCreate(ctx, backend, store.Root, id, func(ctx context.Context, resource deploymentplan.Resource) error {
		return guard.beforeSchemaOwnerWrite(ctx, resource.Desired)
	})
	if err == nil {
		fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.billing original Job absent and database empty")
	}
	return err
}

func applyStagingWorkloadAllocationForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, backend environmentKubernetesBackend) (bool, error) {
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	if entry == nil {
		return false, nil
	}
	if entry.State == "allocated" {
		stage, err := freshCreateWorkloadStage(store, e)
		if err != nil || stage == "workload" {
			return false, err
		}
		return true, e.CompleteEntry(entry.Key, time.Now().UTC())
	}
	if entry.State != "pending" {
		return false, nil
	}
	original, err := readEnvironmentCreateStageInput(store, e, "workload")
	bad := createProviderRecoveryBlock("original staging workload is not the exact unwritten PostgreSQL template admission")
	if err != nil || original == nil || original.ConfigurationSHA256 != parent.ConfigurationSHA256 || original.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return true, bad
	}
	_, written, err := readEnvironmentCreateStageProgress(store, e, "workload", original)
	if err != nil || written {
		return false, err
	}
	id := "StatefulSet/" + parent.Stack + "-platform/postgresql"
	for _, resource := range original.Resources {
		if resource.ID != id {
			continue
		}
		claims := certIssuerObjectList(deploymentplan.Map(resource.Desired["spec"])["volumeClaimTemplates"])
		oldClaims := certIssuerObjectList(deploymentplan.Map(resource.Baseline["spec"])["volumeClaimTemplates"])
		if resource.Action != "update" || len(claims) != 1 || len(oldClaims) != 1 || deploymentplan.Map(deploymentplan.Map(claims[0])["spec"])["storageClassName"] != nil || deploymentplan.String(deploymentplan.Map(deploymentplan.Map(oldClaims[0])["spec"])["storageClassName"]) == "" {
			return false, nil
		}
		current, err := backend.Read(ctx, id)
		if err != nil || deploymentplan.Digest(deploymentplan.Map(current["spec"])["volumeClaimTemplates"]) != deploymentplan.Digest(oldClaims) || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
			return true, bad
		}
		definitions := environmentCreateDefinitions(original)
		bindings := environmentIdentityBindings{Stack: parent.Stack, Owners: map[string]string{id: resource.UID}}
		if err := bindPlatformDataAllocation(definitions, []deploymentplan.Object{current}, bindings, "postgresql", "PostgreSQL"); err != nil {
			return true, err
		}
		stage := stagingWorkloadAllocationPrefix + original.SHA256()[:12]
		if prior, progress, err := readStagingWorkloadAllocationCheckpoint(store, e); err != nil {
			return true, err
		} else if prior != nil && stagingWorkloadRuntimeInterrupted(prior, progress) {
			return true, applyStagingWorkloadRuntimeForward(ctx, cfg, store, parent, e, stage, original, prior, progress, backend)
		}
		if err := applyEnvironmentCreateStage(ctx, cfg, store, parent, e, stage, definitions, os.Stdout); err != nil {
			return true, err
		}
		accepted, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
		if err != nil {
			return true, err
		}
		receipt := stagingWorkloadAllocationReceipt{accepted.SHA256(), journal.Owners, "kubernetes/" + stage, original.SHA256()}
		if err := e.RecordReceipt("kubernetes/workload", mustProviderRecoveryJSON(receipt)); err != nil {
			return true, err
		}
		if err := e.CompleteEntry("kubernetes/workload", time.Now().UTC()); err != nil {
			return true, err
		}
		fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.postgresql original-allocation-retained")
		return true, nil
	}
	return false, nil
}

// Recognize the short interval where the accepted Service update targets
// the new port name but the original workload has not yet been written.
func stagingControllerHandoffRecorded(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) (bool, error) {
	p, j, err := readStagingWorkloadAllocationCheckpoint(store, runtime.Execution)
	if err != nil || p == nil || j.Accepted {
		return false, err
	}
	ns := p.Stack + "-video-cloud"
	serviceID, deploymentID := "Service/"+ns+"/pki-controller", "Deployment/"+ns+"/pki-controller"
	if j.Owners[serviceID] == "" || j.Owners[deploymentID] != "" {
		return false, nil
	}
	var service, controller deploymentplan.Resource
	for _, r := range p.Resources {
		switch r.ID {
		case serviceID:
			service = r
		case deploymentID:
			controller = r
		}
	}
	oldPort := managedNamedField(deploymentplan.Map(service.Baseline["spec"])["ports"], "https")
	newPort := managedNamedField(deploymentplan.Map(service.Desired["spec"])["ports"], "https")
	if cfg.Environment != "staging" || oldPort["targetPort"] != float64(18446) || newPort["targetPort"] != "https" ||
		managedNamedField(mqttFoundationPrimary(controller.Baseline)["ports"], "tls")["containerPort"] != float64(18446) ||
		managedNamedField(mqttFoundationPrimary(controller.Desired)["ports"], "https")["containerPort"] != float64(18446) {
		return false, nil
	}
	actualService, err := runtime.Backend.Read(ctx, serviceID)
	actualController, readErr := runtime.Backend.Read(ctx, deploymentID)
	if err != nil || readErr != nil || service.UID != j.Owners[serviceID] || deploymentplan.Map(actualService["metadata"])["uid"] != service.UID ||
		deploymentplan.Map(actualController["metadata"])["uid"] != controller.UID || !deploymentplan.Matches(service.Desired, actualService) ||
		!deploymentplan.Matches(controller.Baseline, actualController) {
		return false, createProviderRecoveryBlock("staging Controller handoff differs from its exact recorded owners")
	}
	return true, runtime.Execution.Check(ctx)
}

func readStagingResumeIssuer(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, id string) (map[string]any, error) {
	if !runtime.StagingControllerHandoff {
		return runtime.PKIClient.ReadIssuer(ctx, id)
	}
	if !environmentBootstrapOwnerUUID.MatchString(id) {
		return nil, createProviderRecoveryBlock("staging Controller handoff requires an exact issuer UUID")
	}
	query := "SELECT document FROM pki_issuers WHERE id='" + id + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var result map[string]any
	if err != nil || decodeEnvironmentJSON(raw, &result) != nil || result["issuer_id"] != id || result["environment"] != cfg.Environment {
		return nil, createProviderRecoveryBlock("staging Controller handoff cannot verify the original issuer in its native registry")
	}
	return result, nil
}

func verifyStagingProductLeafCRL(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, issuer environmentCreateRegisteredIssuer, certificate string) error {
	if !environmentBootstrapOwnerUUID.MatchString(issuer.ID) || issuer.Environment != cfg.Environment {
		return createProviderRecoveryBlock("staging Controller handoff requires its original Service CRL issuer")
	}
	query := "SELECT json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) FROM pki_crls WHERE issuer_id='" + issuer.ID + "' ORDER BY number DESC LIMIT 1"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var record productCurrentCRL
	if err != nil || decodeEnvironmentJSON(raw, &record) != nil {
		return createProviderRecoveryBlock("staging Controller handoff cannot verify its current native Service CRL")
	}
	crl, err := validateProductCurrentCRL(record, issuer)
	if err != nil {
		return err
	}
	return verifyProductLeafWithCRL(crl, certificate)
}
