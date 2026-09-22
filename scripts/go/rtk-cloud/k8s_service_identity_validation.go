package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"
)

// lkeRequirePlatformServiceIdentitySecret is a read-only, pre-mutation check.
// The listener Secret supplies the independent client trust anchor and CRL;
// a plugin cannot validate itself by supplying its own CA in server-ca.crt.
func lkeRequirePlatformServiceIdentitySecret(env map[string]string, name, label, subject string) error {
	identity, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "secret", name)
	if err != nil {
		return fmt.Errorf("%s is unavailable: %w", label, err)
	}
	listener, err := kubectlResourceJSON(lkeNamespaceName(env, "account-manager"), "secret", serviceRegistrationTLSSecretName)
	if err != nil {
		return fmt.Errorf("service registration TLS Secret is unavailable: %w", err)
	}
	return validatePlatformServiceIdentityMaterial(identity, listener, subject, lkeRegistrationServerDNS(env), time.Now())
}

func kubernetesSecretBytes(secret map[string]any, key string) ([]byte, error) {
	data, ok := secret["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Secret has no data")
	}
	encoded, ok := data[key].(string)
	if !ok || encoded == "" {
		return nil, fmt.Errorf("Secret lacks %s", key)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 {
		return nil, fmt.Errorf("Secret has invalid %s", key)
	}
	return decoded, nil
}

func pemCertificates(raw []byte) ([]*x509.Certificate, error) {
	var certificates []*x509.Certificate
	for len(bytes.TrimSpace(raw)) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("invalid certificate PEM")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid X.509 certificate")
		}
		certificates = append(certificates, certificate)
		raw = rest
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("empty certificate bundle")
	}
	return certificates, nil
}

func pemCRLs(raw []byte) ([]*x509.RevocationList, error) {
	// DER is binary: trimming it can remove a valid trailing signature byte.
	if list, err := x509.ParseRevocationList(raw); err == nil {
		return []*x509.RevocationList{list}, nil
	}
	var lists []*x509.RevocationList
	for len(bytes.TrimSpace(raw)) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil && len(lists) == 0 {
			list, err := x509.ParseRevocationList(bytes.TrimSpace(raw))
			if err == nil {
				return []*x509.RevocationList{list}, nil
			}
		}
		if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 {
			return nil, fmt.Errorf("invalid CRL PEM")
		}
		list, err := x509.ParseRevocationList(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid X.509 CRL")
		}
		lists = append(lists, list)
		raw = rest
	}
	if len(lists) == 0 {
		return nil, fmt.Errorf("empty CRL bundle")
	}
	return lists, nil
}

func certificateRoots(raw []byte, now time.Time) (*x509.CertPool, error) {
	certificates, err := pemCertificates(raw)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	for _, certificate := range certificates {
		if !certificate.IsCA {
			return nil, fmt.Errorf("trust bundle contains a non-CA certificate")
		}
		if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			continue
		}
		roots.AddCert(certificate)
	}
	if len(roots.Subjects()) == 0 {
		return nil, fmt.Errorf("trust bundle has no current CA")
	}
	return roots, nil
}

