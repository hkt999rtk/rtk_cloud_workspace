package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Lock a stable sibling, since initial bundle installation renames the directory.
// Kernel locks are released on process exit, including interrupted enrollment.
func lockDeploymentIdentity(dir string) (func(), error) {
	if activeSecretEnvironmentRoot != "" && strings.HasPrefix(dir, activeSecretEnvironmentRoot+string(filepath.Separator)) {
		store := secretStore{Root: activeSecretEnvironmentRoot, ConfigRoot: filepath.Dir(activeSecretEnvironmentRoot)}
		relative, _ := filepath.Rel(store.Root, filepath.Join(dir, "identity.json"))
		if _, err := store.safePath(relative); err != nil {
			return nil, err
		}
	}

	if err := ensurePrivateDirectory(filepath.Dir(dir)); err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open deployment identity lock: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("deployment identity is in use: %s", dir)
	}
	return func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = f.Close() }, nil
}

func readDeploymentTLSBundle(dir, label string, names []string) (map[string]string, error) {
	result := map[string]string{}
	if info, err := os.Lstat(dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("%s TLS state must be a real directory", label)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s TLS state file %s must be regular", label, name)
		}
		// Legacy bundles may have public certificates at 0644. Restrict permissions,
		// preserving the credential bytes; never follow symlinks.
		if err := os.Chmod(path, 0600); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		result[name] = string(raw)
	}
	if len(result) == 0 {
		return nil, nil
	}
	if len(result) != len(names) {
		return nil, fmt.Errorf("%s TLS state is incomplete under %s; restore the existing bundle, do not generate a replacement", label, dir)
	}
	return result, nil
}

func validateDeploymentTLS(certPEM, keyPEM, caPEM string, subjects, dns []string, purpose x509.ExtKeyUsage) error {
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return errors.New("certificate and private key do not match")
	}
	chain, err := pemCertificates([]byte(certPEM))
	if err != nil {
		return err
	}
	leaf := chain[0]
	if leaf.IsCA || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != purpose || leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		return errors.New("certificate has the wrong purpose")
	}
	if len(subjects) > 0 && !slices.Contains(subjects, leaf.Subject.CommonName) {
		return errors.New("certificate has the wrong subject")
	}
	if purpose == x509.ExtKeyUsageClientAuth && (len(leaf.Subject.Names) != 1 || len(leaf.DNSNames)+len(leaf.IPAddresses)+len(leaf.URIs)+len(leaf.EmailAddresses) != 0) {
		return errors.New("client certificate has an unexpected identity extension")
	}
	roots, err := certificateRoots([]byte(caPEM), time.Now())
	if err != nil {
		return err
	}
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{purpose}}); err != nil {
		return errors.New("certificate chain is invalid or expired")
	}
	for _, name := range dns {
		if err := leaf.VerifyHostname(name); err != nil {
			return fmt.Errorf("certificate does not cover configured DNS %s", name)
		}
	}
	return nil
}
