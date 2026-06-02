# Admin Dashboard Redesign Spec

Status: draft.

Author: Kevin Huang

Audience:

- rtk_cloud_admin frontend developers
- rtk_video_cloud backend developers
- rtk_account_manager backend developers
- PM / QA

Related contracts:

- [TELEMETRY_INSIGHTS.md](../repos/rtk_cloud_contracts_doc/TELEMETRY_INSIGHTS.md)
- [FIRMWARE_CAMPAIGN.md](../repos/rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md)
- [FRONTEND_STYLE.md](../repos/rtk_cloud_contracts_doc/FRONTEND_STYLE.md)
- [HTTP_API.md](../repos/rtk_cloud_contracts_doc/HTTP_API.md)

---

## Background And Motivation

The current admin dashboard was designed from a system-implementation perspective.
It surfaces internal operational vocabulary (`cloud_activation_pending`,
`dead_lettered`, `video_cloud_devid`) and exposes service-health indicators that
are meaningful only to platform engineers. Customer operators — the primary
users — cannot derive actionable answers to their everyday questions from the
current UI.

Typical customer operator questions the current dashboard cannot answer:

- Which of my devices have been unreliable this week?
- How many devices still run the old firmware, and how is the OTA rollout going?
- Is my fleet's signal quality degrading?
- How often are streams actually working?

This spec defines the redesign required to make the dashboard customer-useful
while preserving an operator/internal view for platform teams.

---

## Goals

1. Surface the data customer operators actually need to manage their fleet.
2. Remove internal implementation details from the customer-facing view.
3. Separate customer views from platform/operator views cleanly.
4. Ground all new data surfaces in existing contracts (TELEMETRY_INSIGHTS,
   FIRMWARE_CAMPAIGN) to avoid inventing new vocabulary.

---

## Information Architecture

### Current Navigation (4 sections)

```
Customer Fleet   →  summary metrics + service health + recent ops
Devices          →  device table
Provisioning     →  operations log
Platform Admin   →  customer count + service health
```

### Proposed Navigation (2 top-level views)

**Customer View** — default landing for org operators:

```
Overview         →  fleet health summary (new)
Devices          →  improved device table
Firmware & OTA   →  new section
Stream Health    →  new section
Groups           →  new section (depends on device-group feature)
```

**Platform View** — separate top-level context for internal operators:

```
Service Health   →  moved from customer view
Operations       →  operations log (existing, refined)
Audit Log        →  new section (uses existing audit_events table)
```

The two views should have clearly differentiated entry points. A nav switcher
or separate route prefix (`/admin/ops`) is acceptable. Do not intermix customer
and platform content on the same page.

---

## Section 1: Fleet Health Overview (new)

This replaces the current Customer Fleet Dashboard.

### Purpose

Give the customer operator a single-glance answer to: "Is my fleet healthy right
now, and has it been healthy recently?"

### KPI Strip (top of page)

Four metric cards:

| Card | Value | Source |
|---|---|---|
| Online | count of devices with readiness `online` / total | `/api/summary` (existing) |
| Online Rate (7d) | percentage of time the fleet was collectively online over last 7 days | new endpoint (see Section 7) |
| Needs Attention | count of devices with health `warning` or `critical` | new endpoint (see Section 7) |
| Active Streams | count of currently open stream sessions | new endpoint (see Section 7) |

Remove from this page: service health widget, open operations count, lifecycle
state timeline diagram. Those belong in Platform View.

### Fleet Health Trend Chart

A 7-day or 30-day line chart showing daily:

- % of fleet online
- count of devices in `warning` or `critical` health state

Data source: derived from `device.health.summary` events per
TELEMETRY_INSIGHTS.md. Backend aggregates per org per day.

Toggle between 7d / 30d view. Default 7d.

### Device Health Distribution (donut or bar)

A breakdown of the current fleet by health state:

- Healthy
- Warning (one or more signals: `low_rssi`, `recent_reboot`, `low_memory`)
- Critical (one or more signals: `recent_crash`, `offline_risk`)
- Unknown (no telemetry received)

Each segment links to the Devices page pre-filtered by that health state.

Data source: `device.health.summary` latest event per device per org.

