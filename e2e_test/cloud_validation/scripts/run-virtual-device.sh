#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workspace_root="$(cd "$script_dir/../../.." && pwd)"
bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
out_dir="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}/virtual-device"
ready_file="${CLOUD_VALIDATION_READY_FILE:?CLOUD_VALIDATION_READY_FILE is required}"

brandname="$(jq -er '.brandname' "$bundle")"
environment_root="$(jq -er '.environment_root' "$bundle")"
test_data_db="$(jq -er '.test_data_db' "$bundle")"
max_devices="$(jq -er '.max_devices // 1' "$bundle")"
duration_seconds="$(jq -er '.duration_seconds // 900' "$bundle")"
ca_bundle="$(jq -er '.ca_bundle // empty' "$bundle")"
validation_profile="$(jq -r '.validation_profile // "deploy"' "$bundle")"
reconnect_signal="$out_dir/offline-reconnect.signal"
disconnect_request="$out_dir/offline-disconnect.request"
offline_ready="$out_dir/offline-ready"

if [[ "$max_devices" -lt 1 || "$max_devices" -gt 5 ]]; then
  echo "max_devices must be between 1 and 5" >&2
  exit 2
fi
mkdir -p "$out_dir"
rm -f "$reconnect_signal" "$disconnect_request" "$offline_ready"

export HOME100K_DEVICE_CLIENT_CA_BUNDLE="${ca_bundle:-${CLOUD_VALIDATION_CA_BUNDLE:-}}"
export ACCOUNT_MANAGER_BASE_URL="${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:?CLOUD_VALIDATION_ACCOUNT_MANAGER_URL is required}"
export VIDEO_CLOUD_PUBLIC_BASE_URL="${CLOUD_VALIDATION_VIDEO_CLOUD_URL:?CLOUD_VALIDATION_VIDEO_CLOUD_URL is required}"
export VIDEO_CLOUD_TOKEN_BASE_URL="${CLOUD_VALIDATION_DEVICE_URL:?CLOUD_VALIDATION_DEVICE_URL is required}"
export VIDEO_CLOUD_MQTT_ADDR="${CLOUD_VALIDATION_MQTT_ADDR:?CLOUD_VALIDATION_MQTT_ADDR is required}"

args=(
  --root "$workspace_root"
  --env-root "$environment_root"
  --test-data-db "$test_data_db"
  --brandname "$brandname"
  --out-dir "$out_dir"
  --profile smoke
  --load-model sdk-device-simulator
  --run-id "$CLOUD_VALIDATION_RUN_ID"
  --ready-file "$ready_file"
  --duration-seconds "$duration_seconds"
  --max-users 1
  --max-connected-devices "$max_devices"
  --concurrency "$max_devices"
  --mqtt-probe true
)
if [[ "$validation_profile" == "nightly" ]]; then
  args+=(--sdk-reconnect-signal-file "$reconnect_signal")
  args+=(--sdk-invalid-credential-probe)
  export CLOUD_VALIDATION_OFFLINE_RECONNECT_SIGNAL="$reconnect_signal"
fi

(cd "$workspace_root/scripts/go/cloud-mqtt-test" && GOWORK=off go run . "${args[@]}")
