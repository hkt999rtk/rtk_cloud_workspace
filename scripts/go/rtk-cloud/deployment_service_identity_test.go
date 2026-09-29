package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type deploymentSignerFixture struct {
	store            secretStore
	cfg              deploymentServiceIssuer
	requests         []string
	firstUnavailable bool
	wrongReply       bool
}

func newDeploymentSignerFixture(t *testing.T) *deploymentSignerFixture {
	t.Helper()
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate, rootKey, rootPEM, _, err := newLKECertificateAuthority("Service test root", "p256")
	if err != nil {
		t.Fatal(err)
	}
	rootCerts, _ := pemCertificates([]byte(rootPEM))
	root := rootCerts[0]
	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	intermediateTemplate := &x509.Certificate{SerialNumber: big.NewInt(22), Subject: pkix.Name{CommonName: "Service intermediate"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, rootTemplate, intermediateKey.Public(), rootKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediate, _ := x509.ParseCertificate(intermediateDER)
	intermediatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intermediateDER}))
	bootstrapCert, bootstrapKey, err := newLKESignedCertificate(rootTemplate, rootKey, "service:deployment-bootstrap", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, "p256")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &deploymentSignerFixture{store: store}
	var savedChain string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/certificates/service-client/issue" || r.Method != http.MethodPost || len(r.TLS.PeerCertificates) == 0 {
			t.Error("unexpected enrollment request")
			w.WriteHeader(400)
			return
		}
		var request struct {
			RequestID string `json:"request_id"`
			Subject   string `json:"subject"`
			CSR       string `json:"csr_pem"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("bad request")
			w.WriteHeader(400)
			return
		}
		fixture.requests = append(fixture.requests, request.RequestID+request.CSR)
		if savedChain == "" {
			block, _ := pem.Decode([]byte(request.CSR))
			if block == nil {
				t.Error("missing CSR")
				w.WriteHeader(400)
				return
			}
			csr, err := x509.ParseCertificateRequest(block.Bytes)
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if csr.CheckSignature() != nil {
				t.Error("bad CSR signature")
			}
			leaf := &x509.Certificate{SerialNumber: big.NewInt(33), Subject: pkix.Name{CommonName: request.Subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
			der, err := x509.CreateCertificate(rand.Reader, leaf, intermediate, csr.PublicKey, intermediateKey)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			savedChain = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + intermediatePEM + rootPEM
		}
		if fixture.firstUnavailable && len(fixture.requests) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if fixture.wrongReply {
			request.Subject = "service:shadow"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"request_id": request.RequestID, "subject": request.Subject, "caller_identity": "service:deployment-bootstrap", "certificate_chain_pem": savedChain})
	}))
	pool := x509.NewCertPool()
	pool.AddCert(root)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	server.StartTLS()
	t.Cleanup(server.Close)
	serverCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	cfg := deploymentServiceIssuer{Environment: "dev", Stack: "identity-test", Endpoint: server.URL, ServerName: "example.com", ServerCAFile: "pki/services/server-ca.crt", RootCAFile: "pki/services/root.crt", RootSHA256: certificateSHA256(root), BootstrapCertFile: "pki/services/bootstrap.crt", BootstrapKeyFile: "pki/services/bootstrap.key", RegistrationServerCAFile: "pki/services/root.crt"}
	raw, _ := json.Marshal(cfg)
	for file, value := range map[string]string{"pki/services/issuer.json": string(raw), cfg.RootCAFile: rootPEM, cfg.ServerCAFile: serverCA, cfg.BootstrapCertFile: bootstrapCert, cfg.BootstrapKeyFile: bootstrapKey} {
		if err := store.write(file, []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	fixture.cfg = cfg
	return fixture
}

func TestDeploymentIdentityRetriesPersistedRequestAndReusesCompletedCredential(t *testing.T) {
	f := newDeploymentSignerFixture(t)
	f.firstUnavailable = true
	if _, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, ""); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("uncertain issuance error=%v", err)
	}
	path := filepath.Join(f.store.Root, "pki/services/ota/identity.json")
	pending, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record deploymentServiceIdentity
	_ = json.Unmarshal(pending, &record)
	if record.CSR == "" || record.PrivateKey == "" || record.CertificateChain != "" {
		t.Fatal("pending request not durably saved")
	}
	ready, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || f.requests[0] != f.requests[1] || ready.PrivateKey != record.PrivateKey {
		t.Fatal("retry changed the request or key")
	}
	before, _ := os.ReadFile(path)
	_ = os.Remove(filepath.Join(f.store.Root, f.cfg.BootstrapKeyFile))
	_ = os.Remove(filepath.Join(f.store.Root, f.cfg.BootstrapCertFile))
	again, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if again != ready || !bytes.Equal(before, after) || len(f.requests) != 2 {
		t.Fatal("redeployment modified or signed the completed identity")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("identity file is not private")
	}
	if _, err := ensureDeploymentServiceIdentity(f.store, "other-stack", "service:ota", nil, nil, ""); err == nil {
		t.Fatal("cross-stack identity accepted")
	}
}

func TestDeploymentIdentityRejectsUnexpectedResponseAndPreservesPending(t *testing.T) {
	f := newDeploymentSignerFixture(t)
	f.wrongReply = true
	if _, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, ""); err == nil {
		t.Fatal("wrong subject reply accepted")
	}
	path := filepath.Join(f.store.Root, "pki/services/ota/identity.json")
	raw, _ := os.ReadFile(path)
	var saved deploymentServiceIdentity
	_ = json.Unmarshal(raw, &saved)
	if saved.CertificateChain != "" || saved.CSR == "" {
		t.Fatal("bad response installed or pending record discarded")
	}
	_, otherKey, _ := newLKECertificatePrivateKey("p256")
	saved.PrivateKey = otherKey
	if err := writeDeploymentServiceIdentity(f.store, "pki/services/ota/identity.json", saved); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, ""); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("corrupt pending key error=%v", err)
	}
	if len(f.requests) != 1 {
		t.Fatal("corrupt pending request was submitted")
	}
}

func TestDeploymentIdentityAdoptsVerifiedExistingServiceWithoutSigning(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "identity-test"}
	fixture := newLKEPlatformCertificateFixture(t, env)
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := kubernetesSecretBytes(fixture.listener, "client-ca.crt")
	cfg := deploymentServiceIssuer{Environment: "dev", Stack: "identity-test", Endpoint: "https://issuer.invalid", ServerName: "issuer.invalid", RootCAFile: "pki/services/root.crt", RootSHA256: certificateSHA256(fixture.issuer), RegistrationServerCAFile: "pki/services/root.crt"}
	raw, _ := json.Marshal(cfg)
	if err := store.write("pki/services/issuer.json", raw, false); err != nil {
		t.Fatal(err)
	}
	if err := store.write(cfg.RootCAFile, root, false); err != nil {
		t.Fatal(err)
	}
	existing := fixture.identities["service:ota"]
	record, err := ensureDeploymentServiceIdentity(store, cfg.Stack, "service:ota", existing, fixture.listener, lkeRegistrationServerDNS(env))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := kubernetesSecretBytes(existing, "client.key")
	if record.Source != "adopted" || record.PrivateKey != string(key) {
		t.Fatal("existing identity replaced")
	}
	cfg.RootSHA256 = strings.Repeat("0", 64)
	raw, _ = json.Marshal(cfg)
	_ = store.write("pki/services/issuer.json", raw, true)
	if _, err := ensureDeploymentServiceIdentity(store, cfg.Stack, "service:ota", existing, fixture.listener, lkeRegistrationServerDNS(env)); err == nil {
		t.Fatal("root pin drift accepted")
	}
}

func TestDeploymentIdentityLockAndTLSCorruptionFailWithoutReplacement(t *testing.T) {
	paths := provisionPaths{EnvRoot: t.TempDir()}
	dir := filepath.Join(paths.EnvRoot, "state", "certissuer")
	unlock, err := lockDeploymentIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateLKECertIssuerMaterial(paths, map[string]string{"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519"}); err == nil {
		t.Fatal("concurrent owner accepted")
	}
	unlock()
	first, err := loadOrCreateLKECertIssuerMaterial(paths, map[string]string{"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519"})
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, _ := newLKECertificatePrivateKey("ed25519")
	keyPath := filepath.Join(dir, "client.key")
	if err := os.WriteFile(keyPath, []byte(otherKey), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateLKECertIssuerMaterial(paths, map[string]string{"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519"}); err == nil {
		t.Fatal("wrong key accepted")
	}
	if readTestFile(t, filepath.Join(dir, "client.crt")) != first.ClientCert || readTestFile(t, keyPath) != otherKey {
		t.Fatal("corrupt state was replaced")
	}
	_ = os.Remove(keyPath)
	_ = os.Symlink(filepath.Join(dir, "server.key"), keyPath)
	if _, err := loadOrCreateLKECertIssuerMaterial(paths, map[string]string{"CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "ed25519"}); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestDeploymentIdentityContinuityBlocksLostLocalStateAndManagedDowngrade(t *testing.T) {
	old := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = filepath.Join(t.TempDir(), "dev")
	t.Cleanup(func() { activeSecretEnvironmentRoot = old })
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "identity-test"}
	opts := provisionOptions{workloads: []string{"account-manager"}}
	dir := t.TempDir()
	cmd := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
case "$*" in
 *"get deployment"*) printf '%s' "$TEST_IDENTITY_DEPLOYMENT" ;;
 *"get secret"*) printf '%s' "$TEST_IDENTITY_SECRET" ;;
 *) exit 42 ;;
esac
`
	if err := os.WriteFile(cmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", cmd)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("TEST_IDENTITY_DEPLOYMENT", `{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"PKI_CONTROLLER_SOCKET","value":"/run/account-pki/private/controller.sock"}]}]}}}}`)
	if err := lkeCheckDeploymentIdentityContinuity(provisionPaths{}, env, opts); err == nil || !strings.Contains(err.Error(), "managed identity") {
		t.Fatalf("managed downgrade error=%v", err)
	}
	t.Setenv("TEST_IDENTITY_DEPLOYMENT", "")
	t.Setenv("TEST_IDENTITY_SECRET", lkeTestSecretJSON(t, lkeTestSecret(map[string][]byte{"client.crt": []byte("installed-certificate"), "client.key": []byte("installed-key"), "ca.crt": []byte("installed-ca")})))
	if err := lkeCheckDeploymentIdentityContinuity(provisionPaths{}, env, opts); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("lost local state error=%v", err)
	}
	certDir := filepath.Join(activeSecretEnvironmentRoot, "pki/certissuer")
	if err := os.MkdirAll(certDir, 0700); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{"client.crt": "installed-certificate", "client.key": "installed-key", "service-ca.crt": "installed-ca"} {
		if err := os.WriteFile(filepath.Join(certDir, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := lkeCheckDeploymentIdentityContinuity(provisionPaths{}, env, opts); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(certDir, "client.crt"), []byte("older-certificate"), 0600)
	if err := lkeCheckDeploymentIdentityContinuity(provisionPaths{}, env, opts); err == nil || !strings.Contains(err.Error(), "no overwrite") {
		t.Fatalf("drift error=%v", err)
	}
}

func TestDeploymentIdentitySeedInstallationReusesAndRejectsDrift(t *testing.T) {
	f := newDeploymentSignerFixture(t)
	record, err := ensureDeploymentServiceIdentity(f.store, f.cfg.Stack, "service:ota", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cmd := filepath.Join(dir, "kubectl")
	saved := filepath.Join(dir, "created.json")
	script := `#!/bin/sh
case "$*" in
 *"get secret"*) printf '%s' "$TEST_IDENTITY_SECRET" ;;
 *"create -f -"*) umask 077; cat > "$TEST_CREATED_IDENTITY" ;;
 *) exit 42 ;;
esac
`
	if err := os.WriteFile(cmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", cmd)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("TEST_IDENTITY_SECRET", "")
	t.Setenv("TEST_CREATED_IDENTITY", saved)
	env := map[string]string{"CLOUD_STACK_NAME": f.cfg.Stack}
	if err := installDeploymentIdentitySeed(f.store, env, record); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(saved)
	var created map[string]any
	if json.Unmarshal(raw, &created) != nil {
		t.Fatal("seed not delivered")
	}
	if created["immutable"] != true {
		t.Fatal("seed Secret not immutable")
	}
	seed, _ := json.Marshal(record)
	live := lkeTestSecret(map[string][]byte{"identity.json": seed})
	t.Setenv("TEST_IDENTITY_SECRET", lkeTestSecretJSON(t, live))
	_ = os.Remove(saved)
	if err := installDeploymentIdentitySeed(f.store, env, record); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved); !os.IsNotExist(err) {
		t.Fatal("same seed recreated")
	}
	changed := record
	changed.Fingerprint = "other"
	seed, _ = json.Marshal(changed)
	t.Setenv("TEST_IDENTITY_SECRET", lkeTestSecretJSON(t, lkeTestSecret(map[string][]byte{"identity.json": seed})))
	if err := installDeploymentIdentitySeed(f.store, env, record); err == nil {
		t.Fatal("changed live seed overwritten")
	}
}

func TestDeploymentIdentityRejectsSymlinkedStoreAncestor(t *testing.T) {
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(store.Root, "pki")); err != nil {
		t.Fatal(err)
	}
	if err := store.write("pki/services/identity.json", []byte("private"), false); err == nil {
		t.Fatal("secret written outside environment")
	}
}

func TestDeploymentIdentityCommandBindsEnvironmentAndRequiresStackConfirmation(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	f := newDeploymentSignerFixture(t)
	f.cfg.Stack = "video-cloud-dev"
	raw, _ := json.Marshal(f.cfg)
	if err := f.store.write("pki/services/issuer.json", raw, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", f.store.ConfigRoot)
	args := []string{"service-identity", "--workspace", workspace, "--environment", "dev", "--subject", "service:account-manager"}
	if err := runDeploymentWithOperations(args, deploymentOperations{}); err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("missing confirmation=%v", err)
	}
	if len(f.requests) != 0 {
		t.Fatal("signed before stack confirmation")
	}
	args = append(args, "--confirm", "video-cloud-dev")
	if err := runDeploymentWithOperations(args, deploymentOperations{}); err != nil {
		t.Fatal(err)
	}
	if err := runDeploymentWithOperations(args, deploymentOperations{}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 {
		t.Fatal("second CLI deployment signed another identity")
	}
	if err := runDeploymentServiceIdentity([]string{"--environment", "dev", "--subject", "service:deployment-bootstrap"}); err == nil {
		t.Fatal("normal deployment attempted to mint bootstrap authority")
	}
}

func TestDeploymentIdentityPlatformPreflightAdoptsReinstallsAndRejectsDrift(t *testing.T) {
	env := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "identity-test"}
	fixture := newLKEPlatformCertificateFixture(t, env)
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := kubernetesSecretBytes(fixture.listener, "client-ca.crt")
	cfg := deploymentServiceIssuer{Environment: "dev", Stack: env["CLOUD_STACK_NAME"], Endpoint: "https://issuer.invalid", ServerName: "issuer.invalid", RootCAFile: "pki/services/root.crt", RootSHA256: certificateSHA256(fixture.issuer), RegistrationServerCAFile: "pki/services/root.crt"}
	raw, _ := json.Marshal(cfg)
	if err := store.write("pki/services/issuer.json", raw, false); err != nil {
		t.Fatal(err)
	}
	if err := store.write(cfg.RootCAFile, root, false); err != nil {
		t.Fatal(err)
	}
	old := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = store.Root
	t.Cleanup(func() { activeSecretEnvironmentRoot = old })
	dir := t.TempDir()
	cmd, saved := filepath.Join(dir, "kubectl"), filepath.Join(dir, "created.json")
	script := `#!/bin/sh
case "$*" in
 *"get secret account-manager-service-registration-tls"*) printf '%s' "$TEST_REGISTRATION_SECRET" ;;
 *"get secret"*) printf '%s' "$TEST_IDENTITY_SECRET" ;;
 *"create -f -"*) umask 077; cat > "$TEST_CREATED_IDENTITY" ;;
 *) exit 42 ;;
esac
`
	if err := os.WriteFile(cmd, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", cmd)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("TEST_CREATED_IDENTITY", saved)
	t.Setenv("TEST_REGISTRATION_SECRET", lkeTestSecretJSON(t, fixture.listener))
	t.Setenv("TEST_IDENTITY_SECRET", lkeTestSecretJSON(t, fixture.identities["service:ota"]))
	check := func() error {
		return lkeRequirePlatformServiceIdentitySecret(env, "ota-identity", "OTA identity Secret", "service:ota")
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, "pki/services/ota/identity.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(saved); !os.IsNotExist(err) {
		t.Fatal("adoption mutated live identity")
	}
	t.Setenv("TEST_IDENTITY_SECRET", "")
	if err := check(); err != nil {
		t.Fatal(err)
	}
	var installed map[string]any
	if json.Unmarshal([]byte(readTestFile(t, saved)), &installed) != nil {
		t.Fatal("missing identity was not installed")
	}
	for _, key := range []string{"client.crt", "client.key", "server-ca.crt"} {
		got, _ := kubernetesSecretBytes(installed, key)
		want, _ := kubernetesSecretBytes(fixture.identities["service:ota"], key)
		if !bytes.Equal(got, want) {
			t.Fatalf("reinstall changed %s", key)
		}
	}
	_ = os.Remove(saved)
	t.Setenv("TEST_IDENTITY_SECRET", lkeTestSecretJSON(t, installed))
	if err := check(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_IDENTITY_SECRET", lkeTestSecretJSON(t, fixture.identities["service:shadow"]))
	if err := check(); err == nil || !strings.Contains(err.Error(), "no overwrite") {
		t.Fatalf("live drift error=%v", err)
	}
	if _, err := os.Stat(saved); !os.IsNotExist(err) {
		t.Fatal("repeated preflight or drift overwrote live identity")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("repeat deployment changed environment credential")
	}
}
