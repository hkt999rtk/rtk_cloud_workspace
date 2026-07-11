# LKE Capacity Sizing Model

This document records how staging capacity experiments translate a target device
count into load-generator VMs, MQTT pods, LKE nodes, and resource requests. The
numbers here are conservative planning coefficients, not one-off theoretical
limits. A coefficient becomes usable only after a complete load-test report says
`Status: COMPLETE`, `Result: SUCCESS`, server correlation passes, runtime log
correlation passes, and load-generator saturation is false.

## Formula

For each experiment:

- `users = ceil(devices / devices_per_user)`
- `load_generator_vms = ceil(devices / load_generator_devices_per_vm)`
- `required_mqtt_pods = max(mqtt_min_replicas, ceil(target_connections / measured_safe_connections_per_mqtt_pod))`
- `required_api_pods = max(api_min_replicas, ceil(active_devices / active_devices_per_api_pod))`
- `usable_node_cpu = node_allocatable_cpu - system_reserved_cpu`
- `usable_node_mem = node_allocatable_mem - system_reserved_mem`
- `cpu_nodes = ceil(sum(workload_cpu_requests * replicas) / usable_node_cpu)`
- `memory_nodes = ceil(sum(workload_memory_requests * replicas) / usable_node_mem)`
- `required_nodes = max(cpu_nodes, memory_nodes, required_mqtt_pods, spread_min)`
- `provider_active_services = nodes + postgres_volumes + edge_vms`

MQTT memory is now a first-class sizing input. A run that OOMKills MQTT pods is
not a broker-capacity success even if most device connections were established.
Record provider-neutral MQTT request/limit memory and, when changed, the
adapter-specific EMQX heap limit in the experiment request before using
that run as evidence.

Load-generator VM count is formula-driven, not a fixed profile value. The
current planning input is `load_generator_devices_per_vm=20000`, so the wrapper
computes `ceil(target_devices / 20000)`. If a run proves that input unsafe, update
the input and rerun; do not hard-code a one-off VM count into the runbook.

Logger memory is also part of capacity evidence. Runtime-log correlation is a
hard gate, so `LKE_CLOUD_LOGGER_REQUEST_MEMORY` and
`LKE_CLOUD_LOGGER_LIMIT_MEMORY` must be recorded when cloud-logger sizing is
changed. For 50K and larger runs, the current evidence-profile default is
`LKE_CLOUD_LOGGER_REQUEST_MEMORY=4Gi` and
`LKE_CLOUD_LOGGER_LIMIT_MEMORY=8Gi`. A smaller logger limit can make a
server-capacity run unusable even when MQTT, API, and WebRTC client paths mostly
work, because the report cannot prove runtime shadow evidence after the logger
OOMs.
Cloud Logger must use Loki-backed storage in staging. Store selection is not a
sizing knob and must not be reintroduced as an in-process fallback; use
`RTK_CLOUD_LOGGER_LOKI_URL` only to point at the private or external Loki base
URL.

The initial seed values are:

- `measured_safe_devices_per_generator_vm = 20000`
- `measured_safe_devices_per_mqtt_pod = 20000`
- `devices_per_user = 20`

The 20K-per-MQTT-pod value is only an optimistic seed until validated at larger
targets. It must be reduced if MQTT connect/dial failures, EMQX listener
shutdowns, broken pipes, or uneven broker load appear before API/Postgres or
load-generator saturation.

## Experiment Record

Every capacity run must be launched through
`scripts/run-lke-capacity-experiment.sh`. The wrapper calls the existing
project scripts in order:

1. `scripts/destroy-linode-staging-resources.sh`
2. `scripts/provision-staging.sh`
3. `scripts/setup-staging-e2e-data.sh`
4. `loadtests/home-100k/scripts/home-100k.sh workflow-live`
5. `rtk-cloud lke-capacity-run-summary`

Each dry-run writes the requested plan. Each live run additionally snapshots the
stack config before and after applying the experiment sizing:

- `<env-root>/artifacts/capacity-experiments/<run-id>/request.json`
- `<env-root>/artifacts/capacity-experiments/<run-id>/stack.env.before`
- `<env-root>/artifacts/capacity-experiments/<run-id>/stack.env.applied`
- `<env-root>/artifacts/capacity-experiments/<run-id>/capacity-run-summary.json`
- `loadtests/home-100k/reports/<run-id>/TEST_REPORT.md`
- `loadtests/home-100k/reports/<run-id>/results.json`

The wrapper applies the reference sizing formulas when explicit overrides are
omitted:

- `users = ceil(target_devices / devices_per_user)`
- `load_generator_vms = ceil(target_devices / load_generator_devices_per_vm)`
- `mqtt_pods = ceil(target_devices / mqtt_connections_per_pod)`
- `node_count = mqtt_pods` as the default spread minimum

