package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMQTTLoadAggregateMergesShardMetrics(t *testing.T) {
	result := aggregateMQTTShardResults([]map[string]any{
		{
			"overall": "pass",
			"metrics": map[string]any{
				"devices_selected":   float64(2),
				"commands_attempted": float64(4),
				"commands_passed":    float64(4),
			},
			"devices": []any{
				map[string]any{"latency_ms": []any{float64(10), float64(20)}},
			},
		},
		{
			"overall": "pass",
			"metrics": map[string]any{
				"devices_selected":   float64(1),
				"commands_attempted": float64(2),
				"commands_passed":    float64(1),
			},
			"devices": []any{
				map[string]any{"latency_ms": []any{float64(30), float64(40)}},
			},
		},
	})
	metrics := result["metrics"].(map[string]any)
	if got := metrics["devices_selected"]; got != 3 {
		t.Fatalf("devices_selected = %v, want 3", got)
	}
	if got := metrics["commands_attempted"]; got != 6 {
		t.Fatalf("commands_attempted = %v, want 6", got)
	}
	if got := metrics["commands_passed"]; got != 5 {
		t.Fatalf("commands_passed = %v, want 5", got)
	}
	if result["overall"] != "fail" {
		t.Fatalf("overall = %v, want fail for success rate below threshold", result["overall"])
	}
	if got := metrics["command_latency_p95_ms"]; got != float64(40) {
		t.Fatalf("p95 = %v, want 40", got)
	}
}

func TestMQTTLoadPreparePlanUsesBaselineDefaults(t *testing.T) {
	workspace := t.TempDir()
	output := captureStdout(t, func() {
		if err := runMQTTLoadTestPrepare([]string{
			"--workspace", workspace,
			"--env-root", "cloud_env/staging",
			"--brandname", "RTK",
			"--plan",
		}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"mqtt-loadtest prepare plan",
		"user_count: 2500",
		"device_count: 10000",
		"device_mix: light=3334,air_conditioner=3333,smart_meter=3333",
		"go run ./scripts/go/rtk-cloud -- create-users",
		"go run ./scripts/go/rtk-cloud -- generate-load-devices",
		"bind-devices uses latest rtk-users-*.json",
		"validate-device-bind uses latest rtk-device-bind-*.json",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("prepare plan missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "go-build") {
		t.Fatalf("prepare plan should not expose temporary go-build executable path:\n%s", output)
	}
}

func TestMQTTLoadRunPlanUsesHostShards(t *testing.T) {
	workspace := t.TempDir()
	hostsFile := filepath.Join(t.TempDir(), "hosts.txt")
	if err := os.WriteFile(hostsFile, []byte("root@load-a\nroot@load-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := captureStdout(t, func() {
		if err := runMQTTLoadTestRun([]string{
			"--workspace", workspace,
			"--env-root", "cloud_env/staging",
			"--brandname", "RTK",
			"--hosts-file", hostsFile,
			"--remote-workspace", "/root/ws",
			"--remote-env-root", "/root/ws/cloud_env/staging/linode",
			"--plan",
		}); err != nil {
			t.Fatal(err)
		}
	})
	for _, want := range []string{
		"mqtt-loadtest run plan",
		"profile: baseline-10k",
		"shards: 2",
		"shard 0 host root@load-a",
		"shard 1 host root@load-b",
		"remote_workspace=/root/ws",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("run plan missing %q:\n%s", want, output)
		}
	}
}

func TestBuildRemoteMQTTShardCommandRunsInsideScriptsGoModule(t *testing.T) {
	command := buildRemoteMQTTShardCommand(remoteMQTTLoadInput{
		Workspace:       "/root/ws",
		EnvRoot:         "/root/ws/cloud_env/staging/linode",
		Brandname:       "RTK",
		Profile:         "baseline-10k",
		RampUp:          "10m",
		Telemetry:       "5m",
		State:           "1h",
		CommandRate:     "1",
		Concurrency:     250,
		MaxConnected:    1000,
		DurationSeconds: 1800,
	}, 1, 2, "/tmp/out/shards/001")
	for _, want := range []string{
		"cd /root/ws/scripts/go && GOWORK=off go run ./rtk-cloud -- mqtt-test",
		"--workspace /root/ws",
		"--shard-index 1",
		"--shard-count 2",
		"--max-connected-devices 1000",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("remote command missing %q:\n%s", want, command)
		}
	}
}

func TestPlanSelfCommandUsesStableGoRunForm(t *testing.T) {
	got := shellQuoteArgs(planSelfCommand("create-users", "--brandname", "RTK"))
	if got != "go run ./scripts/go/rtk-cloud -- create-users --brandname RTK" {
		t.Fatalf("plan command = %q", got)
	}
}

func TestMQTTLoadAggregateCommandReadsShardFiles(t *testing.T) {
	root := t.TempDir()
	for idx, commands := range []int{2, 4} {
		dir := filepath.Join(root, "shards", "00"+string(rune('0'+idx)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{
			"overall": "pass",
			"metrics": map[string]any{
				"devices_selected":   commands,
				"commands_attempted": commands,
				"commands_passed":    commands,
			},
		})
		if err := os.WriteFile(filepath.Join(dir, "results.json"), payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outDir := filepath.Join(root, "aggregate")
	if err := runMQTTLoadTestAggregate([]string{"--input-dir", filepath.Join(root, "shards"), "--out-dir", outDir}); err != nil {
		t.Fatal(err)
	}
	report, err := os.ReadFile(filepath.Join(outDir, "TEST_REPORT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "Devices selected: 6") {
		t.Fatalf("aggregate report missing merged device count:\n%s", report)
	}
}

func TestDurationStringSeconds(t *testing.T) {
	got, err := durationStringSeconds("90s")
	if err != nil {
		t.Fatal(err)
	}
	if got != 90 {
		t.Fatalf("duration seconds = %d, want 90", got)
	}
}

func TestReadHostLinesSkipsBlankAndCommentLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.txt")
	if err := os.WriteFile(path, []byte("\n# comment\nroot@host-a\n\nroot@host-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hosts, err := readHostLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(hosts, ","); got != "root@host-a,root@host-b" {
		t.Fatalf("hosts = %q", got)
	}
}

func TestShellQuoteArgsQuotesRemoteCommandParts(t *testing.T) {
	got := shellQuoteArgs([]string{"cd", "/tmp/work space", "&&", "go", "run", "./scripts/go/rtk-cloud"})
	if !strings.Contains(got, "'/tmp/work space'") {
		t.Fatalf("quoted command missing quoted path: %s", got)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = old }()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, reader); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
