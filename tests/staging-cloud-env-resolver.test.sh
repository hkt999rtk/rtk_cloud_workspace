#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "$ROOT/scripts/lib/cloud-env.sh"

WORKSPACE="$(mktemp -d)"
trap 'rm -rf "$WORKSPACE"' EXIT

default_root="$(cloud_env_init "$WORKSPACE" "")"
expected_default="$WORKSPACE/cloud_env/staging/linode"
[[ "$default_root" == "$expected_default" ]] || {
	echo "expected default env root $expected_default, got $default_root" >&2
	exit 1
}

mkdir -p "$WORKSPACE/custom/env-root"
override_root="$(cloud_env_init "$WORKSPACE" "$WORKSPACE/custom/env-root")"
[[ "$override_root" == "$WORKSPACE/custom/env-root" ]] || {
	echo "expected override env root $WORKSPACE/custom/env-root, got $override_root" >&2
	exit 1
}

mkdir -p "$WORKSPACE/cloud_env/staging/linode/services"
staging_root="$(cloud_env_init "$WORKSPACE" "$WORKSPACE/cloud_env/staging")"
[[ "$staging_root" == "$WORKSPACE/cloud_env/staging/linode" ]] || {
	echo "expected staging env root to resolve to $WORKSPACE/cloud_env/staging/linode, got $staging_root" >&2
	exit 1
}

[[ "$(cloud_env_operator_env "$default_root")" == "$expected_default/env/operator.env" ]]
[[ "$(cloud_env_stack_env "$default_root")" == "$expected_default/env/stack.env" ]]
[[ "$(cloud_env_video_config "$default_root")" == "$expected_default/topology/video-cloud-staging.yaml" ]]
[[ "$(cloud_env_test_devices_dir "$default_root")" == "$expected_default/devices/test_device" ]]

metadata_root="$WORKSPACE/metadata/staging/linode"
mkdir -p \
	"$metadata_root/env" \
	"$metadata_root/topology" \
	"$metadata_root/services/account-manager" \
	"$metadata_root/services/cloud-admin"

cat > "$metadata_root/env/stack.env" <<'EOF_ENV'
CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
VIDEO_CLOUD_DOMAIN=video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-staging.realtekconnect.com
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-staging
VIDEO_CLOUD_VPC_LABEL=video-cloud-staging-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-staging-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
EOF_ENV

cat > "$metadata_root/topology/video-cloud-staging.yaml" <<'EOF_TOPOLOGY'
stack: video-cloud-staging
region: us-sea
vpc:
  label: video-cloud-staging-vpc
  subnet:
    label: video-cloud-staging-subnet
instances:
  edge:
    label: video-cloud-staging-edge
    letsencrypt:
      domain: video-cloud-staging.realtekconnect.com
  api:
    label: video-cloud-staging-api
  infra:
    label: video-cloud-staging-infra
  mqtt:
    label: video-cloud-staging-mqtt
  coturn:
    label: video-cloud-staging-coturn
deploy:
  certissuer_domain: certissuer.video-cloud-staging.realtekconnect.com
EOF_TOPOLOGY

cat > "$metadata_root/services/account-manager/account-manager-public-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com
EOF_AM

cat > "$metadata_root/services/cloud-admin/admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
ADMIN_LINODE_DOMAIN=admin.video-cloud-staging.realtekconnect.com
EOF_ADMIN

(
	cloud_env_load_environment "$metadata_root" ""
	[[ "$CLOUD_STACK_NAME" == "video-cloud-staging" ]]
	[[ "$VIDEO_CLOUD_DOMAIN" == "video-cloud-staging.realtekconnect.com" ]]
	[[ "$ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL" == "rtk-account-manager-staging-fw" ]]
	cloud_env_validate_environment "$metadata_root"
)

cp "$metadata_root/env/stack.env" "$metadata_root/env/stack.env.good"
sed 's/account-manager.video-cloud-staging.realtekconnect.com/account-manager-mismatch.realtekconnect.com/' \
	"$metadata_root/env/stack.env.good" > "$metadata_root/env/stack.env"
if (
	cloud_env_load_environment "$metadata_root" ""
	cloud_env_validate_environment "$metadata_root"
) >"$WORKSPACE/mismatch.out" 2>&1; then
	echo "expected metadata mismatch to fail" >&2
	exit 1
fi
grep -F 'Account Manager domain mismatch' "$WORKSPACE/mismatch.out" >/dev/null
