# Private Zammad service for RTK Cloud Admin

Status: dev deployed and enabled on 2026-09-28; staging and production remain disabled.

## Release and topology

Use the official `zammad/zammad` Helm chart version **17.0.2**, with image
`ghcr.io/zammad/zammad:7.1.2-0013` and
[`zammad-values.yaml`](../../cloud_deploy/architectures/kubernetes/zammad-values.yaml).
The release name must be `zammad` in `${CLOUD_STACK_NAME}-support` so the
private `zammad-nginx` ClusterIP resolves at
`http://zammad-nginx.${CLOUD_STACK_NAME}-support.svc.cluster.local:8080`.
Do not create an Ingress, public DNS name or direct browser link. The only user
interface is RTK Cloud Admin.

The chart owns dedicated PostgreSQL, Elasticsearch, Redis and Memcached
dependencies. Attachments use Zammad's default PostgreSQL database storage;
the PostgreSQL PVC and its backup therefore contain both conversation and
attachment data. Redis runs as an ephemeral Deployment because Zammad keeps no
permanent data there. Elasticsearch keeps a PVC: an empty index after a pod
restart requires a rebuild before ticket search is complete. Inspect the
chart's current resource requests, PVC sizes and image availability against
the target LKE service limits before enabling. The values request two 10Gi
PVCs for PostgreSQL and Elasticsearch; budget at least 20Gi of Block
Storage plus backup capacity per environment. Chart upgrades require a new
render, backup compatibility check and staging restore.
The RTK provider quota planner counts these two volumes once
`SUPPORT_TICKETS_ENABLED=true`; include them manually in the bootstrap
projection while the feature is still disabled.

Before running Helm, count the account's existing Linode instances, Block
Storage volumes and NodeBalancers, then reserve two more active services for
each environment that will run Zammad. The separate Block Storage volume quota
does not establish that a new volume can be created: the first dev bootstrap on
2026-09-28 was rejected by the active-services limit on its third PVC. After
explicit cleanup of 13 retired volumes, dev had 41 active services; all three
new PVCs bound at a projected 44 under the earlier three-PVC values. Dev uses 56 as a conservative operator cap:
56 concurrent services were directly observed to succeed before the failed
57th creation. The exact Linode account ceiling is still not published by its
API. Record a provider-confirmed ceiling for staging and production before
those protected rollouts. Keep each feature disabled until both PVCs bind
and the chart becomes ready.

The dev release originally used a persistent Redis StatefulSet. Changing to
the ephemeral Deployment leaves its old PVC/PV and retained Linode volume
behind. Verify a Redis restart, ticket access and search, and confirm no pod
mounts the old claim before deleting that exact retired PVC/PV/volume. Keep
PostgreSQL and Elasticsearch volumes throughout the migration.

The storage class uses `Retain`. If bootstrap fails and the release is removed,
delete only the PVCs and PVs created by that attempt, then verify and remove
their corresponding detached Linode volumes. Removing the Helm release or PVCs
alone may leave chargeable volumes behind.

## Cloud Admin state prerequisite

Cloud Admin's local SQLite is persistent application state: it holds sessions,
audit records and per-user support-ticket read markers. The current staging
LKE Deployment leaves `/app/data/rtk-cloud-admin.db` on the container's writable
layer. Inspection of the live staging Deployment on 2026-09-28 found no
`DATABASE_PATH` override, volume or volume mount; a database file exists in
the running container. Replacing that Pod would discard those records. The
frontend image similarly declares `/data` for its SQLite databases, while the
live staging frontend Deployment has no PVC. Consequently the core recovery
inventory's SQLite PVC assumptions are not yet satisfied.

Before a staging ticket rollout or any Admin image replacement, implement and
rehearse a data-preserving storage migration for **both** SQLite owners. The
Cloud Admin target is a dedicated `ReadWriteOnce` PVC mounted at `/app/data`,
with `DATABASE_PATH=/app/data/rtk-cloud-admin.db`, one replica and a
`Recreate` strategy. Include this PVC in the provider active-services budget;
the frontend needs its own PVC at `/data`. These two PVCs are in addition to
Zammad's two: from the observed 45 active services on 2026-09-28, the full
staging storage addition projects at least 49, before other infrastructure
changes. Do not mount an empty claim over an existing database or scale the
source Pod down before a verified copy exists.

