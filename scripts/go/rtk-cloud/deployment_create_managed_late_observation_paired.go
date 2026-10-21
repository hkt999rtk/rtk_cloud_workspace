package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLatePairedCRLPriorMaterial = "accepted-service-core-managed-late-observation-paired-root-priors"
const managedLatePairedCRLPriorSchema = "rtk-cloud.managed-late-paired-original-root-crl-priors/v1"

// This is historical repair evidence, never an ACK-readiness proof. Its source
// is the actually accepted Core prefix, even while the repair has its own tail.
type managedLatePairedCRLPriorEvidence struct {
	Schema, SourceSHA, CoreSourceSHA, AcceptedCoreSHA string
	Roots                                             map[string]managedLateRootPriorEvidence
}
type managedLateRootPriorEvidence struct {
	RootID, RootSHA, IntermediateID, IntermediateSHA string
	OriginalRecord                                   managedLateCRLRecord
	ICARecord                                        managedLateCRLRecord
	SnapshotSHA, RevocationsSHA, CustodySHA          string
	Consumers                                        []createRootCRLStateTarget
	ACKs                                             []managedLatePriorACK
}

func managedLatePairedOriginalRecords(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, mode managedLateSessionMode) (map[string]managedLateCRLRecord, error) {
	if mode != managedLateSessionMQTTRepair || e == nil || r == nil || r.MQTTRoot == nil || r.OpenBaoRoot == nil || r.MQTTRoot.ID != "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" || r.OpenBaoRoot.ID != "734487b8-abcf-40be-a813-8b52c64f7954" {
		return nil, managedCoreObservationBlock("paired-exact-original-root-authority")
	}
	records := map[string]managedLateCRLRecord{}
	for _, root := range []environmentCreateRegisteredIssuer{*r.MQTTRoot, *r.OpenBaoRoot} {
		if root.Environment != store.Environment || root.Kind != "root" || root.Status != "active" || root.Document["signer_provider"] != "offline" || root.ID == r.MQTTRoot.ID && root.Domain != "mqtt" || root.ID == r.OpenBaoRoot.ID && root.Domain != "openbao_tls" {
			return nil, managedCoreObservationBlock("paired-original-offline-root-kind-domain")
		}
		var selected *environmentCreateJournalEntry
		for index := range e.Journal.Entries {
			entry := &e.Journal.Entries[index]
			if entry.Key == "pki-initial-root-crl-"+root.Domain {
				if selected != nil {
					return nil, managedCoreObservationBlock("paired-original-crl-duplicate-receipt")
				}
				selected = entry
			}
		}
		intent := deploymentplan.Object{"issuer_id": root.ID, "root_sha256": root.RootFingerprint, "crl_number": "1"}
		var receipt map[string]string
		if selected == nil || selected.Phase != "bootstrap" || selected.State != "completed" || selected.StartedAt.IsZero() || selected.CompletedAt.Before(selected.StartedAt) || selected.CompletedAt.After(time.Now().UTC()) || selected.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(selected.Receipt, &receipt) != nil || len(receipt) != 3 || receipt["issuer_id"] != root.ID || !environmentPreflightDigest.MatchString(receipt["crl_pem_sha256"]) {
			return nil, managedCoreObservationBlock("paired-original-crl-completed-receipt")
		}
		// The original producer has one canonical relative output, under this exact
		// deployment generation; receipts cannot redirect to another file or store.
		expectedFile := filepath.ToSlash(filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap", root.Domain, "root-crl/revocations.pem"))
		if receipt["crl_file"] != expectedFile {
			return nil, managedCoreObservationBlock("paired-original-crl-producer-path")
		}
		path, err := store.safePath(receipt["crl_file"])
		if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return nil, managedCoreObservationBlock("paired-original-crl-private-path")
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || environmentPreflightBytesSHA256(raw) != receipt["crl_pem_sha256"] {
			return nil, managedCoreObservationBlock("paired-original-crl-receipt-bytes")
		}
		record, err := managedLateOriginalRecord(root.ID, raw)
		if err != nil {
			return nil, err
		}
		guard := managedLateObserveMQTTPrior
		if root.Domain == "openbao_tls" {
			guard = managedLateObserveOpenBaoTLSPrior
		}
		if _, err = validateManagedLateSignedCRL(root.Domain, root.ID, root.RootFingerprint, root.Status, root.ChainPEM, record, guard, time.Now().UTC()); err != nil {
			return nil, managedCoreObservationBlock("paired-original-crl-signature")
		}
		records[root.ID] = record
	}
	return records, nil
}

func managedLateOriginalRecord(id string, raw []byte) (managedLateCRLRecord, error) {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return managedLateCRLRecord{}, managedCoreObservationBlock("paired-original-crl-pem")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.Number == nil || crl.Number.String() != "1" {
		return managedLateCRLRecord{}, managedCoreObservationBlock("paired-original-crl-number")
	}
	return managedLateCRLRecord{IssuerID: id, Digest: environmentPreflightBytesSHA256(crl.Raw), Number: crl.Number.String(), PEM: string(raw), ThisUpdate: crl.ThisUpdate, NextUpdate: crl.NextUpdate}, nil
}

// The historical clock is selected only after exact original body equality.
// A signed replacement #1 is not sufficient authority for this exception.
func validateManagedLatePairedPriorCRL(domain, id, fp, status, chain string, current managedLateCRLRecord, originals map[string]managedLateCRLRecord, mode managedLateObservationMode, now time.Time) (managedLateCRLProof, error) {
	if mode != managedLateObserveMQTTPrior || len(originals) != 2 || originals["14c1a269-e32e-4eac-aa93-db5ad8e85f7e"].IssuerID != "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" || originals["734487b8-abcf-40be-a813-8b52c64f7954"].IssuerID != "734487b8-abcf-40be-a813-8b52c64f7954" {
		return managedLateCRLProof{}, managedCoreObservationBlock("paired-exact-original-records")
	}
	guard := mode
	if original, ok := originals[id]; ok {
		if original.Number != "1" || deploymentplan.Digest(current) != deploymentplan.Digest(original) || id == "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" && domain != "mqtt" || id == "734487b8-abcf-40be-a813-8b52c64f7954" && domain != "openbao_tls" {
			return managedLateCRLProof{}, managedCoreObservationBlock("paired-original-crl-body")
		}
		if domain == "openbao_tls" {
			guard = managedLateObserveOpenBaoTLSPrior
		}
	}
	return validateManagedLateSignedCRL(domain, id, fp, status, chain, current, guard, now)
}

func observeManagedLatePairedCRLPrior(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery, records map[string]managedLateCRLRecord) (*managedLatePairedCRLPriorEvidence, error) {
	if late == nil || late.Mode != managedLateSessionMQTTRepair || late.AcceptedCore == nil || r == nil || r.MQTTRoot == nil || r.MQTTIntermediate == nil || r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil {
		return nil, managedCoreObservationBlock("paired-source-mode")
	}
	originals, err := managedLatePairedOriginalRecords(store, e, r, late.Mode)
	if err != nil {
		return nil, err
	}
	proof := &managedLatePairedCRLPriorEvidence{Schema: managedLatePairedCRLPriorSchema, SourceSHA: late.SourceSHA, CoreSourceSHA: late.CoreSourceSHA, AcceptedCoreSHA: late.AcceptedCore.SHA256(), Roots: map[string]managedLateRootPriorEvidence{}}
	for _, root := range []environmentCreateRegisteredIssuer{*r.MQTTRoot, *r.OpenBaoRoot} {
		item, err := observeManagedLateRootPrior(ctx, cfg, store, e, b, r, late, root, originals[root.ID], records[root.ID])
		if err != nil {
			return nil, err
		}
		proof.Roots[root.ID] = item
	}
	return proof, nil
}

func observeManagedLateRootPrior(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery, root environmentCreateRegisteredIssuer, original, current managedLateCRLRecord) (managedLateRootPriorEvidence, error) {
	var empty managedLateRootPriorEvidence
	if original.Number != "1" || deploymentplan.Digest(current) != deploymentplan.Digest(original) {
		return empty, managedCoreObservationBlock("paired-native-original-root1")
	}
	snapshot, raw, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, root.ID)
	if err != nil || deploymentplan.Digest(snapshot.Prior) != deploymentplan.Digest(original) {
		return empty, managedCoreObservationBlock("paired-full-native-original-root1")
	}
	// This validates and retains the union of every published and native revoked
	// serial. Its timestamps are not qualification material or a signing request.
	request, err := buildCreateRootCRLRequestForDomain(root, snapshot, root.Domain, time.Now().UTC())
	if err != nil {
		return empty, managedCoreObservationBlock("paired-full-native-revocations")
	}
	var targets []createRootCRLStateTarget
	var custody map[string]string
	var ica environmentCreateRegisteredIssuer
	if root.Domain == "mqtt" {
		_, _, targets, _, _, err = loadMQTTRootCRLTargets(ctx, cfg, e, b, r, late, root, snapshot.Prior)
		ica = *r.MQTTIntermediate
	} else if root.Domain == "openbao_tls" {
		_, _, targets, _, _, err = loadOpenBaoTLSRootCRLTargets(ctx, cfg, e, b, r, late, root, snapshot.Prior)
		ica = *r.OpenBaoIntermediate
	} else {
		return empty, managedCoreObservationBlock("paired-exact-root-domain")
	}
	if err != nil {
		return empty, managedLatePairedDependencyError(root.Domain, "targets", err)
	}
	if len(targets) != 2 {
		return empty, managedCoreObservationBlock("paired-" + root.Domain + "-two-cache-targets")
	}
	if root.Domain == "mqtt" {
		custody, err = createMQTTRootCRLMaterial(store, e.Journal.PlanSHA256, root)
	} else {
		custody, err = createOpenBaoTLSRootCRLMaterial(store, e.Journal.PlanSHA256, root)
	}
	if err != nil {
		return empty, managedLatePairedDependencyError(root.Domain, "custody", err)
	}
	icaNative, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, ica.ID)
	if err != nil {
		return empty, managedCoreObservationBlock("paired-current-original-ica")
	}
	icaCert, err := parseFirstCertificatePEM([]byte(ica.Certificate))
	icaRecord := managedLateCRLRecord{IssuerID: icaNative.Prior.IssuerID, Digest: icaNative.Prior.Digest, Number: icaNative.Prior.Number, PEM: icaNative.Prior.PEM, ThisUpdate: icaNative.Prior.ThisUpdate, NextUpdate: icaNative.Prior.NextUpdate}
	if err != nil || root.Domain == "openbao_tls" && icaRecord.Number != "3" {
		return empty, managedCoreObservationBlock("paired-fresh-original-ica3")
	}
	if _, err = validateManagedLateSignedCRL(ica.Domain, ica.ID, environmentPreflightBytesSHA256(icaCert.Raw), ica.Status, ica.ChainPEM, icaRecord, managedLateObserveACKReady, time.Now().UTC()); err != nil {
		return empty, managedCoreObservationBlock("paired-fresh-original-ica")
	}
	acks, err := readManagedLateRootPriorACKs(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, root.ID, original.Digest)
	wanted := []string{"video-cloud-api", "video-cloud-logingester"}
	if root.Domain == "openbao_tls" {
		wanted = []string{"certissuer", "pki-controller"}
	}
	if err != nil || validateManagedLatePriorACKRoster(acks, root.ID, original, wanted, time.Now().UTC()) != nil {
		return empty, managedCoreObservationBlock("paired-genuine-prior-ack-roster")
	}
	return managedLateRootPriorEvidence{RootID: root.ID, RootSHA: root.RootFingerprint, IntermediateID: ica.ID, IntermediateSHA: environmentPreflightBytesSHA256(icaCert.Raw), OriginalRecord: original, ICARecord: icaRecord, SnapshotSHA: environmentPreflightBytesSHA256(raw), RevocationsSHA: deploymentplan.Digest(request.Entries), CustodySHA: deploymentplan.Digest(custody), Consumers: targets, ACKs: acks}, nil
}

