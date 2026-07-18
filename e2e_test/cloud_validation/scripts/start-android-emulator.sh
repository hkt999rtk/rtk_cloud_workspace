#!/usr/bin/env bash
set -euo pipefail

state_file="${CLOUD_VALIDATION_ANDROID_EMULATOR_STATE:-${RUNNER_TEMP:-/tmp}/sdk-cloud-validation-android-emulator.json}"
android_root="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}"
if [[ -n "$android_root" ]]; then
  export PATH="$android_root/platform-tools:$android_root/emulator:$android_root/cmdline-tools/latest/bin:$PATH"
fi

find_emulator() {
  local serial
  while read -r serial; do
    if [[ "$(adb -s "$serial" shell getprop ro.kernel.qemu 2>/dev/null | tr -d '\r')" == "1" ]]; then
      printf '%s\n' "$serial"
      return 0
    fi
  done < <(adb devices 2>/dev/null | awk 'NR > 1 && $2 == "device" { print $1 }')
  return 1
}

network_validated() {
  local serial="$1"
  local connectivity
  connectivity="$(adb -s "$serial" shell dumpsys connectivity 2>/dev/null | tr -d '\r')"
  grep -q 'INTERNET' <<<"$connectivity" && grep -q 'VALIDATED' <<<"$connectivity"
}

wait_for_network() {
  local serial="$1"
  for _ in $(seq 1 60); do
    if network_validated "$serial"; then
      return 0
    fi
    sleep 2
  done
  echo "Android Emulator $serial booted but did not obtain a validated Internet network" >&2
  adb -s "$serial" shell dumpsys connectivity > \
    "${CLOUD_VALIDATION_OUT_DIR:-${RUNNER_TEMP:-/tmp}}/android-connectivity-timeout.log" 2>&1 || true
  return 1
}

online_device="$(find_emulator || true)"
if [[ -n "$online_device" && "$(adb -s "$online_device" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" == "1" ]]; then
  wait_for_network "$online_device"
  jq -n --arg serial "$online_device" '{schema_version:1,serial:$serial,owned:false}' > "$state_file"
  exit 0
fi

avd_home="${RUNNER_TEMP:-/tmp}/sdk-cloud-validation-avd-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
emulator_home="${RUNNER_TEMP:-/tmp}/sdk-cloud-validation-emulator-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
avd_name="sdk-cloud-validation-api35-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}"
mkdir -p "$avd_home" "$emulator_home" "$(dirname "$state_file")"
export ANDROID_AVD_HOME="$avd_home"
export ANDROID_EMULATOR_HOME="$emulator_home"
emulator_pid=""
cleanup_failed_start=1
cleanup_start_failure() {
  if [[ "$cleanup_failed_start" == "1" ]]; then
    [[ -n "$emulator_pid" ]] && kill "$emulator_pid" >/dev/null 2>&1 || true
    rm -rf "$avd_home" "$emulator_home"
    rm -f "$state_file"
  fi
}
trap cleanup_start_failure EXIT

printf 'no\n' | avdmanager create avd --force \
  --name "$avd_name" \
  --package "system-images;android-35;google_apis;arm64-v8a" \
  --device pixel_6 >/dev/null
log_file="${CLOUD_VALIDATION_OUT_DIR:-${RUNNER_TEMP:-/tmp}}/android-emulator-start.log"
mkdir -p "$(dirname "$log_file")"
nohup emulator -avd "$avd_name" -no-window -no-audio -no-boot-anim \
  -no-snapshot -wipe-data -gpu swiftshader_indirect >"$log_file" 2>&1 &
emulator_pid=$!

for _ in $(seq 1 120); do
  online_device="$(find_emulator || true)"
  if [[ -n "$online_device" && "$(adb -s "$online_device" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" == "1" ]]; then
    if ! wait_for_network "$online_device"; then
      exit 1
    fi
    jq -n \
      --arg serial "$online_device" \
      --arg pid "$emulator_pid" \
      --arg avd_home "$avd_home" \
      --arg emulator_home "$emulator_home" \
      '{schema_version:1,serial:$serial,pid:($pid|tonumber),avd_home:$avd_home,emulator_home:$emulator_home,owned:true}' \
      > "$state_file"
    cleanup_failed_start=0
    exit 0
  fi
  if ! kill -0 "$emulator_pid" >/dev/null 2>&1; then
    echo "Android Emulator exited before boot; see $log_file" >&2
    exit 1
  fi
  sleep 2
done

echo "Android Emulator did not finish booting; see $log_file" >&2
kill "$emulator_pid" >/dev/null 2>&1 || true
exit 1
