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


## Strict device JWT and broker CRL continuation

Added mandatory signed CRL verification for Product device tokens using a read-only
repeatable-read snapshot of entitlement, fingerprint binding, serial, registered
lineage and current CRLs. Existing token/binding/replacement and ancestor-status
checks remain. Missing/stale/invalid CRLs, leaf or ancestor revocation, wrong-cloud
provenance, hierarchy/signature mismatch and legacy bindings fail strict mode.
Broker attributes never supply certificate serials or chains.

The API exposes an explicit Product-PKI-required CRL setting for its token
provenance callback. The separate broker sweep/watch workload exposes an explicit
strict-CRL flag and uses the same verifier before session eviction; malformed
broker flag values fail startup. Enable both after publishing full coverage to
deny reconnect as well as evict existing sessions. Defaults remain off. This
operator verification does not fabricate broker TLS trust-install acknowledgments.

Also corrected command startup validation that still demanded a legacy Device
CA file despite dynamic trust mode; valid dynamic Product-PKI/direct-mTLS/server-TLS
configuration now passes, while mixed trust modes fail.

PostgreSQL tests compose signed CRLs, JWT validation and the EMQX HTTP adapter:
revoked sessions disconnect and healthy sessions remain, with missing/expired CRL
and Brand-revocation cases. PKI/broker/cloudhandoff/API/config race suites, focused
vet and both binaries' builds pass. The disposable PostgreSQL fixture was stopped.
Live broker scale, remaining TLS consumers/domains, installation, recovery and
hardware qualification remain open; production stays disabled.

Local Video Cloud commit `059cce0`, following workspace checkpoint `27126bb`.
No PR, push, remote CI or deployment occurred.


## Trust consumer cancellation continuation

Root and CRL synchronization now share a 30-second context deadline across local
consumer serialization, advisory-lock acquisition and management HTTP work.
Nonblocking lock attempts honor cancellation without releasing another owner or
mutating/acknowledging uninstalled state. This closes the shutdown gap where a
worker could wait indefinitely behind another process's cache lock. Disk-only
operator helpers keep their background-context behavior; runtime callbacks still
must return promptly and OS file I/O is not forcibly interrupted.

Tests hold real root/CRL advisory locks, prove deadline return and unchanged state,
then release/reuse the locks. Mutex ownership/pre-cancellation cases and existing
pkitrust/API race suites and focused vet pass. Full remaining scope still includes
other TLS consumers/domains, platform installation, scheduled recovery/PITR and
live provider/hardware/custody qualification. Production remains disabled.

Local Video Cloud commit `62bd8db`, following workspace checkpoint `e72126b`.
No PR, push, remote CI or deployment occurred.


## Online PostgreSQL WAL segment archive continuation

Added a separate `wal-archive` command and reviewed configuration for online
PostgreSQL 16 segment upload. It does not enter maintenance or pause writers.
Segment headers are bound to the configured cluster system identifier, timeline,
size and address. The adapter streams an identity envelope and WAL bytes into age,
checks source stability, and atomically fsyncs ciphertext plus receipt into a
private spool. Retry reuses the exact ciphertext; changed same-name content or
configuration fails instead of overwriting. Remote immutable publication requires
full ciphertext readback and a completion marker in a dedicated WAL namespace.
Existing core-backup encryption/remote primitives and dedicated credentials are
reused, but the WAL envelope is not a core-backup archive.

Validation includes decrypt/byte equality, retry identity, cluster/layout mismatch,
ambiguous remote retry and corrupt readback, plus a completed segment captured from
a disposable PostgreSQL 16 instance. Recovery race tests, focused CLI tests, vet
and CLI build pass. The container and copied WAL fixture were removed. No remote
storage, scheduler or database deployment settings were changed.

This is the segment upload path, not complete PITR. Timeline/history files,
physical base backups, restore_command, scheduling/retention, matched OpenBao and
registry recovery, and measured RPO/RTO remain required. See
`docs/postgresql-wal-archive.md` for the exact limits and remaining work. Other
trust domains/consumers, installation and live custody/hardware qualification stay
open; production remains disabled. No PR, push or remote CI occurred.


## Verified WAL segment restore continuation

Added `wal-restore` using completed remote objects and the original archive
configuration. It verifies ciphertext checksum, age authentication, envelope scope,
plaintext size/hash and PostgreSQL 16 header before atomically publishing a 0600
segment without replacing any destination. Protected identity files, bounded
metadata/read sizes, shared cancellation deadline, fsync and normal-error cleanup
are enforced. Existing targets (even identical ones) fail closed. Restore requires
the original configuration/spool path; configuration migration is not implemented.

Recovery race tests cover completed-object round trip and malformed/tampered
payloads, missing completion, corrupt download, wrong key/configuration, cancelled
restore, symlinks and existing files. Focused CLI tests, recovery vet and CLI build
pass. No live storage or database settings were changed. This is not yet complete
PITR: timeline/history support, physical base backups, restore_command integration,
scheduling/retention, matched registry/OpenBao recovery and measured RPO/RTO remain.
Other trust domains/consumers and live custody/hardware qualification remain open;
production stays disabled. No PR, push or remote CI occurred.


## Timeline history and promoted WAL continuation

The existing archive/restore commands now transport bounded PostgreSQL timeline
history files through the same encrypted immutable completion protocol. History
parsing validates ancestor ordering and switch-point syntax/order. Promoted
segments may retain an ancestor page header: archive validates the sibling history
against the page interval/fork segment and authenticates those bytes inside the
segment envelope. Restore verifies embedded ancestry before publication. Existing
segment envelopes remain readable. Retry rejects changed history as well as WAL
content. Source hashing now replays the exact bytes validated as the header/history,
closing a source-change window between validation and the first hash.

A disposable PostgreSQL 16 primary/standby pair produced a real promotion history
and completed timeline-2 segment whose first page has timeline 1. Encryption and
restoration reproduced both byte-for-byte. Full recovery race tests (including that
fixture), focused CLI tests, vet and CLI build pass. Tests cover invalid ancestry,
malformed history, missing/symlinked history and immutable retry conflicts. See
`docs/postgresql-wal-archive.md` for source-provenance and size limits.

This does not prove replay eligibility or full PITR. Physical base backups,
backup-history handling, PostgreSQL restore integration, scheduling/retention,
matched OpenBao/registry recovery and measured RPO/RTO remain. Other trust domains,
consumers, platform installation and live custody/hardware qualification remain
open. Production stays disabled; no PR, push, remote CI or deployment occurred.


## Verified physical PostgreSQL backup continuation

Added `base-backup create|restore` with reviewed libpq service/tool configuration,
explicit environment/stack/ID binding, bounded PostgreSQL 16 tar capture with included
WAL, and native manifest/WAL verification. It reuses scoped encryption and immutable
remote completion primitives in a separate `postgres-physical` / `base-v1` namespace.
Private durable ciphertext retries never recapture newer state under the same ID.
Restore authenticates scope/content before extracting into a newly reserved private
recovery directory, reruns native verification and compares evidence. Existing
destinations, unsafe tar members and external tablespaces fail closed. No database
is started automatically. Exact configuration and native tool paths are required.

