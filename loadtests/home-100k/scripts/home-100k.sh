#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
description_file="${HOME100K_DESCRIPTION_FILE:-$repo_root/loadtests/home-100k/scenarios/default.description.env}"

capture_home100k_env_overrides() {
  local name
  env | while IFS='=' read -r name _; do
    case "$name" in
      HOME100K_*)
        printf '%s=%q\n' "$name" "${!name-}"
        ;;
    esac
  done
}

load_linode_token_from_env_file() {
  local env_file="${HOME100K_SECRET_ENV_FILE:-$HOME/.env}"
  local line value
  if [[ -n "${LINODE_TOKEN:-}" || ! -f "$env_file" ]]; then
    return
  fi
  while IFS= read -r line; do
    case "$line" in
      LINODE_TOKEN=*|export\ LINODE_TOKEN=*)
        value="${line#export LINODE_TOKEN=}"
        value="${value#LINODE_TOKEN=}"
        value="${value%$'\r'}"
        value="${value%\"}"
        value="${value#\"}"
        value="${value%\'}"
        value="${value#\'}"
        export LINODE_TOKEN="$value"
        return
        ;;
    esac
  done < "$env_file"
}

explicit_home100k_env="$(capture_home100k_env_overrides)"
preexisting_linode_token="${LINODE_TOKEN:-}"
if [[ -f "$description_file" ]]; then
  set -a
  # shellcheck source=/dev/null
  source "$description_file"
  set +a
fi
if [[ -n "$explicit_home100k_env" ]]; then
  eval "$explicit_home100k_env"
fi
if [[ -n "$preexisting_linode_token" ]]; then
  export LINODE_TOKEN="$preexisting_linode_token"
else
  unset LINODE_TOKEN || true
fi
load_linode_token_from_env_file

env_root="${HOME100K_ENV_ROOT:-cloud_env/staging/lke}"
brandname="${HOME100K_BRANDNAME:-RTK}"
region="${HOME100K_REGION:-us-sea}"
run_id="${HOME100K_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
out_dir="${HOME100K_OUT_DIR:-loadtests/home-100k/reports/${run_id}}"
remote_workspace="${HOME100K_REMOTE_WORKSPACE:-/root/rtk_cloud_workspace}"
remote_env_root="${HOME100K_REMOTE_ENV_ROOT:-${remote_workspace}/${env_root}}"
remote_out_root="${HOME100K_REMOTE_OUT_ROOT:-/var/lib/home-100k}"
ssh_key="${HOME100K_SSH_KEY:-$HOME/.ssh/id_ed25519}"
ssh_user="${HOME100K_SSH_USER:-root}"
authorized_key_file="${HOME100K_AUTHORIZED_KEY_FILE:-${ssh_key}.pub}"
status_interval_seconds="${HOME100K_STATUS_INTERVAL_SECONDS:-30}"
stage_warm_up="${HOME100K_STAGE_WARM_UP:-1m}"
stage_steady="${HOME100K_STAGE_STEADY:-2m}"
stage_cool_down="${HOME100K_STAGE_COOL_DOWN:-45s}"
device_count="${HOME100K_DEVICES:-}"
user_count="${HOME100K_USERS:-}"
devices_per_user="${HOME100K_DEVICES_PER_USER:-}"
runner_mode="${HOME100K_RUNNER_MODE:-live}"
runner_nofile_limit="${HOME100K_RUNNER_NOFILE_LIMIT:-1048576}"
device_session_model="${HOME100K_DEVICE_SESSION_MODEL:-lifetime-subscription}"
runner_read_model="${HOME100K_RUNNER_READ_MODEL:-go-netpoll-bounded-reader-goroutine}"
coordinator_start_delay_ms="${HOME100K_COORDINATOR_START_DELAY_MS:-3000}"
credential_bundle_format="${HOME100K_CREDENTIAL_BUNDLE_FORMAT:-sqlite-gzip}"
mqtt_addr="${HOME100K_MQTT_ADDR:-}"
mqtt_public_lb_count="${HOME100K_MQTT_PUBLIC_LB_COUNT:-}"
video_cloud_public_url="${HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL:-${HOME100K_VIDEO_CLOUD_BASE_URL:-}}"
video_cloud_token_url="${HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL:-}"
account_manager_base_url="${HOME100K_ACCOUNT_MANAGER_BASE_URL:-}"
status_file="$repo_root/$out_dir/.workflow-status"
nodes_file="$repo_root/$out_dir/nodes.tsv"
ssh_known_hosts_file="$repo_root/$out_dir/ssh_known_hosts"
resource_samples_dir="$repo_root/$out_dir/resource-samples"
load_vm_resource_file="$resource_samples_dir/load-vms.tsv"
k8s_node_resource_file="$resource_samples_dir/k8s-nodes.tsv"
workflow_status_log="$repo_root/$out_dir/workflow-status.log"
shutdown_live_vms_on_exit=0
shutdown_on_error="${HOME100K_SHUTDOWN_ON_ERROR:-0}"

