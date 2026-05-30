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
CIDR=""
DRY_RUN=0
MODE=""

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[cloud-ssh-whitelist] %s\n' "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-update-ssh-whitelist.sh [options]

Options:
  --cidr CIDR           CIDR to allow. Default: current public IPv4 /32.
  --mode MODE           append or replace. Default: append in non-interactive runs.
  --workspace PATH      Default: script parent workspace.
  --env-root PATH       Required environment directory, for example cloud_env/staging.
  --secrets-root PATH   Deprecated alias for --env-root.
  --operator-env PATH   Default: <env-root>/env/operator.env.
  --dry-run             Show target firewall updates without calling Linode API.
  -h, --help            Show this help.

Updates SSH port 22 allowlists for the staging firewalls:
  - video-cloud-staging-edge/api/infra/mqtt/coturn
  - rtk-account-manager-staging-fw
  - rtk-cloud-admin-staging-firewall

Modes:
  append                Add the CIDR to existing SSH allowlists.
  replace               Replace SSH allowlists with only the CIDR.

If --mode is omitted in an interactive terminal, the script asks which mode to
use. If --mode is omitted in a non-interactive run, it uses append for backward
compatibility. The script also updates ignored local staging config/env files so
future provision runs keep the same allowlist.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--cidr) CIDR="$2"; shift 2 ;;
	--mode) MODE="$2"; shift 2 ;;
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--env-root) ENV_ROOT="$2"; shift 2 ;;
	--secrets-root) DEPRECATED_ENV_ROOT="$2"; ENV_ROOT="$2"; shift 2 ;;
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
	--dry-run) DRY_RUN=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$ENV_ROOT" ]] || die "--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging"

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

current_public_cidr() {
	local ip
	ip="$(curl -fsS https://api.ipify.org)"
	[[ "$ip" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] || die "cannot detect current public IPv4: $ip"
	printf '%s/32\n' "$ip"
}

validate_cidr() {
	local cidr="$1"
	[[ "$cidr" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$ ]] || die "invalid IPv4 CIDR: $cidr"
}

select_mode() {
	case "$MODE" in
	append|replace) return 0 ;;
	"") ;;
	*) die "invalid --mode: $MODE; expected append or replace" ;;
	esac
	if [[ -t 0 && -t 2 ]]; then
		printf 'Select SSH whitelist update mode:\n' >&2
		printf '  1) append current CIDR to existing SSH allowlist\n' >&2
		printf '  2) replace SSH allowlist with only current CIDR\n' >&2
		printf 'Choice [1]: ' >&2
		local choice
		read -r choice
		case "$choice" in
		""|1) MODE="append" ;;
		2) MODE="replace" ;;
		*) die "invalid mode choice: $choice" ;;
		esac
	else
		MODE="append"
	fi
}

append_csv_env_value() {
	local file="$1"
	local key="$2"
	local cidr="$3"
	[[ -f "$file" ]] || return 0
	python3 - "$file" "$key" "$cidr" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
key = sys.argv[2]
cidr = sys.argv[3]
lines = path.read_text().splitlines()
out = []
updated = False
for line in lines:
    if line.startswith(key + "="):
        _, value = line.split("=", 1)
        parts = [p.strip() for p in value.split(",") if p.strip()]
        if cidr not in parts:
            parts.append(cidr)
        line = key + "=" + ",".join(parts)
        updated = True
    out.append(line)
if not updated:
    out.append(key + "=" + cidr)
path.write_text("\n".join(out) + "\n")
PY
}

set_csv_env_value() {
	local file="$1"
	local key="$2"
	local cidr="$3"
	[[ -f "$file" ]] || return 0
	python3 - "$file" "$key" "$cidr" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
key = sys.argv[2]
cidr = sys.argv[3]
lines = path.read_text().splitlines()
out = []
updated = False
for line in lines:
    if line.startswith(key + "="):
        line = key + "=" + cidr
        updated = True
    out.append(line)
if not updated:
    out.append(key + "=" + cidr)
path.write_text("\n".join(out) + "\n")
PY
}

