package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type acceptedCoreExecutionContract struct {
	cfg          deploymentConfig
	store        secretStore
	e            *environmentCreateExecution
	p            createFoundationRecoveryPlan
	backend      environmentKubernetesBackend
	native       *normalTailE2EBackend
	operator     map[string]string
	protocol     map[string]any
	protocolPath string
	review       string
	session      string
	key          string
	csr          string
	certificate  string
	issuerKey    *ecdsa.PrivateKey
	service      environmentCreateServiceCeremony
	runtime      environmentCreateBootstrapRuntime
}

func coreContractCertificate(t *testing.T, name string, ca bool, parent *x509.Certificate, signer *ecdsa.PrivateKey, lifetimes ...time.Duration) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	lifetime := 4 * time.Hour
	if len(lifetimes) > 1 {
		t.Fatal("one original certificate lifetime required")
	}
	if len(lifetimes) == 1 {
		lifetime = lifetimes[0]
	}
	if lifetime <= 0 {
		t.Fatal("positive original certificate lifetime required")
	}
	extKeyUsage := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if ca {
		extKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(lifetime), BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: extKeyUsage}
	if ca {
		template.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	if parent == nil {
		parent, signer = template, key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func (f *acceptedCoreExecutionContract) write(t *testing.T, path string, v any) {
	t.Helper()
	if err := f.store.write(path, mustProviderRecoveryJSON(v), true); err != nil {
		t.Fatal(err)
	}
}
func (f *acceptedCoreExecutionContract) record(t *testing.T, key string, intent, receipt any) {
	t.Helper()
	if err := f.e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
func (f *acceptedCoreExecutionContract) publish(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.protocolPath, mustProviderRecoveryJSON(f.protocol), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *acceptedCoreExecutionContract) stage(t *testing.T, key string, defs []deploymentplan.Definition) *deploymentplan.Plan {
	t.Helper()
	for i := range defs {
		if defs[i].Object["kind"] == "Secret" {
			data := deploymentplan.Map(defs[i].Object["data"])
			if data == nil {
				data = deploymentplan.Object{}
			}
			for k, v := range deploymentplan.Map(defs[i].Object["stringData"]) {
				data[k] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(v)))
			}
			defs[i].Object["data"] = data
			delete(defs[i].Object, "stringData")
		}
	}
	var observed []deploymentplan.Object
	for _, v := range f.native.objects {
		observed = append(observed, v)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(key, defs), CreateParentSHA256: f.e.Journal.PlanSHA256}, defs, observed)
	if err != nil {
		t.Fatal(key, err)
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	if err := p.Execute(context.Background(), f.native, p.SHA256(), f.store.Root, check, check, check); err != nil {
		t.Fatal(key, err)
	}
	var j deploymentplan.Journal
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json"), &j); err != nil {
		t.Fatal(err)
	}
	if !j.Accepted || len(j.Completed) != len(p.Resources) {
		t.Fatal("child stage never reached actual acceptance", key)
	}
	if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(map[string]any{"plan_sha256": p.SHA256(), "owners": j.Owners})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return p
}

// An optional original database producer may establish this primary before
// Service bootstrap. Its copied verifier credentials must retain those bytes.
func (f *acceptedCoreExecutionContract) originalVerifierDefinition(t *testing.T, namespace string) deploymentplan.Definition {
	t.Helper()
	primary := f.native.objects["Secret/"+f.p.Plan.Stack+"-video-cloud/pki-verifier-database"]
	if primary == nil {
		return environmentCreateSecret(namespace, "pki-verifier-database", map[string]string{"url": "postgres://isolated/verification"})
	}
	data := deploymentplan.Map(primary["data"])
	if deploymentplan.String(data["url"]) == "" {
		t.Fatal("original verifier credential data unavailable")
	}
	return deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-verifier-database", "namespace": namespace}, "type": "Opaque", "data": deploymentplan.Clone(data)}}
}

// This fixture renders the native Service bootstrap, custody, issuer and
// enrollment recipes, executes their real child plans, then exposes explicit
// Kubernetes/registry/Account Manager protocol observations. All private roots,
// authorities, keys and localhost listeners belong only to this test.
func newAcceptedCoreExecutionContract(t *testing.T, expired ...bool) *acceptedCoreExecutionContract {
	t.Helper()
	return newAcceptedCoreExecutionContractWithListeners(t, len(expired) == 1 && expired[0], false)
}

func newAcceptedCoreExecutionContractWithListeners(t *testing.T, expired, listeners bool) *acceptedCoreExecutionContract {
	t.Helper()
	return newCoreExecutionContractAtBoundary(t, expired, listeners, false)
}

type coreExecutionContractHooks struct {
	ImageManifests                       map[string]string
	OriginalServiceCALifetime            time.Duration
	OriginalServiceCRLLifetime           time.Duration
	OriginalServiceIntermediateLifecycle bool
	OriginalServiceSessionLifecycle      bool
	AfterOriginalSessionExpiry           func(*acceptedCoreExecutionContract)
	BeforeServiceSession                 func(*acceptedCoreExecutionContract, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony, []byte)
	TransportNames                       func(deploymentConfig, string) map[string][]string
	Configure                            func(*deploymentConfig, *environmentCreatePlan, *environmentCreateBootstrapPlan)
	BeforeSource                         func(*acceptedCoreExecutionContract, *environmentCreateBootstrapRuntime)
}

func newCoreExecutionContractAtBoundary(t *testing.T, expired, listeners, beforeEnrollment bool, setup ...func(*environmentCreateBootstrapPlan)) *acceptedCoreExecutionContract {
	t.Helper()
	return newCoreExecutionContractAtBoundaryWithHooks(t, expired, listeners, beforeEnrollment, coreExecutionContractHooks{}, setup...)
}

