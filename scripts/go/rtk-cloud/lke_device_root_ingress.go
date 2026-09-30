package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Product-issued device certificates have two intermediates below Device Root.
// Keep their public trust anchor beside the environment's pinned Root identity
// so a future full deployment cannot silently restore the legacy-only bundle.
func pinnedDeviceIngressRootPath(paths provisionPaths) string {
	return filepath.Join(paths.EnvRoot, "pki", "devices", "device-root.crt")
}

func loadPinnedDeviceIngressRoot(paths provisionPaths, env map[string]string) (string, error) {
	id, fingerprint := deviceIngressRootPin(env)
	if id == "" && fingerprint == "" {
		return "", nil
	}
	if id == "" || len(fingerprint) != 64 {
		return "", errors.New("device ingress Root ID and SHA-256 pin must both be configured")
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return "", errors.New("device ingress Root SHA-256 pin is invalid")
	}
	raw, err := os.ReadFile(pinnedDeviceIngressRootPath(paths))
	if err != nil {
		return "", fmt.Errorf("read pinned device ingress Root certificate: %w", err)
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return "", errors.New("device ingress Root file must contain exactly one PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse device ingress Root certificate: %w", err)
	}
	actual := sha256.Sum256(cert.Raw)
	if cert.Subject.CommonName != id || hex.EncodeToString(actual[:]) != fingerprint ||
		!cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 ||
		time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) ||
		cert.CheckSignatureFrom(cert) != nil {
		return "", errors.New("device ingress Root certificate does not match the active environment pin or CA policy")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), nil
}

func deviceIngressRootPin(env map[string]string) (string, string) {
	id, fingerprint := env["PKI_DEVICE_ROOT_ID"], env["PKI_DEVICE_ROOT_SHA256"]
	if activeSecretEnvironmentRoot != "" {
		id, fingerprint = os.Getenv("PKI_DEVICE_ROOT_ID"), os.Getenv("PKI_DEVICE_ROOT_SHA256")
	}
	return strings.TrimSpace(id), strings.ToLower(strings.TrimSpace(fingerprint))
}
