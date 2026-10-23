package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const openBaoTLSOriginalIntermediateID = "48b05140-6167-46c0-b448-562945af691b"

// A completed Root repair remains immutable. Its original ICA certificate is
// also the authority for normal automatic CRL successors; an updated CRL is not
// a new issuer, Root, trust manifest or completion receipt.
func validateOpenBaoTLSCurrentIntermediate(root, ica environmentCreateRegisteredIssuer, prior createRootCRLRecord, snapshot createRootCRLNativeSnapshot, history []createRootCRLRecord, now time.Time) error {
	return validateOpenBaoTLSCurrentIntermediateRemaining(root, ica, prior, snapshot, history, now, 45*time.Minute)
}

func validateOpenBaoTLSCurrentIntermediateRemaining(root, ica environmentCreateRegisteredIssuer, prior createRootCRLRecord, snapshot createRootCRLNativeSnapshot, history []createRootCRLRecord, now time.Time, minimumRemaining time.Duration) error {
	bad := func() error { return openBaoTLSRootCRLError("current-original-ICA-successor") }
	if minimumRemaining < 0 {
		return bad()
	}
	rootCert, rootErr := parseFirstCertificatePEM([]byte(root.Certificate))
	cert, certErr := parseFirstCertificatePEM([]byte(ica.Certificate))
	if root.ID != "734487b8-abcf-40be-a813-8b52c64f7954" || root.Domain != "openbao_tls" || root.Environment != "dev" || root.Kind != "root" || root.Status != "active" || root.Document["signer_provider"] != "offline" || rootErr != nil || certErr != nil || rootCert.CheckSignatureFrom(rootCert) != nil || environmentPreflightBytesSHA256(rootCert.Raw) != root.RootFingerprint ||
		ica.ID != openBaoTLSOriginalIntermediateID || ica.Domain != root.Domain || ica.Environment != root.Environment || ica.Kind != "intermediate" || ica.Status != "active" || ica.Reference != "pki-issuers/openbao_tls/"+ica.ID+"/v1" || ica.RootFingerprint != root.RootFingerprint || ica.Document["parent_issuer_id"] != root.ID || ica.Document["signer_provider"] != "openbao" || cert.CheckSignatureFrom(rootCert) != nil || prior.IssuerID != ica.ID || prior.Number != "3" || len(history) == 0 || len(history) > 256 || deploymentplan.Digest(history[0]) != deploymentplan.Digest(prior) || deploymentplan.Digest(history[len(history)-1]) != deploymentplan.Digest(snapshot.Prior) {
		return bad()
	}
	entries := map[string]createRootCRLRevocation{}
	include := func(v createRootCRLRevocation) error {
		n, ok := new(big.Int).SetString(v.Serial, 16)
		v.RevokedAt = v.RevokedAt.UTC().Truncate(time.Second)
		if !ok || n.Sign() <= 0 || n.Text(16) != v.Serial || v.RevokedAt.IsZero() || v.RevokedAt.After(now) || v.Reason < 0 || v.Reason > 10 || v.Reason == 7 || v.Reason == 8 {
			return bad()
		}
		if old, ok := entries[v.Serial]; ok {
			if !old.RevokedAt.Equal(v.RevokedAt) || v.Reason != 0 && old.Reason != v.Reason {
				return bad()
			}
			return nil
		}
		entries[v.Serial] = v
		return nil
	}
	parse := func(record createRootCRLRecord, clock time.Time) (*x509.RevocationList, error) {
		if _, err := validateManagedLateSignedCRL("openbao_tls", ica.ID, environmentPreflightBytesSHA256(cert.Raw), ica.Status, ica.ChainPEM, deviceRootCRLRecord(record), managedLateObserveACKReady, clock); err != nil || record.ThisUpdate.After(now) {
			return nil, bad()
		}
		block, _ := pem.Decode([]byte(record.PEM))
		if block == nil {
			return nil, bad()
		}
		return x509.ParseRevocationList(block.Bytes)
	}
	var previous *x509.RevocationList
	for _, record := range history {
		crl, err := parse(record, record.ThisUpdate)
		if err != nil || previous != nil && (crl.Number.Cmp(previous.Number) <= 0 || crl.ThisUpdate.Before(previous.ThisUpdate)) {
			return bad()
		}
		for _, v := range crl.RevokedCertificateEntries {
			if err := include(createRootCRLRevocation{v.SerialNumber.Text(16), v.RevocationTime, v.ReasonCode}); err != nil {
				return err
			}
		}
		previous = crl
	}
	for _, v := range snapshot.Bindings {
		if err := include(v); err != nil {
			return err
		}
	}
	for _, v := range snapshot.Certificates {
		leaf, err := parseFirstCertificatePEM([]byte(v.PEM))
		if err != nil || leaf.CheckSignatureFrom(cert) != nil {
			return bad()
		}
		if err := include(createRootCRLRevocation{leaf.SerialNumber.Text(16), v.RevokedAt, 0}); err != nil {
			return err
		}
	}
	current, err := parse(snapshot.Prior, now)
	if err != nil || !current.NextUpdate.After(now.Add(minimumRemaining)) {
		return bad()
	}
	actual := map[string]createRootCRLRevocation{}
	for _, v := range current.RevokedCertificateEntries {
		actual[v.SerialNumber.Text(16)] = createRootCRLRevocation{v.SerialNumber.Text(16), v.RevocationTime, v.ReasonCode}
	}
	if deploymentplan.Digest(actual) != deploymentplan.Digest(entries) {
		return openBaoTLSRootCRLError("complete-current-ICA-revocation-union")
	}
	return nil
}

