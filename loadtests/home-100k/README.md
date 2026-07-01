# 100K Home IoT Device Shadow Load Test

Status: implemented review-time runner and live VM lifecycle scaffold
Owner: rtk_cloud_workspace
Last updated: 2026-06-15

## Summary

This proposal defines a 100K home load-test suite for RTK cloud staging. The
test validates 100,000 simulated home devices, 5,000 simulated app users, and
the IoT Device Shadow behavior that connects user intent to device-reported
state.

The load generators run on ephemeral Linode VMs, not inside the server
Kubernetes cluster. The server target for the first baseline is the current
staging/LKE environment. The first official baseline uses one Linode region so
capacity and bottleneck analysis are not mixed with cross-region network
variance.

The test must prove more than MQTT connectivity. It must prove that IoT Device
Shadow desired, reported, delta, and version semantics still converge under
online, offline, and reconnect load.

## Goals

- Validate a 100K-device and 5K-user home scenario against staging/LKE.
- Model realistic device and user behavior instead of broker-only publish load.
- Exercise RTK's current IoT Device Shadow contract:
  `$vc/devices/{devid}/shadow/...` and `/api/devices/{devid}/shadow`.
- Capture load-generator results and server-side metrics/logs with a shared
  `run_id`.
- Produce a report that includes test conditions, device scenario, user
  scenario, IoT Device Shadow scenario, per-stage results, and bottleneck
  assessment.

## Non-Goals For The First Baseline

- Multi-region load generation.
- Legacy Linode VM server-runtime comparison.
- Named IoT Device Shadows.
- WebRTC, video relay, clip upload, snapshot upload, or media quality testing.
- Long-lived load-generator VM pools.

These can become later profiles after the first single-region baseline is
credible.

## Video-Enabled 1K Pilot Profile

`video-1k-v1` is the first video-enabled pilot profile. It keeps the existing
home MQTT/shadow runner as the primary 1K device load and reuses the workspace
video runner for WebRTC and RTP media evidence.

Default pilot shape:

| Condition | Value |
| --- | ---: |
| Home devices | 1,000 |
| Derived app users | 50 |
| Video-capable camera devices | 100 |
| WebRTC viewers | 100 |
| WebRTC media set | `h264` |

Run it through the wrapper by selecting the scenario description:

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/video-1k.description.env \
HOME100K_RUN_ID=video1k-$(date -u +%Y%m%dT%H%M%SZ) \
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

The wrapper writes video runner artifacts to
`loadtests/home-100k/reports/<run_id>/video/`. `aggregate` reads the existing
`e2e_test/video_cloud/load` JSON output and folds WebRTC lifecycle, media, and
TURN evidence into `TEST_REPORT.md`.

For `video-1k-v1`, missing WebRTC create/setup/close evidence makes the report
`INCOMPLETE`. A signaling success rate below the functional threshold is a
`FAIL`. If H.264 media is enabled, missing ICE-connected or first-RTP evidence
is also a `FAIL`. Missing external TURN/coturn evidence is `INCOMPLETE`, so a
signaling-only pass is never reported as media-capacity success.

## Directory Ownership

All new code and materials for this test suite should live under this
directory:

```text
loadtests/home-100k/
```

Expected layout:

```text
loadtests/home-100k/
  README.md
  scenarios/
  plans/
  reports/
  cmd/
  internal/
  fixtures/
  scripts/
```

`README.md` is the reviewable design and operator-facing overview. Scenario
definitions, generated run plans, report schemas, fixtures, CLI code, VM
orchestration, aggregation, and helper scripts should stay under the same
directory so this load test is versioned as one package.

## Baseline Test Conditions

Default baseline:

| Condition | Value |
| --- | --- |
| Devices | 100,000 |
| Users | 5,000 |
| Devices per user | 20 |
| Server target | Current staging/LKE env-root |
| Load-generator runtime | Ephemeral Linode VMs |
| First baseline region model | Single Linode region |
| Device-generator density | Configured by `HOME100K_LOAD_GENERATOR_DEVICES_PER_VM`; default 20,000 devices per VM |
| Load-generator VM count | Automatically planned as `ceil(HOME100K_DEVICES / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)` unless `HOME100K_VM_COUNT` overrides it |
| Per-VM device task | Up to `HOME100K_LOAD_GENERATOR_DEVICES_PER_VM` devices |
| Per-VM user task | Derived from the assigned device shard and `HOME100K_DEVICES_PER_USER` |
| Total load-generator VM count | 5 for the default 100K-device/5K-user mixed baseline; smaller targets such as 9K or 1K plan 1 VM by default |
| Ramp-up time | Configured by `HOME100K_STAGE_WARM_UP` |
| Target connects | Configured by `HOME100K_DEVICES` |

The test should use deterministic sharding. A run ramps directly to the target
connection count; there is no staged 25%/50%/75%/100% load model.

## Server-Side Capacity Prerequisites

The staging/LKE target must run MQTT on EMQX. Public MQTT load testing should
use an EMQX node pool sized so each broker pod can land on a different
Kubernetes node. For the 100K baseline this means:

- `LKE_MQTT_REPLICAS=9`.
- A dedicated or otherwise uncongested node pool with at least 9 schedulable
  nodes.
- `LKE_MQTT_NODE_POOL_ID=<pool-id>` when EMQX should be pinned to that pool.
- EMQX pod hard anti-affinity on `kubernetes.io/hostname`.
- `mqtt-public` service `externalTrafficPolicy: Local`, so the Linode
  NodeBalancer only sends MQTT traffic to nodes with a local EMQX endpoint.

