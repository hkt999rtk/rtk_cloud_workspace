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
  -> rtk_cloud_logger ingest API or Loki-compatible ingest adapter
  -> Loki on the dedicated logs VM
  -> Cloud Admin BFF/UI through Loki query APIs
```

Applications must not synchronously push logs to the logger backend. They write
structured JSON logs to stdout/stderr and continue to run when the logger backend
or forwarder is degraded. Journald is the local durable buffer. The forwarder is
responsible for batching, retry, cursor persistence, bounded local spool, and
deduplication metadata.

Private-cloud v1 requires Loki as the centralized log storage/query backend.
Grafana is optional and is not the v1 dashboard dependency. The operator log
dashboard is owned by Cloud Admin, which should query Loki through a backend
service or workspace/logger query adapter. Loki should stay private to the
deployment network unless an explicit authenticated proxy is added.

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
2. Provision the logs VM and Loki-backed logger backend.
3. Generate logger ingest/query endpoints and forwarder credentials.
4. Install and enable the forwarder on each service host.
5. Deploy Account Manager, Video Cloud, Cloud Admin, and Frontend services.
6. Run readiness checks that verify Loki/backend health, forwarder status, and
   one sample trace query.

If the logger backend is unavailable, readiness should report `logging:
degraded` while keeping service health checks independent.

## Current Staging Implementation Status

The staging implementation now includes native logger resource provisioning,
Loki-backed backend deployment, per-host forwarder installation, readiness
checks, logger artifact redaction, cleanup coverage, and Cloud Admin dashboard
wiring. `CLOUD_LOGGER_SCRIPT` remains available only as an override/debug hook.

Current status:

| Area | Status | Notes |
| --- | --- | --- |
| Logger VM/firewall/env/state/DNS | Implemented in workspace provisioning | `./stg.sh provision --plan` reports logger resource status and `./stg.sh provision --all` creates the logger resource metadata. |
| Loki-backed store | Implemented in `rtk_cloud_logger` | `rtk-cloud-logger` supports `-store loki` / `RTK_CLOUD_LOGGER_STORE=loki`. |
| Logger backend/Loki service install | Implemented in workspace deploy | When `CLOUD_LOGGER_SCRIPT` is unset, deploy installs Loki plus `rtk-cloud-logger` systemd services on the logger VM. |
| Per-host forwarders | Implemented in workspace deploy | `./stg.sh deploy` installs `rtk-cloud-log-forwarder` when logger env/state is available. |
| Readiness checks | Implemented in workspace deploy | Backend health, ingest/idempotency, sample query, and forwarder status are reported as PASS/DEGRADED. |
| Artifacts and cleanup | Implemented in workspace provisioning/cleanup | Logger inventory and redacted logger env/state evidence are included; cleanup includes logger resources. |
| Cloud Admin dashboard | Implemented in `rtk_cloud_admin` submodule pointer | Cloud Admin owns the v1 UI; Grafana remains optional. |

## Repository Responsibilities

| Repository | Responsibility |
| --- | --- |
| `rtk_cloud_logger` | Zap SDK, HTTP middleware, logger schema, forwarder, ingest API, storage/query backend, operator runbook. |
| `rtk_cloud_contracts_doc` | Shared service logging contract and correlation field rules. |
| `rtk_video_cloud` | Migrate service and worker logs from `slog` to `rtk_cloud_logger` zap and propagate workflow ids. |
| `rtk_account_manager` | Replace stdlib/Gin logging with zap for API, migrations, workers, and cleanup timer. |
| `rtk_cloud_admin` | Add zap request logging, upstream correlation for Account Manager and Video Cloud calls, and the v1 operator log dashboard backed by Loki query APIs. |
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
- Cloud Admin can show those results without Grafana by calling a Loki-backed
  query endpoint.
- Staging readiness reports logger backend health, per-host forwarder health,
  and a sample query result.
- Stopping the logger backend does not stop account, video, admin, or frontend
  services.
