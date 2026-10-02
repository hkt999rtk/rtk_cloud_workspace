# Linode Object Storage bucket inventory

Classification: supporting-note; dated observations and migration targets.

Owner: `rtk_cloud_workspace`.

Historical observation dates: 2026-09-27 and 2026-09-28. Naming targets reviewed: 2026-10-02. A separate 2026-10-02 reconciliation appears below.

Observed through the Linode bucket API on 2026-09-27 and 2026-09-28 using the Dev and Staging operator accounts. This is a dated snapshot, not proof of current consumers or contents. Verify ownership and key scope before migration or deletion. The opening inventory and qualification notes retain those dates. The later reconciliation section records a separate observation. Naming targets follow [Object Storage Policy](object-storage-policy.md) and are not deployed-state claims.

| Observed bucket | Region | Purpose / status at observation | Naming target; migration still required |
| --- | --- | --- | --- |
| `rtk-video-media-dev-us-sea` | `us-sea` | Created and validated; one historical `ota/` object (3,968 bytes) migrated with SHA-256; workload cutover pending | `rtk-cloud-dev-runtime-us-sea` |
| `rtk-ota-firmware-dev-us-sea` | `us-sea` | Earlier E1 OTA bucket; private signed-GET probe passed on 2026-09-27. Retained without cutover or deletion. | Historical E1 candidate; no cutover; reconcile against the E3 destination before retirement |
| `rtk-ota-firmware-dev-us-lax` | `us-lax` | Created and validated as private E3 on 2026-09-28; signed-GET and Cloud Pulse qualification passed. OTA service cutover pending. | `rtk-cloud-dev-ota-firmware-us-lax`; separately qualify the new E3 destination |
| `rtk-video-dev-us-west` | `us-sea` | Existing Dev Video Cloud media; region alias in name | `rtk-cloud-dev-runtime-us-sea` |
| `rtk-clip-staging-us-sea` | `us-sea` | Legacy staging clip bucket; consumer needs verification | `rtk-cloud-staging-runtime-sg-sin-2` only after ownership and duplicate-key reconciliation |
| `rtk-video-staging-sg` | `sg-sin-2` | Configured Staging runtime media | `rtk-cloud-staging-runtime-sg-sin-2` |
| `rtk-ota-firmware-staging-sg-sin-2` | `sg-sin-2` | Created and validated as private E3 on 2026-09-28; migration receipt reports zero source and destination billable objects. OTA service cutover pending. | `rtk-cloud-staging-ota-firmware-sg-sin-2`; separately qualify the new E3 destination |
| `rtk-cloud-client-artifacts` | `us-sea` | Configured shared release artifacts | `rtk-cloud-shared-artifacts-us-sea`; retain formal and SDK key layouts |
| `rtk-cloud-dev-device-pki-backup-us-iad` | `us-iad` | Dev device PKI backup | `rtk-cloud-dev-pki-backup-us-iad`; PKI owner review |
| `rtk-cloud-staging-device-pki-backup-sg-sin-2` | `sg-sin-2` | Staging device PKI backup | `rtk-cloud-staging-pki-backup-sg-sin-2`; PKI owner review |
| `rtk-cloud-dev-recovery-us-sea` | `us-sea` | Dev recovery; ownership and contents need verification | `rtk-cloud-dev-backup-us-sea` only if it fits core recovery ownership |

No System Logger backup bucket was observed. Its future backup namespace remains reserved in the policy; no bucket is claimed to exist. The local MinIO `reports` bucket is not a Linode bucket. Production retains the historical `rtk-video-prod-us-west` compatibility name; this Dev-account inventory did not verify it exists. At that observation, the media cutover command covered only API/clip-verifier and blocked while other deployments still referenced the source. See the operations guide for the current implementation; the historical observation does not prove a workload switch.

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

Staging Cloud Pulse reported GET requests for the 2026-09-28 UTC 07:32–07:34
probe but zero downloaded bytes in those minutes. A wider window showed only
231 downloaded bytes, far below the observed 8 MiB full GET; that automatic
qualification receipt was quarantined. A second verified 2 MiB signed GET at
07:45 UTC again showed a GET but zero downloaded bytes in the samples
available at 07:55 UTC. Treat the byte metric as unresolved and do not use
the 231-byte receipt for cutover. The live Staging database lacks `ota_artifact_grants`
and `ota_download_receipts`, so the current read-only legacy drain gate fails
closed; the older `ota/` releases, campaigns, and deployments each count zero.
OTA service registration remains disabled. Do not cut over the Staging
workload until the metrics, schema, legacy drain, and deployment readiness
gates pass. The confirmed account setting is
`LKE_ACTIVE_SERVICE_LIMIT=unlimited`; the live inventory at preflight was
45 counted services, with no additional services needed for bucket creation.

Production remains outside this migration scope. Its earlier preflight was
NO-GO because runtime and GHCR credentials and the service limit were
missing. Its account, cluster, and media-bucket ownership need confirmation
before choosing an E3 OTA region and bucket name. No Production bucket, key,
Secret, or service was changed.

