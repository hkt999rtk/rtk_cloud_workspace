package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"video_cloud/internal/pki"
	"video_cloud/internal/serviceregistration"
)

type nativeProductRenewalFixture struct {
	store                 *pki.Store
	db                    *sql.DB
	root, issuer          *x509.Certificate
	rootKey, issuerKey    crypto.Signer
	rootID                string
	old, renewed          map[string]string
	state                 string
	oldExpiry             time.Time
	key, csr, certificate []byte
	signs, logins         *atomic.Int32
	leafExpiry            *atomic.Int64
}

type nativeRenewalRevocationProvider struct {
	issuer *x509.Certificate
	key    crypto.Signer
	calls  int
}

func (*nativeRenewalRevocationProvider) ReconcileIntermediate(context.Context, string, string) (string, error) {
	return "", pki.ErrDenied
}
func (*nativeRenewalRevocationProvider) GenerateIntermediate(context.Context, string, string) (string, error) {
	return "", pki.ErrDenied
}
func (*nativeRenewalRevocationProvider) InstallIntermediate(context.Context, string, string) error {
	return pki.ErrDenied
}
func (*nativeRenewalRevocationProvider) ConfigureClientRole(context.Context, string) error {
	return pki.ErrDenied
}
func (p *nativeRenewalRevocationProvider) PublishServiceClientRevocation(_ context.Context, _ string, certificate string) (string, error) {
	chain, err := pki.ParseCertificates(certificate)
	if err != nil || len(chain) != 1 {
		return "", pki.ErrDenied
	}
	p.calls++
	now := time.Now().UTC().Truncate(time.Second)
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(2), ThisUpdate: now, NextUpdate: now.Add(time.Hour), RevokedCertificateEntries: []x509.RevocationListEntry{{SerialNumber: chain[0].SerialNumber, RevocationTime: now}}}, p.issuer, p.key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), err
}

type nativeRenewalCleanup struct {
	store   *pki.Store
	removed bool
}

func (c *nativeRenewalCleanup) VerifyWorkloadAcknowledgments(ctx context.Context, s pki.DeploymentBootstrapSession) error {
	return c.store.VerifyDeploymentBootstrapWorkloads(ctx, s, time.Now().UTC())
}
func (c *nativeRenewalCleanup) RemoveBootstrapAuthorization(context.Context, pki.DeploymentBootstrapSession) error {
	c.removed = true
	return nil
}
func (c *nativeRenewalCleanup) VerifyBootstrapAuthorizationAbsent(context.Context, pki.DeploymentBootstrapSession) (string, error) {
	if !c.removed {
		return "", pki.ErrUnavailable
	}
	return strings.Repeat("a", 64), nil
}