append_video_config_cidr() {
	local file="$1"
	local cidr="$2"
	[[ -f "$file" ]] || return 0
	if grep -Fxq "    - $cidr" "$file"; then
		return 0
	fi
	python3 - "$file" "$cidr" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
cidr = sys.argv[2]
lines = path.read_text().splitlines()
out = []
inserted = False
in_allowed = False
for line in lines:
    if line.strip() == "allowed_source_cidrs:":
        in_allowed = True
        out.append(line)
        continue
    if in_allowed and not inserted and line and not line.startswith("    - "):
        out.append(f"    - {cidr}")
        inserted = True
        in_allowed = False
    out.append(line)
if in_allowed and not inserted:
    out.append(f"    - {cidr}")
path.write_text("\n".join(out) + "\n")
PY
}

replace_video_config_cidr() {
	local file="$1"
	local cidr="$2"
	[[ -f "$file" ]] || return 0
	python3 - "$file" "$cidr" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
cidr = sys.argv[2]
lines = path.read_text().splitlines()
out = []
in_allowed = False
replaced = False
for line in lines:
    if line.strip() == "allowed_source_cidrs:":
        in_allowed = True
        replaced = True
        out.append(line)
        out.append(f"    - {cidr}")
        continue
    if in_allowed:
        if line.startswith("    - "):
            continue
        in_allowed = False
    out.append(line)
if not replaced:
    out.extend(["ssh:", "  allowed_source_cidrs:", f"    - {cidr}"])
path.write_text("\n".join(out) + "\n")
PY
}

discover_firewall_id() {
	local label="$1"
	local firewalls="$2"
	jq -r --arg label "$label" '.data[]? | select(.label == $label) | .id' <<<"$firewalls" | head -n 1
}

