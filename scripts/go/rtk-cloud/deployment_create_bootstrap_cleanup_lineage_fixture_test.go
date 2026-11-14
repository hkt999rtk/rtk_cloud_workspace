package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newBootstrapCleanupLineageFixture(t *testing.T, productGrowth ...bool) serviceSessionNativeMintFixture {
	t.Helper()
	return newBootstrapCleanupLineageFixtureWithLifetime(t, time.Hour, productGrowth...)
}

func newBootstrapCleanupLineageFixtureWithLifetime(t *testing.T, currentLifetime time.Duration, productGrowth ...bool) serviceSessionNativeMintFixture {
	t.Helper()
	ctx := t.Context()
	base := newNativeCreateStageContract(t, "normal")
	cfg, store, plan := base.cfg, base.store, base.parent
	image := plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"]
	subjects := []string{"service:fixture"}
	installed := subjects
	if len(productGrowth) == 1 && productGrowth[0] {
		subjects = nil
		for i := 0; i < 14; i++ {
			subjects = append(subjects, fmt.Sprintf("service:fixture-%02d", i))
		}
		installed = subjects[:9]
	}
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
	// One genuinely signed predecessor client is installed. Cleanup must
	// authenticate its admitted ACK history rather than treating every seed
	// as unused or relying on the separate legacy five-ACK exception.
	var clients, products []managedSessionClient
	for i, subject := range subjects {
		clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		clientCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, clientKey)
		if err != nil {
			t.Fatal(err)
		}
		clientDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(int64(200 + i)), Subject: pkix.Name{CommonName: subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, issuer, &clientKey.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		row := managedSessionClient{Subject: subject, Caller: "service:deployment-bootstrap", Request: fmt.Sprintf("fixture-preserved-client-%d", i), Issuer: service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(clientDER), Status: "succeeded", CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: clientCSR})), Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}))}
		if i < len(installed) {
			clients = append(clients, row)
		} else {
			products = append(products, row)
		}
	}
	firstState.Status, firstState.Installed = "active", installed
	originalCredential := environmentCreateSecret(ns, "service-bootstrap-"+old, map[string]string{"tls.key": string(keyPEM), "tls.crt": string(originalCert), "ca.crt": rootPEM})
	originalCredential.Object["immutable"] = true
	deploymentplan.Map(originalCredential.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": old}
	run("service-credential-secrets", makePlan("original-credential", []deploymentplan.Definition{originalCredential}))
	namespaceUID := "11111111-1111-4111-8111-111111111111"
	binding := environmentBootstrapCleanupBinding{Version: 1, Environment: cfg.Environment, SessionID: old, NamespaceUID: namespaceUID, SecretUID: deploymentplan.String(deploymentplan.Map(backend.objects[deploymentplan.ID(originalCredential.Object)]["metadata"])["uid"]), PolicySHA256: deploymentplan.Digest("fixture-policy"), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	originalObserved := createServiceSessionObserved{Cleanup: binding, UIDs: map[string]string{ns: binding.SecretUID}, Reader: service.CustodyReader, Session: mustProviderRecoveryJSON(oldState)}
	if err := store.write(filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-observed.json"), mustProviderRecoveryJSON(originalObserved), false); err != nil {
		t.Fatal(err)
	}
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
	deploymentplan.Map(credential.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": firstIntent.SessionID}
	run("service-credential-secrets-renewal-"+review, makePlan("credential", []deploymentplan.Definition{credential}))
	binding.SessionID, binding.SecretUID = firstIntent.SessionID, deploymentplan.String(deploymentplan.Map(backend.objects[deploymentplan.ID(credential.Object)]["metadata"])["uid"])
	firstObserved := createServiceSessionObserved{Cleanup: binding, UIDs: map[string]string{ns: binding.SecretUID}, Reader: service.CustodyReader, Session: mustProviderRecoveryJSON(firstState)}
	if err := store.write(filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json"), mustProviderRecoveryJSON(firstObserved), false); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(filepath.Join(dir, "cleanup-clients.json"), mustProviderRecoveryJSON(clients), 0600); err != nil {
		t.Fatal(err)
	}
	if len(products) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "cleanup-product-clients.json"), mustProviderRecoveryJSON(products), 0600); err != nil {
			t.Fatal(err)
		}
	}
	protocolPath := filepath.Join(dir, "unified-kubectl.py")
	protocolRaw, err := os.ReadFile(protocolPath)
	if err != nil {
		t.Fatal(err)
	}
	protocolScript := strings.Replace(string(protocolRaw), "                print('[]')", "                print(json.dumps(load(os.path.join(root, 'cleanup-clients.json'))))", 1)
	if err := os.WriteFile(protocolPath, []byte(protocolScript), 0600); err != nil {
		t.Fatal(err)
	}
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

	currentCert, currentDER := certificate(101, now.Add(currentLifetime))
	service.BootstrapSessionID, service.BootstrapCredentialSecret = renewal.RenewedIntent.SessionID, "service-bootstrap-"+renewal.RenewedIntent.SessionID
	currentCredential := environmentCreateSecret(ns, service.BootstrapCredentialSecret, map[string]string{"tls.key": string(keyPEM), "tls.crt": string(currentCert), "ca.crt": rootPEM})
	currentCredential.Object["immutable"] = true
	deploymentplan.Map(currentCredential.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": service.BootstrapSessionID}
	run("service-session-original-key-renewal-"+secondReview, renewal.Candidate)
	run("service-credential-secrets-renewal-"+secondReview, makePlan("current-credential", []deploymentplan.Definition{currentCredential}))
	if len(productGrowth) == 1 && productGrowth[0] {
		currentReader := deploymentplan.Clone(custody.Object)
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(currentReader)["containers"])[0])
		c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": service.BootstrapSessionID, "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + renewal.BindingSHA[:12] + "/bootstrap.crt"}))
		defs := []deploymentplan.Definition{{Phase: "workload", Object: currentReader}}
		input := first.Inputs
		input.BindingsSHA256 = environmentCreateStageBindingsSHA("current-custody", defs)
		child, err := deploymentplan.Compile(input, defs, []deploymentplan.Object{backend.objects[deploymentplan.ID(currentReader)]})
		if err != nil {
			t.Fatal(err)
		}
		run("service-custody-reader-renewal-"+secondReview, child)
	}
	service.CredentialUIDs = map[string]string{ns: deploymentplan.String(deploymentplan.Map(backend.objects[deploymentplan.ID(currentCredential.Object)]["metadata"])["uid"])}
	service.CleanupBinding = binding
	service.CleanupBinding.SessionID, service.CleanupBinding.SecretUID = service.BootstrapSessionID, service.CredentialUIDs[ns]
	currentState := managedSessionState{Session: service.BootstrapSessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(currentDER), Status: "active", Expires: now.Add(currentLifetime).Truncate(time.Second), Subjects: subjects, Installed: installed}
	currentObserved := createServiceSessionObserved{Cleanup: service.CleanupBinding, UIDs: service.CredentialUIDs, Reader: service.CustodyReader, Session: mustProviderRecoveryJSON(currentState)}
	if err := store.write(filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", secondReview, "observed.json"), mustProviderRecoveryJSON(currentObserved), false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{renewal.SourceJobID, renewal.PreviousJobID} {
		backend.objects[id]["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	}
	if err := beginSessionLineage(store, e, q, secondReview, qualifiedAt); err != nil {
		t.Fatal(err)
	}
	execution := serviceSessionBatchExecution{Backend: api, Ready: func(context.Context) error { return nil }, VerifyHistory: func(context.Context) error { return nil }, Mint: func(context.Context) error { return nil }, Pending: func(ctx context.Context, _ serviceSessionBatch) error {
		return executeServiceSessionReference(ctx, store, e, api, q, io.Discard)
	}, Accept: func(context.Context, *deploymentplan.Plan) error { return nil }, VerifyACKs: func(context.Context) (managedSessionState, error) { return currentState, nil }}
	bounded, cancel := context.WithDeadline(ctx, q.ExecutionDeadline)
	defer cancel()
	if err := executeServiceSessionBatches(bounded, store, e, secondReview, execution, io.Discard); err != nil {
		t.Fatal("completed historical source batch", err)
	}
	for id, object := range backend.objects {
		deploymentplan.Map(object["metadata"])["resourceVersion"] = "1"
		if err := os.WriteFile(filepath.Join(dir, strings.ReplaceAll(id, "/", "_")+".json"), mustProviderRecoveryJSON(object), 0600); err != nil {
			t.Fatal(err)
		}
	}
	material["/run/pki-bootstrap/renew-"+renewal.BindingSHA[:12]+"/bootstrap.crt"] = currentCert
	installBootstrapCleanupNativeProtocol(t, dir, material)
	firstState.Status = "expired" // Native expiry advances the admitted active-but-expired predecessor.
	if len(productGrowth) == 1 && productGrowth[0] {
		// A live registry counts the one current active successor for both
		// historical sessions; their immutable admitted snapshots stay unchanged.
		oldState.OtherActive, firstState.OtherActive = 1, 1
	}
	if err := os.WriteFile(filepath.Join(dir, "mint-states.json"), mustProviderRecoveryJSON(map[string]managedSessionState{old: oldState, firstIntent.SessionID: firstState, currentState.Session: currentState}), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryRuntime, err := restoreSecondSessionRuntime(ctx, store, e, q, secondReview, service, service.Root, plan)
	if err != nil {
		t.Fatal(err)
	}
	runtime.ServiceSessionRecovery = recoveryRuntime
	return serviceSessionNativeMintFixture{cfg: cfg, store: store, e: e, parent: plan, native: native, runtime: runtime, service: service, q: q, review: secondReview, calls: calls, nativeDir: dir, custody: custodyFiles}
}

// Only the native custody shell and UID-preconditioned Kubernetes DELETE are
// modeled here. The shell executes against a private temporary directory, so
// hard links, interruption points and byte-preserving retries are real.
func installBootstrapCleanupNativeProtocol(t *testing.T, dir string, material map[string][]byte) {
	t.Helper()
	custody := filepath.Join(dir, "cleanup-custody")
	if err := os.MkdirAll(custody, 0700); err != nil {
		t.Fatal(err)
	}
	for path, raw := range material {
		relative := strings.TrimPrefix(path, "/run/pki-bootstrap/")
		file := filepath.Join(custody, relative)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "unified-kubectl.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := "import subprocess\n" + string(raw)
	script = strings.Replace(script, "                print(json.dumps(states[ids[-1]]))", "                state=dict(states[ids[-1]])\n                state['installed']=state.get('installed_subjects',[])\n                fields=re.findall(r\"'([a-z0-9_]+)',\",query)\n                print(json.dumps({key:state.get(key) for key in fields}))", 1)
	original := `        elif 'custody' in a and a[-2] == 'cat':
            files = load(os.path.join(root, 'mint-custody.json'))
            if a[-1] not in files: fail('unknown original fixture custody path')
            with open(files[a[-1]]) as f: sys.stdout.write(f.read())
`
	replacement := `        elif 'custody' in a and a[-2] == 'cat':
            if not a[-1].startswith('/run/pki-bootstrap/') or '..' in a[-1]: fail('foreign fixture custody path')
            with open(os.path.join(root, 'cleanup-custody', a[-1][len('/run/pki-bootstrap/'):])) as f: sys.stdout.write(f.read())
        elif 'custody' in a and '/bin/sh' in a:
            shell = a[-1]
            if '/run/pki-bootstrap' not in shell: fail('foreign fixture custody script')
            shell = shell.replace('/run/pki-bootstrap', os.path.join(root, 'cleanup-custody'))
            stop = os.path.join(root, 'cleanup-stop-after-archive')
            if os.path.exists(stop):
                target = ' && rm ' + os.path.join(root, 'cleanup-custody', 'bootstrap.crt')
                if target not in shell: fail('archive interruption boundary changed')
                shell = shell.replace(target, ' && exit 79' + target, 1)
                os.unlink(stop)
            stop = os.path.join(root, 'cleanup-stop-after-unlink')
            if os.path.exists(stop):
                target = ' && rm ' + os.path.join(root, 'cleanup-custody', 'bootstrap.crt')
                if target not in shell: fail('unlink interruption boundary changed')
                shell = shell.replace(target, target + ' && exit 79', 1)
                os.unlink(stop)
            result = subprocess.run(['/bin/sh','-ec',shell],capture_output=True)
            if result.returncode:
                sys.stderr.write(result.stderr.decode())
                fail('isolated custody shell interrupted or rejected')
`
	if strings.Count(script, original) != 1 {
		t.Fatal("original custody protocol changed")
	}
	script = strings.Replace(script, original, replacement, 1)
	marker := "    elif a[0] == 'delete' and len(a) == 4 and a[1].startswith('--raw=') and a[2:] == ['-f', '-']:\n"
	deletes := `    elif a[0] == 'delete' and len(a) == 4 and a[1].startswith('--raw=/api/v1/namespaces/') and a[2:] == ['-f','-']:
        parts = a[1][len('--raw=/api/v1/namespaces/'):].split('/')
        if len(parts)!=3 or parts[1]!='secrets': fail('foreign fixture cleanup target')
        file = path('Secret',parts[0],parts[2]); old=load(file); options=json.load(sys.stdin)
        if old is None: fail('NotFound')
        pre=options.get('preconditions',{})
        if pre.get('uid')!=old['metadata'].get('uid') or pre.get('resourceVersion')!=old['metadata'].get('resourceVersion'): fail('Conflict')
        os.unlink(file); print('{}')
`
	if strings.Count(script, marker) != 1 {
		t.Fatal("original native DELETE protocol changed")
	}
	script = strings.Replace(script, marker, deletes+marker, 1)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
}

func installBootstrapCleanupFinalizer(t *testing.T, f serviceSessionNativeMintFixture) (*environmentCreatePKIClient, environmentCreateBootstrapFinalization, *int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.nativeDir, "cleanup-custody", strings.TrimPrefix(f.runtime.ServiceSessionRecovery.RenewedCertPath, "/run/pki-bootstrap/")))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := parseFirstCertificatePEM(raw)
	if err != nil {
		t.Fatal(err)
	}
	final := environmentCreateBootstrapFinalization{Environment: f.cfg.Environment, SessionID: f.service.BootstrapSessionID, LeafSHA256: environmentPreflightBytesSHA256(leaf.Raw), CRLSHA256: deploymentplan.Digest("isolated-final-crl"), CleanupSHA256: deploymentplan.Digest(f.service.CleanupBinding), FinalizedAt: time.Now().UTC().Truncate(time.Microsecond)}
	file := filepath.Join(f.nativeDir, "cleanup-native-final.json")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/platform/pki/deployment-bootstrap/"+f.service.BootstrapSessionID+"/finalize" || r.Header.Get("Authorization") != "Bearer isolated-cleanup-token" {
			w.WriteHeader(403)
			return
		}
		calls++
		if calls == 1 {
			final.FinalizedAt = time.Now().UTC().Truncate(time.Microsecond)
		}
		var states map[string]managedSessionState
		path := filepath.Join(f.nativeDir, "mint-states.json")
		if readExecutionJSON(path, &states) != nil {
			w.WriteHeader(500)
			return
		}
		state := states[final.SessionID]
		state.Status = "sealed"
		states[final.SessionID] = state
		if os.WriteFile(path, mustProviderRecoveryJSON(states), 0600) != nil || os.WriteFile(file, mustProviderRecoveryJSON(final), 0600) != nil {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(final)
	}))
	t.Cleanup(server.Close)
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), f.cfg.Environment, "77777777-7777-4777-8777-777777777777")
	if err != nil {
		t.Fatal(err)
	}
	client.Execution, client.token, client.authenticatedAt = f.e, "isolated-cleanup-token", time.Now()
	path := filepath.Join(f.nativeDir, "unified-kubectl.py")
	scriptRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := "import hashlib\n" + string(scriptRaw)
	projection := "                print(json.dumps({key:state.get(key) for key in fields}))"
	script = strings.Replace(script, projection, "                if 'finalized_at IS NOT NULL' in query:\n                    native=dict(load(os.path.join(root,'cleanup-native-final.json')))\n                    native['finalized_at']=native['finalized_at'].replace('Z','+00:00')\n                    print(json.dumps(native))\n                else: print(json.dumps({key:state.get(key) for key in fields}))", 1)
	marker := "        elif 'custody' in a and a[-2] == 'cat':\n"
	cleanup := `        elif 'custody' in a and 'cleanup-credentials' in a:
            final=load(os.path.join(root,'cleanup-native-final.json'))
            if not final: fail('native finalization not established')
            base=os.path.join(root,'cleanup-custody'); marker=os.path.join(base,'bootstrap.finalized.json')
            if not os.path.exists(marker):
                hashes={}
                for name in ['bootstrap.key','bootstrap.csr','bootstrap.crt']:
                    with open(os.path.join(base,name),'rb') as f: hashes[name]=hashlib.sha256(f.read()).hexdigest()
                state=load(os.path.join(root,'mint-states.json'))[final['session_id']]
                # Match the native Go cleanup receipt, including struct field
                # order, sorted map keys, compact encoding and trailing LF.
                fields=['environment','session_id','bootstrap_leaf_sha256','crl_sha256','cleanup_sha256','finalized_at']
                final={key:final[key] for key in fields}
                tombstone={'schema':'rtk-cloud.bootstrap-credentials/v1','finalization':final,'root_sha256':state['root_sha256'],'file_sha256':dict(sorted(hashes.items()))}
                with open(marker,'w') as f: f.write(json.dumps(tombstone,separators=(',',':'))+'\n')
                os.chmod(marker,0o600)
            for name in ['bootstrap.key','bootstrap.csr','bootstrap.crt']:
                target=os.path.join(base,name)
                if os.path.exists(target): os.unlink(target)
            protocol=load(os.path.join(root,'mint-protocol.json'))
            target=path('Secret',protocol['namespace'],'service-bootstrap-'+final['session_id'])
            if os.path.exists(target): os.unlink(target)
            crash=os.path.join(root,'cleanup-fail-after-native')
            if os.path.exists(crash): os.unlink(crash); fail('isolated lost response after native cleanup')
`
	if strings.Count(script, marker) != 1 {
		t.Fatal("cleanup exec boundary changed")
	}
	script = strings.Replace(script, marker, cleanup+marker, 1)
	old := "        os.unlink(file); print('{}')\n"
	inject := `        before=os.path.join(root,'cleanup-fail-before-delete')
        after=os.path.join(root,'cleanup-fail-after-delete')
        if os.path.exists(before): os.unlink(before); fail('isolated interruption before ancestor DELETE')
        os.unlink(file)
        if os.path.exists(after): os.unlink(after); fail('isolated lost response after ancestor DELETE')
        print('{}')
`
	if strings.Count(script, old) != 1 {
		t.Fatal("cleanup DELETE boundary changed")
	}
	script = strings.Replace(script, old, inject, 1)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return client, final, &calls
}
