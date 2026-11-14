package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// This opt-in runs the actual preflight shell and actual Go CLI, but does not
// assert GO: provider and publication endpoints are synthetic and Docker run
// deliberately refuses to impersonate real native binaries or ceremonies.
func TestIsolatedFreshShellPreflightReportsRealNoGo(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("RTK_TEST_FRESH_SHELL") != "1" {
		t.Skip("opt-in macOS isolated full shell preflight")
	}
	releaseManifest := strings.TrimSpace(os.Getenv("RTK_TEST_VIDEO_RELEASE_MANIFEST"))
	if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" && releaseManifest == "" {
		t.Skip("set RTK_TEST_VIDEO_RELEASE_MANIFEST to the exact published Video Cloud image-manifest.json for the isolated registrar fixture")
	}
	workspace := writeDeploymentFixture(t, "dev", "lke")
	if err := os.Chmod(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	environmentRoot := filepath.Join(workspace, "cloud_env", "dev")
	root, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := readStrictEnv(filepath.Join(root, "cloud_deploy/architectures/kubernetes/architecture.env"))
	if err != nil {
		t.Fatal("secret-free architecture input", err)
	}
	keys := []string{"PKI_REQUIRE_USER_MFA", "PKI_STATE_KV_MOUNT", "PKI_STATE_LEASE_DURATION", "PKI_REQUIRED_CONSUMERS_DEVICE", "PKI_REQUIRED_CONSUMERS_APP", "PKI_REQUIRED_CONSUMERS_SERVICE", "PKI_REQUIRED_CONSUMERS_MQTT", "PKI_REQUIRED_CONSUMERS_OPENBAO_TLS", "DURABLE_DATA_STORAGE_CLASS", "REDIS_DATA_STORAGE", "PROMETHEUS_DATA_STORAGE", "OPENBAO_STORAGE_BACKEND", "OPENBAO_DATA_STORAGE", "OPENBAO_AUDIT_STORAGE", "OPENBAO_POD_MANAGEMENT_POLICY", "OPENBAO_UPDATE_STRATEGY", "OPENBAO_MIN_REPLICAS", "OPENBAO_NODE_CLASS", "OPENBAO_REQUEST_CPU", "OPENBAO_REQUEST_MEMORY"}
	for _, role := range environmentProviderImageKeys {
		keys = append(keys, environmentProviderImageConfigKey(role))
	}
	architecturePath := filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env")
	existing := map[string]string{}
	for _, name := range []string{"architecture.env", "capacity.env", "topology.env", "workloads.env"} {
		part, readErr := readStrictEnv(filepath.Join(workspace, "cloud_deploy/architectures/kubernetes", name))
		if readErr != nil {
			t.Fatal("synthetic architecture input", readErr)
		}
		if err := mergeDeploymentLayer(existing, part, "architecture"); err != nil {
			t.Fatal("synthetic architecture inputs", err)
		}
	}
	var architecture strings.Builder
	for key, value := range map[string]string{"DEPLOYMENT_PROTECTION_PROFILE": "standard", "PKI_STATE_BACKEND": "openbao"} {
		if _, ok := existing[key]; !ok {
			fmt.Fprintf(&architecture, "%s=%s\n", key, value)
			existing[key] = value
		}
	}
	for _, key := range keys {
		if value := baseline[key]; value != "" {
			if _, ok := existing[key]; !ok {
				fmt.Fprintf(&architecture, "%s=%s\n", key, value)
				existing[key] = value
			}
		}
	}
	appendFile(t, architecturePath, architecture.String())
	writeTestFile(t, filepath.Join(environmentRoot, "storage.env"), "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=rtk-cloud-dev-runtime-us-sea\nRUNTIME_MEDIA_STORAGE_PREFIX=environments/video-cloud-dev\nRUNTIME_OTA_STORAGE_MODE=dedicated\nRUNTIME_OTA_STORAGE_POLICY=colocated\nRUNTIME_OTA_STORAGE_BUCKET=rtk-cloud-dev-ota-firmware-us-sea\nRUNTIME_OTA_STORAGE_PREFIX=environments/video-cloud-dev\n")
	cfg, err := resolveDeploymentConfig(workspace, "dev", environmentRoot)
	if err != nil {
		t.Fatal("synthetic config", err)
	}
	// Fresh create must start without runtime/ or inventory.json. The common
	// store fixture creates both and therefore represents an existing state.
	configRoot := filepath.Join(t.TempDir(), "rtk_cloud")
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	store, err := newSecretStore(configRoot, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(store.Root); err != nil {
		t.Fatal("synthetic store root", err)
	}
	if !isolatedScriptTempPath(workspace) || !isolatedScriptTempPath(store.Root) {
		t.Fatal("only synthetic temp roots allowed")
	}
	// Synthetic operator-confirmed provider account limit, scoped only to the
	// temporary test store; never read or modify a real environment account.
	if err := store.write("deployment/adapters/lke/account.env", []byte("LKE_ACTIVE_SERVICE_LIMIT=1000\n"), false); err != nil {
		t.Fatal(err)
	}
	// Reuse the test ACME protocol server. Its ephemeral certificate is
	// trusted only by the child via a test build overlay, never system trust.
	acme := newPublicTLSACMEServer(t, nil, nil)
	trust := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: acme.server.Certificate().Raw})
	if err := store.write("test/acme-trust/ca.pem", trust, false); err != nil {
		t.Fatal(err)
	}
	acmeCA := filepath.Join(store.Root, "test/acme-trust/ca.pem")
	operator := map[string]string{"LINODE_TOKEN": "isolated-provider-token", "GHCR_PULL_USERNAME": "isolated-user", "GHCR_PULL_TOKEN": "isolated-fake-gh-only", "GODADDY_KEY": "isolated-dns-key", "GODADDY_SECRET": "isolated-dns-secret", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "isolated-media-key", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "isolated-media-secret", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "isolated-ota-key", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "isolated-ota-secret", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@example.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-strong-password", "PKI_OPERATOR_SIGNER_REF": "isolated-signer"}
	operator["LKE_PUBLIC_HTTPS_ACME_SERVER"] = acme.server.URL + "/directory"
	for key, value := range operator {
		if err := store.write(filepath.Join("operator/env", key), []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "isolated-only")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519", pem.EncodeToMemory(block), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519.pub", ssh.MarshalAuthorizedKey(public), false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{store.RuntimeDir(), filepath.Join(store.Root, "inventory.json")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("fresh synthetic store unexpectedly contains runtime or inventory", path)
		}
	}
	// The fake starts with no provider allocations, journal, qualification or
	// generated runtime identity. SSH material above is synthetic operator input.
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
		t.Fatal("fresh store already has a deployment owner")
	}
	// The fake provider supplies a syntactically valid private kubeconfig;
	// its loopback endpoint remains blocked unless the test explicitly adds a
	// permitted synthetic Kubernetes adapter to the sandbox.
	ca := base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))
	kubeconfig := []byte("apiVersion: v1\nkind: Config\ncurrent-context: isolated\nclusters:\n- name: isolated\n  cluster:\n    server: https://127.0.0.1:6443\n    certificate-authority-data: " + ca + "\ncontexts:\n- name: isolated\n  context:\n    cluster: isolated\n    user: isolated\nusers:\n- name: isolated\n  user:\n    token: isolated-kubernetes-token\n")
	provider := newIsolatedFreshProvider(t, cfg, operator, kubeconfig)
	if provider.s3Server == nil || !bytes.Equal(acme.server.Certificate().Raw, provider.s3Server.Certificate().Raw) {
		t.Fatal("synthetic ACME and S3 must share the verified test-only trust anchor")
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = workspace
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		raw, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("synthetic git: %v: %s", e, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	git("init", "-q")
	git("add", "cloud_env", "cloud_deploy")
	if err := os.MkdirAll(filepath.Join(workspace, "repos"), 0700); err != nil {
		t.Fatal(err)
	}
	const videoRevision = "a779bac7189a1563b69ddcaccf837ec389395f67"
	const videoAPI = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:12067783a9e4337f1da4ef31e7576bc71298fca8a63bb841b7f1d8bbdb669e55"
	const videoEMQX = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:1a31147328a3bf4a0b93d511c915e4f7f4b7ddc4cdb3f7a2a1dd6b9912a140d7"
	const videoBao = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:269dcd39a6200bd7c2c854e6bc75861f82bb11816251c95a71ffaedec77dbe2e"
	const accountImage = "ghcr.io/hkt999rtk/rtk_account_manager/account-manager@sha256:1d118e86ec9bbc35c213f0e162b3a974a6ab49adc535491727d12c7e1b8e58a0"
	const accountRevision = "4341a05ec727735e8ef52ddd2394beffef81a986"
	if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" {
		var manifest struct {
			Source  string `json:"source_commit"`
			Run     string `json:"run_id"`
			Attempt string `json:"run_attempt"`
			Images  map[string]struct {
				Image  string `json:"image"`
				Digest string `json:"digest"`
			} `json:"images"`
		}
		raw, err := os.ReadFile(releaseManifest)
		if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Source != videoRevision || manifest.Run != "42844520933" || manifest.Attempt != "1" || manifest.Images["video_cloud_api"].Image+"@"+manifest.Images["video_cloud_api"].Digest != videoAPI || manifest.Images["emqx_pki"].Image+"@"+manifest.Images["emqx_pki"].Digest != videoEMQX || manifest.Images["openbao_pki"].Image+"@"+manifest.Images["openbao_pki"].Digest != videoBao {
			t.Fatal("exact #777 published manifest is required")
		}
	}
	var publications []isolatedPublicationInput
	for i, source := range lkeServiceImageSources() {
		actual := filepath.Join(root, source.RepoPath)
		if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" && source.RepoName == "rtk_video_cloud" {
			actual = strings.TrimSpace(os.Getenv("RTK_TEST_RELEASE_SOURCE"))
			if actual == "" {
				t.Skip("set RTK_TEST_RELEASE_SOURCE to a clean isolated Video Cloud checkout under the selected temporary directory")
			}
			resolved, err := filepath.EvalSymlinks(actual)
			if err != nil || !isolatedScriptTempPath(resolved) {
				t.Fatal("release source must be an isolated temporary checkout")
			}
			status, err := exec.Command("git", "-C", resolved, "status", "--porcelain").Output()
			if err != nil || len(status) != 0 {
				t.Fatal("release source must be clean")
			}
			actual = resolved
		}
		raw, e := exec.Command("git", "-C", actual, "rev-parse", "HEAD").Output()
		if e != nil {
			t.Fatal("isolated source revision", e)
		}
		revision := strings.TrimSpace(string(raw))
		if err := os.Symlink(actual, filepath.Join(workspace, source.RepoPath)); err != nil {
			t.Fatal(err)
		}
		git("update-index", "--add", "--cacheinfo", "160000,"+revision+","+source.RepoPath)
		repo := "hkt999rtk/" + source.RepoName
		primary := "ghcr.io/" + repo + "/" + source.Name
		digest := func(value string) string {
			sum := sha256.Sum256([]byte("isolated-image:" + value))
			return "sha256:" + hex.EncodeToString(sum[:])
		}
		images := map[string]string{primary: digest(primary)}
		if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" && source.RepoName == "rtk_video_cloud" {
			if revision != videoRevision {
				t.Fatal("release source revision differs from #777 manifest")
			}
			images[primary] = strings.Split(videoAPI, "@")[1]
		}
		if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" && source.RepoName == "rtk_account_manager" {
			if revision != accountRevision {
				t.Fatal("account source differs from cached image")
			}
			images[primary] = strings.Split(accountImage, "@")[1]
		}
		if source.RepoName == "rtk_video_cloud" {
			for _, name := range []string{"video-cloud-emqx-pki", "video-cloud-openbao-pki"} {
				key := "ghcr.io/" + repo + "/" + name
				images[key] = digest(key)
				if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" {
					if name == "video-cloud-emqx-pki" {
						images[key] = strings.Split(videoEMQX, "@")[1]
					} else {
						images[key] = strings.Split(videoBao, "@")[1]
					}
				}
			}
		}
		runID := int64(300 + i)
		if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" && source.RepoName == "rtk_video_cloud" {
			runID = 42844520933
		}
		publications = append(publications, isolatedPublicationInput{Repository: repo, Revision: revision, Primary: primary, RunID: runID, Attempt: 1, Images: images})
	}
	git("-c", "user.name=Isolated Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "synthetic environment inputs")
	bin := t.TempDir()
	if err := os.Chmod(bin, 0700); err != nil {
		t.Fatal(err)
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	// Resolve the already installed workspace toolchain before entering the
	// network-denied sandbox. GOPROXY=off prevents an implicit download.
	goEnv := exec.Command(goPath, "env", "GOROOT")
	goEnv.Dir = root
	goEnv.Env = append(os.Environ(), "GOPROXY=off")
	goRoot, err := goEnv.Output()
	if err != nil {
		t.Fatal("installed workspace Go toolchain", err)
	}
	goPath = filepath.Join(strings.TrimSpace(string(goRoot)), "bin", "go")
	// Darwin uses the system trust store and ignores SSL_CERT_FILE. This
	// isolated build overlay changes only the ACME HTTP client's trust roots;
	// hostname, certificate chain and TLS verification remain enabled.
	publicTLSSource := filepath.Join(root, "scripts/go/rtk-cloud/deployment_public_tls.go")
	publicTLSBytes, err := os.ReadFile(publicTLSSource)
	if err != nil {
		t.Fatal(err)
	}
	originalClient := "return environmentPublicTLSOps{HTTP: http.DefaultClient, DNS: environmentCreateDNSAdapter, WaitTXT: waitDNSTXT, WaitDNS01Set: waitPublicTLSDNS01}"
	testClient := `return environmentPublicTLSOps{HTTP: isolatedTestHTTPSClient(), DNS: environmentCreateDNSAdapter, WaitTXT: waitDNSTXT, WaitDNS01Set: waitPublicTLSDNS01}`
	testClientHelper := `
func isolatedTestHTTPSClient() *http.Client {
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(os.Getenv("RTK_TEST_ACME_ROOT_CA"))
	if err == nil { roots.AppendCertsFromPEM(ca) }
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
}
`
	if strings.Count(string(publicTLSBytes), originalClient) != 1 {
		t.Fatal("ACME client overlay target changed")
	}
	overlaySource := strings.Replace(string(publicTLSBytes), originalClient, testClient, 1) + testClientHelper
	if err := store.write("test/overlays/deployment_public_tls.go", []byte(overlaySource), false); err != nil {
		t.Fatal(err)
	}
	overlaySourcePath := filepath.Join(store.Root, "test/overlays/deployment_public_tls.go")
	credentialsSource := filepath.Join(root, "scripts/go/rtk-cloud/deployment_credentials.go")
	credentialsBytes, err := os.ReadFile(credentialsSource)
	if err != nil {
		t.Fatal(err)
	}
	originalS3Client := "client:           &http.Client{Timeout: 15 * time.Second},"
	if strings.Count(string(credentialsBytes), originalS3Client) != 1 {
		t.Fatal("S3 client overlay target changed")
	}
	credentialsOverlay := strings.Replace(string(credentialsBytes), originalS3Client, "client:           isolatedTestHTTPSClient(),", 1)
	if err := store.write("test/overlays/deployment_credentials.go", []byte(credentialsOverlay), false); err != nil {
		t.Fatal(err)
	}
	credentialsOverlayPath := filepath.Join(store.Root, "test/overlays/deployment_credentials.go")
	overlayFiles := map[string]string{publicTLSSource: overlaySourcePath, credentialsSource: credentialsOverlayPath}
	if os.Getenv("RTK_TEST_OPENBAO_FIRST") == "1" {
		createSource := filepath.Join(root, "scripts/go/rtk-cloud/deployment_create.go")
		raw, err := os.ReadFile(createSource)
		if err != nil {
			t.Fatal(err)
		}
		original := "openBaoFirst := false"
		if strings.Count(string(raw), original) != 1 {
			t.Fatal("test-only builder overlay target changed")
		}
		modified := strings.Replace(string(raw), original, "openBaoFirst := true", 1)
		if err := store.write("test/overlays/deployment_create.go", []byte(modified), false); err != nil {
			t.Fatal(err)
		}
		overlayFiles[createSource] = filepath.Join(store.Root, "test/overlays/deployment_create.go")
		t.Log("isolated CLI build uses test-only OpenBao-first plan builder overlay before qualification")
	}
	if os.Getenv(isolatedCustodyStopEnv) == "1" {
		if os.Getenv("RTK_TEST_OPENBAO_FIRST") != "1" || os.Getenv("RTK_TEST_FRESH_CREATE") != "1" {
			t.Fatal("isolated custody interruption requires same-parent OpenBao-first create")
		}
		foundation := filepath.Join(root, "scripts/go/rtk-cloud/deployment_create_foundation.go")
		raw, err := os.ReadFile(foundation)
		if err != nil {
			t.Fatal("isolated custody boundary source unavailable", err)
		}
		modified, err := isolatedCustodyStopOverlay(string(raw))
		if err != nil {
			t.Fatal("isolated custody boundary unavailable", err)
		}
		if err := store.write("test/overlays/deployment_create_foundation.go", []byte(modified), false); err != nil {
			t.Fatal(err)
		}
		overlayFiles[foundation] = filepath.Join(store.Root, "test/overlays/deployment_create_foundation.go")
		t.Log("isolated fixed CLI uses explicit test-only stop before custody")
	}
	overlayJSON, err := json.Marshal(map[string]any{"Replace": overlayFiles})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("test/overlays/go-overlay.json", overlayJSON, false); err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(store.Root, "test/overlays/go-overlay.json")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	// Both official wrappers build separately. In this isolated test only,
	// compile the overlay once and pass the identical private binary to both.
	// The source tree and overlay are fingerprinted around both invocations.
	fingerprintScript := `import hashlib, os, sys
root, overlay = sys.argv[1:]
h = hashlib.sha256()
def add(path, label):
    if os.path.islink(path) or not os.path.isfile(path): raise SystemExit(2)
    with open(path, 'rb') as f: data = f.read()
    h.update(label.encode()+b'\0'+str(len(data)).encode()+b'\0'+data)
for rel in ('go.work', 'go.work.sum'):
    add(os.path.join(root, rel), rel)
for base, dirs, files in os.walk(os.path.join(root, 'scripts/go'), followlinks=False):
    dirs.sort()
    if any(os.path.islink(os.path.join(base, d)) for d in dirs): raise SystemExit(2)
    for name in sorted(files):
        path = os.path.join(base, name)
        add(path, os.path.relpath(path, root))
for name in sorted(os.listdir(overlay)):
    add(os.path.join(overlay, name), 'overlay/'+name)
print(h.hexdigest())
`
	if err := store.write("test/source-fingerprint.py", []byte(fingerprintScript), false); err != nil {
		t.Fatal(err)
	}
	fingerprintPath := filepath.Join(store.Root, "test/source-fingerprint.py")
	privateCLI := filepath.Join(store.Root, "test/isolated-rtk-cloud")
	sourceSHA := filepath.Join(store.Root, "test/source-sha256")
	binarySHA := filepath.Join(store.Root, "test/binary-sha256")
	callsPath := filepath.Join(store.Root, "test/build-calls")
	goShim := `#!/bin/sh
set -eu
umask 077
[ "$#" -eq 4 ] && [ "$1" = build ] && [ "$2" = -o ] && [ "$4" = ./scripts/go/rtk-cloud ] || exit 70
case "$3" in /*) ;; *) exit 70;; esac
fingerprint() { /usr/bin/python3 ` + quote(fingerprintPath) + ` ` + quote(root) + ` ` + quote(filepath.Dir(overlayPath)) + `; }
hashbin() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }
before=$(fingerprint)
if [ ! -e ` + quote(privateCLI) + ` ]; then
  ` + quote(goPath) + ` build -overlay=` + quote(overlayPath) + ` -o ` + quote(privateCLI) + ` ./scripts/go/rtk-cloud
  [ "$before" = "$(fingerprint)" ] || exit 71
  printf '%s\n' "$before" > ` + quote(sourceSHA) + `
  hashbin ` + quote(privateCLI) + ` > ` + quote(binarySHA) + `
else
  [ "$before" = "$(cat ` + quote(sourceSHA) + `)" ] || exit 71
  [ "$(hashbin ` + quote(privateCLI) + `)" = "$(cat ` + quote(binarySHA) + `)" ] || exit 72
fi
/bin/cp ` + quote(privateCLI) + ` "$3"
[ "$(hashbin "$3")" = "$(cat ` + quote(binarySHA) + `)" ] || exit 72
[ "$before" = "$(fingerprint)" ] || exit 71
printf '%s\n' "$(cat ` + quote(binarySHA) + `)" >> ` + quote(callsPath) + `
`
	t.Log("isolated wrapper test shares one privately built CLI; source and binary hashes are checked")
	// Exercise the exact shim in an independent synthetic tree without ever
	// modifying the workspace or the real CLI being used by this test.
	shimTest := t.TempDir()
	shimRoot, shimOverlay := filepath.Join(shimTest, "source"), filepath.Join(shimTest, "overlay")
	if err := os.MkdirAll(filepath.Join(shimRoot, "scripts/go"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(shimOverlay, 0700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		filepath.Join(shimRoot, "go.work"): "synthetic-work", filepath.Join(shimRoot, "go.work.sum"): "synthetic-sum",
		filepath.Join(shimRoot, "scripts/go/input.go"): "synthetic-source", filepath.Join(shimOverlay, "go-overlay.json"): "{}",
	} {
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fakeGo := filepath.Join(shimTest, "go")
	if err := os.WriteFile(fakeGo, []byte("#!/bin/sh\n[ \"$1\" = build ] && [ \"$3\" = -o ] || exit 1\nprintf '#!/bin/sh\\nexit 0\\n' > \"$4\"\nchmod 700 \"$4\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	shimCLI, shimSource, shimBinary, shimCalls := filepath.Join(shimTest, "cli"), filepath.Join(shimTest, "source-sha"), filepath.Join(shimTest, "binary-sha"), filepath.Join(shimTest, "calls")
	shimUnderTest := strings.NewReplacer(quote(goPath), quote(fakeGo), quote(overlayPath), quote(filepath.Join(shimOverlay, "go-overlay.json")), quote(filepath.Dir(overlayPath)), quote(shimOverlay), quote(root), quote(shimRoot), quote(privateCLI), quote(shimCLI), quote(sourceSHA), quote(shimSource), quote(binarySHA), quote(shimBinary), quote(callsPath), quote(shimCalls)).Replace(goShim)
	shimPath := filepath.Join(shimTest, "shim")
	if err := os.WriteFile(shimPath, []byte(shimUnderTest), 0700); err != nil {
		t.Fatal(err)
	}
	runShim := func(args ...string) error { return exec.Command(shimPath, args...).Run() }
	outputPath := filepath.Join(shimTest, "out")
	if err := runShim("build", "-o", outputPath, "./scripts/go/rtk-cloud"); err != nil {
		t.Fatal("isolated shim first build", err)
	}
	if err := runShim("build", "-o", outputPath, "./scripts/go/rtk-cloud"); err != nil {
		t.Fatal("isolated shim reuse", err)
	}
	if err := os.WriteFile(filepath.Join(shimRoot, "scripts/go/input.go"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runShim("build", "-o", outputPath, "./scripts/go/rtk-cloud"); err == nil {
		t.Fatal("shim accepted changed source")
	}
	if err := os.WriteFile(filepath.Join(shimRoot, "scripts/go/input.go"), []byte("synthetic-source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shimCLI, []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runShim("build", "-o", outputPath, "./scripts/go/rtk-cloud"); err == nil {
		t.Fatal("shim accepted changed binary")
	}
	if err := runShim("build", "-o", outputPath, "./different-package"); err == nil {
		t.Fatal("shim accepted unexpected package")
	}
	t.Log("isolated CLI build uses test-only ACME and S3 CA trust overlay")
	// Legacy read-only discovery invokes curl with the fixed public Linode
	// URL. Repoint only that exact origin to the isolated provider; all
	// other destinations fail closed and the OS sandbox still denies network.
	curlShim := `#!/usr/bin/python3
import os, sys, urllib.parse
prefix = "https://api.linode.com/v4/"
args = sys.argv[1:]
matched = [i for i, a in enumerate(args) if a.startswith("http://") or a.startswith("https://")]
if len(matched) != 1 or not args[matched[0]].startswith(prefix):
    sys.exit(92)
root = urllib.parse.urlsplit(os.environ.get("RTK_CLOUD_LINODE_API_ROOT", ""))
if root.scheme != "http" or root.hostname != "127.0.0.1" or root.port is None or root.path not in ("", "/"):
    sys.exit(92)
args[matched[0]] = root.geturl().rstrip("/") + "/" + args[matched[0]][len(prefix):]
os.execv("/usr/bin/curl", ["curl"] + args)
`
	for name, body := range map[string]string{"go": goShim, "kubectl": "#!/bin/sh\nexit 91\n", "curl": curlShim} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	pins := map[string]string{}
	imageValues := appendMap(cfg.Values, cfg.AdapterResolved)
	for _, key := range environmentProviderImageKeys {
		v := imageValues[environmentProviderImageConfigKey(key)]
		if v == "" {
			continue
		}
		if immutableEnvironmentImage.MatchString(v) {
			pins[v] = v
			continue
		}
		repo := environmentImageRepository(v)
		if !strings.Contains(strings.Split(repo, "/")[0], ".") {
			repo = "docker.io/" + repo
		}
		h := sha256.Sum256([]byte("isolated-provider:" + v))
		pins[v] = repo + "@sha256:" + hex.EncodeToString(h[:])
	}
	ci := installIsolatedPublicationDNS(t, bin, "example.test", publications, pins)
	var registrarBridge *isolatedRegistrarBridge
	var openBaoOCIDir string
	if os.Getenv("RTK_TEST_REAL_REGISTRAR") == "1" {
		for image, revision := range map[string]string{videoAPI: videoRevision, videoEMQX: videoRevision, videoBao: videoRevision, accountImage: accountRevision} {
			inspect := exec.Command("docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}} {{index .Config.Labels \"org.opencontainers.image.revision\"}}", image)
			inspected, err := inspect.Output()
			if err != nil || strings.TrimSpace(string(inspected)) != "linux/amd64 "+revision {
				t.Fatal("exact cached image/source label required", image, err)
			}
		}
		runner := func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "docker", args...).Output()
		}
		registrarBridge = newIsolatedRegistrarBridge(t, store.Root, videoAPI, videoRevision, runner)
		registrarBridge.capabilityBinaries = map[string]map[string]bool{
			videoAPI:  {"/bin/sh": true, "/app/pkimanagement": true, "/app/certissuer": true, "/app/api": true, "/app/logingester": true, "/app/pkibroker": true, "/app/serviceidentity-bootstrap": true, "/app/mqttfoundation": true, "/app/shadowworker": true, "/app/otaservice": true, "/app/videostorage": true, "/app/webrtcservice": true, "/app/cloudlogger": true},
			videoEMQX: {"/usr/local/bin/emqxpkihost": true}, accountImage: {"/app/rtk-account-manager": true},
		}
		installIsolatedRegistrarDockerShim(t, bin)
		if os.Getenv("RTK_TEST_OPENBAO_FIRST") == "1" {
			openBaoOCIDir = t.TempDir()
			if err := os.Chmod(openBaoOCIDir, 0700); err != nil {
				t.Fatal(err)
			}
			installIsolatedOpenBaoOCIBridge(t, bin, openBaoOCIDir)
			installIsolatedFoundationOCIBridge(t, bin, openBaoOCIDir)
		}
	}
	_, providerPortRaw, err := net.SplitHostPort(provider.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var providerPort int
	if _, err := fmt.Sscan(providerPortRaw, &providerPort); err != nil {
		t.Fatal(err)
	}
	profile := fmt.Sprintf("(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip \"localhost:%d\"))(allow network-outbound (remote ip \"localhost:%d\"))", providerPort, ci.port(t))
	_, acmePort, err := net.SplitHostPort(acme.server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	profile += fmt.Sprintf("(allow network-outbound (remote ip \"localhost:%s\"))", acmePort)
	_, s3Port, err := net.SplitHostPort(provider.s3Server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	profile += fmt.Sprintf("(allow network-outbound (remote ip \"localhost:%s\"))", s3Port)
	if registrarBridge != nil {
		profile += fmt.Sprintf("(allow network-outbound (remote ip \"localhost:%s\"))", registrarBridge.port(t))
	}
	account, err := user.Current()
	if err != nil || account.HomeDir == "" || !filepath.IsAbs(account.HomeDir) {
		t.Fatal("cannot identify real account home for sandbox deny rules", err)
	}
	moduleCache := os.Getenv("GOMODCACHE")
	if !filepath.IsAbs(moduleCache) {
		t.Fatal("test main did not preserve absolute offline module cache")
	}
	for _, p := range []string{filepath.Join(account.HomeDir, ".config/rtk_cloud"), filepath.Join(account.HomeDir, ".aws"), filepath.Join(account.HomeDir, ".ssh"), filepath.Join(account.HomeDir, ".env")} {
		profile += fmt.Sprintf("(deny file-read* file-write* (subpath %q))", p)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	script := filepath.Join(root, "scripts/check-deployment-preflight.sh")
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, "/bin/bash", script, "--environment", "dev", "--operation", "create", "--workspace", workspace, "--environment-root", environmentRoot)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "RTK_CLOUD_TEST_MODE=1", "RTK_CLOUD_CONFIG_ROOT=" + store.ConfigRoot, "RTK_CLOUD_LINODE_API_ROOT=" + provider.server.URL, "RTK_CLOUD_GODADDY_API_ROOT=" + ci.dns.URL, "RTK_ISOLATED_CI_DIR=" + ci.ciDir, "RTK_CLOUD_KUBECTL=" + filepath.Join(bin, "kubectl"), "GOPROXY=off", "GOTOOLCHAIN=local", "GOENV=off", "GOMODCACHE=" + moduleCache, "GOCACHE=/Volumes/RTKGoBuild/cache", "TMPDIR=" + os.TempDir(), "RTK_TEST_ACME_ROOT_CA=" + acmeCA}
	if os.Getenv(isolatedCustodyStopEnv) == "1" {
		cmd.Env = append(cmd.Env, isolatedCustodyStopEnv+"=1")
	}
	if openBaoOCIDir != "" {
		cmd.Env = append(cmd.Env, "RTK_TEST_OPENBAO_OCI_DIR="+openBaoOCIDir)
	}
	if registrarBridge != nil {
		cmd.Env = append(cmd.Env, "RTK_TEST_REGISTRAR_BRIDGE="+registrarBridge.server.URL, "RTK_TEST_REGISTRAR_TOKEN="+registrarBridge.token)
	}
	output, runErr := cmd.CombinedOutput()
	log, err := os.CreateTemp("/private/tmp", "rtk-isolated-fresh-preflight-*.log")
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Chmod(0600); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write(output); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("full private preflight log:", log.Name())
	if ctx.Err() != nil {
		t.Fatal("actual wrapper timed out", ctx.Err())
	}
	noGo := runErr != nil && strings.Contains(string(output), "NO-GO environment=dev operation=create")
	goDecision := runErr == nil && strings.Contains(string(output), "\nGO environment=dev operation=create")
	if !noGo && !(registrarBridge != nil && goDecision) {
		t.Fatalf("expected explicit isolated create qualification decision; log %s; exit %v", log.Name(), runErr)
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
		t.Fatal("preflight created a deployment owner")
	}
	t.Log("actual shell and CLI reached an isolated qualification decision; inspect saved log")
	runIsolatedFreshCreateAfterGO(t, store, cfg, root, workspace, environmentRoot, profile, cmd.Env, output, runErr, provider.kubeconfig, provider)
	if os.Getenv("RTK_TEST_FRESH_CREATE") == "1" && goDecision {
		calls, readErr := os.ReadFile(callsPath)
		binaryHash, hashErr := os.ReadFile(binarySHA)
		sourceHash, sourceErr := os.ReadFile(sourceSHA)
		if readErr != nil || hashErr != nil || sourceErr != nil || strings.TrimSpace(string(calls)) != strings.TrimSpace(string(binaryHash))+"\n"+strings.TrimSpace(string(binaryHash)) || len(strings.TrimSpace(string(binaryHash))) != 64 || len(strings.TrimSpace(string(sourceHash))) != 64 {
			t.Fatal("isolated preflight and create did not use the same verified CLI")
		}
		t.Logf("isolated wrapper source SHA-256 %s; shared CLI SHA-256 %s", strings.TrimSpace(string(sourceHash)), strings.TrimSpace(string(binaryHash)))
	}
	if strings.Contains(string(output), "cause: complete create stage failed or lacks verified evidence") {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		diagCtx, diagCancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer diagCancel()
		diag := exec.CommandContext(diagCtx, "/usr/bin/sandbox-exec", "-p", profile, executable, "-test.v", "-test.run", "^TestIsolatedFreshPlanDiagnosis$", "-test.timeout", "2m")
		diag.Env = append(cmd.Env, "RTK_TEST_FRESH_DIAG=1", "RTK_TEST_FRESH_WORKSPACE="+workspace, "RTK_TEST_FRESH_ENV_ROOT="+environmentRoot, "RTK_TEST_FRESH_CONFIG_ROOT="+store.ConfigRoot)
		diag.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		diag.Cancel = func() error {
			if diag.Process == nil {
				return nil
			}
			return syscall.Kill(-diag.Process.Pid, syscall.SIGKILL)
		}
		diag.WaitDelay = 5 * time.Second
		diagnostic, diagErr := diag.CombinedOutput()
		diagLog, err := os.CreateTemp("/private/tmp", "rtk-isolated-fresh-diagnosis-*.log")
		if err != nil {
			t.Fatal(err)
		}
		if err := diagLog.Chmod(0600); err != nil {
			t.Fatal(err)
		}
		if _, err := diagLog.Write(diagnostic); err != nil {
			t.Fatal(err)
		}
		if err := diagLog.Close(); err != nil {
			t.Fatal(err)
		}
		t.Log("isolated formal plan diagnostic:", diagLog.Name(), "status", diagErr)
	}
}

// Invoked only as the same sandboxed child against the same synthetic inputs.
// It calls native planning stages sequentially to expose the underlying error;
// it does not create a deployment execution, journal or acceptance receipt.
func TestIsolatedFreshPlanDiagnosis(t *testing.T) {
	if os.Getenv("RTK_TEST_FRESH_DIAG") != "1" {
		t.Skip("only launched in isolated child")
	}
	workspace, envRoot := os.Getenv("RTK_TEST_FRESH_WORKSPACE"), os.Getenv("RTK_TEST_FRESH_ENV_ROOT")
	if !isolatedScriptTempPath(workspace) || !isolatedScriptTempPath(envRoot) {
		t.Fatal("diagnostic inputs must be synthetic temporary paths")
	}
	configRoot := os.Getenv("RTK_TEST_FRESH_CONFIG_ROOT")
	if !isolatedScriptTempPath(configRoot) {
		t.Fatal("diagnostic config root must be synthetic")
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", configRoot)
	cfg, err := resolveDeploymentConfig(workspace, "dev", envRoot)
	if err != nil {
		t.Fatal("config:", err)
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil || !isolatedScriptTempPath(store.Root) {
		t.Fatal("operator or temporary store:", err)
	}
	ctx := t.Context()
	release, _, err := defaultEnvironmentCreateDependencies().Release(ctx, cfg)
	if err != nil {
		t.Fatal("release:", err)
	}
	provider, err := buildEnvironmentCreateProviderPlan(ctx, cfg, operator)
	if err != nil {
		t.Fatal("provider plan:", err)
	}
	bootstrap, err := buildEnvironmentCreateBootstrapPlan(ctx, cfg, release, store, operator)
	if err != nil {
		t.Fatal("bootstrap plan:", err)
	}
	publicTLS, err := prepareEnvironmentCreatePublicTLS(ctx, cfg, store)
	if err != nil {
		t.Fatal("public TLS plan:", err)
	}
	allocations, err := qualifyEnvironmentCreateDependencyContract(ctx, cfg, release, store, operator, bootstrap)
	if err != nil {
		t.Fatal("dependency contract:", err)
	}
	t.Log("dependency allocations", allocations)
	plan := &environmentCreatePlan{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Release: release, Provider: provider, Bootstrap: bootstrap, PublicTLS: publicTLS}
	if _, err := qualifyEnvironmentCreateDataPlaneArtifact(ctx, cfg, plan, store); err != nil {
		t.Fatal("TURN artifact:", err)
	}
}
