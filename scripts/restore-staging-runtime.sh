#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  restore-staging-runtime.sh --source-runtime PATH [--target-runtime PATH]
  restore-staging-runtime.sh --check-only [--target-runtime PATH]

Copies only an allowlist of non-secret controller state from a trusted staging
runtime. This is NOT a database/OpenBao/SecretStore disaster-recovery tool.
It does not delete target files or create/modify cloud resources.
See docs/backup-restore.md for coordinated core backup and restore.
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
  "state/provider-preflight.env"
  "env/stack.env"
)

optional_paths=(
  "adapters/lke/account.env"
  "adapters/lke/state.env"
  "adapters/lke/config.env"
  "state/video-cloud-staging.state.json"
)

check_path() {
  local path="$1"
  [[ "$path" == /* ]] || { echo "absolute paths are required" >&2; return 1; }
  while [[ "$path" != / ]]; do
    [[ ! -L "$path" ]] || { echo "symlink paths are refused" >&2; return 1; }
    path="$(dirname "$path")"
  done
}

check_nonsecret_file() {
  local path="$1"
  check_path "$path"
  [[ -f "$path" ]] || { echo "runtime member is not a regular file" >&2; return 1; }
  # Defensive legacy-content check, not a general-purpose secret scanner.
  # Never print matching lines: they may be credentials.
  if LC_ALL=C grep -Eiq -- '-----BEGIN .*PRIVATE KEY-----|(^|["[:space:]])[A-Z0-9_]*(PASSWORD|SECRET|TOKEN|PRIVATE_KEY|DSN)["[:space:]]*[:=]|postgres(ql)?://[^[:space:]]+:[^[:space:]]+@' "$path"; then
    echo "possible secret-bearing legacy runtime; use secrets migrate first" >&2
    return 1
  fi
}

check_runtime() {
  local root="$1"
  local missing=0
  local relative
  check_path "$root"
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
  for relative in "${required_paths[@]}" "${optional_paths[@]}"; do
    if [[ -e "$root/$relative" || -L "$root/$relative" ]]; then
      check_nonsecret_file "$root/$relative" || return 1
    fi
  done
  echo "staging runtime check passed: $root"
}

if [[ "$check_only" == false ]]; then
  [[ -d "$source_runtime" ]] || { echo "source runtime missing: $source_runtime" >&2; exit 1; }
  check_runtime "$source_runtime"
  check_path "$target_runtime"
  mkdir -p "$target_runtime"
  # Validate all destinations before copying; never recursively transfer the
  # runtime tree (older trees contain kubeconfig, keys and raw service env).
  for relative in "${required_paths[@]}" "${optional_paths[@]}"; do
    check_path "$target_runtime/$relative"
    [[ ! -e "$target_runtime/$relative" || -f "$target_runtime/$relative" ]] || exit 1
  done
  for relative in "${required_paths[@]}" "${optional_paths[@]}"; do
    if [[ -f "$source_runtime/$relative" ]]; then
      mkdir -p "$(dirname "$target_runtime/$relative")"
      cp "$source_runtime/$relative" "$target_runtime/$relative"
      chmod 600 "$target_runtime/$relative"
    fi
  done
fi

check_runtime "$target_runtime"
echo "no cloud resources were created or modified"
