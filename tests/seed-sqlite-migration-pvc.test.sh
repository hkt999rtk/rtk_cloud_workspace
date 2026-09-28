#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d)"
fixture="$(cd "$fixture" && pwd -P)"
trap 'rm -rf "$fixture"' EXIT
mkdir -m 700 "$fixture/source" "$fixture/archive" "$fixture/target" "$fixture/bin"
printf 'SQLite test fixture\n' > "$fixture/source/rtk-cloud-admin.db"
printf 'encrypted archive fixture\n' > "$fixture/archive/admin.age"
chmod 600 "$fixture/source/rtk-cloud-admin.db" "$fixture/archive/admin.age"
: > "$fixture/kubeconfig"
archive_hash="$(shasum -a 256 "$fixture/archive/admin.age" | awk '{print $1}')"
uid=11111111-2222-3333-4444-555555555555
digest="$(printf '%064d' 0)"
"$root/scripts/render-sqlite-migration-helper.sh" --stack video-cloud-staging \
  --workload cloud-admin --image "example.test/admin@sha256:$digest" \
  --image-pull-secret registry-pull > "$fixture/helper.yaml"
for expected in 'namespace: video-cloud-staging-admin' 'claimName: cloud-admin-sqlite-data' \
  'runAsUser: 10001' 'runAsGroup: 999' 'fsGroup: 999' 'mountPath: /migration' \
  'name: registry-pull'; do
  grep -q "$expected" "$fixture/helper.yaml"
done
"$root/scripts/render-sqlite-migration-helper.sh" --stack video-cloud-staging \
  --workload frontend --image "example.test/frontend@sha256:$digest" > "$fixture/frontend-helper.yaml"
grep -q 'claimName: frontend-sqlite-data' "$fixture/frontend-helper.yaml"
grep -q 'runAsUser: 100' "$fixture/frontend-helper.yaml"
grep -q 'fsGroup: 101' "$fixture/frontend-helper.yaml"
if "$root/scripts/render-sqlite-migration-helper.sh" --stack video-cloud-staging \
  --workload cloud-admin --image example.test/admin:mutable > "$fixture/rejected.yaml" 2> /dev/null; then
  printf 'mutable helper image was accepted\n' >&2; exit 1
fi

cat > "$fixture/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" get deployment "*)
    printf '%s\n' '{"spec":{"replicas":1,"template":{"spec":{}}}}'
    exit 0 ;;
  *" get pods "*)
    count=0
    [[ ! -f "$FAKE_POD_COUNT" ]] || count="$(cat "$FAKE_POD_COUNT")"
    count="$((count+1))"
    printf '%s' "$count" > "$FAKE_POD_COUNT"
    uid=11111111-2222-3333-4444-555555555555
    if [[ "${FAKE_CHANGE_UID:-}" == true && "$count" -gt 2 ]]; then uid=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee; fi
    printf '{"items":[{"metadata":{"name":"source-pod","uid":"%s"},"status":{"phase":"Running"}}]}\n' "$uid"
    exit 0 ;;
  *" get pvc "*)
    printf '%s\n' '{"metadata":{"annotations":{}},"status":{"phase":"Bound"}}'
    exit 0 ;;
  *" get pod migration-helper "*)
    mount=/migration
    if [[ "${FAKE_BAD_MOUNT:-}" == true ]]; then mount=/wrong; fi
    uid=10001; group=999; pvc=cloud-admin-sqlite-data; image=admin
    if [[ "${FAKE_WORKLOAD:-}" == frontend ]]; then uid=100; group=101; pvc=frontend-sqlite-data; image=frontend; fi
    if [[ "${FAKE_BAD_IMAGE:-}" == true ]]; then image=unreviewed; fi
    if [[ "${FAKE_BAD_PVC:-}" == true ]]; then pvc=other-sqlite-data; fi
    printf '{"metadata":{"uid":"helper-uid","labels":{"app.kubernetes.io/name":"sqlite-migration-helper","rtk.realtek.com/stack":"video-cloud-staging"}},"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":true}]},"spec":{"automountServiceAccountToken":false,"securityContext":{"fsGroup":%s},"volumes":[{"name":"sqlite-data","persistentVolumeClaim":{"claimName":"%s"}}],"containers":[{"name":"app","image":"example.test/%s@sha256:%s","securityContext":{"runAsUser":%s,"runAsGroup":%s},"volumeMounts":[{"name":"sqlite-data","mountPath":"%s"}]}]}}\n' "$group" "$pvc" "$image" "$FAKE_DIGEST" "$uid" "$group" "$mount"
    exit 0 ;;
