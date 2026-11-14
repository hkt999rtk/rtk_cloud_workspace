package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Test the real parent and child journals and admission with an isolated
// Kubernetes/native registry adapter. No credential is generated outside t.TempDir.
func TestSecondSessionCandidateNativeJournalAndFiveOfFourteen(t *testing.T) {
	runSecondSessionCandidateNativeJournal(t, false)
}

func TestUnifiedSessionSameParentNativeBackend(t *testing.T) {
	runSecondSessionCandidateNativeJournal(t, true)
}

func runSecondSessionCandidateNativeJournal(t *testing.T, unified bool, currentCore ...*currentCoreForwardTestScenario) {
	t.Helper()
	ctx := t.Context()
	digest := strings.Repeat("a", 64)
	image := environmentReleaseImage{Reference: "ghcr.io/example/api@sha256:" + digest}
	var store secretStore
	var execution *environmentCreateExecution
	var backend freshStageBackend
	var parent unifiedOpenBaoParentFixture
	var api deploymentplan.Backend
	var cfg deploymentConfig
	var plan environmentCreatePlan
	var joined *freshProductTerminalJoinedScenario
	if len(currentCore) == 1 {
		joined = currentCore[0].joined
	}
	if joined != nil {
		image = joined.apiImage
	}
	if unified {
		if joined != nil {
			parent = newUnifiedOpenBaoParentFixtureWithAdmission(t, image, nil, func(cfg *deploymentConfig) { currentCoreMaintainedWrapperPolicy(t, cfg) }, &joined.admission, true)
		} else if len(currentCore) == 1 {
			parent = newUnifiedOpenBaoParentFixtureWithConfiguration(t, image, nil, func(cfg *deploymentConfig) { currentCoreMaintainedWrapperPolicy(t, cfg) }, true)
		} else {
			parent = newUnifiedOpenBaoParentFixture(t, image, true)
		}
		store, execution, cfg, plan = parent.store, parent.execution, parent.cfg, parent.plan
		if joined != nil {
			fresh, resolveErr := resolveDeploymentConfig(cfg.Workspace, cfg.Environment, cfg.EnvironmentRoot)
			if resolveErr != nil || deploymentplan.Digest(fresh) != plan.ConfigurationSHA256 {
				t.Fatal("joined admitted maintained configuration changed", resolveErr)
			}
		}
		api = parent.fault
	} else {
		store, execution, _, backend = freshStageFixture(t)
		cfg = deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": execution.Journal.Stack}}
		plan = environmentCreatePlan{Environment: "dev", Stack: execution.Journal.Stack, ConfigurationSHA256: digest, Bootstrap: environmentCreateBootstrapPlan{Image: image.Reference}, Release: environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": image}}}
		api = initializerDefaultBackend{backend}
	}
	now := time.Now().UTC()
	namespace := execution.Journal.Stack + "-video-cloud"
	get := func(id string) deploymentplan.Object {
		object, err := api.Read(ctx, id)
		if err != nil || object == nil {
			t.Fatalf("isolated object %s: %v", id, err)
		}
		return object
	}
	put := func(id string, object deploymentplan.Object) {
		if !unified {
			backend.objects[id] = deploymentplan.Clone(object)
			return
		}
		parts := strings.Split(id, "/")
		if len(parts) != 3 {
			t.Fatal("invalid isolated resource identity")
		}
		if err := os.WriteFile(filepath.Join(parent.nativeDir, strings.Join(parts, "_")+".json"), mustProviderRecoveryJSON(object), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if len(currentCore) == 1 {
		for i, ns := range currentCore[0].namespaces {
			id := "Namespace//" + ns
			if actual, err := api.Read(ctx, id); err != nil {
				t.Fatal(err)
			} else if actual == nil {
				put(id, deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns, "uid": fmt.Sprintf("%08d-aaaa-4aaa-8aaa-111111111111", i+100), "labels": deploymentplan.Object{"kubernetes.io/metadata.name": ns}}})
			}
		}
	}
	inventory := func(ctx context.Context, stack string) ([]deploymentplan.Object, error) {
		if unified {
			return parent.native.Inventory(ctx, stack)
		}
		var items []deploymentplan.Object
		for _, object := range backend.objects {
			items = append(items, deploymentplan.Clone(object))
		}
		return items, nil
	}
	snapshot := func() map[string]deploymentplan.Object {
		items, err := inventory(ctx, execution.Journal.Stack)
		if err != nil {
			t.Fatal(err)
		}
		objects := make(map[string]deploymentplan.Object, len(items))
		for _, object := range items {
			objects[deploymentplan.ID(object)] = deploymentplan.Clone(object)
		}
		return objects
	}
	syncChanges := func(changed, previous map[string]deploymentplan.Object) {
		for id, object := range changed {
			if deploymentplan.Digest(object) != deploymentplan.Digest(previous[id]) {
				put(id, object)
			}
		}
	}
	setFailure := func(phase string) {
		if unified {
			parent.fault.failAfterPhase, parent.fault.failed = phase, false
			return
		}
		backend.failure, backend.failed = "", false
		if phase != "" {
			backend.failure = "after-write-" + phase
		}
	}
	writes := func() map[string]int {
		if unified {
			return parent.fault.writes
		}
		return backend.writes
	}
	root, rootSigner, rootPEM := coreContractCertificate(t, "fixture root", true, nil, nil, 2*365*24*time.Hour)
	issuer, signer, issuerPEM := coreContractCertificate(t, "fixture issuer", true, root, rootSigner, 365*24*time.Hour)
	service := environmentCreateServiceCeremony{Root: environmentCreateRegisteredIssuer{ID: "11111111-2222-4333-8444-555555555555", Certificate: rootPEM, RootFingerprint: environmentPreflightBytesSHA256(root.Raw)}, Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222", Certificate: issuerPEM}, BootstrapCredentialNamespace: namespace, BootstrapCredentialNamespaces: []string{namespace}, CustodyReader: "fixture-custody", BootstrapClaim: "fixture-pki-state"}
	if len(currentCore) == 1 {
		currentCore[0].registeredService(t, &service)
		service.CleanupBinding = currentCore[0].cleanup
		service.CleanupBinding.SessionID = "33333333-3333-4333-8333-333333333333"
		if joined != nil {
			selected, selectedErr := newEnvironmentKubernetesBackend(string(joined.admission.kubeconfig), cfg.Environment)
			if selectedErr != nil {
				t.Fatal("known synthetic selected Kubernetes identity", selectedErr)
			}
			parent.native.KubeconfigContent = slices.Clone(selected.KubeconfigContent)
			var configuration struct {
				Clusters []struct {
					Cluster struct {
						CA string `yaml:"certificate-authority-data"`
					} `yaml:"cluster"`
				} `yaml:"clusters"`
			}
			if yaml.Unmarshal(parent.native.KubeconfigContent, &configuration) != nil || len(configuration.Clusters) != 1 || configuration.Clusters[0].Cluster.CA == "" {
				t.Fatal("joined constructor's known canonical Kubernetes CA required")
			}
			service.CleanupBinding.KubernetesCA = configuration.Clusters[0].Cluster.CA
		}
		if object, err := api.Read(ctx, "Namespace//"+namespace); err == nil && object != nil {
			service.CleanupBinding.NamespaceUID = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
		}
	}
	sessionOpenBaoRoot := service.Root
	if joined != nil {
		sessionOpenBaoRoot = currentCore[0].roots["openbao_tls"]
		service.CustodyReader = "pki-bootstrap-custody-" + execution.Journal.PlanSHA256[:12]
		// This original generation's public CRL is signed by the same actual
		// Root before any source admission or consumer recipe is frozen.
		crlDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}, root, rootSigner)
		if err != nil {
			t.Fatal("joined original native Root CRL", err)
		}
		service.RootCRLSecret = "pki-create-service-root-crl"
		service.RootCRLPEM = string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crlDER}))
		currentCore[0].rootCRLSecret, currentCore[0].rootCRL = service.RootCRLSecret, service.RootCRLPEM
	}
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
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), []byte(issuerPEM+rootPEM)...)
	originalTemplate := *leafTemplate
	originalTemplate.SerialNumber = big.NewInt(99)
	originalTemplate.NotAfter = now.Add(-30 * time.Minute)
	originalDER, err := x509.CreateCertificate(rand.Reader, &originalTemplate, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	originalCert := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: originalDER}), []byte(issuerPEM+rootPEM)...)
	var subjects []string
	for i := 0; i < 13; i++ {
		subjects = append(subjects, fmt.Sprintf("service:fixture-%02d", i))
	}
	subjects = append(subjects, "service:openbao")
	slices.Sort(subjects)
	installedSubjects := append(slices.Clone(subjects[:4]), "service:openbao")
	slices.Sort(installedSubjects)
	if joined != nil {
		var rosterErr error
		subjects, rosterErr = environmentCreatePlannedServiceSubjects(cfg, plan.Bootstrap)
		if rosterErr != nil || len(subjects) != 14 {
			t.Fatal("joined admitted fourteen-subject policy", rosterErr)
		}
		installedSubjects, rosterErr = environmentCreateBootstrapSubjects(cfg)
		if rosterErr != nil || len(installedSubjects) != 9 {
			t.Fatal("joined original nine-subject policy", rosterErr)
		}
	}
	var clients []managedSessionClient
	var clientKeys []string
	for i, subject := range installedSubjects {
		clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, clientKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(int64(i + 1)), Subject: pkix.Name{CommonName: subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, issuer, &clientKey.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, managedSessionClient{Subject: subject, Caller: "service:deployment-bootstrap", Request: fmt.Sprintf("request-%d", i), Issuer: service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leaf), Status: "succeeded", CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: request})), Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf}))})
		encodedKey, err := x509.MarshalPKCS8PrivateKey(clientKey)
		if err != nil {
			t.Fatal(err)
		}
		clientKeys = append(clientKeys, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})))
	}
	if len(clientKeys) != len(clients) {
		t.Fatal("synthetic client key fixture is incomplete")
	}
	old := "33333333-3333-4333-8333-333333333333"
	intent := createServiceSessionIntent{SessionID: old, Claim: service.BootstrapClaim, Subjects: subjects, IssuerID: service.Intermediate.ID, RootSHA: service.Root.RootFingerprint, Namespaces: []string{namespace}}
	env := map[string]string{"PKI_BOOTSTRAP_SESSION_ID": old, "PKI_BOOTSTRAP_SUBJECTS": strings.Join(subjects, ","), "PKI_BOOTSTRAP_ISSUER_ID": service.Intermediate.ID, "PKI_BOOTSTRAP_ROOT_SHA256": service.Root.RootFingerprint, "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt", "OPENBAO_ADDR": "https://openbao." + plan.Stack + "-secrets.svc:8200", "OPENBAO_CACERT": "/run/openbao-ca/ca.crt"}
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": namespace, "name": "fixture-original"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": image.Reference, "env": environmentCreateEnv(env), "volumeMounts": []any{deploymentplan.Object{"name": "openbao-ca", "mountPath": "/run/openbao-ca", "readOnly": true}}}}, "initContainers": []any{deploymentplan.Object{"name": "permissions"}}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": service.BootstrapClaim}}, deploymentplan.Object{"name": "openbao-ca", "configMap": deploymentplan.Object{"name": "pki-openbao-transport-ca"}}}}}}}
	if joined != nil {
		initializers := certIssuerObjectList(environmentPKIPod(job)["initContainers"])
		if len(initializers) != 1 {
			t.Fatal("joined original bootstrap initializer")
		}
		deploymentplan.Map(initializers[0])["image"] = image.Reference
	}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: plan.Stack, Operation: "scoped", ConfigurationSHA256: plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(plan.Release)}
	makePlan := func(stage string, definitions []deploymentplan.Definition) *deploymentplan.Plan {
		t.Helper()
		if joined != nil {
			auditJoinedNativeContainerImages(t, definitions)
		}
		in := inputs
		in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, definitions)
		result, err := deploymentplan.Compile(in, definitions, nil)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	run := func(stage string, child *deploymentplan.Plan) {
		t.Helper()
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return execution.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(ctx, store, execution, stage, child, api, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(stage, err)
		}
	}
	if joined != nil {
		rootCRL := environmentCreateSecret(namespace, service.RootCRLSecret, map[string]string{"revocations.pem": service.RootCRLPEM})
		run("service-active-trust", makePlan("service-active-trust", []deploymentplan.Definition{rootCRL}))
		original := service
		original.BootstrapSessionID, original.BootstrapCredentialSecret = old, "service-bootstrap-"+old
		if err := validateEnvironmentCreateServiceCeremony(cfg, plan.Bootstrap, currentCore[0].runtime, original); err != nil {
			t.Fatal("original joined service enrollment prerequisites", err)
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
	firstIntent.SessionID = sessionRenewalUUID(execution.Journal.PlanSHA256, firstBinding, old)
	firstDefinition, err := managedSessionRenewalDefinition(job, execution.Journal.PlanSHA256, firstBinding, firstIntent.SessionID, image, plan.Stack)
	if err != nil {
		t.Fatal(err)
	}
	first := makePlan("first-candidate", []deploymentplan.Definition{firstDefinition})
	oldState := managedSessionState{Session: old, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(originalDER), Status: "expired", Expires: now.Add(-30 * time.Minute).Truncate(time.Second), Subjects: subjects, Installed: []string{"service:openbao"}}
	firstState := managedSessionState{Session: firstIntent.SessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(leafDER), Status: "expired", Expires: now.Add(-time.Minute), Subjects: subjects, Installed: slices.Clone(installedSubjects)}
	if joined != nil {
		originalCredential := environmentCreateSecret(namespace, "service-bootstrap-"+old, map[string]string{"tls.key": string(keyPEM), "tls.crt": string(originalCert), "ca.crt": rootPEM})
		originalCredential.Object["immutable"] = true
		deploymentplan.Map(originalCredential.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": old}
		run("service-credential-secrets", makePlan("original-credential", []deploymentplan.Definition{originalCredential}))
		joined.observeMint(t, store, execution, api, service, oldState, "")
	}
	if joined != nil {
		joined.prepareInitialTransport(t, cfg, store, execution, api, parent.native, plan, sessionOpenBaoRoot, currentCore[0], service)
	}
	var frozen environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(execution.Journal), &frozen); err != nil {
		t.Fatal(err)
	}
	recovery := createManagedSessionRecovery{PartialInventory: true, OriginalState: &oldState, OriginalClientDigests: partialSessionClientDigests(clients[len(clients)-1:]), OriginalSession: old, OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: environmentPreflightBytesSHA256(originalCert), SessionSHA256: digest, ClientsSHA256: deploymentplan.Digest(clients[len(clients)-1:]), SourceStage: "kubernetes/service-session", SourcePlanSHA256: source.SHA256(), SourceJournalSHA256: deploymentplan.Digest(sourceJournal), BindingSHA256: firstBinding, CandidateSHA256: first.SHA256()}
	proof := issuerEnrollmentContinuationProof{Parent: execution.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(frozen), ParentOwner: deploymentplan.Digest(execution.owner), Release: deploymentplan.Digest(plan.Release), PartialSession: &partialSessionContinuationProof{SourceJournal: frozen, Recovery: recovery, Intent: intent, RenewedIntent: firstIntent, KeySHA: environmentPreflightBytesSHA256(keyPEM), CSRSHA: environmentPreflightBytesSHA256(csr), Candidate: first}}
	review := deploymentplan.Digest(proof)
	qualified := proof
	qualified.QualifiedAt = now.Add(-time.Second)
	qualified.ValidUntil = qualified.QualifiedAt.Add(time.Hour)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"), mustProviderRecoveryJSON(qualified), false); err != nil {
		t.Fatal(err)
	}
	if err := beginPartialSessionAdmission(store, execution, qualified, review, now); err != nil {
		t.Fatal(err)
	}
	run("service-session-original-key-renewal-"+review, first)
	custody := deploymentplan.Definition{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": service.CustodyReader, "namespace": namespace}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "custody", "image": image.Reference}}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": intent.Claim}}}}}}}}
	if joined != nil {
		current := service
		current.BootstrapSessionID = firstIntent.SessionID
		custody = renderEnvironmentCreateCustodyReader(cfg, plan.Bootstrap, current)
	}
	readerContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(custody.Object)["containers"])[0])
	renewedReaderEnv := environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": firstIntent.SessionID, "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + firstBinding[:12] + "/bootstrap.crt"})
	if joined != nil {
		readerContainer["env"] = mergeEnvironmentNamedFields(readerContainer["env"], renewedReaderEnv)
	} else {
		readerContainer["env"] = renewedReaderEnv
	}
	run("service-custody-reader-renewal-"+review, makePlan("custody", []deploymentplan.Definition{custody}))
	credential := environmentCreateSecret(namespace, "service-bootstrap-"+firstIntent.SessionID, map[string]string{"tls.key": string(keyPEM), "tls.crt": string(cert), "ca.crt": rootPEM})
	credential.Object["immutable"] = true
	if joined != nil {
		deploymentplan.Map(credential.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": firstIntent.SessionID}
	}
	run("service-credential-secrets-renewal-"+review, makePlan("credential", []deploymentplan.Definition{credential}))
	if joined != nil {
		joined.observeMint(t, store, execution, api, service, firstState, review)
	}
	states := map[string]managedSessionState{old: oldState, firstIntent.SessionID: firstState}
	path := "/run/pki-bootstrap/renew-" + firstBinding[:12] + "/bootstrap.crt"
	certificates := map[string][]byte{path: cert}
	if joined != nil {
		certificates["/run/pki-bootstrap/bootstrap.crt"] = originalCert
	}
	reads := secondSessionReads{read: api.Read, inventory: inventory, state: func(_ context.Context, id string) (managedSessionState, error) { return states[id], nil }, clients: func(context.Context) ([]managedSessionClient, error) { return clients, nil }, custody: func(_ context.Context, got *createServiceSessionRecovery, proof *createManagedSessionRecovery) error {
		if got.OriginalKeySHA != environmentPreflightBytesSHA256(keyPEM) || got.OriginalCSRSHA != environmentPreflightBytesSHA256(csr) || proof.OriginalCertificateSHA256 != environmentPreflightBytesSHA256(originalCert) || !partialSessionCustodyValid(keyPEM, originalCert, []byte(rootPEM), csr, service, oldState, now) {
			return fmt.Errorf("original custody changed")
		}
		return nil
	}, trust: func(ctx context.Context, root environmentCreateRegisteredIssuer) (string, error) {
		if joined != nil {
			return verifyPartialSessionOpenBaoTrust(ctx, store, execution, parent.native, root)
		}
		return digest, nil
	}, command: func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
		last := args[len(args)-1]
		switch last {
		case "/run/pki-bootstrap/bootstrap.key":
			return keyPEM, nil
		case "/run/pki-bootstrap/bootstrap.csr":
			return csr, nil
		case "/run/pki-bootstrap":
			if joined != nil {
				return []byte("10001:10001\n"), nil
			}
			return []byte("12345:23456\n"), nil
		}
		if value, ok := certificates[last]; ok {
			return value, nil
		}
		return nil, fmt.Errorf("unexpected native read")
	}}
	// Construct the original 59-owner q0/q1/q2 chain under this same parent,
	// using the real on-disk partial admission rather than a synthetic proof.
	admitted, admittedReview, found, err := loadPartialSessionAdmission(store, execution)
	if err != nil || !found || admittedReview != review {
		t.Fatal("original partial admission is unavailable", err)
	}
	shared := initializerSharedSessionFixture{store: store, execution: execution, backend: backend, admitted: admitted, review: admittedReview, configurationSHA: plan.ConfigurationSHA256, releaseSHA: deploymentplan.Digest(plan.Release), sessionID: firstIntent.SessionID}
	if joined != nil {
		shared.apiImage = image.Reference
	}
	if unified {
		shared.nativeBackend, shared.nativeDir = parent.fault, parent.nativeDir
	}
	runInitializerForwardInterruptedFixture(t, true, true, shared)
	q2Boundary, found, err := serviceSessionBoundary(ctx, store, execution, api)
	if err != nil || !found {
		t.Fatal("production q2 boundary did not authenticate same parent", err)
	}
	beforeJournal, beforeWrites := deploymentplan.Digest(execution.Journal), deploymentplan.Digest(writes())
	qualify := func() (*secondSessionQualification, error) {
		if joined != nil {
			now = time.Now().UTC()
		}
		return prepareSecondSessionCandidateWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, now)
	}
	result, err := qualify()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.PreviousState.Installed) != len(installedSubjects) || len(result.PreviousState.Subjects) != 14 || result.SourcePlanSHA != source.SHA256() || result.PreviousPlanSHA != first.SHA256() || result.Intent.SessionID != firstIntent.SessionID || result.RenewedIntent.SessionID == firstIntent.SessionID || result.BindingSHA != secondSessionBinding(result) || beforeJournal != deploymentplan.Digest(execution.Journal) || beforeWrites != deploymentplan.Digest(writes()) {
		t.Fatal("qualification changed state or lost the original chain")
	}
	if joined == nil {
		for _, mode := range []string{"source-uid", "previous-uid", "credential-uid", "custody-uid", "credential-key", "issuer", "competing-session", "client-loss", "custody-csr"} {
			t.Run(mode, func(t *testing.T) {
				objects := snapshot()
				saved := make(map[string]deploymentplan.Object, len(objects))
				for id, value := range objects {
					saved[id] = deploymentplan.Clone(value)
				}
				savedStates, savedClients, savedCSR := states, clients, csr
				t.Cleanup(func() { syncChanges(saved, objects); states, clients, csr = savedStates, savedClients, savedCSR })
				switch mode {
				case "source-uid":
					deploymentplan.Map(objects[source.Resources[0].ID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				case "previous-uid":
					deploymentplan.Map(objects[first.Resources[0].ID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				case "credential-uid":
					deploymentplan.Map(objects["Secret/"+namespace+"/service-bootstrap-"+firstIntent.SessionID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				case "custody-uid":
					deploymentplan.Map(objects["Deployment/"+namespace+"/"+service.CustodyReader]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				case "credential-key":
					deploymentplan.Map(objects["Secret/"+namespace+"/service-bootstrap-"+firstIntent.SessionID]["data"])["tls.key"] = "Zm9yZWlnbg=="
				case "issuer":
					altered := firstState
					altered.Issuer = "foreign"
					states = map[string]managedSessionState{old: oldState, firstIntent.SessionID: altered}
				case "competing-session":
					altered := firstState
					altered.OtherActive = 1
					states = map[string]managedSessionState{old: oldState, firstIntent.SessionID: altered}
				case "client-loss":
					clients = clients[1:]
				case "custody-csr":
					csr = []byte("foreign")
				}
				syncChanges(objects, saved)
				_, observedError := qualify()
				if observedError == nil {
					t.Fatal("altered native evidence was accepted")
				}
				want := map[string]string{"source-uid": "source-job", "previous-uid": "predecessor-job", "credential-uid": "credential-owners", "custody-uid": "custody-owner", "credential-key": "credential-owners", "issuer": "native-client-inventory", "competing-session": "native-client-inventory", "client-loss": "native-client-inventory", "custody-csr": "original-custody"}[mode]
				if !strings.Contains(observedError.Error(), "boundary="+want) {
					t.Fatalf("native failure lost its concrete boundary %s: %v", want, observedError)
				}
			})
		}
	}
	// Admit the next candidate using a real parent journal. A process stopping
	// immediately after admission must reload the same candidate and may not
	// branch from the predecessor or mint another key.
	// Original OpenBao trust projection and public ConfigMap use real native
	// parent/child journals, with no credentials copied from a live store.
	secretsNS := execution.Journal.Stack + "-secrets"
	host := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": secretsNS}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao-pki", "env": environmentCreateEnv(map[string]string{"OPENBAO_PKI_HOST_RENEWAL_TLS_CA": "/run/pki-service/root.pem", "PKI_BOOTSTRAP_SESSION_ID": old}), "volumeMounts": []any{deploymentplan.Object{"name": "trust", "mountPath": "/run/pki-service", "readOnly": true}}}}, "volumes": []any{deploymentplan.Object{"name": "trust", "configMap": deploymentplan.Object{"name": "service-trust"}}}}}}}
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "service-trust", "namespace": secretsNS}, "data": deploymentplan.Object{"root.pem": "original-root", "unrelated": "preserve"}}
	if !unified {
		run("fixture-host", makePlan("fixture-host", []deploymentplan.Definition{{Phase: "workload", Object: host}}))
	} else if _, err := verifyOpenBaoInPlaceReady(ctx, cfg, store, execution, parent.native); err != nil {
		t.Fatal("original producer owner", err)
	}
	run("fixture-trust", makePlan("fixture-trust", []deploymentplan.Definition{{Phase: "configuration", Object: trust}}))
	var sourceSnapshot environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(execution.Journal), &sourceSnapshot); err != nil {
		t.Fatal(err)
	}
	qualifiedAt := time.Now().UTC()
	secondAdmission := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(execution.owner), SourceJournal: sourceSnapshot, SourceJournalSHA256: deploymentplan.Digest(sourceSnapshot), QualifiedAt: qualifiedAt, ExecutionDeadline: qualifiedAt.Add(24 * time.Hour), Renewal: *result}
	if len(currentCore) == 1 {
		// This continuation also qualifies the original remaining-create scope;
		// retain its real admitted clock instead of the older fixture's 24h span.
		secondAdmission.ExecutionDeadline, err = currentInitializerEffectiveDeadline(ctx, store, execution, qualifiedAt)
		if err != nil || !qualifiedAt.Before(secondAdmission.ExecutionDeadline) {
			t.Fatal("original current-core fixture deadline", err)
		}
	}
	// Both original bootstrap Jobs are truly completed in this native fixture.
	for _, id := range []string{result.SourceJobID, result.PreviousJobID} {
		object := get(id)
		object["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
		put(id, object)
	}
	secondAdmission.Batches, err = prepareRecordedServiceSessionBatches(ctx, store, execution, api, inventory, secondAdmission, []string{old, firstIntent.SessionID}, result.RenewedIntent.SessionID)
	if err != nil {
		t.Fatal("compile actual recorded session batches", err)
	}
	for _, batch := range secondAdmission.Batches {
		if batch.Plan != nil {
			for _, resource := range batch.Plan.Resources {
				if resource.ID == result.PreviousCustodyID {
					t.Fatal("custody reader duplicated the native mint")
				}
			}
		}
	}
	secondAdmission.Reference, err = prepareServiceSessionReference(ctx, store, execution, api, old, firstIntent.SessionID, result.RenewedIntent.SessionID)
	if err != nil {
		t.Fatal("prepare native reference", err)
	}
	if err := saveSecondSessionAdmission(store, execution, secondAdmission); err != nil {
		t.Fatal(err)
	}
	secondReview := deploymentplan.Digest(secondAdmission)
	if err := execution.BeginEntry(sessionLineagePrefix+secondReview, "session-lineage", mustProviderRecoveryJSON(sessionLineageIntent(secondReview, secondAdmission)), qualifiedAt); err != nil {
		t.Fatal(err)
	}
	// The executor records the batch intent before entering the native mint.
	// A stop at that boundary must recover this same admitted qualification.
	if err := execution.BeginEntry("service-session-batches-"+secondReview, "service-session-batches", serviceSessionBatchIntent(secondReview, secondAdmission), qualifiedAt); err != nil {
		t.Fatal(err)
	}
	beforeHead := deploymentplan.Digest(execution.Journal)
	pendingHead, pendingReview, pendingErr := sessionLineageHead(store, execution)
	if pendingErr != nil || pendingHead == nil || pendingReview != secondReview || deploymentplan.Digest(*pendingHead) != secondReview || deploymentplan.Digest(execution.Journal) != beforeHead {
		t.Fatal("pre-mint interruption lost the original admitted batch", pendingErr)
	}
	if _, err := restoreSecondSessionRuntime(ctx, store, execution, secondAdmission, secondReview, service, sessionOpenBaoRoot, plan); err != nil {
		t.Fatal("native runtime did not preserve its qualified plan", err)
	}
	if err := beginSessionLineage(store, execution, secondAdmission, secondReview, qualifiedAt); err != nil {
		t.Fatal(err)
	}
	if err := beginSessionLineage(store, execution, secondAdmission, secondReview, qualifiedAt); err != nil {
		t.Fatal("admission retry", err)
	}
	// Interrupt after the ConfigMap is written but before its parent stage
	// completes. The original journal and UID, not a synthetic ACK, prove it.
	setFailure("configuration")
	if err := executeServiceSessionReference(ctx, store, execution, api, secondAdmission, io.Discard); err == nil {
		t.Fatal("expected reference write interruption")
	}
	referenceEntry := createOpenBaoACKCompletionEntry(execution.Journal, "kubernetes/"+secondAdmission.Reference.Stage)
	if referenceEntry == nil || referenceEntry.State == "completed" {
		t.Fatal("interrupted reference incorrectly completed")
	}
	refID := secondAdmission.Reference.Plan.Resources[0].ID
	referenceWrites := writes()[refID]
	sources, err := loadServiceSessionCoreSources(ctx, store, execution, api, []string{refID})
	if err != nil || len(sources) != 1 || sources[0].Stage != secondAdmission.Reference.Stage {
		t.Fatal("pending reference journal not recovered", err)
	}
	live := get(refID)
	metadata := deploymentplan.Map(live["metadata"])
	uid := metadata["uid"]
	metadata["uid"] = "99999999-9999-4999-8999-999999999999"
	put(refID, live)
	if _, err := loadServiceSessionCoreSources(ctx, store, execution, api, []string{refID}); err == nil {
		t.Fatal("replacement ConfigMap UID accepted")
	}
	metadata["uid"] = uid
	put(refID, live)
	data := deploymentplan.Map(live["data"])
	if data["root.pem"] != "original-root" || data["unrelated"] != "preserve" || data[serviceSessionReferenceKey] != result.RenewedIntent.SessionID {
		t.Fatal("reference wrote unrelated data")
	}
	setFailure("")
	// Keep the original child and batch pending as the session expires. The
	// next generation must follow this write, not falsely complete or replay it.
	pendingReferencePath := filepath.Join(store.Root, "deployment", "rollback", secondAdmission.Reference.Plan.SHA256(), "journal.json")
	pendingReferenceJournal, err := os.ReadFile(pendingReferencePath)
	if err != nil {
		t.Fatal("missing interrupted reference journal", err)
	}
	resumed, err := prepareSecondSessionCandidateWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, now, review)
	if err != nil || deploymentplan.Digest(resumed) != deploymentplan.Digest(result) {
		t.Fatal("admission interruption did not preserve the exact candidate", err)
	}
	run("service-session-original-key-renewal-"+secondReview, result.Candidate)
	completedJob := get(result.Candidate.Resources[0].ID)
	completedJob["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	put(result.Candidate.Resources[0].ID, completedJob)
	newLeaf := *leafTemplate
	newLeaf.SerialNumber = big.NewInt(101)
	if joined != nil {
		newLeaf.NotAfter = time.Now().UTC().Add(45 * time.Second).Truncate(time.Second)
	}
	newDER, err := x509.CreateCertificate(rand.Reader, &newLeaf, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	newCert := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newDER}), []byte(issuerPEM+rootPEM)...)
	newPath := "/run/pki-bootstrap/renew-" + result.BindingSHA[:12] + "/bootstrap.crt"
	certificates[newPath] = newCert
	secondExpiry := now.Add(30 * time.Minute)
	if joined != nil {
		secondExpiry = newLeaf.NotAfter
	}
	states[result.RenewedIntent.SessionID] = managedSessionState{Session: result.RenewedIntent.SessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(newDER), Status: "active", Expires: secondExpiry, Subjects: subjects, Installed: slices.Clone(installedSubjects)}
	activeOld, activeFirst := oldState, firstState
	activeOld.OtherActive, activeFirst.OtherActive = 1, 1
	states[old], states[firstIntent.SessionID] = activeOld, activeFirst
	if err := observeSecondSessionSuccessorWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, secondReview, time.Now().UTC()); err != nil {
		t.Fatal("post-mint interruption recovery", err)
	}
	if joined == nil {
		for _, mode := range []string{"new-job-owner", "prior-credential", "original-session", "new-fingerprint", "competing-session", "csr"} {
			t.Run("post-mint-"+mode, func(t *testing.T) {
				objects := snapshot()
				savedObjects := make(map[string]deploymentplan.Object, len(objects))
				for id, value := range objects {
					savedObjects[id] = deploymentplan.Clone(value)
				}
				savedStates := make(map[string]managedSessionState, len(states))
				for id, value := range states {
					savedStates[id] = value
				}
				savedCSR := slices.Clone(csr)
				t.Cleanup(func() { syncChanges(savedObjects, objects); states, csr = savedStates, savedCSR })
				switch mode {
				case "new-job-owner":
					deploymentplan.Map(objects[result.Candidate.Resources[0].ID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				case "prior-credential":
					deploymentplan.Map(objects["Secret/"+namespace+"/service-bootstrap-"+firstIntent.SessionID]["data"])["tls.key"] = "Zm9yZWlnbg=="
				case "original-session":
					altered := states[old]
					altered.Fingerprint = strings.Repeat("f", 64)
					states[old] = altered
				case "new-fingerprint":
					altered := states[result.RenewedIntent.SessionID]
					altered.Fingerprint = strings.Repeat("f", 64)
					states[result.RenewedIntent.SessionID] = altered
				case "competing-session":
					altered := states[result.RenewedIntent.SessionID]
					altered.OtherActive = 1
					states[result.RenewedIntent.SessionID] = altered
				case "csr":
					csr = []byte("foreign")
				}
				syncChanges(objects, savedObjects)
				if err := observeSecondSessionSuccessorWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, secondReview, time.Now().UTC()); err == nil {
					t.Fatal("changed post-mint evidence was accepted")
				}
			})
		}
	}
	if err := verifyServiceSessionMintCustodyCompleted(ctx, store, execution, api, secondAdmission, secondReview); err == nil {
		t.Fatal("custody renewal accepted before its native child")
	}
	newReader := deploymentplan.Clone(custody.Object)
	newContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(newReader)["containers"])[0])
	newContainer["env"] = mergeEnvironmentNamedFields(newContainer["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": result.RenewedIntent.SessionID, "PKI_BOOTSTRAP_CERT_PATH": newPath}))
	readerDefs := []deploymentplan.Definition{{Phase: "workload", Object: newReader}}
	readerInputs := inputs
	readerInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("next-custody", readerDefs)
	readerPlan, err := deploymentplan.Compile(readerInputs, readerDefs, []deploymentplan.Object{get(deploymentplan.ID(newReader))})
	if err != nil {
		t.Fatal(err)
	}
	run("service-custody-reader-renewal-"+secondReview, readerPlan)
	if err := verifyServiceSessionMintCustodyCompleted(ctx, store, execution, api, secondAdmission, secondReview); err != nil {
		t.Fatal("original custody renewal child not accepted", err)
	}
	readerID := result.PreviousCustodyID
	readerObject := get(readerID)
	readerMetadata := deploymentplan.Map(readerObject["metadata"])
	readerUID := readerMetadata["uid"]
	readerMetadata["uid"] = "99999999-9999-4999-8999-999999999999"
	put(readerID, readerObject)
	if err := verifyServiceSessionMintCustodyCompleted(ctx, store, execution, api, secondAdmission, secondReview); err == nil {
		t.Fatal("accepted replacement custody reader")
	}
	readerMetadata["uid"] = readerUID
	put(readerID, readerObject)
	if err := observeSecondSessionSuccessorWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, secondReview, time.Now().UTC()); err != nil {
		t.Fatal("reader-update interruption recovery", err)
	}
	newCredential := environmentCreateSecret(namespace, "service-bootstrap-"+result.RenewedIntent.SessionID, map[string]string{"tls.key": string(keyPEM), "tls.crt": string(newCert), "ca.crt": rootPEM})
	newCredential.Object["immutable"] = true
	if joined != nil {
		deploymentplan.Map(newCredential.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": result.RenewedIntent.SessionID}
	}
	run("service-credential-secrets-renewal-"+secondReview, makePlan("next-credential", []deploymentplan.Definition{newCredential}))
	if joined != nil {
		joined.observeMint(t, store, execution, api, service, states[result.RenewedIntent.SessionID], secondReview)
	}
	states[old], states[firstIntent.SessionID] = oldState, firstState
	expiredSecond := states[result.RenewedIntent.SessionID]
	if joined != nil {
		for time.Now().Before(expiredSecond.Expires) {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
		expiredSecond.Status = "expired"
	} else {
		expiredSecond.Status, expiredSecond.Expires = "expired", now.Add(-30*time.Second)
	}
	states[result.RenewedIntent.SessionID] = expiredSecond
	third, err := qualify()
	if err != nil {
		t.Fatal("qualify third session", err)
	}
	if third.PreviousReview != secondReview || third.Intent.SessionID != result.RenewedIntent.SessionID || third.PreviousBindingSHA != result.BindingSHA || third.KeySHA != result.KeySHA || third.CSRSHA != result.CSRSHA || !slices.Equal(third.ClientDigests, result.ClientDigests) || third.RenewedIntent.SessionID == result.RenewedIntent.SessionID {
		t.Fatal("third session did not extend the same original key and identity lineage")
	}
	if len(currentCore) == 1 {
		currentCore[0].beforeSession(t, cfg, store, execution, api, parent.fault, plan, service, third.Intent.SessionID)
	}
	var thirdSnapshot environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(execution.Journal), &thirdSnapshot); err != nil {
		t.Fatal(err)
	}
	thirdAt := time.Now().UTC()
	thirdAdmission := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(execution.owner), SourceJournal: thirdSnapshot, SourceJournalSHA256: deploymentplan.Digest(thirdSnapshot), QualifiedAt: thirdAt, ExecutionDeadline: secondAdmission.ExecutionDeadline, Renewal: *third}
	priorIDs, priorHistory, err := serviceSessionPriorLineage(store, execution, thirdAdmission)
	if err != nil || !slices.Equal(priorIDs, []string{old, firstIntent.SessionID, result.RenewedIntent.SessionID}) || len(priorHistory) != 1 {
		t.Fatal("interrupted predecessor lineage omitted a previous SID", err, priorIDs)
	}
	thirdAdmission.RequiredACK = serviceSessionRequiredACKs(thirdAdmission, priorHistory)
	thirdAdmission.Batches, err = prepareRecordedServiceSessionBatches(ctx, store, execution, api, inventory, thirdAdmission, priorIDs, third.RenewedIntent.SessionID)
	if err != nil {
		t.Fatal("compile third-generation native owner batches", err)
	}
	for _, batch := range thirdAdmission.Batches {
		if batch.Plan != nil {
			for _, resource := range batch.Plan.Resources {
				if resource.ID == third.PreviousCustodyID {
					t.Fatal("third generation duplicated native custody update")
				}
			}
		}
	}
	thirdAdmission.Reference, err = prepareServiceSessionReference(ctx, store, execution, api, old, third.Intent.SessionID, third.RenewedIntent.SessionID, priorIDs...)
	if err != nil || thirdAdmission.Reference.SourceStage != secondAdmission.Reference.Stage {
		t.Fatal("next session did not follow interrupted native reference", err)
	}
	if err := saveSecondSessionAdmission(store, execution, thirdAdmission); err != nil {
		t.Fatal(err)
	}
	thirdReview := deploymentplan.Digest(thirdAdmission)
	if err := execution.BeginEntry(sessionLineagePrefix+thirdReview, "session-lineage", mustProviderRecoveryJSON(sessionLineageIntent(thirdReview, thirdAdmission)), thirdAt); err != nil {
		t.Fatal(err)
	}
	if err := execution.RecordReceipt(sessionLineagePrefix+thirdReview, mustProviderRecoveryJSON(sessionLineageIntent(thirdReview, thirdAdmission))); err != nil {
		t.Fatal(err)
	}
	if err := beginSessionLineage(store, execution, thirdAdmission, thirdReview, thirdAt); err != nil {
		t.Fatal(err)
	}
	if err := beginSessionLineage(store, execution, thirdAdmission, thirdReview, thirdAt); err != nil {
		t.Fatal("third admission retry", err)
	}
	if err := beginSessionLineage(store, execution, secondAdmission, secondReview, thirdAt); err == nil {
		t.Fatal("older lineage branch was accepted")
	}
	thirdResumed, err := prepareSecondSessionCandidateWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, now, secondReview)
	if err != nil || deploymentplan.Digest(thirdResumed) != deploymentplan.Digest(third) {
		t.Fatal("third admission interruption did not preserve the exact candidate", err)
	}
	run("service-session-original-key-renewal-"+thirdReview, third.Candidate)
	thirdLeaf := *leafTemplate
	thirdLeaf.SerialNumber = big.NewInt(102)
	if joined != nil {
		thirdLeaf.NotAfter = time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	}
	thirdDER, err := x509.CreateCertificate(rand.Reader, &thirdLeaf, issuer, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	thirdPath := "/run/pki-bootstrap/renew-" + third.BindingSHA[:12] + "/bootstrap.crt"
	certificates[thirdPath] = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: thirdDER}), []byte(issuerPEM+rootPEM)...)
	{
		// The isolated native process has now signed the original CSR. Observe
		// that exact written Job's controller outcome before publishing custody.
		id := third.Candidate.Resources[0].ID
		job := get(id)
		journal, exists, err := third.Candidate.ReadExecution(store.Root)
		if err != nil || !exists || journal.Owners[id] != deploymentplan.Map(job["metadata"])["uid"] || !deploymentplan.Matches(third.Candidate.Resources[0].Desired, job) {
			t.Fatal("original third native Job changed before its signed outcome", err)
		}
		completed := time.Now().UTC().Format(time.RFC3339Nano)
		job["status"] = deploymentplan.Object{"startTime": completed, "completionTime": completed, "succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
		if joined != nil {
			for _, observed := range coreContractNativeInventory(map[string]deploymentplan.Object{id: job}) {
				put(deploymentplan.ID(observed), observed)
			}
		} else {
			put(id, job)
		}
	}
	activeOld, activeFirst, activeSecond := oldState, firstState, expiredSecond
	activeOld.OtherActive, activeFirst.OtherActive, activeSecond.OtherActive = 1, 1, 1
	states[old], states[firstIntent.SessionID], states[result.RenewedIntent.SessionID] = activeOld, activeFirst, activeSecond
	thirdExpiry := now.Add(30 * time.Minute)
	if joined != nil {
		thirdExpiry = thirdLeaf.NotAfter
	}
	states[third.RenewedIntent.SessionID] = managedSessionState{Session: third.RenewedIntent.SessionID, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(thirdDER), Status: "active", Expires: thirdExpiry, Subjects: subjects, Installed: slices.Clone(installedSubjects)}
	var realACK *unifiedRealACKFixture
	if os.Getenv("RTK_TEST_REAL_UNIFIED_ACK") == "1" {
		if os.Getenv("VIDEO_CLOUD_TEST_DSN") == "" {
			t.Fatal("isolated PostgreSQL is required for real unified ACK")
		}
		crl := func(cert *x509.Certificate, key *ecdsa.PrivateKey) string {
			der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().UTC().Add(-time.Minute), NextUpdate: time.Now().UTC().Add(time.Hour)}, cert, key)
			if err != nil {
				t.Fatal(err)
			}
			return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
		}
		var realClients []map[string]any
		for i, client := range clients {
			realClients = append(realClients, map[string]any{"subject": client.Subject, "request_id": client.Request, "csr": client.CSR, "certificate": client.Certificate, "key": clientKeys[i]})
		}
		fixture := map[string]any{
			"environment": "dev", "root_id": service.Root.ID, "issuer_id": service.Intermediate.ID,
			"issuer_mount": "pki-issuers/service/" + service.Intermediate.ID + "/v1", "root_pem": rootPEM, "issuer_pem": issuerPEM,
			"root_crl": crl(root, rootSigner), "issuer_crl": crl(issuer, signer),
			"bootstrap_key": string(keyPEM), "bootstrap_csr": string(csr),
			"sessions": []any{map[string]any{"id": third.RenewedIntent.SessionID, "deployment_id": "isolated-third-generation", "certificate": string(certificates[thirdPath]), "subjects": subjects, "created_at": time.Now().UTC().Add(-time.Minute), "expires_at": states[third.RenewedIntent.SessionID].Expires}},
			"clients":  realClients,
		}
		realACK = startUnifiedRealACKFixture(t, mustWorkspaceRoot(t), fixture)
	}
	if err := observeSecondSessionSuccessorWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, thirdReview, time.Now().UTC()); err != nil {
		t.Fatal("third post-mint interruption recovery", err)
	}
	thirdReader := deploymentplan.Clone(newReader)
	thirdContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(thirdReader)["containers"])[0])
	thirdContainer["env"] = mergeEnvironmentNamedFields(thirdContainer["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": third.RenewedIntent.SessionID, "PKI_BOOTSTRAP_CERT_PATH": thirdPath}))
	thirdReaderDefs := []deploymentplan.Definition{{Phase: "workload", Object: thirdReader}}
	thirdReaderInputs := inputs
	thirdReaderInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("third-custody", thirdReaderDefs)
	thirdReaderPlan, err := deploymentplan.Compile(thirdReaderInputs, thirdReaderDefs, []deploymentplan.Object{get(deploymentplan.ID(thirdReader))})
	if err != nil {
		t.Fatal(err)
	}
	thirdCredential := environmentCreateSecret(namespace, "service-bootstrap-"+third.RenewedIntent.SessionID, map[string]string{"tls.key": string(keyPEM), "tls.crt": string(certificates[thirdPath]), "ca.crt": rootPEM})
	thirdCredential.Object["immutable"] = true
	if joined != nil {
		deploymentplan.Map(thirdCredential.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack, "rtk.cloud/bootstrap-session": third.RenewedIntent.SessionID}
	}
	thirdCredentialPlan := makePlan("third-credential", []deploymentplan.Definition{thirdCredential})
	// Registry reads and provider signing are simulated in this fixture. All
	// Kubernetes stage plans, native leases, parent and child journals are real.
	injected, ackAvailable, mints := false, false, 0
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return execution.Check(ctx) }
	applyNative := func(ctx context.Context, name string, p *deploymentplan.Plan) error {
		return executeEnvironmentCreateStagePlan(ctx, store, execution, name, p, api, check, check, check, func(context.Context) error { return nil }, io.Discard)
	}
	adapters := serviceSessionBatchExecution{Backend: api, Ready: func(context.Context) error { return nil }, VerifyHistory: func(ctx context.Context) error {
		if _, err := verifyServiceSessionHistoricalJobs(ctx, store, execution, api, thirdAdmission); err != nil {
			return err
		}
		return verifyServiceSessionReferenceSource(store, execution, thirdAdmission)
	}, Mint: func(ctx context.Context) error {
		mints++
		if err := applyNative(ctx, "service-custody-reader-renewal-"+thirdReview, thirdReaderPlan); err != nil {
			return err
		}
		if err := applyNative(ctx, "service-credential-secrets-renewal-"+thirdReview, thirdCredentialPlan); err != nil {
			return err
		}
		if joined != nil {
			joined.observeMint(t, store, execution, api, service, states[third.RenewedIntent.SessionID], thirdReview)
		}
		return verifyServiceSessionMintCustodyCompleted(ctx, store, execution, api, thirdAdmission, thirdReview)
	}, Pending: func(ctx context.Context, _ serviceSessionBatch) error {
		if err := executeServiceSessionReference(ctx, store, execution, api, thirdAdmission, io.Discard); err != nil {
			return err
		}
		if !injected {
			setFailure("workload")
			injected = true
		}
		return nil
	}, Accept: check, VerifyACKs: func(ctx context.Context) (managedSessionState, error) {
		state := states[third.RenewedIntent.SessionID]
		if realACK != nil {
			if ackAvailable {
				for _, client := range clients {
					if err := realACK.ACK(ctx, third.RenewedIntent.SessionID, client.Subject); err != nil {
						return managedSessionState{}, err
					}
				}
			}
			raw, err := realACK.Select(ctx, third.RenewedIntent.SessionID)
			if err != nil || json.Unmarshal(raw, &state) != nil {
				return managedSessionState{}, fmt.Errorf("actual native registry session: %w", err)
			}
			states[third.RenewedIntent.SessionID] = state
			return state, nil
		}
		if !ackAvailable {
			state.Installed = nil
		}
		return state, nil
	}, Clock: time.Now}
	bounded, cancel := context.WithDeadline(ctx, thirdAdmission.ExecutionDeadline)
	defer cancel()
	if err := executeServiceSessionBatches(bounded, store, execution, thirdReview, adapters, io.Discard); err == nil || !injected {
		t.Fatal("expected workload interruption", err)
	}
	setFailure("")
	if err := executeServiceSessionBatches(bounded, store, execution, thirdReview, adapters, io.Discard); err == nil {
		t.Fatal("missing registry ACK was accepted")
	}
	ackAvailable = true
	if err := executeServiceSessionBatches(bounded, store, execution, thirdReview, adapters, io.Discard); err != nil {
		t.Fatal("third session native batch did not resume", err)
	}
	if done, err := completedServiceSessionBatch(store, execution, thirdAdmission, thirdReview); err != nil || !done || mints != 3 {
		t.Fatal("third batch did not persist ACK and retry state", err, mints)
	}
	if writes()[refID] != referenceWrites+1 || deploymentplan.Map(get(refID)["data"])[serviceSessionReferenceKey] != third.RenewedIntent.SessionID {
		t.Fatal("next reference did not reuse same ConfigMap")
	}
	stillPending, err := os.ReadFile(pendingReferencePath)
	if err != nil || string(stillPending) != string(pendingReferenceJournal) || createOpenBaoACKCompletionEntry(execution.Journal, "kubernetes/"+secondAdmission.Reference.Stage).State == "completed" {
		t.Fatal("previous incomplete reference journal was rewritten", err)
	}
	if _, err := completedApplicationOwnerWrites(ctx, store, execution, api); err != nil {
		t.Fatal("original create did not accept actual acknowledged successor owners", err)
	}
	if err := observeSecondSessionSuccessorWithReads(ctx, cfg, store, execution, reads, plan, service, sessionOpenBaoRoot, thirdReview, time.Now().UTC()); err != nil {
		t.Fatal("third reader-update interruption recovery", err)
	}
	// The actual unified route must re-read registry ACKs before returning to
	// original create. kubectl and registry here are isolated protocol fakes.
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'expires_at',expires_at,'subjects',subjects,'installed_subjects',installed_subjects,'other_active',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND status='active' AND expires_at>now() AND session_id<>'" + third.RenewedIntent.SessionID + "')) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + third.RenewedIntent.SessionID + "'"
	registryArgs := []string{"--namespace", execution.Journal.Stack + "-platform", "exec", "postgresql-0", "--container", "postgres", "--", "env", "PGOPTIONS=-c default_transaction_read_only=on", "/bin/sh", "-ec", `PGPASSWORD="$POSTGRES_PASSWORD" psql -h 127.0.0.1 -U postgres -d video_cloud -v ON_ERROR_STOP=1 -At -c "$1"`, "registry-read", query}
	registryKey := strings.Join(registryArgs, " ")
	responses := map[string]string{registryKey: string(mustProviderRecoveryJSON(states[third.RenewedIntent.SessionID]))}
	var native environmentKubernetesBackend
	var nativeDir string
	if unified {
		native, nativeDir = parent.native, parent.nativeDir
		if err := os.WriteFile(filepath.Join(nativeDir, "exec-responses.json"), mustProviderRecoveryJSON(responses), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		native, nativeDir, _ = unifiedSessionNativeBackend(t, store, backend.objects, responses)
	}
	actualReference, err := native.Read(ctx, refID)
	if err != nil || actualReference == nil || deploymentplan.Map(actualReference["metadata"])["uid"] != uid {
		t.Fatal("production kubectl adapter did not read original isolated ConfigMap", err)
	}
	if realACK != nil {
		t.Setenv("RTK_UNIFIED_ACK_DIR", realACK.dir)
	}
	// Full original-create qualification must also accept the completed head,
	// while retaining the historical original session as its identity anchor.
	readCompleted := func(_ context.Context, id string) (managedSessionState, error) { return states[id], nil }
	if err := verifyCompletedIssuerSession(ctx, store, execution, thirdAdmission, intent, states[old], time.Now().UTC(), readCompleted); err != nil {
		t.Fatal("full preflight could not adopt the completed session lineage", err)
	}
	if joined == nil {
		for _, mode := range []string{"original-fingerprint", "original-installed", "competing", "missing-ack", "stale-head"} {
			t.Run("completed-preflight-"+mode, func(t *testing.T) {
				originalState := states[old]
				latestState := states[third.RenewedIntent.SessionID]
				selected := thirdAdmission
				switch mode {
				case "original-fingerprint":
					originalState.Fingerprint = strings.Repeat("f", 64)
				case "original-installed":
					originalState.Installed = []string{"foreign-subject"}
				case "competing":
					latestState.OtherActive = 1
				case "missing-ack":
					latestState.Installed = nil
				case "stale-head":
					selected = secondAdmission
				}
				if err := verifyCompletedIssuerSession(ctx, store, execution, selected, intent, originalState, time.Now().UTC(), func(context.Context, string) (managedSessionState, error) { return latestState, nil }); err == nil {
					t.Fatal("accepted invalid completed preflight lineage", mode)
				}
			})
		}
	}
	// Resume the real original mount-forward runner after session batches
	// supersede its accepted Deployments. Older templates must not run again.
	q1, q1Update, q1Retain, err := loadInitializerForwardBundle(store, execution, q2Boundary.InitializerQualificationSHA256)
	if err != nil {
		t.Fatal(err)
	}
	q0, _, _, err := loadFailedForwardBundle(store, execution, q1.FailedForwardSHA256)
	if err != nil {
		t.Fatal(err)
	}
	_, q2Update, q2Retain, err := loadInitializerMountForwardBundle(store, execution, deploymentplan.Digest(q2Boundary))
	if err != nil {
		t.Fatal(err)
	}
	originalRetainPath := filepath.Join(store.Root, "deployment", "rollback", q2Retain.SHA256(), "journal.json")
	beforeRetain, err := os.ReadFile(originalRetainPath)
	if err != nil {
		t.Fatal(err)
	}
	resumeContext, resumeCancel := context.WithDeadline(ctx, q2Boundary.ExecutionDeadline)
	defer resumeCancel()
	neverReplay := func(ctx context.Context, stage string, p *deploymentplan.Plan) error {
		if !strings.HasPrefix(stage, "session-owner-retain-") || p.SHA256() == q2Retain.SHA256() {
			return fmt.Errorf("completed predecessor was replayed")
		}
		return applyNative(ctx, stage, p)
	}
	neverReaccept := func(ctx context.Context, p *deploymentplan.Plan) error {
		if p.SHA256() == q2Retain.SHA256() {
			return fmt.Errorf("completed predecessor was reaccepted")
		}
		return p.Verify(ctx, api)
	}
	if err := executeInitializerMountForward(resumeContext, store, execution, api, q2Boundary, q1, q0, admitted, admittedReview, q1Update, q1Retain, q2Update, q2Retain, q2Boundary.ExecutionDeadline, neverReplay, neverReaccept, completeInitializerMountForwardAlias, time.Now); err != nil {
		t.Fatal("original create runner failed after the completed successor", err)
	}
	afterRetain, err := os.ReadFile(originalRetainPath)
	if err != nil || string(afterRetain) != string(beforeRetain) {
		t.Fatal("original failed retained journal was rewritten", err)
	}
	if _, err := completedApplicationOwnerWrites(ctx, store, execution, api); err != nil {
		t.Fatal("new accepted retain alias lost original application history", err)
	}
	for _, stage := range []string{q1.FailedRetainStage, q1.UpdateStage, q2Boundary.RetainStage} {
		entry := createOpenBaoACKCompletionEntry(execution.Journal, "kubernetes/"+stage)
		if entry == nil || entry.State != "completed" || !originalStageSupersessionReceiptMatches(entry.Receipt) {
			t.Fatal("original failed native parent was not truthfully superseded", stage)
		}
		p, err := readEnvironmentCreateStageInput(store, execution, stage)
		if err != nil {
			t.Fatal(err)
		}
		j, exists, err := readEnvironmentCreateStageProgress(store, execution, stage, p)
		if err != nil || !exists || j.Accepted || j.Failed == "" {
			t.Fatal("supersession fabricated original native acceptance", stage, err)
		}
	}
	// An expired completed head re-enters this exact producer before a next
	// same-key renewal. The application alias and all three original FAILED
	// child journals must still be usable as authenticated source history.
	closedJournal, closedWrites := deploymentplan.Digest(execution.Journal), deploymentplan.Digest(writes())
	if _, err := loadServiceSessionApplicationSources(ctx, store, execution, api); err != nil {
		t.Fatal("completed typed source closure cannot qualify the next session", err)
	}
	if deploymentplan.Digest(execution.Journal) != closedJournal || deploymentplan.Digest(writes()) != closedWrites {
		t.Fatal("next-session source observation changed completed native history")
	}
	if len(currentCore) == 1 {
		if joined != nil {
			joined.capture(rootSigner, signer, keyPEM, csr, certificates, states, clients)
			if deploymentplan.Digest(clients) != thirdAdmission.Renewal.ClientsSHA {
				t.Fatal("native Mint changed original clients before product enrollment")
			}
			if err := recordManagedSessionClients(store, filepath.Join("deployment/create", execution.Journal.PlanSHA256, "bootstrap/service/session-renewal", thirdReview, "managed-clients-preserved.json"), map[string]string{"original_session_id": thirdAdmission.Renewal.Intent.SessionID, "renewed_session_id": thirdAdmission.Renewal.RenewedIntent.SessionID, "clients_sha256": thirdAdmission.Renewal.ClientsSHA, "original_key_sha256": thirdAdmission.Renewal.KeySHA, "original_csr_sha256": thirdAdmission.Renewal.CSRSHA}); err != nil {
				t.Fatal(err)
			}
		}
		currentCore[0].afterSession(t, cfg, store, execution, api, parent.native, plan, service, thirdAdmission, thirdReview, states[third.RenewedIntent.SessionID])
		return
	}
	// Ordinary create can write the current same-UID core template and stop
	// before readiness, then accept the public-data producer. A next renewal
	// must use that actual prefix rather than restoring a pre-core template.
	coreBody := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "fixture-core-controller", "namespace": namespace}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "controller", "image": image.Reference, "env": environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": third.RenewedIntent.SessionID})}}}}}}
	coreInitial := makePlan("fixture-core-before-completion", []deploymentplan.Definition{{Object: coreBody, Phase: "workload"}})
	run("fixture-core-before-completion", coreInitial)
	coreID := coreInitial.Resources[0].ID
	coreBody = deploymentplan.Clone(coreInitial.Resources[0].Desired)
	coreContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(coreBody)["containers"])[0])
	coreContainer["env"] = environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": third.RenewedIntent.SessionID, "OPENBAO_SERVER_CRL_MANIFEST": "/run/pki-openbao-create/crls.json"})
	coreDefs := []deploymentplan.Definition{{Object: coreBody, Phase: "workload"}}
	coreInputs := inputs
	coreInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("fixture-managed-complete-issuer", coreDefs)
	corePending, err := deploymentplan.Compile(coreInputs, coreDefs, []deploymentplan.Object{get(coreID)})
	if err != nil || corePending.Resources[0].Action != "update" {
		t.Fatal("ordinary core successor did not preserve original owner", err)
	}
	checkCore := func(ctx context.Context, _ *deploymentplan.Plan) error { return execution.Check(ctx) }
	setFailure("workload")
	if err := executeEnvironmentCreateStagePlan(ctx, store, execution, "fixture-managed-complete-issuer", corePending, api, checkCore, checkCore, checkCore, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("ordinary native core write did not stop at readiness")
	}
	setFailure("")
	retainedPublic := get("ConfigMap/" + namespace + "/fixture-00")
	publicDefs := []deploymentplan.Definition{{Object: retainedPublic, Phase: "configuration", Retain: true}}
	publicInputs := inputs
	publicInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("fixture-public-cache-producer", publicDefs)
	publicRetain, err := deploymentplan.Compile(publicInputs, publicDefs, []deploymentplan.Object{retainedPublic})
	if err != nil || publicRetain.Resources[0].Action != "retain" {
		t.Fatal("public producer did not preserve its shared owner", err)
	}
	run("fixture-public-cache-producer", publicRetain)
	coreJournal, exists, err := corePending.ReadExecution(store.Root)
	if err != nil || !exists || coreJournal.Accepted || coreJournal.Failed == "" {
		t.Fatal("ordinary core prefix lost its truthful native failure", err)
	}
	observedCoreJournal, observedCoreWrites := deploymentplan.Digest(execution.Journal), deploymentplan.Digest(writes())
	coreSources, err := loadServiceSessionCoreSources(ctx, store, execution, api, []string{coreID})
	if err != nil || len(coreSources) != 1 || coreSources[0].Stage != "fixture-managed-complete-issuer" || coreSources[0].Plan.SHA256() != corePending.SHA256() || !coreSources[0].SelectedIDs[coreID] {
		t.Fatal("next renewal ignored the current ordinary core prefix", err)
	}
	if deploymentplan.Digest(execution.Journal) != observedCoreJournal || deploymentplan.Digest(writes()) != observedCoreWrites {
		t.Fatal("ordinary core source observation replayed native work")
	}
	checkedCore, exists, err := corePending.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(checkedCore) != deploymentplan.Digest(coreJournal) {
		t.Fatal("ordinary core source observation changed its failed journal", err)
	}
	// Continue through the real logical application callback with the current
	// acknowledged session bodies, after the original failed source is closed.
	var applicationAlias createFailedForwardAlias
	if err := decodeEnvironmentJSON(createOpenBaoACKCompletionEntry(execution.Journal, "kubernetes/"+q0.FailedStage.Stage).Receipt, &applicationAlias); err != nil {
		t.Fatal(err)
	}
	currentApplication, _, err := acceptedCreateStage(store, execution, applicationAlias.RetainStage)
	if err != nil {
		t.Fatal(err)
	}
	q1.mount = &q2Boundary
	q0.initializer = &q1
	originalJournal, originalWrites := deploymentplan.Digest(execution.Journal), deploymentplan.Digest(writes())
	if err := verifyCompletedApplicationForwardStage(ctx, cfg, store, &plan, execution, api, &q0, environmentCreateDefinitions(currentApplication)); err != nil {
		t.Fatal("original application callback rejected its completed session successor", err)
	}
	if deploymentplan.Digest(execution.Journal) != originalJournal || deploymentplan.Digest(writes()) != originalWrites {
		t.Fatal("completed application callback replayed its original source")
	}
	applicationEntry := createOpenBaoACKCompletionEntry(execution.Journal, "kubernetes/"+q0.FailedStage.Stage)
	originalAliasBytes := append([]byte(nil), applicationEntry.Receipt...)
	applicationAlias.RetainPlanSHA256 = strings.Repeat("f", 64)
	applicationEntry.Receipt = mustProviderRecoveryJSON(applicationAlias)
	observationError := verifyCompletedApplicationForwardStage(ctx, cfg, store, &plan, execution, api, &q0, environmentCreateDefinitions(currentApplication))
	applicationEntry.Receipt = originalAliasBytes
	if observationError == nil {
		t.Fatal("completed application callback accepted a foreign retained child")
	}
	beforeRetry := deploymentplan.Digest(execution.Journal)
	if err := executeInitializerMountForward(resumeContext, store, execution, api, q2Boundary, q1, q0, admitted, admittedReview, q1Update, q1Retain, q2Update, q2Retain, q2Boundary.ExecutionDeadline, func(context.Context, string, *deploymentplan.Plan) error {
		return fmt.Errorf("accepted current owners were replayed")
	}, neverReaccept, completeInitializerMountForwardAlias, time.Now); err != nil {
		t.Fatal("completed current-owner handoff retry", err)
	}
	if deploymentplan.Digest(execution.Journal) != beforeRetry {
		t.Fatal("completed alias receipt was replaced")
	}
	beforeUnified := deploymentplan.Digest(execution.Journal)
	for _, action := range []string{"preflight", "create"} {
		handled, err := tryUnifiedServiceSessionContinuation(ctx, cfg, unifiedDeploymentOptions{Action: action, Operation: "create"}, &unifiedDeploymentReport{}, store, execution, native, plan, nil, environmentCreateJournalEntry{}, admitted, io.Discard, io.Discard)
		if err != nil || handled || deploymentplan.Digest(execution.Journal) != beforeUnified {
			t.Fatal("completed session failed to return to original create", action, err)
		}
	}
	if realACK != nil {
		if err := os.Unsetenv("RTK_UNIFIED_ACK_DIR"); err != nil {
			t.Fatal(err)
		}
	}
	missingACK := states[third.RenewedIntent.SessionID]
	missingACK.Installed = nil
	responses[registryKey] = string(mustProviderRecoveryJSON(missingACK))
	if err := os.WriteFile(filepath.Join(nativeDir, "exec-responses.json"), mustProviderRecoveryJSON(responses), 0600); err != nil {
		t.Fatal(err)
	}
	if handled, err := tryUnifiedServiceSessionContinuation(ctx, cfg, unifiedDeploymentOptions{Action: "preflight", Operation: "create"}, &unifiedDeploymentReport{}, store, execution, native, plan, nil, environmentCreateJournalEntry{}, admitted, io.Discard, io.Discard); err == nil || !handled {
		t.Fatal("unified route accepted missing registry ACK", err)
	}
	// Isolated original continuation admission binds the same q2 deadline.
	// An expired successor must reach host qualification, but cannot execute
	// before a real accepted in-place host receipt exists.
	continuationKey := issuerEnrollmentContinuationPrefix + deploymentplan.Digest("fixture-original-continuation")
	admissionTime := time.Now().UTC()
	if err := execution.BeginEntry(continuationKey, "continuation", mustProviderRecoveryJSON(deploymentplan.Object{"fixture": "original"}), admissionTime); err != nil {
		t.Fatal(err)
	}
	prior := *createOpenBaoACKCompletionEntry(execution.Journal, continuationKey)
	resumePath := filepath.Join("deployment", "create", "issuer-enrollment-continuation", admittedReview, "resume-admission.json")
	if err := recordIssuerResumeAdmission(store, resumePath, admittedReview, prior.Key, admissionTime, q2Boundary.ExecutionDeadline); err != nil {
		t.Fatal(err)
	}
	expiredForRoute := states[third.RenewedIntent.SessionID]
	expiredForRoute.Status, expiredForRoute.Expires = "expired", time.Now().UTC().Add(-time.Second)
	responses[registryKey] = string(mustProviderRecoveryJSON(expiredForRoute))
	if err := os.WriteFile(filepath.Join(nativeDir, "exec-responses.json"), mustProviderRecoveryJSON(responses), 0600); err != nil {
		t.Fatal(err)
	}
	beforeExpiredRoute := deploymentplan.Digest(execution.Journal)
	handled, routeErr := tryUnifiedServiceSessionContinuation(ctx, cfg, unifiedDeploymentOptions{Action: "preflight", Operation: "create"}, &unifiedDeploymentReport{}, store, execution, native, plan, nil, prior, admitted, io.Discard, io.Discard)
	if unified {
		if _, err := verifyOpenBaoInPlaceReady(ctx, cfg, store, execution, native); err != nil {
			t.Fatal("recorded producer owner could not be reverified", err)
		}
		if !handled || routeErr == nil || strings.Contains(routeErr.Error(), "accepted original OpenBao in-place image owner is unavailable") || deploymentplan.Digest(execution.Journal) != beforeExpiredRoute {
			t.Fatal("expired session did not pass the recorded host handoff before the next qualification", routeErr)
		}
	} else if !handled || routeErr == nil || !strings.Contains(routeErr.Error(), "accepted original OpenBao in-place image owner is unavailable") || deploymentplan.Digest(execution.Journal) != beforeExpiredRoute {
		t.Fatal("expired session did not stop at the actual host handoff", routeErr)
	}
	states[old], states[firstIntent.SessionID], states[result.RenewedIntent.SessionID] = oldState, firstState, expiredSecond
	expiredThird := states[third.RenewedIntent.SessionID]
	expiredThird.Status, expiredThird.Expires = "expired", now.Add(-15*time.Second)
	states[third.RenewedIntent.SessionID] = expiredThird
	if realACK != nil {
		if err := realACK.Stop(ctx); err != nil {
			t.Fatal("native ACK harness did not close cleanly", err)
		}
	}
	fourth, err := qualify()
	if err != nil || fourth.PreviousReview != thirdReview || fourth.Intent.SessionID != third.RenewedIntent.SessionID || fourth.KeySHA != result.KeySHA || fourth.CSRSHA != result.CSRSHA {
		t.Fatal("fourth successor did not extend the same lineage", err)
	}
	if unified {
		recordServiceSessionNativeMintTransport(t, cfg, store, execution, get(refID), time.Now().UTC())
		runtime, err := restoreSecondSessionRuntime(ctx, store, execution, thirdAdmission, thirdReview, service, sessionOpenBaoRoot, plan)
		if err != nil {
			t.Fatal(err)
		}
		before := deploymentplan.Digest(execution.Journal)
		if err := verifyCompletedSessionTransportHistory(ctx, cfg, store, execution, runtime); err != nil {
			t.Fatal("completed managed transport could not read historical template authority", err)
		}
		changed := *runtime
		changed.Review = deploymentplan.Digest("foreign session")
		if err := verifyCompletedSessionTransportHistory(ctx, cfg, store, execution, &changed); err == nil {
			t.Fatal("foreign session authorized historical transport")
		}
		if deploymentplan.Digest(execution.Journal) != before {
			t.Fatal("historical transport reader mutated the original execution")
		}
	}
	for _, entry := range execution.Journal.Entries {
		if strings.HasPrefix(entry.Key, sessionLineagePrefix) && (entry.State != "completed" || len(entry.Receipt) == 0) {
			t.Fatal("admission interruption left a pending parent entry")
		}
	}
	// An ordinary workload can replace the same application template after
	// the session batch completes, then fail at readiness. A subsequent
	// source read must follow that actual native prefix without replaying it.
	applicationSources, err := loadServiceSessionApplicationSources(ctx, store, execution, api)
	if err != nil {
		t.Fatal(err)
	}
	var appID string
	for _, source := range applicationSources {
		for _, r := range source.Plan.Resources {
			if source.SelectedIDs[r.ID] && r.Desired["kind"] == "Deployment" {
				appID = r.ID
				break
			}
		}
		if appID != "" {
			break
		}
	}
	if appID == "" {
		t.Fatal("application source absent")
	}
	app := deploymentplan.Clone(get(appID))
	deploymentplan.Map(app["spec"])["replicas"] = float64(3)
	appDefs := []deploymentplan.Definition{{Object: app, Phase: "workload"}}
	appInputs := inputs
	appInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("fixture-ordinary-application", appDefs)
	appPlan, err := deploymentplan.Compile(appInputs, appDefs, []deploymentplan.Object{get(appID)})
	if err != nil || appPlan.Resources[0].Action != "update" {
		t.Fatal(err)
	}
	setFailure("workload")
	if err := executeEnvironmentCreateStagePlan(ctx, store, execution, "fixture-ordinary-application", appPlan, api, checkCore, checkCore, checkCore, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("ordinary application interruption absent")
	}
	setFailure("")
	originalState, originalEffects := deploymentplan.Digest(execution.Journal), deploymentplan.Digest(writes())
	currentSources, err := loadServiceSessionApplicationSources(ctx, store, execution, api)
	if err != nil {
		t.Fatal("ordinary application source cannot be loaded", err)
	}
	foundOrdinary := false
	for _, source := range currentSources {
		if source.SelectedIDs[appID] {
			foundOrdinary = source.Stage == "fixture-ordinary-application" && source.Plan.SHA256() == appPlan.SHA256()
		}
	}
	if !foundOrdinary {
		t.Fatal("ordinary application prefix ignored in session source")
	}
	var oneOwner []serviceSessionRecordedPlan
	for _, source := range currentSources {
		source.SelectedIDs = map[string]bool{appID: source.SelectedIDs[appID]}
		oneOwner = append(oneOwner, source)
	}
	candidate, _, err := compileServiceSessionSIDForward(oneOwner, []deploymentplan.Object{get(appID)}, third.RenewedIntent.SessionID, fourth.RenewedIntent.SessionID, "fixture-follow-ordinary-app")
	if err != nil || candidate == nil || len(candidate.Resources) != 1 || candidate.Resources[0].ID != appID || candidate.Resources[0].UID != appPlan.Resources[0].UID || !deploymentplan.Matches(appPlan.Resources[0].Desired, candidate.Resources[0].Baseline) {
		t.Fatal("ordinary application history cannot compile the original-owner session update", err)
	}
	if deploymentplan.Digest(execution.Journal) != originalState || deploymentplan.Digest(writes()) != originalEffects {
		t.Fatal("application source observation replayed native writes")
	}

	// Also discover a newly created ordinary core owner, as in the final MQTT
	// foundation. Exercise the same complete application/core batch producer
	// used by the next preflight, with the real admitted bootstrap ancestry.
	mqtt := deploymentplan.Clone(coreBody)
	deploymentplan.Map(mqtt["metadata"])["name"] = "fixture-new-mqtt-foundation"
	mqttContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(mqtt)["containers"])[0])
	mqttContainer["env"] = environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": old})
	mqttDefs := []deploymentplan.Definition{{Object: mqtt, Phase: "workload"}}
	mqttInputs := inputs
	mqttInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("fixture-new-mqtt-pending", mqttDefs)
	mqttPlan, err := deploymentplan.Compile(mqttInputs, mqttDefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	setFailure("workload")
	if err := executeEnvironmentCreateStagePlan(ctx, store, execution, "fixture-new-mqtt-pending", mqttPlan, api, checkCore, checkCore, checkCore, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("new MQTT interruption absent")
	}
	setFailure("")
	// The final workload also legitimately updates the same OpenBao template
	// before whole-workload readiness. Its projection source remains a known
	// original write, not an accepted whole-workload receipt.
	baoID := thirdAdmission.Reference.HostID
	baoBody := deploymentplan.Clone(get(baoID))
	deploymentplan.Map(baoBody["spec"])["minReadySeconds"] = float64(2)
	baoDefs := []deploymentplan.Definition{{Object: baoBody, Phase: "workload"}}
	baoInputs := inputs
	baoInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("fixture-known-bao-template", baoDefs)
	baoPending, err := deploymentplan.Compile(baoInputs, baoDefs, []deploymentplan.Object{get(baoID)})
	if err != nil {
		t.Fatal(err)
	}
	setFailure("workload")
	if err := executeEnvironmentCreateStagePlan(ctx, store, execution, "fixture-known-bao-template", baoPending, api, checkCore, checkCore, checkCore, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("OpenBao template interruption absent")
	}
	setFailure("")
	var fourthSnapshot environmentCreateJournal
	if err := json.Unmarshal(mustProviderRecoveryJSON(execution.Journal), &fourthSnapshot); err != nil {
		t.Fatal(err)
	}
	fourthAdmission := secondSessionAdmission{Schema: secondSessionAdmissionSchema, Environment: store.Environment, ParentOwnerSHA256: deploymentplan.Digest(execution.owner), SourceJournal: fourthSnapshot, SourceJournalSHA256: deploymentplan.Digest(fourthSnapshot), QualifiedAt: time.Now().UTC(), ExecutionDeadline: thirdAdmission.ExecutionDeadline, Renewal: *fourth}
	fourthPrior, _, err := serviceSessionPriorLineage(store, execution, fourthAdmission)
	if err != nil {
		t.Fatal(err)
	}
	batchState, batchEffects := deploymentplan.Digest(execution.Journal), deploymentplan.Digest(writes())
	batches, err := prepareRecordedServiceSessionBatches(ctx, store, execution, api, inventory, fourthAdmission, fourthPrior, fourth.RenewedIntent.SessionID)
	if err != nil {
		t.Fatal("full application/core producer rejected actual pending workload writes", err)
	}
	mqttOwners := 0
	for _, batch := range batches {
		if batch.Plan == nil {
			continue
		}
		for _, r := range batch.Plan.Resources {
			if r.ID == mqttPlan.Resources[0].ID {
				mqttOwners++
				if r.Action != "update" || r.UID != deploymentplan.Map(get(r.ID)["metadata"])["uid"] {
					t.Fatal("new MQTT owner identity changed")
				}
			}
		}
	}
	if mqttOwners != 1 {
		t.Fatal("new MQTT owner absent or duplicated in full batch producer", mqttOwners)
	}
	fourthAdmission.Batches = batches
	fourthAdmission.Reference, err = prepareServiceSessionReference(ctx, store, execution, api, old, third.RenewedIntent.SessionID, fourth.RenewedIntent.SessionID, fourthPrior...)
	if err != nil {
		t.Fatal(err)
	}
	if fourthAdmission.Reference.HostStage != "fixture-known-bao-template" {
		t.Fatal("OpenBao projection lost current recorded owner")
	}
	if err := verifyServiceSessionReferenceSource(store, execution, fourthAdmission); err != nil {
		t.Fatal("projection verifier requires whole-workload acceptance", err)
	}
	if deploymentplan.Digest(execution.Journal) != batchState || deploymentplan.Digest(writes()) != batchEffects {
		t.Fatal("full source producer replayed work or minted a session")
	}
	if unified {
		// Template handoff must not wait for the app side of the account
		// Deployment before the session needed by that app has been renewed.
		original := get(appID)
		notReady := deploymentplan.Clone(original)
		notReady["status"] = deploymentplan.Object{"readyReplicas": float64(0)}
		put(appID, notReady)
		templates := environmentCreateKubernetesBackend{environmentKubernetesBackend: parent.native, execution: execution, cfg: cfg, store: store, templateOnly: true}
		resource := appPlan.Resources[0]
		if err := templates.Wait(ctx, resource); err != nil {
			t.Fatal("early templates depend on application readiness", err)
		}
		resource.UID = "foreign-owner"
		if err := templates.Wait(ctx, resource); err == nil {
			t.Fatal("template stage accepted a replaced owner")
		}
		put(appID, original)
	}

}
