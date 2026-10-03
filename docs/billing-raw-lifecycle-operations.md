# Billing Raw Lifecycle Operations

Status: implemented control interfaces; disabled by default; environment qualification pending.

Classification: source.

Owner: `rtk_cloud_workspace` (integration), environment financial and recovery owners (activation).

Last reviewed: 2026-10-03.

## Boundary

Follow the [lifecycle design](design/billing-raw-data-lifecycle.md),
[key custody design](design/billing-backup-key-custody.md), canonical
[billing contract](../repos/rtk_cloud_contracts_doc/billing_usage.md),
[Logger lifecycle specification](../repos/rtk_cloud_logger/docs/spec.md),
[Video reconciliation guide](../repos/rtk_video_cloud/docs/billing-raw-lifecycle.md)
and [Billing authority guide](../repos/rtk_billing/docs/raw-retention-authority.md).
Object naming and cloud retention remain owned by [Object Storage Policy](object-storage-policy.md).

This workflow does not provision a bucket, generate custody keys, install a
scheduler, enable a production environment, delete cloud objects or delete
PostgreSQL evidence/facts/invoices. Twelve-hour capture, 24-hour verified
protection age and four-hour recovery are targets requiring dated qualification,
not guarantees or permission to bypass an integrity check.

## Roles and Preparation

Use distinct principals for uploader, isolated verifier/controller, financial
approval, recovery approval and final cloud disposition. Logger receives age
public recipients and approved Ed25519 public registries only. Billing receives
public verifier material only. The verifier and independent escrow stay outside
the workload cluster and ordinary CI. The recovery admission signing identity is
also independent of the ordinary archive verifier key.

The unattended controller is given only its Logger/controller/storage credentials,
not financial or recovery approval bearer tokens. Run approval commands from the
appropriate separate owner principal/host; do not mount an entire deployment
SecretStore into the controller. OS file permissions alone do not isolate two
processes running as the same user.

Runtime credentials are explicit-provisioned optional catalog entries:

| Credential | Restricted use |
| --- | --- |
| `cloud-logger-lifecycle-token` | Operator/controller lifecycle mutations, distinct from ingestion/handoff. |
| `cloud-logger-lifecycle-read-token` | Billing GET of exact terminal retirement receipt only. |
| `video-billing-raw-lifecycle-token` | Billing read-only consumer reconciliation. |
| `billing-raw-retention-financial` | Approved policy activation, clearance and hold management. |
| `billing-raw-retention-recovery` | Independent recovery-owner policy approval. |
| `billing-raw-retention-controller` | Persistent operation creation, abort request and resolve. |
| `billing-raw-retention-authority-read` | Logger GET of the exact Billing operation; no mutation authority. |
| `billing-backup-access-key-id` / `billing-backup-secret-access-key` | Dedicated bucket-scoped uploader, never media/core fallback. |

The isolated controller's own Object Storage credential is separately installed
in its private operator environment under `RTK_BILLING_VERIFIER_ACCESS_KEY_ID`
and `RTK_BILLING_VERIFIER_SECRET_ACCESS_KEY`. Qualify read payload/list access and
immutable completion publication only; final delete permission is not needed.
No secret value belongs in tracked intent, argv, logs or documentation.

Before enabling any environment, register the exact private bucket, actual
Object Storage region, S3 signing region, endpoint, StoreID, stack, consumer IDs,
keys/escrow, approved capacity and owners. Never copy naming examples as approval.
Review source labels: existing renderers may have used `staging` in Dev/Prod.
Lifecycle scope and source-event label are distinct. Explicitly approve
`source_event_environments` if preserving such legacy records; default is the
actual lifecycle environment only. Do not rewrite event bytes/digests or reset
`video-cloud-mqttusage/<legacy-service-env>` to change lifecycle scope.

## Activation Stages

Deployment intent exposes independent disabled switches:

- `LKE_BILLING_BACKUP_ENABLED=false`.
- `LKE_BILLING_RAW_RETENTION_AUTHORITY_ENABLED=false`.
- `LKE_BILLING_RAW_RETIREMENT_ENABLED=false`.
- `LKE_BILLING_INBOX_COMPACTION_ENABLED=false`.

