#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_ROOT=""
START_EPOCH="$(date +%s)"
BRANDNAME=""
USERS_FILE=""
DEVICES_DIR=""
COUNT=""
SKIP_BOOTSTRAP=0
DRY_RUN=0
CLAIM_TTL_HOURS=24

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	local now elapsed
	now="$(date +%H:%M:%S)"
	elapsed=$(($(date +%s) - START_EPOCH))
	printf '[cloud-bind-devices %s +%03ds] %s\n' "$now" "$elapsed" "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-bind-devices.sh --env-root cloud_env/staging --brandname RTK [options]

Options:
  --workspace PATH       Default: script parent workspace.
  --env-root PATH        Required environment directory, for example cloud_env/staging.
  --brandname NAME       Required brand cloud name.
  --users-file FILE      Credentials artifact from cloud-create-users.sh. Default: latest <brand>-users-*.json.
  --devices-dir DIR      Generated/factory-enrolled devices directory. Default: <env-root>/devices/test_device.
  --count N              Optional number of devices to bind. Default: all devices in manifests/devices.json.
  --claim-ttl-hours N    Claim Token lifetime. Default: 24.
  --dry-run              Print planned assignments without calling APIs or writing artifacts.
  --skip-bootstrap       Do not update/restart the remote platform-admin bootstrap env.
  -h, --help             Show this help.

Binds factory-enrolled devices to brand users through Account Manager APIs only.
stdout contains a redacted summary. Passwords, bearer tokens, raw Claim Tokens,
and device private key paths are never printed or written to the output artifact.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--brandname) BRANDNAME="$2"; shift 2 ;;
	--users-file) USERS_FILE="$2"; shift 2 ;;
	--devices-dir) DEVICES_DIR="$2"; shift 2 ;;
	--count) COUNT="$2"; shift 2 ;;
	--claim-ttl-hours) CLAIM_TTL_HOURS="$2"; shift 2 ;;
	--dry-run) DRY_RUN=1; shift ;;
	--skip-bootstrap) SKIP_BOOTSTRAP=1; shift ;;
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
	[[ "$CLAIM_TTL_HOURS" =~ ^[0-9]+$ ]] || die "--claim-ttl-hours must be a positive integer"
	((CLAIM_TTL_HOURS > 0)) || die "--claim-ttl-hours must be greater than zero"
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

remote_target() {
	AM_HOST="${ACCOUNT_MANAGER_LINODE_HOST:-${ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4:-}}"
	AM_SSH_USER="${ACCOUNT_MANAGER_LINODE_SSH_USER:-root}"
	AM_SSH_KEY="${ACCOUNT_MANAGER_LINODE_SSH_KEY:-$HOME/.ssh/id_ed25519_rtkcloud}"
	[[ -n "$AM_HOST" ]] || die "ACCOUNT_MANAGER_LINODE_HOST or ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4 is required"
	[[ -n "$AM_SSH_KEY" ]] || die "ACCOUNT_MANAGER_LINODE_SSH_KEY is required"
}

