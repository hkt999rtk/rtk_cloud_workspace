# Production PKI implementation ledger

This change implements the Platform PKI contract in dependency order. This file
records actual delivery status; unchecked items are not production capabilities.

## Accepted decisions

- Full production lifecycle, including bootstrap governance and recovery.
- Offline Device/App/Service Root and Brand custody; online Product keys in OpenBao.
- Explicit CA provisioning, separate from business-record creation.
- OIDC MFA step-up and distinct governance/custodian approval identities.
- Preserve staging identities through a bounded 90-day migration window.
- Recovery targets: RPO <= 15 minutes; RTO <= 4 hours, demonstrated by drills.

## Delivery checklist (implementation, not production qualification)

- [x] Durable issuer registry, independent approvals, atomic activation and public chains.
- [x] CSR/certificate exchange and existing-key Product provisioning reconciliation.
- [x] Account Manager PKI authorization/API with request-bound RS256 assertions.
- [x] Sealed one-time bootstrap, including existing installations and concurrent starts.
- [x] Last-administrator protection, including concurrent account/role removals.
- [x] Independently approved administrator recovery to a verified active account.
- [x] OIDC assurance and recent-authentication enforcement; refresh grants no new MFA.
- [x] Cloud Admin lifecycle UI, same-account OIDC MFA callback and public artifact export.
- [x] Offline encrypted Root/Brand key and CA signing CLI.
- [x] Dynamic certissuer selection, signed factory context and exact reservation validation.
- [x] Runtime issuer-to-product binding and explicit staging legacy fingerprint checks.
- [x] Read-only legacy inventory, independently approved imports and bounded CRL refresh.
- [ ] Live staged legacy migration and device replacement qualification.
- [x] Offline CA CRL signing, immutable CRL publication, and exact consumer acknowledgment gates.
- [x] Device replacement with bounded overlap, successor acknowledgment and Go file-based installation helpers.
- [x] Certificate-bound device tokens with live API/refresh/MQTT authentication checks.
- [x] Device WebSocket revalidation, MQTT authentication leases and broker cache/session operator tooling.
- [x] Cumulative Root distrust, consumer acknowledgment gates, atomic local root state and opt-in API TLS reload.
- [x] Explicit reconciliation of historical pending Root removals with governance and acknowledgment gates.
- [x] Administrator reconciliation of uncertain renewal results from stored provider certificates.
- [x] Factory outcome reconciliation and atomic Product-mode signing journal/binding completion.
- [x] Go owner lifetime cancellation, bounded WebSocket heartbeat and direct Pion device-peer teardown.
- [x] C firmware MQTT-owner teardown and session-bound MMF frame queues (host/ARM validation).
- [ ] Remaining trust-consumer adapters and live firmware/trust/session qualification.
- [x] OpenBao Kubernetes login and projected-token reauthentication.
- [x] Explicit runtime/PKI schema migration; Product mode workloads skip startup DDL.
- [x] Explicit controller/certissuer/verifier database grants with restricted-role issuance/recovery tests.
- [x] OpenBao three-node TLS Raft deployment profile and exact-issuer workload ACL rendering.
- [ ] Backup/recovery tooling and SDK installation support.
- [x] Focused unit, PostgreSQL and race-detector tests for implemented controls.
- [ ] Live provider end-to-end and hardware compatibility validation.
- [ ] Live staging migration, custody ceremonies and recovery qualification.

## Completion boundaries

Production CA keys must never be generated on this development workstation.
Custodian approvals, offline key backups, IdP assurance configuration, and live
recovery evidence must be real operational inputs, not test fixtures or flags.
Production remains disabled until all required acceptance evidence is present.

## Current code and tests

Work is isolated at `/private/tmp/rtk-production-pki`; the original workspace's
unrelated edits are preserved. Relevant service branches are
`codex/production-pki-hierarchy`. Nothing has been pushed, merged or deployed.

- Video Cloud: `internal/pki`, `internal/pkicontrollerapp`, `cmd/pkicontroller`,
  and opt-in factory/certissuer/API integration. Operator configuration and
  remaining gates: `repos/rtk_video_cloud/docs/production-pki-controller.md`.
- Account Manager: PKI roles migration, exact-role lookup, OIDC MFA step-up,
  request-bound controller proxy. Configuration:
  `repos/rtk_account_manager/docs/pki-controller-integration.md`.
- Go client: strict certificate-bundle parser accepts additive issuer metadata.

Validated against an isolated PostgreSQL 16 container, not any shared environment:

- Video Cloud affected packages: pki, openbao, certissuer, certissuerapp,
  factoryenroll, factoryenrollapp, httpapi, apiapp, config, and controller build.
- Race-detector coverage: registry idempotency, independent approvals, rotation,
  reservation pinning, ancestor compromise and HTTP bypass rejection.
- Account Manager authentication/API regression suites and PKI-role integration.
- Go SDK certificate-bundle authentication package.

