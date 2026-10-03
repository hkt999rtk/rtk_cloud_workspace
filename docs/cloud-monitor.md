# RTK Cloud monitoring and PDF reports

Status: active implementation guide. Owner: workspace operations. Applies to
`scripts/go/cloud-monitor`; availability claims require dated environment evidence.

## Ownership and scope

The monitor belongs in this workspace because it checks several service
repositories, shared Kubernetes workloads, external edge/TURN, and environment
PKI. It is an independent Go binary with configuration and history per environment.
Extract it into a separate repository only when another workspace needs the same
tool. It does not import service `internal` packages.

`rtk-cloud monitor-inventory` is a read-only export of expected deployment intent.
It uses the maintained environment resolver and DNS rules. It does not run
`deployment plan`, materialize runtime files, read SecretStore values, or contact
the cloud. Missing optional rollout intent remains UNKNOWN; an explicitly disabled
target remains visible as NOT_APPLICABLE. The inventory includes workers,
PostgreSQL, Redis, Fleet Valkey, MQTT, PKI, observability, ingress, and external
edge/TURN. Discovery of an extra workload produces a coverage warning.

Normal probes read existing state. Explicit `--enable-synthetic` enables only
pre-provisioned dedicated authentication, MQTT, and TURN identities. There is no
signup, device enrollment, load test, deployment repair, storage expansion,
certificate renewal, or persistent installation.

## Build and first run

Requires Go 1.25+, `kubectl`, a reachable selected environment, and Chromium for
PDF output. Linux and macOS are supported (writer locking uses Unix `flock`).
The HTML report is self-contained; Chinese fonts must be installed locally, such
as Noto Sans CJK TC. The PDF includes the fonts Chromium uses.

Complete certificate discovery uses an optional external `certificate-tools`
executable, whose implementation is supplied separately. The build script also
builds it when `scripts/go/certificate-tools` is present. To enable discovery, set
`certificate_tool` to the installed executable path in the monitor configuration;
the example leaves it unset. Without it, complete certificate discovery remains
UNKNOWN. Configured native TLS, CA, and CRL checks still run.

From the workspace root:

```sh
scripts/build-cloud-monitor.sh
bin/rtk-cloud monitor-inventory --environment dev --workspace "$PWD" --json \
  > scripts/cloud-monitor/inventory.dev.json
cp scripts/cloud-monitor/config.example.json scripts/cloud-monitor/dev.local.json
# Edit dev.local.json: actual Chromium path and the sources described below.
bin/cloud-monitor validate --environment dev --config scripts/cloud-monitor/dev.local.json
bin/cloud-monitor check --environment dev --workspace "$PWD" \
  --config scripts/cloud-monitor/dev.local.json --out-dir output/cloud-monitor
```

Generate each environment's inventory separately after deployment intent changes.
Do not copy Dev inventory into Staging or Prod. Configuration and inventory
environment/stack must match. Ambient deployment overrides are rejected by
`monitor-inventory` so it cannot accidentally export a different deployment.
The report records the desired configuration fingerprint.

The minimal example is deliberately incomplete: missing token, database,
active identity, semantic readiness, and metric sources produce UNKNOWN. Add
configuration for each required dependency before treating coverage as complete.
Kubernetes marks an external target as `NOT_APPLICABLE` only for Kubernetes
liveness; it is not health evidence. For each required external non-TURN target
(including `public-edge`), configure a required semantic HTTP check with the
same `service`, `liveness_only: false`, and a response `contains` marker. For
each generated external TURN domain, configure a dedicated TURN probe whose
`address` host matches that inventory target. Missing, optional, or mismatched
evidence remains UNKNOWN, and the configured probe's actual result determines
the overall status.

```sh
bin/cloud-monitor watch --environment dev --workspace "$PWD" \
  --config scripts/cloud-monitor/dev.local.json --out-dir output/cloud-monitor \
  --enable-synthetic
# Temporary acceptance run:
bin/cloud-monitor watch --environment dev --workspace "$PWD" \
  --config scripts/cloud-monitor/dev.local.json --out-dir output/cloud-monitor \
  --duration 30m --enable-synthetic
bin/cloud-monitor report --environment dev --config scripts/cloud-monitor/dev.local.json \
  --out-dir output/cloud-monitor --from 2026-10-02T00:00:00+08:00 \
  --to 2026-10-03T00:00:00+08:00
```