The shared defaults, including `LKE_BILLING_BACKUP_INTERVAL=12h`, are declared
in `cloud_deploy/adapters/lke/defaults.env`. Environment-specific intent belongs
in `cloud_env/<environment>/overrides/adapter.env`; `deployment.env` contains
only the deployment selection keys (`DEPLOYMENT_ARCHITECTURE`,
`DEPLOYMENT_ADAPTER`, and `DNS_ADAPTER`).

Each enabled stage requires explicit reviewed configuration; missing dedicated
credentials, keys, scope or bucket fails closed. Retirement additionally requires
backup and authority. Compaction does not grant retirement authority.

Lifecycle traffic uses separate private listeners: Logger `8081`, Billing
authority `8081`, and Video reconciliation `19401`. Public application listeners
never mount these routes. Enabled deployment rendering uses dedicated ClusterIP
services and exact namespace/pod/port NetworkPolicy grants; it does not expose
them through public ingress. Register any off-cluster authenticated access path
separately; a private service name is not an off-cluster route.

1. Migrate Billing's authority schema (`070`, `071` and metadata-only `072`) with the migration owner. Keep protected
   environment startup migrations disabled. Deploy compatible Logger and Video
   binaries with new lifecycle features still disabled.
2. Enable guarded lifecycle/migration and backup in Dev. Logger's online v2
   migration resumes in batches. Existing payloads/identities remain unchanged;
   old receipts start their 90-day age at migration completion. No retirement
   or compaction is allowed until the full invariant audit completes. Old v1
   binaries must not be used to reopen/roll back a migrated store.
3. Qualify isolated verification, escrow recovery, corrupt/missing archives,
   immutable retry and old-cursor rehydration while retaining all hot payloads.
4. Enable the Billing authority, approve immutable policy and explicit period
   clearance, register every consumer, then qualify the automatic controller.
   Source completeness, late arrivals and commercial reconciliation must be
   attested; `closed`, high-water or an empty outbox never suffices.
5. Separately qualify retirement and online compaction. Repeat Dev qualification
   in Staging, then obtain Prod financial/recovery/operational owner approval and
   dated evidence for each feature. Initial Prod cleanup remains disabled.

## Isolated Controller Configuration and Commands

Keep reviewed non-secret controller configuration on the isolated host. Example
shape below is a template, not an environment assignment:

```json
{
  "version": 1,
  "environment": "staging",
  "stack": "<reviewed-stack>",
  "store_id": "<persisted-logger-store-id>",
  "directory": "/absolute/private/controller-journal",
  "logger_url": "https://<private-authenticated-logger-endpoint>",
  "billing_url": "https://<private-authenticated-billing-endpoint>",
  "remote": {
    "endpoint": "https://<registered-object-storage-endpoint>",
    "region": "<actual-object-storage-region-id>",
    "signing_region": "<reviewed-s3-signing-region>",
    "bucket": "rtk-cloud-staging-billing-backup-<actual-region-id>"
  },
  "verifier_key_id": "verifier-v1",
  "verifier_keys": { "verifier-v1": "<base64-32-byte-public-key>" },
  "encryption_recipients": { "encryption-v1": ["<age-public-recipient>"] },
  "source_event_environments": ["staging"],
  "auto_retirement": false,
  "auto_compaction": false,
  "hot_retention_days": 90,
  "policy_id": "<approved-policy-id>",
  "policy_version": 1,
  "clearance_id": "<approved-clearance-id>"
}
```

The controller directory must already exist, be private (`0700`), owned by the
controller user and on qualified encrypted storage. A local single-controller
lock prevents concurrent scheduled loops. Private keys must be real `0600`
files below the resolved environment SecretStore's
`operator/recovery/billing-backup/`, with private owner-controlled ancestors.
`--identity` is repeatable for retained encryption keys. Native signing files
contain the base64 Ed25519 private key; escrow unwrapping is a separate approved
procedure. CLI help never emits key contents.

From a checked-out, dependency-pinned workspace:

```sh
go run ./scripts/go/rtk-cloud -- billing-lifecycle status \
  --environment staging --config /absolute/reviewed/controller.json

go run ./scripts/go/rtk-cloud -- billing-lifecycle capture \
  --environment staging --config /absolute/reviewed/controller.json \
  --confirm-environment staging --confirm-store <persisted-store-id>
```