usage() {
  cat <<EOF
usage: $(basename "$0") [command] [home-100k flags]

Default command:
  workflow-live           Create VMs and run the live lifecycle through aggregate.

Commands:
  plan                    Print the deterministic configured-size mixed run plan.
  dry-run                 Render local review artifacts; does not create VMs.
  provision-vms           Review or live-create Linode VMs.
  sync                    Review or live-sync runner/env-root to VMs.
  run-stages              Review or live-dispatch shard runners.
  collect                 Review or live-collect shard artifacts.
  collect-server-evidence Review or live-collect server evidence.
  aggregate               Aggregate collected shards and server evidence.
  generate-report         Generate TEST_REPORT.md from collected artifacts and template.
  list-vms                Review or live-list leftover VMs by run id.
  shutdown-vms            Live-shutdown VMs by state file for reuse.
  destroy-vms             Review or live-destroy VMs by state file; manual cleanup only.
  workflow-dry-run        Run plan plus dry-run lifecycle review commands.
  workflow-live           Create VMs and run the live lifecycle through aggregate.
  workflow-resume-live    Resume an existing live run from sync using <out-dir>/vms.json.

Defaults can be overridden with:
  HOME100K_DESCRIPTION_FILE default: loadtests/home-100k/scenarios/default.description.env
  HOME100K_SECRET_ENV_FILE  default: ~/.env; only LINODE_TOKEN is read
  HOME100K_ENV_ROOT       default: cloud_env/staging/lke
  HOME100K_BRANDNAME      default: RTK
  HOME100K_REGION         default: us-sea
  HOME100K_RUN_ID         default: current UTC timestamp
  HOME100K_OUT_DIR        default: loadtests/home-100k/reports/<run-id>
  HOME100K_REMOTE_WORKSPACE default: /root/rtk_cloud_workspace
  HOME100K_REMOTE_ENV_ROOT  default: <remote-workspace>/<env-root>
  HOME100K_REMOTE_OUT_ROOT  default: /var/lib/home-100k
  HOME100K_SSH_USER         default: root
  HOME100K_SSH_KEY          default: ~/.ssh/id_ed25519
  HOME100K_AUTHORIZED_KEY_FILE default: <HOME100K_SSH_KEY>.pub
  SSH known_hosts is isolated per run at <out-dir>/ssh_known_hosts
  HOME100K_STATUS_INTERVAL_SECONDS default: 30
  HOME100K_STAGE_WARM_UP default: 15s from the default description file
  HOME100K_STAGE_STEADY default: 45s from the default description file
  HOME100K_STAGE_COOL_DOWN default: 15s from the default description file
  HOME100K_DEVICES configured in the description file; current default description uses 9000
  HOME100K_USERS optional; when omitted, the planner derives users from devices/devices-per-user
  HOME100K_DEVICES_PER_USER configured in the description file; current default description uses 20
  HOME100K_RUNNER_MODE default: live; use sample only for local developer smoke tests
  HOME100K_RUNNER_NOFILE_LIMIT default: 1048576; remote daemon nofile limit for MQTT sockets
  HOME100K_DEVICE_SESSION_MODEL default: lifetime-subscription; device MQTT subscriptions stay open for device lifetime
  HOME100K_RUNNER_READ_MODEL default: go-netpoll-bounded-reader-goroutine; sustained async MQTT reads
  HOME100K_COORDINATOR_START_DELAY_MS default: 3000
  HOME100K_CREDENTIAL_BUNDLE_FORMAT default: sqlite-gzip; only supported format
  HOME100K_MQTT_ADDR public MQTT endpoints for remote Linode generators; use auto-public-mqtt to discover mqtt-public* services
  HOME100K_MQTT_PUBLIC_LB_COUNT limits auto-public-mqtt endpoint count; current 9K profile uses 1
  HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL optional public Video Cloud API base URL for remote generators
  HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL optional mTLS/device Video Cloud token bootstrap base URL
  HOME100K_VIDEO_CLOUD_BASE_URL legacy alias for HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL
  HOME100K_ACCOUNT_MANAGER_BASE_URL optional Account Manager base URL for remote generators
  HOME100K_NODE_RESOURCE_STATUS default: 1
  HOME100K_K8S_NODE_RESOURCE_STATUS default: 1
  HOME100K_SHUTDOWN_ON_ERROR default: 0; keep VMs running after failures for resume/debug
  HOME100K_KUBECONFIG default: RTK_CLOUD_LKE_KUBECONFIG, LKE_KUBECONFIG, CLOUD_STAGING_K8S_KUBECONFIG, or <env-root>/state/lke-kubeconfig.yaml

Examples:
  $(basename "$0")
  $(basename "$0") workflow-live
  HOME100K_RUN_ID=<run-id> $(basename "$0") workflow-resume-live
  $(basename "$0") plan
  $(basename "$0") dry-run
  HOME100K_REGION=us-southeast $(basename "$0") provision-vms --live --confirm-live
EOF
}

