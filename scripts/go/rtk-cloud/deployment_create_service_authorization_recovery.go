package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createServiceAuthorizationRecovery struct {
	Root                      environmentCreateRegisteredIssuer
	Operation                 environmentCreateIssuerOperation
	CRLFile                   string
	ControllerImage           string
	Enrollment                *environmentCreateServiceCeremony
	CompletedEnrollmentStages map[string]bool
	CoreForward               *stagingServiceCoreForward
	MQTTForward               *stagingMQTTRootForward
	SingleAPIOwner            bool
	ManagedHosts              *stagingManagedHostsForward
}

// Select the exact boundary after original Service Root activation and CRL
// publication, with one unresolved authorization read and no later writes.
func stoppedServiceAuthorization(j environmentCreateJournal, request environmentCreateIssuerRequest) (environmentCreateIssuerOperation, error) {
	var operation environmentCreateIssuerOperation
	bad := func() (environmentCreateIssuerOperation, error) {
		return operation, createProviderRecoveryBlock("original Service intermediate reservation and sole pending authorization do not match")
	}
	if j.Environment != request.Environment || j.Stack != "video-cloud-"+request.Environment || !j.CompletedAt.IsZero() || len(j.Entries) < 3 || request.Domain != "service" || request.Kind != "intermediate" {
		return bad()
	}
	tail := j.Entries[len(j.Entries)-3:]
	rootCRL, reserved, pending := tail[0], tail[1], tail[2]
	intent, err := environmentCreateIssuerIntentSHA(request)
	if err != nil || rootCRL.Key != "pki-initial-root-crl-service" || reserved.Key != "pki-reserve-service-intermediate" || reserved.IntentSHA256 != intent || decodeEnvironmentJSON(reserved.Receipt, &operation) != nil || !environmentBootstrapOwnerUUID.MatchString(operation.ID) || !environmentBootstrapOwnerUUID.MatchString(operation.IssuerID) || operation.Status != "requested" || operation.Digest != deploymentplan.Digest(request) {
		return bad()
	}
	authSHA, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(operation))
	if err != nil || pending.Key != "pki-authorize-"+operation.ID || pending.Phase != "bootstrap" || pending.IntentSHA256 != authSHA || pending.State != "pending" || len(pending.Receipt) != 0 || !pending.CompletedAt.IsZero() || pending.StartedAt.Before(reserved.CompletedAt) {
		return bad()
	}
	seen := map[string]bool{pending.Key: true}
	for _, entry := range j.Entries[:len(j.Entries)-1] {
		if seen[entry.Key] || entry.State != "completed" || entry.CompletedAt.IsZero() {
			return bad()
		}
		seen[entry.Key] = true
	}
	return operation, nil
}

