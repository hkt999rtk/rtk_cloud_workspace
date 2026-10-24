package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductIdentityRenderedManagementEnrollmentUsesCurrentRoot(t *testing.T) {
	if os.Getenv("VIDEO_CLOUD_TEST_DSN") == "" {
		t.Skip("isolated PostgreSQL runtime not configured")
	}
	f := newProductProtocolFixture(t, true)
	oldKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := &x509.Certificate{SerialNumber: big.NewInt(889), Subject: pkix.Name{CommonName: "expired-create-management"}, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, old, old, oldKey.Public(), oldKey)
	if err != nil {
		t.Fatal(err)
	}
	historical := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	f.runtime.TransportCAPEM = historical
	f.runtime.TransportRootSHA256 = environmentPreflightBytesSHA256(der)
	sid := "55555555-5555-4555-8555-555555555555"
	ns := f.p.Stack + "-video-cloud"
	service := environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.next, BootstrapSessionID: sid, BootstrapCredentialSecret: "service-bootstrap-" + sid, BootstrapCredentialNamespace: ns, BootstrapCredentialNamespaces: []string{ns}, RootCRLPEM: "native synthetic root CRL", RootCRLSecret: "pki-service-root-crl"}
	settings := []map[string]string{}
	for _, product := range f.l.Outputs {
		definition, err := productIdentityEnrollmentOwner(f.cfg, f.p, f.runtime, service, product)
		if err != nil {
			t.Fatal(err)
		}
		pod := environmentPKIPod(definition.Object)
		c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
		env := map[string]string{"TEST_SUBJECT": product.Subject}
		for _, value := range certIssuerObjectList(c["env"]) {
			e := deploymentplan.Map(value)
			if s, ok := e["value"].(string); ok {
				env[deploymentplan.String(e["name"])] = s
			}
		}
		if env["PKI_CLIENT_ISSUER_TLS_CA"] != "/run/pki-service/root.pem" || env["PKI_CLIENT_ISSUER_SERVER_PKI_ROOT_SHA256"] != service.Root.RootFingerprint || env["PKI_CLIENT_ROOT_SHA256"] != service.Root.RootFingerprint {
			t.Fatal("late product inherited expired temporary management trust")
		}
		settings = append(settings, env)
	}
	path := filepath.Join(t.TempDir(), "rendered-native-management.json")
	if err := os.WriteFile(path, mustProviderRecoveryJSON(deploymentplan.Object{"Environment": f.cfg.Environment, "HistoricalTransport": historical, "Settings": settings}), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"test"}
	if overlay := os.Getenv("RTK_PRODUCT_MANAGEMENT_NATIVE_OVERLAY"); overlay != "" {
		args = append(args, "-overlay="+overlay)
	}
	args = append(args, "./internal/pkitrust", "-run", "^TestProductManagementEnrollmentUsesCurrentServiceRootAndSameBootstrap$", "-count=1", "-v", "-timeout=90s")
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = filepath.Join(f.cfg.Workspace, "repos/rtk_video_cloud")
	command.Env = append(os.Environ(), "GOWORK=off", "RTK_PRODUCT_MANAGEMENT_TLS_FIXTURE="+path)
	raw, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native management enrollment boundary failed: %v; %s", err, raw)
	}
	if !strings.Contains(string(raw), "--- PASS: TestProductManagementEnrollmentUsesCurrentServiceRootAndSameBootstrap") {
		t.Fatal("native management TLS regression did not execute")
	}
	t.Log("maintained5 recipes with expired historical CA: native current ServiceRoot enrollment and ACK passed, original bootstrap reused")
}
