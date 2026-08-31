# RTK Cloud Testing Operations Guide

Status: active

Owner: `rtk_cloud_workspace`

Last reviewed: 2026-08-11

Audience: internal test operators and new maintainers

This document describes the environment, data, and evidence required before
testing. Tests are divided into local validation, deployed acceptance, 1K feature
qualification, and capacity/load testing. These levels do not replace one another.

## Select the Test Level

| Purpose | Entry point | Primary prerequisites |
| --- | --- | --- |
| Fast workspace baseline | `test-matrix` | Complete recursive checkout |
| Service tests | `test-services` | Per-repository runtime/dependencies |
| Deterministic E2E | `test-e2e` | Local fixtures; no shared staging required |
| UI tests | `test-ui` | Chromium and local BFF/fixtures |
| Deployed-environment acceptance | `deployment acceptance` | Matching runtime + kube access |
| Billing staging qualification | GitHub Actions `billing-staging-qualification.yml` | Actions dispatch permission + staging authorization |
| Feature 1K qualification | `test-feature` | Acceptance PASS + dedicated test identity |
| Capacity test | `home-100k.sh workflow-live` | Explicit target, capacity plan, sufficient inventory and generators |

## Common Preparation

1. Run from the workspace root and initialize every submodule:

   ```sh
   git submodule update --init --recursive
   ```

2. Confirm workspace/submodule commits and dirty state:

   ```sh
   go run ./scripts/go/rtk-cloud -- status-all
   ```

3. Live tests use the canonical runtime:

   ```text
   cloud_env/<environment>/runtime
   ```

4. Complete deployment acceptance preflight before live tests:

   ```sh
   go run ./scripts/go/rtk-cloud -- deployment preflight \
     --environment staging --operation acceptance
   ```

5. Use dedicated test users, Devices, and brands. Never capture or export real
   customer data.

## Local Baseline, Service, E2E, and UI

Fast baseline:

```sh
(cd scripts/go && go run ./rtk-cloud -- test-matrix)
```

More complete local validation:

```sh
(cd scripts/go && go run ./rtk-cloud -- test-services)
(cd scripts/go && go run ./rtk-cloud -- test-e2e)
(cd scripts/go && go run ./rtk-cloud -- test-e2e --scripts)
(cd scripts/go && go run ./rtk-cloud -- test-ui --desktop)
(cd scripts/go && go run ./rtk-cloud -- test-ui --mobile)
```

UI staging mode may use only dedicated staging accounts and must set
`E2E_EVIDENCE_SAFE=1`. See [`testing.md`](testing.md) for complete coverage,
artifact layout, and Test ID governance.

## Deployed Staging Acceptance

Acceptance obtains Account Manager, Video Cloud, factory-enroll, and MQTT
endpoints through K8s service discovery/port-forward and does not read retired
VM state.

```sh
go run ./scripts/go/rtk-cloud -- deployment acceptance \
  --environment staging --confirm video-cloud-staging
```

Acceptance must prove user/Device setup, MQTT flow, runtime-log persistence, and
the selected billing checks. Any failure stops creation of paid load generators.

## Billing Staging Qualification

Deployed E2E for billing payment, invoices, and the Cloud Admin portal uses a
dedicated GitHub Actions workflow. When another agent executes it, run the
non-mutating `plan` by default and allow live staging mutation with the correct
confirmation string only after it passes. Do not let an agent assemble local
credentials or runtime itself.

See [`billing-staging-qualification.md`](billing-staging-qualification.md) for
permissions, dispatch/watch commands, seven required Test IDs, the PASS gate,
artifacts, cleanup, and failure-handoff rules.

## 1K Feature Qualification

Generate only a plan first:

```sh
go run ./scripts/go/rtk-cloud -- test-feature \
  --feature device-shadow \
  --profile qualification-1k \
  --environment staging \
  --run-id manual-shadow-001 \
  --plan
```

