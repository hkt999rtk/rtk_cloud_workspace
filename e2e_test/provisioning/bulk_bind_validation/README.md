# Bulk Device Bind Validation

This profile validates the redacted artifact produced by
`scripts/cloud-bind-devices.sh`. It is intended for staging smoke runs after
100 factory-enrolled devices have been claimed, bound, and provision-started for
10 RTK member users.

Run from the workspace root:

```sh
scripts/cloud-validate-device-bind.sh \
  --bind-artifact cloud_env/staging/linode/artifacts/device-bind/rtk-device-bind-<timestamp>.json \
  --expected-count 100 \
  --expected-devices-per-user 10
```

The profile checks:

- all expected device assignments are present
- every assignment has an account device id and provision operation id
- each user has the expected number of devices
- `mqtt_device` assignments do not carry video service options
- service options are limited to `mqtt`, `video_streaming`, and `video_storage`

Reports are written under
`.artifacts/e2e_test/provisioning/bulk_bind_validation/<timestamp>/` by default:

- `bulk-bind-validation-results.json`
- `bulk-bind-validation-report.md`

stdout contains only a summary JSON. Reports use redacted API-level identifiers
and omit credential material and local key paths.
