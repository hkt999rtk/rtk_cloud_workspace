# Billing Raw-data Lifecycle and Backup Design

Status: implementation in progress; features remain disabled until local and per-environment qualification. No production activation is implied.

Classification: supporting-note.

Owner: `rtk_cloud_workspace` (cross-service design); `rtk_cloud_logger` (raw inbox and export); `rtk_video_cloud` (collector evidence); `rtk_billing` (commercial reconciliation).

Last reviewed: 2026-10-03.

Applies to: environment-owned `billing_usage` raw evidence in the retained Logger
inbox, its Linode Object Storage backups and archives, and coordinated consumer
recovery. Operational Loki logs and payment-provider payloads are outside scope.

## Decision and Authority

Keep at least a 90-day hot copy of raw billing receipts in the Logger inbox and
retain verified off-volume copies in a dedicated private billing backup bucket.
Start backups while data is still hot; do not wait 90 days for the first copy.
Age alone never authorizes local deletion. Cloud retention is separately approved
by the financial data and recovery owners, including dispute and legal holds.

[Object Storage Policy](../object-storage-policy.md#billing-raw-data-backup-boundary)
is the sole authority for the `billing-backup` bucket name, namespaces, access
boundary and retention rules. This design does not provision a bucket, select an
environment's region or enable a lifecycle rule. The implementation switches are
independently disabled until qualification; this document is not an activation.

The [billing backup key custody design](billing-backup-key-custody.md) specifies
the proposed off-cluster operator SecretStore location, independent encrypted
escrow, public-only writer and separate verification/recovery access. It is
based on today's age/file interface, not an already deployed vault or automatic
key-management service.

Here "raw billing data" means the accepted periodic `billing_usage` snapshots
and their receipt evidence, not a full log of individual MQTT messages or EMQX
callbacks. Producer-side observation queues have their own recovery requirements;
this archive cannot recreate observations that never reached the retained inbox.

The canonical [billing usage contract](../../repos/rtk_cloud_contracts_doc/billing_usage.md)
owns measured-usage envelopes and producer identity; the
[pricing and invoicing contract](../../repos/rtk_cloud_contracts_doc/pricing_and_invoicing.md)
owns immutable commercial facts and period close. The canonical contract now
defines archive-aware cursor and retirement behavior. The versioned codec in
Logger owns the exact archive wire representation; the existing core backup
reader must not be used for Billing packages.

## Current State and Evidence

| Component | Current behavior and evidence |
| --- | --- |
| Logger raw inbox | [BillingInbox](../../repos/rtk_cloud_logger/billing_inbox.go) commits the event, financial-content SHA-256, monotonic receipt sequence and event-ID index in one synchronous bbolt transaction before acknowledgment. `meta`, `events` and `ids` retain store identity, data and deduplication together. |
| Logger collection | `BillingInbox.Page` freezes a high-water horizon and requires contiguous receipt sequences. It rejects missing records, foreign/ahead cursors and corrupt digests. Consumption does not delete raw receipts. See the [service specification](../../repos/rtk_cloud_logger/docs/spec.md#durable-billing-inbox-and-collection). |
| Video Cloud evidence | [Usage evidence schema](../../repos/rtk_video_cloud/internal/postgres/usage_evidence_schema.go) retains raw payload receipts, Logger receipt bindings, collector state and the durable Billing fact outbox. [Collector](../../repos/rtk_video_cloud/internal/postgres/usage_collector.go) commits validated receipts, facts and cursor progress transactionally. |
| Billing delivery | The [MQTT billing runbook](../../repos/rtk_video_cloud/docs/mqtt-billing-runbook.md#durable-billing-delivery) requires exact Billing creation/duplicate acknowledgment before marking outbox delivery. Uncertain delivery stays pending; an empty outbox does not prove producer completeness or settlement. |
| Qualification | [2026-09-01 local inbox evidence](../../repos/rtk_cloud_logger/docs/billing_inbox_qualification.md) covers retained identity, restart, paging and precision locally, not archive/volume restore, HA, capacity or live staging qualification. |

The pre-lifecycle implementation had no eviction, archive writer or receipt-age
clock. The lifecycle implementation adds a resumable v2 schema and trusted receipt
times without modifying original events or collection responses. It remains
disabled pending qualification. `BillingRecord.Event.Time` is still a producer
timestamp, never an ingestion-age clock. Direct database deletion, unrelated new
store identities, TTL deletion and automatic reinitialization remain prohibited.

Implementation entry points:

- Logger: [migration, bindings and retirement](../../repos/rtk_cloud_logger/billing_lifecycle.go),
  [capture and cache](../../repos/rtk_cloud_logger/billing_backup.go),
  [generation compaction](../../repos/rtk_cloud_logger/billing_compaction.go) and
  [archive codec](../../repos/rtk_cloud_logger/billingarchive/archive.go).
- Video Cloud: [consumer reconciliation](../../repos/rtk_video_cloud/docs/billing-raw-lifecycle.md).
- Billing: [durable retention authority](../../repos/rtk_billing/internal/rawretention/types.go)
  and its PostgreSQL migration; financial clearance is distinct from invoice status.
- Workspace: [isolated verifier/controller](../../scripts/go/rtk-cloud/internal/billingbackup/controller.go),
  [CLI](../../scripts/go/rtk-cloud/billing_lifecycle.go) and
  [activation/recovery runbook](../billing-raw-lifecycle-operations.md).

## Target Lifecycle

| Phase | Required transition |
| --- | --- |
| Durable hot receipt | Commit original event, digest, event ID, receipt sequence and a trusted UTC receipt time together; acknowledge only after the local transaction succeeds. |
| Off-volume protected | Export a fixed committed horizon or take a consistent recovery snapshot, encrypt, upload and independently verify the complete set. Retain the local copy. |
| Retirement eligible | At least 90 days have passed since trusted receipt time, and every verification, consumer, reconciliation and hold gate below passes. Otherwise remain hot. |
| Archive-backed | Retire only qualified local payload ranges while preserving store identity, sequence continuity, replay protection and a working archive read/recovery path. |
| Final disposal | Delete a cloud set only under the owner-approved long-term policy after checking holds, replay and snapshot dependencies; retain an auditable disposition receipt. |

Ninety days means 90 elapsed 24-hour days, not three calendar-month boundaries.
Add trusted receipt time to persisted Logger state in the same acceptance
transaction. Existing records without that timestamp remain ineligible unless
an approved conservative migration assigns an age no earlier than migration
time. Never infer age from the producer's `event_time` or Object Storage upload
time; late-arriving old usage must get a full hot-retention window.

### Backup Cadence and Ownership

The initial capture schedule is every 12 hours, with at least one verified
off-volume recovery snapshot and incremental raw export every 24 hours,
starting when billing ingestion is enabled. The recovery owner must approve the achievable RPO, RTO, snapshot cost
and supported inbox size before rollout; a shorter RPO requires more frequent
protection. Cadence alone is not an RPO guarantee: measure the age of the last
verified complete horizon and alert on failures or lag. Local commit protects
process restart, not loss of the entire volume or cluster. Loss of the volume
can lose acknowledged receipts after the last verified protection point unless
qualified producer retention/replay or another durable copy can recover them.
Daily backup is not a zero-loss guarantee; inventory that replay capability and
accept the residual risk explicitly before rollout.

Logger owns consistent capture and its identity/index metadata. Video Cloud owns
consumer checkpoints, raw receipts and outbox reconciliation. Billing/financial
operations own commercial acknowledgment, closed-period/dispute evidence and
long-term retention approval. The environment recovery owner owns keys, restore
drills and matched recovery-point selection. None of these owners may advance
another component's checkpoint merely to permit cleanup.

Object Storage unavailability retries with backoff and leaves hot data intact;
it does not put remote storage in the per-event acknowledgment path. Monitor
inbox capacity, oldest unprotected receipt, archive failures, consumer lag and
hold-blocked bytes. Capacity pressure must alert and use qualified fail-closed
admission handling, never discard unprotected billing evidence.

## Backup and Archive Publication

Use the raw-archive and inbox-snapshot namespaces registered in
[Object Storage Policy](../object-storage-policy.md#object-namespaces-and-compatibility).
Raw exports are immutable contiguous receipt ranges, bounded for streaming and
restore. Monthly grouping by trusted receipt time is an organization option,
not a new store identity or a permission to truncate the live database.

Two distinct artifacts are needed: portable raw exports for audit/replay and
consistent full inbox snapshots for recovery of bbolt `meta`, `events`, `ids`,
sequence allocation and future archive/retirement metadata. Copying a live
bbolt file with an ordinary file-copy command is not a consistent snapshot;
use a qualified database snapshot mechanism or fence its writer during capture.

Version 1 package contents (see the codec for exact fields):

- Raw export: independently encrypted/compressed NDJSON parts, retaining each
  original stored envelope, event ID, exact JSON quantities, receipt sequence,
  financial-content digest and trusted receipt time. Do not reassign IDs,
  re-rate usage or reinterpret a Logger receipt sequence as a producer sequence.
- Snapshot: independently encrypted/compressed bbolt snapshot parts containing recovery
  metadata. Include the complete identity/deduplication and archive index state,
  not only event payloads. A snapshot after retirement references all archive
  sets needed to restore its readable stream.
- `manifest.json`: format version, environment,
  stack, store identity, immutable archive/backup ID, committed high-water,
  inclusive receipt range, record count and maximum trusted receipt time,
  object names, byte lengths and SHA-256 checksums. Sequence values preserve
  exact integers. Record both encrypted-object checksums and decrypted content
  verification results, plus the approved key ID and public-recipient fingerprint
  from the custody registry. These are Billing package fields, not
  additions accepted by the current core archive reader. Keep tenant payloads,
  tokens and sensitive recovery checkpoints out of plaintext metadata.
- `complete.json`: published last, binding the manifest checksum and the exact
  verified object set. A marker without matching objects/checksums is invalid.
  Sensitive consumer checkpoint/reconciliation metadata stays encrypted and
  identifies a compatible recovery point, not a claimed cross-system transaction.

Each part is at most 256 MiB plaintext; the complete set is at most 64 GiB
plaintext. The initial writer uses an independent private scratch PVC and stops
on insufficient capacity. Snapshot capture is bbolt `Tx.WriteTo`; export and
encryption use the resulting private snapshot. No remote upload holds a live
database transaction. A sealed set reuses the same manifest and ciphertext bytes
on retry. An interrupted, unsealed capture is abandoned, not certified.

`complete.json` is a domain-separated Ed25519 signature envelope, not an uploader
marker. The isolated verifier checks authenticated decrypted parts, the entire
snapshot, contiguous original records, exact financial/consumer bindings, receipt
times and snapshot invariants. Logger trusts only registered public verifier keys.
Approval of an archive does not itself authorize a retirement operation.

Publication protocol:

1. Capture a fixed committed horizon and allocate/persist one immutable set ID.
   Retry with that same ID and content, retaining the generated encrypted bytes
   or verified uploaded objects rather than re-encrypting into conflicting bytes.
   Fence concurrent publishers for the set; mismatched existing content is a
   conflict, never an overwrite.
2. Stream, compress and encrypt with approved public recovery recipients. Follow
   the custody design for independently escrowed identities; the writer receives
   no private identity. Confirm authorized verification/recovery access: readable
   ciphertext without available keys is not a usable backup.
3. Upload payloads and manifest without a completion marker. An isolated,
   separately authorized verifier reads back the exact objects and verifies
   SHA-256, lengths, record counts, contiguous sequence range,
   per-record financial bindings and precision after decryption. ETag alone is
   not content verification. Perform a qualified restore/replay validation.
4. Publish `complete.json` only after verification; read it back and durably
   record the completed set and verification receipt in the local archive catalog.
   Recover a crash between publication and catalog commit by re-verifying the
   existing immutable set; never treat mere upload success as completion.

Incomplete or conflicting sets are not backups and cannot unlock retirement.
Versioning or provider object-lock capability must be separately qualified if
selected; this design does not claim that private ACLs or a naming convention
provide WORM protection. Audit archive access and disposition separately from
the operational log stream.

## Local Retirement Gates

Retirement is a separate implementation phase. Initially run backup/export only
and retain the entire inbox. Before enabling payload retirement, require all of:

1. Every receipt in the proposed contiguous range meets trusted 90-day age and
   belongs to a verified completed archive with a successful qualified restore.
2. Every registered raw consumer has durably committed through the range. For
   Video Cloud, reconcile Logger receipt bindings, complete event receipts and
   derived Billing facts; all associated outbox facts have exact Billing
   acknowledgment. A cursor, count or empty outbox alone is insufficient.
3. Financial operations explicitly clear source completeness, late data and
   commercial reconciliation for the affected periods and receipt range, and
   there is no unresolved dispute, replay/recovery need or legal hold. Check
   holds again under a coordinated deletion fence immediately before mutation.
4. Canonical contracts and coordinated consumers support archive-aware reads
   for old cursors and explicit failure for unavailable/corrupt archived ranges.
   Never silently skip a gap, advance a cursor or claim an empty complete page.
5. Preserve the logical store ID, global receipt allocation/high-water and the
   event-ID-to-original-sequence/digest binding for the supported replay period.
   The v2 ID binding is self-contained and includes original-byte identity;
   retaining a legacy sequence-only `ids` bucket is insufficient. Identical retries remain duplicates and
   changed financial content remains a conflict across archive boundaries.
6. Persist archive references and the local retention floor crash-safely with
   the removal plan. Qualify compaction/replacement, restart and rollback while
   preserving private-file permissions and single-writer ownership. Logical
   deletion alone is not proof that bbolt file size or disk use shrank.

Prefer retiring whole verified contiguous ranges or qualified segments over
unbounded row-by-row deletion. Do not rotate into unrelated randomly balanced
inboxes, reset sequences, discard event identity indexes or run existing
initialization flags as a retention mechanism. The 90-day target may be exceeded
indefinitely when safety gates fail. This design does not delete or expire
Video Cloud PostgreSQL evidence, Billing facts, invoices or producer queues.

### Authority, Holds and Atomic Terminal Decisions

Billing stores immutable policy versions, independent recovery approval,
financial clearance, holds, operation evidence and append-only audit history in
PostgreSQL. It directly fetches authenticated reconciliation from every required
registered consumer. Missing/unknown consumers, legacy unqualified ack markers,
pending facts or incomplete financial evidence block the entire prefix.

In the final Billing transaction, recheck evidence and holds and establish at
most one durable `ACTIVE` fence per StoreID. Logger rechecks exact authority over
a read-only credential. Its transaction atomically removes payloads, advances
the retirement floor and records an immutable completed receipt. Abort instead
records a permanent aborted receipt, including for delayed prepares. Apply and
abort cannot both win. Billing releases its fence only after directly reading
and verifying Logger's exact terminal receipt; no timeout unlock exists.

A hold ordered before `ACTIVE` blocks cleanup. A later hold is durably queued,
immediately protects cloud sets and keys, and becomes active when the ordered
operation resolves. It cannot retroactively cancel already linearized removal.
Rehydrate protected raw data when local access is needed. Cloud expiry and key
destruction are not performed by this controller.

Old cursors still address their original receipt sequence. An unavailable cache
returns retryable HTTP 503 without a page or advanced cursor. Independently
verified rehydration restores exact original records into a controlled cache;
it does not alter global sequence allocation or replay bindings.

### Online Physical Compaction

Compaction creates a shadow generation from a consistent snapshot, then replays
the complete same-transaction mutation journal, including identity/metadata,
operations, catalog, floor, cache and bucket sequence changes. Validate the shadow
before the admission gate. Under that short gate, finish catch-up, fsync the new
generation and atomically publish/fsync the active manifest. Queued requests
target at most one second; an HTTP process or Pod stop is not part of the protocol.

Readers retain old-generation transaction leases until drained. After a durable
manifest switch the old generation is never a fallback, including after a crash.
Missing/corrupt active state fails closed. Catch-up/capacity shortfalls defer
compaction, and old files are reclaimed only after readers have drained. Local
tests are not representative-volume timing qualification.

## Recovery Boundary and Qualification

Raw archives are evidence, not a replacement for the
[matched core backup procedure](../backup-restore.md). Core PostgreSQL backups
cover selected Video Cloud/Billing data, but do not by themselves establish
Logger inbox or archive coverage. Explicitly inventory the inbox volume and
external archive dependencies; never infer coverage from the word "logs".

Fence ingestion, collectors, outbox delivery and cleanup before coordinated
recovery. Choose a compatible Logger snapshot/archives and PostgreSQL recovery
point; restore original store identity, receipt sequence, identity index and
archive catalog. Validate that the consumer's persisted cursor/high-water is
reachable in the recovered stream and that receipt/fact/outbox digests match.
Replay through original IDs and existing deduplication, including facts already
accepted by Billing. A consumer ahead of the restored Logger fails closed;
recover verified missing ranges or choose a compatible recovery set, never
manually rewind/advance immutable collector state or initialize a new inbox.
Also recover the known committed receipt-allocation frontier before enabling new
writes; an old snapshot must not reuse sequence numbers already acknowledged
outside that snapshot. Unrecoverable ranges or an unknown frontier block reopening
until an explicitly qualified recovery protocol resolves the identity and
continuity boundary. An approved RPO alone does not authorize silent gap skipping
or sequence reuse. Record any lost horizon against that RPO and reconcile
producer-held snapshots and downstream evidence before reopening traffic.

Qualification must cover restart/crash during every publication/retirement
phase, upload ambiguity, corrupt/missing objects, key loss, foreign/ahead/old
cursors, late events, exact large quantities, conflicting replay, held data,
pending outbox facts, snapshot/archive dependency retention, backup-writer access
denials and restores at representative volume. The existing
[inbox tests](../../repos/rtk_cloud_logger/billing_inbox_test.go) are a baseline,
not proof of these new behaviors. The implementation tests add migration,
archive corruption, immutable retries, legacy cursor/cache, terminal ordering,
consumer ack and generation-switch cases; dated environment qualification still
must demonstrate representative throughput and restore timing.

Restored Logger copies must start with the persistent recovery fence before
serving traffic. A separate recovery custodian signs a short-lived admission
approval binding StoreID, exact restored allocation frontier, retirement floor,
operation-history digest, archive-dependency digest and independently reconciled
PostgreSQL checkpoints, with financial and recovery approval references. Logger
checks the local state binding and a distinct registered recovery public key;
the ordinary archive verifier cannot unlock recovery. Unknown frontier or
discontinuous decision history means remain fenced, never reset a Pg cursor.
Signing certifies the independent owner reconciliation; it does not discover or
magically reconstruct unknown acknowledged receipts from an old snapshot.

Implementation order:

1. Approve per-environment bucket registration, region, long-term financial
   retention, RPO/RTO and hold owners under Object Storage Policy, and key
   location/custodians/escrow under the key custody design.
2. Specify canonical archive/cursor compatibility and implement trusted receipt
   time, consistent snapshots, bounded exports, verification and monitoring.
3. Qualify recovery with Video Cloud receipts/outbox and Billing reconciliation;
   retain all local receipts during this phase.
4. Only then enable and separately qualify archive-aware replay,
   deduplication, retention-floor handling and gated local compaction/retirement.

Code implements guarded APIs, configuration and the operator-installed controller;
this document does not create a bucket, key, live credential or scheduled job.
Production activation requires dated evidence and explicit owner approval for
backup, retirement and compaction separately. Production retirement starts disabled.
