package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

const createAppRootCRLSchema = "rtk-cloud.create-app-root-crl-recovery/v1"
const managedLateAppRootID = "76d5733a-5cf6-4e37-806e-58a342877656"
const managedLateAppRootPriorSHA = "feee934ce3ecc748214f0812773cf3b870c9809f94fc812cfe79551f6da5e1a6"

func appRootCRLError(stage string) error {
	return createProviderRecoveryBlock("original App Root CRL repair requires exact original custody, authenticated human import and genuine two-consumer proof: stage=" + stage)
}

type createAppRootCRLPlan struct {
	QualifiedAt                                                                                          time.Time
	Schema, Parent, SourceJournalSHA, SourceOwnerSHA, CheckerSHA, ConfigurationSHA, KubeconfigSHA, Image string
	Configuration                                                                                        deploymentConfig
	Generation                                                                                           environmentCreatePlan
	PostSource                                                                                           createManagedLatePostMint
	Root                                                                                                 environmentCreateRegisteredIssuer
	Intermediate                                                                                         environmentCreateRegisteredIssuer
	IntermediateCRL                                                                                      createRootCRLRecord
	Operator                                                                                             initialOperatorBinding
	Snapshot                                                                                             createRootCRLNativeSnapshot
	Request                                                                                              createRootCRLRequest
	Signed                                                                                               createRootCRLRecord
	Candidate                                                                                            string
	Material, NativeEvidence                                                                             map[string]string
	Controller, ConfigMap                                                                                deploymentplan.Resource
	Consumers                                                                                            []createRootCRLStateTarget
	OriginalVolumes, ConsumerVolumes                                                                     map[string]environmentCreateVolumeOwner
}
type createAppRootCRLArtifact struct {
	Schema                  string
	Plan                    createAppRootCRLPlan
	Qualification           deploymentplan.Qualification
	QualifiedAt, ValidUntil time.Time
}
type createAppRootCRLCompletion struct {
	Schema, Parent, Review, SourceSHA, FinalSHA, EvidenceSHA string
	SourceEntryCount, FinalEntryCount                        int
	Completed                                                bool
	NativeEvidence                                           map[string]string
	CompletionReview                                         string `json:",omitempty"`
}

func parseManagedLateAppRootCRL(root environmentCreateRegisteredIssuer, record createRootCRLRecord, fresh bool, now time.Time) (*x509.RevocationList, error) {
	block, rest := pem.Decode([]byte(record.PEM))
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if root.Environment != "dev" || root.Domain != "app" || root.Kind != "root" || root.Status != "active" || root.Document["signer_provider"] != "offline" || err != nil || cert == nil || cert.CheckSignatureFrom(cert) != nil || environmentPreflightBytesSHA256(cert.Raw) != root.RootFingerprint {
		return nil, appRootCRLError("signed-app-root")
	}
	if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 || err != nil {
		return nil, appRootCRLError("signed-app-root")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.CheckSignatureFrom(cert) != nil || !bytes.Equal(crl.RawIssuer, cert.RawSubject) || !bytes.Equal(crl.AuthorityKeyId, cert.SubjectKeyId) || len(cert.SubjectKeyId) == 0 || record.IssuerID != root.ID || record.Digest != environmentPreflightBytesSHA256(crl.Raw) || crl.Number == nil || crl.Number.Sign() <= 0 || crl.Number.BitLen() > 159 || record.Number != crl.Number.String() || !record.ThisUpdate.Equal(crl.ThisUpdate) || !record.NextUpdate.Equal(crl.NextUpdate) {
		return nil, appRootCRLError("signed-app-root")
	}
	if fresh && (crl.ThisUpdate.After(now) || crl.ThisUpdate.Before(now.Add(-7*24*time.Hour)) || !crl.NextUpdate.After(now.Add(45*time.Minute)) || crl.NextUpdate.After(crl.ThisUpdate.Add(7*24*time.Hour)) || crl.NextUpdate.After(cert.NotAfter) || now.Before(cert.NotBefore)) {
		return nil, appRootCRLError("signed-app-root")
	}
	for _, ext := range crl.Extensions {
		if ext.Id.String() != "2.5.29.20" && ext.Id.String() != "2.5.29.35" {
			return nil, appRootCRLError("signed-app-root")
		}
	}
	seen := map[string]bool{}
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber == nil || entry.SerialNumber.Sign() <= 0 || entry.RevocationTime.IsZero() || entry.RevocationTime.After(crl.ThisUpdate) || entry.ReasonCode < 0 || entry.ReasonCode > 10 || entry.ReasonCode == 7 || entry.ReasonCode == 8 || seen[entry.SerialNumber.Text(16)] {
			return nil, appRootCRLError("signed-app-root")
		}
		seen[entry.SerialNumber.Text(16)] = true
		for _, ext := range entry.Extensions {
			if ext.Id.String() != "2.5.29.21" {
				return nil, appRootCRLError("signed-app-root")
			}
		}
	}
	return crl, nil
}

func buildManagedLateAppRootCRLRequest(root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, now time.Time) (createRootCRLRequest, error) {
	var out createRootCRLRequest
	prior, err := parseManagedLateAppRootCRL(root, snapshot.Prior, false, now)
	if err != nil {
		return out, err
	}
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		return out, err
	}
	now = now.UTC().Truncate(time.Second)
	entries := map[string]createRootCRLRevocation{}
	for _, entry := range prior.RevokedCertificateEntries {
		entries[entry.SerialNumber.Text(16)] = createRootCRLRevocation{entry.SerialNumber.Text(16), entry.RevocationTime, entry.ReasonCode}
	}
	include := func(entry createRootCRLRevocation) error {
		serial, ok := new(big.Int).SetString(entry.Serial, 16)
		entry.RevokedAt = entry.RevokedAt.UTC().Truncate(time.Second)
		if !ok || serial.Sign() <= 0 || serial.Text(16) != entry.Serial || entry.RevokedAt.IsZero() || entry.RevokedAt.After(now) || entry.Reason < 0 || entry.Reason > 10 || entry.Reason == 7 || entry.Reason == 8 {
			return appRootCRLError("complete-native-revocations")
		}
		if old, ok := entries[entry.Serial]; ok {
			// Native unspecified reason does not remove an already published reason.
			if !old.RevokedAt.Equal(entry.RevokedAt) || entry.Reason != 0 && old.Reason != entry.Reason {
				return appRootCRLError("complete-native-revocations")
			}
			return nil
		}
		entries[entry.Serial] = entry
		return nil
	}
	for _, entry := range snapshot.Bindings {
		if err := include(entry); err != nil {
			return out, err
		}
	}
	for _, entry := range snapshot.Certificates {
		leaf, err := parseFirstCertificatePEM([]byte(entry.PEM))
		if err != nil || leaf.CheckSignatureFrom(cert) != nil {
			return out, appRootCRLError("complete-native-revocations")
		}
		if err := include(createRootCRLRevocation{Serial: leaf.SerialNumber.Text(16), RevokedAt: entry.RevokedAt}); err != nil {
			return out, err
		}
	}
	serials := make([]string, 0, len(entries))
	for serial := range entries {
		serials = append(serials, serial)
	}
	slices.Sort(serials)
	revoked := make([]createRootCRLRevocation, 0, len(serials))
	for _, serial := range serials {
		revoked = append(revoked, entries[serial])
	}
	next := now.Add(6 * 24 * time.Hour)
	if next.After(cert.NotAfter) {
		return out, appRootCRLError("complete-native-revocations")
	}
	out = createRootCRLRequest{root.ID, root.RootFingerprint, new(big.Int).Add(prior.Number, big.NewInt(1)).String(), now, next, revoked}
	return out, nil
}

