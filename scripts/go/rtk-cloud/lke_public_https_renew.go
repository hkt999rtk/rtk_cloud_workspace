package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

// lkePublicHTTPSLeaf is deliberately public-only. Renewal evidence must never
// contain the TLS private key, a Kubernetes Secret payload, or DNS credentials.
type lkePublicHTTPSLeaf struct {
	Subject         string   `json:"subject"`
	Issuer          string   `json:"issuer"`
	Serial          string   `json:"serial"`
	SHA256          string   `json:"sha256"`
	PublicKeySHA256 string   `json:"public_key_sha256"`
	DNSNames        []string `json:"dns_names"`
	NotBefore       string   `json:"not_before"`
	NotAfter        string   `json:"not_after"`
}

type lkePublicHTTPSRenewalEvidence struct {
	CompletedAt          string             `json:"completed_at"`
	Environment          string             `json:"environment"`
	Stack                string             `json:"stack"`
	Issuer               string             `json:"issuer"`
	IngressNamespace     string             `json:"ingress_namespace"`
	TLSSecret            string             `json:"tls_secret"`
	Hosts                []string           `json:"hosts"`
	OldSecretVersion     string             `json:"old_secret_version"`
	NewSecretVersion     string             `json:"new_secret_version"`
	OldLeaf              lkePublicHTTPSLeaf `json:"old_leaf"`
	NewLeaf              lkePublicHTTPSLeaf `json:"new_leaf"`
	InstalledFingerprint string             `json:"installed_fingerprint"`
}

