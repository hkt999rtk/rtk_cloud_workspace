#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
workspace_root="$(cd "$script_dir/../../.." && pwd)"
environment="staging"
platform=""
mode="source"
profile="deploy"
run_id=""
plan_only=0
scenario_files=""
runtime_bundle_override="${CLOUD_VALIDATION_RUNTIME_BUNDLE:-}"
artifact_override="${CLOUD_VALIDATION_ARTIFACT:-}"
artifact_sha_override="${CLOUD_VALIDATION_ARTIFACT_SHA256:-}"

usage() {
  echo "usage: $0 --platform ios|android|all [--environment staging] [--mode source|package] [--profile deploy|nightly|release] [--run-id ID] [--scenarios FILES] [--plan-only]" >&2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --environment) environment="$2"; shift 2 ;;
    --platform) platform="$2"; shift 2 ;;
    --mode) mode="$2"; shift 2 ;;
    --profile) profile="$2"; shift 2 ;;
    --run-id) run_id="$2"; shift 2 ;;
    --scenarios) scenario_files="$2"; shift 2 ;;
    --plan-only) plan_only=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

if [[ "$platform" != "ios" && "$platform" != "android" && "$platform" != "all" ]]; then
  usage
  exit 2
fi
if [[ "$mode" != "source" && "$mode" != "package" ]]; then
  usage
  exit 2
fi
if [[ "$profile" != "deploy" && "$profile" != "nightly" && "$profile" != "release" ]]; then
  usage
  exit 2
fi
if [[ "$profile" == "release" && "$mode" != "package" ]]; then
  echo "release profile requires --mode package" >&2
  exit 2
fi
if [[ -z "$scenario_files" && "$profile" == "nightly" ]]; then
  scenario_files="$workspace_root/e2e_test/cloud_validation/scenarios/core-smoke.yaml,$workspace_root/e2e_test/cloud_validation/scenarios/nightly-resilience.yaml,$workspace_root/e2e_test/cloud_validation/scenarios/shadow-offline-nightly.yaml"
fi

runner_bin="${CLOUD_VALIDATION_RUNNER_BIN:-}"
runner_bin_owned=0
if [[ -z "$runner_bin" ]]; then
  runner_bin="$(mktemp "${TMPDIR:-/tmp}/sdk-cloud-validation-runner.XXXXXX")"
  runner_bin_owned=1
  (cd "$workspace_root/e2e_test" && GOWORK=off go build -o "$runner_bin" ./cloud_validation/cmd/cloud-validation)
elif [[ ! -x "$runner_bin" ]]; then
  echo "CLOUD_VALIDATION_RUNNER_BIN is not executable: $runner_bin" >&2
  exit 2
fi
cleanup_runner() {
  if [[ "$runner_bin_owned" == "1" ]]; then
    rm -f -- "$runner_bin"
  fi
}
trap cleanup_runner EXIT