func validateManagedLatePriorACKRoster(acks []managedLatePriorACK, root string, prior managedLateCRLRecord, wanted []string, now time.Time) error {
	if len(acks) != 2 || len(wanted) != 2 || !slices.IsSorted(wanted) {
		return managedCoreObservationBlock("paired-prior-ack-roster")
	}
	for i, ack := range acks {
		if ack.IssuerID != root || ack.Digest != prior.Digest || ack.Consumer != wanted[i] || ack.At.IsZero() || ack.At.Before(prior.ThisUpdate) || !ack.At.Before(prior.NextUpdate) || ack.At.After(now) {
			return managedCoreObservationBlock("paired-prior-ack-identity-time")
		}
	}
	return nil
}

// Only the same exact roster may advance its latest genuine native ACK time.
// The paired snapshot, revocations, custody, cache and original identities stay
// immutable. After MQTT #2 import its continuity is checked by that adapter;
// this guard then retains only the non-target TLS#1 evidence.
func managedLatePairedCRLPriorMatches(expected, current *managedLatePairedCRLPriorEvidence, afterImport bool) bool {
	if expected == nil || current == nil || expected.Schema != managedLatePairedCRLPriorSchema || current.Schema != expected.Schema || !environmentPreflightDigest.MatchString(expected.SourceSHA) || expected.SourceSHA != current.SourceSHA || expected.CoreSourceSHA != current.CoreSourceSHA || expected.AcceptedCoreSHA != current.AcceptedCoreSHA || len(expected.Roots) != 2 {
		return false
	}
	ids := []string{"14c1a269-e32e-4eac-aa93-db5ad8e85f7e", "734487b8-abcf-40be-a813-8b52c64f7954"}
	if afterImport {
		ids = ids[1:]
	}
	if len(current.Roots) != len(ids) {
		return false
	}
	for _, id := range ids {
		before, ok := expected.Roots[id]
		if !ok || before.RootID != id || before.OriginalRecord.Number != "1" {
			return false
		}
		actual, ok := current.Roots[id]
		if !ok || len(before.ACKs) != 2 || len(actual.ACKs) != 2 {
			return false
		}
		normalized := actual
		normalized.ACKs = slices.Clone(actual.ACKs)
		for i, ack := range actual.ACKs {
			old := before.ACKs[i]
			if ack.IssuerID != old.IssuerID || ack.Digest != old.Digest || ack.Consumer != old.Consumer || ack.At.Before(old.At) {
				return false
			}
			normalized.ACKs[i].At = old.At
		}
		if deploymentplan.Digest(before) != deploymentplan.Digest(normalized) {
			return false
		}
	}
	return true
}

