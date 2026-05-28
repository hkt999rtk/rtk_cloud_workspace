#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
FAKE_BIN="$TMP/bin"
LOG="$TMP/curl.log"
mkdir -p \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$WORKSPACE/.secrets/staging/linode/video-cloud/config"

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json" <<'JSON'
{"firewalls":{"edge":101,"api":102,"infra":103,"mqtt":104,"coturn":105}}
JSON
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_FIREWALL_ID=201
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
EOF_STATE
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state" <<'EOF_STATE'
ADMIN_LINODE_FIREWALL_ID=301
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
EOF_STATE
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS=203.0.113.10/32
EOF_ENV
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env" <<'EOF_ENV'
ADMIN_LINODE_ALLOWED_SSH_CIDRS=203.0.113.20/32
EOF_ENV
cat > "$WORKSPACE/.secrets/staging/linode/video-cloud/config/video-cloud-staging.yaml" <<'EOF_YAML'
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
{"inbound_policy":"DROP","outbound_policy":"ACCEPT","inbound":[{"label":"ssh","action":"ACCEPT","protocol":"TCP","ports":"22","addresses":{"ipv4":["203.0.113.10/32"]}}],"outbound":[],"version":2,"fingerprint":"test"}
JSON
	;;
*"PUT https://api.linode.com/v4/networking/firewalls/"*"/rules"*)
	exit 0
	;;
*)
	printf 'unexpected curl invocation: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

PATH="$FAKE_BIN:$PATH" CURL_LOG="$LOG" LINODE_TOKEN=test-token \
	"$ROOT/scripts/staging-update-ssh-whitelist.sh" \
		--workspace "$WORKSPACE" \
		--cidr 198.51.100.9/32 >/dev/null

for id in 101 102 103 104 105 201 301; do
	grep -F -- "GET https://api.linode.com/v4/networking/firewalls/$id/rules" "$LOG" >/dev/null
	grep -F -- "PUT https://api.linode.com/v4/networking/firewalls/$id/rules" "$LOG" >/dev/null
done

grep -F '198.51.100.9/32' "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env" >/dev/null
grep -F '198.51.100.9/32' "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env" >/dev/null
grep -F '    - 198.51.100.9/32' "$WORKSPACE/.secrets/staging/linode/video-cloud/config/video-cloud-staging.yaml" >/dev/null
