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

cloud_env_environment_env() {
	printf '%s/env/environment.env\n' "$1"
}

cloud_env_first_existing() {
	local first="$1"
	local second="${2:-}"
	if [[ -n "$first" && -e "$first" ]]; then
		printf '%s\n' "$first"
	elif [[ -n "$second" ]]; then
		printf '%s\n' "$second"
	else
		printf '%s\n' "$first"
	fi
}

cloud_env_video_config() {
	cloud_env_first_existing "$1/topology/video-cloud.yaml" "$1/topology/video-cloud-staging.yaml"
}

cloud_env_video_env() {
	cloud_env_first_existing "$1/services/video-cloud/video-cloud.env" "$1/services/video-cloud/video-cloud-staging.env"
}

cloud_env_account_manager_env() {
	cloud_env_first_existing "$1/services/account-manager/account-manager.env" "$1/services/account-manager/account-manager-public-staging.env"
}

cloud_env_account_manager_platform_admin_env() {
	cloud_env_first_existing "$1/services/account-manager/platform-admin.env" "$1/services/account-manager/account-manager-platform-admin.env"
}

cloud_env_admin_env() {
	cloud_env_first_existing "$1/services/cloud-admin/admin.env" "$1/services/cloud-admin/admin-staging.env"
}

cloud_env_video_state() {
	cloud_env_first_existing "$1/state/video-cloud.state.json" "$1/state/video-cloud-staging.state.json"
}

cloud_env_account_manager_state() {
	cloud_env_first_existing "$1/state/account-manager.env" "$1/state/account-manager-staging.env"
}

