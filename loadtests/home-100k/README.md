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
| Devices per user | 10 |
| Server target | Current staging/LKE env-root |
| Load-generator runtime | Ephemeral Linode VMs |
| First baseline region model | Single Linode region |
| Device-generator density | Up to 10,000 devices per VM |
| Load-generator VM count | 10 `mixed` VMs |
| Per-VM device task | 10,000 devices |
| Per-VM user task | 500 users |
| Total load-generator VM count | 10 for the default 100K-device/5K-user mixed baseline |
| Stage windows | 25K, 50K, 75K, 100K connected devices |

The test should use deterministic sharding. Each stage must have its own
warm-up, steady-state, and cool-down windows, and each stage must have an
independent pass/fail/incomplete result.

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

- Connect to MQTT and remain online during the selected stage window.
- Subscribe to shadow delta topics before user desired writes.
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

- Disconnect and reconnect during stage windows.
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
plan -> provision-vms -> sync -> run-stages -> collect -> collect-server-evidence -> aggregate -> destroy-vms
```

Required behavior:

- `plan` prints VM count, role layout, shard ranges, stage windows, scenario
  mix, expected artifacts, server evidence queries, and cleanup plan.
- `provision-vms` creates ephemeral Linode VMs in the selected region. It is a
  dry-run by default; live provisioning requires `--live --confirm-live` and a
  Linode token from `--linode-token` or `LINODE_TOKEN`.
- `sync` generates an Ansible inventory from `vms.json`, writes one shard
  manifest per VM, and copies only the Linux runner binary, the VM's manifest,
  and minimal env-root artifacts.
- `run-stages` uses the generated Ansible inventory to start device and user
  shards with a shared `run_id`. Formal runs must use `runner_mode=live`;
  sampled actor execution is allowed only for developer smoke tests and must
  not be reported as capacity evidence.
- `collect` uses the generated Ansible inventory to retrieve per-VM results,
  sync telemetry, and local load-generator telemetry.
- `collect-server-evidence` queries server metrics/logs for the same `run_id`
  and stage time windows.
- `aggregate` reads collected shard results plus `server-evidence.json` and
  writes run-level `plan.json` and `results.json`. The public script then runs
  `scripts/generate-report.sh` to render `TEST_REPORT.md` from the fixed
  template and collected artifacts.
- `list-vms` lists leftover load-generator VMs by `home-100k` and `run_id`
  tags when cleanup needs review.
- `destroy-vms` scrubs and deletes load-generator VMs.

If a run fails before cleanup, the tooling must be able to list and destroy
leftover load-generator VMs by tag/run id.

### Device/User Shard Assignment

The default baseline creates 10 mixed VM assignments. Each assignment owns one
device shard and one user shard:

| VM label | Device range | User range |
| --- | ---: | ---: |
| `home-100k-mixed-000` | `0..9999` | `0..499` |
| `home-100k-mixed-001` | `10000..19999` | `500..999` |
| `...` | `...` | `...` |
| `home-100k-mixed-009` | `90000..99999` | `4500..4999` |

The plan's `vm_assignments` array is the source of truth. After
`provision-vms --live` writes `vms.json`, `sync --live` combines those VM
labels and IPs with the deterministic plan, then writes:

```text
<out-dir>/ansible/inventory.json
<out-dir>/ansible/extra-vars.json
<out-dir>/shard-manifests/<vm-label>.json
```

The Ansible inventory binds each provisioned VM IP to exactly one
`local_shard_manifest`. The remote runner receives only its own manifest at
`loadtests/home-100k/shard-manifests/current.json`, so a VM does not receive or
execute every device/user range.

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
  path, and status interval.

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
| `HOME100K_REGION` | `us-sea` |
| `HOME100K_RUN_ID` | Current UTC timestamp |
| `HOME100K_OUT_DIR` | `loadtests/home-100k/reports/<run-id>` |
| `HOME100K_SSH_USER` | `root` |
| `HOME100K_AUTHORIZED_KEY_FILE` | `<HOME100K_SSH_KEY>.pub` |
| `HOME100K_STATUS_INTERVAL_SECONDS` | `30` |
| `HOME100K_STAGE_WARM_UP` | `1m` |
| `HOME100K_STAGE_STEADY` | `2m` |
| `HOME100K_STAGE_COOL_DOWN` | `45s` |
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
lifecycle and destroys the provisioned VMs after report generation. If the
workflow is interrupted, rerun `destroy-vms --live --confirm-live` with the same
`HOME100K_RUN_ID` to clean any leftovers. During the live workflow the script
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
The default debug profile uses `HOME100K_STAGE_WARM_UP=15s`,
`HOME100K_STAGE_STEADY=45s`, and `HOME100K_STAGE_COOL_DOWN=15s`, so the planned
window is 75 seconds per stage and 5 minutes across the 25K, 50K, 75K, and
100K stages before provisioning, sync, collection, and evidence overhead.
Short review runs can lower these values in
`loadtests/home-100k/scenarios/default.description.env` or a custom
`HOME100K_DESCRIPTION_FILE`.

Runner mode also belongs in the non-secret description file. The default is
`HOME100K_RUNNER_MODE=live`. In live mode, each shard invokes the copied
`rtk-cloud` runner and executes live `mqtt-test` traffic against the selected
env-root. It must not fall back to sampled in-memory actor flows. Use
`HOME100K_RUNNER_MODE=sample` only for local developer smoke tests.

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
VM creation and deletion require `--live --confirm-live` plus a Linode token.
When `provision-vms --live` receives `--out-dir`, it writes `vms.json`; pass
that file to `destroy-vms --live --vm-state-file` for cleanup.
Live provision accepts `--linode-type`, `--linode-image`, `--root-pass`, and
`--authorized-key-file`. The root password is sent only to the Linode create
API and is not written into `vms.json`, `results.json`, or the report.
`sync --live` reads the same `vms.json` and requires `--remote-workspace`,
`--remote-env-root`, and SSH options. It builds a Linux runner binary locally,
builds a Linux `rtk-cloud` binary for live MQTT/API traffic, generates the
Ansible inventory from provisioned VM IPs, writes per-VM shard manifests, and
runs `loadtests/home-100k/ansible/sync.yml`. The playbook copies only the
runner binaries, the assigned shard manifest, and selected env-root artifacts;
it does not upload `reports/**`, `plans/**`, or the whole load-test source
tree.
`run-stages --live` reads `vms.json`, regenerates the same inventory, and runs
`loadtests/home-100k/ansible/run-stages.yml`. Each VM writes shard artifacts
under `--remote-out-root/<run-id>/<vm-label>/`. The Ansible vars include
`runner_mode`; `live` mode invokes `rtk-cloud mqtt-test` for real MQTT/API
traffic and refuses sampled actor fallback.
`collect --live` reads `vms.json`, regenerates the same inventory, and runs
`loadtests/home-100k/ansible/collect.yml` to fetch each VM's `results.json`,
`TEST_REPORT.md`, resource snapshot, and sync telemetry into
`--out-dir/shards/<vm-label>/` and `--out-dir/sync-telemetry.d/`.
`collect-server-evidence --live` runs Kubernetes evidence probes with `kubectl`
for EMQX, Video Cloud API, IoT Device Shadow, PostgreSQL, Redis/Valkey,
ingress/nginx, and host/pod resources. It writes `server-evidence.json` when
`--out-dir` is set. Partial probe failure is preserved as
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
test nodes. It scans Linode for VMs whose label starts with `home-100k` or
whose tags include `home-100k`, prints id, label, region, status, IPv4
addresses, and tags, and is dry-run by default:

```sh
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh --yes
```

Use `--prefix <value>` only when intentionally cleaning a different test
prefix.
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
- per-stage results
- client target coverage by stage, including target devices, actual MQTT
  connect/subscription counts, target users, and actual APP login counts
- Device MQTT totals by stage and total
- APP/User totals by stage and total
- per-stage shadow latency p50/p95/p99
- desired/reported convergence rate
- offline desired convergence rate
- delta-clear success rate
- duplicate apply count
- stale version conflict count
- rejected update count
- authorization violation count
- load-generator CPU, memory, network, and file-descriptor saturation
- server-side metrics/log evidence
- server/client counter correlation
- sync/provision telemetry with per-VM transfer bytes and remote disk snapshots
- load-generator VM resource timeline summary from
  `resource-samples/load-vms.tsv`
- per-Kubernetes-node resource timeline summary from
  `resource-samples/k8s-nodes.tsv`
- bottleneck assessment

If IoT Device Shadow evidence, MQTT broker evidence, APP/API evidence, parsed
server counters, non-zero client totals, or stage target coverage cannot be
collected, the report status must be `INCOMPLETE`, not `PASS`.

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
- Redis/Valkey shadow hot-state metrics if enabled.
- NATS/queue latency and pending messages if involved in the runtime path.
- Ingress/nginx upstream latency and errors.
- Host/pod CPU, memory, disk I/O, and network throughput.

## Security And Secret Handling

The synced env-root contains user credentials, device private keys,
certificates, and tokens. Load-generator VMs must be treated as secret-bearing
infrastructure.

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

- 100K devices produce 10 device task shards.
- 5K users produce 10 deterministic user task shards.
- Planner creates 10 mixed VM assignments, each with one device task and one
  user task.
- Device mix resolves to 50K lights, 20K air conditioners, and 30K smart
  meters.
- Presence mix resolves to 85K online steady, 10K offline desired queue, and
  5K flapping reconnect.
- Stages resolve to 25K, 50K, 75K, and 100K windows.

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
- Stage duration, warm-up duration, and cool-down duration.
- Offline duration distribution for the `offline desired queue` class.
- User desired-write rate per stage.
- Server-side metrics source for IoT Device Shadow hot path when Redis/Valkey is
  enabled or disabled.
