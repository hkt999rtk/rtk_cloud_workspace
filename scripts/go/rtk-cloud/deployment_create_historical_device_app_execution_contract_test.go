package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Select the maintained complete policy before the initial immutable source is
// frozen. Only the documented original bare OpenBao alias is restored here: its
// later native refusal and explicit DNS repair are part of this history.
func historicalCompleteDomainPolicy(t *testing.T) func(*environmentCreateBootstrapPlan) {
	t.Helper()
	values, _, release := completeEnvironmentRenderFixture("dev")
	values["PKI_REQUIRED_CONSUMERS_SERVICE"] = "account-manager,certissuer,factory-enroll,pki-controller,video-cloud-api,video-cloud-logingester"
	values["PKI_REQUIRED_BUNDLE_CONSUMERS_SERVICE"] = "account-manager,certissuer,pki-controller"
	values["PKI_REQUIRED_CONSUMERS_DEVICE"] = "pkibroker,video-cloud-api"
	values["PKI_REQUIRED_BUNDLE_CONSUMERS_DEVICE"] = "pkibroker,video-cloud-api"
	values["PKI_REQUIRED_BUNDLE_CONSUMERS_APP"] = "pkibroker,video-cloud-api"
	values["PKI_REQUIRED_BUNDLE_CONSUMERS_MQTT"] = "video-cloud-api,video-cloud-logingester"
	values["PKI_REQUIRED_BUNDLE_CONSUMERS_OPENBAO_TLS"] = "certissuer,pki-controller"
	cfg := deploymentConfig{Environment: "dev", Adapter: "lke", Values: values}
	var err error
	cfg.Workspace, err = workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := buildEnvironmentCreateBootstrapPlan(context.Background(), cfg, release, makeIsolatedTestSecretStore(t, "dev"), nil)
	if err != nil {
		t.Fatal("complete historical domain policy", err)
	}
	for i := range policy.Domains {
		if policy.Domains[i].Domain == "openbao_tls" {
			policy.Domains[i].ServerDNSNames = append(policy.Domains[i].ServerDNSNames, "openbao")
			slices.Sort(policy.Domains[i].ServerDNSNames)
		}
	}
	return func(p *environmentCreateBootstrapPlan) {
		p.Domains = policy.Domains
		p.Claims = policy.Claims
		p.Templates = policy.Templates
		p.AppRoutingMode = policy.AppRoutingMode
		p.PublicCertIssuerHost = policy.PublicCertIssuerHost
	}
}

func historicalIntermediateID(root environmentCreateRegisteredIssuer, ordinal int) string {
	if root.ID == "734487b8-abcf-40be-a813-8b52c64f7954" {
		return openBaoTLSOriginalIntermediateID
	}
	return fmt.Sprintf("%08d-3333-4333-8333-333333333333", ordinal)
}

