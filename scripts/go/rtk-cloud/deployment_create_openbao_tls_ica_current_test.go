package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func openBaoTLSCurrentICAFixture(t *testing.T) (environmentCreateRegisteredIssuer, environmentCreateRegisteredIssuer, *ecdsa.PrivateKey, createRootCRLNativeSnapshot, []createRootCRLRecord, time.Time) {
	t.Helper()
	f := newManagedLateBootstrapChainFixture(t)
	root, ica := f.ceremony.Root, f.ceremony.Intermediate
	root.ID, root.Domain = "734487b8-abcf-40be-a813-8b52c64f7954", "openbao_tls"
	ica.ID, ica.Environment, ica.Domain, ica.Kind, ica.Status = openBaoTLSOriginalIntermediateID, "dev", "openbao_tls", "intermediate", "active"
	ica.RootFingerprint, ica.ChainPEM = root.RootFingerprint, ica.Certificate+root.Certificate
	ica.Reference = "pki-issuers/openbao_tls/" + ica.ID + "/v1"
	ica.Document = deploymentplan.Object{"parent_issuer_id": root.ID, "signer_provider": "openbao"}
	entries := []createRootCRLRevocation{{"63", f.now.Add(-10 * time.Hour), 1}, {"64", f.now.Add(-6 * time.Hour), 0}, {"65", f.now.Add(-3 * time.Hour), 0}}
	prior := signedRootCRLFixture(t, ica, f.icaKey, createRootCRLRequest{ica.ID, "", "3", f.now.Add(-9 * time.Hour), f.now.Add(-8 * time.Hour), entries[:1]})
	middle := signedRootCRLFixture(t, ica, f.icaKey, createRootCRLRequest{ica.ID, "", "4", f.now.Add(-5 * time.Hour), f.now.Add(-4 * time.Hour), entries[:2]})
	current := signedRootCRLFixture(t, ica, f.icaKey, createRootCRLRequest{ica.ID, "", "5", f.now.Add(-time.Hour), f.now.Add(48 * time.Hour), entries})
	snapshot := createRootCRLNativeSnapshot{Prior: current, Bindings: []createRootCRLRevocation{entries[2]}}
	return root, ica, f.icaKey, snapshot, []createRootCRLRecord{prior, middle, current}, f.now
}

func TestOpenBaoTLSCurrentICARequiresOriginalSignedMonotonicCompleteHistory(t *testing.T) {
	root, ica, key, snapshot, history, now := openBaoTLSCurrentICAFixture(t)
	prior := history[0]
	if err := validateOpenBaoTLSCurrentIntermediate(root, ica, prior, snapshot, history, now); err != nil {
		t.Fatalf("genuine automatic original-key ICA3 to ICA5 rejected: %v", err)
	}
	for _, mode := range []string{"foreign-root", "changed-root-pin", "foreign-ICA", "foreign-parent", "foreign-provider", "foreign-certificate", "missing-original", "changed-original", "reordered-history", "duplicate-number", "changed-envelope", "bad-signature", "missing-old-serial", "changed-old-reason", "changed-old-time", "missing-live-revocation", "expired-current", "future-current", "rollback-current"} {
		t.Run(mode, func(t *testing.T) {
			selectedRoot, selectedICA, current := root, ica, snapshot
			selectedHistory := append([]createRootCRLRecord(nil), history...)
			revocations := []createRootCRLRevocation{{"63", now.Add(-10 * time.Hour), 1}, {"64", now.Add(-6 * time.Hour), 0}, {"65", now.Add(-3 * time.Hour), 0}}
			request := createRootCRLRequest{ica.ID, "", "5", now.Add(-time.Hour), now.Add(48 * time.Hour), revocations}
			resign := false
			switch mode {
			case "foreign-root":
				selectedRoot.ID = "11111111-1111-4111-8111-111111111111"
			case "changed-root-pin":
				selectedRoot.RootFingerprint = strings.Repeat("f", 64)
			case "foreign-ICA":
				selectedICA.ID = "11111111-1111-4111-8111-111111111111"
			case "foreign-parent":
				selectedICA.Document = deploymentplan.Object{"parent_issuer_id": "11111111-1111-4111-8111-111111111111", "signer_provider": "openbao"}
			case "foreign-provider":
				selectedICA.Document = deploymentplan.Object{"parent_issuer_id": root.ID, "signer_provider": "offline"}
			case "foreign-certificate":
				other := newManagedLateBootstrapChainFixture(t)
				selectedICA.Certificate, selectedICA.ChainPEM = other.ceremony.Intermediate.Certificate, other.ceremony.Intermediate.Certificate+root.Certificate
			case "missing-original":
				selectedHistory = selectedHistory[1:]
			case "changed-original":
				selectedHistory[0].Digest = strings.Repeat("f", 64)
			case "reordered-history":
				selectedHistory[1], selectedHistory[2] = selectedHistory[2], selectedHistory[1]
			case "duplicate-number":
				selectedHistory = append(selectedHistory[:2], selectedHistory[1], selectedHistory[2])
			case "changed-envelope":
				current.Prior.NextUpdate = current.Prior.NextUpdate.Add(time.Minute)
				selectedHistory[2] = current.Prior
			case "bad-signature":
				otherRoot, otherKey, _, _ := rootCRLFixture(t)
				current.Prior = signedRootCRLFixture(t, otherRoot, otherKey, request)
				current.Prior.IssuerID = ica.ID
				selectedHistory[2] = current.Prior
			case "missing-old-serial":
				request.Entries, resign = request.Entries[1:], true
			case "changed-old-reason":
				request.Entries[0].Reason, resign = 2, true
			case "changed-old-time":
				request.Entries[0].RevokedAt, resign = now.Add(-11*time.Hour), true
			case "missing-live-revocation":
				request.Entries, resign = request.Entries[:2], true
			case "expired-current":
				request.NextUpdate, resign = now.Add(-time.Second), true
			case "future-current":
				request.ThisUpdate, resign = now.Add(time.Second), true
			case "rollback-current":
				current.Prior = prior
				selectedHistory = selectedHistory[:1]
			}
			if resign {
				current.Prior = signedRootCRLFixture(t, ica, key, request)
				selectedHistory[2] = current.Prior
			}
			if err := validateOpenBaoTLSCurrentIntermediate(selectedRoot, selectedICA, prior, current, selectedHistory, now); err == nil {
				t.Fatal("changed authority, signature, history or revocation content accepted")
			}
		})
	}
}

