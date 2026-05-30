#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_ROOT=""
START_EPOCH="$(date +%s)"
BRANDNAME=""
COUNT=10
ROLE="member"
SKIP_BOOTSTRAP=0
DRY_RUN=0
ROTATE_PASSWORD=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	local now elapsed
	now="$(date +%H:%M:%S)"
	elapsed=$(($(date +%s) - START_EPOCH))
	printf '[cloud-create-users %s +%03ds] %s\n' "$now" "$elapsed" "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-create-users.sh --env-root cloud_env/staging --brandname RTK [options]

Options:
  --workspace PATH       Default: script parent workspace.
  --env-root PATH        Required environment directory, for example cloud_env/staging.
  --brandname NAME       Required brand cloud name.
  --count N              Number of users to create. Default: 10.
  --role ROLE            owner, admin, or member. Default: member.
  --rotate-password      Rotate passwords for existing users.
  --dry-run              Print planned users without creating accounts or writing credentials.
  --skip-bootstrap       Do not update/restart the remote platform-admin bootstrap env.
  -h, --help             Show this help.

Creates activated Account Manager users under an existing brand cloud through
the platform-admin API. Passwords are written only to a local credentials JSON
file under the resolved environment root; stdout contains a summary only.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--brandname) BRANDNAME="$2"; shift 2 ;;
	--count) COUNT="$2"; shift 2 ;;
	--role) ROLE="$2"; shift 2 ;;
	--rotate-password) ROTATE_PASSWORD=1; shift ;;
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
	[[ "$COUNT" =~ ^[0-9]+$ ]] || die "--count must be a positive integer"
	((COUNT > 0)) || die "--count must be greater than zero"
	case "$ROLE" in
	owner|admin|member) ;;
	*) die "--role must be owner, admin, or member" ;;
	esac
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

find_brand_cloud() {
	local out="$TMPDIR/brand-clouds.out"
	local status
	log "checking brand cloud: name=$BRANDNAME"
	status="$(curl_json_status "$out" \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		"$AM_BASE_URL/v1/admin/brand-clouds?limit=200")"
	[[ "$status" == "200" ]] || die "brand cloud list failed: HTTP $status"
	BRAND_CLOUD_JSON="$(jq -c --arg name "$BRANDNAME" '.brand_clouds[]? | select(.name == $name or .metadata.brandname == $name)' "$out" | head -n 1)"
	[[ -n "$BRAND_CLOUD_JSON" ]] || die "brand cloud not found: $BRANDNAME"
	BRAND_CLOUD_ID="$(jq -r '.id' <<<"$BRAND_CLOUD_JSON")"
	[[ -n "$BRAND_CLOUD_ID" && "$BRAND_CLOUD_ID" != "null" ]] || die "brand cloud response did not include id"
	log "brand cloud found: id=$BRAND_CLOUD_ID"
}

planned_users_json() {
	local slug="$1"
	jq -cn --arg brand "$BRANDNAME" --arg slug "$slug" --arg role "$ROLE" --argjson count "$COUNT" '
		{
			brandname: $brand,
			role: $role,
			users: [range(1; $count + 1) as $i | {
				email: ($slug + "+" + ($i | tostring | if length < 3 then ("000"[0:(3-length)] + .) else . end) + "@users.local"),
				display_name: ($brand + " User " + ($i | tostring | if length < 3 then ("000"[0:(3-length)] + .) else . end))
			}]
		}'
}

random_password() {
	openssl rand -base64 24 | tr -d '\n'
}

create_user() {
	local email="$1"
	local display_name="$2"
	local password="$3"
	local payload="$TMPDIR/user-payload.json"
	local out="$TMPDIR/user.out"
	local rotate_json=false
	if [[ "$ROTATE_PASSWORD" == "1" ]]; then
		rotate_json=true
	fi
	jq -cn \
		--arg email "$email" \
		--arg password "$password" \
		--arg display_name "$display_name" \
		--arg role "$ROLE" \
		--argjson rotate_password "$rotate_json" \
		'{email:$email,password:$password,display_name:$display_name,role:$role,rotate_password:$rotate_password}' > "$payload"
	local status
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/admin/brand-clouds/$BRAND_CLOUD_ID/users")"
	if [[ "$status" != "200" && "$status" != "201" ]]; then
		printf 'brand user create failed: email=%s HTTP %s\n' "$email" "$status" >&2
		jq '{error, message}' "$out" >&2 2>/dev/null || true
		return 1
	fi
	jq -r '.action // "assigned"' "$out"
}

