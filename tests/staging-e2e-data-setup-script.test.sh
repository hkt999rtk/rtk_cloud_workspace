#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/lke"
mkdir -p "$WORKSPACE" "$ENV_ROOT/env" "$ENV_ROOT/artifacts/users" "$ENV_ROOT/artifacts/device-bind" "$ENV_ROOT/devices/test_device/manifests"

cat > "$ENV_ROOT/env/stack.env" <<'EOF_ENV'
CLOUD_PROVIDER=lke
CLOUD_STACK_NAME=video-cloud-staging
EOF_ENV

export CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0

COMMAND_LOG="$TMP/commands.log"
make_stub() {
	local path="$1"
	local name="$2"
	cat > "$path" <<SH
#!/usr/bin/env bash
set -euo pipefail
printf '%s\\t%s\\n' "$name" "\$*" >> "$COMMAND_LOG"
case "$name" in
create-users)
	mkdir -p "$ENV_ROOT/artifacts/users"
	printf '{"brandname":"RTK","users":[{"email":"rtk+001@users.local"}]}\\n' > "$ENV_ROOT/artifacts/users/rtk-users-test.json"
	;;
generate-devices)
	mkdir -p "$ENV_ROOT/devices/test_device/manifests"
	printf '[]\\n' > "$ENV_ROOT/devices/test_device/manifests/devices.json"
	;;
bind-devices)
	mkdir -p "$ENV_ROOT/artifacts/device-bind"
	printf '{"brandname":"RTK","count":4,"assignments":[{"device_id":"dev-1"}]}\\n' > "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"
	;;
validate-bind)
	printf '{"overall":"pass","report_file":"validate-report.md"}\\n'
	;;
esac
SH
	chmod +x "$path"
}

make_stub "$TMP/create-brand.sh" create-brand
make_stub "$TMP/create-users.sh" create-users
make_stub "$TMP/generate-devices.sh" generate-devices
make_stub "$TMP/bind-devices.sh" bind-devices
make_stub "$TMP/validate-bind.sh" validate-bind

OUT_DIR="$TMP/data-setup"
CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT="$TMP/create-brand.sh" \
CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT="$TMP/create-users.sh" \
CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT="$TMP/generate-devices.sh" \
CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT="$TMP/bind-devices.sh" \
CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT="$TMP/validate-bind.sh" \
	"$ROOT/scripts/setup-staging-e2e-data.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--brandname RTK \
	--user-count 2 \
	--device-count 4 \
	--device-mix camera=2,light=2 \
	--out-dir "$OUT_DIR" \
	--quiet > "$TMP/run.out" 2> "$TMP/run.err"

expected=$'create-brand\ncreate-users\ngenerate-devices\nbind-devices\nvalidate-bind'
actual="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual" == "$expected" ]] || {
	printf 'unexpected command order:\n%s\n' "$actual" >&2
	exit 1
}
grep -F $'create-users\t' "$COMMAND_LOG" | grep -F -- "--env-root $WORKSPACE/cloud_env/staging/lke" | grep -F -- '--count 2' | grep -F -- '--rotate-password' | grep -F -- '--concurrency 64' >/dev/null
grep -F $'generate-devices\t' "$COMMAND_LOG" | grep -F -- "--env-root $WORKSPACE/cloud_env/staging/lke" | grep -F -- '--count 4' | grep -F -- '--mix camera=2,light=2' | grep -F -- '--prefix load-device' | grep -F -- '--force' | grep -F -- '--concurrency 64' >/dev/null
grep -F $'bind-devices\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/lke"$' --brandname RTK --users-file '"$ENV_ROOT/artifacts/users/rtk-users-test.json"$' --devices-dir '"$ENV_ROOT/devices/test_device"$' --count 4 --concurrency 64' "$COMMAND_LOG" >/dev/null

SUMMARY="$(jq -r '.summary_file' "$TMP/run.out")"
test "$SUMMARY" = "$OUT_DIR/summary.json"
test -f "$SUMMARY"
jq -e '.overall == "pass" and .users_file != "" and .device_bind_file != "" and (.steps | length == 5)' "$SUMMARY" >/dev/null
grep -E '\[cloud-staging-e2e\] start: create_brand log=.*/logs/create_brand.log' "$TMP/run.err" >/dev/null
if grep -F '[cloud-staging-e2e] progress:' "$TMP/run.err" >/dev/null; then
	echo "quiet data setup should not print progress lines" >&2
	exit 1
fi

