#!/usr/bin/env bash
if [ -z "${BASH_VERSION:-}" ]; then
	exec /usr/bin/env bash "$0" "$@"
fi
set -euo pipefail

cmd="${1:-}"
shift || true

workspace=""
env_root=""
logger_env=""
logger_state=""
target=""
host=""
endpoint=""
system_max_use=""
system_keep_free=""
max_retention_sec=""
positional_target=""

if [[ "$cmd" == "install-forwarder" || "$cmd" == "forwarder-status" ]]; then
	if [[ "${1:-}" != --* ]]; then
		positional_target="${1:-}"
		shift || true
	fi
fi

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) workspace="$2"; shift 2 ;;
	--env-root) env_root="$2"; shift 2 ;;
	--logger-env) logger_env="$2"; shift 2 ;;
	--logger-state) logger_state="$2"; shift 2 ;;
	--logger-label|--logger-firewall|--logger-domain|--operator-env|--ssh-key) shift 2 ;;
	--host) host="$2"; shift 2 ;;
	--endpoint) endpoint="$2"; shift 2 ;;
	--journald-system-max-use) system_max_use="$2"; shift 2 ;;
	--journald-system-keep-free) system_keep_free="$2"; shift 2 ;;
	--journald-max-retention-sec) max_retention_sec="$2"; shift 2 ;;
	*) shift ;;
	esac
done

write_kv() {
	local path="$1"
	local key="$2"
	local value="$3"
	local tmp
	[[ -n "$path" ]] || return 0
	mkdir -p "$(dirname "$path")"
	tmp="$(mktemp)"
	if [[ -f "$path" ]]; then
		grep -vE "^${key}=" "$path" > "$tmp" || true
	fi
	printf '%s=%q\n' "$key" "$value" >> "$tmp"
	install -m 0600 "$tmp" "$path"
	rm -f "$tmp"
}

case "$cmd" in
provision-backend)
	write_kv "$logger_env" CLOUD_LOGGER_ENDPOINT "$endpoint"
	write_kv "$logger_state" CLOUD_LOGGER_BACKEND_STATUS planned
	printf 'logger backend provisioning is recorded as planned; set CLOUD_LOGGER_SCRIPT to the rtk_cloud_logger deploy hook for live provisioning\n' >&2
	exit 1
	;;
install-forwarder)
	target="${positional_target:-unknown}"
	write_kv "$logger_state" "CLOUD_LOGGER_FORWARDER_${target//[^A-Za-z0-9]/_}_HOST" "$host"
	write_kv "$logger_state" "CLOUD_LOGGER_FORWARDER_${target//[^A-Za-z0-9]/_}_RETENTION" "SystemMaxUse=$system_max_use SystemKeepFree=$system_keep_free MaxRetentionSec=$max_retention_sec"
	printf 'logger forwarder install is recorded as planned for %s; set CLOUD_LOGGER_SCRIPT to the rtk_cloud_logger deploy hook for live install\n' "$target" >&2
	exit 1
	;;
backend-health)
	printf 'logger backend health requires live rtk_cloud_logger backend evidence\n' >&2
	exit 1
	;;
forwarder-status)
	target="${positional_target:-unknown}"
	printf 'logger forwarder status requires live host evidence for %s\n' "$target" >&2
	exit 1
	;;
sample-trace-query)
	printf 'logger sample trace query requires live rtk_cloud_logger query endpoint\n' >&2
	exit 1
	;;
*)
	printf 'usage: scripts/cloud-logger.sh {provision-backend|install-forwarder|backend-health|forwarder-status|sample-trace-query}\n' >&2
	exit 2
	;;
esac
