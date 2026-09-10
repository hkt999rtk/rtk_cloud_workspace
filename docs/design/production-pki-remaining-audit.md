# Remaining production PKI work: evidence audit

Current policy: MFA is optional future human user-login functionality and is
not required for present milestone acceptance. Devices use certificate/key
authentication and never human MFA. Earlier MFA references below are historical;
role checks, independent approvals and custody/recovery evidence remain required.

## Current scope and sources of truth

Four broad milestones remain: trust consumers/live sessions (active);
backup/recovery and SDK integration; provider/hardware compatibility; and
staging/custody/recovery qualification (deferred). The fresh dev Device milestone
is complete according to its [five-item completion audit](production-pki-dev-acceptance-completion.md).
Legacy-data migration is not required for dev; no MFA feature is part of current
acceptance. Preserve ordinary human login and distinct approval roles.

The [current trust-consumer acceptance table](production-pki-trust-consumers.md#current-acceptance-status-2026-09-09-scope-review)
is the sole current progress record. The ledger and dated checkpoints below are
historical evidence, not competing plans or completion counters.

| Document | Responsibility |
| --- | --- |
| [Platform PKI contract](../../repos/rtk_cloud_contracts_doc/platform_pki.md) | Normative hierarchy, identity domains and key-custody boundaries. |
| [Implementation ledger](production-pki-implementation.md) | Original implementation decisions and dated source/test evidence. |
| [Trust-consumer plan](production-pki-trust-consumers.md) | Fixed active acceptance groups, current statuses and immediate execution order. |
| [Detailed remaining execution plan](production-pki-remaining-implementation-plan.md) | Implementation steps, tests and exit criteria for the 18 open checkpoints; no separate completion counter. |
| This scope audit | Scope decisions and limitations; references historical evidence without duplicating it. |
| [Dev Service runbooks](../../scripts/pki-service-dev/README.md) | Phase-specific operational preconditions and recovery; not production deployment tooling or proof of completion. |

## Scope review completion (2026-09-09)

The requested bounded review is complete: inventory/classification, requirement
mapping, duplicate-document consolidation and progress reconciliation (4/4).
This closes the review work package, not the trust-consumer milestone. It is a
scope review, not a line-by-line correctness or security certification.

### Measured change surface

Snapshot: workspace `38d50a5`, base `414aa0b8`. Submodule comparisons use
the old/new gitlinks in that workspace diff, not each repository's local main.
Counts below include implementation, deployment, tests and documentation.

| Repository | Changed files | Added lines | Removed lines |
| --- | ---: | ---: | ---: |
| Workspace (including six gitlink entries) | 114 | 27,520 | 32 |
| Video Cloud | 386 | 49,374 | 477 |
| Cloud Client | 141 | 21,405 | 407 |
| Account Manager | 33 | 2,237 | 19 |
| Ameba WebRTC | 42 | 1,664 | 194 |
| Cloud Admin | 16 | 815 | 16 |
| Contracts | 9 | 183 | 60 |

Earlier local-main comparisons were preliminary and missed some gitlink changes.
These are cumulative branch differences, not files created by the current review,
and do not attribute every inherited change to PKI work.

Workspace additions comprise 14,052 documentation lines, 4,703 test lines and
5,779 dev acceptance/probe lines: approximately 89% supporting material by path
classification. Video Cloud additions comprise 23,096 test lines (47%), 4,163
documentation lines (8%) and 22,115 implementation/deployment lines (45%).
The latter category includes configuration and command adapters; it is not a
measurement of core business logic. Cross-platform SDK tests need language-aware
classification; no aggregate claim that the entire branch is mostly tests is made.

### Scope decisions

| Change family | Decision and reason |
| --- | --- |
| Registry, approved issuer policy, key ownership, issuance/renewal/revocation, caller adapters | Keep: these implement the design's actual identity and custody boundaries. Prefer the existing shared identity/transport owners when finishing integrations. |
| Negative, restart, response-loss and active-session tests | Keep: these verify distinct failure behavior; reducing file count alone is not a reason to remove them. |
| Dev acceptance/bootstrap/recovery scripts | Keep within dev operations; keep phase-specific guards outside request handlers and ordinary login. Reuse existing helpers; do not build a generic deployment framework for this work. |
| Matched backup/recovery and SDK lifecycle work | Retain implemented work under its existing milestone. Do not count it as complete dev trust-consumer acceptance or expand it while closing the factory package. |
| Provider/hardware, staging qualification and legacy fleet migration | Retain historical/local work; live qualification is deferred to the corresponding scope. No dev legacy-data migration or database reset is needed for this review. |
| Repeated status narratives | Consolidate: 58 exact duplicate sections occupied 1,956 lines in this audit. Keep their headings/anchors, replace bodies with links to the original ledger and retain unique evidence. |
| Core business logic and login | Keep simple: reuse one managed identity and transport owner per workload, preserve current human login, and keep optional future MFA outside this milestone. Do not create parallel core flows to satisfy test harnesses. |

### Corrections and next execution boundary

- **Internal inconsistency:** 50%, 88%, 93% and 95% were presented as current
  progress without a stable denominator. Current reporting uses the six original
  acceptance groups; only inventory is fully closed. Service v2 activation is a
  partial transport prerequisite, not completion of all management consumers.
- **Evidence attribution error:** the factory inventory credited listener
  adoption/renewal as factory lifecycle evidence. Corrected: factory runtime
  still uses its legacy static client credential.
- **Historical duplication:** implementation history is maintained in the ledger;
  other summaries link to it. Future commits update current acceptance rows and
  concise evidence references instead of copying the same narrative into several files.
- **Operational gap:** the interrupted factory seed never started (private image
  pull lacked credentials); zero factory Service issuance rows were observed.
  Its temporary provisioner was closed and its exact pending Pod removed.
  The retained PVC and failed report require explicit reconciliation before reuse.
  The seed must use a private subdirectory, not the group-accessible PVC root.

Next valuable implementation remains the six-criterion factory work package in
the active plan. Reconcile the failed seed, complete real factory adoption and
receipts, then qualify renewal/retirement. Do not advance progress for image
builds, helper additions or commits alone. The remaining host, root-policy and
App/relay acceptance is still required before this broad milestone can close.

## Historical evidence index

Dated entries below retain their original scope and may describe superseded
milestone numbering or staging prerequisites. They do not override current scope.

## 2026-09-08 live revocation gate, failed consumer and terminal restart

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-live-revocation-gate-failed-consumer-and-terminal-restart).

