package home100k

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeTinyEnvRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"env/stack.env":                                                             "STACK=test\n",
		"state/lke.env":                                                             "LKE_CLUSTER_ID=123\n",
		"state/lke-kubeconfig.yaml":                                                 "apiVersion: v1\nkind: Config\n",
		"state/video-cloud-staging.state.json":                                      `{"mqtt":{"host":"mqtt.example.invalid","tls_port":8883}}`,
		"services/video-cloud.env":                                                  "VIDEO_CLOUD_BASE_URL=http://example.invalid\n",
		"devices/test_device/loadtest.env":                                          "LOADTEST=1\n",
		"devices/test_device/summary.json":                                          `{"count":2}`,
		"devices/test_device/manifests/devices.csv":                                 "device_id,device_type\n",
		"devices/test_device/manifests/device_ids.txt":                              "load-device-0001\nload-device-0002\n",
		"devices/test_device/devices/light/load-device-0001/device.cert.pem":        "cert\n",
		"devices/test_device/devices/light/load-device-0001/device.key.pem":         "key\n",
		"devices/test_device/devices/light/load-device-0001/device.chain.pem":       "chain\n",
		"devices/test_device/bundles/light/load-device-0001.pem":                    "bundle\n",
		"devices/test_device/devices/smart_meter/load-device-0002/device.cert.pem":  "cert\n",
		"devices/test_device/devices/smart_meter/load-device-0002/device.key.pem":   "key\n",
		"devices/test_device/devices/smart_meter/load-device-0002/device.chain.pem": "chain\n",
		"devices/test_device/bundles/smart_meter/load-device-0002.pem":              "bundle\n",
		"artifacts/users/rtk-users-test.json":                                       `{"users":[]}`,
		"artifacts/device-bind/rtk-device-bind-test.json":                           `{"assignments":[]}`,
	}
	for rel, body := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	devicesJSON := []map[string]any{
		{"device_id": "load-device-0001", "device_type": "light"},
		{"device_id": "load-device-0002", "device_type": "smart_meter"},
	}
	if err := writeJSONFile(filepath.Join(root, "devices/test_device/manifests/devices.json"), devicesJSON); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeHome100KCoverageArtifacts(t *testing.T, envRoot string) {
	t.Helper()
	users := make([]map[string]any, 0, DefaultUserCount)
	for idx := 0; idx < DefaultUserCount; idx++ {
		users = append(users, map[string]any{"email": fmt.Sprintf("user-%04d@example.test", idx)})
	}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260616T000000Z.json"), map[string]any{"brandname": "RTK", "users": users}); err != nil {
		t.Fatal(err)
	}
	mix := proportionalMix(DefaultDeviceCount, homeDiverseDeviceMixBuckets())
	deviceTypes := make([]string, 0, len(mix))
	for deviceType := range mix {
		deviceTypes = append(deviceTypes, deviceType)
	}
	sort.Strings(deviceTypes)
	assignments := make([]map[string]any, 0, DefaultDeviceCount)
	for _, deviceType := range deviceTypes {
		for idx := 0; idx < mix[deviceType]; idx++ {
			deviceIndex := len(assignments)
			assignments = append(assignments, map[string]any{
				"assigned_email":  fmt.Sprintf("user-%04d@example.test", deviceIndex%DefaultUserCount),
				"device_id":       fmt.Sprintf("load-device-%06d", deviceIndex),
				"device_type":     deviceType,
				"service_options": []string{"mqtt"},
			})
		}
	}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260616T000000Z.json"), map[string]any{"brandname": "rtk", "assignments": assignments}); err != nil {
		t.Fatal(err)
	}
}

func readTarGzNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names[header.Name] = true
	}
}

func stubCommandOutputRunner(t *testing.T, fn func(name string, args ...string) (string, error)) {
	t.Helper()
	oldRunner := commandOutputRunner
	oldTimeoutRunner := commandOutputRunnerWithTimeout
	commandOutputRunner = fn
	commandOutputRunnerWithTimeout = func(_ time.Duration, name string, args ...string) (string, error) {
		return fn(name, args...)
	}
	t.Cleanup(func() {
		commandOutputRunner = oldRunner
		commandOutputRunnerWithTimeout = oldTimeoutRunner
	})
}

func TestExecutePlanPrintsDeterministicRunPlan(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"--", "plan",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(plan) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		`"devices": 100000`,
		`"users": 5000`,
		`"role": "mixed"`,
		`"target": {`,
		`"target_connects": 100000`,
		`"ramp_up_time": "1m"`,
		`"offline_desired_queue": 10000`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q:\n%s", want, out)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("plan output is not JSON: %v\n%s", err, out)
	}
	if _, ok := decoded["stages"]; ok {
		t.Fatalf("plan output still exposes stages:\n%s", out)
	}
	target, ok := decoded["target"].(map[string]any)
	if !ok {
		t.Fatalf("plan output missing target object:\n%s", out)
	}
	if target["target_connects"] != float64(100000) || target["ramp_up_time"] != "1m" {
		t.Fatalf("unexpected target object: %#v", target)
	}
}

func TestExecutePlanAcceptsConfiguredStageDurations(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"--", "plan",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--stage-warm-up", "1m",
		"--stage-steady", "3m",
		"--stage-cool-down", "30s",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(plan) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		`"ramp_up_time": "1m"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{`"steady_state"`, `"cool_down"`} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("plan output still exposes %q:\n%s", unwanted, out)
		}
	}
}

func TestExecutePlanRejectsLegacyStageProfileFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"--", "plan",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--stage-profile", "staged",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(plan --stage-profile staged) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined: -stage-profile") {
		t.Fatalf("stderr missing unsupported stage-profile error: %s", stderr.String())
	}
}

func TestExecuteRunWithoutLiveProvisionerProducesIncompleteReport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	outDir := t.TempDir()
	code := Execute([]string{
		"run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--ephemeral-vms",
		"--run-id", "run-cli",
		"--out-dir", outDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(run) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Status: INCOMPLETE",
		"Missing server evidence",
		"## IoT Device Shadow Scenario",
		"Offline desired queue",
		"## Target Results",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(stderr.String(), filepath.Join(outDir, "TEST_REPORT.md")) {
		t.Fatalf("stderr missing artifact path: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "results.json")); err != nil {
		t.Fatalf("missing results artifact: %v", err)
	}
}

func TestExecuteRunRequiresEphemeralVMs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(run without ephemeral-vms) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--ephemeral-vms is required") {
		t.Fatalf("stderr missing ephemeral requirement: %s", stderr.String())
	}
}

func TestExecuteProvisionVMsDefaultsToDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "9000",
		"--run-id", "run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(provision-vms dry-run) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"action": "provision-vm"`) || !strings.Contains(out, `"role": "mixed"`) {
		t.Fatalf("dry-run output missing lifecycle content:\n%s", out)
	}
}

func TestExecuteProvisionVMsRequiresConfirmForLive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "9000",
		"--run-id", "run-cli",
		"--live",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(provision-vms live without confirm) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--confirm-live is required") {
		t.Fatalf("stderr missing confirm-live requirement: %s", stderr.String())
	}
}

func TestExecuteProvisionVMsLiveWritesVMStateFile(t *testing.T) {
	outDir := t.TempDir()
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[],"page":1,"pages":1,"results":0}`))
			return
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		created++
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		label, _ := got["label"].(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":200,"label":"` + label + `","ipv4":["203.0.113.200"],"tags":["home-100k","run-cli","load-generator","mixed"]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--confirm-live",
		"--linode-token", "test-token",
		"--linode-endpoint", server.URL + "/v4",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(provision-vms live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if created != 5 {
		t.Fatalf("created requests = %d, want 5", created)
	}
	if _, err := os.Stat(filepath.Join(outDir, "vms.json")); err != nil {
		t.Fatalf("missing vms.json: %v", err)
	}
	if !strings.Contains(stdout.String(), `"vm_state_file"`) {
		t.Fatalf("stdout missing vm_state_file:\n%s", stdout.String())
	}
}

func TestExecuteProvisionVMsLiveReusesExistingLabelPoolAndBootsPoweredOffVMs(t *testing.T) {
	outDir := t.TempDir()
	posts := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v4/linode/instances":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[
				{"id":101,"label":"lg01","status":"offline","ipv4":["203.0.113.101"],"tags":["home-100k","run-cli","load-generator","mixed"]},
				{"id":102,"label":"lg02","status":"running","ipv4":["203.0.113.102"],"tags":["home-100k","run-cli","load-generator","mixed"]}
			],"page":1,"pages":1,"results":2}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/boot"):
			posts = append(posts, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v4/linode/instances":
			var got map[string]any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			label, _ := got["label"].(string)
			posts = append(posts, "create:"+label)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":200,"label":"` + label + `","ipv4":["203.0.113.200"],"tags":["home-100k","run-cli","load-generator","mixed"]}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--confirm-live",
		"--linode-token", "test-token",
		"--linode-endpoint", server.URL + "/v4",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(provision-vms live reuse) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joinedPosts := strings.Join(posts, "\n")
	if !strings.Contains(joinedPosts, "/v4/linode/instances/101/boot") {
		t.Fatalf("offline reused VM was not booted; posts:\n%s", joinedPosts)
	}
	if strings.Contains(joinedPosts, "create:lg01") || strings.Contains(joinedPosts, "create:lg02") {
		t.Fatalf("reused labels were recreated; posts:\n%s", joinedPosts)
	}
	if strings.Count(joinedPosts, "create:") != 3 {
		t.Fatalf("created count = %d, want 3; posts:\n%s", strings.Count(joinedPosts, "create:"), joinedPosts)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "vms.json"))
	if err != nil {
		t.Fatalf("read vms.json: %v", err)
	}
	if !strings.Contains(string(raw), `"id": 101`) || !strings.Contains(string(raw), `"id": 102`) {
		t.Fatalf("vms.json missing reused VMs:\n%s", raw)
	}
	if !strings.Contains(stdout.String(), `"reused"`) {
		t.Fatalf("stdout missing reused list:\n%s", stdout.String())
	}
}

func TestExecuteProvisionVMsLiveRejectsStaleFixedLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v4/linode/instances":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[
				{"id":101,"label":"lg01","status":"running","ipv4":["203.0.113.101"],"tags":["home-100k","old-run","load-generator","mixed"]}
			],"page":1,"pages":1,"results":1}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--live",
		"--confirm-live",
		"--linode-token", "test-token",
		"--linode-endpoint", server.URL + "/v4",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(provision-vms live stale label) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "existing Linode VM label lg01") {
		t.Fatalf("stderr missing stale label failure: %s", stderr.String())
	}
}

func TestExecuteProvisionVMsLiveUsesVMConfigFlagsWithoutLeakingRootPass(t *testing.T) {
	outDir := t.TempDir()
	keyFile := filepath.Join(outDir, "id_ed25519.pub")
	if err := os.WriteFile(keyFile, []byte("ssh-ed25519 AAAA-test operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[],"page":1,"pages":1,"results":0}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":200,"label":"lg01","ipv4":["203.0.113.200"],"tags":["home-100k","run-cli","load-generator","mixed"]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--confirm-live",
		"--linode-token", "test-token",
		"--linode-endpoint", server.URL + "/v4",
		"--linode-type", "g6-standard-4",
		"--linode-image", "linode/ubuntu24.04",
		"--root-pass", "secret-root-pass",
		"--authorized-key-file", keyFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(provision-vms live config) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if got["type"] != "g6-standard-4" || got["image"] != "linode/ubuntu24.04" || got["root_pass"] != "secret-root-pass" {
		t.Fatalf("payload missing VM config: %#v", got)
	}
	keys, _ := got["authorized_keys"].([]any)
	if len(keys) != 1 || keys[0] != "ssh-ed25519 AAAA-test operator" {
		t.Fatalf("authorized_keys = %#v", got["authorized_keys"])
	}
	if strings.Contains(stdout.String(), "secret-root-pass") {
		t.Fatalf("stdout leaked root password:\n%s", stdout.String())
	}
}

func TestExecuteDestroyVMsDefaultsToDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"destroy-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(destroy-vms dry-run) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"action": "destroy-vm"`) || !strings.Contains(out, `"role": "mixed"`) {
		t.Fatalf("destroy dry-run output missing lifecycle content:\n%s", out)
	}
}

