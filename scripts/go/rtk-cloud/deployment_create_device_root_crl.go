package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createDeviceRootCRLSchema = "rtk-cloud.create-device-root-crl-recovery/v1"
const createDeviceRootCRLID = "80448538-2c59-4861-997f-cca90c3a1fc3"

type createDeviceRootCRLPlan struct {
	Schema, Parent, SourceSHA, OwnerSHA, SourcePins, CheckerSHA, ConfigurationSHA, KubeconfigSHA string
	Configuration                                                                                deploymentConfig
	Root                                                                                         environmentCreateRegisteredIssuer
	Snapshot                                                                                     createRootCRLNativeSnapshot
	ProviderSHA, ProviderReview                                                                  string
	Operator                                                                                     initialOperatorBinding
	Controller, ConfigMap                                                                        deploymentplan.Resource
	Consumers                                                                                    []createRootCRLStateTarget
	Volumes                                                                                      map[string]environmentCreateVolumeOwner
	NativeEvidence                                                                               map[string]string
}

type createDeviceRootCRLArtifact struct {
	Schema                  string
	Plan                    createDeviceRootCRLPlan
	Qualification           deploymentplan.Qualification
	QualifiedAt, ValidUntil time.Time
}

// This journal belongs to the standalone repair, never the original parent.
// Rotation intent is durable before the provider call; an uncertain call is
// reconciled by reading the provider CRL, never by repeating rotation.
type createDeviceRootCRLJournal struct {
	Schema, Parent, Review, SourceSHA, OwnerSHA, Stage                         string
	CompletionReview                                                           string    `json:",omitempty"`
	CompletionStartedAt                                                        time.Time `json:",omitempty"`
	StartedAt, Deadline, RotationStartedAt, RotatedAt, ImportedAt, CompletedAt time.Time
	Lease                                                                      deploymentplan.LeaseOwner
	LeaseReleased                                                              bool
	Signed                                                                     createRootCRLRecord
	Evidence                                                                   map[string]string
}

var createDeviceRootCRLGates = []string{"deployment.device-root-crl.original-authority", "deployment.device-root-crl.live-revocations", "deployment.device-root-crl.provider-key", "deployment.device-root-crl.original-operator", "deployment.device-root-crl.original-consumers", "deployment.device-root-crl.execution-lease"}

func deviceRootCRLError(stage string) error {
	return createProviderRecoveryBlock("original online Device Root CRL repair requires source374, original provider key, complete revocations and genuine consumer ACKs: stage=" + stage)
}
func deviceRootCRLBase(review string) string {
	return filepath.Join("deployment/create/device-root-crl-renewal", review)
}
func deviceRootCRLPointer(parent string) string {
	return filepath.Join("deployment/create/material", parent, "device-root-crl-renewal.json")
}

func deviceRootCRLRecord(record createRootCRLRecord) managedLateCRLRecord {
	return managedLateCRLRecord{IssuerID: record.IssuerID, Digest: record.Digest, Number: record.Number, PEM: record.PEM, ThisUpdate: record.ThisUpdate, NextUpdate: record.NextUpdate}
}

func validateDeviceRootCRL(root environmentCreateRegisteredIssuer, record createRootCRLRecord, number string, fresh bool, now time.Time) (*x509.RevocationList, error) {
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if root.ID != createDeviceRootCRLID || root.Environment != "dev" || root.Domain != "device" || root.Kind != "root" || root.Status != "active" || root.Document["signer_provider"] != "openbao" || root.Reference != "pki-issuers/device/"+createDeviceRootCRLID+"/v1" || cert == nil || err != nil || cert.CheckSignatureFrom(cert) != nil || record.Number != number {
		return nil, deviceRootCRLError("original-authority-and-sequence")
	}
	clock := record.ThisUpdate
	if fresh {
		clock = now
	}
	if _, err := validateManagedLateSignedCRL("device", root.ID, root.RootFingerprint, "active", root.ChainPEM, deviceRootCRLRecord(record), managedLateObserveACKReady, clock); err != nil || fresh && !record.NextUpdate.After(now.Add(45*time.Minute)) {
		return nil, deviceRootCRLError("native-signature-and-freshness")
	}
	block, _ := pem.Decode([]byte(record.PEM))
	if block == nil {
		return nil, deviceRootCRLError("native-CRL-PEM")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		return nil, deviceRootCRLError("native-CRL-PEM")
	}
	return crl, nil
}

