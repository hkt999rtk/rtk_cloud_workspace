#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
ORDER_LOG="$TMP/order.log"
SSH_LOG="$TMP/ssh.log"
SCP_LOG="$TMP/scp.log"
GO_LOG="$TMP/go.log"
CURL_EVENT="$TMP/logger-event.json"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/video-cloud" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/services/cloud-logger" \
	"$ENV_ROOT/artifacts" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$WORKSPACE/repos/rtk_cloud_logger/cmd/rtk-cloud-log-forwarder"

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
cat > "$ENV_ROOT/services/cloud-logger/logger.env" <<'EOF_LOGGER'
CLOUD_LOGGER_ENDPOINT=https://logger.video-cloud-ci.example.com
CLOUD_LOGGER_INGEST_TOKEN=super-secret-forwarder-token
EOF_LOGGER
cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
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

for path in \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh"; do
	cat > "$path" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$(basename "$0")" >> "$ORDER_LOG"
SH
	chmod +x "$path"
done

cat > "$FAKE_BIN/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'GOOS=%s GOARCH=%s args=%s\n' "${GOOS:-}" "${GOARCH:-}" "$*" >> "$GO_LOG"
while [[ "$#" -gt 0 ]]; do
	if [[ "$1" == "-o" ]]; then
		mkdir -p "$(dirname "$2")"
		printf 'fake-binary\n' > "$2"
		chmod +x "$2"
		break
	fi
	shift
done
exit 0
SH
cat > "$FAKE_BIN/scp" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SCP_LOG"
exit 0
SH
cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
host=""
for arg in "$@"; do
	case "$arg" in
	root@*) host="$arg" ;;
	esac
done
if [[ -z "$host" ]]; then
	printf 'missing ssh host: %s\n' "$*" >&2
	exit 8
fi
if [[ "$*" == *"systemctl is-active --quiet rtk-cloud-log-forwarder.service"* ]]; then
	printf 'readiness %s args=%s\n' "$host" "$*" >> "$SSH_LOG"
	exit 0
fi
input="$(cat)"
if [[ "$input" != *super-secret-forwarder-token* ]]; then
	printf 'remote install script missing token\n' >&2
	exit 9
fi
if [[ "$input" != *"RTK_CLOUD_LOGGER_INGEST_URL=https://logger.video-cloud-ci.example.com/v1/logs/ingest"* ]]; then
	printf 'remote install script missing ingest path\n' >&2
	exit 10
fi
printf 'host=%s args=%s\n%s\n' "$host" "$*" "${input//super-secret-forwarder-token/[REDACTED]}" >> "$SSH_LOG"
exit 0
SH
cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
*"/healthz"*)
	printf 'ok\n'
	;;
*"/v1/logs/ingest"*)
	while [[ "$#" -gt 0 ]]; do
		if [[ "$1" == "--data-binary" ]]; then
			printf '%s\n' "$2" > "$CURL_EVENT"
			break
		fi
		shift
	done
	printf '{"results":[{"status":"accepted"}]}\n'
	;;
*"/v1/logs?"*)
	cat "$CURL_EVENT"
	;;
*)
	printf 'unexpected curl: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/go" "$FAKE_BIN/scp" "$FAKE_BIN/ssh" "$FAKE_BIN/curl"

OUT="$TMP/out.txt"
ERR="$TMP/err.txt"
ORDER_LOG="$ORDER_LOG" SSH_LOG="$SSH_LOG" SCP_LOG="$SCP_LOG" GO_LOG="$GO_LOG" CURL_EVENT="$CURL_EVENT" RTK_CLOUD_GO="$FAKE_BIN/go" PATH="$FAKE_BIN:$PATH" /usr/local/go/bin/go run "$ROOT/scripts/go/rtk-cloud" -- deploy \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" >"$OUT" 2>"$ERR"

REPORT="$(grep -F '[cloud-deploy] readiness report:' "$ERR" | tail -n 1 | sed 's/^.*readiness report: //')"
grep -F 'PASS `logger-backend-health`' "$REPORT" >/dev/null
grep -F 'PASS `logger-ingest-idempotency`' "$REPORT" >/dev/null
grep -F 'PASS `logger-sample-trace-query`' "$REPORT" >/dev/null
for host in 203.0.113.20 10.42.1.10 203.0.113.30 203.0.113.10 10.42.1.30 203.0.113.13 203.0.113.14; do
	grep -F "root@$host:/usr/local/bin/rtk-cloud-log-forwarder" "$SCP_LOG" >/dev/null
	grep -F "host=root@$host" "$SSH_LOG" >/dev/null
	grep -F "readiness root@$host" "$SSH_LOG" >/dev/null
done
grep -F 'GOOS=linux GOARCH=amd64' "$GO_LOG" >/dev/null
grep -F -- '-i ' "$SCP_LOG" >/dev/null
grep -F -- 'BatchMode=yes' "$SCP_LOG" >/dev/null
grep -F -- 'StrictHostKeyChecking=accept-new' "$SCP_LOG" >/dev/null
grep -F -- '-i ' "$SSH_LOG" >/dev/null
grep -F -- 'BatchMode=yes' "$SSH_LOG" >/dev/null
grep -F -- 'StrictHostKeyChecking=accept-new' "$SSH_LOG" >/dev/null
grep -F -- 'ProxyCommand=ssh' "$SCP_LOG" >/dev/null
grep -F -- 'ProxyCommand=ssh' "$SSH_LOG" >/dev/null
grep -F -- '-W %h:%p root@203.0.113.10' "$SCP_LOG" >/dev/null
grep -F -- '-W %h:%p root@203.0.113.10' "$SSH_LOG" >/dev/null
if grep -R 'super-secret-forwarder-token' "$OUT" "$ERR" "$ENV_ROOT/artifacts" "$SCP_LOG" "$SSH_LOG" >/dev/null; then
	echo "forwarder token leaked to output, report, or scp args" >&2
	exit 1
fi
