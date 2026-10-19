package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentFreshProductBinding struct{ ParentSHA256, ScopeSHA256, SessionID, IssuerID string }
type environmentFreshProductProgram struct {
	Lifecycle        createNormalTailProductLifecycle
	SourceLeaseCount int
}

func freshProductProgramPath(parent string) string {
	return filepath.Join("deployment/create", parent, "bootstrap/product-identities/fresh-program.json")
}

func readFreshProductProgram(store secretStore, e *environmentCreateExecution) (*environmentFreshProductProgram, error) {
	var saved environmentFreshProductProgram
	if e == nil || e.Store != store || createOpenBaoACKCompletionRead(store, freshProductProgramPath(e.Journal.PlanSHA256), &saved) != nil {
		return nil, environmentCreateBootstrapBlocker("fresh product program has no original selected parent")
	}
	l := &saved.Lifecycle
	var active environmentPreflightActive
	var artifact environmentCreateArtifact
	var original environmentCreatePlan
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/preflight/active.json"), &active) != nil || active.PlanSHA256 != e.Journal.PlanSHA256 || !environmentPreflightArtifactName.MatchString(active.Artifact) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/preflight", active.Artifact, "artifact.json"), &artifact) != nil || artifact.PlanSHA256 != e.Journal.PlanSHA256 || deploymentplan.Digest(artifact) != e.Journal.ArtifactSHA256 || decodeEnvironmentJSON(artifact.Plan, &original) != nil || deploymentplan.Digest(l.Bootstrap) != deploymentplan.Digest(original.Bootstrap) || deploymentplan.Digest(l.Policy) != deploymentplan.Digest(findEnvironmentCreateDomainPolicy(original.Bootstrap, "service")) || len(l.Outputs) != len(original.Bootstrap.ProductIdentities) || len(l.Outputs) == 0 {
		return nil, environmentCreateBootstrapBlocker("fresh products differ from the original fully qualified create plan")
	}
	for i, output := range l.Outputs {
		expected := original.Bootstrap.ProductIdentities[i]
		output.RootID, output.RootSHA256, output.IssuerID = "", "", ""
		if deploymentplan.Digest(output) != deploymentplan.Digest(expected) {
			return nil, environmentCreateBootstrapBlocker("fresh product subject or dedicated owner differs from its original create intent")
		}
	}
	expectedConnectivity, err := environmentFreshConnectivitySource(deploymentConfig{Environment: l.Bootstrap.Environment, Values: map[string]string{"CLOUD_STACK_NAME": l.Bootstrap.Stack}}, l.Bootstrap)
	if err != nil || (len(expectedConnectivity) != 0 && deploymentplan.Digest(expectedConnectivity) != deploymentplan.Digest(l.SourceDefinitions)) || (len(expectedConnectivity) == 0 && len(l.SourceDefinitions) != 0) {
		return nil, environmentCreateBootstrapBlocker("fresh product connectivity differs from its original qualified recipe")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh")
	if l.ParentSHA256 != e.Journal.PlanSHA256 || l.OwnerSHA256 != deploymentplan.Digest(e.owner) || entry == nil || entry.State != "completed" || entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(l) || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(deploymentplan.Object{"program_sha256": productIdentityScope(l)}) {
		return nil, environmentCreateBootstrapBlocker("fresh product program differs from its original admitted stage")
	}
	index := -1
	for i := range e.Journal.Entries {
		if &e.Journal.Entries[i] == entry {
			index = i
			break
		}
	}
	if index < 0 || saved.SourceLeaseCount < 0 || saved.SourceLeaseCount > len(e.Journal.KubernetesLeases) {
		return nil, environmentCreateBootstrapBlocker("fresh product source prefix is unavailable")
	}
	prefix := e.Journal
	prefix.Entries = slices.Clone(prefix.Entries[:index])
	prefix.KubernetesLeases = slices.Clone(prefix.KubernetesLeases[:saved.SourceLeaseCount])
	prefix.CompletedAt = time.Time{}
	if deploymentplan.Digest(prefix) != l.SourceSHA256 {
		return nil, environmentCreateBootstrapBlocker("fresh product original source or lease prefix changed")
	}
	return &saved, nil
}

