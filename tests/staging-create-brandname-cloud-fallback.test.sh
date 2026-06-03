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
	"$ENV_ROOT/env" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/state"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_OPERATOR'
LINODE_TOKEN=fake-linode-token
EOF_OPERATOR

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
ACCOUNT_MANAGER_LINODE_SSH_KEY=/tmp/fake-key
ACCOUNT_MANAGER_LINODE_SSH_USER=root
EOF_ENV

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.10
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.10
ACCOUNT_MANAGER_LINODE_FIREWALL_ID=12345
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=account-fw
EOF_STATE

cat > "$ENV_ROOT/services/account-manager/account-manager-platform-admin.env" <<'EOF_ADMIN'
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=correct-horse-battery-staple
EOF_ADMIN

cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
stdin="$(cat)"
if [[ "$*" == *"bash -s"* && "$stdin" == *"sudo -u postgres psql"* ]]; then
	cat <<'JSON'
{"action":"created","brand_cloud":{"id":"org-rtk","name":"RTK","organization_kind":"brand_cloud","status":"active","tier":"commercial","evaluation_device_quota":5,"metadata":{"brandname":"RTK"},"created_at":"2026-05-27T00:00:00Z","updated_at":"2026-05-27T00:00:00Z"}}
JSON
	exit 0
fi
printf 'bootstrap admin env applied and account-manager is healthy\n' >&2
SH
chmod +x "$FAKE_BIN/ssh"

LIST_COUNT="$TMP/list.count"
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
https://api.ipify.org)
	printf '198.51.100.20'
	exit 0
	;;
https://api.linode.com/v4/networking/firewalls/12345/rules)
	printf '{"inbound":[{"label":"ssh","action":"ACCEPT","protocol":"TCP","ports":"22","addresses":{"ipv4":["198.51.100.20/32"]}}],"outbound":[]}'
	exit 0
	;;
*/v1/auth/login)
	printf '{"tokens":{"access_token":"test-token"}}' >"$out"
	status=200
	;;
*/v1/admin/brand-clouds\?limit=200)
	count_file="${LIST_COUNT:?}"
	count=0
	[[ -f "$count_file" ]] && count="$(cat "$count_file")"
	count=$((count + 1))
	printf '%s' "$count" >"$count_file"
	if (( count == 1 )); then
		printf '{"brand_clouds":[],"pagination":{"limit":200,"offset":0,"total":0}}' >"$out"
	else
		printf '{"brand_clouds":[{"id":"org-rtk","name":"RTK","organization_kind":"brand_cloud","status":"active","tier":"commercial","evaluation_device_quota":5,"metadata":{"brandname":"RTK"},"created_at":"2026-05-27T00:00:00Z","updated_at":"2026-05-27T00:00:00Z"}],"pagination":{"limit":200,"offset":0,"total":1}}' >"$out"
	fi
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

OUT="$TMP/out.json"
PATH="$FAKE_BIN:$PATH" LIST_COUNT="$LIST_COUNT" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-brandname-cloud \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK >"$OUT"

jq -e '.action == "created"' "$OUT" >/dev/null
jq -e '.brand_cloud.id == "org-rtk"' "$OUT" >/dev/null
jq -e '.brand_cloud.metadata.brandname == "RTK"' "$OUT" >/dev/null
