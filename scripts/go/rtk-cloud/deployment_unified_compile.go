package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

var environmentRenderMutex sync.Mutex
var activeRuntimeSecretSnapshot map[string]string

type compiledEnvironmentDeployment struct {
	Plan                     *deploymentplan.Plan
	Release                  environmentRelease
	Bindings                 environmentIdentityBindings
	Store                    secretStore
	Backend                  environmentKubernetesBackend
	Configuration            deploymentConfig
	SourceCommand            environmentReleaseCommand
	RequirePreflightBindings bool
	PreflightGeneration      string
	Proof                    *environmentPreflightProof
	PKICheckedAt             time.Time
	PKIVerifiedUntil         time.Time
	ReleaseProofs            []environmentReleasePublicationProof
}

type environmentDeploymentSnapshot struct {
	Operator              map[string]string
	Secrets               map[string]string
	Kubeconfig            string
	PostgresProtection    postgresBackupDeployment
	ZammadValues          []byte
	ZammadChart           []byte
	StorageEvidenceSHA256 string
	DNSStateSHA256        string
	OTAStorage            environmentOTAStorageObservation
	MediaStorage          environmentOTAStorageObservation
}

func (s environmentDeploymentSnapshot) Digest(cfg deploymentConfig) string {
	return deploymentplan.Digest(struct {
		Values, AdapterValues, AdapterResolved, DNS map[string]string
		Capacity                                    sharedCapacityPlan
		Storage                                     deploymentStoragePlan
		Private                                     environmentDeploymentSnapshot
	}{cfg.Values, cfg.AdapterValues, cfg.AdapterResolved, cfg.DNSValues, cfg.Capacity, cfg.Storage, s})
}

// Only the bounded rendering scope installs compatibility helper inputs. It
// never invokes catalog initialization, imports live credentials or writes files.
func withEnvironmentRenderInputs(cfg deploymentConfig, store secretStore, release environmentRelease, snapshot environmentDeploymentSnapshot, render func(map[string]string) error) error {
	values := environmentConfigurationRenderValues(cfg)
	if err := projectAccountRegistrationCRLRefreshInputs(cfg.Environment, snapshot.Operator, values); err != nil {
		return err
	}
	if err := projectOTAStorageRenderInputs(cfg, snapshot.OTAStorage, values); err != nil {
		return err
	}
	if err := projectVideoStorageRenderInputs(cfg, snapshot.MediaStorage, values); err != nil {
		return err
	}
	return withEnvironmentRenderValues(store, release, snapshot, values, render)
}

// The selected public configuration is shared by full rendering and bounded
// components that do not consume allocated storage observations.
func environmentConfigurationRenderValues(cfg deploymentConfig) map[string]string {
	values := appendMap(appendMap(cfg.Values, cfg.AdapterValues), cfg.AdapterResolved)
	values = appendMap(values, deploymentLegacyLKEValues(values, cfg.Environment))
	values = appendMap(values, cfg.DNSValues)
	values["CLOUD_ENV_NAME"] = cfg.Environment
	return values
}

