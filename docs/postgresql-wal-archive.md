# PostgreSQL WAL segment archive

`wal-archive` is the first online PostgreSQL recovery adapter. It does not stop
writers or enter maintenance mode. Existing matched-set `backup create` remains a
separate maintenance operation; scheduling that command does not provide continuous
backup. This implementation does **not** yet provide physical base-backup capture,
restore_command, timeline-history archival, failover qualification or a PITR/RPO
claim. Do not enable it as a complete production archive_command until those
remaining paths and an actual restore drill are complete.

The supported input is a complete PostgreSQL 16 WAL segment from a little-endian,
8-byte-aligned installation using standard 8192-byte WAL pages. The first long
page header is checked against the configured system identifier, segment size,
filename timeline and starting address. These checks follow the
[PostgreSQL 16 WAL header definitions](https://raw.githubusercontent.com/postgres/postgres/REL_16_STABLE/src/include/access/xlog_internal.h).
They identify the segment; they do not replace PostgreSQL record CRC validation or
prove replay coverage. Partial files, history/backup-history files, other layouts
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

## Local evidence and remaining work

Tests exercise age decrypt/byte equality, durable ciphertext reuse, same-name
content conflicts, layout/cluster mismatch, ambiguous immutable remote retries and
corrupt readback. A completed 16 MiB segment from a disposable PostgreSQL 16 instance
passed real-header/encryption validation. Recovery race tests, focused CLI argument
tests, vet and CLI build pass. The object-store tests use a local adapter fixture;
no remote production storage was accessed.

Remaining implementation: history files/timeline transitions, authenticated fetch
and decryption into restore_command, physical base backups with coverage manifests,
continuous WAL/snapshot scheduling, retention, matched OpenBao/registry recovery,
and measured recovery drills demonstrating RPO <= 15 minutes and RTO <= 4 hours.
