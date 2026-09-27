# Private Zammad service for RTK Cloud Admin

Status: implementation prepared; no environment is enabled or deployed.

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
the target LKE service limits before enabling. Chart upgrades require a new
render, backup compatibility check and staging restore.

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

Create the `RTK Support` group and text ticket object fields `rtk_cloud_id`
and `rtk_category`. The BFF sets the Cloud ID at creation and never offers a
route to change it. Record the group's numeric ID as `ZAMMAD_SUPPORT_GROUP_ID`.
The default unassigned owner ID is `1`; set
`ZAMMAD_UNASSIGNED_OWNER_ID` if this instance differs. Create approved Agent
users separately with login `rtk-<Account Manager user ID>` and Agent role.
The BFF never creates or promotes Agents; an unprovisioned operator receives a
service-unavailable response. Customer user records may be created by the BFF.
After bootstrap, create an integration API token with permissions for ticket,
article, attachment and customer-user operations, place it in SecretStore as
`zammad-integration-token`, and restrict it to server-side use. Revoke a
support operator's Zammad Agent role when their Account Manager support
assignment ends; this also prevents future assignment to that identity.

Before enabling the feature, exercise the pinned Zammad API in dev with two
Cloud UUIDs. Verify that custom-field search returns the right tickets,
`origin_by_id` attributes public and internal articles to the intended RTK
actor, and attachment IDs resolve only under their owning ticket/article.
Run the Admin cross-Cloud and Viewer tests. Verify the support list and detail
through Admin using existing sessions, with Zammad's own UI unreachable from
public networks.

Take a PostgreSQL backup containing a ticket, public reply, internal note and
attachment. Restore it in staging and verify those records, the Cloud scope and
the attachment bytes through Admin. Only then set the environment feature
intent to `true` and deploy Admin through the protected environment flow.
No production rollout is implied by this document.