A disposable network-isolated PostgreSQL 16 integration captures a real row/role,
round-trips encrypted storage through the local object adapter, starts an independent
cluster from restored data and proves captured state plus completed consistency
recovery. It rejects missing required WAL and cluster/layout mismatches. Full
recovery race tests, focused CLI tests, vet and CLI build pass. Native capture's
empty tablespace map is supported; nonempty mappings are refused. No live database,
object store or deployment configuration is changed. See
`docs/postgresql-physical-backup.md` for exact limits and cleanup requirements.

Standalone consistency is not yet point-in-time recovery. Later archived WAL replay,
backup-history handling, continuous scheduling/retention, matched OpenBao/registry
recovery points and measured RPO/RTO remain. Other trust consumers/domains, SDK
installation and live custody/hardware qualification remain open. Production remains
disabled; no PR, push or remote CI occurred.


## Explicit-target PostgreSQL replay continuation

Added `base-backup restore --pitr-plan FILE` for a reviewed LSN and numeric timeline.
It binds the actual WAL restore binary/config/identity to the physical backup,
refuses targets before consistency, preserves source configuration outside PGDATA,
and writes isolated local-socket settings without inherited ALTER SYSTEM/preload/
network/archive behavior. Recovery pauses at the target; it never auto-promotes.
Metadata says prepared-not-replayed and the recovery signal is written last.
Native control warnings now fail closed. Shell/config/percent escaping is covered.

The disposable PostgreSQL 16 test invokes the actual Linux WAL restore CLI against
a TLS object fixture inside a network-isolated container. It fetches completed age
ciphertext, reaches the explicit target, retains the pre-target write, excludes the
later write, stays read-only and exposes no TCP listener. A second fresh restore
with required ciphertext missing must fail and shut down before reaching the target.
Recovery race tests, focused CLI tests, vet and host/Linux builds pass. Docker
Desktop needs a container-local private Unix socket for this test. No live provider,
production database or deployment was touched. See `docs/postgresql-pitr.md`.

This proves a local single-timeline replay path, not full recovery qualification.
Cross-timeline drills, backup-history handling, scheduling/retention, matched
OpenBao/registry recovery and measured operational RPO/RTO remain. Remaining trust
consumers/domains, SDK installation and live custody/provider/hardware acceptance
are still open. Production remains disabled. No PR, push or remote CI occurred.


## Native archive-command and auxiliary WAL files continuation

Added bounded validation and encrypted immutable preservation of PostgreSQL 16
backup-history files plus full-size `.partial` segments under distinct object IDs.
Backup metadata is checked against its start/stop/checkpoint locations, timelines,
segment names and filename offset. Opaque multiline labels cannot override the
trailing identity fields. Partial files never replace complete WAL segments.

`wal-archive-config` renders a new reviewed PostgreSQL fragment with archive mode,
scoped command and 30–300 second native switch interval; it neither applies nor
restarts a server. Explicit scope, no-overwrite publication and shell/config/percent
escaping are enforced. Operational archive age/backlog monitoring is still required.

A disposable native archiver uses the actual Linux CLI and TLS conditional-create/
readback fixture. A physical backup's real history archives and decrypts correctly,
then subsequent WAL archives with zero reported failures. The combined recovery
race suite includes explicit-target replay and missing-WAL failure. Focused CLI
checks, vet and builds pass. Docker tests now resolve the cached image ID because
its tag descriptor intermittently failed lookup; tests never pull automatically.
No deployed PostgreSQL settings, external object store or production provider changed.

Cross-timeline end-to-end drills, scheduled base/snapshot capture, retention,
matched OpenBao/registry recovery and measured operational RPO/RTO remain. The five
unfinished top-level ledger items are unchanged. Production stays disabled; no PR,
push or remote CI occurred.

## Durable physical backup scheduling continuation

Added `base-backup scheduled` with a reviewed interval policy, private durable
checkpoint and nonblocking single-host lock. Each due slot journals its immutable
backup ID before capture; failed or interrupted runs retry that same ID, including
across later slots. Existing staged ciphertext is reused by the physical backup
engine and only verified remote completion advances the checkpoint. Completed
slots are skipped. Configuration drift, malformed/private-state violations and
clock regression fail closed. Missing configuration fields cannot inherit defaults
from the current invocation when decoding persisted checkpoints.

Linux systemd service/timer templates and an operator guide are available in
`cloud_deploy/recovery/systemd/` and `docs/postgresql-backup-scheduling.md`.
The timer checks every minute; the policy determines capture cadence. Templates
have not been installed or enabled against a live environment.

Validation includes race-enabled recovery tests covering journal-before-capture,
ambiguous failure retry, stale pending slots, concurrent invocation exclusion,
cancellation after capture, invalid/private/symlink state, config/policy drift,
clock regression and native-tool failure through the public scheduled engine.
Focused CLI argument tests, recovery vet and CLI build passed. Both systemd
templates passed native `systemd-analyze verify` in a disposable Ubuntu 24.04
container (with a placeholder executable to validate unit wiring). This validates
unit syntax, not live service execution or credentials.

This does not complete the backup/recovery milestone: retention, scheduled restore
rehearsals, matched provider/registry recovery and SDK protected installation
remain required. Base-backup cadence does not establish the WAL RPO, and local
tests do not qualify production RPO/RTO or custody.

## Native cross-timeline recovery rehearsal continuation

The opt-in PostgreSQL PITR integration drill now always exercises one real
promotion/fork in its disposable, network-isolated PostgreSQL 16 container.
It promotes the first recovered copy to timeline 2, writes on both sides of a
new recovery target, and publishes PostgreSQL-generated timeline history and
completed promoted WAL through the actual Linux `wal-archive` CLI. A fresh copy
of the original timeline-1 physical backup uses the existing preparation and
`wal-restore` path to recover onto timeline 2.

Assertions require fetched encrypted history/promoted WAL, paused read-only
recovery, no TCP listener, the new branch's pre-target row, and exclusion of both
abandoned-branch writes and post-target writes. Removing the required history
ciphertext from the fixture object store makes another fresh recovery reject the
missing timeline and shut down. The existing missing-WAL rejection and standalone
physical restore checks remain in the same drill.

This is local compatibility evidence across one native timeline fork. It does
not implement scheduled operator rehearsals, matched OpenBao/registry recovery,
retention, provider IAM or live RPO/RTO acceptance. No production runtime promotes
a server automatically; promotion here is limited to the disposable test copy.

Validation: the race-enabled recovery suite passed with physical backup, native
archive-command and PITR integration enabled (69 seconds total, including the
cross-timeline and missing-history cases). Recovery `go vet` and the Linux CLI/test
binary builds passed. Those fixture timings are not production RTO measurements.

## Read-only PITR runtime observation continuation

Added `base-backup observe` to make the running-state portion of a recovery drill
repeatable. It binds private preparation/verification records to the explicit
backup ID, environment, stack and physical configuration, then runs a fixed,
bounded read-only SQL query over an explicit private local Unix socket. Source
service selection, TCP hosts, connection-string database arguments, psql startup
files and password prompts are excluded. The query fixes its search path to
`pg_catalog` and uses a 10-second statement timeout; the operation deadline is
at most 30 seconds or the configured timeout, whichever is shorter.

Success requires the expected PostgreSQL 16 system identifier and restored data
directory, paused recovery, read-only state, the prepared inclusive LSN/timeline
pause target, sufficient replay position, TCP disabled and archiving off. Output
is `paused-target-observed` with timestamp and target/replay metadata. It leaves
preparation records unchanged and does not start/promote servers, release fences,
or qualify PKI consistency. A manually paused server and point-in-time settings
alone are not evidence of correct application data; native logs, expected data
boundaries and matched provider/registry reconciliation remain required.

