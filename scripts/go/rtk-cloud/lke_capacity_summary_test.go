package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildLKECapacityRunSummaryExtractsCapacityRow(t *testing.T) {
	dir := t.TempDir()
	envRoot := filepath.Join(dir, "envroot")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "LKE_MQTT_REPLICAS=2\nLKE_NODE_COUNT=2\nLKE_NODE_TYPE=g6-standard-2\n")
	runDir := filepath.Join(dir, "reports", "lt1k")
	writeTestFile(t, filepath.Join(runDir, "results.json"), `{
  "run_id":"lt1k",
  "status":"COMPLETE",
  "result":"SUCCESS",
  "report_file":"TEST_REPORT.md",
  "server_evidence_file":"server-evidence.json",
  "plan":{"conditions":{"env_root":"`+envRoot+`","devices":1000,"users":50,"devices_per_user":20,"vm_count":1,"load_generator_devices_per_vm":20000}},
  "load_generator_health":{"saturated":false},
  "server_correlation":{"status":"pass"},
  "runtime_log_correlation":{"status":"pass"},
  "device_mqtt_totals":{"total":{"connect_attempts":1000,"connect_success":1000,"connect_fail":0,"subscribes":1000,"delta_received":50,"reported_publishes":50}},
  "app_user_totals":{"total":{"login_attempts":1,"login_success":1,"login_fail":0,"desired_writes":50,"received_acks":50}},
  "server_evidence":{"complete":true,"sources":{
    "video_cloud_api":{"counters":{"video_cloud_api.request_token.total":1000,"video_cloud_api.request_token.status_200":1000}},
    "emqx":{"counters":{"mqtt.total_connect_attempts":1050,"mqtt.total_connect_success":1050}},
    "iot_device_shadow":{"counters":{"app_user.desired_writes":50,"app_user.received_acks":50,"device_mqtt.delta_received":50,"device_mqtt.reported_publishes":50}},
    "edge_haproxy":{"counters":{"edge_haproxy.systemd.limit_nofile":1048576}},
    "host_pod_resources":{"samples":[
      {"namespace":"video-cloud","pod":"mqtt-0","cpu_millicores":100,"memory_bytes":268435456},
      {"namespace":"video-cloud","pod":"video-cloud-api-abc","cpu_millicores":50,"memory_bytes":134217728},
      {"namespace":"platform","pod":"postgresql-0","cpu_millicores":80,"memory_bytes":536870912}
    ]}
  }}
}`)
	writeTestFile(t, filepath.Join(runDir, "resource-samples", "load-vms.tsv"), "time\trun_id\tphase\tlabel\tip\trole\tid\tstatus\tcpu_pct\tload1\tmem_used_mb\tmem_total_mb\tdisk_used\tdisk_total\tdisk_pct\n2026\tlt1k\trun\tlg01\t192.0.2.1\tmixed\t1\tok\t10\t0.1\t100\t1000\t1G\t10G\t10\n2026\tlt1k\trun\tlg01\t192.0.2.1\tmixed\t1\tok\t20\t0.2\t200\t1000\t1G\t10G\t10\n")
	writeTestFile(t, filepath.Join(runDir, "resource-samples", "k8s-nodes.tsv"), "time\trun_id\tphase\tname\tstatus\tcpu\tcpu_pct\tmem\tmem_pct\treason\n2026\tlt1k\trun\tnode-a\tok\t100m\t10\t1Gi\t50\t\n2026\tlt1k\trun\tnode-a\tok\t200m\t20\t1Gi\t60\t\n")

	summary, err := buildLKECapacityRunSummary(runDir, envRoot, 0, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Outcome.Success || summary.Bottleneck != "none" {
		t.Fatalf("unexpected outcome: %#v bottleneck=%s", summary.Outcome, summary.Bottleneck)
	}
	if got := summary.CapacityCoefficients["safe_devices_per_mqtt_pod"]; got != 500 {
		t.Fatalf("safe_devices_per_mqtt_pod=%v, want 500", got)
	}
	if got := summary.CapacityCoefficients["safe_devices_per_node"]; got != 500 {
		t.Fatalf("safe_devices_per_node=%v, want 500", got)
	}
	if got := summary.Counters["video_cloud_api.request_token.status_200"]; got != 1000 {
		t.Fatalf("request_token.status_200=%d", got)
	}
	if _, ok := summary.ResourceSummary.Pods["video-cloud/mqtt-0"]; !ok {
		t.Fatalf("missing mqtt pod resource summary: %#v", summary.ResourceSummary.Pods)
	}
}

