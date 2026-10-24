// Pure signed-CRL and durable lifecycle guards; no native I/O.
package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"time"
)

type managedLateObservationMode string

const (
	managedLateObserveMQTTPrior       managedLateObservationMode = "mqtt-root-crl-prior"
	managedLateObserveACKReady        managedLateObservationMode = "late-original-key-ack"
	managedLateObserveOpenBaoTLSPrior managedLateObservationMode = "openbao-tls-root-crl-prior"
)

type managedLateCRLRecord struct {
	IssuerID   string    `json:"issuer_id"`
	Digest     string    `json:"crl_sha256"`
	Number     string    `json:"crl_number"`
	PEM        string    `json:"crl_pem"`
	ThisUpdate time.Time `json:"this_update"`
	NextUpdate time.Time `json:"next_update"`
}
type managedLateCRLProof struct {
	IssuerID, Fingerprint, Digest, Number string
	ThisUpdate, NextUpdate                time.Time
}

// This follows native pki.ValidateCRLSignature's direct/full signed contract.
// Only the exact observed MQTT Root#1 prior may be inspected at its signed
// publication time. Runtime ACK readiness always requires current freshness.
func validateManagedLateSignedCRL(domain, issuerID, fingerprint, status, chain string,
	record managedLateCRLRecord, mode managedLateObservationMode, now time.Time) (managedLateCRLProof, error) {
	var empty managedLateCRLProof
	deny := func() (managedLateCRLProof, error) { return empty, errors.New("original signed CRL differs") }
	if mode != managedLateObserveMQTTPrior && mode != managedLateObserveACKReady && mode != managedLateObserveOpenBaoTLSPrior || status != "active" || issuerID == "" || record.IssuerID != issuerID || len(record.PEM) > 1<<20 {
		return deny()
	}
	authorityBlock, _ := pem.Decode([]byte(chain))
	if authorityBlock == nil || authorityBlock.Type != "CERTIFICATE" || len(authorityBlock.Headers) != 0 {
		return deny()
	}
	authority, err := x509.ParseCertificate(authorityBlock.Bytes)
	if err != nil {
		return deny()
	}
	hash := sha256.Sum256(authority.Raw)
	if fingerprint != hex.EncodeToString(hash[:]) {
		return deny()
	}
	block, rest := pem.Decode([]byte(record.PEM))
	if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return deny()
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.CheckSignatureFrom(authority) != nil || !bytes.Equal(crl.RawIssuer, authority.RawSubject) || !bytes.Equal(crl.AuthorityKeyId, authority.SubjectKeyId) || len(authority.SubjectKeyId) == 0 {
		return deny()
	}
	derSHA := sha256.Sum256(crl.Raw)
	if crl.Number == nil || crl.Number.Sign() <= 0 || crl.Number.BitLen() > 159 || record.Digest != hex.EncodeToString(derSHA[:]) || record.Number != crl.Number.String() || !record.ThisUpdate.Equal(crl.ThisUpdate) || !record.NextUpdate.Equal(crl.NextUpdate) {
		return deny()
	}
	clock := now
	if mode == managedLateObserveMQTTPrior && domain == "mqtt" && issuerID == "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" && (crl.Number.Cmp(big.NewInt(1)) != 0 || crl.NextUpdate.After(now)) {
		return deny()
	}
	if mode == managedLateObserveOpenBaoTLSPrior && domain == "openbao_tls" && issuerID == "734487b8-abcf-40be-a813-8b52c64f7954" && crl.Number.Cmp(big.NewInt(1)) != 0 {
		return deny()
	}
	if !crl.NextUpdate.After(now) && mode == managedLateObserveOpenBaoTLSPrior {
		if domain != "openbao_tls" || issuerID != "734487b8-abcf-40be-a813-8b52c64f7954" || crl.Number.Cmp(big.NewInt(1)) != 0 {
			return deny()
		}
		clock = crl.ThisUpdate
	}
	if !crl.NextUpdate.After(now) && mode == managedLateObserveMQTTPrior {
		if domain != "mqtt" || issuerID != "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" || crl.Number.Cmp(big.NewInt(1)) != 0 {
			return deny()
		}
		clock = crl.ThisUpdate
	}
	if crl.ThisUpdate.After(clock) || crl.ThisUpdate.Before(clock.Add(-7*24*time.Hour)) || !crl.NextUpdate.After(clock) || crl.NextUpdate.After(crl.ThisUpdate.Add(7*24*time.Hour)) || crl.NextUpdate.After(authority.NotAfter) || clock.Before(authority.NotBefore) {
		return deny()
	}
	for _, extension := range crl.Extensions {
		oid := extension.Id.String()
		if oid != "2.5.29.20" && oid != "2.5.29.35" {
			return deny()
		}
	}
	serials := map[string]bool{}
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber == nil || entry.SerialNumber.Sign() <= 0 || entry.RevocationTime.IsZero() || entry.RevocationTime.After(crl.ThisUpdate) || entry.ReasonCode < 0 || entry.ReasonCode > 10 || entry.ReasonCode == 7 || entry.ReasonCode == 8 || serials[entry.SerialNumber.Text(16)] {
			return deny()
		}
		serials[entry.SerialNumber.Text(16)] = true
		for _, extension := range entry.Extensions {
			if extension.Id.String() != "2.5.29.21" {
				return deny()
			}
		}
	}
	return managedLateCRLProof{IssuerID: issuerID, Fingerprint: fingerprint, Digest: record.Digest, Number: record.Number, ThisUpdate: record.ThisUpdate, NextUpdate: record.NextUpdate}, nil
}

