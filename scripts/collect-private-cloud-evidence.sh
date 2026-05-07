#!/usr/bin/env sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ENVIRONMENT=${RTK_EVIDENCE_ENVIRONMENT:-evaluation}
OUTPUT_ROOT=${RTK_EVIDENCE_OUTPUT_DIR:-$ROOT_DIR/evidence}
TIMESTAMP=${RTK_EVIDENCE_TIMESTAMP:-$(date -u +%Y%m%dT%H%M%SZ)}
STRICT=${RTK_EVIDENCE_STRICT:-0}
RUN_SERVICE_COLLECTORS=${RTK_EVIDENCE_RUN_SERVICE_COLLECTORS:-0}
PACKAGE_TARBALL=${RTK_EVIDENCE_TARBALL:-1}

case "$ENVIRONMENT" in
  *[!A-Za-z0-9._-]* | "" | .* | *..*)
    echo "invalid RTK_EVIDENCE_ENVIRONMENT: use only letters, digits, dot, underscore, and dash" >&2
    exit 1
    ;;
esac
case "$TIMESTAMP" in
  *[!A-Za-z0-9TZ._-]* | "" | .* | *..*)
    echo "invalid RTK_EVIDENCE_TIMESTAMP: use only letters, digits, T, Z, dot, underscore, and dash" >&2
    exit 1
    ;;
esac
case "$STRICT" in 0|1) ;; *) echo "RTK_EVIDENCE_STRICT must be 0 or 1" >&2; exit 1 ;; esac
case "$RUN_SERVICE_COLLECTORS" in 0|1) ;; *) echo "RTK_EVIDENCE_RUN_SERVICE_COLLECTORS must be 0 or 1" >&2; exit 1 ;; esac
case "$PACKAGE_TARBALL" in 0|1) ;; *) echo "RTK_EVIDENCE_TARBALL must be 0 or 1" >&2; exit 1 ;; esac

ARTIFACT_NAME="realtek-connect-plus-evidence-$ENVIRONMENT-$TIMESTAMP"
ARTIFACT_DIR="$OUTPUT_ROOT/$ARTIFACT_NAME"
STATUS_FILE="$ARTIFACT_DIR/status.txt"
SUMMARY_FILE="$ARTIFACT_DIR/summary.md"

umask 077
mkdir -p "$ARTIFACT_DIR" "$ARTIFACT_DIR/services" "$ARTIFACT_DIR/health" "$ARTIFACT_DIR/metrics" "$ARTIFACT_DIR/brokers" "$ARTIFACT_DIR/backups"
: > "$STATUS_FILE"

failures=0
skips=0
passes=0

redact_sensitive() {
  sed -E \
    -e 's#([A-Za-z][A-Za-z0-9+.-]*://)[^/@[:space:]]+@#\1<redacted>@#g' \
    -e 's#([?&][^=[:space:]]*[Tt][Oo][Kk][Ee][Nn][^=[:space:]]*=)[^&[:space:]]+#\1<redacted>#g' \
    -e 's#([?&][^=[:space:]]*[Ss][Ee][Cc][Rr][Ee][Tt][^=[:space:]]*=)[^&[:space:]]+#\1<redacted>#g' \
    -e 's#([?&][^=[:space:]]*[Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd][^=[:space:]]*=)[^&[:space:]]+#\1<redacted>#g' \
    -e 's#([?&][^=[:space:]]*[Pp][Aa][Ss][Ss][Ww][Dd][^=[:space:]]*=)[^&[:space:]]+#\1<redacted>#g' \
    -e 's#([?&][^=[:space:]]*[Kk][Ee][Yy][^=[:space:]]*=)[^&[:space:]]+#\1<redacted>#g' \
    -e 's#([?&][^=[:space:]]*[Dd][Ss][Nn][^=[:space:]]*=)[^&[:space:]]+#\1<redacted>#g' \
    -e 's#([?&][^=[:space:]]*[Aa][Uu][Tt][Hh][^=[:space:]]*=)[^&[:space:]]+#\1<redacted>#g' \
    -e 's#(Bearer )[A-Za-z0-9._~+/=-]+#\1<redacted>#g' \
    -e 's#([A-Za-z0-9_]*[Tt][Oo][Kk][Ee][Nn][A-Za-z0-9_]*=)[^[:space:]]+#\1<redacted>#g' \
    -e 's#([A-Za-z0-9_]*[Ss][Ee][Cc][Rr][Ee][Tt][A-Za-z0-9_]*=)[^[:space:]]+#\1<redacted>#g' \
    -e 's#([A-Za-z0-9_]*[Pp][Aa][Ss][Ss][Ww][Oo][Rr][Dd][A-Za-z0-9_]*=)[^[:space:]]+#\1<redacted>#g' \
    -e 's#([A-Za-z0-9_]*[Pp][Aa][Ss][Ss][Ww][Dd][A-Za-z0-9_]*=)[^[:space:]]+#\1<redacted>#g' \
    -e 's#([A-Za-z0-9_]*[Dd][Ss][Nn][A-Za-z0-9_]*=)[^[:space:]]+#\1<redacted>#g' \
    -e 's#(-----BEGIN [A-Z ]*PRIVATE KEY-----).*#\1 <redacted>#g'
}