func TestBuildLKECapacityRunSummaryClassifiesGeneratorSaturation(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "reports", "fail")
	writeTestFile(t, filepath.Join(runDir, "results.json"), `{
  "run_id":"fail",
  "status":"INCOMPLETE",
  "result":"INCOMPLETE",
  "plan":{"conditions":{"devices":10000,"users":500,"devices_per_user":20,"vm_count":1,"load_generator_devices_per_vm":20000}},
  "load_generator_health":{"saturated":true},
  "server_evidence":{"complete":false},
  "server_correlation":{"status":"incomplete"},
  "runtime_log_correlation":{"status":"incomplete"}
}`)
	summary, err := buildLKECapacityRunSummary(runDir, "", 0, 1, 1, "g6-standard-2")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Bottleneck != "load_generator" {
		t.Fatalf("bottleneck=%s, want load_generator", summary.Bottleneck)
	}
	if summary.CapacityCoefficients != nil {
		t.Fatalf("failed run must not emit safe coefficients: %#v", summary.CapacityCoefficients)
	}
}

func TestBuildLKECapacityRunSummaryClassifiesRunnerTimeout(t *testing.T) {
	dir := t.TempDir()
	runDir := filepath.Join(dir, "reports", "timeout")
	writeTestFile(t, filepath.Join(runDir, "results.json"), `{
  "run_id":"timeout",
  "status":"INCOMPLETE",
  "result":"INCOMPLETE",
  "plan":{"conditions":{"devices":50000,"users":2500,"devices_per_user":20,"vm_count":3,"load_generator_devices_per_vm":20000}},
  "load_generator_health":{"saturated":false},
  "server_evidence":{"complete":true,"sources":{
    "iot_device_shadow":{"counters":{"app_user.desired_writes":2481,"app_user.received_acks":893}}
  }},
  "server_correlation":{"status":"incomplete"},
  "runtime_log_correlation":{"status":"incomplete"},
  "stage_results":[{
    "failure_details":{"runner_failed":{"live_target_mqtt-test_failed:_rtk-cloud_timed_out_after_33m30s":3}}
  }]
}`)
	summary, err := buildLKECapacityRunSummary(runDir, "", 0, 5, 5, "g6-standard-4")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Bottleneck != "runner_timeout" {
		t.Fatalf("bottleneck=%s, want runner_timeout", summary.Bottleneck)
	}
	if got := summary.Counters["runner.timeout_failures"]; got != 3 {
		t.Fatalf("runner.timeout_failures=%d, want 3", got)
	}
}

