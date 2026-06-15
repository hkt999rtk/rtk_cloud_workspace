#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
description_file="${HOME100K_DESCRIPTION_FILE:-$repo_root/loadtests/home-100k/scenarios/default.description.env}"

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

preexisting_linode_token="${LINODE_TOKEN:-}"
if [[ -f "$description_file" ]]; then
  set -a
  # shellcheck source=/dev/null
  source "$description_file"
  set +a
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
runner_mode="${HOME100K_RUNNER_MODE:-live}"
status_file="$repo_root/$out_dir/.workflow-status"
nodes_file="$repo_root/$out_dir/nodes.tsv"
ssh_known_hosts_file="$repo_root/$out_dir/ssh_known_hosts"
resource_samples_dir="$repo_root/$out_dir/resource-samples"
load_vm_resource_file="$resource_samples_dir/load-vms.tsv"
k8s_node_resource_file="$resource_samples_dir/k8s-nodes.tsv"
workflow_status_log="$repo_root/$out_dir/workflow-status.log"
cleanup_live_vms_on_exit=0

usage() {
  cat <<EOF
usage: $(basename "$0") [command] [home-100k flags]

Default command:
  workflow-live           Create VMs and run the live lifecycle through aggregate.

Commands:
  plan                    Print the deterministic 100K/5K/10-VM mixed run plan.
  dry-run                 Render local review artifacts; does not create VMs.
  provision-vms           Review or live-create Linode VMs.
  sync                    Review or live-sync runner/env-root to VMs.
  run-stages              Review or live-dispatch shard runners.
  collect                 Review or live-collect shard artifacts.
  collect-server-evidence Review or live-collect server evidence.
  aggregate               Aggregate collected shards and server evidence.
  generate-report         Generate TEST_REPORT.md from collected artifacts and template.
  list-vms                Review or live-list leftover VMs by run id.
  destroy-vms             Review or live-destroy VMs by state file.
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
  HOME100K_STAGE_WARM_UP default: 1m
  HOME100K_STAGE_STEADY default: 2m
  HOME100K_STAGE_COOL_DOWN default: 45s
  HOME100K_RUNNER_MODE default: live; use sample only for local developer smoke tests
  HOME100K_NODE_RESOURCE_STATUS default: 1
  HOME100K_K8S_NODE_RESOURCE_STATUS default: 1
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
    vm_count="$( (grep -o '"label"' "$repo_root/$out_dir/vms.json" || true) | wc -l | tr -d ' ')"
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
    printf '[home-100k status] out_dir=%s vms=%s/10 shard_results=%s server_evidence=%s report_status=%s stage_window=warm_up:%s,steady:%s,cool_down:%s\n' "$out_dir" "$vm_count" "$shard_count" "$evidence_status" "$report_status" "$stage_warm_up" "$stage_steady" "$stage_cool_down"
  } >&2
  ensure_resource_logs
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$now" "$run_id" "$elapsed" "$phase" "$vm_count" "$shard_count" "$evidence_status" "$report_status" >> "$workflow_status_log"
  node_resource_status
  k8s_node_resource_status
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

destroy_live_vms() {
  if [[ -f "$repo_root/$out_dir/vms.json" ]]; then
    run_home100k destroy-vms "${common_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json" --live --confirm-live
  fi
}

on_script_exit() {
  local rc="${1:-0}"
  if [[ -n "${status_monitor_pid:-}" ]]; then
    kill "$status_monitor_pid" 2>/dev/null || true
    wait "$status_monitor_pid" 2>/dev/null || true
  fi
  if [[ "$cleanup_live_vms_on_exit" == "1" ]]; then
    set_phase "destroy-vms"
    destroy_live_vms >/dev/null 2>&1 || true
  fi
  exit "$rc"
}

common_args=(
  "--env-root" "$env_root"
  "--brandname" "$brandname"
  "--region" "$region"
  "--stage-warm-up" "$stage_warm_up"
  "--stage-steady" "$stage_steady"
  "--stage-cool-down" "$stage_cool_down"
)

command="${1:-workflow-live}"
if [[ "$command" == "-h" || "$command" == "--help" ]]; then
  usage
  exit 0
fi
if [[ "$#" -gt 0 ]]; then
  shift
fi

case "$command" in
  plan)
    run_home100k plan "${common_args[@]}" "$@"
    ;;
  dry-run)
    mkdir -p "$repo_root/$out_dir"
    run_home100k run "${common_args[@]}" --ephemeral-vms --run-id "$run_id" --out-dir "$out_dir" "$@"
    ;;
  provision-vms)
    mkdir -p "$repo_root/$out_dir"
    run_home100k provision-vms "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" "$@"
    ;;
  sync)
    run_home100k sync "${common_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json" "$@"
    ;;
  run-stages)
    run_home100k run-stages "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --runner-mode "$runner_mode" "$@"
    ;;
  collect)
    mkdir -p "$repo_root/$out_dir"
    run_home100k collect "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" "$@"
    ;;
  collect-server-evidence)
    mkdir -p "$repo_root/$out_dir"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" "$@"
    ;;
  aggregate)
    mkdir -p "$repo_root/$out_dir"
    run_home100k aggregate "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" "$@"
    generate_report_from_artifacts
    ;;
  generate-report)
    mkdir -p "$repo_root/$out_dir"
    generate_report_from_artifacts
    ;;
  list-vms)
    run_home100k list-vms "${common_args[@]}" --run-id "$run_id" "$@"
    ;;
  destroy-vms)
    run_home100k destroy-vms "${common_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json" "$@"
    ;;
  workflow-dry-run)
    run_home100k plan "${common_args[@]}"
    run_home100k provision-vms "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
    run_home100k sync "${common_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json"
    run_home100k run-stages "${common_args[@]}" --run-id "$run_id" --vm-state-file "$out_dir/vms.json" --runner-mode sample
    run_home100k collect "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json"
    run_home100k collect-server-evidence "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
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
    cleanup_live_vms_on_exit=1
    start_status_monitor
    set_phase "provision-vms"
    run_home100k provision-vms "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --live --confirm-live --authorized-key-file "$authorized_key_file" "$@"
    workflow_status
    set_phase "sync"
    run_home100k sync "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    run_home100k run-stages "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-evidence"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --live
    workflow_status
    set_phase "aggregate"
    run_home100k aggregate "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
    generate_report_from_artifacts
    set_phase "destroy-vms"
    cleanup_rc=0
    destroy_live_vms || cleanup_rc=$?
    cleanup_live_vms_on_exit=0
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
    if [[ ! -f "$repo_root/$out_dir/vms.json" ]]; then
      echo "workflow-resume-live requires existing VM state: $out_dir/vms.json" >&2
      exit 2
    fi
    mkdir -p "$repo_root/$out_dir"
    write_nodes_file
    cleanup_live_vms_on_exit=1
    start_status_monitor
    set_phase "sync"
    run_home100k sync "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    run_home100k run-stages "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --vm-state-file "$out_dir/vms.json" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    workflow_status
    set_phase "collect-server-evidence"
    export_kubeconfig_if_available
    run_home100k collect-server-evidence "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir" --live
    workflow_status
    set_phase "aggregate"
    run_home100k aggregate "${common_args[@]}" --run-id "$run_id" --out-dir "$out_dir"
    generate_report_from_artifacts
    set_phase "destroy-vms"
    cleanup_rc=0
    destroy_live_vms || cleanup_rc=$?
    cleanup_live_vms_on_exit=0
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