`--mqtt-pods` and `--node-count` remain explicit overrides for reduction tests
or CPU/memory-driven sizing experiments. The default `node_count` calculation is
intentionally conservative and spread-based; CPU and memory packing must still
be reviewed from the provision plan before treating a smaller node count as
safe.

The wrapper also records `live_runner_timeout_grace`. This protects larger
runs from killing a shard immediately after the configured MQTT duration while
the runner is still disconnecting clients and writing `results.json`.
When a run needs to delete known orphan PVC Block Storage volumes before
provisioning, pass exact IDs through `--cleanup-orphan-volume-ids`; the request
records the list and the cleanup script still requires explicit
`--include-orphan-volumes` behavior internally.
Data setup concurrency is recorded as well. The capacity wrapper defaults to
`user_concurrency=16` and `device_concurrency=16` because the setup path uses
local kubectl port-forwards and is not the runtime capacity target. Local
factory enrollment through kubectl port-forward produced client-side header
timeouts at 64-way device concurrency during
`cap50k-timeoutfix-20260622T170416Z`, and user setup at 64-way concurrency hit
a transient account-manager port-forward disconnect during
`cap100k-assignmentfix-20260623T012916Z`. Slower setup is acceptable; only the
`workflow-live` phase should be used for runtime capacity coefficients.
The wrapper also records `factory_enroll_ports`. Capacity experiments use
multiple local factory-enroll port-forwards by default
(`18443,18444,18445,18446`) so data setup can distribute device enrollment
requests across local forwarding processes. This is a setup-path control, not a
server capacity coefficient; if a run aborts before `workflow-live`, it must not
be used to derive MQTT pod, node, or generator sizing.
The wrapper records `data_setup_retries` as well. The first data setup attempt
uses `--no-resume` after a fresh cleanup/provision. If local port-forward or
network transport fails during long setup, later attempts use `--resume` against
the same users/devices/out-dir and save failed attempt logs under the capacity
artifact directory. This retry policy is only for setup transport reliability;
it does not relax runtime success gates.

The summary row includes target devices, users, node type/count, MQTT replicas,
pass/fail status, bottleneck classification, request token counters, MQTT
counters, APP ACK counters, server/runtime log correlation, load-generator
resource usage, K8s node usage, selected pod resource usage, and live K8s pod
restart/OOM status when the env-root kubeconfig is still available. Load
generator VM count is always recomputed from the formula and request input; do
not copy it from a previous row.

## Current Evidence

| Run ID | Devices | Users | MQTT pods | Nodes | Node type | Result | Safe coefficient |
| --- | ---: | ---: | ---: | ---: | --- | --- | --- |
| `lt1k-20260622T134506Z` | 1000 | 50 | 2 | 2 | `g6-standard-2` | `SUCCESS` | Confirms script correctness only; too small for 100K coefficient |
| `cap2k-multiport-20260622T173743Z` | 2000 | 100 | 2 | 2 | `g6-standard-2` | `SUCCESS` | Confirms multi-port factory-enroll setup and recorded summary pipeline; too small for 100K coefficient |
| `cap10k-anchor-20260622T142103Z` | 10000 | 500 | 1 | 2 | `g6-standard-2` | `SUCCESS` | 10000 devices/MQTT pod, 5000 devices/node for this config only |
| `cap50k-anchor-20260622T152637Z` | 50000 | 2500 | 5 | 5 | `g6-standard-4` | `INCOMPLETE` | No safe coefficient; runner timeout killed shards before `results.json` write |
| `cap50k-timeoutfix-20260622T170416Z` | 50000 | 2500 | 5 | 5 | `g6-standard-4` | `ABORTED` | No safe coefficient; data setup factory enroll client timeouts before load test |
| `cap50k-multiport-20260622T180635Z` | 50000 | 2500 | 5 | 5 | `g6-standard-4` | `INCOMPLETE` | No safe coefficient; all MQTT pods OOMKilled during the load window |
| `cap50k-mqttmem-20260622T195155Z` | 50000 | 2500 | 5 | 5 | `g6-standard-4` | `FAIL` | No safe coefficient; MQTT path passed, but cloud-logger OOMKilled during evidence/report collection |
| `cap50k-logger4g-20260622T213248Z` | 50000 | 2500 | 5 | 5 | `g6-standard-4` | `SUCCESS` | 10000 devices/MQTT pod, 10000 devices/node for this config; load-generator count remains formula-driven |
| `cap100k-anchor-20260622T231201Z` | 100000 | 5000 | 10 | 10 | `g6-standard-4` | `ABORTED` | No safe coefficient; load-generator orchestration failed before runtime because one runner rebuilt a different formula-derived assignment plan |
| `cap100k-assignmentfix-20260623T012916Z` | 100000 | 5000 | 10 | 10 | `g6-standard-4` | `ABORTED` | No safe coefficient; data setup port-forward disconnected during create_users before runtime |
| `cap100k-setup16-20260623T022401Z` | 100000 | 5000 | 10 | 10 | `g6-standard-4` | `ABORTED` | No safe coefficient; data setup bind_devices reached 62352/100000 then account-manager port-forward reset before runtime |
| `lt100k-5vm-20260623T111248Z` | 100000 | 5000 | 10 | 10 | `g6-standard-4` | `SUCCESS` | 10000 devices/MQTT pod, 10000 devices/node, and 20000 devices/load-generator VM for this run's formula input |