For each environment, first inventory the source Pod UID, its database files
and WAL/SHM sidecars, schema version and record counts without logging session
or customer values. Hold external writes and background writers during the
copy. Copy the complete, quiescent database set into a private migration
location and the new PVC; retain an independently recoverable encrypted copy.
Verify checksums, SQLite integrity and representative records, then switch the
single-replica Deployment to the PVC. Confirm sessions, audit history and
ticket read markers after rollout, and rehearse rollback using the retained
copy. If source Pod identity changes or any check fails, stop the migration and
investigate rather than initialize a blank database. Add both SQLite PVCs to
the matched backup configuration and exercise a staging restore before
enabling tickets. The [staging cutover procedure](staging-sqlite-migration.md)
records the observed source inventory, verification gates and rollback
requirements. The capture and PVC seed tools are documented there; a
protected-environment Go/No-Go and live migration verification are still
required. The Zammad bootstrap script does not perform the cutover.

The LKE renderer now supports an explicit storage cutover with
`LKE_CLOUD_ADMIN_SQLITE_PVC_ENABLED=true` and
`LKE_FRONTEND_SQLITE_PVC_ENABLED=true`; both default to `false`. It creates
`cloud-admin-sqlite-data` and `frontend-sqlite-data` in their respective
namespaces, using the `linode-block-storage-retain` class and 5Gi requests by
default. The storage requests can be set
with `LKE_CLOUD_ADMIN_SQLITE_STORAGE` and `LKE_FRONTEND_SQLITE_STORAGE`.
The provider capacity planner counts each enabled PVC. The renderer switches
each Deployment to `Recreate`, requires exactly one replica and uses the
observed process groups (Admin 999, frontend 101) as `fsGroup`. Validate that
the copied files are readable and writable by those groups. Once a Deployment
uses its SQLite PVC, reconciliation refuses to turn that setting off.
Protected staging and production reconciliation also refuses to replace an
existing Admin or frontend Pod while its SQLite PVC setting is still off.
The scoped `cloud-admin-image-deploy` command requires an already migrated
Admin Deployment with one replica, `Recreate`, and the expected PVC mount;
it cannot perform the storage migration itself.

For an existing Deployment, the cutover refuses to replace its Pod until its
Bound PVC carries `rtk.realtek.com/sqlite-source-pod-uid` equal to the sole
current source Pod UID and `rtk.realtek.com/sqlite-copy-sha256` equal to the
recorded 64-character hexadecimal SHA-256 of the verified migration archive.
These annotations are an operator attestation after quiescent copy, integrity
verification and independent rollback archive; merely adding them is not a
data migration. Repeat source Pod and copy verification immediately before
the protected deployment command. A fresh environment with no prior
Deployment may start with an empty PVC.

## Matched backup inventory

Before enabling support tickets in staging or production, extend that
environment's reviewed [matched backup configuration](../backup-restore.md).
The pinned chart renders the following workloads in `${CLOUD_STACK_NAME}-support`:

| Workload | Recovery role |
| --- | --- |
| `zammad-nginx`, `zammad-railsserver`, `zammad-scheduler`, `zammad-websocket`, `zammad-memcached` Deployments | `application` |
| `zammad-elasticsearch-master` StatefulSet and `zammad-redis` Deployment | `offline` |
| `zammad-postgres` StatefulSet | `data` |

Add that namespace to `namespaces`, add each workload to `workloads`, and add
these PostgreSQL components to `components` using the actual stack namespace:

```json
[
  {"id":"zammad-postgres-globals","kind":"postgres","namespace":"<stack>-support","pod":"zammad-postgres-0","container":"postgres","pvc":"data-zammad-postgres-0","user":"zammad","database":"@globals"},
  {"id":"zammad-postgres-db","kind":"postgres","namespace":"<stack>-support","pod":"zammad-postgres-0","container":"postgres","user":"zammad","database":"zammad_production"}
]
```

