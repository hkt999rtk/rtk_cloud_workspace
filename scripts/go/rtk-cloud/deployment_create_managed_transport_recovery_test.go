package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedCheckpointFixture() environmentCreateJournal {
	j := environmentCreateJournal{Environment: "dev"}
	for _, key := range []string{"device-root-bootstrap", "kubernetes/device-root-active", "kubernetes/device-root-trust", "kubernetes/app-root-trust", "kubernetes/app-intermediate-trust", "pki-reserve-app-root", "pki-reserve-app-intermediate", "kubernetes/transport-client-enrollment", "kubernetes/app-root-trust-broker-forward-" + strings.Repeat("a", 64), "openbao-role-certissuer-dev-3ba365a9d611"} {
		j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: key, State: "completed", CompletedAt: time.Now(), IntentSHA256: strings.Repeat("b", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"native": "original"})})
	}
	return j
}

func TestManagedTransportCheckpointRejectsIncompleteOrAlreadyStartedCutover(t *testing.T) {
	for _, mode := range []string{"original", "pending", "allocated", "missing-receipt", "missing-activation", "duplicate", "second-forward", "transport-written", "application-written", "managed-issuer-written", "completed-repair"} {
		t.Run(mode, func(t *testing.T) {
			j := managedCheckpointFixture()
			switch mode {
			case "pending":
				j.Entries[0].State = "pending"
			case "allocated":
				j.Entries[0].State = "allocated"
			case "missing-receipt":
				j.Entries[0].Receipt = nil
			case "missing-activation":
				j.Entries[1].Key = "other-device-stage"
			case "duplicate":
				j.Entries = append(j.Entries, j.Entries[0])
			case "second-forward":
				v := j.Entries[8]
				v.Key = "kubernetes/app-root-trust-broker-forward-" + strings.Repeat("c", 64)
				j.Entries = append(j.Entries, v)
			case "transport-written":
				v := j.Entries[0]
				v.Key = "kubernetes/transport-enrollment-issuer"
				j.Entries = append(j.Entries, v)
			case "application-written":
				v := j.Entries[0]
				v.Key = "kubernetes/managed-application-owners"
				j.Entries = append(j.Entries, v)
			case "managed-issuer-written":
				v := j.Entries[0]
				v.Key = "kubernetes/managed-complete-issuer"
				j.Entries = append(j.Entries, v)
			case "completed-repair":
				v := j.Entries[0]
				v.Key = "qualified-active-temporary-tls-renewal-" + strings.Repeat("c", 64)
				j.Entries = append(j.Entries, v)
			}
			want := mode == "original" || mode == "completed-repair"
			profile, flag := createRecoveryCheckpointProfile(j)
			if isManagedTransportCheckpoint(j) != want || (profile == "all-ca-active-managed-transport") != want || want && flag != "--after-application-enrollment-trust" {
				t.Fatalf("unsafe checkpoint mode %s, profile %s", mode, profile)
			}
		})
	}
}

func managedPublicationFixture(t *testing.T) (secretStore, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer) {
	t.Helper()
	_, _, _, service := createServiceRecipeFixture(t, "dev")
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	roots, intermediates := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
	for i, domain := range managedPKIDomains {
		root := service.Root
		root.Document = deploymentplan.Clone(service.Root.Document)
		root.ID = string(rune('1'+i)) + "1111111-1111-4111-8111-111111111111"
		root.Domain = domain
		root.Document["issuer_id"], root.Document["trust_domain"] = root.ID, domain
		roots[domain] = root
		if domain != "device" {
			intermediate := service.Intermediate
			intermediate.Document = deploymentplan.Clone(service.Intermediate.Document)
			intermediate.ID = string(rune('1'+i)) + "2222222-2222-4222-8222-222222222222"
			intermediate.Domain = domain
			intermediate.Document["issuer_id"], intermediate.Document["trust_domain"], intermediate.Document["parent_issuer_id"] = intermediate.ID, domain, root.ID
			intermediates[domain] = intermediate
		}
	}
	if err := saveEnvironmentCreateRegisteredIssuers(store, roots, intermediates); err != nil {
		t.Fatal(err)
	}
	return store, roots, intermediates
}

