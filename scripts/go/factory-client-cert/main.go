// Command factory-client-cert manages the offline, environment-local factory
// client CA. The CA key is never copied to Kubernetes or returned to customers.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type issuance struct {
	Serial    string     `json:"serial"`
	CloudID   string     `json:"cloud_id"`
	FactoryID string     `json:"factory_id"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

var factoryIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var cloudIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: factory-client-cert init|sign|revoke --environment ENV")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	environment := fs.String("environment", "", "environment name")
	configRoot := fs.String("config-root", "", "operator configuration root")
	csrPath := fs.String("csr", "", "factory CSR PEM file")
	cloudID := fs.String("cloud-id", "", "authorized cloud UUID")
	factoryID := fs.String("factory-id", "", "factory slug")
	outPath := fs.String("out", "", "issued certificate PEM output")
	serial := fs.String("serial", "", "certificate serial to revoke")
	stack := fs.String("stack", "", "verified deployment stack for publishing")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !factoryIDPattern.MatchString(*environment) {
		return errors.New("valid --environment is required")
	}
	root := *configRoot
	if root == "" {
		root = os.Getenv("RTK_CLOUD_CONFIG_ROOT")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, ".config", "rtk_cloud")
	}
	dir := filepath.Join(root, *environment, "pki", "factory-client-ca")
	switch args[0] {
	case "init":
		return initCA(dir, *environment)
	case "sign":
		return signCSR(dir, *csrPath, *cloudID, *factoryID, *outPath)
	case "revoke":
		return revoke(dir, *serial)
	case "refresh-crl":
		registry, err := loadRegistry(dir)
		if err != nil {
			return err
		}
		return updateCRL(dir, registry)
	case "publish":
		return publishCA(filepath.Join(root, *environment), dir, *stack)
	default:
		return errors.New("expected init, sign, revoke, refresh-crl, or publish")
	}
}

func publishCA(environmentRoot, dir, stack string) error {
	if !factoryIDPattern.MatchString(stack) {
		return errors.New("valid --stack is required")
	}
	kubeconfig := filepath.Join(environmentRoot, "kube", "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); err != nil {
		return fmt.Errorf("selected environment kubeconfig: %w", err)
	}
	namespace := stack + "-ingress"
	check := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "namespace", namespace, "-o", "name")
	if output, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("verify selected ingress namespace: %w: %s", err, output)
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return err
	}
	crl, err := os.ReadFile(filepath.Join(dir, "ca.crl"))
	if err != nil {
		return err
	}
	if len(ca) == 0 || len(crl) == 0 {
		return errors.New("factory client CA or CRL is empty")
	}
	manifest, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": stack + "-factory-client-ca", "namespace": namespace}, "type": "Opaque", "data": map[string]string{"ca.crt": base64.StdEncoding.EncodeToString(ca), "ca.crl": base64.StdEncoding.EncodeToString(crl)}})
	if err != nil {
		return err
	}
	apply := exec.Command("kubectl", "--kubeconfig", kubeconfig, "apply", "-f", "-")
	apply.Stdin = strings.NewReader(string(manifest))
	if output, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("publish factory client trust: %w: %s", err, output)
	}
	return nil
}

func initCA(dir, environment string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "ca.key")); err == nil {
		return errors.New("factory client CA already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "RTK Factory Client CA " + environment}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(3, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte("rtk-factory-client-" + environment)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "ca.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return err
	}
	if err := writePrivate(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return err
	}
	if err := saveRegistry(dir, []issuance{}); err != nil {
		return err
	}
	return updateCRL(dir, nil)
}

func signCSR(dir, csrPath, cloudID, factoryID, outPath string) error {
	if !cloudIDPattern.MatchString(cloudID) || !factoryIDPattern.MatchString(factoryID) || csrPath == "" || outPath == "" {
		return errors.New("--csr, --cloud-id UUID, --factory-id slug, and --out are required")
	}
	if filepath.Clean(outPath) == filepath.Join(dir, "ca.key") || filepath.Clean(outPath) == filepath.Join(dir, "ca.crt") || filepath.Clean(outPath) == filepath.Join(dir, "ca.crl") || filepath.Clean(outPath) == filepath.Join(dir, "registry.json") {
		return errors.New("certificate output cannot replace CA material")
	}
	if _, err := os.Stat(outPath); err == nil {
		return errors.New("certificate output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ca, key, err := loadCA(dir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(csrPath)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return errors.New("invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return err
	}
	if err := csr.CheckSignature(); err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	identity := &url.URL{Scheme: "spiffe", Host: "rtk-cloud", Path: "/factory/" + cloudID + "/" + factoryID}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "factory:" + factoryID}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{identity}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, key)
	if err != nil {
		return err
	}
	registry, err := loadRegistry(dir)
	if err != nil {
		return err
	}
	registry = append(registry, issuance{Serial: serial.Text(16), CloudID: cloudID, FactoryID: factoryID, ExpiresAt: cert.NotAfter})
	if err := writePrivate(outPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return err
	}
	return saveRegistry(dir, registry)
}

func revoke(dir, serial string) error {
	if _, err := hex.DecodeString(serial); err != nil || serial == "" {
		return errors.New("valid hexadecimal --serial is required")
	}
	registry, err := loadRegistry(dir)
	if err != nil {
		return err
	}
	found := false
	for i := range registry {
		if registry[i].Serial == serial {
			found = true
			if registry[i].RevokedAt == nil {
				now := time.Now().UTC()
				registry[i].RevokedAt = &now
			}
		}
	}
	if !found {
		return errors.New("certificate serial not found")
	}
	if err := updateCRL(dir, registry); err != nil {
		return err
	}
	return saveRegistry(dir, registry)
}

func updateCRL(dir string, registry []issuance) error {
	ca, key, err := loadCA(dir)
	if err != nil {
		return err
	}
	entries := []x509.RevocationListEntry{}
	for _, item := range registry {
		if item.RevokedAt == nil {
			continue
		}
		serial := new(big.Int)
		if _, ok := serial.SetString(item.Serial, 16); !ok {
			return errors.New("invalid registry serial")
		}
		entries = append(entries, x509.RevocationListEntry{SerialNumber: serial, RevocationTime: *item.RevokedAt})
	}
	now := time.Now().UTC()
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(now.UnixNano()), ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(7 * 24 * time.Hour), RevokedCertificateEntries: entries}, ca, key)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, "ca.crl"), pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}))
}

func loadCA(dir string) (*x509.Certificate, ed25519.PrivateKey, error) {
	certRaw, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, nil, err
	}
	certBlock, _ := pem.Decode(certRaw)
	if certBlock == nil {
		return nil, nil, errors.New("invalid CA certificate")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyRaw, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyRaw)
	if keyBlock == nil {
		return nil, nil, errors.New("invalid CA key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, errors.New("unexpected CA key type")
	}
	return cert, key, nil
}

func loadRegistry(dir string) ([]issuance, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if err != nil {
		return nil, err
	}
	var out []issuance
	err = json.Unmarshal(raw, &out)
	return out, err
}

func saveRegistry(dir string, registry []issuance) error {
	raw, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, "registry.json"), append(raw, '\n'))
}

func writePrivate(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".factory-client-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		return randomSerial()
	}
	return serial, nil
}
