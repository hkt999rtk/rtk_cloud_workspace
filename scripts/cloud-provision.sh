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
DNS_WAIT_TTL="${GODADDY_WAIT_TTL:-${GODADDY_RECORD_WAIT_TTL:-600}}"
DNS_FINAL_TTL="${GODADDY_RECORD_TTL:-600}"
GODADDY_MIN_TTL=600
DNS_WAIT_MAX_SECONDS="${DNS_WAIT_MAX_SECONDS:-700}"
DNS_WAIT_INTERVAL_SECONDS="${DNS_WAIT_INTERVAL_SECONDS:-10}"
ARTIFACT_BASE=""
CONFIRM=""
VERBOSE=0
VIDEO_RELEASE="${VIDEO_RELEASE:-}"
ACCOUNT_RELEASE="${ACCOUNT_RELEASE:-}"
ADMIN_RELEASE="${ADMIN_RELEASE:-}"

DO_PREFLIGHT=0
DO_PLAN=0
DO_RESET=0
DO_APPLY=0
DO_DNS=0
DO_DEPLOY=0
DO_ARTIFACTS=0
DO_E2E=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[cloud-provision] %s\n' "$*" >&2
}

debug() {
	if [[ "$VERBOSE" == "1" ]]; then
		printf '[cloud-provision:debug] %s\n' "$*" >&2
	fi
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-provision.sh [modes] [options]
  scripts/cloud-provision.sh --env-root cloud_env/staging  # default: --plan

Modes:
  default                            Same as --plan; read-only.
  --preflight                         Check local tools, env, credentials, SSH key, and optional release artifact.
  --plan                              Print current and intended cloud resources without mutation.
  --reset --confirm <stack-name>       Delete the 7 target VMs, 7 firewalls, and Video Cloud VPC.
  --apply                             Create/recreate the 7 target VMs and Video Cloud VPC.
  --dns                               Upsert and wait for the 4 staging A records.
  --deploy                            Deploy Video Cloud, Account Manager, and Cloud Admin releases.
  --artifacts                         Write combined provision artifacts.
  --e2e                               Run public endpoint smoke checks and write an E2E report.
  --all                               preflight -> plan -> apply -> dns -> deploy -> artifacts -> e2e.
  --reset-and-all --confirm ...       preflight -> plan -> reset -> apply -> dns -> deploy -> artifacts -> e2e.

Options:
  --workspace PATH                    Default: script parent workspace.
  --operator-env PATH                 Default: <env-root>/env/operator.env.
  --env-root PATH                     Required environment directory, for example cloud_env/staging.
  --secrets-root PATH                 Deprecated alias for --env-root.
  --ssh-key PATH                      Default: ~/.ssh/id_ed25519_rtkcloud.
  --dns-root-domain NAME              Default: realtekconnect.com.
  --godaddy-env ENV                   Default: prod.
  --dns-wait-ttl SECONDS              TTL used while waiting for DNS convergence. Default: GODADDY_WAIT_TTL, GODADDY_RECORD_WAIT_TTL, or 600.
  --dns-final-ttl SECONDS             TTL restored after DNS convergence. Default: GODADDY_RECORD_TTL or 600.
  --dns-ttl SECONDS                   Backward-compatible alias for --dns-final-ttl.
  --dns-wait-max-seconds SECONDS      Max DNS convergence wait per hostname. Default: 700.
  --artifact-dir PATH                 Default: <env-root>/artifacts.
  --video-release VERSION             Optional; otherwise select from Object Storage releases.
  --account-release VERSION           Optional; otherwise select from Object Storage releases.
  --admin-release VERSION             Optional; otherwise select from Object Storage releases.
  --verbose                           Print extra diagnostics.
  -h, --help                          Show this help.

Safety:
  reset requires --confirm matching CLOUD_STACK_NAME. DNS and Object Storage are never deleted.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--preflight) DO_PREFLIGHT=1; shift ;;
	--plan) DO_PLAN=1; shift ;;
	--reset) DO_RESET=1; shift ;;
	--apply) DO_APPLY=1; shift ;;
	--dns) DO_DNS=1; shift ;;
	--deploy) DO_DEPLOY=1; shift ;;
	--artifacts) DO_ARTIFACTS=1; shift ;;
	--e2e) DO_E2E=1; shift ;;
	--all)
		DO_PREFLIGHT=1; DO_PLAN=1; DO_APPLY=1; DO_DNS=1; DO_DEPLOY=1; DO_ARTIFACTS=1; DO_E2E=1; shift ;;
	--reset-and-all)
		DO_PREFLIGHT=1; DO_PLAN=1; DO_RESET=1; DO_APPLY=1; DO_DNS=1; DO_DEPLOY=1; DO_ARTIFACTS=1; DO_E2E=1; shift ;;
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
	--secrets-root) DEPRECATED_ENV_ROOT="$2"; ENV_ROOT="$2"; shift 2 ;;
	--ssh-key) SSH_KEY="$2"; shift 2 ;;
	--dns-root-domain) DNS_ROOT_DOMAIN="$2"; DNS_ROOT_DOMAIN_EXPLICIT=1; shift 2 ;;
	--godaddy-env) GODADDY_ENVIRONMENT="$2"; shift 2 ;;
	--dns-wait-ttl) DNS_WAIT_TTL="$2"; shift 2 ;;
	--dns-final-ttl|--dns-ttl) DNS_FINAL_TTL="$2"; shift 2 ;;
	--dns-wait-max-seconds) DNS_WAIT_MAX_SECONDS="$2"; shift 2 ;;
	--artifact-dir) ARTIFACT_BASE="$2"; shift 2 ;;
	--video-release) VIDEO_RELEASE="$2"; shift 2 ;;
	--account-release) ACCOUNT_RELEASE="$2"; shift 2 ;;
	--admin-release) ADMIN_RELEASE="$2"; shift 2 ;;
	--confirm) CONFIRM="$2"; shift 2 ;;
	--verbose) VERBOSE=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

if [[ "$DO_PREFLIGHT$DO_PLAN$DO_RESET$DO_APPLY$DO_DNS$DO_DEPLOY$DO_ARTIFACTS$DO_E2E" == "00000000" ]]; then
	DO_PLAN=1
