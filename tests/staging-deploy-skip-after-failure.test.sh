#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
SECRETS="$WORKSPACE/.secrets/staging/linode"
FAKE_BIN="$TMP/bin"
mkdir -p \
	"$FAKE_BIN" \
	"$SECRETS/video-cloud/config" \
	"$SECRETS/video-cloud/env" \
	"$SECRETS/video-cloud/artifacts" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/state" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode"

cat > "$SECRETS/video-cloud/env/operator.env" <<'EOF_OPERATOR'
LINODE_TOKEN=test-token
EOF_OPERATOR
touch "$SECRETS/video-cloud/config/video-cloud-staging.yaml"
touch "$SECRETS/video-cloud/env/video-cloud-staging.env"

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json" <<'EOF_STATE'
{"instances":{"edge":{"public_ipv4":"203.0.113.10"}}}
EOF_STATE

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env" <<'EOF_AM_ENV'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20
EOF_AM_ENV
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env" <<'EOF_AM_STATE'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20
EOF_AM_STATE

cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env" <<'EOF_AD_ENV'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30
EOF_AD_ENV
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state" <<'EOF_AD_STATE'
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
if PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/staging-deploy.sh" \
	--workspace "$WORKSPACE" \
	--secrets-root "$SECRETS" \
	--video-release video-test \
	--account-release account-test \
	--admin-release admin-test >"$OUT" 2>"$ERR"; then
	echo "staging-deploy unexpectedly passed" >&2
	exit 1
fi

REPORT="$(grep -F '[staging-deploy] readiness report:' "$ERR" | tail -n 1 | sed 's/^.*readiness report: //')"
if [[ -z "$REPORT" ]]; then
	REPORT="$(find "$SECRETS/video-cloud/artifacts" -name readiness-report.md | head -n 1)"
fi
grep -F 'status: failed' "$REPORT" >/dev/null
grep -F 'FAIL `video-cloud-deploy-verify`' "$REPORT" >/dev/null
grep -F 'SKIP `cloud-admin-deploy` blocked_by=`video-cloud-deploy-verify`' "$REPORT" >/dev/null
grep -F 'SKIP `cloud-admin-verify` blocked_by=`video-cloud-deploy-verify`' "$REPORT" >/dev/null
