# Linode Object Storage bucket inventory

Observed through the Linode bucket API on 2026-09-27 using the Dev operator account, then updated after the Dev bootstrap on the same date. This is a dated snapshot, not proof of current consumers or contents. Target names outside the new Dev rows are design intent and are not claimed to exist. Verify ownership and key scope before migration or deletion.

| Observed bucket | Region | Likely purpose / status | Naming target |
| --- | --- | --- | --- |
| `rtk-video-media-dev-us-sea` | `us-sea` | Created and validated; one historical `ota/` object (3,968 bytes) migrated with SHA-256; workload cutover pending | Canonical Dev media |
| `rtk-ota-firmware-dev-us-sea` | `us-sea` | Created and validated; private signed-GET probe passed on 2026-09-27; OTA service registration disabled, so cutover pending | Canonical Dev billable OTA |
| `rtk-video-dev-us-west` | `us-sea` | Existing Dev Video Cloud media; region alias in name | `rtk-video-media-dev-us-sea` |
| `rtk-clip-staging-us-sea` | `us-sea` | Legacy staging clip bucket; consumer needs verification | `rtk-video-media-staging-sg-sin-2` if part of current staging media |
| `rtk-video-staging-sg` | `sg-sin-2` | Configured Staging runtime media | `rtk-video-media-staging-sg-sin-2` |
| `rtk-cloud-client-artifacts` | `us-sea` | Configured shared release artifacts | `rtk-release-shared-us-sea` |
| `rtk-cloud-dev-device-pki-backup-us-iad` | `us-iad` | Dev device PKI backup | `rtk-device-pki-backup-dev-us-iad` |
| `rtk-cloud-staging-device-pki-backup-sg-sin-2` | `sg-sin-2` | Staging device PKI backup | `rtk-device-pki-backup-staging-sg-sin-2` |
| `rtk-cloud-dev-recovery-us-sea` | `us-sea` | Dev recovery; ownership and contents need verification | `rtk-backup-dev-us-sea` only if it fits core backup ownership |

No System Logger backup bucket was observed; `rtk-logger-backup-dev-us-sea` remains reserved. The local MinIO `reports` bucket is not a Linode bucket. Production configuration retains the historical `rtk-video-prod-us-west` target; this Dev-account inventory did not verify it exists. Dev media cutover must cover all active workloads that reference the old bucket; the current API/clip-verifier cutover command blocks while other deployments still use it.

The old Dev media bucket's existing read/write key was verified and retained as two `0600` rollback entries in the Dev SecretStore (`LINODE_MEDIA_ROLLBACK_OBJ_*`). The temporary read-only migration key was revoked after the one-object copy. No live workload has been switched to either new bucket.

The Dev OTA bucket probe used a temporary object outside `ota-billable-v1/` and
confirmed unsigned GET rejection, exact `206 Range`, signed URL expiry, fresh
URL resume with matching SHA-256, and object deletion. Its HTTPS URL was 423
bytes; a full synthetic 8 MiB BIN downloaded in 7.314 seconds. This does not
verify physical device behavior or Cloud Pulse metrics. Staging and Prod OTA
bucket names remain proposed until each environment passes its own preflight,
bootstrap, evidence export and cutover.

See [storage naming and lifecycle](storage-credential-lifecycle.md) for the canonical pattern, paths, key ownership, and migration order.
