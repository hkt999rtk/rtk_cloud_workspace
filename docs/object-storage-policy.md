# Object Storage Policy

Status: active workspace policy; existing resources require explicit migration.

Classification: source.

Owner: `rtk_cloud_workspace`; each registered bucket has an accountable service owner.

Last reviewed: 2026-10-02.

Applies to: RTK-managed Linode Object Storage buckets and their object namespaces.
Local MinIO fixtures are outside the Linode inventory. Shared wire and payload
contracts remain authoritative in `repos/rtk_cloud_contracts_doc`.

## Authority and Deployment State

This is the sole workspace source for bucket naming, object namespace ownership,
retention defaults, creation, migration and retirement rules. Operational commands
and OTA qualification remain in [storage operations](storage-credential-lifecycle.md).
[Release governance](artifact-release-governance.md) owns artifact manifests and
release verification; [core recovery](backup-restore.md) owns matched backup and
restore procedures. Those documents reference this policy instead of redefining it.

Policy, tracked intent and live state are distinct. Environment intent is in
`cloud_env/<environment>/storage.env`; shared release intent is in
`cloud_deploy/storage/release-artifacts.env`. The [dated inventory](object-storage-inventory.md)
records observations and proposed destinations. A compliant name or configuration
change does not prove that a bucket, lifecycle rule, key or workload is deployed.
Existing names remain explicit compatibility exceptions until their consumers
have migrated. Production migration requires its own verified inventory and plan.

## Bucket Naming and Boundaries

New buckets use:

```text
rtk-cloud-<scope>-<purpose>-<region>
```

- `scope` is a registered logical environment, such as `dev`, `staging` or
  `prod`. `shared` is allowed only for `artifacts`, `test` or `reports` with a
  documented consumer shared across environments. Runtime, OTA, backup and PKI
  buckets remain environment-owned.
- `purpose` is one of `runtime`, `artifacts`, `backup`, `pki-backup`, `test`,
  `reports` or `ota-firmware`. A new purpose requires an explicit policy change.
- `region` is the actual Linode Object Storage region ID, such as `us-sea`,
  `us-iad`, `us-lax` or `sg-sin-2`; it is not a historical alias or the S3
  signing-region value. Record the exact assigned endpoint separately.
- Use only lowercase ASCII letters, digits and single separating hyphens;
  start and end with a letter or digit, and stay within 63 characters. Check
  availability in the target region before creation. No arbitrary suffixes.
- Customer, brand, product, release, date, issue and run IDs belong in object
  keys. Routine CI, tests and releases reuse a registered bucket and allocate
  an owned prefix. Tests of bucket creation itself need a recorded temporary
  exception, owner and expiry before creating an otherwise noncanonical bucket.

Create a separate bucket when access, environment, privacy, recovery, retention,
endpoint qualification or billing measurement needs a separate boundary. A prefix
alone does not isolate credentials. Do not merge unrelated security boundaries
merely to reduce the bucket count.

| Purpose | Naming example | Boundary |
| --- | --- | --- |
| `runtime` | `rtk-cloud-dev-runtime-us-sea` | Environment-owned product media and compatible service namespaces. |
| `ota-firmware` | `rtk-cloud-dev-ota-firmware-us-lax` | Dedicated private E3 OTA bucket, separate writer, credentials and delivery/billing evidence. |
| `artifacts` | `rtk-cloud-shared-artifacts-us-sea` | Formal releases and SDK handoffs; CI prefixes have separate retention. |
| `backup` | `rtk-cloud-dev-backup-us-sea` | Private recovery data with reviewed restore and retention requirements. |
| `pki-backup` | `rtk-cloud-dev-pki-backup-us-iad` | PKI owner and recovery boundary; separate from general artifacts and tests. |
| `test` | `rtk-cloud-staging-test-sg-sin-2` | Disposable test payloads with registered run prefixes. |
| `reports` | `rtk-cloud-shared-reports-us-sea` | Routine reports and separately held qualification evidence. |

These are naming examples, not provisioned-resource claims. A future System
Logger backup may use the `backup` bucket and its own namespace only if access,
retention and recovery requirements agree with that bucket's owner. Otherwise
review its boundary before provisioning. An empty planned PKI or OTA bucket can
remain `reserved`; lack of objects is not evidence that it is waste.

The dedicated OTA E3 requirement remains in force for CDN and signed-GET
delivery. Dev's approved region is `us-lax`; Staging's is `sg-sin-2`. A newly
named destination must independently pass endpoint, signed URL, metrics and
service cutover qualification. A rename cannot upgrade an E1 bucket to E3.
Production has no approved destination until ownership and E3 availability are
verified; preserve its explicit legacy configuration meanwhile.

## Object Namespaces and Compatibility

Use `/` separators and stable producer-owned prefixes. New free-form segments
use lowercase ASCII letters, digits, `-`, `_` and `.`; reject empty segments,
`.`/`..` segments, leading `/`, repeated `/`, whitespace and embedded secrets.
Use UTC for time-derived segments. Opaque identifiers and established filenames
retain their existing representation; do not lowercase an existing key or
replace a stored identifier as part of bucket migration.

