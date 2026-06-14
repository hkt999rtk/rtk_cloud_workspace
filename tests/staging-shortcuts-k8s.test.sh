#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

GO_STUB="$TMP/go"
LOG="$TMP/go.log"
cat > "$GO_STUB" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$STG_SHORTCUT_LOG"
SH
chmod +x "$GO_STUB"
export STG_SHORTCUT_LOG="$LOG"

RTK_CLOUD_GO="$GO_STUB" RTK_CLOUD_STAGING_ENV_ROOT="$TMP/env" "$ROOT/bin/stg.sh" provision --confirm video-cloud-staging
grep -F 'run ./scripts/go/rtk-cloud -- provision-k8s --env-root '"$TMP/env"' --confirm video-cloud-staging' "$LOG" >/dev/null

for retired in deploy deploy-local ssh rm-vm; do
	if RTK_CLOUD_GO="$GO_STUB" RTK_CLOUD_STAGING_ENV_ROOT="$TMP/env" "$ROOT/bin/stg.sh" "$retired" >/tmp/stg-shortcut.out 2>/tmp/stg-shortcut.err; then
		echo "expected $retired shortcut to fail" >&2
		exit 1
	fi
	grep -F "staging VM shortcut \"$retired\" has been retired" /tmp/stg-shortcut.err >/dev/null
done

"$ROOT/bin/stg.sh" --help >"$TMP/help.out"
if grep -E '(^|[[:space:]])(rm-vm|deploy-local|update-ssh-whitelist|remove-all-vm)([[:space:]]|$)' "$TMP/help.out" >/dev/null; then
	echo "stg.sh help should not advertise retired VM shortcuts" >&2
	exit 1
fi
