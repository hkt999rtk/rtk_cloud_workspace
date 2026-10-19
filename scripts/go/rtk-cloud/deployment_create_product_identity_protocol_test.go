package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The boundary fixture replaces subprocesses and native HTTP, not deployment
// business steps. Production issuer/session/enrollment/mirror/approval/cleanup
// functions run against real signed keys/CSRs and real scoped child journals.
type productProtocolFixture struct {
	t                   *testing.T
	cfg                 deploymentConfig
	p                   environmentCreateBootstrapPlan
	store               secretStore
	e                   *environmentCreateExecution
	l                   *createNormalTailProductLifecycle
	runtime             environmentCreateBootstrapRuntime
	h                   *createNormalTailHandoff
	b                   *productProtocolBackend
	plans               map[string]*deploymentplan.Plan
	rootKey, icaKey     *ecdsa.PrivateKey
	root, ica           *x509.Certificate
	next                environmentCreateRegisteredIssuer
	operation           environmentCreateIssuerOperation
	policies            map[string]string
	roles               map[string][]string
	rows                map[string]managedSessionClient
	states, inspections map[string][]byte
	bootstrap           map[string]string
	session, status     string
	subjects, installed []string
	expires             time.Time
	final               environmentCreateBootstrapFinalization
	marker              []byte
	posts               map[string]int
	server              *httptest.Server
	failStage           string
	unscoped            bool
	controls            func(environmentCreateServiceCeremony, bool) ([]deploymentplan.Definition, error)
}

type productProtocolBackend struct{ *normalTailE2EBackend }

// Match the real engine holder format so independent receipt readers can
// authenticate every genuine scoped attempt produced by this fixture.
func (b *productProtocolBackend) AcquireExecution(ctx context.Context, p *deploymentplan.Plan) (deploymentplan.ExecutionLease, error) {
	l, err := b.normalTailE2EBackend.AcquireExecution(ctx, p)
	if err != nil {
		return nil, err
	}
	owner := l.Owner()
	owner.Holder = p.SHA256() + fmt.Sprintf(":%032x", b.leases)
	return workloadForwardRecordedLease{l, owner}, nil
}

func (b *productProtocolBackend) Write(ctx context.Context, action string, o deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	// Model the API response independently from the submitted request: replace
	// preserves native identity/status and the Deployment controller advances
	// only its own revision when a persisted PodTemplate changes.
	old := b.objects[deploymentplan.ID(o)]
	request := deploymentplan.Clone(o)
	if old != nil && action == "update" {
		deploymentplan.Map(request["metadata"])["uid"] = deploymentplan.Map(old["metadata"])["uid"]
		if status, present := old["status"]; present {
			request["status"] = deploymentplan.Clone(deploymentplan.Map(status))
		}
	}
	actual, err := b.normalTailE2EBackend.Write(ctx, action, request, dry)
	if err != nil || dry || actual["kind"] != "Deployment" {
		return actual, err
	}
	if old == nil || deploymentplan.Digest(deploymentplan.Map(old["spec"])["template"]) != deploymentplan.Digest(deploymentplan.Map(actual["spec"])["template"]) {
		annotations := deploymentplan.Map(deploymentplan.Map(actual["metadata"])["annotations"])
		if annotations == nil {
			annotations = deploymentplan.Object{}
			deploymentplan.Map(actual["metadata"])["annotations"] = annotations
		}
		revision, _ := strconv.Atoi(deploymentplan.String(deploymentplan.Map(deploymentplan.Map(old["metadata"])["annotations"])["deployment.kubernetes.io/revision"]))
		annotations["deployment.kubernetes.io/revision"] = strconv.Itoa(revision + 1)
		b.objects[deploymentplan.ID(actual)] = deploymentplan.Clone(actual)
	}
	return deploymentplan.Clone(actual), nil
}

func productPEM(kind string, raw []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: raw}))
}

func newProductProtocolFixture(t *testing.T, successor bool, nativeConnectivity ...bool) *productProtocolFixture {
	return newProductProtocolFixtureLifetime(t, successor, 24*time.Hour, nativeConnectivity...)
}