func verifyManagedLatePairedCRLPrior(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery, expected *managedLatePairedCRLPriorEvidence, afterImport bool) error {
	p := createFoundationRecoveryPlan{Materials: map[string]string{}}
	source, err := authenticateManagedLateObservationSource(cfg, store, e, r, &p, late, managedLateSessionMQTTRepair)
	if err != nil {
		return err
	}
	originals, err := managedLatePairedOriginalRecords(store, e, source, late.Mode)
	if err != nil {
		return err
	}
	var actual *managedLatePairedCRLPriorEvidence
	if !afterImport {
		records, _, err := readManagedLateRegistryCRLs(ctx, cfg, e, b, source, managedLateObserveMQTTPrior, originals)
		if err != nil {
			return err
		}
		actual, err = observeManagedLatePairedCRLPrior(ctx, cfg, store, e, b, source, late, records)
		if err != nil {
			return err
		}
	} else {
		// A completed MQTT import must not be observed as its old Root#1 again.
		root := *source.OpenBaoRoot
		native, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, root.ID)
		if err != nil {
			return err
		}
		record := managedLateCRLRecord{IssuerID: native.Prior.IssuerID, Digest: native.Prior.Digest, Number: native.Prior.Number, PEM: native.Prior.PEM, ThisUpdate: native.Prior.ThisUpdate, NextUpdate: native.Prior.NextUpdate}
		item, err := observeManagedLateRootPrior(ctx, cfg, store, e, b, source, late, root, originals[root.ID], record)
		if err != nil {
			return err
		}
		actual = &managedLatePairedCRLPriorEvidence{Schema: managedLatePairedCRLPriorSchema, SourceSHA: late.SourceSHA, CoreSourceSHA: late.CoreSourceSHA, AcceptedCoreSHA: late.AcceptedCore.SHA256(), Roots: map[string]managedLateRootPriorEvidence{root.ID: item}}
	}
	if !managedLatePairedCRLPriorMatches(expected, actual, afterImport) {
		return managedCoreObservationBlock("paired-original-evidence-changed")
	}
	return e.Check(ctx)
}