func validateManagedLateAppRootCRLSigned(root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, request createRootCRLRequest, raw []byte, now time.Time) (createRootCRLRecord, error) {
	var record createRootCRLRecord
	expected, unionErr := buildManagedLateAppRootCRLRequest(root, snapshot, request.ThisUpdate)
	if unionErr != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(request) {
		return record, appRootCRLError("reviewed-complete-union")
	}
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return record, appRootCRLError("reviewed-signed-app-root")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.Number == nil {
		return record, appRootCRLError("reviewed-signed-app-root")
	}
	record = createRootCRLRecord{root.ID, environmentPreflightBytesSHA256(crl.Raw), crl.Number.String(), string(raw), crl.ThisUpdate, crl.NextUpdate}
	if _, err = parseManagedLateAppRootCRL(root, record, true, now); err != nil {
		return record, err
	}
	if request.IssuerID != root.ID || request.Fingerprint != root.RootFingerprint || record.Number != request.Number || !record.ThisUpdate.Equal(request.ThisUpdate) || !record.NextUpdate.Equal(request.NextUpdate) || len(crl.RevokedCertificateEntries) != len(request.Entries) {
		return record, appRootCRLError("reviewed-signed-app-root")
	}
	wanted := map[string]createRootCRLRevocation{}
	for _, entry := range request.Entries {
		if _, ok := wanted[entry.Serial]; ok {
			return record, appRootCRLError("reviewed-signed-app-root")
		}
		wanted[entry.Serial] = entry
	}
	for _, entry := range crl.RevokedCertificateEntries {
		reviewed, ok := wanted[entry.SerialNumber.Text(16)]
		if !ok || !entry.RevocationTime.Equal(reviewed.RevokedAt) || entry.ReasonCode != reviewed.Reason {
			return record, appRootCRLError("reviewed-signed-app-root")
		}
		delete(wanted, entry.SerialNumber.Text(16))
	}
	prior, err := parseManagedLateAppRootCRL(root, snapshot.Prior, false, now)
	if err != nil || crl.Number.Cmp(prior.Number) <= 0 || crl.ThisUpdate.Before(prior.ThisUpdate) {
		return record, appRootCRLError("reviewed-signed-app-root")
	}
	for _, old := range prior.RevokedCertificateEntries {
		found := false
		for _, entry := range crl.RevokedCertificateEntries {
			if entry.SerialNumber.Cmp(old.SerialNumber) == 0 && entry.RevocationTime.Equal(old.RevocationTime) && entry.ReasonCode == old.ReasonCode {
				found = true
			}
		}
		if !found {
			return record, appRootCRLError("reviewed-signed-app-root")
		}
	}
	return record, nil
}

var createAppRootCRLGates = []string{"deployment.app-root-crl.original-authority", "deployment.app-root-crl.live-revocations", "deployment.app-root-crl.native-signature", "deployment.app-root-crl.original-operator", "deployment.app-root-crl.original-consumers", "deployment.app-root-crl.execution-lease"}

type createAppRootCRLReceipt struct {
	Schema, Parent, Review, File, Digest, EvidenceSHA string
	Completed                                         bool
}

func appRootCRLBase(review string) string {
	return filepath.Join("deployment/create/app-root-crl-recovery", review)
}
func appRootCRLPointer(parent string) string {
	return filepath.Join("deployment/create/material", parent, "app-root-crl-renewal.json")
}
func appRootCRLImportIntent(p createAppRootCRLPlan) map[string]string {
	return map[string]string{"issuer_id": p.Root.ID, "root_sha256": p.Root.RootFingerprint, "prior_crl_sha256": p.Snapshot.Prior.Digest, "signed_crl_sha256": p.Signed.Digest, "request_sha256": deploymentplan.Digest(p.Request), "operator_binding_sha256": deploymentplan.Digest(p.Operator)}
}
func appRootCRLRuntimeIntent(p createAppRootCRLPlan, t createRootCRLStateTarget) deploymentplan.Object {
	return deploymentplan.Object{"target": t, "native_crl_sha256": p.Signed.Digest}
}
func appRootCRLReceipt(a createAppRootCRLArtifact, evidence map[string]string) createAppRootCRLReceipt {
	return createAppRootCRLReceipt{createAppRootCRLSchema, a.Plan.Parent, deploymentplan.Digest(a.Plan), filepath.ToSlash(filepath.Join(a.Plan.Candidate, "signed/revocations.pem")), a.Plan.Signed.Digest, deploymentplan.Digest(evidence), true}
}

func managedLateAppRootCRLProgressValid(a createAppRootCRLArtifact, source, current environmentCreateJournal, complete bool) bool {
	p := a.Plan
	review := deploymentplan.Digest(p)
	n := len(source.Entries)
	if a.Schema != createAppRootCRLSchema+"/artifact" || p.Schema != createAppRootCRLSchema || p.Parent != source.PlanSHA256 || p.SourceJournalSHA != deploymentplan.Digest(source) || current.PlanSHA256 != source.PlanSHA256 || current.OwnerToken != source.OwnerToken || len(current.Entries) < n || len(current.Entries) > n+4 || len(current.KubernetesLeases) < len(source.KubernetesLeases) || len(current.KubernetesLeases) > len(source.KubernetesLeases)+1 || !a.QualifiedAt.Equal(p.QualifiedAt) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != 6 {
		return false
	}
	for i, id := range createAppRootCRLGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || g.Status != "PASS" || !g.Required || g.EvidenceSHA256 != review {
			return false
		}
	}
	prefix := current
	prefix.Entries = current.Entries[:n]
	prefix.KubernetesLeases = current.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) {
		return false
	}
	if len(current.Entries) == n {
		return !complete && len(current.KubernetesLeases) == len(source.KubernetesLeases)
	}
	top := current.Entries[n]
	key := "app-root-crl-renewal-" + review
	if top.Key != key || top.Phase != "bootstrap" || top.IntentSHA256 != activeTransportJournalDigest(p) || top.StartedAt.IsZero() || top.StartedAt.Before(a.QualifiedAt) || !top.StartedAt.Before(a.ValidUntil) || top.StartedAt.After(time.Now().UTC()) || top.State != "pending" && top.State != "allocated" && top.State != "completed" {
		return false
	}
	if len(current.KubernetesLeases) == len(source.KubernetesLeases)+1 {
		lease := current.KubernetesLeases[len(source.KubernetesLeases)]
		if !managedLateLeaseMatches(lease, source.Stack, rootCRLLeasePlan(p.Configuration, review).SHA256()) {
			return false
		}
	} else if len(current.Entries) > n+1 {
		return false
	}
	if top.State == "pending" && (!top.CompletedAt.IsZero() || len(top.Receipt) != 0) || top.State == "allocated" && !top.CompletedAt.IsZero() || top.State == "completed" && (top.CompletedAt.IsZero() || top.CompletedAt.Before(top.StartedAt) || top.CompletedAt.After(time.Now().UTC())) {
		return false
	}
	evidence := map[string]string{}
	previous := top.StartedAt
	for i, entry := range current.Entries[n+1:] {
		if i > 2 || entry.Phase != "bootstrap" || entry.StartedAt.IsZero() || entry.StartedAt.Before(previous) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
			return false
		}
		var intent any
		expectedKey := key + "/native-import"
		if i == 0 {
			intent = appRootCRLImportIntent(p)
		} else {
			if len(p.Consumers) != 2 {
				return false
			}
			target := p.Consumers[i-1]
			expectedKey = key + "/native-runtime-" + deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
			intent = appRootCRLRuntimeIntent(p, target)
		}
		if entry.Key != expectedKey || entry.IntentSHA256 != activeTransportJournalDigest(intent) || entry.StartedAt.After(time.Now().UTC()) {
			return false
		}
		if entry.State == "pending" {
			if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() || n+i+2 != len(current.Entries) {
				return false
			}
			continue
		}
		if entry.State == "completed" && (entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC())) || entry.State == "allocated" && (!entry.CompletedAt.IsZero() || n+i+2 != len(current.Entries)) {
			return false
		}
		if i == 0 {
			expected := deploymentplan.Object{"native_crl": p.Signed, "request_sha256": deploymentplan.Digest(p.Request), "operator_binding_sha256": deploymentplan.Digest(p.Operator)}
			if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(expected) {
				return false
			}
		} else {
			var observed map[string]string
			if decodeEnvironmentJSON(entry.Receipt, &observed) != nil || !managedLateAppRootCRLConsumerEvidenceValid(p, p.Consumers[i-1], observed) {
				return false
			}
			for k, v := range observed {
				if !environmentPreflightDigest.MatchString(v) || evidence[k] != "" && evidence[k] != v {
					return false
				}
				evidence[k] = v
			}
		}
		if entry.State == "completed" {
			previous = entry.CompletedAt
		} else if n+i+2 < len(current.Entries) {
			return false
		}
	}
	if top.State != "pending" {
		if len(current.Entries) != n+4 || activeTransportJournalDigest(jsonRawObject(top.Receipt)) != activeTransportJournalDigest(appRootCRLReceipt(a, evidence)) {
			return false
		}
		if top.State == "completed" && top.CompletedAt.Before(previous) {
			return false
		}
	}
	return !complete || len(current.Entries) == n+4 && top.State == "completed" && current.Entries[n+1].State == "completed" && current.Entries[n+2].State == "completed" && current.Entries[n+3].State == "completed"
}

