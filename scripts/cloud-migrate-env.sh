#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_ROOT=""
FORCE=0
START_EPOCH="$(date +%s)"

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	local now elapsed
	now="$(date +%H:%M:%S)"
	elapsed=$(($(date +%s) - START_EPOCH))
	printf '[cloud-env-migrate %s +%03ds] %s\n' "$now" "$elapsed" "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-migrate-env.sh [options]

Options:
  --workspace PATH   Default: script parent workspace.
  --env-root PATH    Required environment directory, for example cloud_env/staging.
  --force            Overwrite existing target files.
  -h, --help         Show this help.

Copies current local staging environment files from the legacy scattered
locations into cloud_env/staging/linode. Source files are left in place. A
timestamped backup and migration manifest are written under <env-root>/backups.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--force) FORCE=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

sha256_file() {
	local file="$1"
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$file" | awk '{print $1}'
	else
		sha256sum "$file" | awk '{print $1}'
	fi
}

rel_path() {
	local path="$1"
	case "$path" in
	"$WORKSPACE"/*) printf '%s\n' "${path#$WORKSPACE/}" ;;
	*) printf '%s\n' "$path" ;;
	esac
}

record_manifest() {
	local kind="$1"
	local src="$2"
	local dst="$3"
	local status="$4"
	local sha="${5:-}"
	jq -cn \
		--arg ts "$STAMP" \
		--arg kind "$kind" \
		--arg src "$(rel_path "$src")" \
		--arg dst "$(rel_path "$dst")" \
		--arg status "$status" \
		--arg sha "$sha" \
		'{timestamp:$ts, kind:$kind, source:$src, target:$dst, status:$status, sha256:($sha | select(. != ""))}' >> "$MANIFEST_JSONL"
	printf '%s\t%s\t%s\t%s\t%s\n' "$kind" "$(rel_path "$src")" "$(rel_path "$dst")" "$status" "$sha" >> "$MANIFEST_TSV"
}

backup_source_file() {
	local src="$1"
	local backup="$BACKUP_DIR/legacy/$(rel_path "$src")"
	mkdir -p "$(dirname "$backup")"
	cp -p "$src" "$backup"
}

copy_file() {
	local src="$1"
	local dst="$2"
	local kind="${3:-file}"
	if [[ ! -f "$src" ]]; then
		record_manifest "$kind" "$src" "$dst" "missing"
		return 0
	fi
	if [[ -e "$dst" && "$FORCE" != "1" ]]; then
		record_manifest "$kind" "$src" "$dst" "skipped-existing" "$(sha256_file "$dst")"
		return 0
	fi
	mkdir -p "$(dirname "$dst")"
	cp -p "$src" "$dst"
	backup_source_file "$src"
	record_manifest "$kind" "$src" "$dst" "copied" "$(sha256_file "$dst")"
}

copy_dir_contents() {
	local src="$1"
	local dst="$2"
	local kind="$3"
	if [[ ! -d "$src" ]]; then
		record_manifest "$kind" "$src" "$dst" "missing"
		return 0
	fi
	find "$src" -type f | sort | while IFS= read -r file; do
		local rel="${file#$src/}"
		copy_file "$file" "$dst/$rel" "$kind"
	done
}

env_file_value() {
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

yaml_top_value() {
	local file="$1"
	local key="$2"
	[[ -f "$file" ]] || return 0
	awk -F ':' -v key="$key" '$1 == key {sub(/^[[:space:]]+/, "", $2); print $2; exit}' "$file"
}

yaml_path_value() {
	local file="$1"
	local path="$2"
	[[ -f "$file" ]] || return 0
	awk -v path="$path" '
	BEGIN {
		n = split(path, want, ".")
	}
	/^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
	{
		match($0, /^[ ]*/)
		indent = RLENGTH / 2
		line = $0
		sub(/^[ ]*/, "", line)
		key = line
		sub(/:.*/, "", key)
		stack[indent + 1] = key
		for (i = indent + 2; i <= 16; i++) {
			delete stack[i]
		}
		if (line !~ /^[^:]+:[[:space:]]*[^[:space:]]/) {
			next
		}
		if (indent + 1 != n) {
			next
		}
		for (i = 1; i <= n; i++) {
			if (stack[i] != want[i]) {
				next
			}
		}
		sub(/^[^:]+:[[:space:]]*/, "", line)
		print line
		exit
	}' "$file"
}