The full proposed plan is not complete. Immediate next delivery stages are
real recovery/custody/escrow evidence, remaining
trust domains, replacement and CRL publication, least-privilege deployment,
backup/restore, migration tooling, and live qualification. Production OIDC MFA
assurance and real approver/custodian account identifiers have been requested;
they are operational inputs and must not be invented.

## Continuation delivery

- Bootstrap startup now creates and seals once; it ignores retired bootstrap
  secrets thereafter. Existing or disabled administrators do not reopen bootstrap.
- PKI roles bypass the user cache so assignment revocation is immediately visible.
- Offline `pkiceremony` uses encrypted PKCS#8 files and validates independent
  request/CSR/parent digests, hierarchy, keys, validity and output preservation.
- Cloud Admin `/platform/pki` includes the lifecycle workflow and same-session,
  same-provider, same-user OIDC callback. Full console/backend client suites pass.
- Issuer and operation search has environment-scoped keyset pagination.
- Local tests also cover encrypted Root/Brand ceremonies, bootstrap concurrency,
  immutable sealing, console CSRF/state/subject rejection, and search isolation.

No production keys, live migration, deployment, push or merge has occurred.
The UI has backend and syntax coverage; live-browser and real-IdP qualification
remain outstanding. Full production scope remains unfinished as listed above.

Local checkpoint commits (not pushed): Video Cloud `345a541`, Account Manager
`ac3131b`, Cloud Admin `c0a3a8a`, and Go client `762137f`. The workspace checkpoint
pins these four implementations and leaves unrelated repository pointer updates
unstaged. Production remains disabled; this checkpoint does not complete the
full implementation plan.

## Local continuation after checkpoint d8d9c9c

Added parent-signed CRL validation/publication and offline CRL signing. CRL
rollback, omitted prior revocations, wrong signers, stale publication and old
consumer acknowledgments are rejected. Completion is gated on the revoked
issuer's serial and all required consumer acknowledgments. Root distrust and
actual consumer installation remain separate work.

Added sealed last-administrator protection at the database boundary, including
user disablement/demotion, assignment removal and canonical system-role changes.
Concurrent removals preserve one administrator under read-committed and
repeatable-read isolation. Two-person administrative recovery is implemented in the following stage; live qualification remains pending.

No PRs have been created and nothing has been pushed. Work remains local on
`codex/production-pki-hierarchy` as requested.

## Administrator recovery continuation

Migration 077 and the Account Manager API now implement a one-hour recovery
request with immutable target/reason digest, independent PKI Administrator and
Security Custodian approvals, live role revalidation and atomic administrator
grant. The requester and target cannot approve. Passwords and bootstrap sealing
are preserved. Cloud Admin exposes the workflow and can obtain recovery MFA
authority directly from Account Manager during a PKI controller outage.

Validated with PostgreSQL and the race detector: independent approvals, changed
digests, stale MFA, expiration, disabled targets, revoked approver roles, concurrent
execution and single audit/grant behavior. API tests bind the actor to verified
JWT claims. Console tests cover same-origin/session requirements and controller
outage recovery. API, auth, user-cache, console and account-client regression
suites pass. Real IdP/custodian recovery and database disaster drills remain open.

## Certificate replacement continuation

Certissuer now accepts renewal using an existing verified device mTLS identity,
requires a new P-256 CSR key, derives scope from the authoritative binding and
entitlement, and pins the active Product CA once. A durable single-attempt claim
survives retries and rotation. Unknown signing outcomes remain unresolved rather
than being signed again. Atomic completion records the successor and a fixed
24-hour maximum overlap, capped by old expiry/legacy deadline. Acknowledgment
requires successor mTLS and ends old-certificate acceptance immediately. Request,
completion and acknowledgment are audited. Runtime checks enforce the cutoff.

The Go SDK persists a private software key and CSR before network requests,
verifies independently provisioned Device roots, installs a complete key/cert
version through one atomic pointer switch, and supports restart loading and
successor acknowledgment. POSIX storage is required; automatic scheduling,
secure-element integration and Ameba firmware support remain separate work.

Validation: isolated PostgreSQL and race tests cover concurrent claims, changed
requests, same-key rejection, active issuer selection and pinning across rotation,
fixed overlap, old-identity rejection and successor acknowledgment. Go client
race tests cover persisted-key reuse, failed installation preservation, trust/key
validation, atomic switching, and real local TLS renewal/acknowledgment. Existing
certissuer, certissuerapp, runtime HTTP/API regression suites pass.

Unknown provider outcome reconciliation, live OpenBao/device qualification,
expired-device recovery, existing-token/session revocation and the remaining
production deployment/backup/migration stages are still unfinished. No production
keys, PR, push, remote CI or deployment was performed.

Local replacement milestone commits: Video Cloud `ab966f8`, Go client `c429481`.
These follow the recovery workspace checkpoint `5831387`.

## Device token revocation continuation

