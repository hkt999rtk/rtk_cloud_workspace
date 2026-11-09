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
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"video_cloud/internal/pki"
)

func TestNativeRenderedBootstrapOpenBaoTrustAndSameKeyResume(t *testing.T) {
	runNativeRenderedBootstrap(t, false)
}
func TestNativeRenderedBootstrapExpiredProductSessionRenewal(t *testing.T) {
	runNativeRenderedBootstrap(t, true)
}
func runNativeRenderedBootstrap(t *testing.T, renewal bool) {
	if os.Getenv("VIDEO_CLOUD_TEST_DSN") == "" || os.Getenv("RTK_PRODUCT_BOOTSTRAP_RENDERED_FIXTURE") == "" {
		t.Skip("rendered fixture and isolated PostgreSQL are required")
	}
	raw, err := os.ReadFile(os.Getenv("RTK_PRODUCT_BOOTSTRAP_RENDERED_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Jobs                                                map[string]map[string]any `json:"jobs"`
		Root, Intermediate, RootKey, IntermediateKey, OldCA string
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(raw, &data) != nil || json.Unmarshal(data["jobs"], &fixture.Jobs) != nil {
		t.Fatal("invalid rendered job fixture")
	}
	for key, target := range map[string]*string{"root": &fixture.Root, "intermediate": &fixture.Intermediate, "root_key": &fixture.RootKey, "intermediate_key": &fixture.IntermediateKey, "old_ca": &fixture.OldCA} {
		if json.Unmarshal(data[key], target) != nil {
			t.Fatal("invalid synthetic PKI fixture")
		}
	}
	parseCert := func(text string) *x509.Certificate {
		b, _ := pem.Decode([]byte(text))
		if b == nil {
			t.Fatal("certificate fixture missing")
		}
		c, e := x509.ParseCertificate(b.Bytes)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	parseKey := func(text string) crypto.Signer {
		b, _ := pem.Decode([]byte(text))
		if b == nil {
			t.Fatal("key fixture missing")
		}
		k, e := x509.ParsePKCS8PrivateKey(b.Bytes)
		if e != nil {
			t.Fatal(e)
		}
		return k.(crypto.Signer)
	}
	if !parseCert(fixture.OldCA).NotAfter.Before(time.Now()) {
		t.Fatal("historical temporary CA fixture must reproduce actual expiry")
	}
	root, issuer := parseCert(fixture.Root), parseCert(fixture.Intermediate)
	rootKey, issuerKey := parseKey(fixture.RootKey), parseKey(fixture.IntermediateKey)
	object := func(v any) map[string]any { m, _ := v.(map[string]any); return m }
	list := func(v any) []any { l, _ := v.([]any); return l }
	envs := func(job map[string]any) map[string]string {
		pod := object(object(object(job["spec"])["template"])["spec"])
		containers := list(pod["containers"])
		if len(containers) != 1 {
			t.Fatal("native bootstrap executor changed")
		}
		c := object(containers[0])
		command := list(c["command"])
		args := list(c["args"])
		if len(command) != 1 || command[0] != "/app/serviceidentity-bootstrap" || len(args) != 1 || args[0] != "bootstrap" {
			t.Fatal("not the actual native bootstrap command")
		}
		out := map[string]string{}
		for _, v := range list(c["env"]) {
			e := object(v)
			if s, ok := e["value"].(string); ok {
				out[e["name"].(string)] = s
			}
		}
		return out
	}
	original, managed := envs(fixture.Jobs["original"]), envs(fixture.Jobs["managed"])
	if len(original) != len(managed) {
		t.Fatal("trust repair changed native environment")
	}
	for k, v := range original {
		if managed[k] != v {
			t.Fatal("trust repair changed native setting", k)
		}
	}
	expectedRole, expectedMount := original["OPENBAO_KUBERNETES_ROLE"], original["PKI_BOOTSTRAP_ISSUER_MOUNT"]
	if expectedRole == "" || original["OPENBAO_ADDR"] != "https://openbao.video-cloud-dev-secrets.svc:8200" || original["OPENBAO_CACERT"] != "/run/openbao-ca/ca.crt" {
		t.Fatal("rendered selected endpoint/CA contract changed")
	}
	admin, err := sql.Open("postgres", os.Getenv("VIDEO_CLOUD_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "bootstrap_tls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	dsn := os.Getenv("VIDEO_CLOUD_TEST_DSN")
	if strings.Contains(dsn, "://") {
		u, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &pki.Store{DB: db}
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	seed := func(id, kind, parent, certificate, chain, fingerprint string) {
		cert := parseCert(certificate)
		i := pki.Issuer{ID: id, Scope: pki.Scope{Environment: "dev", Domain: "service"}, Kind: kind, Version: 1, ParentID: parent, Status: "active", Provider: "openbao", Reference: expectedMount, Fingerprint: fingerprint, Certificate: certificate, Chain: chain, NotBefore: cert.NotBefore, NotAfter: cert.NotAfter}
		if kind == "root" {
			i.Provider = "offline"
			i.Reference = ""
		}
		document, _ := json.Marshal(i)
		if _, e := db.Exec(`INSERT INTO pki_issuers(id,environment,domain,cloud_id,product_id,kind,version,parent_id,status,document) VALUES($1,'dev','service','','',$2,1,NULLIF($3,'')::uuid,'active',$4)`, id, kind, parent, document); e != nil {
			t.Fatal(e)
		}
	}
	rootID := uuid.NewString()
	seed(rootID, "root", "", fixture.Root, fixture.Root, pki.Fingerprint(root))
	seed(original["PKI_BOOTSTRAP_ISSUER_ID"], "intermediate", rootID, fixture.Intermediate, fixture.Intermediate+fixture.Root, pki.Fingerprint(issuer))
	var logins, signs atomic.Int32
	var leafExpiry atomic.Int64
	var originalExpiry time.Time
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			var req struct{ Role, JWT string }
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.Role != expectedRole || req.JWT != "isolated-projected-openbao-audience" {
				http.Error(w, "denied", 403)
				return
			}
			logins.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"auth": map[string]string{"client_token": "isolated-workload-token"}})
			return
		}
		if r.URL.Path != "/v1/"+expectedMount+"/sign/service-client" || r.Header.Get("X-Vault-Token") != "isolated-workload-token" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			CSR     string `json:"csr"`
			CN      string `json:"common_name"`
			Exclude bool   `json:"exclude_cn_from_sans"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.CN != original["PKI_BOOTSTRAP_SUBJECT"] || !req.Exclude {
			http.Error(w, "denied", 403)
			return
		}
		b, _ := pem.Decode([]byte(req.CSR))
		if b == nil {
			http.Error(w, "csr", 400)
			return
		}
		csr, e := x509.ParseCertificateRequest(b.Bytes)
		if e != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != req.CN {
			http.Error(w, "csr", 400)
			return
		}
		now := time.Now().UTC()
		expires := now.Add(30 * time.Minute)
		if raw := leafExpiry.Load(); raw != 0 {
			expires = time.Unix(0, raw)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(44 + int64(signs.Load())), Subject: csr.Subject, NotBefore: now.Add(-time.Minute), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		der, e := x509.CreateCertificate(rand.Reader, leaf, issuer, csr.PublicKey, issuerKey)
		if e != nil {
			http.Error(w, "sign", 500)
			return
		}
		signs.Add(1)
		// OpenBao's ca_chain includes the issuing CA as well as its root.
		// Preserve the real response envelope; provisionBootstrap currently
		// prepends issuing_ca, so the existing immutable output has four blocks.
		json.NewEncoder(w).Encode(map[string]any{"request_id": uuid.NewString(), "lease_id": "", "renewable": false, "lease_duration": 0, "data": map[string]any{"certificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "issuing_ca": fixture.Intermediate, "ca_chain": []string{fixture.Intermediate, fixture.Root}}, "wrap_info": nil, "warnings": nil, "auth": nil, "mount_type": "pki"})
	})
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	serverKey, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	serverDER, e := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(55), Subject: pkix.Name{CommonName: "isolated-openbao"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, root, serverKey.Public(), rootKey)
	if e != nil {
		t.Fatal(e)
	}
	serverKeyDER, _ := x509.MarshalPKCS8PrivateKey(serverKey)
	pair, e := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}))
	if e != nil {
		t.Fatal(e)
	}
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	server.StartTLS()
	defer server.Close()
	state := filepath.Join(t.TempDir(), "private")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(t.TempDir(), "projected-token")
	if err = os.WriteFile(token, []byte("isolated-projected-openbao-audience"), 0600); err != nil {
		t.Fatal(err)
	}
	for k, v := range original {
		t.Setenv(k, v)
	}
	for k, v := range map[string]string{"PKI_DATABASE_URL": dsn, "OPENBAO_ADDR": server.URL, "OPENBAO_SERVICE_ACCOUNT_TOKEN_FILE": token, "PKI_BOOTSTRAP_KEY_PATH": filepath.Join(state, "bootstrap.key"), "PKI_BOOTSTRAP_CSR_PATH": filepath.Join(state, "bootstrap.csr"), "PKI_BOOTSTRAP_CERT_PATH": filepath.Join(state, "bootstrap.crt")} {
		t.Setenv(k, v)
	}
	projected := func(job map[string]any) string {
		pod := object(object(object(job["spec"])["template"])["spec"])
		for _, v := range list(pod["volumes"]) {
			volume := object(v)
			if volume["name"] != "openbao-ca" {
				continue
			}
			cm := object(volume["configMap"])
			text := fixture.OldCA
			if cm["name"] == "pki-create-openbao-tls-consumer-trust" {
				items := list(cm["items"])
				if len(items) != 1 || object(items[0])["key"] != "roots.pem" || object(items[0])["path"] != "ca.crt" {
					t.Fatal("managed projected key/path changed")
				}
				text = fixture.Root
			} else if cm["name"] != "pki-openbao-transport-ca" {
				t.Fatal("foreign CA source")
			}
			path := filepath.Join(t.TempDir(), "ca.crt")
			if e := os.WriteFile(path, []byte(text), 0600); e != nil {
				t.Fatal(e)
			}
			return path
		}
		t.Fatal("missing rendered CA volume")
		return ""
	}
	t.Setenv("OPENBAO_CACERT", projected(fixture.Jobs["original"]))
	if err = provisionBootstrap(); err == nil || !strings.Contains(err.Error(), "unknown authority") {
		t.Fatal("old CA did not reproduce the actual native TLS failure", err)
	}
	keyBefore, e := readBootstrapPrivate(filepath.Join(state, "bootstrap.key"))
	if e != nil {
		t.Fatal(e)
	}
	csrBefore, e := readBootstrapPrivate(filepath.Join(state, "bootstrap.csr"))
	if e != nil {
		t.Fatal(e)
	}
	var sessions, issuances int
	if e = db.QueryRow(`SELECT count(*) FROM pki_deployment_bootstrap_sessions`).Scan(&sessions); e != nil {
		t.Fatal(e)
	}
	if sessions != 0 || logins.Load() != 0 || signs.Load() != 0 {
		t.Fatal("TLS failure minted or recorded a session")
	}
	t.Setenv("OPENBAO_CACERT", projected(fixture.Jobs["managed"]))
	if renewal {
		originalExpiry = time.Now().UTC().Truncate(time.Second).Add(12 * time.Second)
		leafExpiry.Store(originalExpiry.UnixNano())
		t.Setenv("PKI_BOOTSTRAP_EXPIRES_AT", originalExpiry.Format(time.RFC3339))
	}
	if err = provisionBootstrap(); err != nil {
		t.Fatal("correct current projected CA did not execute native session creation", err)
	}
	if e = db.QueryRow(`SELECT count(*) FROM pki_deployment_bootstrap_sessions`).Scan(&sessions); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM pki_service_client_issuances`).Scan(&issuances); e != nil {
		t.Fatal(e)
	}
	if sessions != 1 || issuances != 1 || logins.Load() != 1 || signs.Load() != 1 {
		t.Fatal("actual native session/issuance count differs")
	}
	keyAfter, _ := readBootstrapPrivate(filepath.Join(state, "bootstrap.key"))
	csrAfter, _ := readBootstrapPrivate(filepath.Join(state, "bootstrap.csr"))
	certBefore, _ := readBootstrapPrivate(filepath.Join(state, "bootstrap.crt"))
	chain, chainErr := pki.ParseCertificates(string(certBefore))
	if chainErr != nil || len(chain) != 4 || !bytes.Equal(chain[1].Raw, issuer.Raw) || !bytes.Equal(chain[1].Raw, chain[2].Raw) || !bytes.Equal(chain[3].Raw, root.Raw) {
		t.Fatal("real OpenBao issuing_ca plus ca_chain response was not retained as the original four-block bootstrap chain", chainErr)
	}
	if !bytes.Equal(keyBefore, keyAfter) || !bytes.Equal(csrBefore, csrAfter) {
		t.Fatal("CA-only correction replaced original key or CSR")
	}
	if err = provisionBootstrap(); err != nil {
		t.Fatal("same original native session could not resume", err)
	}
	certAfter, _ := readBootstrapPrivate(filepath.Join(state, "bootstrap.crt"))
	if !bytes.Equal(certBefore, certAfter) || logins.Load() != 1 || signs.Load() != 1 {
		t.Fatal("native resume signed again or changed certificate")
	}
	t.Log("real OpenBao response retained leaf/ICA/identicalICA/root; native old-CA failure, one current-CA issuance, PostgreSQL session and unchanged same-key/certificate resume PASS")
	if renewal {
		exerciseExpiredProductBootstrapRenewal(t, nativeProductRenewalFixture{store: store, db: db, root: root, issuer: issuer, rootKey: rootKey, issuerKey: issuerKey, rootID: rootID, old: original, renewed: envs(fixture.Jobs["renewal"]), state: state, oldExpiry: originalExpiry, key: keyBefore, csr: csrBefore, certificate: certBefore, signs: &signs, logins: &logins, leafExpiry: &leafExpiry})
	}
}
