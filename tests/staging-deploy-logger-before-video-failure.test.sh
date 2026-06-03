#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
ORDER_LOG="$TMP/order.log"
LOGGER_LOG="$TMP/logger.log"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/video-cloud" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/artifacts" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_OPERATOR'
LINODE_TOKEN=test-token
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
touch "$ENV_ROOT/services/account-manager/account-manager-public-staging.env"
touch "$ENV_ROOT/services/cloud-admin/admin-staging.env"
cat > "$ENV_ROOT/state/video-cloud-ci.state.json" <<'EOF_STATE'
{"instances":{
  "edge":{"public_ipv4":"203.0.113.10"},
  "api":{"private_ip":"10.42.1.10"},
  "infra":{"private_ip":"10.42.1.30"},
  "mqtt":{"public_ipv4":"203.0.113.13"},
  "coturn":{"public_ipv4":"203.0.113.14"}
}}
EOF_STATE
cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20
EOF_AM
cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30
EOF_ADMIN

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'video-cloud-deploy-verify\n' >> "$ORDER_LOG"
exit 42
SH
for path in \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh"; do
	cat > "$path" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$(basename "$0")" >> "$ORDER_LOG"
SH
done
chmod +x \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh"

cat > "$TMP/mock-logger.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$LOGGER_LOG"
case "$1" in
provision-backend)
	printf 'logger-provision-backend\n' >> "$ORDER_LOG"
	;;
install-forwarder)
	printf 'logger-install-forwarder %s\n' "$2" >> "$ORDER_LOG"
	;;
backend-health|sample-trace-query|forwarder-status)
	;;
*)
	exit 27
	;;
esac
SH
chmod +x "$TMP/mock-logger.sh"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '{"data":[]}\n'
SH
for cmd in ssh go tar openssl dig; do
	cat > "$FAKE_BIN/$cmd" <<'SH'
#!/usr/bin/env bash
exit 0
SH
	chmod +x "$FAKE_BIN/$cmd"
done
chmod +x "$FAKE_BIN/curl"

OUT="$TMP/out.txt"
ERR="$TMP/err.txt"
set +e
ORDER_LOG="$ORDER_LOG" LOGGER_LOG="$LOGGER_LOG" CLOUD_LOGGER_SCRIPT="$TMP/mock-logger.sh" PATH="$FAKE_BIN:$PATH" /usr/local/go/bin/go run "$ROOT/scripts/go/rtk-cloud" -- deploy \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" >"$OUT" 2>"$ERR"
status=$?
set -e
[[ "$status" -ne 0 ]]

REPORT="$(grep -F '[cloud-deploy] readiness report:' "$ERR" | tail -n 1 | sed 's/^.*readiness report: //')"
grep -F 'status: failed' "$REPORT" >/dev/null
grep -F 'FAIL `video-cloud-deploy-verify`' "$REPORT" >/dev/null
for target in account-manager video-cloud-api cloud-admin edge infra mqtt coturn; do
	grep -F 'install-forwarder '"$target" "$LOGGER_LOG" >/dev/null
	grep -F 'PASS `logger-forwarder-status:'"$target"'`' "$REPORT" >/dev/null
done
test "$(grep -c '^logger-install-forwarder account-manager$' "$ORDER_LOG")" -eq 1
logger_line="$(grep -n '^logger-provision-backend$' "$ORDER_LOG" | cut -d: -f1)"
install_line="$(grep -n '^logger-install-forwarder account-manager$' "$ORDER_LOG" | cut -d: -f1)"
video_line="$(grep -n '^video-cloud-deploy-verify$' "$ORDER_LOG" | cut -d: -f1)"
[[ "$logger_line" -lt "$video_line" ]]
[[ "$install_line" -lt "$video_line" ]]
! grep -F 'deploy-public-vm.sh' "$ORDER_LOG" >/dev/null
! grep -F 'deploy-admin.sh' "$ORDER_LOG" >/dev/null
