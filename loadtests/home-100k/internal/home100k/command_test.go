package home100k

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

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
		`"connected_devices": 100000`,
		`"offline_desired_queue": 10000`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q:\n%s", want, out)
		}
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
		`"warm_up": "1m"`,
		`"steady_state": "3m"`,
		`"cool_down": "30s"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q:\n%s", want, out)
		}
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
		"## Stage Results",
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
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		created++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":200,"label":"home-100k-mixed-000","ipv4":["203.0.113.200"]}`))
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
	if created != 10 {
		t.Fatalf("created requests = %d, want 10", created)
	}
	if _, err := os.Stat(filepath.Join(outDir, "vms.json")); err != nil {
		t.Fatalf("missing vms.json: %v", err)
	}
	if !strings.Contains(stdout.String(), `"vm_state_file"`) {
		t.Fatalf("stdout missing vm_state_file:\n%s", stdout.String())
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
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":200,"label":"home-100k-mixed-000","ipv4":["203.0.113.200"]}`))
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
			{ID: 101, Label: "home-100k-mixed-000", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "home-100k-mixed-001", PublicIPv4: "203.0.113.102"},
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
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"home-100k"`) || !strings.Contains(out, `"run-cli"`) {
		t.Fatalf("list-vms dry-run output missing tag filter:\n%s", out)
	}
}

func TestExecuteListVMsLiveReturnsTaggedVMs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if !strings.Contains(r.Header.Get("X-Filter"), "run-cli") {
			t.Fatalf("X-Filter = %q", r.Header.Get("X-Filter"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":101,"label":"home-100k-mixed-000","ipv4":["203.0.113.101"]}],"page":1,"pages":1,"results":1}`))
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
	if !strings.Contains(out, `"dry_run": true`) || !strings.Contains(out, `"action": "sync"`) || !strings.Contains(out, `"home-100k-mixed-001"`) {
		t.Fatalf("sync dry-run output missing shard sync actions:\n%s", out)
	}
}

