#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
FAKE_BIN="$TMP/bin"
SSH_KEY="$TMP/id_ed25519_rtkcloud"
OBJECT_ROOT="$TMP/object-storage"
mkdir -p \
	"$ENV_ROOT/services/video-cloud" \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud" \
	"$WORKSPACE/repos/rtk_account_manager" \
	"$WORKSPACE/repos/rtk_cloud_admin" \
	"$WORKSPACE/keys/staging/linode/video-cloud" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/env" \
	"$OBJECT_ROOT/test-bucket/releases/rtk_video_cloud-v1.2.3" \
	"$OBJECT_ROOT/test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456" \
	"$OBJECT_ROOT/test-bucket/releases/rtk_account_manager-ci-20260527-100000-fedcba654321" \
	"$OBJECT_ROOT/test-bucket/releases/rtk_cloud_admin-admin-test"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"https://api.ipify.org"* ]]; then
	printf '203.0.113.99\n'
	exit 0
fi
printf 'unexpected curl: %s\n' "$*" >&2
exit 1
SH
chmod +x "$FAKE_BIN/curl"

cat > "$OBJECT_ROOT/test-bucket/releases/rtk_video_cloud-v1.2.3/manifest.json" <<'EOF_MANIFEST'
{
  "version": "v1.2.3",
  "artifact_path": "releases/rtk_video_cloud-v1.2.3/v1.2.3.tar.gz"
}
EOF_MANIFEST
touch "$OBJECT_ROOT/test-bucket/releases/rtk_video_cloud-v1.2.3/v1.2.3.tar.gz"

cat > "$OBJECT_ROOT/test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/manifest.json" <<'EOF_MANIFEST'
{
  "version": "ci-20260527-093000-abcdef123456",
  "artifact_path": "releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz"
}
EOF_MANIFEST
touch "$OBJECT_ROOT/test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz"

cat > "$OBJECT_ROOT/test-bucket/releases/rtk_account_manager-ci-20260527-100000-fedcba654321/manifest.json" <<'EOF_MANIFEST'
{
  "version": "ci-20260527-100000-fedcba654321",
  "artifact_path": "releases/rtk_account_manager-ci-20260527-100000-fedcba654321/ci-20260527-100000-fedcba654321.tar.gz"
}
EOF_MANIFEST
touch "$OBJECT_ROOT/test-bucket/releases/rtk_account_manager-ci-20260527-100000-fedcba654321/ci-20260527-100000-fedcba654321.tar.gz"

cat > "$OBJECT_ROOT/test-bucket/releases/rtk_cloud_admin-admin-test/manifest.json" <<'EOF_MANIFEST'
{
  "version": "admin-test",
  "artifact_path": "releases/rtk_cloud_admin-admin-test/admin-test.tar.gz"
}
EOF_MANIFEST
touch "$OBJECT_ROOT/test-bucket/releases/rtk_cloud_admin-admin-test/admin-test.tar.gz"

touch "$ENV_ROOT/topology/video-cloud-staging.yaml"
printf 'root-ca\n' > "$WORKSPACE/keys/staging/linode/video-cloud/root-ca.ed25519.cert.pem"
printf 'device-issuer\n' > "$WORKSPACE/keys/staging/linode/video-cloud/production-issuer.ed25519.cert.pem"
printf 'app-issuer\n' > "$WORKSPACE/keys/staging/linode/video-cloud/app-user-issuer.ed25519.cert.pem"
cat > "$ENV_ROOT/services/video-cloud/video-cloud-staging.env" <<'EOF_VIDEO_ENV'
CERT_ISSUER_CA_KEY_SOURCE=/tmp/device-ca.key
CERT_ISSUER_APP_CA_KEY_SOURCE=/tmp/app-ca.key
CERT_ISSUER_SIGNER_PROVIDER=pkcs11
CERT_ISSUER_PKCS11_MODULE_PATH=/usr/lib/softhsm/libsofthsm2.so
CERT_ISSUER_PKCS11_TOKEN_LABEL=video-cloud-signing
CERT_ISSUER_PKCS11_PIN=test-pin
CERT_ISSUER_PKCS11_KEY_LABEL=device-ca
CERT_ISSUER_APP_SIGNER_PROVIDER=pkcs11
CERT_ISSUER_APP_PKCS11_MODULE_PATH=/usr/lib/softhsm/libsofthsm2.so
CERT_ISSUER_APP_PKCS11_TOKEN_LABEL=video-cloud-signing
CERT_ISSUER_APP_PKCS11_PIN=test-pin
CERT_ISSUER_APP_PKCS11_KEY_LABEL=app-ca
VIDEO_CLOUD_AUTH_TOKEN_SIGNER_PROVIDER=pkcs11
VIDEO_CLOUD_AUTH_TOKEN_PKCS11_MODULE_PATH=/usr/lib/softhsm/libsofthsm2.so
VIDEO_CLOUD_AUTH_TOKEN_PKCS11_TOKEN_LABEL=video-cloud-signing
VIDEO_CLOUD_AUTH_TOKEN_PKCS11_PIN=test-pin
VIDEO_CLOUD_AUTH_TOKEN_PKCS11_KEY_LABEL=auth-token
VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL=http://10.42.1.50:18081
VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN=shared-internal-token
VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TIMEOUT=10s
EOF_VIDEO_ENV
cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_AM_ENV'
ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=shared-internal-token
EOF_AM_ENV
touch "$ENV_ROOT/services/cloud-admin/admin-staging.env"
touch "$SSH_KEY" "$SSH_KEY.pub"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
GODADDY_KEY=test-key
GODADDY_SECRET=test-secret
LINODE_OBJ_BUCKET=test-bucket
EOF_ENV
printf 'LINODE_OBJ_ENDPOINT=file://%s\n' "$OBJECT_ROOT" >> "$ENV_ROOT/env/operator.env"

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$SSH_KEY" \
	--preflight >"$OUT" 2>&1

grep -F 'selected Video Cloud Object Storage release: ci-20260527-093000-abcdef123456' "$OUT" >/dev/null
grep -F 'Available Video Cloud releases in Object Storage:' "$OUT" >/dev/null
grep -F '1) ci-20260527-093000-abcdef123456' "$OUT" >/dev/null
grep -F '2) v1.2.3' "$OUT" >/dev/null
grep -F 'Video Cloud Object Storage release readable: releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz' "$OUT" >/dev/null
grep -F 'selected Account Manager Object Storage release: ci-20260527-100000-fedcba654321' "$OUT" >/dev/null
grep -F 'Available Account Manager releases in Object Storage:' "$OUT" >/dev/null
grep -F 'Account Manager Object Storage release readable: releases/rtk_account_manager-ci-20260527-100000-fedcba654321/ci-20260527-100000-fedcba654321.tar.gz' "$OUT" >/dev/null
