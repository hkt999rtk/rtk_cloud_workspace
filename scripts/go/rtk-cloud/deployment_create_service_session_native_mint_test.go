package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Exercise the real Mint callback, managed recovery and Service-session
// orchestration through the native Job write/wait boundary. Historical source
// sessions are fixtures; this test deliberately stops before Job execution,
// custody publication, or an ACK, and does not claim full Mint completion.
func TestServiceSessionNativeMintRetainsQualifiedJobOnWaitRetry(t *testing.T) {
	f := newServiceSessionNativeMintFixture(t)
	ctx := t.Context()
	execution, err := nativeServiceSessionBatchExecution(ctx, f.cfg, f.store, f.e, &f.parent, f.native, f.runtime, f.service, f.service.Root, f.q, f.review, func(context.Context) error { return nil }, io.Discard)
	if err != nil {
		t.Fatal("native batch adapter", err)
	}
	stage := "service-session-original-key-renewal-" + f.review
	expected := f.q.Renewal.Candidate
	var firstInput []byte
	var firstUID any
	for attempt := 0; attempt < 2; attempt++ {
		err := execution.Mint(ctx)
		var waitFailure *deploymentplan.Blocker
		if !errors.As(err, &waitFailure) || waitFailure.ID != "deployment.rollout" || waitFailure.Resource != expected.Resources[0].ID {
			t.Fatalf("real Mint did not reach native Job wait (attempt %d): %v", attempt, err)
		}
		retained, err := readEnvironmentCreateStageInput(f.store, f.e, stage)
		if err != nil || retained == nil || retained.SHA256() != expected.SHA256() {
			actual := "missing"
			if retained != nil {
				actual = retained.SHA256()
			}
			t.Fatalf("Mint recompiled its qualified Job: retained=%s qualified=%s error=%v", actual, expected.SHA256(), err)
		}
		input, err := os.ReadFile(environmentCreateStageInputPath(f.e, stage))
		if err != nil {
			t.Fatal(err)
		}
		parent := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+stage)
		intent, err := environmentCreateKubernetesStageIntent(expected)
		if err != nil || parent == nil || parent.State == "completed" || len(parent.Receipt) != 0 || parent.IntentSHA256 != activeTransportJournalDigest(json.RawMessage(intent)) {
			t.Fatal("native wait did not retain the qualified pending parent intent", err)
		}
		child, exists, err := expected.ReadExecution(f.store.Root)
		if err != nil || !exists || child.PlanSHA256 != expected.SHA256() || child.Accepted || child.Owners[expected.Resources[0].ID] == "" {
			t.Fatal("native wait did not retain the qualified rollback child", err)
		}
		job, err := f.native.Read(ctx, expected.Resources[0].ID)
		if err != nil || job == nil || !deploymentplan.Matches(expected.Resources[0].Desired, job) {
			t.Fatal("native qualified Job missing", err)
		}
		uid := deploymentplan.Map(job["metadata"])["uid"]
		if child.Owners[expected.Resources[0].ID] != deploymentplan.String(uid) {
			t.Fatal("native Job UID differs from its qualified rollback owner")
		}
		if attempt == 0 {
			firstInput, firstUID = input, uid
			// A live lookalike must not be treated as this admitted write. Restore
			// the fixture only after the real Mint rejects its replacement UID.
			original := deploymentplan.Clone(job)
			deploymentplan.Map(job["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			path := filepath.Join(f.nativeDir, strings.ReplaceAll(expected.Resources[0].ID, "/", "_")+".json")
			if err := os.WriteFile(path, mustProviderRecoveryJSON(job), 0600); err != nil {
				t.Fatal(err)
			}
			beforeCalls, err := os.ReadFile(f.calls)
			if err != nil {
				t.Fatal(err)
			}
			tamperErr := execution.Mint(ctx)
			afterCalls, err := os.ReadFile(f.calls)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(original), 0600); err != nil {
				t.Fatal(err)
			}
			if tamperErr == nil || strings.Count(string(beforeCalls), "fixture native Job wait interrupted\n") != strings.Count(string(afterCalls), "fixture native Job wait interrupted\n") {
				t.Fatal("Mint adopted or waited for a replacement Job UID after wait interruption")
			}
		} else if !bytes.Equal(firstInput, input) || firstUID != uid {
			t.Fatal("Mint retry replaced its original input or Job UID")
		}
	}
	calls, err := os.ReadFile(f.calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(calls), "fixture native Job created\n"); n != 1 {
		t.Fatalf("Mint wrote the Job %d times", n)
	}
	if n := strings.Count(string(calls), "fixture native Job wait interrupted\n"); n != 2 {
		t.Fatalf("Mint reached its original Job wait %d times", n)
	}
	for path, want := range f.custody {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("native wait retry changed original key/CSR custody", err)
		}
	}
	// No later native custody or credential stage may be manufactured while
	// the original qualified Job is still waiting.
	if createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/service-custody-reader-renewal-"+f.review) != nil || createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/service-credential-secrets-renewal-"+f.review) != nil {
		t.Fatal("waiting Job advanced current Mint custody or credentials")
	}
}