Do not use older 10K or 100K reports as capacity coefficients unless they were
rerun with the current scripts and produced `COMPLETE/SUCCESS`. Existing older
reports are useful for failure taxonomy only.

### `cap2k-multiport-20260622T173743Z` Notes

- Report: `loadtests/home-100k/reports/cap2k-multiport-20260622T173743Z/TEST_REPORT.md`
- Summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap2k-multiport-20260622T173743Z/capacity-run-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap2k-multiport-20260622T173743Z/request.json`
- Data setup used `factory_enroll_ports=18443,18444,18445,18446` and passed:
  100 users in 10s, 2000 devices in 28s, 2000 binds in 44s, bind validation in
  7s. This validates the multi-port local factory-enroll setup path before
  rerunning 50K.
- Runtime gates passed: `COMPLETE/SUCCESS`, server correlation pass, runtime log
  stream correlation pass, load-generator saturation false.
- Runtime counters: 2000 target connects, 2000 connect successes, 0 connect
  failures, 2000 `/request_token` 200 responses, 100 APP desired writes, 100
  ACKs, 100 device deltas, 100 reported publishes.
- Resource signal: load-generator p95 CPU 15.1%, max CPU 15.5%, p95 memory
  15.8%; K8s node CPU p95 81%/24%, memory p95 79%/58%. The high first-node CPU
  p95 came from the provisioning/run window and the steady MQTT phase was much
  lower; use this run as a flow validation point, not as a 100K coefficient.
- Report caveat: the Markdown bottleneck table may rank broker path from normal
  EMQX `ssl_closed` disconnect counters even when all gates pass. For capacity
  coefficients, use the summary outcome and failed-gate classification; this
  run's summary bottleneck is `none`.
- Cleanup confirmation: the wrapper destroyed the single `home-100k`
  load-generator VM, and a follow-up Linode query returned no matching
  `home-100k` instances.

### `cap10k-anchor-20260622T142103Z` Notes

- Report: `loadtests/home-100k/reports/cap10k-anchor-20260622T142103Z/TEST_REPORT.md`
- Summary: `cloud_env/staging/lke/artifacts/capacity-experiments/cap10k-anchor-20260622T142103Z/capacity-run-summary.json`
- Data setup passed: 500 users in 42s, 10000 devices in 148s, 10000 binds in
  277s, bind validation in 29s.
- Runtime gates passed: `COMPLETE/SUCCESS`, server correlation pass, runtime log
  stream correlation pass, load-generator saturation false.
- MQTT counters: 10000 target connects, 10000 connect successes, 0 connect
  failures, 500 APP desired writes, 500 ACKs, 500 device deltas, 500 reported
  publishes.
- Resource signal: load generator p95 CPU 6.6%, max CPU 18.7%, max memory 26%;
  K8s node CPU p95 10%/28%, max CPU 30%/52%, memory p95 73%/89%, memory max
  74%/98%.
- Interpretation: one MQTT pod handled 10K connections, and one generator VM was
  nowhere near saturated. However, two `g6-standard-2` nodes have weak memory
  headroom for this complete runtime stack. Do not scale this exact node type
  linearly toward 50K/100K.

### `cap50k-anchor-20260622T152637Z` Notes

- Report: `loadtests/home-100k/reports/cap50k-anchor-20260622T152637Z/TEST_REPORT.md`
- Summary: `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-anchor-20260622T152637Z/capacity-run-summary.json`
- Data setup passed: 2500 users in 99s, 50000 devices in 222s, 50000 binds in
  1468s, bind validation in 153s.
- Runtime result: `INCOMPLETE/INCOMPLETE`; all three shard runners were killed
  by the live MQTT command timeout after `33m30s`, before
  `mqtt-test/target/results.json` was written.
- Root cause: the previous runner timeout was `configured_duration + 90s`. For
  a 50K run split across 3 generator VMs, each shard held about 16.6K devices;
  the 90s grace was not enough for disconnect/write-result cleanup after the
  10m ramp-up + 20m steady + 2m cool-down window.
- Fix applied after the run: live runner timeout grace is now scale-aware,
  defaulting to `max(10m, duration / 4)`, and capacity experiments explicitly
  record `HOME100K_LIVE_RUNNER_TIMEOUT_GRACE=15m`.
- Resource signal before timeout: load-generator p95 CPU 9.9% to 14.4%, max
  memory 31.5% to 32.7%; K8s node p95 CPU mostly 16% to 31%, with one node at
  75%, and memory p95 38% to 53%. This does not prove 50K capacity, because the
  run did not complete, but it shows the immediate failure was not generator or
  node memory saturation.
- Server evidence captured partial progress: EMQX saw 19726 connect attempts
  and 19726 successes; ingress saw 21723 `/request_token` 200 responses; central
  runtime logs saw 2481 desired writes and 893 ACKs. These numbers are
  diagnostic only and must not be used as safe capacity coefficients.
- Cleanup confirmation: the wrapper destroyed the three `home-100k`
  load-generator VMs, and a follow-up Linode query returned no matching
  `home-100k`/`lg*` instances.

### `cap50k-timeoutfix-20260622T170416Z` Notes

- Abort summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-timeoutfix-20260622T170416Z/aborted-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-timeoutfix-20260622T170416Z/request.json`
- Data setup log:
  `cloud_env/staging/lke/artifacts/staging-e2e-data/cap50k-timeoutfix-20260622T170416Z/logs/create_devices.log`
