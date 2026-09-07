# Scheduled physical PostgreSQL backups

`rtk-cloud base-backup scheduled` adds a durable, single-host schedule to the
[verified physical backup workflow](postgresql-physical-backup.md). It uses the
same native PostgreSQL verification, age encryption, immutable upload and remote
readback as `base-backup create`. No database restart or writer pause is required.

## Policy and invocation

Example `schedule.json` for daily base backups:

```json
{
  "version": 1,
  "name": "daily",
  "interval_seconds": 86400,
  "state_file": "/var/lib/rtk/recovery/staging/schedule-state.json"
}
```

Intervals are 300 through 604800 seconds. The state parent must be a private
0700 directory with no symlink ancestors. State and lock files must be regular
0600 files. The schedule name is at most 32 characters and uses the recovery ID
character set. Keep one state file and one scheduler host per configured source.

```sh
rtk-cloud base-backup scheduled \
  --config /etc/rtk/recovery/staging/base.json \
  --schedule /etc/rtk/recovery/staging/schedule.json \
  --confirm-environment staging --confirm-stack YOUR_STACK
```

UTC Unix time determines interval slots. The first invocation captures immediately
for its current slot. A successful invocation emits `completed`; subsequent
invocations in that slot emit `not-due` without capture. Failures exit nonzero;
`pending` identifies a journaled attempt whose completion remains unconfirmed.
Do not treat JSON output alone as success: check the process exit status.

Before capture the scheduler durably records the slot and immutable backup ID.
Retries preserve that ID across slot boundaries, reusing already staged encrypted
bytes. Only verified remote completion advances the checkpoint. After an older
pending capture completes, the next invocation captures the current slot. Missed
slots are not fabricated as historical backups. A nonblocking file lock rejects
overlapping invocations using the same state file. This is not a distributed lock.
Clock regression and configuration/policy drift fail closed.

Do not remove scheduler state or the staged spool to clear a failure. Preserve
both, reconcile the pending object's completion, and retry with its original
configuration. Changing recipients, source configuration or cadence requires an
operator-controlled transition after resolving the pending backup. Archive the
old policy and state; use a distinct schedule name/state for the new policy so
backup IDs do not collide. Never reset a state file in place to bypass an error.

## Linux systemd deployment artifacts

Templates are under `cloud_deploy/recovery/systemd/`. They are supplied for
review and installation; the repository does not install or enable them.

Provision a dedicated `rtk-backup` system user/group. Install the built Linux CLI
at `/opt/rtk/bin/rtk-cloud` and the PostgreSQL 16 client tools at the configured
binary directory. Install reviewed source and schedule JSON under
`/etc/rtk/recovery/INSTANCE/`, readable by the service user. The PostgreSQL service
file must be private 0600 and readable by that user; configure authenticated TLS
with hostname verification and least-privilege backup access. Store its required
CA and authentication material outside home directories, which the unit hides.

Create a root-owned 0600 `runtime.env` at that location, using systemd
EnvironmentFile syntax, containing the exact `RTK_RECOVERY_ENVIRONMENT` and
`RTK_RECOVERY_STACK` confirmations and dedicated backup object credentials
(`RTK_BACKUP_ACCESS_KEY_ID`, `RTK_BACKUP_SECRET_ACCESS_KEY`, optionally
`RTK_BACKUP_SESSION_TOKEN`). Do not put credentials in command arguments or Git.
Use the physical backup guide's reviewed remote namespace and age recipients;
the capture host needs no age decryption identity.

Set both the WAL configuration's `directory` (the physical capture spool) and
schedule state path beneath `/var/lib/rtk/recovery/INSTANCE/`. systemd creates
that private persistent directory and permits writes there under the unit's
read-only filesystem policy. Ensure adequate capacity for plaintext verification
and encrypted staging. The service's temporary directory is private. Use separate
configuration/spool ownership for a PostgreSQL server's native WAL archiver;
do not share this service's private state directory with the database user.

After installation and review, an operator can run:

```sh
systemd-analyze verify /etc/systemd/system/rtk-base-backup@.service /etc/systemd/system/rtk-base-backup@.timer
systemctl daemon-reload
systemctl start rtk-base-backup@INSTANCE.service
journalctl -u rtk-base-backup@INSTANCE.service
systemctl enable --now rtk-base-backup@INSTANCE.timer
```

The calendar timer checks every minute, including a catch-up activation after
downtime. The application controls cadence; systemd does not start a second
instance while its oneshot service is active. Nonzero failures remain visible in
the journal and are retried on later timer activations. Operators must connect
failed runs, overdue completed backups and spool capacity to their monitoring.
No notification destination or provider is assumed by these templates.

## Qualification boundary

Daily base backups do not imply a daily or 15-minute RPO: continuous WAL archival,
archive lag and successful replay determine change coverage. This scheduler does
not delete backups, apply retention, perform recovery rehearsals, or prove RTO.
Spool and remote storage grow until a reviewed retention procedure is applied.

It schedules online PostgreSQL physical backups only. The coordinated core backup
command currently pauses writers and leaves recovery fenced; do not substitute it
into this timer. Independently timed PostgreSQL and OpenBao snapshots do not form
a consistent PKI recovery checkpoint. Matched provider/registry recovery,
revocation reconciliation, live restore rehearsals and custody acceptance remain
separate required work before production qualification.