func executeEnvironmentCreateFreshProductIdentities(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) error {
	if len(p.ProductIdentities) == 0 {
		return nil
	}
	if runtime.ServiceSessionRecovery != nil || runtime.Execution == nil || runtime.PKIClient == nil || runtime.Execution.Store != store || runtime.PKIClient.Execution != runtime.Execution || runtime.Execution.Check(ctx) != nil {
		return environmentCreateBootstrapBlocker("fresh product producer requires its original new-generation ceremony")
	}
	subjects, err := environmentCreatePlannedServiceSubjects(cfg, p)
	if err != nil {
		return err
	}
	if len(subjects) != 9+len(p.ProductIdentities) {
		return environmentCreateBootstrapBlocker("fresh session does not declare every original and product owner")
	}
	var program *environmentFreshProductProgram
	if createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "product-identities/fresh") != nil {
		program, err = readFreshProductProgram(store, runtime.Execution)
		if err != nil {
			return err
		}
	} else {
		l := createNormalTailProductLifecycle{ParentSHA256: runtime.ExecutionSHA256, SourceSHA256: deploymentplan.Digest(runtime.Execution.Journal), OwnerSHA256: deploymentplan.Digest(runtime.Execution.owner), Root: s.Root, Intermediate: s.Intermediate, Policy: findEnvironmentCreateDomainPolicy(p, "service"), Bootstrap: p, StorageClass: firstNonEmpty(cfg.Values["LKE_STORAGE_CLASS"], "linode-block-storage-retain"), TransportCAPEM: runtime.TransportCAPEM, Outputs: slices.Clone(p.ProductIdentities)}
		l.SourceDefinitions, err = environmentFreshConnectivitySource(cfg, p)
		if err != nil {
			return err
		}
		for i := range l.Outputs {
			l.Outputs[i].RootID, l.Outputs[i].RootSHA256, l.Outputs[i].IssuerID = s.Root.ID, s.Root.RootFingerprint, s.Intermediate.ID
		}
		program = &environmentFreshProductProgram{Lifecycle: l, SourceLeaseCount: len(runtime.Execution.Journal.KubernetesLeases)}
		if _, err = writeProductArtifact(store, freshProductProgramPath(l.ParentSHA256), program); err != nil {
			return err
		}
		if err = runtime.Execution.BeginEntry("product-identities/fresh", "bootstrap", mustProviderRecoveryJSON(l), time.Now().UTC()); err != nil {
			return err
		}
		if err = runtime.Execution.RecordReceipt("product-identities/fresh", mustProviderRecoveryJSON(deploymentplan.Object{"program_sha256": productIdentityScope(&l)})); err != nil {
			return err
		}
		if err = runtime.Execution.CompleteEntry("product-identities/fresh", time.Now().UTC()); err != nil {
			return err
		}
	}
	l := &program.Lifecycle
	if deploymentplan.Digest(l.Bootstrap) != deploymentplan.Digest(p) || l.Root.ID != s.Root.ID || l.Intermediate.ID != s.Intermediate.ID {
		return environmentCreateBootstrapBlocker("fresh product retry changed its original plan or issuers")
	}
	scopedClient := *runtime.PKIClient
	scopedClient.mutationScope = productIdentityScope(l)
	runtime.PKIClient = &scopedClient
	scopedApply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		return apply(ctx, "product-identities-"+productIdentityScope(l)[:12]+"-"+stage, defs)
	}
	path, err := store.safePath(productIdentitySessionPath(l))
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		if len(l.SourceDefinitions) != 0 {
			dependencies, err := environmentProductEnrollmentConnectivityDefinitions(ctx, cfg, l, s)
			if err != nil {
				return err
			}
			if err = scopedApply(ctx, "enrollment-network", dependencies); err != nil {
				return err
			}
		}
		outputs, err := enrollProductIdentityOutputs(ctx, cfg, p, store, runtime, l, s, scopedApply)
		if err != nil {
			return err
		}
		installed := environmentProductIdentitySession{Schema: productIdentitySessionSchema, ParentSHA256: l.ParentSHA256, PlanSHA256: productIdentityScope(l), OwnerSHA256: l.OwnerSHA256, Ceremony: s, Outputs: outputs}
		if _, err = writeProductArtifact(store, productIdentitySessionPath(l), installed); err != nil {
			return err
		}
	} else if err != nil {
		return environmentCreateBootstrapBlocker("fresh product output path cannot be inspected")
	}
	installed, err := readProductInstalled(store, l)
	if err != nil {
		return err
	}
	if err = verifyProductIdentityOutputs(ctx, cfg, runtime, l, installed); err != nil {
		return err
	}
	closed, err := freshProductEnrollmentClosedDefinitions(store, runtime.Execution, p, s)
	if err != nil {
		return err
	}
	return apply(ctx, "fresh-product-enrollment-closed", closed)
}