func newCoreExecutionContractAtBoundaryWithHooks(t *testing.T, expired, listeners, beforeEnrollment bool, hooks coreExecutionContractHooks, setup ...func(*environmentCreateBootstrapPlan)) *acceptedCoreExecutionContract {
	t.Helper()
	ctx := context.Background()
	cfg, bootstrap, runtime, service := createServiceRecipeFixture(t, "dev")
	var workspaceErr error
	cfg.Workspace, workspaceErr = workspaceRoot()
	if workspaceErr != nil {
		t.Fatal(workspaceErr)
	}
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"layers":[]}`)
	image := "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + environmentPreflightBytesSHA256(manifest)
	bootstrap.Image, bootstrap.AccountManagerImage = image, image
	for role := range bootstrap.Images {
		bootstrap.Images[role] = image
	}
	var replaceImages func(any)
	replaceImages = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k == "image" {
					x[k] = image
				} else {
					replaceImages(v)
				}
			}
		case []any:
			for _, v := range x {
				replaceImages(v)
			}
		}
	}
	for i := range runtime.InitialListeners {
		replaceImages(runtime.InitialListeners[i].Object)
	}
	sourcePath := filepath.Join(cfg.Workspace, "repos/rtk_video_cloud/deploy/pki/render-staging-controller.py")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.Templates = map[string]string{"deploy/pki/render-staging-controller.py": environmentPreflightBytesSHA256(raw)}
	if len(setup) == 1 && setup[0] != nil {
		setup[0](&bootstrap)
	}
	cfg.Values["DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS"] = "3600"
	operator := map[string]string{"PKI_OPERATOR_USER_ID": "77777777-7777-4777-8777-777777777777", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@isolated.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-password"}
	cfg, store, e, _, original := providerExecutionContractFixture(t, providerExecutionContractInputs{
		cfg: cfg, operator: operator, bootstrap: bootstrap,
		prepare: func(selected *deploymentConfig, plan *environmentCreatePlan) []environmentReleasePublicationProof {
			originalImages := map[string]string{}
			for role, selected := range plan.Release.Images {
				originalImages[role] = selected.Reference
			}
			if hooks.Configure != nil {
				hooks.Configure(selected, plan, &plan.Bootstrap)
			}
			for role, v := range plan.Release.Images {
				// Keep an explicitly selected native role alias. The shared
				// default image must not erase a different producer contract.
				if v.Reference == originalImages[role] {
					v.Reference = image
				}
				if v.SourceRevision == "" {
					v.SourceRevision = plan.Release.WorkspaceRevision
				}
				plan.Release.Images[role] = v
			}
			return coreContractPublicationProofs(t, &plan.Release)
		},
	})
	bootstrap = original.Bootstrap
	// The outer original create artifact and every later scoped source bind
	// the same selected configuration/release before any owner is published.
	// Never repair provenance by rewriting a retained original artifact.
	if original.ConfigurationSHA256 != deploymentplan.Digest(cfg) || original.OperatorSHA256 != deploymentplan.Digest(operator) {
		t.Fatal("original selected inputs were not frozen before publication")
	}
	f := &acceptedCoreExecutionContract{cfg: cfg, store: store, e: e, operator: operator}
	f.native = &normalTailE2EBackend{captureJobLifecycle: true, defaultNativeServices: true, e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, routes: map[string]string{}}
	if err := e.RecordReceipt("activate-storage", mustProviderRecoveryJSON(map[string]string{"allocation": "isolated completed provider allocation"})); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry("activate-storage", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	runtime.Backend.Store, runtime.Execution, runtime.ExecutionSHA256 = store, e, e.Journal.PlanSHA256
	if listeners {
		for _, role := range []string{"management", "openbao"} {
			names := map[string][]string{role: {role + ".isolated.svc"}}
			if hooks.TransportNames != nil {
				names = hooks.TransportNames(cfg, role)
			}
			transport, err := createEnvironmentTransport(store, e.Journal.PlanSHA256, role, names, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			if role == "management" {
				ca, err := parseFirstCertificatePEM([]byte(transport.CACert))
				if err != nil {
					t.Fatal(err)
				}
				runtime.TransportCAPEM, runtime.TransportRootSHA256 = transport.CACert, environmentPreflightBytesSHA256(ca.Raw)
			}
		}
		runtime.TransportTrustConfigMap = "pki-create-transport-trust"
		if _, err := ensureEnvironmentCreateAccountSigner(store); err != nil {
			t.Fatal(err)
		}
		f.write(t, "inventory.json", secretInventory{SchemaVersion: secretInventorySchemaVersion, Environment: "dev"})
		f.write(t, "deployment/storage/create-allocation.json", map[string]string{"isolated": "original allocation"})
	}
	caLifetime := 4 * time.Hour
	if hooks.OriginalServiceCALifetime > 0 {
		caLifetime = hooks.OriginalServiceCALifetime
	}
	root, rootKey, rootPEM := coreContractCertificate(t, "isolated-service-root", true, nil, nil, caLifetime)
	intermediateLifetime := 4 * time.Hour
	if hooks.OriginalServiceCALifetime > 0 {
		intermediateLifetime = caLifetime - time.Hour
		if intermediateLifetime <= 0 {
			intermediateLifetime = caLifetime / 2
		}
	}
	intermediate, intermediateKey, intermediatePEM := coreContractCertificate(t, "isolated-service-intermediate", true, root, rootKey, intermediateLifetime)
	f.issuerKey = intermediateKey
	leaf, leafKey, leafPEM := coreContractCertificate(t, "service:deployment-bootstrap", false, intermediate, intermediateKey)
	if expired {
		leaf.NotBefore, leaf.NotAfter = time.Now().UTC().Add(-45*time.Minute), time.Now().UTC().Add(-time.Minute)
		der, err := x509.CreateCertificate(rand.Reader, leaf, intermediate, &leafKey.PublicKey, intermediateKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err = x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		leafPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	document := func(id, kind, cert, chain, provider string) map[string]any {
		return map[string]any{"issuer_id": id, "issuer_version": float64(1), "environment": "dev", "trust_domain": "service", "kind": kind, "status": "active", "signer_provider": provider, "signer_reference": "isolated-issuer/1", "certificate_pem": cert, "chain_pem": chain, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(root.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(chain))}
	}
	rootDoc := document(service.Root.ID, "root", rootPEM, rootPEM, "offline")
	intermediateDoc := document(service.Intermediate.ID, "intermediate", intermediatePEM, intermediatePEM+rootPEM, "openbao")
	intermediateDoc["parent_issuer_id"] = service.Root.ID
	service.Root, err = environmentCreateRegisteredIssuerFromMap(rootDoc, "")
	if err != nil {
		t.Fatal(err)
	}
	service.Intermediate, err = environmentCreateRegisteredIssuerFromMap(intermediateDoc, service.Root.RootFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	f.key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	f.certificate = leafPEM + intermediatePEM + rootPEM
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, leafKey)
	if err != nil {
		t.Fatal(err)
	}
	f.csr = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		t.Fatal(err)
	}
	namespaces := []string{bootstrap.Stack + "-video-cloud", bootstrap.Stack + "-account-manager", bootstrap.Stack + "-secrets"}
	oldSession := "33333333-3333-4333-8333-333333333333"
	originalChild := deploymentplan.Digest("isolated-original-expired-enrollment")
	f.session = sessionRenewalUUID(e.Journal.PlanSHA256, originalChild, oldSession)
	service.BootstrapSessionID, service.BootstrapCredentialSecret, service.BootstrapClaim = f.session, "service-bootstrap-"+f.session, "pki-service-bootstrap-state"
	service.BootstrapCredentialNamespace, service.BootstrapCredentialNamespaces, service.CustodyReader = namespaces[0], namespaces, "pki-bootstrap-custody-"+e.Journal.PlanSHA256[:12]
	f.p = createFoundationRecoveryPlan{ParentSHA: e.Journal.PlanSHA256, Plan: original, Materials: map[string]string{}}
	f.service = service
	for i, namespace := range namespaces {
		f.native.objects["Namespace//"+namespace] = deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace, "uid": fmt.Sprintf("%08d-4444-4444-8444-444444444444", i+1), "labels": deploymentplan.Object{"rtk.realtek.com/stack": bootstrap.Stack}}}
	}
	if hooks.BeforeSource != nil {
		hooks.BeforeSource(f, &runtime)
	}
	sourcePlan := createFoundationRecoveryPlan{ParentSHA: e.Journal.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(e.owner), ParentJournalSHA: deploymentplan.Digest(e.Journal), Plan: f.p.Plan, Materials: map[string]string{"original-service-session-custody-bootstrap.key": environmentPreflightBytesSHA256([]byte(f.key)), "original-service-session-custody-bootstrap.csr": environmentPreflightBytesSHA256([]byte(f.csr))}}
	f.review = deploymentplan.Digest(sourcePlan)
	qualification := deploymentplan.Qualification{PlanSHA256: f.review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		qualification.Gates = append(qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: f.review})
	}
	source := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: sourcePlan, Qualification: qualification, QualifiedAt: time.Now().UTC(), ValidUntil: time.Now().UTC().Add(time.Hour)}
	f.write(t, filepath.Join("deployment/create/openbao-recovery", f.review, "qualification.json"), source)
	rootOperation := environmentCreateIssuerOperation{ID: "66666666-6666-4666-8666-666666666666", IssuerID: service.Root.ID, Status: "requested", Digest: deploymentplan.Digest(environmentCreateIssuerRequest{Environment: "dev", Domain: "service", Kind: "root"})}
	f.record(t, "pki-reserve-service-root", rootOperation, rootOperation)
	for _, stage := range []string{"authorize", "provision", "import"} {
		f.record(t, "pki-"+stage+"-"+rootOperation.ID, rootOperation, map[string]string{"operation_id": rootOperation.ID, "issuer_id": service.Root.ID, "status": stage})
	}
	base := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	ready := deploymentplan.Clone(rootDoc)
	ready["status"] = "ready"
	f.write(t, filepath.Join(base, "ready.json"), ready)
	f.write(t, filepath.Join(base, "root-active.json"), rootDoc)
	f.record(t, "pki-activate-"+rootOperation.ID, rootDoc, rootDoc)
	if hooks.OriginalServiceIntermediateLifecycle {
		publishHistoricalOriginalServiceIntermediate(t, f, service)
	} else {
		f.record(t, "pki-activate-88888888-8888-4888-8888-888888888888", intermediateDoc, intermediateDoc)
	}
	f.write(t, filepath.Join(base, "intermediate-active.json"), intermediateDoc)
	custodyPaths := map[string]string{"key": filepath.Join("pki/custody/roots", service.Root.ID, "ca-key.encrypted.pem"), "certificate": filepath.Join("pki/custody/roots", service.Root.ID, "certificate.pem"), "csr": filepath.Join("pki/custody/roots", service.Root.ID, "csr.pem"), "passphrase": "pki/custody/service-root-v1-key-passphrase"}
	rootKeyDER, err := x509.MarshalECPrivateKey(rootKey)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := x509.EncryptPEMBlock(rand.Reader, "EC PRIVATE KEY", rootKeyDER, []byte("isolated-passphrase"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	rootCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: root.Subject}, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	custody := map[string]string{"key": string(pem.EncodeToMemory(encrypted)), "certificate": rootPEM, "csr": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: rootCSR})), "passphrase": "isolated-passphrase"}
	hashes := map[string]string{}
	for k, path := range custodyPaths {
		if err := store.write(path, []byte(custody[k]), false); err != nil {
			t.Fatal(err)
		}
		hashes[k] = environmentPreflightBytesSHA256([]byte(custody[k]))
	}
	f.write(t, filepath.Join("pki/custody/roots", service.Root.ID, "manifest.json"), map[string]any{"schema": "rtk-root-custody/v1", "environment": "dev", "trust_domain": "service", "root_issuer_id": service.Root.ID, "certificate_fingerprint_sha256": service.Root.RootFingerprint, "operator_user_id": f.operator["PKI_OPERATOR_USER_ID"], "ceremony_operation_id": rootOperation.ID, "paths": custodyPaths, "file_sha256": hashes})
	crlLifetime := time.Hour
	if hooks.OriginalServiceCRLLifetime != 0 {
		crlLifetime = hooks.OriginalServiceCRLLifetime
		if crlLifetime <= 0 {
			t.Fatal("original CRL lifetime must be selected before signing")
		}
	}
	crlDER, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().UTC().Add(-time.Minute), NextUpdate: time.Now().UTC().Add(crlLifetime)}, root, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	service.RootCRLPEM = string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crlDER}))
	service.RootCRLFile = filepath.Join(base, "root.crl.pem")
	if err := store.write(service.RootCRLFile, []byte(service.RootCRLPEM), false); err != nil {
		t.Fatal(err)
	}
	f.record(t, "pki-initial-root-crl-service", map[string]string{"issuer_id": service.Root.ID}, map[string]string{"issuer_id": service.Root.ID, "crl_file": service.RootCRLFile, "crl_pem_sha256": environmentPreflightBytesSHA256([]byte(service.RootCRLPEM))})
	// Publish the original signed CRL through its maintained Secret stage,
	// before any renewal source can capture this history.
	f.stage(t, "kubernetes/service-active-trust", []deploymentplan.Definition{environmentCreateSecret(bootstrap.Stack+"-video-cloud", service.RootCRLSecret, map[string]string{"revocations.pem": service.RootCRLPEM})})
	accountID := "Deployment/" + namespaces[1] + "/account-manager"
	accountUID := "55555555-5555-4555-8555-555555555555"
	accountDefinitions, err := environmentCreateServiceAccountDefinitions(cfg, bootstrap, runtime)
	if err != nil {
		t.Fatal(err)
	}
	accountObject := deploymentplan.Clone(createRecipeObject(t, accountDefinitions, accountID))
	accountMetadata := deploymentplan.Map(accountObject["metadata"])
	accountMetadata["uid"], accountMetadata["resourceVersion"] = accountUID, "1"
	accountMetadata["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": bootstrap.Stack}
	f.native.objects[accountID] = accountObject
	f.record(t, "kubernetes/account-bootstrap", map[string]string{"original": "account owner"}, map[string]any{"owners": map[string]string{accountID: accountUID}})
	binding := initialOperatorBinding{Schema: "rtk-cloud.initial-operator-binding/v1", Parent: e.Journal.PlanSHA256, UserID: f.operator["PKI_OPERATOR_USER_ID"], AccountUID: accountUID, EmailSHA: environmentPreflightBytesSHA256([]byte(f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"])), OriginalConfiguredSHA: environmentPreflightBytesSHA256([]byte(f.operator["PKI_OPERATOR_USER_ID"]))}
	if err := recordInitialOperatorBinding(ctx, store, e, binding); err != nil {
		t.Fatal(err)
	}
	if listeners {
		// Freeze the original listeners from the same operator and release that
		// later source qualification independently renders.
		runtime.InitialListeners, err = renderEnvironmentCreateInitialPKIListeners(cfg, f.p.Plan.Release, f.operator["PKI_OPERATOR_USER_ID"], f.operator)
		if err != nil {
			t.Fatal(err)
		}
		f.runtime.InitialListeners = runtime.InitialListeners
		original, _ := f.interruptedStage(t, "kubernetes/pki-initial-listeners", runtime.InitialListeners, "Deployment/"+bootstrap.Stack+"-video-cloud/pki-controller")
		stage := "kubernetes/pki-initial-listeners-retained-" + original.SHA256()
		f.stage(t, stage, runtime.InitialListeners)
		if err := e.RecordReceipt("kubernetes/pki-initial-listeners", mustProviderRecoveryJSON(map[string]string{"original_plan_sha256": original.SHA256(), "actual_retained_verification_stage": stage})); err != nil {
			t.Fatal(err)
		}
		if err := e.CompleteEntry("kubernetes/pki-initial-listeners", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	// A separate pre-issuance contract may stop here, before any session
	// intent, native bootstrap credential or credential owner exists.
	if hooks.BeforeServiceSession != nil {
		hooks.BeforeServiceSession(f, runtime, service, manifest)
		return f
	}
	if hooks.OriginalServiceSessionLifecycle {
		if !listeners || beforeEnrollment {
			t.Fatal("original session lifecycle requires the full original listener boundary")
		}
		return f.completeOriginalSessionPrefix(t, runtime, service, hooks, manifest, expired)
	}
	policy := "path \"pki/isolated/sign/service-client\" { capabilities = [\"update\"] }\n"
	policyName := "rtk-dev-service-bootstrap-" + f.session
	policyReceipt := deploymentplan.Object{"environment": "dev", "policy": policyName, "policy_sha256": environmentPreflightBytesSHA256([]byte(policy))}
	f.record(t, "openbao-policy-"+policyName, policyReceipt, policyReceipt)
	role := "pki-service-bootstrap-dev"
	roleIntent := deploymentplan.Object{"role": role, "namespace": namespaces[0], "service_accounts": []string{"pki-service-bootstrap"}, "policies": []string{policyName}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	f.record(t, "openbao-role-"+role+"-"+deploymentplan.Digest(roleIntent)[:12], roleIntent, roleIntent)
	if _, err := os.Lstat(filepath.Join(store.Root, "openbao/root-token")); os.IsNotExist(err) {
		if err := store.write("openbao/root-token", []byte("isolated-original-bao-token"), false); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	} else if raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/root-token"), true); err != nil || strings.TrimSpace(string(raw)) == "" {
		t.Fatal("original initialized OpenBao identity is unavailable", err)
	}
	intent := createServiceSessionIntent{SessionID: oldSession, Claim: service.BootstrapClaim, Subjects: subjects, IssuerID: service.Intermediate.ID, RootSHA: service.Root.RootFingerprint, Namespaces: namespaces}
	f.write(t, filepath.Join(base, "session-intent.json"), intent)
	defs, err := renderEnvironmentCreatePKIPhase(ctx, cfg, bootstrap, "service-bootstrap", "--openbao-ca-configmap", "pki-openbao-transport-ca", "--service-bootstrap-subject", "service:deployment-bootstrap", "--service-bootstrap-subjects", strings.Join(subjects, ","), "--service-bootstrap-issuer-id", service.Intermediate.ID, "--service-bootstrap-issuer-mount", service.Intermediate.Reference, "--service-bootstrap-root-sha256", service.Root.RootFingerprint, "--service-bootstrap-deployment-id", "create-"+e.Journal.PlanSHA256[:32], "--service-bootstrap-session-id", oldSession, "--service-bootstrap-state-claim", service.BootstrapClaim)
	if err != nil {
		t.Fatal(err)
	}
	var renewed deploymentplan.Definition
	for _, d := range defs {
		if d.Object["kind"] == "Job" {
			renewed, err = originalKeyRenewalDefinition(d.Object, e.Journal.PlanSHA256, originalChild, f.session)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if renewed.Object == nil {
		t.Fatal("native bootstrap renderer omitted Job")
	}
	if beforeEnrollment {
		// Keep the original native bootstrap recipe, with its original key/CSR
		// paths, for the earlier expired-before-server-enrollment boundary.
		for _, d := range defs {
			if d.Object["kind"] == "Job" {
				renewed = deploymentplan.Definition{Phase: d.Phase, Object: deploymentplan.Clone(d.Object)}
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(renewed.Object)["containers"])[0])
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": f.session}))
			}
		}
		f.stage(t, "kubernetes/service-session-order-recovery-"+f.review, []deploymentplan.Definition{renewed})
	} else {
		f.stage(t, "kubernetes/service-session-original-key-renewal-"+f.review, []deploymentplan.Definition{renewed})
	}
	reader := renderEnvironmentCreateCustodyReader(cfg, bootstrap, service)
	renewedCertPath := "/run/pki-bootstrap/renew-" + originalChild[:12] + "/bootstrap.crt"
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(reader.Object)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": renewedCertPath}))
	f.stage(t, "kubernetes/service-custody-reader-renewal-"+f.review, []deploymentplan.Definition{reader})
	var credentials []deploymentplan.Definition
	for _, ns := range namespaces {
		d := environmentCreateSecret(ns, service.BootstrapCredentialSecret, map[string]string{"tls.crt": f.certificate, "tls.key": f.key, "ca.crt": rootPEM})
		d.Object["immutable"] = true
		deploymentplan.Map(d.Object["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": bootstrap.Stack, "rtk.cloud/bootstrap-session": f.session}
		credentials = append(credentials, d)
	}
	serviceManifest, err := environmentCreateServiceManifest(service.Root, service.Intermediate)
	if err != nil {
		t.Fatal(err)
	}
	for _, ns := range namespaces[1:] {
		credentials = append(credentials, f.originalVerifierDefinition(t, ns), environmentCreateServiceTrustDefinition(ns, service.Root, runtime.TransportCAPEM, serviceManifest))
	}
	f.stage(t, "kubernetes/service-credential-secrets-renewal-"+f.review, credentials)
	if !beforeEnrollment {
		defs, err = renderEnvironmentCreateServiceIssuerDefinitions(ctx, cfg, bootstrap, runtime, service)
		if err != nil {
			t.Fatal(err)
		}
		f.stage(t, "kubernetes/service-enrollment-issuer-renewal-"+f.review, defs)
		defs, err = renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(ctx, cfg, bootstrap, runtime, service)
		if err != nil {
			t.Fatal(err)
		}
		forwardKey := "kubernetes/service-issuer-server-enrollment-renewal-" + f.review
		f.stage(t, forwardKey, defs)
		f.record(t, "kubernetes/service-issuer-server-enrollment", map[string]string{"original_failed_plan_sha256": originalChild}, map[string]string{"actual_qualified_forward_stage": forwardKey, "original_failed_plan_sha256": originalChild, "renewed_session_id": f.session})
		defs, err = renderEnvironmentCreateManagedServiceIssuerDefinitions(ctx, cfg, bootstrap, runtime, service)
		if err != nil {
			t.Fatal(err)
		}
		f.stage(t, "kubernetes/service-managed-enrollment-issuer", defs)
		defs, err = renderEnvironmentCreateServiceEnrollmentDefinitions(ctx, cfg, bootstrap, runtime, service)
		if err != nil {
			t.Fatal(err)
		}
		f.stage(t, "kubernetes/service-core-enrollment", defs)
	}
	uids := map[string]string{}
	for _, ns := range namespaces {
		uids[ns] = deploymentplan.String(deploymentplan.Map(f.native.objects["Secret/"+ns+"/"+service.BootstrapCredentialSecret]["metadata"])["uid"])
	}
	kubeCA := base64.StdEncoding.EncodeToString([]byte(runtime.TransportCAPEM))
	service.CleanupBinding = environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: f.session, NamespaceUID: deploymentplan.String(deploymentplan.Map(f.native.objects["Namespace//"+namespaces[0]]["metadata"])["uid"]), SecretUID: uids[namespaces[0]], PolicySHA256: deploymentplan.Digest(roleIntent), KubernetesCA: kubeCA}
	observedSession := managedSessionState{Session: f.session, Issuer: service.Intermediate.ID, Root: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Status: "active", Expires: leaf.NotAfter, Subjects: append([]string(nil), subjects...), Installed: []string{}}
	f.write(t, filepath.Join(base, "session-renewal", f.review, "observed.json"), createServiceSessionObserved{Cleanup: service.CleanupBinding, UIDs: uids, Reader: service.CustodyReader, Session: mustProviderRecoveryJSON(observedSession)})
	f.service, f.runtime = service, runtime
	f.backend = environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("clusters:\n- cluster:\n    certificate-authority-data: " + kubeCA + "\n")}
	// The selected backend and the durable environment path are one identity.
	// Later recovery admission intentionally reloads the canonical file instead
	// of trusting this in-memory handle, so publish the fixture's selection at
	// the same boundary where it becomes active.
	if err := store.write("kube/kubeconfig.yaml", f.backend.KubeconfigContent, true); err != nil {
		t.Fatal("persist selected fixture kubeconfig", err)
	}
	if cfg.Values[deploymentRolloutTimeoutKey] != "" {
		f.backend.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
		if err != nil {
			t.Fatal("original selected native rollout timeout", err)
		}
	}
	f.protocol = map[string]any{"objects": coreContractNativeInventory(f.native.objects), "operator": binding.UserID, "email": f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], "password": f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"], "issuers": map[string]any{service.Root.ID: rootDoc, service.Intermediate.ID: intermediateDoc}, "policy": environmentCreateOpenBaoIssuerPolicies{Environment: "dev", IssuerID: service.Intermediate.ID, ServiceClientSignerPolicy: policy}, "role": map[string]any{"data": map[string]any{"bound_service_account_names": []string{"pki-service-bootstrap"}, "bound_service_account_namespaces": []string{namespaces[0]}, "token_policies": []string{policyName}, "audience": "openbao", "token_ttl": 900, "token_max_ttl": 900, "token_no_default_policy": true}}, "session": map[string]any{"session_id": f.session, "issuer_id": service.Intermediate.ID, "root": service.Root.RootFingerprint, "fingerprint": environmentPreflightBytesSHA256(leaf.Raw), "status": "active", "subjects": subjects, "installed": []string{}, "expires": leaf.NotAfter, "other_active": 0}, "custody": map[string]string{"/run/pki-bootstrap/bootstrap.key": f.key, "/run/pki-bootstrap/bootstrap.csr": f.csr, renewedCertPath: f.certificate}}
	if hooks.ImageManifests != nil {
		images := map[string]string{}
		for reference, body := range hooks.ImageManifests {
			images[reference] = body
		}
		f.protocol["image_manifests"] = images
	}
	if expired {
		f.protocol["session"].(map[string]any)["status"] = "expired"
	}
	f.preserveHistoricalIssuedSession(t)
	f.installProtocol(t, manifest)
	if err := ensurePrivateDirectory(filepath.Join(store.Root, "deployment/tmp")); err != nil {
		t.Fatal(err)
	}
	return f
}

// Controller-owned observations have independent native UUIDs. A rollout gets
// new ReplicaSet/Pod identities while retaining the original controller owner.
func coreContractObserverUUID(controller, kind string, template any, ordinal int) string {
	sha := deploymentplan.Digest([]any{controller, kind, template, ordinal})
	return sha[:8] + "-" + sha[8:12] + "-4" + sha[13:16] + "-8" + sha[17:20] + "-" + sha[20:32]
}

func coreContractNativeInventory(original map[string]deploymentplan.Object) map[string]deploymentplan.Object {
	out := map[string]deploymentplan.Object{}
	for id, v := range original {
		out[id] = deploymentplan.Clone(v)
	}
	for id, v := range original {
		actual := out[id]
		kind := deploymentplan.String(v["kind"])
		if kind == "PersistentVolumeClaim" {
			actual["status"] = deploymentplan.Object{"phase": "Bound"}
			continue
		}
		if kind != "Deployment" && kind != "Job" {
			continue
		}
		jobStatus := deploymentplan.Map(v["status"])
		capturedJob := kind == "Job" && jobStatus["startTime"] != nil
		completedJob := capturedJob && jobStatus["completionTime"] != nil && jobStatus["succeeded"] == float64(1)
		podSpec := environmentPKIPod(v)
		if len(podSpec) == 0 {
			continue
		}
		meta := deploymentplan.Map(actual["metadata"])
		meta["generation"] = float64(1)
		name, ns, uid := deploymentplan.String(meta["name"]), deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["uid"])
		actual["status"] = deploymentplan.Object{"observedGeneration": float64(1), "replicas": float64(1), "readyReplicas": float64(1), "updatedReplicas": float64(1), "availableReplicas": float64(1), "unavailableReplicas": float64(0)}
		ref := func(k, n, u string) []any {
			api := "apps/v1"
			if k == "Job" {
				api = "batch/v1"
			}
			return []any{deploymentplan.Object{"apiVersion": api, "kind": k, "name": n, "uid": u, "controller": true}}
		}
		owner := ref(kind, name, uid)
		phase := "Running"
		if kind == "Deployment" {
			rsName := name + "-isolated"
			rsUID := coreContractObserverUUID(uid, "ReplicaSet", deploymentplan.Map(actual["spec"])["template"], 0)
			rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"namespace": ns, "name": rsName, "uid": rsUID, "ownerReferences": owner}, "spec": deploymentplan.Object{"template": deploymentplan.Map(actual["spec"])["template"]}}
			out[deploymentplan.ID(rs)] = rs
			owner = ref("ReplicaSet", rsName, rsUID)
		} else {
			phase = "Succeeded"
			actual["status"] = deploymentplan.Object{"succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
			if capturedJob {
				actual["status"] = deploymentplan.Clone(jobStatus)
				if !completedJob {
					phase = "Pending"
				}
			}
		}
		pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": name + "-pod", "uid": coreContractObserverUUID(uid, "Pod", deploymentplan.Map(actual["spec"])["template"], 0), "ownerReferences": owner}, "spec": deploymentplan.Clone(podSpec), "status": deploymentplan.Object{"phase": phase, "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}}
		templateMetadata := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(actual["spec"])["template"])["metadata"])
		if labels := deploymentplan.Map(templateMetadata["labels"]); len(labels) != 0 {
			deploymentplan.Map(pod["metadata"])["labels"] = deploymentplan.Clone(labels)
		}
		for _, group := range []string{"containers", "initContainers"} {
			var statuses []any
			for _, value := range certIssuerObjectList(podSpec[group]) {
				c := deploymentplan.Map(value)
				terminal := group == "initContainers" || kind == "Job"
				state := deploymentplan.Object{"running": deploymentplan.Object{"startedAt": "2026-01-01T00:00:00Z"}}
				if terminal {
					state = deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0)}}
				}
				if capturedJob {
					state = deploymentplan.Object{"waiting": deploymentplan.Object{"reason": "NativeWaitPending"}}
					if completedJob {
						state = deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "reason": "Completed", "containerID": "containerd://isolated-" + deploymentplan.String(c["name"]), "startedAt": jobStatus["startTime"], "finishedAt": jobStatus["completionTime"]}}
					}
				}
				statuses = append(statuses, deploymentplan.Object{"restartCount": float64(0), "name": c["name"], "ready": !terminal, "started": !terminal, "containerID": "containerd://isolated-" + deploymentplan.String(c["name"]), "imageID": "docker-pullable://" + deploymentplan.String(c["image"]), "state": state})
			}
			field := "containerStatuses"
			if group == "initContainers" {
				field = "initContainerStatuses"
			}
			deploymentplan.Map(pod["status"])[field] = statuses
		}
		out[deploymentplan.ID(pod)] = pod
	}
	return out
}

func (f *acceptedCoreExecutionContract) installProtocol(t *testing.T, manifest []byte) {
	t.Helper()
	dir := t.TempDir()
	f.protocolPath = filepath.Join(dir, "native.json")
	f.publish(t)
	script := `#!/usr/bin/env python3
