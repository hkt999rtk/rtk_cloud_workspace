package home100k

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func writeTinyEnvRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"env/stack.env":                        "STACK=test\n",
		"adapters/lke/state.env":               "LKE_CLUSTER_ID=123\n",
		"state/kubeconfig.yaml":                "apiVersion: v1\nkind: Config\n",
		"state/video-cloud-staging.state.json": `{"mqtt":{"host":"mqtt.example.invalid","tls_port":8883}}`,
		"services/video-cloud.env":             "VIDEO_CLOUD_BASE_URL=http://example.invalid\n",
		"devices/test_device/loadtest.env":     "LOADTEST=1\n",
		"devices/test_device/summary.json":     `{"count":2}`,
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
	writeHomeSQLiteTestData(t, root, []string{"user-00@example.test", "user-01@example.test"}, []map[string]any{
		{"assignment_index": 0, "assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
		{"assignment_index": 1, "assigned_email": "user-01@example.test", "device_id": "load-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
	})
	return root
}

func writeHomeSQLiteTestData(t *testing.T, envRoot string, users []string, assignments []map[string]any) {
	t.Helper()
	writeHomeSQLiteTestDataForBrand(t, envRoot, "RTK", "brand-cloud-1", "tenant-1", users, assignments)
}

func writeHomeSQLiteTestDataForBrand(t *testing.T, envRoot string, brandname string, brandCloudID string, tenantSlug string, users []string, assignments []map[string]any) {
	t.Helper()
	dbPath := homeTestDataDBPath(envRoot, brandname)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		`create table metadata (key text primary key, value text not null)`,
		`create table users (brandname text not null, email text not null, brand_cloud_id text, tenant_slug text, password text, tokens_json text, app_credentials_json text, app_certificate_json text, body_json text not null, primary key (brandname, email))`,
		`create table device_credentials (brandname text not null, device_id text not null, cert_pem text, key_pem text, chain_pem text, bundle_pem text, metadata_json text, factory_enroll_request_json text, factory_enroll_response_redacted_json text, primary key (brandname, device_id))`,
		`create table device_bindings (brandname text not null, device_id text not null, brand_cloud_id text, tenant_slug text, assignment_index integer not null, assigned_email text not null, device_type text not null, service_options_json text not null, primary key (brandname, device_id))`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`insert into metadata(key, value) values('schema_version', 'rtk-cloud-workspace-test-data/v1')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	userStmt, err := tx.Prepare(`insert into users(brandname, email, brand_cloud_id, tenant_slug, password, tokens_json, app_credentials_json, app_certificate_json, body_json) values(?, ?, ?, ?, 'pw', '{"access_token":"cached-access","refresh_token":"cached-refresh"}', '{}', '{}', ?)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	defer userStmt.Close()
	for _, email := range users {
		if _, err := userStmt.Exec(brandname, email, brandCloudID, tenantSlug, mustJSONText(t, map[string]any{"email": email, "password": "pw"})); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	bindStmt, err := tx.Prepare(`insert into device_bindings(brandname, device_id, brand_cloud_id, tenant_slug, assignment_index, assigned_email, device_type, service_options_json) values(?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	defer bindStmt.Close()
	credentialStmt, err := tx.Prepare(`insert into device_credentials(brandname, device_id, cert_pem, key_pem, chain_pem, bundle_pem, metadata_json, factory_enroll_request_json, factory_enroll_response_redacted_json) values(?, ?, 'cert', 'key', 'chain', 'bundle', ?, '', '')`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	defer credentialStmt.Close()
	for idx, item := range assignments {
		deviceID := item["device_id"].(string)
		deviceType := item["device_type"].(string)
		serviceOptions := item["service_options"].([]string)
		assignmentIndex, _ := item["assignment_index"].(int)
		if assignmentIndex == 0 && idx > 0 {
			assignmentIndex = idx
		}
		if _, err := bindStmt.Exec(brandname, deviceID, brandCloudID, tenantSlug, assignmentIndex, item["assigned_email"].(string), deviceType, mustJSONText(t, serviceOptions)); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := credentialStmt.Exec(brandname, deviceID, mustJSONText(t, map[string]any{"device_id": deviceID, "device_type": deviceType})); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func mustJSONText(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func writeHome100KCoverageArtifacts(t *testing.T, envRoot string) {
	t.Helper()
	users := make([]string, 0, DefaultUserCount)
	for idx := 0; idx < DefaultUserCount; idx++ {
		users = append(users, fmt.Sprintf("user-%04d@example.test", idx))
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
				"assigned_email":   fmt.Sprintf("user-%04d@example.test", deviceIndex%DefaultUserCount),
				"assignment_index": deviceIndex,
				"device_id":        fmt.Sprintf("load-device-%06d", deviceIndex),
				"device_type":      deviceType,
				"service_options":  []string{"mqtt"},
			})
		}
	}
	writeHomeSQLiteTestData(t, envRoot, users, assignments)
}

func TestServerEvidenceProbesUseRunScopedNamespaces(t *testing.T) {
	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(envRoot, "env", "stack.env"),
		[]byte("CLOUD_STACK_NAME=coverage-runtime\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNTIME_COVERAGE_SHARED_TURN_HOST", "turn.shared.example.test")
	probes := serverEvidenceProbes(envRoot, "runtime-123", "--since=5m")
	for _, probe := range probes {
		if probe.command != "bash" || len(probe.args) < 2 {
			continue
		}
		script := probe.args[len(probe.args)-1]
		switch probe.source {
		case "emqx", "emqx_listener_stats", "video_cloud_api", "turn_registry", "coturn":
			if !strings.Contains(script, "coverage-runtime-video-cloud") {
				t.Fatalf("%s probe does not use the run-scoped video namespace:\n%s", probe.source, script)
			}
		case "postgres", "redis_valkey":
			if !strings.Contains(script, "coverage-runtime-platform") {
				t.Fatalf("%s probe does not use the run-scoped platform namespace:\n%s", probe.source, script)
			}
		case "ingress_nginx":
			if !strings.Contains(script, "coverage-runtime-ingress") {
				t.Fatalf("%s probe does not use the run-scoped ingress namespace:\n%s", probe.source, script)
			}
			if !strings.Contains(script, "app.kubernetes.io/name=runtime-coverage-ingress") {
				t.Fatalf("%s probe does not select the runtime coverage ingress:\n%s", probe.source, script)
			}
		}
	}
}

func TestServerEvidenceProbesKeepStagingIngressSelector(t *testing.T) {
	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(envRoot, "env", "stack.env"),
		[]byte("CLOUD_STACK_NAME=video-cloud-staging\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	for _, probe := range serverEvidenceProbes(envRoot, "staging-123", "--since=5m") {
		if probe.source != "ingress_nginx" {
			continue
		}
		script := strings.Join(probe.args, " ")
		if !strings.Contains(script, "app.kubernetes.io/name=ingress-nginx") {
			t.Fatalf("staging ingress probe selector changed unexpectedly:\n%s", script)
		}
		return
	}
	t.Fatal("serverEvidenceProbes() missing ingress_nginx probe")
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

func assertCredentialBundleCounts(t *testing.T, path string, want map[string]int) {
	t.Helper()
	db := openCredentialBundleForTest(t, path)
	defer db.Close()
	for table, expected := range want {
		var got int
		if err := db.QueryRow("select count(*) from " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != expected {
			t.Fatalf("count %s = %d, want %d", table, got, expected)
		}
	}
}

func assertCredentialBundleMetadata(t *testing.T, path string, want map[string]string) {
	t.Helper()
	db := openCredentialBundleForTest(t, path)
	defer db.Close()
	for key, expected := range want {
		var got string
		if err := db.QueryRow(`select value from metadata where key = ?`, key).Scan(&got); err != nil {
			t.Fatalf("metadata %s: %v", key, err)
		}
		if got != expected {
			t.Fatalf("metadata %s = %q, want %q", key, got, expected)
		}
	}
}

func TestCommandRunnerWithTimeoutKillsChildProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	err := commandRunnerWithTimeout(100*time.Millisecond, "sh", "-c", "sleep 30 & echo $! > "+pidFile+"; wait")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("commandRunnerWithTimeout error = %v, want timeout", err)
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read child pid: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if parseErr != nil {
		t.Fatalf("parse child pid: %v", parseErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("child process %d survived parent timeout", pid)
}

func assertCredentialBundleBrandCounts(t *testing.T, path string, want map[string]int) {
	t.Helper()
	db := openCredentialBundleForTest(t, path)
	defer db.Close()
	rows, err := db.Query(`select brandname, count(*) from device_bindings group by brandname`)
	if err != nil {
		t.Fatalf("brand counts: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var brand string
		var count int
		if err := rows.Scan(&brand, &count); err != nil {
			t.Fatal(err)
		}
		got[brand] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bundle brand counts = %#v, want %#v", got, want)
	}
}

func openCredentialBundleForTest(t *testing.T, path string) *sql.DB {
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
	tmp := filepath.Join(t.TempDir(), "bundle.sqlite")
	out, err := os.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, gz); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", tmp)
	if err != nil {
		t.Fatal(err)
	}
	return db
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
		"--env-root", "cloud_env/staging/runtime",
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
		`"ramp_up_time": "30s"`,
		`"stages": [`,
		`"steady_state": "90s"`,
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
	target, ok := decoded["target"].(map[string]any)
	if !ok {
		t.Fatalf("plan output missing target object:\n%s", out)
	}
	if target["target_connects"] != float64(100000) || target["ramp_up_time"] != "30s" {
		t.Fatalf("unexpected target object: %#v", target)
	}
	stages, ok := decoded["stages"].([]any)
	if !ok || len(stages) != 1 {
		t.Fatalf("unexpected stages payload: %#v", decoded["stages"])
	}
}

func TestBrandPlanDistributesMultiBrandShardBundles(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	writeHomeSQLiteTestDataForBrand(t, envRoot, "RTK-PRIMARY", "brand-primary", "tenant-primary", []string{"primary+001@users.local", "primary+002@users.local"}, []map[string]any{
		{"assignment_index": 0, "assigned_email": "primary+001@users.local", "device_id": "primary-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
		{"assignment_index": 1, "assigned_email": "primary+002@users.local", "device_id": "primary-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
	})
	writeHomeSQLiteTestDataForBrand(t, envRoot, "RTK-SMALL-1", "brand-small", "tenant-small", []string{"small+001@users.local"}, []map[string]any{
		{"assignment_index": 0, "assigned_email": "small+001@users.local", "device_id": "small-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
	})
	planFile := filepath.Join(envRoot, "env", "loadtest-brand-plan.json")
	if err := os.WriteFile(planFile, []byte(`{
		"total_devices": 3,
		"devices_per_user": 1,
		"brands": [
			{"brandname":"RTK-PRIMARY","devices":2,"normal_users":2,"developer_users":{"owner":1,"admin":1}},
			{"brandname":"RTK-SMALL-1","devices":1,"normal_users":1,"developer_users":{"owner":1,"admin":1}}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(PlanOptions{
		EnvRoot:                   envRoot,
		Brandname:                 "RTK",
		BrandPlanFile:             planFile,
		Region:                    "us-sea",
		LoadGeneratorDevicesPerVM: 3,
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	if plan.Conditions.Devices != 3 || plan.Conditions.Users != 3 || plan.Conditions.DeveloperUsers != 4 {
		t.Fatalf("plan counts devices=%d users=%d developers=%d", plan.Conditions.Devices, plan.Conditions.Users, plan.Conditions.DeveloperUsers)
	}
	if len(plan.BrandDistribution) != 2 {
		t.Fatalf("brand distribution = %#v", plan.BrandDistribution)
	}

	bundle, err := writeShardCredentialBundle(filepath.Join(envRoot, "out"), envRoot, plan, plan.Assignments[0])
	if err != nil {
		t.Fatalf("writeShardCredentialBundle() error = %v", err)
	}
	assertCredentialBundleCounts(t, bundle.CompressedPath, map[string]int{
		"devices":         3,
		"users":           3,
		"device_bindings": 3,
	})
	assertCredentialBundleBrandCounts(t, bundle.CompressedPath, map[string]int{
		"RTK-PRIMARY": 2,
		"RTK-SMALL-1": 1,
	})
}

func TestExecutePlanAcceptsConfiguredStageDurations(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"--", "plan",
		"--env-root", "cloud_env/staging/runtime",
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
	for _, want := range []string{`"warm_up": "1m"`, `"steady_state": "3m"`, `"cool_down": "30s"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q:\n%s", want, out)
		}
	}
}

func TestExecutePlanRejectsLegacyStageProfileFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"--", "plan",
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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

func TestExecuteProvisionVMsDryRunIncludesVideoOnlyGenerators(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "9000",
		"--vm-count", "2",
		"--video-generator-vm-count", "2",
		"--video-generator-label-prefix", "vg",
		"--run-id", "run-cli",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(provision-vms dry-run) code = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		`"role": "video"`,
		`"label": "vg01"`,
		`"label": "vg02"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestExecuteProvisionVMsRequiresConfirmForLive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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

func TestExecuteProvisionVMsLiveUsesExistingHostsWithoutLinodeAPI(t *testing.T) {
	outDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "2000",
		"--vm-count", "2",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--existing-hosts", "lg01=203.0.113.101,lg02=203.0.113.102",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(provision-vms live existing hosts) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "vms.json"))
	if err != nil {
		t.Fatalf("read vms.json: %v", err)
	}
	body := string(raw)
	for _, want := range []string{`"label": "lg01"`, `"public_ipv4": "203.0.113.101"`, `"source": "existing-host"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("vms.json missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(stdout.String(), `"source": "existing-host"`) {
		t.Fatalf("stdout missing existing-host source:\n%s", stdout.String())
	}
}

func TestExecuteProvisionVMsLiveExistingHostsRequireAllLoadAssignments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"provision-vms",
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--devices", "2000",
		"--vm-count", "2",
		"--run-id", "run-cli",
		"--live",
		"--existing-hosts", "lg01=203.0.113.101",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("Execute(provision-vms live missing existing host) code = 0 stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "missing load-generator assignments: lg02") {
		t.Fatalf("stderr missing assignment failure: %s", stderr.String())
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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

func TestExecuteDestroyVMsLiveSkipsExistingHosts(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"},
			{Label: "lg02", PublicIPv4: "203.0.113.102", Source: "existing-host"},
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
		deleted = append(deleted, r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"destroy-vms",
		"--env-root", "cloud_env/staging/runtime",
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
		t.Fatalf("Execute(destroy-vms live skip existing) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/linode/instances/101") {
		t.Fatalf("deleted paths = %#v", deleted)
	}
	if !strings.Contains(stdout.String(), `"skipped"`) || !strings.Contains(stdout.String(), `"source": "existing-host"`) {
		t.Fatalf("stdout missing skipped existing host:\n%s", stdout.String())
	}
}

func TestExecuteListVMsDefaultsToDryRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"list-vms",
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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

func TestExecuteSyncLiveHonorsExplicitVMCountOverride(t *testing.T) {
	outDir := t.TempDir()
	envRoot := writeTinyEnvRoot(t)
	writeHome100KCoverageArtifacts(t, envRoot)
	stateFile := filepath.Join(outDir, "vms.json")
	state := map[string]any{
		"created": []LinodeVM{
			{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"},
			{ID: 102, Label: "lg02", PublicIPv4: "203.0.113.102"},
			{ID: 201, Label: "vg01", PublicIPv4: "203.0.113.201"},
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
		"--vm-count", "2",
		"--video-generator-vm-count", "1",
		"--load-generator-devices-per-vm", "4500",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--vm-state-file", stateFile,
		"--remote-workspace", "/root/rtk_cloud_workspace",
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/runtime",
		"--ssh-user", "root",
		"--ssh-key", "/tmp/test-key",
		"--generator-hosts-override-ip", "172.232.190.230",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(sync live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o '" + filepath.Join(outDir, "bin", "home-100k-linux-amd64") + "' ./loadtests/home-100k/cmd/home-100k",
		"GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o '" + filepath.Join(outDir, "bin", "cloud-mqtt-test-linux-amd64") + "' ./scripts/go/cloud-mqtt-test",
		"ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json"),
		"ansible/sync.yml",
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
	for _, want := range []string{
		`"ansible_host": "203.0.113.101"`,
		`"shard_index": 0`,
		`"run_id": "run-cli"`,
		`"role": "mixed"`,
		`ServerAliveInterval=5`,
		`ServerAliveCountMax=1`,
		`ConnectTimeout=10`,
	} {
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
	if got := extraVars["scenario_profile"]; got != DefaultScenarioProfile {
		t.Fatalf("extra vars scenario_profile = %v, want %q", got, DefaultScenarioProfile)
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
	if extraVars["rsync_timeout"] != "90" {
		t.Fatalf("extra vars rsync_timeout = %q, want 90", extraVars["rsync_timeout"])
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
	if extraVars["load_generator_devices_per_vm"] != float64(4500) {
		t.Fatalf("extra vars load_generator_devices_per_vm = %#v, want 4500", extraVars["load_generator_devices_per_vm"])
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
	lg02Manifest, _ := inventoryDoc.All.Children["home_100k"].Hosts["lg02"]["local_shard_manifest"].(string)
	if !filepath.IsAbs(lg02Manifest) {
		t.Fatalf("inventory lg02 local_shard_manifest = %q, want absolute path", lg02Manifest)
	}
	vg01Archive, _ := inventoryDoc.All.Children["home_100k"].Hosts["vg01"]["local_env_archive"].(string)
	if !filepath.IsAbs(vg01Archive) {
		t.Fatalf("inventory vg01 local_env_archive = %q, want absolute path", vg01Archive)
	}
	assertShardManifestRange(t, localManifest, "device-mqtt", 0, 4500)
	assertShardManifestRange(t, lg02Manifest, "device-mqtt", 4500, 9000)
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
	assertCredentialBundleCounts(t, filepath.Join(outDir, "credential-bundles", "lg01.sqlite.gz"), map[string]int{
		"devices":         4500,
		"users":           450,
		"device_bindings": 4500,
	})
	assertCredentialBundleMetadata(t, filepath.Join(outDir, "credential-bundles", "lg01.sqlite.gz"), map[string]string{
		"brand_cloud_id": "brand-cloud-1",
		"tenant_slug":    "tenant-1",
	})
	vg01ArchiveNames := readTarGzNames(t, vg01Archive)
	if !vg01ArchiveNames["loadtests/home-100k/credentials/vg01.no-shard.txt"] {
		t.Fatalf("video-only env archive missing no-shard marker: %#v", vg01ArchiveNames)
	}
	if vg01ArchiveNames["loadtests/home-100k/credentials/vg01.sqlite.gz"] {
		t.Fatalf("video-only env archive should not include shard credential bundle: %#v", vg01ArchiveNames)
	}
	commonArchive := filepath.Join(outDir, "artifact-store", "common", "env-common.tar.gz")
	if _, err := os.Stat(commonArchive); err != nil {
		t.Fatalf("missing common env archive: %v", err)
	}
	commonNames := readTarGzNames(t, commonArchive)
	for _, want := range []string{"env/stack.env", "state/kubeconfig.yaml", "state/video-cloud-staging.state.json", "services/video-cloud.env", "devices/test_device/loadtest.env", "devices/test_device/summary.json"} {
		if !commonNames[want] {
			t.Fatalf("common env archive missing %q", want)
		}
	}
	if commonNames["adapters/lke/state.env"] {
		t.Fatal("common env archive must not expose provider adapter state")
	}
	for _, forbidden := range []string{"devices/test_device/manifests/devices.json", "artifacts/users/rtk-users-20260616T000000Z.json", "artifacts/device-bind/rtk-device-bind-20260616T000000Z.json"} {
		if commonNames[forbidden] {
			t.Fatalf("common env archive included legacy test-data artifact %q", forbidden)
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
	for _, want := range []string{`"role": "device-mqtt"`, `"start": 0`, `"end": 4500`, `"role": "user-app"`, `"start": 0`, `"end": 225`} {
		if !strings.Contains(manifest0, want) {
			t.Fatalf("lg01 manifest missing %q:\n%s", want, manifest0)
		}
	}
	for _, want := range []string{`"role": "device-mqtt"`, `"start": 4500`, `"end": 9000`, `"role": "user-app"`, `"start": 225`, `"end": 450`} {
		if !strings.Contains(manifest1, want) {
			t.Fatalf("lg02 manifest missing %q:\n%s", want, manifest1)
		}
	}
	if !strings.Contains(stdout.String(), `"synced"`) || !strings.Contains(stdout.String(), `"id": 101`) {
		t.Fatalf("stdout missing synced VMs:\n%s", stdout.String())
	}
}

func TestRunAnsiblePlaybookUsesForksOverride(t *testing.T) {
	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")
	t.Setenv("HOME100K_ANSIBLE_FORKS", "1")
	t.Setenv("HOME100K_OUT_DIR", outDir)
	old := commandRunner
	defer func() { commandRunner = old }()
	var calls []string
	commandRunner = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	if err := os.MkdirAll(filepath.Join(outDir, "ansible"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runAnsiblePlaybook(workflowFlagValues{outDir: outDir}, "sync.yml"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "ansible-playbook --forks 1 -i "+filepath.Join(outDir, "ansible", "inventory.json")) {
		t.Fatalf("ansible forks override not used:\n%s", joined)
	}
}

func TestWriteEnvRsyncFilterExcludesExpandedDeviceCredentials(t *testing.T) {
	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "devices", "test_device", "manifests"), 0o755); err != nil {
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
		"+ /env/***",
		"+ /services/***",
		"+ /devices/test_device/loadtest.env",
		"+ /devices/test_device/summary.json",
	} {
		if !strings.Contains(filter, want) {
			t.Fatalf("filter missing %q:\n%s", want, filter)
		}
	}
	for _, forbidden := range []string{
		"/devices/test_device/manifests/",
		"/devices/test_device/devices/",
		"/devices/test_device/bundles/",
		"/artifacts/users/",
		"/artifacts/device-bind/",
	} {
		if strings.Contains(filter, forbidden) {
			t.Fatalf("filter included legacy test-data path %q:\n%s", forbidden, filter)
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
		"remote_common_env_stack_stat",
		"remote_common_env_kubeconfig_stat",
		"remote_shard_credentials_stat",
		"runner_needs_upload",
		"env_archive_needs_upload",
		"remote_extracted",
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
		"rsync_retries",
		"rsync_retry_delay",
		"until: runner_copy.rc == 0",
		"until: rtk_cloud_copy.rc == 0",
		"until: cloud_mqtt_test_copy.rc == 0",
		"until: manifest_copy.rc == 0",
		"until: common_env_archive_copy.rc == 0",
		"until: env_archive_copy.rc == 0",
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
	if !strings.Contains(body, "retries: \"{{ rsync_retries | default(6) }}\"") || !strings.Contains(body, "delay: \"{{ rsync_retry_delay | default(5) }}\"") {
		t.Fatalf("sync.yml should retry transient fanout SSH/rsync failures with a short delay:\n%s", body)
	}
}

func TestEnvArchiveUsesBoundDeviceShardSelection(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	writeHomeSQLiteTestData(t, envRoot,
		[]string{"user-00@example.test", "user-01@example.test", "user-02@example.test"},
		[]map[string]any{
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-01@example.test", "device_id": "load-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-02@example.test", "device_id": "load-device-0003", "device_type": "air_conditioner", "service_options": []string{"mqtt"}},
		})
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
	writeHomeSQLiteTestData(t, envRoot,
		[]string{"user-00@example.test", "user-01@example.test"},
		[]map[string]any{
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-01@example.test", "device_id": "load-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
		})
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
	writeHomeSQLiteTestData(t, envRoot,
		[]string{"user-00@example.test", "user-01@example.test"},
		[]map[string]any{
			{"assigned_email": "user-00@example.test", "device_id": "load-device-0001", "device_type": "light", "service_options": []string{"mqtt"}},
			{"assigned_email": "user-01@example.test", "device_id": "load-device-0002", "device_type": "smart_meter", "service_options": []string{"mqtt"}},
		})
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

func TestEnvArchiveExcludesLegacyUsersAndDeviceBindArtifacts(t *testing.T) {
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
	for _, forbidden := range []string{
		"artifacts/users/rtk-users-20260614T010000Z.json",
		"artifacts/users/rtk-users-20260615T010000Z.json",
		"artifacts/device-bind/rtk-device-bind-20260614T010000Z.json",
		"artifacts/device-bind/rtk-device-bind-20260615T010000Z.json",
	} {
		if names[forbidden] {
			t.Fatalf("archive included legacy test-data artifact %q", forbidden)
		}
	}
}

func TestEnvArchiveIsDeterministicForUnchangedShardInputs(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	writeHomeSQLiteTestData(t, envRoot,
		[]string{"user-00@example.test"},
		[]map[string]any{{"device_id": "load-device-0001", "device_type": "light", "assigned_email": "user-00@example.test", "service_options": []string{"mqtt"}}})
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

func TestWriteCommonEnvArchiveAddsRemoteLoggerCredentials(t *testing.T) {
	envRoot := writeTinyEnvRoot(t)
	loggerDir := filepath.Join(envRoot, "services", "cloud-logger")
	if err := os.MkdirAll(loggerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loggerDir, "logger.env"), []byte("CLOUD_LOGGER_ENDPOINT=https://logger.example\nCLOUD_LOGGER_INGEST_TOKEN=test-ingest-token\n"), 0o600); err != nil {
		t.Fatal(err)
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
	if !names["state/cloud-logger.env"] {
		t.Fatalf("common archive missing remote logger credentials: %#v", names)
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
	for _, want := range []string{`ssh_args`, `Compression=no`, `ControlMaster=no`, `ServerAliveInterval=5`, `ServerAliveCountMax=1`} {
		if !strings.Contains(body, want) {
			t.Fatalf("ansible.cfg missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `ControlMaster=auto`) {
		t.Fatalf("ansible.cfg should not use SSH multiplexing for flaky load-generator VMs:\n%s", body)
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
		"current_report_result()",
		"[[ \"$(current_report_status)\" == \"COMPLETE\" && \"$(current_report_result)\" == \"SUCCESS\" ]]",
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
	for _, want := range []string{`requires existing VM state`, `set_phase "sync"`, `run_live_sync_with_retries`} {
		if !strings.Contains(resume, want) {
			t.Fatalf("workflow-resume-live missing %q:\n%s", want, resume)
		}
	}
}

func TestHome100KScriptHasBoundedLocalLiveWorkflow(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"workflow-local-live)",
		"HOME100K_LOCAL_LIVE_MAX_DEVICES",
		"local_shard_args=(",
		"run_home100k shard-run",
		`"${local_shard_args[@]}"`,
		"--runner-mode live",
		"--rtk-cloud-binary \"$rtk_cloud_binary\"",
		"run_video_loadtest_step",
		"run_clip_storage_loadtest_step",
		"run_home100k collect-server-evidence",
		"run_home100k aggregate",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("home-100k.sh missing bounded local-live marker %q:\n%s", want, body)
		}
	}
	localLive, _, ok := strings.Cut(body, "\n  workflow-resume-live)")
	if !ok {
		t.Fatal("home-100k.sh local-live case is not terminated")
	}
	localLive = localLive[strings.LastIndex(localLive, "\n  workflow-local-live)"):]
	_, shardCall, ok := strings.Cut(localLive, "run_home100k shard-run")
	if !ok {
		t.Fatal("home-100k.sh local-live shard call is missing")
	}
	shardCall, _, ok = strings.Cut(shardCall, "--run-id \"$run_id\"")
	if !ok {
		t.Fatal("home-100k.sh local-live shard call is missing")
	}
	if strings.Contains(shardCall, `"${workflow_args[@]}"`) {
		t.Fatal("local-live shard-run must not receive VM workflow-only flags")
	}
}

func TestHome100KScriptSyncRetrySurvivesSetE(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	_, retryFunc, ok := strings.Cut(body, "\nrun_live_sync_with_retries()")
	if !ok {
		t.Fatal("home-100k.sh missing run_live_sync_with_retries")
	}
	retryFunc, _, ok = strings.Cut(retryFunc, "\ncommand=")
	if !ok {
		t.Fatal("home-100k.sh retry function is not terminated before command dispatch")
	}
	if !strings.Contains(retryFunc, "if run_home100k sync ") || !strings.Contains(retryFunc, "else") || !strings.Contains(retryFunc, "rc=$?") {
		t.Fatalf("sync retry must wrap run_home100k in if/else so set -e does not abort before retry:\n%s", retryFunc)
	}
}

func TestSyncPlaybookSkipsShardCredentialRequirementForVideoOnlyVMs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ansible", "sync.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `when: role | default('mixed') == 'mixed'`) {
		t.Fatalf("sync.yml must only stat shard credentials for mixed shard VMs")
	}
	if !strings.Contains(body, `(role | default('mixed') == 'mixed') and (not (remote_shard_credentials_stat.stat.exists | default(false)))`) {
		t.Fatalf("sync.yml env_archive_needs_upload must not require shard credentials for video-only VMs")
	}
}

func TestHome100KScriptSkipsVMResourceSSHDuringBootstrap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	_, nodeStatus, ok := strings.Cut(body, "\nnode_resource_status()")
	if !ok {
		t.Fatal("home-100k.sh missing node_resource_status")
	}
	nodeStatus, _, ok = strings.Cut(nodeStatus, "\n}\n\nk8s_kubeconfig")
	if !ok {
		t.Fatal("home-100k.sh node_resource_status is not terminated before k8s_kubeconfig")
	}
	for _, want := range []string{"starting|provision-vms|sync)", "return"} {
		if !strings.Contains(nodeStatus, want) {
			t.Fatalf("node_resource_status must skip SSH polling during bootstrap phase, missing %q:\n%s", want, nodeStatus)
		}
	}
}

func TestHome100KScriptNodeInventoryUsesVMRoleTags(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`tags = set(str(tag) for tag in (vm.get("tags") or []))`,
		`if "video" in tags:`,
		`role = "video"`,
		`elif "mixed" in tags:`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("home-100k.sh node inventory missing role/tag marker %q:\n%s", want, body)
		}
	}
}

func TestHome100KScriptSingleCollectPassesSSHKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	_, collectCase, ok := strings.Cut(body, "\n  collect)")
	if !ok {
		t.Fatal("home-100k.sh missing collect case")
	}
	collectCase, _, ok = strings.Cut(collectCase, "\n    ;;")
	if !ok {
		t.Fatal("home-100k.sh collect case is not terminated")
	}
	if !strings.Contains(collectCase, `--ssh-key "$ssh_key"`) {
		t.Fatalf("single-step collect must pass configured ssh key:\n%s", collectCase)
	}
}

func TestHome100KScriptEnvOverridesDescriptionRampUpAndRuntimeWindows(t *testing.T) {
	outDir := t.TempDir()
	descriptionFile := filepath.Join(outDir, "description.env")
	if err := os.WriteFile(descriptionFile, []byte(strings.Join([]string{
		"HOME100K_STAGE_WARM_UP=9m",
		"HOME100K_STAGE_STEADY=9m",
		"HOME100K_STAGE_COOL_DOWN=9m",
		"HOME100K_DEVICES=12000",
		"HOME100K_DEVICES_PER_USER=10",
		"HOME100K_MQTT_ADDR=127.0.0.1:8883",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "plan")
	cmd.Env = home100KTestEnv(
		"HOME100K_DESCRIPTION_FILE="+descriptionFile,
		"HOME100K_RUN_ID=test-env-priority",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
		"HOME100K_STAGE_WARM_UP=15s",
		"HOME100K_STAGE_STEADY=45s",
		"HOME100K_STAGE_COOL_DOWN=15s",
		"HOME100K_DEVICES=9000",
		"HOME100K_DEVICES_PER_USER=20",
	)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("home-100k.sh plan failed: %v\n%s", err, raw)
	}
	body := string(raw)
	for _, want := range []string{`"warm_up": "15s"`, `"steady_state": "45s"`, `"cool_down": "15s"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("plan missing env override %q:\n%s", want, body)
		}
	}
	for _, want := range []string{`"devices": 9000`, `"users": 450`, `"target_connects": 9000`} {
		if !strings.Contains(body, want) {
			t.Fatalf("plan missing size override %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"warm_up": "9m"`) || strings.Contains(body, `"steady_state": "9m"`) {
		t.Fatalf("description file overrode explicit env:\n%s", body)
	}
	if strings.Contains(body, `"devices": 12000`) {
		t.Fatalf("description file size overrode explicit env:\n%s", body)
	}
}

func TestHome100KScriptDefaultDescriptionPlansTenMinuteLoadWindow(t *testing.T) {
	outDir := t.TempDir()
	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "plan")
	cmd.Env = home100KTestEnv(
		"HOME100K_RUN_ID=test-default-duration",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
	)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("home-100k.sh plan failed: %v\n%s", err, raw)
	}
	body := string(raw)
	for _, want := range []string{`"warm_up": "30s"`, `"steady_state": "90s"`, `"cool_down": "30s"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("plan missing default duration %q:\n%s", want, body)
		}
	}
}

func TestHome100KScriptPassesLinodeTypeToProvision(t *testing.T) {
	outDir := t.TempDir()
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(outDir, "go.log")
	goStub := filepath.Join(binDir, "go")
	if err := os.WriteFile(goStub, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> "+shellQuoteForTest(goLog)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "provision-vms")
	cmd.Env = home100KTestEnv(
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME100K_RUN_ID=test-linode-type",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
		"HOME100K_LINODE_TYPE=g6-standard-6",
	)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("home-100k.sh provision-vms failed: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "--linode-type g6-standard-6") {
		t.Fatalf("provision-vms did not receive --linode-type:\n%s", body)
	}
}

func TestHome100KScriptPassesExistingGeneratorHostsToProvision(t *testing.T) {
	outDir := t.TempDir()
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(outDir, "go.log")
	goStub := filepath.Join(binDir, "go")
	if err := os.WriteFile(goStub, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> "+shellQuoteForTest(goLog)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "provision-vms")
	cmd.Env = home100KTestEnv(
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME100K_RUN_ID=test-existing-hosts",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
		"HOME100K_EXISTING_GENERATOR_HOSTS=lg01=203.0.113.101,lg02=203.0.113.102",
	)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("home-100k.sh provision-vms failed: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "--existing-hosts lg01=203.0.113.101,lg02=203.0.113.102") {
		t.Fatalf("provision-vms did not receive --existing-hosts:\n%s", body)
	}
}

func TestHome100KScriptAutoDiscoversPublicMQTTOnlyForLiveCommands(t *testing.T) {
	outDir := t.TempDir()
	descriptionFile := filepath.Join(outDir, "description.env")
	if err := os.WriteFile(descriptionFile, []byte(strings.Join([]string{
		"HOME100K_ENV_ROOT=cloud_env/staging/runtime",
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
	syncCmd := exec.Command("bash", script, "sync", "--live", "--remote-workspace", "/root/ws", "--remote-env-root", "/root/ws/cloud_env/staging/runtime", "--ssh-key", "/tmp/key")
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

func TestHome100KScriptDocumentsWebRTCOnlyWorkflow(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "--help")
	cmd.Env = home100KTestEnv(
		"HOME100K_REGION=",
		"HOME100K_ENV_ROOT="+t.TempDir(),
	)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("home-100k.sh --help failed: %v\n%s", err, raw)
	}
	body := string(raw)
	for _, want := range []string{
		"workflow-video-live",
		"workflow-video-resume-live",
		"Run only the live WebRTC video lifecycle",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("help missing %q:\n%s", want, body)
		}
	}
}

func TestHome100KScriptRequiresResolvedRegionForPlan(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "plan")
	cmd.Env = home100KTestEnv(
		"HOME100K_REGION=",
		"HOME100K_ENV_ROOT="+t.TempDir(),
	)
	raw, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("home-100k.sh plan unexpectedly passed without a provider region:\n%s", raw)
	}
	if !strings.Contains(string(raw), "provider region is unresolved") {
		t.Fatalf("home-100k.sh plan reported the wrong error:\n%s", raw)
	}
}

func TestHome100KClipMixedInventoryUsesConfiguredDeviceCount(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	start := strings.Index(body, "\nrun_clip_storage_loadtest_step() {")
	if start < 0 {
		t.Fatal("home-100k.sh missing run_clip_storage_loadtest_step")
	}
	end := strings.Index(body[start:], "\ncollect_clip_storage_evidence() {")
	if end < 0 {
		t.Fatal("run_clip_storage_loadtest_step is not terminated before evidence collection")
	}
	helper := body[start : start+end]
	if !strings.Contains(helper, `python3 - "$(local_env_root_path)" "$device_count"`) {
		t.Fatalf("clip mixed inventory does not use configured device_count:\n%s", helper)
	}
	if strings.Contains(helper, `python3 - "$(local_env_root_path)" "$devices"`) {
		t.Fatalf("clip mixed inventory references function-local video variable:\n%s", helper)
	}
}

func TestMQTTDeviceTrafficProfileUsesHomeProfileForFeatureWorkloads(t *testing.T) {
	for _, test := range []struct {
		name string
		plan Plan
		want string
	}{
		{name: "shadow", plan: Plan{ScenarioProfile: "home-diverse-v1"}, want: "home-diverse-v1"},
		{name: "shadow canary", plan: Plan{ScenarioProfile: MQTTShadowCanaryScenarioProfile}, want: DefaultScenarioProfile},
		{name: "video", plan: Plan{ScenarioProfile: Video1KScenarioProfile, VideoProfile: VideoProfile{Name: Video1KScenarioProfile}}, want: DefaultScenarioProfile},
		{name: "clip", plan: Plan{ScenarioProfile: "clip-storage-1k-v1", ClipStorageProfile: ClipStorageProfile{Name: "clip-storage-1k-v1"}}, want: DefaultScenarioProfile},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mqttDeviceTrafficProfile(test.plan); got != test.want {
				t.Fatalf("mqttDeviceTrafficProfile() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHome100KScriptWebRTCOnlyWorkflowGeneratesRunLevelReport(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	start := strings.Index(body, "\nrun_video_live_workflow() {")
	if start < 0 {
		t.Fatal("home-100k.sh missing run_video_live_workflow")
	}
	end := strings.Index(body[start:], "\ncommand=\"${1:-workflow-live}\"")
	if end < 0 {
		t.Fatal("home-100k.sh video workflow helper is not terminated before command dispatch")
	}
	helper := body[start : start+end]
	for _, want := range []string{
		`set_phase "aggregate"`,
		`run_home100k aggregate`,
		`generate_report_from_artifacts`,
		`current_report_status`,
		`current_report_result`,
		`should_shutdown_after_workflow`,
	} {
		if !strings.Contains(helper, want) {
			t.Fatalf("run_video_live_workflow missing %q:\n%s", want, helper)
		}
	}
}

func TestHome100KScriptVideoLadderDefaultsConcurrencyToStepViewers(t *testing.T) {
	outDir := t.TempDir()
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	videoLog := filepath.Join(outDir, "video.log")
	videoStub := filepath.Join(binDir, "video-loadtest")
	videoStubBody := `#!/usr/bin/env bash
printf '%s,%s,%s,%s\n' "$VIDEO_CLOUD_LOAD_VIRTUAL_VIEWERS" "$VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES" "$VIDEO_CLOUD_LOAD_VIEWER_CONCURRENCY" "$VIDEO_CLOUD_LOAD_DEVICE_CONCURRENCY" >> ` + shellQuoteForTest(videoLog) + `
`
	if err := os.WriteFile(videoStub, []byte(videoStubBody), 0o755); err != nil {
		t.Fatal(err)
	}
	tokenMap := filepath.Join(outDir, "tokens.json")
	if err := os.WriteFile(tokenMap, []byte(`{"device-001":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	deviceIDs := make([]string, 0, 500)
	for i := 1; i <= 500; i++ {
		deviceIDs = append(deviceIDs, fmt.Sprintf("device-%03d", i))
	}

	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "run-video-loadtest")
	cmd.Env = home100KTestEnv(
		"HOME100K_RUN_ID=test-video-ladder-concurrency",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
		"HOME100K_SCENARIO_PROFILE=video-50k-turn-v1",
		"HOME100K_VIDEO_LOADTEST=on",
		"HOME100K_VIDEO_LOADTEST_SCRIPT="+videoStub,
		"HOME100K_VIDEO_LOADTEST_ARTIFACT_DIR="+filepath.Join(outDir, "video"),
		"HOME100K_VIDEO_LOADTEST_LADDER=100,500",
		"HOME100K_VIDEO_LOADTEST_STEP_COOLDOWN=0s",
		"VIDEO_CLOUD_LOAD_DEVICE_IDS="+strings.Join(deviceIDs, ","),
		"VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE="+tokenMap,
		"VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE="+tokenMap,
	)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-video-loadtest failed: %v\n%s", err, raw)
	}
	raw, err := os.ReadFile(videoLog)
	if err != nil {
		t.Fatalf("read video log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{"100,100,100,100", "500,500,500,500"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("video ladder concurrency log = %#v, want %#v", lines, want)
	}
}

func TestHome100KScriptVideoLadderFailsWhenDeviceInventoryTooSmall(t *testing.T) {
	outDir := t.TempDir()
	videoLog := filepath.Join(outDir, "video.log")
	videoStub := filepath.Join(outDir, "video-stub.sh")
	videoStubBody := `#!/usr/bin/env bash
printf '%s,%s\n' "$VIDEO_CLOUD_LOAD_VIRTUAL_VIEWERS" "$VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES" >> ` + shellQuoteForTest(videoLog) + `
`
	if err := os.WriteFile(videoStub, []byte(videoStubBody), 0o755); err != nil {
		t.Fatal(err)
	}
	tokenMap := filepath.Join(outDir, "tokens.json")
	if err := os.WriteFile(tokenMap, []byte(`{"device-001":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	deviceIDs := make([]string, 0, 100)
	for i := 1; i <= 100; i++ {
		deviceIDs = append(deviceIDs, fmt.Sprintf("device-%03d", i))
	}

	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "run-video-loadtest")
	cmd.Env = home100KTestEnv(
		"HOME100K_RUN_ID=test-video-ladder-inventory",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
		"HOME100K_SCENARIO_PROFILE=video-50k-turn-v1",
		"HOME100K_VIDEO_LOADTEST=on",
		"HOME100K_VIDEO_LOADTEST_SCRIPT="+videoStub,
		"HOME100K_VIDEO_LOADTEST_ARTIFACT_DIR="+filepath.Join(outDir, "video"),
		"HOME100K_VIDEO_LOADTEST_LADDER=100,500",
		"HOME100K_VIDEO_LOADTEST_STEP_COOLDOWN=0s",
		"VIDEO_CLOUD_LOAD_DEVICE_IDS="+strings.Join(deviceIDs, ","),
		"VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE="+tokenMap,
		"VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE="+tokenMap,
	)
	raw, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("run-video-loadtest unexpectedly passed\n%s", raw)
	}
	if !strings.Contains(string(raw), "video loadtest device inventory insufficient for step: requested_devices=500 available_devices=100") {
		t.Fatalf("output missing inventory failure:\n%s", raw)
	}
	logRaw, err := os.ReadFile(videoLog)
	if err != nil {
		t.Fatalf("read video log: %v", err)
	}
	if got := strings.TrimSpace(string(logRaw)); got != "100,100" {
		t.Fatalf("video stub log = %q, want only step-100 before inventory failure", got)
	}
}

func TestHome100KScriptProportionalVideoShardsUseMQTTShardInventory(t *testing.T) {
	outDir := t.TempDir()
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sshLog := filepath.Join(outDir, "ssh.log")
	sshStub := filepath.Join(binDir, "ssh")
	sshStubBody := `#!/usr/bin/env bash
printf 'SSH %s\n' "$*" >> ` + shellQuoteForTest(sshLog) + `
cat >/dev/null || true
`
	if err := os.WriteFile(sshStub, []byte(sshStubBody), 0o755); err != nil {
		t.Fatal(err)
	}
	scpStub := filepath.Join(binDir, "scp")
	if err := os.WriteFile(scpStub, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	goStub := filepath.Join(binDir, "go")
	goStubBody := `#!/usr/bin/env bash
out_env=""
ids_file=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-env) out_env="$2"; shift 2 ;;
    --device-ids-file) ids_file="$2"; shift 2 ;;
    *) shift ;;
  esac
done
mkdir -p "$(dirname "$out_env")"
python3 - "$ids_file" "$out_env" <<'PY'
import json, pathlib, sys
ids = [line.strip() for line in pathlib.Path(sys.argv[1]).read_text().splitlines() if line.strip()]
out = pathlib.Path(sys.argv[2])
device_map = out.with_name("device-token-map.json")
app_map = out.with_name("app-token-map.json")
device_map.write_text(json.dumps({device_id: "device-token" for device_id in ids}), encoding="utf-8")
app_map.write_text(json.dumps({device_id: "app-token" for device_id in ids}), encoding="utf-8")
out.write_text(
    "export VIDEO_CLOUD_LOAD_DEVICE_IDS='" + ",".join(ids) + "'\n"
    "export VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES='" + str(len(ids)) + "'\n"
    "export VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE='" + str(device_map) + "'\n"
    "export VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE='" + str(app_map) + "'\n"
    "export VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN='account-token'\n",
    encoding="utf-8",
)
PY
`
	if err := os.WriteFile(goStub, []byte(goStubBody), 0o755); err != nil {
		t.Fatal(err)
	}
	videoBinary := filepath.Join(outDir, "rtk-video-loadtest")
	if err := os.WriteFile(videoBinary, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	envRoot := filepath.Join(outDir, "env-root")
	users := []string{"user@example.test"}
	assignments := make([]map[string]any, 0, 100)
	for i := 1; i <= 100; i++ {
		assignments = append(assignments, map[string]any{
			"assignment_index": i,
			"assigned_email":   users[0],
			"device_id":        fmt.Sprintf("device-%03d", i),
			"device_type":      "camera",
			"service_options":  []string{"mqtt", "video_streaming"},
		})
	}
	writeHomeSQLiteTestData(t, envRoot, users, assignments)
	reportDir := filepath.Join(outDir, "report")
	manifestDir := filepath.Join(reportDir, "credential-bundles")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeManifest := func(label string, start, count int) {
		ids := make([]string, 0, count)
		for i := start; i < start+count; i++ {
			ids = append(ids, fmt.Sprintf("device-%03d", i))
		}
		raw, err := json.Marshal(map[string]any{
			"label":        label,
			"format":       "home-100k-credential-bundle/v1",
			"device_count": count,
			"device_ids":   ids,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(manifestDir, label+".manifest.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest("lg01", 1, 60)
	writeManifest("lg02", 61, 40)
	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "run-video-loadtest")
	cmd.Env = home100KTestEnv(
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME100K_ENV_ROOT="+envRoot,
		"HOME100K_REGION=us-sea",
		"HOME100K_RUN_ID=test-video-proportional",
		"HOME100K_OUT_DIR="+reportDir,
		"HOME100K_SCENARIO_PROFILE=video-100k-turn-v1",
		"HOME100K_VIDEO_LOADTEST=on",
		"HOME100K_VIDEO_LOADTEST_MODE=remote-sharded",
		"HOME100K_VIDEO_LOADTEST_SHARD_MODE=proportional",
		"HOME100K_VIDEO_LOADTEST_REMOTE_HOSTS=lg01=192.0.2.1,lg02=192.0.2.2",
		"HOME100K_VIDEO_LOADTEST_BINARY="+videoBinary,
		"HOME100K_VIDEO_LOADTEST_ARTIFACT_DIR="+filepath.Join(outDir, "video"),
		"HOME100K_VIDEO_LOADTEST_LADDER=100",
		"HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST=100",
		"HOME100K_VIDEO_LOADTEST_STEP_COOLDOWN=0s",
	)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-video-loadtest failed: %v\n%s", err, raw)
	}
	planRaw, err := os.ReadFile(filepath.Join(outDir, "video", "step-100", "shards.tsv"))
	if err != nil {
		t.Fatalf("read proportional shard plan: %v", err)
	}
	plan := string(planRaw)
	if !strings.Contains(plan, "lg01\t192.0.2.1\t60") || !strings.Contains(plan, "lg02\t192.0.2.2\t40") {
		t.Fatalf("proportional shard plan did not split 60/40:\n%s", plan)
	}
	ids1, err := os.ReadFile(filepath.Join(outDir, "video", "step-100", "shard-01", "device-ids.txt"))
	if err != nil {
		t.Fatalf("read shard-01 IDs: %v", err)
	}
	if !strings.Contains(string(ids1), "device-001") || strings.Contains(string(ids1), "device-061") {
		t.Fatalf("shard-01 IDs are not limited to lg01 inventory:\n%s", ids1)
	}
	sshRaw, err := os.ReadFile(sshLog)
	if err != nil {
		t.Fatalf("read ssh log: %v", err)
	}
	sshBody := string(sshRaw)
	if !strings.Contains(sshBody, "--virtual-devices 60 --virtual-viewers 60") ||
		!strings.Contains(sshBody, "--virtual-devices 40 --virtual-viewers 40") {
		t.Fatalf("remote commands missing proportional virtual counts:\n%s", sshBody)
	}
}

func TestHome100KScriptVideoLoadtestStopsWhenTokenGenerationFails(t *testing.T) {
	outDir := t.TempDir()
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(outDir, "go.log")
	goStub := filepath.Join(binDir, "go")
	goStubBody := `#!/usr/bin/env bash
printf '%s\n' "$*" >> ` + shellQuoteForTest(goLog) + `
exit 1
`
	if err := os.WriteFile(goStub, []byte(goStubBody), 0o755); err != nil {
		t.Fatal(err)
	}
	videoLog := filepath.Join(outDir, "video.log")
	videoStub := filepath.Join(outDir, "video-stub.sh")
	videoStubBody := `#!/usr/bin/env bash
printf 'unexpected video runner call\n' >> ` + shellQuoteForTest(videoLog) + `
`
	if err := os.WriteFile(videoStub, []byte(videoStubBody), 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "run-video-loadtest")
	cmd.Env = home100KTestEnv(
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME100K_ENV_ROOT="+writeTinyEnvRoot(t),
		"HOME100K_REGION=us-sea",
		"HOME100K_RUN_ID=test-video-token-fail",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
		"HOME100K_SCENARIO_PROFILE=video-50k-turn-v1",
		"HOME100K_VIDEO_LOADTEST=on",
		"HOME100K_VIDEO_LOADTEST_SCRIPT="+videoStub,
		"HOME100K_VIDEO_LOADTEST_ARTIFACT_DIR="+filepath.Join(outDir, "video"),
		"HOME100K_VIDEO_LOADTEST_LADDER=100,500",
		"HOME100K_VIDEO_LOADTEST_STEP_COOLDOWN=0s",
		"HOME100K_VIDEO_LOADTEST_TOKEN_REQUEST_TIMEOUT=45s",
		"HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL=https://device.video.example.test",
	)
	raw, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("run-video-loadtest unexpectedly passed\n%s", raw)
	}
	if !strings.Contains(string(raw), "video loadtest token generation failed") {
		t.Fatalf("output missing token generation failure:\n%s", raw)
	}
	if _, err := os.Stat(videoLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("video runner should not be called after token failure, stat err=%v", err)
	}
	goRaw, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatalf("read go log: %v", err)
	}
	if !strings.Contains(string(goRaw), "video-loadtest-tokens") {
		t.Fatalf("token generator was not invoked:\n%s", goRaw)
	}
	if !strings.Contains(string(goRaw), "--request-timeout 45s") {
		t.Fatalf("token generator did not receive request timeout:\n%s", goRaw)
	}
	if !strings.Contains(string(goRaw), "--base-url https://device.video.example.test") {
		t.Fatalf("token generator did not receive explicit token base URL:\n%s", goRaw)
	}
}

func TestHome100KScriptUsesAbsoluteEnvRootKubeconfigForServerEvidence(t *testing.T) {
	outDir := t.TempDir()
	envRoot := filepath.Join(outDir, "env-root")
	kubeconfig := filepath.Join(envRoot, "state", "kubeconfig.yaml")
	if err := os.MkdirAll(filepath.Dir(kubeconfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kubeconfig, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(outDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(outDir, "go.log")
	goStub := filepath.Join(binDir, "go")
	goBody := `#!/usr/bin/env bash
printf 'KUBECONFIG=%s\nARGS=%s\n' "${KUBECONFIG:-}" "$*" >> ` + shellQuoteForTest(goLog) + `
`
	if err := os.WriteFile(goStub, []byte(goBody), 0o755); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "scripts", "home-100k.sh")
	cmd := exec.Command("bash", script, "collect-server-evidence", "--live")
	cmd.Env = home100KTestEnv(
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"HOME100K_ENV_ROOT="+envRoot,
		"HOME100K_REGION=us-sea",
		"HOME100K_RUN_ID=test-absolute-env-root",
		"HOME100K_OUT_DIR="+filepath.Join(outDir, "report"),
	)
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("collect-server-evidence failed: %v\n%s", err, raw)
	}
	logRaw, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logRaw)
	if !strings.Contains(log, "KUBECONFIG="+kubeconfig) {
		t.Fatalf("script did not export absolute env-root kubeconfig:\n%s", log)
	}
	if !strings.Contains(log, "collect-server-evidence") || !strings.Contains(log, "--live") {
		t.Fatalf("script did not invoke collect-server-evidence live:\n%s", log)
	}
}

func TestHome100KScriptDocumentsCloudLoggerEvidenceOverrides(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "home-100k.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		"HOME100K_CLOUD_LOGGER_ENV",
		"HOME100K_CLOUD_LOGGER_ENDPOINT",
		"HOME100K_CLOUD_LOGGER_INGEST_TOKEN",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("home-100k.sh missing logger override marker %q:\n%s", want, body)
		}
	}
}

func home100KTestEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra)+1)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "HOME100K_") {
			continue
		}
		env = append(env, item)
	}
	hasRegionOverride := false
	for _, item := range extra {
		if strings.HasPrefix(item, "HOME100K_REGION=") {
			hasRegionOverride = true
			break
		}
	}
	if !hasRegionOverride {
		env = append(env, "HOME100K_REGION=us-sea")
	}
	return append(env, extra...)
}

func shellQuoteForTest(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func TestLiveRunnerCommandTimeoutUsesScaleAwareGrace(t *testing.T) {
	timeout, err := liveRunnerCommandTimeout(3, "")
	if err != nil {
		t.Fatal(err)
	}
	if timeout != 10*time.Minute+3*time.Second {
		t.Fatalf("timeout = %s, want 10m3s", timeout)
	}

	timeout, err = liveRunnerCommandTimeout(3600, "")
	if err != nil {
		t.Fatal(err)
	}
	if timeout != 75*time.Minute {
		t.Fatalf("timeout = %s, want 75m", timeout)
	}

	timeout, err = liveRunnerCommandTimeout(60, "2m")
	if err != nil {
		t.Fatal(err)
	}
	if timeout != 3*time.Minute {
		t.Fatalf("timeout = %s, want 3m", timeout)
	}

	if _, err := liveRunnerCommandTimeout(60, "bad"); err == nil {
		t.Fatalf("expected invalid timeout grace to fail")
	}
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
		`if [ -f "{{ remote_env_root }}/env/stack.env" ]; then`,
		"generator_hosts_override_ip | default('')",
		"video_cloud_public_url | default('')",
		"video_cloud_token_url | default('')",
		"account_manager_url | default('')",
		"runtime-coverage-server-ca.crt",
		`--devices "{{ device_count }}"`,
		`--users "{{ user_count }}"`,
		`--devices-per-user "{{ devices_per_user }}"`,
		`--load-generator-devices-per-vm "{{ load_generator_devices_per_vm }}"`,
		`--scenario-profile "{{ scenario_profile }}"`,
		`--vm-label-prefix "{{ vm_label_prefix | default('lg') }}"`,
		`--mqtt-concurrency "{{ mqtt_concurrency | default(1000) }}"`,
		`--runtime-logs="{{ runtime_logs | default(true) | string | lower }}"`,
		`runner_nofile_limit="{{ runner_nofile_limit | default(1048576) }}"`,
		`ulimit -n "$runner_nofile_limit"`,
		"runner-ready-response.json",
		"READY_WAIT",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("start-runner.yml missing %q:\n%s", want, body)
		}
	}
}

func TestRunnerDaemonAcceptsRuntimeLogsFlag(t *testing.T) {
	var stderr bytes.Buffer
	opts, values, err := parseRunnerDaemonFlags("home-100k runner-daemon", []string{
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--scenario-profile", ClipStorageCanaryScenarioProfile,
		"--run-id", "run-cli",
		"--role", "mixed",
		"--shard-index", "0",
		"--runtime-logs=false",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseRunnerDaemonFlags error: %v stderr=%s", err, stderr.String())
	}
	if values.runtimeLogs {
		t.Fatalf("runtimeLogs = true, want false")
	}
	if opts.ScenarioProfile != ClipStorageCanaryScenarioProfile {
		t.Fatalf("scenario profile = %q, want %q", opts.ScenarioProfile, ClipStorageCanaryScenarioProfile)
	}
}

func TestRunnerDaemonAcceptsDeviceTokenRequestFlags(t *testing.T) {
	var stderr bytes.Buffer
	opts, values, err := parseRunnerDaemonFlags("home-100k runner-daemon", []string{
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--run-id", "run-cli",
		"--role", "mixed",
		"--shard-index", "0",
		"--device-token-request-timeout", "12s",
		"--device-token-request-retries", "2",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseRunnerDaemonFlags error: %v stderr=%s", err, stderr.String())
	}
	if opts.DeviceTokenRequestTimeout != "12s" {
		t.Fatalf("PlanOptions.DeviceTokenRequestTimeout = %q, want 12s", opts.DeviceTokenRequestTimeout)
	}
	if opts.DeviceTokenRequestRetries != 2 {
		t.Fatalf("PlanOptions.DeviceTokenRequestRetries = %d, want 2", opts.DeviceTokenRequestRetries)
	}
	if values.deviceTokenRequestTimeout != "12s" {
		t.Fatalf("values.deviceTokenRequestTimeout = %q, want 12s", values.deviceTokenRequestTimeout)
	}
	if values.deviceTokenRequestRetries != 2 {
		t.Fatalf("values.deviceTokenRequestRetries = %d, want 2", values.deviceTokenRequestRetries)
	}
}

func TestExecuteRunStagesProducesStageMetrics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"run-stages",
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--video-generator-vm-count", "1",
		"--run-id", "run-cli",
		"--out-dir", outDir,
		"--live",
		"--runner-mode", "live",
		"--vm-state-file", stateFile,
		"--remote-workspace", "/root/rtk_cloud_workspace",
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/runtime",
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
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/runtime",
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
		"ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json"),
		"ansible/start-runner.yml",
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
	if strings.Contains(stdout.String(), `"id": 201`) {
		t.Fatalf("stdout included video-only VM in home shard dispatch:\n%s", stdout.String())
	}
}

func TestExecuteRunStagesLiveRetriesStartRunnerPlaybook(t *testing.T) {
	outDir := t.TempDir()
	envRoot := writeTinyEnvRoot(t)
	writeHome100KCoverageArtifacts(t, envRoot)
	stateFile := filepath.Join(outDir, "vms.json")
	body, err := json.Marshal(map[string]any{
		"created": []LinodeVM{{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	oldRunner := commandRunner
	oldCoordinator := runHostCoordinator
	oldDelay := ansibleRetryDelay
	defer func() {
		commandRunner = oldRunner
		runHostCoordinator = oldCoordinator
		ansibleRetryDelay = oldDelay
	}()
	ansibleRetryDelay = 0

	startAttempts := 0
	commandRunner = func(name string, args ...string) error {
		joined := name + " " + strings.Join(args, " ")
		if strings.Contains(joined, "ansible/start-runner.yml") {
			startAttempts++
			if startAttempts == 1 {
				return errors.New("transient ssh close")
			}
		}
		return nil
	}
	coordinatorCalled := false
	runHostCoordinator = func(vms []LinodeVM, plan Plan, runID string, values workflowFlagValues) (StartCoordination, error) {
		coordinatorCalled = true
		return StartCoordination{Mode: "host-coordinator", ReadyBarrier: "1/1"}, nil
	}

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
		"--remote-env-root", "/root/rtk_cloud_workspace/cloud_env/staging/runtime",
		"--remote-out-root", "/var/lib/home-100k",
		"--ssh-user", "root",
		"--ssh-key", "/tmp/test-key",
		"--mqtt-addr", "mqtt-public.example.test:8883",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(run-stages live) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if startAttempts != 2 {
		t.Fatalf("start-runner attempts = %d, want 2", startAttempts)
	}
	if !coordinatorCalled {
		t.Fatalf("host coordinator was not called after start-runner retry")
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

func TestProbeResultKeepsParseableEvidenceAfterNonZeroExit(t *testing.T) {
	probe := serverEvidenceProbe{
		source: "emqx_listener_stats",
		detail: "listener stats captured",
	}
	out := `tcp:default acceptors 128
ssl:default current_conn 12500
emqx.pod_mqtt_0.ssl_default.current_conn 12500
`
	source, note := evidenceSourceFromProbeResult(probe, "run-fixed", out, errors.New("exit status 1"))
	if !source.Available {
		t.Fatalf("source should stay available when counters are parseable: %+v", source)
	}
	if source.Counters["emqx.broker.identity"] != 1 {
		t.Fatalf("missing broker identity counter: %#v", source.Counters)
	}
	if source.Counters["emqx.ssl_default.current_conn"] != 12500 {
		t.Fatalf("ssl current_conn = %d, want 12500", source.Counters["emqx.ssl_default.current_conn"])
	}
	if note == "" || !strings.Contains(note, "evidence probe failed") {
		t.Fatalf("note = %q, want warning note", note)
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
		"video_cloud_api": {Available: true, Counters: map[string]int64{
			"video_cloud_api.k8s.running_pods":                    7,
			"video_cloud_api.k8s.desired_replicas":                7,
			"video_cloud_api.request_token.status_200":            9723,
			"video_cloud_api.metrics.request_token_count":         140,
			"video_cloud_api.webrtc_signaling_store.enabled_pods": 7,
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
		"video_cloud_api": {Available: true, Counters: map[string]int64{
			"video_cloud_api.k8s.running_pods":                    7,
			"video_cloud_api.k8s.desired_replicas":                7,
			"video_cloud_api.request_token.status_200":            1200,
			"video_cloud_api.metrics.request_token_count":         100,
			"video_cloud_api.webrtc_signaling_store.enabled_pods": 7,
		}},
	}}

	applySourceCounterBaselineDelta(&evidence, baseline, "ingress_nginx")
	applySourceCounterBaselineDelta(&evidence, baseline, "postgres")
	applySourceCounterBaselineDelta(&evidence, baseline, "video_cloud_api")

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
	videoAPI := evidence.Sources["video_cloud_api"].Counters
	if got := videoAPI["video_cloud_api.k8s.running_pods"]; got != 7 {
		t.Fatalf("video api running pods = %d, want raw gauge 7", got)
	}
	if got := videoAPI["video_cloud_api.k8s.desired_replicas"]; got != 7 {
		t.Fatalf("video api desired replicas = %d, want raw gauge 7", got)
	}
	if got := videoAPI["video_cloud_api.webrtc_signaling_store.enabled_pods"]; got != 7 {
		t.Fatalf("signaling store pods = %d, want raw gauge 7", got)
	}
	if got := videoAPI["video_cloud_api.request_token.status_200"]; got != 8523 {
		t.Fatalf("video api request token delta = %d, want 8523", got)
	}
	if got := videoAPI["video_cloud_api.metrics.request_token_count"]; got != 40 {
		t.Fatalf("video api metrics delta = %d, want 40", got)
	}
}

func TestNormalizeEvidenceSourceCatalogMetadataPreservesOptionalSources(t *testing.T) {
	sources := requiredEvidenceSources(true)
	sources["edge_haproxy"] = EvidenceSource{Available: false, Detail: "exit status 1"}

	normalizeEvidenceSourceCatalogMetadata(sources)

	if !sources["edge_haproxy"].Optional {
		t.Fatalf("edge_haproxy optional = false, want true")
	}
	if !allEvidenceSourcesAvailable(sources) {
		t.Fatalf("optional edge_haproxy should not make required evidence incomplete")
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

func TestKubernetesRuntimeLoggerEvidenceUsesNormalizedKubeconfig(t *testing.T) {
	envRoot := t.TempDir()
	stateDir := filepath.Join(envRoot, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if usesKubernetesRuntimeLoggerEvidence(envRoot) {
		t.Fatal("runtime logger evidence enabled without normalized kubeconfig")
	}
	if err := os.WriteFile(filepath.Join(stateDir, "kubeconfig.yaml"), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !usesKubernetesRuntimeLoggerEvidence(envRoot) {
		t.Fatal("runtime logger evidence not enabled by normalized kubeconfig")
	}
}

func TestExplicitCentralLoggerEndpointOverridesKubernetesLokiAdapter(t *testing.T) {
	logger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			t.Fatalf("logger path = %q, want /v1/logs", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer runtime-token" {
			t.Fatalf("logger authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"events":[{"event_id":"runtime-event"}]}`)
	}))
	defer logger.Close()

	envRoot := t.TempDir()
	stateDir := filepath.Join(envRoot, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "kubeconfig.yaml"), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME100K_CLOUD_LOGGER_ENDPOINT", logger.URL)
	t.Setenv("HOME100K_CLOUD_LOGGER_INGEST_TOKEN", "runtime-token")
	if !hasExplicitCentralLoggerEndpoint() {
		t.Fatal("explicit central logger endpoint was not detected")
	}
	events, err := queryCentralLoggerRuntimeEvidenceEvents(
		envRoot,
		"runtime-run",
		time.Now().Add(-time.Minute),
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventID != "runtime-event" {
		t.Fatalf("central logger events = %#v", events)
	}
	t.Setenv("HOME100K_CLOUD_LOGGER_ENDPOINT", "")
	t.Setenv("CLOUD_LOGGER_ENDPOINT", "")
	if hasExplicitCentralLoggerEndpoint() {
		t.Fatal("central logger endpoint reported explicit without an environment override")
	}
}

func TestCentralLoggerRuntimeCountersRequireStructuredRuntimeLogFields(t *testing.T) {
	events := []centralLoggerRuntimeEvent{
		{
			EventID:   "valid",
			Message:   "mqtt_e2e shadow_desired app_controller publish",
			Source:    "device-runtime",
			Component: "device_runtime_log",
			Fields: map[string]any{
				"devid": "device-1", "stream_id": "mqtt-e2e-run-device-1", "seq": float64(1), "source": "app_controller",
			},
		},
		{
			EventID: "legacy-text-only", Message: "mqtt_e2e shadow_desired app_controller publish", Source: "device-runtime", Component: "device_runtime_log",
		},
	}
	_, streams := centralLoggerRuntimeCounters("run", events)
	if streams["runtime_log_schema.valid"] != 1 || streams["runtime_log_schema.invalid"] != 1 {
		t.Fatalf("schema counters = %#v", streams)
	}
	if streams["runtime_log_stream.mqtt-e2e-run-device-1.device.device-1.entries"] != 1 {
		t.Fatalf("missing structured device evidence: %#v", streams)
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
		"--env-root", "cloud_env/staging/runtime",
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
	var capturedTimeout time.Duration
	commandRunnerWithTimeout = func(timeout time.Duration, name string, args ...string) error {
		capturedTimeout = timeout
		return commandRunner(name, args...)
	}
	defer func() {
		commandRunner = oldRunner
		commandRunnerWithTimeout = oldTimeoutRunner
	}()

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"shard-run",
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root cloud_env/staging/runtime",
		"--brandname RTK",
		"--duration-seconds 3",
		"--mqtt-probe --run-id run-cli",
		"--telemetry-interval off",
		"--command-rate-per-device-per-day 1800.00",
		"--stage-names target",
		"--stage-connected-devices 20000",
		"--stage-durations-seconds 3",
		"--stage-min-commands 1000",
		"--device-traffic-profile home-diverse-v1",
		"--concurrency 1000",
		"--command-concurrency 100",
		"--shadow-command-timeout 30s",
		"--runtime-logs=true",
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
	if capturedTimeout != 10*time.Minute+3*time.Second {
		t.Fatalf("live shard timeout = %s, want 10m3s", capturedTimeout)
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
		"--env-root", "cloud_env/staging/runtime",
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

func TestLoadLiveMQTTShardResultsRejectsActorProbeFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.json")
	if err := writeJSONFile(path, map[string]any{
		"overall": "pass",
		"load": map[string]any{
			"load_model": "actor-separated-probe",
		},
		"connect_attempts":   1000,
		"connect_successes":  1000,
		"active_connections": 1000,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := loadLiveMQTTShardResults(path, []Stage{{Name: "target"}}, []string{"1000"})
	if err == nil || !strings.Contains(err.Error(), `load_model = "actor-separated-probe"`) {
		t.Fatalf("loadLiveMQTTShardResults error = %v, want actor probe rejection", err)
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
		"--env-root", "cloud_env/staging/runtime",
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
					"name":                "target",
					"status":              "PASS",
					"connect_attempts":    2500,
					"connect_successes":   2000,
					"subscribe_successes": 2000,
					"publish_successes":   1000,
					"messages_received":   900,
					"http_requests":       250,
					"http_successes":      240,
					"failure_reasons":     map[string]any{"app_token_request_failed": 10},
					"device_mqtt_totals": map[string]any{
						"bytes_sent":                  12345,
						"token_first_attempt_success": 2400,
						"token_first_attempt_fail":    100,
						"token_retry_attempts":        100,
						"token_retry_success":         75,
						"token_retry_exhausted":       25,
					},
					"app_user_totals": map[string]any{"bytes_received": 67890},
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
		"--env-root", "cloud_env/staging/runtime",
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
	if result.Status != "failed" || result.Partial || !strings.Contains(result.Error, "mqtt test failed") {
		t.Fatalf("partial failure metadata not preserved: %#v stderr=%s", result, stderr.String())
	}
	if len(result.StageResults) != 1 {
		t.Fatalf("stage results len = %d, want 1", len(result.StageResults))
	}
	if result.StageResults[0].DeviceMQTTTotals.ConnectAttempts != 2500 ||
		result.StageResults[0].DeviceMQTTTotals.BytesSent != 12345 ||
		result.StageResults[0].DeviceMQTTTotals.TokenFirstAttemptSuccess != 2400 ||
		result.StageResults[0].DeviceMQTTTotals.TokenFirstAttemptFail != 100 ||
		result.StageResults[0].DeviceMQTTTotals.TokenRetryAttempts != 100 ||
		result.StageResults[0].DeviceMQTTTotals.TokenRetrySuccess != 75 ||
		result.StageResults[0].DeviceMQTTTotals.TokenRetryExhausted != 25 {
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
			{ID: 201, Label: "vg01", PublicIPv4: "203.0.113.201"},
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
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--video-generator-vm-count", "1",
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
		"ansible-playbook --forks 20 -i " + filepath.Join(outDir, "ansible", "inventory.json"),
		"ansible/collect.yml",
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
	if strings.Contains(stdout.String(), `"id": 201`) {
		t.Fatalf("stdout included video-only VM in home shard collect:\n%s", stdout.String())
	}
}

func TestExecuteCollectLiveRetriesCollectPlaybook(t *testing.T) {
	outDir := t.TempDir()
	envRoot := writeTinyEnvRoot(t)
	writeHome100KCoverageArtifacts(t, envRoot)
	stateFile := filepath.Join(outDir, "vms.json")
	body, err := json.Marshal(map[string]any{
		"created": []LinodeVM{{ID: 101, Label: "lg01", PublicIPv4: "203.0.113.101"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, body, 0o600); err != nil {
		t.Fatal(err)
	}

	oldRunner := commandRunner
	oldDelay := ansibleRetryDelay
	defer func() {
		commandRunner = oldRunner
		ansibleRetryDelay = oldDelay
	}()
	ansibleRetryDelay = 0

	collectAttempts := 0
	commandRunner = func(name string, args ...string) error {
		joined := name + " " + strings.Join(args, " ")
		if strings.Contains(joined, "ansible/collect.yml") {
			collectAttempts++
			if collectAttempts == 1 {
				return errors.New("transient ssh close")
			}
		}
		return nil
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect",
		"--env-root", envRoot,
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
	if collectAttempts != 2 {
		t.Fatalf("collect playbook attempts = %d, want 2", collectAttempts)
	}
}

func TestExecuteCollectServerEvidenceDefaultsToIncompleteSourcePlan(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"collect-server-evidence",
		"--env-root", "cloud_env/staging/runtime",
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
		`"edge_haproxy"`,
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

func TestCollectCentralLoggerEvidenceQueriesStructuredRunID(t *testing.T) {
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer logger-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		seen = append(seen, r.URL.RawQuery)
		events := []map[string]any{{"event_id": "evt-1", "fields": map[string]any{"run_id": "other-run"}}}
		if r.URL.Query().Get("operation_id") == "home-mqtt-loadtest" {
			events = append(events, map[string]any{"event_id": "evt-2", "fields": map[string]any{"run_id": "run-logger"}})
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
	if source.Counters["central_logger.run_summary.events"] != 1 {
		t.Fatalf("unexpected counters: %+v", source.Counters)
	}
	joined := strings.Join(seen, "\n")
	for _, want := range []string{"operation_id=home-mqtt-loadtest", "limit=1000"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("central logger query missing %q in:\n%s", want, joined)
		}
	}
}

func TestCollectLokiWebRTCTraceEvidenceCountsLifecycleEvents(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Fatalf("unexpected Loki path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.Query().Get("query"), "run-loki") {
			t.Fatalf("Loki query missing run_id: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{{
					"values": [][]string{
						{"1780000000000000000", `{"run_id":"run-loki","event":"create_started","session_id":"s1","duration_ms":10}`},
						{"1780000001000000000", `{"run_id":"run-loki","event":"answer_succeeded","session_id":"s1","duration_ms":25}`},
						{"1780000001500000000", `{"run_id":"run-loki","event":"ice_servers_resolved","session_id":"s1","duration_ms":3,"ice_server_count":2,"static_stun_count":1,"static_turn_count":1,"dynamic_turn_count":0,"turn_registry_node_count":0}`},
						{"1780000001600000000", `{"run_id":"run-loki","event":"turn_registry_lookup_empty","session_id":"s1","duration_ms":2,"turn_registry_node_count":0,"turn_registry_query_limit":0}`},
						{"1780000002000000000", `{"run_id":"run-other","event":"create_started","session_id":"s2"}`},
					},
				}},
			},
		})
	}))
	defer server.Close()
	t.Setenv("HOME100K_LOKI_URL", server.URL)

	source, note := collectLokiWebRTCTraceEvidence(t.TempDir(), "run-loki", "2026-07-07T00:00:00Z")

	if note != "" {
		t.Fatalf("unexpected note: %s", note)
	}
	if !source.Available || !source.Optional {
		t.Fatalf("loki source flags = %+v, want available optional", source)
	}
	if source.Counters["loki_webrtc_trace.create_started.events"] != 1 ||
		source.Counters["loki_webrtc_trace.answer_succeeded.events"] != 1 ||
		source.Counters["loki_webrtc_trace.request_failed.events"] != 0 ||
		source.Counters["loki_webrtc_trace.create_started.duration_p95_ms"] != 10 ||
		source.Counters["loki_webrtc_trace.answer_succeeded.duration_max_ms"] != 25 ||
		source.Counters["loki_webrtc_trace.answer_succeeded.unique_sessions"] != 1 ||
		source.Counters["loki_webrtc_trace.ice_servers_resolved.ice_server_count_max"] != 2 ||
		source.Counters["loki_webrtc_trace.ice_servers_resolved.static_turn_count_max"] != 1 ||
		source.Counters["loki_webrtc_trace.turn_registry_lookup_empty.turn_registry_node_count_max"] != 0 {
		t.Fatalf("unexpected Loki counters: %+v", source.Counters)
	}
	if len(requests) != 1 {
		t.Fatalf("Loki requests = %d, want 1", len(requests))
	}
	if got := requestLimit(t, requests[0]); got != "50000" {
		t.Fatalf("Loki first query limit = %s, want 50000", got)
	}
}

func requestLimit(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse request URL %q: %v", raw, err)
	}
	return parsed.Query().Get("limit")
}

func TestCollectLokiWebRTCTraceEvidenceRetriesWithSmallerLimit(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		if r.URL.Query().Get("limit") != "250" {
			http.Error(w, "query too large", http.StatusGatewayTimeout)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{{
					"values": [][]string{
						{"1780000000000000000", `{"run_id":"run-loki-retry","event":"create_started","session_id":"s1"}`},
						{"1780000001000000000", `{"run_id":"run-loki-retry","event":"offer_delivered","session_id":"s1"}`},
					},
				}},
			},
		})
	}))
	defer server.Close()
	t.Setenv("HOME100K_LOKI_URL", server.URL)

	source, note := collectLokiWebRTCTraceEvidence(t.TempDir(), "run-loki-retry", "2026-07-07T00:00:00Z")

	if note != "" {
		t.Fatalf("unexpected note: %s", note)
	}
	if !source.Available {
		t.Fatalf("loki source = %+v, want available after smaller retry", source)
	}
	if source.Counters["loki_webrtc_trace.create_started.events"] != 1 ||
		source.Counters["loki_webrtc_trace.offer_delivered.events"] != 1 {
		t.Fatalf("unexpected Loki counters: %+v", source.Counters)
	}
	if len(requests) < 2 {
		t.Fatalf("Loki requests = %d, want retry with smaller limit", len(requests))
	}
}

func TestCollectLokiWebRTCTraceEvidenceBackfillsMissingLifecycleEvents(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		query := r.URL.Query().Get("query")
		values := make([][]string, 0, 1002)
		switch {
		case strings.Contains(query, "create_started"):
			values = append(values, []string{"1780000000000000000", `{"run_id":"run-loki-backfill","event":"create_started","session_id":"s1","duration_ms":8}`})
		case strings.Contains(query, "answer_succeeded"):
			values = append(values, []string{"1780000001000000000", `{"run_id":"run-loki-backfill","event":"answer_succeeded","session_id":"s1","duration_ms":17}`})
		case strings.Contains(query, "offer_delivered"):
			values = append(values, []string{"1780000002000000000", `{"run_id":"run-loki-backfill","event":"offer_delivered","session_id":"s1","duration_ms":4}`})
		case strings.Contains(query, "session_created"):
			values = append(values, []string{"1780000003000000000", `{"run_id":"run-loki-backfill","event":"session_created","session_id":"s1","duration_ms":2}`})
		default:
			for i := 0; i < 1000; i++ {
				values = append(values, []string{
					fmt.Sprintf("%019d", 1780000010000000000+i),
					fmt.Sprintf(`{"run_id":"run-loki-backfill","event":"close_succeeded","session_id":"s%d","duration_ms":1}`, i),
				})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{{
					"values": values,
				}},
			},
		})
	}))
	defer server.Close()
	t.Setenv("HOME100K_LOKI_URL", server.URL)

	source, note := collectLokiWebRTCTraceEvidence(t.TempDir(), "run-loki-backfill", "2026-07-07T00:00:00Z")

	if note != "" {
		t.Fatalf("unexpected note: %s", note)
	}
	if !source.Available {
		t.Fatalf("loki source = %+v, want available", source)
	}
	if source.Counters["loki_webrtc_trace.close_succeeded.events"] != 1000 ||
		source.Counters["loki_webrtc_trace.create_started.events"] != 1 ||
		source.Counters["loki_webrtc_trace.session_created.events"] != 1 ||
		source.Counters["loki_webrtc_trace.offer_delivered.events"] != 1 ||
		source.Counters["loki_webrtc_trace.answer_succeeded.events"] != 1 {
		t.Fatalf("unexpected Loki counters after backfill: %+v", source.Counters)
	}
	if len(requests) <= 1 {
		t.Fatalf("Loki requests = %d, want event backfill queries", len(requests))
	}
}

func TestCollectLokiWebRTCTraceEvidenceBackfillsImbalancedLifecycleEvents(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		query := r.URL.Query().Get("query")
		values := make([][]string, 0, 1000)
		eventCounts := map[string]int{
			"create_started":                 500,
			"session_created":                500,
			"offer_delivered":                500,
			"answer_wait_store_completed":    500,
			"answer_succeeded":               500,
			"close_succeeded":                500,
			"turn_registry_lookup_succeeded": 500,
		}
		backfillEvent := ""
		for event := range eventCounts {
			if strings.Contains(query, event) {
				backfillEvent = event
				break
			}
		}
		if backfillEvent != "" {
			for event, count := range eventCounts {
				if event != backfillEvent {
					continue
				}
				for i := 0; i < count; i++ {
					values = append(values, []string{
						fmt.Sprintf("%019d", 1780000020000000000+i),
						fmt.Sprintf(`{"run_id":"run-loki-imbalanced","event":"%s","session_id":"s%d","duration_ms":1}`, event, i),
					})
				}
				break
			}
		} else if strings.Contains(query, "event") {
			values = nil
		} else {
			// Simulate Loki returning only a tail slice: every critical event is
			// present, but counts are obviously too imbalanced to be complete.
			for i := 0; i < 700; i++ {
				values = append(values, []string{
					fmt.Sprintf("%019d", 1780000030000000000+i),
					fmt.Sprintf(`{"run_id":"run-loki-imbalanced","event":"close_succeeded","session_id":"s%d","duration_ms":1}`, i),
				})
			}
			for _, event := range []string{"create_started", "session_created", "offer_delivered", "answer_wait_store_completed", "answer_succeeded"} {
				for i := 0; i < 60; i++ {
					values = append(values, []string{
						fmt.Sprintf("%019d", 1780000040000000000+i),
						fmt.Sprintf(`{"run_id":"run-loki-imbalanced","event":"%s","session_id":"s%d","duration_ms":1}`, event, i),
					})
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []map[string]any{{
					"values": values,
				}},
			},
		})
	}))
	defer server.Close()
	t.Setenv("HOME100K_LOKI_URL", server.URL)

	source, note := collectLokiWebRTCTraceEvidence(t.TempDir(), "run-loki-imbalanced", "2026-07-07T00:00:00Z")

	if note != "" {
		t.Fatalf("unexpected note: %s", note)
	}
	if !source.Available {
		t.Fatalf("loki source = %+v, want available", source)
	}
	for _, event := range []string{"create_started", "session_created", "offer_delivered", "answer_wait_store_completed", "answer_succeeded", "close_succeeded"} {
		if got := source.Counters["loki_webrtc_trace."+event+".events"]; got != 500 {
			t.Fatalf("%s events = %d, want 500; counters=%+v", event, got, source.Counters)
		}
	}
	if len(requests) <= 1 {
		t.Fatalf("Loki requests = %d, want event backfill queries", len(requests))
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
					"device_id": "device-000001",
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
					"device_id": "device-000001",
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
					"device_id": "device-000001",
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
					"device_id": "device-000001",
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
			t.Fatalf("collectLiveServerEvidence queried legacy device_shadows table: %s %s", name, joined)
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

func TestCentralLoggerRuntimeQueryStopsAtWindowBudget(t *testing.T) {
	t.Setenv("HOME100K_CENTRAL_LOGGER_RUNTIME_QUERY_MAX_WINDOWS", "3")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "too wide", http.StatusBadGateway)
	}))
	defer server.Close()

	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "services", "cloud-logger"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "services", "cloud-logger", "logger.env"), []byte("CLOUD_LOGGER_ENDPOINT="+server.URL+"\nCLOUD_LOGGER_INGEST_TOKEN=logger-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, note := collectCentralLoggerRuntimeLogEvidence(envRoot, "run-budget", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	if !strings.Contains(note, "central_logger runtime evidence probe failed") {
		t.Fatalf("note = %q, want runtime evidence failure", note)
	}
	if requests > 3 {
		t.Fatalf("runtime logger requests = %d, want <= 3", requests)
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

func TestCentralLoggerEnvValuesDerivesLKEEndpointFromStackEnv(t *testing.T) {
	envRoot := t.TempDir()
	secretDir := filepath.Join(envRoot, "state", "secrets")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte("CLOUD_PROVIDER=lke\nCLOUD_LOGGER_DOMAIN=logger.video-cloud-staging.realtekconnect.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "cloud-logger-ingest-token"), []byte("current-lke-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	values := centralLoggerEnvValues(envRoot)
	if values["CLOUD_LOGGER_ENDPOINT"] != "https://logger.video-cloud-staging.realtekconnect.com" || values["CLOUD_LOGGER_INGEST_TOKEN"] != "current-lke-token" {
		t.Fatalf("centralLoggerEnvValues() = %#v, want endpoint derived from LKE stack env and token from LKE secret", values)
	}
}

func TestCentralLoggerEndpointAndTokenPrefersExplicitEnv(t *testing.T) {
	t.Setenv("HOME100K_CLOUD_LOGGER_ENDPOINT", "http://127.0.0.1:18090/")
	t.Setenv("HOME100K_CLOUD_LOGGER_INGEST_TOKEN", "override-token")
	t.Setenv("CLOUD_LOGGER_ENDPOINT", "https://public-env.example")
	t.Setenv("CLOUD_LOGGER_INGEST_TOKEN", "public-env-token")

	endpoint, token := centralLoggerEndpointAndToken(map[string]string{
		"CLOUD_LOGGER_ENDPOINT":     "https://logger.example",
		"CLOUD_LOGGER_INGEST_TOKEN": "file-token",
	})
	if endpoint != "http://127.0.0.1:18090" || token != "override-token" {
		t.Fatalf("centralLoggerEndpointAndToken() endpoint=%q token=%q, want explicit HOME100K env", endpoint, token)
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
		case strings.Contains(joined, "edge-vms.json"):
			return "edge_haproxy.ssh_ok 1\nedge_haproxy.vm_count 1\nedge_haproxy.process.fd_count 128\nedge_haproxy.tcp.established_8883 10000\n", nil
		case strings.Contains(joined, "device_runtime_logs"):
			t.Fatalf("collect-server-evidence queried legacy device_runtime_logs table: %s %s", name, joined)
		case strings.Contains(joined, "device_shadows"):
			t.Fatalf("collect-server-evidence queried legacy device_shadows table: %s %s", name, joined)
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
	if !strings.Contains(out, `"edge_haproxy.tcp.established_8883": 10000`) {
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

func TestLocalWorkflowArtifactPathResolvesRelativePathFromWorkspaceRoot(t *testing.T) {
	workspace, err := localWorkspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("loadtests", "home-100k", "reports", "unit-relative-baseline", "server-evidence-baseline.json")

	got, err := localWorkflowArtifactPath(rel)
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(workspace, rel)
	if got != want {
		t.Fatalf("resolved path = %q, want %q", got, want)
	}
}

func TestServerEvidenceProbesIncludeExternalHAProxyHealth(t *testing.T) {
	probes := serverEvidenceProbes("cloud_env/staging/runtime", "run-edge", "--since=1m")
	for _, probe := range probes {
		if probe.source != "edge_haproxy" {
			continue
		}
		joined := strings.Join(append([]string{probe.command}, probe.args...), " ")
		for _, want := range []string{"edge-vms.json", "ssh_access.key_path", "edge_haproxy.process.fd_count", "edge_haproxy.tcp.established_8883"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("edge_haproxy probe missing %q in:\n%s", want, joined)
			}
		}
		return
	}
	t.Fatal("serverEvidenceProbes() missing edge_haproxy probe")
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
		"--env-root", "cloud_env/staging/runtime",
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
		"--env-root", "cloud_env/staging/runtime",
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
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/runtime", Brandname: "RTK", Region: "us-sea"})
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
		"--env-root", "cloud_env/staging/runtime",
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

func TestExecuteAggregateWritesVideoOnlyRunLevelReport(t *testing.T) {
	outDir := t.TempDir()
	videoDir := filepath.Join(outDir, "video", "shard-01")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
		"config": {"webrtc_ice_policy": "relay", "virtual_viewers": 5, "duration_ms": 30000},
		"webrtc": {
			"success_rate": 1,
			"create": {"operations": 5, "successes": 5},
			"setup": {"operations": 0, "successes": 0},
			"close": {"operations": 5, "successes": 5}
		},
			"webrtc_media": {
				"attempts": 5,
				"successes": 5,
				"ice_connected_p95_ms": 2148,
			"time_to_first_rtp_p95_ms": 2225,
			"h264_packets_received": 29,
			"h264_bytes_received": 17763,
			"video_startup_latency": {
				"samples": 5,
				"h264_access_unit_samples": 5,
				"app_request_to_first_rtp_p95_ms": 2232,
					"app_request_to_first_h264_access_unit_p95_ms": 2232
				}
			},
			"turn_evidence": {
				"registry_available": true,
				"active_nodes": 1,
				"coturn_available": true,
				"api_turn_registry_lookup_succeeded": 1,
				"api_dynamic_turn_count": 1,
				"api_turn_registry_node_count": 1
			},
			"video_startup_latency": [
				{"ice_policy":"relay","selected_local_candidate_type":"relay","selected_remote_candidate_type":"relay","selected_local_candidate_protocol":"udp","selected_remote_candidate_protocol":"udp"}
			]
	}`)
	if err := os.WriteFile(filepath.Join(videoDir, "load-results.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	turnSamples := "time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tcoturn_cpu_pct\tcoturn_rss_kb\trx_bytes\ttx_bytes\tevidence_status\n" +
		"2026-07-07T11:06:07Z\tturn01\t198.51.100.20\t36\t10\t0\t0\t20\t20\t250\t0\t19384\t15591006125\t13681693458\tjournal_active\n"
	if err := os.WriteFile(filepath.Join(outDir, "video", "turn-active-samples.tsv"), []byte(turnSamples), 0o644); err != nil {
		t.Fatal(err)
	}
	serverEvidence := completeServerEvidenceWithWebRTCSignalingStore()
	serverEvidence.Sources["turn_registry"] = EvidenceSource{Available: true, Counters: map[string]int64{"turn_registry.active_nodes": 1}}
	serverEvidence.Sources["coturn"] = EvidenceSource{Available: true, Counters: map[string]int64{"coturn.configured_nodes": 1}}
	if err := writeJSONFile(filepath.Join(outDir, "server-evidence.json"), serverEvidence); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{
		"aggregate",
		"--env-root", "cloud_env/staging/runtime",
		"--brandname", "RTK",
		"--region", "us-sea",
		"--scenario-profile", Video50KTurnScenarioProfile,
		"--run-id", "video-only",
		"--out-dir", outDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Execute(aggregate) code = %d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var result RunResult
	if err := readJSON(filepath.Join(outDir, "results.json"), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "COMPLETE" || result.Result != "SUCCESS" {
		t.Fatalf("video-only outcome = %s/%s, want COMPLETE/SUCCESS; reasons=%v", result.Status, result.Result, result.VideoEvidence.Thresholds.Failures)
	}
	if result.VideoEvidence.WebRTCMedia.Successes != 5 || result.VideoEvidence.TURN.ActiveSessions != 20 {
		t.Fatalf("video-only aggregate evidence = media %d turn sessions %d, want 5/20", result.VideoEvidence.WebRTCMedia.Successes, result.VideoEvidence.TURN.ActiveSessions)
	}
	reportRaw, err := os.ReadFile(filepath.Join(outDir, "TEST_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	report := string(reportRaw)
	for _, want := range []string{
		"Status: COMPLETE",
		"Result: SUCCESS",
		"## WebRTC Totals",
		"App request -> first H.264 access unit",
		"active sessions: 20",
		"journal_active",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("video-only report missing %q:\n%s", want, report)
		}
	}
}

func assertShardManifestRange(t *testing.T, path string, role string, start int, end int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shard manifest %s: %v", path, err)
	}
	var assignment VMAssignment
	if err := json.Unmarshal(raw, &assignment); err != nil {
		t.Fatalf("decode shard manifest %s: %v", path, err)
	}
	for _, shard := range assignment.TaskShards {
		if shard.Role == role && shard.Start == start && shard.End == end {
			return
		}
	}
	t.Fatalf("manifest %s missing %s shard [%d,%d): %#v", path, role, start, end, assignment.TaskShards)
}
