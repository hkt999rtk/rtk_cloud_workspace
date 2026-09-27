# Brand Cloud Support Tickets: Design and Delivery Plan

Status: draft target; no ticket runtime is deployed.

Owner: rtk_cloud_workspace.

Last reviewed: 2026-09-28.

Classification: supporting-note.

Applies to: Brand Cloud developer/admin support in RTK Cloud Admin. The shared
HTTP and authorization contract belongs to
[`support_tickets.md`](../../repos/rtk_cloud_contracts_doc/support_tickets.md).

## Decision

RTK Cloud Admin will provide the only customer and agent ticket interface.
Zammad stores tickets, articles and attachments, while Account Manager remains
the authority for users, Brand Cloud membership and platform permissions.
Admin's Go BFF validates the existing account-backed session and current scope
before using a server-only Zammad API token. Neither the `rtk_admin_session`
cookie nor an Account Manager access token is sent to Zammad. No public Zammad
Ingress or customer-facing Zammad login is created.

This feature is for technical and service support, including incidents,
integration problems and usage questions. Billing, payment and contract
discussion remain outside the shared Cloud ticket area. Evaluation and
commercial Clouds may both create tickets. Evaluation tickets do not carry a
response-time promise; commercial SLA terms remain contract-defined. Update
[`business-model.md`](../business-model.md) before release so public claims
match this policy.

## Responsibility and data map

| Component | Responsibility | Persistent facts |
| --- | --- | --- |
| Account Manager | Human identity, live Brand Cloud membership, Viewer ceiling and support-team capabilities | Users, roles and memberships |
| Cloud Admin BFF | Explicit-cloud authorization, Zammad adapter, customer-safe projection, notification read markers and audit | Console-local read markers and audit only |
| Zammad | Ticket, state, support group/owner, article, attachment and searchable `rtk_cloud_uuid` | Ticket conversation and files |
| RTK deployment | Private Zammad release, resources, credentials, backup and restore | Deployment intent and encrypted recovery set |

A Zammad ticket has a BFF-immutable custom `rtk_cloud_uuid` equal to the Account
Manager Brand Cloud UUID. It is the tenant discriminator even when one human
belongs to several Clouds. One Zammad customer record maps to one RTK user ID;
Zammad Organization is not used as the authorization boundary. The BFF always
injects the Cloud ID on creation, filters lists by it, and compares it again
after loading any single ticket or attachment. Search results alone do not
authorize reads. A removed member immediately loses BFF access; existing
tickets remain retained for support and recovery.
The `rtk_category` custom text field stores `incident`, `integration` or
`usage`; both object fields must exist before the feature is enabled.

## Access and workflow

| Actor | Read | Create/public reply | Agent reply/internal note | Assignment/state |
| --- | --- | --- | --- | --- |
| Current Cloud owner/admin/member | Every ticket in that Cloud | Yes | No | No |
| Current Cloud Viewer | Every ticket in that Cloud | No | No | No |
| Support operator | Support group queue, including unassigned and teammates' tickets | No customer action | Yes | Claim self and update state |
| Platform admin | Support group queue | No customer action | Yes | Assign or reassign any agent; update state |

Cloud Admin derives customer-side `ticket.read` and `ticket.write` from the
current Account Manager Brand Cloud membership: every role reads, while Viewer
does not write. Account Manager projects platform-scoped `ticket.support.read`, `ticket.support.reply`,
`ticket.support.note`, `ticket.support.assign` and
`ticket.support.reassign`. The BFF checks live membership and the
relevant permission on every request; the UI mirrors those decisions but is
not the enforcement point. Product-scoped Viewer access does not narrow this
Cloud-wide ticket surface; sensitive billing content is excluded by scope.

New customer tickets enter one `RTK Support` group unassigned. Any support
operator can read the group queue and claim a ticket; a platform admin can
reassign it. The owner is the primary handler, not the visibility boundary.
Use Zammad's existing states and priorities; the Admin UI presents plain
labels. A public customer reply to a closed ticket reopens it. Internal notes
are Zammad `note` articles with `internal=true` and are never included in
customer projections. Public messages use Zammad `web` communication articles
with `internal=false`, so Zammad advances `last_contact_at`; `note` articles do
not advance it. The BFF does not use email article type. Customer unread and
visible update time follow public contact, while the support queue tracks all
ticket updates. Zammad's API distinguishes `internal` from email delivery, so
API writes must suppress Zammad notifications as well as omit email channels.

