package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFactoryClientCertificateLifecycle(t *testing.T) {
	configRoot := t.TempDir()
	dir := filepath.Join(configRoot, "dev", "pki", "factory-client-ca")
	if err := run([]string{"init", "--environment", "dev", "--config-root", configRoot}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"init", "--environment", "dev", "--config-root", configRoot}); err == nil {
		t.Fatal("CA was replaced")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "fixture"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPath := filepath.Join(configRoot, "factory.csr")
	if err := os.WriteFile(csrPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(configRoot, "factory.crt")
	cloudID := "1b276960-4e2b-4d64-9422-c2dba64107e6"
	if err := run([]string{"sign", "--environment", "dev", "--config-root", configRoot, "--csr", csrPath, "--cloud-id", cloudID, "--factory-id", "line-a", "--out", certPath}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://rtk-cloud/factory/"+cloudID+"/line-a" {
		t.Fatalf("wrong factory identity: %v", cert.URIs)
	}
	ca, _, err := loadCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: rootPool(ca), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"revoke", "--environment", "dev", "--config-root", configRoot, "--serial", cert.SerialNumber.Text(16)}); err != nil {
		t.Fatal(err)
	}
	crlRaw, err := os.ReadFile(filepath.Join(dir, "ca.crl"))
	if err != nil {
		t.Fatal(err)
	}
	crlBlock, _ := pem.Decode(crlRaw)
	crl, err := x509.ParseRevocationList(crlBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(crl.RevokedCertificateEntries) != 1 || crl.RevokedCertificateEntries[0].SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Fatalf("revocation missing: %#v", crl.RevokedCertificateEntries)
	}
}

