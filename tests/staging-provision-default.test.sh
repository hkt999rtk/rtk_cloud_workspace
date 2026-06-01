#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
FAKE_BIN="$TMP/bin"
mkdir -p "$FAKE_BIN" "$ENV_ROOT/env"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
*"/linode/instances?page_size=500"*)
	cat <<'JSON'
{"data":[{"id":1,"label":"video-cloud-ci-edge","region":"us-sea","type":"g6-standard-2","status":"running","ipv4":["203.0.113.5"],"tags":["video-cloud-ci","role:edge"]}]}
JSON
	;;
*"/networking/firewalls?page_size=500"*)
	cat <<'JSON'
{"data":[{"id":101,"label":"video-cloud-ci-edge","status":"enabled","tags":["video-cloud-ci","role:edge"]}]}
JSON
	;;
*"/vpcs?page_size=500"*)
	cat <<'JSON'
{"data":[{"id":9001,"label":"video-cloud-ci-vpc","region":"us-sea"}]}
JSON
	;;
*)
	printf 'unexpected curl: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
EOF_ENV
cat > "$ENV_ROOT/env/stack.env" <<'EOF_ENV'
CLOUD_ENV_NAME=ci
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=example.test
CLOUD_STACK_NAME=video-cloud-ci
VIDEO_CLOUD_DOMAIN=video-cloud-ci.example.test
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-ci.example.test
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-ci.example.test
CLOUD_ADMIN_DOMAIN=admin.video-cloud-ci.example.test
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-ci
VIDEO_CLOUD_VPC_LABEL=video-cloud-ci-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-ci-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-ci
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-ci-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-ci
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-ci-firewall
EOF_ENV

if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" >"$OUT"

grep -F 'Target instances:' "$OUT" >/dev/null
grep -F 'video-cloud-ci-edge' "$OUT" >/dev/null
grep -F 'Intended resources:' "$OUT" >/dev/null
grep -F 'video-cloud-ci-edge/api/infra/mqtt/coturn' "$OUT" >/dev/null
grep -F 'rtk-account-manager-ci' "$OUT" >/dev/null
grep -F 'admin.video-cloud-ci.example.test' "$OUT" >/dev/null
