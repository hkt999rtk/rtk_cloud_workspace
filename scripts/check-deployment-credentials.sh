#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

environment=""
require_pki_migration=false
require_product_pki=false
require_deployment_identity=false
require_video_cloud_ready=false
product_pki_cloud_id=""
product_pki_cloud_supplied=false
arguments=()
inputs=("$@")
for ((index = 0; index < ${#inputs[@]}; index++)); do
  argument="${inputs[$index]}"
  case "$argument" in
    --require-pki-migration) require_pki_migration=true ;;
    --require-product-pki) require_product_pki=true ;;
    --require-deployment-identity) require_deployment_identity=true ;;
    --require-video-cloud-ready) require_video_cloud_ready=true ;;
    --product-pki-cloud-id)
      product_pki_cloud_supplied=true
      if ((index + 1 >= ${#inputs[@]})); then
        echo "--product-pki-cloud-id requires a Cloud UUID" >&2
        exit 2
      fi
      product_pki_cloud_id="${inputs[$((index + 1))]}"
      ((index += 1))
      ;;
    --product-pki-cloud-id=*) product_pki_cloud_supplied=true; product_pki_cloud_id="${argument#*=}" ;;
    *) arguments+=("$argument") ;;
  esac
done
if [[ "$product_pki_cloud_supplied" == true && -z "$product_pki_cloud_id" ]]; then
  echo "--product-pki-cloud-id requires a Cloud UUID" >&2
  exit 2
fi
if [[ "$product_pki_cloud_supplied" == true && "$require_product_pki" != true ]]; then
  echo "--product-pki-cloud-id requires --require-product-pki" >&2
  exit 2
fi
for ((index = 0; index < ${#arguments[@]}; index++)); do
  case "${arguments[$index]}" in
    --environment)
      if ((index + 1 < ${#arguments[@]})); then
        environment="${arguments[$((index + 1))]}"
      fi
      ;;
    --environment=*)
      environment="${arguments[$index]#--environment=}"
      ;;
  esac
done

if [[ -n "$environment" ]]; then
  # Provider credentials can all be valid while workload identities held on
  # PVCs are detached from the PKI registry after a database restore/rebuild.
  # Keep this read-only verification in the standard deployment check so that
  # such a stack is a NO-GO before any rollout starts.
  secret_args=(--environment "$environment")
  if [[ "$require_pki_migration" == true ]]; then
    secret_args+=(--require-pki-migration)
  fi
  if [[ "$require_product_pki" == true ]]; then
    secret_args+=(--require-product-pki)
  fi
  if [[ -n "$product_pki_cloud_id" ]]; then
    secret_args+=(--product-pki-cloud-id "$product_pki_cloud_id")
  fi
  if [[ "$require_deployment_identity" == true ]]; then
    secret_args+=(--require-deployment-identity)
  fi
  go run "$ROOT/scripts/go/rtk-cloud" -- secrets verify "${secret_args[@]}"
  if [[ "$require_video_cloud_ready" == true ]]; then
    if [[ ! "$environment" =~ ^[a-z0-9-]+$ ]]; then
      echo "invalid environment for workload readiness check" >&2
      exit 2
    fi
    stack_file="$ROOT/cloud_env/$environment/runtime/env/stack.env"
    stack_name="$(awk -F= '$1 == "CLOUD_STACK_NAME" { print $2; exit }' "$stack_file")"
    if [[ ! "$stack_name" =~ ^[a-z0-9-]+$ ]]; then
      echo "missing or invalid CLOUD_STACK_NAME in $stack_file" >&2
      exit 2
    fi
    kubeconfig="$HOME/.config/rtk_cloud/$environment/kube/kubeconfig.yaml"
    if [[ ! -f "$kubeconfig" ]]; then
      echo "missing kubeconfig for $environment workload readiness check" >&2
      exit 2
    fi
    kubectl="${RTK_CLOUD_KUBECTL:-kubectl}"
    for target in \
      "$stack_name-platform/statefulset/fleet-valkey" \
      "$stack_name-observability/deployment/video-cloud-prometheus" \
      "$stack_name-video-cloud/deployment/video-cloud-api" \
      "$stack_name-video-cloud/statefulset/mqtt" \
      "$stack_name-video-cloud/deployment/video-cloud-logingester" \
      "$stack_name-video-cloud/deployment/video-cloud-mqttusage"; do
      namespace="${target%%/*}"
      resource="${target#*/}"
      echo "Checking $namespace/$resource readiness"
      "$kubectl" --kubeconfig "$kubeconfig" -n "$namespace" rollout status "$resource" --timeout=5s
    done
  fi
elif [[ "$require_pki_migration" == true || "$require_product_pki" == true || "$require_deployment_identity" == true || "$require_video_cloud_ready" == true || -n "$product_pki_cloud_id" ]]; then
  echo "PKI qualification flags require --environment" >&2
  exit 2
fi

exec go run "$ROOT/scripts/go/rtk-cloud" -- deployment credentials-check --workspace "$ROOT" "${arguments[@]}"
