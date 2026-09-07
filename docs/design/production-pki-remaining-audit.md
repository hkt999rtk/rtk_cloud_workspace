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
| 2. Trust consumers/live sessions | Go CRL store/refresh/guard/TLS integration; Android durable CRLs, refresh and bound WebSocket; iOS corresponding implementation through `c863c27`; JavaScript durable CRLs, bounded refresh, guard and mTLS/WebSocket lifetime cancellation; server and firmware adapters recorded in the ledger. | Domain-specific App/Gateway/service issuance and consumer coverage remain unproven. Device-specific provider policy and verification cannot prove coverage of other domains. Native SDK now implements protected Device identity/renewal, durable CRLs, periodic refresh, guarded core HTTP/WebSocket mTLS and bounded DNS/TCP/TLS setup. Host wiring, root-policy changes and live firmware/session behavior need evidence. |
| 3. Backup/recovery and SDK integration | `scripts/go/rtk-cloud/internal/recovery` contains physical backup, WAL/PITR, scheduling and rehearsal code; `repos/rtk_video_cloud/internal/pkicontrollerapp/recovery.go` implements recovery checks. Mobile and JavaScript production renewal and trust support exists. | Go now has durable acknowledgment/retirement, mandatory renewal CRLs and resumable session-replacement coordination; durable expiry-driven scheduling is now implemented; host integration remains. Native protected installation/renewal, durable scheduling and core HTTP/WebSocket owner integration are implemented. Application/domain policy adoption remains an implementation gap, not merely a physical test gate. |
| 4. Provider/hardware compatibility | OpenBao policy/workload/Raft artifacts and local provider tests exist. Host Swift, API 35 emulator and native host checks are recorded. | Supported-provider/version and physical Secure Enclave, Android TEE/StrongBox, firmware/ARM and HSM matrix results. Local tests must not be substituted for this evidence. |
| 5. Staging/custody/recovery qualification | Offline ceremony CLI, recovery tools and runbooks exist. | Real MFA identities and independent custodians, escrow/restore ceremony, failure-domain/seal approval, live matched recovery and post-backup security reconciliation, measured RPO ≤15 min and RTO ≤4 h. Production remains disabled. |

## Concrete implementation gaps found

1. **JavaScript implementation gap addressed locally.** P-256 provisioning,
   independent path/profile validation, immutable identity versions, durable prepared
   renewal/receipt/activation/acknowledgment, predecessor retirement and scheduled
   orchestration are now implemented. Signed full CRLs, a durable monotonic journal,
   bounded HTTPS refresh, periodic refresh, expiry/revocation guards and production
   mTLS owner cancellation are implemented. WebSocket session cancellation now lasts
   until closure. Local tests include a signed revocation closing an upgraded native
   mTLS connection and rejecting reuse of the agent. This addresses the previously
   identified legacy-helper gap; real application host wiring, root-policy replacement,
   supported runtime/filesystem behavior and operational qualification remain gates.
   The old renewal endpoint remains an explicitly separate compatibility helper.
2. **Go renewal recovery invariants.** Saved requests now bind predecessor version
   and fingerprint; CSR/key checks, response/transition persistence, durable
   acknowledgment attempts and retirement precede owned fresh successor mTLS.
   Local tests prove lost-response recovery and SDK file-loader rollback denial.
   Renewal entry points now enforce independent roots, the Device profile and signed
   current CRLs, including TLS handshakes and cached receipt recovery. The resumable
   coordinator requires session replacement before acknowledgment and rejects
   overlapping coordinators. Durable expiry-driven scheduling now retains a stable request through activation
   and acknowledgment and clears it only after completion. Real host supervision
   and qualification remain; returning a TLS identity alone does not close
   owner-lifetime integration.
3. **Native trust implementation boundary.** An optional OpenSSL 3.6/cJSON provider
   now implements `validate_with_trust` for certificate-only Device P-256 bundles.
   It verifies independent roots, exact chain/profile, metadata, signed full CRLs
   and private-key possession. Generated RSA/EC fixtures exercise the public SDK
   callback, sanitizer checks pass, and an installed-package consumer links.
   Native durable CRL high-water state is now implemented with signed history,
   monotonic updates and atomic POSIX publication. Protected identity installation/
   renewal, scheduling, public HTTPS refresh and a POSIX session guard are locally
   implemented, including guard-owned periodic refresh and an optional POSIX TLS
   platform for core HTTP/WebSocket ownership with bounded DNS/TCP/TLS setup
   and owner-independent resolver cleanup. Application policy/domain wiring and physical/OpenSSL-provider qualification remain. The
   original fake callback test remains a plumbing test, not the verifier evidence.
4. **Other trust domains and host inventory.** The generic issuer `Scope` has a
   domain. App intermediate provisioning/import and restricted provider signing
   are now implemented in Video Cloud `4899d2f`, with local OpenBao/PostgreSQL
   coverage. App runtime signer selection remains configuration-based; registry
   binding, full provider response validation and App recovery adapters remain.
   Replacement and runtime verification also require domain-specific review.
   Gateway/service profiles remain unimplemented. Inventory real issuance and consumer entry
   points for each required domain and identify missing adapters. A generic schema
   is not proof of end-to-end domain support. This audit has not exhaustively
   certified those entry points.

## Next execution order

- JavaScript production lifecycle and trust primitives are now locally implemented;
  verify application host ownership and policy replacement with the domain inventory.
- Native protected software key/CSR and immutable verified bundle storage are locally
  implemented (client `d9e44a7`); active selection is implemented in `38694dd`.
  Prepared renewal persistence is implemented in `f23213e`; implement
  actual application/domain policy integration
  (bounded DNS/TCP/TLS and deferred resolver cleanup are implemented in `34bccfa`)
  (core HTTP/WebSocket TLS owner wiring is implemented in `830798f`)
  (guard-owned periodic refresh is implemented in `8b177cb`)
  (continuous POSIX trust guard is implemented in `683230c`)
  (public HTTPS CRL refresh is implemented in `55188e4`)
  (durable scheduling/resume is implemented in `bc80e26`)
  (acknowledgment/retirement and finish coordination are implemented in `6346159`)
  (authenticated HTTP renewal is implemented in `48c600f`)
  (wire conversion is implemented in `8d33454`)
  (adapted-bundle response persistence is implemented in `c1e1018`)
  (post-activation request recovery is implemented in `7be4f12`), then verify SDK host ownership with the domain inventory.
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
