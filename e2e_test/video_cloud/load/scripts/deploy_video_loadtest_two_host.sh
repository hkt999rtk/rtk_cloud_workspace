#!/usr/bin/env bash
set -euo pipefail

workspace_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
load_root="${workspace_root}/e2e_test/video_cloud/load"
e2e_root="${workspace_root}/e2e_test"

dry_run=0
device_host="${VIDEO_CLOUD_LOAD_DEVICE_HOST:-client-a.local}"
app_host="${VIDEO_CLOUD_LOAD_APP_HOST:-client-b.local}"
remote_user="${VIDEO_CLOUD_LOAD_REMOTE_USER:-root}"
remote_dir="${VIDEO_CLOUD_LOAD_REMOTE_DIR:-/opt/rtk-cloud-loadtest}"
binary="${VIDEO_CLOUD_LOAD_BINARY:-${workspace_root}/.artifacts/e2e_test/video_cloud/load/cd/rtk-video-loadtest-linux-amd64}"
run_id="${VIDEO_CLOUD_LOAD_RUN_ID:-two-host-$(date -u +%Y%m%dT%H%M%SZ)}"
artifact_dir="${VIDEO_CLOUD_LOAD_ARTIFACT_DIR:-${workspace_root}/.artifacts/e2e_test/video_cloud/load/${run_id}/two-host}"
ssh_opts_raw="${VIDEO_CLOUD_LOAD_SSH_OPTS:--o BatchMode=yes -o StrictHostKeyChecking=accept-new}"
read -r -a ssh_opts <<< "$ssh_opts_raw"

usage() {
  cat <<'EOF'
Usage: e2e_test/video_cloud/load/scripts/deploy_video_loadtest_two_host.sh [options]

Deploy the same rtk-video-loadtest Linux binary to two Linux hosts:
  - device host: VIDEO_CLOUD_LOAD_ACTORS=device
  - app host:    VIDEO_CLOUD_LOAD_ACTORS=app,viewer

Options:
  --binary PATH         Linux amd64 rtk-video-loadtest binary
  --device-host HOST    Device actor host (default: client-a.local)
  --app-host HOST       App/viewer actor host (default: client-b.local)
  --remote-user USER    SSH user (default: root)
  --remote-dir PATH     Remote install directory (default: /opt/rtk-cloud-loadtest)
  --artifact-dir PATH   Local artifact collection directory
  --dry-run             Print redacted SSH/SCP commands without executing them
  -h, --help            Show this help

Optional environment:
  VIDEO_CLOUD_LOAD_SSH_OPTS
      SSH/SCP options. Default: -o BatchMode=yes -o StrictHostKeyChecking=accept-new

Required environment for live run:
  VIDEO_CLOUD_LOAD_API_URL
  VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN
  VIDEO_CLOUD_LOAD_ADMIN_TOKEN
  VIDEO_CLOUD_LOAD_DEVICE_TOKEN or VIDEO_CLOUD_LOAD_DEVICE_TOKENS
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --binary)
      binary="$2"
      shift 2
      ;;
    --device-host)
      device_host="$2"
      shift 2
      ;;
    --app-host)
      app_host="$2"
      shift 2
      ;;
    --remote-user)
      remote_user="$2"
      shift 2
      ;;
    --remote-dir)
      remote_dir="$2"
      shift 2
      ;;
    --artifact-dir)
      artifact_dir="$2"
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