Add `data-zammad-elasticsearch-master-0` to `excluded_pvcs` with a reason
stating that it holds a rebuildable search index. Redis has no PVC. These
entries describe the chart as rendered with the pinned
repository values; verify names and the PostgreSQL major version against the
installed release before a backup or restore. The chart renders
`zammad-cronjob-reindex` suspended; keep it suspended during maintenance and
resolve any active or pending Helm Jobs before recovery preflight.

The environment's `preflight_checks` and `quiescence_checks` must prove that
external Admin writes and Zammad scheduler work are fenced. On verify, the
recovery engine starts `offline` Elasticsearch/Redis before `recovery_checks`
and starts Zammad `application` workloads afterward. After restoring
PostgreSQL, those recovery checks must invalidate stale Elasticsearch/Redis
data, then rebuild search using the chart's `bundle exec rake
zammad:searchindex:rebuild` task while the fence remains held. `health_checks`
must verify a representative ticket, public reply, internal note, attachment
bytes and cross-Cloud denial through Admin. Do not enable production until a
staging restore with this complete inventory and these checks has passed.

## Preparation

1. Keep `SUPPORT_TICKETS_ENABLED=false` in the target environment until all
   checks below pass. Set the feature intent and `ZAMMAD_SUPPORT_GROUP_ID` in
   the environment configuration only after provisioning the Zammad group.
2. Generate or place `zammad-postgres-password` and `zammad-redis-password`
   in that environment's SecretStore. Create `${CLOUD_STACK_NAME}-support`
   and its two Kubernetes Secrets from those canonical SecretStore files using
   the protected operator workflow. Their names and keys must match the Helm
   values file. Do not print or put the values in shell history or a tracked
   file. The normal LKE deployment also reconciles these Secrets once the
   feature is enabled; it fails if a required value is absent.
3. Install the chart with its ClusterIP only and no Ingress. During bootstrap,
   keep access within the cluster or an operator-only port forward. Once the
   feature is enabled, LKE applies policies that deny incoming traffic by
   default, permit pod traffic within the support namespace, and permit only
   Cloud Admin pods to reach `zammad-nginx:8080` across namespaces.
4. Render then install with the repository values, using the official chart
   repository URL `https://zammad.github.io/zammad-helm`:

   ```sh
   helm repo add zammad https://zammad.github.io/zammad-helm
   helm repo update zammad
   helm template zammad zammad/zammad --version 17.0.2 --namespace "$SUPPORT_NAMESPACE" -f cloud_deploy/architectures/kubernetes/zammad-values.yaml
   helm upgrade --install zammad zammad/zammad --version 17.0.2 --namespace "$SUPPORT_NAMESPACE" -f cloud_deploy/architectures/kubernetes/zammad-values.yaml --wait
   ```

   `SUPPORT_NAMESPACE` is the environment's `${CLOUD_STACK_NAME}-support`.
   Protect the rendered output from accidental disclosure if chart values are
   ever expanded to contain credentials.

5. After all chart workloads and PVCs are ready, run the repeatable bootstrap
   from the selected release checkout. For staging, complete the protected
   environment Go/No-Go first. The tool uses only that environment's kubeconfig
   and SecretStore, checks that the two PVCs are Bound and no Ingress exists,
   then creates or verifies the operator Admin, integration Agent, support group,
   custom Ticket fields and scoped API token. It restarts the Zammad applications
   only when new object fields require a migration. Existing credentials are
   verified and retained on reruns.

   ```sh
   python3 scripts/bootstrap-zammad.py --environment staging
   python3 scripts/bootstrap-zammad.py --environment staging --apply --confirm-stack video-cloud-staging
   ```

   The first command is read-only. Record the reported group ID in the target
   environment configuration before enabling Cloud Admin. Provision approved
   human Agents separately; this tool creates only the integration Agent.

## Zammad bootstrap and verification