func TestExecuteDestroyVMsLiveReadsStateAndDeletesVMs(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "lg02", PublicIPv4: "203.0.113.102"},
		},
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	deleted := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Fatalf("authorization = %q", auth)
		}
		deleted = append(deleted, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"destroy-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--live",
		"--confirm-live",
		"--linode-token", "test-token",
		"--linode-endpoint", server.URL + "/v4",
		"--vm-state-file", stateFile,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(destroy-vms live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if len(deleted) != 2 || !strings.HasSuffix(deleted[0], "/linode/instances/101") || !strings.HasSuffix(deleted[1], "/linode/instances/102") {
		t.Fatalf("deleted paths = %#v", deleted)
	}
	if !strings.Contains(stdout.String(), `"destroyed"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing destroyed VM ids:\n%s", stdout.String())
	}
}

func TestExecuteListVMsDefaultsToDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"list-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(list-vms dry-run) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"home-100k"`) || !strings.Contains(out, `"run-cli"`) || !strings.Contains(out, `"load-generator"`) {
		t.Fatalf("list-vms dry-run output missing tag filter:\n%s", out)
	}
}

func TestExecuteListVMsLiveReturnsTaggedVMs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if !strings.Contains(r.Header.Get("X-Filter"), "run-cli") || !strings.Contains(r.Header.Get("X-Filter"), "load-generator") {
			t.Fatalf("X-Filter = %q", r.Header.Get("X-Filter"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":101,"label":"lg01","ipv4":["203.0.113.101"]}],"page":1,"pages":1,"results":1}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"list-vms",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--live",
		"--linode-token", "test-token",
		"--linode-endpoint", server.URL + "/v4",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(list-vms live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"vms"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing listed VMs:\n%s", stdout.String())
	}
}

func TestExecuteSyncDefaultsToDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"sync",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(sync dry-run) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"action": "sync"`) || !strings.Contains(out, `"lg02"`) {
		t.Fatalf("sync dry-run output missing shard sync actions:\n%s", out)
	}
}

func TestExecuteSyncRejectsUnsupportedCredentialBundleFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"sync",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--credential-bundle-format", "expanded-pem",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(sync) code = 0, want failure; stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unsupported --credential-bundle-format "expanded-pem"`) {
		t.Fatalf("stderr missing unsupported format error:\n%s", stderr.String())
	}
}

func TestExecuteSyncLiveGeneratesAnsibleInventoryFromProvisionedVMs(t *testing.T) {
	outDir := t.TempDir()
	envRoot := writeTinyEnvRoot(t)
	writeHome100KCoverageArtifacts(t, envRoot)
	stateFile := filepath.Join(outDir, "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "lg02", PublicIPv4: "203.0.113.102"},
		},
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	var callsMu sync.Mutex
	calls := []string{}
	oldRunner := commandRunner
	commandRunner = func(name string, args ...string) error {
		callsMu.Lock()
		defer callsMu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "bash" && len(args) >= 2 && args[0] == "-lc" {
			if before, after, ok := strings.Cut(args[1], "go build -o '"); ok {
				_ = before
				if out, _, ok := strings.Cut(after, "'"); ok {
					if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
						return err
					}
					return os.WriteFile(out, []byte("test-binary\n"), 0o755)
				}
			}
		}
		if name == "ssh-keygen" {
			keyPath := args[len(args)-1]
			if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(keyPath, []byte("PRIVATE\n"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(keyPath+".pub", []byte("ssh-ed25519 test\n"), 0o644)
		}
		return nil
	}
	defer func() { commandRunner = oldRunner }()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"sync",
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "9000",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--vm-state-file", stateFile,
		"--remote-workspace", "/root/rtk_cloud_workspace",
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/lke",
		"--ssh-user", "root",
		"--ssh-key", "/tmp/test-key",
		"--generator-hosts-override-ip", "172.232.190.230",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(sync live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"bash -lc GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o '" + filepath.Join(outDir, "bin", "home-100k-linux-amd64") + "' ./loadtests/home-100k/cmd/home-100k",
		"bash -lc GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o '" + filepath.Join(outDir, "bin", "cloud-mqtt-test-linux-amd64") + "' ./scripts/go/cloud-mqtt-test",
		"env ANSIBLE_CONFIG=loadtests/home-100k/ansible/ansible.cfg ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json") + " loadtests/home-100k/ansible/sync.yml",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sync live commands missing %q:\n%s", want, joined)
		}
	}
	inventoryRaw, err := os.ReadFile(filepath.Join(outDir, "ansible", "inventory.json"))
	if err != nil {
		t.Fatalf("missing generated ansible inventory: %v", err)
	}
	inventory := string(inventoryRaw)
	for _, want := range []string{`"ansible_host": "203.0.113.101"`, `"shard_index": 0`, `"run_id": "run-cli"`, `"role": "mixed"`} {
		if !strings.Contains(inventory, want) {
			t.Fatalf("inventory missing %q:\n%s", want, inventory)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "ansible", "extra-vars.json")); err != nil {
		t.Fatalf("missing generated ansible extra vars: %v", err)
	}
	extraVarsRaw, err := os.ReadFile(filepath.Join(outDir, "ansible", "extra-vars.json"))
	if err != nil {
		t.Fatalf("read extra vars: %v", err)
	}
	var extraVars map[string]any
	if err := json.Unmarshal(extraVarsRaw, &extraVars); err != nil {
		t.Fatalf("decode extra vars: %v", err)
	}
	for _, key := range []string{"local_runner", "local_env_root", "local_out_dir"} {
		value, _ := extraVars[key].(string)
		if !filepath.IsAbs(value) {
			t.Fatalf("extra vars %s = %q, want absolute path", key, extraVars[key])
		}
	}
	for _, key := range []string{"local_artifact_store", "fanout_private_key"} {
		value, _ := extraVars[key].(string)
		if !filepath.IsAbs(value) {
			t.Fatalf("extra vars %s = %q, want absolute path", key, extraVars[key])
		}
	}
	localRTKCloud, _ := extraVars["local_rtk_cloud"].(string)
	if !filepath.IsAbs(localRTKCloud) {
		t.Fatalf("extra vars local_rtk_cloud = %q, want absolute path", extraVars["local_rtk_cloud"])
	}
	localCloudMQTTTest, _ := extraVars["local_cloud_mqtt_test"].(string)
	if !filepath.IsAbs(localCloudMQTTTest) {
		t.Fatalf("extra vars local_cloud_mqtt_test = %q, want absolute path", extraVars["local_cloud_mqtt_test"])
	}
	mqttConcurrency, ok := extraVars["mqtt_concurrency"].(float64)
	if !ok || mqttConcurrency != DefaultLiveMQTTConcurrency {
		t.Fatalf("extra vars mqtt_concurrency = %v, want %d", extraVars["mqtt_concurrency"], DefaultLiveMQTTConcurrency)
	}
	commandConcurrency, ok := extraVars["command_concurrency"].(float64)
	if !ok || commandConcurrency != DefaultLiveCommandConcurrency {
		t.Fatalf("extra vars command_concurrency = %v, want %d", extraVars["command_concurrency"], DefaultLiveCommandConcurrency)
	}
	if extraVars["shadow_command_timeout"] != DefaultShadowCommandTimeout {
		t.Fatalf("extra vars shadow_command_timeout = %q, want %s", extraVars["shadow_command_timeout"], DefaultShadowCommandTimeout)
	}
	if extraVars["generator_hosts_override_ip"] != "172.232.190.230" {
		t.Fatalf("extra vars generator_hosts_override_ip = %q, want 172.232.190.230", extraVars["generator_hosts_override_ip"])
	}
	if extraVars["credential_bundle_format"] != "sqlite-gzip" {
		t.Fatalf("extra vars credential_bundle_format = %q, want sqlite-gzip", extraVars["credential_bundle_format"])
	}
	if extraVars["vm_label_prefix"] != "lg" {
		t.Fatalf("extra vars vm_label_prefix = %q, want lg", extraVars["vm_label_prefix"])
	}
	if extraVars["device_count"] != float64(9000) {
		t.Fatalf("extra vars device_count = %#v, want 9000", extraVars["device_count"])
	}
	if extraVars["user_count"] != float64(450) {
		t.Fatalf("extra vars user_count = %#v, want 450", extraVars["user_count"])
	}
	if extraVars["devices_per_user"] != float64(20) {
		t.Fatalf("extra vars devices_per_user = %#v, want 20", extraVars["devices_per_user"])
	}
	if extraVars["runner_nofile_limit"] != float64(1048576) {
		t.Fatalf("extra vars runner_nofile_limit = %#v, want 1048576", extraVars["runner_nofile_limit"])
	}
	var inventoryDoc struct {
		All struct {
			Children map[string]struct {
				Hosts map[string]map[string]any `json:"hosts"`
			} `json:"children"`
		} `json:"all"`
	}
	if err := json.Unmarshal(inventoryRaw, &inventoryDoc); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	orchestraHosts := inventoryDoc.All.Children["home_100k_orchestra"].Hosts
	if len(orchestraHosts) != 1 {
		t.Fatalf("home_100k_orchestra hosts = %#v, want exactly one orchestra", orchestraHosts)
	}
	if _, ok := orchestraHosts["lg01"]; !ok {
		t.Fatalf("home_100k_orchestra = %#v, want first VM lg01", orchestraHosts)
	}
	localManifest, _ := inventoryDoc.All.Children["home_100k"].Hosts["lg01"]["local_shard_manifest"].(string)
	if !filepath.IsAbs(localManifest) {
		t.Fatalf("inventory local_shard_manifest = %q, want absolute path", localManifest)
	}
	localArchive, _ := inventoryDoc.All.Children["home_100k"].Hosts["lg01"]["local_env_archive"].(string)
	if !filepath.IsAbs(localArchive) {
		t.Fatalf("inventory local_env_archive = %q, want absolute path", localArchive)
	}
	if _, err := os.Stat(localArchive); err != nil {
		t.Fatalf("missing env archive: %v", err)
	}
	archiveNames := readTarGzNames(t, localArchive)
	for _, want := range []string{"loadtests/home-100k/credentials/lg01.sqlite.gz", "loadtests/home-100k/credentials/lg01.manifest.json"} {
		if !archiveNames[want] {
			t.Fatalf("shard env archive missing %q", want)
		}
	}
	commonArchive := filepath.Join(outDir, "artifact-store", "common", "env-common.tar.gz")
	if _, err := os.Stat(commonArchive); err != nil {
		t.Fatalf("missing common env archive: %v", err)
	}
	commonNames := readTarGzNames(t, commonArchive)
	for _, want := range []string{"env/stack.env", "state/lke.env", "state/lke-kubeconfig.yaml", "state/video-cloud-staging.state.json", "services/video-cloud.env", "devices/test_device/manifests/devices.json", "artifacts/users/rtk-users-20260616T000000Z.json", "artifacts/device-bind/rtk-device-bind-20260616T000000Z.json"} {
		if !commonNames[want] {
			t.Fatalf("common env archive missing %q", want)
		}
	}
	forbiddenPrefixes := []string{
		"devices/test_device/devices/",
		"devices/test_device/bundles/",
	}
	for name := range archiveNames {
		for _, prefix := range forbiddenPrefixes {
			if strings.HasPrefix(name, prefix) {
				t.Fatalf("env archive must use sqlite credential bundle, found expanded credential path %q", name)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "sync-telemetry.json")); err != nil {
		t.Fatalf("missing sync telemetry placeholder: %v", err)
	}
	manifest0Raw, err := os.ReadFile(filepath.Join(outDir, "shard-manifests", "lg01.json"))
	if err != nil {
		t.Fatalf("missing lg01 manifest: %v", err)
	}
	manifest1Raw, err := os.ReadFile(filepath.Join(outDir, "shard-manifests", "lg02.json"))
	if err != nil {
		t.Fatalf("missing lg02 manifest: %v", err)
	}
	manifest0 := string(manifest0Raw)
	manifest1 := string(manifest1Raw)
	for _, want := range []string{`"role": "device-mqtt"`, `"start": 0`, `"end": 1800`, `"role": "user-app"`, `"start": 0`, `"end": 90`} {
		if !strings.Contains(manifest0, want) {
			t.Fatalf("lg01 manifest missing %q:\n%s", want, manifest0)
		}
	}
	for _, want := range []string{`"role": "device-mqtt"`, `"start": 1800`, `"end": 3600`, `"role": "user-app"`, `"start": 90`, `"end": 180`} {
		if !strings.Contains(manifest1, want) {
			t.Fatalf("lg02 manifest missing %q:\n%s", want, manifest1)
		}
	}
	if !strings.Contains(stdout.String(), `"synced"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing synced VMs:\n%s", stdout.String())
	}
}

func TestWriteEnvRsyncFilterFallsBackToDevicesJSONWhenCSVHasOnlyHeader(t *testing.T) {
	envRoot := t.TempDir()
	manifestDir := filepath.Join(envRoot, "devices", "test_device", "manifests")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "devices.csv"), []byte("device_id,device_type\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	devicesJSON := []map[string]any{
		{"device_id": "load-device-0001", "device_type": "light"},
		{"device_id": "load-device-0002", "device_type": "smart_meter"},
	}
	if err := writeJSONFile(filepath.Join(manifestDir, "devices.json"), devicesJSON); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "filter")
	assignment := VMAssignment{
		Label: "lg01",
		Index: 0,
		Role:  "mixed",
		TaskShards: []Shard{{
			Role:  "device-mqtt",
			Start: 0,
			End:   2,
			Count: 2,
		}},
	}
	if err := writeEnvRsyncFilter(out, envRoot, assignment); err != nil {
		t.Fatalf("writeEnvRsyncFilter: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	filter := string(raw)
	for _, want := range []string{
		"+ /devices/test_device/devices/light/load-device-0001/***",
		"+ /devices/test_device/bundles/light/load-device-0001.pem",
		"+ /devices/test_device/devices/smart_meter/load-device-0002/***",
		"+ /devices/test_device/bundles/smart_meter/load-device-0002.pem",
	} {
		if !strings.Contains(filter, want) {
			t.Fatalf("filter missing %q:\n%s", want, filter)
		}
	}
}

func TestAnsibleSyncUsesCompressedEnvArchive(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "sync.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`artifact_env_archive`, `Fan out compressed env-root shard archive from orchestra`, `ansible.posix.synchronize`, `Extract env-root shard archive`, `env-root.tar.gz`} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync.yml missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{`--filter=merge`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("sync.yml still contains %q:\n%s", forbidden, body)
		}
	}
	if strings.Contains(body, `--no-compress`) {
		t.Fatalf("sync.yml uses macOS-incompatible rsync option --no-compress:\n%s", body)
	}
	if strings.Contains(body, `--inplace`) {
		t.Fatalf("sync.yml uses rsync --inplace, which conflicts with ansible synchronize delay-updates:\n%s", body)
	}
	if strings.Contains(body, `--info=progress2`) {
		t.Fatalf("sync.yml uses GNU-only rsync --info=progress2; macOS /usr/bin/rsync does not support it:\n%s", body)
	}
	if !strings.Contains(body, `compress: false`) {
		t.Fatalf("sync.yml should disable rsync compression through ansible synchronize, not raw rsync opts:\n%s", body)
	}
}

func TestAnsibleSyncSkipsUnchangedArtifactsByChecksum(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "sync.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"remote_runner_stat",
		"remote_env_archive_stat",
		"runner_needs_upload",
		"env_archive_needs_upload",
		"Extract env-root shard archive when changed",
		"files_skipped",
		"bytes_skipped",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync.yml missing checksum-cache marker %q:\n%s", want, body)
		}
	}
}

func TestAnsibleSyncUsesOrchestraFanout(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "sync.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"hosts: home_100k_orchestra",
		"Upload artifact store to orchestra",
		"Install per-run fanout SSH key on orchestra",
		"Rebuild fanout known_hosts on orchestra",
		"ssh-keyscan",
		"Fan out runner binary from orchestra",
		"delegate_to: \"{{ groups['home_100k_orchestra'][0] }}\"",
		"rsync_timeout",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync.yml missing orchestra fanout marker %q:\n%s", want, body)
		}
	}
}

func TestAnsibleSyncRetriesTransientFanoutRsyncFailures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "sync.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, register := range []string{
		"runner_copy",
		"rtk_cloud_copy",
		"cloud_mqtt_test_copy",
		"manifest_copy",
		"common_env_archive_copy",
		"env_archive_copy",
	} {
		if !strings.Contains(body, "register: "+register) {
			t.Fatalf("sync.yml missing fanout register %q:\n%s", register, body)
		}
		if !strings.Contains(body, "until: "+register+".rc == 0") {
			t.Fatalf("sync.yml should retry fanout rsync for %s until rc == 0:\n%s", register, body)
		}
	}
	if !strings.Contains(body, "retries: 3") || !strings.Contains(body, "delay: 5") {
		t.Fatalf("sync.yml should retry transient fanout SSH/rsync failures with a short delay:\n%s", body)
	}
}

func TestEnvArchiveUsesBoundDeviceShardSelection(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	users := map[string]any{"users": []map[string]any{
		{"email": "user-00@example.test"},
		{"email": "user-01@example.test"},
		{"email": "user-02@example.test"},
	}}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260615.json"), users); err != nil {
		t.Fatal(err)
	}
	bind := map[string]any{
		"brandname": "rtk",
		"assignments": []map[string]any{
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-01@example.test", "device_id": "load-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-02@example.test", "device_id": "load-device-0003", "device_type": "air_conditioner", "service_options": []string{"mqtt"}},
		},
	}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260615.json"), bind); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"devices/test_device/devices/air_conditioner/load-device-0003/device.cert.pem",
		"devices/test_device/devices/air_conditioner/load-device-0003/device.key.pem",
		"devices/test_device/devices/air_conditioner/load-device-0003/device.chain.pem",
		"devices/test_device/bundles/air_conditioner/load-device-0003.pem",
	} {
		path := filepath.Join(envRoot, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("pem\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := NewPlan(PlanOptions{EnvRoot: envRoot, Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	assignment := plan.Assignments[1]
	archivePath := filepath.Join(t.TempDir(), "env.tar.gz")
	if err := writeEnvArchive(archivePath, plan, assignment); err != nil {
		t.Fatalf("writeEnvArchive: %v", err)
	}
	names := readTarGzNames(t, archivePath)
	for _, want := range []string{
		"loadtests/home-100k/credentials/lg02.sqlite.gz",
		"loadtests/home-100k/credentials/lg02.manifest.json",
	} {
		if !names[want] {
			t.Fatalf("archive missing shard-selected credential bundle %q", want)
		}
	}
	for name := range names {
		if strings.HasPrefix(name, "devices/test_device/devices/") || strings.HasPrefix(name, "devices/test_device/bundles/") {
			t.Fatalf("archive included expanded device credential %q", name)
		}
	}
}

func TestValidatePlanDataCoverageRejectsInsufficientUsersAndBoundDevices(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	users := map[string]any{"users": []map[string]any{
		{"email": "user-00@example.test"},
		{"email": "user-01@example.test"},
	}}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260615.json"), users); err != nil {
		t.Fatal(err)
	}
	bind := map[string]any{
		"brandname": "rtk",
		"assignments": []map[string]any{
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-01@example.test", "device_id": "load-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
		},
	}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260615.json"), bind); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(PlanOptions{EnvRoot: envRoot, Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}

	err = validatePlanDataCoverage(envRoot, plan)
	if err == nil {
		t.Fatalf("validatePlanDataCoverage succeeded, want insufficient data error")
	}
	for _, want := range []string{
		"users available=2 required=5000",
		"eligible devices available=2 required=100000",
		"light available=1 required=18000",
		"air_conditioner available=0 required=10000",
		"smart_meter available=1 required=8000",
		"smart_plug available=0 required=12000",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("coverage error missing %q:\n%v", want, err)
		}
	}
}

func TestValidatePlanDataCoverageAcceptsMatchingUsersAndBoundDevices(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	users := map[string]any{"users": []map[string]any{
		{"email": "user-00@example.test"},
		{"email": "user-01@example.test"},
	}}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260615.json"), users); err != nil {
		t.Fatal(err)
	}
	bind := map[string]any{
		"brandname": "rtk",
		"assignments": []map[string]any{
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-01@example.test", "device_id": "load-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
		},
	}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260615.json"), bind); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(PlanOptions{EnvRoot: envRoot, Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	plan.Conditions.Users = 2
	plan.Conditions.Devices = 2
	plan.DeviceMix = map[string]int{"light": 1, "smart_meter": 1}

	if err := validatePlanDataCoverage(envRoot, plan); err != nil {
		t.Fatalf("validatePlanDataCoverage() error = %v", err)
	}
}

func TestEnvArchiveOnlyIncludesLatestUsersAndDeviceBindArtifacts(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	oldUsers := filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260614T010000Z.json")
	newUsers := filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260615T010000Z.json")
	for _, path := range []string{oldUsers, newUsers} {
		if err := writeJSONFile(path, map[string]any{"brandname": "RTK", "users": []map[string]any{{"email": "user-00@example.test"}}}); err != nil {
			t.Fatal(err)
		}
	}
	oldBind := filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260614T010000Z.json")
	newBind := filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260615T010000Z.json")
	completeBind := map[string]any{
		"brandname": "RTK",
		"assignments": []map[string]any{
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0002", "device_type": "air_conditioner", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0003", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
		},
	}
	for _, path := range []string{oldBind, newBind} {
		if err := writeJSONFile(path, completeBind); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{
		"devices/test_device/devices/light/load-device-0001/device.cert.pem",
		"devices/test_device/devices/light/load-device-0001/device.key.pem",
		"devices/test_device/devices/light/load-device-0001/device.chain.pem",
		"devices/test_device/bundles/light/load-device-0001.pem",
	} {
		path := filepath.Join(envRoot, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("pem\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := NewPlan(PlanOptions{EnvRoot: envRoot, Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "env-common.tar.gz")
	if err := writeCommonEnvArchive(archivePath, plan); err != nil {
		t.Fatal(err)
	}
	names := readTarGzNames(t, archivePath)
	for _, want := range []string{
		"artifacts/users/rtk-users-20260615T010000Z.json",
		"artifacts/device-bind/rtk-device-bind-20260615T010000Z.json",
	} {
		if !names[want] {
			t.Fatalf("archive missing latest artifact %q", want)
		}
	}
	for _, forbidden := range []string{
		"artifacts/users/rtk-users-20260614T010000Z.json",
		"artifacts/device-bind/rtk-device-bind-20260614T010000Z.json",
	} {
		if names[forbidden] {
			t.Fatalf("archive included stale artifact %q", forbidden)
		}
	}
}

func TestEnvArchiveSelectsArtifactsByFilenameTimestampNotMTime(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	oldUsers := map[string]any{"users": []map[string]any{
		{"email": "old-user@example.test"},
	}}
	newUsers := map[string]any{"users": []map[string]any{
		{"email": "new-user@example.test"},
	}}
	oldUsersPath := filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260615T010000Z.json")
	newUsersPath := filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260615T020000Z.json")
	if err := writeJSONFile(oldUsersPath, oldUsers); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(newUsersPath, newUsers); err != nil {
		t.Fatal(err)
	}
	oldBind := map[string]any{
		"brandname": "rtk",
		"assignments": []map[string]any{
			{"assigned_email": "old-user@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "old-user@example.test", "device_id": "load-device-0002", "device_type": "air_conditioner", "service_options": []string{"mqtt"}},
			{"assigned_email": "old-user@example.test", "device_id": "load-device-0003", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
		},
	}
	newBind := map[string]any{
		"brandname": "rtk",
		"assignments": []map[string]any{
			{"assigned_email": "new-user@example.test", "device_id": "load-device-9001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "new-user@example.test", "device_id": "load-device-9002", "device_type": "air_conditioner", "service_options": []string{"mqtt"}},
			{"assigned_email": "new-user@example.test", "device_id": "load-device-9003", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
		},
	}
	oldBindPath := filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260615T010000Z.json")
	newBindPath := filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260615T020000Z.json")
	if err := writeJSONFile(oldBindPath, oldBind); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(newBindPath, newBind); err != nil {
		t.Fatal(err)
	}
	newTime := time.Now().Add(-time.Hour)
	oldTime := time.Now()
	for _, path := range []string{newUsersPath, newBindPath} {
		if err := os.Chtimes(path, newTime, newTime); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{oldUsersPath, oldBindPath} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := loadShardDeviceRowsFromArtifacts(envRoot, Plan{Conditions: TestConditions{Brandname: "RTK"}}, VMAssignment{
		Index:      0,
		TaskShards: []Shard{{Role: "device-mqtt", Count: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, row := range rows {
		got = append(got, row.DeviceID)
	}
	want := []string{"load-device-9001", "load-device-9002", "load-device-9003"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("selected devices = %v, want %v", got, want)
	}
}

func TestEnvArchiveIsDeterministicForUnchangedShardInputs(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	users := map[string]any{"users": []map[string]any{
		{"email": "user-00@example.test"},
	}}
	bind := map[string]any{"assignments": []map[string]any{
		{"device_id": "load-device-0001", "device_type": "light", "assigned_email": "user-00@example.test", "service_options": []string{"mqtt"}},
	}}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "users", "rtk-users-20260615.json"), users); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(envRoot, "artifacts", "device-bind", "rtk-device-bind-20260615.json"), bind); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(PlanOptions{EnvRoot: envRoot, Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	assignment := plan.Assignments[0]
	outDir := t.TempDir()
	first := filepath.Join(outDir, "first.tar.gz")
	second := filepath.Join(outDir, "second.tar.gz")
	if err := writeEnvArchive(first, plan, assignment); err != nil {
		t.Fatalf("write first archive: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := writeEnvArchive(second, plan, assignment); err != nil {
		t.Fatalf("write second archive: %v", err)
	}
	firstSHA, err := fileSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	secondSHA, err := fileSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstSHA != secondSHA {
		t.Fatalf("archive sha differs for unchanged inputs: first=%s second=%s", firstSHA, secondSHA)
	}
}

func TestAnsibleSyncInstallsKubectlForLiveRunner(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "sync.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"Install kubectl for staging service port-forwarding", "https://dl.k8s.io/release/{{ kubectl_version | default('v1.35.3') }}/bin/linux/amd64/kubectl", "/usr/local/bin/kubectl"} {
		if !strings.Contains(body, want) {
			t.Fatalf("sync.yml missing %q:\n%s", want, body)
		}
	}
}

func TestAnsibleConfigDisablesSSHCompressionForPreCompressedArtifacts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "ansible.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`ssh_args`, `Compression=no`} {
		if !strings.Contains(body, want) {
			t.Fatalf("ansible.cfg missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, ` -C`) {
		t.Fatalf("ansible.cfg should not enable SSH compression for gzip artifacts:\n%s", body)
	}
}

func TestHome100KScriptKeepsVMsForFailedOrIncompleteRunsByDefault(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"should_shutdown_after_workflow()",
		"[[ \"$shutdown_on_error\" == \"1\" ]]",
		"[[ \"$workflow_rc\" != \"0\" ]]",
		"[[ \"$(current_report_status)\" == \"PASS\" ]]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("home-100k.sh missing shutdown gate marker %q:\n%s", want, body)
		}
	}
}

func TestHome100KScriptIncludesK8SRuntimeHealthProbe(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"k8s-runtime-health.log",
		"HOME100K_K8S_RUNTIME_HEALTH_STATUS",
		"HOME100K_K8S_RUNTIME_HEALTH_SINCE",
		"k8s_runtime_health_status()",
		"kubectl_prefix=(env \"KUBECONFIG=$kubeconfig\" \"$kubectl_bin\")",
		"logs deploy/video-cloud-api",
		"emqx ctl listeners",
		"k8s_runtime_health_status",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("home-100k.sh missing k8s runtime health marker %q:\n%s", want, body)
		}
	}
}

func TestHome100KResumeLiveSkipsProvisionWhenVMStateExists(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	_, resume, ok := strings.Cut(body, "\n  workflow-resume-live)")
	if !ok {
		t.Fatal("home-100k.sh missing workflow-resume-live case")
	}
	resume, _, ok = strings.Cut(resume, "\n  *)")
	if !ok {
		t.Fatal("home-100k.sh workflow-resume-live case is not terminated")
	}
	if strings.Contains(resume, "run_home100k provision-vms") {
		t.Fatalf("workflow-resume-live must reuse existing vms.json and skip provision-vms:\n%s", resume)
	}
	for _, want := range []string{`requires existing VM state`, `set_phase "sync"`, `run_home100k sync`} {
		if !strings.Contains(resume, want) {
			t.Fatalf("workflow-resume-live missing %q:\n%s", want, resume)
		}
	}
}

func TestHome100KScriptEnvOverridesDescriptionRampUpAndRuntimeWindows(t *testing.T) {
	outDir := t.TempDir()
	descriptionFile := filepath.Join(outDir, "description.env")
	if err := os.WriteFile(descriptionFile, []byte(strings.Join([]string{
		"HOME100K_RAMP_UP_TIME=9m",
		"HOME100K_MEASUREMENT_WINDOW=9m",
		"HOME100K_POST_RUN_COLLECTION=9m",
		"HOME100K_DEVICES=12000",
		"HOME100K_DEVICES_PER_USER=10",
		"HOME100K_MQTT_ADDR=127.0.0.1:8883",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "plan")
	cmd.Env = append(os.Environ(),
		"HOME100K_DESCRIPTION_FILE="+descriptionFile,
		"HOME100K_RUN_ID=test-env-priority",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
		"HOME100K_RAMP_UP_TIME=15s",
		"HOME100K_MEASUREMENT_WINDOW=45s",
		"HOME100K_POST_RUN_COLLECTION=15s",
		"HOME100K_DEVICES=9000",
		"HOME100K_DEVICES_PER_USER=20",
	)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("home-100k.sh plan failed: %v\n%s", err, raw)
	}
	body := string(raw)
	for _, want := range []string{`"ramp_up_time": "15s"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("plan missing env override %q:\n%s", want, body)
		}
	}
	for _, want := range []string{`"devices": 9000`, `"users": 450`, `"target_connects": 9000`} {
		if !strings.Contains(body, want) {
			t.Fatalf("plan missing size override %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"steady_state"`) || strings.Contains(body, `"cool_down"`) {
		t.Fatalf("plan exposed internal measurement timing:\n%s", body)
	}
	if strings.Contains(body, `"ramp_up_time": "9m"`) {
		t.Fatalf("description file overrode explicit env:\n%s", body)
	}
	if strings.Contains(body, `"devices": 12000`) {
		t.Fatalf("description file size overrode explicit env:\n%s", body)
	}
}

func TestHome100KScriptAutoDiscoversPublicMQTTOnlyForLiveCommands(t *testing.T) {
	outDir := t.TempDir()
	descriptionFile := filepath.Join(outDir, "description.env")
	if err := os.WriteFile(descriptionFile, []byte(strings.Join([]string{
		"HOME100K_ENV_ROOT=cloud_env/staging/lke",
		"HOME100K_BRANDNAME=RTK",
		"HOME100K_REGION=us-sea",
		"HOME100K_MQTT_ADDR=auto-public-mqtt",
		"HOME100K_MQTT_PUBLIC_LB_COUNT=1",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(outDir, "go.log")
	goStub := filepath.Join(binDir, "go")
	if err := os.WriteFile(goStub, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> "+shellQuoteForTest(goLog)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	kubectlLog := filepath.Join(outDir, "kubectl.log")
	kubectlStub := filepath.Join(binDir, "kubectl")
	kubectlBody := `#!/usr/bin/env bash
printf '%s\n' "$*" >> ` + shellQuoteForTest(kubectlLog) + `
cat <<'JSON'
{"items":[
  {"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.20"}]}}},
  {"status":{"loadBalancer":{"ingress":[{"ip":"203.0.113.10"}]}}}
]}
JSON
`
	if err := os.WriteFile(kubectlStub, []byte(kubectlBody), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	commonEnv := append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME100K_DESCRIPTION_FILE="+descriptionFile,
		"HOME100K_RUN_ID=test-auto-mqtt",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
	)
	planCmd := exec.Command("bash", script, "plan")
	planCmd.Env = commonEnv
	if raw, err := planCmd.CombinedOutput(); err != nil {
		t.Fatalf("plan failed: %v\n%s", err, raw)
	}
	if _, err := os.Stat(kubectlLog); err == nil {
		t.Fatal("plan should not call kubectl for HOME100K_MQTT_ADDR=auto-public-mqtt")
	}

	stateFile := filepath.Join(outDir, "report", "vms.json")
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, []byte(`{"created":[{"id":101,"label":"lg01","ipv4":["203.0.113.101"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	syncCmd := exec.Command("bash", script, "sync", "--live", "--remote-workspace", "/root/ws", "--remote-env-root", "/root/ws/cloud_env/staging/lke", "--ssh-key", "/tmp/key")
	syncCmd.Env = commonEnv
	if raw, err := syncCmd.CombinedOutput(); err != nil {
		t.Fatalf("sync failed: %v\n%s", err, raw)
	}
	goRaw, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatalf("read go log: %v", err)
	}
	if !strings.Contains(string(goRaw), "--mqtt-addr 203.0.113.10:8883") {
		t.Fatalf("sync did not pass discovered mqtt addr:\n%s", goRaw)
	}
	if strings.Contains(string(goRaw), "203.0.113.20:8883") {
		t.Fatalf("sync should limit auto-discovered mqtt addr count to 1:\n%s", goRaw)
	}
}

func shellQuoteForTest(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func TestAnsibleStartRunnerUsesPrebuiltCloudMQTTTestAndDaemonWait(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "start-runner.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"runner-daemon",
		`$2 == "home-100k" && index($0, "runner-daemon")`,
		"ss -ltnp 'sport = :18080'",
		"runner daemon listen port :18080 is still in use after cleanup",
		"CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT",
		"{{ remote_home_100k_dir }}/bin/cloud-mqtt-test",
		"generator_hosts_override_ip | default('')",
		"video-cloud-staging.realtekconnect.com",
		"device.video-cloud-staging.realtekconnect.com",
		"account-manager.video-cloud-staging.realtekconnect.com",
		`--devices "{{ device_count }}"`,
		`--users "{{ user_count }}"`,
		`--devices-per-user "{{ devices_per_user }}"`,
		`--vm-label-prefix "{{ vm_label_prefix | default('lg') }}"`,
		`--mqtt-concurrency "{{ mqtt_concurrency | default(1000) }}"`,
		`runner_nofile_limit="{{ runner_nofile_limit | default(1048576) }}"`,
		`ulimit -n "$runner_nofile_limit"`,
		"READY_WAIT",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("start-runner.yml missing %q:\n%s", want, body)
		}
	}
}

func TestExecuteRunStagesProducesStageMetrics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"run-stages",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(run-stages) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		`"stage_results"`,
		`"name": "target"`,
		`"desired_reported_convergence_rate_percent": 100`,
		`"offline_desired_convergence_rate_percent": 100`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("run-stages output missing %q:\n%s", want, out)
		}
	}
}

func TestExecuteRunStagesLiveRunnerModeRefusesSampleFallback(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"run-stages",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--runner-mode", "live",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(run-stages --runner-mode live) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "refusing to run sampled actor executor") {
		t.Fatalf("stderr missing sampled fallback refusal: %s", stderr.String())
	}
}

func TestExecuteRunStagesLiveRequiresPublicMQTTEndpoint(t *testing.T) {
	outDir := t.TempDir()
	stateFile := filepath.Join(outDir, "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"},
		},
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"run-stages",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--runner-mode", "live",
		"--vm-state-file", stateFile,
		"--remote-workspace", "/root/rtk_cloud_workspace",
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/lke",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(run-stages live without mqtt addr) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "public MQTT endpoint") {
		t.Fatalf("stderr missing public MQTT endpoint refusal: %s", stderr.String())
	}
}

func TestExecuteRunStagesLiveStartsRunnerDaemonsThenHostCoordinator(t *testing.T) {
	outDir := t.TempDir()
	envRoot := writeTinyEnvRoot(t)
	writeHome100KCoverageArtifacts(t, envRoot)
	stateFile := filepath.Join(outDir, "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "lg02", PublicIPv4: "203.0.113.102"},
		},
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	var callsMu sync.Mutex
	calls := []string{}
	oldRunner := commandRunner
	oldCoordinator := runHostCoordinator
	commandRunner = func(name string, args ...string) error {
		callsMu.Lock()
		defer callsMu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	coordinatorCalled := false
	runHostCoordinator = func(vms []LinodeVM, plan Plan, runID string, values workflowFlagValues) (StartCoordination, error) {
		coordinatorCalled = true
		if len(vms) != 2 {
			t.Fatalf("coordinator vms len = %d, want 2", len(vms))
		}
		return StartCoordination{
			Mode:         "host-coordinator",
			ReadyBarrier: "2/2",
			StartDelayMS: 3000,
			MaxSkewMS:    25,
			VMs: []VMStartTelemetry{{
				Label:                  "lg01",
				IP:                     "203.0.113.101",
				ReadyAt:                "2026-06-15T00:00:00Z",
				StartSignalReceivedAt:  "2026-06-15T00:00:01Z",
				StageStartedAt:         "2026-06-15T00:00:04Z",
				FirstConnectAt:         "2026-06-15T00:00:04.010Z",
				CoordinatorDisconnects: 0,
				Status:                 "completed",
			}},
		}, nil
	}
	defer func() {
		commandRunner = oldRunner
		runHostCoordinator = oldCoordinator
	}()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"run-stages",
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--vm-state-file", stateFile,
		"--remote-workspace", "/root/rtk_cloud_workspace",
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/lke",
		"--remote-out-root", "/var/lib/home-100k",
		"--ssh-user", "root",
		"--ssh-key", "/tmp/test-key",
		"--mqtt-addr", "mqtt-public.example.test:8883",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(run-stages live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"env ANSIBLE_CONFIG=loadtests/home-100k/ansible/ansible.cfg ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json") + " loadtests/home-100k/ansible/start-runner.yml",
		"--extra-vars @" + filepath.Join(outDir, "ansible", "extra-vars.json"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("run-stages live commands missing %q:\n%s", want, joined)
		}
	}
	if !coordinatorCalled {
		t.Fatalf("host coordinator was not called")
	}
	if _, err := os.Stat(filepath.Join(outDir, "start-coordination.json")); err != nil {
		t.Fatalf("missing start coordination artifact: %v", err)
	}
	if !strings.Contains(stdout.String(), `"dispatched"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing dispatched VMs:\n%s", stdout.String())
	}
}

func TestParseEMQXEvidenceRequiresBrokerIdentity(t *testing.T) {
	out := `tcp:default acceptors 128
tcp:default current_conn 2
tcp:default max_conns 1048576
ssl:default acceptors 128
ssl:default current_conn 10000
ssl:default max_conns 1048576
ssl:default ssl_closed 14482
`
	counters := parseEvidenceCounters("emqx_listener_stats", "run-fixed", out)
	if counters["emqx.broker.identity"] != 1 {
		t.Fatalf("missing EMQX broker identity counter: %#v", counters)
	}
	if counters["emqx.ssl_default.current_conn"] != 10000 {
		t.Fatalf("ssl current_conn = %d, want 10000", counters["emqx.ssl_default.current_conn"])
	}
	if counters["emqx.ssl_default.shutdown_ssl_closed"] != 14482 {
		t.Fatalf("ssl shutdown counter = %d, want 14482", counters["emqx.ssl_default.shutdown_ssl_closed"])
	}
}

func TestApplyEMQXMetricDeltaFromBaseline(t *testing.T) {
	evidence := ServerEvidence{Sources: map[string]EvidenceSource{
		"emqx": {Available: true, Counters: map[string]int64{
			"emqx.metric.client.connected":         110,
			"emqx.metric.packets.connect.received": 111,
		}},
	}}
	baseline := ServerEvidence{Sources: map[string]EvidenceSource{
		"emqx": {Available: true, Counters: map[string]int64{
			"emqx.metric.client.connected":         100,
			"emqx.metric.packets.connect.received": 101,
		}},
	}}

	applyEMQXMetricDelta(&evidence, baseline)

	counters := evidence.Sources["emqx"].Counters
	if counters["mqtt.total_connect_success"] != 10 {
		t.Fatalf("total_connect_success delta = %d, want 10", counters["mqtt.total_connect_success"])
	}
	if counters["device_mqtt.connect_success"] != 10 {
		t.Fatalf("connect_success delta = %d, want 10", counters["device_mqtt.connect_success"])
	}
	if counters["mqtt.total_connect_attempts"] != 10 {
		t.Fatalf("total_connect_attempts delta = %d, want 10", counters["mqtt.total_connect_attempts"])
	}
	if counters["device_mqtt.connect_attempts"] != 10 {
		t.Fatalf("connect_attempts delta = %d, want 10", counters["device_mqtt.connect_attempts"])
	}
}

func TestApplySourceCounterBaselineDelta(t *testing.T) {
	evidence := ServerEvidence{Sources: map[string]EvidenceSource{
		"ingress_nginx": {Available: true, Counters: map[string]int64{
			"ingress_nginx.request_token.status_200": 120,
			"ingress_nginx.request_token.status_500": 14,
			"ingress_nginx.request_token.max_ms":     600,
		}},
		"postgres": {Available: true, Counters: map[string]int64{
			"postgres.too_many_clients": 34,
		}},
	}}
	baseline := ServerEvidence{Sources: map[string]EvidenceSource{
		"ingress_nginx": {Available: true, Counters: map[string]int64{
			"ingress_nginx.request_token.status_200": 100,
			"ingress_nginx.request_token.status_500": 14,
			"ingress_nginx.request_token.max_ms":     900,
		}},
		"postgres": {Available: true, Counters: map[string]int64{
			"postgres.too_many_clients": 34,
		}},
	}}

	applySourceCounterBaselineDelta(&evidence, baseline, "ingress_nginx")
	applySourceCounterBaselineDelta(&evidence, baseline, "postgres")

	ingress := evidence.Sources["ingress_nginx"].Counters
	if ingress["ingress_nginx.request_token.status_200"] != 20 {
		t.Fatalf("status_200 delta = %d, want 20", ingress["ingress_nginx.request_token.status_200"])
	}
	if ingress["ingress_nginx.request_token.status_500"] != 0 {
		t.Fatalf("status_500 delta = %d, want 0", ingress["ingress_nginx.request_token.status_500"])
	}
	if ingress["ingress_nginx.request_token.max_ms"] != 0 {
		t.Fatalf("max_ms negative delta = %d, want 0", ingress["ingress_nginx.request_token.max_ms"])
	}
	if got := evidence.Sources["postgres"].Counters["postgres.too_many_clients"]; got != 0 {
		t.Fatalf("postgres too_many_clients delta = %d, want 0", got)
	}
}

func TestNormalizeEvidenceSourceCatalogMetadataPreservesOptionalSources(t *testing.T) {
	sources := requiredEvidenceSources(true)
	sources["redis_valkey"] = EvidenceSource{Available: false, Detail: "exit status 1"}

	normalizeEvidenceSourceCatalogMetadata(sources)

	if !sources["redis_valkey"].Optional {
		t.Fatalf("redis_valkey optional = false, want true")
	}
	if !allEvidenceSourcesAvailable(sources) {
		t.Fatalf("optional redis_valkey should not make required evidence incomplete")
	}
}

func TestRecomputeVideoCloudAPITopLevelCountersFromPodDeltas(t *testing.T) {
	evidence := ServerEvidence{Sources: map[string]EvidenceSource{
		"video_cloud_api": {Available: true, Counters: map[string]int64{
			"video_cloud_api.request_token.total":                            0,
			"video_cloud_api.request_token.status_200":                       0,
			"video_cloud_api.request_token.status_500":                       0,
			"video_cloud_api.request_token.pod_video_cloud_api_a.total":      300,
			"video_cloud_api.request_token.pod_video_cloud_api_a.status_200": 299,
			"video_cloud_api.request_token.pod_video_cloud_api_a.status_500": 1,
			"video_cloud_api.request_token.pod_video_cloud_api_b.total":      269,
			"video_cloud_api.request_token.pod_video_cloud_api_b.status_200": 269,
			"video_cloud_api.request_token.pod_video_cloud_api_b.status_500": 0,
		}},
	}}

	recomputeVideoCloudAPITopLevelCounters(&evidence)

	counters := evidence.Sources["video_cloud_api"].Counters
	if counters["video_cloud_api.request_token.total"] != 569 {
		t.Fatalf("top-level total = %d, want 569", counters["video_cloud_api.request_token.total"])
	}
	if counters["video_cloud_api.request_token.status_200"] != 568 {
		t.Fatalf("top-level status_200 = %d, want 568", counters["video_cloud_api.request_token.status_200"])
	}
	if counters["video_cloud_api.request_token.status_500"] != 1 {
		t.Fatalf("top-level status_500 = %d, want 1", counters["video_cloud_api.request_token.status_500"])
	}
}

func TestParseEMQXBrokerMetricsCounters(t *testing.T) {
	out := strings.Join([]string{
		"emqx.metric.client.connected 4525",
		"emqx.metric.packets.connect.received 4525",
		"emqx.pod_mqtt_abc.metric.client.connected 1001",
	}, "\n")
	counters := parseEvidenceCounters("emqx", "run-fixed", out)
	if counters["emqx.metric.client.connected"] != 4525 {
		t.Fatalf("client.connected = %d, want 4525", counters["emqx.metric.client.connected"])
	}
	if counters["emqx.metric.packets.connect.received"] != 4525 {
		t.Fatalf("packets.connect.received = %d, want 4525", counters["emqx.metric.packets.connect.received"])
	}
	if counters["emqx.pod_mqtt_abc.metric.client.connected"] != 1001 {
		t.Fatalf("pod client.connected = %d, want 1001", counters["emqx.pod_mqtt_abc.metric.client.connected"])
	}
}

func TestParseIngressRequestTokenAccessLogCounters(t *testing.T) {
	out := strings.Join([]string{
		`198.51.100.10 - - [16/Jun/2026:19:44:34 +0000] "POST /request_token HTTP/1.1" 200 517 "-" "Go-http-client/1.1" 255 0.186 [video-cloud-staging-video-cloud-video-cloud-api-8080] [] 10.128.218.139:80 517 0.186 200 abc`,
		`198.51.100.11 - - [16/Jun/2026:19:44:35 +0000] "POST /request_token HTTP/1.1" 500 51 "-" "Go-http-client/1.1" 255 7.255 [video-cloud-staging-video-cloud-video-cloud-api-8080] [] 10.128.218.139:80 51 7.255 500 def`,
		`198.51.100.12 - - [16/Jun/2026:19:44:36 +0000] "GET /healthz HTTP/1.1" 200 2 "-" "kube-probe" 80 0.001 [upstream] [] 10.128.218.139:80 2 0.001 200 ghi`,
	}, "\n")

	counters := parseEvidenceCounters("ingress_nginx", "run-fixed", out)

	for key, want := range map[string]int64{
		"ingress_nginx.request_token.total":        2,
		"ingress_nginx.request_token.status_200":   1,
		"ingress_nginx.request_token.status_500":   1,
		"ingress_nginx.request_token.gt1s":         1,
		"ingress_nginx.request_token.gt5s":         1,
		"ingress_nginx.request_token.max_ms":       7255,
		"ingress_nginx.request_token.upstream_500": 1,
	} {
		if counters[key] != want {
			t.Fatalf("%s = %d, want %d; counters=%#v", key, counters[key], want, counters)
		}
	}
}

func TestParseVideoCloudAPIMQTTPressureCounters(t *testing.T) {
	out := strings.Join([]string{
		`{"level":"warn","msg":"mqtt inbound handler queue pressure","subscriber_role":"message-sub","topic_class":"device_message","queue_len":4096,"queue_cap":4096,"enqueue_wait":0.25}`,
		`{"level":"warn","msg":"mqtt inbound handler slow","subscriber_role":"message-sub","topic_class":"device_message","handler_duration":6.633,"total_duration":53.048}`,
		`{"level":"warn","msg":"mqtt shadow request slow","device_id":"load-device-0001","action":"update","duration":3.222}`,
		`{"level":"warn","msg":"mqtt shadow request slow","device_id":"load-device-0002","action":"update","duration":"1.5s"}`,
	}, "\n")

	counters := parseEvidenceCounters("video_cloud_api", "run-fixed", out)

	for key, want := range map[string]int64{
		"video_cloud_api.mqtt.message_sub.queue_pressure":                1,
		"video_cloud_api.mqtt.message_sub.queue_full":                    1,
		"video_cloud_api.mqtt.message_sub.max_queue_len":                 4096,
		"video_cloud_api.mqtt.message_sub.max_queue_cap":                 4096,
		"video_cloud_api.mqtt.message_sub.device_message.queue_pressure": 1,
		"video_cloud_api.mqtt.message_sub.handler_slow":                  1,
		"video_cloud_api.mqtt.message_sub.max_handler_ms":                6633,
		"video_cloud_api.mqtt.message_sub.max_total_ms":                  53048,
		"video_cloud_api.mqtt.shadow_request_slow":                       2,
		"video_cloud_api.mqtt.shadow_request.max_ms":                     3222,
	} {
		if counters[key] != want {
			t.Fatalf("%s = %d, want %d; counters=%#v", key, counters[key], want, counters)
		}
	}
}

func TestParsePostgresTooManyClientsCounters(t *testing.T) {
	out := strings.Join([]string{
		`2026-06-16 20:35:30.861 UTC [2642219] FATAL:  sorry, too many clients already`,
		`2026-06-16 20:35:30.862 UTC [2642235] FATAL:  sorry, too many clients already`,
		`2026-06-16 20:35:39.197 UTC [27] LOG:  checkpoint starting: time`,
	}, "\n")

	counters := parseEvidenceCounters("postgres", "run-fixed", out)

	if counters["postgres.too_many_clients"] != 2 {
		t.Fatalf("postgres.too_many_clients = %d, want 2; counters=%#v", counters["postgres.too_many_clients"], counters)
	}
	if counters["postgres.fatal"] != 2 {
		t.Fatalf("postgres.fatal = %d, want 2; counters=%#v", counters["postgres.fatal"], counters)
	}
}

func TestExecuteShardRunWritesShardArtifacts(t *testing.T) {
	outDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"shard-run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--role", "device-mqtt",
		"--shard-index", "0",
		"--out-dir", outDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(shard-run) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "results.json")); err != nil {
		t.Fatalf("missing shard results: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "TEST_REPORT.md")); err != nil {
		t.Fatalf("missing shard report: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"load_generator_health"`) {
		t.Fatalf("shard results missing load_generator_health:\n%s", string(raw))
	}
	if !strings.Contains(string(raw), `"vm_assignment"`) || !strings.Contains(string(raw), `"start": 0`) || !strings.Contains(string(raw), `"end": 20000`) {
		t.Fatalf("shard results missing shard range:\n%s", string(raw))
	}
	if !strings.Contains(stdout.String(), `"role": "device-mqtt"`) || !strings.Contains(stdout.String(), `"shard_index": 0`) {
		t.Fatalf("stdout missing shard metadata:\n%s", stdout.String())
	}
}

func TestExecuteShardRunLiveInvokesRTKCloudMQTTTest(t *testing.T) {
	outDir := t.TempDir()
	oldRunner := commandRunner
	oldTimeoutRunner := commandRunnerWithTimeout
	calls := []string{}
	commandRunner = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name != "rtk-cloud" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		childOutDir := ""
		for idx := 0; idx < len(args)-1; idx++ {
			if args[idx] == "--out-dir" {
				childOutDir = args[idx+1]
			}
		}
		if childOutDir == "" {
			t.Fatalf("missing child --out-dir in args: %v", args)
		}
		payload := map[string]any{
			"overall": "pass",
			"stage_results": []map[string]any{
				{
					"name":                      "target",
					"connected_devices":         20000,
					"active_connections":        20000,
					"active_subscriptions":      20000,
					"status":                    "PASS",
					"commands_attempted":        2500,
					"commands_passed":           2500,
					"publish_successes":         2100,
					"publish_failures":          3,
					"messages_received":         2050,
					"reported_events":           2000,
					"total_bytes_sent":          123456,
					"total_bytes_received":      654321,
					"http_requests":             700,
					"http_successes":            690,
					"http_failures":             10,
					"total_http_bytes_sent":     1111,
					"total_http_bytes_received": 2222,
					"device_mqtt_totals": map[string]any{
						"active_connections":   20000,
						"active_subscriptions": 20000,
					},
				},
			},
		}
		if err := writeJSONFile(filepath.Join(childOutDir, "results.json"), payload); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	commandRunnerWithTimeout = func(_ time.Duration, name string, args ...string) error {
		return commandRunner(name, args...)
	}
	defer func() {
		commandRunner = oldRunner
		commandRunnerWithTimeout = oldTimeoutRunner
	}()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"shard-run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--role", "device-mqtt",
		"--shard-index", "0",
		"--out-dir", outDir,
		"--runner-mode", "live",
		"--rtk-cloud-binary", "rtk-cloud",
		"--workspace", "/root/rtk_cloud_workspace",
		"--stage-warm-up", "1s",
		"--stage-steady", "1s",
		"--stage-cool-down", "1s",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(shard-run live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"rtk-cloud mqtt-test",
		"--load-model home-100k-sustained",
		"--workspace /root/rtk_cloud_workspace",
		"--env-root cloud_env/staging/lke",
		"--brandname RTK",
		"--duration-seconds 3",
		"--telemetry-interval off",
		"--command-rate-per-device-per-day 1800.00",
		"--stage-names target",
		"--stage-connected-devices 20000",
		"--stage-durations-seconds 3",
		"--device-traffic-profile home-diverse-v1",
		"--concurrency 1000",
		"--command-concurrency 100",
		"--shadow-command-timeout 30s",
		"--max-connected-devices 20000",
		"--shard-index 0",
		"--shard-count 5",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live shard command missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--stage-usage-windows") {
		t.Fatalf("live shard command still passes usage windows:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(outDir, "results.json")); err != nil {
		t.Fatalf("missing converted shard results: %v", err)
	}
	var result struct {
		StageResults []StageResult `json:"stage_results"`
	}
	if err := readJSON(filepath.Join(outDir, "results.json"), &result); err != nil {
		t.Fatalf("read converted shard results: %v", err)
	}
	if len(result.StageResults) == 0 {
		t.Fatalf("missing stage results")
	}
	first := result.StageResults[0]
	if first.DeviceMQTTTotals.Publishes != 2103 || first.DeviceMQTTTotals.ReceivedMessages != 2050 || first.DeviceMQTTTotals.BytesSent != 123456 {
		t.Fatalf("device MQTT totals not preserved from live results: %#v", first.DeviceMQTTTotals)
	}
	if first.ConnectedDevices != 100000 || first.ShardConnectedDevices != 20000 {
		t.Fatalf("stage targets not preserved: global=%d shard=%d", first.ConnectedDevices, first.ShardConnectedDevices)
	}
	if first.AppUserTotals.DesiredWrites != 700 || first.AppUserTotals.ReceivedAcks != 690 || first.AppUserTotals.BytesReceived != 2222 {
		t.Fatalf("app/user totals not preserved from live results: %#v", first.AppUserTotals)
	}
}

func TestExecuteShardRunLiveWritesShardResultsWhenMQTTTestFails(t *testing.T) {
	outDir := t.TempDir()
	oldRunner := commandRunner
	oldTimeoutRunner := commandRunnerWithTimeout
	commandRunner = func(name string, args ...string) error {
		childOutDir := ""
		for idx := 0; idx < len(args)-1; idx++ {
			if args[idx] == "--out-dir" {
				childOutDir = args[idx+1]
			}
		}
		if childOutDir == "" {
			t.Fatalf("missing child --out-dir in args: %v", args)
		}
		payload := map[string]any{
			"overall": "fail",
			"metrics": map[string]any{
				"devices_selected":   2500,
				"commands_attempted": 5000,
				"commands_passed":    100,
			},
			"connect_attempts":          2500,
			"connect_successes":         2400,
			"connect_failures":          100,
			"subscribe_successes":       2400,
			"publish_successes":         200,
			"publish_failures":          10,
			"messages_received":         100,
			"reported_events":           90,
			"total_bytes_sent":          1234,
			"total_bytes_received":      5678,
			"http_requests":             5000,
			"http_successes":            100,
			"http_failures":             4900,
			"total_http_bytes_sent":     111,
			"total_http_bytes_received": 222,
		}
		if err := writeJSONFile(filepath.Join(childOutDir, "results.json"), payload); err != nil {
			t.Fatal(err)
		}
		return errors.New("mqtt test failed")
	}
	commandRunnerWithTimeout = func(_ time.Duration, name string, args ...string) error {
		return commandRunner(name, args...)
	}
	defer func() {
		commandRunner = oldRunner
		commandRunnerWithTimeout = oldTimeoutRunner
	}()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"shard-run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--role", "device-mqtt",
		"--shard-index", "0",
		"--out-dir", outDir,
		"--runner-mode", "live",
		"--rtk-cloud-binary", "rtk-cloud",
		"--stage-warm-up", "1s",
		"--stage-steady", "1s",
		"--stage-cool-down", "1s",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(shard-run live) code = 0, want failure")
	}
	var result struct {
		StageResults []StageResult `json:"stage_results"`
	}
	if err := readJSON(filepath.Join(outDir, "results.json"), &result); err != nil {
		t.Fatalf("missing converted failed shard results: %v stderr=%s", err, stderr.String())
	}
	if len(result.StageResults) != 1 {
		t.Fatalf("stage results len = %d, want 1", len(result.StageResults))
	}
	if result.StageResults[0].DeviceMQTTTotals.ConnectAttempts != 2500 || result.StageResults[0].AppUserTotals.DesiredWrites != 5000 {
		t.Fatalf("failed shard counters not preserved: %#v", result.StageResults[0])
	}
}

func TestExecuteShardRunLiveWritesFallbackStageResultsWhenMQTTTestProducesNoResults(t *testing.T) {
	outDir := t.TempDir()
	oldTimeoutRunner := commandRunnerWithTimeout
	commandRunnerWithTimeout = func(_ time.Duration, name string, args ...string) error {
		return errors.New("mqtt test crashed before writing results")
	}
	defer func() {
		commandRunnerWithTimeout = oldTimeoutRunner
	}()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"shard-run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--role", "device-mqtt",
		"--shard-index", "0",
		"--out-dir", outDir,
		"--runner-mode", "live",
		"--rtk-cloud-binary", "rtk-cloud",
		"--stage-warm-up", "1s",
		"--stage-steady", "1s",
		"--stage-cool-down", "1s",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(shard-run live no results) code = 0, want failure")
	}
	var result struct {
		Status       string        `json:"status"`
		Error        string        `json:"error"`
		StageResults []StageResult `json:"stage_results"`
	}
	if err := readJSON(filepath.Join(outDir, "results.json"), &result); err != nil {
		t.Fatalf("missing fallback shard results: %v stderr=%s", err, stderr.String())
	}
	if result.Status != "failed" || !strings.Contains(result.Error, "mqtt test crashed before writing results") {
		t.Fatalf("fallback status/error not preserved: %#v", result)
	}
	if len(result.StageResults) != 1 {
		t.Fatalf("fallback stage results len = %d, want 1", len(result.StageResults))
	}
	if got := result.StageResults[0].FailureReasons["runner_failed"]; got != 1 {
		t.Fatalf("fallback failure reason = %d, want 1; first stage=%#v", got, result.StageResults[0])
	}
	if result.StageResults[0].ConnectedDevices != 100000 {
		t.Fatalf("fallback connected devices = %d, want 100000", result.StageResults[0].ConnectedDevices)
	}
}

func TestExecuteShardRunLivePreservesPartialStagedResultsWhenMQTTTestFails(t *testing.T) {
	outDir := t.TempDir()
	oldRunner := commandRunner
	oldTimeoutRunner := commandRunnerWithTimeout
	commandRunner = func(name string, args ...string) error {
		childOutDir := ""
		for idx := 0; idx < len(args)-1; idx++ {
			if args[idx] == "--out-dir" {
				childOutDir = args[idx+1]
			}
		}
		if childOutDir == "" {
			t.Fatalf("missing child --out-dir in args: %v", args)
		}
		payload := map[string]any{
			"status": "FAIL",
			"stage_results": []map[string]any{
				{
					"name":                "25k",
					"status":              "PASS",
					"connect_attempts":    2500,
					"connect_successes":   2000,
					"subscribe_successes": 2000,
					"publish_successes":   1000,
					"messages_received":   900,
					"http_requests":       250,
					"http_successes":      240,
					"failure_reasons":     map[string]any{"app_token_request_failed": 10},
					"device_mqtt_totals":  map[string]any{"bytes_sent": 12345},
					"app_user_totals":     map[string]any{"bytes_received": 67890},
				},
				{
					"name":              "50k",
					"status":            "FAIL",
					"connect_attempts":  5000,
					"connect_successes": 3000,
					"http_requests":     500,
					"http_successes":    300,
				},
				{
					"name":             "75k",
					"status":           "FAIL",
					"failure_reasons":  map[string]any{"insufficient_shard_devices": 1},
					"connect_attempts": 0,
				},
			},
		}
		if err := writeJSONFile(filepath.Join(childOutDir, "results.json"), payload); err != nil {
			t.Fatal(err)
		}
		return errors.New("mqtt test failed")
	}
	commandRunnerWithTimeout = func(_ time.Duration, name string, args ...string) error {
		return commandRunner(name, args...)
	}
	defer func() {
		commandRunner = oldRunner
		commandRunnerWithTimeout = oldTimeoutRunner
	}()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"shard-run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--role", "device-mqtt",
		"--shard-index", "0",
		"--out-dir", outDir,
		"--runner-mode", "live",
		"--rtk-cloud-binary", "rtk-cloud",
		"--stage-warm-up", "1s",
		"--stage-steady", "1s",
		"--stage-cool-down", "1s",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(shard-run live) code = 0, want failure")
	}
	var result struct {
		Status       string        `json:"status"`
		Error        string        `json:"error"`
		Partial      bool          `json:"partial"`
		StageResults []StageResult `json:"stage_results"`
	}
	if err := readJSON(filepath.Join(outDir, "results.json"), &result); err != nil {
		t.Fatalf("missing converted partial shard results: %v stderr=%s", err, stderr.String())
	}
	if result.Status != "failed" || !result.Partial || !strings.Contains(result.Error, "stage_results len = 3, want 1") {
		t.Fatalf("partial failure metadata not preserved: %#v stderr=%s", result, stderr.String())
	}
	if len(result.StageResults) != 1 {
		t.Fatalf("stage results len = %d, want 1", len(result.StageResults))
	}
	if result.StageResults[0].DeviceMQTTTotals.ConnectAttempts != 2500 || result.StageResults[0].DeviceMQTTTotals.BytesSent != 12345 {
		t.Fatalf("partial device counters not preserved: %#v", result.StageResults[0].DeviceMQTTTotals)
	}
	if result.StageResults[0].AppUserTotals.DesiredWrites != 250 || result.StageResults[0].AppUserTotals.BytesReceived != 67890 {
		t.Fatalf("partial app counters not preserved: %#v", result.StageResults[0].AppUserTotals)
	}
}

func TestLoadLiveMQTTStageResultDoesNotClassifyTimeoutsAsRejectedUpdates(t *testing.T) {
	outDir := t.TempDir()
	payload := map[string]any{
		"overall": "fail",
		"metrics": map[string]any{
			"commands_attempted": 100,
			"commands_passed":    10,
		},
		"device_mqtt_totals": map[string]any{
			"connect_attempts":  100,
			"connect_success":   100,
			"subscribes":        100,
			"publishes":         100,
			"received_messages": 10,
		},
		"app_user_totals": map[string]any{
			"desired_writes": 100,
			"received_acks":  10,
		},
		"http_failures": 90,
	}
	if err := writeJSONFile(filepath.Join(outDir, "results.json"), payload); err != nil {
		t.Fatal(err)
	}
	result, err := loadLiveMQTTStageResult(filepath.Join(outDir, "results.json"), Stage{Name: "25k", ConnectedDevices: 25000}, 2500)
	if err != nil {
		t.Fatal(err)
	}
	if result.RejectedUpdateCount != 0 {
		t.Fatalf("RejectedUpdateCount = %d, want 0 for timeout/missing ack failures", result.RejectedUpdateCount)
	}
}

func TestLoadLiveMQTTStageResultPreservesFailureReasons(t *testing.T) {
	outDir := t.TempDir()
	payload := map[string]any{
		"overall": "fail",
		"failure_reasons": map[string]any{
			"app_desired_publish_failed": float64(7),
			"device_delta_wait_failed":   float64(3),
		},
	}
	if err := writeJSONFile(filepath.Join(outDir, "results.json"), payload); err != nil {
		t.Fatal(err)
	}
	result, err := loadLiveMQTTStageResult(filepath.Join(outDir, "results.json"), Stage{Name: "25k", ConnectedDevices: 25000}, 2500)
	if err != nil {
		t.Fatal(err)
	}
	if result.FailureReasons["app_desired_publish_failed"] != 7 || result.FailureReasons["device_delta_wait_failed"] != 3 {
		t.Fatalf("failure reasons not preserved: %#v", result.FailureReasons)
	}
}

func TestLoadLiveMQTTStageResultPreservesFailureEvents(t *testing.T) {
	outDir := t.TempDir()
	payload := map[string]any{
		"overall": "fail",
		"failure_events": []map[string]any{
			{
				"stage":        "target",
				"reason":       "device_delta_wait_failed",
				"detail":       "network EOF",
				"phase":        "device_delta_wait",
				"device_id":    "rtk-0041",
				"command_id":   "cmd-0041",
				"event_index":  float64(61),
				"session_slot": float64(61),
				"remaining_ms": float64(12000),
				"mqtt_target":  "172.238.59.219:8883",
				"reader_error": "network EOF",
				"occurred_at":  "2026-06-17T02:59:00Z",
			},
		},
	}
	if err := writeJSONFile(filepath.Join(outDir, "results.json"), payload); err != nil {
		t.Fatal(err)
	}
	result, err := loadLiveMQTTStageResult(filepath.Join(outDir, "results.json"), Stage{Name: "target", ConnectedDevices: 6750}, 6750)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.FailureEvents) != 1 {
		t.Fatalf("failure events not preserved: %#v", result.FailureEvents)
	}
	event := result.FailureEvents[0]
	if event.DeviceID != "rtk-0041" || event.EventIndex != 61 || event.ReaderError != "network EOF" {
		t.Fatalf("unexpected failure event: %#v", event)
	}
}

func TestLoadLiveMQTTStageResultPreservesCommandEvents(t *testing.T) {
	outDir := t.TempDir()
	payload := map[string]any{
		"overall": "pass",
		"command_events": []map[string]any{
			{
				"stage":                 "target",
				"device_id":             "rtk-0041",
				"command_id":            "cmd-0041",
				"runtime_log_stream_id": "mqtt-e2e-run-rtk-0041-abcd",
				"event_index":           float64(7),
				"session_slot":          float64(3),
				"mqtt_target":           "127.0.0.1:8883",
				"expected_logs": []map[string]any{
					{"seq": float64(1), "source": "app_controller", "message": "mqtt_e2e shadow_desired app_controller publish"},
					{"seq": float64(2), "source": "device_client", "message": "mqtt_e2e shadow_delta device_client receive"},
					{"seq": float64(3), "source": "device_client", "message": "mqtt_e2e shadow_reported device_client publish"},
					{"seq": float64(4), "source": "app_observer", "message": "mqtt_e2e shadow_reported app_observer receive"},
				},
				"occurred_at": "2026-06-17T04:00:00Z",
			},
		},
	}
	if err := writeJSONFile(filepath.Join(outDir, "results.json"), payload); err != nil {
		t.Fatal(err)
	}
	result, err := loadLiveMQTTStageResult(filepath.Join(outDir, "results.json"), Stage{Name: "target", ConnectedDevices: 2250}, 2250)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CommandEvents) != 1 {
		t.Fatalf("command events not preserved: %#v", result.CommandEvents)
	}
	event := result.CommandEvents[0]
	if event.RuntimeLogStreamID != "mqtt-e2e-run-rtk-0041-abcd" || len(event.ExpectedLogs) != 4 {
		t.Fatalf("unexpected command event: %#v", event)
	}
}

func TestExecuteShardRunRejectsUnknownShard(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"shard-run",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--role", "device-mqtt",
		"--shard-index", "99",
		"--out-dir", t.TempDir(),
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(shard-run unknown shard) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "shard not found") {
		t.Fatalf("stderr missing shard not found: %s", stderr.String())
	}
}

func TestExecuteCollectDefaultsToDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", "loadtests/home-100k/reports/run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(collect) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"remote_results_glob"`) || !strings.Contains(out, `TEST_REPORT.md`) {
		t.Fatalf("collect output missing artifact layout:\n%s", out)
	}
}

func TestExecuteCollectLiveCopiesShardArtifacts(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "lg02", PublicIPv4: "203.0.113.102"},
		},
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	var callsMu sync.Mutex
	calls := []string{}
	oldRunner := commandRunner
	commandRunner = func(name string, args ...string) error {
		callsMu.Lock()
		defer callsMu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	defer func() { commandRunner = oldRunner }()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--vm-state-file", stateFile,
		"--remote-out-root", "/var/lib/home-100k",
		"--ssh-user", "root",
		"--ssh-key", "/tmp/test-key",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(collect live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"env ANSIBLE_CONFIG=loadtests/home-100k/ansible/ansible.cfg ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json") + " loadtests/home-100k/ansible/collect.yml",
		"--extra-vars @" + filepath.Join(outDir, "ansible", "extra-vars.json"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("collect live commands missing %q:\n%s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "shards", "lg01")); err != nil {
		t.Fatalf("missing shard output dir: %v", err)
	}
	if !strings.Contains(stdout.String(), `"collected"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing collected VMs:\n%s", stdout.String())
	}
}

