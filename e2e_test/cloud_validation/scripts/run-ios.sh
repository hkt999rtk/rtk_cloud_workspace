#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workspace_root="$(cd "$script_dir/../../.." && pwd)"

if [[ "${CLOUD_VALIDATION_RUN_XCUITEST:-1}" == "1" ]]; then
  "$workspace_root/repos/rtk_cloud_client/tools/run_ios_sample_ui_tests.sh"
fi

trigger_pid=""
offline_pid=""
cleanup_trigger() {
  if [[ -n "$trigger_pid" ]]; then
    kill "$trigger_pid" >/dev/null 2>&1 || true
    wait "$trigger_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$offline_pid" ]]; then
    kill "$offline_pid" >/dev/null 2>&1 || true
    wait "$offline_pid" >/dev/null 2>&1 || true
  fi
}
trap cleanup_trigger EXIT
if [[ "${CLOUD_VALIDATION_ENABLE_CLOUD_COMMAND_TRIGGER:-1}" == "1" ]]; then
  "$script_dir/run-cloud-command-trigger.sh" &
  trigger_pid=$!
fi
if [[ "${CLOUD_VALIDATION_PROFILE:-deploy}" == "nightly" ]]; then
  "$script_dir/run-offline-reconnect-controller.sh" &
  offline_pid=$!
fi

"$workspace_root/repos/rtk_cloud_client/tools/run_ios_sample_simulator.sh" cloud-validation
if [[ -n "$offline_pid" ]]; then
  wait "$offline_pid"
  offline_pid=""
fi
