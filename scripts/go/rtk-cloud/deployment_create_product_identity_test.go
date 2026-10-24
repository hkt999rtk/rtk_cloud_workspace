package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func enableProductIdentityFixture(cfg *deploymentConfig) {
	for _, key := range []string{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "SHADOW_WORKER_REGISTRATION_ENABLED", "OTA_SERVICE_REGISTRATION_ENABLED", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "WEBRTC_SERVICE_REGISTRATION_ENABLED"} {
		cfg.Values[key] = "true"
	}
	cfg.Values["LOGGER_SERVICE_REGISTRATION_ENABLED"] = "false"
}

func TestProductIdentityIntentAddsRealFreshOwnersWithoutChangingHistoricalNine(t *testing.T) {
	cfg, p, _, _ := createServiceRecipeFixture(t, "dev")
	enableProductIdentityFixture(&cfg)
	old, err := environmentCreatePlannedServiceSubjects(cfg, p)
	if err != nil || len(old) != 9 {
		t.Fatal("historical original scope changed", err)
	}
	products, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(products) != 5 {
		t.Fatal(err)
	}
	policy := findEnvironmentCreateDomainPolicy(p, "service")
	policy.ServiceClientIDs = append(slices.Clone(old), "service:deployment-bootstrap")
	for _, product := range products {
		policy.ServiceClientIDs = append(policy.ServiceClientIDs, product.Subject)
	}
	slices.Sort(policy.ServiceClientIDs)
	p.Domains = []environmentCreateIssuerPolicy{policy}
	for i := range products {
		products[i].PolicySHA256 = deploymentplan.Digest(policy)
	}
	p.ProductIdentities = products
	subjects, err := environmentCreatePlannedServiceSubjects(cfg, p)
	if err != nil || len(subjects) != 14 {
		t.Fatal("fresh native scope omitted a real product", err)
	}
	for _, product := range products {
		if product.Claim == "pki-service-bootstrap-state" || !slices.Contains(subjects, product.Subject) {
			t.Fatal("private owner or subject missing")
		}
	}
	p.ProductIdentities[0].Subject = "service:foreign"
	if _, err = environmentCreatePlannedServiceSubjects(cfg, p); err == nil {
		t.Fatal("foreign product accepted")
	}
}

func TestProductIdentityQualificationReadsActualPolicyAndPreservesOriginalRoot(t *testing.T) {
	cfg, p, _, service := createServiceRecipeFixture(t, "dev")
	enableProductIdentityFixture(&cfg)
	stateCfg, _, operator, a := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), stateCfg, a, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	root := deploymentplan.Clone(service.Root.Document)
	ica := deploymentplan.Clone(service.Intermediate.Document)
	core, _ := environmentCreateBootstrapSubjects(cfg)
	ica["service_client_ids"] = append(core, "service:deployment-bootstrap")
	ica["server_dns_names"] = []string{"certissuer.video-cloud-dev-video-cloud.svc"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("qualification wrote native state")
		}
		if strings.HasSuffix(r.URL.Path, service.Root.ID) {
			json.NewEncoder(w).Encode(root)
		} else {
			json.NewEncoder(w).Encode(ica)
		}
	}))
	defer server.Close()
	origin, _ := url.Parse(server.URL)
	client := &environmentCreatePKIClient{Origin: origin, HTTP: server.Client(), Environment: "dev", Execution: e}
	runtime := environmentCreateBootstrapRuntime{Execution: e, PKIClient: client}
	l, err := prepareEnvironmentCreateProductIdentityLifecycle(t.Context(), cfg, p, runtime, service)
	if err != nil || !l.RequiresSuccessor || l.Root.ID != service.Root.ID || l.Root.Certificate != service.Root.Certificate || len(l.Outputs) != 5 {
		t.Fatal("actual missing policy did not produce same-Root successor intent", err)
	}
	for _, o := range l.Outputs {
		if o.IssuerID != "" || o.RootID != service.Root.ID || o.PolicySHA256 != deploymentplan.Digest(l.Policy) {
			t.Fatal("future output was fabricated")
		}
	}
	ica["service_client_ids"] = slices.Clone(l.Policy.ServiceClientIDs)
	l, err = prepareEnvironmentCreateProductIdentityLifecycle(t.Context(), cfg, p, runtime, service)
	if err != nil || l.RequiresSuccessor {
		t.Fatal("actual existing policy was ignored", err)
	}
	root["certificate_fingerprint_sha256"] = strings.Repeat("f", 64)
	if _, err = prepareEnvironmentCreateProductIdentityLifecycle(t.Context(), cfg, p, runtime, service); err == nil {
		t.Fatal("another Root was accepted")
	}
}

