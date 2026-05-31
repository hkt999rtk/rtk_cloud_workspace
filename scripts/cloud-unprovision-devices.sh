#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_ROOT=""
START_EPOCH="$(date +%s)"
BRANDNAME=""
BIND_ARTIFACT=""
COUNT=""
DRY_RUN=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	local now elapsed
	now="$(date +%H:%M:%S)"
	elapsed=$(($(date +%s) - START_EPOCH))
	printf '[cloud-unprovision-devices %s +%03ds] %s\n' "$now" "$elapsed" "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-unprovision-devices.sh --env-root cloud_env/staging --brandname RTK [options]

Options:
  --workspace PATH       Default: script parent workspace.
  --env-root PATH        Required environment directory, for example cloud_env/staging.
  --brandname NAME       Required brand cloud name.
  --bind-artifact FILE   Device bind artifact. Default: latest <brand>-device-bind-*.json.
  --count N              Optional number of bound devices to unprovision. Default: all assignments.
  --dry-run              Print planned unprovision calls without calling APIs or writing artifacts.
  -h, --help             Show this help.

Releases user/org bindings through Account Manager user-facing unprovision APIs.
stdout contains a redacted summary. Passwords, bearer tokens, raw Claim Tokens,
and device private material are never printed or written to the output artifact.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--brandname) BRANDNAME="$2"; shift 2 ;;
	--bind-artifact) BIND_ARTIFACT="$2"; shift 2 ;;
	--count) COUNT="$2"; shift 2 ;;
	--dry-run) DRY_RUN=1; shift ;;
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

