#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
SECRETS_ROOT=""
OPERATOR_ENV=""
SSH_KEY="$HOME/.ssh/id_ed25519_rtkcloud"
DNS_ROOT_DOMAIN="realtekconnect.com"
GODADDY_ENVIRONMENT="prod"
ARTIFACT_BASE=""
VIDEO_RELEASE=""
ACCOUNT_RELEASE=""
ADMIN_RELEASE=""
VERBOSE=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[staging-deploy] %s\n' "$*" >&2
}

debug() {
	if [[ "$VERBOSE" == "1" ]]; then
		printf '[staging-deploy:debug] %s\n' "$*" >&2
	fi
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/staging-deploy.sh --video-release VERSION --account-release VERSION --admin-release VERSION [options]

Required:
  --video-release VERSION             Explicit Video Cloud Object Storage release. "latest" is not allowed.
  --account-release VERSION           Explicit Account Manager release label. "latest" is not allowed.
  --admin-release VERSION             Explicit Cloud Admin release label. "latest" is not allowed.

Options:
  --workspace PATH                    Default: script parent workspace.
  --operator-env PATH                 Default: <secrets-root>/video-cloud/env/operator.env.
  --secrets-root PATH                 Default: <workspace>/.secrets/staging/linode.
  --ssh-key PATH                      Default: ~/.ssh/id_ed25519_rtkcloud.
  --dns-root-domain NAME              Default: realtekconnect.com.
  --godaddy-env ENV                   Default: prod.
  --artifact-dir PATH                 Default: <secrets-root>/video-cloud/artifacts.
  --verbose                           Print extra diagnostics.
  -h, --help                          Show this help.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--video-release) VIDEO_RELEASE="$2"; shift 2 ;;
	--account-release) ACCOUNT_RELEASE="$2"; shift 2 ;;
	--admin-release) ADMIN_RELEASE="$2"; shift 2 ;;
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
	--secrets-root) SECRETS_ROOT="$2"; shift 2 ;;
	--ssh-key) SSH_KEY="$2"; shift 2 ;;
	--dns-root-domain) DNS_ROOT_DOMAIN="$2"; shift 2 ;;
	--godaddy-env) GODADDY_ENVIRONMENT="$2"; shift 2 ;;
	--artifact-dir) ARTIFACT_BASE="$2"; shift 2 ;;
	--verbose) VERBOSE=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

for value_name in VIDEO_RELEASE ACCOUNT_RELEASE ADMIN_RELEASE; do
	value="${!value_name}"
	[[ -n "$value" ]] || die "--$(tr '[:upper:]_' '[:lower:]-' <<<"$value_name") is required"
	[[ "$value" != "latest" ]] || die "$value_name must be explicit; latest is not allowed"
done

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
SECRETS_ROOT="${SECRETS_ROOT:-$WORKSPACE/.secrets/staging/linode}"
OPERATOR_ENV="${OPERATOR_ENV:-$SECRETS_ROOT/video-cloud/env/operator.env}"
ARTIFACT_BASE="${ARTIFACT_BASE:-$SECRETS_ROOT/video-cloud/artifacts}"

VC_REPO="$WORKSPACE/repos/rtk_video_cloud"
AM_REPO="$WORKSPACE/repos/rtk_account_manager"
ADMIN_REPO="$WORKSPACE/repos/rtk_cloud_admin"

VC_CONFIG="$SECRETS_ROOT/video-cloud/config/video-cloud-staging.yaml"
VC_STATE="$VC_REPO/linode_deploy/state/video-cloud-staging.state.json"
VC_SECRET_STATE="$SECRETS_ROOT/video-cloud/state/video-cloud-staging.state.json"
VC_SECRETS_FILE="$SECRETS_ROOT/video-cloud/env/video-cloud-staging.env"
AM_ENV="$AM_REPO/linode_deploy/secrets/account-manager-public-staging.env"
AM_STATE="$AM_REPO/linode_deploy/state/rtk-account-manager-staging.env"
ADMIN_ENV="$ADMIN_REPO/deploy/linode/admin-staging.env"
ADMIN_STATE="$ADMIN_REPO/deploy/linode/rtk-cloud-admin-staging.state"

VC_GATEWAY_DOMAIN="video-cloud-staging.$DNS_ROOT_DOMAIN"
VC_CERTISSUER_DOMAIN="certissuer.video-cloud-staging.$DNS_ROOT_DOMAIN"
AM_DOMAIN="account-manager.video-cloud-staging.$DNS_ROOT_DOMAIN"
ADMIN_DOMAIN="admin.video-cloud-staging.$DNS_ROOT_DOMAIN"

READY_DIR="$ARTIFACT_BASE/readiness-$(date -u +%Y%m%dT%H%M%SZ)"
REPORT="$READY_DIR/readiness-report.md"
STATUS_FILE="$READY_DIR/status.tsv"

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

