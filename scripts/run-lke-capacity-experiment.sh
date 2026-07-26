#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_ROOT="$ROOT/cloud_env/staging/runtime"
BRANDNAME="RTK"
BRAND_PLAN=""
TARGET_DEVICES=""
DEVICES_PER_USER="20"
MQTT_PODS=""
NODE_COUNT=""
NODE_TYPE=""
MQTT_REQUEST_CPU=""
MQTT_REQUEST_MEMORY=""
MQTT_LIMIT_MEMORY=""
EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE=""
CLOUD_LOGGER_REQUEST_MEMORY="4Gi"
CLOUD_LOGGER_LIMIT_MEMORY="8Gi"
LOAD_GENERATOR_DEVICES_PER_VM="20000"
MQTT_CONNECTIONS_PER_POD="20000"
STAGE_WARM_UP="10m"
STAGE_STEADY="20m"
STAGE_COOL_DOWN="2m"
LIVE_RUNNER_TIMEOUT_GRACE="15m"
USER_CONCURRENCY="16"
DEVICE_CONCURRENCY="16"
BIND_CONCURRENCY="64"
DATA_SETUP_RETRIES="3"
FACTORY_ENROLL_PORTS="18443,18444,18445,18446"
BIND_PROVISION_TIMEOUT="60m"
CLEANUP_ORPHAN_VOLUME_IDS=""
RUN_ID=""
DEVICE_MIX="light=18,switch=7,smart_plug=12,air_conditioner=10,environment_sensor=12,security_sensor=10,smart_meter=8,camera_status=7,door_lock=4,appliance=7,gateway=5"
LIVE=0
CONFIRM=""
PLAN_ONLY=0
CURRENT_PHASE="init"
RUN_LOG_DIR=""

usage() {
	cat <<'USAGE'
Usage:
  scripts/run-lke-capacity-experiment.sh --target-devices N --node-type TYPE [--mqtt-pods N] [--node-count N] [--live --confirm video-cloud-staging]

Runs one recorded LKE capacity experiment using the existing project scripts:
cleanup -> fresh provision -> staging data setup -> home-100k workflow-live ->
capacity-run-summary.json.

Defaults:
  --env-root cloud_env/staging/runtime
  --brandname RTK
  --brand-plan FILE
  --devices-per-user 20
  --load-generator-devices-per-vm 20000
  --mqtt-connections-per-pod 20000
  --mqtt-pods ceil(target-devices / mqtt-connections-per-pod)
  --node-count mqtt-pods, as the spread minimum; override after CPU/memory review
  --mqtt-request-cpu VALUE
  --mqtt-request-memory VALUE
  --mqtt-limit-memory VALUE
  --emqx-force-shutdown-max-heap-size VALUE
  --cloud-logger-request-memory VALUE
  --cloud-logger-limit-memory VALUE
  --stage-warm-up 10m
  --stage-steady 20m
  --stage-cool-down 2m
  --live-runner-timeout-grace 15m
  --user-concurrency 16
  --device-concurrency 16
  --data-setup-retries 3
  --factory-enroll-ports 18443,18444,18445,18446
  --bind-provision-timeout 60m
  --cleanup-orphan-volume-ids ID,ID

Safety:
  Without --live, this prints the planned commands only. Live mode requires
  --confirm to match CLOUD_STACK_NAME from env/stack.env.
USAGE
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 2
}

env_file_value() {
	local file="$1"
	local key="$2"
	awk -F= -v key="$key" '$1 == key {print $2; exit}' "$file" 2>/dev/null || true
}

ceil_div() {
	local n="$1"
	local d="$2"
	printf '%d\n' $(((n + d - 1) / d))
}

set_stack_value() {
	local key="$1"
	local value="$2"
	local file="$ENV_ROOT/env/stack.env"
	local tmp
	tmp="$(mktemp)"
	if [[ -f "$file" ]] && grep -q "^${key}=" "$file"; then
		awk -F= -v key="$key" -v value="$value" 'BEGIN{OFS="="} $1 == key {$0=key "=" value} {print}' "$file" > "$tmp"
	else
		if [[ -f "$file" ]]; then
			cat "$file" > "$tmp"
		fi
		printf '%s=%s\n' "$key" "$value" >> "$tmp"
	fi
	mv "$tmp" "$file"
}