### Recent Alerts (table, last 10)

| Column | Content |
|---|---|
| Time | relative timestamp |
| Device | device name (not video_cloud_devid) |
| Signal | `low_rssi` / `recent_crash` / `offline_risk` etc., from TELEMETRY_INSIGHTS |
| Health | resulting health state |

No service-internal fields. No operation type codes. Customer-readable signal
names only.

---

## Section 2: Devices Table (improved)

### Columns

Remove `video_cloud_devid`. Add `Firmware` and `Health`.

| Column | Content | Notes |
|---|---|---|
| Device | name + serial number | unchanged |
| Organization | org name | unchanged |
| Model | model | unchanged |
| Firmware | current firmware version | new, from `firmware.version.observed` events |
| Health | colored badge: Healthy / Warning / Critical / Unknown | new, from `device.health.summary` |
| Status | readiness state | keep existing values, title-case display only |
| Signal | RSSI quality bucket: Good / Fair / Poor / — | new, from `device.health.rssi_sample` |
| Last Seen | timestamp | unchanged |
| Actions | Provision / Deactivate | unchanged |

### Status Display

Keep the underlying readiness state values from PRODUCT_READINESS.md. Use
title-case for display:

| Internal value | Display label |
|---|---|
| `registered` | Registered |
| `claim_pending` | Claim Pending |
| `local_onboarding_pending` | Local Onboarding |
| `cloud_activation_pending` | Cloud Activation |
| `activated` | Activated |
| `online` | Online |
| `failed` | Failed |
| `deactivation_pending` | Deactivating |
| `deactivated` | Deactivated |

Do not invent new status names that break mapping to the contract vocabulary.

### Filters

Add filter chips above the table:

- Health: All / Healthy / Warning / Critical / Unknown
- Status: All / Online / Activated / Pending / Failed / Deactivated
- Signal: All / Good / Fair / Poor
- Firmware: version picker (populated from observed versions in the org)

Existing free-text search is kept.

### Device Detail Drawer / Page

When clicking a device row, open a side drawer or detail page showing:

- Device identity (name, serial, model, org)
- Current health summary + contributing signals
- Firmware version + last updated time
- RSSI history (7d sparkline)
- Uptime history (7d sparkline)
- Recent events (last 10 telemetry events from this device)
- Active stream status (is there currently an open session?)

This panel should not expose video_cloud_devid or internal operation IDs to
non-platform users.

---

## Section 3: Firmware & OTA (new)

### Purpose

Answer: "Which firmware versions are running across my fleet, and how is the
current OTA campaign progressing?"

### Firmware Distribution Chart

A horizontal bar chart or table showing, for the current org:

| Row | Count | % of fleet |
|---|---|---|
| v1.2.4 (latest) | 42 | 68% |
| v1.2.3 | 18 | 29% |
| v1.1.x and older | 2 | 3% |

Data source: latest `firmware.version.observed` event per device per org.

Clicking a version row filters the Devices table to that version.

### Active Campaigns Table

For each active firmware campaign in the org:

| Column | Content |
|---|---|
| Campaign | campaign name or ID |
| Target Version | target firmware version |
| Policy | `normal` / `force` / `scheduled` / `time_window` |
| Progress | progress bar: applied / total targeted |
| Applied | count of devices with rollout_status `applied` |
| Pending | count `pending` + `eligible` + `downloading` |
| Failed | count `failed` |
| Skipped | count `skipped` |
| State | campaign state badge |
| Started | start timestamp |

Use campaign and device rollout vocabulary from FIRMWARE_CAMPAIGN.md exactly.
Do not rename `applied` → "done" or `skipped` → "excluded".

### Per-Campaign Drill-Down

Clicking a campaign row opens a device-level breakdown table:

| Column | Content |
|---|---|
| Device | name |
| Current Version | firmware_version from telemetry |
| Target Version | from campaign |
| Rollout Status | `pending` / `applied` / `failed` / `skipped` / etc. |
| Reason | failure or skip reason when available |
| Last Updated | timestamp |

Data source: `firmware.rollout.status_changed` events per device per campaign,
and `/query_firmware_rollout` existing route.

---

