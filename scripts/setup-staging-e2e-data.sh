#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_CMD="${RTK_CLOUD_GO:-go}"
WORKSPACE="$ROOT"
ENV_ROOT="${RTK_CLOUD_STAGING_ENV_ROOT:-$ROOT/cloud_env/staging}"
BRANDNAME="RTK"
USER_COUNT="10"
DEVICE_COUNT="100"
DEVICE_MIX="camera=40,light=25,air_conditioner=20,smart_meter=15"
DEVICE_PREFIX="load-device"
PLAN=0
OUT_DIR=""

usage() {
	cat <<'USAGE'
Usage:
  scripts/setup-staging-e2e-data.sh [--plan] [args]

Creates the staging E2E brand cloud, users, factory-enrolled devices, device
bindings, and bind validation artifacts. It does not provision servers and does
not run the live MQTT E2E test.

Options:
  --plan                         Print the data setup plan only.
  --workspace PATH                Workspace root. Default: current checkout.
  --env-root PATH                 Cloud env root. Default: cloud_env/staging.
  --brandname NAME                Brand cloud name. Default: RTK.
  --user-count N                  Users to create. Default: 10.
  --device-count N                Devices to create and bind. Default: 100.
  --device-mix MIX                Device mix for generate-load-devices.
  --device-prefix PREFIX          Device prefix. Default: load-device.
  --out-dir PATH                  Output directory for logs and summary.
  -h, --help                      Show this help.
USAGE
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--plan)
			PLAN=1
			shift
			;;
		--workspace)
			WORKSPACE="${2:-}"
			if [[ -z "$WORKSPACE" ]]; then
				printf 'error: --workspace requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--env-root)
			ENV_ROOT="${2:-}"
			if [[ -z "$ENV_ROOT" ]]; then
				printf 'error: --env-root requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--brandname)
			BRANDNAME="${2:-}"
			if [[ -z "$BRANDNAME" ]]; then
				printf 'error: --brandname requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--user-count)
			USER_COUNT="${2:-}"
			if [[ -z "$USER_COUNT" ]]; then
				printf 'error: --user-count requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--device-count)
			DEVICE_COUNT="${2:-}"
			if [[ -z "$DEVICE_COUNT" ]]; then
				printf 'error: --device-count requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--device-mix)
			DEVICE_MIX="${2:-}"
			if [[ -z "$DEVICE_MIX" ]]; then
				printf 'error: --device-mix requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--device-prefix)
			DEVICE_PREFIX="${2:-}"
			if [[ -z "$DEVICE_PREFIX" ]]; then
				printf 'error: --device-prefix requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--out-dir)
			OUT_DIR="${2:-}"
			if [[ -z "$OUT_DIR" ]]; then
				printf 'error: --out-dir requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			printf 'error: unknown argument: %s\n' "$1" >&2
			usage >&2
			exit 2
			;;
	esac
done

STACK_FILE="$ENV_ROOT/env/stack.env"
PROVIDER="${CLOUD_PROVIDER:-}"
if [[ -z "$PROVIDER" && -f "$STACK_FILE" ]]; then
	PROVIDER="$(awk -F= '$1 == "CLOUD_PROVIDER" {print $2; exit}' "$STACK_FILE")"
fi
PROVIDER="${PROVIDER:-linode}"
if [[ "$PROVIDER" != "linode" ]]; then
	printf 'error: unsupported CLOUD_PROVIDER=%s; staging E2E data setup currently supports only linode\n' "$PROVIDER" >&2
	exit 2
fi

run_args=(
	staging-e2e-data-setup
	--workspace "$WORKSPACE"
	--env-root "$ENV_ROOT"
	--brandname "$BRANDNAME"
	--user-count "$USER_COUNT"
	--device-count "$DEVICE_COUNT"
	--device-mix "$DEVICE_MIX"
	--device-prefix "$DEVICE_PREFIX"
)
if [[ "$PLAN" -eq 1 ]]; then
	run_args+=(--plan)
fi
if [[ -n "$OUT_DIR" ]]; then
	run_args+=(--out-dir "$OUT_DIR")
fi

(cd "$ROOT" && "$GO_CMD" run ./scripts/go/rtk-cloud -- "${run_args[@]}")
