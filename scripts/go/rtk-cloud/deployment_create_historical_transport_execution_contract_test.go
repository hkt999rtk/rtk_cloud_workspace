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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These are external native signing responses, backed by the exact authorities
// already published by the historical fixture. No source/admission reader is
// replaced. The resulting rows can be retained across all later seed renewals.
type historicalTransportIdentityObservations struct {
	Clients    []managedSessionClient
	Servers    []managedTransportServerEvidence
	ClientKeys map[string]string
	ServerKeys map[string]string
}

func historicalTransportSignedLeaf(t *testing.T, issuer environmentCreateRegisteredIssuer, signer *ecdsa.PrivateKey, subject string, names []string, remaining ...time.Duration) (certificate, keyPEM, csrPEM string) {
	t.Helper()
	authority, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
	if err != nil || signer == nil {
		t.Fatal("original native signing authority unavailable", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	lifetime := 3 * time.Hour
	if len(remaining) == 1 {
		lifetime = remaining[0]
	} else if len(remaining) != 0 {
		t.Fatal("one optional leaf lifetime is permitted")
	}
	usage := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if len(names) > 0 {
		usage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: subject}, DNSNames: slices.Clone(names), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(lifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usage, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, leaf, authority, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject, DNSNames: slices.Clone(names)}, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private})), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: request}))
}

func historicalTransportIdentities(t *testing.T, f *acceptedCoreExecutionContract, roots, intermediates map[string]environmentCreateRegisteredIssuer, issuerKeys map[string]*ecdsa.PrivateKey) historicalTransportIdentityObservations {
	t.Helper()
	out := historicalTransportIdentityObservations{ClientKeys: map[string]string{}, ServerKeys: map[string]string{}}
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range subjects {
		count := 1
		if subject == "service:video-cloud-api" {
			count = 3
		}
		for i := 0; i < count; i++ {
			cert, key, csr := historicalTransportSignedLeaf(t, f.service.Intermediate, f.issuerKey, subject, nil)
			leaf, err := parseFirstCertificatePEM([]byte(cert))
			if err != nil {
				t.Fatal(err)
			}
			request := fmt.Sprintf("%s:original-native-client:%d", subject, i)
			out.Clients = append(out.Clients, managedSessionClient{Subject: subject, Caller: subject, Request: request, Issuer: f.service.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Status: "succeeded", CSR: csr, Certificate: cert})
			out.ClientKeys[request] = key
		}
	}
	slices.SortFunc(out.Clients, func(a, b managedSessionClient) int {
		return strings.Compare(a.Subject+"\x00"+a.Caller+"\x00"+a.Request, b.Subject+"\x00"+b.Caller+"\x00"+b.Request)
	})
	for _, domain := range []string{"mqtt", "openbao_tls"} {
		issuer := intermediates[domain]
		if issuer.RootFingerprint != roots[domain].RootFingerprint {
			t.Fatal("transport signing authority differs from original Root", domain)
		}
		names, err := environmentCreateTransportNames(f.p.Plan.Bootstrap, domain)
		if err != nil {
			t.Fatal(err)
		}
		caller := "service:emqx-pki"
		if domain == "openbao_tls" {
			caller = "service:openbao"
		}
		cert, key, csr := historicalTransportSignedLeaf(t, issuer, issuerKeys[issuer.ID], names[0], names)
		leaf, err := parseFirstCertificatePEM([]byte(cert))
		if err != nil {
			t.Fatal(err)
		}
		out.Servers = append(out.Servers, managedTransportServerEvidence{Domain: domain, Caller: caller, Request: f.session + ":" + strings.TrimPrefix(caller, "service:") + ":server", Issuer: issuer.ID, Status: "succeeded", Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), CSR: csr, Certificate: cert, DNS: slices.Clone(names)})
		out.ServerKeys[domain] = key
	}
	if !validateManagedSessionClients(out.Clients, f.service, subjects, time.Now().UTC()) {
		t.Fatal("genuine native client signing fixture is incomplete")
	}
	return out
}

