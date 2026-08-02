#!/usr/bin/env bash
set -euo pipefail

bundle="${CLOUD_VALIDATION_RUNTIME_BUNDLE:-}"
manifest="${CLOUD_VALIDATION_RESOURCE_MANIFEST:?CLOUD_VALIDATION_RESOURCE_MANIFEST is required}"
run_id="${CLOUD_VALIDATION_RUN_ID:?CLOUD_VALIDATION_RUN_ID is required}"
platform="${CLOUD_VALIDATION_PLATFORM:?CLOUD_VALIDATION_PLATFORM is required}"

provider_rc=0
if [[ -n "${CLOUD_VALIDATION_FIXTURE_PROVIDER_COMMAND:-}" ]]; then
  /bin/bash -lc "$CLOUD_VALIDATION_FIXTURE_PROVIDER_COMMAND" || provider_rc=$?
else
  "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/providers/setup-staging-fixture.sh" || provider_rc=$?
fi
if [[ -z "$bundle" || ! -f "$bundle" ]]; then
  if (( provider_rc != 0 )); then
    echo "fixture provider failed before creating any recoverable resource manifest" >&2
    exit "$provider_rc"
  fi
  echo "BLOCKED: runtime fixture bundle is missing after fixture setup" >&2
  exit 2
fi
bundle_mode="$(stat -f '%Lp' "$bundle" 2>/dev/null || stat -c '%a' "$bundle")"
if (( 10#$bundle_mode % 100 != 0 )); then
  echo "runtime fixture bundle must be mode 0600 or stricter" >&2
  exit 1
fi

jq -e --arg run_id "$run_id" --arg platform "$platform" '
  .schema_version == 1 and .run_id == $run_id and .platform == $platform
' "$bundle" >/dev/null

mkdir -p "$(dirname "$manifest")"
jq '{
  schema_version: 1,
  run_id: .run_id,
  platform: .platform,
  brand_cloud_slug: .brand_cloud_slug,
  brand_cloud_active: .brand_cloud_active,
  resources: (.resources // []),
  setup_failure: (.setup_failure // null),
  cleanup_required: ((.resources // []) | length > 0)
}' "$bundle" > "$manifest"
chmod 600 "$manifest"

if ! jq -e '
  [paths as $path | ($path[-1] | tostring | ascii_downcase)] |
  all(.[]; test("^(access_token|refresh_token|claim_token|password|private_key|private_key_pem|certificate_pem|certificate_chain_pem|authorization|client_secret)$") | not)
' "$manifest" >/dev/null; then
  echo "resource manifest contains secret-bearing fields" >&2
  exit 1
fi

if (( provider_rc != 0 )); then
  exit "$provider_rc"
fi

jq -e --arg run_id "$run_id" --arg platform "$platform" '
  .schema_version == 1 and .run_id == $run_id and .platform == $platform and
  (.sdk_commit | type == "string" and length > 0) and
  (.server_version | type == "string" and length > 0) and
  (.brand_cloud_slug | type == "string" and length > 0) and
  .brand_cloud_active == true and
  (.brandname | type == "string" and length > 0) and
  (.environment_root | type == "string" and length > 0) and
  (.app.base_url | type == "string" and length > 0) and
  (.app.device_id | type == "string" and length > 0) and
  (.app.access_token | type == "string" and length > 0) and
  (.app.device_transport_access_token | type == "string" and length > 0) and
  (.app.refresh_token | type == "string" and length > 0) and
  (.app.expired_access_token | type == "string" and length > 0) and
  (.app.foreign_device_id | type == "string" and length > 0) and
  (.app.foreign_device_id != .app.device_id) and
  (.app.certificate_path | type == "string" and length > 0) and
  (.app.private_key_path | type == "string" and length > 0) and
  (.app.pkcs12_path | type == "string" and length > 0) and
  (.app.pkcs12_password | type == "string" and length > 0) and
  (.app.revoked_pkcs12_path | type == "string" and length > 0) and
  (.app.revoked_pkcs12_password | type == "string" and length > 0) and
  (.test_data_db | type == "string" and length > 0)
' "$bundle" >/dev/null