| Data | Registered key shape or compatibility rule |
| --- | --- |
| Formal release | `releases/<artifact-name>-<version>/<version>.tar.gz`, matching `.tar.gz.sha256`, and `manifest.json`. Version-addressed and immutable after publication. |
| SDK release | Preserve `sdk/releases/<version>/...` and `sdk/latest.json`. The latter is an existing discovery pointer, not permission for a formal deployment to infer `latest`. |
| Pro2 examples | Preserve `pro2-examples/<environment>/releases/<version>/...` and `pro2-examples/<environment>/latest.json`. |
| Runtime media | Preserve `environments/<stack>/` and service-owned `clips/`, `brands/`, `snapshots/`, `clip-index/` and existing compatible namespaces beneath it. |
| Registered OTA | Preserve `environments/<stack>/ota-billable-v1/<brand>/<product>/<release>/firmware.bin` in the dedicated OTA bucket. |
| Legacy OTA | Preserve stored `ota/` and historical `firmware/` keys, references and issued URLs in legacy media until the separate drain/migration procedure passes. Never rewrite them as billable `ota-billable-v1/` objects. |
| Core recovery | Preserve configured `<remote.prefix>/<stack>/<backup-id>.age` and its `<backup-id>.complete.json` companion. Do not turn this implemented archive layout into a directory-per-backup layout. |
| Daily PostgreSQL recovery | `<remote.prefix>/<stack>/<cluster_id>/<backup-id>.age`, matching `<backup-id>.complete.json`, and `drills/<backup-id>-<nonce>.json`. `remote.prefix` begins with `<environment>/` and is separate from core archives. Keep explicit `<backup-id>.hold.json` retention holds. See the [PostgreSQL procedure](postgres-backup-restore.md). |
| PKI recovery | Preserve the PKI owner's existing archive keys and contract; record them during inventory before any migration. |
| Future Logger backup | `logger-backups/<stack>/<YYYY>/<MM>/<DD>/<backup-id>/...`; reserved namespace, not an implemented backup job. |
| CI output | `ci/<repository>/<run-id>/<attempt>/<platform>/...`; repository is the basename (for example `rtk_cloud_client`), with explicit retry attempt and platform. |
| Temporary data | `tmp/<producer>/<run-id>/<attempt>/...`; the SDK producer is `rtk-cloud-client-sdk`. New temporary writers and readers adopt the same prefix together. |
| Routine report | `reports/<producer>/<run-id>/...`. |
| Storage canary | Preserve `<configured-prefix>/__rtk_cloud_validation__/<unique-id>` used by the current storage validator. |
| Held evidence | `holds/<evidence-id>/...`, with owner, reason and review date; excluded from automatic expiry. Existing held evidence elsewhere needs an equally verified exclusion. |

CI and temporary path identifiers use lowercase ASCII letters, digits, hyphens
or underscores, without dots or embedded slashes. Existing flat CI prefixes such
as `ci/rtk-cloud-client-ci-<run>-<attempt>-<platform>/` and old
`sdk/staging/<run>/<attempt>/` objects remain bounded migration/cleanup inputs;
new CI/SDK temporary writes use the registered layouts above.

Existing writers and manifests are the source for exact compatible suffixes.
Formal deployment and handoff consumers resolve an explicit version and its
manifest; they must not infer a release from a floating `latest` object.
Bucket migration preserves keys by default. Any key transformation needs an
explicit mapping, collision check, consumer update and rollback plan. Prefixes
that merely look obsolete remain unclassified until their owner and consumers
are known.

## Retention and Cost Control

| Class | Default policy |
| --- | --- |
| CI output under `ci/` | Expire after 30 days. |
| Disposable data under `tmp/` | Expire after 7 days. |
| Routine output under `reports/` | Expire after 30 days. |
| Registered storage canary prefix | Immediate cleanup by the validator, with a 1-day expiry fallback. |
| Formal releases, SDK handoffs, product/runtime data, OTA firmware, backups and PKI | No blanket expiry; owner-approved product, release or recovery retention applies. |
| Held evidence | No automatic expiry until the evidence owner releases the hold. |

These defaults require reviewed provider lifecycle configuration and readback;
they are not evidence that lifecycle is currently installed. Match the exact
registered prefix, including its trailing `/` and any configured environment
prefix. Do not apply these defaults with an empty prefix to an entire bucket.
The Object Storage defaults do not change GitHub artifact retention or local
test-report policies.

