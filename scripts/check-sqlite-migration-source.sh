#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s --stack STACK --kubeconfig FILE\n' "$0" >&2
  exit 2
}

stack=""
kubeconfig=""
while (($#)); do
  case "$1" in
    --stack)
      (($# >= 2)) || usage
      stack="$2"
      shift 2
      ;;
    --kubeconfig)
      (($# >= 2)) || usage
      kubeconfig="$2"
      shift 2
      ;;
    *) usage ;;
  esac
done

[[ "$stack" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || usage
[[ -f "$kubeconfig" ]] || usage
command -v jq >/dev/null || { printf 'jq is required\n' >&2; exit 2; }
command -v kubectl >/dev/null || { printf 'kubectl is required\n' >&2; exit 2; }

kubectl_read() {
  kubectl --kubeconfig "$kubeconfig" "$@"
}

inspect() {
  local namespace="$1" deployment="$2" directory="$3"
  local deployment_json pods_json pod uid file_list
  deployment_json="$(kubectl_read -n "$namespace" get deployment "$deployment" -o json)"
  if ! jq -e --arg directory "$directory" '
    .spec.replicas == 1 and
    ([.spec.template.spec.volumes[]? | select(.name == "sqlite-data")] | length) == 0 and
    ([.spec.template.spec.containers[]? | select(.name == "app") | .volumeMounts[]? | select(.mountPath == $directory)] | length) == 0
  ' <<<"$deployment_json" >/dev/null; then
    printf '%s: expected one source replica without a SQLite data mount\n' "$deployment" >&2
    return 1
  fi
  pods_json="$(kubectl_read -n "$namespace" get pods -l "app.kubernetes.io/name=$deployment" -o json)"
  if ! jq -e '.items | length == 1' <<<"$pods_json" >/dev/null; then
    printf '%s: expected exactly one source Pod\n' "$deployment" >&2
    return 1
  fi
  pod="$(jq -r '.items[0].metadata.name // empty' <<<"$pods_json")"
  uid="$(jq -r '.items[0].metadata.uid // empty' <<<"$pods_json")"
  [[ -n "$pod" && -n "$uid" ]] || { printf '%s: source Pod identity is missing\n' "$deployment" >&2; return 1; }
  file_list="$(kubectl_read -n "$namespace" exec "$pod" -c app -- sh -c '
    set -eu
    directory="$1"
    find "$directory" -maxdepth 1 -type f \( -name "*.db" -o -name "*.db-wal" -o -name "*.db-shm" -o -name "*.db-journal" \) -exec ls -ln {} +
  ' sh "$directory")"
  [[ -n "$file_list" ]] || { printf '%s: no SQLite files found in %s\n' "$deployment" "$directory" >&2; return 1; }
  printf 'Deployment: %s/%s\nSource Pod: %s\nSource UID: %s\nSQLite files (metadata only):\n%s\n\n' \
    "$namespace" "$deployment" "$pod" "$uid" "$file_list"
}

inspect "$stack-admin" cloud-admin /app/data
inspect "$stack-frontend" frontend /data
