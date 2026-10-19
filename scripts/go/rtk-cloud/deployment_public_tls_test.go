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
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type publicTLSFakeDNS struct {
	values           map[string][]string
	present, cleanup int
	failCleanup      bool
}

func (d *publicTLSFakeDNS) Name() string                                      { return "godaddy" }
func (d *publicTLSFakeDNS) Validate(context.Context, dnsAdapterContext) error { return nil }
func (d *publicTLSFakeDNS) DiscoverZone(_ context.Context, c dnsAdapterContext) (dnsZone, error) {
	return dnsZone{Name: c.RootDomain}, nil
}
func (d *publicTLSFakeDNS) GetRecordSet(_ context.Context, _ dnsAdapterContext, _ dnsZone, name, kind string) (dnsRecordSet, error) {
	return dnsRecordSet{Name: name, Type: kind, Values: d.values[strings.TrimPrefix(name, "_acme-challenge.")]}, nil
}
func (d *publicTLSFakeDNS) UpsertRecordSet(context.Context, dnsAdapterContext, dnsZone, dnsRecordSet) error {
	return errors.New("not part of this protocol")
}
func (d *publicTLSFakeDNS) DeleteRecordValues(context.Context, dnsAdapterContext, dnsZone, dnsRecordSet) error {
	return errors.New("not part of this protocol")
}
func (d *publicTLSFakeDNS) PresentDNS01Challenge(_ context.Context, _ dnsAdapterContext, _ dnsZone, domain, value string) error {
	d.present++
	d.values[domain] = mergeDNSValues(d.values[domain], value)
	return nil
}
func (d *publicTLSFakeDNS) CleanupDNS01Challenge(_ context.Context, _ dnsAdapterContext, _ dnsZone, domain, value string) error {
	if d.failCleanup {
		d.failCleanup = false
		return errors.New("interrupted cleanup")
	}
	d.cleanup++
	var keep []string
	for _, v := range d.values[domain] {
		if v != value {
			keep = append(keep, v)
		}
	}
	d.values[domain] = keep
	return nil
}
func (d *publicTLSFakeDNS) CollectEvidence(context.Context, dnsAdapterContext, dnsZone) error {
	return nil
}

// Real ACME HTTP/JWS boundaries, with a local CA signing the actual frozen CSR.
// No producer result or certificate is substituted by a business callback.
type publicTLSACMEServer struct {
	t                           *testing.T
	server                      *httptest.Server
	dns                         *publicTLSFakeDNS
	hosts                       []string
	status                      string
	authorized                  map[int]bool
	accounts, orders, finalizes int
	csr                         []byte
	certificate                 []byte
	failFinalize, failOrder     bool
	failValidation              bool
	oldOrders                   map[int]map[string]any
	afterAccept                 func(int)
	accepts                     map[int]int
	drift                       bool
}

func newPublicTLSACMEServer(t *testing.T, dns *publicTLSFakeDNS, hosts []string) *publicTLSACMEServer {
	s := &publicTLSACMEServer{t: t, dns: dns, hosts: hosts, status: "pending", authorized: map[int]bool{}}
	s.server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}