func certificateKeyPair(secret map[string]any, certKey, privateKey string) (tls.Certificate, []*x509.Certificate, error) {
	certPEM, err := kubernetesSecretBytes(secret, certKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyPEM, err := kubernetesSecretBytes(secret, privateKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("%s and %s do not form a valid key pair", certKey, privateKey)
	}
	chain, err := pemCertificates(certPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return pair, chain, nil
}

func validateRegistrationServerMaterial(secret map[string]any, dns string) error {
	return validateRegistrationServerMaterialAt(secret, dns, time.Now())
}

func validateRegistrationServerMaterialAt(secret map[string]any, dns string, now time.Time) error {
	if err := validateServiceRegistrationSecretData(secret); err != nil {
		return err
	}
	_, chain, err := certificateKeyPair(secret, "tls.crt", "tls.key")
	if err != nil {
		return err
	}
	leaf := chain[0]
	if leaf.IsCA || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 || leaf.VerifyHostname(dns) != nil {
		return fmt.Errorf("service registration server certificate is invalid for %s", dns)
	}
	serverAuth := false
	for _, purpose := range leaf.ExtKeyUsage {
		serverAuth = serverAuth || purpose == x509.ExtKeyUsageServerAuth
	}
	if !serverAuth {
		return fmt.Errorf("service registration server certificate lacks server authentication")
	}
	clientCA, err := kubernetesSecretBytes(secret, "client-ca.crt")
	if err != nil {
		return err
	}
	if _, err := certificateRoots(clientCA, now); err != nil {
		return fmt.Errorf("service registration client CA: %w", err)
	}
	crlPEM, err := kubernetesSecretBytes(secret, "client.crl")
	if err != nil {
		return err
	}
	lists, err := pemCRLs(crlPEM)
	if err != nil {
		return err
	}
	current := false
	for _, list := range lists {
		current = current || !now.Before(list.ThisUpdate) && now.Before(list.NextUpdate)
	}
	if !current {
		return fmt.Errorf("service registration client CRL is not current")
	}
	return nil
}

func validatePlatformServiceIdentityMaterial(identity, listener map[string]any, subject, serverDNS string, now time.Time) error {
	if err := validateRequiredKubernetesSecretData(identity, "platform service identity Secret", mqttFoundationIdentitySecretKeys); err != nil {
		return err
	}
	if err := validateRegistrationServerMaterialAt(listener, serverDNS, now); err != nil {
		return err
	}
	_, clientChain, err := certificateKeyPair(identity, "client.crt", "client.key")
	if err != nil {
		return err
	}
	client := clientChain[0]
	if client.IsCA || client.Subject.CommonName != subject || len(client.Subject.Names) != 1 || client.KeyUsage != x509.KeyUsageDigitalSignature || len(client.ExtKeyUsage) != 1 || client.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || len(client.DNSNames)+len(client.IPAddresses)+len(client.URIs)+len(client.EmailAddresses) != 0 {
		return fmt.Errorf("platform service client certificate has the wrong identity or purpose")
	}
	for _, extension := range client.Extensions {
		if extension.Id.String() == "2.5.29.17" {
			return fmt.Errorf("platform service client certificate contains a SAN extension")
		}
	}
	clientCA, err := kubernetesSecretBytes(listener, "client-ca.crt")
	if err != nil {
		return err
	}
	clientRoots, err := certificateRoots(clientCA, now)
	if err != nil {
		return err
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range clientChain[1:] {
		intermediates.AddCert(certificate)
	}
	verified, err := client.Verify(x509.VerifyOptions{Roots: clientRoots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		return fmt.Errorf("platform service client certificate is not signed by the listener's client CA")
	}
	crlPEM, err := kubernetesSecretBytes(listener, "client.crl")
	if err != nil {
		return err
	}
	lists, err := pemCRLs(crlPEM)
	if err != nil {
		return err
	}
	trustedAndUnrevoked := false
	for _, chain := range verified {
		if len(chain) < 2 {
			continue
		}
		var newest *x509.RevocationList
		for _, list := range lists {
			if !bytes.Equal(list.RawIssuer, chain[1].RawSubject) || list.CheckSignatureFrom(chain[1]) != nil || now.Before(list.ThisUpdate) || !now.Before(list.NextUpdate) {
				continue
			}
			if newest == nil || list.ThisUpdate.After(newest.ThisUpdate) || list.ThisUpdate.Equal(newest.ThisUpdate) && list.Number != nil && (newest.Number == nil || list.Number.Cmp(newest.Number) > 0) {
				newest = list
			}
		}
		if newest == nil {
			continue
		}
		revoked := false
		for _, entry := range newest.RevokedCertificateEntries {
			revoked = revoked || entry.SerialNumber.Cmp(client.SerialNumber) == 0
		}
		trustedAndUnrevoked = trustedAndUnrevoked || !revoked
	}
	if !trustedAndUnrevoked {
		return fmt.Errorf("platform service client certificate has no current issuer CRL or is revoked")
	}
	serverCA, err := kubernetesSecretBytes(identity, "server-ca.crt")
	if err != nil {
		return err
	}
	serverRoots, err := certificateRoots(serverCA, now)
	if err != nil {
		return fmt.Errorf("platform registry server CA: %w", err)
	}
	_, serverChain, err := certificateKeyPair(listener, "tls.crt", "tls.key")
	if err != nil {
		return err
	}
	serverIntermediates := x509.NewCertPool()
	for _, certificate := range serverChain[1:] {
		serverIntermediates.AddCert(certificate)
	}
	if _, err := serverChain[0].Verify(x509.VerifyOptions{Roots: serverRoots, Intermediates: serverIntermediates, DNSName: serverDNS, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return fmt.Errorf("platform registry server certificate is not trusted by the service identity")
	}
	return nil
}