run_or_print() {
	printf '+'
	printf ' %q' "$@"
	printf '\n'
	if [[ "$LIVE" -eq 1 ]]; then
		"$@"
	fi
}

run_or_print_logged() {
	local log_file="$1"
	shift
	printf '+'
	printf ' %q' "$@"
	printf ' > %q 2>&1\n' "$log_file"
	if [[ "$LIVE" -eq 1 ]]; then
		mkdir -p "$(dirname "$log_file")"
		"$@" > "$log_file" 2>&1
	fi
}

cleanup_load_generators() {
	if [[ "$LIVE" -ne 1 || -z "${RUN_ID:-}" ]]; then
		return
	fi
	if [[ ! -f "$ROOT/loadtests/home-100k/reports/$RUN_ID/vms.json" ]]; then
		return
	fi
	local log_file="${1:-}"
	(
		cd "$ROOT/loadtests/home-100k"
		if [[ -n "$log_file" ]]; then
			HOME100K_ENV_ROOT="$ENV_ROOT" \
			HOME100K_RUN_ID="$RUN_ID" \
			HOME100K_SSH_KEY="$HOME/.ssh/id_ed25519_rtkcloud" \
			HOME100K_AUTHORIZED_KEY_FILE="$HOME/.ssh/id_ed25519_rtkcloud.pub" \
				./scripts/home-100k.sh destroy-vms --live --confirm-live > "$log_file" 2>&1 || true
		else
			HOME100K_ENV_ROOT="$ENV_ROOT" \
			HOME100K_RUN_ID="$RUN_ID" \
			HOME100K_SSH_KEY="$HOME/.ssh/id_ed25519_rtkcloud" \
			HOME100K_AUTHORIZED_KEY_FILE="$HOME/.ssh/id_ed25519_rtkcloud.pub" \
				./scripts/home-100k.sh destroy-vms --live --confirm-live >/dev/null 2>&1 || true
		fi
	)
}

report_success() {
	local report="$REPORT_DIR/TEST_REPORT.md"
	[[ -f "$report" ]] || return 1
	grep -F -- '- Status: COMPLETE' "$report" >/dev/null &&
		grep -F -- '- Result: SUCCESS' "$report" >/dev/null
}

record_abort() {
	local rc="$1"
	if [[ "$LIVE" -ne 1 || "$rc" -eq 0 || -z "${CAPACITY_DIR:-}" ]]; then
		return
	fi
	local classification="unknown"
	local root_cause=""
	if [[ "$CURRENT_PHASE" == "data-setup" ]]; then
		if data_setup_retryable_failure; then
			classification="data_setup_port_forward_lost"
			root_cause="Kubernetes port-forward disconnected during staging data setup; this run did not reach load-generator runtime and must not be used as server capacity evidence."
		else
			classification="data_setup_failed"
		fi
	elif [[ "$CURRENT_PHASE" == "workflow-live" ]]; then
		classification="load_generator_or_runtime_failed"
	elif [[ "$CURRENT_PHASE" == "report-collection" ]]; then
		classification="report_collection_failed"
		root_cause="home-100k workflow-live returned before capacity wrapper could find results.json; preserve load-generator VMs and workflow logs for resume or manual collection."
	fi
	mkdir -p "$CAPACITY_DIR"
	cat > "$CAPACITY_DIR/aborted-summary.json" <<EOF
{
  "schema": "rtk-cloud-workspace.lke-capacity-aborted-summary/v1",
  "run_id": "$RUN_ID",
  "aborted_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "phase": "$CURRENT_PHASE",
  "exit_code": $rc,
  "classification": "$classification",
  "root_cause": "$root_cause",
  "request": "$CAPACITY_DIR/request.json",
  "data_setup_dir": "$DATA_DIR",
  "report_dir": "$REPORT_DIR",
  "workflow_log": "$RUN_LOG_DIR/workflow-live.log",
  "workflow_exit_file": "$WORKFLOW_STATUS_FILE",
  "summary_file": "$SUMMARY_FILE",
  "note": "This aborted run must not be used for MQTT pod, LKE node, or load-generator safe coefficients."
}
EOF
}