import json,os,sys,http.server
a=sys.argv[1:]
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as r:d=json.load(r)
if 'port-forward' in a:
 port=int(a[-1].split(':')[0])
 class Handler(http.server.BaseHTTPRequestHandler):
  def log_message(self,*args): pass
  def respond(self,v,status=200):
   raw=json.dumps(v).encode();self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
  def do_POST(self):
   body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
   if self.path!='/v1/auth/login' or body!={'email':d['email'],'password':d['password']}:self.respond({},403);return
   self.respond({'user':{'id':d['operator'],'email':d['email']},'tokens':{'access_token':'isolated-native-session'}})
  def do_GET(self):
   key=self.path.split('/')[-1]
   if self.headers.get('Authorization')!='Bearer isolated-native-session' or key not in d['issuers']:self.respond({},403);return
   self.respond(d['issuers'][key])
 server=http.server.HTTPServer(('127.0.0.1',port),Handler)
 print('Forwarding from 127.0.0.1:%d -> 8080'%port,flush=True);server.serve_forever()
if 'get' in a:
 i=a.index('get');kind=a[i+1];ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 if kind=='pods' and '-l' in a:
  key,value=a[a.index('-l')+1].split('=',1)
  print(json.dumps({'items':[v for v in d['objects'].values() if v['kind']=='Pod' and v.get('metadata',{}).get('namespace')==ns and v.get('metadata',{}).get('labels',{}).get(key)==value]}));sys.exit()
 if kind=='namespace':print(json.dumps({'items':[v for v in d['objects'].values() if v['kind']=='Namespace']}));sys.exit()
 requested=set(kind.lower().split(','))
 cluster={'clusterrole','clusterrolebinding','ingressclass','validatingwebhookconfiguration'}
 listing=',' in kind or i+2>=len(a) or a[i+2].startswith('-')
 if listing and (',' in kind or requested.issubset(cluster)):
  selectors={}
  if '-l' in a:
   for selector in a[a.index('-l')+1].split(','):
    if selector.count('=')!=1:sys.exit('unsupported exact native selector')
    key,value=selector.split('=',1);selectors[key]=value
  rows=[v for v in d['objects'].values() if v['kind'].lower() in requested and (v.get('metadata',{}).get('namespace')==ns if ns else not v.get('metadata',{}).get('namespace')) and all(v.get('metadata',{}).get('labels',{}).get(k)==value for k,value in selectors.items())]
  print(json.dumps({'apiVersion':'v1','kind':'List','metadata':{},'items':rows}));sys.exit()
 name=a[i+2];v=d['objects'].get(kind+'/'+ns+'/'+name)
 if v is not None:print(json.dumps(v))
 sys.exit()
