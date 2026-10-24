package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func activeTransportFixture(t *testing.T) (secretStore, string, environmentCreateTransport, []byte, time.Time) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	parent := strings.Repeat("a", 64)
	now := time.Now().UTC().Truncate(time.Second)
	original, err := createEnvironmentTransport(store, parent, "management", map[string][]string{"account-manager": {"account-manager.video-cloud-dev-account-manager.svc"}, "pki-controller": {"pki-controller.video-cloud-dev-video-cloud.svc"}}, now.Add(-5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	old, key, err := renewInitialTransportMaterial(original, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return store, parent, old, key, now
}

func TestActiveTransportSameKeyPreservesAuthorityAndEveryServingIdentity(t *testing.T) {
	_, _, old, key, now := activeTransportFixture(t)
	renewed, err := renewActiveTransportSameKey(old, key, now)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := parseFirstCertificatePEM([]byte(old.CACert))
	after, _ := parseFirstCertificatePEM([]byte(renewed.CACert))
	if old.CACert == renewed.CACert || !bytes.Equal(before.RawSubjectPublicKeyInfo, after.RawSubjectPublicKeyInfo) || !bytes.Equal(before.RawSubject, after.RawSubject) || deploymentplan.Digest(before.Extensions) != deploymentplan.Digest(after.Extensions) {
		t.Fatal("original temporary authority key or constraints changed")
	}
	if after.NotAfter.Sub(after.NotBefore) != 24*time.Hour+time.Minute || after.CheckSignatureFrom(after) != nil {
		t.Fatal("renewed original authority is not the bounded self-signed identity")
	}
	for name, original := range old.Identities {
		candidate := renewed.Identities[name]
		if candidate.Key != original.Key || candidate.Certificate == original.Certificate {
			t.Fatal("original serving key changed or certificate was not renewed")
		}
		leaf, _ := parseFirstCertificatePEM([]byte(candidate.Certificate))
		if leaf.CheckSignatureFrom(after) != nil {
			t.Fatal("renewed leaf lacks original authority")
		}
	}
	if err := verifyActiveTransportSameKey(old, renewed, key, now); err != nil {
		t.Fatal(err)
	}
}

func TestActiveTransportSameKeyRejectsChangedKeysConstraintsAndExpiry(t *testing.T) {
	_, _, old, key, now := activeTransportFixture(t)
	good, err := renewActiveTransportSameKey(old, key, now)
	if err != nil {
		t.Fatal(err)
	}
	clone := func(value environmentCreateTransport) environmentCreateTransport {
		raw, _ := json.Marshal(value)
		var out environmentCreateTransport
		_ = json.Unmarshal(raw, &out)
		return out
	}
	mutations := map[string]func(environmentCreateTransport) environmentCreateTransport{
		"missing-leaf": func(c environmentCreateTransport) environmentCreateTransport {
			delete(c.Identities, "account-manager")
			return c
		},
		"changed-leaf-key": func(c environmentCreateTransport) environmentCreateTransport {
			i := c.Identities["account-manager"]
			i.Key = old.Identities["pki-controller"].Key
			c.Identities["account-manager"] = i
			return c
		},
		"extra-leaf": func(c environmentCreateTransport) environmentCreateTransport {
			c.Identities["unknown"] = c.Identities["account-manager"]
			return c
		},
		"old-leaf": func(c environmentCreateTransport) environmentCreateTransport {
			c.Identities["account-manager"] = old.Identities["account-manager"]
			return c
		},
		"old-authority": func(c environmentCreateTransport) environmentCreateTransport { c.CACert = old.CACert; return c },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if verifyActiveTransportSameKey(old, mutate(clone(good)), key, now) == nil {
				t.Fatal("changed original identity or projection accepted")
			}
		})
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(private)
	wrong := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for name, bad := range map[string][]byte{"wrong-authority-key": wrong, "missing-authority-key": nil, "not-private-key": []byte(good.CACert)} {
		t.Run(name, func(t *testing.T) {
			if _, err := renewActiveTransportSameKey(old, bad, now); err == nil {
				t.Fatal("missing authority custody regenerated or accepted")
			}
		})
	}
	if verifyActiveTransportSameKey(old, good, key, now.Add(23*time.Hour)) == nil {
		t.Fatal("certificate unable to cover renewal execution accepted")
	}
}

func TestActiveTransportConsumerRestartChangesOnlyPublicTemplateAnnotation(t *testing.T) {
	original := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "certissuer", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"annotations": deploymentplan.Object{"original": "retained"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "certissuer", "image": "immutable-original", "env": []any{deploymentplan.Object{"name": "OPENBAO_CACERT", "value": "original-public-roots"}}}}}}}}
	renewed := activeTransportConsumerRestart(original, "same-key-ca")
	got := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(renewed["spec"])["template"])["metadata"])
	annotations := deploymentplan.Map(got["annotations"])
	if annotations["original"] != "retained" || annotations["rtk.realtek.com/temporary-openbao-ca-sha256"] != environmentPreflightBytesSHA256([]byte("same-key-ca")) {
		t.Fatal("exact public trust restart absent")
	}
	delete(annotations, "rtk.realtek.com/temporary-openbao-ca-sha256")
	if deploymentplan.Digest(renewed) != deploymentplan.Digest(original) {
		t.Fatal("restart changed image, environment, keys, PVCs or unrelated owner state")
	}
}