Using `externalTrafficPolicy: Cluster` can hide missing broker placement by
forwarding traffic through kube-proxy on nodes without EMQX. That makes node
CPU and conntrack pressure look like broker capacity and is not the desired
baseline for this test.

## Home Device Mix

Each simulated 10-device home uses this mix:

| Device type | Count per home | Total at 100K |
| --- | ---: | ---: |
| Light | 5 | 50,000 |
| Air conditioner | 2 | 20,000 |
| Smart meter | 3 | 30,000 |

The mix intentionally creates heavier light-control traffic, moderate HVAC
state transitions, and steady smart-meter reported telemetry.

## Device Presence Mix

The load test must include devices that are not online when user desired state
is written. IoT Device Shadow is specifically valuable because the cloud can
store desired state while a device is offline and later converge when it
reconnects.

Default presence mix:

| Presence class | Percent | Count at 100K | Purpose |
| --- | ---: | ---: | --- |
| Online steady | 85% | 85,000 | Baseline connected-device and realtime delta behavior. |
| Offline desired queue | 10% | 10,000 | User writes desired while device is offline; device converges after reconnect. |
| Flapping reconnect | 5% | 5,000 | Reconnect, version handling, duplicate-apply prevention, and stale desired behavior. |

The report must include offline duration distribution and per-presence-class
results. A run that only tests always-online devices is not a full IoT Device
Shadow load test.

## IoT Device Shadow Scenario

Canonical success path:

```text
app writes desired
cloud computes delta
device receives delta or reads shadow after reconnect
device applies desired
device writes reported
cloud clears delta
```

Required contract coverage:

- App/user token can write `state.desired`.
- Device token can write `state.reported`.
- Device token cannot write `state.desired`.
- Desired/reported mismatch produces `state.delta`.
- Matching reported state clears `state.delta`.
- `version` increases on successful mutation.
- Stale `version` writes are classified as conflicts.
- `clientToken` is preserved for request/result correlation.
- MQTT shadow responses publish accepted/rejected/documents/delta events as
  applicable.

Runtime cache and worker sizing notes:

- The MQTT inbound queue is a short burst buffer between the broker read loop
  and application handlers. It is not a capacity multiplier. Raising handler
  worker count above downstream capacity only adds scheduler overhead and can
  push pressure into database, Redis, or serialized MQTT writes.
- Size Video Cloud MQTT handler workers conservatively against the slowest
  downstream path. Shadow mutation handlers may write Redis and the backing
  shadow store; accepted/documents/delta MQTT publishes are still serialized by
  the runtime socket writer.
- Shadow reads must prefer the Redis/Valkey hot document. When the Redis shadow
  document is present, `get` must return from cache without reading the backing
  store. Backing-store reads are only for cache miss hydration or explicit
  mutation paths.
- Successful shadow `update` and `delete` requests are document mutations even
  when the value patch is idempotent: version, timestamp, metadata, and
  client-token response fields can change. The hot path writes Redis first and
  projects to PostgreSQL asynchronously.
- Redis/Valkey is the authoritative API/runtime read path during shadow cache
  operation. PostgreSQL is allowed to lag Redis by the configured write-behind
  flush interval, normally 1s.
- Shadow write-behind must use at least double buffering: while one batch is
  flushing to PostgreSQL, new mutations continue entering a separate active
  buffer. The buffer is keyed by shadow document key so repeated writes in one
  flush window coalesce to the newest version.
- Redis dirty-set membership is the crash-recovery source for write-behind
  projection. A successful PostgreSQL batch flush removes dirty keys; failed
  flushes keep dirty keys for retry.
- Runtime logs, telemetry, and shadow commands must be interpreted separately
  when diagnosing 100pct command bursts. If these share one FIFO handler queue,
  increasing worker count does not guarantee shadow command latency.

HTTP surface:

```text
GET    /api/devices/{devid}/shadow
POST   /api/devices/{devid}/shadow
DELETE /api/devices/{devid}/shadow
GET    /api/devices/{devid}/shadows
```

MQTT surface:

```text
$vc/devices/{devid}/shadow/get
$vc/devices/{devid}/shadow/get/accepted
$vc/devices/{devid}/shadow/get/rejected
$vc/devices/{devid}/shadow/update
$vc/devices/{devid}/shadow/update/accepted
$vc/devices/{devid}/shadow/update/rejected
$vc/devices/{devid}/shadow/update/delta
$vc/devices/{devid}/shadow/update/documents
$vc/devices/{devid}/shadow/delete
$vc/devices/{devid}/shadow/delete/accepted
$vc/devices/{devid}/shadow/delete/rejected
```

Named shadow topics and `name=` HTTP parameters are out of scope for the first
baseline.

## Device Scenario

All simulated devices use issued device tokens and MQTT. Device certificates
from the env-root are used only for token bootstrap.

### Online Steady Devices

- Connect to MQTT and remain online during the run window.
- Subscribe to shadow delta topics immediately after MQTT connect and keep that
  subscription for the whole device lifetime. Stage transitions must add newly
  online devices; they must not disconnect and resubscribe devices that were
  already online in an earlier stage.
- Call shadow `get` on startup to establish initial version and state.
- Apply desired changes and publish reported state.
- Confirm that delta clears after reported matches desired.

### Offline Desired Queue Devices

