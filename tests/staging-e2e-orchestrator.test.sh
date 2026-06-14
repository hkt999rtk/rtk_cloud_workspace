#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
mkdir -p "$WORKSPACE" "$ENV_ROOT/env" "$ENV_ROOT/artifacts/users" "$ENV_ROOT/artifacts/device-bind" "$ENV_ROOT/devices/test_device/manifests"

cat > "$ENV_ROOT/env/stack.env" <<'EOF_ENV'
CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
VIDEO_CLOUD_DOMAIN=video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-staging.realtekconnect.com
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-staging
VIDEO_CLOUD_VPC_LABEL=video-cloud-staging-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-staging-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
EOF_ENV

COMMAND_LOG="$TMP/commands.log"
make_stub() {
	local path="$1"
	local name="$2"
	cat > "$path" <<SH
#!/usr/bin/env bash
set -euo pipefail
printf '%s\\t%s\\n' "$name" "\$*" >> "$COMMAND_LOG"
case "$name" in
create-users)
	mkdir -p "$ENV_ROOT/artifacts/users"
	printf '{"brandname":"RTK","users":[{"email":"rtk+001@users.local","password":"super-secret"}]}\\n' > "$ENV_ROOT/artifacts/users/rtk-users-test.json"
	chmod 600 "$ENV_ROOT/artifacts/users/rtk-users-test.json"
	;;
generate-devices)
	mkdir -p "$ENV_ROOT/devices/test_device/manifests"
	printf '[]\\n' > "$ENV_ROOT/devices/test_device/manifests/devices.json"
	;;
bind-devices)
	mkdir -p "$ENV_ROOT/artifacts/device-bind"
	printf '{"brandname":"RTK","count":1,"assignments":[{"device_id":"dev-1"}]}\\n' > "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"
	;;
validate-bind)
	printf '{"overall":"pass","report_file":"validate-report.md"}\\n'
	;;
