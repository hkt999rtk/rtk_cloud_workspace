# Linode 100K Home IoT Device Shadow Load Test

Status: live VM lifecycle and real MQTT/API shard runner wiring implemented;
IoT Device Shadow exact correlation still gates PASS
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

## Baseline

Default first baseline:

| Condition | Value |
| --- | --- |
| Server target | Current staging/LKE env-root |
| Load generators | Ephemeral Linode VMs |
| Region model | Single Linode region |
| Ramp-up time | Configured by `HOME100K_STAGE_WARM_UP` |
| Target connects | Configured by `HOME100K_DEVICES` |
| VM layout | Automatically planned as `ceil(HOME100K_DEVICES / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)` mixed execution shards unless `HOME100K_VM_COUNT` overrides it |
| Per-VM device task | Up to 20K simulated devices |
| Per-VM user task | Derived from the assigned device shard and `HOME100K_DEVICES_PER_USER` |
| Total load-generator VMs | Derived from the target and per-VM capacity; with the default 20,000 devices per VM, 100K plans 5 VMs while 9K or 1K plans 1 VM |

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
  path, status interval, ramp-up time, and target load size.

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
cloud_env/staging/runtime
```

## EMQX Capacity And Placement

The server target is EMQX on LKE. Before a formal 100K run, verify that the
staging server deployment has enough broker capacity and that broker pods are
spread across nodes:

- `MQTT_MIN_REPLICAS=9` in the selected environment's architecture override when preserving the validated nine-broker floor.
- At least 9 schedulable nodes labeled `rtk.io/node-class=broker`.
- The EMQX deployment uses hard pod anti-affinity by
  `kubernetes.io/hostname`.
- `mqtt-public` uses `externalTrafficPolicy: Local`.
- External HAProxy edge evidence must show HAProxy running with the expected
  NodePort upstreams for public `8883/TCP` before the load test starts.

Do not use `externalTrafficPolicy: Cluster` to compensate for uneven EMQX
placement. It can route public MQTT traffic through kube-proxy on nodes without
broker pods, which makes one node appear as the bottleneck instead of measuring
EMQX capacity.

Defaults:

| Environment variable | Default |
| --- | --- |
| `HOME100K_DESCRIPTION_FILE` | `loadtests/home-100k/scenarios/default.description.env` |
| `HOME100K_SECRET_ENV_FILE` | `~/.env`, only `LINODE_TOKEN` is read |
| `HOME100K_ENV_ROOT` | `cloud_env/staging/runtime` |
| `HOME100K_BRANDNAME` | `RTK` |
| `HOME100K_REGION` | `us-sea` |
| `HOME100K_RUN_ID` | Current UTC timestamp |
| `HOME100K_OUT_DIR` | `loadtests/home-100k/reports/<run-id>` |
| `HOME100K_SSH_KEY` | `~/.ssh/id_ed25519_rtkcloud` |
| `HOME100K_SSH_USER` | `root` |
| `HOME100K_AUTHORIZED_KEY_FILE` | `~/.ssh/id_ed25519_rtkcloud.pub` |
| `HOME100K_STATUS_INTERVAL_SECONDS` | `30` |
| `HOME100K_STAGE_WARM_UP` | `30s` |
| `HOME100K_STAGE_STEADY` | `90s` |
| `HOME100K_STAGE_COOL_DOWN` | `30s` |
| `HOME100K_DEVICES` | `9000` |
| `HOME100K_USERS` | unset; planner derives `ceil(devices / devices-per-user)` |
| `HOME100K_DEVICES_PER_USER` | `20` |
| `HOME100K_LOAD_GENERATOR_DEVICES_PER_VM` | `20000`; per-VM capacity used by the planner |
| `HOME100K_VM_COUNT` | unset; planner derives `ceil(HOME100K_DEVICES / HOME100K_LOAD_GENERATOR_DEVICES_PER_VM)` |
| `HOME100K_RUNNER_MODE` | `live` |
| `HOME100K_RUNNER_NOFILE_LIMIT` | `1048576`; remote runner daemon file-descriptor limit for MQTT sockets |
| `HOME100K_MQTT_CONCURRENCY` | `1000`; per-VM-shard live MQTT connect worker concurrency |
| `HOME100K_COMMAND_CONCURRENCY` | `100`; per-VM-shard live shadow command concurrency |
| `HOME100K_SHADOW_COMMAND_TIMEOUT` | `30s`; per-phase shadow command wait timeout |
| `HOME100K_FUNCTIONAL_SUCCESS_THRESHOLD_PERCENT` | `99.5`; MQTT connect, app ACK, delta, and convergence success threshold |
| `HOME100K_CLIENT_TARGET_COMPLETENESS_PERCENT` | `100`; active target devices/subscriptions and desired-write attempts must reach the planned target |
| `HOME100K_EXACT_EVENT_CORRELATION_PERCENT` | `100`; command stream/sequence evidence correlation threshold |
| `HOME100K_AGGREGATE_CORRELATION_TOLERANCE_PERCENT` | `0.1`; aggregate server/client counter sanity tolerance |
| `HOME100K_AGGREGATE_CORRELATION_MIN_TOLERANCE` | `5`; minimum aggregate counter tolerance |
| `HOME100K_CREDENTIAL_BUNDLE_FORMAT` | `sqlite-gzip` |
| `HOME100K_MQTT_ADDR` | `auto-public-mqtt`; live commands discover public MQTT LoadBalancer IPs |
| `HOME100K_MQTT_PUBLIC_LB_COUNT` | `1`; limits auto-discovered MQTT LoadBalancers for the current 9K profile |
| `HOME100K_NODE_RESOURCE_STATUS` | `1` |
| `HOME100K_K8S_NODE_RESOURCE_STATUS` | `1` |
| `HOME100K_KUBECONFIG` | unset; falls back to existing LKE kubeconfig env or `<env-root>/state/kubeconfig.yaml` |

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
HOME100K_RUN_ID=20260615T100000Z \
  loadtests/home-100k/scripts/home-100k.sh

HOME100K_RUN_ID=20260615T100000Z \
  loadtests/home-100k/scripts/home-100k.sh destroy-vms --live --confirm-live
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
`HOME100K_KUBECONFIG`, then the selected environment's normalized
`<runtime-root>/state/kubeconfig.yaml`. Set
`HOME100K_K8S_NODE_RESOURCE_STATUS=0` to disable K8s node probing.

Stage duration is part of the non-secret description file. The default profile
uses `HOME100K_STAGE_WARM_UP=30s`, `HOME100K_STAGE_STEADY=90s`, and
`HOME100K_STAGE_COOL_DOWN=30s`, which plans 150 seconds per stage and 10 minutes
for the four load stages before VM lifecycle and evidence overhead. Use explicit
shell environment overrides for longer baseline runs, or a custom
`HOME100K_DESCRIPTION_FILE` for a reusable alternate profile. Explicit shell
environment variables take precedence over the description file.

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
Live run-stages reads `vms.json`, regenerates the same inventory, and runs
`loadtests/home-100k/ansible/start-runner.yml`. Ansible starts a
`home-100k runner-daemon` on each VM and waits for `READY_WAIT`; it does not
perform the synchronized stage start. The host coordinator waits for the full
ready barrier, sends `START(run_id, sequence, delay_ms)` to every VM, and each
runner uses its local monotonic clock for the final delay before opening
MQTT/API traffic. The run writes `start-coordination.json` with ready barrier,
per-VM start timestamps, and max start skew. In `runner_mode=live`, each VM
calls the copied `rtk-cloud` binary once to run its assigned target-connect
MQTT/API shard only after START is received. Device MQTT delta subscriptions
are lifetime state: the runner ramps directly to the target connections and
keeps those sessions active through the measurement window. Reports must show
both new subscribe packets and active connection/subscription gauges.

Load-generator runtime limits are part of the test conditions:

- The remote runner daemon sets `ulimit -n` from
  `HOME100K_RUNNER_NOFILE_LIMIT`, default `1048576`. This value is file
  descriptor headroom, not a target connection count. Small targets such as 9K
  devices normally run on one automatically planned generator VM; 100K
  automatically plans five. The limit prevents either run from being
  invalidated by the generator's OS FD ceiling before EMQX,
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
PostgreSQL, required Redis/Valkey cache, ingress/nginx, host/pod resources, and
optional external HAProxy edge evidence. Partial required-probe failure is
written as `complete=false` evidence so the final report remains `INCOMPLETE`.
Aggregate reads collected shard results plus `server-evidence.json` and writes
run-level `plan.json` and `results.json`. The public `home-100k.sh aggregate`
path then calls `scripts/generate-report.sh` to render fixed-format
`TEST_REPORT.md`. Any shard with `load_generator_health.saturated=true` still
forces `INCOMPLETE`; insufficient client target coverage in an otherwise
completed run now produces `status=COMPLETE` and `result=FAIL`.
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

If IoT Device Shadow evidence, server evidence, parsed MQTT/API counters, or
resource telemetry is missing, the report status must be `INCOMPLETE`, not
`PASS`. If client target coverage is present but below the required `99.5%`
success-rate threshold, the report status is `COMPLETE` and result is `FAIL`.

If load-generator saturation invalidates the run, the report must say so
instead of attributing the bottleneck to the server.

## Relationship To The Legacy 10K MQTT Helper

The existing `rtk-cloud mqtt-loadtest` helper remains useful as a lower-level
MQTT shard and aggregation reference. The 100K home load test owns the product
scenario, IoT Device Shadow semantics, VM lifecycle, and report contract.