Device/camera JWT issuance now records the verified mTLS certificate fingerprint.
The API's token service checks live certificate binding, entitlement, cloud and
issuer-lineage state on issuance, validation and refresh. Refresh preserves its
original certificate provenance. Old-certificate acknowledgment or deadline,
leaf revocation and ancestor compromise deny outstanding associated tokens on
subsequent validation. Missing provenance and registry failures fail closed.
Cloud identity is checked against the authoritative entitlement even when cached
device projections supply issuance metadata. MQTT HTTP authentication uses the
same verifier. App, subscriber and administrator token policies are unchanged.

Tests cover persisted replacement with old/new tokens, revoked leaf/entitlement/
ancestor, staging deadlines, cross-cloud/environment rejection, refresh provenance,
missing verifier and direct-TLS provenance attachment. The MQTT endpoint is tested
before/after revocation. Affected authentication, PKI, HTTP, API, workflow and
certissuer regression/race suites and focused vet checks pass.

Rollout requires existing device clients to reacquire certificate-bound tokens;
old device/camera JWTs lacking provenance are rejected in Product PKI mode.
The environment example remains disabled. Uncached registry reads need load
qualification. Broker authentication caches, already-open MQTT/WebSocket/WebRTC
sessions, presigned URLs and separate offline JWT consumers are outside this
validation boundary. Active-session eviction and live propagation drills remain
next work alongside the other unfinished production milestones.

Local token-revocation service commit: Video Cloud `5039ecd`, following workspace
checkpoint `7754f9a`. No PR, push, deployment or remote CI run was performed.

## Active device session continuation

Product PKI device WebSockets now revalidate their original token immediately
and every ten seconds with a five-second check deadline, closing idle rejected
connections and canceling session work. Closure targets the original connection
so a successor remains connected. Device/camera MQTT HTTP authentication now
returns a maximum 60-second absolute session lease and trusted certificate/token
provenance attributes for broker inspection.

The separate `pkibroker prepare|sweep|watch` operator command verifies disabled
node authentication caching, resets authentication/authorization caches, inventories
sessions before mutations, rechecks rejected identities and disconnects exact
rejected device client IDs. Old device sessions without provenance are removed;
valid successor, app and internal-server identities are preserved. Broker errors
and unsupported/unknown policy are not reported as successful revocation. Watch
retries failures, and lease expiry is the fallback for new admissions. Reserved
device client-ID prefixes and broker-owned provenance attributes are documented.

Added build/release inclusion, optional systemd unit, separate operator environment
example and EMQX policy fragment. No production enablement is introduced. The
broker credential is isolated from the public API; deployment is explicit.

Validation: race tests cover real local WebSocket idle closure, successor survival,
expired/invalid checks and cancellation. Simulated EMQX API tests cover cache policy,
reset failure, missing inventory, multi-page inventory before deletion, exact
eviction, vanished sessions and changed identities. MQTT response tests verify
lease caps and provenance. HTTP/API, WebSocket/coordinator and cloud-handoff
regressions, vet, command build and package-script syntax checks pass.

EMQX API behavior was checked against e5.9.0 source. No actual broker, cluster,
cache reset, shared service or device was changed. Live broker expiry/cache/
reconnect-load qualification, direct peer-to-peer WebRTC media termination,
presigned URL/offline-consumer revocation and broader PKI production milestones
remain unfinished. Closing a control socket is not proof that media stopped.

Local active-session service commit: Video Cloud `5bf3495`, following workspace
checkpoint `fc66594`. No PR, push, deployment or remote CI run was performed.

## Root distrust continuation

Approved Root revocation/compromise now atomically publishes an immutable,
environment/trust-domain-scoped removal record. The controller distributes a
cumulative versioned policy and verifies configured consumer acknowledgments of
its exact digest and canonical loaded-root bundle. Pending revocations require
all configured consumers to acknowledge the latest cumulative policy. A Root
self-CRL cannot complete removal. Same-key reissued certificates are removed too.

Added `pkitrust` atomic on-disk policy/root updates, process advisory locking,
monotonic cumulative-policy checks and explicit empty trust pools. Its runtime
reload helper returns acknowledgment evidence only after the consumer's install
callback succeeds. Policy integrity digests are not signatures; input must come
from the authenticated controller/console. Cloud Admin now downloads the policy.

The API optionally reloads this state for new TLS handshakes, with resumption
disabled and rollback/scope/corruption checks. A post-verification chain check
rejects cross-certificates carrying a removed root key. A separate App root file
cannot reintroduce that key. Existing connections remain covered by the previous
session/token revocation work, not by a TLS pool update alone.

Validation: PostgreSQL/race tests cover cumulative policy changes, stale or missing
consumer evidence, exact bundle digests, immutable history and consumer scope.
Filesystem tests cover competing writers, failed runtime reload, rollback,
corruption and explicit empty pools. Real TLS tests reject the removed root and
its valid cross-certificate while preserving the successor root. PKI, API, HTTP,
configuration, certissuer and console regression suites pass; vet, CLI build,
JavaScript syntax and package/install script syntax checks pass.

Explicit PKI migration is required. Pre-migration pending Root operations are not
automatically backfilled. Other consumer adapters, automatic acknowledgment
transport, live trust-domain rollout and rollback-resistant recovery remain open.
Root removal does not solve direct peer-to-peer media termination; device/client
enforcement and live broker/hardware qualification remain unfinished. No shared
trust store, production key, deployment, push, PR or remote CI run was changed.

