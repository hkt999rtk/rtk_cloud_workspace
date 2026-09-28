#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s --stack STACK --confirm-stack STACK --kubeconfig FILE --workload cloud-admin|frontend --source-pod-uid UID --source-dir DIR --archive FILE --archive-sha256 HASH --helper-pod POD --helper-image IMAGE@sha256:DIGEST\n' "$0" >&2
  exit 2
}

stack='' confirm_stack='' kubeconfig='' workload='' source_uid=''
source_dir='' archive='' archive_hash='' helper='' helper_image=''
while (($#)); do
  (($# >= 2)) || usage
  case "$1" in
    --stack) stack="$2" ;;
    --confirm-stack) confirm_stack="$2" ;;
    --kubeconfig) kubeconfig="$2" ;;
    --workload) workload="$2" ;;
    --source-pod-uid) source_uid="$2" ;;
    --source-dir) source_dir="$2" ;;
    --archive) archive="$2" ;;
    --archive-sha256) archive_hash="$2" ;;
    --helper-pod) helper="$2" ;;
    --helper-image) helper_image="$2" ;;
    *) usage ;;
  esac
  shift 2
done

[[ "$stack" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ && "$confirm_stack" == "$stack" ]] || usage
[[ "$source_uid" =~ ^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$ ]] || usage
[[ "$archive_hash" =~ ^[a-fA-F0-9]{64}$ && "$helper" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || usage
[[ "$helper_image" =~ ^[A-Za-z0-9._:/-]+@sha256:[a-fA-F0-9]{64}$ ]] || usage
archive_hash="$(printf '%s' "$archive_hash" | tr 'A-F' 'a-f')"
[[ -f "$kubeconfig" ]] || usage
[[ "$workload" == cloud-admin || "$workload" == frontend ]] || usage
for executable in kubectl jq tar shasum python3; do
  command -v "$executable" >/dev/null || { printf '%s is required\n' "$executable" >&2; exit 2; }
done

# The capture and pack commands use the same no-symlink, private-directory rule.
python3 - "$source_dir" "$archive" <<'PY'
import os, stat, sys
source, archive = map(os.path.abspath, sys.argv[1:])
for path in (source, os.path.dirname(archive)):
    current = path
    while True:
        info = os.lstat(current)
        if stat.S_ISLNK(info.st_mode):
            raise SystemExit('SQLite copy/archive path has a symlink ancestor')
        if current == path and (not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077):
            raise SystemExit('SQLite copy/archive parent must be private 0700')
        parent = os.path.dirname(current)
        if parent == current:
            break
        current = parent
info = os.lstat(archive)
if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
    raise SystemExit('encrypted archive must be a private 0600 regular file')
PY

actual_archive_hash="$(shasum -a 256 -- "$archive" | awk '{print $1}')"
[[ "$actual_archive_hash" == "$archive_hash" ]] || { printf 'encrypted archive hash mismatch\n' >&2; exit 1; }

if [[ "$workload" == cloud-admin ]]; then
  namespace="$stack-admin"; deployment=cloud-admin; source_path=/app/data
  pvc=cloud-admin-sqlite-data; uid=10001; group=999
  required=(rtk-cloud-admin.db)
else
  namespace="$stack-frontend"; deployment=frontend; source_path=/data
  pvc=frontend-sqlite-data; uid=100; group=101
  required=(analytics.db connectplus.db)
fi

shopt -s nullglob dotglob
entries=("$source_dir"/*)
((${#entries[@]} > 0)) || { printf 'private SQLite copy is empty\n' >&2; exit 1; }
names=() hashes=()
for path in "${entries[@]}"; do
  name="${path##*/}"
  [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*\.db(-wal|-shm|-journal)?$ && -f "$path" && ! -L "$path" && -s "$path" ]] || {
    printf 'private SQLite copy contains an unsafe entry\n' >&2; exit 1;
  }
  if [[ "$name" == *.db-* ]]; then
    base="${name%.db-*}.db"
    [[ -f "$source_dir/$base" && ! -L "$source_dir/$base" ]] || {
      printf 'SQLite sidecar has no database\n' >&2; exit 1;
    }
  fi
  names+=("$name")
  hashes+=("$(shasum -a 256 -- "$path" | awk '{print $1}')")
done
for name in "${required[@]}"; do
  [[ -f "$source_dir/$name" ]] || { printf 'required SQLite database missing: %s\n' "$name" >&2; exit 1; }
done

k() { kubectl --kubeconfig "$kubeconfig" -n "$namespace" "$@"; }
source_pod() {
  local pods name uid_found phase
  pods="$(k get pods -l "app.kubernetes.io/name=$deployment" -o json)"
  [[ "$(jq -r '.items | length' <<<"$pods")" == 1 ]] || { printf 'source Pod count changed\n' >&2; return 1; }
  name="$(jq -r '.items[0].metadata.name // empty' <<<"$pods")"
  uid_found="$(jq -r '.items[0].metadata.uid // empty' <<<"$pods")"
  phase="$(jq -r '.items[0].status.phase // empty' <<<"$pods")"
  [[ -n "$name" && "$uid_found" == "$source_uid" && "$phase" == Running ]] || {
    printf 'source Pod UID or Running state changed\n' >&2; return 1;
  }
  printf '%s\n' "$name"
}

deployment_json="$(k get deployment "$deployment" -o json)"
jq -e --arg path "$source_path" '
  .spec.replicas == 1 and
  ([.spec.template.spec.volumes[]? | select(.name == "sqlite-data")] | length) == 0 and
  ([.spec.template.spec.containers[]? | select(.name == "app") | .volumeMounts[]? | select(.mountPath == $path)] | length) == 0
' <<<"$deployment_json" >/dev/null || { printf 'source Deployment is not a single container-layer SQLite writer\n' >&2; exit 1; }

source_pod_name="$(source_pod)"
pvc_json="$(k get pvc "$pvc" -o json)"
jq -e '.status.phase == "Bound" and (.metadata.annotations["rtk.realtek.com/sqlite-copy-sha256"] // "") == "" and (.metadata.annotations["rtk.realtek.com/sqlite-source-pod-uid"] // "") == ""' \
  <<<"$pvc_json" >/dev/null || { printf 'target PVC is not Bound or is already attested\n' >&2; exit 1; }

helper_json="$(k get pod "$helper" -o json)"
helper_uid="$(jq -r '.metadata.uid // empty' <<<"$helper_json")"
[[ -n "$helper_uid" ]] || { printf 'helper Pod UID missing\n' >&2; exit 1; }
jq -e --arg pvc "$pvc" --arg image "$helper_image" --arg stack "$stack" --argjson uid "$uid" --argjson group "$group" '
  .status.phase == "Running" and
  .metadata.labels["app.kubernetes.io/name"] == "sqlite-migration-helper" and
  .metadata.labels["rtk.realtek.com/stack"] == $stack and
  .spec.automountServiceAccountToken == false and
  ([.status.containerStatuses[]? | select(.name == "app" and .ready == true)] | length) == 1 and
  (.spec.containers | length) == 1 and
  (.spec.volumes | length) == 1 and
  (.spec.containers[0].volumeMounts | length) == 1 and
  .spec.containers[0].name == "app" and
  .spec.containers[0].image == $image and
  .spec.containers[0].securityContext.runAsUser == $uid and
  .spec.containers[0].securityContext.runAsGroup == $group and
  .spec.securityContext.fsGroup == $group and
  ([.spec.volumes[]? | select(.name == "sqlite-data" and .persistentVolumeClaim.claimName == $pvc)] | length) == 1 and
  ([.spec.containers[0].volumeMounts[]? | select(.name == "sqlite-data" and .mountPath == "/migration")] | length) == 1
' <<<"$helper_json" >/dev/null || { printf 'helper Pod does not have the reviewed image, identity and target PVC mount\n' >&2; exit 1; }

target_entries="$(k exec "$helper" -c app -- sh -c 'find /migration -mindepth 1 -maxdepth 1 ! -name lost+found -print')"
[[ -z "$target_entries" ]] || { printf 'target PVC is not empty\n' >&2; exit 1; }

expected_list="$(printf '%s\n' "${names[@]}" | LC_ALL=C sort)"
source_list() {
  k exec "$source_pod_name" -c app -- sh -c '
    set -eu
    find "$1" -maxdepth 1 \( -name "*.db" -o -name "*.db-*" \) -print
  ' sh "$source_path" | sed "s#^$source_path/##" | LC_ALL=C sort
}
check_source() {
  [[ "$(source_pod)" == "$source_pod_name" ]] || return 1
  [[ "$(source_list)" == "$expected_list" ]] || { printf 'source SQLite file set changed\n' >&2; return 1; }
  local index remote local_hash
  for index in "${!names[@]}"; do
    remote="$(k exec "$source_pod_name" -c app -- sha256sum "$source_path/${names[index]}")"
    local_hash="$(shasum -a 256 -- "$source_dir/${names[index]}" | awk '{print $1}')"
    [[ "${remote%% *}" == "${hashes[index]}" && "$local_hash" == "${hashes[index]}" ]] || {
      printf 'source or private copy SQLite hash changed: %s\n' "${names[index]}" >&2; return 1;
    }
  done
}
check_source

printf 'Seeding %s/%s from verified private SQLite copy; keep writer fence active.\n' "$namespace" "$pvc"
COPYFILE_DISABLE=1 tar -C "$source_dir" -cf - "${names[@]}" | k exec -i "$helper" -c app -- tar -C /migration -xf -

helper_after="$(k get pod "$helper" -o json)"
[[ "$(jq -r '.metadata.uid // empty' <<<"$helper_after")" == "$helper_uid" ]] || {
  printf 'helper Pod changed during PVC seed\n' >&2; exit 1;
}
target_list="$(k exec "$helper" -c app -- sh -c 'find /migration -mindepth 1 -maxdepth 1 ! -name lost+found -print' | sed 's#^/migration/##' | LC_ALL=C sort)"
[[ "$target_list" == "$expected_list" ]] || { printf 'target PVC file set differs from private copy\n' >&2; exit 1; }
for index in "${!names[@]}"; do
  name="${names[index]}"
  remote="$(k exec "$helper" -c app -- sha256sum "/migration/$name")"
  [[ "${remote%% *}" == "${hashes[index]}" ]] || { printf 'target PVC hash mismatch: %s\n' "$name" >&2; exit 1; }
  k exec "$helper" -c app -- sh -c 'test -f "$1" && test ! -L "$1" && test -r "$1" && test -w "$1"' sh "/migration/$name"
done
check_source
[[ "$(shasum -a 256 -- "$archive" | awk '{print $1}')" == "$archive_hash" ]] || {
  printf 'encrypted archive changed during PVC seed\n' >&2; exit 1;
}
printf 'PVC bytes verified for %s; source Pod UID %s; archive SHA-256 %s.\n' "$workload" "$source_uid" "$archive_hash"
printf 'Keep the writer fence active. Complete integrity/count review and independent restore evidence before annotating or cutting over.\n'