func TestProductIdentityCapabilitiesRequireEveryActualBinaryAndSuppressPrivateErrors(t *testing.T) {
	cfg, _, release := completeEnvironmentRenderFixture("dev")
	config := deploymentConfig{Environment: "dev", Values: cfg}
	enableProductIdentityFixture(&config)
	outputs, err := environmentCreateProductIdentityIntent(config)
	if err != nil {
		t.Fatal(err)
	}
	l := &createNormalTailProductLifecycle{Outputs: outputs}
	for _, mode := range []string{"pass", "missing-ack", "wrong-service", "malformed", "external-error"} {
		t.Run(mode, func(t *testing.T) {
			seen := map[string]bool{}
			run := func(_ context.Context, args ...string) ([]byte, error) {
				entry := ""
				for i, a := range args {
					if a == "--entrypoint" {
						entry = args[i+1]
					}
				}
				seen[entry] = true
				if entry == "/bin/sh" {
					return nil, nil
				}
				service := strings.TrimPrefix(entry, "/app/")
				if service == "rtk-account-manager" {
					service = "account-manager"
				}
				features := []string{"deployment-bootstrap-finalize-v1", "first-trust-v1", "create-service-enrollment-v1", "registry-only-issuance-v1", "owned-server-export-v1", "service-registration-bootstrap-ack-v1"}
				if entry == "/app/webrtcservice" {
					switch mode {
					case "missing-ack":
						features = nil
					case "wrong-service":
						service = "foreign"
					case "malformed":
						return []byte("not-json"), nil
					case "external-error":
						return nil, errors.New("private-token-canary")
					}
				}
				return json.Marshal(deploymentplan.Object{"schema": "rtk-cloud.deployment-capabilities/v1", "service": service, "features": features})
			}
			err := verifyProductCapabilities(t.Context(), release, l, run)
			if (err == nil) != (mode == "pass") {
				t.Fatal("incorrect real binary capability decision", err)
			}
			if err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatal("private tool error escaped")
			}
			if mode == "pass" && len(seen) != 10 {
				t.Fatalf("expected all producer/control/product interfaces, got %d", len(seen))
			}
		})
	}
}

