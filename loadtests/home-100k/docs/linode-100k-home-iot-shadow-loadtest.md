# Linode 100K Home IoT Device Shadow Load Test

Status: live VM lifecycle and real MQTT/API shard runner wiring implemented;
50K staging PASS captured, 100K requires a fresh target-specific PASS
Owner: rtk_cloud_workspace

## Summary

The Linode 100K Home IoT Device Shadow load test validates whether the current
staging/LKE runtime can handle the first 100K home baseline:

- 100,000 simulated home devices.
- 5,000 simulated app users.
- 10 devices per user.
- IoT Device Shadow desired/reported/delta/version convergence.
- Online, offline, and flapping reconnect device presence.

This is not a broker-only MQTT test. MQTT transport is part of the scenario,
but the capacity claim depends on the complete IoT Device Shadow path:

```text
app writes desired
cloud computes delta
device receives delta or gets shadow after reconnect
device writes reported
cloud clears delta
```

The canonical design, scenario definitions, review-time CLI, fixtures, and
report schema live under:

```text
loadtests/home-100k/
```

## Latest Validated Staging Run

The latest completed staging run for this flow is:

| Field | Value |
| --- | --- |
| Run ID | `lt50k-api3-ramp15m-fallback-r4-20260620T045511Z` |
| Target | 50,000 devices, 2,500 app users |
| Generator layout | 5 mixed Linode VMs, 10K devices and 500 users per VM |
| Stage window | 15m ramp, 20m steady, 2m cool-down |
| Result | `PASS` |
| Device target coverage | 50,000 / 50,000 |
| Device token failures / retry exhausted | 0 / 0 |
| App desired writes / ACKs | 2,500 / 2,500 |
| Server correlation | `pass` |

This validates the 50K profile with a realistic ramp. It is not a 100K result.
For 100K, re-run the capacity plan from the requested target and produce a new
100K `PASS` report.

Residual 50K evidence to keep watching: EMQX reported small
`video-cloud-api-*-message-sub` pressure (`emqx.conn_congestion=2`,
`emqx.socket_error=8`, `emqx.timeout=8`). That signal did not fail the 50K
report, but it should be treated as the next API-to-MQTT shadow-path bottleneck
candidate for larger targets.

## Baseline

Default first baseline:

| Condition | Value |
| --- | --- |
| Server target | Current staging/LKE env-root |
| Load generators | Ephemeral Linode VMs |
| Region model | Single Linode region |
| Stage window | Single target stage at the requested connected-device count |
| VM layout | 5 mixed execution shards labeled `lg01`..`lg05` by default |
| Per-VM device task | Up to 10K simulated devices |
| Per-VM user task | 500 simulated app users |
| Total load-generator VMs | 5 for the default 100K-device/5K-user mixed baseline |

Device mix:

| Device type | Count |
| --- | ---: |
| Light | 50,000 |
| Air conditioner | 20,000 |
| Smart meter | 30,000 |

Presence mix:

| Presence class | Count |
| --- | ---: |
| Online steady | 85,000 |
| Offline desired queue | 10,000 |
| Flapping reconnect | 5,000 |

## Review-Time CLI

Use one script under the load-test package:

```sh
loadtests/home-100k/scripts/home-100k.sh
loadtests/home-100k/scripts/home-100k.sh plan
loadtests/home-100k/scripts/home-100k.sh dry-run
loadtests/home-100k/scripts/home-100k.sh workflow-dry-run
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh
```

Secrets and non-secret test descriptions are intentionally separate:

- `~/.env` supplies only `LINODE_TOKEN`.
- `loadtests/home-100k/scenarios/default.description.env` supplies the
  non-secret test description: env-root, brand, region, remote paths, SSH key
  path, status interval, stage durations, and target load size.

The `home-100k` name is the package name only; VM labels identify load-generator
execution shards and default to `lg01`..`lg05` via `HOME100K_VM_LABEL_PREFIX=lg`.
The active test size is configured in the description file with
`HOME100K_DEVICES`, optional `HOME100K_USERS`, and
`HOME100K_DEVICES_PER_USER`. Changing between 10K, 50K, 100K, or another target
must be a single description-file/profile or shell environment change, not edits
spread across script names, docs, Ansible, and Go code.

The default description file points at the existing provision-server staging
environment:

```text
cloud_env/staging/lke
```

## EMQX Capacity And Placement

The server target is EMQX on LKE. Before a formal 100K run, verify that the
staging server deployment has enough broker capacity and that broker pods are
spread across nodes:

- `LKE_MQTT_REPLICAS=9`.
- `LKE_MQTT_NODE_POOL_ID=<pool-id>` points EMQX at a node pool with at least 9
  schedulable nodes.
