#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
mkdir -p \
	"$FAKE_BIN" \
	"$ENV_ROOT/env" \
	"$ENV_ROOT/services/account-manager" \
	"$ENV_ROOT/state"

cat > "$ENV_ROOT/env/operator.env" <<'EOF_OPERATOR'
LINODE_TOKEN=fake-linode-token
EOF_OPERATOR

cat > "$ENV_ROOT/services/account-manager/account-manager-public-staging.env" <<'EOF_ENV'
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.example.com
ACCOUNT_MANAGER_LINODE_SSH_KEY=/tmp/fake-key
ACCOUNT_MANAGER_LINODE_SSH_USER=root
EOF_ENV

cat > "$ENV_ROOT/state/account-manager-staging.env" <<'EOF_STATE'
ACCOUNT_MANAGER_LINODE_HOST=203.0.113.10
ACCOUNT_MANAGER_LINODE_PUBLIC_IPV4=203.0.113.10
ACCOUNT_MANAGER_LINODE_FIREWALL_ID=12345
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=account-fw
EOF_STATE

cat > "$ENV_ROOT/services/account-manager/account-manager-platform-admin.env" <<'EOF_ADMIN'
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=root@example.com
ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=correct-horse-battery-staple
EOF_ADMIN

cat > "$FAKE_BIN/ssh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
cat >/dev/null
if [[ "$*" == *"Account Manager VM is provisioned but the runtime is not deployed"* ]]; then
	printf 'Account Manager VM is provisioned but the runtime is not deployed on this host.\n' >&2
	printf 'Run ./stg.sh deploy --account-release <release> or pass --account-release-bundle <bundle>, then retry ./stg.sh brand.\n' >&2
	exit 1
fi
printf 'Unit rtk-account-manager.service could not be found.\n' >&2
exit 1
SH
chmod +x "$FAKE_BIN/ssh"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
url=""
for arg in "$@"; do
	if [[ "$arg" == http://* || "$arg" == https://* ]]; then
		url="$arg"
		break
	fi
done
case "$url" in
https://api.ipify.org)
	printf '198.51.100.20'
	;;
https://api.linode.com/v4/networking/firewalls/12345/rules)
	printf '{"inbound":[{"label":"ssh","action":"ACCEPT","protocol":"TCP","ports":"22","addresses":{"ipv4":["198.51.100.20/32"]}}],"outbound":[]}'
	;;
*)
	exit 99
	;;
esac
SH
chmod +x "$FAKE_BIN/curl"

if PATH="$FAKE_BIN:$PATH" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-brandname-cloud \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK >"$TMP/out" 2>&1; then
	echo "expected missing Account Manager runtime to fail" >&2
	exit 1
fi

grep -F 'Account Manager VM is provisioned but the runtime is not deployed' "$TMP/out" >/dev/null
grep -F './stg.sh deploy --account-release' "$TMP/out" >/dev/null