func runCreateServiceAuthorizationRecovery(args []string) error {
	fs := flag.NewFlagSet("recover-create-service-authorization", flag.ContinueOnError)
	env := fs.String("environment", "", "selected staging environment")
	parent := fs.String("parent-plan-sha256", "", "original stopped create parent")
	controllerSource := fs.String("controller-source", "", "formal Video Cloud source revision for the original Controller")
	resumeEnrollment := fs.Bool("resume-enrollment", false, "continue the original active session from its recorded enrollment stages")
	resumeMQTT := fs.Bool("resume-mqtt-root", false, "continue the original ready MQTT Root after its API placement was blocked")
	resumeHosts := fs.Bool("resume-managed-hosts", false, "resume the original staging managed-host admission without replaying issuer or server enrollment")
	singleAPI := fs.Bool("single-api-owner", false, "continue with one staging replica per existing API identity")
	confirm := fs.String("confirm", "", "exact staging stack")
	report := fs.String("report", "", "canonical private report")
	timeout := fs.Duration("timeout", 60*time.Minute, "remaining original execution window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *resumeHosts {
		*resumeMQTT = true
	}
	if *resumeMQTT {
		*resumeEnrollment = true
	}
	if *resumeHosts && !*singleAPI || *singleAPI && !*resumeMQTT || fs.NArg() != 0 || *env != "staging" || *confirm != "video-cloud-staging" || !environmentPreflightDigest.MatchString(*parent) || !deploymentUpgradeSHA.MatchString(*controllerSource) || *timeout <= 0 || *timeout > 90*time.Minute {
		return createProviderRecoveryBlock("explicit staging stack, original parent, formal Controller source and bounded remaining timeout are required")
	}
	if *report != "" {
		if err := validateEnvironmentDeploymentReportPath(*env, *report); err != nil {
			return err
		}
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-service-authorization"}, args...)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	e, err := lockStoppedCreateExecution(ctx, cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()

	plan, _, err := loadEnvironmentCreateAdmittedGeneration(store, *parent, e.Journal.ArtifactSHA256)
	if err != nil {
		return err
	}
	if err := validateEnvironmentCreatePlan(cfg, &plan, operator); err != nil {
		return err
	}
	var provider environmentCreateProviderRecoveryArtifact
	if err := readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/recovery", *parent, "qualification.json"), &provider); err != nil || deploymentplan.Digest(provider) != e.Journal.ArtifactSHA256 || provider.Plan.OriginalJournal != e.Journal.OriginalJournal || provider.Plan.OriginalJournalSHA256 != e.Journal.OriginalJournalSHA256 {
		return createProviderRecoveryBlock("original provider recovery artifact does not bind the stopped execution")
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(raw) != provider.Plan.KubeconfigSHA256 {
		return createProviderRecoveryBlock("original staging kubeconfig changed")
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), cfg.Environment)
	if err != nil {
		return err
	}
	backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.original.verify")
	for _, r := range plan.Provider.Resources {
		receipt := provider.Plan.Receipts[r.Key]
		actual, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, r, receipt, operator)
		if err != nil || !ready || actual.PublicIP != receipt.PublicIP || actual.PrivateIP != receipt.PrivateIP {
			return createProviderRecoveryBlock("original allocated provider identity or address changed")
		}
	}
	if err := validateEnvironmentCreateStorageAllocation(cfg); err != nil {
		return err
	}
	if err := qualifyProviderRecoveryStorage(ctx, cfg, operator); err != nil {
		return err
	}
	if err := verifyEnvironmentRelease(ctx, cfg, plan.Release); err != nil {
		return err
	}
	if *resumeHosts {
		if err := resumeStagingGeneralVolumeCapacity(ctx, cfg, store, &plan, provider.Plan.Receipts, operator, e, backend); err != nil {
			return err
		}
		if err := resumeStagingOriginalOpenBaoRollout(ctx, cfg, store, e, backend); err != nil {
			return err
		}
		finalized := filepath.Join(store.Root, environmentCreateCeremonyDirectory(environmentCreateBootstrapRuntime{ExecutionSHA256: *parent}, "service"), "finalized.json")
		if _, err := os.Lstat(finalized); err == nil {
			return resumeFinalizedStagingApplicationCreate(ctx, cfg, &plan, provider.Plan.Receipts, store, operator, e, backend, *controllerSource, *report)
		} else if !os.IsNotExist(err) {
			return createProviderRecoveryBlock("original staging finalization artifact cannot be inspected")
		}
		if entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize"); entry != nil && entry.State == "pending" {
			return resumeFinalizedStagingApplicationCreate(ctx, cfg, &plan, provider.Plan.Receipts, store, operator, e, backend, *controllerSource, *report)
		}
	}
	if err := qualifyEnvironmentCreateDNS(ctx, cfg, plan.DNS, operator); err != nil {
		return err
	}
	openbaoCAPath := filepath.Join(store.Root, "deployment/create/material", *parent, "openbao-transport/ca.crt")
	var management environmentCreateTransport
	if *resumeHosts {
		management, openbaoCAPath, err = readStagingManagedHostsTransport(ctx, cfg, store, e, backend)
	} else if *singleAPI {
		management, openbaoCAPath, err = prepareStagingMQTTTransport(ctx, cfg, store, &plan, e, backend)
	} else {
		_, management, err = foundationRecoveryMaterialsForExecution(store, *parent, e, createRecoveryTransportMinimumLife(ctx, 0, true))
	}
	if err != nil {
		return err
	}
	if *resumeHosts {
		if err := recoverStagingInitialBillingWrite(ctx, cfg, store, &plan, e, backend); err != nil {
			return err
		}
	}
	if *resumeHosts && stagingManagedHostsAccepted(e.Journal) {
		if err := verifyStagingManagedOpenBao(ctx, cfg, store, e, backend); err != nil {
			return err
		}
	} else if _, _, err := verifyInitializedOpenBaoIdentity(ctx, cfg, store, e, backend); err != nil {
		return err
	}
	var rootDocument map[string]any
	if err := readExecutionJSON(filepath.Join(store.Root, "deployment/create", *parent, "bootstrap/service/root-active.json"), &rootDocument); err != nil {
		return createProviderRecoveryBlock("original active Service Root snapshot cannot be read")
	}
	root, err := environmentCreateRegisteredIssuerFromMap(rootDocument, "")
	if err != nil || root.Environment != cfg.Environment || root.Domain != "service" || root.Kind != "root" || root.Status != "active" {
		return createProviderRecoveryBlock("original active Service Root snapshot is invalid")
	}
	crl, err := originalCreateRootCRLFile(store, e, root, "service")
	if err != nil {
		return err
	}
	policy := findEnvironmentCreateDomainPolicy(plan.Bootstrap, "service")
	request := environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: "service", Kind: "intermediate", ParentID: root.ID, ServerDNSNames: policy.ServerDNSNames, ServiceClientIDs: policy.ServiceClientIDs}
	repairStage := "service-authorization-controller-" + *controllerSource
	var checkpoint environmentCreateJournal
	snapshot := filepath.Join("deployment/create", *parent, "service-authorization-recovery", "source-journal.json")
	if *resumeEnrollment {
		if err := readExecutionJSON(filepath.Join(store.Root, snapshot), &checkpoint); err != nil || !createOpenBaoACKCompletionMembership(checkpoint, e.Journal, e.owner) {
			return createProviderRecoveryBlock("original stopped authorization journal does not bind the enrollment continuation")
		}
	} else {
		checkpoint = e.Journal
		if len(checkpoint.Entries) > 0 && checkpoint.Entries[len(checkpoint.Entries)-1].Key == "kubernetes/"+repairStage {
			checkpoint.Entries = checkpoint.Entries[:len(checkpoint.Entries)-1]
		}
	}
	operation, err := stoppedServiceAuthorization(checkpoint, request)
	if err != nil {
		return err
	}

	listeners := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/pki-initial-listeners")
	if listeners == nil {
		return createProviderRecoveryBlock("original initial listener stage is missing")
	}
	fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.controller.release")
	repairImage, err := qualifyServiceAuthorizationControllerImage(ctx, cfg, plan.Release, *controllerSource)
	if err != nil {
		return err
	}
	listenerPlan, listenerJournal, err := originalCreateChildAtIntent(store, e, *listeners)
	if err != nil || !listenerJournal.Accepted {
		return createProviderRecoveryBlock("original initial listeners have no accepted child")
	}
	runtime := environmentCreateBootstrapRuntime{
		ExecutionSHA256: *parent, Execution: e, Backend: backend, InitialAuthSeparated: true,
		OpenBao: environmentCreateOpenBaoBootstrap{
			RootTokenPath: filepath.Join(store.Root, "openbao/root-token"),
			URL:           "https://openbao." + plan.Stack + "-secrets.svc:8200",
			CACertPath:    openbaoCAPath,
		},
	}
	for _, resource := range listenerPlan.Resources {
		runtime.InitialListeners = append(runtime.InitialListeners, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase})
	}
	runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(cfg, runtime.InitialListeners)
	if err != nil {
		return err
	}
	recovery := &createServiceAuthorizationRecovery{Root: root, Operation: operation, CRLFile: crl, ControllerImage: repairImage.Reference, SingleAPIOwner: *singleAPI}
	apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		if recovery.CompletedEnrollmentStages[stage] {
			return e.Check(ctx)
		}
		if stage == "service-core-enrollment" && recovery.CoreForward != nil {
			return executeStagingServiceCoreForward(ctx, cfg, store, &plan, e, recovery.CoreForward)
		}
		if recovery.ManagedHosts != nil && recovery.ManagedHosts.ServerForward != nil && stage == recovery.ManagedHosts.ServerForward.Stage {
			return executeStagingServerEnrollmentForward(ctx, cfg, store, &plan, e, recovery.ManagedHosts.ServerForward)
		}
		if stage == "mqtt-root-trust" && recovery.MQTTForward != nil {
			return executeStagingMQTTRootForward(ctx, cfg, store, &plan, e, recovery.MQTTForward)
		}
		if stage == "workload" && recovery.ManagedHosts != nil && recovery.ManagedHosts.ApplicationsAccepted {
			handled, err := applyStagingWorkloadAllocationForward(ctx, cfg, store, &plan, e, backend)
			if handled || err != nil {
				return err
			}
		}
		bindCreateServiceAuthorizationController(definitions, plan.Stack, repairImage.Reference)
		if recovery.SingleAPIOwner {
			for _, definition := range definitions {
				bindStagingSingleAPIOwner(definition.Object, plan.Stack)
			}
		}
		return applyEnvironmentCreateStage(ctx, cfg, store, &plan, e, stage, definitions, os.Stdout)
	}
	if !*resumeEnrollment {
		if err := recoverServiceAuthorizationControllerStage(ctx, cfg, store, &plan, e, backend, checkpoint, snapshot, repairStage, repairImage, apply); err != nil {
			return err
		}
	}
	if !*resumeHosts {
		liveRoot, _, err := loadOriginalReadyServiceRoot(ctx, cfg, store, e, backend, operator, &createFoundationRecoveryPlan{Materials: map[string]string{}}, true, *resumeMQTT)
		if err != nil {
			return err
		}
		if deploymentplan.Digest(liveRoot.Document) != deploymentplan.Digest(root.Document) {
			return createProviderRecoveryBlock("original Service Root changed during Controller recovery")
		}
	}
	runtime, stop, err := continueEnvironmentCreateAfterAccount(ctx, cfg, &plan, store, operator, e, apply, runtime, management)
	if err != nil {
		return err
	}
	defer stop()
	if *resumeHosts {
		runtime.StagingControllerHandoff, err = stagingControllerHandoffRecorded(ctx, cfg, store, runtime)
		if err != nil {
			return err
		}
	}
	client := runtime.PKIClient
	if *resumeEnrollment {
		fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.service-enrollment.verify")
		if err := loadServiceAuthorizationEnrollment(ctx, cfg, store, &plan, e, runtime, recovery, repairStage, repairImage, *resumeMQTT, *resumeHosts); err != nil {
			return err
		}
		if *resumeHosts {
			recovery.ManagedHosts, err = qualifyStagingManagedHostsForward(ctx, cfg, store, &plan, e, runtime)
			if err != nil {
				return err
			}
		} else if *resumeMQTT {
			recovery.MQTTForward, err = qualifyStagingMQTTRootForward(ctx, cfg, store, &plan, e, runtime, operator, *singleAPI)
			if err != nil {
				return err
			}
		}
		fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.service-enrollment original-session-and-stages-verified")
	} else if err := reconcileOriginalServiceAuthorization(ctx, e, client, root, operation); err != nil {
		return err
	}
	runtime.ServiceAuthorizationRecovery = recovery
	err = executeEnvironmentCreateWithRuntime(ctx, cfg, &plan, provider.Plan.Receipts, store, operator, e, os.Stdout, runtime, apply)
	return finishServiceAuthorizationRecovery(ctx, cfg, store, e, *report, *controllerSource, repairImage.Reference, err)
}