func TestExecuteCollectServerEvidenceDefaultsToIncompleteSourcePlan(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect-server-evidence",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(collect-server-evidence) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		`"complete": false`,
		`"emqx"`,
		`"iot_device_shadow"`,
		`"redis_valkey"`,
		`"ingress_nginx"`,
		`"host_pod_resources"`,
		`"postgres"`,
		`"run_id": "run-cli"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("server evidence output missing %q:\n%s", want, out)
		}
	}
}

func TestCollectCentralLoggerEvidenceQueriesRunIDIndexes(t *testing.T) {
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer logger-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		seen = append(seen, r.URL.RawQuery)
		events := []map[string]string{{"event_id": "evt-1"}}
		if r.URL.Query().Get("operation_id") == "home-mqtt-loadtest" {
			events = append(events, map[string]string{"event_id": "evt-2"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
	}))
	defer server.Close()

	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "services", "cloud-logger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "services", "cloud-logger", "logger.env"), []byte("CLOUD_LOGGER_ENDPOINT="+server.URL+"\nCLOUD_LOGGER_INGEST_TOKEN=logger-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	source, note := collectCentralLoggerEvidence(envRoot, "run-logger")
	if note != "" {
		t.Fatalf("unexpected note: %s", note)
	}
	if !source.Available || !source.Optional {
		t.Fatalf("central logger source flags = %+v, want available optional", source)
	}
	if source.Counters["central_logger.trace_id.events"] != 1 ||
		source.Counters["central_logger.request_id.events"] != 1 ||
		source.Counters["central_logger.operation_id.events"] != 1 ||
		source.Counters["central_logger.home_mqtt_operation.events"] != 2 {
		t.Fatalf("unexpected counters: %+v", source.Counters)
	}
	joined := strings.Join(seen, "\n")
	for _, want := range []string{"trace_id=run-logger", "request_id=run-logger", "operation_id=run-logger", "operation_id=home-mqtt-loadtest"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("central logger query missing %q in:\n%s", want, joined)
		}
	}
}

func TestCollectLiveServerEvidenceFallsBackToCentralLoggerRuntimeLogs(t *testing.T) {
	outDir := t.TempDir()
	if err := writeJSONFile(filepath.Join(outDir, "start-coordination.json"), StartCoordination{
		VMs: []VMStartTelemetry{{
			Label:                 "lg01",
			StartSignalReceivedAt: "2026-06-16T21:06:10Z",
			StageStartedAt:        "2026-06-16T21:06:13Z",
		}},
	}); err != nil {
		t.Fatalf("write start coordination: %v", err)
	}

	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer logger-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		requests = append(requests, r.URL.RawQuery)
		query := r.URL.Query()
		events := []map[string]any{{"event_id": "evt-generic"}}
		if query.Get("component") == "device_runtime_log" && query.Get("source") == "device-runtime" {
			events = []map[string]any{
				{
					"event_id":  "evt-1",
					"ts":        "2026-06-16T21:06:20Z",
					"level":     "info",
					"msg":       "mqtt_e2e shadow_desired app_controller publish",
					"service":   "video-cloud",
					"env":       "staging",
					"version":   "test",
					"host":      "host",
					"unit":      "unit",
					"source":    "device-runtime",
					"component": "device_runtime_log",
					"fields": map[string]any{
						"stream_id": "mqtt-e2e-run-cli-device-000001",
						"seq":       1,
						"source":    "app_controller",
					},
				},
				{
					"event_id":  "evt-2",
					"ts":        "2026-06-16T21:06:21Z",
					"level":     "info",
					"msg":       "mqtt_e2e shadow_delta device_client receive",
					"service":   "video-cloud",
					"env":       "staging",
					"version":   "test",
					"host":      "host",
					"unit":      "unit",
					"source":    "device-runtime",
					"component": "device_runtime_log",
					"fields": map[string]any{
						"stream_id": "mqtt-e2e-run-cli-device-000001",
						"seq":       2,
						"source":    "device_client",
					},
				},
				{
					"event_id":  "evt-3",
					"ts":        "2026-06-16T21:06:22Z",
					"level":     "info",
					"msg":       "mqtt_e2e shadow_reported device_client publish",
					"service":   "video-cloud",
					"env":       "staging",
					"version":   "test",
					"host":      "host",
					"unit":      "unit",
					"source":    "device-runtime",
					"component": "device_runtime_log",
					"fields": map[string]any{
						"stream_id": "mqtt-e2e-run-cli-device-000001",
						"seq":       3,
						"source":    "device_client",
					},
				},
				{
					"event_id":  "evt-4",
					"ts":        "2026-06-16T21:06:23Z",
					"level":     "info",
					"msg":       "mqtt_e2e shadow_reported app_observer receive",
					"service":   "video-cloud",
					"env":       "staging",
					"version":   "test",
					"host":      "host",
					"unit":      "unit",
					"source":    "device-runtime",
					"component": "device_runtime_log",
					"fields": map[string]any{
						"stream_id": "mqtt-e2e-run-cli-device-000001",
						"seq":       4,
						"source":    "app_observer",
					},
				},
				{
					"event_id":  "evt-other",
					"ts":        "2026-06-16T21:06:24Z",
					"level":     "info",
					"msg":       "mqtt_e2e shadow_desired app_controller publish",
					"service":   "video-cloud",
					"env":       "staging",
					"version":   "test",
					"host":      "host",
					"unit":      "unit",
					"source":    "device-runtime",
					"component": "device_runtime_log",
					"fields": map[string]any{
						"stream_id": "mqtt-e2e-other-run-device-000001",
						"seq":       1,
						"source":    "app_controller",
					},
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
	}))
	defer server.Close()

	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "services", "cloud-logger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "services", "cloud-logger", "logger.env"), []byte("CLOUD_LOGGER_ENDPOINT="+server.URL+"\nCLOUD_LOGGER_INGEST_TOKEN=logger-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldRunner := commandOutputRunner
	commandOutputRunner = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "device_runtime_logs"):
			t.Fatalf("collectLiveServerEvidence queried legacy device_runtime_logs table: %s %s", name, joined)
		case strings.Contains(joined, "device_shadows"):
			return "device_shadow.rows_current_converged\t1\n", nil
		default:
			return "", nil
		}
		return "", nil
	}
	defer func() { commandOutputRunner = oldRunner }()

	evidence := collectLiveServerEvidence(envRoot, "run-cli", outDir)
	shadow := evidence.Sources["iot_device_shadow"]
	if !shadow.Available {
		t.Fatalf("iot_device_shadow source unavailable after central logger fallback: %+v notes=%v", shadow, evidence.Notes)
	}
	for _, counter := range []string{"app_user.desired_writes", "device_mqtt.delta_received", "device_mqtt.reported_publishes", "app_user.received_acks"} {
		if shadow.Counters[counter] != 1 {
			t.Fatalf("%s = %d, want 1 in counters %+v", counter, shadow.Counters[counter], shadow.Counters)
		}
	}
	streams := evidence.Sources["iot_device_shadow_streams"]
	if !streams.Available {
		t.Fatalf("iot_device_shadow_streams source unavailable after central logger fallback: %+v notes=%v", streams, evidence.Notes)
	}
	if streams.Counters["runtime_log_streams.total"] != 1 ||
		streams.Counters["runtime_log_stream.mqtt-e2e-run-cli-device-000001.entries"] != 4 ||
		streams.Counters["runtime_log_stream.mqtt-e2e-run-cli-device-000001.seq.4"] != 1 {
		t.Fatalf("unexpected stream counters: %+v", streams.Counters)
	}
	joined := strings.Join(requests, "\n")
	for _, want := range []string{"component=device_runtime_log", "source=device-runtime", "since=2026-06-16T21%3A06%3A05Z", "limit=1000"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("central logger runtime query missing %q in:\n%s", want, joined)
		}
	}
}

func TestCentralLoggerEnvValuesFindsSiblingLinodeEnv(t *testing.T) {
	root := t.TempDir()
	lkeRoot := filepath.Join(root, "lke")
	loggerDir := filepath.Join(root, "linode", "services", "cloud-logger")
	if err := os.MkdirAll(lkeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(loggerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loggerDir, "logger.env"), []byte("CLOUD_LOGGER_ENDPOINT=https://logger.example\nCLOUD_LOGGER_INGEST_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	values := centralLoggerEnvValues(lkeRoot)
	if values["CLOUD_LOGGER_ENDPOINT"] != "https://logger.example" || values["CLOUD_LOGGER_INGEST_TOKEN"] != "secret" {
		t.Fatalf("centralLoggerEnvValues() = %#v, want sibling linode logger env", values)
	}
}

func TestCentralLoggerEnvValuesPrefersLKESecretToken(t *testing.T) {
	root := t.TempDir()
	lkeRoot := filepath.Join(root, "lke")
	loggerDir := filepath.Join(root, "linode", "services", "cloud-logger")
	secretDir := filepath.Join(lkeRoot, "state", "secrets")
	if err := os.MkdirAll(loggerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loggerDir, "logger.env"), []byte("CLOUD_LOGGER_ENDPOINT=https://logger.example\nCLOUD_LOGGER_INGEST_TOKEN=stale-linode-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "cloud-logger-ingest-token"), []byte("current-lke-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	values := centralLoggerEnvValues(lkeRoot)
	if values["CLOUD_LOGGER_ENDPOINT"] != "https://logger.example" || values["CLOUD_LOGGER_INGEST_TOKEN"] != "current-lke-token" {
		t.Fatalf("centralLoggerEnvValues() = %#v, want endpoint from env and token from lke secret", values)
	}
}

func TestExecuteCollectServerEvidenceLiveWritesCompleteEvidence(t *testing.T) {
	outDir := t.TempDir()
	if err := writeJSONFile(filepath.Join(outDir, "start-coordination.json"), StartCoordination{
		VMs: []VMStartTelemetry{{
			Label:                 "lg01",
			StartSignalReceivedAt: "2026-06-16T21:06:10Z",
			StageStartedAt:        "2026-06-16T21:06:13Z",
		}},
	}); err != nil {
		t.Fatalf("write start coordination: %v", err)
	}
	logger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer logger-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		events := []map[string]any{{"event_id": "evt-generic"}}
		query := r.URL.Query()
		if query.Get("component") == "device_runtime_log" && query.Get("source") == "device-runtime" {
			events = []map[string]any{}
			for idx := 0; idx < 10; idx++ {
				streamID := fmt.Sprintf("mqtt-e2e-run-cli-device-%06d", idx)
				for seq, row := range []struct {
					source  string
					message string
				}{
					{"app_controller", "mqtt_e2e shadow_desired app_controller publish"},
					{"device_client", "mqtt_e2e shadow_delta device_client receive"},
					{"device_client", "mqtt_e2e shadow_reported device_client publish"},
					{"app_observer", "mqtt_e2e shadow_reported app_observer receive"},
				} {
					events = append(events, map[string]any{
						"event_id":  fmt.Sprintf("evt-%d-%d", idx, seq+1),
						"ts":        "2026-06-16T21:06:20Z",
						"msg":       row.message,
						"source":    "device-runtime",
						"component": "device_runtime_log",
						"fields": map[string]any{
							"stream_id": streamID,
							"seq":       seq + 1,
							"source":    row.source,
						},
					})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
	}))
	defer logger.Close()
	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "services", "cloud-logger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "services", "cloud-logger", "logger.env"), []byte("CLOUD_LOGGER_ENDPOINT="+logger.URL+"\nCLOUD_LOGGER_INGEST_TOKEN=logger-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	calls := []string{}
	stubCommandOutputRunner(t, func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "device_runtime_logs"):
			t.Fatalf("collect-server-evidence queried legacy device_runtime_logs table: %s %s", name, joined)
		case strings.Contains(joined, "device_shadows"):
			return "device_shadow.reported_converged\t10\n", nil
		case strings.Contains(joined, "app.kubernetes.io/name=mqtt"):
			return "client.connected rtk-e2e-run-cli-home-device-000001-device-1\n", nil
		default:
			return "", nil
		}
		return "", nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect-server-evidence",
		"--env-root", envRoot,
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(collect-server-evidence live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"complete": true`) || !strings.Contains(out, `"host_pod_resources"`) {
		t.Fatalf("stdout missing complete evidence:\n%s", out)
	}
	if !strings.Contains(out, `"evidence_window_mode": "run_scoped_since_time"`) || !strings.Contains(out, `"evidence_window_start": "2026-06-16T21:06:05Z"`) {
		t.Fatalf("stdout missing run-scoped evidence window:\n%s", out)
	}
	if !strings.Contains(out, `"app_user.desired_writes": 10`) || !strings.Contains(out, `"device_shadow.reported_converged": 10`) {
		t.Fatalf("stdout missing parsed counters:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(outDir, "server-evidence.json")); err != nil {
		t.Fatalf("missing server-evidence.json: %v", err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"kubectl get pods -A",
		"kubectl -n 'video-cloud-staging-video-cloud' get pods --selector 'app.kubernetes.io/name=mqtt' -o name",
		"kubectl -n 'video-cloud-staging-video-cloud' logs '--since-time=2026-06-16T21:06:05Z' --selector 'app.kubernetes.io/name=video-cloud-api'",
		"kubectl -n 'video-cloud-staging-platform' logs '--since-time=2026-06-16T21:06:05Z' --selector 'app.kubernetes.io/name=postgresql'",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live evidence commands missing %q:\n%s", want, joined)
		}
	}
}

