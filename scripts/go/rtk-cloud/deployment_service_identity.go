package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// References are relative to this environment's SecretStore, never another env.
type deploymentServiceIssuer struct {
	Environment              string `json:"environment"`
	Stack                    string `json:"stack"`
	Endpoint                 string `json:"endpoint"`
	ServerName               string `json:"server_name"`
	ServerCAFile             string `json:"server_ca_file"`
	RegistrationServerCAFile string `json:"registration_server_ca_file"`
	RootCAFile               string `json:"root_ca_file"`
	RootSHA256               string `json:"root_sha256"`
	BootstrapCertFile        string `json:"bootstrap_cert_file"`
	BootstrapKeyFile         string `json:"bootstrap_key_file"`
}

type deploymentServiceIdentity struct {
	Version          int    `json:"version"`
	Environment      string `json:"environment"`
	Stack            string `json:"stack"`
	Subject          string `json:"subject"`
	RootSHA256       string `json:"root_sha256"`
	RequestID        string `json:"request_id"`
	CSR              string `json:"csr_pem,omitempty"`
	PrivateKey       string `json:"private_key_pem"`
	CertificateChain string `json:"certificate_chain_pem,omitempty"`
	ServerCA         string `json:"server_ca_pem,omitempty"`
	Fingerprint      string `json:"certificate_sha256,omitempty"`
	Source           string `json:"source"`
}

func deploymentServiceSubject(subject string) bool {
	switch subject {
	case "service:account-manager", "service:certissuer", "service:pki-controller", "service:factory-enroll", "service:video-cloud-api", "service:mqtt", "service:shadow", "service:webrtc", "service:video-storage", "service:ota", "service:logger", "service:video-cloud-logingester", "service:emqx-pki", "service:pkibroker", "service:openbao":
		return true
	}
	return false
}

func deploymentIdentityStore(env map[string]string) (secretStore, error) {
	name := env["CLOUD_ENV_NAME"]
	if activeSecretEnvironmentRoot == "" || !secretEnvironmentPattern.MatchString(name) || env["CLOUD_STACK_NAME"] == "" || filepath.Base(activeSecretEnvironmentRoot) != name {
		return secretStore{}, errors.New("deployment identity requires the selected environment SecretStore and stack")
	}
	return secretStore{Root: activeSecretEnvironmentRoot, ConfigRoot: filepath.Dir(activeSecretEnvironmentRoot), Environment: name}, nil
}

func readDeploymentServiceIssuer(store secretStore, stack string) (deploymentServiceIssuer, string, error) {
	var cfg deploymentServiceIssuer
	raw, err := store.read("pki/services/issuer.json")
	if err != nil {
		return cfg, "", fmt.Errorf("configure pki/services/issuer.json using this environment's recorded Service bootstrap session: %w", err)
	}
	if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, "", errors.New("invalid deployment issuer configuration JSON")
	}
	endpoint, err := url.Parse(cfg.Endpoint)
	if cfg.Environment != store.Environment || cfg.Stack != stack || err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || cfg.ServerName == "" {
		return cfg, "", errors.New("deployment issuer environment, stack or HTTPS endpoint is invalid")
	}
	root, err := store.read(cfg.RootCAFile)
	if err != nil {
		return cfg, "", err
	}
	certs, err := pemCertificates([]byte(root))
	if err != nil || len(certs) != 1 || !certs[0].IsCA || certs[0].CheckSignatureFrom(certs[0]) != nil {
		return cfg, "", errors.New("deployment Service root must be one self-signed CA")
	}
	if certificateSHA256(certs[0]) != cfg.RootSHA256 {
		return cfg, "", errors.New("deployment Service root fingerprint does not match configured pin")
	}
	if _, err := certificateRoots([]byte(root), time.Now()); err != nil {
		return cfg, "", err
	}
	return cfg, root, nil
}