: > "$COMMAND_LOG"
cat > "$ENV_ROOT/artifacts/users/rtk-users-complete.json" <<'EOF_USERS'
{"brandname":"RTK","users":[{"email":"rtk+001@users.local"},{"email":"rtk+002@users.local"}]}
EOF_USERS
cat > "$ENV_ROOT/devices/test_device/manifests/devices.json" <<'EOF_DEVICES'
[
  {"device_id":"load-device-0001","device_type":"camera"},
  {"device_id":"load-device-0002","device_type":"camera"},
  {"device_id":"load-device-0003","device_type":"light"},
  {"device_id":"load-device-0004","device_type":"light"}
]
EOF_DEVICES
for bind_artifact in "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-complete.json" "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"; do
cat > "$bind_artifact" <<'EOF_BIND'
{"brandname":"RTK","assignments":[
  {"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
  {"assigned_email":"rtk+002@users.local","device_id":"load-device-0002","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
  {"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"light","service_options":["mqtt"]},
  {"assigned_email":"rtk+002@users.local","device_id":"load-device-0004","device_type":"light","service_options":["mqtt"]}
]}
EOF_BIND
done
CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT="$TMP/create-brand.sh" \
CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT="$TMP/create-users.sh" \
CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT="$TMP/generate-devices.sh" \
CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT="$TMP/bind-devices.sh" \
CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT="$TMP/validate-bind.sh" \
	"$ROOT/scripts/setup-staging-e2e-data.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--brandname RTK \
	--user-count 2 \
	--device-count 4 \
	--device-mix camera=2,light=2 \
	--out-dir "$TMP/data-setup-resume-default" \
	--quiet > "$TMP/resume-default.out" 2> "$TMP/resume-default.err"
expected=$'create-brand\nvalidate-bind'
actual="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual" == "$expected" ]] || {
	printf 'default data setup should reuse complete artifacts; command order:\n%s\n' "$actual" >&2
	exit 1
}
grep -F 'skip: create_users reason="--resume users artifact count=2"' "$TMP/resume-default.err" >/dev/null
grep -F 'skip: create_devices reason="--resume' "$TMP/resume-default.err" >/dev/null

: > "$COMMAND_LOG"
CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT="$TMP/create-brand.sh" \
CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT="$TMP/create-users.sh" \
CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT="$TMP/generate-devices.sh" \
CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT="$TMP/bind-devices.sh" \
CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT="$TMP/validate-bind.sh" \
	"$ROOT/scripts/setup-staging-e2e-data.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--brandname RTK \
	--user-count 2 \
	--device-count 4 \
	--device-mix camera=2,light=2 \
	--out-dir "$TMP/data-setup-no-resume" \
	--no-resume \
	--quiet > "$TMP/no-resume.out" 2> "$TMP/no-resume.err"
expected=$'create-brand\ncreate-users\ngenerate-devices\nbind-devices\nvalidate-bind'
actual="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual" == "$expected" ]] || {
	printf '--no-resume should recreate artifacts; command order:\n%s\n' "$actual" >&2
	exit 1
}

: > "$COMMAND_LOG"
cat > "$ENV_ROOT/devices/test_device/manifests/devices.json" <<'EOF_DEVICES'
[
  {"device_id":"load-device-0001","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"],"certificate_path":"devices/camera/load-device-0001/device.cert.pem","certificate_chain_path":"devices/camera/load-device-0001/device.chain.pem","key_path":"devices/camera/load-device-0001/device.key.pem"},
  {"device_id":"load-device-0002","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"],"certificate_path":"devices/camera/load-device-0002/device.cert.pem","certificate_chain_path":"devices/camera/load-device-0002/device.chain.pem","key_path":"devices/camera/load-device-0002/device.key.pem"},
  {"device_id":"load-device-0003","device_type":"light","service_options":["mqtt"],"certificate_path":"devices/light/load-device-0003/device.cert.pem","certificate_chain_path":"devices/light/load-device-0003/device.chain.pem","key_path":"devices/light/load-device-0003/device.key.pem"},
  {"device_id":"load-device-0004","device_type":"light","service_options":["mqtt"],"certificate_path":"devices/light/load-device-0004/device.cert.pem","certificate_chain_path":"devices/light/load-device-0004/device.chain.pem","key_path":"devices/light/load-device-0004/device.key.pem"}
]
EOF_DEVICES
for device_dir in \
	"$ENV_ROOT/devices/test_device/devices/camera/load-device-0001" \
	"$ENV_ROOT/devices/test_device/devices/camera/load-device-0002" \
	"$ENV_ROOT/devices/test_device/devices/light/load-device-0003" \
	"$ENV_ROOT/devices/test_device/devices/light/load-device-0004"
do
	mkdir -p "$device_dir"
	printf 'cert\n' > "$device_dir/device.cert.pem"
	printf 'chain\n' > "$device_dir/device.chain.pem"
	printf 'key\n' > "$device_dir/device.key.pem"