func newProductProtocolFixtureLifetime(t *testing.T, successor bool, caLifetime time.Duration, nativeConnectivity ...bool) *productProtocolFixture {
	t.Helper()
	cfg, p, _, r, _, domainRoots, domainIntermediates, _ := createApplicationFinalizeFixture(t, "dev")
	// Resolved configuration carries the selected environment separately; the
	// renderer derives CLOUD_ENV_NAME. Do not inherit the legacy fixture hint.
	cfg.Values = appendMap(cfg.Values, map[string]string{})
	delete(cfg.Values, "CLOUD_ENV_NAME")
	for domain, issuer := range domainIntermediates {
		issuer.Reference = "pki-issuers/" + domain + "/" + issuer.ID + "/v1"
		issuer.Document["signer_reference"] = issuer.Reference
		domainIntermediates[domain] = issuer
	}
	enableProductIdentityFixture(&cfg)
	cfg.Workspace, _ = workspaceRoot()
	template := "deploy/pki/render-staging-controller.py"
	raw, err := os.ReadFile(filepath.Join(cfg.Workspace, "repos/rtk_video_cloud", template))
	if err != nil {
		t.Fatal(err)
	}
	p.Templates = map[string]string{template: environmentPreflightBytesSHA256(raw)}
	qualifiedOutputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedPolicy := findEnvironmentCreateDomainPolicy(p, "service")
	core, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedPolicy.ServiceClientIDs = append(slices.Clone(core), "service:deployment-bootstrap")
	for _, o := range qualifiedOutputs {
		qualifiedPolicy.ServiceClientIDs = append(qualifiedPolicy.ServiceClientIDs, o.Subject)
	}
	slices.Sort(qualifiedPolicy.ServiceClientIDs)
	for i := range qualifiedOutputs {
		qualifiedOutputs[i].PolicySHA256 = deploymentplan.Digest(qualifiedPolicy)
	}
	p.ProductIdentities = qualifiedOutputs
	if len(nativeConnectivity) != 0 && nativeConnectivity[0] {
		p.NativeConnectivitySHA256 = deploymentplan.Digest(environmentNativeConnectivityMarker(cfg))
	}
	for i := range p.Domains {
		if p.Domains[i].Domain == "service" {
			p.Domains[i] = qualifiedPolicy
		}
	}
	stateCfg, store, operator, a := createStateFixture(t, "dev")
	var original environmentCreatePlan
	if decodeEnvironmentJSON(a.Plan, &original) != nil {
		t.Fatal("original create fixture unavailable")
	}
	original.Bootstrap = p
	originalRaw := mustProviderRecoveryJSON(original)
	originalSHA, _ := environmentCreatePlanSHA256(originalRaw)
	q := a.Qualification
	q.PlanSHA256 = originalSHA
	generation, err := invalidateEnvironmentPreflightProofWithToken(stateCfg)
	if err != nil {
		t.Fatal(err)
	}
	a, err = publishEnvironmentCreateArtifact(t.Context(), stateCfg, generation, originalRaw, operator, q, a.ReleaseProofs, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	e, err := startEnvironmentCreateExecution(t.Context(), stateCfg, a, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	f := &productProtocolFixture{t: t, cfg: cfg, p: p, store: store, e: e, plans: map[string]*deploymentplan.Plan{}, policies: map[string]string{}, roles: map[string][]string{}, rows: map[string]managedSessionClient{}, states: map[string][]byte{}, inspections: map[string][]byte{}, bootstrap: map[string]string{}, status: "active", posts: map[string]int{}}
	f.b = &productProtocolBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	f.h = &createNormalTailHandoff{Execution: e, Pins: map[string]string{"fixture": deploymentplan.Digest("original qualified handoff")}}
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	f.rootKey, f.icaKey = key(), key()
	now := time.Now().UTC()
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "original-service-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(caLifetime), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	rootDER, _ := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &f.rootKey.PublicKey, f.rootKey)
	f.root, _ = x509.ParseCertificate(rootDER)
	icaTemplate := *rootTemplate
	icaTemplate.SerialNumber = big.NewInt(2)
	icaTemplate.Subject = pkix.Name{CommonName: "native-successor"}
	icaDER, _ := x509.CreateCertificate(rand.Reader, &icaTemplate, f.root, &f.icaKey.PublicKey, f.rootKey)
	f.ica, _ = x509.ParseCertificate(icaDER)
	issuer := func(id, kind string, der []byte) environmentCreateRegisteredIssuer {
		chain := productPEM("CERTIFICATE", der)
		if kind != "root" {
			chain += productPEM("CERTIFICATE", rootDER)
		}
		doc := map[string]any{"issuer_id": id, "environment": "dev", "trust_domain": "service", "kind": kind, "status": "active", "signer_provider": "openbao", "signer_reference": "pki-issuers/service/" + id + "/v1", "certificate_pem": productPEM("CERTIFICATE", der), "chain_pem": chain, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(der), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(chain))}
		i, err := environmentCreateRegisteredIssuerFromMap(doc, environmentPreflightBytesSHA256(rootDER))
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	root := issuer("11111111-1111-4111-8111-111111111111", "root", rootDER)
	f.next = issuer("22222222-2222-4222-8222-222222222222", "intermediate", icaDER)
	f.next.Document["parent_issuer_id"] = root.ID
	old := f.next
	if successor {
		old.ID = "33333333-3333-4333-8333-333333333333"
		old.Document = deploymentplan.Clone(old.Document)
		old.Document["issuer_id"] = old.ID
		old.Reference = "pki-issuers/service/" + old.ID + "/v1"
		old.Document["signer_reference"] = old.Reference
	}
	outputs := slices.Clone(qualifiedOutputs)
	policy := findEnvironmentCreateDomainPolicy(p, "service")
	for _, o := range outputs {
		f.subjects = append(f.subjects, o.Subject)
	}
	slices.Sort(policy.ServiceClientIDs)
	slices.Sort(f.subjects)
	for i := range p.Domains {
		if p.Domains[i].Domain == "service" {
			p.Domains[i] = policy
		}
	}
	f.p = p
	f.l = &createNormalTailProductLifecycle{ParentSHA256: e.Journal.PlanSHA256, SourceSHA256: deploymentplan.Digest(e.Journal), OwnerSHA256: deploymentplan.Digest(e.owner), Root: root, Intermediate: old, Policy: policy, Outputs: outputs, RequiresSuccessor: successor, Bootstrap: p, StorageClass: "linode-block-storage-retain", TransportCAPEM: r.TransportCAPEM, SourceOwners: map[string]string{}, SignerPolicies: []string{"original-signer"}}
	ns := p.Stack + "-video-cloud"
	add := func(o deploymentplan.Object) {
		m := deploymentplan.Map(o["metadata"])
		m["uid"] = fmt.Sprintf("%08d-aaaa-4aaa-8aaa-aaaaaaaaaaaa", len(f.b.objects)+1)
		m["resourceVersion"] = "1"
		m["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": p.Stack}
		f.b.objects[deploymentplan.ID(o)] = o
	}
	add(deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns}})
	for _, suffix := range []string{"account-manager", "secrets"} {
		add(deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": p.Stack + "-" + suffix}})
	}
	for _, name := range []string{"pki-verifier-database", "pki-migration-database", "ghcr-pull"} {
		d := environmentCreateSecret(ns, name, map[string]string{"url": "isolated-only"})
		o := deploymentplan.Clone(d.Object)
		o["data"] = deploymentplan.Object{"url": base64.StdEncoding.EncodeToString([]byte("isolated-only"))}
		delete(o, "stringData")
		add(o)
	}
	for _, name := range []string{"pki-controller", "certissuer"} {
		o := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": name}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": name}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": name}}, "spec": deploymentplan.Object{"serviceAccountName": "pki-controller", "containers": []any{deploymentplan.Object{"name": name, "image": p.Image, "env": environmentCreateEnv(map[string]string{"PKI_HOST_IDENTITY_STATE": "/var/lib/pki-service/identity/server.json", "PKI_HOST_NAME": "pki-controller." + ns + ".svc", "PKI_HOST_ROOT_SHA256": root.RootFingerprint})}}}}}}
		add(o)
		environmentPKIPod(o)["automountServiceAccountToken"] = false
		f.l.SourceOwners[deploymentplan.ID(o)] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(o), Phase: "workload"})
	}
	f.controls = func(s environmentCreateServiceCeremony, provision bool) ([]deploymentplan.Definition, error) {
		pure := r
		pure.Execution = nil
		pure.PKIClient = nil
		defs, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, pure, s)
		if err != nil {
			return nil, err
		}
		roots, issuers := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
		for d, v := range domainRoots {
			roots[d] = v
		}
		for d, v := range domainIntermediates {
			issuers[d] = v
		}
		roots["service"] = s.Root
		issuers["service"] = s.Intermediate
		defs, err = configureEnvironmentCreateFinalIssuer(p, roots, issuers, defs)
		if err != nil {
			return nil, err
		}
		var out []deploymentplan.Definition
		for _, d := range defs {
			if d.Object["kind"] != "Deployment" {
				continue
			}
			name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
			if name != "pki-controller" && name != "certissuer" {
				continue
			}
			if name == "certissuer" && !provision {
				c := mqttFoundationPrimary(d.Object)
				c["env"] = environmentCreateServiceWithoutEnv(c["env"], "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID")
			}
			if name == "pki-controller" {
				d.Object, err = configureCreateTransportServiceConsumerController(p, d.Object)
				if err != nil {
					return nil, err
				}
			}
			out = append(out, d)
		}
		return out, nil
	}
	seedSID := "55555555-5555-4555-8555-555555555555"
	seed := environmentCreateServiceCeremony{Root: root, Intermediate: old, BootstrapSessionID: seedSID, BootstrapCredentialSecret: "service-bootstrap-" + seedSID, BootstrapCredentialNamespace: ns, BootstrapCredentialNamespaces: []string{ns}, RootCRLPEM: "isolated native root CRL", RootCRLSecret: "pki-service-root-crl", CleanupBinding: environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: seedSID, NamespaceUID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SecretUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", PolicySHA256: deploymentplan.Digest("original exact policy"), KubernetesCA: base64.StdEncoding.EncodeToString([]byte(root.Certificate))}}
	maintained, err := f.controls(seed, false)
	if err != nil {
		t.Fatal(err)
	}
	f.l.SourceDefinitions = nil
	for _, d := range maintained {
		id := deploymentplan.ID(d.Object)
		deploymentplan.Map(d.Object["metadata"])["uid"] = f.l.SourceOwners[id]
		deploymentplan.Map(d.Object["metadata"])["resourceVersion"] = "1"
		labels := deploymentplan.Map(deploymentplan.Map(d.Object["metadata"])["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			deploymentplan.Map(d.Object["metadata"])["labels"] = labels
		}
		labels["rtk.realtek.com/stack"] = p.Stack
		f.b.objects[id] = deploymentplan.Clone(d.Object)
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, d)
	}
	for _, entry := range []struct {
		name string
		data map[string]string
	}{{"pki-issuer-database", map[string]string{"url": "isolated native registry DSN"}}, {"factoryenroll-runtime", map[string]string{"FACTORY_ENROLL_PRODUCTION_JWT_SECRET": "isolated native JWT authority", "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE": "isolated-products"}}} {
		d := environmentCreateSecret(ns, entry.name, entry.data)
		o := deploymentplan.Clone(d.Object)
		data := deploymentplan.Object{}
		for k, v := range entry.data {
			data[k] = base64.StdEncoding.EncodeToString([]byte(v))
		}
		delete(o, "stringData")
		o["data"] = data
		add(o)
	}
	refs, _ := environmentCreateServiceManifest(root, old)
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "pki-create-service-trust"}, "data": deploymentplan.Object{"root.pem": root.Certificate, "bundles.json": refs, "listener-crls.json": environmentCreateServiceCRLManifest(root, environmentCreateServiceState+"/private/crl/listener-root.json", old), "outbound-crls.json": environmentCreateServiceCRLManifest(root, environmentCreateServiceState+"/private/crl/outbound-root.json", old)}}
	add(trust)
	f.l.SourceOwners[deploymentplan.ID(trust)] = deploymentplan.String(deploymentplan.Map(trust["metadata"])["uid"])
	f.l.SourceTrust = []deploymentplan.Resource{{ID: deploymentplan.ID(trust), Baseline: deploymentplan.Clone(trust)}}
	for _, d := range f.l.SourceDefinitions {
		f.l.SourceTrust = append(f.l.SourceTrust, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Baseline: deploymentplan.Clone(d.Object)})
	}
	f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Clone(trust), Phase: "configuration"})
	for _, o := range outputs {
		role := strings.TrimPrefix(o.Subject, "service:")
		body := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": role}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": role}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": role}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": role, "image": p.Image, "volumeMounts": []any{deploymentplan.Object{"name": "identity", "mountPath": "/run/platform-identity", "readOnly": true}}}}, "volumes": []any{deploymentplan.Object{"name": "identity", "secret": deploymentplan.Object{"secretName": strings.Split(o.ID, "/")[2]}}}}}}}
		f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: body, Phase: "workload"})
	}
	f.roles["pki-controller-dev"] = []string{"original-controller"}
	f.roles["certissuer-dev"] = slices.Clone(f.l.SignerPolicies)
	text := "path \"original\" { capabilities = [\"read\"] }\n"
	name := "create-original-controller"
	f.policies[name] = text
	if err = e.BeginEntry("openbao-policy-"+name, "bootstrap", mustProviderRecoveryJSON(deploymentplan.Object{"environment": "dev", "policy": name, "policy_sha256": environmentPreflightBytesSHA256([]byte(text))}), now); err != nil {
		t.Fatal(err)
	}
	if err = e.RecordReceipt("openbao-policy-"+name, mustProviderRecoveryJSON(deploymentplan.Object{"environment": "dev", "policy": name, "policy_sha256": environmentPreflightBytesSHA256([]byte(text))})); err != nil {
		t.Fatal(err)
	}
	if err = e.CompleteEntry("openbao-policy-"+name, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	f.roles["pki-controller-dev"] = []string{name}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	origin, _ := url.Parse(f.server.URL)
	client := &environmentCreatePKIClient{Origin: origin, HTTP: f.server.Client(), Environment: "dev", OperatorID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Execution: e, token: "isolated-token", authenticatedAt: now}
	f.runtime = environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, PKIClient: client, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("clusters:\n- cluster:\n    certificate-authority-data: " + base64.StdEncoding.EncodeToString([]byte(root.Certificate)) + "\n"), RolloutTimeout: time.Second}, TransportCAPEM: r.TransportCAPEM, TransportTrustConfigMap: "pki-create-transport-trust", TransportSecrets: map[string]string{"pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport"}}
	f.runtime.OpenBao.RootTokenPath = filepath.Join(store.Root, "isolated-bao-token")
	f.runtime.TransportRootSHA256 = r.TransportRootSHA256
	f.runtime.TransportSecrets["account-manager"] = "pki-create-account-manager-transport"
	if err = os.WriteFile(f.runtime.OpenBao.RootTokenPath, []byte("isolated-bao-token"), 0600); err != nil {
		t.Fatal(err)
	}
	if successor {
		f.installRootCustody()
	}
	f.runtime.ProductIdentityScope = productIdentityScope(f.l)
	client.mutationScope = f.runtime.ProductIdentityScope
	bin := t.TempDir()
	script := "#!/usr/bin/env python3\nimport sys,json,urllib.request,base64,os\np=json.dumps({'args':sys.argv[1:],'input':base64.b64encode(sys.stdin.buffer.read()).decode()}).encode()\nr=json.load(urllib.request.urlopen(urllib.request.Request(os.environ['PRODUCT_PROTOCOL_BOUNDARY']+'/'+os.path.basename(sys.argv[0]),data=p,headers={'Content-Type':'application/json'})))\nsys.stdout.buffer.write(base64.b64decode(r.get('output','')))\nsys.exit(r.get('code',0))\n"
	for _, name := range []string{"kubectl", "docker"} {
		if err = os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PRODUCT_PROTOCOL_BOUNDARY", f.server.URL+"/boundary")
	return f
}