// Activate the same previously issued READY root and sign its intermediate with
// the original test-held root key. Native documents, custody, operation intents
// and parent records are all bound to those actual certificates; no alternate
// authority is introduced when the next historical stage is constructed.
func (f *acceptedCoreExecutionContract) activateHistoricalTrustAuthority(t *testing.T, root environmentCreateRegisteredIssuer, ordinal int, keys map[string]*ecdsa.PrivateKey) (environmentCreateRegisteredIssuer, environmentCreateRegisteredIssuer) {
	t.Helper()
	key := keys[root.ID]
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil || key == nil || root.Status != "ready" && root.Status != "active" {
		t.Fatal("original READY authority/key unavailable", root.Domain, err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil || string(public) != string(cert.RawSubjectPublicKeyInfo) {
		t.Fatal("held signer differs from original READY authority")
	}
	base := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap", root.Domain)
	if root.Status == "ready" {
		root = f.activateHistoricalReadyRoot(t, root, ordinal)
	}
	issuers := f.protocol["issuers"].(map[string]any)
	intermediate, intermediateKey, intermediatePEM := coreContractCertificate(t, "isolated-"+root.Domain+"-intermediate", true, cert, key, 7*24*time.Hour)
	id := historicalIntermediateID(root, ordinal)
	operationID := fmt.Sprintf("%08d-4444-4444-8444-444444444444", ordinal)
	keys[id] = intermediateKey
	chain := intermediatePEM + root.Certificate
	idoc := deploymentplan.Object{"issuer_id": id, "issuer_version": float64(1), "environment": f.cfg.Environment, "trust_domain": root.Domain, "kind": "intermediate", "status": "ready", "signer_provider": "openbao", "signer_reference": "pki-issuers/" + root.Domain + "/" + id + "/v1", "parent_issuer_id": root.ID, "certificate_pem": intermediatePEM, "chain_pem": chain, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(intermediate.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(chain))}
	policy := findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, root.Domain)
	operation := environmentCreateIssuerOperation{ID: operationID, IssuerID: id, Status: "requested", Digest: deploymentplan.Digest(environmentCreateIssuerRequest{Environment: f.cfg.Environment, Domain: root.Domain, Kind: "intermediate", ParentID: root.ID, ServerDNSNames: policy.ServerDNSNames, ServiceClientIDs: policy.ServiceClientIDs})}
	if entry := createOpenBaoACKCompletionEntry(f.e.Journal, "pki-reserve-"+root.Domain+"-intermediate"); entry != nil {
		var actual environmentCreateIssuerOperation
		if entry.State != "completed" || json.Unmarshal(entry.Receipt, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(operation) {
			t.Fatal("native admitted intermediate reservation changed")
		}
	} else {
		f.record(t, "pki-reserve-"+root.Domain+"-intermediate", operation, operation)
	}
	for _, stage := range []string{"authorize", "provision", "import"} {
		f.record(t, "pki-"+stage+"-"+operationID, operation, map[string]string{"operation_id": operationID, "issuer_id": id, "status": stage})
	}
	f.write(t, filepath.Join(base, "intermediate-ready.json"), idoc)
	idoc = deploymentplan.Clone(idoc)
	idoc["status"] = "active"
	f.write(t, filepath.Join(base, "intermediate-active.json"), idoc)
	f.record(t, "pki-activate-"+operationID, idoc, idoc)
	issuers[id] = idoc
	active, err := environmentCreateRegisteredIssuerFromMap(idoc, root.RootFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve the signed initial root CRL as the actual native publication.
	// Supporting roots must remain current for the entire connected historical
	// flow; only the two roots whose recovery is under test use a short lifetime.
	nextUpdate := time.Now().UTC().Add(6 * time.Hour)
	if root.ID == "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" || root.ID == "734487b8-abcf-40be-a813-8b52c64f7954" {
		// The original publication starts valid, then genuinely expires during
		// the later session transition. Never rewrite an accepted CRL to age it.
		nextUpdate = time.Now().UTC().Add(10 * time.Minute)
	}
	crl, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().UTC().Add(-time.Minute), NextUpdate: nextUpdate}, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "root-crl/revocations.pem")
	body := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crl})
	if err := f.store.write(path, body, false); err != nil {
		t.Fatal(err)
	}
	f.record(t, "pki-initial-root-crl-"+root.Domain, deploymentplan.Object{"issuer_id": root.ID, "root_sha256": root.RootFingerprint, "crl_number": "1"}, map[string]string{"issuer_id": root.ID, "crl_file": path, "crl_pem_sha256": environmentPreflightBytesSHA256(body)})
	f.publish(t)
	return root, active
}

// Registry replies are derived from the native documents actually published by
// this isolated protocol, rather than from a validator's expected cardinality.
func (f *acceptedCoreExecutionContract) installHistoricalAuthorityProtocol(t *testing.T) {
	t.Helper()
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
a=sys.argv[1:];joined=' '.join(a)
if 'wait' in a and '--for=delete' in a and 'pod' in a:
 if '--selector' not in a or '--namespace' not in a:sys.exit('missing exact original owner selector')
 ns=a[a.index('--namespace')+1];pairs=[part.split('=',1) for part in a[a.index('--selector')+1].split(',')]
 owners=[o for o in d['objects'].values() if o['kind']=='Deployment' and o['metadata'].get('namespace')==ns and all(o['spec'].get('template',{}).get('metadata',{}).get('labels',{}).get(k)==v for k,v in pairs)]
 pods=[o for o in d['objects'].values() if o['kind']=='Pod' and o['metadata'].get('namespace')==ns and all(o['metadata'].get('labels',{}).get(k)==v for k,v in pairs)]
 if len(owners)!=1 or owners[0]['spec'].get('replicas')!=0 or pods:sys.exit('original owner still has a native Pod')
 print('original identity owner stopped')
elif ('bao status -format=json' in joined or 'bao audit list -format=json' in joined or 'bao auth list -format=json' in joined or 'bao read -format=json auth/kubernetes/config' in joined) and os.environ.get('RTK_HISTORICAL_FOUNDATION_NATIVE'):
 os.execv(os.environ['RTK_HISTORICAL_FOUNDATION_NATIVE'],[os.environ['RTK_HISTORICAL_FOUNDATION_NATIVE']]+a)
elif 'SELECT (SELECT count(*) FROM pki_issuers)' in joined:
 n=len(d['issuers']);print(str(n)+'|'+str(n))
elif "json_build_object('issuers',(SELECT count(*) FROM pki_issuers WHERE id='" in joined:
 marker="WHERE id='";key=joined.split(marker,1)[1].split("'",1)[0];print(json.dumps({'issuers':int(key in d['issuers'])}))
else:os.execv(os.environ['RTK_HISTORICAL_AUTHORITY_PARENT'],[os.environ['RTK_HISTORICAL_AUTHORITY_PARENT']]+a)
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_AUTHORITY_PARENT", original)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHistoricalCompleteAuthorityContinuationPreservesReadyIdentity(t *testing.T) {
	f := newCoreExecutionContractAtBoundary(t, false, true, false, historicalCompleteDomainPolicy(t))
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	if _, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p); err != nil {
		t.Fatal("complete original Service policy", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	f.installHistoricalAuthorityProtocol(t)
	keys := map[string]*ecdsa.PrivateKey{}
	ready := f.trustAuthority(t, "mqtt", 21, false, keys)
	f.publish(t)
	p.Materials = map[string]string{}
	actual, _, err := loadOriginalReadyCreateRoot(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, "mqtt")
	if err != nil || actual.ID != ready.ID {
		t.Fatal("genuine READY authority", err)
	}
	root, intermediate := f.activateHistoricalTrustAuthority(t, ready, 21, keys)
	loaded, err := loadOriginalActiveCreateIntermediate(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, root, findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, "mqtt"))
	if err != nil || loaded.ID != intermediate.ID || keys[loaded.ID] == nil || root.RootFingerprint != ready.RootFingerprint {
		t.Fatal("same signed ACTIVE lineage", err)
	}
	originalReady, err := originalReadyIntermediate(f.store, f.e, intermediate)
	if err != nil || originalReady.Certificate != loaded.Certificate {
		t.Fatal("original READY intermediate", err)
	}
}

