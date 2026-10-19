package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func rootCRLFixture(t *testing.T) (environmentCreateRegisteredIssuer, *ecdsa.PrivateKey, createRootCRLNativeSnapshot, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated-original-Service-Root"}, NotBefore: now.Add(-10 * 24 * time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	root := environmentCreateRegisteredIssuer{ID: "11111111-1111-4111-8111-111111111111", Environment: "dev", Domain: "service", Kind: "root", Status: "active", Certificate: certificate, ChainPEM: certificate, RootFingerprint: environmentPreflightBytesSHA256(der), BundleVersion: environmentPreflightBytesSHA256([]byte(certificate)), Document: deploymentplan.Object{"signer_provider": "offline"}}
	priorRequest := createRootCRLRequest{root.ID, root.RootFingerprint, "1", now.Add(-2 * 24 * time.Hour), now.Add(-24 * time.Hour), []createRootCRLRevocation{{"63", now.Add(-49 * time.Hour), 1}}}
	prior := signedRootCRLFixture(t, root, key, priorRequest)
	snapshot := createRootCRLNativeSnapshot{Prior: prior, Bindings: []createRootCRLRevocation{{"64", now.Add(-2 * time.Hour), 0}}}
	return root, key, snapshot, now
}
func signedRootCRLFixture(t *testing.T, root environmentCreateRegisteredIssuer, key *ecdsa.PrivateKey, request createRootCRLRequest) createRootCRLRecord {
	t.Helper()
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	entries := []x509.RevocationListEntry{}
	for _, entry := range request.Entries {
		serial, _ := new(big.Int).SetString(entry.Serial, 16)
		entries = append(entries, x509.RevocationListEntry{SerialNumber: serial, RevocationTime: entry.RevokedAt, ReasonCode: entry.Reason})
	}
	number, _ := new(big.Int).SetString(request.Number, 10)
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: number, ThisUpdate: request.ThisUpdate, NextUpdate: request.NextUpdate, RevokedCertificateEntries: entries}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return createRootCRLRecord{root.ID, environmentPreflightBytesSHA256(der), request.Number, string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), request.ThisUpdate, request.NextUpdate}
}

func TestOriginalRootCRLRenewalPreservesRevocationsAndIncludesLiveInventory(t *testing.T) {
	root, key, snapshot, now := rootCRLFixture(t)
	request, err := buildCreateRootCRLRequest(root, snapshot, now)
	if err != nil || request.Number != "2" || len(request.Entries) != 2 || request.Entries[0].Serial != "63" || request.Entries[0].Reason != 1 || request.Entries[1].Serial != "64" || !request.NextUpdate.Equal(now.Add(6*24*time.Hour)) {
		t.Fatal("renewal lost original history or live revocation", err)
	}
	signed := signedRootCRLFixture(t, root, key, request)
	record, err := validateCreateRootCRLSigned(root, snapshot, request, []byte(signed.PEM), now)
	if err != nil || record.Digest != signed.Digest {
		t.Fatal("genuine original-key publication rejected", err)
	}
	if _, err := parseCreateRootCRL(root, snapshot.Prior, true, now); err == nil {
		t.Fatal("expired historical publication became current trust")
	}
	if _, err := parseCreateRootCRL(root, snapshot.Prior, false, now); err != nil {
		t.Fatal("historical signature evidence was discarded", err)
	}
}

func TestOriginalRootCRLRequestRejectsUnprovenOrChangedRevocationHistory(t *testing.T) {
	for _, mode := range []string{"foreign-root", "missing-signature", "changed-time", "changed-reason", "future-revocation", "noncanonical-serial", "remove-from-crl", "wrong-metadata", "genuine-no-revocations"} {
		t.Run(mode, func(t *testing.T) {
			root, key, snapshot, now := rootCRLFixture(t)
			switch mode {
			case "foreign-root":
				root.RootFingerprint = strings.Repeat("f", 64)
			case "missing-signature":
				snapshot.Prior.PEM = ""
			case "changed-time":
				snapshot.Bindings = append(snapshot.Bindings, createRootCRLRevocation{"63", now.Add(-time.Hour), 0})
			case "changed-reason":
				snapshot.Bindings = append(snapshot.Bindings, createRootCRLRevocation{"63", now.Add(-49 * time.Hour), 2})
			case "future-revocation":
				snapshot.Bindings[0].RevokedAt = now.Add(time.Second)
			case "noncanonical-serial":
				snapshot.Bindings[0].Serial = "064"
			case "remove-from-crl":
				snapshot.Bindings[0].Reason = 8
			case "wrong-metadata":
				snapshot.Prior.Digest = strings.Repeat("f", 64)
			case "genuine-no-revocations":
				snapshot.Bindings = nil
				snapshot.Prior = signedRootCRLFixture(t, root, key, createRootCRLRequest{root.ID, root.RootFingerprint, "1", now.Add(-2 * 24 * time.Hour), now.Add(-24 * time.Hour), []createRootCRLRevocation{}})
			}
			request, err := buildCreateRootCRLRequest(root, snapshot, now)
			if (err == nil) != (mode == "genuine-no-revocations") {
				t.Fatal("unproven history accepted", mode, err)
			}
			if mode == "genuine-no-revocations" && len(request.Entries) != 0 {
				t.Fatal("invented revocation")
			}
		})
	}
}