func runLKERenewPublicHTTPS(args []string) error {
	fs := flag.NewFlagSet("lke-renew-public-https", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	envRootFlag := fs.String("env-root", "", "normalized environment runtime root")
	confirm := fs.String("confirm", "", "exact CLOUD_STACK_NAME confirmation")
	output := fs.String("output", "", "new private directory for public renewal evidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("lke-renew-public-https --env-root is required")
	}
	if *output == "" {
		return errors.New("lke-renew-public-https --output is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	env, err := envroot.Load(envRoot, "")
	if err != nil {
		return err
	}
	if env.Values["CLOUD_PROVIDER"] != "lke" {
		return fmt.Errorf("lke-renew-public-https requires CLOUD_PROVIDER=lke, got %q", env.Values["CLOUD_PROVIDER"])
	}
	stack := env.Values["CLOUD_STACK_NAME"]
	if *confirm != stack || stack == "" {
		return fmt.Errorf("--confirm must exactly match CLOUD_STACK_NAME=%q", stack)
	}
	paths := newProvisionPaths(workspace, envRoot, provisionOptions{})
	environment := env.Values["CLOUD_ENV_NAME"]
	if environment == "" {
		return errors.New("CLOUD_ENV_NAME is required")
	}
	_, restore, err := configurePublicHTTPSRenewalSecretStore(environment)
	if err != nil {
		return err
	}
	defer restore()
	defer func() { activeSecretEnvironmentRoot = "" }()
	if err := ensureLKEKubeAccess(paths, env.Values, false); err != nil {
		return err
	}
	return lkeRenewPublicHTTPS(paths, env.Values, *output)
}

// configurePublicHTTPSRenewalSecretStore checks the exact protected inputs used
// by this command. Full provisioning also validates every historical evidence
// file under pki/, including retained executable probes, which is unrelated to a
// public certificate renewal and would block it on non-secret file modes.
func configurePublicHTTPSRenewalSecretStore(environment string) (secretStore, func(), error) {
	store, err := newSecretStore("", environment)
	if err != nil {
		return secretStore{}, nil, err
	}
	if err := verifySecretStorePermissionsOnly(store); err != nil {
		return secretStore{}, nil, err
	}
	if _, err := store.read("kube/kubeconfig.yaml"); err != nil {
		return secretStore{}, nil, fmt.Errorf("read renewal kubeconfig: %w", err)
	}
	values, err := store.readOperator()
	if err != nil {
		return secretStore{}, nil, err
	}
	values["RTK_CLOUD_LKE_KUBECONFIG"] = store.KubeconfigPath()
	values["RTK_CLOUD_KUBECONFIG"] = store.KubeconfigPath()
	restore := installAllCredentialEnvironment(values)
	lkeRuntimeSecretCache = map[string]string{}
	lkeRuntimeSecretStateDir = store.RuntimeDir()
	activeSecretEnvironmentRoot = store.Root
	return store, restore, nil
}

func lkeRenewPublicHTTPS(paths provisionPaths, env map[string]string, output string) error {
	hosts := lkePublicHTTPSHosts(lkePublicHTTPSRoutes(env))
	if len(hosts) == 0 {
		return errors.New("no public HTTPS routes configured")
	}
	oldPEM, oldVersion, err := lkeReadPublicHTTPSTLSCertificate(env)
	if err != nil {
		return err
	}
	oldLeaf, err := lkePublicHTTPSLeafFromPEM(oldPEM)
	if err != nil {
		return fmt.Errorf("read current public HTTPS leaf: %w", err)
	}

	certPEM, keyPEM, err := lkeIssueForcedPublicHTTPSCertificate(paths, env, hosts)
	if err != nil {
		return err
	}
	if err := lkeValidatePublicHTTPSCertificate(certPEM, keyPEM, hosts); err != nil {
		return fmt.Errorf("validate renewed public HTTPS certificate before installation: %w", err)
	}
	newLeaf, err := lkePublicHTTPSLeafFromPEM([]byte(certPEM))
	if err != nil {
		return err
	}
	if newLeaf.SHA256 == oldLeaf.SHA256 {
		return errors.New("forced public HTTPS renewal returned the currently installed leaf")
	}

	if err := kubectlApply(lkePublicHTTPSTLSSecretManifest(env, certPEM, keyPEM)); err != nil {
		return err
	}
	installedPEM, newVersion, err := lkeReadPublicHTTPSTLSCertificate(env)
	if err != nil {
		return fmt.Errorf("read installed public HTTPS certificate: %w", err)
	}
	installedLeaf, err := lkePublicHTTPSLeafFromPEM(installedPEM)
	if err != nil {
		return fmt.Errorf("parse installed public HTTPS certificate: %w", err)
	}
	if installedLeaf.SHA256 != newLeaf.SHA256 {
		return fmt.Errorf("installed public HTTPS leaf fingerprint %s does not match issued successor %s", installedLeaf.SHA256, newLeaf.SHA256)
	}
	if newVersion == oldVersion {
		return errors.New("public HTTPS Secret resourceVersion did not change after renewal")
	}
	if err := lkeWritePublicHTTPSCertificateCache(paths, env, hosts, certPEM, keyPEM); err != nil {
		return fmt.Errorf("public HTTPS certificate was installed but cache update failed: %w", err)
	}
	return lkeWritePublicHTTPSRenewalEvidence(output, lkePublicHTTPSRenewalEvidence{
		CompletedAt:          time.Now().UTC().Format(time.RFC3339),
		Environment:          env["CLOUD_ENV_NAME"],
		Stack:                env["CLOUD_STACK_NAME"],
		Issuer:               "ACME DNS-01 via workspace certbot owner",
		IngressNamespace:     lkeIngressNamespace(env),
		TLSSecret:            lkePublicHTTPSTLSSecretName(env),
		Hosts:                hosts,
		OldSecretVersion:     oldVersion,
		NewSecretVersion:     newVersion,
		OldLeaf:              oldLeaf,
		NewLeaf:              newLeaf,
		InstalledFingerprint: installedLeaf.SHA256,
	})
}

func lkeReadPublicHTTPSTLSCertificate(env map[string]string) ([]byte, string, error) {
	out, err := kubectlCombinedOutput(nil, "-n", lkeIngressNamespace(env), "get", "secret", lkePublicHTTPSTLSSecretName(env), "-o", "jsonpath={.data.tls\\.crt}{\"\\n\"}{.metadata.resourceVersion}")
	if err != nil {
		return nil, "", fmt.Errorf("read public HTTPS Secret certificate: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, "", errors.New("public HTTPS Secret did not return certificate and resourceVersion")
	}
	certPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, "", fmt.Errorf("decode public HTTPS Secret certificate: %w", err)
	}
	return certPEM, strings.TrimSpace(parts[1]), nil
}

func lkeValidatePublicHTTPSCertificate(certPEM, keyPEM string, hosts []string) error {
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return fmt.Errorf("certificate and key do not form a TLS pair: %w", err)
	}
	leaf, err := parseFirstCertificatePEM([]byte(certPEM))
	if err != nil || leaf == nil {
		return errors.New("renewed public HTTPS leaf is missing or malformed")
	}
	if time.Now().After(leaf.NotAfter) {
		return errors.New("renewed public HTTPS leaf is expired")
	}
	for _, host := range hosts {
		if err := leaf.VerifyHostname(host); err != nil {
			return fmt.Errorf("renewed public HTTPS leaf does not cover %s: %w", host, err)
		}
	}
	return nil
}

func lkePublicHTTPSLeafFromPEM(raw []byte) (lkePublicHTTPSLeaf, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return lkePublicHTTPSLeaf{}, errors.New("certificate PEM is empty")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return lkePublicHTTPSLeaf{}, err
	}
	publicKey, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return lkePublicHTTPSLeaf{}, err
	}
	fingerprint := sha256.Sum256(cert.Raw)
	publicKeyHash := sha256.Sum256(publicKey)
	return lkePublicHTTPSLeaf{
		Subject:         cert.Subject.String(),
		Issuer:          cert.Issuer.String(),
		Serial:          strings.ToUpper(cert.SerialNumber.Text(16)),
		SHA256:          hex.EncodeToString(fingerprint[:]),
		PublicKeySHA256: hex.EncodeToString(publicKeyHash[:]),
		DNSNames:        append([]string(nil), cert.DNSNames...),
		NotBefore:       cert.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:        cert.NotAfter.UTC().Format(time.RFC3339),
	}, nil
}

func lkeWritePublicHTTPSRenewalEvidence(output string, evidence lkePublicHTTPSRenewalEvidence) error {
	if err := os.Mkdir(output, 0o700); err != nil {
		return fmt.Errorf("create public HTTPS renewal evidence directory: %w", err)
	}
	body, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(output, "renewal.json")
	if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "[lke-renew-public-https] public evidence: %s\n", path)
	return nil
}
