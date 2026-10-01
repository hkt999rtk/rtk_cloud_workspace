#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
if [ -z "${RTK_CLOUD_KUBECONFIG:-}${RTK_CLOUD_LKE_KUBECONFIG:-}${CLOUD_STAGING_K8S_KUBECONFIG:-}${KUBECONFIG:-}" ]; then
  protected_kubeconfig="${RTK_CLOUD_CONFIG_ROOT:-$HOME/.config/rtk_cloud}/staging/kube/kubeconfig.yaml"
  if [ -s "$protected_kubeconfig" ]; then
    RTK_CLOUD_KUBECONFIG="$protected_kubeconfig"
    export RTK_CLOUD_KUBECONFIG
  fi
fi
exec go run "$ROOT/scripts/go/rtk-cloud" -- staging-acceptance --workspace "$ROOT" "$@"
