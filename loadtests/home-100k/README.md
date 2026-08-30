# 100K Home IoT Device Shadow Load Test

Status: implemented live Home MQTT/shadow runner with optional WebRTC/TURN
video profiles
Owner: rtk_cloud_workspace
Last updated: 2026-07-05

## Summary

This package defines the Home IoT load-test suite for RTK cloud staging. The
default home profile validates simulated home devices, simulated app users, and
the IoT Device Shadow behavior that connects user intent to device-reported
state. Video-enabled profiles add a WebRTC/TURN relay-only viewer ladder on top
of the same MQTT/shadow background load.

The load generators run outside the server Kubernetes cluster. The default
runtime is ephemeral Linode VMs, but `workflow-live` can also consume
operator-provided existing generator hosts through the same script when Linode
active-service quota prevents creating new VMs. The server target for the first
baseline is the current staging/LKE environment. The first official baseline
uses one Linode region so capacity and bottleneck analysis are not mixed with
cross-region network variance.

A fresh Git clone does not include the secret and stateful files under
`cloud_env/<environment>/runtime/`. Before using another controller machine,
follow [`docs/prepare-another-machine.md`](docs/prepare-another-machine.md) to
restore or generate the prerequisites. Load-generator VMs receive a scoped
runtime bundle from `workflow-live` and should not receive a manually copied
operator runtime.

The test must prove more than MQTT connectivity. It must prove that IoT Device
Shadow desired, reported, delta, and version semantics still converge under
online, offline, and reconnect load. When a video profile is selected, the
report must also prove WebRTC create/setup/close, relay-only ICE candidates,
first RTP, H.264 RTP packet evidence, and external TURN/coturn evidence.

## Live workflow safety gates

`workflow-live` performs two gates before creating a load-generator VM:

1. `preflight` validates the selected fixture brand, device mix, user/device
   counts, current device/app certificates, and certificate chain against the
   current staging CA bundle.
2. A one-device actor-separated smoke validates token bootstrap and MQTT
   connectivity using the same fixture and CA bundle.

The CA bundle is resolved from `HOME100K_DEVICE_CLIENT_CA_BUNDLE` or
`<env-root>/state/pki/device-client-ca-bundle.pem`. A failed gate stops the
workflow with a stable classification such as `FIXTURE_MISMATCH`,
`CERTIFICATE_CA_MISSING`, or `TOKEN_BOOTSTRAP_FAILED` and no load-generator VM
is provisioned.

Run the gate explicitly when diagnosing a fixture or certificate change:

```sh
HOME100K_BRANDNAME=RTK-LOAD1K-20260717-DIVERSE \
./loadtests/home-100k/scripts/home-100k.sh preflight
```

Run-created VMs are destroyed automatically after collection and report
generation. Set `HOME100K_PRESERVE_VMS=1` only for an intentional investigation
or resume workflow. The default is safe cleanup. The host coordinator also
falls back to the VM's SSH loopback when the public runner control port is not
reachable; this avoids treating a public-port race as a load-test failure.

Common failure classifications include:

- `RUNNER_READY_BARRIER_FAILED`: one or more runners did not report the exact
  run as ready before the start deadline.
- `SHARD_RESULTS_MISSING`: the run did not produce a shard result to aggregate.
- `CLEANUP_FAILED`: run-created VM deletion needs operator follow-up.

Before running on a newly cloned workspace, prepare the staging runtime using
[`docs/staging-runtime-bootstrap.zh-TW.md`](../../docs/staging-runtime-bootstrap.zh-TW.md).
The runtime is intentionally not stored in Git.

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
- WebRTC, video relay, clip upload, snapshot upload, or media quality testing
  in the first MQTT-only baseline profile.
- Long-lived load-generator VM pools.

These can become later profiles after the first single-region baseline is
credible.

## MQTT 1K Validation Profile

`mqtt-1k.description.env` is the lightweight MQTT/Device Shadow validation
profile used after a new staging deployment. It defines 1,000 devices, 50
users, the full eleven-type `home-diverse-v1` inventory, one ephemeral mixed
load-generator VM, and a 150-second target stage.

Prepare matching data before running it; a four-type smoke inventory is not
compatible with this scenario. The complete new-environment sequence is in
[`../../docs/staging-from-scratch.md`](../../docs/staging-from-scratch.md).

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID=mqtt1k-$(date -u +%Y%m%dT%H%M%SZ) \
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

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