run_home100k() {
  (cd "$repo_root" && go run ./loadtests/home-100k/cmd/home-100k -- "$@")
}

set_phase() {
  mkdir -p "$(dirname "$status_file")"
  printf '%s\n' "$1" > "$status_file"
}

ensure_resource_logs() {
  mkdir -p "$resource_samples_dir"
  if [[ ! -f "$load_vm_resource_file" ]]; then
    printf 'time\trun_id\tphase\tlabel\tip\trole\tid\tstatus\tcpu_pct\tload1\tmem_used_mb\tmem_total_mb\tdisk_used\tdisk_total\tdisk_pct\n' > "$load_vm_resource_file"
  fi
  if [[ ! -f "$k8s_node_resource_file" ]]; then
    printf 'time\trun_id\tphase\tname\tstatus\tcpu\tcpu_pct\tmem\tmem_pct\treason\n' > "$k8s_node_resource_file"
  fi
  if [[ ! -f "$workflow_status_log" ]]; then
    printf 'time\trun_id\telapsed\tphase\tvms\tshard_results\tserver_evidence\treport_status\n' > "$workflow_status_log"
  fi
}

kv_field() {
  local sample="$1"
  local key="$2"
  printf '%s\n' "$sample" | tr ' ' '\n' | awk -F= -v key="$key" '$1 == key {print $2; found=1; exit} END {if (!found) print ""}'
}

generate_report_from_artifacts() {
  "$repo_root/loadtests/home-100k/scripts/generate-report.sh" --out-dir "$repo_root/$out_dir"
}

write_nodes_file() {
  local vms_file="$repo_root/$out_dir/vms.json"
  if [[ ! -f "$vms_file" ]]; then
    return
  fi
  python3 - "$vms_file" "$nodes_file" <<'PY'
import json
import sys

vms_path, nodes_path = sys.argv[1], sys.argv[2]
with open(vms_path) as f:
    data = json.load(f)
vms = data.get("created") or data.get("vms") or []
with open(nodes_path, "w") as f:
    f.write("label\tip\trole\tid\n")
    for vm in vms:
        label = vm.get("label", "")
        ip = vm.get("public_ipv4", "")
        role = "mixed"
        parts = label.split("-")
        if len(parts) >= 4 and parts[0] == "home" and parts[1] == "100k":
            role = "-".join(parts[2:-1])
        f.write(f"{label}\t{ip}\t{role}\t{vm.get('id', '')}\n")
PY
}