load_local_deployment_credentials() {
  local env_root="${CLOUD_VALIDATION_ENV_ROOT:-}"
  local video_auth_file logger_token_file video_secret
  if [[ "${CLOUD_VALIDATION_DISABLE_LOCAL_CREDENTIAL_DISCOVERY:-0}" == "1" || -z "$env_root" || ! -d "$env_root" ]]; then
    return
  fi
  env_root="$(cd "$env_root" && pwd -P)"

  local provider=""
  if [[ -f "$env_root/env/stack.env" ]]; then
    provider="$(awk -F= '$1 == "CLOUD_PROVIDER" {print $2; exit}' "$env_root/env/stack.env")"
  fi

  if [[ -z "${CLOUD_VALIDATION_PLATFORM_ADMIN_TOKEN:-}" ]]; then
    CLOUD_VALIDATION_PLATFORM_ADMIN_TOKEN="$({
      cd "$workspace_root/scripts/go"
      ACCOUNT_MANAGER_BASE_URL="${CLOUD_VALIDATION_ACCOUNT_MANAGER_URL:-}" \
        GOWORK=off go run ./rtk-cloud -- platform-admin-token --workspace "$workspace_root" --env-root "$env_root"
    })"
    export CLOUD_VALIDATION_PLATFORM_ADMIN_TOKEN
  fi

  video_auth_file="$env_root/state/secrets/video-auth"
  if [[ -z "${CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN:-}" && "$provider" == "lke" ]]; then
    CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN="$({
      cd "$workspace_root/scripts/go"
      GOWORK=off go run ./rtk-cloud -- video-cloud-admin-token --workspace "$workspace_root" --env-root "$env_root" --ttl 30m
    })"
    export CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN
  elif [[ -z "${CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN:-}" && -s "$video_auth_file" ]]; then
    video_secret="$(<"$video_auth_file")"
    CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN="$({
      cd "$workspace_root/repos/rtk_video_cloud"
      VIDEO_CLOUD_AUTH_SECRET="$video_secret" GOWORK=off go run ./cmd/admin-token --ttl 30m
    })"
    unset video_secret
    export CLOUD_VALIDATION_VIDEO_CLOUD_ADMIN_TOKEN
  fi

  logger_token_file="$env_root/state/secrets/cloud-logger-ingest-token"
  if [[ -z "${CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN:-}" && "$provider" == "lke" ]]; then
    CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN="$({
      cd "$workspace_root/scripts/go"
      GOWORK=off go run ./rtk-cloud -- cloud-logger-token --workspace "$workspace_root" --env-root "$env_root"
    })"
    export CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN
  elif [[ -z "${CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN:-}" && -s "$logger_token_file" ]]; then
    CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN="$(<"$logger_token_file")"
    export CLOUD_VALIDATION_CLOUD_LOGGER_TOKEN
  fi
}

load_local_deployment_credentials