func freshProductEnrollmentClosedDefinitions(store secretStore, e *environmentCreateExecution, p environmentCreateBootstrapPlan, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	plan, journal, err := acceptedCreateStage(store, e, "kubernetes/managed-complete-issuer")
	if err != nil {
		return nil, err
	}
	var out []deploymentplan.Definition
	for _, r := range plan.Resources {
		if r.ID != "Deployment/"+p.Stack+"-video-cloud/certissuer" {
			continue
		}
		uid := journal.Owners[r.ID]
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return nil, environmentCreateBootstrapBlocker("fresh issuer provisioning lacks its original accepted owner")
		}
		o := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(o["metadata"])["uid"] = uid
		var issuer deploymentplan.Object
		for _, raw := range certIssuerObjectList(environmentPKIPod(o)["containers"]) {
			c := deploymentplan.Map(raw)
			if c["name"] == "certissuer" {
				if issuer != nil {
					return nil, environmentCreateBootstrapBlocker("fresh issuer provisioning has ambiguous native owners")
				}
				issuer = c
			}
		}
		subjects := environmentCreateServiceEnrollmentSubjects(findEnvironmentCreateDomainPolicy(p, "service"))
		if issuer == nil || mqttFoundationEnv(issuer, "CERT_ISSUER_REGISTRY_ONLY_ENABLED") != "true" || mqttFoundationEnv(issuer, "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER") != "service:deployment-bootstrap" || mqttFoundationEnv(issuer, "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID") != s.BootstrapSessionID || mqttFoundationEnv(issuer, "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS") != strings.Join(subjects, ",") || mqttFoundationEnv(issuer, "CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256") != s.Root.RootFingerprint {
			return nil, environmentCreateBootstrapBlocker("fresh issuer provisioning differs from its exact qualified fourteen-subject session")
		}
		issuer["env"] = environmentCreateServiceWithoutEnv(issuer["env"], "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS")
		out = append(out, deploymentplan.Definition{Object: o, Phase: r.Phase})
	}
	if len(out) != 1 {
		return nil, environmentCreateBootstrapBlocker("fresh issuer provisioning has no unique original native issuer")
	}
	return out, nil
}

