#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
SECRETS="$WORKSPACE/.secrets/staging/linode"
FAKE_BIN="$TMP/bin"
LOG="$TMP/api.log"
SSH_KEY="$TMP/id_ed25519_rtkcloud"
VC_STATE="$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json"
VC_SECRET_STATE="$SECRETS/video-cloud/state/video-cloud-staging.state.json"
AM_STATE="$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env"

mkdir -p \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$SECRETS/video-cloud/config" \
	"$SECRETS/video-cloud/env" \
	"$SECRETS/video-cloud/state"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$API_LOG"
case "$*" in
*"https://api.ipify.org"*)
	printf '203.0.113.99\n'
	;;
*"-X GET https://api.linode.com/v4/linode/instances?page_size=500"*)
	printf '{"data":[]}\n'
	;;
*"-X GET https://api.linode.com/v4/networking/firewalls?page_size=500"*)
	cat <<'JSON'
{"data":[
  {"id":25411464,"label":"video-cloud-staging-edge","status":"enabled","tags":["managed-by:linode-deploy","role:edge","video-cloud-staging"]},
  {"id":25411467,"label":"video-cloud-staging-api","status":"enabled","tags":["managed-by:linode-deploy","role:api","video-cloud-staging"]},
  {"id":24476583,"label":"rtk-account-manager-staging-fw","status":"enabled","tags":[]},
  {"id":24476605,"label":"rtk-cloud-admin-staging-firewall","status":"enabled","tags":[]}
] }
JSON
	;;
*"-X GET https://api.linode.com/v4/vpcs?page_size=500"*)
	printf '{"data":[{"id":499050,"label":"video-cloud-staging-vpc","region":"us-sea"}]}\n'
	;;
*"-X DELETE https://api.linode.com/v4/networking/firewalls/"*)
	printf '{}\n'
	;;
*"-X DELETE https://api.linode.com/v4/vpcs/"*)
	printf '{}\n'
	;;
*"-X POST https://api.linode.com/v4/linode/instances"*)
	printf '{"id":700,"ipv4":["203.0.113.70"]}\n'
	;;
*"-X POST https://api.linode.com/v4/networking/firewalls"*)
	printf '{"id":701}\n'
	;;
*"-X POST https://api.linode.com/v4/networking/firewalls/701/devices"*)
	printf '{}\n'
	;;
*"-X GET https://api.linode.com/v4/networking/firewalls/"*"/rules"*)
	printf '{"inbound":[{"label":"ssh","addresses":{"ipv4":[]}}]}\n'
	;;
*"-X PUT https://api.linode.com/v4/networking/firewalls/"*"/rules"*)
	printf '{}\n'
	;;
*)
	printf 'unexpected curl: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

cat > "$FAKE_BIN/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ -e "$VC_STATE_PATH" || -e "$VC_SECRET_STATE_PATH" ]]; then
	printf 'stale Video Cloud state still exists before apply\n' >&2
	exit 1
fi
mkdir -p "$(dirname "$VC_STATE_PATH")"
cat > "$VC_STATE_PATH" <<'JSON'
{
  "stack": "video-cloud-staging",
  "region": "us-sea",
  "vpc_id": 9001,
  "subnet_id": 9002,
  "firewalls": {"edge": 11, "api": 12, "infra": 13, "mqtt": 14, "coturn": 15},
  "instances": {
    "edge": {"id": 1, "role": "edge", "label": "video-cloud-staging-edge", "public_ipv4": "203.0.113.5", "private_ip": "10.42.1.5"},
    "api": {"id": 2, "role": "api", "label": "video-cloud-staging-api", "private_ip": "10.42.1.10"},
    "infra": {"id": 3, "role": "infra", "label": "video-cloud-staging-infra", "private_ip": "10.42.1.30"},
    "mqtt": {"id": 4, "role": "mqtt", "label": "video-cloud-staging-mqtt", "public_ipv4": "203.0.113.40", "private_ip": "10.42.1.40"},
    "coturn": {"id": 5, "role": "coturn", "label": "video-cloud-staging-coturn", "public_ipv4": "203.0.113.50"}
  }
}
JSON
printf 'applied fake video cloud\n'
SH
chmod +x "$FAKE_BIN/go"

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/provision-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
mkdir -p "$(dirname "$ACCOUNT_MANAGER_LINODE_STATE_PATH")"
cat > "$ACCOUNT_MANAGER_LINODE_STATE_PATH" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_ID=600
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.60
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.60
ACCOUNT_MANAGER_LINODE_FIREWALL_ID=601
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
EOF_STATE
SH
chmod +x "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/provision-public-vm.sh"

touch "$SECRETS/video-cloud/config/video-cloud-staging.yaml"
touch "$SECRETS/video-cloud/env/video-cloud-staging.env"
touch "$SSH_KEY"
printf 'ssh-ed25519 test-key\n' > "$SSH_KEY.pub"

cat > "$SECRETS/video-cloud/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
EOF_ENV
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS=198.51.100.10/32
EOF_AM
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_ALLOWED_SSH_CIDRS=198.51.100.10/32
EOF_ADMIN

cat > "$VC_STATE" <<'JSON'
{"stack":"video-cloud-staging","vpc_id":499050,"subnet_id":123,"firewalls":{"edge":999999},"instances":{}}
JSON
cp "$VC_STATE" "$VC_SECRET_STATE"

PATH="$FAKE_BIN:$PATH" \
	API_LOG="$LOG" \
	VC_STATE_PATH="$VC_STATE" \
	VC_SECRET_STATE_PATH="$VC_SECRET_STATE" \
	"$ROOT/scripts/staging-provision.sh" \
	--workspace "$WORKSPACE" \
	--secrets-root "$SECRETS" \
	--ssh-key "$SSH_KEY" \
	--apply >/dev/null

grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/25411464' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/25411467' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/24476583' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/networking/firewalls/24476605' "$LOG" >/dev/null
grep -F -- '-X DELETE https://api.linode.com/v4/vpcs/499050' "$LOG" >/dev/null
grep -F 'vpc_id' "$VC_STATE" >/dev/null
test -f "$AM_STATE"
