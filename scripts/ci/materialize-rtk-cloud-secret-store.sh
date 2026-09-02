#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
  echo "usage: materialize-rtk-cloud-secret-store.sh ENVIRONMENT BUNDLE_JSON [KUBECONFIG]" >&2
  exit 2
}

[[ $# -ge 2 && $# -le 3 ]] || usage
environment="$1"
bundle="$2"
kubeconfig="${3:-}"
: "${RTK_CLOUD_CONFIG_ROOT:?RTK_CLOUD_CONFIG_ROOT must point inside the job temporary directory}"
: "${RUNNER_TEMP:?RUNNER_TEMP is required}"

case "$environment" in
  *[!a-z0-9-]*|'') echo "invalid environment name" >&2; exit 2 ;;
esac
case "$RTK_CLOUD_CONFIG_ROOT/" in
  "$RUNNER_TEMP"/*) ;;
  *) echo "RTK_CLOUD_CONFIG_ROOT must be inside RUNNER_TEMP" >&2; exit 2 ;;
esac
[[ -f "$bundle" && ! -L "$bundle" ]] || { echo "bundle must be a regular file" >&2; exit 2; }
jq -e '((.operator // {}) | type == "object") and ((.runtime // {}) | type == "object") and ((.files // {}) | type == "object")' "$bundle" >/dev/null

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
go run "$repo_root/scripts/go/rtk-cloud" -- secrets init \
  --environment "$environment" --config-root "$RTK_CLOUD_CONFIG_ROOT" >/dev/null
environment_root="$RTK_CLOUD_CONFIG_ROOT/$environment"

write_object() {
  local object_name="$1" target_dir="$2" key encoded target
  while IFS=$'\t' read -r key encoded; do
    [[ -n "$key" ]] || continue
    case "$key" in
      *'/'*|*'..'*) echo "invalid $object_name key" >&2; exit 2 ;;
    esac
    target="$target_dir/$key"
    [[ ! -e "$target" && ! -L "$target" ]] || { echo "refusing to overwrite secret entry" >&2; exit 2; }
    printf '%s' "$encoded" | openssl base64 -d -A > "$target"
    chmod 0600 "$target"
  done < <(jq -r --arg name "$object_name" '(.[$name] // {}) | to_entries[] | [.key, (.value | @base64)] | @tsv' "$bundle")
}

write_object operator "$environment_root/operator/env"
write_object runtime "$environment_root/runtime"

# New shared credentials are introduced independently from the opaque bundle so
# an existing environment can bootstrap one catalog entry without replacing or
# exposing the rest of its SecretStore. Once the bundle contains the entry, both
# sources must remain identical until the transition variable is retired.
bootstrap_runtime_secret() {
  local id="$1" variable="$2" value="${!2:-}" target="$environment_root/runtime/$1"
  [[ -n "$value" ]] || return 0
  if [[ -e "$target" ]]; then
    [[ ! -L "$target" && -f "$target" ]] || { echo "invalid runtime secret entry: $id" >&2; exit 2; }
    [[ "$(cat "$target")" == "$value" ]] || { echo "runtime secret transition differs from bundle: $id" >&2; exit 2; }
    return 0
  fi
  printf '%s' "$value" > "$target"
  chmod 0600 "$target"
}

bootstrap_runtime_secret mqtt-usage-settlement RTK_CLOUD_MQTT_USAGE_SETTLEMENT_TOKEN

# GitHub-provided job secrets may be injected as environment variables. This
# allowlist is CI-only and does not change deployment credential precedence.
for key in LINODE_TOKEN GHCR_PULL_USERNAME GHCR_PULL_TOKEN GODADDY_KEY GODADDY_SECRET; do
  value="${!key:-}"
  target="$environment_root/operator/env/$key"
  if [[ -n "$value" && ! -e "$target" && ! -L "$target" ]]; then
    printf '%s\n' "$value" > "$target"
    chmod 0600 "$target"
  fi
done

while IFS=$'\t' read -r relative encoded; do
  [[ -n "$relative" ]] || continue
  case "/$relative/" in
    *'/../'*|*'/./'*|'/'*'//'*) echo "invalid secret file path" >&2; exit 2 ;;
  esac
  [[ "$relative" != /* ]] || { echo "secret file path must be relative" >&2; exit 2; }
  target="$environment_root/$relative"
  [[ ! -e "$target" && ! -L "$target" ]] || { echo "refusing to overwrite secret file" >&2; exit 2; }
  mkdir -p "$(dirname "$target")"
  chmod 0700 "$(dirname "$target")"
  printf '%s' "$encoded" | openssl base64 -d -A > "$target"
  chmod 0600 "$target"
done < <(jq -r '(.files // {}) | to_entries[] | [.key, (.value | @base64)] | @tsv' "$bundle")

# Verify the desired SecretStore before installing the kubeconfig.  A full
# K8s-mirror comparison belongs after deployment: doing it here would make a
# new catalog entry or an intentional rotation impossible to bootstrap.
go run "$repo_root/scripts/go/rtk-cloud" -- secrets verify \
  --environment "$environment" --config-root "$RTK_CLOUD_CONFIG_ROOT" >/dev/null

if [[ -n "$kubeconfig" ]]; then
  [[ -f "$kubeconfig" && ! -L "$kubeconfig" ]] || { echo "kubeconfig must be a regular file" >&2; exit 2; }
  install -m 0600 "$kubeconfig" "$environment_root/kube/kubeconfig.yaml"
fi

echo "materialized and content-verified CI SecretStore for $environment"
