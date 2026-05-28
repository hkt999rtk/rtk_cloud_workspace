#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_ROOT=""
OPERATOR_ENV=""
MATCH_TEXT="staging"
VPC_LABEL="video-cloud-staging-vpc"

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[cloud-remove-all-vm] %s\n' "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-remove-all-vm.sh [options]

Options:
  --workspace PATH      Default: script parent workspace.
  --env-root PATH       Required environment directory, for example cloud_env/staging.
  --operator-env PATH   Default: <env-root>/env/operator.env.
  -h, --help            Show this help.

Safety:
  The script asks for an interactive "yes" before reading credentials or calling
  the Linode API. It deletes Linode VMs whose label contains "staging", then
  deletes the matching staging firewalls and Video Cloud staging VPC.
  After delete requests are submitted, active local state files for the staging
  VMs are moved to <env-root>/backups/remove-vm-<timestamp>/state.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"

printf 'Delete all Linode VMs whose label contains "%s"? Type yes to continue: ' "$MATCH_TEXT" >&2
read -r answer
if [[ "$answer" != "yes" ]]; then
	log "cancelled"
	exit 0
fi

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

load_env_file() {
	local path="$1"
	if [[ -f "$path" ]]; then
		set -a
		# shellcheck source=/dev/null
		. "$path"
		set +a
	fi
}

linode_api() {
	local method="$1"
	local path="$2"
	curl -fsS -X "$method" "https://api.linode.com/v4$path" \
		-H "Authorization: Bearer $LINODE_TOKEN" \
		-H 'Content-Type: application/json'
}

linode_delete_ignore_missing() {
	local path="$1"
	local err
	if err="$(linode_api DELETE "$path" 2>&1 >/dev/null)"; then
		return 0
	fi
	if [[ "$err" == *"404"* || "$err" == *"provided ID did not match"* || "$err" == *"not found"* ]]; then
		log "delete skipped; already gone: $path"
		return 0
	fi
	printf '%s\n' "$err" >&2
	return 1
}

wait_for_vm_deletion() {
	local remaining
	for _ in $(seq 1 60); do
		remaining="$(linode_api GET '/linode/instances?page_size=500' | jq -r --arg text "$MATCH_TEXT" '[.data[] | select(.label | contains($text))] | length')"
		[[ "$remaining" == "0" ]] && return 0
		sleep 10
	done
	die "timed out waiting for staging VM deletion"
}

remove_orphan_firewalls() {
	local firewalls tmp id label
	firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
	tmp="$1/firewalls.tsv"
	jq -r '
		.data[]
		| select(
			(.label | startswith("video-cloud-staging-"))
			or .label == "rtk-account-manager-staging-fw"
			or .label == "rtk-cloud-admin-staging-firewall"
		)
		| [.id,.label] | @tsv
	' <<<"$firewalls" > "$tmp"
	if [[ ! -s "$tmp" ]]; then
		log "no staging firewalls found"
		return 0
	fi
	log "deleting staging firewalls:"
	awk -F '\t' '{printf "  - %s (%s)\n", $2, $1}' "$tmp" >&2
	while IFS=$'\t' read -r id label; do
		[[ -n "$id" ]] || continue
		log "delete firewall $label ($id)"
		linode_delete_ignore_missing "/networking/firewalls/$id"
	done < "$tmp"
}

remove_orphan_vpcs() {
	local vpcs tmp id label
	vpcs="$(linode_api GET '/vpcs?page_size=500')"
	tmp="$1/vpcs.tsv"
	jq -r --arg label "$VPC_LABEL" '.data[] | select(.label == $label) | [.id,.label] | @tsv' <<<"$vpcs" > "$tmp"
	if [[ ! -s "$tmp" ]]; then
		log "no staging VPCs found"
		return 0
	fi
	log "deleting staging VPCs:"
	awk -F '\t' '{printf "  - %s (%s)\n", $2, $1}' "$tmp" >&2
	while IFS=$'\t' read -r id label; do
		[[ -n "$id" ]] || continue
		log "delete VPC $label ($id)"
		linode_delete_ignore_missing "/vpcs/$id"
	done < "$tmp"
}

backup_and_remove_state_files() {
	local backup_dir state_file
	backup_dir="$ENV_ROOT/backups/remove-vm-$(date -u +%Y%m%dT%H%M%SZ)/state"
	for state_file in \
		"$(cloud_env_video_state "$ENV_ROOT")" \
		"$(cloud_env_account_manager_state "$ENV_ROOT")" \
		"$(cloud_env_admin_state "$ENV_ROOT")"
	do
		if [[ -f "$state_file" ]]; then
			mkdir -p "$backup_dir"
			cp -p "$state_file" "$backup_dir/$(basename "$state_file")"
			rm -f "$state_file"
			log "removed local state: $state_file"
		fi
	done
	if [[ -d "$backup_dir" ]]; then
		log "local state backup: ${backup_dir%/state}"
	fi
}

need_cmd curl
need_cmd jq

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
OPERATOR_ENV="${OPERATOR_ENV:-$(cloud_env_operator_env "$ENV_ROOT")}"
load_env_file "$OPERATOR_ENV"
[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"

tmp="$(mktemp -d /tmp/rtk-cloud-remove-vm.XXXXXX)"
trap 'rm -rf "$tmp"' EXIT

instances="$(linode_api GET '/linode/instances?page_size=500')"
jq -r --arg text "$MATCH_TEXT" '.data[] | select(.label | contains($text)) | [.id,.label] | @tsv' <<<"$instances" > "$tmp/targets.tsv"

if [[ ! -s "$tmp/targets.tsv" ]]; then
	log "no VMs found with label containing: $MATCH_TEXT"
	exit 0
fi

log "deleting VMs:"
awk -F '\t' '{printf "  - %s (%s)\n", $2, $1}' "$tmp/targets.tsv" >&2

while IFS=$'\t' read -r id label; do
	[[ -n "$id" ]] || continue
	log "delete $label ($id)"
	linode_delete_ignore_missing "/linode/instances/$id"
done < "$tmp/targets.tsv"

log "VM delete requests submitted"
wait_for_vm_deletion
remove_orphan_firewalls "$tmp"
remove_orphan_vpcs "$tmp"
backup_and_remove_state_files
log "remove complete"
