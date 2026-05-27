#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
SECRETS="$WORKSPACE/.secrets/staging/linode"
FAKE_BIN="$TMP/bin"
SSH_KEY="$TMP/id_ed25519_rtkcloud"
mkdir -p \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$SECRETS/video-cloud/config" \
	"$SECRETS/video-cloud/env"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
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
	exit 0
	;;
*)
	printf 'unexpected aws: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/aws"

touch "$SECRETS/video-cloud/config/video-cloud-staging.yaml"
touch "$SECRETS/video-cloud/env/video-cloud-staging.env"
touch "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env"
touch "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env"
touch "$SSH_KEY" "$SSH_KEY.pub"

cat > "$SECRETS/video-cloud/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
GODADDY_KEY=test-key
GODADDY_SECRET=test-secret
LINODE_OBJ_BUCKET=test-bucket
LINODE_OBJ_ENDPOINT=https://object.example.test
EOF_ENV

OUT="$TMP/out.txt"
if PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/staging-provision.sh" \
	--workspace "$WORKSPACE" \
	--secrets-root "$SECRETS" \
	--ssh-key "$SSH_KEY" \
	--preflight >"$OUT" 2>&1; then
	printf 'preflight unexpectedly passed without Object Storage releases\n' >&2
	exit 1
fi

grep -F 'no rtk_video_cloud release manifest found in Object Storage under releases/' "$OUT" >/dev/null
