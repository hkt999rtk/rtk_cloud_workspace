#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workspace_root="$(cd "$script_dir/../../.." && pwd)"

# This path is a parent-process contract shared by emulator start, the Android
# sample runner, and emulator cleanup. An export performed by start-android-
# emulator.sh cannot propagate back to this process, so select it here before
# invoking any child script. Keeping the default under the run output also
# prevents parallel/local runs from sharing stale emulator state.
export CLOUD_VALIDATION_ANDROID_EMULATOR_STATE="${CLOUD_VALIDATION_ANDROID_EMULATOR_STATE:-${CLOUD_VALIDATION_OUT_DIR:?CLOUD_VALIDATION_OUT_DIR is required}/android-emulator-state.json}"

if [[ "${CLOUD_VALIDATION_INSTALL_ANDROID_SYSTEM_IMAGE:-0}" == "1" ]]; then
  sdkmanager "platform-tools" "emulator" "platforms;android-35" \
    "build-tools;35.0.0" "system-images;android-35;google_apis;arm64-v8a"
fi

"$script_dir/start-android-emulator.sh"
trigger_pid=""
offline_pid=""
cleanup_emulator() {
  if [[ -n "$trigger_pid" ]]; then
    kill "$trigger_pid" >/dev/null 2>&1 || true
    wait "$trigger_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$offline_pid" ]]; then
    kill "$offline_pid" >/dev/null 2>&1 || true
    wait "$offline_pid" >/dev/null 2>&1 || true
  fi
  "$script_dir/stop-android-emulator.sh"
}
trap cleanup_emulator EXIT

if [[ "${CLOUD_VALIDATION_ENABLE_CLOUD_COMMAND_TRIGGER:-1}" == "1" ]]; then
  "$script_dir/run-cloud-command-trigger.sh" &
  trigger_pid=$!
fi
if [[ "${CLOUD_VALIDATION_PROFILE:-deploy}" == "nightly" ]]; then
  "$script_dir/run-offline-reconnect-controller.sh" &
  offline_pid=$!
fi

"$workspace_root/repos/rtk_cloud_client/tools/run_android_sample_cloud_validation.sh"
if [[ -n "$offline_pid" ]]; then
  wait "$offline_pid"
  offline_pid=""
fi
