#!/usr/bin/env bash
set -euo pipefail

bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
out_dir="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}"
run_id="${CLOUD_VALIDATION_RUN_ID:?CLOUD_VALIDATION_RUN_ID is required}"
platform="${CLOUD_VALIDATION_PLATFORM:?CLOUD_VALIDATION_PLATFORM is required}"
video_url="${CLOUD_VALIDATION_VIDEO_CLOUD_URL:?CLOUD_VALIDATION_VIDEO_CLOUD_URL is required}"
device_id="$(jq -er '.app.device_id' "$bundle")"
secret_root="$(jq -er 'first(.resources[] | select(.kind == "local_fixture_root") | .path)' "$bundle")"
header_file="$secret_root/video-headers.txt"
evidence_file="$out_dir/cloud-command-trigger-evidence.json"
request_body="$secret_root/cloud-command-trigger.json"
response_headers="$secret_root/cloud-command-trigger-headers.txt"

if [[ ! -f "$header_file" ]]; then
  echo "Cloud command trigger cannot authenticate" >&2
  exit 2
fi

jq -n --arg devid "$device_id" --arg run_id "$run_id" '{
  devid:$devid,
  event:"sdk_cloud_validation",
  data:{run_id:$run_id, action:"websocket_receive_roundtrip"}
}' > "$request_body"
chmod 600 "$request_body"
mkdir -p "$out_dir"

deadline=$((SECONDS + ${CLOUD_VALIDATION_COMMAND_TRIGGER_TIMEOUT_SECONDS:-60}))
successes=0
while (( SECONDS < deadline )); do
  : > "$response_headers"
  raw_status="$(curl --silent --show-error --max-time 5 -X POST \
    --header "@$header_file" -H 'Content-Type: application/json' \
    --data-binary "@$request_body" --dump-header "$response_headers" \
    --output /dev/null --write-out '%{http_code}' \
    "${video_url%/}/api/devices/$device_id/commands" || printf '000')"
  status="${raw_status: -3}"
  if [[ "$status" == "200" ]]; then
    successes=$((successes + 1))
    request_id="$(awk 'BEGIN{IGNORECASE=1} /^x-request-id:/ {gsub("\r", "", $2); print $2; exit}' "$response_headers")"
    observed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    tmp_evidence="$evidence_file.tmp"
    jq -n \
      --arg run_id "$run_id" --arg platform "$platform" \
      --arg observed_at "$observed_at" --arg request_id "${request_id:-cloud-command-http-200}" \
      --argjson attempts "$successes" '{
        schema_version:1,
        run_id:$run_id,
        platform:$platform,
        events:[{
          scenario_id:"websocket_receive_roundtrip",
          correlation_id:($run_id + "-" + $platform + "-websocket_receive_roundtrip"),
          type:"command_dispatched",
          observed_at:$observed_at,
          evidence:{http_status:200, source_event_ids:[$request_id], successful_dispatches:$attempts}
        }]
      }' > "$tmp_evidence"
    chmod 600 "$tmp_evidence"
    mv "$tmp_evidence" "$evidence_file"
  fi
  sleep 1
done

if (( successes == 0 )); then
  echo "Cloud command trigger did not observe an active App WebSocket session" >&2
  exit 1
fi