// Only a genuine already completed Root2 repair may consume automatic ICA
// successors. Original Root repair qualification/import keeps its exact ICA3.
func readCompletedOpenBaoTLSCurrentIntermediate(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createOpenBaoTLSRootCRLArtifact) (createRootCRLNativeSnapshot, string, bool, error) {
	return readCompletedOpenBaoTLSCurrentIntermediateRemaining(ctx, cfg, store, e, b, a, 45*time.Minute)
}

func readCompletedOpenBaoTLSCurrentIntermediateRemaining(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createOpenBaoTLSRootCRLArtifact, minimumRemaining time.Duration) (createRootCRLNativeSnapshot, string, bool, error) {
	var empty createRootCRLNativeSnapshot
	completed, proof, err := readCreateOpenBaoTLSRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil {
		return empty, "", false, err
	}
	if completed == nil && proof == nil {
		return empty, "", false, nil
	}
	if completed == nil || proof == nil || deploymentplan.Digest(completed) != deploymentplan.Digest(a) || a.Plan.Parent != e.Journal.PlanSHA256 || a.Plan.Signed.Number != "2" {
		return empty, "", false, openBaoTLSRootCRLError("completed-current-ICA-authority")
	}
	native, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, a.Plan.Intermediate.ID)
	if err != nil {
		return empty, "", true, err
	}
	query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) ORDER BY number),'[]'::json) FROM pki_crls WHERE issuer_id='" + openBaoTLSOriginalIntermediateID + "' AND number>=3"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var history []createRootCRLRecord
	if err != nil || decodeEnvironmentJSON(raw, &history) != nil || validateOpenBaoTLSCurrentIntermediateRemaining(a.Plan.Root, a.Plan.Intermediate, a.Plan.IntermediateCRL, native, history, time.Now().UTC(), minimumRemaining) != nil {
		return empty, "", true, openBaoTLSRootCRLError("signed-current-ICA-history-and-inventory")
	}
	return native, environmentPreflightBytesSHA256(raw), true, nil
}

func readOpenBaoTLSCurrentIntermediateACKs(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, ica environmentCreateRegisteredIssuer, record createRootCRLRecord) ([]managedLatePriorACK, error) {
	cert, certErr := parseFirstCertificatePEM([]byte(ica.Certificate))
	if ica.ID != openBaoTLSOriginalIntermediateID || ica.Domain != "openbao_tls" || ica.Environment != "dev" || ica.Kind != "intermediate" || ica.Status != "active" || ica.Document["parent_issuer_id"] != "734487b8-abcf-40be-a813-8b52c64f7954" || record.IssuerID != ica.ID || !environmentPreflightDigest.MatchString(record.Digest) || certErr != nil {
		return nil, openBaoTLSRootCRLError("exact-current-ICA-ACK-authority")
	}
	if _, err := validateManagedLateSignedCRL("openbao_tls", ica.ID, environmentPreflightBytesSHA256(cert.Raw), ica.Status, ica.ChainPEM, deviceRootCRLRecord(record), managedLateObserveACKReady, time.Now().UTC()); err != nil {
		return nil, openBaoTLSRootCRLError("signed-current-ICA-ACK-authority")
	}
	query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'digest',digest,'consumer_id',consumer_id,'acknowledged_at',acknowledged_at) ORDER BY consumer_id),'[]'::json) FROM pki_crl_acknowledgments WHERE issuer_id='" + ica.ID + "' AND digest='" + record.Digest + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var rows []managedLatePriorACK
	if err != nil || decodeEnvironmentJSON(raw, &rows) != nil || validateManagedLatePriorACKRoster(rows, ica.ID, deviceRootCRLRecord(record), []string{"certissuer", "pki-controller"}, time.Now().UTC()) != nil {
		return nil, openBaoTLSRootCRLError("genuine-current-two-ICA-ACKs")
	}
	return rows, nil
}

func verifyOpenBaoTLSCurrentIntermediateInstallation(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, ica environmentCreateRegisteredIssuer, expected createRootCRLNativeSnapshot) ([]managedLatePriorACK, error) {
	rows, err := readOpenBaoTLSCurrentIntermediateACKs(ctx, cfg, runtime, ica, expected.Prior)
	if err != nil {
		return nil, err
	}
	latest, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, runtime.Execution, runtime.Backend, ica.ID)
	if err != nil || deploymentplan.Digest(latest) != deploymentplan.Digest(expected) {
		return nil, openBaoTLSRootCRLError("current-ICA-changed-during-installation-proof")
	}
	return rows, nil
}

func readOpenBaoTLSCurrentIntermediateState(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget, ica environmentCreateRegisteredIssuer, record createRootCRLRecord) ([]byte, error) {
	if ica.ID != openBaoTLSOriginalIntermediateID || len(target.Paths) != 2 || target.Paths[1] != "/var/lib/pki-service/private/crl/openbao-root-"+ica.ID+".json" {
		return nil, openBaoTLSRootCRLError("original-current-ICA-cache-path")
	}
	raw, err := readOpenBaoTLSRootCRLCacheBytes(ctx, cfg, b, target, target.Paths[1])
	if err != nil || validateOpenBaoTLSRootCRLCache(raw, ica, record, true) != nil {
		return nil, openBaoTLSRootCRLError("exact-current-signed-ICA-cache")
	}
	return raw, nil
}
