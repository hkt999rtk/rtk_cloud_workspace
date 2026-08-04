#!/usr/bin/env bash
set -euo pipefail

bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
out_dir="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}"
platform="${CLOUD_VALIDATION_PLATFORM:?CLOUD_VALIDATION_PLATFORM is required}"
run_id="${CLOUD_VALIDATION_RUN_ID:?CLOUD_VALIDATION_RUN_ID is required}"
video_url="${CLOUD_VALIDATION_VIDEO_CLOUD_URL:?CLOUD_VALIDATION_VIDEO_CLOUD_URL is required}"
device_url="${CLOUD_VALIDATION_DEVICE_URL:?CLOUD_VALIDATION_DEVICE_URL is required}"
video_admin_token="${CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN:?CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN is required}"
platform_result="$out_dir/$platform/platform-result.json"
evidence_file="$out_dir/auth-resilience-evidence.json"
secret_root="$(jq -er 'first(.resources[] | select(.kind == "local_fixture_root") | .path)' "$bundle")"

device_id="$(jq -er '.app.device_id' "$bundle")"
foreign_device_id="$(jq -er '.app.foreign_device_id' "$bundle")"
app_cert="$(jq -er '.app.certificate_path' "$bundle")"
app_key="$(jq -er '.app.private_key_path' "$bundle")"
device_cert="$(jq -er '.app.device_certificate_path' "$bundle")"
device_key="$(jq -er '.app.device_private_key_path' "$bundle")"
foreign_db="$(jq -er '.foreign_test_data_db' "$bundle")"
sqlite="$(command -v sqlite3)"

