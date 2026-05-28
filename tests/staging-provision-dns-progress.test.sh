#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
FAKE_BIN="$TMP/bin"
COUNT_FILE="$TMP/dig.count"
GO_ARGS_FILE="$TMP/go.args"
mkdir -p \
	"$FAKE_BIN" \
	"$WORKSPACE/repos/rtk_video_cloud/tools/godaddy-dns" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/state" \
	"$ENV_ROOT/services/cloud-admin" \
	"$ENV_ROOT/env"

cat > "$FAKE_BIN/go" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$GO_ARGS_FILE"
exit 0
SH
chmod +x "$FAKE_BIN/go"

cat > "$FAKE_BIN/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
chmod +x "$FAKE_BIN/sleep"

cat > "$FAKE_BIN/dig" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *" NS "* || "$*" == NS\ * ]]; then
	printf 'ns1.example.test.\n'
	exit 0
fi
count=0
if [[ -f "$DIG_COUNT_FILE" ]]; then
	count="$(cat "$DIG_COUNT_FILE")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$DIG_COUNT_FILE"
if (( count <= 2 )); then
	printf '198.51.100.10\n'
else
	printf '203.0.113.5\n'
fi
SH
chmod +x "$FAKE_BIN/dig"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
GODADDY_KEY=test-key
GODADDY_SECRET=test-secret
EOF_ENV

cat > "$ENV_ROOT/state/video-cloud-staging.state.json" <<'EOF_STATE'
{
  "instances": {
    "edge": {"public_ipv4": "203.0.113.5"}
  }
}
EOF_STATE

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.5
EOF_AM

cat > "$ENV_ROOT/state/cloud-admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_PUBLIC_IPV4=203.0.113.5
EOF_ADMIN

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" DIG_COUNT_FILE="$COUNT_FILE" GO_ARGS_FILE="$GO_ARGS_FILE" "$ROOT/scripts/staging-provision.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--dns-wait-ttl 600 \
	--dns-final-ttl 600 \
	--dns >"$OUT" 2>&1

grep -F 'waiting DNS attempt 1/70: video-cloud-staging.realtekconnect.com expected=203.0.113.5 google=198.51.100.10 auth=198.51.100.10' "$OUT" >/dev/null
grep -F 'DNS converged: video-cloud-staging.realtekconnect.com -> 203.0.113.5' "$OUT" >/dev/null
grep -F 'restoring DNS final TTL: 600' "$OUT" >/dev/null
if [[ "$(grep -F -c -- 'records upsert realtekconnect.com --type A --name video-cloud-staging --data 203.0.113.5 --ttl 600' "$GO_ARGS_FILE")" -lt 2 ]]; then
	echo "expected wait and final TTL upserts for video-cloud-staging" >&2
	exit 1
fi

if "$ROOT/scripts/staging-provision.sh" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" --dns --dns-wait-ttl 60 >"$TMP/invalid.out" 2>&1; then
	echo "expected --dns-wait-ttl 60 to fail before GoDaddy API call" >&2
	exit 1
fi
grep -F -- '--dns-wait-ttl must be >= 600 for GoDaddy DNS records' "$TMP/invalid.out" >/dev/null

if "$ROOT/scripts/staging-provision.sh" --workspace "$WORKSPACE" --env-root "$ENV_ROOT" --dns --dns-wait-max-seconds abc >"$TMP/invalid-wait-max.out" 2>&1; then
	echo "expected invalid --dns-wait-max-seconds to fail" >&2
	exit 1
fi
grep -F -- '--dns-wait-max-seconds must be a positive integer' "$TMP/invalid-wait-max.out" >/dev/null
