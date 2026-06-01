# RTK Cloud Service Logging Architecture

Status: implementation handoff.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-01.

## Purpose

This document defines the cross-repository service logging target for RTK Cloud
private-cloud deployments. It coordinates the service repositories, the shared
`rtk_cloud_logger` module, the shared contracts repository, and the Linode
provisioning scripts.

The goal is traceable cloud operations without coupling application request
paths to a remote logging backend.

## Target Architecture

```text
Go services and workers
  -> zap JSON logs on stdout/stderr
  -> systemd journald on each VM
  -> rtk-cloud-log-forwarder systemd service
  -> rtk_cloud_logger ingest API
  -> logger storage/query backend
```

Applications must not synchronously push logs to the logger backend. They write
structured JSON logs to stdout/stderr and continue to run when the logger backend
or forwarder is degraded. Journald is the local durable buffer. The forwarder is
responsible for batching, retry, cursor persistence, bounded local spool, and
deduplication metadata.

## Log Taxonomy

| Type | Owner | Storage | Notes |
| --- | --- | --- | --- |
| Service logs | Each service, schema owned by `rtk_cloud_logger` | Logger backend | Zap JSON events emitted by servers, workers, migrations, and jobs. |
| Audit events | Product service that owns the action | Service database or audit store | User-visible or compliance-sensitive events; do not replace with debug logs. |
| Device runtime logs | `rtk_video_cloud` runtime log ingestion contract | Runtime log store | Device-originated diagnostic records, not cloud service logs. |
| Metrics | Service Prometheus endpoints | Prometheus-compatible store | Numeric counters/gauges/histograms, not log search. |
| Traces | Future tracing owner | Trace backend when adopted | `trace_id` must be loggable now even before full tracing exists. |

## Required Fields

Every cloud service log should include:

- `ts`
- `level`
- `msg`
- `service`
- `env`
- `version`
- `host`
- `unit`
- `component` when the process has multiple internal components

Correlation fields must be propagated whenever available:

- `trace_id`: end-to-end request or workflow trace id.
- `request_id`: inbound HTTP request id, usually from `X-Request-Id`.
- `operation_id`: idempotent business operation id.
- `device_id`: device identifier in the log body only.
- `org_id`: organization identifier in the log body only.
- `user_id`: user identifier in the log body only.

High-cardinality values must stay in the log body. They must not become default
labels or partition keys unless the logger backend explicitly supports that
query pattern.

## Delivery And Deduplication

Delivery is at-least-once. Duplicate delivery can happen when the forwarder
successfully writes a batch to the backend and crashes before saving its journal
cursor. The backend must make ingestion idempotent by `event_id`.

The forwarder should generate a stable `event_id` from values that are stable
across retry:

```text
sha256(host_id + boot_id + systemd_unit + journal_cursor)
```

If a source does not have a journal cursor, the source adapter must document its
own stable id material before enabling retries.

## Forwarder Requirements

The Go forwarder lives in `rtk_cloud_logger` and runs as a root-owned systemd
service on every service VM.

Required behavior:

- read selected systemd journal units and known file/container sources
- preserve cursor state under `/var/lib/rtk-cloud-logger/`
- use bounded local spool for unsent batches
- retry with backoff and jitter
- expose local health/status output for readiness reports
- send batches with an auth token or mTLS identity
- never delete journald data directly
- never block application service startup

Journald retention is controlled by systemd settings such as `SystemMaxUse`,
`SystemKeepFree`, and `MaxRetentionSec`. The forwarder only advances its cursor
after backend acknowledgement.

## Non-Go Sources

The forwarder must also collect deployment-owned sources that are not emitted by
the Go zap SDK:

- nginx access/error logs
- EMQX broker logs
- NATS logs
- coturn logs
- PostgreSQL logs when locally managed
- Redis logs when locally managed

Each source must include low-cardinality source labels such as `host`, `unit`,
`source`, and `service`. Raw payload parsing can be minimal in the first phase,
but secrets must still be redacted before upload when a parser handles the
payload.

## Security Rules

Logs must not contain authorization headers, bearer tokens, refresh tokens,
cookies, passwords, database DSNs with credentials, OIDC client secrets, TURN
shared secrets, Linode credentials, Object Storage credentials, SMTP
credentials, private keys, or certificate private material.

When a sensitive value is needed for correlation, log a stable hash or redacted
marker instead of the raw value.

## Provisioning Model

Provisioning must create logging dependencies before starting application
traffic, but application services remain tolerant of logging degradation.

Required order:

1. Create network, VPC, DNS, and base secrets.
2. Provision the logger backend VM or service.
3. Generate logger endpoint and forwarder credentials.
4. Install and enable the forwarder on each service host.
5. Deploy Account Manager, Video Cloud, Cloud Admin, and Frontend services.
6. Run readiness checks that verify backend health, forwarder status, and one
   sample trace query.

If the logger backend is unavailable, readiness should report `logging:
degraded` while keeping service health checks independent.

## Repository Responsibilities

| Repository | Responsibility |
| --- | --- |
| `rtk_cloud_logger` | Zap SDK, HTTP middleware, logger schema, forwarder, ingest API, storage/query backend, operator runbook. |
| `rtk_cloud_contracts_doc` | Shared service logging contract and correlation field rules. |
| `rtk_video_cloud` | Migrate service and worker logs from `slog` to `rtk_cloud_logger` zap and propagate workflow ids. |
| `rtk_account_manager` | Replace stdlib/Gin logging with zap for API, migrations, workers, and cleanup timer. |
| `rtk_cloud_admin` | Add zap request logging and upstream correlation for Account Manager and Video Cloud calls. |
| `rtk_cloud_frontend` | Add zap logs for Go web/search processes and define labels for web/runtime logs. |
| `rtk_cloud_client` | Document SDK correlation propagation and the boundary between device runtime logs and cloud service logs. |
| `rtk_cloud_workspace` | Provision logger role, forwarder install, env/state files, readiness evidence, and cross-repo issue tracking. |

## Acceptance Criteria

- Each Go server process emits single-line JSON zap logs to stdout/stderr.
- `journalctl -u <unit>` shows parseable JSON for Go services and clear source
  labels for non-Go services.
- Forwarder retries do not create duplicate backend records for the same
  journal event.
- A support engineer can query by `trace_id`, `operation_id`, `request_id`,
  `device_id`, service, unit, host, and time range.
- Staging readiness reports logger backend health, per-host forwarder health,
  and a sample query result.
- Stopping the logger backend does not stop account, video, admin, or frontend
  services.
