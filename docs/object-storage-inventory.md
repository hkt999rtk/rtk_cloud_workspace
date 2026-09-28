# Linode Object Storage bucket inventory

Observed through the Linode bucket API on 2026-09-27 and 2026-09-28 using the Dev operator account. This is a dated snapshot, not proof of current consumers or contents. Target names outside the Dev rows are design intent and are not claimed to exist. Verify ownership and key scope before migration or deletion.

| Observed bucket | Region | Likely purpose / status | Naming target |
| --- | --- | --- | --- |
| `rtk-video-media-dev-us-sea` | `us-sea` | Created and validated; one historical `ota/` object (3,968 bytes) migrated with SHA-256; workload cutover pending | Canonical Dev media |
| `rtk-ota-firmware-dev-us-sea` | `us-sea` | Earlier E1 OTA bucket; private signed-GET probe passed on 2026-09-27. Retained without cutover or deletion. | Historical Dev OTA candidate; not the E3 target |
| `rtk-ota-firmware-dev-us-lax` | `us-lax` | Created and validated as private E3 on 2026-09-28; signed-GET and Cloud Pulse qualification passed. OTA service cutover pending. | Dev billable OTA target |
| `rtk-video-dev-us-west` | `us-sea` | Existing Dev Video Cloud media; region alias in name | `rtk-video-media-dev-us-sea` |
| `rtk-clip-staging-us-sea` | `us-sea` | Legacy staging clip bucket; consumer needs verification | `rtk-video-media-staging-sg-sin-2` if part of current staging media |
| `rtk-video-staging-sg` | `sg-sin-2` | Configured Staging runtime media | `rtk-video-media-staging-sg-sin-2` |
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

Staging and Prod remain on their tracked `legacy-shared` policy. The
2026-09-28 Staging read-only preflight is NO-GO because the environment-local
`LKE_ACTIVE_SERVICE_LIMIT` is unset; the proposed E3 OTA bucket
`rtk-ota-firmware-staging-sg-sin-2` has not been created. The Prod preflight
is NO-GO because runtime and GHCR credentials and that service limit are
missing. Its account, cluster and media-bucket ownership also need
confirmation before choosing an E3 OTA region and bucket name. No protected
environment bucket, key, Secret or service was changed.

See [storage naming and lifecycle](storage-credential-lifecycle.md) for the canonical pattern, paths, key ownership, and migration order.
