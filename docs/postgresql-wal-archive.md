# PostgreSQL WAL segment archive

`wal-archive` is the first online PostgreSQL recovery adapter. It does not stop
writers or enter maintenance mode. Existing matched-set `backup create` remains a
separate maintenance operation; scheduling that command does not provide continuous
backup. This implementation does **not** yet provide physical base-backup capture,
automated replay, failover qualification or a PITR/RPO
claim. Do not enable it as a complete production archive_command until those
remaining paths and an actual restore drill are complete.

The supported input is a complete PostgreSQL 16 WAL segment from a little-endian,
8-byte-aligned installation using standard 8192-byte WAL pages. The first long
page header is checked against the configured system identifier, segment size,
filename timeline and starting address (or authenticated ancestor history for a
promotion segment). These checks follow the
[PostgreSQL 16 WAL header definitions](https://raw.githubusercontent.com/postgres/postgres/REL_16_STABLE/src/include/access/xlog_internal.h).
They identify the segment; they do not replace PostgreSQL record CRC validation or
prove replay coverage. Partial files, backup-history files, other layouts
and mismatched cluster data fail closed. Continuous recovery needs a base backup
and the required WAL sequence, as described in
[PostgreSQL continuous archiving](https://www.postgresql.org/docs/16/continuous-archiving.html).

## Reviewed configuration

Supply a version-one JSON object with these fields:

| Field | Required value |
| --- | --- |
| `environment`, `stack` | Explicit existing deployment scope |
| `system_identifier` | Canonical nonzero decimal identifier from the source `pg_control_system()` |
| `segment_bytes` | Source cluster's segment size, power of two from 1 MiB through 1 GiB |
| `directory` | Dedicated absolute private spool directory; mode 0700, no symlink ancestors, encrypted storage, outside Git/SecretStore |
| `recipients` | 1–32 independently provisioned age X25519 public recipient strings |
| `remote` | `endpoint`, `region`, `bucket`, `prefix`; private HTTPS origin, prefix starts with `environment/` |
| `timeout_seconds` | 1–900; bounds context-aware reads and remote requests |

Use dedicated `RTK_BACKUP_ACCESS_KEY_ID`, `RTK_BACKUP_SECRET_ACCESS_KEY` and optional
`RTK_BACKUP_SESSION_TOKEN`, as in core recovery. No credentials or decryption keys
belong in JSON or argv. Bucket privacy, retention and failure-domain independence
remain operator checks; a URL is not evidence of them.

```sh
rtk-cloud wal-archive --config /private/recovery/wal.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --name 000000010000000000000001 --source /postgres/pg_wal/000000010000000000000001
```

The name/source correspond to PostgreSQL's archive inputs. This example invokes
one segment explicitly; no server settings, scheduler, bucket or service are
installed by the command. It returns failure on any unsupported input, mismatch,
contention or incomplete publication, retaining source ownership with PostgreSQL.

## Durability and retry

The spool serializes each segment with an advisory lock. It hashes the source,
streams metadata and WAL bytes into age encryption, then checks that source bytes
did not change. Ciphertext and a receipt are fsynced and published together by an
atomic directory rename and parent fsync. No additional plaintext WAL copy is
created. Files are 0600 and spool subdirectories are 0700.

Retries require the same configuration, source digest and intact ciphertext.
They reuse the exact randomized ciphertext, allowing safe immutable upload retries.
Changed same-name segments, changed cluster/recipient configuration or corrupt
retry state fail instead of replacing an existing archive. Keep the durable spool
across retries and outages. An interrupted `.wal-*` temporary directory is not a
completed segment; cleanup of reviewed leftovers and retention remain manual.

Remote keys use a dedicated `prefix/wal-v1/system_identifier/stack/` namespace.
Success requires conditional create, a complete ciphertext readback matching size
and SHA-256, and a completion marker. Ambiguous uploads are reconciled by exact
readback; existing names are never overwritten. This namespace contains the WAL
age envelope, not a core-backup tar archive. Core restore cannot consume it.

## Restore a completed segment

`wal-restore` fetches the completion marker and ciphertext, verifies the remote
size/checksum, decrypts the envelope, and checks the original configuration digest,
cluster, name, plaintext hash/size and PostgreSQL header. It consumes the final age
authentication tag before publishing any destination. Supply the **exact original
archive configuration**, including spool path and recipients; changing configuration
fails closed. The original private spool path must be available on the recovery
host. Configuration migration is not supported by this envelope version.

```sh
rtk-cloud wal-restore --config /private/recovery/wal.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --name 000000010000000000000001 --destination /postgres/pg_wal/RECOVERYXLOG \
  --identity /private/recovery/age-identity.txt
```

The identity argument is a file path, never the private key itself. The identity
must be a regular file of at most 1 MiB with no group/other permissions. Use a
read-only object-store credential for recovery. The destination parent must already
exist and be controlled by the recovery operator. Relative destinations resolve
against the process working directory, matching PostgreSQL's `%p` convention.

Plaintext is streamed to a 0600 temporary file in that parent, fsynced, then
published by an atomic no-clobber hard link and parent-directory fsync. **Every
existing destination is rejected**, including identical files and symlinks; this
command never replaces a file. The destination filesystem must support hard links
and directory fsync. A sync failure returns failure even if publication occurred;
inspect the target before retrying. Temporary plaintext and downloaded ciphertext
are removed on normal success/failure; abrupt process termination can leave
`.wal-restore-*` files for reviewed cleanup. Use encrypted storage for both spool
and recovery destination. Context deadlines do not forcibly interrupt OS disk I/O.

This provides the full-segment fetch/decrypt primitive for a future
`restore_command`; it does not configure PostgreSQL or establish replay eligibility.
Unsupported partial/backup-history files return failure. Base-backup selection
and an actual PostgreSQL replay drill remain unqualified.

## Timeline history and promoted segments

Both commands now accept names such as `00000002.history`. These use immutable
`history-00000002.age` objects in the same cluster namespace, with the same
completion, encryption, checksum, retry and no-clobber guarantees as segments.
Their plaintext names remain the original PostgreSQL history filenames.

History validation follows the [PostgreSQL 16 history format](https://raw.githubusercontent.com/postgres/postgres/REL_16_STABLE/src/backend/access/transam/timeline.c):
ancestor timeline numbers are decimal, switch points are hexadecimal LSNs, and
comments/blank lines are supported. This adapter requires nonempty history, positive
and increasing ancestor IDs below the child ID, nonzero nondecreasing switch points,
no NUL bytes, lines shorter than 1024 bytes and a total size at most 64 KiB.
Timeline 1 has no history file. Unsupported or oversized history fails closed.
History has no intrinsic cluster identifier: operators must source it from the
configured cluster's `pg_wal`; the encryption envelope binds that asserted scope.

A promoted segment can retain an ancestor's first-page timeline. For this case,
archive reads the regular, bounded `<child-timeline>.history` beside the source,
verifies that the page address belongs to that ancestor's interval and the segment
contains the child's fork point, then includes those history bytes in its encrypted
envelope. Retry requires identical history bytes. Restore validates the embedded
history and needs no sibling file. The complete history file must also be archived
for PostgreSQL's own timeline selection. Own-timeline segments keep the original
envelope without an added history field; earlier segments remain readable. Metadata
is now bounded to 128 KiB to accommodate history. Older binaries reject the new
ancestor envelope; upgrade recovery tooling before using it.

These are header/ancestry checks, not record CRC validation or proof that an entire
WAL sequence is replayable. PostgreSQL remains responsible for replay validation.

## Local evidence and remaining work

Tests exercise age decrypt/byte equality, durable ciphertext reuse, same-name
content conflicts, layout/cluster mismatch, ambiguous immutable remote retries and
corrupt readback. A completed 16 MiB segment from a disposable PostgreSQL 16 instance
passed real-header/encryption validation. Recovery race tests, focused CLI argument
tests, vet and CLI build pass. The object-store tests use a local adapter fixture;
no remote production storage was accessed.

Restore tests additionally prove exact bytes, missing completion/corrupt remote
rejection, wrong configuration/key, truncated or tampered authentication, extra or
short plaintext, header/hash mismatch, cancellation and no-clobber behavior.

A disposable PostgreSQL 16 standby was promoted to timeline 2. Its actual history
and completed segment (first-page timeline 1) passed encrypted round trips with
byte-for-byte equality. Tests also reject unrelated ancestry/fork intervals, missing
or symlinked history, malformed histories and changed history on retry.

Remaining implementation: restore_command integration, backup-history handling,
physical base backups with coverage manifests,
continuous WAL/snapshot scheduling, retention, matched OpenBao/registry recovery,
and measured recovery drills demonstrating RPO <= 15 minutes and RTO <= 4 hours.
