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
[[ "$(cloud_env_video_config "$default_root")" == "$expected_default/topology/video-cloud-staging.yaml" ]]
[[ "$(cloud_env_test_devices_dir "$default_root")" == "$expected_default/devices/test_device" ]]
