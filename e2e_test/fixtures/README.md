# E2E Fixtures

This directory documents local fixture conventions for workspace E2E tests. It
must not contain secret material.

## Local Artifact Locations

Use these ignored paths for generated or operator-provided E2E data:

```text
.artifacts/e2e_test/fixtures/account_manager_test_users/<run_id>/
.artifacts/e2e_test/fixtures/device_certsets/<run_id>/
```

When a certset is intentionally kept outside the workspace, point scripts to it
with an explicit environment variable instead of copying secrets into git:

```sh
E2E_DEVICE_CERTSET_DIR=../rtk_video_cloud/keys/factory-linode-certset-20260514T225802Z
```

## Account Manager Test Users

Expected files:

| File | Secret | Purpose |
| --- | --- | --- |
| `credentials.json` | Yes | Email, password, user id, organization id. Must be mode `0600`. |
| `summary.json` | No password | Non-secret run summary for operator inspection. |
| `users.jsonl` | No password | One user per line for scripts that do not need passwords. |

## Device Certsets

Expected files:

```text
device-material/device-NNN/device.key
device-material/device-NNN/device.csr
device-material/device-NNN/device.crt
device-material/device-NNN/device-chain.crt
factory-enroll-results.json
factory-enroll-report.md
```

`device.key` is secret. Scripts may read it for mTLS tests, but reports must not
print or commit it.