The BFF may create Zammad Customer records for RTK users before customer
writes. Approved Agent records are provisioned separately with the Agent role
and `rtk-<Account Manager user ID>` login. Approved Agents and the integration
account also need `full` permission on the `RTK Support` group. The BFF never
creates or promotes Agent accounts. It records the RTK actor in the resulting
article/audit entry. The pinned Zammad 7.1.2-0013 container accepted
`rtk_cloud_uuid`, Cloud-filtered search and `origin_by_id`; it rejected the
earlier `rtk_cloud_id` object name because custom names cannot end in `_id`.
Search indexing depends on the running scheduler and is eventually consistent.

## UI and HTTP flow

Customer Support lives under `/console/clouds/{cloudId}/support`; the Platform
support queue lives under `/admin/support`. Both reuse the existing Admin shell,
spacing, typography and responsive patterns. Customer pages have an all-ticket
list with status/search filters, new-ticket form, and full-page conversation
with public replies and bounded attachments. The agent page adds Unassigned,
Mine and Team filters, owner/state controls, and an explicit Public Reply versus
Internal Note composer. Ticket number, last update, current state and assignee
are visible; the internal note control never appears in customer views.

The browser calls only same-origin BFF routes. Customer routes are under
`/api/developer/brand-clouds/{cloudId}/support/tickets`; agent routes are under
`/api/admin/support/tickets`. The canonical methods, payloads and error model
are specified in the contract. The BFF treats Zammad IDs as opaque, sanitizes
returned rich text, bounds attachment size and type, and
streams downloads only after scope checks. Browser caches are keyed by the
explicit Cloud ID and discarded on Cloud switch.

V1 notifications stay in Admin: list rows show unread public
replies or new agent assignments, using per-user read markers in Admin's
console-local store and polling while a support view is open. The BFF records a
marker when the user opens a ticket. No email or separate notification service
is introduced. A read-marker failure must not block the ticket operation; it
may temporarily leave an item marked unread.

## Deployment and recovery

Use pinned, verified Docker images through the official Zammad Helm chart in a
dedicated `${CLOUD_STACK_NAME}-support` namespace on LKE. Expose the Zammad
nginx Service only through a ClusterIP reachable by Cloud Admin; deny other
application namespaces with NetworkPolicy. Provide dedicated PostgreSQL,
Elasticsearch, Redis and Memcached chart dependencies and a persistent Zammad
PostgreSQL database attachment store. Do not reuse RTK core PostgreSQL or its credentials. Capacity and
volume sizing must pass the deployment planner and account service-limit check.

Add a default-off `SUPPORT_TICKETS_ENABLED` environment intent, pinned chart
values and a derived internal `ZAMMAD_BASE_URL` for Admin. Add a Zammad integration
token and dependency credentials to the per-environment SecretStore catalog and
runtime injection; keep values out of tracked files and logs. The Admin BFF
fails closed when enabled but credentials or the private service are missing.
Only the existing Admin hostname appears in user links.

Extend the matched maintenance backup inventory with Zammad PostgreSQL before
enabling production. PostgreSQL also contains attachments. Elasticsearch is rebuilt from
authoritative records after restore; cache data is not a ticket source of
truth. Verify a representative ticket, article, attachment and Cloud boundary
after a staging restore. Chart upgrades require a matching backup and restore
review because dependency major versions and data layouts can change.

## Delivery gates

1. Refresh workspace and recursive submodules to freshly fetched main; resolve
   incomplete checkouts and validate gitlinks before modifying source.
2. Complete this design, the canonical contract, Admin UI spec and policy
   update in docs-first commits. Run `docs-check` and `contracts-check`.
3. Implement Account Manager permissions, Admin BFF and UI, and deployment
   support with the feature disabled; prove pinned Zammad API mapping in an
   isolated container before enabling it.
4. Test cross-Cloud denial, Viewer writes, member revocation, internal-note
   redaction, article authorship, attachments, assignment, notifications,
   Zammad outage and restore. Run focused tests and the workspace pre-PR gate.
5. Roll out disabled by default; qualify dev, then staging including backup
   restore, then production through the existing protected-environment gate.

The repository contains a default-off Admin implementation, Agent permissions,
pinned Helm values and a deployment runbook. Local pinned-version API
compatibility has been exercised with two Cloud UUIDs, articles, attachment and
assignment. Zammad remains undeployed; environment capacity and staging restore
qualification are still release gates.