data_setup_retryable_failure() {
	[[ -d "${DATA_DIR:-}/logs" ]] || return 1
	grep -R -q -E \
		'127\.0\.0\.1:[0-9]+: connect: connection refused|port-forward .*lost connection to pod|connection reset by peer|error creating error stream .*Timeout occurred|error upgrading connection|broken pipe|unexpected EOF' \
		"$DATA_DIR/logs" 2>/dev/null
}

data_setup_last_progress() {
	local logs_dir="${1:-${DATA_DIR:-}/logs}"
	[[ -d "$logs_dir" ]] || return 0
	grep -R -h -E 'bulk bind chunk summary|bind progress|device generation progress|user creation progress|pass:|fail:|error:' "$logs_dir" 2>/dev/null | tail -n 1 || true
}

run_data_setup() {
	local attempt=1
	local rc=0
	local resume_flag="--no-resume"
	local from_step_args=()
	while (( attempt <= DATA_SETUP_RETRIES )); do
		printf '[capacity] data setup attempt %d/%d resume_flag=%s' "$attempt" "$DATA_SETUP_RETRIES" "$resume_flag"
		if [[ "${#from_step_args[@]}" -gt 0 ]]; then
			printf ' %s' "${from_step_args[@]}"
		fi
		printf '\n'
		rm -rf "$DATA_DIR/logs"
		local setup_args=(
			--env-root "$ENV_ROOT"
			--brandname "$BRANDNAME"
			--user-count "$USERS"
			--device-count "$TARGET_DEVICES"
			--device-mix "$DEVICE_MIX"
			--device-prefix "$RUN_ID-device"
			--user-concurrency "$USER_CONCURRENCY"
			--device-concurrency "$DEVICE_CONCURRENCY"
			--bind-concurrency "$BIND_CONCURRENCY"
			"$resume_flag"
		)
		if [[ -n "$BRAND_PLAN" ]]; then
			setup_args+=(--brand-plan "$BRAND_PLAN")
			if [[ "$LIVE" -eq 1 ]]; then
				setup_args+=(--load-run-id "$RUN_ID" --load-target "$((TARGET_DEVICES / 1000))K" --email-activate-owners)
			fi
		fi
		if [[ "${#from_step_args[@]}" -gt 0 ]]; then
			setup_args+=("${from_step_args[@]}")
		fi
		setup_args+=(--out-dir "$DATA_DIR")
		set +e
		CLOUD_STAGING_E2E_FACTORY_ENROLL_PORTS="$FACTORY_ENROLL_PORTS" \
		CLOUD_STAGING_E2E_BIND_PROVISION_TIMEOUT="$BIND_PROVISION_TIMEOUT" \
			"$ROOT/scripts/setup-staging-e2e-data.sh" "${setup_args[@]}"
		rc=$?
		set -e
		if [[ "$rc" -eq 0 ]]; then
			if [[ -f "$DATA_DIR/resolved-brand-plan.json" ]]; then
				BRAND_PLAN="$DATA_DIR/resolved-brand-plan.json"
			fi
			return 0
		fi
		local attempt_logs="$CAPACITY_DIR/data-setup-attempt-$attempt-logs"
		if [[ -d "$DATA_DIR/logs" ]]; then
			rm -rf "$attempt_logs"
			cp -R "$DATA_DIR/logs" "$attempt_logs"
		fi
		if (( attempt >= DATA_SETUP_RETRIES )) || ! data_setup_retryable_failure; then
			return "$rc"
		fi
		local last_progress
		last_progress="$(data_setup_last_progress "$DATA_DIR/logs")"
		printf '[capacity] data setup failed with retryable port-forward/network error; retrying with --resume' >&2
		if [[ -n "$last_progress" ]]; then
			printf ' last_progress=%q' "$last_progress" >&2
		fi
		printf '\n' >&2
		resume_flag="--resume"
		if [[ -f "$attempt_logs/bind_devices.log" ]]; then
			from_step_args=(--from-step bind_devices)
		fi
		sleep "${LKE_CAPACITY_DATA_SETUP_RETRY_SLEEP_SECONDS:-10}"
		attempt=$((attempt + 1))
	done
	return "$rc"
}