Validation: the race-enabled recovery suite passed with native physical backup,
archiving and cross-timeline PITR enabled. The actual Linux CLI observed both
paused recovery timelines and rejected the first server after explicit fixture
promotion. Focused race tests cover wrong identity, incomplete replay, target and
isolation mismatches, unsafe connection inputs, private-file validation, malformed
or oversized output and cancellation. Focused CLI tests, recovery vet and the Linux
CLI build passed. All five top-level unfinished milestones remain open.

## Automated isolated PostgreSQL rehearsal continuation

Added `base-backup rehearse` for an explicit backup ID and reviewed PITR plan. It
restores a fresh private copy, directly owns the PostgreSQL process, waits for the
existing paused-target observation, and confirms clean fast shutdown before
emitting `postgres-replay-rehearsed` and writing durable `rehearsal.json`. It runs
as a non-root recovery OS user and retains recovered data/private logs. It never
promotes a server, overwrites an existing destination or releases traffic fences.

The child receives only explicit backup-reader credentials, TLS trust overrides
and minimal PATH/locale. Source PG settings, shell interpretation and daemonized
startup are excluded. Cancellation still invokes bounded shutdown; immediate or
forced shutdown and unconfirmed termination fail the drill. External SIGKILL/host
failure require supervisor/operator cleanup. The report's elapsed command duration
is not a production RTO measurement and excludes provisioning/escrow retrieval.

The native integration drill invokes the actual CLI for successful replay and
missing-WAL failure, independently checks that both servers stopped, and rejects
success evidence after failed replay. Focused subprocess tests cover controlled
shutdown, unexpected exit, cancellation cleanup and inherited environment isolation.
This implements the PostgreSQL restore/start/observe/stop portion, not scheduled
matched PKI rehearsals: provider/registry consistency, leaf validation, controlled
issuance, retention, custody and SDK protected installation remain unfinished.

Validation: race-enabled recovery suite passed with native physical backup,
archive-command, cross-timeline replay and automated rehearsal enabled (73 seconds).
Focused CLI tests, recovery vet and Linux CLI build passed. Five top-level
milestones remain open; these local fixture results do not qualify production.

## Provider/registry recovery lineage check continuation

Added `pkicontroller recovery-check ISSUER_ID EXPECTED_ROOT_SHA256` for one
reviewed Product issuer. The independent Root pin prevents replacing both stores
with a new trust domain and treating their agreement as recovery. A bounded,
read-only repeatable-read database transaction checks indexed/document state,
Product/Brand/Root scope and ancestry, CSR key bindings, certificate metadata,
constraints and signatures. Active/retiring members are permitted; revoked or
inconsistent state fails before provider access.

OpenBao lookup follows the device role's actual issuer reference, reads public
issuer metadata and compares its full chain with the registry. A separate rendered
recovery ACL permits only exact-mount role/issuer metadata reads. The command does
not modify PKI data, sign certificates, export keys or release recovery fences.
Its `issuer-lineage-matched` result is not private-key usability, leaf/CRL or
whole-inventory qualification. Controlled issuance, post-backup reconciliation,
matched snapshot acceptance and live custody checks remain unfinished.

Validation: race-enabled PKI, OpenBao adapter and controller-app tests passed.
A disposable PostgreSQL 16 instance exercised matching state, independent Root-pin
rejection, provider mismatch and indexed/document disagreement. Recovery ACL and
argument tests passed, along with controller build and focused vet. The temporary
database was removed. Live OpenBao restore/key usability remains unqualified.

## Existing device leaf recovery validation continuation

Extended `pkicontroller recovery-check` with optional `DEVICE_ID LEAF_PEM` inputs.
The existing independent Root pin and role-selected provider lineage comparison
now accompany verification of a known existing leaf's signature, P-256 key,
client-auth policy, identity, exact binding issuer/serial/expiry and active device
entitlement. It requires current signed Product/Brand/Root CRLs, unrevoked lineage
and valid replacement overlap. A shared strict CRL verifier runs inside the same
read-only repeatable-read registry transaction rather than opening a second
snapshot. Public certificate inputs are bounded regular files; no device key,
CSR, signing operation or PKI mutation is involved.

Success is scoped as `device-certificate-recovery-checked`. This does not recover
lost post-backup security changes, prove device key possession, test hardware or
perform controlled provider issuance. All five top-level milestones remain open.

Validation: race-enabled PKI/OpenBao/controller tests passed. Disposable PostgreSQL
integration covered valid existing-leaf recovery, missing/stale CRLs, disabled
entitlement, altered serial/expiry, binding revocation, expired replacement overlap,
signed leaf revocation and signed ancestor revocation. The existing strict-token
and broker-eviction regression passed against the shared verifier. Controller build
and focused vet passed; the temporary database was removed.

## Native matched provider/registry restore and signing continuation

Added an opt-in native OpenBao 2.5.5 TLS file-store/PostgreSQL recovery drill.
Fixture Root/Brand keys stay in memory and the Product key is generated internally
by OpenBao. The drill checks a registered leaf and signed CRLs, captures stopped
provider storage and a native registry dump, removes the issuer and binding, then
restores both stores. Restoring only the provider must fail while the registry
binding is absent. The matched registry restore recovers the existing leaf check;
a fresh in-memory CSR must then receive a valid signature from the original
Product issuer. The probe is not registered as a usable device identity.

The test verifies the disposable PostgreSQL container label and cluster identifier
before applying its unique schema dump. It uses original test unseal material
separately from the copied provider state and cleans its temporary provider.
No live provider, production key, production credential or new runtime signing
bypass is introduced. This is local file-store recovery evidence; live Raft/HSM/
quorum custody, post-backup security reconciliation, other-service recovery and
scheduled full PKI rehearsals remain required. Five top-level milestones stay open.

The native drill also uses separate tokens carrying only the generated recovery
and signer policies. Recovery reads succeed and signing with the recovery token
must receive HTTP 403 both before and after restore. Fresh signing succeeds with
the signer token. Privileged fixture credentials are limited to setup/destruction;
they are not used to satisfy the recovery/read/sign checks.

Validation: the native matched restore/signing drill passed with the Go race
detector against OpenBao 2.5.5 and PostgreSQL 16. PKI/OpenBao/controller regression
tests and focused vet passed. The disposable PostgreSQL container was removed;
the drill removed each temporary provider container and filesystem fixture.
These results do not measure production RPO/RTO or qualify live custody.

## iOS protected-key certificate matching

The iOS Keychain certificate installer now checks key possession before deleting
the current certificate. A fresh random challenge is signed using the selected
Keychain key and verified against the incoming certificate public key. Missing
keys, unsupported algorithms and mismatches fail before certificate mutation;
private key material is never exported by this check.

Validation: all 43 Swift package tests passed on macOS, including matching and
unrelated native Security key proofs. This is not physical Secure Enclave/iOS
qualification. Independently pinned issuer/identity validation, atomic versioned
installation and production renewal/ack integration remain required. Existing
Swift concurrency and duplicate-pattern warnings remain outside this change.
Five top-level milestones remain open.

## iOS provisioning retry key preservation

