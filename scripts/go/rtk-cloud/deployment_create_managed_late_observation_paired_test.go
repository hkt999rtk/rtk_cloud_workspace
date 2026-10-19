package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLatePairedCryptographicFixture(t *testing.T) (map[string]environmentCreateRegisteredIssuer, map[string]managedLateCRLRecord, time.Time) {
	t.Helper()
	roots := map[string]environmentCreateRegisteredIssuer{}
	records := map[string]managedLateCRLRecord{}
	now := time.Now().UTC().Truncate(time.Second)
	for _, item := range []struct{ id, domain string }{{"14c1a269-e32e-4eac-aa93-db5ad8e85f7e", "mqtt"}, {"734487b8-abcf-40be-a813-8b52c64f7954", "openbao_tls"}} {
		root, key, _, _ := rootCRLFixture(t)
		root.ID, root.Domain = item.id, item.domain
		request := createRootCRLRequest{root.ID, root.RootFingerprint, "1", now.Add(-48 * time.Hour), now.Add(-24 * time.Hour), nil}
		signed := signedRootCRLFixture(t, root, key, request)
		roots[root.ID] = root
		records[root.ID] = managedLateCRLRecord{IssuerID: signed.IssuerID, Digest: signed.Digest, Number: signed.Number, PEM: signed.PEM, ThisUpdate: signed.ThisUpdate, NextUpdate: signed.NextUpdate}
	}
	return roots, records, now
}

func TestManagedLatePairedPriorOnlyAcceptsExactTwoOriginalSignedRoots(t *testing.T) {
	roots, originals, now := managedLatePairedCryptographicFixture(t)
	for _, scenario := range []string{"mqtt-original1", "tls-original1", "changed-body", "changed-number", "changed-root", "expired-ICA", "expired-other-domain", "expired-foreign-root", "ACK-mode", "TLS-only-mode", "missing-mqtt", "extra-root", "changed-signature"} {
		t.Run(scenario, func(t *testing.T) {
			selected := "734487b8-abcf-40be-a813-8b52c64f7954"
			if scenario == "mqtt-original1" {
				selected = "14c1a269-e32e-4eac-aa93-db5ad8e85f7e"
			}
			root := roots[selected]
			current := originals[selected]
			captured := map[string]managedLateCRLRecord{}
			for id, record := range originals {
				captured[id] = record
			}
			mode := managedLateObserveMQTTPrior
			switch scenario {
			case "changed-body":
				// Another genuinely signed #1 from the same root is still not the
				// original completed receipt. Altering metadata cannot bless it.
				current.PEM += "\n"
			case "changed-number":
				current.Number = "2"
			case "changed-root":
				root.RootFingerprint = strings.Repeat("f", 64)
			case "expired-ICA":
				root.ID = "48b05111-1111-4111-8111-111111111111"
				current.IssuerID = root.ID
			case "expired-other-domain":
				root.Domain = "service"
			case "expired-foreign-root":
				root.ID = "22222222-2222-4222-8222-222222222222"
				current.IssuerID = root.ID
			case "ACK-mode":
				mode = managedLateObserveACKReady
			case "TLS-only-mode":
				mode = managedLateObserveOpenBaoTLSPrior
			case "missing-mqtt":
				delete(captured, "14c1a269-e32e-4eac-aa93-db5ad8e85f7e")
			case "extra-root":
				captured["foreign"] = current
			case "changed-signature":
				current.PEM = strings.Replace(current.PEM, "A", "B", 1)
			}
			_, err := validateManagedLatePairedPriorCRL(root.Domain, root.ID, root.RootFingerprint, root.Status, root.ChainPEM, current, captured, mode, now)
			pass := scenario == "mqtt-original1" || scenario == "tls-original1"
			if (err == nil) != pass {
				t.Fatalf("wrong paired historical authority for %s: %v", scenario, err)
			}
		})
	}
	// Even the very same original TLS#1 bytes never become current ACK authority.
	tls := roots["734487b8-abcf-40be-a813-8b52c64f7954"]
	if _, err := validateManagedLateSignedCRL(tls.Domain, tls.ID, tls.RootFingerprint, tls.Status, tls.ChainPEM, originals[tls.ID], managedLateObserveACKReady, now); err == nil {
		t.Fatal("paired history leaked into ACK-ready freshness")
	}
}