func (f *productProtocolFixture) installRootCustody() {
	paths := map[string]string{"key": "pki/custody/roots/" + f.l.Root.ID + "/ca-key.encrypted.pem", "certificate": "pki/custody/roots/" + f.l.Root.ID + "/certificate.pem", "csr": "pki/custody/roots/" + f.l.Root.ID + "/csr.pem", "passphrase": "pki/custody/service-root-v1-key-passphrase"}
	hashes := map[string]string{}
	for name, path := range paths {
		raw := []byte("isolated-encrypted-" + name)
		if name == "certificate" {
			raw = []byte(f.l.Root.Certificate)
		}
		if err := f.store.write(path, raw, false); err != nil {
			f.t.Fatal(err)
		}
		hashes[name] = environmentPreflightBytesSHA256(raw)
	}
	manifest := deploymentplan.Object{"schema": "rtk-root-custody/v1", "environment": "dev", "trust_domain": "service", "root_issuer_id": f.l.Root.ID, "certificate_fingerprint_sha256": f.l.Root.RootFingerprint, "operator_user_id": f.runtime.PKIClient.OperatorID, "paths": paths, "file_sha256": hashes}
	if err := f.store.write("pki/custody/roots/"+f.l.Root.ID+"/manifest.json", mustProviderRecoveryJSON(manifest), false); err != nil {
		f.t.Fatal(err)
	}
	sha, err := qualifyProductRootCustodyWithRunner(f.store, f.l.Root, f.runtime.PKIClient.OperatorID, func(string, string) ([]byte, error) { return f.root.RawSubjectPublicKeyInfo, nil })
	if err != nil {
		f.t.Fatal(err)
	}
	f.l.RootCustodySHA256 = sha
}