// A completed fresh bootstrap is reused through its original receipt and live
// permanent outputs. It must not revisit the seed ceremony after native cleanup.
func restoreEnvironmentFreshProductBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, runtime environmentCreateBootstrapRuntime, apply ...environmentCreateBootstrapApply) (environmentIdentityBindings, bool, error) {
	var empty environmentIdentityBindings
	if len(p.ProductIdentities) == 0 || runtime.ServiceSessionRecovery != nil {
		return empty, false, nil
	}
	finalized := filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "finalized.json")
	path, err := store.safePath(finalized)
	if err != nil {
		return empty, false, err
	}
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		return empty, false, nil
	} else if err != nil {
		return empty, false, environmentCreateBootstrapBlocker("original fresh finalization cannot be inspected")
	}
	program, err := readFreshProductProgram(store, runtime.Execution)
	if err != nil {
		return empty, false, err
	}
	l := &program.Lifecycle
	installed, err := readProductInstalled(store, l)
	if err != nil {
		return empty, false, err
	}
	if deploymentplan.Digest(l.Bootstrap) != deploymentplan.Digest(p) {
		return empty, false, environmentCreateBootstrapBlocker("fresh finalized bootstrap recipe changed")
	}
	if err = verifyProductIdentityOutputs(ctx, cfg, runtime, l, installed); err != nil {
		return empty, false, err
	}
	var saved struct {
		Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
		Tombstone any                                    `json:"custody_tombstone"`
		UIDs      map[string]string                      `json:"credential_uids"`
	}
	if createOpenBaoACKCompletionRead(store, finalized, &saved) != nil {
		return empty, false, environmentCreateBootstrapBlocker("fresh finalized bootstrap record is malformed")
	}
	entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "service-session-finalize")
	if entry != nil && entry.State != "completed" {
		if err = reconcileProductFinalization(ctx, cfg, runtime, l, installed, saved.Native); err != nil {
			return empty, false, err
		}
	}
	if _, err = verifyProductFinalization(ctx, cfg, runtime, l, installed); err != nil {
		return empty, false, err
	}
	var original environmentIdentityBindings
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/bindings.json"), &original) != nil || validateEnvironmentIdentityBindings(original, cfg.Environment) != nil || original.ProductIdentities == nil || original.ProductIdentities.ScopeSHA256 != productIdentityScope(l) {
		return empty, false, environmentCreateBootstrapBlocker("fresh finalized bootstrap lacks its original complete bindings")
	}
	if len(apply) > 1 || len(apply) == 1 && apply[0] == nil {
		return empty, false, environmentCreateBootstrapBlocker("fresh product cleanup has no unique original stage adapter")
	}
	var stageApply environmentCreateBootstrapApply
	if len(apply) == 1 {
		stageApply = apply[0]
	}
	// Resume the original cleanup before checking the broad bootstrap owner
	// set. A recorded partial retirement must not make its own retry fail
	// because an original preparation Deployment is already gone.
	if err = completeFreshProductPreparations(ctx, cfg, store, runtime.Execution, l, installed, runtime.Backend, stageApply); err != nil {
		return empty, false, err
	}
	retired, err := productPreparationDefinitions(cfg, store, runtime.Execution, l, installed, true, true)
	if err != nil {
		return empty, false, err
	}
	retiredOwners := map[string]string{}
	for _, d := range retired {
		retiredOwners[deploymentplan.ID(d.Object)] = deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"])
	}
	live, err := runtime.Backend.Inventory(ctx, p.Stack)
	if err != nil {
		return empty, false, err
	}
	current := map[string]deploymentplan.Object{}
	for _, o := range live {
		current[deploymentplan.ID(o)] = o
	}
	for id, uid := range original.Owners {
		cleaned := retiredOwners[id] == uid && current[id] == nil
		for _, ns := range installed.Ceremony.BootstrapCredentialNamespaces {
			if id == "Secret/"+ns+"/"+installed.Ceremony.BootstrapCredentialSecret && uid == installed.Ceremony.CredentialUIDs[ns] && current[id] == nil {
				cleaned = true
			}
		}
		if cleaned {
			continue
		}
		if current[id] == nil || deploymentplan.Map(current[id]["metadata"])["uid"] != uid {
			return empty, false, environmentCreateBootstrapBlocker("fresh finalized bootstrap original owner changed")
		}
	}
	b, err := observeEnvironmentBindings(cfg.Environment, release, live)
	if err != nil {
		return empty, false, err
	}
	if err = applyEnvironmentFreshProductBindings(ctx, cfg, runtime.Execution, &b); err != nil {
		return empty, false, err
	}
	return b, true, runtime.Execution.Check(ctx)
}