func exerciseExpiredProductBootstrapRenewal(t *testing.T, f nativeProductRenewalFixture) {
	t.Helper()
	ctx := context.Background()
	subjects := strings.Split(f.old["PKI_BOOTSTRAP_SUBJECTS"], ",")
	slices.Sort(subjects)
	if len(subjects) != 5 {
		t.Fatal("native renewal fixture must declare all exact five products")
	}
	issuerID := f.old["PKI_BOOTSTRAP_ISSUER_ID"]
	oldSID := f.old["PKI_BOOTSTRAP_SESSION_ID"]
	issuer, err := f.store.GetIssuer(ctx, issuerID)
	if err != nil {
		t.Fatal(err)
	}
	issuer.ServiceClientIDs = append(slices.Clone(subjects), "service:deployment-bootstrap")
	slices.Sort(issuer.ServiceClientIDs)
	raw, _ := json.Marshal(issuer)
	if _, err := f.db.Exec(`UPDATE pki_issuers SET document=$2 WHERE id=$1`, issuerID, raw); err != nil {
		t.Fatal(err)
	}
	req := pki.CreateRequest{Scope: issuer.Scope, Kind: issuer.Kind, ParentID: issuer.ParentID, ServiceClientIDs: issuer.ServiceClientIDs}
	if _, err := f.db.Exec(`INSERT INTO pki_operations(id,issuer_id,idempotency_key,digest,status,created_by,created_at,action) VALUES($1,$2,'native-product-renewal',$3,'active','fixture',$4,'provision')`, uuid.NewString(), issuerID, req.Digest(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	admin := pki.Principal{ID: "isolated-renewal-operator", Roles: []string{"pki_admin"}, AuthTime: time.Now().UTC(), MFA: true}
	crl := func(id string, certificate *x509.Certificate, key crypto.Signer) {
		now := time.Now().UTC().Truncate(time.Second)
		der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(1), ThisUpdate: now.Add(-time.Second), NextUpdate: now.Add(time.Hour)}, certificate, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.ImportCRL(ctx, admin, id, string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	crl(f.rootID, f.root, f.rootKey)
	crl(issuerID, f.issuer, f.issuerKey)
	pairs := map[string]tls.Certificate{}
	fingerprints := map[string]string{}
	for n, subject := range subjects {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, key)
		if err != nil {
			t.Fatal(err)
		}
		request := pki.ServiceClientIssuanceRequest{Environment: "dev", Caller: "service:deployment-bootstrap", RequestID: "original-product-" + strings.TrimPrefix(subject, "service:"), Subject: subject, CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), TTLDays: 1, ContextDigest: "original-product-session"}
		now := time.Now().UTC()
		if err := f.store.AuthorizeDeploymentBootstrap(ctx, "dev", oldSID, request.Caller, subject, now); err != nil {
			t.Fatal("original native session did not authorize original product", err)
		}
		claim, err := f.store.BeginServiceClientIssuance(ctx, request, now)
		if err != nil {
			t.Fatal("native original product signing claim", err)
		}
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(int64(101 + n)), Subject: pkix.Name{CommonName: subject}, BasicConstraintsValid: true, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, f.issuer, key.Public(), f.issuerKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		if _, err := f.store.CompleteServiceClientIssuance(ctx, request, claim.ClaimToken, leaf, now); err != nil {
			t.Fatal("native original product completion", err)
		}
		encoded, _ := x509.MarshalPKCS8PrivateKey(key)
		pair, err := tls.X509KeyPair([]byte(leaf+issuer.Chain), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
		if err != nil {
			t.Fatal(err)
		}
		pairs[subject] = pair
		fingerprints[subject] = pki.Fingerprint(mustNativeRenewalCertificate(t, der))
	}
	before, err := f.store.GetDeploymentBootstrapSession(ctx, "dev", oldSID)
	if err != nil || len(before.InstalledSubjects) != 0 {
		t.Fatal("old native session unexpectedly acknowledged", err)
	}
	if delay := time.Until(f.oldExpiry.Add(30 * time.Millisecond)); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
	}
	if err := provisionBootstrap(); err == nil {
		t.Fatal("expired original session was silently reopened")
	}
	if f.signs.Load() != 1 || f.logins.Load() != 1 {
		t.Fatal("expired old retry signed again")
	}
	for _, subject := range subjects {
		if err := f.store.AcknowledgeDeploymentBootstrapSubject(ctx, "dev", oldSID, subject, time.Now().UTC()); err == nil {
			t.Fatal("expired original session accepted a new ACK")
		}
	}
	newSID := f.renewed["PKI_BOOTSTRAP_SESSION_ID"]
	logicalPath := f.renewed["PKI_BOOTSTRAP_CERT_PATH"]
	if newSID == oldSID || f.renewed["PKI_BOOTSTRAP_DEPLOYMENT_ID"] == f.old["PKI_BOOTSTRAP_DEPLOYMENT_ID"] || f.renewed["PKI_BOOTSTRAP_KEY_PATH"] != f.old["PKI_BOOTSTRAP_KEY_PATH"] || f.renewed["PKI_BOOTSTRAP_CSR_PATH"] != f.old["PKI_BOOTSTRAP_CSR_PATH"] || !strings.HasPrefix(logicalPath, "/run/pki-bootstrap/renew-") || !strings.HasSuffix(logicalPath, "/bootstrap.crt") {
		t.Fatal("renewal did not preserve original key/CSR and distinct certificate/session intent")
	}
	boundary := map[string]string{}
	for _, k := range []string{"PKI_DATABASE_URL", "OPENBAO_ADDR", "OPENBAO_CACERT", "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE"} {
		boundary[k] = os.Getenv(k)
	}
	for k, v := range f.renewed {
		t.Setenv(k, v)
	}
	for k, v := range boundary {
		t.Setenv(k, v)
	}
	path := filepath.Join(f.state, strings.TrimPrefix(logicalPath, "/run/pki-bootstrap/"))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PKI_BOOTSTRAP_KEY_PATH", filepath.Join(f.state, "bootstrap.key"))
	t.Setenv("PKI_BOOTSTRAP_CSR_PATH", filepath.Join(f.state, "bootstrap.csr"))
	t.Setenv("PKI_BOOTSTRAP_CERT_PATH", path)
	t.Setenv("PKI_BOOTSTRAP_EXPIRES_AT", "")
	// The native process retains its selected isolated registry/TLS boundary.
	f.leafExpiry.Store(time.Now().UTC().Add(8 * time.Hour).UnixNano())
	if err := provisionBootstrap(); err != nil {
		t.Fatal("actual native same-key product renewal", err)
	}
	if f.signs.Load() != 2 || f.logins.Load() != 2 {
		t.Fatal("renewal did not mint exactly one bootstrap leaf")
	}
	if key, _ := readBootstrapPrivate(filepath.Join(f.state, "bootstrap.key")); !bytes.Equal(key, f.key) {
		t.Fatal("renewal replaced original private key")
	}
	if csr, _ := readBootstrapPrivate(filepath.Join(f.state, "bootstrap.csr")); !bytes.Equal(csr, f.csr) {
		t.Fatal("renewal replaced original CSR")
	}
	if old, _ := readBootstrapPrivate(filepath.Join(f.state, "bootstrap.crt")); !bytes.Equal(old, f.certificate) {
		t.Fatal("original expired certificate overwritten")
	}
	renewed, err := f.store.GetDeploymentBootstrapSession(ctx, "dev", newSID)
	if err != nil || renewed.IssuerID != before.IssuerID || renewed.RootSHA256 != before.RootSHA256 || !slices.Equal(renewed.Subjects, before.Subjects) || renewed.BootstrapFingerprint == before.BootstrapFingerprint || !renewed.ExpiresAt.After(time.Now()) {
		t.Fatal("native renewed session lineage differs", err)
	}
	old, err := f.store.GetDeploymentBootstrapSession(ctx, "dev", oldSID)
	if err != nil || old.Status != "expired" || old.BootstrapFingerprint != before.BootstrapFingerprint || old.ExpiresAt != before.ExpiresAt || len(old.InstalledSubjects) != 0 {
		t.Fatal("original expired session history changed", err)
	}
	if err := provisionBootstrap(); err != nil || f.signs.Load() != 2 || f.logins.Load() != 2 {
		t.Fatal("renewed retry signed another leaf", err)
	}
	exerciseNativeProductReadyACKs(t, f, pairs, newSID, issuerID)
	if err := f.store.VerifyDeploymentBootstrapWorkloads(ctx, renewedWithACKs(t, f.store, newSID), time.Now().UTC()); err != nil {
		t.Fatal("native finalizer workload verification", err)
	}
	provider := &nativeRenewalRevocationProvider{issuer: f.issuer, key: f.issuerKey}
	controller := &pki.Controller{Store: f.store, Provider: provider, RequiredConsumers: []string{"mqtt", "shadow", "ota", "video-storage", "webrtc"}}
	cleanup := &nativeRenewalCleanup{store: f.store}
	if _, err := controller.FinalizeDeploymentBootstrap(ctx, admin, "dev", newSID, cleanup, time.Now().UTC()); err == nil || cleanup.removed {
		t.Fatal("finalizer skipped native CRL consumer proof")
	} else {
		t.Log("first finalization expected block", err, "provider calls", provider.calls)
	}
	current, err := f.store.GetCRL(ctx, issuerID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, consumer := range controller.RequiredConsumers {
		if err := f.store.AcknowledgeCRL(ctx, issuerID, current.Digest, consumer, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	finalized, err := controller.FinalizeDeploymentBootstrap(ctx, admin, "dev", newSID, cleanup, time.Now().UTC())
	if err != nil || !cleanup.removed || finalized.SessionID != newSID {
		t.Fatal("actual native finalization", err)
	}
	retry, err := controller.FinalizeDeploymentBootstrap(ctx, admin, "dev", newSID, cleanup, time.Now().UTC())
	if err != nil || retry != finalized || provider.calls != 1 {
		t.Fatal("native finalization retry changed receipt", err)
	}
	var sessions, issued int
	if err := f.db.QueryRow(`SELECT count(*) FROM pki_deployment_bootstrap_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM pki_service_client_issuances`).Scan(&issued); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 || issued != 7 {
		t.Fatal("renewal reissued permanent product identities", sessions, issued)
	}
	for subject, fp := range fingerprints {
		var actual string
		if err := f.db.QueryRow(`SELECT fingerprint FROM pki_service_client_issuances WHERE subject=$1`, subject).Scan(&actual); err != nil || actual != fp {
			t.Fatal("original permanent product receipt changed", err)
		}
	}
	t.Log("REAL: native OpenBao TLS bootstrap, expired original refusal, same-key/CSR distinct-session renewal, PostgreSQL issuance/ACK/seal/revocation/finalization. SIMULATED: OpenBao signing provider and CRL consumer/cleanup adapters; no Kubernetes or dev state.")
}
func mustNativeRenewalCertificate(t *testing.T, raw []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func renewedWithACKs(t *testing.T, s *pki.Store, id string) pki.DeploymentBootstrapSession {
	t.Helper()
	v, err := s.GetDeploymentBootstrapSession(context.Background(), "dev", id)
	if err != nil || len(v.InstalledSubjects) != 5 {
		t.Fatal("missing actual five native ACKs", err)
	}
	return v
}

func exerciseNativeProductReadyACKs(t *testing.T, f nativeProductRenewalFixture, pairs map[string]tls.Certificate, session, issuerID string) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(f.root)
	registered, ready := map[string]bool{}, map[string]bool{}
	controller := &pki.HTTPServer{Controller: &pki.Controller{Store: f.store}, Environment: "dev", ServiceClientRootSHA256: pki.Fingerprint(f.root)}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "identity", 403)
			return
		}
		subject := r.TLS.PeerCertificates[0].Subject.CommonName
		if strings.HasPrefix(r.URL.Path, "/v1/pki/") {
			if !registered[subject] || !ready[subject] {
				http.Error(w, "runtime not registered/ready", 403)
				return
			}
			controller.ServeHTTP(w, r)
			return
		}
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "body", 400)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			ready[subject] = request["ready"] == true
		} else {
			if request["service_id"] != strings.TrimPrefix(subject, "service:") {
				http.Error(w, "subject", 403)
				return
			}
			registered[subject] = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"heartbeat_seconds": 5, "lease_expires_at": time.Now().Add(time.Minute)})
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	server.StartTLS()
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "controller.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for subject, pair := range pairs {
		dir := t.TempDir()
		certPath, keyPath := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
		var chain []byte
		for _, der := range pair.Certificate {
			chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
		}
		key, _ := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
		if err := os.WriteFile(certPath, chain, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PKI_BOOTSTRAP_CONTROLLER_URL", server.URL)
		t.Setenv("PKI_BOOTSTRAP_ISSUER_ID", issuerID)
		t.Setenv("PKI_BOOTSTRAP_SESSION_ID", session)
		clientHTTP, err := serviceregistration.NewMTLSHTTPClient(certPath, keyPath, ca)
		if err != nil {
			t.Fatal(err)
		}
		client, err := serviceregistration.New(server.URL, clientHTTP, serviceregistration.Manifest{ServiceID: strings.TrimPrefix(subject, "service:"), InstanceID: strings.TrimPrefix(subject, "service:") + "-original", ManifestVersion: "1", EndpointRef: "isolated", Options: []serviceregistration.Option{{Code: "runtime", DisplayName: "runtime"}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Register(context.Background()); err != nil {
			t.Fatal("real runtime registration", err)
		}
		if err := client.Heartbeat(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		before := renewedWithPartialACKs(t, f.store, session)
		if slices.Contains(before.InstalledSubjects, subject) {
			t.Fatal("unready runtime acknowledged")
		}
		if err := client.Heartbeat(context.Background(), true); err != nil || !client.Available() {
			t.Fatal("real ready runtime mTLS ACK", subject, err)
		}
	}
}
func renewedWithPartialACKs(t *testing.T, s *pki.Store, id string) pki.DeploymentBootstrapSession {
	t.Helper()
	v, err := s.GetDeploymentBootstrapSession(context.Background(), "dev", id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