`check` stores a snapshot and creates HTML/PDF. `--skip-report` stores only the
snapshot. Exit codes: 0 = PASS (or successful watch/report/validate); 1 = health
requires attention; 2 = invalid configuration or execution/storage error;
3 = check completed but PDF rendering failed. Watch does not exit for a service
failure or PDF failure; it exits for unrecoverable local storage/configuration
errors. SIGINT/SIGTERM release locks and stop pending probes.

## Secret layout and permissions

`--config-root` is the SecretStore **base** (default `$RTK_CLOUD_CONFIG_ROOT` or
`~/.config/rtk_cloud`). Files are resolved below `<base>/<environment>`. Kubeconfig
is `<base>/<environment>/kube/kubeconfig.yaml`.

JSON configuration contains references only. Token/password/key/DSN files must be
regular files with no group/other permissions, normally 0600. Escaping symlinks
and references into another environment are refused. Public CA/certificate/CRL
files may be 0644. Raw secrets, private keys, response bodies, and database error
strings are excluded from reports. Synthetic rotated tokens stay under
`<base>/<environment>/monitor/state`, separate from report history.

## Configuration of checks

See the Go `Config` and check structures in
[`types.go`](../scripts/go/internal/cloudmonitor/types.go) for the complete schema.
JSON rejects unknown fields and trailing data. Important examples:

```json
{
  "http": [{
    "name": "authorized-readiness", "service": "video-cloud",
    "url": "https://video-cloud-dev.realtekconnect.com/your-approved-read-endpoint",
    "success_codes": [200], "contains": "expected-response-marker",
    "token_file": "monitor/video-access.token", "required": true,
    "max_latency_ms": 500
  }],
  "tokens": [{
    "name": "account-manager-access", "service": "account-manager", "kind": "jwt",
    "token_file": "monitor/am-access.token", "key_file": "monitor/am-verification.pem",
    "algorithm": "HS256", "validation_url": "https://account-manager.video-cloud-dev.realtekconnect.com/v1/me",
    "required": true, "warn_seconds": 300, "critical_seconds": 60
  }],
  "postgres": [{
    "name": "postgres", "dsn_file": "monitor/postgres-readonly.dsn", "required": true,
    "volume": {"namespace": "video-cloud-dev-platform", "pod": "postgresql-0",
      "container": "postgres", "mount": "/var/lib/postgresql/data"}
  }],
  "redis": [{
    "name": "redis", "role": "shadow", "address": "127.0.0.1:16379", "required": true,
    "volume": {"namespace": "video-cloud-dev-platform", "pod": "actual-redis-pod",
      "container": "redis", "mount": "/data"}
  }],
  "metrics": [{
    "name": "redis_commands_per_second", "service": "redis", "required": true,
    "url": "http://127.0.0.1:19090", "query": "rate(redis_commands_processed_total{job=\"redis\"}[2m])",
    "unit": "commands/s", "max_age_seconds": 120
  }]
}
```

These are fragments to merge into the example, not a complete runnable file.
Use the actual signing method and least-privilege verification material. An
online JWT validation endpoint may be used without copying a signing secret.
`subject_claim` defaults to `sub`; use `subject_id` for Video Cloud's current JWT
contract when checking a specific identity.
In that mode decoded expiry/scope is trusted only after the configured service
accepts the token. Opaque tokens require online validation and authoritative
metadata for expiry; missing expiry remains UNKNOWN. A fixed bearer credential
is never assumed to last forever. Avoid configuring a mutating validation URL.

Metadata for managed renewal uses `last_succeeded_at` and `last_failed_at`.
Token metadata also supports `expires_at`, `renew_at`, `issuer_verified`, and a
`token_sha256` binding to the exact opaque token; unverified metadata is UNKNOWN.
certificate metadata supplies `installed_fingerprint`. A managed short certificate cannot
be judged solely against a generic 30-day window. Configure `role: "managed"`,
renewal metadata, loaded `expected_fingerprint`, CA bundle, and applicable CRLs.
TLS checks support an address/server name or a public certificate file; client
mTLS uses `client_cert_file` and `client_key_file`, with separate
`client_ca_file` / `client_crl_files` when its issuer differs from the server.
Use `application_url`, `application_contains`, and optional
`application_token_file` for a permitted read endpoint to prove mTLS acceptance:
the endpoint must accept the dedicated client and reject the same request without
its certificate. A TLS handshake alone leaves acceptance UNKNOWN.
A stored version is reported
as reference evidence, separate from live/loaded evidence.

### PostgreSQL and Redis