esac
while (($#)); do
  if [[ "$1" == exec ]]; then break; fi
  shift
done
[[ "$1" == exec ]] || exit 2
shift
if [[ "$1" == -i ]]; then shift; fi
pod="$1"
shift
[[ "$1" == -c && "$2" == app && "$3" == -- ]] || exit 2
shift 3
if [[ "$pod" == source-pod ]]; then
  if [[ "$1" == sha256sum ]]; then
    if [[ "${FAKE_BAD_SOURCE_HASH:-}" == true ]]; then
      printf '%064d  %s\n' 0 "$2"
    else
      printf '%s  %s\n' "$(shasum -a 256 "$FAKE_SOURCE/${2##*/}" | awk '{print $1}')" "$2"
    fi
  elif [[ "$1" == sh ]]; then
    if [[ "${FAKE_WORKLOAD:-}" == frontend ]]; then
      printf '/data/analytics.db\n/data/connectplus.db\n'
    else
      printf '/app/data/rtk-cloud-admin.db\n'
    fi
  else exit 2; fi
elif [[ "$pod" == migration-helper ]]; then
  if [[ "$1" == tar ]]; then
    tar -C "$FAKE_TARGET" -xf -
    if [[ "${FAKE_CORRUPT_TARGET:-}" == true ]]; then printf 'changed' >> "$FAKE_TARGET/rtk-cloud-admin.db"; fi
  elif [[ "$1" == sha256sum ]]; then
    printf '%s  %s\n' "$(shasum -a 256 "$FAKE_TARGET/${2##*/}" | awk '{print $1}')" "$2"
  elif [[ "$1" == sh ]]; then
    if [[ "$3" == *'find /migration'* ]]; then
      for path in "$FAKE_TARGET"/*; do
        [[ -e "$path" ]] || continue
        printf '/migration/%s\n' "${path##*/}"
      done
    else
      name="${*: -1}"
      name="${name##*/}"
      [[ -f "$FAKE_TARGET/$name" && ! -L "$FAKE_TARGET/$name" && -r "$FAKE_TARGET/$name" && -w "$FAKE_TARGET/$name" ]]
    fi
  else exit 2; fi
else exit 2; fi
FAKE
chmod +x "$fixture/bin/kubectl"
export PATH="$fixture/bin:$PATH"
export FAKE_SOURCE="$fixture/source" FAKE_TARGET="$fixture/target"
export FAKE_POD_COUNT="$fixture/pod-count" FAKE_DIGEST="$digest"

args=(--stack video-cloud-staging --confirm-stack video-cloud-staging
  --kubeconfig "$fixture/kubeconfig" --workload cloud-admin
  --source-pod-uid "$uid" --source-dir "$fixture/source"
  --archive "$fixture/archive/admin.age" --archive-sha256 "$archive_hash"
  --helper-pod migration-helper --helper-image "example.test/admin@sha256:$digest")

"$root/scripts/seed-sqlite-migration-pvc.sh" "${args[@]}" > "$fixture/result"
grep -q 'PVC bytes verified' "$fixture/result"
cmp "$fixture/source/rtk-cloud-admin.db" "$fixture/target/rtk-cloud-admin.db"

rm -f "$fixture/target/rtk-cloud-admin.db" "$FAKE_POD_COUNT"
printf 'already populated\n' > "$fixture/target/existing.db"
if "$root/scripts/seed-sqlite-migration-pvc.sh" "${args[@]}" > "$fixture/result" 2> "$fixture/error"; then
  printf 'nonempty PVC was accepted\n' >&2; exit 1
fi
grep -q 'target PVC is not empty' "$fixture/error"
rm "$fixture/target/existing.db" "$FAKE_POD_COUNT"

for scenario in FAKE_BAD_MOUNT FAKE_BAD_IMAGE FAKE_BAD_PVC FAKE_BAD_SOURCE_HASH FAKE_CORRUPT_TARGET FAKE_CHANGE_UID; do
  export "$scenario=true"
  if "$root/scripts/seed-sqlite-migration-pvc.sh" "${args[@]}" > "$fixture/result" 2> "$fixture/error"; then
    printf '%s was accepted\n' "$scenario" >&2; exit 1
  fi
  unset "$scenario"
  rm -f "$fixture/target/rtk-cloud-admin.db" "$FAKE_POD_COUNT"
done

mkdir -m 700 "$fixture/frontend-source"
printf 'frontend analytics SQLite\n' > "$fixture/frontend-source/analytics.db"
printf 'frontend connection SQLite\n' > "$fixture/frontend-source/connectplus.db"
chmod 600 "$fixture/frontend-source"/*.db
printf 'encrypted frontend archive fixture\n' > "$fixture/archive/frontend.age"
chmod 600 "$fixture/archive/frontend.age"
frontend_hash="$(shasum -a 256 "$fixture/archive/frontend.age" | awk '{print $1}')"
frontend_args=("${args[@]}")
frontend_args[7]=frontend
frontend_args[11]="$fixture/frontend-source"
frontend_args[13]="$fixture/archive/frontend.age"
frontend_args[15]="$frontend_hash"
frontend_args[19]="example.test/frontend@sha256:$digest"
export FAKE_WORKLOAD=frontend FAKE_SOURCE="$fixture/frontend-source"
"$root/scripts/seed-sqlite-migration-pvc.sh" "${frontend_args[@]}" > "$fixture/result"
cmp "$fixture/frontend-source/analytics.db" "$fixture/target/analytics.db"
cmp "$fixture/frontend-source/connectplus.db" "$fixture/target/connectplus.db"
unset FAKE_WORKLOAD
export FAKE_SOURCE="$fixture/source"
rm -f "$fixture/target"/*.db "$FAKE_POD_COUNT"

if "$root/scripts/seed-sqlite-migration-pvc.sh" "${args[@]:0:2}" --confirm-stack another-stack "${args[@]:4}" > "$fixture/result" 2> "$fixture/error"; then
  printf 'wrong stack confirmation was accepted\n' >&2; exit 1
fi

printf 'SQLite PVC seed safety checks passed\n'