Keychain generation no longer deletes the selected key. Existing P-256 keys are
reused with their observed Secure Enclave attribute; only an explicit missing-key
result permits creation. Unsupported keys and lookup errors fail closed. A shared
process lock serializes store instances, while cross-process provisioning still
requires application coordination. Rotation must select a fresh version label.
The existing hardware fallback policy is unchanged.

Validation: all 45 Swift tests passed on macOS. Native Keychain tests use uniquely
tagged temporary keys to prove retry preservation and rejection of an existing
P-384 key without replacement, then delete their own fixtures. This does not prove
physical iOS Secure Enclave behavior or complete production renewal installation.
Five top-level milestones remain open.

## iOS required hardware policy

The default Keychain identity store now requires a P-256 Secure Enclave key and
checks observed key attributes for both new and existing keys. Software fallback
is available only through explicit development/test configuration. Generation,
CSR signing, certificate key matching and mTLS identity lookup enforce this
policy. Existing software keys are rejected without deletion. Key-generation
token attributes now follow Apple's documented top-level placement, and access
control creation must succeed.

Validation: all 45 Swift tests passed on macOS. Native Keychain tests prove default
policy rejects a software key for generation, CSR and identity lookup; explicit
compatibility mode preserves it. These tests do not prove successful physical
Secure Enclave provisioning. Physical iOS enrollment/restart/signing/mTLS, atomic
certificate installation and renewal integration remain required. Five top-level
milestones remain open.

## iOS independent-root installation validation

The Keychain installer now requires a complete certificate chain and independently
configured device roots. Native Security client-authentication trust evaluation
uses only those anchors, disables network fetching, checks current validity and
requires the evaluated chain to match the supplied order exactly. Validation
precedes key proof and storage mutation. Empty roots fail closed; issuance response
roots are never automatically trusted.

Validation: all 51 Swift tests passed on macOS, including a valid four-level chain,
missing/unrelated anchors, server-only EKU, expiration, incomplete/reordered/extra
certificates, malformed/oversized PEM and installer rejection before key lookup.
Only public synthetic certificates are committed. Strict device identity/profile
checks, revocation, atomic version activation, intermediate persistence and physical
iOS qualification remain required. Five top-level milestones remain open.

## iOS configured device identity checks

The Keychain store now takes an independently configured device identity. CSR
requests must match it; incoming certificate Common Names must match before
installation mutation. Stored identity reads and mTLS lookup also check the
configured name using the native Common Name API rather than display summaries.

Validation: all 53 Swift tests passed on macOS. Tests cover correct/wrong/missing
identity configuration, installation rejection before key lookup and native
Keychain CSR identity enforcement. Duplicate subject fields, SAN restrictions,
revocation, atomic activation, intermediate persistence and production renewal/ack
remain unfinished, as does physical iOS validation. Five top-level milestones
remain open.

## iOS strict leaf profile

Device identity checks now require a v3 leaf with one matching Common Name,
P-256, non-CA basic constraints, digitalSignature-only key usage, clientAuth-only
EKU and no SAN extension. Duplicate extensions are rejected. A bounded DER reader
checks these profile fields in addition to native cryptographic trust validation.

Validation: 54 Swift tests passed on macOS, with public negative fixtures covering
duplicate/missing CN, SAN, CA status, extra usages, missing profile extensions and
P-384. The arm64 iOS simulator package build also passed. Existing unrelated Swift
concurrency warnings remain. Revocation, atomic version activation, intermediate
persistence, production renewal/ack and physical iOS qualification are still open.
Five top-level milestones remain unfinished.

## iOS immutable installation and intermediate persistence

Certificate version labels are now immutable. The public chain is persisted in
a ThisDeviceOnly Keychain record before the leaf is added; identical retries
complete interrupted installation while conflicting replacements fail without
deleting the old certificate. mTLS lookup validates the persisted chain, profile,
leaf and key, and its credential includes leaf/intermediates without the root.

Validation: 55 Swift tests passed on macOS. A native Keychain/OpenSSL test covers
four-level issuance from an SDK CSR, installation/restart, identical/conflicting
retries, incomplete-write rejection/recovery and client credential chain contents.
The arm64 iOS simulator build passed. The test exposed and fixed a missing CSR PEM
footer newline and macOS certificate-reference insertion losing the version label.
Temporary fixture material was cleaned, including one certificate left by the
earlier failing insertion test, identified by its unique fixture issuer.

Active-version switching, production renewal/ack, revocation and live physical
iOS sessions remain required. The legacy same-label renewal method does not
replace immutable installed versions. Five top-level milestones remain open.

## iOS active identity selection

The Keychain store can select a fully validated certificate version using a single
ThisDeviceOnly selection record. Callers supply the expected previous label to
reject stale work; retries of the selected version are idempotent. Selection reads
revalidate the identity and do not fall back to older certificates. Existing
clients remain pinned to their original label and old versions are retained.

Validation: all 55 Swift tests passed on macOS and the arm64 iOS simulator build
passed. The native test creates two distinct key/certificate versions, switches
and reloads selection, retains the previous identity, and rejects missing versions,
stale expectations and corrupt selection records. This exposed macOS file-Keychain
certificate indexing behavior missed by single-version testing; reference insertion
followed by issuer/serial lookup, exact DER matching and version labeling resolves
it without renaming another version's certificate.

Production renewal/ack and session replacement integration, revocation, and physical
iOS restart/network qualification remain required. Cross-process writers still
need external serialization. Five top-level milestones remain open.

## Portable iOS certificate validity

Keychain installation and expiry reporting now parse certificate validity with
the bounded DER reader on both Apple platforms. iOS no longer lacks expiry
metadata due to the macOS-only certificate-values API. UTC/GeneralizedTime formats,
year boundaries and calendar values are checked explicitly.

Validation: all 58 Swift tests passed on macOS, including installed-certificate
expiry and renewal status, fixed validity dates, leap days, year boundaries and
malformed date rejection. The arm64 iOS simulator build passed. Background renewal
scheduling, production request/ack integration, revocation and physical-device
qualification remain open. Five top-level milestones remain unfinished.

## Durable iOS renewal preparation

Renewal preparation now retains a deterministic protected key version and a
bounded ThisDeviceOnly request journal before returning. Retries preserve the
original CSR, parameters and previous active label; changed parameters or CSR
identity/key/signature failures are rejected. Preparation does not switch the
active identity or perform network requests. CSR generation now includes empty
attributes and omits ECDSA algorithm parameters.

Validation: 60 Swift tests passed on macOS and the arm64 iOS simulator build passed.
Native Keychain tests cover request reload, parameter changes, corrupted CSR and
unchanged active selection; additional tests cover signature/key mismatches and
device/request namespace boundaries. Native OpenSSL issuance still passes.
Network renewal/response installation/ack integration, scheduling, revocation and
physical-device restart qualification remain required. Five top-level milestones
remain open.

## iOS renewal submission and response installation

The SDK submits the persisted CSR and request parameters to the production renewal
path using the previous identity. HTTPS and redirect refusal are enforced by the
built-in path. Responses are checked for request/overlap metadata, trusted chain,
device profile, leaf/chain agreement and pending key possession before immutable
installation. Active selection remains unchanged.

