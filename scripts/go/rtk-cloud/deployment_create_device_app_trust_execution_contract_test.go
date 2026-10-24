package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type deviceTrustExecutionContract struct {
	*postServiceMQTTExecutionContract
	root                   environmentCreateRegisteredIssuer
	readyReview, operation string
	original               *deploymentplan.Plan
	journal                deploymentplan.Journal
	authorityKeys          map[string]*ecdsa.PrivateKey
	fullAncestry           bool
}

// These are isolated native protocol responses backed by genuine signed public
// authorities, encrypted custody and completed operation records. They exercise
// the selected Device/App recovery preparers; the earlier global admission
// selector is independently covered by the accepted Core/MQTT source tests.
func (f *acceptedCoreExecutionContract) trustAuthority(t *testing.T, domain string, ordinal int, active bool, keys ...map[string]*ecdsa.PrivateKey) environmentCreateRegisteredIssuer {
	t.Helper()
	return f.trustAuthorityWithID(t, domain, ordinal, fmt.Sprintf("%08d-1111-4111-8111-111111111111", ordinal), active, keys...)
}

// Historical repair scopes pin these native authority IDs. Select them before
// signing or recording the original custody; accepted identities are never edited.
func (f *acceptedCoreExecutionContract) trustAuthorityWithID(t *testing.T, domain string, ordinal int, id string, active bool, keys ...map[string]*ecdsa.PrivateKey) environmentCreateRegisteredIssuer {
	t.Helper()
	if !environmentBootstrapOwnerUUID.MatchString(id) {
		t.Fatal("invalid original synthetic authority ID")
	}
	opid := fmt.Sprintf("%08d-2222-4222-8222-222222222222", ordinal)
	cert, key, certPEM := coreContractCertificate(t, "isolated-"+domain+"-root", true, nil, nil, 7*24*time.Hour)
	if len(keys) == 1 {
		keys[0][id] = key
	}
	doc := deploymentplan.Object{"issuer_id": id, "issuer_version": float64(1), "environment": f.cfg.Environment, "trust_domain": domain, "kind": "root", "status": "ready", "signer_provider": "offline", "signer_reference": "offline/" + id, "certificate_pem": certPEM, "chain_pem": certPEM, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(cert.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(certPEM))}
	if domain == "device" {
		// Device is an online native authority, including in the bounded
		// transport coordinator. Choose its provider before first publication.
		doc["signer_provider"] = "openbao"
		doc["signer_reference"] = "pki-issuers/device/" + id + "/v1"
	}
	root, err := environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	op := environmentCreateIssuerOperation{ID: opid, IssuerID: id, Status: "requested", Digest: deploymentplan.Digest(environmentCreateIssuerRequest{Environment: f.cfg.Environment, Domain: domain, Kind: "root"})}
	f.record(t, "pki-reserve-"+domain+"-root", op, op)
	for _, stage := range []string{"authorize", "provision", "import"} {
		f.record(t, "pki-"+stage+"-"+op.ID, op, map[string]string{"operation_id": op.ID, "issuer_id": id, "status": stage})
	}
	base := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap", domain)
	f.write(t, filepath.Join(base, "ready.json"), doc)
	paths := map[string]string{"key": filepath.Join("pki/custody/roots", id, "ca-key.encrypted.pem"), "certificate": filepath.Join("pki/custody/roots", id, "certificate.pem"), "csr": filepath.Join("pki/custody/roots", id, "csr.pem"), "passphrase": "pki/custody/" + domain + "-root-v1-key-passphrase"}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := x509.EncryptPEMBlock(rand.Reader, "EC PRIVATE KEY", der, []byte("isolated-authority-passphrase"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: cert.Subject}, key)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string][]byte{"key": pem.EncodeToMemory(encrypted), "certificate": []byte(certPEM), "csr": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), "passphrase": []byte("isolated-authority-passphrase")}
	hashes := map[string]string{}
	for k, path := range paths {
		if err := f.store.write(path, values[k], false); err != nil {
			t.Fatal(err)
		}
		hashes[k] = environmentPreflightBytesSHA256(values[k])
	}
	f.write(t, filepath.Join("pki/custody/roots", id, "manifest.json"), map[string]any{"schema": "rtk-root-custody/v1", "environment": f.cfg.Environment, "trust_domain": domain, "root_issuer_id": id, "certificate_fingerprint_sha256": root.RootFingerprint, "operator_user_id": f.operator["PKI_OPERATOR_USER_ID"], "ceremony_operation_id": op.ID, "paths": paths, "file_sha256": hashes})
	issuers := f.protocol["issuers"].(map[string]any)
	issuers[id] = doc
	if !active {
		return root
	}
	doc = deploymentplan.Clone(doc)
	doc["status"] = "active"
	f.write(t, filepath.Join(base, "root-active.json"), doc)
	f.record(t, "pki-activate-"+op.ID, doc, doc)
	issuers[id] = doc
	root, err = environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	intermediate, intermediateKey, intermediatePEM := coreContractCertificate(t, "isolated-"+domain+"-intermediate", true, cert, key, 7*24*time.Hour)
	iid := fmt.Sprintf("%08d-3333-4333-8333-333333333333", ordinal)
	iop := fmt.Sprintf("%08d-4444-4444-8444-444444444444", ordinal)
	if len(keys) == 1 {
		keys[0][iid] = intermediateKey
	}
	chain := intermediatePEM + certPEM
	idoc := deploymentplan.Object{"issuer_id": iid, "issuer_version": float64(1), "environment": f.cfg.Environment, "trust_domain": domain, "kind": "intermediate", "status": "active", "signer_provider": "openbao", "signer_reference": "pki-issuers/" + domain + "/" + iid + "/v1", "parent_issuer_id": id, "certificate_pem": intermediatePEM, "chain_pem": chain, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(intermediate.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(chain))}
	policy := findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, domain)
	operation := environmentCreateIssuerOperation{ID: iop, IssuerID: iid, Status: "requested", Digest: deploymentplan.Digest(environmentCreateIssuerRequest{Environment: f.cfg.Environment, Domain: domain, Kind: "intermediate", ParentID: id, ServerDNSNames: policy.ServerDNSNames, ServiceClientIDs: policy.ServiceClientIDs})}
	f.record(t, "pki-reserve-"+domain+"-intermediate", operation, operation)
	for _, stage := range []string{"authorize", "provision", "import"} {
		f.record(t, "pki-"+stage+"-"+iop, operation, map[string]string{"operation_id": iop, "issuer_id": iid, "status": stage})
	}
	ready := deploymentplan.Clone(idoc)
	ready["status"] = "ready"
	f.write(t, filepath.Join(base, "intermediate-ready.json"), ready)
	f.write(t, filepath.Join(base, "intermediate-active.json"), idoc)
	f.record(t, "pki-activate-"+iop, idoc, idoc)
	issuers[iid] = idoc
	return root
}

