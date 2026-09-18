package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rolloutWrite(t *testing.T, path, contents string, mode os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func rolloutTLSFixture(t *testing.T, expiry time.Time, purpose x509.ExtKeyUsage) rolloutTLSOptions {
	t.Helper()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: expiry, DNSNames: []string{"controller.example.test"}, ExtKeyUsage: []x509.ExtKeyUsage{purpose}, KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return rolloutTLSOptions{
		cert:     rolloutWrite(t, filepath.Join(dir, "chain.pem"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})), 0600),
		ca:       rolloutWrite(t, filepath.Join(dir, "ca.pem"), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})), 0600),
		key:      rolloutWrite(t, filepath.Join(dir, "key.pem"), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), 0600),
		hostname: "controller.example.test", purpose: "server", minDays: 7,
	}
}

func TestRolloutTLSQualification(t *testing.T) {
	for _, name := range []string{"valid", "wrong DNS", "wrong purpose", "expiring", "expired", "key mismatch", "untrusted CA", "insecure key", "client"} {
		t.Run(name, func(t *testing.T) {
			expiry := time.Now().Add(30 * 24 * time.Hour)
			if name == "expiring" {
				expiry = time.Now().Add(24 * time.Hour)
			}
			if name == "expired" {
				expiry = time.Now().Add(-time.Minute)
			}
			purpose := x509.ExtKeyUsageServerAuth
			if name == "client" {
				purpose = x509.ExtKeyUsageClientAuth
			}
			opts := rolloutTLSFixture(t, expiry, purpose)
			switch name {
			case "wrong DNS":
				opts.hostname = "other.example.test"
			case "wrong purpose":
				opts.purpose = "client"
			case "key mismatch":
				opts.key = rolloutTLSFixture(t, expiry, purpose).key
			case "untrusted CA":
				opts.ca = rolloutTLSFixture(t, expiry, purpose).ca
			case "insecure key":
				if err := os.Chmod(opts.key, 0644); err != nil {
					t.Fatal(err)
				}
			case "client":
				opts.purpose = "client"
				opts.hostname = ""
			}
			check := checkRolloutTLS(opts)
			if check.Passed != (name == "valid" || name == "client") {
				t.Fatalf("unexpected result: %+v", check)
			}
		})
	}
}

