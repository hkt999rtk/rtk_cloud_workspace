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
ACCOUNT_OBJECT_CONTENT="fake-account-bundle"
ACCOUNT_OBJECT_SHA="$(printf '%s\n' "$ACCOUNT_OBJECT_CONTENT" | shasum -a 256 | awk '{print $1}')"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/services/video-cloud" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/artifacts" \
	"$ENV_ROOT/certificates/video-cloud-ci.example.com" \
	"$ENV_ROOT/certificates/account-manager.video-cloud-ci.example.com" \
	"$ENV_ROOT/certificates/admin.video-cloud-ci.example.com" \
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

make_cert video-cloud-ci.example.com "$ENV_ROOT/certificates/video-cloud-ci.example.com"
make_cert account-manager.video-cloud-ci.example.com "$ENV_ROOT/certificates/account-manager.video-cloud-ci.example.com"
make_cert admin.video-cloud-ci.example.com "$ENV_ROOT/certificates/admin.video-cloud-ci.example.com"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_OPERATOR'
LINODE_TOKEN=test-token
LINODE_OBJ_BUCKET=test-bucket
LINODE_OBJ_ENDPOINT=https://example.invalid
EOF_OPERATOR
cat > "$ENV_ROOT/env/stack.env" <<'EOF_STACK'
CLOUD_ENV_NAME=ci
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=example.com
CLOUD_STACK_NAME=video-cloud-ci
VIDEO_CLOUD_DOMAIN=video-cloud-ci.example.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-ci.example.com
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-ci.example.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-ci.example.com
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-ci
VIDEO_CLOUD_VPC_LABEL=video-cloud-ci-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-ci-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-ci
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-ci-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-ci
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-ci-fw
EOF_STACK
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
printf 'am_bundle=%s\n' "${ACCOUNT_MANAGER_LINODE_RELEASE_BUNDLE:-}" >> "$CERT_CACHE_LOG"
[ -s "${ACCOUNT_MANAGER_LINODE_RELEASE_BUNDLE:-}" ]
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
  {"id":1,"label":"video-cloud-ci-edge","ipv4":["203.0.113.10"],"ipv6":"","tags":["video-cloud-ci"]},
  {"id":2,"label":"video-cloud-ci-api","ipv4":["203.0.113.11"],"ipv6":"","tags":["video-cloud-ci"]},
  {"id":3,"label":"video-cloud-ci-infra","ipv4":["203.0.113.12"],"ipv6":"","tags":["video-cloud-ci"]},
  {"id":4,"label":"video-cloud-ci-mqtt","ipv4":["203.0.113.13"],"ipv6":"","tags":["video-cloud-ci"]},
  {"id":5,"label":"video-cloud-ci-coturn","ipv4":["203.0.113.14"],"ipv6":"","tags":["video-cloud-ci"]},
  {"id":6,"label":"rtk-account-manager-ci","ipv4":["203.0.113.20"],"ipv6":"","tags":[]},
  {"id":7,"label":"rtk-cloud-admin-ci","ipv4":["203.0.113.30"],"ipv6":"","tags":[]}
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
*video-cloud-ci*) echo "203.0.113.10" ;;
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
cat > "$FAKE_BIN/aws" <<SH
#!/usr/bin/env bash
set -euo pipefail
case "\$*" in
*"s3 cp s3://test-bucket/releases/rtk_account_manager-account-test/manifest.json -"*)
	cat <<'JSON'
{
  "version": "account-test",
  "artifact_path": "releases/rtk_account_manager-account-test/account-test.tar.gz",
  "sha256": "$ACCOUNT_OBJECT_SHA"
}
JSON
	;;
*"s3 cp s3://test-bucket/releases/rtk_account_manager-account-test/account-test.tar.gz "*)
	dest="\${@: -3:1}"
	printf '%s\n' "$ACCOUNT_OBJECT_CONTENT" > "\$dest"
	;;
*)
	printf 'unexpected aws: %s\n' "\$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/aws"
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

grep -F "am=$ENV_ROOT/certificates/account-manager.video-cloud-ci.example.com" "$LOG" >/dev/null
grep -F "am_bundle=$ENV_ROOT/artifacts/readiness-" "$LOG" >/dev/null
grep -F "vc=$ENV_ROOT/certificates/video-cloud-ci.example.com" "$LOG" >/dev/null
grep -F "admin=$ENV_ROOT/certificates/admin.video-cloud-ci.example.com" "$LOG" >/dev/null