func (f *productProtocolFixture) issue(subject string) {
	if f.states[subject] != nil {
		return
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now().UTC()
	serial := big.NewInt(int64(len(f.states) + 10))
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, template, f.ica, &key.PublicKey, f.icaKey)
	leaf, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: template.Subject}, key)
	chain := productPEM("CERTIFICATE", der) + f.next.Certificate + f.l.Root.Certificate
	if subject == "service:deployment-bootstrap" {
		// OpenBao returns IssuingCA plus CAChain=[ICA,Root]. The native
		// bootstrap command preserves both, while regular Service state is3.
		chain = productPEM("CERTIFICATE", der) + f.next.Certificate + f.next.Certificate + f.l.Root.Certificate
	}
	state := deploymentplan.Object{"version": 1, "subject": subject, "current": deploymentplan.Object{"private_key_pem": productPEM("PRIVATE KEY", keyDER), "certificate_chain_pem": chain, "installed_at": now}}
	f.states[subject] = mustProviderRecoveryJSON(state)
	f.inspections[subject] = mustProviderRecoveryJSON(deploymentplan.Object{"subject": subject, "fingerprint": environmentPreflightBytesSHA256(der), "public_key_sha256": environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo), "root_sha256": f.l.Root.RootFingerprint, "state_sha256": environmentPreflightBytesSHA256(f.states[subject]), "pending": false})
	f.rows[subject] = managedSessionClient{Subject: subject, Caller: "service:deployment-bootstrap", Request: "native-" + subject, Issuer: f.next.ID, Fingerprint: environmentPreflightBytesSHA256(der), Status: "succeeded", CSR: productPEM("CERTIFICATE REQUEST", csr), Certificate: productPEM("CERTIFICATE", der)}
	if subject == "service:deployment-bootstrap" {
		f.bootstrap = map[string]string{"bootstrap.key": productPEM("PRIVATE KEY", keyDER), "bootstrap.csr": productPEM("CERTIFICATE REQUEST", csr), "bootstrap.crt": chain}
	}
}

func (f *productProtocolFixture) apply(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
	full := "product-identities-" + productIdentityScope(f.l)[:12] + "-" + stage
	if f.unscoped {
		full = stage
	}
	short := stage
	if strings.HasPrefix(short, "product-identities-") && len(short) > len("product-identities-")+13 {
		short = short[len("product-identities-")+13:]
	}
	p := f.plans[full]
	if p == nil {
		live := []deploymentplan.Object{}
		for _, o := range f.b.objects {
			live = append(live, o)
		}
		var err error
		p, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(f.p.Images), BindingsSHA256: environmentCreateStageBindingsSHA(full, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
		if err != nil {
			return err
		}
		f.plans[full] = p
	}
	execute := executeCreateNormalTailStageBackend
	if f.unscoped {
		// Fresh stages use their actual ordinary immutable input and progress
		// readers; they do not borrow a normal-tail handoff as authority.
		execute = func(ctx context.Context, store secretStore, _ *createNormalTailHandoff, stage string, p *deploymentplan.Plan, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer, capture func(context.Context) error) error {
			return executeEnvironmentCreateStagePlan(ctx, store, f.e, stage, p, backend, qualify, fence, accept, capture, out)
		}
	}
	err := execute(ctx, f.store, f.h, full, p, f.b, func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, f.b, f.store.Root)
	}, func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, f.b) }, io.Discard, func(context.Context) error { return nil })
	if err != nil {
		return err
	}
	if short == "service-session" {
		for _, d := range defs {
			if d.Object["kind"] == "Job" {
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
				f.session = mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID")
				f.expires = time.Now().UTC().Add(time.Hour)
				f.issue("service:deployment-bootstrap")
			}
		}
	}
	if strings.HasPrefix(short, "enroll-") {
		f.issue("service:" + strings.TrimPrefix(short, "enroll-"))
	}
	if f.failStage == stage {
		f.failStage = ""
		return fmt.Errorf("isolated interruption after original %s", stage)
	}
	return nil
}

