# Daily PostgreSQL Backup and Restore

Status: implementation procedure; staging qualification pending. Production use
requires dated restore and performance evidence for the selected environment.

Classification: source for PostgreSQL-only backup operations.

Owner: `rtk_cloud_workspace` operations; service owners validate restored data.

Last reviewed: 2026-10-06.

## Scope and decision

Take one full physical backup of the platform PostgreSQL 16 instance every day
at **03:00 Asia/Shanghai** by default. A reviewed `schedule` and `time_zone` may
select another daily time. The Go runner calls native `pg_basebackup` with plain
format and streamed WAL (`-Fp -Xstream`), verifies the native manifest with
`pg_verifybackup`, then encrypts and uploads one independent backup. All databases,
roles and PostgreSQL data in that instance belong to the same capture. The source
continues serving application reads and writes.

Streamed WAL covers the backup window and makes that full backup recoverable.
The daily workflow alone does not enable a continuous WAL archive. The new
authenticated `archive-wal`/`restore-wal` helpers and isolated `pitr-drill` use an
explicitly configured continuous archive; installing the daily CronJob does not
install or qualify it. Each successful daily backup remains a separate recovery point;
a missed job increases possible data loss. The initial recovery objective is the
last completed daily capture, and actual recovery time must be measured in a drill.

The first implementation uses the primary and native PostgreSQL tools. Use one
backup at a time, a configurable transfer rate, a spread checkpoint, bounded CPU
and memory, and a dedicated scratch PVC. PostgreSQL still performs reads and
checkpoint/WAL work, so zero performance cost is not a supported claim. Under
comparable representative workload, **both API p95 and p99 must increase by no
more than 5% during backup** before the profile is qualified. If tuning cannot
meet that gate, review the rate, schedule and storage resources before enabling
daily production runs. A standby is a future option requiring its own design and
measured evidence.

This PostgreSQL backup has `scope=postgres-physical`. It does not include OpenBao,
SQLite, durable Shadow Redis, media or firmware objects, audit history, or escrow.
Use [Core Backup and Restore](backup-restore.md) for matched cross-system recovery.
The PostgreSQL command must not bypass that procedure's maintenance fence when
performing a coordinated production rollback. See [ADR 0002](adr/0002-daily-postgres-physical-backups.md).

## Environment configuration

Use the reviewed `cloud_env/<environment>/postgres-backup.json` deployment
configuration. The CLI's `plan` action reads configuration only. The worker
configuration is defined in
[`postgresbackup.Config`](../scripts/go/rtk-cloud/internal/postgresbackup/types.go),
and deployment fields in
[`postgres_backup.go`](../scripts/go/rtk-cloud/postgres_backup.go).

Start from the [deployment configuration example](examples/postgres-backup.example.json).
After the scoped environment preflight, copy it to
`cloud_env/<environment>/postgres-backup.json` and replace every `REPLACE_WITH_...`
value with reviewed evidence. The example intentionally fails validation until
the source/runner digests, cluster/system identity, age public recipient and
private destination are supplied. Its staging names are examples; update both
environment/stack fields, namespace, source host and remote prefix together for
another environment. No credentials or private age identity belong in this file.

The example budgets 20 GiB of plaintext and 30 GiB of encrypted archive on a
60 GiB scratch PVC, leaving space beyond the required 64 MiB reserve. It fits
only when measured source data, growth and retry needs fit those limits and the
source PVC is no larger than 20 GiB; increase the scratch size and limits together
when necessary. Set `storage_class` explicitly for disposable scratch and drill
PVCs. The example uses `linode-block-storage`; verify its `Delete` reclaim policy
before configuring the backup. Staging's default observed on 2026-10-03 is
`linode-block-storage-retain`, so omission selects retained storage. A `Retain`
class requires separately reviewed PV/provider-volume cleanup after PVC removal.

Record and review:

- Logical environment, stack and stable `cluster_id`; exact PostgreSQL system
  identifier; source and runner images pinned to digests. `image_pull_secret`
  defaults to `ghcr-pull`; the isolated drill receives only the reviewed registry
  Secret needed to pull its runner image.
- The selected platform service, `postgresql-0`, `data-postgresql-0`, and dedicated
  replication role `rtk_postgres_backup`. Do not reuse application or superuser
  credentials for the recurring runner.
- A private scratch PVC separate from the database PVC, sized for plaintext,
  encrypted output and retry headroom. Deployment requires at least 60 GiB and
  three times the source PVC size; actual limits must fit measured data size and
  available capacity. Free space must cover `max_plaintext_bytes` plus
  `max_archive_bytes` and a 64 MiB reserve before capture starts.