The 1K qualification runs that video runner on the live `us-sea` load-generator
VM (`HOME100K_VIDEO_LOADTEST_MODE=remote-sharded`) rather than on the operator
workstation. This keeps the relay-capacity measurement inside the staging
region; the functional threshold, 100 concurrent sessions, and relay-only
assertions remain unchanged. The global shard model uses the single 1K
generator and fails before launch if more than 100 viewers are assigned to it.

For `video-1k-v1`, missing WebRTC create/setup/close evidence makes the report
`INCOMPLETE`. A signaling success rate below the functional threshold is a
`FAIL`. If H.264 media is enabled, missing ICE-connected or first-RTP evidence
is also a `FAIL`. Missing external TURN/coturn evidence is `INCOMPLETE`, so a
signaling-only pass is never reported as media-capacity success.

## WebRTC/TURN Sizing Profiles

`video-50k-turn-v1` and `video-100k-turn-v1` keep the Home MQTT/shadow run as
background load and add a relay-only WebRTC viewer ladder for coturn sizing.
The video runner is still `e2e_test/video_cloud/load`; the home runner owns
orchestration, evidence collection, and the final `TEST_REPORT.md`.

Default TURN sizing shape:

| Profile | Home devices | Derived app users | Video devices | Viewer ladder | Media |
| --- | ---: | ---: | ---: | --- | --- |
| `video-50k-turn-v1` | 50,000 | 2,500 | 5,000 | `100,500,1000,2000,5000` | `h264` |
| `video-100k-turn-v1` | 100,000 | 5,000 | 5,000 | `100,500,1000,2000,5000` | `h264` |

The TURN sizing profiles use `HOME100K_VIDEO_LOADTEST_MODE=remote-sharded` and
`HOME100K_VIDEO_LOADTEST_SHARD_MODE=proportional`. Each ladder step is split
across the same live MQTT load-generator shards (`lgNN`): if a step requests
5,000 viewers in a 100,000-device run, each shard runs video for roughly 5% of
its own MQTT device inventory. The video runner fetches its own remote-sharded
artifacts under `video/step-<viewers>/`. The runner scheduling window is
`HOME100K_VIDEO_LOADTEST_DURATION=0s`, so each planned shard starts as a
concurrent batch after all remote hosts have been prepared. The per-session
media/relay hold time is
`HOME100K_VIDEO_LOADTEST_MEDIA_DURATION=10s`; this is the active TURN allocation
sample window used for coturn sizing evidence.

TURN sizing profiles allow both `turn:...?transport=udp` and
`turn:...?transport=tcp` ICE server URLs. `relay` means WebRTC must use TURN
relay candidates instead of direct host or srflx candidates; it does not mean
UDP-only.

The 100K TURN profile also sets
`HOME100K_DEVICE_TOKEN_REQUEST_RETRIES=1` for device MQTT bootstrap. This retry
is only for transient `POST /request_token` failures while ramping devices to the
target; it does not relax the 100% client target completeness gate. A report
with `device_token_attempts > connect_attempts` shows that retry was exercised,
and any remaining device token timeout must be treated as an API/token-bootstrap
capacity issue before interpreting coturn ladder results.

TURN sizing is only valid when generator CPU and memory remain below
saturation. The profile therefore sets
`HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST=1000` as an initial calibration
guardrail for the 5-generator / 5K-viewer largest step; in proportional mode,
any shard whose assigned video count exceeds that per-host limit fails before
the step starts. This is not a protocol limit.
The viewer path is intentionally protocol-only: it tracks first RTP, H.264/Opus
RTP packet/byte counters, selected candidate type, and startup latency. It must
not decode frames, parse every H.264 NAL, rebuild the full Annex-B bitstream for
every packet, or hash the whole received audio/video stream in the 100K sizing
path. Optional stricter media checks may be added for small calibration runs,
but they are not the default coturn sizing gate.

Do not add video-only generators for the default TURN sizing profile. Add them
only after a proportional mixed-shard report proves the existing load generators
saturated before coturn.

Run a full scripted 50K profile with:

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/video-50k-turn.description.env \
HOME100K_RUN_ID=lt50k-video-turn-$(date -u +%Y%m%dT%H%M%SZ) \
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

Run the 100K TURN sizing profile by switching the description file:

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/video-100k-turn.description.env \
HOME100K_RUN_ID=lt100k-video-turn-$(date -u +%Y%m%dT%H%M%SZ) \
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

If Linode active-service quota prevents creating new generator VMs, use the
same workflow with explicitly provided generator hosts instead of hand-editing
`vms.json`:

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/video-100k-turn.description.env \
HOME100K_RUN_ID=lt100k-video-turn-$(date -u +%Y%m%dT%H%M%SZ) \
HOME100K_EXISTING_GENERATOR_HOSTS='lg01=203.0.113.10,lg02=203.0.113.11,lg03=203.0.113.12,lg04=203.0.113.13,lg05=203.0.113.14' \
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