- The EMQX deployment uses hard pod anti-affinity by
  `kubernetes.io/hostname`.
- `mqtt-public` uses `externalTrafficPolicy: Local`.
- Linode NodeBalancer health for port `8883` must show the expected EMQX nodes
  as up before the load test starts.

Do not use `externalTrafficPolicy: Cluster` to compensate for uneven EMQX
placement. It can route public MQTT traffic through kube-proxy on nodes without
broker pods, which makes one node appear as the bottleneck instead of measuring
EMQX capacity.

Defaults:

These are the checked-in short debug defaults. Formal 10K, 50K, 100K, or custom
runs must override the target size and stage windows explicitly.

| Environment variable | Default |
| --- | --- |
| `HOME100K_DESCRIPTION_FILE` | `loadtests/home-100k/scenarios/default.description.env` |
| `HOME100K_SECRET_ENV_FILE` | `~/.env`, only `LINODE_TOKEN` is read |
| `HOME100K_ENV_ROOT` | `cloud_env/staging/lke` |
| `HOME100K_BRANDNAME` | `RTK` |
| `HOME100K_REGION` | `us-sea` |
| `HOME100K_RUN_ID` | Current UTC timestamp |
| `HOME100K_OUT_DIR` | `loadtests/home-100k/reports/<run-id>` |
| `HOME100K_SSH_USER` | `root` |
| `HOME100K_AUTHORIZED_KEY_FILE` | `<HOME100K_SSH_KEY>.pub` |
| `HOME100K_STATUS_INTERVAL_SECONDS` | `30` |
| `HOME100K_STAGE_WARM_UP` | `15s` |
| `HOME100K_STAGE_STEADY` | `45s` |
| `HOME100K_STAGE_COOL_DOWN` | `15s` |
| `HOME100K_DEVICES` | `9000` from the default debug description |
| `HOME100K_USERS` | unset; planner derives `ceil(devices / devices-per-user)` |
| `HOME100K_DEVICES_PER_USER` | `20` |
| `HOME100K_RUNNER_MODE` | `live` |
| `HOME100K_RUNNER_NOFILE_LIMIT` | `1048576`; remote runner daemon file-descriptor limit for MQTT sockets |
| `HOME100K_RUNNER_MQTT_CONCURRENCY` | `1000`; per-VM `cloud-mqtt-test` connect/action concurrency |
| `HOME100K_CREDENTIAL_BUNDLE_FORMAT` | `sqlite-gzip` |
| `HOME100K_MQTT_ADDR` | `auto-public-mqtt`; live commands discover public MQTT LoadBalancer IPs |
| `HOME100K_MQTT_PUBLIC_LB_COUNT` | `1`; limits auto-discovered MQTT LoadBalancers for the default debug profile |
| `HOME100K_NODE_RESOURCE_STATUS` | `1` |
| `HOME100K_K8S_NODE_RESOURCE_STATUS` | `1` |
| `HOME100K_KUBECONFIG` | unset; falls back to existing LKE kubeconfig env or `<env-root>/state/lke-kubeconfig.yaml` |

Generate a deterministic plan without creating Linode resources:

```sh
loadtests/home-100k/scripts/home-100k.sh plan
```

Review each workflow step without creating or deleting Linode resources:

```sh
HOME100K_RUN_ID=20260615T100000Z loadtests/home-100k/scripts/home-100k.sh workflow-dry-run
```

Render the local review-time run report. VM lifecycle is handled by the
separate workflow commands above; without server evidence this command marks
the report incomplete by design:

```sh
HOME100K_RUN_ID=20260615T100000Z loadtests/home-100k/scripts/home-100k.sh dry-run
```

Run the live VM workflow:

```sh
HOME100K_RUN_ID=lt100k-example-$(date -u +%Y%m%dT%H%M%SZ) \
HOME100K_DEVICES=100000 \
HOME100K_STAGE_WARM_UP=30m \
HOME100K_STAGE_STEADY=40m \
HOME100K_STAGE_COOL_DOWN=1s \
HOME100K_RUNNER_MQTT_CONCURRENCY=1000 \
  loadtests/home-100k/scripts/home-100k.sh workflow-live

HOME100K_RUN_ID=<run-id> \
  loadtests/home-100k/scripts/home-100k.sh shutdown-vms
```