func TestActiveTransportBodyRetainsUIDAndRejectsConcurrentDrift(t *testing.T) {
	original := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "tls", "namespace": "dev"}, "data": deploymentplan.Object{"tls.key": "original-key", "tls.crt": "old-cert"}}
	desired := deploymentplan.Clone(original)
	deploymentplan.Map(desired["data"])["tls.crt"] = "new-cert"
	actual := deploymentplan.Clone(original)
	deploymentplan.Map(actual["metadata"])["uid"] = "original-uid"
	deploymentplan.Map(actual["metadata"])["resourceVersion"] = "42"
	deploymentplan.Map(actual["data"])["unrelated-original-entry"] = "retained-original-value"
	r := activeTransportRenewalResource{ID: "Secret/dev/tls", UID: "original-uid", Before: original, Desired: desired}
	body, err := activeTransportResourceBody(r, actual)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Map(body["metadata"])["resourceVersion"] != "42" || deploymentplan.Map(body["data"])["tls.key"] != "original-key" || deploymentplan.Map(body["data"])["unrelated-original-entry"] != "retained-original-value" || !deploymentplan.Matches(desired, body) {
		t.Fatal("original UID, precondition or serving key lost")
	}
	for name, change := range map[string]func(deploymentplan.Object){"owner": func(o deploymentplan.Object) { deploymentplan.Map(o["metadata"])["uid"] = "different-owner" }, "key": func(o deploymentplan.Object) { deploymentplan.Map(o["data"])["tls.key"] = "different-key" }, "certificate": func(o deploymentplan.Object) { deploymentplan.Map(o["data"])["tls.crt"] = "unqualified-cert" }} {
		t.Run(name, func(t *testing.T) {
			bad := deploymentplan.Clone(actual)
			change(bad)
			if _, err := activeTransportResourceBody(r, bad); err == nil {
				t.Fatal("unqualified concurrent mutation accepted")
			}
		})
	}
	if _, err := activeTransportResourceBody(r, body); err != nil {
		t.Fatal("exact completed write could not be reconciled", err)
	}
}

