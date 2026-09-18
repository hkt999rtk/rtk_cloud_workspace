#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$TMP/bin" "$TMP/workspace/cloud_env/staging" "$TMP/workspace/scripts"
printf 'CLOUD_STACK_NAME=video-cloud-staging\n' >"$TMP/workspace/cloud_env/staging/deployment.env"
printf 'test\n' >"$TMP/kubeconfig"
cp "$ROOT/scripts/check-certissuer-app-mtls.sh" "$TMP/workspace/scripts/check-certissuer-app-mtls.sh"

cat >"$TMP/bin/kubectl" <<'SH'
#!/bin/sh
case " $* " in
  *" get pod "*) printf 'account-manager-0' ;;
  *" exec "*) printf '%s' "$FAKE_CERTISSUER_RESPONSE" ;;
  *) exit 2 ;;
esac
SH
chmod +x "$TMP/bin/kubectl" "$TMP/workspace/scripts/check-certissuer-app-mtls.sh"

export PATH="$TMP/bin:$PATH"
export KUBECONFIG="$TMP/kubeconfig"

export FAKE_CERTISSUER_RESPONSE='HTTP/1.1 400 Bad Request
Content-Type: application/json

{"error":{"code":"user_id_required","message":"user_id is required"}}'
"$TMP/workspace/scripts/check-certissuer-app-mtls.sh" staging | grep -q '^PASS:'

export FAKE_CERTISSUER_RESPONSE='HTTP/1.1 401 Unauthorized
Content-Type: application/json

{"error":{"code":"mtls_required","message":"client certificate required"}}'
if "$TMP/workspace/scripts/check-certissuer-app-mtls.sh" staging >/dev/null 2>&1; then
	printf 'expected mTLS authorization failure\n' >&2
	exit 1
fi

printf 'check-certissuer-app-mtls tests: PASS\n'