on_exit() {
	local rc="$?"
	record_abort "$rc"
	exit "$rc"
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--env-root) ENV_ROOT="$2"; shift 2 ;;
		--brandname) BRANDNAME="$2"; shift 2 ;;
		--brand-plan) BRAND_PLAN="$2"; shift 2 ;;
		--target-devices) TARGET_DEVICES="$2"; shift 2 ;;
		--devices-per-user) DEVICES_PER_USER="$2"; shift 2 ;;
		--mqtt-pods) MQTT_PODS="$2"; shift 2 ;;
		--node-count) NODE_COUNT="$2"; shift 2 ;;
		--node-type) NODE_TYPE="$2"; shift 2 ;;
		--mqtt-request-cpu) MQTT_REQUEST_CPU="$2"; shift 2 ;;
		--mqtt-request-memory) MQTT_REQUEST_MEMORY="$2"; shift 2 ;;
		--mqtt-limit-memory) MQTT_LIMIT_MEMORY="$2"; shift 2 ;;
		--emqx-force-shutdown-max-heap-size) EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE="$2"; shift 2 ;;
		--cloud-logger-request-memory) CLOUD_LOGGER_REQUEST_MEMORY="$2"; shift 2 ;;
		--cloud-logger-limit-memory) CLOUD_LOGGER_LIMIT_MEMORY="$2"; shift 2 ;;
		--load-generator-devices-per-vm) LOAD_GENERATOR_DEVICES_PER_VM="$2"; shift 2 ;;
		--mqtt-connections-per-pod) MQTT_CONNECTIONS_PER_POD="$2"; shift 2 ;;
		--stage-warm-up) STAGE_WARM_UP="$2"; shift 2 ;;
		--stage-steady) STAGE_STEADY="$2"; shift 2 ;;
		--stage-cool-down) STAGE_COOL_DOWN="$2"; shift 2 ;;
		--live-runner-timeout-grace) LIVE_RUNNER_TIMEOUT_GRACE="$2"; shift 2 ;;
		--user-concurrency) USER_CONCURRENCY="$2"; shift 2 ;;
		--device-concurrency) DEVICE_CONCURRENCY="$2"; shift 2 ;;
		--bind-concurrency) BIND_CONCURRENCY="$2"; shift 2 ;;
		--data-setup-retries) DATA_SETUP_RETRIES="$2"; shift 2 ;;
		--factory-enroll-ports) FACTORY_ENROLL_PORTS="$2"; shift 2 ;;
		--bind-provision-timeout) BIND_PROVISION_TIMEOUT="$2"; shift 2 ;;
		--cleanup-orphan-volume-ids) CLEANUP_ORPHAN_VOLUME_IDS="$2"; shift 2 ;;
		--device-mix) DEVICE_MIX="$2"; shift 2 ;;
		--run-id) RUN_ID="$2"; shift 2 ;;
		--live) LIVE=1; shift ;;
		--plan) PLAN_ONLY=1; shift ;;
		--confirm) CONFIRM="$2"; shift 2 ;;
		-h|--help) usage; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$TARGET_DEVICES" ]] || die "--target-devices is required"
