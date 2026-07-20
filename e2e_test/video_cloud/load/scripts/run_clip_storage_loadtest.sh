#!/usr/bin/env bash
set -euo pipefail

workspace_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
e2e_root="${workspace_root}/e2e_test"
video_cloud_root="${workspace_root}/repos/rtk_video_cloud"
run_id="${VIDEO_CLOUD_LOAD_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
artifact_dir="${VIDEO_CLOUD_LOAD_ARTIFACT_DIR:-${workspace_root}/.artifacts/e2e_test/video_cloud/load/${run_id}/clip-storage}"
mkdir -p "$artifact_dir"

clip_ids_file="${VIDEO_CLOUD_LOAD_CLIP_DEVICE_IDS_FILE:?VIDEO_CLOUD_LOAD_CLIP_DEVICE_IDS_FILE is required}"
fixture="${VIDEO_CLOUD_LOAD_CLIP_FIXTURE:-${workspace_root}/e2e_test/video_cloud/load/testdata/clip_1080p_h264_3mbps_15s.mp4}"
thumbnail="${VIDEO_CLOUD_LOAD_CLIP_THUMBNAIL:-${workspace_root}/e2e_test/video_cloud/load/testdata/thumbnail_1080p.jpg}"
api_url="${VIDEO_CLOUD_LOAD_API_URL:?VIDEO_CLOUD_LOAD_API_URL is required}"
token_map="${VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE:?VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE is required}"
mixed_non_clip="${VIDEO_CLOUD_LOAD_CLIP_MIXED_NONCLIP:-false}"
device_ids_file="${VIDEO_CLOUD_LOAD_DEVICE_IDS_FILE:-}"
if [[ "$mixed_non_clip" != "true" && "$mixed_non_clip" != "false" ]]; then
  echo "VIDEO_CLOUD_LOAD_CLIP_MIXED_NONCLIP must be true or false" >&2
  exit 2
fi
storage_exec="${VIDEO_CLOUD_LOAD_STORAGE_EXEC:-local}"
storage_namespace="${VIDEO_CLOUD_LOAD_STORAGE_NAMESPACE:-video-cloud-staging-video-cloud}"
storage_deployment="${VIDEO_CLOUD_LOAD_STORAGE_DEPLOYMENT:-video-cloud-api}"
storage_target="deployment/$storage_deployment"
if [[ -n "${VIDEO_CLOUD_LOAD_STORAGE_POD:-}" ]]; then
  storage_target="pod/$VIDEO_CLOUD_LOAD_STORAGE_POD"
fi
storage_preflight_binary="${VIDEO_CLOUD_LOAD_STORAGE_PREFLIGHT_BINARY:-/app/clipuploadpreflight}"
storage_reconcile_binary="${VIDEO_CLOUD_LOAD_STORAGE_RECONCILE_BINARY:-/app/clipreconcile}"
storage_probe_binary="${VIDEO_CLOUD_LOAD_STORAGE_PROBE_BINARY:-}"
runner_exec="${VIDEO_CLOUD_LOAD_RUNNER_EXEC:-local}"
runner_namespace="${VIDEO_CLOUD_LOAD_RUNNER_NAMESPACE:-clip-upload-loadtest}"
runner_pod="${VIDEO_CLOUD_LOAD_RUNNER_POD:-clip-load-runner}"
runner_binary="${VIDEO_CLOUD_LOAD_RUNNER_BINARY:-/tmp/rtk-video-loadtest}"
storage_env=()
for mapping in \
  "VIDEO_CLOUD_LOAD_BLOB_ENDPOINT:VIDEO_CLOUD_BLOB_ENDPOINT" \
  "VIDEO_CLOUD_LOAD_BLOB_REGION:VIDEO_CLOUD_BLOB_REGION" \
  "VIDEO_CLOUD_LOAD_BLOB_BUCKET:VIDEO_CLOUD_BLOB_BUCKET" \
  "VIDEO_CLOUD_LOAD_BLOB_PREFIX:VIDEO_CLOUD_BLOB_PREFIX" \
  "VIDEO_CLOUD_LOAD_BLOB_FORCE_PATH_STYLE:VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE" \
  "VIDEO_CLOUD_LOAD_BLOB_ACCESS_KEY:AWS_ACCESS_KEY_ID" \
  "VIDEO_CLOUD_LOAD_BLOB_SECRET_KEY:AWS_SECRET_ACCESS_KEY"; do
  source_name="${mapping%%:*}"
  target_name="${mapping#*:}"
  if [[ -n "${!source_name:-}" ]]; then
    storage_env+=("$target_name=${!source_name}")
  fi
done

