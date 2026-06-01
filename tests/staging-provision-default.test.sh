#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
OBJECT_ROOT="$TMP/object-storage"
DEPLOY_LOG="$TMP/deploy.log"
GO_ARGS="$TMP/go.args"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/topology" \
	"$ENV_ROOT/services/video-cloud" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/artifacts" \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_video_cloud/tools/godaddy-dns" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode" \
	"$OBJECT_ROOT/test-bucket/releases/rtk_video_cloud-video-test" \
	"$OBJECT_ROOT/test-bucket/releases/rtk_account_manager-account-test" \
	"$OBJECT_ROOT/test-bucket/releases/rtk_cloud_admin-admin-test"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
*"/healthz"|*"/version"|*"/v1/health"|*"/api/service-health"*)
	printf 'ok\n'
	exit 0
	;;
esac
case "$*" in
*"Authorization: Bearer test-token"*) ;;
*)
	printf 'missing operator token: %s\n' "$*" >&2
	exit 22
	;;
esac
case "$*" in
*"/linode/instances?page_size=500"*)
	printf '{"data":[{"id":1,"label":"video-cloud-ci-edge","status":"running"}]}\n'
	;;
*"/networking/firewalls?page_size=500"*)
	printf '{"data":[]}\n'
	;;
*"/vpcs?page_size=500"*)
	printf '{"data":[]}\n'
	;;
*"/networking/firewalls/"*"/rules"*)
	printf '{"inbound":[{"label":"ssh","action":"ACCEPT","protocol":"TCP","ports":"22","addresses":{"ipv4":[]}}]}\n'
	;;
*"-X PUT https://api.linode.com/v4/networking/firewalls/"*"/rules"*)
	printf '{}\n'
	;;
*)
	printf 'unexpected curl: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

cat > "$FAKE_BIN/go" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$GO_ARGS"
if [[ "$PWD" == */repos/rtk_video_cloud/linode_deploy && "$*" == *" ./cmd/linode-deploy apply "* ]]; then
	config=""
	while [[ "$#" -gt 0 ]]; do
		if [[ "$1" == "--config" ]]; then
			config="$2"
			break
		fi
		shift
	done
	state="${config%/topology/*}/state/video-cloud-staging.state.json"
	mkdir -p "$(dirname "$state")"
	cat > "$state" <<'JSON'
{
  "stack": "video-cloud-ci",
  "region": "us-sea",
  "vpc_id": 9001,
  "subnet_id": 9002,
  "firewalls": {"edge": 101, "api": 102, "infra": 103, "mqtt": 104, "coturn": 105},
  "instances": {
    "edge": {"id": 1, "label": "video-cloud-ci-edge", "public_ipv4": "203.0.113.5", "private_ip": "10.42.1.5"},
    "api": {"id": 2, "label": "video-cloud-ci-api", "private_ip": "10.42.1.10"},
    "infra": {"id": 3, "label": "video-cloud-ci-infra", "private_ip": "10.42.1.30"},
    "mqtt": {"id": 4, "label": "video-cloud-ci-mqtt", "public_ipv4": "203.0.113.40", "private_ip": "10.42.1.40"},
    "coturn": {"id": 5, "label": "video-cloud-ci-coturn", "public_ipv4": "203.0.113.50"}
  }
}
JSON
fi
exit 0
SH
chmod +x "$FAKE_BIN/go"

cat > "$FAKE_BIN/dig" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *" NS "* || "$*" == NS\ * ]]; then
	printf 'ns1.example.test.\n'
elif [[ "$*" == *"account-manager.video-cloud-ci.example.test"* ]]; then
	printf '203.0.113.60\n'
elif [[ "$*" == *"admin.video-cloud-ci.example.test"* ]]; then
	printf '203.0.113.70\n'
else
	printf '203.0.113.5\n'
fi
SH
chmod +x "$FAKE_BIN/dig"

cat > "$FAKE_BIN/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$FAKE_BIN/sleep"

for prefix in rtk_video_cloud rtk_account_manager rtk_cloud_admin; do
	case "$prefix" in
	rtk_video_cloud) version=video-test ;;
	rtk_account_manager) version=account-test ;;
	rtk_cloud_admin) version=admin-test ;;
	esac
	cat > "$OBJECT_ROOT/test-bucket/releases/$prefix-$version/manifest.json" <<EOF_MANIFEST
{"version":"$version","artifact_path":"releases/$prefix-$version/$version.tar.gz"}
EOF_MANIFEST
	printf 'bundle\n' > "$OBJECT_ROOT/test-bucket/releases/$prefix-$version/$version.tar.gz"
