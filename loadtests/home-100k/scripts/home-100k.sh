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

environment="${HOME100K_ENVIRONMENT:-staging}"
environment_root="${HOME100K_ENVIRONMENT_ROOT:-cloud_env/${environment}}"
if [[ "$environment_root" == */staging/lke || "$environment_root" == cloud_env/staging/lke ]]; then
  echo "legacy provider env-root is not supported; use HOME100K_ENVIRONMENT=staging" >&2
  exit 2
fi
env_root="${HOME100K_ENV_ROOT:-${environment_root}/runtime}"
brandname="${HOME100K_BRANDNAME:-RTK}"
brand_plan="${HOME100K_BRAND_PLAN:-}"
if [[ -n "$brand_plan" && "$brand_plan" != /* ]]; then
  brand_plan="$repo_root/$brand_plan"
fi
scenario_profile="${HOME100K_SCENARIO_PROFILE:-}"
region="${HOME100K_REGION:-}"
if [[ -z "$region" ]]; then
  case "$env_root" in
    /*) provider_preflight_file="$env_root/state/provider-preflight.env" ;;
    *) provider_preflight_file="$repo_root/$env_root/state/provider-preflight.env" ;;
  esac
  if [[ -f "$provider_preflight_file" ]]; then
    region="$(awk -F= '$1 == "PROVIDER_REGION" {print $2; exit}' "$provider_preflight_file")"
  fi
fi
if [[ -z "$region" ]]; then
  echo "provider region is unresolved; run rtk-cloud deployment plan --environment $environment" >&2
  exit 2
fi
linode_type="${HOME100K_LINODE_TYPE:-}"
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
video_generator_vm_count="${HOME100K_VIDEO_GENERATOR_VM_COUNT:-}"
video_generator_label_prefix="${HOME100K_VIDEO_GENERATOR_LABEL_PREFIX:-vg}"
runner_nofile_limit="${HOME100K_RUNNER_NOFILE_LIMIT:-1048576}"
mqtt_concurrency="${HOME100K_MQTT_CONCURRENCY:-1000}"
command_concurrency="${HOME100K_COMMAND_CONCURRENCY:-100}"
shadow_command_timeout="${HOME100K_SHADOW_COMMAND_TIMEOUT:-30s}"
device_token_request_timeout="${HOME100K_DEVICE_TOKEN_REQUEST_TIMEOUT:-10s}"
device_token_request_retries="${HOME100K_DEVICE_TOKEN_REQUEST_RETRIES:-0}"
runtime_logs="${HOME100K_RUNTIME_LOGS:-true}"
if [[ "$runtime_logs" != "true" && "$runtime_logs" != "TRUE" && "$runtime_logs" != "1" ]]; then
  case "$scenario_profile" in
    video-50k-turn-v1|video-100k-turn-v1)
      echo "HOME100K_RUNTIME_LOGS must be true for $scenario_profile; TURN sizing reports require run-scoped shadow runtime evidence." >&2
      exit 2
      ;;
  esac
fi
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
video_loadtest_mode="${HOME100K_VIDEO_LOADTEST_MODE:-local}"
video_loadtest_shard_mode="${HOME100K_VIDEO_LOADTEST_SHARD_MODE:-}"
default_video_loadtest_binary="$repo_root/.artifacts/e2e_test/video_cloud/load/cd/rtk-video-loadtest-linux-amd64"
video_loadtest_binary="${HOME100K_VIDEO_LOADTEST_BINARY:-$default_video_loadtest_binary}"
video_loadtest_rebuild="${HOME100K_VIDEO_LOADTEST_REBUILD:-auto}"
video_loadtest_remote_dir="${HOME100K_VIDEO_LOADTEST_REMOTE_DIR:-/opt/rtk-video-loadtest}"
video_loadtest_remote_hosts="${HOME100K_VIDEO_LOADTEST_REMOTE_HOSTS:-}"
video_loadtest_artifact_dir="${HOME100K_VIDEO_LOADTEST_ARTIFACT_DIR:-$repo_root/$out_dir/video}"
video_loadtest_brandname="${HOME100K_VIDEO_LOADTEST_BRANDNAME:-$brandname}"
video_loadtest_viewers="${HOME100K_VIDEO_LOADTEST_VIEWERS:-100}"
video_loadtest_devices="${HOME100K_VIDEO_LOADTEST_DEVICES:-100}"
video_loadtest_concurrency="${HOME100K_VIDEO_LOADTEST_CONCURRENCY:-}"
video_loadtest_max_viewers_per_host="${HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST:-}"
video_loadtest_ladder="${HOME100K_VIDEO_LOADTEST_LADDER:-}"
video_loadtest_step_cooldown="${HOME100K_VIDEO_LOADTEST_STEP_COOLDOWN:-0s}"
video_loadtest_turn_sample_interval="${HOME100K_VIDEO_LOADTEST_TURN_SAMPLE_INTERVAL_SECONDS:-5}"
video_loadtest_token_concurrency="${HOME100K_VIDEO_LOADTEST_TOKEN_CONCURRENCY:-32}"
video_loadtest_token_expiry_seconds="${HOME100K_VIDEO_LOADTEST_TOKEN_EXPIRY_SECONDS:-1800}"
video_loadtest_token_request_timeout="${HOME100K_VIDEO_LOADTEST_TOKEN_REQUEST_TIMEOUT:-30s}"
video_loadtest_media_set="${HOME100K_VIDEO_LOADTEST_WEBRTC_MEDIA_SET:-h264}"
video_loadtest_ice_policy="${HOME100K_VIDEO_LOADTEST_WEBRTC_ICE_POLICY:-relay}"
video_loadtest_duration="${HOME100K_VIDEO_LOADTEST_DURATION:-30s}"
video_loadtest_media_duration="${HOME100K_VIDEO_LOADTEST_MEDIA_DURATION:-20s}"
video_loadtest_device_online_settle="${HOME100K_VIDEO_LOADTEST_DEVICE_ONLINE_SETTLE:-}"
clip_storage_loadtest="${HOME100K_CLIP_STORAGE_LOADTEST:-auto}"
clip_storage_loadtest_script="${HOME100K_CLIP_STORAGE_LOADTEST_SCRIPT:-$repo_root/e2e_test/video_cloud/load/scripts/run_clip_storage_loadtest.sh}"
clip_storage_loadtest_artifact_dir="${HOME100K_CLIP_STORAGE_LOADTEST_ARTIFACT_DIR:-$repo_root/$out_dir/clip-storage}"
clip_storage_camera_ids_file="${HOME100K_CLIP_STORAGE_CAMERA_IDS_FILE:-}"
clip_storage_token_map_file="${HOME100K_CLIP_STORAGE_TOKEN_MAP_FILE:-}"
clip_storage_fixture="${HOME100K_CLIP_STORAGE_FIXTURE:-$repo_root/e2e_test/video_cloud/load/testdata/clip_1080p_h264_3mbps_15s.mp4}"
clip_storage_thumbnail="${HOME100K_CLIP_STORAGE_THUMBNAIL:-$repo_root/e2e_test/video_cloud/load/testdata/thumbnail_1080p.jpg}"
clip_storage_count_per_camera="${HOME100K_CLIP_STORAGE_CLIPS_PER_CAMERA:-10}"
clip_storage_window="${HOME100K_CLIP_STORAGE_WINDOW:-30m}"
clip_storage_seed="${HOME100K_CLIP_STORAGE_POISSON_SEED:-20260719}"
clip_storage_concurrency="${HOME100K_CLIP_STORAGE_UPLOAD_CONCURRENCY:-64}"
if [[ -z "$video_loadtest_shard_mode" ]]; then
  case "$scenario_profile:$video_loadtest_mode" in
    video-50k-turn-v1:remote-sharded|video-100k-turn-v1:remote-sharded)
      video_loadtest_shard_mode="proportional"
      ;;
    *)
      video_loadtest_shard_mode="global"
      ;;
  esac
fi
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
existing_generator_hosts="${HOME100K_EXISTING_GENERATOR_HOSTS:-}"
provider_active_service_limit=""
if [[ "$out_dir" == /* ]]; then
  local_out_dir="$out_dir"
else
  local_out_dir="$repo_root/$out_dir"
fi
if [[ -z "${HOME100K_VIDEO_LOADTEST_ARTIFACT_DIR:-}" ]]; then
  video_loadtest_artifact_dir="$local_out_dir/video"
fi
if [[ -z "${HOME100K_CLIP_STORAGE_LOADTEST_ARTIFACT_DIR:-}" ]]; then
  clip_storage_loadtest_artifact_dir="$local_out_dir/clip-storage"
fi
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
auto_destroy_on_exit="${HOME100K_AUTO_DESTROY_ON_EXIT:-1}"
preserve_vms="${HOME100K_PRESERVE_VMS:-0}"
single_device_smoke="${HOME100K_SINGLE_DEVICE_SMOKE:-1}"

usage() {
  cat <<EOF
usage: $(basename "$0") [command] [home-100k flags]

Default command:
  workflow-live           Create VMs and run the live lifecycle through aggregate.

Commands:
  plan                    Print the deterministic configured-size mixed run plan.
  preflight               Validate fixture inventory and current client CA before VM creation.
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
  workflow-video-live     Run only the live WebRTC video lifecycle; skips MQTT/shadow.
  workflow-video-resume-live
                          Resume only the live WebRTC video lifecycle; skips MQTT/shadow.

Defaults can be overridden with:
  HOME100K_DESCRIPTION_FILE default: loadtests/home-100k/scenarios/default.description.env
  HOME100K_SECRET_ENV_FILE  default: ~/.env; only LINODE_TOKEN is read
  HOME100K_ENVIRONMENT    default: staging; selects cloud_env/<environment>
  HOME100K_ENV_ROOT       internal runtime override; default: cloud_env/<environment>/runtime
  HOME100K_BRANDNAME      default: RTK
  HOME100K_BRAND_PLAN     optional multi-brand load-test plan JSON
  HOME100K_SCENARIO_PROFILE optional scenario profile, e.g. video-1k-v1, video-50k-turn-v1, video-100k-turn-v1
  HOME100K_REGION         explicit test-only provider region override; normally resolved from environment
  HOME100K_LINODE_TYPE    optional Linode VM type for load generators, passed to provision-vms
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
  HOME100K_VIDEO_GENERATOR_VM_COUNT optional extra video-only VMs; not the default for proportional TURN sizing
  HOME100K_VIDEO_GENERATOR_LABEL_PREFIX default: vg
  HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST optional safety gate; in proportional mode this is each MQTT shard's video viewer cap, not a fixed generator capability claim
  HOME100K_RUNNER_MODE default: live; use sample only for local developer smoke tests
  HOME100K_RUNNER_NOFILE_LIMIT default: 1048576; remote daemon nofile limit for MQTT sockets
  HOME100K_MQTT_CONCURRENCY default: 1000 per VM shard; live MQTT connect worker concurrency
  HOME100K_COMMAND_CONCURRENCY default: 100 per VM shard; live shadow command concurrency
  HOME100K_SHADOW_COMMAND_TIMEOUT default: 30s; per-phase shadow command wait timeout
  HOME100K_DEVICE_TOKEN_REQUEST_TIMEOUT default: 10s; per-attempt device /request_token timeout during MQTT bootstrap
  HOME100K_DEVICE_TOKEN_REQUEST_RETRIES default: 0; bounded retry count after the first device /request_token attempt
  HOME100K_RUNTIME_LOGS default: true; set false for large sustained loads to avoid MQTT /logs ingestion pressure
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
  HOME100K_VIDEO_LOADTEST auto/off/on; auto runs for HOME100K_SCENARIO_PROFILE=video-1k-v1, video-50k-turn-v1, or video-100k-turn-v1
  HOME100K_VIDEO_LOADTEST_MODE local/remote-sharded; remote-sharded splits each video step across live load-generator VMs
  HOME100K_VIDEO_LOADTEST_SHARD_MODE global/proportional; video-50k/video-100k TURN profiles default to proportional
  HOME100K_VIDEO_LOADTEST_BRANDNAME optional brand used to mint video load-test tokens
  HOME100K_VIDEO_LOADTEST_VIEWERS default: 100
  HOME100K_VIDEO_LOADTEST_LADDER optional comma-separated viewer steps, e.g. 100,500,1000,2000,5000
  HOME100K_VIDEO_LOADTEST_DURATION runner scheduling window; use 0s for concurrent viewer batches
  HOME100K_VIDEO_LOADTEST_MEDIA_DURATION WebRTC media/session hold duration per viewer; default: 20s
  HOME100K_VIDEO_LOADTEST_STEP_COOLDOWN default: 0s
  HOME100K_VIDEO_LOADTEST_TURN_SAMPLE_INTERVAL_SECONDS default: 5
  HOME100K_VIDEO_LOADTEST_TOKEN_CONCURRENCY default: 32
  HOME100K_VIDEO_LOADTEST_TOKEN_EXPIRY_SECONDS default: 1800
  HOME100K_VIDEO_LOADTEST_CONCURRENCY optional override; default: each ladder step uses its viewer/device count for concurrency
  HOME100K_VIDEO_LOADTEST_WEBRTC_MEDIA_SET default: h264
  HOME100K_VIDEO_LOADTEST_WEBRTC_ICE_POLICY default: relay
  HOME100K_VIDEO_LOADTEST_DEVICE_ONLINE_SETTLE optional delay after WebSocket device owners connect before viewer requests
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
  HOME100K_AUTO_DESTROY_ON_EXIT default: 1; destroy run-created VMs after workflow cleanup
  HOME100K_PRESERVE_VMS default: 0; explicit opt-out for investigation/resume
  HOME100K_SINGLE_DEVICE_SMOKE default: 1; run one actor-separated device/app smoke before provisioning
  HOME100K_KUBECONFIG default: CLOUD_STAGING_K8S_KUBECONFIG or <env-root>/state/kubeconfig.yaml

Examples:
  $(basename "$0")
  $(basename "$0") workflow-live
  HOME100K_RUN_ID=<run-id> $(basename "$0") workflow-resume-live
  $(basename "$0") workflow-video-live
  $(basename "$0") plan
  $(basename "$0") dry-run
  HOME100K_REGION=us-southeast $(basename "$0") provision-vms --live --confirm-live
EOF
}

run_home100k() {
  (cd "$repo_root" && GOWORK=auto go run ./loadtests/home-100k/cmd/home-100k -- "$@")
}

device_ca_bundle_path() {
  local candidate
  for candidate in \
    "${HOME100K_DEVICE_CLIENT_CA_BUNDLE:-}" \
    "$(local_env_root_path)/state/secrets/device-client-ca-bundle.pem"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

run_fixture_preflight() {
  local ca_bundle="$1"
  local preflight_json="$local_out_dir/preflight.json"
  local preflight_log="$local_out_dir/preflight.log"
  mkdir -p "$local_out_dir"
  if ! run_home100k preflight "${base_args[@]}" --ca-bundle "$ca_bundle" >"$preflight_json" 2>"$preflight_log"; then
    echo "loadtest preflight failed; classification is in $preflight_log" >&2
    return 1
  fi
  echo "loadtest fixture preflight passed: $preflight_json" >&2
}

run_single_device_smoke() {
  [[ "$single_device_smoke" == "1" || "$single_device_smoke" == "true" || "$single_device_smoke" == "TRUE" ]] || return 0
  [[ "$runner_mode" == "live" ]] || return 0
  local ca_bundle db_path smoke_dir brand_file
  ca_bundle="$(device_ca_bundle_path)" || { echo "CERTIFICATE_CA_MISSING: current device CA bundle not found" >&2; return 1; }
  brand_file="$(printf '%s' "$brandname" | tr '[:upper:]' '[:lower:]')"
  db_path="$(local_env_root_path)/artifacts/test-data/${brand_file}-test-data.sqlite"
  [[ -f "$db_path" ]] || { echo "FIXTURE_UNAVAILABLE: test-data DB not found: $db_path" >&2; return 1; }
  smoke_dir="$local_out_dir/single-device-smoke"
  mkdir -p "$smoke_dir"
  echo "running single-device actor smoke before VM provisioning" >&2
  if ! (cd "$repo_root/scripts/go/cloud-mqtt-test" && \
    HOME100K_DEVICE_CLIENT_CA_BUNDLE="$ca_bundle" \
    ACCOUNT_MANAGER_BASE_URL="$account_manager_base_url" \
    VIDEO_CLOUD_TOKEN_BASE_URL="$video_cloud_token_url" \
    VIDEO_CLOUD_MQTT_ADDR="$mqtt_addr" \
    VIDEO_CLOUD_PUBLIC_BASE_URL="$video_cloud_public_url" \
    GOWORK=off go run . \
      -root "$repo_root" -env-root "$(local_env_root_path)" -test-data-db "$db_path" \
      -brandname "$brandname" -out-dir "$smoke_dir" -load-model actor-separated-probe \
      -max-connected-devices 1 -max-users 1 -concurrency 1 -duration-seconds 5 -mqtt-probe true \
      -run-id "$run_id-single-device-smoke" >"$smoke_dir/console.log" 2>&1); then
    echo "TOKEN_BOOTSTRAP_FAILED or MQTT_SMOKE_FAILED: see $smoke_dir/console.log" >&2
    return 1
  fi
  echo "single-device actor smoke passed: $smoke_dir/TEST_REPORT.md" >&2
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
    printf 'time\trun_id\tphase\tlabel\tip\trole\tid\tstatus\tcpu_pct\tload1\tmem_used_mb\tmem_total_mb\tdisk_used\tdisk_total\tdisk_pct\trx_mbps\ttx_mbps\n' > "$load_vm_resource_file"
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
  "$repo_root/loadtests/home-100k/scripts/generate-report.sh" --out-dir "$local_out_dir"
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
        tags = set(str(tag) for tag in (vm.get("tags") or []))
        role = str(vm.get("role") or "").strip()
        if not role:
            if "video" in tags:
                role = "video"
            elif "mixed" in tags:
                role = "mixed"
        if not role:
            role = "mixed"
        parts = label.split("-")
        if role == "mixed" and len(parts) >= 4 and parts[0] == "home" and parts[1] == "100k":
            role = "-".join(parts[2:-1])
        f.write(f"{label}\t{ip}\t{role}\t{vm.get('id', '')}\n")
PY
}

node_resource_status() {
  if [[ "${HOME100K_NODE_RESOURCE_STATUS:-1}" == "0" || ! -f "$nodes_file" ]]; then
    return
  fi
  local now phase
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  phase="starting"
  if [[ -f "$status_file" ]]; then
    phase="$(cat "$status_file")"
  fi
  case "$phase" in
    starting|provision-vms|sync)
      return
      ;;
  esac
  ensure_resource_logs
  while IFS=$'\t' read -r label ip role id; do
    if [[ "$label" == "label" || -z "$ip" ]]; then
      continue
    fi
    local sample
    sample="$(ssh -n -o BatchMode=yes -o ConnectTimeout=5 -o ConnectionAttempts=1 -o ServerAliveInterval=5 -o ServerAliveCountMax=1 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "${ssh_user}@${ip}" \
      'read_net() { rx=0; tx=0; for dev in /sys/class/net/*; do name=${dev##*/}; [ "$name" = lo ] && continue; [ -r "$dev/statistics/rx_bytes" ] || continue; r=$(cat "$dev/statistics/rx_bytes"); t=$(cat "$dev/statistics/tx_bytes"); rx=$((rx+r)); tx=$((tx+t)); done; printf "%s %s" "$rx" "$tx"; }; read _ u n s i w irq sirq steal guest guestn < /proc/stat; total1=$((u+n+s+i+w+irq+sirq+steal)); idle1=$((i+w)); set -- $(read_net); rx1=$1; tx1=$2; sleep 1; read _ u n s i w irq sirq steal guest guestn < /proc/stat; total2=$((u+n+s+i+w+irq+sirq+steal)); idle2=$((i+w)); set -- $(read_net); rx2=$1; tx2=$2; awk -v t1=$total1 -v t2=$total2 -v i1=$idle1 -v i2=$idle2 "BEGIN {dt=t2-t1; di=i2-i1; if (dt>0) printf \"cpu_pct=%.1f \", 100*(dt-di)/dt; else printf \"cpu_pct=unknown \"}"; awk "{printf \"load1=%s \", \$1}" /proc/loadavg; free -m | awk "/^Mem:/ {printf \"mem_used_mb=%s mem_total_mb=%s \", \$3, \$2}"; df -h / | awk "NR==2 {printf \"disk_used=%s disk_total=%s disk_pct=%s \", \$3, \$2, \$5}"; awk -v rx1=$rx1 -v rx2=$rx2 -v tx1=$tx1 -v tx2=$tx2 "BEGIN {printf \"rx_mbps=%.3f tx_mbps=%.3f\", ((rx2-rx1)*8)/1000000, ((tx2-tx1)*8)/1000000}"' 2>/dev/null || true)"
    if [[ -z "$sample" ]]; then
      sample="unreachable"
    fi
    printf '[home-100k node] label=%s ip=%s role=%s id=%s %s\n' "$label" "$ip" "$role" "$id" "$sample" >&2
    if [[ "$sample" == "unreachable" ]]; then
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\tunreachable\t\t\t\t\t\t\t\t\t\n' "$now" "$run_id" "$phase" "$label" "$ip" "$role" "$id" >> "$load_vm_resource_file"
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\tok\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$now" "$run_id" "$phase" "$label" "$ip" "$role" "$id" \
        "$(kv_field "$sample" cpu_pct)" \
        "$(kv_field "$sample" load1)" \
        "$(kv_field "$sample" mem_used_mb)" \
        "$(kv_field "$sample" mem_total_mb)" \
        "$(kv_field "$sample" disk_used)" \
        "$(kv_field "$sample" disk_total)" \
        "$(kv_field "$sample" disk_pct | tr -d '%')" \
        "$(kv_field "$sample" rx_mbps)" \
        "$(kv_field "$sample" tx_mbps)" >> "$load_vm_resource_file"
    fi
  done < "$nodes_file"
}