- Disconnect before selected user desired writes.
- Remain offline while app/user actors write desired state.
- Reconnect after the configured offline duration.
- Call shadow `get` immediately after reconnect.
- Apply queued desired state.
- Publish reported state.
- Confirm that delta clears.

### Flapping Reconnect Devices

- Disconnect and reconnect during the run window.
- Call shadow `get` on each reconnect.
- Avoid duplicate application of desired state that is already reflected in
  reported state.
- Treat stale version writes as conflicts, not generic failures.
- Report any out-of-order or repeated apply behavior.

### Device-Type Behavior

Light devices:

- Apply desired `power`.
- Apply desired `brightness`.
- Apply desired `color_temperature_kelvin`.
- Publish reported state after applying each desired update.

Air conditioner devices:

- Apply desired `power`.
- Apply desired `mode`.
- Apply desired `target_temperature_celsius`.
- Apply desired `fan`.
- Move `current_temperature_celsius` gradually toward target temperature.
- Publish reported state after applying each desired update.

Smart meter devices:

- Publish reported `energy_kwh` as a monotonic counter.
- Publish reported `power_watts`, `voltage_v`, `current_a`, and
  `frequency_hz`.
- Validate freshness and monotonic behavior from user/app reads.
- Smart meters are read-oriented in the first baseline; heavy command behavior
  is out of scope.

## User Scenario

User actors represent app users, not device credentials. Each user owns 10
devices from the bind artifact.

Required user flow:

1. Login or reuse credentials from env-root user artifacts.
2. Bootstrap app certificate if Account Manager requires a CSR.
3. Request a Video Cloud app token over mTLS.
4. List authorized devices.
5. Read current IoT Device Shadow documents for owned devices.
6. Write desired state for selected owned lights and air conditioners.
7. Observe convergence until reported matches desired and delta clears.
8. Read smart-meter reported state and validate freshness and monotonic
   counters.
9. Execute negative checks for cross-user access where configured.

User actors must not use device credentials. Device actors must not use app/user
credentials.

## VM Orchestration

The first implementation should provide a single workflow:

```text
plan -> provision-vms/reuse-vms -> sync -> run-stages -> collect -> collect-server-evidence -> aggregate -> shutdown-vms-or-preserve-vms
```

Required behavior:

- `plan` prints VM count, role layout, shard ranges, ramp-up time, target
  connects, scenario
  mix, expected artifacts, server evidence queries, and cleanup plan.
  By default the VM count is calculated as
  `ceil(HOME100K_DEVICES / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)`.
  `HOME100K_VM_COUNT`/`--vm-count` is an explicit override for operator-driven
  sharding experiments, but the planner rejects an override that exceeds the
  configured per-VM generator capacity.
- `provision-vms` creates or reuses Linode VMs in the selected region. It is a
  dry-run by default; live provisioning requires `--live --confirm-live` and a
  Linode token from `--linode-token` or `LINODE_TOKEN`.
- `sync` generates an Ansible inventory from `vms.json`, writes one shard
  manifest per VM, builds one compressed SQLite credential bundle per VM, and
  copies only the Linux runner binary, the VM's manifest, the compressed
  credential bundle, and minimal env-root artifacts.
- `run-stages` uses the generated Ansible inventory only to start per-VM
  runner daemons in `READY_WAIT`. The host coordinator then waits for all VMs
  to report ready, sends a shared `START` command, and polls completion. Formal
  runs must use `runner_mode=live`; sampled actor execution is allowed only for
  developer smoke tests and must not be reported as capacity evidence.
- `collect` uses the generated Ansible inventory to retrieve per-VM results,
  sync telemetry, and local load-generator telemetry.
- `collect-server-evidence` queries server metrics/logs for the same `run_id`
  and measured run window.
- `aggregate` reads collected shard results plus `server-evidence.json` and
  writes run-level `plan.json` and `results.json`. The public script then runs
  `scripts/generate-report.sh` to render `TEST_REPORT.md` from the fixed
  template and collected artifacts.
- `list-vms` lists leftover load-generator VMs by `home-100k`, `run_id`, and
  `load-generator` tags when cleanup needs review.
- `shutdown-vms` powers off reusable load-generator VMs after collection. Use
  the cleanup script only when the operator intentionally wants to delete the
  VM pool.
- `workflow-live` and `workflow-resume-live` automatically shut down VMs only
  when the workflow return code is `0` and the rendered report status is
  `PASS`. Failed, `FAIL`, or `INCOMPLETE` runs preserve VMs by default for
  resume/debug. Set `HOME100K_SHUTDOWN_ON_ERROR=1` only when the operator wants
  automatic shutdown even after a failed or incomplete run.

If a run fails before cleanup, the tooling must be able to list, shut down, or
explicitly delete leftover load-generator VMs by tag/run id.

### Device/User Shard Assignment

The default 100K baseline creates 5 mixed VM assignments. Each assignment owns
one device shard and one user shard. Smaller targets keep the same mixed layout
when the planner can fit the target into those assignments; the 50K staging
PASS used the same 5 labels with 10K devices and 500 app users per VM.
VM labels identify execution shards only; brand/cloud distribution remains in
the plan and inventory metadata.

The 100K multi-brand baseline uses
`loadtests/home-100k/scenarios/brand-plan-100k.json` as the data distribution
source of truth: 10 brand clouds, 5,000 normal member users, 10 developer
users, and 100,000 devices. Each brand cloud has one developer `owner` user;
runtime MQTT/app-command traffic is generated by member users.

