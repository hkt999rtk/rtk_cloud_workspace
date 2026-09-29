package main

import (
	"bytes"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func certificateInspectionFixture(t *testing.T) (certificateCheckRunner, certificateCheckTarget, certificateCheckMaterial, lkePlatformCertificateFixture) {
	t.Helper()
	env := map[string]string{"CLOUD_STACK_NAME": "identity-test"}
	fixture := newLKEPlatformCertificateFixture(t, env)
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := kubernetesSecretBytes(fixture.listener, "client-ca.crt")
	if err := store.write("pki/root.crt", root, false); err != nil {
		t.Fatal(err)
	}
	cert, _ := kubernetesSecretBytes(fixture.identities["service:ota"], "client.crt")
	key, _ := kubernetesSecretBytes(fixture.identities["service:ota"], "client.key")
	target := certificateCheckTarget{ID: "ota", Source: "file", Subject: "service:ota", Purpose: "client", RootFile: "pki/root.crt", RootSHA256: certificateSHA256(fixture.issuer), CertFile: "pki/ota.crt", KeyFile: "pki/ota.key"}
	for file, value := range map[string][]byte{target.CertFile: cert, target.KeyFile: key} {
		if err := store.write(file, value, false); err != nil {
			t.Fatal(err)
		}
	}
	runner := certificateCheckRunner{store: store, stack: env["CLOUD_STACK_NAME"], now: fixture.now, warn: 30 * time.Minute, critical: 10 * time.Minute, checkedOwners: map[string]bool{}}
	return runner, target, certificateCheckMaterial{chain: string(cert), key: string(key)}, fixture
}

func TestCertificateCheckExpiryTrustKeyAndCRL(t *testing.T) {
	r, target, material, fixture := certificateInspectionFixture(t)
	cases := []struct {
		name   string
		at     time.Duration
		change func(*certificateCheckTarget, *certificateCheckMaterial)
		want   string
	}{
		{name: "healthy", want: "OK"},
		{name: "warning", at: 40 * time.Minute, want: "WARNING"},
		{name: "critical", at: 55 * time.Minute, want: "CRITICAL"},
		{name: "expired", at: 2 * time.Hour, want: "CRITICAL"},
		{name: "not yet valid", at: -2 * time.Hour, want: "CRITICAL"},
		{name: "wrong subject", change: func(target *certificateCheckTarget, _ *certificateCheckMaterial) { target.Subject = "service:shadow" }, want: "CRITICAL"},
		{name: "wrong purpose", change: func(target *certificateCheckTarget, _ *certificateCheckMaterial) { target.Purpose = "server" }, want: "CRITICAL"},
		{name: "root drift", change: func(target *certificateCheckTarget, _ *certificateCheckMaterial) {
			target.RootSHA256 = strings.Repeat("0", 64)
		}, want: "CRITICAL"},
		{name: "missing trust", change: func(target *certificateCheckTarget, _ *certificateCheckMaterial) { target.RootFile = "absent.crt" }, want: "UNKNOWN"},
		{name: "bad key", change: func(_ *certificateCheckTarget, m *certificateCheckMaterial) { m.key = "bad" }, want: "CRITICAL"},
		{name: "missing cert", change: func(_ *certificateCheckTarget, m *certificateCheckMaterial) { m.chain = "" }, want: "CRITICAL"},
		{name: "pending", change: func(_ *certificateCheckTarget, m *certificateCheckMaterial) { m.pending = true }, want: "WARNING"},
		{name: "owner subject mismatch", change: func(_ *certificateCheckTarget, m *certificateCheckMaterial) { m.subject = "service:shadow" }, want: "CRITICAL"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			copyRunner, copyTarget, copyMaterial := r, target, material
			copyRunner.now = r.now.Add(test.at)
			if test.change != nil {
				test.change(&copyTarget, &copyMaterial)
			}
			row := copyRunner.inspect(copyTarget, copyMaterial)
			if row.Status != test.want {
				t.Fatalf("status=%s findings=%v", row.Status, row.Findings)
			}
		})
	}
	target.CRLFile = "pki/clients.crl"
	goodCRL := lkeTestCRL(t, fixture.issuer, fixture.issuerKey, fixture.now, nil)
	_ = r.store.write(target.CRLFile, goodCRL, false)
	if row := r.inspect(target, material); row.Status != "OK" || row.Revocation != "checked" {
		t.Fatalf("valid CRL=%+v", row)
	}
	revoked := lkeTestCRL(t, fixture.issuer, fixture.issuerKey, fixture.now, []*big.Int{fixture.serials["service:ota"]})
	_ = r.store.write(target.CRLFile, revoked, true)
	if row := r.inspect(target, material); row.Status != "CRITICAL" || !strings.Contains(strings.Join(row.Findings, " "), "revoked") {
		t.Fatalf("revocation=%+v", row)
	}
	_ = r.store.write(target.CRLFile, []byte("invalid"), true)
	if row := r.inspect(target, material); row.Status != "UNKNOWN" {
		t.Fatalf("invalid CRL=%+v", row)
	}
	target.CRLFile = ""
	target.CompareTo = target.CertFile
	if row := r.inspect(target, material); row.Status != "OK" {
		t.Fatal(row)
	}
	other, _ := kubernetesSecretBytes(fixture.identities["service:shadow"], "client.crt")
	_ = r.store.write(target.CertFile, other, true)
	if row := r.inspect(target, material); row.Status != "CRITICAL" {
		t.Fatal("deployment drift was accepted")
	}
}

