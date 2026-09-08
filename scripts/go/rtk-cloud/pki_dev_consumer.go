package main

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

func validPKIDevConsumer(name string) bool {
	switch name {
	case "video-cloud-api", "certissuer", "factoryenroll", "pkibroker", "emqx-pki":
		return true
	}
	return false
}

// Each client has an independent dev transport CA. The controller trusts its
// public CA explicitly; existing server, Account Manager and JWT keys stay intact.
// This is bootstrap transport, not Device/Brand/Product authority.
func preparePKIDevConsumer(store secretStore, name string, now time.Time) (string, error) {
	return preparePKIDevTransport(store, name, now, false)
}

func preparePKIDevTransport(store secretStore, name string, now time.Time, server bool) (string, error) {
	valid := validPKIDevConsumer(name)
	category := "consumers"
	if server {
		valid = name == "video-cloud-api-pki" || name == "mqtt-pki"
		category = "servers"
	}
	if store.Environment != "dev" || !valid {
		return "", errors.New("known dev consumer required")
	}
	parent := filepath.Join(store.Root, "pki", category)
	for _, dir := range []string{store.ConfigRoot, store.Root, filepath.Join(store.Root, "pki"), parent} {
		if err := ensurePrivateDirectory(dir); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(parent, name)
	if err := os.Mkdir(dir+".lock", 0700); err != nil {
		return "", fmt.Errorf("acquire consumer preparation lock: %w", err)
	}
	defer os.Remove(dir + ".lock")
	if _, err := os.Lstat(dir); err == nil {
		return dir, validatePKIDevTransport(dir, name, now, server)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	ca, caKey, caPEM, _, err := newLKECertificateAuthority("rtk-dev-"+name+"-management-ca", "p256")
	if err != nil {
		return "", err
	}
	key, keyPEM, err := newLKECertificatePrivateKey("p256")
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", err
	}
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(90 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		host := name + ".video-cloud-dev-video-cloud.svc"
		leaf.DNSNames = []string{host, host + ".cluster.local"}
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.Public(), caKey)
	if err != nil {
		return "", err
	}
	staged, err := os.MkdirTemp(parent, "."+name+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	for filename, data := range map[string][]byte{
		"ca.crt": []byte(caPEM), "tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "tls.key": []byte(keyPEM),
	} {
		if err := os.WriteFile(filepath.Join(staged, filename), data, 0600); err != nil {
			return "", err
		}
	}
	if err := validatePKIDevTransport(staged, name, now, server); err != nil {
		return "", err
	}
	if err := os.Rename(staged, dir); err != nil {
		return "", err
	}
	return dir, nil
}

func validatePKIDevConsumer(dir, name string, now time.Time) error {
	return validatePKIDevTransport(dir, name, now, false)
}

func validatePKIDevTransport(dir, name string, now time.Time, server bool) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("consumer directory must be a real mode-0700 directory")
	}
	material := make(map[string][]byte)
	for _, filename := range []string{"ca.crt", "tls.crt", "tls.key"} {
		path := filepath.Join(dir, filename)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("consumer %s must be a regular mode-0600 file", filename)
		}
		material[filename], err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read consumer %s failed", filename)
		}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(material["ca.crt"]) {
		return errors.New("invalid consumer CA")
	}
	pair, err := tls.X509KeyPair(material["tls.crt"], material["tls.key"])
	if err != nil {
		return errors.New("invalid consumer TLS pair")
	}
	usage := x509.ExtKeyUsageClientAuth
	hostname := ""
	if server {
		usage = x509.ExtKeyUsageServerAuth
		hostname = name + ".video-cloud-dev-video-cloud.svc"
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || leaf.Subject.CommonName != name || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != usage || leaf.NotAfter.Sub(now) < 24*time.Hour {
		return errors.New("invalid consumer leaf profile or remaining lifetime")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, DNSName: hostname, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return errors.New("consumer certificate validation failed")
	}
	return nil
}
