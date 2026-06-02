package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteProvisionArtifactsIncludesCloudAdminPrivateIP(t *testing.T) {
	root := t.TempDir()
	paths := provisionPaths{
		EnvRoot:             root,
		VideoState:          filepath.Join(root, "state", "video-cloud-staging.state.json"),
		AccountManagerState: filepath.Join(root, "state", "account-manager-staging.env"),
		AdminState:          filepath.Join(root, "state", "cloud-admin-staging.env"),
		ArtifactsDir:        filepath.Join(root, "artifacts"),
	}
	mkdirAll(t, filepath.Dir(paths.VideoState))
	writeFile(t, paths.VideoState, `{
  "stack":"video-cloud-test",
  "vpc_id":1,
  "subnet_id":2,
  "firewalls":{"edge":10,"api":11,"infra":12,"mqtt":13,"coturn":14},
  "instances":{
    "edge":{"id":101,"role":"edge","label":"vc-edge","public_ipv4":"203.0.113.5","private_ip":"10.42.1.5"},
    "api":{"id":102,"role":"api","label":"vc-api","public_ipv4":"203.0.113.6","private_ip":"10.42.1.10"},
    "infra":{"id":103,"role":"infra","label":"vc-infra","public_ipv4":"203.0.113.7","private_ip":"10.42.1.30"},
    "mqtt":{"id":104,"role":"mqtt","label":"vc-mqtt","public_ipv4":"203.0.113.8","private_ip":"10.42.1.40"},
    "coturn":{"id":105,"role":"coturn","label":"vc-coturn","public_ipv4":"203.0.113.9","private_ip":""}
  }
}`)
	writeFile(t, paths.AccountManagerState, "ACCOUNT_MANAGER_LINODE_ID=201\nACCOUNT_MANAGER_LINODE_LABEL=am\nACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20\nACCOUNT_MANAGER_LINODE_PRIVATE_IPV4=10.42.1.50\nACCOUNT_MANAGER_LINODE_FIREWALL_ID=301\n")
	writeFile(t, paths.AdminState, "ADMIN_LINODE_ID=202\nADMIN_LINODE_LABEL=admin\nADMIN_LINODE_PUBLIC_IPV4=203.0.113.30\nADMIN_LINODE_PRIVATE_IPV4=10.42.1.60\nADMIN_LINODE_FIREWALL_ID=302\n")

	dir, err := writeProvisionArtifacts(paths, "video-cloud-test")
	if err != nil {
		t.Fatalf("writeProvisionArtifacts returned error: %v", err)
	}

	var inventory struct {
		CloudAdmin struct {
			PrivateIP string `json:"private_ip"`
		} `json:"cloud_admin"`
	}
	readJSON(t, filepath.Join(dir, "inventory.json"), &inventory)
	if inventory.CloudAdmin.PrivateIP != "10.42.1.60" {
		t.Fatalf("cloud_admin.private_ip = %q", inventory.CloudAdmin.PrivateIP)
	}

	var targets struct {
		Targets map[string]struct {
			PrivateIP string `json:"private_ip"`
		} `json:"targets"`
	}
	readJSON(t, filepath.Join(dir, "deployment-targets.json"), &targets)
	if targets.Targets["cloud_admin"].PrivateIP != "10.42.1.60" {
		t.Fatalf("targets.cloud_admin.private_ip = %q", targets.Targets["cloud_admin"].PrivateIP)
	}
}

func TestFirewallTargetsUsesConfiguredVideoCloudStackState(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "env"))
	mkdirAll(t, filepath.Join(root, "state"))
	writeFile(t, filepath.Join(root, "env", "stack.env"), "CLOUD_STACK_NAME=video-cloud-stg-0529\n")
	writeFile(t, filepath.Join(root, "state", "video-cloud-stg-0529.state.json"), `{
  "stack":"video-cloud-stg-0529",
  "firewalls":{"edge":101,"api":102,"infra":103,"mqtt":104,"coturn":105}
}`)
	writeFile(t, filepath.Join(root, "state", "account-manager-staging.env"), "ACCOUNT_MANAGER_LINODE_FIREWALL_ID=201\nACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=account-fw\n")
	writeFile(t, filepath.Join(root, "state", "cloud-admin-staging.env"), "ADMIN_LINODE_FIREWALL_ID=202\nADMIN_LINODE_FIREWALL_LABEL=admin-fw\n")

	targets, err := firewallTargets(root)
	if err != nil {
		t.Fatalf("firewallTargets returned error: %v", err)
	}
	byRole := map[string]firewallTarget{}
	for _, target := range targets {
		byRole[target.Role] = target
	}
	for role, wantID := range map[string]string{
		"edge":            "101",
		"api":             "102",
		"infra":           "103",
		"mqtt":            "104",
		"coturn":          "105",
		"account-manager": "201",
		"cloud-admin":     "202",
	} {
		if byRole[role].ID != wantID {
			t.Fatalf("%s firewall id = %q, want %q; targets=%#v", role, byRole[role].ID, wantID, targets)
		}
	}
	if byRole["edge"].Label != "video-cloud-stg-0529-edge" {
		t.Fatalf("edge label = %q", byRole["edge"].Label)
	}
}