Create the `RTK Support` group and custom Ticket object fields
`rtk_cloud_uuid` and `rtk_category` as `input` fields with `type: text` and
`maxlength: 255`. Zammad rejects custom attribute names ending in `_id`;
the Cloud field must be `rtk_cloud_uuid`. Execute object migrations through
`POST /api/v1/object_manager_attributes_execute_migrations` and restart Zammad
before creating tickets. The BFF sets the Cloud UUID at creation and never
offers a route to change it. Record the group's numeric ID as
`ZAMMAD_SUPPORT_GROUP_ID`.
The default unassigned owner ID is `1`; set
`ZAMMAD_UNASSIGNED_OWNER_ID` if this instance differs. Create approved Agent
users separately with login `rtk-<Account Manager user ID>`, Agent role and
`full` access to the `RTK Support` group.
Set Zammad's `ticket_last_contact_behaviour` to
`based_on_customer_reaction` for this dedicated instance. The default
`check_if_agent_already_replied` keeps the start of a consecutive customer
thread as `last_contact_at`, so later customer replies would not update the
Cloud list or other members' unread markers. Verify the setting by posting two
customer `web` replies in succession and checking that each advances
`last_contact_at`. In the private Zammad Rails console, use
`Setting.set('ticket_last_contact_behaviour', 'based_on_customer_reaction')`.
The BFF never creates or promotes Agents; an unprovisioned operator receives a
service-unavailable response. Customer user records may be created by the BFF.
After bootstrap, create a dedicated integration Agent with `full` access to
`RTK Support`. Issue that Agent an API token scoped to `ticket.agent`; the
pinned HTTP API probe confirmed this scope supports Customer creation/search,
ticket search/create/update, public/internal articles and attachment download.
The Agent role, group access and token scope are all required: omitting either
the role or group access returned HTTP 403 in the probe. The integration user
does not need the Admin role. Place the issued token in SecretStore as
`zammad-integration-token` and restrict it to server-side use. If the Zammad
PostgreSQL data was reset, issue a fresh token from the new instance and
replace any old SecretStore value; a nonempty file cannot authenticate against
the replacement database. Zammad requires at least one active Admin user.
Keep a separate operator-only `rtk-zammad-operator` Admin account with a random
password in that environment's `runtime/zammad-operator-admin-password`
SecretStore file (mode `0600`); it has no public UI or Ingress. Do not give the
integration Agent the Admin role. Revoke a support operator's Zammad Agent role
when their Account Manager support assignment ends; this also prevents future
assignment to that identity.

The local pinned `7.1.2-0013` HTTP API probe passed Agent-only token
authentication, Customer creation and `login:` search, two-Cloud custom-field
search, Customer `origin_by_id`, Agent `web` sender and actor preferences,
public/internal contact timestamps, consecutive Customer replies, attachment
download, assignment and state update. Search indexing depends on the running
Zammad scheduler and may lag writes briefly. Repeat the API probe in dev before
enabling the feature. Verify that custom-field search returns the right tickets,
Customer `origin_by_id` identifies the RTK customer, Agent article `preferences`
retain the verified RTK Agent ID and name, and attachment IDs resolve only under
their owning ticket/article. Do not set `origin_by_id` on Agent articles: the
pinned local model probe showed Zammad changes their sender to Customer if it
is present. Verify that Agent and Customer public `web` replies advance
`last_contact_at`, whereas an internal `note` does not. After a customer marks
a ticket seen, an internal note must not make that ticket unread or expose its
update time.
Run the Admin cross-Cloud and Viewer tests. Verify the support list and detail
through Admin using existing sessions, with Zammad's own UI unreachable from
public networks.

Before enabling staging, take a dev PostgreSQL backup containing a ticket,
public reply, internal note and attachment. Restore it in staging and verify
those records, the Cloud scope and the attachment bytes through Admin. Then
set staging's feature intent to `true` and deploy Admin through the protected
environment flow. Production needs the same restore evidence and its own
protected rollout. The dev feature can be enabled after the local API and
Admin integration checks above pass.