safe_name() {
  printf '%s' "$1" | tr '/: ' '---' | tr -cd 'A-Za-z0-9._-'
}

record() {
  status=$1
  area=$2
  reason=$3
  case "$status" in
    PASS) passes=$((passes + 1)) ;;
    FAIL) failures=$((failures + 1)) ;;
    SKIP) skips=$((skips + 1)) ;;
    *) echo "invalid status $status" >&2; exit 1 ;;
  esac
  printf '%s\t%s\t%s\n' "$status" "$area" "$reason" >> "$STATUS_FILE"
}

capture_text() {
  out=$1
  shift
  {
    printf '$'
    for arg in "$@"; do printf ' %s' "$arg"; done
    printf '\n\n'
    "$@" 2>&1 || printf '\ncommand_exit=%s\n' "$?"
  } | redact_sensitive > "$out"
}

write_manifest() {
  {
    printf 'created_utc=%s\n' "$TIMESTAMP"
    printf 'environment=%s\n' "$ENVIRONMENT"
    printf 'workspace_root=%s\n' "$ROOT_DIR"
    printf 'run_service_collectors=%s\n' "$RUN_SERVICE_COLLECTORS"
    printf 'strict=%s\n' "$STRICT"
    printf 'generated_by=%s\n' "$(id -un 2>/dev/null || printf unknown)"
    printf 'hostname=%s\n' "$(hostname 2>/dev/null || printf unknown)"
    printf 'uname=%s\n' "$(uname -a 2>/dev/null || printf unknown)"
  } > "$ARTIFACT_DIR/manifest.txt"
  record PASS manifest "wrote product evidence manifest"
}

collect_commits() {
  out="$ARTIFACT_DIR/services/commits.tsv"
  printf 'path\tcommit\tbranch\tdirty\n' > "$out"
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    if [ ! -d "$ROOT_DIR/$path/.git" ] && ! git -C "$ROOT_DIR/$path" rev-parse --git-dir >/dev/null 2>&1; then
      printf '%s\tmissing\tunknown\tunknown\n' "$path" >> "$out"
      record FAIL "commit:$path" "repository path missing"
      continue
    fi
    commit=$(git -C "$ROOT_DIR/$path" rev-parse HEAD 2>/dev/null || printf unknown)
    branch=$(git -C "$ROOT_DIR/$path" rev-parse --abbrev-ref HEAD 2>/dev/null || printf unknown)
    if [ -n "$(git -C "$ROOT_DIR/$path" status --porcelain --untracked-files=no 2>/dev/null || true)" ]; then
      dirty=yes
    else
      dirty=no
    fi
    printf '%s\t%s\t%s\t%s\n' "$path" "$commit" "$branch" "$dirty" >> "$out"
    if [ "$dirty" = yes ]; then
      record FAIL "commit:$path" "repository has uncommitted changes"
    else
      record PASS "commit:$path" "pinned commit captured"
    fi
  done <<'PATHS'
.
repos/rtk_cloud_contracts_doc
repos/rtk_account_manager
repos/rtk_video_cloud
repos/rtk_cloud_client
repos/rtk_cloud_frontend
repos/rtk_cloud_admin
PATHS
}

collect_versions() {
  out="$ARTIFACT_DIR/services/versions.txt"
  : > "$out"
  for path in repos/rtk_account_manager repos/rtk_video_cloud repos/rtk_cloud_client repos/rtk_cloud_frontend repos/rtk_cloud_admin; do
    printf '== %s ==\n' "$path" >> "$out"
    if [ ! -d "$ROOT_DIR/$path" ]; then
      printf 'SKIP missing repository\n\n' >> "$out"
      record SKIP "version:$path" "repository path missing"
      continue
    fi
    if [ -f "$ROOT_DIR/$path/package.json" ]; then
      sed -n 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/package.json version=\1/p' "$ROOT_DIR/$path/package.json" | head -n 1 >> "$out" || true
    fi
    if [ -f "$ROOT_DIR/$path/go.mod" ]; then
      sed -n '1p' "$ROOT_DIR/$path/go.mod" >> "$out" || true
    fi
    if [ -f "$ROOT_DIR/$path/VERSION" ]; then
      printf 'VERSION=' >> "$out"
      cat "$ROOT_DIR/$path/VERSION" >> "$out"
    fi
    printf '\n' >> "$out"
    record PASS "version:$path" "local version metadata captured when present"
  done
}

