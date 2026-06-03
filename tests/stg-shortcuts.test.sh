#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

FAKE_BIN="$TMP/bin"
GO_LOG="$TMP/go.log"
mkdir -p "$FAKE_BIN"

cat > "$FAKE_BIN/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$GO_LOG"
exit 0
SH
chmod +x "$FAKE_BIN/go"

GO_LOG="$GO_LOG" RTK_CLOUD_GO="$FAKE_BIN/go" "$ROOT/bin/stg.sh" devices RTK 100 --force
grep -F -- 'run ./scripts/go/rtk-cloud -- generate-load-devices --env-root '"$ROOT"'/cloud_env/staging --prefix rtk --count 100 --force' "$GO_LOG" >/dev/null

: > "$GO_LOG"
GO_LOG="$GO_LOG" RTK_CLOUD_GO="$FAKE_BIN/go" "$ROOT/bin/stg.sh" devices 100 --force
grep -F -- 'run ./scripts/go/rtk-cloud -- generate-load-devices --env-root '"$ROOT"'/cloud_env/staging --count 100 --force' "$GO_LOG" >/dev/null