Validation: 61 Swift tests and the arm64 iOS simulator build passed. Native
Keychain/OpenSSL issuance with an injected HTTP transport covers valid submission/
installation and negative response, client, URL and redirect cases. No live
endpoint was called. JSON decoding is bounded; streaming response limits, durable
response/ack recovery, acknowledgement, scheduling, revocation and live device/
network validation remain open.

Progress reporting clarification: these SDK changes advance milestone 3; they
do not complete a top-level milestone. Five broad milestones remain. Reports
should identify the milestone advanced and concrete unfinished substeps rather
than repeating the count as if it measured implementation progress.

## iOS acknowledgement and uncertain-result recovery

The active successor client can acknowledge renewal over a fresh built-in mTLS
transport. A durable local retirement record is written before sending, so an
uncertain server result cannot re-enable the previous identity. New identity/
transport lookups and activation reject retired versions. Acknowledgement state
progresses from attempted to acknowledged without downgrade on later failures.

Validation: all 61 Swift tests and the arm64 iOS simulator build passed. The
native Keychain/injected-transport test covers lost response, state reload, retry,
old-version blocking and monotonic completed state. No live endpoint was called.
Already-created sessions require explicit application closure. Durable response
recovery, scheduling, streaming response bounds, revocation and physical/live
qualification remain required. This advances milestone 3; five broad milestones
remain open.

## iOS durable renewal receipts and explicit transport closure

Validated renewal responses are persisted as immutable, bounded ThisDeviceOnly
Keychain receipts before return or installation. Restart recovery can reload a
receipt without reissuing; conflicts and corrupt records fail closed. Loading
revalidates trust, device identity and key possession. Expired overlap permits
inspection only, while installation retains its overlap gate.

The concrete mTLS transport now supports idempotent closure, cancels its session
and rejects new requests. Applications still own replacement and closure of
retained predecessor sessions; there is no global session registry.

Validation: all 61 Swift tests passed, including expanded native Keychain tests
for receipt recovery, corruption/conflict rejection, overlap gates and closed
transport rejection. The arm64 iOS simulator build passed. No live service or
physical-device qualification was performed.

This advances milestone 3. Its remaining SDK substeps include renewal scheduling,
streaming response limits, revocation freshness and application session ownership
integration; backup/recovery qualification also remains. Five broad milestones
remain: legacy migration/device replacement; remaining trust consumers/live
firmware sessions; backup/recovery and SDK integration; live provider/hardware
compatibility; staging/key custody/recovery qualification.

## Bounded iOS mTLS response streaming

The built-in transport now uses URLSession data-delegate callbacks, enforcing
128 KiB before appending each chunk and rejecting declared oversize at headers.
Unknown or misleading lengths cannot bypass the body cap. Oversize cancels the
task, preserves the original error and never returns partial data. Per-task
state is synchronized, removed on completion or timeout, and separate across
requests. Closing the transport also cancels in-flight streamed responses.

Validation: all 65 Swift tests and the arm64 iOS simulator build passed. Native
URLSession/URLProtocol fixtures exercise chunk callbacks, exact limit, overflow,
declared oversize, session reuse and in-flight closure. This is local transport
evidence, not live mTLS or physical-device qualification.

Milestone 3 advanced: the built-in streaming response bounds substep is complete.
Remaining SDK work includes scheduling, revocation freshness, application session
ownership and live integration. Backup/recovery qualification also remains.
Five broad milestones remain: legacy migration/device replacement; remaining
trust consumers/live firmware sessions; backup/recovery and SDK integration;
live provider/hardware compatibility; staging/key custody/recovery qualification.

## Resumable iOS renewal orchestration

The coordinator now resumes one persisted request through receipt recovery,
installation, activation, an explicit application session-replacement callback,
and acknowledgement. A callback failure stops acknowledgement while preserving
active selection. A reconstructed coordinator retries uncertain acknowledgement
with the successor and skips already-completed HTTP work. Missing receipts and
unrelated active versions fail closed. Request ID/TTL retention and cross-instance
serialization remain application responsibilities.

Validation: all 65 Swift tests passed, including expanded native Keychain tests
for saved-receipt activation, session-owner failure, reentrancy rejection, missing
receipt, lost acknowledgement, reconstruction and completed retry. The arm64 iOS
simulator build passed. No live endpoint or physical device was used.

Milestone 3 advanced: the resumable SDK orchestration substep is implemented.
Remaining work includes background scheduling, revocation freshness, wiring real
application session owners, live SDK integration and backup/recovery qualification.
Five broad milestones remain: migration/device replacement; trust consumers/live
firmware sessions; backup/recovery and SDK integration; provider/hardware;
staging/key custody/recovery qualification.

## Durable iOS expiry-based renewal scheduling

The SDK now determines the renewal due date from validated installed validity,
using the configured lead (default 30 days) capped at one third of the lifetime.
A ThisDeviceOnly schedule persists a device/predecessor-scoped request ID before
preparation and network work. `resumeIfDue` prioritizes that pending request
across restarts and active-version changes, and clears it only after successor
acknowledgement. The next check uses successor validity, preventing immediate
repeat renewal of short-lived replacements. Corrupt records, changed pending
TTL and unrelated active identities fail closed.

Validation: all 68 Swift tests and the arm64 iOS simulator build passed. Tests
cover timing boundaries and native Keychain persistence/recovery/cleanup. This
implements SDK due checks and durable pending scheduling, not OS wake delivery.
The host must wire its permitted background task, foreground checks, expiration
cancellation and error backoff. Physical-device background behavior is unproven.

Milestone 3 advanced. Remaining SDK work: host background integration, actual
application session owners, revocation freshness and live qualification. Backup/
recovery qualification also remains. Five broad milestones remain: migration/
device replacement; trust consumers/live sessions; backup/recovery and SDK
integration; provider/hardware; staging/key custody/recovery qualification.

## In-flight cancellation for iOS renewal

Native renewal/ack requests now register an operation-token callback that cancels
the current URLSession task and returns the cancellation status promptly. Token
handlers are synchronized, run outside the token lock, and unregister after the
request. Durable renewal state is preserved; an uncertain acknowledgement still
leaves the predecessor retired. Custom transports must implement their own
interruption support.

Validation: all 71 Swift tests and the arm64 iOS simulator build passed. Native
URLSession fixture tests cover interruption before request timeout, pre-cancelled
requests and session reuse; token tests cover races and reentry. This is a
prerequisite for host task expiration, not physical background execution evidence.
The sample app does not currently own a device PKI identity, so attaching device
renewal to its user-session lifecycle would not establish the required integration.

Milestone 3 advanced. Remaining: host background/device-session integration,
revocation freshness, live SDK and backup/recovery qualification. Five broad
milestones remain: migration/device replacement; trust consumers/live sessions;
backup/recovery and SDK integration; provider/hardware; staging/custody/recovery.

## iOS BackgroundTasks renewal adapter

An iOS-only adapter now registers a host-specified processing task, schedules
network-required requests, runs the coordinator on a serial worker, cancels via
the expiration token and completes the OS task after the attempt. Success uses
the certificate's due date; pending work/failure/expiration uses a configurable
bounded retry delay. Both renewal and scheduler failures are reported. The host
must supply permitted identifiers, processing mode, launch-time registration,
foreground checks and its actual device-session callback.

