# Daily PostgreSQL Backup and Restore

Status: implementation procedure; staging qualification pending. Production use
requires dated restore and performance evidence for the selected environment.

Classification: source for PostgreSQL-only backup operations.

Owner: `rtk_cloud_workspace` operations; service owners validate restored data.

Last reviewed: 2026-10-03.

## Scope and decision

Take one full physical backup of the platform PostgreSQL 16 instance every day
at **03:00 Asia/Shanghai** by default. A reviewed `schedule` and `time_zone` may
select another daily time. The Go runner calls native `pg_basebackup` with plain
format and streamed WAL (`-Fp -Xstream`), verifies the native manifest with
`pg_verifybackup`, then encrypts and uploads one independent backup. All databases,
roles and PostgreSQL data in that instance belong to the same capture. The source
continues serving application reads and writes.

Streamed WAL covers the backup window and makes that full backup recoverable.
This workflow does not retain a continuous WAL archive or support restoring to an
arbitrary later time. Each successful daily backup is a separate recovery point;
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
it in `RTK_POSTGRES_RUNNER_SOURCE_IMAGE`. The worker rejects a mismatch with the
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

Update the environment's matched-core inventory before its next maintenance
backup: explicitly exclude `rtk-postgres-backup-scratch` with the reason that it
contains reproducible backup scratch/retry artifacts, suspend the backup CronJob,
and drain active backup Jobs. Do not classify a running backup Job as a business
writer or assume the shared lock replaces the core procedure's inventory checks.

One run captures and locally verifies the full data directory, compresses it with
gzip level 1, encrypts it, uploads
it, verifies the remote ciphertext by reading it back, and publishes a completion
record last. A partially uploaded object has no successful recovery-point claim.
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
namespace private and verify PVC/provider-volume cleanup as part of the evidence.

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
  A running attempt beyond four hours is FAIL; a skipped attempt is WARN.
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

**Current qualification:** staging performance, remote backup readback, isolated
restore and production enablement are pending. Local tests do not establish
those deployment outcomes. See [testing requirements](testing.md#postgresql-daily-backup-qualification).