func TestManagedLatePairedOriginalReceiptsCannotBlessChangedRootOne(t *testing.T) {
	for _, scenario := range []string{"original", "changed-file", "changed-file-and-receipt", "changed-number", "wrong-intent", "pending", "duplicate", "foreign-file", "foreign-root", "ACK-mode"} {
		t.Run(scenario, func(t *testing.T) {
			roots, records, now := managedLatePairedCryptographicFixture(t)
			configRoot := t.TempDir()
			store := secretStore{ConfigRoot: configRoot, Root: filepath.Join(configRoot, "dev"), Environment: "dev"}
			if err := os.Chmod(configRoot, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(store.Root, 0700); err != nil {
				t.Fatal(err)
			}
			parent := strings.Repeat("a", 64)
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{PlanSHA256: parent}}
			mqtt, tls := roots["14c1a269-e32e-4eac-aa93-db5ad8e85f7e"], roots["734487b8-abcf-40be-a813-8b52c64f7954"]
			r := &createServiceSessionRecovery{MQTTRoot: &mqtt, OpenBaoRoot: &tls}
			for _, root := range []environmentCreateRegisteredIssuer{mqtt, tls} {
				relative := filepath.ToSlash(filepath.Join("deployment/create", parent, "bootstrap", root.Domain, "root-crl/revocations.pem"))
				raw := []byte(records[root.ID].PEM)
				if err := store.write(relative, raw, true); err != nil {
					t.Fatal(err)
				}
				receipt := map[string]string{"issuer_id": root.ID, "crl_file": relative, "crl_pem_sha256": environmentPreflightBytesSHA256(raw)}
				intent := deploymentplan.Object{"issuer_id": root.ID, "root_sha256": root.RootFingerprint, "crl_number": "1"}
				entry := environmentCreateJournalEntry{Key: "pki-initial-root-crl-" + root.Domain, Phase: "bootstrap", State: "completed", StartedAt: now.Add(-48 * time.Hour), CompletedAt: now.Add(-47 * time.Hour), IntentSHA256: activeTransportJournalDigest(intent)}
				if root.Domain == "openbao_tls" {
					switch scenario {
					case "changed-file":
						if err := store.write(relative, append(raw, '\n'), true); err != nil {
							t.Fatal(err)
						}
					case "changed-file-and-receipt":
						// Authentic source pin prevents this mutable replacement in the real
						// loader; here also show a replacement body fails its signed number.
						altered := strings.Replace(string(raw), "A", "B", 1)
						if err := store.write(relative, []byte(altered), true); err != nil {
							t.Fatal(err)
						}
						receipt["crl_pem_sha256"] = environmentPreflightBytesSHA256([]byte(altered))
					case "changed-number":
						intent["crl_number"] = "2"
						entry.IntentSHA256 = activeTransportJournalDigest(intent)
					case "wrong-intent":
						entry.IntentSHA256 = strings.Repeat("f", 64)
					case "pending":
						entry.State = "pending"
					case "foreign-file":
						receipt["crl_file"] = "pki/foreign.pem"
					case "foreign-root":
						receipt["issuer_id"] = mqtt.ID
					}
				}
				entry.Receipt, _ = json.Marshal(receipt)
				e.Journal.Entries = append(e.Journal.Entries, entry)
			}
			if scenario == "duplicate" {
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[1])
			}
			mode := managedLateSessionMQTTRepair
			if scenario == "ACK-mode" {
				mode = managedLateSessionACKReady
			}
			actual, err := managedLatePairedOriginalRecords(store, e, r, mode)
			if (err == nil) != (scenario == "original") {
				t.Fatalf("wrong original receipt authority: %s %v", scenario, err)
			}
			if err == nil && deploymentplan.Digest(actual) != deploymentplan.Digest(records) {
				t.Fatal("original signed body lost")
			}
		})
	}
}