- A private environment backup bucket, HTTPS endpoint, region, owned prefix,
  age public recipients and bounded plaintext/archive sizes.
- `timeout_seconds: 14400`, `retention_days: 14` and
  `minimum_backups: 14`. Keep the CronJob suspended until qualification passes.
- `schedule` defaults to `0 3 * * *`; only one numeric minute and hour with
  `* * *` day/month/weekday fields are accepted. `time_zone` defaults to
  `Asia/Shanghai` and must be an IANA time zone.
- The unified catalog additionally requires explicit `schedule_enabled` intent.
  It does not copy live CronJob settings or silently reset them. `true` still
  requires actual schedule/restore/performance qualification before execution.
- The backup transfer rate and selected namespace/resource permissions. The
  rate caps the base-backup transfer, so it is not a limit on every source I/O or
  WAL byte. Monitor the database and API while choosing it.

Custom tablespaces, cross-major upgrades, another environment's data and a changed
system identifier are outside this workflow. An image or topology change requires
reviewing configuration and repeating the relevant restore checks.

Build the runner with the [PostgreSQL Backup Runner workflow](../.github/workflows/postgres-backup-image.yml).
Supply the source Pod's actual platform image digest as `postgres_image`; a
multi-architecture index digest is not the running container's platform digest.
The Dockerfile uses that exact PostgreSQL image as its runtime base and records
it in `RTK_POSTGRES_RUNNER_SOURCE_IMAGE`. This runner adapter requires the
PostgreSQL 16 Alpine image family. The worker rejects a mismatch with the
reviewed source-image digest. Configure/manual-run preflight compares the actual
ready source container's `imageID` digest, and the worker repeats this check before
each scheduled capture. Record the resulting runner digest and
workflow image evidence in the selected environment's qualification record.

## Credentials and encryption

Store dedicated credentials under the selected environment
[SecretStore](secret-store.md):

- `operator/env/RTK_POSTGRES_BACKUP_ACCESS_KEY_ID` and
  `operator/env/RTK_POSTGRES_BACKUP_SECRET_ACCESS_KEY` for the backup writer and
  reviewed retention operations.
- `operator/env/RTK_POSTGRES_RESTORE_ACCESS_KEY_ID` and
  `operator/env/RTK_POSTGRES_RESTORE_SECRET_ACCESS_KEY` for independent read access
  during restore. A restore identity does not need object deletion permission.
- The source role's password in `runtime/postgres-backup` under that environment;
  the worker receives only its mounted `/run/postgres-backup/source-password`.

Keep age private identities in independent protected escrow. The recurring worker
needs only public recipients. Obtain the identity explicitly for an isolated
restore drill; never embed it in configuration, command text, logs or the archive.
Backups are authenticated encrypted archives, but creator identity and access to
completion records still rely on trusted operator storage and credentials.

Use the [Object Storage Policy](object-storage-policy.md) backup boundary. A bucket
name, an access key or a configured schedule does not establish that backups are
private, independently recoverable or qualified.

## Operator commands

Run from the workspace root. These staging names are examples; environment and
stack confirmation must match the reviewed configuration exactly.

```sh
go run ./scripts/go/rtk-cloud -- postgres-backup plan \
  --environment staging --config cloud_env/staging/postgres-backup.json

go run ./scripts/go/rtk-cloud -- postgres-backup configure \
  --environment staging --config cloud_env/staging/postgres-backup.json \
  --confirm-environment staging --confirm-stack video-cloud-staging

go run ./scripts/go/rtk-cloud -- postgres-backup run \
  --environment staging --config cloud_env/staging/postgres-backup.json \
  --confirm-environment staging --confirm-stack video-cloud-staging

go run ./scripts/go/rtk-cloud -- postgres-backup status \
  --environment staging --config cloud_env/staging/postgres-backup.json
```

`configure` creates the reviewed resources with a suspended schedule by default.
After successful qualification, repeat it with `--enable-schedule --qualification
/private/evidence/postgres-backup-qualification.json`. The CronJob uses the
reviewed daily time, forbids overlapping Jobs and enforces a four-hour
deadline. The shared recovery lock also excludes matched core maintenance and
competing backup/restore operations. A crash must not silently clear an active
maintenance fence; inspect the old operation before recovering its lock.
Schedule enablement requires a published backup, a successful drill for the
configured PostgreSQL system, and reviewed performance evidence. The qualification
JSON binds environment, stack, cluster/system identity, source/runner image
digests, backup rate and the successfully drilled backup. Each baseline and
backup window must contain at least five minutes of requests, errors, p95 and
p99 measurements. Request rates must agree within 5%, both latency percentiles
must stay within 5% of baseline, and error rate must not increase. A nonempty
workload ID and reviewer, with a valid measurement timestamp, identify the
retained supporting evidence. The validator checks these values; the reviewer
remains responsible for their measurement and representative workload.