func TestActiveTransportReadMaterialDoesNotGenerateOrBorrowCustody(t *testing.T) {
	store, parent, _, _, _ := activeTransportFixture(t)
	_, _, _, _, _, err := activeTransportReadMaterial(store, parent, "deployment/repairs/absent")
	if err == nil {
		t.Fatal("missing original renewal was regenerated")
	}
	for _, rel := range []string{"../another-generation", "/other/environment", "deployment/repairs/../foreign"} {
		if _, _, _, _, _, err := activeTransportReadMaterial(store, parent, rel); err == nil {
			t.Fatal("non-canonical candidate admitted")
		}
	}
	if _, _, err := readActiveTransportRenewal(store, parent, nil, nil); err != nil {
		t.Fatal("missing active pointer should preserve existing original lifecycle", err)
	}
	if err := store.write(filepath.Join("deployment/create/material", parent, "active-transport-renewal.json"), []byte(`{"schema":"rtk-cloud.active-temporary-transport-renewal/v2","completed":true}`), false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readActiveTransportRenewal(store, parent, nil, nil); err == nil {
		t.Fatal("synthetic completed proof without exact original source accepted")
	}
}

func activeTransportCompletedProofFixture(t *testing.T) (secretStore, string, map[string]environmentCreateTransport, *initialTransportRenewal, environmentCreateOwner, environmentCreateJournal, activeTransportRenewalProof) {
	t.Helper()
	store, parent, management, managementKey, now := activeTransportFixture(t)
	original, err := createEnvironmentTransport(store, parent, "openbao", map[string][]string{"openbao": {"openbao.video-cloud-dev-secrets.svc"}}, now.Add(-5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	openbao, openbaoKey, err := renewInitialTransportMaterial(original, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	old := map[string]environmentCreateTransport{"management": management, "openbao": openbao}
	keys := map[string][]byte{"management": managementKey, "openbao": openbaoKey}
	legacy := &initialTransportRenewal{Schema: "rtk-cloud.initial-transport-renewal/v1", Parent: parent, Review: strings.Repeat("b", 64), Completed: true, Material: map[string]string{}, Owners: map[string]string{}}
	material := map[string]string{}
	authority := map[string]string{}
	current := map[string]environmentCreateTransport{}
	write := func(path string, body []byte) {
		t.Helper()
		if err := store.write(path, body, false); err != nil {
			t.Fatal(err)
		}
		material[path] = environmentPreflightBytesSHA256(body)
	}
	for _, role := range []string{"management", "openbao"} {
		rel := filepath.Join("deployment/create/transport-renewal", legacy.Review, role+"-transport/ca.key")
		write(rel, keys[role])
		authority[role] = rel
		renewed, err := renewActiveTransportSameKey(old[role], keys[role], now)
		if err != nil {
			t.Fatal(err)
		}
		current[role] = renewed
	}
	owner := environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: "isolated-original-owner", Journal: filepath.Join("deployment/create/executions", parent, "execution-isolated/journal.json")}
	source := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: owner.OwnerToken}
	for _, key := range []string{"device-root-bootstrap", "kubernetes/device-root-active", "kubernetes/device-root-trust", "kubernetes/app-root-trust", "kubernetes/app-intermediate-trust", "pki-reserve-app-root", "pki-reserve-app-intermediate", "kubernetes/transport-client-enrollment", "kubernetes/app-root-trust-broker-forward-" + strings.Repeat("c", 64), "openbao-role-certissuer-dev-accepted"} {
		source.Entries = append(source.Entries, environmentCreateJournalEntry{Key: key, State: "completed", IntentSHA256: strings.Repeat("d", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"original": "complete"}), StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Hour)})
	}
	if !isManagedTransportCheckpoint(source) {
		t.Fatal("fixture is not the exact completed CA checkpoint")
	}
	plan := activeTransportRenewalPlan{Schema: activeTransportRenewalSchema, Parent: parent, OwnerSHA: deploymentplan.Digest(owner), JournalSHA: deploymentplan.Digest(source), OriginalProofSHA: deploymentplan.Digest(legacy), Candidate: "deployment/repairs/isolated-same-key", Material: material, AuthorityKeys: authority, PodUID: "original-pod", SetUID: "original-set"}
	for n := 0; n < 13; n++ {
		name := fmt.Sprintf("isolated-%d", n)
		before := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "data": deploymentplan.Object{"tls.crt": "original", "tls.key": "preserved"}}
		desired := deploymentplan.Clone(before)
		deploymentplan.Map(desired["data"])["tls.crt"] = "renewed"
		plan.Resources = append(plan.Resources, activeTransportRenewalResource{ID: deploymentplan.ID(before), UID: "original-owner-" + name, Before: before, Desired: desired})
	}
	review := deploymentplan.Digest(plan)
	base := filepath.Join("deployment/create/transport-renewal", review)
	q := deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range activeTransportQualificationGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	artifact := activeTransportRenewalArtifact{Schema: activeTransportRenewalSchema + "/artifact", Plan: plan, Qualification: q, QualifiedAt: now.Add(-time.Minute), ValidUntil: now.Add(14 * time.Minute)}
	for path, value := range map[string]any{filepath.Join(base, "source-journal.json"): source, filepath.Join(base, "qualification.json"): artifact, "deployment/.execution-active/owner.json": owner} {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	proof := activeTransportRenewalProof{Schema: activeTransportRenewalSchema, Parent: parent, Review: review, OriginalProofSHA: deploymentplan.Digest(legacy), AuthorityKeys: authority, Material: map[string]string{}, Owners: map[string]string{}, PodUID: plan.PodUID, SetUID: plan.SetUID, Completed: true}
	for path, sha := range material {
		proof.Material[path] = sha
	}
	for _, role := range []string{"management", "openbao"} {
		for name, body := range map[string][]byte{"identities.json": mustProviderRecoveryJSON(current[role]), "ca.crt": []byte(current[role].CACert)} {
			path := filepath.Join(base, role+"-transport", name)
			if err := store.write(path, body, false); err != nil {
				t.Fatal(err)
			}
			proof.Material[path] = environmentPreflightBytesSHA256(body)
		}
	}
	for _, r := range plan.Resources {
		proof.Owners[r.ID] = r.UID
	}
	journal := source
	journal.Entries = append(append([]environmentCreateJournalEntry(nil), source.Entries...), environmentCreateJournalEntry{Key: "active-transport-renewal-" + review, Phase: "recovery", State: "completed", IntentSHA256: activeTransportJournalDigest(plan), Receipt: activeTransportCanonicalReceipt(t, proof), StartedAt: now.Add(-30 * time.Second), CompletedAt: now})
	for _, r := range plan.Resources {
		intent := map[string]string{"resource": r.ID, "uid": r.UID, "before_sha256": deploymentplan.Digest(r.Before), "desired_sha256": deploymentplan.Digest(r.Desired)}
		journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: "active-transport-renewal-" + review + "-" + strings.ReplaceAll(r.ID, "/", "-"), Phase: "recovery", State: "completed", IntentSHA256: deploymentplan.Digest(intent), Receipt: mustProviderRecoveryJSON(intent), StartedAt: now.Add(-20 * time.Second), CompletedAt: now.Add(-time.Second)})
	}
	for path, value := range map[string]any{owner.Journal: journal, filepath.Join(base, "completed.json"): proof, filepath.Join("deployment/create/material", parent, "active-transport-renewal.json"): proof} {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	return store, parent, old, legacy, owner, journal, proof
}

