#!/usr/bin/env bash
set -euo pipefail

bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
out_dir="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}"
run_id="${CLOUD_VALIDATION_RUN_ID:?CLOUD_VALIDATION_RUN_ID is required}"
platform="${CLOUD_VALIDATION_PLATFORM:?CLOUD_VALIDATION_PLATFORM is required}"
device_id="$(jq -er '.app.device_id' "$bundle")"
base_url="$(jq -er '.app.base_url' "$bundle")"
app_cert="$(jq -er '.app.certificate_path' "$bundle")"
app_key="$(jq -er '.app.private_key_path' "$bundle")"
secret_root="$(jq -er 'first(.resources[] | select(.kind == "local_fixture_root") | .path)' "$bundle")"
header_file="$secret_root/app-headers.txt"
signal_file="$out_dir/virtual-device/offline-reconnect.signal"
disconnect_request="$out_dir/virtual-device/offline-disconnect.request"
offline_ready="$out_dir/virtual-device/offline-ready"
evidence_file="$out_dir/offline-reconnect-evidence.json"
response="$secret_root/offline-shadow-response.json"
headers="$secret_root/offline-shadow-headers.txt"

test -f "$header_file"
mkdir -p "$(dirname "$signal_file")"
offline_timeout_seconds="${CLOUD_VALIDATION_OFFLINE_TIMEOUT_SECONDS:-120}"
start_timeout_seconds="${CLOUD_VALIDATION_OFFLINE_START_TIMEOUT_SECONDS:-600}"
# Android can finish the online round-trip and begin the offline scenario in
# less than one second. Poll frequently enough to observe that convergence
# window before asking the virtual device to disconnect.
poll_interval_seconds="${CLOUD_VALIDATION_OFFLINE_POLL_INTERVAL_SECONDS:-0.1}"
deadline=$((SECONDS + start_timeout_seconds))
queued_at=""

read_shadow() {
  : > "$headers"
  local curl_mtls=(--fail --silent --show-error --max-time 5 --cert "$app_cert" --key "$app_key")
  if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
    curl_mtls+=(--cacert "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
  fi
  curl "${curl_mtls[@]}" \
    --header "@$header_file" --dump-header "$headers" \
    "${base_url%/}/things/$device_id/shadow" > "$response"
}

while (( SECONDS < deadline )); do
  if read_shadow; then
    if jq -e --arg run_id "$run_id" '
      .state.desired.cloud_validation_run == $run_id and
      .state.reported.cloud_validation_run == $run_id and
      .state.desired.enabled == true and
      .state.reported.enabled == true and
      ((.state.delta == null) or (.state.delta == {}))
    ' "$response" >/dev/null || jq -e --arg run_id "$run_id" '
      .state.desired.cloud_validation_run == $run_id and
      .state.desired.cloud_validation_scenario == "shadow_offline_reconnect" and
      .state.delta.cloud_validation_run == $run_id and
      .state.delta.cloud_validation_scenario == "shadow_offline_reconnect" and
      (.state.reported.cloud_validation_scenario // "") != "shadow_offline_reconnect"
    ' "$response" >/dev/null; then
      : > "$disconnect_request"
      chmod 600 "$disconnect_request"
      break
    fi
  fi
  sleep "$poll_interval_seconds"
done

if [[ ! -f "$disconnect_request" ]]; then
  echo "offline controller did not observe online shadow convergence" >&2
  exit 1
fi

deadline=$((SECONDS + offline_timeout_seconds))
while (( SECONDS < deadline )); do
  if [[ -f "$offline_ready" ]] && jq -e --arg run_id "$run_id" '
    .schema_version == 1 and .run_id == $run_id and .status == "OFFLINE"
  ' "$offline_ready" >/dev/null; then
    break
  fi
  sleep "$poll_interval_seconds"
done

if [[ ! -f "$offline_ready" ]]; then
  echo "virtual device did not confirm the offline phase" >&2
  exit 1
fi

deadline=$((SECONDS + offline_timeout_seconds))
while (( SECONDS < deadline )); do
  if read_shadow && jq -e --arg run_id "$run_id" '
    .state.desired.cloud_validation_run == $run_id and
    .state.desired.cloud_validation_scenario == "shadow_offline_reconnect" and
    .state.delta.cloud_validation_run == $run_id and
    .state.delta.cloud_validation_scenario == "shadow_offline_reconnect"
  ' "$response" >/dev/null; then
    queued_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    : > "$signal_file"
    chmod 600 "$signal_file"
    break
  fi
  sleep "$poll_interval_seconds"
done

if [[ -z "$queued_at" ]]; then
  echo "offline controller did not observe the queued desired state" >&2
  exit 1
fi

deadline=$((SECONDS + offline_timeout_seconds))
while (( SECONDS < deadline )); do
  if read_shadow && jq -e --arg run_id "$run_id" '
    .state.desired.cloud_validation_run == $run_id and
    .state.reported.cloud_validation_run == $run_id and
    .state.reported.cloud_validation_scenario == "shadow_offline_reconnect" and
    ((.state.delta == null) or (.state.delta == {}))
  ' "$response" >/dev/null; then
    observed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    request_id="$(awk 'BEGIN{IGNORECASE=1} /^x-request-id:/ {gsub("\r", "", $2); print $2; exit}' "$headers")"
    jq -n --arg run_id "$run_id" --arg platform "$platform" \
      --arg queued_at "$queued_at" --arg observed_at "$observed_at" \
      --arg source_id "${request_id:-shadow-http-observation}" '{
      schema_version:1,
      run_id:$run_id,
      platform:$platform,
      events:[
        {scenario_id:"shadow_offline_reconnect", correlation_id:($run_id + "-" + $platform + "-shadow_offline_reconnect"), type:"desired_queued", observed_at:$queued_at, evidence:{source_event_ids:[$source_id]}},
        {scenario_id:"shadow_offline_reconnect", correlation_id:($run_id + "-" + $platform + "-shadow_offline_reconnect"), type:"device_reconnected", observed_at:$observed_at, evidence:{source_event_ids:[$source_id]}},
        {scenario_id:"shadow_offline_reconnect", correlation_id:($run_id + "-" + $platform + "-shadow_offline_reconnect"), type:"reported_written", observed_at:$observed_at, evidence:{source_event_ids:[$source_id]}},
        {scenario_id:"shadow_offline_reconnect", correlation_id:($run_id + "-" + $platform + "-shadow_offline_reconnect"), type:"delta_cleared", observed_at:$observed_at, evidence:{source_event_ids:[$source_id]}}
      ]
    }' > "$evidence_file"
    chmod 600 "$evidence_file"
    exit 0
  fi
  sleep "$poll_interval_seconds"
done

echo "offline controller did not observe post-reconnect convergence" >&2
exit 1