func TestManagedLatePairedEvidenceOnlyAllowsMonotonicACKsAndPostImportTLS(t *testing.T) {
	_, records, now := managedLatePairedCryptographicFixture(t)
	fixture := &managedLatePairedCRLPriorEvidence{Schema: managedLatePairedCRLPriorSchema, SourceSHA: strings.Repeat("a", 64), CoreSourceSHA: strings.Repeat("b", 64), AcceptedCoreSHA: strings.Repeat("c", 64), Roots: map[string]managedLateRootPriorEvidence{}}
	for _, id := range []string{"14c1a269-e32e-4eac-aa93-db5ad8e85f7e", "734487b8-abcf-40be-a813-8b52c64f7954"} {
		names := []string{"video-cloud-api", "video-cloud-logingester"}
		if id != "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" {
			names = []string{"certissuer", "pki-controller"}
		}
		fixture.Roots[id] = managedLateRootPriorEvidence{RootID: id, OriginalRecord: records[id], SnapshotSHA: strings.Repeat("d", 64), CustodySHA: strings.Repeat("e", 64), RevocationsSHA: strings.Repeat("f", 64), ACKs: []managedLatePriorACK{{IssuerID: id, Digest: records[id].Digest, Consumer: names[0], At: now.Add(-36 * time.Hour)}, {IssuerID: id, Digest: records[id].Digest, Consumer: names[1], At: now.Add(-35 * time.Hour)}}}
	}
	for _, scenario := range []string{"original", "ACK-advanced", "ACK-regressed", "ACK-changed-roster", "changed-root-body", "changed-custody", "changed-native-revocations", "changed-ICA", "changed-cache-owner", "changed-source", "missing-root", "foreign-root", "post-import-TLS", "post-import-both", "post-import-changed-TLS"} {
		t.Run(scenario, func(t *testing.T) {
			raw, _ := json.Marshal(fixture)
			var actual managedLatePairedCRLPriorEvidence
			if err := json.Unmarshal(raw, &actual); err != nil {
				t.Fatal(err)
			}
			id := "734487b8-abcf-40be-a813-8b52c64f7954"
			item := actual.Roots[id]
			after := strings.HasPrefix(scenario, "post-import-")
			switch scenario {
			case "ACK-advanced":
				item.ACKs[0].At = item.ACKs[0].At.Add(time.Minute)
			case "ACK-regressed":
				item.ACKs[0].At = item.ACKs[0].At.Add(-time.Minute)
			case "ACK-changed-roster":
				item.ACKs[0].Consumer = "foreign"
			case "changed-root-body":
				item.OriginalRecord.PEM += "\n"
			case "changed-custody":
				item.CustodySHA = strings.Repeat("a", 64)
			case "changed-native-revocations":
				item.RevocationsSHA = strings.Repeat("a", 64)
			case "changed-ICA":
				item.ICARecord.Number = "99"
			case "changed-cache-owner":
				item.Consumers = []createRootCRLStateTarget{{UID: "replacement"}}
			case "changed-source":
				actual.SourceSHA = strings.Repeat("f", 64)
			case "missing-root":
				delete(actual.Roots, "14c1a269-e32e-4eac-aa93-db5ad8e85f7e")
			case "foreign-root":
				actual.Roots["foreign"] = item
			case "post-import-changed-TLS":
				item.OriginalRecord.Number = "2"
			}
			actual.Roots[id] = item
			if after && scenario != "post-import-both" {
				delete(actual.Roots, "14c1a269-e32e-4eac-aa93-db5ad8e85f7e")
			}
			pass := scenario == "original" || scenario == "ACK-advanced" || scenario == "post-import-TLS"
			if managedLatePairedCRLPriorMatches(fixture, &actual, after) != pass {
				t.Fatalf("wrong paired evidence monotonic authority: %s", scenario)
			}
		})
	}
}

func TestManagedLatePairedHistoricalACKMustPrecedeSignedExpiry(t *testing.T) {
	_, records, now := managedLatePairedCryptographicFixture(t)
	prior := records["734487b8-abcf-40be-a813-8b52c64f7954"]
	for _, scenario := range []string{"genuine", "at-expiry", "after-expiry", "before-publication", "future", "duplicate", "foreign-digest"} {
		t.Run(scenario, func(t *testing.T) {
			acks := []managedLatePriorACK{{IssuerID: prior.IssuerID, Digest: prior.Digest, Consumer: "certissuer", At: prior.ThisUpdate.Add(time.Hour)}, {IssuerID: prior.IssuerID, Digest: prior.Digest, Consumer: "pki-controller", At: prior.ThisUpdate.Add(2 * time.Hour)}}
			switch scenario {
			case "at-expiry":
				acks[0].At = prior.NextUpdate
			case "after-expiry":
				acks[0].At = prior.NextUpdate.Add(time.Second)
			case "before-publication":
				acks[0].At = prior.ThisUpdate.Add(-time.Second)
			case "future":
				acks[0].At = now.Add(time.Minute)
			case "duplicate":
				acks[1].Consumer = acks[0].Consumer
			case "foreign-digest":
				acks[0].Digest = strings.Repeat("f", 64)
			}
			err := validateManagedLatePriorACKRoster(acks, prior.IssuerID, prior, []string{"certissuer", "pki-controller"}, now)
			if (err == nil) != (scenario == "genuine") {
				t.Fatalf("impossible historical native ACK accepted: %s %v", scenario, err)
			}
		})
	}
}

