#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

ENV_ROOT="$TMP/cloud_env/staging/lke"
mkdir -p "$ENV_ROOT/env"
cat > "$ENV_ROOT/env/stack.env" <<'EOF_ENV'
CLOUD_PROVIDER=lke
CLOUD_STACK_NAME=video-cloud-staging
EOF_ENV

"$ROOT/scripts/run-lke-capacity-experiment.sh" \
	--env-root "$ENV_ROOT" \
	--target-devices 10000 \
	--mqtt-pods 1 \
	--node-count 2 \
	--node-type g6-standard-2 \
	--cleanup-orphan-volume-ids 123,456 \
	--run-id cap-test \
	--plan > "$TMP/plan.out"

grep -F '[capacity] run_id=cap-test target_devices=10000 users=500 mqtt_pods=1 node_count=2 node_type=g6-standard-2 load_generator_vms=1' "$TMP/plan.out" >/dev/null
grep -F 'dry-run only; pass --live --confirm video-cloud-staging' "$TMP/plan.out" >/dev/null
grep -F 'destroy-linode-staging-resources.sh' "$TMP/plan.out" >/dev/null
grep -F -- '--include-orphan-volumes' "$TMP/plan.out" >/dev/null
grep -F -- '--orphan-volume-ids' "$TMP/plan.out" >/dev/null

jq -e '
  .schema == "rtk-cloud-workspace.lke-capacity-experiment-request/v1" and
  .target_devices == 10000 and
  .users == 500 and
  .mqtt_pods == 1 and
  .mqtt_pods_source == "override" and
  .node_count == 2 and
  .node_count_source == "override" and
  .node_type == "g6-standard-2" and
  .formula.load_generator_vms == "ceil(target_devices / load_generator_devices_per_vm)" and
  .formula.mqtt_pods == "ceil(target_devices / mqtt_connections_per_pod)" and
  .mqtt_request_cpu == "" and
  .mqtt_request_memory == "" and
  .mqtt_limit_memory == "" and
  .emqx_force_shutdown_max_heap_size == "" and
  .cloud_logger_request_memory == "" and
  .cloud_logger_limit_memory == "" and
  .load_generator_vms == 1 and
  .live_runner_timeout_grace == "15m" and
  .user_concurrency == 16 and
  .device_concurrency == 16 and
  .bind_concurrency == 64 and
  .data_setup_retries == 3 and
  .factory_enroll_ports == "18443,18444,18445,18446" and
  .bind_provision_timeout == "60m" and
  .cleanup_orphan_volume_ids == "123,456"
' "$ENV_ROOT/artifacts/capacity-experiments/cap-test/request.json" >/dev/null

"$ROOT/scripts/run-lke-capacity-experiment.sh" \
	--env-root "$ENV_ROOT" \
	--target-devices 100000 \
	--node-type g6-standard-4 \
	--load-generator-devices-per-vm 20000 \
	--mqtt-connections-per-pod 10000 \
	--run-id cap-derived \
	--plan > "$TMP/derived.out"

grep -F '[capacity] run_id=cap-derived target_devices=100000 users=5000 mqtt_pods=10 node_count=10 node_type=g6-standard-4 load_generator_vms=5' "$TMP/derived.out" >/dev/null

jq -e '
  .target_devices == 100000 and
  .users == 5000 and
  .load_generator_devices_per_vm == 20000 and
  .load_generator_vms == 5 and
  .mqtt_connections_per_pod == 10000 and
  .mqtt_pods == 10 and
  .mqtt_pods_source == "formula" and
  .node_count == 10 and
  .node_count_source == "formula_spread_min" and
  .formula.node_count == "max(cpu_nodes, memory_nodes, mqtt_pods, spread_min); wrapper default uses mqtt_pods as spread_min when --node-count is omitted"
' "$ENV_ROOT/artifacts/capacity-experiments/cap-derived/request.json" >/dev/null

FAKE_ROOT="$TMP/fake-root"
mkdir -p "$FAKE_ROOT/scripts" "$FAKE_ROOT/loadtests/home-100k/scripts" "$FAKE_ROOT/cloud_env/staging/lke/env"
cp "$ROOT/scripts/run-lke-capacity-experiment.sh" "$FAKE_ROOT/scripts/run-lke-capacity-experiment.sh"
chmod +x "$FAKE_ROOT/scripts/run-lke-capacity-experiment.sh"