See [Object Storage Policy](object-storage-policy.md) for naming, namespaces, retention and migration/retirement requirements; use [storage operations](storage-credential-lifecycle.md) for credential handling and implemented cutover commands.


## Read-only reconciliation: 2026-10-02

At 08:15 UTC, the Dev operator account still contained the same 11 buckets,
7,256 current objects and 76,135,727,051 bytes reported in the account API.
A new read-only scoped-key audit verified current object lists for shared
artifacts, the active Dev media bucket, the prepared Dev media bucket, and
Dev E3 OTA. The other buckets require their own verified scoped credentials
and owner qualification before any cleanup. Current-object counts do not
include proof of every historical version or multipart upload.

The shared artifact bucket held 1,095 `ci/` objects totaling 55,817,280,975
bytes; 219 `releases/` objects totaling 5,584,342,077 bytes; 28
`pro2-examples/` objects totaling 45,231,963 bytes; and 16 `sdk/` objects
totaling 2,990,376 bytes. CI objects ranged from 2026-08-29 to 2026-10-02.
Formal releases, SDK handoffs and PRO2 keys require preservation. No lifecycle
configuration was returned for this bucket (HTTP 404). This is a concrete
retention opportunity, pending hold classification and reviewed lifecycle
installation; it is not evidence that those bytes were reclaimed.

The active Dev media bucket held two environment-prefixed objects totaling
6,016 bytes. The earlier prepared media bucket held one environment-prefixed
object totaling 3,968 bytes. Dev E3 OTA remained empty but reserved for its
qualified dedicated purpose. Scoped inspections returned no unfinished
multipart uploads for these four buckets. Policy, ACL and available CORS
configuration fingerprints were captured without exposing credentials or
policy principals. Unsupported or denied encryption/lock/CORS inspection
remains unresolved and must be qualified for the actual provider endpoint.

Five active GitHub publishers (Client, Video Cloud, Account Manager, Cloud
Admin and System Logger) still bind `rtk-cloud-client-artifacts` and its
API-observed Seattle endpoint. Dev and Staging frontend workloads both read
that bucket through `frontend-sdk-downloads`; they use `sdk/latest.json`
and the existing root `pro2-examples/` prefix. PRO2 publishing also consumes
that selected environment's artifact key. Client and Video retain legacy
bucket/endpoint secret fallbacks whose values cannot be read through the
GitHub API. Every one of these consumers must participate in artifact cutover.
The Frontend repository has an inactive historical bucket variable; record
its removal separately from active publisher cutover.

### Live mutation verdict

Both mandatory read-only credential checks returned **NO-GO** on 2026-10-02:

- Dev: installed service CRL signature/freshness validation failed, and the
  log-ingester PKI identity was not proven by an available current workload.
- Staging: the deployed broker's dynamic root reference lacked the matching
  PKI registry policy and conflicted with the fixed Device Root enforcement.

These are live PKI qualification failures outside the storage naming change.
This read-only reconciliation changed no bucket, object, lifecycle, key,
GitHub binding or live workload.
Prepared naming intent is guarded by cutover receipts. Production remains
outside migration scope. Before resuming live migration, repair and re-run the
selected environment's credential check, then complete the storage-specific
ACL/CORS/history, consumer, metrics and drain gates. Preserve this dated
observation when adding a later result.


A separate Staging-scoped audit completed at 08:39 UTC. It verified the
active Staging media bucket's two objects (2,097,152 bytes), the empty
reserved Staging E3 OTA bucket, and the shared artifact bucket using that
environment's own keys. No unfinished uploads were reported for those
inspected buckets. The same artifact scan identified 237 CI objects totaling
6,338,128,143 bytes older than the policy's 30-day default. These are retention
candidates subject to owner/hold and complete-history review; no deletion or
lifecycle activation was performed. Unknown ACL/CORS/encryption capabilities
remain explicit rather than being treated as successful migration checks.

## Shared artifact destination preparation: 2026-10-02

At 14:12 UTC, the Dev-only preparation step created
`rtk-cloud-shared-artifacts-us-sea` in `us-sea`. The provider reported an E1
endpoint. A separate private candidate profile holds a limited key that can
read and write only this destination bucket; the active artifact credentials
remain unchanged. The bootstrap canary was removed. Read-only verification
found an owner-only ACL, no public policy principal, no current objects,
versions, delete markers or incomplete multipart uploads, and confirmed that
the new key cannot list the old artifact bucket.

This records a prepared empty destination only. No source object was copied,
no CORS or source policy was applied, and no publisher, reader, workload or
credential binding was changed. The source bucket continues to receive writes.
Before copying, re-inventory its current objects and configuration. Before a
consumer cutover or retirement, complete the writer fence, exact copy and
verification, reader/publisher reconciliation, and the relevant environment
qualification. The Dev and Staging PKI findings above still block runtime
storage cutover.
