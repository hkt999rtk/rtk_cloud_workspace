#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
SECRETS="$ENV_ROOT"
FAKE_BIN="$TMP/bin"
mkdir -p "$FAKE_BIN" "$ENV_ROOT/env"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
*"/linode/instances?page_size=500"*)
	cat <<'JSON'
{"data":[{"id":1,"label":"video-cloud-staging-edge","region":"us-sea","type":"g6-standard-2","status":"running","ipv4":["203.0.113.5"],"tags":["video-cloud-staging","role:edge"]}]}
JSON
	;;
*"/networking/firewalls?page_size=500"*)
	cat <<'JSON'
{"data":[{"id":101,"label":"video-cloud-staging-edge","status":"enabled","tags":["video-cloud-staging","role:edge"]}]}
JSON
	;;
*"/vpcs?page_size=500"*)
	cat <<'JSON'
{"data":[{"id":9001,"label":"video-cloud-staging-vpc","region":"us-sea"}]}
JSON
	;;
*)
	printf 'unexpected curl: %s\n' "$*" >&2
	exit 1
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_ENV'
LINODE_TOKEN=test-token
EOF_ENV

if PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/cloud-provision.sh" \
	--workspace "$WORKSPACE" >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

OUT="$TMP/out.txt"
PATH="$FAKE_BIN:$PATH" "$ROOT/scripts/cloud-provision.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" >"$OUT"

grep -F 'Target instances:' "$OUT" >/dev/null
grep -F 'video-cloud-staging-edge' "$OUT" >/dev/null
grep -F 'Intended resources:' "$OUT" >/dev/null
