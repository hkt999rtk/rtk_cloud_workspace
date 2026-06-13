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
USER_CONCURRENCY="16"
DEVICE_CONCURRENCY="16"
BIND_CONCURRENCY="16"
PLAN=0
OUT_DIR=""
QUIET=0
RESUME=0
FROM_STEP=""
USERS_FILE=""
BIND_ARTIFACT=""

env_file_value() {
	local file="$1"
	local key="$2"
	if [[ -f "$file" ]]; then
		awk -F= -v key="$key" '$1 == key {print $2; exit}' "$file"
	fi
}

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
  --user-concurrency N            Concurrent user creation workers. Default: 16.
  --device-concurrency N          Concurrent device generation workers. Default: 16.
  --bind-concurrency N            Concurrent device binding workers. Default: 16.
  --out-dir PATH                  Output directory for logs and summary.
  --quiet                         Suppress periodic progress lines.
  --resume                        Reuse matching completed artifacts.
  --from-step STEP                Start from create_brand, create_users, create_devices, bind_devices, or validate_bind.
  --users-file PATH               Existing users artifact for bind/validate resume.
  --bind-artifact PATH            Existing bind artifact for validate resume.
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
		--user-concurrency)
			USER_CONCURRENCY="${2:-}"
			if [[ -z "$USER_CONCURRENCY" ]]; then
				printf 'error: --user-concurrency requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--device-concurrency)
			DEVICE_CONCURRENCY="${2:-}"
			if [[ -z "$DEVICE_CONCURRENCY" ]]; then
				printf 'error: --device-concurrency requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--bind-concurrency)
			BIND_CONCURRENCY="${2:-}"
			if [[ -z "$BIND_CONCURRENCY" ]]; then
				printf 'error: --bind-concurrency requires a value\n' >&2
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
		--quiet)
			QUIET=1
			shift
			;;
		--resume)
			RESUME=1
			shift
			;;
		--from-step)
			FROM_STEP="${2:-}"
			if [[ -z "$FROM_STEP" ]]; then
				printf 'error: --from-step requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--users-file)
			USERS_FILE="${2:-}"
			if [[ -z "$USERS_FILE" ]]; then
				printf 'error: --users-file requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--bind-artifact)
			BIND_ARTIFACT="${2:-}"
			if [[ -z "$BIND_ARTIFACT" ]]; then
				printf 'error: --bind-artifact requires a value\n' >&2
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

PROVIDER="${CLOUD_PROVIDER:-${RTK_CLOUD_STAGING_PROVIDER:-}}"
if [[ "$(basename "$ENV_ROOT")" == "staging" ]]; then
	if [[ -n "$PROVIDER" ]]; then
		ENV_ROOT="$ENV_ROOT/$PROVIDER"
	elif [[ "$(env_file_value "$ENV_ROOT/lke/env/stack.env" CLOUD_PROVIDER)" == "lke" ]]; then
		ENV_ROOT="$ENV_ROOT/lke"
	else
		ENV_ROOT="$ENV_ROOT/linode"
	fi
fi
STACK_FILE="$ENV_ROOT/env/stack.env"
if [[ -z "$PROVIDER" && -f "$STACK_FILE" ]]; then
	PROVIDER="$(env_file_value "$STACK_FILE" CLOUD_PROVIDER)"
fi
PROVIDER="${PROVIDER:-linode}"
if [[ "$PROVIDER" != "linode" && "$PROVIDER" != "lke" ]]; then
	printf 'error: unsupported CLOUD_PROVIDER=%s; staging E2E data setup currently supports linode or lke\n' "$PROVIDER" >&2
	exit 2
fi
export CLOUD_PROVIDER="$PROVIDER"

run_args=(
	staging-e2e-data-setup
	--workspace "$WORKSPACE"
	--env-root "$ENV_ROOT"
	--brandname "$BRANDNAME"
	--user-count "$USER_COUNT"
	--device-count "$DEVICE_COUNT"
	--device-mix "$DEVICE_MIX"
	--device-prefix "$DEVICE_PREFIX"
	--user-concurrency "$USER_CONCURRENCY"
	--device-concurrency "$DEVICE_CONCURRENCY"
	--bind-concurrency "$BIND_CONCURRENCY"
)
if [[ "$PLAN" -eq 1 ]]; then
	run_args+=(--plan)
fi
if [[ -n "$OUT_DIR" ]]; then
	run_args+=(--out-dir "$OUT_DIR")
fi
if [[ "$QUIET" -eq 1 ]]; then
	run_args+=(--quiet)
fi
if [[ "$RESUME" -eq 1 ]]; then
	run_args+=(--resume)
fi
if [[ -n "$FROM_STEP" ]]; then
	run_args+=(--from-step "$FROM_STEP")
fi
if [[ -n "$USERS_FILE" ]]; then
	run_args+=(--users-file "$USERS_FILE")
fi
if [[ -n "$BIND_ARTIFACT" ]]; then
	run_args+=(--bind-artifact "$BIND_ARTIFACT")
fi

(cd "$ROOT" && "$GO_CMD" run ./scripts/go/rtk-cloud -- "${run_args[@]}")
