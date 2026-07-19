#!/usr/bin/env bash
set -euo pipefail

workspace_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
e2e_root="${workspace_root}/e2e_test"
run_id="${VIDEO_CLOUD_LOAD_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
artifact_dir="${VIDEO_CLOUD_LOAD_ARTIFACT_DIR:-${workspace_root}/.artifacts/e2e_test/video_cloud/load/${run_id}/clip-storage}"
mkdir -p "$artifact_dir"

clip_ids_file="${VIDEO_CLOUD_LOAD_CLIP_DEVICE_IDS_FILE:?VIDEO_CLOUD_LOAD_CLIP_DEVICE_IDS_FILE is required}"
fixture="${VIDEO_CLOUD_LOAD_CLIP_FIXTURE:-${workspace_root}/e2e_test/video_cloud/load/testdata/clip_1080p_h264_3mbps_15s.mp4}"
thumbnail="${VIDEO_CLOUD_LOAD_CLIP_THUMBNAIL:-${workspace_root}/e2e_test/video_cloud/load/testdata/thumbnail_1080p.jpg}"
api_url="${VIDEO_CLOUD_LOAD_API_URL:?VIDEO_CLOUD_LOAD_API_URL is required}"
token_map="${VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE:?VIDEO_CLOUD_LOAD_DEVICE_TOKEN_MAP_FILE is required}"

[[ -r "$clip_ids_file" ]] || { echo "clip camera inventory is not readable: $clip_ids_file" >&2; exit 2; }
[[ -r "$fixture" ]] || { echo "clip fixture is not readable: $fixture" >&2; exit 2; }
[[ -r "$thumbnail" ]] || { echo "clip thumbnail is not readable: $thumbnail" >&2; exit 2; }
[[ -r "$token_map" ]] || { echo "clip token map is not readable: $token_map" >&2; exit 2; }

count="$(awk 'NF {n++} END {print n+0}' "$clip_ids_file")"
expected=$((count * ${VIDEO_CLOUD_LOAD_CLIP_COUNT_PER_DEVICE:-10}))
cat >"$artifact_dir/clip-storage-input.json" <<EOF
{"run_id":"$run_id","camera_devices":$count,"expected_clips":$expected,"schedule_window":"${VIDEO_CLOUD_LOAD_CLIP_SCHEDULE_WINDOW:-30m}","poisson_seed":${VIDEO_CLOUD_LOAD_CLIP_POISSON_SEED:-20260719},"fixture":"$fixture","thumbnail":"$thumbnail"}
EOF

(
  cd "$e2e_root"
  GOWORK=off go run ./video_cloud/load/cmd/rtk-video-loadtest run \
    --profile "${VIDEO_CLOUD_LOAD_PROFILE:-safe-staging}" \
    --actors device \
    --device-route-set off \
    --device-online-mode none \
    --webrtc-media-set off \
    --clip-set storage-poisson \
    --clip-device-ids-file "$clip_ids_file" \
    --clip-count-per-device "${VIDEO_CLOUD_LOAD_CLIP_COUNT_PER_DEVICE:-10}" \
    --clip-schedule-window "${VIDEO_CLOUD_LOAD_CLIP_SCHEDULE_WINDOW:-30m}" \
    --clip-poisson-seed "${VIDEO_CLOUD_LOAD_CLIP_POISSON_SEED:-20260719}" \
    --clip-upload-concurrency "${VIDEO_CLOUD_LOAD_CLIP_UPLOAD_CONCURRENCY:-64}" \
    --clip-fixture "$fixture" \
    --clip-thumbnail "$thumbnail" \
    --device-token-map-file "$token_map" \
    --app-token-map-file "${VIDEO_CLOUD_LOAD_APP_TOKEN_MAP_FILE:-}" \
    --api-url "$api_url" \
    --run-id "$run_id" \
    --instance-id "${VIDEO_CLOUD_LOAD_INSTANCE_ID:-local}" \
    --http-timeout "${VIDEO_CLOUD_LOAD_HTTP_TIMEOUT:-60s}" \
    --min-success-rate "${VIDEO_CLOUD_LOAD_MIN_SUCCESS_RATE:-0.995}" \
    --output "$artifact_dir/load-results.json" \
    --report-output "$artifact_dir/load-report.md" \
    "$@"
)

echo "clip storage load test artifacts: $artifact_dir"