setup-data)
	out_dir=""
	while [[ \$# -gt 0 ]]; do
		case "\$1" in
			--out-dir)
				out_dir="\$2"
				shift 2
				;;
			*)
				shift
				;;
		esac
	done
	mkdir -p "\$out_dir/logs" "\$out_dir/bind-validation" "$ENV_ROOT/artifacts/users" "$ENV_ROOT/artifacts/device-bind"
	printf '{"brandname":"RTK","users":[{"email":"rtk+001@users.local"}]}\\n' > "$ENV_ROOT/artifacts/users/rtk-users-test.json"
	printf '{"brandname":"RTK","count":1,"assignments":[{"device_id":"dev-1"}]}\\n' > "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"
	cat > "\$out_dir/summary.json" <<JSON
{
  "overall": "pass",
  "summary_file": "\$out_dir/summary.json",
  "users_file": "$ENV_ROOT/artifacts/users/rtk-users-test.json",
  "device_bind_file": "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json",
  "bind_validation_dir": "\$out_dir/bind-validation",
  "steps": [
    {"name": "create_brand", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/create_brand.log"},
    {"name": "create_users", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/create_users.log"},
    {"name": "create_devices", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/create_devices.log"},
    {"name": "bind_devices", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/bind_devices.log"},
    {"name": "validate_bind", "status": "PASS", "exit_code": 0, "duration_seconds": 1, "log_file": "\$out_dir/logs/validate_bind.log"}
  ]
}
JSON
	printf '{"overall":"pass","summary_file":"%s","users_file":"%s","device_bind_file":"%s"}\\n' "\$out_dir/summary.json" "$ENV_ROOT/artifacts/users/rtk-users-test.json" "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"
	;;
mqtt-test)
	mkdir -p "$TMP/mqtt-report"
	printf '{"overall":"pass","status":"PASS","report_file":"%s","results_file":"%s"}\\n' "$TMP/mqtt-report/TEST_REPORT.md" "$TMP/mqtt-report/results.json"
	printf '# MQTT Report\\nPASS\\n' > "$TMP/mqtt-report/TEST_REPORT.md"
	printf '{"overall":"pass","devices":[{"device_id":"dev-1","runtime_log_stream_id":"mqtt-e2e-dev-1","runtime_log_expectations":[{"seq":1,"source":"device_client","message":"mqtt_e2e telemetry device_client publish"}]}]}\\n' > "$TMP/mqtt-report/results.json"
	;;
mqtt-log-verify)
	out_dir=""
	while [[ \$# -gt 0 ]]; do
		case "\$1" in
			--out-dir)
				out_dir="\$2"
				shift 2
				;;
			*)
				shift
				;;
		esac
	done
	mkdir -p "\$out_dir"
	printf '{"overall":"pass","checked_devices":1,"checked_logs":1}\\n' > "\$out_dir/summary.json"
	printf '{"overall":"pass","summary_file":"%s"}\\n' "\$out_dir/summary.json"
	;;
esac
SH
	chmod +x "$path"
}

make_stub "$TMP/remove-k8s.sh" remove-k8s
make_stub "$TMP/provision-k8s.sh" provision-k8s
make_stub "$TMP/create-brand.sh" create-brand
make_stub "$TMP/create-users.sh" create-users
make_stub "$TMP/generate-devices.sh" generate-devices
make_stub "$TMP/bind-devices.sh" bind-devices
make_stub "$TMP/validate-bind.sh" validate-bind
make_stub "$TMP/setup-data.sh" setup-data
make_stub "$TMP/mqtt-test.sh" mqtt-test
make_stub "$TMP/mqtt-log-verify.sh" mqtt-log-verify

PLAN_OUT="$TMP/plan.out"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--plan > "$PLAN_OUT"

grep -F 'cloud-staging-e2e-test plan' "$PLAN_OUT" >/dev/null
grep -F 'target: k8s' "$PLAN_OUT" >/dev/null
grep -F 'reset K8s staging with '"$TMP/remove-k8s.sh" "$PLAN_OUT" >/dev/null
grep -F 'provision K8s staging with '"$TMP/provision-k8s.sh" "$PLAN_OUT" >/dev/null
test ! -e "$COMMAND_LOG"

RUN_OUT="$TMP/run.out"
RUN_ERR="$TMP/run.err"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
CLOUD_STAGING_E2E_PROGRESS_INTERVAL=100ms \
CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0 \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--run \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 1 \
	--device-count 3 \
	--device-mix camera=1,light=1,smart_meter=1 \
	--skip-mqtt-probe > "$RUN_OUT" 2> "$RUN_ERR"

expected=$'remove-k8s\nprovision-k8s\nsetup-data\nmqtt-test\nmqtt-log-verify'
actual="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual" == "$expected" ]] || {
	printf 'unexpected command order:\n%s\n' "$actual" >&2
	exit 1
}
grep -F $'remove-k8s\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/linode"$' --yes' "$COMMAND_LOG" >/dev/null
grep -F $'provision-k8s\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/linode"$' --confirm video-cloud-staging' "$COMMAND_LOG" >/dev/null
grep -F $'setup-data\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/linode"$' --brandname RTK --user-count 1 --device-count 3 --device-mix camera=1,light=1,smart_meter=1 --device-prefix load-device --user-concurrency 16 --device-concurrency 16 --bind-concurrency 16 --out-dir ' "$COMMAND_LOG" >/dev/null
if grep -E '(^|[[:space:]])(remove-all-vm|provision|deploy|remove_vm|provision_all)([[:space:]]|$)' "$COMMAND_LOG" >/dev/null; then
	echo "staging-e2e-test should not invoke retired VM runtime commands" >&2
	exit 1
fi
for step in reset_k8s provision_k8s setup_brand_devices cloud_mqtt_test verify_mqtt_logs; do
	grep -E "\\[cloud-staging-e2e\\] pass: ${step} duration_seconds=[0-9]+" "$RUN_ERR" >/dev/null
done
grep -E "\\[cloud-staging-e2e\\] start: provision_k8s log=.*/logs/provision_k8s.log" "$RUN_ERR" >/dev/null

