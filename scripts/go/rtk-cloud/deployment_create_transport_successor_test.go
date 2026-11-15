package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type initialSuccessorTestBackend struct {
	object       deploymentplan.Object
	writes       int
	loseResponse bool
}

type initialSuccessorFlowBackend struct {
	objects      map[string]deploymentplan.Object
	expected     map[string]deploymentplan.Object
	writes       int
	loseResponse bool
}

func (b *initialSuccessorFlowBackend) Read(_ context.Context, id string) (deploymentplan.Object, error) {
	return deploymentplan.Clone(b.objects[id]), nil
}
func (b *initialSuccessorFlowBackend) Write(_ context.Context, _ string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if !dry {
		b.writes++
		b.objects[deploymentplan.ID(body)] = deploymentplan.Clone(body)
		if b.loseResponse {
			b.loseResponse = false
			return nil, errors.New("simulated lost response")
		}
	}
	return deploymentplan.Clone(body), nil
}
func (b *initialSuccessorFlowBackend) Wait(_ context.Context, resource deploymentplan.Resource) error {
	for id, desired := range b.expected {
		if !deploymentplan.Matches(desired, b.objects[id]) {
			return errors.New("a dependent deployment has not received successor trust")
		}
	}
	if strings.HasSuffix(resource.ID, "/mqtt-pki") {
		return errors.New("bootstrap trust helper is waiting for later PKI creation")
	}
	return nil
}