func appRootCRLMaterial(store secretStore, parent string, root environmentCreateRegisteredIssuer) (map[string]string, error) {
	paths := []string{filepath.Join("pki/custody/roots", root.ID, "manifest.json"), filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem"), filepath.Join("pki/custody/roots", root.ID, "certificate.pem"), filepath.Join("pki/custody/roots", root.ID, "csr.pem"), "pki/custody/app-root-v1-key-passphrase", filepath.Join("deployment/create/material", parent, "operator-binding.json")}
	m := map[string]string{}
	for _, path := range paths {
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path), true)
		if err != nil {
			return nil, appRootCRLError("original-custody-and-operator")
		}
		m[path] = environmentPreflightBytesSHA256(raw)
	}
	return m, nil
}
func validateManagedLateAppRootCRLSource(store secretStore, e *environmentCreateExecution, p createAppRootCRLPlan) (*createServiceSessionRecovery, error) {
	return validateManagedLateAppRootCRLSourceContent(store, e, p, false)
}
func validateManagedLateAppRootCRLSourceContent(store secretStore, e *environmentCreateExecution, p createAppRootCRLPlan, historical bool) (*createServiceSessionRecovery, error) {
	if e == nil || p.Schema != createAppRootCRLSchema || p.Parent != e.Journal.PlanSHA256 || p.SourceOwnerSHA != deploymentplan.Digest(e.owner) || p.SourceJournalSHA != deploymentplan.Digest(e.Journal) || p.Root.ID != managedLateAppRootID || p.Snapshot.Prior.Digest != managedLateAppRootPriorSHA || p.Snapshot.Prior.Number != "1" || p.Root.Domain != "app" || p.Root.Document["signer_provider"] != "offline" || p.PostSource.InstallationClock != nil {
		return nil, appRootCRLError("original-source")
	}
	if !historical {
		if err := managedLateInstallationClockAbsent(store, p.Parent); err != nil {
			return nil, err
		}
	}
	r, post, err := loadManagedLatePostMintSourceContent(p.Configuration, store, e, historical)
	if err != nil {
		return nil, fmt.Errorf("authenticate original App post-mint history: %w", err)
	}
	if managedLatePostMintDigest(post) != managedLatePostMintDigest(&p.PostSource) {
		return nil, appRootCRLError("original-App-post-source")
	}
	if deploymentplan.Digest(r.ManagedTransportRecovery.AppRoot) != deploymentplan.Digest(p.Root) {
		return nil, appRootCRLError("original-App-root-authority")
	}
	if deploymentplan.Digest(r.ManagedTransportRecovery.AppIntermediate) != deploymentplan.Digest(p.Intermediate) {
		return nil, appRootCRLError("original-App-intermediate-authority")
	}
	var initial createRootCRLRecord
	for _, entry := range e.Journal.Entries {
		if entry.Key == "pki-initial-root-crl-app" && entry.State == "completed" {
			var receipt struct {
				File     string `json:"crl_file"`
				SHA      string `json:"crl_pem_sha256"`
				IssuerID string `json:"issuer_id"`
			}
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.IssuerID != p.Root.ID || entry.Phase != "bootstrap" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.IntentSHA256 != activeTransportJournalDigest(deploymentplan.Object{"issuer_id": p.Root.ID, "root_sha256": p.Root.RootFingerprint, "crl_number": "1"}) {
				return nil, appRootCRLError("initial-App-receipt")
			}
			if receipt.File != filepath.ToSlash(filepath.Join("deployment/create", p.Parent, "bootstrap/app/root-crl/revocations.pem")) {
				return nil, appRootCRLError("canonical-initial-App-crl-path")
			}
			raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, receipt.File), true)
			if err != nil || environmentPreflightBytesSHA256(raw) != receipt.SHA {
				return nil, appRootCRLError("initial-App-bytes")
			}
			parsed, err := x509.ParseRevocationList(func() []byte {
				b, _ := pem.Decode(raw)
				if b == nil {
					return nil
				}
				return b.Bytes
			}())
			if err != nil {
				return nil, appRootCRLError("initial-App-signature")
			}
			initial = createRootCRLRecord{p.Root.ID, environmentPreflightBytesSHA256(parsed.Raw), parsed.Number.String(), string(raw), parsed.ThisUpdate, parsed.NextUpdate}
		}
	}
	if deploymentplan.Digest(initial) != deploymentplan.Digest(p.Snapshot.Prior) {
		return nil, appRootCRLError("exact-initial-App-prior")
	}
	material, err := appRootCRLMaterial(store, p.Parent, p.Root)
	if err != nil || deploymentplan.Digest(material) != deploymentplan.Digest(p.Material) {
		return nil, appRootCRLError("original-custody-drift")
	}
	var binding initialOperatorBinding
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/material", p.Parent, "operator-binding.json"), &binding) != nil || deploymentplan.Digest(binding) != deploymentplan.Digest(p.Operator) || binding.Parent != p.Parent || !environmentBootstrapOwnerUUID.MatchString(binding.UserID) || !environmentBootstrapOwnerUUID.MatchString(binding.AccountUID) {
		return nil, appRootCRLError("original-human-principal")
	}
	if !p.QualifiedAt.IsZero() {
		if err := validateManagedLateAppRootCRLSourceTargets(store, e, r, p); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func loadManagedLateAppRootCRLArtifact(store secretStore, e *environmentCreateExecution, review string) (createAppRootCRLArtifact, environmentCreateJournal, error) {
	var a createAppRootCRLArtifact
	var source environmentCreateJournal
	var owner environmentCreateOwner
	if !environmentPreflightDigest.MatchString(review) || createOpenBaoACKCompletionRead(store, filepath.Join(appRootCRLBase(review), "qualification.json"), &a) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(appRootCRLBase(review), "source-journal.json"), &source) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(appRootCRLBase(review), "source-owner.json"), &owner) != nil || deploymentplan.Digest(a.Plan) != review || a.Plan.SourceOwnerSHA != deploymentplan.Digest(owner) || deploymentplan.Digest(owner) != deploymentplan.Digest(e.owner) || !managedLateAppRootCRLProgressValid(a, source, source, false) {
		return a, source, appRootCRLError("qualified-artifact")
	}
	if !appRootCRLCandidatePathValid(a.Plan.Candidate) {
		return a, source, appRootCRLError("canonical-candidate-path")
	}
	clock, clockErr := readManagedLateInstallationClock(store, e.Journal.PlanSHA256)
	if clockErr != nil {
		return a, source, clockErr
	}
	historical := false
	if clock != nil {
		if !managedLateAppRootCRLHistoricalClockAllowed(a, source, e.Journal, clock) {
			return a, source, appRootCRLError("pending-App-cannot-follow-installation-clock")
		}
		bounded := e.Journal
		bounded.Entries = slices.Clone(e.Journal.Entries[:len(source.Entries)+4])
		bounded.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases[:len(source.KubernetesLeases)+1])
		if err := validateManagedLateAppRootCRLCompletion(store, a, source, bounded); err != nil {
			return a, source, err
		}
		if err := validateManagedLateAppRootCRLInstallationHistory(store, e, a, bounded, clock); err != nil {
			return a, source, err
		}
		historical = true
	}
	pure := *e
	pure.Journal = source
	if _, err := validateManagedLateAppRootCRLSourceContent(store, &pure, a.Plan, historical); err != nil {
		return a, source, err
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, a.Plan.Candidate, "signed/revocations.pem"), true)
	signed, signErr := validateManagedLateAppRootCRLSigned(a.Plan.Root, a.Plan.Snapshot, a.Plan.Request, raw, a.QualifiedAt)
	if err != nil || signErr != nil || deploymentplan.Digest(signed) != deploymentplan.Digest(a.Plan.Signed) {
		return a, source, appRootCRLError("qualified-signed-bytes")
	}
	return a, source, nil
}