collect_url() {
  name=$1
  url=$2
  target_dir=$3
  required=$4
  file="$target_dir/$(safe_name "$name").txt"
  if [ -z "$url" ]; then
    if [ "$required" = required ]; then
      record FAIL "$name" "URL not configured"
    else
      record SKIP "$name" "URL not configured"
    fi
    printf 'status=SKIP\nreason=URL not configured\n' > "$file"
    return
  fi
  if ! command -v curl >/dev/null 2>&1; then
    record SKIP "$name" "curl unavailable"
    printf 'status=SKIP\nreason=curl unavailable\nurl=%s\n' "$url" | redact_sensitive > "$file"
    return
  fi
  tmp="$file.tmp"
  if curl -fsS --max-time 10 "$url" > "$tmp" 2>&1; then
    redact_sensitive < "$tmp" > "$file"
    rm -f "$tmp"
    record PASS "$name" "HTTP probe succeeded"
  else
    code=$?
    redact_sensitive < "$tmp" > "$file" 2>/dev/null || true
    rm -f "$tmp"
    record FAIL "$name" "HTTP probe failed with curl exit $code"
  fi
}

collect_health() {
  collect_url frontend_health "${RTK_EVIDENCE_FRONTEND_HEALTH_URL:-}" "$ARTIFACT_DIR/health" optional
  collect_url admin_health "${RTK_EVIDENCE_ADMIN_HEALTH_URL:-}" "$ARTIFACT_DIR/health" optional
  collect_url account_manager_health "${RTK_EVIDENCE_ACCOUNT_MANAGER_HEALTH_URL:-}" "$ARTIFACT_DIR/health" optional
  collect_url video_cloud_health "${RTK_EVIDENCE_VIDEO_CLOUD_HEALTH_URL:-}" "$ARTIFACT_DIR/health" optional
}

collect_metrics() {
  urls=${RTK_EVIDENCE_METRICS_URLS:-}
  if [ -z "$urls" ]; then
    record SKIP metrics "RTK_EVIDENCE_METRICS_URLS not configured"
    printf 'status=SKIP\nreason=RTK_EVIDENCE_METRICS_URLS not configured\n' > "$ARTIFACT_DIR/metrics/metrics.txt"
    return
  fi
  for url in $urls; do
    collect_url "metrics:$url" "$url" "$ARTIFACT_DIR/metrics" optional
  done
}

collect_brokers() {
  {
    printf 'emqx_required=%s\n' "${RTK_EVIDENCE_EMQX_REQUIRED:-auto}"
    printf 'emqx_status_url=%s\n' "${RTK_EVIDENCE_EMQX_STATUS_URL:-}"
    printf 'nats_required=%s\n' "${RTK_EVIDENCE_NATS_REQUIRED:-auto}"
    printf 'nats_url=%s\n' "${RTK_EVIDENCE_NATS_URL:-}"
  } | redact_sensitive > "$ARTIFACT_DIR/brokers/config.txt"

  collect_url emqx_status "${RTK_EVIDENCE_EMQX_STATUS_URL:-}" "$ARTIFACT_DIR/brokers" optional

  if [ -n "${RTK_EVIDENCE_NATS_URL:-}" ]; then
    record PASS nats_config "NATS JetStream URL configured; smoke test is service/operator-owned"
  else
    record SKIP nats_config "RTK_EVIDENCE_NATS_URL not configured; cross-service channel may be disabled"
  fi

  if [ -n "${RTK_EVIDENCE_BROKER_SMOKE_REF:-}" ]; then
    printf '%s\n' "$RTK_EVIDENCE_BROKER_SMOKE_REF" | redact_sensitive > "$ARTIFACT_DIR/brokers/smoke-reference.txt"
    record PASS broker_smoke_reference "broker smoke evidence reference captured"
  else
    printf 'status=SKIP\nreason=RTK_EVIDENCE_BROKER_SMOKE_REF not configured\n' > "$ARTIFACT_DIR/brokers/smoke-reference.txt"
    record SKIP broker_smoke_reference "broker smoke evidence reference not configured"
  fi
}

