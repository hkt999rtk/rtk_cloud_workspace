# Remaining production PKI work: evidence audit

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


## Recurring TURN controller checkpoint (2026-09-08)

Video Cloud commit `c8b115d` adds the separate `pkiturn` operator workload with
sweep/watch/health commands. It composes persisted TURN grant validation, current
App registry/CRL verification and confirmed coturn cancellation. Watch waits ten
seconds after each scan. It requires explicit dedicated-relay configuration,
shared signaling Redis, verifier-only database access, numeric loopback CLI and
a private password file. Production remains gated. No schema migrations or
coturn configuration mutations occur. Optional env/systemd assets and binary
packaging are included; they are not enabled or deployed.

Atomic health state records scan start/completion and aggregate counts. Failed,
stale/future results and stalled scans are unhealthy; last completion older than
30 seconds fails health. Dependency failures degrade health while unverifiable
grants remain denied where coturn is reachable. Monitors must invoke the health
command; process liveness alone does not establish scan freshness. Scan/socket/
verification contexts remain bounded; no hard fleet cutoff is claimed.

Validation: full Go suite, targeted race suites, script checks and release bundle
verification passed. An opt-in race integration used disposable PostgreSQL 16,
Redis 8.6.0 and coturn 4.6.3: the assembled one-shot process preserved a recorded
admin grant, cancelled an unknown grant, cancelled the recorded grant after close,
and failed health after a broken registry query. Watch failure/recovery/shutdown
are unit-tested. App certificate/CRL decisions remain separately tested; this
runtime fixture used the explicit admin policy. Both task containers and the
local Redis process were shut down and verified terminal afterward.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: review remaining host/domain wiring
and TURN grant/session association, plus cutoff/load/hostile-client qualification.
No push, PR, remote CI, deployed relay change or custody operation occurred.
Goal remains active.


## Atomic signaling closure checkpoint (2026-09-08)

Video Cloud commit `79260e4` fixes a race found while preparing TURN grant/session
association: Redis SaveAnswer previously read and rewrote the entire record, so
an answer could overwrite concurrent closure and restore TURN authorization.
Answer/close updates now use a bounded optimistic retry with an atomic Lua
snapshot comparison, preserving the existing remaining TTL. Closed/preflight/
expired records reject answers; repeated close preserves its original timestamp.
Deleted keys cannot be recreated by outstanding updates. Memory storage applies
the same lifecycle checks under its mutex.

Validation: full Go suite and signaling race suite passed. Concurrent answer/close
checks passed in memory, the Redis protocol fixture and real local Redis 8.6.0.
The real fixture also verified stale-snapshot rejection after close/deletion and
no TTL extension. Each race case confirms TURN authorization stays denied after
closure. The temporary Redis process was shut down and verified terminal.
Signaling writers now require EVAL/GET/PTTL/SET session-key permissions; controller
receipt readers do not need the write script. No unsafe write fallback exists.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next remains preflight-to-session grant
association, now on top of atomic lifecycle updates. No push, PR, remote CI,
deployment or custody operation occurred. Goal remains active.


## PKI preflight/session association checkpoint (2026-09-08)

Video Cloud `6783af8` accepts the optional `ice_username` create field and
validates the persisted preflight grant against the original principal and
device. Memory locking and Redis compare-and-swap make claims single-use;
session expiry cannot exceed grant expiry. Both records carry reciprocal links.
Closing, expiring or losing the linked record denies subsequent signaling/TURN
authorization. A failed claim returns no credentials; cleanup is best-effort,
but its unbound target cannot authorize and remains subject to TTL cleanup.
Existing relay allocations require the recurring TURN controller to cancel them.

Go SDK `b963e1a` forwards the PKI preflight username, rejects conflicting grants
before creating a peer, and omits the field for legacy/static credentials. The
server OpenAPI and stream contract and Go README describe the behavior.
Association remains optional for existing callers; native and other SDK host
wiring remains unfinished. This does not complete the live-session gate.

Validation: full server Go suite; signaling/HTTP race suites including real local
Redis 8.6.0 concurrent claims; full Go SDK race suite and vet. SDK wire tests cover
exact forwarding with original bearer identity, legacy omission and conflicting
grant rejection. Server tests cover foreign principal/device rejection, one
winner under concurrent claims, target closure/deletion and parent closure. The
disposable Redis process was shut down and confirmed terminal.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: per-session preflight forwarding in
native and remaining SDK integrations, followed by the outstanding original
acceptance requirements. No push, PR, remote CI, deployment or custody operation
occurred. Goal remains active.


## Native preflight/session association checkpoint (2026-09-08)

WebRTC SDK `b1cd16d` passes each session's original preflight JSON to the native
create callback. The POSIX transport extracts the PKI TURN username and sends
`ice_username`, with legacy omission and rejection of conflicting/oversized
grants before HTTP creation. No preflight state is stored in the shared transport.
The callback ABI changed; SONAME is 2. Native consumers and custom transports must
rebuild and update the signature. Non-POSIX transports must implement forwarding.
The preflight buffer is borrowed only for the callback duration.

Validation: baseline native build succeeded; updated shared libdatachannel/POSIX/
Ameba host build and core build succeeded. All 22 full native CTests and all 10
core CTests passed. The expanded POSIX HTTP contract test additionally passed
after rebuilding its executable: exact PKI forwarding, duplicate matching grants,
legacy omission, conflicting/oversized rejection and two outstanding grants on
one shared transport. The native core test verifies the callback receives the
original preflight response. The full suite includes direct and local TURN H264,
authorization lease expiry and repeated connect/close. These are local host
fixtures, not physical-device or live staging qualification.

Reproducible local checks: `cmake --build /private/tmp/rtk-webrtc-pki-peer -j 4`
and `ctest --test-dir /private/tmp/rtk-webrtc-pki-peer --output-on-failure`;
corresponding core build/test directory is `/private/tmp/rtk-webrtc-pki-core`.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: cloud-client typed signaling methods
in Go/JavaScript/iOS/Android/native still need grant-reference plumbing and host
integration review. Their source entry points were located during this checkpoint;
no completion is claimed for them. No push, PR, remote CI, deployment or custody
operation occurred. Goal remains active.


## Cloud-client preflight reference checkpoint (2026-09-08)

Cloud-client `c250789` adds optional grant references to Go, JavaScript, Swift,
Kotlin and native C/C++ WebRTC create requests. Nonempty values are transmitted
unchanged as `ice_username`; absent/empty values retain legacy omission. Native
uses a struct_size-gated trailing extension and accepts the previous layout,
without reading the extension from older requests. Kotlin consumers must rebuild.
The common PKI create fixture exercises the same wire representation across
JavaScript, Swift, Kotlin and native; Go tests exercise forwarding and omission.

Validation: full Go race suite; JavaScript 42-test suite plus rebuilt 35-test
package suite after field-order normalization; all 85 Swift host tests; Android
Gradle unit tests; native build and all 12 CTests. The native error-path fixture
uses the old struct_size with nonzero trailing storage and verifies that the grant
is omitted. These are local host checks, not physical-device qualification.

These cloud-client methods perform signaling only. Hosts still obtain preflight
and pass the reference for the original device/token; they do not gain automatic
peer lifetime ownership from the optional field. WebRTC viewer forwarding was
implemented separately in the prior Go/native checkpoints. Omission remains
supported by the server; this is not proof that every deployed caller binds grants.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next implementation priority returns to
App certificate revocation/publication and other issuance-domain gaps in the
original audit, together with host adoption. Current App verification checks
revoked_at and signed CRLs, but tests still inject receipt revocation directly;
automatic revocation/controller publication is not complete. No push, PR, remote
CI, deployment or custody operation occurred. Goal remains active.


