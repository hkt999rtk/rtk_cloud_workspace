# Linode Object Storage naming and lifecycle

This is the source of truth for bucket names and object paths. The dated [bucket inventory](object-storage-inventory.md) separates observed buckets from proposed names. Runtime intent lives in `cloud_env/<environment>/storage.env`; shared release intent lives in `cloud_deploy/storage/release-artifacts.env`. The CLI discovers endpoints from Linode's API.

## Bucket names

Use `rtk-<purpose>-<environment|shared>-<Linode storage region>`, all lowercase with hyphens. The purpose is an access and lifecycle boundary: `video-media`, `ota-firmware`, `logger-backup`, `backup`, `device-pki-backup`, `reports`, `release`, `ci`, or `loadtest`. Use the actual Object Storage region ID such as `us-sea`, never an older logical alias. Keep the name within 63 characters using lowercase ASCII letters, digits, and hyphens. Check availability in the target region before creation. A rename requires a new bucket, migration, consumer cutover, and later key retirement.

Use `shared` only for a release or CI consumer that intentionally serves several environments. Keep Dev, Staging, and Prod runtime data separate. Separate OTA firmware from clips because OTA has its own writer, CDN or signed Object Storage delivery, billing ledger, and key rotation. Put customer, brand, product, release, date, and backup ID in object keys, not bucket names. Do not create a bucket per release or customer.

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

The resolved compute and Object Storage regions on 2026-09-27 give the OTA
targets `rtk-ota-firmware-staging-sg-sin-2` and
`rtk-ota-firmware-prod-us-sea`. These are target names, not evidence that
either bucket exists or has been cut over. Confirm the provider-reported region
before creation; preserve each environment's `legacy-shared` setting until its
dedicated bucket, scoped credentials, migration and workload cutover pass.

The independent OTA service writes `ota-billable-v1/` inside its configured prefix. That namespace is part of billing and delivery behavior. Historical core `ota/` objects stay in legacy media storage with existing URLs until a separate compatibility migration is proved. The old `firmware/` namespace is historical; new registered OTA writes never use it. All new billable OTA firmware therefore uses one path.

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
rtk-cloud deployment storage-cutover --environment dev --purpose ota --source-env-file /secure/ota-source.env --confirm video-cloud-dev
```

Routine deployment does not silently create buckets. `--purpose media` is the compatibility default. OTA bootstrap always requests an assigned E3 endpoint (or E2 where available), including when CDN is configured. It verifies the bucket's reported type and refuses an existing E0/E1 bucket before creating another key. Reusing the same bucket name does not change its endpoint type. Media migration includes `clips/`, `brands/`, `snapshots/`, `clip-index/`, and historical `ota/` / `firmware/`. OTA migration includes only `ota-billable-v1/`. Keys already beneath the environment prefix are not prefixed twice. Per-object SHA-256 receipts are kept in ignored runtime state. Never rewrite a historical `ota/` key into `ota-billable-v1/`.

OTA migration inventories both unprefixed and environment-prefixed `ota-billable-v1/` objects in the current source bucket. On every run it rechecks the source and destination SHA-256 of objects already listed in `storage-migration-ota.json`; an empty source still produces a zero-object receipt. OTA cutover requires the same `--source-env-file`, verifies that its bucket and region match the live core API Deployment, repeats the source inventory and hash check against the receipt, and rejects missing or extra destination objects before changing the OTA workload. If the live core source cannot be verified, cutover stops. Stop source writes during the final migration and cutover, and rerun migration if an object changes. The cutover does not infer that an absent receipt means the source is empty.

OTA cutover requires independent OTA service registration, validated private OTA storage, and a ready deployment. Set the non-secret `VIDEO_CLOUD_OTA_CDN_BASE_URL` and optional `VIDEO_CLOUD_OTA_CDN_TOKEN_NAME` in the environment's tracked `environment.env`; keep the token key in the private `ota-cdn-runtime` Secret. CDN delivery requires a valid HTTPS base URL and token key; when both are absent, new device grants use short-lived signed GET URLs for that bucket. A partially configured CDN is an error. Direct-download cutover rejects an E0/E1 bucket or missing endpoint type. Before enabling billable direct delivery, perform a controlled signed GET, export bucket-scoped `obj_requests_get` and `obj_bytes_downloaded` for its UTC window, and archive the bucket, region, endpoint, window and export result with the cutover evidence. Endpoint type alone does not prove export. Akamai's [endpoint matrix](https://techdocs.akamai.com/cloud-computing/docs/endpoint-types) lists supported types and regional availability; [Object Storage metrics](https://techdocs.akamai.com/cloud-computing/docs/object-storage-cloud-pulse-metrics) are retained for 93 days, so month-close evidence must be exported and kept independently. If service registration is disabled, the bucket can be prepared but no cutover is recorded. `storage-retire` covers the existing media key lifecycle only: it requires cutover state plus `storage-consumers.json` confirming `generic_key_in_use: false` and revokes one explicit key ID. OTA key retirement requires separate consumer verification. No command deletes buckets.

For direct delivery, archive the Cloud Pulse export below
`runtime/artifacts/ota-metrics/` and record
`runtime/state/ota-metrics-qualification.json` before cutover or a normal OTA
deployment. The JSON fields are `source: "akamai_cloud_pulse"`, `environment`,
`bucket`, `region`, `endpoint`, `exported_at` (RFC 3339 UTC), `recorded_by`,
`export_file` (path relative to `runtime/`), `export_sha256`, `get_metric`
set to `obj_requests_get`, positive `get_requests`, `downloaded_bytes_metric`
set to `obj_bytes_downloaded`, and positive `downloaded_bytes`. The export must
contain both metric identifiers and the exact bucket and endpoint host. The
operator compares the recorded counts with the export after a controlled GET.
The deployment validator checks target match, archive digest and freshness
within 72 hours; the receipt is an operator attestation, not authentication of
Cloud Pulse's source. Preserve the export for monthly review after the
provider's retention window.

Media validation writes `runtime/state/storage-preflight.json`; OTA validation writes `runtime/state/storage-preflight-ota.json`. Receipts contain bucket, region, API endpoint, numeric key ID, redacted access suffix, and time. Runtime state should be backed up using the encrypted environment-state procedure.
