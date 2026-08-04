#!/usr/bin/env bash
set -euo pipefail

bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
account_url="${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:?CLOUD_VALIDATION_ACCOUNT_MANAGER_URL is required}"
video_url="${CLOUD_VALIDATION_VIDEO_CLOUD_URL:?CLOUD_VALIDATION_VIDEO_CLOUD_URL is required}"

secret_root="$(jq -r 'first(.resources[]? | select(.kind == "local_fixture_root") | .path) // ""' "$bundle")"
cleanup_out="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}/fixture-cleanup"
mkdir -p "$cleanup_out"
cleanup_failures=0

if [[ -z "$secret_root" || ! -d "$secret_root" ]]; then
  echo "fixture secret root is unavailable; remote cleanup cannot authenticate" >&2
  exit 1
fi

cleanup_request() {
  local method="$1" header_file="$2" url="$3" body_file="${4:-}" status
  local args=(--silent --show-error --max-time 15 -X "$method" --header "@$header_file" --output /dev/null --write-out '%{http_code}')
  if [[ -n "$body_file" ]]; then
    args+=(-H 'Content-Type: application/json' --data-binary "@$body_file")
  fi
  status="$(curl "${args[@]}" "$url")"
  case "$status" in
    2??|404|409) return 0 ;;
    *) echo "cleanup request failed with HTTP $status: $method $url" >&2; return 1 ;;
  esac
}

while IFS=$'\t' read -r account_device_id device_id; do
  [[ -n "$account_device_id$device_id" ]] || continue
  if [[ -n "$account_device_id" ]]; then
    # Account Manager owns the cross-service unprovision workflow, including
    # Video Cloud entitlement removal. Revoking Video first makes the later
    # unprovision callback non-idempotent and can strand its outbox retry.
    unprovision_body="$secret_root/admin-unprovision.json"
    jq -n --arg run_id "${CLOUD_VALIDATION_RUN_ID:-unknown}" '{
      reason:"SDK deployed-cloud validation cleanup",
      evidence:{test_suite:"sdk_cloud_validation", run_id:$run_id}
    }' > "$unprovision_body"
    chmod 600 "$unprovision_body"
    if ! cleanup_request POST "$secret_root/account-headers.txt" \
      "${account_url%/}/v1/admin/devices/$account_device_id/unprovision" "$unprovision_body"; then
      cleanup_failures=$((cleanup_failures + 1))
    fi
  elif [[ -n "$device_id" ]]; then
    # Only an orphaned Video-only resource bypasses Account Manager.
    if ! cleanup_request POST "$secret_root/video-headers.txt" \
      "${video_url%/}/api/devices/$device_id/entitlement/revoke"; then
      cleanup_failures=$((cleanup_failures + 1))
    fi
  fi
done < <(jq -r '.resources[]? | select(.kind == "device_binding") | [(.id // ""), (.device_id // "")] | @tsv' "$bundle")

while IFS=$'\t' read -r brand_cloud_id user_id; do
  [[ -n "$brand_cloud_id" && -n "$user_id" ]] || continue
  if ! cleanup_request POST "$secret_root/account-headers.txt" \
    "${account_url%/}/v1/admin/brand-clouds/$brand_cloud_id/users/$user_id/app-certificate/revoke"; then
    cleanup_failures=$((cleanup_failures + 1))
  fi
  if ! cleanup_request DELETE "$secret_root/account-headers.txt" \
    "${account_url%/}/v1/admin/brand-clouds/$brand_cloud_id/users/$user_id"; then
    cleanup_failures=$((cleanup_failures + 1))
  fi
done < <(jq -r '.resources[]? | select(.kind == "brand_cloud_user") | [(.brand_cloud_id // ""), (.id // "")] | @tsv' "$bundle")

if (( cleanup_failures > 0 )); then
  echo "fixture cleanup completed with $cleanup_failures failed remote operation(s)" >&2
  exit 1
fi
rm -rf -- "$secret_root"
