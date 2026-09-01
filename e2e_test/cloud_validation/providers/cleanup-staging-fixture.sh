#!/usr/bin/env bash
set -euo pipefail

bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
account_url="${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:?CLOUD_VALIDATION_ACCOUNT_MANAGER_URL is required}"
video_url="${CLOUD_VALIDATION_VIDEO_CLOUD_URL:?CLOUD_VALIDATION_VIDEO_CLOUD_URL is required}"

secret_root="$(jq -r 'first(.resources[]? | select(.kind == "local_fixture_root") | .path) // ""' "$bundle")"
cleanup_out="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}/fixture-cleanup"
mkdir -p "$cleanup_out"
cleanup_failures=0
cleanup_attempts="${CLOUD_VALIDATION_CLEANUP_ATTEMPTS:-3}"
cleanup_retry_delay="${CLOUD_VALIDATION_CLEANUP_RETRY_DELAY_SECONDS:-1}"
attempts_file="$cleanup_out/attempts.ndjson"
report_file="$cleanup_out/cleanup-report.json"
: > "$attempts_file"

if ! [[ "$cleanup_attempts" =~ ^[1-9][0-9]*$ && "$cleanup_retry_delay" =~ ^[0-9]+$ ]]; then
  echo "cleanup retry settings must be non-negative integers and attempts must be greater than zero" >&2
  exit 1
fi

write_cleanup_report() {
  local status="$1" assessment="$2" generated_at
  generated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  jq -s \
    --arg run_id "${CLOUD_VALIDATION_RUN_ID:-unknown}" \
    --arg status "$status" \
    --arg assessment "$assessment" \
    --arg generated_at "$generated_at" \
    '{
      schema_version: 1,
      run_id: $run_id,
      status: $status,
      assessment: $assessment,
      generated_at: $generated_at,
      attempts: .
    }' "$attempts_file" > "$report_file.tmp"
  mv "$report_file.tmp" "$report_file"
  chmod 644 "$report_file"
}

record_cleanup_attempt() {
  local operation="$1" resource_id="$2" method="$3" attempt="$4" curl_exit="$5" http_status="$6" outcome="$7"
  jq -cn \
    --arg operation "$operation" \
    --arg resource_id "$resource_id" \
    --arg method "$method" \
    --argjson attempt "$attempt" \
    --argjson curl_exit "$curl_exit" \
    --arg http_status "$http_status" \
    --arg outcome "$outcome" \
    '{operation:$operation,resource_id:$resource_id,method:$method,attempt:$attempt,curl_exit:$curl_exit,http_status:$http_status,outcome:$outcome}' \
    >> "$attempts_file"
}

if [[ -z "$secret_root" || ! -d "$secret_root" ]]; then
  write_cleanup_report "FAIL" "fixture secret root is unavailable; remote cleanup cannot authenticate"
  echo "fixture secret root is unavailable; remote cleanup cannot authenticate" >&2
  exit 1
fi

cleanup_request() {
  local operation="$1" resource_id="$2" method="$3" header_file="$4" url="$5" body_file="${6:-}"
  local attempt status curl_exit outcome retryable
  local args=(--silent --show-error --max-time 15 -X "$method" --header "@$header_file" --output /dev/null --write-out '%{http_code}')
  if [[ -n "$body_file" ]]; then
    args+=(-H 'Content-Type: application/json' --data-binary "@$body_file")
  fi
  for ((attempt = 1; attempt <= cleanup_attempts; attempt++)); do
    status=""
    if status="$(curl "${args[@]}" "$url")"; then
      curl_exit=0
    else
      curl_exit=$?
    fi
    [[ -n "$status" ]] || status="000"
    case "$status" in
      2??|404|409)
        record_cleanup_attempt "$operation" "$resource_id" "$method" "$attempt" "$curl_exit" "$status" "PASS"
        return 0
        ;;
    esac

    retryable=0
    if (( curl_exit != 0 )); then
      retryable=1
    else
      case "$status" in
        408|425|429|5??) retryable=1 ;;
      esac
    fi
    outcome="FAIL"
    if (( retryable == 1 && attempt < cleanup_attempts )); then
      outcome="RETRY"
    fi
    record_cleanup_attempt "$operation" "$resource_id" "$method" "$attempt" "$curl_exit" "$status" "$outcome"
    if [[ "$outcome" == "RETRY" ]]; then
      sleep "$cleanup_retry_delay"
      continue
    fi
    echo "cleanup operation $operation failed after $attempt attempt(s) (curl_exit=$curl_exit, http_status=$status)" >&2
    return 1
  done
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
    if ! cleanup_request "account_device_unprovision" "$account_device_id" POST "$secret_root/account-headers.txt" \
      "${account_url%/}/v1/admin/devices/$account_device_id/unprovision" "$unprovision_body"; then
      cleanup_failures=$((cleanup_failures + 1))
    fi
  elif [[ -n "$device_id" ]]; then
    # Only an orphaned Video-only resource bypasses Account Manager.
    if ! cleanup_request "video_entitlement_revoke" "$device_id" POST "$secret_root/video-headers.txt" \
      "${video_url%/}/api/devices/$device_id/entitlement/revoke"; then
      cleanup_failures=$((cleanup_failures + 1))
    fi
  fi
done < <(jq -r '.resources[]? | select(.kind == "device_binding") | [(.id // ""), (.device_id // "")] | @tsv' "$bundle")

while IFS=$'\t' read -r brand_cloud_id user_id; do
  [[ -n "$brand_cloud_id" && -n "$user_id" ]] || continue
  if ! cleanup_request "organization_membership_delete" "$user_id" DELETE "$secret_root/account-headers.txt" \
    "${account_url%/}/v1/admin/brand-clouds/$brand_cloud_id/users/$user_id"; then
    cleanup_failures=$((cleanup_failures + 1))
  fi
done < <(jq -r '.resources[]? | select(.kind == "organization_membership") | [(.brand_cloud_id // ""), (.id // "")] | @tsv' "$bundle")

if (( cleanup_failures > 0 )); then
  write_cleanup_report "FAIL" "fixture cleanup completed with $cleanup_failures failed remote operation(s)"
  echo "fixture cleanup completed with $cleanup_failures failed remote operation(s)" >&2
  exit 1
fi
write_cleanup_report "PASS" "all run-owned remote resources were cleaned up"
rm -rf -- "$secret_root"
