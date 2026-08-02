#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/sdk-cloud-provider-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

# Live evidence is qualified against the workspace canonical contracts source.
# The SDK's nested pinned copy is implementation input, not the spec inventory
# ground truth consumed by the feature-evidence importer.
grep -Fq '"$workspace_root/repos/rtk_cloud_contracts_doc"' "$root/e2e_test/cloud_validation/scripts/run-cloud-validation.sh"
if grep -Fq '"$workspace_root/repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc"' "$root/e2e_test/cloud_validation/scripts/run-cloud-validation.sh"; then
  echo "cloud validation must not anchor evidence to the SDK nested contracts copy" >&2
  exit 1
fi

mkdir -p "$tmp/bin" "$tmp/workspace/scripts" "$tmp/source-env/state" "$tmp/out" "$tmp/secrets"
printf 'state' > "$tmp/source-env/state/marker"
printf 'test ca' > "$tmp/ca.pem"

sqlite_bin="$(command -v sqlite3)"
cat > "$tmp/workspace/scripts/setup-staging-e2e-data.sh" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${SETUP_ARGS_LOG:?SETUP_ARGS_LOG is required}"
env_root=""
brandname=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-root) env_root="$2"; shift 2 ;;
    --brandname) brandname="$2"; shift 2 ;;
    *) shift ;;
  esac
done
slug="$(printf '%s' "$brandname" | tr '[:upper:] ' '[:lower:]-')"
case "$slug" in
  sdk-e2e-ios) tenant_slug="sdk-e2e-ios-a579a0e7" ;;
  sdk-e2e-android) tenant_slug="sdk-e2e-android-0d152276" ;;
  *) tenant_slug="$slug" ;;
esac
db="$env_root/artifacts/test-data/${slug}-test-data.sqlite"
mkdir -p "$(dirname "$db")"
cloud_id="cloud-${slug}"
user_id="user-${slug}"
device_id="device-${slug}"
account_device_id="account-device-${slug}"
"$SQLITE_BIN" "$db" <<SQL
create table users (brandname text, email text, brand_cloud_id text, tenant_slug text, app_credentials_json text, app_certificate_json text, body_json text);
create table device_bindings (brandname text, tenant_slug text, device_id text, account_device_id text, assigned_email text, assignment_index integer);
create table device_credentials (brandname text, device_id text, cert_pem text, key_pem text, chain_pem text);
insert into users values ('$brandname','run-user@users.local','$cloud_id','$tenant_slug','{"private_key_pem":"-----BEGIN PRIVATE KEY-----\\nkey\\n-----END PRIVATE KEY-----"}','{"certificate_pem":"-----BEGIN CERTIFICATE-----\\nleaf\\n-----END CERTIFICATE-----","certificate_chain_pem":"-----BEGIN CERTIFICATE-----\\nchain\\n-----END CERTIFICATE-----"}','{"brand_cloud_user_id":"$user_id"}');
alter table users add column password text default 'fixture-password';
insert into device_bindings values ('$brandname','$tenant_slug','$device_id','$account_device_id','run-user@users.local',1);
insert into device_credentials values ('$brandname','$device_id','-----BEGIN CERTIFICATE-----\nleaf\n-----END CERTIFICATE-----','-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----','-----BEGIN CERTIFICATE-----\nchain\n-----END CERTIFICATE-----');
SQL
chmod 600 "$db"
if [[ "${FAIL_AFTER_USER:-0}" == "1" ]]; then
  "$SQLITE_BIN" "$db" 'delete from device_bindings; delete from device_credentials;'
  exit 28
fi
if [[ "${FAIL_AFTER_DB:-0}" == "1" ]]; then
  exit 27
fi
SCRIPT
chmod +x "$tmp/workspace/scripts/setup-staging-e2e-data.sh"

cat > "$tmp/bin/curl" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
joined="$*"
if [[ -n "${CURL_LOG:-}" ]]; then
  printf '%s\n' "$joined" >> "$CURL_LOG"