Use a PostgreSQL monitoring account with SELECT/statistics privileges only.
Every SQL statistics query uses a read-only transaction with a statement timeout;
one connection is used per configured database. The tool observes connections,
activity/locks/long transactions, transactions and row counters, temporary data,
indexes, WAL and replication statistics. Insufficient rights or unsupported
statistics are explicit gaps. A DSN file can reference a temporary local
port-forward; it must not be embedded in configuration or command arguments.

Disk capacity uses `df -Pk` for a configured actual mount, via `kubectl exec`.
Database size and requested PVC size are not substitutes for used filesystem
bytes. Thresholds: 70% warning, 80% expansion planning, 90% urgent failure,
95% critical failure. A changed Pod name requires updating the volume target.
RBAC must permit reading selected workloads/pods/PVCs and the specific diagnostic
exec operations. Do not grant mutation privileges to the monitor account.

Configure Redis separately for `cache`, `shadow`, and `fleet`. Durable Shadow and
Fleet must use AOF, everysec fsync, and noeviction. The tool reads PING, INFO, and
three CONFIG GET keys. These need a suitable read-only Redis ACL. Capacity includes
used/maxmemory, RSS, AOF volume, fragmentation, command/network/error counters,
and persistence state. RSS plus a container limit does not prove container total
memory; provide actual working-set/limit metrics. A cache can omit AOF volume.

Memory at 70% warns. At 85%, history establishes whether the condition lasted
five minutes; new rejected writes/OOM errors require immediate attention.
Counter restarts and gaps are excluded from rate calculations.

### PostgreSQL backup evidence

Add one read-only check for each configured daily PostgreSQL backup source:

```json
{
  "postgres_backups": [{
    "name": "platform-postgres",
    "required": true,
    "enabled": true,
    "namespace": "video-cloud-dev-platform",
    "cluster_id": "REPLACE_WITH_REVIEWED_CLUSTER_ID",
    "status_config_map": "rtk-postgres-backup-status",
    "warn_age_hours": 26,
    "fail_age_hours": 28
  }]
}
```

Use the selected environment's exact namespace and worker `cluster_id`. Grant
only `get` on the named ConfigMap for this probe. The monitor reads `data[status.json]`
and validates its version, environment, stack, cluster identity and timestamps.
It does not read Object Storage credentials, start a Job, mount database volumes
or issue synthetic writes. Check mode and the one-minute watch service category
collect the same evidence. Missing configuration for an expected PostgreSQL
service leaves a coverage gap.

Freshness comes from `latest.manifest.finished_at`, the last committed backup's
capture timestamp, with warning after 26 hours and failure after 28 hours by
default. A retry or recent `generated_at` cannot refresh an old recovery point.
An enabled source with no successful backup is FAIL. Missing, unreadable,
malformed or mismatched evidence is UNKNOWN. Only an explicitly disabled source
is NOT_APPLICABLE.

The latest attempt is independent: failed is FAIL, skipped is WARN, and running
for more than four hours is FAIL. Workers refused by the shared recovery lock
leave status unchanged; their Job/log records the skip. `latest_drill` is assessed
separately: missing
evidence is UNKNOWN and a failed drill is FAIL. A successful upload cannot satisfy
a restore drill. The result displays hours since the recorded drill and does not
infer that newer backups have been exercised. Status strings and subprocess
errors are never copied into reports.

The status is controller evidence of completed remote verification, not a new
remote readback on each monitor tick. Rehearse independent restore access and
retain dated evidence using [the PostgreSQL procedure](postgres-backup-restore.md).

### Throughput and capacity forecasts

Add Prometheus queries for actual service metrics. Use `warn_above`/`fail_above`
only for agreed latency, errors, saturation or backlog targets. A maximum
throughput baseline requires `validated_baseline` plus `baseline_evidence`, such
as the existing dated staging load report and matching resource topology.
Current low QPS does not prove remaining capacity.

Compute p95/p99 only from an actual histogram. A missing bucket series, NaN,
stale values, or inaccessible metrics produce UNKNOWN. For evaluated PromQL
expressions, also configure source freshness queries (for example timestamp or
scrape age); an expression's current evaluation timestamp alone does not prove
its inputs are current. Prometheus may use `ca_file` and `token_file`.

Forecasts require at least 72 hours, at least 90% observed sampling coverage,
unchanged capacity, and positive daily growth. They use the median daily growth
from at most seven days. A 30-minute acceptance cannot establish an exhaustion
forecast. History coverage includes missed sampling time and interrupted files.

## Dedicated synthetic probes

A credentials JSON must contain `"dedicated": true`. The tool does not turn a
normal operator/device identity into a test identity. Provision dedicated accounts
and resources before enabling this option. One probe group runs at a time, no
more often than five minutes in watch, and has a total 90-second deadline.