| VM label | Device range | User range |
| --- | ---: | ---: |
| `lg01` | `0..19999` | `0..999` |
| `lg02` | `20000..39999` | `1000..1999` |
| `...` | `...` | `...` |
| `lg05` | `80000..99999` | `4000..4999` |

The plan's `vm_assignments` array is the source of truth. After
`provision-vms --live` writes `vms.json`, `sync --live` combines those VM
labels and IPs with the deterministic plan, then writes:

```text
<out-dir>/ansible/inventory.json
<out-dir>/ansible/extra-vars.json
<out-dir>/shard-manifests/<vm-label>.json
<out-dir>/credential-bundles/<vm-label>.sqlite.gz
<out-dir>/credential-bundles/<vm-label>.manifest.json
```

The Ansible inventory binds each provisioned VM IP to exactly one
`local_shard_manifest`. The remote runner receives only its own manifest at
`loadtests/home-100k/shard-manifests/current.json`, so a VM does not receive or
execute every device/user range.

### SQLite Credential Bundles

Per-device PEM fan-out is not used for Home 100K sync. Before Ansible runs,
`sync --live` creates one SQLite database per VM shard, compresses it as
`<vm-label>.sqlite.gz`, and writes a sha256 manifest next to it. The database
contains only the users, devices, credentials, and bindings assigned to that VM
from the staging test-data SQLite store. It is extracted on the VM under
`<remote-env-root>/loadtests/home-100k/credentials/`.

The runner reads device certificate/key/chain material directly from SQLite and
constructs TLS certificates from bytes. It must not require
`devices/test_device/devices/<type>/<id>/*.pem` or
`devices/test_device/bundles/<type>/<id>.pem` to exist on the load VM. This
keeps each VM sync to a small number of files, avoids tens of thousands of
inodes per shard, and lets future orchestra/coordinator reuse skip uploads when
the bundle sha256 has not changed.

The source of truth is `<env-root>/artifacts/test-data/<brand>-test-data.sqlite`.
For multi-brand runs, each source brand still owns its own SQLite DB, while the
per-VM shard bundle may contain rows from multiple brands. Bundle rows include
`brandname`, `brand_cloud_id`, and `tenant_slug`, so the runner can use the
correct tenant for app login/token refresh and the correct brand in MQTT
payloads. Shard manifests carry only shard metadata and do not duplicate
credentials.

## Public CLI Shape

Use the script wrapper as the operator entrypoint:

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
  path, status interval, ramp-up time, and target load size.

The `home-100k` directory, command, VM label prefix, and remote path are package
names. They do not define the active load size. The canonical target size is
configured by `HOME100K_DEVICES`, optional `HOME100K_USERS`, and
`HOME100K_DEVICES_PER_USER` in the description file. For example, the current
debug profile uses `HOME100K_DEVICES=9000`; switching back to a 100K run should
be a description-file change, not a filename or code change.

The default description file points at the existing provision-server staging
environment:

```text
cloud_env/staging/lke
```

The script keeps non-secret defaults in one place:

| Environment variable | Default |
| --- | --- |
| `HOME100K_DESCRIPTION_FILE` | `loadtests/home-100k/scenarios/default.description.env` |
| `HOME100K_SECRET_ENV_FILE` | `~/.env`, only `LINODE_TOKEN` is read |
| `HOME100K_ENV_ROOT` | `cloud_env/staging/lke` |
| `HOME100K_BRANDNAME` | `RTK` |
| `HOME100K_BRAND_PLAN` | unset; use `loadtests/home-100k/scenarios/brand-plan-100k.json` for the 100K multi-brand 7/7 baseline |
| `HOME100K_REGION` | `us-sea` |
| `HOME100K_RUN_ID` | Current UTC timestamp |
| `HOME100K_OUT_DIR` | `loadtests/home-100k/reports/<run-id>` |
| `HOME100K_SSH_KEY` | `~/.ssh/id_ed25519_rtkcloud` from the default description file |
| `HOME100K_SSH_USER` | `root` |
| `HOME100K_AUTHORIZED_KEY_FILE` | `~/.ssh/id_ed25519_rtkcloud.pub` from the default description file |
| `HOME100K_STATUS_INTERVAL_SECONDS` | `30` |
| `HOME100K_VM_LABEL_PREFIX` | `lg`; load-generator VM labels are `<prefix>01..<prefix>NN` |
| `HOME100K_STAGE_WARM_UP` | `30s` from the default description file |
| `HOME100K_STAGE_STEADY` | `90s` from the default description file |
| `HOME100K_STAGE_COOL_DOWN` | `30s` from the default description file |
| `HOME100K_DEVICES` | `9000` from the default description file |
| `HOME100K_USERS` | unset; planner derives `ceil(devices / devices-per-user)` |
| `HOME100K_DEVICES_PER_USER` | `20` from the default description file |
| `HOME100K_LOAD_GENERATOR_DEVICES_PER_VM` | `20000`; per-VM generator capacity used by the sizing formula |
| `HOME100K_VM_COUNT` | unset; planner derives `ceil(HOME100K_DEVICES / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)`, so the default 9K profile uses 1 VM and the 100K baseline uses 5 VMs |
| `HOME100K_RUNNER_NOFILE_LIMIT` | `1048576`; remote runner daemon file-descriptor limit for MQTT sockets |
| `HOME100K_MQTT_CONCURRENCY` | `1000`; per-VM-shard live MQTT connect worker concurrency |
| `HOME100K_COMMAND_CONCURRENCY` | `100`; per-VM-shard live shadow command concurrency |
| `HOME100K_SHADOW_COMMAND_TIMEOUT` | `30s`; per-phase shadow command wait timeout |
| `HOME100K_LIVE_RUNNER_TIMEOUT_GRACE` | unset; live shard command timeout defaults to `stage duration + max(10m, stage duration / 4)` |
| `HOME100K_FUNCTIONAL_SUCCESS_THRESHOLD_PERCENT` | `99.5`; MQTT connect, app ACK, delta, and convergence success threshold |
| `HOME100K_CLIENT_TARGET_COMPLETENESS_PERCENT` | `100`; active target devices/subscriptions and desired-write attempts must reach the planned target |
| `HOME100K_EXACT_EVENT_CORRELATION_PERCENT` | `100`; command stream/sequence evidence correlation threshold |
| `HOME100K_AGGREGATE_CORRELATION_TOLERANCE_PERCENT` | `0.1`; aggregate server/client counter sanity tolerance |
| `HOME100K_AGGREGATE_CORRELATION_MIN_TOLERANCE` | `5`; minimum aggregate counter tolerance |
| `HOME100K_MQTT_ADDR` | `auto-public-mqtt`; live commands discover public MQTT LoadBalancer IPs |
| `HOME100K_MQTT_PUBLIC_LB_COUNT` | `1`; limits auto-discovered MQTT LoadBalancers for the current 9K profile |
| `HOME100K_NODE_RESOURCE_STATUS` | `1` |
| `HOME100K_K8S_NODE_RESOURCE_STATUS` | `1` |
| `HOME100K_KUBECONFIG` | unset; falls back to existing LKE kubeconfig env or `<env-root>/state/lke-kubeconfig.yaml` |