func TestOpenBaoTLSCurrentICAFreshnessSeparatesAdmissionFromContinuation(t *testing.T) {
	root, ica, key, snapshot, history, now := openBaoTLSCurrentICAFixture(t)
	request := createRootCRLRequest{ica.ID, "", snapshot.Prior.Number, now.Add(-time.Minute), now.Add(44 * time.Minute), []createRootCRLRevocation{{"63", now.Add(-10 * time.Hour), 1}, {"64", now.Add(-6 * time.Hour), 0}, {"65", now.Add(-3 * time.Hour), 0}}}
	snapshot.Prior = signedRootCRLFixture(t, ica, key, request)
	history[len(history)-1] = snapshot.Prior
	if err := validateOpenBaoTLSCurrentIntermediate(root, ica, history[0], snapshot, history, now); err == nil {
		t.Fatal("initial admission accepted less than the 45-minute freshness margin")
	}
	if err := validateOpenBaoTLSCurrentIntermediateRemaining(root, ica, history[0], snapshot, history, now, 0); err != nil {
		t.Fatal("already admitted continuation rejected a current unchanged CRL", err)
	}
	if err := validateOpenBaoTLSCurrentIntermediateRemaining(root, ica, history[0], snapshot, history, request.NextUpdate, 0); err == nil {
		t.Fatal("continuation accepted an expired CRL")
	}
}

func TestOpenBaoTLSCurrentICACacheRequiresExactLatestSignedRecord(t *testing.T) {
	_, ica, _, snapshot, history, _ := openBaoTLSCurrentICAFixture(t)
	cert, _ := parseFirstCertificatePEM([]byte(ica.Certificate))
	for _, mode := range []string{"latest5", "old3", "foreign-fingerprint", "terminal", "retained", "changed-envelope"} {
		t.Run(mode, func(t *testing.T) {
			state := deploymentplan.Object{"issuer_fingerprint": environmentPreflightBytesSHA256(cert.Raw), "crl": snapshot.Prior}
			switch mode {
			case "old3":
				state["crl"] = history[0]
			case "foreign-fingerprint":
				state["issuer_fingerprint"] = strings.Repeat("f", 64)
			case "terminal":
				state["terminal_issuer_id"] = ica.ID
			case "retained":
				state["retained_crl"] = history[0]
			case "changed-envelope":
				record := snapshot.Prior
				record.Number = "6"
				state["crl"] = record
			}
			err := validateOpenBaoTLSRootCRLCache(mustProviderRecoveryJSON(state), ica, snapshot.Prior, true)
			if (err == nil) != (mode == "latest5") {
				t.Fatal("current cache proof accepted stale or replaced signed state", err)
			}
		})
	}
}

