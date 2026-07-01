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
brand_plan="${HOME100K_BRAND_PLAN:-}"
scenario_profile="${HOME100K_SCENARIO_PROFILE:-}"
region="${HOME100K_REGION:-us-sea}"
vm_label_prefix="${HOME100K_VM_LABEL_PREFIX:-lg}"
run_id="${HOME100K_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
out_dir="${HOME100K_OUT_DIR:-loadtests/home-100k/reports/${run_id}}"
remote_workspace="${HOME100K_REMOTE_WORKSPACE:-/root/rtk_cloud_workspace}"
remote_env_root="${HOME100K_REMOTE_ENV_ROOT:-${remote_workspace}/${env_root}}"
remote_out_root="${HOME100K_REMOTE_OUT_ROOT:-/var/lib/home-100k}"
ssh_key="${HOME100K_SSH_KEY:-$HOME/.ssh/id_ed25519_rtkcloud}"
ssh_user="${HOME100K_SSH_USER:-root}"
authorized_key_file="${HOME100K_AUTHORIZED_KEY_FILE:-${ssh_key}.pub}"
status_interval_seconds="${HOME100K_STATUS_INTERVAL_SECONDS:-30}"
stage_warm_up="${HOME100K_STAGE_WARM_UP:-30s}"
stage_steady="${HOME100K_STAGE_STEADY:-90s}"
stage_cool_down="${HOME100K_STAGE_COOL_DOWN:-30s}"
device_count="${HOME100K_DEVICES:-}"
user_count="${HOME100K_USERS:-}"
devices_per_user="${HOME100K_DEVICES_PER_USER:-}"
vm_count="${HOME100K_VM_COUNT:-}"
load_generator_devices_per_vm="${HOME100K_LOAD_GENERATOR_DEVICES_PER_VM:-20000}"
runner_mode="${HOME100K_RUNNER_MODE:-live}"
runner_nofile_limit="${HOME100K_RUNNER_NOFILE_LIMIT:-1048576}"
mqtt_concurrency="${HOME100K_MQTT_CONCURRENCY:-1000}"
command_concurrency="${HOME100K_COMMAND_CONCURRENCY:-100}"
shadow_command_timeout="${HOME100K_SHADOW_COMMAND_TIMEOUT:-30s}"
live_runner_timeout_grace="${HOME100K_LIVE_RUNNER_TIMEOUT_GRACE:-}"
device_session_model="${HOME100K_DEVICE_SESSION_MODEL:-lifetime-subscription}"
runner_read_model="${HOME100K_RUNNER_READ_MODEL:-go-netpoll-bounded-reader-goroutine}"
functional_success_threshold_percent="${HOME100K_FUNCTIONAL_SUCCESS_THRESHOLD_PERCENT:-99.5}"
client_target_completeness_percent="${HOME100K_CLIENT_TARGET_COMPLETENESS_PERCENT:-100}"
exact_event_correlation_percent="${HOME100K_EXACT_EVENT_CORRELATION_PERCENT:-100}"
aggregate_correlation_tolerance_percent="${HOME100K_AGGREGATE_CORRELATION_TOLERANCE_PERCENT:-0.1}"
aggregate_correlation_min_tolerance="${HOME100K_AGGREGATE_CORRELATION_MIN_TOLERANCE:-5}"
coordinator_start_delay_ms="${HOME100K_COORDINATOR_START_DELAY_MS:-3000}"
credential_bundle_format="${HOME100K_CREDENTIAL_BUNDLE_FORMAT:-sqlite-gzip}"
mqtt_addr="${HOME100K_MQTT_ADDR:-}"
mqtt_public_lb_count="${HOME100K_MQTT_PUBLIC_LB_COUNT:-}"
video_cloud_public_url="${HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL:-${HOME100K_VIDEO_CLOUD_BASE_URL:-}}"
video_cloud_token_url="${HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL:-}"
account_manager_base_url="${HOME100K_ACCOUNT_MANAGER_BASE_URL:-}"
generator_hosts_override_ip="${HOME100K_GENERATOR_HOSTS_OVERRIDE_IP:-}"
video_loadtest="${HOME100K_VIDEO_LOADTEST:-auto}"
video_loadtest_script="${HOME100K_VIDEO_LOADTEST_SCRIPT:-$repo_root/e2e_test/video_cloud/load/scripts/run_video_loadtest.sh}"
video_loadtest_artifact_dir="${HOME100K_VIDEO_LOADTEST_ARTIFACT_DIR:-$repo_root/$out_dir/video}"
video_loadtest_viewers="${HOME100K_VIDEO_LOADTEST_VIEWERS:-100}"
video_loadtest_devices="${HOME100K_VIDEO_LOADTEST_DEVICES:-100}"
video_loadtest_concurrency="${HOME100K_VIDEO_LOADTEST_CONCURRENCY:-10}"
video_loadtest_media_set="${HOME100K_VIDEO_LOADTEST_WEBRTC_MEDIA_SET:-h264}"
video_loadtest_duration="${HOME100K_VIDEO_LOADTEST_DURATION:-30s}"
token_only_base_url="${HOME100K_TOKEN_ONLY_BASE_URL:-${video_cloud_token_url:-${video_cloud_public_url:-}}}"
token_only_requests="${HOME100K_TOKEN_ONLY_REQUESTS:-1000}"
token_only_concurrency="${HOME100K_TOKEN_ONLY_CONCURRENCY:-100}"
token_only_profile="${HOME100K_TOKEN_ONLY_PROFILE:-}"
token_only_timeout="${HOME100K_TOKEN_ONLY_TIMEOUT:-10s}"
token_only_cert_file="${HOME100K_TOKEN_ONLY_CERT_FILE:-}"
token_only_key_file="${HOME100K_TOKEN_ONLY_KEY_FILE:-}"
token_seed_redis_addr="${HOME100K_TOKEN_SEED_REDIS_ADDR:-}"
token_seed_device_prefix="${HOME100K_TOKEN_SEED_DEVICE_PREFIX:-load-device-}"
token_seed_ttl="${HOME100K_TOKEN_SEED_TTL:-24h}"
local_out_dir="$repo_root/$out_dir"
local_vm_state_file="$local_out_dir/vms.json"
status_file="$local_out_dir/.workflow-status"
nodes_file="$local_out_dir/nodes.tsv"
ssh_known_hosts_file="$local_out_dir/ssh_known_hosts"
resource_samples_dir="$local_out_dir/resource-samples"
load_vm_resource_file="$resource_samples_dir/load-vms.tsv"
k8s_node_resource_file="$resource_samples_dir/k8s-nodes.tsv"
workflow_status_log="$local_out_dir/workflow-status.log"
k8s_runtime_health_file="$resource_samples_dir/k8s-runtime-health.log"
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
  token-only              Run isolated /request_token load against an API base URL.
  seed-token-projections  Seed Redis device + entitlement projections for token-only.
  sync                    Review or live-sync runner/env-root to VMs.
  run-stages              Review or live-dispatch shard runners for the target window.
  collect                 Review or live-collect shard artifacts.
  run-video-loadtest      Run the workspace video load runner into <out-dir>/video.
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
  HOME100K_BRAND_PLAN     optional multi-brand load-test plan JSON
  HOME100K_SCENARIO_PROFILE optional scenario profile, e.g. video-1k-v1
  HOME100K_REGION         default: us-sea
  HOME100K_VM_LABEL_PREFIX default: lg; load-generator VM labels are <prefix>01..<prefix>NN
  HOME100K_RUN_ID         default: current UTC timestamp
  HOME100K_OUT_DIR        default: loadtests/home-100k/reports/<run-id>
  HOME100K_REMOTE_WORKSPACE default: /root/rtk_cloud_workspace
  HOME100K_REMOTE_ENV_ROOT  default: <remote-workspace>/<env-root>
  HOME100K_REMOTE_OUT_ROOT  default: /var/lib/home-100k
  HOME100K_SSH_USER         default: root
  HOME100K_SSH_KEY          default: ~/.ssh/id_ed25519_rtkcloud
  HOME100K_AUTHORIZED_KEY_FILE default: <HOME100K_SSH_KEY>.pub
  SSH known_hosts is isolated per run at <out-dir>/ssh_known_hosts
  HOME100K_STATUS_INTERVAL_SECONDS default: 30
  HOME100K_STAGE_WARM_UP default: 30s from the default description file
  HOME100K_STAGE_STEADY default: 90s from the default description file
  HOME100K_STAGE_COOL_DOWN default: 30s from the default description file
  HOME100K_DEVICES configured in the description file; current default description uses 9000
  HOME100K_USERS optional; when omitted, the planner derives users from devices/devices-per-user
  HOME100K_DEVICES_PER_USER configured in the description file; current default description uses 20
  HOME100K_LOAD_GENERATOR_DEVICES_PER_VM default: 20000; per-VM generator capacity used for automatic VM sizing
  HOME100K_VM_COUNT optional; default planner value is ceil(HOME100K_DEVICES / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM) mixed generator VMs
  HOME100K_RUNNER_MODE default: live; use sample only for local developer smoke tests
  HOME100K_RUNNER_NOFILE_LIMIT default: 1048576; remote daemon nofile limit for MQTT sockets
  HOME100K_MQTT_CONCURRENCY default: 1000 per VM shard; live MQTT connect worker concurrency
  HOME100K_COMMAND_CONCURRENCY default: 100 per VM shard; live shadow command concurrency
  HOME100K_SHADOW_COMMAND_TIMEOUT default: 30s; per-phase shadow command wait timeout
  HOME100K_LIVE_RUNNER_TIMEOUT_GRACE optional; defaults to max(10m, live duration / 4) before killing a shard runner
  HOME100K_DEVICE_SESSION_MODEL default: lifetime-subscription; device MQTT subscriptions stay open for device lifetime
  HOME100K_RUNNER_READ_MODEL default: go-netpoll-bounded-reader-goroutine; sustained async MQTT reads
  HOME100K_FUNCTIONAL_SUCCESS_THRESHOLD_PERCENT default: 99.5
  HOME100K_CLIENT_TARGET_COMPLETENESS_PERCENT default: 100
  HOME100K_EXACT_EVENT_CORRELATION_PERCENT default: 100
  HOME100K_AGGREGATE_CORRELATION_TOLERANCE_PERCENT default: 0.1
  HOME100K_AGGREGATE_CORRELATION_MIN_TOLERANCE default: 5
  HOME100K_COORDINATOR_START_DELAY_MS default: 3000
  HOME100K_CREDENTIAL_BUNDLE_FORMAT default: sqlite-gzip; only supported format
  HOME100K_MQTT_ADDR public MQTT endpoints for remote Linode generators; use auto-public-mqtt to discover mqtt-public* services
  HOME100K_MQTT_PUBLIC_LB_COUNT limits auto-public-mqtt endpoint count; current 9K profile uses 1
  HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL optional public Video Cloud API base URL for remote generators
  HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL optional mTLS/device Video Cloud token bootstrap base URL
  HOME100K_VIDEO_LOADTEST auto/off/on; auto runs for HOME100K_SCENARIO_PROFILE=video-1k-v1
  HOME100K_VIDEO_LOADTEST_VIEWERS default: 100
  HOME100K_VIDEO_LOADTEST_CONCURRENCY default: 10
  HOME100K_VIDEO_LOADTEST_WEBRTC_MEDIA_SET default: h264
  HOME100K_GENERATOR_HOSTS_OVERRIDE_IP optional /etc/hosts IPv4 override for staging HTTPS hostnames on generators
  HOME100K_TOKEN_ONLY_BASE_URL optional base URL for token-only; defaults to token/public Video Cloud URL
  HOME100K_TOKEN_ONLY_PROFILE optional comma-separated concurrency stages, e.g. 1000,5000,10000
  HOME100K_TOKEN_SEED_REDIS_ADDR Redis/Valkey address for seed-token-projections
  HOME100K_CLOUD_LOGGER_ENV optional cloud-logger env file for server evidence
  HOME100K_CLOUD_LOGGER_ENDPOINT optional explicit logger /v1/logs endpoint; useful with kubectl port-forward
  HOME100K_CLOUD_LOGGER_INGEST_TOKEN optional explicit logger query token; prefer env/root secret when available
  HOME100K_VIDEO_CLOUD_BASE_URL legacy alias for HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL
  HOME100K_ACCOUNT_MANAGER_BASE_URL optional Account Manager base URL for remote generators
  HOME100K_NODE_RESOURCE_STATUS default: 1
  HOME100K_K8S_NODE_RESOURCE_STATUS default: 1
  HOME100K_K8S_RUNTIME_HEALTH_STATUS default: 1 during run-stages; set 0 to disable API/EMQX health snapshots
  HOME100K_K8S_RUNTIME_HEALTH_SINCE default: 2m; log lookback window for API/EMQX error snapshots
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
  (cd "$repo_root" && GOWORK=auto go run ./loadtests/home-100k/cmd/home-100k -- "$@")
}