// Both renderers install the same frozen credentials. Only the executable
// renderer above accepts genuine allocated storage observations.
func withEnvironmentRenderValues(store secretStore, release environmentRelease, snapshot environmentDeploymentSnapshot, values map[string]string, render func(map[string]string) error) error {
	environmentRenderMutex.Lock()
	defer environmentRenderMutex.Unlock()
	if err := verifySecretStorePermissionsOnly(store); err != nil {
		return err
	}
	values = envroot.Derive(values)
	// Frontend producer and consumer must share the selected SDK intent. Fill
	// absent configuration from the canonical operator snapshot, and explicitly
	// clear unselected ambient SDK overrides for this bounded legacy render scope.
	for _, key := range environmentFrontendSDKRenderKeys() {
		if key == "SDK_ARTIFACT_ACCESS_KEY_ID" || key == "SDK_ARTIFACT_SECRET_ACCESS_KEY" {
			values[key] = snapshot.Operator[key]
		} else if _, selected := values[key]; !selected {
			values[key] = snapshot.Operator[key]
		}
	}
	// Payment-provider validation and legacy manifests consume one selected
	// snapshot. Explicit operator false values outrank TEST_LAB defaults.
	for _, key := range environmentBillingRenderKeys() {
		if _, selected := values[key]; !selected {
			values[key] = snapshot.Operator[key]
		}
	}
	for key, image := range release.Images {
		values[key] = image.Reference
	}
	installed := appendMap(snapshot.Operator, values)
	// The maintained legacy manifests consume these aliases. Their sole source
	// is the selected dedicated media credential pair, including fresh bootstrap.
	if snapshot.Operator["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "" && snapshot.Operator["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"] != "" {
		installed["LINODE_OBJ_ACCESS_KEY_ID"] = snapshot.Operator["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"]
		installed["LINODE_OBJ_SECRET_ACCESS_KEY"] = snapshot.Operator["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"]
	}
	// Clear ambient compatibility overrides; tracked settings and the selected
	// environment operator profile are the sole inputs to the common compiler.
	for _, setting := range os.Environ() {
		key, _, _ := strings.Cut(setting, "=")
		if strings.HasPrefix(key, "LKE_") {
			if _, ok := installed[key]; !ok {
				installed[key] = ""
			}
		}
	}
	restore := installAllCredentialEnvironment(installed)
	defer restore()
	oldCanonical, oldRoot, oldDir, oldCache := activeCanonicalSecretStore, activeSecretEnvironmentRoot, lkeRuntimeSecretStateDir, lkeRuntimeSecretCache
	oldSnapshot := activeRuntimeSecretSnapshot
	defer func() {
		activeCanonicalSecretStore, activeSecretEnvironmentRoot, lkeRuntimeSecretStateDir, lkeRuntimeSecretCache = oldCanonical, oldRoot, oldDir, oldCache
		activeRuntimeSecretSnapshot = oldSnapshot
	}()
	activeCanonicalSecretStore = true
	activeSecretEnvironmentRoot = store.Root
	lkeRuntimeSecretStateDir = store.RuntimeDir()
	lkeRuntimeSecretCache = map[string]string{}
	activeRuntimeSecretSnapshot = snapshot.Secrets
	return render(values)
}

func environmentRuntimeSecretSnapshot(store secretStore) (map[string]string, error) {
	entries, err := os.ReadDir(store.RuntimeDir())
	if err != nil {
		return nil, errors.New("deployment.inputs: runtime SecretStore inventory unavailable")
	}
	values := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, errors.New("deployment.inputs: runtime secrets require the maintained flat catalog layout")
		}
		value, err := readSensitiveFile(filepath.Join(store.RuntimeDir(), entry.Name()), "environment runtime secret")
		if err != nil {
			return nil, errors.New("deployment.inputs: runtime SecretStore entry unavailable")
		}
		values[entry.Name()] = value
	}
	return values, nil
}

func environmentDeploymentInputDigest(cfg deploymentConfig, store secretStore) (string, error) {
	return environmentDeploymentInputDigestContext(context.Background(), cfg, store)
}

func environmentDeploymentInputDigestContext(ctx context.Context, cfg deploymentConfig, store secretStore) (string, error) {
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		return "", err
	}
	return environmentDeploymentSnapshotDigest(ctx, cfg, snapshot), nil
}

// Create owns DNS publication later in the same original journal. Its current
// ownership file is an output, not a configuration input. The routing adapter
// checks every actual record against the immutable intent and recorded receipt.
// Other operations continue to pin their observed DNS state as before.
func environmentDeploymentSnapshotDigest(ctx context.Context, cfg deploymentConfig, snapshot environmentDeploymentSnapshot) string {
	if h, ok := createNormalTailHandoffFromContext(ctx); ok && h.Execution.Store.Environment == cfg.Environment {
		snapshot.DNSStateSHA256 = ""
	}
	return snapshot.Digest(cfg)
}

