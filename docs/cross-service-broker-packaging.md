# Cross-Service Broker Packaging

Status: retired.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-07.

## Decision

The workspace no longer defines a current cross-service broker package for the
account/video lifecycle path. NATS JetStream is removed from the supported cloud
runtime, deployment provisioning, and readiness expectations.

Account/video coordination should use explicit service APIs. If a future
workflow needs asynchronous retry across service boundaries, the producing
service should own a database-backed outbox with retry/lease state and call an
explicit receiver API owned by the consuming service.

## Current Runtime Position

- no workspace-provisioned broker is required for account/video lifecycle
  coordination
- no NATS/JetStream endpoint, stream smoke, backup evidence, or broker metrics
  are required for private-cloud readiness
- Account Manager keeps `CROSS_SERVICE_BROKER=log` as the disabled/default local
  behavior and may keep separate non-local adapters such as Azure Event Hubs
  where explicitly configured
- Video Cloud does not package or start a cross-service gateway service

## Reintroduction Bar

A shared event bus should only be reintroduced when there is a real
multi-consumer eventing requirement that cannot be met by direct APIs plus
DB-backed outbox/retry. Any future proposal must define ownership, persistence,
consumer groups, replay, security, operations, evidence, and migration before
adding a broker dependency back to provision or deployment.