func TestWritePlatformAdminSummaryRedactsPassword(t *testing.T) {
	root := t.TempDir()
	platformEnv := filepath.Join(root, "services", "account-manager", "account-manager-platform-admin.env")
	mkdirAll(t, filepath.Dir(platformEnv))
	writeFile(t, platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.test\nACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=super-secret-password\n")

	var out strings.Builder
	writePlatformAdminSummary(&out, provisionPaths{EnvRoot: root})
	body := out.String()
	if !strings.Contains(body, "username: root@example.test") {
		t.Fatalf("summary missing username:\n%s", body)
	}
	if !strings.Contains(body, "password: see "+platformEnv) {
		t.Fatalf("summary missing password file hint:\n%s", body)
	}
	if strings.Contains(body, "super-secret-password") {
		t.Fatalf("summary leaked password:\n%s", body)
	}
}

func TestSelectObjectReleaseSupportsHTTPObjectStorage(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket" && r.URL.Query().Get("prefix") == "releases/":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<ListBucketResult>
<Contents><Key>releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/manifest.json</Key><LastModified>2026-06-01T00:00:00Z</LastModified><Size>1</Size></Contents>
<Contents><Key>releases/rtk_video_cloud-v1.2.3/manifest.json</Key><LastModified>2026-05-31T00:00:00Z</LastModified><Size>1</Size></Contents>
<IsTruncated>false</IsTruncated>
</ListBucketResult>`))
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/manifest.json":
			_, _ = w.Write([]byte(`{"version":"ci-20260527-093000-abcdef123456","artifact_path":"releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_video_cloud-v1.2.3/manifest.json":
			_, _ = w.Write([]byte(`{"version":"v1.2.3","artifact_path":"releases/rtk_video_cloud-v1.2.3/v1.2.3.tar.gz"}`))
		case r.Method == http.MethodHead && r.URL.Path == "/test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz":
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected object storage request: %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	version, key, err := selectObjectRelease(map[string]string{
		"LINODE_OBJ_BUCKET":            "test-bucket",
		"LINODE_OBJ_ENDPOINT":          server.URL,
		"LINODE_OBJ_ACCESS_KEY_ID":     "access",
		"LINODE_OBJ_SECRET_ACCESS_KEY": "secret",
	}, "Video Cloud", "rtk_video_cloud", "")
	if err != nil {
		t.Fatalf("selectObjectRelease returned error: %v", err)
	}
	if version != "ci-20260527-093000-abcdef123456" {
		t.Fatalf("version = %q", version)
	}
	if key != "releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz" {
		t.Fatalf("key = %q", key)
	}
	if len(requests) < 4 || !strings.HasPrefix(requests[0], "GET /test-bucket?") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestMaterializeReleaseBundleSupportsHTTPObjectStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_account_manager-account-test/manifest.json":
			_, _ = w.Write([]byte(`{"version":"account-test","artifact_path":"releases/rtk_account_manager-account-test/account-test.tar.gz"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/test-bucket/releases/rtk_account_manager-account-test/account-test.tar.gz":
			_, _ = w.Write([]byte("bundle"))
		default:
			t.Fatalf("unexpected object storage request: %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer server.Close()

	out, err := materializeReleaseBundle(t.TempDir(), map[string]string{
		"LINODE_OBJ_BUCKET":            "test-bucket",
		"LINODE_OBJ_ENDPOINT":          server.URL,
		"LINODE_OBJ_ACCESS_KEY_ID":     "access",
		"LINODE_OBJ_SECRET_ACCESS_KEY": "secret",
	}, "rtk_account_manager", "account-test")
	if err != nil {
		t.Fatalf("materializeReleaseBundle returned error: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "bundle" {
		t.Fatalf("bundle = %q", string(data))
	}
}

func TestProvisionAccountManagerCommitSupportsNATS(t *testing.T) {
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "repos", "rtk_account_manager")
	mkdirAll(t, filepath.Join(repo, "internal", "broker"))
	writeFile(t, filepath.Join(repo, "internal", "broker", "broker.go"), `package broker

const AdapterLog = "log"
`)
	runGit(t, repo, "init")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "without nats")
	withoutNATS := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(repo, "internal", "broker", "broker.go"), `package broker

const AdapterLog = "log"
const AdapterNATS = "nats"
`)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "with nats")

	paths := provisionPaths{Workspace: workspace}
	supports, err := provisionAccountManagerCommitSupportsNATS(paths, withoutNATS)
	if err != nil {
		t.Fatalf("provisionAccountManagerCommitSupportsNATS returned error: %v", err)
	}
	if supports {
		t.Fatal("commit without NATS reported support")
	}
	supports, err = provisionAccountManagerCommitSupportsNATS(paths, "HEAD")
	if err != nil {
		t.Fatalf("provisionAccountManagerCommitSupportsNATS returned error: %v", err)
	}
	if !supports {
		t.Fatal("HEAD with NATS did not report support")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}
