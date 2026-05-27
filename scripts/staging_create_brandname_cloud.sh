#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
START_EPOCH="$(date +%s)"
BRANDNAME=""
SECRETS_ROOT=""
SKIP_BOOTSTRAP=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	local now elapsed
	now="$(date +%H:%M:%S)"
	elapsed=$(($(date +%s) - START_EPOCH))
	printf '[staging-brand-cloud %s +%03ds] %s\n' "$now" "$elapsed" "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/staging_create_brandname_cloud.sh --brandname NAME [options]

Options:
  --workspace PATH       Default: script parent workspace.
  --secrets-root PATH    Default: <workspace>/.secrets/staging/linode.
  --skip-bootstrap       Do not update/restart the remote platform-admin bootstrap env.
  -h, --help             Show this help.

Creates an Account Manager brand cloud named NAME on the Linode staging
deployment. The script is idempotent: if the brand cloud already exists, it
prints the existing record.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--brandname) BRANDNAME="$2"; shift 2 ;;
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--secrets-root) SECRETS_ROOT="$2"; shift 2 ;;
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

json_string() {
	jq -Rn --arg value "$1" '$value'
}

curl_json_status() {
	local out="$1"
	shift
	curl -sS -o "$out" -w '%{http_code}' "$@"
}

validate_brandname() {
	BRANDNAME="$(printf '%s' "$BRANDNAME" | awk '{$1=$1; print}')"
	[[ -n "$BRANDNAME" ]] || die "--brandname is required"
	if printf '%s' "$BRANDNAME" | LC_ALL=C grep -q '[[:cntrl:]]'; then
		die "--brandname must not contain control characters"
	fi
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
			if curl -fsS http://127.0.0.1:18081/v1/health >/dev/null; then
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

find_existing_brand_cloud() {
	local out="$TMPDIR/list.out"
	local status
	log "checking existing brand clouds for name=$BRANDNAME"
	status="$(curl_json_status "$out" \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		"$AM_BASE_URL/v1/admin/brand-clouds?limit=200")"
	[[ "$status" == "200" ]] || die "brand cloud list failed: HTTP $status"
	EXISTING_ID="$(jq -r --arg name "$BRANDNAME" '.brand_clouds[]? | select(.name == $name) | .id' "$out" | head -n 1)"
	if [[ -n "$EXISTING_ID" ]]; then
		log "brand cloud already exists: id=$EXISTING_ID"
		jq --arg id "$EXISTING_ID" \
			'{action:"exists", brand_cloud:(.brand_clouds[] | select(.id == $id))}' "$out"
		return 0
	fi
	log "brand cloud not found; will create it"
	return 1
}

create_via_api() {
	local payload="$TMPDIR/create.json"
	local out="$TMPDIR/create.out"
	log "creating brand cloud via Account Manager API: name=$BRANDNAME"
	jq -cn --arg name "$BRANDNAME" '{name:$name, metadata:{brandname:$name}}' > "$payload"
	local status
	status="$(curl_json_status "$out" \
		-H 'content-type: application/json' \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		--data-binary "@$payload" \
		"$AM_BASE_URL/v1/admin/brand-clouds")"
	if [[ "$status" == "201" ]]; then
		log "brand cloud created via API"
		jq '{action:"created", brand_cloud:.brand_cloud}' "$out"
		return 0
	fi
	if [[ "$status" != "500" ]]; then
		printf 'brand cloud create failed: HTTP %s\n' "$status" >&2
		jq '{error, message}' "$out" >&2 2>/dev/null || true
		return 1
	fi
	log "API create returned HTTP 500; falling back to direct PostgreSQL upsert"
	return 2
}

create_via_postgres_fallback() {
	remote_target
	local email_b64 brand_b64
	log "creating brand cloud via PostgreSQL fallback on account-manager host=$AM_HOST"
	email_b64="$(printf '%s' "$ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL" | base64)"
	brand_b64="$(printf '%s' "$BRANDNAME" | base64)"
	ssh -i "$AM_SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new "$AM_SSH_USER@$AM_HOST" bash -s -- "$email_b64" "$brand_b64" <<'REMOTE'
set -euo pipefail
email="$(printf '%s' "$1" | base64 -d)"
brand="$(printf '%s' "$2" | base64 -d)"
sudo -u postgres psql -d rtk_account_manager -v ON_ERROR_STOP=1 -v email="$email" -v brand="$brand" -At <<'SQL'
WITH existing AS (
	SELECT id::text, name, organization_kind, status, tier, evaluation_device_quota, metadata, created_at, updated_at, false AS created
	FROM organizations
	WHERE organization_kind = 'brand_cloud' AND name = :'brand'
	ORDER BY created_at ASC
	LIMIT 1
), actor AS (
	SELECT id::text AS id FROM users WHERE email = :'email'
), inserted AS (
	INSERT INTO organizations (name, organization_kind, status, tier, evaluation_device_quota, metadata)
	SELECT :'brand', 'brand_cloud', 'active', 'commercial', 5, jsonb_build_object('brandname', :'brand')
	WHERE NOT EXISTS (SELECT 1 FROM existing)
	RETURNING id::text, name, organization_kind, status, tier, evaluation_device_quota, metadata, created_at, updated_at, true AS created
), selected AS (
	SELECT * FROM inserted
	UNION ALL
	SELECT * FROM existing
), member_upsert AS (
	INSERT INTO organization_members (organization_id, user_id, role)
	SELECT selected.id::uuid, actor.id::uuid, 'owner'
	FROM selected, actor
	ON CONFLICT (organization_id, user_id)
	DO UPDATE SET role = 'owner', updated_at = now()
	RETURNING organization_id
), audit AS (
	INSERT INTO audit_events (event_type, actor_user_id, organization_id, subject_type, subject_id, payload)
	SELECT 'brand_cloud_created', actor.id::uuid, selected.id::uuid, 'brand_cloud', selected.id,
		jsonb_build_object('name', selected.name, 'organization_kind', selected.organization_kind, 'status', selected.status, 'initial_owner_user_id', actor.id)
	FROM selected, actor
	WHERE selected.created
	RETURNING id
)
SELECT jsonb_build_object(
	'action', CASE WHEN created THEN 'created' ELSE 'exists' END,
	'brand_cloud', jsonb_build_object(
		'id', id,
		'name', name,
		'organization_kind', organization_kind,
		'status', status,
		'tier', tier,
		'evaluation_device_quota', evaluation_device_quota,
		'metadata', metadata,
		'created_at', created_at,
		'updated_at', updated_at
	)
)::text
FROM selected;
SQL
REMOTE
}

verify_created_with_api() {
	local expected_json="$1"
	local expected_id
	expected_id="$(jq -r '.brand_cloud.id' <<<"$expected_json")"
	[[ -n "$expected_id" && "$expected_id" != "null" ]] || die "fallback did not return a brand cloud id"
	local out="$TMPDIR/verify-list.out"
	local status
	log "verifying brand cloud through Account Manager API: id=$expected_id"
	status="$(curl_json_status "$out" \
		-H "authorization: Bearer $ACCESS_TOKEN" \
		"$AM_BASE_URL/v1/admin/brand-clouds?limit=200")"
	[[ "$status" == "200" ]] || die "post-create brand cloud verification failed: HTTP $status"
	jq -e --arg id "$expected_id" '.brand_clouds[]? | select(.id == $id)' "$out" >/dev/null ||
		die "post-create brand cloud verification did not find $expected_id"
	log "brand cloud verified: id=$expected_id"
	printf '%s\n' "$expected_json"
}

validate_brandname
log "start staging brand cloud create: brandname=$BRANDNAME"
need_cmd curl
need_cmd jq
need_cmd ssh
need_cmd base64

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
SECRETS_ROOT="${SECRETS_ROOT:-$WORKSPACE/.secrets/staging/linode}"
log "workspace=$WORKSPACE"
AM_REPO="$WORKSPACE/repos/rtk_account_manager"
AM_ENV="$AM_REPO/linode_deploy/secrets/account-manager-public-staging.env"
AM_STATE="$AM_REPO/linode_deploy/state/rtk-account-manager-staging.env"
AM_PLATFORM_ADMIN_ENV="$AM_REPO/linode_deploy/secrets/account-manager-platform-admin.env"

log "loading Account Manager staging env/state"
load_env_file "$AM_ENV"
load_env_file "$AM_STATE"
load_env_file "$AM_PLATFORM_ADMIN_ENV"

AM_DOMAIN="${ACCOUNT_MANAGER_LINODE_DOMAIN:-account-manager.video-cloud-staging.realtekconnect.com}"
AM_BASE_URL="https://$AM_DOMAIN"
TMPDIR="$(mktemp -d /tmp/rtk-brand-cloud.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT

ensure_platform_admin_bootstrap
login_platform_admin
if find_existing_brand_cloud; then
	log "complete: brand cloud exists"
	exit 0
fi
if created_json="$(create_via_api)"; then
	printf '%s\n' "$created_json"
	log "complete: brand cloud created"
	exit 0
fi
fallback_json="$(create_via_postgres_fallback)"
verify_created_with_api "$fallback_json"
log "complete: brand cloud created through fallback"
