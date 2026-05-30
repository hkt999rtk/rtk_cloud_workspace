#!/usr/bin/env bash
# shellcheck shell=bash

load_runner_specs() {
  if [[ -n "${CI_RUNNER_PROFILE:-}" && "${CI_RUNNER_PROFILE:-}" != "shared-linux" ]]; then
    printf 'error: only shared-linux CI runner profile is supported\n' >&2
    return 2
  fi

  RUNNER_SPECS=(
    "rtk-shared-linux-ci|rtk-ci-account-manager|hkt999rtk/rtk_account_manager|g6-standard-4|account-manager-ci"
    "rtk-shared-linux-ci|rtk-ci-cloud-admin|hkt999rtk/rtk_cloud_admin|g6-standard-4|rtk-cloud-admin-ci"
    "rtk-shared-linux-ci|rtk-ci-cloud-frontend|hkt999rtk/rtk_cloud_frontend|g6-standard-4|rtk_cloud_frontend,go"
    "rtk-shared-linux-ci|rtk-ci-cloud-client-linux|hkt999rtk/rtk_cloud_client|g6-standard-4|client-sdk-ci"
    "rtk-shared-linux-ci|rtk-ci-cloud-logger|hkt999rtk/rtk_cloud_logger|g6-standard-4|rtk-cloud-logger-ci"
  )
}
