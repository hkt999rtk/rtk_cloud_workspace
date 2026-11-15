package main

// This overlay is compiled only by an explicitly requested isolated workspace
// integration test; it is not part of a deployable image or production code.
import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"video_cloud/internal/pki"
	"video_cloud/internal/serviceregistration"
)

type unifiedNativeACKInput struct {
	Directory    string                    `json:"directory"`
	Environment  string                    `json:"environment"`
	RootID       string                    `json:"root_id"`
	IssuerID     string                    `json:"issuer_id"`
	IssuerMount  string                    `json:"issuer_mount"`
	RootPEM      string                    `json:"root_pem"`
	IssuerPEM    string                    `json:"issuer_pem"`
	RootCRL      string                    `json:"root_crl"`
	IssuerCRL    string                    `json:"issuer_crl"`
	BootstrapKey string                    `json:"bootstrap_key"`
	BootstrapCSR string                    `json:"bootstrap_csr"`
	Sessions     []unifiedNativeACKSession `json:"sessions"`
	Clients      []unifiedNativeACKClient  `json:"clients"`
}
type unifiedNativeACKSession struct {
	ID           string    `json:"id"`
	DeploymentID string    `json:"deployment_id"`
	Certificate  string    `json:"certificate"`
	Subjects     []string  `json:"subjects"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type unifiedNativeACKClient struct {
	Subject     string `json:"subject"`
	RequestID   string `json:"request_id"`
	CSR         string `json:"csr"`
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}
type unifiedNativeACKRequest struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Subject string `json:"subject"`
	Session string `json:"session"`
}
type unifiedNativeACKResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func TestNativeUnifiedSessionACKHarness(t *testing.T) {
	fixturePath := os.Getenv("RTK_UNIFIED_ACK_FIXTURE")
	if fixturePath == "" {
		t.Skip("explicit isolated unified ACK fixture required")
	}
	info, err := os.Lstat(fixturePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("private 0600 input required")
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var f unifiedNativeACKInput
	if json.Unmarshal(raw, &f) != nil || f.Environment != "dev" || len(f.Sessions) == 0 || len(f.Clients) == 0 {
		t.Fatal("invalid isolated input")
	}
	dir, err := filepath.Abs(f.Directory)
	if err != nil || dir != f.Directory {
		t.Fatal("absolute IPC directory required")
	}
	di, err := os.Lstat(dir)
	if err != nil || !di.IsDir() || di.Mode().Perm() != 0700 || filepath.Dir(fixturePath) != dir {
		t.Fatal("private IPC directory required")
	}
	dsn := os.Getenv("VIDEO_CLOUD_TEST_DSN")
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("isolated loopback PostgreSQL URL required")
	}
	parseCert := func(raw string) *x509.Certificate {
		block, _ := pem.Decode([]byte(raw))
		if block == nil {
			t.Fatal("invalid certificate")
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	root, issuer := parseCert(f.RootPEM), parseCert(f.IssuerPEM)
	if issuer.CheckSignatureFrom(root) != nil || !root.IsCA || !issuer.IsCA {
		t.Fatal("invalid original issuer chain")
	}
	bootstrapCSRBlock, _ := pem.Decode([]byte(f.BootstrapCSR))
	if bootstrapCSRBlock == nil {
		t.Fatal("bootstrap CSR required")
	}
	bootstrapCSR, err := x509.ParseCertificateRequest(bootstrapCSRBlock.Bytes)
	if err != nil || bootstrapCSR.CheckSignature() != nil {
		t.Fatal("bootstrap CSR signature")
	}
	publicKey := func(value any) []byte {
		b, err := x509.MarshalPKIXPublicKey(value)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, session := range f.Sessions {
		pair, err := tls.X509KeyPair([]byte(session.Certificate), []byte(f.BootstrapKey))
		if err != nil {
			t.Fatal("bootstrap key/certificate mismatch")
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || !bytes.Equal(publicKey(leaf.PublicKey), publicKey(bootstrapCSR.PublicKey)) || leaf.CheckSignatureFrom(issuer) != nil {
			t.Fatal("bootstrap CSR/certificate/issuer mismatch")
		}
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "unified_ack_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &pki.Store{DB: db}
	if err = store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	subjects := make([]string, 0, len(f.Clients)+1)
	for _, client := range f.Clients {
		subjects = append(subjects, client.Subject)
	}
	subjects = append(subjects, "service:deployment-bootstrap")
	slices.Sort(subjects)
	seed := func(id, kind, parent, cert, chain string, certificate *x509.Certificate) {
		item := pki.Issuer{ID: id, Scope: pki.Scope{Environment: f.Environment, Domain: "service"}, Kind: kind, Version: 1, ParentID: parent, Status: "active", Provider: "openbao", Reference: f.IssuerMount, Fingerprint: pki.Fingerprint(certificate), Certificate: cert, Chain: chain, NotBefore: certificate.NotBefore, NotAfter: certificate.NotAfter}
		if kind == "root" {
			item.Provider = "offline"
			item.Reference = ""
		} else {
			item.ServiceClientIDs = subjects
		}
		raw, _ := json.Marshal(item)
		if _, err := db.Exec(`INSERT INTO pki_issuers(id,environment,domain,cloud_id,product_id,kind,version,parent_id,status,document) VALUES($1,$2,'service','','',$3,1,NULLIF($4,'')::uuid,'active',$5)`, id, f.Environment, kind, parent, raw); err != nil {
			t.Fatal(err)
		}
		if kind != "root" {
			req := pki.CreateRequest{Scope: item.Scope, Kind: item.Kind, ParentID: item.ParentID, ServiceClientIDs: item.ServiceClientIDs}
			if _, err := db.Exec(`INSERT INTO pki_operations(id,issuer_id,idempotency_key,digest,status,created_by,created_at,action) VALUES($1,$2,'unified-ack',$3,'active','fixture',$4,'provision')`, uuid.NewString(), id, req.Digest(), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(f.RootID, "root", "", f.RootPEM, f.RootPEM, root)
	seed(f.IssuerID, "intermediate", f.RootID, f.IssuerPEM, f.IssuerPEM+f.RootPEM, issuer)
	adminPrincipal := pki.Principal{ID: "isolated-unified-operator", Roles: []string{"pki_admin"}, AuthTime: time.Now().UTC(), MFA: true}
	for _, item := range []struct{ id, crl string }{{f.RootID, f.RootCRL}, {f.IssuerID, f.IssuerCRL}} {
		if _, err := store.ImportCRL(t.Context(), adminPrincipal, item.id, item.crl, time.Now().UTC()); err != nil {
			t.Fatal("input CRL rejected", err)
		}
	}
	for _, item := range f.Sessions {
		cert := parseCert(item.Certificate)
		session := pki.DeploymentBootstrapSession{Environment: f.Environment, DeploymentID: item.DeploymentID, SessionID: item.ID, Caller: "service:deployment-bootstrap", IssuerID: f.IssuerID, RootSHA256: pki.Fingerprint(root), BootstrapFingerprint: pki.Fingerprint(cert), Subjects: item.Subjects, CreatedAt: item.CreatedAt, ExpiresAt: item.ExpiresAt}
		if err := store.CreateDeploymentBootstrapSession(t.Context(), session); err != nil {
			t.Fatal("native session", err)
		}
		leaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
		if err := store.RecordDeploymentBootstrapCertificate(t.Context(), f.Environment, item.ID, f.BootstrapCSR, string(leaf), item.CreatedAt); err != nil {
			t.Fatal("native bootstrap receipt", err)
		}
	}
	pairs := map[string]tls.Certificate{}
	for _, client := range f.Clients {
		pair, err := tls.X509KeyPair([]byte(client.Certificate+f.IssuerPEM), []byte(client.Key))
		if err != nil {
			t.Fatal("client key mismatch")
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || leaf.Subject.CommonName != client.Subject || leaf.CheckSignatureFrom(issuer) != nil {
			t.Fatal("client issuer/subject mismatch")
		}
		block, _ := pem.Decode([]byte(client.CSR))
		if block == nil {
			t.Fatal("client CSR missing")
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || !bytes.Equal(publicKey(csr.PublicKey), publicKey(leaf.PublicKey)) {
			t.Fatal("client CSR mismatch")
		}
		request := pki.ServiceClientIssuanceRequest{Environment: f.Environment, Caller: "service:deployment-bootstrap", RequestID: client.RequestID, Subject: client.Subject, CSR: client.CSR, TTLDays: 1, ContextDigest: "unified-original"}
		now := time.Now().UTC()
		claim, err := store.BeginServiceClientIssuance(t.Context(), request, now)
		if err != nil {
			reason := "native issuer lineage, authorization, or request"
			switch {
			case !now.Before(issuer.NotAfter.Add(-pki.ParentMargin)):
				reason = "issuer is within native parent validity margin"
			case issuer.NotAfter.Add(-pki.ParentMargin).Sub(now) < 24*time.Hour:
				reason = "issuer has less than the native 24-hour signing window"
			case csr.Subject.CommonName != client.Subject || len(csr.Subject.Names) != 1 || len(csr.Extensions) != 0 || len(csr.DNSNames)+len(csr.IPAddresses)+len(csr.EmailAddresses)+len(csr.URIs) != 0:
				reason = "client CSR does not match native subject-only policy"
			}
			t.Fatalf("original client claim (%s): %v", reason, err)
		}
		if _, err := store.CompleteServiceClientIssuance(t.Context(), request, claim.ClaimToken, client.Certificate, now); err != nil {
			reason := "native issuer lineage or certificate validation"
			if leaf.NotBefore.Before(now.Add(-5 * time.Minute)) {
				reason = "client certificate predates the native claim by over five minutes"
			}
			t.Fatalf("original client receipt (%s): %v", reason, err)
		}
		pairs[client.Subject] = pair
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	registered, ready := map[string]bool{}, map[string]bool{}
	var mu sync.Mutex
	controller := &pki.HTTPServer{Controller: &pki.Controller{Store: store}, Environment: f.Environment, ServiceClientRootSHA256: pki.Fingerprint(root)}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "identity", 403)
			return
		}
		subject := r.TLS.PeerCertificates[0].Subject.CommonName
		if strings.HasPrefix(r.URL.Path, "/v1/pki/") {
			mu.Lock()
			allowed := registered[subject] && ready[subject]
			mu.Unlock()
			if !allowed {
				http.Error(w, "not ready", 403)
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
		mu.Lock()
		defer mu.Unlock()
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
	caPath := filepath.Join(dir, "controller-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	respond := func(response unifiedNativeACKResponse) {
		b, _ := json.Marshal(response)
		tmp := filepath.Join(dir, "response.tmp")
		if err := os.WriteFile(tmp, b, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "response.json")); err != nil {
			t.Fatal(err)
		}
	}
	respond(unifiedNativeACKResponse{ID: "ready", Result: json.RawMessage(`true`)})
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	seen := ""
	for {
		select {
		case <-deadline.C:
			t.Fatal("isolated IPC deadline exceeded")
		case <-ticker.C:
			raw, err := os.ReadFile(filepath.Join(dir, "request.json"))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			var req unifiedNativeACKRequest
			if json.Unmarshal(raw, &req) != nil || req.ID == "" {
				t.Fatal("invalid IPC request")
			}
			if req.ID == seen {
				continue
			}
			seen = req.ID
			response := unifiedNativeACKResponse{ID: req.ID}
			switch req.Action {
			case "stop":
				respond(response)
				return
			case "select":
				id, err := uuid.Parse(req.Session)
				if err != nil || id.String() != req.Session {
					response.Error = "invalid session"
					break
				}
				var result []byte
				err = db.QueryRowContext(t.Context(), `SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'expires_at',expires_at,'subjects',subjects,'installed_subjects',installed_subjects,'other_active',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment=$1 AND status='active' AND expires_at>now() AND session_id<>$2)) FROM pki_deployment_bootstrap_sessions WHERE environment=$1 AND session_id=$2`, f.Environment, req.Session).Scan(&result)
				if err != nil {
					response.Error = err.Error()
				} else {
					response.Result = result
				}
			case "ack":
				pair, ok := pairs[req.Subject]
				if !ok {
					response.Error = "unknown subject"
					break
				}
				certPath, keyPath := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
				var chain []byte
				for _, der := range pair.Certificate {
					chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
				}
				key, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(certPath, chain, 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PKI_BOOTSTRAP_CONTROLLER_URL", server.URL)
				t.Setenv("PKI_BOOTSTRAP_ISSUER_ID", f.IssuerID)
				t.Setenv("PKI_BOOTSTRAP_SESSION_ID", req.Session)
				httpClient, err := serviceregistration.NewMTLSHTTPClient(certPath, keyPath, caPath)
				if err != nil {
					response.Error = err.Error()
					break
				}
				name := strings.TrimPrefix(req.Subject, "service:")
				client, err := serviceregistration.New(server.URL, httpClient, serviceregistration.Manifest{ServiceID: name, InstanceID: name + "-original", ManifestVersion: "1", EndpointRef: "isolated", Options: []serviceregistration.Option{{Code: "runtime", DisplayName: "runtime"}}})
				if err != nil {
					response.Error = err.Error()
					break
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				_, err = client.Register(ctx)
				if err == nil {
					err = client.Heartbeat(ctx, true)
				}
				cancel()
				if err != nil || !client.Available() {
					response.Error = fmt.Sprintf("mTLS ACK failed: %v", err)
				} else {
					response.Result = json.RawMessage(`true`)
				}
			default:
				response.Error = "unsupported IPC action"
			}
			respond(response)
		}
	}
}
