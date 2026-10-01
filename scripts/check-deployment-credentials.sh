#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

environment=""
require_pki_migration=false
require_product_pki=false
require_deployment_identity=false
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
elif [[ "$require_pki_migration" == true || "$require_product_pki" == true || "$require_deployment_identity" == true || -n "$product_pki_cloud_id" ]]; then
  echo "PKI qualification flags require --environment" >&2
  exit 2
fi

exec go run "$ROOT/scripts/go/rtk-cloud" -- deployment credentials-check --workspace "$ROOT" "${arguments[@]}"