`migrate` advances one batch; the configured worker also resumes migration.
`verify` downloads/decrypts/audits one explicit snapshot prefix and publishes
the independently signed immutable completion, then registers it with Logger:

```sh
go run ./scripts/go/rtk-cloud -- billing-lifecycle verify \
  --environment staging --config /absolute/reviewed/controller.json \
  --confirm-environment staging --confirm-store <persisted-store-id> \
  --identity /absolute/secret-root/operator/recovery/billing-backup/encryption-v1.agekey \
  --signing-key /absolute/secret-root/operator/recovery/billing-backup/verifier-v1.ed25519key \
  --prefix billing-inbox-snapshots/<stack>/<store-id>/<YYYY>/<MM>/<DD>/<set-id>
```

`rehydrate` uses the same key/prefix arguments plus `--from` / `--through`,
bounded to 1,000 consecutive receipts. A signing private key is not required for
this read/recovery action. It verifies the signed archive and
original bindings before inserting controlled cache records. A pending cache
miss is retryable 503; do not advance/reset the Pg collector cursor.

`authority --role financial|recovery|controller --path /v1/internal/billing/raw-retention/... --request /absolute/private/request.json`
uses the dedicated role credential and strict bounded JSON. The service guide
specifies policy, approval, clearance, revoke and hold request bodies. Keep
financial/recovery approval duties distinct from unattended controller operation.
Use `--method GET` without `--request` for read-only authority queries, including
the scoped unresolved-operation listing. Financial/recovery commands do not
require Logger mutation credentials.

`operation-status --operation-id ...` obtains Logger's exact retirement receipt
using its dedicated read-only token. `cache-evict --through ...` evicts only the
controlled rehydration cache, with explicit environment/StoreID confirmation;
it does not retire additional hot records. Cache capacity is bounded and an
operator must evict qualified cached ranges before a refill that exceeds it.

After stage approval, `run` with key arguments installs no scheduler: it runs in
the foreground on the isolated host. The operator explicitly installs/supervises
it using the host's approved service manager. It captures every 12 hours, verifies
published sets, registers signed evidence and, only when configured, attempts
one qualified contiguous retirement prefix and scheduled compaction. It never
skips a failed prefix. `controller-status.json` is atomically persisted for the
host's monitoring agent.

Discovery lists published prefixes, but historical sets with a private durable
`.registered.json` acknowledgement do not repeatedly read remote metadata or
POST verification on each minute tick or restart. The acknowledgement is written
only after Logger confirms registration and retains the exact manifest bytes and
independently signed receipt; scope, approved keys and signature are revalidated
locally. A `.verified.json` receipt alone is not a registration acknowledgement.
Missing acknowledgement or an ambiguous response retries the same receipt;
corrupt/private-permission or scope failures are reported, not silently cached.
Retirement and recovery still independently read and verify remote archive bytes
and live authority; this journal is not a new cleanup permit or an integrity audit.

## Ambiguous Results and Holds

The controller saves an immutable `.plan.json` before sending its operation.
After a timeout/crash it queries Billing using that same ID, never creates a
replacement ID merely because the response was lost. An authenticated 404
allows the same intent to be submitted with a fresh proof; existing decisions
remain immutable. `resume --operation-id ...` finishes known decisions.
`abort --operation-id ...` requests Billing abort, commands Logger's permanent
abort, then asks Billing to independently obtain the exact terminal receipt.
For a saved request that was never accepted, `abort` submits its complete stable
intent to Billing. Under its store lock Billing permanently cancels that ID
before acceptance; a delayed create cannot revive it. This cancellation is not
a Logger terminal receipt and releases no previously ACTIVE fence.
Neither command releases a fence on timeout. Never delete an unresolved journal
or authority row to unlock a store. Alert and recover authoritative history.
Journal replay validates operation identity, scope, stable digest and terminal
status; corrupt terminal files are errors, not evidence that work completed.

