#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
SECRETS="$WORKSPACE/.secrets/staging/linode"
SSH_KEY="$TMP/id_ed25519_rtkcloud"
mkdir -p \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$SECRETS/video-cloud/env" \
	"$SECRETS/video-cloud/artifacts"

cat > "$SECRETS/video-cloud/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
EOF_ENV

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json" <<'EOF_STATE'
{"stack":"video-cloud-staging","instances":{"edge":{"public_ipv4":"203.0.113.5"}}}
EOF_STATE

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.60
EOF_AM

cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.70
EOF_ADMIN

touch "$SSH_KEY" "$SSH_KEY.pub"

cat > "$TMP/mock-staging-deploy-fails.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "mock deploy failed" >&2
exit 42
SH
chmod +x "$TMP/mock-staging-deploy-fails.sh"

OUT="$TMP/out.txt"
if STAGING_DEPLOY_SCRIPT="$TMP/mock-staging-deploy-fails.sh" "$ROOT/scripts/staging-provision.sh" \
	--workspace "$WORKSPACE" \
	--secrets-root "$SECRETS" \
	--ssh-key "$SSH_KEY" \
	--video-release video-test \
	--account-release account-test \
	--admin-release admin-test \
	--deploy >"$OUT" 2>&1; then
	echo "staging-provision unexpectedly passed" >&2
	exit 1
fi

grep -F 'mock deploy failed' "$OUT" >/dev/null
grep -F 'deploy failed; artifacts and e2e were not run' "$OUT" >/dev/null
if grep -F '[staging-provision] deploy complete' "$OUT" >/dev/null; then
	echo "deploy complete was logged after failure" >&2
	exit 1
fi
