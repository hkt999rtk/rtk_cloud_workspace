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
   coverage. App runtime registry selection, durable single-owner claims, response validation
   and status-checked replay are implemented behind an opt-in setting in Video
   Cloud `4532ff8`. Known-serial pending-outcome recovery is implemented in `06a11d2`.
   Restricted App issuer/controller database grants and lineage locking are
   implemented and exercised under non-owner roles in `07672e6`.
   App API certificate authentication now requires registry receipts and signed
   intermediate/root CRLs in `15e08ba`. Provider evidence discovery for unknown
   serials, automatic App revocation/CRL publication, backup/restore adapters and
   live-session enforcement remain. App bearer-token provenance and current
   registry/CRL checks at issuance, validation and refresh are implemented in
   `de8d6cd`. App MQTT lease and operator-sweep wiring is implemented in `9c55992`
   with HTTP fixture evidence; real EMQX acceptance remains. WebRTC signaling
   creator provenance/revalidation is implemented in `3d6498d` with memory/Redis
   protocol fixture evidence. Established peer connections and TURN allocations
   still need actual lifetime enforcement; closing a signaling record is insufficient. Configured provider response validation was
   implemented in `6522c24`; the real-provider test in `4532ff8` corrected its
   rejection of valid leaves without the optional Basic Constraints extension.
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


## Viewer local teardown checkpoint (2026-09-08)

Native WebRTC SDK commit `27ccaef` closes the viewer peer before state callbacks,
token acquisition and remote cleanup. Failed starts close locally before reporting
failure. The close callback runs once, blocks reentrant session destruction, and
retains its error for subsequent explicit cleanup. Peer destroy retains final
resource ownership; explicit close/destroy still cleans up cloud session records.

Validation: macOS AppleClang core build and all 10 configured CTest tests passed
in `/private/tmp/rtk-webrtc-pki-core` (POSIX HTTP, Ameba host tests and examples
disabled). Tests assert local closure before token refresh/cloud cleanup,
immediate failed-start teardown, once-only close, close failure propagation and
reentrant close/destroy rejection. No hardware or live relay qualification.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Autonomous viewer authorization/expiry
checks and existing TURN allocation termination remain open. No push, PR, remote
CI, deployment or custody operation occurred. Goal remains active.


## Go viewer authorization lifetime checkpoint (2026-09-08)

WebRTC SDK commit `0475139` binds the Go viewer's Pion media lifetime to the
original owner context, requested duration, mandatory server expiry and original
token expiry when supplied. Invalid/expired server expiry fails connection and
cleans up locally and remotely. The effective deadline cannot be extended by
subsequent token refresh. The example waits for local session termination.

A 10-second watcher rechecks the original session/token through the existing
answer endpoint, each request bounded by five seconds. Denial, malformed response
or network failure ends the lifetime. A separate cancellation/deadline watcher
closes the actual peer independently of network checks and token/remote cleanup;
Done signals local teardown and further PLI requests are rejected. Custom token
providers with zero expiry add no token bound; requested/server/owner bounds still
apply. Production mTLS providers supply JWT expiry. Registry revocation checks
require the matching registry-aware server implementation.

Validation from `repos/rtk_ameba_webrtc/packages/golang`: `CGO_ENABLED=0 GOWORK=off
go test ./...`, `GOWORK=off go test -race ./...` and `GOWORK=off go vet ./...` passed.
A final targeted race test also passed after strengthening the rotating-token
fixture. Coverage includes original-principal polling, server/token/owner expiry,
owner cancellation, invalid expiry, blocked token cleanup, and actual Pion
ICE/DTLS/SRTP H.264 receipt followed by viewer transport closure while the device
remains live. Evidence is local macOS; live registry propagation, load, platform
scheduling and relay allocation termination remain unqualified.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Native C viewer autonomous lifetime checks,
other host/domain wiring, and TURN allocation termination remain open. No push,
PR, remote CI, deployment or custody operation occurred. Goal remains active.


## Native viewer expiry watchdog checkpoint (2026-09-08)

WebRTC SDK commit `edfc170` requires native viewer backends to implement
`set_expiry`. The SDK arms the earlier requested-duration/original-token deadline
before ICE/offer work, validates server RFC3339 expiry after creation, and only
tightens lifetime. Invalid clock/expiry or backend rejection fails admission.
The existing device RFC3339 parser is shared unchanged with the viewer. Custom
backends and consumers must rebuild for the changed struct layout; native shared
ABI/SONAME is now 1, confirmed via macOS install-name inspection. No release was
published. Zero token expiry adds no token-specific bound; duration/server bounds
remain required.

The libdatachannel backend owns an independent 100ms wall/steady-clock watchdog.
It closes/deletes the PeerConnection and its C API track, serializes deletion
against offer/answer/stats operations, wakes gathering on expiry, and rejects
reactivation or lifetime extension. It drops new RTP callbacks after observing
expiry. Backend media/state callbacks must signal the host rather than reenter
session/backend operations. Stats failure lets the native example leave its
200ms loop and perform cloud cleanup. Host scheduling and native close latency
still affect observed shutdown time. Native registry revocation polling and
forced removal of remote TURN allocations are not implemented by this checkpoint.

Validation: core build/10 tests passed; final shared libdatachannel build with
POSIX HTTP and Ameba host tests passed all 21 CTests. Actual H.264 over direct
Pion and local TURN fixture paths now verifies native peer expiry after media
receipt; adapter tests verify shortening, no extension/reactivation, and required
admission. Missing backend support and malformed server expiry are rejected.
A separately instrumented ThreadSanitizer adapter test passed with halt_on_error.
All evidence is local macOS, not physical firmware or deployment qualification.

