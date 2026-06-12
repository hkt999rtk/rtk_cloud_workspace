#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
mkdir -p "$WORKSPACE" "$ENV_ROOT/env" "$ENV_ROOT/artifacts/users" "$ENV_ROOT/artifacts/device-bind" "$ENV_ROOT/devices/test_device/manifests"

cat > "$ENV_ROOT/env/stack.env" <<'EOF_ENV'
CLOUD_PROVIDER=linode
CLOUD_STACK_NAME=video-cloud-staging
EOF_ENV

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
	--out-dir "$OUT_DIR" > "$TMP/run.out"

expected=$'create-brand\ncreate-users\ngenerate-devices\nbind-devices\nvalidate-bind'
actual="$(cut -f1 "$COMMAND_LOG")"
[[ "$actual" == "$expected" ]] || {
	printf 'unexpected command order:\n%s\n' "$actual" >&2
	exit 1
}
grep -F $'create-users\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/linode"$' --brandname RTK --count 2 --rotate-password' "$COMMAND_LOG" >/dev/null
grep -F $'generate-devices\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/linode"$' --count 4 --mix camera=2,light=2 --prefix load-device --force' "$COMMAND_LOG" >/dev/null
grep -F $'bind-devices\t--workspace '"$WORKSPACE"$' --env-root '"$WORKSPACE/cloud_env/staging/linode"$' --brandname RTK --users-file '"$ENV_ROOT/artifacts/users/rtk-users-test.json"$' --devices-dir '"$ENV_ROOT/devices/test_device"$' --count 4' "$COMMAND_LOG" >/dev/null

SUMMARY="$(jq -r '.summary_file' "$TMP/run.out")"
test "$SUMMARY" = "$OUT_DIR/summary.json"
test -f "$SUMMARY"
jq -e '.overall == "pass" and .users_file != "" and .device_bind_file != "" and (.steps | length == 5)' "$SUMMARY" >/dev/null

if CLOUD_PROVIDER=aws "$ROOT/scripts/setup-staging-e2e-data.sh" --workspace "$WORKSPACE" --env-root "$WORKSPACE/cloud_env/staging" --plan >"$TMP/provider.out" 2>"$TMP/provider.err"; then
	echo "expected unsupported provider to fail" >&2
	exit 1
fi
grep -F 'unsupported CLOUD_PROVIDER=aws' "$TMP/provider.err" >/dev/null