k8s_kubeconfig() {
  local candidate
  local env_root_kubeconfig
  case "$env_root" in
    /*) env_root_kubeconfig="$env_root/state/kubeconfig.yaml" ;;
    *) env_root_kubeconfig="$repo_root/$env_root/state/kubeconfig.yaml" ;;
  esac
  for candidate in \
    "${HOME100K_KUBECONFIG:-}" \
    "${CLOUD_STAGING_K8S_KUBECONFIG:-}" \
    "$env_root_kubeconfig"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

local_env_root_path() {
  case "$env_root" in
    /*) printf '%s\n' "$env_root" ;;
    *) printf '%s/%s\n' "$repo_root" "$env_root" ;;
  esac
}

load_generator_vm_count_for_quota() {
  if [[ -n "$existing_generator_hosts" ]]; then
    printf '0\n'
    return
  fi
  local mixed_count="${vm_count:-}"
  if [[ -z "$mixed_count" && "${device_count:-}" =~ ^[0-9]+$ && "$device_count" -gt 0 ]]; then
    mixed_count="$(( (device_count + load_generator_devices_per_vm - 1) / load_generator_devices_per_vm ))"
  fi
  mixed_count="${mixed_count:-5}"
  local extra_video="${video_generator_vm_count:-0}"
  if ! [[ "$extra_video" =~ ^[0-9]+$ ]]; then
    extra_video=0
  fi
  printf '%s\n' "$((mixed_count + extra_video))"
}

linode_active_service_preflight() {
	local env_root_path artifact active_json rc
	env_root_path="$(local_env_root_path)"
	if [[ -f "$env_root_path/state/provider-preflight.env" ]]; then
		provider_active_service_limit="$(awk -F= '$1 == "PROVIDER_ACTIVE_SERVICE_LIMIT" {print $2; exit}' "$env_root_path/state/provider-preflight.env")"
	fi
	if [[ -n "$existing_generator_hosts" && -z "$provider_active_service_limit" ]]; then
		return
	fi
	if [[ -z "${LINODE_TOKEN:-}" ]]; then
		return
	fi
	mkdir -p "$local_out_dir"
	artifact="$local_out_dir/linode-active-service-preflight.json"
  active_json="$(mktemp)"
  rc=0
  /usr/bin/curl -fsS -H "Authorization: Bearer $LINODE_TOKEN" 'https://api.linode.com/v4/linode/instances?page_size=500' > "$active_json" || rc=$?
  if [[ "$rc" -ne 0 ]]; then
    rm -f "$active_json"
    echo "warning: unable to query Linode active services for quota preflight" >&2
    return
  fi
	if ! python3 - "$active_json" "$env_root_path" "$artifact" "$(load_generator_vm_count_for_quota)" "$provider_active_service_limit" <<'PY'
import json
import os
import sys

active_path, env_root, artifact, load_generators_raw, limit_raw = sys.argv[1:]

def env_file(path):
    out = {}
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                k, v = line.split("=", 1)
                out[k.strip()] = v.strip().strip('"').strip("'")
    except FileNotFoundError:
        pass
    return out

def int_value(value, default=0):
    try:
        return int(str(value).strip())
    except Exception:
        return default

def count_active_artifact_items(path, key, active_ids, active_labels):
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except Exception:
        return 0
    items = data.get(key)
    if isinstance(items, list):
        count = 0
        for item in items:
            if not isinstance(item, dict):
                continue
            item_id = item.get("id")
            label = str(item.get("label") or "")
            if item_id in active_ids or label in active_labels:
                count += 1
        return count
    return 0

with open(active_path, encoding="utf-8") as f:
    active = json.load(f)
active_items = active.get("data") or []
current_active = len(active_items)
active_ids = {item.get("id") for item in active_items}
active_labels = {str(item.get("label") or "") for item in active_items}

stack = env_file(os.path.join(env_root, "env", "stack.env"))
desired_edge = int_value(stack.get("EDGE_REPLICAS"), 1)
desired_coturn = int_value(stack.get("TURN_REPLICAS"), 1)
existing_edge = count_active_artifact_items(os.path.join(env_root, "artifacts", "edge-haproxy", "edge-vms.json"), "edge_vms", active_ids, active_labels)
existing_coturn = count_active_artifact_items(os.path.join(env_root, "artifacts", "coturn-vm", "coturn-vms.json"), "coturn_vms", active_ids, active_labels)
missing_edge = max(0, desired_edge - existing_edge)
missing_coturn = max(0, desired_coturn - existing_coturn)
load_generators = int_value(load_generators_raw, 0)
projected_required = current_active + missing_edge + missing_coturn + load_generators
limit = int_value(limit_raw, 0)
ok = True
reason = ""
if limit > 0 and projected_required > limit:
    ok = False
    reason = (
        f"projected active services {projected_required} exceeds configured limit {limit} "
        f"(current={current_active}, missing_edge={missing_edge}, missing_coturn={missing_coturn}, "
        f"load_generators={load_generators})"
    )

doc = {
    "schema": "home-100k-linode-active-service-preflight/v1",
    "current_active_services": current_active,
    "missing_edge_vms": missing_edge,
    "missing_coturn_vms": missing_coturn,
    "planned_load_generator_vms": load_generators,
    "projected_required_active_services": projected_required,
    "configured_limit": limit if limit > 0 else None,
    "ok": ok,
    "reason": reason,
    "active_instances": [
        {
            "id": item.get("id"),
            "label": item.get("label"),
            "status": item.get("status"),
            "type": item.get("type"),
            "region": item.get("region"),
            "tags": item.get("tags") or [],
        }
        for item in active_items
    ],
}
os.makedirs(os.path.dirname(artifact), exist_ok=True)
with open(artifact, "w", encoding="utf-8") as f:
    json.dump(doc, f, indent=2, sort_keys=True)
    f.write("\n")
print(
    "current=%d missing_edge=%d missing_coturn=%d load_generators=%d projected=%d limit=%s ok=%s"
    % (current_active, missing_edge, missing_coturn, load_generators, projected_required, limit if limit > 0 else "unset", str(ok).lower())
)
if not ok:
    print(reason, file=sys.stderr)
    sys.exit(3)
PY
  then
    rm -f "$active_json"
    echo "Linode active-service quota preflight failed; see $artifact" >&2
    exit 2
  fi
  rm -f "$active_json"
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
  if ! command -v jq >/dev/null 2>&1; then
    echo "HOME100K_MQTT_ADDR=auto-public-mqtt requires jq" >&2
    return 1
  fi
  local state_file edge_host stack_name
  for state_file in "$env_root"/state/*.state.json; do
    [[ -f "$state_file" ]] || continue
    edge_host="$(jq -r '.instances.edge.public_ipv4 // empty' "$state_file")"
    if [[ -n "$edge_host" ]]; then
      printf '%s:8883\n' "$edge_host"
      return 0
    fi
  done
  local kubectl_bin="${HOME100K_KUBECTL:-${RTK_CLOUD_KUBECTL:-kubectl}}"
  if ! command -v "$kubectl_bin" >/dev/null 2>&1; then
    echo "HOME100K_MQTT_ADDR=auto-public-mqtt found no normalized edge endpoint and requires kubectl for service discovery" >&2
    return 1
  fi
  local kubeconfig output addrs namespace
  kubeconfig="$(k8s_kubeconfig || true)"
  stack_name="$(jq -r '.stack // empty' "$env_root"/state/*.state.json 2>/dev/null | head -1)"
  namespace="${stack_name:-video-cloud-staging}-video-cloud"
  if [[ -n "$kubeconfig" ]]; then
    output="$(KUBECONFIG="$kubeconfig" "$kubectl_bin" -n "$namespace" get svc -l app.kubernetes.io/component=public-mqtt -o json)"
  else
    output="$("$kubectl_bin" -n "$namespace" get svc -l app.kubernetes.io/component=public-mqtt -o json)"
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
    echo "HOME100K_MQTT_ADDR=auto-public-mqtt found neither a normalized edge endpoint nor public MQTT LoadBalancer IPs" >&2
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
  top_output="$("${kubectl_prefix[@]}" top pods -A --containers --request-timeout=5s 2>&1 | grep -E 'mqtt|video-cloud-(api|clipverifier)|postgres|redis|ingress' || true)"
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
      [[ "$scenario_profile" == "video-1k-v1" || "$scenario_profile" == "video-50k-turn-v1" || "$scenario_profile" == "video-100k-turn-v1" ]]
      ;;
    *)
      echo "invalid HOME100K_VIDEO_LOADTEST: $video_loadtest" >&2
      return 2
      ;;
  esac
}

max_video_token_devices() {
  local max="$video_loadtest_devices"
  local item
  if [[ -n "$video_loadtest_ladder" ]]; then
    IFS=',' read -ra ladder_items <<<"$video_loadtest_ladder"
    for item in "${ladder_items[@]}"; do
      item="${item//[[:space:]]/}"
      if [[ ! "$item" =~ ^[0-9]+$ || "$item" -le 0 ]]; then
        echo "invalid HOME100K_VIDEO_LOADTEST_LADDER item: $item" >&2
        return 2
      fi
      if (( item > max )); then
        max="$item"
      fi
    done
  fi
  printf '%s\n' "$max"
}

ensure_video_loadtest_tokens() {
  local max_devices="$1"
  local token_env="$video_loadtest_artifact_dir/token-env.sh"
  if [[ -z "${VIDEO_CLOUD_LOAD_DEVICE_IDS:-}" ]] || \
     [[ -z "${VIDEO_CLOUD_LOAD_DEVICE_TOKENS:-}" && -z "${VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE:-}" ]] || \
     [[ -z "${VIDEO_CLOUD_LOAD_APP_TOKENS:-}" && -z "${VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE:-}" ]]; then
    mkdir -p "$video_loadtest_artifact_dir"
    local token_args=(
      video-loadtest-tokens
      --env-root "$(local_env_root_path)" \
      --brandname "$video_loadtest_brandname" \
      --max-devices "$max_devices" \
      --require-devices "$max_devices" \
      --expiry-seconds "$video_loadtest_token_expiry_seconds" \
      --concurrency "$video_loadtest_token_concurrency" \
      --request-timeout "$video_loadtest_token_request_timeout" \
      --out-env "$token_env"
    )
    if [[ -n "$video_cloud_token_url" ]]; then
      token_args+=(--base-url "$video_cloud_token_url")
    fi
    if [[ -n "$brand_plan" ]]; then
      token_args+=(--brand-plan "$brand_plan")
    fi
    if ! (cd "$repo_root/scripts/go/rtk-cloud" && GOWORK=off go run . "${token_args[@]}"); then
      echo "video loadtest token generation failed" >&2
      return 1
    fi
    if [[ ! -s "$token_env" ]]; then
      echo "video loadtest token env was not written: $token_env" >&2
      return 1
    fi
    # shellcheck disable=SC1090
    source "$token_env"
  fi
}

ensure_video_loadtest_tokens_for_ids() {
  local device_ids_file="$1"
  local token_env="$2"
  if [[ ! -s "$device_ids_file" ]]; then
    echo "video loadtest exact token device IDs missing: $device_ids_file" >&2
    return 1
  fi
  mkdir -p "$(dirname "$token_env")"
  local token_args=(
    video-loadtest-tokens
    --env-root "$(local_env_root_path)"
    --brandname "$video_loadtest_brandname"
    --device-ids-file "$device_ids_file"
    --require-devices "$(wc -l <"$device_ids_file" | tr -d '[:space:]')"
    --expiry-seconds "$video_loadtest_token_expiry_seconds"
    --concurrency "$video_loadtest_token_concurrency"
    --request-timeout "$video_loadtest_token_request_timeout"
    --out-env "$token_env"
  )
  if [[ -n "$video_cloud_token_url" ]]; then
    token_args+=(--base-url "$video_cloud_token_url")
  fi
  if [[ -n "$brand_plan" ]]; then
    token_args+=(--brand-plan "$brand_plan")
  fi
  if ! (cd "$repo_root/scripts/go/rtk-cloud" && GOWORK=off go run . "${token_args[@]}"); then
    echo "video loadtest exact token generation failed" >&2
    return 1
  fi
  if [[ ! -s "$token_env" ]]; then
    echo "video loadtest exact token env was not written: $token_env" >&2
    return 1
  fi
  # shellcheck disable=SC1090
  source "$token_env"
}

coturn_vm_rows() {
  local env_root_json
  env_root_json="$(local_env_root_path)"
  python3 - "$env_root_json" <<'PY'
import json, sys
from pathlib import Path
root = Path(sys.argv[1])
summary = root / "artifacts" / "coturn-vm" / "coturn-vms.json"
single = root / "artifacts" / "coturn-vm" / "coturn-vm.json"
items = []
if summary.exists():
    data = json.load(summary.open())
    items = data.get("coturn_vms") or []
elif single.exists():
    data = json.load(single.open())
    items = [data.get("coturn_vm") or data]
for item in items:
    name = (item.get("name") or item.get("label") or "coturn").strip()
    ip = (item.get("public_ip") or "").strip()
    domain = (item.get("domain") or "").strip()
    if ip or domain:
        print("\t".join([name, ip, domain]))
PY
}

start_coturn_active_sampler() {
  local artifact_dir="$1"
  local sample_file="$artifact_dir/turn-active-samples.tsv"
  local rows_file="$artifact_dir/coturn-vms.tsv"
  mkdir -p "$artifact_dir"
  coturn_vm_rows >"$rows_file" || true
  if [[ ! -s "$rows_file" ]]; then
    return 0
  fi
  {
    printf 'time\tnode\thost\tudp_sockets\ttcp_estab\trelay_udp_flows\trelay_tcp_flows\tactive_allocations\tactive_sessions\tjournal_events\tcoturn_cpu_pct\tcoturn_rss_kb\trx_bytes\ttx_bytes\tevidence_status\n'
  } >"$sample_file"
  (
    while true; do
      local now
      now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      while IFS=$'\t' read -r node ip domain; do
        [[ -n "${node:-}" ]] || continue
        local host="${ip:-$domain}"
        [[ -n "$host" ]] || continue
        local sample
        sample="$(ssh -n -o BatchMode=yes -o ConnectTimeout=5 -o ConnectionAttempts=1 -o ServerAliveInterval=5 -o ServerAliveCountMax=1 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "${ssh_user}@${host}" \
          'udp=$(ss -Huanp 2>/dev/null | awk "/turnserver/ {c++} END {print c+0}");
           tcp=$(ss -Htanp 2>/dev/null | awk "/turnserver/ && /ESTAB/ {c++} END {print c+0}");
           relay_udp=$(ss -Huanp 2>/dev/null | awk "/turnserver/ && ($0 ~ /:(49[1-9][0-9][0-9]|5[0-9][0-9][0-9][0-9]|6[0-4][0-9][0-9][0-9]|65[0-4][0-9][0-9]|655[0-2][0-9]|6553[0-5])/) {c++} END {print c+0}");
           relay_tcp=$(ss -Htanp 2>/dev/null | awk "/turnserver/ && /ESTAB/ && ($0 ~ /:(49[1-9][0-9][0-9]|5[0-9][0-9][0-9][0-9]|6[0-4][0-9][0-9][0-9]|65[0-4][0-9][0-9]|655[0-2][0-9]|6553[0-5])/) {c++} END {print c+0}");
           events=$(journalctl -u coturn --since "1 minutes ago" --no-pager 2>/dev/null | grep -Eci "session|allocation|peer|relay" || true);
           cpu=$(ps -C turnserver -o %cpu= 2>/dev/null | awk "{s+=\$1} END {printf \"%.0f\", s+0}");
           rss=$(ps -C turnserver -o rss= 2>/dev/null | awk "{s+=\$1} END {print s+0}");
	           rx=$(awk "{s+=\$1} END {print s+0}" /sys/class/net/*/statistics/rx_bytes 2>/dev/null || echo 0);
	           tx=$(awk "{s+=\$1} END {print s+0}" /sys/class/net/*/statistics/tx_bytes 2>/dev/null || echo 0);
	           cli_password=$(awk -F= '"'"'$1=="COTURN_CLI_PASSWORD" {print $2; exit}'"'"' /etc/video-cloud-coturn-cli.env 2>/dev/null || true);
	           cli_out="";
	           if [ -n "$cli_password" ] && command -v nc >/dev/null 2>&1; then
	             cli_out=$({ sleep 0.2; printf "%s\r\n" "$cli_password"; sleep 0.2; printf "sr video_cloud\r\n"; sleep 0.2; printf "ps\r\n"; sleep 0.2; printf "quit\r\n"; sleep 0.2; } | nc -w 3 127.0.0.1 5766 2>/dev/null || true);
	           fi;
	           cli_active=$(printf "%s\n" "$cli_out" | awk '"'"'
	             BEGIN { IGNORECASE = 1 }
	             /total[[:space:]]+sessions/ {
	               for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+$/ && $i + 0 > max) max = $i + 0
	             }
	             END { print max + 0 }
	           '"'"');
	           ct_udp=$(if command -v conntrack >/dev/null 2>&1; then conntrack -L -p udp 2>/dev/null || true; fi);
           ct_tcp=$(if command -v conntrack >/dev/null 2>&1; then conntrack -L -p tcp 2>/dev/null || true; fi);
           ct_control_udp=$(printf "%s\n" "$ct_udp" | awk '"'"'{for(i=1;i<=NF;i++) if($i=="dport=3478" || $i=="sport=3478" || $i=="dport=3478," || $i=="sport=3478,") {c++; break}} END{print c+0}'"'"');
           ct_control_tcp=$(printf "%s\n" "$ct_tcp" | awk '"'"'{for(i=1;i<=NF;i++) if($i=="dport=3478" || $i=="sport=3478" || $i=="dport=3478," || $i=="sport=3478,") {c++; break}} END{print c+0}'"'"');
           ct_relay_udp=$(printf "%s\n" "$ct_udp" | awk '"'"'{hit=0; for(i=1;i<=NF;i++){split($i,a,"="); if((a[1]=="sport" || a[1]=="dport") && a[2]+0>=49152 && a[2]+0<=65535) hit=1} if(hit)c++} END{print c+0}'"'"');
           ct_relay_tcp=$(printf "%s\n" "$ct_tcp" | awk '"'"'{hit=0; for(i=1;i<=NF;i++){split($i,a,"="); if((a[1]=="sport" || a[1]=="dport") && a[2]+0>=49152 && a[2]+0<=65535) hit=1} if(hit)c++} END{print c+0}'"'"');
           ct_control=$(( ${ct_control_udp:-0} + ${ct_control_tcp:-0} ));
           if [ "${ct_relay_udp:-0}" -gt "${relay_udp:-0}" ]; then relay_udp="$ct_relay_udp"; fi;
           if [ "${ct_relay_tcp:-0}" -gt "${relay_tcp:-0}" ]; then relay_tcp="$ct_relay_tcp"; fi;
	           journal_active=$(journalctl -u coturn --since "30 minutes ago" --no-pager 2>/dev/null | awk '"'"'
             BEGIN { IGNORECASE = 1 }
             /session/ && (/ALLOCATE processed, success/ || /new allocation/ || /: new/ || /New allocation/) {
               for (i = 1; i < NF; i++) if ($i == "session") { id = $(i + 1); sub(/:.*/, "", id); if (id != "") open[id] = 1 }
             }
             /session/ && (/closed/ || /delete allocation/ || /allocation timeout/ || /delete session/ || /session closed/) {
               for (i = 1; i < NF; i++) if ($i == "session") { id = $(i + 1); sub(/:.*/, "", id); if (id != "") delete open[id] }
             }
             END { for (id in open) c++; print c + 0 }
	           '"'"' || true);
	           active="${journal_active:-0}";
	           if [ "${cli_active:-0}" -gt "$active" ]; then active="$cli_active"; fi;
	           if [ "${ct_control:-0}" -gt "$active" ]; then active="$ct_control"; fi;
	           status=unavailable;
           if [ "${udp:-0}" -gt 16 ] || [ "${tcp:-0}" -gt 0 ] || [ "${events:-0}" -gt 0 ]; then status=socket_activity_observed; fi;
	           if [ "${relay_udp:-0}" -gt 0 ] || [ "${relay_tcp:-0}" -gt 0 ]; then status=relay_flow_observed; fi;
	           if [ "${journal_active:-0}" -gt 0 ]; then status=journal_active; fi;
	           if [ "${ct_control:-0}" -gt 0 ]; then status=conntrack_active; fi;
	           if [ "${cli_active:-0}" -gt 0 ]; then status=cli_active; fi;
	           allocations="${cli_active:-0}";
	           sessions="${cli_active:-0}";
	           if [ "${active:-0}" -gt "$allocations" ]; then allocations="$active"; fi;
	           if [ "${active:-0}" -gt "$sessions" ]; then sessions="$active"; fi;
	           printf "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s" "$udp" "$tcp" "$relay_udp" "$relay_tcp" "${allocations:-0}" "${sessions:-0}" "$events" "${cpu:-0}" "${rss:-0}" "${rx:-0}" "${tx:-0}" "$status"' 2>/dev/null || true)"
        if [[ -z "$sample" ]]; then
          sample="0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"0"$'\t'"unavailable"
        fi
        printf '%s\t%s\t%s\t%s\n' "$now" "$node" "$host" "$sample" >>"$sample_file"
      done <"$rows_file"
      sleep "$video_loadtest_turn_sample_interval"
    done
  ) >/dev/null 2>&1 &
  printf '%s\n' "$!"
}