cat > "$FAKE_ROOT/cloud_env/staging/lke/env/stack.env" <<'EOF_ENV'
CLOUD_PROVIDER=lke
CLOUD_STACK_NAME=video-cloud-staging
EOF_ENV

cat > "$FAKE_ROOT/scripts/destroy-linode-staging-resources.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
echo "fake destroy staging $*"
EOF_SH
chmod +x "$FAKE_ROOT/scripts/destroy-linode-staging-resources.sh"

cat > "$FAKE_ROOT/scripts/provision-staging.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
echo "fake provision $*"
EOF_SH
chmod +x "$FAKE_ROOT/scripts/provision-staging.sh"

cat > "$FAKE_ROOT/scripts/setup-staging-e2e-data.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
out_dir=""
while [[ $# -gt 0 ]]; do
	case "$1" in
		--out-dir) out_dir="$2"; shift 2 ;;
		*) shift ;;
	esac
done
mkdir -p "$out_dir/logs"
echo "fake data setup"
EOF_SH
chmod +x "$FAKE_ROOT/scripts/setup-staging-e2e-data.sh"

cat > "$FAKE_ROOT/loadtests/home-100k/scripts/home-100k.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
run_id="${HOME100K_RUN_ID:-fake-run}"
case "${1:-}" in
	workflow-live)
		mkdir -p "reports/$run_id"
		printf '{"vms":[{"id":1,"label":"lg01"}]}\n' > "reports/$run_id/vms.json"
		echo "fake workflow-live before SIGPIPE"
		exit 141
		;;
	destroy-vms)
		echo "destroy-vms called" >> "../../destroy-vms.called"
		;;
	*)
		echo "fake home-100k $*"
		;;
esac
EOF_SH
chmod +x "$FAKE_ROOT/loadtests/home-100k/scripts/home-100k.sh"

set +e
"$FAKE_ROOT/scripts/run-lke-capacity-experiment.sh" \
	--env-root "$FAKE_ROOT/cloud_env/staging/lke" \
	--target-devices 1000 \
	--mqtt-pods 1 \
	--node-count 1 \
	--node-type g6-standard-2 \
	--run-id cap-workflow-141 \
	--live \
	--confirm video-cloud-staging > "$TMP/workflow-141.out" 2> "$TMP/workflow-141.err"
rc=$?
set -e

if [[ "$rc" -eq 0 ]]; then
	echo "expected fake workflow-live failure" >&2
	exit 1
fi
if [[ -f "$FAKE_ROOT/loadtests/destroy-vms.called" ]]; then
	echo "destroy-vms must not run after workflow-live failure" >&2
	exit 1
fi

CAP_DIR="$FAKE_ROOT/cloud_env/staging/lke/artifacts/capacity-experiments/cap-workflow-141"
grep -F 'fake workflow-live before SIGPIPE' "$CAP_DIR/logs/workflow-live.log" >/dev/null
grep -F '141' "$CAP_DIR/workflow-live.exit" >/dev/null
jq -e '
  .classification == "report_collection_failed" and
  (.workflow_log | endswith("/logs/workflow-live.log")) and
  (.workflow_exit_file | endswith("/workflow-live.exit"))
' "$CAP_DIR/aborted-summary.json" >/dev/null
grep -F 'workflow-live failed with rc=141; preserving load-generator VMs' "$TMP/workflow-141.err" >/dev/null

FAKE_RETRY_ROOT="$TMP/fake-retry-root"
mkdir -p "$FAKE_RETRY_ROOT/scripts/go" "$FAKE_RETRY_ROOT/fakebin" "$FAKE_RETRY_ROOT/loadtests/home-100k/scripts" "$FAKE_RETRY_ROOT/cloud_env/staging/lke/env"
cp "$ROOT/scripts/run-lke-capacity-experiment.sh" "$FAKE_RETRY_ROOT/scripts/run-lke-capacity-experiment.sh"
chmod +x "$FAKE_RETRY_ROOT/scripts/run-lke-capacity-experiment.sh"

cat > "$FAKE_RETRY_ROOT/cloud_env/staging/lke/env/stack.env" <<'EOF_ENV'
CLOUD_PROVIDER=lke
CLOUD_STACK_NAME=video-cloud-staging
EOF_ENV

cat > "$FAKE_RETRY_ROOT/scripts/destroy-linode-staging-resources.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
echo "fake destroy staging $*"
EOF_SH
chmod +x "$FAKE_RETRY_ROOT/scripts/destroy-linode-staging-resources.sh"