// Completed App history may be read after installation begins only through
// that same real, clock-pinned, freshly qualified Post admission.
func validateManagedLateAppRootCRLInstallationHistory(store secretStore, e *environmentCreateExecution, a createAppRootCRLArtifact, final environmentCreateJournal, clock *createManagedLateInstallationClock) error {
	index := len(final.Entries)
	if clock == nil || len(e.Journal.Entries) <= index {
		return appRootCRLError("historical-App-installation-admission")
	}
	artifact, captured, err := loadAdmittedCreateRecovery(store, e, clock.Review)
	var post createManagedLatePostMint
	if err != nil || artifact.Plan.Schema != managedLatePostMintSchema || deploymentplan.Digest(captured) != deploymentplan.Digest(final) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", clock.Review, managedLatePostMintSourceFilename), &post) != nil || post.Review != clock.Review || post.OriginalReview != a.Plan.PostSource.OriginalReview || post.SourceSHA != deploymentplan.Digest(final) || post.SourceOwnerSHA != a.Plan.SourceOwnerSHA || post.AppRootReview != deploymentplan.Digest(a.Plan) || post.AppRootArtifactSHA != deploymentplan.Digest(a) || post.Candidate == nil {
		return appRootCRLError("historical-App-installation-source")
	}
	var proof createAppRootCRLCompletion
	if createOpenBaoACKCompletionRead(store, appRootCRLPointer(a.Plan.Parent), &proof) != nil || post.AppRootCompletionSHA != deploymentplan.Digest(proof) {
		return appRootCRLError("historical-App-installation-completion")
	}
	for key, value := range managedLatePostMintPins(&post) {
		if artifact.Plan.Materials[managedLatePostMintMaterial+key] != value {
			return appRootCRLError("historical-App-installation-material")
		}
	}
	validated, err := validateManagedLateInstallationAdmission(store, e, &post, artifact, e.Journal.Entries[index])
	if err != nil || deploymentplan.Digest(validated) != deploymentplan.Digest(clock) {
		return appRootCRLError("historical-App-installation-clock")
	}
	return nil
}
func validateManagedLateAppRootCRLSourceTargets(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, p createAppRootCRLPlan) error {
	owners, err := managedLateAppRootCRLSourceOwners(r, &p)
	ns := e.Journal.Stack + "-video-cloud"
	if err != nil || deploymentplan.Digest(owners["Deployment/"+ns+"/pki-controller"]) != deploymentplan.Digest(p.Controller) || deploymentplan.Digest(owners["ConfigMap/"+ns+"/pki-create-app-trust"]) != deploymentplan.Digest(p.ConfigMap) || len(p.Consumers) != 2 {
		return appRootCRLError("qualified-original-consumer-authority")
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, r.ManagedLateSession.Review)
	volumeMaterial := "accepted-service-core-managed-complete-current-native-volumes"
	if err != nil || !environmentPreflightDigest.MatchString(p.NativeEvidence[volumeMaterial]) || original.Plan.Materials[volumeMaterial] != p.NativeEvidence[volumeMaterial] {
		return appRootCRLError("qualified-original-volume-admission")
	}
	volumes, consumerVolumes, err := readManagedLateAppRootCRLVolumes(e, p.Consumers, p.NativeEvidence)
	if err != nil || deploymentplan.Digest(volumes) != deploymentplan.Digest(p.OriginalVolumes) || deploymentplan.Digest(consumerVolumes) != deploymentplan.Digest(p.ConsumerVolumes) {
		return appRootCRLError("qualified-original-volume-body")
	}
	for index, name := range []string{"video-cloud-api-app-pki", "mqtt-pki"} {
		expected, targetErr := managedLateAppRootCRLTargetBinding(p.Root, p.Intermediate, p.ConfigMap, owners["Deployment/"+ns+"/"+name])
		actual := p.Consumers[index]
		if targetErr != nil || !environmentBootstrapOwnerUUID.MatchString(actual.PodUID) || actual.PodName == "" || !environmentBootstrapOwnerUUID.MatchString(actual.ClaimUID) || len(actual.StateSHA) != 2 {
			return appRootCRLError("qualified-original-consumer-capture")
		}
		for _, path := range expected.Paths {
			if !environmentPreflightDigest.MatchString(actual.StateSHA[path]) {
				return appRootCRLError("qualified-original-cache-capture")
			}
		}
		expected.PodUID, expected.PodName, expected.ClaimUID, expected.StateSHA = actual.PodUID, actual.PodName, actual.ClaimUID, actual.StateSHA
		if deploymentplan.Digest(expected) != deploymentplan.Digest(actual) {
			return appRootCRLError("qualified-original-consumer-recipe")
		}
	}
	return nil
}

// An App repair observation is carried only by its authenticated source-bound
// plan. It authorizes historical inspection of that exact signed Root1, never
// runtime trust or ACK readiness for it.
type managedLateAppRootCRLObservationKey struct{}
type managedLateAppRootCRLObservation struct {
	Plan        createAppRootCRLPlan
	AfterImport bool
}

func managedLateAppRootCRLPrior(ctx context.Context, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, record managedLateCRLRecord) (managedLateCRLProof, bool, error) {
	observation, ok := ctx.Value(managedLateAppRootCRLObservationKey{}).(managedLateAppRootCRLObservation)
	if !ok || record.IssuerID != managedLateAppRootID {
		return managedLateCRLProof{}, false, nil
	}
	p := observation.Plan
	framed, _, frameErr := managedLateAppRootCRLFrame(store, e, false)
	if frameErr != nil {
		return managedLateCRLProof{}, true, frameErr
	}
	if _, err := validateManagedLateAppRootCRLSource(store, framed, p); err != nil {
		return managedLateCRLProof{}, true, err
	}
	if r == nil || r.ManagedTransportRecovery == nil || deploymentplan.Digest(r.ManagedTransportRecovery.AppRoot) != deploymentplan.Digest(p.Root) {
		return managedLateCRLProof{}, true, appRootCRLError("native-original-App-authority")
	}
	expected := p.Snapshot.Prior
	clock := record.ThisUpdate
	if observation.AfterImport {
		expected = p.Signed
		clock = time.Now().UTC()
	}
	actual := createRootCRLRecord{record.IssuerID, record.Digest, record.Number, record.PEM, record.ThisUpdate, record.NextUpdate}
	if deploymentplan.Digest(actual) != deploymentplan.Digest(expected) {
		return managedLateCRLProof{}, true, appRootCRLError("native-exact-App-publication")
	}
	proof, err := validateManagedLateSignedCRL("app", p.Root.ID, p.Root.RootFingerprint, p.Root.Status, p.Root.ChainPEM, record, managedLateObserveACKReady, clock)
	return proof, true, err
}