Live VM lifecycle commands are also routed through the same script:

```sh
HOME100K_RUN_ID=20260615T100000Z \
  loadtests/home-100k/scripts/home-100k.sh

HOME100K_RUN_ID=20260615T100000Z \
  loadtests/home-100k/scripts/home-100k.sh destroy-vms --live --confirm-live
```

The default command is `workflow-live`. It runs the full paid/destructive VM
lifecycle and powers off reusable VMs only after a successful `PASS` report.
If the workflow fails, or the final report is `FAIL`/`INCOMPLETE`, VMs stay
available for inspection and `workflow-resume-live`. Run `shutdown-vms` when
debugging is finished, or `destroy-vms --live --confirm-live` only when the VM
pool should be deleted. During the live workflow the script
prints status immediately and every 30 seconds with the current phase, VM count,
collected shard count, server evidence state, report status, per load-generator
VM resource samples, and per Kubernetes node resource samples. Provisioned node
inventory is written to `loadtests/home-100k/reports/<run-id>/nodes.tsv`. The
same 30-second samples are persisted for report generation:

- `workflow-status.log`
- `resource-samples/load-vms.tsv`
- `resource-samples/k8s-nodes.tsv`

Kubernetes node resource samples use `kubectl top nodes --no-headers` and print
`[home-100k k8s-node]` lines. Kubeconfig resolution order is
`HOME100K_KUBECONFIG`, `RTK_CLOUD_LKE_KUBECONFIG`, `LKE_KUBECONFIG`,
`CLOUD_STAGING_K8S_KUBECONFIG`, then
`<env-root>/state/lke-kubeconfig.yaml`. Set
`HOME100K_K8S_NODE_RESOURCE_STATUS=0` to disable K8s node probing.

Stage duration belongs in the non-secret description file, not in `~/.env`.
The default profile uses `HOME100K_STAGE_WARM_UP=30s`,
`HOME100K_STAGE_STEADY=90s`, and `HOME100K_STAGE_COOL_DOWN=30s`, so the planned
load window is 150 seconds per stage and 10 minutes across the 25%, 50%, 75%,
and 100% stages before provisioning, sync, collection, and evidence overhead.
Short debug runs can lower these values with explicit shell environment
overrides or a custom `HOME100K_DESCRIPTION_FILE`; explicit shell environment
variables take precedence over the description file.

For large live runs, the runner allows extra time after the planned stage
duration for MQTT disconnect, cleanup, and `results.json` writes. The default
grace is `max(10m, duration / 4)`. Capacity experiment wrappers may set
`HOME100K_LIVE_RUNNER_TIMEOUT_GRACE` explicitly so that the grace value is
recorded in the experiment request artifact.

Capacity experiments also record user/device/bind setup concurrency. Keep these
values with the run record because data setup failures are not MQTT or node
capacity evidence.

Runner mode also belongs in the non-secret description file. The default is
`HOME100K_RUNNER_MODE=live`. In live mode, each shard invokes the copied
`rtk-cloud` runner and executes live `mqtt-test` traffic against the selected
env-root. It must not fall back to sampled in-memory actor flows. Use
`HOME100K_RUNNER_MODE=sample` only for local developer smoke tests.

Credential bundle format also belongs in the non-secret description file. The
only supported value is `HOME100K_CREDENTIAL_BUNDLE_FORMAT=sqlite-gzip`, which
means local sync preparation writes one per-VM SQLite database and uploads the
compressed database plus manifest instead of expanded per-device credential
files.