collect_backups() {
  out="$ARTIFACT_DIR/backups/references.txt"
  {
    printf 'postgres_backup_ref=%s\n' "${RTK_EVIDENCE_POSTGRES_BACKUP_REF:-}"
    printf 'object_storage_backup_ref=%s\n' "${RTK_EVIDENCE_OBJECT_STORAGE_BACKUP_REF:-}"
    printf 'frontend_sqlite_backup_ref=%s\n' "${RTK_EVIDENCE_FRONTEND_BACKUP_REF:-}"
    printf 'emqx_backup_ref=%s\n' "${RTK_EVIDENCE_EMQX_BACKUP_REF:-}"
    printf 'nats_backup_ref=%s\n' "${RTK_EVIDENCE_NATS_BACKUP_REF:-}"
  } | redact_sensitive > "$out"

  found=0
  for var in RTK_EVIDENCE_POSTGRES_BACKUP_REF RTK_EVIDENCE_OBJECT_STORAGE_BACKUP_REF RTK_EVIDENCE_FRONTEND_BACKUP_REF RTK_EVIDENCE_EMQX_BACKUP_REF RTK_EVIDENCE_NATS_BACKUP_REF; do
    eval value=\${$var:-}
    [ -n "$value" ] && found=1
  done
  if [ "$found" = 1 ]; then
    record PASS backup_refs "backup evidence references captured"
  else
    record SKIP backup_refs "no backup evidence references configured"
  fi
}

collect_service_artifacts() {
  if [ "$RUN_SERVICE_COLLECTORS" != 1 ]; then
    record SKIP service_collectors "RTK_EVIDENCE_RUN_SERVICE_COLLECTORS=0"
    return
  fi

  vc_script="$ROOT_DIR/repos/rtk_video_cloud/deploy/collect-readiness-evidence.sh"
  if [ -x "$vc_script" ]; then
    vc_status=0
    VIDEO_CLOUD_EVIDENCE_OUTPUT_DIR="$ARTIFACT_DIR/services" "$vc_script" > "$ARTIFACT_DIR/services/video-cloud-collector.txt" 2>&1 || vc_status=$?
    redact_sensitive < "$ARTIFACT_DIR/services/video-cloud-collector.txt" > "$ARTIFACT_DIR/services/video-cloud-collector.redacted.txt"
    rm -f "$ARTIFACT_DIR/services/video-cloud-collector.txt"
    if [ "$vc_status" -eq 0 ]; then
      record PASS video_cloud_collector "video cloud evidence collector completed"
    else
      record FAIL video_cloud_collector "service collector exited non-zero"
    fi
  else
    record SKIP video_cloud_collector "video cloud collector unavailable"
  fi

  for name in account_manager admin frontend; do
    var=$(printf 'RTK_EVIDENCE_%s_COLLECTOR_CMD' "$(printf '%s' "$name" | tr '[:lower:]' '[:upper:]')")
    eval cmd=\${$var:-}
    if [ -z "$cmd" ]; then
      record SKIP "${name}_collector" "$var not configured"
      continue
    fi
    collector_status=0
    sh -c "$cmd" > "$ARTIFACT_DIR/services/${name}-collector.txt" 2>&1 || collector_status=$?
    redact_sensitive < "$ARTIFACT_DIR/services/${name}-collector.txt" > "$ARTIFACT_DIR/services/${name}-collector.redacted.txt"
    rm -f "$ARTIFACT_DIR/services/${name}-collector.txt"
    if [ "$collector_status" -eq 0 ]; then
      record PASS "${name}_collector" "collector command completed"
    else
      record FAIL "${name}_collector" "collector command exited non-zero"
    fi
  done
}

write_summary() {
  {
    printf '# Realtek Connect+ Private Cloud Evidence Summary\n\n'
    printf '%s\n' "- Environment: \`$ENVIRONMENT\`"
    printf '%s\n' "- Created UTC: \`$TIMESTAMP\`"
    printf '%s\n' "- Pass: \`$passes\`"
    printf '%s\n' "- Fail: \`$failures\`"
    printf '%s\n' "- Skip: \`$skips\`"
    printf '\n## Status\n\n'
    awk -F '\t' '{ printf "- `%s` `%s` - %s\n", $1, $2, $3 }' "$STATUS_FILE"
  } > "$SUMMARY_FILE"
}

package_artifact() {
  if [ "$PACKAGE_TARBALL" != 1 ]; then
    return
  fi
  tarball="$OUTPUT_ROOT/$ARTIFACT_NAME.tar.gz"
  (cd "$OUTPUT_ROOT" && tar -czf "$tarball" "$ARTIFACT_NAME")
  printf 'tarball=%s\n' "$tarball" > "$ARTIFACT_DIR/tarball.txt"
}

main() {
  cd "$ROOT_DIR"
  write_manifest
  collect_commits
  collect_versions
  collect_health
  collect_metrics
  collect_brokers
  collect_backups
  collect_service_artifacts
  write_summary
  package_artifact

  echo "artifact_dir=$ARTIFACT_DIR"
  if [ "$PACKAGE_TARBALL" = 1 ]; then
    echo "tarball=$OUTPUT_ROOT/$ARTIFACT_NAME.tar.gz"
  fi
  echo "passes=$passes"
  echo "failures=$failures"
  echo "skips=$skips"

  if [ "$STRICT" = 1 ] && [ "$failures" -gt 0 ]; then
    exit 1
  fi
}

main "$@"