node_resource_status() {
  if [[ "${HOME100K_NODE_RESOURCE_STATUS:-1}" == "0" || ! -f "$nodes_file" ]]; then
    return
  fi
  ensure_resource_logs
  local now phase
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  phase="starting"
  if [[ -f "$status_file" ]]; then
    phase="$(cat "$status_file")"
  fi
  while IFS=$'\t' read -r label ip role id; do
    if [[ "$label" == "label" || -z "$ip" ]]; then
      continue
    fi
    local sample
    sample="$(ssh -n -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "${ssh_user}@${ip}" \
      'read _ u n s i w irq sirq steal guest guestn < /proc/stat; total1=$((u+n+s+i+w+irq+sirq+steal)); idle1=$((i+w)); sleep 1; read _ u n s i w irq sirq steal guest guestn < /proc/stat; total2=$((u+n+s+i+w+irq+sirq+steal)); idle2=$((i+w)); awk -v t1=$total1 -v t2=$total2 -v i1=$idle1 -v i2=$idle2 "BEGIN {dt=t2-t1; di=i2-i1; if (dt>0) printf \"cpu_pct=%.1f \", 100*(dt-di)/dt; else printf \"cpu_pct=unknown \"}"; awk "{printf \"load1=%s \", \$1}" /proc/loadavg; free -m | awk "/^Mem:/ {printf \"mem_used_mb=%s mem_total_mb=%s \", \$3, \$2}"; df -h / | awk "NR==2 {printf \"disk_used=%s disk_total=%s disk_pct=%s\", \$3, \$2, \$5}"' 2>/dev/null || true)"
    if [[ -z "$sample" ]]; then
      sample="unreachable"
    fi
    printf '[home-100k node] label=%s ip=%s role=%s id=%s %s\n' "$label" "$ip" "$role" "$id" "$sample" >&2
    if [[ "$sample" == "unreachable" ]]; then
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\tunreachable\t\t\t\t\t\t\t\n' "$now" "$run_id" "$phase" "$label" "$ip" "$role" "$id" >> "$load_vm_resource_file"
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\tok\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$now" "$run_id" "$phase" "$label" "$ip" "$role" "$id" \
        "$(kv_field "$sample" cpu_pct)" \
        "$(kv_field "$sample" load1)" \
        "$(kv_field "$sample" mem_used_mb)" \
        "$(kv_field "$sample" mem_total_mb)" \
        "$(kv_field "$sample" disk_used)" \
        "$(kv_field "$sample" disk_total)" \
        "$(kv_field "$sample" disk_pct | tr -d '%')" >> "$load_vm_resource_file"
    fi
  done < "$nodes_file"
}

k8s_kubeconfig() {
  local candidate
  for candidate in \
    "${HOME100K_KUBECONFIG:-}" \
    "${RTK_CLOUD_LKE_KUBECONFIG:-}" \
    "${LKE_KUBECONFIG:-}" \
    "${CLOUD_STAGING_K8S_KUBECONFIG:-}" \
    "$repo_root/$env_root/state/lke-kubeconfig.yaml"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

export_kubeconfig_if_available() {
  local kubeconfig
  kubeconfig="$(k8s_kubeconfig || true)"
  if [[ -n "$kubeconfig" ]]; then
    export KUBECONFIG="$kubeconfig"
  fi
}

command_needs_public_mqtt_addr() {
  case "$1" in
    sync|run-stages|workflow-live|workflow-resume-live)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

discover_public_mqtt_addr() {
  local kubectl_bin="${HOME100K_KUBECTL:-${RTK_CLOUD_KUBECTL:-kubectl}}"
  if ! command -v "$kubectl_bin" >/dev/null 2>&1; then
    echo "HOME100K_MQTT_ADDR=auto-public-mqtt requires kubectl" >&2
    return 1
  fi
  if ! command -v jq >/dev/null 2>&1; then
    echo "HOME100K_MQTT_ADDR=auto-public-mqtt requires jq" >&2
    return 1
  fi
  local kubeconfig output addrs
  kubeconfig="$(k8s_kubeconfig || true)"
  if [[ -n "$kubeconfig" ]]; then
    output="$(KUBECONFIG="$kubeconfig" "$kubectl_bin" -n video-cloud-staging-video-cloud get svc -l app.kubernetes.io/component=public-mqtt -o json)"
  else
    output="$("$kubectl_bin" -n video-cloud-staging-video-cloud get svc -l app.kubernetes.io/component=public-mqtt -o json)"
  fi
  local lb_count="${mqtt_public_lb_count:-0}"
  if ! [[ "$lb_count" =~ ^[0-9]+$ ]]; then
    echo "HOME100K_MQTT_PUBLIC_LB_COUNT must be a non-negative integer, got: $mqtt_public_lb_count" >&2
    return 1
  fi
  addrs="$(printf '%s' "$output" | jq -r --argjson limit "$lb_count" '
    [.items[] | select(.status.loadBalancer.ingress[0].ip != null) | .status.loadBalancer.ingress[0].ip + ":8883"]
    | sort
    | if $limit > 0 then .[:$limit] else . end
    | join(",")
  ')"
  if [[ -z "$addrs" ]]; then
    echo "HOME100K_MQTT_ADDR=auto-public-mqtt found no public MQTT LoadBalancer IPs" >&2
    return 1
  fi
  printf '%s\n' "$addrs"
}

resolve_mqtt_addr_for_command() {
  local command_name="$1"
  case "$mqtt_addr" in
    auto|auto-public-mqtt)
      if command_needs_public_mqtt_addr "$command_name"; then
        mqtt_addr="$(discover_public_mqtt_addr)"
        printf '[home-100k config] resolved HOME100K_MQTT_ADDR=%s\n' "$mqtt_addr" >&2
      else
        mqtt_addr=""
      fi
      ;;
  esac
}

k8s_node_resource_status() {
  if [[ "${HOME100K_K8S_NODE_RESOURCE_STATUS:-1}" == "0" ]]; then
    return
  fi
  ensure_resource_logs
  local now phase
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  phase="starting"
  if [[ -f "$status_file" ]]; then
    phase="$(cat "$status_file")"
  fi
  local kubectl_bin="${HOME100K_KUBECTL:-${RTK_CLOUD_KUBECTL:-kubectl}}"
  if ! command -v "$kubectl_bin" >/dev/null 2>&1; then
    printf '[home-100k k8s-node] unavailable reason=kubectl-not-found\n' >&2
    printf '%s\t%s\t%s\t%s\tunavailable\t\t\t\t\tkubectl-not-found\n' "$now" "$run_id" "$phase" "all" >> "$k8s_node_resource_file"
    return
  fi
  local kubeconfig="" output rc
  kubeconfig="$(k8s_kubeconfig || true)"
  if [[ -n "$kubeconfig" ]]; then
    output="$(KUBECONFIG="$kubeconfig" "$kubectl_bin" top nodes --no-headers --request-timeout=5s 2>&1)" || rc=$?
  else
    output="$("$kubectl_bin" top nodes --no-headers --request-timeout=5s 2>&1)" || rc=$?
  fi
  if [[ -n "${rc:-}" ]]; then
    output="$(printf '%s' "$output" | tr '\n' ' ' | sed 's/[[:space:]]\+/ /g; s/[[:space:]]$//')"
    printf '[home-100k k8s-node] unavailable reason=%s\n' "${output:-kubectl-top-nodes-failed}" >&2
    printf '%s\t%s\t%s\t%s\tunavailable\t\t\t\t\t%s\n' "$now" "$run_id" "$phase" "all" "${output:-kubectl-top-nodes-failed}" >> "$k8s_node_resource_file"
    return
  fi
  if [[ -z "$output" ]]; then
    printf '[home-100k k8s-node] unavailable reason=no-node-metrics\n' >&2
    printf '%s\t%s\t%s\t%s\tunavailable\t\t\t\t\tno-node-metrics\n' "$now" "$run_id" "$phase" "all" >> "$k8s_node_resource_file"
    return
  fi
  printf '%s\n' "$output" | awk '
    NF >= 5 {
      printf("[home-100k k8s-node] name=%s cpu=%s cpu_pct=%s mem=%s mem_pct=%s\n", $1, $2, $3, $4, $5)
    }
  ' >&2
  printf '%s\n' "$output" | awk -v now="$now" -v run_id="$run_id" -v phase="$phase" '
    NF >= 5 {
      gsub(/%$/, "", $3)
      gsub(/%$/, "", $5)
      printf("%s\t%s\t%s\t%s\tok\t%s\t%s\t%s\t%s\t\n", now, run_id, phase, $1, $2, $3, $4, $5)
    }
  ' >> "$k8s_node_resource_file"
}

workflow_status() {
  local now elapsed phase vm_count shard_count report_status evidence_status
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  elapsed="$(($(date +%s) - workflow_start_epoch))s"
  phase="starting"
  if [[ -f "$status_file" ]]; then
    phase="$(cat "$status_file")"
  fi
  vm_count="0"
  if [[ -f "$repo_root/$out_dir/vms.json" ]]; then
    write_nodes_file
    vm_count="$(python3 - "$repo_root/$out_dir/vms.json" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
vms = data.get("created") or data.get("vms") or []
print(len({vm.get("label", "") for vm in vms if vm.get("label")}))
PY
)"
  fi
  shard_count="0"
  if [[ -d "$repo_root/$out_dir/shards" ]]; then
    shard_count="$(find "$repo_root/$out_dir/shards" -name results.json -type f | wc -l | tr -d ' ')"
  fi
  report_status="not-written"
  if [[ -f "$repo_root/$out_dir/TEST_REPORT.md" ]]; then
    report_status="$( (grep -m1 '^- Status:' "$repo_root/$out_dir/TEST_REPORT.md" || true) | sed 's/^- Status: //')"
    if [[ -z "$report_status" ]]; then
      report_status="unknown"
    fi
  fi
  evidence_status="not-written"
  if [[ -f "$repo_root/$out_dir/server-evidence.json" ]]; then
    if grep -q '"complete"[[:space:]]*:[[:space:]]*true' "$repo_root/$out_dir/server-evidence.json"; then
      evidence_status="complete"
    else
      evidence_status="incomplete"
    fi
  fi
  {
    printf '[home-100k status] time=%s elapsed=%s run_id=%s phase=%s\n' "$now" "$elapsed" "$run_id" "$phase"
    printf '[home-100k status] out_dir=%s vms=%s/5 shard_results=%s server_evidence=%s report_status=%s target_devices=%s target_users=%s devices_per_user=%s stage_window=warm_up:%s,steady:%s,cool_down:%s\n' "$out_dir" "$vm_count" "$shard_count" "$evidence_status" "$report_status" "${device_count:-planner-default}" "${user_count:-derived}" "${devices_per_user:-planner-default}" "$stage_warm_up" "$stage_steady" "$stage_cool_down"
  } >&2
  ensure_resource_logs
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$now" "$run_id" "$elapsed" "$phase" "$vm_count" "$shard_count" "$evidence_status" "$report_status" >> "$workflow_status_log"
  node_resource_status
  k8s_node_resource_status
}