func TestOriginalRootCRLSignedPublicationRejectsRollbackOmissionAndUnreviewedContent(t *testing.T) {
	for _, mode := range []string{"original", "rollback", "omission", "changed-old-reason", "changed-time", "expired", "bad-signature"} {
		t.Run(mode, func(t *testing.T) {
			root, key, snapshot, now := rootCRLFixture(t)
			request, err := buildCreateRootCRLRequest(root, snapshot, now)
			if err != nil {
				t.Fatal(err)
			}
			signedRequest := request
			signedRequest.Entries = append([]createRootCRLRevocation(nil), request.Entries...)
			switch mode {
			case "rollback":
				signedRequest.Number = "1"
			case "omission":
				signedRequest.Entries = signedRequest.Entries[1:]
			case "changed-old-reason":
				signedRequest.Entries[0].Reason = 2
			case "changed-time":
				signedRequest.ThisUpdate = now.Add(-time.Hour)
			case "expired":
				signedRequest.ThisUpdate = now.Add(-2 * time.Hour)
				signedRequest.NextUpdate = now.Add(-time.Hour)
			}
			signed := signedRootCRLFixture(t, root, key, signedRequest)
			if mode == "bad-signature" {
				block, _ := pem.Decode([]byte(signed.PEM))
				block.Bytes[len(block.Bytes)-1] ^= 1
				signed.PEM = string(pem.EncodeToMemory(block))
			}
			if _, err := validateCreateRootCRLSigned(root, snapshot, request, []byte(signed.PEM), now); (err == nil) != (mode == "original") {
				t.Fatal("unsafe signed publication accepted", mode, err)
			}
			if mode == "changed-old-reason" {
				if _, err := validateCreateRootCRLSigned(root, snapshot, signedRequest, []byte(signed.PEM), now); err == nil {
					t.Fatal("reviewed request erased prior signed history")
				}
			}
		})
	}
}

func rootCRLTailFixture() (environmentCreateJournal, createRootCRLRecoveryArtifact, environmentCreateJournal) {
	source := managedCheckpointFixture()
	source.Stack = "video-cloud-dev"
	now := time.Now().UTC().Add(-time.Minute)
	plan := createRootCRLRecoveryPlan{Schema: createRootCRLRecoverySchema, Parent: strings.Repeat("b", 64), Candidate: "deployment/repairs/root-crl-isolated", Root: environmentCreateRegisteredIssuer{ID: "11111111-1111-4111-8111-111111111111", RootFingerprint: strings.Repeat("a", 64)}, Snapshot: createRootCRLNativeSnapshot{Prior: createRootCRLRecord{Digest: strings.Repeat("c", 64)}}, Signed: createRootCRLRecord{Digest: strings.Repeat("d", 64)}, Secret: activeTransportRenewalResource{ID: "Secret/video-cloud-dev-video-cloud/pki-create-service-root-crl", UID: "original-secret", Desired: deploymentplan.Object{"kind": "Secret"}}}
	for _, name := range []string{"pki-controller", "certissuer"} {
		plan.Targets = append(plan.Targets, createRootCRLStateTarget{ID: "Deployment/video-cloud-dev-video-cloud/" + name, UID: "original-" + name, Container: name, ClaimID: "PersistentVolumeClaim/video-cloud-dev-video-cloud/" + environmentCreateServiceOwnerClaim(name), ClaimUID: "original-claim-" + name, PodName: name + "-original", PodUID: "original-pod-" + name, Paths: []string{environmentCreateServiceState + "/private/crl/listener-root.json", environmentCreateServiceState + "/private/crl/outbound-root.json"}})
	}
	a := createRootCRLRecoveryArtifact{Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	review := deploymentplan.Digest(plan)
	key := "service-root-crl-renewal-" + review
	proof := createRootCRLRecoveryProof{Schema: createRootCRLRecoverySchema, Parent: plan.Parent, Review: review, File: plan.Candidate + "/signed/revocations.pem", Digest: plan.Signed.Digest, Completed: true}
	current := source
	current.Entries = append([]environmentCreateJournalEntry(nil), source.Entries...)
	appendStep := func(key string, intent, receipt any) {
		raw, _ := canonicalEnvironmentCreateJSON(mustProviderRecoveryJSON(receipt))
		current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "completed", StartedAt: now.Add(time.Second), CompletedAt: now.Add(2 * time.Second), IntentSHA256: activeTransportJournalDigest(intent), Receipt: raw})
	}
	appendStep(key, plan, proof)
	appendStep(key+"/native-import", map[string]string{"issuer_id": plan.Root.ID, "root_sha256": plan.Root.RootFingerprint, "prior_crl_sha256": plan.Snapshot.Prior.Digest, "signed_crl_sha256": plan.Signed.Digest, "request_sha256": deploymentplan.Digest(plan.Request)}, map[string]any{"native_crl": plan.Signed, "request_sha256": deploymentplan.Digest(plan.Request)})
	appendStep(key+"/original-secret", plan.Secret, map[string]string{"resource_id": plan.Secret.ID, "uid": plan.Secret.UID, "desired_sha256": deploymentplan.Digest(plan.Secret.Desired)})
	for _, target := range plan.Targets {
		appendStep(key+"/state-"+target.Container, map[string]any{"target": target, "native_crl_sha256": plan.Signed.Digest}, map[string]any{"resource_id": target.ID, "uid": target.UID, "crl_sha256": plan.Signed.Digest, "paths": target.Paths, "installed_on_disk": true, "runtime_reload_required": true})
	}
	return current, a, source
}