func installHistoricalTransportProtocol(t *testing.T, f *acceptedCoreExecutionContract, identity historicalTransportIdentityObservations) {
	t.Helper()
	f.runtime.Backend = f.backend
	f.protocol["historical_clients"], f.protocol["historical_servers"] = identity.Clients, identity.Servers
	f.protocol["historical_client_keys"] = identity.ClientKeys
	f.publish(t)
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as r:d=json.load(r)
a=sys.argv[1:];joined=' '.join(a)
if 'get' in a and a[a.index('get')+1]=='persistentvolumes':
 print(json.dumps({'apiVersion':'v1','kind':'PersistentVolumeList','items':[v for v in d['objects'].values() if v['kind']=='PersistentVolume']}))
elif 'FROM pki_server_issuances' in joined and "domain IN ('mqtt','openbao_tls')" in joined and "json_build_object('domain',domain,'request_id',request_id)" in joined:
 print(json.dumps([{'domain':v['domain'],'request_id':v['request_id']} for v in sorted(d['historical_servers'],key=lambda x:x['domain'])]))
elif 'FROM pki_server_issuances' in joined and "domain IN ('mqtt','openbao_tls')" in joined:
 print(json.dumps(d['historical_servers']))
elif 'FROM pki_service_client_issuances' in joined and "subject<>'service:deployment-bootstrap'" in joined:
 print(json.dumps(d['historical_clients']))
else:os.execv(os.environ['RTK_HISTORICAL_TRANSPORT_ORIGINAL'],[os.environ['RTK_HISTORICAL_TRANSPORT_ORIGINAL']]+a)
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_TRANSPORT_ORIGINAL", original)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestHistoricalTransportProtocolProjectsOnlySelectedServerRequestFields(t *testing.T) {
	f, _, _, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	requests := []managedTransportServerEvidence{
		{Domain: "mqtt", Request: f.session + ":emqx-pki:server"},
		{Domain: "openbao_tls", Request: f.session + ":openbao:server"},
	}
	installHistoricalTransportProtocol(t, f, historicalTransportIdentityObservations{Servers: requests})
	runtime := f.runtime
	runtime.Execution, runtime.Backend = f.e, f.backend
	query := "SELECT COALESCE(json_agg(json_build_object('domain',domain,'request_id',request_id) ORDER BY domain),'[]'::json) FROM pki_server_issuances WHERE environment='" + f.cfg.Environment + "' AND domain IN ('mqtt','openbao_tls')"
	raw, err := readEnvironmentCreateRegistryJSON(t.Context(), f.cfg, runtime, query)
	var projected []struct {
		Domain  string `json:"domain"`
		Request string `json:"request_id"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &projected) != nil || len(projected) != 2 || projected[0].Domain != "mqtt" || projected[0].Request != requests[0].Request || projected[1].Domain != "openbao_tls" || projected[1].Request != requests[1].Request {
		t.Fatal("native server request projection returned unselected fields or changed rows", err, string(raw))
	}
}

// Model native CSI binding only for claims that the real stages have already
// created. A repeated observation preserves existing PV/PVC identities. The
// production capture writes and authenticates the actual durable ledger.
func historicalTransportCaptureVolumes(t *testing.T, f *acceptedCoreExecutionContract) environmentCreateResourceLedger {
	t.Helper()
	ids := []string{}
	for id, object := range f.native.objects {
		if object["kind"] == "PersistentVolumeClaim" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	used := map[string]bool{}
	for _, object := range f.native.objects {
		if object["kind"] == "PersistentVolume" {
			handle := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(object["spec"])["csi"])["volumeHandle"])
			used[strings.SplitN(handle, "-", 2)[0]] = true
		}
	}
	ordinal := 10000
	for _, id := range ids {
		claim := f.native.objects[id]
		meta := deploymentplan.Map(claim["metadata"])
		spec := deploymentplan.Map(claim["spec"])
		if spec == nil {
			t.Fatal("created claim lacks native spec", id)
		}
		if spec["volumeName"] != nil {
			continue
		}
		uid := deploymentplan.String(meta["uid"])
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			t.Fatal("created claim has no original native owner", id)
		}
		for used[strconv.Itoa(ordinal)] {
			ordinal++
		}
		nativeID := strconv.Itoa(ordinal)
		used[nativeID] = true
		pvName := "pvc-" + uid
		hash := deploymentplan.Digest("historical-native-pv:" + uid)
		pvUID := hash[:8] + "-" + hash[8:12] + "-4" + hash[13:16] + "-8" + hash[17:20] + "-" + hash[20:32]
		spec["volumeName"] = pvName
		claim["status"] = deploymentplan.Object{"phase": "Bound"}
		pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": pvName, "uid": pvUID, "resourceVersion": "1"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": meta["namespace"], "name": meta["name"], "uid": uid}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": nativeID + "-pvc-" + uid}}, "status": deploymentplan.Object{"phase": "Bound"}}
		f.native.objects[deploymentplan.ID(pv)] = pv
	}
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	if err := captureEnvironmentCreateVolumeOwners(context.Background(), f.cfg, f.backend, f.e); err != nil {
		t.Fatal("capture actual original native volume owners", err)
	}
	ledger, err := readEnvironmentCreateLedger(f.e)
	if err != nil {
		t.Fatal(err)
	}
	var active managedTransportVolumeLedgerActive
	if err := createOpenBaoACKCompletionRead(f.store, "deployment/resources/active.json", &active); err != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		t.Fatal("native volume capture failed to produce its exact active pointer", err)
	}
	return ledger
}

func TestHistoricalTransportNativeObservationContract(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	roots, intermediates := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
	keys := map[string]*ecdsa.PrivateKey{}
	for i, domain := range []string{"mqtt", "openbao_tls"} {
		root, rootKey, rootPEM := coreContractCertificate(t, "isolated-"+domain+"-root", true, nil, nil)
		intermediate, intermediateKey, intermediatePEM := coreContractCertificate(t, "isolated-"+domain+"-intermediate", true, root, rootKey)
		rootID := fmt.Sprintf("%08d-1111-4111-8111-111111111111", 80+i)
		issuerID := fmt.Sprintf("%08d-2222-4222-8222-222222222222", 80+i)
		roots[domain] = environmentCreateRegisteredIssuer{ID: rootID, Environment: f.cfg.Environment, Domain: domain, Kind: "root", Status: "active", RootFingerprint: environmentPreflightBytesSHA256(root.Raw), Certificate: rootPEM, ChainPEM: rootPEM}
		intermediates[domain] = environmentCreateRegisteredIssuer{ID: issuerID, Environment: f.cfg.Environment, Domain: domain, Kind: "intermediate", Status: "active", RootFingerprint: roots[domain].RootFingerprint, Certificate: intermediatePEM, ChainPEM: intermediatePEM + rootPEM}
		keys[issuerID] = intermediateKey
		if intermediate.CheckSignatureFrom(root) != nil {
			t.Fatal("native authority chain")
		}
		name := "mqtt-pki." + f.e.Journal.Stack + "-video-cloud.svc"
		if domain == "openbao_tls" {
			name = "openbao." + f.e.Journal.Stack + "-secrets.svc"
		}
		f.p.Plan.Bootstrap.Domains = append(f.p.Plan.Bootstrap.Domains, environmentCreateIssuerPolicy{Domain: domain, ServerDNSNames: []string{name, name + ".cluster.local"}})
	}
	identity := historicalTransportIdentities(t, f, roots, intermediates, keys)
	installHistoricalTransportProtocol(t, f, identity)
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	clients, err := readManagedSessionClients(t.Context(), f.cfg, f.runtime, f.service, subjects)
	if err != nil || clients != deploymentplan.Digest(identity.Clients) {
		t.Fatal("native client rows rejected", err)
	}
	servers, err := readManagedTransportServers(t.Context(), f.cfg, f.runtime, f.p.Plan.Bootstrap, roots, intermediates)
	if err != nil || servers != deploymentplan.Digest(identity.Servers) {
		t.Fatal("native host rows rejected", err)
	}
	t.Run("preserved naturally expired servers", func(t *testing.T) {
		expired := slices.Clone(identity.Servers)
		for i := range expired {
			issuer := intermediates[expired[i].Domain]
			certificate, _, csr := historicalTransportSignedLeaf(t, issuer, keys[issuer.ID], expired[i].DNS[0], expired[i].DNS, -time.Minute)
			leaf, parseErr := parseFirstCertificatePEM([]byte(certificate))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			expired[i].Certificate = certificate
			expired[i].CSR = csr
			expired[i].Fingerprint = environmentPreflightBytesSHA256(leaf.Raw)
		}
		f.protocol["historical_servers"] = expired
		f.publish(t)
		if _, err := readManagedTransportServers(t.Context(), f.cfg, f.runtime, f.p.Plan.Bootstrap, roots, intermediates); err == nil {
			t.Fatal("active admission accepted expired server leaves")
		}
		if got, err := readManagedTransportServersPreserved(t.Context(), f.cfg, f.runtime, f.p.Plan.Bootstrap, roots, intermediates); err != nil || got != deploymentplan.Digest(expired) {
			t.Fatal("preserved continuation rejected unchanged naturally expired server leaves", err)
		}
		f.protocol["historical_servers"] = identity.Servers
		f.publish(t)
	})
	originalJournal := readTestFile(t, f.e.JournalPath)
	ledger := historicalTransportCaptureVolumes(t, f)
	if len(ledger.Volumes) == 0 {
		t.Fatal("real accepted stages produced no durable claims")
	}
	again := historicalTransportCaptureVolumes(t, f)
	if deploymentplan.Digest(ledger.Volumes) != deploymentplan.Digest(again.Volumes) || readTestFile(t, f.e.JournalPath) != originalJournal {
		t.Fatal("native recapture replaced original allocations or execution")
	}
	for _, owner := range ledger.Volumes {
		claim := f.native.objects["PersistentVolumeClaim/"+owner.Namespace+"/"+owner.PVCName]
		volume := f.native.objects["PersistentVolume//"+owner.PVName]
		if err := validateManagedTransportHostVolume(owner, claim, volume); err != nil {
			t.Fatal("recorded volume lost native binding", err)
		}
	}
	for _, mode := range []string{"server-names", "server-csr", "server-revoked", "client-missing", "client-foreign-issuer"} {
		t.Run(mode, func(t *testing.T) {
			badServers := slices.Clone(identity.Servers)
			badClients := slices.Clone(identity.Clients)
			switch mode {
			case "server-names":
				badServers[0].DNS = []string{"foreign.invalid"}
			case "server-csr":
				badServers[0].CSR = badServers[1].CSR
			case "server-revoked":
				now := time.Now().UTC()
				badServers[0].Revoked = &now
			case "client-missing":
				badClients = badClients[:len(badClients)-1]
			case "client-foreign-issuer":
				badClients[0].Issuer = intermediates["mqtt"].ID
			}
			f.protocol["historical_clients"], f.protocol["historical_servers"] = badClients, badServers
			f.publish(t)
			if strings.HasPrefix(mode, "server-") {
				if _, err := readManagedTransportServers(t.Context(), f.cfg, f.runtime, f.p.Plan.Bootstrap, roots, intermediates); err == nil {
					t.Fatal("changed native host identity accepted")
				}
			} else if _, err := readManagedSessionClients(t.Context(), f.cfg, f.runtime, f.service, subjects); err == nil {
				t.Fatal("changed native client identity accepted")
			}
		})
	}
	f.protocol["historical_clients"], f.protocol["historical_servers"] = identity.Clients, identity.Servers
	f.publish(t)
}

// A bootstrap issuance is retained at the signing boundary independently of the
// session table. SQL counts must not turn a stray session row into an issuance.
type historicalBootstrapIssuance struct {
	Session     string `json:"session_id"`
	Issuer      string `json:"issuer_id"`
	Caller      string `json:"caller"`
	Fingerprint string `json:"fingerprint"`
	Certificate string `json:"certificate_pem"`
}

func recordHistoricalBootstrapIssuance(t *testing.T, f *acceptedCoreExecutionContract, state managedSessionState, certificate string) {
	t.Helper()
	leaf, err := parseFirstCertificatePEM([]byte(certificate))
	issuer, issuerErr := parseFirstCertificatePEM([]byte(f.service.Intermediate.Certificate))
	if err != nil || issuerErr != nil || leaf.CheckSignatureFrom(issuer) != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || state.Session == "" || state.Issuer != f.service.Intermediate.ID || state.Root != f.service.Root.RootFingerprint || state.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || !state.Expires.Equal(leaf.NotAfter) {
		t.Fatal("native bootstrap issuance differs from the actual original signature")
	}
	rows, ok := f.protocol["historical_bootstrap_issuances"].(map[string]historicalBootstrapIssuance)
	if f.protocol["historical_bootstrap_issuances"] != nil && !ok {
		t.Fatal("native bootstrap issuance registry is malformed")
	}
	if rows == nil {
		rows = map[string]historicalBootstrapIssuance{}
	}
	row := historicalBootstrapIssuance{Session: state.Session, Issuer: state.Issuer, Caller: leaf.Subject.CommonName, Fingerprint: state.Fingerprint, Certificate: certificate}
	if original, exists := rows[state.Session]; exists && deploymentplan.Digest(original) != deploymentplan.Digest(row) {
		t.Fatal("native issuance would replace its original signed row")
	}
	rows[state.Session] = row
	f.protocol["historical_bootstrap_issuances"] = rows
}

// This independent native OpenBao endpoint persists written policies and roles,
// authenticates every request with the original fixture token, and serves exact
// session projections from signed native certificates. It does not inspect or
// write deployment admission, child acceptance, or recovery completion files.
func installHistoricalTransportSessionProtocol(t *testing.T, f *acceptedCoreExecutionContract, sessions map[string]managedSessionState) {
	t.Helper()
	statePath, reused := f.protocol["historical_session_bao_path"].(string)
	state := map[string]any{"policies": map[string]any{}, "roles": map[string]any{}, "policy_writes": 0, "role_writes": 0}
	if reused {
		info, err := os.Lstat(statePath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || readExecutionJSON(statePath, &state) != nil {
			t.Fatal("original native session endpoint is unavailable")
		}
		if _, ok := state["policies"].(map[string]any); !ok {
			t.Fatal("original native policies are malformed")
		}
		if _, ok := state["roles"].(map[string]any); !ok {
			t.Fatal("original native roles are malformed")
		}
	} else {
		statePath = filepath.Join(t.TempDir(), "native-openbao.json")
	}
	originalState := deploymentplan.Digest(state)
	for _, entry := range f.e.Journal.Entries {
		if reused {
			break
		}
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "openbao-role-") {
			continue
		}
		var intent struct {
			Role      string   `json:"role"`
			Namespace string   `json:"namespace"`
			Accounts  []string `json:"service_accounts"`
			Policies  []string `json:"policies"`
		}
		if json.Unmarshal(entry.Receipt, &intent) != nil || intent.Role == "" {
			t.Fatal("original native role receipt unreadable")
		}
		state["roles"].(map[string]any)[intent.Role] = map[string]any{"bound_service_account_names": intent.Accounts, "bound_service_account_namespaces": []string{intent.Namespace}, "token_policies": intent.Policies, "audience": "openbao", "token_ttl": 900, "token_max_ttl": 900, "token_no_default_policy": true}
	}
	rootToken, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, "openbao/root-token"), true)
	if err != nil || strings.TrimSpace(string(rootToken)) == "" {
		t.Fatal("original isolated OpenBao credential unavailable", err)
	}
	// Preserve the actual original endpoint's current policy and exclusive role
	// before adding renewal write support. Receipts alone do not supply native
	// policy bytes, and an empty replacement map would erase the live policy.
	for session := range sessions {
		original := &createServiceSessionRecovery{Ceremony: f.service, Intent: createServiceSessionIntent{SessionID: session}}
		name, role, expected, err := verifyServiceSessionRecordedAuthenticationNative(t.Context(), f.cfg, f.store, f.e, f.backend, original)
		if err != nil || verifyServiceSessionAuthentication(t.Context(), f.cfg, f.store, f.e, f.backend, original) != nil {
			t.Fatal("original native session authentication before protocol handoff", err)
		}
		token := []byte(strings.TrimSpace(string(rootToken)) + "\n")
		policy, err := environmentCreateOpenBaoExec(t.Context(), f.cfg, f.backend, token, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read "+name+"\n")
		if err != nil || environmentPreflightBytesSHA256(policy) != environmentPreflightBytesSHA256([]byte(expected)) {
			t.Fatal("original native policy bytes differ from completed publication", err)
		}
		raw, err := environmentCreateOpenBaoExec(t.Context(), f.cfg, f.backend, token, "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json auth/kubernetes/role/"+role+"\n")
		var observed struct {
			Data map[string]any `json:"data"`
		}
		if err != nil || decodeEnvironmentJSON(raw, &observed) != nil || observed.Data == nil {
			t.Fatal("original native exclusive role unavailable", err)
		}
		state["policies"].(map[string]any)[name] = string(policy)
		state["roles"].(map[string]any)[role] = observed.Data
	}
	if reused {
		if deploymentplan.Digest(state) != originalState {
			t.Fatal("reinstalled native endpoint differs from its actual current policy or role")
		}
	} else if err := os.WriteFile(statePath, mustProviderRecoveryJSON(state), 0600); err != nil {
		t.Fatal(err)
	}
	f.protocol["historical_session_bao_path"] = statePath
	f.protocol["historical_bao_root_token"] = strings.TrimSpace(string(rootToken))
	// Earlier native issuances remain registry rows after another seed is
	// selected. Only the explicitly selected current sessions above authorize
	// the policy/role handoff; preserved rows never grant stale login authority.
	retained, ok := f.protocol["historical_sessions"].(map[string]managedSessionState)
	if f.protocol["historical_sessions"] != nil && !ok {
		t.Fatal("original native session registry is malformed")
	}
	if retained == nil {
		retained = map[string]managedSessionState{}
	}
	for id, current := range sessions {
		if original, exists := retained[id]; exists && deploymentplan.Digest(original) != deploymentplan.Digest(current) {
			t.Fatal("native session handoff would replace its original issued row", id)
		}
		retained[id] = current
	}
	f.protocol["historical_sessions"] = retained
	f.publish(t)
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env python3
import datetime,json,os,re,shlex,sys
original=%q
state_file=%q
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
a=sys.argv[1:];joined=' '.join(a)
if 'FROM pki_deployment_bootstrap_sessions' in joined:
 if 'registry-read' not in a or a.index('registry-read')!=len(a)-2:sys.exit('exact native registry query required')
 query=a[-1]
 selected=re.search(r"(?:WHERE|AND) session_id='([a-zA-Z0-9-]+)'$",query)
 if not query.startswith('SELECT ') or any(c in query for c in ';\r\n') or selected is None:sys.exit('unsupported native session selection')
 sid=selected.group(1)
 if sid not in d['historical_sessions']:print('null');sys.exit()
 session=dict(d['historical_sessions'][sid])
 early="SELECT json_build_object('count',(SELECT count(*) FROM pki_deployment_bootstrap_sessions),'servers',(SELECT count(*) FROM pki_server_issuances),'clients',(SELECT count(*) FROM pki_service_client_issuances),'session_id',session_id,'issuer_id',issuer_id,'root',root_sha256,'caller',caller,'fingerprint',bootstrap_fingerprint,'subjects',subjects,'installed',installed_subjects,'status',status,'expires',expires_at) FROM pki_deployment_bootstrap_sessions WHERE session_id='"+sid+"'"
 if "'count'" in query and query!=early:sys.exit('unsupported original-session projection')
 now=datetime.datetime.now(datetime.timezone.utc)
 expires=datetime.datetime.fromisoformat(session['expires_at'].replace('Z','+00:00'))
 if session['status']=='active' and expires<=now:session['status']='expired'
 session['other_active']=sum(1 for other_id,other in d['historical_sessions'].items() if other_id!=sid and other['status']=='active' and datetime.datetime.fromisoformat(other['expires_at'].replace('Z','+00:00'))>now)
 if query==early:
  issued=d.get('historical_bootstrap_issuances',{});row=issued.get(sid)
  if row is None or row['session_id']!=sid or row['issuer_id']!=session['issuer_id'] or row['fingerprint']!=session['bootstrap_fingerprint']:sys.exit('selected session has no matching native bootstrap issuance')
  session={'count':len(d['historical_sessions']),'servers':len(d.get('historical_servers') or []),'clients':len(issued)+len(d.get('historical_clients') or []),'session_id':session['session_id'],'issuer_id':session['issuer_id'],'root':session['root_sha256'],'caller':row['caller'],'fingerprint':session['bootstrap_fingerprint'],'subjects':session['subjects'],'installed':session['installed_subjects'],'status':session['status'],'expires':session['expires_at']}
 else:
  end=query.rfind(' FROM pki_deployment_bootstrap_sessions WHERE environment=')
  keys=re.findall(r"'([a-z0-9_]+)',",query[:end] if end>=0 else '')
  aliases={'root':'root_sha256','fingerprint':'bootstrap_fingerprint','installed':'installed_subjects','expires':'expires_at'}
  session['complete']=set(session['subjects']).issubset(set(session['installed_subjects']))
  if not keys or any(aliases.get(k,k) not in session for k in keys):sys.exit('unsupported native session projection')
  session={k:session[aliases.get(k,k)] for k in keys}
 # psql renders json_build_object with spaces around the outer object
 # separators while nested TEXT[] values remain compact JSON. Preserve that
 # exact lexical contract: completion proofs hash the native bytes, not only
 # their decoded values.
 print('{'+', '.join(json.dumps(k)+' : '+json.dumps(v,separators=(',',':')) for k,v in session.items())+'}');sys.exit()
if 'bao policy ' not in joined and 'bao read -format=json auth/kubernetes/role/' not in joined:
 os.execv(original,[original]+a)
with open(state_file) as source:s=json.load(source)
token,_,body=sys.stdin.read().partition('\n')
if token!=d['historical_bao_root_token']:sys.exit('wrong original fixture OpenBao credential')
if 'bao policy ' in joined:
 write=re.search(r'bao policy write ([a-zA-Z0-9_.-]+) -',joined)
 read=re.search(r'bao policy read ([a-zA-Z0-9_.-]+)',joined)
 if not read:sys.exit('policy has no native readback')
 if write:
  if write.group(1)!=read.group(1) or not body:sys.exit('policy intent differs from readback')
  s['policies'][write.group(1)]=body;s['policy_writes']+=1
 if read.group(1) not in s['policies']:sys.exit('unknown native policy')
 result=s['policies'][read.group(1)]
else:
 write=re.search(r'bao write auth/kubernetes/role/([a-zA-Z0-9_.-]+) ([^\n]+)',joined)
 read=re.search(r'bao read -format=json auth/kubernetes/role/([a-zA-Z0-9_.-]+)',joined)
 if not read:sys.exit('role has no native readback')
 if write:
  fields=dict(v.split('=',1) for v in shlex.split(write.group(2)) if '=' in v)
  required={'bound_service_account_names','bound_service_account_namespaces','audience','token_policies','token_ttl','token_max_ttl','token_no_default_policy'}
  if set(fields)!=required or write.group(1)!=read.group(1) or fields['audience']!='openbao' or fields['token_ttl']!='15m' or fields['token_max_ttl']!='15m' or fields['token_no_default_policy']!='true':sys.exit('invalid scoped native role intent')
  fields={k:(v.split(',') if k in ['bound_service_account_names','bound_service_account_namespaces','token_policies'] else v) for k,v in fields.items()}
  fields.update(token_ttl=900,token_max_ttl=900,token_no_default_policy=True)
  s['roles'][write.group(1)]=fields;s['role_writes']+=1
 if read.group(1) not in s['roles']:sys.exit('unknown native role')
 result=json.dumps({'data':s['roles'][read.group(1)]})
with open(state_file,'w') as target:json.dump(s,target)
sys.stdout.write(result)
`, original, statePath)
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_SESSION_PARENT", original)
	t.Setenv("RTK_HISTORICAL_SESSION_BAO", statePath)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for session := range sessions {
		original := &createServiceSessionRecovery{Ceremony: f.service, Intent: createServiceSessionIntent{SessionID: session}}
		if err := verifyServiceSessionAuthentication(t.Context(), f.cfg, f.store, f.e, f.backend, original); err != nil {
			t.Fatal("original native authentication changed across protocol handoff", err)
		}
	}
}