validate_args() {
	BRANDNAME="$(printf '%s' "$BRANDNAME" | awk '{$1=$1; print}')"
	[[ -n "$BRANDNAME" ]] || die "--brandname is required"
	[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"
	if [[ -n "$COUNT" ]]; then
		[[ "$COUNT" =~ ^[0-9]+$ ]] || die "--count must be a positive integer"
		((COUNT > 0)) || die "--count must be greater than zero"
	fi
	if printf '%s' "$BRANDNAME" | LC_ALL=C grep -q '[[:cntrl:]]'; then
		die "--brandname must not contain control characters"
	fi
}

brand_slug() {
	printf '%s' "$BRANDNAME" |
		tr '[:upper:]' '[:lower:]' |
		sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//' |
		awk '{ if ($0 == "") print "brand"; else print }'
}

abs_existing_file() {
	local path="$1"
	[[ -f "$path" ]] || die "file not found: $path"
	(cd "$(dirname "$path")" && printf '%s/%s\n' "$(pwd)" "$(basename "$path")")
}

resolve_default_inputs() {
	local slug bind_dir latest_bind
	if [[ -z "$BIND_ARTIFACT" ]]; then
		slug="$(brand_slug)"
		bind_dir="$(cloud_env_artifacts_dir "$ENV_ROOT")/device-bind"
		[[ -d "$bind_dir" ]] || die "--bind-artifact was not provided and bind artifact directory was not found: $bind_dir"
		latest_bind="$(find "$bind_dir" -type f -name "$slug-device-bind-*.json" -print | sort | tail -n 1)"
		[[ -n "$latest_bind" ]] || die "--bind-artifact was not provided and no bind artifact matched: $bind_dir/$slug-device-bind-*.json"
		BIND_ARTIFACT="$latest_bind"
	fi
}

validate_bind_artifact() {
	BIND_ARTIFACT="$(abs_existing_file "$BIND_ARTIFACT")"
	BIND_BRAND="$(jq -r '.brandname // empty' "$BIND_ARTIFACT")"
	[[ "$BIND_BRAND" == "$BRANDNAME" ]] || die "--bind-artifact brandname $BIND_BRAND does not match --brandname $BRANDNAME"
	BRAND_CLOUD_ID="$(jq -r '.brand_cloud_id // empty' "$BIND_ARTIFACT")"
	[[ -n "$BRAND_CLOUD_ID" && "$BRAND_CLOUD_ID" != "null" ]] || die "--bind-artifact missing brand_cloud_id"
	USERS_FILE="$(jq -r '.inputs.users_file // empty' "$BIND_ARTIFACT")"
	[[ -n "$USERS_FILE" && "$USERS_FILE" != "null" ]] || die "--bind-artifact missing inputs.users_file"
	USERS_FILE="$(abs_existing_file "$USERS_FILE")"
	jq -e '
		(.assignments | type == "array" and length > 0) and
		all(.assignments[]; (.assigned_email | type == "string" and length > 0) and
			(.device_id | type == "string" and length > 0) and
			(.account_device_id | type == "string" and length > 0))
	' "$BIND_ARTIFACT" >/dev/null || die "--bind-artifact assignments require assigned_email, device_id, and account_device_id"
	jq -e '
		all(.users[]; (.email | type == "string" and length > 0) and (.password | type == "string" and length > 0))
	' "$USERS_FILE" >/dev/null || die "bind artifact users_file users require email and password"
	BIND_COUNT="$(jq '.assignments | length' "$BIND_ARTIFACT")"
	if [[ -z "$COUNT" ]]; then
		COUNT="$BIND_COUNT"
	fi
	((COUNT <= BIND_COUNT)) || die "--count $COUNT exceeds bind assignment count $BIND_COUNT"
}

build_plan() {
	jq --argjson count "$COUNT" '
		.assignments[0:$count]
		| map({
			assignment_index,
			assigned_email,
			device_id,
			device_type,
			category,
			service_options,
			claim_id,
			account_device_id,
			previous_status: (.status // "unknown")
		})
	' "$BIND_ARTIFACT"
}

login() {
	local email="$1"
	local password="$2"
	local out="$TMPDIR/login.out"
	local payload="$TMPDIR/login-payload.json"
	jq -cn --arg email "$email" --arg password "$password" '{email:$email,password:$password}' > "$payload"
	local status
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/auth/login")"
	[[ "$status" == "200" ]] || die "login failed: email=$email HTTP $status"
	jq -r '.tokens.access_token // empty' "$out"
}

write_secret_user_tokens() {
	local users_jsonl="$TMPDIR/user-tokens.jsonl"
	local emails_file="$TMPDIR/plan-emails.txt"
	: > "$users_jsonl"
	jq -r '.[].assigned_email' <<<"$PLAN_JSON" | sort -u > "$emails_file"
	local email password token
	while IFS= read -r email; do
		password="$(jq -r --arg email "$email" '.users[] | select(.email == $email) | .password' "$USERS_FILE" | head -n 1)"
		[[ -n "$password" && "$password" != "null" ]] || die "users_file missing password for assigned user: $email"
		log "logging in assigned user: email=$email"
		token="$(login "$email" "$password")"
		[[ -n "$token" ]] || die "user login response did not include an access token: $email"
		jq -cn --arg email "$email" --arg token "$token" '{email:$email, token:$token}' >> "$users_jsonl"
	done < "$emails_file"
	jq -s . "$users_jsonl" > "$USER_TOKENS_FILE"
}

user_token_for_email() {
	local email="$1"
	jq -r --arg email "$email" '.[] | select(.email == $email) | .token' "$USER_TOKENS_FILE" | head -n 1
}

preflight_unprovision_route() {
	local email password token payload out status probe_device_id error
	email="$(jq -r '.[0].assigned_email' <<<"$PLAN_JSON")"
	password="$(jq -r --arg email "$email" '.users[] | select(.email == $email) | .password' "$USERS_FILE" | head -n 1)"
	[[ -n "$password" && "$password" != "null" ]] || die "users_file missing password for assigned user: $email"
	log "checking Account Manager unprovision API route: email=$email"
	token="$(login "$email" "$password")"
	[[ -n "$token" ]] || die "preflight login response did not include an access token: $email"
	probe_device_id="00000000-0000-0000-0000-000000000000"
	payload="$TMPDIR/unprovision-preflight.json"
	out="$TMPDIR/unprovision-preflight.out"
	jq -cn --arg reason "route_preflight" '{reason:$reason}' > "$payload"
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		-H "authorization: Bearer $token" \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/orgs/$BRAND_CLOUD_ID/devices/$probe_device_id/unprovision")"
	case "$status" in
	404)
		error="$(jq -r '.error // empty' "$out" 2>/dev/null || true)"
		if [[ "$error" == "not_found" ]]; then
			log "Account Manager unprovision API route is available"
			return 0
		fi
		die "Account Manager unprovision API route is not deployed at $AM_BASE_URL; deploy an Account Manager build with /v1/orgs/:orgId/devices/:deviceId/unprovision before running this script"
		;;
	403)
		die "assigned user lacks device.unprovision permission in brand cloud: email=$email brand_cloud_id=$BRAND_CLOUD_ID"
		;;
	400|409)
		log "Account Manager unprovision API route is available"
		return 0
		;;
	*)
		die "unexpected Account Manager unprovision API preflight status: HTTP $status"
		;;
	esac
}

unprovision_device() {
	local row="$1"
	local user_token="$2"
	local device_id account_device_id payload out status
	device_id="$(jq -r '.device_id' <<<"$row")"
	account_device_id="$(jq -r '.account_device_id' <<<"$row")"
	payload="$TMPDIR/unprovision-$device_id.json"
	out="$TMPDIR/unprovision-$device_id.out"
	jq -cn --arg reason "user_resale_factory_ready" '{reason:$reason}' > "$payload"
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		-H "authorization: Bearer $user_token" \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/orgs/$BRAND_CLOUD_ID/devices/$account_device_id/unprovision")"
	if [[ "$status" != "200" ]]; then
		printf 'unprovision failed: device=%s account_device=%s HTTP %s\n' "$device_id" "$account_device_id" "$status" >&2
		jq '{error, message}' "$out" >&2 2>/dev/null || true
		return 1
	fi
	jq -c --arg status "$status" '. + {http_status:($status|tonumber)}' "$out"
}

validate_args
need_cmd curl
need_cmd jq

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
TMPDIR="$(mktemp -d /tmp/rtk-unprovision-devices.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
resolve_default_inputs
validate_bind_artifact

PLAN_JSON="$(build_plan)"

