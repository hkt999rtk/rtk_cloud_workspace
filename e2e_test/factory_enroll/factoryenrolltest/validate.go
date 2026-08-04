package factoryenrolltest

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
)

func ValidateCertificate(deviceID string, key *ecdsa.PrivateKey, certificatePEM string) (DeviceResult, error) {
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return DeviceResult{}, fmt.Errorf("response certificate_pem is not a certificate PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return DeviceResult{}, fmt.Errorf("parse certificate: %w", err)
	}
	result := DeviceResult{
		CertSubjectCN: cert.Subject.CommonName,
		CertNotBefore: cert.NotBefore,
		CertNotAfter:  cert.NotAfter,
		CA:            cert.IsCA,
	}
	if !strings.EqualFold(cert.Subject.CommonName, deviceID) {
		return result, fmt.Errorf("certificate subject cn %q does not match devid %q", cert.Subject.CommonName, deviceID)
	}
	certPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return result, fmt.Errorf("certificate public key is %T, want ECDSA", cert.PublicKey)
	}
	if certPub.X.Cmp(key.PublicKey.X) != 0 || certPub.Y.Cmp(key.PublicKey.Y) != 0 {
		return result, fmt.Errorf("certificate public key does not match generated device key")
	}
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			result.ClientAuthUsable = true
			break
		}
	}
	if cert.IsCA {
		return result, fmt.Errorf("device certificate must not be a CA certificate")
	}
	if !result.ClientAuthUsable {
		return result, fmt.Errorf("device certificate missing client auth extended key usage")
	}
	return result, nil
}

func ValidateCertificateChain(certificatePEM, chainPEM string) error {
	leafBlock, _ := pem.Decode([]byte(certificatePEM))
	if leafBlock == nil || leafBlock.Type != "CERTIFICATE" {
		return fmt.Errorf("response certificate_pem is not a certificate PEM block")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		return fmt.Errorf("parse leaf certificate: %w", err)
	}
	var chain []*x509.Certificate
	rest := []byte(chainPEM)
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("parse chain certificate: %w", err)
		}
		chain = append(chain, cert)
	}
	if len(chain) == 0 {
		return fmt.Errorf("response certificate_chain_pem contains no certificates")
	}
	roots := x509.NewCertPool()
	intermediates := x509.NewCertPool()
	for idx, cert := range chain {
		if cert.CheckSignatureFrom(cert) == nil || idx == len(chain)-1 {
			roots.AddCert(cert)
		} else {
			intermediates.AddCert(cert)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return fmt.Errorf("verify certificate chain: %w", err)
	}
	return nil
}