func TestOriginalRootCRLProofAuthenticatesCanonicalCompleteNativeSteps(t *testing.T) {
	for _, mode := range []string{"complete", "typed-digest", "wrong-native-crl", "wrong-secret-owner", "missing-state", "unknown-step", "expired-admission", "future-unrelated-work"} {
		t.Run(mode, func(t *testing.T) {
			current, a, source := rootCRLTailFixture()
			start := len(source.Entries)
			switch mode {
			case "typed-digest":
				current.Entries[start].IntentSHA256 = deploymentplan.Digest(a.Plan)
			case "wrong-native-crl":
				current.Entries[start+1].Receipt = mustProviderRecoveryJSON(map[string]string{"native": "different"})
			case "wrong-secret-owner":
				current.Entries[start+2].Receipt = mustProviderRecoveryJSON(map[string]string{"uid": "replacement"})
			case "missing-state":
				current.Entries = current.Entries[:len(current.Entries)-1]
			case "unknown-step":
				current.Entries[start+4].Key += "-unknown"
			case "expired-admission":
				current.Entries[start].StartedAt = a.ValidUntil
			case "future-unrelated-work":
				current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "later-qualified-work", State: "pending"})
			}
			want := mode == "complete" || mode == "future-unrelated-work"
			if rootCRLJournalTailValid(current, a, source, true) != want {
				t.Fatal("unsafe completion proof", mode)
			}
			if mode == "future-unrelated-work" && rootCRLJournalTailValid(current, a, source, false) {
				t.Fatal("historical CRL proof authorized unrelated pending work")
			}
		})
	}
}