type managedLateCachedCRL struct {
	IssuerFingerprint string                `json:"issuer_fingerprint"`
	CRL               managedLateCRLRecord  `json:"crl"`
	TerminalIssuerID  string                `json:"terminal_issuer_id,omitempty"`
	RetainedCRL       *managedLateCRLRecord `json:"retained_crl,omitempty"`
}

func validateManagedLateCRLCache(cache managedLateCachedCRL, latest managedLateCRLRecord, issuerFingerprint string, installedACKs []string, consumer string, mode managedLateObservationMode) error {
	if mode != managedLateObserveMQTTPrior && mode != managedLateObserveACKReady && mode != managedLateObserveOpenBaoTLSPrior || cache.TerminalIssuerID != "" || cache.RetainedCRL != nil || cache.IssuerFingerprint != issuerFingerprint || cache.CRL.IssuerID != latest.IssuerID || cache.CRL.Digest != latest.Digest || cache.CRL.Number != latest.Number || cache.CRL.PEM != latest.PEM || !cache.CRL.ThisUpdate.Equal(latest.ThisUpdate) || !cache.CRL.NextUpdate.Equal(latest.NextUpdate) {
		return errors.New("original installed signed CRL differs")
	}
	seen := map[string]bool{}
	for _, id := range installedACKs {
		if id == "" || seen[id] {
			return errors.New("native CRL ACK inventory differs")
		}
		seen[id] = true
	}
	if !seen[consumer] {
		return errors.New("genuine current digest ACK absent")
	}
	return nil
}

// A source constructor must separately authenticate every plan/body/receipt.
// This scalar guard rejects an old partial owner journal before native reads.
func validateManagedLateAcceptedCore(ids, completed []string, originalOwners, actualOwners map[string]string, accepted, acceptanceStarted bool, failed, inFlight string, actions []string, controllerUpdate bool) error {
	if len(ids) != 16 || len(completed) != 16 || len(actualOwners) != 16 || !accepted || !acceptanceStarted || failed != "" || inFlight != "" || !slices.Equal(ids, completed) || len(actions) != 16 {
		return errors.New("genuine accepted restored Core required")
	}
	for index, id := range ids {
		expected := "retain"
		if index == 14 || index == 13 && controllerUpdate {
			expected = "update"
		}
		if id == "" || actualOwners[id] == "" || actualOwners[id] != originalOwners[id] || actions[index] != expected {
			return errors.New("restored Core original ownership or scope differs")
		}
	}
	return nil
}

// Only durable completed receipts enter this guard. Their exact intents, UIDs,
// bodies and source bindings are authenticated by the completion reader first.
type managedLateCompletionTime struct {
	Name                   string
	StartedAt, CompletedAt time.Time
}

func validateManagedLateCompletionTimes(phases []managedLateCompletionTime, qualifiedAt, completedAt time.Time) error {
	wanted := []string{"seed-authorization", "seed", "custody", "credentials", "cleanup", "cleanup-authorization", "owners", "exclusive-log"}
	if qualifiedAt.IsZero() || completedAt.Before(qualifiedAt) || len(phases) != len(wanted) {
		return errors.New("late completion lifecycle bounds differ")
	}
	previous := qualifiedAt
	for index, phase := range phases {
		if phase.Name != wanted[index] || phase.StartedAt.IsZero() || phase.CompletedAt.IsZero() || phase.StartedAt.Before(previous) || phase.CompletedAt.Before(phase.StartedAt) || phase.CompletedAt.After(completedAt) {
			return errors.New("late completion receipts are out of order")
		}
		previous = phase.CompletedAt
	}
	return nil
}