## Section 4: Stream Health (new)

### Purpose

Answer: "Are my devices' video streams actually working for end users?"

### Fleet Stream KPIs

| Card | Value |
|---|---|
| Stream Success Rate (7d) | % of stream requests that succeeded |
| Avg Stream Duration | average session length in minutes |
| Active Sessions Now | count of currently open stream sessions |
| Devices Never Streamed | count of `online` devices with zero stream history |

### Stream Success Rate Trend

7d / 30d line chart: daily stream request count vs. success rate (%).

Break down by mode: RTSP / Relay / WebRTC on the same chart using three lines.

### Per-Device Stream Table

Devices sorted by stream failure rate descending (worst first):

| Column | Content |
|---|---|
| Device | name |
| Mode Used | most common stream mode |
| Success Rate (7d) | % |
| Total Requests (7d) | count |
| Last Stream | timestamp |
| Status | badge from device readiness |

Data source: new backend aggregation endpoint. See Section 7.

---

## Section 5: Device Groups (new)

Blocked on the device group feature described in the device-group-firmware-campaign
issue set. This section is a placeholder; it will be detailed in a follow-up
spec once the group CRUD API is implemented.

When available, the Groups section provides:

- Group list with device count, online rate, firmware distribution per group
- Ability to view Fleet Health / Firmware / Stream metrics scoped to one group
- Group creation and member management (requires account_manager device group API)

---

## Section 6: Platform View — Retained And Refined

### Service Health (moved from Customer View)

The Account Manager, Video Cloud, and SQLite health indicators remain. Move
them entirely out of the customer-facing view. They belong in the Platform View
only.

When a service is in `demo` mode, add a visible "Demo Mode" banner to the
Platform View page, not to any customer-facing page.

### Operations Log (refined)

The Operations page is retained for platform operators.

Changes:

- Add a `Friendly Summary` column that translates internal operation types into
  readable sentences. Example:
  - `DeviceProvisionRequested` → "Provisioning requested"
  - `dead_lettered` → "Failed after retries — needs investigation"
- Keep raw type and state values visible as secondary text for platform engineers.
- Add filter by state: All / Pending / Succeeded / Failed / Dead Lettered.

### Audit Log (new, minimal)

Surface the existing `audit_events` table (actor, action, target, created_at)
as a read-only table in Platform View. No new data model required; the table
already exists in the store.

---

## Section 7: New API Endpoints Required

The following new endpoints must be added to `rtk_cloud_admin` (or proxied from
upstream services) to power the redesigned sections.

### `GET /api/fleet/health-summary`

Returns current health distribution and 7d/30d trend for the org.

Response:

```json
{
  "org_id": "org-123",
  "current": {
    "healthy": 42,
    "warning": 8,
    "critical": 2,
    "unknown": 5
  },
  "online_rate_7d_pct": 91.4,
  "trend": [
    {
      "date": "2026-04-27",
      "online_pct": 88.2,
      "warning_count": 10,
      "critical_count": 1
    }
  ]
}
```

Data source: derived from `device.health.summary` events (TELEMETRY_INSIGHTS)
aggregated per org per day. First implementation may use rtk_video_cloud's
`/get_statistics` as a seed source until full telemetry ingestion exists.

### `GET /api/fleet/firmware-distribution`

Returns firmware version distribution and active campaign summaries for the org.

Response:

```json
{
  "org_id": "org-123",
  "versions": [
    { "version": "v1.2.4", "count": 42, "pct": 68.0, "is_latest": true },
    { "version": "v1.2.3", "count": 18, "pct": 29.0, "is_latest": false }
  ],
  "campaigns": [
    {
      "campaign_id": "campaign-2026-04",
      "target_version": "v1.2.4",
      "policy": "normal",
      "state": "active",
      "applied": 42,
      "pending": 18,
      "failed": 2,
      "skipped": 1,
      "total": 63,
      "started_at": "2026-04-01T00:00:00Z"
    }
  ]
}
```

Data source: `firmware.version.observed` and `firmware.rollout.status_changed`
events; existing `/query_firmware_rollout` route from HTTP_API.md.

### `GET /api/fleet/stream-stats`

Returns stream health metrics for the org over a time window.