func TestRolloutSecretMountQualification(t *testing.T) {
	for _, tc := range []struct {
		name, security, container, volume string
		pass                              bool
	}{
		{"missing fsGroup", `"runAsUser":10001`, ``, `"defaultMode":288`, false},
		{"group readable", `"runAsUser":10001,"fsGroup":10001`, ``, `"defaultMode":288`, true},
		{"root owner", `"runAsUser":0`, ``, `"defaultMode":256`, true},
		{"container overrides root", `"runAsUser":0`, `"securityContext":{"runAsUser":10001},`, `"defaultMode":256`, false},
		{"item override", `"runAsUser":10001,"fsGroup":10001`, ``, `"defaultMode":288,"items":[{"key":"tls.key","path":"tls.key","mode":0}]`, false},
		{"invalid mode", `"runAsUser":0`, ``, `"defaultMode":1024`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"spec":{"template":{"spec":{"securityContext":{%s},"containers":[{%s"volumeMounts":[{"name":"pki"}]}],"volumes":[{"name":"pki","secret":{%s}}]}}}}`, tc.security, tc.container, tc.volume)
			path := rolloutWrite(t, filepath.Join(t.TempDir(), "workload.json"), raw, 0600)
			if check := checkRolloutMounts(path); check.Passed != tc.pass {
				t.Fatalf("unexpected result: %+v", check)
			}
		})
	}
	for _, raw := range []string{`{}`, `not json`, `{"containers":[]}`, `{"containers":[{"volumeMounts":[{"name":"absent"}]}]}`, `{"containers":[],"volumes":[{"projected":{"sources":[]}}]}`} {
		path := rolloutWrite(t, filepath.Join(t.TempDir(), "bad.json"), raw, 0600)
		if check := checkRolloutMounts(path); check.Passed {
			t.Fatalf("accepted incomplete workload: %s", raw)
		}
	}
}

func TestRolloutReadOnlyDNSDoesNotWrite(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	backend := newDeploymentCredentialTestServer(t, deploymentCredentialTestServerOptions{})
	defer backend.Close()
	client := backend.Client()
	transport := client.Transport
	client.Transport = rolloutRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Errorf("read-only request attempted %s", r.Method)
		}
		return transport.RoundTrip(r)
	})
	var out bytes.Buffer
	checker := deploymentCredentialChecker{client: client, out: &out, goDaddyAPIRoot: backend.URL}
	options := deploymentCredentialCheckOptions{readOnly: true, selected: keySet("dns")}
	if err := checker.checkWithOptions(testDeploymentCredentialConfig(), writeDeploymentCredentialEnv(t, backend.URL, "token"), options); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "writes not tested") {
		t.Fatal(out.String())
	}
}

type rolloutRoundTrip func(*http.Request) (*http.Response, error)

func (f rolloutRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRolloutReadOnlyStorageLeavesNoReceipt(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("attempted write %s", r.Method)
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/v4/regions/sg-sin-2":
			fmt.Fprint(w, `{"id":"sg-sin-2","status":"ok","capabilities":["Kubernetes","Object Storage"]}`)
		case "/v4/object-storage/buckets":
			fmt.Fprintf(w, `{"data":[{"label":"media","region":"sg-sin-2","s3_endpoint":%q}]}`, server.URL)
		case "/v4/object-storage/keys":
			fmt.Fprint(w, `{"data":[{"id":42,"access_key":"access","bucket_access":[{"bucket_name":"media","region":"sg-sin-2","permissions":"read_write"}]}]}`)
		case "/media":
			fmt.Fprint(w, `<ListBucketResult/>`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	target := deploymentStorageTarget{Purpose: "runtime-media", Policy: "colocated", Bucket: "media", Region: "sg-sin-2", Prefix: "environments/staging"}
	cfg := deploymentConfig{RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{RuntimeMedia: target, ReleaseArtifacts: target}}
	values := map[string]string{"LINODE_TOKEN": "token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "secret", "LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID": "access", "LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY": "secret"}
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4", readOnly: true}
	for _, check := range []deploymentCredentialCheck{checker.checkResolvedObjectStorage(cfg, values), checker.checkResolvedArtifactStorage(cfg, values)} {
		if !check.Passed {
			t.Fatal(check.Detail)
		}
	}
	entries, err := os.ReadDir(cfg.RuntimeRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only check wrote receipt: %v %v", entries, err)
	}
}

func TestRolloutPullIsolatedAndRedacted(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "config-path")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ROLLOUT_TEST_RECORD", record)
	rolloutWrite(t, filepath.Join(dir, "docker"), `#!/bin/sh
[ "$1" = --config ] && [ "$3" = pull ] && [ "$4" = --platform ] && [ "$5" = linux/amd64 ] || exit 2
printf '%s' "$2" > "$ROLLOUT_TEST_RECORD"
/usr/bin/grep -q 'dXNlcjp0b2tlbg==' "$2/config.json" || exit 3
printf 'token must not appear in errors' >&2
exit "${ROLLOUT_TEST_EXIT:-0}"
`, 0700)
	image := "ghcr.io/owner/repo@sha256:" + strings.Repeat("a", 64)
	for _, exitCode := range []string{"0", "1"} {
		t.Setenv("ROLLOUT_TEST_EXIT", exitCode)
		err := pullRolloutImage("user", "token", image)
		if (err == nil) != (exitCode == "0") {
			t.Fatalf("unexpected result %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "token") {
			t.Fatalf("raw diagnostic leaked: %v", err)
		}
		path, readErr := os.ReadFile(record)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
			t.Fatal("temporary credentials not removed")
		}
	}
}

func TestRolloutInvalidRegistryCredentialNeverPulls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, token, ok := r.BasicAuth()
		if !ok || user != "selected-user" || token != "bad-token" {
			t.Error("wrong selected identity")
		}
		http.Error(w, "sensitive response", 401)
	}))
	defer server.Close()
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	t.Setenv("PATH", dir)
	t.Setenv("ROLLOUT_TEST_MARKER", marker)
	rolloutWrite(t, filepath.Join(dir, "docker"), "#!/bin/sh\nprintf called > \"$ROLLOUT_TEST_MARKER\"\n", 0700)
	checker := deploymentCredentialChecker{client: server.Client(), ghcrTokenRoot: server.URL}
	check := checker.checkRolloutImage(map[string]string{"GHCR_PULL_USERNAME": "selected-user", "GHCR_PULL_TOKEN": "bad-token"}, "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64))
	if check.Passed || strings.Contains(check.Detail, "sensitive") {
		t.Fatalf("unexpected %+v", check)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invalid selected credential fell back to docker")
	}
}

func TestRolloutLocalChecksAggregateWithoutCredentialStore(t *testing.T) {
	var out bytes.Buffer
	checker := deploymentCredentialChecker{out: &out}
	opts := deploymentCredentialCheckOptions{selected: keySet("tls", "mounts"), readOnly: true, tls: rolloutTLSOptions{cert: "absent", key: "absent"}, manifests: []string{"absent.json"}}
	if err := checker.checkWithOptions(deploymentConfig{}, "absent-store", opts); err == nil {
		t.Fatal("failed checks returned success")
	}
	if !strings.Contains(out.String(), "[FAIL] rollout TLS") || !strings.Contains(out.String(), "[FAIL] rollout Secret mounts") || strings.Contains(out.String(), "credential env file") {
		t.Fatal(out.String())
	}
}

func TestRolloutCLIRejectsInvalidQualification(t *testing.T) {
	for _, args := range [][]string{
		{"provision", "--read-only"}, {"credentials-check", "--read-only", "--grant-object-storage-bucket-access"},
		{"credentials-check", "--checks", "typo"}, {"credentials-check", "--checks", "tls"},
		{"credentials-check", "--checks", "mounts"}, {"credentials-check", "--image", "ghcr.io/owner/repo:latest"},
		{"credentials-check", "--tls-cert", "cert"},
		{"credentials-check", "--min-valid-days", "30"},
		{"credentials-check", "--tls-purpose", "client"},
	} {
		if err := runDeploymentWithOperations(args, deploymentOperations{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestRolloutRawGHCRNewlineFailsBeforeNetwork(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	store, err := newSecretStore(t.TempDir(), "staging")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(store.Root, "operator", "env")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	rolloutWrite(t, filepath.Join(dir, "GHCR_PULL_USERNAME"), "user", 0600)
	rolloutWrite(t, filepath.Join(dir, "GHCR_PULL_TOKEN"), "token\n", 0600)
	var out bytes.Buffer
	client := &http.Client{Transport: rolloutRoundTrip(func(r *http.Request) (*http.Response, error) {
		t.Error("malformed raw credential reached network")
		return nil, fmt.Errorf("unexpected network")
	})}
	checker := deploymentCredentialChecker{client: client, out: &out}
	if err := checker.checkWithOptions(testDeploymentCredentialConfig(), dir, deploymentCredentialCheckOptions{selected: keySet("ghcr"), readOnly: true}); err == nil {
		t.Fatal("canonical newline was hidden by normalization")
	}
	if !strings.Contains(out.String(), "CR/LF") {
		t.Fatal(out.String())
	}
}

func TestRolloutLocalCLICompletesWithoutProviders(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	workspace := writeDeploymentFixture(t, "staging", "lke")
	opts := rolloutTLSFixture(t, time.Now().Add(30*24*time.Hour), x509.ExtKeyUsageServerAuth)
	if err := runDeploymentWithOperations([]string{"credentials-check", "--workspace", workspace, "--environment", "staging", "--read-only", "--checks", "tls", "--tls-cert", opts.cert, "--tls-key", opts.key, "--tls-ca", opts.ca, "--tls-name", opts.hostname}, deploymentOperations{}); err != nil {
		t.Fatal(err)
	}
}

func TestRolloutScopedProviderMustBeConfigured(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	file := writeDeploymentCredentialEnv(t, "http://unused.invalid", "token")
	var out bytes.Buffer
	checker := deploymentCredentialChecker{out: &out}
	if err := checker.checkWithOptions(deploymentConfig{}, file, deploymentCredentialCheckOptions{selected: keySet("dns"), readOnly: true}); err == nil {
		t.Fatal("unconfigured explicit selection reported PASS")
	}
	if !strings.Contains(out.String(), "not configured") {
		t.Fatal(out.String())
	}
}

func TestRolloutInitContainerMustReadSecret(t *testing.T) {
	raw := `{"securityContext":{"runAsUser":0},"containers":[{"volumeMounts":[{"name":"pki"}]}],"initContainers":[{"securityContext":{"runAsUser":10001},"volumeMounts":[{"name":"pki"}]}],"volumes":[{"name":"pki","secret":{"defaultMode":256}}]}`
	path := rolloutWrite(t, filepath.Join(t.TempDir(), "pod.json"), raw, 0600)
	if check := checkRolloutMounts(path); check.Passed {
		t.Fatal("unreadable init-container Secret passed")
	}
}

func TestRolloutTokenExchangeAloneDoesNotProveImageAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"token":"scoped-token"}`)
			return
		}
		if r.Method != http.MethodHead || r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Error("manifest must use the selected scoped credential")
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), ghcrTokenRoot: server.URL + "/token", ghcrRegistryRoot: server.URL}
	check := checker.checkRolloutImage(map[string]string{"GHCR_PULL_USERNAME": "user", "GHCR_PULL_TOKEN": "token"}, "ghcr.io/owner/repo@sha256:"+strings.Repeat("a", 64))
	if check.Passed || !strings.Contains(check.Detail, "exact image manifest") {
		t.Fatalf("unexpected %+v", check)
	}
}