The live run must explicitly add `--run --confirm video-cloud-staging`. Available
features are `device-shadow`, `video-webrtc`, and `clip-storage`. Each has a
different 1K definition; Device count alone does not determine test intensity.
Qualification must pass canary before creating 1K load.

## Capacity/Load-Test Prerequisites

10K, 50K, 100K, and custom targets are independent sizing exercises. Before each
run, record:

- Target connected Devices/clients and success threshold; the default functional
  threshold is usually 99.5%.
- Warm-up, steady, and cool-down durations.
- User/Device inventory count, Device mix, brand, and certificate validity.
- Generator VM count, per-VM connection budget, CPU/memory/NIC, FDs, and ephemeral ports.
- K8s node class/count, EMQX workload shape/replicas, pod placement, and service endpoints.
- HAProxy backend list, `maxconn`, file descriptors, and memory.
- PostgreSQL CPU/memory/storage/connection limit and Video Cloud API replicas/DB pool.
- Expected bottleneck and the metrics that confirm or disprove it.

Do not merely change `HOME100K_DEVICES` and treat a 10K-sized environment as
50K/100K capacity evidence.

## Load Plan, Preflight, and Execution

First select a scenario description and confirm the prepared inventory covers
the target:

```sh
HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime" \
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID="preflight-mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)" \
  ./loadtests/home-100k/scripts/home-100k.sh plan
```

Fixture/certificate preflight:

```sh
HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime" \
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
  ./loadtests/home-100k/scripts/home-100k.sh preflight
```

Run only after both pass:

```sh
HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime" \
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID="mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)" \
  ./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

50K, 100K, and other targets require matching descriptions/capacity plans. See
[`../loadtests/home-100k/README.md`](../loadtests/home-100k/README.md) for complete
profiles and parameters.

## Runtime Monitoring

Monitor at least:

- K8s node/pod/container CPU, memory, restarts, and unschedulable workloads.
- EMQX cluster membership, listener current connections, and shutdown/socket/
  congestion counters.
- HAProxy frontend/backend sessions, backend health, FDs, RSS, and connection errors.
- PostgreSQL connections, CPU, memory, IO, and transaction/lock pressure.
- Video Cloud API Token bootstrap latency, DB pool, and API-to-MQTT connection health.
- Generator process limits, CPU, memory, network, and per-shard completion.

Confirm routing correctness and generator headroom before increasing server capacity.

## Report, Resume, and Cleanup

The final report is at:

```text
loadtests/home-100k/reports/<run-id>/test_report.md
```

`Status: COMPLETE` means only that the workflow completed. Claim success only
when `Result: SUCCESS` and the target gate is satisfied. `FAIL`, `INCOMPLETE`,
and `BLOCKED` are non-passing outcomes.

If VMs exist but the workflow was interrupted, reuse the same run ID:

```sh
HOME100K_RUN_ID=<run-id> \
  ./loadtests/home-100k/scripts/home-100k.sh workflow-resume-live
```

After investigation, clean up only load-generator VMs created by that run:

```sh
HOME100K_RUN_ID=<run-id> \
  ./loadtests/home-100k/scripts/home-100k.sh list-vms
HOME100K_RUN_ID=<run-id> \
  ./loadtests/home-100k/scripts/home-100k.sh destroy-vms --live --confirm-live
```

Do not delete the LKE cluster, CI runners, edge/TURN VMs, release buckets, or
generators from other runs.

## Common Stop Conditions

- Incomplete runtime or identity mismatch: stop and restore matching runtime.
- Inventory below target or incorrect Device mix: prepare data again; do not start generators.
- MQTT route/backend does not match ready pod placement: fix routing first.
- Generator FD/CPU/network saturation: expand generators first; do not attribute
  the result to the server.
- `/request_token` failure: inspect API/PostgreSQL first; do not merely add EMQX pods.
- Device connects but shadow delta/ACK fails: inspect API-to-MQTT, workers,
  database, and persisted runtime logs.
