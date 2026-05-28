#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
START_EPOCH="$(date +%s)"
ENV_ROOT=""
DEPRECATED_ENV_ROOT=""
BRANDNAME=""
JSON_OUTPUT=0
LIMIT=200

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	local now elapsed
	now="$(date +%H:%M:%S)"
	elapsed=$(($(date +%s) - START_EPOCH))
	printf '[staging-brand-cloud-list %s +%03ds] %s\n' "$now" "$elapsed" "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/staging_list_brandname_clouds.sh [options]

Options:
  --workspace PATH       Default: script parent workspace.
  --env-root PATH        Required environment directory, for example cloud_env/staging.
  --secrets-root PATH    Deprecated alias for --env-root.
  --brandname NAME       Show only a matching brand cloud name or metadata.brandname.
  --limit N              API list limit. Default: 200.
  --json                 Print the full API response JSON.
  -h, --help             Show this help.

Lists Account Manager brand clouds on the selected Linode staging environment.
The script logs in with that environment's platform-admin credentials and only
performs read-only Account Manager admin API calls.
USAGE
}

require_value() {
	local opt="$1"
	local value="${2:-}"
	[[ -n "$value" ]] || die "$opt requires a value"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) require_value "$1" "${2:-}"; WORKSPACE="$2"; shift 2 ;;
	--env-root) require_value "$1" "${2:-}"; ENV_ROOT="$2"; shift 2 ;;
	--secrets-root) require_value "$1" "${2:-}"; DEPRECATED_ENV_ROOT="$2"; ENV_ROOT="$2"; shift 2 ;;
	--brandname) require_value "$1" "${2:-}"; BRANDNAME="$2"; shift 2 ;;
	--limit) require_value "$1" "${2:-}"; LIMIT="$2"; shift 2 ;;
	--json) JSON_OUTPUT=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

load_env_file() {
	local path="$1"
	[[ -f "$path" ]] || die "required env file not found: $path"
	set -a
	# shellcheck source=/dev/null
	. "$path"
	set +a
}

curl_json_status() {
	local out="$1"
	shift
	curl -sS -o "$out" -w '%{http_code}' "$@"
}

login_platform_admin() {
	local login_payload="$TMPDIR/login.json"
	local login_out="$TMPDIR/login.out"
	log "logging in platform admin: $AM_BASE_URL/v1/auth/login"
	jq -cn \
		--arg email "$ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL" \
		--arg password "$ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD" \
		'{email:$email,password:$password}' > "$login_payload"
	local status
	status="$(curl_json_status "$login_out" \
		-H 'content-type: application/json' \
		--data-binary "@$login_payload" \
		"$AM_BASE_URL/v1/auth/login")"
	[[ "$status" == "200" ]] || die "platform admin login failed: HTTP $status"
	ACCESS_TOKEN="$(jq -r '.tokens.access_token // empty' "$login_out")"
	[[ -n "$ACCESS_TOKEN" ]] || die "platform admin login response did not include an access token"
	log "platform admin login ok"
}

list_brand_clouds() {
	local out="$TMPDIR/brand-clouds.out"
	local filtered="$TMPDIR/brand-clouds.filtered.json"
	local status
	log "listing brand clouds: limit=$LIMIT"
	status="$(curl_json_status "$out" \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		"$AM_BASE_URL/v1/admin/brand-clouds?limit=$LIMIT")"
	[[ "$status" == "200" ]] || die "brand cloud list failed: HTTP $status"

	if [[ -n "$BRANDNAME" ]]; then
		log "filtering brand clouds: brandname=$BRANDNAME"
		jq --arg name "$BRANDNAME" '
			.brand_clouds = [
				.brand_clouds[]?
				| select(.name == $name or (.metadata.brandname // "") == $name)
			]
			| .pagination.filtered_total = (.brand_clouds | length)
		' "$out" > "$filtered"
	else
		cp "$out" "$filtered"
	fi
	BRAND_CLOUDS_JSON="$filtered"
}

print_human_summary() {
	local listed_count api_total
	listed_count="$(jq -r '.brand_clouds | length' "$BRAND_CLOUDS_JSON")"
	api_total="$(jq -r '.pagination.total // (.brand_clouds | length)' "$BRAND_CLOUDS_JSON")"
	if [[ -n "$BRANDNAME" ]]; then
		printf 'brand_clouds=%s api_total=%s filter=%s\n' "$listed_count" "$api_total" "$BRANDNAME"
	else
		printf 'brand_clouds=%s api_total=%s\n' "$listed_count" "$api_total"
	fi
	printf '%-36s  %-24s  %-10s  %-12s  %-5s  %-16s  %-24s  %s\n' \
		'id' 'name' 'status' 'tier' 'quota' 'metadata.brandname' 'created_at' 'metadata'
	jq -r '
		.brand_clouds[]?
		| [
			.id,
			.name,
			.status,
			.tier,
			(.evaluation_device_quota | tostring),
			(.metadata.brandname // ""),
			.created_at,
			(.metadata // {} | tostring)
		]
		| @tsv
	' "$BRAND_CLOUDS_JSON" | awk -F '\t' '{
		printf "%-36s  %-24s  %-10s  %-12s  %-5s  %-16s  %-24s  %s\n", $1, $2, $3, $4, $5, $6, $7, $8
	}'
}

[[ "$LIMIT" =~ ^[0-9]+$ ]] || die "--limit must be a positive integer"
[[ "$LIMIT" -gt 0 ]] || die "--limit must be a positive integer"
[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"

log "start staging brand cloud list"
need_cmd curl
need_cmd jq

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
DEPRECATED_ENV_ROOT="$ENV_ROOT"
log "workspace=$WORKSPACE"
log "env_root=$ENV_ROOT"
AM_REPO="$WORKSPACE/repos/rtk_account_manager"
AM_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
AM_STATE="$(cloud_env_account_manager_state "$ENV_ROOT")"
AM_PLATFORM_ADMIN_ENV="$(cloud_env_account_manager_platform_admin_env "$ENV_ROOT")"

log "loading Account Manager staging env/state"
load_env_file "$AM_ENV"
load_env_file "$AM_STATE"
load_env_file "$AM_PLATFORM_ADMIN_ENV"

AM_DOMAIN="${ACCOUNT_MANAGER_LINODE_DOMAIN:-account-manager.video-cloud-staging.realtekconnect.com}"
AM_BASE_URL="https://$AM_DOMAIN"
TMPDIR="$(mktemp -d /tmp/rtk-brand-cloud-list.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT

login_platform_admin
list_brand_clouds

if [[ "$JSON_OUTPUT" == "1" ]]; then
	jq . "$BRAND_CLOUDS_JSON"
else
	print_human_summary
fi
log "complete: listed brand clouds"