To stop new retirement admissions during maintenance, deactivate the approved
financial policy and stop new controller selection. Keep the authority/private
listener and its credentials available until all ACTIVE/ABORT_REQUESTED fences
are resolved. Turning off the authority listener preserves durable fences; it
does not resolve them, release them, or authorize deleting their state.

A hold before ACTIVE blocks removal. A later hold remains durably pending,
protects archive/key dependencies immediately and is finalized after the exact
ordered terminal outcome. Local rehydration is available when protected retired
records must be read. Releasing a hold is a separate explicit financial action.

## Coordinated Recovery

1. Fence ingestion, collectors, delivery and cleanup with the core recovery
   procedure. Retain operation journals and independent verification receipts.
2. Retrieve original identities from independent escrow if needed. Select
   matched Pg recovery points and the exact Logger snapshot plus all catalog
   archive/key dependencies. Validate decrypted snapshot/parts before installing
   them; never ordinary-copy a live bbolt file or use the core tar reader for it.
   `restore-stage` with key/prefix arguments and `--destination` naming an existing
   private encrypted staging root creates a new operation-owned directory. It
   audits the signed source, snapshot and exact raw/time bindings, then produces
   a recovery-fenced `inbox.db`, bound active manifest and `recovery-stage.json`.
   It requires no Logger access or signing key, does not overwrite live files,
   and does not itself approve all remote dependencies or Pg history. Retain
   its source receipt and staged hash for the matched installation procedure.
3. Start the restored Logger with
   `RTK_CLOUD_LOGGER_BILLING_INBOX_RECOVERY_MODE=true` before traffic. The durable
   fence cannot be cleared merely by removing that environment flag. Use
   `recovery-status` to inspect bound StoreID, high-water, floor, operation-history
   and archive-dependency digests. All normal writing/collection remains fenced.
4. Independently reconcile Logger receipt allocation/ID bindings, every archive
   dependency, Billing's ACTIVE/terminal/hold history, Video receipts/events/
   deterministic facts/outbox/exact ack and existing Pg cursor. No unknown
   allocation frontier, missing range or discontinuous decision history is
   acceptable. A four-hour drill deadline is not permission to discard data.
5. Financial and recovery owners approve independently retained evidence. The
   separate recovery custodian produces a maximum-15-minute signed admission
   bound to the exact restored state, frontier and Pg checkpoint digest, using
   the registered recovery key (never the archive-verifier key).
6. `recovery-admit --request /absolute/private/signed-approval.json` forwards that
   approval with explicit environment/StoreID confirmations. Logger checks
   signature, expiry and exact local bindings in its transaction. Only then
   reopen approved traffic and replay through original identities. Never reset
   a Pg cursor, reuse an old generation after switch or force-clear an ACTIVE.

The signature attests owner reconciliation; it does not reconstruct unknown
receipts or certify an old snapshot automatically. Keep the writer fenced if
the latest allocation frontier cannot be established. No zero-loss promise is
made for acknowledged data after the latest verified recovery horizon.

## Monitoring, Capacity and Crash Cleanup

Collect Logger lifecycle status, controller status and Billing unresolved
operation/hold state. Alert on verified-horizon age above 24 hours, earliest
unprotected receipt age, uploader/verifier errors, hot/scratch/free capacity,
consumer lag/unqualified ack, retirement block reasons, unresolved fences,
pending holds and compaction lag. An ambiguous result is immediately actionable;
never auto-unlock it. The status reports are not themselves a deployed alert rule.

After a crash, inventory the operation's recorded private `verify-...` or
`range-...` directory before removing plaintext scratch. Preserve `.verified.json`,
`.plan.json`, `.terminal.json`, original identities and any unresolved operation.
Do not recursively clean a SecretStore, controller root or unrelated generation.
Files on SSDs/snapshotting disks are not securely erased by ordinary unlink.
Capacity shortfalls stop/defer work and retain hot data; they never authorize
unprotected deletion. Representative-volume journal/switch tests must demonstrate
the one-second admission-queue target and coordinated four-hour recovery target.

Cloud final disposition remains a separate manual workflow: inventory retention,
holds, active readers and snapshot/key dependencies; obtain financial and recovery
owner approvals; retain the immutable disposition record. A local-retirement
permit never authorizes cloud/key deletion.