// Once finalization has removed the bootstrap credential, authenticate the
// original permanent Account Manager and let the shared restore path verify the
// native tombstone and installed identities before continuing ordinary stages.
func resumeFinalizedStagingApplicationCreate(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, store secretStore, operator map[string]string, e *environmentCreateExecution, backend environmentKubernetesBackend, controllerSource, report string) error {
	if err := verifyStagingAcceptedApplications(store, e); err != nil {
		return err
	}
	stage, err := freshCreateWorkloadStage(store, e)
	if err != nil {
		return err
	}
	workload, _, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil {
		return err
	}
	if _, err := readFreshProductProgram(store, e); err != nil {
		return err
	}
	if err := qualifyCreateNormalTailDNS(ctx, cfg, &createNormalTailHandoff{Execution: e, OriginalPlan: plan, Receipts: receipts}, operator); err != nil {
		return err
	}
	image, err := qualifyServiceAuthorizationControllerImage(ctx, cfg, plan.Release, controllerSource)
	if err != nil {
		return err
	}
	var originalImage environmentReleaseImage
	imagePath := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "service-authorization-recovery", "controller-release.json")
	if readExecutionJSON(imagePath, &originalImage) != nil || deploymentplan.Digest(originalImage) != deploymentplan.Digest(image) {
		return createProviderRecoveryBlock("finalized staging Controller differs from the original qualified repair")
	}
	origin, transport, err := newEnvironmentCreateAccountClient(ctx, cfg, backend, e)
	if err != nil {
		return err
	}
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, e, backend, operator)
	if err != nil || !recorded {
		return createProviderRecoveryBlock("finalized staging requires its original registered operator")
	}
	client, err := newEnvironmentCreatePKIClient(origin, transport, cfg.Environment, binding.UserID)
	if err != nil {
		return err
	}
	if _, err := client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{
		ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: backend,
		APIOrigin: origin, APIClient: transport, PKIClient: client,
		ServiceAuthorizationRecovery: &createServiceAuthorizationRecovery{ControllerImage: image.Reference, SingleAPIOwner: true},
	}
	apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		return applyEnvironmentCreateStage(ctx, cfg, store, plan, e, stage, definitions, os.Stdout)
	}
	if entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize"); entry != nil && entry.State == "pending" {
		fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.staging.finalization verify original sealed session and published revocation")
		ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
		if plan.PublicTLS != nil {
			ctx = context.WithValue(ctx, environmentCreatePublicTLSKey{}, plan.PublicTLS)
		}
		compiled := &compiledEnvironmentDeployment{Plan: workload, Release: plan.Release, Bindings: environmentIdentityBindings{}, Backend: backend, Store: store, Configuration: cfg}
		compiled, err = finishEnvironmentCreateFreshProducts(ctx, cfg, plan, store, operator, e, compiled, apply)
		if err == nil {
			err = finishEnvironmentCreateRouting(ctx, cfg, plan, receipts, store, operator, e, compiled, os.Stdout)
		}
		return finishServiceAuthorizationRecovery(ctx, cfg, store, e, report, controllerSource, image.Reference, err)
	}
	fmt.Fprintln(os.Stdout, "[RUNNING] deployment.recovery.staging.finalized verify original native finalization and continue acceptance")
	err = executeEnvironmentCreateWithRuntime(ctx, cfg, plan, receipts, store, operator, e, os.Stdout, runtime, apply)
	return finishServiceAuthorizationRecovery(ctx, cfg, store, e, report, controllerSource, image.Reference, err)
}

