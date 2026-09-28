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

Set `RUNTIME_MEDIA_STORAGE_*` and `RUNTIME_OTA_STORAGE_*` in the environment's `storage.env`. Dedicated OTA requires `RUNTIME_OTA_STORAGE_MODE=dedicated` and the exact bucket name `rtk-ota-firmware-<environment>-<actual storage region>`, distinct from media. Use `RUNTIME_OTA_STORAGE_POLICY=colocated` when the selected Object Storage region matches compute; the OTA region then comes from `LKE_REGION`. When that region has no E2/E3 endpoint, an explicitly reviewed cross-region target uses `RUNTIME_OTA_STORAGE_POLICY=cross-region` and `RUNTIME_OTA_STORAGE_REGION=<provider region>`. The latter must differ from `LKE_REGION`; configuration validation checks the bucket's environment and actual-region suffix. Endpoint selection and Cloud Pulse qualification still use the provider's live inventory. This policy selects where the OTA bucket lives; it does not change the CDN-versus-signed-GET grant rule or enable runtime failover. `legacy-shared` preserves Staging and Prod behavior until each is independently migrated.

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

OTA migration inventories both unprefixed and environment-prefixed `ota-billable-v1/` objects in the current source bucket. On every run it rechecks the source and destination SHA-256 of objects already listed in `storage-migration-ota.json`; an empty source still produces a zero-object receipt. OTA cutover requires the same `--source-env-file`, verifies that its bucket, region, and endpoint match the live core API Deployment, and requires the live source prefix to be empty or equal to the destination prefix. It then repeats the source inventory and hash check against the receipt and rejects missing or extra destination objects before changing the OTA workload. If the live core source cannot be verified, cutover stops. Stop source writes during the final migration and cutover, and rerun migration if an object changes. The cutover does not infer that an absent receipt means the source is empty.

