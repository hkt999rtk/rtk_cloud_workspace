#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/runtime"
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
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-staging.example.com
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

PORT=$((20000 + RANDOM % 20000))
python3 -c '
import http.server, json, sys
class Handler(http.server.BaseHTTPRequestHandler):
    def respond(self, status, body):
        raw = json.dumps(body).encode()
        self.send_response(status); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw))); self.end_headers(); self.wfile.write(raw)
    def log_message(self, *_): pass
    def do_POST(self):
        if self.path == "/v1/auth/login": return self.respond(200, {"tokens":{"access_token":"test-token"}})
        if self.path == "/v1/admin/brand-clouds": return self.respond(201, {"brand_cloud":{"id":"org-rtk","name":"RTK","organization_kind":"brand_cloud","status":"active","tier":"commercial","evaluation_device_quota":5,"metadata":{"brandname":"RTK"}}})
        self.respond(404, {})
    def do_GET(self):
        if self.path == "/v1/admin/brand-clouds?limit=200": return self.respond(200, {"brand_clouds":[],"pagination":{"limit":200,"offset":0,"total":0}})
        self.respond(404, {})
http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Handler).serve_forever()
' "$PORT" &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true; rm -rf "$TMP"' EXIT
sleep 0.2

OUT="$TMP/out.json"
if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-brandname-cloud \
	--workspace "$WORKSPACE" \
	--brandname RTK >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

ACCOUNT_MANAGER_BASE_URL="http://127.0.0.1:$PORT" PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-brandname-cloud \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK >"$OUT"

jq -e '.action == "created"' "$OUT" >/dev/null
jq -e '.brand_cloud.id == "org-rtk"' "$OUT" >/dev/null
jq -e '.brand_cloud.metadata.brandname == "RTK"' "$OUT" >/dev/null
