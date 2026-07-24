#!/usr/bin/env bash
set -uo pipefail

workspace="${GITHUB_WORKSPACE:-$(git rev-parse --show-toplevel)}"
run_id="${RUNTIME_COVERAGE_RUN_ID:-}"
mode="${RUNTIME_COVERAGE_MODE:-preflight}"
confirm="${RUNTIME_COVERAGE_CONFIRM:-}"
cluster_label="${CLOUD_STAGING_LKE_CLUSTER_LABEL:-}"
runner_label="${RUNTIME_COVERAGE_RUNNER_LABEL:-}"
runner_os="${RUNNER_OS:-unknown}"
runner_arch="${RUNNER_ARCH:-unknown}"
report="${RUNTIME_COVERAGE_PREFLIGHT_REPORT:-$workspace/.artifacts/runtime-coverage/$run_id/preflight-report.json}"
failures=()

fail() {
  failures+=("$1")
}

for command in docker kubectl go node jq git curl sqlite3 openssl; do
  command -v "$command" >/dev/null 2>&1 || fail "missing command: $command"
done
docker buildx version >/dev/null 2>&1 || fail "docker buildx is unavailable"

if [[ "$(node -p 'process.versions.node.split(`.`)[0]' 2>/dev/null)" != "22" ]]; then
  fail "Node 22 is required"
fi
if [[ "$mode" != "preflight" && "$mode" != "run" ]]; then
  fail "mode must be preflight or run"
fi
if [[ "$mode" == "run" && "$confirm" != "video-cloud-staging-lke" ]]; then
  fail "run mode requires confirmation video-cloud-staging-lke"
fi
if [[ "$cluster_label" != "video-cloud-staging-lke" ]]; then
  fail "CLOUD_STAGING_LKE_CLUSTER_LABEL must be video-cloud-staging-lke"
fi
if [[ -z "$runner_label" ]]; then
  fail "RUNTIME_COVERAGE_RUNNER_LABEL is required"
fi
if [[ "${GITHUB_ACTIONS:-}" == "true" && "$runner_os" != "${RUNTIME_COVERAGE_RUNNER_OS:-Linux}" ]]; then
  fail "runner OS must be ${RUNTIME_COVERAGE_RUNNER_OS:-Linux}"
fi
if [[ "${GITHUB_ACTIONS:-}" == "true" && "$runner_arch" != "${RUNTIME_COVERAGE_RUNNER_ARCH:-X64}" ]]; then
  fail "runner architecture must be ${RUNTIME_COVERAGE_RUNNER_ARCH:-X64}"
fi
if ! [[ "${LKE_ACTIVE_SERVICE_LIMIT:-}" =~ ^[1-9][0-9]*$ ]]; then
  fail "LKE_ACTIVE_SERVICE_LIMIT repository variable must be a positive integer"
fi

for name in LINODE_TOKEN GHCR_PULL_TOKEN; do
  [[ -n "${!name:-}" ]] || fail "missing required credential: $name"
done
[[ -s "$workspace/repos/rtk_video_cloud/cmd/admin-token/main.go" ]] ||
  fail "run-scoped Video Cloud admin-token source is missing"
grep -Fq "admin-token" "$workspace/tests/runtime-coverage/Dockerfile.video-cloud" ||
  fail "runtime Video Cloud image does not include admin-token"
openssl ecparam -list_curves 2>/dev/null | grep -Fq "prime256v1" ||
  fail "OpenSSL prime256v1 support is unavailable"

if [[ -z "${KUBECONFIG:-}" || ! -s "${KUBECONFIG:-}" ]]; then
  fail "run-scoped kubeconfig is missing"
elif ! kubectl --kubeconfig "$KUBECONFIG" get --raw=/readyz >/dev/null 2>&1; then
  fail "shared staging Kubernetes API is not ready"
fi

snapshot="$workspace/.artifacts/runtime-coverage/$run_id/staging-before.json"
if [[ ! -s "$snapshot" ]]; then
  fail "staging deployment snapshot is missing"
elif [[ "$(jq '.deployments | length' "$snapshot" 2>/dev/null)" == "0" ]]; then
  fail "staging deployment snapshot contains no workloads"
elif [[ "$(jq '.image_digests | length' "$snapshot" 2>/dev/null)" == "0" ]]; then
  fail "staging deployment snapshot contains no running image digests"
fi

mkdir -p "$(dirname "$report")"
status="PASS"
if ((${#failures[@]})); then
  status="BLOCKED"
fi
failure_json="$(printf '%s\n' "${failures[@]:-}" | jq -Rsc 'split("\n") | map(select(length > 0))')"
workspace_commit="$(git -C "$workspace" rev-parse HEAD)"
submodule_commits="$(git -C "$workspace" submodule status | awk '{gsub(/^[+-]/, "", $1); print $2 "=" $1}' | jq -Rsc 'split("\n") | map(select(length > 0))')"
jq -n \
  --arg status "$status" \
  --arg mode "$mode" \
  --arg run_id "$run_id" \
  --arg cluster_label "$cluster_label" \
  --arg runner_label "$runner_label" \
  --arg runner_name "${RUNNER_NAME:-}" \
  --arg runner_os "$runner_os" \
  --arg runner_arch "$runner_arch" \
  --arg workspace_commit "$workspace_commit" \
  --arg generated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson failures "$failure_json" \
  --argjson submodule_commits "$submodule_commits" \
  '{
    schema_version: 1,
    status: $status,
    mode: $mode,
    run_id: $run_id,
    cluster_label: $cluster_label,
    runner: {
      label: $runner_label,
      name: $runner_name,
      os: $runner_os,
      architecture: $runner_arch
    },
    credential_strategy: {
      cluster_access: "repository-secret",
      admin_token: "run-scoped-after-deploy",
      clip_user_key: "run-scoped-after-deploy",
      clip_server_public_key: "derived-from-run-scoped-stack",
      ui_sessions: "run-scoped-test-accounts"
    },
    workspace_commit: $workspace_commit,
    submodule_commits: $submodule_commits,
    generated_at: $generated_at,
    failures: $failures
  }' > "$report"

if [[ "$status" != "PASS" ]]; then
  echo "runtime coverage preflight BLOCKED; inspect $report" >&2
  exit 1
fi
echo "runtime coverage preflight PASS: $report"
