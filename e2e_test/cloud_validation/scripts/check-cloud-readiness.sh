#!/usr/bin/env bash
set -euo pipefail

out_dir="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}"
account_url="${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:?CLOUD_VALIDATION_ACCOUNT_MANAGER_URL is required}"
video_url="${CLOUD_VALIDATION_VIDEO_CLOUD_URL:?CLOUD_VALIDATION_VIDEO_CLOUD_URL is required}"
device_url="${CLOUD_VALIDATION_DEVICE_URL:?CLOUD_VALIDATION_DEVICE_URL is required}"
mqtt_addr="${CLOUD_VALIDATION_MQTT_ADDR:?CLOUD_VALIDATION_MQTT_ADDR is required}"
account_path="${CLOUD_VALIDATION_ACCOUNT_HEALTH_PATH:-/v1/health}"
video_path="${CLOUD_VALIDATION_VIDEO_HEALTH_PATH:-/healthz}"
device_path="${CLOUD_VALIDATION_DEVICE_HEALTH_PATH:-/healthz}"
probe_timeout="${CLOUD_VALIDATION_PROBE_TIMEOUT_SECONDS:-10}"
probe_attempts="${CLOUD_VALIDATION_PROBE_ATTEMPTS:-3}"
probe_retry_delay="${CLOUD_VALIDATION_PROBE_RETRY_DELAY_SECONDS:-2}"

if [[ ! "$probe_timeout" =~ ^[1-9][0-9]*$ ]] || [[ ! "$probe_attempts" =~ ^[1-9][0-9]*$ ]] || [[ ! "$probe_retry_delay" =~ ^[0-9]+$ ]]; then
  echo "cloud readiness timeout/attempts must be positive integers and retry delay must be a non-negative integer" >&2
  exit 2
fi

mkdir -p "$out_dir"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/sdk-cloud-readiness.XXXXXX")"
results_file="$out_dir/readiness-results.json"
jq -n '{schema_version: 1, status: "RUNNING", probes: []}' > "$results_file"
chmod 600 "$results_file"

finalize() {
  local rc=$?
  local status="PASS"
  [[ "$rc" -eq 0 ]] || status="FAIL"
  if [[ -f "$results_file" ]]; then
    jq --arg status "$status" '.status = $status' "$results_file" > "$tmp_dir/readiness-results.final.json" || true
    if [[ -s "$tmp_dir/readiness-results.final.json" ]]; then
      mv "$tmp_dir/readiness-results.final.json" "$results_file"
      chmod 600 "$results_file"
    fi
  fi
  rm -rf "$tmp_dir"
  return "$rc"
}
trap finalize EXIT

record_probe() {
  local name="$1"
  local status="$2"
  local assessment="$3"
  local attempts="$4"
  local http_status="$5"
  local curl_code="$6"
  local total_seconds="$7"
  jq \
    --arg name "$name" \
    --arg status "$status" \
    --arg assessment "$assessment" \
    --arg http_status "$http_status" \
    --argjson attempts "$attempts" \
    --argjson curl_code "$curl_code" \
    --argjson total_seconds "$total_seconds" \
    '.probes += [{
      name: $name,
      status: $status,
      assessment: $assessment,
      attempts: $attempts,
      http_status: (if $http_status == "" then null else $http_status end),
      curl_code: $curl_code,
      total_seconds: $total_seconds
    }]' "$results_file" > "$tmp_dir/readiness-results.next.json"
  mv "$tmp_dir/readiness-results.next.json" "$results_file"
  chmod 600 "$results_file"
}

probe_http_status=""
probe_curl_code=0
probe_attempt_count=0
probe_total_seconds=0

transport_request() {
  local name="$1"
  shift
  local metrics=""
  local rc=0
  local attempt
  for ((attempt = 1; attempt <= probe_attempts; attempt++)); do
    probe_attempt_count="$attempt"
    set +e
    metrics="$(curl --silent --show-error --location --max-time "$probe_timeout" \
      --output "$tmp_dir/${name}.json" --write-out $'%{http_code}\t%{time_total}' \
      "$@" 2>"$tmp_dir/${name}.stderr")"
    rc=$?
    set -e
    probe_curl_code="$rc"
    IFS=$'\t' read -r probe_http_status probe_total_seconds <<< "$metrics"
    probe_http_status="${probe_http_status:-000}"
    probe_total_seconds="${probe_total_seconds:-0}"
    if [[ "$rc" -eq 0 ]]; then
      return 0
    fi
    case "$rc" in
      6|7|28|35|52|56)
        if (( attempt < probe_attempts )); then
          sleep "$probe_retry_delay"
          continue
        fi
        ;;
    esac
    record_probe "$name" "FAIL" "transport_error" "$attempt" "$probe_http_status" "$rc" "$probe_total_seconds"
    echo "$name readiness transport failed (curl=$rc attempts=$attempt)" >&2
    return "$rc"
  done
  record_probe "$name" "FAIL" "transport_error" "$probe_attempt_count" "$probe_http_status" "$probe_curl_code" "$probe_total_seconds"
  echo "$name readiness transport failed (curl=$probe_curl_code attempts=$probe_attempt_count)" >&2
  return "$probe_curl_code"
}