SUMMARY="$(jq -r '.summary_file' "$RUN_OUT")"
REPORT="$(jq -r '.report_file' "$RUN_OUT")"
test -f "$SUMMARY"
test -f "$REPORT"
jq -e '.overall == "pass" and .target == "k8s" and (.steps | length == 5) and .artifacts.data_setup_summary_file != "" and .artifacts.bind_validation_dir != "" and .artifacts.mqtt_log_verify_summary_file != ""' "$SUMMARY" >/dev/null
jq -e '.steps[] | select(.name == "setup_brand_devices")' "$SUMMARY" >/dev/null
grep -F 'Staging E2E Test Report' "$REPORT" >/dev/null
grep -F 'Data setup summary' "$REPORT" >/dev/null
grep -F 'cloud_mqtt_test' "$REPORT" >/dev/null
grep -F 'verify_mqtt_logs' "$REPORT" >/dev/null
if grep -R -Ei 'super-secret|password|bearer|token|PRIVATE KEY|-----BEGIN' "$SUMMARY" "$REPORT" >/dev/null; then
	echo "orchestrator reports must be redacted" >&2
	exit 1
fi

: > "$COMMAND_LOG"
QUIET_OUT="$TMP/quiet.out"
QUIET_ERR="$TMP/quiet.err"
CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT="$TMP/remove-k8s.sh" \
CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT="$TMP/provision-k8s.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT="$TMP/mqtt-log-verify.sh" \
CLOUD_STAGING_E2E_PROGRESS_INTERVAL=100ms \
CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0 \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--run \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 1 \
	--device-count 3 \
	--device-mix camera=1,light=1,smart_meter=1 \
	--skip-mqtt-probe \
	--quiet > "$QUIET_OUT" 2> "$QUIET_ERR"

grep -F $'setup-data\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/linode"$' --brandname RTK --user-count 1 --device-count 3 --device-mix camera=1,light=1,smart_meter=1 --device-prefix load-device --user-concurrency 16 --device-concurrency 16 --bind-concurrency 16 --out-dir ' "$COMMAND_LOG" | grep -F -- '--quiet' >/dev/null
grep -E "\\[cloud-staging-e2e\\] start: provision_k8s log=.*/logs/provision_k8s.log" "$QUIET_ERR" >/dev/null
if grep -F '[cloud-staging-e2e] progress:' "$QUIET_ERR" >/dev/null; then
	echo "quiet staging e2e should not print progress lines" >&2
	exit 1
fi

LKE_ENV_ROOT="$WORKSPACE/cloud_env/staging/lke"
mkdir -p "$LKE_ENV_ROOT/env"
cat > "$LKE_ENV_ROOT/env/stack.env" <<'EOF_LKE_ENV'
CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
EOF_LKE_ENV
: > "$COMMAND_LOG"
LKE_OUT="$TMP/lke.out"
LKE_ERR="$TMP/lke.err"
CLOUD_STAGING_E2E_REMOVE_SCRIPT="$TMP/remove.sh" \
CLOUD_STAGING_E2E_PROVISION_SCRIPT="$TMP/provision.sh" \
CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT="$TMP/setup-data.sh" \
CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT="$TMP/mqtt-test.sh" \
	"/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--run \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 1 \
	--device-count 3 \
	--device-mix camera=1,light=1,smart_meter=1 \
	--skip-mqtt-probe > "$LKE_OUT" 2> "$LKE_ERR"

grep -F $'remove\t--workspace '"$WORKSPACE"$' --env-root '"$LKE_ENV_ROOT"$' --yes' "$COMMAND_LOG" >/dev/null
grep -F $'provision\t--workspace '"$WORKSPACE"$' --env-root '"$LKE_ENV_ROOT"$' --all --confirm video-cloud-staging' "$COMMAND_LOG" >/dev/null
if grep -F 'staging certificate cache' "$LKE_ERR" >/dev/null; then
	echo "LKE staging e2e should not require VM certificate caches before remove" >&2
	exit 1
fi

if "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- staging-e2e-test \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--run \
	--confirm wrong-stack >/tmp/should-fail.out 2>/tmp/should-fail.err; then
	echo "expected wrong confirm to fail" >&2
	exit 1
fi
grep -F 'does not match CLOUD_STACK_NAME=video-cloud-staging' /tmp/should-fail.err >/dev/null
