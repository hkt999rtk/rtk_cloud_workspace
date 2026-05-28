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
mkdir -p \
	"$ENV_ROOT/services/video-cloud" \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud" \
	"$WORKSPACE/repos/rtk_account_manager" \
	"$WORKSPACE/repos/rtk_cloud_admin" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/env"

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

cat > "$FAKE_BIN/aws" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
*"s3 ls s3://test-bucket/releases/ --recursive"*)
	cat <<'EOF_LS'
2026-05-26 10:00:00        180 releases/rtk_video_cloud-v1.2.3/manifest.json
2026-05-27 09:30:00        198 releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/manifest.json
2026-05-27 10:30:00        198 releases/rtk_cloud_admin-admin-test/manifest.json
EOF_LS
	;;
*"s3 cp s3://test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/manifest.json -"*)
	cat <<'EOF_MANIFEST'
{
  "version": "ci-20260527-093000-abcdef123456",
  "artifact_path": "releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz"
}
EOF_MANIFEST
	;;
*"s3 cp s3://test-bucket/releases/rtk_cloud_admin-admin-test/manifest.json -"*)
	cat <<'EOF_MANIFEST'
{
  "version": "admin-test",
  "artifact_path": "releases/rtk_cloud_admin-admin-test/admin-test.tar.gz"
}
EOF_MANIFEST
	;;
*"s3 ls s3://test-bucket/releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz"*)
	printf '2026-05-27 09:31:00 123456 releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz\n'
	;;
*"s3 ls s3://test-bucket/releases/rtk_cloud_admin-admin-test/admin-test.tar.gz"*)
	printf '2026-05-27 10:31:00 123456 releases/rtk_cloud_admin-admin-test/admin-test.tar.gz\n'
	;;
*)
	printf 'unexpected aws: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/aws"

touch "$ENV_ROOT/topology/video-cloud-staging.yaml"
touch "$ENV_ROOT/services/video-cloud/video-cloud-staging.env"
touch "$ENV_ROOT/services/account-manager/account-manager-public-staging.env"
touch "$ENV_ROOT/services/cloud-admin/admin-staging.env"
touch "$SSH_KEY" "$SSH_KEY.pub"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
GODADDY_KEY=test-key
GODADDY_SECRET=test-secret
LINODE_OBJ_BUCKET=test-bucket
LINODE_OBJ_ENDPOINT=https://object.example.test
EOF_ENV

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/cloud-provision.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$SSH_KEY" \
	--preflight >"$OUT" 2>&1

grep -F 'selected Video Cloud Object Storage release: ci-20260527-093000-abcdef123456' "$OUT" >/dev/null
grep -F 'Available Video Cloud releases in Object Storage:' "$OUT" >/dev/null
grep -F '1) ci-20260527-093000-abcdef123456' "$OUT" >/dev/null
grep -F '2) v1.2.3' "$OUT" >/dev/null
grep -F 'Video Cloud Object Storage release readable: releases/rtk_video_cloud-ci-20260527-093000-abcdef123456/ci-20260527-093000-abcdef123456.tar.gz' "$OUT" >/dev/null