Use the [qualification schema example](examples/postgres-backup-qualification.example.json)
to prepare the file. Its placeholders and zero measurements intentionally fail
validation; replace them with actual evidence, never estimated pass values.

After the manual backup, isolated drill and performance gates pass, enable the
staging schedule and observe **two consecutive successful daily scheduled Jobs**.
Retain their capture, remote readback/completion and monitoring evidence. Two
immediate manual runs do not satisfy this observation. Staging qualification and
production runbook readiness require those scheduled results; production
activation remains a separate reviewed operation.

Update the environment's matched-core inventory before its next maintenance
backup: explicitly exclude `rtk-postgres-backup-scratch` with the reason that it
contains reproducible backup scratch/retry artifacts, suspend the backup CronJob,
and drain active backup Jobs. Do not classify a running backup Job as a business
writer or assume the shared lock replaces the core procedure's inventory checks.

One run captures and locally verifies the full data directory, compresses it with
gzip level 1, encrypts and uploads it, verifies the remote ciphertext by reading
it back, and publishes a completion record last. A partially uploaded object has
no successful recovery-point claim.
Retry a retained encrypted capture with `postgres-backup retry-upload --id ID`
and the same identity/confirmation arguments. A retry preserves the original
capture timestamp. Review plaintext and encrypted scratch cleanup after a killed
runner; retain the known encrypted capture needed by an upload retry.

## Retention

Keep the union of:

1. All successful backups captured within the last 14 days.
2. The newest 14 successful backups, even when failures spread them over more
   than 14 calendar days.
3. The backup used by the last successful restore drill, until a later successful
   drill replaces that protection.

An unsuccessful run, absent or malformed metadata, a failed drill, or uncertain
remote inventory must not authorize removal of a known good backup. Retention
considers verified completion records and the configured environment/cluster;
it must not apply blanket bucket expiry. Preview with:

```sh
go run ./scripts/go/rtk-cloud -- postgres-backup prune --dry-run \
  --environment staging --config cloud_env/staging/postgres-backup.json
```

The successful daily run applies this retention policy; the manual `prune`
command also requires explicit target confirmation. No automatic deletion is
allowed until a successful restore drill references a retained backup. Explicit
hold records also protect their backup. Review the candidate list and remote
permissions before enabling scheduling. Keep interrupted multipart uploads and
incomplete objects outside the successful-backup count; their cleanup policy must
not delete committed backup objects or protected drill evidence.

Automatic retention currently requires a never-versioned bucket
(`GetBucketVersioning.Status` is empty) and writer permission to read bucket
versioning. Enabled or suspended versioning requires a separately reviewed
object-version cleanup policy; the current command refuses deletion in either
case. Verify this prerequisite before enabling scheduling. The versioning check
runs only when deletion candidates exist, so a successful dry-run or an early
backup with no candidates does not establish compatibility.

## Isolated restore drill

Use `postgres-restore drill` with `--id`, a unique `--drill-id`, the independently
retrieved `--identity`, and the same environment/config/confirmation arguments.
The drill downloads and verifies a completed backup, decrypts it into a fresh
isolated same-environment target, verifies its native PostgreSQL manifest, and
starts the exact matching PostgreSQL image for recovery checks. It must not mount
the source database PVC or change application database endpoints.

Record native verification, PostgreSQL startup/recovery, all application database
presence, expected roles, representative service-owned data checks and measured
elapsed time. Native checksum verification alone does not prove service-level
correctness. Retain the redacted result and the referenced backup. Confirm that
independent credentials and escrow work when the original worker is unavailable.

Use `postgres-restore cleanup --drill-id ID` with the reviewed configuration and
confirmation arguments to remove only that drill's resources after evidence is
saved. Production replacement and application cutover require a separately
reviewed coordinated recovery; the drill does not perform them.
The isolated PVC contains decrypted database files until cleanup. Keep its
namespace private and verify PVC, PV and provider-volume removal as part of the
cleanup evidence. Apply the same checks when retiring the scratch PVC after its
retained retry artifacts are no longer needed. With a `Retain` class, PVC removal
alone does not establish that the underlying database files were removed.

### Continuous WAL and time-point drill