## 2026-09-08 live complete broker consumer and cross-CA replacement

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-live-complete-broker-consumer-and-cross-ca-replacement).

## 2026-09-08 broker installed bundles and first-issuer bootstrap

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-broker-installed-bundles-and-first-issuer-bootstrap).

## 2026-09-08 broker installed Root trust and receipts

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-broker-installed-root-trust-and-receipts).

## 2026-09-08 broker terminal-authority handling

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-broker-terminal-authority-handling).

## 2026-09-08 broker Device CRL receipt implementation

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-broker-device-crl-receipt-implementation).

## 2026-09-08 real MQTT replacement and restart checkpoint

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-real-mqtt-replacement-and-restart-checkpoint).

## 2026-09-08 MQTT callback deployment checkpoint

Service `9d11534` adds the missing independent broker-service mTLS callback and
is deployed only on the separate dev PKI API. Real callback authorization and
identity/route isolation tests pass; the public Device listener retains its
revocation denial and persisted trust. The workspace image now packages
`pkibroker`, and dev broker transport identities are prepared. See the
[fresh dev record](production-pki-fresh-dev-rehearsal.md#mqtt-callback-transport-deployed-2026-09-08).

This resolves the callback transport prerequisite. Actual EMQX deployment,
cache/lease enforcement, MQTT session replacement/revocation, fresh Product/device
provisioning and repeatable complete-run evidence remain. No overall milestone
is closed, and the existing broker and staging remain untouched.

## 2026-09-08 Product revocation and HTTP session checkpoint

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-product-revocation-and-http-session-checkpoint).

## 2026-09-08 fresh device lifecycle checkpoint

Enrollment with a device-generated key, one-slot admission, direct API mTLS and
two new-key renewals passed. The second renewal survived certissuer/API restarts
before acknowledgment with exact response replay. Only successor-key acknowledgment
completed cutover, after which the old identity was denied. Source `74906d7` fixes
the timestamp discrepancy discovered during live replay; `86c0417` fixes workload
auth validation and separates Device renewal trust from service caller authority.

The recorded dev acceptance cases for steps 1–3 pass. Next are revocation/live
sessions and repeatable complete-run evidence. Unknown provider-signing failures,
strict OpenSSL service-certificate compatibility, and narrower runtime database
roles are not qualified by this result. The five overall areas remain open;
no staging or legacy fleet migration was attempted.

