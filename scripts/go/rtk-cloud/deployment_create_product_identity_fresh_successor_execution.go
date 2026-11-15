package main

import (
	"context"
	"os"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func freshProductSuccessorRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, programme *environmentFreshProductProgram, service environmentCreateServiceCeremony) (environmentCreateBootstrapRuntime, error) {
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: native, ServiceCeremony: &service}
	if previous, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime); ok && previous.Execution == e && previous.Backend.Store == store {
		runtime = previous
		runtime.Backend = native
		runtime.ServiceCeremony = &service
	}
	if cfg.Values["PKI_STATE_BACKEND"] == "openbao" {
		var err error
		runtime.Custody, err = createEnvironmentCustodyBinding(cfg, programme.Lifecycle.Bootstrap, e.Journal.PlanSHA256, "")
		if err != nil || runtime.Custody == nil {
			return runtime, freshProductSuccessorBlock()
		}
		runtime.Custody.ServiceRootSHA256 = service.Root.RootFingerprint
		ca, _, err := initialTransportAuthorityPath(store, e.Journal.PlanSHA256, "openbao")
		if err != nil {
			return runtime, err
		}
		runtime.OpenBao.CACertPath = ca
	}
	return runtime, e.Check(ctx)
}

// The writer records new linkage only after real native completion. Original
// programme, output receipts and native child journals are never reconstructed.
func ensureFreshProductSessionSuccessor(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, q secondSessionAdmission, review string) error {
	if createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil
	}
	programme, err := readFreshProductProgram(store, e)
	if err != nil {
		return err
	}
	l := &programme.Lifecycle
	if l.FreshResume == nil || l.FreshResume.SessionReviewSHA256 == review {
		return nil
	}
	installedPath, err := store.safePath(productIdentitySessionPath(l))
	if err != nil {
		return err
	}
	if _, err = os.Lstat(installedPath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	expected, effective, err := expectedFreshProductSessionSuccessor(store, e, l, q, review)
	if err != nil {
		return err
	}
	original, err := readProductInstalled(store, l)
	if err != nil {
		return err
	}
	runtime, err := freshProductSuccessorRuntime(ctx, cfg, store, e, native, programme, effective.Ceremony)
	if err != nil {
		return err
	}
	if err = verifyFreshProductOriginalOutputs(ctx, cfg, runtime, l, original); err != nil {
		return err
	}
	if err = e.Check(ctx); err != nil {
		return err
	}
	_, err = writeProductArtifact(store, freshProductSuccessorPath(e.Journal.PlanSHA256, review), expected)
	return err
}

// Qualification of another seed cannot substitute any already-issued product.
func verifyFreshProductSessionSuccessorSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend) error {
	if createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil
	}
	programme, err := readFreshProductProgram(store, e)
	if err != nil {
		return err
	}
	l := &programme.Lifecycle
	path, err := store.safePath(productIdentitySessionPath(l))
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	original, err := readProductInstalled(store, l)
	if err != nil {
		return err
	}
	runtime, err := freshProductSuccessorRuntime(ctx, cfg, store, e, native, programme, original.Ceremony)
	if err != nil {
		return err
	}
	return verifyFreshProductOriginalOutputs(ctx, cfg, runtime, l, original)
}

func freshProductSuccessorControlDefinitions(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) (string, []deploymentplan.Definition, *environmentFreshProductProgram, error) {
	e := runtime.Execution
	programme, err := readFreshProductProgram(store, e)
	if err != nil {
		return "", nil, nil, err
	}
	binding, err := readFreshProductSessionSuccessor(store, e, &programme.Lifecycle)
	if err != nil || binding == nil {
		return "", nil, programme, err
	}
	stage := freshProductSuccessorControlStage(binding.CurrentReview)
	effective, err := readEffectiveFreshProductInstalled(store, e, &programme.Lifecycle)
	if err != nil {
		return "", nil, nil, err
	}
	runtime, err = freshProductSuccessorRuntime(ctx, cfg, store, e, runtime.Backend, programme, effective.Ceremony)
	if err != nil {
		return "", nil, nil, err
	}
	const suffix = "/pki-controller"
	controllerID := "Deployment/" + e.Journal.Stack + "-video-cloud" + suffix
	q, err := loadSecondSessionAdmission(store, e, binding.CurrentReview)
	if err != nil {
		return "", nil, nil, err
	}
	var controller deploymentplan.Object
	for _, batch := range q.Batches {
		if batch.Plan != nil {
			for _, resource := range batch.Plan.Resources {
				if resource.ID == controllerID {
					if controller != nil {
						return "", nil, nil, freshProductSuccessorBlock()
					}
					accepted, j, err := acceptedCreateStage(store, e, "kubernetes/"+batch.Stage)
					if err != nil || accepted.SHA256() != batch.Plan.SHA256() || !environmentBootstrapOwnerUUID.MatchString(j.Owners[controllerID]) {
						return "", nil, nil, freshProductSuccessorBlock()
					}
					controller = deploymentplan.Clone(resource.Desired)
					deploymentplan.Map(controller["metadata"])["uid"] = j.Owners[controllerID]
				}
			}
		}
	}
	if controller == nil {
		return "", nil, nil, freshProductSuccessorBlock()
	}
	common, err := applyEnvironmentBootstrapCleanup(map[string]string{"CLOUD_ENV_NAME": cfg.Environment, "CLOUD_STACK_NAME": e.Journal.Stack}, controller, effective.Ceremony.CleanupBinding)
	if err != nil {
		return "", nil, nil, err
	}
	var dependencies []string
	for _, d := range common {
		dependencies = append(dependencies, deploymentplan.ID(d.Object))
	}
	common = append(common, deploymentplan.Definition{Object: controller, Phase: "workload", Dependencies: dependencies})
	original, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return "", nil, nil, err
	}
	projected, err := projectEnvironmentCreateCustodyStage(withEnvironmentCreateBootstrapRuntime(ctx, runtime), cfg, store, programme.SelectedPlan, e, common, original)
	return stage, projected, programme, err
}

