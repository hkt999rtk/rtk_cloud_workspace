#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
mkdir -p "$FAKE_BIN" "$ENV_ROOT/services/account-manager"

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
EOF_ENV

cat > "$ENV_ROOT/services/account-manager/account-manager-platform-admin.env" <<'EOF_ADMIN'
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=correct-horse-battery-staple
EOF_ADMIN

cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
echo "unexpected ssh fallback" >&2
exit 1
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
url=""
for arg in "${args[@]}"; do
	if [[ "$arg" == http://* || "$arg" == https://* ]]; then
		url="$arg"
		break
	fi
done
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
	printf '{"error":{"code":"internal_error","message":"Internal server error"}}' >"$out"
	status=500
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

if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-brandname-cloud \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK >"$TMP/out" 2>"$TMP/err"; then
	echo "expected create-brandname-cloud API 500 to fail without VM fallback" >&2
	exit 1
fi

grep -F 'PostgreSQL fallback is retired for K8s staging' "$TMP/err" >/dev/null