func TestCertificateCheckRuntimeAndOwnerCoverageAreReadOnly(t *testing.T) {
	r, target, material, fixture := certificateInspectionFixture(t)
	target.Source = "secret"
	target.Namespace = "video-cloud"
	target.Secret = "ota-identity"
	target.CertKey = "client.crt"
	target.KeyKey = "client.key"
	r.query = func(args ...string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), " get secret ") {
			t.Fatalf("unexpected query: %v", args)
		}
		return json.Marshal(fixture.identities["service:ota"])
	}
	if rows := r.check(target); len(rows) != 1 || rows[0].Status != "OK" {
		t.Fatal(rows)
	}
	r.query = func(...string) ([]byte, error) { return nil, nil }
	if rows := r.check(target); rows[0].Status != "CRITICAL" {
		t.Fatal("missing Secret passed")
	}
	r.query = func(...string) ([]byte, error) { return nil, errors.New("secret-sensitive-error") }
	if rows := r.check(target); rows[0].Status != "UNKNOWN" || strings.Contains(strings.Join(rows[0].Findings, " "), "sensitive") {
		t.Fatal(rows)
	}
	r.localOnly = true
	r.query = func(...string) ([]byte, error) { t.Fatal("local-only reached cluster"); return nil, nil }
	if rows := r.check(target); rows[0].Status != "UNKNOWN" {
		t.Fatal(rows)
	}
	r.localOnly = false
	target.Source = "managed"
	target.Selector = "app=ota"
	target.Container = "owner"
	target.StateFile = "/var/lib/identity.json"
	pods := []byte(`{"items":[{"metadata":{"name":"ota-1"},"status":{"phase":"Running"},"spec":{"containers":[{"name":"owner","env":[{"name":"OTA_IDENTITY_STATE","value":"/var/lib/identity.json"}]}]}}]}`)
	r.query = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, " get pods"):
			return pods, nil
		case strings.Contains(joined, " exec ota-1 -c owner -- /usr/local/bin/pkitrust inspect-identity /var/lib/identity.json"):
			return json.Marshal(map[string]any{"version": 1, "subject": target.Subject, "certificate_chain_pem": material.chain, "key_matches": true})
		default:
			t.Fatalf("unexpected mutation or query: %v", args)
			return nil, nil
		}
	}
	if rows := r.check(target); rows[0].Status != "OK" {
		t.Fatal(rows)
	}
	if rows := r.checkOwnerCoverage(map[string]bool{r.stack + "-video-cloud": true}); len(rows) != 0 {
		t.Fatal(rows)
	}
	r.checkedOwners = map[string]bool{}
	if rows := r.checkOwnerCoverage(map[string]bool{r.stack + "-video-cloud": true}); len(rows) != 1 || rows[0].Status != "UNKNOWN" {
		t.Fatal("uncovered managed identity appeared healthy")
	}
}

