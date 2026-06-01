#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/state"

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
ACCOUNT_MANAGER_LINODE_SSH_KEY=/tmp/fake-key
ACCOUNT_MANAGER_LINODE_SSH_USER=root
EOF_ENV

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.10
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.10
EOF_STATE

cat > "$ENV_ROOT/services/account-manager/account-manager-platform-admin.env" <<'EOF_ADMIN'
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=correct-horse-battery-staple
EOF_ADMIN

cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
cat >/dev/null
printf 'bootstrap admin env applied and account-manager is healthy\n' >&2
SH
chmod +x "$FAKE_BIN/ssh"

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
	printf '{"brand_clouds":[],"pagination":{"limit":200,"offset":0,"total":0}}' >"$out"
	status=200
	;;
*/v1/admin/brand-clouds)
	printf '{"brand_cloud":{"id":"org-rtk","name":"RTK","organization_kind":"brand_cloud","status":"active","tier":"commercial","evaluation_device_quota":5,"metadata":{"brandname":"RTK"},"created_at":"2026-05-27T00:00:00Z","updated_at":"2026-05-27T00:00:00Z"}}' >"$out"
	status=201
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

OUT="$TMP/out.json"
if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-brandname-cloud \
	--workspace "$WORKSPACE" \
	--brandname RTK >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-brandname-cloud \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK >"$OUT"

jq -e '.action == "created"' "$OUT" >/dev/null
jq -e '.brand_cloud.id == "org-rtk"' "$OUT" >/dev/null
jq -e '.brand_cloud.metadata.brandname == "RTK"' "$OUT" >/dev/null
