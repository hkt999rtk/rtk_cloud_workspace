package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"slices"
	"testing"
	"time"
)

const draftMQTTRoot = "14c1a269-e32e-4eac-aa93-db5ad8e85f7e"

func draftSignedCRL(t *testing.T, number int64, expiry time.Duration, entries []x509.RevocationListEntry, extra []pkix.Extension) (string, string, managedLateCRLRecord, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 13, 22, 0, 0, 0, time.UTC)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Original MQTT Root"}, NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{1, 3, 7}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	thisUpdate := now.Add(-24 * time.Hour)
	raw, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(number), ThisUpdate: thisUpdate, NextUpdate: now.Add(expiry), RevokedCertificateEntries: entries, ExtraExtensions: extra}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	certHash, crlHash := sha256.Sum256(der), sha256.Sum256(raw)
	record := managedLateCRLRecord{IssuerID: draftMQTTRoot, Digest: hex.EncodeToString(crlHash[:]), Number: big.NewInt(number).String(), PEM: string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: raw})), ThisUpdate: thisUpdate, NextUpdate: now.Add(expiry)}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), hex.EncodeToString(certHash[:]), record, now
}

func TestDraftSignedCRLFreshnessHasSeparateRepairAndACKAuthority(t *testing.T) {
	modes := []string{"fresh-ack", "fresh-root1-repair", "expired-root1-repair", "expired-root1-ack", "expired-root2-repair", "expired-service-repair", "expired-other-root-repair", "unknown-mode", "changed-fingerprint", "changed-issuer", "changed-status", "changed-number", "changed-digest", "changed-this-update", "changed-next-update", "changed-signature", "extra-pem", "foreign-authority", "unsupported-extension", "future-revocation", "duplicate-serial", "remove-from-crl-reason"}
	for _, scenario := range modes {
		t.Run(scenario, func(t *testing.T) {
			expiry, number := time.Hour, int64(1)
			if slices.Contains([]string{"expired-root1-repair", "expired-root1-ack", "expired-root2-repair", "expired-service-repair", "expired-other-root-repair"}, scenario) {
				expiry = -time.Hour
			}
			if scenario == "expired-root2-repair" {
				number = 2
			}
			var entries []x509.RevocationListEntry
			var extra []pkix.Extension
			fixed := time.Date(2026, 10, 12, 21, 0, 0, 0, time.UTC)
			if scenario == "future-revocation" {
				entries = []x509.RevocationListEntry{{SerialNumber: big.NewInt(2), RevocationTime: fixed.Add(48 * time.Hour)}}
			}
			if scenario == "duplicate-serial" {
				entries = []x509.RevocationListEntry{{SerialNumber: big.NewInt(2), RevocationTime: fixed}, {SerialNumber: big.NewInt(2), RevocationTime: fixed}}
			}
			if scenario == "remove-from-crl-reason" {
				entries = []x509.RevocationListEntry{{SerialNumber: big.NewInt(2), RevocationTime: fixed, ReasonCode: 8}}
			}
			if scenario == "unsupported-extension" {
				extra = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 3, 4}, Value: []byte{5, 0}}}
			}
			chain, fp, record, now := draftSignedCRL(t, number, expiry, entries, extra)
			domain, issuer, status, mode := "mqtt", draftMQTTRoot, "active", managedLateObserveACKReady
			if scenario == "fresh-root1-repair" || scenario == "expired-root1-repair" || scenario == "expired-root2-repair" || scenario == "expired-service-repair" || scenario == "expired-other-root-repair" {
				mode = managedLateObserveMQTTPrior
			}
			switch scenario {
			case "expired-service-repair":
				domain = "service"
			case "expired-other-root-repair":
				issuer = "22222222-2222-4222-8222-222222222222"
				record.IssuerID = issuer
			case "unknown-mode":
				mode = "unknown"
			case "changed-fingerprint":
				fp = "foreign"
			case "changed-issuer":
				record.IssuerID = "foreign"
			case "changed-status":
				status = "retiring"
			case "changed-number":
				record.Number = "99"
			case "changed-digest":
				record.Digest = "foreign"
			case "changed-this-update":
				record.ThisUpdate = record.ThisUpdate.Add(time.Second)
			case "changed-next-update":
				record.NextUpdate = record.NextUpdate.Add(time.Second)
			case "changed-signature":
				block, _ := pem.Decode([]byte(record.PEM))
				block.Bytes[len(block.Bytes)-1] ^= 1
				record.PEM = string(pem.EncodeToMemory(block))
			case "extra-pem":
				record.PEM += record.PEM
			case "foreign-authority":
				chain, fp, _, _ = draftSignedCRL(t, 1, time.Hour, nil, nil)
			}
			proof, err := validateManagedLateSignedCRL(domain, issuer, fp, status, chain, record, mode, now)
			pass := scenario == "fresh-ack" || scenario == "expired-root1-repair"
			if (err == nil) != pass {
				t.Fatal("wrong signed CRL authority", scenario, err)
			}
			if pass && (proof.Digest != record.Digest || proof.Number != record.Number || proof.NextUpdate != record.NextUpdate) {
				t.Fatal("public proof lost signed dates")
			}
		})
	}
}

