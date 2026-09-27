# Private Zammad service for RTK Cloud Admin

Status: implementation prepared; pinned local API probe passed; no environment is enabled or deployed.

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
attachment data. The Elasticsearch index and caches are rebuildable. Inspect
the chart's current resource requests, PVC sizes and image availability against
the target LKE service limits before enabling. The values request three 10Gi
PVCs for PostgreSQL, Elasticsearch and Redis; budget at least 30Gi of Block
Storage plus backup capacity per environment. Chart upgrades require a new
render, backup compatibility check and staging restore.
The RTK provider quota planner counts these three volumes once
`SUPPORT_TICKETS_ENABLED=true`; include them manually in the bootstrap
projection while the feature is still disabled.

Before running Helm, count the account's existing Linode instances, Block
Storage volumes and NodeBalancers, then reserve three more active services for
each environment that will run Zammad. Confirm the account's **active services**
ceiling with Linode Support and update `LKE_ACTIVE_SERVICE_LIMIT` in the
environment configuration after an increase is approved. The separate Block
Storage volume quota does not establish that a new volume can be created: the
dev bootstrap on 2026-09-28 was rejected by the active-services limit on its
third PVC even though the volume-specific quota had room. Keep the feature
disabled until all three PVCs bind and the chart becomes ready.

The storage class uses `Retain`. If bootstrap fails and the release is removed,
delete only the PVCs and PVs created by that attempt, then verify and remove
their corresponding detached Linode volumes. Removing the Helm release or PVCs
alone may leave chargeable volumes behind.

## Matched backup inventory

Before enabling support tickets in staging or production, extend that
environment's reviewed [matched backup configuration](../backup-restore.md).
The pinned chart renders the following workloads in `${CLOUD_STACK_NAME}-support`:

| Workload | Recovery role |
| --- | --- |
| `zammad-nginx`, `zammad-railsserver`, `zammad-scheduler`, `zammad-websocket`, `zammad-memcached` Deployments | `application` |
| `zammad-elasticsearch-master`, `zammad-redis` StatefulSets | `offline` |
| `zammad-postgres` StatefulSet | `data` |

Add that namespace to `namespaces`, add each workload to `workloads`, and add
these PostgreSQL components to `components` using the actual stack namespace:

```json
[
  {"id":"zammad-postgres-globals","kind":"postgres","namespace":"<stack>-support","pod":"zammad-postgres-0","container":"postgres","pvc":"data-zammad-postgres-0","user":"zammad","database":"@globals"},
  {"id":"zammad-postgres-db","kind":"postgres","namespace":"<stack>-support","pod":"zammad-postgres-0","container":"postgres","user":"zammad","database":"zammad_production"}
]
```

Add `data-zammad-elasticsearch-master-0` and `data-zammad-redis-0` to
`excluded_pvcs`, each with a reason stating that it holds rebuildable search
or cache data. These entries describe the chart as rendered with the pinned
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
the replacement database. Revoke a support operator's Zammad Agent role when
their Account Manager support
assignment ends; this also prevents future assignment to that identity.

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

Take a PostgreSQL backup containing a ticket, public reply, internal note and
attachment. Restore it in staging and verify those records, the Cloud scope and
the attachment bytes through Admin. Only then set the environment feature
intent to `true` and deploy Admin through the protected environment flow.
No production rollout is implied by this document.
