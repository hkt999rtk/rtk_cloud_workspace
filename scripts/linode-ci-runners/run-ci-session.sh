#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/linode-ci-runners/run-ci-session.sh \
    [--account-run-id RUN_ID] \
    [--admin-run-id RUN_ID] \
    [--frontend-run-id RUN_ID] \
    [--client-run-id RUN_ID] \
    [--logger-run-id RUN_ID] \
    [--rerun true|false] \
    [--shutdown-policy always|on-success|never] \
    [--smoke-only true|false]

Boots the shared Linode Linux CI runner VM, waits for repo-scoped GitHub
runners to become online, optionally reruns the selected GitHub Actions runs,
watches them to completion, archives run metadata/artifacts to Linode Object
Storage, and shuts the VM down according to the shutdown policy.

With --smoke-only true, the script only boots the shared Linux CI VM, waits for
GitHub runners to become online, and shuts the VM down. It does not require run
ids and does not archive artifacts.
USAGE
}

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
account_run_id=""
admin_run_id=""
frontend_run_id=""
client_run_id=""
logger_run_id=""
rerun="true"
shutdown_policy="always"
smoke_only="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --account-run-id) account_run_id="$2"; shift 2 ;;
    --admin-run-id) admin_run_id="$2"; shift 2 ;;
    --frontend-run-id) frontend_run_id="$2"; shift 2 ;;
    --client-run-id) client_run_id="$2"; shift 2 ;;
    --logger-run-id) logger_run_id="$2"; shift 2 ;;
    --rerun) rerun="$2"; shift 2 ;;
    --shutdown-policy) shutdown_policy="$2"; shift 2 ;;
    --smoke-only) smoke_only="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$rerun" in true|false) ;; *) echo "--rerun must be true or false" >&2; exit 2 ;; esac
case "$shutdown_policy" in always|on-success|never) ;; *) echo "--shutdown-policy must be always, on-success, or never" >&2; exit 2 ;; esac
case "$smoke_only" in true|false) ;; *) echo "--smoke-only must be true or false" >&2; exit 2 ;; esac

if [[ "$smoke_only" == "false" && -z "$account_run_id" && -z "$admin_run_id" && -z "$frontend_run_id" && -z "$client_run_id" && -z "$logger_run_id" ]]; then
  echo "at least one run id is required" >&2
  usage >&2
  exit 2
fi

: "${LINODE_TOKEN:?LINODE_TOKEN is required}"
: "${LINODE_OBJ_BUCKET:=}"
: "${LINODE_OBJ_ENDPOINT:=}"
command -v gh >/dev/null 2>&1 || { echo "gh is required" >&2; exit 1; }

if [[ "$smoke_only" == "false" ]]; then
  : "${LINODE_OBJ_BUCKET:?LINODE_OBJ_BUCKET is required}"
  : "${LINODE_OBJ_ENDPOINT:?LINODE_OBJ_ENDPOINT is required}"
fi

overall=0

shutdown_runners() {
  "$ROOT_DIR/scripts/linode-ci-runners/power-ci-runners.sh" stop || true
}

if [[ "$shutdown_policy" == "always" ]]; then
  trap shutdown_runners EXIT
fi

"$ROOT_DIR/scripts/linode-ci-runners/power-ci-runners.sh" start
"$ROOT_DIR/scripts/linode-ci-runners/wait-runners-online.sh"

if [[ "$smoke_only" == "true" ]]; then
  echo "[linode-ci-session] smoke-only lifecycle completed"
  if [[ "$shutdown_policy" == "always" ]]; then
    trap - EXIT
    shutdown_runners
  elif [[ "$shutdown_policy" == "on-success" ]]; then
    shutdown_runners
  fi
  exit 0
fi

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
watch_and_archive hkt999rtk/rtk_cloud_frontend "$frontend_run_id"
watch_and_archive hkt999rtk/rtk_cloud_client "$client_run_id"
watch_and_archive hkt999rtk/rtk_cloud_logger "$logger_run_id"

if [[ "$shutdown_policy" == "on-success" && "$overall" -eq 0 ]]; then
  shutdown_runners
elif [[ "$shutdown_policy" == "always" ]]; then
  trap - EXIT
  shutdown_runners
fi

exit "$overall"
