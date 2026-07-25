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
capacity_json='{}'

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
for name in \
  RUNTIME_COVERAGE_PLANNED_PVCS \
  RUNTIME_COVERAGE_PLANNED_LOAD_BALANCERS \
  RUNTIME_COVERAGE_PLANNED_GENERATORS; do
  if ! [[ "${!name:-}" =~ ^[0-9]+$ ]]; then
    fail "$name must be a non-negative integer"
  fi
done

for name in \
  LINODE_TOKEN \
  GHCR_PULL_TOKEN \
  LKE_TURN_SHARED \
  VIDEO_CLOUD_WEBRTC_TURN_URLS \
  RUNTIME_COVERAGE_SHARED_TURN_HOST \
  LINODE_OBJ_ACCESS_KEY_ID \
  LINODE_OBJ_SECRET_ACCESS_KEY \
  LINODE_OBJ_ENDPOINT \
  LINODE_OBJ_REGION \
  LINODE_OBJ_BUCKET; do
  [[ -n "${!name:-}" ]] || fail "missing required credential: $name"
done
[[ "${RUNTIME_COVERAGE_STORAGE_SOURCE_NAMESPACE:-}" == "video-cloud-staging-video-cloud" ]] ||
  fail "runtime object storage must be sourced from the shared staging video-cloud namespace"
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

if [[ -n "${LINODE_TOKEN:-}" ]] &&
  [[ "${LKE_ACTIVE_SERVICE_LIMIT:-}" =~ ^[1-9][0-9]*$ ]] &&
  [[ "${RUNTIME_COVERAGE_PLANNED_PVCS:-}" =~ ^[0-9]+$ ]] &&
  [[ "${RUNTIME_COVERAGE_PLANNED_LOAD_BALANCERS:-}" =~ ^[0-9]+$ ]] &&
  [[ "${RUNTIME_COVERAGE_PLANNED_GENERATORS:-}" =~ ^[0-9]+$ ]]; then
  capacity_dir="$(mktemp -d)"
  capacity_failed=0
  for entry in \
    "instances:linode/instances" \
    "volumes:volumes" \
    "nodebalancers:nodebalancers"; do
    key="${entry%%:*}"
    endpoint="${entry#*:}"
    if ! curl -fsS \
      -H "Authorization: Bearer $LINODE_TOKEN" \
      "https://api.linode.com/v4/$endpoint?page_size=500" > "$capacity_dir/$key.json"; then
      fail "unable to query Linode $key for active-service headroom"
      capacity_failed=1
    fi
  done
  if [[ "$capacity_failed" == "0" ]]; then
    current_instances="$(jq -er '.results // (.data | length)' "$capacity_dir/instances.json")"
    current_volumes="$(jq -er '.results // (.data | length)' "$capacity_dir/volumes.json")"
    current_nodebalancers="$(jq -er '.results // (.data | length)' "$capacity_dir/nodebalancers.json")"
    current_total="$((current_instances + current_volumes + current_nodebalancers))"
    planned_total="$((
      RUNTIME_COVERAGE_PLANNED_PVCS +
      RUNTIME_COVERAGE_PLANNED_LOAD_BALANCERS +
      RUNTIME_COVERAGE_PLANNED_GENERATORS
    ))"
    projected_total="$((current_total + planned_total))"
    headroom="$((LKE_ACTIVE_SERVICE_LIMIT - current_total))"
    capacity_json="$(
      jq -n \
        --argjson configured_limit "$LKE_ACTIVE_SERVICE_LIMIT" \
        --argjson current_instances "$current_instances" \
        --argjson current_volumes "$current_volumes" \
        --argjson current_nodebalancers "$current_nodebalancers" \
        --argjson current_total "$current_total" \
        --argjson planned_pvcs "$RUNTIME_COVERAGE_PLANNED_PVCS" \
        --argjson planned_load_balancers "$RUNTIME_COVERAGE_PLANNED_LOAD_BALANCERS" \
        --argjson planned_generators "$RUNTIME_COVERAGE_PLANNED_GENERATORS" \
        --argjson planned_total "$planned_total" \
        --argjson headroom "$headroom" \
        --argjson projected_total "$projected_total" \
        '{
          configured_limit: $configured_limit,
          current: {
            instances: $current_instances,
            volumes: $current_volumes,
            nodebalancers: $current_nodebalancers,
            total: $current_total
          },
          planned: {
            persistent_volumes: $planned_pvcs,
            load_balancers: $planned_load_balancers,
            generators: $planned_generators,
            total: $planned_total
          },
          available_headroom: $headroom,
          projected_total: $projected_total,
          sufficient: ($projected_total <= $configured_limit)
        }'
    )"
    if ((projected_total > LKE_ACTIVE_SERVICE_LIMIT)); then
      fail "Linode active-service headroom is insufficient: current=$current_total planned=$planned_total projected=$projected_total limit=$LKE_ACTIVE_SERVICE_LIMIT"
    fi
  fi
  unlink "$capacity_dir/instances.json" 2>/dev/null || true
  unlink "$capacity_dir/volumes.json" 2>/dev/null || true
  unlink "$capacity_dir/nodebalancers.json" 2>/dev/null || true
  rmdir "$capacity_dir" 2>/dev/null || true
fi

if [[ -n "${KUBECONFIG:-}" && -s "${KUBECONFIG:-}" ]]; then
  released_coverage_pvs="$(
    kubectl --kubeconfig "$KUBECONFIG" get pv -o json 2>/dev/null |
      jq -r '.items[]
        | select(.status.phase == "Released")
        | select((.spec.claimRef.namespace // "") | startswith("coverage-"))
        | .metadata.name'
  )"
  if [[ -n "$released_coverage_pvs" ]]; then
    fail "released runtime-coverage PVs must be cleaned before a new run: $(printf '%s' "$released_coverage_pvs" | paste -sd, -)"
  fi
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
  --argjson active_service_capacity "$capacity_json" \
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
      clip_object_storage: "repository-secret-with-run-scoped-prefix",
      admin_token: "run-scoped-after-deploy",
      clip_user_key: "run-scoped-after-deploy",
      clip_server_public_key: "derived-from-run-scoped-stack",
      ui_sessions: "run-scoped-test-accounts"
    },
    workspace_commit: $workspace_commit,
    submodule_commits: $submodule_commits,
    active_service_capacity: $active_service_capacity,
    generated_at: $generated_at,
    failures: $failures
  }' > "$report"

if [[ "$status" != "PASS" ]]; then
  echo "runtime coverage preflight BLOCKED; inspect $report" >&2
  exit 1
fi
echo "runtime coverage preflight PASS: $report"
