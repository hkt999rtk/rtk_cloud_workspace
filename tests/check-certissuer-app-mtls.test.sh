#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/workspace/cloud_env/staging" "$TMP/workspace/scripts" "$TMP/config/staging/kube"
printf 'CLOUD_STACK_NAME=video-cloud-staging\n' >"$TMP/workspace/cloud_env/staging/environment.env"
printf 'DEPLOYMENT_ADAPTER=lke\n' >"$TMP/workspace/cloud_env/staging/deployment.env"
printf 'test\n' >"$TMP/config/staging/kube/kubeconfig.yaml"
cp "$ROOT/scripts/check-certissuer-app-mtls.sh" "$ROOT/scripts/check-certissuer-app-socket.pl" \
  "$ROOT/scripts/check-certissuer-app-helper.pl" "$TMP/workspace/scripts/"

cat >"$TMP/bin/kubectl" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_CALLS"
[ "$1" = --kubeconfig ] && [ "$2" = "$FAKE_EXPECTED_KUBECONFIG" ] || exit 91
case " $* " in *' -n video-cloud-staging-account-manager '*) ;; *) exit 97 ;; esac
case " $* " in
  *" get pods "*)
    case "${FAKE_KUBE_FAILURE:-}" in
      timeout) sleep 30 & child=$!; wait "$child" ;;
      forbidden) printf 'Error from server (Forbidden): do-not-leak-this-secret\n' >&2; exit 1 ;;
      *) printf '%s' "$FAKE_PODS" ;;
    esac ;;
  *'APP_CERT_ISSUER_SOCKET:-'*) printf '%s' "${FAKE_SOCKET:-}" ;;
  *" -- perl -"*)
    cat >/dev/null
    case "${FAKE_SOCKET_FAILURE:-}" in
      unavailable) printf 'CertIssuer socket is unavailable do-not-leak-this-secret\n' >&2; exit 1 ;;
      timeout) printf 'CERTISSUER_SOCKET_TIMEOUT do-not-leak-this-secret\n' >&2; exit 1 ;;
      *) printf '%s' "$FAKE_CERTISSUER_RESPONSE" ;;
    esac ;;
  *" -- sh -c "*)
    while [ "$1" != -- ]; do shift; done
    shift
    exec "$@" ;;
  *) exit 92 ;;
esac
SH
cat >"$TMP/bin/openssl" <<'SH'
#!/bin/sh
case " $* " in *' -verify_return_error '*) ;; *) exit 93 ;; esac
case " $* " in *' -verify_hostname issuer.example '*) ;; *) exit 94 ;; esac
case " $* " in *' -connect issuer.example:443 '*) ;; *) exit 95 ;; esac
# The mounted file contains leaf and intermediates; s_client -cert alone sends only the leaf.
case " $* " in *' -cert_chain /fake/client.crt '*) ;; *) printf 'ssl alert unknown ca\n' >&2; exit 1 ;; esac
cat >/dev/null
case "${FAKE_TLS_FAILURE:-}" in
  ca) printf 'verify error:num=20:unable to get local issuer certificate do-not-leak-this-secret\n' >&2; exit 1 ;;
  hostname) printf 'verify error:num=62:hostname mismatch do-not-leak-this-secret\n' >&2; exit 1 ;;
  handshake) printf 'ssl alert certificate required do-not-leak-this-secret\n' >&2; exit 1 ;;
  timeout) exit 124 ;;
  transport) printf 'connect:errno=111 do-not-leak-this-secret\n' >&2; exit 1 ;;
  *) printf '%s' "$FAKE_CERTISSUER_RESPONSE" ;;
esac
SH
cat >"$TMP/bin/timeout" <<'SH'
#!/bin/sh
[ "$1" = 12 ] || exit 96
shift
exec "$@"
SH
chmod +x "$TMP/bin/"* "$TMP/workspace/scripts/check-certissuer-app-mtls.sh"
export PATH="$TMP/bin:$PATH"
# Use the selected environment under RTK_CLOUD_CONFIG_ROOT, regardless of HOME.
unset RTK_CLOUD_KUBECONFIG RTK_CLOUD_LKE_KUBECONFIG KUBECONFIG
export RTK_CLOUD_CONFIG_ROOT="$TMP/config"
export FAKE_EXPECTED_KUBECONFIG="$TMP/config/staging/kube/kubeconfig.yaml"
export FAKE_CALLS="$TMP/calls"
export APP_CERT_ISSUER_BASE_URL=https://issuer.example
export APP_CERT_ISSUER_CLIENT_CERT=/fake/client.crt APP_CERT_ISSUER_CLIENT_KEY=/fake/client.key APP_CERT_ISSUER_CA_FILE=/fake/ca.crt
export FAKE_PODS='{"items":[{"metadata":{"name":"account-manager-0"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}'
GOOD_PODS="$FAKE_PODS"
export FAKE_CERTISSUER_RESPONSE='HTTP/1.1 400 Bad Request
Content-Type: application/json

{"error":{"code":"user_id_required","message":"do-not-leak-this-secret"}}'
GOOD_RESPONSE="$FAKE_CERTISSUER_RESPONSE"
CHECK="$TMP/workspace/scripts/check-certissuer-app-mtls.sh"
checks=0
expect() {
  local status="$1" pattern="$2" actual
  if "$CHECK" staging >"$TMP/output" 2>&1; then actual=0; else actual=$?; fi
  if [[ "$actual" != "$status" ]] || ! grep -Fq "$pattern" "$TMP/output"; then
    printf 'expected status %s with %s; got status %s\n' "$status" "$pattern" "$actual" >&2
    cat "$TMP/output" >&2
    exit 1
  fi
  if grep -Fq 'do-not-leak-this-secret' "$TMP/output"; then
    printf 'probe leaked response or stderr material\n' >&2
    exit 1
  fi
  checks=$((checks + 1))
}