func verifyManagedLateAppRootCRLInputs(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p createAppRootCRLPlan, afterImport bool) error {
	frame, _, err := managedLateAppRootCRLFrame(store, e, false)
	if err != nil {
		return err
	}
	r, err := validateManagedLateAppRootCRLSource(store, frame, p)
	if err != nil {
		return err
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, r.ManagedLateSession.Review)
	if err != nil {
		return err
	}
	recovery := original.Plan
	recovery.Materials = map[string]string{}
	for k, v := range original.Plan.Materials {
		recovery.Materials[k] = v
	}
	ledger, ledgerErr := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if ledgerErr != nil || createOpenBaoACKCompletionRead(store, "deployment/resources/active.json", &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return appRootCRLError("canonical-original-eighteen-volumes")
	}
	recovery.Materials["accepted-service-core-managed-application-current-volume-ledger"] = active.LedgerSHA
	recovery.Materials["accepted-service-core-managed-application-current-volume-active"] = deploymentplan.Digest(active)
	scoped := context.WithValue(ctx, managedLateAppRootCRLObservationKey{}, managedLateAppRootCRLObservation{p, afterImport})
	if err := verifyManagedLateSessionCurrentNative(scoped, cfg, store, e, b, r, &recovery, r.ManagedLateSession); err != nil {
		return err
	}
	if err := verifyServiceSessionAuthentication(scoped, cfg, store, e, b, r); err != nil {
		return err
	}
	if len(p.NativeEvidence) > 0 {
		for _, key := range []string{"accepted-service-core-managed-complete-current-native-volumes", "accepted-service-core-managed-active-registry", managedLateAPICurrentMaterial, "accepted-service-core-managed-application-current-servers", "accepted-service-core-managed-application-current-clients"} {
			if !environmentPreflightDigest.MatchString(p.NativeEvidence[key]) || recovery.Materials[key] != p.NativeEvidence[key] {
				return appRootCRLError("unchanged-native-identity-pins")
			}
		}
	}
	return e.Check(ctx)
}

func appRootCRLHumanClient(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, expected initialOperatorBinding) (*environmentCreatePKIClient, error) {
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil || !recorded || deploymentplan.Digest(binding) != deploymentplan.Digest(expected) {
		return nil, appRootCRLError("original-authenticated-human-principal")
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return nil, err
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, binding.UserID)
	if err != nil {
		return nil, err
	}
	actual, err := client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"])
	if err != nil || actual != binding.UserID {
		return nil, appRootCRLError("original-human-login")
	}
	return client, nil
}
func appRootCRLNativeIssuerMatches(actual map[string]any, root environmentCreateRegisteredIssuer) bool {
	return actual["issuer_id"] == root.ID && actual["environment"] == "dev" && actual["trust_domain"] == "app" && actual["kind"] == "root" && actual["status"] == "active" && actual["certificate_fingerprint_sha256"] == root.RootFingerprint && actual["certificate_pem"] == root.Certificate && actual["signer_provider"] == "offline" && deploymentplan.String(actual["chain_pem"]) == root.ChainPEM && deploymentplan.String(actual["trust_bundle_version"]) == root.BundleVersion && deploymentplan.String(actual["signer_reference"]) == root.Reference && deploymentplan.String(actual["parent_issuer_id"]) == deploymentplan.String(root.Document["parent_issuer_id"])
}

func prepareManagedLateAppRootCRLCandidate(ctx context.Context, store secretStore, e *environmentCreateExecution, p *createAppRootCRLPlan) error {
	selector := deploymentplan.Digest(deploymentplan.Object{"schema": createAppRootCRLSchema, "parent": p.Parent, "source": p.SourceJournalSHA, "owner": p.SourceOwnerSHA, "root": p.Root, "snapshot": p.Snapshot, "custody": p.Material})
	p.Candidate = filepath.ToSlash(filepath.Join("deployment/repairs/app-root-crl", selector))
	var saved struct {
		Schema   string
		Source   string
		Root     environmentCreateRegisteredIssuer
		Snapshot createRootCRLNativeSnapshot
		Material map[string]string
		Request  createRootCRLRequest
	}
	path := filepath.Join(p.Candidate, "candidate.json")
	if _, err := os.Lstat(filepath.Join(store.Root, path)); errors.Is(err, os.ErrNotExist) {
		request, err := buildManagedLateAppRootCRLRequest(p.Root, p.Snapshot, time.Now().UTC())
		if err != nil {
			return err
		}
		p.Request = request
		saved.Schema, saved.Source, saved.Root, saved.Snapshot, saved.Material, saved.Request = createAppRootCRLSchema+"/candidate", p.SourceJournalSHA, p.Root, p.Snapshot, p.Material, p.Request
		if err := writeMQTTRootCRLImmutable(store, path, saved); err != nil {
			return err
		}
	} else if err != nil || createOpenBaoACKCompletionRead(store, path, &saved) != nil || saved.Schema != createAppRootCRLSchema+"/candidate" || saved.Source != p.SourceJournalSHA || deploymentplan.Digest(saved.Root) != deploymentplan.Digest(p.Root) || deploymentplan.Digest(saved.Snapshot) != deploymentplan.Digest(p.Snapshot) || deploymentplan.Digest(saved.Material) != deploymentplan.Digest(p.Material) {
		return appRootCRLError("stable-signed-candidate")
	}
	p.Request = saved.Request
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(p.Candidate, "request.json"), p.Request); err != nil {
		return err
	}
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(p.Candidate, "issuer.json"), p.Root.Document); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(store.Root, p.Candidate, "signed")); errors.Is(err, os.ErrNotExist) {
		if e.Check(ctx) != nil {
			return appRootCRLError("original-fence")
		}
		if err := runEnvironmentCreatePKICeremony(ctx, store, p.Image, "crl", "--issuer", "/store/"+p.Candidate+"/issuer.json", "--crl-request", "/store/"+p.Candidate+"/request.json", "--key", "/store/pki/custody/roots/"+p.Root.ID+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/app-root-v1-key-passphrase", "--expected-request-sha256", deploymentplan.Digest(p.Request), "--out", "/store/"+p.Candidate+"/signed"); err != nil {
			return err
		}
	} else if err != nil {
		return appRootCRLError("existing-candidate")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, p.Candidate, "signed/revocations.pem"), true)
	if err != nil {
		return err
	}
	p.Signed, err = validateManagedLateAppRootCRLSigned(p.Root, p.Snapshot, p.Request, raw, time.Now().UTC())
	return err
}