The worker provides `archive-wal` with an exact `--wal-pgdata`, `--wal-name`
and `--wal-source`. It verifies PostgreSQL 16's actual source system identifier
and native WAL path before encrypting. Each segment/history file is bound to
the environment, stack, cluster, original PostgreSQL system ID and filename.
The immutable encrypted object is read back before a completion record is
published. Failed upload/readback never acknowledges archive completion.
Retries may reuse an identical committed segment but cannot replace it with
different bytes. Continuous archive does not acquire the base-backup lock.

Production configuration must install the matched runner in the source Pod,
enable PostgreSQL `archive_mode`, bound `archive_timeout`, reference the private
archive config/credentials and retain sufficient durable WAL/spool space.
The common compiler now includes a maintained archive installer, source
`archive_mode=on`/`archive_timeout=60s`, durable encrypted spool on the original
PostgreSQL claim, backup config/Secret, access policy, scratch PVC, base-backup CronJob and WAL
monitor CronJob.
The source and installer are pinned to the selected PostgreSQL/runner release;
runtime credentials come from the exact hashed private snapshot. Initialization
of the dedicated database role in the common execution stages and full
public-entry qualification remain in progress. Do not claim live WAL protection from rendering
or installing only part of this catalog.
The worker's `check-wal --config <mounted-config>` performs a fixed read-only
native query with the dedicated role. It checks the original physical system ID,
primary state, `archive_mode=on`, an `archive_timeout` at most 60 seconds, source
clock/observation freshness, a native segment archived within 15 minutes, and no
newer unresolved archiver failure. It then reads the exact environment/system
remote completion and verifies the complete encrypted object's checksum. An old
base-backup/upload status does not pass this check. The backup role remains `NOINHERIT` with granted `pg_monitor` membership; the
probe explicitly switches only its session to that role. It uses a temporary
0600 credential file and removes it.

