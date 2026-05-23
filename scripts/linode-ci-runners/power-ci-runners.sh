#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT_DIR/scripts/linode-ci-runners/runner-specs.sh"
load_runner_specs
LINODE_API_BASE="${LINODE_API_BASE:-https://api.linode.com/v4}"
WORKSPACE="${WORKSPACE:-$ROOT_DIR}"

usage() {
  cat <<'USAGE'
Usage:
  scripts/linode-ci-runners/power-ci-runners.sh start|stop|status

Starts, shuts down, or lists the Linode VMs that host repo-scoped CI runners.
This script does not create VMs; run provision-ci-runners.sh first.
USAGE
}

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
load_env_file() {
  local file="$1"
  if [[ -f "$file" ]]; then
    set -a
    # shellcheck disable=SC1090
    . "$file"
    set +a
  fi
}

load_env_file "$WORKSPACE/.secrets/shared/linode/env/ci-runners.env"
need curl
need jq
[[ -n "${LINODE_TOKEN:-}" ]] || die "LINODE_TOKEN is required"

action="${1:-}"
case "$action" in
  start|stop|status) ;;
  -h|--help) usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

api() {
  local method="$1" path="$2" data="${3:-}"
  if [[ -n "$data" ]]; then
    curl -fsS -X "$method" "$LINODE_API_BASE$path" \
      -H "Authorization: Bearer $LINODE_TOKEN" \
      -H 'Content-Type: application/json' \
      --data-binary "$data"
  else
    curl -fsS -X "$method" "$LINODE_API_BASE$path" \
      -H "Authorization: Bearer $LINODE_TOKEN" \
      -H 'Content-Type: application/json'
  fi
}

linode_by_label() {
  local label="$1"
  api GET "/linode/instances?page_size=500" | jq -c --arg label "$label" '.data[] | select(.label == $label)' | head -n 1
}

declare -A SEEN_HOSTS=()
for spec in "${RUNNER_SPECS[@]}"; do
  IFS='|' read -r host_label _runner_name repo _type custom_label <<<"$spec"
  if [[ -n "${SEEN_HOSTS[$host_label]:-}" ]]; then
    continue
  fi
  SEEN_HOSTS[$host_label]=1
  vm="$(linode_by_label "$host_label" || true)"
  if [[ -z "$vm" ]]; then
    printf '%s\t%s\tmissing\n' "$host_label" "$repo"
    continue
  fi
  id="$(jq -r '.id' <<<"$vm")"
  status="$(jq -r '.status' <<<"$vm")"
  ipv4="$(jq -r '.ipv4[0] // ""' <<<"$vm")"
  case "$action" in
    start)
      if [[ "$status" == "running" ]]; then
        printf '%s\talready-running\t%s\n' "$host_label" "$ipv4"
      else
        api POST "/linode/instances/$id/boot" '{}' >/dev/null
        printf '%s\tboot-requested\t%s\n' "$host_label" "$ipv4"
      fi
      ;;
    stop)
      if [[ "$status" == "offline" ]]; then
        printf '%s\talready-offline\t%s\n' "$host_label" "$ipv4"
      else
        api POST "/linode/instances/$id/shutdown" '{}' >/dev/null
        printf '%s\tshutdown-requested\t%s\n' "$host_label" "$ipv4"
      fi
      ;;
    status)
      printf '%s\t%s\t%s\n' "$host_label" "$status" "$ipv4"
      ;;
  esac
done