load_operator_env() {
	[[ -f "$OPERATOR_ENV" ]] || die "operator env not found: $OPERATOR_ENV"
	load_env_file "$OPERATOR_ENV"
	[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"
}

linode_api() {
	local method="$1"
	local path="$2"
	curl -fsS -X "$method" "https://api.linode.com/v4$path" \
		-H "Authorization: Bearer $LINODE_TOKEN" \
		-H 'Content-Type: application/json'
}

resolve_dns() {
	local server="$1"
	local domain="$2"
	dig +short "@$server" "$domain" | tail -n 1 || true
}

authoritative_ns() {
	dig NS "$DNS_ROOT_DOMAIN" +short | head -n 1
}

require_files() {
	[[ -d "$VC_REPO" ]] || die "missing submodule: $VC_REPO"
	[[ -d "$AM_REPO" ]] || die "missing submodule: $AM_REPO"
	[[ -d "$ADMIN_REPO" ]] || die "missing submodule: $ADMIN_REPO"
	[[ -f "$VC_CONFIG" ]] || die "missing Video Cloud config: $VC_CONFIG"
	[[ -f "$VC_SECRETS_FILE" ]] || die "missing Video Cloud secrets: $VC_SECRETS_FILE"
	[[ -f "$VC_STATE" || -f "$VC_SECRET_STATE" ]] || die "missing Video Cloud state"
	[[ -f "$AM_ENV" ]] || die "missing Account Manager env: $AM_ENV"
	[[ -f "$AM_STATE" ]] || die "missing Account Manager state: $AM_STATE"
	[[ -f "$ADMIN_ENV" ]] || die "missing Cloud Admin env: $ADMIN_ENV"
	[[ -f "$ADMIN_STATE" ]] || die "missing Cloud Admin state: $ADMIN_STATE"
	[[ -f "$SSH_KEY" ]] || die "missing SSH key: $SSH_KEY"
}

target_instance_filter='
	.data[]
	| select(
		.label == "rtk-cloud-admin-staging"
		or .label == "rtk-account-manager-staging"
		or ((.label | startswith("video-cloud-staging-")) and ((.tags // []) | index("video-cloud-staging")))
	)
'

ensure_live_targets() {
	local instances count
	instances="$(linode_api GET '/linode/instances?page_size=500')"
	count="$(jq -r "[$target_instance_filter] | length" <<<"$instances")"
	[[ "$count" == "7" ]] || die "expected 7 target VMs, found $count"
}

load_states() {
	if [[ ! -f "$VC_STATE" && -f "$VC_SECRET_STATE" ]]; then
		VC_STATE="$VC_SECRET_STATE"
	fi
	load_env_file "$AM_STATE"
	load_env_file "$ADMIN_STATE"
}

ensure_dns_converged() {
	local edge_ip ns failures=0
	edge_ip="$(jq -r '.instances.edge.public_ipv4' "$VC_STATE")"
	ns="$(authoritative_ns)"
	[[ -n "$ns" ]] || die "could not resolve authoritative NS for $DNS_ROOT_DOMAIN"
	for item in \
		"$VC_GATEWAY_DOMAIN $edge_ip" \
		"$VC_CERTISSUER_DOMAIN $edge_ip" \
		"$AM_DOMAIN $ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" \
		"$ADMIN_DOMAIN $ADMIN_LINODE_PUBLIC_IPV4"; do
		local domain="${item% *}"
		local ip="${item#* }"
		local google auth
		google="$(resolve_dns 8.8.8.8 "$domain")"
		auth="$(resolve_dns "$ns" "$domain")"
		debug "dns $domain expected=$ip google=${google:-<empty>} auth=${auth:-<empty>}"
		if [[ "$google" != "$ip" || "$auth" != "$ip" ]]; then
			printf 'DNS mismatch: %s expected=%s google=%s auth=%s\n' "$domain" "$ip" "${google:-<empty>}" "${auth:-<empty>}" >&2
			failures=$((failures + 1))
		fi
	done
	[[ "$failures" == "0" ]] || die "DNS is not converged"
}

init_report() {
	mkdir -p "$READY_DIR"
	: > "$STATUS_FILE"
	cat > "$REPORT" <<EOF_REPORT
# Workspace Staging Readiness Report

- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)
- video_release: $VIDEO_RELEASE
- account_release: $ACCOUNT_RELEASE
- admin_release: $ADMIN_RELEASE
- status: running

## Steps

EOF_REPORT
}

append_report_line() {
	printf '%s\n' "$*" >> "$REPORT"
}

run_step() {
	local name="$1"
	shift
	local log_file="$READY_DIR/${name//[^A-Za-z0-9_.-]/_}.log"
	log "step start: $name"
	if "$@" >"$log_file" 2>&1; then
		printf '%s\tPASS\t%s\n' "$name" "$log_file" >> "$STATUS_FILE"
		append_report_line "- PASS \`$name\` log: \`$log_file\`"
		log "step pass: $name"
	else
		local code=$?
		printf '%s\tFAIL\t%s\n' "$name" "$log_file" >> "$STATUS_FILE"
		append_report_line "- FAIL \`$name\` exit=$code log: \`$log_file\`"
		finalize_report "failed"
		die "step failed: $name; see $log_file"
	fi
}

finalize_report() {
	local status="$1"
	local tmp
	tmp="$(mktemp)"
	awk -v status="$status" '{gsub(/status: running/, "status: " status)} {print}' "$REPORT" > "$tmp"
	mv "$tmp" "$REPORT"
	{
		printf '\n## DNS\n\n'
		local edge_ip ns
		edge_ip="$(jq -r '.instances.edge.public_ipv4' "$VC_STATE")"
		ns="$(authoritative_ns)"
		printf '| Domain | Expected | 8.8.8.8 | %s |\n| --- | --- | --- | --- |\n' "$ns"
		for item in \
			"$VC_GATEWAY_DOMAIN $edge_ip" \
			"$VC_CERTISSUER_DOMAIN $edge_ip" \
			"$AM_DOMAIN $ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" \
			"$ADMIN_DOMAIN $ADMIN_LINODE_PUBLIC_IPV4"; do
			local domain="${item% *}"
			local ip="${item#* }"
			printf '| `%s` | `%s` | `%s` | `%s` |\n' "$domain" "$ip" "$(resolve_dns 8.8.8.8 "$domain")" "$(resolve_dns "$ns" "$domain")"
		done
		printf '\n## Service Artifact Paths\n\n'
		printf -- '- account_manager_verify: `%s/.artifacts/linode-account-manager-verify`\n' "$AM_REPO"
		printf -- '- video_cloud_verify_report: `%s`\n' "$READY_DIR/video-cloud-runtime-health.md"
		printf -- '- cloud_admin_verify: `%s/.artifacts/linode-admin-verify`\n' "$ADMIN_REPO"
	} >> "$REPORT"
}

account_manager_deploy() {
	(
		cd "$AM_REPO"
		set -a
		# shellcheck source=/dev/null
		. "$OPERATOR_ENV"
		# shellcheck source=/dev/null
		. "$AM_ENV"
		# shellcheck source=/dev/null
		. "$AM_STATE"
		set +a
		ACCOUNT_MANAGER_LINODE_RELEASE="$ACCOUNT_RELEASE" \
			linode_deploy/scripts/deploy-public-vm.sh
	)
}

account_manager_verify() {
	(
		cd "$AM_REPO"
		set -a
		# shellcheck source=/dev/null
		. "$AM_ENV"
		# shellcheck source=/dev/null
		. "$AM_STATE"
		set +a
		linode_deploy/scripts/verify-public-vm.sh
	)
}

video_cloud_deploy_and_verify() {
	(
		cd "$VC_REPO"
		DEPLOY_SECRETS_DIR="$SECRETS_ROOT/video-cloud" \
			linode_deploy/scripts/deploy-staging.sh \
				--config "$VC_CONFIG" \
				--secrets-file "$VC_SECRETS_FILE" \
				--env-file "$OPERATOR_ENV" \
				--release "$VIDEO_RELEASE" \
				--report "$READY_DIR/video-cloud-runtime-health.md"
	)
}

cloud_admin_deploy() {
	(
		cd "$ADMIN_REPO"
		set -a
		# shellcheck source=/dev/null
		. "$ADMIN_ENV"
		# shellcheck source=/dev/null
		. "$ADMIN_STATE"
		set +a
		ADMIN_LINODE_RELEASE="$ADMIN_RELEASE" \
			ACCOUNT_MANAGER_BASE_URL="https://$AM_DOMAIN" \
			VIDEO_CLOUD_BASE_URL="https://$VC_GATEWAY_DOMAIN" \
			deploy/linode/deploy-admin.sh
	)
}

cloud_admin_verify() {
	(
		cd "$ADMIN_REPO"
		set -a
		# shellcheck source=/dev/null
		. "$ADMIN_ENV"
		# shellcheck source=/dev/null
		. "$ADMIN_STATE"
		set +a
		ADMIN_LINODE_BASE_URL="https://$ADMIN_DOMAIN" \
			deploy/linode/verify-admin.sh
	)
}

for cmd in curl jq dig ssh go tar docker; do
	need_cmd "$cmd"
done
require_files
load_operator_env
load_states
mkdir -p "$READY_DIR"
ensure_live_targets
ensure_dns_converged
init_report

run_step account-manager-deploy account_manager_deploy
run_step account-manager-verify account_manager_verify
run_step video-cloud-deploy-verify video_cloud_deploy_and_verify
run_step cloud-admin-deploy cloud_admin_deploy
run_step cloud-admin-verify cloud_admin_verify
finalize_report "passed"
log "readiness report: $REPORT"
