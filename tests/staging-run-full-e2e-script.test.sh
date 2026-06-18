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
printf 'CLOUD_DNS_ROOT_DOMAIN=%s\n' "${CLOUD_DNS_ROOT_DOMAIN:-}" >> "$STG_LOG"
printf 'VIDEO_RELEASE=%s\n' "${VIDEO_RELEASE:-}" >> "$STG_LOG"
printf 'ACCOUNT_RELEASE=%s\n' "${ACCOUNT_RELEASE:-}" >> "$STG_LOG"
printf 'ADMIN_RELEASE=%s\n' "${ADMIN_RELEASE:-}" >> "$STG_LOG"
printf 'CLOUD_PROVIDER=%s\n' "${CLOUD_PROVIDER:-}" >> "$STG_LOG"
printf 'RTK_CLOUD_STAGING_ENV_ROOT=%s\n' "${RTK_CLOUD_STAGING_ENV_ROOT:-}" >> "$STG_LOG"
printf 'LKE_VIDEO_CLOUD_IMAGE=%s\n' "${LKE_VIDEO_CLOUD_IMAGE:-}" >> "$STG_LOG"
printf 'LKE_ACCOUNT_MANAGER_IMAGE=%s\n' "${LKE_ACCOUNT_MANAGER_IMAGE:-}" >> "$STG_LOG"
printf 'LKE_CLOUD_ADMIN_IMAGE=%s\n' "${LKE_CLOUD_ADMIN_IMAGE:-}" >> "$STG_LOG"
printf 'LKE_FRONTEND_IMAGE=%s\n' "${LKE_FRONTEND_IMAGE:-}" >> "$STG_LOG"
if [[ "$*" == *"--plan"* ]]; then
	printf 'cloud-staging-e2e-test plan\n'
	exit 0
fi
cat > "$TMP/summary.json" <<JSON
{
  "overall": "pass",
  "generated_at": "2026-06-12T09:36:28Z",
  "env_root": "$TMP/env-root",
  "stack": "video-cloud-staging",
  "brandname": "RTK",
  "steps": [
    {
      "name": "reset_k8s",
      "status": "pass",
      "duration_seconds": 3,
      "log_file": "$TMP/report-dir/logs/reset_k8s.log"
    },
    {
      "name": "provision_k8s",
      "status": "pass",
      "duration_seconds": 7,
      "log_file": "$TMP/report-dir/logs/provision_k8s.log"
    },
    {
      "name": "setup_brand_devices",
      "status": "pass",
      "duration_seconds": 11,
      "log_file": "$TMP/report-dir/logs/setup_brand_devices.log"
    }
  ],
  "artifacts": {
    "report_file": "$TMP/TEST_REPORT.md",
    "data_setup_summary_file": "$TMP/report-dir/data-setup/summary.json",
    "bind_validation_dir": "$TMP/report-dir/data-setup/bind-validation"
  }
}
JSON
printf '# E2E Report\n' > "$TMP/TEST_REPORT.md"
printf '{"overall":"pass","summary_file":"%s","report_file":"%s"}\n' "$TMP/summary.json" "$TMP/TEST_REPORT.md"
SH
chmod +x "$TMP/stg-stub.sh"

mkdir -p "$TMP/bin"
cat > "$TMP/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf 'go\t%s\n' "$*" >> "$GO_LOG"
if [[ "$1" != "run" || "$2" != "./scripts/go/rtk-cloud" || "$3" != "--" || "$4" != "lke-resolve-images" ]]; then
	echo "unexpected go invocation: $*" >&2
	exit 1
fi
out=""
while [[ $# -gt 0 ]]; do
	case "$1" in
		--out)
			out="${2:-}"
			shift 2
			;;
		*)
			shift
			;;
	esac
done
if [[ -z "$out" ]]; then
	echo "missing --out" >&2
	exit 1
fi
mkdir -p "$(dirname "$out")"
cat > "$out" <<'JSON'
{
  "env": {
    "LKE_POSTGRES_IMAGE": "postgres:16-alpine",
    "LKE_VIDEO_CLOUD_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api:sha-testvideo",
    "LKE_ACCOUNT_MANAGER_IMAGE": "ghcr.io/hkt999rtk/rtk_account_manager/account-manager:sha-testaccount",
    "LKE_CLOUD_ADMIN_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-testadmin",
    "LKE_FRONTEND_IMAGE": "ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend:sha-testfront"
  }
}
JSON
SH
chmod +x "$TMP/bin/go"
export GO_LOG="$TMP/go.log"