// A real Kubernetes admission rejection of the historical underscore name. The
// adapter rejects only this invalid external request, not any history validator.
type historicalOpenBaoNameAdmission struct{ freshStageBackend }

func (b historicalOpenBaoNameAdmission) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if body["kind"] == "ConfigMap" && deploymentplan.Map(body["metadata"])["name"] == "pki-create-openbao_tls-consumer-trust" {
		return nil, fmt.Errorf("native admission: ConfigMap metadata.name is not a DNS subdomain")
	}
	return b.freshStageBackend.Write(ctx, action, body, dry)
}

func historicalMQTTDefinitions(t *testing.T, f *acceptedCoreExecutionContract, root environmentCreateRegisteredIssuer, intermediate ...environmentCreateRegisteredIssuer) []deploymentplan.Definition {
	t.Helper()
	defs, err := renderEnvironmentCreateApplicationOwners(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, f.service)
	if err != nil {
		t.Fatal(err)
	}
	defs, err = configureEnvironmentCreateMQTTConsumers(f.p.Plan.Bootstrap, f.service, root, intermediate, defs)
	if err != nil {
		t.Fatal(err)
	}
	defs = environmentCreateApplicationDefinitionsFor(defs, "video-cloud-api", "video-cloud-logingester")
	return slices.DeleteFunc(defs, func(d deploymentplan.Definition) bool {
		return deploymentplan.ID(d.Object) == "Secret/"+f.p.Plan.Stack+"-video-cloud/factoryenroll-runtime"
	})
}

// Produce the original unwritten OpenBao request at the native admission
// boundary. The returned plan is the exact input later reconstructed by the
// maintained name-recovery preparer; no child write or accepted receipt exists.
func (f *acceptedCoreExecutionContract) rejectHistoricalOpenBaoName(t *testing.T, service environmentCreateServiceCeremony, root environmentCreateRegisteredIssuer) *deploymentplan.Plan {
	t.Helper()
	defs, err := renderEnvironmentCreatePreparedOpenBaoConsumers(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, service, root)
	if err != nil {
		t.Fatal(err)
	}
	for i := range defs {
		defs[i].Object = deploymentplan.Clone(defs[i].Object)
		if defs[i].Object["kind"] == "ConfigMap" && deploymentplan.Map(defs[i].Object["metadata"])["name"] == "pki-create-openbao-tls-consumer-trust" {
			deploymentplan.Map(defs[i].Object["metadata"])["name"] = "pki-create-openbao_tls-consumer-trust"
		}
		if defs[i].Object["kind"] == "Deployment" {
			for _, value := range certIssuerObjectList(environmentPKIPod(defs[i].Object)["volumes"]) {
				cm := deploymentplan.Map(deploymentplan.Map(value)["configMap"])
				if cm["name"] == "pki-create-openbao-tls-consumer-trust" {
					cm["name"] = "pki-create-openbao_tls-consumer-trust"
				}
			}
		}
	}
	live, err := f.backend.Inventory(context.Background(), f.p.Plan.Stack)
	if err != nil {
		t.Fatal(err)
	}
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("openbao-root-trust", defs)}, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	backend := historicalOpenBaoNameAdmission{freshStageBackend{f.native}}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, backend, f.store.Root)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	writes := deploymentplan.Digest(f.native.writes)
	err = executeEnvironmentCreateStagePlan(context.Background(), f.store, f.e, "openbao-root-trust", p, backend, qualify, check, check, f.e.Check, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "deployment.admission ConfigMap/"+f.p.Plan.Stack+"-video-cloud/pki-create-openbao_tls-consumer-trust") || deploymentplan.Digest(f.native.writes) != writes {
		t.Fatal("native invalid name admission did not preserve unwritten boundary", err)
	}
	if _, exists, err := p.ReadExecution(f.store.Root); err != nil || exists {
		t.Fatal("admission failure produced child write history", err)
	}
	return p
}

