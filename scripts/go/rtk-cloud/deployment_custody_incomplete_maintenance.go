package main

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// An unfinished create already has an admitted, natively initialized custody
// scope. Read that scope under its original execution fence, without publishing
// the completed-deployment bindings that only final acceptance may produce.
func readInitializedCreateCustodyBinding(cfg deploymentConfig, store secretStore, generation string, held ...*environmentCreateExecution) (*environmentCustodyBinding, error) {
	bad := func() (*environmentCustodyBinding, error) {
		return nil, environmentCreateBootstrapBlocker("CRL maintenance requires the original accepted custody initialization and active create owner")
	}
	var e *environmentCreateExecution
	var original *environmentCreatePlan
	var err error
	if len(held) == 1 && held[0] != nil {
		e = held[0]
		if e.Store != store || e.Journal.PlanSHA256 != generation || e.Check(context.Background()) != nil {
			return bad()
		}
		original, err = authenticatedCreateOperatorPlan(store, e)
	} else {
		var artifact environmentCreateProviderRecoveryArtifact
		e, artifact, err = loadCreateRecoveryExecutionOwner(cfg, store, generation)
		if err != nil {
			return bad()
		}
		defer e.Close()
		original = &artifact.Plan.Plan
	}
	if err != nil || original == nil || deploymentplan.Digest(cfg) != original.ConfigurationSHA256 {
		return bad()
	}
	binding, err := createEnvironmentCustodyBinding(cfg, original.Bootstrap, generation, "")
	if err != nil || binding == nil {
		return bad()
	}
	scope := deploymentplan.Object{"schema": "rtk.custody.bootstrap/v1", "environment": cfg.Environment, "generation": generation, "mount": cfg.Values["PKI_STATE_KV_MOUNT"], "cas_required": true, "owners": binding.Owners}
	body := mustProviderRecoveryJSON(scope)
	scopeSHA, err := environmentCreatePlanSHA256(body)
	if err != nil {
		return bad()
	}
	path, err := store.safePath(filepath.Join("deployment/create", generation, "custody/owners.json"))
	if err != nil {
		return bad()
	}
	raw, err := readBoundedEnvironmentInputLimit(path, true, false, 256<<10)
	if err != nil {
		return bad()
	}
	recordedSHA, err := environmentCreatePlanSHA256(raw)
	entry := createOpenBaoACKCompletionEntry(e.Journal, "workload-custody-initialize")
	if err != nil || recordedSHA != scopeSHA || entry == nil || entry.Phase != "bootstrap" || entry.State != "completed" || entry.IntentSHA256 != scopeSHA || entry.StartedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || deploymentplan.Digest(jsonRawObject(entry.Receipt)) != deploymentplan.Digest(map[string]string{"scope_sha256": scopeSHA}) {
		return bad()
	}
	// The original activation responses are actual registry readbacks. They
	// supply authority pins, independently of the current consumer's settings.
	roots := map[string]environmentCreateRegisteredIssuer{}
	for _, entry := range e.Journal.Entries {
		if !strings.HasPrefix(entry.Key, "pki-activate-") || entry.Phase != "bootstrap" || entry.State != "completed" || entry.CompletedAt.Before(entry.StartedAt) {
			continue
		}
		doc := jsonRawObject(entry.Receipt)
		domain := deploymentplan.String(doc["trust_domain"])
		if doc["kind"] != "root" || domain != "service" && domain != "openbao_tls" {
			continue
		}
		root, err := environmentCreateRegisteredIssuerFromMap(doc, "")
		cert, certErr := parseFirstCertificatePEM([]byte(root.Certificate))
		now := time.Now().UTC()
		if err != nil || certErr != nil || root.Environment != cfg.Environment || root.Status != "active" || roots[domain].ID != "" || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) || environmentPreflightBytesSHA256(cert.Raw) != root.RootFingerprint {
			return bad()
		}
		roots[domain] = root
	}
	snapshot, _, err := readEnvironmentCustodyTransportState(cfg, store, generation)
	if err != nil || roots["service"].ID == "" || roots["openbao_tls"].ID != snapshot.ManagedRootID || roots["openbao_tls"].RootFingerprint != snapshot.ManagedRootSHA256 {
		return bad()
	}
	binding.ServiceRootSHA256 = roots["service"].RootFingerprint
	binding.OpenBaoRootSHA256 = roots["openbao_tls"].RootFingerprint
	for _, owner := range binding.Owners {
		if _, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, *binding, owner); err != nil {
			return bad()
		}
	}
	if e.Check(context.Background()) != nil {
		return bad()
	}
	return binding, nil
}