## App leaf revocation checkpoint (2026-09-08)

Video Cloud `583c943` adds durable App revocation receipts and authenticated
`revoke-app` / `finalize-app-revocation` issuer endpoints. The initial transaction
marks the issued receipt revoked and records original operator/reason/time;
same-reason retries preserve that record. Existing App identity/token checks and
issuance replay/completion deny afterward. Publication remains pending until a
current signed full issuer CRL contains the leaf serial and every configured
consumer acknowledges its exact digest. Empty consumer policy is rejected;
finalization rechecks freshness/policy and avoids duplicate audits. Controller-only
SQL grants cover the new table; migration and explicit grant refresh are required.

Review also found that issuer retirement omitted App leaves. Retirement now blocks
pending App signing outcomes, live unrevoked leaves and unpublished revocations.
It cannot discard the signer before those descendants settle.

Validation: full server Go suite; full PKI race suite against disposable PostgreSQL
16; restricted controller/issuer/verifier role integration; signed HTTP assertion,
service identity and environment tests; retirement before/after publication.
App token verification/refresh tests now invoke RevokeApp instead of directly
mutating revoked_at, and pass. The task PostgreSQL container was stopped and
removed through its --rm lifecycle. No live environment or provider was mutated.

This is durable denial and verified publication receipt handling, not automatic
OpenBao revocation. Provider revoke/CRL retrieval, bounded retries/recovery and
worker scheduling remain the next implementation step. Disconnected-client/fleet
cutoff and real consumer acknowledgments remain qualification requirements.
Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. No push, PR, remote CI, deployment or
custody operation occurred. Goal remains active.


## App provider publication checkpoint (2026-09-08)

Video Cloud `7c62649` implements a bounded OpenBao App revocation adapter and
`publish-app-revocation` controller endpoint. Only an existing revoked receipt can
request publication. The persisted certificate/issuer determines the serial and
exact provider mount. The adapter revokes, rotates the full CRL and retrieves PEM;
the controller verifies signature, freshness, target serial and monotonic history
before importing it. Provider outcomes do not manufacture consumer acknowledgments.
A valid current imported CRL is reused so retries preserve the digest while
consumers acknowledge it. Failure leaves the original denial/pending receipt intact.

App controller ACLs now include exact-mount revoke update and CRL rotate/PEM read;
the App signer has no revocation rights. Re-render/application is an explicit
operational step. Other domains are unchanged. The adapter has a 15-second total
context, redirect denial, bounded responses and explicit mutation confirmations.

Validation: full server Go suite; full PostgreSQL PKI race suite; OpenBao adapter
race suite (malformed/missing confirmations, redirects, oversized responses and
timeouts); real local OpenBao 2.5.5 plus PostgreSQL integration using restricted
controller/signer tokens. The real fixture verifies revocation/rotation/import,
repeated provider revocation after a possible uncertain outcome, stable imported
retry digest, and mandatory consumer acknowledgment before finalization. HTTP
publication assertion/identity/environment tests pass. Both disposable containers
were stopped and removed. An initial timeout-test fixture teardown bug was fixed;
all final suites passed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: recurring pending-publication retries,
initial/ongoing App CRL freshness and health/scheduling integration. Provider pruning
that removes historical entries is still rejected by monotonic import and requires
retention compatibility qualification. Other trust domains, backup/recovery host
wiring and live/hardware/custody acceptance remain open. No push, PR, remote CI,
deployment or custody operation occurred. Goal remains active.


## Recurring App CRL worker checkpoint (2026-09-08)

Video Cloud `173cd4f` adds an opt-in worker to the existing PKI controller.
`PKI_APP_CRL_WORKER_ENABLED` defaults false and accepts only explicit booleans.
The worker pages active/retiring App issuers and pending receipts, retries
publication/finalization, creates initial full App intermediate CRLs, and refreshes
within one hour of expiry. It waits ten seconds after a complete scan; SQL and
provider operations are bounded and cancellation propagates through shutdown.
Page size is 32; total scan time depends on inventory and provider response time.

Internal operations use fixed workload audit identity `pki-app-crl-worker` without
fabricating an MFA Principal. Interactive API wrappers retain MFA enforcement.
The worker cannot create revocations or consumer acknowledgments. Offline root
CRLs are verified/reported, not generated. Current root and intermediate CRL
digests must be acknowledged by every configured consumer for health to succeed.
With the worker enabled, the existing mTLS listener exposes GET /healthz with
aggregate counts/last complete scan; startup, errors, missing acknowledgments,
shutdown and completion older than five minutes are unhealthy. Large/failing
inventories remain unhealthy rather than receiving a hard-cutoff claim.

Validation: full server Go suite; full PKI/controller/provider race suites against
local PostgreSQL and OpenBao 2.5.5; real CRL refresh and a complete worker scan;
initial publication, provider failure/recovery, stable ack-wait digest, automatic
finalization, workload audit identity, freshness refresh preserving historical
entries, loop failure/recovery/shutdown and stale health. A 33-issuer fixture
crosses the page boundary and excludes another environment. Both task containers
were stopped/removed. Optional deployment settings/documentation were added;
no workload was enabled or deployed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: audit real consumer fetch/apply/ack
wiring and App backup/recovery evidence; then the remaining unsupported trust
domains and original acceptance requirements. Root custody, live fleet cutoff,
provider retention/pruning and physical-platform qualification remain open.
No push, PR, remote CI, deployment or custody operation occurred. Goal remains active.


## App-only API CRL consumer checkpoint (2026-09-08)

Video Cloud `9c47d1b` removes a Device-only prerequisite from the existing API
CRL consumer. App-only PKI now uses provisioned App CA trust and the existing
management URL/CA/cert/key settings without requiring Device Root ID/state or
Product PKI. Product PKI still requires dynamic Device trust; legacy Device mTLS
without Device trust is rejected. The App-only path does not start a Device root
policy worker. Static App root policy changes still require reconfiguration/restart.

The existing consumer fetches signed CRLs over independent management mTLS,
checks monotonic history, persists/activates exact records and then acknowledges
them. Initial synchronization precedes listening; current CRLs are enforced at
handshake and on requests over existing TLS connections. Registry-backed App
certificate/token checks remain additional enforcement, not replaced by the cache.

Validation: full server Go suite and full API/trust/config race suites passed.
A three-level App root/intermediate/P-256 leaf fixture verifies activation before
both acknowledgments, live App mTLS success, existing-request/new-handshake denial
after signed revocation, and rollback denial. Configuration tests prove App-only
acceptance without weakening Product or legacy Device trust requirements. Existing
Product CRL consumer tests still pass. Deployment example and config documentation
were updated; no runtime settings were enabled or deployed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: broker/TURN and other service consumer
acknowledgment wiring, App backup/recovery evidence and remaining trust domains.
Dynamic App root-policy replacement and live host/fleet/custody qualification
remain open. No push, PR, remote CI, deployment or custody operation occurred.
Goal remains active.


## Broker/TURN App CRL consumer checkpoint (2026-09-08)