run_platform() {
  local selected_platform="$1"
  local selected_run_id="$run_id"
  if [[ -z "$selected_run_id" ]]; then
    selected_run_id="sdk-cloud-${selected_platform}-$(date -u +%Y%m%dT%H%M%SZ)"
  elif [[ "$platform" == "all" ]]; then
    selected_run_id="${selected_run_id}-${selected_platform}"
  fi
  local output_dir="${CLOUD_VALIDATION_ARTIFACT_ROOT:-$workspace_root/.artifacts/e2e_test/cloud_validation}/$selected_run_id"
  mkdir -p "$output_dir"

  export RTK_CLOUD_WORKSPACE="$workspace_root"
  export CLOUD_VALIDATION_ENVIRONMENT="$environment"
  export CLOUD_VALIDATION_PLATFORM="$selected_platform"
  export CLOUD_VALIDATION_MODE="$mode"
  export CLOUD_VALIDATION_PROFILE="$profile"
  if [[ "$profile" == "nightly" ]]; then
    export CLOUD_VALIDATION_VERIFY_RESTART=1
  fi
  export CLOUD_VALIDATION_RUN_ID="$selected_run_id"
  export CLOUD_VALIDATION_OUT_DIR="$output_dir"
  local platform_upper
  platform_upper="$(printf '%s' "$selected_platform" | tr '[:lower:]' '[:upper:]')"
  local platform_bundle_var="CLOUD_VALIDATION_${platform_upper}_RUNTIME_BUNDLE"
  local platform_artifact_var="CLOUD_VALIDATION_${platform_upper}_ARTIFACT"
  local platform_sha_var="CLOUD_VALIDATION_${platform_upper}_ARTIFACT_SHA256"
  if [[ -n "${!platform_bundle_var:-}" ]]; then
    export CLOUD_VALIDATION_RUNTIME_BUNDLE="${!platform_bundle_var}"
  elif [[ "$platform" != "all" && -n "$runtime_bundle_override" ]]; then
    export CLOUD_VALIDATION_RUNTIME_BUNDLE="$runtime_bundle_override"
  else
    secret_base="${CLOUD_VALIDATION_SECRET_ROOT:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/sdk-cloud-validation-secrets}"
    export CLOUD_VALIDATION_RUNTIME_BUNDLE="$secret_base/$selected_run_id/runtime-bundle.json"
  fi
  if [[ -n "${!platform_artifact_var:-}" ]]; then
    export CLOUD_VALIDATION_ARTIFACT="${!platform_artifact_var}"
    export CLOUD_VALIDATION_ARTIFACT_SHA256="${!platform_sha_var:-}"
  elif [[ "$platform" != "all" ]]; then
    export CLOUD_VALIDATION_ARTIFACT="$artifact_override"
    export CLOUD_VALIDATION_ARTIFACT_SHA256="$artifact_sha_override"
  elif [[ "$mode" == "package" ]]; then
    echo "package mode with --platform all requires CLOUD_VALIDATION_IOS_ARTIFACT and CLOUD_VALIDATION_ANDROID_ARTIFACT" >&2
    return 2
  fi
  export CLOUD_VALIDATION_SDK_COMMIT="${CLOUD_VALIDATION_SDK_COMMIT:-$(git -C "$workspace_root/repos/rtk_cloud_client" rev-parse HEAD)}"
  if [[ -z "${CLOUD_VALIDATION_CONTRACTS_COMMIT:-}" && -d "$workspace_root/repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc" ]]; then
    export CLOUD_VALIDATION_CONTRACTS_COMMIT="$(git -C "$workspace_root/repos/rtk_cloud_client/docs/rtk_cloud_contracts_doc" rev-parse HEAD 2>/dev/null || true)"
  fi
  export CLOUD_VALIDATION_READINESS_COMMAND="${CLOUD_VALIDATION_READINESS_COMMAND:-$script_dir/check-cloud-readiness.sh}"
  export CLOUD_VALIDATION_SETUP_COMMAND="${CLOUD_VALIDATION_SETUP_COMMAND:-$script_dir/setup-fixture.sh}"
  export CLOUD_VALIDATION_VIRTUAL_DEVICE_COMMAND="${CLOUD_VALIDATION_VIRTUAL_DEVICE_COMMAND:-$script_dir/run-virtual-device.sh}"
  export CLOUD_VALIDATION_EVIDENCE_COMMAND="${CLOUD_VALIDATION_EVIDENCE_COMMAND:-$script_dir/collect-cloud-evidence.sh}"
  export CLOUD_VALIDATION_CLEANUP_COMMAND="${CLOUD_VALIDATION_CLEANUP_COMMAND:-$script_dir/cleanup-fixture.sh}"
  if [[ "$selected_platform" == "ios" ]]; then
    export CLOUD_VALIDATION_PLATFORM_COMMAND="${CLOUD_VALIDATION_IOS_COMMAND:-$script_dir/run-ios.sh}"
  else
    export CLOUD_VALIDATION_PLATFORM_COMMAND="${CLOUD_VALIDATION_ANDROID_COMMAND:-$script_dir/run-android.sh}"
  fi

  local args=(
    --environment "$environment"
    --platform "$selected_platform"
    --mode "$mode"
    --run-id "$selected_run_id"
    --out-dir "$output_dir"
  )
  if [[ -n "$scenario_files" ]]; then
    args+=(--scenarios "$scenario_files")
  fi
  if [[ "$plan_only" == "1" ]]; then
    args+=(--plan-only)
  fi
  "$runner_bin" "${args[@]}"
}

if [[ "$platform" == "all" ]]; then
  set +e
  run_platform ios
  ios_rc=$?
  run_platform android
  android_rc=$?
  set -e
  if [[ "$ios_rc" == "1" || "$android_rc" == "1" ]]; then
    exit 1
  fi
  if [[ "$ios_rc" == "2" || "$android_rc" == "2" ]]; then
    exit 2
  fi
  if [[ "$ios_rc" == "3" || "$android_rc" == "3" ]]; then
    exit 3
  fi
else
  run_platform "$platform"
fi
