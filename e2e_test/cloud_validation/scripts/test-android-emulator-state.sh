#!/usr/bin/env bash
set -euo pipefail

workspace_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/sdk-cloud-validation-android-state.XXXXXX")"
cleanup() {
  rm -rf "$test_root"
}
trap cleanup EXIT

scripts_dir="$test_root/workspace/e2e_test/cloud_validation/scripts"
runner_dir="$test_root/workspace/repos/rtk_cloud_client/tools"
mkdir -p "$scripts_dir" "$runner_dir" "$test_root/output"
cp "$workspace_root/e2e_test/cloud_validation/scripts/run-android.sh" "$scripts_dir/run-android.sh"

parser_dir="$test_root/parser"
mkdir -p "$parser_dir/bin" "$parser_dir/output"
cp "$workspace_root/e2e_test/cloud_validation/scripts/start-android-emulator.sh" "$parser_dir/start-android-emulator.sh"
cat > "$parser_dir/bin/adb" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "devices" ]]; then
  printf 'List of devices attached\nemulator-test\tdevice\n'
elif [[ "$*" == *"getprop ro.kernel.qemu"* || "$*" == *"getprop sys.boot_completed"* ]]; then
  printf '1\n'
elif [[ "$*" == *"dumpsys connectivity"* ]]; then
  printf 'Capabilities: INTERNET\nTransportInfo: test\nValidation: VALIDATED\n'
else
  exit 1
fi
EOF
chmod +x "$parser_dir/bin/adb" "$parser_dir/start-android-emulator.sh"
env -u ANDROID_HOME -u ANDROID_SDK_ROOT PATH="$parser_dir/bin:$PATH" \
  CLOUD_VALIDATION_ANDROID_EMULATOR_STATE="$parser_dir/output/state.json" \
  CLOUD_VALIDATION_OUT_DIR="$parser_dir/output" \
  "$parser_dir/start-android-emulator.sh"
jq -e '.serial == "emulator-test" and .owned == false' "$parser_dir/output/state.json" >/dev/null

cat > "$scripts_dir/start-android-emulator.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: "${CLOUD_VALIDATION_ANDROID_EMULATOR_STATE:?state path must be exported before start}"
mkdir -p "$(dirname "$CLOUD_VALIDATION_ANDROID_EMULATOR_STATE")"
printf '{"schema_version":1,"serial":"emulator-test","owned":true}\n' > "$CLOUD_VALIDATION_ANDROID_EMULATOR_STATE"
EOF

cat > "$scripts_dir/stop-android-emulator.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: "${CLOUD_VALIDATION_ANDROID_EMULATOR_STATE:?state path must remain exported for cleanup}"
test -f "$CLOUD_VALIDATION_ANDROID_EMULATOR_STATE"
printf '%s\n' "$CLOUD_VALIDATION_ANDROID_EMULATOR_STATE" > "${CLOUD_VALIDATION_OUT_DIR}/stop-state-path.txt"
rm -f "$CLOUD_VALIDATION_ANDROID_EMULATOR_STATE"
EOF

cat > "$runner_dir/run_android_sample_cloud_validation.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
: "${CLOUD_VALIDATION_ANDROID_EMULATOR_STATE:?state path must be exported to the Android runner}"
test -f "$CLOUD_VALIDATION_ANDROID_EMULATOR_STATE"
printf '%s\n' "$CLOUD_VALIDATION_ANDROID_EMULATOR_STATE" > "${CLOUD_VALIDATION_OUT_DIR}/runner-state-path.txt"
EOF

chmod +x "$scripts_dir"/*.sh "$runner_dir/run_android_sample_cloud_validation.sh"
CLOUD_VALIDATION_OUT_DIR="$test_root/output" \
  CLOUD_VALIDATION_ENABLE_CLOUD_COMMAND_TRIGGER=0 \
  "$scripts_dir/run-android.sh"

expected="$test_root/output/android-emulator-state.json"
test "$(cat "$test_root/output/runner-state-path.txt")" = "$expected"
test "$(cat "$test_root/output/stop-state-path.txt")" = "$expected"
test ! -e "$expected"
printf 'android emulator state coordination: PASS\n'
