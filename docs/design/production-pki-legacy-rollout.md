# Milestone 1: legacy migration/device replacement

## Current execution order: dev first

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