func (s *publicTLSACMEServer) order() map[string]any {
	ids := []map[string]string{}
	auth := []string{}
	for i, h := range s.hosts {
		ids = append(ids, map[string]string{"type": "dns", "value": h})
		endpoint := fmt.Sprintf("%s/auth/%d", s.server.URL, i)
		if s.orders > 1 {
			endpoint += fmt.Sprintf("?attempt=%d", s.orders)
		}
		auth = append(auth, endpoint)
	}
	if s.drift {
		ids[0]["value"] = "foreign.example.test"
	}
	n := max(1, s.orders)
	return map[string]any{"status": s.status, "identifiers": ids, "authorizations": auth, "finalize": fmt.Sprintf("%s/finalize/%d", s.server.URL, n), "certificate": fmt.Sprintf("%s/certificate/%d", s.server.URL, n), "expires": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}
}
func (s *publicTLSACMEServer) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Replay-Nonce", fmt.Sprint(time.Now().UnixNano()))
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/nonce" {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Path == "/directory" {
		_ = json.NewEncoder(w).Encode(map[string]string{"newNonce": s.server.URL + "/nonce", "newAccount": s.server.URL + "/account", "newOrder": s.server.URL + "/order"})
		return
	}
	var envelope struct{ Protected, Payload, Signature string }
	if json.NewDecoder(r.Body).Decode(&envelope) != nil || envelope.Signature == "" {
		s.t.Error("ACME request lacks JWS")
		w.WriteHeader(400)
		return
	}
	var payload map[string]any
	raw, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		s.t.Error(err)
		return
	}
	if len(raw) > 0 {
		if json.Unmarshal(raw, &payload) != nil {
			s.t.Error("bad JWS payload")
			return
		}
	}
	switch {
	case r.URL.Path == "/account":
		s.accounts++
		w.Header().Set("Location", s.server.URL+"/account/1")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "valid", "orders": s.server.URL + "/orders"})
	case r.URL.Path == "/order":
		if s.orders > 0 {
			if s.oldOrders == nil {
				s.oldOrders = map[int]map[string]any{}
			}
			s.oldOrders[s.orders] = s.order()
			s.status, s.authorized = "pending", map[int]bool{}
		}
		s.orders++
		var names []string
		for _, v := range payload["identifiers"].([]any) {
			names = append(names, deploymentplan.String(deploymentplan.Map(v)["value"]))
		}
		if !slices.Equal(names, s.hosts) {
			s.t.Error("order names differ from frozen catalog")
		}
		if s.failOrder {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"type":"urn:ietf:params:acme:error:malformed","detail":"unknown order"}`)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("%s/order/%d", s.server.URL, s.orders))
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(s.order())
	case strings.HasPrefix(r.URL.Path, "/order/"):
		var n int
		fmt.Sscanf(r.URL.Path, "/order/%d", &n)
		if n < s.orders {
			_ = json.NewEncoder(w).Encode(s.oldOrders[n])
			return
		}
		_ = json.NewEncoder(w).Encode(s.order())
	case strings.HasPrefix(r.URL.Path, "/auth/"):
		var i int
		_, _ = fmt.Sscanf(r.URL.Path, "/auth/%d", &i)
		status := "pending"
		if s.authorized[i] {
			status = "valid"
		}
		endpoint := fmt.Sprintf("%s/challenge/%d", s.server.URL, i)
		token := fmt.Sprint("token-", i)
		if s.orders > 1 {
			endpoint += fmt.Sprintf("?attempt=%d", s.orders)
			token += fmt.Sprintf("-attempt-%d", s.orders)
		}
		challenge := map[string]any{"type": "dns-01", "url": endpoint, "token": token}
		if s.status == "invalid" && i == 0 {
			status = "invalid"
			challenge["error"] = map[string]any{"type": "urn:ietf:params:acme:error:dns", "detail": "DNS problem: NXDOMAIN; private-challenge-canary", "status": 400}
		}
		challenge["status"] = status
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "identifier": map[string]string{"type": "dns", "value": s.hosts[i]}, "challenges": []any{challenge}})
	case strings.HasPrefix(r.URL.Path, "/challenge/"):
		var i int
		_, _ = fmt.Sscanf(r.URL.Path, "/challenge/%d", &i)
		if s.accepts == nil {
			s.accepts = map[int]int{}
		}
		s.accepts[i]++
		for _, h := range s.hosts {
			if len(s.dns.values[h]) < 2 {
				s.t.Error("challenge accepted before complete genuine TXT batch publication")
			}
		}
		if s.failValidation {
			s.failValidation = false
			s.status = "invalid"
			_ = json.NewEncoder(w).Encode(map[string]string{"type": "dns-01", "url": s.server.URL + r.URL.Path, "token": fmt.Sprint("token-", i), "status": "invalid"})
			return
		}
		s.authorized[i] = true
		if len(s.authorized) == len(s.hosts) && s.status != "invalid" {
			s.status = "ready"
		}
		if s.afterAccept != nil {
			s.afterAccept(i)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"type": "dns-01", "url": s.server.URL + r.URL.Path, "token": fmt.Sprint("token-", i), "status": "valid"})
	case strings.HasPrefix(r.URL.Path, "/finalize/"):
		s.finalizes++
		s.csr, err = base64.RawURLEncoding.DecodeString(deploymentplan.String(payload["csr"]))
		if err != nil {
			s.t.Error(err)
			return
		}
		csr, err := x509.ParseCertificateRequest(s.csr)
		if err != nil || csr.CheckSignature() != nil || !slices.Equal(csr.DNSNames, s.hosts) {
			s.t.Error("finalizer did not receive original signed CSR")
			return
		}
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		ca := &x509.Certificate{SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "isolated test public CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour)}
		caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(11), DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, csr.PublicKey, key)
		if err != nil {
			s.t.Error(err)
			return
		}
		s.certificate = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
		s.status = "valid"
		if s.failFinalize {
			s.failFinalize = false
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"type":"urn:ietf:params:acme:error:malformed","detail":"response lost after issuance"}`)
			return
		}
		w.Header().Set("Location", fmt.Sprintf("%s/order/%d", s.server.URL, s.orders))
		_ = json.NewEncoder(w).Encode(s.order())
	case strings.HasPrefix(r.URL.Path, "/certificate/"):
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(s.certificate)
	default:
		s.t.Errorf("unexpected ACME path %s", r.URL.Path)
		w.WriteHeader(404)
	}
}

