#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
FAKE_BIN="$TMP/bin"
mkdir -p \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/state"

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
EOF_ENV

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.10
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.10
EOF_STATE

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-platform-admin.env" <<'EOF_ADMIN'
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=correct-horse-battery-staple
EOF_ADMIN

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
out=""
write_code=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
	case "${args[$i]}" in
	-o) out="${args[$((i + 1))]}" ;;
	-w) write_code="${args[$((i + 1))]}" ;;
	esac
done
url="${args[$((${#args[@]} - 1))]}"
case "$url" in
*/v1/auth/login)
	printf '{"tokens":{"access_token":"test-token"}}' >"$out"
	status=200
	;;
*/v1/admin/brand-clouds\?limit=200)
	cat >"$out" <<'JSON'
{"brand_clouds":[
{"id":"org-rtk","name":"RTK","organization_kind":"brand_cloud","status":"active","tier":"commercial","evaluation_device_quota":5,"metadata":{"brandname":"RTK","region":"staging"},"created_at":"2026-05-27T00:00:00Z","updated_at":"2026-05-27T00:00:00Z"},
{"id":"org-demo","name":"DEMO","organization_kind":"brand_cloud","status":"active","tier":"evaluation","evaluation_device_quota":2,"metadata":{"brandname":"DEMO"},"created_at":"2026-05-28T00:00:00Z","updated_at":"2026-05-28T00:00:00Z"}
],"pagination":{"limit":200,"offset":0,"total":2}}
JSON
	status=200
	;;
*)
	printf 'unexpected curl url: %s\n' "$url" >&2
	exit 1
	;;
esac
if [[ -n "$write_code" ]]; then
	printf '%s' "${write_code//'%{http_code}'/$status}"
fi
SH
chmod +x "$FAKE_BIN/curl"

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/staging_list_brandname_clouds.sh" \
	--workspace "$WORKSPACE" >"$OUT"

grep -F 'brand_clouds=2 api_total=2' "$OUT" >/dev/null
grep -F 'org-rtk' "$OUT" >/dev/null
grep -F 'RTK' "$OUT" >/dev/null
grep -F '"region":"staging"' "$OUT" >/dev/null

JSON_OUT="$TMP/out.json"
PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/staging_list_brandname_clouds.sh" \
	--workspace "$WORKSPACE" \
	--brandname RTK \
	--json >"$JSON_OUT"

jq -e '.brand_clouds | length == 1' "$JSON_OUT" >/dev/null
jq -e '.brand_clouds[0].id == "org-rtk"' "$JSON_OUT" >/dev/null
jq -e '.brand_clouds[0].metadata.region == "staging"' "$JSON_OUT" >/dev/null