[[ -r "$clip_ids_file" ]] || { echo "clip camera inventory is not readable: $clip_ids_file" >&2; exit 2; }
[[ -r "$fixture" ]] || { echo "clip fixture is not readable: $fixture" >&2; exit 2; }
[[ -r "$thumbnail" ]] || { echo "clip thumbnail is not readable: $thumbnail" >&2; exit 2; }
[[ -r "$token_map" ]] || { echo "clip token map is not readable: $token_map" >&2; exit 2; }
if [[ "$mixed_non_clip" == "true" ]]; then
  [[ -r "$device_ids_file" ]] || { echo "mixed clip run requires readable VIDEO_CLOUD_LOAD_DEVICE_IDS_FILE" >&2; exit 2; }
  [[ -n "${VIDEO_CLOUD_LOAD_ADMIN_TOKEN:-}" ]] || { echo "mixed clip run requires VIDEO_CLOUD_LOAD_ADMIN_TOKEN" >&2; exit 2; }
fi
if [[ -n "${VIDEO_CLOUD_LOAD_ADMIN_TOKEN:-}" ]]; then
  [[ -r "${VIDEO_CLOUD_LOAD_CLIP_USER_PRIVATE_KEY:-}" ]] || { echo "clip user private key is required for sampled decryption" >&2; exit 2; }
  [[ -r "${VIDEO_CLOUD_LOAD_CLIP_SERVER_PUBLIC_KEY:-}" ]] || { echo "clip server public key is required for sampled decryption" >&2; exit 2; }
fi

run_storage_preflight() {
  if [[ "$storage_exec" == "kubernetes" ]]; then
    kubectl -n "$storage_namespace" exec "$storage_target" -- env "${storage_env[@]}" "$storage_preflight_binary"
    return
  fi
  (cd "$video_cloud_root" && GOWORK=off go run ./cmd/clipuploadpreflight)
}

run_storage_reconcile() {
  if [[ "$storage_exec" == "kubernetes" ]]; then
    kubectl -n "$storage_namespace" exec -i "$storage_target" -- \
      env "${storage_env[@]}" CLIP_RUN_ID="$run_id" CLIP_RECONCILE_WAIT="${VIDEO_CLOUD_LOAD_RECONCILE_WAIT:-5m}" CLIP_RECONCILE_BINARY="$storage_reconcile_binary" \
      sh -c 'result=$(mktemp /tmp/clip-load-results.XXXXXX); trap '\''rm -f "$result"'\'' EXIT; cat >"$result"; "$CLIP_RECONCILE_BINARY" --run-id "$CLIP_RUN_ID" --load-results "$result" --wait-backlog "$CLIP_RECONCILE_WAIT"' \
      <"$artifact_dir/load-results.json"
    return
  fi
  (cd "$video_cloud_root" && GOWORK=off go run ./cmd/clipreconcile \
    --run-id "$run_id" \
    --load-results "$artifact_dir/load-results.json" \
    --wait-backlog "${VIDEO_CLOUD_LOAD_RECONCILE_WAIT:-5m}")
}

count="$(awk 'NF {n++} END {print n+0}' "$clip_ids_file")"
expected=$((count * ${VIDEO_CLOUD_LOAD_CLIP_COUNT_PER_DEVICE:-10}))
mixed_device_count=0
if [[ "$mixed_non_clip" == "true" ]]; then
  mixed_device_count="$(awk 'NF {n++} END {print n+0}' "$device_ids_file")"
fi
cat >"$artifact_dir/clip-storage-input.json" <<EOF
{"schema":"clip-storage-10k-v2","architecture":"direct-presigned-put","run_id":"$run_id","camera_devices":$count,"expected_clips":$expected,"schedule_window":"${VIDEO_CLOUD_LOAD_CLIP_SCHEDULE_WINDOW:-30m}","poisson_seed":${VIDEO_CLOUD_LOAD_CLIP_POISSON_SEED:-20260719},"mixed_non_clip":$mixed_non_clip,"mixed_device_count":$mixed_device_count,"fixture":"$fixture","thumbnail":"$thumbnail"}
EOF

if [[ "${VIDEO_CLOUD_LOAD_SKIP_STORAGE_PREFLIGHT:-false}" != "true" ]]; then
  run_storage_preflight >"$artifact_dir/s3-checksum-preflight.log" 2>&1 || {
    echo "S3 checksum preflight failed; see $artifact_dir/s3-checksum-preflight.log" >&2
    exit 2
  }
fi