Local Root distrust commits: Video Cloud `eee0ebd`, Cloud Admin `fb44dff`, following
workspace checkpoint `7019525`.


## Historical Root removal reconciliation continuation

The existing operation reconciliation endpoint now repairs missing removal records
for pre-migration pending Root revocations/compromises. Recent administrator MFA,
the original request digest, independent recorded administrator/custodian approvals,
matching issuer state and execution audit are required. Publication and audit are
atomic under operation/scope locks; concurrent retries reuse the same record.
The operation stays pending until all required consumers acknowledge the latest
cumulative policy. Earlier acknowledgments become stale after backfill. Policy
reads reject a requested historical Root that has not itself been published.
Cloud Admin labels the existing reconciliation control for both supported uses.

Validation: isolated PostgreSQL and race tests cover both lifecycle actions,
concurrent retries, missing approvals/execution history, changed request digest,
wrong issuer status, stale MFA, wrong role, stale consumer evidence and subsequent
completion with fresh acknowledgments. PKI, local trust-store and API race suites,
controller compilation, focused vet and Cloud Admin application tests pass.

This closes historical Root policy publication only. Unknown leaf-signing outcomes,
remaining consumer adapters, deployment/backup/migration automation and live
qualification are still unfinished. No shared database, provider, trust store or
production key was changed. Work remains local with no PR, push or deployment.

Local historical-removal commits: Video Cloud `17cbe09`, Cloud Admin `9a34bdc`,
following workspace checkpoint `7b7ddc7`.


## Uncertain renewal signing recovery continuation

Added administrator recovery for existing replacement claims through an issuer-
scoped, request-bound controller endpoint and Cloud Admin form. The original
request ID and provider serial locate a public certificate in the pinned OpenBao
mount. Recovery performs no signing or claim reset. Missing/revoked/invalid
provider results preserve the unresolved claim. The original device retrieves
successful recovery by replaying its existing renewal request.

Completion now independently checks the full client-auth chain, requested key and
identity, claim-relative backdating, signed validity interval, parent margin, live
old identity and active entitlement. The binding, result, bounded installation
overlap and operator/fingerprint audit are atomic. Exact retries are idempotent;
a different recovered certificate cannot replace a committed result.

Validated locally with isolated PostgreSQL and race tests for concurrent recovery,
wrong keys/CN/usage/validity, revoked entitlement, stale MFA, wrong role/issuer,
body-bound API assertions, device-caller denial, result replay and unchanged overlap.
Provider HTTP tests cover exact read-only lookup, serial mismatch, missing status,
revocation, redirects, oversized/malformed responses and missing certificates.
PKI, certissuer, API/HTTP regressions and Cloud Admin tests pass. No live OpenBao
or production keys were used.

Factory signing claims use a separate journal and remain the next recovery stage.
Expired-device recovery, automatic provider investigation, remaining trust adapters,
deployment/backup/migration and live qualification remain unfinished. No PR, push,
remote CI or deployment was performed.

Local renewal-recovery commits: Video Cloud `c34f7a3`, Cloud Admin `158d232`,
following workspace checkpoint `165e59d`.


## Uncertain factory signing recovery continuation

Added a recent-MFA administrator endpoint and Cloud Admin form for existing
Product-mode factory signing claims. Recovery checks the original public CSR DER
hash against the durable journal, exact issuer pin/request fingerprint/device,
reservation scope and cancellation state, then reads the existing certificate
from the pinned OpenBao mount. Shared result validation checks the requested key,
identity, full chain, current lineage, validity and parent margin.

Normal Product-mode completion now atomically commits the runtime certificate
binding, signing journal result and audit using the original claim token. Recovery
uses the same transaction. Concurrent completions converge without another
signature; conflicting certificates and revoked/replaced bindings are rejected.
Identical unrevoked bindings stranded by the former two-write flow can be adopted.
The factory service retrieves recovery through its original authorized request
and retains ownership of enrollment evidence/projection/reservation completion.

Validation uses actual runtime/PKI migrations, PostgreSQL repositories, the factory
HTTP handler and a simulated OpenBao HTTP server. Tests cover lost signing replies,
normal issuance, exact replay without re-signing, concurrent normal/recovery
completion, injected journal-write rollback, canceled reservations, compromised
issuers, stranded/revoked bindings, wrong CSR/issuer/token, request-bound admin
assertions and factory-caller denial. Regression/race and console checks are run
locally; provider transport is simulated, not live qualification.

No schema additions, production keys, deployment, push, PR or remote CI were
introduced. Unknown serial investigation, cancellation after uncertain signing,
remaining trust consumers, least-privilege deployment, backup/restore, legacy
migration and live qualification remain open.

Local factory-recovery commits: Video Cloud `4f29909`, Cloud Admin `c9f9a4f`,
following workspace checkpoint `37b3854`. PKI, certissuer, factory enrollment and
PostgreSQL regression/race suites, controller compilation, focused vet, console
application/account-client tests and JavaScript syntax checks passed.