cat > "$TMP/stack.env" <<'EOF'
CLOUD_PROVIDER=linode
CLOUD_STACK_NAME=video-cloud-staging
CLOUD_DNS_ROOT_DOMAIN=example.test
EOF

if RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" "$ROOT/scripts/run-staging-e2e.sh" >"$TMP/missing.out" 2>"$TMP/missing.err"; then
	echo "expected missing --confirm to fail" >&2
	exit 1
fi
grep -F -- '--confirm video-cloud-staging is required' "$TMP/missing.err" >/dev/null
test ! -e "$STG_LOG"

if RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" "$ROOT/scripts/run-staging-e2e.sh" --confirm wrong >"$TMP/wrong.out" 2>"$TMP/wrong.err"; then
	echo "expected wrong --confirm to fail" >&2
	exit 1
fi
grep -F -- '--confirm must be video-cloud-staging' "$TMP/wrong.err" >/dev/null
test ! -e "$STG_LOG"

if CLOUD_PROVIDER=aws RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" "$ROOT/scripts/run-staging-e2e.sh" --plan >"$TMP/provider.out" 2>"$TMP/provider.err"; then
	echo "expected unsupported provider to fail" >&2
	exit 1
fi
grep -F 'unsupported CLOUD_PROVIDER=aws' "$TMP/provider.err" >/dev/null
test ! -e "$STG_LOG"

RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" "$ROOT/scripts/run-staging-e2e.sh" --plan >"$TMP/plan.out"
grep -F 'cloud-staging-e2e-test plan' "$TMP/plan.out" >/dev/null
grep -F 'args=e2e --plan' "$STG_LOG" >/dev/null

rm "$STG_LOG"
mkdir -p "$TMP/lke/env"
cat > "$TMP/lke/env/stack.env" <<'EOF_LKE_STACK'
CLOUD_PROVIDER=lke
CLOUD_STACK_NAME=video-cloud-staging
EOF_LKE_STACK
RTK_CLOUD_STACK_FILE="$TMP/lke/env/stack.env" \
RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" \
	"$ROOT/scripts/run-staging-e2e.sh" --plan >"$TMP/lke-plan.out"
grep -F 'cloud-staging-e2e-test plan' "$TMP/lke-plan.out" >/dev/null
grep -F 'LKE image resolve: automatic when service image env vars are missing' "$TMP/lke-plan.out" >/dev/null
grep -F 'args=e2e --plan' "$STG_LOG" >/dev/null
grep -F 'CLOUD_PROVIDER=lke' "$STG_LOG" >/dev/null
grep -F 'RTK_CLOUD_STAGING_ENV_ROOT='"$TMP/lke" "$STG_LOG" >/dev/null
test ! -e "$GO_LOG"

rm "$STG_LOG"
PATH="$TMP/bin:$PATH" \
RTK_CLOUD_STACK_FILE="$TMP/lke/env/stack.env" \
RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" \
	"$ROOT/scripts/run-staging-e2e.sh" \
	--confirm video-cloud-staging >"$TMP/lke-run.out"
grep -F $'go\trun ./scripts/go/rtk-cloud -- lke-resolve-images --workspace '"$ROOT"$' --env-root '"$TMP/lke" "$GO_LOG" >/dev/null
grep -F 'LKE_VIDEO_CLOUD_IMAGE=ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api:sha-testvideo' "$STG_LOG" >/dev/null
grep -F 'LKE_ACCOUNT_MANAGER_IMAGE=ghcr.io/hkt999rtk/rtk_account_manager/account-manager:sha-testaccount' "$STG_LOG" >/dev/null
grep -F 'LKE_CLOUD_ADMIN_IMAGE=ghcr.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-testadmin' "$STG_LOG" >/dev/null
grep -F 'LKE_FRONTEND_IMAGE=ghcr.io/hkt999rtk/rtk_cloud_frontend/frontend:sha-testfront' "$STG_LOG" >/dev/null
grep -R -F "LKE_VIDEO_CLOUD_IMAGE='ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api:sha-testvideo'" "$ROOT/.artifacts/lke-images" >/dev/null