The device session model belongs in the same description file. The supported
live value is `HOME100K_DEVICE_SESSION_MODEL=lifetime-subscription`: devices
subscribe once after MQTT connect and keep their shadow delta subscription
until the shard runner exits, except for explicit offline/flapping scenarios.
The runner uses Go's network poller through normal `net.Conn` operations; it
does not hand-roll Linux `epoll`. Each active device MQTT connection owns one
bounded reader goroutine that dispatches subscribed shadow delta publishes to
the command flow. The goroutine lifetime is the device session lifetime, so the
upper bound is the shard's connected-device target plus a small number of
connect/app workers. `HOME100K_RUNNER_NOFILE_LIMIT` must be high enough for the
planned device and app MQTT sockets, and load-generator CPU, memory, disk, and
resource telemetry are part of the report gate.

Two load-generator runtime constraints are real capacity-test requirements, not
implementation preferences:

- File descriptor headroom: every live MQTT TCP/TLS connection consumes at
  least one file descriptor on the load generator. Small profiles such as 9K
  devices normally fit in one VM under the 20K-per-VM planner rule, while a
  100K-device profile plans five mixed VMs and can require tens of thousands
  of FDs per VM once device sessions, app MQTT sessions, logs, files, DNS, and
  SSH sockets are included. The default
  `HOME100K_RUNNER_NOFILE_LIMIT=1048576` is a `2^20` high-water limit used to
  keep the generator OS ceiling out of the server-capacity result. It does not
  create 1,048,576 connections; actual connection count is still controlled by
  the plan's device/user shard assignments. Operators may lower it for small
  profiles, but a run that hits `too many open files` is a load-generator
  failure and must be reported as `INCOMPLETE`.
- Sustained asynchronous reads: subscribed device sessions must keep reading
  MQTT traffic for their full online lifetime. The runner must not wait until a
  command is issued and then perform a one-shot blocking read on a shared MQTT
  connection. In Go this is implemented with the runtime netpoller plus one
  bounded reader goroutine per active device MQTT connection. This is required
  to model real shadow delta delivery and to avoid client-side read starvation
  being mistaken for EMQX, NodeBalancer, or IoT Device Shadow failure.

Load-generator start coordination is controlled by
`HOME100K_COORDINATOR_START_DELAY_MS`, default `3000`. This is not an absolute
wall-clock start time. The host coordinator first waits until every VM daemon is
ready, then sends the same START command to all VMs. Each VM waits the
configured delay using its local monotonic clock and records its actual stage
start and first-connect timestamps for report-time start skew calculation.

Device MQTT subscriptions are lifetime state, not scheduled publish events. In
live mode, each shard runner opens one device session pool during ramp-up and
keeps target connections and shadow delta subscriptions active through the
measurement window. The report tracks both new subscribe packets and active
connection/subscription gauges; capacity gates use the active gauges.
`HOME100K_MQTT_CONCURRENCY` controls client-side connect workers per VM shard.
It is a runner throughput knob, not a staged load target.
`HOME100K_COMMAND_CONCURRENCY` separately controls concurrent app/user shadow
commands per VM shard, and `HOME100K_SHADOW_COMMAND_TIMEOUT` controls each
shadow command wait phase. These do not change target connects or ramp-up time.

### LKE Capacity Placement

LKE pod and node counts are experiment-derived capacity controls, not fixed
defaults. The current model starts from
`required_mqtt_pods = ceil(devices / measured_safe_devices_per_mqtt_pod)` and
`required_nodes = max(cpu_nodes, memory_nodes, required_mqtt_pods, spread_min)`.
The seed value is 20,000 devices per MQTT pod, but it must be replaced by the
lowest successful coefficient from recorded capacity experiments.

For 100K planning, start with 5 load-generator VMs, 5 MQTT pods, and at least
5 general LKE nodes, then let `rtk-cloud provision --plan` raise the node count
if CPU requests, memory requests, or placement spread require more. If
PostgreSQL p95 CPU, memory, or I/O becomes the bottleneck, move it to a
dedicated larger node pool and record that placement in the experiment
artifacts. The source of truth for formulas and reviewable capacity records is
`docs/lke-capacity-sizing.md`.

The Go CLI remains the implementation entrypoint underneath the script:

```sh
go run ./loadtests/home-100k/cmd/home-100k -- plan \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region>

go run ./loadtests/home-100k/cmd/home-100k -- provision-vms \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --out-dir loadtests/home-100k/reports/<run-id>

go run ./loadtests/home-100k/cmd/home-100k -- sync \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- run-stages \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- collect \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --out-dir loadtests/home-100k/reports/<run-id>

go run ./loadtests/home-100k/cmd/home-100k -- collect-server-evidence \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- aggregate \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --out-dir loadtests/home-100k/reports/<run-id>

go run ./loadtests/home-100k/cmd/home-100k -- list-vms \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- destroy-vms \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --vm-state-file loadtests/home-100k/reports/<run-id>/vms.json

go run ./loadtests/home-100k/cmd/home-100k -- run \
  --env-root cloud_env/staging/lke \
  --brandname RTK \
  --region <linode-region> \
  --ephemeral-vms \
  --run-id <run-id> \
  --out-dir loadtests/home-100k/reports/<run-id> \
  --server-evidence-file loadtests/home-100k/reports/<run-id>/input-server-evidence.json
```