func newHistoricalOpenBaoTrustNameExecutionContract(t *testing.T) (*postServiceMQTTExecutionContract, *createServiceSessionRecovery, map[string]*ecdsa.PrivateKey) {
	t.Helper()
	core := newCoreExecutionContractAtBoundary(t, false, true, false, historicalCompleteDomainPolicy(t))
	keys := map[string]*ecdsa.PrivateKey{}
	root := core.trustAuthority(t, "mqtt", 21, false, keys)
	f := newPostServiceMQTTExecutionContractFromCore(t, core, &root)
	f.installFormalRepairPublication(t)
	f.installHistoricalAuthorityProtocol(t)
	f.protocol["objects"] = coreContractNativeInventory(f.native.objects)
	f.publish(t)
	p := f.p
	p.Materials = map[string]string{}
	r := &createServiceSessionRecovery{Ceremony: f.service}
	if _, _, err := loadPostServiceMQTTSource(f.store, f.e, r); err != nil {
		t.Fatal("original MQTT source", err)
	}
	if err := prepareMQTTTrustOrderRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, r); err != nil {
		t.Fatal("original MQTT candidate", err)
	}
	mqReview, _ := f.admitRecovery(t, "rtk-cloud.create-mqtt-trust-order-recovery/v1", p.Materials)
	stage := "mqtt-root-trust-order-recovery-" + mqReview
	f.acceptedHistoricalStage(t, stage, f.definitions)
	if err := f.e.RecordReceipt("kubernetes/mqtt-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": f.original.SHA256()})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/mqtt-root-trust", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	mqtt, ica := f.activateHistoricalTrustAuthority(t, root, 21, keys)
	f.acceptedHistoricalStage(t, "mqtt-intermediate-trust", historicalMQTTDefinitions(t, core, mqtt, ica))
	bao := f.trustAuthority(t, "openbao_tls", 22, false, keys)
	f.runtime.Backend = f.backend
	f.runtime.InitialListeners, _, _ = initialCertIssuerAuthDefinitions(f.cfg, f.runtime.InitialListeners)
	f.runtime.OpenBao.CACertPath = filepath.Join(f.store.Root, "deployment/create/material", f.e.Journal.PlanSHA256, "openbao-transport/ca.crt")
	f.publish(t)
	original := f.rejectHistoricalOpenBaoName(t, f.service, bao)
	p = f.p
	p.Materials = map[string]string{"accepted-service-core-openbao-trust-checkpoint": deploymentplan.Digest(original)}
	r = &createServiceSessionRecovery{Ceremony: f.service, PostServiceResume: true, PostTransportResume: true}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if err := prepareOpenBaoTrustNameRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, r); err != nil {
		t.Fatal("exact native unwritten OpenBao request", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if p.Materials["accepted-service-core-openbao-trust-original-plan"] != original.SHA256() || r.Candidate == nil || len(r.Candidate.Resources) != 16 {
		t.Fatal("corrected candidate lost original request")
	}
	for _, v := range r.Candidate.Resources {
		if strings.Contains(v.ID, "openbao_tls-consumer-trust") {
			t.Fatal("invalid native name survived correction")
		}
	}
	var defs []deploymentplan.Definition
	for _, v := range r.Candidate.Resources {
		defs = append(defs, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
	}
	review, _ := f.admitRecovery(t, "rtk-cloud.create-openbao-trust-name-recovery/v1", p.Materials)
	accepted := f.acceptedHistoricalStage(t, "openbao-root-trust-name-recovery-"+review, defs)
	if _, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/openbao-root-trust-name-recovery-"+review); err != nil || !j.Accepted || len(accepted.Resources) != 16 {
		t.Fatal("corrected native request did not reach actual acceptance", err)
	}
	r.CompletedTransportReview = review
	f.p = p
	if err := f.e.RecordReceipt("kubernetes/openbao-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/openbao-root-trust-name-recovery-" + review, "original_failed_plan_sha256": original.SHA256(), "original_root_issuer_id": bao.ID})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/openbao-root-trust", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return f, r, keys
}

func TestHistoricalOpenBaoTrustNameExecutionContractAdmitsOnlyCorrectedNativeRequest(t *testing.T) {
	newHistoricalOpenBaoTrustNameExecutionContract(t)
}