- Runtime result: aborted during `setup-staging-e2e-data/create_devices`, before
  load-generator VMs were created and before `home-100k workflow-live`.
- Observed progress at abort: 1703/50000 done, 1646 generated, 57 failed.
- Client error pattern: local factory enroll calls through
  `http://127.0.0.1:18443/v1/factory/enroll` timed out waiting for response
  headers.
- Server observation: factoryenroll and certissuer logs showed observed requests
  returning 200 in roughly 10-30ms, and K8s node/pod CPU and memory were low.
  This points away from MQTT/node sizing and toward the local data setup path,
  most likely port-forward/client enrollment behavior under concurrency.
- Cleanup confirmation: no `home-100k`/`lg*` load-generator VMs existed after
  abort, because the run had not reached the load-generator phase.
- Next action: fix or bypass the local factory-enroll data setup bottleneck,
  then rerun the same 50K config. This run must not produce MQTT pod, node, or
  generator safe coefficients.

### `cap50k-multiport-20260622T180635Z` Notes

- Report: `loadtests/home-100k/reports/cap50k-multiport-20260622T180635Z/TEST_REPORT.md`
- Summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-multiport-20260622T180635Z/capacity-run-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-multiport-20260622T180635Z/request.json`
- Data setup passed with multi-port factory enrollment: 2500 users in 107s,
  50000 devices in 698s, 50000 binds in 1545s, and bind validation in 170s.
  This confirms the previous setup-path bottleneck was removed for this target.
- The live workflow completed its 10m warm-up, 20m steady, and 2m cool-down
  window and wrote `results.json`; the scale-aware runner timeout fix worked.
- Runtime result: `INCOMPLETE/INCOMPLETE`. Client counters reached 49776/50000
  successful MQTT connects, then reported EOF/broken-pipe failures, 32080
  rejected telemetry publishes, and only 139/2501 APP shadow ACKs.
- Generator/resource signal: the three load-generator VMs were not saturated
  (highest p95 CPU was 43.4%), and the LKE nodes were not globally saturated
  (highest observed node CPU max 78%, memory max 66%).
- Root cause evidence: all five MQTT pods restarted with `OOMKilled` exit code
  137 around `2026-06-22T19:20:29Z` to `2026-06-22T19:21:50Z`. The live
  manifest used MQTT resources of `request.cpu=200m`, `request.memory=512Mi`,
  and `limit.memory=1536Mi`, plus `EMQX_FORCE_SHUTDOWN__MAX_HEAP_SIZE=512MB`.
- Interpretation: the optimistic seed `20000 devices/MQTT pod` is not validated
  with a 1536Mi MQTT pod memory limit. This run must not produce safe capacity
  coefficients. The next 50K retry should keep the target fixed and increase
  MQTT memory request/limit first, for example by recording
  `--mqtt-request-memory 2Gi --mqtt-limit-memory 4Gi` in the capacity wrapper,
  then rerunning the same 5-pod/5-node shape before extrapolating to 100K.
- Cleanup confirmation: the wrapper destroyed the three `home-100k`
  load-generator VMs. The LKE cluster was intentionally left available long
  enough to inspect pod restart/OOM evidence.

### `cap50k-mqttmem-20260622T195155Z` Notes

- Report: `loadtests/home-100k/reports/cap50k-mqttmem-20260622T195155Z/TEST_REPORT.md`
- Summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-mqttmem-20260622T195155Z/capacity-run-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-mqttmem-20260622T195155Z/request.json`
- Setup passed: 2500 users in 103s, 50000 devices in 687s, 50000 binds in
  1514s, and bind validation in 169s.
