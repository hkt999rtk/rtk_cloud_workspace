#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
SSH_KEY="$TMP/id_ed25519_rtkcloud"
mkdir -p \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/artifacts" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
EOF_ENV

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
{"stack":"video-cloud-staging","instances":{"edge":{"public_ipv4":"203.0.113.5"}}}
EOF_STATE

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.60
EOF_AM

cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.70
EOF_ADMIN

touch "$SSH_KEY" "$SSH_KEY.pub"

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "mock deploy failed" >&2
exit 42
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "account deploy should not run" >&2
exit 99
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "admin deploy should not run" >&2
exit 99
SH
chmod +x \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh"

OUT="$TMP/out.txt"
if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$SSH_KEY" \
	--video-release video-test \
	--account-release account-test \
	--admin-release admin-test \
	--deploy >"$OUT" 2>&1; then
	echo "cloud-provision unexpectedly passed" >&2
	exit 1
fi

grep -F 'mock deploy failed' "$OUT" >/dev/null
REPORT="$(find "$ENV_ROOT/artifacts" -name readiness-report.md | head -n 1)"
grep -F 'status: failed' "$REPORT" >/dev/null
grep -F 'SKIP `cloud-admin-deploy` blocked_by=`video-cloud-deploy-verify`' "$REPORT" >/dev/null
