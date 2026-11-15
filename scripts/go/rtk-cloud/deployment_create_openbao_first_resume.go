package main

import (
	"context"
	"io"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// resumeOpenBaoFirstInitialized continues using only the original initialized
// OpenBao and completed material receipts. The caller must hold the original
// owner. Loading the exact active proof rechecks provider, storage, namespace
// and bootstrap evidence under that owner immediately before any write.
func resumeOpenBaoFirstInitialized(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, proofSHA string, out io.Writer) (environmentCreateBootstrapRuntime, func(), error) {
	var runtime environmentCreateBootstrapRuntime
	stop := func() {}
	bad := func() (environmentCreateBootstrapRuntime, func(), error) {
		return runtime, stop, createProviderRecoveryBlock("original completed OpenBao-first material or recovery qualification is unavailable or changed")
	}
	if e == nil || out == nil || cfg.Environment != "dev" {
		return bad()
	}
	proof, err := loadOpenBaoFirstCheckpointProof(ctx, cfg, store, e, b, operator, proofSHA)
	if err != nil || proof.Qualification.OpenBao != "initialized" || proof.Qualification.Management != "completed" || proof.Qualification.Signer != "completed" {
		return bad()
	}
	plan, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil || !plan.OpenBaoFirst {
		return bad()
	}
	managementFiles, missing, state, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, "management-transport-material", b)
	if err != nil || state != "completed" || len(missing) != 0 {
		return bad()
	}
	var management environmentCreateTransport
	if decodeEnvironmentJSON(managementFiles["identities.json"], &management) != nil {
		return bad()
	}
	signerFiles, missing, state, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, "account-signer-material", b)
	if err != nil || state != "completed" || len(missing) != 0 {
		return bad()
	}
	signer := map[string]string{}
	for _, name := range []string{"access.key", "access.pub", "refresh.key", "refresh.pub"} {
		signer[name] = string(signerFiles[name])
	}
	transport, exists, caMissing, err := readOpenBaoFirstRetainedTransport(ctx, cfg, store, e, b)
	if err != nil || !exists || caMissing {
		return bad()
	}
	bao, err := readOpenBaoFirstInitialized(ctx, cfg, store, e, b)
	if err != nil {
		return runtime, stop, err
	}
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		return bad()
	}
	definitions, err := renderEnvironmentCreateFoundationServices(cfg, &plan, store, operator, raw, management, transport, signer)
	if err != nil {
		return runtime, stop, err
	}
	runtime = environmentCreateBootstrapRuntime{ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: b, OpenBao: bao}
	apply := func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		return applyEnvironmentCreateStage(ctx, cfg, store, &plan, e, stage, definitions, out)
	}
	if err := apply(withEnvironmentCreateBootstrapRuntime(ctx, runtime), "foundation-services", definitions); err != nil {
		return runtime, stop, err
	}
	return continueEnvironmentCreateAfterOpenBao(ctx, cfg, &plan, store, operator, e, apply, runtime, management)
}