validate_args
need_cmd curl
need_cmd jq
need_cmd ssh
need_cmd openssl

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
log "workspace=$WORKSPACE"
log "env_root=$ENV_ROOT"

AM_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
AM_STATE="$(cloud_env_account_manager_state "$ENV_ROOT")"
AM_PLATFORM_ADMIN_ENV="$(cloud_env_account_manager_platform_admin_env "$ENV_ROOT")"

log "loading Account Manager env/state"
load_env_file "$AM_ENV"
load_env_file "$AM_STATE"
load_env_file "$AM_PLATFORM_ADMIN_ENV"

AM_DOMAIN="${ACCOUNT_MANAGER_LINODE_DOMAIN:-account-manager.video-cloud-staging.realtekconnect.com}"
AM_BASE_URL="https://$AM_DOMAIN"
TMPDIR="$(mktemp -d /tmp/rtk-create-users.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT

ensure_platform_admin_bootstrap
login_platform_admin
find_brand_cloud

SLUG="$(brand_slug)"
PLAN_JSON="$(planned_users_json "$SLUG")"

if [[ "$DRY_RUN" == "1" ]]; then
	jq -cn --argjson brand_cloud "$BRAND_CLOUD_JSON" --argjson plan "$PLAN_JSON" \
		'{action:"dry_run", brand_cloud:$brand_cloud, role:$plan.role, users:$plan.users}'
	exit 0
fi

ARTIFACT_DIR="$(cloud_env_artifacts_dir "$ENV_ROOT")/users"
mkdir -p "$ARTIFACT_DIR"
CREDENTIALS_FILE="$ARTIFACT_DIR/$SLUG-users-$(date -u +%Y%m%dT%H%M%SZ).json"
CREDENTIALS_TMP="$TMPDIR/credentials.json"
CREATED=0
ASSIGNED=0
USERS_JSONL="$TMPDIR/users.jsonl"
: > "$USERS_JSONL"

for i in $(seq 1 "$COUNT"); do
	suffix="$(printf '%03d' "$i")"
	email="$SLUG+$suffix@users.local"
	display_name="$BRANDNAME User $suffix"
	password="$(random_password)"
	log "creating brand user: email=$email role=$ROLE"
	action="$(create_user "$email" "$display_name" "$password")"
	case "$action" in
	created) CREATED=$((CREATED + 1)) ;;
	*) ASSIGNED=$((ASSIGNED + 1)) ;;
	esac
	jq -cn \
		--arg email "$email" \
		--arg display_name "$display_name" \
		--arg role "$ROLE" \
		--arg password "$password" \
		--arg action "$action" \
		'{email:$email,display_name:$display_name,role:$role,password:$password,action:$action}' >> "$USERS_JSONL"
done

jq -s \
	--arg brandname "$BRANDNAME" \
	--arg brand_cloud_id "$BRAND_CLOUD_ID" \
	--arg role "$ROLE" \
	'{brandname:$brandname,brand_cloud_id:$brand_cloud_id,role:$role,users:.}' \
	"$USERS_JSONL" > "$CREDENTIALS_TMP"
install -m 0600 "$CREDENTIALS_TMP" "$CREDENTIALS_FILE"
log "credentials written: $CREDENTIALS_FILE"

jq -cn \
	--arg brandname "$BRANDNAME" \
	--arg brand_cloud_id "$BRAND_CLOUD_ID" \
	--arg role "$ROLE" \
	--arg credentials_file "$CREDENTIALS_FILE" \
	--argjson count "$COUNT" \
	--argjson created "$CREATED" \
	--argjson assigned "$ASSIGNED" \
	'{action:"created", brandname:$brandname, brand_cloud_id:$brand_cloud_id, role:$role, count:$count, created:$created, assigned:$assigned, credentials_file:$credentials_file}'