The lifecycle commands are dry-run/review-time commands by default. Live Linode
VM creation, shutdown, and deletion require `--live --confirm-live` plus a
Linode token.
When `provision-vms --live` receives `--out-dir`, it writes `vms.json`; pass
that file to `shutdown-vms --live --vm-state-file` after a normal run, or to
`destroy-vms --live --vm-state-file` only for intentional pool deletion.
Live provision accepts `--linode-type`, `--linode-image`, `--root-pass`, and
`--authorized-key-file`. The root password is sent only to the Linode create
API and is not written into `vms.json`, `results.json`, or the report.
`sync --live` reads the same `vms.json` and requires `--remote-workspace`,
`--remote-env-root`, and SSH options. It builds a Linux runner binary locally,
builds a Linux `rtk-cloud` binary for live MQTT/API traffic, generates the
Ansible inventory from provisioned VM IPs, writes per-VM shard manifests, and
runs `loadtests/home-100k/ansible/sync.yml`. The playbook copies only the
runner binaries, the assigned shard manifest, a per-shard
`home-100k/credentials/*.sqlite.gz` bundle, its sha256 manifest, and selected
env-root runtime metadata; it does not upload `reports/**`, `plans/**`, the
whole load-test source tree, or expanded per-device PEM directories.
`run-stages --live` reads `vms.json`, regenerates the same inventory, and runs
`loadtests/home-100k/ansible/start-runner.yml`. The playbook starts a
`home-100k runner-daemon` on each VM and waits only until the daemon reports
`READY_WAIT`. After that, the host coordinator waits for the full ready barrier,
sends `START(run_id, sequence, delay_ms)` to every VM, and each runner uses its
local monotonic clock for the final delay before opening MQTT/API traffic. Each
VM starts one `rtk-cloud mqtt-test` process for its target-connect slice, so
device MQTT sessions and shadow delta subscriptions remain alive for the run.
Each VM writes shard artifacts under
`--remote-out-root/<run-id>/<vm-label>/`. The
run-level `start-coordination.json` records ready barrier, configured start
delay, per-VM start timestamps, and max start skew.
`collect --live` reads `vms.json`, regenerates the same inventory, and runs
`loadtests/home-100k/ansible/collect.yml` to fetch each VM's `results.json`,
`TEST_REPORT.md`, `coordination.json`, runner daemon log, resource snapshot,
and sync telemetry into
`--out-dir/shards/<vm-label>/` and `--out-dir/sync-telemetry.d/`.
`collect-server-evidence --live` runs Kubernetes evidence probes with `kubectl`
for EMQX, Video Cloud API, PostgreSQL, Redis/Valkey, ingress/nginx, and
host/pod resources. Pod resource samples from `kubectl top pods -A` are parsed
into server evidence so the final report can list Postgres pod CPU/memory p95
by namespace and pod. Redis/Valkey evidence includes `INFO` counters such as
commands processed, keyspace hits/misses, connected clients, memory, keyspace
keys, and commandstats GET/SET calls when the cache is enabled. These counters
are required evidence for Redis-backed shadow hot state and `/request_token`
device/camera projection reads. IoT Device Shadow runtime-log
evidence comes from central
logger `/v1/logs` `device_runtime_log` events; the old PostgreSQL
`device_runtime_logs` table is a legacy deployment detail and is not required by
current 10K/100K reports.
Optional external HAProxy edge evidence is also collected when the selected
env-root contains `artifacts/edge-haproxy/edge-vms.json`. The central logger
query API is used when `services/cloud-logger/logger.env` is available in the
selected env-root.
For LKE environments where the logger config is intentionally stored elsewhere,
set `HOME100K_CLOUD_LOGGER_ENV` to the logger env file path or export
`HOME100K_CLOUD_LOGGER_ENDPOINT` and
`HOME100K_CLOUD_LOGGER_INGEST_TOKEN` before evidence collection. The endpoint
can be a `kubectl port-forward` URL such as `http://127.0.0.1:18090` when the
public logger ingress is not the desired evidence path; do not put logger
tokens directly in the description file.
It writes `server-evidence.json` when `--out-dir` is set. Partial probe failure is preserved as
`complete=false`, so the final report cannot become a false `PASS`.
`aggregate` reads `--out-dir/shards/*/results.json` plus
`--out-dir/server-evidence.json`, then writes run-level JSON artifacts. The
operator-facing `home-100k.sh aggregate` command runs
`scripts/generate-report.sh`, which reads `results.json`, server evidence,
sync telemetry, workflow status logs, and resource sample TSV files to render
the fixed-format `TEST_REPORT.md` from
`reports/templates/TEST_REPORT.md.tmpl`. Any shard with
`load_generator_health.saturated=true` forces `INCOMPLETE` so load-generator
saturation cannot be mistaken for server capacity.
`cleanup-home-100k-vms.sh` is the emergency cleanup helper for leftover Linode
test nodes. It scans Linode for VMs tagged with `home-100k`, the selected
`run_id`, and `load-generator`, prints id, label, region, status, IPv4
addresses, and tags, and is dry-run by default:

```sh
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh --run-id <run-id>
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh --run-id <run-id> --yes
```

Use `--prefix <value>` only when intentionally cleaning a different test
tag family.
`run` writes `plan.json`, `results.json`, `server-evidence.json`, and
`TEST_REPORT.md` to the selected output directory. Without a server evidence
file, the run is intentionally marked `INCOMPLETE`. The live shard execution may
wrap or reuse existing `rtk-cloud mqtt-loadtest` logic for MQTT transport, but
this package owns the 100K home scenario, VM lifecycle action plan, scenario
definitions, report schema, and server evidence contract.

## Report Requirements

Every report must include:

