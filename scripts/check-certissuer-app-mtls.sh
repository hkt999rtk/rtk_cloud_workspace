#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENVIRONMENT="${1:-staging}"
HELPER="$ROOT/scripts/check-certissuer-app-helper.pl"
TIMEOUT_SECONDS="${RTK_CERTISSUER_CHECK_TIMEOUT_SECONDS:-20}"
fail() { printf 'FAIL [%s]: %s\n' "$1" "$2" >&2; exit 1; }

[[ "$ENVIRONMENT" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || fail CONFIG_INVALID 'invalid deployment environment name'
[[ "$TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ ]] && ((TIMEOUT_SECONDS <= 300)) || fail CONFIG_INVALID 'probe timeout must be between 1 and 300 seconds'
ENV_FILE="$ROOT/cloud_env/$ENVIRONMENT/environment.env"
[[ -f "$ENV_FILE" && -f "$ROOT/cloud_env/$ENVIRONMENT/deployment.env" ]] || fail ENVIRONMENT_MISSING "deployment environment does not exist: $ENVIRONMENT"
command -v kubectl >/dev/null || fail TOOL_MISSING 'kubectl is required'
command -v perl >/dev/null || fail TOOL_MISSING 'Perl with JSON::PP is required'
perl -MJSON::PP -e 1 2>/dev/null || fail TOOL_MISSING 'Perl JSON::PP is required'
[[ -f "$HELPER" && -f "$ROOT/scripts/check-certissuer-app-socket.pl" ]] || fail TOOL_MISSING 'CertIssuer probe helper is missing'

STACK="$(awk -F= '$1 == "CLOUD_STACK_NAME" {sub(/^[^=]*=/, ""); sub(/\r$/, ""); print; exit}' "$ENV_FILE")"
[[ "$STACK" == "video-cloud-$ENVIRONMENT" ]] || fail CONFIG_INVALID 'environment identity has a missing or mismatched CLOUD_STACK_NAME'
CONFIG_ROOT="${RTK_CLOUD_CONFIG_ROOT:-$HOME/.config/rtk_cloud}"
KUBECONFIG_PATH="${RTK_CLOUD_KUBECONFIG:-${RTK_CLOUD_LKE_KUBECONFIG:-${KUBECONFIG:-$CONFIG_ROOT/$ENVIRONMENT/kube/kubeconfig.yaml}}}"
[[ -s "$KUBECONFIG_PATH" ]] || fail KUBECONFIG_MISSING "kubeconfig is missing for $ENVIRONMENT"

ACCOUNT_NAMESPACE="$STACK-account-manager"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
run_probe() {
  local stage="$1" status
  shift
  if RESULT="$(perl "$HELPER" bounded "$TIMEOUT_SECONDS" kubectl --kubeconfig "$KUBECONFIG_PATH" \
    --request-timeout="${TIMEOUT_SECONDS}s" -n "$ACCOUNT_NAMESPACE" "$@" 2>"$TMP/stderr")"; then
    return 0
  else
    status=$?
    perl "$HELPER" failure "$stage" "$status" <"$TMP/stderr" >&2
    return 1
  fi
}

run_probe pod-list get pods -l app.kubernetes.io/name=account-manager -o json
POD="$(printf '%s' "$RESULT" | perl "$HELPER" pod)"
run_probe socket-detection exec "$POD" -c app -- sh -c 'printf "%s" "${APP_CERT_ISSUER_SOCKET:-}"'
SOCKET="$RESULT"

# The incomplete request must reach validation without issuing a certificate.
if [[ -n "$SOCKET" ]]; then
  MODE=socket
  run_probe socket exec -i "$POD" -c app -- perl - <"$ROOT/scripts/check-certissuer-app-socket.pl"
else
  MODE=tls
  run_probe tls exec "$POD" -c app -- sh -c '
set -eu
case "${APP_CERT_ISSUER_BASE_URL:-}" in
  https://*) host="${APP_CERT_ISSUER_BASE_URL#https://}"; host="${host%%/*}" ;;
  *) printf "CERTISSUER_CONFIG_INVALID\n" >&2; exit 2 ;;
esac
case "$host" in ""|*[!a-zA-Z0-9.:-]*) printf "CERTISSUER_CONFIG_INVALID\n" >&2; exit 2 ;; esac
name="${host%%:*}"
case "$host" in *:*) address="$host" ;; *) address="$host:443" ;; esac
if [ -z "${APP_CERT_ISSUER_CLIENT_CERT:-}" ] || [ -z "${APP_CERT_ISSUER_CLIENT_KEY:-}" ] || [ -z "${APP_CERT_ISSUER_CA_FILE:-}" ]; then
  printf "CERTISSUER_CONFIG_INVALID\n" >&2; exit 2
fi
{
  printf "POST /v1/certificates/app/issue HTTP/1.1\r\n"
  printf "Host: %s\r\n" "$host"
  printf "Content-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"
} | timeout 12 openssl s_client -quiet -connect "$address" -servername "$name" \
  -verify_return_error -verify_hostname "$name" \
  -cert "$APP_CERT_ISSUER_CLIENT_CERT" -key "$APP_CERT_ISSUER_CLIENT_KEY" \
  -CAfile "$APP_CERT_ISSUER_CA_FILE"
'
fi

printf '%s' "$RESULT" | perl "$HELPER" response "$MODE"
