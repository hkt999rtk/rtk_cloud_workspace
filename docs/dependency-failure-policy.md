# Dependency Failure Policy

Status: source.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-18.

This policy defines how RTK cloud services should behave when a service cannot
reach a downstream dependency such as PostgreSQL, Account Manager, Video Cloud,
Cloud Logger, a message broker, or an optional external API.

The goal is consistent operator behavior: critical services fail clearly,
degraded features remain explicit, durable asynchronous work is retried, and
observability outages do not take down product traffic.

## Dependency Classes

| Class | Examples | Startup behavior | Runtime behavior |
| --- | --- | --- | --- |
| Startup-critical state | Primary PostgreSQL for account/auth state, required local SQLite store, required migration target | Fail fast during startup if configuration, connection, ping, or schema migration fails. | Return service unavailable only if the dependency fails after startup and no safe local path exists. Readiness should fail. |
| Optional state with local fallback | Video Cloud memory repositories when `VIDEO_CLOUD_DB_DSN` is not configured, local demo/cache paths | Start only when the optional dependency is not configured. | Use the documented fallback. Do not silently fall back if a production dependency is configured but failing. |
| Request-scoped upstream API | Admin BFF calls to Account Manager, Video Cloud, Cloud Logger query API | Do not block startup with upstream preflight checks. | Bound each call with timeouts. Return explicit `502`, `503`, or `504`, or a typed `degraded`/`unavailable` response for dashboard widgets. |
| Durable asynchronous dependency | Cross-service broker publishing/consuming, log forwarder ingest uploads | Start if required local durable state and configuration are valid. | Retry transient failures, persist progress only after acknowledgement, expose backlog/degraded state, and dead-letter exhausted work when applicable. |
| Observability dependency | Central logger backend, Loki/query backend, metrics scrape targets | Never require this for application request handling. | Keep application logs on stdout/stderr. Forwarders should spool, retry, and report degraded status. Product APIs should continue unless the request is specifically for logs/metrics. |
| Optional feature dependency | OpenAI-backed search or CloudWatch statistics source when not selected | Validate only when the feature is enabled or selected. | Disable the feature or return a feature-specific degraded response when safe. If selected for a critical flow, fail that flow explicitly. |

## Required Rules

1. A configured production dependency must not silently downgrade to demo,
   memory, or cached behavior. Fallback is allowed only when the dependency is
   absent by configuration and the deployment profile documents that mode.

2. Startup-critical dependencies must be checked before accepting traffic. The
   check should include enough proof to catch bad credentials and missing schema:
   connect or open, ping when available, and migration or schema verification
   when the service owns the schema.

3. Request-scoped upstream calls must have bounded timeouts. A timeout should
   map to `504 Gateway Timeout`. Connection failures and upstream `5xx` should
   map to `502 Bad Gateway` or `503 Service Unavailable`, depending on whether
   the caller is acting as a gateway endpoint or presenting a local degraded
   dashboard projection.

4. A service may start without an upstream API only when it can still serve a
   useful local mode. Health and status responses must label that mode as
   `demo`, `not_configured`, `degraded`, or `unavailable`; they must not report
   `ok`.

5. Asynchronous delivery must be durable when losing the work would change
   customer-visible state. Use an outbox, inbox, journal cursor, checkpoint, or
   spool. Save progress only after downstream acknowledgement.

6. Transient asynchronous failures should retry with a bounded policy. When the
   retry budget is exhausted or the error is permanent, record a dead-letter
   state with the last error and enough identifiers for requeue or diagnosis.

7. Observability delivery must not sit in the synchronous request path.
   Application services write JSON logs to stdout/stderr through
   `rtk_cloud_logger`; the forwarder owns remote delivery, spooling, and
   degraded status.

8. Every dependency failure path must emit structured logs with stable fields
   such as `component`, `dependency`, `operation`, `error_category`,
   `request_id`, `operation_id`, and the relevant upstream status. Do not log
   credentials, tokens, DSNs with passwords, private keys, or raw authorization
   headers.

9. Health endpoints should separate process liveness from dependency readiness:
   liveness answers whether the process can run; readiness answers whether it
   should receive traffic for its critical function. Dashboard service-health
   APIs may show per-upstream status without making the whole dashboard process
   unhealthy.

10. Operators must have a recovery path for durable failure modes: inspect
    backlog/dead-letter metrics, inspect the last error, and requeue after the
    downstream dependency or bad payload has been corrected.

## HTTP Mapping

| Condition | Recommended response |
| --- | --- |
| Dependency not configured and feature is required for this endpoint | `503 Service Unavailable` with `not_configured` or a clear text reason. |
| Upstream connect error, DNS error, refused connection, or upstream `5xx` while proxying a request | `502 Bad Gateway`. |
| Upstream request timed out | `504 Gateway Timeout`. |
| Optional dashboard data source is unavailable | `200 OK` with a typed `degraded`/`unavailable` widget payload, or `503` if the endpoint is only that data source. |
| Caller authentication or authorization failed at the upstream and is meaningful to the caller | Preserve `401` or `403` when safe. |
| Internal required store is unavailable after startup | `503 Service Unavailable`; readiness should fail. |

## Current Workspace Examples

- `rtk_account_manager` treats its primary PostgreSQL database as
  startup-critical. It opens and pings the database before the server starts,
  and exits when the connection fails.

- `rtk_video_cloud` can use memory repositories when `VIDEO_CLOUD_DB_DSN` is
  not configured. If the DSN is configured but opening PostgreSQL or ensuring
  schema fails, application construction returns an error instead of silently
  falling back.

- `rtk_cloud_logger` keeps application logging out of the product request path.
  Services write to stdout/stderr; the forwarder reads journald or container
  logs, sends them to the backend, spools when upload fails, and exposes
  degraded status.

- `rtk_cloud_admin` treats Account Manager, Video Cloud, and Cloud Logger query
  calls as request-scoped upstream dependencies. It starts without preflight
  blocking and maps failures to gateway errors or dashboard-level degraded
  payloads.

- `rtk_account_manager` cross-service message workers use retrying and
  dead-letter states for durable outbox/inbox processing. Broker creation and
  database connection are startup-critical for the worker process; individual
  message failures are recorded per message.

## Review Checklist

Use this checklist when adding a new dependency:

- Is this dependency required for the process to accept any meaningful traffic?
- If it is configured but unavailable, is fallback safe or would it hide a
  production outage?
- Is the dependency called synchronously from a user request? If yes, what is
  the timeout and HTTP mapping?
- Can work be lost if the dependency is unavailable? If yes, where is the
  durable queue, cursor, checkpoint, or spool?
- How does readiness report the dependency state?
- How does an operator find backlog, dead letters, last error, and requeue
  instructions?
- Which structured log fields identify the dependency, operation, request, and
  failure class?
- Which secrets or high-cardinality values must be redacted?
