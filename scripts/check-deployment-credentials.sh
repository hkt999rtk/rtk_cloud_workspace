#!/usr/bin/env bash
set -euo pipefail

# The phase wrappers set these only for their child process. Standalone legacy
# callers retain the existing post-deploy provider-canary behavior.
CHECK_PHASE="${RTK_CLOUD_CHECK_SCRIPT_PHASE:-post-deploy}"
CHECK_READ_ONLY="${RTK_CLOUD_CHECK_SCRIPT_READ_ONLY:-false}"
case "$CHECK_PHASE" in
  pre-deploy)
    printf '%s\n' '部署前可部署性檢查（pre-deploy）：確認完整 create／upgrade 的目標設定、工具與 SecretStore 前置條件；請在整個環境部署前使用。'
    printf '%s\n' '唯讀檢查，未驗證寫入權限；評估舊路由能否安全遷移，不以舊部署健康問題阻擋升級。通過不代表目前服務健康；--fast 只減少驗證深度。'
    ;;
  post-deploy)
    printf '%s\n' '部署後環境健康檢查（post-deploy）：驗證已部署環境的 Secret 綁定、PKI 與公開入口；請在部署後或排查現況時使用。'
    if [[ "$CHECK_READ_ONLY" == true ]]; then
      printf '%s\n' '此入口唯讀；不作為部署前可部署性判定，也不取代完整應用驗收。--fast 只減少驗證深度。'
    else
      printf '%s\n' 'check-deployment-credentials.sh 是 post-deploy 相容入口；標準模式 provider 驗證可能使用暫時性 DNS／storage 寫入。唯讀請用 check-deployment-health.sh；--fast 只減少驗證深度。'
    fi
    ;;
  *)
    printf '%s\n' '部署環境檢查：部署前使用 check-deployment-preflight.sh；部署後使用 check-deployment-health.sh。'
    printf '%s\n' 'error: unsupported wrapper phase' >&2
    exit 2
    ;;
esac

# A fixed-purpose script cannot silently become the other checker because a
# later flag overrides its phase. Normalize matching phase flags before build.
CHECK_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --phase|-phase)
      if [[ $# -lt 2 || "$2" != "$CHECK_PHASE" ]]; then
        printf 'error: this script requires --phase %s\n' "$CHECK_PHASE" >&2
        exit 2
      fi
      shift 2
      ;;
    --phase=*|-phase=*)
      if [[ "${1#*=}" != "$CHECK_PHASE" ]]; then
        printf 'error: this script requires --phase %s\n' "$CHECK_PHASE" >&2
        exit 2
      fi
      shift
      ;;
    --read-only=*|-read-only=*)
      case "${1#*=}" in
        false|False|FALSE|f|F|0)
          if [[ "$CHECK_READ_ONLY" == true || "$CHECK_PHASE" == pre-deploy ]]; then
            printf '%s\n' 'error: this script is read-only; --read-only=false is not allowed' >&2
            exit 2
          fi
          ;;
      esac
      CHECK_ARGS+=("$1")
      shift
      ;;
    *)
      CHECK_ARGS+=("$1")
      shift
      ;;
  esac
done
if [[ "$CHECK_READ_ONLY" == true ]]; then
  CHECK_ARGS+=(--read-only)
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Build once and execute once: go run would turn the checker's exit 2 into 1.
CHECK_TMP="$(mktemp -d)"
CHECK_PID=""
trap 'rm -rf "$CHECK_TMP"' EXIT

forward_signal() {
  local signal="$1" status="$2"
  trap '' INT TERM
  if [[ -n "$CHECK_PID" ]]; then
    kill -s "$signal" "$CHECK_PID" 2>/dev/null || true
    # Give the checker time to perform its bounded canary cleanup.
    wait "$CHECK_PID" 2>/dev/null || true
  fi
  exit "$status"
}
trap 'forward_signal INT 1' INT
trap 'forward_signal TERM 1' TERM

run_child() {
  local status=0
  "$@" &
  CHECK_PID=$!
  wait "$CHECK_PID" || status=$?
  CHECK_PID=""
  return "$status"
}

cd "$ROOT"
run_child go build -o "$CHECK_TMP/rtk-cloud" ./scripts/go/rtk-cloud
export RTK_CLOUD_CHECK_BANNER_PHASE="$CHECK_PHASE"
run_child "$CHECK_TMP/rtk-cloud" deployment check --workspace "$ROOT" --phase "$CHECK_PHASE" "${CHECK_ARGS[@]}"