func runEnvironmentCreateAppRootCRLRecovery(args []string) error {
	return runEnvironmentCreateAppRootCRLRecoveryContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateAppRootCRLRecoveryContext(parentCtx context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-app-root-crl", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original parent")
	workspaceOpt := fs.String("workspace", "", "selected workspace")
	execute := fs.Bool("execute", false, "execute exact qualified App CRL refresh")
	review := fs.String("review-sha256", "", "exact fresh App review")
	confirm := fs.String("confirm", "", "original stack")
	timeout := fs.Duration("timeout", 30*time.Minute, "bounded scope")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || *timeout <= 0 || *timeout > 30*time.Minute || !*execute && *review != "" {
		return appRootCRLError("arguments")
	}
	if recoveryMutationGuard(append([]string{"deployment", "recover-create-app-root-crl"}, args...)) != nil {
		return appRootCRLError("mutation-guard")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	if *workspaceOpt != "" {
		workspace = *workspaceOpt
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parentCtx, *timeout)
	defer cancel()
	profile := make([]bool, 11)
	for i := range profile {
		profile[i] = true
	}
	e, original, _, _, err := loadInitializedCreateAudit(cfg, store, *parent, false, false, false, false, false, profile...)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return appRootCRLError("original-operator-material")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return appRootCRLError("original-kubeconfig")
	}
	b, err := newEnvironmentCreateCRLBackend(string(kube), *env, cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	if *execute {
		if *confirm != cfg.Values["CLOUD_STACK_NAME"] || !environmentPreflightDigest.MatchString(*review) {
			return appRootCRLError("confirmation")
		}
		a, source, err := loadManagedLateAppRootCRLArtifact(store, e, *review)
		if err != nil {
			return err
		}
		if a.Plan.CheckerSHA != checker || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || a.Plan.Image != original.Plan.Plan.Bootstrap.Image {
			return appRootCRLError("current-qualified-inputs")
		}
		return executeManagedLateAppRootCRL(ctx, cfg, store, e, b, operator, a, source, out)
	}
	r, post, err := loadManagedLatePostMintSourceContent(cfg, store, e, false)
	if err != nil {
		return err
	}
	if len(e.Journal.Entries) != 370 || post.InstallationClock != nil {
		return appRootCRLError("exact-uninstalled-source370")
	}
	p := createAppRootCRLPlan{Schema: createAppRootCRLSchema, Parent: e.Journal.PlanSHA256, SourceJournalSHA: deploymentplan.Digest(e.Journal), SourceOwnerSHA: deploymentplan.Digest(e.owner), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(cfg), Configuration: cfg, Generation: original.Plan.Plan, KubeconfigSHA: environmentPreflightBytesSHA256(kube), Image: original.Plan.Plan.Bootstrap.Image, PostSource: *post, Root: r.ManagedTransportRecovery.AppRoot, Intermediate: r.ManagedTransportRecovery.AppIntermediate}
	p.Snapshot, _, err = readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil {
		return err
	}
	p.Material, err = appRootCRLMaterial(store, p.Parent, p.Root)
	if err != nil {
		return err
	}
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/material", p.Parent, "operator-binding.json"), &p.Operator) != nil {
		return appRootCRLError("recorded-human-principal")
	}
	if _, err := validateManagedLateAppRootCRLSource(store, e, p); err != nil {
		return err
	}
	scoped := context.WithValue(ctx, managedLateAppRootCRLObservationKey{}, managedLateAppRootCRLObservation{p, false})
	admitted, _, err := loadAdmittedCreateRecovery(store, e, r.ManagedLateSession.Review)
	if err != nil {
		return err
	}
	recovery := admitted.Plan
	recovery.Materials = map[string]string{}
	for k, v := range admitted.Plan.Materials {
		recovery.Materials[k] = v
	}
	ledger, ledgerErr := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if ledgerErr != nil || createOpenBaoACKCompletionRead(store, "deployment/resources/active.json", &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return appRootCRLError("original-ledger")
	}
	recovery.Materials["accepted-service-core-managed-application-current-volume-ledger"] = active.LedgerSHA
	recovery.Materials["accepted-service-core-managed-application-current-volume-active"] = deploymentplan.Digest(active)
	if err := verifyManagedLateSessionCurrentNative(scoped, cfg, store, e, b, r, &recovery, r.ManagedLateSession); err != nil {
		return err
	}
	p.NativeEvidence = recovery.Materials
	records, _, err := readManagedLateRegistryCRLs(scoped, cfg, e, b, r, managedLateObserveACKReady)
	if err != nil {
		return err
	}
	ica := records[p.Intermediate.ID]
	p.IntermediateCRL = createRootCRLRecord{ica.IssuerID, ica.Digest, ica.Number, ica.PEM, ica.ThisUpdate, ica.NextUpdate}
	human, err := appRootCRLHumanClient(ctx, cfg, store, e, b, operator, p.Operator)
	if err != nil {
		return err
	}
	nativeRoot, err := human.ReadIssuer(ctx, p.Root.ID)
	if err != nil || !appRootCRLNativeIssuerMatches(nativeRoot, p.Root) {
		return appRootCRLError("authenticated-original-registry-root")
	}
	if err := prepareManagedLateAppRootCRLCandidate(ctx, store, e, &p); err != nil {
		return err
	}
	if err := loadManagedLateAppRootCRLTargets(ctx, cfg, store, e, b, r, &p); err != nil {
		return err
	}
	p.OriginalVolumes, p.ConsumerVolumes, err = readManagedLateAppRootCRLVolumes(e, p.Consumers, p.NativeEvidence)
	if err != nil {
		return err
	}
	if err := verifyManagedLateAppRootCRLInputs(ctx, cfg, store, e, b, p, false); err != nil {
		return err
	}
	fresh, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(p.Snapshot) {
		return appRootCRLError("snapshot-changed-after-signing")
	}
	p.QualifiedAt = time.Now().UTC()
	sha := deploymentplan.Digest(p)
	if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, sha)); err != nil {
		return err
	}
	q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
	for _, id := range createAppRootCRLGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: sha})
	}
	a := createAppRootCRLArtifact{createAppRootCRLSchema + "/artifact", p, q, p.QualifiedAt, p.QualifiedAt.Add(15 * time.Minute)}
	for name, value := range map[string]any{"qualification.json": a, "source-journal.json": e.Journal, "source-owner.json": e.owner} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(appRootCRLBase(sha), name), value); err != nil {
			return err
		}
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "GO environment=dev scope=original-app-root-crl review-sha256=%s crl-number=%s consumers=2\n", sha, p.Signed.Number)
	return nil
}

func readManagedLateAppRootCRLVolumes(e *environmentCreateExecution, targets []createRootCRLStateTarget, materials map[string]string) (map[string]environmentCreateVolumeOwner, map[string]environmentCreateVolumeOwner, error) {
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		return nil, nil, err
	}
	var active managedTransportVolumeLedgerActive
	if createOpenBaoACKCompletionRead(e.Store, "deployment/resources/active.json", &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return nil, nil, appRootCRLError("canonical-volume-ledger")
	}
	// The historical App authority pins the original eighteen. A later typed
	// Redis allocation remains independently authenticated in the full ledger.
	ledger, err = managedLateRedisHistoricalVolumes(e.Store, e, ledger)
	if err != nil {
		return nil, nil, err
	}
	original, consumers, hashes := map[string]environmentCreateVolumeOwner{}, map[string]environmentCreateVolumeOwner{}, map[string]string{}
	for _, owner := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + owner.Namespace + "/" + owner.PVCName
		if original[id].PVCUID != "" {
			return nil, nil, appRootCRLError("duplicate-volume")
		}
		original[id] = owner
		hashes[id] = deploymentplan.Digest(owner)
	}
	if len(original) != 18 || materials["accepted-service-core-managed-complete-current-native-volumes"] != deploymentplan.Digest(hashes) {
		return nil, nil, appRootCRLError("original-eighteen-volume-authority")
	}
	for _, target := range targets {
		owner, ok := original[target.ClaimID]
		if !ok || owner.PVCUID != target.ClaimUID || !environmentBootstrapOwnerUUID.MatchString(owner.PVUID) || owner.PVName == "" {
			return nil, nil, appRootCRLError("original-App-consumer-volume")
		}
		consumers[target.ClaimID] = owner
	}
	if len(consumers) != 2 {
		return nil, nil, appRootCRLError("exact-two-consumer-claims")
	}
	return original, consumers, nil
}

