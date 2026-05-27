# Account + Video Provisioning Smoke

This suite validates the first product-level provisioning path that crosses
Account Manager, Video Cloud identity, and factory-issued device certificates.

## Intended Inputs

```text
.artifacts/e2e_test/fixtures/account_manager_test_users/<run_id>/credentials.json
../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z/factory-enroll-results.json
../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z/device-material/
```

The runner accepts overrides:

```sh
E2E_ACCOUNT_USERS_DIR=.artifacts/e2e_test/fixtures/account_manager_test_users/20260519T014833Z
E2E_DEVICE_CERTSET_DIR=../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z
ACCOUNT_MANAGER_BASE_URL=https://account-manager.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_BASE_URL=https://video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_DEVICE_BASE_URL=https://device.video-cloud-staging.realtekconnect.com
```

## Run

Preflight-only mode validates local fixture/config availability and writes a
redacted `PASS` or `BLOCKED` report without calling live services:

```sh
cd e2e_test
go run ./provisioning/account_video_smoke/cmd/rtk-account-video-smoke \
  --plan-only
```

Live mode calls staged services and writes artifacts under
`.artifacts/e2e_test/provisioning/account_video_smoke/<run_id>/`:

```sh
cd e2e_test
go run ./provisioning/account_video_smoke/cmd/rtk-account-video-smoke
```

Generated files:

- `account-video-smoke-results.json`
- `account-video-smoke-report.md`

Useful optional inputs:

| Variable | Purpose |
| --- | --- |
| `ACCOUNT_MANAGER_PLATFORM_ADMIN_TOKEN` | Existing platform-admin bearer token for Claim Token creation. |
| `ACCOUNT_MANAGER_PLATFORM_ADMIN_EMAIL` / `ACCOUNT_MANAGER_PLATFORM_ADMIN_PASSWORD` | Platform-admin login fallback when a token is not provided. |
| `E2E_CLAIM_TOKEN` | Existing raw Claim Token; skips platform-admin Claim Token creation. |
| `E2E_DEVICE_ID` | Select a specific `devid` from the certset. |
| `ACCOUNT_VIDEO_SMOKE_STRICT_BLOCKED=1` | Exit non-zero on `BLOCKED` as well as `FAIL`. |

## Target Flow

1. Login with a test Account Manager user.
2. Select one organization from that user fixture.
3. Select one factory-enrolled `devid` from the certset.
4. Create or import a Claim Token as platform admin.
5. Resolve the Claim Token into the user's organization.
6. Start account-side provisioning for the resolved device.
7. Read account-side provisioning state.
8. Use the device certificate for video-cloud mTLS token smoke when the staging
   device-facing TLS endpoint is configured for the cert issuer CA.

## Boundaries

This smoke does not replace service-local tests. It proves that existing staged
services and local fixture material can be composed into the product-level
provisioning path.

The runner stops at the first unavailable prerequisite and records `BLOCKED`
with the missing configuration or service dependency. It does not report pass
for a flow that cannot reach platform-admin Claim Token creation, account-side
provisioning, video-side lifecycle projection, or device-facing mTLS token
issuance.

Reports redact private keys, raw bearer tokens, raw Claim Tokens, HMAC secrets,
certificate bodies, CSRs, and common secret-bearing URLs or environment values.
