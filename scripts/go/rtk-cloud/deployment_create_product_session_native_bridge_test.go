package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProductIdentityExpiredSessionRunsNativeRenewalBridge(t *testing.T) {
	if os.Getenv("VIDEO_CLOUD_TEST_DSN") == "" {
		t.Skip("isolated PostgreSQL test runtime not configured")
	}
	f, original := productOpenBaoTrustFixture(t, 365*24*time.Hour)
	managed, err := bindProductIdentityOpenBaoTrust(f.l, original)
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]deploymentplan.Object{}
	for _, row := range []struct {
		name string
		defs []deploymentplan.Definition
	}{{"original", original}, {"managed", managed}} {
		for _, d := range row.defs {
			if d.Object["kind"] == "Job" {
				jobs[row.name] = d.Object
			}
		}
	}
	originalJob := jobs["original"]
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(originalJob)["containers"])[0])
	scope := productIdentityScope(f.l)
	sid, did, certPath := productSessionRenewalIdentity(f.l.ParentSHA256, scope, mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID"))
	source := productSessionRenewalSource{Schema: productSessionRenewalSchema, ParentSHA256: f.l.ParentSHA256, ScopeSHA256: scope, OriginalSession: productSessionInstallations{Session: mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID")}, SessionID: sid, DeploymentID: did, CertificatePath: certPath, KeySHA256: strings.Repeat("1", 64), CSRSHA256: strings.Repeat("2", 64)}
	renewal, err := productSessionRenewalJobDefinitions(f.cfg, f.l, source, originalJob)
	if err != nil {
		t.Fatal("actual renewal job rendering", err)
	}
	for _, d := range renewal {
		if d.Object["kind"] == "Job" {
			jobs["renewal"] = d.Object
		}
	}
	if jobs["renewal"] == nil {
		t.Fatal("actual renewal Job is absent")
	}
	oldKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := &x509.Certificate{Subject: f.root.Subject, SerialNumber: big.NewInt(707), NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	oldDER, err := x509.CreateCertificate(rand.Reader, oldRoot, oldRoot, oldKey.Public(), oldKey)
	if err != nil {
		t.Fatal(err)
	}
	oldCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: oldDER}))
	rootKey, _ := x509.MarshalPKCS8PrivateKey(f.rootKey)
	icaKey, _ := x509.MarshalPKCS8PrivateKey(f.icaKey)
	fixture := deploymentplan.Object{"jobs": jobs, "root": f.l.Root.Certificate, "intermediate": f.next.Certificate, "root_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rootKey})), "intermediate_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: icaKey})), "old_ca": oldCA}
	path := filepath.Join(t.TempDir(), "rendered-private-fixture.json")
	if err := os.WriteFile(path, mustProviderRecoveryJSON(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	runProductNativeRegression(t, f.cfg.Workspace, "cmd/serviceidentity-bootstrap", "TestNativeRenderedBootstrapExpiredProductSessionRenewal", "RTK_PRODUCT_BOOTSTRAP_RENDERED_FIXTURE="+path, "150s")
	t.Log("actual native expiry/renewal bridge: real isolated PostgreSQL, native bootstrap command, original same key/CSR, five original identities, genuine mTLS ACK/finalization; simulated provider signing and Kubernetes cleanup boundary")
}

// Run the maintained fixture in the exact selected product checkout without writing into it.
func runProductNativeRegression(t *testing.T, workspace, pkg, testName, fixtureEnv, timeout string) {
	t.Helper()
	repo := filepath.Join(workspace, "repos", "rtk_video_cloud")
	fixtures := filepath.Join(workspace, "scripts", "go", "rtk-cloud", "testdata", "native-product", filepath.FromSlash(pkg))
	files, err := os.ReadDir(fixtures)
	if err != nil || len(files) == 0 {
		t.Fatalf("native regression fixtures for %s unavailable: %v", pkg, err)
	}
	replacements := make(map[string]string, len(files))
	for _, file := range files {
		virtual := filepath.Join(repo, filepath.FromSlash(pkg), file.Name())
		if _, err := os.Lstat(virtual); !os.IsNotExist(err) {
			t.Fatalf("isolated native fixture would shadow product source %s", file.Name())
		}
		replacements[virtual] = filepath.Join(fixtures, file.Name())
	}
	overlay := filepath.Join(t.TempDir(), "overlay.json")
	raw, err := json.Marshal(map[string]map[string]string{"Replace": replacements})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, raw, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), filepath.Join(runtime.GOROOT(), "bin/go"), "test", "-overlay", overlay, "-vet=off", "./"+pkg, "-run", "^"+testName+"$", "-count=1", "-v", "-timeout="+timeout)
	command.Dir = repo
	command.Env = append(os.Environ(), "GOWORK=off", fixtureEnv)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native regression %s failed: %v; %s", testName, err, output)
	}
	if !strings.Contains(string(output), "--- PASS: "+testName) {
		t.Fatalf("native regression %s did not execute: %s", testName, output)
	}
}