current_report_status() {
  local report="$repo_root/$out_dir/TEST_REPORT.md"
  local status=""
  if [[ -f "$report" ]]; then
    status="$( (grep -m1 '^- Status:' "$report" || true) | sed 's/^- Status: //')"
  fi
  printf '%s\n' "${status:-not-written}"
}

start_status_monitor() {
  workflow_start_epoch="$(date +%s)"
  set_phase "starting"
  workflow_status
  (
    while true; do
      sleep "$status_interval_seconds"
      workflow_status
    done
  ) &
  status_monitor_pid="$!"
  trap 'on_script_exit $?' EXIT
}

stop_status_monitor() {
  if [[ -n "${status_monitor_pid:-}" ]]; then
    kill "$status_monitor_pid" 2>/dev/null || true
    wait "$status_monitor_pid" 2>/dev/null || true
    trap - EXIT
  fi
}

shutdown_live_vms() {
  local vms_file="$repo_root/$out_dir/vms.json"
  if [[ ! -f "$vms_file" || -z "${LINODE_TOKEN:-}" ]]; then
    return
  fi
  python3 - "$vms_file" <<'PY' | while IFS= read -r id; do
import json
import sys

with open(sys.argv[1]) as f:
    data = json.load(f)
for vm in data.get("created") or data.get("vms") or []:
    if vm.get("id"):
        print(vm["id"])
PY
    [[ -n "$id" ]] || continue
    curl -fsS -X POST \
      -H "Authorization: Bearer $LINODE_TOKEN" \
      -H "Content-Type: application/json" \
      "https://api.linode.com/v4/linode/instances/${id}/shutdown" >/dev/null || true
  done
}

