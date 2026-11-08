package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentCustodyCRLState struct {
	IssuerSHA256 string               `json:"issuer_fingerprint"`
	CRL          createRootCRLRecord  `json:"crl"`
	Terminal     string               `json:"terminal_issuer_id,omitempty"`
	Retained     *createRootCRLRecord `json:"retained_crl,omitempty"`
}

func environmentCustodyPublicCRLJob(cfg deploymentConfig, binding *environmentCustodyBinding, ref *environmentCustodyPublicCRLReference, source deploymentplan.Object, image, bootstrapRoot string, apply bool) (deploymentplan.Object, error) {
	if ref == nil || binding == nil {
		return nil, environmentCreateBootstrapBlocker("original public CRL scope is required")
	}
	alias := ""
	for _, owner := range binding.Owners {
		if owner.ID == ref.Owner && owner.Namespace == ref.Namespace {
			aliases := owner.StorageAliases
			if len(aliases) == 0 {
				aliases = []string{owner.ID}
			}
			alias = aliases[0]
		}
	}
	if err := validateEnvironmentCustodyPublicCRLReference(ref, cfg, binding, ref.Namespace, alias, ref.StatePath, ref.Domain, ref.IssuerID, ref.IssuerSHA256); err != nil {
		return nil, err
	}
	// Exec streams public JSON directly, avoiding argument/environment size limits
	// and another temporary ConfigMap. The Job deadline and TTL bound its lifetime.
	command := []any{"/bin/sh", "-ec", "trap 'exit 0' TERM INT; while :; do sleep 1; done"}
	return environmentCustodyScopedJob(cfg, binding, ref.Namespace, alias, ref.StatePath, source, image, bootstrapRoot, command, apply)
}

func runEnvironmentCustodyPublicCRL(ctx context.Context, b environmentKubernetesBackend, job deploymentplan.Object, ref *environmentCustodyPublicCRLReference, issuer environmentCreateRegisteredIssuer, record *createRootCRLRecord) (environmentCustodyCRLState, error) {
	var empty environmentCustodyCRLState
	if ref == nil || issuer.Environment != b.Store.Environment || ref.IssuerID != issuer.ID || ref.Domain != issuer.Domain || ref.IssuerSHA256 != deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"]) {
		return empty, environmentCreateBootstrapBlocker("public CRL operation differs from its original authority")
	}
	arguments := []string{"inspect-public-crl", ref.StatePath, ref.IssuerID, ref.IssuerSHA256}
	var input []byte
	if record != nil {
		arguments[0] = "apply-public-crl"
		input, _ = json.Marshal(record)
	}
	raw, err := runEnvironmentCustodyJob(ctx, b, job, ref.Namespace, arguments, input)
	if err != nil {
		return empty, err
	}
	var result environmentCustodyCRLState
	if decodeEnvironmentJSON(raw, &result) != nil || result.IssuerSHA256 != ref.IssuerSHA256 || result.Terminal != "" {
		return empty, environmentCreateBootstrapBlocker("native committed public CRL readback differs or retains terminal denial")
	}
	if _, err := validateMaintenanceCRL(b.Store.Environment, issuer, result.CRL, record != nil, time.Now().UTC()); err != nil {
		return empty, environmentCreateBootstrapBlocker("native committed CRL signature or authority is invalid")
	}
	if record != nil && result.CRL.Digest != record.Digest {
		return empty, environmentCreateBootstrapBlocker("committed public CRL differs from the selected native record; rediscover current state")
	}
	return result, nil
}

// These are original generation facts, not a consumable preflight GO. No image
// selection, identity creation or recovery-state substitution happens on a read.
func readEnvironmentCustodyBootstrapBinding(cfg deploymentConfig, store secretStore, generation string) (*environmentCustodyBinding, error) {
	bad := func() (*environmentCustodyBinding, error) {
		return nil, environmentCreateBootstrapBlocker("original completed PKI bootstrap custody scope is unavailable or inconsistent")
	}
	if store.Environment != cfg.Environment || cfg.Values["PKI_STATE_BACKEND"] != "openbao" || !environmentPreflightDigest.MatchString(generation) {
		return bad()
	}
	path, err := store.safePath(filepath.Join("deployment", "create", generation, "bootstrap", "bindings.json"))
	if err != nil {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var recorded environmentIdentityBindings
	if err != nil || decodeEnvironmentJSON(raw, &recorded) != nil || validateEnvironmentIdentityBindings(recorded, cfg.Environment) != nil || recorded.Custody == nil || recorded.Custody.Generation != generation || !environmentPreflightDigest.MatchString(recorded.Custody.ServiceRootSHA256) {
		return bad()
	}
	snapshot, _, err := readEnvironmentCustodyTransportState(cfg, store, generation)
	if err != nil || !snapshot.ManagedOnly || recorded.Custody.OpenBaoRootSHA256 != snapshot.ManagedRootSHA256 {
		return bad()
	}
	for _, owner := range recorded.Custody.Owners {
		if _, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, *recorded.Custody, owner); err != nil {
			return bad()
		}
	}
	return recorded.Custody, nil
}