video_loadtest_first_device_ids() {
  local limit="$1"
  local raw="${VIDEO_CLOUD_LOAD_DEVICE_IDS:-}"
  if [[ -z "$raw" ]]; then
    return 0
  fi
  awk -v limit="$limit" '
    BEGIN { RS = ","; ORS = "" }
    NR <= limit {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", $0)
      if ($0 == "") {
        next
      }
      if (count > 0) {
        printf ","
      }
      printf "%s", $0
      count++
    }
  ' <<<"$raw"
}

video_loadtest_device_id_count() {
  local raw="$1"
  if [[ -z "$raw" ]]; then
    printf '0\n'
    return 0
  fi
  awk '
    BEGIN { RS = "," }
    {
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", $0)
      if ($0 != "") {
        count++
      }
    }
    END { print count + 0 }
  ' <<<"$raw"
}

shell_quote() {
  printf '%q' "$1"
}

video_loadtest_remote_host_rows() {
  if [[ -n "$video_loadtest_remote_hosts" ]]; then
    python3 - "$video_loadtest_remote_hosts" <<'PY'
import sys

raw = sys.argv[1]
for index, item in enumerate(raw.replace("\n", ",").split(","), 1):
    item = item.strip()
    if not item:
        continue
    if "=" in item:
        label, host = item.split("=", 1)
    else:
        label, host = f"video{index:02d}", item
    label, host = label.strip(), host.strip()
    if host:
        print(f"{label}\t{host}")
PY
    return
  fi
  python3 - "$local_vm_state_file" "$local_out_dir" "$video_loadtest_shard_mode" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
out_dir = Path(sys.argv[2])
shard_mode = sys.argv[3]
if not path.exists():
    raise SystemExit(0)
data = json.load(path.open())
vms = data.get("created") or data.get("vms") or []
video_vms = []
mixed_vms = []
for index, vm in enumerate(vms, 1):
    label = str(vm.get("label") or f"lg{index:02d}")
    host = str(vm.get("public_ipv4") or vm.get("ip") or "")
    tags = set(str(tag) for tag in (vm.get("tags") or []))
    if host and "video" in tags:
        video_vms.append((label, host))
    manifest = out_dir / "credential-bundles" / f"{label}.manifest.json"
    if host and manifest.exists():
        mixed_vms.append((label, host))
if shard_mode == "proportional":
    selected = mixed_vms
else:
    selected = video_vms or [
        (str(vm.get("label") or f"lg{index:02d}"), str(vm.get("public_ipv4") or vm.get("ip") or ""))
        for index, vm in enumerate(vms, 1)
    ]
for label, host in selected:
    if host:
        print(f"{label}\t{host}")
PY
}