// Artifact readers authenticate local receipts/custody and source declarations;
// native cache, revocations and current freshness are rechecked before import.
func validateManagedLatePairedCRLPriorSource(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery, proof *managedLatePairedCRLPriorEvidence) error {
	if e == nil || late == nil || late.Mode != managedLateSessionMQTTRepair || late.AcceptedCore == nil || r == nil || r.MQTTRoot == nil || r.MQTTIntermediate == nil || r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil || r.ManagedCorePartial == nil || r.ManagedCorePartial.Apps == nil || proof == nil || proof.Schema != managedLatePairedCRLPriorSchema || len(proof.Roots) != 2 || proof.SourceSHA != late.SourceSHA || deploymentplan.Digest(e.Journal) != late.SourceSHA || proof.CoreSourceSHA != late.CoreSourceSHA || proof.AcceptedCoreSHA != late.AcceptedCore.SHA256() || !environmentPreflightDigest.MatchString(proof.SourceSHA) || !environmentPreflightDigest.MatchString(proof.CoreSourceSHA) {
		return managedCoreObservationBlock("paired-exact-source-evidence")
	}
	originals, err := managedLatePairedOriginalRecords(store, e, r, late.Mode)
	if err != nil {
		return err
	}
	accepted := managedLatePairedAcceptedOwners(r, late)
	for _, root := range []environmentCreateRegisteredIssuer{*r.MQTTRoot, *r.OpenBaoRoot} {
		item, ok := proof.Roots[root.ID]
		ica := *r.MQTTIntermediate
		roster := []string{"video-cloud-api", "video-cloud-logingester"}
		container := "app"
		if root.Domain == "openbao_tls" {
			ica = *r.OpenBaoIntermediate
			roster = []string{"certissuer", "pki-controller"}
		}
		cert, parseErr := parseFirstCertificatePEM([]byte(ica.Certificate))
		if !ok || parseErr != nil || item.RootID != root.ID || item.RootSHA != root.RootFingerprint || item.IntermediateID != ica.ID || item.IntermediateSHA != environmentPreflightBytesSHA256(cert.Raw) || deploymentplan.Digest(item.OriginalRecord) != deploymentplan.Digest(originals[root.ID]) || !environmentPreflightDigest.MatchString(item.SnapshotSHA) || !environmentPreflightDigest.MatchString(item.RevocationsSHA) || !environmentPreflightDigest.MatchString(item.CustodySHA) || len(item.Consumers) != 2 || validateManagedLatePriorACKRoster(item.ACKs, root.ID, item.OriginalRecord, roster, time.Now().UTC()) != nil {
			return managedCoreObservationBlock("paired-source-root-record-inventory")
		}
		if item.ICARecord.IssuerID != ica.ID || root.Domain == "openbao_tls" && item.ICARecord.Number != "3" {
			return managedCoreObservationBlock("paired-source-original-ica")
		}
		// Offline artifact readback proves signature only, never current readiness.
		if _, err := validateManagedLateSignedCRL(ica.Domain, ica.ID, item.IntermediateSHA, ica.Status, ica.ChainPEM, item.ICARecord, managedLateObserveACKReady, item.ICARecord.ThisUpdate); err != nil {
			return managedCoreObservationBlock("paired-source-original-ica-signature")
		}
		var custody map[string]string
		if root.Domain == "mqtt" {
			custody, err = createMQTTRootCRLMaterial(store, e.Journal.PlanSHA256, root)
		} else {
			custody, err = createOpenBaoTLSRootCRLMaterial(store, e.Journal.PlanSHA256, root)
		}
		if err != nil || item.CustodySHA != deploymentplan.Digest(custody) {
			return managedCoreObservationBlock("paired-source-original-custody")
		}
		if root.Domain == "openbao_tls" {
			cm := accepted["ConfigMap/"+e.Journal.Stack+"-video-cloud/pki-create-openbao-tls-consumer-trust"]
			if cm.UID == "" || validateOpenBaoTLSRootCRLManifest(cm, root, ica) != nil {
				return managedCoreObservationBlock("paired-source-original-tls-manifest")
			}
		}
		for i, target := range item.Consumers {
			res := accepted["Deployment/"+e.Journal.Stack+"-video-cloud/"+roster[i]]
			paths := []string{"/var/lib/mqtt-trust/root-crl.json"}
			volume := "mqtt-bundle-state"
			if root.Domain == "openbao_tls" {
				paths = []string{"/var/lib/pki-service/private/crl/openbao-root.json", "/var/lib/pki-service/private/crl/openbao-root-" + ica.ID + ".json"}
				volume = "service-state"
				container = roster[i]
			}
			volumes, volErr := environmentPKINamedFields(environmentPKIPod(res.Desired)["volumes"])
			claim := deploymentplan.String(deploymentplan.Map(volumes[volume]["persistentVolumeClaim"])["claimName"])
			if res.UID == "" || target.ID != res.ID || target.UID != res.UID || deploymentplan.Digest(target.Desired) != deploymentplan.Digest(res.Desired) || target.Container != container || !environmentBootstrapOwnerUUID.MatchString(target.PodUID) || target.PodName == "" || !environmentBootstrapOwnerUUID.MatchString(target.ClaimUID) || volErr != nil || claim == "" || target.ClaimID != "PersistentVolumeClaim/"+e.Journal.Stack+"-video-cloud/"+claim || !slices.Equal(target.Paths, paths) || len(target.StateSHA) != len(paths) {
				return managedCoreObservationBlock("paired-source-original-cache-owner")
			}
			for _, path := range paths {
				if !environmentPreflightDigest.MatchString(target.StateSHA[path]) {
					return managedCoreObservationBlock("paired-source-original-cache-hash")
				}
			}
			if claimOwner, ok := accepted[target.ClaimID]; ok && claimOwner.UID != target.ClaimUID {
				return managedCoreObservationBlock("paired-source-original-cache-PVC")
			}
		}
	}
	return nil
}