Validation: all 76 Swift tests passed, including shared scheduling/expiration
logic, and the adapter compiled for the arm64 iOS 13 simulator. These checks do
not demonstrate actual OS task delivery. No sample user identity was substituted
for device identity and no application configuration was silently changed.

Milestone 3 advanced: the native background adapter is implemented. Remaining:
host configuration/device-session wiring, physical wake/expiration validation,
revocation freshness, live SDK and backup/recovery qualification. Five broad
milestones remain: migration/device replacement; trust consumers/live sessions;
backup/recovery and SDK integration; provider/hardware; staging/custody/recovery.

## Independent roots for the iOS certificate-bundle consumer

Inspection of remaining iOS trust consumers found that the certificate-bundle
validator anchored at the root carried by the bundle. It now requires caller-
configured independent roots, restricts native trust to those anchors, disables
network completion of the chain and requires exact path order. Parse/validate/
test-import calls without configured roots fail closed. Signed leaf validity
must match metadata, and JSON/chain bounds are enforced.

Validation: all 76 Swift tests and the arm64 iOS simulator build passed. Native
bundle fixtures exercise independent-root acceptance, missing/unrelated roots,
validity mismatch, size limits and validation before test import. These checks
do not establish revocation freshness or production root distribution.

Milestone 2 advanced: one remaining certificate-bundle trust consumer now enforces
independent anchors. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Revocation freshness, live session
validation and other-platform consumer audits remain unfinished.

## Go certificate-bundle path validation

The Go bundle consumer previously checked adjacent signatures without establishing
independent root trust or full path validity. Parse/validate/TLS construction now
require caller-configured roots, perform ClientAuth x509 path verification and
match the exact supplied chain. JSON/chain bounds are enforced, and TLS identity
construction revalidates production key policy. Existing callers without root
configuration must migrate; roots must not be derived from the received bundle.

Validation: the full Go SDK suite passed with `GOWORK=off go test ./...`. Tests
cover independently rooted ECDSA/Ed25519/RSA bundles, missing/unrelated/nil roots,
expired issuers with valid leaves, size bounds and direct TLS policy bypass.
No live service or revocation freshness qualification was performed.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android/native bundle trust and
revocation freshness remain among the next consumer gaps.

## Android certificate-bundle trust validation

Android bundle parse/validate/test-import now require independently provisioned
roots and validate the PKIX path. Root validity, signing/path-length constraints,
chain order and ClientAuth-compatible EKUs are enforced explicitly. Input bounds
and trailing-DER rejection apply. The bundle no longer establishes its own trust.
Revocation lookup is explicitly disabled in this path-only validator until a
responder/freshness policy is configured; no revocation qualification is claimed.

Validation: 39 Android JVM unit tests passed, one live integration test skipped,
and the release build passed with Gradle 8.10.2/JDK 17. New path fixtures reject
untrusted roots, expired roots, non-CA issuers, path-length violations and a
server-only issuer. Native Android KeyStore/provider/device behavior remains
unqualified.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Native C bundle trust and verifier
revocation freshness remain among the next gaps.

## Native C trust-aware import gate

The native bundle path had a validation bypass: import directly invoked its
provider import callback. It now requires a trust-aware provider callback and
explicit independent root input, then revalidates before every import. Legacy
callback-only/old-sized interfaces fail closed. Embedded hosts have an explicit
validation-time import API; the existing wrapper requires a working wall clock.
Input size, embedded-NUL and allocator-pair checks were added, and truncated
field extraction no longer advances past a missing delimiter.

Validation: all 11 native tests passed on macOS arm64/Clang. The bundle test also
passed ASan/UBSan across all truncated fixture prefixes. Callback tests establish
dispatch/error behavior only: actual X.509 path validation, revocation freshness
and hardware/firmware provider integration remain unqualified. The native SDK
still delegates those responsibilities to the host cryptographic provider.

Milestone 2 advanced by closing import's validation bypass and making trust input
explicit. Five broad milestones remain: migration/device replacement; remaining
trust consumers/live sessions; backup/recovery and SDK integration; provider/
hardware; staging/custody/recovery. Native provider implementation/qualification
and verifier revocation freshness are still outstanding.

## Explicit current-CRL validation for Go bundles

The Go bundle API now offers mandatory-CRL validation and TLS construction modes.
They require independent trust plus one signed, current, full direct CRL for each
chain issuer, reject revoked leaves/intermediates and unsupported CRL scopes, and
reparse bounded signed DER instead of trusting mutable object fields. Path-only
APIs remain distinct and must not be treated as revocation evidence.

Validation: the complete Go SDK suite passed. Signed fixtures exercise valid
bundle/CRL combinations, freshness/signature/coverage failures, scoped rejection,
leaf/intermediate revocation and TLS failure without CRLs. Live publication,
refresh and persisted anti-rollback state remain outstanding.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Next revocation work includes durable
CRL update/freshness integration and equivalent platform verifiers.

## Durable Go CRL version state

A private file store now retains signed per-issuer CRLs with monotonic number/
thisUpdate checks, exact same-version retry and atomic synchronized replacement.
Initialization is explicit; missing/corrupt state does not reset the high-water
marks. Valid revocation of the current identity is persisted before subsequent
use is rejected. State is reloaded and signatures/freshness rechecked at use.
In-process instances share locking; cross-process serialization is host-owned.

Validation: the complete Go SDK suite and auth race tests passed. Fixtures cover
restart, concurrent instances, version conflict/rollback, revocation persistence,
staleness and missing/corrupt state. Filesystem snapshot rollback still requires
external monotonic/recovery reconciliation and is not solved by a local file.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Next revocation work: fetching/refresh,
runtime use/session termination and platform integration/qualification.

## Bounded HTTPS refresh for durable Go CRLs

The file store now fetches a complete issuer set from configured HTTPS endpoints,
refuses redirects/cookies, bounds response sizes and operation time, and accepts
DER/PEM or the controller JSON envelope with signed-metadata agreement. Only a
complete validated set reaches atomic update. Revoking updates are persisted
before the final identity check returns failure. Controller service-peer scope
is unchanged; caller configuration supplies authorized endpoints and TLS.

Validation: the full Go SDK suite and auth race tests passed with local HTTPS
fixtures for valid formats, metadata errors, redirects, cancellation, declared/
streamed limits, preserved state, revocation and rollback. No live deployment
qualification was performed.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Runtime refresh scheduling, validation
at use and session termination remain the next integration steps.

## Go TLS identity selection uses durable revocation state

The durable CRL store now builds a TLS config around a caller-managed signer,
matching its public key to the leaf and snapshotting public trust inputs.
New handshakes validate current file-backed CRL/certificate state with no static
identity fallback or session resumption. Server trust remains distinct and the
root is omitted from the client chain. Established connections are not closed
by this handshake-only guard.

Validation: the complete Go SDK suite and auth race tests passed. A local mTLS
server verifies the client, accepts pre-revocation requests and rejects new
connections after a persisted CRL update before its application handler. Tests
also cover signer mismatch and snapshot isolation. Hardware and live service
qualification remain open.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Next: active-session revocation/
expiry teardown and scheduled refresh integration.

## Go active-owner trust lifetime

The durable CRL store now issues a validated owner context that periodically
rechecks state and wakes at earlier certificate/CRL deadlines. Revocation,
expiry, missing/corrupt state or parent cancellation permanently ends the context.
Binding the existing WebSocket lifetime to it closes the established socket and
rejects later sends. Root-input changes and unbound media/other transport owners
still require explicit lifecycle integration.