metadata_env_name() {
	if [[ "$(basename "$ENV_ROOT")" == "linode" ]]; then
		basename "$(dirname "$ENV_ROOT")"
	else
		basename "$ENV_ROOT"
	fi
}

write_environment_metadata() {
	local dst
	dst="$(cloud_env_stack_env "$ENV_ROOT")"
	if [[ -e "$dst" && "$FORCE" != "1" ]]; then
		record_manifest "stack-metadata" "$dst" "$dst" "skipped-existing" "$(sha256_file "$dst")"
		return 0
	fi

	local video_config am_env admin_env env_name provider region root_domain stack
	local video_domain certissuer_domain am_domain admin_domain
	local label_prefix vpc_label subnet_label am_label am_fw_label admin_label admin_fw_label
	video_config="$(cloud_env_video_config "$ENV_ROOT")"
	am_env="$(cloud_env_account_manager_env "$ENV_ROOT")"
	admin_env="$(cloud_env_admin_env "$ENV_ROOT")"

	env_name="$(metadata_env_name)"
	provider="linode"
	region="$(yaml_top_value "$video_config" region)"
	region="${region:-us-sea}"
	root_domain="${CLOUD_DNS_ROOT_DOMAIN:-realtekconnect.com}"
	stack="$(yaml_top_value "$video_config" stack)"
	stack="${stack:-video-cloud-$env_name}"

	video_domain="$(yaml_path_value "$video_config" instances.edge.letsencrypt.domain)"
	video_domain="${video_domain:-$stack.$root_domain}"
	certissuer_domain="$(yaml_path_value "$video_config" deploy.certissuer_domain)"
	certissuer_domain="${certissuer_domain:-certissuer.$stack.$root_domain}"
	am_domain="$(env_file_value "$am_env" ACCOUNT_MANAGER_LINODE_DOMAIN)"
	am_domain="${am_domain:-account-manager.$stack.$root_domain}"
	admin_domain="$(env_file_value "$admin_env" ADMIN_LINODE_DOMAIN)"
	admin_domain="${admin_domain:-admin.$stack.$root_domain}"

	label_prefix="$stack"
	vpc_label="$(yaml_path_value "$video_config" vpc.label)"
	vpc_label="${vpc_label:-$label_prefix-vpc}"
	subnet_label="$(yaml_path_value "$video_config" vpc.subnet.label)"
	subnet_label="${subnet_label:-$label_prefix-subnet}"
	am_label="$(env_file_value "$am_env" ACCOUNT_MANAGER_LINODE_LABEL)"
	am_label="${am_label:-rtk-account-manager-$env_name}"
	am_fw_label="$(env_file_value "$am_env" ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL)"
	am_fw_label="${am_fw_label:-$am_label-fw}"
	admin_label="$(env_file_value "$admin_env" ADMIN_LINODE_LABEL)"
	admin_label="${admin_label:-rtk-cloud-admin-$env_name}"
	admin_fw_label="$(env_file_value "$admin_env" ADMIN_LINODE_FIREWALL_LABEL)"
	admin_fw_label="${admin_fw_label:-$admin_label-firewall}"

	mkdir -p "$(dirname "$dst")"
	{
		printf 'CLOUD_ENV_NAME=%s\n' "$env_name"
		printf 'CLOUD_PROVIDER=%s\n' "$provider"
		printf 'CLOUD_REGION=%s\n' "$region"
		printf 'CLOUD_DNS_ROOT_DOMAIN=%s\n' "$root_domain"
		printf 'CLOUD_STACK_NAME=%s\n' "$stack"
		printf '\n'
		printf 'VIDEO_CLOUD_DOMAIN=%s\n' "$video_domain"
		printf 'VIDEO_CLOUD_CERTISSUER_DOMAIN=%s\n' "$certissuer_domain"
		printf 'ACCOUNT_MANAGER_DOMAIN=%s\n' "$am_domain"
		printf 'CLOUD_ADMIN_DOMAIN=%s\n' "$admin_domain"
		printf '\n'
		printf 'VIDEO_CLOUD_LABEL_PREFIX=%s\n' "$label_prefix"
		printf 'VIDEO_CLOUD_VPC_LABEL=%s\n' "$vpc_label"
		printf 'VIDEO_CLOUD_SUBNET_LABEL=%s\n' "$subnet_label"
		printf 'ACCOUNT_MANAGER_LINODE_LABEL=%s\n' "$am_label"
		printf 'ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=%s\n' "$am_fw_label"
		printf 'ADMIN_LINODE_LABEL=%s\n' "$admin_label"
		printf 'ADMIN_LINODE_FIREWALL_LABEL=%s\n' "$admin_fw_label"
	} > "$dst"
	record_manifest "stack-metadata" "$dst" "$dst" "generated" "$(sha256_file "$dst")"
}