ensure_video_loadtest_binary() {
  if [[ -x "$video_loadtest_binary" ]]; then
    case "$video_loadtest_rebuild" in
      never)
        return 0
        ;;
      always)
        ;;
      auto|"")
        if [[ "$video_loadtest_binary" != "$default_video_loadtest_binary" ]]; then
          return 0
        fi
        if ! find "$repo_root/e2e_test/video_cloud/load" -type f -name '*.go' -newer "$video_loadtest_binary" -print -quit | grep -q .; then
          return 0
        fi
        ;;
      *)
        echo "invalid HOME100K_VIDEO_LOADTEST_REBUILD: $video_loadtest_rebuild" >&2
        return 2
        ;;
    esac
  fi
  mkdir -p "$(dirname "$video_loadtest_binary")"
  echo "building video loadtest binary: $video_loadtest_binary" >&2
  (cd "$repo_root/e2e_test" && GOWORK=off GOOS=linux GOARCH=amd64 go build -o "$video_loadtest_binary" ./video_cloud/load/cmd/rtk-video-loadtest)
}

check_video_loadtest_remote_capacity() {
  if [[ "$video_loadtest_mode" != "remote-sharded" || -z "$video_loadtest_max_viewers_per_host" ]]; then
    return 0
  fi
  local steps
  if [[ -n "$video_loadtest_ladder" ]]; then
    steps="$video_loadtest_ladder"
  else
    steps="$video_loadtest_viewers"
  fi
  local host_rows
  host_rows="$(video_loadtest_remote_host_rows)"
  python3 - "$steps" "$host_rows" "$video_loadtest_max_viewers_per_host" "$video_loadtest_shard_mode" "$local_out_dir" <<'PY'
import json
import math
import sys
from pathlib import Path

steps_raw = sys.argv[1]
hosts_raw = sys.argv[2]
max_per_host_raw = sys.argv[3]
shard_mode = sys.argv[4]
out_dir = Path(sys.argv[5])
try:
    max_per_host = int(max_per_host_raw)
except ValueError as exc:
    raise SystemExit(f"invalid HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST={max_per_host_raw!r}") from exc
if max_per_host <= 0:
    raise SystemExit("HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST must be positive")
steps = []
for raw in steps_raw.split(","):
    raw = raw.strip()
    if not raw:
        continue
    try:
        steps.append(int(raw))
    except ValueError as exc:
        raise SystemExit(f"invalid video viewer step={raw!r}") from exc
hosts = []
for line in hosts_raw.splitlines():
    parts = line.split("\t")
    if len(parts) >= 2 and parts[1].strip():
        hosts.append((parts[0].strip(), parts[1].strip()))
if not hosts:
    raise SystemExit("remote-sharded video loadtest requires live VM hosts or HOME100K_VIDEO_LOADTEST_REMOTE_HOSTS")
if shard_mode == "proportional":
    counts = []
    for label, _ in hosts:
        manifest = out_dir / "credential-bundles" / f"{label}.manifest.json"
        if not manifest.exists():
            raise SystemExit(f"proportional video shard missing credential manifest for host label={label}: {manifest}")
        data = json.loads(manifest.read_text(encoding="utf-8"))
        ids = data.get("device_ids") or []
        if not ids:
            raise SystemExit(f"proportional video shard missing device_ids for host label={label}: {manifest}")
        counts.append((label, len(ids)))
    total = sum(count for _, count in counts)
    if total <= 0:
        raise SystemExit("proportional video shard has no MQTT device inventory")
    for step in steps:
        if step <= 0:
            raise SystemExit(f"invalid video viewer step={step}; must be positive")
        if step > total:
            raise SystemExit(f"proportional video viewer step exceeds shard inventory: requested_viewers={step} available_devices={total}")
        allocations = []
        remaining = step
        remainders = []
        for label, count in counts:
            exact = step * count / total
            base = int(math.floor(exact))
            allocations.append([label, base])
            remaining -= base
            remainders.append((exact - base, label))
        allocation_by_label = {label: base for label, base in allocations}
        for _, label in sorted(remainders, key=lambda item: (-item[0], item[1]))[:remaining]:
            allocation_by_label[label] += 1
        over = [(label, count) for label, count in allocation_by_label.items() if count > max_per_host]
        if over:
            detail = ", ".join(f"{label}={count}" for label, count in over)
            raise SystemExit(
                "proportional video loadtest generator capacity insufficient: "
                f"requested_viewers={step} max_viewers_per_host={max_per_host} over_limit={detail}"
            )
    raise SystemExit(0)
for step in steps:
    if step <= 0:
        raise SystemExit(f"invalid video viewer step={step}; must be positive")
    required_hosts = math.ceil(step / max_per_host)
    if len(hosts) < required_hosts:
        raise SystemExit(
            "remote-sharded video loadtest generator capacity insufficient: "
            f"requested_viewers={step} max_viewers_per_host={max_per_host} "
            f"required_hosts={required_hosts} available_hosts={len(hosts)}"
        )
PY
}