func managedLateAppRootCRLFrame(store secretStore, e *environmentCreateExecution, completedOnly bool) (*environmentCreateExecution, *createAppRootCRLArtifact, error) {
	if e == nil {
		return nil, nil, appRootCRLError("selected-frame")
	}
	index := -1
	review := ""
	for i, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "app-root-crl-renewal-") && !strings.Contains(entry.Key, "/") {
			if index != -1 {
				return nil, nil, appRootCRLError("duplicate-App-scope")
			}
			index = i
			review = strings.TrimPrefix(entry.Key, "app-root-crl-renewal-")
		}
	}
	if index == -1 {
		if _, err := os.Lstat(filepath.Join(store.Root, appRootCRLPointer(e.Journal.PlanSHA256))); !errors.Is(err, os.ErrNotExist) {
			return nil, nil, appRootCRLError("orphan-App-completion-or-unknown-pointer")
		}
		return e, nil, nil
	}
	a, source, err := loadManagedLateAppRootCRLArtifact(store, e, review)
	if err != nil {
		return nil, nil, err
	}
	if index != len(source.Entries) || index != 370 || len(e.Journal.KubernetesLeases) < len(source.KubernetesLeases) {
		return nil, nil, appRootCRLError("exact-App-source-boundary")
	}
	count := len(e.Journal.Entries) - index
	if count > 4 {
		count = 4
	}
	bounded := e.Journal
	bounded.Entries = e.Journal.Entries[:index+count]
	leaseCount := len(source.KubernetesLeases)
	if len(e.Journal.KubernetesLeases) > leaseCount {
		leaseCount++
	}
	bounded.KubernetesLeases = e.Journal.KubernetesLeases[:leaseCount]
	if !managedLateAppRootCRLProgressValid(a, source, bounded, completedOnly) || len(e.Journal.Entries) > index+4 && !managedLateAppRootCRLProgressValid(a, source, bounded, true) {
		return nil, nil, appRootCRLError("literal-App-progress")
	}
	if completedOnly {
		if err := validateManagedLateAppRootCRLCompletion(store, a, source, bounded); err != nil {
			return nil, nil, err
		}
	}
	frame := *e
	frame.Journal = e.Journal
	frame.Journal.Entries = append(slices.Clone(e.Journal.Entries[:index]), e.Journal.Entries[index+count:]...)
	frame.Journal.KubernetesLeases = append(slices.Clone(e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]), e.Journal.KubernetesLeases[leaseCount:]...)
	return &frame, &a, nil
}
func validateManagedLateAppRootCRLCompletion(store secretStore, a createAppRootCRLArtifact, source, final environmentCreateJournal) error {
	if !managedLateAppRootCRLProgressValid(a, source, final, true) {
		return appRootCRLError("completed-App-protocol")
	}
	review := deploymentplan.Digest(a.Plan)
	var proof createAppRootCRLCompletion
	var mirrored createAppRootCRLCompletion
	var captured environmentCreateJournal
	if createOpenBaoACKCompletionRead(store, appRootCRLPointer(a.Plan.Parent), &proof) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(appRootCRLBase(review), "completed.json"), &mirrored) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(appRootCRLBase(review), "final-journal.json"), &captured) != nil || deploymentplan.Digest(proof) != deploymentplan.Digest(mirrored) || proof.Schema != createAppRootCRLSchema+"/completion" || proof.Parent != a.Plan.Parent || proof.Review != review || !proof.Completed || proof.SourceSHA != deploymentplan.Digest(source) || proof.FinalSHA != deploymentplan.Digest(final) || deploymentplan.Digest(captured) != proof.FinalSHA || proof.SourceEntryCount != len(source.Entries) || proof.FinalEntryCount != len(final.Entries) || proof.EvidenceSHA != deploymentplan.Digest(proof.NativeEvidence) || activeTransportJournalDigest(jsonRawObject(final.Entries[len(source.Entries)].Receipt)) != activeTransportJournalDigest(appRootCRLReceipt(a, proof.NativeEvidence)) {
		return appRootCRLError("genuine-App-completion")
	}
	evidence := map[string]string{}
	for _, entry := range final.Entries[len(source.Entries)+2:] {
		var observed map[string]string
		if decodeEnvironmentJSON(entry.Receipt, &observed) != nil {
			return appRootCRLError("recorded-consumer-evidence")
		}
		for k, v := range observed {
			if old := evidence[k]; old != "" && old != v {
				return appRootCRLError("recorded-consumer-conflict")
			}
			evidence[k] = v
		}
	}
	if deploymentplan.Digest(evidence) != proof.EvidenceSHA {
		return appRootCRLError("completed-consumer-evidence")
	}
	if proof.CompletionReview != "" {
		var owner environmentCreateOwner
		if createOpenBaoACKCompletionRead(store, filepath.Join(appRootCRLBase(review), "source-owner.json"), &owner) != nil || deploymentplan.Digest(owner) != a.Plan.SourceOwnerSHA {
			return appRootCRLError("completion-only-original-owner")
		}
		return validateManagedLateAppCompletionProof(store, a, source, proof, final, owner)
	}
	return nil
}

