#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

WORKSPACE="$TMP/workspace"
ENV_ROOT="$WORKSPACE/cloud_env/staging/linode"
FAKE_BIN="$TMP/bin"
CURL_LOG="$TMP/curl-log"
mkdir -p \
	"$FAKE_BIN" \
	"$CURL_LOG" \
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
printf 'bootstrap admin env applied and account-manager is healthy\n' >&2
SH
chmod +x "$FAKE_BIN/ssh"

cat > "$FAKE_BIN/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
out=""
write_code=""
data=""
args=("$@")
for ((i = 0; i < ${#args[@]}; i++)); do
	case "${args[$i]}" in
	-o) out="${args[$((i + 1))]}" ;;
	-w) write_code="${args[$((i + 1))]}" ;;
	--data-binary) data="${args[$((i + 1))]}" ;;
	esac
done
url=""
for arg in "${args[@]}"; do
	if [[ "$arg" == http://* || "$arg" == https://* ]]; then
		url="$arg"
		break
	fi
done
case "$url" in
https://api.ipify.org)
	printf '198.51.100.20'
	exit 0
	;;
https://api.linode.com/v4/networking/firewalls/12345/rules)
	printf '{"inbound":[{"label":"ssh","action":"ACCEPT","protocol":"TCP","ports":"22","addresses":{"ipv4":["198.51.100.20/32"]}}],"outbound":[]}'
	exit 0
	;;
*/v1/auth/login|*/v1/brand-clouds/*/auth/login)
	payload="${data#@}"
	email="$(jq -r '.email' "$payload")"
	csr="$(jq -r '.app_csr_pem // ""' "$payload")"
	status=200
	case "$email" in
	root@example.com)
		printf '{"tokens":{"access_token":"test-token"}}' >"$out"
		;;
	*)
		user_id="user-$(printf '%s' "$email" | tr '@+' '__')"
		brand_user_id="brand-user-$(printf '%s' "$email" | tr '@+' '__')"
		if [[ -n "$csr" ]]; then
			if [[ "${FAKE_APP_LOGIN_ERROR:-0}" == "1" ]]; then
				printf '{"code":"app_certificate_csr_invalid","message":"CSR subject does not match the authenticated user"}' >"$out"
				status=400
			elif ! jq -r '.app_csr_pem' "$payload" | openssl req -noout -subject -nameopt RFC2253 | grep -F "CN=app-brand-cloud-user:$brand_user_id" >/dev/null; then
				printf '{"code":"app_certificate_csr_invalid","message":"CSR subject does not match the authenticated brand-cloud user"}' >"$out"
				status=400
			else
				cp "$payload" "$FAKE_CURL_LOG/app-login-$(printf '%s' "$email" | tr '@+' '__').json"
				jq -cn --arg email "$email" --arg user_id "$user_id" --arg brand_user_id "$brand_user_id" \
					'{user:{id:$user_id,email:$email,display_name:$email,email_verified:true,signup_pending_verification:false,platform_admin:false,created_at:"2026-05-30T00:00:00Z",updated_at:"2026-05-30T00:00:00Z"},tokens:{access_token:"user-token"},app_certificate:{status:"issued",subject:("app-brand-cloud-user:"+$brand_user_id),certificate_pem:"-----BEGIN CERTIFICATE-----\nissued\n-----END CERTIFICATE-----\n",certificate_chain_pem:"-----BEGIN CERTIFICATE-----\nchain\n-----END CERTIFICATE-----\n",fingerprint_sha256:("fp-"+$user_id),serial_number:"01",issuer_request_id:("req-"+$user_id),not_before:"2026-06-03T00:00:00Z",not_after:"2027-06-03T00:00:00Z"}}' >"$out"
			fi
		elif [[ "${FAKE_INITIAL_ISSUED:-0}" == "1" ]]; then
			jq -cn --arg email "$email" --arg user_id "$user_id" --arg brand_user_id "$brand_user_id" \
				'{user:{id:$user_id,email:$email,display_name:$email,email_verified:true,signup_pending_verification:false,platform_admin:false,created_at:"2026-05-30T00:00:00Z",updated_at:"2026-05-30T00:00:00Z"},tokens:{access_token:"user-token"},app_certificate:{status:"issued",subject:("app-brand-cloud-user:"+$brand_user_id),certificate_pem:"-----BEGIN CERTIFICATE-----\nissued-existing\n-----END CERTIFICATE-----\n",certificate_chain_pem:"-----BEGIN CERTIFICATE-----\nchain-existing\n-----END CERTIFICATE-----\n",fingerprint_sha256:("fp-existing-"+$user_id),serial_number:"02",issuer_request_id:("req-existing-"+$user_id),not_before:"2026-06-03T00:00:00Z",not_after:"2027-06-03T00:00:00Z"}}' >"$out"
		else
			jq -cn --arg email "$email" --arg user_id "$user_id" \
				'{user:{id:$user_id,email:$email,display_name:$email,email_verified:true,signup_pending_verification:false,platform_admin:false,created_at:"2026-05-30T00:00:00Z",updated_at:"2026-05-30T00:00:00Z"},tokens:{access_token:"user-token"},app_certificate:{status:"csr_required"}}' >"$out"
		fi
		;;
	esac
	;;
*/v1/admin/brand-clouds\?limit=200)
	if [[ "${FAKE_NO_BRAND:-0}" == "1" ]]; then
		printf '{"brand_clouds":[],"pagination":{"limit":200,"offset":0,"total":0}}' >"$out"
	else
		printf '{"brand_clouds":[{"id":"org-rtk","tenant_slug":"rtk-test","name":"RTK","organization_kind":"brand_cloud","status":"active","tier":"commercial","evaluation_device_quota":5,"metadata":{"brandname":"RTK"},"created_at":"2026-05-27T00:00:00Z","updated_at":"2026-05-27T00:00:00Z"}],"pagination":{"limit":200,"offset":0,"total":1}}' >"$out"
	fi
	status=200
	;;
	*/v1/admin/brand-clouds/org-rtk/users)
		payload="${data#@}"
		email="$(jq -r '.email' "$payload")"
		role="$(jq -r '.role' "$payload")"
		rotate="$(jq -r '.rotate_password // false' "$payload")"
		action="${FAKE_USER_ACTION:-created}"
		if [[ "$action" == "created" ]]; then
			status=201
		else
			status=200
		fi
		cp "$payload" "$FAKE_CURL_LOG/user-$(printf '%s' "$email" | tr '@+' '__').json"
		jq -cn --arg action "$action" --arg email "$email" --arg role "$role" --arg brand_user_id "brand-user-$(printf '%s' "$email" | tr '@+' '__')" --argjson rotate "$rotate" \
			'{action:$action, user:{id:("user-"+$email), email:$email, display_name:$email, email_verified:true, signup_pending_verification:false, created_at:"2026-05-30T00:00:00Z", updated_at:"2026-05-30T00:00:00Z"}, brand_cloud_user:{id:$brand_user_id}, member:{organization_id:"org-rtk", user_id:("user-"+$email), email:$email, role:$role, created_at:"2026-05-30T00:00:00Z", updated_at:"2026-05-30T00:00:00Z"}, rotate_password:$rotate}' >"$out"
		;;