write_video_loadtest_shard_plan() {
  local devices="$1"
  local step_dir="$2"
  local host_rows="$3"
  local plan_file="$step_dir/shards.tsv"
  local all_ids_file="$step_dir/device-ids.txt"
  mkdir -p "$step_dir"
  if [[ "$video_loadtest_shard_mode" == "proportional" ]]; then
    python3 - "$devices" "$host_rows" "$plan_file" "$step_dir" "$video_loadtest_max_viewers_per_host" "$local_out_dir" "$(local_env_root_path)" "$all_ids_file" <<'PY'
import json
import math
import sqlite3
import sys
from pathlib import Path

requested = int(sys.argv[1])
hosts_raw = sys.argv[2]
plan_path = Path(sys.argv[3])
step_dir = Path(sys.argv[4])
max_per_host_raw = sys.argv[5].strip()
out_dir = Path(sys.argv[6])
env_root = Path(sys.argv[7])
all_ids_path = Path(sys.argv[8])

hosts = []
for line in hosts_raw.splitlines():
    parts = line.split("\t")
    if len(parts) >= 2 and parts[0].strip() and parts[1].strip():
        hosts.append((parts[0].strip(), parts[1].strip()))
if not hosts:
    raise SystemExit("proportional video loadtest requires live MQTT shard VMs")

video_capable = set()
for db_path in sorted((env_root / "artifacts" / "test-data").glob("*-test-data.sqlite")):
    conn = sqlite3.connect(db_path)
    try:
        for device_id, device_type, service_options_json in conn.execute(
            "select device_id, device_type, service_options_json from device_bindings"
        ):
            try:
                options = json.loads(service_options_json or "[]")
            except json.JSONDecodeError:
                options = []
            if device_type == "camera" and "video_streaming" in options:
                video_capable.add(str(device_id))
    finally:
        conn.close()
if not video_capable:
    raise SystemExit("proportional video shard has no camera/video_streaming devices in test-data SQLite")

max_per_host = None
if max_per_host_raw:
    try:
        max_per_host = int(max_per_host_raw)
    except ValueError as exc:
        raise SystemExit(f"invalid HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST={max_per_host_raw!r}") from exc
    if max_per_host <= 0:
        raise SystemExit("HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST must be positive")

shards = []
for label, host in hosts:
    manifest = out_dir / "credential-bundles" / f"{label}.manifest.json"
    if not manifest.exists():
        raise SystemExit(f"proportional video shard missing credential manifest for host label={label}: {manifest}")
    data = json.loads(manifest.read_text(encoding="utf-8"))
    mqtt_ids = [str(item).strip() for item in (data.get("device_ids") or []) if str(item).strip()]
    if not mqtt_ids:
        raise SystemExit(f"proportional video shard missing device_ids for host label={label}: {manifest}")
    video_ids = [device_id for device_id in mqtt_ids if device_id in video_capable]
    if not video_ids:
        raise SystemExit(f"proportional video shard has no shard-local camera/video_streaming device IDs for host label={label}")
    shards.append({"label": label, "host": host, "ids": video_ids, "mqtt_devices": len(mqtt_ids), "video_capable_devices": len(video_ids)})

total_devices = sum(item["video_capable_devices"] for item in shards)
if requested <= 0:
    raise SystemExit(f"invalid video viewer step={requested}; must be positive")
if requested > total_devices:
    raise SystemExit(f"proportional video viewer step exceeds shard inventory: requested_viewers={requested} available_devices={total_devices}")

remaining = requested
remainders = []
for item in shards:
    exact = requested * item["video_capable_devices"] / total_devices
    count = int(math.floor(exact))
    item["video_count"] = count
    remaining -= count
    remainders.append((exact - count, item["label"]))
by_label = {item["label"]: item for item in shards}
for _, label in sorted(remainders, key=lambda pair: (-pair[0], pair[1]))[:remaining]:
    by_label[label]["video_count"] += 1

if max_per_host is not None:
    over = [f"{item['label']}={item['video_count']}" for item in shards if item["video_count"] > max_per_host]
    if over:
        raise SystemExit(
            "proportional video loadtest generator capacity insufficient: "
            f"requested_viewers={requested} max_viewers_per_host={max_per_host} over_limit={','.join(over)}"
        )

with plan_path.open("w", encoding="utf-8") as out:
    out.write("shard\tlabel\thost\tcount\tdevice_ids_file\tartifact_dir\tmqtt_devices\tvideo_viewers\tvideo_ratio\n")
    all_step_ids = []
    for index, item in enumerate(shards, 1):
        count = item["video_count"]
        if count <= 0:
            continue
        shard = f"shard-{index:02d}"
        shard_dir = step_dir / shard
        shard_dir.mkdir(parents=True, exist_ok=True)
        shard_ids = item["ids"][:count]
        all_step_ids.extend(shard_ids)
        shard_ids_file = shard_dir / "device-ids.txt"
        shard_ids_file.write_text("\n".join(shard_ids) + "\n", encoding="utf-8")
        ratio = count / item["mqtt_devices"] if item["mqtt_devices"] else 0
        out.write(
            f"{shard}\t{item['label']}\t{item['host']}\t{count}\t{shard_ids_file}\t{shard_dir}\t"
            f"{item['mqtt_devices']}\t{count}\t{ratio:.6f}\n"
        )
all_ids_path.write_text("\n".join(all_step_ids) + "\n", encoding="utf-8")
PY
    printf '%s\n' "$plan_file"
    return
  fi
  video_loadtest_first_device_ids "$devices" | tr ',' '\n' | awk 'NF' >"$all_ids_file"
  python3 - "$devices" "$host_rows" "$all_ids_file" "$plan_file" "$step_dir" "$video_loadtest_max_viewers_per_host" <<'PY'
import math
import sys
from pathlib import Path

requested = int(sys.argv[1])
hosts_raw = sys.argv[2]
ids_path = Path(sys.argv[3])
plan_path = Path(sys.argv[4])
step_dir = Path(sys.argv[5])
max_per_host_raw = sys.argv[6].strip()

hosts = []
for line in hosts_raw.splitlines():
    parts = line.split("\t")
    if len(parts) >= 2 and parts[1].strip():
        hosts.append((parts[0].strip() or f"video{len(hosts)+1:02d}", parts[1].strip()))
ids = [line.strip() for line in ids_path.read_text(encoding="utf-8").splitlines() if line.strip()]
if len(ids) < requested:
    raise SystemExit(f"video loadtest device inventory insufficient for remote shard: requested_devices={requested} available_devices={len(ids)}")
if not hosts:
    raise SystemExit("remote-sharded video loadtest requires live VM hosts or HOME100K_VIDEO_LOADTEST_REMOTE_HOSTS")
if max_per_host_raw:
    try:
        max_per_host = int(max_per_host_raw)
    except ValueError as exc:
        raise SystemExit(f"invalid HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST={max_per_host_raw!r}") from exc
    if max_per_host <= 0:
        raise SystemExit("HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST must be positive")
    required_hosts = math.ceil(requested / max_per_host)
    if len(hosts) < required_hosts:
        raise SystemExit(
            "remote-sharded video loadtest generator capacity insufficient: "
            f"requested_viewers={requested} max_viewers_per_host={max_per_host} "
            f"required_hosts={required_hosts} available_hosts={len(hosts)}"
        )
    shard_count = min(len(hosts), requested, required_hosts)
else:
    shard_count = min(len(hosts), requested)
base = requested // shard_count
extra = requested % shard_count
offset = 0
with plan_path.open("w", encoding="utf-8") as out:
    out.write("shard\tlabel\thost\tcount\tdevice_ids_file\tartifact_dir\tmqtt_devices\tvideo_viewers\tvideo_ratio\n")
    for index in range(shard_count):
        count = base + (1 if index < extra else 0)
        shard_ids = ids[offset : offset + count]
        offset += count
        label, host = hosts[index]
        shard = f"shard-{index+1:02d}"
        shard_dir = step_dir / shard
        shard_dir.mkdir(parents=True, exist_ok=True)
        shard_ids_file = shard_dir / "device-ids.txt"
        shard_ids_file.write_text("\n".join(shard_ids) + "\n", encoding="utf-8")
        out.write(f"{shard}\t{label}\t{host}\t{count}\t{shard_ids_file}\t{shard_dir}\t0\t{count}\t0\n")
PY
  printf '%s\n' "$plan_file"
}