func TestInitialTransportSuccessorFlowResumesAndPublishesOnlyAfterVerification(t *testing.T) {
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	parent := e.Journal.PlanSHA256
	now := time.Now().UTC()
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := prepareInitialTransportSuccessor(store, parent, now)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(store.Root, "deployment", "create", "material", parent, "openbao-transport", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := renderEnvironmentCustodyTrust(cfg, parent, string(original), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := renderEnvironmentCustodyTrust(cfg, parent, candidate.Transports["openbao"].CACert, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment", "create", parent, "custody", "transport.json"), []byte(deploymentplan.String(deploymentplan.Map(before[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	continuation := issuerEnrollmentContinuationPrefix + strings.Repeat("b", 64)
	if err := e.BeginEntry(continuation, "continuation", json.RawMessage(`{"original":true}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"kubernetes/device-root-active", "kubernetes/app-root-trust", "kubernetes/app-intermediate-trust"} {
		body := json.RawMessage(`{"original":true}`)
		if err := e.BeginEntry(key, "bootstrap", body, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := e.RecordReceipt(key, body); err != nil {
			t.Fatal(err)
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	roleIntent := deploymentplan.Object{"role": "certissuer-dev", "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "service_accounts": []string{"certissuer"}, "policies": []string{"create-device-signer"}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	roleKey := "openbao-role-certissuer-dev-" + deploymentplan.Digest(roleIntent)[:12]
	if err := e.BeginEntry(roleKey, "bootstrap", mustProviderRecoveryJSON(roleIntent), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	source := e.Journal
	plan := initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: parent, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), JournalSHA: deploymentplan.Digest(source), CandidateSHA: deploymentplan.Digest(candidate)}
	backend := &initialSuccessorFlowBackend{objects: map[string]deploymentplan.Object{}, expected: map[string]deploymentplan.Object{}, loseResponse: true}
	for i := range before {
		id := deploymentplan.ID(before[i].Object)
		plan.Resources = append(plan.Resources, activeTransportRenewalResource{ID: id, UID: "original-" + id, Before: before[i].Object, Desired: after[i].Object})
		live := deploymentplan.Clone(before[i].Object)
		deploymentplan.Map(live["metadata"])["uid"] = "original-" + id
		deploymentplan.Map(live["metadata"])["resourceVersion"] = "1"
		backend.objects[id] = live
	}
	oldCert, err := parseFirstCertificatePEM(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mqtt-pki", "pki-controller"} {
		container := deploymentplan.Object{"name": name, "env": environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao", "PKI_STATE_BOOTSTRAP_ROOT_SHA256": environmentPreflightBytesSHA256(oldCert.Raw), "PKI_STATE_TRANSPORT_TRUST": environmentCustodyTrustPath + "/transport.json"})}
		if name == "mqtt-pki" {
			container["command"] = []any{"/app/pkibroker", "--deployment-bootstrap-trust"}
		}
		originalDeployment := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{container}}}}}
		updated, err := initialTransportCustodySuccessor(originalDeployment, string(original), candidate.Transports["openbao"].CACert)
		if err != nil {
			t.Fatal(err)
		}
		id := deploymentplan.ID(originalDeployment)
		plan.Resources = append(plan.Resources, activeTransportRenewalResource{ID: id, UID: "original-" + id, Before: originalDeployment, Desired: updated})
		live := deploymentplan.Clone(originalDeployment)
		deploymentplan.Map(live["metadata"])["uid"] = "original-" + id
		deploymentplan.Map(live["metadata"])["resourceVersion"] = "1"
		backend.objects[id], backend.expected[id] = live, updated
	}
	a := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	base := filepath.Join("deployment", "create", "transport-successor", deploymentplan.Digest(a))
	if err := store.write(filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(a), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join(base, "source-journal.json"), mustProviderRecoveryJSON(source), false); err != nil {
		t.Fatal(err)
	}
	reloads, verifies := 0, 0
	reload := func() error { reloads++; return nil }
	verify := func() error { verifies++; return nil }
	check := func() error { return nil }
	if err := executeInitialTransportSuccessor(t.Context(), cfg, store, e, backend, a, source, candidate, check, reload, verify); err == nil {
		t.Fatal("lost response incorrectly completed recovery")
	}
	if _, ok, err := readCompletedInitialTransportSuccessor(store, parent); err != nil || ok {
		t.Fatal("partial execution published completed trust", err)
	}
	if _, ok, err := initialSuccessorContinuationEntry(store, e); err != nil || ok {
		t.Fatal("partial execution incorrectly admitted continuation", err)
	}
	if err := executeInitialTransportSuccessor(t.Context(), cfg, store, e, backend, a, source, candidate, check, reload, verify); err != nil {
		t.Fatal("resume failed", err)
	}
	if backend.writes != 4 || reloads != 1 || verifies != 1 {
		t.Fatal("unexpected replay or verification counts", backend.writes, reloads, verifies)
	}
	if _, ok, err := readCompletedInitialTransportSuccessor(store, parent); err != nil || !ok {
		t.Fatal("completed trust was not published", err)
	}
	if entry, ok, err := initialSuccessorContinuationEntry(store, e); err != nil || !ok || entry.Key != continuation {
		t.Fatal("completed successor did not preserve the original continuation", entry.Key, err)
	}
	if checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store); err != nil || checkpoint.Profile != "initial-transport-successor-continuation" {
		t.Fatal("completed successor checkpoint was not recognized", checkpoint.Profile, err)
	}
	partialReview := strings.Repeat("a", 64)
	if err := e.BeginEntry("partial-session-renewal-"+partialReview, "partial-session-renewal", json.RawMessage(`{"selected":true}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stage := "service-session-original-key-renewal-" + partialReview
	if err := e.BeginEntry("kubernetes/"+stage, stage, json.RawMessage(`{"selected":true}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if checkpoint, err := inspectCreateRecoveryCheckpoint(cfg, store); err != nil || checkpoint.Profile != "initial-transport-successor-continuation" || checkpoint.Flag != "" {
		t.Fatal("original successor continuation was misclassified as a historical seed forward", checkpoint.Profile, err)
	}
	if !initialSuccessorAllowsPendingRole(store, e, roleKey, roleIntent) {
		t.Fatal("original pending CertIssuer role cannot resume")
	}
	foreign := deploymentplan.Clone(roleIntent)
	foreign["policies"] = []string{"foreign"}
	if initialSuccessorAllowsPendingRole(store, e, roleKey, foreign) {
		t.Fatal("foreign role intent was accepted")
	}
}

func (b *initialSuccessorTestBackend) Read(_ context.Context, _ string) (deploymentplan.Object, error) {
	return deploymentplan.Clone(b.object), nil
}
func (b *initialSuccessorTestBackend) Write(_ context.Context, _ string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if !dry {
		b.writes++
		b.object = deploymentplan.Clone(body)
		if b.loseResponse {
			b.loseResponse = false
			return nil, errors.New("simulated lost response")
		}
	}
	return deploymentplan.Clone(body), nil
}
func (*initialSuccessorTestBackend) Wait(context.Context, deploymentplan.Resource) error { return nil }

func TestInitialTransportSuccessorResourceRecoversLostResponse(t *testing.T) {
	_, store, _, execution, _ := createOpenBaoNativeFixture(t, "valid")
	before := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-transport-trust", "namespace": "dev", "uid": "original", "resourceVersion": "1"}, "data": deploymentplan.Object{"ca.crt": "old", "unrelated": "keep"}}
	after := deploymentplan.Clone(before)
	deploymentplan.Map(after["data"])["ca.crt"] = "new"
	resource := activeTransportRenewalResource{ID: deploymentplan.ID(before), UID: "original", Before: before, Desired: after}
	backend := &initialSuccessorTestBackend{object: deploymentplan.Clone(before), loseResponse: true}
	key, base := "initial-transport-successor-test", "deployment/create/transport-successor/test"
	if err := applyInitialTransportSuccessorResource(t.Context(), store, execution, backend, key, base, resource, "old", "new", func() error { return nil }); err == nil {
		t.Fatal("lost response was treated as a completed write")
	}
	if err := applyInitialTransportSuccessorResource(t.Context(), store, execution, backend, key, base, resource, "old", "new", func() error { return nil }); err != nil {
		t.Fatal("could not reconcile the original successful write", err)
	}
	if backend.writes != 1 || deploymentplan.Map(backend.object["data"])["unrelated"] != "keep" {
		t.Fatal("recovery replayed the write or changed unrelated data")
	}
	entry := createOpenBaoACKCompletionEntry(execution.Journal, key+"-"+strings.ReplaceAll(resource.ID, "/", "-"))
	if entry == nil || entry.State != "completed" {
		t.Fatal("verified readback was not recorded")
	}
}

func TestInitialTransportSuccessorCommandRequiresExplicitPhase(t *testing.T) {
	var out bytes.Buffer
	if err := runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), nil, &out, &out); err == nil {
		t.Fatal("recovery command accepted missing environment, generation and phase")
	}
	if err := runEnvironmentCreateInitialTransportSuccessorContext(t.Context(), []string{"--environment", "dev", "--parent-plan-sha256", strings.Repeat("a", 64), "--prepare", "--qualify"}, &out, &out); err == nil {
		t.Fatal("recovery command accepted two conflicting phases")
	}
}

func TestInitialTransportSuccessorArtifactBindsQualificationAndJournal(t *testing.T) {
	now := time.Now().UTC()
	source := environmentCreateJournal{Environment: "dev", PlanSHA256: strings.Repeat("a", 64), OwnerToken: "original"}
	resource := activeTransportRenewalResource{ID: "ConfigMap/example/trust", UID: "owner", Before: deploymentplan.Object{"data": "old"}, Desired: deploymentplan.Object{"data": "new"}}
	plan := initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: source.PlanSHA256, JournalSHA: deploymentplan.Digest(source), CandidateSHA: strings.Repeat("b", 64), Resources: []activeTransportRenewalResource{resource}}
	a := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	review := deploymentplan.Digest(a)
	if err := validateInitialTransportSuccessorArtifact(a, review, source, now.Add(time.Minute)); err != nil {
		t.Fatal("exact fresh qualification rejected", err)
	}
	if err := validateInitialTransportSuccessorArtifact(a, strings.Repeat("c", 64), source, now); err == nil {
		t.Fatal("foreign review digest accepted")
	}
	changed := source
	changed.OwnerToken = "foreign"
	if err := validateInitialTransportSuccessorArtifact(a, review, changed, now); err == nil {
		t.Fatal("changed source journal accepted")
	}
	if err := validateInitialTransportSuccessorArtifact(a, review, source, a.ValidUntil); err == nil {
		t.Fatal("expired qualification accepted for a new execution")
	}
}

func TestInitialTransportSuccessorCompletedProofRequiresOriginalJournal(t *testing.T) {
	now := time.Now().UTC()
	source := environmentCreateJournal{Environment: "dev", PlanSHA256: strings.Repeat("a", 64), OwnerToken: "original", Entries: []environmentCreateJournalEntry{{Key: "existing", State: "completed"}}}
	resource := activeTransportRenewalResource{ID: "ConfigMap/example/trust", UID: "owner", Before: deploymentplan.Object{"data": "old"}, Desired: deploymentplan.Object{"data": "new"}}
	plan := initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: source.PlanSHA256, JournalSHA: deploymentplan.Digest(source), CandidateSHA: strings.Repeat("b", 64), Resources: []activeTransportRenewalResource{resource}}
	a := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	review := deploymentplan.Digest(a)
	proof := initialTransportSuccessorProof{Schema: "rtk-cloud.initial-transport-successor-proof/v1", Parent: source.PlanSHA256, Review: review, CandidateSHA: plan.CandidateSHA, Completed: true}
	intent := initialTransportSuccessorIntent(proof)
	entry := environmentCreateJournalEntry{Key: "initial-transport-successor-" + review, Phase: "recovery", State: "completed", IntentSHA256: deploymentplan.Digest(intent), StartedAt: now.Add(time.Minute), CompletedAt: now.Add(2 * time.Minute), Receipt: mustProviderRecoveryJSON(proof)}
	current := source
	partIntent := initialTransportSuccessorResourceIntent(resource)
	part := environmentCreateJournalEntry{Key: entry.Key + "-ConfigMap-example-trust", Phase: "recovery", State: "completed", IntentSHA256: deploymentplan.Digest(partIntent), StartedAt: now.Add(time.Minute), CompletedAt: now.Add(2 * time.Minute), Receipt: mustProviderRecoveryJSON(partIntent)}
	current.Entries = append(append([]environmentCreateJournalEntry(nil), source.Entries...), entry, part)
	if err := validateInitialTransportSuccessorProof(a, proof, source, current); err != nil {
		t.Fatal("completed matching proof rejected", err)
	}
	current.Entries[len(source.Entries)].State = "pending"
	if err := validateInitialTransportSuccessorProof(a, proof, source, current); err == nil {
		t.Fatal("pending repair was treated as complete")
	}
	current.Entries[len(source.Entries)] = entry
	current.Entries[len(current.Entries)-1].Receipt = json.RawMessage(`{"foreign":true}`)
	if err := validateInitialTransportSuccessorProof(a, proof, source, current); err == nil {
		t.Fatal("foreign resource receipt was accepted")
	}
	current.Entries[len(current.Entries)-1] = part
	current.Entries[0].Key = "foreign"
	if err := validateInitialTransportSuccessorProof(a, proof, source, current); err == nil {
		t.Fatal("changed original journal was accepted")
	}
}

func TestInitialTransportSuccessorReaderRequiresCompletedProof(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	parent := strings.Repeat("a", 64)
	now := time.Now().UTC()
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := prepareInitialTransportSuccessor(store, parent, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readCompletedInitialTransportSuccessor(store, parent); err != nil || ok {
		t.Fatal("uncompleted candidate was accepted", err)
	}
	source := environmentCreateJournal{Environment: "dev", PlanSHA256: parent, OwnerToken: "original", Entries: []environmentCreateJournalEntry{{Key: "existing", State: "completed"}}}
	resource := activeTransportRenewalResource{ID: "ConfigMap/example/trust", UID: "owner", Before: deploymentplan.Object{"data": "old"}, Desired: deploymentplan.Object{"data": "new"}}
	plan := initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: parent, JournalSHA: deploymentplan.Digest(source), CandidateSHA: deploymentplan.Digest(candidate), Resources: []activeTransportRenewalResource{resource}}
	a := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	review := deploymentplan.Digest(a)
	journalPath := filepath.Join("deployment", "create", "executions", parent, "execution-fixture", "journal.json")
	proof := initialTransportSuccessorProof{Schema: "rtk-cloud.initial-transport-successor-proof/v1", Parent: parent, Review: review, CandidateSHA: plan.CandidateSHA, Journal: journalPath, Completed: true}
	key := "initial-transport-successor-" + review
	intent := initialTransportSuccessorIntent(proof)
	partIntent := initialTransportSuccessorResourceIntent(resource)
	journal := source
	journal.Entries = append(append([]environmentCreateJournalEntry(nil), source.Entries...),
		environmentCreateJournalEntry{Key: key, Phase: "recovery", State: "completed", IntentSHA256: deploymentplan.Digest(intent), StartedAt: now.Add(time.Minute), CompletedAt: now.Add(3 * time.Minute), Receipt: mustProviderRecoveryJSON(proof)},
		environmentCreateJournalEntry{Key: key + "-ConfigMap-example-trust", Phase: "recovery", State: "completed", IntentSHA256: deploymentplan.Digest(partIntent), StartedAt: now.Add(time.Minute), CompletedAt: now.Add(2 * time.Minute), Receipt: mustProviderRecoveryJSON(partIntent)})
	base := filepath.Join("deployment", "create", "transport-successor", review)
	for _, role := range []string{"management", "openbao"} {
		if err := store.write(filepath.Join(base, role+"-transport", "ca.crt"), []byte(candidate.Transports[role].CACert), false); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]any{filepath.Join(base, "qualification.json"): a, filepath.Join(base, "source-journal.json"): source, filepath.Join(base, "completed.json"): proof, journalPath: journal, filepath.Join("deployment", "create", "material", parent, "initial-transport-successor.json"): proof} {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := readCompletedInitialTransportSuccessor(store, parent)
	if err != nil || !ok || deploymentplan.Digest(got) != deploymentplan.Digest(candidate) {
		t.Fatal("valid completed successor rejected", err)
	}
	path, ca, err := initialTransportAuthorityPath(store, parent, "openbao")
	if err != nil || ca != candidate.Transports["openbao"].CACert || path != filepath.Join(store.Root, base, "openbao-transport", "ca.crt") {
		t.Fatal("completed successor authority was not selected", err)
	}
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	originalCA, err := os.ReadFile(filepath.Join(store.Root, "deployment", "create", "material", parent, "openbao-transport", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, trust := range []struct{ path, ca string }{
		{filepath.Join("deployment", "create", parent, "custody", "transport.json"), string(originalCA)},
		{filepath.Join(base, "custody", "transport.json"), candidate.Transports["openbao"].CACert},
	} {
		definitions, err := renderEnvironmentCustodyTrust(cfg, parent, trust.ca, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.write(trust.path, []byte(deploymentplan.String(deploymentplan.Map(definitions[0].Object["data"])["transport.json"])), false); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _, err := readEnvironmentCustodyTransportState(cfg, store, parent)
	if err != nil || snapshot.BootstrapPEM != candidate.Transports["openbao"].CACert {
		t.Fatal("custody did not consume completed successor trust", err)
	}
	journal.Entries[len(journal.Entries)-1].State = "pending"
	if err := store.write(journalPath, mustProviderRecoveryJSON(journal), true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readCompletedInitialTransportSuccessor(store, parent); err == nil || ok {
		t.Fatal("incomplete native receipt accepted")
	}
}

func TestInitialTransportSuccessorStagesTrustWithoutChangingOriginal(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	parent := strings.Repeat("a", 64)
	now := time.Now().UTC()
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := prepareInitialTransportSuccessor(store, parent, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(filepath.Join(store.Root, "deployment", "create", "material", parent, "openbao-transport", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	before, err := renderEnvironmentCustodyTrust(cfg, parent, string(old), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := renderEnvironmentCustodyTrust(cfg, parent, candidate.Transports["openbao"].CACert, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	original := deploymentplan.String(deploymentplan.Map(before[0].Object["data"])["transport.json"])
	originalPath := filepath.Join("deployment", "create", parent, "custody", "transport.json")
	if err := store.write(originalPath, []byte(original), false); err != nil {
		t.Fatal(err)
	}
	plan := initialTransportSuccessorPlan{Parent: parent, CandidateSHA: deploymentplan.Digest(candidate)}
	for i := range before {
		plan.Resources = append(plan.Resources, activeTransportRenewalResource{ID: deploymentplan.ID(before[i].Object), UID: "fixture", Before: before[i].Object, Desired: after[i].Object})
	}
	a := initialTransportSuccessorArtifact{Plan: plan, QualifiedAt: now}
	if err := stageInitialTransportSuccessorMaterial(cfg, store, a, candidate); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join("deployment", "create", "transport-successor", deploymentplan.Digest(a))
	for _, role := range []string{"management", "openbao"} {
		var stagedIdentity environmentCreateTransport
		if err := readExecutionJSON(filepath.Join(store.Root, base, role+"-transport", "identities.json"), &stagedIdentity); err != nil || deploymentplan.Digest(stagedIdentity) != deploymentplan.Digest(candidate.Transports[role]) {
			t.Fatal("successor serving identities were not retained", err)
		}
	}
	staged, err := os.ReadFile(filepath.Join(store.Root, base, "custody", "transport.json"))
	if err != nil || string(staged) != deploymentplan.String(deploymentplan.Map(after[0].Object["data"])["transport.json"]) {
		t.Fatal("successor trust was not staged", err)
	}
	retained, err := os.ReadFile(filepath.Join(store.Root, originalPath))
	if err != nil || string(retained) != original {
		t.Fatal("original trust was changed", err)
	}
	plan.Resources[1].Desired = deploymentplan.Clone(plan.Resources[1].Desired)
	deploymentplan.Map(plan.Resources[1].Desired["data"])["transport.json"] = "foreign"
	a.Plan = plan
	if err := stageInitialTransportSuccessorMaterial(cfg, store, a, candidate); err == nil {
		t.Fatal("mismatched trust consumers accepted")
	}
}

func TestInitialTransportSuccessorResumesOnlyKnownResourceOutcomes(t *testing.T) {
	before := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "trust", "namespace": "dev", "uid": "original"}, "data": deploymentplan.Object{"ca.crt": "old"}}
	after := deploymentplan.Clone(before)
	deploymentplan.Map(after["data"])["ca.crt"] = "new"
	r := activeTransportRenewalResource{ID: deploymentplan.ID(before), UID: "original", Before: before, Desired: after}
	intent := initialTransportSuccessorResourceIntent(r)
	pending := environmentCreateJournalEntry{State: "pending", IntentSHA256: deploymentplan.Digest(intent)}
	completed := environmentCreateJournalEntry{State: "completed", IntentSHA256: deploymentplan.Digest(intent), Receipt: mustProviderRecoveryJSON(intent), CompletedAt: time.Now()}
	for _, test := range []struct {
		name      string
		entry     *environmentCreateJournalEntry
		actual    deploymentplan.Object
		write, ok bool
	}{
		{"fresh", nil, before, true, true},
		{"resume-before", &pending, before, true, true},
		{"resume-after", &pending, after, false, true},
		{"completed", &completed, after, false, true},
		{"unrecorded-after", nil, after, false, false},
		{"completed-before", &completed, before, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			write, err := initialTransportSuccessorNeedsWrite(r, test.entry, test.actual)
			if (err == nil) != test.ok || test.ok && write != test.write {
				t.Fatalf("write=%v err=%v", write, err)
			}
		})
	}
}

func TestInitialTransportSuccessorCandidateSurvivesRetryWithoutReplacingKeys(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	parent := strings.Repeat("a", 64)
	now := time.Now().UTC()
	for _, role := range []string{"management", "openbao"} {
		if _, err := createEnvironmentTransport(store, parent, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-5*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readInitialTransportSuccessorCandidate(store, parent, now); err == nil {
		t.Fatal("qualification created an unprepared candidate")
	}
	first, err := prepareInitialTransportSuccessor(store, parent, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := prepareInitialTransportSuccessor(store, parent, now.Add(time.Minute))
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(again) {
		t.Fatal("retry changed temporary authority material", err)
	}
	readOnly, err := readInitialTransportSuccessorCandidate(store, parent, now.Add(time.Minute))
	if err != nil || deploymentplan.Digest(first) != deploymentplan.Digest(readOnly) {
		t.Fatal("qualification changed retained candidate", err)
	}
	path := filepath.Join(store.Root, "deployment", "create", parent, "transport-successor", "candidate.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("candidate is not a private regular file", err)
	}
	if _, err := prepareInitialTransportSuccessor(store, parent, now.Add(26*time.Hour)); err == nil {
		t.Fatal("expired candidate was silently regenerated")
	}
	if err := os.WriteFile(path, []byte(`{"schema":"interrupted"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareInitialTransportSuccessor(store, parent, now); err == nil {
		t.Fatal("partially written candidate was silently replaced")
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != `{"schema":"interrupted"}` {
		t.Fatal("partial candidate was changed", err)
	}
}

func TestInitialTransportSuccessorCheckpointRejectsUncertainWrites(t *testing.T) {
	now := time.Now().UTC()
	complete := func(key string) environmentCreateJournalEntry {
		return environmentCreateJournalEntry{Key: key, State: "completed", CompletedAt: now, Receipt: json.RawMessage(`{}`)}
	}
	parent := environmentCreateJournalEntry{Key: issuerEnrollmentContinuationPrefix + strings.Repeat("a", 64), Phase: "continuation", State: "pending", StartedAt: now}
	intent := strings.Repeat("b", 64)
	role := environmentCreateJournalEntry{Key: "openbao-role-certissuer-dev-" + intent[:12], Phase: "bootstrap", State: "pending", IntentSHA256: intent, StartedAt: now}
	journal := environmentCreateJournal{Environment: "dev", Entries: []environmentCreateJournalEntry{parent, complete("kubernetes/device-root-active"), complete("kubernetes/app-root-trust"), complete("kubernetes/app-intermediate-trust"), role}}
	if got, ok := initialTransportSuccessorCheckpoint(journal); !ok || got.Key != parent.Key {
		t.Fatal("original pending role checkpoint rejected")
	}
	journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: "unrelated", State: "pending"})
	if _, ok := initialTransportSuccessorCheckpoint(journal); ok {
		t.Fatal("unknown later mutation accepted")
	}
	journal.Entries = journal.Entries[:len(journal.Entries)-1]
	journal.Entries[len(journal.Entries)-1].IntentSHA256 = strings.Repeat("c", 64)
	if _, ok := initialTransportSuccessorCheckpoint(journal); ok {
		t.Fatal("changed role intent accepted")
	}
}

func TestInitialTransportCustodySuccessorPreservesManagedTrustAndScope(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	now := time.Now().UTC()
	old, err := createEnvironmentTransport(store, strings.Repeat("a", 64), "openbao", map[string][]string{"openbao": {"openbao.isolated.svc"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := renewInitialTransportMaterial(old, now.Add(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	oldCert, err := parseFirstCertificatePEM([]byte(old.CACert))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := environmentCustodyTransportTrust{Schema: "rtk.custody.transport/v1", Environment: "dev", Generation: strings.Repeat("a", 64), ServerName: "openbao.isolated.svc", BootstrapPEM: old.CACert, ManagedRootID: "original-managed-root", ManagedRootSHA256: strings.Repeat("d", 64), ManagedPEM: "original-managed-certificate"}
	raw, _ := json.Marshal(snapshot)
	cm := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": environmentCustodyTrustName}, "data": deploymentplan.Object{"transport.json": string(raw), "managed-root.pem": snapshot.ManagedPEM}}
	container := deploymentplan.Object{"name": "app", "env": environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao", "PKI_STATE_BOOTSTRAP_ROOT_SHA256": environmentPreflightBytesSHA256(oldCert.Raw), "PKI_STATE_TRANSPORT_TRUST": environmentCustodyTrustPath + "/transport.json"})}
	pod := deploymentplan.Object{"containers": []any{container}}
	template := deploymentplan.Object{"spec": pod}
	consumer := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": "original-consumer"}, "spec": deploymentplan.Object{"template": template}}
	for _, original := range []deploymentplan.Object{cm, consumer} {
		if !initialTransportReferencesAuthority(original, old.CACert, old.CACert) {
			t.Fatal("missed an original custody trust reference")
		}
		before := deploymentplan.Digest(original)
		updated, err := initialTransportCustodySuccessor(original, old.CACert, next.CACert)
		if err != nil || deploymentplan.Digest(original) != before || deploymentplan.Digest(updated) == before {
			t.Fatal("trust successor altered original or failed to update", err)
		}
		if _, err := initialTransportCustodySuccessor(original, next.CACert, old.CACert); err == nil {
			t.Fatal("foreign original anchor accepted")
		}
		if original["kind"] == "ConfigMap" {
			var got environmentCustodyTransportTrust
			if json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(updated["data"])["transport.json"])), &got) != nil || got.BootstrapPEM != next.CACert || got.ManagedRootID != snapshot.ManagedRootID || got.ManagedPEM != snapshot.ManagedPEM || got.Generation != snapshot.Generation {
				t.Fatal("managed trust or original generation changed")
			}
		}
		live := deploymentplan.Clone(original)
		deploymentplan.Map(live["metadata"])["uid"] = "original-owner"
		deploymentplan.Map(live["metadata"])["resourceVersion"] = "123"
		deploymentplan.Map(live["metadata"])["annotations"] = deploymentplan.Object{"unrelated": "kept"}
		resource := activeTransportRenewalResource{ID: deploymentplan.ID(original), UID: "original-owner", Before: original, Desired: updated}
		body, err := initialTransportSuccessorResourceBody(resource, live, old.CACert, next.CACert)
		if err != nil || deploymentplan.Map(body["metadata"])["resourceVersion"] != "123" || deploymentplan.Map(deploymentplan.Map(body["metadata"])["annotations"])["unrelated"] != "kept" || !deploymentplan.Matches(updated, body) {
			t.Fatal("qualified update lost original owner, version or metadata", err)
		}
		deploymentplan.Map(live["metadata"])["uid"] = "foreign-owner"
		if _, err := initialTransportSuccessorResourceBody(resource, live, old.CACert, next.CACert); err == nil {
			t.Fatal("foreign owner accepted")
		}
	}
	secret := deploymentplan.Object{"kind": "Secret", "data": deploymentplan.Object{"ca.crt": base64.StdEncoding.EncodeToString([]byte(old.CACert))}}
	if !initialTransportReferencesAuthority(secret, old.CACert, old.CACert) {
		t.Fatal("missed an encoded original authority")
	}
	if initialTransportReferencesAuthority(deploymentplan.Object{"kind": "ConfigMap", "data": deploymentplan.Object{"other": "unrelated"}}, old.CACert, old.CACert) {
		t.Fatal("unrelated configuration was marked as an original authority")
	}
}

func TestInitialTransportSuccessorUpdatesOnlyExactTrustAnchor(t *testing.T) {
	original := deploymentplan.Object{"data": deploymentplan.Object{"roots.pem": "managed-root\nold-temporary-root\n", "other": "unchanged"}}
	updated, err := initialTransportReplacePublicCA(original, "roots.pem", "old-temporary-root", "new-temporary-root")
	if err != nil || deploymentplan.Map(updated["data"])["roots.pem"] != "managed-root\nnew-temporary-root\n" || deploymentplan.Map(updated["data"])["other"] != "unchanged" || deploymentplan.Map(original["data"])["roots.pem"] != "managed-root\nold-temporary-root\n" {
		t.Fatal("temporary trust update changed unrelated state", err)
	}
	for _, value := range []string{"other-root", "old-temporary-root\nold-temporary-root"} {
		changed := deploymentplan.Clone(original)
		deploymentplan.Map(changed["data"])["roots.pem"] = value
		if _, err := initialTransportReplacePublicCA(changed, "roots.pem", "old-temporary-root", "new-temporary-root"); err == nil {
			t.Fatal("accepted missing or duplicated original authority")
		}
	}
}

func TestInitialTransportSuccessorRejectsUnrelatedDataChange(t *testing.T) {
	before := deploymentplan.Object{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-transport-trust", "namespace": "example", "uid": "owner", "resourceVersion": "1"}, "data": deploymentplan.Object{"ca.crt": "old", "unrelated": "original"}}
	desired := deploymentplan.Clone(before)
	deploymentplan.Map(desired["data"])["ca.crt"] = "new"
	deploymentplan.Map(desired["data"])["unrelated"] = "changed"
	r := activeTransportRenewalResource{ID: deploymentplan.ID(before), UID: "owner", Before: before, Desired: desired}
	if _, err := initialTransportSuccessorResourceBody(r, before, "old", "new"); err == nil {
		t.Fatal("successor update admitted an unrelated data change")
	}
}

func TestInitialTransportSuccessorRejectsForeignCustody(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	now := time.Now().UTC()
	old, err := createEnvironmentTransport(store, strings.Repeat("a", 64), "openbao", map[string][]string{"openbao": {"openbao.isolated.svc"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := renewInitialTransportMaterial(old, now.Add(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []deploymentplan.Object{
		{"kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "foreign"}, "data": deploymentplan.Object{"transport.json": `{}`}},
		{"kind": "Deployment", "metadata": deploymentplan.Object{"name": "foreign"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app"}}}}}},
		{"kind": "Secret", "metadata": deploymentplan.Object{"name": "foreign"}},
	} {
		if _, err := initialTransportCustodySuccessor(bad, old.CACert, next.CACert); err == nil {
			t.Fatal("foreign resource admitted", deploymentplan.ID(bad))
		}
	}
}