func TestOriginalRootCRLRequestDigestUsesNativeFieldOrder(t *testing.T) {
	root, _, snapshot, now := rootCRLFixture(t)
	request, err := buildCreateRootCRLRequest(root, snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	if deploymentplan.Digest(request) != environmentPreflightBytesSHA256(raw) {
		t.Fatal("native request digest changed")
	}
	if deploymentplan.Digest(request) == activeTransportJournalDigest(request) {
		t.Fatal("fixture does not detect typed versus canonical journal digest")
	}
}

func TestOriginalRootCRLRepairDirectoriesUsePrivateDirectoryLayout(t *testing.T) {
	for _, mode := range []string{"new", "existing", "symlink-deployment", "symlink-repairs", "symlink-candidate", "wrong-prefix", "unclean-path", "absolute"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			candidate := "deployment/repairs/root-crl-isolated-directory"
			outside := t.TempDir()
			originalOutside, err := os.Lstat(outside)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(outside, "preserve.txt")
			if err := os.WriteFile(marker, []byte("outside original"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "existing":
				if err := os.MkdirAll(filepath.Join(store.Root, candidate), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(store.Root, candidate, "original.json"), []byte("original archive"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-deployment":
				if err := os.Symlink(outside, filepath.Join(store.Root, "deployment")); err != nil {
					t.Fatal(err)
				}
			case "symlink-repairs":
				if err := os.Mkdir(filepath.Join(store.Root, "deployment"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(store.Root, "deployment/repairs")); err != nil {
					t.Fatal(err)
				}
			case "symlink-candidate":
				if err := os.MkdirAll(filepath.Join(store.Root, "deployment/repairs"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(store.Root, candidate)); err != nil {
					t.Fatal(err)
				}
			case "wrong-prefix":
				candidate = "deployment/repairs/unrelated"
			case "unclean-path":
				candidate = "deployment/repairs/root-crl-isolated/../../escape"
			case "absolute":
				candidate = filepath.Join(store.Root, candidate)
			}
			err = prepareCreateRootCRLRepairDirectory(store, candidate)
			success := mode == "new" || mode == "existing"
			if (err == nil) != success {
				t.Fatal("incorrect directory qualification", mode, err)
			}
			if success {
				for _, relative := range []string{"deployment", "deployment/repairs", candidate} {
					info, err := os.Lstat(filepath.Join(store.Root, relative))
					if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
						t.Fatal("repair directory is not private real0700", relative, err)
					}
				}
				if err := prepareCreateRootCRLRepairDirectory(store, candidate); err != nil {
					t.Fatal("existing archive directory could not be safely reused", err)
				}
				if mode == "existing" {
					raw, err := os.ReadFile(filepath.Join(store.Root, candidate, "original.json"))
					if err != nil || string(raw) != "original archive" {
						t.Fatal("original archive changed", err)
					}
				}
			}
			raw, err := os.ReadFile(marker)
			if err != nil || string(raw) != "outside original" {
				t.Fatal("outside original material changed", err)
			}
			info, err := os.Lstat(outside)
			if err != nil || info.Mode().Perm() != originalOutside.Mode().Perm() {
				t.Fatal("outside directory changed", err)
			}
		})
	}
}

func rootCRLCoreOwnerFixture(t *testing.T, mode string) (secretStore, *environmentCreateExecution, string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	parent, token := strings.Repeat("a", 64), strings.Repeat("b", 64)
	root, _, _, _ := rootCRLFixture(t)
	root.ID = "80448538-2c59-4861-997f-cca90c3a1fc3"
	root.Reference = "pki-issuers/device/" + root.ID + "/v1"
	ready := deploymentplan.Object{"issuer_id": root.ID, "environment": "dev", "trust_domain": "device", "kind": "root", "status": "ready", "certificate_pem": root.Certificate, "chain_pem": root.ChainPEM, "certificate_fingerprint_sha256": root.RootFingerprint, "trust_bundle_version": root.BundleVersion, "signer_reference": root.Reference}
	before := deviceTrustCheckpointFixture()
	before.PlanSHA256, before.OwnerToken, before.Stack = parent, token, "video-cloud-dev"
	before.Entries[1].Receipt = mustProviderRecoveryJSON(map[string]string{"environment": "dev", "issuer_id": root.ID, "signer_reference": root.Reference, "root_sha256": root.RootFingerprint, "bundle_version": root.BundleVersion})
	defs := func(size int, forward bool) []deploymentplan.Definition {
		var result []deploymentplan.Definition
		for i := 0; i < size; i++ {
			name, kind, phase := "fixture-"+big.NewInt(int64(i)).String(), "ConfigMap", "configuration"
			if i == 0 || i == 1 && !forward {
				kind, phase, name = "Deployment", "workload", "pki-controller"
				if i == 1 {
					name = "certissuer"
				}
			}
			value := "openbao"
			if forward {
				value = "final-device-forward"
			}
			result = append(result, deploymentplan.Definition{Phase: phase, Object: deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud", "annotations": deploymentplan.Object{"fixture-source": value}}}})
		}
		return result
	}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: before.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("c", 64), ReleaseSHA256: strings.Repeat("d", 64), BindingsSHA256: strings.Repeat("e", 64)}
	original, err := deploymentplan.Compile(inputs, defs(16, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := deploymentplan.Compile(inputs, defs(7, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{ParentSHA: parent, ParentJournalSHA: deploymentplan.Digest(before), Materials: map[string]string{"accepted-service-core-device-trust-forward-plan": candidate.SHA256(), "accepted-service-core-device-trust-original-plan": strings.Repeat("f", 64), "accepted-service-core-device-trust-root": deploymentplan.Digest(ready)}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	base := "deployment/create/openbao-recovery/" + review
	write(base+"/qualification.json", a)
	write(base+"/source-journal.json", before)
	write("deployment/create/"+parent+"/bootstrap/device/root-ready.json", ready)
	if mode == "candidate-body" {
		deploymentplan.Map(candidate.Resources[0].Desired["metadata"])["name"] = "unreviewed-controller"
	}
	write(base+"/openbao-root-trust-plan.json", candidate)
	runtime := *candidate
	runtime.Resources = append([]deploymentplan.Resource(nil), candidate.Resources...)
	// The real accepted stage has a different scoped execution binding, while
	// the entire admitted resource plan remains identical.
	runtime.BindingsSHA256 = strings.Repeat("9", 64)
	if mode == "runtime-body" {
		runtime.Resources[0].Desired = deploymentplan.Clone(runtime.Resources[0].Desired)
		deploymentplan.Map(runtime.Resources[0].Desired["metadata"])["name"] = "unreviewed-controller"
	}
	e := &environmentCreateExecution{Journal: before}
	e.Journal.Entries = append([]environmentCreateJournalEntry(nil), before.Entries...)
	stage := "kubernetes/device-root-trust-policy-forward-" + review
	forward := map[string]string{"actual_qualified_forward_stage": stage, "original_failed_plan_sha256": strings.Repeat("f", 64), "original_root_issuer_id": root.ID}
	if mode == "wrong-original" {
		forward["original_failed_plan_sha256"] = strings.Repeat("7", 64)
	}
	if mode == "wrong-root" {
		forward["original_root_issuer_id"] = "11111111-1111-4111-8111-111111111111"
	}
	e.Journal.Entries[2].State, e.Journal.Entries[2].CompletedAt, e.Journal.Entries[2].Receipt = "completed", time.Now(), mustProviderRecoveryJSON(forward)
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})})
	alias := "kubernetes/device-root-trust-policy-forward-" + strings.Repeat("6", 64)
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: alias, State: "completed", CompletedAt: time.Now(), Receipt: mustProviderRecoveryJSON(forward)})
	accept := func(key string, plan *deploymentplan.Plan) {
		journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, Owners: map[string]string{}}
		for _, resource := range plan.Resources {
			journal.Completed = append(journal.Completed, resource.ID)
			journal.Owners[resource.ID] = "original-" + resource.ID
		}
		if key == stage && mode == "replacement-owner" {
			journal.Owners["Deployment/video-cloud-dev-video-cloud/pki-controller"] = "replacement-controller"
		}
		if key == stage && mode == "unaccepted" {
			journal.Accepted = false
		}
		write("deployment/rollback/"+plan.SHA256()+"/plan.json", plan)
		write("deployment/rollback/"+plan.SHA256()+"/journal.json", journal)
		intent, err := environmentCreateKubernetesStageIntent(plan)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := environmentCreatePlanSHA256(intent)
		if err != nil {
			t.Fatal(err)
		}
		e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: key, State: "completed", CompletedAt: time.Now(), IntentSHA256: hash, Receipt: mustProviderRecoveryJSON(map[string]string{"plan_sha256": plan.SHA256()})})
	}
	accept("kubernetes/openbao-intermediate-trust", original)
	accept(stage, &runtime)
	return store, e, alias
}