func (f *productProtocolFixture) serve(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/boundary/") {
		f.serveBoundary(w, r)
		return
	}
	if r.Method == http.MethodPost {
		f.posts[r.URL.Path]++
	}
	if r.URL.Path == "/v1/platform/service-workloads" {
		var approval deploymentplan.Object
		json.NewDecoder(r.Body).Decode(&approval)
		if approval["certificate_subject"] == "" {
			http.Error(w, "bad approval", 400)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/v1/platform/pki/operations" {
		var request environmentCreateIssuerRequest
		json.NewDecoder(r.Body).Decode(&request)
		f.operation = environmentCreateIssuerOperation{ID: "44444444-4444-4444-8444-444444444444", IssuerID: f.next.ID, Digest: deploymentplan.Digest(request), Status: "requested"}
		json.NewEncoder(w).Encode(f.operation)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/platform/pki/operations/") {
		switch {
		case strings.HasSuffix(r.URL.Path, "/authorize"):
			f.next.Document["status"] = "approved"
		case strings.HasSuffix(r.URL.Path, "/provision"):
			f.next.Document["status"] = "provisioning"
			csr, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: f.ica.Subject}, f.icaKey)
			f.next.Document["csr_pem"] = productPEM("CERTIFICATE REQUEST", csr)
			json.NewEncoder(w).Encode(f.next.Document)
			return
		case strings.HasSuffix(r.URL.Path, "/import"):
			f.next.Document["status"] = "ready"
		case strings.HasSuffix(r.URL.Path, "/activate"):
			f.next.Document["status"] = "active"
		default:
			json.NewEncoder(w).Encode(f.operation)
			return
		}
		w.WriteHeader(204)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/finalize") {
		if len(f.installed) != len(f.subjects) {
			http.Error(w, "missing real workload ACK", 409)
			return
		}
		f.final = environmentCreateBootstrapFinalization{Environment: "dev", SessionID: f.session, LeafSHA256: f.rows["service:deployment-bootstrap"].Fingerprint, CRLSHA256: deploymentplan.Digest("real-revocation-crl"), CleanupSHA256: deploymentplan.Digest("real-cleanup"), FinalizedAt: time.Now().UTC()}
		f.status = "sealed"
		json.NewEncoder(w).Encode(f.final)
		return
	}
	if strings.HasSuffix(r.URL.Path, "/crl") {
		issuer, cert, key := f.next, f.ica, f.icaKey
		if strings.Contains(r.URL.Path, f.l.Root.ID) {
			issuer, cert, key = f.l.Root, f.root, f.rootKey
		} else if f.next.Document["status"] != "active" {
			http.Error(w, "native CRL worker has no active successor", http.StatusServiceUnavailable)
			return
		}
		now := time.Now().UTC().Truncate(time.Second)
		der, _ := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}, cert, key)
		json.NewEncoder(w).Encode(deploymentplan.Object{"issuer_id": issuer.ID, "crl_sha256": environmentPreflightBytesSHA256(der), "crl_number": "1", "crl_pem": productPEM("X509 CRL", der), "this_update": now.Add(-time.Minute), "next_update": now.Add(time.Hour)})
		return
	}
	if strings.HasSuffix(r.URL.Path, f.l.Root.ID) {
		json.NewEncoder(w).Encode(f.l.Root.Document)
		return
	}
	if strings.HasSuffix(r.URL.Path, f.next.ID) {
		json.NewEncoder(w).Encode(f.next.Document)
		return
	}
	http.Error(w, "unexpected isolated native operation", 500)
}

