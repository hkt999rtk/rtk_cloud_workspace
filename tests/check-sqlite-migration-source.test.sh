#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
: > "$fixture/kubeconfig"
cat > "$fixture/kubectl" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
case " $* " in
  *" get deployment "*)
    if [[ "${FAKE_SQLITE_PVC:-}" == true ]]; then
      printf '%s\n' '{"spec":{"replicas":1,"template":{"spec":{"volumes":[{"name":"sqlite-data"}]}}}}'
    elif [[ "${FAKE_OTHER_MOUNT:-}" == true ]]; then
      printf '%s\n' '{"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"app","volumeMounts":[{"name":"legacy","mountPath":"/app/data"}]}]}}}}'
    else
      printf '%s\n' '{"spec":{"replicas":1,"template":{"spec":{}}}}'
    fi
    ;;
  *" get pods "*)
    if [[ "${FAKE_MULTIPLE_PODS:-}" == true ]]; then
      printf '%s\n' '{"items":[{"metadata":{"name":"one","uid":"source-1"}},{"metadata":{"name":"two","uid":"source-2"}}]}'
    else
      printf '%s\n' '{"items":[{"metadata":{"name":"one","uid":"source-1"}}]}'
    fi
    ;;
  *" exec "*)
    printf '%s\n' '-rw------- 1 10001 999 1234 Sep 28 00:00 /app/data/test.db'
    ;;
  *) exit 1 ;;
esac
FAKE
chmod +x "$fixture/kubectl"
export PATH="$fixture:$PATH"

"$root/scripts/check-sqlite-migration-source.sh" --stack video-cloud-staging --kubeconfig "$fixture/kubeconfig" > "$fixture/output"
[[ "$(grep -c 'Source UID: source-1' "$fixture/output")" -eq 2 ]]

if FAKE_MULTIPLE_PODS=true "$root/scripts/check-sqlite-migration-source.sh" --stack video-cloud-staging --kubeconfig "$fixture/kubeconfig" > "$fixture/output" 2> "$fixture/error"; then
  printf 'multiple source Pods were accepted\n' >&2
  exit 1
fi
grep -q 'expected exactly one source Pod' "$fixture/error"

if FAKE_SQLITE_PVC=true "$root/scripts/check-sqlite-migration-source.sh" --stack video-cloud-staging --kubeconfig "$fixture/kubeconfig" > "$fixture/output" 2> "$fixture/error"; then
  printf 'existing SQLite PVC was accepted as a container-layer source\n' >&2
  exit 1
fi
grep -q 'without a SQLite data mount' "$fixture/error"

if FAKE_OTHER_MOUNT=true "$root/scripts/check-sqlite-migration-source.sh" --stack video-cloud-staging --kubeconfig "$fixture/kubeconfig" > "$fixture/output" 2> "$fixture/error"; then
  printf 'data mount with a different name was accepted\n' >&2
  exit 1
fi
grep -q 'without a SQLite data mount' "$fixture/error"

printf 'SQLite migration source inventory safety checks passed\n'
