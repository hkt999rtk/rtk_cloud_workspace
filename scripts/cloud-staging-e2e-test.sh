#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE="$(cd "$SCRIPT_DIR/.." && pwd)"
# shellcheck source=scripts/lib/cloud-env.sh
source "$SCRIPT_DIR/lib/cloud-env.sh"

ENV_ROOT=""
MODE="plan"
CONFIRM=""
BRANDNAME="RTK"
USER_COUNT="10"
DEVICE_COUNT="100"
DEVICE_MIX="camera=40,light=25,air_conditioner=20,smart_meter=15"
DEVICE_PREFIX="load-device"
VIDEO_RELEASE="${VIDEO_RELEASE:-}"
ACCOUNT_RELEASE="${ACCOUNT_RELEASE:-}"
ADMIN_RELEASE="${ADMIN_RELEASE:-}"
SKIP_REMOVE=0
SKIP_MQTT_PROBE=0
OUT_DIR=""

REMOVE_SCRIPT="${CLOUD_STAGING_E2E_REMOVE_SCRIPT:-$SCRIPT_DIR/cloud-remove-all-vm.sh}"
PROVISION_SCRIPT="${CLOUD_STAGING_E2E_PROVISION_SCRIPT:-$SCRIPT_DIR/cloud-provision.sh}"
CREATE_BRAND_SCRIPT="${CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT:-$SCRIPT_DIR/cloud-create-brandname-cloud.sh}"
CREATE_USERS_SCRIPT="${CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT:-$SCRIPT_DIR/cloud-create-users.sh}"
GENERATE_DEVICES_SCRIPT="${CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT:-$SCRIPT_DIR/cloud-generate-load-devices.sh}"
BIND_DEVICES_SCRIPT="${CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT:-$SCRIPT_DIR/cloud-bind-devices.sh}"
VALIDATE_BIND_SCRIPT="${CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT:-$SCRIPT_DIR/cloud-validate-device-bind.sh}"
MQTT_TEST_SCRIPT="${CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT:-$SCRIPT_DIR/cloud_mqtt_test.sh}"

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

log() {
	printf '[cloud-staging-e2e] %s\n' "$*" >&2
}

usage() {
	cat <<'USAGE'
Usage:
  scripts/cloud-staging-e2e-test.sh --env-root cloud_env/staging --plan
  scripts/cloud-staging-e2e-test.sh --env-root cloud_env/staging --run --confirm <stack-name> [options]

Options:
  --workspace PATH          Default: script parent workspace.
  --env-root PATH           Required environment directory, for example cloud_env/staging.
  --plan                    Print the execution plan without mutating cloud resources. Default.
  --run                     Execute the one-stop staging E2E flow.
  --confirm STACK           Required with --run. Must match CLOUD_STACK_NAME.
  --skip-remove             Do not call cloud-remove-all-vm.sh before provisioning.
  --brandname NAME          Default: RTK.
  --user-count N            Default: 10.
  --device-count N          Default: 100.
  --device-mix SPEC         Default: camera=40,light=25,air_conditioner=20,smart_meter=15.
  --device-prefix PREFIX    Default: load-device.
  --video-release VERSION   Forwarded to cloud-provision.sh.
  --account-release VERSION Forwarded to cloud-provision.sh.
  --admin-release VERSION   Forwarded to cloud-provision.sh.
  --out-dir PATH            Default: <env-root>/artifacts/staging-e2e/<timestamp>.
  --skip-mqtt-probe         Run simulation without the live MQTT TLS/mTLS socket probe.
  -h, --help                Show this help.

Flow:
  remove VM -> provision all -> create brand -> create users -> create devices
  -> bind/provision devices -> validate bind -> live home MQTT E2E.

Safety:
  --run requires --confirm matching CLOUD_STACK_NAME. Reports are sanitized and
  do not include passwords, bearer tokens, raw secrets, private keys, or cert bodies.
USAGE
}

require_value() {
	local opt="$1"
	local value="${2:-}"
	[[ -n "$value" ]] || die "$opt requires a value"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--workspace) require_value "$1" "${2:-}"; WORKSPACE="$2"; shift 2 ;;
	--env-root) require_value "$1" "${2:-}"; ENV_ROOT="$2"; shift 2 ;;
	--plan) MODE="plan"; shift ;;
	--run) MODE="run"; shift ;;
	--confirm) require_value "$1" "${2:-}"; CONFIRM="$2"; shift 2 ;;
	--skip-remove) SKIP_REMOVE=1; shift ;;
	--brandname) require_value "$1" "${2:-}"; BRANDNAME="$2"; shift 2 ;;
	--user-count) require_value "$1" "${2:-}"; USER_COUNT="$2"; shift 2 ;;
	--device-count) require_value "$1" "${2:-}"; DEVICE_COUNT="$2"; shift 2 ;;
	--device-mix) require_value "$1" "${2:-}"; DEVICE_MIX="$2"; shift 2 ;;
	--device-prefix) require_value "$1" "${2:-}"; DEVICE_PREFIX="$2"; shift 2 ;;
	--video-release) require_value "$1" "${2:-}"; VIDEO_RELEASE="$2"; shift 2 ;;
	--account-release) require_value "$1" "${2:-}"; ACCOUNT_RELEASE="$2"; shift 2 ;;
	--admin-release) require_value "$1" "${2:-}"; ADMIN_RELEASE="$2"; shift 2 ;;
	--out-dir) require_value "$1" "${2:-}"; OUT_DIR="$2"; shift 2 ;;
	--skip-mqtt-probe) SKIP_MQTT_PROBE=1; shift ;;
	-h|--help) usage; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$ENV_ROOT" ]] || die "--env-root is required"
