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
DNS_ROOT_DOMAIN_EXPLICIT=0
GODADDY_ENVIRONMENT="prod"
DNS_TTL="${GODADDY_RECORD_TTL:-600}"
ARTIFACT_BASE=""
CERT_CACHE_ROOT=""
CERT_CACHE_MIN_VALID_SECONDS="${CLOUD_CERT_CACHE_MIN_VALID_SECONDS:-604800}"
VIDEO_RELEASE=""
ACCOUNT_RELEASE=""
ADMIN_RELEASE=""
ADMIN_RELEASE_BUNDLE="${ADMIN_RELEASE_BUNDLE:-}"
ACCOUNT_RELEASE_BUNDLE="${ACCOUNT_RELEASE_BUNDLE:-}"
VERBOSE=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[cloud-deploy] %s\n' "$*" >&2
}

debug() {
	if [[ "$VERBOSE" == "1" ]]; then
		printf '[cloud-deploy:debug] %s\n' "$*" >&2
	fi
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-deploy.sh --video-release VERSION --account-release VERSION --admin-release VERSION [options]

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
  --cert-cache-root PATH              Default: <env-root>/certificates.
  --cert-cache-min-valid-seconds N    Default: CLOUD_CERT_CACHE_MIN_VALID_SECONDS or 604800.
  --account-release-bundle PATH       Optional local Account Manager release bundle override.
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
	--account-release-bundle) ACCOUNT_RELEASE_BUNDLE="$2"; shift 2 ;;
	--admin-release-bundle) ADMIN_RELEASE_BUNDLE="$2"; shift 2 ;;
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
	--secrets-root) DEPRECATED_ENV_ROOT="$2"; ENV_ROOT="$2"; shift 2 ;;
	--ssh-key) SSH_KEY="$2"; shift 2 ;;
	--dns-root-domain) DNS_ROOT_DOMAIN="$2"; DNS_ROOT_DOMAIN_EXPLICIT=1; shift 2 ;;
	--godaddy-env) GODADDY_ENVIRONMENT="$2"; shift 2 ;;
	--dns-ttl) DNS_TTL="$2"; shift 2 ;;
	--artifact-dir) ARTIFACT_BASE="$2"; shift 2 ;;
	--cert-cache-root) CERT_CACHE_ROOT="$2"; shift 2 ;;
	--cert-cache-min-valid-seconds) CERT_CACHE_MIN_VALID_SECONDS="$2"; shift 2 ;;
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
if [[ "$DNS_ROOT_DOMAIN_EXPLICIT" == "1" ]]; then
	cloud_env_load_environment "$ENV_ROOT" "$DNS_ROOT_DOMAIN"
else
	cloud_env_load_environment "$ENV_ROOT" ""
fi
DNS_ROOT_DOMAIN="$CLOUD_DNS_ROOT_DOMAIN"
cloud_env_validate_environment "$ENV_ROOT"
cloud_env_export_filter_vars
DEPRECATED_ENV_ROOT="$ENV_ROOT"
OPERATOR_ENV="${OPERATOR_ENV:-$(cloud_env_operator_env "$ENV_ROOT")}"
ARTIFACT_BASE="${ARTIFACT_BASE:-$(cloud_env_artifacts_dir "$ENV_ROOT")}"
CERT_CACHE_ROOT="${CERT_CACHE_ROOT:-$(cloud_env_certificates_dir "$ENV_ROOT")}"
CERT_CACHE_ROOT="$(cloud_env_abs_path "$CERT_CACHE_ROOT")"
[[ "$CERT_CACHE_MIN_VALID_SECONDS" =~ ^[0-9]+$ ]] || die "--cert-cache-min-valid-seconds must be a non-negative integer"

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
LOGGER_ENV="$(cloud_env_logger_env "$ENV_ROOT")"
LOGGER_STATE="$(cloud_env_logger_state "$ENV_ROOT")"
CLOUD_LOGGER_SCRIPT="${CLOUD_LOGGER_SCRIPT:-$SCRIPT_DIR/cloud-logger.sh}"

