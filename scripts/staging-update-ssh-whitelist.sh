#!/usr/bin/env bash
if [ -z "${BASH_VERSION:-}" ]; then
	exec /usr/bin/env bash "$0" "$@"
fi
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
SECRETS_ROOT=""
OPERATOR_ENV=""
CIDR=""
DRY_RUN=0

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[staging-ssh-whitelist] %s\n' "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/staging-update-ssh-whitelist.sh [options]

Options:
  --cidr CIDR           CIDR to allow. Default: current public IPv4 /32.
  --workspace PATH      Default: script parent workspace.
  --secrets-root PATH   Default: <workspace>/.secrets/staging/linode.
  --operator-env PATH   Default: <secrets-root>/video-cloud/env/operator.env.
  --dry-run             Show target firewall updates without calling Linode API.
  -h, --help            Show this help.

Updates SSH port 22 allowlists for the staging firewalls:
  - video-cloud-staging-edge/api/infra/mqtt/coturn
  - rtk-account-manager-staging-fw
  - rtk-cloud-admin-staging-firewall

The script appends the CIDR; it does not remove existing allowlist entries.
It also updates ignored local staging config/env files so future provision runs
keep the same allowlist.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--cidr) CIDR="$2"; shift 2 ;;
	--workspace) WORKSPACE="$2"; shift 2 ;;
	--secrets-root) SECRETS_ROOT="$2"; shift 2 ;;
	--operator-env) OPERATOR_ENV="$2"; shift 2 ;;
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
	local rules updated already
	if [[ -z "$id" || "$id" == "null" ]]; then
		log "skip: $role firewall id missing label=$label"
		return 0
	fi
	rules="$(linode_api GET "/networking/firewalls/$id/rules")"
	already="$(jq -r --arg cidr "$cidr" '
		[.inbound[]? | select((.label == "ssh" or (.protocol == "TCP" and .ports == "22")) and ((.addresses.ipv4 // []) | index($cidr)))] | length
	' <<<"$rules")"
	if [[ "$already" != "0" ]]; then
		log "already allowed: role=$role firewall=$label id=$id cidr=$cidr"
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
		log "dry-run update: role=$role firewall=$label id=$id add=$cidr"
	else
		linode_api PUT "/networking/firewalls/$id/rules" "$updated" >/dev/null
		log "updated: role=$role firewall=$label id=$id add=$cidr"
	fi
}

need_cmd curl
need_cmd jq
need_cmd python3

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
SECRETS_ROOT="${SECRETS_ROOT:-$WORKSPACE/.secrets/staging/linode}"
OPERATOR_ENV="${OPERATOR_ENV:-$SECRETS_ROOT/video-cloud/env/operator.env}"

if [[ -z "$CIDR" ]]; then
	CIDR="$(current_public_cidr)"
fi
validate_cidr "$CIDR"

load_env_file "$OPERATOR_ENV"
[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"

VC_STATE="$WORKSPACE/repos/rtk_video_cloud/linode_deploy/state/video-cloud-staging.state.json"
VC_SECRET_STATE="$SECRETS_ROOT/video-cloud/state/video-cloud-staging.state.json"
VC_CONFIG="$SECRETS_ROOT/video-cloud/config/video-cloud-staging.yaml"
AM_ENV="$WORKSPACE/repos/rtk_account_manager/linode_deploy/secrets/account-manager-public-staging.env"
AM_STATE="$WORKSPACE/repos/rtk_account_manager/linode_deploy/state/rtk-account-manager-staging.env"
ADMIN_ENV="$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/admin-staging.env"
ADMIN_STATE="$WORKSPACE/repos/rtk_cloud_admin/deploy/linode/rtk-cloud-admin-staging.state"

load_env_file "$AM_STATE"
load_env_file "$ADMIN_STATE"

log "allowing SSH CIDR: $CIDR"
firewalls="$(linode_api GET '/networking/firewalls?page_size=500')"
tmp="$(mktemp /tmp/rtk-staging-ssh-firewalls.XXXXXX)"
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
	append_video_config_cidr "$VC_CONFIG" "$CIDR"
	append_csv_env_value "$AM_ENV" ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS "$CIDR"
	append_csv_env_value "$ADMIN_ENV" ADMIN_LINODE_ALLOWED_SSH_CIDRS "$CIDR"
	log "local ignored staging config/env updated"
fi
