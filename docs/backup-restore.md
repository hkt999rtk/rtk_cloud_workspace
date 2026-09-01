# Core Backup and Restore

Status: active procedure; live environment qualification required before production use.

Owner: rtk_cloud_workspace (orchestration); service owners (dataset and recovery checks).

Last reviewed: 2026-08-31.

Classification: source for cross-repository operations. The
[Platform PKI contract](../repos/rtk_cloud_contracts_doc/platform_pki.md#14-backup-escrow-and-disaster-recovery)
remains authoritative for trust, issuer recovery and escrow. This procedure does
not weaken its production gates.

Implementation: [workspace command](../scripts/go/rtk-cloud/recovery.go) and
[recovery package](../scripts/go/rtk-cloud/internal/recovery/). Environment-owned
inventory/checks configure the adapter; archive contents never supply commands.

## Decision and Scope

Version 1 uses a **manual maintenance window**, not a zero-downtime backup.
Stop all application writers and external ingress before taking one matched
backup set. PostgreSQL and Redis stay available for logical exports; OpenBao
file storage and SQLite writers stop before their PVCs are read. A backup is
not a cross-system transaction: consistency depends on holding the write fence
throughout capture.

The same commands accept `--environment staging`, `--environment prod`, or
another configured logical environment. There is no staging fallback. A
restore may target newly deployed infrastructure or the existing deployment,
but must retain the **same environment, stack, dataset selection and workload
images**. Copying prod into staging is not supported.

The archive declares `scope=core`. Media, clips, firmware and other object
payloads are excluded in v1. A successful core restore is **not** a complete
environment disaster-recovery claim. Independently retain/check the referenced
objects, release images and external audit history.

## Data Inventory and Ownership

| Data | v1 treatment | Restore requirement |
| --- | --- | --- |
| Account Manager, Billing and Video Cloud PostgreSQL | Custom-format dumps for every application database; globals per server. | Same PostgreSQL major version and release; roles, permissions, schema, rows, ownership, billing and outbox consistency checks. |
| OpenBao file storage | Offline archive of the entire dedicated PVC, in the same backup set as PostgreSQL. | Original seal/unseal access, issuer IDs, private keys, policies, auth configuration, trust chains and revocation reconciliation. Never replace a lost issuer with a newly generated Root and call it recovery. |
| Frontend SQLite | Offline PVC archive including `connectplus.db`, `analytics.db` and any remaining WAL sidecars. | `PRAGMA integrity_check` on both databases, lead/admin and analytics checks. |
| Cloud Admin SQLite | Offline PVC archive and explicitly listed database files. | Integrity check and local Admin configuration/session behavior. Account Manager remains the identity authority. |
| Durable Redis/Valkey | Explicit literal key-prefix inventory, serialized with DUMP/RESTORE; no TTL allowed. | Shadow documents, named indexes, tombstones and notification outbox survive together; verify version semantics and pending delivery. |
| Redis cache, sessions, locks, leases | Exclude from durable prefix inventory; do not restore. | Invalidate derived authorization/cache data after database restore. Do not reuse old worker leases. |
| Runtime secrets and deployment PKI | Explicit SecretStore `runtime/`, `pki/`, and OpenBao TLS files, plus selected Kubernetes Secrets/ConfigMaps. | Replace selected files, remove stale selected credentials, synchronize consumers, compare certificate fingerprints. |
| Worker checkpoints and non-reproducible service configuration | Offline PVC component when enabled (for example Azure Event Hub checkpoint). | Reconcile checkpoint/outbox boundaries before worker startup. |
| EMQX | Back up non-reproducible configuration/TLS references using selected ConfigMaps/Secrets or a dedicated offline configuration PVC. | Broker auth and representative MQTT publish/subscribe checks. Broker runtime sessions/queues are not automatically covered; declare any required persistent broker state explicitly. |
| Git configuration and workload releases | Git plus encrypted workload image inventory. | Obtain the recorded release images separately; changed images block restore. No automatic migrations during restore. |
| Media/firmware object payloads | Excluded; document bucket/prefix/version dependencies. | Check referenced objects still exist; object deletion cannot be undone with PostgreSQL. |
| OpenBao audit, application audit, Loki/log retention | Independently retained, not rewound by core restore. | Preserve both pre- and post-restore history and record the recovery operation. |
| Operator/cloud credentials, kubeconfig, age identities, unseal/recovery shares, root tokens, offline Root/HSM | **Separate escrow**, not automatically included in the core archive. | Obtain through the approved recovery process. An OpenBao backup cannot unseal itself. |
| Test Device/user credentials | Separate controlled controller handoff, not production core data. | See [staging runtime bootstrap](staging-runtime-bootstrap.md); never recreate production identity by regenerating test fixtures. |

Redis is not uniformly disposable: the
[Device Shadow persistence contract](../repos/rtk_video_cloud/docs/device-shadow-spec.md)
requires durable documents/indexes/outbox. Current LKE Redis manifests use
`emptyDir`; core export does not fix that crash-durability gap. A production
profile must satisfy the service's AOF/storage requirement before qualification.

For the default shadow store, select `video_cloud:shadow:` and explicitly
exclude the exact `video_cloud:shadow:outbox:publisher-lock` key with
`exclude_keys`. This retains the outbox stream itself, retry/dead-letter and
per-shadow version state, while omitting the expiring publisher lease. Review
prefix overrides against the deployed service configuration. Any other
unexpected expiring key fails capture rather than silently disappearing.

## Supported Adapter and Limits

The current adapter uses Kubernetes APIs/`kubectl` for the workspace LKE layout,
PostgreSQL client tools inside the database pod, Redis-compatible `redis-cli`
inside its pod, and digest-pinned helper pods for offline PVCs. Go implements
age encryption and SQLite integrity checks; an `age` executable is not needed
to run backup/restore.

Version 1 deliberately rejects or does not implement:

- OpenBao Raft/HA, HSM restoration and managed/external database adapters;
- custom PostgreSQL tablespaces, non-simple role names in globals restoration,
  cross-version upgrades and cross-environment cloning;
- Redis Cluster, expiring keys inside durable prefixes and provider-managed
  Redis that cannot use the reviewed local CLI/auth setup;
- symlinks, special files, path traversal and duplicate members in file archives;
- online snapshots, WAL/PITR, scheduling, automatic retention/pruning and bucket
  creation;
- encrypted archives above the configured limit (maximum 4 GiB for single-PUT
  v1); larger environments need a reviewed multipart/streaming adapter.

Keep helpers private, use a reviewed image digest providing `sh`, `sleep`, `tar`
and `find -xdev -mindepth -delete`, and verify UID/GID restoration in the drill.
Helpers temporarily run as UID 0 with narrowly listed file-management
capabilities; no service-account token is mounted. A helper may mount only the
explicit inventoried PVC after all other pod mounts have disappeared.

## Preparation and Configuration

Use [the configuration example](examples/backup-config.example.json) as a
schema illustration, **not** as a discovered or deployable inventory. Replace
all placeholders and review it per environment; do not simply rename staging
to prod. Keep the actual non-secret configuration with the environment intent,
for example `cloud_env/<environment>/backup.json`.

Required preparation:

1. Inventory all Deployments/StatefulSets, PVCs, application databases, durable
   Redis prefixes, SQLite files, runtime Secrets/ConfigMaps and external
   dependencies. Include Billing, OpenBao and worker processes, not only APIs.
   Every PVC must be included or explicitly excluded with a reason.
2. Classify workload roles: `application` stops before quiescence checks;
   `offline` stops before file capture; `data` stays up for PostgreSQL/Redis
   logical capture. Do not classify a business worker as `data`.
3. Establish and test an external traffic/write fence: public API, MQTT/device
   writes, webhooks, email/payment dispatch, direct database writers and jobs
   outside the listed namespaces. Disable GitOps/redeployment/autoscaler
   interference. RBAC and network policy, not a local journal, enforce this
   boundary. Preserve the prior controller/traffic configuration separately.
4. Suspend CronJobs; drain or suspend unfinished Jobs. HPAs and DaemonSets in
   the recovery namespaces are blockers in v1 and need an operator-reviewed
   arrangement. Unknown workloads, running unmanaged pods and PVCs fail
   preflight. No controller is silently deleted by backup.
5. Prepare an independent private backup bucket with least-privilege access,
   HTTPS, public-access denial, retention and optionally immutability. Do not
   reuse a public release-artifact bucket/prefix. Bucket privacy, independent
   failure domain and retention are operator qualification checks; the client
   does not infer them from an endpoint name.
6. Prepare a dedicated absolute local backup directory with mode `0700` on an
   encrypted disk; files are `0600`. No symlink ancestors. Do not use the Git
   workspace, a shared temporary directory or the SecretStore itself as the
   archive destination. Temporary plaintext capture/validation files exist
   only inside this private directory and are removed on normal completion or
   handled failure. After a killed process, review/remove only its leftover
   `.capture-*`, `.restore-*`, `.retry-*`, `.inspect-*` directories.
7. Configure age X25519 public recipients. Protect the corresponding identity
   files independently of the cluster and backups; do not put a private
   identity in configuration, argv, logs or the core archive. Test retrieval
   of the **original** OpenBao seal/recovery material and public CA fingerprints.
8. Configure reviewed environment-specific check executables. Their argv must
   contain no secrets; use protected files or environment injection. Checks
   run without shell interpolation; output is suppressed and only check IDs
   appear in errors. They are operator code, never loaded from an archive.

| Check list | Required responsibility |
| --- | --- |
| `preflight_checks` | Target identity, held external traffic/worker fence, suspended external automation, independent private backup storage, escrow availability, supported OpenBao file backend, storage capacity and source/target compatibility. |
| `startup_checks` | Reconfirm the external ingress/dispatch fence and automation exclusion immediately before private service startup, including a retry. Keep this independent of remote-backup availability so an upload outage does not prevent recovery of unchanged source services. |
| `quiescence_checks` | Zero business writes/in-flight work, no direct writers, stable checkpoint/outbox boundary; must work while OpenBao is offline too. |
| `recovery_checks` | Wait for private offline services, unseal using original escrow where necessary, reconcile runtime secrets/Kubernetes auth for the target cluster, invalidate disposable authorization caches, compare CA/issuer/trust data and database references; keep charge/email workers gated. |
| `health_checks` | Bounded private smoke tests: databases, SQLite-backed functionality, existing certificate validation plus controlled issuance, authentication, shadow/index/outbox and MQTT. Must not generate real charges or uncontrolled notifications. |

The example deliberately points to absent operator check executables. Missing
checks fail; do not replace them with `true`. This is not yet a plug-and-play
live-environment qualification profile. A service owner must implement/review
the checks for the exact deployment and record drill evidence.

Remote credentials are supplied only through
`RTK_BACKUP_ACCESS_KEY_ID`, `RTK_BACKUP_SECRET_ACCESS_KEY`, and optionally
`RTK_BACKUP_SESSION_TOKEN`. They do not fall back to media/release credentials.
Kubeconfig defaults to the selected SecretStore's `kube/kubeconfig.yaml`;
`--kubeconfig` can explicitly select the rebuilt target. `--config-root` changes
the SecretStore parent, not the logical environment.

## Commands and Maintenance State

Run from the workspace root. The following uses staging only as an example:

```sh
go run ./scripts/go/rtk-cloud -- backup plan \
  --environment staging --config cloud_env/staging/backup.json

go run ./scripts/go/rtk-cloud -- backup preflight \
  --environment staging --config cloud_env/staging/backup.json \
  --confirm-environment staging --confirm-stack video-cloud-staging

go run ./scripts/go/rtk-cloud -- backup create \
  --environment staging --config cloud_env/staging/backup.json \
  --confirm-environment staging --confirm-stack video-cloud-staging
```

`plan` reads configuration only; it does not contact or change the cluster.
`preflight` also executes the explicitly configured operator checks.
`create` records original replicas in the `rtk-core-recovery` ConfigMap and
`<SecretStore>/recovery-state.json` **before** scaling anything. It captures,
validates and encrypts the matched set, uploads it under
`<remote.prefix>/<stack>/<backup-id>.age`, reads it back in full to compare its
SHA-256, then publishes `<backup-id>.complete.json`. It does **not** reopen
traffic or automatically resume services.

Completion means a validated encrypted archive reached remote storage; it does
not mean a live recovery rehearsal passed. age authenticates ciphertext but
does not establish who created a backup. Restore only trusted operator-owned
archives/completion records: PostgreSQL dumps contain executable SQL.

To finish a successful source backup, run `backup verify` then `backup resume`
with the same environment/config/confirmation flags. Verification starts
private services, performs recovery checks before applications start, then runs
health checks. Restore the external ingress/controller configuration only after
sign-off. The original workload replica counts are preserved.

`backup status` or `restore status` shows the persisted phase. An individual
command also holds a local `recovery-command.lock` and a cluster
`rtk-core-recovery-command` ConfigMap to refuse competing controllers. Interrupted
commands never time out these locks automatically. Before manually removing
**only the command locks**, prove the old process is stopped and no helper is
still operating. Keep the maintenance journal.

The cooperating workspace CLI refuses normal mutating commands while it sees
a local recovery journal/command lock. It is a conservative controller-local
guard, not protection against another host, direct Kubernetes calls or GitOps.

## Restore After Deployment

1. Prepare the target's infrastructure, namespaces, PVCs and **matching release
   images** first. For new infrastructure, isolate ingress and external side
   effects before deployment, do not onboard devices or create business data,
   and do not treat newly bootstrapped identities as replacements for the saved
   PKI. The current provision command is not a dedicated recovery-target mode.
   Restoring data is a separate explicit action after deployment.
2. Obtain the backup, original unseal/escrow access and age identity independently.
   Recreate Kubernetes auth/access for the target cluster as necessary; its
   kubeconfig, UID and workload auth are not copied blindly from the source.
3. Download with `backup fetch --id BACKUP_ID` (plus config, environment and
   confirmation flags), or securely transfer an existing encrypted archive.
   Fetch requires a valid completion marker and matching ciphertext checksum;
   existing local filenames are never overwritten.
4. Run `restore inspect --file /private/backups/BACKUP_ID.age --identity
   /private/escrow/backup.agekey` with config/environment flags. This decrypts
   into a private temporary directory, authenticates the full stream, checks
   every member/hash and matches the logical inventory without touching live
   target data. Inspection is not a service recovery test.
5. Run `restore apply` with the same file/identity and both confirmation flags.
   It validates artifacts and target workload images before entering
   maintenance, then takes and remotely verifies a **mandatory target safety
   backup**, even for a newly deployed target. No target dataset is overwritten
   unless this succeeds. The source archive and safety archive have distinct IDs.
6. Application/database writes stay fenced. Restore globals before PostgreSQL
   databases, the stopped OpenBao/SQLite/configuration PVCs, selected runtime
   secrets and durable Redis prefixes. The recorded target inventory, not
   arbitrary names in the backup, selects resources to mutate. Offline PVC
   restore replaces all contents of that specific PVC; never mix unrelated
   data on it. Existing selected SecretStore files and Redis keys are replaced,
   including removal of stale entries.
7. Run `restore verify` with config/environment/confirmation flags. Recovery
   checks must reconcile PKI/revocation, seal access, target Kubernetes auth and
   application credentials before workers can run. Verification may start
   applications **behind the still-held external fence**; payment/email workers
   require their own dispatch gates, not only blocked ingress.
8. Prepare a reviewed reconciliation JSON (below), then run `restore resume
   --reconciliation /private/evidence/reconciliation.json` with the same flags.
   Verification must have passed within 30 minutes. Resume rechecks health,
   records the operation and clears maintenance locks; the operator then
   reopens traffic/controllers. Never re-enable a charge merely because its
   completion row disappeared in the rollback.

```json
{
  "environment": "staging",
  "stack": "video-cloud-staging",
  "operation_id": "REPLACE_WITH_RESTORE_OPERATION_ID",
  "backup_id": "REPLACE_WITH_SELECTED_BACKUP_ID",
  "approved_by": "REPLACE_WITH_APPROVING_OPERATOR",
  "payment_and_email_reconciled": true,
  "pki_and_revocation_reconciled": true,
  "objects_and_audit_checked": true
}
```

The booleans are operator attestations, not automated proof. Retain the
supporting evidence separately without secrets. Reconcile revoked credentials
and external security changes after the backup point so restoration does not
reactivate revoked access.

## Failure, Retry and Retention

| Failure | Safe action |
| --- | --- |
| Invalid config/archive/target image | Fix before applying; no dataset overwrite. |
| Capture or upload failure | Maintenance remains. Encrypted local archive, if completed, is retained. Retry only `backup upload --id ID`; a completion marker is published only after exact remote readback. |
| Source or target stopped before restore mutation | `backup abort` restores/verifies the unchanged service state, then explicit `backup resume`. This does not create a successful backup claim. |
| Partial restore / failed verification | Keep fencing and journal. Repair the cause; `restore retry --file ... --identity ...` accepts only that operation's original or safety backup. It re-pauses writers and re-applies the complete set. Then verify and reconcile again. No unlock-only escape after overwrite. |
| Remaining helper pod | Inspect/remove only `rtk-recovery-<component-id>` after confirming the command stopped; a mounted helper blocks further file operations. |
| Controller loss | Recover the exact reviewed configuration, SecretStore/escrow and target kubeconfig. The cluster journal is authoritative; `status` checks namespace UID and configuration hash. Do not delete it to hide a partial restore. |

Never prune automatically in v1. The operator defines per-environment RPO,
RTO, retention, owner and backup frequency. RPO is the age of the last usable
matched set; RTO must be measured from a drill, not inferred from upload time.
Manual maintenance backups do not satisfy a future continuous-backup/PITR SLA.

## Verification and Production Gate

Local tests:

```sh
go test ./scripts/go/rtk-cloud/internal/recovery
RTK_RECOVERY_INTEGRATION=1 go test ./scripts/go/rtk-cloud/internal/recovery -count=1
go test ./scripts/go/rtk-cloud -run TestRecovery -count=1
go run ./scripts/go/rtk-cloud -- docs-check
go run ./scripts/go/rtk-cloud -- contracts-check
```

Opt-in integration tests use a temporary Unix-socket Redis and a uniquely named,
network-isolated Docker PostgreSQL 16 fixture; they do not pull images or touch
deployed resources. Missing local dependencies produce explicit skips.

Before production use, rehearse the **whole configured set** on isolated
same-environment recovery infrastructure: OpenBao file/PVC restoration and
original unseal, existing-leaf validation/test issuance, PostgreSQL and SQLite
service checks, durable Redis state, secret/auth rebinding, remote S3-compatible
storage readback, interrupted operations and safety-backup rollback. Record
measured downtime/RPO/RTO and all exclusions. Unit tests and local database
drills are not evidence that LKE/OpenBao or a remote bucket has been qualified.

The old [restore-staging-runtime.sh](../scripts/restore-staging-runtime.sh) copies
only an allowlisted **non-secret controller runtime**. It is not database,
OpenBao, certificate, SecretStore or cloud disaster recovery.