func applyEnvironmentFreshProductBindings(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b *environmentIdentityBindings) error {
	if e == nil || createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil
	}
	program, err := readFreshProductProgram(e.Store, e)
	if err != nil {
		return err
	}
	installed, err := readProductInstalled(e.Store, &program.Lifecycle)
	if err != nil {
		return err
	}
	for _, receipt := range installed.Outputs {
		if b.Owners[receipt.ResourceID] != receipt.SecretUID {
			return environmentCreateBootstrapBlocker("fresh product mirror differs from its original native UID")
		}
	}
	b.ProductIdentities = &environmentFreshProductBinding{ParentSHA256: e.Journal.PlanSHA256, ScopeSHA256: productIdentityScope(&program.Lifecycle), SessionID: installed.Ceremony.BootstrapSessionID, IssuerID: installed.Ceremony.Intermediate.ID}
	return e.Check(ctx)
}

func bindFreshProductTargetDefinitions(ctx context.Context, cfg deploymentConfig, store secretStore, b environmentIdentityBindings, defs []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if b.ProductIdentities == nil {
		return defs, nil
	}
	runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
	if !ok || runtime.Execution == nil || runtime.Execution.Store != store {
		return nil, environmentCreateBootstrapBlocker("fresh product target lacks the original held execution")
	}
	e := runtime.Execution
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return nil, err
	}
	l := &program.Lifecycle
	installed, err := readProductInstalled(store, l)
	if err != nil {
		return nil, err
	}
	if b.ProductIdentities.ParentSHA256 != l.ParentSHA256 || b.ProductIdentities.ScopeSHA256 != productIdentityScope(l) || b.ProductIdentities.SessionID != installed.Ceremony.BootstrapSessionID || b.ProductIdentities.IssuerID != installed.Ceremony.Intermediate.ID {
		return nil, environmentCreateBootstrapBlocker("fresh product binding changed its original installed outputs")
	}
	finalizer := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	bootstrap := finalizer == nil || finalizer.State != "completed"
	outputs := map[string]environmentCreateProductIdentity{}
	for _, p := range l.Outputs {
		outputs[p.ID] = p
	}
	counts := map[string]int{}
	result := make([]deploymentplan.Definition, len(defs))
	for i, d := range defs {
		d.Object = deploymentplan.Clone(d.Object)
		if err = resolveCreateNormalTailProductACK(d.Object, outputs, counts, installed.Ceremony, bootstrap); err != nil {
			return nil, err
		}
		result[i] = d
	}
	for id := range outputs {
		if counts[id] != 1 {
			return nil, environmentCreateBootstrapBlocker("fresh product has no unique maintained registration consumer")
		}
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/fresh-product-producer-retire") != nil {
		// Only genuine completed retirement permits an owner to leave the
		// maintained target. Partial cleanup is resumed before this renderer.
		if err = productPreparationsRetired(ctx, cfg, store, e, l, installed, runtime.Backend, true); err != nil {
			return nil, err
		}
	} else {
		preparations, prepErr := productPreparationDefinitions(cfg, store, e, l, installed, false, true)
		if prepErr != nil {
			return nil, prepErr
		}
		seen := map[string]bool{}
		for _, d := range result {
			seen[deploymentplan.ID(d.Object)] = true
		}
		for _, d := range preparations {
			if seen[deploymentplan.ID(d.Object)] {
				return nil, environmentCreateBootstrapBlocker("fresh product preparation duplicates a maintained target owner")
			}
			result = append(result, d)
		}
	}
	if len(l.SourceDefinitions) != 0 {
		dependencies, err := environmentProductEnrollmentConnectivityDefinitions(ctx, cfg, l, installed.Ceremony)
		if err != nil {
			return nil, err
		}
		result = append(result, dependencies...)
	}
	if err := bindEnvironmentManagedMQTTConsumers(result, installed.Ceremony.BootstrapSessionID); err != nil {
		return nil, err
	}
	return result, e.Check(ctx)
}