func recoverServiceAuthorizationControllerStage(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, e *environmentCreateExecution, backend environmentKubernetesBackend, checkpoint environmentCreateJournal, snapshot, stage string, image environmentReleaseImage, apply environmentCreateBootstrapApply) error {
	trust := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/service-root-trust")
	if trust == nil {
		return createProviderRecoveryBlock("original Service Root trust stage is missing")
	}
	trustPlan, trustJournal, err := originalCreateChildAtIntent(store, e, *trust)
	if err != nil || !trustJournal.Accepted {
		return createProviderRecoveryBlock("original Service Root trust has no accepted child")
	}
	expectedImage := plan.Bootstrap.Image
	if len(checkpoint.Entries) != len(e.Journal.Entries) {
		expectedImage = image.Reference
	}
	definitions, err := qualifyServiceAuthorizationControllers(ctx, cfg, store, backend, trustPlan, trustJournal, plan.Bootstrap.Image, expectedImage)
	if err != nil {
		return err
	}
	if len(checkpoint.Entries) == len(e.Journal.Entries) {
		before, err := readBoundedEnvironmentFile(e.JournalPath, true)
		if err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, snapshot, before); err != nil {
			return err
		}
	} else {
		var saved environmentCreateJournal
		if err := readExecutionJSON(filepath.Join(store.Root, snapshot), &saved); err != nil || deploymentplan.Digest(saved.Entries) != deploymentplan.Digest(checkpoint.Entries) || saved.OwnerToken != e.Journal.OwnerToken {
			return createProviderRecoveryBlock("original pre-repair journal does not match the resumed controller stage")
		}
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(filepath.Dir(snapshot), "controller-release.json"), mustProviderRecoveryJSON(image)); err != nil {
		return err
	}
	return apply(ctx, stage, definitions)
}

