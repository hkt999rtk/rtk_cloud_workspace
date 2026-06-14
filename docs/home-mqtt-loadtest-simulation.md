# Home MQTT Load-Test Simulation Plan

Status: implemented smoke wrapper
Owner: rtk_cloud_workspace
Last updated: 2026-05-31

## Summary

The first realistic load-test expansion should model a home user operating
MQTT-only devices. It must not require operators to manually assemble user,
device, key, certificate, and binding inputs. The simulation starts from the
same Linode K8s staging environment root used by `provision-k8s` and the E2E flow, for example:

```sh
go run ./scripts/go/rtk-cloud -- mqtt-test \
  --env-root cloud_env/staging \
  --brandname RTK
```

`go run ./scripts/go/rtk-cloud -- mqtt-test` is the direct entry point. Reports are written under
`<env-root>/artifacts/home-mqtt-loadtest/<timestamp>/` and include both
`results.json` and `TEST_REPORT.md`.

The command resolves `cloud_env/staging` to `cloud_env/staging/linode`, reads
the existing user/device artifacts, and runs a "home daily use" workload where
APP actors use Cloud APIs and device actors use mTLS only to bootstrap device
tokens for MQTT.
WebRTC relay, video streaming, storage, clips, and snapshots are explicitly out
of scope for this profile.

## Environment Contract

The runner must use `scripts/go/rtk-cloud/internal/envroot` as the source of truth for local
environment layout:

- `cloud_env_init` resolves `cloud_env/staging` to the provider-specific root.
- `cloud_env_test_devices_dir` locates the generated load-test device fixture.
- `cloud_env_artifacts_dir` locates users and device-bind artifacts.
- `cloud_env_account_manager_env` and `cloud_env_account_manager_state` locate
  Account Manager endpoint and runtime state.
- `cloud_env_video_env` and `cloud_env_video_state` locate Video Cloud and MQTT
  endpoint/runtime state.
- `cloud_env_keys_dir` and `cloud_env_certificates_dir` locate local mTLS and
  certificate material when service env files reference them.

Required inputs:

- `<env-root>/devices/test_device/manifests/devices.json`
- `<env-root>/devices/test_device/manifests/device_ids.txt`
- `<env-root>/devices/test_device/loadtest.env`
- `<env-root>/devices/test_device/devices/<type>/<device_id>/device.cert.pem`
- `<env-root>/devices/test_device/devices/<type>/<device_id>/device.key.pem`
- `<env-root>/devices/test_device/devices/<type>/<device_id>/device.chain.pem`
- latest `<env-root>/artifacts/users/<brand>-users-*.json`
- latest `<env-root>/artifacts/device-bind/<brand>-device-bind-*.json`

If required inputs are missing, unreadable, or inconsistent, the wrapper should
write a redacted `BLOCKED` report. It must not fall back to synthetic users,
synthetic device credentials, broad wildcard MQTT credentials, or hard-coded
staging paths.

## User-Case Model

The workload is a home daily-use scenario. The APP actor represents a real
logged-in user and uses only user/app-scoped credentials. It does not use device
tokens or device certificates.

Expected APP flow:

- Login or reuse credentials from the env-root user artifact.
- If Account Manager returns `app_certificate.status=csr_required`, generate an
  app-local private key and CSR with subject `app-user:<user_id>`, then submit
  `app_csr_pem` through Account Manager so certissuer can sign the app
  certificate over service mTLS.
- Pin the app certificate identity locally; private keys, PEM bodies, and raw
  tokens must not be written to reports.
- Use the pinned app certificate only to call Video Cloud `POST /request_token`
  over mutual TLS and obtain a subject-bound `app` token.
- Open the home screen by listing the user's authorized devices.
- Read current state for assigned light, air-conditioner, and smart-meter
  devices.
- Send light commands through Cloud API: power, brightness, and color
  temperature.
- Send air-conditioner commands through Cloud API: power, mode, target
  temperature, and fan.
- Read or subscribe to smart-meter telemetry and verify freshness.
- Measure command round-trip latency from APP command request to observed
  `command_result` or `state_report`.

Expected device flow:

- Use the per-device mTLS cert/key from env-root only to call
  `POST /request_token` and obtain a device token.
- Connect to MQTT with the issued device token credential.
- Subscribe only to the device's command topic.
- Publish heartbeat/status and capability-specific telemetry.
- Maintain local state across commands.
- Publish `command_result` and `state_report` with the command correlation id.

## Device Models

