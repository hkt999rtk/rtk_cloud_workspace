# Remaining production PKI work: evidence audit

Audited on 2026-09-07 against workspace `dc4638d` and client `c863c27`.
This is a gap audit, not production sign-off. It preserves the five outstanding
milestones and distinguishes missing implementation from missing qualification.
No deployment, production key operation, remote CI or external approval was run.

## Why the count remains five

The original checklist mixes implementation and live acceptance in the same
items. Completing an SDK substep does not close the containing milestone. Three
items primarily require operational/physical evidence; two still contain concrete
code gaps. The repeated count is therefore not five equally large coding tasks.

| Milestone | Current implementation evidence | What still prevents completion |
| --- | --- | --- |
| 1. Legacy migration/device replacement | `repos/rtk_video_cloud/internal/pkicontrollerapp/legacy.go`, `internal/pki/legacy.go`, `internal/pki/replacement.go` implement staged inventory/import and device replacement; the ledger records local tests. | Actual staging cohort inventory, approved import, measured replacement/overlap and residual legacy population. Local fixtures do not establish cohort adoption. |
| 2. Trust consumers/live sessions | Go CRL store/refresh/guard/TLS integration; Android durable CRLs, refresh and bound WebSocket; iOS corresponding implementation through `c863c27`; server and firmware adapters recorded in the ledger. | Domain-specific App/Gateway/service issuance and consumer coverage remain unproven. Device-specific provider policy and verification cannot prove coverage of other domains. Native SDK uses a host trust callback. Host wiring, root-policy changes and live firmware/session behavior need evidence. |
| 3. Backup/recovery and SDK integration | `scripts/go/rtk-cloud/internal/recovery` contains physical backup, WAL/PITR, scheduling and rehearsal code; `repos/rtk_video_cloud/internal/pkicontrollerapp/recovery.go` implements recovery checks. Mobile renewal and trust support exists. | JavaScript remains on legacy PKI helpers; Go renewal has low-level helpers but no durable acknowledgment-attempt/retirement orchestration equivalent to the mobile stores. Native provider integration remains host-supplied. These are implementation gaps, not merely physical test gates. |
| 4. Provider/hardware compatibility | OpenBao policy/workload/Raft artifacts and local provider tests exist. Host Swift, API 35 emulator and native host checks are recorded. | Supported-provider/version and physical Secure Enclave, Android TEE/StrongBox, firmware/ARM and HSM matrix results. Local tests must not be substituted for this evidence. |
| 5. Staging/custody/recovery qualification | Offline ceremony CLI, recovery tools and runbooks exist. | Real MFA identities and independent custodians, escrow/restore ceremony, failure-domain/seal approval, live matched recovery and post-backup security reconciliation, measured RPO ≤15 min and RTO ≤4 h. Production remains disabled. |

## Concrete implementation gaps found

1. **JavaScript key and identity lifecycle.** In
   `repos/rtk_cloud_client/packages/javascript/src/index.ts`, `generateDeviceKey`
   originally generated an RSA PEM with default overwrite behavior. The subsequent
   JavaScript provisioning checkpoint fixes this with default P-256, explicit RSA
   compatibility and exclusive publication/reuse. `storeDeviceCert` parses PEM then writes it without an
   independently anchored chain/profile check. `buildMtlsAgent` passes files to
   `https.Agent`; this is not a local device-chain revocation policy.
   `renewDeviceCert` still calls `/api/device/renew_certificate`. Implement a
   protected, retry-safe P-256 identity path, independent trust validation,
   production renewal/receipt/activation/acknowledgment state, and owner lifetime
   integration. Preserve an explicitly documented legacy compatibility boundary.
2. **Go renewal recovery invariants.** In
   `repos/rtk_cloud_client/packages/golang/rtkc/auth/renewal.go`, `Acknowledge`
   directly invokes `renewalPOST`; the caller is told to use fresh successor mTLS
   and never roll back, but the method does not persist acknowledgment-attempt or
   predecessor-retirement state. `LoadCurrentRenewal` reads the current symlink,
   request and TLS key pair. Review the full lifecycle against the mobile durable
   invariants before claiming uniform SDK recovery. Existing Go CRL/TLS lifetime
   support does not close this separate gap.
3. **Native trust implementation boundary.**
   `repos/rtk_cloud_client/packages/native/src/rtkc_certificate_bundle.c` calls
   `validate_with_trust`; the public header requires the host to implement strict
   chain validation. The SDK test supplies that callback. This proves callback
   gating, not an actual provider's cryptographic validation, protected key
   installation or production renewal. Identify and wire the supported provider
   before qualification; do not describe the fake callback as a completed verifier.
4. **Other trust domains and host inventory.** The generic issuer `Scope` has a
   domain, but `internal/pki/openbao_policy.go`, replacement and runtime
   verification contain device-specific restrictions. The controller runbook still
   lists App/Gateway/service work. Inventory real issuance and consumer entry
   points for each required domain and identify missing adapters. A generic schema
   is not proof of end-to-end domain support. This audit has not exhaustively
   certified those entry points.

## Next execution order

- Address the JavaScript identity overwrite/provisioning boundary, then its
  production lifecycle and trust integration.
- Close Go durable acknowledgment/retirement gaps and native provider integration.
- Audit and implement domain-specific issuance/consumer and host wiring gaps.
- Run the corresponding local integration checks; keep qualification evidence
  separate and attributable to the platform/environment actually exercised.
- Perform live/hardware/custody acceptance only within explicitly authorized
  operational scope and with real supplied identities/material.

## Evidence limits

Source inspection found the gaps above; no new runtime tests were run for this
document-only audit. Prior test results remain attached to their implementation
ledger entries. This is not an exhaustive claim that all unlisted requirements
are complete. Completion still requires reviewing every original contract gate,
including trust adoption, compromise handling, escrow and restored security state.
The full goal remains active.