type serviceSessionNativeMintFixture struct {
	cfg                      deploymentConfig
	store                    secretStore
	e                        *environmentCreateExecution
	parent                   environmentCreatePlan
	native                   environmentKubernetesBackend
	runtime                  environmentCreateBootstrapRuntime
	service                  environmentCreateServiceCeremony
	q                        secondSessionAdmission
	review, calls, nativeDir string
	custody                  map[string][]byte
}

func newServiceSessionNativeMintFixture(t *testing.T) serviceSessionNativeMintFixture {
	t.Helper()
	ctx := t.Context()
	base := newNativeCreateStageContract(t, "normal")
	cfg, store, plan := base.cfg, base.store, base.parent
	image := plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"]
	subjects := []string{"service:fixture"}
	plan.Bootstrap = environmentCreateBootstrapPlan{Stack: plan.Stack, Image: image.Reference, Domains: []environmentCreateIssuerPolicy{{Domain: "service", ServiceClientIDs: subjects}}}
	e, _ := providerExecutionContractFork(t, base.e, plan, "isolated-native-mint")
	e.originalPlanSHA = deploymentplan.Digest(plan)
	backend := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	api := initializerDefaultBackend{backend}
	ns := plan.Stack + "-video-cloud"
	now := time.Now().UTC()
	root, rootSigner, rootPEM := coreContractCertificate(t, "fixture root", true, nil, nil, 2*365*24*time.Hour)
	issuer, signer, issuerPEM := coreContractCertificate(t, "fixture issuer", true, root, rootSigner, 365*24*time.Hour)
	service := environmentCreateServiceCeremony{Root: environmentCreateRegisteredIssuer{ID: "11111111-2222-4333-8444-555555555555", Status: "active", Certificate: rootPEM, RootFingerprint: environmentPreflightBytesSHA256(root.Raw)}, Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222", Status: "active", Certificate: issuerPEM}, BootstrapCredentialNamespace: ns, BootstrapCredentialNamespaces: []string{ns}, CustodyReader: "pki-bootstrap-custody-" + e.Journal.PlanSHA256[:12], BootstrapClaim: "fixture-pki-state"}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	certificate := func(serial int64, expires time.Time) ([]byte, []byte) {
		t.Helper()
		leaf, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-2 * time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, issuer, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}), []byte(issuerPEM+rootPEM)...), leaf
	}
	originalCert, originalDER := certificate(99, now.Add(-30*time.Minute))
	firstCert, firstDER := certificate(100, now.Add(-time.Minute))
	old := "33333333-3333-4333-8333-333333333333"
	intent := createServiceSessionIntent{SessionID: old, Claim: service.BootstrapClaim, Subjects: subjects, IssuerID: service.Intermediate.ID, RootSHA: service.Root.RootFingerprint, Namespaces: []string{ns}}
	env := map[string]string{"PKI_BOOTSTRAP_SESSION_ID": old, "PKI_BOOTSTRAP_SUBJECTS": strings.Join(subjects, ","), "PKI_BOOTSTRAP_ISSUER_ID": service.Intermediate.ID, "PKI_BOOTSTRAP_ROOT_SHA256": service.Root.RootFingerprint, "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt", "OPENBAO_ADDR": "https://openbao." + plan.Stack + "-secrets.svc:8200", "OPENBAO_CACERT": "/run/openbao-ca/ca.crt"}
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": ns, "name": "fixture-original"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": image.Reference, "env": environmentCreateEnv(env), "volumeMounts": []any{deploymentplan.Object{"name": "openbao-ca", "mountPath": "/run/openbao-ca", "readOnly": true}}}}, "initContainers": []any{deploymentplan.Object{"name": "permissions"}}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": service.BootstrapClaim}}, deploymentplan.Object{"name": "openbao-ca", "configMap": deploymentplan.Object{"name": "pki-openbao-transport-ca"}}}}}}}
	makePlan := func(stage string, defs []deploymentplan.Definition) *deploymentplan.Plan {
		t.Helper()
		result, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: plan.Stack, Operation: "scoped", ConfigurationSHA256: plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	run := func(stage string, child *deploymentplan.Plan) {
		t.Helper()
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(ctx, store, e, stage, child, api, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(stage, err)
		}
	}
	source := makePlan("service-session", []deploymentplan.Definition{{Phase: "bootstrap", Object: job}})
	run("service-session", source)
	sourceJournal, _, err := source.ReadExecution(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	firstBinding := strings.Repeat("b", 64)
	firstIntent := intent
	firstIntent.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, firstBinding, old)
	definition, err := managedSessionRenewalDefinition(job, e.Journal.PlanSHA256, firstBinding, firstIntent.SessionID, image, plan.Stack)
	if err != nil {
		t.Fatal(err)
	}
	first := makePlan("first-candidate", []deploymentplan.Definition{definition})
	oldState := managedSessionState{Session: old, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(originalDER), Status: "expired", Expires: now.Add(-30 * time.Minute).Truncate(time.Second), Subjects: subjects}
	firstState := managedSessionState{Session: firstIntent.SessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(firstDER), Status: "expired", Expires: now.Add(-time.Minute).Truncate(time.Second), Subjects: subjects}
	clients := []managedSessionClient{}
	var frozen environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &frozen); err != nil {
		t.Fatal(err)
	}
	recovery := createManagedSessionRecovery{PartialInventory: true, OriginalState: &oldState, OriginalClientDigests: partialSessionClientDigests(clients), OriginalSession: old, OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: environmentPreflightBytesSHA256(originalCert), SessionSHA256: deploymentplan.Digest(oldState), ClientsSHA256: deploymentplan.Digest(clients), SourceStage: "kubernetes/service-session", SourcePlanSHA256: source.SHA256(), SourceJournalSHA256: deploymentplan.Digest(sourceJournal), BindingSHA256: firstBinding, CandidateSHA256: first.SHA256()}
	proof := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(frozen), ParentOwner: deploymentplan.Digest(e.owner), Release: deploymentplan.Digest(plan.Release), PartialSession: &partialSessionContinuationProof{SourceJournal: frozen, Recovery: recovery, Intent: intent, RenewedIntent: firstIntent, KeySHA: environmentPreflightBytesSHA256(keyPEM), CSRSHA: environmentPreflightBytesSHA256(csr), Candidate: first}}
	review := deploymentplan.Digest(proof)
	proof.QualifiedAt, proof.ValidUntil = now.Add(-time.Second), now.Add(time.Hour-time.Second)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"), mustProviderRecoveryJSON(proof), false); err != nil {
		t.Fatal(err)
	}
	if err := beginPartialSessionAdmission(store, e, proof, review, now); err != nil {
		t.Fatal(err)
	}
	run("service-session-original-key-renewal-"+review, first)
	service.BootstrapSessionID = firstIntent.SessionID
	custody := renderEnvironmentCreateCustodyReader(cfg, plan.Bootstrap, service)
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(custody.Object)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + firstBinding[:12] + "/bootstrap.crt"}))
	run("service-custody-reader-renewal-"+review, makePlan("custody", []deploymentplan.Definition{custody}))
	credential := environmentCreateSecret(ns, "service-bootstrap-"+firstIntent.SessionID, map[string]string{"tls.key": string(keyPEM), "tls.crt": string(firstCert), "ca.crt": rootPEM})
	credential.Object["immutable"] = true
	run("service-credential-secrets-renewal-"+review, makePlan("credential", []deploymentplan.Definition{credential}))
	// The native requalification reads this original, completed transport proof.
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-create-openbao-tls-consumer-trust"}, "data": deploymentplan.Object{"roots.pem": rootPEM}}
	run("fixture-consumer-trust", makePlan("fixture-consumer-trust", []deploymentplan.Definition{{Phase: "configuration", Object: trust}}))
	recordServiceSessionNativeMintTransport(t, cfg, store, e, backend.objects[deploymentplan.ID(trust)], now)
	host := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": plan.Stack + "-secrets"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao-pki", "env": environmentCreateEnv(map[string]string{"OPENBAO_PKI_HOST_RENEWAL_TLS_CA": "/run/pki-service/root.pem", "PKI_BOOTSTRAP_SESSION_ID": old}), "volumeMounts": []any{deploymentplan.Object{"name": "trust", "mountPath": "/run/pki-service", "readOnly": true}}}}, "volumes": []any{deploymentplan.Object{"name": "trust", "configMap": deploymentplan.Object{"name": "service-trust"}}}}}}}
	hostTrust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "service-trust", "namespace": plan.Stack + "-secrets"}, "data": deploymentplan.Object{"root.pem": rootPEM}}
	run("fixture-host", makePlan("fixture-host", []deploymentplan.Definition{{Phase: "workload", Object: host}}))
	run("fixture-host-trust", makePlan("fixture-host-trust", []deploymentplan.Definition{{Phase: "configuration", Object: hostTrust}}))
	for id, object := range base.read(t).Objects {
		if object["kind"] == "Namespace" {
			object = deploymentplan.Clone(object)
			delete(deploymentplan.Map(object["metadata"]), "labels")
			backend.objects[id] = object
		}
	}
	isolated, err := filepath.Rel(os.TempDir(), store.Root)
	if err != nil || isolated == "." || isolated == ".." || strings.HasPrefix(isolated, ".."+string(filepath.Separator)) || cfg.Workspace == "" || !strings.HasPrefix(cfg.Workspace, os.TempDir()+string(filepath.Separator)) {
		t.Fatal("native Mint fixture requires its isolated temporary store and maintained workspace")
	}
	native, dir, calls := unifiedSessionNativeBackend(t, store, backend.objects, nil)
	native.RolloutTimeout = time.Second
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: native}
	material := map[string][]byte{"/run/pki-bootstrap/bootstrap.key": keyPEM, "/run/pki-bootstrap/bootstrap.csr": csr, "/run/pki-bootstrap/bootstrap.crt": originalCert, "/run/pki-bootstrap/renew-" + firstBinding[:12] + "/bootstrap.crt": firstCert}
	custodyFiles := installServiceSessionNativeMintProtocol(t, dir, cfg, service, map[string]managedSessionState{old: oldState, firstIntent.SessionID: firstState}, material)
	rootToken := filepath.Join(t.TempDir(), "root-token")
	if err := os.WriteFile(rootToken, []byte("isolated-root-token"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.OpenBao.RootTokenPath = rootToken
	renewal, err := prepareSecondSessionCandidate(ctx, cfg, store, e, native, plan, service, service.Root, time.Now().UTC())
	if err != nil {
		t.Fatal("native requalification", err)
	}
	var snapshot environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &snapshot); err != nil {
		t.Fatal(err)
	}
	qualifiedAt := time.Now().UTC()
	q := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), SourceJournal: snapshot, SourceJournalSHA256: deploymentplan.Digest(snapshot), QualifiedAt: qualifiedAt, ExecutionDeadline: qualifiedAt.Add(time.Hour), Renewal: *renewal}
	for _, id := range []string{old, firstIntent.SessionID} {
		batch := serviceSessionBatch{Previous: id, Stage: "service-session-forward-" + deploymentplan.Digest([]string{id, renewal.RenewedIntent.SessionID})[:16]}
		if id == old {
			batch.Pending = []serviceSessionPendingOwner{{ID: deploymentplan.ID(host), Reason: "non-deployment"}}
		}
		q.Batches = append(q.Batches, batch)
	}
	q.Reference, err = prepareServiceSessionReference(ctx, store, e, api, old, firstIntent.SessionID, renewal.RenewedIntent.SessionID)
	if err != nil {
		t.Fatal("native reference", err)
	}
	if err := saveSecondSessionAdmission(store, e, q); err != nil {
		t.Fatal(err)
	}
	secondReview := deploymentplan.Digest(q)
	if err := e.BeginEntry("service-session-batches-"+secondReview, "service-session-batches", serviceSessionBatchIntent(secondReview, q), qualifiedAt); err != nil {
		t.Fatal(err)
	}
	return serviceSessionNativeMintFixture{cfg: cfg, store: store, e: e, parent: plan, native: native, runtime: runtime, service: service, q: q, review: secondReview, calls: calls, nativeDir: dir, custody: custodyFiles}
}
