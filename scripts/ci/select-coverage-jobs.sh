#!/usr/bin/env bash
set -eo pipefail

base_ref=${1:-}
head_ref=${2:-HEAD}
event_name=${3:-pull_request}
output=${GITHUB_OUTPUT:-/dev/stdout}

go_modules=()
node_modules=()
policy=false
account_manager_postgres=false
billing_postgres=false
video_cloud_postgres_emqx=false

add_unique() {
  local value=$1
  shift
  local existing
  for existing in "$@"; do
    if [ "$existing" = "$value" ]; then
      return
    fi
  done
  go_modules+=("$value")
}

add_node_unique() {
  local value=$1
  local existing
  for existing in "${node_modules[@]}"; do
    if [ "$existing" = "$value" ]; then
      return
    fi
  done
  node_modules+=("$value")
}

select_all() {
  go_modules=(
    workspace-tooling workspace-e2e home-load-runner account-manager
    billing-service cloud-admin-backend cloud-client-golang cloud-frontend
    cloud-logger video-cloud godaddy-dns-toolkit
  )
  node_modules=(cloud-admin-web cloud-client-javascript)
  policy=true
  account_manager_postgres=true
  billing_postgres=true
  video_cloud_postgres_emqx=true
}

# A gitlink has no paths in the parent diff. Avoid scheduling unrelated Go
# coverage when the leaf commit changes only the PKI manifest renderer or its
# rollout instructions.
# Missing submodule history falls back to the full Video Cloud checks.
video_cloud_pki_support_only() {
  local before after paths path
  before=$(git rev-parse "$base_ref:repos/rtk_video_cloud" 2>/dev/null) || return 1
  after=$(git rev-parse "$head_ref:repos/rtk_video_cloud" 2>/dev/null) || return 1
  paths=$(git -C repos/rtk_video_cloud diff --name-only "$before" "$after" 2>/dev/null) || return 1
  [ -n "$paths" ] || return 1
  while IFS= read -r path; do
    case "$path" in
      deploy/pki/*.py|deploy/pki/*.md|docs/pki-staging-rollout.md) ;;
      *) return 1 ;;
    esac
  done <<< "$paths"
}

if [ "$event_name" = "workflow_dispatch" ] || [ -z "$base_ref" ] || [ "$base_ref" = "0000000000000000000000000000000000000000" ]; then
  select_all
else
  changed_files=()
  while IFS= read -r changed; do
    changed_files+=("$changed")
  done < <(git diff --name-only "$base_ref" "$head_ref")
  for changed in "${changed_files[@]}"; do
    case "$changed" in
      scripts/go/*|go.work|go.work.sum|.github/workflows/go-coverage-governance.yml|scripts/ci/select-coverage-jobs.sh)
        add_unique workspace-tooling "${go_modules[@]}"
        policy=true
        ;;
      e2e_test/*)
        add_unique workspace-e2e "${go_modules[@]}"
        ;;
      loadtests/home-100k/*)
        add_unique home-load-runner "${go_modules[@]}"
        ;;
      repos/rtk_account_manager|repos/rtk_account_manager/*)
        add_unique account-manager "${go_modules[@]}"
        account_manager_postgres=true
        policy=true
        ;;
      repos/rtk_billing|repos/rtk_billing/*)
        add_unique billing-service "${go_modules[@]}"
        billing_postgres=true
        policy=true
        ;;
      repos/rtk_cloud_admin|repos/rtk_cloud_admin/*)
        add_unique cloud-admin-backend "${go_modules[@]}"
        add_node_unique cloud-admin-web
        policy=true
        ;;
      repos/rtk_cloud_client|repos/rtk_cloud_client/*)
        add_unique cloud-client-golang "${go_modules[@]}"
        add_node_unique cloud-client-javascript
        policy=true
        ;;
      repos/rtk_cloud_frontend|repos/rtk_cloud_frontend/*)
        add_unique cloud-frontend "${go_modules[@]}"
        policy=true
        ;;
      repos/rtk_cloud_logger|repos/rtk_cloud_logger/*)
        add_unique cloud-logger "${go_modules[@]}"
        policy=true
        ;;
      repos/rtk_video_cloud|repos/rtk_video_cloud/*)
        if [ "$changed" = "repos/rtk_video_cloud" ] && video_cloud_pki_support_only; then
          policy=true
          continue
        fi
        add_unique video-cloud "${go_modules[@]}"
        add_unique godaddy-dns-toolkit "${go_modules[@]}"
        video_cloud_postgres_emqx=true
        policy=true
        ;;
      repos/rtk_cloud_contracts_doc|repos/rtk_cloud_contracts_doc/*|tests/*|docs/test-*|docs/spec-test-*)
        policy=true
        ;;
    esac
  done
fi

go_json=$(jq -cn '$ARGS.positional' --args "${go_modules[@]}")
node_json=$(jq -cn '$ARGS.positional' --args "${node_modules[@]}")
coverage_modules=$(jq -cn --argjson go "$go_json" --argjson node "$node_json" '$go + $node')
run_unit=$([ "${#go_modules[@]}" -gt 0 ] && echo true || echo false)
run_node=$([ "${#node_modules[@]}" -gt 0 ] && echo true || echo false)
run_coverage=false
if [ "$run_unit" = true ] || [ "$run_node" = true ] || [ "$account_manager_postgres" = true ] || [ "$billing_postgres" = true ] || [ "$video_cloud_postgres_emqx" = true ]; then
  run_coverage=true
fi

{
  echo "policy=$policy"
  echo "go_modules=$go_json"
  echo "node_modules=$node_json"
  echo "coverage_modules=$coverage_modules"
  echo "run_unit=$run_unit"
  echo "run_node=$run_node"
  echo "run_coverage=$run_coverage"
  echo "account_manager_postgres=$account_manager_postgres"
  echo "billing_postgres=$billing_postgres"
  echo "video_cloud_postgres_emqx=$video_cloud_postgres_emqx"
} >> "$output"
