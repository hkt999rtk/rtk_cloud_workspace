#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
SSH_KEY="$TMP/id_ed25519_rtkcloud"
LOG="$TMP/cert-cache-env.log"
ADMIN_BUNDLE="$TMP/rtk_cloud_admin-admin-test.tar.gz"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/services/video-cloud" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/artifacts" \
	"$ENV_ROOT/certificates/video-cloud-staging.example.com" \
	"$ENV_ROOT/certificates/account-manager.video-cloud-staging.example.com" \
	"$ENV_ROOT/certificates/admin.video-cloud-staging.example.com" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode"

make_cert() {
	local domain="$1"
	local dir="$2"
	openssl req -x509 -newkey rsa:2048 -sha256 -days 30 -nodes \
		-subj "/CN=$domain" \
		-keyout "$dir/privkey.pem" \
		-out "$dir/fullchain.pem" >/dev/null 2>&1
}

make_cert video-cloud-staging.example.com "$ENV_ROOT/certificates/video-cloud-staging.example.com"
make_cert account-manager.video-cloud-staging.example.com "$ENV_ROOT/certificates/account-manager.video-cloud-staging.example.com"
make_cert admin.video-cloud-staging.example.com "$ENV_ROOT/certificates/admin.video-cloud-staging.example.com"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_OPERATOR'
LINODE_TOKEN=test-token
LINODE_OBJ_BUCKET=test-bucket
LINODE_OBJ_ENDPOINT=https://example.invalid
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
touch "$SSH_KEY"
printf 'fake-admin-bundle\n' > "$ADMIN_BUNDLE"

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'am=%s\n' "${ACCOUNT_MANAGER_LINODE_CERT_CACHE_DIR:-}" >> "$CERT_CACHE_LOG"
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
SH
cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'vc=%s\n' "${LINODE_DEPLOY_CERT_CACHE_DIR:-}" >> "$CERT_CACHE_LOG"
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'admin=%s\n' "${ADMIN_LINODE_CERT_CACHE_DIR:-}" >> "$CERT_CACHE_LOG"
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
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
cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
exit 1
SH
for cmd in go tar; do
	cat > "$FAKE_BIN/$cmd" <<'SH'
#!/usr/bin/env bash
exit 0
SH
	chmod +x "$FAKE_BIN/$cmd"
done
chmod +x "$FAKE_BIN/curl" "$FAKE_BIN/dig" "$FAKE_BIN/ssh"

PATH="$FAKE_BIN:$PATH" CERT_CACHE_LOG="$LOG" "$ROOT/scripts/cloud-deploy.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$SSH_KEY" \
	--dns-root-domain example.com \
	--video-release video-test \
	--account-release account-test \
	--admin-release admin-test \
	--admin-release-bundle "$ADMIN_BUNDLE" >/dev/null

grep -F "am=$ENV_ROOT/certificates/account-manager.video-cloud-staging.example.com" "$LOG" >/dev/null
grep -F "vc=$ENV_ROOT/certificates/video-cloud-staging.example.com" "$LOG" >/dev/null
grep -F "admin=$ENV_ROOT/certificates/admin.video-cloud-staging.example.com" "$LOG" >/dev/null
