package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestMQTTRootCRLDomainSeamKeepsServiceStrictAndPreservesRealHistory(t *testing.T) {
	root, key, snapshot, now := rootCRLFixture(t)
	root.Domain = "mqtt"
	if _, err := buildCreateRootCRLRequest(root, snapshot, now); err == nil {
		t.Fatal("Service wrapper accepted a different domain")
	}
	request, err := buildCreateRootCRLRequestForDomain(root, snapshot, "mqtt", now)
	if err != nil {
		t.Fatal(err)
	}
	if request.Number != "2" || len(request.Entries) != 2 || request.Entries[0].Serial != "63" || request.Entries[0].Reason != 1 || request.Entries[1].Serial != "64" {
		t.Fatal("lost genuine prior/native revocation inventory")
	}
	signed := signedRootCRLFixture(t, root, key, request)
	if _, err := validateCreateRootCRLSignedForDomain(root, snapshot, request, []byte(signed.PEM), "mqtt", now); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"service", "device", "app", "openbao_tls", ""} {
		if _, err := parseCreateRootCRLForDomain(root, signed, domain, true, now); err == nil {
			t.Fatal("incorrect domain admitted", domain)
		}
	}
	for _, mode := range []string{"omitted-prior", "changed-reason", "unreviewed-serial", "wrong-key-number", "future-time", "expired", "wrong-digest"} {
		t.Run(mode, func(t *testing.T) {
			bad := request
			bad.Entries = append([]createRootCRLRevocation(nil), request.Entries...)
			switch mode {
			case "omitted-prior":
				bad.Entries = bad.Entries[1:]
			case "changed-reason":
				bad.Entries[0].Reason = 5
			case "unreviewed-serial":
				bad.Entries = append(bad.Entries, createRootCRLRevocation{"65", now.Add(-time.Minute), 0})
			case "wrong-key-number":
				bad.Number = "3"
			case "future-time":
				bad.ThisUpdate = now.Add(time.Hour)
			case "expired":
				bad.ThisUpdate = now.Add(-2 * time.Hour)
				bad.NextUpdate = now.Add(-time.Hour)
			}
			altered := signedRootCRLFixture(t, root, key, bad)
			if mode == "wrong-digest" {
				altered.Digest = strings.Repeat("b", 64)
			}
			if _, err := validateCreateRootCRLSignedForDomain(root, snapshot, request, []byte(altered.PEM), "mqtt", now); err == nil && mode != "wrong-digest" {
				t.Fatal("unsafe signed request accepted", mode)
			}
			if mode == "wrong-digest" {
				if _, err := parseCreateRootCRLForDomain(root, altered, "mqtt", true, now); err == nil {
					t.Fatal("unbound record digest accepted")
				}
			}
		})
	}
}

