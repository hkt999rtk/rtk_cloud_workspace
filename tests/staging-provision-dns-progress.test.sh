#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
SECRETS="$WORKSPACE/.secrets/staging/linode"
FAKE_BIN="$TMP/bin"
COUNT_FILE="$TMP/dig.count"
mkdir -p \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud/tools/godaddy-dns" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$SECRETS/video-cloud/env"

cat > "$FAKE_BIN/go" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$FAKE_BIN/go"

cat > "$FAKE_BIN/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$FAKE_BIN/sleep"

cat > "$FAKE_BIN/dig" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *" NS "* || "$*" == NS\ * ]]; then
	printf 'ns1.example.test.\n'
	exit 0
fi
count=0
if [[ -f "$DIG_COUNT_FILE" ]]; then
	count="$(cat "$DIG_COUNT_FILE")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$DIG_COUNT_FILE"
if (( count <= 2 )); then
	printf '198.51.100.10\n'
else
	printf '203.0.113.5\n'
fi
SH
chmod +x "$FAKE_BIN/dig"

cat > "$SECRETS/video-cloud/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
GODADDY_KEY=test-key
GODADDY_SECRET=test-secret
EOF_ENV

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json" <<'EOF_STATE'
{
  "instances": {
    "edge": {"public_ipv4": "203.0.113.5"}
  }
}
EOF_STATE

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.5
EOF_AM

cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.5
EOF_ADMIN

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" DIG_COUNT_FILE="$COUNT_FILE" "$ROOT/scripts/staging-provision.sh" \
	--workspace "$WORKSPACE" \
	--secrets-root "$SECRETS" \
	--dns >"$OUT" 2>&1

grep -F 'waiting DNS attempt 1/60: video-cloud-staging.realtekconnect.com expected=203.0.113.5 google=198.51.100.10 auth=198.51.100.10' "$OUT" >/dev/null
grep -F 'DNS converged: video-cloud-staging.realtekconnect.com -> 203.0.113.5' "$OUT" >/dev/null
