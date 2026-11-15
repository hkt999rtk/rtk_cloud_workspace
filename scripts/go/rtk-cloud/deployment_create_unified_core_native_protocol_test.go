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
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// unifiedCoreNativeBackend joins the accepted-core protocol to the existing
// file-backed Kubernetes fake. All ownership/writes use the latter; only
// account tunnels and exact native exec protocol are delegated to core. The
// registry ACK IPC, when configured, remains handled by the file-backed fake.
func unifiedCoreNativeBackend(t *testing.T, f *acceptedCoreExecutionContract) (environmentKubernetesBackend, string, string) {
	t.Helper()
	if f == nil || f.e == nil || f.protocolPath == "" || f.store != f.e.Store {
		t.Fatal("original accepted core fixture required")
	}
	coreKube := os.Getenv("RTK_CLOUD_KUBECTL")
	if coreKube == "" {
		t.Fatal("original isolated core protocol unavailable")
	}
	kube, err := readBoundedEnvironmentFile(f.store.KubeconfigPath(), true)
	if err != nil || string(kube) != string(f.backend.KubeconfigContent) {
		t.Fatal("original canonical kubeconfig changed", err)
	}
	objects := coreContractNativeInventory(f.native.objects)
	backend, dir, calls := unifiedSessionNativeBackend(t, f.store, objects, nil)
	backend.KubeconfigContent = append([]byte(nil), kube...)
	backend.RolloutTimeout = f.backend.RolloutTimeout
	t.Setenv("RTK_COMBINED_CORE_KUBECTL", coreKube)
	t.Setenv("RTK_COMBINED_CANONICAL_KUBECONFIG", f.store.KubeconfigPath())
	path := filepath.Join(dir, "unified-kubectl.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	forwardMarker := "if len(a) < 4 or a[0] != '--kubeconfig' or not a[2].startswith('--request-timeout='):\n"
	forward := `if 'port-forward' in a:
    if len(a) < 2 or a[0] != '--kubeconfig': sys.exit(71)
    with open(a[1], 'rb') as f:
        if f.read() != open(os.environ['RTK_COMBINED_CANONICAL_KUBECONFIG'], 'rb').read(): sys.exit(72)
    os.execv(os.environ['RTK_COMBINED_CORE_KUBECTL'], [os.environ['RTK_COMBINED_CORE_KUBECTL']] + sys.argv[1:])
`
	if strings.Count(script, forwardMarker) != 1 {
		t.Fatal("original fake tunnel boundary changed")
	}
	script = strings.Replace(script, forwardMarker, forward+forwardMarker, 1)
	old := "    if f.read() != b'isolated-native-kubeconfig': sys.exit(72)"
	if strings.Count(script, old) != 1 {
		t.Fatal("original fake kubeconfig guard changed")
	}
	script = strings.Replace(script, old, "    if f.read() != open(os.environ['RTK_COMBINED_CANONICAL_KUBECONFIG'], 'rb').read(): sys.exit(72)", 1)
	marker := "a = a[3:]\n"
	dispatch := `a = a[3:]
# Account Manager protocol serves its own loopback HTTP only. Exec requests
# with explicit unified responses or real registry IPC remain on this backend.
if 'exec' in a:
    try:
        with open(os.path.join(root, 'exec-responses.json')) as f: responses = json.load(f) or {}
    except FileNotFoundError: responses = {}
    if ' '.join(a) not in responses and not (os.environ.get('RTK_UNIFIED_ACK_DIR') and 'registry-read' in a):
        os.execv(os.environ['RTK_COMBINED_CORE_KUBECTL'], [os.environ['RTK_COMBINED_CORE_KUBECTL']] + sys.argv[1:])
`
	if strings.Count(script, marker) != 1 {
		t.Fatal("original fake command boundary changed")
	}
	script = strings.Replace(script, marker, dispatch, 1)
	listMarker := "            print(json.dumps({'items': items}))\n"
	filtered := `            if kind == 'pods': items = [o for o in items if o.get('kind') == 'Pod']
            if '-l' in a:
                selectors = dict(part.split('=', 1) for part in a[a.index('-l')+1].split(','))
                items = [o for o in items if all(o.get('metadata', {}).get('labels', {}).get(k) == v for k, v in selectors.items())]
            print(json.dumps({'items': items}))
`
	if strings.Count(script, listMarker) != 1 {
		t.Fatal("original fake inventory boundary changed")
	}
	script = strings.Replace(script, listMarker, filtered, 1)
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return backend, dir, calls
}

func TestUnifiedCoreNativeBackendPreservesParentAndProtocol(t *testing.T) {
	f := newAcceptedCoreExecutionContract(t)
	originalJournal := deploymentplan.Digest(f.e.Journal)
	b, dir, _ := unifiedCoreNativeBackend(t, f)
	ns := f.e.Journal.Stack + "-account-manager"
	original, err := b.Read(t.Context(), "Deployment/"+ns+"/account-manager")
	if err != nil || original == nil {
		t.Fatal("original file-backed deployment unavailable", err)
	}
	accountUID := deploymentplan.String(deploymentplan.Map(original["metadata"])["uid"])
	if accountUID == "" {
		t.Fatal("original owner UID missing")
	}
	deploymentplan.Map(original["metadata"])["annotations"] = deploymentplan.Object{"fixture": "file-backed"}
	if _, err = b.Write(t.Context(), "update", original, false); err != nil {
		t.Fatal("native file-backed update", err)
	}
	updated, err := b.Read(t.Context(), "Deployment/"+ns+"/account-manager")
	if err != nil || deploymentplan.Map(deploymentplan.Map(updated["metadata"])["annotations"])["fixture"] != "file-backed" || deploymentplan.Map(deploymentplan.Map(f.native.objects["Deployment/"+ns+"/account-manager"]["metadata"])["annotations"])["fixture"] != nil {
		t.Fatal("file-backed Kubernetes is not the authority", err)
	}
	raw, err := b.command(t.Context(), nil, "--namespace", f.e.Journal.Stack+"-platform", "exec", "postgresql-0", "--", "registry-read", "SELECT isolated")
	var session managedSessionState
	if err != nil || json.Unmarshal(raw, &session) != nil || session.Session != f.session {
		t.Fatal("core registry protocol lost", err)
	}
	execArgs := []string{"--namespace", ns, "exec", "account-manager", "--", "fixture-exact"}
	responses, _ := json.Marshal(map[string]string{strings.Join(execArgs, " "): "exact-unified-response"})
	if err := os.WriteFile(filepath.Join(dir, "exec-responses.json"), responses, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := b.command(t.Context(), nil, execArgs...); err != nil || string(output) != "exact-unified-response" {
		t.Fatal("unified exact exec did not take precedence", err)
	}
	origin, client, err := newEnvironmentCreateAccountClient(t.Context(), f.cfg, b, f.e)
	if err != nil {
		t.Fatal("original native account transport", err)
	}
	pki, err := newEnvironmentCreatePKIClient(origin, client, "dev", f.operator["PKI_OPERATOR_USER_ID"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pki.Authenticate(t.Context(), f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		t.Fatal("core account login", err)
	}
	issuer, err := pki.ReadIssuer(t.Context(), f.service.Intermediate.ID)
	if err != nil || issuer["issuer_id"] != f.service.Intermediate.ID {
		t.Fatal("core issuer protocol", err)
	}
	// Verify actual kubeconfig bytes, not merely the selected environment name.
	changed := b
	changed.KubeconfigContent = []byte("foreign fixture kubeconfig")
	if _, err = changed.Read(context.Background(), "Deployment/"+ns+"/account-manager"); err == nil {
		t.Fatal("foreign kubeconfig accepted")
	}
	latest, err := b.Read(t.Context(), "Deployment/"+ns+"/account-manager")
	if err != nil || deploymentplan.String(deploymentplan.Map(latest["metadata"])["uid"]) != accountUID || deploymentplan.Digest(f.e.Journal) != originalJournal {
		t.Fatal("original parent or owner changed", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "exec-responses.json")); err != nil {
		t.Fatal("unified exec dispatcher missing", err)
	}
}

// This opt-in test validates the joined fake transport against the real
// product ACK endpoint and a separately isolated PostgreSQL schema. The PKI
// identities are synthetic transport inputs, not the accepted-core history.
func TestUnifiedCoreNativeBackendPostgresMTLSACK(t *testing.T) {
	if os.Getenv("RTK_TEST_REAL_UNIFIED_ACK") != "1" {
		t.Skip("explicit isolated PostgreSQL/mTLS ACK test required")
	}
	if os.Getenv("VIDEO_CLOUD_TEST_DSN") == "" {
		t.Fatal("isolated PostgreSQL is required")
	}
	f := newAcceptedCoreExecutionContract(t)
	originalJournal := deploymentplan.Digest(f.e.Journal)
	b, _, _ := unifiedCoreNativeBackend(t, f)
	root, rootKey, rootPEM := coreContractCertificate(t, "fixture-root", true, nil, nil, 2*365*24*time.Hour)
	issuer, issuerKey, issuerPEM := coreContractCertificate(t, "fixture-issuer", true, root, rootKey, 365*24*time.Hour)
	bootstrap, bootstrapKey, bootstrapPEM := coreContractCertificate(t, "service:deployment-bootstrap", false, issuer, issuerKey, time.Hour)
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	clientDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "service:openbao"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, issuer, &clientKey.PublicKey, issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	clientPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}))
	encodeKey := func(key *ecdsa.PrivateKey) string {
		raw, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
	}
	encodeCSR := func(name string, key *ecdsa.PrivateKey) string {
		raw, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: name}}, key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: raw}))
	}
	crl := func(certificate *x509.Certificate, key *ecdsa.PrivateKey) string {
		raw, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: time.Now().UTC().Add(-time.Minute), NextUpdate: time.Now().UTC().Add(time.Hour)}, certificate, key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: raw}))
	}
	bootstrapCSR := encodeCSR("service:deployment-bootstrap", bootstrapKey)
	clientCSR := encodeCSR("service:openbao", clientKey)
	clientCertificate, err := x509.ParseCertificate(clientDER)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		certificate *x509.Certificate
		csr         string
	}{{bootstrap, bootstrapCSR}, {clientCertificate, clientCSR}} {
		block, _ := pem.Decode([]byte(pair.csr))
		if block == nil {
			t.Fatal("synthetic CSR missing")
		}
		request, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || request.CheckSignature() != nil || !bytes.Equal(request.RawSubjectPublicKeyInfo, pair.certificate.RawSubjectPublicKeyInfo) || pair.certificate.CheckSignatureFrom(issuer) != nil {
			t.Fatal("synthetic CSR/key/issuer mismatch", err)
		}
	}
	if issuer.CheckSignatureFrom(root) != nil || !issuer.IsCA || !root.IsCA || clientCertificate.NotBefore.Before(now.Add(-5*time.Minute)) || !now.Add(30*time.Minute).Before(bootstrap.NotAfter) || issuer.NotAfter.Before(now.Add(24*time.Hour)) {
		t.Fatal("synthetic issuer chain or session/claim window differs from the proven native fixture")
	}
	const rootID = "11111111-2222-4333-8444-555555555555"
	const issuerID = "22222222-2222-4222-8222-222222222222"
	const sessionID = "66666666-6666-4666-8666-666666666666"
	fixture := map[string]any{
		"environment": "dev", "root_id": rootID, "issuer_id": issuerID,
		"issuer_mount": "pki-issuers/service/" + issuerID + "/v1", "root_pem": rootPEM, "issuer_pem": issuerPEM,
		"root_crl": crl(root, rootKey), "issuer_crl": crl(issuer, issuerKey),
		"bootstrap_key": encodeKey(bootstrapKey), "bootstrap_csr": bootstrapCSR,
		"sessions": []any{map[string]any{"id": sessionID, "deployment_id": "isolated-protocol-test", "certificate": bootstrapPEM + issuerPEM + rootPEM, "subjects": []string{"service:openbao"}, "created_at": time.Now().UTC().Add(-time.Minute), "expires_at": now.Add(30 * time.Minute)}},
		"clients":  []any{map[string]any{"subject": "service:openbao", "request_id": "request-0", "csr": clientCSR, "certificate": clientPEM, "key": encodeKey(clientKey)}},
	}
	ack := startUnifiedRealACKFixture(t, mustWorkspaceRoot(t), fixture)
	t.Setenv("RTK_UNIFIED_ACK_DIR", ack.dir)
	runtime := environmentCreateBootstrapRuntime{Execution: f.e, Backend: b}
	before, _, err := readManagedSessionState(t.Context(), f.cfg, runtime, sessionID)
	if err != nil || before.Session != sessionID || len(before.Installed) != 0 {
		t.Fatal("unified backend did not reach real empty registry", err)
	}
	if err := ack.ACK(t.Context(), sessionID, "service:openbao"); err != nil {
		t.Fatal("native service mTLS ACK", err)
	}
	after, _, err := readManagedSessionState(t.Context(), f.cfg, runtime, sessionID)
	if err != nil || after.Session != sessionID || !slices.Contains(after.Installed, "service:openbao") || after.Issuer != issuerID || after.Session == f.session {
		t.Fatal("unified backend did not observe real ACK", err)
	}
	if deploymentplan.Digest(f.e.Journal) != originalJournal {
		t.Fatal("ACK protocol changed original parent journal")
	}
	if err := ack.Stop(t.Context()); err != nil {
		t.Fatal("isolated native ACK harness", err)
	}
}