func TestDraftCRLCacheRequiresExactLatestSignedBytesAndGenuineACK(t *testing.T) {
	for _, scenario := range []string{"original", "missing-ack", "duplicate-ack", "foreign-fingerprint", "terminal", "retained-terminal", "different-number", "different-pem", "unknown-mode"} {
		t.Run(scenario, func(t *testing.T) {
			_, fp, record, _ := draftSignedCRL(t, 2, time.Hour, nil, nil)
			cache := managedLateCachedCRL{IssuerFingerprint: fp, CRL: record}
			acks := []string{"video-cloud-api", "video-cloud-logingester"}
			mode := managedLateObserveACKReady
			switch scenario {
			case "missing-ack":
				acks = acks[:1]
			case "duplicate-ack":
				acks = append(acks, acks[0])
			case "foreign-fingerprint":
				cache.IssuerFingerprint = "foreign"
			case "terminal":
				cache.TerminalIssuerID = draftMQTTRoot
			case "retained-terminal":
				cache.RetainedCRL = &record
			case "different-number":
				cache.CRL.Number = "3"
			case "different-pem":
				cache.CRL.PEM += "\n"
			case "unknown-mode":
				mode = "unknown"
			}
			err := validateManagedLateCRLCache(cache, record, fp, acks, "video-cloud-logingester", mode)
			if (err == nil) != (scenario == "original") {
				t.Fatal("wrong cache/ACK proof", scenario, err)
			}
		})
	}
}

func TestDraftRestoredCoreRequiresActualAcceptanceAndOriginalScope(t *testing.T) {
	for _, scenario := range []string{"accepted", "old-failed-child", "acceptance-only", "missing-acceptance-start", "missing-owner", "replacement-owner", "completed-order", "extra-owner", "changed-scope", "inflight", "formal-role-image", "wrong-controller-action"} {
		t.Run(scenario, func(t *testing.T) {
			ids, actions := []string{}, []string{}
			original, current := map[string]string{}, map[string]string{}
			for i := 0; i < 16; i++ {
				id := fmt.Sprintf("resource%d", i)
				ids = append(ids, id)
				actions = append(actions, "retain")
				original[id] = fmt.Sprintf("UID%d", i)
				current[id] = original[id]
			}
			actions[13], actions[14] = "update", "update"
			controllerUpdate := true
			completed := slices.Clone(ids)
			accepted, started, failed, inflight := true, true, "", ""
			switch scenario {
			case "old-failed-child":
				completed = completed[:13]
				delete(current, ids[14])
				delete(current, ids[15])
				accepted, started, failed = false, false, ids[13]
			case "acceptance-only":
				accepted = false
			case "missing-acceptance-start":
				started = false
			case "missing-owner":
				delete(current, ids[13])
			case "replacement-owner":
				current[ids[13]] = "foreign"
			case "completed-order":
				completed[0], completed[1] = completed[1], completed[0]
			case "extra-owner":
				current["foreign"] = "foreign"
			case "changed-scope":
				actions[15] = "update"
			case "inflight":
				inflight = ids[15]
			case "formal-role-image":
				controllerUpdate, actions[13] = false, "retain"
			case "wrong-controller-action":
				controllerUpdate = false
			}
			err := validateManagedLateAcceptedCore(ids, completed, original, current, accepted, started, failed, inflight, actions, controllerUpdate)
			if (err == nil) != (scenario == "accepted" || scenario == "formal-role-image") {
				t.Fatal("wrong genuine restored Core proof", scenario, err)
			}
		})
	}
}

