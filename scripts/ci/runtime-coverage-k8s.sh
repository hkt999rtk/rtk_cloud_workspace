#!/usr/bin/env bash
set -euo pipefail

action="${1:-}"
workspace="${GITHUB_WORKSPACE:-$(git rev-parse --show-toplevel)}"
run_id="${RUNTIME_COVERAGE_RUN_ID:-}"
stack="${RUNTIME_COVERAGE_STACK:-}"
kubeconfig="${KUBECONFIG:-}"
output_root="${RUNTIME_COVERAGE_OUTPUT_ROOT:-$workspace/.artifacts/runtime-coverage/$run_id}"

usage() {
  echo "usage: runtime-coverage-k8s.sh prepare|collect|cleanup" >&2
}

validate_scope() {
  [[ "$run_id" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || {
    echo "RUNTIME_COVERAGE_RUN_ID is missing or invalid" >&2
    exit 2
  }
  [[ "$stack" =~ ^coverage-[a-z0-9-]+$ ]] || {
    echo "RUNTIME_COVERAGE_STACK must start with coverage- and contain lowercase letters, digits, and hyphens" >&2
    exit 2
  }
  [[ "$stack" != *staging* && "$stack" != *production* && "$stack" != *prod* ]] || {
    echo "runtime coverage refuses staging/production stack names" >&2
    exit 2
  }
  [[ -n "$kubeconfig" && -f "$kubeconfig" ]] || {
    echo "KUBECONFIG must point to the isolated runtime-coverage cluster credentials" >&2
    exit 2
  }
}

namespace_module() {
  case "$1" in
    "$stack-account-manager") echo "account-manager" ;;
    "$stack-admin") echo "cloud-admin-backend" ;;
    "$stack-frontend") echo "cloud-frontend" ;;
    "$stack-logger") echo "cloud-logger" ;;
    "$stack-video-cloud") echo "video-cloud" ;;
    *) return 1 ;;
  esac
}

coverage_namespaces() {
  printf '%s\n' \
    "$stack-account-manager" \
    "$stack-admin" \
    "$stack-frontend" \
    "$stack-logger" \
    "$stack-video-cloud"
}

stack_namespaces() {
  printf '%s\n' \
    "$stack-platform" \
    "$stack-secrets" \
    "$stack-account-manager" \
    "$stack-admin" \
    "$stack-frontend" \
    "$stack-logger" \
    "$stack-video-cloud" \
    "$stack-observability" \
    "$stack-ingress"
}

prepare_deployment() {
  local namespace="$1"
  local deployment="$2"
  local claim="${deployment}-runtime-coverage"
  local container patch
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $claim
  labels:
    rtk-cloud-run-id: $run_id
    rtk-cloud-purpose: runtime-coverage
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
EOF
  container="$(kubectl --kubeconfig "$kubeconfig" -n "$namespace" get "deployment/$deployment" -o jsonpath='{.spec.template.spec.containers[0].name}')"
  patch="$(jq -cn \
    --arg container "$container" \
    --arg claim "$claim" \
    --arg run_id "$run_id" \
    '{spec:{template:{spec:{
      securityContext:{fsGroup:10001,fsGroupChangePolicy:"OnRootMismatch"},
      volumes:[{name:"runtime-coverage",persistentVolumeClaim:{claimName:$claim}}],
      containers:[{
        name:$container,
        env:[
          {name:"GOCOVERDIR",value:"/coverage"},
          {name:"RUNTIME_COVERAGE_RUN_ID",value:$run_id}
        ],
        volumeMounts:[{name:"runtime-coverage",mountPath:"/coverage"}]
      }]
    }}}}')"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" patch "deployment/$deployment" --type strategic -p "$patch"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" rollout status "deployment/$deployment" --timeout=10m
}

