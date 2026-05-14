#!/usr/bin/env bash
set -euo pipefail

workspace_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
e2e_root="${workspace_root}/e2e_test"
video_cloud_dir="${RTK_VIDEO_CLOUD_DIR:-${workspace_root}/../rtk_video_cloud}"
example_dir="${video_cloud_dir}/examples/factory-enrollment"
work_dir="${FACTORY_ENROLL_TEST_WORK_DIR:-${workspace_root}/.artifacts/e2e_test/factory_enroll/.work}"
run_id="${FACTORY_ENROLL_TEST_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
artifact_dir="${FACTORY_ENROLL_TEST_ARTIFACT_DIR:-${workspace_root}/.artifacts/e2e_test/factory_enroll/${run_id}}"
factory_addr="${FACTORY_ENROLL_ADDR:-127.0.0.1:18443}"
issuer_addr="${FAKE_CERT_ISSUER_ADDR:-127.0.0.1:19443}"
auth_key="${FACTORY_ENROLL_TEST_AUTH_KEY:-${FACTORY_ENROLL_AUTH_KEY:-factory-secret}}"
count="${FACTORY_ENROLL_TEST_COUNT:-100}"
concurrency="${FACTORY_ENROLL_TEST_CONCURRENCY:-8}"
audit_log="${FACTORY_ENROLL_AUDIT_LOG_PATH:-${artifact_dir}/factoryenroll-audit.jsonl}"

cleanup() {
  if [ -n "${factory_pid:-}" ]; then
    kill "${factory_pid}" 2>/dev/null || true
  fi
  if [ -n "${issuer_pid:-}" ]; then
    kill "${issuer_pid}" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

wait_for_url() {
  local url=$1
  local ca=${2:-}
  local cert=${3:-}
  local key=${4:-}
  for _ in $(seq 1 20); do
    if [ -n "$ca" ] && [ -n "$cert" ] && [ -n "$key" ]; then
      curl -fsS --cacert "$ca" --cert "$cert" --key "$key" "$url" >/dev/null 2>&1 && return 0
    elif [ -n "$ca" ]; then
      curl -fsS --cacert "$ca" "$url" >/dev/null 2>&1 && return 0
    else
      curl -fsS "$url" >/dev/null 2>&1 && return 0
    fi
    sleep 1
  done
  echo "timed out waiting for $url" >&2
  return 1
}

if [ ! -d "$video_cloud_dir" ]; then
  echo "rtk_video_cloud directory not found: $video_cloud_dir" >&2
  exit 1
fi

mkdir -p "$work_dir" "$artifact_dir"
rm -f "$audit_log" "$artifact_dir/fake-certissuer.log" "$artifact_dir/factoryenroll.log"

WORK_DIR="$work_dir" "$example_dir/dev-pki.sh"

(
  cd "$video_cloud_dir"
  FAKE_CERT_ISSUER_ADDR="$issuer_addr" \
  FAKE_CERT_ISSUER_SERVER_CERT="$work_dir/certissuer-server.crt" \
  FAKE_CERT_ISSUER_SERVER_KEY="$work_dir/certissuer-server.key" \
  FAKE_CERT_ISSUER_CLIENT_CA="$work_dir/issuer-client-ca.crt" \
    go run ./examples/factory-enrollment/fake-certissuer >"$artifact_dir/fake-certissuer.log" 2>&1
) &
issuer_pid=$!

wait_for_url "https://$issuer_addr/healthz" "$work_dir/issuer-client-ca.crt" "$work_dir/factoryenroll-client.crt" "$work_dir/factoryenroll-client.key"

(
  cd "$video_cloud_dir"
  FACTORY_ENROLL_ADDR="$factory_addr" \
  FACTORY_ENROLL_AUTH_KEY="$auth_key" \
  FACTORY_ENROLL_AUDIT_LOG_PATH="$audit_log" \
  FACTORY_ENROLL_CERT_ISSUER_URL="https://$issuer_addr" \
  FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT="$work_dir/factoryenroll-client.crt" \
  FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY="$work_dir/factoryenroll-client.key" \
  FACTORY_ENROLL_CERT_ISSUER_CA="$work_dir/issuer-client-ca.crt" \
    go run ./cmd/factoryenroll >"$artifact_dir/factoryenroll.log" 2>&1
) &
factory_pid=$!

wait_for_url "http://$factory_addr/healthz"

(
  cd "$e2e_root"
  go run ./factory_enroll/cmd/rtk-factory-enroll-test run \
    --factory-url "http://$factory_addr" \
    --auth-key "$auth_key" \
    --count "$count" \
    --concurrency "$concurrency" \
    --run-id "$run_id" \
    --artifact-dir "$artifact_dir" \
    --output "$artifact_dir/factory-enroll-results.json" \
    --report-output "$artifact_dir/factory-enroll-report.md"
)

test -s "$artifact_dir/factory-enroll-results.json"
test -s "$artifact_dir/factory-enroll-report.md"
grep -q '"event_type":"certificate_delivered"' "$audit_log"
if grep -Eq 'BEGIN CERTIFICATE REQUEST|BEGIN CERTIFICATE-----|BEGIN (EC |RSA )?PRIVATE KEY|factory-secret' "$audit_log"; then
  echo "audit log leaked sensitive material" >&2
  exit 1
fi

printf 'factory enrollment local e2e passed; run_id=%s artifacts=%s\n' "$run_id" "$artifact_dir"
