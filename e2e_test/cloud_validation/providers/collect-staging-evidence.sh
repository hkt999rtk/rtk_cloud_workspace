#!/usr/bin/env bash
set -euo pipefail

out_dir="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}"
bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
logger_url="${CLOUD_VALIDATION_CLOUD_LOGGER_URL:?CLOUD_VALIDATION_CLOUD_LOGGER_URL is required}"
logger_token="${CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN:?CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN is required}"
platform_result="$out_dir/${CLOUD_VALIDATION_PLATFORM:?CLOUD_VALIDATION_PLATFORM is required}/platform-result.json"
if [[ ! -f "$platform_result" ]]; then
  platform_result="$out_dir/platform-result.json"
fi
ready_file="${CLOUD_VALIDATION_READY_FILE:?CLOUD_VALIDATION_READY_FILE is required}"
evidence_file="$out_dir/cloud-evidence.json"
trigger_evidence="$out_dir/cloud-command-trigger-evidence.json"
offline_evidence="$out_dir/offline-reconnect-evidence.json"

device_id="$(jq -er '.app.device_id' "$bundle")"
foreign_device_id="$(jq -er '.app.foreign_device_id' "$bundle")"
since="$(jq -er '.ready_at' "$ready_file")"
raw_dir="$(jq -er '.resources[] | select(.kind == "local_fixture_root") | .path' "$bundle")"
raw_logs="$raw_dir/cloud-logger-query.json"
logger_headers="$raw_dir/logger-headers.txt"
printf 'Authorization: Bearer %s\n' "$logger_token" > "$logger_headers"
chmod 600 "$logger_headers"

query_logs() {
  local target="$1"
  curl --fail --silent --show-error --max-time 15 --get \
    --header "@$logger_headers" \
    --data-urlencode "since=$since" \
    --data-urlencode "device_id=$target" \
    --data-urlencode 'order=asc' \
    --data-urlencode 'limit=1000' \
    "${logger_url%/}/v1/logs"
}

deadline=$((SECONDS + 30))
while :; do
  own="$(query_logs "$device_id")"
  foreign="$(query_logs "$foreign_device_id")"
  jq -n --argjson own "$own" --argjson foreign "$foreign" \
    '{events: (($own.events // []) + ($foreign.events // []))}' > "$raw_logs"
  if jq -e '.events | length > 0' "$raw_logs" >/dev/null || (( SECONDS >= deadline )); then
    break
  fi
  sleep 2
done
chmod 600 "$raw_logs"

# Cloud Logger intentionally redacts token exchange details and does not retain
# shadow documents. Add independent, customer-safe Cloud probes while the
# run-scoped app certificate still exists. Raw responses remain under the
# mode-0700 fixture root and are removed by cleanup; the report stores only
# status, version, and correlation evidence.
direct_evidence="$raw_dir/direct-cloud-evidence.json"
app_cert="$(jq -er '.app.certificate_path' "$bundle")"
app_key="$(jq -er '.app.private_key_path' "$bundle")"
app_token="$(jq -er '.app.access_token' "$bundle")"
base_url="$(jq -er '.app.base_url' "$bundle")"
probe_headers="$raw_dir/direct-probe-headers.txt"
printf 'Authorization: Bearer %s\n' "$app_token" > "$probe_headers"
chmod 600 "$probe_headers"
token_request="$raw_dir/direct-token-request.json"
token_response="$raw_dir/direct-token-response.json"
info_response="$raw_dir/direct-device-info.json"
foreign_info_response="$raw_dir/direct-foreign-device-info.json"
invalid_token_response="$raw_dir/direct-invalid-token-info.json"
shadow_response="$raw_dir/direct-shadow.json"
jq -n --arg devid "$device_id" '{scope:"app", devid:$devid}' > "$token_request"
chmod 600 "$token_request"

