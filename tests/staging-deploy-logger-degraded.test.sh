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
ACCOUNT_BUNDLE="$TMP/rtk_account_manager-account-test.tar.gz"
ADMIN_BUNDLE="$TMP/rtk_cloud_admin-admin-test.tar.gz"
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

printf 'fake-account-bundle\n' > "$ACCOUNT_BUNDLE"
printf 'fake-admin-bundle\n' > "$ADMIN_BUNDLE"
touch "$TMP/id_ed25519_rtkcloud"

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

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
{"instances":{"edge":{"public_ipv4":"203.0.113.10"}}}
EOF_STATE
cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.20
EOF_AM
cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.30
EOF_ADMIN

cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'account-manager-deploy\n' >> "$ORDER_LOG"
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'account-manager-verify\n' >> "$ORDER_LOG"
SH
cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'video-cloud-deploy-verify\n' >> "$ORDER_LOG"
while [[ $# -gt 0 ]]; do
	if [[ "$1" == "--report" ]]; then
		printf '# video health\n' > "$2"
		shift 2
	else
		shift
	fi
done
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'cloud-admin-deploy\n' >> "$ORDER_LOG"
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'cloud-admin-verify\n' >> "$ORDER_LOG"
SH
chmod +x \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh"

cat > "$TMP/mock-logger.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$LOGGER_LOG"
case "$1" in
provision-backend)
	printf 'logger-provision-backend\n' >> "$ORDER_LOG"
	exit 23
	;;
install-forwarder)
	printf 'logger-install-forwarder %s\n' "$2" >> "$ORDER_LOG"
	exit 24
	;;
backend-health|sample-trace-query)
	exit 25
	;;
forwarder-status)
	exit 26
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
for cmd in ssh go tar openssl; do
	cat > "$FAKE_BIN/$cmd" <<'SH'
#!/usr/bin/env bash
exit 0
SH
	chmod +x "$FAKE_BIN/$cmd"
done
chmod +x "$FAKE_BIN/curl" "$FAKE_BIN/dig"

OUT="$TMP/out.txt"
ERR="$TMP/err.txt"
ORDER_LOG="$ORDER_LOG" LOGGER_LOG="$LOGGER_LOG" CLOUD_LOGGER_SCRIPT="$TMP/mock-logger.sh" PATH="$FAKE_BIN:$PATH" /usr/local/go/bin/go run "$ROOT/scripts/go/rtk-cloud" -- deploy \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$TMP/id_ed25519_rtkcloud" \
	--video-release video-test \
	--account-release account-test \
	--account-release-bundle "$ACCOUNT_BUNDLE" \
	--admin-release admin-test \
	--admin-release-bundle "$ADMIN_BUNDLE" >"$OUT" 2>"$ERR"

REPORT="$(grep -F '[cloud-deploy] readiness report:' "$ERR" | tail -n 1 | sed 's/^.*readiness report: //')"
grep -F 'status: passed' "$REPORT" >/dev/null
grep -F 'logging: degraded' "$REPORT" >/dev/null
grep -F 'DEGRADED `logger-backend-health`' "$REPORT" >/dev/null
grep -F 'DEGRADED `logger-forwarder:account-manager`' "$REPORT" >/dev/null
grep -F 'DEGRADED `logger-forwarder:video-cloud-api`' "$REPORT" >/dev/null
grep -F 'DEGRADED `logger-forwarder:cloud-admin`' "$REPORT" >/dev/null
grep -F 'DEGRADED `logger-sample-trace-query`' "$REPORT" >/dev/null
grep -F 'install-forwarder frontend' "$LOGGER_LOG" >/dev/null
grep -F 'install-forwarder non-go-host-sources' "$LOGGER_LOG" >/dev/null

logger_line="$(grep -n '^logger-provision-backend$' "$ORDER_LOG" | cut -d: -f1)"
account_line="$(grep -n '^account-manager-deploy$' "$ORDER_LOG" | cut -d: -f1)"
[[ "$logger_line" -lt "$account_line" ]]