duration_millis() {
  python3 - "$1" <<'PY'
import re
import sys

raw = sys.argv[1].strip()
if not raw:
    raise SystemExit("empty duration")
units = {
    "h": 3600000,
    "m": 60000,
    "s": 1000,
    "ms": 1,
    "us": 0.001,
    "µs": 0.001,
    "ns": 0.000001,
}
pos = 0
total = 0.0
for match in re.finditer(r"([0-9]+(?:\.[0-9]+)?)(h|ms|us|µs|ns|m|s)", raw):
    if match.start() != pos:
        raise SystemExit(f"invalid duration {raw!r}")
    total += float(match.group(1)) * units[match.group(2)]
    pos = match.end()
if pos != len(raw) or total <= 0:
    raise SystemExit(f"invalid duration {raw!r}")
print(int(total))
PY
}

validate_stage_timing() {
  local stage_warm_up="${1:-}"
  local stage_steady="${2:-}"
  local stage_cool_down="${3:-}"
  local warm_ms steady_ms cool_ms full_load_ms
  if ! warm_ms="$(duration_millis "$stage_warm_up")"; then
    echo "invalid HOME100K_STAGE_WARM_UP: $stage_warm_up" >&2
    exit 2
  fi
  if ! steady_ms="$(duration_millis "$stage_steady")"; then
    echo "invalid HOME100K_STAGE_STEADY: $stage_steady" >&2
    exit 2
  fi
  if ! cool_ms="$(duration_millis "$stage_cool_down")"; then
    echo "invalid HOME100K_STAGE_COOL_DOWN: $stage_cool_down" >&2
    exit 2
  fi
  full_load_ms=$((steady_ms + cool_ms))
  if (( warm_ms >= full_load_ms )); then
    echo "HOME100K_STAGE_WARM_UP ($stage_warm_up) must be less than the full-load test window HOME100K_STAGE_STEADY + HOME100K_STAGE_COOL_DOWN ($stage_steady + $stage_cool_down)" >&2
    exit 2
  fi
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
  if [[ ! -f "$k8s_runtime_health_file" ]]; then
    printf '# Kubernetes runtime health snapshots for run_id=%s\n' "$run_id" > "$k8s_runtime_health_file"
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
  local vms_file="$local_vm_state_file"
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
  local env_root_kubeconfig
  case "$env_root" in
    /*) env_root_kubeconfig="$env_root/state/lke-kubeconfig.yaml" ;;
    *) env_root_kubeconfig="$repo_root/$env_root/state/lke-kubeconfig.yaml" ;;
  esac
  for candidate in \
    "${HOME100K_KUBECONFIG:-}" \
    "${RTK_CLOUD_LKE_KUBECONFIG:-}" \
    "${LKE_KUBECONFIG:-}" \
    "${CLOUD_STAGING_K8S_KUBECONFIG:-}" \
    "$env_root_kubeconfig"; do
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

k8s_runtime_health_status() {
  if [[ "${HOME100K_K8S_RUNTIME_HEALTH_STATUS:-1}" == "0" ]]; then
    return
  fi
  local phase
  phase="starting"
  if [[ -f "$status_file" ]]; then
    phase="$(cat "$status_file")"
  fi
  if [[ "$phase" != "run-stages" ]]; then
    return
  fi
  ensure_resource_logs
  local kubectl_bin="${HOME100K_KUBECTL:-${RTK_CLOUD_KUBECTL:-kubectl}}"
  local since="${HOME100K_K8S_RUNTIME_HEALTH_SINCE:-2m}"
  local now kubeconfig ns
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  ns="video-cloud-staging-video-cloud"
  if ! command -v "$kubectl_bin" >/dev/null 2>&1; then
    printf '[home-100k k8s-health] unavailable reason=kubectl-not-found\n' >&2
    printf '\n## %s phase=%s\nunavailable: kubectl-not-found\n' "$now" "$phase" >> "$k8s_runtime_health_file"
    return
  fi
  kubeconfig="$(k8s_kubeconfig || true)"
  local kubectl_prefix=()
  if [[ -n "$kubeconfig" ]]; then
    kubectl_prefix=(env "KUBECONFIG=$kubeconfig" "$kubectl_bin")
  else
    kubectl_prefix=("$kubectl_bin")
  fi

  local top_output api_errors emqx_errors listener_output mqtt_pods pod
  top_output="$("${kubectl_prefix[@]}" top pods -A --containers --request-timeout=5s 2>&1 | grep -E 'mqtt|video-cloud-api|postgres|redis|ingress' || true)"
  api_errors="$("${kubectl_prefix[@]}" -n "$ns" logs deploy/video-cloud-api --since="$since" --tail=300 2>&1 | grep -Ei 'broken pipe|connection reset|socket_error|congest|timeout' || true)"
  mqtt_pods="$("${kubectl_prefix[@]}" -n "$ns" get pods --selector app.kubernetes.io/name=mqtt -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  listener_output=""
  emqx_errors=""
  while IFS= read -r pod; do
    [[ -n "$pod" ]] || continue
    listener_output+="$(
      "${kubectl_prefix[@]}" -n "$ns" exec "$pod" -- emqx ctl listeners 2>/dev/null |
        awk -v pod="$pod" '/ssl:default/{flag=1} flag && /current_conn|max_conns|shutdown_count/{gsub(/^ +/,""); printf "%s %s; ", pod, $0} flag && /ws:default/{flag=0} END{print ""}'
    )"
    listener_output+=$'\n'
    emqx_errors+="$(
      "${kubectl_prefix[@]}" -n "$ns" logs "$pod" --since="$since" --tail=200 2>&1 |
        grep -Ei 'video-cloud-api|congest|socket_error|timeout|broken|reset' |
        sed "s/^/$pod /" || true
    )"
    emqx_errors+=$'\n'
  done <<< "$mqtt_pods"

  {
    printf '\n## %s phase=%s since=%s\n' "$now" "$phase" "$since"
    printf '\n### top pods\n%s\n' "${top_output:-none}"
    printf '\n### emqx listeners\n%s\n' "${listener_output:-none}"
    printf '\n### api errors\n%s\n' "${api_errors:-none}"
    printf '\n### emqx errors\n%s\n' "${emqx_errors:-none}"
  } >> "$k8s_runtime_health_file"

  local api_error_count emqx_error_count
  api_error_count="$(printf '%s\n' "$api_errors" | awk 'NF {count++} END {print count+0}')"
  emqx_error_count="$(printf '%s\n' "$emqx_errors" | awk 'NF {count++} END {print count+0}')"
  printf '[home-100k k8s-health] phase=%s since=%s api_errors=%s emqx_errors=%s log=%s\n' "$phase" "$since" "$api_error_count" "$emqx_error_count" "$k8s_runtime_health_file" >&2
  if [[ -n "$listener_output" ]]; then
    printf '%s\n' "$listener_output" | sed '/^[[:space:]]*$/d; s/^/[home-100k k8s-health] listener /' >&2
  fi
  if [[ "$api_error_count" != "0" ]]; then
    printf '%s\n' "$api_errors" | tail -20 | sed 's/^/[home-100k k8s-health] api-error /' >&2
  fi
  if [[ "$emqx_error_count" != "0" ]]; then
    printf '%s\n' "$emqx_errors" | tail -20 | sed 's/^/[home-100k k8s-health] emqx-error /' >&2
  fi
}

workflow_status() {
  local now elapsed phase vm_count shard_count report_status evidence_status expected_vm_count
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  elapsed="$(($(date +%s) - workflow_start_epoch))s"
  phase="starting"
  if [[ -f "$status_file" ]]; then
    phase="$(cat "$status_file")"
  fi
  vm_count="0"
  if [[ -f "$local_vm_state_file" ]]; then
    write_nodes_file
    vm_count="$(python3 - "$local_vm_state_file" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    data = json.load(f)
vms = data.get("created") or data.get("vms") or []
print(len({vm.get("label", "") for vm in vms if vm.get("label")}))
PY
)"
  fi
  shard_count="0"
  if [[ -d "$local_out_dir/shards" ]]; then
    shard_count="$(find "$local_out_dir/shards" -name results.json -type f | wc -l | tr -d ' ')"
  fi
  report_status="not-written"
  if [[ -f "$local_out_dir/TEST_REPORT.md" ]]; then
    report_status="$( (grep -m1 '^- Status:' "$local_out_dir/TEST_REPORT.md" || true) | sed 's/^- Status: //')"
    if [[ -z "$report_status" ]]; then
      report_status="unknown"
    fi
  fi
  evidence_status="not-written"
  if [[ -f "$local_out_dir/server-evidence.json" ]]; then
    if grep -q '"complete"[[:space:]]*:[[:space:]]*true' "$local_out_dir/server-evidence.json"; then
      evidence_status="complete"
    else
      evidence_status="incomplete"
    fi
  fi
  expected_vm_count="${HOME100K_VM_COUNT:-}"
  if [[ -z "$expected_vm_count" && "${device_count:-}" =~ ^[0-9]+$ && "$device_count" -gt 0 ]]; then
    expected_vm_count="$(( (device_count + load_generator_devices_per_vm - 1) / load_generator_devices_per_vm ))"
  fi
  expected_vm_count="${expected_vm_count:-5}"
  {
    printf '[home-100k status] time=%s elapsed=%s run_id=%s phase=%s\n' "$now" "$elapsed" "$run_id" "$phase"
    printf '[home-100k status] out_dir=%s vms=%s/%s shard_results=%s server_evidence=%s report_status=%s target_connects=%s target_users=%s devices_per_user=%s ramp_up_time=%s command_concurrency=%s shadow_command_timeout=%s\n' "$out_dir" "$vm_count" "$expected_vm_count" "$shard_count" "$evidence_status" "$report_status" "${device_count:-planner-default}" "${user_count:-derived}" "${devices_per_user:-planner-default}" "$stage_warm_up" "$command_concurrency" "$shadow_command_timeout"
  } >&2
  ensure_resource_logs
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$now" "$run_id" "$elapsed" "$phase" "$vm_count" "$shard_count" "$evidence_status" "$report_status" >> "$workflow_status_log"
  node_resource_status
  k8s_node_resource_status
  k8s_runtime_health_status
}

current_report_status() {
  local report="$local_out_dir/TEST_REPORT.md"
  local status=""
  if [[ -f "$report" ]]; then
    status="$( (grep -m1 '^- Status:' "$report" || true) | sed 's/^- Status: //')"
  fi
  printf '%s\n' "${status:-not-written}"
}

current_report_result() {
  local report="$local_out_dir/TEST_REPORT.md"
  local result=""
  if [[ -f "$report" ]]; then
    result="$( (grep -m1 '^- Result:' "$report" || true) | sed 's/^- Result: //')"
  fi
  printf '%s\n' "${result:-not-written}"
}

video_loadtest_enabled() {
  case "$video_loadtest" in
    1|true|on|yes)
      return 0
      ;;
    0|false|off|no)
      return 1
      ;;
    auto|"")
      [[ "$scenario_profile" == "video-1k-v1" ]]
      ;;
    *)
      echo "invalid HOME100K_VIDEO_LOADTEST: $video_loadtest" >&2
      return 2
      ;;
  esac
}

run_video_loadtest_step() {
  if ! video_loadtest_enabled; then
    return 0
  fi
  if [[ ! -x "$video_loadtest_script" ]]; then
    echo "video loadtest script not executable: $video_loadtest_script" >&2
    return 1
  fi
  mkdir -p "$video_loadtest_artifact_dir"
  set_phase "run-video-loadtest"
  local token_env="$video_loadtest_artifact_dir/token-env.sh"
  if [[ -z "${VIDEO_CLOUD_LOAD_DEVICE_IDS:-}" ]] || \
     [[ -z "${VIDEO_CLOUD_LOAD_DEVICE_TOKENS:-}" && -z "${VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE:-}" ]] || \
     [[ -z "${VIDEO_CLOUD_LOAD_APP_TOKENS:-}" && -z "${VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE:-}" ]]; then
    (cd "$repo_root/scripts/go/rtk-cloud" && GOWORK=off go run . video-loadtest-tokens \
      --env-root "$repo_root/$env_root" \
      --brandname "$brandname" \
      --max-devices "$video_loadtest_devices" \
      --out-env "$token_env")
    # shellcheck disable=SC1090
    source "$token_env"
  fi
  VIDEO_CLOUD_LOAD_RUN_ID="${VIDEO_CLOUD_LOAD_RUN_ID:-$run_id}" \
  VIDEO_CLOUD_LOAD_ARTIFACT_DIR="$video_loadtest_artifact_dir" \
  VIDEO_CLOUD_LOAD_PROFILE="${VIDEO_CLOUD_LOAD_PROFILE:-safe-staging}" \
  VIDEO_CLOUD_LOAD_ACTORS="${VIDEO_CLOUD_LOAD_ACTORS:-device,viewer}" \
  VIDEO_CLOUD_LOAD_APP_ROUTE_SET="${VIDEO_CLOUD_LOAD_APP_ROUTE_SET:-smoke}" \
  VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET="${VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET:-off}" \
  VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET="${VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET:-smoke}" \
  VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET="${VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET:-smoke}" \
  VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET="${VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET:-$video_loadtest_media_set}" \
  VIDEO_CLOUD_LOAD_DURATION="${VIDEO_CLOUD_LOAD_DURATION:-$video_loadtest_duration}" \
  VIDEO_CLOUD_LOAD_HTTP_TIMEOUT="${VIDEO_CLOUD_LOAD_HTTP_TIMEOUT:-60s}" \
  VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES="${VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES:-$video_loadtest_devices}" \
  VIDEO_CLOUD_LOAD_VIRTUAL_VIEWERS="${VIDEO_CLOUD_LOAD_VIRTUAL_VIEWERS:-$video_loadtest_viewers}" \
  VIDEO_CLOUD_LOAD_DEVICE_CONCURRENCY="${VIDEO_CLOUD_LOAD_DEVICE_CONCURRENCY:-$video_loadtest_concurrency}" \
  VIDEO_CLOUD_LOAD_VIEWER_CONCURRENCY="${VIDEO_CLOUD_LOAD_VIEWER_CONCURRENCY:-$video_loadtest_concurrency}" \
  VIDEO_CLOUD_LOAD_API_URL="${VIDEO_CLOUD_LOAD_API_URL:-$video_cloud_public_url}" \
  "$video_loadtest_script"
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
  local vms_file="$local_vm_state_file"
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
  [[ "$(current_report_status)" == "COMPLETE" && "$(current_report_result)" == "SUCCESS" ]]
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
validate_stage_timing "$stage_warm_up" "$stage_steady" "$stage_cool_down"
resolve_mqtt_addr_for_command "$command"

base_args=(
  "--env-root" "$env_root"
  "--brandname" "$brandname"
  "--region" "$region"
  "--vm-label-prefix" "$vm_label_prefix"
  "--stage-warm-up" "$stage_warm_up"
  "--stage-steady" "$stage_steady"
  "--stage-cool-down" "$stage_cool_down"
  "--functional-success-threshold-percent" "$functional_success_threshold_percent"
  "--client-target-completeness-percent" "$client_target_completeness_percent"
  "--exact-event-correlation-percent" "$exact_event_correlation_percent"
  "--aggregate-correlation-tolerance-percent" "$aggregate_correlation_tolerance_percent"
  "--aggregate-correlation-min-tolerance" "$aggregate_correlation_min_tolerance"
)
if [[ -n "$brand_plan" ]]; then
  base_args+=("--brand-plan" "$brand_plan")
fi
if [[ -n "$scenario_profile" ]]; then
  base_args+=("--scenario-profile" "$scenario_profile")
fi
if [[ -n "$device_count" ]]; then
  base_args+=("--devices" "$device_count")
fi
if [[ -n "$user_count" ]]; then
  base_args+=("--users" "$user_count")
fi
if [[ -n "$devices_per_user" ]]; then
  base_args+=("--devices-per-user" "$devices_per_user")
fi
if [[ -n "$vm_count" ]]; then
  base_args+=("--vm-count" "$vm_count")
fi
base_args+=("--load-generator-devices-per-vm" "$load_generator_devices_per_vm")
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
workflow_args+=("--mqtt-concurrency" "$mqtt_concurrency")
workflow_args+=("--command-concurrency" "$command_concurrency")
workflow_args+=("--shadow-command-timeout" "$shadow_command_timeout")
workflow_args+=("--live-runner-timeout-grace" "$live_runner_timeout_grace")
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
if [[ -n "$generator_hosts_override_ip" ]]; then
  workflow_args+=("--generator-hosts-override-ip" "$generator_hosts_override_ip")
fi

case "$command" in
  plan)
    run_home100k plan "${plan_condition_args[@]}" "$@"
    ;;
  dry-run)
    mkdir -p "$local_out_dir"
    run_home100k run "${plan_condition_args[@]}" --ephemeral-vms --run-id "$run_id" --out-dir "$local_out_dir" "$@"
    ;;
  provision-vms)
    mkdir -p "$local_out_dir"
    run_home100k provision-vms "${base_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" "$@"
    ;;
  token-only)
    mkdir -p "$repo_root/$out_dir"
    token_only_args=(
      "--base-url" "$token_only_base_url"
      "--out-dir" "$out_dir"
      "--requests" "$token_only_requests"
      "--concurrency" "$token_only_concurrency"
      "--timeout" "$token_only_timeout"
    )
    if [[ -n "$token_only_profile" ]]; then
      token_only_args+=("--profile" "$token_only_profile")
    fi
    if [[ -n "$token_only_cert_file" || -n "$token_only_key_file" ]]; then
      token_only_args+=("--cert-file" "$token_only_cert_file" "--key-file" "$token_only_key_file")
    fi
    run_home100k token-only "${token_only_args[@]}" "$@"
    ;;
  seed-token-projections)
    seed_args=(
      "--redis-addr" "$token_seed_redis_addr"
      "--devices" "${device_count:-1000}"
      "--device-prefix" "$token_seed_device_prefix"
      "--ttl" "$token_seed_ttl"
    )
    run_home100k seed-token-projections "${seed_args[@]}" "$@"
    ;;
  sync)
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --vm-state-file "$local_vm_state_file" "$@"
    ;;
  run-stages)
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --runner-mode "$runner_mode" "$@"
    ;;
  collect)
    mkdir -p "$local_out_dir"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" "$@"
    ;;
  run-video-loadtest)
    run_video_loadtest_step "$@"
    ;;
  collect-server-evidence)
    mkdir -p "$local_out_dir"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" "$@"
    ;;
  aggregate)
    mkdir -p "$local_out_dir"
    run_home100k aggregate "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" "$@"
    generate_report_from_artifacts
    ;;
  generate-report)
    mkdir -p "$local_out_dir"
    generate_report_from_artifacts
    ;;
  list-vms)
    run_home100k list-vms "${base_args[@]}" --run-id "$run_id" "$@"
    ;;
  destroy-vms)
    run_home100k destroy-vms "${base_args[@]}" --run-id "$run_id" --vm-state-file "$local_vm_state_file" "$@"
    ;;
  shutdown-vms)
    shutdown_live_vms
    ;;
  workflow-dry-run)
    run_home100k plan "${plan_condition_args[@]}"
    run_home100k provision-vms "${base_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir"
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --vm-state-file "$local_vm_state_file"
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --vm-state-file "$local_vm_state_file" --runner-mode sample
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir"
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
    mkdir -p "$local_out_dir"
    rm -f "$ssh_known_hosts_file"
    shutdown_live_vms_on_exit=1
    start_status_monitor
    set_phase "provision-vms"
    run_home100k provision-vms "${base_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --live --confirm-live --authorized-key-file "$authorized_key_file" "$@"
    workflow_status
    set_phase "sync"
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-baseline"
    export_kubeconfig_if_available
    rm -f "$local_out_dir/server-evidence-baseline.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --server-evidence-file "$local_out_dir/server-evidence-baseline.json" --live
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    run_video_loadtest_step || workflow_rc=$?
    workflow_status
    set_phase "collect-server-evidence"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --live
    workflow_status
    set_phase "aggregate"
    run_home100k aggregate "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir"
    generate_report_from_artifacts
    report_status="$(current_report_status)"
    report_result="$(current_report_result)"
    if [[ "$workflow_rc" -eq 0 && ( "$report_status" != "COMPLETE" || "$report_result" != "SUCCESS" ) ]]; then
      workflow_rc=1
      echo "report status is $report_status result is $report_result; preserving VMs for investigation" >&2
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
    if [[ ! -f "$local_vm_state_file" ]]; then
      echo "workflow-resume-live requires existing VM state: $out_dir/vms.json" >&2
      exit 2
    fi
    mkdir -p "$local_out_dir"
    shutdown_live_vms_on_exit=1
    start_status_monitor
    set_phase "sync"
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-baseline"
    export_kubeconfig_if_available
    rm -f "$local_out_dir/server-evidence-baseline.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --server-evidence-file "$local_out_dir/server-evidence-baseline.json" --live
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    run_video_loadtest_step || workflow_rc=$?
    workflow_status
    set_phase "collect-server-evidence"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --live
    workflow_status
    set_phase "aggregate"
    run_home100k aggregate "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir"
    generate_report_from_artifacts
    report_status="$(current_report_status)"
    report_result="$(current_report_result)"
    if [[ "$workflow_rc" -eq 0 && ( "$report_status" != "COMPLETE" || "$report_result" != "SUCCESS" ) ]]; then
      workflow_rc=1
      echo "report status is $report_status result is $report_result; preserving VMs for investigation" >&2
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