Video Cloud `3d70c01` wires optional reviewed App CRL manifests and independent
management mTLS into the broker and TURN sweep workloads. Preparation fetches,
validates, persists and installs signed monotonic records before each sweep.
App token checks require the prepared root/intermediate digests in the same
repeatable-read database snapshot as identity verification. Missing authorities,
registry advancement and registry rollback deny affected tokens. Preparation
failure still permits cleanup of unverifiable sessions and prevents acknowledgment.

After successful sweep completion, acknowledgment reloads the prepared disk
record and revalidates its current registry digest without fetching new evidence.
Changed records require another preparation/sweep. The consumer retains monotonic
disk state across restart and an in-process floor; installation inherits bounded
operation cancellation. Workloads without the optional configuration retain
registry checks without acknowledgment. Deployment examples and controller
configuration documentation describe scope and required identities/manifests.

Validation: full Go suite; API/trust/broker/TURN race suites; full PKI and trust
race suites against disposable PostgreSQL; focused vet. Real mTLS/PostgreSQL
coverage proves preparation without acknowledgment, changed-registry rejection,
recovery, persisted rollback protection and restart acknowledgment. App leaf tests
prove prepared root/intermediate completeness and denial after registry advance
or simulated snapshot rollback. Consumer tests cover changed disk evidence,
failed runtime revalidation, expiry and installer cancellation. The PostgreSQL
fixture was stopped/removed. No workload was enabled or deployed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. This checkpoint supplies sweep-consumer
evidence; it does not attest broker/coturn TLS-store installation or real cluster
eviction timing. Next: App backup/recovery reconciliation and remaining trust-domain
adapters; dynamic App root policy and live host/fleet/custody qualification remain.
No push, PR, remote CI, deployment or custody operation occurred. Goal remains active.


## App issuer and certificate recovery checkpoint (2026-09-08)

Video Cloud `a05c6a8` adds the read-only `pkicontroller recovery-check-app`
command. It compares the registry's independent App intermediate/root lineage
with the key-bound issuer selected by OpenBao's App role and an independently
supplied Root fingerprint. Optional App subject/public leaf inputs also require
the exact successful issuance receipt and fresh signed intermediate/root CRLs;
revoked, expired, mismatched and unregistered identities cannot pass. Issuer-only
and existing-certificate evidence have distinct report statuses.

The bounded check uses a read-only repeatable-read registry snapshot, checks
indexed/document identity, CSR/key and certificate metadata, and performs only
provider metadata GETs. The existing exact-mount App recovery ACL suffices.
Device recovery retains its three-authority profile through shared validation;
App recovery uses its independent two-authority chain. Public leaf file bounds
and regular-file checks cover both commands. No new configuration is enabled.

Validation: full server Go suite; full PKI/OpenBao/controller race suites with
local PostgreSQL; recovery negative cases; focused vet; real OpenBao 2.5.5 App
recovery ACL/role-selected chain retrieval. The restricted identity is denied
signing, revocation and key-generation writes. Existing App provisioning/signing/
revocation integration still passes. Final focused recovery race tests passed
after simplifying the shared validator. Both task fixtures were stopped/removed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. This proves public lineage and selected
existing identity checks, not private-key usability after restore or complete
backup/revocation inventory. Writers must remain fenced during comparison.
Next: matched App backup/restore inventory and reconciliation of uncertain signing
and revocation outcomes, followed by remaining trust-domain/runtime adapters.
No push, PR, remote CI, deployment or custody operation occurred. Goal remains active.


## App lost-serial recovery checkpoint (2026-09-08)

Video Cloud `937acc7` closes the known-serial prerequisite for an unresolved
App signing outcome. The existing authenticated reconciliation endpoint accepts
an omitted serial and derives discovery inputs only from the original persisted
claim. OpenBao discovery scans exact-mount public certificate inventory in pages
of 64, requires a complete unique match to the original CSR key, and re-reads that
unrevoked leaf before the existing transactional completion checks. It never
signs again or releases the original signing claim after failure.

Duplicate-key outcomes, revoked matching certificates, absent outcomes, invalid
or repeated pages, and cancellation leave recovery unresolved. Unrelated revoked
certificates do not block a valid match. The operation is bounded to 30 seconds;
large or pruned inventories require independently obtained evidence rather than
a partial-success claim. The exact App controller policy adds certs:list; signer
and issuer-lineage-only recovery identities retain their narrower permissions.
Provider writers and tidying must be fenced during reconstruction because listing
is not a provider snapshot. API/configuration documentation describes these limits.

Validation: full server Go suite; full PKI/OpenBao/controller race suites with
PostgreSQL; focused vet; a 65-certificate paginated HTTP fixture including
ambiguity, revoked matches, unrelated revocations, incomplete/repeated pages and
cancellation. Real OpenBao 2.5.5 recovery with more than 64 stored certificates
returns the original signed leaf through the restricted controller identity;
the signer cannot list the inventory. Original-owner completion remains idempotent,
failed discovery preserves the pending claim, and recovery uses the persisted CSR.
Task PostgreSQL/OpenBao fixtures were stopped/removed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Full database/provider capture already
includes App tables and keys, but matched restore inventory, post-backup security
reconciliation and real custody/RPO/RTO qualification remain. A recovered outcome
does not prove a complete inventory or authorize resuming issuance. Next: connect
App inventory and security-state checks to matched recovery acceptance, then
remaining trust-domain/runtime adapters. No push, PR, remote CI, deployment or
custody operation occurred. Goal remains active.


## App registry recovery inventory checkpoint (2026-09-08)

Video Cloud `7e0aaf2` adds `recovery-inventory-app` for an explicitly pinned App
intermediate/root and required consumer set. The bounded read-only check scans
all issuer receipts in one repeatable-read database snapshot with 128-row pages.
It detects unresolved claims and invalid original request/certificate/CSR records,
checks revocation receipt consistency, verifies historical publication digests
against signed CRLs containing the leaf, and requires current signed CRL coverage
and root/intermediate acknowledgments. Revoked intermediates are rejected.
Incomplete or blocked inventories fail; success is registry consistency evidence.
The verifier role gains only SELECT on revocation and CRL acknowledgment records.

Validation: full server Go suite; full PKI/controller/PostgreSQL race suites with
a disposable PostgreSQL fixture; focused vet; final inventory race tests after
avoiding repeated full historical-CRL copies. A 129-receipt test crosses the page
boundary. Missing acknowledgments, unpublished or false publication receipts,
request digest mismatch, revoked intermediates and missing revocation records
are denied; complete published revocations pass. Restricted-role tests verify
recovery evidence reads without revocation write access. The fixture was removed.

Workspace backup documentation now composes the App issuer/leaf and inventory
commands through environment-owned recovery_checks, retaining independent root
pins, consumers and provider/registry credentials. No live configuration changed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. The registry inventory is not proof of
complete external history or restored-key usability. Independently retained
post-backup security history, full issuer inventory, real matched restore/custody
and RPO/RTO evidence remain. Next: review remaining original domain/runtime
implementation gaps against the fixed acceptance list. No push, PR, remote CI,
deployment or custody operation occurred. Goal remains active.


## Approved private server issuer policy checkpoint (2026-09-08)

Video Cloud `893ff9a` implements canonical server_dns_names in approved issuer
requests and public issuer records. Only service/mqtt/openbao_tls intermediates
can carry the policy. Exact lowercase sorted unique DNS names are required;
wildcards, IPs, URI syntax, invalid labels and client-domain reuse are rejected.
The policy participates in the request digest. Controller/store provisioning,
CSR persistence, import and activation recheck the original approved request,
preventing policy removal or changes from bypassing approval.