func TestServerEvidenceProbesIncludeMQTTNodeBalancerHealth(t *testing.T) {
	probes := serverEvidenceProbes("run-nb", "--since=1m")
	for _, probe := range probes {
		if probe.source != "mqtt_nodebalancer" {
			continue
		}
		joined := strings.Join(append([]string{probe.command}, probe.args...), " ")
		for _, want := range []string{"LINODE_TOKEN", "mqtt-public", "nodebalancers", "mqtt_nodebalancer.nodes_up"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("mqtt_nodebalancer probe missing %q in:\n%s", want, joined)
			}
		}
		return
	}
	t.Fatal("serverEvidenceProbes() missing mqtt_nodebalancer probe")
}

func TestKubectlLogEvidenceProbesBoundLogCollectionTime(t *testing.T) {
	probe := kubectlLogsProbe("video_cloud_api", "video-cloud-staging-video-cloud", "app.kubernetes.io/name=video-cloud-api", "--since=30m", "logs")
	joined := strings.Join(append([]string{probe.command}, probe.args...), " ")
	for _, want := range []string{
		"timeout 20s kubectl",
		"logs '--since=30m'",
		"--tail=120000",
		"|| true",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("kubectl log probe missing bounded-log marker %q:\n%s", want, joined)
		}
	}
}

