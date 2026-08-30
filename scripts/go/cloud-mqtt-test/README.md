# Cloud MQTT and OTA device simulator

`cloud-mqtt-test` contains the workspace's existing actor-separated, Home 100K,
SDK virtual-device, and firmware OTA device load models. Firmware OTA design
and acceptance rules are documented in
[`../../../docs/firmware-ota-virtual-device-load-test-design.md`](../../../docs/firmware-ota-virtual-device-load-test-design.md).

## Firmware OTA canary

Release creation and campaign activation are operator-owned prerequisites. The
simulator requires a credential bundle containing the campaign's provisioned,
bound, MQTT-enabled devices.

From the workspace root:

```sh
workspace_root="$PWD"
(cd scripts/go && GOWORK=off go run ./cloud-mqtt-test \
  --root "$workspace_root" \
  --env-root "$workspace_root/cloud_env/staging/runtime" \
  --test-data-db "$workspace_root/cloud_env/staging/runtime/artifacts/test-data/rtk-test-data.sqlite" \
  --brandname RTK \
  --out-dir .artifacts/ota-canary \
  --profile smoke \
  --load-model ota-device-simulator \
  --run-id "ota-canary-$(date -u +%Y%m%dT%H%M%SZ)" \
  --ota-campaign-id CAMPAIGN_ID \
  --ota-target-version 2.0.0 \
  --ota-current-version 1.0.0 \
  --ota-hardware-revision rev-a \
  --max-connected-devices 10 \
  --concurrency 10 \
  --ramp-up 5s \
  --mqtt-probe true)
```

The run writes the normal `results.json` and `TEST_REPORT.md` plus a protected
`ota-devices.jsonl` containing one redacted row per selected device.

The first OTA check and every subsequent check are staggered per device using
a deterministic truncated normal distribution. The defaults are 10 seconds
minimum, 60 seconds maximum, and a 35-second mean. Override the bounds with
`--ota-poll-min-interval` and `--ota-poll-max-interval` when a qualification
profile requires different pacing.

## Single-generator 10K qualification

Use a campaign whose target snapshot contains the exact 10,000-device fixture
and whose schedule, phase concurrency/rate limits, and failure policy can finish
inside the configured deadline:

```sh
workspace_root="$PWD"
(cd scripts/go && GOWORK=off go run ./cloud-mqtt-test \
  --root "$workspace_root" \
  --env-root "$workspace_root/cloud_env/staging/runtime" \
  --test-data-db /secure/runtime/ota-10k.sqlite \
  --brandname RTK \
  --out-dir .artifacts/ota-10k \
  --profile baseline-10k \
  --load-model ota-device-simulator \
  --run-id "ota-10k-$(date -u +%Y%m%dT%H%M%SZ)" \
  --ota-campaign-id CAMPAIGN_ID \
  --ota-target-version 2.0.0 \
  --ota-current-version 1.0.0 \
  --ota-hardware-revision rev-a \
  --max-connected-devices 10000 \
  --concurrency 250 \
  --ota-http-concurrency 250 \
  --ota-download-concurrency 64 \
  --ota-upgrade-timeout 30m \
  --mqtt-probe true)
```

For sharded execution, use the same run ID, seed, campaign, and versions while
assigning each generator a unique zero-based `--shard-index` and the same
`--shard-count`. The final qualification must merge the JSONL files and prove
there are exactly 10,000 unique device IDs with no omissions or overlap.

Failure profiles are optional and deterministic. For example,
`--ota-install-failure-percent 2` assigns two percent of devices to the
expected `failed` terminal bucket. All failure percentages are mutually
exclusive and must sum to at most 100.