## Database privilege separation continuation

Added explicit `pkicontroller grant-runtime-roles` migration-owner tooling and a
non-secret environment template. Three NOLOGIN groups separate controller writes,
certissuer claim/result access and verifier reads. Existing administrative/owning/
inheriting roles are rejected. Reconciliation removes stale table/column grants,
PUBLIC access on governed tables and PUBLIC schema CREATE; it grants no ownership,
DDL, deletion, truncation or future-table defaults. Runtime identities remain
separate from the migration owner and must have no additional privileged grants.

The actual factory issuance/recovery/replay integration now also runs under these
restricted roles. It verifies denied verifier writes/DDL, issuer governance edits,
claim-token reset and controller reservation finalization. The signing pin read
no longer takes an unnecessary update lock. This is database-boundary delivery;
OpenBao HA/policies, actual workload logins, broader service grants and live
qualification remain unfinished. No shared database or deployment was changed.

Local database-role commit: Video Cloud `4781830`, following workspace checkpoint
`731e5f7`. PostgreSQL restricted-role issuance/recovery, PKI/certissuer regression
and race suites, command compilation, reserved-role validation and vet passed.


## OpenBao HA and workload policy artifact continuation

Added a separate official-chart-based three-node TLS Raft profile with persistent
Raft/audit volumes, node anti-affinity, one-unavailable disruption budget, non-root
containers and restricted API ingress. A required local Kustomize post-renderer
replaces the pinned chart's TLS-skipping readiness probe and removes its unsuitable
test pod. The API address references a pod variable defined before expansion.
No existing file-storage installation or PVC is converted by this profile.

Added registry-backed `render-openbao-policy` output with exact Product mount paths:
controller provisioning/public recovery is separate from leaf-signing authority.
No Root/Brand generation, private-key export, KV, cross-mount wildcard or deletion
permission is granted. The runbook binds reviewed policy lists to exact Kubernetes
service accounts/namespaces and audience-bound projected tokens. Image provenance,
transport certificates, quorum custody and credential provisioning remain real
operator inputs rather than fabricated artifacts.

Validation: rendered official Helm chart 0.28.3 with upstream server baseline
2.5.5, passed structured manifest checks and post-renderer shell syntax, PKI ACL
scope tests, controller compilation and vet. No Helm install, Kubernetes apply,
provider login, initialization or key generation occurred. Real HA, token-rotation,
ACL-denial, audit and restore drills remain in the live qualification checklist.
The next implementation work includes backup/restore and legacy migration tooling,
remaining trust adapters/domains and hardware/client integration.


The HA baseline image is pinned to OCI digest
`sha256:6150c4a6b62067db6141c8da7a6a6b5763f4f47c315343d0c848b40fecdfd452`.
The actual v2.5.5 binary accepted the HCL in verify-only mode with network disabled
and disposable local transport/Kubernetes fixtures; no cluster initialization or
unseal occurred. Its unsupported legacy `disable_mlock` field was removed.
Controller provisioning/inventory/recovery now share signing's bounded projected-
token reauthentication after definite 403 responses. Tests cover exact body
replay, a one-connection transport, and preserved no-retry behavior for uncertain
mutations. PKI, OpenBao, PostgreSQL and certissuer race/regression suites passed.

Local HA/policy service commit: Video Cloud `4156f45`, following database-role
workspace checkpoint `15e4d84`. Full implementation and live qualification remain
unfinished; the active goal is not marked complete.

## Native Raft recovery adapter continuation

Extended the canonical encrypted core recovery engine with `openbao-raft` rather
than introducing an independent backup format. The same maintenance journal,
matched database/runtime archive, remote publication and safety-backup gates now
support native OpenBao snapshots. The selected pod loads a mounted operator token
internally, verifies loopback HTTPS with an explicit CA/server name and streams
snapshot bytes without a pod-local snapshot file. Restore retains the original
seal check and disables transport retries; uncertain outcomes stay in maintenance.

Target validation requires one OpenBao backend, an odd peer inventory of at least
three pods, data workload classification, all configured peers present, explicit
PVC exclusions and existing operator checks. Local validation checks compressed
and decompressed limits, native archive members, metadata, digests and presence
of sealed checksums before restore maintenance begins. OpenBao performs the
actual sealed-checksum validation. The runbook specifies issuance/revocation
fencing, snapshot-only operator ACLs, independent audit retention, escrow and
post-restore issuer/trust/revocation verification.

Validation includes corrupt/truncated snapshots, missing sealed hashes, duplicate
members/digests, bounded decompression, invalid/missing/unlisted peer inventory,
TLS/token command boundaries, capture/apply wiring and no ambiguous-operation
retry. A disposable local TLS Raft instance of OpenBao 2.5.5 successfully streamed
save/restore and reverted a post-snapshot test mutation; the recovery reader also
accepted its native sealed snapshot. This single-node protocol test does not
qualify three-node HA or production disaster recovery. Scheduled backups/PITR,
RPO/RTO measurement, legacy migration, remaining trust domains/consumers and live
qualification are still open. No production/shared environment was modified.