curl_mtls=(--silent --show-error --max-time 15 --cert "$app_cert" --key "$app_key")
if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
  curl_mtls+=(--cacert "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
fi
token_status="000"
if status="$(curl "${curl_mtls[@]}" --output "$token_response" --write-out '%{http_code}' \
  -H 'Content-Type: application/json' --data-binary "@$token_request" \
  "${base_url%/}/request_token")"; then
  token_status="$status"
fi
info_status="000"
if status="$(curl "${curl_mtls[@]}" --output "$info_response" --write-out '%{http_code}' \
  --header "@$probe_headers" "${base_url%/}/api/devices/${device_id}/info")"; then
  info_status="$status"
fi
foreign_info_status="000"
if status="$(curl "${curl_mtls[@]}" --output "$foreign_info_response" --write-out '%{http_code}' \
  --header "@$probe_headers" "${base_url%/}/api/devices/${foreign_device_id}/info")"; then
  foreign_info_status="$status"
fi
invalid_probe_headers="$raw_dir/direct-invalid-probe-headers.txt"
printf 'Authorization: Bearer invalid-cloud-validation-token\n' > "$invalid_probe_headers"
chmod 600 "$invalid_probe_headers"
invalid_token_status="000"
if status="$(curl "${curl_mtls[@]}" --output "$invalid_token_response" --write-out '%{http_code}' \
  --header "@$invalid_probe_headers" "${base_url%/}/api/devices/${device_id}/info")"; then
  invalid_token_status="$status"
fi
shadow_status="000"
if status="$(curl "${curl_mtls[@]}" --output "$shadow_response" --write-out '%{http_code}' \
  --header "@$probe_headers" "${base_url%/}/api/devices/${device_id}/shadow")"; then
  shadow_status="$status"
fi
for response_file in "$token_response" "$info_response" "$foreign_info_response" "$invalid_token_response" "$shadow_response"; do
  if ! jq -e . "$response_file" >/dev/null 2>&1; then
    printf '{}\n' > "$response_file"
  fi
done
chmod 600 "$token_response" "$info_response" "$foreign_info_response" "$invalid_token_response" "$shadow_response" 2>/dev/null || true

jq -n \
  --arg run_id "$CLOUD_VALIDATION_RUN_ID" \
  --arg platform "$CLOUD_VALIDATION_PLATFORM" \
  --arg observed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg token_status "$token_status" \
  --arg info_status "$info_status" \
  --arg foreign_info_status "$foreign_info_status" \
  --arg invalid_token_status "$invalid_token_status" \
  --arg shadow_status "$shadow_status" \
  --slurpfile token "$token_response" \
  --slurpfile shadow "$shadow_response" \
  --slurpfile ready "$ready_file" \
  --slurpfile result "$platform_result" '
  def passed($id): any(($result[0].results // [])[]; .scenario_id == $id and .status == "PASS");
  def event($scenario; $type; $source; $extra): {
    scenario_id: $scenario,
    correlation_id: ($run_id + "-" + $platform + "-" + $scenario),
    type: $type,
    observed_at: $observed_at,
    evidence: ({source_event_ids:[$source]} + $extra)
  };
  ($shadow[0] // {}) as $doc |
  ($doc.state // {}) as $state |
  ($state.desired // {}) as $desired |
  ($state.reported // {}) as $reported |
  ($state.delta // {}) as $delta |
  [
    if passed("token_mtls_http") and $token_status == "200" and (($token[0].access_token // "") | length) > 0
      then event("token_mtls_http"; "token_issued"; "direct-cloud-token-http-200"; {http_status:200}) else empty end,
    if passed("token_mtls_http") and (($ready[0].evidence.device_token_successes // 0) > 0)
      then event("token_mtls_http"; "device_mtls_authenticated"; "virtual-device-mtls-ready"; {device_token_successes:$ready[0].evidence.device_token_successes}) else empty end,
    if passed("token_mtls_http") and $info_status == "200"
      then event("token_mtls_http"; "authorized_device_read"; "direct-cloud-device-http-200"; {http_status:200}) else empty end,
    if passed("cross_cloud_denied") and ($foreign_info_status == "401" or $foreign_info_status == "403")
      then event("cross_cloud_denied"; "authorization_denied"; ("direct-cloud-foreign-http-" + $foreign_info_status); {http_status:($foreign_info_status | tonumber)}) else empty end,
    if passed("invalid_or_expired_token") and ($invalid_token_status == "401" or $invalid_token_status == "403")
      then event("invalid_or_expired_token"; "authorization_denied"; ("direct-cloud-invalid-token-http-" + $invalid_token_status); {http_status:($invalid_token_status | tonumber)}) else empty end,
    if passed("shadow_online_roundtrip") and $shadow_status == "200" and $desired.cloud_validation_run == $run_id
      then event("shadow_online_roundtrip"; "desired_written"; "direct-cloud-shadow-state"; {version:($doc.version // 0)}) else empty end,
    if passed("shadow_online_roundtrip") and $shadow_status == "200" and $reported.cloud_validation_run == $run_id
      then event("shadow_online_roundtrip"; "delta_delivered"; "direct-cloud-shadow-convergence"; {proof:"desired-reported-converged"}) else empty end,
    if passed("shadow_online_roundtrip") and $shadow_status == "200" and $reported.cloud_validation_run == $run_id
      then event("shadow_online_roundtrip"; "reported_written"; "direct-cloud-shadow-state"; {version:($doc.version // 0)}) else empty end,
    if passed("shadow_online_roundtrip") and $shadow_status == "200" and ($delta | length) == 0
      then event("shadow_online_roundtrip"; "delta_cleared"; "direct-cloud-shadow-state"; {version:($doc.version // 0)}) else empty end
  ] | {schema_version:1, run_id:$run_id, platform:$platform, events:.}
' > "$direct_evidence"
chmod 600 "$direct_evidence"

# Map only Cloud-observed log records. The platform correlation ID identifies
# the scenario; source_event_ids retain the independently observed Cloud proof.
jq -n \
  --arg run_id "$CLOUD_VALIDATION_RUN_ID" \
  --arg platform "$CLOUD_VALIDATION_PLATFORM" \
  --slurpfile result "$platform_result" \
  --slurpfile logs "$raw_logs" '
  def text($e): ($e | tostring | ascii_downcase);
  def matches($kind; $e):
    (text($e)) as $t |
    if $kind == "token_issued" then ($t | test("request_token|token issued")) and ($t | test("success|200"))
    elif $kind == "device_mtls_authenticated" then ($t | test("mtls|client certificate|request_token")) and ($t | test("success|200"))
    elif $kind == "authorized_device_read" then ($t | test("device.*(info|read)|/api/devices/")) and ($t | test("success|200"))
    elif $kind == "transport_connected" then ($t | test("websocket|transport")) and ($t | test("connect"))
    elif $kind == "transport_disconnected" then ($t | test("websocket|transport")) and ($t | test("disconnect|close"))
    elif $kind == "authorization_denied" then ($t | test("denied|forbidden|unauthorized|401|403"))
    elif $kind == "desired_written" then ($t | test("shadow")) and ($t | test("desired"))
    elif $kind == "delta_delivered" then ($t | test("shadow")) and ($t | test("delta"))
    elif $kind == "reported_written" then ($t | test("shadow")) and ($t | test("reported"))
    elif $kind == "delta_cleared" then ($t | test("shadow")) and ($t | test("delta.*clear|clear.*delta"))
    elif $kind == "desired_queued" then ($t | test("shadow")) and ($t | test("desired")) and ($t | test("queue|pending|offline"))
    elif $kind == "device_reconnected" then ($t | test("device|mqtt")) and ($t | test("reconnect|connected"))
    else false end;
  def expected($scenario_id):
    if $scenario_id == "token_mtls_http" then ["token_issued", "device_mtls_authenticated", "authorized_device_read"]
    elif $scenario_id == "websocket_lifecycle" then ["transport_connected", "transport_disconnected"]
    elif $scenario_id == "repeated_connect_disconnect" then ["transport_connected", "transport_disconnected"]
    elif $scenario_id == "cross_cloud_denied" or $scenario_id == "invalid_or_expired_token" then ["authorization_denied"]
    elif $scenario_id == "shadow_online_roundtrip" then ["desired_written", "delta_delivered", "reported_written", "delta_cleared"]
    elif $scenario_id == "shadow_offline_reconnect" then ["desired_queued", "device_reconnected", "reported_written", "delta_cleared"]
    else [] end;
  [($result[0].results // [])[] | select(.status == "PASS") as $r |
    expected($r.scenario_id)[] as $kind |
    [($logs[0].events // [])[] | select(matches($kind; .))] as $matched |
    select(($matched | length) > 0) |
    {
      scenario_id: $r.scenario_id,
      correlation_id: $r.correlation_id,
      type: $kind,
      observed_at: (($matched[0].ts // $matched[0].timestamp // $matched[0].observed_at) | fromdateiso8601? // now | todateiso8601),
      evidence: {source_event_ids: [$matched[] | (.event_id // .request_id // .trace_id // "cloud-log-event")] | unique}
    }
  ] as $events |
  {schema_version:1, run_id:$run_id, platform:$platform, events:$events}
' > "$evidence_file"

merged="$evidence_file.tmp"
jq -s '.[0] as $base | .[1] as $direct | $base | .events += ($direct.events // [])' \
  "$evidence_file" "$direct_evidence" > "$merged"
mv "$merged" "$evidence_file"

if [[ -f "$trigger_evidence" ]]; then
  merged="$evidence_file.tmp"
  jq -s '.[0] as $base | .[1] as $trigger | $base | .events += ($trigger.events // [])' \
    "$evidence_file" "$trigger_evidence" > "$merged"
  mv "$merged" "$evidence_file"
fi
if [[ -f "$offline_evidence" ]]; then
  merged="$evidence_file.tmp"
  jq -s '.[0] as $base | .[1] as $offline | $base | .events += ($offline.events // [])' \
    "$evidence_file" "$offline_evidence" > "$merged"
  mv "$merged" "$evidence_file"
fi
chmod 600 "$evidence_file"