func TestExecuteSyncLiveGeneratesAnsibleInventoryFromProvisionedVMs(t *testing.T) {
	outDir := t.TempDir()
	stateFile := filepath.Join(outDir, "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "home-100k-mixed-000", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "home-100k-mixed-001", PublicIPv4: "203.0.113.102"},
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
		return nil
	}
	defer func() { commandRunner = oldRunner }()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"sync",
		"--env-root", "cloud_env/staging/lke",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--vm-state-file", stateFile,
		"--remote-workspace", "/root/rtk_cloud_workspace",
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/lke",
		"--ssh-user", "root",
		"--ssh-key", "/tmp/test-key",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(sync live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"bash -lc GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o '" + filepath.Join(outDir, "bin", "home-100k-linux-amd64") + "' ./loadtests/home-100k/cmd/home-100k",
		"ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json") + " loadtests/home-100k/ansible/sync.yml",
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
	var extraVars map[string]string
	if err := json.Unmarshal(extraVarsRaw, &extraVars); err != nil {
		t.Fatalf("decode extra vars: %v", err)
	}
	for _, key := range []string{"local_runner", "local_env_root", "local_out_dir"} {
		if !filepath.IsAbs(extraVars[key]) {
			t.Fatalf("extra vars %s = %q, want absolute path", key, extraVars[key])
		}
	}
	if !filepath.IsAbs(extraVars["local_rtk_cloud"]) {
		t.Fatalf("extra vars local_rtk_cloud = %q, want absolute path", extraVars["local_rtk_cloud"])
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
	localManifest, _ := inventoryDoc.All.Children["home_100k"].Hosts["home-100k-mixed-000"]["local_shard_manifest"].(string)
	if !filepath.IsAbs(localManifest) {
		t.Fatalf("inventory local_shard_manifest = %q, want absolute path", localManifest)
	}
	localFilter, _ := inventoryDoc.All.Children["home_100k"].Hosts["home-100k-mixed-000"]["local_env_rsync_filter"].(string)
	if !filepath.IsAbs(localFilter) {
		t.Fatalf("inventory local_env_rsync_filter = %q, want absolute path", localFilter)
	}
	filterRaw, err := os.ReadFile(localFilter)
	if err != nil {
		t.Fatalf("missing env rsync filter: %v", err)
	}
	for _, want := range []string{"+ /devices/test_device/devices/", "+ /artifacts/users/*.json", "- *"} {
		if !strings.Contains(string(filterRaw), want) {
			t.Fatalf("env rsync filter missing %q:\n%s", want, string(filterRaw))
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "sync-telemetry.json")); err != nil {
		t.Fatalf("missing sync telemetry placeholder: %v", err)
	}
	manifest0Raw, err := os.ReadFile(filepath.Join(outDir, "shard-manifests", "home-100k-mixed-000.json"))
	if err != nil {
		t.Fatalf("missing mixed-000 manifest: %v", err)
	}
	manifest1Raw, err := os.ReadFile(filepath.Join(outDir, "shard-manifests", "home-100k-mixed-001.json"))
	if err != nil {
		t.Fatalf("missing mixed-001 manifest: %v", err)
	}
	manifest0 := string(manifest0Raw)
	manifest1 := string(manifest1Raw)
	for _, want := range []string{`"role": "device-mqtt"`, `"start": 0`, `"end": 10000`, `"role": "user-app"`, `"start": 0`, `"end": 500`} {
		if !strings.Contains(manifest0, want) {
			t.Fatalf("mixed-000 manifest missing %q:\n%s", want, manifest0)
		}
	}
	for _, want := range []string{`"role": "device-mqtt"`, `"start": 10000`, `"end": 20000`, `"role": "user-app"`, `"start": 500`, `"end": 1000`} {
		if !strings.Contains(manifest1, want) {
			t.Fatalf("mixed-001 manifest missing %q:\n%s", want, manifest1)
		}
	}
	if !strings.Contains(stdout.String(), `"synced"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing synced VMs:\n%s", stdout.String())
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
		`"name": "100k"`,
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

func TestExecuteRunStagesLiveDispatchesShardCommands(t *testing.T) {
	outDir := t.TempDir()
	stateFile := filepath.Join(outDir, "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "home-100k-mixed-000", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "home-100k-mixed-001", PublicIPv4: "203.0.113.102"},
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
		return nil
	}
	defer func() { commandRunner = oldRunner }()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"run-stages",
		"--env-root", "cloud_env/staging/lke",
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
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(run-stages live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json") + " loadtests/home-100k/ansible/run-stages.yml",
		"--extra-vars @" + filepath.Join(outDir, "ansible", "extra-vars.json"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("run-stages live commands missing %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(stdout.String(), `"dispatched"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing dispatched VMs:\n%s", stdout.String())
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
	if !strings.Contains(string(raw), `"vm_assignment"`) || !strings.Contains(string(raw), `"start": 0`) || !strings.Contains(string(raw), `"end": 10000`) {
		t.Fatalf("shard results missing shard range:\n%s", string(raw))
	}
	if !strings.Contains(stdout.String(), `"role": "device-mqtt"`) || !strings.Contains(stdout.String(), `"shard_index": 0`) {
		t.Fatalf("stdout missing shard metadata:\n%s", stdout.String())
	}
}

func TestExecuteShardRunLiveInvokesRTKCloudMQTTTest(t *testing.T) {
	outDir := t.TempDir()
	oldRunner := commandRunner
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
			"metrics": map[string]any{
				"devices_selected":   2500,
				"commands_attempted": 2500,
				"commands_passed":    2500,
			},
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
		}
		if err := writeJSONFile(filepath.Join(childOutDir, "results.json"), payload); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	defer func() { commandRunner = oldRunner }()

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
		"--workspace /root/rtk_cloud_workspace",
		"--env-root cloud_env/staging/lke",
		"--brandname RTK",
		"--duration-seconds 3",
		"--max-connected-devices 2500",
		"--shard-index 0",
		"--shard-count 10",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live shard command missing %q:\n%s", want, joined)
		}
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
	if first.AppUserTotals.ReadShadowRequests != 700 || first.AppUserTotals.ReceivedAcks != 690 || first.AppUserTotals.BytesReceived != 2222 {
		t.Fatalf("app/user totals not preserved from live results: %#v", first.AppUserTotals)
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
			{ID: 101, Label: "home-100k-mixed-000", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "home-100k-mixed-001", PublicIPv4: "203.0.113.102"},
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
		"ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json") + " loadtests/home-100k/ansible/collect.yml",
		"--extra-vars @" + filepath.Join(outDir, "ansible", "extra-vars.json"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("collect live commands missing %q:\n%s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "shards", "home-100k-mixed-000")); err != nil {
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

func TestExecuteCollectServerEvidenceLiveWritesCompleteEvidence(t *testing.T) {
	outDir := t.TempDir()
	calls := []string{}
	oldRunner := commandRunner
	commandRunner = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	defer func() { commandRunner = oldRunner }()

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
		t.Fatalf("Execute(collect-server-evidence live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, `"complete": true`) || !strings.Contains(out, `"host_pod_resources"`) {
		t.Fatalf("stdout missing complete evidence:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(outDir, "server-evidence.json")); err != nil {
		t.Fatalf("missing server-evidence.json: %v", err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"kubectl get pods -A",
		"kubectl -n 'video-cloud-staging-video-cloud' get pods --selector 'app.kubernetes.io/name=mqtt' -o name",
		"kubectl -n 'video-cloud-staging-video-cloud' logs --since=30m --selector 'app.kubernetes.io/name=video-cloud-api'",
		"kubectl -n 'video-cloud-staging-platform' logs --since=30m --selector 'app.kubernetes.io/name=postgresql'",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("live evidence commands missing %q:\n%s", want, joined)
		}
	}
}

func TestExecuteCollectServerEvidenceLiveWritesIncompleteEvidenceOnProbeFailure(t *testing.T) {
	outDir := t.TempDir()
	oldRunner := commandRunner
	commandRunner = func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), "postgres") {
			return errors.New("postgres probe failed")
		}
		return nil
	}
	defer func() { commandRunner = oldRunner }()

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
	if !strings.Contains(out, `"complete": false`) || !strings.Contains(out, `"postgres probe failed"`) {
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
	oldRunner := commandRunner
	commandRunner = func(name string, args ...string) error {
		if strings.Join(args, " ") == "get pods -A -o wide" {
			return errors.New("pod inventory failed")
		}
		return nil
	}
	defer func() { commandRunner = oldRunner }()

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
	if err := writeJSONFile(filepath.Join(outDir, "shards", "home-100k-mixed-000", "results.json"), map[string]any{
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
