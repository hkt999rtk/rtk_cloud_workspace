#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
FAKE_BIN="$TMP/bin"
DEPLOY_LOG="$TMP/deploy.log"
SSH_KEY="$TMP/id_ed25519_rtkcloud"
ACCOUNT_BUNDLE="$TMP/account.tar.gz"
mkdir -p \
	"$FAKE_BIN" \
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
{
  "stack": "video-cloud-staging",
  "instances": {
    "edge": {"public_ipv4": "203.0.113.5"}
  }
}
EOF_STATE

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.60
EOF_AM

cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.70
EOF_ADMIN

touch "$SSH_KEY" "$SSH_KEY.pub"
printf 'fake account bundle\n' > "$ACCOUNT_BUNDLE"

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'video %s\n' "$*" >> "$DEPLOY_LOG"
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'account release=%s bundle=%s\n' "${ACCOUNT_MANAGER_LINODE_RELEASE:-}" "${ACCOUNT_MANAGER_LINODE_RELEASE_BUNDLE:-}" >> "$DEPLOY_LOG"
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'admin release=%s prometheus=%s\n' "${ADMIN_LINODE_RELEASE:-}" "${VIDEO_CLOUD_PROMETHEUS_BASE_URL:-}" >> "$DEPLOY_LOG"
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
SH
chmod +x \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh"

OUT="$TMP/out.txt"
DEPLOY_LOG="$DEPLOY_LOG" \
PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$SSH_KEY" \
	--video-release staging-20260527T075403Z-c536e34 \
	--account-release account-test-release \
	--account-release-bundle "$ACCOUNT_BUNDLE" \
	--admin-release admin-test-release \
	--deploy >"$OUT" 2>&1

grep -F 'video --stack video-cloud-staging --gateway-domain video-cloud-staging.realtekconnect.com' "$DEPLOY_LOG" >/dev/null
grep -F 'account release=account-test-release bundle='"$ACCOUNT_BUNDLE" "$DEPLOY_LOG" >/dev/null
grep -F 'admin release=admin-test-release prometheus=http://10.42.1.30:9090' "$DEPLOY_LOG" >/dev/null