*)
	printf 'unexpected curl url: %s\n' "$url" >&2
	exit 1
	;;
esac
if [[ -n "$write_code" ]]; then
	printf '%s' "${write_code//'%{http_code}'/$status}"
fi
SH
chmod +x "$FAKE_BIN/curl"

if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--brandname RTK >"$TMP/missing-env-root.out" 2>&1; then
	echo "expected missing --env-root to fail" >&2
	exit 1
fi
grep -F -- '--env-root is required' "$TMP/missing-env-root.out" >/dev/null

DRY_RUN="$TMP/dry-run.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--count 2 \
	--dry-run >"$DRY_RUN"
jq -e '.action == "dry_run" and .brand_cloud.id == "org-rtk" and (.users | length == 2)' "$DRY_RUN" >/dev/null
jq -e '.users[0].email == "rtk+001@users.local" and .users[1].email == "rtk+002@users.local"' "$DRY_RUN" >/dev/null
if find "$CURL_LOG" -name 'user-*.json' | grep -q .; then
	echo "dry-run must not call create user API" >&2
	exit 1
fi

OUT="$TMP/out.json"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--count 2 >"$OUT"

if grep -i 'password' "$OUT" >/dev/null; then
	echo "stdout must not include passwords" >&2
	exit 1
fi
if grep -i 'private_key' "$OUT" >/dev/null; then
	echo "stdout must not include private keys" >&2
	exit 1
