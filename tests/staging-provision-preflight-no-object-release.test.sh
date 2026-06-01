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
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/env" \
	"$OBJECT_ROOT/test-bucket/releases"

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
EOF_ENV
printf 'LINODE_OBJ_ENDPOINT=file://%s\n' "$OBJECT_ROOT" >> "$ENV_ROOT/env/operator.env"

OUT="$TMP/out.txt"
if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$SSH_KEY" \
	--preflight >"$OUT" 2>&1; then
	printf 'preflight unexpectedly passed without Object Storage releases\n' >&2
	exit 1
fi

grep -F 'no rtk_video_cloud release manifest found in Object Storage under releases/' "$OUT" >/dev/null