need_cmd jq
WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="$ENV_ROOT/backups/migration-$STAMP"
MANIFEST_JSONL="$BACKUP_DIR/migration-manifest.jsonl"
MANIFEST_TSV="$BACKUP_DIR/migration-manifest.tsv"
mkdir -p "$BACKUP_DIR"
printf 'kind\tsource\ttarget\tstatus\tsha256\n' > "$MANIFEST_TSV"
: > "$MANIFEST_JSONL"

log "workspace=$WORKSPACE"
log "env_root=$ENV_ROOT"
log "backup=$BACKUP_DIR"

copy_file "$WORKSPACE/.secrets/staging/linode/video-cloud/env/operator.env" "$(cloud_env_operator_env "$ENV_ROOT")" "operator-env"
copy_file "$WORKSPACE/.secrets/staging/linode/video-cloud/config/video-cloud-staging.yaml" "$(cloud_env_video_config "$ENV_ROOT")" "topology"
copy_file "$WORKSPACE/.secrets/staging/linode/video-cloud/env/video-cloud-staging.env" "$(cloud_env_video_env "$ENV_ROOT")" "service-env"
copy_file "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env" "$(cloud_env_account_manager_env "$ENV_ROOT")" "service-env"
copy_file "$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-platform-admin.env" "$(cloud_env_account_manager_platform_admin_env "$ENV_ROOT")" "service-env"
copy_file "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env" "$(cloud_env_admin_env "$ENV_ROOT")" "service-env"
if [[ -f "$WORKSPACE/.secrets/staging/linode/video-cloud/env/stack.env" ]]; then
	copy_file "$WORKSPACE/.secrets/staging/linode/video-cloud/env/stack.env" "$(cloud_env_stack_env "$ENV_ROOT")" "stack-metadata"
elif [[ -f "$WORKSPACE/.secrets/staging/linode/video-cloud/env/environment.env" ]]; then
	copy_file "$WORKSPACE/.secrets/staging/linode/video-cloud/env/environment.env" "$(cloud_env_stack_env "$ENV_ROOT")" "stack-metadata"
else
	write_environment_metadata
fi

if [[ -f "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json" ]]; then
	copy_file "$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json" "$(cloud_env_video_state "$ENV_ROOT")" "state"
else
	copy_file "$WORKSPACE/.secrets/staging/linode/video-cloud/state/video-cloud-staging.state.json" "$(cloud_env_video_state "$ENV_ROOT")" "state"
fi
copy_file "$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env" "$(cloud_env_account_manager_state "$ENV_ROOT")" "state"
copy_file "$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state" "$(cloud_env_admin_state "$ENV_ROOT")" "state"

copy_dir_contents "$WORKSPACE/keys/staging/linode" "$(cloud_env_keys_dir "$ENV_ROOT")" "keys"
copy_dir_contents "$WORKSPACE/keys/test_device" "$(cloud_env_test_devices_dir "$ENV_ROOT")" "devices"
copy_dir_contents "$WORKSPACE/.secrets/staging/linode/video-cloud/artifacts" "$(cloud_env_artifacts_dir "$ENV_ROOT")" "artifacts"

log "migration manifest: $MANIFEST_TSV"
printf 'env_root=%s\n' "$ENV_ROOT"
printf 'backup=%s\n' "$BACKUP_DIR"
printf 'manifest=%s\n' "$MANIFEST_TSV"
