# PostgreSQL Capacity Expansion Runbook

Status: operational design and validation contract.

This runbook defines how to respond when PostgreSQL capacity must grow in a
production-like deployment. It covers storage growth first, but also separates
storage pressure from compute, I/O, connection, and workload problems. It does
not promise zero downtime until the exact storage driver, filesystem, and
PostgreSQL topology have passed the validation described here.

## Scope and decision rule

Classify the pressure before changing infrastructure:

| Symptom | First response | What it does not solve |
| --- | --- | --- |
| PVC/filesystem is filling | Expand the existing PVC or managed DB storage | CPU, IOPS, query plans, or table bloat |
| `pg_wal` is growing | Check long transactions, replication slots, archiving, and backups | General data-volume growth |
| High read latency/QPS | Index/query work, read-through cache, or a read replica | Primary write capacity or storage size |
| Too many connections | Pooling and connection-limit review | Disk pressure |
| High CPU/RAM/IOPS | Resize the DB instance/node or redesign the workload | A full filesystem |
| Table/index bloat | Vacuum, reindex, partitioning, or retention policy | A genuinely larger data set |

Never delete PostgreSQL data files, `pg_wal`, or replication metadata as an
emergency space-saving measure. Stop or reduce non-essential writers while the
capacity path is being evaluated if the filesystem is close to full.

## Supported deployment choices

| Deployment | Storage expansion path | Online behavior | Required fallback |
| --- | --- | --- | --- |
| LKE StatefulSet with Linode CSI PVC | Increase the existing PVC request | May be online when the StorageClass, CSI driver, and filesystem support it | Planned Pod restart or PostgreSQL failover |
| Linode VM with Block Storage | Resize the Linode volume, then resize the filesystem | The provider's manual procedure is not assumed to be online | Maintenance window or standby cutover |
| Managed PostgreSQL | Provider storage resize operation | Use the provider's documented SLA and maintenance behavior | Provider failover or migration |
| New PostgreSQL target | Replication or dump/restore, then writer cutover | Can be made low-downtime, but is a migration rather than an in-place resize | Keep the old primary read-only until validation completes |

