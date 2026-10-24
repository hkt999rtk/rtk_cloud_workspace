package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// A full static renderer reuses these local files, including its existing CA.
// Qualify exactly that desired material before any workload or route mutation;
// inspecting only the live Secret could approve material the renderer replaces.
func lkeRequireCertIssuerDesiredMaterial(ctx context.Context, paths provisionPaths, env map[string]string) error {
	runtime := newDeploymentCheckRuntime(ctx, "")
	return lkeRequireCertIssuerDesiredMaterialWithCommand(ctx, paths, env, func(_ io.Reader, args ...string) ([]byte, error) {
		return runtime.run(false, lkeKubectlArgs(args...)...)
	})
}

func lkeRequireCertIssuerDesiredMaterialWithCommand(ctx context.Context, paths provisionPaths, env map[string]string, command certIssuerIngressCommand) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	config := lkeResolveCertIssuerTLSConfig(env)
	if err := config.Validate(); err != nil {
		return err
	}
	deployment, err := certIssuerReadPublicObject(command, "deployment", config.Namespace, "certissuer")
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return secretCheckFailure(err, "cannot inspect existing CertIssuer deployment before qualifying desired TLS material")
	}
	if deployment == nil {
		return lkeRequireCertIssuerInitialMaterialWithContext(ctx, paths, env)
	}
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	var container map[string]any
	for _, item := range certIssuerObjectList(pod["containers"]) {
		entry := certIssuerObjectMap(item)
		if certIssuerObjectString(entry["name"]) == "certissuer" {
			container = entry
		}
	}
	if container == nil {
		return errors.New("existing CertIssuer has no identifiable serving container; cannot qualify static material")
	}
	settings := map[string]string{}
	for _, item := range certIssuerObjectList(container["env"]) {
		entry := certIssuerObjectMap(item)
		settings[certIssuerObjectString(entry["name"])] = certIssuerObjectString(entry["value"])
	}
	if settings["CERT_ISSUER_HOST_IDENTITY_STATE"] != "" {
		return errors.New("managed CertIssuer requires its owner identity lifecycle before a full static render")
	}
	certificates, err := certIssuerReadDesiredStaticMaterial(ctx, paths, env)
	if err != nil {
		return err
	}
	server, ca := certificates["server.crt"], certificates["service-ca.crt"]
	// Read only the selected public certificate keys. Private Secret data never
	// leaves Kubernetes, and local private keys never leave this process.
	liveServer, err := certIssuerReadMountedPublicChain(command, config, pod, container, settings["CERT_ISSUER_SERVER_CERT"])
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return secretCheckFailure(err, "cannot inspect the selected live CertIssuer public serving certificate")
	}
	if !bytes.Equal(server.Raw, liveServer[0].Raw) {
		return certIssuerDesiredMaterialFailure("server.crt", "persisted serving identity differs from the selected live certificate")
	}
	liveCA, err := certIssuerReadMountedPublicChain(command, config, pod, container, settings["CERT_ISSUER_CLIENT_CA"])
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	if err != nil {
		return secretCheckFailure(err, "cannot inspect the selected live CertIssuer public CA certificate")
	}
	if len(liveCA) != 1 || !bytes.Equal(ca.Raw, liveCA[0].Raw) {
		return certIssuerDesiredMaterialFailure("service-ca.crt", "persisted CA differs from the selected live trust material")
	}
	return ctx.Err()
}

// Initial bootstrap may generate a CA only when none of the renderer's owned
// files exists. Persisted state still must be complete and usable before reuse.
func lkeRequireCertIssuerInitialMaterial(paths provisionPaths, env map[string]string) error {
	return lkeRequireCertIssuerInitialMaterialWithContext(context.Background(), paths, env)
}