The default command is `workflow-live`: it creates the load-generator VMs,
syncs the runner binaries plus minimal env-root artifacts, dispatches shards,
collects shard artifacts, collects server evidence, and aggregates the report.
It then powers off reusable VMs only after a successful `PASS` report. If the
workflow fails, or the final report is `FAIL`/`INCOMPLETE`, the VMs remain
available for inspection and `workflow-resume-live`; run `shutdown-vms` when
debugging is finished, or `destroy-vms --live --confirm-live` only when the pool
should be deleted. During the live workflow the script prints status immediately
and every 30 seconds with the current
phase, VM count, collected shard count, server evidence state, report status,
per load-generator VM resource samples, and per Kubernetes node resource
samples. Provisioned node inventory is written to
`loadtests/home-100k/reports/<run_id>/nodes.tsv`.

Kubernetes node resource samples use `kubectl top nodes --no-headers` and print
`[home-100k k8s-node]` lines. Kubeconfig resolution order is
`HOME100K_KUBECONFIG`, `RTK_CLOUD_LKE_KUBECONFIG`, `LKE_KUBECONFIG`,
`CLOUD_STAGING_K8S_KUBECONFIG`, then
`<env-root>/state/lke-kubeconfig.yaml`. Set
`HOME100K_K8S_NODE_RESOURCE_STATUS=0` to disable K8s node probing.

Stage duration is part of the non-secret description file. The default profile
uses `HOME100K_STAGE_WARM_UP=15s`, `HOME100K_STAGE_STEADY=45s`, and
`HOME100K_STAGE_COOL_DOWN=15s`, which plans a 75-second single target stage
before VM lifecycle and evidence overhead. The warm-up value is the connect
ramp and must be less than the full-load test window
`HOME100K_STAGE_STEADY + HOME100K_STAGE_COOL_DOWN`. Use explicit
shell environment overrides for longer baseline runs, or a custom
`HOME100K_DESCRIPTION_FILE` for a reusable alternate profile. Explicit shell
environment variables take precedence over the description file.

The 100K report profile uses a 30-minute warm-up ramp and a 40-minute steady
window, plus a minimal 1-second cool-down because duration values must be
positive. This ramp avoids compressing token bootstrap into a short burst while
preserving a full-load evidence window. If a shorter ramp is selected, treat
high `device_request_token` failure, retry exhaustion, ingress 499/5xx, or
request-token p95/p99 latency as token bootstrap pressure rather than as
broker-capacity evidence.

Runner mode is also part of the non-secret description file. The default is
`HOME100K_RUNNER_MODE=live`. Live mode invokes the copied `rtk-cloud` runner to
execute real `mqtt-test` traffic against the selected env-root. It must not
fall back to sampled in-memory actor flows.

The workflow writes `plan.json`, `results.json`, `server-evidence.json`, raw
workflow/resource telemetry, and `TEST_REPORT.md`. The final Markdown report is
generated by `loadtests/home-100k/scripts/generate-report.sh` from
`loadtests/home-100k/reports/templates/TEST_REPORT.md.tmpl`; do not hand-author
the report.

The intended live VM workflow represented by the generated lifecycle action
plan is:

```text
plan -> provision-vms -> sync -> run-stages -> collect -> collect-server-evidence -> aggregate -> destroy-vms
```

VM provisioning is dry-run by default for review. A paid/destructive Linode
operation must use `provision-vms --live --confirm-live` with a Linode token.
Live provision writes `vms.json` when `--out-dir` is set; pass that file to
`destroy-vms --live --vm-state-file` for cleanup.
Live provision accepts `--linode-type`, `--linode-image`, `--root-pass`, and
`--authorized-key-file`; root password material is not written to test
artifacts.
Live sync reads `vms.json`, generates an Ansible inventory from provisioned VM
IPs, writes one shard manifest per VM, and runs
`loadtests/home-100k/ansible/sync.yml`. The playbook copies only the Linux
runner binary, assigned shard manifest, one compressed SQLite credential bundle
for that VM shard, and minimal env-root artifacts; it does not copy the full
generated device tree or report history.