## 2026-09-08 fresh dev hierarchy checkpoint

Root, Brand and Product v2 are active with real API trust acknowledgment. The
OpenBao mount collision is fixed in `1067380`; Product key generation remains
inside OpenBao with a separate exact controller policy. Fresh acceptance steps
1–2 passed for this dev simulation. Steps 3–5 remain: device enrollment/renewal,
revocation/live sessions, and repeatable full-lifecycle evidence. No broader
milestone or staging/custody qualification is marked complete. See the
[fresh dev rehearsal](production-pki-fresh-dev-rehearsal.md).

## 2026-09-08 independent audit-history recovery checkpoint

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08-independent-audit-history-recovery-checkpoint).

## 2026-09-08 staging packaging checkpoint

Service commit: `08be3b6`. Validation passed: three renderer unit tests,
local linux/amd64 image build and controller executable/usage smoke check,
default-PKCS11 release bundle verification, workflow YAML parsing, shell syntax
and diff checks. No push, PR, remote CI or staging deployment occurred.

Local staging preparation now includes the controller in the shared image and
release bundle, with an offline renderer for separate migration, role grants and
controller deployment phases. Runtime credentials are separate from migration
credentials; deployment rendering requires a digest-pinned image and explicit
consumer IDs. See the [service rollout runbook](../../repos/rtk_video_cloud/docs/pki-staging-rollout.md).

Read-only inspection found OpenBao Pending with CSI volume mount failures; the
public root/CRL query could not start. No storage repair or live change occurred.
The [preflight](production-pki-legacy-staging-preflight.md) records prerequisites.
All five live legacy checklist items remain open; item 1 is partially progressed.
The five broader milestones remain unchanged, with legacy migration active.

Active acceptance work is **milestone 1: legacy migration/device replacement**.
See the [fixed five-item rollout checklist](production-pki-legacy-rollout.md).
Local reporting preparation does not complete a live cohort acceptance item.

Read-only staging discovery is recorded in the
[2026-09-08 preflight](production-pki-legacy-staging-preflight.md): 226 successful
source issuances and 204 active candidate devices. The live PKI schema/controller
and complete legacy root/CRL evidence are prerequisites still missing; no candidate
is yet declared eligible or migrated. Item 1 has progressed, not completed.

Completed local MQTT host work is recorded in [Managed EMQX host identity](production-pki-emqx-host.md).
EMQX owns MQTT TLS termination; `pkibroker` is an outbound session-management
worker. The fixed four-item plan separates local supervisor implementation from
real broker, cluster and recovery qualification.

Initial audit: 2026-09-07 against workspace `dc4638d` and client `c863c27`.
Current-state review: 2026-09-08 against workspace `76e1e13` and Video Cloud
`7e0aaf2`. See [domain/host inventory](production-pki-domain-host-inventory.md)
for the next concrete implementation gap.
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
| 5. Staging/custody/recovery qualification | Offline ceremony CLI, recovery tools and runbooks exist. | Real authenticated identities and independent custodians, escrow/restore ceremony, failure-domain/seal approval, live matched recovery and post-backup security reconciliation, measured RPO ≤15 min and RTO ≤4 h. Production remains disabled. |

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
   coverage. App runtime registry selection, durable single-owner claims, response validation
   and status-checked replay are implemented behind an opt-in setting in Video
   Cloud `4532ff8`. Known-serial pending-outcome recovery is implemented in `06a11d2`.
   Restricted App issuer/controller database grants and lineage locking are
   implemented and exercised under non-owner roles in `07672e6`.
   App certificate/token verification, API CRL fetch/install/acknowledgment,
   broker and TURN sweeps with prepared-digest acknowledgment, durable revocation
   and recurring CRL publication are now locally implemented. App lost-serial
   recovery, read-only provider lineage/leaf checks and full per-issuer registry
   recovery inventory are implemented through `7e0aaf2`. The appended checkpoints
   retain the corresponding tests and limits; these are no longer missing-code
   items. Real host adoption, firmware/media lifetime enforcement and cluster
   eviction timing remain qualification requirements.
   Gateway/server issuance still uses the legacy Device signer in
   `internal/certissuer/service.go:signGateway`. Its DNS allowlist is deployment
   configuration, and it has no independent registry issuer selection or durable
   single-owner claim. `onlineClientRole` and `ConfigureClientRole` support Device
   and App only. Service, dedicated MQTT and OpenBao TLS online profiles and host
   adoption remain concrete implementation gaps. The new domain/host inventory
   records their required boundaries and next implementation order.

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