func lkeRequireCertIssuerInitialMaterialWithContext(ctx context.Context, paths provisionPaths, env map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := lkeResolveCertIssuerTLSConfig(env).Validate(); err != nil {
		return err
	}
	if _, err := lkeInternalTLSKeyAlgorithm(env); err != nil {
		return err
	}
	for _, name := range []string{"server.crt", "server.key", "service-ca.crt", "client.crt", "client.key", "factory.crt", "factory.key"} {
		_, err := os.Stat(sensitiveEnvironmentPath(paths, "certissuer", name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return certIssuerDesiredMaterialFailure(name, "cannot inspect persisted state")
		}
		_, err = certIssuerReadDesiredStaticMaterial(ctx, paths, env)
		return err
	}
	return ctx.Err()
}

func certIssuerReadDesiredStaticMaterial(ctx context.Context, paths provisionPaths, env map[string]string) (map[string]*x509.Certificate, error) {
	config := lkeResolveCertIssuerTLSConfig(env)
	if err := config.Validate(); err != nil {
		return nil, err
	}
	algorithm, err := lkeInternalTLSKeyAlgorithm(env)
	if err != nil {
		return nil, err
	}
	stateDir := sensitiveEnvironmentPath(paths, "certissuer")
	certificates := map[string]*x509.Certificate{}
	for _, name := range []string{"service-ca.crt", "server.crt", "client.crt", "factory.crt"} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(stateDir, name)
		actual, err := lkePEMCertificatePublicKeyAlgorithm(path)
		if err != nil {
			return nil, certIssuerDesiredMaterialFailure(name, "missing or invalid persisted certificate")
		}
		if actual != algorithm {
			return nil, certIssuerDesiredMaterialFailure(name, "key algorithm differs from desired configuration; deployment would replace the existing CA")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, certIssuerDesiredMaterialFailure(name, "cannot read persisted certificate")
		}
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
			return nil, certIssuerDesiredMaterialFailure(name, "invalid persisted certificate")
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, certIssuerDesiredMaterialFailure(name, "invalid persisted certificate")
		}
		certificates[name] = certificate
	}
	now := time.Now()
	ca := certificates["service-ca.crt"]
	if !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 || ca.CheckSignatureFrom(ca) != nil || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return nil, certIssuerDesiredMaterialFailure("service-ca.crt", "persisted CA is not a valid signing root")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, pair := range []struct {
		name  string
		usage x509.ExtKeyUsage
	}{{"server", x509.ExtKeyUsageServerAuth}, {"client", x509.ExtKeyUsageClientAuth}, {"factory", x509.ExtKeyUsageClientAuth}} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		keyName := pair.name + ".key"
		keyPath := filepath.Join(stateDir, keyName)
		actual, err := lkePEMPrivateKeyAlgorithm(keyPath)
		if err != nil {
			return nil, certIssuerDesiredMaterialFailure(keyName, "missing or invalid persisted private key")
		}
		if actual != algorithm {
			return nil, certIssuerDesiredMaterialFailure(keyName, "key algorithm differs from desired configuration; deployment would replace the existing CA")
		}
		key, err := readSensitiveFile(keyPath, "CertIssuer private key")
		if err != nil {
			return nil, certIssuerDesiredMaterialFailure(keyName, "cannot read persisted private key")
		}
		leaf := certificates[pair.name+".crt"]
		certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		if _, err := tls.X509KeyPair(certificatePEM, []byte(key)); err != nil {
			return nil, certIssuerDesiredMaterialFailure(keyName, "persisted certificate and private key do not match")
		}
		if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
			return nil, certIssuerDesiredMaterialFailure(pair.name+".crt", "persisted leaf is not a usable TLS identity")
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{pair.usage}}); err != nil {
			return nil, certIssuerDesiredMaterialFailure(pair.name+".crt", "persisted identity is expired, has the wrong TLS purpose, or does not verify against its persisted CA")
		}
	}
	server := certificates["server.crt"]
	for _, name := range config.ServerDNSNames() {
		if server.VerifyHostname(name) != nil {
			return nil, certIssuerDesiredMaterialFailure("server.crt", "persisted serving certificate omits a resolved public or internal DNS name")
		}
	}
	return certificates, ctx.Err()
}

func certIssuerDesiredMaterialFailure(name, cause string) error {
	return fmt.Errorf("CertIssuer desired TLS material %s: %s; restore matching persisted operator state or complete the approved identity migration before deployment", name, cause)
}