Recovery package race tests (including the native snapshot fixture) and focused
vet passed. The full `go test -race ./rtk-cloud/...` run exceeded the CLI package's
10-minute default timeout while scanning test-spec inventory in
`TestImportCloudValidationRejectsMissingCloudEvent`; it is not a passing full-suite
result. The internal recovery, envroot and runner packages passed in that run.
The targeted CLI recovery tests (`go test ./rtk-cloud -run '^TestRecovery'
-count=1`) also passed. The disposable OpenBao container and test key material
were removed after verification.


## Governed legacy staging migration continuation

Added read-only, paginated `pkicontroller legacy-inventory` based on persisted
issuance records, exact DER fingerprints, current entitlements, the target Product
lineage and existing public legacy CA/CRLs. It rejects registered Root public-key
aliases and reports incomplete evidence; it never signs, mutates or contacts the
provider. The controller and console now request/review/approve/execute immutable
legacy manifests through the existing request-bound MFA proxy. Root-scoped
operations require distinct PKI Administrator and Security Custodian approvals;
the requester cannot approve.

A single immutable staging epoch begins with the first successful request and
ends 2,160 hours later. Each binding is additionally bounded by chain expiry and
reviewed full-CRL freshness, using the existing strict seven-day CRL validator.
Approved refresh can update that shorter evidence cutoff within the same epoch,
without rolling back accepted CRLs, changing the target Root/anchor, overwriting
Product bindings or reopening revoked/replaced credentials. Runtime verification
requires exact identity/fingerprint/cutoff membership in a completed approved
manifest; hand-written legacy bindings no longer authorize devices. Imports
revalidate source records and entitlements and commit atomically, with audited
idempotent replay. Explicit schema migration and dedicated runtime-role updates
include the new provenance tables; production legacy admission remains denied.

Validation: isolated PostgreSQL and race tests cover two-person approval, request
replay/rebinding, stale entitlements and whole-batch rollback, closed/immutable
epochs, CRL refresh/rollback/equivocation, shorter evidence expiry, intermediate
revocation, Root-key aliases, forged manifest membership, concurrency, authenticated
HTTP routes and execution-body rejection. PKI, PostgreSQL and certissuer suites,
controller compilation, focused vet, Cloud Admin app/account-client tests and
JavaScript syntax checks passed. No deployed database, production CA or live
migration was changed. Actual staged consumer trust installation, device renewal,
legacy trust removal and hardware/live qualification remain required.

Local migration service commits: Video Cloud `3f76113` and Cloud Admin
`452d0e2`, following native Raft recovery workspace checkpoint `2622e61`.
All commits remain local; no PR, push or deployment has occurred.


## Go owner-to-media lifetime continuation

Cloud Client WebSocket sessions now retain the caller's lifetime context and
expose `Context()` for authorized media/command work. Remote closure, parent/local
cancellation, message backlog or heartbeat failure ends the lifetime. The reader
fails closed on its bounded queue instead of blocking behind unread commands;
10-second pings with a 5-second reply timeout bound stalled transport detection.
Close cancels dependents immediately and waits for reader/heartbeat cleanup.
Reconnects create a fresh lifetime and cannot revive old peers.

The pure-Go Ameba WebRTC device/simulator peer now keeps that context after answer
creation, closes Pion on cancellation and rejects subsequent media samples. Its
Done signal denotes completed local teardown. The WebRTC Go module previously
collided with the Cloud Client module name; its canonical module/import path is
now `github.com/hkt999rtk/rtk_ameba_webrtc/packages/golang`, with its commands and
examples updated. This is a documented import migration for the next release,
not a modification to published artifacts.

Added `tests/pki-session-lifetime`, which composes both checked-out SDKs under
separate canonical paths. It creates an actual local WebSocket owner and real
ICE/DTLS/SRTP peers, receives H.264, then proves media teardown on remote close,
backlog and parent cancellation. Package tests separately cover heartbeat failure,
healthy control traffic, canceled creation, worker cleanup and idempotent closure.
Both SDK race suites, the combined race test and vet passed. The authority decision
in the combined test is a fixture; existing server PKI tests cover authorization.
Production C/libdatachannel firmware, live IdP/registry/transport qualification,
other trust domains and the remaining recovery/installation gates stay open.

Cloud Client also builds without CGo; the WebRTC SDK builds without CGo for
macOS/Linux amd64/arm64. Local SDK commits: Cloud Client `73f718d`, Ameba WebRTC
`5895ca2`, following workspace migration checkpoint `6d7c031`. No PR, push,
published module release or deployment was performed.


## Ameba C owner-to-media lifetime continuation

The composed firmware service now pins each accepted offer to its MQTT connection
generation. Disconnection, stale broker traffic, missing trusted time and dynamic
credential refresh/expiry deny that generation. Every successful CONNECT/SUBACK
creates a distinct owner. Device polling and media submission close the old local
peer on authorization loss; reconnect alone cannot revive it. Answer creation and
publication also recheck authority/expiry. Local teardown avoids cloud cleanup
through credentials belonging to a replacement owner.