OpenBao provisioning now supports these explicit private server intermediates,
generates private keys in the provider and configures exact-name P-256 server-only
roles after importing the independently signed chain. CSR names are ignored as
policy, and wildcard/subdomain/IP/URI alternatives are disabled. ACL rendering
separates server signing from controller and recovery metadata access. Device/App
profiles remain separate; server reservations without policy remain unsupported.
Public HTTPS continues to require public CA/ACME.

Validation: full Go suite; full PKI/OpenBao/controller/PostgreSQL race suites with
local PostgreSQL; focused vet; real OpenBao 2.5.5 server CA provisioning/import,
restricted server signing, injected CSR-name exclusion and rejection of unapproved
CN/SAN, wildcard, subdomain and alternate-role issuance. Existing App provider/
revocation/recovery integration still passes. Registry tests prove policy persistence,
idempotency conflict on changed names and rejection after approved-policy drift
or removal. Both local fixtures were stopped/removed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Domain-inventory steps 1–2 are implemented;
next are durable server leaf claims and validated replay/recovery, gateway handler
and bootstrap integration, then revocation and consuming-host adoption. The legacy
gateway handler still uses its old signer; this checkpoint does not claim complete
server runtime migration. No push, PR, remote CI, deployment or custody operation
occurred. Goal remains active.


## App recovery end-to-end permission correction (2026-09-08)

Video Cloud `330e2a5` corrects two defects found while preparing server claim
integration. The verifier previously lacked SELECT on CSR/TTL/request-digest
columns used by the full App inventory. A real HTTP App issuance also included
ContextDigest in its request hash without persisting that field, so inventory
could not reconstruct the original request. Earlier owner-role inventory tests
and limited verifier SELECT tests did not cover this combined path.

The explicit schema migration now adds context_digest; issuance persists it,
reconciliation reconstructs and compares the entire request digest before any
provider access, and inventory includes that context. Verifier grants permit the
required public/request metadata reads while withholding claim tokens and writes.
Existing receipts whose original context was not persisted remain blocked when
their digest cannot be reconstructed; no original digest is rewritten or claim
released to bypass recovery evidence. Schema migration and grant refresh are
required before using the updated code; no live migration was performed.

Validation: full server Go suite, full PKI/PostgreSQL/controller race suites with
local PostgreSQL and focused vet. The restricted-role integration now performs
the complete inventory over real HTTP issuance, known-serial recovery and a
published revocation, rather than checking only selected table access. It passes
while claim-token reads and CSR writes remain denied. Nonempty-context recovery
passes; changed persisted context is denied before provider discovery. Inventory
also rejects context tampering. The task PostgreSQL fixture was removed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. This was a prerequisite correction, not a
new milestone. Next remains durable private server signing claims, gateway runtime
integration, and domain-specific recovery/revocation and host adoption. No push,
PR, remote CI, deployment or custody operation occurred. Goal remains active.


## Durable private server signing claims checkpoint (2026-09-08)

Video Cloud `f304ec1` adds pki_server_issuances with environment/domain/caller/
request identity, original CSR and DNS set, context digest, pinned issuer,
single-owner claim token and persisted result. Only the first committed claim
receives signing ownership; pending retry never releases it. Completion and replay
check the original request, approved server DNS policy, exact P-256 server leaf
profile/key/SANs, bounded original-claim lifetime, current signed root/intermediate
CRLs and registry lifecycle status. Claim tokens are not serialized.

Scope and issuer/root row locks coordinate signing completion with lifecycle
changes. Pending server work and unexpired leaves block CA retirement; locally
revoked unexpired leaves conservatively remain blockers until server publication/
consumer evidence is implemented. Exact database grants permit issuer claim/result
writes and public CRL reads while withholding request/policy/context/revocation
updates. Verifiers cannot read server claim tokens. Schema/grant migration is
explicit; no runtime mode is enabled by this change.

Validation: full Go suite; full PKI/PostgreSQL/controller/certissuer race suites
with local PostgreSQL; focused vet. Concurrent requests yield one owner; timeout
retry, changed context, wrong owner, incorrect SAN/EKU/lifetime, changed policy,
wrong domain and disabled roots are denied. CRL revocation blocks replay and
completion. Pending/live retirement checks pass. A restricted-role integration
creates/completes/replays a real signed server certificate and denies immutable
metadata/revocation writes and verifier token reads. App regression coverage also
passes after sharing only the online lineage-lock helper. The local fixture was
stopped/removed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: provider/HTTP gateway composition
with the approved server issuer and durable claims, followed by uncertain-outcome
recovery, revocation publication and host adoption. The legacy gateway handler is
not yet switched; this checkpoint supplies registry primitives and does not claim
end-to-end server issuance migration. No push, PR, remote CI, deployment or custody
operation occurred. Goal remains active.


## 2026-09-08 — Private server provider and gateway integration

Video Cloud `6f5838e` adds `ServerIssuers` and the opt-in
`CERT_ISSUER_SERVER_PKI_DOMAIN` (`service`, `mqtt`, `openbao_tls`, default empty).
The gateway HTTP route uses direct authenticated caller identity and configured
DNS allowlists, then pins the approved registry issuer before contacting OpenBao.
The provider receives a fixed server role and explicit DNS SANs; completion checks
its certificate against registry lineage, original CSR/lifetime and current CRLs.
Pending provider failures or invalid certificates cannot release the claim or
fall back to the legacy signer. Replays preserve certificate/time and recheck CRLs.
Bootstrap requires an explicitly migrated registry and suppresses automatic schema
migration in this mode; config/environment documentation records the opt-in.

Validation: full Go suite; PKI/Postgres/certissuer/bootstrap/config/OpenBao race
suites with disposable PostgreSQL; focused vet; actual OpenBao 2.5.5 restricted
server-role test with explicit multi-name SANs. HTTP/PostgreSQL coverage proves
identical replay, changed-purpose conflict, uncertain outcome held pending,
incorrect EKU rejected, wrong-domain denial and revoked-leaf replay denial without
another signing call. Existing Device/App tests passed. Local fixtures removed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next server work is durable
uncertain-outcome reconciliation and revocation publication/consumer adoption.
Service client identity, dynamic App root policy, real host/SDK adoption and live
qualification remain unfinished. This is an implementation checkpoint, not a live
rollout or completion of any broad gate. No push, PR, remote CI or deployment.


## 2026-09-08 — Server uncertain-outcome recovery

Video Cloud `a5ad490` adds authenticated `reconcile-server` for a pinned private
server issuer. Fresh pki_admin MFA and the existing Account Manager mTLS/assertion
binding apply. The controller reconstructs exact DNS/CSR/TTL/context from the
receipt and verifies its digest before provider reads. Known serial recovery uses
one public certificate read; omitted serial discovers one unique stored CSR-key
match under the exact service/mqtt/openbao_tls mount. Bounded paginated discovery
rejects ambiguous, revoked, missing-status, malformed and incomplete inventories.
The controller ACL adds only exact-mount certificate listing; it cannot sign.
Recovery cannot release a pending claim or overwrite a different committed leaf.
Final validation includes elapsed provider-read time and current issuer/CRL state.

