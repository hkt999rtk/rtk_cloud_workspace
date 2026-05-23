#!/usr/bin/env bash
# shellcheck shell=bash

load_runner_specs() {
  if [[ -n "${CI_RUNNER_PROFILE:-}" && "${CI_RUNNER_PROFILE:-}" != "dedicated" ]]; then
    printf 'error: shared CI runner profiles are not supported; use dedicated VMs\n' >&2
    return 2
  fi

  RUNNER_SPECS=(
    "rtk-ci-account-manager|rtk-ci-account-manager|hkt999rtk/rtk_account_manager|g6-standard-2|account-manager-ci"
    "rtk-ci-cloud-admin|rtk-ci-cloud-admin|hkt999rtk/rtk_cloud_admin|g6-standard-2|rtk-cloud-admin-ci"
    "rtk-ci-video-cloud|rtk-ci-video-cloud|hkt999rtk/rtk_video_cloud|g6-standard-4|video-cloud-ci"
  )
}