Reproduce the full suite with `cmake --build /private/tmp/rtk-webrtc-pki-peer -j6`
and `ctest --test-dir /private/tmp/rtk-webrtc-pki-peer --output-on-failure`.
ThreadSanitizer executable: `/private/tmp/rtk-webrtc-pki-tsan/rtk_libdatachannel_peer_test`.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: native live authorization rechecks
and remote TURN allocation termination. No push, PR, remote CI, deployment or
custody operation occurred. Goal remains active.


## Native authorization recheck and lease checkpoint (2026-09-08)

WebRTC SDK commit `113b53f` adds `rtk_session_poll` / C++ Session::poll, wired to
the native example's serialized 200ms loop. Every 10 seconds the SDK rechecks the
original session/token through Wait Answer with a five-second request budget.
It neither refreshes the original identity nor reapplies returned SDP. Failure,
empty response, expiry or clock rollback closes locally before reporting failure.
Original-token memory is explicitly cleared on failure/close/destruction. The
symbol is exported by the shared library and both export manifests.

The developing native ABI 1 backend now also requires refresh_authorization: a
30-second monotonic lease, renewable only while alive and bounded by the fixed
hard session expiry. Successful checks renew it; missing polls, blocked signaling
or prolonged setup expire it. The independent backend watchdog closes the actual
peer even if the C host loop is stuck. The POSIX adapter caps total Wait Answer
request timeout by the supplied budget using a per-call config copy. Custom
backends/consumers must rebuild and implement the same lease contract. Effective
certificate revocation checking requires the registry-aware server endpoint.

Validation: the full native shared build passed all 22 CTests, including direct
and local TURN H.264 paths. The core test verifies original-token successful
recheck followed by denial/local closure. A real backend test renews at 15 seconds,
remains alive beyond the original 30-second lease, then stops renewal and observes
closure near 45 seconds; expired renewal cannot revive it. This 45-second lease
test also passed ThreadSanitizer with halt_on_error. A subsequent targeted HTTP
test passed a short-budget timeout followed by a longer-budget successful call,
verifying transport config is not permanently shortened.

Reproduce: `cmake --build /private/tmp/rtk-webrtc-pki-peer -j6` then
`ctest --test-dir /private/tmp/rtk-webrtc-pki-peer --output-on-failure -j4`.
TSan: `TSAN_OPTIONS=halt_on_error=1 /private/tmp/rtk-webrtc-pki-tsan/rtk_libdatachannel_peer_test --lease`.
All evidence is local macOS; scheduling, physical hosts, deployment load and
registry/CRL propagation require independent qualification.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: remote TURN allocation termination
and remaining host/domain wiring. No push, PR, remote CI, deployment or custody
operation occurred. Goal remains active.


## TURN grant authorization checkpoint (2026-09-08)

Video Cloud commit `e9bbab5` persists TURN-bearing preflight grant receipts in App
PKI mode before returning credentials. Save failure denies issuance. Original
principal, exact issued ICE entries and expiry survive service reconstruction;
PKI preflight/session IDs use separate random UUID namespaces. Preflight records
cannot be consumed as signaling sessions. Memory store copies ICE slices and
principals to prevent returned-object mutation of authoritative receipts.

AuthorizeTURNUsername is the relay controller's policy prerequisite: it requires
canonical PKI usernames, exact persisted issuance, live credential/record/token
expiry, open state and current original-principal registry/CRL authorization.
Unknown, altered, unbound and legacy grants are denied. Legacy API behavior stays
unchanged. Full Go suite and signaling race suite passed, including memory/Redis
protocol persistence, revocation, expiry boundary, close, failed-save and alias
checks. Real coturn cancellation has not yet been implemented or qualified.

Upstream coturn 4.6.2 source confirms administrative exact-user session listing
and numeric allocation cancellation commands. Next is restricted CLI/controller
wiring, cancellation confirmation and repeat-allocation handling. A preflight
grant remains independent of its subsequent signaling session; closing that
session alone does not identify/close the preflight grant. Principal revocation
and expiry still apply to both. See service docs/turn.md for precise limits.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. No push, PR, remote CI, deployment or
custody operation occurred. Goal remains active.


## Coturn cancellation adapter checkpoint (2026-09-08)

Video Cloud commit `51a6200` adds internal/turncontrol: loopback-only authenticated
coturn CLI access, bounded session inventory, exact username/ID reread before
numeric cancellation, and disappearance confirmation. A bounded sweep rechecks
denied authorization immediately before cancellation and reports only confirmed
removals. No arbitrary command API is exposed; malformed/truncated inventories
and unsafe command input are rejected. The sweep assumes a dedicated managed
relay where unknown users are denied. Each socket operation has a five-second
bound; scan/check contexts are two minutes/five seconds respectively.

Validation: full Go suite passed; package race tests passed. A disposable cached
coturn 4.6.3 localhost container created two real UDP allocations, cancelled one
without affecting the other, preserved allocations on stale username/ID input,
accepted an idempotent cancellation, demonstrated reallocation, and cancelled
both remaining allocations with a denying sweep. The pinned source reference is
coturn 4.6.2; do not conflate that reference with the tested container version.
The fixture container was removed. Test config and opt-in commands are recorded
in service docs/turn.md. A final targeted race suite passed after checking that
late authorization success cannot override an expired check context.

Recurring process/registry-validator wiring, deployment secret delivery, health
reporting, fleet latency and hostile-client qualification remain outstanding.
The adapter is not running automatically. Reallocation remains possible with
unexpired shared-secret credentials, so repeated sweeps and further qualification
are required; this is not a hard revocation cutoff guarantee.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. No push, PR, remote CI, deployed relay
change or custody operation occurred. Goal remains active.
