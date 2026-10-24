#!/usr/bin/env bash
set -euo pipefail

# The phase wrappers set these only for their child process. Standalone legacy
# callers retain the existing post-deploy provider-canary behavior.
CHECK_PHASE="${RTK_CLOUD_CHECK_SCRIPT_PHASE:-post-deploy}"
CHECK_READ_ONLY="${RTK_CLOUD_CHECK_SCRIPT_READ_ONLY:-false}"
case "$CHECK_PHASE" in
  pre-deploy)
    cat <<'PREFLIGHT_PURPOSE'
Pre-deploy deployability check (pre-deploy)
  Validate prerequisites before deployment.
  For existing Pod image updates, use --operation image-upgrade.

Operation
  - Default: complete upgrade of an existing Kubernetes environment.
  - --operation create detects fresh creation or an unfinished original
    dev checkpoint and qualifies the appropriate prerequisites.
  - Original-checkpoint qualification preserves the execution fence and
    OpenBao/PKI identities; it never starts deployment.
  - Original recovery inputs are resolved from journal-bound evidence in
    the selected canonical store; no extra recovery operation is needed.
  - Fresh dev/staging create qualifies provider allocation and native
    bootstrap without requiring an existing kubeconfig.

Validation and results
  - Runs complete validation; every required gate must pass.
  - Missing capability or evidence means NO-GO.
  - Builds the exact plan from settings, live identity references and
    formal CI image-manifest.json artifacts.
  - Saves checked bindings and evidence with an expiry time.
  - Deployment verifies and reuses valid evidence, runs live checks,
    then performs post-deploy acceptance.

Provider access
  - Checks the configured media bucket; may create it if missing and
    no storage cutover is required.
  - Other provider probes are read-only.
  - No data migration or credential replacement.

Required inputs
  - Existing Kubernetes upgrades use the selected kubeconfig.
  - Fresh create uses selected operator credentials, verified target
    absence and formal CI images; deployment creates new runtime state.
  - No pre-existing bindings or local release file is required.
  - Existing upgrades need no local Linode cluster ID or account quota state.
  - Private schema-1 plans are retired.

PREFLIGHT_PURPOSE
    ;;
  post-deploy)
    cat <<'HEALTH_PURPOSE'
Post-deploy environment health check (post-deploy)
  Run after deployment or to diagnose the current environment.

Checks
  - Deployed Secret bindings and PKI.
  - Required public mTLS endpoints.
  - Environment workload readiness.
  - Selected provider and input checks.

Scope
  - Reports the current environment's health.
  - Use check-deployment-preflight.sh before deploying a new target.
  - Full application acceptance requires separate application tests.

Execution mode
HEALTH_PURPOSE
    if [[ "$CHECK_READ_ONLY" == true ]]; then
      cat <<'HEALTH_READ_ONLY'
  - This entry point is read-only.
  - --fast reduces validation depth.

HEALTH_READ_ONLY
    else
      cat <<'HEALTH_COMPATIBILITY'
  - Compatibility entry point: check-deployment-credentials.sh.
  - Provider validation may use temporary DNS or storage writes.
  - Use check-deployment-health.sh for read-only checks.
  - --fast reduces validation depth.

HEALTH_COMPATIBILITY
    fi
    ;;
  *)
    printf '%s\n' 'Deployment environment check: Use check-deployment-preflight.sh before deployment, and check-deployment-health.sh after deployment.'
    printf '%s\n' 'error: unsupported wrapper phase' >&2
    exit 2
    ;;
esac

# A fixed-purpose script cannot silently become the other checker because a
# later flag overrides its phase. Normalize matching phase flags before build.
CHECK_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --fast|-fast|--fast=*|-fast=*)
      if [[ "$CHECK_PHASE" == pre-deploy ]]; then
        printf '%s\n' 'error: --fast is not supported by preflight; omit it to run complete validation' >&2
        exit 2
      fi
      CHECK_ARGS+=("$1")
      shift
      ;;
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