func TestFactoryClientCAPublishUsesSelectedIngressNamespace(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dev", "pki", "factory-client-ca")
	if err := initCA(dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if err := publishCA(filepath.Join(root, "dev"), dir, "bad_stack"); err == nil {
		t.Fatal("invalid stack accepted")
	}
	if err := publishCA(filepath.Join(root, "dev"), dir, "rtk-dev"); err == nil {
		t.Fatal("missing kubeconfig accepted")
	}
	kubeconfig := filepath.Join(root, "dev", "kube", "kubeconfig.yaml")
	if err := os.MkdirAll(filepath.Dir(kubeconfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfig, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "applied.json")
	t.Setenv("FAKE_KUBECTL_OUTPUT", output)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\ncase \"$3\" in\n  get) test \"$FAKE_KUBECTL_MODE\" != get-fail && test \"$4\" = namespace && test \"$5\" = rtk-dev-ingress ;;\n  apply) test \"$FAKE_KUBECTL_MODE\" != apply-fail && cat > \"$FAKE_KUBECTL_OUTPUT\" ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishCA(filepath.Join(root, "dev"), dir, "rtk-dev"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var secret struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &secret); err != nil {
		t.Fatal(err)
	}
	if secret.Metadata.Name != "rtk-dev-factory-client-ca" || secret.Metadata.Namespace != "rtk-dev-ingress" {
		t.Fatalf("wrong secret target: %+v", secret.Metadata)
	}
	if len(secret.Data) != 2 || secret.Data["ca.key"] != "" {
		t.Fatalf("unexpected secret keys: %v", secret.Data)
	}
	for _, name := range []string{"ca.crt", "ca.crl"} {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := base64.StdEncoding.DecodeString(secret.Data[name])
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s not published intact", name)
		}
	}
	for _, mode := range []string{"get-fail", "apply-fail"} {
		t.Setenv("FAKE_KUBECTL_MODE", mode)
		if err := publishCA(filepath.Join(root, "dev"), dir, "rtk-dev"); err == nil {
			t.Fatalf("%s accepted", mode)
		}
	}
	t.Setenv("FAKE_KUBECTL_MODE", "")
	for _, name := range []string{"ca.crt", "ca.crl"} {
		path := filepath.Join(dir, name)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := publishCA(filepath.Join(root, "dev"), dir, "rtk-dev"); err == nil {
			t.Fatalf("missing %s accepted", name)
		}
		if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := publishCA(filepath.Join(root, "dev"), dir, "rtk-dev"); err == nil {
			t.Fatalf("broken %s accepted", name)
		}
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFactoryClientCAPublishRejectsStaleOrUnrelatedCRL(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dev", "pki", "factory-client-ca")
	other := filepath.Join(root, "other", "pki", "factory-client-ca")
	for _, item := range []struct{ dir, environment string }{{dir, "dev"}, {other, "other"}} {
		if err := initCA(item.dir, item.environment); err != nil {
			t.Fatal(err)
		}
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	crl, err := os.ReadFile(filepath.Join(dir, "ca.crl"))
	if err != nil {
		t.Fatal(err)
	}
	otherCRL, err := os.ReadFile(filepath.Join(other, "ca.crl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePublishTrust(ca, crl, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		ca, crl []byte
		at      time.Time
	}{
		{"expired", ca, crl, time.Now().Add(8 * 24 * time.Hour)},
		{"unrelated", ca, otherCRL, time.Now().UTC()},
		{"malformed", ca, []byte("not PEM"), time.Now().UTC()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validatePublishTrust(tc.ca, tc.crl, tc.at); err == nil {
				t.Fatal("invalid trust accepted")
			}
		})
	}
}

func TestFactoryClientCertificateRejectsUnsafeIssuance(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dev", "pki", "factory-client-ca")
	if err := run([]string{"init", "--environment", "dev", "--config-root", root}); err != nil {
		t.Fatal(err)
	}
	csr := filepath.Join(root, "bad.csr")
	if err := os.WriteFile(csr, []byte("not a CSR"), 0o600); err != nil {
		t.Fatal(err)
	}
	cloud := "1b276960-4e2b-4d64-9422-c2dba64107e6"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing command", nil},
		{"unknown command", []string{"unknown", "--environment", "dev", "--config-root", root}},
		{"invalid environment", []string{"sign", "--environment", "../prod", "--config-root", root}},
		{"invalid cloud", []string{"sign", "--environment", "dev", "--config-root", root, "--csr", csr, "--cloud-id", "other", "--factory-id", "line-a", "--out", filepath.Join(root, "out.crt")}},
		{"invalid factory", []string{"sign", "--environment", "dev", "--config-root", root, "--csr", csr, "--cloud-id", cloud, "--factory-id", "../other", "--out", filepath.Join(root, "out.crt")}},
		{"CA overwrite", []string{"sign", "--environment", "dev", "--config-root", root, "--csr", csr, "--cloud-id", cloud, "--factory-id", "line-a", "--out", filepath.Join(dir, "ca.key")}},
		{"invalid CSR", []string{"sign", "--environment", "dev", "--config-root", root, "--csr", csr, "--cloud-id", cloud, "--factory-id", "line-a", "--out", filepath.Join(root, "out.crt")}},
		{"invalid serial", []string{"revoke", "--environment", "dev", "--config-root", root, "--serial", "invalid"}},
		{"unknown serial", []string{"revoke", "--environment", "dev", "--config-root", root, "--serial", "abc123"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := run(tc.args); err == nil {
				t.Fatal("unsafe request accepted")
			}
		})
	}
	if err := run([]string{"refresh-crl", "--environment", "dev", "--config-root", root}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), []byte("broken JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"refresh-crl", "--environment", "dev", "--config-root", root}); err == nil {
		t.Fatal("corrupt registry accepted")
	}
}

func TestFactoryClientCertificateRejectsCorruptTrustAndRegistry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dev", "pki", "factory-client-ca")
	if err := initCA(dir, "dev"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca.crt", "ca.key"} {
		path := filepath.Join(dir, name)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, content := range [][]byte{nil, []byte("broken")} {
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadCA(dir); err == nil {
				t.Fatalf("invalid %s accepted", name)
			}
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadCA(dir); err == nil {
			t.Fatalf("missing %s accepted", name)
		}
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), []byte(`[{"serial":"not-hex","revoked_at":"2026-09-25T00:00:00Z"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := loadRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateCRL(dir, registry); err == nil {
		t.Fatal("invalid revoked serial accepted")
	}
	if err := os.Remove(filepath.Join(dir, "registry.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRegistry(dir); err == nil {
		t.Fatal("missing registry accepted")
	}
}

func rootPool(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}
