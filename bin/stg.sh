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
  provision [args]              -> rtk-cloud provision --env-root cloud_env/staging (default: --all)
  deploy [args]                 -> rtk-cloud deploy --env-root cloud_env/staging
  token [args]                  -> rtk-cloud platform-admin-token --env-root cloud_env/staging
  brand NAME [args]             -> create-brandname-cloud
  brands [args]                 -> list-brandname-clouds
  users NAME [COUNT] [args]     -> create-users
  devices [BRAND] [COUNT] [args]-> generate-load-devices
  bind NAME [COUNT] [args]      -> bind-devices
  unprovision NAME [args]       -> unprovision-devices
  mqtt NAME [args]              -> mqtt-test
  mqtt-report [NAME] [args]     -> mqtt-trace-report
  video NAME [args]             -> video-relay-test
  certs [args]                  -> check-certificates
  ssh [CIDR] [args]             -> update-ssh-whitelist
  rm-vm [args]                  -> remove-all-vm
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

cmd="${1:-}"
if [[ -z "$cmd" || "$cmd" == "-h" || "$cmd" == "--help" ]]; then
	usage
	exit 0
fi
shift

case "$cmd" in
	provision)
		with_env provision "$@"
		;;
	deploy)
		with_env deploy "$@"
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
		if [[ "${1:-}" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/([0-9]|[12][0-9]|3[0-2])$ ]]; then
			cidr="$1"
			shift
			with_env update-ssh-whitelist --cidr "$cidr" "$@"
		else
			with_env update-ssh-whitelist "$@"
		fi
		;;
	rm-vm)
		with_env remove-all-vm "$@"
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