Linode Block Storage can be increased but not decreased. Its general resize
procedure includes powering off an attached Linode and resizing the filesystem;
that procedure must not be confused with Kubernetes CSI online expansion.
See the [Linode resize-volume documentation](https://techdocs.akamai.com/cloud-computing/docs/resize-a-volume)
and [Linode volume API documentation](https://techdocs.akamai.com/cloud-computing/docs/manage-block-storage-volumes-with-the-api).

## LKE PVC expansion contract

An LKE PVC resize is eligible for an online test only when all of these are
true:

- The PVC is backed by a dynamically provisioned Linode CSI volume.
- The StorageClass has `allowVolumeExpansion: true`.
- The installed Linode CSI driver advertises volume expansion.
- The filesystem is supported and can expand while mounted, or the workload
  has an approved restart/failover path.
- The provider account has enough Block Storage quota and the requested size is
  larger than the current size.
- A recent PostgreSQL backup has passed the applicable restore check.
- The application can tolerate the resize fallback defined for this topology.

Kubernetes supports resizing an in-use PVC, but the CSI driver and filesystem
remain the authority for whether node-side filesystem expansion is online. See
the [Kubernetes PersistentVolume documentation](https://kubernetes.io/docs/concepts/storage/persistent-volumes/).

The configured `LKE_POSTGRES_STORAGE` value describes the generated workload
manifest for provisioning. It does not by itself resize an already-bound
`data-postgresql-0` claim. Existing claims require an explicit, audited resize
operation.

## Preflight checks

Record the following before changing capacity:

```sh
NS=video-cloud-staging-platform
PVC=data-postgresql-0

kubectl -n "$NS" get statefulset postgresql
kubectl -n "$NS" get pod postgresql-0 -o wide
kubectl -n "$NS" get pvc "$PVC" -o yaml
kubectl get storageclass -o yaml
kubectl -n "$NS" describe pvc "$PVC"
```

Confirm and record:

- current requested and actual capacity;
- StorageClass, provisioner, reclaim policy, and `allowVolumeExpansion`;
- PV and provider volume identity;
- CSI driver version and expansion capability;
- filesystem type and current free space;
- PostgreSQL database sizes, `pg_wal` size, replication slots, long-running
  transactions, and backup status;
- current API error rate, latency, and PostgreSQL readiness;
- target size, expected cost, quota headroom, and rollback/fallback owner.

At 70%, create a capacity warning. At 80%, schedule expansion. At 90%, require
operator approval for urgent expansion and investigate growth cause. At 95%,
protect the database by reducing non-essential writes while expansion runs.

## LKE online expansion procedure

Use an explicit target size; never shrink a claim:

```sh
kubectl -n "$NS" patch pvc "$PVC" --type=merge \
  -p '{"spec":{"resources":{"requests":{"storage":"100Gi"}}}}'
```

Then monitor the provider and Kubernetes state:

```sh
kubectl -n "$NS" get pvc "$PVC" -w
kubectl -n "$NS" describe pvc "$PVC"
kubectl -n "$NS" get pv
kubectl -n "$NS" exec postgresql-0 -- df -h /var/lib/postgresql/data
```

During the operation, verify that:

- the PostgreSQL Pod remains Ready;
- API read/write smoke continues to pass;
- no `FileSystemResizePending`, attach, mount, or CSI errors remain;
- the filesystem reports the new size;
- PostgreSQL can write, checkpoint, and restart cleanly if a restart is part of
  the approved fallback;
- the final PVC/PV/provider capacities agree.

If the claim reaches `FileSystemResizePending`, follow the approved restart or
failover path. Do not repeatedly patch the claim or delete the PVC. Preserve
the PV and provider volume while investigating.

## Non-online and migration paths

Use a maintenance path when the CSI driver or filesystem cannot expand online:

1. Announce the maintenance window or route traffic to a standby.
2. Confirm a current backup and record the source volume identity.
3. Stop writes or fail over the PostgreSQL writer.
4. Resize the volume/filesystem or attach the larger target.
5. Start PostgreSQL and wait for recovery/readiness.
6. Run schema, row-count, read/write, and application smoke checks.
7. Re-enable writes and monitor errors, latency, WAL, and free space.
8. Keep the previous volume/primary available for the agreed rollback window.

Use replication plus writer cutover when a single-replica restart is not
acceptable, when the storage class cannot be resized, or when the database
must move to a different provider, region, machine type, or filesystem.

## HA requirement for a zero-downtime promise

PVC online expansion can remove the need for a planned restart, but it does not
provide database availability if PostgreSQL, the node, the CSI attachment, or
the filesystem fails. A production zero-downtime objective requires a standby,
health-checked failover endpoint, replication lag monitoring, and a tested
rejoin/rollback procedure, or a managed PostgreSQL service with an equivalent
provider contract.

The current single PostgreSQL StatefulSet is therefore documented as
"online expansion subject to validation", not as an HA or zero-downtime
guarantee.

## API and cache boundaries

The Account Manager and Video Cloud APIs should be the only application-facing
database boundary. API/store interfaces allow the operator to change the
PostgreSQL endpoint, introduce a read replica, fail over to a new primary, or
run a migration without changing clients.

Read-through cache, projections, and device-shadow hot state can reduce read
pressure and make some read paths more tolerant of a short database event. They
do not expand PostgreSQL storage and must not replace PostgreSQL for
transactions, ACL decisions, quota mutation, lifecycle state, outbox/inbox
claims, or other correctness-critical writes. PostgreSQL remains the source of
truth; cache behavior follows [the persistence/cache roadmap](persistence-cache-refactor-roadmap.md).

## Validation drill and evidence

Before calling a production-like profile online-expandable, run this drill in
staging:

1. Start PostgreSQL with a small PVC and continuous API read/write smoke.
2. Resize the bound PVC to a larger target.
3. Record Pod readiness, application downtime, PVC/PV conditions, CSI events,
   filesystem size, and PostgreSQL logs.
4. Verify database checksums/row counts for the test data and run a restart
   recovery check.
5. Repeat with the documented failure/fallback path if the driver reports an
   offline filesystem resize.
6. Store the sanitized result with the deployment evidence and record the
   tested Kubernetes, CSI, StorageClass, and filesystem versions.

The result must explicitly state:

```text
online_resize_supported: true|false
pod_restart_required: true|false
observed_application_downtime_seconds: <number>
filesystem_resize_mode: online|offline|unknown
fallback: <restart|standby-failover|new-primary-migration>
rollback_owner: <team or role>
```

Do not enable automatic disk-driven expansion until quota, cost limits,
approval, audit logging, backup verification, and the failure path are defined.
The first implementation should be an operator-approved, observable resize.