func (f *productProtocolFixture) serveBoundary(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Args  []string `json:"args"`
		Input string   `json:"input"`
	}
	json.NewDecoder(r.Body).Decode(&request)
	args := request.Args
	input, _ := base64.StdEncoding.DecodeString(request.Input)
	respond := func(raw []byte, code int) {
		json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(raw), "code": code})
	}
	if strings.HasSuffix(r.URL.Path, "/docker") {
		if slices.Contains(args, "openssl") {
			respond(f.root.RawSubjectPublicKeyInfo, 0)
			return
		}
		if i := slices.Index(args, "--out"); i >= 0 {
			path := filepath.Join(f.store.Root, strings.TrimPrefix(args[i+1], "/store/"), "certificate.pem")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				respond(nil, 1)
				return
			}
			if err := os.WriteFile(path, []byte(f.next.Certificate), 0600); err != nil {
				respond(nil, 1)
				return
			}
			respond(nil, 0)
			return
		}
		respond(nil, 1)
		return
	}
	joined := strings.Join(args, " ")
	if slices.Contains(args, "delete") {
		for _, arg := range args {
			if strings.HasPrefix(arg, "--raw=/api/v1/namespaces/") {
				parts := strings.Split(strings.TrimPrefix(arg, "--raw=/api/v1/namespaces/"), "/")
				if len(parts) != 3 || parts[1] != "secrets" {
					respond(nil, 1)
					return
				}
				id := "Secret/" + parts[0] + "/" + parts[2]
				o := f.b.objects[id]
				var options struct {
					Preconditions struct {
						UID     string `json:"uid"`
						Version string `json:"resourceVersion"`
					} `json:"preconditions"`
				}
				if json.Unmarshal(input, &options) != nil || o == nil || deploymentplan.Map(o["metadata"])["uid"] != options.Preconditions.UID || deploymentplan.Map(o["metadata"])["resourceVersion"] != options.Preconditions.Version {
					respond(nil, 1)
					return
				}
				delete(f.b.objects, id)
				respond(nil, 0)
				return
			}
		}
		respond(nil, 1)
		return
	}
	if i := slices.Index(args, "get"); i >= 0 {
		kind := args[i+1]
		name := ""
		ns := ""
		for j, a := range args {
			if a == "--namespace" && j+1 < len(args) {
				ns = args[j+1]
			}
		}
		if i+2 < len(args) && !strings.HasPrefix(args[i+2], "-") {
			name = args[i+2]
		}
		if name == "" && (kind == "namespace" || strings.Contains(kind, ",") || kind == "validatingwebhookconfiguration") {
			items := []deploymentplan.Object{}
			for _, o := range f.b.objects {
				m := deploymentplan.Map(o["metadata"])
				requestedKind := slices.Contains(strings.Split(kind, ","), strings.ToLower(deploymentplan.String(o["kind"])))
				if kind == "namespace" && o["kind"] == "Namespace" || strings.Contains(kind, ",") && requestedKind && (ns != "" && m["namespace"] == ns || ns == "" && (m["namespace"] == nil || m["namespace"] == "") && o["kind"] != "Namespace") {
					items = append(items, o)
				}
			}
			respond(mustProviderRecoveryJSON(deploymentplan.Object{"apiVersion": "v1", "kind": "List", "items": items}), 0)
			return
		}
		o := f.b.objects[kind+"/"+ns+"/"+name]
		if o == nil {
			respond(nil, 0)
		} else {
			respond(mustProviderRecoveryJSON(o), 0)
		}
		return
	}
	if strings.Contains(joined, "render-openbao-policy") {
		id := args[len(args)-1]
		respond(mustProviderRecoveryJSON(environmentCreateOpenBaoIssuerPolicies{Environment: "dev", IssuerID: id, Mount: "pki-issuers/service/" + id + "/v1", ControllerPolicy: "controller text", SignerPolicy: "signer text", RecoveryPolicy: "recovery text", ServiceClientSignerPolicy: "client text"}), 0)
		return
	}
	if strings.Contains(joined, "openbao-0") {
		script := args[len(args)-1]
		lines := strings.Split(script, "\n")
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[0] == "bao" && fields[1] == "policy" && fields[2] == "write" {
				_, text, _ := bytes.Cut(input, []byte("\n"))
				f.policies[fields[3]] = string(text)
			}
			if len(fields) >= 3 && fields[0] == "bao" && fields[1] == "write" && strings.HasPrefix(fields[2], "auth/kubernetes/role/") {
				name := strings.TrimPrefix(fields[2], "auth/kubernetes/role/")
				for _, field := range fields {
					if strings.HasPrefix(field, "token_policies=") {
						f.roles[name] = strings.Split(strings.TrimPrefix(field, "token_policies="), ",")
					}
				}
			}
		}
		if strings.Contains(script, "bao policy read ") {
			name := strings.Fields(strings.Split(script, "bao policy read ")[1])[0]
			respond([]byte(f.policies[name]), 0)
			return
		}
		if strings.Contains(script, "bao read -format=json auth/kubernetes/role/") {
			name := strings.Fields(strings.Split(script, "bao read -format=json auth/kubernetes/role/")[1])[0]
			account := strings.TrimSuffix(name, "-dev")
			if strings.HasPrefix(name, "pki-product-bootstrap-") {
				account = name
			}
			respond(mustProviderRecoveryJSON(deploymentplan.Object{"data": deploymentplan.Object{"bound_service_account_names": []string{account}, "bound_service_account_namespaces": []string{f.p.Stack + "-video-cloud"}, "token_policies": f.roles[name], "audience": "openbao", "token_no_default_policy": true, "token_ttl": 900, "token_max_ttl": 900}}), 0)
			return
		}
		respond(nil, 1)
		return
	}
	if strings.Contains(joined, "registry-read") {
		q := args[len(args)-1]
		switch {
		case strings.Contains(q, "pki_bundle_acknowledgments"):
			ready := f.next
			ready.Document = deploymentplan.Clone(f.next.Document)
			ready.Status, ready.Document["status"] = "ready", "ready"
			if !f.trustReadersMatch(ready) {
				respond(nil, 1)
				return
			}
			respond(mustProviderRecoveryJSON(f.l.Policy.BundleConsumers), 0)
		case strings.Contains(q, "pki_crl_acknowledgments"):
			if !f.trustReadersMatch(f.next) {
				respond(nil, 1)
				return
			}
			consumers, err := productIdentityCRLConsumers(f.l)
			if err != nil {
				respond(nil, 1)
				return
			}
			respond(mustProviderRecoveryJSON(consumers), 0)
		case strings.Contains(q, "pki_service_client_issuances"):
			var rows []managedSessionClient
			for subject, row := range f.rows {
				if strings.Contains(q, "subject='"+subject+"'") {
					rows = append(rows, row)
				}
			}
			respond(mustProviderRecoveryJSON(rows), 0)
		case strings.Contains(q, "finalized_at IS NOT NULL"):
			respond(mustProviderRecoveryJSON(f.final), 0)
		case strings.Contains(q, "'installed',installed_subjects"):
			respond(mustProviderRecoveryJSON(productSessionInstallations{Session: f.session, Issuer: f.next.ID, Root: f.l.Root.RootFingerprint, Subjects: f.subjects, Installed: f.installed, Status: f.status, Expires: f.expires}), 0)
		default:
			respond(mustProviderRecoveryJSON(deploymentplan.Object{"session_id": f.session, "issuer_id": f.next.ID, "root_sha256": f.l.Root.RootFingerprint, "bootstrap_fingerprint": f.rows["service:deployment-bootstrap"].Fingerprint, "status": f.status, "subjects": f.subjects, "expires_at": f.expires}), 0)
		}
		return
	}
	if strings.Contains(joined, "cleanup-credentials") {
		files := map[string]string{}
		for name, raw := range f.bootstrap {
			files[name] = environmentPreflightBytesSHA256([]byte(raw))
		}
		f.marker = append(mustProviderRecoveryJSON(managedLateCustodyTombstone{Schema: "rtk-cloud.bootstrap-credentials/v1", Finalization: f.final, Root: f.l.Root.RootFingerprint, Files: files}), '\n')
		f.bootstrap = map[string]string{}
		delete(f.b.objects, "Secret/"+f.p.Stack+"-video-cloud/service-bootstrap-"+f.session)
		respond(nil, 0)
		return
	}
	if strings.Contains(joined, "bootstrap.finalized.json") {
		respond(f.marker, 0)
		return
	}
	if strings.Contains(joined, "test ! -e /run/pki-bootstrap/bootstrap.key") {
		code := 0
		if len(f.bootstrap) != 0 {
			code = 1
		}
		respond(nil, code)
		return
	}
	if i := slices.Index(args, "cat"); i >= 0 {
		path := args[i+1]
		if strings.HasPrefix(path, "/run/pki-bootstrap/") {
			respond([]byte(f.bootstrap[filepath.Base(path)]), 0)
			return
		}
		for subject, raw := range f.states {
			if subject == "service:deployment-bootstrap" {
				continue
			}
			role := strings.TrimPrefix(subject, "service:")
			if strings.Contains(joined, "deployment/pki-product-"+role+"-") {
				respond(raw, 0)
				return
			}
		}
	}
	if slices.Contains(args, "inspect") {
		for subject, raw := range f.inspections {
			role := strings.TrimPrefix(subject, "service:")
			if strings.Contains(joined, "deployment/pki-product-"+role+"-") {
				respond(raw, 0)
				return
			}
		}
	}
	respond(nil, 1)
}

func (f *productProtocolFixture) trustReadersMatch(issuer environmentCreateRegisteredIssuer) bool {
	defs, err := productIdentityTrustStageDefinitions(f.l, issuer)
	if err != nil {
		return false
	}
	for _, d := range defs {
		if !deploymentplan.Matches(deploymentplan.Projection(d.Object), f.b.objects[deploymentplan.ID(d.Object)]) {
			return false
		}
	}
	return true
}

