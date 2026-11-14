package main

import (
	"context"
	"path/filepath"
	"slices"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A completed PKI lifecycle is observed through its native finalization,
// permanent outputs and original retirement receipts. Deleted enrollment
// credentials/readers are historical owners, not live prerequisites.
func observeFinalizedFreshBootstrap(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (*environmentProductIdentitySession, error) {
	if e == nil || createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		return nil, nil
	}
	for _, key := range []string{"service-session-finalize", "kubernetes/fresh-product-steady", "kubernetes/fresh-product-producer-retire"} {
		if entry := createOpenBaoACKCompletionEntry(e.Journal, key); entry == nil || entry.State != "completed" {
			return nil, nil
		}
	}
	if e.Store != store || b.Store != store || cfg.Environment != store.Environment || cfg.Values["CLOUD_STACK_NAME"] != e.Journal.Stack || e.Check(ctx) != nil {
		return nil, environmentCreateBootstrapBlocker("finalized fresh observation lost its original selected environment and execution owner")
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return nil, err
	}
	l := &program.Lifecycle
	installed, err := readEffectiveFreshProductInstalled(store, e, l)
	if err != nil {
		return nil, err
	}
	runtime, err := freshProductSuccessorRuntime(ctx, cfg, store, e, b, program, installed.Ceremony)
	if err != nil {
		return nil, err
	}
	finalized, err := verifyProductFinalization(ctx, cfg, runtime, l, installed)
	if err != nil {
		return nil, err
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, installed.Ceremony.BootstrapSessionID)
	if err != nil || state.Session != installed.Ceremony.BootstrapSessionID || state.Issuer != installed.Ceremony.Intermediate.ID || state.Root != installed.Ceremony.Root.RootFingerprint || state.Fingerprint != finalized.LeafSHA256 || state.OtherActive != 0 || !slices.Equal(state.Subjects, environmentCreateServiceEnrollmentSubjects(l.Policy)) {
		return nil, environmentCreateBootstrapBlocker("finalized fresh session differs from its original permanent installation roster")
	}
	if err = verifyEnvironmentCreateFinalizerInstallations(ctx, cfg, runtime, installed.Ceremony); err != nil {
		return nil, err
	}
	source, err := readProductInstalled(store, l)
	if err != nil {
		return nil, err
	}
	if err = verifyFreshProductOriginalOutputs(ctx, cfg, runtime, l, source); err != nil {
		return nil, err
	}
	var bindings environmentIdentityBindings
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/bindings.json"), &bindings) != nil || validateEnvironmentIdentityBindings(bindings, cfg.Environment) != nil || bindings.ProductIdentities == nil || bindings.ProductIdentities.ScopeSHA256 != productIdentityScope(l) {
		return nil, environmentCreateBootstrapBlocker("finalized fresh PKI lacks its original ownership bindings")
	}
	owners := map[string]deploymentplan.Resource{}
	for id, uid := range bindings.Owners {
		owners[id] = deploymentplan.Resource{ID: id, UID: uid}
	}
	retired, err := completedBootstrapCopyRetirementsAfter(ctx, e, "kubernetes/service-session", owners)
	if err != nil {
		return nil, err
	}
	definitions, err := freshProductPreparationDefinitions(ctx, cfg, store, e, l, source, b, true)
	if err != nil {
		return nil, err
	}
	if err = productPreparationsRetired(ctx, cfg, store, e, l, installed, b, true); err != nil {
		return nil, err
	}
	for _, definition := range definitions {
		retired[deploymentplan.ID(definition.Object)] = deploymentplan.String(deploymentplan.Map(definition.Object["metadata"])["uid"])
	}
	for ns, uid := range installed.Ceremony.CredentialUIDs {
		retired["Secret/"+ns+"/"+installed.Ceremony.BootstrapCredentialSecret] = uid
	}
	live, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return nil, err
	}
	current := map[string]deploymentplan.Object{}
	for _, object := range live {
		current[deploymentplan.ID(object)] = object
	}
	for id, uid := range bindings.Owners {
		actual := current[id]
		if retired[id] == uid && actual == nil {
			continue
		}
		if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid {
			return nil, environmentCreateBootstrapBlocker("finalized fresh PKI original permanent or retired owner changed: " + id)
		}
	}
	return installed, e.Check(ctx)
}