func publicTLSProtocolFixture(t *testing.T) (deploymentConfig, secretStore, *environmentCreateExecution, []deploymentplan.Definition, *environmentPublicTLSPlan, environmentPublicTLSOps, *publicTLSACMEServer, *normalTailE2EBackend, func(context.Context, deploymentplan.Definition) (string, error)) {
	t.Helper()
	cfg, store, operator, a := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, a, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	cfg.DNSAdapter = "godaddy"
	cfg.Values["CLOUD_DNS_ROOT_DOMAIN"] = "example.test"
	hosts := []string{"api.example.test", "frontend.example.test"}
	dns := &publicTLSFakeDNS{values: map[string][]string{hosts[0]: {"unrelated-txt"}, hosts[1]: {"unrelated-txt"}}}
	server := newPublicTLSACMEServer(t, dns, hosts)
	if err = store.write("operator/env/LKE_PUBLIC_HTTPS_ACME_SERVER", []byte(server.server.URL+"/directory"), false); err != nil {
		t.Fatal(err)
	}
	defs, err := decodeEnvironmentCreateManifests(lkePublicHTTPSIngressManifest(cfg.Values, "public", []lkePublicHTTPSRoute{{Host: hosts[0], Namespace: "video-cloud-dev-video-cloud", Service: "api", ServicePort: 80}, {Host: hosts[1], Namespace: "video-cloud-dev-frontend", Service: "frontend", ServicePort: 80}}, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	bindings := environmentIdentityBindings{Environment: "dev", Stack: e.Journal.Stack, Owners: map[string]string{}}
	p, err := prepareEnvironmentPublicTLS(t.Context(), cfg, store, defs, nil, bindings)
	if err != nil {
		t.Fatal(err)
	}
	ops := environmentPublicTLSOps{HTTP: server.server.Client(), DNS: func(deploymentConfig, map[string]string) (dnsAdapter, dnsAdapterContext, error) {
		return dns, dnsAdapterContext{RootDomain: p.RootDomain, Values: map[string]string{"CLOUD_ENV_NAME": "dev"}}, nil
	}, WaitTXT: func(ctx context.Context, _ string, value string, _ map[string]string) error {
		if !slices.Contains(dns.values[hosts[0]], value) && !slices.Contains(dns.values[hosts[1]], value) {
			return errors.New("TXT absent")
		}
		return ctx.Err()
	}}
	b := &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"public-tls": deploymentplan.Digest(p)}}
	var original *deploymentplan.Plan
	var originalInput string
	apply := func(ctx context.Context, d deploymentplan.Definition) (string, error) {
		input := deploymentplan.Digest(d)
		if original == nil {
			originalInput = input
			original, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("formal-release"), BindingsSHA256: environmentCreateStageBindingsSHA("public-tls", []deploymentplan.Definition{d}), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}, []deploymentplan.Definition{d}, nil)
			if err != nil {
				return "", err
			}
		} else if input != originalInput {
			return "", errors.New("publication definition changed after original child freeze")
		}
		err = executeCreateNormalTailStageBackend(ctx, store, h, "public-tls", original, b, func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, store.Root) }, func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }, io.Discard, func(context.Context) error { return nil })
		if err != nil {
			return "", err
		}
		return deploymentplan.String(deploymentplan.Map(b.objects[p.SecretID]["metadata"])["uid"]), nil
	}
	return cfg, store, e, defs, p, ops, server, b, apply
}