func TestExecuteCollectServerEvidenceLiveWritesIncompleteEvidenceOnProbeFailure(t *testing.T) {
	outDir := t.TempDir()
	stubCommandOutputRunner(t, func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "postgres") {
			return "", errors.New("postgres probe failed")
		}
		return "", nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect-server-evidence",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(collect-server-evidence partial live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	var evidence ServerEvidence
	if err := json.Unmarshal([]byte(out), &evidence); err != nil {
		t.Fatalf("decode server evidence stdout: %v\n%s", err, out)
	}
	if evidence.Complete || !strings.Contains(evidence.Sources["postgres"].Detail, "postgres probe failed") {
		t.Fatalf("stdout missing incomplete postgres evidence:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "server-evidence.json"))
	if err != nil {
		t.Fatalf("read server-evidence.json: %v", err)
	}
	if !strings.Contains(string(raw), `"complete": false`) {
		t.Fatalf("server evidence file did not preserve incomplete status:\n%s", string(raw))
	}
}

func TestExecuteCollectServerEvidenceLivePreservesFailureForRepeatedSource(t *testing.T) {
	stubCommandOutputRunner(t, func(name string, args ...string) (string, error) {
		if strings.Join(args, " ") == "get pods -A -o wide" {
			return "", errors.New("pod inventory failed")
		}
		return "", nil
	})

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect-server-evidence",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--live",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(collect-server-evidence repeated source failure) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"complete": false`) || !strings.Contains(out, `"host_pod_resources"`) || !strings.Contains(out, "pod inventory failed") {
		t.Fatalf("stdout did not preserve repeated-source failure:\n%s", out)
	}
}

func TestMergeEvidenceSourcePreservesExistingDataWhenLaterProbeFails(t *testing.T) {
	current := EvidenceSource{
		Available: true,
		Detail:    "logs captured",
		Counters: map[string]int64{
			"video_cloud_api.mqtt.message_sub.queue_pressure": 10660,
		},
		Samples: []EvidenceResourceSample{{Kind: "k8s_pod_top", Pod: "video-cloud-api-0", CPUCoreMil: 1000}},
	}
	next := EvidenceSource{Available: false, Detail: "kubectl logs timed out"}

	merged := mergeEvidenceSource(current, next)

	if !merged.Available {
		t.Fatalf("merged source should remain available when existing data is preserved: %#v", merged)
	}
	if merged.Counters["video_cloud_api.mqtt.message_sub.queue_pressure"] != 10660 {
		t.Fatalf("merged counters = %#v, want preserved queue pressure", merged.Counters)
	}
	if len(merged.Samples) != 1 {
		t.Fatalf("merged samples = %#v, want preserved samples", merged.Samples)
	}
	if !strings.Contains(merged.Detail, "kubectl logs timed out") {
		t.Fatalf("merged detail missing later failure: %q", merged.Detail)
	}
}

func TestMergeEvidenceSourceUsesLaterDataWhenEarlierProbeFailed(t *testing.T) {
	current := EvidenceSource{Available: false, Detail: "first probe failed"}
	next := EvidenceSource{
		Available: true,
		Detail:    "metrics captured",
		Counters: map[string]int64{
			"mqtt.total_connect_success": 105000,
		},
	}

	merged := mergeEvidenceSource(current, next)

	if !merged.Available {
		t.Fatalf("merged source should become available when later data exists: %#v", merged)
	}
	if merged.Counters["mqtt.total_connect_success"] != 105000 {
		t.Fatalf("merged counters = %#v, want later metrics", merged.Counters)
	}
	if !strings.Contains(merged.Detail, "first probe failed") || !strings.Contains(merged.Detail, "metrics captured") {
		t.Fatalf("merged detail missing probe history: %q", merged.Detail)
	}
}

func TestParseEvidenceSamplesCapturesPostgresTopPods(t *testing.T) {
	out := `NAMESPACE                          NAME                                           CPU(cores)   MEMORY(bytes)
video-cloud-staging-platform       postgresql-0                                   189m         236Mi
video-cloud-staging-video-cloud    video-cloud-api-6bb7d87f5b-qcclw              21m          42Mi
`
	samples := parseEvidenceSamples("host_pod_resources", out)
	if len(samples) != 2 {
		t.Fatalf("samples len = %d, want 2: %+v", len(samples), samples)
	}
	postgres := samples[0]
	if postgres.Kind != "k8s_pod_top" ||
		postgres.Namespace != "video-cloud-staging-platform" ||
		postgres.Pod != "postgresql-0" ||
		postgres.CPUCoreMil != 189 ||
		postgres.MemoryBytes != 236*1024*1024 {
		t.Fatalf("unexpected postgres sample: %+v", postgres)
	}
}

func TestParseEvidenceCountersCapturesRedisInfo(t *testing.T) {
	out := `# Stats
total_commands_processed:120
keyspace_hits:80
keyspace_misses:5
connected_clients:7
used_memory:1048576
db0:keys=42,expires=3,avg_ttl=1000
cmdstat_get:calls=50,usec=100,usec_per_call=2.00
cmdstat_set:calls=12,usec=24,usec_per_call=2.00
`
	counters := parseEvidenceCounters("redis_valkey", "run-cli", out)
	for key, want := range map[string]int64{
		"redis_valkey.total_commands_processed": 120,
		"redis_valkey.keyspace_hits":            80,
		"redis_valkey.keyspace_misses":          5,
		"redis_valkey.connected_clients":        7,
		"redis_valkey.used_memory":              1048576,
		"redis_valkey.keyspace.db0.keys":        42,
		"redis_valkey.keyspace.db0.expires":     3,
		"redis_valkey.command.get.calls":        50,
		"redis_valkey.command.set.calls":        12,
	} {
		if counters[key] != want {
			t.Fatalf("%s = %d, want %d in %+v", key, counters[key], want, counters)
		}
	}
}

func TestExecuteAggregateWritesRunLevelReport(t *testing.T) {
	outDir := t.TempDir()
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatal(err)
	}
	stages, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "shards", "lg01", "results.json"), map[string]any{
		"run_id":        "run-cli",
		"role":          "device-mqtt",
		"shard_index":   0,
		"stage_results": stages,
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), correlatedEvidenceForStages("run-cli", stages)); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"aggregate",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(aggregate) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"status": "INCOMPLETE"`) || !strings.Contains(stdout.String(), `"report_file"`) {
		t.Fatalf("aggregate stdout missing INCOMPLETE/report path:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "TEST_REPORT.md")); err != nil {
		t.Fatalf("missing aggregate report: %v", err)
	}
}