should_shutdown_after_workflow() {
  if [[ "$shutdown_on_error" == "1" ]]; then
    return 0
  fi
  if [[ "$workflow_rc" != "0" ]]; then
    return 1
  fi
  [[ "$(current_report_status)" == "PASS" ]]
}

on_script_exit() {
  local rc="${1:-0}"
  if [[ -n "${status_monitor_pid:-}" ]]; then
    kill "$status_monitor_pid" 2>/dev/null || true
    wait "$status_monitor_pid" 2>/dev/null || true
  fi
  if [[ "$shutdown_live_vms_on_exit" == "1" && ( "$rc" == "0" || "$shutdown_on_error" == "1" ) ]]; then
    set_phase "shutdown-vms"
    shutdown_live_vms >/dev/null 2>&1 || true
  fi
  exit "$rc"
}

command="${1:-workflow-live}"
if [[ "$command" == "-h" || "$command" == "--help" ]]; then
  usage
  exit 0
fi
if [[ "$#" -gt 0 ]]; then
  shift
fi
resolve_mqtt_addr_for_command "$command"

base_args=(
  "--env-root" "$env_root"
  "--brandname" "$brandname"
  "--region" "$region"
  "--stage-warm-up" "$stage_warm_up"
  "--stage-steady" "$stage_steady"
  "--stage-cool-down" "$stage_cool_down"
)
if [[ -n "$device_count" ]]; then
  base_args+=("--devices" "$device_count")