func TestActiveTransportCompletedProofAuthenticatesAncestryAndEveryWrite(t *testing.T) {
	store, parent, old, legacy, owner, journal, proof := activeTransportCompletedProofFixture(t)
	renewed, effective, err := readActiveTransportRenewal(store, parent, old, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Schema != activeTransportRenewalSchema || effective.Review != proof.Review || renewed["openbao"].Identities["openbao"].Key != old["openbao"].Identities["openbao"].Key {
		t.Fatal("effective same-key proof lost original serving identity")
	}
	// The immutable completed proof remains usable as original deployment appends
	// independently qualified stages, without admitting a half-written repair.
	journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: "later-qualified-stage", State: "completed", Receipt: mustProviderRecoveryJSON(map[string]string{"later": "native"}), CompletedAt: time.Now().UTC()})
	if err := store.write(owner.Journal, mustProviderRecoveryJSON(journal), true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readActiveTransportRenewal(store, parent, old, legacy); err != nil {
		t.Fatal("completed repair was invalidated by later original deployment work", err)
	}
}

func TestActiveTransportCompletedProofRejectsHalfWritesAndUnboundMaterial(t *testing.T) {
	mutations := map[string]func(*testing.T, secretStore, environmentCreateOwner, environmentCreateJournal, activeTransportRenewalProof){
		"half-written-resource": func(t *testing.T, s secretStore, o environmentCreateOwner, j environmentCreateJournal, p activeTransportRenewalProof) {
			j.Entries[len(j.Entries)-1].State = "pending"
			j.Entries[len(j.Entries)-1].Receipt = nil
			j.Entries[len(j.Entries)-1].CompletedAt = time.Time{}
			_ = s.write(o.Journal, mustProviderRecoveryJSON(j), true)
		},
		"missing-resource-intent": func(t *testing.T, s secretStore, o environmentCreateOwner, j environmentCreateJournal, p activeTransportRenewalProof) {
			j.Entries = j.Entries[:len(j.Entries)-1]
			_ = s.write(o.Journal, mustProviderRecoveryJSON(j), true)
		},
		"different-native-resource-receipt": func(t *testing.T, s secretStore, o environmentCreateOwner, j environmentCreateJournal, p activeTransportRenewalProof) {
			j.Entries[len(j.Entries)-1].Receipt = mustProviderRecoveryJSON(map[string]string{"uid": "other"})
			_ = s.write(o.Journal, mustProviderRecoveryJSON(j), true)
		},
		"different-original-owner": func(t *testing.T, s secretStore, o environmentCreateOwner, j environmentCreateJournal, p activeTransportRenewalProof) {
			o.OwnerToken = "changed-generation"
			_ = s.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(o), true)
		},
		"changed-authority-custody": func(t *testing.T, s secretStore, o environmentCreateOwner, j environmentCreateJournal, p activeTransportRenewalProof) {
			_ = s.write(p.AuthorityKeys["openbao"], []byte("other-authority-key"), true)
		},
		"changed-qualified-public-certificate": func(t *testing.T, s secretStore, o environmentCreateOwner, j environmentCreateJournal, p activeTransportRenewalProof) {
			_ = s.write(filepath.Join("deployment/create/transport-renewal", p.Review, "openbao-transport/ca.crt"), []byte("other-certificate"), true)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store, parent, old, legacy, owner, journal, proof := activeTransportCompletedProofFixture(t)
			mutate(t, store, owner, journal, proof)
			if _, _, err := readActiveTransportRenewal(store, parent, old, legacy); err == nil {
				t.Fatal("unqualified or half-written active TLS proof accepted")
			}
		})
	}
}

