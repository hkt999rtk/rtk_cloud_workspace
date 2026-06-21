# Baseline 100K Scenario

This scenario is the default first baseline for `home-100k`.

## Conditions

- Server target: current staging/LKE env-root.
- Load-generator runtime: ephemeral Linode VMs.
- Region model: single region.
- Devices: 100,000.
- Users: 5,000.
- Devices per user: 10.
- Load-generator VMs: 5 total `mixed` VMs.
- Each `mixed` VM owns one `device-mqtt` task shard with 20,000 devices and
  one `user-app` task shard with 1,000 users.
- Target window: ramp directly to 100,000 connected devices.

## Device Mix

- 50,000 lights.
- 20,000 air conditioners.
- 30,000 smart meters.

## Presence Mix

- 85,000 online steady devices.
- 10,000 offline desired queue devices.
- 5,000 flapping reconnect devices.

## IoT Device Shadow Success Path

```text
app writes desired
cloud computes delta
device receives delta or gets shadow after reconnect
device applies desired
device writes reported
cloud clears delta
```

## Required Negative Coverage

- Device credentials cannot write desired state.
- Cross-user access must fail for devices outside the user's assignment.
- Stale version writes must be counted as version conflicts.
- Missing shadow/server evidence must produce an incomplete report.
