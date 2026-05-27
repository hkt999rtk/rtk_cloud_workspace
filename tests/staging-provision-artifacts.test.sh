#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
SECRETS="$WORKSPACE/.secrets/staging/linode"
FAKE_BIN="$TMP/bin"
ERR="$TMP/staging-provision-artifacts.err"
mkdir -p \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$SECRETS/video-cloud/env" \
	"$SECRETS/video-cloud/artifacts"

cat > "$FAKE_BIN/dig" <<'SH'
#!/usr/bin/env bash
if [[ "$*" == *" NS "* || "$*" == NS\ * ]]; then
	printf 'ns1.example.test.\n'
else
	printf '203.0.113.5\n'
fi
SH
chmod +x "$FAKE_BIN/dig"

cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"root@10.42.1.10"* ]]; then
	count=0
	if [[ -f "$SSH_API_COUNT_FILE" ]]; then
		count="$(cat "$SSH_API_COUNT_FILE")"
	fi
	count=$((count + 1))
	printf '%s\n' "$count" > "$SSH_API_COUNT_FILE"
	if (( count == 1 )); then
		exit 1
	fi
fi
exit 0
SH
chmod +x "$FAKE_BIN/ssh"

cat > "$FAKE_BIN/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$FAKE_BIN/sleep"

cat > "$FAKE_BIN/ssh-keyscan" <<'SH'
#!/usr/bin/env bash
host="${@: -1}"
printf '%s ssh-ed25519 AAAATESTKEY\n' "$host"
SH
chmod +x "$FAKE_BIN/ssh-keyscan"

cat > "$SECRETS/video-cloud/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
EOF_ENV

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json" <<'EOF_STATE'
{
  "stack": "video-cloud-staging",
  "region": "us-sea",
  "vpc_id": 9001,
  "subnet_id": 9002,
  "firewalls": {
    "edge": 101,
    "api": 102,
    "infra": 103,
    "mqtt": 104,
    "coturn": 105
  },
  "instances": {
    "edge": {"id": 1, "role": "edge", "label": "video-cloud-staging-edge", "public_ipv4": "203.0.113.5", "private_ip": "10.42.1.5"},
    "api": {"id": 2, "role": "api", "label": "video-cloud-staging-api", "public_ipv4": "", "private_ip": "10.42.1.10"},
    "infra": {"id": 3, "role": "infra", "label": "video-cloud-staging-infra", "public_ipv4": "", "private_ip": "10.42.1.30"},
    "mqtt": {"id": 4, "role": "mqtt", "label": "video-cloud-staging-mqtt", "public_ipv4": "203.0.113.40", "private_ip": "10.42.1.40"},
    "coturn": {"id": 5, "role": "coturn", "label": "video-cloud-staging-coturn", "public_ipv4": "203.0.113.50", "private_ip": ""}
  }
}
EOF_STATE

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_ID=6
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.60
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.60
ACCOUNT_MANAGER_LINODE_FIREWALL_ID=106
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
EOF_AM

cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state" <<'EOF_ADMIN'
ADMIN_LINODE_ID=7
ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.70
ADMIN_LINODE_HOST=203.0.113.70
ADMIN_LINODE_FIREWALL_ID=107
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
EOF_ADMIN

OUT="$TMP/staging-provision-artifacts.out"
PATH="$FAKE_BIN:$PATH" SSH_API_COUNT_FILE="$TMP/ssh-api.count" "$ROOT/scripts/staging-provision.sh" \
	--workspace "$WORKSPACE" \
	--secrets-root "$SECRETS" \
	--ssh-key "$TMP/id_ed25519" \
	--artifacts >"$OUT" 2>"$ERR"

ARTIFACT_DIR="$(tail -n 1 "$OUT")"
REPORT="$ARTIFACT_DIR/provision-report.md"

grep -F '## VM Configuration' "$REPORT" >/dev/null
grep -F '| `api` | `video-cloud-staging-api` | `2` | `102` | `private` | `N/A` | `10.42.1.10` | `VPC via edge ProxyJump` | `root@203.0.113.5` |' "$REPORT" >/dev/null
grep -F '| `edge` | `video-cloud-staging-edge` | `1` | `101` | `public+vpc` | `203.0.113.5` | `10.42.1.5` | `direct public SSH` | `N/A` |' "$REPORT" >/dev/null
grep -F '| `account-manager` | `rtk-account-manager-staging` | `6` | `106` | `public` | `203.0.113.60` | `N/A` | `direct public SSH` | `N/A` |' "$REPORT" >/dev/null
grep -F '| `cloud-admin` | `rtk-cloud-admin-staging` | `7` | `107` | `public` | `203.0.113.70` | `N/A` | `direct public SSH` | `N/A` |' "$REPORT" >/dev/null
grep -F 'VPN: not configured by this script; private service access uses edge SSH ProxyJump over the Linode VPC.' "$REPORT" >/dev/null
grep -F 'SSH readiness attempt 1/30: role=api host=10.42.1.10 route=proxy_jump via root@203.0.113.5' "$ERR" >/dev/null
grep -F 'SSH readiness pending: role=api host=10.42.1.10 attempt=1/30; retrying in 10s' "$ERR" >/dev/null
grep -F 'SSH readiness ok: role=api host=10.42.1.10 attempt=2/30' "$ERR" >/dev/null