Validation: the complete Go SDK suite and auth/transport race tests passed. Local
WebSocket/signature fixtures establish a session then revoke it, confirming
context and socket teardown. Deadline/missing-state/non-resurrection cases pass.
This is local integration evidence, not live firmware/media qualification.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Scheduled refresh, application wiring
and other-platform/live owner qualification remain outstanding.

## Scheduled Go CRL refresh worker

A context-owned worker now immediately refreshes configured CRLs, retries while
stored state remains valid and schedules ahead of signed expiry. Existing trust
deadlines bound network attempts; failures do not extend freshness. Revocation,
expired/unavailable state or parent cancellation ends the worker. It is designed
to run alongside the established-owner GuardContext, with host supervision.

Validation: the complete Go SDK suite and auth/transport race tests passed.
Local HTTPS tests cover request cancellation, transient 503 recovery, signed
revocation persistence and worker termination. Real application lifecycle and
live publication/refresh qualification remain open.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Next work includes concrete
application wiring and equivalent platform/runtime qualification.

## Public Go WebSocket client preserves configured PKI transport

Inspection found Client.Connect ignored the SDK HTTP/TLS client and dialed with
global defaults. It now uses the configured *http.Client, refuses redirects and
query-escapes device identity. Unsupported custom Do-only transports fail rather
than falling back. A public configured-client transport entry point is available.

Validation: the full Go SDK suite and full race suite passed. The public client
connects to a local mTLS-required WebSocket server using the CRL-guarded TLS config;
RunRefresh fetches signed revocation and GuardContext terminates the owner/socket.
Redirect and device-query encoding cases also pass. Actual deployed application/
media lifecycle and physical-device qualification remain outstanding.

Milestone 2 advanced through public SDK integration. Five broad milestones remain:
migration/device replacement; remaining trust consumers/live sessions; backup/
recovery and SDK integration; provider/hardware; staging/custody/recovery.

## Android key preservation and verified hardware policy

Android generation now reuses existing EC P-256 aliases and preserves certificate
and device metadata, with an in-process creation lock. It explicitly selects
P-256 and verifies actual Android Keystore protection instead of reporting the
requested setting. Hardware is the default; software acceptance requires an
explicit development/test option. Explicit StrongBox requires Android 12+ actual
security-level verification and never falls back. Failed verification preserves
the key for recovery rather than deleting an identity.

Validation: Android JVM tests, release AAR and instrumentation APK builds passed.
JVM tests cover curve and protection policy; instrumentation adds retry/reopen
SPKI and metadata preservation. No connected Android device was available, so
native execution and physical TEE/StrongBox qualification remain unverified.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Concrete SDK substeps still include
Android production certificate installation, durable renewal/session replacement,
and native runtime/provider qualification.

## Android installation and mTLS identity validation

Android now requires independently configured device roots and validates the
complete current trust path, Keystore key match and exact device leaf profile
before storing an issued certificate. The same gate runs before constructing
mTLS contexts. Certificate writes check synchronous persistence success; PEM
input is bounded and rejects unrelated/trailing data. Self-signed legacy test
fixtures require a separate explicit test-only option.

Validation: signed JVM fixtures cover valid issuance and trust/key/profile/expiry
failures; all Android unit tests, release AAR and instrumentation APK builds pass.
No physical Android execution was performed. Revocation freshness, durable
renewal/activation, backup exclusion and already-open session teardown remain.

Milestone 3 advanced; five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. The next Android SDK work is durable
renewal/activation and lifecycle/revocation integration, followed by runtime tests.

## Android durable renewal preparation and runtime verification

Android now prepares and persists the replacement key alias, exact CSR,
predecessor, request ID and TTL before any production renewal submission. Atomic
bounded records live outside Android backup and use file sync plus process/file
locking. Reopening verifies CSR signature/profile/key binding and rejects changed
parameters, corrupted records or missing keys. An interrupted pre-record attempt
reuses its deterministic replacement key; the predecessor remains installed.

Validation: the full JVM suite and release AAR build passed. Seven PKI tests ran
successfully on the local API 35 emulator, including durable preparation and
actual Android signed-certificate installation/rejection/mTLS trust gates. This
also verifies earlier key-reuse instrumentation. The emulator was stopped after
the run. Physical TEE/StrongBox qualification remains outstanding.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next requires production
submission and durable response receipts, activation/session replacement and
acknowledgement, plus revocation/lifecycle integration. Prepared state alone does
not replace the existing legacy renewal endpoint.

## Android production renewal transport and durable receipts

Prepared Android requests now submit through the predecessor mTLS identity to
the production renewal endpoint with HTTPS/redirect/response-size/deadline gates.
Verified matching responses persist as immutable atomic receipts outside backup
before returning. Retry reuses a saved receipt without a network call; installation
requires the saved response and a live overlap and preserves the predecessor.
Temporary HTTP resources are closed. Conflicting saved responses fail closed.

Validation: full JVM tests and release build passed. Eight API 35 emulator PKI
tests passed, covering predecessor mTLS, production request fields, redirects,
leaf mismatch, streamed oversize, expired overlap, offline receipt reuse and
predecessor preservation. Test transport certificates are explicit fixtures;
independent signed-profile installation also runs in the same suite. The emulator
was stopped afterward; physical hardware/live controller integration is unproven.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next needs activation,
session replacement, acknowledgement/retirement and in-flight cancellation,
followed by revocation/lifecycle and live provider qualification.

## Android renewal cancellation

CancellationToken now supports internal race-safe native cancellation handlers.
Production renewal registers the OkHttp call before execution, unregisters during
cleanup, maps cancelled I/O to CANCELLED, and checks cancellation before receipt
persistence. A receipt whose commit already began remains recoverable; cancellation
cannot undo server issuance. Other transports require explicit hook integration.

Validation: full JVM suite and release build passed. Tests cover handler removal,
repeated cancellation, registration races and callback reentrancy. All eight PKI
instrumentation tests passed on API 35, including a stalled authenticated renewal
that cancels within five seconds without persisting a receipt and then retries.
The local emulator was stopped after testing.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next needs durable activation,
session replacement and acknowledgement/retirement, followed by revocation and
application lifecycle integration and physical/live qualification.

## Android durable active identity selection

Android now explicitly initializes an active device identity, resolves it with
key/trust/fingerprint validation, and activates installed renewal successors using
the saved receipt and expected predecessor. Atomic bounded records outside backup
use file sync and process/file locking. Repeating completed activation is safe;
bootstrap cannot replace a different selection. Installation itself does not
switch identities. Missing/corrupted state fails instead of selecting a fallback.

Validation: full JVM suite and release build passed. Eight API 35 PKI tests passed,
covering pre-install rejection, separate install/activation, retry/reopen, bootstrap
rollback rejection and corrupt-state rejection. The initial instrumentation launch
was rejected before user storage unlocked; rerunning after RUNNING_UNLOCKED passed.
The emulator was stopped after testing. Physical hardware remains unqualified.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Next Android steps are session
replacement, acknowledgement/retirement and resumable lifecycle coordination,
then revocation and physical/live provider qualification.

## Android acknowledgement, retirement and resumable renewal