func TestCertificateCheckLocalInventoryReportsAndNoMutation(t *testing.T) {
	r, target, material, _ := certificateInspectionFixture(t)
	before := readTestFile(t, filepath.Join(r.store.Root, target.KeyFile))
	if rows := r.check(target); rows[0].Status != "OK" {
		t.Fatal(rows)
	}
	target.Source = "deployment"
	target.RecordFile = "pki/services/ota/identity.json"
	target.ServiceRoot = true
	issuer := deploymentServiceIssuer{Environment: r.store.Environment, Stack: r.stack, RootCAFile: target.RootFile, RootSHA256: target.RootSHA256}
	raw, _ := json.Marshal(issuer)
	_ = r.store.write("pki/services/issuer.json", raw, false)
	record := deploymentServiceIdentity{Version: 1, Environment: r.store.Environment, Stack: r.stack, Subject: target.Subject, PrivateKey: material.key, CertificateChain: material.chain}
	if err := writeDeploymentServiceIdentity(r.store, target.RecordFile, record); err != nil {
		t.Fatal(err)
	}
	if rows := r.check(target); rows[0].Status != "OK" {
		t.Fatal(rows)
	}
	r.localOnly = true
	report := r.run([]certificateCheckTarget{target, {ID: "disabled", Disabled: true}}, nil)
	if certificateCheckExit(report) != 0 {
		t.Fatal(report)
	}
	for _, format := range []string{"table", "json"} {
		var out bytes.Buffer
		if err := writeCertificateCheckReport(&out, report, format); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "BEGIN") || strings.Contains(out.String(), material.key) || !strings.Contains(out.String(), "ota") {
			t.Fatal("report leaks private input or omits identity")
		}
	}
	for status, code := range map[string]int{"OK": 0, "WARNING": 1, "CRITICAL": 2, "UNKNOWN": 3, "DISABLED": 3} {
		if got := certificateCheckExit(certificateCheckReport{Rows: []certificateCheckRow{{Status: status}}}); got != code {
			t.Fatalf("%s exit=%d", status, got)
		}
	}
	if readTestFile(t, filepath.Join(r.store.Root, target.KeyFile)) != before {
		t.Fatal("inspection mutated key")
	}
	if err := os.Chmod(filepath.Join(r.store.Root, target.RecordFile), 0644); err != nil {
		t.Fatal(err)
	}
	if rows := r.check(target); rows[0].Status != "UNKNOWN" {
		t.Fatal("unsafe state accepted")
	}
	info, _ := os.Stat(filepath.Join(r.store.Root, target.RecordFile))
	if info.Mode().Perm() != 0644 {
		t.Fatal("check repaired permissions")
	}
}