func serviceAuthorizationEnrollmentStages(j environmentCreateJournal, session string) ([]environmentCreateJournalEntry, error) {
	bad := func() ([]environmentCreateJournalEntry, error) {
		return nil, createProviderRecoveryBlock("original stopped session is outside its recorded enrollment stage sequence")
	}
	stages := []string{"service-enrollment-issuer", "service-issuer-server-enrollment", "service-managed-enrollment-issuer", "service-core-enrollment", "service-managed-core", "application-client-enrollment", "transport-client-enrollment"}
	anchor := -1
	for i, entry := range j.Entries {
		if entry.Key == "kubernetes/service-credential-secrets" {
			anchor = i
		}
		if entry.State != "completed" && (i != len(j.Entries)-1 || entry.State != "pending" || len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero()) || entry.State == "completed" && (entry.CompletedAt.IsZero() || len(entry.Receipt) == 0) {
			return bad()
		}
	}
	if anchor < 0 || len(j.Entries) < anchor+4 || len(j.Entries) > anchor+5+len(stages) {
		return bad()
	}
	roles := j.Entries[anchor+1 : anchor+4]
	if roles[0].Key != "openbao-policy-create-"+session+"-cleanup-controller" || !strings.HasPrefix(roles[1].Key, "openbao-role-pki-controller-"+j.Environment+"-") || !strings.HasPrefix(roles[2].Key, "openbao-role-certissuer-"+j.Environment+"-") || roles[2].State != "completed" {
		return bad()
	}
	var tail []environmentCreateJournalEntry
	for _, entry := range j.Entries[anchor+4:] {
		if len(tail) == 4 && strings.HasPrefix(entry.Key, "kubernetes/service-core-enrollment-rwo-") && entry.State == "completed" {
			continue
		}
		i := len(tail)
		if i >= len(stages) || entry.Key != "kubernetes/"+stages[i] || entry.Phase != stages[i] {
			return bad()
		}
		tail = append(tail, entry)
	}
	return tail, nil
}