prepare() {
  while IFS= read -r namespace; do
    kubectl --kubeconfig "$kubeconfig" get namespace "$namespace" >/dev/null
    while IFS= read -r deployment; do
      [[ -n "$deployment" ]] || continue
      prepare_deployment "$namespace" "$deployment"
    done < <(kubectl --kubeconfig "$kubeconfig" -n "$namespace" get deployment \
      -l "rtk.realtek.com/stack=$stack" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  done < <(coverage_namespaces)
}

collect_claim() {
  local namespace="$1"
  local module="$2"
  local claim="$3"
  local collector="coverage-collector-${claim:0:35}"
  local destination="$output_root/$module/$namespace/$claim"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" delete pod "$collector" --ignore-not-found=true --wait=true
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" run "$collector" \
    --image=alpine:3.22 --restart=Never --overrides="$(
      printf '{"spec":{"containers":[{"name":"collector","image":"alpine:3.22","command":["sh","-c","sleep 1800"],"volumeMounts":[{"name":"coverage","mountPath":"/coverage"}]}],"volumes":[{"name":"coverage","persistentVolumeClaim":{"claimName":"%s"}}]}}' "$claim"
    )"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" wait --for=condition=Ready "pod/$collector" --timeout=3m
  mkdir -p "$destination"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" cp "$collector:/coverage/." "$destination"
  kubectl --kubeconfig "$kubeconfig" -n "$namespace" delete pod "$collector" --wait=true
}

write_anchor() {
  local module="$1"
  local module_path="$2"
  local destination="$output_root/$module/coverage-runtime.json"
  local workspace_commit module_commit generated_at
  workspace_commit="$(git -C "$workspace" rev-parse HEAD)"
  module_commit="$(git -C "$workspace/$module_path" rev-parse HEAD)"
  generated_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  mkdir -p "$(dirname "$destination")"
  jq -n \
    --arg run_id "$run_id" \
    --arg module "$module" \
    --arg workspace_commit "$workspace_commit" \
    --arg module_commit "$module_commit" \
    --arg generated_at "$generated_at" \
    '{schema_version:1,run_id:$run_id,module:$module,workspace_commit:$workspace_commit,module_commit:$module_commit,generated_at:$generated_at}' \
    > "$destination"
}

collect() {
  while IFS= read -r namespace; do
    module="$(namespace_module "$namespace")"
    kubectl --kubeconfig "$kubeconfig" -n "$namespace" scale deployment --all --replicas=0
    while IFS= read -r claim; do
      [[ -n "$claim" ]] || continue
      collect_claim "$namespace" "$module" "$claim"
    done < <(kubectl --kubeconfig "$kubeconfig" -n "$namespace" get pvc \
      -l "rtk-cloud-run-id=$run_id,rtk-cloud-purpose=runtime-coverage" \
      -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  done < <(coverage_namespaces)

  write_anchor account-manager repos/rtk_account_manager
  write_anchor cloud-admin-backend repos/rtk_cloud_admin
  write_anchor cloud-frontend repos/rtk_cloud_frontend
  write_anchor cloud-logger repos/rtk_cloud_logger
  write_anchor video-cloud repos/rtk_video_cloud

  for module in account-manager cloud-admin-backend cloud-frontend cloud-logger video-cloud; do
    find "$output_root/$module" -type f -name 'covmeta.*' -print -quit | grep -q . || {
      echo "$module runtime coverage has no covmeta files" >&2
      exit 1
    }
    find "$output_root/$module" -type f -name 'covcounters.*' -print -quit | grep -q . || {
      echo "$module runtime coverage has no covcounters files" >&2
      exit 1
    }
  done
}

cleanup() {
  while IFS= read -r namespace; do
    kubectl --kubeconfig "$kubeconfig" delete namespace "$namespace" --ignore-not-found=true --wait=true
  done < <(stack_namespaces)
}

validate_scope
case "$action" in
  prepare) prepare ;;
  collect) collect ;;
  cleanup) cleanup ;;
  *) usage; exit 2 ;;
esac