export FAKE_SOCKET=/run/account-pki/private/controller.sock
expect 0 'PASS: managed CertIssuer socket request'
if grep -Fq 'mTLS' "$TMP/output"; then printf 'socket result claimed direct mTLS verification\n' >&2; exit 1; fi
export FAKE_SOCKET=''
expect 0 'PASS: CertIssuer verified TLS endpoint'
# Confirm the explicit override still takes priority over the canonical root.
printf 'override\n' >"$TMP/override-kubeconfig"
export KUBECONFIG="$TMP/override-kubeconfig" FAKE_EXPECTED_KUBECONFIG="$TMP/override-kubeconfig"
expect 0 'PASS:'
unset KUBECONFIG
export FAKE_EXPECTED_KUBECONFIG="$TMP/config/staging/kube/kubeconfig.yaml"
for failure in ca hostname handshake timeout transport; do
  export FAKE_TLS_FAILURE="$failure"
  case "$failure" in
    ca) code=TLS_CERTIFICATE_INVALID ;;
    hostname) code=TLS_HOSTNAME_INVALID ;;
    handshake) code=TLS_HANDSHAKE_FAILED ;;
    timeout) code=PROBE_TIMEOUT ;;
    transport) code=TRANSPORT_FAILED ;;
  esac
  expect 1 "FAIL [$code]"
done
unset FAKE_TLS_FAILURE
export FAKE_SOCKET=/run/account-pki/private/controller.sock
export FAKE_SOCKET_FAILURE=unavailable
expect 1 'FAIL [SOCKET_UNAVAILABLE]'
export FAKE_SOCKET_FAILURE=timeout
expect 1 'FAIL [PROBE_TIMEOUT]'
unset FAKE_SOCKET_FAILURE
for status in 401 403 503; do
  export FAKE_CERTISSUER_RESPONSE="HTTP/1.1 $status Error
Content-Type: text/html

do-not-leak-this-secret"
  if [[ "$status" = 503 ]]; then code=UPSTREAM_UNAVAILABLE; else code=AUTH_REJECTED; fi
  expect 1 "FAIL [$code]"
done
export FAKE_CERTISSUER_RESPONSE='HTTP/1.1 400 Bad Request
Content-Type: application/json

{ "error" : { "message" : "do-not-leak-this-secret", "code" : "user_id_required" } }'
expect 0 'PASS:'
export FAKE_CERTISSUER_RESPONSE='HTTP/1.1 400 Bad Request
Content-Type: application/json

{broken-do-not-leak-this-secret'
expect 1 'FAIL [RESPONSE_JSON_INVALID]'
export FAKE_CERTISSUER_RESPONSE='HTTP/1.1 400 Bad Request
Content-Type: application/json

{"error":{"code":"other_error","message":"do-not-leak-this-secret"}}'
expect 1 'FAIL [VALIDATION_CONTRACT_MISMATCH]'
export FAKE_CERTISSUER_RESPONSE="${GOOD_RESPONSE/400/200}"
expect 1 'FAIL [VALIDATION_CONTRACT_MISMATCH]'
export FAKE_CERTISSUER_RESPONSE=do-not-leak-this-secret
expect 1 'FAIL [RESPONSE_INVALID]'
# Chunked HTTP is decoded before parsing JSON.
body='{"error":{"code":"user_id_required"}}'
printf -v FAKE_CERTISSUER_RESPONSE 'HTTP/1.1 400 Bad Request\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n0\r\n\r\n' "${#body}" "$body"
export FAKE_CERTISSUER_RESPONSE
expect 0 'PASS:'
export FAKE_CERTISSUER_RESPONSE="$GOOD_RESPONSE"
export FAKE_PODS='{"items":[{"metadata":{"name":"not-ready"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"False"}]}},{"metadata":{"name":"terminating","deletionTimestamp":"2026-01-01T00:00:00Z"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}'
expect 1 'FAIL [POD_NOT_READY]'
export FAKE_PODS='{"items":[{"metadata":{"name":"terminating","deletionTimestamp":"2026-01-01T00:00:00Z"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"ready-b"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}},{"metadata":{"name":"ready-a"},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}]}}]}'
: >"$FAKE_CALLS"
expect 0 'PASS:'
grep -q ' exec ready-a ' "$FAKE_CALLS"
if grep -Eq ' exec (-i )?(ready-b|terminating) ' "$FAKE_CALLS"; then printf 'selected wrong Pod\n' >&2; exit 1; fi
export FAKE_PODS=do-not-leak-this-secret
expect 1 'FAIL [POD_RESPONSE_INVALID]'
export FAKE_PODS="$GOOD_PODS"
export FAKE_KUBE_FAILURE=forbidden
expect 1 'FAIL [K8S_FORBIDDEN]'
export FAKE_KUBE_FAILURE=timeout RTK_CERTISSUER_CHECK_TIMEOUT_SECONDS=1
start=$SECONDS
expect 1 'FAIL [PROBE_TIMEOUT]'
((SECONDS - start < 5)) || { printf 'kubectl timeout was not bounded\n' >&2; exit 1; }
# The sleeping child inherits stdout. Completion within the bound also checks
# that cancellation closes the child process group's pipe to the caller.
unset FAKE_KUBE_FAILURE RTK_CERTISSUER_CHECK_TIMEOUT_SECONDS
printf 'CLOUD_STACK_NAME=video-cloud-wrong\n' >"$TMP/workspace/cloud_env/staging/environment.env"
expect 1 'FAIL [CONFIG_INVALID]'
printf 'check-certissuer-app-mtls tests: PASS (%s cases)\n' "$checks"
