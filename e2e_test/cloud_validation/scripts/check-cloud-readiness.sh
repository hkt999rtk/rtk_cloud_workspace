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

mkdir -p "$out_dir"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/sdk-cloud-readiness.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

probe() {
  local name="$1"
  local url="$2"
  curl --fail --silent --show-error --location --max-time 10 \
    --output "$tmp_dir/${name}.json" "$url"
}

probe account "${account_url%/}${account_path}"
probe video "${video_url%/}${video_path}"
probe video_version "${video_url%/}/version"

# The device ingress is intentionally mTLS-only, and a run-scoped client
# certificate does not exist until fixture setup. Verify that the gateway is
# reachable without pretending anonymous health is supported. Fixture setup's
# mTLS request_token call is the authoritative backend readiness check.
device_status="$(curl --silent --show-error --location --max-time 10 \
  --output "$tmp_dir/device.json" --write-out '%{http_code}' \
  "${device_url%/}${device_path}")"
if [[ "$device_status" != "200" ]] && ! { [[ "$device_status" == "400" ]] && grep -q 'No required SSL certificate was sent' "$tmp_dir/device.json"; }; then
  echo "device mTLS ingress readiness failed with HTTP $device_status" >&2
  exit 1
fi

# Fixture cleanup is a hard gate: creating remote resources is unsafe when the
# deployed Video Cloud cannot revoke their entitlement. Probe a guaranteed
# nonexistent UUID, which must authenticate and reach the route before
# returning 404. A 401 means bad credentials; 405 means the required server
# contract has not been deployed yet.
video_admin_token="${CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN:?CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN is required}"
video_headers="$tmp_dir/video-admin-headers.txt"
printf 'Authorization: Bearer %s\n' "$video_admin_token" > "$video_headers"
chmod 600 "$video_headers"
cleanup_probe_status="$(curl --silent --show-error --max-time 10 --output "$tmp_dir/cleanup-probe.json" --write-out '%{http_code}' \
  -X POST --header "@$video_headers" \
  "${video_url%/}/api/devices/00000000-0000-4000-8000-000000000001/entitlement/revoke")"
if [[ "$cleanup_probe_status" != "404" ]]; then
  echo "Video Cloud cleanup contract is unavailable or unauthorized (HTTP $cleanup_probe_status; expected 404 for the nonexistent probe device)" >&2
  exit 1
fi

mqtt_first="${mqtt_addr%%,*}"
mqtt_host="${mqtt_first%:*}"
mqtt_port="${mqtt_first##*:}"
if [[ -z "$mqtt_host" || -z "$mqtt_port" || "$mqtt_host" == "$mqtt_port" ]]; then
  echo "invalid CLOUD_VALIDATION_MQTT_ADDR; expected host:port" >&2
  exit 1
fi
nc -z -w 5 "$mqtt_host" "$mqtt_port"

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