ensure_platform_admin_bootstrap() {
	if [[ "$SKIP_BOOTSTRAP" != "0" ]]; then
		log "skip bootstrap admin env update"
		return 0
	fi
	remote_target
	[[ -n "${ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL:-}" ]] || die "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL is required"
	[[ -n "${ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD:-}" ]] || die "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD is required"
	log "updating platform-admin bootstrap env on account-manager host=$AM_HOST"
	{
		printf 'ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=%s\n' "$ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"
		printf 'ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=%s\n' "$ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"
	} | ssh -i "$AM_SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new "$AM_SSH_USER@$AM_HOST" 'set -euo pipefail
		env_file=/etc/rtk-account-manager/account-manager.env
		test -f "$env_file"
		cp -p "$env_file" "$env_file.bootstrap-admin.bak.$(date -u +%Y%m%dT%H%M%SZ)"
		tmp="$(mktemp)"
		grep -vE "^ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_(EMAIL|PASSWORD)=" "$env_file" > "$tmp"
		cat >> "$tmp"
		install -m 0600 -o root -g root "$tmp" "$env_file"
		rm -f "$tmp"
		systemctl restart rtk-account-manager.service
		for _ in $(seq 1 30); do
			if curl -fsS http://127.0.0.1:18081/v1/health >/dev/null 2>&1; then
				echo "bootstrap admin env applied and account-manager is healthy" >&2
				exit 0
			fi
			sleep 1
		done
		journalctl -u rtk-account-manager.service -n 80 --no-pager >&2 || true
		exit 1
	'
	log "platform-admin bootstrap env ready"
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

login_platform_admin() {
	log "logging in platform admin: $AM_BASE_URL/v1/auth/login"
	ACCESS_TOKEN="$(login "$ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL" "$ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD")"
	[[ -n "$ACCESS_TOKEN" ]] || die "platform admin login response did not include an access token"
	log "platform admin login ok"
}

find_brand_cloud() {
	local out="$TMPDIR/brand-clouds.out"
	local status
	log "checking brand cloud: name=$BRANDNAME"
	status="$(curl_json_status "$out" \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		"$AM_BASE_URL/v1/admin/brand-clouds?limit=200")"
	[[ "$status" == "200" ]] || die "brand cloud list failed: HTTP $status"
	BRAND_CLOUD_JSON="$(jq -c --arg name "$BRANDNAME" '.brand_clouds[]? | select((.name == $name or .metadata.brandname == $name) and (.organization_kind == "brand_cloud" or .organization_kind == null))' "$out" | head -n 1)"
	[[ -n "$BRAND_CLOUD_JSON" ]] || die "brand cloud not found: $BRANDNAME"
	BRAND_CLOUD_ID="$(jq -r '.id' <<<"$BRAND_CLOUD_JSON")"
	[[ -n "$BRAND_CLOUD_ID" && "$BRAND_CLOUD_ID" != "null" ]] || die "brand cloud response did not include id"
	log "brand cloud found: id=$BRAND_CLOUD_ID"
}

abs_existing_file() {
	local path="$1"
	[[ -f "$path" ]] || die "file not found: $path"
	(cd "$(dirname "$path")" && printf '%s/%s\n' "$(pwd)" "$(basename "$path")")
}

abs_existing_dir() {
	local path="$1"
	[[ -d "$path" ]] || die "directory not found: $path"
	(cd "$path" && pwd)
}

resolve_default_inputs() {
	local slug users_dir latest_users
	if [[ -z "$DEVICES_DIR" ]]; then
		DEVICES_DIR="$(cloud_env_test_devices_dir "$ENV_ROOT")"
	fi
	if [[ -z "$USERS_FILE" ]]; then
		slug="$(brand_slug)"
		users_dir="$(cloud_env_artifacts_dir "$ENV_ROOT")/users"
		[[ -d "$users_dir" ]] || die "--users-file was not provided and users artifact directory was not found: $users_dir"
		latest_users="$(find "$users_dir" -type f -name "$slug-users-*.json" -print | sort | tail -n 1)"
		[[ -n "$latest_users" ]] || die "--users-file was not provided and no users artifact matched: $users_dir/$slug-users-*.json"
		USERS_FILE="$latest_users"
	fi
}

validate_users_and_devices() {
	USERS_FILE="$(abs_existing_file "$USERS_FILE")"
	DEVICES_DIR="$(abs_existing_dir "$DEVICES_DIR")"
	DEVICES_MANIFEST="$DEVICES_DIR/manifests/devices.json"
	[[ -f "$DEVICES_MANIFEST" ]] || die "device manifest not found: $DEVICES_MANIFEST"
	USER_COUNT="$(jq '.users | length' "$USERS_FILE")"
	DEVICE_COUNT="$(jq 'length' "$DEVICES_MANIFEST")"
	[[ "$USER_COUNT" =~ ^[0-9]+$ && "$USER_COUNT" -gt 0 ]] || die "--users-file must contain at least one user"
	[[ "$DEVICE_COUNT" =~ ^[0-9]+$ && "$DEVICE_COUNT" -gt 0 ]] || die "devices manifest must contain at least one device"
	if [[ -z "$COUNT" ]]; then
		COUNT="$DEVICE_COUNT"
	fi
	((COUNT <= DEVICE_COUNT)) || die "--count $COUNT exceeds device manifest count $DEVICE_COUNT"
	USERS_BRAND="$(jq -r '.brandname // empty' "$USERS_FILE")"
	[[ -z "$USERS_BRAND" || "$USERS_BRAND" == "$BRANDNAME" ]] || die "--users-file brandname $USERS_BRAND does not match --brandname $BRANDNAME"
	USERS_BRAND_CLOUD_ID="$(jq -r '.brand_cloud_id // empty' "$USERS_FILE")"
	jq -e '
		all(.users[]; (.email | type == "string" and length > 0) and (.password | type == "string" and length > 0))
	' "$USERS_FILE" >/dev/null || die "--users-file users require email and password"
	jq -e '
		all(.[]; (.device_id | type == "string" and length > 0) and (.device_type | type == "string" and length > 0) and (.service_options | type == "array" and length > 0))
	' "$DEVICES_MANIFEST" >/dev/null || die "devices manifest requires device_id, device_type, and service_options"
	jq -e '
		all(.[]; all(.service_options[]; . == "mqtt" or . == "video_streaming" or . == "video_storage"))
	' "$DEVICES_MANIFEST" >/dev/null || die "devices manifest has unsupported service_options"
}

build_assignments() {
	local devices="$TMPDIR/devices-selected.json"
	jq --argjson count "$COUNT" '.[0:$count]' "$DEVICES_MANIFEST" > "$devices"
	jq -n \
		--slurpfile users "$USERS_FILE" \
		--slurpfile devices "$devices" '
		($users[0].users) as $users |
		($devices[0]) as $devices |
		($users | length) as $user_count |
		[$devices[].device_type] | unique as $types |
		(reduce $types[] as $type (
			{offset: 0, by_id: {}};
			([range(0; $devices|length) as $i | select($devices[$i].device_type == $type) | $i]) as $idxs |
			reduce range(0; $idxs|length) as $j (
				.;
				($idxs[$j]) as $device_index |
				((.offset + $j) % $user_count) as $user_index |
				.by_id[$devices[$device_index].device_id] = {
					assignment_index: $device_index,
					device_id: $devices[$device_index].device_id,
					device_type: $devices[$device_index].device_type,
					category: (if (($devices[$device_index].service_options // []) | index("video_streaming") or index("video_storage")) then "ip_camera" else "mqtt_device" end),
					display_name: ($devices[$device_index].display_name // $devices[$device_index].device_id),
					service_options: $devices[$device_index].service_options,
					assigned_email: $users[$user_index].email,
					assigned_display_name: ($users[$user_index].display_name // $users[$user_index].email)
				}
			)
			| .offset = ((.offset + ($idxs | length)) % $user_count)
		)) as $assigned |
		[$devices[] | $assigned.by_id[.device_id]]
	'
}

write_secret_user_tokens() {
	local users_jsonl="$TMPDIR/user-tokens.jsonl"
	: > "$users_jsonl"
	local email password token row
	while IFS= read -r row; do
		email="$(jq -r '.email' <<<"$row")"
		password="$(jq -r '.password' <<<"$row")"
		log "logging in assigned user: email=$email"
		token="$(login "$email" "$password")"
		[[ -n "$token" ]] || die "user login response did not include an access token: $email"
		jq -cn --arg email "$email" --arg token "$token" '{email:$email, token:$token}' >> "$users_jsonl"
	done < <(jq -rc '.users[]' "$USERS_FILE")
	jq -s . "$users_jsonl" > "$USER_TOKENS_FILE"
}

user_token_for_email() {
	local email="$1"
	jq -r --arg email "$email" '.[] | select(.email == $email) | .token' "$USER_TOKENS_FILE" | head -n 1
}

create_claim_token() {
	local assignment="$1"
	local device_id device_type category service_options_json payload out status expires activity_id
	device_id="$(jq -r '.device_id' <<<"$assignment")"
	device_type="$(jq -r '.device_type' <<<"$assignment")"
	service_options_json="$(jq -c '.service_options' <<<"$assignment")"
	category="$(jq -r '.category' <<<"$assignment")"
	expires="$(date -u -v+"${CLAIM_TTL_HOURS}"H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "+${CLAIM_TTL_HOURS} hours" +%Y-%m-%dT%H:%M:%SZ)"
	activity_id="bulk-bind-$RUN_ID-$device_id"
	payload="$TMPDIR/claim-$device_id.json"
	out="$TMPDIR/claim-$device_id.out"
	jq -cn \
		--arg organization_id "$BRAND_CLOUD_ID" \
		--arg category "$category" \
		--arg video_cloud_devid "$device_id" \
		--arg activity_id "$activity_id" \
		--arg clip_public_key "bulk-bind-placeholder-public-key" \
		--arg expires_at "$expires" \
		--arg device_type "$device_type" \
		--arg source "cloud-bind-devices" \
		--argjson service_options "$service_options_json" \
		'{
			organization_id: $organization_id,
			category: $category,
			video_cloud_devid: $video_cloud_devid,
			activity_id: $activity_id,
			clip_public_key: $clip_public_key,
			expires_at: $expires_at,
			service_options: $service_options,
			metadata: {
				source: $source,
				device_type: $device_type,
				service_options: $service_options
			}
		}' > "$payload"
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/admin/device-claim-tokens")"
	if [[ "$status" != "200" && "$status" != "201" ]]; then
		printf 'claim token create failed: device=%s HTTP %s\n' "$device_id" "$status" >&2
		jq '{error, message}' "$out" >&2 2>/dev/null || true
		return 1
	fi
	jq -c --arg status "$status" '{claim_id:(.id // .claim_id), claim_token:(.claim_token // .token), http_status:($status|tonumber)}' "$out"
}

resolve_claim() {
	local assignment="$1"
	local claim_token="$2"
	local user_token="$3"
	local device_id device_name payload out status
	device_id="$(jq -r '.device_id' <<<"$assignment")"
	device_name="$(jq -r '.display_name' <<<"$assignment")"
	payload="$TMPDIR/resolve-$device_id.json"
	out="$TMPDIR/resolve-$device_id.out"
	jq -cn --arg claim_token "$claim_token" --arg device_name "$device_name" '{claim_token:$claim_token,device_name:$device_name}' > "$payload"
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		-H "authorization: Bearer $user_token" \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/orgs/$BRAND_CLOUD_ID/devices/claim/resolve")"
	if [[ "$status" != "200" && "$status" != "201" ]]; then
		printf 'claim resolve failed: device=%s HTTP %s\n' "$device_id" "$status" >&2
		jq '{error, message}' "$out" >&2 2>/dev/null || true
		return 1
	fi
	jq -c --arg status "$status" '. + {http_status:($status|tonumber)}' "$out"
}

start_provision() {
	local assignment="$1"
	local resolve_json="$2"
	local user_token="$3"
	local device_id account_device_id operation_id service_options_json payload out status
	device_id="$(jq -r '.device_id' <<<"$assignment")"
	account_device_id="$(jq -r '.device.id // empty' <<<"$resolve_json")"
	[[ -n "$account_device_id" && "$account_device_id" != "null" ]] || die "claim resolve missing account device id: $device_id"
	operation_id="bulk-bind-$RUN_ID-$device_id"
	service_options_json="$(jq -c '.provision_input.service_options // empty' <<<"$resolve_json")"
	if [[ -z "$service_options_json" ]]; then
		service_options_json="$(jq -c '.service_options' <<<"$assignment")"
	fi
	payload="$TMPDIR/provision-$device_id.json"
	out="$TMPDIR/provision-$device_id.out"
	jq -cn \
		--arg video_cloud_devid "$(jq -r '.provision_input.video_cloud_devid // .device.video_cloud_devid // empty' <<<"$resolve_json")" \
		--arg activity_id "$(jq -r '.provision_input.activity_id // empty' <<<"$resolve_json")" \
		--arg clip_public_key "$(jq -r '.provision_input.clip_public_key // empty' <<<"$resolve_json")" \
		--arg operation_id "$operation_id" \
		--argjson service_options "$service_options_json" \
		'{
			video_cloud_devid: $video_cloud_devid,
			activity_id: $activity_id,
			clip_public_key: $clip_public_key,
			operation_id: $operation_id,
			service_options: $service_options
		}' > "$payload"
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		-H "authorization: Bearer $user_token" \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/orgs/$BRAND_CLOUD_ID/devices/$account_device_id/provision")"
	if [[ "$status" != "200" && "$status" != "201" && "$status" != "202" ]]; then
		printf 'provision start failed: device=%s account_device=%s HTTP %s\n' "$device_id" "$account_device_id" "$status" >&2
		jq '{error, message}' "$out" >&2 2>/dev/null || true
		return 1
	fi
	jq -c --arg operation_id "$operation_id" --arg status "$status" '. + {operation_id: (.operation.id // .id // $operation_id), http_status:($status|tonumber)}' "$out"
}

validate_args
need_cmd curl
need_cmd jq
need_cmd ssh

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
TMPDIR="$(mktemp -d /tmp/rtk-bind-devices.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
resolve_default_inputs
validate_users_and_devices

ASSIGNMENTS_JSON="$(build_assignments)"

if [[ "$DRY_RUN" == "1" ]]; then
	jq -cn \
		--arg brandname "$BRANDNAME" \
		--arg users_file "$USERS_FILE" \
		--arg devices_dir "$DEVICES_DIR" \
		--argjson count "$COUNT" \
		--argjson assignments "$ASSIGNMENTS_JSON" \
		'{action:"dry_run", brandname:$brandname, count:$count, users_file:$users_file, devices_dir:$devices_dir, assignments:$assignments}'
	exit 0
fi

log "workspace=$WORKSPACE"
log "env_root=$ENV_ROOT"
log "users_file=$USERS_FILE"
log "devices_dir=$DEVICES_DIR"

AM_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
AM_STATE="$(cloud_env_account_manager_state "$ENV_ROOT")"
AM_PLATFORM_ADMIN_ENV="$(cloud_env_account_manager_platform_admin_env "$ENV_ROOT")"

log "loading Account Manager env/state"
load_env_file "$AM_ENV"
load_env_file "$AM_STATE"
load_env_file "$AM_PLATFORM_ADMIN_ENV"

AM_DOMAIN="${ACCOUNT_MANAGER_LINODE_DOMAIN:-account-manager.video-cloud-staging.realtekconnect.com}"
AM_BASE_URL="https://$AM_DOMAIN"
USER_TOKENS_FILE="$TMPDIR/user-tokens.json"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"

ensure_platform_admin_bootstrap
login_platform_admin
find_brand_cloud
if [[ -n "$USERS_BRAND_CLOUD_ID" && "$USERS_BRAND_CLOUD_ID" != "$BRAND_CLOUD_ID" ]]; then
	die "--users-file brand_cloud_id $USERS_BRAND_CLOUD_ID does not match resolved brand cloud $BRAND_CLOUD_ID"
fi
write_secret_user_tokens

RESULTS_JSONL="$TMPDIR/results.jsonl"
: > "$RESULTS_JSONL"
CREATED_CLAIMS=0
RESOLVED_CLAIMS=0
PROVISION_STARTED=0

while IFS= read -r row; do
	device_id="$(jq -r '.device_id' <<<"$row")"
	email="$(jq -r '.assigned_email' <<<"$row")"
	log "binding device: device=$device_id user=$email"
	user_token="$(user_token_for_email "$email")"
	[[ -n "$user_token" ]] || die "no login token for assigned user: $email"
	claim_json="$(create_claim_token "$row")"
	CREATED_CLAIMS=$((CREATED_CLAIMS + 1))
	claim_token="$(jq -r '.claim_token // empty' <<<"$claim_json")"
	claim_id="$(jq -r '.claim_id // empty' <<<"$claim_json")"
	[[ -n "$claim_token" && "$claim_token" != "null" ]] || die "claim token create response missing raw token: $device_id"
	resolve_json="$(resolve_claim "$row" "$claim_token" "$user_token")"
	RESOLVED_CLAIMS=$((RESOLVED_CLAIMS + 1))
	provision_json="$(start_provision "$row" "$resolve_json" "$user_token")"
	PROVISION_STARTED=$((PROVISION_STARTED + 1))
	jq -cn \
		--argjson assignment "$row" \
		--arg claim_id "$claim_id" \
		--arg account_device_id "$(jq -r '.device.id' <<<"$resolve_json")" \
		--arg operation_id "$(jq -r '.operation_id' <<<"$provision_json")" \
		'{
			assignment_index: $assignment.assignment_index,
			assigned_email: $assignment.assigned_email,
			device_id: $assignment.device_id,
			device_type: $assignment.device_type,
			category: $assignment.category,
			service_options: $assignment.service_options,
			claim_id: $claim_id,
			account_device_id: $account_device_id,
			operation_id: $operation_id,
			status: "provision_requested"
		}' >> "$RESULTS_JSONL"
done < <(jq -rc '.[]' <<<"$ASSIGNMENTS_JSON")

SLUG="$(brand_slug)"
ARTIFACT_DIR="$(cloud_env_artifacts_dir "$ENV_ROOT")/device-bind"
mkdir -p "$ARTIFACT_DIR"
ARTIFACT_FILE="$ARTIFACT_DIR/$SLUG-device-bind-$RUN_ID.json"
ARTIFACT_TMP="$TMPDIR/artifact.json"
jq -s \
	--arg schema "rtk-cloud-workspace.bulk-device-bind/v1" \
	--arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
	--arg brandname "$BRANDNAME" \
	--arg brand_cloud_id "$BRAND_CLOUD_ID" \
	--arg users_file "$USERS_FILE" \
	--arg devices_dir "$DEVICES_DIR" \
	--argjson count "$COUNT" \
	'{schema:$schema, generated_at:$generated_at, brandname:$brandname, brand_cloud_id:$brand_cloud_id, count:$count, inputs:{users_file:$users_file, devices_dir:$devices_dir}, assignments:.}' \
	"$RESULTS_JSONL" > "$ARTIFACT_TMP"
install -m 0600 "$ARTIFACT_TMP" "$ARTIFACT_FILE"
log "bind artifact written: $ARTIFACT_FILE"

jq -cn \
	--arg brandname "$BRANDNAME" \
	--arg brand_cloud_id "$BRAND_CLOUD_ID" \
	--arg artifact_file "$ARTIFACT_FILE" \
	--argjson count "$COUNT" \
	--argjson created_claims "$CREATED_CLAIMS" \
	--argjson resolved_claims "$RESOLVED_CLAIMS" \
	--argjson provision_started "$PROVISION_STARTED" \
	'{action:"bound", brandname:$brandname, brand_cloud_id:$brand_cloud_id, count:$count, created_claims:$created_claims, resolved_claims:$resolved_claims, provision_started:$provision_started, artifact_file:$artifact_file}'