func historicalTransportSeedLeaf(t *testing.T, f *acceptedCoreExecutionContract, session string, expires ...time.Time) (string, managedSessionState) {
	t.Helper()
	original, err := parseFirstCertificatePEM([]byte(f.certificate))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := parseFirstCertificatePEM([]byte(f.service.Intermediate.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	original.SerialNumber, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	original.NotBefore = time.Now().UTC().Add(-time.Minute)
	original.NotAfter = time.Now().UTC().Add(3 * time.Hour)
	if len(expires) != 0 {
		if len(expires) != 1 || !expires[0].After(time.Now()) {
			t.Fatal("historical native seed requires one live expiry selected before signing")
		}
		original.NotAfter = expires[0]
	}
	der, err := x509.CreateCertificate(rand.Reader, original, authority, original.PublicKey, f.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := managedSessionState{Session: session, Issuer: f.service.Intermediate.ID, Root: f.service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(der), Status: "active", Expires: leaf.NotAfter, Subjects: subjects, Installed: []string{}}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + f.service.Intermediate.ChainPEM, state
}

// This adapter keeps the production stage transaction and final Verify. Native
// issuance is exposed only after its exact renewal Job has actually completed.
func historicalTransportSessionApply(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery, certificate string, state managedSessionState) environmentCreateBootstrapApply {
	t.Helper()
	return func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		// Kubernetes stores Secret bytes in data even when the API request used
		// stringData. Normalize only this external serialization before compilation.
		normalized := make([]deploymentplan.Definition, len(defs))
		for i, d := range defs {
			normalized[i] = d
			normalized[i].Object = deploymentplan.Clone(d.Object)
			if d.Object["kind"] == "Secret" {
				data := deploymentplan.Map(normalized[i].Object["data"])
				if data == nil {
					data = deploymentplan.Object{}
				}
				for key, value := range deploymentplan.Map(normalized[i].Object["stringData"]) {
					data[key] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
				}
				normalized[i].Object["data"] = data
				delete(normalized[i].Object, "stringData")
			}
		}
		var live []deploymentplan.Object
		for _, o := range f.native.objects {
			live = append(live, o)
		}
		fresh, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, normalized)}, normalized, live)
		if err != nil {
			return err
		}
		p, err := readEnvironmentCreateStageInput(f.store, f.e, stage)
		if err != nil {
			return err
		}
		if p == nil {
			p = fresh
		}
		expected := map[string]string{}
		for _, resource := range fresh.Resources {
			expected[resource.ID] = deploymentplan.Digest(deploymentplan.Projection(resource.Desired))
		}

		for _, resource := range p.Resources {
			if expected[resource.ID] != deploymentplan.Digest(deploymentplan.Projection(resource.Desired)) {
				return fmt.Errorf("original session stage body changed: %s", resource.ID)
			}
			delete(expected, resource.ID)
		}
		if len(expected) != 0 {
			return fmt.Errorf("original session stage omitted a resource")
		}
		backend := historicalAcceptedScopedBackend{normalTailE2EBackend: f.native, parent: environmentCreateKubernetesBackend{execution: f.e, cfg: f.cfg, store: f.store}, parentSHA: f.e.Journal.PlanSHA256}
		qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.AdmitResumable(ctx, backend, f.store.Root)
		}
		fence := func(ctx context.Context, p *deploymentplan.Plan) error {
			return backend.CheckParentExecution(ctx, p, f.store.Root)
		}
		accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }
		if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, p, backend, qualify, fence, accept, f.e.Check, io.Discard); err != nil {
			return err
		}
		if stage == "service-session-original-key-renewal-"+r.Review {
			f.protocol["custody"].(map[string]string)[r.RenewedCertPath] = certificate
			f.protocol["historical_sessions"].(map[string]managedSessionState)[state.Session] = state
			// The issuance row is written at the signer boundary from the actual
			// certificate. Keep it independent of the session-table fixture so a
			// later altered session fingerprint remains observable as corruption.
			issuanceState := state
			leaf, err := parseFirstCertificatePEM([]byte(certificate))
			if err != nil {
				t.Fatal("native bootstrap issuance certificate cannot be parsed", err)
			}
			issuanceState.Fingerprint = environmentPreflightBytesSHA256(leaf.Raw)
			recordHistoricalBootstrapIssuance(t, f, issuanceState, certificate)
		}
		f.protocol["objects"] = historicalNativeInventory(f.native.objects)
		f.publish(t)
		return nil
	}
}