func deviceRootCRLUnion(root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot) ([]createRootCRLRevocation, error) {
	prior, err := validateDeviceRootCRL(root, snapshot.Prior, "3", false, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	cert, _ := parseFirstCertificatePEM([]byte(root.Certificate))
	entries := map[string]createRootCRLRevocation{}
	add := func(v createRootCRLRevocation) error {
		n, ok := new(big.Int).SetString(v.Serial, 16)
		if !ok || n.Sign() <= 0 || n.Text(16) != v.Serial || v.RevokedAt.IsZero() || v.RevokedAt.After(time.Now()) || v.Reason < 0 || v.Reason > 10 || v.Reason == 7 || v.Reason == 8 {
			return deviceRootCRLError("complete-revocation-union")
		}
		v.RevokedAt = v.RevokedAt.UTC().Truncate(time.Second)
		if old, ok := entries[v.Serial]; ok {
			if !old.RevokedAt.Equal(v.RevokedAt) || v.Reason != 0 && v.Reason != old.Reason {
				return deviceRootCRLError("conflicting-revocation")
			}
			return nil
		}
		entries[v.Serial] = v
		return nil
	}
	for _, v := range prior.RevokedCertificateEntries {
		if err := add(createRootCRLRevocation{v.SerialNumber.Text(16), v.RevocationTime, v.ReasonCode}); err != nil {
			return nil, err
		}
	}
	for _, v := range snapshot.Bindings {
		if err := add(v); err != nil {
			return nil, err
		}
	}
	for _, v := range snapshot.Certificates {
		leaf, err := parseFirstCertificatePEM([]byte(v.PEM))
		if err != nil || leaf == nil || leaf.CheckSignatureFrom(cert) != nil {
			return nil, deviceRootCRLError("revoked-original-certificate")
		}
		if err := add(createRootCRLRevocation{Serial: leaf.SerialNumber.Text(16), RevokedAt: v.RevokedAt}); err != nil {
			return nil, err
		}
	}
	keys := []string{}
	for k := range entries {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := []createRootCRLRevocation{}
	for _, k := range keys {
		out = append(out, entries[k])
	}
	return out, nil
}

func validateDeviceRootCRLSuccessor(p createDeviceRootCRLPlan, raw []byte, started, now time.Time) (createRootCRLRecord, error) {
	var out createRootCRLRecord
	block, rest := pem.Decode(raw)
	if block == nil || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return out, deviceRootCRLError("provider-CRL-response")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.Number == nil {
		return out, deviceRootCRLError("provider-CRL-response")
	}
	out = createRootCRLRecord{p.Root.ID, environmentPreflightBytesSHA256(crl.Raw), crl.Number.String(), string(raw), crl.ThisUpdate, crl.NextUpdate}
	prior, priorErr := validateDeviceRootCRL(p.Root, p.Snapshot.Prior, "3", false, now)
	if priorErr != nil {
		return createRootCRLRecord{}, priorErr
	}
	// OpenBao owns CRL numbering. A single admitted rotation may advance more
	// than one number; the signed outcome must strictly advance the exact prior.
	if _, err := validateDeviceRootCRL(p.Root, out, out.Number, true, now); err != nil || crl.Number.Cmp(prior.Number) <= 0 || crl.ThisUpdate.Before(prior.ThisUpdate) || started.IsZero() || crl.ThisUpdate.Before(started.Add(-time.Second)) {
		return createRootCRLRecord{}, deviceRootCRLError("same-key-current-successor")
	}
	wanted, err := deviceRootCRLUnion(p.Root, p.Snapshot)
	actual := []createRootCRLRevocation{}
	for _, v := range crl.RevokedCertificateEntries {
		actual = append(actual, createRootCRLRevocation{v.SerialNumber.Text(16), v.RevocationTime, v.ReasonCode})
	}
	slices.SortFunc(actual, func(a, b createRootCRLRevocation) int { return strings.Compare(a.Serial, b.Serial) })
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(wanted) {
		return createRootCRLRecord{}, deviceRootCRLError("exact-complete-revocation-union")
	}
	return out, nil
}

func validateDeviceRootCRLCurrent(p createDeviceRootCRLPlan, record createRootCRLRecord, now time.Time) error {
	canonical, err := validateDeviceRootCRLSuccessor(p, []byte(record.PEM), p.Snapshot.Prior.ThisUpdate, now)
	if err != nil || deploymentplan.Digest(canonical) != deploymentplan.Digest(record) {
		return deviceRootCRLError("exact-current-signed-successor")
	}
	return nil
}

// This read-only proof reconciles an already issued outcome. It grants no
// rotation or write authority; a separate fresh qualification binds its exact
// bytes and the untouched historical rotating journal before completion.
func validateDeviceRootCRLRotationReadback(p createDeviceRootCRLPlan, original createDeviceRootCRLJournal, provider createRootCRLRecord, now time.Time) error {
	if original.Schema != createDeviceRootCRLSchema+"/journal" || original.Parent != p.Parent || original.Review != deploymentplan.Digest(p) || original.SourceSHA != p.SourceSHA || original.OwnerSHA != p.OwnerSHA || original.Stage != "rotating" || original.CompletionReview != "" || !original.CompletionStartedAt.IsZero() || !original.LeaseReleased || original.Lease.ResourceID != "Lease/"+p.Configuration.Values["CLOUD_STACK_NAME"]+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(original.Lease.UID) || original.Lease.Holder == "" || original.StartedAt.IsZero() || original.RotationStartedAt.Before(original.StartedAt) || original.RotationStartedAt.After(now) || !original.Deadline.Equal(original.StartedAt.Add(30*time.Minute)) || original.RotationStartedAt.After(original.Deadline) || !original.RotatedAt.IsZero() || !original.ImportedAt.IsZero() || !original.CompletedAt.IsZero() || len(original.Evidence) != 0 || deploymentplan.Digest(original.Signed) != deploymentplan.Digest(createRootCRLRecord{}) {
		return deviceRootCRLError("original-released-uncertain-rotation-intent")
	}
	canonical, err := validateDeviceRootCRLSuccessor(p, []byte(provider.PEM), original.RotationStartedAt, now)
	if err != nil || deploymentplan.Digest(canonical) != deploymentplan.Digest(provider) {
		return deviceRootCRLError("canonical-existing-rotation-outcome")
	}
	return nil
}

type managedLateDeviceRootCRLObservationKey struct{}
type managedLateDeviceRootCRLObservation struct {
	Plan       createDeviceRootCRLPlan
	Signed     *createRootCRLRecord
	Recovery   *createServiceSessionRecovery
	Source     *createManagedLateExpiredRenewal
	SourcePins string
}

// The observation is created only after loadDeviceRootCRLSource authenticates
// the original source and provider ancestor. A CRL callback must not recursively
// rebuild that same history; it still binds the in-memory proof to this exact
// unchanged command source. Every native observation below remains fresh.
func deviceRootCRLObservationSourceValid(observation managedLateDeviceRootCRLObservation, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) bool {
	p, source, authenticated := observation.Plan, observation.Source, observation.Recovery
	return e != nil && source != nil && authenticated != nil && authenticated.ManagedTransportRecovery != nil && authenticated.ManagedLateExpiredRenewal != nil && r != nil && r.ManagedTransportRecovery != nil &&
		e.Store.Root == store.Root && len(e.Journal.Entries) == 374 && e.Journal.CompletedAt.IsZero() &&
		p.Schema == createDeviceRootCRLSchema && p.Parent == e.Journal.PlanSHA256 && p.SourceSHA == deploymentplan.Digest(e.Journal) && p.OwnerSHA == deploymentplan.Digest(e.owner) &&
		source.ParentSHA == p.Parent && source.SourceSHA == p.SourceSHA && source.SourceOwnerSHA == p.OwnerSHA &&
		environmentPreflightDigest.MatchString(observation.SourcePins) && observation.SourcePins == p.SourcePins && managedLateExpiredRenewalSourcePins(source) == observation.SourcePins && managedLateExpiredRenewalSourcePins(authenticated.ManagedLateExpiredRenewal) == observation.SourcePins &&
		p.ConfigurationSHA == deploymentplan.Digest(p.Configuration) && environmentPreflightDigest.MatchString(p.ProviderReview) && environmentPreflightDigest.MatchString(p.ProviderSHA) &&
		deploymentplan.Digest(authenticated.ManagedTransportRecovery.Device) == deploymentplan.Digest(p.Root) && deploymentplan.Digest(r.ManagedTransportRecovery.Device) == deploymentplan.Digest(p.Root)
}

func managedLateDeviceRootCRLPrior(ctx context.Context, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, record managedLateCRLRecord) (managedLateCRLProof, bool, error) {
	observation, ok := ctx.Value(managedLateDeviceRootCRLObservationKey{}).(managedLateDeviceRootCRLObservation)
	if !ok || record.IssuerID != createDeviceRootCRLID {
		return managedLateCRLProof{}, false, nil
	}
	p := observation.Plan
	if managedLateSourceReuseActive(e) {
		if err := checkManagedLateSourceReuse(e); err != nil {
			return managedLateCRLProof{}, true, err
		}
		if !deviceRootCRLObservationSourceValid(observation, store, e, r) {
			return managedLateCRLProof{}, true, deviceRootCRLError("scoped-original-Device-root")
		}
	} else {
		// A mutation invalidates the shared capability in every source frame.
		// Post-write observations authenticate source history anew instead.
		if _, _, err := loadDeviceRootCRLSource(p.Configuration, store, e, &p); err != nil {
			return managedLateCRLProof{}, true, err
		}
		if r == nil || r.ManagedTransportRecovery == nil || deploymentplan.Digest(r.ManagedTransportRecovery.Device) != deploymentplan.Digest(p.Root) {
			return managedLateCRLProof{}, true, deviceRootCRLError("scoped-original-Device-root")
		}
	}
	expected, clock := p.Snapshot.Prior, record.ThisUpdate
	if observation.Signed != nil {
		expected, clock = *observation.Signed, time.Now().UTC()
	}
	if deploymentplan.Digest(record) != deploymentplan.Digest(deviceRootCRLRecord(expected)) {
		return managedLateCRLProof{}, true, deviceRootCRLError("only-exact-original-prior-or-accepted-successor")
	}
	proof, err := validateManagedLateSignedCRL("device", p.Root.ID, p.Root.RootFingerprint, "active", p.Root.ChainPEM, record, managedLateObserveACKReady, clock)
	return proof, true, err
}

func loadDeviceRootCRLSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createDeviceRootCRLPlan) (*createServiceSessionRecovery, *createManagedLateExpiredRenewal, error) {
	r, source, err := loadManagedLateExpiredRenewalSource(cfg, store, e)
	if err != nil {
		return nil, nil, err
	}
	if p != nil && (p.Schema != createDeviceRootCRLSchema || p.Parent != e.Journal.PlanSHA256 || p.SourceSHA != deploymentplan.Digest(e.Journal) || p.OwnerSHA != deploymentplan.Digest(e.owner) || p.SourcePins != managedLateExpiredRenewalSourcePins(source) || deploymentplan.Digest(p.Root) != deploymentplan.Digest(r.ManagedTransportRecovery.Device)) {
		return nil, nil, deviceRootCRLError("exact-source374")
	}
	if p != nil {
		if !environmentPreflightDigest.MatchString(p.ProviderReview) || !environmentPreflightDigest.MatchString(p.ProviderSHA) {
			return nil, nil, deviceRootCRLError("original-provider-ancestor-required")
		}
		a, _, err := loadAdmittedCreateRecovery(store, e, p.ProviderReview)
		if err != nil || a.Plan.Materials["accepted-service-core-device-trust-signer"] != p.ProviderSHA {
			return nil, nil, deviceRootCRLError("authenticated-original-provider-ancestor")
		}
	}
	return r, source, nil
}

func readDeviceRootCRLCache(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, p createDeviceRootCRLPlan, target createRootCRLStateTarget, record createRootCRLRecord) ([]byte, error) {
	if len(target.Paths) != 1 || verifyCreateRootCRLTargetOwners(ctx, cfg, b, target) != nil {
		return nil, deviceRootCRLError("exact-consumer-Pod")
	}
	raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "pod/"+target.PodName, "--container", target.Container, "--", "/bin/sh", "-ec", `test -f "$1" && test ! -L "$1" && test "$(stat -c %a "$1")" = 600 && test "$(stat -c %u "$1")" = 10001 && cat "$1"`, "device-root-crl-native-state", target.Paths[0])
	var cache struct {
		Fingerprint string              `json:"issuer_fingerprint"`
		CRL         createRootCRLRecord `json:"crl"`
		Terminal    string              `json:"terminal_issuer_id"`
		Retained    json.RawMessage     `json:"retained_crl"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &cache) != nil || cache.Fingerprint != p.Root.RootFingerprint || cache.Terminal != "" || len(cache.Retained) != 0 || deploymentplan.Digest(cache.CRL) != deploymentplan.Digest(record) {
		return nil, deviceRootCRLError("exact-signed-native-Device-cache")
	}
	if record.Number == "3" {
		if _, err := validateDeviceRootCRL(p.Root, record, "3", false, time.Now().UTC()); err != nil {
			return nil, err
		}
	} else if err := validateDeviceRootCRLCurrent(p, record, time.Now().UTC()); err != nil {
		return nil, err
	}
	return raw, nil
}

func deviceRootCRLProviderAncestor(store secretStore, e *environmentCreateExecution, proof string) (string, error) {
	for _, entry := range e.Journal.Entries {
		const prefix = "openbao-recovery-admission-"
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, prefix) {
			continue
		}
		review := strings.TrimPrefix(entry.Key, prefix)
		if !environmentPreflightDigest.MatchString(review) {
			return "", deviceRootCRLError("original-provider-admission")
		}
		var candidate createFoundationRecoveryArtifact
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), &candidate) != nil {
			return "", deviceRootCRLError("original-provider-qualification")
		}
		if candidate.Plan.Materials["accepted-service-core-device-trust-signer"] == "" {
			continue
		}
		a, _, err := loadAdmittedCreateRecovery(store, e, review)
		if err != nil || a.Plan.Materials["accepted-service-core-device-trust-signer"] != proof {
			return "", deviceRootCRLError("unchanged-original-provider-key")
		}
		return review, nil
	}
	return "", deviceRootCRLError("accepted-original-provider-proof-absent")
}

func beginDeviceRootCRLSourcePhase(store secretStore, e *environmentCreateExecution) error {
	if managedLateSourceReuseActive(e) {
		return checkManagedLateSourceReuse(e)
	}
	// A prior phase's shared pointer stays invalidated. This new capability
	// starts empty and can memoize only newly successful strong derivations.
	reuse, err := newManagedLateSourceReuse(store, e)
	if err != nil {
		return err
	}
	e.lateSourceReuse = reuse
	return nil
}

func observeDeviceRootCRLNative(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p createDeviceRootCRLPlan, signed *createRootCRLRecord) (map[string]string, error) {
	if err := beginDeviceRootCRLSourcePhase(store, e); err != nil {
		return nil, err
	}
	r, source, err := loadDeviceRootCRLSource(cfg, store, e, &p)
	if err != nil {
		return nil, err
	}
	a, _, err := loadAdmittedCreateRecovery(store, e, source.PriorReview)
	if err != nil {
		return nil, err
	}
	plan := a.Plan
	plan.Materials = map[string]string{}
	for k, v := range a.Plan.Materials {
		plan.Materials[k] = v
	}
	ledger, err := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if err != nil || createOpenBaoACKCompletionRead(store, "deployment/resources/active.json", &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return nil, deviceRootCRLError("canonical-original-volume-ledger")
	}
	plan.Materials["accepted-service-core-managed-application-current-volume-ledger"] = active.LedgerSHA
	plan.Materials["accepted-service-core-managed-application-current-volume-active"] = deploymentplan.Digest(active)
	scoped := context.WithValue(ctx, managedLateDeviceRootCRLObservationKey{}, managedLateDeviceRootCRLObservation{Plan: p, Signed: signed, Recovery: r, Source: source, SourcePins: managedLateExpiredRenewalSourcePins(source)})
	if err := verifyManagedLateSessionCurrentNative(scoped, cfg, store, e, b, r, &plan, r.ManagedLateSession); err != nil {
		return nil, err
	}
	if len(p.NativeEvidence) > 0 {
		for _, k := range []string{"accepted-service-core-managed-complete-current-native-volumes", managedLateAPICurrentMaterial, "accepted-service-core-managed-application-current-servers", "accepted-service-core-managed-application-current-clients"} {
			if !environmentPreflightDigest.MatchString(p.NativeEvidence[k]) || p.NativeEvidence[k] != plan.Materials[k] {
				return nil, deviceRootCRLError("original-identity-volume-continuity")
			}
		}
	}
	return plan.Materials, e.Check(ctx)
}

// Only public metadata and CRLs leave the original provider; never key bytes.
func readDeviceRootCRLProvider(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, root environmentCreateRegisteredIssuer, rotate bool) (string, []byte, error) {
	if validateEnvironmentCreateDeviceRoot(cfg, root) != nil || root.ID != createDeviceRootCRLID || e.Check(ctx) != nil {
		return "", nil, deviceRootCRLError("original-provider-source")
	}
	token, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/root-token"), true)
	if err != nil {
		return "", nil, deviceRootCRLError("original-provider-authentication")
	}
	input := append(bytes.TrimSpace(token), '\n')
	prefix := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, input, prefix+"bao read -format=json "+root.Reference+"/issuer/default\n")
	if err != nil {
		return "", nil, deviceRootCRLError("original-provider-public-issuer")
	}
	proof, err := deviceTrustSignerObservation(raw, root)
	if err != nil {
		return "", nil, err
	}
	if rotate {
		raw, err = environmentCreateOpenBaoExec(ctx, cfg, b, input, prefix+"bao read -format=json "+root.Reference+"/crl/rotate\n")
		var result struct {
			Data struct {
				Success bool `json:"success"`
			} `json:"data"`
		}
		if err != nil || json.Unmarshal(raw, &result) != nil || !result.Data.Success {
			return proof, nil, deviceRootCRLError("provider-rotation-outcome-requires-reconciliation")
		}
	}
	raw, err = environmentCreateOpenBaoExec(ctx, cfg, b, input, prefix+"bao read -field=certificate "+root.Reference+"/cert/crl\n")
	if err != nil {
		return proof, nil, deviceRootCRLError("provider-current-signed-CRL")
	}
	return proof, raw, nil
}

func deviceRootCRLTargets(cfg deploymentConfig, r *createServiceSessionRecovery, objects []deploymentplan.Object) (deploymentplan.Resource, deploymentplan.Resource, []createRootCRLStateTarget, error) {
	if r == nil || r.ManagedLateExpiredRenewal == nil {
		return deploymentplan.Resource{}, deploymentplan.Resource{}, nil, deviceRootCRLError("original-current-owners")
	}
	owners := r.ManagedLateExpiredRenewal.PriorOwners
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	controller, cm := owners["Deployment/"+ns+"/pki-controller"], owners["ConfigMap/"+ns+"/pki-create-device-trust"]
	c := managedNamedField(environmentPKIPod(controller.Desired)["containers"], "pki-controller")
	roster := strings.Split(mqttFoundationEnv(c, "PKI_REQUIRED_CONSUMERS_DEVICE"), ",")
	slices.Sort(roster)
	if !slices.Equal(roster, []string{"pkibroker", "video-cloud-api"}) || !environmentBootstrapOwnerUUID.MatchString(cm.UID) {
		return controller, cm, nil, deviceRootCRLError("exact-required-consumers")
	}
	targets := []createRootCRLStateTarget{}
	for _, name := range []string{"video-cloud-api-pki", "mqtt-pki"} {
		resource := owners["Deployment/"+ns+"/"+name]
		container, key, path, claim, setting := "app", "api-crls.json", "/run/pki-state/device-root-crl.json", "video-cloud-api-pki-trust", "VIDEO_CLOUD_AUTH_CRL_MANIFEST"
		if name == "mqtt-pki" {
			container, key, path, claim, setting = "pkibroker", "broker-crls.json", "/run/pki-state/device-crl.json", "mqtt-pki-trust", "PKI_BROKER_DEVICE_CRL_MANIFEST"
		}
		c := managedNamedField(environmentPKIPod(resource.Desired)["containers"], container)
		file, err := coreMQTTFile(resource.Desired, c, "pki-device", key)
		mount := managedNamedField(c["volumeMounts"], "pki-state")
		volume := managedNamedField(environmentPKIPod(resource.Desired)["volumes"], "pki-state")
		var entries []struct {
			Issuer deploymentplan.Object `json:"issuer"`
			Path   string                `json:"state_path"`
		}
		root := r.ManagedTransportRecovery.Device
		if err != nil || file.reference != "pki-create-device-trust" || file.kind != "ConfigMap" || file.file != "/run/pki-device/"+key || mqttFoundationEnv(c, setting) != file.file || mount["mountPath"] != "/run/pki-state" || mount["readOnly"] == true || mount["subPath"] != nil || deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] != claim || decodeEnvironmentJSON([]byte(deploymentplan.String(deploymentplan.Map(cm.Desired["data"])[key])), &entries) != nil || len(entries) != 1 || entries[0].Path != path {
			return controller, cm, nil, deviceRootCRLError("original-manifest-and-private-claim")
		}
		issuer, err := environmentCreateRegisteredIssuerFromMap(entries[0].Issuer, root.RootFingerprint)
		if err != nil || issuer.ID != root.ID || issuer.Certificate != root.Certificate || issuer.ChainPEM != root.ChainPEM || issuer.BundleVersion != root.BundleVersion || issuer.Domain != "device" || deploymentplan.String(deploymentplan.Map(cm.Desired["data"])["roots.pem"]) != root.Certificate {
			return controller, cm, nil, deviceRootCRLError("original-Device-manifest")
		}
		target := createRootCRLStateTarget{ID: resource.ID, UID: resource.UID, Desired: deploymentplan.Clone(resource.Desired), Container: container, ClaimID: "PersistentVolumeClaim/" + ns + "/" + claim, Paths: []string{path}, StateSHA: map[string]string{}}
		if err := managedLateAppRootCRLInventoryTarget(objects, &target, true); err != nil {
			return controller, cm, nil, err
		}
		targets = append(targets, target)
	}
	return controller, cm, targets, nil
}

func observeDeviceRootCRLConsumer(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p createDeviceRootCRLPlan, target createRootCRLStateTarget, signed createRootCRLRecord) (map[string]string, error) {
	if len(target.Paths) != 1 || len(p.Consumers) != 2 || !slices.ContainsFunc(p.Consumers, func(v createRootCRLStateTarget) bool {
		return deploymentplan.Digest(v) == deploymentplan.Digest(target)
	}) {
		return nil, deviceRootCRLError("admitted-original-consumer")
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil || managedLateAppRootCRLInventoryTarget(objects, &target, true) != nil {
		return nil, deviceRootCRLError("owned-ready-consumer")
	}
	if err := verifyManagedLateAppRootCRLPodImages(ctx, target, objects, func(ctx context.Context, image string) (map[string]bool, error) {
		return environmentRuntimeImageDigests(ctx, store, image)
	}); err != nil {
		return nil, err
	}
	byID := map[string]deploymentplan.Object{}
	for _, o := range objects {
		byID[deploymentplan.ID(o)] = o
	}
	for _, resource := range []deploymentplan.Resource{p.Controller, p.ConfigMap} {
		o := byID[resource.ID]
		if o == nil || deploymentplan.Map(o["metadata"])["uid"] != resource.UID || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, o) {
			return nil, deviceRootCRLError("unchanged-controller-CM")
		}
	}
	owner, ok := p.Volumes[target.ClaimID]
	volume, err := b.Read(ctx, "PersistentVolume//"+owner.PVName)
	if !ok || err != nil || owner.PVCUID != target.ClaimUID || validateManagedTransportHostVolume(owner, byID[target.ClaimID], volume) != nil {
		return nil, deviceRootCRLError("original-consumer-volume")
	}
	raw, err := readDeviceRootCRLCache(ctx, cfg, b, p, target, signed)
	if err != nil {
		return nil, err
	}
	if err := validateDeviceRootCRLCurrent(p, signed, time.Now().UTC()); err != nil {
		return nil, err
	}
	query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'digest',digest,'consumer_id',consumer_id,'acknowledged_at',acknowledged_at) ORDER BY consumer_id),'[]'::json) FROM pki_crl_acknowledgments WHERE issuer_id='" + createDeviceRootCRLID + "' AND digest='" + signed.Digest + "'"
	ackRaw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var acks []managedLatePriorACK
	if err != nil || decodeEnvironmentJSON(ackRaw, &acks) != nil || validateManagedLatePriorACKRoster(acks, p.Root.ID, deviceRootCRLRecord(signed), []string{"pkibroker", "video-cloud-api"}, time.Now().UTC()) != nil {
		return nil, deviceRootCRLError("genuine-two-Device-CRL-ACKs")
	}
	return map[string]string{"consumer-owner-" + target.ID: deploymentplan.Digest(target.Desired), "consumer-Pod-" + target.ID: deploymentplan.Digest(target.PodUID), "consumer-volume-" + target.ClaimID: deploymentplan.Digest(owner), "cache-" + target.ID: environmentPreflightBytesSHA256(raw), "native-ACKs-" + p.Root.ID: deploymentplan.Digest(acks)}, e.Check(ctx)
}