[[ -n "$NODE_TYPE" ]] || die "--node-type is required"
[[ "$TARGET_DEVICES" =~ ^[0-9]+$ ]] || die "--target-devices must be numeric"
[[ "$DEVICES_PER_USER" =~ ^[0-9]+$ ]] || die "--devices-per-user must be numeric"
[[ -z "$MQTT_PODS" || "$MQTT_PODS" =~ ^[0-9]+$ ]] || die "--mqtt-pods must be numeric"
[[ -z "$NODE_COUNT" || "$NODE_COUNT" =~ ^[0-9]+$ ]] || die "--node-count must be numeric"
[[ "$LOAD_GENERATOR_DEVICES_PER_VM" =~ ^[0-9]+$ ]] || die "--load-generator-devices-per-vm must be numeric"
[[ "$MQTT_CONNECTIONS_PER_POD" =~ ^[0-9]+$ ]] || die "--mqtt-connections-per-pod must be numeric"
[[ "$USER_CONCURRENCY" =~ ^[0-9]+$ ]] || die "--user-concurrency must be numeric"
[[ "$DEVICE_CONCURRENCY" =~ ^[0-9]+$ ]] || die "--device-concurrency must be numeric"
[[ "$BIND_CONCURRENCY" =~ ^[0-9]+$ ]] || die "--bind-concurrency must be numeric"
[[ "$DATA_SETUP_RETRIES" =~ ^[0-9]+$ && "$DATA_SETUP_RETRIES" -gt 0 ]] || die "--data-setup-retries must be a positive integer"
[[ "$FACTORY_ENROLL_PORTS" =~ ^[0-9]+(,[0-9]+)*$ ]] || die "--factory-enroll-ports must be a comma-separated port list"
[[ -z "$CLEANUP_ORPHAN_VOLUME_IDS" || "$CLEANUP_ORPHAN_VOLUME_IDS" =~ ^[0-9]+(,[0-9]+)*$ ]] || die "--cleanup-orphan-volume-ids must be a comma-separated numeric ID list"
[[ -z "$MQTT_REQUEST_CPU" || "$MQTT_REQUEST_CPU" =~ ^[A-Za-z0-9._+-]+$ ]] || die "--mqtt-request-cpu contains unsupported characters"
[[ -z "$MQTT_REQUEST_MEMORY" || "$MQTT_REQUEST_MEMORY" =~ ^[A-Za-z0-9._+-]+$ ]] || die "--mqtt-request-memory contains unsupported characters"
[[ -z "$MQTT_LIMIT_MEMORY" || "$MQTT_LIMIT_MEMORY" =~ ^[A-Za-z0-9._+-]+$ ]] || die "--mqtt-limit-memory contains unsupported characters"
[[ -z "$EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE" || "$EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE" =~ ^[A-Za-z0-9._+-]+$ ]] || die "--emqx-force-shutdown-max-heap-size contains unsupported characters"
[[ -z "$CLOUD_LOGGER_REQUEST_MEMORY" || "$CLOUD_LOGGER_REQUEST_MEMORY" =~ ^[A-Za-z0-9._+-]+$ ]] || die "--cloud-logger-request-memory contains unsupported characters"
[[ -z "$CLOUD_LOGGER_LIMIT_MEMORY" || "$CLOUD_LOGGER_LIMIT_MEMORY" =~ ^[A-Za-z0-9._+-]+$ ]] || die "--cloud-logger-limit-memory contains unsupported characters"
[[ "$DEVICES_PER_USER" -gt 0 ]] || die "--devices-per-user must be greater than zero"
[[ "$LOAD_GENERATOR_DEVICES_PER_VM" -gt 0 ]] || die "--load-generator-devices-per-vm must be greater than zero"
[[ "$MQTT_CONNECTIONS_PER_POD" -gt 0 ]] || die "--mqtt-connections-per-pod must be greater than zero"

ENV_ROOT="$(cd "$ENV_ROOT" && pwd)"
STACK_FILE="$ENV_ROOT/env/stack.env"
STACK_NAME="$(env_file_value "$STACK_FILE" CLOUD_STACK_NAME)"
STACK_NAME="${STACK_NAME:-video-cloud-staging}"
if [[ "$LIVE" -eq 1 && "$CONFIRM" != "$STACK_NAME" ]]; then
	die "--live requires --confirm $STACK_NAME"
fi

if [[ -z "$RUN_ID" ]]; then
	if (( TARGET_DEVICES % 1000 == 0 )); then
		RUN_ID="cap${TARGET_DEVICES}d-$(date -u +%Y%m%dT%H%M%SZ)"
	else
		RUN_ID="cap${TARGET_DEVICES}-$(date -u +%Y%m%dT%H%M%SZ)"
	fi
fi
trap on_exit EXIT

USERS="$(ceil_div "$TARGET_DEVICES" "$DEVICES_PER_USER")"
LOAD_GENERATOR_VMS="$(ceil_div "$TARGET_DEVICES" "$LOAD_GENERATOR_DEVICES_PER_VM")"
MQTT_PODS_SOURCE="override"
if [[ -z "$MQTT_PODS" ]]; then
	MQTT_PODS="$(ceil_div "$TARGET_DEVICES" "$MQTT_CONNECTIONS_PER_POD")"
	MQTT_PODS_SOURCE="formula"
fi
NODE_COUNT_SOURCE="override"
if [[ -z "$NODE_COUNT" ]]; then
	# This wrapper can compute the MQTT spread floor. CPU and memory packing
	# still need review from the generated provision plan before a live run.
	NODE_COUNT="$MQTT_PODS"
	NODE_COUNT_SOURCE="formula_spread_min"
