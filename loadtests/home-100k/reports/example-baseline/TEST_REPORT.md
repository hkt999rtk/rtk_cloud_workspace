# 100K Home IoT Device Shadow Load Test Report

- Run ID: example-baseline
- Status: INCOMPLETE
- Missing server evidence

## Test Conditions
- Env root: `cloud_env/staging/lke`
- Brand: `RTK`
- Region: `us-sea`
- Devices: 100000
- Users: 5000
- Devices per user: 10

## Scenario Mix
- Device mix:
  - Light: 50000
  - Air conditioner: 20000
  - Smart meter: 30000
- Presence mix:
  - Online steady: 85000
  - Offline desired queue: 10000
  - Flapping reconnect: 5000

## Device Scenario
- Online steady devices subscribe to shadow delta, apply desired state, and write reported state.
- Offline desired queue devices reconnect, call shadow get, apply queued desired state, and clear delta.
- Flapping reconnect devices verify reconnect sync, version handling, and duplicate-apply prevention.

## User Scenario
- App users login, list authorized devices, read shadows, write desired state, and wait for reported state plus delta clear.

## IoT Device Shadow Scenario
- Canonical path: app writes desired -> cloud computes delta -> device receives delta or reads shadow after reconnect -> device writes reported -> delta clears.
- Device desired writes must be rejected; stale versions must be counted as conflicts.

## Stage Results
| Stage | Devices | MQTT connect | Reconnects | Shadow get p50 | Shadow get p95 | Shadow get p99 | Desired->reported p95 | Offline desired p95 | Delta clear |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 25k | 25000 | 99.95% | 1250 | 45.00 ms | 90.00 ms | 135.00 ms | 280.00 ms | 840.00 ms | 100.00% |
| 50k | 50000 | 99.95% | 2500 | 50.00 ms | 100.00 ms | 150.00 ms | 310.00 ms | 930.00 ms | 100.00% |
| 75k | 75000 | 99.95% | 3750 | 55.00 ms | 110.00 ms | 165.00 ms | 340.00 ms | 1020.00 ms | 100.00% |
| 100k | 100000 | 99.95% | 5000 | 60.00 ms | 120.00 ms | 180.00 ms | 370.00 ms | 1110.00 ms | 100.00% |

## Server Evidence
- server evidence: incomplete
- emqx: available=false
- video_cloud_api: available=false
- iot_device_shadow: available=false
- postgres: available=false
- redis_valkey: available=false
- ingress_nginx: available=false
- host_pod_resources: available=false