OTA cutover requires independent OTA service registration, validated private OTA storage, and a ready deployment. It applies the OTA NetworkPolicy and private Service, then requires a Ready EndpointSlice before writing `storage-cutover-ota.json`; later normal deployments require that matching completed receipt. Set the non-secret `VIDEO_CLOUD_OTA_CDN_BASE_URL` and optional `VIDEO_CLOUD_OTA_CDN_TOKEN_NAME` in the environment's tracked `environment.env`; keep the token key in the private `ota-cdn-runtime` Secret. CDN delivery requires a valid HTTPS base URL and token key; when both are absent, new device grants use short-lived signed GET URLs for that bucket. A partially configured CDN is an error. Direct-download cutover rejects an E0/E1 bucket or missing endpoint type. Before enabling billable direct delivery, perform a controlled signed GET, export bucket-scoped `obj_requests_get` and `obj_bytes_downloaded` for its UTC window, and archive the bucket, region, endpoint, window and export result with the cutover evidence. Endpoint type alone does not prove export. Akamai's [endpoint matrix](https://techdocs.akamai.com/cloud-computing/docs/endpoint-types) lists supported types and regional availability; [Object Storage metrics](https://techdocs.akamai.com/cloud-computing/docs/object-storage-cloud-pulse-metrics) are retained for 93 days, so month-close evidence must be exported and kept independently. If service registration is disabled, the bucket can be prepared but no cutover is recorded. `storage-retire` covers the existing media key lifecycle only: it requires cutover state plus `storage-consumers.json` confirming `generic_key_in_use: false` and revokes one explicit key ID. OTA key retirement requires separate consumer verification. No command deletes buckets.

Cut over core first while the device mTLS ingress still routes to it. Core then forwards operator and App routes and only signed `PUT /v1/device/ota/internal/upload/` to the dedicated OTA service; new device check, report and grant requests temporarily return `503`. Historical artifact GETs continue on core and its original media bucket. After the observed core rollout is complete, enable the device edge route: `/v1/device/ota/` goes to the dedicated service, while the more specific `/v1/device/ota/internal/artifact/` stays on core. Core checks each historical token's expiry and current device grant. The core preflight requires the existing device mTLS route and a passing live legacy drain check before changing the workload; the edge preflight repeats the drain check and requires the observed core cutover and Ready OTA endpoint. Keep the 503 interval short and verify device retries and its persistent report journal. Roll back in reverse order, removing the edge before restoring core handlers. Keep the media bucket and read credential until the last historical artifact URL expires and outstanding OTA data is retired.

### Drain legacy OTA before moving the device edge

1. While core still handles device OTA, stop issuing legacy `ota/` work. Close or cancel every campaign and deployment using an `ota/<brand>/<product>/<release>/firmware.bin` object. Re-upload firmware as a new release in the dedicated bucket's `ota-billable-v1/` namespace and issue a new campaign there; do not reuse a legacy release or deployment in the billable service.
2. Wait until all legacy artifact grants have expired and at least 48 hours have passed since the last legacy campaign/deployment update, grant expiry, stored event, or download receipt. This is the configured report-lateness window. Check device persistent journals where devices are reachable. Offline journals cannot be inspected centrally; the 48-hour quiet window and zero nonterminal records are the measurable cutover gate, not proof that every offline device has no journal entry.
3. Before changing the core workload, the deployment tool queries the shared live `video_cloud` database read-only. It refuses core cutover if any legacy key is noncanonical, campaign/deployment is nonterminal, grant is unexpired, the quiet window has not elapsed, or the query fails. Thus core continues serving device OTA during the wait instead of returning `503` for 48 hours. Once this check passes, switch core and confirm its rollout while `/v1/device/ota/` still reaches core. New device OTA requests then receive `503` and cannot add legacy work. Move the device edge immediately afterward; the tool repeats the live drain check before changing ingress. Archive both checks' counts, database times, and last activity with the cutover evidence.

The live drain query only covers releases whose stored object key begins `ota/`, then verifies each complete legacy key. It does not count `ota-billable-v1/` releases. The dedicated OTA service uses the same database but accepts only its own namespace; it cannot finish a legacy `ota/` deployment or its delayed device reports. Preserve the old media bucket and core artifact GET path for already issued URLs until their expiry.

For direct delivery, first perform a controlled private signed GET on the
actual E2/E3 OTA bucket. After Cloud Pulse has reported that request, export a
recent UTC interval with the operator command below. Its window is
minute-aligned and half-open `[start,end)`; the API query ends one second before
`end` so adjacent intervals cannot count the same minute twice. This command
is a **cutover qualification**, with a maximum 24-hour window. It is not a
complete monthly evidence export.

```bash
rtk-cloud deployment storage-metrics-export --environment staging --purpose ota \
  --window-start 2026-09-28T06:00:00Z --window-end 2026-09-28T07:00:00Z \
  --recorded-by '<operator-id>' --confirm video-cloud-staging
```

The command reads the environment's `LINODE_TOKEN` from its operator
SecretStore and obtains a six-hour account-wide Cloud Pulse Object Storage
service token. The provider currently rejects bucket-scoped token requests
and does not accept an `entity_id` query filter. The token is used only in
memory to query the selected region for `obj_requests_get` and
`obj_bytes_downloaded` with one-minute granularity. It requires a complete
`success` matrix, exact bucket and endpoint labels, in-window timestamps,
whole nonnegative values and positive totals. Before writing any file, the
command selects only the exact OTA bucket and endpoint series and discards the
rest of the account-wide response. It archives that filtered JSON under
`runtime/artifacts/ota-metrics/` with a SHA-256 digest and atomically
writes `runtime/state/ota-metrics-qualification.json`. It never stores the
service token. The receipt includes the UTC window, bucket hostname, endpoint,
metric totals, export time and operator identity. Subsequent deployment checks
reparse the archived series, compare both totals and require the export and
window to be within 72 hours. The operator must compare the controlled GET's
time and transferred bytes with this result; an empty interval or unrelated
traffic is not proof of qualification. The local archive and receipt are
operator-held evidence, not independent authentication of provider origin.
The first E3 run must verify the provider's actual matrix labels and response
shape against this parser; local mock tests do not establish provider output.

Cloud Pulse Object Storage metrics are bucket aggregates. They do not identify
individual devices, replace verified `downloaded` reports, or equal the
provider's monthly invoice. The provider retains these metrics for 93 days;
retain monthly exports and independent completeness proof before that window
expires. Inactive intervals can have no points, and collection delays or gaps
need separate investigation before closing a charged month.
The export follows Akamai's [service token](https://techdocs.akamai.com/linode-api/reference/post-get-token),
[metrics query](https://techdocs.akamai.com/linode-api/reference/post-read-metric),
and [Object Storage metric definitions](https://techdocs.akamai.com/cloud-computing/docs/object-storage-cloud-pulse-metrics).

Media validation writes `runtime/state/storage-preflight.json`; OTA validation writes `runtime/state/storage-preflight-ota.json`. Receipts contain bucket, region, API endpoint, numeric key ID, redacted access suffix, and time. Runtime state should be backed up using the encrypted environment-state procedure.