func TestManagedTransportPublicationVerificationPreservesFilesAndRejectsDifferentLineage(t *testing.T) {
	for _, mode := range []string{"original", "wrong-parent", "root-ready", "changed-file", "missing-file", "unexpected-intermediate"} {
		t.Run(mode, func(t *testing.T) {
			store, roots, intermediates := managedPublicationFixture(t)
			path := filepath.Join(store.Root, "pki/app/issuer.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "wrong-parent":
				v := intermediates["app"]
				v.Document["parent_issuer_id"] = roots["service"].ID
				intermediates["app"] = v
			case "root-ready":
				v := roots["app"]
				v.Status = "ready"
				roots["app"] = v
			case "changed-file":
				if err := os.WriteFile(path, []byte(`{"root":{"status":"active","issuer_id":"foreign"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unexpected-intermediate":
				intermediates["device"] = intermediates["app"]
			}
			materials := map[string]string{}
			err = verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, materials)
			if (err == nil) != (mode == "original") {
				t.Fatalf("unsafe publication mode %s", mode)
			}
			if mode == "original" {
				after, err := os.ReadFile(path)
				if err != nil || string(before) != string(after) || len(materials) != len(managedPKIDomains) {
					t.Fatal("verification changed original publication or omitted evidence")
				}
			}
			if mode == "missing-file" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("verification regenerated missing publication")
				}
			}
		})
	}
}

func TestManagedReadyIntermediateRetainsIdentityAndOriginalStatus(t *testing.T) {
	store, _, intermediates := managedPublicationFixture(t)
	active := intermediates["app"]
	e := &environmentCreateExecution{Journal: environmentCreateJournal{PlanSHA256: strings.Repeat("a", 64)}}
	relative := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/app/intermediate-ready.json")
	readyDoc := deploymentplan.Clone(active.Document)
	readyDoc["status"] = "ready"
	if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, readyDoc); err != nil {
		t.Fatal(err)
	}
	ready, err := originalReadyIntermediate(store, e, active)
	if err != nil || ready.Status != "ready" || ready.ID != active.ID || ready.Reference != active.Reference || ready.ChainPEM != active.ChainPEM {
		t.Fatal("original READY identity changed", err)
	}
	active.Reference = "different-provider-key"
	if _, err := originalReadyIntermediate(store, e, active); err == nil {
		t.Fatal("changed provider signer accepted")
	}
}

func managedPublicationJournalFixture(t *testing.T) (secretStore, *environmentCreateExecution, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer) {
	t.Helper()
	store, roots, intermediates := managedPublicationFixture(t)
	e := &environmentCreateExecution{Journal: environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("a", 64)}}
	write := func(relative string, value any) {
		t.Helper()
		if err := store.write(relative, mustProviderRecoveryJSON(value), true); err != nil {
			t.Fatal(err)
		}
	}
	operator := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	now := time.Now().UTC().Truncate(time.Second)
	for index, domain := range managedPKIDomains {
		if domain == "device" {
			continue
		}
		root := roots[domain]
		root.Document["signer_provider"] = "offline"
		root.Document["created_by"] = operator
		roots[domain] = root
		publication := deploymentplan.Object{"root": root.Document, "intermediate": intermediates[domain].Document}
		write(filepath.Join("pki", domain, "issuer.json"), publication)
		rootOperation := ""
		for k, kind := range []string{"root", "intermediate"} {
			issuer := root
			if kind == "intermediate" {
				issuer = intermediates[domain]
			}
			operation := environmentCreateIssuerOperation{ID: fmt.Sprintf("%08d-3333-4333-8333-333333333333", index*2+k+1), IssuerID: issuer.ID, Digest: strings.Repeat("b", 64), Status: "requested"}
			if kind == "root" {
				rootOperation = operation.ID
			}
			e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "pki-reserve-" + domain + "-" + kind, State: "completed", CompletedAt: now, Receipt: mustProviderRecoveryJSON(operation)}, environmentCreateJournalEntry{Key: "pki-activate-" + operation.ID, State: "completed", CompletedAt: now, Receipt: mustProviderRecoveryJSON(issuer.Document)})
			write(filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap", domain, kind+"-active.json"), issuer.Document)
		}
		paths := map[string]string{"key": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem")), "certificate": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "certificate.pem")), "csr": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "csr.pem")), "passphrase": "pki/custody/" + domain + "-root-v1-key-passphrase"}
		hashes := map[string]string{}
		for key, path := range paths {
			raw := []byte("isolated-original-" + domain + "-" + key)
			if key == "certificate" {
				raw = []byte(root.Certificate)
			}
			if err := store.write(path, raw, false); err != nil {
				t.Fatal(err)
			}
			hashes[key] = environmentPreflightBytesSHA256(raw)
		}
		// These extra fields are mandatory legitimate native custody metadata. The
		// custody signer reference intentionally differs from the registered issuer
		// reference, as in the original offline ceremony.
		manifest := map[string]any{"schema": "rtk-root-custody/v1", "environment": "dev", "trust_domain": domain, "root_issuer_id": root.ID, "certificate_fingerprint_sha256": root.RootFingerprint, "operator_user_id": operator, "ceremony_operation_id": rootOperation, "signer_reference": "isolated-original-custody-key", "key_format": "encrypted-pkcs8-pem", "paths": paths, "file_sha256": hashes, "created_at": now, "verified_at": now}
		write(filepath.Join("pki/custody/roots", root.ID, "manifest.json"), manifest)
	}
	return store, e, roots, intermediates
}

func TestManagedPublicationJournalAcceptsCompleteNativeCustodyAndRejectsChangedPins(t *testing.T) {
	for _, mode := range []string{"complete", "reservation", "activation", "active-file", "custody-operation", "custody-certificate-pin", "custody-key-hash", "changed-key", "unknown-custody-field"} {
		t.Run(mode, func(t *testing.T) {
			store, e, roots, intermediates := managedPublicationJournalFixture(t)
			root := roots["service"]
			manifestPath := filepath.Join(store.Root, "pki/custody/roots", root.ID, "manifest.json")
			raw, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if json.Unmarshal(raw, &manifest) != nil {
				t.Fatal("fixture manifest")
			}
			stage := ""
			switch mode {
			case "reservation":
				e.Journal.Entries[slices.IndexFunc(e.Journal.Entries, func(entry environmentCreateJournalEntry) bool { return entry.Key == "pki-reserve-service-root" })].Receipt = mustProviderRecoveryJSON(environmentCreateIssuerOperation{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", IssuerID: "different"})
				stage = "reservation-pin"
			case "activation":
				reserveIndex := slices.IndexFunc(e.Journal.Entries, func(entry environmentCreateJournalEntry) bool { return entry.Key == "pki-reserve-service-root" })
				e.Journal.Entries[reserveIndex+1].Receipt = mustProviderRecoveryJSON(map[string]string{"status": "other"})
				stage = "activation-receipt"
			case "active-file":
				if err := store.write(filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/root-active.json"), []byte(`{"issuer_id":"different"}`), true); err != nil {
					t.Fatal(err)
				}
				stage = "active-file-pin"
			case "custody-operation":
				manifest["ceremony_operation_id"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
				stage = "custody-manifest-pin"
			case "custody-certificate-pin":
				manifest["certificate_fingerprint_sha256"] = strings.Repeat("f", 64)
				stage = "custody-manifest-pin"
			case "custody-key-hash":
				deploymentplan.Map(manifest["file_sha256"])["key"] = strings.Repeat("f", 64)
				stage = "custody-file-pin-key"
			case "changed-key":
				if err := store.write(filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem"), []byte("changed"), true); err != nil {
					t.Fatal(err)
				}
				stage = "custody-file-pin-key"
			case "unknown-custody-field":
				manifest["unrecognized_authority"] = "unexpected"
				stage = "custody-manifest-decode"
			}
			if err := os.WriteFile(manifestPath, mustProviderRecoveryJSON(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			err = verifyManagedPublicationJournal(store, e, roots, intermediates)
			if (err == nil) != (mode == "complete") {
				t.Fatal("unsafe original evidence verification", mode, err)
			}
			if err != nil && (!strings.Contains(err.Error(), "domain=service kind=root stage="+stage) || strings.Contains(err.Error(), "isolated-original")) {
				t.Fatal("diagnosis lost the public stage or exposed private fixture material", err)
			}
		})
	}
}

func TestManagedIssuerPublicationFullSchemaIncludesIntermediate(t *testing.T) {
	store, _, _, _ := managedPublicationJournalFixture(t)
	var publication struct {
		Root         map[string]any `json:"root"`
		Intermediate map[string]any `json:"intermediate"`
	}
	if err := readExecutionJSON(filepath.Join(store.Root, "pki/service/issuer.json"), &publication); err != nil || publication.Root["kind"] != "root" || publication.Intermediate["kind"] != "intermediate" {
		t.Fatal("legitimate complete published lineage was rejected", err)
	}
}

func TestManagedCapabilityLineageAllowsOnlyFiveVerifiedPublications(t *testing.T) {
	for _, mode := range []string{"managed", "fresh-no-allowance", "missing-publication-pin", "missing-custody-owner", "changed-publication", "extra-domain-file", "legacy-service-publication", "extra-root", "extra-passphrase", "symlink-publication", "symlink-domain", "public-permissions"} {
		t.Run(mode, func(t *testing.T) {
			store, _, roots, _ := managedPublicationJournalFixture(t)
			verifiedRoots := map[string]bool{}
			custody := map[string]bool{"roots": true}
			publications := map[string]string{}
			original := map[string]string{}
			for _, domain := range managedPKIDomains {
				relative := "pki/" + domain + "/issuer.json"
				raw, err := store.read(relative)
				if err != nil {
					t.Fatal(err)
				}
				original[relative] = raw
				publications[relative] = environmentPreflightBytesSHA256([]byte(raw))
				if domain != "device" {
					verifiedRoots[roots[domain].ID] = true
					custody[domain+"-root-v1-key-passphrase"] = true
				}
			}
			path := filepath.Join(store.Root, "pki/device/issuer.json")
			switch mode {
			case "fresh-no-allowance":
				publications = nil
			case "missing-publication-pin":
				delete(publications, "pki/device/issuer.json")
			case "missing-custody-owner":
				delete(verifiedRoots, roots["service"].ID)
			case "changed-publication":
				if err := store.write("pki/device/issuer.json", []byte(`{"root":{"issuer_id":"different"}}`), true); err != nil {
					t.Fatal(err)
				}
			case "extra-domain-file":
				if err := store.write("pki/device/unverified.json", []byte("unverified"), false); err != nil {
					t.Fatal(err)
				}
			case "legacy-service-publication":
				if err := store.write("pki/services/issuer.json", []byte("legacy"), false); err != nil {
					t.Fatal(err)
				}
			case "extra-root":
				if err := os.Mkdir(filepath.Join(store.Root, "pki/custody/roots/unverified-root"), 0700); err != nil {
					t.Fatal(err)
				}
			case "extra-passphrase":
				if err := store.write("pki/custody/device-root-v1-key-passphrase", []byte("unverified"), false); err != nil {
					t.Fatal(err)
				}
			case "symlink-publication":
				outside := filepath.Join(t.TempDir(), "issuer.json")
				if err := os.WriteFile(outside, []byte(original["pki/device/issuer.json"]), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "symlink-domain":
				originalDir := filepath.Join(store.Root, "original-device-publication")
				if err := os.Rename(filepath.Dir(path), originalDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(originalDir, filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			case "public-permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			err := qualifyEnvironmentCreateBootstrapLineage(store, verifiedRoots, custody, publications)
			if (err == nil) != (mode == "managed") {
				t.Fatal("unsafe published continuation allowance", mode, err)
			}
			if mode == "managed" {
				for relative, want := range original {
					got, err := store.read(relative)
					if err != nil || got != want {
						t.Fatal("qualification rewrote an original public issuer", relative, err)
					}
				}
			}
		})
	}
}

func TestManagedCapabilityPublicationHashesCannotReplaceNativeBodyProof(t *testing.T) {
	store, e, roots, intermediates := managedPublicationJournalFixture(t)
	root := roots["service"]
	changed := deploymentplan.Clone(root.Document)
	changed["issuer_id"] = "11111111-1111-4111-8111-111111111111"
	if err := store.write("pki/service/issuer.json", mustProviderRecoveryJSON(deploymentplan.Object{"root": changed, "intermediate": intermediates["service"].Document}), true); err != nil {
		t.Fatal(err)
	}
	// Even a caller that recomputes the changed file's bytes cannot qualify its
	// publication against the independently retained native ACTIVE documents.
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, nil); err == nil {
		t.Fatal("repinned foreign publication accepted")
	}
	if err := verifyManagedPublicationJournal(store, e, roots, intermediates); err != nil {
		t.Fatal("unchanged native source fixture should remain authentic", err)
	}
}
