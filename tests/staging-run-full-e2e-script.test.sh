#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin"
GO_LOG="$TMP/go.log"
cat > "$TMP/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GO_LOG"
case "$*" in
	*" -- run-staging-e2e "*)
		printf 'cloud-staging-e2e-test plan\n'
		;;
	*" -- staging-reset-k8s "*)
		printf 'cloud-staging-reset-k8s plan\n'
		;;
	*" -- staging-provision "*)
		printf 'cloud-staging-provision plan\n'
		;;
	*" -- staging-acceptance "*)
		printf 'cloud-staging-e2e-test plan\nphase: acceptance\n'
		;;
	*)
		echo "unexpected go invocation: $*" >&2
		exit 1
		;;
esac
SH
chmod +x "$TMP/bin/go"
export GO_LOG

PATH="$TMP/bin:$PATH" "$ROOT/scripts/run-staging-e2e.sh" --plan >"$TMP/full-plan.out"
grep -F 'cloud-staging-e2e-test plan' "$TMP/full-plan.out" >/dev/null
grep -F 'run '"$ROOT"'/scripts/go/rtk-cloud -- run-staging-e2e --workspace '"$ROOT"' --plan' "$GO_LOG" >/dev/null

: > "$GO_LOG"
PATH="$TMP/bin:$PATH" "$ROOT/scripts/reset-staging-k8s.sh" --plan >"$TMP/reset-plan.out"
grep -F 'cloud-staging-reset-k8s plan' "$TMP/reset-plan.out" >/dev/null
grep -F 'run '"$ROOT"'/scripts/go/rtk-cloud -- staging-reset-k8s --workspace '"$ROOT"' --plan' "$GO_LOG" >/dev/null

: > "$GO_LOG"
PATH="$TMP/bin:$PATH" "$ROOT/scripts/provision-staging.sh" --plan >"$TMP/provision-plan.out"
grep -F 'cloud-staging-provision plan' "$TMP/provision-plan.out" >/dev/null
grep -F 'run '"$ROOT"'/scripts/go/rtk-cloud -- staging-provision --workspace '"$ROOT"' --plan' "$GO_LOG" >/dev/null

: > "$GO_LOG"
PATH="$TMP/bin:$PATH" "$ROOT/scripts/run-staging-acceptance.sh" --plan >"$TMP/acceptance-plan.out"
grep -F 'phase: acceptance' "$TMP/acceptance-plan.out" >/dev/null
grep -F 'run '"$ROOT"'/scripts/go/rtk-cloud -- staging-acceptance --workspace '"$ROOT"' --plan' "$GO_LOG" >/dev/null