func qualifyFreshProductSuccessorControls(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "product-identities/fresh") == nil {
		return nil
	}
	stage, defs, programme, err := freshProductSuccessorControlDefinitions(ctx, cfg, store, runtime)
	if err != nil || stage == "" {
		return err
	}
	if entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "kubernetes/"+stage); entry != nil && entry.State == "completed" {
		_, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, runtime.Execution, runtime.Backend, entry.Key)
		return err
	}
	live, err := runtime.Backend.Inventory(ctx, runtime.Execution.Journal.Stack)
	if err != nil {
		return err
	}
	// Existing authority owners must remain those authenticated by the complete
	// accepted core and its recorded same-UID successors; only the new CM is new.
	owned, err := currentEnvironmentCreateAccountRegistrationCore(ctx, cfg, runtime, nil)
	if err != nil {
		return err
	}
	owners := map[string]string{}
	for _, d := range owned {
		owners[deploymentplan.ID(d.Object)] = deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"])
	}
	byID := map[string]deploymentplan.Object{}
	for _, o := range live {
		byID[deploymentplan.ID(o)] = o
	}
	saved, err := readEnvironmentCreateStageInput(store, runtime.Execution, stage)
	if err != nil {
		return err
	}
	for _, d := range defs {
		id := deploymentplan.ID(d.Object)
		if expected := owners[id]; expected != "" {
			if byID[id] == nil || deploymentplan.Map(byID[id]["metadata"])["uid"] != expected {
				return freshProductSuccessorBlock()
			}
		}
		if d.Object["kind"] == "ConfigMap" && saved == nil && byID[id] != nil {
			return freshProductSuccessorBlock()
		}
	}
	p := saved
	if p == nil {
		p, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: runtime.Execution.Journal.Stack, Operation: "scoped", ConfigurationSHA256: programme.SelectedPlan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(programme.SelectedPlan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, live)
		if err != nil {
			return err
		}
	}
	if p.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, defs) {
		return freshProductSuccessorBlock()
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: runtime.Execution, cfg: cfg, store: store}
	return p.AdmitResumable(ctx, backend, store.Root)
}
func applyFreshProductSuccessorControls(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	if createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "product-identities/fresh") == nil {
		return nil
	}
	stage, defs, _, err := freshProductSuccessorControlDefinitions(ctx, cfg, store, runtime)
	if err != nil || stage == "" {
		return err
	}
	if apply == nil {
		return freshProductSuccessorBlock()
	}
	return apply(ctx, stage, defs)
}
func verifyFreshProductSuccessorControls(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle) error {
	binding, err := readFreshProductSessionSuccessor(store, runtime.Execution, l)
	if err != nil || binding == nil {
		return err
	}
	_, err = verifyAcceptedStageAfterRecordedHistory(ctx, store, runtime.Execution, runtime.Backend, "kubernetes/"+freshProductSuccessorControlStage(binding.CurrentReview))
	return err
}

