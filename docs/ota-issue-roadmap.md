# OTA Campaign Interface-First Issue Roadmap

Status: supporting-note.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-04-30.

## Purpose

This roadmap maps the Realtek Connect+ OTA campaign-depth gap to concrete
GitHub issues by owner repository. It is not the normative contract. The
normative cross-repo OTA campaign interface source is
`repos/rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`, with route inventory in
`repos/rtk_cloud_contracts_doc/HTTP_API.md`.

## Strategy

OTA campaign work uses an interface-first rollout:

1. define firmware campaign policy vocabulary in the contracts repository
2. define issue ownership in the workspace repository
3. open implementation issues that link back to the pushed documents
4. implement backend, SDK, and website changes in follow-up PRs

The first phase is campaign policy core only. It defines schedule, time-window,
cancel/archive, and user-consent policy vocabulary without committing to approval
workflow, dashboards, analytics, staged percentage rollout, or mobile UX.

## Owner Matrix

| Capability | Owner repository | First issue type |
| --- | --- | --- |
| Firmware campaign policy contract | `hkt999rtk/rtk_cloud_contracts_doc` | Contract/interface docs |
| Campaign state and device report vocabulary | `hkt999rtk/rtk_cloud_contracts_doc` | Contract/interface docs |
| Campaign backend behavior | `hkt999rtk/rtk_video_cloud` | Backend rollout policy alignment |
| SDK firmware helper alignment | `hkt999rtk/rtk_cloud_client` | SDK interface/helper alignment |
| Public OTA availability wording | `hkt999rtk/rtk_cloud_frontend` | Website content alignment |

## Issues To Open After Docs Are Pushed

These interface-first issues were opened and closed after the contracts and
workspace planning documents were pushed. Do not reopen them for the next
implementation phase; use `docs/implementation-gap-backlog.md` for remaining
backend/SDK/frontend work.

### `hkt999rtk/rtk_cloud_contracts_doc`

#### `[OTA] Define firmware campaign and rollout policy contract`

Summary: make `FIRMWARE_CAMPAIGN.md` the shared source for OTA campaign policy
semantics.

Dependencies:

- pushed `FIRMWARE_CAMPAIGN.md`
- existing `HTTP_API.md`

Acceptance criteria:

- contract defines release, campaign, targeting, rollout policy, schedule,
  time-window, user-consent policy, cancel, and archive vocabulary
- contract distinguishes current firmware lifecycle routes from a complete
  campaign engine
- first phase explicitly excludes approval workflow, dashboards, analytics,
  staged percentage rollout, and mobile UX
- README and HTTP API docs point to the campaign contract

#### `[OTA] Define campaign state and device report vocabulary`

Summary: establish common campaign and device rollout state names for follow-up
backend, SDK, and UI work.

Dependencies:

- pushed `FIRMWARE_CAMPAIGN.md`

Acceptance criteria:

- campaign states include `draft`, `scheduled`, `active`, `paused`, `canceled`,
  `archived`, and `completed`
- device rollout states include `pending`, `eligible`, `waiting_for_window`,
  `waiting_for_user`, `downloading`, `applied`, `failed`, `canceled`, and
  `skipped`
- document states that existing backend states `pending`, `applied`, `failed`,
  and `canceled` are a subset of the campaign vocabulary
- device report vocabulary includes current version, target version, status, and
  reason/error text

### `hkt999rtk/rtk_video_cloud`

#### `[OTA] Align firmware rollout backend with campaign policy contract`

Summary: map existing release/target/rollout backend behavior to campaign policy
semantics before adding full campaign engine behavior.

Dependencies:

- `rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`
- existing firmware release, target, rollout, report, cancel, and download routes

Acceptance criteria:

- backend docs explain how current release/target/rollout objects map to campaign
  concepts
- unsupported campaign policies such as schedule, time-window, user consent, and
  archive are explicit rather than silently implied
- implementation plan identifies required backend changes for campaign policy
  enforcement
- tests or follow-up test plan cover current basic rollout behavior and future
  policy gates separately

Post-verification note: latest `rtk_video_cloud` documents this mapping in
`docs/firmware-campaign-alignment.md` and keeps unsupported campaign policies
explicit. The full campaign engine is still future implementation work:
first-class campaign resource, policy enforcement, archive, and campaign-level
cancel.

### `hkt999rtk/rtk_cloud_client`

#### `[OTA] Align SDK firmware helpers with campaign contract`

Summary: align SDK firmware helper documentation and package behavior with the
campaign contract.

Dependencies:

- `rtk_cloud_contracts_doc/FIRMWARE_CAMPAIGN.md`
- current firmware route helpers in native, Android, iOS, and JavaScript/TypeScript

Acceptance criteria:

- SDK docs identify which firmware lifecycle helpers are available today
- campaign policy helpers not implemented return or document explicit
  `unsupported_capability` behavior
- SDK result/error vocabulary can represent campaign/device rollout states from
  the contract
- SDK does not decide campaign eligibility or policy; backend remains source of
  truth

### `hkt999rtk/rtk_cloud_frontend`

#### `[OTA] Align public OTA copy with implementation status`

Summary: keep Realtek Connect+ OTA copy accurate while preserving the campaign
vision.

Dependencies:

- pushed `FIRMWARE_CAMPAIGN.md`
- workspace gap analysis

Acceptance criteria:

- OTA page distinguishes available firmware lifecycle foundation from planned
  campaign policy engine
- schedule/time-window/user-consent/cancel/archive are labeled according to
  implementation status
- approval workflow, dashboards, analytics, and staged percentage rollout are not
  described as available in phase one
- copy links or internal references point to the contract source where
  appropriate

## Commit And Issue Workflow

1. Commit and push `rtk_cloud_contracts_doc` documentation first.
2. Sync nested contracts submodules and push docs-only submodule pointer commits.
3. Commit and push workspace planning docs with the new contracts submodule
   pointer.
4. Open issues using GitHub links to the pushed contract and workspace docs.
5. Keep issue bodies focused on acceptance criteria rather than duplicating the
   full design text.

## Next Implementation Backlog

Use `docs/implementation-gap-backlog.md` for the owner-repo issues that remain
after the interface-first phase. The highest-priority OTA backlog is in
`hkt999rtk/rtk_video_cloud`:

- first-class firmware campaign resource and persistence
- schedule, time-window, and user-consent policy enforcement
- campaign-level cancel and archive behavior

SDK and frontend follow-up should depend on the backend campaign APIs instead of
inventing availability or eligibility semantics locally.