Daily PostgreSQL backups have an approved retention floor: keep the union of all
successful backups captured in the last 14 days, the newest 14 successful backups,
and the backup referenced by the last successful restore drill. A failed job or
drill cannot reduce that floor. Pruning requires valid, environment-bound completed
backup metadata and protects the last successful drill's reference. Do not apply a
14-day bucket lifecycle rule: it could delete the only drilled backup or violate
the successful-count floor after missed runs. An incomplete multipart upload is
not a successful backup; bounded cleanup must distinguish it from a committed
artifact. See the [retention procedure](postgres-backup-restore.md#retention).

Before enabling expiration, classify existing objects and isolate held evidence
outside every matching expiration rule, or verify a supported exclusion. A
manifest flag or object metadata alone does not override a provider lifecycle
rule. Retain original provenance when copying evidence to a held namespace.

Inventory noncurrent versions, delete markers and incomplete multipart uploads
as well as current objects. Versioned temporary data requires an explicit
noncurrent-version cleanup rule; deleting a current object does not reclaim all
versions. Every multipart writer needs a reviewed incomplete-upload abort period
that exceeds its supported upload/retry duration. Record these periods instead
of assuming the visible-object expiry also cleans them up. Never add automatic
old-version expiry to product, PKI or recovery data without its owner review.

Provider lifecycle is asynchronous. Confirm rule readback and later deletion
evidence; do not promise exact deletion at the cutoff. A copied object's creation
time can change its effective expiry, so record original age and intended expiry
in the migration plan. Do not let migration silently extend temporary retention.

Track reclaimable bytes, versions, unfinished uploads and unnecessary traffic.
Bucket count is an ownership signal, not a cost estimate. Cost assessment uses
the account's actual storage, outbound-transfer and applicable request pricing.

## Registration and Creation

Each bucket registration records its scope, purpose, owner, account, region,
endpoint, writers/readers, key prefixes, access and privacy requirements,
versioning/lifecycle configuration, hold rules, and state (`active`, `reserved`,
`migration-source`, or `retire-candidate`). Reserved and temporary resources
also need a reason and review/expiry date. A registration may be tracked intent
plus a dated inventory; never commit credentials.

Provision through the explicit reviewed storage workflow. Routine deployments,
tests and release jobs must not create unregistered buckets on missing-target
errors. Validate identity, exact name, endpoint, privacy and scoped access before
writing; validate a bounded canary and remove it. Capture non-secret receipts.
Limited access keys are scoped per bucket; a key label or prefix is not an access
boundary. Custom bucket policies must preserve existing limited-key rules and
be tested for both allowed and denied operations.

## Existing Bucket Migration and Retirement

1. Inventory the live account and exact endpoint. Enumerate current objects,
   versions, delete markers and multipart uploads; identify all consumers,
   issued URLs and referenced backup sets. Classify every bucket, including
   empty reserved resources. A name mismatch alone never authorizes deletion.
2. Produce an exact source-to-destination bucket/key mapping and collision
   report. Existing destination objects require content verification or an
   explicit resolution; never silently overwrite them. Decide whether version
   history must be retained and how stored version IDs will remain usable.
3. Record the destination's policy, ACL, CORS, encryption, versioning, lock and
   lifecycle requirements. Provision and verify the destination separately.
   Preserve service keys and supported SDK/release/recovery layouts.
4. Copy and verify contents with SHA-256 or another agreed content checksum;
   an ETag alone is not a general content checksum. Verify complete backup sets
   with a restore test. Record counts, bytes, exceptions and intended expiry.
5. Stop or fence all writers and deletion jobs for the final reconciliation,
   including presigned uploads, CI, lifecycle and background cleanup. Account
   for source deletions since the first copy. Repeat inventory and verification,
   then cut over consumers with their service-specific gates.
6. Retain the source and required read credentials through URL expiry and a
   recorded observation/rollback period. Verify consumers individually. If new
   data reaches the destination, rollback must reconcile those writes and
   deletions before switching back. Apply OTA drain and metrics gates from the
   operations guide; do not treat naming compliance as cutover evidence.
7. Prepare an explicit retirement plan listing account, endpoint, bucket,
   object/version/upload scope, counts and bytes, exclusions/holds, ownership,
   migration/restore evidence, remaining references, observation end, credential
   retirement and recovery limits. Re-inventory immediately before execution;
   any drift invalidates the old deletion list. Delete only the reviewed scope
   after its required approval. A key-retirement receipt does not authorize
   bucket deletion.

The current storage commands cover only their documented operations; this policy
does not imply that they implement account-wide reconciliation, generic cleanup,
version-history migration or bucket deletion. Preserve the dated inventory when
recording a newer observation rather than rewriting historical state.

## Provider References

- [Bucket constraints and name reuse](https://techdocs.akamai.com/cloud-computing/docs/create-and-manage-buckets)
- [Lifecycle actions and limitations](https://techdocs.akamai.com/cloud-computing/docs/lifecycle-policies)
- [Lifecycle prefix and tag filters](https://techdocs.akamai.com/cloud-computing/docs/use-tags-and-prefixes-in-lifecycle-policies-to-delete-objects)
- [Access keys](https://techdocs.akamai.com/cloud-computing/docs/manage-access-keys) and [bucket policies](https://techdocs.akamai.com/cloud-computing/docs/define-access-and-permissions-using-bucket-policies)
- [Object Storage pricing](https://techdocs.akamai.com/cloud-computing/docs/object-storage-pricing)
