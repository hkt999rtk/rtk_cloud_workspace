#!/usr/bin/env bash
set -euo pipefail

workspace="${RTK_CLOUD_WORKSPACE:?RTK_CLOUD_WORKSPACE is required}"
out_dir="${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}"
bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:?CLOUD_VALIDATION_RUNTIME_BUNDLE is required}"
run_id="${CLOUD_VALIDATION_RUN_ID:?CLOUD_VALIDATION_RUN_ID is required}"
platform="${CLOUD_VALIDATION_PLATFORM:?CLOUD_VALIDATION_PLATFORM is required}"
source_env_root="${CLOUD_VALIDATION_ENV_ROOT:?CLOUD_VALIDATION_ENV_ROOT is required}"
account_url="${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:?CLOUD_VALIDATION_ACCOUNT_MANAGER_URL is required}"
video_url="${CLOUD_VALIDATION_VIDEO_CLOUD_URL:?CLOUD_VALIDATION_VIDEO_CLOUD_URL is required}"
device_url="${CLOUD_VALIDATION_DEVICE_URL:?CLOUD_VALIDATION_DEVICE_URL is required}"
admin_token="${CLOUD_VALIDATION_PLATFORM_ADMIN_TOKEN:?CLOUD_VALIDATION_PLATFORM_ADMIN_TOKEN is required}"
ca_bundle="${CLOUD_VALIDATION_CA_BUNDLE:?CLOUD_VALIDATION_CA_BUNDLE is required}"

platform_upper="$(printf '%s' "$platform" | tr '[:lower:]' '[:upper:]')"
cloud_var="CLOUD_VALIDATION_${platform_upper}_CLOUD_SLUG"
cloud_slug="${!cloud_var:?$cloud_var is required}"
if [[ "$platform" == "ios" ]]; then
  foreign_cloud_slug="${CLOUD_VALIDATION_ANDROID_CLOUD_SLUG:?CLOUD_VALIDATION_ANDROID_CLOUD_SLUG is required}"
else
  foreign_cloud_slug="${CLOUD_VALIDATION_IOS_CLOUD_SLUG:?CLOUD_VALIDATION_IOS_CLOUD_SLUG is required}"
fi