api_url="${VIDEO_CLOUD_LOAD_API_URL:-}"
ws_url="${VIDEO_CLOUD_LOAD_WS_URL:-}"
account_token="${VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN:-}"
admin_token="${VIDEO_CLOUD_LOAD_ADMIN_TOKEN:-}"
device_token="${VIDEO_CLOUD_LOAD_DEVICE_TOKEN:-}"
device_tokens="${VIDEO_CLOUD_LOAD_DEVICE_TOKENS:-}"
app_tokens="${VIDEO_CLOUD_LOAD_APP_TOKENS:-}"
refresh_token="${VIDEO_CLOUD_LOAD_REFRESH_TOKEN:-}"
allow_stress="${VIDEO_CLOUD_LOAD_ALLOW_STRESS:-0}"
allow_soak="${VIDEO_CLOUD_LOAD_ALLOW_SOAK:-0}"
profile="${VIDEO_CLOUD_LOAD_PROFILE:-safe-staging}"
app_route_set="${VIDEO_CLOUD_LOAD_APP_ROUTE_SET:-smoke}"
device_route_set="${VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET:-smoke}"
device_transport_set="${VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET:-smoke}"
viewer_route_set="${VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET:-smoke}"
webrtc_media_set="${VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET:-off}"
clip_set="${VIDEO_CLOUD_LOAD_CLIP_SET:-off}"
mqtt_set="${VIDEO_CLOUD_LOAD_MQTT_SET:-off}"
mqtt_addr="${VIDEO_CLOUD_MQTT_ADDR:-}"
mqtt_username="${VIDEO_CLOUD_MQTT_USERNAME:-}"
mqtt_password="${VIDEO_CLOUD_MQTT_PASSWORD:-}"
mqtt_topic_root="${VIDEO_CLOUD_MQTT_TOPIC_ROOT:-devices}"
mqtt_device_profile="${VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE:-camera}"
mqtt_iot_mix="${VIDEO_CLOUD_LOAD_MQTT_IOT_MIX:-light=4,air_conditioner=3,smart_meter=3}"
mqtt_required="${VIDEO_CLOUD_LOAD_MQTT_REQUIRED:-0}"
negative_set="${VIDEO_CLOUD_LOAD_NEGATIVE_SET:-off}"
negative_malformed_path="${VIDEO_CLOUD_LOAD_NEGATIVE_MALFORMED_PATH:-}"
negative_timeout_path="${VIDEO_CLOUD_LOAD_NEGATIVE_TIMEOUT_PATH:-}"
duration="${VIDEO_CLOUD_LOAD_DURATION:-30s}"
virtual_devices="${VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES:-1}"
virtual_viewers="${VIDEO_CLOUD_LOAD_VIRTUAL_VIEWERS:-1}"
iterations="${VIDEO_CLOUD_LOAD_ITERATIONS:-1}"
app_rate="${VIDEO_CLOUD_LOAD_APP_RATE:-0}"
device_rate="${VIDEO_CLOUD_LOAD_DEVICE_RATE:-0}"
viewer_rate="${VIDEO_CLOUD_LOAD_VIEWER_RATE:-0}"
device_prefix="${VIDEO_CLOUD_LOAD_DEVICE_PREFIX:-load-device}"
device_online_mode="${VIDEO_CLOUD_LOAD_DEVICE_ONLINE_MODE:-websocket}"
device_warmup_seconds="${VIDEO_CLOUD_LOAD_DEVICE_WARMUP_SECONDS:-3}"
device_tail_seconds="${VIDEO_CLOUD_LOAD_DEVICE_TAIL_SECONDS:-15}"
device_ids="${VIDEO_CLOUD_LOAD_DEVICE_IDS:-}"
contracts_commit="${VIDEO_CLOUD_LOAD_CONTRACTS_COMMIT:-$(git -C "${workspace_root}/repos/rtk_cloud_contracts_doc" rev-parse HEAD 2>/dev/null || true)}"
server_commit="${VIDEO_CLOUD_LOAD_SERVER_COMMIT:-unknown}"
client_commit="${VIDEO_CLOUD_LOAD_CLIENT_COMMIT:-$(git -C "${workspace_root}" rev-parse HEAD 2>/dev/null || true)}"

mkdir -p "$artifact_dir"

fail() {
  echo "error: $*" >&2
  exit 1
}

