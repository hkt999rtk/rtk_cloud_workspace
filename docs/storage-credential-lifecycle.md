# Linode Object Storage Operations and Credentials

Status: active operational procedure; live qualification and cutover are environment-specific.

Classification: source for operator commands and credential handling.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-10-03.

[Object Storage Policy](object-storage-policy.md) is the sole authority for bucket
names, object namespaces, retention, creation, migration and retirement rules.
This guide describes the implemented commands and service-specific gates. The
[dated inventory](object-storage-inventory.md) distinguishes observed resources
from naming targets. Runtime intent is in `cloud_env/<environment>/storage.env`;
shared release intent is in `cloud_deploy/storage/release-artifacts.env`.
The CLI discovers endpoints from Linode's API.

Names observed on 2026-09-27/28 are historical inventory, not proof that the
resources still exist. Dedicated Dev and Staging OTA buckets were privately validated as E3
on 2026-09-28; workload cutover and each environment's remaining qualification
are separate gates. A new naming target does not inherit that qualification.
Production's account ownership and assigned E3 endpoint still require verification.

## Configuration and credentials

Set `RUNTIME_MEDIA_STORAGE_*` and `RUNTIME_OTA_STORAGE_*` in the environment's `storage.env`. Dedicated OTA requires `RUNTIME_OTA_STORAGE_MODE=dedicated` and a dedicated bucket selected under [the naming policy](object-storage-policy.md#bucket-naming-and-boundaries), distinct from media. Existing configured names are compatibility exceptions until migration passes. Use `RUNTIME_OTA_STORAGE_POLICY=colocated` when the selected Object Storage region matches compute; the OTA region then comes from `LKE_REGION`. When the colocated endpoint cannot pass the required E3 or transfer qualification, an explicitly reviewed cross-region target uses `RUNTIME_OTA_STORAGE_POLICY=cross-region` and `RUNTIME_OTA_STORAGE_REGION=<provider region>`. The latter must differ from `LKE_REGION`; configuration validation checks the bucket's environment and actual-region suffix. Record the approved target and reason in the naming policy. Endpoint selection and Cloud Pulse qualification still use the provider's live inventory. This policy selects where the OTA bucket lives; it does not change the CDN-versus-signed-GET grant rule or enable runtime failover. `legacy-shared` preserves a legacy environment until its dedicated migration passes. Tracked `dedicated` intent alone does not mean its live workload has switched.

When a configured media target has been prepared but the live workloads still use the old bucket, set `RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED=true` for that environment. Normal create, upgrade, provision, and test commands then require a matching `storage-cutover.json` receipt before changing workloads. It must match the environment, bucket, region and prefix, contain a valid past cutover time, and bind the unchanged migration proof to a completed private journal with the same cutover ID. A copied or edited receipt alone cannot satisfy the gate. Bootstrap and migration remain available. Keep the gate until the live cutover and rollback check are complete; a tracked bucket name alone does not authorize a deployment switch.

Credentials are individual `0600` files below `~/.config/rtk_cloud/<environment>/operator/env/`. Media uses `LINODE_MEDIA_OBJ_ACCESS_KEY_ID` / `LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY`; OTA uses `LINODE_OTA_OBJ_ACCESS_KEY_ID` / `LINODE_OTA_OBJ_SECRET_ACCESS_KEY`. Each key is limited to its bucket. The OTA pod receives the OTA key through the `ota-object-storage` Kubernetes Secret; API and clip workers keep the media key. Shared release uses `LINODE_ARTIFACT_OBJ_*`. Never commit secrets.

## Operator lifecycle

### Environment-owned receipts and proof location

All environments use `~/.config/rtk_cloud/<environment>/deployment/storage/`
for authoritative storage evidence (directories `0700`, files `0600`).
`RTK_CLOUD_CONFIG_ROOT` changes the parent configuration root, never the
environment binding. This state is independent of the workspace or worktree.