Idle environments also need an explicit maintained archive heartbeat. The
read-only probe never switches WAL or writes a heartbeat to create its own
freshness evidence. Missing/stale remote completion, inaccessible state or
corrupt ciphertext fails. Native isolated PostgreSQL tests cover the real query,
remote read-back and PITR; freshness alone does not establish measured RPO,
security-state reconciliation or production qualification. The shared `rtk-postgres-backup-wal-monitor` CronJob runs every minute with
`Forbid` concurrency and a 180-second deadline. It explicitly calls
`heartbeat-wal`, waits 65 seconds for archiving, then runs the independent
read-only `check-wal`. Both schedules remain suspended until qualification.
Monitoring uses bounded temporary scratch, no source/backup PVC and no Kubernetes
API token. The heartbeat uses PostgreSQL's native
[`pg_logical_emit_message`](https://www.postgresql.org/docs/16/functions-admin.html),
without a business table or schema migration. It emits only for the configured
original primary system ID with archiving enabled and bounded timeout. A failed
heartbeat or remote verification makes the Job fail; preflight cannot emit a
heartbeat to manufacture freshness. Actual role/configuration installation and
live freshness must still be verified before GO.

WAL/history retention requires a reviewed policy preserving every protected
base backup's recovery chain; the daily full-backup prune does not delete WAL.

For an environment whose continuous archive is already configured and verified:

```sh
go run ./scripts/go/rtk-cloud -- postgres-restore pitr-drill \
  --environment staging --config cloud_env/staging/postgres-backup.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --id "$BACKUP_ID" --drill-id "$DRILL_ID" --identity "$RESTORE_IDENTITY" \
  --recovery-time "$EXACT_RFC3339_TARGET" --recovery-timeline "$TIMELINE" \
  --max-wal-bytes "$REVIEWED_WAL_CACHE_LIMIT"
```

Use an explicit timestamp with UTC offset, a reviewed timeline and measured WAL
cache headroom in addition to downloaded ciphertext and restored data. The
target must follow the authenticated base backup. Missing archive, corrupt or
foreign WAL, invalid ancestry or unavailable target time fails the drill.
There is no implicit `latest` timeline selection and no promotion on success.

The fresh isolated Job uses read-only storage credentials, no source PVC or
source database credential, no service-account token, and no inbound route.
Native verification replaces inherited runtime configuration with Unix-socket
only recovery, no source replication/archive hooks or preload libraries, and
read-only startup. It observes PostgreSQL's recovery pause, last replay timestamp
and LSN, executes maintained data checks and stops the target even after a check
failure. Configuration changes after preparation prevent startup. Decryption
or staging alone never records restore success.

The result records the exact target, native replay time/LSN and measured restore
time. Controller timing additionally includes Job scheduling/download/validation;
the four-hour objective must hold for that complete interval. This is PostgreSQL
data evidence, not complete production recovery approval. Production RPO requires
an independently observed fault-time/last recovered commit witness; the age of an
operator-entered target is not an RPO measurement. Independently reconcile
revocation, suspension, Root distrust and audit history before traffic or
issuance can reopen, as required by the
[readiness standard](deployment-readiness-standard.md#bootstrap-and-qualification).

Local native fixture evidence on 2026-10-06 verifies actual encrypted WAL replay:
a transaction before the target is restored, a later one is excluded, the target
remains in read-only recovery and is stopped. The fixture uses a disposable
network-isolated PostgreSQL container and an in-memory encrypted-object store;
it is not live provider, HA, custody or production RPO qualification.

## Monitoring and acceptance

[Cloud Monitor](cloud-monitor.md#postgresql-backup-evidence) reads
`rtk-postgres-backup-status` in the selected platform namespace. Its `status.json`
contains redacted identities, the latest committed backup, recent attempt and
independent restore-drill evidence. The monitor never starts a job or writes a
test record.

- Freshness uses `latest.manifest.finished_at`, the capture completion timestamp,
  rather than upload or status publication time: **WARN after 26 hours, FAIL
  after 28 hours**. Capture completion is recorded when `pg_basebackup` returns,
  before verification and upload; it is not an arbitrary transaction timestamp
  or a continuous-recovery watermark.
- Enabled backup with no completed backup is FAIL. Missing/unreadable/malformed
  or mismatched status evidence is UNKNOWN, requiring investigation.
- A failed attempt is reported independently of the last successful capture.
  A running attempt beyond four hours is FAIL. Recorded skipped attempts are
  WARN; a worker that cannot acquire the shared recovery lock leaves shared
  status unchanged, so inspect that Job and its log for the skipped attempt.
- Restore-drill status is independent: absent evidence is UNKNOWN, a failed drill
  is FAIL, and success applies to the named tested backup. A new successful upload
  does not turn an untested restore into PASS.

Before enabling production scheduling, retain dated evidence of a representative
staging baseline and backup window with the same workload, resources, query mix
and sampling method. Both API p95 and p99 must stay within 5% of baseline; also
record errors, throughput, database latency, CPU, disk latency/IOPS, WAL growth,
backup size/duration and scratch headroom. Exercise timeout/cancellation, upload
failure/retry, corrupt/incomplete artifacts, lock exclusion, retention protection,
and a clean isolated restore. Record every missing prerequisite as pending.

### Validation evidence and pending staging qualification

Local implementation checks recorded on 2026-10-03:

| Check | Observed result | Evidence boundary |
| --- | --- | --- |
| [Native PostgreSQL 16 round trip](../scripts/go/rtk-cloud/internal/postgresbackup/integration_test.go) | PASS, 14.19 seconds. A Docker fixture with networking disabled restored two databases and role grants, left the source intact, and rejected a damaged native manifest and missing WAL. | Local fixture only; the elapsed test time is not staging RTO or an API performance measurement. |
| [Large multipart transport](../scripts/go/rtk-cloud/internal/postgresbackup/remote_large_test.go) | PASS, 16.59 seconds. The actual S3 SDK and local HTTPS fixture transferred a 6 GiB + 17 byte file in 97 parts and verified readback/completion. | Synthetic local transport evidence; it does not qualify Linode compatibility, remote throughput, encryption of a production dataset or a live restore. |
| Cloud Monitor | Unit and race suites passed, including capture-age thresholds, independent drill outcomes and malformed evidence. | Read-only fixture results; no claim of current live backup coverage. |

The native test is opt-in with `RTK_POSTGRES_BACKUP_INTEGRATION=1` and uses an
already-installed image; it does not pull images or contact a deployment. The
large transfer test uses `RTK_POSTGRES_BACKUP_LARGE_INTEGRATION=1` and needs at
least 7 GiB of temporary space and loopback HTTPS access. A sandbox socket denial
is an execution restriction, not proof that Docker or credentials are missing.

**Staging qualification remains pending:** representative workload baseline and
backup windows meeting the p95/p99 <=5% gate, remote provider readback, independent
credential/escrow recovery, isolated staging restore, schedule enablement and two
consecutive successful daily scheduled Jobs.
No local result above satisfies those gates. Record the deployment images,
topology, source size, measured results and reviewer in the qualification evidence
before enabling the schedule. See [testing requirements](testing.md#postgresql-daily-backup-qualification).