func activeTransportCanonicalReceipt(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := canonicalEnvironmentCreateJSON(mustProviderRecoveryJSON(value))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestActiveTransportParentIntentAndReceiptUseJournalCanonicalization(t *testing.T) {
	plan := activeTransportRenewalPlan{Schema: activeTransportRenewalSchema, Parent: strings.Repeat("a", 64), Material: map[string]string{"original": "preserved"}}
	proof := activeTransportRenewalProof{Schema: activeTransportRenewalSchema, Parent: plan.Parent, Review: deploymentplan.Digest(plan), Completed: true}
	rawPlan := activeTransportCanonicalReceipt(t, plan)
	rawProof := activeTransportCanonicalReceipt(t, proof)
	if activeTransportJournalDigest(plan) != environmentPreflightBytesSHA256(rawPlan) || activeTransportJournalDigest(proof) != environmentPreflightBytesSHA256(rawProof) {
		t.Fatal("repair parent intent or receipt does not match durable journal canonicalization")
	}
	if activeTransportJournalDigest(plan) == deploymentplan.Digest(plan) || activeTransportJournalDigest(proof) == deploymentplan.Digest(proof) {
		t.Fatal("fixture did not distinguish struct artifact hashes from journal canonical hashes")
	}
}

func TestActiveTransportRestartNativeWriteRetainsDefaultsAndUnrelatedTemplateMetadata(t *testing.T) {
	before := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "certissuer", "namespace": "dev"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"original": "retained"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "certissuer", "image": "original"}}}}}}
	desired := activeTransportConsumerRestart(before, "same-key-ca")
	actual := deploymentplan.Clone(before)
	deploymentplan.Map(actual["metadata"])["uid"] = "original-owner"
	spec := deploymentplan.Map(actual["spec"])
	spec["progressDeadlineSeconds"] = float64(600)
	pod := deploymentplan.Map(spec["template"])
	deploymentplan.Map(pod["metadata"])["annotations"] = deploymentplan.Object{"unrelated.example/injected": "retain"}
	deploymentplan.Map(pod["spec"])["dnsPolicy"] = "ClusterFirst"
	body, err := activeTransportResourceBody(activeTransportRenewalResource{ID: "Deployment/dev/certissuer", UID: "original-owner", Before: before, Desired: desired}, actual)
	if err != nil {
		t.Fatal(err)
	}
	annotations := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(body["spec"])["template"])["metadata"])["annotations"])
	delete(annotations, "rtk.realtek.com/temporary-openbao-ca-sha256")
	if deploymentplan.Digest(body) != deploymentplan.Digest(actual) {
		t.Fatal("narrow restart dropped server defaults, unrelated annotations or live declared state")
	}
	deploymentplan.Map(desired["spec"])["replicas"] = float64(2)
	if _, err := activeTransportResourceBody(activeTransportRenewalResource{UID: "original-owner", Before: before, Desired: desired}, actual); err == nil {
		t.Fatal("unqualified workload change disguised as restart")
	}
}