fi
if [[ "$joined" == *"--write-out"* && "$joined" == *"/request_token"* ]]; then
  output=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --output) output="$2"; shift 2 ;;
      *) shift ;;
    esac
  done
  test -n "$output"
  if [[ -n "${AUTH_DEACTIVATED_STATE:-}" && -e "$AUTH_DEACTIVATED_STATE" && "$joined" == *"auth-resilience-foreign-cert.pem"* ]]; then
    printf '%s\n' '{"error":"certificate_rejected"}' > "$output"
    printf '403'
  elif [[ -n "${ROTATION_TOKEN_STATE:-}" && ! -e "$ROTATION_TOKEN_STATE" ]]; then
    : > "$ROTATION_TOKEN_STATE"
    printf '%s\n' '{"error":"authorization_pending"}' > "$output"
    printf '401'
  else
    printf '%s\n' '{"access_token":"live-access","refresh_token":"live-refresh"}' > "$output"
    printf '200'
  fi
elif [[ "$joined" == *"--write-out"* && "$joined" == *"entitlement/revoke"* && "${FAIL_ENTITLEMENT_REVOKE:-0}" == "1" ]]; then
  printf '500'
elif [[ "$joined" == *"--write-out"* && "$joined" == *"/commands"* ]]; then
  printf '200'
elif [[ "$joined" == *"--write-out"* && "$joined" == *"auth-resilience-expired-headers.txt"* ]]; then
  printf '401'
elif [[ "$joined" == *"--write-out"* && "$joined" == *"/api/devices/"* && "$joined" == *"/deactivate"* ]]; then
  : > "${AUTH_DEACTIVATED_STATE:?AUTH_DEACTIVATED_STATE is required}"
  printf '204'
elif [[ "$joined" == *"--write-out"* && "$joined" == *"auth-resilience-"* && "$joined" == *"/info"* ]]; then
  printf '200'
elif [[ "$joined" == *"--write-out"* && "$joined" == *"device-sdk-e2e-android/info"* ]]; then
  printf '403'
elif [[ "$joined" == *"--write-out"* && "$joined" == *"direct-invalid-probe-headers.txt"* ]]; then
  printf '401'
elif [[ "$joined" == *"--write-out"* ]]; then
  printf '204'
elif [[ "$joined" == *"/v1/admin/brand-clouds"* ]]; then
  printf '%s\n' '{"brand_clouds":[{"id":"cloud-sdk-e2e-ios","name":"SDK E2E iOS","tenant_slug":"sdk-e2e-ios-a579a0e7","status":"active"},{"id":"cloud-sdk-e2e-android","name":"SDK E2E Android","tenant_slug":"sdk-e2e-android-0d152276","status":"active"}]}'
elif [[ "$joined" == *"/request_token"* ]]; then
  printf '%s\n' '{"access_token":"live-access","refresh_token":"live-refresh"}'
elif [[ "$joined" == *"/auth/login"* ]]; then
  printf '%s\n' '{"tokens":{"access_token":"rotated-access","refresh_token":"rotated-refresh"},"app_certificate":{"status":"issued","certificate_pem":"-----BEGIN CERTIFICATE-----\nrotated\n-----END CERTIFICATE-----"}}'
elif [[ "$joined" == *"/things/device-sdk-e2e-ios/shadow"* ]]; then
  printf '%s\n' '{"state":{"desired":{"cloud_validation_run":"run-ios","cloud_validation_scenario":"shadow_offline_reconnect","enabled":true},"reported":{"cloud_validation_run":"run-ios","cloud_validation_scenario":"shadow_offline_reconnect","enabled":true},"delta":{}},"version":3}'