- Runtime client gates passed: 50000/50000 MQTT connects, 50000/50000
  subscriptions, 2502 APP desired writes, 2502 APP ACKs, 2502 device deltas,
  2502 reported publishes, and 0 rejected publishes.
- MQTT/resource result: no MQTT pod restarted or OOMKilled. EMQX server
  evidence saw 52502/52502 connect successes. The three load-generator VMs were
  not saturated. K8s node p95/max resource usage topped out at 75%/76% CPU on
  one node and 64%/64% memory on another node.
- Report result: `COMPLETE/FAIL` because server/runtime log correlation only
  found 603 of 2502 expected runtime streams. The 603 streams that were found
  had complete sequences; missing sequence count was 0.
- Root cause evidence: the `cloud-logger` pod OOMKilled with exit code 137 at
  `2026-06-22T21:28:47Z` during evidence/report collection. The default logger
  memory limit was too small for this 50K run's runtime evidence volume.
- Interpretation: the MQTT memory fix moved the bottleneck away from broker
  OOM. This still cannot be used as a safe 50K coefficient because the evidence
  gate failed. The next retry should keep 50K, 5 MQTT pods, and 5 nodes fixed,
  then raise cloud-logger memory. The current recommended floor is
  `--cloud-logger-request-memory 4Gi --cloud-logger-limit-memory 8Gi`.
- Cleanup confirmation: the wrapper destroyed the three `home-100k`
  load-generator VMs; a follow-up Linode query returned no matching `home-100k`
  instances. The final cleanup plan listed three new unattached PVC volumes
  from this deployment; delete them by exact ID before the next fresh run.

### `cap50k-logger4g-20260622T213248Z` Notes

- Report: `loadtests/home-100k/reports/cap50k-logger4g-20260622T213248Z/TEST_REPORT.md`
- Summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-logger4g-20260622T213248Z/capacity-run-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-logger4g-20260622T213248Z/request.json`
- Applied config:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap50k-logger4g-20260622T213248Z/stack.env.applied`
- Cleanup input: the run deleted exact orphan PVC volume IDs
  `16391306,16391305,16391278` before provisioning, and recorded that list in
  the request artifact.
- Setup passed: 2500 users in 102s, 50000 devices in 672s, 50000 binds in
  1556s, and bind validation in 151s.
- Runtime gates passed: `COMPLETE/SUCCESS`, client target completeness 100%,
  server correlation pass, runtime log stream correlation pass, server evidence
  complete, and load-generator saturation false.
- Runtime counters: 50000/50000 client MQTT connects, 0 connect failures,
  50000 subscriptions, 2502 APP desired writes, 2502 APP ACKs, 2502 device
  deltas, 2502 reported publishes, and 0 rejected publishes.
- Server evidence: EMQX saw 52502/52502 connect successes. Runtime log
  correlation found 2502/2502 streams with 0 missing streams and 0 missing
  sequence counts.
- Resource signal: load-generator p95 CPU was 12.1%, 10.5%, and 20.6%; max CPU
  was 51.3%, 17.9%, and 56.1%. Load-generator max memory stayed below 38%.
  K8s node p95 CPU topped out at 76% on one node, and p95 memory topped out at
  64%. The five MQTT pods used roughly 776-805Mi at evidence collection.
- Pod health confirmation after the run: MQTT, cloud-logger, video-cloud-api,
  Postgres, and account-manager pods were running with 0 restarts. This confirms
  the MQTT 4Gi limit and cloud-logger 4Gi limit were enough for this 50K run.
- Report caveat: the Markdown bottleneck table still ranks broker path because
  it counts normal end-of-run EMQX `ssl_closed` disconnects. The summary
  bottleneck classification is `none`; use failed gates and summary
  classification, not this cosmetic table, for capacity decisions.
- Safe coefficients from this run: use `10000 devices/MQTT pod` and `10000
  devices/node` as conservative measured server-side coefficients until a
  larger `COMPLETE/SUCCESS` run proves higher values. The run used three
  load-generator VMs for 50K, but current load-generator planning intentionally
  keeps the operator input at `load_generator_devices_per_vm=20000`; compute
  future load-generator VM count with the formula instead of copying this row.
- Cleanup confirmation: the wrapper destroyed the three `home-100k`
  load-generator VMs, and a follow-up Linode query returned no matching
  `home-100k` instances. The post-run cleanup plan listed new unattached PVC
  volumes `16393032`, `16393033`, and `16393017`; delete them by exact ID before
  the next fresh run if the run will destroy and recreate the LKE stack.

### `lt50k-video-turn-20260702T124500Z` Logger Evidence Notes

- Report:
  `loadtests/home-100k/reports/lt50k-video-turn-20260702T124500Z/TEST_REPORT.md`