func productMaterialFixture(t *testing.T) ([]byte, []byte, environmentCreateProductIdentity, environmentCreateServiceCeremony, managedSessionClient, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	makeKey := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	rootKey, icaKey, clientKey := makeKey(), makeKey(), makeKey()
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "original-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := x509.ParseCertificate(rootDER)
	icaTemplate := *rootTemplate
	icaTemplate.SerialNumber = big.NewInt(2)
	icaTemplate.Subject = pkix.Name{CommonName: "native-successor"}
	icaDER, err := x509.CreateCertificate(rand.Reader, &icaTemplate, root, &icaKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	ica, _ := x509.ParseCertificate(icaDER)
	p := environmentCreateProductIdentity{Subject: "service:shadow", ID: "Secret/video-cloud-dev-video-cloud/shadow-worker-platform-identity", Claim: "pki-product-shadow-identity"}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: p.Subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(30 * time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ica, &clientKey.PublicKey, icaKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	certificate := func(der []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	s := environmentCreateServiceCeremony{Root: environmentCreateRegisteredIssuer{ID: "11111111-1111-4111-8111-111111111111", Certificate: certificate(rootDER), RootFingerprint: environmentPreflightBytesSHA256(rootDER)}, Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222", Certificate: certificate(icaDER)}}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(clientKey)
	chain := certificate(leafDER) + certificate(icaDER) + certificate(rootDER)
	state := deploymentplan.Object{"version": 1, "subject": p.Subject, "current": deploymentplan.Object{"private_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), "certificate_chain_pem": chain, "installed_at": now}}
	raw, _ := json.Marshal(state)
	proof, _ := json.Marshal(deploymentplan.Object{"subject": p.Subject, "fingerprint": environmentPreflightBytesSHA256(leafDER), "public_key_sha256": environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo), "root_sha256": s.Root.RootFingerprint, "state_sha256": environmentPreflightBytesSHA256(raw), "pending": false})
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, clientKey)
	row := managedSessionClient{Subject: p.Subject, Caller: "service:deployment-bootstrap", Request: "original-native-request", Issuer: s.Intermediate.ID, Fingerprint: environmentPreflightBytesSHA256(leafDER), Status: "succeeded", CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})), Certificate: certificate(leafDER)}
	return raw, proof, p, s, row, now
}

func TestProductIdentityMirrorRequiresNativeKeyCSRRegistryAndExactSameRoot(t *testing.T) {
	for _, mode := range []string{"pass", "private-key", "csr", "root", "issuer", "subject", "revoked", "pending", "expiry", "state-hash", "extra-json"} {
		t.Run(mode, func(t *testing.T) {
			raw, proof, p, s, row, now := productMaterialFixture(t)
			before := environmentPreflightBytesSHA256(raw)
			switch mode {
			case "private-key":
				var state deploymentplan.Object
				json.Unmarshal(raw, &state)
				deploymentplan.Map(state["current"])["private_key_pem"] = "replacement"
				raw, _ = json.Marshal(state)
			case "csr":
				row.CSR = "unknown"
			case "root":
				s.Root.RootFingerprint = strings.Repeat("f", 64)
			case "issuer":
				row.Issuer = "33333333-3333-4333-8333-333333333333"
			case "subject":
				row.Subject = "service:foreign"
			case "revoked":
				row.Revoked = &now
			case "pending":
				proof = []byte(strings.Replace(string(proof), `"pending":false`, `"pending":true`, 1))
			case "expiry":
				now = now.Add(2 * time.Hour)
			case "state-hash":
				raw = append(raw, ' ')
			case "extra-json":
				raw = append(raw, []byte("{}")...)
			}
			material, err := productIdentityMaterial(raw, proof, p, s, row, now)
			if (err == nil) != (mode == "pass") {
				t.Fatal("unverified private identity mirrored", err)
			}
			if mode == "pass" && (len(material) != 3 || material["server-ca.crt"] != s.Root.Certificate || environmentPreflightBytesSHA256(raw) != before) {
				t.Fatal("native state changed")
			}
		})
	}
}

func TestProductIdentityRootCustodyChecksExactFilesAndEncryptedKeyPublicIdentity(t *testing.T) {
	for _, mode := range []string{"pass", "missing-key", "key-mode", "self-consistent-wrong-key", "wrong-operator", "wrong-certificate", "private-tool-error"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			_, _, _, s, _, _ := productMaterialFixture(t)
			root := s.Root
			root.Environment, root.Domain, root.Kind = "dev", "service", "root"
			operator := "33333333-3333-4333-8333-333333333333"
			paths := map[string]string{"key": "pki/custody/roots/" + root.ID + "/ca-key.encrypted.pem", "certificate": "pki/custody/roots/" + root.ID + "/certificate.pem", "csr": "pki/custody/roots/" + root.ID + "/csr.pem", "passphrase": "pki/custody/service-root-v1-key-passphrase"}
			files := map[string][]byte{"key": []byte("isolated-encrypted-pkcs8"), "certificate": []byte(root.Certificate), "csr": []byte("isolated-original-csr"), "passphrase": []byte("private-passphrase-canary")}
			hashes := map[string]string{}
			for name, path := range paths {
				if err := ensurePrivateDirectory(filepath.Dir(filepath.Join(store.Root, path))); err != nil {
					t.Fatal(err)
				}
				if err := store.write(path, files[name], false); err != nil {
					t.Fatal(err)
				}
				hashes[name] = environmentPreflightBytesSHA256(files[name])
			}
			if mode == "self-consistent-wrong-key" {
				if err := store.write(paths["key"], []byte("another-encrypted-key"), true); err != nil {
					t.Fatal(err)
				}
				hashes["key"] = environmentPreflightBytesSHA256([]byte("another-encrypted-key"))
			}
			if mode == "wrong-certificate" {
				root.Certificate = "another-certificate"
			}
			manifest := deploymentplan.Object{"schema": "rtk-root-custody/v1", "environment": "dev", "trust_domain": "service", "root_issuer_id": root.ID, "certificate_fingerprint_sha256": root.RootFingerprint, "operator_user_id": operator, "paths": paths, "file_sha256": hashes}
			raw, _ := json.Marshal(manifest)
			if err := store.write("pki/custody/roots/"+root.ID+"/manifest.json", raw, false); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-key" {
				os.Remove(filepath.Join(store.Root, paths["key"]))
			}
			if mode == "key-mode" {
				os.Chmod(filepath.Join(store.Root, paths["key"]), 0644)
			}
			if mode == "wrong-operator" {
				operator = "44444444-4444-4444-8444-444444444444"
			}
			cert, _ := parseFirstCertificatePEM([]byte(s.Root.Certificate))
			runner := func(key, pass string) ([]byte, error) {
				if key != paths["key"] || pass != paths["passphrase"] {
					t.Fatal("arbitrary file authority")
				}
				if mode == "self-consistent-wrong-key" {
					return []byte("another-spki"), nil
				}
				if mode == "private-tool-error" {
					return nil, errors.New("private-passphrase-canary")
				}
				return cert.RawSubjectPublicKeyInfo, nil
			}
			sha, err := qualifyProductRootCustodyWithRunner(store, root, operator, runner)
			if (err == nil) != (mode == "pass") || err == nil && !environmentPreflightDigest.MatchString(sha) {
				t.Fatal("unverified original signing key admitted", err)
			}
			if err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatal("private signer diagnostic escaped")
			}
		})
	}
}