func TestEnvironmentPublicTLSProtocolIssuesAndResumesOriginalChild(t *testing.T) {
	for _, failure := range []string{"none", "finalize-response", "cleanup", "child-response", "after-published-before-completion"} {
		t.Run(failure, func(t *testing.T) {
			cfg, store, e, defs, p, ops, server, b, apply := publicTLSProtocolFixture(t)
			before := deploymentplan.Digest(e.Journal)
			if err := checkEnvironmentPublicTLSPrerequisitesWithOps(t.Context(), cfg, store, p, ops); err != nil {
				t.Fatal(err)
			}
			if server.accounts != 0 || server.orders != 0 || deploymentplan.Digest(e.Journal) != before {
				t.Fatal("qualification generated identity or mutation intent")
			}
			switch failure {
			case "finalize-response":
				server.failFinalize = true
			case "cleanup":
				server.dns.failCleanup = true
			case "child-response":
				b.failure = "unknown-write"
			}
			publish := apply
			if failure == "after-published-before-completion" {
				lost := true
				publish = func(ctx context.Context, d deploymentplan.Definition) (string, error) {
					uid, err := apply(ctx, d)
					if err == nil && lost {
						lost = false
						return "", errors.New("original child response lost")
					}
					return uid, err
				}
			}
			err := executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, publish, ops)
			if failure != "none" && err == nil {
				t.Fatal("injected interruption was ignored")
			}
			if failure == "child-response" { // uncertain API write remains closed; no secret recreation
				if executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, publish, ops) == nil || b.writes[p.SecretID] != 1 {
					t.Fatal("unknown child write was replayed or falsely accepted")
				}
				return
			}
			if err != nil {
				if err = executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, publish, ops); err != nil {
					t.Fatal("original order/child could not resume", err)
				}
			}
			if err = executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, publish, ops); err != nil {
				t.Fatal("completed original child was not reusable", err)
			}
			if server.orders != 1 || server.finalizes != 1 || b.writes[p.SecretID] != 1 {
				t.Fatalf("native identity or Secret recreated orders=%d finalize=%d writes=%d", server.orders, server.finalizes, b.writes[p.SecretID])
			}
			resolved, err := resolveEnvironmentPublicTLSDefinitions(store, p, defs)
			if err != nil {
				t.Fatal(err)
			}
			last := resolved[len(resolved)-1]
			if deploymentplan.ID(last.Object) != p.SecretID || !last.Retain || deploymentplan.Map(last.Object["metadata"])["uid"] == "" {
				t.Fatal("resolved target lacks genuine retained owner")
			}
			for _, h := range p.Hosts {
				if !slices.Equal(server.dns.values[h], []string{"unrelated-txt"}) {
					t.Fatal("DNS cleanup deleted unrelated TXT or leaked owned value")
				}
			}
			for _, name := range []string{"keys.json", "account.json", "order.json", "material.json", "completed.json"} {
				path, _ := store.safePath(filepath.Join(publicTLSBase(p), name))
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("canonical identity state is not private", name, err)
				}
			}
			// Recorded completion never grants a foreign live Secret owner.
			deploymentplan.Map(b.objects[p.SecretID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			if executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, publish, ops) == nil {
				t.Fatal("completed publication accepted a replacement Secret")
			}
		})
	}
}

func TestEnvironmentPublicTLSUnknownOrderAndCanceledChallengesStayOriginal(t *testing.T) {
	for _, mode := range []string{"unknown-order", "canceled-challenge", "order-drift", "partial-cache"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e, _, p, ops, server, _, apply := publicTLSProtocolFixture(t)
			switch mode {
			case "unknown-order":
				server.failOrder = true
			case "canceled-challenge":
				ops.WaitTXT = func(context.Context, string, string, map[string]string) error { return context.Canceled }
			case "order-drift":
				server.drift = true
			case "partial-cache":
				if err := publicTLSWrite(store, filepath.Join("pki", "public-https", "video-cloud-dev-public-tls", "fullchain.pem"), map[string]string{"invalid": "partial"}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := readEnvironmentPublicTLSLegacyCache(store, p); err == nil {
					t.Fatal("partial original pair was ignored")
				}
				return
			}
			if executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops) == nil {
				t.Fatal("unknown or invalid producer outcome passed")
			}
			var keys environmentPublicTLSKeys
			if err := publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "keys.json"), &keys); err != nil {
				t.Fatal(err)
			}
			original := deploymentplan.Digest(keys)
			server.failOrder = false
			server.drift = false
			if mode == "unknown-order" {
				if executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops) == nil || server.orders != 1 {
					t.Fatal("unknown order response caused a new ACME order")
				}
			}
			if mode == "canceled-challenge" {
				ops.WaitTXT = func(context.Context, string, string, map[string]string) error { return nil }
				if err := executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops); err != nil {
					t.Fatal(err)
				}
				if server.orders != 1 {
					t.Fatal("cancellation replaced original order")
				}
			}
			var after environmentPublicTLSKeys
			if publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "keys.json"), &after) != nil || deploymentplan.Digest(after) != original {
				t.Fatal("an interrupted request regenerated its original account/key/CSR")
			}
		})
	}
}