func TestProductIdentitySuccessorResumesOriginalTrustPhasesWithoutReadyReplay(t *testing.T) {
	for _, stage := range []string{"successor-trust", "successor-crls"} {
		t.Run(stage, func(t *testing.T) {
			f := newProductProtocolFixture(t, true)
			f.failStage = stage
			if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, f.apply); err == nil {
				t.Fatal("original trust interruption was not exercised")
			}
			activate := "/v1/platform/pki/operations/" + f.operation.ID + "/activate"
			before := f.posts[activate]
			if before != map[string]int{"successor-trust": 0, "successor-crls": 1}[stage] {
				t.Fatal("issuer activated before original READY bundle ACKs")
			}
			if stage == "successor-trust" {
				_, err := readProductCurrentCRL(t.Context(), f.runtime.PKIClient, f.next)
				var unavailable *environmentCreatePKIHTTPFailure
				if !errors.As(err, &unavailable) || unavailable.Status != http.StatusServiceUnavailable {
					t.Fatal("fixture allowed CRL publication for a READY successor", err)
				}
			}
			f.failStage = "service-credential-secrets"
			if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, f.apply); err == nil || f.session == "" {
				t.Fatal("same original active issuer could not progress to its credential child", err)
			}
			if f.posts[activate] != 1 || !f.trustReadersMatch(f.next) {
				t.Fatal("retry replayed activation, restored READY manifests or omitted actual current readers")
			}
		})
	}
}

func TestProductIdentityProductionProtocolAllFiveAndOriginalSessionResume(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(fmt.Sprintf("successor-%v", successor), func(t *testing.T) {
			f := newProductProtocolFixture(t, successor)
			before := productIdentityScope(f.l)
			// Stop after a genuine accepted credential child, before cleanup controls
			// and the public product-session record. The resumed path must keep it.
			f.failStage = "service-credential-secrets"
			if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, f.apply); err == nil {
				t.Fatal("interruption did not stop original protocol")
			}
			originalSID := f.session
			originalCert := f.rows["service:deployment-bootstrap"].Fingerprint
			originalKey, originalChain := f.bootstrap["bootstrap.key"], f.bootstrap["bootstrap.crt"]
			bootstrapPair, pairErr := tls.X509KeyPair([]byte(originalChain), []byte(originalKey))
			if pairErr != nil || len(bootstrapPair.Certificate) != 4 || !bytes.Equal(bootstrapPair.Certificate[1], bootstrapPair.Certificate[2]) {
				t.Fatal("fixture must reproduce native OpenBao duplicated-ICA bootstrap chain")
			}

			if err := executeProductIdentityProtocol(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, f.apply); err != nil {
				t.Fatal("actual producer failed original session resume", err)
			}
			installed, err := readProductInstalled(f.store, f.l)
			if err != nil {
				t.Fatal(err)
			}
			if f.bootstrap["bootstrap.key"] != originalKey || f.bootstrap["bootstrap.crt"] != originalChain {
				t.Fatal("existing native bootstrap key/chain bytes changed during resume")
			}
			if len(installed.Outputs) != 5 || f.session != originalSID || f.rows["service:deployment-bootstrap"].Fingerprint != originalCert || productIdentityScope(f.l) != before {
				t.Fatal("producer regenerated identity, changed source, or omitted product")
			}

			for _, output := range f.l.Outputs {
				var state struct {
					Current struct {
						Chain string `json:"certificate_chain_pem"`
						Key   string `json:"private_key_pem"`
					} `json:"current"`
				}
				if json.Unmarshal(f.states[output.Subject], &state) != nil {
					t.Fatal("regular native product state missing")
				}
				pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.Key))
				if err != nil || len(pair.Certificate) != 3 {
					t.Fatal("regular Service producer must retain its canonical three-certificate contract")
				}
			}
			if err = verifyProductIdentityOutputs(t.Context(), f.cfg, f.runtime, f.l, installed); err != nil {
				t.Fatal(err)
			}
			// This is the actual next production boundary after provisioning:
			// ordinary binding observation must see a closed native issuer.
			live := []deploymentplan.Object{}
			for _, o := range f.b.objects {
				live = append(live, o)
			}
			_, _, formal := completeEnvironmentRenderFixture("dev")
			if _, err = observeEnvironmentBindings("dev", formal, live); err != nil {
				t.Fatal("ordinary bindings rejected real completed producer outputs", err)
			}
			if f.posts["/v1/platform/service-workloads"] != 5 {
				t.Fatal("workload approval missing")
			}
			for _, o := range f.l.Outputs {
				actual := f.b.objects[o.ID]
				raw, _ := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(actual["data"])["server-ca.crt"]))
				if string(raw) != f.l.Root.Certificate || string(raw) == f.runtime.TransportCAPEM {
					t.Fatal("temporary transport CA substituted for real Service listener root")
				}
			}
			f.installed = slices.Clone(f.subjects)
			slices.Reverse(f.installed)
			if err = completeProductIdentitySession(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, installed, nil); err != nil {
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, environmentCreateServiceFinalizationKey(f.runtime))
				var saved struct {
					Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
					Tombstone json.RawMessage                        `json:"custody_tombstone"`
					UIDs      map[string]string                      `json:"credential_uids"`
				}
				readErr := createOpenBaoACKCompletionRead(f.store, filepath.Join(environmentCreateCeremonyDirectory(f.runtime, "service"), "finalized.json"), &saved)
				var receipt struct {
					Native environmentCreateBootstrapFinalization `json:"native_finalization"`
					Absent bool                                   `json:"copies_absent"`
				}
				var tomb managedLateCustodyTombstone
				receiptErr := decodeEnvironmentJSON(entry.Receipt, &receipt)
				tombErr := decodeEnvironmentJSON(saved.Tombstone, &tomb)
				t.Logf("public predicate booleans: read=%v completed=%v intent=%v receipt=%v tomb=%v absent=%v receiptEqual=%v nativeTombEqual=%v root=%v files=%d uid=%v sid=%v time=%v", readErr == nil, entry.State == "completed", entry.IntentSHA256 == deploymentplan.Digest(deploymentplan.Object{"session_id": installed.Ceremony.BootstrapSessionID, "issuer_id": installed.Ceremony.Intermediate.ID, "cleanup_binding": installed.Ceremony.CleanupBinding}), receiptErr == nil, tombErr == nil, receipt.Absent, managedLateFinalizationEqual(receipt.Native, saved.Native), managedLateFinalizationEqual(tomb.Finalization, saved.Native), tomb.Root == f.l.Root.RootFingerprint, len(tomb.Files), deploymentplan.Digest(saved.UIDs) == deploymentplan.Digest(installed.Ceremony.CredentialUIDs), saved.Native.SessionID == installed.Ceremony.BootstrapSessionID, !saved.Native.FinalizedAt.After(entry.CompletedAt))
				t.Fatal("actual native finalization protocol failed", err)
			}
			// Simulate the two real durable crash boundaries without altering the
			// native result: artifact before receipt, and allocated before close.
			for _, state := range []string{"pending", "allocated"} {
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, environmentCreateServiceFinalizationKey(f.runtime))
				receipt := slices.Clone(entry.Receipt)
				entry.State = state
				entry.CompletedAt = time.Time{}
				if state == "pending" {
					entry.Receipt = nil
				}
				if err = f.e.save(); err != nil {
					t.Fatal(err)
				}
				if err = completeProductIdentitySession(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, installed, nil); err != nil {
					t.Fatalf("original %s finalization failed to reconcile: %v", state, err)
				}
				entry = createOpenBaoACKCompletionEntry(f.e.Journal, environmentCreateServiceFinalizationKey(f.runtime))
				if entry.State != "completed" || !bytes.Equal(entry.Receipt, receipt) {
					t.Fatal("native finalization receipt was replaced")
				}
			}
			if err = completeProductIdentitySession(t.Context(), f.cfg, f.p, f.store, f.runtime, f.l, installed, nil); err != nil {
				t.Fatal("completed original finalization did not resume", err)
			}
			if f.posts["/v1/platform/pki/deployment-bootstrap/"+f.session+"/finalize"] != 1 {
				t.Fatal("native finalizer replayed")
			}
			defs, err := productSteadyDefinitions(f.cfg, f.l, installed)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.apply(t.Context(), "steady", defs); err != nil {
				t.Fatal(err)
			}
			for _, o := range f.l.Outputs {
				if f.b.writes[o.ID] != 1 {
					t.Fatal("original mirror was recreated")
				}
			}
		})
	}
}