func TestProductIdentityMaintainedRecipesQualifyAllSixPrivateClaimsAndExactNewSession(t *testing.T) {
	cfg, p, runtime, s := createServiceRecipeFixture(t, "dev")
	enableProductIdentityFixture(&cfg)
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workspace = workspace
	products, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	policy := findEnvironmentCreateDomainPolicy(p, "service")
	for _, o := range products {
		policy.ServiceClientIDs = append(policy.ServiceClientIDs, o.Subject)
	}
	p.Domains = []environmentCreateIssuerPolicy{policy}
	path := "deploy/pki/render-staging-controller.py"
	raw, err := os.ReadFile(filepath.Join(cfg.Workspace, "repos/rtk_video_cloud", path))
	if err != nil {
		t.Fatal(err)
	}
	p.Templates = map[string]string{path: environmentPreflightBytesSHA256(raw)}
	l := &createNormalTailProductLifecycle{ParentSHA256: strings.Repeat("a", 64), Root: s.Root, Intermediate: s.Intermediate, Outputs: products, Policy: policy, Bootstrap: p, TransportCAPEM: runtime.TransportCAPEM, StorageClass: "linode-block-storage-retain"}
	scope := productIdentityScope(l)
	s.BootstrapClaim = "pki-product-bootstrap-" + scope[:12]
	s.CustodyReader = "pki-product-custody-" + scope[:12]
	s.BootstrapCredentialNamespaces = []string{s.BootstrapCredentialNamespace}
	claims, err := createNormalTailProductClaimDefinitions(cfg, l)
	if err != nil || len(claims) != 6 {
		t.Fatal("capacity omitted a real stage allocation", err)
	}
	seen := map[string]bool{}
	for _, d := range claims {
		id := deploymentplan.ID(d.Object)
		if seen[id] || d.Object["kind"] != "PersistentVolumeClaim" {
			t.Fatal("claim collision")
		}
		seen[id] = true
	}
	defs, err := productStageDefinitions(t.Context(), cfg, l, s, "service-session")
	if err != nil || len(defs) != 3 {
		t.Fatal(err)
	}
	for _, d := range defs {
		switch d.Object["kind"] {
		case "PersistentVolumeClaim":
			if !seen[deploymentplan.ID(d.Object)] {
				t.Fatal("unqualified native session allocation")
			}
		case "Job":
			pod := environmentPKIPod(d.Object)
			if pod["serviceAccountName"] != "pki-product-bootstrap-"+scope[:12] {
				t.Fatal("borrowed shared bootstrap role")
			}
			container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			var deploymentID string
			for _, raw := range certIssuerObjectList(container["env"]) {
				entry := deploymentplan.Map(raw)
				if entry["name"] == "PKI_BOOTSTRAP_DEPLOYMENT_ID" {
					deploymentID = deploymentplan.String(entry["value"])
				}
			}
			if deploymentID != "product-"+scope[:48] || len(deploymentID) > 63 {
				t.Fatal("native deploymentID contract differs")
			}
		}
	}
	for _, o := range products {
		role := strings.TrimPrefix(o.Subject, "service:")
		defs, err = productStageDefinitions(t.Context(), cfg, l, s, "enroll-"+role)
		if err != nil {
			t.Fatal(err)
		}
		if len(defs) != 2 || !seen[deploymentplan.ID(defs[0].Object)] {
			t.Fatal("owner allocation not qualified")
		}
		input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: scope, ReleaseSHA256: scope, BindingsSHA256: scope}
		compiled, err := deploymentplan.Compile(input, defs, nil)
		if err != nil || len(compiled.Resources) != 2 {
			t.Fatal("actual native recipe does not compile", err)
		}
	}
}

