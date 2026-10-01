#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

environment=""
require_pki_migration=false
require_product_pki=false
require_deployment_identity=false
require_video_cloud_ready=false
require_video_cloud_image=false
require_billable_logging_ready=false
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
    --require-video-cloud-image) require_video_cloud_image=true; require_video_cloud_ready=true ;;
    --require-billable-logging-ready) require_billable_logging_ready=true; require_video_cloud_ready=true ;;
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
    environment_root="${RTK_CLOUD_ENV_ROOT:-$ROOT/cloud_env/$environment/runtime}"
    stack_file="$environment_root/env/stack.env"
    stack_name="$(awk -F= '$1 == "CLOUD_STACK_NAME" { print $2; exit }' "$stack_file")"
    if [[ ! "$stack_name" =~ ^[a-z0-9-]+$ ]]; then
      echo "missing or invalid CLOUD_STACK_NAME in $stack_file" >&2
      exit 2
    fi
    config_root="${RTK_CLOUD_CONFIG_ROOT:-$HOME/.config/rtk_cloud}"
    kubeconfig="$config_root/$environment/kube/kubeconfig.yaml"
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
    if [[ "$require_video_cloud_image" == true ]]; then
      image_file="$config_root/$environment/operator/env/LKE_VIDEO_CLOUD_IMAGE"
      if [[ ! -s "$image_file" ]]; then
        echo "missing pinned Video Cloud image: $image_file" >&2
        exit 2
      fi
      expected_image="$(<"$image_file")"
      if [[ -z "$expected_image" || "$expected_image" == *$'\n'* ]]; then
        echo "invalid pinned Video Cloud image: $image_file" >&2
        exit 2
      fi
      video_namespace="$stack_name-video-cloud"
      deployed_images="$("$kubectl" --kubeconfig "$kubeconfig" -n "$video_namespace" get deployments -o 'jsonpath={range .items[*]}{.metadata.name}{"\t"}{.spec.template.spec.containers[0].image}{"\n"}{end}')"
      seen='|'
      while IFS=$'\t' read -r name image; do
        case "$name" in
          video-cloud-api|video-cloud-cleaner|video-cloud-clipverifier|video-cloud-statistics|video-cloud-metricsexporter|video-cloud-turnregistry|video-cloud-logingester|video-cloud-mqttusage|video-cloud-mqttfoundation|video-cloud-shadowworker|video-cloud-webrtcservice|video-cloud-videostorage|video-cloud-otaregistrar|video-cloud-otaservice)
            seen+="$name|"
            if [[ "$image" != "$expected_image" ]]; then
              echo "image drift: $video_namespace/$name has $image; expected $expected_image" >&2
              exit 1
            fi
            ;;
        esac
      done <<< "$deployed_images"
      for name in video-cloud-api video-cloud-cleaner video-cloud-clipverifier video-cloud-statistics video-cloud-metricsexporter video-cloud-turnregistry video-cloud-logingester video-cloud-mqttusage; do
        if [[ "$seen" != *"|$name|"* ]]; then
          echo "missing required Video Cloud deployment: $video_namespace/$name" >&2
          exit 1
        fi
      done
      echo "Video Cloud deployments match pinned image: $expected_image"
    fi
    if [[ "$require_billable_logging_ready" == true ]]; then
      encoded_gate="$("$kubectl" --kubeconfig "$kubeconfig" -n "$stack_name-account-manager" get secret account-manager-runtime -o 'jsonpath={.data.ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES}')"
      if [[ -z "$encoded_gate" || "$(printf '%s' "$encoded_gate" | base64 -d)" != true ]]; then
        echo "billable logging NO-GO: Account Manager Product service writes are disabled" >&2
        exit 1
      fi
      registry_state="$("$kubectl" --kubeconfig "$kubeconfig" -n "$stack_name-platform" exec statefulset/postgresql -- psql -U postgres -d rtk_account_manager -At -c "SELECT (SELECT count(*) FROM device_item_profiles p WHERE NOT EXISTS (SELECT 1 FROM product_service_grants g WHERE g.product_id=p.id)), COALESCE((SELECT status FROM platform_services WHERE environment='$environment' AND service_id='logger'),'missing')")"
      if [[ "$registry_state" != '0|active' ]]; then
        echo "billable logging NO-GO: missing Product grants / Logger catalog status = $registry_state (required 0|active)" >&2
        exit 1
      fi
      echo "Billable logging Product grants and Logger catalog are ready"
    fi
  fi
elif [[ "$require_pki_migration" == true || "$require_product_pki" == true || "$require_deployment_identity" == true || "$require_video_cloud_ready" == true || "$require_video_cloud_image" == true || "$require_billable_logging_ready" == true || -n "$product_pki_cloud_id" ]]; then
  echo "PKI qualification flags require --environment" >&2
  exit 2
fi

exec go run "$ROOT/scripts/go/rtk-cloud" -- deployment credentials-check --workspace "$ROOT" "${arguments[@]}"
