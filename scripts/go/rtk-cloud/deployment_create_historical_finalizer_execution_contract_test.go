package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the external Account Manager/controller/custody interfaces are modeled.
// Admission, finalization, historical retirement and durable completion remain
// production code. This adapter never issues a replacement CA or workload leaf.
type historicalNativeFinalizer struct {
	mu       sync.Mutex
	fixture  *acceptedCoreExecutionContract
	recovery *createServiceSessionRecovery
	service  environmentCreateServiceCeremony
	owners   map[string]string
	copies   map[string]deploymentplan.Object
	controls []deploymentplan.Definition
	token    string
	final    environmentCreateBootstrapFinalization
	crl      managedLateCRLRecord
	marker   []byte
	calls    int
	logins   int
	rejected string
	cacheDir string
	acks     map[string]string
}

func historicalFinalizerSubjectOwners(stack string) map[string]string {
	ns := stack + "-video-cloud/"
	return map[string]string{
		"service:account-manager":         "Deployment/" + stack + "-account-manager/account-manager",
		"service:certissuer":              "Deployment/" + ns + "certissuer",
		"service:emqx-pki":                "Deployment/" + ns + "mqtt-pki",
		"service:factory-enroll":          "Deployment/" + ns + "factoryenroll",
		"service:openbao":                 "StatefulSet/" + stack + "-secrets/openbao",
		"service:pki-controller":          "Deployment/" + ns + "pki-controller",
		"service:pkibroker":               "Deployment/" + ns + "mqtt-pki",
		"service:video-cloud-api":         "Deployment/" + ns + "video-cloud-api",
		"service:video-cloud-logingester": "Deployment/" + ns + "video-cloud-logingester",
	}
}