- Account Manager: configure `/v1/auth/login`, `/v1/auth/refresh`, `/v1/me` URLs. The 0600
  credentials JSON contains `email` and `password`, or an initial refresh token.
  Each successful rotated pair is stored atomically before later probes use it.
- Video Cloud: configure `/request_token`, `/refresh_token`, and a permitted
  authenticated read endpoint. Dedicated JSON contains `client_cert_file`,
  `client_key_file`, `scope`, `devid`, and `service` as required by the identity.
  This exercises the existing token reauthentication flow using mTLS.
  Tokens with current product/entitlement revisions and test_lab actors are
  reauthenticated; the legacy stateless refresh is tested only where supported.
- MQTT: configure broker, authorized exact topic prefix (no wildcard), and JSON
  username/password or dedicated client mTLS references. A random suffix and
  client ID isolate each run. Subscribe, publish a non-retained payload, verify
  the roundtrip, unsubscribe, and disconnect. Confirm the account's ACL permits
  that dedicated prefix.
- TURN: configure UDP/TCP server, realm, and an operator-provisioned dedicated
  STUN responder as `peer_address`; JSON includes username/password. Allocate,
  relay a STUN binding request through that peer, verify the matching response,
  and close the allocation. A successful allocation alone is not relay success.

Missing credentials, test peer, or probe configuration remains UNKNOWN/NOT_RUN.
Authentication tests are not a business operation or billing transaction test.
Cleanup is reported separately from functional success.

## Scheduling, files and operational use

| Category | Interval |
| --- | --- |
| Database, capacity and performance metrics | 30 seconds |
| Kubernetes, HTTP, active TLS and token validity | 60 seconds |
| Complete certificate-tools discovery | 15 minutes |
| Explicit dedicated synthetic probes | 5 minutes |
| Previous calendar day's PDF | 08:00 Asia/Taipei (configurable) |

Categories have separate bounded runs, so slow certificate discovery and PDF
rendering do not pause metrics. A late/cached result keeps its real observation
time. Full certificate discovery has a five-minute deadline; cancellation stops
its subprocess, and the next certificate run cannot overlap it. One writer is
allowed per environment/output directory. Locks release after
a crash; the lock file remains. Restart resumes history and daily report state.
Watch confirms transient HTTP connection failures over three observations and
recovery over two successes. It preserves the raw status and streak counts;
critical expiry, revocation, disappeared workloads and rejected writes are immediate.

Outputs:

- `history/<environment>/<UTC-date>.jsonl`: schema-versioned samples/results.
- `history/<environment>/latest.json`: atomic latest snapshot.
- `history/<environment>/latest-report.json`: daily report outcome.
- `reports/<environment>/<name>.json`, `.html` and `.pdf`: summary, service status,
  credentials, capacity, performance, charts, failures and coverage gaps.

HTML/PDF fold more than 40 passing discovered-certificate details into a counted
summary, while retaining every risk and unknown entry. JSON and saved history
retain all observations, including the folded passing items and events.

Owned directories are 0700 and files 0600. Retention removes only date-named
history for that environment after 30 days by default. Complete malformed history
stops reading rather than silently skipping it. A crash-truncated tail becomes an
UNKNOWN gap and can be resumed. Report generation reads saved observations only;
missing historical data is visible. A failed Chromium run preserves HTML.

The supplied [`cloud-monitor@.service`](../scripts/cloud-monitor/cloud-monitor@.service)
is a Linux installation template. It is not installed by this change. Adjust
paths, kubeconfig, Chromium/fonts and credentials for the monitor service account.
The template leaves synthetics disabled; add the flag only with dedicated inputs.

## Verification

```sh
cd scripts/go
go test ./internal/cloudmonitor ./cloud-monitor
go test -race ./internal/cloudmonitor ./cloud-monitor
go test ./rtk-cloud -run TestMonitorInventory
```

Tests cover missing/disappeared workloads, unknown intent, TLS/CRL and invalid
tokens, expired/opaque credentials, HTTP fallbacks, Redis durability, counter
resets, forecast evidence, private history, crash recovery, writer exclusion,
redaction, report failures, and dedicated probe rotation/cleanup. CI uses fixtures
and local fake servers; it must not contact Dev/Staging/Prod.

For environment acceptance, compare collected values against existing sources,
observe at least 30 minutes, review actual rendered PDF pages, and retain a dated
summary. Dedicated identity and PostgreSQL role prerequisites must be recorded;
UNKNOWN is not acceptance of an untested function.
