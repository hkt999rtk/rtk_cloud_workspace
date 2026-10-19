package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func accountRegistrationFixture(t *testing.T) (deploymentConfig, environmentCreateBootstrapPlan, environmentCreateServiceCeremony, string, string, string, []deploymentplan.Definition) {
	t.Helper()
	cfg, p, runtime, s := createServiceRecipeFixture(t, "dev")
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "9444"
	definitions, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, s)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-registration-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := x509.ParseCertificate(rootDER)
	intermediateKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	intermediateTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "fixture-registration-intermediate"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(180 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, root, &intermediateKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediate, _ := x509.ParseCertificate(intermediateDER)
	pemCertificate := func(raw []byte) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
	}
	s.Root.Certificate = pemCertificate(rootDER)
	s.Root.RootFingerprint = environmentPreflightBytesSHA256(rootDER)
	s.Intermediate.Certificate = pemCertificate(intermediateDER)
	s.Intermediate.RootFingerprint = s.Root.RootFingerprint
	name := "account-manager." + p.Stack + "-account-manager.svc.cluster.local"
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: now, NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, intermediate, &serverKey.PublicKey, intermediateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(serverKey)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	makeCRL := func(authority *x509.Certificate, key *ecdsa.PrivateKey, number int64) string {
		raw, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(number), ThisUpdate: now, NextUpdate: now.Add(time.Hour)}, authority, key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: raw}))
	}
	s.RootCRLPEM = makeCRL(root, rootKey, 1)
	return cfg, p, s, pemCertificate(leafDER) + pemCertificate(intermediateDER) + pemCertificate(rootDER), keyPEM, makeCRL(intermediate, intermediateKey, 4), definitions
}

func TestCreateAccountRegistrationUsesOriginalServerAndSignedRevocationBundle(t *testing.T) {
	cfg, p, s, certificate, key, crl, definitions := accountRegistrationFixture(t)
	out, err := renderEnvironmentCreateAccountRegistration(cfg, p, s, certificate, key, crl, definitions)
	if err != nil {
		t.Fatal(err)
	}
	secret := createRecipeObject(t, out, "Secret/"+p.Stack+"-account-manager/account-manager-service-registration-tls")
	data := deploymentplan.Map(secret["stringData"])
	if data["tls.crt"] != certificate || data["tls.key"] != key || data["client-ca.crt"] != s.Root.Certificate || !strings.Contains(deploymentplan.String(data["client.crl"]), strings.TrimSpace(crl)) {
		t.Fatal("registration replaced original server material or lost current signed revocation evidence")
	}
	account := createRecipeObject(t, out, "Deployment/"+p.Stack+"-account-manager/account-manager")
	pod := environmentPKIPod(account)
	main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	env := createRecipeEnv(main)
	if env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] != "9444" || env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL"] != "/etc/rtk-account-manager/service-registration/client.crl" {
		t.Fatal("maintained registration was left disabled after bootstrap")
	}
	sidecar := deploymentplan.Map(certIssuerObjectList(pod["containers"])[1])
	if createRecipeEnv(sidecar)["PKI_MANAGEMENT_ACCOUNT_LISTEN_ADDR"] != ":18443" {
		t.Fatal("registration displaced the original managed Account listener")
	}
	ports := map[string]float64{}
	service := createRecipeObject(t, out, "Service/"+p.Stack+"-account-manager/account-manager")
	for _, raw := range certIssuerObjectList(deploymentplan.Map(service["spec"])["ports"]) {
		v := deploymentplan.Map(raw)
		ports[deploymentplan.String(v["name"])], _ = v["port"].(float64)
	}
	if ports["service-reg"] != 8443 || ports["service-mtls"] != 18443 {
		t.Fatal("registration and managed API collided", ports)
	}
}

func TestCreateAccountRegistrationRejectsForeignOrUnsignedInputs(t *testing.T) {
	for _, mode := range []string{"key-mismatch", "foreign-root", "unsigned-crl", "wrong-crl-authority", "inactive-issuer", "port-collision", "missing-owner"} {
		t.Run(mode, func(t *testing.T) {
			cfg, p, s, certificate, key, crl, definitions := accountRegistrationFixture(t)
			switch mode {
			case "key-mismatch":
				key = "missing-original-key"
			case "foreign-root":
				s.Root.RootFingerprint = strings.Repeat("0", 64)
			case "unsigned-crl":
				crl = "no-native-revocation-publication"
			case "wrong-crl-authority":
				crl = s.RootCRLPEM
			case "inactive-issuer":
				s.Intermediate.Status = "ready"
			case "port-collision":
				cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "18443"
			case "missing-owner":
				definitions = nil
			}
			if _, err := renderEnvironmentCreateAccountRegistration(cfg, p, s, certificate, key, crl, definitions); err == nil {
				t.Fatal("registration published unqualified identity or omitted its maintained owner")
			}
		})
	}
}