fi
CAPACITY_DIR="$ENV_ROOT/artifacts/capacity-experiments/$RUN_ID"
DATA_DIR="$ENV_ROOT/artifacts/staging-e2e-data/$RUN_ID"
REPORT_DIR="$ROOT/loadtests/home-100k/reports/$RUN_ID"
SUMMARY_FILE="$CAPACITY_DIR/capacity-run-summary.json"
WORKFLOW_STATUS_FILE="$CAPACITY_DIR/workflow-live.exit"
RUN_LOG_DIR="$CAPACITY_DIR/logs"

mkdir -p "$CAPACITY_DIR" "$RUN_LOG_DIR"
cat > "$CAPACITY_DIR/request.json" <<EOF
{
  "schema": "rtk-cloud-workspace.lke-capacity-experiment-request/v1",
  "run_id": "$RUN_ID",
  "env_root": "$ENV_ROOT",
  "brandname": "$BRANDNAME",
  "brand_plan_file": "$BRAND_PLAN",
  "target_devices": $TARGET_DEVICES,
  "users": $USERS,
  "devices_per_user": $DEVICES_PER_USER,
  "mqtt_pods": $MQTT_PODS,
  "mqtt_pods_source": "$MQTT_PODS_SOURCE",
  "node_count": $NODE_COUNT,
  "node_count_source": "$NODE_COUNT_SOURCE",
  "node_type": "$NODE_TYPE",
  "formula": {
    "users": "ceil(target_devices / devices_per_user)",
    "load_generator_vms": "ceil(target_devices / load_generator_devices_per_vm)",
    "mqtt_pods": "ceil(target_devices / mqtt_connections_per_pod)",
    "node_count": "max(cpu_nodes, memory_nodes, mqtt_pods, spread_min); wrapper default uses mqtt_pods as spread_min when --node-count is omitted"
  },
  "mqtt_request_cpu": "$MQTT_REQUEST_CPU",
  "mqtt_request_memory": "$MQTT_REQUEST_MEMORY",
  "mqtt_limit_memory": "$MQTT_LIMIT_MEMORY",
  "emqx_force_shutdown_max_heap_size": "$EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE",
  "cloud_logger_request_memory": "$CLOUD_LOGGER_REQUEST_MEMORY",
  "cloud_logger_limit_memory": "$CLOUD_LOGGER_LIMIT_MEMORY",
  "load_generator_devices_per_vm": $LOAD_GENERATOR_DEVICES_PER_VM,
  "mqtt_connections_per_pod": $MQTT_CONNECTIONS_PER_POD,
  "load_generator_vms": $LOAD_GENERATOR_VMS,
  "stage_warm_up": "$STAGE_WARM_UP",
  "stage_steady": "$STAGE_STEADY",
  "stage_cool_down": "$STAGE_COOL_DOWN",
  "live_runner_timeout_grace": "$LIVE_RUNNER_TIMEOUT_GRACE",
  "user_concurrency": $USER_CONCURRENCY,
  "device_concurrency": $DEVICE_CONCURRENCY,
  "bind_concurrency": $BIND_CONCURRENCY,
  "data_setup_retries": $DATA_SETUP_RETRIES,
  "factory_enroll_ports": "$FACTORY_ENROLL_PORTS",
  "bind_provision_timeout": "$BIND_PROVISION_TIMEOUT",
  "cleanup_orphan_volume_ids": "$CLEANUP_ORPHAN_VOLUME_IDS"
}
EOF

printf '[capacity] run_id=%s target_devices=%s users=%s mqtt_pods=%s node_count=%s node_type=%s load_generator_vms=%s\n' \
	"$RUN_ID" "$TARGET_DEVICES" "$USERS" "$MQTT_PODS" "$NODE_COUNT" "$NODE_TYPE" "$LOAD_GENERATOR_VMS"
printf '[capacity] request=%s\n' "$CAPACITY_DIR/request.json"