func TestProductIdentityFreshFourteenFinalizationRestoresBeforeSeedCeremony(t *testing.T) {
	f := newProductProtocolFixture(t, false)
	f.unscoped = true
	f.runtime.ProductIdentityScope = ""
	f.runtime.PKIClient.mutationScope = ""
	var err error
	f.subjects, err = environmentCreatePlannedServiceSubjects(f.cfg, f.p)
	if err != nil || len(f.subjects) != 14 {
		t.Fatal("fresh original policy", err)
	}
	crl, err := readProductCurrentCRL(t.Context(), f.runtime.PKIClient, f.l.Root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := createEnvironmentServiceSession(t.Context(), f.cfg, f.p, f.store, f.runtime, environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next, RootCRLPEM: crl, RootCRLSecret: "pki-service-root-crl"}, f.subjects, f.apply)
	if err != nil {
		t.Fatal("original fourteen-subject session", err)
	}
	controls, err := f.controls(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.apply(t.Context(), "managed-complete-issuer", controls); err != nil {
		t.Fatal("original maintained temporary issuer stage", err)
	}
	if err = executeEnvironmentCreateFreshProductIdentities(t.Context(), f.cfg, f.p, environmentRelease{}, f.store, f.runtime, s, f.apply); err != nil {
		t.Fatal("actual fresh five-output producer", err)
	}
	program, err := readFreshProductProgram(f.store, f.e)
	if err != nil {
		t.Fatal(err)
	}
	l := &program.Lifecycle
	installed, err := readProductInstalled(f.store, l)
	if err != nil {
		t.Fatal(err)
	}
	bindings := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: f.p.Stack, Owners: map[string]string{}}
	for id, o := range f.b.objects {
		bindings.Owners[id] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
	}
	if err = applyEnvironmentFreshProductBindings(t.Context(), f.cfg, f.e, &bindings); err != nil {
		t.Fatal(err)
	}
	if _, err = writeProductArtifact(f.store, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/bindings.json"), bindings); err != nil {
		t.Fatal(err)
	}
	freshApply := prepareFreshProductWorkload(t, f, l, installed, bindings)
	f.installed = slices.Clone(f.subjects)
	slices.Reverse(f.installed)
	if err = finalizeEnvironmentCreateServiceSession(t.Context(), f.runtime.PKIClient, f.cfg, f.p, f.store, f.runtime, s, nil); err != nil {
		t.Fatal("actual original14 native finalizer", err)
	}
	if _, err = verifyProductFinalization(t.Context(), f.cfg, f.runtime, l, installed); err != nil {
		t.Fatal(err)
	}
	if err = completeFreshProductPreparations(t.Context(), f.cfg, f.store, f.e, l, installed, f.runtime.Backend, freshApply); err != nil {
		t.Fatal("fresh standard preparation cleanup", err)
	}
	_, _, release := completeEnvironmentRenderFixture("dev")
	before := deploymentplan.Digest(f.e.Journal)
	calls := f.posts["/v1/platform/pki/deployment-bootstrap/"+s.BootstrapSessionID+"/finalize"]
	got, restored, err := restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime)
	if err != nil || !restored || got.ProductIdentities == nil || got.ProductIdentities.SessionID != s.BootstrapSessionID || got.ProductIdentities.IssuerID != s.Intermediate.ID {
		t.Fatal("finalized fresh original bootstrap did not restore", err)
	}
	if before != deploymentplan.Digest(f.e.Journal) || calls != f.posts["/v1/platform/pki/deployment-bootstrap/"+s.BootstrapSessionID+"/finalize"] {
		t.Fatal("readonly completed restore replayed original operations")
	}
	for _, mode := range []string{"owner", "mirror", "native", "saved-program"} {
		t.Run(mode, func(t *testing.T) {
			id := l.Outputs[0].ID
			o := deploymentplan.Clone(f.b.objects[id])
			native := f.final
			programPath := filepath.Join(f.store.Root, freshProductProgramPath(f.e.Journal.PlanSHA256))
			raw, _ := os.ReadFile(programPath)
			switch mode {
			case "owner":
				deploymentplan.Map(f.b.objects[id]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "mirror":
				f.b.objects[id]["immutable"] = false
			case "native":
				f.final.CleanupSHA256 = deploymentplan.Digest("different cleanup")
			case "saved-program":
				os.WriteFile(programPath, []byte("{}"), 0600)
			}
			if _, ok, err := restoreEnvironmentFreshProductBootstrap(t.Context(), f.cfg, f.p, release, f.store, f.runtime); err == nil || ok {
				t.Fatal("changed finalized original source accepted")
			}
			f.b.objects[id] = o
			f.final = native
			os.WriteFile(programPath, raw, 0600)
		})
	}
}