func TestEnvironmentPublicTLSSelectedCatalogAndExistingOwnership(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, env)
			cfg := deploymentConfig{Environment: env, DNSAdapter: "godaddy", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + env, "CLOUD_DNS_ROOT_DOMAIN": "example.test"}}
			t.Setenv("LKE_PUBLIC_HTTPS_TLS_SECRET", "ambient-foreign-secret")
			defs, err := decodeEnvironmentCreateManifests(lkePublicHTTPSIngressManifest(cfg.Values, "public", []lkePublicHTTPSRoute{{Host: "public.example.test", Namespace: cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", Service: "api", ServicePort: 80}}, "", ""))
			if err != nil {
				t.Fatal(err)
			}
			passthrough := deploymentplan.Clone(defs[0].Object)
			deploymentplan.Map(passthrough["metadata"])["name"] = "certissuer"
			deploymentplan.Map(passthrough["metadata"])["annotations"] = deploymentplan.Object{"nginx.ingress.kubernetes.io/ssl-passthrough": "true"}
			deploymentplan.Map(passthrough["spec"])["tls"] = []any{deploymentplan.Object{"secretName": "native-certificate-must-remain", "hosts": []any{"issuer.example.test"}}}
			defs = append(defs, deploymentplan.Definition{Object: passthrough})
			bindings := environmentIdentityBindings{Environment: env, Stack: cfg.Values["CLOUD_STACK_NAME"], Owners: map[string]string{}}
			p, err := prepareEnvironmentPublicTLS(t.Context(), cfg, store, defs, nil, bindings)
			if err != nil {
				t.Fatal(err)
			}
			if p.SecretID != "Secret/"+cfg.Values["CLOUD_STACK_NAME"]+"-ingress/"+cfg.Values["CLOUD_STACK_NAME"]+"-public-tls" || len(p.Hosts) != 1 {
				t.Fatal("ambient/staging/native identity leaked into selected public contract")
			}
			outputs, err := environmentPublicTLSOutputContract(p)
			if err != nil || len(outputs) != 1 {
				t.Fatal(err)
			}
			if _, err = bindEnvironmentPublicTLSReferences(p, defs); err != nil {
				t.Fatal(err)
			}
			bad := *p
			bad.Hosts = append(slices.Clone(p.Hosts), "foreign.invalid")
			if _, err = environmentPublicTLSOutputContract(&bad); err == nil {
				t.Fatal("foreign DNS zone admitted")
			}
			bad = *p
			bad.OwnerUID = "11111111-1111-4111-8111-111111111111"
			if _, err = environmentPublicTLSOutputContract(&bad); err == nil {
				t.Fatal("missing recorded keypair treated as new identity authority")
			}
			if p, err = prepareEnvironmentPublicTLS(t.Context(), cfg, store, defs[1:], nil, bindings); err != nil || p != nil {
				t.Fatal("native passthrough incorrectly requires WebPKI producer", err)
			}
		})
	}
}