func TestManagedLatePairedOwnersUseActualAcceptedCoreRoleImage(t *testing.T) {
	id := "Deployment/video-cloud-dev-video-cloud/certissuer"
	old := deploymentplan.Resource{ID: id, Desired: deploymentplan.Object{"image": "original-business-image"}}
	current := old
	current.Desired = deploymentplan.Object{"image": "genuine-formal-CertIssuer-image"}
	uid := "11111111-1111-4111-8111-111111111111"
	r := &createServiceSessionRecovery{ManagedCorePartial: &createManagedCorePartialRecovery{Apps: &deploymentplan.Plan{Resources: []deploymentplan.Resource{old}}, AppsJournal: deploymentplan.Journal{Owners: map[string]string{id: uid}}}}
	late := &createManagedLateSessionRecovery{AcceptedCore: &deploymentplan.Plan{Resources: []deploymentplan.Resource{current}}, AcceptedCoreJournal: deploymentplan.Journal{Owners: map[string]string{id: uid}}}
	actual := managedLatePairedAcceptedOwners(r, late)[id]
	if actual.UID != uid || deploymentplan.Digest(actual.Desired) != deploymentplan.Digest(current.Desired) {
		t.Fatal("historical Apps body replaced genuine accepted Core image authority")
	}
}

func TestManagedLatePairedPriorRejectsGenuineResignedNumberOne(t *testing.T) {
	_, originals, now := managedLatePairedCryptographicFixture(t)
	root, key, _, _ := rootCRLFixture(t)
	root.ID, root.Domain = "734487b8-abcf-40be-a813-8b52c64f7954", "openbao_tls"
	request := createRootCRLRequest{root.ID, root.RootFingerprint, "1", now.Add(-48 * time.Hour), now.Add(-24 * time.Hour), nil}
	initial := signedRootCRLFixture(t, root, key, request)
	original := managedLateCRLRecord{IssuerID: initial.IssuerID, Digest: initial.Digest, Number: initial.Number, PEM: initial.PEM, ThisUpdate: initial.ThisUpdate, NextUpdate: initial.NextUpdate}
	originals[root.ID] = original
	request.Entries = []createRootCRLRevocation{{Serial: "63", RevokedAt: now.Add(-49 * time.Hour), Reason: 1}}
	changed := signedRootCRLFixture(t, root, key, request)
	current := managedLateCRLRecord{IssuerID: changed.IssuerID, Digest: changed.Digest, Number: changed.Number, PEM: changed.PEM, ThisUpdate: changed.ThisUpdate, NextUpdate: changed.NextUpdate}
	if _, err := validateManagedLateSignedCRL(root.Domain, root.ID, root.RootFingerprint, root.Status, root.ChainPEM, current, managedLateObserveOpenBaoTLSPrior, now); err != nil {
		t.Fatal("fixture is not a genuinely signed same-root replacement #1", err)
	}
	if _, err := validateManagedLatePairedPriorCRL(root.Domain, root.ID, root.RootFingerprint, root.Status, root.ChainPEM, current, originals, managedLateObserveMQTTPrior, now); err == nil {
		t.Fatal("a genuine signature replaced the authenticated original number-one body")
	}
}

func TestManagedLatePairedDependencyDiagnosisPreservesOnlyKnownPublicStages(t *testing.T) {
	for _, scenario := range []struct {
		name, domain, part string
		err                error
		want               string
	}{
		{"TLS-cache-permission", "openbao_tls", "targets", openBaoTLSRootCRLError("owned-regular0600-cache"), "paired-openbao_tls-targets-owned-regular0600-cache"},
		{"TLS-ICA-signature", "openbao_tls", "targets", openBaoTLSRootCRLError("signed-ICA3-cache"), "paired-openbao_tls-targets-signed-ICA3-cache"},
		{"MQTT-owner", "mqtt", "targets", mqttRootCRLError("one-current-owned-Pod"), "paired-mqtt-targets-one-current-owned-Pod"},
		{"custody-file", "openbao_tls", "custody", errors.New("private-path secret-token"), "paired-openbao_tls-custody-native-or-private-read"},
		{"unknown-public-stage", "mqtt", "targets", mqttRootCRLError("secret-token"), "paired-mqtt-targets-native-or-private-read"},
		{"stage-plus-sensitive-tail", "openbao_tls", "targets", openBaoTLSRootCRLError("signed-root-cache; secret-token"), "paired-openbao_tls-targets-native-or-private-read"},
		{"foreign-domain", "foreign-secret-token", "targets", errors.New("private"), "paired-unknown-dependency"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			message := managedLatePairedDependencyError(scenario.domain, scenario.part, scenario.err).Error()
			if !strings.Contains(message, "stage="+scenario.want+";") || strings.Contains(message, "secret-token") || strings.Contains(message, "private-path") {
				t.Fatalf("unsafe or unhelpful paired dependency diagnosis: %s", message)
			}
		})
	}
}