done
for bind_artifact in "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-complete.json" "$ENV_ROOT/artifacts/device-bind/rtk-device-bind-test.json"; do
cat > "$bind_artifact" <<'EOF_BIND'
{"brandname":"RTK","assignments":[
  {"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
  {"assigned_email":"rtk+002@users.local","device_id":"load-device-0002","device_type":"camera","service_options":["mqtt","video_streaming","video_storage"]},
  {"assigned_email":"rtk+001@users.local","device_id":"load-device-0003","device_type":"light","service_options":["mqtt"]},
  {"assigned_email":"rtk+002@users.local","device_id":"load-device-0004","device_type":"light","service_options":["mqtt"]}
]}
EOF_BIND
done
cat > "$TMP/validate-bind-repair.sh" <<SH
#!/usr/bin/env bash
set -euo pipefail
printf '%s\\t%s\\n' "validate-bind" "\$*" >> "$COMMAND_LOG"
out_dir=""
while [[ \$# -gt 0 ]]; do
	case "\$1" in
		--out-dir)
			out_dir="\$2"
			shift 2
			;;
		*)
			shift
			;;
	esac
done
mkdir -p "\$out_dir"
count_file="$TMP/validate-repair-count"
count=0
if [[ -f "\$count_file" ]]; then
	count="\$(cat "\$count_file")"
fi
count=\$((count + 1))
printf '%s\\n' "\$count" > "\$count_file"
if [[ "\$count" -eq 1 ]]; then
	printf '{"overall":"fail","failure_categories":{"already_bound_not_ready":4}}\\n' > "\$out_dir/bulk-device-bind-validation-results.json"
	printf '{"overall":"fail","failure_categories":{"already_bound_not_ready":4}}\\n'
	exit 1
fi
printf '{"overall":"pass","failure_categories":{}}\\n' > "\$out_dir/bulk-device-bind-validation-results.json"
printf '{"overall":"pass","failure_categories":{}}\\n'
SH
chmod +x "$TMP/validate-bind-repair.sh"
CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT="$TMP/create-brand.sh" \
CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT="$TMP/create-users.sh" \
CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT="$TMP/generate-devices.sh" \
CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT="$TMP/bind-devices.sh" \
CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT="$TMP/validate-bind-repair.sh" \
	"$ROOT/scripts/setup-staging-e2e-data.sh" \
	--workspace "$WORKSPACE" \
	--env-root "$WORKSPACE/cloud_env/staging" \
	--brandname RTK \
	--user-count 2 \
	--device-count 4 \
	--device-mix camera=2,light=2 \
	--out-dir "$TMP/data-setup-repair-bind" \
	--quiet > "$TMP/repair-bind.out" 2> "$TMP/repair-bind.err"
expected=$'create-brand\nvalidate-bind\nbind-devices\nvalidate-bind'
actual="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual" == "$expected" ]] || {
	printf 'already_bound_not_ready should repair bind and validate again; command order:\n%s\n' "$actual" >&2
	exit 1
}
grep -F 'repair: validate_bind failure_category="already_bound_not_ready"; rerunning bind_devices' "$TMP/repair-bind.err" >/dev/null

if CLOUD_PROVIDER=aws "$ROOT/scripts/setup-staging-e2e-data.sh" --workspace "$WORKSPACE" --env-root "$WORKSPACE/cloud_env/staging" --plan >"$TMP/provider.out" 2>"$TMP/provider.err"; then
	echo "expected unsupported provider to fail" >&2
	exit 1
fi
grep -F 'unsupported CLOUD_PROVIDER=aws' "$TMP/provider.err" >/dev/null

mkdir -p "$WORKSPACE/cloud_env/staging/lke/env"
cat > "$WORKSPACE/cloud_env/staging/lke/env/stack.env" <<'EOF_LKE_ENV'
CLOUD_PROVIDER=lke
CLOUD_STACK_NAME=video-cloud-staging
EOF_LKE_ENV
CLOUD_PROVIDER=lke "$ROOT/scripts/setup-staging-e2e-data.sh" --workspace "$WORKSPACE" --env-root "$WORKSPACE/cloud_env/staging" --plan >"$TMP/lke-plan.out"
grep -F 'cloud-staging-e2e-data-setup plan' "$TMP/lke-plan.out" >/dev/null
grep -F 'env_root: '"$WORKSPACE/cloud_env/staging/lke" "$TMP/lke-plan.out" >/dev/null

RTK_CLOUD_STAGING_ENV_ROOT="$WORKSPACE/cloud_env/staging" "$ROOT/stg.sh" data --plan >"$TMP/stg-data-plan.out"
grep -F 'cloud-staging-e2e-data-setup plan' "$TMP/stg-data-plan.out" >/dev/null

if CLOUD_PROVIDER=aws RTK_CLOUD_STAGING_ENV_ROOT="$WORKSPACE/cloud_env/staging" "$ROOT/stg.sh" data --plan >"$TMP/stg-data-provider.out" 2>"$TMP/stg-data-provider.err"; then
	echo "expected stg.sh data unsupported provider to fail" >&2
	exit 1
fi
grep -F 'unsupported CLOUD_PROVIDER=aws' "$TMP/stg-data-provider.err" >/dev/null

CLOUD_PROVIDER=lke RTK_CLOUD_STAGING_ENV_ROOT="$WORKSPACE/cloud_env/staging" "$ROOT/stg.sh" data --plan >"$TMP/stg-data-lke-plan.out"
grep -F 'env_root: '"$WORKSPACE/cloud_env/staging/lke" "$TMP/stg-data-lke-plan.out" >/dev/null