The final timing test exposed PostgreSQL microsecond precision differing from the
initial nanosecond response. Follow-up `72df2ef` returns the timestamp persisted by
SQL; a deterministic sub-microsecond regression test proves exact replay. The
checkpoint includes both commits; the initial recovery commit alone was not the
validated endpoint. All final checks passed: full Go suite; PKI/Postgres/OpenBao/
controller race suites, rerun PKI/Postgres/certissuer race suites after the fix;
focused vet; real local OpenBao 2.5.5 restricted controller recovery and signer
listing denial. SQL integration recovers with the restricted controller role.
Local discovery tests cover all three server domains plus App regressions, HTTP
tests cover assertion/peer/environment/body binding and concurrent completion.
Disposable local fixtures were removed; no external deployment or custody action.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next: server revocation
receipts/publication/consumer acknowledgment and recovery verification, followed
by actual host adoption. Read-only discovery requires writers/tidying fenced and
does not establish complete external history, live RPO/RTO or restored-key use.
Goal remains active. No push, PR or remote CI.


## 2026-09-08 — Private server revocation publication protocol

Video Cloud `742a768` adds durable server revocation work plus authenticated
`revoke-server`, `publish-server-revocation`, and `finalize-server-revocation`
controller routes. The original receipt is denied before provider work, including
replay and recovery. Idempotent revocation returns the persisted timestamp;
conflicting reasons are rejected. The publisher uses only the pinned independent
server mount, validates the full signed CRL and target serial, and reuses fresh
current evidence while consumers acknowledge it. The provider cannot create ACKs.
Finalization checks signed current CRL metadata and every required consumer's exact
digest; prior finalization does not bypass a new CRL or changed consumer policy.

Explicit schema/grant refresh is required for `pki_server_revocations`; controllers
can manage receipts and verifiers can read them, while issuer/verifier mutation
is denied. Regenerate exact-mount OpenBao controller policies for revoke/rotate/read
permissions. Signing tokens remain unable to revoke/list. No runtime mode enabled.
Server retirement conservatively remains blocked by unexpired leaves until actual
TLS consumer enforcement and session handling are integrated; publication receipts
alone do not prove a host consumed the evidence or terminated existing connections.

Validation passed: full Go suite; PKI/Postgres/OpenBao/controller/certissuer race
suites with local PostgreSQL; focused vet; real OpenBao 2.5.5 revoke/rotate/read,
restricted token boundaries and revoked-result recovery denial. Tests exercise
failure-before-publication, retry/digest stability, missing serial in CRL, new and
missing consumer ACKs, CRL advancement, corrupted restored metadata, HTTP assertion
binding and restricted database roles. The later-CRL fixture preserves the original
revocation timestamp, and metadata-corruption simulation disables the immutable
trigger only inside its disposable SQL schema. Local fixtures removed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next: server TLS verification,
CRL refresh/consumer integration and recovery verification, followed by real host
adoption. No push, PR, remote CI, live deployment or custody operation. Goal active.


## 2026-09-08 — Private server registry verification and TLS admission

Video Cloud `093bc79` adds `Store.VerifyServer` with independently configured
private domain, exact DNS name and root fingerprint. A read-only repeatable-read
snapshot checks the successful/unrevoked receipt, original CSR/DNS/TTL/context
digest, approved issuer operation, exact active/retiring lineage, signed current
root/intermediate CRLs and metadata, leaf profile/lifetime and hostname. Receipt
revocation denies admission before publication. The existing verifier role has the
necessary read access; no new schema/grants are introduced.

`Store.ServerTLSConfig` clones an explicit-root/name client TLS configuration,
preserves normal Go chain/hostname verification and prior callbacks, and adds a
bounded registry check to every handshake including resumption. Independently
reviewed root pins are not discovered from peer/database state. Invalid chains or
registry failures deny admission. The helper does not yet wire workload transports,
acknowledge CRLs or terminate existing connections; owners must implement those
steps before claiming runtime adoption.

Validation: full Go suite, PKI/Postgres/certissuer race suites with disposable
PostgreSQL, focused vet and actual TLS handshake/resumption tests. New connections
fail after receipt revocation. Wrong environment/domain/hostname/root pin, incomplete
lineage, policy drift, request-context tampering and stale CRLs are rejected.
Restricted verifier-role integration admits then denies the same server after
revocation. Final focused verification passed after guarding malformed input
certificate metadata. Disposable fixture removed. No push, PR, remote CI, live
rollout or custody action.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next: connect concrete
workload transports to server admission, revalidate/evict existing connections,
and integrate CRL/policy refresh plus acknowledgment before real host adoption.
Server recovery verification and live qualification also remain. Goal active.


## 2026-09-08 — Established private server connection enforcement

Video Cloud `243386e` adds `Store.ServerConnections`, an owner for independently
pinned outbound TLS connections. Dial admission uses normal TLS plus registry
verification. Normal close removes tracking; owner shutdown stops new admission
and closes tracked sockets. `Sweep` revalidates established connections and closes
those denied by registry/CRL policy or whose validation is unavailable/canceled.
Socket closure precedes TLS cleanup to avoid a blocked close-notify delaying
eviction. Sweeps use a 20-second context bound; no locks are held across database
or network work. Active streams are terminated, not only idle pooled sockets.

Validation: full Go suite; PKI/Postgres/certissuer race suites with disposable
PostgreSQL; focused vet. Live TLS stream tests prove healthy streams survive a
sweep, while receipt revocation, database failure and canceled validation terminate
the connection and notify the server. Ordinary close cleans up tracking and owner
shutdown denies new dials. Local fixture removed. No push/PR/remote CI/deployment.

This is a tested connection lifecycle primitive, not completed process adoption.
It does not replace existing transports, start a timer or manufacture exact-CRL
acknowledgments. Concrete HTTP/MQTT/OpenBao host wiring, sweep scheduling, health/
shutdown ownership and digest-bound policy installation remain next. Five acceptance
milestones remain: legacy migration/device replacement; trust consumers/live
sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Goal remains active.


## 2026-09-08 — API and log-ingester MQTT server trust integration

Video Cloud `a17c4ff` wires the connection owner into all API MQTT subscriber and
publisher shards and the dedicated log ingester subscriber. Opt-in settings are
`VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256` plus `VIDEO_CLOUD_MQTT_SERVER_PKI_NAME`;
both default empty. The domain is fixed to `mqtt`, normal TLS requires explicit
roots, and startup checks existing registry table/read access. Configured mode
suppresses automatic schema initialization, including partial configuration, and
cannot fall back to legacy dialing if initialization is absent. Existing verifier
read grants are required; no new schema is introduced.

The runtime schedules bounded connection sweeps (default 10s, allowed 1s–1m), logs
validation failures and closes denied/unverifiable connections. Reconnect loops
retain handshake admission and cannot reconnect to a still-revoked certificate.
Cancellation closes the connection owner and prevents replacement connections.
Legacy behavior remains when both opt-in fields are empty. Config/deploy docs
record the mode and its independent root pin/name requirements.

Validation passed: full Go suite; MQTT/PKI/API/log-ingester/config race suites with
local PostgreSQL; focused vet. A real TLS MQTT protocol fixture connects the API's
three subscribers and one publisher plus the dedicated log subscriber, publishes
a message, revokes the registered broker certificate, observes timer-driven closure
of all five original connections and denies subscriber/publisher reconnects. The
test waits for publisher CONNACK readiness and tracks original connection closures
so handshake attempts cannot substitute for eviction evidence. Config tests cover
partial/invalid trust settings and interval bounds. Disposable fixture removed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. This is actual local workload
transport composition, but not a deployed broker cutover. Next: CRL refresh and
exact-digest consumer evidence, remaining Service/OpenBao transports, recovery
verification and real host adoption. No root-file/key rotation or broker deployment,
no fabricated acknowledgment, no push/PR/remote CI. Goal remains active.