install_video_loadtest_remote_host() {
  local host="$1"
  local remote="${ssh_user}@${host}"
  local remote_base="$video_loadtest_remote_dir/$run_id"
  local remote_testdata="$remote_base/e2e_test/video_cloud/load/testdata"
  ssh -n -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "$remote" \
    "mkdir -p $(shell_quote "$remote_base") $(shell_quote "$remote_testdata")"
  scp_video_loadtest_file_if_changed "$remote" "$video_loadtest_binary" "$remote_base/rtk-video-loadtest"
  scp_video_loadtest_file_if_changed "$remote" "$repo_root/e2e_test/video_cloud/load/testdata/testsrc2_1080p_2s.h264" "$remote_testdata/testsrc2_1080p_2s.h264"
  scp_video_loadtest_file_if_changed "$remote" "$repo_root/e2e_test/video_cloud/load/testdata/testtone_48k_mono_2s.opusframes" "$remote_testdata/testtone_48k_mono_2s.opusframes"
  ssh -n -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "$remote" \
    "chmod 755 $(shell_quote "$remote_base")/rtk-video-loadtest"
}

scp_video_loadtest_file_if_changed() {
  local remote="$1"
  local local_path="$2"
  local remote_path="$3"
  local local_sha
  local_sha="$(shasum -a 256 "$local_path" | awk '{print $1}')"
  if ssh -n -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "$remote" \
    "test -f $(shell_quote "$remote_path") && command -v sha256sum >/dev/null 2>&1 && test \"\$(sha256sum $(shell_quote "$remote_path") | awk '{print \$1}')\" = $(shell_quote "$local_sha")"; then
    return 0
  fi
  scp -q -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" \
    "$local_path" "${remote}:$(shell_quote "$remote_path")"
}

run_video_loadtest_remote_shard() {
  local shard="$1"
  local host="$2"
  local count="$3"
  local device_ids_file="$4"
  local artifact_dir="$5"
  local step_run_id="$6"
  local remote="${ssh_user}@${host}"
  local remote_base="$video_loadtest_remote_dir/$run_id"
  local remote_artifact_dir="$remote_base/runs/$step_run_id/$shard"
  local remote_device_ids="$remote_artifact_dir/device-ids.txt"
  local remote_device_tokens="$remote_artifact_dir/device-token-map.json"
  local remote_app_tokens="$remote_artifact_dir/app-token-map.json"
  local remote_secret_env="$remote_artifact_dir/secrets.env"
  local concurrency="${video_loadtest_concurrency:-$count}"
  if (( concurrency > count )); then
    concurrency="$count"
  fi
  if [[ -z "${VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN:-}" || -z "${VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE:-}" || -z "${VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE:-}" ]]; then
    echo "remote-sharded video loadtest requires VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN, VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE, and VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE from token generation" >&2
    return 1
  fi
  mkdir -p "$artifact_dir"
  ssh -n -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "$remote" \
    "mkdir -p $(shell_quote "$remote_artifact_dir")"
  scp -q -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" \
    "$device_ids_file" "${remote}:$(shell_quote "$remote_device_ids")"
  scp -q -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" \
    "${VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE}" "${remote}:$(shell_quote "$remote_device_tokens")"
  scp -q -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" \
    "${VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE}" "${remote}:$(shell_quote "$remote_app_tokens")"
  printf 'VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN=%q\n' "${VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN:-}" | \
    ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "$remote" \
      "umask 077 && cat > $(shell_quote "$remote_secret_env")"

  local cmd_parts=()
  cmd_parts+=("cd $(shell_quote "$remote_base") &&")
  cmd_parts+=("set -a && . $(shell_quote "$remote_secret_env") && set +a &&")
  cmd_parts+=("./rtk-video-loadtest run")
  cmd_parts+=("--profile $(shell_quote "${VIDEO_CLOUD_LOAD_PROFILE:-safe-staging}")")
  cmd_parts+=("--actors device,viewer")
  cmd_parts+=("--app-route-set $(shell_quote "${VIDEO_CLOUD_LOAD_APP_ROUTE_SET:-smoke}")")
  cmd_parts+=("--device-route-set $(shell_quote "${VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET:-off}")")
  cmd_parts+=("--device-transport-set $(shell_quote "${VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET:-smoke}")")
  cmd_parts+=("--viewer-route-set $(shell_quote "${VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET:-smoke}")")
  cmd_parts+=("--webrtc-media-set $(shell_quote "${VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET:-$video_loadtest_media_set}")")
  cmd_parts+=("--webrtc-relay-role both")
  cmd_parts+=("--webrtc-ice-policy $(shell_quote "${VIDEO_CLOUD_LOAD_WEBRTC_ICE_POLICY:-$video_loadtest_ice_policy}")")
  cmd_parts+=("--clip-set off --mqtt-set off")
  cmd_parts+=("--api-url $(shell_quote "${VIDEO_CLOUD_LOAD_API_URL:-$video_cloud_public_url}")")
  cmd_parts+=("--run-id $(shell_quote "$step_run_id-$shard")")
  cmd_parts+=("--instance-id $(shell_quote "$shard")")
  cmd_parts+=("--contracts-commit $(shell_quote "$(git -C "$repo_root/repos/rtk_cloud_contracts_doc" rev-parse HEAD 2>/dev/null || true)")")
  cmd_parts+=("--client-commit $(shell_quote "$(git -C "$repo_root" rev-parse HEAD 2>/dev/null || true)")")
  cmd_parts+=("--server-commit unknown")
  cmd_parts+=("--duration $(shell_quote "${VIDEO_CLOUD_LOAD_DURATION:-$video_loadtest_duration}")")
  cmd_parts+=("--webrtc-media-duration $(shell_quote "${VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_DURATION:-$video_loadtest_media_duration}")")
  cmd_parts+=("--http-timeout $(shell_quote "${VIDEO_CLOUD_LOAD_HTTP_TIMEOUT:-60s}")")
  cmd_parts+=("--device-online-settle $(shell_quote "${VIDEO_CLOUD_LOAD_DEVICE_ONLINE_SETTLE:-${video_loadtest_device_online_settle:-2s}}")")
  cmd_parts+=("--device-owner-connect-retries $(shell_quote "${VIDEO_CLOUD_LOAD_DEVICE_OWNER_CONNECT_RETRIES:-3}")")
  cmd_parts+=("--virtual-devices $count --virtual-viewers $count --iterations 1")
  cmd_parts+=("--app-concurrency 0 --device-concurrency $concurrency --viewer-concurrency $concurrency")
  cmd_parts+=("--app-rate 0 --device-rate 0 --viewer-rate 0")
  cmd_parts+=("--device-token-map-file $(shell_quote "$remote_device_tokens")")
  cmd_parts+=("--app-token-map-file $(shell_quote "$remote_app_tokens")")
  cmd_parts+=("--device-ids-file $(shell_quote "$remote_device_ids")")
  cmd_parts+=("--output $(shell_quote "$remote_artifact_dir/load-results.json")")
  cmd_parts+=("--report-output $(shell_quote "$remote_artifact_dir/load-report.md")")

  local remote_cmd stdout_log stderr_log status=0
  remote_cmd="${cmd_parts[*]}"
  stdout_log="$remote_artifact_dir/stdout.log"
  stderr_log="$remote_artifact_dir/stderr.log"
  set +e
  ssh -n -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "$remote" \
    "{ $remote_cmd; } >$(shell_quote "$stdout_log") 2>$(shell_quote "$stderr_log")"
  status=$?
  set -e
  local file
  for file in load-results.json load-report.md metadata.json stdout.log stderr.log; do
    scp -q -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" \
      "${remote}:$(shell_quote "$remote_artifact_dir")/$file" "$artifact_dir/$file" >/dev/null 2>&1 || true
  done
  return "$status"
}

run_video_loadtest_once_remote_sharded() {
  local artifact_dir="$1"
  local viewers="$2"
  local devices="$3"
  local step_run_id="$4"
  mkdir -p "$artifact_dir"
  ensure_video_loadtest_binary
  local sampler_pid=""
  sampler_pid="$(start_coturn_active_sampler "$artifact_dir" || true)"
  local host_rows plan_file
  host_rows="$(video_loadtest_remote_host_rows)"
  plan_file="$(write_video_loadtest_shard_plan "$devices" "$artifact_dir" "$host_rows")"
  ensure_video_loadtest_tokens_for_ids "$artifact_dir/device-ids.txt" "$artifact_dir/token-env.sh"
  local installed_hosts_file="$artifact_dir/.installed-hosts"
  : >"$installed_hosts_file"
  local install_host
  while IFS= read -r install_host; do
    [[ -z "$install_host" ]] && continue
    echo "installing remote video loadtest host=$install_host step=$(basename "$artifact_dir")" >&2
    install_video_loadtest_remote_host "$install_host"
    printf '%s\n' "$install_host" >>"$installed_hosts_file"
  done < <(awk -F '\t' 'NR > 1 && $3 != "" && !seen[$3]++ { print $3 }' "$plan_file")
  local shard label host count ids_file shard_dir mqtt_devices video_viewers video_ratio
  local pids=()
  local labels=()
  local rc=0
  while IFS=$'\t' read -r shard label host count ids_file shard_dir mqtt_devices video_viewers video_ratio; do
    [[ "$shard" == "shard" ]] && continue
    echo "running remote video shard step=$(basename "$artifact_dir") shard=$shard host=$host mqtt_devices=${mqtt_devices:-unknown} viewers=$count video_ratio=${video_ratio:-unknown} artifact_dir=$shard_dir" >&2
    run_video_loadtest_remote_shard "$shard" "$host" "$count" "$ids_file" "$shard_dir" "$step_run_id" &
    pids+=("$!")
    labels+=("$shard@$host")
  done <"$plan_file"
  local idx status
  for idx in "${!pids[@]}"; do
    status=0
    wait "${pids[$idx]}" || status=$?
    if [[ "$status" -ne 0 ]]; then
      echo "remote video shard failed: ${labels[$idx]} rc=$status" >&2
      rc="$status"
    fi
  done
  if [[ -n "$sampler_pid" ]]; then
    kill "$sampler_pid" >/dev/null 2>&1 || true
    wait "$sampler_pid" >/dev/null 2>&1 || true
  fi
  return "$rc"
}

