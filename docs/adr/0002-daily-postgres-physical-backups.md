# ADR 0002: Daily PostgreSQL Physical Backups

Status: accepted

Date: 2026-10-03

Supersedes: none

Superseded by: none

## Context

PostgreSQL stores authoritative Account Manager, Billing and Video Cloud data.
The existing matched core recovery procedure stops application writers so that
PostgreSQL, OpenBao, SQLite and durable Redis can be captured together. Routine
database protection needs an online schedule with a measured performance budget.
The initial service can accept a daily recovery point; continuous recovery and a
standby are not initial requirements.

## Decision

Use the workspace Go runner with PostgreSQL 16's native `pg_basebackup` in plain
format and streamed WAL. Produce one independent full backup daily at 03:00
Asia/Shanghai. Stream only the WAL required by that capture, verify with
`pg_verifybackup`, encrypt with age and publish to private object storage only
after complete ciphertext readback verification. Do not introduce pgBackRest,
continuous WAL archival/PITR or a standby in this implementation.

Capture the entire instance from the primary with one runner, a configurable
transfer limit, spread checkpoint, separate scratch PVC and bounded resources.
Enforce a four-hour deadline. Qualification requires representative API p95 and
p99 each to increase no more than 5% during backup. Resource isolation does not
remove source database I/O, so the measured gate governs production enablement.

Retain all captures from the last 14 days, at least 14 successful backups, and
the last successfully drilled backup. A recent upload does not refresh capture
age. Monitoring reports capture freshness, latest execution and independent
restore evidence; unknown evidence must stay visible. Restore drills use isolated
resources and the original image/cluster identity. Source and application
endpoints remain outside the drill's mutation scope.

## Consequences

Each successful daily artifact can be restored without a chain of older backup
sets. Storage and read traffic grow with the complete instance size. A lost or
missed daily capture increases potential data loss, and arbitrary point-in-time
recovery is unavailable. A tighter recovery objective requires a later decision.

The PostgreSQL archive is separately scoped from the matched core archive. It
does not establish cross-system consistency or replace original OpenBao/PKI
escrow. Production rollback still needs coordinated external-state and service
reconciliation. Enabling schedules requires the dated staging performance and
restore evidence specified in [Daily PostgreSQL Backup and Restore](../postgres-backup-restore.md).