elif [[ "$joined" == *"/v1/logs"* ]]; then
  printf '%s\n' '{"events":[
    {"event_id":"evt-token","ts":"2026-07-18T10:00:01Z","msg":"mTLS request_token issued success 200","device_id":"device-sdk-e2e-ios"},
    {"event_id":"evt-read","ts":"2026-07-18T10:00:02Z","msg":"device info read success 200","device_id":"device-sdk-e2e-ios"},
    {"event_id":"evt-ws-open","ts":"2026-07-18T10:00:03Z","msg":"websocket transport connected","device_id":"device-sdk-e2e-ios"},
    {"event_id":"evt-ws-close","ts":"2026-07-18T10:00:04Z","msg":"websocket transport disconnected","device_id":"device-sdk-e2e-ios"},
    {"event_id":"evt-denied","ts":"2026-07-18T10:00:05Z","msg":"authorization denied 401","device_id":"device-sdk-e2e-android"},
    {"event_id":"evt-desired","ts":"2026-07-18T10:00:06Z","msg":"shadow desired written","device_id":"device-sdk-e2e-ios"},
    {"event_id":"evt-delta","ts":"2026-07-18T10:00:07Z","msg":"shadow delta delivered","device_id":"device-sdk-e2e-ios"},
    {"event_id":"evt-reported","ts":"2026-07-18T10:00:08Z","msg":"shadow reported written","device_id":"device-sdk-e2e-ios"},
    {"event_id":"evt-cleared","ts":"2026-07-18T10:00:09Z","msg":"shadow delta cleared","device_id":"device-sdk-e2e-ios"}
  ]}'
else
  exit 0
fi
SCRIPT
cat > "$tmp/bin/go" <<'SCRIPT'
#!/usr/bin/env bash
if [[ -n "${GO_ARGS_LOG:-}" ]]; then
  printf '%s\n' "$*" >> "$GO_ARGS_LOG"
fi
exit 0
SCRIPT
cat > "$tmp/bin/openssl" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -out) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
test -n "$out"
printf '%s\n' 'test-pkcs12' > "$out"
SCRIPT
cat > "$tmp/bin/request-device-token" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
output=""
cert=""
expect_status=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --cert) cert="$2"; shift 2 ;;
    --expect-http-status) expect_status="$2"; shift 2 ;;
    *) shift ;;
  esac
done
test -n "$output"
if [[ -n "$expect_status" && "$cert" == *"auth-resilience-foreign-cert.pem" && -e "${AUTH_DEACTIVATED_STATE:?AUTH_DEACTIVATED_STATE is required}" ]]; then
  exit 0
fi
if [[ -n "${DEVICE_TOKEN_STATE:-}" && ! -e "$DEVICE_TOKEN_STATE" ]]; then
  : > "$DEVICE_TOKEN_STATE"
  exit 1
fi
printf '%s\n' '{"access_token":"device-transport-token"}' > "$output"
chmod 600 "$output"
SCRIPT
cat > "$tmp/bin/mtls-probe" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail
expected=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --expect-http-status) expected="$2"; shift 2 ;;
    *) shift ;;
  esac
done
case "$expected" in
  200|401,403) exit 0 ;;
  *) exit 1 ;;
esac
SCRIPT
chmod +x "$tmp/bin/curl" "$tmp/bin/go" "$tmp/bin/openssl" "$tmp/bin/request-device-token" "$tmp/bin/mtls-probe"

export PATH="$tmp/bin:$PATH"
export SQLITE_BIN="$sqlite_bin"
export SETUP_ARGS_LOG="$tmp/setup-args.log"
export RTK_CLOUD_WORKSPACE="$tmp/workspace"
export CLOUD_VALIDATION_OUT_DIR="$tmp/out"
export CLOUD_VALIDATION_RUNTIME_BUNDLE="$tmp/secrets/run-ios/runtime-bundle.json"
export CLOUD_VALIDATION_RUN_ID="run-ios"
export CLOUD_VALIDATION_PLATFORM="ios"
export CLOUD_VALIDATION_ENV_ROOT="source-env"
export CLOUD_VALIDATION_ACCOUNT_MANAGER_URL="https://account.test"
export CLOUD_VALIDATION_VIDEO_CLOUD_URL="https://video.test"
export CLOUD_VALIDATION_DEVICE_URL="https://device.test"
export CLOUD_VALIDATION_PLATFORM_ADMIN_TOKEN="secret-admin"
export CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN="secret-video-admin"
export CLOUD_VALIDATION_CA_BUNDLE="ca.pem"
export CLOUD_VALIDATION_SECRET_ROOT="$tmp/secrets"
export CLOUD_VALIDATION_IOS_CLOUD_SLUG="sdk-e2e-ios-a579a0e7"
export CLOUD_VALIDATION_ANDROID_CLOUD_SLUG="sdk-e2e-android-0d152276"
export CLOUD_VALIDATION_DEVICE_TOKEN_HELPER="$tmp/bin/request-device-token"
export CLOUD_VALIDATION_MTLS_PROBE_HELPER="$tmp/bin/mtls-probe"
export ROTATION_TOKEN_STATE="$tmp/rotation-token-ready"
export DEVICE_TOKEN_STATE="$tmp/device-token-ready"
export AUTH_DEACTIVATED_STATE="$tmp/auth-device-deactivated"

