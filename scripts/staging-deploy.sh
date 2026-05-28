#!/usr/bin/env bash
if [ -z "${BASH_VERSION:-}" ]; then
	exec /usr/bin/env bash "$0" "$@"
fi
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_ROOT=""
DEPRECATED_ENV_ROOT=""
OPERATOR_ENV=""
SSH_KEY="$HOME/.ssh/id_ed25519_rtkcloud"
DNS_ROOT_DOMAIN="realtekconnect.com"
GODADDY_ENVIRONMENT="prod"
DNS_TTL="${GODADDY_RECORD_TTL:-600}"
ARTIFACT_BASE=""
VIDEO_RELEASE=""
ACCOUNT_RELEASE=""
ADMIN_RELEASE=""
ADMIN_RELEASE_BUNDLE="${ADMIN_RELEASE_BUNDLE:-}"
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
  --operator-env PATH                 Default: <env-root>/env/operator.env.
  --env-root PATH                     Required environment directory, for example cloud_env/staging.
  --secrets-root PATH                 Deprecated alias for --env-root.
  --ssh-key PATH                      Default: ~/.ssh/id_ed25519_rtkcloud.
  --dns-root-domain NAME              Default: realtekconnect.com.
  --godaddy-env ENV                   Default: prod.
  --dns-ttl SECONDS                   Default: GODADDY_RECORD_TTL or 600.
  --artifact-dir PATH                 Default: <env-root>/artifacts.
  --admin-release-bundle PATH         Optional local Cloud Admin release bundle override.
  --verbose                           Print extra diagnostics.
  -h, --help                          Show this help.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--video-release) VIDEO_RELEASE="$2"; shift 2 ;;
	--account-release) ACCOUNT_RELEASE="$2"; shift 2 ;;
	--admin-release) ADMIN_RELEASE="$2"; shift 2 ;;
	--admin-release-bundle) ADMIN_RELEASE_BUNDLE="$2"; shift 2 ;;
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
	--secrets-root) DEPRECATED_ENV_ROOT="$2"; ENV_ROOT="$2"; shift 2 ;;
	--ssh-key) SSH_KEY="$2"; shift 2 ;;
	--dns-root-domain) DNS_ROOT_DOMAIN="$2"; shift 2 ;;
	--godaddy-env) GODADDY_ENVIRONMENT="$2"; shift 2 ;;
	--dns-ttl) DNS_TTL="$2"; shift 2 ;;
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
[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
[[ "$DNS_TTL" =~ ^[0-9]+$ && "$DNS_TTL" -gt 0 ]] || die "--dns-ttl must be a positive integer"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
DEPRECATED_ENV_ROOT="$ENV_ROOT"
OPERATOR_ENV="${OPERATOR_ENV:-$(cloud_env_operator_env "$ENV_ROOT")}"
ARTIFACT_BASE="${ARTIFACT_BASE:-$(cloud_env_artifacts_dir "$ENV_ROOT")}"

VC_REPO="$WORKSPACE/repos/rtk_video_cloud"
AM_REPO="$WORKSPACE/repos/rtk_account_manager"
ADMIN_REPO="$WORKSPACE/repos/rtk_cloud_admin"

VC_CONFIG="$(cloud_env_video_config "$ENV_ROOT")"
VC_STATE="$(cloud_env_video_state "$ENV_ROOT")"
VC_SECRET_STATE="$VC_STATE"
VC_SECRETS_FILE="$(cloud_env_video_env "$ENV_ROOT")"
AM_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
AM_STATE="$(cloud_env_account_manager_state "$ENV_ROOT")"
ADMIN_ENV="$(cloud_env_admin_env "$ENV_ROOT")"
ADMIN_STATE="$(cloud_env_admin_state "$ENV_ROOT")"

VC_GATEWAY_DOMAIN="video-cloud-staging.$DNS_ROOT_DOMAIN"
VC_CERTISSUER_DOMAIN="certissuer.video-cloud-staging.$DNS_ROOT_DOMAIN"
AM_DOMAIN="account-manager.video-cloud-staging.$DNS_ROOT_DOMAIN"
ADMIN_DOMAIN="admin.video-cloud-staging.$DNS_ROOT_DOMAIN"

READY_DIR="$ARTIFACT_BASE/readiness-$(date -u +%Y%m%dT%H%M%SZ)"
REPORT="$READY_DIR/readiness-report.md"
STATUS_FILE="$READY_DIR/status.tsv"
DEPLOY_STEPS=(
	account-manager-deploy
	account-manager-verify
	video-cloud-deploy-verify
	cloud-admin-deploy
	cloud-admin-verify
)

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

object_storage_aws() {
	local aws_path="${LINODE_AWS_CLI_PATH:-aws}"
	AWS_ACCESS_KEY_ID="${LINODE_OBJ_ACCESS_KEY_ID:-}" \
		AWS_SECRET_ACCESS_KEY="${LINODE_OBJ_SECRET_ACCESS_KEY:-}" \
		"$aws_path" s3 "$@" --endpoint-url "$LINODE_OBJ_ENDPOINT"
}

download_admin_release_bundle() {
	if [[ -n "$ADMIN_RELEASE_BUNDLE" ]]; then
		[[ -s "$ADMIN_RELEASE_BUNDLE" ]] || die "Cloud Admin release bundle not found: $ADMIN_RELEASE_BUNDLE"
		return
	fi
	[[ -n "${LINODE_OBJ_BUCKET:-}" ]] || die "LINODE_OBJ_BUCKET is required"
	[[ -n "${LINODE_OBJ_ENDPOINT:-}" ]] || die "LINODE_OBJ_ENDPOINT is required"
	need_cmd "${LINODE_AWS_CLI_PATH:-aws}"
	local manifest_key manifest version object_key expected_sha bundle_dir checksum
	manifest_key="releases/rtk_cloud_admin-$ADMIN_RELEASE/manifest.json"
	manifest="$(object_storage_aws cp "s3://$LINODE_OBJ_BUCKET/$manifest_key" -)"
	version="$(jq -r '.version // empty' <<<"$manifest")"
	object_key="$(jq -r '.artifact_path // empty' <<<"$manifest")"
	expected_sha="$(jq -r '.sha256 // empty' <<<"$manifest")"
	[[ "$version" == "$ADMIN_RELEASE" ]] || die "Cloud Admin manifest version mismatch: requested=$ADMIN_RELEASE manifest=${version:-<empty>}"
	[[ -n "$object_key" ]] || die "Cloud Admin manifest missing artifact_path: $manifest_key"
	[[ -n "$expected_sha" ]] || die "Cloud Admin manifest missing sha256: $manifest_key"
	bundle_dir="$READY_DIR/releases/rtk_cloud_admin-$ADMIN_RELEASE"
	mkdir -p "$bundle_dir"
	ADMIN_RELEASE_BUNDLE="$bundle_dir/rtk_cloud_admin-$ADMIN_RELEASE.tar.gz"
	log "downloading Cloud Admin release artifact: $object_key"
	object_storage_aws cp "s3://$LINODE_OBJ_BUCKET/$object_key" "$ADMIN_RELEASE_BUNDLE" >/dev/null
	if command -v shasum >/dev/null 2>&1; then
		checksum="$(shasum -a 256 "$ADMIN_RELEASE_BUNDLE" | awk '{print $1}')"
	else
		checksum="$(sha256sum "$ADMIN_RELEASE_BUNDLE" | awk '{print $1}')"
	fi
	[[ "$checksum" == "$expected_sha" ]] || die "Cloud Admin artifact checksum mismatch: expected=$expected_sha got=$checksum"
	log "Cloud Admin release artifact ready: $ADMIN_RELEASE_BUNDLE"
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
	if "$@" > >(tee "$log_file") 2>&1; then
		printf '%s\tPASS\t%s\n' "$name" "$log_file" >> "$STATUS_FILE"
		append_report_line "- PASS \`$name\` log: \`$log_file\`"
		log "step pass: $name"
	else
		local code=$?
		printf '%s\tFAIL\t%s\n' "$name" "$log_file" >> "$STATUS_FILE"
		append_report_line "- FAIL \`$name\` exit=$code log: \`$log_file\`"
		mark_skipped_after_failure "$name"
		finalize_report "failed"
		log "readiness report: $REPORT"
		die "step failed: $name; see $log_file"
	fi
}

mark_skipped_after_failure() {
	local failed="$1"
	local step seen_failed=0
	for step in "${DEPLOY_STEPS[@]}"; do
		if [[ "$seen_failed" == "1" ]]; then
			printf '%s\tSKIP\tblocked_by=%s\n' "$step" "$failed" >> "$STATUS_FILE"
			append_report_line "- SKIP \`$step\` blocked_by=\`$failed\`"
			log "step skip: $step blocked_by=$failed"
		fi
		if [[ "$step" == "$failed" ]]; then
			seen_failed=1
		fi
	done
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
		DEPLOY_SECRETS_DIR="$ENV_ROOT" \
			linode_deploy/scripts/deploy-staging.sh \
				--config "$VC_CONFIG" \
				--secrets-file "$VC_SECRETS_FILE" \
				--env-file "$OPERATOR_ENV" \
				--release "$VIDEO_RELEASE" \
				--report "$READY_DIR/video-cloud-runtime-health.md"
	)
}

cloud_admin_deploy() {
	download_admin_release_bundle
	(
		cd "$ADMIN_REPO"
		set -a
		# shellcheck source=/dev/null
		. "$ADMIN_ENV"
		# shellcheck source=/dev/null
		. "$ADMIN_STATE"
		set +a
		ADMIN_LINODE_RELEASE="$ADMIN_RELEASE" \
			ADMIN_LINODE_RELEASE_BUNDLE="$ADMIN_RELEASE_BUNDLE" \
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

for cmd in curl jq dig ssh go tar; do
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
