#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/scripts/linode-ci-runners/runner-specs.sh"
load_runner_specs

if [[ "${#RUNNER_SPECS[@]}" -ne 5 ]]; then
  printf 'expected 5 shared Linux CI runner specs, got %s\n' "${#RUNNER_SPECS[@]}" >&2
  exit 1
fi

expected_host="rtk-shared-linux-ci"
expected_type="g6-standard-4"
expected_specs=(
  "$expected_host|rtk-ci-account-manager|hkt999rtk/rtk_account_manager|$expected_type|account-manager-ci"
  "$expected_host|rtk-ci-cloud-admin|hkt999rtk/rtk_cloud_admin|$expected_type|rtk-cloud-admin-ci"
  "$expected_host|rtk-ci-cloud-frontend|hkt999rtk/rtk_cloud_frontend|$expected_type|rtk_cloud_frontend,go"
  "$expected_host|rtk-ci-cloud-client-linux|hkt999rtk/rtk_cloud_client|$expected_type|client-sdk-ci"
  "$expected_host|rtk-ci-cloud-logger|hkt999rtk/rtk_cloud_logger|$expected_type|rtk-cloud-logger-ci"
)

for i in "${!expected_specs[@]}"; do
  if [[ "${RUNNER_SPECS[$i]}" != "${expected_specs[$i]}" ]]; then
    printf 'spec %s mismatch\nexpected: %s\nactual:   %s\n' "$i" "${expected_specs[$i]}" "${RUNNER_SPECS[$i]}" >&2
    exit 1
  fi
done
