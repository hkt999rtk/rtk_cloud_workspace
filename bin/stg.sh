#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_ROOT="${RTK_CLOUD_STAGING_ENV_ROOT:-$ROOT/cloud_env/staging}"
GO_CMD="${RTK_CLOUD_GO:-go}"

usage() {
	cat <<'USAGE'
Usage:
  stg.sh <command> [args]

Shortcuts:
  provision [args]              -> provision-k8s
  token [args]                  -> rtk-cloud platform-admin-token --env-root cloud_env/staging
  brand NAME [args]             -> create-brandname-cloud
  brands [args]                 -> list-brandname-clouds
  users NAME [COUNT] [args]     -> create-users
  devices [BRAND] [COUNT] [args]-> generate-load-devices
  bind NAME [COUNT] [args]      -> bind-devices
  data [args]                   -> scripts/setup-staging-e2e-data.sh
  unprovision NAME [args]       -> unprovision-devices
  mqtt NAME [args]              -> mqtt-test
  mqtt-report [NAME] [args]     -> mqtt-trace-report
  video NAME [args]             -> video-relay-test
  certs [args]                  -> check-certificates
  e2e [args]                    -> staging-e2e-test
  raw COMMAND [args]            -> pass through to rtk-cloud with staging env-root

Environment:
  RTK_CLOUD_STAGING_ENV_ROOT    Override staging env root.
  RTK_CLOUD_GO                  Override go binary.
USAGE
}

rtk() {
	(cd "$ROOT" && "$GO_CMD" run ./scripts/go/rtk-cloud -- "$@")
}

with_env() {
	local cmd="$1"
	shift
	rtk "$cmd" --env-root "$ENV_ROOT" "$@"
}

need_value() {
	local name="$1"
	local value="${2:-}"
	if [[ -z "$value" || "$value" == -* ]]; then
		printf 'error: %s is required\n' "$name" >&2
		exit 2
	fi
}

retired_vm_shortcut() {
	printf 'error: staging VM shortcut "%s" has been retired; use K8s staging commands instead\n' "$1" >&2
	exit 2
}

cmd="${1:-}"
if [[ -z "$cmd" || "$cmd" == "-h" || "$cmd" == "--help" ]]; then
	usage
	exit 0
fi
shift

case "$cmd" in
	provision)
		with_env provision-k8s "$@"
		;;
	deploy)
		retired_vm_shortcut "$cmd"
		;;
	deploy-local)
		retired_vm_shortcut "$cmd"
		;;
	token)
		with_env platform-admin-token "$@"
		;;
	brand)
		need_value "brand name" "${1:-}"
		brand="$1"
		shift
		with_env create-brandname-cloud --brandname "$brand" "$@"
		;;
	brands)
		with_env list-brandname-clouds "$@"
		;;
	users)
		need_value "brand name" "${1:-}"
		brand="$1"
		shift
		if [[ "${1:-}" =~ ^[0-9]+$ ]]; then
			count="$1"
			shift
			with_env create-users --brandname "$brand" --count "$count" "$@"
		else
			with_env create-users --brandname "$brand" "$@"
		fi
		;;
	devices)
		if [[ "${1:-}" =~ ^[0-9]+$ ]]; then
			count="$1"
			shift
			with_env generate-load-devices --count "$count" "$@"
		elif [[ -n "${1:-}" && "${1:-}" != -* ]]; then
			brand="$1"
			shift
			prefix="$(printf '%s' "$brand" | tr '[:upper:]' '[:lower:]')"
			if [[ "${1:-}" =~ ^[0-9]+$ ]]; then
				count="$1"
				shift
				with_env generate-load-devices --prefix "$prefix" --count "$count" "$@"
			else
				with_env generate-load-devices --prefix "$prefix" "$@"
			fi
		else
			with_env generate-load-devices "$@"
		fi
		;;
	bind)
		need_value "brand name" "${1:-}"
		brand="$1"
		shift
		if [[ "${1:-}" =~ ^[0-9]+$ ]]; then
			count="$1"
			shift
			with_env bind-devices --brandname "$brand" --count "$count" "$@"
		else
			with_env bind-devices --brandname "$brand" "$@"
		fi
		;;
	data)
		exec "$ROOT/scripts/setup-staging-e2e-data.sh" "$@"
		;;
	unprovision)
		need_value "brand name" "${1:-}"
		brand="$1"
		shift
		with_env unprovision-devices --brandname "$brand" "$@"
		;;
	mqtt)
		need_value "brand name" "${1:-}"
		brand="$1"
		shift
		with_env mqtt-test --brandname "$brand" "$@"
		;;
	mqtt-report)
		if [[ -n "${1:-}" && "${1:-}" != -* ]]; then
			brand="$1"
			shift
			with_env mqtt-trace-report --brandname "$brand" "$@"
		else
			with_env mqtt-trace-report "$@"
		fi
		;;
	video)
		need_value "brand name" "${1:-}"
		brand="$1"
		shift
		with_env video-relay-test --brandname "$brand" "$@"
		;;
	certs)
		with_env check-certificates "$@"
		;;
	ssh)
		retired_vm_shortcut "$cmd"
		;;
	rm-vm)
		retired_vm_shortcut "$cmd"
		;;
	e2e)
		with_env staging-e2e-test "$@"
		;;
	raw)
		need_value "rtk-cloud command" "${1:-}"
		raw_cmd="$1"
		shift
		with_env "$raw_cmd" "$@"
		;;
	*)
		printf 'error: unknown staging shortcut: %s\n\n' "$cmd" >&2
		usage >&2
		exit 2
		;;
esac