## Viewer local teardown checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#viewer-local-teardown-checkpoint-2026-09-08).


## Go viewer authorization lifetime checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#go-viewer-authorization-lifetime-checkpoint-2026-09-08).


## Native viewer expiry watchdog checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#native-viewer-expiry-watchdog-checkpoint-2026-09-08).


## Native authorization recheck and lease checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#native-authorization-recheck-and-lease-checkpoint-2026-09-08).


## TURN grant authorization checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#turn-grant-authorization-checkpoint-2026-09-08).


## Coturn cancellation adapter checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#coturn-cancellation-adapter-checkpoint-2026-09-08).


## Recurring TURN controller checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#recurring-turn-controller-checkpoint-2026-09-08).


## Atomic signaling closure checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#atomic-signaling-closure-checkpoint-2026-09-08).


## PKI preflight/session association checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#pki-preflightsession-association-checkpoint-2026-09-08).


## Native preflight/session association checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#native-preflightsession-association-checkpoint-2026-09-08).


## Cloud-client preflight reference checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#cloud-client-preflight-reference-checkpoint-2026-09-08).


## App leaf revocation checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#app-leaf-revocation-checkpoint-2026-09-08).


## App provider publication checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#app-provider-publication-checkpoint-2026-09-08).


## Recurring App CRL worker checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#recurring-app-crl-worker-checkpoint-2026-09-08).


## App-only API CRL consumer checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#app-only-api-crl-consumer-checkpoint-2026-09-08).


## Broker/TURN App CRL consumer checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#brokerturn-app-crl-consumer-checkpoint-2026-09-08).


## App issuer and certificate recovery checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#app-issuer-and-certificate-recovery-checkpoint-2026-09-08).


## App lost-serial recovery checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#app-lost-serial-recovery-checkpoint-2026-09-08).


## App registry recovery inventory checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#app-registry-recovery-inventory-checkpoint-2026-09-08).


## Approved private server issuer policy checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#approved-private-server-issuer-policy-checkpoint-2026-09-08).


## App recovery end-to-end permission correction (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#app-recovery-end-to-end-permission-correction-2026-09-08).


## Durable private server signing claims checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#durable-private-server-signing-claims-checkpoint-2026-09-08).


## 2026-09-08 — Private server provider and gateway integration

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--private-server-provider-and-gateway-integration).


## 2026-09-08 — Server uncertain-outcome recovery

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--server-uncertain-outcome-recovery).


## 2026-09-08 — Private server revocation publication protocol

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--private-server-revocation-publication-protocol).


## 2026-09-08 — Private server registry verification and TLS admission

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--private-server-registry-verification-and-tls-admission).


## 2026-09-08 — Established private server connection enforcement

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--established-private-server-connection-enforcement).


## 2026-09-08 — API and log-ingester MQTT server trust integration

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--api-and-log-ingester-mqtt-server-trust-integration).


## 2026-09-08 — Domain-scoped server CRL maintenance worker

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--domain-scoped-server-crl-maintenance-worker).


## 2026-09-08 — MQTT exact-CRL consumer acknowledgments

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#2026-09-08--mqtt-exact-crl-consumer-acknowledgments).


### Private server recovery verification checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#private-server-recovery-verification-checkpoint-2026-09-08).


### Private server restored-registry inventory checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#private-server-restored-registry-inventory-checkpoint-2026-09-08).


### Controller OpenBao transport integration checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#controller-openbao-transport-integration-checkpoint-2026-09-08).


### Certificate-issuer OpenBao transport adoption checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#certificate-issuer-openbao-transport-adoption-checkpoint-2026-09-08).


### HTTP consumer exact-CRL acknowledgment checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#http-consumer-exact-crl-acknowledgment-checkpoint-2026-09-08).


### API Account Manager Service transport checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#api-account-manager-service-transport-checkpoint-2026-09-08).


### Factory certificate-issuer Service transport checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#factory-certificate-issuer-service-transport-checkpoint-2026-09-08).

### Factory Account Manager Service transport checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#factory-account-manager-service-transport-checkpoint-2026-09-08).

### Approved Service client identity policy checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#approved-service-client-identity-policy-checkpoint-2026-09-08).