run_video_loadtest_once() {
  local artifact_dir="$1"
  local viewers="$2"
  local devices="$3"
  local step_run_id="$4"
  if [[ "$video_loadtest_mode" == "remote-sharded" ]]; then
    run_video_loadtest_once_remote_sharded "$artifact_dir" "$viewers" "$devices" "$step_run_id"
    return $?
  fi
  if [[ "$video_loadtest_mode" != "local" ]]; then
    echo "invalid HOME100K_VIDEO_LOADTEST_MODE: $video_loadtest_mode" >&2
    return 2
  fi
  mkdir -p "$artifact_dir"
  local sampler_pid=""
  sampler_pid="$(start_coturn_active_sampler "$artifact_dir" || true)"
  local step_device_ids=""
  step_device_ids="$(video_loadtest_first_device_ids "$devices")"
  local step_device_count
  step_device_count="$(video_loadtest_device_id_count "$step_device_ids")"
  if (( step_device_count < devices )); then
    echo "video loadtest device inventory insufficient for step: requested_devices=$devices available_devices=$step_device_count; check HOME100K_BRAND_PLAN/HOME100K_VIDEO_LOADTEST_BRANDNAME token inventory" >&2
    if [[ -n "$sampler_pid" ]]; then
      kill "$sampler_pid" >/dev/null 2>&1 || true
      wait "$sampler_pid" >/dev/null 2>&1 || true
    fi
    return 1
  fi
  local device_concurrency="${VIDEO_CLOUD_LOAD_DEVICE_CONCURRENCY:-${video_loadtest_concurrency:-$devices}}"
  local viewer_concurrency="${VIDEO_CLOUD_LOAD_VIEWER_CONCURRENCY:-${video_loadtest_concurrency:-$viewers}}"
  local rc=0
  VIDEO_CLOUD_LOAD_RUN_ID="${VIDEO_CLOUD_LOAD_RUN_ID:-$step_run_id}" \
  VIDEO_CLOUD_LOAD_ARTIFACT_DIR="$artifact_dir" \
  VIDEO_CLOUD_LOAD_PROFILE="${VIDEO_CLOUD_LOAD_PROFILE:-safe-staging}" \
  VIDEO_CLOUD_LOAD_ACTORS="${VIDEO_CLOUD_LOAD_ACTORS:-device,viewer}" \
  VIDEO_CLOUD_LOAD_APP_ROUTE_SET="${VIDEO_CLOUD_LOAD_APP_ROUTE_SET:-smoke}" \
  VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET="${VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET:-off}" \
  VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET="${VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET:-smoke}" \
  VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET="${VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET:-smoke}" \
  VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET="${VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET:-$video_loadtest_media_set}" \
  VIDEO_CLOUD_LOAD_WEBRTC_ICE_POLICY="${VIDEO_CLOUD_LOAD_WEBRTC_ICE_POLICY:-$video_loadtest_ice_policy}" \
  VIDEO_CLOUD_LOAD_DURATION="${VIDEO_CLOUD_LOAD_DURATION:-$video_loadtest_duration}" \
  VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_DURATION="${VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_DURATION:-$video_loadtest_media_duration}" \
  VIDEO_CLOUD_LOAD_DEVICE_ONLINE_SETTLE="${VIDEO_CLOUD_LOAD_DEVICE_ONLINE_SETTLE:-$video_loadtest_device_online_settle}" \
  VIDEO_CLOUD_LOAD_HTTP_TIMEOUT="${VIDEO_CLOUD_LOAD_HTTP_TIMEOUT:-60s}" \
  VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES="$devices" \
  VIDEO_CLOUD_LOAD_VIRTUAL_VIEWERS="$viewers" \
  VIDEO_CLOUD_LOAD_DEVICE_IDS="$step_device_ids" \
  VIDEO_CLOUD_LOAD_DEVICE_CONCURRENCY="$device_concurrency" \
  VIDEO_CLOUD_LOAD_VIEWER_CONCURRENCY="$viewer_concurrency" \
  VIDEO_CLOUD_LOAD_API_URL="${VIDEO_CLOUD_LOAD_API_URL:-$video_cloud_public_url}" \
  "$video_loadtest_script" || rc=$?
  if [[ -n "$sampler_pid" ]]; then
    kill "$sampler_pid" >/dev/null 2>&1 || true
    wait "$sampler_pid" >/dev/null 2>&1 || true
  fi
  return "$rc"
}

run_video_loadtest_step() {
  if ! video_loadtest_enabled; then
    return 0
  fi
  if [[ ! -x "$video_loadtest_script" ]]; then
    echo "video loadtest script not executable: $video_loadtest_script" >&2
    return 1
  fi
  set_phase "run-video-loadtest"
  check_video_loadtest_remote_capacity || return $?
  if [[ "$video_loadtest_mode" != "remote-sharded" ]]; then
    local max_devices
    max_devices="$(max_video_token_devices)"
    ensure_video_loadtest_tokens "$max_devices" || return $?
  fi
  if [[ -z "$video_loadtest_ladder" ]]; then
    run_video_loadtest_once "$video_loadtest_artifact_dir" "$video_loadtest_viewers" "$video_loadtest_devices" "$run_id"
    return $?
  fi
  local item step_dir rc=0
  IFS=',' read -ra ladder_items <<<"$video_loadtest_ladder"
  for item in "${ladder_items[@]}"; do
    item="${item//[[:space:]]/}"
    step_dir="$video_loadtest_artifact_dir/step-${item}"
    echo "running video loadtest ladder step viewers=$item artifact_dir=$step_dir" >&2
    run_video_loadtest_once "$step_dir" "$item" "$item" "$run_id-video-${item}" || rc=$?
    if [[ "$rc" -ne 0 ]]; then
      return "$rc"
    fi
    if [[ "$video_loadtest_step_cooldown" != "0" && "$video_loadtest_step_cooldown" != "0s" ]]; then
      sleep "$video_loadtest_step_cooldown"
    fi
  done
}

clip_storage_loadtest_enabled() {
  case "$clip_storage_loadtest" in
    off|false|0) return 1 ;;
    on|true|1) return 0 ;;
    auto) [[ "$scenario_profile" == "clip-storage-10k-v2" ]] ;;
    *) echo "invalid HOME100K_CLIP_STORAGE_LOADTEST: $clip_storage_loadtest" >&2; return 2 ;;
  esac
}

run_clip_storage_loadtest_step() {
  clip_storage_loadtest_enabled || return $?
  if [[ ! -x "$clip_storage_loadtest_script" ]]; then
    echo "clip storage loadtest script not executable: $clip_storage_loadtest_script" >&2
    return 1
  fi
  if [[ -z "$clip_storage_camera_ids_file" || -z "$clip_storage_token_map_file" ]]; then
    echo "clip storage loadtest requires HOME100K_CLIP_STORAGE_CAMERA_IDS_FILE and HOME100K_CLIP_STORAGE_TOKEN_MAP_FILE" >&2
    return 1
  fi
  set_phase "run-clip-storage-loadtest"
  VIDEO_CLOUD_LOAD_RUN_ID="${VIDEO_CLOUD_LOAD_RUN_ID:-$run_id-clip-storage}" \
  VIDEO_CLOUD_LOAD_ARTIFACT_DIR="$clip_storage_loadtest_artifact_dir" \
  VIDEO_CLOUD_LOAD_CLIP_DEVICE_IDS_FILE="$clip_storage_camera_ids_file" \
  VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE="$clip_storage_token_map_file" \
  VIDEO_CLOUD_LOAD_CLIP_FIXTURE="$clip_storage_fixture" \
  VIDEO_CLOUD_LOAD_CLIP_THUMBNAIL="$clip_storage_thumbnail" \
  VIDEO_CLOUD_LOAD_CLIP_COUNT_PER_DEVICE="$clip_storage_count_per_camera" \
  VIDEO_CLOUD_LOAD_CLIP_SCHEDULE_WINDOW="$clip_storage_window" \
  VIDEO_CLOUD_LOAD_CLIP_POISSON_SEED="$clip_storage_seed" \
  VIDEO_CLOUD_LOAD_CLIP_UPLOAD_CONCURRENCY="$clip_storage_concurrency" \
  VIDEO_CLOUD_LOAD_API_URL="${VIDEO_CLOUD_LOAD_API_URL:-$video_cloud_public_url}" \
  VIDEO_CLOUD_LOAD_STORAGE_EXEC="${VIDEO_CLOUD_LOAD_STORAGE_EXEC:-kubernetes}" \
  KUBECONFIG="${KUBECONFIG:-$(k8s_kubeconfig)}" \
  "$clip_storage_loadtest_script"
}