rm "$STG_LOG" "$GO_LOG"
PATH="$TMP/bin:$PATH" \
LKE_VIDEO_CLOUD_IMAGE=manual-video \
LKE_ACCOUNT_MANAGER_IMAGE=manual-account \
LKE_CLOUD_ADMIN_IMAGE=manual-admin \
LKE_FRONTEND_IMAGE=manual-frontend \
RTK_CLOUD_STACK_FILE="$TMP/lke/env/stack.env" \
RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" \
	"$ROOT/scripts/run-staging-e2e.sh" \
	--confirm video-cloud-staging >"$TMP/lke-manual-run.out"
test ! -e "$GO_LOG"
grep -F 'LKE_VIDEO_CLOUD_IMAGE=manual-video' "$STG_LOG" >/dev/null
grep -F 'LKE_ACCOUNT_MANAGER_IMAGE=manual-account' "$STG_LOG" >/dev/null
grep -F 'LKE_CLOUD_ADMIN_IMAGE=manual-admin' "$STG_LOG" >/dev/null
grep -F 'LKE_FRONTEND_IMAGE=manual-frontend' "$STG_LOG" >/dev/null

rm "$STG_LOG"
CLOUD_PROVIDER=linode \
RTK_CLOUD_STAGING_STACK_NAME=custom-stack \
RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" \
	"$ROOT/scripts/run-staging-e2e.sh" \
	--confirm custom-stack >"$TMP/custom-stack.out"
grep -F 'args=e2e --run --confirm custom-stack' "$STG_LOG" >/dev/null

rm "$STG_LOG"
VIDEO_RELEASE=video-ci-release \
ACCOUNT_RELEASE=account-ci-release \
ADMIN_RELEASE=admin-ci-release \
CLOUD_PROVIDER=linode \
RTK_CLOUD_STAGING_STACK_NAME=video-cloud-staging \
RTK_CLOUD_STACK_FILE="$TMP/stack.env" \
RTK_CLOUD_STG_SH="$TMP/stg-stub.sh" \
	"$ROOT/scripts/run-staging-e2e.sh" \
	--confirm video-cloud-staging \
	--brandname RTK \
	--user-count 10 \
	--device-count 100 \
	--device-mix camera=40,light=25,air_conditioner=20,smart_meter=15 \
	--device-prefix load-device \
	--out-dir "$TMP/report-dir" \
	--skip-mqtt-probe >"$TMP/run.out"

grep -F 'args=e2e --run --confirm video-cloud-staging --brandname RTK --user-count 10 --device-count 100 --device-mix camera=40,light=25,air_conditioner=20,smart_meter=15 --device-prefix load-device --out-dir '"$TMP/report-dir"' --skip-mqtt-probe' "$STG_LOG" >/dev/null
grep -F 'CLOUD_DNS_ROOT_DOMAIN=example.test' "$STG_LOG" >/dev/null
grep -F 'summary_file='"$TMP/summary.json" "$TMP/run.out" >/dev/null
grep -F 'report_file='"$TMP/TEST_REPORT.md" "$TMP/run.out" >/dev/null
grep -F 'install_report_file='"$TMP/report-dir/INSTALL_REPORT.md" "$TMP/run.out" >/dev/null
grep -F 'logs_dir='"$TMP/report-dir/logs" "$TMP/run.out" >/dev/null
grep -F 'data_setup_summary_file='"$TMP/report-dir/data-setup/summary.json" "$TMP/run.out" >/dev/null
grep -F 'bind_validation_dir='"$TMP/report-dir/data-setup/bind-validation" "$TMP/run.out" >/dev/null
grep -F 'mqtt_report_file='"$TMP/report-dir/home-mqtt/TEST_REPORT.md" "$TMP/run.out" >/dev/null

grep -F '# Staging Installation Report' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
grep -F -- '- Provider: linode' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
grep -F -- '- Total duration seconds: 21' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
grep -F -- '- Data setup summary: `'"$TMP/report-dir/data-setup/summary.json"'`' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
grep -F -- '- Bind validation: `'"$TMP/report-dir/data-setup/bind-validation"'`' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
grep -F '| reset_k8s | pass | 3 | `'"$TMP/report-dir/logs/reset_k8s.log"'` |' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
grep -F '| provision_k8s | pass | 7 | `'"$TMP/report-dir/logs/provision_k8s.log"'` |' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
grep -F '| setup_brand_devices | pass | 11 | `'"$TMP/report-dir/logs/setup_brand_devices.log"'` |' "$TMP/report-dir/INSTALL_REPORT.md" >/dev/null
