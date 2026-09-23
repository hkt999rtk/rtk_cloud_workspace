#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENVIRONMENT="${1:-staging}"
ENV_FILE="$ROOT/cloud_env/$ENVIRONMENT/deployment.env"

if [[ ! -f "$ENV_FILE" ]]; then
	printf 'FAIL: deployment environment does not exist: %s\n' "$ENVIRONMENT" >&2
	exit 1
fi

STACK="$(awk -F= '$1 == "CLOUD_STACK_NAME" {sub(/^[^=]*=/, ""); print; exit}' "$ENV_FILE")"
if [[ -z "$STACK" ]]; then
	STACK="video-cloud-$ENVIRONMENT"
fi

KUBECONFIG_PATH="${RTK_CLOUD_KUBECONFIG:-${RTK_CLOUD_LKE_KUBECONFIG:-${KUBECONFIG:-$HOME/.config/rtk_cloud/$ENVIRONMENT/kube/kubeconfig.yaml}}}"
if [[ ! -s "$KUBECONFIG_PATH" ]]; then
	printf 'FAIL: kubeconfig is missing for %s\n' "$ENVIRONMENT" >&2
	exit 1
fi

ACCOUNT_NAMESPACE="$STACK-account-manager"
POD="$(kubectl --kubeconfig "$KUBECONFIG_PATH" -n "$ACCOUNT_NAMESPACE" get pod \
	-l app.kubernetes.io/name=account-manager -o jsonpath='{.items[0].metadata.name}')"
if [[ -z "$POD" ]]; then
	printf 'FAIL: Account Manager pod was not found\n' >&2
	exit 1
fi

# An authenticated but incomplete request must pass mTLS authorization and fail
# request validation with HTTP 400. It cannot issue or rotate a certificate.
SOCKET="$(kubectl --kubeconfig "$KUBECONFIG_PATH" -n "$ACCOUNT_NAMESPACE" exec "$POD" -c app -- sh -c 'printf "%s" "${APP_CERT_ISSUER_SOCKET:-}"')"
if [[ -n "$SOCKET" ]]; then
	RESPONSE="$(kubectl --kubeconfig "$KUBECONFIG_PATH" -n "$ACCOUNT_NAMESPACE" exec -i "$POD" -c app -- perl - < "$ROOT/scripts/check-certissuer-app-socket.pl")" || {
		printf 'FAIL: managed CertIssuer socket request could not be completed\n' >&2
		exit 1
	}
else
	RESPONSE="$(kubectl --kubeconfig "$KUBECONFIG_PATH" -n "$ACCOUNT_NAMESPACE" exec "$POD" -c app -- sh -c '
set -eu
host="${APP_CERT_ISSUER_BASE_URL#https://}"
host="${host%%/*}"
body="{}"
{
  printf "POST /v1/certificates/app/issue HTTP/1.1\r\n"
  printf "Host: %s\r\n" "${host%%:*}"
  printf "Content-Type: application/json\r\n"
  printf "Content-Length: 2\r\n"
  printf "Connection: close\r\n\r\n%s" "$body"
} | timeout 12 openssl s_client -quiet -connect "$host" -servername "${host%%:*}" \
  -cert "$APP_CERT_ISSUER_CLIENT_CERT" -key "$APP_CERT_ISSUER_CLIENT_KEY" \
  -CAfile "$APP_CERT_ISSUER_CA_FILE" 2>/dev/null
')" || {
		printf 'FAIL: certissuer mTLS request could not be completed\n' >&2
		exit 1
	}
fi

STATUS="$(printf '%s\n' "$RESPONSE" | awk 'NR == 1 {print $2}')"
CODE="$(printf '%s\n' "$RESPONSE" | tr -d '\r\n' | sed -n 's/.*"code":"\([^"]*\)".*/\1/p')"
if [[ "$STATUS" != "400" || "$CODE" != "user_id_required" ]]; then
	printf 'FAIL: certissuer rejected the Account Manager mTLS identity before request validation (HTTP %s, code %s)\n' "${STATUS:-unknown}" "${CODE:-unknown}" >&2
	exit 1
fi

printf 'PASS: certissuer authorized the Account Manager mTLS identity and reached request validation\n'