collect_clip_storage_evidence() {
  clip_storage_loadtest_enabled || return $?
  if [[ ! -s "$clip_storage_loadtest_artifact_dir/load-results.json" || ! -s "$clip_storage_loadtest_artifact_dir/load-report.md" || ! -s "$clip_storage_loadtest_artifact_dir/reconciliation.json" || ! -s "$clip_storage_loadtest_artifact_dir/s3-checksum-preflight.log" ]]; then
    echo "clip storage evidence is incomplete: expected results, report, reconciliation, and S3 preflight artifacts under $clip_storage_loadtest_artifact_dir" >&2
    return 1
  fi
  cp "$clip_storage_loadtest_artifact_dir/load-results.json" "$clip_storage_loadtest_artifact_dir/clip-storage-evidence.json"
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
    if vm.get("source") == "existing-host":
        continue
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

destroy_live_vms() {
  if [[ "$auto_destroy_on_exit" != "1" && "$auto_destroy_on_exit" != "true" && "$auto_destroy_on_exit" != "TRUE" ]]; then
    return 0
  fi
  if [[ "$preserve_vms" == "1" || "$preserve_vms" == "true" || "$preserve_vms" == "TRUE" ]]; then
    echo "preserving live VMs because HOME100K_PRESERVE_VMS is enabled" >&2
    return 0
  fi
  if [[ ! -f "$local_vm_state_file" || -z "${LINODE_TOKEN:-}" ]]; then
    return 0
  fi
  run_home100k destroy-vms "${base_args[@]}" --run-id "$run_id" --vm-state-file "$local_vm_state_file" --live --confirm-live >/dev/null
}

cleanup_live_vms() {
  shutdown_live_vms || true
  destroy_live_vms || {
    echo "CLEANUP_FAILED: unable to destroy run-created VMs; state=$local_vm_state_file" >&2
    return 1
  }
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
  if [[ "$shutdown_live_vms_on_exit" == "1" && ( "$rc" == "0" || "$shutdown_on_error" == "1" || "$auto_destroy_on_exit" == "1" || "$auto_destroy_on_exit" == "true" || "$auto_destroy_on_exit" == "TRUE" ) ]]; then
    set_phase "shutdown-vms"
    cleanup_live_vms >/dev/null 2>&1 || true
  fi
  exit "$rc"
}

run_live_sync_with_retries() {
  local max_attempts="${HOME100K_SYNC_RETRIES:-3}"
  local retry_delay="${HOME100K_SYNC_RETRY_DELAY_SECONDS:-20}"
  local attempt rc
  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    if run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"; then
      return 0
    else
      rc=$?
    fi
    if (( attempt >= max_attempts )); then
      return "$rc"
    fi
    echo "sync attempt $attempt/$max_attempts failed with rc=$rc; retrying in ${retry_delay}s" >&2
    sleep "$retry_delay"
  done
}

prepare_load_generator_hosts() {
  local host_rows
  host_rows="$(video_loadtest_remote_host_rows)"
  [[ -n "$host_rows" ]] || return 0
  local label host remote
  while IFS=$'\t' read -r label host; do
    [[ -n "$host" ]] || continue
    remote="${ssh_user}@${host}"
    echo "preparing load generator host label=$label host=$host" >&2
    ssh -n -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=$ssh_known_hosts_file" -i "$ssh_key" "$remote" \
      "systemctl stop unattended-upgrades apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service >/dev/null 2>&1 || true; systemctl mask unattended-upgrades apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service >/dev/null 2>&1 || true"
  done <<<"$host_rows"
}

run_video_live_workflow() {
  local resume_mode="${1:-0}"
  shift || true
  local command_name="workflow-video-live"
  if [[ "$resume_mode" == "1" ]]; then
    command_name="workflow-video-resume-live"
  fi
  if [[ "$resume_mode" == "0" && -z "$existing_generator_hosts" && -z "${LINODE_TOKEN:-}" ]]; then
    echo "$command_name requires LINODE_TOKEN" >&2
    exit 2
  fi
  if [[ ! -f "$ssh_key" ]]; then
    echo "$command_name SSH key not found: $ssh_key" >&2
    exit 2
  fi
  if [[ "$resume_mode" == "0" && -z "$existing_generator_hosts" && ! -f "$authorized_key_file" ]]; then
    echo "$command_name authorized public key not found: $authorized_key_file" >&2
    exit 2
  fi
  if [[ "$resume_mode" == "1" && ! -f "$local_vm_state_file" ]]; then
    echo "$command_name requires existing VM state: $out_dir/vms.json" >&2
    exit 2
  fi
  mkdir -p "$local_out_dir"
  rm -f "$ssh_known_hosts_file"
  shutdown_live_vms_on_exit=1
  start_status_monitor
  workflow_rc=0
  ca_bundle_path=""
  if ca_bundle_path="$(device_ca_bundle_path)"; then
    export HOME100K_DEVICE_CLIENT_CA_BUNDLE="$ca_bundle_path"
  else
    echo "CERTIFICATE_CA_MISSING: current device/app CA bundle is required for live workflow" >&2
    workflow_rc=1
  fi
  if [[ "$workflow_rc" -eq 0 ]]; then
    run_fixture_preflight "$ca_bundle_path" || workflow_rc=$?
  fi
  if [[ "$workflow_rc" -eq 0 ]]; then
    run_single_device_smoke || workflow_rc=$?
  fi
  if [[ "$resume_mode" == "0" ]]; then
    if [[ "$workflow_rc" -eq 0 ]]; then
      linode_active_service_preflight
      set_phase "provision-vms"
      run_home100k provision-vms "${provision_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --live --confirm-live --authorized-key-file "$authorized_key_file" "$@" || workflow_rc=$?
    fi
    workflow_status
  fi
  if [[ "$workflow_rc" -eq 0 ]]; then
    set_phase "sync"
    run_live_sync_with_retries || workflow_rc=$?
  fi
  if [[ "$workflow_rc" -eq 0 ]]; then
    prepare_load_generator_hosts || workflow_rc=$?
  fi
  workflow_status
  if [[ "$workflow_rc" -eq 0 ]]; then
    set_phase "collect-server-baseline"
    export_kubeconfig_if_available
    rm -f "$local_out_dir/server-evidence-baseline.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --server-evidence-file "$local_out_dir/server-evidence-baseline.json" --live || workflow_rc=$?
  fi
  workflow_status
  if [[ "$workflow_rc" -eq 0 ]]; then
    set_phase "run-video-loadtest"
    run_video_loadtest_step || workflow_rc=$?
  fi
  if [[ "$workflow_rc" -eq 0 ]]; then
    run_clip_storage_loadtest_step || workflow_rc=$?
  fi
  if [[ "$workflow_rc" -eq 0 ]]; then
    collect_clip_storage_evidence || workflow_rc=$?
  fi
  workflow_status
  set_phase "collect-server-evidence"
  export_kubeconfig_if_available
  run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --live || true
  set_phase "aggregate"
  run_home100k aggregate "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" || workflow_rc=$?
  generate_report_from_artifacts
  report_status="$(current_report_status)"
  report_result="$(current_report_result)"
  if [[ "$workflow_rc" -eq 0 && ( "$report_status" != "COMPLETE" || "$report_result" != "SUCCESS" ) ]]; then
    workflow_rc=1
    echo "report status is $report_status result is $report_result; preserving VMs for investigation" >&2
  fi
  cleanup_rc=0
  if should_shutdown_after_workflow || [[ "$workflow_rc" -ne 0 ]]; then
    set_phase "shutdown-vms"
    cleanup_live_vms || cleanup_rc=$?
  else
    set_phase "preserve-vms"
    echo "preserving live VMs for resume/debug; run shutdown-vms when finished" >&2
  fi
  shutdown_live_vms_on_exit=0
  set_phase "complete"
  stop_status_monitor
  echo "live WebRTC video workflow artifacts: $out_dir"
  if [[ "$workflow_rc" -ne 0 ]]; then
    exit "$workflow_rc"
  fi
  exit "$cleanup_rc"
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
if [[ -n "$video_generator_vm_count" ]]; then
  base_args+=("--video-generator-vm-count" "$video_generator_vm_count")
fi
if [[ -n "$video_generator_label_prefix" ]]; then
  base_args+=("--video-generator-label-prefix" "$video_generator_label_prefix")
fi
base_args+=("--load-generator-devices-per-vm" "$load_generator_devices_per_vm")
provision_args=("${base_args[@]}")
if [[ -n "$linode_type" ]]; then
  provision_args+=("--linode-type" "$linode_type")
fi
if [[ -n "$existing_generator_hosts" ]]; then
  provision_args+=("--existing-hosts" "$existing_generator_hosts")
fi
plan_condition_args=(
  "${base_args[@]}"
  "--runner-nofile-limit" "$runner_nofile_limit"
  "--device-session-model" "$device_session_model"
  "--runner-read-model" "$runner_read_model"
  "--device-token-request-timeout" "$device_token_request_timeout"
  "--device-token-request-retries" "$device_token_request_retries"
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
workflow_args+=("--device-token-request-timeout" "$device_token_request_timeout")
workflow_args+=("--device-token-request-retries" "$device_token_request_retries")
workflow_args+=("--runtime-logs=$runtime_logs")
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
  preflight)
    ca_bundle="$(device_ca_bundle_path || true)"
    if [[ -z "$ca_bundle" ]]; then
      echo "CERTIFICATE_CA_MISSING: current device/app CA bundle not found" >&2
      exit 1
    fi
    run_home100k preflight "${base_args[@]}" --ca-bundle "$ca_bundle" "$@"
    ;;
  dry-run)
    mkdir -p "$local_out_dir"
    run_home100k run "${plan_condition_args[@]}" --ephemeral-vms --run-id "$run_id" --out-dir "$local_out_dir" "$@"
    ;;
  provision-vms)
    mkdir -p "$local_out_dir"
    run_home100k provision-vms "${provision_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" "$@"
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
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --ssh-key "$ssh_key" "$@"
    ;;
  run-video-loadtest)
    run_video_loadtest_step "$@"
    ;;
  run-clip-storage-loadtest)
    run_clip_storage_loadtest_step "$@"
    ;;
  collect-clip-storage-evidence)
    collect_clip_storage_evidence "$@"
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
    run_home100k provision-vms "${provision_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir"
    run_home100k sync "${workflow_args[@]}" --run-id "$run_id" --vm-state-file "$local_vm_state_file"
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --vm-state-file "$local_vm_state_file" --runner-mode sample
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir"
    ;;
  workflow-live)
    if [[ -z "$existing_generator_hosts" && -z "${LINODE_TOKEN:-}" ]]; then
      echo "workflow-live requires LINODE_TOKEN" >&2
      exit 2
    fi
    if [[ ! -f "$ssh_key" ]]; then
      echo "workflow-live SSH key not found: $ssh_key" >&2
      exit 2
    fi
    if [[ -z "$existing_generator_hosts" && ! -f "$authorized_key_file" ]]; then
      echo "workflow-live authorized public key not found: $authorized_key_file" >&2
      exit 2
    fi
    mkdir -p "$local_out_dir"
    rm -f "$ssh_known_hosts_file"
    shutdown_live_vms_on_exit=1
    start_status_monitor
    workflow_rc=0
    ca_bundle_path=""
    if ca_bundle_path="$(device_ca_bundle_path)"; then
      export HOME100K_DEVICE_CLIENT_CA_BUNDLE="$ca_bundle_path"
    else
      echo "CERTIFICATE_CA_MISSING: current device/app CA bundle is required for live workflow" >&2
      workflow_rc=1
    fi
    if [[ "$workflow_rc" -eq 0 ]]; then
      run_fixture_preflight "$ca_bundle_path" || workflow_rc=$?
    fi
    if [[ "$workflow_rc" -eq 0 ]]; then
      run_single_device_smoke || workflow_rc=$?
    fi
    if [[ "$workflow_rc" -ne 0 ]]; then
      set_phase "preflight-failed"
      workflow_status
      shutdown_live_vms_on_exit=0
      stop_status_monitor
      exit "$workflow_rc"
    fi
    linode_active_service_preflight
    set_phase "provision-vms"
    run_home100k provision-vms "${provision_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --live --confirm-live --authorized-key-file "$authorized_key_file" "$@"
    workflow_status
    set_phase "sync"
    run_live_sync_with_retries
    prepare_load_generator_hosts
    workflow_status
    set_phase "collect-server-baseline"
    export_kubeconfig_if_available
    rm -f "$local_out_dir/server-evidence-baseline.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --server-evidence-file "$local_out_dir/server-evidence-baseline.json" --live
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    clip_storage_pid=""
    if clip_storage_loadtest_enabled; then
      run_clip_storage_loadtest_step &
      clip_storage_pid=$!
    fi
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ -n "$clip_storage_pid" ]]; then
      clip_storage_rc=0
      wait "$clip_storage_pid" || clip_storage_rc=$?
      if [[ "$clip_storage_rc" -ne 0 ]]; then
        echo "clip storage loadtest returned rc=$clip_storage_rc" >&2
        workflow_rc=$clip_storage_rc
      else
        collect_clip_storage_evidence || workflow_rc=$?
      fi
    fi
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    if [[ "$workflow_rc" -eq 0 ]]; then
      run_video_loadtest_step || workflow_rc=$?
    else
      echo "skipping video loadtest because run-stages returned rc=$workflow_rc" >&2
    fi
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
    if should_shutdown_after_workflow || [[ "$workflow_rc" -ne 0 ]]; then
      set_phase "shutdown-vms"
      cleanup_live_vms || cleanup_rc=$?
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
    run_live_sync_with_retries
    prepare_load_generator_hosts
    workflow_status
    set_phase "collect-server-baseline"
    export_kubeconfig_if_available
    rm -f "$local_out_dir/server-evidence-baseline.json"
    run_home100k collect-server-evidence "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --server-evidence-file "$local_out_dir/server-evidence-baseline.json" --live
    workflow_status
    set_phase "run-stages"
    workflow_rc=0
    clip_storage_pid=""
    if clip_storage_loadtest_enabled; then
      run_clip_storage_loadtest_step &
      clip_storage_pid=$!
    fi
    run_home100k run-stages "${workflow_args[@]}" "${coordinator_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-workspace "$remote_workspace" --remote-env-root "$remote_env_root" --remote-out-root "$remote_out_root" --ssh-key "$ssh_key" --runner-mode "$runner_mode" || workflow_rc=$?
    if [[ -n "$clip_storage_pid" ]]; then
      clip_storage_rc=0
      wait "$clip_storage_pid" || clip_storage_rc=$?
      if [[ "$clip_storage_rc" -ne 0 ]]; then
        echo "clip storage loadtest returned rc=$clip_storage_rc" >&2
        workflow_rc=$clip_storage_rc
      else
        collect_clip_storage_evidence || workflow_rc=$?
      fi
    fi
    if [[ "$workflow_rc" -ne 0 ]]; then
      echo "run-stages returned rc=$workflow_rc; continuing to collect artifacts and generate report" >&2
    fi
    workflow_status
    set_phase "collect"
    run_home100k collect "${workflow_args[@]}" --run-id "$run_id" --out-dir "$local_out_dir" --vm-state-file "$local_vm_state_file" --live --remote-out-root "$remote_out_root" --ssh-key "$ssh_key"
    if [[ "$workflow_rc" -eq 0 ]]; then
      run_video_loadtest_step || workflow_rc=$?
    else
      echo "skipping video loadtest because run-stages returned rc=$workflow_rc" >&2
    fi
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
  workflow-video-live)
    run_video_live_workflow 0 "$@"
    ;;
  workflow-video-resume-live)
    run_video_live_workflow 1 "$@"
    ;;
  *)
    echo "unknown command: $command" >&2
    usage >&2
    exit 2
    ;;
esac