func executeManagedLateAppRootCRL(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, a createAppRootCRLArtifact, source environmentCreateJournal, out io.Writer) (resultErr error) {
	p := a.Plan
	review := deploymentplan.Digest(p)
	key := "app-root-crl-renewal-" + review
	if err := managedLateInstallationClockAbsent(store, p.Parent); err != nil {
		return err
	}
	if !managedLateAppRootCRLProgressValid(a, source, e.Journal, false) {
		return appRootCRLError("exact-execution-source")
	}
	admitted := len(e.Journal.Entries) > len(source.Entries)
	if admitted && len(e.Journal.KubernetesLeases) > len(source.KubernetesLeases) {
		return appRootCRLError("existing-App-outcome-needs-separate-qualified-recovery")
	}
	if !time.Now().UTC().Before(a.ValidUntil) {
		return appRootCRLError("fresh-sixGO")
	}
	snapshot, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil {
		return err
	}
	imported := deploymentplan.Digest(snapshot.Prior) == deploymentplan.Digest(p.Signed)
	expected := snapshot
	if imported {
		expected.Prior = p.Snapshot.Prior
	}
	if deploymentplan.Digest(expected) != deploymentplan.Digest(p.Snapshot) || imported && !admitted {
		return appRootCRLError("native-reviewed-prior-and-complete-revocations")
	}
	if err := verifyManagedLateAppRootCRLInputs(ctx, cfg, store, e, b, p, imported); err != nil {
		return err
	}
	client, err := appRootCRLHumanClient(ctx, cfg, store, e, b, operator, p.Operator)
	if err != nil {
		return err
	}
	nativeRoot, err := client.ReadIssuer(ctx, p.Root.ID)
	if err != nil || !appRootCRLNativeIssuerMatches(nativeRoot, p.Root) {
		return appRootCRLError("original-registry-issuer")
	}
	if !time.Now().UTC().Before(a.ValidUntil) || e.Check(ctx) != nil {
		return appRootCRLError("immediate-fresh-sixGO-before-admission")
	}
	if err := rootCRLStepBegin(e, key, p); err != nil {
		return err
	}
	lease, err := b.AcquireExecution(ctx, rootCRLLeasePlan(cfg, review))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(cleanup))
	}()
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	guard := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return lease.Check(ctx)
	}
	importKey := key + "/native-import"
	if err := rootCRLStepBegin(e, importKey, appRootCRLImportIntent(p)); err != nil {
		return err
	}
	if !imported {
		if err := guard(); err != nil {
			return err
		}
		if err := verifyManagedLateAppRootCRLInputs(ctx, cfg, store, e, b, p, false); err != nil {
			return err
		}
		if err := refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
			return err
		}
		nativeRoot, err = client.ReadIssuer(ctx, p.Root.ID)
		if err != nil || !appRootCRLNativeIssuerMatches(nativeRoot, p.Root) {
			return appRootCRLError("immediate-authenticated-original-root")
		}
		immediate, _, snapshotErr := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
		if snapshotErr != nil || deploymentplan.Digest(immediate) != deploymentplan.Digest(p.Snapshot) {
			return appRootCRLError("immediate-reviewed-prior-and-revocation-union")
		}
		if err := guard(); err != nil {
			return err
		}
		var observed createRootCRLRecord
		if err := client.call(ctx, "POST", "/v1/platform/pki/issuers/"+p.Root.ID+"/crl", "", map[string]string{"crl_pem": p.Signed.PEM}, &observed); err != nil {
			return err
		}
		if deploymentplan.Digest(observed) != deploymentplan.Digest(p.Signed) {
			return appRootCRLError("genuine-native-human-import")
		}
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, p.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(p.Signed) {
		return appRootCRLError("actual-native-App2")
	}
	current.Prior = p.Snapshot.Prior
	if deploymentplan.Digest(current) != deploymentplan.Digest(p.Snapshot) {
		return appRootCRLError("native-revocations-preserved")
	}
	if err := rootCRLStepComplete(e, importKey, deploymentplan.Object{"native_crl": p.Signed, "request_sha256": deploymentplan.Digest(p.Request), "operator_binding_sha256": deploymentplan.Digest(p.Operator)}); err != nil {
		return err
	}
	evidence := map[string]string{}
	for _, target := range p.Consumers {
		stage := key + "/native-runtime-" + deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
		if err := rootCRLStepBegin(e, stage, appRootCRLRuntimeIntent(p, target)); err != nil {
			return err
		}
		var actual map[string]string
		for {
			if err := guard(); err != nil {
				return err
			}
			actual, err = observeManagedLateAppRootCRLConsumer(ctx, cfg, store, e, b, a, target)
			if err == nil {
				break
			}
			if err := waitEnvironmentCreateProviderPoll(ctx); err != nil {
				return appRootCRLError("native-consumer-cache-and-ACK-not-complete")
			}
		}
		for k, v := range actual {
			if old := evidence[k]; old != "" && old != v {
				return appRootCRLError("native-consumer-evidence-conflict")
			}
			evidence[k] = v
		}
		if err := rootCRLStepComplete(e, stage, actual); err != nil {
			return err
		}
	}
	if err := guard(); err != nil {
		return err
	}
	if err := verifyManagedLateAppRootCRLInputs(ctx, cfg, store, e, b, p, true); err != nil {
		return err
	}
	if err := guard(); err != nil {
		return err
	}
	if err := rootCRLStepComplete(e, key, appRootCRLReceipt(a, evidence)); err != nil {
		return err
	}
	if !managedLateAppRootCRLProgressValid(a, source, e.Journal, true) {
		return appRootCRLError("actual-completed-four-tail")
	}
	proof := createAppRootCRLCompletion{Schema: createAppRootCRLSchema + "/completion", Parent: p.Parent, Review: review, SourceSHA: deploymentplan.Digest(source), FinalSHA: deploymentplan.Digest(e.Journal), EvidenceSHA: deploymentplan.Digest(evidence), SourceEntryCount: len(source.Entries), FinalEntryCount: len(e.Journal.Entries), Completed: true, NativeEvidence: evidence}
	for name, value := range map[string]any{"final-journal.json": e.Journal, "completed.json": proof} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(appRootCRLBase(review), name), value); err != nil {
			return err
		}
	}
	if err := writeMQTTRootCRLImmutable(store, appRootCRLPointer(p.Parent), proof); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS environment=dev scope=original-app-root-crl review-sha256=%s crl-number=%s consumers=2 original-issuer-key-preserved=true\n", review, p.Signed.Number)
	return nil
}

func managedLateAppRootCRLReadyExceptions(ctx context.Context, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, ready map[string]bool) error {
	observation, selected := ctx.Value(managedLateAppRootCRLObservationKey{}).(managedLateAppRootCRLObservation)
	if !selected {
		return nil
	}
	frame, _, err := managedLateAppRootCRLFrame(store, e, false)
	if err != nil {
		return err
	}
	if _, err := validateManagedLateAppRootCRLSource(store, frame, observation.Plan); err != nil {
		return err
	}
	if r == nil || r.ManagedTransportRecovery == nil || deploymentplan.Digest(r.ManagedTransportRecovery.AppRoot) != deploymentplan.Digest(observation.Plan.Root) {
		return appRootCRLError("scoped-expected-stale-App-health")
	}
	for _, name := range []string{"pki-controller", "video-cloud-api-app-pki", "mqtt-pki"} {
		id := "Deployment/" + e.Journal.Stack + "-video-cloud/" + name
		if _, ok := ready[id]; !ok {
			return appRootCRLError("expected-original-App-dependent-owner")
		}
		ready[id] = false
	}
	return nil
}

func appRootCRLCandidatePathValid(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path && strings.HasPrefix(filepath.ToSlash(path), "deployment/repairs/app-root-crl/") && environmentPreflightDigest.MatchString(filepath.Base(path))
}
func managedLateAppRootCRLConsumerEvidenceValid(p createAppRootCRLPlan, target createRootCRLStateTarget, evidence map[string]string) bool {
	owner, ok := p.ConsumerVolumes[target.ClaimID]
	if !ok || len(evidence) != 7 || len(target.Paths) != 2 || owner.PVCUID != target.ClaimUID || evidence["consumer-owner-"+target.ID] != deploymentplan.Digest(target.Desired) || evidence["consumer-Pod-"+target.ID] != deploymentplan.Digest(target.PodUID) || evidence["consumer-volume-"+target.ClaimID] != deploymentplan.Digest(owner) {
		return false
	}
	expected := map[string]bool{"consumer-owner-" + target.ID: true, "consumer-Pod-" + target.ID: true, "consumer-volume-" + target.ClaimID: true, "native-ACKs-" + p.Root.ID: true, "native-ACKs-" + p.Intermediate.ID: true}
	for _, path := range target.Paths {
		expected["cache-"+target.ID+"-"+filepath.Base(path)] = true
	}
	if len(expected) != 7 {
		return false
	}
	for key, value := range evidence {
		if !expected[key] || !environmentPreflightDigest.MatchString(value) {
			return false
		}
	}
	return true
}
func managedLateAppRootCRLHistoricalClockAllowed(a createAppRootCRLArtifact, source, current environmentCreateJournal, clock *createManagedLateInstallationClock) bool {
	n := len(source.Entries)
	if !managedLateInstallationClockValid(clock) || len(current.Entries) < n+4 || len(current.KubernetesLeases) < len(source.KubernetesLeases)+1 {
		return false
	}
	bounded := current
	bounded.Entries = current.Entries[:n+4]
	bounded.KubernetesLeases = current.KubernetesLeases[:len(source.KubernetesLeases)+1]
	return managedLateAppRootCRLProgressValid(a, source, bounded, true) && clock.Parent == a.Plan.Parent && clock.SourceSHA == deploymentplan.Digest(bounded) && clock.OwnerSHA == a.Plan.SourceOwnerSHA && clock.SessionID == a.Plan.PostSource.Ceremony.BootstrapSessionID && clock.Fingerprint == a.Plan.PostSource.Fingerprint && clock.SessionExpires.Equal(a.Plan.PostSource.SessionExpires) && clock.LeafNotAfter.Equal(a.Plan.PostSource.LeafNotAfter) && !clock.StartedAt.Before(current.Entries[n].CompletedAt)
}
