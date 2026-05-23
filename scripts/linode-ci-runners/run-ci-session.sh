#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/linode-ci-runners/run-ci-session.sh \
    [--account-run-id RUN_ID] \
    [--admin-run-id RUN_ID] \
    [--video-run-id RUN_ID] \
    [--rerun true|false] \
    [--shutdown-policy always|on-success|never]

Boots the dedicated Linode CI runner VMs, waits for GitHub runners to become
online, optionally reruns the selected GitHub Actions runs, watches them to
completion, archives run metadata/artifacts to Linode Object Storage, and shuts
VMs down according to the shutdown policy.
USAGE
}

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
account_run_id=""
admin_run_id=""
video_run_id=""
rerun="true"
shutdown_policy="always"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --account-run-id) account_run_id="$2"; shift 2 ;;
    --admin-run-id) admin_run_id="$2"; shift 2 ;;
    --video-run-id) video_run_id="$2"; shift 2 ;;
    --rerun) rerun="$2"; shift 2 ;;
    --shutdown-policy) shutdown_policy="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$rerun" in true|false) ;; *) echo "--rerun must be true or false" >&2; exit 2 ;; esac
case "$shutdown_policy" in always|on-success|never) ;; *) echo "--shutdown-policy must be always, on-success, or never" >&2; exit 2 ;; esac

if [[ -z "$account_run_id" && -z "$admin_run_id" && -z "$video_run_id" ]]; then
  echo "at least one run id is required" >&2
  usage >&2
  exit 2
fi

: "${LINODE_TOKEN:?LINODE_TOKEN is required}"
: "${LINODE_OBJ_BUCKET:?LINODE_OBJ_BUCKET is required}"
: "${LINODE_OBJ_ENDPOINT:?LINODE_OBJ_ENDPOINT is required}"
command -v gh >/dev/null 2>&1 || { echo "gh is required" >&2; exit 1; }

overall=0

shutdown_runners() {
  "$ROOT_DIR/scripts/linode-ci-runners/power-ci-runners.sh" stop || true
}

if [[ "$shutdown_policy" == "always" ]]; then
  trap shutdown_runners EXIT
fi

"$ROOT_DIR/scripts/linode-ci-runners/power-ci-runners.sh" start
"$ROOT_DIR/scripts/linode-ci-runners/wait-runners-online.sh"

watch_and_archive() {
  local repo="$1"
  local run_id="$2"
  [[ -n "$run_id" ]] || return 0

  echo "[linode-ci-session] processing $repo run $run_id"
  if [[ "$rerun" == "true" ]]; then
    gh run rerun "$run_id" --repo "$repo"
  fi

  if ! gh run watch "$run_id" --repo "$repo" --exit-status; then
    overall=1
  fi

  if ! "$ROOT_DIR/scripts/linode-ci-runners/archive-ci-artifacts.sh" --repo "$repo" --run-id "$run_id"; then
    overall=1
  fi
}

watch_and_archive hkt999rtk/rtk_account_manager "$account_run_id"
watch_and_archive hkt999rtk/rtk_cloud_admin "$admin_run_id"
watch_and_archive hkt999rtk/rtk_video_cloud "$video_run_id"

if [[ "$shutdown_policy" == "on-success" && "$overall" -eq 0 ]]; then
  shutdown_runners
elif [[ "$shutdown_policy" == "always" ]]; then
  trap - EXIT
  shutdown_runners
fi

exit "$overall"