// The outer fresh-create driver invokes this after the real complete workload
// is accepted. It returns the final steady target for normal FULL acceptance.
func finishEnvironmentCreateFreshProducts(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, store secretStore, operator map[string]string, e *environmentCreateExecution, c *compiledEnvironmentDeployment, apply environmentCreateBootstrapApply) (*compiledEnvironmentDeployment, error) {
	if len(plan.Bootstrap.ProductIdentities) == 0 {
		return c, nil
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return nil, err
	}
	l := &program.Lifecycle
	installed, err := readProductInstalled(store, l)
	if err != nil {
		return nil, err
	}
	s := installed.Ceremony
	origin, transport, err := newEnvironmentCreateAccountClient(ctx, cfg, c.Backend, e)
	if err != nil {
		return nil, err
	}
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, e, c.Backend, operator)
	if err != nil || !recorded {
		return nil, environmentCreateBootstrapBlocker("fresh finalizer requires its original registered operator")
	}
	client, err := newEnvironmentCreatePKIClient(origin, transport, cfg.Environment, binding.UserID)
	if err != nil {
		return nil, err
	}
	if _, err = client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return nil, err
	}
	client.Execution = e
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: c.Backend, PKIClient: client}
	finalizedPath, err := store.safePath(filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "finalized.json"))
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Lstat(finalizedPath); statErr == nil {
		var saved struct {
			Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
			Tombstone any                                    `json:"custody_tombstone"`
			UIDs      map[string]string                      `json:"credential_uids"`
		}
		if createOpenBaoACKCompletionRead(store, filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "finalized.json"), &saved) != nil {
			return nil, environmentCreateBootstrapBlocker("fresh finalization record is malformed")
		}
		entry := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
		if entry != nil && entry.State != "completed" {
			if err = reconcileProductFinalization(ctx, cfg, runtime, l, installed, saved.Native); err != nil {
				return nil, err
			}
		}
	} else if !os.IsNotExist(statErr) {
		return nil, environmentCreateBootstrapBlocker("fresh finalization record cannot be inspected")
	} else {
		subjects, subjectErr := environmentCreatePlannedServiceSubjects(cfg, plan.Bootstrap)
		if subjectErr != nil {
			return nil, subjectErr
		}
		if err = waitProductSessionInstallations(ctx, cfg, runtime, s, subjects); err != nil {
			return nil, err
		}
		if err = finalizeEnvironmentCreateServiceSession(ctx, client, cfg, plan.Bootstrap, store, runtime, s, operator); err != nil {
			return nil, err
		}
	}
	if _, err = verifyProductFinalization(ctx, cfg, runtime, l, installed); err != nil {
		return nil, err
	}
	// Remove ACK settings using the original accepted catalog, then retire
	// the six native preparation owners. Rendering while those owners still
	// mount the erased bootstrap credential would produce an invalid target.
	ctx = withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	if err = completeFreshProductPreparations(ctx, cfg, store, e, l, installed, c.Backend, apply); err != nil {
		return nil, err
	}
	final, err := compileEnvironmentDeploymentWithImagesTarget(ctx, cfg, "create", plan.Release, nil)
	if err != nil {
		return nil, err
	}
	return final, e.Check(ctx)
}