cat > "$FAKE_RETRY_ROOT/scripts/provision-staging.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
echo "fake provision $*"
EOF_SH
chmod +x "$FAKE_RETRY_ROOT/scripts/provision-staging.sh"

cat > "$FAKE_RETRY_ROOT/scripts/setup-staging-e2e-data.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
attempt_file="$root/setup-attempts"
attempt=1
if [[ -f "$attempt_file" ]]; then
	attempt=$(( $(cat "$attempt_file") + 1 ))
fi
printf '%s\n' "$attempt" > "$attempt_file"
printf 'attempt=%s args=%s\n' "$attempt" "$*" >> "$root/setup-args.log"
out_dir=""
while [[ $# -gt 0 ]]; do
	case "$1" in
		--out-dir) out_dir="$2"; shift 2 ;;
		*) shift ;;
	esac
done
mkdir -p "$out_dir/logs"
if [[ "$attempt" -eq 1 ]]; then
	{
		echo "[cloud-bind-devices 00:00:01 +000s] bind progress: done=42/1000 bulk_created=42 provision_started=42 skipped=0"
		echo "error: HTTP request failed for http://127.0.0.1:18081/v1/orgs/example/devices/example/provision: dial tcp 127.0.0.1:18081: connect: connection refused"
	} > "$out_dir/logs/bind_devices.log"
	exit 1
fi
{
	echo "[cloud-bind-devices 00:00:02 +000s] bind progress: done=1000/1000 bulk_created=1000 provision_started=1000 skipped=0"
	echo '{"overall":"pass","test_data_db":"/tmp/test.sqlite","bind_validation_dir":"/tmp/bind-validation","steps":[]}'
} > "$out_dir/logs/bind_devices.log"
EOF_SH
chmod +x "$FAKE_RETRY_ROOT/scripts/setup-staging-e2e-data.sh"

cat > "$FAKE_RETRY_ROOT/loadtests/home-100k/scripts/home-100k.sh" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
run_id="${HOME100K_RUN_ID:-fake-run}"
case "${1:-}" in
	workflow-live)
		mkdir -p "reports/$run_id"
		printf '{"ok":true}\n' > "reports/$run_id/results.json"
		cat > "reports/$run_id/TEST_REPORT.md" <<'EOF_REPORT'
- Status: COMPLETE
- Result: SUCCESS
EOF_REPORT
		;;
	destroy-vms)
		echo "destroy-vms called"
		;;
	*)
		echo "fake home-100k $*"
		;;
esac
EOF_SH
chmod +x "$FAKE_RETRY_ROOT/loadtests/home-100k/scripts/home-100k.sh"

cat > "$FAKE_RETRY_ROOT/fakebin/go" <<'EOF_SH'
#!/usr/bin/env bash
set -euo pipefail
echo "fake go $*"
EOF_SH
chmod +x "$FAKE_RETRY_ROOT/fakebin/go"

PATH="$FAKE_RETRY_ROOT/fakebin:$PATH" \
LKE_CAPACITY_DATA_SETUP_RETRY_SLEEP_SECONDS=0 \
"$FAKE_RETRY_ROOT/scripts/run-lke-capacity-experiment.sh" \
	--env-root "$FAKE_RETRY_ROOT/cloud_env/staging/lke" \
	--target-devices 1000 \
	--mqtt-pods 1 \
	--node-count 1 \
	--node-type g6-standard-2 \
	--data-setup-retries 2 \
	--run-id cap-data-retry \
	--live \
	--confirm video-cloud-staging > "$TMP/data-retry.out" 2> "$TMP/data-retry.err"

RETRY_CAP_DIR="$FAKE_RETRY_ROOT/cloud_env/staging/lke/artifacts/capacity-experiments/cap-data-retry"
grep -F 'data setup failed with retryable port-forward/network error; retrying with --resume' "$TMP/data-retry.err" >/dev/null
grep -F 'last_progress=' "$TMP/data-retry.err" >/dev/null
grep -F 'attempt=2 args=' "$FAKE_RETRY_ROOT/setup-args.log" | grep -F -- '--resume' | grep -F -- '--from-step bind_devices' >/dev/null
grep -F 'connect: connection refused' "$RETRY_CAP_DIR/data-setup-attempt-1-logs/bind_devices.log" >/dev/null
if [[ -e "$RETRY_CAP_DIR/data-setup-attempt-2-logs/bind_devices.log" ]]; then
	echo "successful retry logs should not be archived as failed attempt logs" >&2
	exit 1
fi
