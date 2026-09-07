# Targeted PostgreSQL WAL recovery

`base-backup restore --pitr-plan FILE` prepares a newly restored PostgreSQL 16
cluster for an explicit LSN and timeline. It uses the real `wal-restore` command
for authenticated archive fetches. It does not start a server, automatically
promote it, resume writers or prove that the target was reached.

## Reviewed plan

```json
{
  "version": 1,
  "target_lsn": "0/3000100",
  "target_timeline": 1,
  "executable": "/opt/rtk/bin/rtk-cloud",
  "wal_config_file": "/private/recovery/wal.json"
}
```

Use the actual reviewed target, not the example LSN. Timeline is a positive decimal
number, not `latest`. The LSN must be at or after the base backup's verified
consistency point. PostgreSQL validates the target timeline's ancestry using the
archived histories. The WAL JSON must exactly match the physical backup's nested
WAL configuration. The executable/config/identity paths must be absolute, clean,
and regular files; the tool must be executable and the identity private. Install
the binary for the recovery host's OS/architecture. Preparation checks file/config
binding; actual execution, archive access and reaching the target are separate
runtime checks.

```sh
rtk-cloud base-backup restore --config /private/recovery/base.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --id base-20260907-001 --destination /private/recovery/target-001 \
  --identity /private/recovery/age-identity.txt \
  --pitr-plan /private/recovery/target.json
```

The result is explicitly `pitr-prepared-not-replayed`. `pitr.json` also records
`prepared-not-replayed`; `verified.json` remains evidence of the base backup only.
Failures remove the newly created recovery destination. Existing directories are
refused. No source database or original archive is changed.

## Isolated configuration

After verifying the restored backup again, preparation moves copied
`postgresql.conf`, `postgresql.auto.conf` and any `postmaster.opts` into
`source-*` files outside PGDATA. It creates a self-contained configuration without
including those files. Resource settings needed for recovery are taken from the
validated native control data, and control-data warnings fail closed.

The generated server has TCP disabled, a private Unix socket with peer
authentication, archive mode disabled, no inherited preload libraries and no
inherited `ALTER SYSTEM` settings. Runtime limits still need sufficient recovery
host resources. Database extensions that require preload libraries may need a
separately reviewed recovery profile; this initial profile does not enable them.
The socket filesystem must support Unix sockets (Docker Desktop host bind mounts
may not; use a private runtime-local socket directory for that test environment).

`restore_command` invokes the configured CLI with explicit environment/stack,
WAL configuration and protected identity paths. Static shell arguments and
PostgreSQL percent/config escaping are handled separately; credentials are never
embedded. The server OS user must be able to read those inputs, write the private
WAL spool and access the archive using dedicated read-only object credentials.
Provide `RTK_BACKUP_ACCESS_KEY_ID`, `RTK_BACKUP_SECRET_ACCESS_KEY` and any session
credential securely in the server process environment. Do not put secret values
in the PostgreSQL configuration. Normal HTTPS server verification applies.

The target is inclusive and uses `recovery_target_action='pause'`, with hot standby
enabled. A missing archive object returns failure; if PostgreSQL exhausts recovery
before reaching the configured target, it fails instead of silently completing at
an earlier point. These semantics follow the
[PostgreSQL recovery settings](https://www.postgresql.org/docs/16/runtime-config-wal.html#RUNTIME-CONFIG-WAL-RECOVERY-TARGET).
The recovery signal is written last after the generated configuration is durable.
An interrupted preparation is not ready for use.

## Runtime verification

Start only the isolated restored cluster, with the correct OS ownership and secure
runtime environment. It must remain disconnected from application traffic. Connect
through its private socket and inspect:

```sql
SELECT pg_is_in_recovery(), pg_get_wal_replay_pause_state(), pg_last_wal_replay_lsn();
SHOW listen_addresses;
```

Require recovery state `true`, pause state `paused`, the expected replay position,
and no TCP listener. Verify required database contents and that later changes are
excluded. Check issuer mappings, provider state, CRLs, revocations and other PKI
invariants before any separate decision to promote or resume writers. Preparation
alone supplies none of that operational evidence. This command does not promote.

## Local integration evidence

The test cross-compiles the actual recovery CLI and a TLS object-store fixture for
the container architecture. For the current ARM64 test image, from `scripts/go`:

```sh
GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build \
  -o /private/tmp/rtk-pki-linux-cli ./rtk-cloud
GOWORK=off GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c \
  -o /private/tmp/rtk-pki-recovery-linux.test ./rtk-cloud/internal/recovery
RTK_PHYSICAL_BACKUP_INTEGRATION=1 RTK_PITR_INTEGRATION=1 \
  RTK_ARCHIVE_COMMAND_INTEGRATION=1 \
  RTK_PITR_CLI_BINARY=/private/tmp/rtk-pki-linux-cli \
  RTK_PITR_HELPER_BINARY=/private/tmp/rtk-pki-recovery-linux.test \
  GOWORK=off go test -race ./rtk-cloud/internal/recovery \
  -run '^TestPostgresPhysicalEncryptedRoundTrip$' -v
```

Use the matching architecture if the cached container image differs. Tests never
pull an image automatically. The disposable container is network-isolated and the
TLS object fixture runs inside it. With `RTK_ARCHIVE_COMMAND_INTEGRATION`, the
primary also uses the real archiver and verifies native backup-history publication. Native PostgreSQL calls the actual `wal-restore`
binary, downloads completed encrypted WAL, decrypts it and reaches the requested
LSN. Assertions cover the captured row, a post-backup/pre-target row, exclusion of
a later row, paused/read-only state, disabled TCP and confirmed remote ciphertext
fetch. A second fresh recovery with required ciphertext removed must fail before
the target and shut down. The original standalone physical-backup test also runs.
The fixture does not emulate provider IAM or prove production bucket policy.

This is local single-timeline targeted-replay evidence. Cross-timeline end-to-end
drills, time-based target selection, scheduled capture/retention, matched
OpenBao/registry recovery, production failure-domain testing and measured
RPO <= 15 minutes / RTO <= 4 hours remain. Production PKI stays disabled until all
required operational acceptance evidence is complete.