fi
if [[ -n "$user_count" ]]; then
  base_args+=("--users" "$user_count")
fi
if [[ -n "$devices_per_user" ]]; then
  base_args+=("--devices-per-user" "$devices_per_user")
fi
plan_condition_args=(
  "${base_args[@]}"
  "--runner-nofile-limit" "$runner_nofile_limit"
  "--device-session-model" "$device_session_model"
  "--runner-read-model" "$runner_read_model"
)
workflow_args=("${base_args[@]}")
coordinator_args=(
  "--coordinator-start-delay-ms" "$coordinator_start_delay_ms"
)
workflow_args+=("--credential-bundle-format" "$credential_bundle_format")
workflow_args+=("--runner-nofile-limit" "$runner_nofile_limit")
workflow_args+=("--device-session-model" "$device_session_model")
workflow_args+=("--runner-read-model" "$runner_read_model")
if [[ -n "$mqtt_addr" ]]; then
  workflow_args+=("--mqtt-addr" "$mqtt_addr")
fi
if [[ -n "$video_cloud_public_url" ]]; then
  workflow_args+=("--video-cloud-public-base-url" "$video_cloud_public_url")
fi
if [[ -n "$video_cloud_token_url" ]]; then
  workflow_args+=("--video-cloud-token-base-url" "$video_cloud_token_url")
fi
if [[ -n "$account_manager_base_url" ]]; then
  workflow_args+=("--account-manager-base-url" "$account_manager_base_url")
fi