func readEnvironmentDeploymentSnapshot(cfg deploymentConfig, store secretStore) (environmentDeploymentSnapshot, error) {
	var snapshot environmentDeploymentSnapshot
	if err := verifySecretStorePermissionsOnly(store); err != nil {
		return snapshot, err
	}
	if err := verifySecretStoreContents(store); err != nil {
		return snapshot, err
	}
	operator, err := store.readOperator()
	if err != nil {
		return snapshot, err
	}
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return snapshot, err
	}
	kubeconfig, err := readSensitiveFile(store.KubeconfigPath(), "selected environment kubeconfig")
	if err != nil {
		return snapshot, err
	}
	if cfg.Values["SUPPORT_TICKETS_ENABLED"] == "true" {
		snapshot.ZammadValues, snapshot.ZammadChart, err = readEnvironmentZammadInputs(cfg)
		if err != nil {
			return snapshot, err
		}
	}
	var protection postgresBackupDeployment
	if cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] == "true" {
		protection, err = readPostgresBackupDeployment(filepath.Join(cfg.EnvironmentRoot, "postgres-backup.json"), cfg.Environment)
		if err != nil {
			return snapshot, errors.New("deployment.inputs: enabled PostgreSQL backup configuration unavailable")
		}
	} else if cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] != "false" {
		return snapshot, errors.New("deployment.inputs: DEPLOYMENT_POSTGRES_BACKUP_ENABLED must be explicitly true or false")
	}
	storageEvidence, err := deploymentStorageEvidenceDigest(cfg)
	if err != nil {
		return snapshot, err
	}
	dnsDigest, err := environmentDNSStateDigest(cfg)
	if err != nil {
		return snapshot, err
	}
	var otaStorage environmentOTAStorageObservation
	var mediaStorage environmentOTAStorageObservation
	if cfg.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true" {
		mediaStorage, err = readMediaStorageObservation(cfg, store)
		if err != nil {
			return snapshot, err
		}
	}
	if cfg.Values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" {
		otaStorage, err = readOTAStorageObservation(cfg, store)
		if err != nil {
			return snapshot, err
		}
	}
	return environmentDeploymentSnapshot{MediaStorage: mediaStorage, OTAStorage: otaStorage, ZammadValues: snapshot.ZammadValues, ZammadChart: snapshot.ZammadChart, DNSStateSHA256: dnsDigest, Operator: operator, Secrets: secrets, Kubeconfig: kubeconfig, PostgresProtection: protection, StorageEvidenceSHA256: storageEvidence}, nil
}

func compileEnvironmentDeployment(ctx context.Context, cfg deploymentConfig, operation string) (*compiledEnvironmentDeployment, error) {
	if operation == "create" {
		return nil, checkEnvironmentExecutionAvailable(ctx, cfg, operation)
	}
	if _, err := environmentPreflightValidity(cfg.Values); err != nil {
		return nil, err
	}
	ctx = newEnvironmentReleaseProofCapture(ctx)
	environmentDeploymentProgress(ctx, "deployment.release.resolve")
	run := environmentCIImageCommand(cfg)
	release, err := loadEnvironmentCIImages(ctx, cfg, run)
	environmentDeploymentProgressDone(ctx, "deployment.release.resolve", err)
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.release.resolve", "formal CI image selection could not be completed", "verify selected service revisions, CI publication and registry access, then repeat preflight")
	}
	return compileEnvironmentDeploymentWithImages(ctx, cfg, operation, release, run)
}

// Keep expected public blockers intact. Unknown provider/tool errors are not
// safe to print; report the exact compilation stage and its repair boundary.
func environmentCompilationError(err error, id, cause, next string) error {
	var blocker *deploymentplan.Blocker
	if errors.As(err, &blocker) {
		return err
	}
	return &deploymentplan.Blocker{ID: id, Cause: cause, Next: next}
}

func compileEnvironmentDeploymentWithImages(ctx context.Context, cfg deploymentConfig, operation string, release environmentRelease, sourceCommand environmentReleaseCommand) (*compiledEnvironmentDeployment, error) {
	if operation == "create" {
		return nil, checkEnvironmentExecutionAvailable(ctx, cfg, operation)
	}
	return compileEnvironmentDeploymentWithImagesTarget(ctx, cfg, operation, release, sourceCommand)
}

// Only the admitted create lifecycle calls this after real bootstrap. Public
// create planning uses its stage intent and cannot invent existing bindings.
type renderedEnvironmentTarget struct {
	Snapshot                         environmentDeploymentSnapshot
	Definitions, RuntimeDependencies []deploymentplan.Definition
	Live                             []deploymentplan.Object
	Bindings                         environmentIdentityBindings
	Backend                          environmentKubernetesBackend
	Store                            secretStore
	Release                          environmentRelease
}

// Render once through the maintained catalog. Stage-aware create qualification
// can inspect all consumer/producer contracts before native lifecycle outputs
// exist; only the common compiler below creates an executable Kubernetes plan.
// Authenticate the original completed generation against its literal release
// before selecting ordinary explicit dev overrides. The historical readers keep
// their full source/publication/receipt checks and never see a mutated release.
func effectiveEnvironmentDeploymentReleaseForInventory(ctx context.Context, cfg deploymentConfig, store secretStore, operation string, release environmentRelease, operator map[string]string, live []deploymentplan.Object, run environmentReleaseCommand) (environmentRelease, error) {
	if operation != "create" {
		var err error
		release, err = effectiveCreateOpenBaoACKReleaseForInventoryWithCommand(ctx, cfg, store, release, live, run)
		if err != nil {
			return environmentRelease{}, err
		}
	}
	selected, err := accountRegistrationCRLRefreshRelease(cfg, release, operator)
	if err != nil {
		return environmentRelease{}, environmentCompilationError(err, "deployment.configuration.account-registration-crls", "selected management image or registration CRL refresh profile is invalid", "retain the original Account Manager identity and select the exact supported dev profile and immutable image")
	}
	return selected, nil
}