func installHistoricalNativeFinalizer(t *testing.T, f *acceptedCoreExecutionContract, recovery *createServiceSessionRecovery) *historicalNativeFinalizer {
	t.Helper()
	if f.runtime.ServiceCeremony == nil || f.runtime.PKIClient != nil {
		t.Fatal("one original current native ceremony and no previous finalizer required")
	}
	s := *f.runtime.ServiceCeremony
	if validateEnvironmentBootstrapCleanupBinding(&s.CleanupBinding, f.cfg.Environment) != nil {
		t.Fatal("native finalizer needs exact current cleanup binding")
	}
	controllerID := "Deployment/" + s.BootstrapCredentialNamespace + "/pki-controller"
	controller := deploymentplan.Clone(f.native.objects[controllerID])
	controls, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(f.cfg), controller, s.CleanupBinding)
	if err != nil {
		t.Fatal("native finalizer original controller cleanup", err)
	}
	a := &historicalNativeFinalizer{fixture: f, recovery: recovery, service: s, owners: map[string]string{}, copies: map[string]deploymentplan.Object{}, controls: controls, token: deploymentplan.Digest([]any{s.BootstrapSessionID, time.Now().UTC()}), cacheDir: t.TempDir(), acks: map[string]string{}}
	for _, id := range historicalFinalizerSubjectOwners(f.p.Plan.Stack) {
		object := f.native.objects[id]
		uid := deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
		if object == nil || !environmentBootstrapOwnerUUID.MatchString(uid) {
			t.Fatal("native finalizer original workload owner missing", id)
		}
		a.owners[id] = uid
	}
	a.owners["Deployment/"+s.BootstrapCredentialNamespace+"/"+s.CustodyReader] = deploymentplan.String(deploymentplan.Map(f.native.objects["Deployment/"+s.BootstrapCredentialNamespace+"/"+s.CustodyReader]["metadata"])["uid"])
	for _, def := range controls {
		id := deploymentplan.ID(def.Object)
		object := f.native.objects[id]
		if object == nil || !deploymentplan.Matches(def.Object, object) {
			t.Fatal("native finalizer cleanup resource is not exact", id)
		}
		a.owners[id] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	}
	for id, object := range f.native.objects {
		meta := deploymentplan.Map(object["metadata"])
		if object["kind"] == "Secret" && strings.HasPrefix(deploymentplan.String(meta["name"]), "service-bootstrap-") {
			a.copies[id] = deploymentplan.Clone(object)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(server.Close)
	client, err := newEnvironmentCreatePKIClient(server.URL, server.Client(), f.cfg.Environment, f.operator["PKI_OPERATOR_USER_ID"])
	if err != nil {
		t.Fatal(err)
	}
	client.Execution = f.e
	if _, err := client.Authenticate(t.Context(), f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		t.Fatal("native finalizer original operator authentication", err)
	}
	client.operatorCredentials = f.operator
	f.runtime.PKIClient = client
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/usr/bin/env python3
import base64,json,os,subprocess,sys,urllib.request
a=sys.argv[1:];joined=' '.join(a)
selected=('registry-read' in a and 'pki_deployment_bootstrap_sessions' in joined) or ('delete' in a and any(x.startswith('--raw=/api/v1/namespaces/') and '/secrets/' in x for x in a)) or ('exec' in a and any(x in joined for x in ['cleanup-credentials','bootstrap.finalized.json','expired-','test ! -e /run/pki-bootstrap/bootstrap.key']))
if not selected:os.execv(%q,[%q]+a)
raw=sys.stdin.buffer.read()
request=urllib.request.Request(%q+'/boundary',data=json.dumps({'args':a,'input':base64.b64encode(raw).decode()}).encode(),headers={'Content-Type':'application/json'})
with urllib.request.urlopen(request,timeout=30) as response:result=json.load(response)
if not result['handled']:
 sys.exit(subprocess.run([%q]+a,input=raw).returncode)
sys.stdout.buffer.write(base64.b64decode(result.get('output','')))
sys.exit(result['code'])
`, original, original, server.URL, original)
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return a
}

func (a *historicalNativeFinalizer) publish() error {
	// Retain extra native Pod/log overlays supplied by the Log process adapter.
	f := a.fixture
	objects, ok := f.protocol["objects"].(map[string]deploymentplan.Object)
	if !ok {
		return errors.New("native inventory unavailable")
	}
	for id := range a.copies {
		if f.native.objects[id] == nil {
			delete(objects, id)
		}
	}
	return os.WriteFile(f.protocolPath, mustProviderRecoveryJSON(f.protocol), 0600)
}

func (a *historicalNativeFinalizer) current() (managedSessionState, error) {
	f, s := a.fixture, a.service
	states, ok := f.protocol["historical_sessions"].(map[string]managedSessionState)
	state, found := states[s.BootstrapSessionID]
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	installed := slices.Clone(state.Installed)
	slices.Sort(installed)
	if !ok || !found || err != nil || state.Session != s.BootstrapSessionID || state.Issuer != s.Intermediate.ID || state.Root != s.Root.RootFingerprint || state.OtherActive != 0 || !slices.Equal(state.Subjects, subjects) || !slices.Equal(installed, subjects) || len(installed) != 9 || state.Status != "active" && state.Status != "sealed" || !state.Expires.After(time.Now()) {
		return state, errors.New("original live session lacks exact nine native installations")
	}
	for id, other := range states {
		if id != state.Session && other.Status == "active" && other.Expires.After(time.Now()) {
			return state, errors.New("another original native bootstrap session remains active")
		}
	}
	clients, ok := f.protocol["historical_clients"].([]managedSessionClient)
	if !ok || !validateManagedSessionClients(clients, s, subjects, time.Now()) {
		return state, errors.New("native workload installation lacks original signed client identity")
	}
	for id, uid := range a.owners {
		object := f.native.objects[id]
		meta := deploymentplan.Map(object["metadata"])
		if object == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || meta["uid"] != uid || meta["deletionTimestamp"] != nil {
			return state, errors.New("original native finalizer owner changed")
		}
	}
	for _, id := range historicalFinalizerSubjectOwners(f.p.Plan.Stack) {
		found := false
		for _, group := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(environmentPKIPod(f.native.objects[id])[group]) {
				for _, value := range certIssuerObjectList(deploymentplan.Map(raw)["env"]) {
					setting := deploymentplan.Map(value)
					if strings.Contains(deploymentplan.String(setting["name"]), "BOOTSTRAP_SESSION_ID") && setting["value"] == state.Session {
						found = true
					}
				}
			}
		}
		if !found {
			return state, errors.New("native consumer selected another bootstrap session")
		}
	}
	for _, def := range a.controls {
		if !deploymentplan.Matches(def.Object, f.native.objects[deploymentplan.ID(def.Object)]) {
			return state, errors.New("native finalizer cleanup authority changed")
		}
	}
	namespace := f.native.objects["Namespace//"+s.BootstrapCredentialNamespace]
	nsMeta := deploymentplan.Map(namespace["metadata"])
	if namespace == nil || nsMeta["uid"] != s.CleanupBinding.NamespaceUID || nsMeta["name"] != s.BootstrapCredentialNamespace || deploymentplan.Map(nsMeta["labels"])["rtk.realtek.com/stack"] != f.p.Plan.Stack {
		return state, errors.New("native cleanup namespace owner changed")
	}
	return state, nil
}

func (a *historicalNativeFinalizer) serve(w http.ResponseWriter, request *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, s := a.fixture, a.service
	if request.Method == http.MethodPost && request.URL.Path == "/boundary" {
		var body struct {
			Args  []string `json:"args"`
			Input []byte   `json:"input"`
		}
		if json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&body) != nil {
			http.Error(w, "malformed native request", 400)
			return
		}
		out, handled, err := a.command(body.Args, body.Input)
		code := 0
		if err != nil {
			code = 1
			out = []byte(err.Error())
		}
		if out == nil {
			out = []byte{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"handled": handled, "code": code, "output": out})
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/auth/login" {
		var login map[string]string
		if json.NewDecoder(request.Body).Decode(&login) != nil || login["email"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"] || login["password"] != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] || len(login) != 2 {
			http.Error(w, "original operator required", 403)
			return
		}
		a.logins++
		_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": f.operator["PKI_OPERATOR_USER_ID"], "email": login["email"]}, "tokens": map[string]any{"access_token": a.token}})
		return
	}
	if request.Header.Get("Authorization") != "Bearer "+a.token {
		http.Error(w, "original authenticated operator required", 403)
		return
	}
	if request.Method == http.MethodPost && request.URL.Path == "/v1/platform/pki/deployment-bootstrap/"+s.BootstrapSessionID+"/finalize" {
		a.calls++
		if raw, err := io.ReadAll(io.LimitReader(request.Body, 2)); err != nil || len(raw) != 0 {
			http.Error(w, "native finalizer has no replacement input", 400)
			return
		}
		if err := a.finalize(); err != nil {
			a.rejected = err.Error()
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(a.final)
		return
	}
	if request.Method == http.MethodGet && request.URL.Path == "/v1/platform/pki/issuers/"+s.Intermediate.ID+"/crl" && a.crl.IssuerID != "" {
		_ = json.NewEncoder(w).Encode(a.crl)
		return
	}
	http.Error(w, "unknown original native finalizer operation", 404)
}

func (a *historicalNativeFinalizer) finalize() error {
	state, err := a.current()
	if err != nil {
		return err
	}
	if !a.final.FinalizedAt.IsZero() {
		if state.Status != "sealed" || state.Fingerprint != a.final.LeafSHA256 {
			return errors.New("accepted native finalization changed")
		}
		return nil
	}
	f, s := a.fixture, a.service
	custody := f.protocol["custody"].(map[string]string)
	path := "/run/pki-bootstrap/bootstrap.crt"
	if a.recovery != nil && a.recovery.Renewal {
		path = a.recovery.RenewedCertPath
	}
	pair, err := tls.X509KeyPair([]byte(custody[path]), []byte(custody["/run/pki-bootstrap/bootstrap.key"]))
	issuer, issuerErr := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	if err != nil || issuerErr != nil || len(pair.Certificate) != 3 || f.issuerKey == nil {
		return errors.New("native finalizer original signing custody unavailable")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.CheckSignatureFrom(issuer) != nil || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || !leaf.NotAfter.Equal(state.Expires) || environmentPreflightBytesSHA256(pair.Certificate[2]) != state.Root {
		return errors.New("native finalizer selected a different original signed leaf")
	}
	records, ok := f.protocol["historical_crls"].(map[string]managedLateCRLRecord)
	prior := records[s.Intermediate.ID]
	block, _ := pem.Decode([]byte(prior.PEM))
	if !ok || block == nil || prior.Digest != environmentPreflightBytesSHA256(block.Bytes) {
		return errors.New("original native revocation publication missing")
	}
	previous, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || previous.CheckSignatureFrom(issuer) != nil {
		return errors.New("original native revocation signer differs")
	}
	for _, entry := range previous.RevokedCertificateEntries {
		if entry.SerialNumber.Cmp(leaf.SerialNumber) == 0 {
			return errors.New("original temporary leaf already revoked without finalization")
		}
	}
	bao, policyName, err := a.originalPolicy()
	if err != nil {
		return err
	}
	primary := "Secret/" + s.BootstrapCredentialNamespace + "/" + s.BootstrapCredentialSecret
	secret := f.native.objects[primary]
	if secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != s.CleanupBinding.SecretUID || !deploymentplan.Matches(a.copies[primary], secret) {
		return errors.New("native finalizer original primary Secret changed")
	}
	meta, data := deploymentplan.Map(secret["metadata"]), deploymentplan.Map(secret["data"])
	labels := deploymentplan.Map(meta["labels"])
	certificate, certErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.crt"]))
	key, keyErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.key"]))
	primaryPair, primaryErr := tls.X509KeyPair(certificate, key)
	if len(data) != 3 || data["ca.crt"] == nil || meta["namespace"] != s.BootstrapCredentialNamespace || meta["name"] != s.BootstrapCredentialSecret || deploymentplan.String(meta["resourceVersion"]) == "" || labels["rtk.realtek.com/stack"] != f.p.Plan.Stack || labels["rtk.cloud/bootstrap-session"] != state.Session || certErr != nil || keyErr != nil || primaryErr != nil || environmentPreflightBytesSHA256(primaryPair.Certificate[0]) != state.Fingerprint {
		return errors.New("native primary Secret lacks the exact temporary credential")
	}
	for _, ns := range s.BootstrapCredentialNamespaces {
		if ns != s.BootstrapCredentialNamespace && f.native.objects["Secret/"+ns+"/"+s.BootstrapCredentialSecret] != nil {
			return errors.New("native finalizer requires exact secondary copy retirement first")
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	next := now.Add(time.Hour)
	if issuer.NotAfter.Before(next) {
		next = issuer.NotAfter
	}
	if !next.After(now) {
		return errors.New("original online signing authority expired")
	}
	entries := append(slices.Clone(previous.RevokedCertificateEntries), x509.RevocationListEntry{SerialNumber: leaf.SerialNumber, RevocationTime: now, ReasonCode: 5})
	number := new(big.Int).Add(previous.Number, big.NewInt(1))
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: number, ThisUpdate: now, NextUpdate: next, RevokedCertificateEntries: entries}, issuer, f.issuerKey)
	if err != nil {
		return err
	}
	signed, err := x509.ParseRevocationList(der)
	if err != nil || signed.CheckSignatureFrom(issuer) != nil {
		return errors.New("native finalizer revocation publication signature differs")
	}
	a.crl = managedLateCRLRecord{IssuerID: s.Intermediate.ID, Digest: environmentPreflightBytesSHA256(der), Number: number.String(), PEM: string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), ThisUpdate: signed.ThisUpdate, NextUpdate: signed.NextUpdate}
	records[s.Intermediate.ID] = a.crl
	state.Status = "sealed"
	f.protocol["historical_sessions"].(map[string]managedSessionState)[state.Session] = state
	// Native workers consume the actual signed publication using the already
	// authenticated current owner roster. Every cache is independently read
	// back before publishing its ACK; no requested digest alone is a receipt.
	consumers, err := managedTransportServiceConsumerIDs(findEnvironmentCreateDomainPolicy(f.p.Plan.Bootstrap, "service").Consumers)
	if err != nil {
		return err
	}
	for _, consumer := range consumers {
		id := historicalFinalizerSubjectOwners(f.p.Plan.Stack)["service:"+consumer]
		if id == "" || a.owners[id] == "" {
			return errors.New("native finalizer CRL consumer lacks original owner")
		}
		path := filepath.Join(a.cacheDir, consumer+".pem")
		if err := os.WriteFile(path, []byte(a.crl.PEM), 0600); err != nil {
			return err
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		cached, _ := pem.Decode(raw)
		if err != nil || cached == nil || environmentPreflightBytesSHA256(cached.Bytes) != a.crl.Digest {
			return errors.New("native finalizer CRL cache readback differs")
		}
		parsed, err := x509.ParseRevocationList(cached.Bytes)
		if err != nil || parsed.CheckSignatureFrom(issuer) != nil {
			return errors.New("native finalizer consumer rejected the publication signature")
		}
		a.acks[consumer] = a.crl.Digest
	}
	// The native controller's cleanup binds exactly one temporary session
	// policy. Preserve every other policy/role, then independently verify the
	// selected authorization's absence before returning completion metadata.
	currentPolicy, _, err := a.originalPolicy()
	if err != nil || deploymentplan.Digest(currentPolicy) != deploymentplan.Digest(bao) {
		return errors.New("native temporary authorization changed before deletion")
	}
	delete(f.native.objects, primary)
	f.native.writes[primary]++
	delete(deploymentplan.Map(bao["policies"]), policyName)
	if err := os.WriteFile(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), mustProviderRecoveryJSON(bao), 0600); err != nil {
		return err
	}
	var absent deploymentplan.Object
	if readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &absent) != nil || deploymentplan.Map(absent["policies"])[policyName] != nil || deploymentplan.Digest(absent) != deploymentplan.Digest(bao) || f.native.objects[primary] != nil {
		return errors.New("native temporary authorization absence is not verified")
	}
	// Preserve the native DeploymentBootstrapAuthorization field order. Its
	// digest is over the typed public authorization, not ConfigMap JSON order.
	binding := struct {
		Version      int    `json:"schema_version"`
		Environment  string `json:"environment"`
		SessionID    string `json:"session_id"`
		NamespaceUID string `json:"namespace_uid"`
		SecretUID    string `json:"secret_uid"`
		PolicySHA256 string `json:"policy_sha256"`
	}{s.CleanupBinding.Version, s.CleanupBinding.Environment, s.CleanupBinding.SessionID, s.CleanupBinding.NamespaceUID, s.CleanupBinding.SecretUID, s.CleanupBinding.PolicySHA256}
	a.final = environmentCreateBootstrapFinalization{Environment: f.cfg.Environment, SessionID: state.Session, LeafSHA256: state.Fingerprint, CRLSHA256: a.crl.Digest, CleanupSHA256: environmentPreflightBytesSHA256(mustProviderRecoveryJSON(binding)), FinalizedAt: time.Now().UTC().Truncate(time.Microsecond)}
	return a.publish()
}

var historicalFinalizerSessionQuery = regexp.MustCompile(`session_id='([a-f0-9-]{36})'`)
var historicalFinalizerProjection = regexp.MustCompile(`'([a-z_]+)',`)

func (a *historicalNativeFinalizer) originalPolicy() (deploymentplan.Object, string, error) {
	name := "rtk-" + a.fixture.cfg.Environment + "-service-bootstrap-" + a.service.BootstrapSessionID
	var state deploymentplan.Object
	raw, err := readBoundedEnvironmentFile(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), true)
	if err != nil || json.Unmarshal(raw, &state) != nil {
		return nil, name, errors.New("original native temporary policy state unavailable")
	}
	policy := deploymentplan.String(deploymentplan.Map(state["policies"])[name])
	if policy == "" || environmentPreflightBytesSHA256([]byte(policy)) != a.service.CleanupBinding.PolicySHA256 {
		return nil, name, errors.New("original native temporary policy changed")
	}
	return state, name, nil
}

func (a *historicalNativeFinalizer) command(args []string, input []byte) ([]byte, bool, error) {
	f, s := a.fixture, a.service
	joined := strings.Join(args, " ")
	if slices.Contains(args, "registry-read") && strings.Contains(joined, "pki_deployment_bootstrap_sessions") {
		matches := historicalFinalizerSessionQuery.FindAllStringSubmatch(joined, -1)
		if len(matches) == 0 {
			return nil, false, nil
		}
		for _, match := range matches {
			if match[1] != matches[0][1] {
				return nil, true, errors.New("native session query selects conflicting sessions")
			}
		}
		state, ok := f.protocol["historical_sessions"].(map[string]managedSessionState)[matches[0][1]]
		if !ok || !strings.Contains(joined, "environment='"+f.cfg.Environment+"'") {
			return nil, true, errors.New("native session query selects unknown environment or session")
		}
		if state.Status == "active" && !state.Expires.After(time.Now()) {
			state.Status = "expired"
		}
		state.OtherActive = 0
		for id, other := range f.protocol["historical_sessions"].(map[string]managedSessionState) {
			if id != state.Session && other.Status == "active" && other.Expires.After(time.Now()) {
				state.OtherActive++
			}
		}
		if strings.Contains(joined, "finalized_at IS NOT NULL") {
			if state.Session != a.final.SessionID || a.final.FinalizedAt.IsZero() {
				return []byte("null"), true, nil
			}
			return mustProviderRecoveryJSON(a.final), true, nil
		}
		installed := slices.Clone(state.Installed)
		slices.Sort(installed)
		data := deploymentplan.Object{"session_id": state.Session, "issuer_id": state.Issuer, "root_sha256": state.Root, "root": state.Root, "bootstrap_fingerprint": state.Fingerprint, "fingerprint": state.Fingerprint, "status": state.Status, "subjects": state.Subjects, "installed_subjects": state.Installed, "installed": state.Installed, "expires_at": state.Expires, "expires": state.Expires, "other_active": state.OtherActive, "complete": slices.Equal(installed, state.Subjects)}
		projection := deploymentplan.Object{}
		for _, match := range historicalFinalizerProjection.FindAllStringSubmatch(joined, -1) {
			value, exists := data[match[1]]
			if !exists {
				return nil, true, errors.New("unsupported native session projection")
			}
			projection[match[1]] = value
		}
		if len(projection) == 0 {
			return nil, true, errors.New("native session projection missing")
		}
		return mustProviderRecoveryJSON(projection), true, nil
	}
	for _, arg := range args {
		const prefix = "--raw=/api/v1/namespaces/"
		if slices.Contains(args, "delete") && strings.HasPrefix(arg, prefix) {
			parts := strings.Split(strings.TrimPrefix(arg, prefix), "/")
			if len(parts) != 3 || parts[1] != "secrets" {
				return nil, false, nil
			}
			id := "Secret/" + parts[0] + "/" + parts[2]
			original, actual := a.copies[id], f.native.objects[id]
			var options struct {
				APIVersion    string `json:"apiVersion"`
				Kind          string `json:"kind"`
				Preconditions struct {
					UID     string `json:"uid"`
					Version string `json:"resourceVersion"`
				} `json:"preconditions"`
			}
			if original == nil || actual == nil || json.Unmarshal(input, &options) != nil || options.APIVersion != "v1" || options.Kind != "DeleteOptions" || options.Preconditions.UID != deploymentplan.Map(original["metadata"])["uid"] || options.Preconditions.UID != deploymentplan.Map(actual["metadata"])["uid"] || options.Preconditions.Version == "" || options.Preconditions.Version != deploymentplan.Map(actual["metadata"])["resourceVersion"] || !deploymentplan.Matches(original, actual) {
				return nil, true, errors.New("native exact-UID credential deletion refused")
			}
			delete(f.native.objects, id)
			f.native.writes[id]++
			return nil, true, a.publish()
		}
	}
	if !slices.Contains(args, "exec") {
		return nil, false, nil
	}
	index := slices.Index(args, "exec")
	if index+1 >= len(args) || args[index+1] != "deployment/"+s.CustodyReader {
		return nil, false, nil
	}
	ns, container := slices.Index(args, "--namespace"), slices.Index(args, "--container")
	id := "Deployment/" + s.BootstrapCredentialNamespace + "/" + s.CustodyReader
	if ns < 0 || ns+1 >= len(args) || args[ns+1] != s.BootstrapCredentialNamespace || container < 0 || container+1 >= len(args) || args[container+1] != "custody" || deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] != a.owners[id] {
		return nil, true, errors.New("native original custody reader changed")
	}
	custody := f.protocol["custody"].(map[string]string)
	if slices.Contains(args, "cat") && strings.HasPrefix(args[len(args)-1], "/run/pki-bootstrap/expired-") {
		value, ok := custody[args[len(args)-1]]
		if !ok {
			return nil, true, errors.New("original archive unavailable")
		}
		return []byte(value), true, nil
	}
	if slices.Contains(args, "cat") && args[len(args)-1] == "/run/pki-bootstrap/bootstrap.finalized.json" {
		if len(a.marker) == 0 {
			return nil, true, errors.New("native finalization tombstone unavailable")
		}
		return slices.Clone(a.marker), true, nil
	}
	if slices.Contains(args, "cleanup-credentials") {
		expected := []string{"env", "PKI_BOOTSTRAP_CERT_PATH=/run/pki-bootstrap/bootstrap.crt", "/app/serviceidentity-bootstrap", "cleanup-credentials"}
		dash := slices.Index(args[index+2:], "--")
		if dash < 0 || !slices.Equal(args[index+2+dash+1:], expected) || a.final.FinalizedAt.IsZero() || len(a.marker) != 0 {
			return nil, true, errors.New("native finalization required before one custody cleanup")
		}
		files := map[string]string{}
		for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
			raw := custody["/run/pki-bootstrap/"+name]
			if raw == "" {
				return nil, true, errors.New("original temporary custody missing")
			}
			files[name] = environmentPreflightBytesSHA256([]byte(raw))
		}
		pair, err := tls.X509KeyPair([]byte(custody["/run/pki-bootstrap/bootstrap.crt"]), []byte(custody["/run/pki-bootstrap/bootstrap.key"]))
		if err != nil || environmentPreflightBytesSHA256(pair.Certificate[0]) != a.final.LeafSHA256 {
			return nil, true, errors.New("native custody still contains a different temporary leaf")
		}
		leaf, leafErr := x509.ParseCertificate(pair.Certificate[0])
		block, _ := pem.Decode([]byte(custody["/run/pki-bootstrap/bootstrap.csr"]))
		if leafErr != nil || block == nil || block.Type != "CERTIFICATE REQUEST" {
			return nil, true, errors.New("native original CSR unavailable before cleanup")
		}
		csr, csrErr := x509.ParseCertificateRequest(block.Bytes)
		if csrErr != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != "service:deployment-bootstrap" || !bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
			return nil, true, errors.New("native original CSR does not own the finalized leaf")
		}
		a.marker = mustProviderRecoveryJSON(managedLateCustodyTombstone{Schema: "rtk-cloud.bootstrap-credentials/v1", Finalization: a.final, Root: s.Root.RootFingerprint, Files: files})
		for name := range files {
			delete(custody, "/run/pki-bootstrap/"+name)
		}
		return nil, true, a.publish()
	}
	if args[len(args)-1] == `test ! -e /run/pki-bootstrap/bootstrap.key && test ! -e /run/pki-bootstrap/bootstrap.csr && test ! -e /run/pki-bootstrap/bootstrap.crt` {
		if len(a.marker) == 0 || custody["/run/pki-bootstrap/bootstrap.key"] != "" || custody["/run/pki-bootstrap/bootstrap.csr"] != "" || custody["/run/pki-bootstrap/bootstrap.crt"] != "" {
			return nil, true, errors.New("original temporary custody remains present")
		}
		return nil, true, nil
	}
	if strings.Contains(args[len(args)-1], "&& ln /run/pki-bootstrap/bootstrap.crt ") {
		return nil, true, a.archive(args[len(args)-1])
	}
	return nil, false, nil
}

func (a *historicalNativeFinalizer) archive(script string) error {
	r, f := a.recovery, a.fixture
	if r == nil || !r.Renewal || a.final.SessionID != r.RenewedIntent.SessionID {
		return errors.New("native archive requires actual renewed finalization")
	}
	custody := f.protocol["custody"].(map[string]string)
	old, renewed := custody["/run/pki-bootstrap/bootstrap.crt"], custody[r.RenewedCertPath]
	expiredSHA, renewedSHA := environmentPreflightBytesSHA256([]byte(old)), environmentPreflightBytesSHA256([]byte(renewed))
	base := "/run/pki-bootstrap/expired-" + r.Intent.SessionID
	expected := "test ! -L /run/pki-bootstrap && test ! -L /run/pki-bootstrap/bootstrap.crt && test ! -L " + r.RenewedCertPath + " && test ! -e " + base + " && printf '%s  %s\\n' '" + expiredSHA + "' '/run/pki-bootstrap/bootstrap.crt' '" + renewedSHA + "' '" + r.RenewedCertPath + "' | sha256sum -c - >/dev/null && mkdir " + base + " && chmod 0700 " + base + " && ln /run/pki-bootstrap/bootstrap.crt " + base + "/bootstrap.crt && rm /run/pki-bootstrap/bootstrap.crt && ln " + r.RenewedCertPath + " /run/pki-bootstrap/bootstrap.crt"
	oldLeaf, oldErr := parseFirstCertificatePEM([]byte(old))
	newLeaf, newErr := parseFirstCertificatePEM([]byte(renewed))
	if script != expected || oldErr != nil || newErr != nil || oldLeaf.NotAfter.After(time.Now()) || !bytes.Equal(oldLeaf.RawSubjectPublicKeyInfo, newLeaf.RawSubjectPublicKeyInfo) || environmentPreflightBytesSHA256(newLeaf.Raw) != a.final.LeafSHA256 || custody[base+"/bootstrap.crt"] != "" || environmentPreflightBytesSHA256([]byte(custody["/run/pki-bootstrap/bootstrap.key"])) != r.OriginalKeySHA || environmentPreflightBytesSHA256([]byte(custody["/run/pki-bootstrap/bootstrap.csr"])) != r.OriginalCSRSHA {
		return errors.New("native exact original archive request differs")
	}
	custody[base+"/bootstrap.crt"], custody["/run/pki-bootstrap/bootstrap.crt"] = old, renewed
	return a.publish()
}

// This is a native-interface qualification fixture: it starts with the real
// signed Core identity and externally models the nine already-installed owners.
// It makes no assertion about a complete late-source or public GO admission.
func newHistoricalFinalizerNativeContract(t *testing.T) (*acceptedCoreExecutionContract, *historicalNativeFinalizer) {
	t.Helper()
	f := newAcceptedCoreExecutionContract(t)
	s := f.service
	policy := f.protocol["policy"].(environmentCreateOpenBaoIssuerPolicies).ServiceClientSignerPolicy
	s.CleanupBinding.PolicySHA256 = environmentPreflightBytesSHA256([]byte(policy))
	baoPath := filepath.Join(t.TempDir(), "native-policies.json")
	if err := os.WriteFile(baoPath, mustProviderRecoveryJSON(deploymentplan.Object{"policies": deploymentplan.Object{"rtk-" + f.cfg.Environment + "-service-bootstrap-" + s.BootstrapSessionID: policy, "original-permanent-issuer": "untouched issuer policy"}, "roles": deploymentplan.Object{"original-role": []string{"original-permanent-issuer"}}}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_SESSION_BAO", baoPath)
	s.CredentialUIDs = map[string]string{}
	for _, ns := range s.BootstrapCredentialNamespaces {
		s.CredentialUIDs[ns] = deploymentplan.String(deploymentplan.Map(f.native.objects["Secret/"+ns+"/"+s.BootstrapCredentialSecret]["metadata"])["uid"])
	}
	for _, id := range historicalFinalizerSubjectOwners(f.p.Plan.Stack) {
		parts := strings.Split(id, "/")
		object := deploymentplan.Clone(f.native.objects[id])
		if object == nil {
			object = deploymentplan.Object{"apiVersion": "apps/v1", "kind": parts[0], "metadata": deploymentplan.Object{"namespace": parts[1], "name": parts[2]}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"serviceAccountName": parts[2], "automountServiceAccountToken": false, "containers": []any{deploymentplan.Object{"name": "app", "image": f.p.Plan.Bootstrap.Image}}}}}}
		}
		pod := environmentPKIPod(object)
		container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": s.BootstrapSessionID}))
		if _, err := f.native.Write(t.Context(), "apply", object, false); err != nil {
			t.Fatal(err)
		}
	}
	controller := deploymentplan.Clone(f.native.objects["Deployment/"+s.BootstrapCredentialNamespace+"/pki-controller"])
	controls, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(f.cfg), controller, s.CleanupBinding)
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range append(controls, deploymentplan.Definition{Object: controller}) {
		if original := f.native.objects[deploymentplan.ID(def.Object)]; original != nil {
			deploymentplan.Map(def.Object["metadata"])["uid"] = deploymentplan.Map(original["metadata"])["uid"]
		}
		if _, err := f.native.Write(t.Context(), "apply", def.Object, false); err != nil {
			t.Fatal(err)
		}
	}
	leaf, err := parseFirstCertificatePEM([]byte(f.certificate))
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := environmentCreateBootstrapSubjects(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.protocol["historical_sessions"] = map[string]managedSessionState{s.BootstrapSessionID: {Session: s.BootstrapSessionID, Issuer: s.Intermediate.ID, Root: s.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Status: "active", Subjects: slices.Clone(subjects), Installed: slices.Clone(subjects), Expires: leaf.NotAfter}}
	var clients []managedSessionClient
	for _, subject := range subjects {
		count := 1
		if subject == "service:video-cloud-api" {
			count = 3
		}
		for i := 0; i < count; i++ {
			certificate, _, csr := historicalTransportSignedLeaf(t, s.Intermediate, f.issuerKey, subject, nil)
			leaf, err := parseFirstCertificatePEM([]byte(certificate))
			if err != nil {
				t.Fatal(err)
			}
			clients = append(clients, managedSessionClient{Subject: subject, Caller: subject, Request: fmt.Sprintf("original-finalizer-client-%s-%d", subject, i), Issuer: s.Intermediate.ID, Status: "succeeded", Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), CSR: csr, Certificate: certificate})
		}
	}
	f.protocol["historical_clients"] = clients
	f.protocol["historical_crls"] = map[string]managedLateCRLRecord{s.Intermediate.ID: historicalNativeSignedCRL(t, s.Intermediate, f.issuerKey)}
	f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.crt"] = f.certificate
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	f.runtime.Backend, f.runtime.Execution, f.runtime.ServiceCeremony = f.backend, f.e, &s
	return f, installHistoricalNativeFinalizer(t, f, nil)
}

func TestHistoricalNativeFinalizerAuthenticatesOwnersAndSignsOriginalRevocation(t *testing.T) {
	f, a := newHistoricalFinalizerNativeContract(t)
	s := a.service
	client := f.runtime.PKIClient
	url := "/v1/platform/pki/deployment-bootstrap/" + s.BootstrapSessionID + "/finalize"
	originalPrivate := createBootstrapStateSnapshot(t, f.store.Root)
	// Refusals occur before sealing or deleting anything. Restore each modeled
	// external fault before trying the independent next boundary.
	for _, mode := range []string{"wrong-auth", "missing-installation", "competing-active-session", "foreign-session-owner", "foreign-workload-uid", "namespace-label", "namespace-name", "broad-cleanup-role", "changed-temporary-policy", "foreign-workload-leaf", "replaced-primary-secret", "missing-primary-version", "foreign-native-fingerprint", "wrong-delete-version"} {
		t.Run(mode, func(t *testing.T) {
			beforeObjects := deploymentplan.Digest(f.native.objects)
			beforeCustody := deploymentplan.Digest(f.protocol["custody"])
			restore := func() {}
			switch mode {
			case "wrong-auth":
				old := client.token
				client.token = "different-operator"
				restore = func() { client.token = old }
			case "missing-installation", "foreign-native-fingerprint":
				states := f.protocol["historical_sessions"].(map[string]managedSessionState)
				old := states[s.BootstrapSessionID]
				state := old
				if mode == "missing-installation" {
					state.Installed = slices.Clone(state.Installed[:8])
				} else {
					state.Fingerprint = deploymentplan.Digest("foreign signed leaf")
				}
				states[state.Session] = state
				restore = func() { states[old.Session] = old }
			case "competing-active-session":
				states := f.protocol["historical_sessions"].(map[string]managedSessionState)
				state := states[s.BootstrapSessionID]
				state.Session = "99999999-9999-4999-8999-999999999999"
				states[state.Session] = state
				restore = func() { delete(states, state.Session) }
			case "foreign-session-owner", "foreign-workload-uid":
				id := historicalFinalizerSubjectOwners(f.p.Plan.Stack)["service:video-cloud-logingester"]
				old := deploymentplan.Clone(f.native.objects[id])
				if mode == "foreign-workload-uid" {
					deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				} else {
					c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(f.native.objects[id])["containers"])[0])
					c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": "99999999-9999-4999-8999-999999999999"}))
				}
				restore = func() { f.native.objects[id] = old }
			case "namespace-label", "namespace-name":
				id := "Namespace//" + s.BootstrapCredentialNamespace
				old := deploymentplan.Clone(f.native.objects[id])
				meta := deploymentplan.Map(f.native.objects[id]["metadata"])
				if mode == "namespace-label" {
					deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] = "different-stack"
				} else {
					meta["name"] = "different-namespace"
				}
				restore = func() { f.native.objects[id] = old }
			case "broad-cleanup-role":
				id := "Role/" + s.BootstrapCredentialNamespace + "/bootstrap-cleanup"
				old := deploymentplan.Clone(f.native.objects[id])
				rule := deploymentplan.Map(certIssuerObjectList(f.native.objects[id]["rules"])[0])
				rule["resourceNames"] = []any{"*"}
				restore = func() { f.native.objects[id] = old }
			case "changed-temporary-policy":
				path := os.Getenv("RTK_HISTORICAL_SESSION_BAO")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var state deploymentplan.Object
				if json.Unmarshal(raw, &state) != nil {
					t.Fatal("original native policy")
				}
				deploymentplan.Map(state["policies"])["rtk-"+f.cfg.Environment+"-service-bootstrap-"+s.BootstrapSessionID] = "foreign policy"
				if os.WriteFile(path, mustProviderRecoveryJSON(state), 0600) != nil {
					t.Fatal("mutate isolated policy")
				}
				restore = func() {
					if os.WriteFile(path, raw, 0600) != nil {
						t.Fatal("restore isolated policy")
					}
				}
			case "foreign-workload-leaf":
				old := slices.Clone(f.protocol["historical_clients"].([]managedSessionClient))
				changed := slices.Clone(old)
				changed[0].Certificate = f.certificate
				f.protocol["historical_clients"] = changed
				restore = func() { f.protocol["historical_clients"] = old }
			case "replaced-primary-secret", "missing-primary-version":
				id := "Secret/" + s.BootstrapCredentialNamespace + "/" + s.BootstrapCredentialSecret
				old := deploymentplan.Clone(f.native.objects[id])
				if mode == "replaced-primary-secret" {
					deploymentplan.Map(f.native.objects[id]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
				} else {
					delete(deploymentplan.Map(f.native.objects[id]["metadata"]), "resourceVersion")
				}
				restore = func() { f.native.objects[id] = old }
			}
			var err error
			if mode == "wrong-delete-version" {
				ns := s.BootstrapCredentialNamespaces[1]
				body := mustProviderRecoveryJSON(deploymentplan.Object{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": deploymentplan.Object{"uid": s.CredentialUIDs[ns], "resourceVersion": "changed-version"}})
				_, err = f.backend.command(t.Context(), body, "delete", "--raw=/api/v1/namespaces/"+ns+"/secrets/"+s.BootstrapCredentialSecret, "-f", "-")
			} else {
				err = client.call(t.Context(), http.MethodPost, url, "", nil, nil)
			}
			restore()
			if err == nil || !a.final.FinalizedAt.IsZero() || len(a.marker) != 0 || deploymentplan.Digest(f.native.objects) != beforeObjects || deploymentplan.Digest(f.protocol["custody"]) != beforeCustody {
				t.Fatal("native finalizer accepted a changed owner or changed custody on refusal", err)
			}
			expected := map[string]string{"missing-installation": "nine native installations", "competing-active-session": "another original native bootstrap session", "foreign-session-owner": "another bootstrap session", "foreign-workload-uid": "owner changed", "namespace-label": "namespace owner changed", "namespace-name": "namespace owner changed", "broad-cleanup-role": "cleanup authority changed", "changed-temporary-policy": "temporary policy changed", "foreign-workload-leaf": "original signed client identity", "replaced-primary-secret": "primary Secret changed", "missing-primary-version": "exact temporary credential", "foreign-native-fingerprint": "different original signed leaf"}
			if want := expected[mode]; want != "" && !strings.Contains(a.rejected, want) {
				t.Fatal("native refusal was caused by an unrelated prerequisite", a.rejected)
			}
		})
	}
	if err := finalizeEnvironmentCreateServiceSession(t.Context(), client, f.cfg, f.p.Plan.Bootstrap, f.store, f.runtime, s, f.operator); err != nil {
		t.Fatal("unchanged production finalizer with authentic external native protocol", err)
	}
	block, _ := pem.Decode([]byte(a.crl.PEM))
	if block == nil {
		t.Fatal("real native CRL missing")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	issuer, issuerErr := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	leaf, leafErr := parseFirstCertificatePEM([]byte(f.certificate))
	if err != nil || issuerErr != nil || leafErr != nil || crl.CheckSignatureFrom(issuer) != nil || len(crl.RevokedCertificateEntries) != 1 || crl.RevokedCertificateEntries[0].SerialNumber.Cmp(leaf.SerialNumber) != 0 || a.final.CRLSHA256 != environmentPreflightBytesSHA256(block.Bytes) {
		t.Fatal("native finalization did not revoke exactly the original signed temporary leaf")
	}
	if a.logins != 1 || len(a.marker) == 0 || createOpenBaoACKCompletionEntry(f.e.Journal, "service-session-finalize").State != "completed" {
		t.Fatal("actual finalizer omitted login, native tombstone or durable completion")
	}
	beforeRetry := deploymentplan.Digest([]any{a.final, a.crl, f.native.writes, f.protocol["custody"]})
	var again environmentCreateBootstrapFinalization
	if err := client.call(t.Context(), http.MethodPost, url, "", nil, &again); err != nil || !managedLateFinalizationEqual(again, a.final) || deploymentplan.Digest([]any{a.final, a.crl, f.native.writes, f.protocol["custody"]}) != beforeRetry {
		t.Fatal("same native completion request reissued or changed original evidence", err)
	}
	var bao deploymentplan.Object
	if readExecutionJSON(os.Getenv("RTK_HISTORICAL_SESSION_BAO"), &bao) != nil || deploymentplan.Map(bao["policies"])["original-permanent-issuer"] != "untouched issuer policy" || deploymentplan.Map(bao["policies"])["rtk-"+f.cfg.Environment+"-service-bootstrap-"+s.BootstrapSessionID] != nil || len(a.acks) < 3 {
		t.Fatal("native finalization lost permanent authorization or lacked consumed signed CRL")
	}
	after := createBootstrapStateSnapshot(t, f.store.Root)
	for path, digest := range originalPrivate {
		if strings.HasPrefix(path, "pki/") && after[path] != digest {
			t.Fatal("temporary finalization changed original permanent authority", path)
		}
	}
	for _, ns := range s.BootstrapCredentialNamespaces {
		if f.native.objects["Secret/"+ns+"/"+s.BootstrapCredentialSecret] != nil {
			t.Fatal("native credential copy survived exact cleanup", ns)
		}
	}
}

func TestHistoricalNativeFinalizerArchivesExactExpiredSameKeyCustody(t *testing.T) {
	f, a := newHistoricalFinalizerNativeContract(t)
	s := a.service
	leaf, err := parseFirstCertificatePEM([]byte(f.certificate))
	issuer, issuerErr := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	if err != nil || issuerErr != nil {
		t.Fatal(err, issuerErr)
	}
	// A separate signed historical input is selected before this isolated
	// custody operation. No source receipt or current certificate is rewritten.
	oldTemplate := *leaf
	oldTemplate.SerialNumber = big.NewInt(10001)
	oldTemplate.NotBefore, oldTemplate.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, &oldTemplate, issuer, leaf.PublicKey, f.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	old := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + s.Intermediate.Certificate + s.Root.Certificate
	custody := f.protocol["custody"].(map[string]string)
	path := "/run/pki-bootstrap/renew-native-finalizer/bootstrap.crt"
	custody[path], custody["/run/pki-bootstrap/bootstrap.crt"] = f.certificate, old
	r := &createServiceSessionRecovery{Renewal: true, Review: deploymentplan.Digest("isolated native custody operation"), Intent: createServiceSessionIntent{SessionID: "99999999-8888-4888-8888-999999999999"}, RenewedIntent: createServiceSessionIntent{SessionID: s.BootstrapSessionID}, RenewedCertPath: path, OriginalKeySHA: environmentPreflightBytesSHA256([]byte(f.key)), OriginalCSRSHA: environmentPreflightBytesSHA256([]byte(f.csr))}
	a.recovery = r
	f.runtime.ServiceSessionRecovery = r
	f.publish(t)
	for _, ns := range s.BootstrapCredentialNamespaces {
		if ns != s.BootstrapCredentialNamespace {
			if err := removeEnvironmentCreateCredentialCopy(t.Context(), f.cfg, f.runtime, s, ns); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.runtime.PKIClient.call(t.Context(), http.MethodPost, "/v1/platform/pki/deployment-bootstrap/"+s.BootstrapSessionID+"/finalize", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(custody)
	if err := a.archive("test ! -L /run/pki-bootstrap && ln /run/pki-bootstrap/bootstrap.crt /foreign/bootstrap.crt"); err == nil || deploymentplan.Digest(custody) != before {
		t.Fatal("unselected custody archive path changed original material")
	}
	if err := prepareRenewedBootstrapCleanup(t.Context(), f.cfg, f.runtime, s, a.final); err != nil {
		t.Fatal("actual production same-key archive and independent readback", err)
	}
	archive := "/run/pki-bootstrap/expired-" + r.Intent.SessionID + "/bootstrap.crt"
	if custody[archive] != old || custody["/run/pki-bootstrap/bootstrap.crt"] != f.certificate || custody["/run/pki-bootstrap/bootstrap.key"] != f.key || custody["/run/pki-bootstrap/bootstrap.csr"] != f.csr {
		t.Fatal("native archive replaced or lost original same-key custody")
	}
	key := "bootstrap-renewed-cleanup-custody-" + r.Review
	if entry := createOpenBaoACKCompletionEntry(f.e.Journal, key); entry == nil || entry.State != "completed" {
		t.Fatal("actual custody producer omitted durable original archive receipt")
	}
}