// The actually accepted restored Core supersedes overlapping historical Apps
// declarations, including the genuine formal CertIssuer role-only image.
func managedLatePairedAcceptedOwners(r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) map[string]deploymentplan.Resource {
	out := map[string]deploymentplan.Resource{}
	for _, stage := range []struct {
		p *deploymentplan.Plan
		j deploymentplan.Journal
	}{{r.ManagedCorePartial.Apps, r.ManagedCorePartial.AppsJournal}, {late.AcceptedCore, late.AcceptedCoreJournal}} {
		for _, res := range stage.p.Resources {
			res.UID = stage.j.Owners[res.ID]
			out[res.ID] = res
		}
	}
	return out
}

// Preserve only known public verifier stage IDs. Native errors and local file
// errors can contain credentials or sensitive paths and are never forwarded.
func managedLatePairedDependencyError(domain, dependency string, err error) error {
	if domain != "mqtt" && domain != "openbao_tls" || dependency != "targets" && dependency != "custody" {
		return managedCoreObservationBlock("paired-unknown-dependency")
	}
	detail := "native-or-private-read"
	var blocker *deploymentplan.Blocker
	if errors.As(err, &blocker) {
		_, suffix, found := strings.Cut(blocker.Cause, "stage=")
		allowed := []string{"source-owner-plans", "accepted-controller/shared-CM", "one-current-owned-Pod", "owned-Pod-UID", "accepted-native-consumer", "native-app-container", "original-CRL-mount", "original-consumer-claim", "genuine-signed-root-cache", "exact-original-core", "actual-completed-Core-owners", "original-native-ICA3-lineage", "original-root-signed-ICA", "genuine-latest-ICA3", "unchanged-original-manifest", "one-genuine-owned-Pod", "original-owned-Pod-UID", "native-original-provider-consumer", "original-private-state-PVC-mount", "original-claim-and-Pod", "original-root-cache", "signed-root-cache", "original-ICA3-cache", "signed-ICA3-cache", "exact-two-native-consumer-owners", "exact-two-native-consumer-owners-deployment-read", "exact-two-native-consumer-owners-deployment-owner", "exact-two-native-consumer-owners-deployment-desired", "exact-two-native-consumer-owners-claim-read", "exact-two-native-consumer-owners-claim-owner", "exact-two-native-consumer-owners-pod-read", "exact-two-native-consumer-owners-pod-owner", "actual-native-provider-consumer-Ready", "owned-regular0600-cache", "original-mqtt-custody", "custody-parent", "original-openbao-tls-custody"}
		if found && slices.Contains(allowed, suffix) {
			detail = suffix
		}
	}
	return managedCoreObservationBlock("paired-" + domain + "-" + dependency + "-" + detail)
}