func TestDraftLateCompletionRequiresCompletedNonoverlappingNativeLifecycle(t *testing.T) {
	for _, scenario := range []string{"exact", "same-boundary", "missing-phase", "extra-phase", "duplicate-phase", "swapped-phase", "zero-qualification", "zero-start", "zero-complete", "start-before-go", "overlap", "completion-before-start", "after-overall-proof", "late-log-before-owners", "fake-later-cutoff"} {
		t.Run(scenario, func(t *testing.T) {
			at := time.Date(2026, 10, 13, 23, 0, 0, 0, time.UTC)
			names := []string{"seed-authorization", "seed", "custody", "credentials", "cleanup", "cleanup-authorization", "owners", "exclusive-log"}
			phases := []managedLateCompletionTime{}
			for index, name := range names {
				start := at.Add(time.Duration(index*2+1) * time.Minute)
				phases = append(phases, managedLateCompletionTime{name, start, start.Add(time.Minute)})
			}
			done := at.Add(17 * time.Minute)
			switch scenario {
			case "same-boundary":
				phases[1].StartedAt = phases[0].CompletedAt
			case "missing-phase":
				phases = phases[:7]
			case "extra-phase":
				phases = append(phases, phases[7])
			case "duplicate-phase":
				phases[2].Name = phases[1].Name
			case "swapped-phase":
				phases[1], phases[2] = phases[2], phases[1]
			case "zero-qualification":
				at = time.Time{}
			case "zero-start":
				phases[3].StartedAt = time.Time{}
			case "zero-complete":
				phases[3].CompletedAt = time.Time{}
			case "start-before-go":
				phases[0].StartedAt = at.Add(-time.Second)
			case "overlap":
				phases[4].StartedAt = phases[3].CompletedAt.Add(-time.Second)
			case "completion-before-start":
				phases[3].CompletedAt = phases[3].StartedAt.Add(-time.Second)
			case "after-overall-proof":
				phases[7].CompletedAt = done.Add(time.Second)
			case "late-log-before-owners":
				phases[7].StartedAt = phases[6].StartedAt
			case "fake-later-cutoff":
				done = at.Add(15 * time.Minute)
			}
			err := validateManagedLateCompletionTimes(phases, at, done)
			if (err == nil) != (scenario == "exact" || scenario == "same-boundary") {
				t.Fatal("wrong durable lifecycle chronology", scenario, err)
			}
		})
	}
}

func TestDraftOpenBaoTLSPriorExceptionCannotAdmitForeignOrStaleACKAuthority(t *testing.T) {
	for _, scenario := range []string{"fresh-original-one", "expired-original-one", "fresh-two", "expired-two", "foreign-root", "expired-service", "expired-mqtt", "expired-one-ACK"} {
		t.Run(scenario, func(t *testing.T) {
			expiry, number := time.Hour, int64(1)
			if scenario != "fresh-original-one" && scenario != "fresh-two" {
				expiry = -time.Hour
			}
			if scenario == "fresh-two" || scenario == "expired-two" {
				number = 2
			}
			chain, fp, record, now := draftSignedCRL(t, number, expiry, nil, nil)
			domain, issuer, mode := "openbao_tls", "734487b8-abcf-40be-a813-8b52c64f7954", managedLateObserveOpenBaoTLSPrior
			switch scenario {
			case "foreign-root":
				issuer = "99999999-9999-4999-8999-999999999999"
			case "expired-service":
				domain = "service"
			case "expired-mqtt":
				domain = "mqtt"
				issuer = draftMQTTRoot
			case "expired-one-ACK":
				mode = managedLateObserveACKReady
			}
			record.IssuerID = issuer
			_, err := validateManagedLateSignedCRL(domain, issuer, fp, "active", chain, record, mode, now)
			if (err == nil) != (scenario == "fresh-original-one" || scenario == "expired-original-one") {
				t.Fatal("wrong explicitly scoped TLS prior authority", scenario, err)
			}
		})
	}
}