func mqttRootCRLJournalFixture(t *testing.T) (createMQTTRootCRLArtifact, environmentCreateJournal, environmentCreateJournal) {
	t.Helper()
	now := time.Now().UTC()
	hash := strings.Repeat("a", 64)
	source := environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: hash, OwnerToken: "isolated-original-owner"}
	source.Entries = []environmentCreateJournalEntry{{Key: "isolated-existing-core", Phase: "bootstrap", IntentSHA256: hash, State: "completed", StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Minute), Receipt: json.RawMessage(`{"actual":"isolated-source"}`)}}
	p := createMQTTRootCRLPlan{Schema: createMQTTRootCRLSchema, Parent: hash, SourceJournalSHA: deploymentplan.Digest(source), Configuration: deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}, Candidate: "deployment/repairs/mqtt-root-crl/isolated", SnapshotSHA: hash, Signed: createRootCRLRecord{Digest: strings.Repeat("b", 64), Number: "2"}}
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		p.Consumers = append(p.Consumers, createRootCRLStateTarget{ID: "Deployment/video-cloud-dev-video-cloud/" + name, Container: "app", Desired: deploymentplan.Object{"metadata": deploymentplan.Object{"name": name}}, Paths: []string{"/var/lib/mqtt-trust/root-crl.json"}})
	}
	a := createMQTTRootCRLArtifact{Schema: createMQTTRootCRLSchema + "/artifact", Plan: p, QualifiedAt: now.Add(-time.Minute), ValidUntil: now.Add(14 * time.Minute)}
	review := deploymentplan.Digest(p)
	key := "mqtt-root-crl-renewal-" + review
	evidence := []map[string]string{}
	for _, target := range p.Consumers {
		evidence = append(evidence, map[string]string{"native-publication": hash, target.ID + ":state": hash, target.ID + ":owner": deploymentplan.Digest(target), target.ID + ":native-ack": hash})
	}
	combined := map[string]string{}
	for _, item := range evidence {
		for k, v := range item {
			combined[k] = v
		}
	}
	receipt := createMQTTRootCRLReceipt{createMQTTRootCRLSchema, p.Parent, review, p.Candidate + "/signed/revocations.pem", p.Signed.Digest, deploymentplan.Digest(combined), true}
	keys := []string{key, key + "/native-import", key + "/native-runtime-video-cloud-api", key + "/native-runtime-video-cloud-logingester"}
	intents := []any{p, mqttRootCRLImportIntent(p), mqttRootCRLRuntimeIntent(p, p.Consumers[0]), mqttRootCRLRuntimeIntent(p, p.Consumers[1])}
	receipts := []any{receipt, map[string]any{"native_crl": p.Signed, "request_sha256": deploymentplan.Digest(p.Request)}, evidence[0], evidence[1]}
	current := source
	current.Entries = append([]environmentCreateJournalEntry(nil), source.Entries...)
	current.KubernetesLeases = []deploymentplan.LeaseOwner{{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: rootCRLLeasePlan(p.Configuration, review).SHA256() + ":isolated-owner"}}
	for i, k := range keys {
		canonical, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(intents[i]))
		if err != nil {
			t.Fatal(err)
		}
		current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: k, Phase: "bootstrap", IntentSHA256: canonical, State: "completed", StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: mustProviderRecoveryJSON(receipts[i])})
	}
	return a, source, current
}

func TestMQTTRootCRLJournalPinsActualCanonicalAdmissionAndExactFourSteps(t *testing.T) {
	a, source, current := mqttRootCRLJournalFixture(t)
	if !mqttRootCRLProgressValid(source, a, source, false) || !mqttRootCRLProgressValid(current, a, source, true) || !mqttRootCRLImportAdmitted(current, a, source) {
		t.Fatal("actual canonical native steps rejected")
	}
	for _, mode := range []string{"extra-tail", "foreign-pending", "changed-source", "struct-hash", "changed-import", "missing-native-ack", "wrong-owner", "duplicate-consumer", "expired-admission", "foreign-lease", "missing-lease", "out-of-order"} {
		t.Run(mode, func(t *testing.T) {
			bad := current
			bad.Entries = append([]environmentCreateJournalEntry(nil), current.Entries...)
			switch mode {
			case "extra-tail":
				bad.Entries = append(bad.Entries, bad.Entries[len(bad.Entries)-1])
			case "foreign-pending":
				bad.Entries[1].Key = "mqtt-root-crl-other"
			case "changed-source":
				bad.Entries[0].Receipt = json.RawMessage(`{"fabricated":true}`)
			case "struct-hash":
				bad.Entries[1].IntentSHA256 = deploymentplan.Digest(a.Plan)
			case "changed-import":
				bad.Entries[2].Receipt = json.RawMessage(`{"fabricated":true}`)
			case "missing-native-ack":
				bad.Entries[3].Receipt = json.RawMessage(`{"native-publication":"missing"}`)
			case "wrong-owner":
				var v map[string]string
				json.Unmarshal(bad.Entries[3].Receipt, &v)
				v[a.Plan.Consumers[0].ID+":owner"] = strings.Repeat("b", 64)
				bad.Entries[3].Receipt = mustProviderRecoveryJSON(v)
			case "duplicate-consumer":
				bad.Entries[4].Key = bad.Entries[3].Key
			case "expired-admission":
				bad.Entries[1].StartedAt = a.ValidUntil
			case "foreign-lease":
				bad.KubernetesLeases = append(bad.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: "foreign"})
			case "missing-lease":
				bad.KubernetesLeases = nil
			case "out-of-order":
				bad.Entries[2].State = "pending"
				bad.Entries[2].Receipt = nil
				bad.Entries[2].CompletedAt = time.Time{}
			}
			if mqttRootCRLProgressValid(bad, a, source, true) {
				t.Fatal("unqualified tail accepted", mode)
			}
		})
	}
	// An unchanged pending import after genuine top admission remains resumable;
	// failed/uncertain native mutation is never represented as a fake completion.
	pending := current
	pending.Entries = append([]environmentCreateJournalEntry(nil), current.Entries[:3]...)
	pending.Entries[1].State = "pending"
	pending.Entries[1].Receipt = nil
	pending.Entries[1].CompletedAt = time.Time{}
	pending.Entries[2].State = "pending"
	pending.Entries[2].Receipt = nil
	pending.Entries[2].CompletedAt = time.Time{}
	if !mqttRootCRLImportAdmitted(pending, a, source) || mqttRootCRLProgressValid(pending, a, source, true) {
		t.Fatal("uncertain native import lost its exact pending fence")
	}
}