The credential bundle format is fixed by
`HOME100K_CREDENTIAL_BUNDLE_FORMAT=sqlite-gzip`. Before Ansible sync, the host
builds `<out-dir>/credential-bundles/<vm-label>.sqlite.gz` plus a manifest for
each VM. The archive uploaded to that VM includes only
`loadtests/home-100k/credentials/<vm-label>.sqlite.gz` and the manifest, not
the expanded `devices/test_device/devices/**` and
`devices/test_device/bundles/**` PEM fan-out. This avoids tens of thousands of
inodes per shard and makes future VM/orchestra reuse checkable by sha256.
Artifact fanout compares sha256 checksums and skips unchanged runner binaries,
the `rtk-cloud` binary, shard manifests, and other cacheable files.
Live run-stages reads `vms.json`, regenerates the same inventory, and runs
`loadtests/home-100k/ansible/start-runner.yml`. Ansible starts a
`home-100k runner-daemon` on each VM and waits for `READY_WAIT`; it does not
perform the synchronized stage start. The host coordinator waits for the full
ready barrier, sends `START(run_id, sequence, delay_ms)` to every VM, and each
runner uses its local monotonic clock for the final delay before opening
MQTT/API traffic. The run writes `start-coordination.json` with ready barrier,
per-VM start timestamps, and max start skew. In `runner_mode=live`, each VM
calls the copied `rtk-cloud` binary once to run the assigned staged MQTT/API
shard only after START is received. Device MQTT delta subscriptions are
lifetime state for the single target stage. The stage warm-up spreads
`/request_token`, TLS, MQTT CONNECT, and shadow-delta subscription work across
the ramp interval. The ramp interval is the target arrival curve, not a hard
connect stop; if the shard is still below target when ramp ends, it keeps
opening remaining sessions until target coverage is reached or the stage's
action-reserve deadline is hit. Existing device connections and subscriptions
remain open through the steady and cool-down windows. Reports must show both
new subscribe packets and active connection/subscription gauges for the
requested target.

Load-generator runtime limits are part of the test conditions:

- The remote runner daemon sets `ulimit -n` from
  `HOME100K_RUNNER_NOFILE_LIMIT`, default `1048576`. This value is file
  descriptor headroom, not a target connection count. It prevents a 9K or 100K
  run from being invalidated by the generator's OS FD ceiling before EMQX,
  NodeBalancer, IoT Device Shadow, or the Kubernetes nodes are actually tested.
  If any generator hits `too many open files`, the report must classify the run
  as `INCOMPLETE` because the load generator saturated first.
- Device MQTT reads are sustained for the whole device online lifetime. The
  runner uses Go's normal network poller and one bounded reader goroutine per
  active device MQTT connection to dispatch subscribed shadow delta publishes.
  A command-time one-shot blocking read is not an acceptable live model because
  it can create client-side read starvation and falsely look like missing
  server-side deltas.

Live collect reads `vms.json`, regenerates the inventory, and runs
`loadtests/home-100k/ansible/collect.yml` to fetch shard `results.json`, shard
reports, runner coordination telemetry, daemon logs, resource snapshots, and
sync telemetry.
Live server evidence collection runs `kubectl` probes for EMQX, Video Cloud API,
IoT Device Shadow, PostgreSQL, Redis/Valkey, ingress/nginx, and host/pod
resources. Partial probe failure is written as `complete=false` evidence so the
final report remains `INCOMPLETE`.
Aggregate reads collected shard results plus `server-evidence.json` and writes
run-level `plan.json` and `results.json`. The public `home-100k.sh aggregate`
path then calls `scripts/generate-report.sh` to render fixed-format
`TEST_REPORT.md`. Any shard with `load_generator_health.saturated=true` or
insufficient client target coverage forces `INCOMPLETE`.
Use `list-vms --live --run-id <run-id>` to inspect leftover load-generator VMs
by `home-100k`, `<run-id>`, and `load-generator` tags before cleanup.

If a run fails before the normal cleanup path, use the emergency cleanup helper:

```sh
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh --run-id <run-id>
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh --run-id <run-id> --yes
```

The first command is dry-run and prints every matched Linode VM. The helper
matches only VMs tagged with `home-100k`, the selected run id, and
`load-generator`. Prefer run-scoped shutdown or destroy commands for normal
cleanup.

## Report Rules

The final report must include:

- Test conditions.
- Device scenario.
- User scenario.
- IoT Device Shadow scenario.
- Offline desired queue and flapping reconnect coverage.
- Per-stage shadow p50/p95/p99 latency.
- Desired/reported convergence rate.
- Offline desired convergence rate.
- Delta-clear success rate.
- Duplicate apply count.
- Version conflict and rejected update counts.
- Load-generator saturation.
- Server-side metrics/log evidence.
- Client target coverage by stage.
- Device MQTT and APP/User totals by stage and total.
- Load-generator VM resource usage during the run.
- Per-Kubernetes-node resource usage during the run.
- Sync/provision transfer telemetry.
- Bottleneck assessment.

If IoT Device Shadow evidence, server evidence, parsed MQTT/API counters,
client target coverage, or resource telemetry is missing, the report status
must be `INCOMPLETE`, not `PASS`.

If load-generator saturation invalidates the run, the report must say so
instead of attributing the bottleneck to the server.

## Relationship To The Legacy 10K MQTT Helper

The existing `rtk-cloud mqtt-loadtest` helper remains useful as a lower-level
MQTT shard and aggregation reference. The 100K home load test owns the product
scenario, IoT Device Shadow semantics, VM lifecycle, and report contract.