func TestHistoricalTransportCompleteSessionProducerPreservesOriginalCustody(t *testing.T) {
	for _, mode := range []string{"complete", "native-session-fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			f, r := newExpiredSessionExecutionContract(t, historicalCompleteDomainPolicy(t))
			issued, ok := f.protocol["historical_sessions"].(map[string]managedSessionState)
			if !ok || issued[f.session].Session != f.session {
				t.Fatal("original issued native session is unavailable")
			}
			originalRows := map[string]string{}
			for id, row := range issued {
				originalRows[id] = deploymentplan.Digest(row)
			}
			p := f.p
			p.Materials = map[string]string{}
			if _, err := prepareExpiredServiceSessionRecovery(t.Context(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
				t.Fatal(err)
			}
			r.Review = deploymentplan.Digest(p)
			originalKey, originalCSR, originalCertificate := f.key, f.csr, f.certificate
			certificate, state := historicalTransportSeedLeaf(t, f, r.RenewedIntent.SessionID)
			if mode == "native-session-fingerprint" {
				state.Fingerprint = deploymentplan.Digest("foreign-native-leaf")
			}
			installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{})
			// The complete producer requires the original primary verifier Secret; this
			// narrow expired-session fixture otherwise only supplies its two copies.
			id := "Secret/" + f.service.BootstrapCredentialNamespace + "/pki-verifier-database"
			if f.native.objects[id] == nil {
				f.acceptedHistoricalStage(t, "isolated-original-primary-verifier", []deploymentplan.Definition{environmentCreateSecret(f.service.BootstrapCredentialNamespace, "pki-verifier-database", map[string]string{"url": "postgres://isolated/verification"})})
			}
			runtime := f.runtime
			runtime.Backend = f.backend
			runtime.ServiceSessionRecovery = r
			runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
			subjects, err := environmentCreateBootstrapSubjects(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := createEnvironmentServiceSession(t.Context(), f.cfg, p.Plan.Bootstrap, f.store, runtime, f.service, subjects, historicalTransportSessionApply(t, f, r, certificate, state))
			observedPath := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "observed.json")
			if mode != "complete" {
				if err == nil || !strings.Contains(err.Error(), "actual native session differs") {
					t.Fatal("changed native seed became accepted", err)
				}
				if _, err := os.Stat(observedPath); !os.IsNotExist(err) {
					t.Fatal("failed native seed wrote completion evidence")
				}
			} else {
				if err != nil {
					t.Fatal("complete actual seed producer rejected", err)
				}
				if actual.BootstrapSessionID != state.Session || actual.CleanupBinding.SessionID != state.Session || len(actual.CredentialUIDs) != 3 {
					t.Fatal("complete seed lost actual owner bindings")
				}
				var observed createServiceSessionObserved
				if err := readExecutionJSON(observedPath, &observed); err != nil || len(observed.Session) == 0 || observed.Cleanup != actual.CleanupBinding || len(observed.UIDs) != 3 {
					t.Fatal("actual producer omitted original native observation", err)
				}
				var native map[string]any
				if err := readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &native); err != nil || native["policy_writes"] != float64(2) || native["role_writes"] != float64(2) {
					t.Fatal("complete seed did not install both exact policy/role bindings", err)
				}
				for _, stage := range []string{"service-session-original-key-renewal-", "service-custody-reader-renewal-", "service-credential-secrets-renewal-"} {
					if _, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage+r.Review); err != nil || !j.Accepted {
						t.Fatal("actual stage not accepted", stage, err)
					}
				}
				assertCompletedReplay := func(before map[string]string, entries, writes string) {
					t.Helper()
					after := createBootstrapStateSnapshot(t, f.store.Root)
					journalPath, err := filepath.Rel(f.store.Root, f.e.JournalPath)
					if err != nil {
						t.Fatal(err)
					}
					// The maintained child executor records a fresh lease and
					// revalidation report. Original receipts/material stay exact.
					for path, value := range before {
						if path != journalPath && after[path] != value {
							t.Fatal("completed replay changed original private evidence", path)
						}
					}
					for path := range after {
						if _, existed := before[path]; !existed && path != "deployment/recovery" && !strings.HasPrefix(path, "deployment/recovery/revalidated-") {
							t.Fatal("completed replay created unexpected evidence", path)
						}
					}
					if deploymentplan.Digest(f.e.Journal.Entries) != entries || deploymentplan.Digest(f.native.writes) != writes {
						t.Fatal("completed replay changed original receipts or native resources")
					}
					var bao map[string]any
					if err := readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &bao); err != nil || bao["policy_writes"] != float64(2) || bao["role_writes"] != float64(2) {
						t.Fatal("completed replay rewrote native policy or role", err)
					}
				}
				beforeRetry := createBootstrapStateSnapshot(t, f.store.Root)
				entries, writes := deploymentplan.Digest(f.e.Journal.Entries), deploymentplan.Digest(f.native.writes)
				again, err := createEnvironmentServiceSession(t.Context(), f.cfg, p.Plan.Bootstrap, f.store, runtime, f.service, subjects, historicalTransportSessionApply(t, f, r, certificate, state))
				if err != nil || deploymentplan.Digest(again) != deploymentplan.Digest(actual) {
					t.Fatal("completed producer retry did not preserve exact native evidence", err)
				}
				assertCompletedReplay(beforeRetry, entries, writes)
				originalObserved, err := os.ReadFile(observedPath)
				if err != nil {
					t.Fatal(err)
				}
				for _, mutation := range []string{"cleanup-owner", "native-session", "malformed", "public-mode"} {
					t.Run(mutation, func(t *testing.T) {
						raw := slices.Clone(originalObserved)
						if mutation == "malformed" {
							raw = []byte("{broken")
						} else if mutation != "public-mode" {
							var changed deploymentplan.Object
							if err := json.Unmarshal(raw, &changed); err != nil {
								t.Fatal(err)
							}
							if mutation == "cleanup-owner" {
								deploymentplan.Map(changed["cleanup_binding"])["secret_uid"] = "99999999-9999-4999-8999-999999999999"
							} else {
								deploymentplan.Map(changed["session"])["bootstrap_fingerprint"] = deploymentplan.Digest("foreign-observation")
							}
							raw = mustProviderRecoveryJSON(changed)
						}
						if err := os.WriteFile(observedPath, raw, 0600); err != nil {
							t.Fatal(err)
						}
						if mutation == "public-mode" {
							if err := os.Chmod(observedPath, 0644); err != nil {
								t.Fatal(err)
							}
						}
						t.Cleanup(func() {
							if err := os.Chmod(observedPath, 0600); err != nil {
								t.Error(err)
							}
							if err := os.WriteFile(observedPath, originalObserved, 0600); err != nil {
								t.Error(err)
							}
						})
						before := createBootstrapStateSnapshot(t, f.store.Root)
						entries, writes := deploymentplan.Digest(f.e.Journal.Entries), deploymentplan.Digest(f.native.writes)
						if _, err := createEnvironmentServiceSession(t.Context(), f.cfg, p.Plan.Bootstrap, f.store, runtime, f.service, subjects, historicalTransportSessionApply(t, f, r, certificate, state)); err == nil {
							t.Fatal("completed producer accepted changed original observation")
						}
						assertCompletedReplay(before, entries, writes)
					})
				}
			}
			custody := f.protocol["custody"].(map[string]string)
			if custody["/run/pki-bootstrap/bootstrap.key"] != originalKey || custody["/run/pki-bootstrap/bootstrap.csr"] != originalCSR || custody["/run/pki-bootstrap/bootstrap.crt"] != originalCertificate {
				t.Fatal("same-key producer changed original custody")
			}
			issued = f.protocol["historical_sessions"].(map[string]managedSessionState)
			for id, original := range originalRows {
				if deploymentplan.Digest(issued[id]) != original {
					t.Fatal("renewal or completed replay changed an originally issued native row", id)
				}
			}
			if len(issued) != len(originalRows)+1 || deploymentplan.Digest(issued[state.Session]) != deploymentplan.Digest(state) {
				t.Fatal("renewal did not retain exactly its actual newly issued native row")
			}
		})
	}
}