if [[ "$DRY_RUN" == "1" ]]; then
	jq -cn \
		--arg brandname "$BRANDNAME" \
		--arg brand_cloud_id "$BRAND_CLOUD_ID" \
		--arg bind_artifact "$BIND_ARTIFACT" \
		--arg users_file "$USERS_FILE" \
		--argjson count "$COUNT" \
		--argjson assignments "$PLAN_JSON" \
		'{action:"dry_run", brandname:$brandname, brand_cloud_id:$brand_cloud_id, count:$count, bind_artifact:$bind_artifact, users_file:$users_file, assignments:$assignments}'
	exit 0
fi

log "workspace=$WORKSPACE"
log "env_root=$ENV_ROOT"
log "bind_artifact=$BIND_ARTIFACT"

AM_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
AM_STATE="$(cloud_env_account_manager_state "$ENV_ROOT")"

log "loading Account Manager env/state"
load_env_file "$AM_ENV"
load_env_file "$AM_STATE"

AM_DOMAIN="${ACCOUNT_MANAGER_LINODE_DOMAIN:-${ACCOUNT_MANAGER_DOMAIN:-account-manager.video-cloud-staging.realtekconnect.com}}"
AM_BASE_URL="https://$AM_DOMAIN"
USER_TOKENS_FILE="$TMPDIR/user-tokens.json"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"

preflight_unprovision_route
write_secret_user_tokens

RESULTS_JSONL="$TMPDIR/results.jsonl"
: > "$RESULTS_JSONL"
UNPROVISIONED=0

while IFS= read -r row; do
	device_id="$(jq -r '.device_id' <<<"$row")"
	email="$(jq -r '.assigned_email' <<<"$row")"
	account_device_id="$(jq -r '.account_device_id' <<<"$row")"
	log "unprovisioning device: device=$device_id account_device=$account_device_id user=$email"
	user_token="$(user_token_for_email "$email")"
	[[ -n "$user_token" ]] || die "no login token for assigned user: $email"
	unprovision_json="$(unprovision_device "$row" "$user_token")"
	UNPROVISIONED=$((UNPROVISIONED + 1))
	jq -cn \
		--argjson assignment "$row" \
		--arg account_device_id "$account_device_id" \
		--arg response_device_id "$(jq -r '.unprovision.device_id // empty' <<<"$unprovision_json")" \
		--arg organization_id "$(jq -r '.unprovision.organization_id // empty' <<<"$unprovision_json")" \
		--arg video_cloud_devid "$(jq -r '.unprovision.video_cloud_devid // empty' <<<"$unprovision_json")" \
		--arg unprovisioned_at "$(jq -r '.unprovision.unprovisioned_at // empty' <<<"$unprovision_json")" \
		'{
			assignment_index: $assignment.assignment_index,
			assigned_email: $assignment.assigned_email,
			device_id: $assignment.device_id,
			device_type: $assignment.device_type,
			category: $assignment.category,
			service_options: $assignment.service_options,
			claim_id: $assignment.claim_id,
			account_device_id: $account_device_id,
			response_device_id: $response_device_id,
			organization_id: $organization_id,
			video_cloud_devid: $video_cloud_devid,
			status: "unprovisioned",
			unprovisioned_at: $unprovisioned_at
		}' >> "$RESULTS_JSONL"
done < <(jq -rc '.[]' <<<"$PLAN_JSON")

SLUG="$(brand_slug)"
ARTIFACT_DIR="$(cloud_env_artifacts_dir "$ENV_ROOT")/device-unprovision"
mkdir -p "$ARTIFACT_DIR"
ARTIFACT_FILE="$ARTIFACT_DIR/$SLUG-device-unprovision-$RUN_ID.json"
ARTIFACT_TMP="$TMPDIR/artifact.json"
jq -s \
	--arg schema "rtk-cloud-workspace.bulk-device-unprovision/v1" \
	--arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	--arg brandname "$BRANDNAME" \
	--arg brand_cloud_id "$BRAND_CLOUD_ID" \
	--arg bind_artifact "$BIND_ARTIFACT" \
	--arg users_file "$USERS_FILE" \
	--argjson count "$COUNT" \
	'{schema:$schema, generated_at:$generated_at, brandname:$brandname, brand_cloud_id:$brand_cloud_id, count:$count, inputs:{bind_artifact:$bind_artifact, users_file:$users_file}, assignments:.}' \
	"$RESULTS_JSONL" > "$ARTIFACT_TMP"
install -m 0600 "$ARTIFACT_TMP" "$ARTIFACT_FILE"
log "unprovision artifact written: $ARTIFACT_FILE"

jq -cn \
	--arg brandname "$BRANDNAME" \
	--arg brand_cloud_id "$BRAND_CLOUD_ID" \
	--arg artifact_file "$ARTIFACT_FILE" \
	--argjson count "$COUNT" \
	--argjson unprovisioned "$UNPROVISIONED" \
	'{action:"unprovisioned", brandname:$brandname, brand_cloud_id:$brand_cloud_id, count:$count, unprovisioned:$unprovisioned, artifact_file:$artifact_file}'
