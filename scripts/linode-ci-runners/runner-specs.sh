#!/usr/bin/env bash
# shellcheck shell=bash

load_runner_specs() {
  local profile="${CI_RUNNER_PROFILE:-dedicated}"
  case "$profile" in
    dedicated)
      RUNNER_SPECS=(
        "rtk-ci-account-manager|rtk-ci-account-manager|hkt999rtk/rtk_account_manager|g6-standard-2|account-manager-ci"
        "rtk-ci-cloud-admin|rtk-ci-cloud-admin|hkt999rtk/rtk_cloud_admin|g6-standard-2|rtk-cloud-admin-ci"
        "rtk-ci-video-cloud|rtk-ci-video-cloud|hkt999rtk/rtk_video_cloud|g6-standard-4|video-cloud-ci"
      )
      ;;
    shared-platform)
      RUNNER_SPECS=(
        "rtk-ci-platform|rtk-ci-platform-account-manager|hkt999rtk/rtk_account_manager|g6-standard-4|account-manager-ci"
        "rtk-ci-platform|rtk-ci-platform-cloud-admin|hkt999rtk/rtk_cloud_admin|g6-standard-4|rtk-cloud-admin-ci"
        "rtk-ci-video-cloud|rtk-ci-video-cloud|hkt999rtk/rtk_video_cloud|g6-standard-4|video-cloud-ci"
      )
      ;;
    shared-all)
      RUNNER_SPECS=(
        "rtk-ci-shared|rtk-ci-shared-account-manager|hkt999rtk/rtk_account_manager|g6-standard-8|account-manager-ci"
        "rtk-ci-shared|rtk-ci-shared-cloud-admin|hkt999rtk/rtk_cloud_admin|g6-standard-8|rtk-cloud-admin-ci"
        "rtk-ci-shared|rtk-ci-shared-video-cloud|hkt999rtk/rtk_video_cloud|g6-standard-8|video-cloud-ci"
      )
      ;;
    *)
      printf 'error: unsupported CI_RUNNER_PROFILE: %s\n' "$profile" >&2
      return 2
      ;;
  esac
}