The labels must match the generated mixed shard assignments (`lgNN`). The
script writes `vms.json` with `source=existing-host`, syncs artifacts through
the normal Ansible path, and skips automatic shutdown/delete for those hosts.
Do not point this at Kubernetes server nodes or shared CI runners when the
result is meant to size staging capacity; doing so contaminates server-side or
CI capacity evidence. Only add separate `vgNN` video-only hosts for an explicit
follow-up generator-capacity experiment after proportional mixed shards prove
the generators are the bottleneck.

The workflow writes a remote-sharded video runner layout for larger viewer
steps:

```text
video/step-<viewers>/shard-01/load-results.json
video/step-<viewers>/shard-02/load-results.json
...
video/step-<viewers>/turn-active-samples.tsv
```

Each shard process keeps device websocket owners online and runs the
corresponding app/viewer WebRTC clients for its device slice. Device-side WebRTC
handling is an async state machine: the websocket listener only records
`webrtc_offer` receipt and enqueues answer preparation; bounded answer workers
POST the SDP answer and then release; bounded media workers wait for ICE and send
RTP for the configured media duration. The viewer records first RTP latency and
H.264 RTP packet evidence, keeps the session open for the configured media
duration, then closes it. The current TURN sizing profiles use a 10-second media
duration so generator CPU can be calibrated before longer sustained-stream
windows are attempted. Token maps and device ID files are uploaded as files
rather than expanded into long SSH command lines. Bearer tokens are passed
through environment files or the process environment; the workflow must not place
them in process argv.

Startup latency breakdown distinguishes elapsed checkpoints from ICE work:
`remote_answer_set` is the viewer-side SetRemoteDescription point, `ICE check`
is the Pion ICE checking interval, and `ICE connected since session start` is
the elapsed time from app request/session start to ICE connected. Pion phase
fields (`pion_create_peer`, `pion_create_offer`, `pion_create_answer`,
`pion_set_local_description`, `pion_ice_gathering_wait`, and
`pion_set_remote_description`) are phase durations, not elapsed checkpoints,
and are emitted in raw evidence plus the startup latency report. Device-side
async queue fields (`answer_queue_wait`, `answer_prepare`, `answer_post`,
`device_ice_wait`, `sender_queue_wait`, and `sender_first_write_after_ice`) are
also reported so an operator can distinguish answer-worker backlog, Pion answer
creation, HTTP answer POST latency, sender backlog, device-side ICE wait, and
TURN/media path delay. Reports must not collapse those fields into a single
ambiguous `ICE connect` number.

The final report gates are intentionally strict:

- missing 50K/100K MQTT/shadow evidence: `INCOMPLETE`
- missing WebRTC create/setup/close evidence: `INCOMPLETE`
- relay-only run with selected non-relay candidates: `FAIL`
- missing external TURN/coturn active-window evidence: `INCOMPLETE`
- missing first RTP or H.264 RTP packet evidence with H.264 media: `FAIL`
- missing multi-pod WebRTC signaling-store evidence for TURN sizing profiles:
  `INCOMPLETE`

Server-side shadow correlation is sourced from the Loki-backed central logger
`device_runtime_log` events. EMQX connection correlation prefers run-scoped
counters; when a stale baseline delta undercounts a live run, the report may use
the active `emqx.metric.client.connected` gauge as warning-level evidence.

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
| Load-generator runtime | Ephemeral Linode VMs by default; script-provided existing hosts when `HOME100K_EXISTING_GENERATOR_HOSTS` is set |
| First baseline region model | Single Linode region |
| Device-generator density | Configured by `HOME100K_LOAD_GENERATOR_DEVICES_PER_VM`; default 20,000 devices per VM for MQTT-only and the 100K TURN sizing profile |
| Load-generator VM count | Automatically planned as `ceil(HOME100K_DEVICES / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)` unless `HOME100K_VM_COUNT` overrides it |
| Per-VM device task | Up to `HOME100K_LOAD_GENERATOR_DEVICES_PER_VM` devices |
| Per-VM user task | Derived from the assigned device shard and `HOME100K_DEVICES_PER_USER` |
| Video shard model | `HOME100K_VIDEO_LOADTEST_SHARD_MODE=proportional`; video devices/viewers are selected from each VM's own MQTT shard inventory |
| Video-only VM count | Optional exception via `HOME100K_VIDEO_GENERATOR_VM_COUNT`; not used by default TURN sizing profiles |
| Total load-generator VM count | 5 mixed hosts for the default 100K-device/5K-user MQTT baseline; TURN sizing profiles fail fast if a proportional shard exceeds `HOME100K_VIDEO_LOADTEST_MAX_VIEWERS_PER_HOST` |
| Ramp-up time | Configured by `HOME100K_STAGE_WARM_UP` |
| Target connects | Configured by `HOME100K_DEVICES` |