MMF frames carry a C11 atomic session epoch sampled at enqueue. Drain releases
inactive/previous-session frames without sending them, including an old IDR that
remains queued when a fresh authorized offer creates a new peer. Epochs never
repeat within a device lifetime; exhaustion denies new sessions. Production
service wiring supplies the authorization callback automatically; standalone
orchestrator users must configure it for governed operation. Device-task lifecycle
serialization and producer shutdown before device destruction remain required.

Validation: the host orchestrator suite passes with MQTT/peer doubles covering
service wiring, disconnect, unobserved reconnect, answer-time authority loss,
local cleanup without HTTP, fresh-offer requirements and MMF stale-frame release.
AddressSanitizer/UndefinedBehaviorSanitizer tests and the actual SDK 9.6e Cortex-M33
compile check (GCC 10.3/newlib 4.1.0, including MQTT source) pass. This is local
implementation evidence, not live broker revocation/physical camera qualification.
The live firmware, consumer installation, non-Device trust domains, custody/IdP,
HA/PITR/recovery and staging migration qualification gates remain open; production
remains disabled.

Local Ameba commit: `d31cc34`, following workspace checkpoint `1886896`.
No PR, push, remote CI or deployment was performed.


## Authenticated trust consumer synchronization continuation

Video Cloud `internal/pkitrust.Consumer` now composes bounded authenticated policy
fetch, expected environment/domain verification, monotonic disk installation,
runtime reload and exact controller acknowledgment. Management mTLS uses explicit
independent server roots and a workload client identity. Redirects, insecure TLS,
malformed/oversized responses and scope substitution fail before installation.
The callback must install both the root pool and cross-certificate distrust check;
failed reloads produce no acknowledgment. Failed or uncertain acknowledgments
preserve removed trust and are retried safely after restart without restoring
bootstrap roots. A newer controller policy requires another synchronization.

Local real-mTLS tests cover consumer identity, runtime failure, exact evidence,
acknowledgment retry/restart, cancellation, rollback and hostile responses. Race
tests for pkitrust and apiapp and focused vet pass. This is a shared consumer
primitive: automatic API/broker worker composition, scheduling, CRL installation,
other trust domains and live/hardware qualification remain incomplete. No disk-only
command claims runtime installation; production remains disabled.

Local Video Cloud commit `72fd90a`, following workspace checkpoint `525a5e7`.
No PR, push, deployment or remote CI was performed.


## API trust synchronization worker continuation

The API now optionally composes the authenticated trust consumer into startup and
runtime. Explicit independent management CA/client identity, controller origin,
Root ID and dynamic Device state are required together. Initial synchronization
and live loader validation complete before listening. A 30-second refresh worker
repeats fetch/install/ack; failures deny new TLS handshakes until a successful
retry. It preserves cumulative trust removal, disables TLS resumption and uses
the same App-alias/cross-certificate/rollback checks as actual handshakes. Worker
shutdown cancels in-flight requests, joins the goroutine and releases idle HTTP
connections. Existing session termination remains governed by separate PKI checks.

Local tests compose real management mTLS and the API runtime loader with actual
client TLS handshakes: removed trust is rejected, retained trust succeeds, and
acknowledgment follows reload. Failure/retry, startup, partial configuration,
cancellation/join and disk rollback tests pass, alongside API/pkitrust/config race
suites, focused vet and API binary compilation.

The current controller endpoint requires an already revoked/compromised Root and
preinstalled state. Initial trust bootstrap and domain-scoped synchronization
before the first removal are still open; no artificial revocation is authorized.
Broker/other consumers, CRLs, non-Device domains, installation/recovery and live
qualification remain required. Defaults leave production synchronization disabled.

Local Video Cloud commit `1b6963c`, following workspace checkpoint `4615e66`.
No PR, push, remote CI or deployment occurred.


## Initial trust policy continuation

Active/retiring Root IDs now expose their environment/domain's authenticated
cumulative removal policy before the first revocation. A fresh scope publishes a
digested version-zero policy with no removals; configured mTLS consumers can
acknowledge it after runtime installation. Independently provisioned root-only
bundles can be installed using the existing atomic pkitrust workflow, then the API
worker fetches current policy before listening. No artificial revocation or new
private key generation is needed. Removed Root IDs continue to require publication.

Positive versions require removal records and version zero cannot restore trust
after removal. Initial acknowledgments cannot complete later revocations. Policy
reads/acknowledgments now also reject scopes containing historical removed Roots
without publication; governed reconciliation must repair those omissions. Empty
trust pools validate every removal record instead of bypassing digest-field checks.

PostgreSQL-backed PKI and local pkitrust/API race suites and focused vet pass.
Tests cover consumer bootstrap routes, first compromise, stale acknowledgment
rejection, missing historical publication, old persisted reconciliation evidence,
state rollback and real synchronized API TLS rejection/acceptance. The disposable
PostgreSQL fixture was stopped. Initial anchor/custody provisioning, broker/CRL
consumers, non-Device domains, platform installation, recovery and live/hardware
qualification remain required; production is not enabled.

