# Milestone 1: dev PKI acceptance and later legacy migration

## Current milestone status: complete (2026-09-08)

The authorized fresh dev PKI milestone is **100% complete, with zero remaining
items**. The committed tool's uninterrupted run passed 12/12 checks; independent
inspection verified all five acceptance requirements and final cleanup. See the
[completion audit](production-pki-dev-acceptance-completion.md).

Four broader areas remain: other trust consumers/live sessions (next priority),
backup/recovery and SDK, provider/hardware compatibility, and deferred
staging/custody/recovery qualification. Legacy migration remains deferred under
the user's dev-scope correction. Historical progress entries below retain their
original checkpoint counts; this section is the current status.

## Current execution scope: fresh dev PKI (2026-09-08)

The user authorizes temporary accounts/devices and resetting dev databases when
useful. Existing dev data is not a migration requirement. Staging is untouched.
This supersedes the earlier dev legacy-canary prerequisite and requests for real
operator/device-owner input. Use distinct temporary accounts and ordinary login
for simulated approval testing; MFA stays disabled and never applies to devices.

The completed dev acceptance sequence is:

1. Deploy the controller and Account Manager with management mTLS and RS256 login;
   bootstrap temporary administrators and distinct approval accounts.
2. Create a fresh Cloud/Product and governed Device Root/Brand/Product hierarchy;
   verify offline Root/Brand and OpenBao Product key custody without exporting
   Product private keys. Install and acknowledge actual runtime trust.
3. Enroll a fresh device using a device-generated key, then prove direct-mTLS
   authentication and certificate renewal using the resulting hierarchy.
4. Verify revoked credentials are rejected and active sessions close, including
   MQTT with a compatible broker; exercise restart and interrupted renewal.
5. Retain repeatable dev setup and result evidence, with explicit pass/fail status.

Use schema migrations to initialize current schemas where needed; do not spend
this rehearsal reconciling old issuance histories or replacing the existing dev
fleet. A dev database reset is authorized, not mandatory. Reset only identified
dev databases and coordinate affected workloads if a reset is actually needed.
Legacy migration/device replacement remains deferred to environments that need
it; its checklist below is retained for that later rollout, not as a dev gate.
Dev test approvals demonstrate software behavior, not independent human custody.

The completed dev execution and its historical checkpoints are in the
[fresh dev rehearsal](production-pki-fresh-dev-rehearsal.md).

## Historical migration execution notes (superseded for dev)

MFA is not a current prerequisite. It is an optional future human user-login
feature, disabled by default through `PKI_REQUIRE_USER_MFA=false`. Ordinary
authenticated user sessions may invoke PKI with current role checks and distinct
approvals intact; assertions must honestly retain `mfa=false`. If explicitly
enabled later, both Account Manager and the controller enforce recent verified
human MFA. Device authentication, renewal and replacement never require MFA.
RS256 request binding, management mTLS, independent approval and certificate
validation remain. This replaces the earlier proposed dev-only exception.

Current bootstrap progress: the dev migration and grants Jobs completed, and
dedicated runtime database access passed live read/denial checks. TLS/key/trust
dependencies and a deny-by-default OpenBao Kubernetes auth binding are installed.
The controller and Account Manager signer cutover remain pending, starting with
the actual consumer management identities and direct-mTLS transport binding.
See the latest dev preflight checkpoint; older discovery counts below describe
the environment before this bootstrap. No governed import/replacement has run.

See the [dev source preflight](production-pki-legacy-dev-preflight.md): 111 current
credentials selected uniquely from issuance history, with full chain/CRL checks
passed after refreshing the two expired dev CRLs. Registry eligibility, target
hierarchy, device possession and canary execution remain pending.

Local implementation `558c101` adds dev-specific immutable windows, imports,
verification and report environment binding. Legacy/replacement PostgreSQL tests,
race checks, vet and diff checks passed. Tests exercise actual dev replacement
through acknowledgment and predecessor rejection, an expired staging window
alongside a fresh dev window, repeated schema upgrade, immutable windows,
cross-environment denial and production exclusion. At that checkpoint no dev
schema/deployment mutation had been performed; the latest checkpoint above records
the subsequent live database bootstrap.