(cd "$tmp" && "$root/e2e_test/cloud_validation/providers/setup-staging-fixture.sh")
test -e "$tmp/secrets/run-ios/environment/state"
[[ "$(readlink "$tmp/secrets/run-ios/environment/state")" == /* ]]
grep -q -- '--brandname SDK E2E iOS .*--from-step create_users' "$SETUP_ARGS_LOG"
grep -q -- '--brandname SDK E2E Android .*--from-step create_users' "$SETUP_ARGS_LOG"
while IFS= read -r device_prefix; do
  test "${#device_prefix}" -le 58
done < <(sed -nE 's/.*--device-prefix ([^ ]+).*/\1/p' "$SETUP_ARGS_LOG")
export CLOUD_VALIDATION_ENV_ROOT="$tmp/source-env"
export CLOUD_VALIDATION_CA_BUNDLE="$tmp/ca.pem"
test "$(stat -f '%Lp' "$CLOUD_VALIDATION_RUNTIME_BUNDLE")" = "600"
jq -e '.run_id == "run-ios" and .brand_cloud_slug == "sdk-e2e-ios-a579a0e7" and .brand_cloud_active == true and .app.device_id == "device-sdk-e2e-ios" and (.app.device_transport_access_token | length > 0) and .app.foreign_device_id == "device-sdk-e2e-android" and (.app.revoked_pkcs12_path | length > 0) and (.app.revoked_pkcs12_password | length > 0) and (.test_data_db | endswith("/sdk-e2e-ios-test-data.sqlite")) and ((.resources | length) == 7)' "$CLOUD_VALIDATION_RUNTIME_BUNDLE" >/dev/null

export CLOUD_VALIDATION_READY_FILE="$tmp/out/virtual-device-ready.json"
export CLOUD_VALIDATION_ACCOUNT_MANAGER_URL="https://account.test"
export CLOUD_VALIDATION_VIDEO_CLOUD_URL="https://video.test"
export CLOUD_VALIDATION_DEVICE_URL="https://device.test"
export CLOUD_VALIDATION_MQTT_ADDR="mqtt.test:8883"
export GO_ARGS_LOG="$tmp/go-args.log"
"$root/e2e_test/cloud_validation/scripts/run-virtual-device.sh"
if grep -q -- '--sdk-reconnect-signal-file' "$GO_ARGS_LOG"; then
  echo "deploy virtual-device command unexpectedly included nightly arguments" >&2
  exit 1
fi
jq '.validation_profile = "nightly"' "$CLOUD_VALIDATION_RUNTIME_BUNDLE" > "$CLOUD_VALIDATION_RUNTIME_BUNDLE.tmp"
mv "$CLOUD_VALIDATION_RUNTIME_BUNDLE.tmp" "$CLOUD_VALIDATION_RUNTIME_BUNDLE"
chmod 600 "$CLOUD_VALIDATION_RUNTIME_BUNDLE"
"$root/e2e_test/cloud_validation/scripts/run-virtual-device.sh"
tail -1 "$GO_ARGS_LOG" | grep -q -- '--sdk-reconnect-signal-file'
tail -1 "$GO_ARGS_LOG" | grep -q -- '--sdk-invalid-credential-probe'

cat > "$tmp/out/platform-result.json" <<'JSON'
{"schema_version":1,"run_id":"run-ios","platform":"ios","sdk_commit":"test","server_version":"test","status":"PASS","results":[
  {"scenario_id":"token_mtls_http","status":"PASS","duration_ms":1,"correlation_id":"run-ios-token"},
  {"scenario_id":"websocket_lifecycle","status":"PASS","duration_ms":1,"correlation_id":"run-ios-ws"},
  {"scenario_id":"websocket_receive_roundtrip","status":"PASS","duration_ms":1,"correlation_id":"run-ios-ios-websocket_receive_roundtrip"},
  {"scenario_id":"cross_cloud_denied","status":"PASS","duration_ms":1,"correlation_id":"run-ios-denied"},
  {"scenario_id":"shadow_online_roundtrip","status":"PASS","duration_ms":1,"correlation_id":"run-ios-shadow"}
]}
JSON
mkdir -p "$tmp/out/ios"
cp "$tmp/out/platform-result.json" "$tmp/out/ios/platform-result.json"
touch "$ROTATION_TOKEN_STATE"
"$root/e2e_test/cloud_validation/scripts/run-auth-resilience-scenarios.sh"
jq -e '[.results[].scenario_id] | contains(["app_certificate_token_bootstrap","device_token_certificate_recovery","deactivated_certificate_rejected"])' "$tmp/out/ios/platform-result.json" >/dev/null
jq -e '[.events[].type] | contains(["account_session_issued","app_certificate_issued","token_reissued","device_deactivated","certificate_rejected"])' "$tmp/out/auth-resilience-evidence.json" >/dev/null
export CLOUD_VALIDATION_COMMAND_TRIGGER_TIMEOUT_SECONDS=2
"$root/e2e_test/cloud_validation/scripts/run-cloud-command-trigger.sh"
jq -e '.events[0].type == "command_dispatched" and .events[0].evidence.http_status == 200' "$tmp/out/cloud-command-trigger-evidence.json" >/dev/null
cat > "$tmp/out/virtual-device/offline-ready" <<'JSON'
{"schema_version":1,"run_id":"run-ios","status":"OFFLINE"}
JSON
chmod 600 "$tmp/out/virtual-device/offline-ready"
export CURL_LOG="$tmp/offline-controller-curl.log"
"$root/e2e_test/cloud_validation/scripts/run-offline-reconnect-controller.sh"
grep -q -- '--cert .* --key ' "$CURL_LOG"
grep -q -- '/things/device-sdk-e2e-ios/shadow' "$CURL_LOG"
unset CURL_LOG
test -f "$tmp/out/virtual-device/offline-reconnect.signal"
jq -e '[.events[].type] | contains(["desired_queued","device_reconnected","reported_written","delta_cleared"])' "$tmp/out/offline-reconnect-evidence.json" >/dev/null
cat > "$tmp/out/ready.json" <<'JSON'
{"schema_version":1,"run_id":"run-ios","status":"READY","ready_at":"2026-07-18T10:00:00Z","device_ids":["device-sdk-e2e-ios"]}
JSON
export CLOUD_VALIDATION_READY_FILE="$tmp/out/ready.json"
export CLOUD_VALIDATION_CLOUD_LOGGER_URL="https://logger.test"
export CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN="secret-logger"
"$root/e2e_test/cloud_validation/providers/collect-staging-evidence.sh"
jq -e '[.events[].type] | contains(["token_issued","device_mtls_authenticated","authorized_device_read","transport_connected","transport_disconnected","command_dispatched","authorization_denied","desired_written","delta_delivered","desired_queued","device_reconnected","reported_written","delta_cleared"])' "$tmp/out/cloud-evidence.json" >/dev/null

export CURL_LOG="$tmp/cleanup-curl.log"
export FAIL_ENTITLEMENT_REVOKE=1
set +e
"$root/e2e_test/cloud_validation/providers/cleanup-staging-fixture.sh"
cleanup_rc=$?
set -e
test "$cleanup_rc" -eq 1
test -f "$CLOUD_VALIDATION_RUNTIME_BUNDLE"
grep -q '/v1/admin/devices/account-device-sdk-e2e-ios/unprovision' "$CURL_LOG"
grep -q '/v1/admin/devices/account-device-sdk-e2e-android/unprovision' "$CURL_LOG"
grep -q '/v1/admin/brand-clouds/cloud-sdk-e2e-ios/users/user-sdk-e2e-ios' "$CURL_LOG"
grep -q '/v1/admin/brand-clouds/cloud-sdk-e2e-android/users/user-sdk-e2e-android' "$CURL_LOG"
unset FAIL_ENTITLEMENT_REVOKE
"$root/e2e_test/cloud_validation/providers/cleanup-staging-fixture.sh"
test ! -e "$tmp/secrets/run-ios"

# A setup failure after remote resources are created must still produce a
# secret-free recovery manifest and permit deterministic cleanup.
export CLOUD_VALIDATION_RUNTIME_BUNDLE="$tmp/secrets/run-ios-failed/runtime-bundle.json"
export CLOUD_VALIDATION_RESOURCE_MANIFEST="$tmp/out/failed-resource-manifest.json"
export CLOUD_VALIDATION_RUN_ID="run-ios-failed"
export FAIL_AFTER_DB=1
set +e
"$root/e2e_test/cloud_validation/scripts/setup-fixture.sh"
setup_rc=$?
set -e
unset FAIL_AFTER_DB
test "$setup_rc" -eq 27
test -f "$CLOUD_VALIDATION_RESOURCE_MANIFEST"
jq -e '.setup_failure.exit_code == 27 and .cleanup_required == true and ([.resources[].kind] | contains(["brand_cloud_user","device_binding","local_fixture_root"]))' "$CLOUD_VALIDATION_RESOURCE_MANIFEST" >/dev/null
"$root/e2e_test/cloud_validation/scripts/cleanup-fixture.sh"
test ! -e "$tmp/secrets/run-ios-failed"
test -f "$CLOUD_VALIDATION_RESOURCE_MANIFEST"

# Empty sqlite3 -json query output is not JSON. A failure after user creation
# but before device enrollment must normalize it to [] and retain user cleanup.
long_run_id="staging-sdk-ios-release-20260718T235959Z-extra-long"
export CLOUD_VALIDATION_RUNTIME_BUNDLE="$tmp/secrets/$long_run_id/runtime-bundle.json"
export CLOUD_VALIDATION_RESOURCE_MANIFEST="$tmp/out/user-only-resource-manifest.json"
export CLOUD_VALIDATION_RUN_ID="$long_run_id"
export FAIL_AFTER_USER=1
set +e
"$root/e2e_test/cloud_validation/scripts/setup-fixture.sh"
setup_rc=$?
set -e
unset FAIL_AFTER_USER
test "$setup_rc" -eq 28
long_run_device_prefix="$(tail -1 "$SETUP_ARGS_LOG" | sed -nE 's/.*--device-prefix ([^ ]+).*/\1/p')"
test -n "$long_run_device_prefix"
test "${#long_run_device_prefix}" -le 58
[[ "$long_run_device_prefix" =~ -[0-9a-f]{10}$ ]]
jq -e '
  .setup_failure.exit_code == 28 and .cleanup_required == true and
  ([.resources[].kind] | contains(["brand_cloud_user","local_fixture_root"])) and
  ([.resources[].kind] | index("device_binding") | not)
' "$CLOUD_VALIDATION_RESOURCE_MANIFEST" >/dev/null
"$root/e2e_test/cloud_validation/scripts/cleanup-fixture.sh"
test ! -e "$tmp/secrets/$long_run_id"

mkdir -p "$tmp/upload"
printf '%s\n' 'safe customer-facing evidence' > "$tmp/upload/safe.log"
printf '%s\n' 'Authorization: Bearer must-not-upload' > "$tmp/upload/unsafe.log"
set +e
"$root/e2e_test/cloud_validation/scripts/sanitize-upload-artifacts.sh" "$tmp/upload"
sanitize_rc=$?
set -e
test "$sanitize_rc" -eq 1
test -f "$tmp/upload/safe.log"
test ! -e "$tmp/upload/unsafe.log"
grep -qx 'unsafe.log' "$tmp/upload/redaction-failures.txt"
