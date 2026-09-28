# Staging SQLite storage cutover

Status: preparation only; no live data copy, PVC creation or Pod replacement has been performed.

This procedure covers the existing `video-cloud-staging` Cloud Admin and frontend
Pods. They currently keep SQLite files in their container writable layers. The
PVC renderer and cutover guard are described in
[support-ticket-zammad.md](support-ticket-zammad.md#cloud-admin-state-prerequisite).
A new Pod cannot recover those layers, so the source Pods must stay running until
an independently recoverable copy and populated PVCs have been verified.

## Observed source, 2026-09-28

| Deployment | Source directory | Files observed | Owner |
| --- | --- | --- | --- |
| `video-cloud-staging-admin/cloud-admin` | `/app/data` | `rtk-cloud-admin.db` | 10001:999 |
| `video-cloud-staging-frontend/frontend` | `/data` | `connectplus.db`, `analytics.db` | 100:101 |

No WAL/SHM sidecars or `search.db` were present at inspection time. Each of
these namespaces had one Deployment and one Service selecting its app label; no
StatefulSet or CronJob was present there. These are observations, not proof that
all writers are fenced or a cutover inventory. Both images contain `tar` and
`sha256sum`, but neither contains `sqlite3` or Python. Recheck Pod UIDs and
all database, WAL, SHM and journal files immediately before maintenance:

```sh
scripts/check-sqlite-migration-source.sh \
  --stack video-cloud-staging \
  --kubeconfig "$STAGING_KUBECONFIG"
```

The script only reads Deployment and Pod metadata and lists file metadata. It
must fail if either Deployment no longer has one replica or has a data mount,
or if its labeled source Pod is ambiguous. Save its
sanitized output with the protected Go/No-Go record. Do not use the UIDs printed
in this document as an attestation after a Pod change.

## Go/No-Go before any copy or cutover

1. Run the staging read-only credential qualification for the selected images,
   the mTLS check, and the selected release's live LKE provider preflight and
   plan. The plan must use the actual operator configuration, report
   `additional_required`, and prove room for two retained SQLite PVCs plus
   three Zammad PVCs. Do not invent an active-services limit.
2. Confirm a reviewed independent private backup bucket, age X25519 recipient
   and independently retrievable private identity. Keep all plaintext capture
   files in a private `0700` directory on an encrypted disk, outside Git and
   shared temporary directories. A backup in the same cluster is insufficient
   as the only rollback copy.
3. Record exact current image references, Deployment and Pod UIDs, database
   filenames, sizes, SHA-256 values, schema versions, row counts and rollback
   image references. Record counts and schema only; exclude session and customer
   values from logs.
4. Prepare and rehearse a traffic/worker fence for both applications. Confirm
   no external or background writer remains and no request is in flight. Do not
   scale either source Deployment to zero: that destroys its container-layer
   database. A stable checksum alone does not establish a write fence.
5. Reconcile the environment-local runtime controller files with the selected
   release and live cluster. Run the protected preflight/plan and retain its
   sanitized result before entering maintenance. If any condition fails, stop.

## Controlled cutover sequence

1. Announce and enter the approved maintenance window; apply and verify the
   traffic/worker fence. Keep both source Pods alive. Re-read their UIDs and
   file inventories. If a UID changes, stop and restart the assessment from the
   new source.
2. While writes remain fenced, capture each complete SQLite file set, including
   any WAL/SHM/journal files that appear. Neither image has `sqlite3`, and
   copying an actively written database file is unsafe. Create separate empty
   private `0700` directories on an encrypted disk outside Git and shared
   temporary directories. Use the source UIDs from the **current** maintenance
   inventory, and the selected staging kubeconfig:

   ```sh
   go run ./scripts/go/rtk-cloud -- sqlite-migration-capture \
     --stack video-cloud-staging --kubeconfig "$STAGING_KUBECONFIG" \
     --workload cloud-admin --source-pod-uid "$ADMIN_SOURCE_UID" \
     --output-dir "$PRIVATE_ADMIN_COPY"
   go run ./scripts/go/rtk-cloud -- sqlite-migration-capture \
     --stack video-cloud-staging --kubeconfig "$STAGING_KUBECONFIG" \
     --workload frontend --source-pod-uid "$FRONTEND_SOURCE_UID" \
     --output-dir "$PRIVATE_FRONTEND_COPY"
   ```

   The command checks the single source replica, rejects an existing data
   mount, copies only regular SQLite files through a tar stream, compares every
   copied SHA-256 with the source, then rechecks the Pod UID and all hashes. It
   rejects a changed file set and removes partial copies on failure. This
   checks copy consistency; the independently verified write fence remains
   mandatory. Keep the emitted source hashes in the Go/No-Go record. Run
   `sqlite-migration-pack` separately for Admin and frontend; it checks every
   database with `PRAGMA integrity_check`,
   rejects unexpected files and symlinks, and creates a `0600` age archive with
   a file-hash manifest:

   ```sh
   go run ./scripts/go/rtk-cloud -- sqlite-migration-pack \
     --workload cloud-admin --source-dir "$PRIVATE_ADMIN_COPY" \
     --source-pod-uid "$ADMIN_SOURCE_UID" \
     --recipient "$AGE_RECIPIENT" --output "$PRIVATE_ARCHIVE_DIR/admin.age"
   go run ./scripts/go/rtk-cloud -- sqlite-migration-pack \
     --workload frontend --source-dir "$PRIVATE_FRONTEND_COPY" \
     --source-pod-uid "$FRONTEND_SOURCE_UID" \
     --recipient "$AGE_RECIPIENT" --output "$PRIVATE_ARCHIVE_DIR/frontend.age"
   ```

   Record the printed SHA-256 of each **encrypted** archive. Independently
   record representative schema versions and row counts without customer
   values. Upload to the reviewed private backup store, read the archives back
   and prove that the escrowed identity decrypts them. Do not continue on a
   partial archive or unverified upload. After read-back and decrypt verification,
   securely remove the local plaintext copies; if packing was interrupted,
   inspect and remove only its leftover `.sqlite-verify-*` directory inside the
   private source directory. The pack command does not access Kubernetes, fence
   writers, transfer files or upload archives.
3. Create `cloud-admin-sqlite-data` and `frontend-sqlite-data` as separate
   `ReadWriteOnce` PVCs using `linode-block-storage-retain`. Bind each PVC to a
   short-lived migration Pod using a reviewed image and the target filesystem
   group (Admin 999, frontend 101). Copy the verified file sets into the
   respective volume roots, preserve file ownership and modes, and compare the
   bytes with the independently retained archive. Check SQLite integrity and
   representative counts from the populated PVC before releasing the helper.
   The copy mechanism and helper image require their own review; annotations
   alone are not proof of copied data.
4. Recheck the still-running source Pod UID and write fence. Annotate each Bound
   PVC with `rtk.realtek.com/sqlite-source-pod-uid` set to that UID and
   `rtk.realtek.com/sqlite-copy-sha256` set to the verified archive's 64-character
   SHA-256. Set `LKE_CLOUD_ADMIN_SQLITE_PVC_ENABLED=true` and
   `LKE_FRONTEND_SQLITE_PVC_ENABLED=true` in the canonical staging configuration.
   The renderer then uses one replica, `Recreate`, and mounts `/app/data` or
   `/data`. Run the protected plan again and deploy the selected CI images.
5. Before reopening traffic, verify both new Pods mount the intended PVCs,
   database integrity, Admin sessions/audit/support read markers, frontend lead
   and analytics records, and representative authenticated flows. Record the
   running image digests and both source-to-PVC checksums. Add both PVCs to the
   matched core backup inventory and complete a staging restore drill before
   enabling support tickets.

If the cutover fails, keep traffic fenced. Restore from the independent archive
into the retained PVC or a newly verified claim, then redeploy and verify. The
old source Pod may already be gone after `Recreate`; reverting the Deployment
image or disabling the PVC flag does not restore its writable layer. Escalate a
Pod UID change, incomplete copy, checksum mismatch or failed integrity check
without starting an empty database.