Android acknowledges through a fresh active-successor mTLS connection and persists
predecessor retirement before sending. Uncertain results remain ATTEMPTED and block
new predecessor mTLS contexts/active selection; confirmed results become monotonic
ACKNOWLEDGED and require no further request. The shared production transport keeps
HTTPS, redirect, bounded response, deadline and cancellation behavior for both calls.

The resumable coordinator now orders preparation, receipt retrieval/submission,
installation, activation, required host session replacement and acknowledgement.
Callback failure stops acknowledgement; retries repeat the idempotent replacement
callback. Same-device in-process reentrancy is rejected. Host cross-process and
unrelated low-level mutation coordination is still required.

Validation: full JVM suite and release/test builds passed. Eight API 35 tests
passed, including successor mTLS, disconnect-after-request retirement, old-context
construction rejection, retry/offline acknowledgement reuse and replacement failure
preventing network acknowledgement. The emulator was stopped after the run. These
fixtures do not prove actual host session closure or physical/live provider behavior.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next requires durable due-time
scheduling/lifecycle wiring, backup/recovery reconciliation and CRL/live session
integration, with physical/provider qualification still outstanding.

## Android durable renewal schedule

Android now calculates renewal due time from certificate lifetime and persists a
deterministic pending request before preparation/network activity. Bounded atomic
records outside backup preserve the predecessor/TTL through activation and retry;
conflicting parameters or corrupted records fail. The due-only coordinator clears
the matching schedule only after the successor is active and acknowledged, then
returns its next due time. No OS job is registered by these APIs.

Validation: JVM date tests cover short/normal/long lifetimes and invalid inputs;
the full JVM suite and release/test builds passed. Eight API 35 PKI tests passed,
including schedule reopen, TTL conflict, corruption, completed retirement clearing
the schedule and successor waiting state. Emulator user storage was verified
unlocked before instrumentation and the emulator was stopped after testing.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next needs native background
job/lifecycle wiring and backup/recovery reconciliation; CRL/live session and
physical/provider qualification remain outstanding.

## Android native background renewal jobs

A host-subclassed JobService adapter now invokes renewal on a worker thread,
schedules the next due date/retry, cancels the supplied token on Android stop and
suppresses late completion from stopped runs. It uses network constraints and
backoff, reports failures and supports explicit persisted-across-reboot scheduling.
The host declares the protected service/permissions and supplies its configured
store plus session replacement callback. No application-specific identity is
invented by the library.

Validation: full JVM suite and release/test builds passed. Ten API 35 tests passed,
including registered JobScheduler execution, next-due scheduling and OS stop
cancellation plus existing PKI renewal tests. SDK35 source documentation was used
to verify lifecycle behavior. User storage was unlocked before the run; the emulator
was stopped afterward. Reboot/Doze/OEM policy and real host owner behavior remain
unqualified.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Next SDK work includes backup/recovery
reconciliation and remaining trust/lifecycle consumers, with host application and
physical/live qualification still required.

## Android identity metadata backup boundary

Certificate and device-identity metadata now use bounded synced atomic records
outside Android backup, with immutable per-alias identity binding. Existing
SharedPreferences data requires explicit migration through the current key and
certificate trust/profile policy. Conflicting metadata or missing keys fail;
generation cannot replace the key behind a saved identity. Legacy data remains
available for inspection but is never an automatic identity fallback.

Validation: full JVM tests and release/test builds passed. Eleven API 35 tests
passed, including matching/idempotent migration, certificate mismatch, identity
rebinding and deleted-key rejection with no key regeneration. The existing PKI
and background-job suite passes against the new storage. The emulator was stopped
after testing. Actual backup transport/restore and fleet entitlement/revocation
reconciliation remain unqualified.

Milestone 3 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Remaining work includes other-platform
trust/lifecycle consumers, host integration and operational backup/recovery,
physical/provider and staging qualification.

## Android signed/current issuer CRL gate

Production Android identity validation now requires a host CRL provider and checks
one signed current full/direct CRL for every chain issuer after trust/profile
validation. It checks issuer CRL-signing permission, AKI/SKI, positive number,
freshness and non-root serial revocation, rejecting unsupported scoped/delta/
indirect forms. Installation/migration/active lookup/new mTLS construction use the
gate. No missing-provider fallback exists outside explicit test-certificate mode.

Validation: full JVM tests and release/test builds passed. Signed fixtures cover
leaf/intermediate revocation, coverage, signature/authority/number/freshness,
unsupported forms and trailing data. Eleven API 35 tests passed, including a CRL
update rejecting new mTLS contexts. The emulator was stopped after testing.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next requires durable CRL
rollback state, refresh and existing-owner teardown; host and physical/live
provider qualification remain outstanding.

## Android durable CRL rollback state

Android now explicitly creates/reopens a bounded CRL store outside backup, verifies
signed current updates and rejects lower numbers, same-number conflicts and older
issue times. Issuer history binds subject/public key and remains across same-key
certificate reissuance. Updates persist revocations of the current identity;
provider reads revalidate signatures/freshness before identity serial checks.
Missing/corrupt state is never silently recreated by open/read/update.

CRL commits verify read-back and fsync the parent directory after AtomicFile,
whose source shows that rename failures can be logged without throwing. The
older identity/renewal journals require the same follow-up commit verification.

Validation: full JVM suite and release build passed. Eleven API 35 tests passed,
covering reopen, identical updates, rollback/version conflicts, revoked-identity
persistence, corruption and missing-state rejection. The emulator was stopped
afterward. No power-loss or snapshot-recovery qualification is claimed.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Next work includes Android journal
commit verification, CRL refresh and existing-owner teardown, followed by host/
physical/live qualification.

## Verified commits across Android PKI journals

The CRL commit-verification follow-up now covers all Android identity and renewal
journals through a shared bounded writer: content sync, AtomicFile completion,
read-back verification and directory sync. Schedule deletion likewise checks all
atomic-file variants are gone and synchronizes the directory. Existing locks and
record limits are preserved; repeated write boilerplate has been removed.

Validation: full JVM tests and release/test builds passed. Twelve API 35 tests
passed, including injected silent replacement/deletion failures preserving prior
state, bounds and normal writes/deletes, plus all renewal, CRL and JobScheduler
cases. The emulator was stopped afterward. Physical power-loss and snapshot
recovery qualification remain outstanding.

Milestone 3 advanced; the Android journal commit-verification substep is complete.
Five broad milestones remain: migration/device replacement; remaining trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery. Next Android work is CRL refresh and existing-owner
teardown, with host and operational recovery qualification still required.

## Android authenticated CRL refresh transport

The Android CRL store now fetches a configured HTTPS issuer set with a shared
network deadline, cancellation and bounded responses. It disables redirects,
cookies/cache and transparent retries while preserving caller-provided TLS/auth
configuration. DER, PEM and controller JSON are decoded; envelope metadata must
match signed content. All fetches finish before durable update, and a revoking
update persists before refresh reports revocation. Owned HTTP resources close.

Validation: full JVM suite and release/test builds passed. Twelve API 35 tests
passed, including redirect refusal, controller metadata mismatch/acceptance,
revocation persistence and stalled-fetch cancellation preserving state. JVM tests
also cover DER/PEM and bounds. The emulator was stopped after the run. No live
controller/distribution authorization or owner lifecycle qualification is claimed.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next needs scheduled CRL
refresh and existing-owner teardown, with host and physical/live qualification
still outstanding.