fi
jq -e '.action == "created" and .created == 2 and .role == "member"' "$OUT" >/dev/null
CREDS="$(jq -r '.credentials_file' "$OUT")"
test -f "$CREDS"
jq -e '.brandname == "RTK" and .brand_cloud_id == "org-rtk" and (.users | length == 2)' "$CREDS" >/dev/null
jq -e '.users[0].email == "rtk+001@users.local" and (.users[0].password | length >= 24)' "$CREDS" >/dev/null
jq -e '.users[0].app_credentials.private_key_pem | startswith("-----BEGIN EC PRIVATE KEY-----")' "$CREDS" >/dev/null
jq -e '.users[0].app_credentials.csr_pem | startswith("-----BEGIN CERTIFICATE REQUEST-----")' "$CREDS" >/dev/null
jq -e '.users[0].app_certificate.status == "issued" and .users[0].app_certificate.fingerprint_sha256 == "fp-user-rtk_001_users.local"' "$CREDS" >/dev/null
test "$(find "$CURL_LOG" -name 'app-login-*.json' | wc -l | tr -d ' ')" = "2"
jq -e '.app_csr_pem | startswith("-----BEGIN CERTIFICATE REQUEST-----")' "$CURL_LOG/app-login-rtk_001_users.local.json" >/dev/null
test "$(find "$CURL_LOG" -name 'user-*.json' | wc -l | tr -d ' ')" = "2"
jq -e '.role == "member" and .rotate_password == false' "$CURL_LOG/user-rtk_001_users.local.json" >/dev/null

ASSIGNED_ERR="$TMP/assigned.err"
if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_USER_ACTION=assigned "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--count 1 >"$TMP/assigned.out" 2>"$ASSIGNED_ERR"; then
	echo "expected assigned users without --rotate-password to fail" >&2
	exit 1
fi
grep -F 'brand user already exists and password was not rotated: email=rtk+001@users.local' "$ASSIGNED_ERR" >/dev/null
if find "$ENV_ROOT/artifacts/users" -name 'rtk-users-*.json' -newer "$OUT" 2>/dev/null | grep -q .; then
	echo "assigned failure must not write a new credentials artifact" >&2
	exit 1
fi

ROTATE_OUT="$TMP/rotate.out"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_USER_ACTION=assigned "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--count 1 \
	--rotate-password >"$ROTATE_OUT"
jq -e '.assigned == 1 and .created == 0' "$ROTATE_OUT" >/dev/null
ROTATE_CREDS="$(jq -r '.credentials_file' "$ROTATE_OUT")"
jq -e '.users[0].action == "assigned" and (.users[0].password | length >= 24)' "$ROTATE_CREDS" >/dev/null
jq -e '.users[0].app_certificate.status == "issued"' "$ROTATE_CREDS" >/dev/null
jq -e '.rotate_password == true' "$CURL_LOG/user-rtk_001_users.local.json" >/dev/null

ISSUED_OUT="$TMP/issued-existing.out"
PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_USER_ACTION=assigned FAKE_INITIAL_ISSUED=1 "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--count 1 \
	--rotate-password >"$ISSUED_OUT"
ISSUED_CREDS="$(jq -r '.credentials_file' "$ISSUED_OUT")"
jq -e '.users[0].app_certificate.status == "issued" and .users[0].app_certificate.fingerprint_sha256 == "fp-existing-user-rtk_001_users.local"' "$ISSUED_CREDS" >/dev/null
jq -e '.users[0].app_credentials.private_key_pem | startswith("-----BEGIN EC PRIVATE KEY-----")' "$ISSUED_CREDS" >/dev/null
jq -e '.users[0].app_credentials.csr_pem | startswith("-----BEGIN CERTIFICATE REQUEST-----")' "$ISSUED_CREDS" >/dev/null

APP_LOGIN_ERR="$TMP/app-login.err"
if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_APP_LOGIN_ERROR=1 "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--count 1 >"$TMP/app-login.out" 2>"$APP_LOGIN_ERR"; then
	echo "expected app certificate login failure" >&2
	exit 1
fi
grep -F 'login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 400: code=app_certificate_csr_invalid message=CSR subject does not match the authenticated user' "$APP_LOGIN_ERR" >/dev/null
if grep -Ei 'password|private_key|BEGIN CERTIFICATE REQUEST' "$APP_LOGIN_ERR" >/dev/null; then
	echo "app login error must not include sensitive material" >&2
	exit 1
fi

MISSING="$TMP/missing-brand.err"
if PATH="$FAKE_BIN:$PATH" FAKE_CURL_LOG="$CURL_LOG" FAKE_NO_BRAND=1 "/usr/local/go/bin/go" run "$ROOT/scripts/go/rtk-cloud" -- create-users \
	--workspace "$WORKSPACE" \
	--env-root "$ENV_ROOT" \
	--brandname RTK \
	--count 1 >"$TMP/missing-brand.out" 2>"$MISSING"; then
	echo "expected missing brand cloud to fail" >&2
	exit 1
fi
grep -F 'brand cloud not found: RTK' "$MISSING" >/dev/null