## 2026-09-08 — Domain-scoped server CRL maintenance worker

Video Cloud `7869d5e` adds an opt-in server CRL worker selected by
`PKI_SERVER_CRL_DOMAIN` (empty disables; service/mqtt/openbao_tls selects exactly
one domain). It keyset-scans active/retiring intermediates and durable pending
revocations in bounded pages, publishes existing authorized work, attempts exact-
consumer finalization and refreshes intermediate CRLs before expiry. Fresh evidence
is reused while awaiting ACKs. Offline root CRLs are checked but never generated.
The worker cannot create a revocation, sign replacement identities or grant MFA.

Existing controller worker health combines enabled App and server scan reports;
missing evidence/ACKs, scan failure, stale results or shutdown remain unready. The
same PKI_REQUIRED_CONSUMERS applies to enabled workers in a process; differing
policies require separate deployment. SQL/provider operations are bounded and
cancellable. Signed CRL digest/number/timestamp metadata is checked before treating
current evidence as valid. No schema or live mode change is introduced; existing
server registry grants and exact OpenBao controller ACLs are prerequisites.

Validation passed: full Go suite; PKI/OpenBao/controller/Postgres race suites;
focused vet; real OpenBao 2.5.5 rotate/read and scoped worker scans before/after
explicit fixture acknowledgments. Tests cover publication retries, stable digest
while waiting, finalization, scheduled refresh preserving prior revocations,
33-issuer pagination, domain/environment isolation and offline-root absence without
provider mutation. Final targeted tests passed after shared signed-metadata checking.
Local fixtures removed. No push/PR/remote CI/deployment/custody operation.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next: bind MQTT consumer
adoption and connection sweeps to exact installed CRL digests before automated ACKs;
remaining Service/OpenBao transports, root-policy adoption, recovery verification
and real rollout are still required. Goal remains active.


## 2026-09-08 — MQTT exact-CRL consumer acknowledgments

Video Cloud `57c68f6` adds a reviewed private-server CRL consumer and wires it into
API/log-ingester MQTT startup and periodic sweeps. Optional manifest/controller/
management-mTLS settings are all empty by default and require registry MQTT server
trust. The consumer persists/reloads signed records with monotonic rollback floors,
installs all manifest members, sweeps connections and only then acknowledges the
same prepared digests. Failed preparation still triggers eviction; failed sweeps
withhold ACKs until a later successful sweep. No newer record is fetched after the
sweep. Changed current evidence or acknowledgment failure clears readiness.

`VerifyServerWithCRLs` checks exact installed root/intermediate digests in the same
snapshot as server admission. The connection owner accepts an additional check
that can only restrict normal TLS/registry verification; it applies at handshake
and sweep. Missing/stale bounds deny admission. Distinct per-process management
identities and persistent paths are required; the controller authenticates the
consumer CN. Existing verifier SQL grants suffice; no new schema is introduced.

Validation passed: full Go suite; MQTT/pkitrust/PKI/API/log-ingester/Postgres race
suites; focused vet. Actual controller CRL/ACK endpoints and separate consumer
identities establish five TLS MQTT connections, publish, import the revoking CRL,
verify original connection eviction before accepting new-digest ACKs, and record
both exact consumer receipts. Tests also reject missing/stale bounds, registry
advance after prepare, rollback and rollback after restart. An additional-denial
connection test proves the installed-evidence check evicts an otherwise valid
stream. Final checks passed. Disposable PostgreSQL fixture removed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next: remaining Service/
OpenBao transport integration, server recovery verification and root-policy/key
renewal adoption, followed by real rollout/qualification. MQTT acknowledgment
protocol is implemented locally; no live fleet evidence is claimed. No push, PR,
remote CI, live deployment or custody operation. Goal remains active.


### Private server recovery verification checkpoint (2026-09-08)

Implemented `recovery-check-server DOMAIN ISSUER_ID EXPECTED_ROOT_SHA256
[DNS_NAME LEAF_PEM]` for independent Service, MQTT and OpenBao TLS domains.
The check validates restored public issuer indexes/CSR/certificate lineage,
independent root pin and approved DNS-policy digest, and reads the provider's
fixed server-role selection and public key binding. Optional leaf verification
uses the same read-only repeatable-read snapshot for the original receipt,
identity/profile and signed CRLs. Provider access is GET-only and rejects other
domains. Writers must remain fenced across database/provider verification.

Local PostgreSQL tests cover all three domains, pin/provider/policy/index/domain
mismatches, revoked receipts/leaves, expired certificates and stale CRLs. HTTP
provider tests cover role selection, key binding, malformed/oversized responses,
redirects and mount isolation; CLI tests cover explicit domains and unsafe files.
This is public recovery evidence, not private-key usability, complete issuance
inventory, external-history reconciliation, or live RPO/RTO qualification.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining server work
includes complete recovery inventory, Service/OpenBao transport integration,
root-policy/key renewal adoption, and live rollout qualification. No push, PR,
remote CI, live deployment or custody operation. Goal remains active.

Service commit: `d8a0e50`. Full Go suite with PostgreSQL, focused PKI/OpenBao/
controller race tests, `go vet`, formatting and diff checks passed. An initially
malformed policy-tampering fixture was corrected to use a valid-format incorrect
digest before final validation and commit. Disposable database removed.


### Private server restored-registry inventory checkpoint (2026-09-08)

Implemented `recovery-inventory-server DOMAIN ISSUER_ID EXPECTED_ROOT_SHA256
CONSUMER_IDS_CSV`. Explicit Service/MQTT/OpenBao TLS scope, independent root pin,
approved DNS-policy digest, full public lineage, signed root/intermediate CRLs and
exact current consumer ACKs are checked in one read-only repeatable-read snapshot.
Every receipt attached to the issuer is scanned in 128-row keyset pages; scope
mismatches remain visible. Original request/CSR/DNS/issuance metadata, pending
claims, revocation records, historical publication and current CRL coverage are
checked. Unexpired unrevoked leaves also pass current server admission. Reports
contain public counts; no provider calls, signing, acknowledgments or repair occur.

Tests cover all three domains, 129-row pending inventories and 385-row mixed-domain
inventories with repeated caller/request keys; malformed context/DNS/digest/scope,
missing ACKs, missing/unpublished/mismatched revocations and revoked intermediates.
The restricted verifier SQL role runs the inventory and detects unresolved
revocation. This proves restored-registry consistency only: provider completeness,
post-backup external history, private-key usability and live recovery qualification
remain separate evidence requirements.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next server work is remaining
Service/OpenBao transport integration and root-policy/key renewal adoption, with
external-history reconciliation and live qualification still required. Local only;
no push, PR, remote CI, deployment or custody operation. Goal remains active.

Service commit: `340aed2`. Full Go suite with PostgreSQL, PKI/controller/Postgres
race tests, restricted SQL-role checks, vet, formatting and diff checks passed.
Fixture corrections used a valid-but-wrong server domain and observation times
after reconciliation; final checks passed before commit. Disposable PostgreSQL
fixture removed. No production acceptance gate is claimed closed.


### Controller OpenBao transport integration checkpoint (2026-09-08)