// Build the actual pre-ServiceAccount Device attempt, then qualify and execute
// the separate forward Job. The original terminal Job and its native UID remain
// present. This helper starts only after the caller's accepted OpenBao ancestry.
func newHistoricalDeviceOrderExecutionContract(t *testing.T, base *postServiceMQTTExecutionContract, keys map[string]*ecdsa.PrivateKey, complete ...bool) *deviceTrustExecutionContract {
	t.Helper()
	f := &deviceTrustExecutionContract{postServiceMQTTExecutionContract: base, authorityKeys: keys, fullAncestry: len(complete) == 1 && complete[0], operation: "12345678-9999-4999-8999-999999999999"}
	id := "12345678-5555-4555-8555-555555555555"
	path := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/device")
	intent := mustProviderRecoveryJSON(map[string]any{"environment": f.cfg.Environment, "issuer_id": id, "bundle_consumers": []string{"pkibroker", "video-cloud-api"}})
	if err := f.store.write(filepath.Join(path, "root-intent.json"), intent, false); err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry("device-root-bootstrap", "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	defs, err := renderEnvironmentCreateDeviceBootstrapJob(context.Background(), f.cfg, f.p.Plan.Bootstrap, id, "ready")
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for i := range defs {
		// The original defect lacked the declared ServiceAccount prerequisite.
		// Keep original object bodies, and emit that producer's serial order.
		defs[i].Dependencies = nil
		defs[i].Phase = "bootstrap"
		if defs[i].Object["kind"] == "Job" {
			failed = deploymentplan.ID(defs[i].Object)
		}
	}
	original, j := f.historicalSerialInterruptedStage(t, "kubernetes/device-root-ready", defs, failed)
	if len(j.Completed) != 0 || len(j.Owners) != 1 || len(original.Resources) != 2 {
		t.Fatal("Device original attempt lost Job-before-account boundary")
	}
	f.native.objects[failed]["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "DeadlineExceeded"}}}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.protocol["trust_mounts"] = map[string]any{}
	f.publish(t)
	f.installTrustProtocol(t)
	r := &createServiceSessionRecovery{Ceremony: f.service, DeviceBootstrapRecovery: &createDeviceBootstrapRecovery{}}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if f.fullAncestry {
		p := f.p
		p.Materials = map[string]string{}
		r, err = loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
		if err != nil {
			t.Fatal("complete original Device order ancestry", err)
		}
		f.p = p
	} else if err := prepareDeviceBootstrapOrderRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &f.p, r); err != nil {
		t.Fatal("original Device order qualification", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	f.readyReview, _ = f.admitRecovery(t, "rtk-cloud.create-device-order-recovery/v1", f.p.Materials)
	r.Review = f.readyReview
	runtime := f.runtime
	runtime.Execution, runtime.ExecutionSHA256, runtime.Backend, runtime.ServiceSessionRecovery = f.e, f.e.Journal.PlanSHA256, f.backend, r
	var accepted *deploymentplan.Plan
	if err := executeDeviceBootstrapForward(context.Background(), f.cfg, runtime, func(_ context.Context, stage string, definitions []deploymentplan.Definition) error {
		accepted = f.acceptedHistoricalStage(t, stage, definitions)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if accepted == nil || len(accepted.Resources) != 2 || deploymentplan.Map(f.native.objects[failed]["metadata"])["uid"] != j.Owners[failed] {
		t.Fatal("Device forward replaced original terminal Job")
	}
	cert, key, certPEM := coreContractCertificate(t, "isolated-device-root", true, nil, nil, 7*24*time.Hour)
	keys[id] = key
	doc := deploymentplan.Object{"issuer_id": id, "issuer_version": float64(1), "environment": f.cfg.Environment, "trust_domain": "device", "kind": "root", "status": "ready", "signer_provider": "openbao", "signer_reference": "pki-issuers/device/" + id + "/v1", "certificate_pem": certPEM, "chain_pem": certPEM, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(cert.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(certPEM))}
	f.root, err = environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, filepath.Join(path, "root-ready.json"), doc)
	if err := f.e.RecordReceipt("device-root-bootstrap", mustProviderRecoveryJSON(map[string]string{"environment": f.cfg.Environment, "issuer_id": id, "signer_reference": f.root.Reference, "root_sha256": f.root.RootFingerprint, "bundle_version": f.root.BundleVersion})); err != nil {
		t.Fatal(err)
	}
	logs, ok := f.protocol["logs"].(map[string]string)
	if !ok {
		logs = map[string]string{}
	}
	for _, res := range accepted.Resources {
		if res.Desired["kind"] == "Job" {
			logs["job/"+deploymentplan.String(deploymentplan.Map(res.Desired["metadata"])["name"])] = mustStringJSON(map[string]any{"result": map[string]string{"operation_id": f.operation}, "issuer": doc})
		}
	}
	f.protocol["logs"] = logs
	f.protocol["issuers"].(map[string]any)[id] = doc
	f.protocol["trust_mounts"] = map[string]any{f.root.Reference + "/": map[string]any{"type": "pki"}}
	f.protocol["trust_signer"] = map[string]any{"data": map[string]any{"certificate": certPEM, "ca_chain": []string{certPEM}, "issuer_id": "12345678-6666-4666-8666-666666666666", "key_id": "12345678-7777-4777-8777-777777777777", "usage": "issuing-certificates,crl-signing"}}
	f.protocol["trust_broker"] = map[string]any{"broker": 1, "issuer_id": f.service.Intermediate.ID}
	owners, err := renderEnvironmentCreateApplicationOwners(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, f.service)
	if err != nil {
		t.Fatal(err)
	}
	owners, err = configureEnvironmentCreateDeviceConsumers(f.cfg, f.p.Plan.Bootstrap, f.root, owners)
	if err != nil {
		t.Fatal(err)
	}
	defs = environmentCreateApplicationDefinitionsFor(owners, "video-cloud-api-pki", "mqtt-pki")
	for i := range defs {
		if defs[i].Object["kind"] == "Deployment" {
			defs[i].Phase = "workload"
			defs[i].Dependencies = nil
		}
	}
	f.original, f.journal = f.historicalSerialInterruptedStage(t, "kubernetes/device-root-trust", defs, "Deployment/"+f.p.Plan.Stack+"-video-cloud/mqtt-pki")
	if !knownDeviceTrustFailure(f.original, f.journal, f.p.Plan.Stack) {
		t.Fatal("original Device trust interruption changed")
	}
	f.p.Materials["accepted-service-core-device-trust-source"] = f.readyReview
	f.publishTrust(t)
	return f
}

func (f *acceptedCoreExecutionContract) activateHistoricalReadyRoot(t *testing.T, root environmentCreateRegisteredIssuer, ordinal int) environmentCreateRegisteredIssuer {
	t.Helper()
	if root.Status != "ready" {
		t.Fatal("only original READY root can activate")
	}
	doc := deploymentplan.Clone(root.Document)
	doc["status"] = "active"
	f.write(t, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap", root.Domain, "root-active.json"), doc)
	f.record(t, "pki-activate-"+fmt.Sprintf("%08d-2222-4222-8222-222222222222", ordinal), doc, doc)
	f.protocol["issuers"].(map[string]any)[root.ID] = doc
	active, err := environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	f.publish(t)
	return active
}

// Exercise both the native rejected request and the exact separately admitted
// DNS correction through ReserveIssuer, preserving the original pending intent.
func advanceHistoricalOpenBaoDNS(t *testing.T, f *postServiceMQTTExecutionContract, r *createServiceSessionRecovery, keys map[string]*ecdsa.PrivateKey, lateSeedLife ...time.Duration) environmentCreateRegisteredIssuer {
	t.Helper()
	root := f.activateHistoricalReadyRoot(t, *r.OpenBaoRoot, 22)
	policy := findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, "openbao_tls")
	original := environmentCreateIssuerRequest{Environment: f.cfg.Environment, Domain: "openbao_tls", Kind: "intermediate", ParentID: root.ID, ServerDNSNames: policy.ServerDNSNames, ServiceClientIDs: policy.ServiceClientIDs}
	names, err := correctedOpenBaoDNSPolicy(original.ServerDNSNames)
	if err != nil {
		t.Fatal(err)
	}
	corrected := original
	corrected.ServerDNSNames = names
	requests := 0
	operation := environmentCreateIssuerOperation{ID: "00000022-4444-4444-8444-444444444444", IssuerID: historicalIntermediateID(root, 22), Status: "requested", Digest: deploymentplan.Digest(corrected)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodPost && req.URL.Path == "/v1/auth/login" {
			var body map[string]string
			if json.NewDecoder(req.Body).Decode(&body) != nil || body["email"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || body["password"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
				http.Error(w, "wrong native credentials", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"id": f.operator["PKI_OPERATOR_USER_ID"], "email": body["email"]}, "tokens": map[string]string{"access_token": "isolated-history-reservation"}})
			return
		}
		if req.Method != http.MethodPost || req.URL.Path != "/v1/platform/pki/operations" || req.Header.Get("Authorization") != "Bearer isolated-history-reservation" {
			http.Error(w, "wrong native operation", http.StatusForbidden)
			return
		}
		var request environmentCreateIssuerRequest
		if json.NewDecoder(req.Body).Decode(&request) != nil {
			http.Error(w, "malformed native request", http.StatusBadRequest)
			return
		}
		requests++
		switch deploymentplan.Digest(request) {
		case deploymentplan.Digest(original):
			http.Error(w, "DNS policy requires dotted aliases", http.StatusBadRequest)
		case deploymentplan.Digest(corrected):
			_ = json.NewEncoder(w).Encode(operation)
		default:
			http.Error(w, "foreign native policy", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), f.cfg.Environment, f.operator["PKI_OPERATOR_USER_ID"])
	if err != nil {
		t.Fatal(err)
	}
	client.Execution = f.e
	if _, err := client.Authenticate(context.Background(), f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReserveIssuer(context.Background(), "isolated-original-openbao-intermediate", original); err == nil || requests != 1 {
		t.Fatal("original native DNS policy was not refused", err)
	}
	entry := createOpenBaoACKCompletionEntry(f.e.Journal, "pki-reserve-openbao_tls-intermediate")
	intent, err := environmentCreateIssuerIntentSHA(original)
	if err != nil || entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 || entry.IntentSHA256 != intent {
		t.Fatal("native refusal lost exact original no-write intent", err)
	}
	r.OpenBaoIntermediateRejected = true
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if len(lateSeedLife) == 1 {
		p := f.p
		p.Materials = map[string]string{}
		r, err = loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
		if err != nil {
			t.Fatal("complete original DNS rejection ancestry", err)
		}
		if !r.OpenBaoIntermediateRejected || !r.PostServiceRenewal || r.RenewalCandidate == nil {
			t.Fatal("DNS continuation lost actual expired used seed")
		}
		f.p = p
	} else if err := prepareOpenBaoIntermediateDNSRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &f.p, r); err != nil {
		t.Fatal("original intermediate DNS recovery", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if deploymentplan.Digest(findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, "openbao_tls").ServerDNSNames) != deploymentplan.Digest(names) {
		t.Fatal("DNS recovery changed more than refused bare alias")
	}
	review, a := f.admitRecovery(t, "rtk-cloud.create-openbao-intermediate-dns-recovery/v1", f.p.Materials)
	if len(lateSeedLife) == 1 {
		r.Review = review
		publishHistoricalUsedRenewal(t, f.acceptedCoreExecutionContract, r, review, false, false, time.Now().UTC().Add(lateSeedLife[0]))
		clients, err := renderEnvironmentCreateTransportClientDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		f.acceptedHistoricalStage(t, "transport-client-enrollment-renewal-"+review, clients)
	}
	client.ReservationRecoveryReview = review
	client.RejectedOpenBaoIntermediateRequest = &original
	actual, err := client.ReserveIssuer(context.Background(), "isolated-original-openbao-intermediate-forward", corrected)
	if err != nil || actual != operation || requests != 2 {
		t.Fatal("qualified original intermediate reservation", err)
	}
	root, intermediate := f.activateHistoricalTrustAuthority(t, root, 22, keys)
	defs, err := renderEnvironmentCreatePreparedOpenBaoConsumers(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, root, intermediate)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "openbao-intermediate-trust", defs)
	f.p.Materials["accepted-service-core-device-order-source"] = deploymentplan.Digest(a)
	return intermediate
}

func TestHistoricalOpenBaoDNSAndDeviceOrderExecutionContractPreservesNativeIdentity(t *testing.T) {
	base, r, keys := newHistoricalOpenBaoTrustNameExecutionContract(t)
	advanceHistoricalOpenBaoDNS(t, base, r, keys)
	device := newHistoricalDeviceOrderExecutionContract(t, base, keys)
	before := createBootstrapStateSnapshot(t, device.store.Root)
	prepared, err := device.prepare(t)
	if err != nil {
		t.Fatal("complete native Device ordering/trust boundary", err)
	}
	assertRecoveryContractReadOnly(t, device.store, before)
	if prepared.DeviceTrustRecovery.Root.ID != device.root.ID || prepared.Candidate == nil || len(prepared.Candidate.Resources) != 7 {
		t.Fatal("Device continuation lost original root or exact consumers")
	}
}

// Continue the real Device READY/partial-trust boundary through App ACTIVE.
// Every forward has its own admitted source snapshot and accepted child, while
// the original failed broker and its UID remain part of the retained history.
func advanceHistoricalDeviceApp(t *testing.T, f *deviceTrustExecutionContract) (*createServiceSessionRecovery, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer) {
	t.Helper()
	device, err := f.prepare(t)
	if err != nil {
		t.Fatal("Device trust candidate", err)
	}
	device.Review, _ = f.admitRecovery(t, "rtk-cloud.create-device-trust-recovery/v1", f.p.Materials)
	if err := writeCreateServiceRecoveryCandidates(f.store, device.Review, device, true); err != nil {
		t.Fatal("archive exact qualified Device candidate before execution", err)
	}
	f.executeDevice(t, device)
	active := f.activateDevice(t)
	app := f.trustAuthority(t, "app", 23, false, f.authorityKeys)
	defs, err := renderEnvironmentCreateApplicationOwners(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, f.service)
	if err != nil {
		t.Fatal(err)
	}
	defs, err = configureEnvironmentCreateDeviceConsumers(f.cfg, f.p.Plan.Bootstrap, f.root, defs)
	if err != nil {
		t.Fatal(err)
	}
	defs, err = configureEnvironmentCreateAppConsumers(f.p.Plan.Bootstrap, app, nil, defs)
	if err != nil {
		t.Fatal(err)
	}
	defs = environmentCreateApplicationDefinitionsFor(defs, "video-cloud-api-app-pki", "mqtt-pki")
	failed := "Deployment/" + f.p.Plan.Stack + "-video-cloud/mqtt-pki"
	for i := range defs {
		if defs[i].Object["kind"] == "Deployment" {
			defs[i].Phase = "workload"
			defs[i].Dependencies = nil
		}
		if deploymentplan.ID(defs[i].Object) == failed {
			for _, value := range certIssuerObjectList(environmentPKIPod(defs[i].Object)["containers"]) {
				container := deploymentplan.Map(value)
				if container["name"] == "pkibroker" {
					container["env"] = environmentCreateServiceWithoutEnv(container["env"], "PKI_BROKER_APP_PKI_ENABLED")
				}
			}
		}
	}
	original, interrupted := f.historicalSerialInterruptedStage(t, "kubernetes/app-root-trust", defs, failed)
	if !knownAppTrustFailure(original, interrupted, f.p.Plan.Stack) {
		t.Fatal("App original interruption lost exact known prefix")
	}
	f.publishTrust(t)
	selected, selectedJournal, err := loadOriginalAppTrustPlan(f.store, f.e)
	if err != nil {
		t.Fatal(err)
	}
	recovery := &createServiceSessionRecovery{Ceremony: f.service, AppTrustRecovery: &createAppTrustRecovery{SourceReview: device.Review, Original: selected, Journal: selectedJournal}}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if f.fullAncestry {
		p := f.p
		p.Materials = map[string]string{}
		recovery, err = loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
		if err != nil {
			t.Fatal("complete original App trust ancestry", err)
		}
		f.p = p
	} else if err := prepareAppTrustRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &f.p, recovery); err != nil {
		t.Fatal("App exact original trust preparation", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	recovery.Review, _ = f.admitRecovery(t, "rtk-cloud.create-app-trust-recovery/v1", f.p.Materials)
	runtime := f.runtime
	runtime.Execution, runtime.ExecutionSHA256, runtime.Backend, runtime.ServiceSessionRecovery = f.e, f.e.Journal.PlanSHA256, f.backend, recovery
	if err := executeAppTrustForward(context.Background(), runtime, func(_ context.Context, stage string, definitions []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, definitions)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	accepted, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/app-root-trust-broker-forward-"+recovery.Review)
	if err != nil || len(accepted.Resources) != 7 || !journal.Accepted {
		t.Fatal("App forward not accepted", err)
	}
	for id, uid := range interrupted.Owners {
		if journal.Owners[id] != uid {
			t.Fatal("App native owner was replaced", id)
		}
	}
	app, intermediate := f.activateHistoricalTrustAuthority(t, app, 23, f.authorityKeys)
	defs, err = renderEnvironmentCreateApplicationOwners(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.p.Plan.Release, f.store, f.operator, f.service)
	if err != nil {
		t.Fatal(err)
	}
	defs, err = configureEnvironmentCreateDeviceConsumers(f.cfg, f.p.Plan.Bootstrap, f.root, defs)
	if err != nil {
		t.Fatal(err)
	}
	defs, err = configureEnvironmentCreateAppConsumers(f.p.Plan.Bootstrap, app, []environmentCreateRegisteredIssuer{intermediate}, defs)
	if err != nil {
		t.Fatal(err)
	}
	defs = environmentCreateApplicationDefinitionsFor(defs, "video-cloud-api-app-pki", "mqtt-pki")
	f.acceptedHistoricalStage(t, "app-intermediate-trust", defs)
	roots := map[string]environmentCreateRegisteredIssuer{"service": f.service.Root, "device": active, "app": app}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": f.service.Intermediate, "app": intermediate}
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		for _, kind := range []string{"root", "intermediate"} {
			var doc map[string]any
			if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap", domain, kind+"-active.json"), &doc); err != nil {
				t.Fatal(err)
			}
			rootSHA := ""
			if kind == "intermediate" {
				rootSHA = roots[domain].RootFingerprint
			}
			issuer, err := environmentCreateRegisteredIssuerFromMap(doc, rootSHA)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "root" {
				roots[domain] = issuer
			} else {
				intermediates[domain] = issuer
			}
		}
	}
	if err := saveEnvironmentCreateRegisteredIssuers(f.store, roots, intermediates); err != nil {
		t.Fatal("all original ACTIVE authority publication", err)
	}
	f.publishTrust(t)
	if len(f.protocol["issuers"].(map[string]any)) != 9 {
		t.Fatal("publication did not retain exactly nine original authorities")
	}
	return recovery, roots, intermediates
}

func TestHistoricalDeviceAppContinuationPreservesSignedAuthoritiesAndOwners(t *testing.T) {
	base, r, keys := newHistoricalOpenBaoTrustNameExecutionContract(t)
	advanceHistoricalOpenBaoDNS(t, base, r, keys)
	device := newHistoricalDeviceOrderExecutionContract(t, base, keys)
	recovery, roots, intermediates := advanceHistoricalDeviceApp(t, device)
	if recovery.AppTrustRecovery.Root.ID != roots["app"].ID || roots["device"].Certificate != device.root.Certificate || intermediates["app"].RootFingerprint != roots["app"].RootFingerprint {
		t.Fatal("App continuation changed original authority")
	}
	if _, err := originalReadyIntermediate(device.store, device.e, intermediates["app"]); err != nil {
		t.Fatal("original App READY intermediate", err)
	}
	for domain, root := range roots {
		var published struct {
			Root         map[string]any `json:"root"`
			Intermediate map[string]any `json:"intermediate"`
		}
		if readExecutionJSON(filepath.Join(device.store.Root, "pki", domain, "issuer.json"), &published) != nil || deploymentplan.Digest(published.Root) != deploymentplan.Digest(root.Document) {
			t.Fatal("native original public issuer not preserved", domain)
		}
	}
}