func loadServiceAuthorizationEnrollment(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, e *environmentCreateExecution, runtime environmentCreateBootstrapRuntime, recovery *createServiceAuthorizationRecovery, repairStage string, repairImage environmentReleaseImage, resumeMQTT, resumeHosts bool) error {
	bad := func(cause string) error { return createProviderRecoveryBlock("original Service enrollment " + cause) }
	base := environmentCreateCeremonyDirectory(runtime, "service")
	var savedImage environmentReleaseImage
	releaseFile := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "service-authorization-recovery", "controller-release.json")
	if readExecutionJSON(releaseFile, &savedImage) != nil || deploymentplan.Digest(savedImage) != deploymentplan.Digest(repairImage) {
		return bad("Controller publication binding changed")
	}
	var intent createServiceSessionIntent
	var observation struct {
		Session        json.RawMessage                    `json:"session"`
		CleanupBinding environmentBootstrapCleanupBinding `json:"cleanup_binding"`
		CredentialUIDs map[string]string                  `json:"credential_uids"`
		CustodyReader  string                             `json:"custody_reader"`
	}
	ns := plan.Stack + "-video-cloud"
	namespaces := []string{ns, plan.Stack + "-account-manager", plan.Stack + "-secrets"}
	subjects, err := environmentCreatePlannedServiceSubjects(cfg, plan.Bootstrap)
	if err != nil || readExecutionJSON(filepath.Join(store.Root, base, "session-intent.json"), &intent) != nil || !environmentBootstrapOwnerUUID.MatchString(intent.SessionID) || intent.IssuerID != recovery.Operation.IssuerID || intent.RootSHA != recovery.Root.RootFingerprint || !slices.Equal(intent.Subjects, subjects) || !slices.Equal(intent.Namespaces, namespaces) || intent.Claim != "pki-service-bootstrap-state" || readExecutionJSON(filepath.Join(store.Root, base, "session-observed.json"), &observation) != nil {
		return bad("session intent and native observation differ")
	}
	checkpoint := e.Journal
	if resumeHosts {
		anchor := slices.IndexFunc(checkpoint.Entries, func(entry environmentCreateJournalEntry) bool { return entry.Key == "pki-reserve-mqtt-root" })
		if anchor < 1 || checkpoint.Entries[anchor-1].Key != "kubernetes/transport-client-enrollment" {
			return bad("original enrollment boundary was not recorded before MQTT")
		}
		checkpoint.Entries = checkpoint.Entries[:anchor]
	} else if resumeMQTT {
		boundary, err := validateStagingMQTTRootTail(stagingMQTTSourceEntries(checkpoint.Entries))
		if err != nil {
			return err
		}
		checkpoint.Entries = checkpoint.Entries[:boundary]
	}
	tail, err := serviceAuthorizationEnrollmentStages(checkpoint, intent.SessionID)
	if err != nil {
		return err
	}
	accepted := func(key string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
		entry := createOpenBaoACKCompletionEntry(e.Journal, key)
		if entry == nil || entry.State != "completed" {
			return nil, deploymentplan.Journal{}, bad("missing accepted stage " + key)
		}
		child, journal, err := originalCreateChildAtIntent(store, e, *entry)
		if key == "kubernetes/"+stagingCoreEnrollmentStage && err == nil && !journal.Accepted {
			return acceptedStagingCoreForward(store, e, *entry, child, journal)
		}
		if err != nil || !createOpenBaoACKCompletedChild(child, journal) {
			return nil, deploymentplan.Journal{}, bad("invalid accepted stage " + key)
		}
		return child, journal, nil
	}
	if _, _, err := accepted("kubernetes/" + repairStage); err != nil {
		return err
	}
	credentials, journal, err := accepted("kubernetes/service-credential-secrets")
	if err != nil {
		return err
	}
	currentCredentials := *credentials
	currentCredentials.Resources = nil
	for _, resource := range credentials.Resources {
		if strings.HasSuffix(resource.ID, "/service-bootstrap-"+intent.SessionID) && resource.Desired["kind"] == "Secret" {
			currentCredentials.Resources = append(currentCredentials.Resources, resource)
		}
	}
	if len(currentCredentials.Resources) != len(namespaces) {
		return bad("original credentials stage does not bind each immutable session Secret")
	}
	if err := verifyOriginalMigrationCheckpoint(ctx, cfg, store, runtime.Backend, &currentCredentials, journal); err != nil {
		return err
	}
	completed := map[string]bool{}
	for _, entry := range tail {
		if entry.State == "completed" {
			if _, _, err := accepted(entry.Key); err != nil {
				return err
			}
			completed[entry.Phase] = true
		}
	}
	if resumeMQTT && !completed["transport-client-enrollment"] {
		return bad("MQTT continuation requires all original enrollment stages to be accepted")
	}
	active, err := readStagingResumeIssuer(ctx, cfg, runtime, intent.IssuerID)
	var recorded map[string]any
	activation := createOpenBaoACKCompletionEntry(e.Journal, "pki-activate-"+recovery.Operation.ID)
	if err != nil || activation == nil || activation.State != "completed" || json.Unmarshal(activation.Receipt, &recorded) != nil || deploymentplan.Digest(active) != deploymentplan.Digest(recorded) {
		return bad("native active issuer differs from its original receipt")
	}
	intermediate, err := environmentCreateRegisteredIssuerFromMap(active, recovery.Root.RootFingerprint)
	if err != nil || intermediate.ID != intent.IssuerID || intermediate.Status != "active" || intermediate.Domain != "service" || intermediate.Kind != "intermediate" || active["parent_issuer_id"] != recovery.Root.ID {
		return bad("native active issuer lineage changed")
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'subjects',subjects,'expires_at',expires_at) FROM pki_deployment_bootstrap_sessions WHERE environment='staging' AND session_id='" + intent.SessionID + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var liveSession, originalSession map[string]any
	if err != nil || decodeEnvironmentJSON(raw, &liveSession) != nil || decodeEnvironmentJSON(observation.Session, &originalSession) != nil || deploymentplan.Digest(liveSession) != deploymentplan.Digest(originalSession) || liveSession["status"] != "active" || liveSession["session_id"] != intent.SessionID || liveSession["issuer_id"] != intent.IssuerID || liveSession["root_sha256"] != intent.RootSHA || deploymentplan.Digest(liveSession["subjects"]) != deploymentplan.Digest(subjects) {
		return bad("native bootstrap session is not the original active session")
	}
	expires, err := time.Parse(time.RFC3339, deploymentplan.String(liveSession["expires_at"]))
	deadline, bounded := ctx.Deadline()
	if err != nil || !bounded || !expires.After(deadline) {
		return bad("native bootstrap session does not cover the bounded continuation")
	}
	secret := "service-bootstrap-" + intent.SessionID
	for _, namespace := range namespaces {
		uid := observation.CredentialUIDs[namespace]
		if !environmentBootstrapOwnerUUID.MatchString(uid) || journal.Owners["Secret/"+namespace+"/"+secret] != uid {
			return bad("bootstrap Secret owner changed")
		}
	}
	namespace, err := runtime.Backend.Read(ctx, "Namespace//"+ns)
	cleanup := observation.CleanupBinding
	if err != nil || len(observation.CredentialUIDs) != len(namespaces) || observation.CustodyReader != "pki-bootstrap-custody-"+e.Journal.PlanSHA256[:12] || cleanup.SessionID != intent.SessionID || cleanup.SecretUID != observation.CredentialUIDs[ns] || cleanup.NamespaceUID != deploymentplan.Map(namespace["metadata"])["uid"] || validateEnvironmentBootstrapCleanupBinding(&cleanup, cfg.Environment) != nil {
		return bad("original custody and cleanup binding changed")
	}
	crl, err := readBoundedEnvironmentFile(filepath.Join(store.Root, recovery.CRLFile), true)
	if err != nil {
		return err
	}
	recovery.Enrollment = &environmentCreateServiceCeremony{Root: recovery.Root, Intermediate: intermediate, RootCRLFile: recovery.CRLFile, RootCRLPEM: string(crl), RootCRLSecret: "pki-create-service-root-crl", BootstrapSessionID: intent.SessionID, BootstrapClaim: intent.Claim, BootstrapCredentialNamespace: ns, BootstrapCredentialNamespaces: namespaces, BootstrapCredentialSecret: secret, CredentialUIDs: observation.CredentialUIDs, CustodyReader: observation.CustodyReader, CleanupBinding: cleanup}
	recovery.CompletedEnrollmentStages = completed
	if len(tail) > 0 && tail[len(tail)-1].Key == "kubernetes/service-core-enrollment" && tail[len(tail)-1].State == "pending" {
		recovery.CoreForward, err = qualifyStagingServiceCoreForward(ctx, cfg, store, plan, e, runtime, *recovery.Enrollment, tail[len(tail)-1])
		return err
	}
	return nil
}