### Durable Service client issuance and verification checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#durable-service-client-issuance-and-verification-checkpoint-2026-09-08).

### Service client reconciliation and revocation checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#service-client-reconciliation-and-revocation-checkpoint-2026-09-08).

### Authenticated Service client issuance checkpoint (2026-09-08)

Video Cloud `73fdaa2` closes the cert-issuer integration gap for new Service client
credentials. A dedicated direct-mTLS provisioner route creates a durable registry
claim before the pinned OpenBao Service issuer is called. Exact replay returns the
recorded certificate without a second signature; changed caller/subject/CSR/TTL/
metadata conflicts. The route rejects forwarded identities, unauthorized callers,
non-approved subjects, SANs, requested extensions and lifetimes above 90 days.

Configuration is explicit and fail closed: the feature is disabled by default,
requires the Service issuance migration and OpenBao, has a separate provisioner CN
policy, and prevents automatic schema mutation. Its 90-day leaf ceiling remains
independent from the shared maximum needed by Device/App issuance.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Service client issuance is
no longer a local implementation gap. Remaining work includes Service listener and
client-host adoption, renewal ownership, periodic CRL consumption/publication and
restore qualification. No push, PR, remote CI, deployment or custody action. Goal
remains active.

Full Go tests, targeted race tests, vet, formatting and diff checks passed. Tests
cover the authenticated route, fixed OpenBao role/mount, replay/conflict behavior,
CSR profile and coexistence with longer shared TTL settings. No production
acceptance gate is claimed closed.

### Registry-checked Service client renewal checkpoint (2026-09-08)

Video Cloud `791a323` closes the server-side Service client self-renewal gap. The
current mTLS chain must correspond to its original successful unrevoked receipt,
approved identity, independently pinned root and fresh signed CRLs. Only then can
the same exact Service identity submit a successor CSR through the existing
durable claim and fixed OpenBao role. Missing root/registry/CRL evidence fails
unavailable; revoked or altered identity evidence is denied.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Server-side Service client
lifecycle APIs are locally implemented. The open integration work is private-key
and certificate storage on real hosts, automatic renewal scheduling, listener and
outbound-client adoption, exact CRL acknowledgment, restored-inventory checks and
live qualification. No push, PR, remote CI, deployment or custody action. Goal
remains active.

Full Go tests, targeted race tests, vet, formatting and diff checks passed. Tests
exercise renewal admission, successor issuance and registry denial. No production
acceptance gate is claimed closed.

### Host-owned Service credential store checkpoint (2026-09-08)

Video Cloud `835d50d` closes the local file-backed Service private-key persistence
primitive. Keys are generated on the workload host, stored in an atomic `0600`
state file, and never placed in the PKI registry. Pending request/key/CSR state is
durable before network issuance and survives restart without changing the request.
Validated successor chains are promoted atomically; a bad response or mismatched
key leaves the previous credential usable.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. The remaining integration
gap is to orchestrate initial issuance and renewal around this store, expose the
active credential to actual Service clients/listeners, install exact CRLs and
collect host/recovery evidence. Hardware-backed non-exportable key support remains
in the provider/platform milestone. No push, PR, remote CI, deployment or custody
action. Goal remains active.

Full Go tests, the targeted race test, vet, formatting and diff checks passed.
No production acceptance gate is claimed closed.
# Service credential trust correction (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#service-credential-trust-correction-2026-09-08).

### Service client integration milestone completed locally (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#service-client-integration-milestone-completed-locally-2026-09-08).

### Service client provider recovery checkpoint (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#service-client-provider-recovery-checkpoint-2026-09-08).

### Authenticated private-server renewal API milestone (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#authenticated-private-server-renewal-api-milestone-2026-09-08).

### Managed certificate-issuer server-host milestone (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#managed-certificate-issuer-server-host-milestone-2026-09-08).



### PKI controller managed server-host milestone (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#pki-controller-managed-server-host-milestone-2026-09-08).

### Managed EMQX host implementation (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#managed-emqx-host-implementation-2026-09-08).

### Milestone 1 local cohort reporting preparation (2026-09-08)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#milestone-1-local-cohort-reporting-preparation-2026-09-08).

### Governed MQTT host and actual-client checkpoint (2026-09-10)

Historical evidence is maintained in the [implementation ledger](production-pki-implementation.md#governed-mqtt-host-and-actual-client-checkpoint-2026-09-10). This closes T8; MQTT root-policy replacement remains R3 and the remaining cross-host outage matrix remains T11.
