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
runtime environment. It must remain disconnected from application traffic.
Observe it through its private Unix socket:

```sh
rtk-cloud base-backup observe --config /private/recovery/base.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --id base-20260907-001 --destination /private/recovery/target-001 \
  --socket-directory /private/recovery/target-001/socket \
  --user postgres --database postgres --port 5432
```

Use the same reviewed physical backup configuration as preparation. The command
requires existing private directories with no symlink ancestors and private regular
`verified.json` and `pitr.json` records. It binds their backup ID, environment,
stack and configuration to the invocation. It connects only to an explicit local
socket, with explicit user/database/port, no psql startup file or password prompt,
and no inherited source service selection. The OS user needs peer-authenticated
access to a recovery database role allowed to read the server settings and
`pg_control_system()`; run against the isolated copy with its authorized recovery
administrator. No age identity or object-store credential is needed for observation.

The fixed query runs in a read-only transaction with a `pg_catalog` search path,
a 10-second statement timeout and an overall deadline of at most 30 seconds
(or the configured timeout, if shorter). It requires PostgreSQL 16, the expected
system identifier and exact restored data directory, recovery state true, replay
paused, read-only state, the prepared explicit target LSN/timeline with inclusive
pause action, replay at or beyond the target LSN, no TCP listener and archiving off.
These checks use [PostgreSQL's recovery and control functions](https://www.postgresql.org/docs/16/functions-admin.html).
An unavailable or mismatched server exits nonzero without a success observation.

Success emits `paused-target-observed` JSON with observation time, scope, target
and replay LSN. Preparation records remain unchanged. This is a point-in-time
observation, not a persistent guarantee against later promotion/configuration
changes. It does not establish why replay paused, independently verify every
replayed record, or prove application data correctness. In particular, review
server logs and expected data boundaries alongside the observation; a manually
paused server is not by itself proof of a successful target-driven rehearsal.

Verify required database contents and that later changes are excluded. Check issuer
mappings, matched provider state, CRLs, revocations and other PKI invariants before
any separate decision to promote or resume writers. Observation does not release a
maintenance lock, start/promote a server, write qualification records, or mark a
whole PKI restore as verified.

## Automated isolated PostgreSQL rehearsal

`base-backup rehearse` runs the restore/start/observe/stop sequence on a fresh local
copy. Run it as the non-root OS account that will own PostgreSQL and use a database
role available through the generated peer-authentication policy:

```sh
rtk-cloud base-backup rehearse --config /private/recovery/base.json \
  --confirm-environment staging --confirm-stack video-cloud-staging \
  --id base-20260907-001 --destination /private/recovery/drill-001 \
  --identity /private/recovery/age-identity.txt \
  --pitr-plan /private/recovery/target.json --user postgres --database postgres
```

The destination must be new, private, and on a filesystem supporting Unix sockets.
Its `socket/` path must fit the OS Unix-socket path limit. Install the PostgreSQL 16
server binary as well as backup/client tools in the configured binary directory.
The executable and WAL configuration in the PITR plan must be usable by that OS
account. Provision enough disk and memory for the restored cluster. Read-only
archive credentials and independent access to the age identity must be available
before the run; the command does not retrieve escrow or generate recovery keys.

The command downloads and verifies the immutable encrypted base, prepares the
isolated configuration, and directly owns the `postgres` process. It does not use a
shell or daemonize through `pg_ctl`. The child inherits only a minimal PATH/locale,
dedicated `RTK_BACKUP_*` credentials and optional `SSL_CERT_FILE`/`SSL_CERT_DIR`
trust paths. Ambient `PG*` and loader settings are excluded. PostgreSQL writes a
private `rehearsal-server.log` under the recovered directory. The server has TCP
and archiving disabled; the command never promotes it or opens application traffic.

Readiness polling uses the same observation checks above until the configured
deadline. Successful observation is followed by fast shutdown (SIGINT) and a
confirmed process exit. Shutdown survives normal caller cancellation and may take
up to 45 additional seconds: after 30 seconds it requests immediate shutdown, then
uses forced termination if necessary. Any forced or unconfirmed shutdown fails the
drill. SIGKILL, host failure or a stuck kernel cannot be handled by normal process
cleanup; use a service supervisor that cleans its process group and inspect retained
private data/processes after such failures.

Only successful observation **and** clean shutdown produce
`postgres-replay-rehearsed` JSON and durable `rehearsal.json`. The report includes
start/finish times, elapsed command duration and the runtime observation. Missing
WAL, server exit, cancellation and cleanup failure return nonzero without success
evidence. Recovered data and private logs are retained for inspection; failed
preparation follows the restore command's existing cleanup rules. An existing
destination is never reused, overwritten or silently removed by a rerun.

This automates the PostgreSQL replay portion of a drill. It does not assert PKI
application invariants, validate a leaf chain, test issuance, reconcile provider
snapshots, choose a recovery target, schedule repeats, or apply retention. Its
elapsed duration excludes infrastructure provisioning and escrow retrieval and is
not production RTO. Schedule the complete matched PKI rehearsal only after its
remaining provider/application checks and custody policy are implemented.

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
The drill also promotes the disposable recovered copy to timeline 2, creates
writes before and after a new recovery target, and archives PostgreSQL's native
history and completed WAL through the actual `wal-archive` CLI. A fresh copy of
the original timeline-1 base is restored with the explicit timeline-2 plan. It
must fetch encrypted history and promoted WAL, pause read-only with TCP disabled,
include the new branch's pre-target write and exclude both the old branch's later
writes and the new branch's post-target write. A further fresh restore with the
required timeline-history ciphertext removed must reject the missing timeline
and shut down. This exercises the timeline
ancestry described by [PostgreSQL's timeline documentation](https://www.postgresql.org/docs/16/continuous-archiving.html#BACKUP-TIMELINES).
The drill invokes the actual `base-backup observe` CLI on both paused timelines
and requires it to reject the first copy after explicit promotion.
The actual `base-backup rehearse` CLI also runs a fresh complete replay cycle and
a missing-WAL failure; both must leave their server stopped, and the failed run
must not write success evidence.
Only this disposable test explicitly promotes a restored server; the production
restore command continues to prepare a paused recovery without starting/promoting.

The fixture does not emulate provider IAM or prove production bucket policy.

This is local targeted-replay evidence across one real timeline fork. Multiple
failovers, time-based target selection, retention and scheduled restore rehearsals, matched
OpenBao/registry recovery, production failure-domain testing and measured
RPO <= 15 minutes / RTO <= 4 hours remain. Production PKI stays disabled until all
required operational acceptance evidence is complete.
