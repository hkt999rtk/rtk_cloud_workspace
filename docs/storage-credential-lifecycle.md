# Linode Object Storage naming and lifecycle

This is the source of truth for bucket names and object paths. The dated [bucket inventory](object-storage-inventory.md) separates observed buckets from proposed names. Runtime intent lives in `cloud_env/<environment>/storage.env`; shared release intent lives in `cloud_deploy/storage/release-artifacts.env`. The CLI discovers endpoints from Linode's API.

## Bucket names

Use `rtk-<purpose>-<environment|shared>-<Linode storage region>`, all lowercase with hyphens. The purpose is an access and lifecycle boundary: `video-media`, `ota-firmware`, `logger-backup`, `backup`, `device-pki-backup`, `reports`, `release`, `ci`, or `loadtest`. Use the actual Object Storage region ID such as `us-sea`, never an older logical alias. Keep the name within 63 characters using lowercase ASCII letters, digits, and hyphens. Check availability in the target region before creation. A rename requires a new bucket, migration, consumer cutover, and later key retirement.

Use `shared` only for a release or CI consumer that intentionally serves several environments. Keep Dev, Staging, and Prod runtime data separate. Separate OTA firmware from clips because OTA has its own writer, CDN delivery, billing ledger, and key rotation. Put customer, brand, product, release, date, and backup ID in object keys, not bucket names. Do not create a bucket per release or customer.

| Purpose | Example Dev bucket | Key beneath bucket prefix | Status |
| --- | --- | --- | --- |
| Clip and media metadata | `rtk-video-media-dev-us-sea` | `environments/video-cloud-dev/clips/...`, `brands/...`, `snapshots/...`, `clip-index/...` | Dev target |
| Registered billable OTA | `rtk-ota-firmware-dev-us-sea` | `environments/video-cloud-dev/ota-billable-v1/<brand>/<product>/<release>/firmware.bin` | Dev target |
| System Logger backup | `rtk-logger-backup-dev-us-sea` | `logger-backups/video-cloud-dev/<YYYY>/<MM>/<DD>/<backup-id>/...` | Reserved; job not implemented |
| Core backup | `rtk-backup-dev-us-sea` | `backups/video-cloud-dev/<backup-id>/...` | Reserved |
| Device PKI backup | `rtk-device-pki-backup-dev-us-iad` | PKI owner defined key | Proposed target |
| Reports | `rtk-reports-dev-us-sea` | `reports/<producer>/...` | Reserved; local MinIO `reports` is unrelated |
| Release artifacts | `rtk-release-shared-us-sea` | Existing release manifest keys | Proposed target |
| CI evidence | `rtk-ci-shared-us-sea` | `ci/<repo>/<run>/...` | Reserved |
| Staging load test | `rtk-loadtest-staging-us-sea` | `loadtests/<run>/...` | Reserved |

The independent OTA service writes `ota-billable-v1/` inside its configured prefix. That namespace is part of billing and CDN behavior. Historical core `ota/` objects stay in legacy media storage with existing URLs until a separate compatibility migration is proved. The old `firmware/` namespace is historical; new registered OTA writes never use it. All new billable OTA firmware therefore uses one path.

## Configuration and credentials

Set `RUNTIME_MEDIA_STORAGE_*` and `RUNTIME_OTA_STORAGE_*` in the environment's `storage.env`. Dedicated OTA requires `RUNTIME_OTA_STORAGE_MODE=dedicated`, `colocated` policy, and a bucket distinct from media. `legacy-shared` preserves Staging and Prod behavior until each is independently migrated.

When a configured media target has been prepared but the live workloads still use the old bucket, set `RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED=true`. Dev uses this gate. Normal create, upgrade, provision, and test commands then require a matching `storage-cutover.json` receipt before changing workloads. Bootstrap and migration remain available. Keep the gate until the live cutover and rollback check are complete; a tracked bucket name alone does not authorize a deployment switch.

Credentials are individual `0600` files below `~/.config/rtk_cloud/<environment>/operator/env/`. Media uses `LINODE_MEDIA_OBJ_ACCESS_KEY_ID` / `LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY`; OTA uses `LINODE_OTA_OBJ_ACCESS_KEY_ID` / `LINODE_OTA_OBJ_SECRET_ACCESS_KEY`. Each key is limited to its bucket. The OTA pod receives the OTA key through the `ota-object-storage` Kubernetes Secret; API and clip workers keep the media key. Shared release uses `LINODE_ARTIFACT_OBJ_*`. Never commit secrets.

## Operator lifecycle

Fetch the latest workspace and submodule refs, inspect live buckets, then run `storage-plan`. Bootstrap each purpose explicitly, validate a signed list and write/read/delete canary, migrate eligible objects with checksums, cut over the owning workload, and keep old credentials until verified unused.

```bash
rtk-cloud deployment storage-plan --environment dev
rtk-cloud deployment storage-bootstrap --environment dev --purpose media --confirm video-cloud-dev
rtk-cloud deployment storage-bootstrap --environment dev --purpose ota --confirm video-cloud-dev
rtk-cloud deployment storage-migrate --environment dev --purpose media --source-env-file /secure/media-source.env --confirm video-cloud-dev
rtk-cloud deployment storage-migrate --environment dev --purpose ota --source-env-file /secure/ota-source.env --confirm video-cloud-dev
rtk-cloud deployment storage-cutover --environment dev --purpose media --confirm video-cloud-dev
rtk-cloud deployment storage-cutover --environment dev --purpose ota --confirm video-cloud-dev
```

Routine deployment does not silently create buckets. `--purpose media` is the compatibility default. Media migration includes `clips/`, `brands/`, `snapshots/`, `clip-index/`, and historical `ota/` / `firmware/`. OTA migration includes only `ota-billable-v1/`. Keys already beneath the environment prefix are not prefixed twice. Per-object SHA-256 receipts are kept in ignored runtime state. Never rewrite a historical `ota/` key into `ota-billable-v1/`.

OTA cutover requires independent OTA service registration, CDN configuration, validated OTA storage, and a ready deployment. If service registration is disabled, the bucket can be prepared but no cutover is recorded. `storage-retire` covers the existing media key lifecycle only: it requires cutover state plus `storage-consumers.json` confirming `generic_key_in_use: false` and revokes one explicit key ID. OTA key retirement requires separate consumer verification. No command deletes buckets.

Media validation writes `runtime/state/storage-preflight.json`; OTA validation writes `runtime/state/storage-preflight-ota.json`. Receipts contain bucket, region, API endpoint, numeric key ID, redacted access suffix, and time. Runtime state should be backed up using the encrypted environment-state procedure.
