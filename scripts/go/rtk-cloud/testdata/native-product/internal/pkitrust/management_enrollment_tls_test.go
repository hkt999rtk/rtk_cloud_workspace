package pkitrust

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"video_cloud/internal/certissuer"
	"video_cloud/internal/config"
	"video_cloud/internal/openbao"
	"video_cloud/internal/pki"
	"video_cloud/internal/serviceidentity"
	"video_cloud/internal/serviceregistration"
)

// Actual native client, issuer, registry and bootstrap ACK implementations run
// here. Only the offline signer and platform registration lease boundary are
// replaced; neither can fabricate a registry issuance or installed session row.
func TestProductManagementEnrollmentUsesCurrentServiceRootAndSameBootstrap(t *testing.T) {
	path := os.Getenv("RTK_PRODUCT_MANAGEMENT_TLS_FIXTURE")
	if path == "" {
		t.Skip("maintained rendered product settings required")
	}
	var fixture struct {
		Environment, HistoricalTransport string
		Settings                         []map[string]string
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &fixture) != nil || len(fixture.Settings) != 5 {
		t.Fatal("rendered input unavailable")
	}
	oldBlock, _ := pem.Decode([]byte(fixture.HistoricalTransport))
	if oldBlock == nil {
		t.Fatal("historical CA absent")
	}
	oldCA, err := x509.ParseCertificate(oldBlock.Bytes)
	if err != nil || !oldCA.NotAfter.Before(time.Now()) {
		t.Fatal("historical transport must be expired")
	}
	store := serviceIntegrationStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	root, rc, rk := serviceIntegrationCA(t, nil, nil, now)
	issuer, ic, ik := serviceIntegrationCA(t, &root, rk, now)
	root.Environment, issuer.Environment = fixture.Environment, fixture.Environment
	issuerName := fixture.Settings[0]["PKI_CLIENT_ISSUER_SERVER_PKI_NAME"]
	if issuerName == "" {
		t.Fatal("rendered issuer name absent")
	}
	subjects := []string{}
	for _, settings := range fixture.Settings {
		subjects = append(subjects, settings["TEST_SUBJECT"])
	}
	slices.Sort(subjects)
	issuer.ServiceClientIDs = append(slices.Clone(subjects), "service:deployment-bootstrap")
	slices.Sort(issuer.ServiceClientIDs)
	issuer.ServerDNSNames = []string{issuerName}
	for _, i := range []pki.Issuer{root, issuer} {
		document, _ := json.Marshal(i)
		if _, err := store.DB.Exec(`INSERT INTO pki_issuers(id,environment,domain,cloud_id,product_id,kind,version,parent_id,status,document) VALUES($1,$2,'service','','',$3,1,NULLIF($4,'')::uuid,'active',$5)`, i.ID, i.Environment, i.Kind, i.ParentID, document); err != nil {
			t.Fatal(err)
		}
	}
	approved := pki.CreateRequest{Scope: issuer.Scope, Kind: issuer.Kind, ParentID: issuer.ParentID, ServiceClientIDs: issuer.ServiceClientIDs, ServerDNSNames: issuer.ServerDNSNames}
	if _, err := store.DB.Exec(`INSERT INTO pki_operations(id,issuer_id,idempotency_key,digest,status,created_by,created_at,action) VALUES($1,$2,'product-management-tls',$3,'active','fixture',$4,'provision')`, uuid.NewString(), issuer.ID, approved.Digest(), now); err != nil {
		t.Fatal(err)
	}
	admin := pki.Principal{ID: "operator", Roles: []string{"pki_admin"}, MFA: true, AuthTime: now}
	for _, record := range []pki.CRL{signedCRL(t, root, rc, rk, 1, now, now), signedCRL(t, issuer, ic, ik, 1, now, now)} {
		if _, err := store.ImportCRL(ctx, admin, record.IssuerID, record.PEM, now); err != nil {
			t.Fatal(err)
		}
	}
	sign := func(csr *x509.CertificateRequest, serial int64, server bool) string {
		use := x509.ExtKeyUsageClientAuth
		if server {
			use = x509.ExtKeyUsageServerAuth
		}
		tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: csr.Subject, DNSNames: csr.DNSNames, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{use}}
		der, err := x509.CreateCertificate(rand.Reader, tpl, ic, csr.PublicKey, ik)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	// Seed the one original native bootstrap receipt, then reuse that exact leaf
	// and private key for all five enrollments.
	bootstrapKey, bootstrapCSR, err := serviceidentity.GenerateBootstrapCSR("service:deployment-bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	csr, _ := pki.ParseCSR(bootstrapCSR)
	bootstrapLeaf := sign(csr, 10, false)
	// This is the original provisionBootstrap output from real OpenBao's
	// issuing_ca plus ca_chain=[issuing ICA, root]. Never rewrite its bytes.
	bootstrapChain := bootstrapLeaf + issuer.Certificate + issuer.Certificate + root.Certificate
	certs, _ := pki.ParseCertificates(bootstrapChain)
	sid := uuid.NewString()
	session := pki.DeploymentBootstrapSession{Environment: fixture.Environment, DeploymentID: "native-product-management-tls", SessionID: sid, Caller: "service:deployment-bootstrap", IssuerID: issuer.ID, RootSHA256: root.Fingerprint, BootstrapFingerprint: pki.Fingerprint(certs[0]), Subjects: subjects, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.CreateDeploymentBootstrapSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeploymentBootstrapCertificate(ctx, session.Environment, sid, bootstrapCSR, bootstrapLeaf, now); err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requestDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: issuerName}, DNSNames: []string{issuerName}}, key)
	serverRequest := pki.ServerIssuanceRequest{Environment: fixture.Environment, Domain: "service", Caller: "service:certissuer", RequestID: "native-issuer-listener", CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER})), DNSNames: []string{issuerName}, TTLDays: 1, ContextDigest: "native-product-management-tls"}
	claim, err := store.BeginServerIssuance(ctx, serverRequest, now)
	if err != nil {
		t.Fatal(err)
	}
	serverCSR, _ := pki.ParseCSR(serverRequest.CSR)
	issued, err := store.CompleteServerIssuance(ctx, serverRequest, claim.ClaimToken, sign(serverCSR, 11, true), now)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	serverPair, err := tls.X509KeyPair([]byte(issued.CertificateChainPEM), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	var signatures, bootstrapPeers, acks atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			CSR string `json:"csr"`
			CN  string `json:"common_name"`
		}
		if r.URL.Path != "/v1/"+issuer.Reference+"/sign/service-client" || json.NewDecoder(r.Body).Decode(&request) != nil || !slices.Contains(subjects, request.CN) {
			w.WriteHeader(400)
			return
		}
		csr, err := pki.ParseCSR(request.CSR)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		leaf := sign(csr, int64(100+signatures.Add(1)), false)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"certificate": leaf}})
	}))
	defer provider.Close()
	bao, err := openbao.NewClient(openbao.Config{Addr: provider.URL, Token: "synthetic-provider"})
	if err != nil {
		t.Fatal(err)
	}
	svc := certissuer.NewService(nil, nil, nil, nil, nil, nil, nil, 1, 90, 30, certissuer.WithServiceClientIssuers(&certissuer.ServiceClientIssuers{Registry: store, Client: bao, Environment: fixture.Environment, RootSHA256: root.Fingerprint}, regexp.MustCompile("^$")), certissuer.WithServiceClientBootstrapSession(sid, "service:deployment-bootstrap", subjects...))
	router := svc.Router()
	roots := x509.NewCertPool()
	roots.AddCert(rc)
	issuerServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || pki.Fingerprint(r.TLS.PeerCertificates[0]) != session.BootstrapFingerprint {
			t.Error("enrollment did not reuse original bootstrap mTLS identity")
			w.WriteHeader(403)
			return
		}
		if len(r.TLS.PeerCertificates) != 4 || !bytes.Equal(r.TLS.PeerCertificates[1].Raw, r.TLS.PeerCertificates[2].Raw) || len(r.TLS.VerifiedChains[0]) != 3 {
			t.Error("bootstrap TLS did not retain the raw four-block chain and cryptographically build the three-certificate verification path")
			w.WriteHeader(403)
			return
		}
		bootstrapPeers.Add(1)
		router.ServeHTTP(w, r)
	}))
	issuerServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	issuerServer.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	issuerServer.StartTLS()
	defer issuerServer.Close()
	controller := &pki.HTTPServer{Controller: &pki.Controller{Store: store}, Environment: fixture.Environment, ServiceClientRootSHA256: root.Fingerprint}
	// Registration is a real mTLS handshake/lease response; only its business
	// database is stubbed. The bootstrap ACK itself uses the native SQL handler.
	registry := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(403)
			return
		}
		peer := r.TLS.PeerCertificates[0]
		if store.VerifyServiceClient(r.Context(), fixture.Environment, peer.Subject.CommonName, root.Fingerprint, r.TLS.PeerCertificates, time.Now()) != nil {
			w.WriteHeader(403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/pki/") {
			controller.ServeHTTP(w, r)
			acks.Add(1)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"heartbeat_seconds": 5, "lease_expires_at": time.Now().Add(time.Minute)})
	}))
	registry.Config.ErrorLog = log.New(io.Discard, "", 0)
	listenerKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	listenerDER, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(12), Subject: pkix.Name{CommonName: "isolated-controller"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, rc, listenerKey.Public(), rk)
	listenerKeyDER, _ := x509.MarshalPKCS8PrivateKey(listenerKey)
	listenerPair, _ := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: listenerDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: listenerKeyDER}))
	registry.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{listenerPair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots}
	registry.StartTLS()
	defer registry.Close()
	directory := t.TempDir()
	caPath := filepath.Join(directory, "current-root.pem")
	oldPath := filepath.Join(directory, "historical-expired.pem")
	certPath := filepath.Join(directory, "bootstrap.crt")
	keyPath := filepath.Join(directory, "bootstrap.key")
	for path, text := range map[string]string{caPath: root.Certificate, oldPath: fixture.HistoricalTransport, certPath: bootstrapChain, keyPath: bootstrapKey} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for index, rendered := range fixture.Settings {
		subject := rendered["TEST_SUBJECT"]
		settings := map[string]string{}
		for k, v := range rendered {
			settings[k] = v
		}
		if settings["PKI_CLIENT_ISSUER_TLS_CA"] != "/run/pki-service/root.pem" || settings["PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT"] != "/run/pki-bootstrap/tls.crt" || settings["PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY"] != "/run/pki-bootstrap/tls.key" {
			t.Fatal("maintained recipe selected historical management trust")
		}
		settings["PKI_CLIENT_IDENTITY_STATE"] = filepath.Join(directory, strings.TrimPrefix(subject, "service:"), "client.json")
		settings["PKI_CLIENT_ROOT_SHA256"] = root.Fingerprint
		settings["PKI_CLIENT_ISSUER_SERVER_PKI_ROOT_SHA256"] = root.Fingerprint
		settings["PKI_CLIENT_ISSUER_SERVER_PKI_NAME"] = issuerName
		settings["PKI_CLIENT_ISSUER_TLS_CA"] = caPath
		settings["PKI_CLIENT_IDENTITY_BOOTSTRAP_CERT"], settings["PKI_CLIENT_IDENTITY_BOOTSTRAP_KEY"] = certPath, keyPath
		get := func(k string) string { return settings[k] }
		if index == 0 {
			bad := config.LoadServerHTTPTrust("PKI_CLIENT_ISSUER", get)
			bad.CAFile = oldPath
			if client, err := LoadManagedServiceClientFromSettings(ctx, store, get, "PKI_CLIENT", subject, fixture.Environment, issuerServer.URL, bad, nil); err == nil {
				client.Close()
				t.Fatal("expired old management CA authenticated current listener")
			}
			if signatures.Load() != 0 {
				t.Fatal("wrong CA reached signer")
			}
		}
		client, err := LoadManagedServiceClientFromSettings(ctx, store, get, "PKI_CLIENT", subject, fixture.Environment, issuerServer.URL, config.LoadServerHTTPTrust("PKI_CLIENT_ISSUER", get), nil)
		if err != nil {
			t.Fatal("current Service root enrollment", subject, err)
		}
		identity, err := client.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(identity.Certificate) != 3 {
			t.Fatal("regular native product enrollment did not return the canonical leaf/ICA/root chain")
		}
		keyDER, _ := x509.MarshalPKCS8PrivateKey(identity.PrivateKey)
		leafFile := filepath.Join(directory, "client-"+strings.TrimPrefix(subject, "service:")+".crt")
		leafKey := leafFile + ".key"
		chain := []byte{}
		for _, der := range identity.Certificate {
			chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
		}
		_ = os.WriteFile(leafFile, chain, 0600)
		_ = os.WriteFile(leafKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600)
		_ = client.Close()
		t.Setenv("PKI_BOOTSTRAP_CONTROLLER_URL", registry.URL)
		t.Setenv("PKI_BOOTSTRAP_ISSUER_ID", issuer.ID)
		t.Setenv("PKI_BOOTSTRAP_SESSION_ID", sid)
		httpClient, err := serviceregistration.NewMTLSHTTPClient(leafFile, leafKey, caPath)
		if err != nil {
			t.Fatal(err)
		}
		role := strings.TrimPrefix(subject, "service:")
		registration, err := serviceregistration.New(registry.URL, httpClient, serviceregistration.Manifest{ServiceID: role, InstanceID: role + "-1", ManifestVersion: "1", EndpointRef: role, Options: []serviceregistration.Option{{Code: role, DisplayName: role}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = registration.Register(ctx); err != nil {
			t.Fatal(err)
		}
		if err = registration.Heartbeat(ctx, true); err != nil || !registration.Available() {
			t.Fatal("native registration ACK", subject, err)
		}
		before := signatures.Load()
		restarted, err := LoadManagedServiceClientFromSettings(ctx, store, get, "PKI_CLIENT", subject, fixture.Environment, issuerServer.URL, config.LoadServerHTTPTrust("PKI_CLIENT_ISSUER", get), nil)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := restarted.Current(ctx)
		_ = restarted.Close()
		if !bytes.Equal(again.Certificate[0], identity.Certificate[0]) || signatures.Load() != before {
			t.Fatal("installed identity restart signed again")
		}
	}
	var installed []byte
	if err := store.DB.QueryRow(`SELECT to_json(installed_subjects) FROM pki_deployment_bootstrap_sessions WHERE session_id=$1`, sid).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	var actual []string
	if json.Unmarshal(installed, &actual) != nil {
		t.Fatal("native ACK rows unavailable")
	}
	slices.Sort(actual)
	originalKey, _ := os.ReadFile(keyPath)
	originalCert, _ := os.ReadFile(certPath)
	if !slices.Equal(actual, subjects) || signatures.Load() != 5 || bootstrapPeers.Load() != 5 || acks.Load() != 5 || string(originalKey) != bootstrapKey || string(originalCert) != bootstrapChain {
		t.Fatal("native enrollment/ACK replaced bootstrap identity, duplicated issuance or changed roster")
	}
	t.Log("all5 native registry-backed mTLS enrollments and ready-registration ACKs use current Service Root; expired historical CA rejected; original four-block bootstrap key/cert reused; regular product outputs remain three-block chains; no restart resign")
}