// Birth definitions remain the source of IDs and UIDs. Current retain/retire
// bodies follow only authenticated same-owner native successor history.
func freshProductPreparationDefinitions(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, backend deploymentplan.Backend, retire bool) ([]deploymentplan.Definition, error) {
	original, err := readProductInstalled(store, l)
	if err != nil {
		return nil, err
	}
	defs, err := productPreparationDefinitions(cfg, store, e, l, original, retire, true)
	if err != nil {
		return nil, err
	}
	binding, err := readFreshProductSessionSuccessor(store, e, l)
	if err != nil || binding == nil {
		return defs, err
	}
	var frozen *deploymentplan.Plan
	if retire {
		frozen, err = readEnvironmentCreateStageInput(store, e, "fresh-product-producer-retire")
		if err != nil {
			return nil, err
		}
		if frozen != nil {
			if _, _, err := readEnvironmentCreateStageProgress(store, e, "fresh-product-producer-retire", frozen); err != nil {
				return nil, err
			}
			if len(frozen.Resources) != len(defs) {
				return nil, freshProductSuccessorBlock()
			}
		}
	}
	birthStages := map[string]string{}
	stages := []string{l.FreshResume.CustodyStage}
	for _, output := range l.Outputs {
		stages = append(stages, "product-identities-"+productIdentityScope(l)[:12]+"-enroll-"+strings.TrimPrefix(output.Subject, "service:"))
	}
	for _, stage := range stages {
		p, j, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
		if err != nil {
			return nil, err
		}
		for _, r := range p.Resources {
			if r.Desired["kind"] == "Deployment" {
				if birthStages[r.ID] != "" || !environmentBootstrapOwnerUUID.MatchString(j.Owners[r.ID]) {
					return nil, freshProductSuccessorBlock()
				}
				birthStages[r.ID] = "kubernetes/" + stage
			}
		}
	}
	var history map[string]completedRecordedSuccessorStage
	for i := range defs {
		id := deploymentplan.ID(defs[i].Object)
		uid := deploymentplan.String(deploymentplan.Map(defs[i].Object["metadata"])["uid"])
		if frozen != nil {
			found := false
			for _, r := range frozen.Resources {
				if r.ID == id {
					if found || r.Action != "retire" || r.UID != uid {
						return nil, freshProductSuccessorBlock()
					}
					defs[i].Object = deploymentplan.Clone(r.Desired)
					deploymentplan.Map(defs[i].Object["metadata"])["uid"] = uid
					found = true
				}
			}
			if !found {
				return nil, freshProductSuccessorBlock()
			}
			continue
		}
		stage := birthStages[id]
		if stage == "" {
			return nil, freshProductSuccessorBlock()
		}
		if history == nil {
			history, err = authenticatedRecordedPendingWrites(ctx, store, e, backend)
			if err != nil {
				return nil, err
			}
		}
		latest, err := completedRecordedOwnerSuccessorsAfterHistory(ctx, store, e, backend, stage, []activeTransportRenewalResource{sessionRecordedOwnerBase(id, uid, defs[i].Object)}, history)
		if err != nil || len(latest) != 1 {
			return nil, freshProductSuccessorBlock()
		}
		defs[i].Object = deploymentplan.Clone(latest[0].Desired)
		deploymentplan.Map(defs[i].Object["metadata"])["uid"] = uid
	}
	return defs, e.Check(ctx)
}

func verifyFreshProductOriginalOutputs(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return verifyProductIdentityOutputs(ctx, cfg, runtime, l, installed)
	}
	if installed == nil || l == nil || len(installed.Outputs) != len(l.Outputs) {
		return freshProductSuccessorBlock()
	}
	for i, output := range l.Outputs {
		receipt := installed.Outputs[i]
		actual, err := runtime.Backend.Read(ctx, output.ID)
		if receipt.ResourceID != output.ID || receipt.Subject != output.Subject || err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != receipt.SecretUID || actual["immutable"] != true || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil {
			return freshProductSuccessorBlock()
		}
		public, err := inspectEnvironmentProductCustody(ctx, cfg, runtime, l, output, receipt)
		if err != nil {
			return err
		}
		if public.Pending || public.Fingerprint != receipt.CertificateSHA256 {
			return freshProductSuccessorBlock()
		}
	}
	return runtime.Execution.Check(ctx)
}

func freshProductFinalizedCredentialOwners(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony) (map[string]string, error) {
	retired := map[string]string{}
	for ns, uid := range current.CredentialUIDs {
		retired["Secret/"+ns+"/"+current.BootstrapCredentialSecret] = uid
	}
	if runtime.ServiceSessionRecovery == nil || !runtime.ServiceSessionRecovery.SecondSession {
		return retired, nil
	}
	ancestors, _, err := secondSessionBootstrapCleanupAncestors(ctx, cfg, store, runtime, current)
	if err != nil {
		return nil, err
	}
	for _, observed := range ancestors {
		old := current
		old.BootstrapSessionID = observed.Cleanup.SessionID
		old.BootstrapCredentialSecret = "service-bootstrap-" + old.BootstrapSessionID
		old.CleanupBinding = observed.Cleanup
		old.CredentialUIDs = observed.UIDs
		for ns, uid := range observed.UIDs {
			complete, err := completedRenewedBootstrapCredentialRetirement(ctx, cfg, runtime, old, ns)
			if err != nil {
				return nil, err
			}
			if !complete {
				return nil, freshProductSuccessorBlock()
			}
			retired["Secret/"+ns+"/"+old.BootstrapCredentialSecret] = uid
		}
	}
	return retired, runtime.Execution.Check(ctx)
}