func reconcileOriginalServiceAuthorization(ctx context.Context, e *environmentCreateExecution, client *environmentCreatePKIClient, root environmentCreateRegisteredIssuer, operation environmentCreateIssuerOperation) error {
	var live environmentCreateIssuerOperation
	if err := client.call(ctx, http.MethodGet, "/v1/platform/pki/operations/"+operation.ID, "", nil, &live); err != nil {
		return err
	}
	approved, err := client.ReadIssuer(ctx, operation.IssuerID)
	if err != nil {
		return err
	}
	if live.ID != operation.ID || live.IssuerID != operation.IssuerID || live.Digest != operation.Digest || live.Status != "approved" || approved["status"] != "approved" || approved["kind"] != "intermediate" || approved["trust_domain"] != "service" || approved["parent_issuer_id"] != root.ID || approved["signer_provider"] != "openbao" {
		return createProviderRecoveryBlock("native registry did not verify the exact existing approved operation and intermediate")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	client.Execution = e
	if err := client.completeMutation("pki-authorize-"+operation.ID, approved); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.service-authorization original-approved-issuer-verified")
	return nil
}

func finishServiceAuthorizationRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, report, source, image string, err error) error {
	if err == nil {
		err = e.CompleteAndReleaseContext(ctx, time.Now().UTC())
	}
	decision := "GO"
	if err != nil {
		decision = "NO-GO"
	}
	if report != "" {
		result := map[string]any{"schema": "rtk-cloud.service-authorization-recovery-result/v1", "environment": cfg.Environment, "decision": decision, "parent_plan_sha256": e.Journal.PlanSHA256, "completed": err == nil, "controller_source": source, "controller_image": image}
		if err != nil {
			result["cause"] = err.Error()
		}
		rel, pathErr := filepath.Rel(store.Root, report)
		if pathErr != nil {
			return pathErr
		}
		if writeErr := store.write(rel, mustProviderRecoveryJSON(result), true); writeErr != nil {
			return writeErr
		}
	}
	fmt.Fprintf(os.Stdout, "%s environment=staging scope=service-authorization-recovery completed=%t\n", decision, err == nil)
	return err
}