func TestMQTTRootCRLNativeSnapshotRetryAcceptsOnlyExactAdmittedTwo(t *testing.T) {
	root, key, snapshot, now := rootCRLFixture(t)
	root.Domain = "mqtt"
	request, err := buildCreateRootCRLRequestForDomain(root, snapshot, "mqtt", now)
	if err != nil {
		t.Fatal(err)
	}
	signed := signedRootCRLFixture(t, root, key, request)
	a := createMQTTRootCRLPlan{Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Signed: signed}
	current := snapshot
	current.Prior = signed
	if mqttRootCRLSnapshotMatches(a, current, false) || !mqttRootCRLSnapshotMatches(a, current, true) {
		t.Fatal("native publication accepted without exact prior admitted import")
	}
	current.Bindings = append(current.Bindings, createRootCRLRevocation{"65", now.Add(-time.Hour), 0})
	if mqttRootCRLSnapshotMatches(a, current, true) {
		t.Fatal("changed genuine inventory accepted")
	}
}

func TestMQTTRootCRLPointerAbsentOnlyAndHalfWrittenAlwaysFenced(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", base)
	store := secretStore{ConfigRoot: base, Root: filepath.Join(base, "dev"), Environment: "dev"}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	parent := strings.Repeat("a", 64)
	if a, c, err := readCreateMQTTRootCRLRecovery(store, parent); err != nil || a != nil || c != nil {
		t.Fatal("absent pointer not nil", err)
	}
	path := filepath.Join(store.Root, "deployment/create/material", parent, "mqtt-root-crl-renewal.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", `{}`, `{"Completed":true}`, `{"Schema":"rtk-cloud.create-mqtt-root-crl-recovery/v1/completion","Parent":"` + parent + `","Review":"` + strings.Repeat("b", 64) + `","Completed":true}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readCreateMQTTRootCRLRecovery(store, parent); err == nil {
			t.Fatal("half-written completion accepted")
		}
	}
	if _, _, err := readCreateMQTTRootCRLRecovery(store, "../../other-env"); err == nil {
		t.Fatal("noncanonical parent selector accepted")
	}
}

func TestMQTTRootCRLACKRosterRequiresBothRealConsumersAfterNativePublication(t *testing.T) {
	published := time.Now().UTC().Add(-time.Minute)
	now := published.Add(time.Minute)
	issuer := "11111111-1111-4111-8111-111111111111"
	digest := strings.Repeat("a", 64)
	rows := []mqttRootCRLACK{{issuer, digest, "video-cloud-api", published.Add(time.Second)}, {issuer, digest, "video-cloud-logingester", published.Add(2 * time.Second)}}
	if !mqttRootCRLACKsValid(rows, issuer, digest, published, now) {
		t.Fatal("genuine exact native roster rejected")
	}
	for _, mode := range []string{"missing", "extra", "duplicate", "foreign-issuer", "wrong-digest", "before-import", "future", "zero-import-time"} {
		t.Run(mode, func(t *testing.T) {
			bad := append([]mqttRootCRLACK(nil), rows...)
			at := published
			switch mode {
			case "missing":
				bad = bad[:1]
			case "extra":
				bad = append(bad, mqttRootCRLACK{issuer, digest, "foreign", published.Add(time.Second)})
			case "duplicate":
				bad[1].Consumer = bad[0].Consumer
			case "foreign-issuer":
				bad[0].IssuerID = "22222222-2222-4222-8222-222222222222"
			case "wrong-digest":
				bad[0].Digest = strings.Repeat("b", 64)
			case "before-import":
				bad[0].At = published.Add(-time.Nanosecond)
			case "future":
				bad[0].At = now.Add(time.Nanosecond)
			case "zero-import-time":
				at = time.Time{}
			}
			if mqttRootCRLACKsValid(bad, issuer, digest, at, now) {
				t.Fatal("unqualified native evidence accepted", mode)
			}
		})
	}
}

func TestMQTTRootCRLImmutableFilesRejectParentSymlinkAndPermissionRepair(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", base)
	store := secretStore{ConfigRoot: base, Root: filepath.Join(base, "dev"), Environment: "dev"}
	if err := os.Mkdir(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	relative := "deployment/repairs/mqtt-root-crl/isolated/source.json"
	value := map[string]string{"original": "isolated"}
	if err := writeMQTTRootCRLImmutable(store, relative, value); err != nil {
		t.Fatal("new isolated canonical file rejected", err)
	}
	if err := writeMQTTRootCRLImmutable(store, relative, value); err != nil {
		t.Fatal("exact immutable retry rejected", err)
	}
	if err := writeMQTTRootCRLImmutable(store, relative, map[string]string{"different": "body"}); err == nil {
		t.Fatal("immutable artifact replaced")
	}
	parent := filepath.Join(store.Root, "deployment/repairs/mqtt-root-crl/isolated")
	if err := os.Chmod(parent, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeMQTTRootCRLImmutable(store, relative, value); err == nil {
		t.Fatal("nonprivate parent accepted")
	}
	info, err := os.Stat(parent)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("qualification silently repaired an existing permission mismatch")
	}
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), parent); err != nil {
		t.Fatal(err)
	}
	if err := writeMQTTRootCRLImmutable(store, relative, value); err == nil {
		t.Fatal("parent symlink accepted")
	}
}

func mqttRootCRLPairedPlanFixture(t *testing.T) createMQTTRootCRLPlan {
	t.Helper()
	root, key, snapshot, now := rootCRLFixture(t)
	root.Domain = "mqtt"
	root.ID = "14c1a269-e32e-4eac-aa93-db5ad8e85f7e"
	snapshot.Prior.IssuerID = root.ID
	request, err := buildCreateRootCRLRequestForDomain(root, snapshot, "mqtt", now)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	p := createMQTTRootCRLPlan{Schema: createMQTTRootCRLSchema, Parent: hash, SourceJournalSHA: hash, SourceCoreSHA: hash, Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signedRootCRLFixture(t, root, key, request), Material: map[string]string{"pki/custody/isolated-original": hash}, NativeSourceEvidence: map[string]string{}}
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		p.Consumers = append(p.Consumers, createRootCRLStateTarget{ID: "Deployment/video-cloud-dev-video-cloud/" + name, UID: "11111111-1111-4111-8111-111111111111", Container: "app", Desired: deploymentplan.Object{"metadata": deploymentplan.Object{"name": name}}, Paths: []string{"/var/lib/mqtt-trust/root-crl.json"}})
	}
	original := managedLateCRLRecord{IssuerID: root.ID, Digest: snapshot.Prior.Digest, Number: snapshot.Prior.Number, PEM: snapshot.Prior.PEM, ThisUpdate: snapshot.Prior.ThisUpdate, NextUpdate: snapshot.Prior.NextUpdate}
	mqtt := managedLateRootPriorEvidence{RootID: root.ID, RootSHA: root.RootFingerprint, OriginalRecord: original, SnapshotSHA: hash, RevocationsSHA: deploymentplan.Digest(request.Entries), CustodySHA: deploymentplan.Digest(p.Material), Consumers: p.Consumers, ACKs: []managedLatePriorACK{{root.ID, original.Digest, "video-cloud-api", original.ThisUpdate.Add(time.Minute)}, {root.ID, original.Digest, "video-cloud-logingester", original.ThisUpdate.Add(2 * time.Minute)}}}
	p.PairedPrior = &managedLatePairedCRLPriorEvidence{Schema: managedLatePairedCRLPriorSchema, SourceSHA: p.SourceJournalSHA, CoreSourceSHA: hash, AcceptedCoreSHA: p.SourceCoreSHA, Roots: map[string]managedLateRootPriorEvidence{root.ID: mqtt, "734487b8-abcf-40be-a813-8b52c64f7954": {RootID: "734487b8-abcf-40be-a813-8b52c64f7954", OriginalRecord: managedLateCRLRecord{Number: "1"}}}}
	p.NativeSourceEvidence[managedLatePairedCRLPriorMaterial] = deploymentplan.Digest(p.PairedPrior)
	return p
}

func TestMQTTRootCRLPairedPriorCrossPinsSigningSnapshotCustodyAndConsumers(t *testing.T) {
	p := mqttRootCRLPairedPlanFixture(t)
	if !mqttRootCRLPairedEvidenceBound(p) {
		t.Fatal("paired observation was not bound to its actual signing snapshot")
	}
	for _, mode := range []string{"missing", "unbound-evidence", "foreign-source", "foreign-Core", "missing-TLS", "changed-root", "changed-prior", "changed-custody", "changed-revocations", "changed-cache-owner"} {
		t.Run(mode, func(t *testing.T) {
			var bad createMQTTRootCRLPlan
			if json.Unmarshal(mustProviderRecoveryJSON(p), &bad) != nil {
				t.Fatal("fixture")
			}
			mqtt := bad.PairedPrior.Roots[bad.Root.ID]
			switch mode {
			case "missing":
				bad.PairedPrior = nil
			case "unbound-evidence":
				delete(bad.NativeSourceEvidence, managedLatePairedCRLPriorMaterial)
			case "foreign-source":
				bad.PairedPrior.SourceSHA = strings.Repeat("b", 64)
			case "foreign-Core":
				bad.PairedPrior.AcceptedCoreSHA = strings.Repeat("b", 64)
			case "missing-TLS":
				delete(bad.PairedPrior.Roots, "734487b8-abcf-40be-a813-8b52c64f7954")
			case "changed-root":
				mqtt.RootSHA = strings.Repeat("b", 64)
			case "changed-prior":
				mqtt.OriginalRecord.Number = "2"
			case "changed-custody":
				mqtt.CustodySHA = strings.Repeat("b", 64)
			case "changed-revocations":
				mqtt.RevocationsSHA = strings.Repeat("b", 64)
			case "changed-cache-owner":
				mqtt.Consumers[0].UID = "22222222-2222-4222-8222-222222222222"
			}
			if bad.PairedPrior != nil {
				bad.PairedPrior.Roots[bad.Root.ID] = mqtt
				if mode != "unbound-evidence" {
					bad.NativeSourceEvidence[managedLatePairedCRLPriorMaterial] = deploymentplan.Digest(bad.PairedPrior)
				}
			}
			if mqttRootCRLPairedEvidenceBound(bad) {
				t.Fatal("cross-bound prior changed", mode)
			}
		})
	}
}

func TestMQTTRootCRLPairedQualificationReusesExactRequestAndSignedCandidate(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", base)
	store := secretStore{ConfigRoot: base, Root: filepath.Join(base, "dev"), Environment: "dev"}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	p := mqttRootCRLPairedPlanFixture(t)
	e := &environmentCreateExecution{Store: store, owner: environmentCreateOwner{OwnerToken: "isolated-original-owner"}, Journal: environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: p.Parent, OwnerToken: "isolated-original-owner"}}
	p.SourceJournalSHA = deploymentplan.Digest(e.Journal)
	p.PairedPrior.SourceSHA = p.SourceJournalSHA
	p.NativeSourceEvidence[managedLatePairedCRLPriorMaterial] = deploymentplan.Digest(p.PairedPrior)
	selector := mqttRootCRLCandidateSelector(p.Parent, p.SourceJournalSHA, p.Root, p.Snapshot, p.Material)
	p.Candidate = filepath.ToSlash(filepath.Join("deployment/repairs/mqtt-root-crl", selector))
	descriptor := mqttRootCRLCandidate{Schema: createMQTTRootCRLSchema + "/candidate", Parent: p.Parent, SourceOwnerSHA: deploymentplan.Digest(e.owner), SourceJournalSHA: p.SourceJournalSHA, SnapshotSHA: p.SnapshotSHA, Root: p.Root, Snapshot: p.Snapshot, Material: p.Material, Request: p.Request}
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(p.Candidate, "candidate.json"), descriptor); err != nil {
		t.Fatal(err)
	}
	if err := writeMQTTRootCRLImmutable(store, filepath.Join(p.Candidate, "request.json"), p.Request); err != nil {
		t.Fatal(err)
	}
	signedPath := filepath.Join(store.Root, p.Candidate, "signed/revocations.pem")
	if err := prepareMQTTRootCRLPrivateParent(store, filepath.Dir(signedPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signedPath, []byte(p.Signed.PEM), 0600); err != nil {
		t.Fatal(err)
	}
	requestBefore, err := os.ReadFile(filepath.Join(store.Root, p.Candidate, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	signedBefore, err := os.ReadFile(signedPath)
	if err != nil {
		t.Fatal(err)
	}
	originalReview := deploymentplan.Digest(p)
	for _, qualification := range []string{"original", "advanced-same-root-ACK"} {
		if qualification == "advanced-same-root-ACK" {
			mqtt := p.PairedPrior.Roots[p.Root.ID]
			mqtt.ACKs[0].At = mqtt.ACKs[0].At.Add(time.Minute)
			p.PairedPrior.Roots[p.Root.ID] = mqtt
			p.NativeSourceEvidence[managedLatePairedCRLPriorMaterial] = deploymentplan.Digest(p.PairedPrior)
			if deploymentplan.Digest(p) == originalReview {
				t.Fatal("outer qualification did not bind actual prior ACK evidence")
			}
		}
		candidate, request, signed, err := prepareMQTTRootCRLCandidate(context.Background(), store, e, "unused-no-native-execution", p.Root, p.Snapshot, p.Material)
		if err != nil || candidate != p.Candidate || deploymentplan.Digest(request) != deploymentplan.Digest(p.Request) || deploymentplan.Digest(signed) != deploymentplan.Digest(p.Signed) {
			t.Fatal("unused genuine candidate was regenerated or changed", qualification, err)
		}
		requestAfter, err := os.ReadFile(filepath.Join(store.Root, p.Candidate, "request.json"))
		if err != nil {
			t.Fatal(err)
		}
		signedAfter, err := os.ReadFile(signedPath)
		if err != nil || !bytes.Equal(requestBefore, requestAfter) || !bytes.Equal(signedBefore, signedAfter) {
			t.Fatal("immutable request/signature bytes changed", qualification, err)
		}
	}
	material := map[string]string{}
	for k, v := range p.Material {
		material[k] = v
	}
	material["pki/custody/isolated-original"] = strings.Repeat("c", 64)
	if mqttRootCRLCandidateSelector(p.Parent, p.SourceJournalSHA, p.Root, p.Snapshot, material) == selector {
		t.Fatal("signing custody lost its candidate identity pin")
	}
}

func mqttRecordedVolumeFixture(t *testing.T) (createMQTTRootCRLPlan, *createServiceSessionRecovery, *createManagedLateSessionRecovery) {
	t.Helper()
	p := mqttRootCRLPairedPlanFixture(t)
	p.Generation.Stack = "video-cloud-dev"
	p.Controller = deploymentplan.Resource{ID: "Deployment/video-cloud-dev-video-cloud/pki-controller", Action: "retain", UID: "11111111-1111-4111-8111-111111111111", Desired: deploymentplan.Object{"metadata": deploymentplan.Object{"name": "pki-controller"}}}
	p.ControllerPodName, p.ControllerPodUID = "original-controller-pod", p.Controller.UID
	p.ConfigMap = deploymentplan.Resource{ID: "ConfigMap/video-cloud-dev-video-cloud/pki-create-mqtt-consumer-trust", Action: "retain", UID: "22222222-2222-4222-8222-222222222222", Desired: deploymentplan.Object{"metadata": deploymentplan.Object{"name": "pki-create-mqtt-consumer-trust"}}}
	p.ConsumerVolumes, p.OriginalVolumes = map[string]environmentCreateVolumeOwner{}, map[string]environmentCreateVolumeOwner{}
	hashes := map[string]string{}
	for i := 0; i < 18; i++ {
		name := fmt.Sprintf("original-claim-%02d", i)
		if i == 0 {
			name = "video-cloud-api-service-state"
		}
		if i == 1 {
			name = "video-cloud-logingester-service-state"
		}
		owner := environmentCreateVolumeOwner{NativeID: fmt.Sprint(1000 + i), PVName: fmt.Sprintf("original-pv-%02d", i), PVUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+i), PVCName: name, PVCUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 200+i), Namespace: "video-cloud-dev-video-cloud"}
		id := "PersistentVolumeClaim/" + owner.Namespace + "/" + name
		p.OriginalVolumes[id], hashes[id] = owner, deploymentplan.Digest(owner)
		if i < 2 {
			p.ConsumerVolumes[id] = owner
			target := &p.Consumers[i]
			target.UID = fmt.Sprintf("00000000-0000-4000-8000-%012d", 300+i)
			target.ClaimID, target.ClaimUID = id, owner.PVCUID
			target.PodName, target.PodUID = fmt.Sprintf("original-pod-%02d", i), fmt.Sprintf("00000000-0000-4000-8000-%012d", 400+i)
			target.StateSHA = map[string]string{target.Paths[0]: strings.Repeat("a", 64)}
			target.Desired["spec"] = deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "mqtt-bundle-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": name}}}}}}
		}
	}
	p.NativeSourceEvidence["accepted-service-core-managed-complete-current-native-volumes"] = deploymentplan.Digest(hashes)
	apps := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Stack: p.Generation.Stack}, Resources: []deploymentplan.Resource{p.ConfigMap, {ID: p.Consumers[0].ID, UID: p.Consumers[0].UID, Action: "update", Desired: p.Consumers[0].Desired}, {ID: p.Consumers[1].ID, UID: p.Consumers[1].UID, Action: "update", Desired: p.Consumers[1].Desired}}}
	aj := deploymentplan.Journal{Owners: map[string]string{}}
	for _, res := range apps.Resources {
		aj.Owners[res.ID] = res.UID
	}
	core := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Stack: p.Generation.Stack}, Resources: []deploymentplan.Resource{p.Controller}}
	cj := deploymentplan.Journal{Owners: map[string]string{p.Controller.ID: p.Controller.UID}}
	return p, &createServiceSessionRecovery{ManagedCorePartial: &createManagedCorePartialRecovery{Apps: apps, AppsJournal: aj}}, &createManagedLateSessionRecovery{AcceptedCore: core, AcceptedCoreJournal: cj}
}

func TestMQTTRootCRLRecordedClaimsOutsideLaterAppsAndCoreRoundTrip(t *testing.T) {
	p, r, late := mqttRecordedVolumeFixture(t)
	for _, stage := range []*deploymentplan.Plan{r.ManagedCorePartial.Apps, late.AcceptedCore} {
		for _, res := range stage.Resources {
			for _, target := range p.Consumers {
				if res.ID == target.ClaimID {
					t.Fatal("fixture incorrectly placed historical claim into a later stage")
				}
			}
		}
	}
	var round createMQTTRootCRLPlan
	if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(p), &round); err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(round) != deploymentplan.Digest(p) || !mqttRootCRLAcceptedTargetsValid(round, r, late) {
		t.Fatal("recorded original claims rejected after artifact round-trip")
	}
	for _, mode := range []string{"missing-volume", "wrong-PVCUID", "wrong-PVUID", "wrong-PVName", "wrong-native-volume", "foreign-claim", "changed-original-aggregate", "extra-original-volume", "foreign-declared-claim"} {
		t.Run(mode, func(t *testing.T) {
			var bad createMQTTRootCRLPlan
			if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(p), &bad); err != nil {
				t.Fatal(err)
			}
			id := bad.Consumers[0].ClaimID
			owner := bad.ConsumerVolumes[id]
			switch mode {
			case "missing-volume":
				delete(bad.ConsumerVolumes, id)
			case "wrong-PVCUID":
				owner.PVCUID = "11111111-1111-4111-8111-111111111111"
			case "wrong-PVUID":
				owner.PVUID = "11111111-1111-4111-8111-111111111111"
			case "wrong-PVName":
				owner.PVName = "foreign-pv"
			case "wrong-native-volume":
				owner.NativeID = "99999"
			case "foreign-claim":
				bad.Consumers[0].ClaimID = "PersistentVolumeClaim/video-cloud-dev-video-cloud/foreign"
			case "changed-original-aggregate":
				bad.NativeSourceEvidence["accepted-service-core-managed-complete-current-native-volumes"] = strings.Repeat("b", 64)
			case "extra-original-volume":
				bad.OriginalVolumes["foreign"] = owner
			case "foreign-declared-claim":
				cloned := *r.ManagedCorePartial.Apps
				cloned.Resources = append(append([]deploymentplan.Resource(nil), cloned.Resources...), deploymentplan.Resource{ID: id, UID: "11111111-1111-4111-8111-111111111111"})
				rr, cc := *r, *r.ManagedCorePartial
				cc.Apps = &cloned
				cc.AppsJournal = deploymentplan.Journal{Owners: map[string]string{}}
				for k, v := range r.ManagedCorePartial.AppsJournal.Owners {
					cc.AppsJournal.Owners[k] = v
				}
				cc.AppsJournal.Owners[id] = "11111111-1111-4111-8111-111111111111"
				rr.ManagedCorePartial = &cc
				if mqttRootCRLAcceptedTargetsValid(bad, &rr, late) {
					t.Fatal("foreign later claim overrode original custody")
				}
				return
			}
			if mode != "missing-volume" {
				bad.ConsumerVolumes[id] = owner
			}
			if mqttRootCRLAcceptedTargetsValid(bad, r, late) {
				t.Fatal("changed original owner accepted", mode)
			}
		})
	}
}

func TestMQTTRootCRLRecordedVolumeRequiresActualPVAndCSIClaimBinding(t *testing.T) {
	p, _, _ := mqttRecordedVolumeFixture(t)
	owner := p.ConsumerVolumes[p.Consumers[0].ClaimID]
	claim := deploymentplan.Object{"kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"namespace": owner.Namespace, "name": owner.PVCName, "uid": owner.PVCUID}, "spec": deploymentplan.Object{"volumeName": owner.PVName}, "status": deploymentplan.Object{"phase": "Bound"}}
	volume := deploymentplan.Object{"kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": owner.PVName, "uid": owner.PVUID}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": owner.Namespace, "name": owner.PVCName, "uid": owner.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": owner.NativeID}}, "status": deploymentplan.Object{"phase": "Bound"}}
	if err := validateManagedTransportHostVolume(owner, claim, volume); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"wrong-claim-UID", "wrong-PV-UID", "wrong-claimRef", "wrong-CSI", "wrong-driver", "not-bound"} {
		t.Run(mode, func(t *testing.T) {
			c, v := deploymentplan.Clone(claim), deploymentplan.Clone(volume)
			switch mode {
			case "wrong-claim-UID":
				deploymentplan.Map(c["metadata"])["uid"] = "foreign"
			case "wrong-PV-UID":
				deploymentplan.Map(v["metadata"])["uid"] = "foreign"
			case "wrong-claimRef":
				deploymentplan.Map(deploymentplan.Map(v["spec"])["claimRef"])["uid"] = "foreign"
			case "wrong-CSI":
				deploymentplan.Map(deploymentplan.Map(v["spec"])["csi"])["volumeHandle"] = "99999"
			case "wrong-driver":
				deploymentplan.Map(deploymentplan.Map(v["spec"])["csi"])["driver"] = "foreign"
			case "not-bound":
				deploymentplan.Map(v["status"])["phase"] = "Pending"
			}
			if validateManagedTransportHostVolume(owner, c, v) == nil {
				t.Fatal("changed native binding accepted", mode)
			}
		})
	}
}