done

cat > "$ENV_ROOT/env/operator.env" <<EOF_ENV
LINODE_TOKEN=test-token
GODADDY_KEY=test-key
GODADDY_SECRET=test-secret
LINODE_OBJ_BUCKET=test-bucket
LINODE_OBJ_ENDPOINT=file://$OBJECT_ROOT
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

touch "$ENV_ROOT/topology/video-cloud-staging.yaml"
touch "$ENV_ROOT/services/video-cloud/video-cloud-staging.env"
cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_AM_ENV'
ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS=198.51.100.10/32
EOF_AM_ENV
cat > "$ENV_ROOT/services/cloud-admin/admin-staging.env" <<'EOF_ADMIN_ENV'
ADMIN_LINODE_ALLOWED_SSH_CIDRS=198.51.100.10/32
VIDEO_CLOUD_PROMETHEUS_BASE_URL=http://10.42.1.30:9090
EOF_ADMIN_ENV

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
{
  "stack": "video-cloud-ci",
  "region": "us-sea",
  "vpc_id": 9001,
  "subnet_id": 9002,
  "firewalls": {"edge": 101, "api": 102, "infra": 103, "mqtt": 104, "coturn": 105},
  "instances": {
    "edge": {"id": 1, "label": "video-cloud-ci-edge", "public_ipv4": "203.0.113.5", "private_ip": "10.42.1.5"},
    "api": {"id": 2, "label": "video-cloud-ci-api", "private_ip": "10.42.1.10"},
    "infra": {"id": 3, "label": "video-cloud-ci-infra", "private_ip": "10.42.1.30"},
    "mqtt": {"id": 4, "label": "video-cloud-ci-mqtt", "public_ipv4": "203.0.113.40", "private_ip": "10.42.1.40"},
    "coturn": {"id": 5, "label": "video-cloud-ci-coturn", "public_ipv4": "203.0.113.50"}
  }
}
EOF_STATE
cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_ID=6
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-ci
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.60
ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4=10.42.1.50
ACCOUNT_MANAGER_LINODE_FIREWALL_ID=106
EOF_AM
cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_ID=7
ADMIN_LINODE_LABEL=rtk-cloud-admin-ci
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.70
ADMIN_LINODE_PRIVATE_IPV4=10.42.1.60
ADMIN_LINODE_FIREWALL_ID=107
EOF_ADMIN

touch "$TMP/id_ed25519" "$TMP/id_ed25519.pub"

cat > "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'video %s\n' "$*" >> "$DEPLOY_LOG"
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'account %s\n' "${ACCOUNT_MANAGER_LINODE_RELEASE:-}" >> "$DEPLOY_LOG"
SH
cat > "$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'admin %s %s\n' "${ADMIN_LINODE_RELEASE:-}" "${VIDEO_CLOUD_PROMETHEUS_BASE_URL:-}" >> "$DEPLOY_LOG"
SH
cat > "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
SH
chmod +x \
	"$WORKSPACE/repos/rtk_video_cloud/linode_deploy/scripts/deploy-staging.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/deploy-public-vm.sh" \
	"$WORKSPACE/repos/rtk_account_manager/linode_deploy/scripts/verify-public-vm.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/deploy-admin.sh" \
	"$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/verify-admin.sh"

if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" \
RTK_CLOUD_GO="$FAKE_BIN/go" \
GO_ARGS="$GO_ARGS" \
DEPLOY_LOG="$DEPLOY_LOG" \
"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- provision \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--ssh-key "$TMP/id_ed25519" >"$OUT" 2>&1

grep -F 'Target instances:' "$OUT" >/dev/null
grep -F 'Intended resources:' "$OUT" >/dev/null
grep -F 'video-cloud-ci-edge/api/infra/mqtt/coturn' "$OUT" >/dev/null
grep -F 'video --stack video-cloud-ci --gateway-domain video-cloud-ci.example.test' "$DEPLOY_LOG" >/dev/null
grep -F 'account account-test' "$DEPLOY_LOG" >/dev/null
grep -F 'admin admin-test http://10.42.1.30:9090' "$DEPLOY_LOG" >/dev/null
grep -F 'records upsert example.test --type A --name video-cloud-ci --data 203.0.113.5 --ttl 600' "$GO_ARGS" >/dev/null
find "$ENV_ROOT/artifacts" -path '*provision-report.md' | grep -q .
find "$ENV_ROOT/artifacts" -path '*e2e-report.md' | grep -q .