// Both initial listeners run the Controller executable. Preserve their UIDs
// and specs and verify readiness for all other accepted Service trust owners.
func qualifyServiceAuthorizationControllers(ctx context.Context, cfg deploymentConfig, store secretStore, backend environmentKubernetesBackend, trust *deploymentplan.Plan, journal deploymentplan.Journal, oldImage, expectedImage string) ([]deploymentplan.Definition, error) {
	retained := *trust
	retained.Resources = nil
	var controllers []deploymentplan.Definition
	for _, resource := range trust.Resources {
		if serviceAuthorizationControllerName(resource.ID, trust.Stack) == "" {
			retained.Resources = append(retained.Resources, resource)
			continue
		}
		id := resource.ID
		live, err := backend.Read(ctx, id)
		if err != nil || journal.Owners[id] == "" || deploymentplan.Map(live["metadata"])["uid"] != journal.Owners[id] {
			return nil, createProviderRecoveryBlock("an original Controller listener Deployment identity changed: " + id)
		}
		normalized := deploymentplan.Clone(live)
		container := serviceAuthorizationControllerContainer(normalized, trust.Stack)
		if actual := container["image"]; actual != oldImage && actual != expectedImage {
			return nil, createProviderRecoveryBlock("an original Controller listener has an unreviewed image or executable: " + id)
		}
		container["image"] = oldImage
		if !deploymentplan.Matches(deploymentplan.Projection(resource.Desired), deploymentplan.Projection(normalized)) {
			return nil, createProviderRecoveryBlock("an original Controller listener changed outside its isolated image: " + id)
		}
		controllers = append(controllers, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase})
	}
	if len(controllers) != 2 {
		return nil, createProviderRecoveryBlock("the accepted Service trust stage must have both original Controller listeners")
	}
	if err := verifyOriginalMigrationCheckpoint(ctx, cfg, store, backend, &retained, journal); err != nil {
		return nil, err
	}
	return controllers, nil
}

func qualifyServiceAuthorizationControllerImage(ctx context.Context, cfg deploymentConfig, release environmentRelease, revision string) (environmentReleaseImage, error) {
	var image environmentReleaseImage
	run := environmentCIImageCommand(cfg)
	sourceDir := filepath.Join(cfg.Workspace, "repos/rtk_video_cloud")
	base := release.Images["LKE_VIDEO_CLOUD_IMAGE"].SourceRevision
	parents, err := run(ctx, "git", "-C", sourceDir, "rev-list", "--parents", "-n", "1", revision)
	fields := strings.Fields(string(parents))
	if err != nil || len(fields) != 2 || fields[0] != revision || fields[1] != base {
		return image, createProviderRecoveryBlock("formal Controller repair must directly follow the original Video Cloud source")
	}
	diff, err := run(ctx, "git", "-C", sourceDir, "diff", "--name-only", base, revision)
	files := strings.Fields(string(diff))
	if err != nil || len(files) != 2 || files[0] != "internal/pkicontrollerapp/provider_transport.go" || files[1] != "internal/pkicontrollerapp/provider_transport_test.go" {
		return image, createProviderRecoveryBlock("formal repair source includes changes outside the initial Controller transport")
	}
	const repo = "hkt999rtk/rtk_video_cloud"
	evidence, runID, attempt, err := latestEnvironmentPublication(ctx, repo, "release.yml", revision, false, run)
	if err != nil {
		return image, createProviderRecoveryBlock("formal Controller repair publication is unavailable")
	}
	image, err = selectEnvironmentPublishedImage(evidence, repo, runID, attempt, revision, "ghcr.io/"+repo+"/video-cloud-api", false)
	if err != nil {
		return image, createProviderRecoveryBlock("formal Controller repair digest cannot be selected from the exact CI artifact")
	}
	if _, err := run(ctx, "docker", "pull", "--platform", "linux/amd64", image.Reference); err != nil {
		return image, createProviderRecoveryBlock("formal Controller repair image could not be pulled")
	}
	if err := verifyDeploymentUpgradeImageCIAtWithCommand(ctx, image.Reference, revision, run); err != nil {
		return image, createProviderRecoveryBlock("formal Controller repair image provenance or CI is incomplete")
	}
	return image, nil
}

func bindCreateServiceAuthorizationController(definitions []deploymentplan.Definition, stack, image string) {
	for i, definition := range definitions {
		if serviceAuthorizationControllerContainer(definition.Object, stack) == nil {
			continue
		}
		object := deploymentplan.Clone(definition.Object)
		serviceAuthorizationControllerContainer(object, stack)["image"] = image
		definitions[i].Object = object
	}
}

func serviceAuthorizationControllerName(id, stack string) string {
	prefix := "Deployment/" + stack + "-video-cloud/"
	switch id {
	case prefix + "pki-controller", prefix + "certissuer":
		return strings.TrimPrefix(id, prefix)
	}
	return ""
}

func serviceAuthorizationControllerContainer(object deploymentplan.Object, stack string) deploymentplan.Object {
	name := serviceAuthorizationControllerName(deploymentplan.ID(object), stack)
	if name == "" {
		return nil
	}
	for _, value := range certIssuerObjectList(environmentPKIPod(object)["containers"]) {
		container := deploymentplan.Map(value)
		command := certIssuerObjectList(container["command"])
		if container["name"] == name && len(command) == 1 && command[0] == "/app/pkicontroller" {
			return container
		}
	}
	return nil
}