func renderEnvironmentDeploymentTarget(ctx context.Context, cfg deploymentConfig, operation string, release environmentRelease) (*renderedEnvironmentTarget, error) {
	if _, err := environmentPreflightValidity(cfg.Values); err != nil {
		return nil, err
	}
	storageErr := validateDeploymentStorageActivation(cfg)
	if operation == "create" {
		storageErr = validateEnvironmentCreateStorageAllocation(cfg)
	}
	if err := storageErr; err != nil {
		return nil, &deploymentplan.Blocker{ID: "deployment.storage.activation", Cause: "completed environment storage activation evidence is unavailable or invalid", Next: "import the exact original completed receipt/proof with deployment storage-import-state, or finish the managed storage lifecycle; preflight cannot produce completion evidence"}
	}
	profile := cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"]
	if profile != "standard" && profile != "production" {
		return nil, &deploymentplan.Blocker{ID: "deployment.target", Cause: "an explicit supported protection profile is required", Next: "maintain DEPLOYMENT_PROTECTION_PROFILE=standard or production in the architecture and reviewed environment overrides"}
	}
	if cfg.Environment == "prod" && profile != "production" {
		return nil, &deploymentplan.Blocker{ID: "deployment.production", Cause: "production protection cannot be downgraded through configuration", Next: "restore the production profile and qualify its actual HA, custody, recovery and PKI capabilities"}
	}
	if cfg.Adapter != "lke" {
		return nil, &deploymentplan.Blocker{ID: "deployment.adapter", Resource: cfg.Adapter, Cause: "selected infrastructure adapter has no complete shared implementation", Next: "use an implemented maintained adapter before planning an environment update"}
	}
	rolloutTimeout, err := environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return nil, err
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return nil, err
	}
	nextStep, abortSteps := environmentDeploymentSteps(ctx)
	defer abortSteps()
	nextStep("deployment.inputs")
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			return nil, blocker
		}
		return nil, &deploymentplan.Blocker{ID: "deployment.inputs", Cause: "complete private configuration and credential snapshot cannot be verified", Next: "restore environment-local operator, runtime, kubeconfig and backup configuration with private directory/file permissions"}
	}
	if cfg.Values["OTA_SERVICE_REGISTRATION_ENABLED"] == "true" {
		nextStep("deployment.inputs.ota-storage")
		snapshot.OTAStorage, err = observeOTAStorageForRender(ctx, cfg, store, snapshot.Operator, defaultDeploymentCredentialChecker())
		if err != nil {
			return nil, err
		}
	}
	if cfg.Values["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] == "true" {
		nextStep("deployment.inputs.media-storage")
		snapshot.MediaStorage, err = observeMediaStorageForRender(ctx, cfg, store, snapshot.Operator, defaultDeploymentCredentialChecker())
		if err != nil {
			return nil, err
		}
	}
	var protection environmentPostgresProtection
	if cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] == "true" {
		protection, err = environmentPostgresProtectionFromSnapshot(snapshot)
		if err != nil {
			return nil, &deploymentplan.Blocker{ID: "deployment.configuration.postgres-backup", Cause: "enabled PostgreSQL backup configuration or credentials are incomplete", Next: "restore the existing enabled backup component's maintained configuration and dedicated credentials; do not enable backups merely to run an ordinary update"}
		}
	}
	backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, cfg.Environment)
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.access.kubeconfig", "selected Kubernetes identity could not be initialized", "restore the selected environment's compatible embedded Kubernetes credentials, then repeat preflight")
	}
	backend.RolloutTimeout = rolloutTimeout
	nextStep("deployment.inventory.read")
	live, err := backend.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.inventory.read", "selected Kubernetes workload and dependency inventory could not be read", "verify selected kubeconfig, API connectivity and inventory read permissions, then repeat preflight")
	}
	nextStep("deployment.pki.observation")
	release, err = effectiveEnvironmentDeploymentReleaseForInventory(ctx, cfg, store, operation, release, snapshot.Operator, live, environmentCIImageCommand(cfg))
	if err != nil {
		return nil, err
	}
	bindings, err := observeEnvironmentTargetBindings(ctx, cfg, release, operation, live)
	if err != nil {
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			return nil, blocker
		}
		return nil, &deploymentplan.Blocker{ID: "deployment.pki.observation", Cause: "existing environment identity references or ownership cannot be verified", Next: "verify the selected Kubernetes inventory and existing identity references; preflight generates bindings without a pre-existing local file"}
	}
	nextStep("deployment.renderer.support")
	support, err := renderEnvironmentZammad(ctx, cfg, store, release, operation, snapshot.ZammadValues, snapshot.ZammadChart)
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.renderer.support", "fixed Zammad chart produced invalid resource output", "verify the maintained chart, values and installed Helm, then repeat preflight")
	}
	if err := bindZammadLifecycle(support, live); err != nil {
		return nil, err
	}
	support, err = retireCompletedZammadInitJobs(support, live, release)
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.lifecycle.support", "Zammad initialization job lifecycle could not be verified", "verify original initialization job ownership and completion before retirement, then repeat preflight")
	}
	var definitions []deploymentplan.Definition
	var runtimeDependencies []deploymentplan.Definition
	nextStep("deployment.renderer.target")
	err = withEnvironmentRenderInputs(cfg, store, release, snapshot, func(values map[string]string) error {
		var err error
		definitions, err = renderEnvironmentResources(values, bindings, release, support)
		if err == nil {
			runtimeDependencies, err = renderEnvironmentRuntimeDependencyDefinitions(values, snapshot.Secrets, snapshot.Operator)
			definitions = append(definitions, runtimeDependencies...)
		}
		if err == nil {
			err = bindEnvironmentNewStorageClasses(definitions, values["DURABLE_DATA_STORAGE_CLASS"], bindings.Owners)
		}
		if err == nil && cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] == "true" {
			definitions, err = renderEnvironmentPostgresProtection(definitions, protection, release)
		}
		return err
	})
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.renderer.target", "shared target rendering could not complete its configuration or lifecycle references", "verify maintained component configuration, original identity references and shared renderer support, then repeat preflight")
	}
	definitions, err = bindEnvironmentInitialSchemaOwner(definitions, cfg.Environment, release.Stack, operation, release, cfg.Values["LKE_IMAGE_PULL_SECRET_NAME"], bindings.Owners)
	if err != nil {
		return nil, err
	}
	definitions, err = bindEnvironmentCreatePublicTLSTarget(ctx, store, definitions)
	if err != nil {
		return nil, err
	}
	definitions, err = bindFreshProductTargetDefinitions(ctx, cfg, store, bindings, definitions)
	if err != nil {
		return nil, err
	}
	nextStep("")
	return &renderedEnvironmentTarget{Snapshot: snapshot, Definitions: definitions, RuntimeDependencies: runtimeDependencies, Live: live, Bindings: bindings, Backend: backend, Store: store, Release: release}, nil
}