func TestOpenBaoTLSCurrentICAInstallationUsesRealTwoACKsAndLatestReread(t *testing.T) {
	for _, mode := range []string{"latest5", "missing-consumer", "foreign-consumer", "foreign-digest", "future-ACK", "old-ACK", "foreign-ICA", "changed-after-ACK", "changed-revocations-after-ACK"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, original := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, original, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			_, ica, key, expected, _, now := openBaoTLSCurrentICAFixture(t)
			latest := expected
			rows := []managedLatePriorACK{{ica.ID, expected.Prior.Digest, "certissuer", now.Add(-30 * time.Second)}, {ica.ID, expected.Prior.Digest, "pki-controller", now.Add(-20 * time.Second)}}
			switch mode {
			case "missing-consumer":
				rows = rows[:1]
			case "foreign-consumer":
				rows[1].Consumer = "foreign"
			case "foreign-digest":
				rows[1].Digest = strings.Repeat("f", 64)
			case "future-ACK":
				rows[1].At = now.Add(time.Minute)
			case "old-ACK":
				rows[1].At = expected.Prior.ThisUpdate.Add(-time.Second)
			case "foreign-ICA":
				ica.ID = "11111111-1111-4111-8111-111111111111"
			case "changed-after-ACK":
				latest.Prior = signedRootCRLFixture(t, ica, key, createRootCRLRequest{ica.ID, "", "6", now, now.Add(48 * time.Hour), nil})
			case "changed-revocations-after-ACK":
				latest.Bindings = append(append([]createRootCRLRevocation(nil), latest.Bindings...), createRootCRLRevocation{"66", now.Add(-time.Minute), 0})
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "acks.json"), mustProviderRecoveryJSON(rows), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), mustProviderRecoveryJSON(latest), 0600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
set -eu
for query do :; done
printf '%s\n' "$query" >> "$RTK_TLS_ICA_TEST_DIR/queries"
case "$query" in
 *"FROM pki_crl_acknowledgments WHERE issuer_id='48b05140-6167-46c0-b448-562945af691b'"*) cat "$RTK_TLS_ICA_TEST_DIR/acks.json" ;;
 *"FROM pki_crls WHERE issuer_id='48b05140-6167-46c0-b448-562945af691b'"*) cat "$RTK_TLS_ICA_TEST_DIR/snapshot.json" ;;
 *) exit 73 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
			t.Setenv("RTK_TLS_ICA_TEST_DIR", dir)
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-tls-ica-native-proof")}}
			got, err := verifyOpenBaoTLSCurrentIntermediateInstallation(context.Background(), cfg, runtime, ica, expected)
			if (err == nil) != (mode == "latest5") || mode == "latest5" && deploymentplan.Digest(got) != deploymentplan.Digest(rows) {
				t.Fatal("real current ACK/rotation-race proof result differs", mode, err)
			}
			if mode == "latest5" || strings.HasSuffix(mode, "after-ACK") {
				queries, err := os.ReadFile(filepath.Join(dir, "queries"))
				if err != nil || !strings.Contains(string(queries), "pki_crl_acknowledgments") || !strings.Contains(string(queries), "pki_certificate_bindings") {
					t.Fatal("native installation proof omitted real ACK or latest inventory reread")
				}
			}
		})
	}
}

func TestOpenBaoTLSCurrentICARevokedCertificatesMustUseOriginalKey(t *testing.T) {
	root, ica, key, snapshot, history, now := openBaoTLSCurrentICAFixture(t)
	f := newManagedLateBootstrapChainFixture(t)
	// The certificate is genuinely signed, but by a different isolated ICA.
	snapshot.Certificates = append(snapshot.Certificates, struct {
		PEM       string    `json:"certificate_pem"`
		RevokedAt time.Time `json:"revoked_at"`
	}{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.leaf})), now.Add(-2 * time.Hour)})
	_ = key
	if err := validateOpenBaoTLSCurrentIntermediate(root, ica, history[0], snapshot, history, now); err == nil {
		t.Fatal("foreign signed certificate became original ICA revocation inventory")
	}
	// This test's fixtures and all native backend responses remain isolated.
	if _, err := json.Marshal(snapshot); err != nil {
		t.Fatal(err)
	}
}
