# Account + Video Provisioning Smoke

This suite will validate the first product-level provisioning path that crosses
Account Manager, Video Cloud identity, and factory-issued device certificates.

## Intended Inputs

```text
.artifacts/e2e_test/fixtures/account_manager_test_users/<run_id>/credentials.json
../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z/factory-enroll-results.json
../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z/device-material/
```

Scripts should accept overrides:

```sh
E2E_ACCOUNT_USERS_DIR=.artifacts/e2e_test/fixtures/account_manager_test_users/20260519T014833Z
E2E_DEVICE_CERTSET_DIR=../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z
ACCOUNT_MANAGER_BASE_URL=https://account-manager.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_BASE_URL=https://video-cloud-staging.realtekconnect.com
```

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

The initial version may stop at account-side provisioning if video-side device
mTLS or lifecycle worker prerequisites are not available. That result must be
reported as `BLOCKED` with the missing prerequisite, not as pass.
