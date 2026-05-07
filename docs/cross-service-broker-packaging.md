# Cross-Service Broker Packaging Decision

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-05-07.

## Decision

Private-cloud packaging treats the account/video cross-service broker as a
platform/operator-owned infrastructure component, with workspace-owned product
requirements and service-owned client configuration.

The approved default broker is NATS JetStream. An equivalent broker may be used
only when it preserves the contracts in `rtk_cloud_contracts_doc`, including
durable streams, partitioning, redelivery, dead-letter handling, and observable
publish/consume outcomes.

Ownership is split deliberately:

| Responsibility | Owner |
| --- | --- |
| Product-level broker requirement, profiles, smoke checklist, and evidence expectations | `rtk_cloud_workspace` |
| Account-side producer/consumer adapter, env config, outbox/inbox behavior | `rtk_account_manager` |
| Video-side lifecycle worker, metrics, operation store, dead-letter files | `rtk_video_cloud` |
| Broker installation, security policy, backup/restore, monitoring, capacity | platform/operator |

The workspace does not vendor or run the broker binary. It documents the product
contract and validates that deployment evidence exists.

## Why Not Service-Owned Packaging

Putting broker packaging wholly inside account manager or video cloud would make
one service own an infrastructure dependency used by both services. That creates
ambiguous upgrade, backup, security, and incident ownership.

Putting broker packaging wholly inside the workspace would turn this repository
into an operations distribution, which conflicts with the workspace boundary:
workspace owns cross-repo coordination, not service runtime implementation.

Therefore the private-cloud BOM requires an operator-provided broker profile,
while service repos own their local connection settings and smoke commands.

## Profiles

### Single-Node Evaluation

Purpose: demo, internal validation, and customer workshop bring-up.

Recommended shape:

- one local NATS server with JetStream enabled
- listener bound to localhost or private host network
- credentials suitable for evaluation only
- file-backed JetStream store under an operator-owned data directory
- one stream for account-to-video commands
- one stream for video-to-account events
- short retention appropriate for test traffic
- explicit `SKIP` in product evidence when the lifecycle channel is disabled

Minimum smoke:

```sh
nats stream ls
nats pub account.video.commands '<redacted test envelope>'
nats sub video.account.events --count 1
```

If the `nats` CLI is not installed, the operator may provide equivalent
container, API, or service-local smoke output. The evidence wrapper records a
reference through `RTK_EVIDENCE_BROKER_SMOKE_REF`.

### Production-Like Private Cloud

Purpose: customer pilot or commercial private deployment.

Required shape:

- JetStream enabled with persistent storage
- authenticated clients and TLS on non-local networks
- service-specific credentials for account manager and video cloud
- explicit stream retention and max-delivery policy
- monitoring for publish, consume, ack, redelivery, pending, and dead-letter
  metrics
- backup/restore process for stream config and state when the deployment relies
  on durable broker state
- upgrade and rollback procedure owned by the operator/platform runbook

Production-like deployments should not expose the broker outside the private
service network.

## Streams And Subjects

The current account/video lifecycle channel uses these logical streams:

| Direction | Stream | Purpose |
| --- | --- | --- |
| Account to video | `account.video.commands` | Provision and deactivate commands. |
| Video to account | `video.account.events` | Provision, deactivate, online, and metadata result events. |

Subject naming may be broker-adapter-specific, but it must preserve these
contract-level streams and message types. Partitioning for device lifecycle
messages must follow the account device identifier so redelivery and
idempotency remain stable.

## Retention And Delivery Policy

Minimum policy:

- durable streams for both directions
- ack-required consumers for lifecycle workers
- max delivery greater than one for retryable failures
- dead-letter handling after max delivery or non-retryable validation failures
- retention long enough to survive service restart and planned maintenance
- bounded storage limits with alerts before exhaustion

Production retention values are deployment-specific. They should be recorded in
the operator runbook and captured in evidence, not hardcoded in workspace docs.

## Dead-Letter Handling

Dead-letter ownership is split:

- broker-level redelivery and pending-message state belongs to the operator
- account-side outbox/inbox retry and dead-letter state belongs to
  `rtk_account_manager`
- video-side lifecycle dead-letter files and operation records belong to
  `rtk_video_cloud`

Operators should inspect broker state first, then service-local dead-letter
stores. Do not replay lifecycle commands without checking `operation_id` and
idempotency state in the owning service.

## Backup And Restore

Single-node evaluation may skip broker backup if the lifecycle channel is marked
non-production and disposable.

Production-like deployments need:

- stream configuration backup
- JetStream state backup when in-flight lifecycle messages must survive broker
  loss
- service-local database backup for account manager outbox/inbox and video cloud
  operation stores
- restore smoke showing publish, consume, ack, redelivery, and service-local
  worker processing still work after restore

Product-level evidence records broker backup references through
`RTK_EVIDENCE_NATS_BACKUP_REF`.

## Smoke And Evidence

Required product-level evidence when cross-service lifecycle is enabled:

- NATS/JetStream endpoint is configured without leaking credentials
- streams exist for `account.video.commands` and `video.account.events`
- publish/consume smoke passed or a service-local end-to-end lifecycle smoke
  passed
- account manager outbox/inbox workers are healthy or intentionally disabled
- video cloud `video_cloud-crossservice.service` or equivalent worker is healthy
- broker metrics/logs are available to the operator
- dead-letter state is empty or documented with recovery action
- backup reference exists for production-like deployments

The workspace wrapper records broker configuration presence and smoke references;
it does not perform destructive lifecycle replay against production data.

## BOM Linkage

`docs/private-cloud-deployment.md` uses this decision as the owner source for
cross-service broker packaging. Service-local runbooks should link back here for
product-level packaging assumptions and keep service-specific env variables,
commands, and troubleshooting in their own repositories.
