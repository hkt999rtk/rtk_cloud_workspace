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

func TestExecuteSyncLiveRunsSSHAndRsyncForVMState(t *testing.T) {
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
	knownHosts := filepath.Join(outDir, "ssh_known_hosts")
	sshBase := "-i /tmp/test-key -o UserKnownHostsFile=" + knownHosts
	for _, want := range []string{
		"bash -lc GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o '" + filepath.Join(outDir, "bin", "home-100k-linux-amd64") + "' ./loadtests/home-100k/cmd/home-100k",
		"ssh " + sshBase + " -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new root@203.0.113.101 true",
		"ssh " + sshBase + " root@203.0.113.101 bash -lc command -v rsync >/dev/null 2>&1 || (apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y rsync)",
		"ssh " + sshBase + " root@203.0.113.101 mkdir -p /root/rtk_cloud_workspace /root/rtk_cloud_workspace/cloud_env/staging/lke /root/rtk_cloud_workspace/loadtests/home-100k/shard-manifests /root/rtk_cloud_workspace/loadtests/home-100k/bin",
		"rsync -az -e ssh " + sshBase + " loadtests/home-100k go.work root@203.0.113.101:/root/rtk_cloud_workspace/",
		"rsync -az -e ssh " + sshBase + " " + filepath.Join(outDir, "bin", "home-100k-linux-amd64") + " root@203.0.113.101:/root/rtk_cloud_workspace/loadtests/home-100k/bin/home-100k",
		"rsync -az -e ssh " + sshBase + " " + filepath.Join(outDir, "shard-manifests", "home-100k-mixed-000.json") + " root@203.0.113.101:/root/rtk_cloud_workspace/loadtests/home-100k/shard-manifests/current.json",
		"rsync -az -e ssh " + sshBase + " --include /env/*** --include /services/*** --include /devices/ --include /devices/test_device/ --include /devices/test_device/loadtest.env --include /devices/test_device/summary.json --include /artifacts/ --include /artifacts/users/ --include /artifacts/users/*.json --include /artifacts/device-bind/ --include /artifacts/device-bind/*.json --exclude * cloud_env/staging/lke/ root@203.0.113.102:/root/rtk_cloud_workspace/cloud_env/staging/lke/",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sync live commands missing %q:\n%s", want, joined)
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

func TestExecuteRunStagesLiveDispatchesShardCommands(t *testing.T) {
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
	sshBase := "-i /tmp/test-key -o UserKnownHostsFile=loadtests/home-100k/reports/run-cli/ssh_known_hosts"
	for _, want := range []string{
		"ssh " + sshBase + " root@203.0.113.101 cd /root/rtk_cloud_workspace && /root/rtk_cloud_workspace/loadtests/home-100k/bin/home-100k shard-run --env-root /root/rtk_cloud_workspace/cloud_env/staging/lke --brandname RTK --region us-sea --stage-warm-up 1m --stage-steady 2m --stage-cool-down 45s --run-id run-cli --role mixed --shard-index 0 --shard-manifest /root/rtk_cloud_workspace/loadtests/home-100k/shard-manifests/current.json --honor-stage-durations --out-dir /var/lib/home-100k/run-cli/home-100k-mixed-000",
		"ssh " + sshBase + " root@203.0.113.102 cd /root/rtk_cloud_workspace && /root/rtk_cloud_workspace/loadtests/home-100k/bin/home-100k shard-run --env-root /root/rtk_cloud_workspace/cloud_env/staging/lke --brandname RTK --region us-sea --stage-warm-up 1m --stage-steady 2m --stage-cool-down 45s --run-id run-cli --role mixed --shard-index 1 --shard-manifest /root/rtk_cloud_workspace/loadtests/home-100k/shard-manifests/current.json --honor-stage-durations --out-dir /var/lib/home-100k/run-cli/home-100k-mixed-001",
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
	sshBase := "-i /tmp/test-key -o UserKnownHostsFile=" + filepath.Join(outDir, "ssh_known_hosts")
	for _, want := range []string{
		"scp " + sshBase + " root@203.0.113.101:/var/lib/home-100k/run-cli/home-100k-mixed-000/results.json",
		"scp " + sshBase + " root@203.0.113.102:/var/lib/home-100k/run-cli/home-100k-mixed-001/TEST_REPORT.md",
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
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), ServerEvidence{
		RunID:    "run-cli",
		Complete: true,
		Sources:  requiredEvidenceSources(true),
	}); err != nil {
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
	if !strings.Contains(stdout.String(), `"status": "PASS"`) || !strings.Contains(stdout.String(), `"report_file"`) {
		t.Fatalf("aggregate stdout missing PASS/report path:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "TEST_REPORT.md")); err != nil {
		t.Fatalf("missing aggregate report: %v", err)
	}
}