cloud_env_admin_state() {
	cloud_env_first_existing "$1/state/cloud-admin.env" "$1/state/cloud-admin-staging.env"
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

cloud_env_name_from_root() {
	local env_root="$1"
	if [[ "$(basename "$env_root")" == "linode" ]]; then
		basename "$(dirname "$env_root")"
	else
		basename "$env_root"
	fi
}

cloud_env_die() {
	printf 'error: %s\n' "$*" >&2
	return 1
}

cloud_env_require_var() {
	local name="$1"
	local value="${!name:-}"
	[[ -n "$value" ]] || cloud_env_die "required environment metadata missing: $name"
}

cloud_env_load_environment() {
	local env_root="$1"
	local dns_override="${2:-}"
	local metadata_file
	metadata_file="$(cloud_env_environment_env "$env_root")"
	local metadata_dns=""
	if [[ -f "$metadata_file" ]]; then
		set -a
		# shellcheck source=/dev/null
		. "$metadata_file"
		set +a
		metadata_dns="${CLOUD_DNS_ROOT_DOMAIN:-}"
	fi

	CLOUD_ENV_NAME="${CLOUD_ENV_NAME:-$(cloud_env_name_from_root "$env_root")}"
	CLOUD_PROVIDER="${CLOUD_PROVIDER:-linode}"
	CLOUD_REGION="${CLOUD_REGION:-us-sea}"
	if [[ -n "$dns_override" && -n "$metadata_dns" && "$dns_override" != "$metadata_dns" ]]; then
		cloud_env_die "--dns-root-domain $dns_override does not match $(cloud_env_environment_env "$env_root") CLOUD_DNS_ROOT_DOMAIN=$metadata_dns"
		return 1
	fi
	CLOUD_DNS_ROOT_DOMAIN="${metadata_dns:-${dns_override:-${CLOUD_DNS_ROOT_DOMAIN:-realtekconnect.com}}}"
	CLOUD_STACK_NAME="${CLOUD_STACK_NAME:-video-cloud-$CLOUD_ENV_NAME}"
	VIDEO_CLOUD_DOMAIN="${VIDEO_CLOUD_DOMAIN:-$CLOUD_STACK_NAME.$CLOUD_DNS_ROOT_DOMAIN}"
	VIDEO_CLOUD_CERTISSUER_DOMAIN="${VIDEO_CLOUD_CERTISSUER_DOMAIN:-certissuer.$CLOUD_STACK_NAME.$CLOUD_DNS_ROOT_DOMAIN}"
	ACCOUNT_MANAGER_DOMAIN="${ACCOUNT_MANAGER_DOMAIN:-account-manager.$CLOUD_STACK_NAME.$CLOUD_DNS_ROOT_DOMAIN}"
	CLOUD_ADMIN_DOMAIN="${CLOUD_ADMIN_DOMAIN:-admin.$CLOUD_STACK_NAME.$CLOUD_DNS_ROOT_DOMAIN}"
	VIDEO_CLOUD_LABEL_PREFIX="${VIDEO_CLOUD_LABEL_PREFIX:-$CLOUD_STACK_NAME}"
	VIDEO_CLOUD_VPC_LABEL="${VIDEO_CLOUD_VPC_LABEL:-$VIDEO_CLOUD_LABEL_PREFIX-vpc}"
	VIDEO_CLOUD_SUBNET_LABEL="${VIDEO_CLOUD_SUBNET_LABEL:-$VIDEO_CLOUD_LABEL_PREFIX-subnet}"
	ACCOUNT_MANAGER_LINODE_LABEL="${ACCOUNT_MANAGER_LINODE_LABEL:-rtk-account-manager-$CLOUD_ENV_NAME}"
	ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL="${ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL:-$ACCOUNT_MANAGER_LINODE_LABEL-fw}"
	ADMIN_LINODE_LABEL="${ADMIN_LINODE_LABEL:-rtk-cloud-admin-$CLOUD_ENV_NAME}"
	ADMIN_LINODE_FIREWALL_LABEL="${ADMIN_LINODE_FIREWALL_LABEL:-$ADMIN_LINODE_LABEL-firewall}"

	if [[ -f "$metadata_file" ]]; then
		for name in \
			CLOUD_ENV_NAME CLOUD_PROVIDER CLOUD_REGION CLOUD_DNS_ROOT_DOMAIN CLOUD_STACK_NAME \
			VIDEO_CLOUD_DOMAIN VIDEO_CLOUD_CERTISSUER_DOMAIN ACCOUNT_MANAGER_DOMAIN CLOUD_ADMIN_DOMAIN \
			VIDEO_CLOUD_LABEL_PREFIX VIDEO_CLOUD_VPC_LABEL VIDEO_CLOUD_SUBNET_LABEL \
			ACCOUNT_MANAGER_LINODE_LABEL ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL \
			ADMIN_LINODE_LABEL ADMIN_LINODE_FIREWALL_LABEL
		do
			cloud_env_require_var "$name" || return 1
		done
	fi
	[[ "$CLOUD_PROVIDER" == "linode" ]] || {
		cloud_env_die "unsupported CLOUD_PROVIDER=$CLOUD_PROVIDER"
		return 1
	}
}

cloud_env_file_var() {
	local file="$1"
	local key="$2"
	[[ -f "$file" ]] || return 0
	(
		set -a
		# shellcheck source=/dev/null
		. "$file"
		set +a
		printf '%s\n' "${!key:-}"
	)
}

cloud_env_yaml_top_value() {
	local file="$1"
	local key="$2"
	[[ -f "$file" ]] || return 0
	awk -F ':' -v key="$key" '$1 == key {sub(/^[[:space:]]+/, "", $2); print $2; exit}' "$file"
}

cloud_env_video_gateway_domain() {
	local file="$1"
	[[ -f "$file" ]] || return 0
	awk '
		/^instances:/ {in_instances=1}
		in_instances && /^  edge:/ {in_edge=1; next}
		in_edge && /^  [A-Za-z0-9_-]+:/ && $0 !~ /^  edge:/ {in_edge=0; in_le=0}
		in_edge && /^    letsencrypt:/ {in_le=1; next}
		in_le && /^    [A-Za-z0-9_-]+:/ && $0 !~ /^    letsencrypt:/ {in_le=0}
		in_edge && in_le && /^      domain:/ {sub(/^[^:]+:[[:space:]]*/, ""); print; exit}
	' "$file"
}

cloud_env_video_deploy_value() {
	local file="$1"
	local key="$2"
	[[ -f "$file" ]] || return 0
	awk -F ':' -v key="$key" '
		/^deploy:/ {in_deploy=1; next}
		in_deploy && /^[^[:space:]]/ {in_deploy=0}
		in_deploy && $1 ~ "^[[:space:]]*" key "$" {
			sub(/^[[:space:]]+/, "", $2)
			print $2
			exit
		}
	' "$file"
}

cloud_env_assert_equal_if_set() {
	local label="$1"
	local expected="$2"
	local actual="$3"
	[[ -z "$actual" || "$actual" == "null" || "$actual" == "$expected" ]] && return 0
	cloud_env_die "$label mismatch: expected $expected, got $actual"
}

cloud_env_validate_environment() {
	local env_root="$1"
	local video_config am_env admin_env value
	video_config="$(cloud_env_video_config "$env_root")"
	am_env="$(cloud_env_account_manager_env "$env_root")"
	admin_env="$(cloud_env_admin_env "$env_root")"

	value="$(cloud_env_yaml_top_value "$video_config" stack)"
	cloud_env_assert_equal_if_set "topology stack" "$CLOUD_STACK_NAME" "$value" || return 1
	value="$(cloud_env_yaml_top_value "$video_config" region)"
	cloud_env_assert_equal_if_set "topology region" "$CLOUD_REGION" "$value" || return 1
	value="$(cloud_env_video_gateway_domain "$video_config")"
	cloud_env_assert_equal_if_set "video gateway domain" "$VIDEO_CLOUD_DOMAIN" "$value" || return 1
	value="$(cloud_env_video_deploy_value "$video_config" certissuer_domain)"
	cloud_env_assert_equal_if_set "video certissuer domain" "$VIDEO_CLOUD_CERTISSUER_DOMAIN" "$value" || return 1
	value="$(cloud_env_file_var "$am_env" ACCOUNT_MANAGER_LINODE_DOMAIN)"
	cloud_env_assert_equal_if_set "Account Manager domain" "$ACCOUNT_MANAGER_DOMAIN" "$value" || return 1
	value="$(cloud_env_file_var "$am_env" ACCOUNT_MANAGER_LINODE_LABEL)"
	cloud_env_assert_equal_if_set "Account Manager label" "$ACCOUNT_MANAGER_LINODE_LABEL" "$value" || return 1
	value="$(cloud_env_file_var "$am_env" ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL)"
	cloud_env_assert_equal_if_set "Account Manager firewall label" "$ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL" "$value" || return 1
	value="$(cloud_env_file_var "$admin_env" ADMIN_LINODE_DOMAIN)"
	cloud_env_assert_equal_if_set "Cloud Admin domain" "$CLOUD_ADMIN_DOMAIN" "$value" || return 1
	value="$(cloud_env_file_var "$admin_env" ADMIN_LINODE_LABEL)"
	cloud_env_assert_equal_if_set "Cloud Admin label" "$ADMIN_LINODE_LABEL" "$value" || return 1
	value="$(cloud_env_file_var "$admin_env" ADMIN_LINODE_FIREWALL_LABEL)"
	cloud_env_assert_equal_if_set "Cloud Admin firewall label" "$ADMIN_LINODE_FIREWALL_LABEL" "$value" || return 1
}

cloud_env_export_filter_vars() {
	export CLOUD_STACK_NAME VIDEO_CLOUD_LABEL_PREFIX VIDEO_CLOUD_VPC_LABEL VIDEO_CLOUD_SUBNET_LABEL
	export ACCOUNT_MANAGER_LINODE_LABEL ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL
	export ADMIN_LINODE_LABEL ADMIN_LINODE_FIREWALL_LABEL
}

cloud_env_video_role_label() {
	local role="$1"
	printf '%s-%s\n' "$VIDEO_CLOUD_LABEL_PREFIX" "$role"
}