Added an origin-bound registry-backed HTTP/1.1 connection owner and wired it into
controller workload OpenBao construction before authentication. Opt-in requires
an independently pinned OpenBao TLS root, reviewed server DNS name and dedicated
CA file; optional sweep interval defaults to 10s (1s–1m). Startup validates registry
schema access. Normal TLS and registry receipt/DNS/CRL checks gate handshakes;
periodic sweeps evict active/idle connections on revocation or unavailable evidence.
Shutdown stops admission. Alternate origins, plaintext, redirects and environment
proxies cannot carry provider authentication through this configured transport.

Local TLS/PostgreSQL tests cover Service and OpenBao TLS domains, real client
Kubernetes login and definite-403 token renewal against a fixture provider,
verified HTTP keep-alive, wrong pins/origins and active response eviction after
revocation, database loss and cancellation. Controller partial-policy configuration
fails closed. Default configuration remains unchanged. No actual OpenBao host
rollout or production qualification is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Certificate-issuer provider
clients and other Service hosts still require adoption. Exact installed-CRL ACKs,
root-policy and server key renewal, external recovery history and live qualification
remain. Recovery commands retain independent recovery trust. No push, PR, remote
CI, live deployment or custody operation. Goal remains active.

Service commit: `43eb2ee`. Full Go suite with PostgreSQL, PKI/controller/OpenBao
race tests, final focused authentication/eviction race checks, vet, formatting
and diff checks passed. Disposable database removed. Host inventory updated to
distinguish completed controller wiring from remaining client/renewal adoption.


### Certificate-issuer OpenBao transport adoption checkpoint (2026-09-08)

Certificate-issuer configuration now loads and validates the independent OpenBao
transport pin, DNS name and bounded sweep interval. Enabled mode creates one
application-owned verified HTTP transport before signer setup and passes it to
Product/App/server registry clients plus both legacy OpenBao signer adapters.
Partial policy fails before database setup. Startup does not auto-migrate in this
mode; shutdown and bootstrap failure close the transport before its registry DB.
Defaults preserve existing transport behavior; secret/environment preparation
retains independent bootstrap trust.

Tests cover environment loading and invalid pins/DNS/origins/intervals, application
bootstrap in legacy and combined registry modes, unmigrated-schema refusal without
schema mutation, cleanup ordering and post-shutdown denial. Legacy signer tests
prove custom transport rejection reaches both adapters. Existing provider TLS,
authentication, renewal and stream-eviction tests remain part of the full suite.
No deployed host or production qualification evidence is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining work includes
other Service clients, exact installed-CRL ACKs for HTTP consumers, root-policy/key
renewal, external recovery-history reconciliation and actual host qualification.
No push, PR, remote CI, live deployment or custody operation. Goal remains active.

Service commit: `58f8e47`. Full Go suite with PostgreSQL, config/certificate-issuer/
bootstrap/PKI race tests, vet, formatting and diff checks passed. Disposable database
removed. No production acceptance gate is claimed closed.


### HTTP consumer exact-CRL acknowledgment checkpoint (2026-09-08)

Controller and certificate-issuer provider transports now optionally load the
existing registry server CRL consumer using an explicit manifest and separate
management mTLS identity. Configuration rejects partial policy. Startup prepares,
sweeps and acknowledges before returning the provider transport; the management
endpoint must already be reachable independently of the starting controller.
Each timer cycle prepares installed CRLs, sweeps connections with exact digest
bounds, then acknowledges only the prepared evidence. Failed preparation still
sweeps; failed sweeps withhold ACKs. ACK failure clears consumer readiness and
immediately sweeps again so pooled HTTP connections cannot bypass that denial.
Consumer management resources close with the transport lifecycle.

HTTP tests use actual registry CRLs/ACK rows with a test adapter and assert stream
eviction before new-digest ACK, plus preparation/ACK-failure eviction and startup
rejection. Existing production consumer tests separately cover management mTLS,
persistence, rollback rejection and exact prepared-digest acknowledgment. This is
composed local coverage; no live HTTP fleet/latency qualification is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining work includes
other Service host adoption, root-policy/key renewal, external recovery history,
legacy cohorts, hardware/platform evidence and live qualification. No push, PR,
remote CI, live deployment or custody operation. Goal remains active.

Service commit: `fc93e2f`. Full Go suite with PostgreSQL, PKI/consumer/config/host
race tests, final focused startup/order checks, vet, formatting and diff checks
passed. Disposable PostgreSQL fixture removed. No production acceptance gate closed.


### API Account Manager Service transport checkpoint (2026-09-08)

Added opt-in Service-domain trust to API app-token authorization requests to
Account Manager. Explicit root pin/DNS/CA configuration is validated before runtime
setup; enabled mode requires the API registry and disables schema auto-initialization.
The API owns the shared verified HTTP connection lifecycle and closes it before
the database. The authorizer receives that client, preserves its configured timeout
and bearer-token contract, and propagates transport denial without fallback.
Optional exact-CRL management settings reuse installed-digest preparation/sweep/ACK.
Domain selection remains fixed to `service`, independent of peer input.

Local tests cover config/env mapping, early partial-policy rejection, unmigrated
registry refusal without mutation, rejection of a CA-file-trusted but unregistered
server before HTTP, authorization transport denial, and timeout preservation.
Registered-Service TLS/eviction/ACK behavior is covered by the shared transport
suite. These are composed local checks, not live Account Manager deployment proof.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Cross-service workers and
other Service hosts still require adoption; Service clientAuth lifecycle, root/key
renewal, external recovery history, legacy cohorts and live/hardware qualification
remain. No push, PR, remote CI, live deployment or custody operation. Goal active.

Service commit: `d612c1c`. Full Go suite with PostgreSQL, config/consumer/HTTP/API/
PKI race tests, vet, formatting and diff checks passed. Disposable database removed.
No production acceptance gate is claimed closed.


### Factory certificate-issuer Service transport checkpoint (2026-09-08)

An authoritative workspace search found no production constructor calls for the
three cross-service workers; each already accepts an HTTP client. Work moved to
the active factory-enrollment certificate-issuer client instead of inventing a
worker executable. Opt-in factory Service trust now defers issuer construction
until application-owned registry setup. Existing client certificate/key material
is attached to the verified Service transport; a dedicated Service CA/pin controls
server admission independently of legacy CA settings. Request timeout and refusal
of issuance/cancellation redirects remain. Enabled mode rejects proxies, externally
supplied issuer clients and missing registry/environment, and does not auto-migrate.
Shutdown/bootstrap failure/listener failure close the owned transport and registry.

Local tests cover mTLS through injected HTTP transport, timeout/redirect behavior,
environment decoding, unsafe bootstrap rejection, schema non-mutation, transport
ownership and listener-failure cleanup. Bootstrap tests use parseable identity
fixtures without asserting peer admission; registered-Service admission and exact
CRL behavior have shared local transport coverage. No live deployment is claimed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next active host gap is
factory Account Manager admission/recovery transport; Service clientAuth lifecycle,
other hosts, root/key renewal, external recovery history and live/hardware evidence
remain. No push, PR, remote CI, deployment or custody action. Goal remains active.

Service commit: `184f422`. Full Go suite with PostgreSQL, factory/client/bootstrap/
consumer/PKI race tests, vet, formatting and diff checks passed. Disposable database
removed. No production acceptance gate is claimed closed.

### Factory Account Manager Service transport checkpoint (2026-09-08)

