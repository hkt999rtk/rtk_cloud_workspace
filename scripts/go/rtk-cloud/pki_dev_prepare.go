package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pkiDevServerName = "pki-controller.video-cloud-dev-video-cloud.svc"

var pkiDevMaterialFiles = []string{
	"management-ca.crt", "controller.crt", "controller.key", "account-manager.crt", "account-manager.key",
	"jwt-access.key", "jwt-access.pub", "jwt-refresh.key", "jwt-refresh.pub", "database-password",
}

// Preparation only: no Kubernetes, database, provider or operator-env mutations.
func runPKIDevPrepare(args []string) error {
	fs := flag.NewFlagSet("pki-dev-prepare", flag.ContinueOnError)
	environment := fs.String("environment", "", "must be dev")
	configRoot := fs.String("config-root", "", "canonical SecretStore base directory")
	consumer := fs.String("consumer", "", "optional management client: video-cloud-api, certissuer, factoryenroll or pkibroker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *environment != "dev" || fs.NArg() != 0 {
		return errors.New("pki-dev-prepare requires --environment dev and no positional arguments")
	}
	if *consumer != "" && !validPKIDevConsumer(*consumer) {
		return errors.New("unknown dev PKI consumer")
	}
	store, err := newSecretStore(*configRoot, "dev")
	if err != nil {
		return err
	}
	dir, err := preparePKIDevMaterial(store, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Printf("Validated dev PKI bootstrap material at %s; no live configuration changed\n", dir)
	if *consumer != "" {
		path, err := preparePKIDevConsumer(store, *consumer, time.Now().UTC())
		if err != nil {
			return err
		}
		fmt.Printf("Validated dev PKI consumer material at %s; bind its CA to controller client trust before use\n", path)
	}
	return nil
}

func preparePKIDevMaterial(store secretStore, now time.Time) (string, error) {
	if store.Environment != "dev" {
		return "", errors.New("PKI bootstrap material is dev-only")
	}
	parent := filepath.Join(store.Root, "pki")
	for _, dir := range []string{store.ConfigRoot, store.Root, parent} {
		if err := ensurePrivateDirectory(dir); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(parent, "controller-bootstrap")
	// Serialize generation; a partial/invalid existing bundle is never rotated.
	lock := dir + ".lock"
	if err := os.Mkdir(lock, 0700); err != nil {
		return "", fmt.Errorf("acquire dev PKI preparation lock: %w", err)
	}
	defer os.Remove(lock)
	if _, err := os.Lstat(dir); err == nil {
		return dir, validatePKIDevMaterial(dir, now)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	material, err := newPKIDevMaterial(now)
	if err != nil {
		return "", err
	}
	staged, err := os.MkdirTemp(parent, ".controller-bootstrap-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	for _, name := range pkiDevMaterialFiles {
		if err := os.WriteFile(filepath.Join(staged, name), material[name], 0600); err != nil {
			return "", err
		}
	}
	if err := validatePKIDevMaterial(staged, now); err != nil {
		return "", err
	}
	if err := os.Rename(staged, dir); err != nil {
		return "", err
	}
	return dir, nil
}

func newPKIDevMaterial(now time.Time) (map[string][]byte, error) {
	material := make(map[string][]byte)
	// Independent management transport only. The CA private key is not retained.
	ca, caKey, caPEM, _, err := newLKECertificateAuthority("rtk-dev-pki-bootstrap-management-ca", "p256")
	if err != nil {
		return nil, err
	}
	material["management-ca.crt"] = []byte(caPEM)
	for _, name := range []string{"controller", "account-manager"} {
		key, keyPEM, err := newLKECertificatePrivateKey("p256")
		if err != nil {
			return nil, err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name},
			NotBefore: now.Add(-time.Minute), NotAfter: now.Add(90 * 24 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if name == "controller" {
			leaf.DNSNames = []string{pkiDevServerName, pkiDevServerName + ".cluster.local"}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.Public(), caKey)
		if err != nil {
			return nil, err
		}
		material[name+".crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		material[name+".key"] = []byte(keyPEM)
	}
	for _, name := range []string{"jwt-access", "jwt-refresh"} {
		key, err := rsa.GenerateKey(rand.Reader, 3072)
		if err != nil {
			return nil, err
		}
		private, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return nil, err
		}
		material[name+".key"] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})
		material[name+".pub"] = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})
	}
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return nil, err
	}
	material["database-password"] = []byte(base64.RawURLEncoding.EncodeToString(password))
	return material, nil
}

func validatePKIDevMaterial(dir string, now time.Time) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("dev PKI material directory must be a real mode-0700 directory")
	}
	material := make(map[string][]byte)
	for _, name := range pkiDevMaterialFiles {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("dev PKI material %s must be a regular mode-0600 file", name)
		}
		material[name], err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read dev PKI material %s failed", name)
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material["management-ca.crt"]) {
		return errors.New("invalid dev management CA")
	}
	for _, name := range []string{"controller", "account-manager"} {
		pair, err := tls.X509KeyPair(material[name+".crt"], material[name+".key"])
		if err != nil {
			return fmt.Errorf("invalid dev %s TLS pair", name)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || leaf.IsCA || leaf.Subject.CommonName != name || len(leaf.ExtKeyUsage) != 1 {
			return fmt.Errorf("invalid dev %s leaf profile", name)
		}
		opts := x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if name == "controller" {
			opts.DNSName = pkiDevServerName
			opts.KeyUsages = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		if leaf.ExtKeyUsage[0] != opts.KeyUsages[0] || leaf.NotAfter.Sub(now) < 24*time.Hour {
			return fmt.Errorf("dev %s certificate profile or remaining lifetime requires review", name)
		}
		if _, err := leaf.Verify(opts); err != nil {
			return fmt.Errorf("dev %s certificate validation failed", name)
		}
	}
	for _, name := range []string{"jwt-access", "jwt-refresh"} {
		block, _ := pem.Decode(material[name+".key"])
		if block == nil {
			return fmt.Errorf("invalid dev %s private key", name)
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		key, ok := parsed.(*rsa.PrivateKey)
		if err != nil || !ok || key.N.BitLen() < 3072 {
			return fmt.Errorf("invalid dev %s RSA key", name)
		}
		public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		block, _ = pem.Decode(material[name+".pub"])
		if err != nil || block == nil || !bytes.Equal(block.Bytes, public) {
			return fmt.Errorf("dev %s public/private key mismatch", name)
		}
	}
	if bytes.Equal(material["jwt-access.pub"], material["jwt-refresh.pub"]) {
		return errors.New("dev access and refresh signers must be distinct")
	}
	password, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(material["database-password"])))
	if err != nil || len(password) != 32 {
		return errors.New("invalid dev controller database password")
	}
	return nil
}
