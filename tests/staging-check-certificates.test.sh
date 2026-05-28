#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
mkdir -p \
	"$ENV_ROOT/certificates/video-cloud-staging.example.com" \
	"$ENV_ROOT/certificates/account-manager.video-cloud-staging.example.com" \
	"$ENV_ROOT/certificates/admin.video-cloud-staging.example.com" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/services/cloud-admin"

make_cert() {
	local domain="$1"
	local dir="$2"
	local days="$3"
	local san="${4:-DNS:$domain}"
	openssl req -x509 -newkey rsa:2048 -sha256 -days "$days" -nodes \
		-subj "/CN=$domain" \
		-addext "subjectAltName=$san" \
		-keyout "$dir/privkey.pem" \
		-out "$dir/fullchain.pem" >/dev/null 2>&1
}

make_cert \
	video-cloud-staging.example.com \
	"$ENV_ROOT/certificates/video-cloud-staging.example.com" \
	30 \
	"DNS:video-cloud-staging.example.com,DNS:certissuer.video-cloud-staging.example.com"
make_cert \
	account-manager.video-cloud-staging.example.com \
	"$ENV_ROOT/certificates/account-manager.video-cloud-staging.example.com" \
	30
make_cert \
	admin.video-cloud-staging.example.com \
	"$ENV_ROOT/certificates/admin.video-cloud-staging.example.com" \
	30

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_AM'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
EOF_AM
cat > "$ENV_ROOT/services/cloud-admin/admin-staging.env" <<'EOF_ADMIN'
ADMIN_LINODE_DOMAIN=admin.video-cloud-staging.example.com
EOF_ADMIN

JSON_OUT="$TMP/pass.json"
"$ROOT/scripts/cloud-check-certificates.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--dns-root-domain example.com \
	--skip-live \
	--json > "$JSON_OUT"

jq -e '.status == "pass"' "$JSON_OUT" >/dev/null
jq -e '.results | length == 4' "$JSON_OUT" >/dev/null
jq -e '[.results[].status] | all(. == "pass")' "$JSON_OUT" >/dev/null

make_cert \
	admin.video-cloud-staging.example.com \
	"$ENV_ROOT/certificates/admin.video-cloud-staging.example.com" \
	1

FAIL_OUT="$TMP/fail.txt"
if "$ROOT/scripts/cloud-check-certificates.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--dns-root-domain example.com \
	--min-valid-days 7 \
	--skip-live > "$FAIL_OUT"; then
	echo "expected soon-expiring certificate to fail" >&2
	exit 1
fi
grep -F 'cloud_certificates status=fail' "$FAIL_OUT" >/dev/null
grep -F 'expires within 7 days' "$FAIL_OUT" >/dev/null

ERR="$TMP/missing-env-root.err"
if "$ROOT/scripts/cloud-check-certificates.sh" \
	--workspace "$WORKSPACE" \
	--skip-live > "$TMP/missing-env-root.out" 2> "$ERR"; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$ERR" >/dev/null
