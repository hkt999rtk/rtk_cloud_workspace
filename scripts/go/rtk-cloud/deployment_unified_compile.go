package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
}

type environmentDeploymentSnapshot struct {
	Operator              map[string]string
	Secrets               map[string]string
	Kubeconfig            string
	PostgresProtection    postgresBackupDeployment
	StorageEvidenceSHA256 string
	ProviderState         map[string]string
	ProviderAccount       map[string]string
	DNSStateSHA256        string
}

func (s environmentDeploymentSnapshot) Digest(cfg deploymentConfig) string {
	return deploymentplan.Digest(struct {
		Values, Adapter, DNS map[string]string
		Capacity             sharedCapacityPlan
		Storage              deploymentStoragePlan
		Private              environmentDeploymentSnapshot
	}{cfg.Values, cfg.AdapterResolved, cfg.DNSValues, cfg.Capacity, cfg.Storage, s})
}

// Only the bounded rendering scope installs compatibility helper inputs. It
// never invokes catalog initialization, imports live credentials or writes files.
func withEnvironmentRenderInputs(cfg deploymentConfig, store secretStore, release environmentRelease, snapshot environmentDeploymentSnapshot, render func(map[string]string) error) error {
	environmentRenderMutex.Lock()
	defer environmentRenderMutex.Unlock()
	if err := verifySecretStorePermissionsOnly(store); err != nil {
		return err
	}
	values := appendMap(cfg.Values, cfg.AdapterResolved)
	values = appendMap(values, deploymentLegacyLKEValues(values, cfg.Environment))
	values = appendMap(values, cfg.DNSValues)
	values["CLOUD_ENV_NAME"] = cfg.Environment
	values = envroot.Derive(values)
	for key, image := range release.Images {
		values[key] = image.Reference
	}
	installed := appendMap(snapshot.Operator, values)
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
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		return "", err
	}
	return snapshot.Digest(cfg), nil
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
	providerState, err := readLKEProviderState(cfg.Environment, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return snapshot, err
	}
	account, err := readLKEAccountState(cfg.Environment, false)
	if err != nil {
		return snapshot, lkeProviderStateBlocker("private provider account profile cannot be verified")
	}
	dnsDigest, err := environmentDNSStateDigest(cfg)
	if err != nil {
		return snapshot, err
	}
	return environmentDeploymentSnapshot{DNSStateSHA256: dnsDigest, Operator: operator, Secrets: secrets, Kubeconfig: kubeconfig, PostgresProtection: protection, StorageEvidenceSHA256: storageEvidence, ProviderState: providerState, ProviderAccount: account}, nil
}

func compileEnvironmentDeployment(ctx context.Context, cfg deploymentConfig, operation string) (*compiledEnvironmentDeployment, error) {
	run := environmentCIImageCommand(cfg)
	release, err := loadEnvironmentCIImages(ctx, cfg, run)
	if err != nil {
		return nil, err
	}
	return compileEnvironmentDeploymentWithImages(ctx, cfg, operation, release, run)
}