if [[ ! "$run_id" =~ ^[A-Za-z0-9._-]+$ || ! "$cloud_slug" =~ ^[A-Za-z0-9._-]+$ || ! "$foreign_cloud_slug" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "run_id and cloud slug must contain only letters, numbers, dot, underscore, or dash" >&2
  exit 2
fi
if [[ "$cloud_slug" == "$foreign_cloud_slug" ]]; then
  echo "primary and foreign Brand Cloud slugs must differ" >&2
  exit 2
fi
if [[ ! -d "$source_env_root" || ! -f "$ca_bundle" ]]; then
  echo "CLOUD_VALIDATION_ENV_ROOT or CA bundle is unavailable" >&2
  exit 2
fi
source_env_root="$(cd "$source_env_root" && pwd -P)"
ca_bundle="$(cd "$(dirname "$ca_bundle")" && pwd -P)/$(basename "$ca_bundle")"

sqlite="$(command -v sqlite3 || true)"
if [[ -z "$sqlite" ]]; then
  echo "sqlite3 is required by the built-in staging fixture provider" >&2
  exit 2
fi

secret_base="${CLOUD_VALIDATION_SECRET_ROOT:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sdk-cloud-validation-secrets}"
secret_root="$secret_base/$run_id"
isolated_env="$secret_root/environment"
mkdir -p "$secret_root" "$isolated_env"
chmod 700 "$secret_root" "$isolated_env"
account_headers="$secret_root/account-headers.txt"
video_headers="$secret_root/video-headers.txt"
printf 'Authorization: Bearer %s\n' "$admin_token" > "$account_headers"
printf 'Authorization: Bearer %s\n' "${CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN:?CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN is required}" > "$video_headers"
chmod 600 "$account_headers" "$video_headers"

# Reuse deployment configuration/state while keeping each run's SQLite data and
# credential files isolated. The artifacts directory is deliberately not linked.
for entry in "$source_env_root"/*; do
  [[ -e "$entry" ]] || continue
  [[ "$(basename "$entry")" == "artifacts" ]] && continue
  ln -s "$entry" "$isolated_env/$(basename "$entry")"
done
mkdir -p "$isolated_env/artifacts"
chmod 700 "$isolated_env/artifacts"

normalized_run="$(printf '%s' "$run_id" | tr '[:upper:]_.:' '[:lower:]---')"
if command -v sha256sum >/dev/null 2>&1; then
  run_hash="$(printf '%s' "$run_id" | sha256sum | awk '{print substr($1,1,10)}')"
else
  run_hash="$(printf '%s' "$run_id" | shasum -a 256 | awk '{print substr($1,1,10)}')"
fi
bounded_run_fragment() {
  local max_length="$1" prefix_length
  if (( ${#normalized_run} <= max_length )); then
    printf '%s' "$normalized_run"
    return
  fi
  if (( max_length < 12 )); then
    echo "bounded run fragment must leave room for a hash" >&2
    return 2
  fi
  prefix_length=$((max_length - 11))
  printf '%s-%s' "$(printf '%s' "$normalized_run" | cut -c1-"$prefix_length")" "$run_hash"
}
safe_run="$(bounded_run_fragment 40)"
setup_out="$out_dir/fixture-setup-primary"
foreign_setup_out="$out_dir/fixture-setup-foreign"
db="$isolated_env/artifacts/test-data/${cloud_slug}-test-data.sqlite"
foreign_db="$isolated_env/artifacts/test-data/${foreign_cloud_slug}-test-data.sqlite"

brand_slug() {
  local value="$1"
  value="$(printf '%s' "$value" | tr '[:upper:]' '[:lower:]')"
  value="$(printf '%s' "$value" | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//')"
  if [[ -z "$value" ]]; then
    value="brand"
  fi
  printf '%s' "$value"
}

summary_test_data_db() {
  local summary="$1" fallback="$2" candidate parent expected_parent
  if [[ ! -f "$summary" ]]; then
    printf '%s' "$fallback"
    return
  fi
  candidate="$(jq -er '.test_data_db' "$summary")"
  if [[ ! -f "$candidate" ]]; then
    echo "fixture setup summary references a missing test-data DB" >&2
    return 1
  fi
  parent="$(cd "$(dirname "$candidate")" && pwd -P)"
  expected_parent="$(cd "$isolated_env/artifacts/test-data" && pwd -P)"
  if [[ "$parent" != "$expected_parent" ]]; then
    echo "fixture setup summary references a test-data DB outside the isolated run" >&2
    return 1
  fi
  printf '%s/%s' "$parent" "$(basename "$candidate")"
}

sqlite_json_array() {
  local database="$1" query="$2" result
  result="$($sqlite -json "$database" "$query" 2>/dev/null || true)"
  if [[ -z "$result" ]] || ! jq -e 'type == "array"' <<<"$result" >/dev/null 2>&1; then
    result='[]'
  fi
  printf '%s' "$result"
}

write_recovery_bundle() {
  local stage="$1" setup_rc="$2"
  local recovery_user_json='[]' recovery_binding_json='[]'
  local foreign_recovery_user_json='[]' foreign_recovery_binding_json='[]'
  if [[ -f "$db" ]]; then
    recovery_user_json="$(sqlite_json_array "$db" "select email, brand_cloud_id, tenant_slug, body_json from users where tenant_slug = '$cloud_slug' order by email limit 1")"
    recovery_binding_json="$(sqlite_json_array "$db" "select device_id, account_device_id from device_bindings where tenant_slug = '$cloud_slug' order by assignment_index limit 1")"
  fi
  if [[ -f "$foreign_db" ]]; then
    foreign_recovery_user_json="$(sqlite_json_array "$foreign_db" "select email, brand_cloud_id, tenant_slug, body_json from users where tenant_slug = '$foreign_cloud_slug' order by email limit 1")"
    foreign_recovery_binding_json="$(sqlite_json_array "$foreign_db" "select device_id, account_device_id from device_bindings where tenant_slug = '$foreign_cloud_slug' order by assignment_index limit 1")"
  fi
  mkdir -p "$(dirname "$bundle")"
  jq -n \
    --arg run_id "$run_id" \
    --arg platform "$platform" \
    --arg sdk_commit "${CLOUD_VALIDATION_SDK_COMMIT:-unknown}" \
    --arg cloud_slug "$cloud_slug" \
    --arg foreign_cloud_slug "$foreign_cloud_slug" \
    --arg env_root "$isolated_env" \
    --arg db "$db" \
    --arg foreign_db "$foreign_db" \
    --arg ca "$ca_bundle" \
    --arg base_url "$device_url" \
    --arg secret_root "$secret_root" \
    --arg stage "$stage" \
    --argjson user "$recovery_user_json" \
    --argjson binding "$recovery_binding_json" \
    --argjson foreign_user "$foreign_recovery_user_json" \
    --argjson foreign_binding "$foreign_recovery_binding_json" \
    --argjson setup_rc "$setup_rc" '
    def fixture_resources($users; $bindings; $role):
      ($users[0] // {}) as $u |
      ($bindings[0] // {}) as $b |
      ($u.body_json // "{}" | fromjson? // {}) as $body |
      (if (($body.brand_cloud_user_id // "") | length) > 0 then [
        {kind:"brand_cloud_user", id:$body.brand_cloud_user_id, email:($u.email // ""), brand_cloud_id:($u.brand_cloud_id // ""), fixture_role:$role},
        {kind:"app_certificate", owner_id:$body.brand_cloud_user_id, brand_cloud_id:($u.brand_cloud_id // ""), fixture_role:$role}
      ] else [] end) +
      (if (($b.account_device_id // "") | length) > 0 then [
        {kind:"device_binding", id:$b.account_device_id, device_id:($b.device_id // ""), brand_cloud_id:($u.brand_cloud_id // ""), fixture_role:$role}
      ] else [] end);
    ($binding[0] // {}) as $b |
    ($foreign_binding[0] // {}) as $fb |
    {
      schema_version: 1,
      run_id: $run_id,
      platform: $platform,
      sdk_commit: $sdk_commit,
      server_version: "unknown",
      brand_cloud_slug: $cloud_slug,
      foreign_brand_cloud_slug: $foreign_cloud_slug,
      brand_cloud_active: false,
      brandname: $cloud_slug,
      environment_root: $env_root,
      test_data_db: $db,
      foreign_test_data_db: $foreign_db,
      ca_bundle: $ca,
      app: {base_url: $base_url, device_id: ($b.device_id // ""), foreign_device_id: ($fb.device_id // "")},
      local_temporary_files: [],
      resources: (fixture_resources($user; $binding; "primary") +
        fixture_resources($foreign_user; $foreign_binding; "foreign") +
        [{kind:"local_fixture_root", path:$secret_root}]),
      setup_failure: {stage:$stage, exit_code:$setup_rc}
    }' > "$bundle"
  chmod 600 "$bundle"
}

# Persist local cleanup state before the first remote call. Later calls enrich
# the same bundle with every successfully created remote resource.
write_recovery_bundle "fixture_setup_started" 1

# Dedicated SDK E2E clouds are intentionally long lived. Fixture setup must not
# silently create a typoed cloud, because that would hide configuration drift.
cloud_list="$secret_root/brand-clouds.json"
curl --fail --silent --show-error --max-time 15 \
  --header "@$account_headers" \
  "${account_url%/}/v1/admin/brand-clouds?page_size=200" > "$cloud_list"
chmod 600 "$cloud_list"
for required_slug in "$cloud_slug" "$foreign_cloud_slug"; do
  if ! jq -e --arg slug "$required_slug" '
    any((.brand_clouds // .items // [])[]; .tenant_slug == $slug and .status == "active")
  ' "$cloud_list" >/dev/null; then
    echo "dedicated Brand Cloud $required_slug is missing or inactive" >&2
    exit 1
  fi
done
cloud_name="$(jq -er --arg slug "$cloud_slug" 'first((.brand_clouds // .items // [])[] | select(.tenant_slug == $slug)) | .name' "$cloud_list")"
foreign_cloud_name="$(jq -er --arg slug "$foreign_cloud_slug" 'first((.brand_clouds // .items // [])[] | select(.tenant_slug == $slug)) | .name' "$cloud_list")"
# The shared data setup keys its SQLite filename from the Brand Cloud display
# name, while rows inside the DB retain the tenant slug. These values can differ
# because Account Manager adds a uniqueness suffix to tenant slugs.
db="$isolated_env/artifacts/test-data/$(brand_slug "$cloud_name")-test-data.sqlite"
foreign_db="$isolated_env/artifacts/test-data/$(brand_slug "$foreign_cloud_name")-test-data.sqlite"
write_recovery_bundle "clouds_validated" 1

run_setup() {
  local name="$1" role="$2" output="$3"
  local device_prefix_base="sdk-${platform}-${role}-"
  # OpenBao validates the common name as one IDNA label (63-byte maximum).
  # Reserve five characters for the generated -0001 suffix.
  local max_device_prefix_length=58
  local run_fragment_length=$((max_device_prefix_length - ${#device_prefix_base}))
  if (( run_fragment_length < 1 )); then
    echo "SDK fixture device prefix leaves no room for a run identifier" >&2
    return 2
  fi
  local device_run_fragment
  device_run_fragment="$(bounded_run_fragment "$run_fragment_length")"
  local device_prefix="${device_prefix_base}${device_run_fragment}"
  ACCOUNT_MANAGER_BASE_URL="$account_url" \
    RTK_CLOUD_APP_CERT_KEY_ALGORITHM=p256 \
    CLOUD_LOAD_DEVICES_FACTORY_ENROLL_RETRIES="${CLOUD_VALIDATION_FACTORY_ENROLL_RETRIES:-3}" \
    CLOUD_LOAD_DEVICES_FACTORY_ENROLL_RETRY_DELAY="${CLOUD_VALIDATION_FACTORY_ENROLL_RETRY_DELAY:-1s}" \
    "$workspace/scripts/setup-staging-e2e-data.sh" \
    --workspace "$workspace" \
    --env-root "$isolated_env" \
    --brandname "$name" \
    --user-count 1 \
    --user-email-prefix "sdk-${platform}-${role}-${safe_run}" \
    --device-count 1 \
    --device-mix light=1 \
    --device-prefix "$device_prefix" \
    --user-concurrency 1 \
    --device-concurrency 1 \
    --bind-concurrency 1 \
    --out-dir "$output" \
    --from-step create_users \
    --no-resume
}

set +e
run_setup "$cloud_name" primary "$setup_out"
setup_rc=$?
if (( setup_rc == 0 )); then
  # Persist the primary resources before starting the foreign fixture. A
  # failure in the second setup must not orphan the first user or device.
  write_recovery_bundle "primary_fixture_created" 1
  run_setup "$foreign_cloud_name" foreign "$foreign_setup_out"
  setup_rc=$?
fi
set -e

if (( setup_rc != 0 )); then
  # The setup command persists each successfully created resource in SQLite.
  # Build a partial bundle before returning the original failure so the outer
  # runner can still perform deterministic, replay-safe cleanup.
  write_recovery_bundle "staging_e2e_data" "$setup_rc"
  exit "$setup_rc"
fi

db="$(summary_test_data_db "$setup_out/summary.json" "$db")"
foreign_db="$(summary_test_data_db "$foreign_setup_out/summary.json" "$foreign_db")"

if [[ ! -f "$db" || ! -f "$foreign_db" ]]; then
  echo "fixture setup did not create both expected SQLite test-data DBs" >&2
  exit 1
fi
chmod 600 "$db" "$foreign_db"
write_recovery_bundle "post_setup_validation" 1

user_json="$($sqlite -json "$db" "select email, password, brand_cloud_id, tenant_slug, app_credentials_json, app_certificate_json, body_json from users where tenant_slug = '$cloud_slug' order by email limit 1")"
binding_json="$($sqlite -json "$db" "select device_id, account_device_id, assigned_email from device_bindings where tenant_slug = '$cloud_slug' order by assignment_index limit 1")"
foreign_user_json="$($sqlite -json "$foreign_db" "select email, brand_cloud_id, tenant_slug, body_json from users where tenant_slug = '$foreign_cloud_slug' order by email limit 1")"
foreign_binding_json="$($sqlite -json "$foreign_db" "select device_id, account_device_id, assigned_email from device_bindings where tenant_slug = '$foreign_cloud_slug' order by assignment_index limit 1")"
if ! jq -e 'length == 1' <<<"$user_json" >/dev/null || ! jq -e 'length == 1' <<<"$binding_json" >/dev/null || \
   ! jq -e 'length == 1' <<<"$foreign_user_json" >/dev/null || ! jq -e 'length == 1' <<<"$foreign_binding_json" >/dev/null; then
  echo "fixture DBs are missing a run-scoped user or binding" >&2
  exit 1
fi

email="$(jq -er '.[0].email' <<<"$user_json")"
password="$(jq -er '.[0].password' <<<"$user_json")"
brand_cloud_id="$(jq -er '.[0].brand_cloud_id' <<<"$user_json")"
tenant_slug="$(jq -er '.[0].tenant_slug' <<<"$user_json")"
brand_cloud_user_id="$(jq -er '.[0].body_json | fromjson | .brand_cloud_user_id' <<<"$user_json")"
device_id="$(jq -er '.[0].device_id' <<<"$binding_json")"
account_device_id="$(jq -er '.[0].account_device_id' <<<"$binding_json")"
foreign_brand_cloud_id="$(jq -er '.[0].brand_cloud_id' <<<"$foreign_user_json")"
foreign_brand_cloud_user_id="$(jq -er '.[0].body_json | fromjson | .brand_cloud_user_id' <<<"$foreign_user_json")"
foreign_email="$(jq -er '.[0].email' <<<"$foreign_user_json")"
foreign_device_id="$(jq -er '.[0].device_id' <<<"$foreign_binding_json")"
foreign_account_device_id="$(jq -er '.[0].account_device_id' <<<"$foreign_binding_json")"
server_version="$(jq -r '.version // "unknown"' "$out_dir/server-version.json" 2>/dev/null || printf 'unknown')"

if ! jq -e --arg id "$brand_cloud_id" --arg slug "$tenant_slug" --arg foreign_id "$foreign_brand_cloud_id" --arg foreign_slug "$foreign_cloud_slug" '
  any((.brand_clouds // .items // [])[]; .id == $id and .tenant_slug == $slug and .status == "active") and
  any((.brand_clouds // .items // [])[]; .id == $foreign_id and .tenant_slug == $foreign_slug and .status == "active")
' "$cloud_list" >/dev/null; then
  echo "dedicated Brand Clouds are missing, inactive, or do not match fixture data" >&2
  exit 1
fi

revoked_app_key="$secret_root/revoked-app-private-key.pem"
revoked_app_cert="$secret_root/revoked-app-certificate-chain.pem"
revoked_app_cert_raw="$secret_root/revoked-app-certificate-chain.raw.pem"
revoked_app_identity="$secret_root/revoked-app-identity.p12"
revoked_app_identity_password_file="$secret_root/revoked-app-identity-password.txt"
jq -er '.[0].app_credentials_json | fromjson | .private_key_pem' <<<"$user_json" > "$revoked_app_key"
jq -er '.[0].app_certificate_json | fromjson |
  if ((.certificate_chain_pem // "") | length) > 0 then .certificate_chain_pem else .certificate_pem end' \
  <<<"$user_json" > "$revoked_app_cert_raw"
sed 's/-----END CERTIFICATE----------BEGIN CERTIFICATE-----/-----END CERTIFICATE-----\
-----BEGIN CERTIFICATE-----/g' "$revoked_app_cert_raw" > "$revoked_app_cert"
rm -f -- "$revoked_app_cert_raw"
revoked_app_identity_password="$(LC_ALL=C od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
printf '%s' "$revoked_app_identity_password" > "$revoked_app_identity_password_file"
chmod 600 "$revoked_app_identity_password_file"
openssl pkcs12 -export -out "$revoked_app_identity" -inkey "$revoked_app_key" -in "$revoked_app_cert" -passout "file:$revoked_app_identity_password_file"
rm -f -- "$revoked_app_identity_password_file"

# Rotate the fixture identity before platform execution. The mobile scenario
# must prove that this preserved old identity is rejected while the newly
# issued identity can obtain a token and perform an authorized device read.
curl --fail --silent --show-error --max-time 15 --request POST \
  --header "@$account_headers" --header 'Content-Type: application/json' --data '{}' \
  "${account_url%/}/v1/admin/brand-clouds/$brand_cloud_id/users/$brand_cloud_user_id/app-certificate/revoke" >/dev/null

app_key="$secret_root/app-private-key.pem"
app_csr="$secret_root/app-certificate.csr.pem"
app_login_request="$secret_root/app-certificate-login-request.json"
app_login_response="$secret_root/app-certificate-login-response.json"
app_cert="$secret_root/app-certificate-chain.pem"
app_cert_raw="$secret_root/app-certificate-chain.raw.pem"
app_identity="$secret_root/app-identity.p12"
app_identity_password_file="$secret_root/app-identity-password.txt"
openssl ecparam -name prime256v1 -genkey -noout -out "$app_key"
openssl req -new -key "$app_key" -subj "/CN=app-brand-cloud-user:$brand_cloud_user_id" -out "$app_csr"
jq -n --arg email "$email" --arg password "$password" --rawfile csr "$app_csr" \
  '{email:$email,password:$password,app_csr_pem:$csr}' > "$app_login_request"
curl --fail --silent --show-error --max-time 15 \
  --header 'Content-Type: application/json' --data-binary "@$app_login_request" \
  "${account_url%/}/v1/brand-clouds/$tenant_slug/auth/login" > "$app_login_response"
jq -e '.app_certificate.status == "issued" and
  (((.app_certificate.certificate_chain_pem // .app_certificate.certificate_pem // "") | length) > 0)' \
  "$app_login_response" >/dev/null
jq -r '.app_certificate |
  if ((.certificate_chain_pem // "") | length) > 0 then .certificate_chain_pem else .certificate_pem end' \
  "$app_login_response" > "$app_cert_raw"
sed 's/-----END CERTIFICATE----------BEGIN CERTIFICATE-----/-----END CERTIFICATE-----\
-----BEGIN CERTIFICATE-----/g' "$app_cert_raw" > "$app_cert"
rm -f -- "$app_cert_raw" "$app_login_request" "$app_login_response" "$app_csr"
app_identity_password="$(LC_ALL=C od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
printf '%s' "$app_identity_password" > "$app_identity_password_file"
chmod 600 "$app_identity_password_file"
openssl pkcs12 -export -out "$app_identity" -inkey "$app_key" -in "$app_cert" -passout "file:$app_identity_password_file"
rm -f -- "$app_identity_password_file"
chmod 600 "$revoked_app_key" "$revoked_app_cert" "$revoked_app_identity" \
  "$app_key" "$app_cert" "$app_identity" "$cloud_list"

# The SDK WebSocket session is a device-transport API and must use a
# device-scoped token. Issue it from the run-scoped virtual-device certificate;
# only the short-lived token is passed to the mobile test process.
device_credential_json="$($sqlite -json "$db" "select cert_pem, key_pem, chain_pem from device_credentials where device_id = '$device_id' limit 1")"
if ! jq -e 'length == 1 and (.[0].cert_pem | length > 0) and (.[0].key_pem | length > 0)' <<<"$device_credential_json" >/dev/null; then
  echo "run-scoped device credential is missing from fixture data" >&2
  exit 1
fi
device_cert_raw="$secret_root/device-certificate-chain.raw.pem"
device_cert="$secret_root/device-certificate-chain.pem"
device_key="$secret_root/device-private-key.pem"
jq -r '.[0].cert_pem, (.[0].chain_pem // empty)' <<<"$device_credential_json" > "$device_cert_raw"
sed 's/-----END CERTIFICATE----------BEGIN CERTIFICATE-----/-----END CERTIFICATE-----\
-----BEGIN CERTIFICATE-----/g' "$device_cert_raw" > "$device_cert"
jq -er '.[0].key_pem' <<<"$device_credential_json" > "$device_key"
rm -f -- "$device_cert_raw"
chmod 600 "$device_cert" "$device_key"
device_token_request="$secret_root/device-transport-token-request.json"
device_token_response="$secret_root/device-transport-token-response.json"
jq -n '{scope:"device", service:"mqtt"}' > "$device_token_request"
token_helper_args=(
  --url "${device_url%/}/request_token"
  --cert "$device_cert"
  --key "$device_key"
  --request "$device_token_request"
  --output "$device_token_response"
  --timeout 15s)
if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
  token_helper_args+=(--ca "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
fi
if [[ -n "${CLOUD_VALIDATION_DEVICE_TOKEN_HELPER:-}" ]]; then
  "${CLOUD_VALIDATION_DEVICE_TOKEN_HELPER}" "${token_helper_args[@]}"
else
  (cd "$workspace/e2e_test" && GOWORK=off go run ./cloud_validation/cmd/request-token "${token_helper_args[@]}")
fi
chmod 600 "$device_token_request" "$device_token_response"
jq -e '.access_token | type == "string" and length > 0' "$device_token_response" >/dev/null

token_response="$secret_root/app-token-response.json"
token_request="$secret_root/app-token-request.json"
jq -n --arg devid "$device_id" '{scope:"app", service:"mqtt", devid:$devid}' > "$token_request"
app_token_curl=(curl --silent --show-error --max-time 15 --cert "$app_cert" --key "$app_key")
if [[ -n "${CLOUD_VALIDATION_SERVER_CA_BUNDLE:-}" ]]; then
  app_token_curl+=(--cacert "$CLOUD_VALIDATION_SERVER_CA_BUNDLE")
fi
app_token_ready=0
app_token_status="000"
app_token_attempts="${CLOUD_VALIDATION_APP_CERT_READY_ATTEMPTS:-30}"
for ((attempt = 1; attempt <= app_token_attempts; attempt++)); do
  set +e
  app_token_status="$("${app_token_curl[@]}" --output "$token_response" --write-out '%{http_code}' \
    -H 'Content-Type: application/json' --data-binary "@$token_request" \
    "${device_url%/}/request_token")"
  app_token_rc=$?
  set -e
  if (( app_token_rc == 0 )) && [[ "$app_token_status" == "200" ]] && \
    jq -e '.access_token | type == "string" and length > 0' "$token_response" >/dev/null 2>&1; then
    app_token_ready=1
    break
  fi
  case "$app_token_status" in
    000|401|403|404|409|425|429|5??) ;;
    *) echo "rotated app certificate readiness failed with HTTP $app_token_status" >&2; exit 1 ;;
  esac
  if (( attempt < app_token_attempts )); then
    sleep "${CLOUD_VALIDATION_APP_CERT_READY_DELAY_SECONDS:-1}"
  fi
done
if [[ "$app_token_ready" != "1" ]]; then
  echo "rotated app certificate did not become ready after $app_token_attempts attempts (last HTTP $app_token_status)" >&2
  exit 1
fi
chmod 600 "$token_response"
app_headers="$secret_root/app-headers.txt"
printf 'Authorization: Bearer %s\n' "$(jq -er '.access_token' "$token_response")" > "$app_headers"
chmod 600 "$app_headers"

mkdir -p "$(dirname "$bundle")"
jq -n \
  --arg run_id "$run_id" \
  --arg platform "$platform" \
  --arg validation_profile "${CLOUD_VALIDATION_PROFILE:-deploy}" \
  --arg sdk_commit "${CLOUD_VALIDATION_SDK_COMMIT:-unknown}" \
  --arg server_version "$server_version" \
  --arg cloud_slug "$cloud_slug" \
  --arg brandname "$cloud_slug" \
  --arg env_root "$isolated_env" \
  --arg db "$db" \
  --arg foreign_db "$foreign_db" \
  --arg ca "$ca_bundle" \
  --arg base_url "$device_url" \
  --arg device_id "$device_id" \
  --arg foreign_device_id "$foreign_device_id" \
  --arg access_token "$(jq -er '.access_token' "$token_response")" \
  --arg device_transport_access_token "$(jq -er '.access_token' "$device_token_response")" \
  --arg refresh_token "$(jq -r 'if ((.refresh_token // "") | length) > 0 then .refresh_token else (.access_token // "") end' "$token_response")" \
  --arg cert_path "$app_cert" \
  --arg key_path "$app_key" \
  --arg pkcs12_path "$app_identity" \
  --arg pkcs12_password "$app_identity_password" \
  --arg revoked_pkcs12_path "$revoked_app_identity" \
  --arg revoked_pkcs12_password "$revoked_app_identity_password" \
  --arg user_id "$brand_cloud_user_id" \
  --arg email "$email" \
  --arg cloud_id "$brand_cloud_id" \
  --arg account_device_id "$account_device_id" \
  --arg foreign_user_id "$foreign_brand_cloud_user_id" \
  --arg foreign_email "$foreign_email" \
  --arg foreign_cloud_id "$foreign_brand_cloud_id" \
  --arg foreign_account_device_id "$foreign_account_device_id" \
  --arg secret_root "$secret_root" \
  --arg token_file "$token_response" \
  --arg token_request_file "$token_request" \
  --arg cloud_file "$cloud_list" '
  {
    schema_version: 1,
    run_id: $run_id,
    platform: $platform,
    validation_profile: $validation_profile,
    sdk_commit: $sdk_commit,
    server_version: $server_version,
    brand_cloud_slug: $cloud_slug,
    brand_cloud_active: true,
    brandname: $brandname,
    environment_root: $env_root,
    test_data_db: $db,
    foreign_test_data_db: $foreign_db,
    ca_bundle: $ca,
    max_devices: 1,
    duration_seconds: 900,
    app: {
      base_url: $base_url,
      device_id: $device_id,
      access_token: $access_token,
      device_transport_access_token: $device_transport_access_token,
      refresh_token: $refresh_token,
      expired_access_token: "expired-sdk-cloud-validation-token",
      foreign_device_id: $foreign_device_id,
      certificate_path: $cert_path,
      private_key_path: $key_path,
      pkcs12_path: $pkcs12_path,
      pkcs12_password: $pkcs12_password,
      revoked_pkcs12_path: $revoked_pkcs12_path,
      revoked_pkcs12_password: $revoked_pkcs12_password
    },
    local_temporary_files: [$token_file, $token_request_file, $cloud_file],
    resources: [
      {kind:"brand_cloud_user", id:$user_id, email:$email, brand_cloud_id:$cloud_id, fixture_role:"primary"},
      {kind:"device_binding", id:$account_device_id, device_id:$device_id, brand_cloud_id:$cloud_id, fixture_role:"primary"},
      {kind:"app_certificate", owner_id:$user_id, brand_cloud_id:$cloud_id, fixture_role:"primary"},
      {kind:"brand_cloud_user", id:$foreign_user_id, email:$foreign_email, brand_cloud_id:$foreign_cloud_id, fixture_role:"foreign"},
      {kind:"device_binding", id:$foreign_account_device_id, device_id:$foreign_device_id, brand_cloud_id:$foreign_cloud_id, fixture_role:"foreign"},
      {kind:"app_certificate", owner_id:$foreign_user_id, brand_cloud_id:$foreign_cloud_id, fixture_role:"foreign"},
      {kind:"local_fixture_root", path:$secret_root}
    ]
  }' > "$bundle"
chmod 600 "$bundle"
