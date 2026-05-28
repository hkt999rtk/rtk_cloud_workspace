#!/usr/bin/env bash

cloud_env_default_root() {
	local workspace="$1"
	printf '%s/cloud_env/staging/linode\n' "$workspace"
}

cloud_env_abs_path() {
	local path="$1"
	if [[ -d "$path" ]]; then
		(cd "$path" && pwd)
		return
	fi
	local parent base
	parent="$(dirname "$path")"
	base="$(basename "$path")"
	if [[ -d "$parent" ]]; then
		printf '%s/%s\n' "$(cd "$parent" && pwd)" "$base"
	else
		printf '%s\n' "$path"
	fi
}

cloud_env_init() {
	local workspace="$1"
	local env_root="${2:-}"
	if [[ -z "$env_root" ]]; then
		env_root="$(cloud_env_default_root "$workspace")"
	fi
	env_root="$(cloud_env_abs_path "$env_root")"
	if [[ "$(basename "$env_root")" == "staging" && ! -d "$env_root/services" && ! -d "$env_root/env" && ! -d "$env_root/topology" ]]; then
		env_root="$(cloud_env_abs_path "$env_root/linode")"
	elif [[ -d "$env_root/linode" && ! -d "$env_root/services" && ! -d "$env_root/env" && ! -d "$env_root/topology" ]]; then
		env_root="$(cloud_env_abs_path "$env_root/linode")"
	fi
	printf '%s\n' "$env_root"
}

cloud_env_operator_env() {
	printf '%s/env/operator.env\n' "$1"
}

cloud_env_video_config() {
	printf '%s/topology/video-cloud-staging.yaml\n' "$1"
}

cloud_env_video_env() {
	printf '%s/services/video-cloud/video-cloud-staging.env\n' "$1"
}

cloud_env_account_manager_env() {
	printf '%s/services/account-manager/account-manager-public-staging.env\n' "$1"
}

cloud_env_account_manager_platform_admin_env() {
	printf '%s/services/account-manager/account-manager-platform-admin.env\n' "$1"
}

cloud_env_admin_env() {
	printf '%s/services/cloud-admin/admin-staging.env\n' "$1"
}

cloud_env_video_state() {
	printf '%s/state/video-cloud-staging.state.json\n' "$1"
}

cloud_env_account_manager_state() {
	printf '%s/state/account-manager-staging.env\n' "$1"
}

cloud_env_admin_state() {
	printf '%s/state/cloud-admin-staging.env\n' "$1"
}

cloud_env_artifacts_dir() {
	printf '%s/artifacts\n' "$1"
}

cloud_env_keys_dir() {
	printf '%s/keys\n' "$1"
}

cloud_env_certificates_dir() {
	printf '%s/certificates\n' "$1"
}

cloud_env_test_devices_dir() {
	printf '%s/devices/test_device\n' "$1"
}