VC_GATEWAY_DOMAIN="$VIDEO_CLOUD_DOMAIN"
VC_CERTISSUER_DOMAIN="$VIDEO_CLOUD_CERTISSUER_DOMAIN"
AM_DOMAIN="$ACCOUNT_MANAGER_DOMAIN"
ADMIN_DOMAIN="$CLOUD_ADMIN_DOMAIN"
VC_CERT_CACHE_DIR="$CERT_CACHE_ROOT/$VC_GATEWAY_DOMAIN"
AM_CERT_CACHE_DIR="$CERT_CACHE_ROOT/$AM_DOMAIN"
ADMIN_CERT_CACHE_DIR="$CERT_CACHE_ROOT/$ADMIN_DOMAIN"

READY_DIR="$ARTIFACT_BASE/readiness-$(date -u +%Y%m%dT%H%M%SZ)"
REPORT="$READY_DIR/readiness-report.md"
STATUS_FILE="$READY_DIR/status.tsv"
LOGGING_REPORT_STATUS="healthy"
DEPLOY_STEPS=(
	logger-provision-backend
	logger-install-forwarders
	account-manager-deploy
	account-manager-verify
	video-cloud-deploy-verify
	cloud-admin-deploy
	cloud-admin-verify
)

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

cert_cache_valid() {
	local domain="$1"
	local dir="$2"
	[[ -s "$dir/fullchain.pem" && -s "$dir/privkey.pem" ]] || return 1
	if ! openssl x509 -in "$dir/fullchain.pem" -noout -checkend "$CERT_CACHE_MIN_VALID_SECONDS" >/dev/null 2>&1; then
		return 1
	fi
	if ! openssl x509 -in "$dir/fullchain.pem" -noout -subject -issuer -enddate >/dev/null 2>&1; then
		return 1
	fi
	debug "certificate cache valid: domain=$domain dir=$dir"
	return 0
}

cert_cache_env_value() {
	local domain="$1"
	local dir="$2"
	if cert_cache_valid "$domain" "$dir"; then
		printf '%s\n' "$dir"
	else
		printf '\n'
	fi
}