[[ "$USER_COUNT" =~ ^[0-9]+$ && "$USER_COUNT" -gt 0 ]] || die "--user-count must be a positive integer"
[[ "$DEVICE_COUNT" =~ ^[0-9]+$ && "$DEVICE_COUNT" -gt 0 ]] || die "--device-count must be a positive integer"

WORKSPACE="$(cd "$WORKSPACE" && pwd)"
ENV_ROOT="$(cloud_env_init "$WORKSPACE" "$ENV_ROOT")"
cloud_env_load_environment "$ENV_ROOT" ""
STACK_NAME="$CLOUD_STACK_NAME"

if [[ "$MODE" == "run" && "$CONFIRM" != "$STACK_NAME" ]]; then
	die "--confirm $CONFIRM does not match CLOUD_STACK_NAME=$STACK_NAME"
fi

if [[ -z "$OUT_DIR" ]]; then
	OUT_DIR="$(cloud_env_artifacts_dir "$ENV_ROOT")/staging-e2e/$(date -u '+%Y%m%dT%H%M%SZ')"
fi

brand_slug() {
	printf '%s' "$BRANDNAME" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//' | awk '{if ($0=="") print "brand"; else print}'
}

latest_file() {
	local pattern="$1"
	find "$(dirname "$pattern")" -maxdepth 1 -type f -name "$(basename "$pattern")" 2>/dev/null | sort | tail -1
}

print_plan() {
	cat <<EOF
cloud-staging-e2e-test plan
workspace: $WORKSPACE
env_root: $ENV_ROOT
stack: $STACK_NAME
brandname: $BRANDNAME
user_count: $USER_COUNT
device_count: $DEVICE_COUNT
device_mix: $DEVICE_MIX
skip_remove: $SKIP_REMOVE
steps:
EOF
	if [[ "$SKIP_REMOVE" != "1" ]]; then
		printf '  - remove VMs with %s\n' "$REMOVE_SCRIPT"
	fi
	cat <<EOF
  - provision all with $PROVISION_SCRIPT
  - create brand cloud with $CREATE_BRAND_SCRIPT
  - create users with $CREATE_USERS_SCRIPT
  - generate/factory-enroll devices with $GENERATE_DEVICES_SCRIPT
  - bind/provision devices with $BIND_DEVICES_SCRIPT
  - validate bind artifact with $VALIDATE_BIND_SCRIPT
  - run live home MQTT E2E with $MQTT_TEST_SCRIPT
EOF
}

if [[ "$MODE" == "plan" ]]; then
	print_plan
	exit 0
fi

mkdir -p "$OUT_DIR/logs"
SUMMARY_FILE="$OUT_DIR/summary.json"
REPORT_FILE="$OUT_DIR/TEST_REPORT.md"
STEPS_JSONL="$OUT_DIR/steps.jsonl"
: > "$STEPS_JSONL"

run_step() {
	local name="$1"
	shift
	local start end rc log_file
	start="$(date +%s)"
	log_file="$OUT_DIR/logs/$name.log"
	log "start: $name"
	set +e
	"$@" >"$log_file" 2>&1
	rc=$?
	set -e
	end="$(date +%s)"
	jq -cn \
		--arg name "$name" \
		--arg status "$([[ "$rc" == "0" ]] && printf PASS || printf FAIL)" \
		--arg log_file "$log_file" \
		--argjson exit_code "$rc" \
		--argjson duration_seconds "$((end - start))" \
		'{name:$name,status:$status,exit_code:$exit_code,duration_seconds:$duration_seconds,log_file:$log_file}' >> "$STEPS_JSONL"
	if [[ "$rc" != "0" ]]; then
		log "fail: $name (see $log_file)"
		return "$rc"
	fi
	log "pass: $name"
}

provision_args=(--workspace "$WORKSPACE" --env-root "$ENV_ROOT" --reset-and-all --confirm "$STACK_NAME")
[[ -n "$VIDEO_RELEASE" ]] && provision_args+=(--video-release "$VIDEO_RELEASE")
[[ -n "$ACCOUNT_RELEASE" ]] && provision_args+=(--account-release "$ACCOUNT_RELEASE")
[[ -n "$ADMIN_RELEASE" ]] && provision_args+=(--admin-release "$ADMIN_RELEASE")