Factory enrollment now loads optional Account Manager Service trust under
`FACTORY_ENROLL_ACCOUNT_MANAGER_` and injects the application-owned verified
HTTP client into its existing admission adapter. Reservation, lookup, cancellation
and result publication, including recovery coordination, share this transport.
Dedicated bearer credentials, redirect refusal and uncertain-outcome semantics
are preserved. Partial trust configuration, missing registry/environment/token
and unmigrated PKI schemas fail startup. Either factory Service trust mode disables
automatic schema creation. Owned connections close before the registry database.
Optional exact CRL consumers use the existing prepare/sweep/ACK lifecycle.

Service commit: `ea00d9e`. Full Go suite, targeted factory/PKI/consumer race suite,
vet, formatting and diff checks passed. The disposable PostgreSQL fixture was
removed. Bootstrap tests cover both issuer and admission ownership and schema
non-mutation. Admission requests to an unregistered TLS peer remain unavailable
for all four operations and never become non-issuance evidence. Positive registry
TLS admission, eviction and exact CRL acknowledgement rely on shared transport
coverage; this checkpoint does not claim live Account Manager qualification.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Factory admission transport
is no longer an implementation gap. Service clientAuth issuance/verification and
lifecycle, remaining host adoption, root/key renewal, external recovery history,
and real hardware/staging/custody evidence remain. Next implementation focus is
the Service clientAuth lifecycle, grounded in the original domain design.
No push, PR, remote CI, deployment or custody action. Goal remains active.

### Approved Service client identity policy checkpoint (2026-09-08)

Service intermediate requests now persist an exact `service_client_ids` policy
bound to independent approval alongside optional server DNS policy. Canonical
`service:<name>` identities are sorted/unique; other domains and issuer kinds
reject the field. Empty existing policy grants no new rights. Policy tampering,
removal and request reuse are rejected. Unsupported provider adapters cannot
consume provisioning claims or import provider material for the new profile.

A separate OpenBao `service-client` role uses P-256, digitalSignature and
clientAuth only, a 90-day maximum TTL, exact common names, no alternative SANs
and stored certificates. Its constrained signer ACL is emitted separately from
server signing on dual-profile Service issuers. Controller/recovery permissions
remain separate from signing. Local provider evidence checks certificate profile,
unapproved names/extra SAN rejection and reciprocal server/client signer denial.
This implements policy governance and provider provisioning, not completed
Service client leaf lifecycle or production host qualification.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next is durable Service
client issuance with strict CSR/provider validation and current receipt-based
verification, followed by revocation/recovery, renewal and listener adoption.
The original goal remains active. No push, PR, remote CI, deployment or real
custody action is authorized or performed by this checkpoint.

Service commit: `51709bb`. Full Go suite with local PostgreSQL/OpenBao,
PKI/provider/controller race tests and vet passed. After tightening the client
role maximum TTL to the design's 90-day target, affected policy/provider tests
were rerun under race detection and passed. Disposable fixtures were removed.
No production acceptance gate is claimed closed.

### Durable Service client issuance and verification checkpoint (2026-09-08)

Implemented independent `pki_service_client_issuances` receipts with a single
signing owner, immutable environment/caller/request/CSR/context binding, approved
exact Service identity policy and a 1–90 day requested lifetime. Pending or
uncertain outcomes cannot obtain a second signing claim. Completion/replay checks
current issuer lineage, original CSR/key/profile and fresh signed root/intermediate
CRLs. Both client-only and dual-profile Service issuers are supported; Device,
App and server receipt paths remain separate. Stored PostgreSQL issuance precision
is returned on completion/replay. Pending and unexpired client leaves, even locally
revoked leaves, now conservatively block Service issuer retirement.

Read-only repeatable-read verification requires an original successful unrevoked
receipt, recomputed request digest, approved Service policy, exact stored lineage,
independently supplied root pin, expected service identity and fresh signed CRLs.
The installed-CRL variant additionally requires both exact current issuer digests.
The verifier alone does not prove possession; callers must require authenticated
TLS. Schema migration and database-role refresh are explicit prerequisites. The
actual restricted issuer/controller/verifier roles preserve receipt/token/mutation
boundaries in local tests.

Local coverage includes concurrency, unknown outcomes, invalid CSR/leaf profiles,
wrong identity/environment/root, unrecorded certificates, policy/context tampering,
CRL advance/expiry/revocation, retirement accounting and real OpenBao sign/complete/
verify/replay. OpenBao non-CA leaves omit optional Basic Constraints; validation
accepts that standard encoding while rejecting CA=true, CA signing usage, non-client
EKUs and every SAN extension. A root-disable fixture was corrected to update both
indexed status and the canonical issuer document, as lifecycle writes do.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next Service client work is
authenticated issuance integration, operational uncertain-outcome reconciliation
and revocation, TLS listener/consumer adoption and renewal/recovery qualification.
No push, PR, remote CI, deployment or real custody action. Goal remains active.

Service commit: `992e15d`. Full Go suite with PostgreSQL/OpenBao, targeted
PKI/PostgreSQL/controller race suite, vet, formatting and diff checks passed.
Disposable database/provider fixtures were removed. No production acceptance
gate is claimed closed.

### Service client reconciliation and revocation checkpoint (2026-09-08)

Added issuer-scoped controller operations for Service client reconciliation,
revocation, provider publication and finalization. They use the existing Account
Manager mTLS/request-bound assertion boundary and require fresh MFA `pki_admin`.
Recovery takes only caller/request/optional serial, revalidates durable request
context and recovers a unique provider certificate without re-signing. Missing
serial recovery uses bounded complete inventory; writers must be fenced. Owner
completion and recovery share the same immutable completion transaction.

Revocation commits receipt denial and a durable Service client revocation row
atomically. Provider failure cannot restore access. Publication is scoped to the
Service issuer mount and imports only a signed CRL covering the recorded serial;
fresh covering evidence is reused. Finalization requires all configured consumers
on the exact current digest and rechecks policy/CRL changes. No acknowledgment is
manufactured by recovery or publication. Unexpired leaves still conservatively
block issuer retirement until consumer/host acceptance exists.

Tests cover request assertion binding, authorization, concurrent/uncertain recovery,
missing serials and original-context tampering, publication failures, wrong domains,
current-digest/consumer expansion gates, restricted SQL roles and local OpenBao
lost-serial recovery plus revoke/publish/finalize. Real-provider fixture comparison
allows equivalent surrounding PEM whitespace; acknowledgment time is sampled after
provider publication, so a newly issued CRL is not tested against an older clock.
Local fixture acknowledgments are not live listener-eviction evidence.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining Service client work
includes authenticated issuance integration, periodic CRL publication/consumer work,
TLS listener adoption, renewal and restored-inventory qualification. Explicit schema
migration and refreshed role grants are required for the new revocation table.
No push, PR, remote CI, deployment or custody action. Goal remains active.

The expanded real-provider flow exposed a shared current-CRL query bug: selecting
`number::text` and ordering by its unqualified output name used lexical ordering,
so CRL 9 could outrank CRL 10. Current selection now orders the numeric table column
explicitly. A regression imports 9, 10, 99 and 100 and checks current selection and
rejection of stale-digest acknowledgments. This fixes CRL selection across domains;
the earlier timestamp-fixture adjustment alone did not resolve the failure.

Service commit: `9897894`. The full Go suite with PostgreSQL/OpenBao, the targeted
PKI/PostgreSQL/controller race suite, vet, formatting and diff checks passed.
Disposable PostgreSQL and OpenBao fixtures were removed. No production acceptance
gate is claimed closed.