write_failure_report() {
  local phase="$1"
  local reason="$2"
  local detail="${3:-}"
  local dir="${artifact_dir}/${phase}"
  mkdir -p "$dir"
  local redacted_reason
  local redacted_detail
  redacted_reason="$(redact "$reason")"
  redacted_detail="$(redact "$detail")"
  cat > "${dir}/failure-report.md" <<EOF
# Two-Host Load-Test Failure

| Field | Value |
| --- | --- |
| Status | FAIL |
| Phase | ${phase} |
| Run ID | ${run_id} |
| Device host | ${device_host} |
| App/viewer host | ${app_host} |
| API URL | ${api_url:-<unset>} |
| Client commit | ${client_commit:-unknown} |
| Contracts commit | ${contracts_commit:-unknown} |
| Server commit | ${server_commit:-unknown} |

## Reason

${redacted_reason}

## Detail

\`\`\`text
${redacted_detail}
\`\`\`
EOF
  python3 - "$dir/failure-report.json" "$phase" "$run_id" "$device_host" "$app_host" \
    "${api_url:-}" "${client_commit:-unknown}" "${contracts_commit:-unknown}" \
    "${server_commit:-unknown}" "$redacted_reason" "$redacted_detail" <<'PY'
import json
import sys

path, phase, run_id, device_host, app_host, api_url, client_commit, contracts_commit, server_commit, reason, detail = sys.argv[1:]
with open(path, "w", encoding="utf-8") as fh:
    json.dump(
        {
            "status": "FAIL",
            "phase": phase,
            "run_id": run_id,
            "device_host": device_host,
            "app_host": app_host,
            "api_url": api_url,
            "client_commit": client_commit,
            "contracts_commit": contracts_commit,
            "server_commit": server_commit,
            "reason": reason,
            "detail": detail,
        },
        fh,
        indent=2,
        sort_keys=True,
    )
    fh.write("\n")
PY
}

fail_with_report() {
  local phase="$1"
  local reason="$2"
  local detail="${3:-}"
  write_failure_report "$phase" "$reason" "$detail"
  fail "$reason"
}

quote() {
  printf "%q" "$1"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

duration_plus_seconds() {
  python3 - "$1" "$2" "$3" <<'PY'
import re
import sys

raw, warmup, tail = sys.argv[1], float(sys.argv[2]), float(sys.argv[3])
units = {
    "ns": 1e-9,
    "us": 1e-6,
    "µs": 1e-6,
    "ms": 1e-3,
    "s": 1,
    "m": 60,
    "h": 3600,
}
pos = 0
seconds = 0.0
for match in re.finditer(r"([0-9]+(?:\.[0-9]+)?)(ns|us|µs|ms|s|m|h)", raw):
    if match.start() != pos:
        raise SystemExit(f"unsupported duration: {raw}")
    seconds += float(match.group(1)) * units[match.group(2)]
    pos = match.end()
if pos != len(raw) or seconds <= 0:
    raise SystemExit(f"unsupported duration: {raw}")
total = int(seconds + warmup + tail + 0.999)
print(f"{total}s")
PY
}

json_secret_values() {
  python3 - "$1" <<'PY'
import json
import sys

raw = sys.argv[1]
if not raw:
    raise SystemExit(0)
try:
    values = json.loads(raw)
except Exception:
    raise SystemExit(0)
if isinstance(values, dict):
    for value in values.values():
        if isinstance(value, str) and value:
            print(value)
PY
}

redact() {
  local text="$1"
  local secrets=("$account_token" "$admin_token" "$device_token" "$device_tokens" "$app_tokens" "$refresh_token" "$mqtt_username" "$mqtt_password")
  local map_secret
  while IFS= read -r map_secret; do
    secrets+=("$map_secret")
  done < <(json_secret_values "$device_tokens")
  while IFS= read -r map_secret; do
    secrets+=("$map_secret")
  done < <(json_secret_values "$app_tokens")
  for secret in "${secrets[@]}"; do
    if [ -n "$secret" ]; then
      text="${text//$secret/<redacted>}"
    fi
  done
  printf '%s\n' "$text"
}

redact_file() {
  local file="$1"
  [ -f "$file" ] || return 0
  local tmp="${file}.redacted.$$"
  while IFS= read -r line || [ -n "$line" ]; do
    redact "$line"
  done < "$file" > "$tmp"
  mv "$tmp" "$file"
}

run_cmd() {
  if [ "$dry_run" -eq 1 ]; then
    redact "+ $*"
  else
    "$@"
  fi
}

ssh_cmd() {
  ssh "${ssh_opts[@]}" "$@"
}

scp_cmd() {
  scp "${ssh_opts[@]}" "$@"
}

run_ssh() {
  if [ "$dry_run" -eq 1 ]; then
    redact "+ ssh ${ssh_opts[*]} $*"
  else
    ssh_cmd "$@"
  fi
}

run_scp() {
  if [ "$dry_run" -eq 1 ]; then
    redact "+ scp ${ssh_opts[*]} $*"
  else
    scp_cmd "$@"
  fi
}

[ -n "$device_host" ] || fail_with_report "preflight" "missing device host"
[ -n "$app_host" ] || fail_with_report "preflight" "missing app/viewer host"
[ -f "$binary" ] || fail_with_report "preflight" "missing load-test binary: $binary"

if [ "$dry_run" -eq 0 ]; then
  missing=()
  [ -n "$api_url" ] || missing+=("VIDEO_CLOUD_LOAD_API_URL")
  [ -n "$account_token" ] || missing+=("VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN")
  [ -n "$admin_token" ] || missing+=("VIDEO_CLOUD_LOAD_ADMIN_TOKEN")
  if [ -z "$device_token" ] && [ -z "$device_tokens" ]; then
    missing+=("VIDEO_CLOUD_LOAD_DEVICE_TOKEN or VIDEO_CLOUD_LOAD_DEVICE_TOKENS")
  fi
  if [ "${#missing[@]}" -gt 0 ]; then
    fail_with_report "preflight" "missing required env: ${missing[*]}"
  fi
fi

binary_sha256="$(sha256_file "$binary")"
device_duration="${VIDEO_CLOUD_LOAD_DEVICE_HOLD_DURATION:-$(duration_plus_seconds "$duration" "$device_warmup_seconds" "$device_tail_seconds")}"

verify_binary_checksum() {
  local checksum_file="${VIDEO_CLOUD_LOAD_BINARY_SHA256SUMS:-$(dirname "$binary")/SHA256SUMS}"
  local expected="${VIDEO_CLOUD_LOAD_BINARY_SHA256:-}"
  if [ -n "$expected" ] && [ "$expected" != "$binary_sha256" ]; then
    fail_with_report "preflight" "load-test binary checksum mismatch" "expected ${expected}, got ${binary_sha256}"
  fi
  if [ -z "$expected" ] && [ -f "$checksum_file" ]; then
    local checksum_path
    case "$checksum_file" in
      /*) checksum_path="$checksum_file" ;;
      *) checksum_path="$(pwd)/$checksum_file" ;;
    esac
    local base
    base="$(basename "$binary")"
    if awk '{print $2}' "$checksum_file" | sed 's/^\*//' | grep -Fxq "$base"; then
      if ! (cd "$(dirname "$binary")" && sha256sum -c "$checksum_path" --ignore-missing >/tmp/rtk-loadtest-sha256-check.$$ 2>&1); then
        local detail
        detail="$(cat /tmp/rtk-loadtest-sha256-check.$$ 2>/dev/null || true)"
        rm -f /tmp/rtk-loadtest-sha256-check.$$
        fail_with_report "preflight" "load-test binary checksum verification failed" "$detail"
      fi
      rm -f /tmp/rtk-loadtest-sha256-check.$$
    fi
  fi
}

parse_api_endpoint() {
  python3 - "$api_url" <<'PY'
import sys
from urllib.parse import urlparse

raw = sys.argv[1]
parsed = urlparse(raw)
if not parsed.scheme or not parsed.hostname:
    raise SystemExit(f"invalid VIDEO_CLOUD_LOAD_API_URL: {raw}")
port = parsed.port
if port is None:
    port = 443 if parsed.scheme == "https" else 80
print(parsed.hostname, port)
PY
}

preflight_remote_host() {
  local host="$1"
  local role="$2"
  local remote="${remote_user}@${host}"
  local endpoint
  endpoint="$(parse_api_endpoint)" || fail_with_report "preflight" "invalid VIDEO_CLOUD_LOAD_API_URL" "$api_url"
  local api_host api_port
  read -r api_host api_port <<< "$endpoint"

  run_ssh "$remote" "true" || fail_with_report "preflight" "SSH preflight failed for ${role} host ${host}" "ssh ${ssh_opts[*]} ${remote} true"

  local resolve_cmd
  resolve_cmd="host=$(quote "$api_host"); case \"\$host\" in *.local) grep -E \"(^|[[:space:]])\${host}([[:space:]]|$)\" /etc/hosts >/dev/null ;; *) getent hosts \"\$host\" >/dev/null 2>&1 || python3 -c 'import socket,sys; socket.getaddrinfo(sys.argv[1], None)' \"\$host\" ;; esac"
  run_ssh "$remote" "$resolve_cmd" || fail_with_report "preflight" "API host resolution preflight failed on ${role} host ${host}" "host=${api_host}; for .local names the static Go runner requires a stable /etc/hosts or DNS-resolvable name"

  local tcp_cmd
  tcp_cmd="host=$(quote "$api_host"); port=$(quote "$api_port"); if command -v python3 >/dev/null 2>&1; then python3 -c 'import socket,sys; s=socket.create_connection((sys.argv[1], int(sys.argv[2])), timeout=5); s.close()' \"\$host\" \"\$port\"; elif command -v nc >/dev/null 2>&1; then nc -z -w 5 \"\$host\" \"\$port\"; else timeout 5 bash -c '</dev/tcp/\$0/\$1' \"\$host\" \"\$port\"; fi"
  run_ssh "$remote" "$tcp_cmd" || fail_with_report "preflight" "API TCP preflight failed on ${role} host ${host}" "host=${api_host} port=${api_port}"
}

install_binary() {
  local host="$1"
  local remote="${remote_user}@${host}"
  run_ssh "$remote" "mkdir -p $(quote "$remote_dir")"
  run_scp "$binary" "${remote}:$(quote "$remote_dir")/rtk-video-loadtest"
  run_ssh "$remote" "chmod 755 $(quote "$remote_dir")/rtk-video-loadtest && printf '%s  rtk-video-loadtest\n' $(quote "$binary_sha256") > $(quote "$remote_dir")/SHA256SUMS"
}

write_remote_metadata() {
  local host="$1"
  local role="$2"
  local actors="$3"
  local instance_id="$4"
  local remote="${remote_user}@${host}"
  local remote_artifact_dir="${remote_dir}/runs/${run_id}/${role}"
  local metadata
  metadata="$(cat <<EOF
{
  "run_id": "${run_id}",
  "instance_id": "${instance_id}",
  "host": "${host}",
  "host_role": "${role}",
  "actors": "${actors}",
  "app_route_set": "${app_route_set}",
  "device_route_set": "${device_route_set}",
  "device_transport_set": "${device_transport_set}",
  "viewer_route_set": "${viewer_route_set}",
  "webrtc_media_set": "${webrtc_media_set}",
  "clip_set": "${clip_set}",
  "device_ids": "${device_ids}",
  "mqtt_set": "${mqtt_set}",
  "mqtt_addr": "${mqtt_addr}",
  "mqtt_device_profile": "${mqtt_device_profile}",
  "mqtt_iot_mix": "${mqtt_iot_mix}",
  "mqtt_required": "${mqtt_required}",
  "negative_set": "${negative_set}",
  "api_url": "${api_url}",
  "server_commit": "${server_commit}",
  "client_commit": "${client_commit}",
  "contracts_commit": "${contracts_commit}",
  "binary_sha256": "${binary_sha256}"
}
EOF
)"
  if [ "$dry_run" -eq 1 ]; then
    redact "+ ssh ${remote} mkdir -p $(quote "$remote_artifact_dir") && write metadata.json"
  else
    ssh_cmd "$remote" "mkdir -p $(quote "$remote_artifact_dir")"
    printf '%s\n' "$metadata" | ssh_cmd "$remote" "cat > $(quote "$remote_artifact_dir")/metadata.json"
  fi
}

remote_run_command() {
  local role="$1"
  local actors="$2"
  local instance_id="$3"
  local role_duration="$4"
  local remote_artifact_dir="${remote_dir}/runs/${run_id}/${role}"
  local cmd=()
  cmd+=("VIDEO_CLOUD_LOAD_API_URL=$(quote "$api_url")")
  cmd+=("VIDEO_CLOUD_LOAD_WS_URL=$(quote "$ws_url")")
  cmd+=("VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN=$(quote "$account_token")")
  cmd+=("VIDEO_CLOUD_LOAD_ADMIN_TOKEN=$(quote "$admin_token")")
  cmd+=("VIDEO_CLOUD_LOAD_DEVICE_TOKEN=$(quote "$device_token")")
  cmd+=("VIDEO_CLOUD_LOAD_DEVICE_TOKENS=$(quote "$device_tokens")")
  cmd+=("VIDEO_CLOUD_LOAD_APP_TOKENS=$(quote "$app_tokens")")
  cmd+=("VIDEO_CLOUD_LOAD_REFRESH_TOKEN=$(quote "$refresh_token")")
  cmd+=("VIDEO_CLOUD_LOAD_ALLOW_STRESS=$(quote "$allow_stress")")
  cmd+=("VIDEO_CLOUD_LOAD_ALLOW_SOAK=$(quote "$allow_soak")")
  cmd+=("VIDEO_CLOUD_LOAD_ACTORS=$(quote "$actors")")
  cmd+=("VIDEO_CLOUD_LOAD_APP_ROUTE_SET=$(quote "$app_route_set")")
  cmd+=("VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET=$(quote "$device_route_set")")
  cmd+=("VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET=$(quote "$device_transport_set")")
  cmd+=("VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET=$(quote "$viewer_route_set")")
  cmd+=("VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET=$(quote "$webrtc_media_set")")
  cmd+=("VIDEO_CLOUD_LOAD_CLIP_SET=$(quote "$clip_set")")
  cmd+=("VIDEO_CLOUD_LOAD_MQTT_SET=$(quote "$mqtt_set")")
  cmd+=("VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE=$(quote "$mqtt_device_profile")")
  cmd+=("VIDEO_CLOUD_LOAD_MQTT_IOT_MIX=$(quote "$mqtt_iot_mix")")
  cmd+=("VIDEO_CLOUD_LOAD_MQTT_REQUIRED=$(quote "$mqtt_required")")
  cmd+=("VIDEO_CLOUD_MQTT_ADDR=$(quote "$mqtt_addr")")
  cmd+=("VIDEO_CLOUD_MQTT_USERNAME=$(quote "$mqtt_username")")
  cmd+=("VIDEO_CLOUD_MQTT_PASSWORD=$(quote "$mqtt_password")")
  cmd+=("VIDEO_CLOUD_MQTT_TOPIC_ROOT=$(quote "$mqtt_topic_root")")
  cmd+=("VIDEO_CLOUD_LOAD_NEGATIVE_SET=$(quote "$negative_set")")
  cmd+=("VIDEO_CLOUD_LOAD_NEGATIVE_MALFORMED_PATH=$(quote "$negative_malformed_path")")
  cmd+=("VIDEO_CLOUD_LOAD_NEGATIVE_TIMEOUT_PATH=$(quote "$negative_timeout_path")")
  cmd+=("VIDEO_CLOUD_LOAD_CLIENT_COMMIT=$(quote "$client_commit")")
  cmd+=("VIDEO_CLOUD_LOAD_SERVER_COMMIT=$(quote "$server_commit")")
  cmd+=("VIDEO_CLOUD_LOAD_BINARY_SHA256=$(quote "$binary_sha256")")
  cmd+=("VIDEO_CLOUD_LOAD_DEVICE_ONLINE_MODE=$(quote "$device_online_mode")")
  cmd+=("VIDEO_CLOUD_LOAD_DEVICE_IDS=$(quote "$device_ids")")
  cmd+=("$(quote "$remote_dir")/rtk-video-loadtest" run)
  cmd+=(--profile "$(quote "$profile")")
  cmd+=(--actors "$(quote "$actors")")
  cmd+=(--app-route-set "$(quote "$app_route_set")")
  cmd+=(--device-route-set "$(quote "$device_route_set")")
  cmd+=(--device-transport-set "$(quote "$device_transport_set")")
  cmd+=(--viewer-route-set "$(quote "$viewer_route_set")")
  cmd+=(--webrtc-media-set "$(quote "$webrtc_media_set")")
  cmd+=(--clip-set "$(quote "$clip_set")")
  cmd+=(--mqtt-set "$(quote "$mqtt_set")")
  cmd+=(--mqtt-addr "$(quote "$mqtt_addr")")
  cmd+=(--mqtt-username "$(quote "$mqtt_username")")
  cmd+=(--mqtt-password "$(quote "$mqtt_password")")
  cmd+=(--mqtt-topic-root "$(quote "$mqtt_topic_root")")
  cmd+=(--mqtt-device-profile "$(quote "$mqtt_device_profile")")
  cmd+=(--mqtt-iot-mix "$(quote "$mqtt_iot_mix")")
  if [ "$mqtt_required" = "1" ] || [ "$mqtt_required" = "true" ]; then
    cmd+=(--mqtt-required)
  fi
  cmd+=(--negative-set "$(quote "$negative_set")")
  cmd+=(--negative-malformed-path "$(quote "$negative_malformed_path")")
  cmd+=(--negative-timeout-path "$(quote "$negative_timeout_path")")
  cmd+=(--api-url "$(quote "$api_url")")
  cmd+=(--run-id "$(quote "$run_id")")
  cmd+=(--instance-id "$(quote "$instance_id")")
  cmd+=(--contracts-commit "$(quote "$contracts_commit")")
  cmd+=(--client-commit "$(quote "$client_commit")")
  cmd+=(--server-commit "$(quote "$server_commit")")
  cmd+=(--binary-sha256 "$(quote "$binary_sha256")")
  cmd+=(--duration "$(quote "$role_duration")")
  cmd+=(--virtual-devices "$(quote "$virtual_devices")")
  cmd+=(--virtual-viewers "$(quote "$virtual_viewers")")
  cmd+=(--iterations "$(quote "$iterations")")
  cmd+=(--app-rate "$(quote "$app_rate")")
  cmd+=(--device-rate "$(quote "$device_rate")")
  cmd+=(--viewer-rate "$(quote "$viewer_rate")")
  cmd+=(--device-prefix "$(quote "$device_prefix")")
  if [ -n "$device_ids" ]; then
    cmd+=(--device-ids "$(quote "$device_ids")")
  fi
  cmd+=(--ws-url "$(quote "$ws_url")")
  cmd+=(--device-online-mode "$(quote "$device_online_mode")")
  if [ -n "$app_tokens" ]; then
    cmd+=(--app-token-map-json "$(quote "$app_tokens")")
  fi
  cmd+=(--output "$(quote "$remote_artifact_dir")/load-results.json")
  cmd+=(--report-output "$(quote "$remote_artifact_dir")/load-report.md")
  printf '%s ' "${cmd[@]}"
}

copy_remote_role_artifacts() {
  local host="$1"
  local role="$2"
  local remote="${remote_user}@${host}"
  local remote_artifact_dir="${remote_dir}/runs/${run_id}/${role}"
  local local_role_dir="${artifact_dir}/${role}"
  mkdir -p "$local_role_dir"
  local file
  for file in metadata.json load-results.json load-report.md stdout.log stderr.log; do
    if ! run_scp "${remote}:$(quote "$remote_artifact_dir")/${file}" "$local_role_dir/${file}"; then
      printf 'failed to collect %s from %s:%s\n' "$file" "$host" "$remote_artifact_dir" >> "${local_role_dir}/artifact-collection.log"
    else
      case "$file" in
        stdout.log|stderr.log) redact_file "$local_role_dir/${file}" ;;
      esac
    fi
  done
}

run_remote_role() {
  local host="$1"
  local role="$2"
  local actors="$3"
  local instance_id="$4"
  local role_duration="$5"
  local remote="${remote_user}@${host}"
  local remote_artifact_dir="${remote_dir}/runs/${run_id}/${role}"
  local cmd
  cmd="$(remote_run_command "$role" "$actors" "$instance_id" "$role_duration")"
  local stdout_log="${remote_artifact_dir}/stdout.log"
  local stderr_log="${remote_artifact_dir}/stderr.log"
  write_remote_metadata "$host" "$role" "$actors" "$instance_id"
  local status=0
  if [ "$dry_run" -eq 1 ]; then
    run_ssh "$remote" "mkdir -p $(quote "$remote_artifact_dir") && { ${cmd}; } >$(quote "$stdout_log") 2>$(quote "$stderr_log")"
  else
    set +e
    ssh_cmd "$remote" "mkdir -p $(quote "$remote_artifact_dir") && { ${cmd}; } >$(quote "$stdout_log") 2>$(quote "$stderr_log")"
    status=$?
    set -e
  fi
  copy_remote_role_artifacts "$host" "$role"
  if [ "$status" -ne 0 ]; then
    write_failure_report "remote-${role}" "remote ${role} actor failed with exit status ${status}" "host=${host}"
  fi
  return "$status"
}

verify_binary_checksum
preflight_remote_host "$device_host" "device"
preflight_remote_host "$app_host" "app-viewer"

install_binary "$device_host"
install_binary "$app_host"

run_two_host_roles() {
  if [ "$dry_run" -eq 1 ]; then
    redact "+ background device role on ${device_host}"
    run_remote_role "$device_host" "device" "device" "${VIDEO_CLOUD_LOAD_DEVICE_INSTANCE_ID:-${run_id}-device}" "$device_duration"
    redact "+ wait ${device_warmup_seconds}s for device owner transport"
    run_remote_role "$app_host" "app-viewer" "app,viewer" "${VIDEO_CLOUD_LOAD_APP_INSTANCE_ID:-${run_id}-app-viewer}" "$duration"
    redact "+ wait for device role"
    return 0
  fi

  local device_status=0
  local app_status=0
  run_remote_role "$device_host" "device" "device" "${VIDEO_CLOUD_LOAD_DEVICE_INSTANCE_ID:-${run_id}-device}" "$device_duration" &
  local device_pid=$!
  sleep "$device_warmup_seconds"
  run_remote_role "$app_host" "app-viewer" "app,viewer" "${VIDEO_CLOUD_LOAD_APP_INSTANCE_ID:-${run_id}-app-viewer}" "$duration" || app_status=$?
  wait "$device_pid" || device_status=$?
  if [ "$app_status" -ne 0 ]; then
    return "$app_status"
  fi
  return "$device_status"
}

two_host_status=0
run_two_host_roles || two_host_status=$?

aggregate_cmd=(
  python3 "${load_root}/tools/aggregate_video_loadtest_two_host.py"
  --input "device=${artifact_dir}/device/load-results.json"
  --input "app-viewer=${artifact_dir}/app-viewer/load-results.json"
  --metadata "device=${artifact_dir}/device/metadata.json"
  --metadata "app-viewer=${artifact_dir}/app-viewer/metadata.json"
  --output "${artifact_dir}/two-host-load-report.md"
  --server-commit "${server_commit}"
  --contracts-commit "${contracts_commit}"
  --binary-sha256 "${binary_sha256}"
)
if ! run_cmd "${aggregate_cmd[@]}"; then
  write_failure_report "aggregate" "two-host load-test aggregate report generation failed" "artifact_dir=${artifact_dir}"
  if [ "$two_host_status" -eq 0 ]; then
    two_host_status=1
  fi
fi

echo "two-host load-test artifacts: ${artifact_dir}"
exit "$two_host_status"
