#!/usr/bin/env bash
set -euo pipefail

workspace_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
load_root="${workspace_root}/e2e_test/video_cloud/load"
e2e_root="${workspace_root}/e2e_test"
run_id="${VIDEO_CLOUD_LOAD_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
artifact_dir="${VIDEO_CLOUD_LOAD_ARTIFACT_DIR:-${workspace_root}/.artifacts/e2e_test/video_cloud/load/${run_id}}"
mkdir -p "${artifact_dir}"

profile="${VIDEO_CLOUD_LOAD_PROFILE:-safe-staging}"
actors="${VIDEO_CLOUD_LOAD_ACTORS:-all}"
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
api_url="${VIDEO_CLOUD_LOAD_API_URL:-}"
contracts_commit="${VIDEO_CLOUD_LOAD_CONTRACTS_COMMIT:-$(git -C "${workspace_root}/repos/rtk_cloud_contracts_doc" rev-parse HEAD 2>/dev/null || true)}"
client_commit="${VIDEO_CLOUD_LOAD_CLIENT_COMMIT:-$(git -C "${workspace_root}" rev-parse HEAD 2>/dev/null || true)}"
server_commit="${VIDEO_CLOUD_LOAD_SERVER_COMMIT:-unknown}"
account_token="${VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN:-}"
admin_token="${VIDEO_CLOUD_LOAD_ADMIN_TOKEN:-}"
device_token="${VIDEO_CLOUD_LOAD_DEVICE_TOKEN:-}"
device_tokens="${VIDEO_CLOUD_LOAD_DEVICE_TOKENS:-}"
app_tokens="${VIDEO_CLOUD_LOAD_APP_TOKENS:-}"
refresh_token="${VIDEO_CLOUD_LOAD_REFRESH_TOKEN:-}"

cat >"${artifact_dir}/metadata.json" <<EOF
{
  "run_id": "${run_id}",
  "instance_id": "${VIDEO_CLOUD_LOAD_INSTANCE_ID:-local}",
  "profile": "${profile}",
  "actors": "${actors}",
  "app_route_set": "${app_route_set}",
  "device_route_set": "${device_route_set}",
  "device_transport_set": "${device_transport_set}",
  "viewer_route_set": "${viewer_route_set}",
  "webrtc_media_set": "${webrtc_media_set}",
  "clip_set": "${clip_set}",
  "mqtt_set": "${mqtt_set}",
  "mqtt_addr": "${mqtt_addr}",
  "mqtt_device_profile": "${mqtt_device_profile}",
  "mqtt_iot_mix": "${mqtt_iot_mix}",
  "mqtt_required": "${mqtt_required}",
  "negative_set": "${negative_set}",
  "api_url": "${api_url}",
  "client_commit": "${client_commit}",
  "server_commit": "${server_commit}",
  "contracts_commit": "${contracts_commit}",
  "started_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

extra_args=()
if [ -n "${account_token}" ]; then
  extra_args+=(--account-token "${account_token}")
fi
if [ -n "${admin_token}" ]; then
  extra_args+=(--admin-token "${admin_token}")
fi
if [ -n "${device_token}" ]; then
  extra_args+=(--device-token "${device_token}")
fi
if [ -n "${device_tokens}" ]; then
  extra_args+=(--device-token-map-json "${device_tokens}")
fi
if [ -n "${app_tokens}" ]; then
  extra_args+=(--app-token-map-json "${app_tokens}")
fi
if [ -n "${refresh_token}" ]; then
  extra_args+=(--refresh-token "${refresh_token}")
fi
if [ "${mqtt_required}" = "1" ] || [ "${mqtt_required}" = "true" ]; then
  extra_args+=(--mqtt-required)
fi

(
  cd "${e2e_root}"
  go run ./video_cloud/load/cmd/rtk-video-loadtest run \
    --profile "${profile}" \
    --actors "${actors}" \
    --app-route-set "${app_route_set}" \
    --device-route-set "${device_route_set}" \
    --device-transport-set "${device_transport_set}" \
    --viewer-route-set "${viewer_route_set}" \
    --webrtc-media-set "${webrtc_media_set}" \
    --clip-set "${clip_set}" \
    --mqtt-set "${mqtt_set}" \
    --mqtt-addr "${mqtt_addr}" \
    --mqtt-username "${mqtt_username}" \
    --mqtt-password "${mqtt_password}" \
    --mqtt-topic-root "${mqtt_topic_root}" \
    --mqtt-device-profile "${mqtt_device_profile}" \
    --mqtt-iot-mix "${mqtt_iot_mix}" \
    --negative-set "${negative_set}" \
    --negative-malformed-path "${negative_malformed_path}" \
    --negative-timeout-path "${negative_timeout_path}" \
    --api-url "${api_url}" \
    --run-id "${run_id}" \
    --instance-id "${VIDEO_CLOUD_LOAD_INSTANCE_ID:-local}" \
    --contracts-commit "${contracts_commit}" \
    --client-commit "${client_commit}" \
    --server-commit "${server_commit}" \
    --duration "${duration}" \
    --virtual-devices "${virtual_devices}" \
    --virtual-viewers "${virtual_viewers}" \
    --iterations "${iterations}" \
    --app-rate "${app_rate}" \
    --device-rate "${device_rate}" \
    --viewer-rate "${viewer_rate}" \
    --output "${artifact_dir}/load-results.json" \
    --report-output "${artifact_dir}/load-report.md" \
    "${extra_args[@]}" \
    "$@"
)

echo "load test artifacts: ${artifact_dir}"
