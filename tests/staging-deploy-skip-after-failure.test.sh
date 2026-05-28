#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
FAKE_BIN="$TMP/bin"
VIDEO_DEPLOY_ARGS="$TMP/video-deploy.args"
mkdir -p \
	"$ENV_ROOT/services/video-cloud" \
	"$FAKE_BIN" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/artifacts" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts" \
	"$ENV_ROOT/state" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/cloud-admin" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_OPERATOR'
LINODE_TOKEN=test-token
EOF_OPERATOR
touch "$ENV_ROOT/topology/video-cloud-staging.yaml"
touch "$ENV_ROOT/services/video-cloud/video-cloud-staging.env"

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
{"instances":{"edge":{"public_ipv4":"203.0.113.10"}}}
EOF_STATE

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_AM_ENV'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20
EOF_AM_ENV
cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM_STATE'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20
EOF_AM_STATE

cat > "$ENV_ROOT/services/cloud-admin/admin-staging.env" <<'EOF_AD_ENV'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30
EOF_AD_ENV
cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_AD_STATE'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30
EOF_AD_STATE

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
SH
cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" > "$VIDEO_DEPLOY_ARGS"
echo "video cloud failed" >&2
exit 42
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "cloud admin should not run" >&2
exit 99
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "cloud admin verify should not run" >&2
exit 99
SH
chmod +x \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
cat <<'JSON'
{"data":[
  {"label":"video-cloud-staging-edge","tags":["video-cloud-staging"]},
  {"label":"video-cloud-staging-api","tags":["video-cloud-staging"]},
  {"label":"video-cloud-staging-infra","tags":["video-cloud-staging"]},
  {"label":"video-cloud-staging-mqtt","tags":["video-cloud-staging"]},
  {"label":"video-cloud-staging-coturn","tags":["video-cloud-staging"]},
  {"label":"rtk-account-manager-staging","tags":[]},
  {"label":"rtk-cloud-admin-staging","tags":[]}
]}
JSON
SH
cat > "$FAKE_BIN/dig" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
*" NS "*) echo "ns.example.com." ;;
*account-manager*) echo "203.0.113.20" ;;
*admin*) echo "203.0.113.30" ;;
*video-cloud-staging*) echo "203.0.113.10" ;;
*) echo "203.0.113.10" ;;
esac
SH
for cmd in ssh go tar; do
	cat > "$FAKE_BIN/$cmd" <<'SH'
#!/usr/bin/env bash
exit 0
SH
	chmod +x "$FAKE_BIN/$cmd"
done
chmod +x "$FAKE_BIN/curl" "$FAKE_BIN/dig"

OUT="$TMP/out.txt"
ERR="$TMP/err.txt"
if PATH="$FAKE_BIN:$PATH" VIDEO_DEPLOY_ARGS="$VIDEO_DEPLOY_ARGS" "$ROOT/scripts/cloud-deploy.sh" \
	--workspace "$WORKSPACE" \
	--video-release video-test \
	--account-release account-test \
	--admin-release admin-test >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

if PATH="$FAKE_BIN:$PATH" VIDEO_DEPLOY_ARGS="$VIDEO_DEPLOY_ARGS" "$ROOT/scripts/cloud-deploy.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--video-release video-test \
	--account-release account-test \
	--admin-release admin-test >"$OUT" 2>"$ERR"; then
	echo "cloud-deploy unexpectedly passed" >&2
	exit 1
fi

REPORT="$(grep -F '[cloud-deploy] readiness report:' "$ERR" | tail -n 1 | sed 's/^.*readiness report: //')"
if [[ -z "$REPORT" ]]; then
	REPORT="$(find "$ENV_ROOT/artifacts" -name readiness-report.md | head -n 1)"
fi
grep -F 'status: failed' "$REPORT" >/dev/null
grep -F 'FAIL `video-cloud-deploy-verify`' "$REPORT" >/dev/null
grep -F 'SKIP `cloud-admin-deploy` blocked_by=`video-cloud-deploy-verify`' "$REPORT" >/dev/null
grep -F 'SKIP `cloud-admin-verify` blocked_by=`video-cloud-deploy-verify`' "$REPORT" >/dev/null
test -f "$VIDEO_DEPLOY_ARGS"
if grep -F -- '--dns-ttl' "$VIDEO_DEPLOY_ARGS" >/dev/null; then
	echo "root cloud-deploy passed unsupported --dns-ttl to Video Cloud deploy" >&2
	exit 1
fi