joined=' '.join(a)
if 'SELECT id FROM users' in joined:print(d['operator'])
elif 'SELECT (SELECT count(*) FROM pki_issuers)' in joined:print('2|2')
elif 'registry-read' in a and 'json_agg(subject' in joined and 'pki_service_client_issuances' in joined:print(json.dumps(d['issued']))
elif 'registry-read' in a:print(json.dumps(d['session']))
elif 'logs' in a and a[a.index('logs')+1] in d.get('logs',{}):print(d['logs'][a[a.index('logs')+1]],end='')
elif 'render-openbao-policy' in a:print(json.dumps(d['policy']))
elif 'bao policy read' in joined:print(d['policy']['service_client_signer_policy'],end='')
elif 'bao read -format=json auth/kubernetes/role/' in joined:print(json.dumps(d['role']))
elif 'cat' in a and a[-1] in d['custody']:print(d['custody'][a[-1]],end='')
else:sys.exit('unhandled isolated native request: '+joined)
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	docker := `#!/usr/bin/env python3
import json,os,sys
if sys.argv[1:2]==['info']:print(json.dumps([{'Name':'buildx','Path':os.path.abspath(sys.argv[0])}]))
elif sys.argv[1:2]==['login']:
 if sys.stdin.read().strip()!='isolated-fake-gh-only':sys.exit('unexpected isolated registry credential')
 print('Login Succeeded')
else:
 with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as f:d=json.load(f)
 manifest=d.get('image_manifests',{}).get(sys.argv[-1])
 if manifest is not None:sys.stdout.write(manifest)
 else:
  with open(os.path.join(os.path.dirname(sys.argv[0]),'manifest.json'),'rb') as f:sys.stdout.buffer.write(f.read())
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("RTK_CORE_CONTRACT_NATIVE", f.protocolPath)
}

func TestAcceptedCoreExecutionContractLoadsOriginalSameKeyLineageWithoutWrites(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	var observed createServiceSessionObserved
	var saved managedSessionState
	observedPath := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "observed.json")
	if err := readExecutionJSON(observedPath, &observed); err != nil || decodeEnvironmentJSON(observed.Session, &saved) != nil || saved.Session != f.session || saved.Issuer != f.service.Intermediate.ID || saved.Root != f.service.Root.RootFingerprint || saved.Status != "active" || len(saved.Subjects) != 9 || len(saved.Installed) != 0 {
		t.Fatal("original renewal observation omitted its exact native session", err)
	}
	// Reopen the earliest immutable create artifact independently. The later
	// recovery recipe must not silently select another configuration/release.
	original, err := loadEnvironmentCreateArtifact(f.cfg, deploymentplan.Digest(f.operator), f.e.Journal.PlanSHA256, time.Now().UTC())
	var initial environmentCreatePlan
	if err != nil || decodeEnvironmentJSON(original.Plan, &initial) != nil || deploymentplan.Digest(initial) != deploymentplan.Digest(f.p.Plan) {
		t.Fatal("earliest create artifact differs from original selected recovery inputs", err)
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	r, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p)
	if err != nil {
		t.Fatal("complete rendered and accepted original Service core rejected", err)
	}
	if !r.CoreAccepted || r.RenewedIntent.SessionID != f.session || r.OriginalKeySHA != environmentPreflightBytesSHA256([]byte(f.key)) || r.OriginalCSRSHA != environmentPreflightBytesSHA256([]byte(f.csr)) || r.Ceremony.Root.RootFingerprint != f.service.Root.RootFingerprint || p.CheckpointPlanSHA == "" || p.CheckpointJournalSHA == "" {
		t.Fatal("accepted Service history lost original issuer, key, CSR or final accepted stage")
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	if !reflect.DeepEqual(before, after) {
		for path, value := range after {
			if before[path] != value {
				t.Errorf("accepted history loader changed path %s", path)
			}
		}
		for path := range before {
			if _, ok := after[path]; !ok {
				t.Errorf("accepted history loader removed path %s", path)
			}
		}
		t.Fatal("accepted history loader changed original private state")
	}
}

func TestAcceptedCoreExecutionContractRejectsChangedAncestryAndNativeCustody(t *testing.T) {
	for _, mutation := range []string{"qualified-gate", "child-acceptance", "native-owner", "root-custody", "original-csr", "concurrent-session", "exclusive-role"} {
		t.Run(mutation, func(t *testing.T) {
			f := newAcceptedCoreExecutionContract(t)
			switch mutation {
			case "qualified-gate":
				path := filepath.Join("deployment/create/openbao-recovery", f.review, "qualification.json")
				var a createFoundationRecoveryArtifact
				if err := readExecutionJSON(filepath.Join(f.store.Root, path), &a); err != nil {
					t.Fatal(err)
				}
				a.Qualification.Gates[0].Status = "FAIL"
				f.write(t, path, a)
			case "child-acceptance":
				_, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/service-core-enrollment")
				if err != nil {
					t.Fatal(err)
				}
				j.Accepted = false
				f.write(t, filepath.Join("deployment/rollback", j.PlanSHA256, "journal.json"), j)
			case "native-owner":
				objects := f.protocol["objects"].(map[string]deploymentplan.Object)
				meta := deploymentplan.Map(objects["Deployment/"+f.p.Plan.Stack+"-video-cloud/certissuer"]["metadata"])
				meta["uid"] = "99999999-9999-4999-8999-999999999999"
			case "root-custody":
				if err := f.store.write(filepath.Join("pki/custody/roots", f.service.Root.ID, "certificate.pem"), []byte(f.service.Intermediate.Certificate), true); err != nil {
					t.Fatal(err)
				}
			case "original-csr":
				f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.csr"] = "a different original CSR"
			case "concurrent-session":
				f.protocol["session"].(map[string]any)["other_active"] = 1
			case "exclusive-role":
				f.protocol["role"].(map[string]any)["data"].(map[string]any)["token_no_default_policy"] = false
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			if _, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p); err == nil {
				t.Fatal("accepted changed original history or native custody", mutation)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
