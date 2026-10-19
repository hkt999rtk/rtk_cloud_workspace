package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A public record can precede its final parent receipt. Re-observe the exact
// native result and original custody owner, then close that same intent only;
// this path never calls the native finalizer again.
func reconcileProductFinalization(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, native environmentCreateBootstrapFinalization) error {
	s := installed.Ceremony
	e := runtime.Execution
	entry := createOpenBaoACKCompletionEntry(e.Journal, environmentCreateServiceFinalizationKey(runtime))
	intent := deploymentplan.Object{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID, "cleanup_binding": s.CleanupBinding}
	if entry == nil || entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(intent) || entry.State != "pending" && entry.State != "allocated" || !entry.CompletedAt.IsZero() || entry.StartedAt.IsZero() || native.FinalizedAt.Before(entry.StartedAt) || entry.State == "pending" && len(entry.Receipt) != 0 {
		return environmentCreateBootstrapBlocker("product finalization cannot reconcile a different original intent")
	}
	query := "SELECT json_build_object('environment',environment,'session_id',session_id,'bootstrap_leaf_sha256',bootstrap_fingerprint,'crl_sha256',finalized_crl_sha256,'cleanup_sha256',cleanup_sha256,'finalized_at',finalized_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "' AND finalized_at IS NOT NULL AND status IN ('sealed','expired')"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var actual environmentCreateBootstrapFinalization
	if err != nil || decodeEnvironmentJSON(raw, &actual) != nil || !managedLateFinalizationEqual(native, actual) {
		return environmentCreateBootstrapBlocker("product finalization original native result is unavailable")
	}
	stage := "kubernetes/service-custody-reader"
	if runtime.ProductIdentityScope != "" {
		stage = "kubernetes/product-identities-" + productIdentityScope(l)[:12] + "-service-custody-reader"
	}
	p, j, err := acceptedCreateStage(e.Store, e, stage)
	if runtime.ProductIdentityScope != "" {
		if renewed, er := readProductSessionRenewal(e.Store, e, l); er != nil {
			return er
		} else if renewed != nil {
			p, j, err = acceptedCreateStage(e.Store, e, "kubernetes/"+productSessionRenewalStage(l, renewed.Source, "custody"))
		}
	}
	if err != nil || len(p.Resources) != 1 {
		return environmentCreateBootstrapBlocker("product finalization has no original accepted custody owner")
	}
	resource := p.Resources[0]
	owner, err := runtime.Backend.Read(ctx, resource.ID)
	if err != nil || owner == nil || deploymentplan.Map(owner["metadata"])["uid"] != j.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, owner) {
		return environmentCreateBootstrapBlocker("original product custody owner changed before reconciliation")
	}
	marker, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.finalized.json")
	var tombstone managedLateCustodyTombstone
	if err != nil || decodeEnvironmentJSON(marker, &tombstone) != nil || tombstone.Schema != "rtk-cloud.bootstrap-credentials/v1" || !managedLateFinalizationEqual(tombstone.Finalization, native) || tombstone.Root != l.Root.RootFingerprint || len(tombstone.Files) != 3 {
		return environmentCreateBootstrapBlocker("original native product custody tombstone changed")
	}
	var saved struct {
		Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
		Tombstone json.RawMessage                        `json:"custody_tombstone"`
		UIDs      map[string]string                      `json:"credential_uids"`
	}
	if createOpenBaoACKCompletionRead(e.Store, filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "finalized.json"), &saved) != nil {
		return environmentCreateBootstrapBlocker("original product finalization record disappeared")
	}
	var compact bytes.Buffer
	if json.Compact(&compact, marker) != nil || !bytes.Equal(compact.Bytes(), saved.Tombstone) || deploymentplan.Digest(saved.UIDs) != deploymentplan.Digest(s.CredentialUIDs) {
		return environmentCreateBootstrapBlocker("original native product tombstone bytes differ from the saved record")
	}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		copy, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+s.BootstrapCredentialSecret)
		if err != nil || copy != nil {
			return environmentCreateBootstrapBlocker("product credential absence cannot be re-observed")
		}
	}
	if _, err = runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", "test ! -e /run/pki-bootstrap/bootstrap.key && test ! -e /run/pki-bootstrap/bootstrap.csr && test ! -e /run/pki-bootstrap/bootstrap.crt"); err != nil {
		return environmentCreateBootstrapBlocker("product custody cleanup is not complete")
	}
	return recordEnvironmentCreateServiceFinalization(ctx, cfg, e.Store, runtime, s, native, marker)
}

func productSteadyDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession) ([]deploymentplan.Definition, error) {
	initial, err := resolveCreateNormalTailProductDefinitions(cfg, l, installed)
	if err != nil {
		return nil, err
	}
	final, err := resolveCreateNormalTailProductFinalDefinitions(cfg, l, installed)
	if err != nil {
		return nil, err
	}
	index := map[string]deploymentplan.Object{}
	for _, d := range initial {
		index[deploymentplan.ID(d.Object)] = d.Object
	}
	var changed []deploymentplan.Definition
	for _, d := range final {
		if deploymentplan.Digest(index[deploymentplan.ID(d.Object)]) != deploymentplan.Digest(d.Object) {
			// The original complete workload has already accepted every prerequisite.
			// This stage changes only the exact transient enrollment settings.
			d.Dependencies = nil
			d.Retain = false
			changed = append(changed, d)
		}
	}
	if len(changed) != len(l.Outputs) {
		return nil, environmentCreateBootstrapBlocker("product steady transition changed an undeclared workload or issuer setting")
	}
	return changed, nil
}

func verifyProductFinalization(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession) (environmentCreateBootstrapFinalization, error) {
	var zero environmentCreateBootstrapFinalization
	var saved struct {
		Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
		Tombstone json.RawMessage                        `json:"custody_tombstone"`
		UIDs      map[string]string                      `json:"credential_uids"`
	}
	path := filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "finalized.json")
	if createOpenBaoACKCompletionRead(runtime.Execution.Store, path, &saved) != nil {
		return zero, environmentCreateBootstrapBlocker("product finalization lacks its original private durable record")
	}
	s := installed.Ceremony
	entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, environmentCreateServiceFinalizationKey(runtime))
	intent := deploymentplan.Object{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID, "cleanup_binding": s.CleanupBinding}
	var receipt struct {
		Native       environmentCreateBootstrapFinalization `json:"native_finalization"`
		TombstoneSHA string                                 `json:"custody_tombstone_sha256"`
		Absent       bool                                   `json:"copies_absent"`
	}
	var tombstone managedLateCustodyTombstone
	if entry == nil || entry.State != "completed" || entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(intent) || entry.CompletedAt.Before(entry.StartedAt) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || decodeEnvironmentJSON(saved.Tombstone, &tombstone) != nil || !receipt.Absent || !managedLateFinalizationEqual(receipt.Native, saved.Native) || !managedLateFinalizationEqual(tombstone.Finalization, saved.Native) || tombstone.Schema != "rtk-cloud.bootstrap-credentials/v1" || tombstone.Root != l.Root.RootFingerprint || len(tombstone.Files) != 3 || deploymentplan.Digest(saved.UIDs) != deploymentplan.Digest(s.CredentialUIDs) || saved.Native.Environment != cfg.Environment || saved.Native.SessionID != s.BootstrapSessionID || saved.Native.FinalizedAt.IsZero() || saved.Native.FinalizedAt.After(entry.CompletedAt) {
		return zero, environmentCreateBootstrapBlocker("product finalization receipt, original session or custody tombstone differs")
	}
	for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
		if !environmentPreflightDigest.MatchString(tombstone.Files[name]) {
			return zero, environmentCreateBootstrapBlocker("product finalization lost an original custody hash")
		}
	}
	raw, err := json.Marshal(tombstone)
	if err != nil || environmentPreflightBytesSHA256(raw) != receipt.TombstoneSHA && environmentPreflightBytesSHA256(append(raw, '\n')) != receipt.TombstoneSHA {
		return zero, environmentCreateBootstrapBlocker("product finalization original tombstone bytes changed")
	}
	query := "SELECT json_build_object('environment',environment,'session_id',session_id,'bootstrap_leaf_sha256',bootstrap_fingerprint,'crl_sha256',finalized_crl_sha256,'cleanup_sha256',cleanup_sha256,'finalized_at',finalized_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "' AND finalized_at IS NOT NULL AND status IN ('sealed','expired')"
	nativeRaw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var native environmentCreateBootstrapFinalization
	if err != nil || decodeEnvironmentJSON(nativeRaw, &native) != nil || !managedLateFinalizationEqual(native, saved.Native) {
		return zero, environmentCreateBootstrapBlocker("product session has no exact genuine native finalization")
	}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		actual, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+s.BootstrapCredentialSecret)
		if err != nil || actual != nil {
			return zero, environmentCreateBootstrapBlocker("finalized product bootstrap credential copy is not absent")
		}
	}
	return saved.Native, runtime.Execution.Check(ctx)
}