probe() {
  local name="$1"
  local url="$2"
  transport_request "$name" "$url"
  case "$probe_http_status" in
    2??) record_probe "$name" "PASS" "http_ready" "$probe_attempt_count" "$probe_http_status" 0 "$probe_total_seconds" ;;
    *)
      record_probe "$name" "FAIL" "unexpected_http_status" "$probe_attempt_count" "$probe_http_status" 0 "$probe_total_seconds"
      echo "$name readiness failed with HTTP $probe_http_status" >&2
      return 1
      ;;
  esac
}

probe account "${account_url%/}${account_path}"
probe video "${video_url%/}${video_path}"
probe video_version "${video_url%/}/version"

# The device ingress is intentionally mTLS-only, and a run-scoped client
# certificate does not exist until fixture setup. Verify that the gateway is
# reachable without pretending anonymous health is supported. Fixture setup's
# mTLS request_token call is the authoritative backend readiness check.
transport_request device "${device_url%/}${device_path}"
device_status="$probe_http_status"
if [[ "$device_status" != "200" ]] && ! { [[ "$device_status" == "400" ]] && grep -q 'No required SSL certificate was sent' "$tmp_dir/device.json"; }; then
  record_probe device "FAIL" "unexpected_http_status" "$probe_attempt_count" "$device_status" 0 "$probe_total_seconds"
  echo "device mTLS ingress readiness failed with HTTP $device_status" >&2
  exit 1
fi
record_probe device "PASS" "mtls_ingress_ready" "$probe_attempt_count" "$device_status" 0 "$probe_total_seconds"

# Fixture cleanup is a hard gate: creating remote resources is unsafe when the
# deployed Video Cloud cannot revoke their entitlement. Probe a guaranteed
# nonexistent UUID, which must authenticate and reach the route before
# returning 404. A 401 means bad credentials; 405 means the required server
# contract has not been deployed yet.
video_admin_token="${CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN:?CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN is required}"
video_headers="$tmp_dir/video-admin-headers.txt"
printf 'Authorization: Bearer %s\n' "$video_admin_token" > "$video_headers"
chmod 600 "$video_headers"
transport_request cleanup_contract -X POST --header "@$video_headers" \
  "${video_url%/}/api/devices/00000000-0000-4000-8000-000000000001/entitlement/revoke"
cleanup_probe_status="$probe_http_status"
if [[ "$cleanup_probe_status" != "404" ]]; then
  record_probe cleanup_contract "FAIL" "unexpected_http_status" "$probe_attempt_count" "$cleanup_probe_status" 0 "$probe_total_seconds"
  echo "Video Cloud cleanup contract is unavailable or unauthorized (HTTP $cleanup_probe_status; expected 404 for the nonexistent probe device)" >&2
  exit 1
fi
record_probe cleanup_contract "PASS" "authenticated_route_ready" "$probe_attempt_count" "$cleanup_probe_status" 0 "$probe_total_seconds"

mqtt_first="${mqtt_addr%%,*}"
mqtt_host="${mqtt_first%:*}"
mqtt_port="${mqtt_first##*:}"
if [[ -z "$mqtt_host" || -z "$mqtt_port" || "$mqtt_host" == "$mqtt_port" ]]; then
  echo "invalid CLOUD_VALIDATION_MQTT_ADDR; expected host:port" >&2
  exit 1
fi
mqtt_ready=0
for ((attempt = 1; attempt <= probe_attempts; attempt++)); do
  if nc -z -w 5 "$mqtt_host" "$mqtt_port" >/dev/null 2>&1; then
    mqtt_ready=1
    record_probe mqtt "PASS" "tcp_ready" "$attempt" "" 0 0
    break
  fi
  if (( attempt < probe_attempts )); then
    sleep "$probe_retry_delay"
  fi
done
if [[ "$mqtt_ready" -ne 1 ]]; then
  record_probe mqtt "FAIL" "transport_error" "$probe_attempts" "" 1 0
  echo "MQTT readiness transport failed after $probe_attempts attempts" >&2
  exit 1
fi

# Do not copy full health/version payloads into artifacts; retain only public
# build fields. Account Manager health may omit a build identifier, while
# Video Cloud exposes its API and application versions from /version.
account_version="$(jq -r '.version // .git_commit // .commit // .build // "unknown"' "$tmp_dir/account.json")"
video_api_version="$(jq -r '.ApiVersion // .api_version // .version // "unknown"' "$tmp_dir/video_version.json")"
video_app_version="$(jq -r '.AppVersion // .app_version // .build // "unknown"' "$tmp_dir/video_version.json")"
jq -n \
  --arg version "account-manager=${account_version};video-cloud=${video_api_version}/${video_app_version}" \
  --arg service "$(jq -r '.service // "account-manager"' "$tmp_dir/account.json")" \
  --arg account_version "$account_version" \
  --arg video_api_version "$video_api_version" \
  --arg video_app_version "$video_app_version" \
  '{
    schema_version: 1,
    version: $version,
    service: $service,
    services: {
      account_manager: {version: $account_version},
      video_cloud: {api_version: $video_api_version, app_version: $video_app_version}
    }
  }' \
  > "$out_dir/server-version.json"
chmod 600 "$out_dir/server-version.json"