// Exercise the native transport coordinator with real accepted child stages.
// This selected-boundary fixture does not claim to satisfy the separate full
// historical-source loader; the same apply adapter is reused by that chain.
func newHistoricalTransportCoordinatorExecutionContract(t *testing.T, hooks ...coreExecutionContractHooks) (*acceptedCoreExecutionContract, map[string]environmentCreateRegisteredIssuer, map[string]environmentCreateRegisteredIssuer, *deploymentplan.Plan) {
	t.Helper()
	selected := historicalOriginalFoundationHooks(t)
	if len(hooks) > 1 {
		t.Fatal("one original native cohort hook required")
	}
	if len(hooks) == 1 {
		selected = hooks[0]
	}
	f := newCoreExecutionContractAtBoundaryWithHooks(t, false, true, false, selected, historicalCompleteDomainPolicy(t))
	f.runtime.Backend = f.backend
	f.runtime.OpenBao.CACertPath = filepath.Join(f.store.Root, "deployment/create/material", f.e.Journal.PlanSHA256, "openbao-transport/ca.crt")
	f.runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
	var err error
	f.runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(f.cfg, f.runtime.InitialListeners)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range f.operator {
		// The shared original-input producer publishes these before freezing
		// the outer artifact; continuation must retain the exact originals.
		actual, err := os.ReadFile(filepath.Join(f.store.Root, "operator/env", name))
		if err != nil || string(actual) != value {
			t.Fatal("original operator input differs", name, err)
		}
	}
	for _, name := range []string{"emqx-dashboard-password", "mqtt-broker-auth", "emqx-handoff-api-key", "emqx-handoff-api-secret"} {
		if err := f.store.write("runtime/"+name, []byte("isolated-original-"+name), false); err != nil {
			t.Fatal(err)
		}
	}
	roots := map[string]environmentCreateRegisteredIssuer{"service": f.service.Root}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": f.service.Intermediate}
	keys := map[string]*ecdsa.PrivateKey{}
	for i, domain := range []string{"mqtt", "openbao_tls", "app"} {
		ready := f.trustAuthority(t, domain, 41+i, false, keys)
		roots[domain], intermediates[domain] = f.activateHistoricalTrustAuthority(t, ready, 41+i, keys)
	}
	ready := f.trustAuthority(t, "device", 44, false, keys)
	roots["device"] = f.activateHistoricalReadyRoot(t, ready, 44)
	installHistoricalTransportSessionProtocol(t, f, map[string]managedSessionState{})
	// Preserve the earlier App signer binding when the coordinator adds
	// the transport signers to the exact same native certissuer role.
	if err := configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, f.runtime, "certissuer-"+f.cfg.Environment, []string{"certissuer"}, []string{"create-" + intermediates["app"].ID + "-signer"}); err != nil {
		t.Fatal(err)
	}
	defs, err := renderEnvironmentCreateTransportClientDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, roots, intermediates)
	if err != nil {
		t.Fatal(err)
	}
	clients := f.acceptedHistoricalStage(t, "transport-client-enrollment", defs)
	return f, roots, intermediates, clients
}