| File | Written after |
| --- | --- |
| `storage-preflight.json` | Explicit Media storage write/read/delete qualification |
| `storage-preflight-ota.json` | Explicit OTA storage qualification |
| `storage-preflight-release-artifacts.json` | Explicit artifact storage qualification |
| `storage-migration.json`, `storage-migration-ota.json`, `storage-migration-artifacts.json` | Verified object migration; the completed cutover binds the unchanged proof digest |
| `storage-cutover.json`, `storage-cutover-ota.json` | Successful cutover or reinitialization, workload verification, credential promotion and completed private journal |

The private journals remain under the same environment's `migration-backup/`;
they contain sensitive original workload/Secret state and are not copied into
reports. Preserve receipts, migration proofs and journals together in the
encrypted environment backup. `cloud_env/<environment>/runtime/state/` is no
longer the authority for these files. Normal reads never search old worktrees.

Preflight and deployment use the same storage activation verifier. Missing,
wrong-environment or mismatched receipt/proof/journal is NO-GO. Preflight is
read-only: it neither performs a cutover nor produces a completion receipt.
Explicit storage qualification can produce a preparation receipt, which alone
does not prove cutover. Full deployment binds the current storage evidence into
its plan input digest and repeats checks before execution stages.

For existing completed operations, explicitly import the original evidence:

```sh
./scripts/deploy-environment.sh storage-import-state \
  --environment staging --purpose media \
  --source-runtime /absolute/original/cloud_env/staging/runtime \
  --confirm video-cloud-staging
```

Repeat with `--purpose ota` and its original runtime when necessary. Import
checks the selected environment/target and completed private journal, binds the
unchanged migration or reinitialization proof, preserves original bytes and
completion times, and refuses conflicting canonical state. Identical retries
are idempotent. No cloud resources or active credentials change. A partial
import remains subject to the same deployment gates. Import is not GO; repeat
the full preflight. Do not rerun `storage-reinitialize` merely because a checkout
lost its generated files, edit a receipt, or disable the activation gate.