case "$command" in
  plan)
    run_home100k plan "${plan_condition_args[@]}" "$@"
    ;;
  dry-run)
    mkdir -p "$repo_root/$out_dir"
    run_home100k run "${plan_condition_args[@]}" --ephemeral-vms --run-id "$run_id" --out-dir "$out_dir" "$@"
    ;;
  provision-vms)
    mkdir -p "$repo_root/$out_dir"
    run_home100k provision-vms "${base_args[@]}" --run-id "$run_id" --out-dir "$out_dir" "$@"
    ;;
  sync)
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json" "$@"
    ;;
  run-stages)
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --runner-mode "$runner_mode" "$@"
    ;;
  collect)
    mkdir -p "$repo_root/$out_dir"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" "$@"
    ;;
  collect-server-evidence)
    mkdir -p "$repo_root/$out_dir"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" "$@"
    ;;
  aggregate)
    mkdir -p "$repo_root/$out_dir"
    run_home100k aggregate "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" "$@"
    generate_report_from_artifacts
    ;;
  generate-report)
    mkdir -p "$repo_root/$out_dir"
    generate_report_from_artifacts
    ;;
  list-vms)
    run_home100k list-vms "${base_args[@]}" --run-id "$run_id" "$@"
    ;;
  destroy-vms)
    run_home100k destroy-vms "${base_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json" "$@"
    ;;
  shutdown-vms)
    shutdown_live_vms
    ;;
  workflow-dry-run)
    run_home100k plan "${plan_condition_args[@]}"
    run_home100k provision-vms "${base_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json"
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json" --runner-mode sample
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
    ;;
  workflow-live)
    if [[ -z "${LINODE_TOKEN:-}" ]]; then
      echo "workflow-live requires LINODE_TOKEN" >&2
      exit 2
    fi
    if [[ ! -f "$ssh_key" ]]; then
      echo "workflow-live SSH key not found: $ssh_key" >&2
      exit 2
    fi
    if [[ ! -f "$authorized_key_file" ]]; then
      echo "workflow-live authorized public key not found: $authorized_key_file" >&2
      exit 2
    fi
    mkdir -p "$repo_root/$out_dir"
    rm -f "$ssh_known_hosts_file"
    shutdown_live_vms_on_exit=1
    start_status_monitor
    set_phase "provision-vms"
    run_home100k provision-vms "${base_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --live --confirm-live --authorized-key-file "$authorized_key_file" "$@"
    workflow_status
    set_phase "sync"
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-baseline"
    export_kubeconfig_if_available
    rm -f "$repo_root/$out_dir/server-evidence-baseline.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --server-evidence-file "$out_dir/server-evidence-baseline.json" --live
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-evidence"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --live
    workflow_status
    set_phase "aggregate"
    run_home100k aggregate "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
    generate_report_from_artifacts
    report_status="$(current_report_status)"
    if [[ "$report_status" != "PASS" && "$workflow_rc" -eq 0 ]]; then
      workflow_rc=1
      echo "report status is $report_status; preserving VMs for investigation" >&2
    fi
    cleanup_rc=0
    if should_shutdown_after_workflow; then
      set_phase "shutdown-vms"
      shutdown_live_vms || cleanup_rc=$?
    else
      set_phase "preserve-vms"
      echo "preserving live VMs for resume/debug; run shutdown-vms when finished" >&2
    fi
    shutdown_live_vms_on_exit=0
    set_phase "complete"
    stop_status_monitor
    echo "live workflow artifacts: $out_dir"
    if [[ "$workflow_rc" -ne 0 ]]; then
      exit "$workflow_rc"
    fi
    exit "$cleanup_rc"
    ;;
  workflow-resume-live)
    if [[ -z "${LINODE_TOKEN:-}" ]]; then
      echo "workflow-resume-live requires LINODE_TOKEN" >&2
      exit 2
    fi
    if [[ ! -f "$ssh_key" ]]; then
      echo "workflow-resume-live SSH key not found: $ssh_key" >&2
      exit 2
    fi
    if [[ ! -f "$authorized_key_file" ]]; then
      echo "workflow-resume-live authorized public key not found: $authorized_key_file" >&2
      exit 2
    fi
    if [[ ! -f "$repo_root/$out_dir/vms.json" ]]; then
      echo "workflow-resume-live requires existing VM state: $out_dir/vms.json" >&2
      exit 2
    fi
    mkdir -p "$repo_root/$out_dir"
    shutdown_live_vms_on_exit=1
    start_status_monitor
    set_phase "sync"
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-baseline"
    export_kubeconfig_if_available
    rm -f "$repo_root/$out_dir/server-evidence-baseline.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --server-evidence-file "$out_dir/server-evidence-baseline.json" --live
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-evidence"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --live
    workflow_status
    set_phase "aggregate"
    run_home100k aggregate "${workflow_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
    generate_report_from_artifacts
    report_status="$(current_report_status)"
    if [[ "$report_status" != "PASS" && "$workflow_rc" -eq 0 ]]; then
      workflow_rc=1
      echo "report status is $report_status; preserving VMs for investigation" >&2
    fi
    cleanup_rc=0
    if should_shutdown_after_workflow; then
      set_phase "shutdown-vms"
      shutdown_live_vms || cleanup_rc=$?
    else
      set_phase "preserve-vms"
      echo "preserving live VMs for resume/debug; run shutdown-vms when finished" >&2
    fi
    shutdown_live_vms_on_exit=0
    set_phase "complete"
    stop_status_monitor
    echo "live workflow artifacts: $out_dir"
    if [[ "$workflow_rc" -ne 0 ]]; then
      exit "$workflow_rc"
    fi
    exit "$cleanup_rc"
    ;;
  *)
    echo "unknown command: $command" >&2
    usage >&2
    exit 2
    ;;
esac
