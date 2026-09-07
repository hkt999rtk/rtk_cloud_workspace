# Encrypted PostgreSQL physical backups

`rtk-cloud base-backup create|restore` captures a PostgreSQL 16 cluster, verifies
its native manifest and required WAL, and uses the existing encrypted immutable
object-store protocol. It does not pause writers or install a scheduler. It is
separate from maintenance `backup create` and is not a matched OpenBao/registry
recovery set or a complete PITR/RPO implementation.

## Configuration and credentials

Supply a reviewed JSON object with:

| Field | Value |
| --- | --- |
| `wal` | The complete [WAL configuration](postgresql-wal-archive.md), including environment, stack, cluster system identifier, segment size, spool, recipients and remote destination |
| `binary_directory` | Absolute directory containing matching PostgreSQL 16 `pg_basebackup`, `pg_verifybackup`, `pg_controldata` and `pg_waldump` executables |
| `service_file` | Absolute path to a private regular libpq service file, at most 1 MiB, with no group/other permissions |
| `service` | Reviewed libpq service name, using lowercase letters, digits and hyphens |
| `timeout_seconds` | Overall operation deadline, 1–14400 seconds |
| `max_archive_bytes` | Encrypted object limit, 4 MiB–4 GiB; capture tar is limited to this value minus 1 MiB for encryption/metadata overhead |

The service file owns connection settings and may refer to protected pass/certificate
files. The command forces `sslmode=verify-full` for TCP; local Unix sockets use
local authentication. Tool processes receive a minimal environment containing the
explicit service-file path, C locale and tool/system PATH, excluding ambient `PG*`
variables. Tool stdout/stderr is never included in an error. Do not put private keys
or passwords in JSON/argv. Use encrypted storage for the spool and restore target.

A capture account needs PostgreSQL replication access, and the server must retain
all WAL needed until capture finishes. The adapter uses native tar output to stdout
with `--wal-method=fetch`, SHA-256 manifest checksums and a fast checkpoint.
Additional tablespaces cannot be captured through this stdout mode. Native backup
failure or recycled required WAL produces failure, never a completed object.
The behavior follows [pg_basebackup](https://www.postgresql.org/docs/16/app-pgbasebackup.html).

## Capture and retry

```sh
rtk-cloud base-backup create --config /private/recovery/base.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --id base-20260907-001
```

Use a new ID for each new recovery point. Capture writes a bounded plaintext tar
into a private temporary directory, extracts it safely for native verification,
and encrypts the tar with a `postgres-physical` manifest. Source tar and verification
trees are removed before ciphertext plus receipt are fsynced and atomically staged.
Remote keys live under `prefix/base-v1/system_identifier/stack/`; success requires
ciphertext readback and a completion marker. This namespace is distinct from WAL
and maintenance core backups. Core restore rejects the physical scope.

The native verifier checks manifest integrity, file checksums and required WAL
records using `pg_waldump`, without disabling WAL parsing. The adapter also checks
`PG_VERSION`, control-file cluster identity, WAL segment size, block size/alignment,
and manifest WAL ranges. It stores the native manifest digest and required ranges
in the encrypted envelope. See [pg_verifybackup](https://www.postgresql.org/docs/16/app-pgverifybackup.html).

Once staged, retry reuses the exact ciphertext, including after an ambiguous remote
write. It does not recapture newer database data under the same ID. A changed
configuration or corrupt receipt/ciphertext fails. Capture uses a per-ID advisory
lock; another process receives a retry error. Different IDs can capture concurrently;
operators must serialize scheduled runs to avoid unnecessary database load.

## Restore and verification

```sh
rtk-cloud base-backup restore --config /private/recovery/base.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --id base-20260907-001 --destination /private/recovery/restored-001 \
  --identity /private/recovery/age-identity.txt
```

Restore requires the exact original configuration; original tool/spool paths must
be available. The parent of `--destination` must exist and be canonical (no symlink
aliases). Every existing destination is refused. After completed-object download,
full age authentication and manifest/hash checks, restore exclusively creates the
requested private directory. It extracts into `destination/pgdata`, verifies it
again with native tools and compares the verification evidence to the encrypted
manifest. Files are 0600 and directories 0700. A successful result includes
`destination/verified.json`; the command fsyncs extracted files and directories.
It never starts PostgreSQL or changes source/database deployment settings.

**Do not use or start the destination until the command succeeds.** It is a staging
directory during restore and is removed on normal failure. Existing destination
contents are never overwritten. Links, devices, traversal, duplicate/conflicting
members, runtime PID/recovery signals, nonempty tablespace maps and external
tablespaces are rejected. Extraction is bounded to 100,000 tar members and the
configured byte limit; native manifests are bounded to 64 MiB. An empty PostgreSQL
`tablespace_map` is valid. Source configuration files remain in the backup: review
network addresses, absolute paths, archive commands and ownership before starting
an isolated recovery server. The command does not sanitize or execute that config.

Abrupt termination can leave private `.base-*`, `.base-download-*`, `.base-restore-*`
files or an incomplete destination. Review and clean these explicitly; never treat
an interrupted directory as verified. Disk capacity must cover plaintext tar,
extracted data and ciphertext during capture (roughly three copies), and downloaded
ciphertext, unpacked tar and extracted data during restore. Size limits prevent
unbounded capture but do not reserve disk space. Context deadlines cancel native
tools and context-aware reads; OS disk I/O is not forcibly interrupted.

## Evidence and remaining work

Run the disposable test using a cached `postgres:16-alpine` image:

```sh
cd scripts/go
RTK_PHYSICAL_BACKUP_INTEGRATION=1 GOWORK=off go test -race \
  ./rtk-cloud/internal/recovery -run '^TestPostgresPhysicalEncryptedRoundTrip$' -v
```

The test uses a network-isolated container, captures real data and a role, exercises
immutable object-store upload/download through an in-memory adapter, decrypts and
verifies the backup, then starts an independent PostgreSQL instance from it. It
checks the captured row/role, exclusion of post-capture changes, completed consistency
recovery, immutable capture retry, missing-WAL rejection and cluster/layout mismatch.
Containers and copied fixtures are removed by test cleanup. No production storage
or database is contacted.

This proves standalone backup consistency, not replay of later archived WAL to a
chosen time. Remaining work includes restore_command/PITR integration, backup-history
files, scheduling/retention, matched OpenBao/registry recovery points and measured
RPO <= 15 minutes / RTO <= 4 hours. Larger clusters, tablespaces and other PostgreSQL
versions/layouts require an expanded adapter and qualification. Production remains
disabled until the complete PKI acceptance evidence exists.