func finishProductSteady(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession) error {
	runtime, _, err := productIdentityRuntime(ctx, cfg, h, c, l)
	if err != nil {
		return err
	}
	if _, err = verifyProductFinalization(ctx, cfg, runtime, l, installed); err != nil {
		return err
	}
	return applyProductSteadyCleanup(ctx, cfg, c.Store, h.Execution, l, installed, productLifecycleApply(ctx, cfg, h, c, l))
}

func applyProductSteadyCleanup(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, apply environmentCreateBootstrapApply) error {
	defs, err := productSteadyDefinitions(cfg, l, installed)
	if err != nil {
		return err
	}
	if err := apply(ctx, "steady", defs); err != nil {
		return err
	}
	retire, err := productPreparationDefinitions(cfg, store, e, l, installed, true)
	if err != nil {
		return err
	}
	return apply(ctx, "producer-retire", retire)
}

func createNormalTailProductsSteady(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle) (bool, error) {
	if l == nil {
		return false, nil
	}
	key := "kubernetes/product-identities-" + productIdentityScope(l)[:12] + "-steady"
	if createOpenBaoACKCompletionEntry(h.Execution.Journal, key) == nil {
		return false, nil
	}
	runtime, _, err := productIdentityRuntime(ctx, cfg, h, c, l)
	if err != nil {
		return false, err
	}
	installed, err := readEffectiveProductInstalled(c.Store, h.Execution, l)
	if err != nil {
		return false, err
	}
	if _, err = verifyProductFinalization(ctx, cfg, runtime, l, installed); err != nil {
		return false, err
	}
	p, j, err := acceptedCreateStageForProduct(c.Store, h.Execution, l, "steady")
	if err != nil {
		return false, err
	}
	if err := validateProductSteadyPlanOwners(c.Store, h.Execution, p); err != nil {
		return false, err
	}
	defs, err := productSteadyDefinitions(cfg, l, installed)
	if err != nil || p.BindingsSHA256 != environmentCreateStageBindingsSHA("product-identities-"+productIdentityScope(l)[:12]+"-steady", defs) {
		return false, environmentCreateBootstrapBlocker("product steady cleanup differs from its declared final transition")
	}
	for _, resource := range p.Resources {
		actual, err := c.Backend.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			return false, environmentCreateBootstrapBlocker("accepted product steady owner or exact runtime settings changed")
		}
	}
	if err := productPreparationsRetired(ctx, cfg, c.Store, h.Execution, l, installed, c.Backend); err != nil {
		return false, err
	}
	return true, h.Execution.Check(ctx)
}