Local Video Cloud commit `756d591`, following workspace checkpoint `53fda6a`.
No PR, push, remote CI or deployment occurred.


## Durable CRL consumer cache continuation

Added signed public CRL cache installation and complete-chain revocation checking
in Video Cloud pkitrust. Installation binds the independently trusted authority
fingerprint and exact signed metadata, validates full/direct CRLs and freshness,
serializes writers and fsyncs atomic replacement. New CRLs cannot roll back their
number/time, equivocate at an existing number or remove/change prior revocations.
Expired state can refresh without discarding signature/monotonicity checks;
corrupt or changed-authority state cannot silently reset. Load fails on missing,
corrupt or expired data.

The chain verifier supplements ordinary X.509 and Root distrust checks. Every
non-root certificate needs current CRL coverage from its exact parent, including
intermediates. Tests use real signed chains and cover leaf/ancestor revocation,
missing coverage, expiry, wrong authority, metadata mismatch, restart, corruption,
rollback/equivocation and concurrent writers retaining the highest version.
pkitrust/API race suites and focused vet pass.

This is a consumer foundation, not a running CRL distribution worker. Authenticated
retrieval, runtime activation/acknowledgment, API/broker wiring, long-lived consumer
revalidation, remaining domains/platform installation, recovery and live/hardware
qualification remain open. Production stays disabled. No production keys or
external services were changed.

Local Video Cloud commit `4e4416c`, following workspace checkpoint `01e25ee`.
No PR, push, remote CI or deployment occurred.


## Authenticated CRL synchronization continuation

Added a scoped CRL consumer that reuses verified management mTLS, canonical issuer
URLs, redirect rejection, bounded responses and request deadlines. It fetches,
validates and persists a signed CRL, reloads it under the cache lock, and acknowledges
only the exact runtime record. Freshness is rechecked after installation. Expected
environment/domain and independently provisioned authority fingerprint are required;
private keys are neither fetched nor regenerated.

Reload failure produces no acknowledgment. Failed/uncertain acknowledgment leaves
the stricter cache intact for restart retry. Real local mTLS tests cover workload
identity, exact runtime digest, reload failure, acknowledgment retry, restart,
expiry during installation, wrong issuer, cancellation and scope mismatch.
pkitrust/API race suites and focused vet pass.

Actual API/broker CRL worker composition and long-lived runtime revalidation remain
next integration work. Other trust domains, platform installation, recovery and
live/hardware qualification remain open; production remains disabled. No PR, push,
remote CI or deployment occurred.

Local Video Cloud commit `d3874fd`, following workspace checkpoint `538c032`.


## API CRL runtime worker continuation

The API now optionally loads a reviewed, bounded issuer/cache manifest and composes
CRL consumers into startup and periodic refresh. It uses independent management
mTLS and requires matching environment, Device/App domain, unique authorities and
absolute cache paths. All configured consumers synchronize before listening;
failed refresh denies new handshakes and requests until successful retry.

Runtime activation precedes acknowledgment. TLS verification and every HTTP
request, including existing keep-alive requests, check current Root distrust and
fresh CRL coverage across the client chain. Runtime monotonicity also rejects a
restored older cache even if paired with an old authenticated response. Worker
shutdown uses cancellation/join and idle connection cleanup. Defaults remain off.

Tests combine local management mTLS, real X.509-verified chains, runtime TLS
callbacks, exact acknowledgment, subsequent-request leaf revocation and restored
cache rollback. API/pkitrust/config race suites, focused vet and API compilation
pass. Upgraded WebSocket lifetime CRL revalidation, broker consumers, other trust
domains/platform installation, recovery and live/hardware qualification remain
open; this is not production qualification.

Local Video Cloud commit `3e580bd`, following workspace checkpoint `b9a3838`.
No PR, push, remote CI or deployment occurred.


## Device WebSocket certificate lifetime continuation

API CRL middleware now pins the original TLS identity into the request/session
context. The existing Product PKI socket watcher checks it before and after JWT
validation every ten seconds. Root distrust, expired/missing CRLs, synchronization
failure or certificate expiry cancels the lifetime and closes idle reads even
while the token remains valid. Watcher cleanup remains joined.

The callback re-verifies X.509 client authentication against current roots and
current time before applying current Root/CRL checks. Old handshakes cannot bypass
anchor removal or certificate expiry, including on existing HTTP connections.
Checks use local runtime state; no per-socket network fetch was added.

Real WebSocket tests prove healthy traffic, idle closure with a valid JWT, cleanup
and trust loss during token validation. Signed historical certificate fixtures
prove current expiry rejection and current-root removal. HTTP API/API/pkitrust
race suites, focused vet and API build pass. Broker consumers, other domains and
transports, platform/recovery integration and live/hardware qualification remain
open. Production stays disabled.

Local Video Cloud commit `b84e468`, following workspace checkpoint `bf52705`.
No PR, push, remote CI or deployment occurred.
