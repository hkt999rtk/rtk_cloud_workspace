#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
OPERATOR_ENV=""
MATCH_TEXT="staging"

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[staging-remove-all-vm] %s\n' "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/staging-remove-all-vm.sh [options]

Options:
  --operator-env PATH   Default: <workspace>/.secrets/staging/linode/video-cloud/env/operator.env.
  -h, --help            Show this help.

Safety:
  The script asks for an interactive "yes" before reading credentials or calling
  the Linode API. It deletes only Linode VMs whose label contains "staging".
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

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

need_cmd curl
need_cmd jq

OPERATOR_ENV="${OPERATOR_ENV:-$WORKSPACE/.secrets/staging/linode/video-cloud/env/operator.env}"
load_env_file "$OPERATOR_ENV"
[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"

tmp="$(mktemp -d /tmp/rtk-staging-remove-vm.XXXXXX)"
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
	linode_api DELETE "/linode/instances/$id" >/dev/null
done < "$tmp/targets.tsv"

log "delete requests submitted"