- Result: `INCOMPLETE`. This run must not be used as a 50K-with-WebRTC capacity
  coefficient.
- MQTT/shadow client signal: 50000 device attempts produced 49996 MQTT connect
  successes and 4 token failures. APP desired writes reached 2502, but only
  2320 ACKs were observed; 182 APP waits timed out.
- WebRTC signal: the first relay-only video step reached 77/100 media sessions.
  The remaining 23 failed at `webrtc_media_answer` with HTTP 400
  `device not online`. The failing devices still had active websocket owner
  evidence, so this was a Video Cloud API online-projection/signaling ownership
  bug, not coturn pressure.
- TURN signal: turnregistry reported one active coturn node, coturn active
  allocations/sessions were around 46 during the active window, all selected
  candidates were relay, and no non-relay candidate was reported. This is not
  enough pressure to justify adding coturn nodes.
- Logger root cause evidence: the server evidence probe reported
  `central logger query failed: logger query status=502`, and
  `runtime_log_streams=0`. The previous `cloud-logger` pod had
  `Last State: Terminated`, `Reason: OOMKilled`, `Exit Code: 137`, restart count
  3, and a 4Gi memory limit. Because runtime shadow evidence was missing, the
  report correctly stayed `INCOMPLETE`.
- Fix after the run: `cloud_env/staging/lke/env/stack.env` now sets
  `LKE_CLOUD_LOGGER_REQUEST_MEMORY=4Gi` and
  `LKE_CLOUD_LOGGER_LIMIT_MEMORY=8Gi`; the LKE manifest fallback and
  `scripts/run-lke-capacity-experiment.sh` defaults use the same 4Gi/8Gi
  capacity floor. After redeploy, the new `cloud-logger` pod had restart count
  0 and the 8Gi limit applied. A later live check found an old pod had been
  evicted under node memory pressure while using about 4.37Gi with only a 1Gi
  request, so the request floor must stay near the observed active-window
  footprint rather than only raising the memory limit.
- Required next-run checks: record `cloud-logger` restart count before and
  after the 50K-with-WebRTC run, capture memory p95/max during the active
  window, and verify `/v1/logs` can return run-scoped runtime events before
  accepting MQTT/shadow evidence. If logger query fails, classify the run as
  `evidence_logging` or `cloud_logger_oom`; do not attribute it to coturn,
  MQTT, or API capacity without separate evidence.

### `cap100k-anchor-20260622T231201Z` Notes

- Abort summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap100k-anchor-20260622T231201Z/aborted-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap100k-anchor-20260622T231201Z/request.json`
- Report directory:
  `loadtests/home-100k/reports/cap100k-anchor-20260622T231201Z`
- Requested config: 100000 devices, 5000 users, 10 MQTT pods, 10
  `g6-standard-4` LKE nodes, a run-specific
  `load_generator_devices_per_vm` input, MQTT request/limit `2Gi/4Gi`, EMQX
  heap `2GB`, and cloud-logger request/limit `1Gi/4Gi`. The load-generator VM
  count came from the standard
  `ceil(target_devices / load_generator_devices_per_vm)` formula.
- Setup passed: 5000 users in 198s, 100000 devices in 1345s, 100000 binds in
  4606s, and bind validation in 305s. These are valid setup-path timings, but
  they do not prove runtime capacity.
- Provisioning passed: the LKE cluster was rebuilt with 10 nodes and 10 MQTT
  pods, DNS converged, and the cached TLS certificate was restored without a new
  Let's Encrypt request.
- Load-generator preparation used the formula-derived VM set. `lg01` through
  `lg05` entered `READY_WAIT`; `lg06` failed before runtime with
  `assignment not found: role=mixed index=5`.
- Root cause: the controller planned 6 assignments using
  `load_generator_devices_per_vm=16667`, but `ansible/start-runner.yml` did not
  pass `--load-generator-devices-per-vm` into the remote runner daemon. The
  daemon therefore rebuilt the plan with the default 20000 devices per VM,
  which produces only 5 assignments for 100K. This is an orchestration bug, not
  a MQTT pod, LKE node, or generator-capacity failure.
- Fix after the run: the start-runner playbook now passes
  `--load-generator-devices-per-vm`, the generated Ansible extra-vars record
  `load_generator_devices_per_vm`, and tests cover the 100K/16667 -> 6
  assignment case.
- Follow-up planning correction: this run used a conservative
  `load_generator_devices_per_vm=16667` input. The active wrapper computes VM
  count as `ceil(target_devices / load_generator_devices_per_vm)`; do not copy a
  fixed VM count from this aborted run.
- Readiness diagnostic improvement: the READY_WAIT polling now writes
  `runner-ready-response.json` and avoids hiding daemon startup failures behind
  a JSON parse error.
- Cleanup confirmation: a follow-up Linode query found no `home-100k`
  load-generator VMs. The next fresh run should delete unattached PVC volume IDs
  `16394599`, `16394611`, and `16394612` by exact ID before provisioning.

### `cap100k-assignmentfix-20260623T012916Z` Notes

- Abort summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap100k-assignmentfix-20260623T012916Z/aborted-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap100k-assignmentfix-20260623T012916Z/request.json`
- Requested config: 100000 devices, 5000 users, 10 MQTT pods, 10
  `g6-standard-4` LKE nodes, a run-specific
  `load_generator_devices_per_vm` input, MQTT request/limit `2Gi/4Gi`, EMQX
  heap `2GB`, and cloud-logger request/limit `1Gi/4Gi`. The load-generator VM
  count came from the standard
  `ceil(target_devices / load_generator_devices_per_vm)` formula.