func TestEnvironmentPublicTLSRetainsOwnedNameAndAuthenticatesExistingPair(t *testing.T) {
	cfg, store, e, defs, p, ops, server, backend, apply := publicTLSProtocolFixture(t)
	if err := executeEnvironmentPublicTLSWithOps(t.Context(), cfg, store, e, p, apply, ops); err != nil {
		t.Fatal(err)
	}
	var material environmentPublicTLSMaterial
	if err := publicTLSReadJSON(store, filepath.Join(publicTLSBase(p), "material.json"), &material); err != nil {
		t.Fatal(err)
	}
	legacy := "video-cloud-staging-public-tls"
	ingress := deploymentplan.Clone(defs[0].Object)
	deploymentplan.Map(ingress["metadata"])["uid"] = "12345678-1234-4234-8234-123456789abc"
	rows, _ := deploymentplan.Map(ingress["spec"])["tls"].([]any)
	for _, row := range rows {
		deploymentplan.Map(row)["secretName"] = legacy
	}
	secret := deploymentplan.Clone(backend.objects[p.SecretID])
	deploymentplan.Map(secret["metadata"])["name"] = legacy
	deploymentplan.Map(deploymentplan.Map(secret["metadata"])["labels"])["app.kubernetes.io/name"] = legacy
	bindings := environmentIdentityBindings{Environment: cfg.Environment, Stack: p.Stack, Owners: map[string]string{deploymentplan.ID(ingress): deploymentplan.String(deploymentplan.Map(ingress["metadata"])["uid"]), deploymentplan.ID(secret): deploymentplan.String(deploymentplan.Map(secret["metadata"])["uid"])}}
	for _, mode := range []string{"valid", "foreign-secret-uid", "missing-recorded", "unrecorded", "key-mismatch", "wrong-san", "expired", "unsafe-cache"} {
		t.Run(mode, func(t *testing.T) {
			catalog := []deploymentplan.Definition{{Object: deploymentplan.Clone(defs[0].Object)}}
			actual := []deploymentplan.Object{deploymentplan.Clone(ingress), deploymentplan.Clone(secret)}
			owners := appendMap(bindings.Owners, nil)
			switch mode {
			case "foreign-secret-uid":
				owners[deploymentplan.ID(secret)] = "99999999-9999-4999-8999-999999999999"
			case "missing-recorded":
				actual = actual[:1]
			case "unrecorded":
				delete(owners, deploymentplan.ID(secret))
			case "key-mismatch":
				deploymentplan.Map(actual[1]["data"])["tls.key"] = base64.StdEncoding.EncodeToString([]byte("unrelated key"))
			case "wrong-san":
				catalog[0].Object = deploymentplan.Clone(catalog[0].Object)
				tlsRows, _ := deploymentplan.Map(catalog[0].Object["spec"])["tls"].([]any)
				deploymentplan.Map(tlsRows[0])["hosts"] = []any{"different.example.test"}
			case "expired":
				if validateEnvironmentPublicTLSMaterial(p, material, time.Now().Add(100*24*time.Hour)) == nil {
					t.Fatal("expired original material passed")
				}
				return
			case "unsafe-cache":
				base := filepath.Join("pki", "public-https", "unsafe-public-tls")
				path, _ := store.safePath(base)
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(store.Root, "unavailable-key"), filepath.Join(path, "privkey.pem")); err != nil {
					t.Fatal(err)
				}
				copy := *p
				copy.SecretID = "Secret/" + p.Stack + "-ingress/unsafe-public-tls"
				if _, _, err := readEnvironmentPublicTLSLegacyCache(store, &copy); err == nil {
					t.Fatal("symlinked original pair passed")
				}
				return
			}
			selected, err := prepareEnvironmentPublicTLS(t.Context(), cfg, store, catalog, actual, environmentIdentityBindings{Environment: cfg.Environment, Stack: p.Stack, Owners: owners})
			if (err == nil) != (mode == "valid") {
				t.Fatalf("wrong original material disposition: %v", err)
			}
			if mode != "valid" {
				return
			}
			if selected.SecretID != deploymentplan.ID(secret) || selected.OwnerUID != bindings.Owners[deploymentplan.ID(secret)] || selected.Source == nil || deploymentplan.Digest(selected.Source) != deploymentplan.Digest(material) {
				t.Fatal("existing UID/name/key material changed")
			}
			consumers, _, secretID, err := environmentPublicTLSConsumers(catalog)
			if err != nil || secretID != selected.SecretID || deploymentplan.Digest(consumers) != deploymentplan.Digest(selected.Consumers) {
				t.Fatal("qualified catalog did not retain original TLS reference", err)
			}
			if err = checkEnvironmentPublicTLSPrerequisitesWithOps(t.Context(), cfg, store, selected, environmentPublicTLSOps{}); err != nil {
				t.Fatal("retained native material unnecessarily contacted DNS/ACME", err)
			}
		})
	}
	if server.orders != 1 {
		t.Fatal("read-only existing-material qualification created another order")
	}
}