func compileEnvironmentDeploymentWithImagesTarget(ctx context.Context, cfg deploymentConfig, operation string, release environmentRelease, sourceCommand environmentReleaseCommand) (*compiledEnvironmentDeployment, error) {
	rendered, err := renderEnvironmentDeploymentTarget(ctx, cfg, operation, release)
	if err != nil {
		return nil, err
	}
	return compileRenderedEnvironmentDeploymentTarget(ctx, cfg, operation, rendered, sourceCommand)
}

// A staged create resolves only its authenticated native outputs into the same
// maintained definitions, then enters this ordinary executable-plan compiler.
func compileRenderedEnvironmentDeploymentTarget(ctx context.Context, cfg deploymentConfig, operation string, rendered *renderedEnvironmentTarget, sourceCommand environmentReleaseCommand) (*compiledEnvironmentDeployment, error) {
	var err error
	definitions, runtimeDependencies, live := rendered.Definitions, rendered.RuntimeDependencies, rendered.Live
	bindings, backend, store := rendered.Bindings, rendered.Backend, rendered.Store
	backend.SchemaWorkspace = cfg.Workspace
	release := rendered.Release
	configurationDigest := environmentDeploymentSnapshotDigest(ctx, cfg, rendered.Snapshot)
	nextStep, abortSteps := environmentDeploymentSteps(ctx)
	defer abortSteps()
	nextStep("deployment.ownership")
	// Explicit desired cluster-scoped owners may predate stack labels. Read each
	// exact ID, then require an existing label or a recorded UID-bound adoption.
	seen := map[string]bool{}
	for _, object := range live {
		seen[deploymentplan.ID(object)] = true
	}
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		if seen[id] {
			continue
		}
		current, err := backend.Read(ctx, id)
		if err != nil {
			return nil, &deploymentplan.Blocker{ID: "deployment.inventory.read", Resource: id, Cause: "planned resource's original owner could not be read", Next: "verify selected Kubernetes API access and this resource's read permissions, then repeat preflight"}
		}
		if current != nil {
			meta := deploymentplan.Map(current["metadata"])
			label := deploymentplan.String(deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"])
			if deploymentplan.String(meta["namespace"]) == "" && strings.HasPrefix(deploymentplan.String(meta["name"]), bindings.Stack+"-") && (label == "" || label == bindings.Stack) && meta["deletionTimestamp"] == nil {
				uid := deploymentplan.String(meta["uid"])
				if uid == "" {
					return nil, &deploymentplan.Blocker{ID: "deployment.ownership", Resource: id, Cause: "observed cluster resource has no owner UID", Next: "verify the original resource owner through the selected Kubernetes API and repeat preflight"}
				}
				bindings.Owners[id] = uid
			}
			live = append(live, current)
			seen[id] = true
		}
	}
	definitions, err = retireLegacyOTARegistrar(definitions, live, bindings)
	if err != nil {
		return nil, err
	}
	nextStep("deployment.compatibility.transition")
	definitions, err = bindEnvironmentRuntimeDependencyOwners(definitions, runtimeDependencies, live, bindings.Owners)
	if err != nil {
		return nil, err
	}
	if err := bindOpenBaoAllocations(definitions, live); err != nil {
		return nil, environmentCompilationError(err, "deployment.compatibility.openbao", "original OpenBao persistent allocations could not be matched to the target", "verify original OpenBao data/audit claims and immutable allocation compatibility, then repeat preflight")
	}
	if err := validateEnvironmentPKITransition(definitions, live); err != nil {
		return nil, err
	}
	if err := validateEnvironmentDurableStorageTransition(definitions, live); err != nil {
		return nil, err
	}
	// Normal updates preserve existing bootstrap credentials and their lifecycle.
	// Bootstrap retirement is a separate explicit operation, not an upgrade side effect.
	nextStep("deployment.dependencies")
	definitions, err = bindRetainedEnvironmentDependencies(definitions, live, bindings)
	if err != nil {
		return nil, err
	}
	if err := validateEnvironmentCertIssuerPublicTransport(deploymentEndpointValues(cfg), definitions); err != nil {
		return nil, err
	}
	for _, definition := range definitions {
		if err := validateEnvironmentIssuerStartup(definition.Object); err != nil {
			return nil, err
		}
	}
	fresh, err := environmentDeploymentInputDigestContext(ctx, cfg, store)
	if err != nil || fresh != configurationDigest {
		return nil, &deploymentplan.Blocker{ID: "deployment.inputs.changed", Cause: "configuration or credential inputs changed or became unavailable during rendering", Next: "restore stable maintained settings and environment credentials, then repeat full preflight before deployment"}
	}
	input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: operation, ConfigurationSHA256: configurationDigest, ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}
	nextStep("deployment.plan.compile")
	plan, err := deploymentplan.Compile(input, definitions, live, bindings.Owners)
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.plan.compile", "resource ordering or lifecycle plan compilation failed", "repair maintained resource phases, dependencies and lifecycle classifications, then repeat preflight")
	}
	if err := qualifyEnvironmentOnDeleteOpenBaoPlan(plan, definitions, live); err != nil {
		return nil, err
	}
	nextStep("deployment.storage.observation")
	storage, err := backend.storageBindings(ctx, plan)
	if err != nil {
		return nil, environmentCompilationError(err, "deployment.storage.observation", "original storage bindings could not be verified", "verify the planned PVC/PV owners and selected Kubernetes storage access, then repeat preflight")
	}
	plan.StorageSHA256 = deploymentplan.Digest(storage)
	compiled := &compiledEnvironmentDeployment{Plan: plan, Release: release, Bindings: bindings, Store: store, Backend: backend, Configuration: cfg, SourceCommand: sourceCommand, ReleaseProofs: snapshotVerifiedReleaseProofs(ctx, release)}
	nextStep("deployment.inputs.fence")
	if err := compiled.Fence(ctx, plan); err != nil {
		return nil, environmentCompilationError(err, "deployment.inputs.recheck", "compiled plan inputs or runtime ownership no longer match the observed baseline", "recheck configuration, CI selection, original owners and storage bindings, then repeat full preflight for a fresh plan")
	}
	nextStep("")
	return compiled, nil
}

