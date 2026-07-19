#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_CMD="${RTK_CLOUD_GO:-go}"
WORKSPACE="$ROOT"
ENV_ROOT="${RTK_CLOUD_STAGING_ENV_ROOT:-$ROOT/cloud_env/staging}"
BRANDNAME="RTK"
BRAND_PLAN=""
SCENARIO_PROFILE=""
USER_COUNT="10"
USER_EMAIL_PREFIX=""
DEVICE_COUNT="100"
DEVICE_MIX="camera=40,light=25,air_conditioner=20,smart_meter=15"
DEVICE_PREFIX="load-device"
USER_CONCURRENCY="64"
DEVICE_CONCURRENCY="64"
BIND_CONCURRENCY="64"
PLAN=0
OUT_DIR=""
QUIET=0
RESUME=1
FROM_STEP=""
DESCRIPTION_FILE="${HOME100K_DESCRIPTION_FILE:-}"

capture_home100k_env_overrides() {
	local name
	env | while IFS='=' read -r name _; do
		case "$name" in
			HOME100K_*)
				printf '%s=%q\n' "$name" "${!name-}"
				;;
		esac
	done
}

description_file_from_args() {
	while [[ $# -gt 0 ]]; do
		case "$1" in
			--description-file)
				printf '%s\n' "${2:-}"
				return
				;;
			--)
				return
				;;
		esac
		shift
	done
}

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
  --brand-plan FILE               Multi-brand load-test plan JSON.
  --description-file FILE         HOME100K description env; HOME100K_* values become data setup defaults.
  --user-count N                  Users to create. Default: 10.
  --user-email-prefix PREFIX      Optional run-scoped user email prefix.
  --device-count N                Devices to create and bind. Default: 100.
  --device-mix MIX                Device mix for generate-load-devices.
  --device-prefix PREFIX          Device prefix. Default: load-device.
  --user-concurrency N            Concurrent user creation workers. Default: 64.
  --device-concurrency N          Concurrent device generation workers. Default: 64.
  --bind-concurrency N            Concurrent device binding workers. Default: 64.
  --out-dir PATH                  Output directory for logs and summary.
  --quiet                         Suppress periodic progress lines.
  --resume                        Reuse matching completed SQLite test data. Default.
  --no-resume                     Recreate users/devices/bind data even when SQLite test data is complete.
  --from-step STEP                Start from create_brand, create_users, create_devices, bind_devices, or validate_bind.
  -h, --help                      Show this help.
USAGE
}

description_from_args="$(description_file_from_args "$@")"
if [[ -n "$description_from_args" ]]; then
	DESCRIPTION_FILE="$description_from_args"
fi
explicit_home100k_env="$(capture_home100k_env_overrides)"
if [[ -n "$DESCRIPTION_FILE" ]]; then
	if [[ "$DESCRIPTION_FILE" != /* && ! -f "$DESCRIPTION_FILE" && -f "$ROOT/$DESCRIPTION_FILE" ]]; then
		DESCRIPTION_FILE="$ROOT/$DESCRIPTION_FILE"
	fi
	if [[ ! -f "$DESCRIPTION_FILE" ]]; then
		printf 'error: --description-file not found: %s\n' "$DESCRIPTION_FILE" >&2
		exit 2
	fi
	set -a
	# shellcheck source=/dev/null
	source "$DESCRIPTION_FILE"
	set +a
fi
if [[ -n "$explicit_home100k_env" ]]; then
	eval "$explicit_home100k_env"
fi

BRANDNAME="${HOME100K_BRANDNAME:-$BRANDNAME}"
BRAND_PLAN="${HOME100K_BRAND_PLAN:-$BRAND_PLAN}"
SCENARIO_PROFILE="${HOME100K_SCENARIO_PROFILE:-$SCENARIO_PROFILE}"
USER_COUNT="${HOME100K_USERS:-$USER_COUNT}"
DEVICE_COUNT="${HOME100K_DEVICES:-$DEVICE_COUNT}"
DEVICE_MIX="${HOME100K_DEVICE_MIX:-$DEVICE_MIX}"
DEVICE_PREFIX="${HOME100K_DEVICE_PREFIX:-$DEVICE_PREFIX}"
USER_CONCURRENCY="${HOME100K_USER_CONCURRENCY:-$USER_CONCURRENCY}"
DEVICE_CONCURRENCY="${HOME100K_DEVICE_CONCURRENCY:-$DEVICE_CONCURRENCY}"
BIND_CONCURRENCY="${HOME100K_BIND_CONCURRENCY:-$BIND_CONCURRENCY}"

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
		--brand-plan)
			BRAND_PLAN="${2:-}"
			if [[ -z "$BRAND_PLAN" ]]; then
				printf 'error: --brand-plan requires a value\n' >&2
				exit 2
			fi
			shift 2
			;;
		--description-file)
			DESCRIPTION_FILE="${2:-}"
			if [[ -z "$DESCRIPTION_FILE" ]]; then
				printf 'error: --description-file requires a value\n' >&2
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
		--user-email-prefix)
			USER_EMAIL_PREFIX="${2:-}"
			if [[ -z "$USER_EMAIL_PREFIX" ]]; then
				printf 'error: --user-email-prefix requires a value\n' >&2
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
		--no-resume)
			RESUME=0
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
		ENV_ROOT="$ENV_ROOT/lke"
	fi
fi
STACK_FILE="$ENV_ROOT/env/stack.env"
if [[ -z "$PROVIDER" && -f "$STACK_FILE" ]]; then
	PROVIDER="$(env_file_value "$STACK_FILE" CLOUD_PROVIDER)"
fi
PROVIDER="${PROVIDER:-lke}"
case "$PROVIDER" in
	lke|k8s|gke|eks|aks)
		;;
	linode)
		printf 'error: CLOUD_PROVIDER=linode used the retired VM runtime; use CLOUD_PROVIDER=lke or another Kubernetes provider\n' >&2
		exit 2
		;;
	*)
		printf 'error: unsupported CLOUD_PROVIDER=%s; staging E2E data setup supports Kubernetes providers only\n' "$PROVIDER" >&2
		exit 2
		;;
esac
export CLOUD_PROVIDER="$PROVIDER"

if [[ -n "$BRAND_PLAN" && "$BRAND_PLAN" != /* ]]; then
	BRAND_PLAN="$WORKSPACE/$BRAND_PLAN"
fi
case "$SCENARIO_PROFILE" in
	video-50k-turn-v1|video-100k-turn-v1)
		if [[ -z "$BRAND_PLAN" ]]; then
			printf 'error: %s requires HOME100K_BRAND_PLAN or --brand-plan for empty data setup\n' "$SCENARIO_PROFILE" >&2
			exit 2
		fi
		;;
esac

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
if [[ -n "$USER_EMAIL_PREFIX" ]]; then
	run_args+=(--user-email-prefix "$USER_EMAIL_PREFIX")
fi
if [[ -n "$BRAND_PLAN" ]]; then
	run_args+=(--brand-plan "$BRAND_PLAN")
fi
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
else
	run_args+=(--no-resume)
fi
if [[ -n "$FROM_STEP" ]]; then
	run_args+=(--from-step "$FROM_STEP")
fi
(cd "$ROOT" && "$GO_CMD" run ./scripts/go/rtk-cloud -- "${run_args[@]}")
