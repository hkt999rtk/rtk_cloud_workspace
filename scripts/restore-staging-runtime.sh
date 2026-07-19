#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  restore-staging-runtime.sh --source-runtime PATH [--target-runtime PATH]
  restore-staging-runtime.sh --check-only [--target-runtime PATH]

The source must be a trusted copy of the existing staging runtime. The script
does not delete target files and does not create or modify cloud resources.
EOF
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_runtime=""
target_runtime="$repo_root/cloud_env/staging/runtime"
check_only=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-runtime)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      source_runtime="$2"
      shift 2
      ;;
    --target-runtime)
      [[ $# -ge 2 ]] || { usage; exit 2; }
      target_runtime="$2"
      shift 2
      ;;
    --check-only)
      check_only=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage
      exit 2
      ;;
  esac
done

if [[ "$check_only" == false && -z "$source_runtime" ]]; then
  echo "--source-runtime is required unless --check-only is used" >&2
  exit 2
fi

required_paths=(
  "adapters/lke/account.env"
  "adapters/lke/state.env"
  "adapters/lke/config.env"
  "state/kubeconfig.yaml"
  "state/provider-preflight.env"
  "state/video-cloud-staging.state.json"
  "state/openbao/root-token"
  "state/openbao/unseal-key"
  "env/stack.env"
  "services/account-manager/account-manager-platform-admin.env"
  "services/account-manager/account-manager.env"
  "services/video-cloud/video-cloud.env"
  "devices/test_device/loadtest.env"
  "artifacts/test-data/rtk-test-data.sqlite"
)

check_runtime() {
  local root="$1"
  local missing=0
  local relative
  if [[ ! -d "$root" ]]; then
    echo "runtime directory missing: $root" >&2
    return 1
  fi
  for relative in "${required_paths[@]}"; do
    if [[ ! -s "$root/$relative" ]]; then
      echo "missing or empty: $root/$relative" >&2
      missing=1
    fi
  done
  if [[ "$missing" -ne 0 ]]; then
    return 1
  fi
  if [[ "$(stat -f '%Lp' "$root/state/kubeconfig.yaml" 2>/dev/null || true)" != "600" ]]; then
    echo "expected mode 600: $root/state/kubeconfig.yaml" >&2
    return 1
  fi
  if [[ "$(stat -f '%Lp' "$root/adapters/lke/account.env" 2>/dev/null || true)" != "600" ]]; then
    echo "expected mode 600: $root/adapters/lke/account.env" >&2
    return 1
  fi
  echo "staging runtime check passed: $root"
}

if [[ "$check_only" == false ]]; then
  [[ -d "$source_runtime" ]] || { echo "source runtime missing: $source_runtime" >&2; exit 1; }
  check_runtime "$source_runtime"
  mkdir -p "$target_runtime"
  rsync -a "$source_runtime/" "$target_runtime/"
  chmod 600 "$target_runtime/state/kubeconfig.yaml" "$target_runtime/adapters/lke/account.env"
fi

check_runtime "$target_runtime"
echo "no cloud resources were created or modified"