// Re-resolve maintained files; hashing the original in-memory configuration
// would miss an operator editing tracked settings between rollout phases.
func (c *compiledEnvironmentDeployment) Fence(ctx context.Context, plan *deploymentplan.Plan) error {
	_, err := c.FenceInventory(ctx, plan)
	return err
}

// The observation belongs to this fence only. Execution may reuse it for its
// immediately following phase checks, but still reads each owner before writes.
// These are fixed failure categories at the current-state fence. Unknown tool
// output and credential-bearing errors stay private; typed public blockers and
// cancellation retain their existing contract.
func environmentFenceFailure(err error, id, cause string) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return environmentCompilationError(err, id, cause, "repair the reported current input or runtime observation, then resume the recorded plan with its completed-resource journal and original execution window")
}

func (c *compiledEnvironmentDeployment) FenceInventory(ctx context.Context, plan *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.Plan == nil || plan == nil || plan.SHA256() != c.Plan.SHA256() {
		return nil, environmentFenceFailure(nil, "deployment.inputs.plan", "selected execution plan changed")
	}
	fresh, err := resolveDeploymentConfig(c.Configuration.Workspace, c.Configuration.Environment, c.Configuration.EnvironmentRoot)
	if err != nil {
		return nil, environmentFenceFailure(err, "deployment.inputs.configuration-resolution", "maintained environment configuration cannot be re-resolved")
	}
	if c.Proof != nil {
		if err := validateEnvironmentExecutionProof(ctx, c, fresh, plan, time.Now().UTC()); err != nil {
			return nil, err
		}
		if err := verifyEnvironmentActivePreflightProof(c); err != nil {
			return nil, err
		}
	}
	digest, err := environmentDeploymentInputDigestContext(ctx, fresh, c.Store)
	if err != nil {
		return nil, environmentFenceFailure(err, "deployment.inputs.snapshot", "selected canonical configuration and credential snapshot could not be read")
	}
	if digest != plan.ConfigurationSHA256 {
		return nil, environmentFenceFailure(nil, "deployment.inputs.configuration", "maintained configuration or environment credentials changed")
	}
	if deploymentplan.Digest(c.Release) != plan.ReleaseSHA256 {
		return nil, environmentFenceFailure(nil, "deployment.inputs.release", "pinned CI image selection changed")
	}
	if c.SourceCommand != nil {
		if err := verifyEnvironmentImageSources(ctx, fresh, c.Release, c.SourceCommand); err != nil {
			return nil, environmentFenceFailure(err, "deployment.inputs.release-source", "selected immutable release source could not be verified")
		}
	}
	if deploymentplan.Digest(c.Bindings) != plan.BindingsSHA256 {
		return nil, environmentFenceFailure(nil, "deployment.inputs.bindings", "observed identity or ownership bindings changed")
	}
	if c.RequirePreflightBindings {
		if err := verifyEnvironmentPreflightBindings(c); err != nil {
			return nil, err
		}
	}
	live, err := c.Backend.Inventory(ctx, plan.Stack)
	if err != nil {
		return nil, environmentFenceFailure(err, "deployment.inventory", "current Kubernetes inventory could not be read or decoded")
	}
	observed := make(map[string]deploymentplan.Object, len(live))
	currentOwners := map[string]string{}
	for _, object := range live {
		observed[deploymentplan.ID(object)] = object
		currentOwners[deploymentplan.ID(object)] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	}
	if h, ok := createNormalTailHandoffFromContext(ctx); ok {
		if err := verifyCreateNormalTailPostgresAccessFromInventory(ctx, fresh, h, c.Backend, observed); err != nil {
			return nil, err
		}
	}
	if err := checkCreateNormalTailWorkloadStorage(ctx, c, plan, observed); err != nil {
		return nil, err
	}
	retired := map[string]bool{}
	for _, resource := range plan.Resources {
		if resource.Action == "retire" {
			retired[resource.ID] = true
		}
	}
	for id, uid := range c.Bindings.Owners {
		if retired[id] && currentOwners[id] == "" {
			continue
		}
		if currentOwners[id] == "" {
			object, err := c.Backend.Read(ctx, id)
			if err != nil {
				return nil, err
			}
			if object != nil {
				observed[id] = object
				currentOwners[id] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
			}
		}
		if currentOwners[id] != uid {
			return nil, &deploymentplan.Blocker{ID: "deployment.ownership.drift", Resource: id, Cause: "observed original resource owner changed or disappeared", Next: "repeat preflight against the current environment; deployment must not overwrite a replacement owner"}
		}
	}
	if err := plan.CheckWorkloadInventory(live); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return observed, nil
}