Follow [the migration and retirement policy](object-storage-policy.md#existing-bucket-migration-and-retirement) before using these commands. Fetch the latest workspace and submodule refs, inspect live buckets, then run `storage-plan`. The command selectors `--purpose media`, `--purpose ota` and `--purpose artifacts` address policy purposes `runtime`, `ota-firmware` and `artifacts`.

```bash
rtk-cloud object-storage-audit --environment dev --inspect --out /secure/dev-storage-audit.json
rtk-cloud deployment storage-plan --environment dev
rtk-cloud deployment storage-bootstrap --environment dev --purpose media --destination-env-file /secure/dev-media-candidate.env --confirm video-cloud-dev
rtk-cloud deployment storage-bootstrap --environment dev --purpose ota --destination-env-file /secure/dev-ota-candidate.env --confirm video-cloud-dev
rtk-cloud deployment storage-migrate --environment dev --purpose media --source-env-file /secure/media-source.env --destination-env-file /secure/dev-media-candidate.env --confirm video-cloud-dev
rtk-cloud deployment storage-migrate --environment dev --purpose ota --source-env-file /secure/ota-source.env --destination-env-file /secure/dev-ota-candidate.env --confirm video-cloud-dev
rtk-cloud deployment storage-cutover --environment dev --purpose media --source-env-file /secure/media-source.env --destination-env-file /secure/dev-media-candidate.env --confirm video-cloud-dev
rtk-cloud deployment storage-cutover --environment dev --purpose ota --source-env-file /secure/ota-source.env --destination-env-file /secure/dev-ota-candidate.env --confirm video-cloud-dev
```

`object-storage-audit` reads the account's bucket and key inventory. `--inspect` also reads accessible object inventories and bucket configuration, including versioning, the presence of versions or incomplete multipart uploads, lifecycle, access policy, ACL, CORS and encryption. This audit is not a complete historical-version transfer inventory. It records access failures as unresolved. `--out` saves a private report; the command does not write to the provider or authorize deletion. Account access does not imply that the available S3 keys can inspect every bucket.

To prepare a lifecycle review without changing bucket rules:

```bash
rtk-cloud object-storage-lifecycle-plan --environment dev --candidate-profile /secure/dev-media-candidate.env --purpose media --out /secure/dev-media-lifecycle-plan.json
rtk-cloud object-storage-lifecycle-plan --environment dev --candidate-profile /secure/artifacts-candidate.env --purpose artifacts --out /secure/artifacts-lifecycle-plan.json
```

The private plan includes the exact existing lifecycle XML, its hash and read status, plus proposed XML for the policy-owned namespaces. It is a review artifact, not a replacement configuration. Check held evidence, existing rules, provider support and consumer ownership before separately applying any rule. `--abort-incomplete-days <1..365>` adds a proposed incomplete multipart rule only after an explicit upload-duration review. Missing access or bucket configuration remains unresolved; the planner makes no provider writes.

Bootstrap, migration and cutover require `--destination-env-file`: an absolute path to a private `0600` candidate profile, separate from the active operator profile. Bootstrap creates the marked candidate and writes the selected purpose's replacement credentials there. Migration and cutover reuse that candidate. Active credentials are promoted only after the workload rollout and service verification pass. Preserve the candidate, source profile and private rollback records through the observation period.

Creation and retention rules are defined in [Object Storage Policy](object-storage-policy.md#registration-and-creation). `--purpose media` is the compatibility default. OTA bootstrap requires an E3 endpoint type available to the account in the selected region, including when CDN is configured. The [endpoint inventory](https://techdocs.akamai.com/linode-api/reference/get-object-storage-endpoints) can report `s3_endpoint: null` before that type has been assigned; this permits requesting `endpoint_type: E3` for the first bucket. The created bucket must independently report E3 and its own valid assigned endpoint before a key or storage receipt is created. No other regional endpoint fills in a missing bucket endpoint. An existing E0/E1/E2 bucket is refused before creating another key. Reusing the same bucket name does not change its endpoint type. Private access, key validation, transfer qualification and activation gates remain required after creation.

Media migration includes `clips/`, `brands/`, `snapshots/`, `clip-index/`, and historical `ota/` / `firmware/`. OTA migration includes only `ota-billable-v1/`. Both require a preserved `--source-env-file` for migration and cutover, with the source bucket, region, endpoint and credentials. An explicit `LINODE_OBJ_PREFIX` binds the source prefix, including an explicitly empty value. The compatibility behavior when it is absent inventories both legacy unprefixed and environment-prefixed keys. Preserve logical keys; never rewrite a historical `ota/` key into `ota-billable-v1/`.

Migration receipts bind the full source and destination identities and physical key mapping. They record each object's SHA-256 and attributes. A resumed copy rechecks recorded source objects and every corresponding destination's bytes, metadata and tags; it can copy newly discovered source keys. Missing or changed recorded source objects and conflicting destination objects stop the copy. Media and OTA cutover additionally require an exact destination inventory for their selected namespaces, rejecting missing or extra objects before switching consumers. Copy completion alone is not cutover evidence.

Enabled/suspended versioning, non-null version history, delete markers and incomplete multipart uploads stop these operations. The commands copy current objects only; they cannot transfer version history or preserve historical version IDs. Unsupported composite multipart checksums also require a separately qualified transfer procedure. An empty source produces a zero-object receipt; a missing receipt never means the source is empty. Stop writes and deletes through the final copy, verification and cutover, and rerun the reviewed migration if data changes.

Media cutover inventories Deployments, StatefulSets, DaemonSets, Jobs, CronJobs, ReplicaSets and Pods, including regular, init and ephemeral containers. It resolves `envFrom` and `valueFrom` Secret/ConfigMap references for source identification, honoring source order and explicit environment overrides. Indirect source settings require a reviewed literal mapping before automatic cutover. Standalone source-bound Pods block cutover; managed source Pods must belong to a selected controller and terminate before completion. It verifies literal source bucket, endpoint, region and prefix before changing matched consumers, including API variants, videostorage, cleaner, verifier and legacy OTA. Unfinished Jobs must be finished or recreated separately; affected CronJobs must already be suspended. Indirect or ambiguous storage settings require explicit reconciliation. Matching consumers outside the selected stack block the switch. Changes use the recorded Kubernetes object identity, resource version and old environment values; concurrent changes stop the operation. Source credential Secrets and old settings are retained privately. Rollout checks and a repeat consumer inventory precede completion.

OTA cutover verifies the existing dedicated OTA Deployment's source, or the live core API source when introducing the dedicated service, before changing workloads. Its bucket, region, endpoint and prefix must match the verified migration. Preserve the separate registration, metrics, routing and legacy drain gates below.

### Reinitialize after an authorized source deletion

When an owner has explicitly authorized discarding the source data and the
bucket has already been deleted, use `storage-reinitialize`. Do not manufacture
an empty migration receipt or remove the deployment activation gate.

First bootstrap and verify the canonical destination with an isolated candidate
profile. Create a private source identity file containing
`RTK_STORAGE_SOURCE_ENVIRONMENT`, `LINODE_OBJ_BUCKET`, `LINODE_OBJ_REGION`,
`LINODE_OBJ_ENDPOINT` and an explicit `LINODE_OBJ_PREFIX` (which can be empty
for a historical root prefix). Deleted source credentials are not required.

```bash
rtk-cloud deployment storage-reinitialize --environment dev --purpose media \
  --source-env-file /secure/deleted-media-source.env \
  --destination-env-file /secure/dev-media-candidate.env \
  --acknowledge-discarded-source <exact-deleted-bucket> --plan
# Execute the reviewed plan with the same identities:
rtk-cloud deployment storage-reinitialize --environment dev --purpose media \
  --source-env-file /secure/deleted-media-source.env \
  --destination-env-file /secure/dev-media-candidate.env \
  --acknowledge-discarded-source <exact-deleted-bucket> --confirm video-cloud-dev
```

The read-only plan proves source absence through the full provider inventory
and verifies that the destination has no current objects, versions, delete
markers or incomplete multipart uploads. It checks privacy, exact scoped
credentials and the existing consumer mapping. Execution repeats these checks,
validates a bounded write/read/delete canary, and updates consumers with guarded
Kubernetes changes while preserving their images. A failed rollout does not
activate the candidate credentials or produce a completed receipt.

The private journal and receipt explicitly record `operation=reinitialize`,
the acknowledged discarded source, destination proof and unavailable data
rollback. The deployment gate accepts this completed proof separately from
migration evidence. `storage-rollback` refuses a deleted-source journal;
recovery requires repairing or reconciling the destination, including any new
writes. Preserve a failed journal before a separately reviewed retry.

`--purpose ota` retains E3 endpoint, registration, metrics/CDN and new
ready-service gates. It repairs only the storage binding of an existing dedicated
OTA Deployment and its owned Pods. The existing dedicated command, image and
registration must be preserved. A selected and observed completed core cutover
and correct device edge, including the historical artifact route to core, permits
this repair without repeating the legacy drain. Otherwise the original full
legacy drain is required, so storage can be repaired before its initial handoff.
The chosen handoff state, core and ingress identities and configuration are pinned and
rechecked before the journal and credential promotion; drift stops activation.
The deleted-source OTA service need not already be Ready, but the repaired service
must have a Ready endpoint before credentials or its receipt are activated.

This bounded repair does not introduce a service or move core handlers or device
routes. The full legacy database drain and 48-hour quiet window below remain
mandatory for those handoffs. Discarding old firmware does not waive that drain or the
billing and metering gates. There is no operator switch to skip the drain.

With `RUNTIME_OTA_STORAGE_MODE=dedicated`, media reinitialization leaves the
exact existing OTA Deployment and its owned ReplicaSets and Pods unchanged.
Media may be activated while OTA qualification is still blocked; qualify and
activate OTA separately with `--purpose ota`. Unknown or foreign source-bound
OTA consumers still require an explicit owner mapping. An excluded OTA consumer
must not reference any destination Secret that the media operation would write.

Keep the excluded OTA controller tree steady during this bounded operation.
The media plan pins its UIDs, specifications and resolved storage bindings,
then checks them again before mutation and credential promotion. OTA Pod or
ReplicaSet replacement, removal, or specification changes stop the operation
without a completed receipt; preserve its private journal and review a new plan.
Routine readiness/status changes do not invalidate this snapshot. Candidate and
source proof hashes are derived from the same private bytes used for parsing;
profile replacement during provider validation also stops activation.

### Shared artifact preparation

```bash
rtk-cloud deployment storage-bootstrap --environment dev --purpose artifacts --destination-env-file /secure/artifacts-candidate.env --confirm video-cloud-dev
rtk-cloud deployment storage-migrate --environment dev --purpose artifacts --source-env-file /secure/artifacts-source.env --destination-env-file /secure/artifacts-candidate.env --confirm video-cloud-dev
```

Artifact migration copies all source keys unchanged, including retained release, SDK, PRO2 and recovery layouts. `--purpose artifacts` supports preparation and copying only. Its consumers include external CI, release publishers, SDK readers, backup tools and download links. After the final write fence, manually reconcile the complete source and destination inventories, including any extra destination keys, and verify every external binding through a separately reviewed cutover. Runtime cutover and rollback reject this purpose. Copy completion does not activate artifact credentials or prove those consumers have switched.

### Rollback and retirement

```bash
rtk-cloud deployment storage-rollback --environment dev --purpose media --destination-env-file /secure/dev-media-candidate.env --confirm video-cloud-dev
rtk-cloud deployment storage-rollback --environment dev --purpose ota --destination-env-file /secure/dev-ota-candidate.env --confirm video-cloud-dev
```

Cutover saves a private journal under the environment SecretStore's `migration-backup/` before changing Kubernetes. It binds the cluster UID, migration receipt hash, source and candidate profiles, original settings and Secret snapshots. Rollback checks the same cluster, resource identities and expected settings, then restores workloads and the previous active credential pair. It refuses to overwrite concurrent changes. Keep the journal private and preserve it for review before another attempt.

Rollback first rechecks both stores against the original migration proof. Any new, changed or deleted destination object blocks switching back. Fence writers and reconcile destination changes back to the source through a reviewed recovery procedure; the command does not reverse-copy data or automatically accept a replacement proof. Failed rollout or credential promotion uses the same data and concurrency checks before automatic restoration. A blocked restoration retains its journal for operator recovery.

`storage-retire --purpose media --key-id <id>` revokes only the exact preserved source key after a completed, matching cutover and typed `storage-consumers.json` evidence. The evidence binds environment, cutover, source, destination and key ID; covers the entire bucket's consumers; and records the elapsed post-cutover observation window, issued URL expiry and recent checks confirming the key is unused. Missing or incomplete evidence, an unrestricted or multi-bucket key, active credentials, or retained artifact/OTA storage block retirement. OTA and artifact keys require separately verified provider key management. No command deletes buckets.

OTA cutover requires independent OTA service registration, validated private OTA storage, and a ready deployment. It applies the OTA NetworkPolicy and private Service, then requires a Ready EndpointSlice before writing `storage-cutover-ota.json`; later normal deployments require that matching completed receipt. Set the non-secret `VIDEO_CLOUD_OTA_CDN_BASE_URL` and optional `VIDEO_CLOUD_OTA_CDN_TOKEN_NAME` in the environment's tracked `environment.env`; keep the token key in the private `ota-cdn-runtime` Secret. CDN delivery requires a valid HTTPS base URL and token key; when both are absent, new device grants use short-lived signed GET URLs for that bucket. A partially configured CDN is an error. Direct-download cutover rejects an E0/E1/E2 bucket or missing endpoint type. Before enabling billable direct delivery, perform a controlled signed GET, export bucket-scoped `obj_requests_get` and `obj_bytes_downloaded` for its UTC window, and archive the bucket, region, endpoint, window and export result with the cutover evidence. Endpoint type alone does not prove export. Akamai's [endpoint matrix](https://techdocs.akamai.com/cloud-computing/docs/endpoint-types) lists supported types and regional availability; [Object Storage metrics](https://techdocs.akamai.com/cloud-computing/docs/object-storage-cloud-pulse-metrics) are retained for 93 days, so month-close evidence must be exported and kept independently. If service registration is disabled, the bucket can be prepared but no cutover is recorded.

Cut over core first while the device mTLS ingress still routes to it. Core then forwards operator and App routes and only signed `PUT /v1/device/ota/internal/upload/` to the dedicated OTA service; new device check, report and grant requests temporarily return `503`. Historical artifact GETs continue on core and its original media bucket. After the observed core rollout is complete, enable the device edge route: `/v1/device/ota/` goes to the dedicated service, while the more specific `/v1/device/ota/internal/artifact/` stays on core. Core checks each historical token's expiry and current device grant. The core preflight requires the existing device mTLS route and a passing live legacy drain check before changing the workload; the edge preflight repeats the drain check and requires the observed core cutover and Ready OTA endpoint. Keep the 503 interval short and verify device retries and its persistent report journal. Roll back in reverse order, removing the edge before restoring core handlers. Keep the media bucket and read credential until the last historical artifact URL expires and outstanding OTA data is retired.

### Drain legacy OTA before moving the device edge

1. While core still handles device OTA, stop issuing legacy `ota/` work. Close or cancel every campaign and deployment using an `ota/<brand>/<product>/<release>/firmware.bin` object. Re-upload firmware as a new release in the dedicated bucket's `ota-billable-v1/` namespace and issue a new campaign there; do not reuse a legacy release or deployment in the billable service.
2. Wait until all legacy artifact grants have expired and at least 48 hours have passed since the last legacy campaign/deployment update, grant expiry, stored event, or download receipt. This is the configured report-lateness window. Check device persistent journals where devices are reachable. Offline journals cannot be inspected centrally; the 48-hour quiet window and zero nonterminal records are the measurable cutover gate, not proof that every offline device has no journal entry.
3. Before changing the core workload, the deployment tool queries the shared live `video_cloud` database read-only. It refuses core cutover if any legacy key is noncanonical, campaign/deployment is nonterminal, grant is unexpired, the quiet window has not elapsed, or the query fails. Thus core continues serving device OTA during the wait instead of returning `503` for 48 hours. Once this check passes, switch core and confirm its rollout while `/v1/device/ota/` still reaches core. New device OTA requests then receive `503` and cannot add legacy work. Move the device edge immediately afterward; the tool repeats the live drain check before changing ingress. Archive both checks' counts, database times, and last activity with the cutover evidence.

The live drain query only covers releases whose stored object key begins `ota/`, then verifies each complete legacy key. It does not count `ota-billable-v1/` releases. The dedicated OTA service uses the same database but accepts only its own namespace; it cannot finish a legacy `ota/` deployment or its delayed device reports. Preserve the old media bucket and core artifact GET path for already issued URLs until their expiry.

For direct delivery, first perform a controlled private signed GET on the
actual E3 OTA bucket. After Cloud Pulse has reported that request, export a
recent UTC interval with the operator command below. Its window is
minute-aligned and half-open `[start,end)`. The provider query includes `end`,
so even a one-minute interval sends a full 60 seconds. Before archiving, the
tool removes points exactly at `end`; adjacent local intervals cannot count
the same boundary twice. Every other out-of-window point is rejected. This command
is a **cutover qualification**, with a maximum 24-hour window. It is not a
complete monthly evidence export.

```bash
rtk-cloud deployment storage-metrics-export --environment staging --purpose ota \
  --destination-env-file /secure/staging-ota-candidate.env \
  --window-start '<recent-start-utc>' --window-end '<recent-end-utc>' \
  --probe-evidence-file /secure/staging-ota-probe.json \
  --recorded-by '<operator-id>' --confirm video-cloud-staging
```

Replace the window placeholders with minute-aligned RFC3339 UTC timestamps
(for example, `YYYY-MM-DDTHH:MM:00Z`) that include the controlled test and satisfy
the 72-hour freshness limit. Do not reuse a historical qualification window.

Create the private `0600` probe evidence from a completed, verified download.
It records only successful `GetObject` response bodies (`200` or `206`), whose
contents matched the fixture. Exclude `ListObjects` bodies, denied or expired
GETs, and protocol overhead. For a range/resume test, count each verified range
body once; verify the reconstructed fixture digest. The selected window must
contain the probe and the reported provider samples, including any collection
delay. Its identity and window must exactly match the export:

```json
{
  "version": 1,
  "environment": "staging",
  "bucket": "rtk-cloud-staging-ota-firmware-sg-sin-2",
  "region": "sg-sin-2",
  "endpoint": "https://sg-sin-1.linodeobjects.com",
  "started_at": "<probe-start-UTC>",
  "completed_at": "<probe-completion-UTC>",
  "window_start": "<selected-minute-UTC>",
  "window_end": "<selected-minute-UTC>",
  "fixture_sha256": "<verified-64-character-lowercase-SHA-256>",
  "successful_get_requests": 3,
  "successful_downloaded_bytes": 16777216
}
```

The example counts describe an 8 MiB fixture downloaded once through two
verified ranges and once through a full GET. Use the actual completed probe's
counts, times and digest. The tool has no arbitrary minimum-byte override.

The command reads `LINODE_TOKEN` from the selected credential profile; the
example uses the candidate prepared from the environment's operator profile.
It obtains a six-hour account-wide Cloud Pulse Object Storage service token. The provider currently rejects bucket-scoped token requests
and does not accept an `entity_id` query filter. The token is used only in
memory to query the selected region for `obj_requests_get` and
`obj_bytes_downloaded` with one-minute granularity. It requires a complete
`success` matrix, exact bucket and endpoint labels, in-window timestamps,
whole nonnegative values, and GET/download totals at least as large as the
verified successful probe bodies by default. When the operator has authorized
a small measurement variance, add `--accept-small-probe-shortfall` to this
export command. It permits a downloaded-byte shortfall only when the actual
gap is both at most 1,024 bytes and at most one basis point (0.01%) of the
verified successful body total. The relative limit uses exact integer
arithmetic, rounded down to whole bytes. GET totals must still cover every
verified successful request. A positive count of error-response bytes,
larger partial payload accounting or unrelated traffic does not establish qualification.
Before writing any file, the
command selects only the exact OTA bucket and endpoint series and discards the
rest of the account-wide response. It writes the filtered JSON and exact probe
evidence to separate `0600` files under `runtime/artifacts/ota-metrics/`, using
exclusive creation and file sync. It then atomically writes
`runtime/state/ota-metrics-qualification.json`. It never stores the service
token. The receipt binds both archive SHA-256 values and includes the UTC window, bucket hostname, endpoint,
metric totals, export time and operator identity. If a small shortfall was
explicitly accepted, the receipt and command output record the actual gap as
`accepted_probe_shortfall_bytes`; the provider totals and verified probe remain
unchanged. A missing or zero field retains strict byte coverage. Subsequent deployment checks
reparse both archives, check the exact target/window and successful probe
totals, recompute any accepted gap and require an exact receipt match within
both limits, compare the metric totals and require the export and window to be
within 72 hours. Existing receipts without probe evidence must be requalified.
The operator remains responsible for recording the actual verified bodies and
isolating the controlled interval; bucket aggregates cannot attribute traffic
to individual requests. The local archives and receipt are
operator-held evidence, not independent authentication of provider origin.
The 2026-09-28 Dev E3 run verified the provider's actual matrix labels and
response shape against this parser. Requalify each bucket and environment
separately before cutover.

Cloud Pulse Object Storage metrics are bucket aggregates. They do not identify
individual devices, replace verified `downloaded` reports, or equal the
provider's monthly invoice. The provider retains these metrics for 93 days;
retain monthly exports and independent completeness proof before that window
expires. Inactive minutes can appear as empty-string values in a returned
series; the qualification sum skips those placeholders and still requires
provider totals that cover the verified successful probe bodies, subject only
to the explicitly accepted bounded variance above. Large undercounts remain
blocked. Collection
delays or gaps need separate investigation before closing a charged month.
The export follows Akamai's [service token](https://techdocs.akamai.com/linode-api/reference/post-get-token),
[metrics query](https://techdocs.akamai.com/linode-api/reference/post-read-metric),
and [Object Storage metric definitions](https://techdocs.akamai.com/cloud-computing/docs/object-storage-cloud-pulse-metrics).

Media validation writes `~/.config/rtk_cloud/<environment>/deployment/storage/storage-preflight.json`; OTA validation writes `storage-preflight-ota.json` in that same directory. Receipts contain bucket, region, API endpoint, numeric key ID, redacted access suffix, and time. Preserve this evidence with the environment's encrypted state backup.
