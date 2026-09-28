# Linode Object Storage bucket inventory

Observed through the Linode bucket API on 2026-09-27 and 2026-09-28 using the Dev and Staging operator accounts. This is a dated snapshot, not proof of current consumers or contents. Verify ownership and key scope before migration or deletion.

| Observed bucket | Region | Likely purpose / status | Naming target |
| --- | --- | --- | --- |
| `rtk-video-media-dev-us-sea` | `us-sea` | Created and validated; one historical `ota/` object (3,968 bytes) migrated with SHA-256; workload cutover pending | Canonical Dev media |
| `rtk-ota-firmware-dev-us-sea` | `us-sea` | Earlier E1 OTA bucket; private signed-GET probe passed on 2026-09-27. Retained without cutover or deletion. | Historical Dev OTA candidate; not the E3 target |
| `rtk-ota-firmware-dev-us-lax` | `us-lax` | Created and validated as private E3 on 2026-09-28; signed-GET and Cloud Pulse qualification passed. OTA service cutover pending. | Dev billable OTA target |
| `rtk-video-dev-us-west` | `us-sea` | Existing Dev Video Cloud media; region alias in name | `rtk-video-media-dev-us-sea` |
| `rtk-clip-staging-us-sea` | `us-sea` | Legacy staging clip bucket; consumer needs verification | `rtk-video-media-staging-sg-sin-2` if part of current staging media |
| `rtk-video-staging-sg` | `sg-sin-2` | Configured Staging runtime media | `rtk-video-media-staging-sg-sin-2` |
| `rtk-ota-firmware-staging-sg-sin-2` | `sg-sin-2` | Created and validated as private E3 on 2026-09-28; migration receipt reports zero source and destination billable objects. OTA service cutover pending. | Staging billable OTA target |
| `rtk-cloud-client-artifacts` | `us-sea` | Configured shared release artifacts | `rtk-release-shared-us-sea` |
| `rtk-cloud-dev-device-pki-backup-us-iad` | `us-iad` | Dev device PKI backup | `rtk-device-pki-backup-dev-us-iad` |
| `rtk-cloud-staging-device-pki-backup-sg-sin-2` | `sg-sin-2` | Staging device PKI backup | `rtk-device-pki-backup-staging-sg-sin-2` |
| `rtk-cloud-dev-recovery-us-sea` | `us-sea` | Dev recovery; ownership and contents need verification | `rtk-backup-dev-us-sea` only if it fits core backup ownership |

No System Logger backup bucket was observed; `rtk-logger-backup-dev-us-sea` remains reserved. The local MinIO `reports` bucket is not a Linode bucket. Production configuration retains the historical `rtk-video-prod-us-west` target; this Dev-account inventory did not verify it exists. Dev media cutover must cover all active workloads that reference the old bucket; the current API/clip-verifier cutover command blocks while other deployments still use it.

The old Dev media bucket's existing read/write key was verified and retained as two `0600` rollback entries in the Dev SecretStore (`LINODE_MEDIA_ROLLBACK_OBJ_*`). The temporary read-only migration key was revoked after the one-object copy. No live workload has been switched to either new bucket.

The Dev E3 OTA bucket probe used a temporary object outside `ota-billable-v1/`
and confirmed unsigned GET rejection, exact `206 Range`, signed URL expiry,
fresh URL resume with matching SHA-256, and object deletion. Its HTTPS URL was
423 bytes; a full synthetic 8 MiB BIN downloaded in about 9.7 seconds. The
earlier E1 probe completed the same checks in 7.314 seconds. Neither probe
verifies physical device behavior.

The assigned `us-lax` E3 endpoint and new Dev OTA bucket were verified live.
The 2026-09-28 UTC 05:20–05:24 Cloud Pulse qualification for this exact bucket
exported 6 GET requests and 16,778,980 downloaded bytes. These are bucket-level
measurements, not per-device proof. The temporary probe object was deleted.
The older `us-sea` E1 bucket remains untouched. Do not delete either bucket
until its object inventory and consumer references are verified. Akamai
supports Object Storage Cloud Pulse metrics on E2/E3 endpoints; this OTA
deployment requires E3. See the
[endpoint matrix](https://techdocs.akamai.com/cloud-computing/docs/endpoint-types)
and [metric definitions](https://techdocs.akamai.com/cloud-computing/docs/object-storage-cloud-pulse-metrics).

The Staging dedicated OTA E3 bucket was created on 2026-09-28 in `sg-sin-2`
with one scoped key. A temporary 8 MiB probe outside `ota-billable-v1/`
confirmed unsigned GET rejection, exact `206 Range`, signed URL expiry,
renewed URL resume with matching SHA-256, and complete download in about
3.1 seconds. The renewed HTTPS URL was 448 bytes. The probe object was
deleted. This provider-client test does not verify physical device behavior.
The source media bucket's unprefixed and environment-prefixed
`ota-billable-v1/` and historical `ota/` paths were empty at the migration
inventory; the OTA migration receipt records zero objects and bytes. Staging
workloads still use the historical `legacy-shared` OTA configuration.

Staging Cloud Pulse qualification for the 2026-09-28 UTC 07:32–07:34 probe
window has not yet shown positive GET and downloaded-byte metrics. The live
Staging database also lacks `ota_artifact_grants` and
`ota_download_receipts`, so the current read-only legacy drain gate fails
closed. OTA service registration remains disabled. Do not cut over the
Staging workload until the metrics, schema, legacy drain, and deployment
readiness gates pass. The confirmed account setting is
`LKE_ACTIVE_SERVICE_LIMIT=unlimited`; the live inventory at preflight was
45 counted services, with no additional services needed for bucket creation.

Production remains outside this migration scope. Its earlier preflight was
NO-GO because runtime and GHCR credentials and the service limit were
missing. Its account, cluster, and media-bucket ownership need confirmation
before choosing an E3 OTA region and bucket name. No Production bucket, key,
Secret, or service was changed.

See [storage naming and lifecycle](storage-credential-lifecycle.md) for the canonical pattern, paths, key ownership, and migration order.