func TestOriginalRootCRLCoreOwnersSelectAdmittedFinalForwardAndRejectDrift(t *testing.T) {
	for _, mode := range []string{"original-with-closed-alias", "wrong-original", "wrong-root", "candidate-body", "runtime-body", "replacement-owner", "unaccepted"} {
		t.Run(mode, func(t *testing.T) {
			store, e, alias := rootCRLCoreOwnerFixture(t, mode)
			resources, owners, err := loadCreateRootCRLCoreOwners(store, e)
			if mode != "original-with-closed-alias" {
				if err == nil {
					t.Fatal("unreviewed accepted forward drift admitted", mode)
				}
				return
			}
			if err != nil || len(resources) != 2 || len(owners) != 2 {
				t.Fatal("genuine accepted forward rejected due to closed alias", err)
			}
			for name, want := range map[string]string{"pki-controller": "final-device-forward", "certissuer": "openbao"} {
				id := "Deployment/video-cloud-dev-video-cloud/" + name
				if owners[id] != "original-"+id || deploymentplan.Map(deploymentplan.Map(resources[id].Desired["metadata"])["annotations"])["fixture-source"] != want {
					t.Fatal("wrong original owner or latest admitted resource body selected", name)
				}
			}
			if _, _, err := acceptedCreateStage(store, e, alias); err == nil {
				t.Fatal("closed alias became an accepted runtime plan")
			}
		})
	}
}
