# Linode 100K Home IoT Device Shadow Load Test

Status: implemented review-time runner and live VM lifecycle scaffold
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
| Stages | 25K, 50K, 75K, 100K connected devices |
| VM layout | 10 `mixed` VMs |
| Per-VM device task | Up to 10K simulated devices |
| Per-VM user task | 500 simulated app users |
| Total load-generator VMs | 10 for the default 100K-device/5K-user mixed baseline |

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
  path, and status interval.

The default description file points at the existing provision-server staging
environment:

```text
cloud_env/staging/lke
```

Defaults:

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
syncs the runner/env-root, dispatches shards, collects shard artifacts,
collects server evidence, and aggregates the report. VM destruction remains a
separate explicit command. During the live workflow the script prints status
immediately and every 30 seconds with the current phase, VM count, collected
shard count, server evidence state, report status, per load-generator VM
resource samples, and per Kubernetes node resource samples. Provisioned node inventory is written to
`loadtests/home-100k/reports/<run-id>/nodes.tsv`.

Kubernetes node resource samples use `kubectl top nodes --no-headers` and print
`[home-100k k8s-node]` lines. Kubeconfig resolution order is
`HOME100K_KUBECONFIG`, `RTK_CLOUD_LKE_KUBECONFIG`, `LKE_KUBECONFIG`,
`CLOUD_STAGING_K8S_KUBECONFIG`, then
`<env-root>/state/lke-kubeconfig.yaml`. Set
`HOME100K_K8S_NODE_RESOURCE_STATUS=0` to disable K8s node probing.

Stage duration is part of the non-secret description file. The default profile
uses `HOME100K_STAGE_WARM_UP=1m`, `HOME100K_STAGE_STEADY=2m`, and
`HOME100K_STAGE_COOL_DOWN=45s`, which plans 3 minutes 45 seconds per stage and
15 minutes for the four load stages before VM lifecycle and evidence overhead. Use a
custom `HOME100K_DESCRIPTION_FILE` or edit
`loadtests/home-100k/scenarios/default.description.env` for shorter or longer
runs.

The command writes `plan.json`, `results.json`, `server-evidence.json`, and
`TEST_REPORT.md`. Add `--server-evidence-file <path>` when server evidence has
been collected for the same run id. Without server evidence, the report status
is `INCOMPLETE`.

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
Live sync reads `vms.json` and uses SSH plus rsync to copy `loadtests/home-100k`,
`go.work`, and the selected env-root to each load-generator VM.
Live run-stages reads `vms.json` and dispatches `shard-run` over SSH. Each VM
writes shard artifacts under `--remote-out-root/<run-id>/<vm-label>/`.
Live collect reads `vms.json` and uses scp to copy each VM's `results.json` and
`TEST_REPORT.md` from that remote shard directory into local shard artifact
directories.
Live server evidence collection runs `kubectl` probes for EMQX, Video Cloud API,
IoT Device Shadow, PostgreSQL, Redis/Valkey, ingress/nginx, and host/pod
resources. Partial probe failure is written as `complete=false` evidence so the
final report remains `INCOMPLETE`.
Aggregate reads collected shard results plus `server-evidence.json` and writes
the run-level `plan.json`, `results.json`, and `TEST_REPORT.md`. Any shard with
`load_generator_health.saturated=true` forces `INCOMPLETE`.
Use `list-vms --live --run-id <run-id>` to inspect leftover load-generator VMs
by tag before cleanup.

If a run fails before the normal cleanup path, use the emergency cleanup helper:

```sh
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh
loadtests/home-100k/scripts/cleanup-home-100k-vms.sh --yes
```

The first command is dry-run and prints every matched Linode VM. The `--yes`
form deletes VMs whose label starts with `home-100k` or whose tags include
`home-100k`.

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
- Bottleneck assessment.

If IoT Device Shadow evidence or server evidence is missing, the report status
must be `INCOMPLETE`, not `PASS`.

If load-generator saturation invalidates the run, the report must say so
instead of attributing the bottleneck to the server.

## Relationship To The Legacy 10K MQTT Helper

The existing `rtk-cloud mqtt-loadtest` helper remains useful as a lower-level
MQTT shard and aggregation reference. The 100K home load test owns the product
scenario, IoT Device Shadow semantics, VM lifecycle, and report contract.