fi
[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
[[ "$DNS_WAIT_TTL" =~ ^[0-9]+$ && "$DNS_WAIT_TTL" -gt 0 ]] || die "--dns-wait-ttl must be a positive integer"
[[ "$DNS_FINAL_TTL" =~ ^[0-9]+$ && "$DNS_FINAL_TTL" -gt 0 ]] || die "--dns-final-ttl must be a positive integer"
[[ "$DNS_WAIT_MAX_SECONDS" =~ ^[0-9]+$ && "$DNS_WAIT_MAX_SECONDS" -gt 0 ]] || die "--dns-wait-max-seconds must be a positive integer"
[[ "$DNS_WAIT_INTERVAL_SECONDS" =~ ^[0-9]+$ && "$DNS_WAIT_INTERVAL_SECONDS" -gt 0 ]] || die "DNS_WAIT_INTERVAL_SECONDS must be a positive integer"
[[ "$DNS_WAIT_TTL" -ge "$GODADDY_MIN_TTL" ]] || die "--dns-wait-ttl must be >= $GODADDY_MIN_TTL for GoDaddy DNS records"
[[ "$DNS_FINAL_TTL" -ge "$GODADDY_MIN_TTL" ]] || die "--dns-final-ttl must be >= $GODADDY_MIN_TTL for GoDaddy DNS records"
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
CLOUD_DEPLOY_SCRIPT="${CLOUD_DEPLOY_SCRIPT:-${STAGING_DEPLOY_SCRIPT:-$SCRIPT_DIR/cloud-deploy.sh}}"

VC_GATEWAY_DOMAIN="$VIDEO_CLOUD_DOMAIN"
VC_CERTISSUER_DOMAIN="$VIDEO_CLOUD_CERTISSUER_DOMAIN"
AM_DOMAIN="$ACCOUNT_MANAGER_DOMAIN"
ADMIN_DOMAIN="$CLOUD_ADMIN_DOMAIN"

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

env_file_has_key() {
	local path="$1"
	local key="$2"
	[[ -f "$path" ]] && grep -Eq "^${key}=" "$path"
}

append_secret_if_missing() {
	local path="$1"
	local key="$2"
	local value="$3"
	if ! env_file_has_key "$path" "$key"; then
		printf '%s=%s\n' "$key" "$value" >> "$path"
		log "generated Video Cloud secret: $key"
	fi
}

ensure_video_cloud_factory_enroll_secrets() {
	mkdir -p "$(dirname "$VC_SECRETS_FILE")"
	touch "$VC_SECRETS_FILE"
	chmod 0600 "$VC_SECRETS_FILE"
	local key_dir="$WORKSPACE/keys/staging/linode/video-cloud"
	local edge_private_ip
	edge_private_ip="$(cloud_env_yaml_path_value "$VC_CONFIG" instances.edge.private_ip)"
	edge_private_ip="${edge_private_ip:-10.42.1.5}"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_URL "https://$VC_GATEWAY_DOMAIN"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_AUTH_KEY "$(openssl rand -hex 32)"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_AUDIT_LOG_PATH "/var/log/video_cloud/factoryenroll-audit.jsonl"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_CERT_ISSUER_URL "https://$VC_CERTISSUER_DOMAIN"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_CERT_ISSUER_PROXY_URL "http://$edge_private_ip:3128"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT_SOURCE "$key_dir/factoryenroll-client.ed25519.cert.pem"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY_SOURCE "$key_dir/factoryenroll-client.ed25519.key.pem"
	append_secret_if_missing "$VC_SECRETS_FILE" FACTORY_ENROLL_CERT_ISSUER_CA_SOURCE "$key_dir/root-ca.ed25519.cert.pem"
}

load_operator_env() {
	[[ -f "$OPERATOR_ENV" ]] || die "operator env not found: $OPERATOR_ENV"
	load_env_file "$OPERATOR_ENV"
	[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"
}

linode_api() {
	local method="$1"
	local path="$2"
	local data="${3:-}"
	if [[ -n "$data" ]]; then
		curl -fsS -X "$method" "https://api.linode.com/v4$path" \
			-H "Authorization: Bearer $LINODE_TOKEN" \
			-H 'Content-Type: application/json' \
			--data-binary "$data"
	else
		curl -fsS -X "$method" "https://api.linode.com/v4$path" \
			-H "Authorization: Bearer $LINODE_TOKEN" \
			-H 'Content-Type: application/json'
	fi
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

object_storage_aws() {
	local aws_path="${LINODE_AWS_CLI_PATH:-aws}"
	AWS_ACCESS_KEY_ID="${LINODE_OBJ_ACCESS_KEY_ID:-}" \
		AWS_SECRET_ACCESS_KEY="${LINODE_OBJ_SECRET_ACCESS_KEY:-}" \
		"$aws_path" s3 "$@" --endpoint-url "$LINODE_OBJ_ENDPOINT"
}

list_release_manifest_keys() {
	local prefix="$1"
	object_storage_aws ls "s3://$LINODE_OBJ_BUCKET/releases/" --recursive \
		| awk -v prefix="$prefix" '$4 ~ ("^releases/" prefix "-[^/]+/manifest\\.json$") {print $1 " " $2 "\t" $4}' \
		| sort -r
}

version_from_manifest_key() {
	local prefix="$1"
	local key="$2"
	key="${key#releases/$prefix-}"
	printf '%s\n' "${key%/manifest.json}"
}

select_release_manifest_key() {
	local display="$1"
	local prefix="$2"
	local list_file count i line modified key version choice
	list_file="$(mktemp "/tmp/${prefix}-releases.XXXXXX")"
	list_release_manifest_keys "$prefix" > "$list_file"
	count="$(wc -l < "$list_file" | tr -d ' ')"
	if [[ "$count" == "0" ]]; then
		rm -f "$list_file"
		die "no $prefix release manifest found in Object Storage under releases/"
	fi
	printf 'Available %s releases in Object Storage:\n' "$display" >&2
	i=1
	while IFS=$'\t' read -r modified key; do
		version="$(version_from_manifest_key "$prefix" "$key")"
		printf '  %d) %s  %s\n' "$i" "$version" "$modified" >&2
		i=$((i + 1))
	done < "$list_file"
	if [[ -t 0 ]]; then
		printf 'Select %s release [1]: ' "$display" >&2
		read -r choice
		choice="${choice:-1}"
	else
		choice=1
		log "non-interactive shell; selecting newest $display Object Storage release"
	fi
	if ! [[ "$choice" =~ ^[0-9]+$ ]] || (( choice < 1 || choice > count )); then
		rm -f "$list_file"
		die "invalid release selection: $choice"
	fi
	line="$(sed -n "${choice}p" "$list_file")"
	rm -f "$list_file"
	printf '%s\n' "${line#*$'\t'}"
}

check_object_storage_release() {
	local display="$1"
	local prefix="$2"
	local requested="$3"
	[[ -n "${LINODE_OBJ_BUCKET:-}" ]] || die "LINODE_OBJ_BUCKET is required"
	[[ -n "${LINODE_OBJ_ENDPOINT:-}" ]] || die "LINODE_OBJ_ENDPOINT is required"
	need_cmd "${LINODE_AWS_CLI_PATH:-aws}"
	local manifest_key manifest version object_key
	if [[ -n "$requested" ]]; then
		manifest_key="releases/$prefix-$requested/manifest.json"
	else
		manifest_key="$(select_release_manifest_key "$display" "$prefix")"
	fi
	manifest="$(object_storage_aws cp "s3://$LINODE_OBJ_BUCKET/$manifest_key" -)"
	version="$(jq -r '.version // empty' <<<"$manifest")"
	object_key="$(jq -r '.artifact_path // empty' <<<"$manifest")"
	[[ -n "$version" ]] || die "latest Object Storage manifest missing version: $manifest_key"
	[[ -n "$object_key" ]] || die "latest Object Storage manifest missing artifact_path: $manifest_key"
	[[ -z "$requested" || "$requested" == "$version" ]] || die "$display manifest version mismatch: requested $requested but $manifest_key contains $version"
	log "selected $display Object Storage release: $version"
	object_storage_aws ls "s3://$LINODE_OBJ_BUCKET/$object_key" >/dev/null
	log "$display Object Storage release readable: $object_key"
	printf '%s\n' "$version"
}

check_latest_video_release() {
	VIDEO_RELEASE="$(check_object_storage_release "Video Cloud" "rtk_video_cloud" "$VIDEO_RELEASE")"
}

check_latest_account_release() {
	ACCOUNT_RELEASE="$(check_object_storage_release "Account Manager" "rtk_account_manager" "$ACCOUNT_RELEASE")"
}

check_latest_admin_release() {
	ADMIN_RELEASE="$(check_object_storage_release "Cloud Admin" "rtk_cloud_admin" "$ADMIN_RELEASE")"
}

resolve_deploy_releases() {
	if [[ -z "$VIDEO_RELEASE" ]]; then
		check_latest_video_release
	fi
	if [[ -z "$ACCOUNT_RELEASE" ]]; then
		check_latest_account_release
	fi
	if [[ -z "$ADMIN_RELEASE" ]]; then
		check_latest_admin_release
	fi
	log "deploy releases: video=$VIDEO_RELEASE account=$ACCOUNT_RELEASE admin=$ADMIN_RELEASE"
}

expand_path() {
	local path="$1"
	case "$path" in
	~/*) printf '%s/%s\n' "$HOME" "${path#~/}" ;;
	*) printf '%s\n' "$path" ;;
	esac
}

current_public_cidr() {
	local ip
	ip="$(curl -fsS https://api.ipify.org || true)"
	[[ -n "$ip" ]] || return 1
	printf '%s/32\n' "$ip"
}

record_name_for_domain() {
	local domain="$1"
	if [[ "$domain" == "$DNS_ROOT_DOMAIN" ]]; then
		printf '@\n'
		return
	fi
	[[ "$domain" == *".$DNS_ROOT_DOMAIN" ]] || die "domain is not under $DNS_ROOT_DOMAIN: $domain"
	printf '%s\n' "${domain%.$DNS_ROOT_DOMAIN}"
}

godaddy_tool() {
	printf '%s/tools/godaddy-dns\n' "$VC_REPO"
}

godaddy_upsert_a() {
	local domain="$1"
	local ip="$2"
	local ttl="$3"
	local purpose="${4:-update}"
	local name
	local attempt delay=5
	name="$(record_name_for_domain "$domain")"
	for attempt in $(seq 1 5); do
		log "GoDaddy upsert $purpose attempt $attempt/5: $domain A $ip ttl=$ttl"
		if (
			cd "$(godaddy_tool)"
			GODADDY_ENV="$GODADDY_ENVIRONMENT" go run ./cmd/godaddy-dns \
				--env-file "$OPERATOR_ENV" \
				records upsert "$DNS_ROOT_DOMAIN" --type A --name "$name" --data "$ip" --ttl "$ttl"
		); then
			return 0
		fi
		log "GoDaddy upsert $purpose failed for $domain attempt $attempt/5; retrying in ${delay}s"
		sleep "$delay"
		delay=$((delay * 2))
	done
	return 1
}

resolve_dns() {
	local server="$1"
	local domain="$2"
	dig +short "@$server" "$domain" | tail -n 1 || true
}

authoritative_ns() {
	dig NS "$DNS_ROOT_DOMAIN" +short | head -n 1
}

wait_dns() {
	local domain="$1"
	local ip="$2"
	local ns
	local got_google got_auth attempt max_attempts
	max_attempts=$(((DNS_WAIT_MAX_SECONDS + DNS_WAIT_INTERVAL_SECONDS - 1) / DNS_WAIT_INTERVAL_SECONDS))
	ns="$(authoritative_ns)"
	[[ -n "$ns" ]] || die "could not resolve authoritative NS for $DNS_ROOT_DOMAIN"
	for attempt in $(seq 1 "$max_attempts"); do
		got_google="$(resolve_dns 8.8.8.8 "$domain")"
		got_auth="$(resolve_dns "$ns" "$domain")"
		if [[ "$got_google" == "$ip" && "$got_auth" == "$ip" ]]; then
			log "DNS converged: $domain -> $ip"
			return 0
		fi
		log "waiting DNS attempt $attempt/$max_attempts: $domain expected=$ip google=${got_google:-<empty>} auth=${got_auth:-<empty>}"
		sleep "$DNS_WAIT_INTERVAL_SECONDS"
	done
	die "DNS did not converge: $domain expected=$ip google=${got_google:-<empty>} auth=${got_auth:-<empty>}"
}

target_instance_filter='
	.data[]
	| select(
		.label == env.ADMIN_LINODE_LABEL
		or .label == env.ACCOUNT_MANAGER_LINODE_LABEL
		or ((.label | startswith(env.VIDEO_CLOUD_LABEL_PREFIX + "-")) and ((.tags // []) | index(env.CLOUD_STACK_NAME)))
	)
'

target_firewall_filter='
	.data[]
	| select(
		.label == env.ADMIN_LINODE_FIREWALL_LABEL
		or .label == env.ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL
		or ((.label | startswith(env.VIDEO_CLOUD_LABEL_PREFIX + "-")) and ((.tags // []) | index(env.CLOUD_STACK_NAME)))
	)
'

preflight() {
	log "preflight"
	for cmd in curl jq dig ssh openssl go tar; do
		need_cmd "$cmd"
	done
	ensure_video_cloud_factory_enroll_secrets
	[[ -d "$VC_REPO" ]] || die "missing submodule: $VC_REPO"
	[[ -d "$AM_REPO" ]] || die "missing submodule: $AM_REPO"
	[[ -d "$ADMIN_REPO" ]] || die "missing submodule: $ADMIN_REPO"
	[[ -f "$VC_CONFIG" ]] || die "missing Video Cloud config: $VC_CONFIG"
	[[ -f "$VC_SECRETS_FILE" ]] || die "missing Video Cloud secrets file: $VC_SECRETS_FILE"
	[[ -f "$AM_ENV" ]] || die "missing Account Manager env: $AM_ENV"
	[[ -f "$ADMIN_ENV" ]] || die "missing Cloud Admin env: $ADMIN_ENV"
	[[ -f "$SSH_KEY" ]] || die "missing SSH key: $SSH_KEY"
	[[ -f "$SSH_KEY.pub" ]] || die "missing SSH public key: $SSH_KEY.pub"
	load_operator_env
	[[ -n "${GODADDY_KEY:-${GODADDY_API_KEY:-}}" ]] || die "GoDaddy key is required"
	[[ -n "${GODADDY_SECRET:-${GODADDY_API_SECRET:-}}" ]] || die "GoDaddy secret is required"
	local cidr
	cidr="$(current_public_cidr)" || die "could not determine current public IP"
	log "operator public CIDR: $cidr"
	check_latest_video_release
	check_latest_account_release
	check_latest_admin_release
	if [[ "$DO_DEPLOY" == "1" ]]; then
		resolve_deploy_releases
	fi
}

plan() {
	log "plan"
	load_operator_env
	local instances firewalls vpcs
	instances="$(linode_api GET '/linode/instances?page_size=500')"
	firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
	vpcs="$(linode_api GET '/vpcs?page_size=500')"
	printf 'Target instances:\n'
	jq -r "$target_instance_filter | [.id,.label,.region,.type,.status,((.ipv4 // []) | join(\",\")),((.tags // []) | join(\",\"))] | @tsv" <<<"$instances" || true
	printf '\nTarget firewalls:\n'
	jq -r "$target_firewall_filter | [.id,.label,.status,((.tags // []) | join(\",\"))] | @tsv" <<<"$firewalls" || true
	printf '\nTarget VPCs:\n'
	jq -r --arg label "$VIDEO_CLOUD_VPC_LABEL" '.data[] | select(.label == $label) | [.id,.label,.region] | @tsv' <<<"$vpcs" || true
	printf '\nIntended resources:\n'
	cat <<EOF_PLAN
- instances: $VIDEO_CLOUD_LABEL_PREFIX-edge/api/infra/mqtt/coturn, $ACCOUNT_MANAGER_LINODE_LABEL, $ADMIN_LINODE_LABEL
- firewalls: $VIDEO_CLOUD_LABEL_PREFIX-edge/api/infra/mqtt/coturn, $ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL, $ADMIN_LINODE_FIREWALL_LABEL
- vpc/subnet: $VIDEO_CLOUD_VPC_LABEL / $VIDEO_CLOUD_SUBNET_LABEL
- dns: $VC_GATEWAY_DOMAIN, $VC_CERTISSUER_DOMAIN, $AM_DOMAIN, $ADMIN_DOMAIN
- logger backend: $CLOUD_LOGGER_LINODE_LABEL
- logger firewall: $CLOUD_LOGGER_LINODE_FIREWALL_LABEL
- logger endpoint: $CLOUD_LOGGER_DOMAIN
- logger env: $LOGGER_ENV
- logger state: $LOGGER_STATE
- forwarder credentials: generated after logger backend provisioning
- forwarder targets: $CLOUD_LOGGER_FORWARDER_TARGETS
- journald retention: SystemMaxUse=$CLOUD_LOGGER_JOURNALD_SYSTEM_MAX_USE SystemKeepFree=$CLOUD_LOGGER_JOURNALD_SYSTEM_KEEP_FREE MaxRetentionSec=$CLOUD_LOGGER_JOURNALD_MAX_RETENTION_SEC
- readiness evidence: logger backend health, per-host forwarder status, sample trace query
EOF_PLAN
}

backup_state_files() {
	local ts backup_dir
	ts="$(date -u +%Y%m%dT%H%M%SZ)"
	backup_dir="$DEPRECATED_ENV_ROOT/full-reset-backup-$ts"
	mkdir -p "$backup_dir"
	for file in "$VC_STATE" "$VC_SECRET_STATE" "$AM_STATE" "$ADMIN_STATE"; do
		if [[ -f "$file" ]]; then
			mkdir -p "$backup_dir/$(dirname "${file#$WORKSPACE/}")"
			cp "$file" "$backup_dir/${file#$WORKSPACE/}"
		fi
	done
	printf '%s\n' "$backup_dir"
}

reset_stack() {
	[[ "$CONFIRM" == "$CLOUD_STACK_NAME" ]] || die "reset requires --confirm $CLOUD_STACK_NAME"
	log "reset"
	load_operator_env
	local backup_dir tmp instances firewalls vpcs count remaining
	backup_dir="$(backup_state_files)"
	log "state backup: $backup_dir"
	tmp="$(mktemp -d /tmp/rtk-staging-reset.XXXXXX)"
	trap 'rm -rf "$tmp"' RETURN
	instances="$(linode_api GET '/linode/instances?page_size=500')"
	jq -r "$target_instance_filter | .id" <<<"$instances" > "$tmp/instance_ids"
	jq -r "$target_instance_filter | .label" <<<"$instances" > "$tmp/instance_labels"
	count="$(wc -l < "$tmp/instance_ids" | tr -d ' ')"
	if [[ "$count" != "0" && "$count" != "7" ]]; then
		cat "$tmp/instance_labels" >&2
		die "partial target instance set found: $count/7. Refusing reset."
	fi
	if [[ "$count" == "7" ]]; then
		log "deleting instances: $(paste -sd ' ' "$tmp/instance_labels")"
		while IFS= read -r id; do
			[[ -n "$id" ]] && linode_delete_ignore_missing "/linode/instances/$id"
		done < "$tmp/instance_ids"
		for _ in $(seq 1 60); do
			remaining="$(linode_api GET '/linode/instances?page_size=500' | jq -r "[$target_instance_filter] | length")"
			[[ "$remaining" == "0" ]] && break
			sleep 10
		done
		[[ "$remaining" == "0" ]] || die "timed out waiting for instance deletion"
	fi
	firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
	jq -r "$target_firewall_filter | .id" <<<"$firewalls" > "$tmp/firewall_ids"
	log "deleting firewalls: $(paste -sd ' ' "$tmp/firewall_ids")"
	while IFS= read -r id; do
		[[ -n "$id" ]] && linode_delete_ignore_missing "/networking/firewalls/$id"
	done < "$tmp/firewall_ids"
	vpcs="$(linode_api GET '/vpcs?page_size=500')"
	jq -r --arg label "$VIDEO_CLOUD_VPC_LABEL" '.data[] | select(.label == $label) | .id' <<<"$vpcs" > "$tmp/vpc_ids"
	log "deleting VPCs: $(paste -sd ' ' "$tmp/vpc_ids")"
	while IFS= read -r id; do
		[[ -n "$id" ]] && linode_delete_ignore_missing "/vpcs/$id"
	done < "$tmp/vpc_ids"
	rm -f "$VC_STATE" "$VC_SECRET_STATE" "$AM_STATE" "$ADMIN_STATE"
}

delete_firewalls_matching_filter() {
	local filter="$1"
	local firewalls tmp id label
	firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
	tmp="$(mktemp /tmp/rtk-firewalls.XXXXXX)"
	jq -r "$filter | [.id,.label] | @tsv" <<<"$firewalls" > "$tmp"
	while IFS=$'\t' read -r id label; do
		[[ -n "$id" ]] || continue
		log "deleting orphan firewall $label ($id)"
		linode_delete_ignore_missing "/networking/firewalls/$id"
	done < "$tmp"
	rm -f "$tmp"
}

delete_vpcs_matching_label() {
	local label="$1"
	local vpcs tmp id vpc_label
	vpcs="$(linode_api GET '/vpcs?page_size=500')"
	tmp="$(mktemp /tmp/rtk-vpcs.XXXXXX)"
	jq -r --arg label "$label" '.data[] | select(.label == $label) | [.id,.label] | @tsv' <<<"$vpcs" > "$tmp"
	while IFS=$'\t' read -r id vpc_label; do
		[[ -n "$id" ]] || continue
		log "deleting orphan VPC $vpc_label ($id)"
		linode_delete_ignore_missing "/vpcs/$id"
	done < "$tmp"
	rm -f "$tmp"
}

cleanup_orphan_video_cloud_infra() {
	log "cleaning orphan Video Cloud firewalls/VPC/state before fresh apply"
	delete_firewalls_matching_filter "$target_firewall_filter | select(.label | startswith(env.VIDEO_CLOUD_LABEL_PREFIX + \"-\"))"
	delete_vpcs_matching_label "$VIDEO_CLOUD_VPC_LABEL"
	rm -f "$VC_STATE" "$VC_SECRET_STATE"
}

cleanup_orphan_public_service() {
	local label="$1"
	local firewall_label="$2"
	local state_path="$3"
	log "cleaning orphan firewall/state for missing VM $label"
	delete_firewalls_matching_filter ".data[] | select(.label == \"$firewall_label\")"
	rm -f "$state_path"
}

run_video_cloud_apply() {
	local repo_state repo_backup_dir repo_backup
	repo_state="$VC_REPO/linode_deploy/state/$CLOUD_STACK_NAME.state.json"
	repo_backup_dir="$ARTIFACT_BASE/legacy-state-backup-$(date -u +%Y%m%dT%H%M%SZ)"
	if [[ -e "$repo_state" || -L "$repo_state" ]]; then
		mkdir -p "$repo_backup_dir"
		repo_backup="$repo_backup_dir/$CLOUD_STACK_NAME.state.json"
		mv "$repo_state" "$repo_backup"
		log "moved legacy Video Cloud repo state backup: $repo_backup"
	fi
	(
		cd "$VC_REPO/linode_deploy"
		go run ./cmd/linode-deploy apply --config "$VC_CONFIG"
	)
	if [[ -f "$repo_state" ]]; then
		mkdir -p "$(dirname "$VC_STATE")"
		cp "$repo_state" "$VC_STATE"
		rm -f "$repo_state"
	fi
}

load_service_envs() {
	load_env_file "$AM_ENV"
	load_env_file "$ADMIN_ENV"
}

create_admin_vm() {
	load_service_envs
	local label="${ADMIN_LINODE_LABEL:-$ADMIN_LINODE_LABEL}"
	local region="${ADMIN_LINODE_REGION:-$CLOUD_REGION}"
	local type="${ADMIN_LINODE_TYPE:-g6-standard-2}"
	local image="${ADMIN_LINODE_IMAGE:-linode/ubuntu24.04}"
	local public_key_path
	local allowed_ssh_cidrs="${ADMIN_LINODE_ALLOWED_SSH_CIDRS:-}"
	local firewall_label="${ADMIN_LINODE_FIREWALL_LABEL:-${label}-firewall}"
	public_key_path="$(expand_path "${ADMIN_LINODE_PUBLIC_KEY_PATH:-$SSH_KEY.pub}")"
	[[ -s "$public_key_path" ]] || die "admin public key not found: $public_key_path"
	[[ -n "$allowed_ssh_cidrs" ]] || die "ADMIN_LINODE_ALLOWED_SSH_CIDRS is required"
	if linode_api GET '/linode/instances?page_size=500' | jq -e --arg label "$label" '.data[] | select(.label == $label)' >/dev/null; then
		die "Cloud Admin Linode already exists: $label"
	fi
	local current_cidr merged_cidrs root_pass ssh_key create_payload create_json linode_id public_ipv4 firewall_payload firewall_json firewall_id
	current_cidr="$(current_public_cidr || true)"
	merged_cidrs="$allowed_ssh_cidrs"
	if [[ -n "$current_cidr" && ",$merged_cidrs," != *",$current_cidr,"* ]]; then
		merged_cidrs="$merged_cidrs,$current_cidr"
	fi
	root_pass="$(openssl rand -base64 36)"
	ssh_key="$(cat "$public_key_path")"
	create_payload="$(jq -cn \
		--arg label "$label" --arg region "$region" --arg type "$type" --arg image "$image" \
		--arg root_pass "$root_pass" --arg ssh_key "$ssh_key" \
		--arg env_tag "$CLOUD_STACK_NAME" \
		'{label:$label, region:$region, type:$type, image:$image, root_pass:$root_pass, authorized_keys:[$ssh_key], tags:[$env_tag,"admin-deploy"]}')"
	log "creating Cloud Admin Linode $label"
	create_json="$(linode_api POST /linode/instances "$create_payload")"
	linode_id="$(jq -r '.id' <<<"$create_json")"
	public_ipv4="$(jq -r '.ipv4[0]' <<<"$create_json")"
	firewall_payload="$(jq -cn --arg label "$firewall_label" --arg cidrs "$merged_cidrs" '{
		label:$label,
		rules:{
			inbound_policy:"DROP",
			outbound_policy:"ACCEPT",
			inbound:[
				{label:"ssh",action:"ACCEPT",protocol:"TCP",ports:"22",addresses:{ipv4:($cidrs|split(","))}},
				{label:"http",action:"ACCEPT",protocol:"TCP",ports:"80",addresses:{ipv4:["0.0.0.0/0"],ipv6:["::/0"]}},
				{label:"https",action:"ACCEPT",protocol:"TCP",ports:"443",addresses:{ipv4:["0.0.0.0/0"],ipv6:["::/0"]}}
			],
			outbound:[]
		}
	}')"
	log "creating Cloud Admin firewall $firewall_label"
	firewall_json="$(linode_api POST /networking/firewalls "$firewall_payload")"
	firewall_id="$(jq -r '.id' <<<"$firewall_json")"
	linode_api POST "/networking/firewalls/$firewall_id/devices" "$(jq -cn --argjson id "$linode_id" '{id:$id,type:"linode"}')" >/dev/null
	mkdir -p "$(dirname "$ADMIN_STATE")"
	cat > "$ADMIN_STATE" <<STATE
ADMIN_LINODE_ID=$linode_id
ADMIN_LINODE_LABEL=$label
ADMIN_LINODE_PUBLIC_IPV4=$public_ipv4
ADMIN_LINODE_HOST=$public_ipv4
ADMIN_LINODE_FIREWALL_ID=$firewall_id
ADMIN_LINODE_FIREWALL_LABEL=$firewall_label
STATE
	chmod 0600 "$ADMIN_STATE"
}

hydrate_video_state_from_live() {
	local instances firewalls vpcs vpc_id subnet_id
	instances="$(linode_api GET '/linode/instances?page_size=500')"
	firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
	vpcs="$(linode_api GET '/vpcs?page_size=500')"
	vpc_id="$(jq -r --arg label "$VIDEO_CLOUD_VPC_LABEL" '.data[] | select(.label == $label) | .id' <<<"$vpcs")"
	[[ -n "$vpc_id" && "$vpc_id" != "null" ]] || die "cannot hydrate Video Cloud state: missing $VIDEO_CLOUD_VPC_LABEL"
	subnet_id="$(linode_api GET "/vpcs/$vpc_id/subnets" | jq -r --arg label "$VIDEO_CLOUD_SUBNET_LABEL" '.data[] | select(.label == $label) | .id' | head -n 1)"
	[[ -n "$subnet_id" && "$subnet_id" != "null" ]] || die "cannot hydrate Video Cloud state: missing $VIDEO_CLOUD_SUBNET_LABEL"
	mkdir -p "$(dirname "$VC_STATE")" "$(dirname "$VC_SECRET_STATE")"
	jq -n \
		--arg stack "$CLOUD_STACK_NAME" \
		--arg region "$CLOUD_REGION" \
		--arg label_prefix "$VIDEO_CLOUD_LABEL_PREFIX" \
		--arg stack_tag "$CLOUD_STACK_NAME" \
		--argjson vpc_id "$vpc_id" \
		--argjson subnet_id "$subnet_id" \
		--argjson instances "$instances" \
		--argjson firewalls "$firewalls" '{
		stack:$stack,
		region:$region,
		vpc_id:$vpc_id,
		subnet_id:$subnet_id,
		firewalls:(
			$firewalls.data
			| map(select((.label | startswith($label_prefix + "-")) and ((.tags // []) | index($stack_tag))))
			| map({role: ((.tags // []) | map(select(startswith("role:"))) | .[0] | sub("^role:";"")), id})
			| map(select(.role != null and .role != ""))
			| map({key:.role, value:.id})
			| from_entries
		),
		instances:(
			$instances.data
			| map(select((.label | startswith($label_prefix + "-")) and ((.tags // []) | index($stack_tag))))
			| map({
				role: ((.tags // []) | map(select(startswith("role:"))) | .[0] | sub("^role:";"")),
				value: {
					id,
					role: ((.tags // []) | map(select(startswith("role:"))) | .[0] | sub("^role:";"")),
					label,
					public_ipv4: ((.ipv4 // []) | .[0] // ""),
					public_ipv6: (.ipv6 // ""),
					private_ip: (
						if (.label | endswith("-edge")) then "10.42.1.5"
						elif (.label | endswith("-api")) then "10.42.1.10"
						elif (.label | endswith("-infra")) then "10.42.1.30"
						elif (.label | endswith("-mqtt")) then "10.42.1.40"
						else "" end
					),
					tags: (.tags // [])
				}
			})
			| map(select(.role != null and .role != ""))
			| map({key:.role, value:.value})
			| from_entries
		),
		tags:[$stack_tag,"managed-by:linode-deploy"]
	}' > "$VC_STATE"
	if [[ "$VC_STATE" != "$VC_SECRET_STATE" ]]; then
		cp "$VC_STATE" "$VC_SECRET_STATE"
	fi
}

ensure_video_cloud_state_or_apply() {
	local instances count
	instances="$(linode_api GET '/linode/instances?page_size=500')"
	count="$(jq -r --arg label_prefix "$VIDEO_CLOUD_LABEL_PREFIX" --arg stack_tag "$CLOUD_STACK_NAME" '[.data[] | select((.label | startswith($label_prefix + "-")) and ((.tags // []) | index($stack_tag)))] | length' <<<"$instances")"
	if [[ "$count" == "5" ]]; then
		log "Video Cloud instances already exist; hydrating state and skipping apply"
		hydrate_video_state_from_live
	elif [[ "$count" == "0" ]]; then
		cleanup_orphan_video_cloud_infra
		run_video_cloud_apply
		mkdir -p "$(dirname "$VC_SECRET_STATE")"
		if [[ "$VC_STATE" != "$VC_SECRET_STATE" ]]; then
			cp "$VC_STATE" "$VC_SECRET_STATE"
		fi
	else
		die "partial Video Cloud instance set found: $count/5"
	fi
}

hydrate_public_state_from_live() {
	local label="$1"
	local firewall_label="$2"
	local state_path="$3"
	local prefix="$4"
	local instances firewalls id ip firewall_id
	instances="$(linode_api GET '/linode/instances?page_size=500')"
	firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
	id="$(jq -r --arg label "$label" '.data[] | select(.label == $label) | .id' <<<"$instances")"
	ip="$(jq -r --arg label "$label" '.data[] | select(.label == $label) | .ipv4[0]' <<<"$instances")"
	firewall_id="$(jq -r --arg label "$firewall_label" '.data[] | select(.label == $label) | .id' <<<"$firewalls")"
	[[ -n "$id" && "$id" != "null" && -n "$ip" && "$ip" != "null" ]] || die "cannot hydrate state for $label"
	[[ -n "$firewall_id" && "$firewall_id" != "null" ]] || die "cannot hydrate firewall state for $firewall_label"
	mkdir -p "$(dirname "$state_path")"
	cat > "$state_path" <<STATE
${prefix}_LINODE_ID=$id
${prefix}_LINODE_LABEL=$label
${prefix}_LINODE_PUBLIC_IPV4=$ip
${prefix}_LINODE_HOST=$ip
${prefix}_LINODE_FIREWALL_ID=$firewall_id
${prefix}_LINODE_FIREWALL_LABEL=$firewall_label
STATE
	chmod 0600 "$state_path"
}

update_public_firewall_ssh_allowlist() {
	local firewall_id="$1"
	local cidrs_csv="$2"
	local current_cidr rules updated
	current_cidr="$(current_public_cidr || true)"
	if [[ -n "$current_cidr" && ",$cidrs_csv," != *",$current_cidr,"* ]]; then
		cidrs_csv="$cidrs_csv,$current_cidr"
	fi
	rules="$(linode_api GET "/networking/firewalls/$firewall_id/rules")"
	updated="$(jq --arg cidrs "$cidrs_csv" '.inbound |= map(if .label == "ssh" then .addresses.ipv4 = ($cidrs | split(",") | unique) else . end)' <<<"$rules")"
	linode_api PUT "/networking/firewalls/$firewall_id/rules" "$updated" >/dev/null
}

account_manager_private_ipv4() {
	printf '%s\n' "${ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4:-10.42.1.50}"
}

account_manager_vpc_cidr() {
	local cidr
	cidr="$(cloud_env_yaml_path_value "$VC_CONFIG" vpc.subnet.cidr)"
	printf '%s\n' "${cidr:-10.42.1.0/24}"
}

write_state_var() {
	local path="$1"
	local key="$2"
	local value="$3"
	local tmp
	tmp="$(mktemp)"
	if [[ -f "$path" ]]; then
		grep -vE "^${key}=" "$path" > "$tmp"
	fi
	printf '%s=%s\n' "$key" "$value" >> "$tmp"
	install -m 0600 "$tmp" "$path"
	rm -f "$tmp"
}

ensure_account_manager_vpc_interface() {
	local private_ip subnet_id linode_id configs config_id config updated
	private_ip="$(account_manager_private_ipv4)"
	subnet_id="$(jq -r '.subnet_id // empty' "$VC_STATE")"
	linode_id="${ACCOUNT_MANAGER_LINODE_ID:-}"
	[[ -n "$linode_id" && "$linode_id" != "null" ]] || die "ACCOUNT_MANAGER_LINODE_ID is required before VPC interface ensure"
	[[ -n "$subnet_id" && "$subnet_id" != "null" ]] || die "Video Cloud subnet_id is required before Account Manager VPC interface ensure"
	configs="$(linode_api GET "/linode/instances/$linode_id/configs")"
	config_id="$(jq -r '.data[0].id // empty' <<<"$configs")"
	[[ -n "$config_id" && "$config_id" != "null" ]] || die "cannot find Account Manager Linode config for $linode_id"
	config="$(linode_api GET "/linode/instances/$linode_id/configs/$config_id")"
	if jq -e --arg private_ip "$private_ip" '.interfaces[]? | select(.purpose == "vpc" and .ipv4.vpc == $private_ip)' <<<"$config" >/dev/null; then
		write_state_var "$AM_STATE" ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4 "$private_ip"
		return 0
	fi
	log "adding Account Manager VPC interface: linode=$linode_id subnet=$subnet_id private_ip=$private_ip"
	updated="$(jq --argjson subnet_id "$subnet_id" --arg private_ip "$private_ip" '
		.interfaces = [
			{purpose:"public", primary:true},
			{purpose:"vpc", subnet_id:$subnet_id, ipv4:{vpc:$private_ip}}
		]
	' <<<"$config")"
	linode_api PUT "/linode/instances/$linode_id/configs/$config_id" "$updated" >/dev/null
	write_state_var "$AM_STATE" ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4 "$private_ip"
	log "rebooting Account Manager to activate VPC interface"
	linode_api POST "/linode/instances/$linode_id/reboot" '{}' >/dev/null
}

apply_stack() {
	log "apply"
	load_operator_env
	ensure_video_cloud_factory_enroll_secrets
	load_service_envs
	ensure_video_cloud_state_or_apply
	if linode_api GET '/linode/instances?page_size=500' | jq -e --arg label "$ACCOUNT_MANAGER_LINODE_LABEL" '.data[] | select(.label == $label)' >/dev/null; then
		log "Account Manager VM already exists; hydrating state and skipping provision"
		hydrate_public_state_from_live "$ACCOUNT_MANAGER_LINODE_LABEL" "$ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL" "$AM_STATE" "ACCOUNT_MANAGER"
	else
		cleanup_orphan_public_service "$ACCOUNT_MANAGER_LINODE_LABEL" "$ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL" "$AM_STATE"
		(
			cd "$AM_REPO"
			ACCOUNT_MANAGER_LINODE_VPC_SUBNET_ID="$(jq -r '.subnet_id' "$VC_STATE")" \
				ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4="$(account_manager_private_ipv4)" \
				ACCOUNT_MANAGER_LINODE_VPC_CIDR="$(account_manager_vpc_cidr)" \
			ACCOUNT_MANAGER_LINODE_STATE_PATH="$AM_STATE" \
				linode_deploy/scripts/provision-public-vm.sh
		)
	fi
	load_env_file "$AM_STATE"
	ensure_account_manager_vpc_interface
	if linode_api GET '/linode/instances?page_size=500' | jq -e --arg label "$ADMIN_LINODE_LABEL" '.data[] | select(.label == $label)' >/dev/null; then
		log "Cloud Admin VM already exists; hydrating state and skipping provision"
		hydrate_public_state_from_live "$ADMIN_LINODE_LABEL" "$ADMIN_LINODE_FIREWALL_LABEL" "$ADMIN_STATE" "ADMIN"
	else
		cleanup_orphan_public_service "$ADMIN_LINODE_LABEL" "$ADMIN_LINODE_FIREWALL_LABEL" "$ADMIN_STATE"
		create_admin_vm
	fi
	load_env_file "$AM_STATE"
	load_env_file "$ADMIN_STATE"
	update_public_firewall_ssh_allowlist "$ACCOUNT_MANAGER_LINODE_FIREWALL_ID" "${ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS:-}"
	update_public_firewall_ssh_allowlist "$ADMIN_LINODE_FIREWALL_ID" "${ADMIN_LINODE_ALLOWED_SSH_CIDRS:-}"
}

require_state() {
	[[ -f "$VC_STATE" ]] || die "missing Video Cloud state: $VC_STATE"
	[[ -f "$AM_STATE" ]] || die "missing Account Manager state: $AM_STATE"
	[[ -f "$ADMIN_STATE" ]] || die "missing Cloud Admin state: $ADMIN_STATE"
}

dns_stack() {
	log "dns"
	load_operator_env
	require_state
	load_env_file "$AM_STATE"
	load_env_file "$ADMIN_STATE"
	local edge_ip
	edge_ip="$(jq -r '.instances.edge.public_ipv4' "$VC_STATE")"
	log "DNS wait TTL: $DNS_WAIT_TTL"
	godaddy_upsert_a "$VC_GATEWAY_DOMAIN" "$edge_ip" "$DNS_WAIT_TTL" "wait-ttl"
	godaddy_upsert_a "$VC_CERTISSUER_DOMAIN" "$edge_ip" "$DNS_WAIT_TTL" "wait-ttl"
	godaddy_upsert_a "$AM_DOMAIN" "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" "$DNS_WAIT_TTL" "wait-ttl"
	godaddy_upsert_a "$ADMIN_DOMAIN" "$ADMIN_LINODE_PUBLIC_IPV4" "$DNS_WAIT_TTL" "wait-ttl"
	wait_dns "$VC_GATEWAY_DOMAIN" "$edge_ip"
	wait_dns "$VC_CERTISSUER_DOMAIN" "$edge_ip"
	wait_dns "$AM_DOMAIN" "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4"
	wait_dns "$ADMIN_DOMAIN" "$ADMIN_LINODE_PUBLIC_IPV4"
	log "restoring DNS final TTL: $DNS_FINAL_TTL"
	godaddy_upsert_a "$VC_GATEWAY_DOMAIN" "$edge_ip" "$DNS_FINAL_TTL" "final-ttl"
	godaddy_upsert_a "$VC_CERTISSUER_DOMAIN" "$edge_ip" "$DNS_FINAL_TTL" "final-ttl"
	godaddy_upsert_a "$AM_DOMAIN" "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" "$DNS_FINAL_TTL" "final-ttl"
	godaddy_upsert_a "$ADMIN_DOMAIN" "$ADMIN_LINODE_PUBLIC_IPV4" "$DNS_FINAL_TTL" "final-ttl"
}

deploy_stack() {
	log "deploy"
	load_operator_env
	ensure_video_cloud_factory_enroll_secrets
	require_state
	[[ -x "$CLOUD_DEPLOY_SCRIPT" || -f "$CLOUD_DEPLOY_SCRIPT" ]] || die "missing cloud deploy implementation: $CLOUD_DEPLOY_SCRIPT"
	resolve_deploy_releases
	local args=(
		--workspace "$WORKSPACE"
		--operator-env "$OPERATOR_ENV"
		--secrets-root "$DEPRECATED_ENV_ROOT"
		--ssh-key "$SSH_KEY"
		--dns-root-domain "$DNS_ROOT_DOMAIN"
		--godaddy-env "$GODADDY_ENVIRONMENT"
		--dns-ttl "$DNS_FINAL_TTL"
		--artifact-dir "$ARTIFACT_BASE"
		--video-release "$VIDEO_RELEASE"
		--account-release "$ACCOUNT_RELEASE"
		--admin-release "$ADMIN_RELEASE"
	)
	if [[ "$VERBOSE" == "1" ]]; then
		args+=(--verbose)
	fi
	log "deploy start: Video Cloud release=$VIDEO_RELEASE"
	log "deploy start: Account Manager release=$ACCOUNT_RELEASE"
	log "deploy start: Cloud Admin release=$ADMIN_RELEASE"
	if ! bash "$CLOUD_DEPLOY_SCRIPT" "${args[@]}"; then
		die "deploy failed; artifacts and e2e were not run"
	fi
	log "deploy complete"
}

ssh_ready() {
	local host="$1"
	local proxy="${2:-}"
	local args=(-i "$SSH_KEY" -o BatchMode=yes -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)
	if [[ -n "$proxy" ]]; then
		args+=(-o ProxyCommand="$proxy")
	fi
	ssh "${args[@]}" "root@$host" true >/dev/null 2>&1
}

check_ssh_with_retries() {
	local role="$1"
	local host="$2"
	local proxy="${3:-}"
	local ok="no"
	local attempt route="direct"
	if [[ -n "$proxy" ]]; then
		route="proxy_jump via ${proxy##* }"
	fi
	for attempt in $(seq 1 30); do
		log "SSH readiness attempt $attempt/30: role=$role host=$host route=$route"
		if ssh_ready "$host" "$proxy"; then
			ok="yes"
			log "SSH readiness ok: role=$role host=$host attempt=$attempt/30"
			break
		fi
		log "SSH readiness pending: role=$role host=$host attempt=$attempt/30; retrying in 10s"
		sleep 10
	done
	if [[ "$ok" != "yes" ]]; then
		log "SSH readiness failed: role=$role host=$host attempts=30"
	fi
	printf '%s\n' "$ok"
}

report_value() {
	local value="${1:-}"
	if [[ -n "$value" && "$value" != "null" ]]; then
		printf '%s\n' "$value"
	else
		printf 'N/A\n'
	fi
}

network_profile() {
	local public_ip="${1:-}"
	local private_ip="${2:-}"
	if [[ -n "$public_ip" && "$public_ip" != "null" && -n "$private_ip" && "$private_ip" != "null" ]]; then
		printf 'public+vpc\n'
	elif [[ -n "$public_ip" && "$public_ip" != "null" ]]; then
		printf 'public\n'
	else
		printf 'private\n'
	fi
}

write_video_vm_config_rows() {
	local role edge_public label id firewall public_ip private_ip profile access proxy
	edge_public="$(jq -r '.instances.edge.public_ipv4 // ""' "$VC_STATE")"
	for role in edge api infra mqtt coturn; do
		label="$(jq -r --arg role "$role" '.instances[$role].label // ""' "$VC_STATE")"
		id="$(jq -r --arg role "$role" '.instances[$role].id // ""' "$VC_STATE")"
		firewall="$(jq -r --arg role "$role" '.firewalls[$role] // ""' "$VC_STATE")"
		public_ip="$(jq -r --arg role "$role" '.instances[$role].public_ipv4 // ""' "$VC_STATE")"
		private_ip="$(jq -r --arg role "$role" '.instances[$role].private_ip // ""' "$VC_STATE")"
		profile="$(network_profile "$public_ip" "$private_ip")"
		access="direct public SSH"
		proxy="N/A"
		if [[ "$role" == "api" || "$role" == "infra" || "$role" == "mqtt" ]]; then
			access="VPC via edge ProxyJump"
			proxy="root@$edge_public"
		fi
		printf '| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` |\n' \
			"$role" \
			"$(report_value "$label")" \
			"$(report_value "$id")" \
			"$(report_value "$firewall")" \
			"$profile" \
			"$(report_value "$public_ip")" \
			"$(report_value "$private_ip")" \
			"$access" \
			"$proxy"
	done
}

write_public_vm_config_row() {
	local role="$1"
	local label="$2"
	local id="$3"
	local firewall="$4"
	local public_ip="$5"
	local private_ip="${6:-}"
	local profile
	profile="$(network_profile "$public_ip" "$private_ip")"
	printf '| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `direct public SSH` | `N/A` |\n' \
		"$role" \
		"$(report_value "$label")" \
		"$(report_value "$id")" \
		"$(report_value "$firewall")" \
		"$profile" \
		"$(report_value "$public_ip")" \
		"$(report_value "$private_ip")"
}

write_artifacts() {
	log "artifacts"
	load_operator_env
	require_state
	load_env_file "$AM_STATE"
	load_env_file "$ADMIN_STATE"
	mkdir -p "$ARTIFACT_BASE"
	local ts art edge_ip coturn_ip api_ip infra_ip mqtt_ip ns tmp proxy_cmd
	ts="$(date -u +%Y%m%dT%H%M%SZ)"
	art="$ARTIFACT_BASE/provision-$ts"
	mkdir -p "$art"
	edge_ip="$(jq -r '.instances.edge.public_ipv4' "$VC_STATE")"
	coturn_ip="$(jq -r '.instances.coturn.public_ipv4' "$VC_STATE")"
	api_ip="$(jq -r '.instances.api.private_ip' "$VC_STATE")"
	infra_ip="$(jq -r '.instances.infra.private_ip' "$VC_STATE")"
	mqtt_ip="$(jq -r '.instances.mqtt.private_ip' "$VC_STATE")"
	ns="$(authoritative_ns)"
	tmp="$(mktemp -d /tmp/rtk-provision-artifacts.XXXXXX)"
	trap 'rm -rf "$tmp"' RETURN
	jq --arg stack "$CLOUD_STACK_NAME" \
		--arg am_id "$ACCOUNT_MANAGER_LINODE_ID" --arg am_label "$ACCOUNT_MANAGER_LINODE_LABEL" --arg am_ip "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" --arg am_private "${ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4:-}" --arg am_fw "$ACCOUNT_MANAGER_LINODE_FIREWALL_ID" \
		--arg ad_id "$ADMIN_LINODE_ID" --arg ad_label "$ADMIN_LINODE_LABEL" --arg ad_ip "$ADMIN_LINODE_PUBLIC_IPV4" --arg ad_fw "$ADMIN_LINODE_FIREWALL_ID" \
		'{stack:$stack, generated_at:(now|todate), video_cloud:., account_manager:{id:($am_id|tonumber),label:$am_label,public_ipv4:$am_ip,private_ip:$am_private,firewall_id:($am_fw|tonumber)}, cloud_admin:{id:($ad_id|tonumber),label:$ad_label,public_ipv4:$ad_ip,firewall_id:($ad_fw|tonumber)}}' \
		"$VC_STATE" > "$art/inventory.json"
	jq -n --slurpfile vc "$VC_STATE" --arg am "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" --arg am_private "${ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4:-}" --arg ad "$ADMIN_LINODE_PUBLIC_IPV4" '{
		generated_at:(now|todate),
		targets:{
			edge:{host:$vc[0].instances.edge.public_ipv4,user:"root"},
			api:{host:$vc[0].instances.api.private_ip,user:"root",proxy_jump:("root@"+$vc[0].instances.edge.public_ipv4)},
			infra:{host:$vc[0].instances.infra.private_ip,user:"root",proxy_jump:("root@"+$vc[0].instances.edge.public_ipv4)},
			mqtt:{host:$vc[0].instances.mqtt.private_ip,user:"root",proxy_jump:("root@"+$vc[0].instances.edge.public_ipv4)},
			coturn:{host:$vc[0].instances.coturn.public_ipv4,user:"root"},
			account_manager:{host:$am,user:"root",private_ip:$am_private},
			cloud_admin:{host:$ad,user:"root"}
		}
	}' > "$art/deployment-targets.json"
	: > "$art/known_hosts"
	for host in "$edge_ip" "$coturn_ip" "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" "$ADMIN_LINODE_PUBLIC_IPV4"; do
		ssh-keyscan -T 10 "$host" >> "$art/known_hosts" 2>/dev/null || true
	done
	proxy_cmd="ssh -i $SSH_KEY -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -W %h:%p root@$edge_ip"
	{
		printf 'edge\t%s\t%s\n' "$edge_ip" "$(check_ssh_with_retries edge "$edge_ip")"
		printf 'coturn\t%s\t%s\n' "$coturn_ip" "$(check_ssh_with_retries coturn "$coturn_ip")"
		printf 'account-manager\t%s\t%s\n' "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" "$(check_ssh_with_retries account-manager "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4")"
		printf 'cloud-admin\t%s\t%s\n' "$ADMIN_LINODE_PUBLIC_IPV4" "$(check_ssh_with_retries cloud-admin "$ADMIN_LINODE_PUBLIC_IPV4")"
		printf 'api\t%s\t%s\n' "$api_ip" "$(check_ssh_with_retries api "$api_ip" "$proxy_cmd")"
		printf 'infra\t%s\t%s\n' "$infra_ip" "$(check_ssh_with_retries infra "$infra_ip" "$proxy_cmd")"
		printf 'mqtt\t%s\t%s\n' "$mqtt_ip" "$(check_ssh_with_retries mqtt "$mqtt_ip" "$proxy_cmd")"
	} > "$tmp/ssh.tsv"
	for item in \
		"$VC_GATEWAY_DOMAIN $edge_ip" \
		"$VC_CERTISSUER_DOMAIN $edge_ip" \
		"$AM_DOMAIN $ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" \
		"$ADMIN_DOMAIN $ADMIN_LINODE_PUBLIC_IPV4"; do
		local domain="${item% *}"
		local ip="${item#* }"
		printf '%s\t%s\t%s\t%s\n' "$domain" "$ip" "$(resolve_dns 8.8.8.8 "$domain")" "$(resolve_dns "$ns" "$domain")" >> "$tmp/dns.tsv"
	done
	{
		printf '# Provision Report\n\n'
		printf -- '- generated_at: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		printf -- '- scope: Video Cloud five-role stack + Account Manager public VM + Cloud Admin public VM\n'
		printf -- '- video_cloud_vpc_id: %s\n' "$(jq -r '.vpc_id' "$VC_STATE")"
		printf -- '- video_cloud_subnet_id: %s\n' "$(jq -r '.subnet_id' "$VC_STATE")"
		printf -- '- artifact_dir: %s\n\n' "$art"
		printf '## VM Configuration\n\n'
		printf -- '- VPN: not configured by this script; private service access uses edge SSH ProxyJump over the Linode VPC.\n\n'
		printf '| Role | Label | Linode ID | Firewall ID | Network | Public IPv4 | Private/VPC IPv4 | Access / VPN | ProxyJump |\n'
		printf '| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n'
		write_video_vm_config_rows
		write_public_vm_config_row "account-manager" "$ACCOUNT_MANAGER_LINODE_LABEL" "$ACCOUNT_MANAGER_LINODE_ID" "$ACCOUNT_MANAGER_LINODE_FIREWALL_ID" "$ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4" "${ACCOUNT_MANAGER_LINODE_PRIVATE_IPV4:-}"
		write_public_vm_config_row "cloud-admin" "$ADMIN_LINODE_LABEL" "$ADMIN_LINODE_ID" "$ADMIN_LINODE_FIREWALL_ID" "$ADMIN_LINODE_PUBLIC_IPV4"
		printf '\n'
		printf '## DNS Status\n\n| Domain | Expected | 8.8.8.8 | %s |\n| --- | --- | --- | --- |\n' "$ns"
		awk -F '\t' '{printf "| `%s` | `%s` | `%s` | `%s` |\n", $1, $2, $3, $4}' "$tmp/dns.tsv"
		printf '\n## SSH Readiness\n\n| Role | Host | Status |\n| --- | --- | --- |\n'
		awk -F '\t' '{printf "| `%s` | `%s` | `%s` |\n", $1, $2, $3}' "$tmp/ssh.tsv"
	} > "$art/provision-report.md"
	printf '%s\n' "$art"
}

init_e2e_report() {
	local report="$1"
	cat > "$report" <<EOF_REPORT
# Staging Provision E2E Smoke Report

- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)
- status: running

## Checks

EOF_REPORT
}

finalize_e2e_report() {
	local report="$1"
	local status="$2"
	local tmp
	tmp="$(mktemp)"
	awk -v status="$status" '{gsub(/status: running/, "status: " status)} {print}' "$report" > "$tmp"
	mv "$tmp" "$report"
}

run_e2e_check() {
	local name="$1"
	local url="$2"
	local out="$3"
	local report="$4"
	log "e2e check: $name $url"
	if curl -fsS "$url" > "$out"; then
		printf -- '- PASS `%s` `%s` output: `%s`\n' "$name" "$url" "$out" >> "$report"
		log "e2e pass: $name"
	else
		local code=$?
		printf -- '- FAIL `%s` `%s` exit=%s output: `%s`\n' "$name" "$url" "$code" "$out" >> "$report"
		log "e2e fail: $name exit=$code"
		return "$code"
	fi
}

e2e_smoke() {
	log "e2e"
	load_operator_env
	require_state
	load_env_file "$AM_STATE"
	load_env_file "$ADMIN_STATE"
	need_cmd curl
	local ts dir report failures=0
	ts="$(date -u +%Y%m%dT%H%M%SZ)"
	dir="$ARTIFACT_BASE/e2e-$ts"
	mkdir -p "$dir"
	report="$dir/e2e-report.md"
	init_e2e_report "$report"
	run_e2e_check "video-cloud-healthz" "https://$VC_GATEWAY_DOMAIN/healthz" "$dir/video-cloud-healthz.out" "$report" || failures=$((failures + 1))
	run_e2e_check "video-cloud-version" "https://$VC_GATEWAY_DOMAIN/version" "$dir/video-cloud-version.out" "$report" || failures=$((failures + 1))
	run_e2e_check "account-manager-health" "https://$AM_DOMAIN/v1/health" "$dir/account-manager-health.out" "$report" || failures=$((failures + 1))
	run_e2e_check "admin-healthz" "https://$ADMIN_DOMAIN/healthz" "$dir/admin-healthz.out" "$report" || failures=$((failures + 1))
	run_e2e_check "admin-service-health" "https://$ADMIN_DOMAIN/api/service-health" "$dir/admin-service-health.out" "$report" || failures=$((failures + 1))
	if [[ "$failures" == "0" ]]; then
		finalize_e2e_report "$report" "passed"
		log "e2e report: $dir"
		return 0
	fi
	finalize_e2e_report "$report" "failed"
	log "e2e report: $dir"
	die "e2e smoke failed: $failures check(s) failed"
}

if [[ "$DO_PREFLIGHT" == "1" ]]; then preflight; fi
if [[ "$DO_PLAN" == "1" ]]; then plan; fi
if [[ "$DO_RESET" == "1" ]]; then reset_stack; fi
if [[ "$DO_APPLY" == "1" ]]; then apply_stack; fi
if [[ "$DO_DNS" == "1" ]]; then dns_stack; fi
if [[ "$DO_DEPLOY" == "1" ]]; then deploy_stack; fi
if [[ "$DO_ARTIFACTS" == "1" ]]; then write_artifacts; fi
if [[ "$DO_E2E" == "1" ]]; then e2e_smoke; fi