cache_remote_certificate() {
	local label="$1"
	local host="$2"
	local domain="$3"
	local dir="$4"
	[[ -n "$host" ]] || return 0
	mkdir -p "$dir"
	local tmp
	tmp="$(mktemp -d)"
	local remote="$host"
	if [[ "$remote" != *@* ]]; then
		remote="root@$remote"
	fi
	local ssh_opts=(-i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
	if ! ssh "${ssh_opts[@]}" "$remote" "test -s /etc/letsencrypt/live/$domain/fullchain.pem -a -s /etc/letsencrypt/live/$domain/privkey.pem"; then
		debug "certificate not present on remote: label=$label domain=$domain host=$host"
		rm -rf "$tmp"
		return 0
	fi
	log "caching certificate: $label domain=$domain"
	if ! ssh "${ssh_opts[@]}" "$remote" "cat /etc/letsencrypt/live/$domain/fullchain.pem" > "$tmp/fullchain.pem"; then
		rm -rf "$tmp"
		return 1
	fi
	if ! ssh "${ssh_opts[@]}" "$remote" "cat /etc/letsencrypt/live/$domain/privkey.pem" > "$tmp/privkey.pem"; then
		rm -rf "$tmp"
		return 1
	fi
	for name in cert.pem chain.pem; do
		ssh "${ssh_opts[@]}" "$remote" "cat /etc/letsencrypt/live/$domain/$name" > "$tmp/$name" 2>/dev/null || true
	done
	if ! cert_cache_valid "$domain" "$tmp"; then
		log "warning: remote certificate was not cached because it is invalid or expires too soon: $domain"
		rm -rf "$tmp"
		return 0
	fi
	install -m 0644 "$tmp/fullchain.pem" "$dir/fullchain.pem"
	install -m 0600 "$tmp/privkey.pem" "$dir/privkey.pem"
	for name in cert.pem chain.pem; do
		if [[ -s "$tmp/$name" ]]; then
			install -m 0644 "$tmp/$name" "$dir/$name"
		fi
	done
	openssl x509 -in "$dir/fullchain.pem" -noout -enddate > "$dir/metadata.txt"
	printf 'domain=%s\ncached_at=%s\nsource_host=%s\n' "$domain" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$host" >> "$dir/metadata.txt"
	rm -rf "$tmp"
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

download_account_release_bundle() {
	if [[ -n "$ACCOUNT_RELEASE_BUNDLE" ]]; then
		[[ -s "$ACCOUNT_RELEASE_BUNDLE" ]] || die "Account Manager release bundle not found: $ACCOUNT_RELEASE_BUNDLE"
		return
	fi
	[[ -n "${LINODE_OBJ_BUCKET:-}" ]] || die "LINODE_OBJ_BUCKET is required"
	[[ -n "${LINODE_OBJ_ENDPOINT:-}" ]] || die "LINODE_OBJ_ENDPOINT is required"
	need_cmd "${LINODE_AWS_CLI_PATH:-aws}"
	local manifest_key manifest version object_key expected_sha bundle_dir checksum
	manifest_key="releases/rtk_account_manager-$ACCOUNT_RELEASE/manifest.json"
	manifest="$(object_storage_aws cp "s3://$LINODE_OBJ_BUCKET/$manifest_key" -)"
	version="$(jq -r '.version // empty' <<<"$manifest")"
	object_key="$(jq -r '.artifact_path // empty' <<<"$manifest")"
	expected_sha="$(jq -r '.sha256 // empty' <<<"$manifest")"
	[[ "$version" == "$ACCOUNT_RELEASE" ]] || die "Account Manager manifest version mismatch: requested=$ACCOUNT_RELEASE manifest=${version:-<empty>}"
	[[ -n "$object_key" ]] || die "Account Manager manifest missing artifact_path: $manifest_key"
	[[ -n "$expected_sha" ]] || die "Account Manager manifest missing sha256: $manifest_key"
	bundle_dir="$READY_DIR/releases/rtk_account_manager-$ACCOUNT_RELEASE"
	mkdir -p "$bundle_dir"
	ACCOUNT_RELEASE_BUNDLE="$bundle_dir/rtk_account_manager-$ACCOUNT_RELEASE.tar.gz"
	log "downloading Account Manager release artifact: $object_key"
	object_storage_aws cp "s3://$LINODE_OBJ_BUCKET/$object_key" "$ACCOUNT_RELEASE_BUNDLE" >/dev/null
	if command -v shasum >/dev/null 2>&1; then
		checksum="$(shasum -a 256 "$ACCOUNT_RELEASE_BUNDLE" | awk '{print $1}')"
	else
		checksum="$(sha256sum "$ACCOUNT_RELEASE_BUNDLE" | awk '{print $1}')"
	fi
	[[ "$checksum" == "$expected_sha" ]] || die "Account Manager artifact checksum mismatch: expected=$expected_sha got=$checksum"
	log "Account Manager release artifact ready: $ACCOUNT_RELEASE_BUNDLE"
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
		.label == env.ADMIN_LINODE_LABEL
		or .label == env.ACCOUNT_MANAGER_LINODE_LABEL
		or ((.label | startswith(env.VIDEO_CLOUD_LABEL_PREFIX + "-")) and ((.tags // []) | index(env.CLOUD_STACK_NAME)))
	)
'

ensure_live_targets() {
	local instances count
	instances="$(linode_api GET '/linode/instances?page_size=500')"
	count="$(jq -r "[$target_instance_filter] | length" <<<"$instances")"
	[[ "$count" == "7" ]] || die "expected 7 target VMs, found $count"
	hydrate_video_state_from_live "$instances"
}

hydrate_video_state_from_live() {
	local instances="$1"
	local current
	current="{}"
	if [[ -f "$VC_STATE" ]]; then
		current="$(cat "$VC_STATE")"
	fi
	mkdir -p "$(dirname "$VC_STATE")"
	jq -n \
		--arg stack "$CLOUD_STACK_NAME" \
		--arg region "$CLOUD_REGION" \
		--arg label_prefix "$VIDEO_CLOUD_LABEL_PREFIX" \
		--argjson current "$current" \
		--argjson instances "$instances" '
		def role_instance($role; $private):
			($instances.data[] | select(.label == ($label_prefix + "-" + $role))) as $i
			| {
				id: $i.id,
				role: $role,
				label: $i.label,
				public_ipv4: (($i.ipv4 // []) | .[0] // ""),
				public_ipv6: ($i.ipv6 // ""),
				private_ip: $private,
				tags: ($i.tags // [])
			};
		($current // {})
		| .stack = $stack
		| .region = $region
		| .instances = {
			edge: role_instance("edge"; "10.42.1.5"),
			api: role_instance("api"; "10.42.1.10"),
			infra: role_instance("infra"; "10.42.1.30"),
			mqtt: role_instance("mqtt"; "10.42.1.40"),
			coturn: role_instance("coturn"; "")
		}
		| .tags = [$stack, "managed-by:linode-deploy"]
	' > "$VC_STATE"
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
- logging: running
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
	local logging_status="${2:-$LOGGING_REPORT_STATUS}"
	local tmp
	tmp="$(mktemp)"
	awk -v status="$status" -v logging_status="$logging_status" '
		{gsub(/status: running/, "status: " status)}
		{gsub(/logging: running/, "logging: " logging_status)}
		{print}
	' "$REPORT" > "$tmp"
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

logger_host_for_target() {
	local target="$1"
	case "$target" in
	edge) jq -r '.instances.edge.public_ipv4 // ""' "$VC_STATE" ;;
	api|video-cloud-api) jq -r '.instances.api.private_ip // ""' "$VC_STATE" ;;
	infra|video-cloud-infra) jq -r '.instances.infra.private_ip // ""' "$VC_STATE" ;;
	mqtt) jq -r '.instances.mqtt.private_ip // ""' "$VC_STATE" ;;
	coturn) jq -r '.instances.coturn.public_ipv4 // ""' "$VC_STATE" ;;
	account-manager) printf '%s\n' "${ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4:-}" ;;
	cloud-admin) printf '%s\n' "${ADMIN_LINODE_PUBLIC_IPV4:-}" ;;
	frontend|non-go-host-sources) jq -r '.instances.edge.public_ipv4 // ""' "$VC_STATE" ;;
	*) printf '\n' ;;
	esac
}

run_logging_command() {
	local name="$1"
	shift
	local log_file="$READY_DIR/${name//[^A-Za-z0-9_.-]/_}.log"
	if [[ ! -x "$CLOUD_LOGGER_SCRIPT" && ! -f "$CLOUD_LOGGER_SCRIPT" ]]; then
		printf '%s\tSKIP\tmissing_logger_script=%s\n' "$name" "$CLOUD_LOGGER_SCRIPT" >> "$STATUS_FILE"
		append_report_line "- SKIP \`$name\` missing_logger_script=\`$CLOUD_LOGGER_SCRIPT\`"
		return 1
	fi
	if bash "$CLOUD_LOGGER_SCRIPT" "$@" > >(tee "$log_file") 2>&1; then
		printf '%s\tPASS\t%s\n' "$name" "$log_file" >> "$STATUS_FILE"
		append_report_line "- PASS \`$name\` log: \`$log_file\`"
		return 0
	fi
	local code=$?
	printf '%s\tDEGRADED\t%s\n' "$name" "$log_file" >> "$STATUS_FILE"
	append_report_line "- DEGRADED \`$name\` exit=$code log: \`$log_file\`"
	return 1
}

provision_logging_stack() {
	local degraded=0 target host normalized
	log "logger provision start: backend=$CLOUD_LOGGER_LINODE_LABEL"
	run_logging_command logger-provision-backend \
		provision-backend \
		--workspace "$WORKSPACE" \
		--env-root "$ENV_ROOT" \
		--operator-env "$OPERATOR_ENV" \
		--logger-env "$LOGGER_ENV" \
		--logger-state "$LOGGER_STATE" \
		--logger-label "$CLOUD_LOGGER_LINODE_LABEL" \
		--logger-firewall "$CLOUD_LOGGER_LINODE_FIREWALL_LABEL" \
		--logger-domain "$CLOUD_LOGGER_DOMAIN" || degraded=1

	for target in edge api infra mqtt coturn account-manager cloud-admin frontend non-go-host-sources; do
		host="$(logger_host_for_target "$target")"
		normalized="$target"
		[[ "$target" == "api" ]] && normalized="video-cloud-api"
		if [[ -z "$host" || "$host" == "null" ]]; then
			printf '%s\tDEGRADED\tmissing_host\n' "logger-forwarder:$normalized" >> "$STATUS_FILE"
			append_report_line "- DEGRADED \`logger-forwarder:$normalized\` missing_host"
			degraded=1
			continue
		fi
		run_logging_command "logger-forwarder:$normalized" \
			install-forwarder "$target" \
			--host "$host" \
			--ssh-key "$SSH_KEY" \
			--logger-env "$LOGGER_ENV" \
			--logger-state "$LOGGER_STATE" \
			--endpoint "$CLOUD_LOGGER_DOMAIN" \
			--journald-system-max-use "$CLOUD_LOGGER_JOURNALD_SYSTEM_MAX_USE" \
			--journald-system-keep-free "$CLOUD_LOGGER_JOURNALD_SYSTEM_KEEP_FREE" \
			--journald-max-retention-sec "$CLOUD_LOGGER_JOURNALD_MAX_RETENTION_SEC" || degraded=1
	done
	return "$degraded"
}

check_logging_readiness() {
	local degraded="$1" target normalized
	append_report_line ""
	append_report_line "## Service Logging Readiness"
	append_report_line ""
	run_logging_command logger-backend-health backend-health --logger-state "$LOGGER_STATE" --endpoint "$CLOUD_LOGGER_DOMAIN" || degraded=1
	for target in edge api infra mqtt coturn account-manager cloud-admin frontend non-go-host-sources; do
		normalized="$target"
		[[ "$target" == "api" ]] && normalized="video-cloud-api"
		run_logging_command "logger-forwarder:$normalized" forwarder-status "$target" --logger-state "$LOGGER_STATE" || degraded=1
	done
	run_logging_command logger-sample-trace-query sample-trace-query --logger-state "$LOGGER_STATE" --endpoint "$CLOUD_LOGGER_DOMAIN" || degraded=1
	return "$degraded"
}

account_manager_deploy() {
	download_account_release_bundle
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
		local cert_cache_dir
		cert_cache_dir="$(cert_cache_env_value "$AM_DOMAIN" "$AM_CERT_CACHE_DIR")"
		if [[ -n "$cert_cache_dir" ]]; then
			log "using cached certificate for Account Manager: $AM_DOMAIN"
		fi
		set +e
		ACCOUNT_MANAGER_LINODE_RELEASE="$ACCOUNT_RELEASE" \
			ACCOUNT_MANAGER_LINODE_RELEASE_BUNDLE="$ACCOUNT_RELEASE_BUNDLE" \
			ACCOUNT_MANAGER_LINODE_CERT_CACHE_DIR="$cert_cache_dir" \
			linode_deploy/scripts/deploy-public-vm.sh
		local status=$?
		set -e
		cache_remote_certificate "account-manager" "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" "$AM_DOMAIN" "$AM_CERT_CACHE_DIR"
		return "$status"
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
		local cert_cache_dir edge_ip
		cert_cache_dir="$(cert_cache_env_value "$VC_GATEWAY_DOMAIN" "$VC_CERT_CACHE_DIR")"
		edge_ip="$(jq -r '.instances.edge.public_ipv4' "$VC_STATE")"
		if [[ -n "$cert_cache_dir" ]]; then
			log "using cached certificate for Video Cloud gateway: $VC_GATEWAY_DOMAIN"
		fi
		set +e
		DEPLOY_SECRETS_DIR="$ENV_ROOT" \
			LINODE_DEPLOY_CERT_CACHE_DIR="$cert_cache_dir" \
			linode_deploy/scripts/deploy-staging.sh \
				--stack "$CLOUD_STACK_NAME" \
				--config "$VC_CONFIG" \
				--secrets-file "$VC_SECRETS_FILE" \
				--env-file "$OPERATOR_ENV" \
				--release "$VIDEO_RELEASE" \
				--gateway-domain "$VC_GATEWAY_DOMAIN" \
				--dns-domain "$DNS_ROOT_DOMAIN" \
				--dns-name "${VC_GATEWAY_DOMAIN%.$DNS_ROOT_DOMAIN}" \
				--report "$READY_DIR/video-cloud-runtime-health.md"
		local status=$?
		set -e
		cache_remote_certificate "video-cloud" "$edge_ip" "$VC_GATEWAY_DOMAIN" "$VC_CERT_CACHE_DIR"
		return "$status"
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
		local cert_cache_dir
		cert_cache_dir="$(cert_cache_env_value "$ADMIN_DOMAIN" "$ADMIN_CERT_CACHE_DIR")"
		if [[ -n "$cert_cache_dir" ]]; then
			log "using cached certificate for Cloud Admin: $ADMIN_DOMAIN"
		fi
		set +e
		ADMIN_LINODE_RELEASE="$ADMIN_RELEASE" \
			ADMIN_LINODE_RELEASE_BUNDLE="$ADMIN_RELEASE_BUNDLE" \
			ADMIN_LINODE_CERT_CACHE_DIR="$cert_cache_dir" \
			ACCOUNT_MANAGER_BASE_URL="https://$AM_DOMAIN" \
			VIDEO_CLOUD_BASE_URL="https://$VC_GATEWAY_DOMAIN" \
			deploy/linode/deploy-admin.sh
		local status=$?
		set -e
		cache_remote_certificate "cloud-admin" "$ADMIN_LINODE_PUBLIC_IPV4" "$ADMIN_DOMAIN" "$ADMIN_CERT_CACHE_DIR"
		return "$status"
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

for cmd in curl jq dig ssh go tar openssl; do
	need_cmd "$cmd"
done
require_files
load_operator_env
load_states
mkdir -p "$READY_DIR"
ensure_live_targets
ensure_dns_converged
init_report

LOGGING_DEGRADED=0
provision_logging_stack || LOGGING_DEGRADED=1
if [[ "$LOGGING_DEGRADED" == "1" ]]; then
	LOGGING_REPORT_STATUS="degraded"
fi
run_step account-manager-deploy account_manager_deploy
run_step account-manager-verify account_manager_verify
run_step video-cloud-deploy-verify video_cloud_deploy_and_verify
run_step cloud-admin-deploy cloud_admin_deploy
run_step cloud-admin-verify cloud_admin_verify
check_logging_readiness "$LOGGING_DEGRADED" || LOGGING_DEGRADED=1
if [[ "$LOGGING_DEGRADED" == "1" ]]; then
	LOGGING_REPORT_STATUS="degraded"
	finalize_report "passed" "degraded"
else
	LOGGING_REPORT_STATUS="healthy"
	finalize_report "passed" "healthy"
fi
log "readiness report: $REPORT"
