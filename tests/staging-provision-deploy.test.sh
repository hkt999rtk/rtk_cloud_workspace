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
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/artifacts"

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

cat > "$TMP/mock-staging-deploy.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" > "$DEPLOY_LOG"
SH
chmod +x "$TMP/mock-staging-deploy.sh"

OUT="$TMP/out.txt"
CLOUD_DEPLOY_SCRIPT="$TMP/mock-staging-deploy.sh" \
DEPLOY_LOG="$DEPLOY_LOG" \
PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/cloud-provision.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$SSH_KEY" \
	--video-release staging-20260527T075403Z-c536e34 \
	--account-release account-test-release \
	--admin-release admin-test-release \
	--deploy >"$OUT" 2>&1

grep -F '[cloud-provision] deploy' "$OUT" >/dev/null
grep -F 'deploy releases: video=staging-20260527T075403Z-c536e34 account=account-test-release admin=admin-test-release' "$OUT" >/dev/null
grep -F -- '--workspace' "$DEPLOY_LOG" >/dev/null
grep -F -- "$WORKSPACE" "$DEPLOY_LOG" >/dev/null
grep -F -- '--video-release staging-20260527T075403Z-c536e34' "$DEPLOY_LOG" >/dev/null
grep -F -- '--account-release account-test-release' "$DEPLOY_LOG" >/dev/null
grep -F -- '--admin-release admin-test-release' "$DEPLOY_LOG" >/dev/null