func (r *maintenanceRuntime) custodyPublicCRL(ctx context.Context, pod deploymentplan.Object, c crlMaintenanceConsumer, issuer environmentCreateRegisteredIssuer, record *createRootCRLRecord) (environmentCustodyCRLState, error) {
	var empty environmentCustodyCRLState
	if record != nil && r.lease == nil {
		return empty, environmentCreateBootstrapBlocker("public CRL installation requires original environment execution ownership")
	}
	spec := deploymentplan.Map(pod["spec"])
	source := managedNamedField(spec["containers"], c.Container)
	prefix := mqttFoundationEnv(source, "PKI_STATE_RECORD_PREFIX")
	parts := strings.Split(prefix, "/")
	if len(parts) != 3 || parts[0] != r.cfg.Environment || mqttFoundationEnv(source, "PKI_STATE_BACKEND") != "openbao" || deploymentplan.Map(pod["metadata"])["namespace"] != c.Namespace || deploymentplan.Map(pod["metadata"])["uid"] != c.PodUID {
		return empty, environmentCreateBootstrapBlocker("original public CRL consumer scope changed")
	}
	binding, err := readEnvironmentCustodyBootstrapBinding(r.cfg, r.store, parts[1])
	if err != nil {
		return empty, err
	}
	alias := ""
	for _, owner := range binding.Owners {
		if owner.ID == parts[2] && owner.Namespace == c.Namespace && spec["serviceAccountName"] == owner.ServiceAccount {
			aliases := owner.StorageAliases
			if len(aliases) == 0 {
				aliases = []string{owner.ID}
			}
			alias = aliases[0]
		}
	}
	pin := deploymentplan.String(issuer.Document["certificate_fingerprint_sha256"])
	ref, err := newEnvironmentCustodyPublicCRLReference(r.cfg, binding, c.Namespace, alias, c.StatePath, issuer.Domain, issuer.ID, pin)
	if err != nil {
		return empty, err
	}
	placement := deploymentplan.Object{"metadata": pod["metadata"], "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": spec}}}
	owned := false
	for _, raw := range certIssuerObjectList(source["volumeMounts"]) {
		name := deploymentplan.String(deploymentplan.Map(raw)["name"])
		_, valid := environmentCustodyMemoryRelativeFile(placement, source, name, c.StatePath)
		owned = owned || valid
	}
	if !owned {
		return empty, environmentCreateBootstrapBlocker("original public CRL path is outside its selected memory projection")
	}
	parentID, parent, err := r.consumerDeployment(ctx, c)
	if err != nil || parentID != "Deployment/"+c.Namespace+"/"+ref.Owner {
		return empty, environmentCreateBootstrapBlocker("current public CRL consumer has no original logical owner chain")
	}
	if r.image == "" {
		if r.ensureImage == nil {
			return empty, environmentCreateBootstrapBlocker("selected native public CRL helper image is unavailable")
		}
		r.image, err = r.ensureImage(ctx)
		if err != nil {
			return empty, err
		}
	}
	profile := deploymentplan.Clone(spec)
	profile["containers"] = []any{source}
	job, err := environmentCustodyPublicCRLJob(r.cfg, binding, ref, profile, r.image, "", record != nil)
	if err != nil {
		return empty, err
	}
	backend := r.backend
	if backend.Store.Root != r.store.Root || backend.Store.Environment != r.cfg.Environment {
		return empty, environmentCreateBootstrapBlocker("public CRL helper differs from selected environment store")
	}
	if backend.RolloutTimeout <= 0 {
		backend.RolloutTimeout, err = environmentRolloutTimeout(r.cfg.Values)
		if err != nil {
			return empty, err
		}
	}
	result, err := runEnvironmentCustodyPublicCRL(ctx, backend, job, ref, issuer, record)
	if err != nil {
		return empty, err
	}
	after, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
	if err != nil || deploymentplan.Map(after["metadata"])["uid"] != c.PodUID {
		return empty, environmentCreateBootstrapBlocker("original public CRL consumer changed during committed readback")
	}
	_, current, err := r.consumerDeployment(ctx, c)
	if err != nil || deploymentplan.Map(current["metadata"])["uid"] != deploymentplan.Map(parent["metadata"])["uid"] {
		return empty, environmentCreateBootstrapBlocker("original public CRL owner changed during committed readback")
	}
	if r.lease != nil {
		if err := r.lease.Check(ctx); err != nil {
			return empty, err
		}
	}
	return result, nil
}