func certificateSHA256(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Stable even if a local pending file is lost: the registry will conflict on a
// changed CSR instead of signing a second initial identity. Recovery restores it.
func initialServiceRequestID(environment, stack, subject, root string) string {
	sum := sha256.Sum256([]byte("deployment-initial-v1\x00" + environment + "\x00" + stack + "\x00" + subject + "\x00" + root))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func writeDeploymentServiceIdentity(store secretStore, path string, record deploymentServiceIdentity) error {
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err = store.write(path, append(raw, '\n'), true); err != nil {
		return err
	}
	// Persist the rename before any consumer installation/network side effect.
	dir, err := os.Open(filepath.Join(store.Root, filepath.Dir(path)))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validateDeploymentServiceIdentity(record deploymentServiceIdentity, root string) error {
	if err := validateDeploymentTLS(record.CertificateChain, record.PrivateKey, root, []string{record.Subject}, nil, x509.ExtKeyUsageClientAuth); err != nil {
		return err
	}
	chain, err := pemCertificates([]byte(record.CertificateChain))
	if err != nil {
		return err
	}
	if record.Fingerprint != "" && certificateSHA256(chain[0]) != record.Fingerprint {
		return errors.New("saved identity fingerprint mismatch")
	}
	if record.Source == "enrolled" {
		if len(chain) != 3 || certificateSHA256(chain[2]) != record.RootSHA256 || chain[0].CheckSignatureFrom(chain[1]) != nil || chain[1].CheckSignatureFrom(chain[2]) != nil {
			return errors.New("issued identity must include the pinned Service Root and intermediate")
		}
		key, ok := chain[0].PublicKey.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P256() || len(chain[0].UnknownExtKeyUsage) != 0 || chain[0].NotAfter.Sub(chain[0].NotBefore) > 30*24*time.Hour+5*time.Minute {
			return errors.New("issued identity violates the Service client profile")
		}
	}

	return nil
}

// Initial enrollment never renews, changes an existing key, or writes runtime
// owner state. The caller holds the per-service lock until installation finishes.
func ensureDeploymentServiceIdentity(store secretStore, stack, subject string, existing map[string]any, listener map[string]any, dns string) (deploymentServiceIdentity, error) {
	var record deploymentServiceIdentity
	if !deploymentServiceSubject(subject) {
		return record, errors.New("unrecognized deployment service subject")
	}
	cfg, root, err := readDeploymentServiceIssuer(store, stack)
	if err != nil {
		return record, err
	}
	if listener != nil {
		ca, err := store.read(cfg.RegistrationServerCAFile)
		if err != nil {
			return record, err
		}
		if _, err := certificateRoots([]byte(ca), time.Now()); err != nil {
			return record, err
		}
	}
	path := "pki/services/" + strings.TrimPrefix(subject, "service:") + "/identity.json"
	raw, err := store.read(path)
	if err == nil {
		if json.Unmarshal([]byte(raw), &record) != nil {
			return record, errors.New("invalid saved deployment identity JSON")
		}
		if record.Version != 1 || record.Environment != store.Environment || record.Stack != stack || record.Subject != subject || record.RootSHA256 != cfg.RootSHA256 || (record.Source != "enrolled" && record.Source != "adopted") {
			return record, errors.New("saved deployment identity scope/pin differs from selected environment; reconciliation required")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return record, err
	} else {
		record = deploymentServiceIdentity{Version: 1, Environment: store.Environment, Stack: stack, Subject: subject, RootSHA256: cfg.RootSHA256, Source: "enrolled", RequestID: initialServiceRequestID(store.Environment, stack, subject, cfg.RootSHA256)}
		if existing != nil {
			if listener == nil {
				return record, errors.New("adoption requires independent listener trust and CRL")
			}
			if err := validatePlatformServiceIdentityMaterial(existing, listener, subject, dns, time.Now()); err != nil {
				return record, err
			}
			cert, _ := kubernetesSecretBytes(existing, "client.crt")
			key, _ := kubernetesSecretBytes(existing, "client.key")
			serverCA, _ := kubernetesSecretBytes(existing, "server-ca.crt")
			record.CertificateChain, record.PrivateKey, record.ServerCA, record.Source = string(cert), string(key), string(serverCA), "adopted"
		} else {
			signer, key, err := newLKECertificatePrivateKey("p256")
			if err != nil {
				return record, err
			}
			der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, signer)
			if err != nil {
				return record, err
			}
			record.PrivateKey, record.CSR = key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
			if err := writeDeploymentServiceIdentity(store, path, record); err != nil {
				return record, err
			}
		}
	}
	if record.CertificateChain == "" {
		if existing != nil {
			return record, errors.New("pending local enrollment and installed identity coexist; reconcile before deployment")
		}
		if record.Source != "enrolled" || record.RequestID != initialServiceRequestID(store.Environment, stack, subject, cfg.RootSHA256) {
			return record, errors.New("invalid pending initial identity")
		}
		// Prove the saved CSR/key pair before resubmission; never regenerate on error.
		block, _ := pem.Decode([]byte(record.CSR))
		if block == nil {
			return record, errors.New("pending CSR is missing")
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != subject {
			return record, errors.New("pending CSR is invalid")
		}
		keyBlock, _ := pem.Decode([]byte(record.PrivateKey))
		if keyBlock == nil {
			return record, errors.New("pending key is missing")
		}
		key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return record, errors.New("pending key is invalid")
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return record, errors.New("pending key is not a signing key")
		}
		pub, err := x509.MarshalPKIXPublicKey(signer.Public())
		if err != nil || !bytes.Equal(pub, csr.RawSubjectPublicKeyInfo) {
			return record, errors.New("pending CSR and key do not match")
		}
		chain, err := issueDeploymentServiceIdentity(store, cfg, record)
		if err != nil {
			return record, err
		}
		record.CertificateChain = chain
	}
	if err := validateDeploymentServiceIdentity(record, root); err != nil {
		return record, fmt.Errorf("deployment identity validation failed: %w", err)
	}
	chain, _ := pemCertificates([]byte(record.CertificateChain))
	record.Fingerprint = certificateSHA256(chain[0])
	if record.ServerCA == "" {
		if listener != nil {
			record.ServerCA, err = store.read(cfg.RegistrationServerCAFile)
			if err != nil {
				return record, err
			}
		} else {
			record.ServerCA = root
		}
	}
	if listener != nil {
		if err := validatePlatformServiceIdentityMaterial(record.secret(), listener, subject, dns, time.Now()); err != nil {
			return record, err
		}
	}
	// Completed records are reused byte-for-byte; do not require live bootstrap credentials.
	if raw != "" {
		var saved deploymentServiceIdentity
		_ = json.Unmarshal([]byte(raw), &saved)
		if saved.CertificateChain != "" {
			return record, nil
		}
	}
	if err := writeDeploymentServiceIdentity(store, path, record); err != nil {
		return record, err
	}
	return record, nil
}

func issueDeploymentServiceIdentity(store secretStore, cfg deploymentServiceIssuer, record deploymentServiceIdentity) (string, error) {
	cert, err := store.read(cfg.BootstrapCertFile)
	if err != nil {
		return "", err
	}
	key, err := store.read(cfg.BootstrapKeyFile)
	if err != nil {
		return "", err
	}
	pair, err := tls.X509KeyPair([]byte(cert), []byte(key))
	if err != nil {
		return "", errors.New("invalid deployment bootstrap key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" {
		return "", errors.New("expected the session-scoped deployment bootstrap identity")
	}
	ca, err := store.read(cfg.ServerCAFile)
	if err != nil {
		return "", err
	}
	roots, err := certificateRoots([]byte(ca), time.Now())
	if err != nil {
		return "", err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: cfg.ServerName, Certificates: []tls.Certificate{pair}}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("issuer redirect refused") }}
	raw, _ := json.Marshal(map[string]any{"request_id": record.RequestID, "subject": record.Subject, "csr_pem": record.CSR, "ttl_days": 30})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(cfg.Endpoint, "/")+"/v1/certificates/service-client/issue", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Service enrollment unavailable; retained pending request %s for identical retry", record.RequestID)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Service enrollment HTTP %d; retained pending request %s (do not create another request)", response.StatusCode, record.RequestID)
	}
	var reply struct {
		RequestID string `json:"request_id"`
		Subject   string `json:"subject"`
		Caller    string `json:"caller_identity"`
		Chain     string `json:"certificate_chain_pem"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&reply) != nil || reply.RequestID != record.RequestID || reply.Subject != record.Subject || reply.Caller != "service:deployment-bootstrap" {
		return "", errors.New("Service enrollment response identity does not match saved request")
	}
	return reply.Chain, nil
}

func (record deploymentServiceIdentity) secret() map[string]any {
	data := map[string]any{}
	for key, value := range map[string]string{"client.crt": record.CertificateChain, "client.key": record.PrivateKey, "server-ca.crt": record.ServerCA} {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return map[string]any{"data": data}
}

func lkeGetOptionalSecret(namespace, name string, extra ...string) (map[string]any, error) {
	args := append([]string{"-n", namespace, "get", "secret", name, "--ignore-not-found=true", "-o", "json"}, extra...)
	raw, err := kubectlCombinedOutput(nil, args...)
	if err != nil {
		return nil, fmt.Errorf("read identity Secret %s/%s failed", namespace, name)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var secret map[string]any
	if json.Unmarshal(raw, &secret) != nil {
		return nil, errors.New("invalid identity Secret response")
	}
	return secret, nil
}

func lkeEnsureDeploymentServiceIdentity(env map[string]string, name, subject string, listener map[string]any) (map[string]any, error) {
	store, err := deploymentIdentityStore(env)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(store.Root, "pki/services", strings.TrimPrefix(subject, "service:"))
	unlock, err := lockDeploymentIdentity(dir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	ns := lkeNamespaceName(env, "video-cloud")
	existing, err := lkeGetOptionalSecret(ns, name)
	if err != nil {
		return nil, err
	}
	record, err := ensureDeploymentServiceIdentity(store, env["CLOUD_STACK_NAME"], subject, existing, listener, lkeRegistrationServerDNS(env))
	if err != nil {
		return nil, err
	}
	wanted := record.secret()
	if existing != nil {
		for _, key := range []string{"client.crt", "client.key", "server-ca.crt"} {
			have, err := kubernetesSecretBytes(existing, key)
			if err != nil {
				return nil, err
			}
			want, _ := kubernetesSecretBytes(wanted, key)
			if !bytes.Equal(bytes.TrimSpace(have), bytes.TrimSpace(want)) {
				return nil, fmt.Errorf("installed %s differs from the environment identity; reconcile before deployment (no overwrite)", subject)
			}
		}
		return existing, nil
	}
	wanted["apiVersion"], wanted["kind"], wanted["type"] = "v1", "Secret", "Opaque"
	wanted["metadata"] = map[string]any{"namespace": ns, "name": name, "annotations": map[string]string{"rtk.realtek.com/deployment-identity-sha256": record.Fingerprint}}
	raw, _ := json.Marshal(wanted)
	// Create cannot overwrite a concurrent installation. Never echo kubectl's
	// diagnostic response: admission errors may include Secret contents.
	if _, err := kubectlCombinedOutput(bytes.NewReader(raw), "create", "-f", "-"); err != nil {
		return nil, errors.New("identity Secret creation failed; local identity retained, inspect installation before retry")
	}
	return wanted, nil
}

func runDeploymentServiceIdentity(args []string) error {
	fs := flag.NewFlagSet("deployment service-identity", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	subject := fs.String("subject", "", "exact service:<name> from deployment catalogue")
	confirm := fs.String("confirm", "", "selected stack name")
	installSeed := fs.Bool("install-seed", false, "create the selected workload initial identity Secret without overwriting existing state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" || !deploymentServiceSubject(*subject) {
		return errors.New("--environment and a catalogued --subject are required")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	if *confirm == "" || *confirm != cfg.Values["CLOUD_STACK_NAME"] {
		return errors.New("--confirm must match the selected stack")
	}
	store, err := newSecretStore("", *environment)
	if err != nil {
		return err
	}
	identityPath, err := store.safePath(filepath.Join("pki/services", strings.TrimPrefix(*subject, "service:"), "identity.json"))
	if err != nil {
		return err
	}
	unlock, err := lockDeploymentIdentity(filepath.Dir(identityPath))
	if err != nil {
		return err
	}
	defer unlock()
	record, err := ensureDeploymentServiceIdentity(store, *confirm, *subject, nil, nil, "")
	if err != nil {
		return err
	}
	if *installSeed {
		if err := installDeploymentIdentitySeed(store, cfg.Values, record); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stdout, "%s %s certificate_sha256=%s (environment SecretStore; runtime owner state unchanged)\n", *environment, *subject, record.Fingerprint)
	return nil
}

// The seed is separate from runtime state and is immutable during normal deploy.
func installDeploymentIdentitySeed(store secretStore, env map[string]string, record deploymentServiceIdentity) error {
	namespaceKey := "video-cloud"
	if record.Subject == "service:account-manager" {
		namespaceKey = "account-manager"
	}
	if record.Subject == "service:openbao" {
		namespaceKey = "secrets"
	}
	namespace := lkeName(env["CLOUD_STACK_NAME"]) + "-" + namespaceKey
	name := strings.TrimPrefix(record.Subject, "service:") + "-deployment-identity"
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	live, err := lkeGetOptionalSecret(namespace, name, "--kubeconfig", store.KubeconfigPath())
	if err != nil {
		return err
	}
	if live != nil {
		value, err := kubernetesSecretBytes(live, "identity.json")
		if err != nil {
			return err
		}
		var existing deploymentServiceIdentity
		if json.Unmarshal(value, &existing) != nil || existing != record {
			return errors.New("installed deployment seed differs from environment record; no overwrite")
		}
		return nil
	}
	secret := newK8SSecretObject(namespace, name, map[string]string{"identity.json": string(raw)})
	secret["immutable"] = true
	payload, _ := json.Marshal(secret)
	if _, err := kubectlCombinedOutput(bytes.NewReader(payload), "create", "-f", "-", "--kubeconfig", store.KubeconfigPath()); err != nil {
		return errors.New("deployment seed installation failed; local record retained")
	}
	return nil
}
