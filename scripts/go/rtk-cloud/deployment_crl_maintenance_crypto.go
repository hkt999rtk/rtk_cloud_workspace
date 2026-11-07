package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"time"
)

func validateMaintenanceCRL(environment string, root environmentCreateRegisteredIssuer, record createRootCRLRecord, fresh bool, now time.Time) (*x509.RevocationList, error) {
	block, rest := pem.Decode([]byte(record.PEM))
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if (environment != "dev" && environment != "staging" && environment != "prod") || root.Environment != environment || (root.Status != "active" && root.Status != "retiring") || err != nil || !cert.IsCA || environmentPreflightBytesSHA256(cert.Raw) != deploymentplan.String(root.Document["certificate_fingerprint_sha256"]) && environmentPreflightBytesSHA256(cert.Raw) != root.RootFingerprint {
		return nil, errors.New("CRL signature, authority, metadata or validity is invalid")
	}
	if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 || err != nil {
		return nil, errors.New("CRL signature, authority, metadata or validity is invalid")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.CheckSignatureFrom(cert) != nil || !bytes.Equal(crl.RawIssuer, cert.RawSubject) || !bytes.Equal(crl.AuthorityKeyId, cert.SubjectKeyId) || len(cert.SubjectKeyId) == 0 || record.IssuerID != root.ID || record.Digest != environmentPreflightBytesSHA256(crl.Raw) || crl.Number == nil || crl.Number.Sign() <= 0 || crl.Number.BitLen() > 159 || record.Number != crl.Number.String() || !record.ThisUpdate.Equal(crl.ThisUpdate) || !record.NextUpdate.Equal(crl.NextUpdate) {
		return nil, errors.New("CRL signature, authority, metadata or validity is invalid")
	}
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return nil, errors.New("issuer certificate is expired or not yet valid")
	}
	if fresh && (crl.ThisUpdate.After(now) || crl.ThisUpdate.Before(now.Add(-7*24*time.Hour)) || !crl.NextUpdate.After(now.Add(45*time.Minute)) || crl.NextUpdate.After(crl.ThisUpdate.Add(7*24*time.Hour)) || crl.NextUpdate.After(cert.NotAfter) || now.Before(cert.NotBefore)) {
		return nil, errors.New("CRL signature, authority, metadata or validity is invalid")
	}
	for _, ext := range crl.Extensions {
		if ext.Id.String() != "2.5.29.20" && ext.Id.String() != "2.5.29.35" {
			return nil, errors.New("CRL signature, authority, metadata or validity is invalid")
		}
	}
	seen := map[string]bool{}
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber == nil || entry.SerialNumber.Sign() <= 0 || entry.RevocationTime.IsZero() || entry.RevocationTime.After(crl.ThisUpdate) || entry.ReasonCode < 0 || entry.ReasonCode > 10 || entry.ReasonCode == 7 || entry.ReasonCode == 8 || seen[entry.SerialNumber.Text(16)] {
			return nil, errors.New("CRL signature, authority, metadata or validity is invalid")
		}
		seen[entry.SerialNumber.Text(16)] = true
		for _, ext := range entry.Extensions {
			if ext.Id.String() != "2.5.29.21" {
				return nil, errors.New("CRL signature, authority, metadata or validity is invalid")
			}
		}
	}
	return crl, nil
}

func buildMaintenanceCRLRequest(environment string, item crlMaintenanceIssuer, now time.Time) (createRootCRLRequest, error) {
	var out createRootCRLRequest
	root, snapshot := item.Issuer, item.Snapshot
	if root.Kind != "root" || root.Document["signer_provider"] != "offline" {
		return out, errors.New("original offline root required")
	}
	prior, err := validateMaintenanceCRL(environment, root, snapshot.Prior, false, now)
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
			return errors.New("complete revocation inventory or issuer lifetime is invalid")
		}
		if old, ok := entries[entry.Serial]; ok {
			// Native unspecified reason does not remove an already published reason.
			if !old.RevokedAt.Equal(entry.RevokedAt) || entry.Reason != 0 && old.Reason != entry.Reason {
				return errors.New("complete revocation inventory or issuer lifetime is invalid")
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
			return out, errors.New("complete revocation inventory or issuer lifetime is invalid")
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
		return out, errors.New("complete revocation inventory or issuer lifetime is invalid")
	}
	out = createRootCRLRequest{root.ID, root.RootFingerprint, new(big.Int).Add(prior.Number, big.NewInt(1)).String(), now, next, revoked}
	return out, nil
}

func validateMaintenanceSuccessor(environment string, item crlMaintenanceIssuer, request createRootCRLRequest, signed createRootCRLRecord, now time.Time) error {
	crl, err := validateMaintenanceCRL(environment, item.Issuer, signed, true, now)
	if err != nil {
		return err
	}
	expected, err := buildMaintenanceCRLRequest(environment, item, request.ThisUpdate)
	if err != nil {
		return err
	}
	if deploymentplan.Digest(expected) != deploymentplan.Digest(request) || signed.Number != request.Number || !signed.ThisUpdate.Equal(request.ThisUpdate) || !signed.NextUpdate.Equal(request.NextUpdate) || len(crl.RevokedCertificateEntries) != len(request.Entries) {
		return errors.New("signed CRL differs from complete reviewed successor")
	}
	entries := map[string]createRootCRLRevocation{}
	for _, e := range request.Entries {
		entries[e.Serial] = e
	}
	for _, e := range crl.RevokedCertificateEntries {
		want, ok := entries[e.SerialNumber.Text(16)]
		if !ok || !want.RevokedAt.Equal(e.RevocationTime) || want.Reason != e.ReasonCode {
			return errors.New("signed CRL dropped or changed a revocation")
		}
	}
	return nil
}