update_firewall() {
	local role="$1"
	local label="$2"
	local id="$3"
	local cidr="$4"
	local rules updated already exact
	if [[ -z "$id" || "$id" == "null" ]]; then
		log "skip: $role firewall id missing label=$label"
		return 0
	fi
	rules="$(linode_api GET "/networking/firewalls/$id/rules")"
	if [[ "$MODE" == "replace" ]]; then
		exact="$(jq -r --arg cidr "$cidr" '
			[.inbound[]? | select(.label == "ssh" or (.protocol == "TCP" and .ports == "22"))] as $ssh
			| (($ssh | length) > 0 and ($ssh | all((.addresses.ipv4 // []) == [$cidr])))
		' <<<"$rules")"
		if [[ "$exact" == "true" ]]; then
			log "already restricted: mode=replace role=$role firewall=$label id=$id cidr=$cidr"
			return 0
		fi
		updated="$(jq --arg cidr "$cidr" '
			.inbound |= map(
				if (.label == "ssh" or (.protocol == "TCP" and .ports == "22")) then
					.addresses.ipv4 = [$cidr]
				else
					.
				end
			)
			| del(.version, .fingerprint)
		' <<<"$rules")"
		if [[ "$DRY_RUN" == "1" ]]; then
			log "dry-run replace: mode=replace role=$role firewall=$label id=$id cidr=$cidr"
		else
			linode_api PUT "/networking/firewalls/$id/rules" "$updated" >/dev/null
			log "replaced: mode=replace role=$role firewall=$label id=$id cidr=$cidr"
		fi
		return 0
	fi
	already="$(jq -r --arg cidr "$cidr" '
		[.inbound[]? | select((.label == "ssh" or (.protocol == "TCP" and .ports == "22")) and ((.addresses.ipv4 // []) | index($cidr)))] | length
	' <<<"$rules")"
	if [[ "$already" != "0" ]]; then
		log "already allowed: mode=append role=$role firewall=$label id=$id cidr=$cidr"
		return 0
	fi
	updated="$(jq --arg cidr "$cidr" '
		.inbound |= map(
			if (.label == "ssh" or (.protocol == "TCP" and .ports == "22")) then
				.addresses.ipv4 = (((.addresses.ipv4 // []) + [$cidr]) | unique)
			else
				.
			end
		)
		| del(.version, .fingerprint)
	' <<<"$rules")"
	if [[ "$DRY_RUN" == "1" ]]; then
		log "dry-run append: mode=append role=$role firewall=$label id=$id cidr=$cidr"
	else
		linode_api PUT "/networking/firewalls/$id/rules" "$updated" >/dev/null
		log "appended: mode=append role=$role firewall=$label id=$id cidr=$cidr"
	fi
}

need_cmd curl
need_cmd jq
need_cmd python3

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
source "$SCRIPT_DIR/lib/cloud-env.sh"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
DEPRECATED_ENV_ROOT="$ENV_ROOT"
OPERATOR_ENV="${OPERATOR_ENV:-$(cloud_env_operator_env "$ENV_ROOT")}"

if [[ -z "$CIDR" ]]; then
	CIDR="$(current_public_cidr)"
fi
validate_cidr "$CIDR"
select_mode

load_env_file "$OPERATOR_ENV"
[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"

VC_STATE="$(cloud_env_video_state "$ENV_ROOT")"
VC_SECRET_STATE="$VC_STATE"
VC_CONFIG="$(cloud_env_video_config "$ENV_ROOT")"
AM_ENV="$(cloud_env_account_manager_env "$ENV_ROOT")"
AM_STATE="$(cloud_env_account_manager_state "$ENV_ROOT")"
ADMIN_ENV="$(cloud_env_admin_env "$ENV_ROOT")"
ADMIN_STATE="$(cloud_env_admin_state "$ENV_ROOT")"

load_env_file "$AM_STATE"
load_env_file "$ADMIN_STATE"

log "allowing SSH CIDR: mode=$MODE cidr=$CIDR"
firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
tmp="$(mktemp /tmp/rtk-cloud-ssh-firewalls.XXXXXX)"
trap 'rm -f "$tmp"' EXIT

if [[ -f "$VC_STATE" ]]; then
	jq -r '.firewalls // {} | to_entries[] | [.key, ("video-cloud-staging-" + .key), (.value|tostring)] | @tsv' "$VC_STATE" >> "$tmp"
elif [[ -f "$VC_SECRET_STATE" ]]; then
	jq -r '.firewalls // {} | to_entries[] | [.key, ("video-cloud-staging-" + .key), (.value|tostring)] | @tsv' "$VC_SECRET_STATE" >> "$tmp"
else
	for role in edge api infra mqtt coturn; do
		label="video-cloud-staging-$role"
		id="$(discover_firewall_id "$label" "$firewalls")"
		printf '%s\t%s\t%s\n' "$role" "$label" "$id" >> "$tmp"
	done
fi

printf 'account-manager\t%s\t%s\n' \
	"${ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL:-rtk-account-manager-staging-fw}" \
	"${ACCOUNT_MANAGER_LINODE_FIREWALL_ID:-$(discover_firewall_id "${ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL:-rtk-account-manager-staging-fw}" "$firewalls")}" >> "$tmp"
printf 'cloud-admin\t%s\t%s\n' \
	"${ADMIN_LINODE_FIREWALL_LABEL:-rtk-cloud-admin-staging-firewall}" \
	"${ADMIN_LINODE_FIREWALL_ID:-$(discover_firewall_id "${ADMIN_LINODE_FIREWALL_LABEL:-rtk-cloud-admin-staging-firewall}" "$firewalls")}" >> "$tmp"

while IFS=$'\t' read -r role label id; do
	update_firewall "$role" "$label" "$id" "$CIDR"
done < "$tmp"

if [[ "$DRY_RUN" != "1" ]]; then
	if [[ "$MODE" == "replace" ]]; then
		replace_video_config_cidr "$VC_CONFIG" "$CIDR"
		set_csv_env_value "$AM_ENV" ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS "$CIDR"
		set_csv_env_value "$ADMIN_ENV" ADMIN_LINODE_ALLOWED_SSH_CIDRS "$CIDR"
	else
		append_video_config_cidr "$VC_CONFIG" "$CIDR"
		append_csv_env_value "$AM_ENV" ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS "$CIDR"
		append_csv_env_value "$ADMIN_ENV" ADMIN_LINODE_ALLOWED_SSH_CIDRS "$CIDR"
	fi
	log "local ignored staging config/env updated: mode=$MODE cidr=$CIDR"
fi