monitor_pid=""
top_monitor_pid=""
stop_resource_monitor() {
  if [[ -n "$monitor_pid" ]]; then
    kill "$monitor_pid" 2>/dev/null || true
    wait "$monitor_pid" 2>/dev/null || true
    monitor_pid=""
  fi
  if [[ -n "$top_monitor_pid" ]]; then
    kill "$top_monitor_pid" 2>/dev/null || true
    wait "$top_monitor_pid" 2>/dev/null || true
    top_monitor_pid=""
  fi
}
trap stop_resource_monitor EXIT
if [[ "$storage_exec" == "kubernetes" && -n "$storage_probe_binary" ]]; then
  (
    while true; do
      kubectl -n "$storage_namespace" exec "$storage_target" -- "$storage_probe_binary" || true
      sleep "${VIDEO_CLOUD_LOAD_RESOURCE_SAMPLE_INTERVAL:-15}"
    done
  ) >>"$artifact_dir/resource-samples.jsonl" 2>>"$artifact_dir/resource-samples.log" &
  monitor_pid=$!
  (
    while true; do
      date -u +%FT%TZ
      if [[ "$storage_target" == pod/* ]]; then
        kubectl -n "$storage_namespace" top pod "${storage_target#pod/}" 2>&1 || true
      else
        kubectl -n "$storage_namespace" top "$storage_target" 2>&1 || true
      fi
      if [[ -n "${VIDEO_CLOUD_LOAD_OBJECT_STORAGE_POD:-}" ]]; then
        kubectl -n "${VIDEO_CLOUD_LOAD_OBJECT_STORAGE_NAMESPACE:-clip-upload-loadtest}" top pod "${VIDEO_CLOUD_LOAD_OBJECT_STORAGE_POD}" 2>&1 || true
      fi
      sleep "${VIDEO_CLOUD_LOAD_RESOURCE_SAMPLE_INTERVAL:-15}"
    done
  ) >>"$artifact_dir/kubernetes-resource-samples.log" 2>&1 &
  top_monitor_pid=$!
fi

load_rc=0
runner_clip_ids="$clip_ids_file"
runner_fixture="$fixture"
runner_thumbnail="$thumbnail"
runner_token_map="$token_map"
runner_device_ids="$device_ids_file"
runner_user_private_key="${VIDEO_CLOUD_LOAD_CLIP_USER_PRIVATE_KEY:-}"
runner_server_public_key="${VIDEO_CLOUD_LOAD_CLIP_SERVER_PUBLIC_KEY:-}"
runner_output="$artifact_dir/load-results.json"
runner_report="$artifact_dir/load-report.md"
if [[ "$runner_exec" == "kubernetes" ]]; then
  runner_clip_ids="${VIDEO_CLOUD_LOAD_RUNNER_CLIP_DEVICE_IDS_FILE:-/tmp/clip-camera-ids.txt}"
  runner_fixture="${VIDEO_CLOUD_LOAD_RUNNER_CLIP_FIXTURE:-/tmp/clip-fixture.mp4}"
  runner_thumbnail="${VIDEO_CLOUD_LOAD_RUNNER_CLIP_THUMBNAIL:-/tmp/clip-thumbnail.jpg}"
  runner_token_map="${VIDEO_CLOUD_LOAD_RUNNER_DEVICE_TOKEN_MAP_FILE:-/tmp/device-token-map.json}"
  runner_device_ids="${VIDEO_CLOUD_LOAD_RUNNER_DEVICE_IDS_FILE:-/tmp/all-device-ids.txt}"
  runner_user_private_key="${VIDEO_CLOUD_LOAD_RUNNER_USER_PRIVATE_KEY:-/tmp/clip-user-private-key.pem}"
  runner_server_public_key="${VIDEO_CLOUD_LOAD_RUNNER_SERVER_PUBLIC_KEY:-/tmp/clip-server-public-key.pem}"
  runner_output="/tmp/clip-load-results.json"
  runner_report="/tmp/clip-load-report.md"
fi
runner_actors="device"
runner_app_route_set="smoke"
runner_device_route_set="off"
runner_duration="${VIDEO_CLOUD_LOAD_CLIP_CONTROL_DURATION:-1ns}"
runner_ramp_up="${VIDEO_CLOUD_LOAD_CLIP_CONTROL_RAMP_UP:-1ns}"
runner_virtual_devices=1
runner_app_concurrency=1
runner_device_concurrency=1
mixed_args=()
if [[ "$mixed_non_clip" == "true" ]]; then
  # The current HTTP surface has no /camera_event route; the established app
  # actor is the non-clip control traffic for all devices. Device transport
  # (websocket/MQTT snapshot) has separate contract coverage.
  runner_actors="${VIDEO_CLOUD_LOAD_MIXED_ACTORS:-app}"
  runner_device_route_set="${VIDEO_CLOUD_LOAD_MIXED_DEVICE_ROUTE_SET:-off}"
  runner_app_route_set="${VIDEO_CLOUD_LOAD_MIXED_APP_ROUTE_SET:-smoke}"
  runner_duration="${VIDEO_CLOUD_LOAD_MIXED_DURATION:-${VIDEO_CLOUD_LOAD_CLIP_SCHEDULE_WINDOW:-30m}}"
  runner_ramp_up="${VIDEO_CLOUD_LOAD_MIXED_RAMP_UP:-1m}"
  runner_virtual_devices="$mixed_device_count"
  runner_app_concurrency="${VIDEO_CLOUD_LOAD_MIXED_APP_CONCURRENCY:-64}"
  runner_device_concurrency="${VIDEO_CLOUD_LOAD_MIXED_DEVICE_CONCURRENCY:-64}"
  mixed_args+=(--device-ids-file "$runner_device_ids" --clip-mixed-nonclip=true)
fi
runner_args=(
  run
    --profile "${VIDEO_CLOUD_LOAD_PROFILE:-safe-staging}" \
    --actors "$runner_actors" \
    --app-route-set "$runner_app_route_set" \
    --device-route-set "$runner_device_route_set" \
    --device-online-mode none \
    --duration "$runner_duration" \
    --iterations 1 \
    --ramp-up "$runner_ramp_up" \
    --virtual-devices "$runner_virtual_devices" \
    --app-concurrency "$runner_app_concurrency" \
    --device-concurrency "$runner_device_concurrency" \
    --app-rate 0 \
    --device-rate 0 \
    --webrtc-media-set off \
    --clip-set storage-poisson \
    --clip-device-ids-file "$runner_clip_ids" \
    --clip-count-per-device "${VIDEO_CLOUD_LOAD_CLIP_COUNT_PER_DEVICE:-10}" \
    --clip-schedule-window "${VIDEO_CLOUD_LOAD_CLIP_SCHEDULE_WINDOW:-30m}" \
    --clip-poisson-seed "${VIDEO_CLOUD_LOAD_CLIP_POISSON_SEED:-20260719}" \
    --clip-upload-concurrency "${VIDEO_CLOUD_LOAD_CLIP_UPLOAD_CONCURRENCY:-64}" \
    --clip-fixture "$runner_fixture" \
    --clip-thumbnail "$runner_thumbnail" \
    --clip-user-private-key "$runner_user_private_key" \
    --clip-server-public-key "$runner_server_public_key" \
    --device-token-map-file "$runner_token_map" \
    --app-token-map-file "${VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE:-}" \
    --api-url "$api_url" \
    --run-id "$run_id" \
    --instance-id "${VIDEO_CLOUD_LOAD_INSTANCE_ID:-local}" \
    --http-timeout "${VIDEO_CLOUD_LOAD_HTTP_TIMEOUT:-60s}" \
    --min-success-rate "${VIDEO_CLOUD_LOAD_MIN_SUCCESS_RATE:-0.995}" \
    --output "$runner_output" \
    --report-output "$runner_report" \
    "${mixed_args[@]}" \
    "$@"
)
if [[ "$runner_exec" == "kubernetes" ]]; then
  runner_env=()
  if [[ -n "${VIDEO_CLOUD_LOAD_ADMIN_TOKEN:-}" ]]; then
    runner_env+=("VIDEO_CLOUD_LOAD_ADMIN_TOKEN=$VIDEO_CLOUD_LOAD_ADMIN_TOKEN")
  fi
  kubectl -n "$runner_namespace" exec "pod/$runner_pod" -- env "${runner_env[@]}" "$runner_binary" "${runner_args[@]}" || load_rc=$?
  kubectl -n "$runner_namespace" cp "$runner_pod:$runner_output" "$artifact_dir/load-results.json"
  kubectl -n "$runner_namespace" cp "$runner_pod:$runner_report" "$artifact_dir/load-report.md"
else
  (cd "$e2e_root" && GOWORK=off go run ./video_cloud/load/cmd/rtk-video-loadtest "${runner_args[@]}") || load_rc=$?
fi

stop_resource_monitor

run_storage_reconcile >"$artifact_dir/reconciliation.json" 2>"$artifact_dir/reconciliation.log" || {
  echo "clip storage reconciliation failed; see $artifact_dir/reconciliation.json and $artifact_dir/reconciliation.log" >&2
  exit 1
}

if [[ "$load_rc" -ne 0 ]]; then
  echo "clip storage load thresholds failed; reconciliation artifacts were still collected" >&2
  exit "$load_rc"
fi

echo "clip storage load test artifacts: $artifact_dir"