Read-only dev inventory: 1,331 successful issuance rows; 111 active entitlements;
1,211 successful rows map to active-entitlement device IDs, and 120 have no
entitlement. Every active entitlement has at least one successful source row.
These are issuance counts, not unique eligible device/certificate counts. Resolve
current credentials and historical issuance before selecting a fixed canary.
Initial discovery found zero public `pki_*` tables. The controller/schema and
governed target hierarchy must be prepared before import. Source history and dev
public chain/CRL validation are the next prerequisites; staging stays untouched.

Per the user's correction, perform migration testing on `video-cloud-dev` before
any further staging work. Dev discovery verified context `lke649805-ctx` and a
Ready OpenBao pod. Staging is out of scope for subsequent operations. The earlier
authorized staging unseal completed before the correction; no staging lineage
export or migration followed it.

Implement explicit `dev` support alongside `staging` in legacy inventory/import,
immutable environment-specific migration windows, verification and reporting.
Do not label dev as staging or weaken independent approval, signed chain/CRL,
entitlement, replacement or cutoff checks. Production remains excluded.
Test dev import/replacement locally, then use only dev credentials and workloads
for the canary rehearsal. Dev evidence must be labeled as such and cannot close
staging/custody qualification. The original five acceptance items remain intact.

Local deployment preparation is available in the
[staging rollout runbook](../../repos/rtk_video_cloud/docs/pki-staging-rollout.md):
controller packaging plus separate offline migration, grants and runtime
manifests. This closes a packaging gap, not a live acceptance item. The
[staging preflight](production-pki-legacy-staging-preflight.md) also records the
OpenBao volume mount blocker. All five live items below remain open.

This is the next active acceptance milestone. The existing governed staging
inventory/import, immutable overlap window, Product replacement, acknowledgment
and reconciliation flows are implemented. Do not replace those controls or
invent approval/device evidence to close this milestone.

## Fixed acceptance checklist

1. Identify the actual staging environment and fixed device cohort; inventory
   every source record and resolve omitted/ineligible devices.
2. Prepare and independently approve a canary import with current public legacy
   CA/CRL evidence; verify deployment of the required consumer trust.
3. Measure canary replacement and successor acknowledgment, old-credential
   rejection and active-session termination; test restart/interrupted renewal.
4. Expand approved batches and account for every residual device/fingerprint,
   including pending signing, unacknowledged replacements, offline devices,
   revocations, cutoffs and remediation.
5. Withdraw legacy trust from every consumer when eligible, verify live rejection,
   and retain rollout/recovery evidence and the final residual inventory.

All five live acceptance items remain open until evidence exists. Local code/test
completion does not close a live item. The staging environment and target cohort
must be identified before operational execution; independent governed approval
and real device access remain required at their respective steps.

## Local preparation

Completed in Video Cloud `e6c17a4`: the report, runbook, PostgreSQL-backed lifecycle
test, affected-package tests, race checks and vet. The canonical staging environment
has now been identified and inspected read-only. See the
[staging preflight](production-pki-legacy-staging-preflight.md): 226 source
issuances, 204 active candidate devices, no live PKI registry schema, and incomplete
legacy root/CRL evidence. Cohort eligibility and canary selection remain incomplete;
no live checklist item is marked complete.

Add `pkicontroller legacy-progress OPERATION_ID` to report one completed,
immutable import batch under a repeatable-read, read-only transaction. Report
replacement state separately from current legacy registry acceptance. Signing
pending and issuance without acknowledgment are not completion. Expired/revoked
old credentials remain in the denominator and are not labeled as replacements.

The report is bounded by the existing 50-entry import limit. It is not global
inventory, current successor usability, CRL installation evidence, or a
trust-withdrawal authorization. Refreshed imports can overlap; do not sum batches
without deduplicating fingerprints. Preserve original operation IDs and compare
dated reports for the same cohort.

Local tests must exercise governed import, claim, completion, acknowledgment,
cutoff and residual accounting. Existing migration/replacement regression tests
must pass. Then commit locally and record the exact evidence and remaining gates.

The other four broader acceptance areas remain: trust consumers/live sessions;
backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification.