func (f *deviceTrustExecutionContract) publishTrust(t *testing.T) {
	t.Helper()
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	issuers, operations, err := deviceTrustRegistryIdentities(f.e.Journal, f.root.ID, f.operation)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(issuers))
	for id := range issuers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	ops := make([]map[string]string, 0, len(operations))
	for id, issuer := range operations {
		ops = append(ops, map[string]string{"id": id, "issuer_id": issuer})
	}
	slices.SortFunc(ops, func(a, b map[string]string) int { return strings.Compare(a["id"], b["id"]) })
	f.protocol["trust_registry"] = map[string]any{"issuers": ids, "operations": ops}
	f.publish(t)
}
func (f *deviceTrustExecutionContract) installTrustProtocol(t *testing.T) {
	t.Helper()
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as r:d=json.load(r)
a=sys.argv[1:];joined=' '.join(a)
if 'bao secrets list -format=json' in joined:
 print(json.dumps(d['trust_mounts']))
elif 'bao read -format=json pki-issuers/device/' in joined:
 print(json.dumps(d['trust_signer']))
elif "json_build_object('broker'" in joined:
 print(json.dumps(d['trust_broker']))
elif "json_build_object('issuers',(SELECT count(*) FROM pki_issuers WHERE id='" in joined:
 key=joined.split("WHERE id='",1)[1].split("'",1)[0]
 print(json.dumps({'issuers':int(key in d['issuers'])}))
elif "json_build_object('issuers'" in joined:
 print(json.dumps(d['trust_registry']))
else:os.execv(os.environ['RTK_TRUST_CONTRACT_ORIGINAL'],[os.environ['RTK_TRUST_CONTRACT_ORIGINAL']]+a)
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_TRUST_CONTRACT_ORIGINAL", original)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func newDeviceTrustExecutionContract(t *testing.T) *deviceTrustExecutionContract {
	t.Helper()
	base := newPostServiceMQTTExecutionContract(t)
	// The inherited helper selects only the Service phase. Select the native
	// Device policy for this later, independently qualified preparer boundary.
	base.p.Plan.Bootstrap.Domains = append(base.p.Plan.Bootstrap.Domains, environmentCreateIssuerPolicy{Domain: "device", Consumers: []string{"pkibroker", "video-cloud-api"}, BundleConsumers: []string{"pkibroker", "video-cloud-api"}})
	f := &deviceTrustExecutionContract{postServiceMQTTExecutionContract: base, readyReview: deploymentplan.Digest("isolated-accepted-device-ready-order"), operation: "12345678-9999-4999-8999-999999999999"}
	forward := "kubernetes/mqtt-root-trust-order-recovery-" + f.identityReview
	actual := f.stage(t, forward, f.definitions)
	if err := f.e.RecordReceipt("kubernetes/mqtt-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": forward, "original_failed_plan_sha256": f.postServiceMQTTExecutionContract.original.SHA256(), "forward_plan_sha256": actual.SHA256()})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/mqtt-root-trust", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	f.trustAuthority(t, "mqtt", 21, true)
	f.trustAuthority(t, "openbao_tls", 22, true)
	core, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	f.stage(t, "kubernetes/openbao-intermediate-trust", core)
	cert, _, certPEM := coreContractCertificate(t, "isolated-device-root", true, nil, nil, 7*24*time.Hour)
	id := "12345678-5555-4555-8555-555555555555"
	doc := deploymentplan.Object{"issuer_id": id, "issuer_version": float64(1), "environment": f.cfg.Environment, "trust_domain": "device", "kind": "root", "status": "ready", "signer_provider": "openbao", "signer_reference": "pki-issuers/device/" + id + "/v1", "certificate_pem": certPEM, "chain_pem": certPEM, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(cert.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(certPEM))}
	f.root, err = environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	intent := mustProviderRecoveryJSON(map[string]any{"environment": f.cfg.Environment, "issuer_id": id, "bundle_consumers": []string{"pkibroker", "video-cloud-api"}})
	path := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/device")
	if err := f.store.write(filepath.Join(path, "root-intent.json"), intent, false); err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry("device-root-bootstrap", "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.RecordReceipt("device-root-bootstrap", mustProviderRecoveryJSON(map[string]string{"environment": f.cfg.Environment, "issuer_id": id, "signer_reference": f.root.Reference, "root_sha256": f.root.RootFingerprint, "bundle_version": f.root.BundleVersion})); err != nil {
		t.Fatal(err)
	}
	f.write(t, filepath.Join(path, "root-ready.json"), doc)
	defs, err := renderEnvironmentCreateDeviceBootstrapJob(context.Background(), f.cfg, f.p.Plan.Bootstrap, id, "ready")
	if err != nil {
		t.Fatal(err)
	}
	ready := f.stage(t, "kubernetes/device-root-ready-order-recovery-"+f.readyReview, defs)
	logs := map[string]string{}
	for _, r := range ready.Resources {
		if r.Desired["kind"] == "Job" {
			logs["job/"+deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["name"])] = mustStringJSON(map[string]any{"result": map[string]string{"operation_id": f.operation}, "issuer": doc})
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
		t.Fatalf("Device native interruption differs: resources=%d completed=%d owners=%d", len(f.original.Resources), len(f.journal.Completed), len(f.journal.Owners))
	}
	f.p.Materials["accepted-service-core-device-trust-source"] = f.readyReview
	f.publishTrust(t)
	f.installTrustProtocol(t)
	return f
}
func (f *deviceTrustExecutionContract) prepare(t *testing.T) (*createServiceSessionRecovery, error) {
	t.Helper()
	if f.fullAncestry {
		p := f.p
		p.Materials = map[string]string{}
		r, err := loadAcceptedServiceCore(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &p, true, true)
		if err == nil {
			f.p = p
		}
		return r, err
	}
	r := &createServiceSessionRecovery{Ceremony: f.service, Review: deploymentplan.Digest("isolated-qualified-device-trust-forward"), DeviceTrustRecovery: &createDeviceTrustRecovery{SourceReview: f.readyReview}}
	err := prepareDeviceTrustRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &f.p, r)
	return r, err
}
func TestDeviceTrustExecutionContractQualifiesAndExecutesOriginalIdentity(t *testing.T) {
	f := newDeviceTrustExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	r, err := f.prepare(t)
	if err != nil {
		t.Fatal("genuine Device READY/partial trust rejected", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if r.Candidate == nil || len(r.Candidate.Resources) != 7 || r.DeviceTrustRecovery.Root.ID != f.root.ID || r.DeviceTrustRecovery.Original.SHA256() != f.original.SHA256() || f.p.Materials["accepted-service-core-device-trust-registry"] == "" {
		t.Fatal("qualified candidate lost exact authority, registry or original write boundary")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256, Backend: f.backend, ServiceSessionRecovery: r}
	if err := executeDeviceTrustForward(context.Background(), runtime, func(_ context.Context, stage string, defs []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, defs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/device-root-trust-policy-forward-"+r.Review)
	if err != nil || len(p.Resources) != 7 || !j.Accepted {
		t.Fatal("Device forward never reached actual acceptance", err)
	}
	for id, uid := range f.journal.Owners {
		if j.Owners[id] != uid {
			t.Fatal("forward replaced an original native owner", id)
		}
	}
	for _, entry := range f.e.Journal.Entries {
		if entry.Key == "kubernetes/device-root-trust" {
			var receipt map[string]string
			if entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt["original_root_issuer_id"] != f.root.ID || receipt["original_failed_plan_sha256"] != f.original.SHA256() {
				t.Fatal("forward lost original root and interrupted child receipt")
			}
		}
	}
}

func TestDeviceTrustExecutionContractRejectsChangedNativeAndHistoricalEvidence(t *testing.T) {
	for _, mutation := range []string{"signer-key", "registry-extra", "ready-job-owner", "partial-owner", "unwritten-api", "controller-owner", "broker-issuer", "root-custody"} {
		t.Run(mutation, func(t *testing.T) {
			f := newDeviceTrustExecutionContract(t)
			objects := f.protocol["objects"].(map[string]deploymentplan.Object)
			switch mutation {
			case "signer-key":
				f.protocol["trust_signer"].(map[string]any)["data"].(map[string]any)["key_id"] = "00000000-0000-0000-0000-000000000000"
			case "registry-extra":
				registry := f.protocol["trust_registry"].(map[string]any)
				registry["issuers"] = append(registry["issuers"].([]string), "99999999-9999-4999-8999-999999999999")
			case "ready-job-owner":
				for _, obj := range objects {
					if obj["kind"] == "Job" && strings.HasSuffix(deploymentplan.String(deploymentplan.Map(obj["metadata"])["name"]), "-ready") {
						deploymentplan.Map(obj["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
					}
				}
			case "partial-owner":
				deploymentplan.Map(objects[f.journal.Failed]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "unwritten-api":
				for _, res := range f.original.Resources {
					if f.journal.Owners[res.ID] == "" {
						obj := deploymentplan.Clone(res.Desired)
						deploymentplan.Map(obj["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
						objects[res.ID] = obj
					}
				}
			case "controller-owner":
				deploymentplan.Map(objects["Deployment/"+f.p.Plan.Stack+"-video-cloud/pki-controller"]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "broker-issuer":
				f.protocol["trust_broker"].(map[string]any)["issuer_id"] = f.root.ID
			case "root-custody":
				if err := f.store.write("pki/custody/openbao_tls-root-v1-key-passphrase", []byte("changed custody"), true); err != nil {
					t.Fatal(err)
				}
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if _, err := f.prepare(t); err == nil {
				t.Fatal("Device trust accepted changed original/native evidence", mutation)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

func (f *deviceTrustExecutionContract) executeDevice(t *testing.T, r *createServiceSessionRecovery) {
	t.Helper()
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256, Backend: f.backend, ServiceSessionRecovery: r}
	if err := executeDeviceTrustForward(context.Background(), runtime, func(_ context.Context, stage string, defs []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, defs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func (f *deviceTrustExecutionContract) activateDevice(t *testing.T) environmentCreateRegisteredIssuer {
	t.Helper()
	defs, err := renderEnvironmentCreateDeviceBootstrapJob(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.root.ID, "active")
	if err != nil {
		t.Fatal(err)
	}
	plan := f.acceptedHistoricalStage(t, "device-root-active", defs)
	doc := deploymentplan.Clone(f.root.Document)
	doc["status"] = "active"
	f.write(t, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/device/root-active.json"), doc)
	for _, res := range plan.Resources {
		if res.Desired["kind"] == "Job" {
			f.protocol["logs"].(map[string]string)["job/"+deploymentplan.String(deploymentplan.Map(res.Desired["metadata"])["name"])] = mustStringJSON(map[string]any{"issuer": doc})
		}
	}
	f.protocol["issuers"].(map[string]any)[f.root.ID] = doc
	if err := f.e.CompleteEntry("device-root-bootstrap", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	active, err := environmentCreateRegisteredIssuerFromMap(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	return active
}
func TestAppTrustExecutionContractQualifiesAndExecutesOriginalIdentity(t *testing.T) {
	f := newDeviceTrustExecutionContract(t)
	device, err := f.prepare(t)
	if err != nil {
		t.Fatal("Device preparation", err)
	}
	f.executeDevice(t, device)
	active := f.activateDevice(t)
	app := f.trustAuthority(t, "app", 23, false)
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
			for _, v := range certIssuerObjectList(environmentPKIPod(defs[i].Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pkibroker" {
					c["env"] = environmentCreateServiceWithoutEnv(c["env"], "PKI_BROKER_APP_PKI_ENABLED")
				}
			}
		}
	}
	original, j := f.historicalSerialInterruptedStage(t, "kubernetes/app-root-trust", defs, failed)
	if !knownAppTrustFailure(original, j, f.p.Plan.Stack) {
		t.Fatalf("App partial native writes differ: resources=%d completed=%d owners=%d", len(original.Resources), len(j.Completed), len(j.Owners))
	}
	f.publishTrust(t)
	selected, selectedJournal, err := loadOriginalAppTrustPlan(f.store, f.e)
	if err != nil {
		t.Fatal("App original loader", err)
	}
	r := &createServiceSessionRecovery{Ceremony: f.service, Review: deploymentplan.Digest("isolated-qualified-app-trust-forward"), AppTrustRecovery: &createAppTrustRecovery{SourceReview: device.Review, Original: selected, Journal: selectedJournal}}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	if err := prepareAppTrustRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, f.operator, &f.p, r); err != nil {
		t.Fatal("App preparation", err)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if r.Candidate == nil || len(r.Candidate.Resources) != 7 || r.AppTrustRecovery.Root.ID != app.ID || r.AppTrustRecovery.Device.ID != active.ID || r.AppTrustRecovery.DeviceReady.Status != "ready" || f.p.Materials["accepted-service-core-app-trust-device-active"] == "" || f.p.Materials["accepted-service-core-original-app-root-custody-manifest"] == "" {
		t.Fatal("App candidate lost original Device/root/custody identity")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256, Backend: f.backend, ServiceSessionRecovery: r}
	if err := executeAppTrustForward(context.Background(), runtime, func(_ context.Context, stage string, defs []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, defs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, accepted, err := acceptedCreateStage(f.store, f.e, "kubernetes/app-root-trust-broker-forward-"+r.Review)
	if err != nil || len(p.Resources) != 7 || !accepted.Accepted {
		t.Fatal("App forward never reached actual acceptance", err)
	}
	for id, uid := range j.Owners {
		if accepted.Owners[id] != uid {
			t.Fatal("App forward replaced original native owner", id)
		}
	}
	var broker deploymentplan.Object
	for _, value := range certIssuerObjectList(environmentPKIPod(f.native.objects[failed])["containers"]) {
		container := deploymentplan.Map(value)
		if container["name"] == "pkibroker" {
			broker = container
		}
	}
	if createRecipeEnv(broker)["PKI_BROKER_APP_PKI_ENABLED"] != "true" {
		t.Fatal("App forward omitted native broker enforcement")
	}
}
