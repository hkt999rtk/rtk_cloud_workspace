#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

export STG_LOG="$TMP/stg.log"
export TMP

cat > "$TMP/stg-stub.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'args=%s\n' "$*" >> "$STG_LOG"
printf 'VIDEO_RELEASE=%s\n' "${VIDEO_RELEASE:-}" >> "$STG_LOG"
printf 'ACCOUNT_RELEASE=%s\n' "${ACCOUNT_RELEASE:-}" >> "$STG_LOG"
printf 'ADMIN_RELEASE=%s\n' "${ADMIN_RELEASE:-}" >> "$STG_LOG"
if [[ "$*" == *"--plan"* ]]; then
	printf 'cloud-staging-e2e-test plan\n'
	exit 0
fi
printf '{"overall":"pass","summary_file":"%s","report_file":"%s"}\n' "$TMP/summary.json" "$TMP/TEST_REPORT.md"
SH
chmod +x "$TMP/stg-stub.sh"

if RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" "$ROOT/scripts/run-staging-linode-e2e.sh" >"$TMP/missing.out" 2>"$TMP/missing.err"; then
	echo "expected missing --confirm to fail" >&2
	exit 1
fi
grep -F -- '--confirm video-cloud-staging is required' "$TMP/missing.err" >/dev/null
test ! -e "$STG_LOG"

if RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" "$ROOT/scripts/run-staging-linode-e2e.sh" --confirm wrong >"$TMP/wrong.out" 2>"$TMP/wrong.err"; then
	echo "expected wrong --confirm to fail" >&2
	exit 1
fi
grep -F -- '--confirm must be video-cloud-staging' "$TMP/wrong.err" >/dev/null
test ! -e "$STG_LOG"

RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" "$ROOT/scripts/run-staging-linode-e2e.sh" --plan >"$TMP/plan.out"
grep -F 'cloud-staging-e2e-test plan' "$TMP/plan.out" >/dev/null
grep -F 'args=e2e --plan --brandname RTK --user-count 10 --device-count 100' "$STG_LOG" >/dev/null

rm "$STG_LOG"
RTK_CLOUD_STAGING_STACK_NAME=custom-stack \
RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" \
	"$ROOT/scripts/run-staging-linode-e2e.sh" \
	--confirm custom-stack >"$TMP/custom-stack.out"
grep -F 'args=e2e --run --confirm custom-stack --brandname RTK --user-count 10 --device-count 100' "$STG_LOG" >/dev/null

rm "$STG_LOG"
VIDEO_RELEASE=video-ci-release \
ACCOUNT_RELEASE=account-ci-release \
ADMIN_RELEASE=admin-ci-release \
RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging \
RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" \
	"$ROOT/scripts/run-staging-linode-e2e.sh" \
	--confirm video-cloud-staging \
	--out-dir "$TMP/report-dir" >"$TMP/run.out"

grep -F 'args=e2e --run --confirm video-cloud-staging --brandname RTK --user-count 10 --device-count 100 --out-dir '"$TMP/report-dir" "$STG_LOG" >/dev/null
grep -F 'VIDEO_RELEASE=video-ci-release' "$STG_LOG" >/dev/null
grep -F 'ACCOUNT_RELEASE=account-ci-release' "$STG_LOG" >/dev/null
grep -F 'ADMIN_RELEASE=admin-ci-release' "$STG_LOG" >/dev/null
grep -F 'summary_file='"$TMP/summary.json" "$TMP/run.out" >/dev/null
grep -F 'report_file='"$TMP/TEST_REPORT.md" "$TMP/run.out" >/dev/null
grep -F 'logs_dir='"$TMP/report-dir/logs" "$TMP/run.out" >/dev/null
grep -F 'bind_validation_dir='"$TMP/report-dir/bind-validation" "$TMP/run.out" >/dev/null
grep -F 'mqtt_report_file='"$TMP/report-dir/home-mqtt/TEST_REPORT.md" "$TMP/run.out" >/dev/null