func TestProductIdentityActualACKArrivalOrderAndPartialInstallation(t *testing.T) {
	_, _, _, s, _, now := productMaterialFixture(t)
	s.BootstrapSessionID = "33333333-3333-4333-8333-333333333333"
	policy := []string{"service:mqtt", "service:ota", "service:shadow", "service:video-storage", "service:webrtc"}
	ack := productSessionInstallations{Session: s.BootstrapSessionID, Issuer: s.Intermediate.ID, Root: s.Root.RootFingerprint, Subjects: slices.Clone(policy), Installed: []string{"service:webrtc", "service:ota", "service:shadow", "service:mqtt", "service:video-storage"}, Status: "active", Expires: now.Add(time.Hour)}
	before := deploymentplan.Digest(ack)
	if done, err := productInstallationsComplete(ack, s, policy, now); err != nil || !done || deploymentplan.Digest(ack) != before {
		t.Fatal("real append-order native ACKs rejected or mutated", err)
	}
	ack.Installed = ack.Installed[:2]
	if done, err := productInstallationsComplete(ack, s, policy, now); err != nil || done {
		t.Fatal("partial native install did not wait", err)
	}
	for _, mode := range []string{"duplicate", "foreign", "sealed-partial", "expired", "wrong-session"} {
		t.Run(mode, func(t *testing.T) {
			bad := ack
			bad.Installed = slices.Clone(ack.Installed)
			switch mode {
			case "duplicate":
				bad.Installed = append(bad.Installed, bad.Installed[0])
			case "foreign":
				bad.Installed = append(bad.Installed, "service:foreign")
			case "sealed-partial":
				bad.Status = "sealed"
			case "expired":
				bad.Expires = now
			case "wrong-session":
				bad.Session = "44444444-4444-4444-8444-444444444444"
			}
			if _, err := productInstallationsComplete(bad, s, policy, now); err == nil {
				t.Fatal("unqualified ACK roster accepted")
			}
		})
	}
}