func TestCertificateCheckInventoryOverrideValidationAndCLI(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	r, target, _, _ := certificateInspectionFixture(t)
	writeInventory := func(path string, targets []certificateCheckTarget) {
		t.Helper()
		raw, _ := json.Marshal(certificateCheckInventory{Version: 1, Targets: targets})
		writeTestFile(t, path, string(raw))
	}
	base := filepath.Join(workspace, "cloud_deploy/certificate-check.json")
	override := filepath.Join(workspace, "cloud_env/dev/certificate-check.json")
	writeInventory(base, []certificateCheckTarget{target})
	got, err := loadCertificateCheckInventory(workspace, "dev")
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	changed := target
	changed.Subject = "service:shadow"
	writeInventory(override, []certificateCheckTarget{changed})
	got, err = loadCertificateCheckInventory(workspace, "dev")
	if err != nil || got[0].Subject != changed.Subject {
		t.Fatal("override ignored")
	}
	writeInventory(override, []certificateCheckTarget{target, target})
	if _, err := loadCertificateCheckInventory(workspace, "dev"); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	changed = target
	changed.Purpose = "typo"
	writeInventory(override, []certificateCheckTarget{changed})
	if _, err := loadCertificateCheckInventory(workspace, "dev"); err == nil {
		t.Fatal("bad purpose accepted")
	}
	writeInventory(override, []certificateCheckTarget{target})
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", r.store.ConfigRoot)
	var result error
	out := captureStdout(t, func() {
		result = runDeploymentWithOperations([]string{"certificate-check", "--environment", "dev", "--workspace", workspace, "--local-only", "--format", "json"}, deploymentOperations{})
	})
	// One-hour fixture is critically expiring under the operator defaults.
	var code exitCode
	if !errors.As(result, &code) || int(code) != 2 {
		t.Fatalf("CLI exit=%v", result)
	}
	var report certificateCheckReport
	if json.Unmarshal([]byte(out), &report) != nil || report.Scope != "local-only" || report.Environment != "dev" {
		t.Fatal("invalid CLI report")
	}
	if err := runDeploymentCertificateCheck([]string{"--environment", "dev", "--warn-days", "1", "--critical-days", "3"}); !errors.As(err, &code) || int(code) != 3 {
		t.Fatalf("invalid options=%v", err)
	}
	root, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		if targets, err := loadCertificateCheckInventory(root, environment); err != nil || len(targets) < 20 {
			t.Fatalf("%s inventory=%d err=%v", environment, len(targets), err)
		}
	}
}

func TestCertificateCheckInitialExpiryDoesNotMisreportRenewedOwner(t *testing.T) {
	r, initial, material, fixture := certificateInspectionFixture(t)
	r.now = fixture.now.Add(2 * time.Hour)
	initial.Source = "deployment"
	initial.RecordFile = "pki/services/ota/identity.json"
	initial.SupersededBy = "ota-current"
	record := deploymentServiceIdentity{Version: 1, Environment: r.store.Environment, Stack: r.stack, Subject: initial.Subject, PrivateKey: material.key, CertificateChain: material.chain}
	if err := writeDeploymentServiceIdentity(r.store, initial.RecordFile, record); err != nil {
		t.Fatal(err)
	}
	current := initial
	current.ID = "ota-current"
	current.Source = "managed"
	current.SupersededBy = ""
	current.Namespace = "video-cloud"
	current.Selector = "app=ota"
	current.Container = "owner"
	current.StateFile = "/identity/state.json"
	// The new current leaf retains the exact service subject and client purpose.
	_, newCert := lkeTestLeaf(t, fixture.issuer, fixture.issuerKey, big.NewInt(100), pkix.Name{CommonName: initial.Subject}, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, r.now)
	r.query = func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " exec ") {
			return json.Marshal(map[string]any{"version": 1, "subject": initial.Subject, "certificate_chain_pem": string(newCert), "key_matches": true})
		}
		if strings.Contains(joined, "-l app=ota") {
			return []byte(`{"items":[{"metadata":{"name":"ota-1"},"status":{"phase":"Running"}}]}`), nil
		}
		return []byte(`{"items":[]}`), nil
	}
	report := r.run([]certificateCheckTarget{initial, current}, nil)
	if certificateCheckExit(report) != 0 {
		t.Fatalf("renewed owner misreported: %+v", report.Rows)
	}
	for _, row := range report.Rows {
		if row.ID == initial.ID && row.Status != "INFO" {
			t.Fatalf("initial expiry=%+v", row)
		}
	}
	current.EnabledBy = "UNKNOWN_FEATURE"
	report = r.run([]certificateCheckTarget{initial, current}, nil)
	if certificateCheckExit(report) != 3 {
		t.Fatal("unconfigured feature flag silently disabled inspection")
	}
}