if [[ "$SKIP_REMOVE" != "1" ]]; then
	run_step remove_vm bash -c 'printf "yes\n" | "$@"' _ "$REMOVE_SCRIPT" --workspace "$WORKSPACE" --env-root "$ENV_ROOT"
fi
run_step provision_all "$PROVISION_SCRIPT" "${provision_args[@]}"
run_step create_brand "$CREATE_BRAND_SCRIPT" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" --brandname "$BRANDNAME"
run_step create_users "$CREATE_USERS_SCRIPT" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" --brandname "$BRANDNAME" --count "$USER_COUNT"
run_step create_devices "$GENERATE_DEVICES_SCRIPT" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" --count "$DEVICE_COUNT" --mix "$DEVICE_MIX" --prefix "$DEVICE_PREFIX" --force

slug="$(brand_slug)"
users_file="$(latest_file "$(cloud_env_artifacts_dir "$ENV_ROOT")/users/$slug-users-*.json")"
[[ -n "$users_file" ]] || die "no users artifact found for brand slug $slug"

run_step bind_devices "$BIND_DEVICES_SCRIPT" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" --brandname "$BRANDNAME" --users-file "$users_file" --devices-dir "$(cloud_env_test_devices_dir "$ENV_ROOT")" --count "$DEVICE_COUNT"

bind_file="$(latest_file "$(cloud_env_artifacts_dir "$ENV_ROOT")/device-bind/$slug-device-bind-*.json")"
[[ -n "$bind_file" ]] || die "no device-bind artifact found for brand slug $slug"
expected_per_user="$(( (DEVICE_COUNT + USER_COUNT - 1) / USER_COUNT ))"
run_step validate_bind "$VALIDATE_BIND_SCRIPT" --bind-artifact "$bind_file" --out-dir "$OUT_DIR/bind-validation" --expected-count "$DEVICE_COUNT" --expected-devices-per-user "$expected_per_user"

mqtt_args=(--env-root "$ENV_ROOT" --brandname "$BRANDNAME" --profile smoke --out-dir "$OUT_DIR/home-mqtt")
if [[ "$SKIP_MQTT_PROBE" == "1" ]]; then
	mqtt_args+=(--no-mqtt-probe)
else
	mqtt_args+=(--mqtt-probe)
fi
run_step cloud_mqtt_test "$MQTT_TEST_SCRIPT" "${mqtt_args[@]}"

overall="pass"
if jq -e 'select(.status != "PASS")' "$STEPS_JSONL" >/dev/null; then
	overall="fail"
fi

jq -s \
	--arg overall "$overall" \
	--arg generated_at "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
	--arg env_root "$ENV_ROOT" \
	--arg stack "$STACK_NAME" \
	--arg brandname "$BRANDNAME" \
	--arg users_file "$users_file" \
	--arg bind_file "$bind_file" \
	--arg report_file "$REPORT_FILE" \
	'{
		overall: $overall,
		generated_at: $generated_at,
		env_root: $env_root,
		stack: $stack,
		brandname: $brandname,
		artifacts: {
			users_file: $users_file,
			device_bind_file: $bind_file,
			report_file: $report_file
		},
		steps: .
	}' "$STEPS_JSONL" > "$SUMMARY_FILE"

{
	printf '# Staging E2E Test Report\n\n'
	printf -- '- Overall: %s\n' "$overall"
	printf -- '- Generated: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	printf -- '- Env root: `%s`\n' "$ENV_ROOT"
	printf -- '- Stack: `%s`\n' "$STACK_NAME"
	printf -- '- Brand: `%s`\n\n' "$BRANDNAME"
	printf '## Steps\n\n'
	printf '| Step | Status | Duration seconds | Log |\n'
	printf '| --- | --- | ---: | --- |\n'
	jq -r '. | "| \(.name) | \(.status) | \(.duration_seconds) | `\(.log_file)` |"' "$STEPS_JSONL"
	printf '\n## Artifacts\n\n'
	printf -- '- Users artifact: `%s`\n' "$users_file"
	printf -- '- Device bind artifact: `%s`\n' "$bind_file"
	printf -- '- Home MQTT report: `%s`\n' "$OUT_DIR/home-mqtt/TEST_REPORT.md"
	printf -- '- Home MQTT results: `%s`\n' "$OUT_DIR/home-mqtt/results.json"
} > "$REPORT_FILE"

if grep -R -Ei 'password|bearer|raw-token|-----BEGIN|PRIVATE KEY|JWT_ACCESS_SECRET|VIDEO_CLOUD_AUTH_SECRET' "$SUMMARY_FILE" "$REPORT_FILE" >/dev/null; then
	die "sanitized report contains sensitive terms"
fi

jq -cn --arg overall "$overall" --arg summary_file "$SUMMARY_FILE" --arg report_file "$REPORT_FILE" \
	'{overall:$overall,summary_file:$summary_file,report_file:$report_file}'
[[ "$overall" == "pass" ]]