// Retained Secrets, claims and configuration references are dependencies, not
// renderer inputs. Their complete baseline is fenced by the common planner.
func bindRetainedEnvironmentDependencies(definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings) ([]deploymentplan.Definition, error) {
	return bindRetainedEnvironmentDependenciesWithOutputs(definitions, live, bindings, nil)
}

// Only stage-intent qualification supplies outputs. They are authenticated
// producer contracts, not Kubernetes objects. The executable target compiler
// uses the wrapper above and requires their genuine produced owners.
func bindRetainedEnvironmentDependenciesWithOutputs(definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings, outputs map[string]string) ([]deploymentplan.Definition, error) {
	seen := map[string]bool{}
	objects := map[string]deploymentplan.Object{}
	for _, definition := range definitions {
		seen[deploymentplan.ID(definition.Object)] = true
	}
	for _, object := range live {
		objects[deploymentplan.ID(object)] = object
	}
	var refs []environmentResourceReference
	add := func(kind, namespace, name string) {
		if name != "" {
			refs = append(refs, environmentResourceReference{kind + "/" + namespace + "/" + name, kind, namespace, name})
		}
	}
	for _, definition := range definitions {
		refs = append(refs, environmentResourceReferences(definition.Object)...)
	}
	// Claim-template PVCs and managed PKI Secrets retain an independent lifecycle
	// even when no currently rendered Pod explicitly references them.
	for _, object := range live {
		kind := deploymentplan.String(object["kind"])
		metadata := deploymentplan.Map(object["metadata"])
		if kind != "PersistentVolumeClaim" && kind != "Secret" {
			continue
		}
		if deploymentplan.String(deploymentplan.Map(metadata["labels"])["rtk.realtek.com/stack"]) == bindings.Stack || bindings.Owners[deploymentplan.ID(object)] == deploymentplan.String(metadata["uid"]) {
			add(kind, deploymentplan.String(metadata["namespace"]), deploymentplan.String(metadata["name"]))
		}
	}
	missing := map[string]bool{}
	for _, definition := range definitions {
		for _, id := range definition.Dependencies {
			if !seen[id] && objects[id] == nil && outputs[id] == "" {
				missing[id] = true
			}
		}
	}
	for _, ref := range refs {
		if !seen[ref.ID] && objects[ref.ID] == nil && outputs[ref.ID] == "" {
			missing[ref.ID] = true
		}
	}
	if len(missing) != 0 {
		ids := make([]string, 0, len(missing))
		for id := range missing {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return nil, &deploymentplan.Blocker{ID: "deployment.dependency.missing", Resource: strings.Join(ids, ","), Cause: "required lifecycle dependencies are absent", Next: "complete the recorded managed bootstrap or matched restore for every listed dependency, then repeat full preflight"}
	}
	for _, ref := range refs {
		if seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		current := objects[ref.ID]
		if current == nil && outputs[ref.ID] != "" {
			continue
		}
		if current == nil {
			return nil, &deploymentplan.Blocker{ID: "deployment.dependency.missing", Resource: ref.ID, Cause: "required lifecycle dependency is absent", Next: "complete the recorded managed bootstrap or matched restore for this exact dependency, then repeat full preflight"}
		}
		metadata := map[string]any{"name": ref.Name, "namespace": ref.Namespace}
		// Retained assets do not require label mutations merely to pass planning.
		labels := deploymentplan.Map(deploymentplan.Map(current["metadata"])["labels"])
		if deploymentplan.String(labels["rtk.realtek.com/stack"]) != bindings.Stack && bindings.Owners[ref.ID] != deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) {
			return nil, &deploymentplan.Blocker{ID: "deployment.ownership.dependency", Resource: ref.ID, Cause: "required lifecycle dependency lacks exact recorded ownership", Next: "verify the exact environment owner and repeat preflight to observe its UID; do not hand-edit generated bindings"}
		}
		object := deploymentplan.Object{"apiVersion": current["apiVersion"], "kind": ref.Kind, "metadata": metadata}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: environmentResourcePhase(ref.Kind), Retain: true})
	}
	for i := range definitions {
		dependencies := map[string]bool{}
		for _, id := range definitions[i].Dependencies {
			dependencies[id] = true
		}
		namespace := deploymentplan.String(deploymentplan.Map(definitions[i].Object["metadata"])["namespace"])
		if namespace != "" {
			dependencies["Namespace//"+namespace] = true
		}
		for _, ref := range environmentResourceReferences(definitions[i].Object) {
			dependencies[ref.ID] = true
		}
		delete(dependencies, deploymentplan.ID(definitions[i].Object))
		definitions[i].Dependencies = nil
		for id := range dependencies {
			definitions[i].Dependencies = append(definitions[i].Dependencies, id)
		}
		sort.Strings(definitions[i].Dependencies)
	}
	return definitions, nil
}