func TestActiveTransportPrivateFileRejectsNonPrivateAndSymlinkedParents(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	rel := "deployment/repairs/isolated-candidate/openbao-transport/ca.crt"
	if err := store.write(rel, []byte("private-public-certificate"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := activeTransportPrivateFile(store, rel); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(store.Root, filepath.Dir(rel))
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := activeTransportPrivateFile(store, rel); err == nil {
		t.Fatal("nonprivate parent accepted")
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "ca.crt"), []byte("foreign-certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := activeTransportPrivateFile(store, rel); err == nil {
		t.Fatal("foreign symlinked candidate directory accepted")
	}
}

func TestActiveTransportResumeRejectsUnrelatedOrUnboundRepairSuffix(t *testing.T) {
	tests := map[string]func(environmentCreateJournal, activeTransportRenewalProof) environmentCreateJournal{
		"unrelated-completed-stage": func(j environmentCreateJournal, p activeTransportRenewalProof) environmentCreateJournal {
			j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "unrelated-completed", Phase: "recovery", State: "completed", IntentSHA256: strings.Repeat("a", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"wrong": "stage"}), StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()})
			return j
		},
		"prefix-only-stage": func(j environmentCreateJournal, p activeTransportRenewalProof) environmentCreateJournal {
			j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "active-transport-renewal-" + p.Review + "-unqualified", Phase: "recovery", State: "pending", IntentSHA256: strings.Repeat("a", 64), StartedAt: time.Now().UTC()})
			return j
		},
		"different-resource-intent": func(j environmentCreateJournal, p activeTransportRenewalProof) environmentCreateJournal {
			j.Entries[len(j.Entries)-1].IntentSHA256 = strings.Repeat("a", 64)
			return j
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store, _, _, _, owner, journal, proof := activeTransportCompletedProofFixture(t)
			e := &environmentCreateExecution{Store: store, owner: owner, Journal: journal}
			if !activeTransportNativeSourceCheckpoint(store, e) {
				t.Fatal("exact original admitted resource suffix not recognized")
			}
			e.Journal = mutate(journal, proof)
			if activeTransportNativeSourceCheckpoint(store, e) {
				t.Fatal("unrelated or unbound suffix admitted by prefix")
			}
		})
	}
}

func TestActiveTransportCandidatePathStoredRelativeExecutionAndAbsoluteQualification(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	canonical := "deployment/repairs/active-temporary-tls-same-key-isolated"
	artifact := activeTransportRenewalArtifact{Plan: activeTransportRenewalPlan{Candidate: canonical}}
	cases := map[string]string{"stored-relative-execution": artifact.Plan.Candidate, "absolute-qualification": filepath.Join(store.Root, canonical)}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			relative, err := activeTransportCandidateRelative(store, input)
			if err != nil || relative != canonical {
				t.Fatalf("canonical candidate could not be consumed: got=%q err=%v", relative, err)
			}
		})
	}
	for _, input := range []string{"../deployment/repairs/foreign", "deployment/repairs/../foreign", filepath.Join(t.TempDir(), canonical), "deployment/repairs"} {
		if _, err := activeTransportCandidateRelative(store, input); err == nil {
			t.Fatal("noncanonical or foreign candidate accepted")
		}
	}
}