func TestHistoricalTransportCoordinatorPreservesEnrolledClients(t *testing.T) {
	for _, mode := range []string{"complete", "client-owner-drift", "interrupt-before-server", "native-role-drift"} {
		t.Run(mode, func(t *testing.T) {
			f, roots, intermediates, clients := newHistoricalTransportCoordinatorExecutionContract(t)
			_, clientJournal, err := acceptedCreateStage(f.store, f.e, "kubernetes/transport-client-enrollment")
			if err != nil {
				t.Fatal(err)
			}
			originalWrites := map[string]int{}
			for _, resource := range clients.Resources {
				originalWrites[resource.ID] = f.native.writes[resource.ID]
			}
			if mode == "client-owner-drift" {
				deploymentplan.Map(f.native.objects[clients.Resources[0].ID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				f.protocol["objects"] = historicalNativeInventory(f.native.objects)
				f.publish(t)
			}
			if mode == "native-role-drift" {
				policies := []string{"create-" + f.service.Intermediate.ID + "-signer", "create-" + f.service.Intermediate.ID + "-service-client", "create-" + intermediates["mqtt"].ID + "-signer", "create-" + intermediates["openbao_tls"].ID + "-signer", "create-" + intermediates["app"].ID + "-signer"}
				slices.Sort(policies)
				if err := configureEnvironmentCreateOpenBaoRole(t.Context(), f.cfg, f.runtime, "certissuer-"+f.cfg.Environment, []string{"certissuer"}, policies); err != nil {
					t.Fatal(err)
				}
				var native map[string]any
				if err := readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &native); err != nil {
					t.Fatal(err)
				}
				roles := native["roles"].(map[string]any)
				roles["certissuer-"+f.cfg.Environment].(map[string]any)["audience"] = "foreign"
				if err := os.WriteFile(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), mustProviderRecoveryJSON(native), 0600); err != nil {
					t.Fatal(err)
				}
			}
			apply := historicalTransportSessionApply(t, f, &createServiceSessionRecovery{}, "", managedSessionState{})
			// The selected executor receives a complete immutable release. The
			// narrower Core source fixture only needs its Service images.
			release := f.p.Plan.Release
			release.Images = map[string]environmentReleaseImage{}
			for role, image := range f.p.Plan.Release.Images {
				release.Images[role] = image
			}
			for _, role := range []string{"LKE_OPENBAO_IMAGE", "LKE_OPENBAO_PKI_IMAGE", "LKE_MQTT_PKI_IMAGE", "LKE_VIDEO_CLOUD_IMAGE"} {
				release.Images[role] = environmentReleaseImage{Reference: f.p.Plan.Bootstrap.Image}
			}
			var called []string
			err = executeEnvironmentCreateManagedTransports(t.Context(), f.cfg, f.p.Plan.Bootstrap, release, f.runtime, f.service, roots, intermediates, func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
				called = append(called, stage)
				if mode == "interrupt-before-server" && stage == "transport-server-enrollment" {
					return context.Canceled
				}
				return apply(ctx, stage, defs)
			})
			if mode == "complete" {
				if err != nil {
					t.Fatal("actual transport coordinator", err)
				}
				if !slices.Equal(called, []string{"transport-enrollment-issuer", "transport-server-enrollment", "transport-managed-hosts"}) {
					t.Fatal("transport order changed", called)
				}
				for _, stage := range called {
					if _, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage); err != nil || !j.Accepted {
						t.Fatal("transport stage not genuinely accepted", stage, err)
					}
				}
			} else if err == nil || slices.Contains(called, "transport-managed-hosts") {
				t.Fatal("transport cutover continued after native drift or interruption", err, called)
			}
			for _, resource := range clients.Resources {
				if f.native.writes[resource.ID] != originalWrites[resource.ID] {
					t.Fatal("transport cutover replayed an enrolled client", resource.ID)
				}
				if mode != "client-owner-drift" && deploymentplan.Map(f.native.objects[resource.ID]["metadata"])["uid"] != clientJournal.Owners[resource.ID] {
					t.Fatal("transport cutover replaced original client owner", resource.ID)
				}
			}
		})
	}
}
