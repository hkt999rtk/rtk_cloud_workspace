# Linode 10k MQTT Load Test

Status: implemented v1 script
Owner: rtk_cloud_workspace

## Summary

The 10k MQTT load test validates whether the current Linode staging topology can
handle the first AWS baseline workload: 2,500 users, 10,000 MQTT-only devices,
four devices per user, 100% average MQTT connectivity, and no camera/WebRTC
traffic.

The workflow is intentionally two phase:

1. Prepare the fleet once.
2. Run repeatable load-test shards against that prepared fleet.

This avoids rebuilding 10,000 users/devices for every capacity run and keeps
destructive VM lifecycle operations out of the load script.

## Prepare Phase

Plan the preparation commands:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest prepare \
  --env-root cloud_env/staging \
  --brandname RTK \
  --plan
```

Run preparation:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest prepare \
  --env-root cloud_env/staging \
  --brandname RTK \
  --run
```

Default preparation values:

- Users: `2500`
- Devices: `10000`
- Device mix: `light=3334,air_conditioner=3333,smart_meter=3333`
- Device output: `<env-root>/devices/test_device`
- User artifact: `<env-root>/artifacts/users/<brand>-users-*.json`
- Bind artifact: `<env-root>/artifacts/device-bind/<brand>-device-bind-*.json`

The prepare command wraps the existing `create-users`,
`generate-load-devices`, `bind-devices`, and `validate-device-bind` commands.
It does not create or delete Linode VMs.

## Run Phase

Run one local shard:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest run \
  --env-root cloud_env/staging \
  --brandname RTK \
  --shard-index 0 \
  --shard-count 1
```

The default profile is `baseline-10k`:

- Ramp-up: `10m`
- Duration: `30m`
- Telemetry interval: `5m`
- State interval: `1h`
- Command rate: `1` command per device per day equivalent
- Default concurrency: `250`

Run a planning pass for multiple load-generator hosts:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest run \
  --env-root cloud_env/staging \
  --brandname RTK \
  --hosts-file load-hosts.txt \
  --plan
```

`load-hosts.txt` contains one SSH target per line, for example:

```text
root@203.0.113.10
root@203.0.113.11
```

Run distributed shards:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest run \
  --env-root cloud_env/staging \
  --brandname RTK \
  --hosts-file load-hosts.txt \
  --remote-workspace /root/rtk_cloud_workspace \
  --remote-env-root /root/rtk_cloud_workspace/cloud_env/staging/linode
```

The script uses SSH to run one shard per host, copies each shard
`results.json` back to the local output directory, and then aggregates the
shards. The first version assumes the operator has already provisioned the
load-generator VMs.

If the load-generator hosts do not already have the runner and env-root
artifacts, add `--sync-remote`:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest run \
  --env-root cloud_env/staging \
  --brandname RTK \
  --hosts-file load-hosts.txt \
  --remote-workspace /root/rtk_cloud_workspace \
  --remote-env-root /root/rtk_cloud_workspace/cloud_env/staging/linode \
  --sync-remote
```

`--sync-remote` copies `scripts/go` and the env-root to each remote host over
SSH before running the shard. The env-root contains user artifacts, device
private keys, and certificates, so these load-generator hosts must be treated as
secret-bearing test infrastructure.

## Aggregate Phase

If shards are run manually, aggregate them afterwards:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-loadtest aggregate \
  --input-dir cloud_env/staging/linode/artifacts/mqtt-loadtest/<run>/shards
```

Aggregate outputs:

- `results.json`
- `TEST_REPORT.md`

The aggregate report includes selected devices, commands attempted, commands
passed, success rate, p95 latency, and p99 latency.

## Capacity Evidence

Collect these metrics during every 10k run:

- EMQX connected clients, connection churn, dropped clients, message rate, CPU,
  memory, and network throughput.
- API request-token latency and error rate.
- PostgreSQL CPU, connections, slow queries, locks, and write I/O.
- Redis/Valkey memory, ops/sec, and evictions.
- NATS publish/ack latency, pending messages, and redelivery.
- Edge nginx TLS handshakes, upstream latency, and error rate.
- Host CPU, memory, disk I/O, and network throughput on all Linode VMs.

Use the measured saturation point to update both capacity planning and the AWS
cost model. Do not claim that the existing Linode provision supports 10,000
always-connected devices until the 10k run passes with acceptable server
metrics.