if [[ "$LIVE" -eq 1 ]]; then
	cp "$STACK_FILE" "$CAPACITY_DIR/stack.env.before"
	# Persist explicit image overrides in the environment source of truth. Without
	# this, a later provision --deploy can silently reload an older image manifest
	# and roll the freshly validated stack backward.
	for image_key in LKE_VIDEO_CLOUD_IMAGE LKE_ACCOUNT_MANAGER_IMAGE LKE_CLOUD_ADMIN_IMAGE LKE_FRONTEND_IMAGE LKE_CLOUD_LOGGER_IMAGE; do
		image_value="${!image_key:-}"
		if [[ -n "$image_value" ]]; then
			set_stack_value "$image_key" "$image_value"
		fi
	done
	set_stack_value LKE_TARGET_CONNECTS "$TARGET_DEVICES"
	set_stack_value LKE_MQTT_REPLICAS "$MQTT_PODS"
	set_stack_value LKE_NODE_COUNT "$NODE_COUNT"
	set_stack_value LKE_NODE_TYPE "$NODE_TYPE"
	set_stack_value LKE_MQTT_CONNECTIONS_PER_POD "$MQTT_CONNECTIONS_PER_POD"
	if [[ -n "$MQTT_REQUEST_CPU" ]]; then
		set_stack_value LKE_MQTT_REQUEST_CPU "$MQTT_REQUEST_CPU"
	fi
	if [[ -n "$MQTT_REQUEST_MEMORY" ]]; then
		set_stack_value LKE_MQTT_REQUEST_MEMORY "$MQTT_REQUEST_MEMORY"
	fi
	if [[ -n "$MQTT_LIMIT_MEMORY" ]]; then
		set_stack_value LKE_MQTT_LIMIT_MEMORY "$MQTT_LIMIT_MEMORY"
	fi
	if [[ -n "$EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE" ]]; then
		set_stack_value LKE_EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE "$EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE"
	fi
	if [[ -n "$CLOUD_LOGGER_REQUEST_MEMORY" ]]; then
		set_stack_value LKE_CLOUD_LOGGER_REQUEST_MEMORY "$CLOUD_LOGGER_REQUEST_MEMORY"
	fi
	if [[ -n "$CLOUD_LOGGER_LIMIT_MEMORY" ]]; then
		set_stack_value LKE_CLOUD_LOGGER_LIMIT_MEMORY "$CLOUD_LOGGER_LIMIT_MEMORY"
	fi
	cp "$STACK_FILE" "$CAPACITY_DIR/stack.env.applied"
else
	printf '[capacity] dry-run only; pass --live --confirm %s to execute cleanup/deploy/load-test\n' "$STACK_NAME"
fi

cleanup_args=(--env-root "$ENV_ROOT")
if [[ -n "$CLEANUP_ORPHAN_VOLUME_IDS" ]]; then
	cleanup_args+=(--include-orphan-volumes --orphan-volume-ids "$CLEANUP_ORPHAN_VOLUME_IDS")
fi

CURRENT_PHASE="cleanup-plan"
run_or_print "$ROOT/scripts/destroy-linode-staging-resources.sh" "${cleanup_args[@]}"
if [[ "$PLAN_ONLY" -eq 1 && "$LIVE" -eq 0 ]]; then
	exit 0
fi

CURRENT_PHASE="cleanup-apply"
run_or_print "$ROOT/scripts/destroy-linode-staging-resources.sh" "${cleanup_args[@]}" --yes --confirm-text "destroy $STACK_NAME"
CURRENT_PHASE="provision"
run_or_print "$ROOT/scripts/provision-staging.sh" --env-root "$ENV_ROOT" --confirm "$STACK_NAME"
CURRENT_PHASE="data-setup"
if [[ "$LIVE" -eq 1 ]]; then
	run_data_setup
else
	data_setup_plan_args=(
		--env-root "$ENV_ROOT"
		--brandname "$BRANDNAME"
		--user-count "$USERS"
		--device-count "$TARGET_DEVICES"
		--device-mix "$DEVICE_MIX"
		--device-prefix "$RUN_ID-device"
		--user-concurrency "$USER_CONCURRENCY"
		--device-concurrency "$DEVICE_CONCURRENCY"
		--bind-concurrency "$BIND_CONCURRENCY"
		--no-resume
		--out-dir "$DATA_DIR"
	)
	if [[ -n "$BRAND_PLAN" ]]; then
		data_setup_plan_args+=(--brand-plan "$BRAND_PLAN")
		if [[ "$LIVE" -eq 1 ]]; then
			data_setup_plan_args+=(--load-run-id "$RUN_ID" --load-target "$((TARGET_DEVICES / 1000))K" --email-activate-owners)
		fi
	fi
	run_or_print env \
		CLOUD_STAGING_E2E_FACTORY_ENROLL_PORTS="$FACTORY_ENROLL_PORTS" \
		CLOUD_STAGING_E2E_BIND_PROVISION_TIMEOUT="$BIND_PROVISION_TIMEOUT" \
		"$ROOT/scripts/setup-staging-e2e-data.sh" \
		"${data_setup_plan_args[@]}"