Query params: `window=7d` (default) or `window=30d`.

Response:

```json
{
  "org_id": "org-123",
  "window": "7d",
  "success_rate_pct": 94.1,
  "avg_duration_seconds": 312,
  "active_sessions": 3,
  "never_streamed_count": 2,
  "by_mode": {
    "rtsp":   { "requests": 120, "success_rate_pct": 96.7 },
    "relay":  { "requests": 45,  "success_rate_pct": 91.1 },
    "webrtc": { "requests": 18,  "success_rate_pct": 88.9 }
  },
  "trend": [
    {
      "date": "2026-04-27",
      "requests": 23,
      "success_rate_pct": 95.6
    }
  ],
  "worst_devices": [
    {
      "device_id": "acct-dev-4",
      "device_name": "factory-line-mqtt",
      "success_rate_pct": 55.0,
      "requests": 20
    }
  ]
}
```

Data source: new stream session event log in rtk_video_cloud, recording each
`/request_stream` and `/api/request_webrtc` call outcome (success/failure,
mode, duration). rtk_video_cloud must expose a query API or push aggregated
facts to rtk_cloud_admin.

### `GET /api/devices/{id}/telemetry`

Returns recent telemetry events and health summary for one device.

Response:

```json
{
  "device_id": "acct-dev-1",
  "health": "warning",
  "signals": ["low_rssi"],
  "firmware_version": "v1.2.4",
  "rssi_7d": [
    { "date": "2026-04-27", "avg_dbm": -71, "quality": "fair" }
  ],
  "uptime_7d": [
    { "date": "2026-04-27", "online_pct": 98.1 }
  ],
  "recent_events": [
    {
      "occurred_at": "2026-04-30T10:00:00Z",
      "event_type": "device.health.rssi_sample",
      "summary": "Signal quality dropped to Poor (−82 dBm)"
    }
  ]
}
```

Data source: `device.health.rssi_sample`, `device.health.summary`,
`device.reboot.reported`, `device.crash.reported` events from
TELEMETRY_INSIGHTS. rtk_video_cloud must make these available via a per-device
query API.

---

## Section 8: Out Of Scope For This Spec

The following are intentionally deferred:

- Device group creation and management UI (depends on device-group feature)
- Alert notification rules and email/webhook delivery
- Multi-org / platform-level fleet aggregation (single-org only for now)
- Stream viewer / live preview
- User roles and permission management
- Audit log export

---

## Section 9: Implementation Dependency Map

```
[rtk_video_cloud] — stream session event log + /api/fleet/stream-stats endpoint
    └── [rtk_cloud_admin] — Stream Health section

[rtk_video_cloud] — telemetry event ingestion (rssi, health, firmware)
    └── [rtk_cloud_admin] — Fleet Health Overview, Device detail, Firmware Distribution

[rtk_account_manager] — device group CRUD API  (separate feature)
    └── [rtk_cloud_admin] — Groups section (blocked, deferred)

[rtk_cloud_admin] — all frontend sections above
    ├── Fleet Health Overview (new)
    ├── Devices Table (improved)
    ├── Firmware & OTA (new)
    ├── Stream Health (new)
    └── Platform View (reorganized)
```

Backend work in rtk_video_cloud is the critical path dependency for
Fleet Health, Firmware Distribution, Stream Health, and Device Detail.
The rtk_cloud_admin frontend changes that don't require new data
(table column cleanup, status label display, platform view reorganization,
audit log surface) can proceed immediately.

---

## Section 10: Acceptance Checklist

For each new frontend section, the implementation must verify:

- No internal IDs (`video_cloud_devid`, operation IDs) are exposed in
  customer-facing views
- State labels use contract vocabulary (title-cased) from PRODUCT_READINESS.md
  and FIRMWARE_CAMPAIGN.md
- Health signals use vocabulary from TELEMETRY_INSIGHTS.md
- Charts display a loading state when data is unavailable
- Empty states are defined (e.g., "No campaigns active", "No stream data yet")
- Tables show the most actionable items first (worst-performing devices at top)
- Color usage follows FRONTEND_STYLE.md tokens
- Platform View content does not appear in Customer View routes