func TestBuildLKECapacityRunSummaryClassifiesMQTTPodOOM(t *testing.T) {
	dir := t.TempDir()
	envRoot := filepath.Join(dir, "envroot")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME=video-cloud-staging\nLKE_MQTT_REPLICAS=5\nLKE_NODE_COUNT=5\nLKE_NODE_TYPE=g6-standard-4\n")
	writeTestFile(t, filepath.Join(envRoot, "state", "lke-kubeconfig.yaml"), "apiVersion: v1\n")
	kubectl := filepath.Join(dir, "kubectl")
	writeTestFile(t, kubectl, `#!/usr/bin/env bash
cat <<'JSON'
{
  "items": [
    {
      "metadata": {"name": "mqtt-0", "namespace": "video-cloud-staging-video-cloud"},
      "status": {
        "containerStatuses": [{
          "restartCount": 1,
          "lastState": {
            "terminated": {
              "reason": "OOMKilled",
              "exitCode": 137,
              "finishedAt": "2026-06-22T19:21:50Z"
            }
          }
        }]
      }
    },
    {
      "metadata": {"name": "video-cloud-api-abc", "namespace": "video-cloud-staging-video-cloud"},
      "status": {"containerStatuses": [{"restartCount": 0}]}
    }
  ]
}
JSON
`)
	if err := os.Chmod(kubectl, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	runDir := filepath.Join(dir, "reports", "mqtt-oom")
	writeTestFile(t, filepath.Join(runDir, "results.json"), `{
  "run_id":"mqtt-oom",
  "status":"INCOMPLETE",
  "result":"INCOMPLETE",
  "plan":{"conditions":{"env_root":"`+envRoot+`","devices":50000,"users":2500,"devices_per_user":20,"vm_count":3,"load_generator_devices_per_vm":20000}},
  "load_generator_health":{"saturated":false},
  "server_evidence":{"complete":false,"sources":{
    "emqx":{"counters":{"mqtt.total_connect_attempts":50000,"mqtt.total_connect_success":49776}},
    "iot_device_shadow":{"counters":{"app_user.desired_writes":2501,"app_user.received_acks":139}}
  }},
  "server_correlation":{"status":"incomplete"},
  "runtime_log_correlation":{"status":"incomplete"},
  "device_mqtt_totals":{"total":{"connect_attempts":50000,"connect_success":49776,"connect_fail":224}},
  "app_user_totals":{"total":{"desired_writes":2501,"received_acks":139}}
}`)

	summary, err := buildLKECapacityRunSummary(runDir, envRoot, 0, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Bottleneck != "mqtt_pod_oom" {
		t.Fatalf("bottleneck=%s, want mqtt_pod_oom", summary.Bottleneck)
	}
	if len(summary.ResourceSummary.PodStatuses) != 1 || !summary.ResourceSummary.PodStatuses[0].OOMKilled {
		t.Fatalf("expected MQTT OOM status, got %#v", summary.ResourceSummary.PodStatuses)
	}
}

func TestBuildLKECapacityRunSummaryClassifiesCloudLoggerOOM(t *testing.T) {
	dir := t.TempDir()
	envRoot := filepath.Join(dir, "envroot")
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME=video-cloud-staging\n")
	writeTestFile(t, filepath.Join(envRoot, "state", "lke-kubeconfig.yaml"), "apiVersion: v1\n")
	kubectl := filepath.Join(dir, "kubectl")
	writeTestFile(t, kubectl, `#!/usr/bin/env bash
cat <<'JSON'
{
  "items": [
    {
      "metadata": {"name": "cloud-logger-abc", "namespace": "video-cloud-staging-logger"},
      "status": {
        "containerStatuses": [{
          "restartCount": 1,
          "lastState": {
            "terminated": {
              "reason": "OOMKilled",
              "exitCode": 137,
              "finishedAt": "2026-06-22T21:28:47Z"
            }
          }
        }]
      }
    }
  ]
}
JSON
`)
	if err := os.Chmod(kubectl, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	runDir := filepath.Join(dir, "reports", "logger-oom")
	writeTestFile(t, filepath.Join(runDir, "results.json"), `{
  "run_id":"logger-oom",
  "status":"COMPLETE",
  "result":"FAIL",
  "plan":{"conditions":{"env_root":"`+envRoot+`","devices":50000,"users":2500,"devices_per_user":20,"vm_count":3,"load_generator_devices_per_vm":20000}},
  "load_generator_health":{"saturated":false},
  "server_evidence":{"complete":true,"sources":{
    "emqx":{"counters":{"mqtt.total_connect_attempts":52502,"mqtt.total_connect_success":52502}},
    "iot_device_shadow":{"counters":{"app_user.desired_writes":603,"app_user.received_acks":603,"device_mqtt.delta_received":603,"device_mqtt.reported_publishes":603}}
  }},
  "server_correlation":{"status":"fail"},
  "runtime_log_correlation":{"status":"fail"},
  "device_mqtt_totals":{"connect_attempts":50000,"connect_success":50000,"connect_fail":0,"delta_received":2502,"reported_publishes":2502},
  "app_user_totals":{"login_attempts":3,"login_success":3,"login_fail":0,"desired_writes":2502,"received_acks":2502}
}`)

	summary, err := buildLKECapacityRunSummary(runDir, envRoot, 0, 5, 5, "g6-standard-4")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Bottleneck != "cloud_logger_oom" {
		t.Fatalf("bottleneck=%s, want cloud_logger_oom", summary.Bottleneck)
	}
	if got := summary.Counters["client.app_user.desired_writes"]; got != 2502 {
		t.Fatalf("client.app_user.desired_writes=%d, want 2502", got)
	}
}