// Both fresh completion and early finalized-bootstrap restoration use this
// same original pair of ordinary children, including partial delete outcomes.
func completeFreshProductPreparations(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, backend deploymentplan.Backend, apply environmentCreateBootstrapApply) error {
	initial, journal, err := acceptedCreateStage(store, e, "kubernetes/workload")
	if err != nil {
		return err
	}
	input, err := readEnvironmentCreateStageInput(store, e, "workload")
	if err != nil || input == nil || input.SHA256() != initial.SHA256() {
		return environmentCreateBootstrapBlocker("fresh cleanup lost its original accepted workload input")
	}
	progress, exists, err := readEnvironmentCreateStageProgress(store, e, "workload", input)
	if err != nil || !exists || !progress.Accepted || deploymentplan.Digest(progress) != deploymentplan.Digest(journal) {
		return environmentCreateBootstrapBlocker("fresh cleanup lost its original accepted workload owners")
	}
	// Derive only the declared ACK removal from the immutable original plan;
	// no new renderer, inventory or replacement resource body is introduced.
	steady := *initial
	steady.Resources = slices.Clone(initial.Resources)
	outputs := map[string]environmentCreateProductIdentity{}
	for _, output := range l.Outputs {
		outputs[output.ID] = output
	}
	for i := range steady.Resources {
		steady.Resources[i].Desired = deploymentplan.Clone(steady.Resources[i].Desired)
		if err = resolveCreateNormalTailProductACK(steady.Resources[i].Desired, outputs, map[string]int{}, installed.Ceremony, false); err != nil {
			return err
		}
	}
	changed, err := freshProductSteadyDefinitions(initial, &steady, l, installed.Ceremony)
	if err != nil {
		return err
	}
	for i := range changed {
		uid := journal.Owners[deploymentplan.ID(changed[i].Object)]
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return environmentCreateBootstrapBlocker("fresh steady consumer lost its original accepted workload UID")
		}
		deploymentplan.Map(changed[i].Object["metadata"])["uid"] = uid
	}
	retired, err := productPreparationDefinitions(cfg, store, e, l, installed, true, true)
	if err != nil {
		return err
	}
	for _, stage := range []struct {
		name        string
		definitions []deploymentplan.Definition
	}{{"fresh-product-steady", changed}, {"fresh-product-producer-retire", retired}} {
		if apply == nil {
			p, _, readErr := acceptedCreateStage(store, e, "kubernetes/"+stage.name)
			if readErr != nil || p.BindingsSHA256 != environmentCreateStageBindingsSHA(stage.name, stage.definitions) {
				return environmentCreateBootstrapBlocker("fresh cleanup requires its original resumable stage adapter")
			}
			if _, err = readProductPreparationProgress(store, e, stage.name, p, true); err != nil {
				return err
			}
			if err = p.Verify(ctx, backend); err != nil {
				return err
			}
		} else if err = apply(ctx, stage.name, stage.definitions); err != nil {
			return err
		}
	}
	return productPreparationsRetired(ctx, cfg, store, e, l, installed, backend, true)
}

func freshProductSteadyDefinitions(initial, final *deploymentplan.Plan, l *createNormalTailProductLifecycle, s environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if initial == nil || final == nil || len(initial.Resources) != len(final.Resources) {
		return nil, environmentCreateBootstrapBlocker("fresh steady target changed its original resource catalog")
	}
	outputs := map[string]environmentCreateProductIdentity{}
	for _, p := range l.Outputs {
		outputs[p.ID] = p
	}
	counts := map[string]int{}
	byID := map[string]deploymentplan.Resource{}
	for _, r := range final.Resources {
		byID[r.ID] = r
	}
	var changed []deploymentplan.Definition
	for _, old := range initial.Resources {
		r, ok := byID[old.ID]
		if !ok {
			return nil, environmentCreateBootstrapBlocker("fresh steady target lost an original resource")
		}
		expected := deploymentplan.Clone(old.Desired)
		if err := resolveCreateNormalTailProductACK(expected, outputs, counts, s, false); err != nil {
			return nil, err
		}
		if deploymentplan.Digest(expected) != deploymentplan.Digest(r.Desired) {
			return nil, environmentCreateBootstrapBlocker("fresh steady target changed settings outside its exact transient ACK fields")
		}
		if deploymentplan.Digest(old.Desired) != deploymentplan.Digest(expected) {
			changed = append(changed, deploymentplan.Definition{Object: expected, Phase: r.Phase})
		}
	}
	for id := range outputs {
		if counts[id] != 1 {
			return nil, environmentCreateBootstrapBlocker("fresh steady target has no unique original product consumer")
		}
	}
	return changed, nil
}

func validFreshProductBinding(b *environmentFreshProductBinding) bool {
	return b == nil || environmentPreflightDigest.MatchString(b.ParentSHA256) && environmentPreflightDigest.MatchString(b.ScopeSHA256) && environmentBootstrapOwnerUUID.MatchString(b.SessionID) && environmentBootstrapOwnerUUID.MatchString(b.IssuerID) && !strings.ContainsAny(b.ParentSHA256+b.ScopeSHA256, "\r\n")
}