curl_tls=(--silent --show-error --max-time 15)
if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
  curl_tls+=(--cacert "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
fi

request_device_token() {
  local cert="$1" key="$2" devid="$3" output="$4" request
  request="$secret_root/auth-resilience-token-request.json"
  jq -n --arg devid "$devid" '{scope:"device",devid:$devid}' > "$request"
  local args=(--url "${device_url%/}/request_token" --cert "$cert" --key "$key" --request "$request" --output "$output" --timeout 15s)
  if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
    args+=(--ca "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
  fi
  if [[ -n "${CLOUD_VALIDATION_DEVICE_TOKEN_HELPER:-}" ]]; then
    "${CLOUD_VALIDATION_DEVICE_TOKEN_HELPER}" "${args[@]}"
  else
    (cd "${RTK_CLOUD_WORKSPACE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}/e2e_test" && GOWORK=off go run ./cloud_validation/cmd/request-token "${args[@]}")
  fi
}

expect_device_token_rejection() {
  local cert="$1" key="$2" devid="$3" request output
  request="$secret_root/auth-resilience-token-request.json"
  output="$secret_root/auth-resilience-rejected-response.json"
  jq -n --arg devid "$devid" '{scope:"device",devid:$devid}' > "$request"
  local args=(--url "${device_url%/}/request_token" --cert "$cert" --key "$key" --request "$request" --output "$output" --timeout 15s --expect-http-status 401,403)
  if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
    args+=(--ca "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
  fi
  rm -f -- "$output"
  if [[ -n "${CLOUD_VALIDATION_DEVICE_TOKEN_HELPER:-}" ]]; then
    "${CLOUD_VALIDATION_DEVICE_TOKEN_HELPER}" "${args[@]}"
  else
    (cd "${RTK_CLOUD_WORKSPACE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}/e2e_test" && GOWORK=off go run ./cloud_validation/cmd/request-token "${args[@]}")
  fi
  test ! -e "$output"
}

require_access_token() {
  local response="$1" description="$2"
  if ! jq -e '.access_token | type == "string" and length > 0' "$response" >/dev/null; then
    echo "$description response did not contain an access token" >&2
    exit 1
  fi
}

probe_device_token() {
  local cert="$1" key="$2" token_file="$3" expected="$4"
  local args=(--url "${device_url%/}/api/devices/$device_id/info" --cert "$cert" --key "$key" --token-file "$token_file" --expect-http-status "$expected" --timeout 15s)
  if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
    args+=(--ca "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
  fi
  if [[ -n "${CLOUD_VALIDATION_MTLS_PROBE_HELPER:-}" ]]; then
    "${CLOUD_VALIDATION_MTLS_PROBE_HELPER}" "${args[@]}"
  else
    (cd "${RTK_CLOUD_WORKSPACE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)}/e2e_test" && GOWORK=off go run ./cloud_validation/cmd/mtls-probe "${args[@]}")
  fi
}

app_request="$secret_root/auth-resilience-app-token-request.json"
app_response="$secret_root/auth-resilience-app-token-response.json"
jq -n --arg devid "$device_id" '{scope:"app",devid:$devid}' > "$app_request"
app_status="$(curl "${curl_tls[@]}" --cert "$app_cert" --key "$app_key" --output "$app_response" --write-out '%{http_code}' \
  --header 'Content-Type: application/json' --data-binary "@$app_request" "${device_url%/}/request_token" || true)"
if [[ "$app_status" != "200" ]] || ! jq -e '.access_token | type == "string" and length > 0' "$app_response" >/dev/null; then
  echo "app certificate bootstrap token request failed with HTTP $app_status" >&2
  exit 1
fi
app_headers="$secret_root/auth-resilience-app-headers.txt"
printf 'Authorization: Bearer %s\n' "$(jq -er '.access_token' "$app_response")" > "$app_headers"
app_read_status="$(curl "${curl_tls[@]}" --cert "$app_cert" --key "$app_key" --output /dev/null --write-out '%{http_code}' \
  --header "@$app_headers" "${device_url%/}/api/devices/$device_id/info" || true)"
if [[ "$app_read_status" != "200" ]]; then
  echo "app certificate bootstrap device read failed with HTTP $app_read_status" >&2
  exit 1
fi

device_initial="$secret_root/auth-resilience-device-initial.json"
device_reissued="$secret_root/auth-resilience-device-reissued.json"
if ! request_device_token "$device_cert" "$device_key" "$device_id" "$device_initial"; then
  echo "initial device certificate token request failed" >&2
  exit 1
fi
require_access_token "$device_initial" "initial device certificate token request"
if ! expect_device_token_rejection "$device_cert" "$device_key" "$foreign_device_id"; then
  echo "device certificate was accepted for a conflicting device identity" >&2
  exit 1
fi
expired_headers="$secret_root/auth-resilience-expired-headers.txt"
printf '%s\n' '{"access_token":"expired-cloud-validation-token"}' > "$expired_headers"
chmod 600 "$expired_headers"
if ! probe_device_token "$device_cert" "$device_key" "$expired_headers" "401,403"; then
  echo "expired device token was not rejected with HTTP 401/403" >&2
  exit 1
fi
if ! request_device_token "$device_cert" "$device_key" "$device_id" "$device_reissued"; then
  echo "device certificate token recovery failed" >&2
  exit 1
fi
require_access_token "$device_reissued" "device certificate token recovery"
if ! probe_device_token "$device_cert" "$device_key" "$device_reissued" "200"; then
  echo "reissued device token did not authorize the device read" >&2
  exit 1
fi

foreign_json="$($sqlite -json "$foreign_db" "select cert_pem, key_pem, chain_pem from device_credentials where device_id = '$foreign_device_id' limit 1")"
foreign_cert="$secret_root/auth-resilience-foreign-cert.pem"
foreign_key="$secret_root/auth-resilience-foreign-key.pem"
jq -er '.[0].cert_pem, (.[0].chain_pem // empty)' <<<"$foreign_json" > "$foreign_cert"
jq -er '.[0].key_pem' <<<"$foreign_json" > "$foreign_key"
chmod 600 "$foreign_cert" "$foreign_key"
foreign_initial="$secret_root/auth-resilience-foreign-initial.json"
if ! request_device_token "$foreign_cert" "$foreign_key" "$foreign_device_id" "$foreign_initial"; then
  echo "foreign device pre-deactivation token request failed" >&2
  exit 1
fi
require_access_token "$foreign_initial" "foreign device pre-deactivation token request"
admin_headers="$secret_root/auth-resilience-admin-headers.txt"
printf 'Authorization: Bearer %s\n' "$video_admin_token" > "$admin_headers"
deactivate_status="$(curl --silent --show-error --max-time 15 --output /dev/null --write-out '%{http_code}' \
  --request POST --header "@$admin_headers" --header 'Content-Type: application/json' --data '{}' \
  "${video_url%/}/api/devices/$foreign_device_id/deactivate" || true)"
if [[ "$deactivate_status" != "200" && "$deactivate_status" != "202" && "$deactivate_status" != "204" ]]; then
  echo "foreign device deactivation failed with HTTP $deactivate_status" >&2
  exit 1
fi
rejected=0
for _ in $(seq 1 30); do
  if expect_device_token_rejection "$foreign_cert" "$foreign_key" "$foreign_device_id"; then
    rejected=1
    break
  fi
  sleep 1
done
if [[ "$rejected" != "1" ]]; then
  echo "deactivated device certificate was not rejected with HTTP 401/403" >&2
  exit 1
fi

observed_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq -n --arg run_id "$run_id" --arg platform "$platform" --arg observed_at "$observed_at" '{
  schema_version:1, run_id:$run_id, platform:$platform,
  events:[
    {scenario_id:"app_certificate_token_bootstrap",correlation_id:($run_id+"-"+$platform+"-app_certificate_token_bootstrap"),type:"account_session_issued",observed_at:$observed_at,evidence:{source_event_ids:["run-scoped-account-login"]}},
    {scenario_id:"app_certificate_token_bootstrap",correlation_id:($run_id+"-"+$platform+"-app_certificate_token_bootstrap"),type:"app_certificate_issued",observed_at:$observed_at,evidence:{source_event_ids:["run-scoped-csr-issuance"]}},
    {scenario_id:"app_certificate_token_bootstrap",correlation_id:($run_id+"-"+$platform+"-app_certificate_token_bootstrap"),type:"token_issued",observed_at:$observed_at,evidence:{source_event_ids:["app-mtls-token-http-200"]}},
    {scenario_id:"app_certificate_token_bootstrap",correlation_id:($run_id+"-"+$platform+"-app_certificate_token_bootstrap"),type:"authorized_device_read",observed_at:$observed_at,evidence:{source_event_ids:["app-device-read-http-200"]}},
    {scenario_id:"device_token_certificate_recovery",correlation_id:($run_id+"-"+$platform+"-device_token_certificate_recovery"),type:"device_mtls_authenticated",observed_at:$observed_at,evidence:{source_event_ids:["device-mtls-token-initial"]}},
    {scenario_id:"device_token_certificate_recovery",correlation_id:($run_id+"-"+$platform+"-device_token_certificate_recovery"),type:"token_issued",observed_at:$observed_at,evidence:{source_event_ids:["device-token-initial"]}},
    {scenario_id:"device_token_certificate_recovery",correlation_id:($run_id+"-"+$platform+"-device_token_certificate_recovery"),type:"conflicting_device_identity_rejected",observed_at:$observed_at,evidence:{source_event_ids:["device-certificate-conflicting-identity-denied"]}},
    {scenario_id:"device_token_certificate_recovery",correlation_id:($run_id+"-"+$platform+"-device_token_certificate_recovery"),type:"token_reissued",observed_at:$observed_at,evidence:{source_event_ids:["device-token-reissued"]}},
    {scenario_id:"device_token_certificate_recovery",correlation_id:($run_id+"-"+$platform+"-device_token_certificate_recovery"),type:"certificate_recovery_succeeded",observed_at:$observed_at,evidence:{source_event_ids:["reissued-token-device-read-http-200"]}},
    {scenario_id:"deactivated_certificate_rejected",correlation_id:($run_id+"-"+$platform+"-deactivated_certificate_rejected"),type:"token_issued",observed_at:$observed_at,evidence:{source_event_ids:["pre-deactivation-token"]}},
    {scenario_id:"deactivated_certificate_rejected",correlation_id:($run_id+"-"+$platform+"-deactivated_certificate_rejected"),type:"device_deactivated",observed_at:$observed_at,evidence:{source_event_ids:["video-device-deactivate"]}},
    {scenario_id:"deactivated_certificate_rejected",correlation_id:($run_id+"-"+$platform+"-deactivated_certificate_rejected"),type:"certificate_rejected",observed_at:$observed_at,evidence:{source_event_ids:["post-deactivation-token-denied"]}}
  ]
}' > "$evidence_file"
chmod 600 "$evidence_file"

merged="$platform_result.tmp"
jq --arg run_id "$run_id" --arg platform "$platform" '
  .results += [
    {scenario_id:"app_certificate_token_bootstrap",status:"PASS",duration_ms:0,correlation_id:($run_id+"-"+$platform+"-app_certificate_token_bootstrap"),evidence:["account login CSR issued an app certificate","app mTLS token authorized the run-owned device"]},
    {scenario_id:"device_token_certificate_recovery",status:"PASS",duration_ms:0,correlation_id:($run_id+"-"+$platform+"-device_token_certificate_recovery"),evidence:["conflicting device identity rejected","expired token rejected","device certificate reissued a usable token"]},
    {scenario_id:"deactivated_certificate_rejected",status:"PASS",duration_ms:0,correlation_id:($run_id+"-"+$platform+"-deactivated_certificate_rejected"),evidence:["pre-deactivation token issued","deactivated certificate rebootstrap rejected"]}
  ] |
  .results |= unique_by(.scenario_id) |
  .status = (if any(.results[]; .status == "FAIL") then "FAIL" elif any(.results[]; .status == "BLOCKED") then "BLOCKED" else "PASS" end)
' "$platform_result" > "$merged"
mv "$merged" "$platform_result"
chmod 600 "$platform_result"

rm -f -- "$app_request" "$app_response" "$app_headers" "$device_initial" "$device_reissued" \
  "$expired_headers" "$foreign_cert" "$foreign_key" "$foreign_initial" \
  "$admin_headers" "$secret_root/auth-resilience-token-request.json" "$secret_root/auth-resilience-rejected-response.json"
