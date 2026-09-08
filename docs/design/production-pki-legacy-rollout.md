# Milestone 1: legacy migration/device replacement

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
test, affected-package tests, race checks and vet. The staging environment/cohort
has been requested but not selected. No live checklist item is marked complete.

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