- `run_id`
- server target and deploy/version metadata
- test conditions
- home device mix
- device presence mix
- device scenario
- user scenario
- IoT Device Shadow scenario
- run-window results
- run-window diagnostics, including connect window, action window, connected
  before/after counts, command schedule counts, and skip reason when shadow
  actions were not attempted
- client target coverage for target connects, including target devices, actual MQTT
  connect/subscription counts, target users, and actual APP login counts
- Device MQTT totals
- APP/User totals
- shadow latency p50/p95/p99
- desired/reported convergence rate
- offline desired convergence rate
- delta-clear success rate
- duplicate apply count
- stale version conflict count
- rejected update count
- authorization violation count
- load-generator CPU, memory, network, and file-descriptor saturation
- server-side metrics/log evidence
- central logger runtime-log evidence for IoT Device Shadow stream correlation
- server/client counter correlation
- sync/provision telemetry with per-VM transfer bytes and remote disk snapshots
- load-generator VM resource timeline summary from
  `resource-samples/load-vms.tsv`
- per-Kubernetes-node resource timeline summary from
  `resource-samples/k8s-nodes.tsv`
- Postgres pod CPU/memory p95 from server `host_pod_resources` evidence
- Redis/Valkey INFO counters when shadow hot-state or token projection cache is
  enabled
- bottleneck assessment

If IoT Device Shadow evidence, MQTT broker evidence, APP/API evidence, parsed
server counters, or non-zero client totals cannot be collected, the report
status must be `INCOMPLETE`, not `PASS`. If the run completes but stage target
coverage is below the required success-rate threshold, the report status is
`COMPLETE` and the result is `FAIL`.

The default staging success-rate threshold is `99.5%` for stage connection,
subscription, APP desired-write, and APP ACK coverage.

If load-generator saturation invalidates the run, the report must say so
instead of attributing the bottleneck to the server.

## Metrics

Device-side metrics:

- MQTT connect success rate.
- MQTT reconnect count.
- Shadow get latency.
- Delta receive latency.
- Desired-to-reported apply latency.
- Offline desired convergence latency.
- Reported update accepted/rejected count.
- Version conflict count.
- Duplicate apply count.
- Per-device-type error classes.

User-side metrics:

- Login latency and success rate.
- App certificate bootstrap latency and success rate.
- App token request latency and success rate.
- Device-list latency.
- Shadow get latency.
- Desired update latency.
- Desired-to-reported convergence latency.
- Delta-clear latency.
- Authorization/cross-user access failures.

Server-side evidence:

- EMQX connected clients, churn, message rate, dropped clients, CPU, memory,
  and network throughput.
- Video Cloud API latency, error rate, and request-token latency.
- IoT Device Shadow HTTP latency and error rate.
- Shadow update/delta/reported throughput.
- PostgreSQL CPU, connections, locks, slow queries, and write I/O.
- Redis/Valkey shadow hot-state and token projection metrics if enabled.
- Redis/Valkey shadow hot-state and token projection metrics if enabled.
- External HAProxy edge process/socket evidence when edge artifacts are present.
- NATS/queue latency and pending messages if involved in the runtime path.
- Ingress/nginx upstream latency and errors.
- Host/pod CPU, memory, disk I/O, and network throughput.

## Security And Secret Handling

The synced env-root contains user credentials and the per-shard SQLite
credential bundle with device private keys and certificates. Load-generator VMs
must be treated as secret-bearing infrastructure.

Requirements:

- Use ephemeral VMs by default.
- Redact passwords, bearer tokens, private keys, certificate bodies, and raw
  secret env values from reports.
- Store only derived identifiers, hashes, counts, timing, and error classes in
  final reports.
- Scrub and destroy VMs after collection.
- Do not keep long-lived wildcard MQTT credentials on load-generator hosts.

## Validation Plan

Planner tests:

- 100K devices produce 5 device task shards.
- 5K users produce 5 deterministic user task shards.
- Planner creates 5 mixed VM assignments, each with one device task and one
  user task.
- Device mix resolves to the current `home-diverse-v1` profile, including
  lights, switches, smart plugs, HVAC, sensors, meters, locks, appliances, and
  gateways.
- Presence mix resolves to 85K online steady, 10K offline desired queue, and
  5K flapping reconnect.
- Planner resolves one target-connect window for the configured device count.

IoT Device Shadow scenario tests:

- Desired update creates delta.
- Reported update clears delta.
- Offline desired write converges after reconnect and shadow get.
- Flapping reconnect does not duplicate apply completed desired state.
- Device desired write is rejected.
- Stale version conflict is classified correctly.
- `clientToken` survives correlation and aggregation.

Report tests:

- Test conditions, device scenario, user scenario, and IoT Device Shadow
  scenario are always rendered.
- Offline-device scenario is always rendered.
- Missing shadow metrics marks report `INCOMPLETE`.
- Load-generator saturation prevents a false server-capacity `PASS`.
- Secrets are redacted.

Dry-run acceptance:

- The plan command produces a complete run plan without creating Linode
  resources.
- The generated plan is reviewable before running a paid or destructive test.

## Open Review Items

- Exact Linode region for the first baseline.
- VM instance type for `device-mqtt` and `user-app` roles.
- Target-window measurement duration and post-run collection duration.
- Offline duration distribution for the `offline desired queue` class.
- User desired-write rate per target window.
- Server-side metrics source for IoT Device Shadow hot path when Redis/Valkey is
  enabled or disabled.