Light state:

- `power`
- `brightness`
- `color_temperature_kelvin`
- `last_changed_at`

Light behavior:

- `set_power`, `set_brightness`, and `set_color_temperature` are supported.
- Brightness is constrained to `1..100`.
- Color temperature is constrained to the product-supported range documented by
  the implementation issue.
- Apply delay should be deterministic from the run seed, with a realistic range
  such as 100-800 ms.

Air-conditioner state:

- `power`
- `mode`
- `target_temperature_celsius`
- `current_temperature_celsius`
- `fan`
- `last_changed_at`

Air-conditioner behavior:

- `set_power`, `set_mode`, `set_temperature`, and `set_fan` are supported.
- Target temperature is constrained to the product-supported range documented
  by the implementation issue, initially `16..30 C`.
- Current temperature moves gradually toward target temperature over time.
- Apply delay should be deterministic from the run seed, with a realistic range
  such as 300 ms-2 s.

Smart-meter state:

- `power_watts`
- `energy_kwh`
- `voltage_v`
- `current_a`
- `frequency_hz`

Smart-meter behavior:

- Telemetry is read-oriented in the first profile.
- `energy_kwh` must be monotonic.
- `power_watts` should vary within a realistic band, with seeded occasional
  spikes.
- APP telemetry checks should verify message freshness and monotonic counters.

## Developer Issue Plan

Create implementation issues in this order:

1. [`[LoadTest] Add env-root discovery for home MQTT simulation`](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/60)
   - Add the wrapper entry point and preflight discovery.
   - Resolve users, bind artifact, devices, service endpoints, and mTLS files
     from `--env-root`.
   - Produce `BLOCKED` artifacts for missing prerequisites.

2. [`[LoadTest] Add MQTT mTLS device connections from env-root fixtures`](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/61)
   - Load per-device cert/key/chain paths from the device manifest.
   - Connect device actors to the configured MQTT broker with mTLS.
   - Keep credentials out of reports and logs.

3. [`[LoadTest] Add home daily-use APP actor through Cloud API`](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/62)
   - Login users from the users artifact.
   - Model first-login app key generation, CSR submission, app certificate
     pinning, and app token issuance.
   - Exchange the pinned app certificate for a Video Cloud `app` token before
     app-side commands or subscriptions.
   - Use the bind artifact to restrict each APP actor to authorized devices.
   - Send device commands through Cloud API, not direct device credentials.

4. [`[LoadTest] Add stateful light, air-conditioner, and smart-meter models`](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/63)
   - Replace fixed MQTT sample payloads with seeded state machines.
   - Implement state transitions, telemetry cadence, and command result
     correlation.

5. [`[LoadTest] Add home MQTT report metrics and negative authorization checks`](https://github.com/hkt999rtk/rtk_cloud_workspace/issues/64)
   - Report per-user, per-device, and per-capability metrics.
   - Report command round-trip p95/p99 and telemetry freshness.
   - Add negative checks for cross-user device access and cross-device MQTT
     topic access.

## Validation

Preflight validation:

- Env-root resolves correctly.
- Device manifest and `loadtest.env` exist.
- All selected device cert/key/chain files exist and are readable.
- Latest users artifact exists and has mode `0600`.
- Latest bind artifact exists and passes `go run ./scripts/go/rtk-cloud -- validate-device-bind`.

Smoke profile:

- One user.
- Three MQTT-only devices: light, air-conditioner, and smart meter.
- Two-minute duration.
- No WebRTC, viewer, storage, clip, or snapshot coverage.

Real-case profile:

- All users and MQTT-only devices from the latest bind artifact.
- 10-30 minute duration.
- Default command mix: `read=50,light=25,air_conditioner=15,smart_meter=10`.

Acceptance criteria:

- The simulation starts from `--env-root`; scattered manual paths are optional
  overrides only.
- APP traffic includes Account Manager login, app key/CSR bootstrap,
  certificate pinning, mTLS app-token bootstrap, and Cloud APIs.
- Device token bootstrap uses per-device mTLS credentials from env-root; MQTT
  publish/subscribe traffic uses the issued device token.
- Reports include per-user, per-device, and per-capability metrics.
- Reports include command round-trip p95/p99.
- MQTT/home-device coverage passes with success rate at least 95%.
- WebRTC, relay, storage, clip, and snapshot are disabled or reported as
  `NOT_RUN`.
- Reports redact passwords, bearer tokens, private keys, certificate bodies,
  and raw service env secrets.
