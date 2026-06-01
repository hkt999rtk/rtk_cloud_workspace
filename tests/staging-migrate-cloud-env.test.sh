#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"

mkdir -p \
	"$WORKSPACE/.secrets/staging/linode/video-cloud/env" \
	"$WORKSPACE/.secrets/staging/linode/video-cloud/config" \
	"$WORKSPACE/.secrets/staging/linode/video-cloud/artifacts/run-1" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state" \
	"$WORKSPACE/keys/staging/linode/video-cloud" \
	"$WORKSPACE/keys/test_device/manifests"

printf 'LINODE_TOKEN=test\n' > "$WORKSPACE/.secrets/staging/linode/video-cloud/env/operator.env"
printf 'stack: video-cloud-staging\n' > "$WORKSPACE/.secrets/staging/linode/video-cloud/config/video-cloud-staging.yaml"
printf 'VIDEO_CLOUD_AUTH_SECRET=test\n' > "$WORKSPACE/.secrets/staging/linode/video-cloud/env/video-cloud-staging.env"
printf 'ACCOUNT_MANAGER_LINODE_DOMAIN=am.example\n' > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env"
printf 'ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com\n' > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-platform-admin.env"
printf 'ACCOUNT_MANAGER_LINODE_ID=1\n' > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env"
printf 'ADMIN_LINODE_DOMAIN=admin.example\n' > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env"
printf 'ADMIN_LINODE_ID=2\n' > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state"
printf '{"stack":"video-cloud-staging"}\n' > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json"
printf 'key\n' > "$WORKSPACE/keys/staging/linode/video-cloud/root-ca.key.pem"
printf 'load-device-0001\n' > "$WORKSPACE/keys/test_device/manifests/device_ids.txt"
printf 'artifact\n' > "$WORKSPACE/.secrets/staging/linode/video-cloud/artifacts/run-1/report.md"

OUT="$TMP/out.txt"
if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- migrate-env --workspace "$WORKSPACE" > "$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- migrate-env --workspace "$WORKSPACE" --env-root "$WORKSPACE/cloud_env/staging" > "$OUT"

test -f "$ENV_ROOT/env/operator.env"
test -f "$ENV_ROOT/env/stack.env"
test -f "$ENV_ROOT/topology/video-cloud-staging.yaml"
test -f "$ENV_ROOT/services/video-cloud/video-cloud-staging.env"
test -f "$ENV_ROOT/services/account-manager/account-manager-public-staging.env"
test -f "$ENV_ROOT/services/account-manager/account-manager-platform-admin.env"
test -f "$ENV_ROOT/services/cloud-admin/admin-staging.env"
test -f "$ENV_ROOT/state/video-cloud-staging.state.json"
test -f "$ENV_ROOT/state/account-manager-staging.env"
test -f "$ENV_ROOT/state/cloud-admin-staging.env"
test -f "$ENV_ROOT/keys/video-cloud/root-ca.key.pem"
test -f "$ENV_ROOT/devices/test_device/manifests/device_ids.txt"
test -f "$ENV_ROOT/artifacts/run-1/report.md"

MANIFEST="$(sed -n 's/^manifest=//p' "$OUT")"
test -f "$MANIFEST"
grep -F 'operator-env' "$MANIFEST" >/dev/null
grep -F 'stack-metadata' "$MANIFEST" >/dev/null
grep -F 'copied' "$MANIFEST" >/dev/null
awk -F '\t' 'NR > 1 && $5 == "" { exit 1 }' "$MANIFEST"
grep -F 'CLOUD_STACK_NAME=video-cloud-staging' "$ENV_ROOT/env/stack.env" >/dev/null
grep -F 'ACCOUNT_MANAGER_DOMAIN=am.example' "$ENV_ROOT/env/stack.env" >/dev/null
grep -F 'CLOUD_ADMIN_DOMAIN=admin.example' "$ENV_ROOT/env/stack.env" >/dev/null
