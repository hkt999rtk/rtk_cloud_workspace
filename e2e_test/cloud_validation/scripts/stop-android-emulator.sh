#!/usr/bin/env bash
set -euo pipefail

state_file="${CLOUD_VALIDATION_ANDROID_EMULATOR_STATE:-${RUNNER_TEMP:-/tmp}/sdk-cloud-validation-android-emulator.json}"
if [[ ! -f "$state_file" ]] || ! jq -e '.owned == true' "$state_file" >/dev/null; then
  exit 0
fi
serial="$(jq -r '.serial' "$state_file")"
pid="$(jq -r '.pid' "$state_file")"
adb -s "$serial" emu kill >/dev/null 2>&1 || true
kill "$pid" >/dev/null 2>&1 || true
for _ in $(seq 1 30); do
  if ! adb devices 2>/dev/null | awk -v serial="$serial" 'NR > 1 && $1 == serial && $2 == "device" { found=1 } END { exit !found }'; then
    break
  fi
  sleep 1
done
if adb devices 2>/dev/null | awk -v serial="$serial" 'NR > 1 && $1 == serial && $2 == "device" { found=1 } END { exit !found }'; then
  echo "Android Emulator $serial did not stop" >&2
  exit 1
fi
rm -rf "$(jq -r '.avd_home' "$state_file")" "$(jq -r '.emulator_home' "$state_file")"
rm -f "$state_file"
