#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
LOG="$TMP/curl.log"
PAYLOAD_LOG="$TMP/payloads.jsonl"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/topology"

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'JSON'
{"firewalls":{"edge":101,"api":102,"infra":103,"mqtt":104,"coturn":105}}
JSON
cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_FIREWALL_ID=201
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
EOF_STATE
cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_STATE'
ADMIN_LINODE_FIREWALL_ID=301
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
EOF_STATE
cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS=203.0.113.10/32
EOF_ENV
cat > "$ENV_ROOT/services/cloud-admin/admin-staging.env" <<'EOF_ENV'
ADMIN_LINODE_ALLOWED_SSH_CIDRS=203.0.113.20/32
EOF_ENV
cat > "$ENV_ROOT/topology/video-cloud-staging.yaml" <<'EOF_YAML'
ssh:
  user: root
  allowed_source_cidrs:
    - 203.0.113.30/32
instances: {}
EOF_YAML

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CURL_LOG"
case "$*" in
*"GET https://api.linode.com/v4/networking/firewalls?page_size=500"*)
	cat <<'JSON'
{"data":[
  {"id":101,"label":"video-cloud-staging-edge"},
  {"id":102,"label":"video-cloud-staging-api"},
  {"id":103,"label":"video-cloud-staging-infra"},
  {"id":104,"label":"video-cloud-staging-mqtt"},
  {"id":105,"label":"video-cloud-staging-coturn"},
  {"id":201,"label":"rtk-account-manager-staging-fw"},
  {"id":301,"label":"rtk-cloud-admin-staging-firewall"}
]}
JSON
	;;
*"GET https://api.linode.com/v4/networking/firewalls/"*"/rules"*)
	cat <<'JSON'
{"inbound_policy":"DROP","outbound_policy":"ACCEPT","inbound":[{"label":"ssh","action":"ACCEPT","protocol":"TCP","ports":"22","addresses":{"ipv4":["203.0.113.10/32","203.0.113.11/32"]}},{"label":"https","action":"ACCEPT","protocol":"TCP","ports":"443","addresses":{"ipv4":["0.0.0.0/0"]}}],"outbound":[],"version":2,"fingerprint":"test"}
JSON
	;;
*"PUT https://api.linode.com/v4/networking/firewalls/"*"/rules"*)
	payload=""
	while [[ $# -gt 0 ]]; do
		if [[ "$1" == "--data-binary" ]]; then
			payload="$2"
			break
		fi
		shift
	done
	jq -c . <<<"$payload" >> "$CURL_PAYLOAD_LOG"
	exit 0
	;;
*)
	printf 'unexpected curl invocation: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

if PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" CURL_PAYLOAD_LOG="$PAYLOAD_LOG" LINODE_TOKEN=test-token \
	"$ROOT/scripts/cloud-update-ssh-whitelist.sh" \
		--workspace "$WORKSPACE" \
		--cidr 198.51.100.9/32 >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null
: > "$LOG"

PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" CURL_PAYLOAD_LOG="$PAYLOAD_LOG" LINODE_TOKEN=test-token \
	"$ROOT/scripts/cloud-update-ssh-whitelist.sh" \
		--workspace "$WORKSPACE" \
		--env-root "$ENV_ROOT" \
		--cidr 198.51.100.9/32 >/dev/null

for id in 101 102 103 104 105 201 301; do
	grep -F -- "GET https://api.linode.com/v4/networking/firewalls/$id/rules" "$LOG" >/dev/null
	grep -F -- "PUT https://api.linode.com/v4/networking/firewalls/$id/rules" "$LOG" >/dev/null
done

grep -F '198.51.100.9/32' "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" >/dev/null
grep -F '198.51.100.9/32' "$ENV_ROOT/services/cloud-admin/admin-staging.env" >/dev/null
grep -F '    - 198.51.100.9/32' "$ENV_ROOT/topology/video-cloud-staging.yaml" >/dev/null
grep -F '203.0.113.10/32,198.51.100.9/32' "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" >/dev/null
grep -F '203.0.113.20/32,198.51.100.9/32' "$ENV_ROOT/services/cloud-admin/admin-staging.env" >/dev/null
grep -F '"203.0.113.10/32"' "$PAYLOAD_LOG" >/dev/null
grep -F '"203.0.113.11/32"' "$PAYLOAD_LOG" >/dev/null
grep -F '"198.51.100.9/32"' "$PAYLOAD_LOG" >/dev/null
grep -F '"0.0.0.0/0"' "$PAYLOAD_LOG" >/dev/null

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS=203.0.113.10/32,198.51.100.9/32
EOF_ENV
cat > "$ENV_ROOT/services/cloud-admin/admin-staging.env" <<'EOF_ENV'
ADMIN_LINODE_ALLOWED_SSH_CIDRS=203.0.113.20/32,198.51.100.9/32
EOF_ENV
cat > "$ENV_ROOT/topology/video-cloud-staging.yaml" <<'EOF_YAML'
ssh:
  user: root
  allowed_source_cidrs:
    - 203.0.113.30/32
    - 198.51.100.9/32
instances: {}
EOF_YAML
: > "$LOG"
: > "$PAYLOAD_LOG"

PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" CURL_PAYLOAD_LOG="$PAYLOAD_LOG" LINODE_TOKEN=test-token \
	"$ROOT/scripts/cloud-update-ssh-whitelist.sh" \
		--workspace "$WORKSPACE" \
		--env-root "$ENV_ROOT" \
		--mode replace \
		--cidr 198.51.100.77/32 >/dev/null

grep -Fx 'ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS=198.51.100.77/32' "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" >/dev/null
grep -Fx 'ADMIN_LINODE_ALLOWED_SSH_CIDRS=198.51.100.77/32' "$ENV_ROOT/services/cloud-admin/admin-staging.env" >/dev/null
grep -F '    - 198.51.100.77/32' "$ENV_ROOT/topology/video-cloud-staging.yaml" >/dev/null
if grep -F '203.0.113.30/32' "$ENV_ROOT/topology/video-cloud-staging.yaml" >/dev/null; then
	echo "expected replace mode to remove old video-cloud SSH CIDR" >&2
	exit 1
fi
grep -F '"198.51.100.77/32"' "$PAYLOAD_LOG" >/dev/null
if grep -F '203.0.113.10/32' "$PAYLOAD_LOG" >/dev/null; then
	echo "expected replace mode PUT payload to remove old SSH CIDR" >&2
	exit 1
fi
grep -F '"0.0.0.0/0"' "$PAYLOAD_LOG" >/dev/null