- Provisioning passed: 10 MQTT pods joined the EMQX cluster, the public TLS
  certificate was restored from cache, HAProxy edge was provisioned, and DNS
  converged to `172.238.48.97`.
- Data setup aborted during `create_users` after 1130/5000 completed. The
  local kubectl port-forwards reported lost connections, and the create-users
  process failed with `dial tcp 127.0.0.1:18081: connect: connection refused`.
- Live cluster inspection after abort showed account-manager, video-cloud-api,
  factoryenroll, MQTT, cloud-logger, and Postgres pods Running with 0 restarts.
  Kubernetes events did not show an app pod restart/OOM at the failure time.
- Interpretation: this is a setup-path port-forward reliability failure before
  load-generator runtime. It must not produce MQTT pod, LKE node, or generator
  capacity coefficients.
- Fix after the run: capacity experiments now default user setup concurrency to
  16, matching the existing device setup concurrency choice. The next attempt
  should use a fresh cleanup/provision so the partial account-manager DB state
  does not affect app certificate recovery.

### `cap100k-setup16-20260623T022401Z` Notes

- Abort summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap100k-setup16-20260623T022401Z/aborted-summary.json`
- Request:
  `cloud_env/staging/lke/artifacts/capacity-experiments/cap100k-setup16-20260623T022401Z/request.json`
- Data setup log:
  `cloud_env/staging/lke/artifacts/staging-e2e-data/cap100k-setup16-20260623T022401Z/logs/bind_devices.log`
- Provision result before setup: 10 `g6-standard-4` nodes, 10 MQTT pods, cached
  public TLS certificate restored without a Let's Encrypt request, DNS converged
  to the new edge, and all pods were running with zero restarts after abort.
- Data setup progress: `create_users` passed with 5000 users in 322s,
  `create_devices` passed with 100000 devices in 2137s, and `bind_devices`
  reached 62352/100000 with 0 skipped before failing.
- Failure pattern: bind continued returning successful Video Cloud activation
  calls near the last progress point, then Account Manager calls through
  `127.0.0.1:18081` started failing with `connection reset by peer`; kubectl
  also reported port-forward lost/error-stream timeout messages.
- Live cluster state after abort: Account Manager, Video Cloud API, Postgres,
  factoryenroll, and MQTT pods were still running with no application restarts;
  node CPU and memory were low. This points to setup transport reliability
  rather than MQTT pods, LKE nodes, or load-generator capacity.
- Script fix after this abort: the capacity wrapper now classifies connection
  reset/error-stream timeout as `data_setup_port_forward_lost`, records failed
  setup attempt logs, and retries setup with `--resume` so a long bind can
  continue without rebuilding the cluster.
- This run still never reached `workflow-live`, so it must not change MQTT pod,
  node, or generator safe coefficients.

### `lt100k-5vm-20260623T111248Z` Notes

- Report:
  `loadtests/home-100k/reports/lt100k-5vm-20260623T111248Z/TEST_REPORT.md`
- Summary:
  `cloud_env/staging/lke/artifacts/capacity-experiments/lt100k-5vm-20260623T111248Z/capacity-run-summary.json`
- Formula input: `load_generator_devices_per_vm=20000`; the load-generator VM
  count was computed as `ceil(target_devices / load_generator_devices_per_vm)`.
  Do not copy the resulting VM count as a fixed 100K profile.
- Runtime gates passed: `COMPLETE/SUCCESS`, client target completeness 100%,
  server correlation pass, runtime log stream correlation pass, server evidence
  complete, and load-generator saturation false.
- Runtime counters: 100000/100000 client MQTT connects, 100000 subscriptions,
  0 connect failures, 100000 `/request_token` successes, 5000 APP desired
  writes, 5000 APP ACKs, 5000 device deltas, 5000 reported publishes, and 0
  rejected publishes.
- Server evidence: EMQX saw 105000/105000 total connect successes including
  APP/user MQTT connections. Runtime log correlation found 5000/5000 streams
  with 0 missing streams and 0 missing sequence counts.
- Resource signal: load-generator VM p95 CPU ranged from 10.3% to 15.3%, with
  one max sample at 63.7%; p95 memory stayed below 42.5%. K8s node p95 CPU
  peaked at 99% on the account-manager node during APP login/token preparation,
  and p95 node memory peaked at 81% on one node during the run. Treat these as
  the first 100K sizing pressure points to watch in the next reduction or
  repeat run.
- Observed setup/runtime note: before MQTT ramp, Account Manager handled APP
  login/token preparation at high CPU; the final runtime report shows only 5
  APP login attempts because the runner reused per-shard app sessions during
  the stage.
- Cleanup confirmation: the workflow stopped the load-generator instances but
  left them listed as `offline`; a follow-up
  `home-100k.sh destroy-vms --live --confirm-live` for the same run ID deleted
  them, and the Linode `home-100k` query returned an empty list.

## Data Setup Optimizations

The capacity experiments should keep using the normal user/device/bind flow.
Do not add a privileged bulk-import backdoor just to make tests faster. The
right optimization direction is to make the normal flow resumable and
observable:

- keep user/device/app tokens and refresh old tokens before login when possible;
- generate CSRs locally once and pass them through the normal API flow;
- persist local test artifacts in a SQLite store instead of many small JSON
  files, then export JSON only for backward-compatible runner inputs;
- record per-step durations and retry history so slow create/bind paths can be
  traced to Account Manager, Video Cloud API, Postgres, or local transport.

## Adaptive Search

Use model-first adaptive search instead of a fixed ladder:

- Start with a 10K anchor using the current seed formula.
- Run a 50K anchor to expose mid-scale API/Postgres/MQTT/generator effects.
- Attempt 100K with the formula-predicted config.
- If 100K passes, reduce MQTT pods and/or node count by binary search to find
  the smallest PASS configuration.
- If 100K fails, bisect between the highest PASS target and 100K, classify the
  bottleneck, tune the relevant subsystem, then retry 100K.

Failure classification controls the next action:

- `load_generator`: increase load-generator VMs or lower per-VM device target.
- `mqtt_emqx`: increase MQTT pods and node spread, then rerun the same target.
- `mqtt_pod_oom`: increase MQTT pod memory request/limit, keep one MQTT pod per
  node, and rerun the same target before changing target size.
- `api_request_token` or `api_postgres_shadow`: tune Video Cloud API/Postgres
  resources and pools before adding MQTT capacity.
- `evidence_logging`: fix logger/server evidence before treating the run as a
  capacity datapoint.
- `cloud_logger_oom`: increase cloud-logger memory or move to a durable logger
  backend, then rerun the same target before changing MQTT/node sizing. For
  50K+ staging evidence runs, do not go below the current 4Gi request / 8Gi
  limit floor, and verify logger restart count plus memory p95/max in the next
  report.

## 100K Initial Prediction

Using the current operator load-generator rule and the server-side coefficients
from `cap50k-logger4g-20260622T213248Z`, the initial 100K planning config is:

- target devices: `100000`
- users: `5000`
- load-generator VMs: computed by
  `ceil(target_devices / load_generator_devices_per_vm)`.
- MQTT replicas: `10`, computed as `ceil(100000 / 10000)`
- minimum LKE general nodes: at least `10` for one MQTT pod per node; the final
  node count must still be checked against CPU and memory requests
- node type: start with `g6-standard-4` for general nodes unless quota or
  measured CPU/memory pressure requires a larger type
- HAProxy edge: `1` VM with `LKE_EDGE_HAPROXY_MAXCONN >= 400000`
- Postgres: use explicit CPU/memory requests and consider a dedicated larger
  node pool once Postgres p95 CPU, memory, or I/O pressure appears

These coefficients are intentionally conservative on the server side. The
load-generator count follows the formula above; if the 100K run shows generator
CPU, file descriptor, network, or timeout saturation, the bottleneck
classification must say `load_generator` and the next run should reduce
`load_generator_devices_per_vm` or otherwise adjust generator sizing through
the same formula. A 100K PASS can raise confidence; only a follow-up
reduction/binary-search run can justify reducing pod or node count.

The final node count is not just `5`. The shared planner calculates each logical
node class independently as the maximum of its configured floor, workload CPU,
workload memory, and spread rules. Use
`rtk-cloud deployment plan --environment staging` after setting generic capacity
targets, minimum replicas, planning shapes, and workload requests in the
environment architecture override.

## Review Checklist

A capacity recommendation is reviewable only when it cites:

- all `capacity-run-summary.json` rows used as evidence,
- the exact `TEST_REPORT.md` for each row,
- the chosen conservative safe coefficients,
- the computed 100K config,
- the bottleneck analysis for every failed or incomplete run,
- confirmation that no `home-100k` load-generator VMs remained after each run.