Before starting a live 100K TURN sizing run, inspect the selected environment's
resolved deployment plan and provider preflight. Its projected active services
must cover all logical node classes, edge/TURN resources, load generators, and
unrelated provider resources that remain active. Provider quota and instance
types belong to the selected deployment adapter; the load test does not compute
them from provider-specific environment variables.

The test should use deterministic sharding. A run ramps directly to the target
connection count; there is no staged 25%/50%/75%/100% load model.

## Server-Side Capacity Prerequisites

The selected Kubernetes environment must run MQTT on EMQX. Public MQTT load testing should
use an EMQX node pool sized so each broker pod can land on a different
Kubernetes node. For the 100K baseline this means:

- `MQTT_MIN_REPLICAS=9` in the environment architecture override when preserving the validated nine-broker floor.
- A dedicated or otherwise uncongested node pool with at least 9 schedulable
  nodes labeled `rtk.io/node-class=broker`.
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

### Tenant-aware MQTT and runtime-log evidence

Load generators publish and subscribe only to logical topics such as
`devices/{device_id}/...` and `$vc/devices/{device_id}/shadow/...`. They never
put `_bc/{brand_cloud_id}` into a client topic: EMQX derives that physical
namespace from the JWT-backed MQTT connection identity. The issued Video Cloud
token supplies the MQTT username and client-id base; the token is never copied
to reports or traces.

`SUCCESS` also requires structured central-logger runtime-log evidence. Each
runtime log must include device id, stream id, positive sequence, source, and
message so the runner can correlate it with the shadow command. Missing or
unparseable logger evidence is `INCOMPLETE`; a tenant/device mismatch is
`FAIL`.

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
  Linode token from `--linode-token` or `LINODE_TOKEN`. When
  `--existing-hosts` is provided, it writes the same `vms.json` state from
  `label=ipv4` entries and skips Linode VM creation.
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
- `shutdown-vms` powers off reusable load-generator VMs after collection.
  Existing-host entries are marked with `source=existing-host` and are skipped
  by `shutdown-vms` and `destroy-vms`.
- `workflow-live` and `workflow-resume-live` automatically shut down VMs only
  when the workflow return code is `0` and the rendered report status is
  `SUCCESS`. Failed, `FAIL`, or `INCOMPLETE` runs preserve VMs by default for
  resume/debug; existing-host entries are never powered off by this workflow.
  Set `HOME100K_SHUTDOWN_ON_ERROR=1` only when the operator wants automatic
  shutdown even after a failed or incomplete run.

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

- The selected environment SecretStore supplies `LINODE_TOKEN`.
- `loadtests/home-100k/scenarios/default.description.env` supplies the
  non-secret test description: environment, brand, region, remote paths, SSH key
  path, status interval, ramp-up time, and target load size.

The `home-100k` directory, command, VM label prefix, and remote path are package
names. They do not define the active load size. The canonical target size is
configured by `HOME100K_DEVICES`, optional `HOME100K_USERS`, and
`HOME100K_DEVICES_PER_USER` in the description file. For example, the current
debug profile uses `HOME100K_DEVICES=9000`; switching back to a 100K run should
be a description-file change, not a filename or code change.

The default description selects the staging environment. The runner resolves
its normalized runtime without knowing which deployment adapter implements it:

```text
HOME100K_ENVIRONMENT=staging
```

The script keeps non-secret defaults in one place:

| Environment variable | Default |
| --- | --- |
| `HOME100K_DESCRIPTION_FILE` | `loadtests/home-100k/scenarios/default.description.env` |
| `HOME100K_LINODE_TOKEN_FILE` | Optional override; defaults to `<config-root>/<environment>/operator/env/LINODE_TOKEN` |
| `HOME100K_ENVIRONMENT` | `staging`; resolves `cloud_env/staging/runtime` |
| `HOME100K_ENV_ROOT` | internal/custom runtime override only |
| `HOME100K_BRANDNAME` | `RTK` |
| `HOME100K_BRAND_PLAN` | unset; use `loadtests/home-100k/scenarios/brand-plan-100k.json` for the 100K multi-brand 7/7 baseline |
| `HOME100K_REGION` | Explicit test-only override; normally read from normalized environment provider preflight state. |
| `HOME100K_LINODE_TYPE` | unset; optional Linode VM type for load generators. TURN sizing profiles use `g6-standard-6` |
| `HOME100K_EXISTING_GENERATOR_HOSTS` | unset; comma/space separated `label=ipv4` entries such as `lg01=203.0.113.10,lg02=203.0.113.11`. When set, `workflow-live` skips Linode VM creation and uses these hosts as the generated `vms.json` |
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
| `HOME100K_DEVICE_TOKEN_REQUEST_TIMEOUT` | `10s`; per-attempt device `/request_token` timeout during MQTT bootstrap |
| `HOME100K_DEVICE_TOKEN_REQUEST_RETRIES` | `0`; bounded retry count after the first device `/request_token` attempt. The 100K TURN profile overrides this to `1` |
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
| `HOME100K_KUBECONFIG` | unset; falls back to existing LKE kubeconfig env or `<env-root>/state/kubeconfig.yaml` |

Live VM lifecycle commands are also routed through the same script:

```sh
HOME100K_RUN_ID=20260615T100000Z \
  loadtests/home-100k/scripts/home-100k.sh

HOME100K_RUN_ID=20260615T100000Z \
  loadtests/home-100k/scripts/home-100k.sh destroy-vms --live --confirm-live
```

The default command is `workflow-live`. It runs the full paid/destructive VM
lifecycle and powers off reusable VMs only after a successful `SUCCESS` report.
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
- `linode-active-service-preflight.json`
- `resource-samples/load-vms.tsv`
- `resource-samples/k8s-nodes.tsv`

`workflow-local-live` is the bounded canary variant used by isolated nightly
runtime coverage. It runs the same formal MQTT actor, video/clip runners,
server evidence collection, correlation, and aggregate report locally, without
provisioning or SSHing into a load-generator VM. It requires
`HOME100K_DEVICES` to be at most `HOME100K_LOCAL_LIVE_MAX_DEVICES` (default
100); qualification and capacity profiles continue to use `workflow-live`.

For focused WebRTC/TURN investigation, use `workflow-video-live` or
`workflow-video-resume-live`. These commands keep the same scripted VM
provision, sync, host preparation, server evidence, token generation, and
remote-sharded video runner path, but intentionally skip `run-stages` and do
not run the MQTT/shadow lifecycle. They are intended for quickly reproducing
Pion/ICE/TURN bottlenecks after device inventory and token prerequisites are
available.

Kubernetes node resource samples use `kubectl top nodes --no-headers` and print
`[home-100k k8s-node]` lines. Kubeconfig resolution order is
`HOME100K_KUBECONFIG`, then the selected environment's normalized
`<runtime-root>/state/kubeconfig.yaml`. Set
`HOME100K_K8S_NODE_RESOURCE_STATUS=0` to disable K8s node probing.

Load-generator samples are collected through SSH from `/proc/stat`,
`/sys/class/net`, `free`, and `df`; the report renders per-VM CPU, memory,
disk, and RX/TX network utilization so generator saturation cannot be confused
with server-side capacity.

`workflow-live` writes `linode-active-service-preflight.json` before creating
load-generator VMs. It records current active Linodes, missing edge/coturn VMs,
planned load-generator VMs, and projected total active services. Set
The selected adapter's quota setting makes this a hard fail-fast gate when the
projected total exceeds the known account limit.

Stage duration belongs in the non-secret description file, not in SecretStore.
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
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region>

go run ./loadtests/home-100k/cmd/home-100k -- provision-vms \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --out-dir loadtests/home-100k/reports/<run-id>

go run ./loadtests/home-100k/cmd/home-100k -- sync \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- run-stages \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- collect \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --out-dir loadtests/home-100k/reports/<run-id>

go run ./loadtests/home-100k/cmd/home-100k -- collect-server-evidence \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- aggregate \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --out-dir loadtests/home-100k/reports/<run-id>

go run ./loadtests/home-100k/cmd/home-100k -- list-vms \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id>

go run ./loadtests/home-100k/cmd/home-100k -- destroy-vms \
  --env-root cloud_env/staging/runtime \
  --brandname RTK \
  --region <linode-region> \
  --run-id <run-id> \
  --vm-state-file loadtests/home-100k/reports/<run-id>/vms.json

go run ./loadtests/home-100k/cmd/home-100k -- run \
  --env-root cloud_env/staging/runtime \
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