fi

CURRENT_PHASE="workflow-live"
(
	cd "$ROOT/loadtests/home-100k"
	set +e
	run_or_print_logged "$RUN_LOG_DIR/workflow-live.log" env \
		HOME100K_ENV_ROOT="$ENV_ROOT" \
		HOME100K_BRAND_PLAN="$BRAND_PLAN" \
		HOME100K_DEVICES="$TARGET_DEVICES" \
		HOME100K_DEVICES_PER_USER="$DEVICES_PER_USER" \
		HOME100K_LOAD_GENERATOR_DEVICES_PER_VM="$LOAD_GENERATOR_DEVICES_PER_VM" \
		HOME100K_STAGE_WARM_UP="$STAGE_WARM_UP" \
		HOME100K_STAGE_STEADY="$STAGE_STEADY" \
		HOME100K_STAGE_COOL_DOWN="$STAGE_COOL_DOWN" \
		HOME100K_LIVE_RUNNER_TIMEOUT_GRACE="$LIVE_RUNNER_TIMEOUT_GRACE" \
		HOME100K_RUN_ID="$RUN_ID" \
		HOME100K_KUBECONFIG="$ENV_ROOT/state/kubeconfig.yaml" \
		HOME100K_MQTT_ADDR="video-cloud-staging.realtekconnect.com:8883" \
		HOME100K_SSH_KEY="$HOME/.ssh/id_ed25519_rtkcloud" \
		HOME100K_AUTHORIZED_KEY_FILE="$HOME/.ssh/id_ed25519_rtkcloud.pub" \
		HOME100K_STATUS_INTERVAL_SECONDS=30 \
		./scripts/home-100k.sh workflow-live
	workflow_status=$?
	set -e
	if [[ "$workflow_status" -ne 0 ]]; then
		printf '%s\n' "$workflow_status" > "$WORKFLOW_STATUS_FILE"
		printf '[capacity] workflow-live failed with rc=%s; preserving load-generator VMs for resume/debug\n' "$workflow_status" >&2
	elif report_success; then
		run_or_print_logged "$RUN_LOG_DIR/destroy-vms.log" env \
			HOME100K_ENV_ROOT="$ENV_ROOT" \
			HOME100K_DEVICES="$TARGET_DEVICES" \
			HOME100K_RUN_ID="$RUN_ID" \
			HOME100K_SSH_KEY="$HOME/.ssh/id_ed25519_rtkcloud" \
			HOME100K_AUTHORIZED_KEY_FILE="$HOME/.ssh/id_ed25519_rtkcloud.pub" \
			./scripts/home-100k.sh destroy-vms --live --confirm-live
	else
		printf '[capacity] workflow-live returned zero but report is not COMPLETE/SUCCESS; preserving load-generator VMs for investigation\n' >&2
	fi
)

CURRENT_PHASE="final-cleanup-plan"
run_or_print "$ROOT/scripts/destroy-linode-staging-resources.sh" --env-root "$ENV_ROOT"
if [[ "$LIVE" -eq 1 && ! -f "$REPORT_DIR/results.json" ]]; then
	CURRENT_PHASE="report-collection"
	die "missing $REPORT_DIR/results.json; cannot write capacity summary"
fi
(
	cd "$ROOT/scripts/go"
	CURRENT_PHASE="capacity-summary"
	run_or_print go run ./rtk-cloud -- lke-capacity-run-summary \
		--env-root "$ENV_ROOT" \
		--run-dir "$REPORT_DIR" \
		--target-devices "$TARGET_DEVICES" \
		--mqtt-pods "$MQTT_PODS" \
		--node-count "$NODE_COUNT" \
		--node-type "$NODE_TYPE" \
		--out "$SUMMARY_FILE"
)

printf '[capacity] summary=%s\n' "$SUMMARY_FILE"
if [[ -f "$WORKFLOW_STATUS_FILE" ]]; then
	CURRENT_PHASE="workflow-live"
	exit "$(cat "$WORKFLOW_STATUS_FILE")"
fi
