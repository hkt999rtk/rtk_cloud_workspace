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
- [ ] Legacy inventory/import automation and staged migration.
- [x] Offline CA CRL signing, immutable CRL publication, and exact consumer acknowledgment gates.
- [x] Device replacement with bounded overlap, successor acknowledgment and Go file-based installation helpers.
- [x] Certificate-bound device tokens with live API/refresh/MQTT authentication checks.
- [x] Device WebSocket revalidation, MQTT authentication leases and broker cache/session operator tooling.
- [x] Cumulative Root distrust, consumer acknowledgment gates, atomic local root state and opt-in API TLS reload.
- [x] Explicit reconciliation of historical pending Root removals with governance and acknowledgment gates.
- [ ] Remaining trust-consumer adapters, direct media termination and live trust/session qualification.
- [x] OpenBao Kubernetes login and projected-token reauthentication.
- [x] Explicit runtime/PKI schema migration; Product mode workloads skip startup DDL.
- [ ] OpenBao Raft deployment, scoped workload policies and database grants.
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