func compileEnvironmentDeploymentWithImages(ctx context.Context, cfg deploymentConfig, operation string, release environmentRelease, sourceCommand environmentReleaseCommand) (*compiledEnvironmentDeployment, error) {
	if err := validateDeploymentStorageActivation(cfg); err != nil {
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
		return nil, errors.New("deployment.adapter: selected infrastructure adapter has no complete shared implementation")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return nil, err
	}
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			return nil, blocker
		}
		return nil, &deploymentplan.Blocker{ID: "deployment.inputs", Cause: "complete private configuration and credential snapshot cannot be verified", Next: "restore environment-local operator, runtime, kubeconfig and backup configuration with private directory/file permissions"}
	}
	if operation == "upgrade" && (snapshot.ProviderState["LKE_CLUSTER_ID"] == "" || snapshot.ProviderAccount["LKE_ACTIVE_SERVICE_LIMIT"] == "") {
		return nil, lkeProviderStateBlocker("complete upgrade requires the original cluster owner and confirmed account quota in the selected environment store")
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
		return nil, err
	}
	live, err := backend.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return nil, err
	}
	admissionID := "ValidatingWebhookConfiguration//" + cfg.Values["CLOUD_STACK_NAME"] + "-ingress-admission"
	admissionSeen := false
	for _, object := range live {
		if deploymentplan.ID(object) == admissionID {
			admissionSeen = true
		}
	}
	if !admissionSeen {
		admission, err := backend.Read(ctx, admissionID)
		if err != nil {
			return nil, err
		}
		if admission != nil {
			live = append(live, admission)
		}
	}
	bindings, err := observeEnvironmentBindings(cfg.Environment, release, live)
	if err != nil {
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) {
			return nil, blocker
		}
		return nil, &deploymentplan.Blocker{ID: "deployment.pki.observation", Cause: "existing environment identity references or ownership cannot be verified", Next: "verify the selected Kubernetes inventory and existing identity references; preflight generates bindings without a pre-existing local file"}
	}
	configurationDigest := snapshot.Digest(cfg)
	var definitions []deploymentplan.Definition
	err = withEnvironmentRenderInputs(cfg, store, release, snapshot, func(values map[string]string) error {
		var err error
		definitions, err = renderEnvironmentResources(values, bindings, release)
		if err == nil && cfg.Values["DEPLOYMENT_POSTGRES_BACKUP_ENABLED"] == "true" {
			definitions, err = renderEnvironmentPostgresProtection(definitions, protection, release)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
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
			return nil, err
		}
		if current != nil {
			meta := deploymentplan.Map(current["metadata"])
			label := deploymentplan.String(deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"])
			if deploymentplan.String(meta["namespace"]) == "" && strings.HasPrefix(deploymentplan.String(meta["name"]), bindings.Stack+"-") && (label == "" || label == bindings.Stack) && meta["deletionTimestamp"] == nil {
				uid := deploymentplan.String(meta["uid"])
				if uid == "" {
					return nil, errors.New("observed cluster resource has no owner UID")
				}
				bindings.Owners[id] = uid
			}
			live = append(live, current)
			seen[id] = true
		}
	}
	if err := validateEnvironmentPKITransition(definitions, live); err != nil {
		return nil, err
	}
	if err := validateEnvironmentDurableStorageTransition(definitions, live); err != nil {
		return nil, err
	}
	// Normal updates preserve existing bootstrap credentials and their lifecycle.
	// Bootstrap retirement is a separate explicit operation, not an upgrade side effect.
	definitions, err = bindRetainedEnvironmentDependencies(definitions, live, bindings)
	if err != nil {
		return nil, err
	}
	fresh, err := environmentDeploymentInputDigest(cfg, store)
	if err != nil || fresh != configurationDigest {
		return nil, errors.New("deployment.inputs: configuration credentials changed during rendering; repeat planning")
	}
	input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: operation, ConfigurationSHA256: configurationDigest, ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}
	plan, err := deploymentplan.Compile(input, definitions, live, bindings.Owners)
	if err != nil {
		return nil, err
	}
	storage, err := backend.storageBindings(ctx, plan)
	if err != nil {
		return nil, err
	}
	plan.StorageSHA256 = deploymentplan.Digest(storage)
	compiled := &compiledEnvironmentDeployment{Plan: plan, Release: release, Bindings: bindings, Store: store, Backend: backend, Configuration: cfg, SourceCommand: sourceCommand}
	if err := compiled.Fence(ctx, plan); err != nil {
		return nil, err
	}
	return compiled, nil
}

// Re-resolve maintained files; hashing the original in-memory configuration
// would miss an operator editing tracked settings between rollout phases.
func (c *compiledEnvironmentDeployment) Fence(ctx context.Context, plan *deploymentplan.Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.Plan == nil || plan == nil || plan.SHA256() != c.Plan.SHA256() {
		return errors.New("deployment.inputs: selected execution plan changed")
	}
	if err := c.Backend.CheckStorageBindings(ctx, plan); err != nil {
		return err
	}
	fresh, err := resolveDeploymentConfig(c.Configuration.Workspace, c.Configuration.Environment, c.Configuration.EnvironmentRoot)
	if err != nil {
		return errors.New("deployment.inputs: maintained environment configuration cannot be re-resolved")
	}
	digest, err := environmentDeploymentInputDigest(fresh, c.Store)
	if err != nil || digest != plan.ConfigurationSHA256 {
		return errors.New("deployment.inputs: maintained configuration or environment credentials changed")
	}
	if deploymentplan.Digest(c.Release) != plan.ReleaseSHA256 {
		return errors.New("deployment.inputs: pinned CI image selection changed")
	}
	if c.SourceCommand != nil {
		if err := verifyEnvironmentImageSources(ctx, fresh, c.Release, c.SourceCommand); err != nil {
			return err
		}
	}
	if deploymentplan.Digest(c.Bindings) != plan.BindingsSHA256 {
		return errors.New("deployment.inputs: observed identity or ownership bindings changed")
	}
	if c.RequirePreflightBindings {
		if err := verifyEnvironmentPreflightBindings(c); err != nil {
			return err
		}
	}
	live, err := c.Backend.Inventory(ctx, plan.Stack)
	if err != nil {
		return errors.New("deployment.inventory: complete runtime owners cannot be re-enumerated")
	}
	currentOwners := map[string]string{}
	for _, object := range live {
		currentOwners[deploymentplan.ID(object)] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
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
				return err
			}
			if object != nil {
				currentOwners[id] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
			}
		}
		if currentOwners[id] != uid {
			return &deploymentplan.Blocker{ID: "deployment.ownership.drift", Resource: id, Cause: "observed original resource owner changed or disappeared", Next: "repeat preflight against the current environment; deployment must not overwrite a replacement owner"}
		}
	}
	if err := plan.CheckWorkloadInventory(live); err != nil {
		return err
	}
	return nil
}

// Retained Secrets, claims and configuration references are dependencies, not
// renderer inputs. Their complete baseline is fenced by the common planner.
func bindRetainedEnvironmentDependencies(definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings) ([]deploymentplan.Definition, error) {
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
	for _, ref := range refs {
		if seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		current := objects[ref.ID]
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
