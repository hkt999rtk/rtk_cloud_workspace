# Registered Services Architecture: Implementation Plan

Status: source implementation and workspace integration are merged; environment
rollout remains open. Not deployed.

Owner: rtk_cloud_workspace.

Last reviewed: 2026-09-20.

Classification: supporting-note.

Applies to: the MQTT foundation and optional-service architecture. Registry,
Product, factory, Cloud Admin, service-process, and SDK changes are merged in
their owning repositories. This is not a completed deployment, migration, or
release.

Source delivery checkpoint (2026-09-20): contracts PR #161, Cloud Client PR
#562, Account Manager PR #330 (with coverage repair PR #331), Video Cloud PR
#668, Cloud Admin PR #400, Ameba PR #9, and workspace integration PR #477 are
merged. Their CI gates passed, including Account Manager's PostgreSQL report,
Video Cloud's Postgres/EMQX integration, Cloud Admin's UI validation, and
Ameba's Linux, macOS, sanitizer, and QEMU jobs. Workspace PR #477 merged at
`120edd262359b25ca05b7c02e216682799da676f`; its integrated gate also passed
mobile and desktop E2E, catalog/policy checks, and coverage aggregate/redaction.
These source results do not qualify a live environment or enable Product
writes.

## 1. Design Entry Point

The proposed shared contract is
[Platform Service Registration And Product Entitlements](../../repos/rtk_cloud_contracts_doc/service_registration.md).
It owns registration identity, catalog eligibility, Product selection, factory
binding, token grants, and compatibility semantics. This plan owns cross-repo
sequencing and verification. Existing wire contracts remain current until the
explicit cutover; this document is not a second API specification.

Use Account Manager as the initial Platform Server. Keep core PKI and device
token issuance in the existing runtime repository while removing their startup
dependency on optional video features. Implement a registry module and service
registration clients first. MQTT, Shadow, WebRTC, and video storage must have
independently deployable processes and readiness in the first implementation.
They remain in the existing repository and may share internal packages; moving
them to separate repositories is outside the first implementation.

## 2. Current Evidence And Gaps

Reviewed local snapshot: workspace `006f8586`, contracts `7fd4dc6`, Account
Manager `db70846`, Video Cloud `e81aed0`, and Cloud Admin `913eea6`.
These are code/document observations, not deployed-environment evidence.

In the integrated source snapshot, Account Manager has a dedicated mTLS
registry listener, approved workload option codes, immutable manifests,
publication CAS, heartbeat
leases, catalog read, Product grant revisions, run-pinned JWTs, and factory
admission checks. Cloud Admin reads the catalog, while Video Cloud factory
enrollment derives options from the signed run context. Account Manager
database-backed tests cover registration, unregistered-option rejection,
third-party option selection, Product/run pinning, and factory admission.
A local PostgreSQL integration test now connects a real mTLS registration
listener and Video Cloud registration client to the resulting Account Manager
catalog, Product creation, and production-run pinning. A separate Account
Manager API integration test carries a newly registered third-party option
through Product creation, the signed production JWT, factory admission, and a
Product-bound Claim Token; it rejects a widened Claim Token request. The
Product-grant Claim Token API now accepts registered option syntax instead of
the legacy fixed list, while unbound legacy Claim Tokens retain that list.
An additional isolated PostgreSQL cross-process test now uses distinct
approved test service certificates to register MQTT and a third-party plugin
through Account Manager's dedicated mTLS listener, then creates a revisioned
Product and signed production run. It drives the separately compiled Video
Cloud factory application through Account Manager's real HTTP admission API,
rejects a widened factory option echo before signing, and verifies the exact
option set, Product revision, and grant digest in the durable factory
entitlement. The issued device certificate then completes a real mTLS
handshake to Video Cloud's token handler; the signed device JWT retains those
exact options and revisions. The test passed locally. Its test CA and direct
registration HTTP client do not prove the deployed Platform issuer or the
production Video Cloud registrar, and it does not exercise outage recovery.
The workspace PR PostgreSQL coverage job and manual baseline workflow now
compile the independent Video Cloud factory fixture and explicitly run this
chain plus the production-registrar mTLS probe with the required database and
source-root inputs. Video Cloud changes also select the Account Manager
PostgreSQL job; changes to the manual baseline workflow select policy tests.
The CI-required flag turns a missing fixture into a failure
instead of a successful skip. A local workflow guard checks the step order,
inputs, and selector. Both workflows require explicit Go test `pass` events
and upload raw JSON evidence for both cross-service selectors, so package
success after a skip is insufficient. These CI edits have not yet produced a
remote run or artifact. The local workspace `test-matrix` passed after
regenerating its spec-traceability document. The shared design is now a
registered draft specification with eight planned requirements; new Account
Manager and Cloud Admin operations map to their owning requirements. Formal
workflow definitions and draft operator operations describe the
registration-to-token chain, entitlement delivery, and guarded rollout. The
required-mode spec inventory
now reports zero blocking source-mapping findings. These mappings and proposed
operator steps are not execution evidence or release qualification. After the
mapping edits, required-mode inventory, OpenAPI validation, and focused
workflow tests passed; the full workspace `test-matrix` also passed on that
revision.
Separate Video Cloud tests cover signed-run factory derivation and the mTLS
device-token endpoint carrying a third-party grant, then omitting it after an
explicit local grant edit. The cross-process chain now joins local mTLS
registration, factory issuance, and a network device-token TLS handshake, but
has not run against deployed Platform PKI or staging workloads.
The workspace Account+Video smoke runner now has an opt-in Product mode that
reads the live registered catalog and Product, creates a Product-bound Claim
Token, waits for activation, and compares the exact service-option set and
Product revision in the device token. Local mock-server tests reject expanded
options, stale revisions, invalid JWT signatures, and pending activation. The
runner fetches the device-host public key and verifies the token signature.
It has not run against a deployed registered-service environment and does not
perform the registration; it is an acceptance harness, not completion evidence
for the cross-service rollout.
This is a partial implementation: WebRTC/storage process shells and registration
clients are now local. A default-off WebRTC edge route can precede core handler
removal; a separate default-off core HTTP gateway can forward storage media
routes to the private storage Pod and remove core clip handlers. Their real
Platform/PKI integration, live routing, full dependency failure/recovery, and
removal of every monolithic startup dependency are unverified. General
cross-service entitlement migration/revocation delivery, full SDK
compatibility, deployed legacy data backfill, and rollout remain.
Account Manager now has a report-first, digest-pinned, all-or-none legacy grant
backfill tool and integration tests preserving HTTP-only Shadow Products and
existing runs. An isolated PostgreSQL run passed dry-run, apply, idempotence,
stale-snapshot, unknown-code, and corrupt-grant checks. It has not been applied
to a deployed database; the required backup, preflight, reconciliation, and
rollout evidence remain open.
The mTLS listener checks the approved workload record and reloads an
issuer-signed service CRL at TLS handshake and on every registration request.
Local TLS listener tests cover valid registration, missing client certificate,
CRL rotation before a fresh connection, and revocation over an already reused
keep-alive connection. A PostgreSQL-backed listener test now also exercises
an approved MQTT and Shadow certificate through real mTLS registration and
ready heartbeats into the catalog: MQTT becomes selectable, while ready Shadow
stays suspended until an explicit status change. An unapproved, otherwise
valid certificate is denied. With `VIDEO_CLOUD_SOURCE_ROOT` set to the local
Video Cloud checkout, the same test launches a separate process using its
production service-registration client for the Shadow registration and
heartbeat; that cross-module run passed against isolated PostgreSQL. The same
test now checks that suspended Shadow cannot be selected, that the old catalog
revision cannot be reused after activation, and that a fresh Product and
production run pin the registered option grant. The test uses a local CA and a
fixed Shadow probe, not the deployed Platform issuer or full Shadow worker.
The real certificate issuer, CRL update pipeline, failure/replay under live
dependencies, and deployment still need evidence.
`product_service_revision` is carried locally in the new device-token path,
and Video Cloud now also carries a local `entitlement_revision` from durable
device grants into token projections and JWTs. Neither is a released
cross-service guarantee; token issuance rejects stale local entitlement
projections, and revisioned MQTT-only grants cannot access reserved Shadow
topics or HTTP Shadow endpoints. MQTT authentication rechecks revisioned grants against the backing
entitlement and caps such sessions at 60 seconds; a verified global
authorization-staleness bound is still missing. Device WebSocket connections
now pin the local revision and check it on frames and periodic idle sweeps.
Direct Account Manager provisioning now binds new outbox operations to a
Product grant inside the device transaction, forwards that snapshot through
the existing direct-HTTP publisher, and makes Video Cloud persist/compare the
entitlement before activation. Same-operation replay keeps the original
outbox revision. The opt-in Product-write gate now requires authenticated
`direct_http` delivery; the legacy bus worker cannot silently discard grant
fields. Local tests cover replay, rejected caller echo, forwarding, entitlement
conflicts, preserved factory evidence, and revisioned JWT issuance. Account
Manager now resolves an existing pin from a Claim Token, completed factory
admission, or successful lifecycle outbox, ignoring editable device metadata;
their conflicting revisions fail closed. Local PostgreSQL tests cover each
source across a later Product edit. This is not an end-to-end deployed path:
legacy devices without revisioned provenance, ambiguous pins, and an
authorized entitlement migration/revocation workflow still require explicit
reconciliation.
The first Account Device association of an existing factory entitlement is
now persisted as a write-once binding in memory and PostgreSQL, advances the
entitlement revision, and refreshes the Redis token projection. Tests reject
a second Account Device and preserve the binding on factory replay.
Video Cloud now has an authenticated internal receiver for complete, ordered
Account Manager entitlement snapshots. It atomically applies a newer platform
revision to an existing, identity-bound grant, advances the local revision,
refreshes Redis, accepts identical replay, and rejects stale/conflicting
updates. Isolated PostgreSQL and HTTP/token tests cover revocation and narrowed
options. Account Manager now has a gated, authenticated Device entitlement
snapshot API. It derives the baseline from a successful revisioned provision
or prior snapshot, requires Device and Product authorization, and commits each
monotonic full-snapshot revision with an audit event and direct-HTTP outbox in
one transaction. The publisher sends `PUT` to the Video Cloud receiver and
the inbox records its success/failure without changing Device metadata. Local
PostgreSQL tests cover replay, narrowing, explicit latest-Product-revision
migration, rejection of an ungranted option, atomic rollback, immutable
history, and physical Device deletion after history is retained. This is not a
released migration or revocation path: cross-process authenticated E2E,
legacy-device reconciliation, delivery failure/replay under concurrency,
authorization-freshness bounds, retention/purge policy for detached immutable
history, and staging activation remain unverified.
Video Cloud now uses an atomic Redis revision comparison for entitlement
projections, so a delayed older writer cannot replace a newer revocation.
Local tests cover cache-write failure after durable commit, identical snapshot
replay repairing that projection, and the Account Manager publisher retrying a
receiver `503`; a real Redis integration test checks the monotonic write.
These tests do not yet establish a live end-to-end failure-recovery guarantee.
New WebRTC signaling receipts are locally pinned to `entitlement_revision`:
answer waits and new TURN allocation checks reject superseded grants. The
local `pkiturn` sweep now also rechecks the durable video-streaming grant and
revision before preserving an existing coturn allocation; an isolated
PostgreSQL/Redis/coturn run canceled the allocation after grant removal and
failed health when entitlement data became unavailable. This does not yet
establish a deployed revocation bound or terminate direct/non-TURN media paths.
Video Cloud has a reusable HTTPS registration/heartbeat/drain client, a
colocated registrar for the independent EMQX broker, and an initial independent
Shadow worker. Both use distinct service certificates and only advertise ready
after their live dependency probes and Platform heartbeat. Local composition,
unit tests, packaged-binary verification, and one isolated EMQX multi-replica
subscription test exist; startup with Platform/Redis and approved PKI,
certificate rotation, failure/replay, broker device ACL, and cutover are not
yet verified. The Shadow MQTT request handler and outbox pause
when registration is unavailable. The Shadow worker now also mounts only its
two HTTP Shadow route families, using token/entitlement/SigV4 checks and the
same Platform-lease availability gate. A default-off core HTTP switch proxies
those routes to the private worker while preserving the signed Host; local
HTTP-server tests cover real SigV4, Product-grant rejection/revocation, route
isolation, and outage responses, but authenticated cross-process traffic remains
unverified. The default API still hosts optional HTTP handlers until each
independent cutover. WebRTC and video storage now have opt-in restricted HTTP
entrypoints, dedicated mTLS registration manifests, and readiness probes for
PostgreSQL plus Redis signaling or S3 respectively. The package contains
disabled systemd units. They still need real Platform/PKI/dependency integration,
real edge route ownership cutover, and removal of unused composition dependencies.
An isolated integration run with PostgreSQL, Redis, mock S3, and an mTLS test
server verified ready/unready heartbeats, route isolation, and drain; it is not
evidence of approved Account Manager registration or staging operation.
Optional-process plugin routes now also return `503` when the local Platform
registration heartbeat is unavailable; a PostgreSQL/mock-S3 integration test
checks that a storage dependency outage removes route access after the unready
heartbeat. The shared registrar now rejects absent/expired Platform lease
deadlines and caps local availability at 90 seconds. This is a direct-instance
guard, not dynamic gateway routing.
The WebRTC-scoped process now starts even with unusable blob configuration and
no clip key. The core API still initializes both plugin paths by default, but
can skip signaling Redis and clip-key/direct-upload setup with separate
default-off cutover flags. Token/MQTT paths remain mounted. This is local
routing/composition evidence only: the core storage gateway now returns `503`
when its fixed private upstream is unavailable, but does not select instances
directly from Platform registration records. Core
firmware/OTA still require blob storage.
Shadow worker replicas reuse the pre-existing shared-subscription group rather
than each receiving every MQTT request. A local EMQX 6.2.3 test observed one
response per request with two workers; failure/replay, mixed-generation overlap,
and staging broker authorization remain unverified.
An SDK source audit found JavaScript, Android, iOS, and Native token bundles
pass the JWT as an opaque string rather than interpreting service option codes.
The Go token-cache parser likewise reads only scope, subject, and expiry, and a
local regression test accepts an unknown option alongside MQTT. Ameba's local
MQTT credential extractor now requires `mqtt` in `service_options` for a
revisioned token, ignores unknown option codes, and still accepts legacy tokens
without revision/option claims. This is a local preflight check after verified
TLS, not JWT signature verification or a replacement for broker authorization.
Its native host test and the pinned GCC 10.3.1 ARM/QEMU CI suite pass on the
merged Ameba PR. The local workspace checkout still lacks that pinned ARM
toolchain and SDK root; other SDKs have not gained local feature gates, so
plugin access remains subject to server-side authorization. End-to-end SDK
compatibility validation is still pending.
A 2026-09-20 read-only staging inventory is explicitly `NO-GO` for cutover:
current staging has no new registrar/optional-service workloads, dedicated
registration listener or plugin ingress routes, and no CI-published image for
this uncommitted implementation. The local `kubectl` is v1.32 against a v1.36
server, beyond supported version skew. Existing Linode/GHCR repository access
and Account Manager-to-certissuer mTLS checks passed, but did not qualify new
image digests, service certificates, migrations, or application E2E. The
sanitized preflight report is outside the repository under `/tmp`; no staging
state was changed.
The local LKE workload preflight now checks more than Secret key presence:
Account Manager's listener key pair, private DNS identity and current CRL,
plus each selected registrar's exact service CN, client-auth profile, key
pair, verified chain to the listener's client CA, signed non-revoked issuer
CRL, and reciprocal server trust. Local tests reject mismatched keys, wrong
CA/CN, stale or revoked CRLs and accept the listener's DER/PEM CRL formats.
These checks do not replace actual PKI issuance receipts, workload approval,
live TLS/CRL rotation evidence, or the protected-environment Go/No-Go gate.
Cloud Admin's canonical scoped Product page reads the registered catalog. The
separate legacy Customer Product API and its older UI now read the active
Brand Cloud's catalog, forward its revision on grant changes, and retain
registered option codes in Product responses and impact previews. Those older
UI choices remain available only while the Product-write gate is off. Editing
Product metadata without changing services omits grant fields, including when
a previously selected plugin has become unavailable. The older UI is not the
canonical `/console/clouds/{id}/products` route; this closes a remaining API
compatibility gap rather than replacing the scoped Product page. Focused local
API and UI-helper tests cover it; browser and deployed cross-service E2E remain
open.
Cloud Admin's Test Lab now projects HTTP Shadow from the `iot_shadow` grant
and MQTT Shadow from both `iot_shadow` and `mqtt`, rather than inferring Shadow
from MQTT alone. Its UI explains the separate grants, including legacy
HTTP-only Shadow. The Cloud Admin OpenAPI now documents both live catalog
endpoints, dynamic option codes, and Product-write catalog revisions rather
than a fixed built-in enum. Local Test Lab and UI tests pass; this is not
deployed authorization evidence.
Account Manager's local OpenAPI likewise documents its catalog response and
revisioned Product writes, and no longer constrains every service option to
the four built-ins. The shared contracts OpenAPI remains the canonical wire
reference for the dedicated mTLS registration listener; the local Account
Manager OpenAPI does not imply those routes run on its user API port.
The local LKE renderer now has a default-off Account Manager service-registry
port, Secret mount, and narrowly scoped NetworkPolicy. It checks the required
Secret keys before a selected Account Manager rollout. Both Video Cloud LKE
image build paths include the four independent service binaries. A separate
default-off manifest can deploy one MQTT foundation registrar with a stable,
preapproved instance identity; it checks for its dedicated client-identity
Secret and private Platform endpoint. A second default-off path packages one
Shadow worker with its own identity, broker access, and core MQTT subscription
cutover. Its separate default-off HTTP switch requires a ready private
EndpointSlice, admits only core-to-worker traffic, and replaces core Shadow
handlers with a fail-closed gateway. A third default-off path now packages a WebRTC service Pod with its
own identity, PostgreSQL and Redis signaling dependencies, and TURN registry
access. Separate default-off flags can render exact WebRTC ingress routes and
disable the core WebRTC handlers, with observed-readiness and rollback-order
checks. A fourth default-off path now packages one private video-storage Pod
with a distinct identity, S3 readiness, and the existing clip crypto key.
An independent default-off core cutover switch checks that its private Service
has a ready EndpointSlice, opens a core-to-storage NetworkPolicy, and replaces
core media handlers with a segment-aware HTTP forwarder. Both public hosts
still reach core first. HTTP Shadow routes belong to core by default and move
only when the independent switch is enabled. The renderer does not create
issuer material, approve workload identities, or dynamically discover storage instances from
Platform leases. These local packaging steps do not change the staging
`NO-GO` verdict until PKI, CI images, clients, routes, and end-to-end
acceptance are proven.

### Edge route ownership for optional HTTP services

The current public ingress sends each host's `/` prefix to the core API. The
cutover must keep the existing public host and device-mTLS host semantics while
moving plugin handling to a ready, registered instance. Kubernetes Service
readiness can remove an unready instance from endpoints, but this is not yet
a registry-driven gateway discovery implementation. No cutover flag may be
enabled until the corresponding gateway route, private Service, network policy,
approved workload certificate, and fail-closed readiness have been verified
together.

| Owner | Edge match on the existing Video Cloud host | Boundary |
| --- | --- | --- |
| WebRTC | Exact `/api/request_webrtc`, `/api/request_webrtc/ice`, `/api/request_webrtc/answer`, `/api/request_webrtc/close` | Do not use prefix `/api/request_webrtc`; adjacent paths remain core/404. Preserve the device-mTLS host if device clients use it. |
| Video storage | Keep the existing public and device-mTLS ingress on core; when the plugin is ready, core's HTTP gateway forwards exact legacy `/upload_clip`, `/total_clips`, `/enum_clips`, `/get_clip_info`, `/delete_clip`, `/v1/media/playback-key`, plus prefix `/download/` to the private storage Service | Core skips clip crypto and upload handlers after cutover, although firmware/OTA still use shared blob storage; the storage Pod performs authorization and stays registration-gated. |
| Video storage device media | Core gateway forwards only `/v1/devices/{device_id}/clip-uploads[/...]` and `/v1/devices/{device_id}/clips/{clip_id}/playback-session` with exact segment grammar | All other `/v1/devices/` paths remain outside the storage proxy. This avoids ingress-nginx host-wide regex effects and preserves the existing URLs. |
| Shadow (target; not cut over) | Keep both existing ingress hosts on core; its HTTP gateway forwards only `/things/` and `/api/things/shadow/ListNamedShadowsForThing/` to the private registered Shadow worker | The worker must serve the same authorized Shadow handlers and preserve the original HTTP Host for SigV4 verification. Core must stop serving local Shadow HTTP handlers after cutover; unrelated `/api/things/` paths remain core/404. |

Rollout order differs by route owner. For WebRTC, deploy and verify the optional
process with routes unpublished, add exact edge rules, then disable matching
core handlers in a separate operation. LKE applies workloads before public
ingress when both are selected, so combined first activation must not disable
core handlers before the edge exists. Require ready, registration-gated
EndpointSlice targets before edge publication and observed live edge ownership
before WebRTC core cutover. Rollback re-enables core WebRTC handlers before
removing edge rules. For storage, keep both existing hosts on core and switch
its HTTP forwarding separately after the private Pod is ready. Test both hosts
for plugin success, non-plugin pass-through, and plugin-unready failure.
Do not let both MQTT Shadow workers subscribe at once. Shadow HTTP handoff is a
separate switch from MQTT subscription ownership: first verify the registered
worker's private HTTP endpoint and token/entitlement checks, then switch core
from local Shadow handlers to a fail-closed private forwarder. It must not
select the worker through a broad public ingress prefix or rewrite the signed
Host on SigV4 requests. Video storage's dynamic device-media routes
use the core HTTP gateway as a segment-aware forwarder, not a broad ingress
prefix. Deploy the storage Pod privately first with its own Platform identity
and S3 readiness, then switch core from local handlers to forwarding only
after the private Service is observed ready. An upstream outage returns `503`
and must never fall back to local storage handling or silently broaden grants.
Rollback switches core media handlers back only after confirming the core still
has its local S3 and clip-key dependencies. Registration or a private Pod alone
is not authorization to cut over. The storage handler rejects extra path
segments after `playback-session`; the gateway matcher must use the same exact
segment grammar. First registration selects the manifest but leaves every
optional service `suspended`, even when its private Pod reports ready; only
`mqtt` starts `active`. Keep the option suspended during the private-Pod phase,
verify both hosts and authorization after route cutover, then use the
platform-admin status operation to activate new Product selection. A later
registration or heartbeat must not undo suspension. The LKE renderer does not
automate this catalog gate; it remains a release precondition. Existing
optional rows created by older builds may already be active: inventory and
suspend them before enabling their Product-write path, rather than assuming
the new first-registration or database default changed existing state.

The following table records the initial baseline that motivated this plan, not
the current worktree state; several target deltas above are now implemented
locally but remain unreleased.

| Area | Initial baseline evidence | Target delta |
| --- | --- | --- |
| Product persistence | Account Manager `internal/store/device_item_profiles.go` persists `ServiceOptions` and calls `validateClaimServiceOptions` | Validate catalog-backed selection and persist immutable service revisions |
| Option validation | Account Manager `internal/store/device_claims.go` recognizes four static codes | Replace global allowlist with authoritative catalog validation; preserve safe legacy reads |
| Inconsistent option propagation | Account Manager `internal/channel/channel.go` and Video Cloud `internal/entitlement/entitlement.go` recognize only MQTT, streaming, and storage | Audit all active paths and retained compatibility validators; preserve `iot_shadow` end to end |
| Product UI | Cloud Admin `web/src/cloud-products.mjs` exports three hard-coded `productServices`; `CloudProducts.jsx` renders them | Read scoped catalog; show MQTT required and selected/unavailable plugins accurately |
| Additional UI mapping | Cloud Admin `web/src/main.jsx` contains separate service labels/defaults | Remove authorization decisions from display constants; audit every Product create path |
| Factory boundary | Video Cloud `internal/factoryenroll/service.go` normalizes body options; OpenAPI declares a fixed option enum | Resolve a production-run-bound Product revision; body options become an equality-checked echo |
| Entitlement defaults | Video Cloud `internal/entitlement/entitlement.go` has a static `DefaultServices()` including video services | Audit callers and remove broad fallback from the new grant path |
| Tokens | Video Cloud `internal/auth/auth.go` already serializes `service_options`; `internal/httpapi/router.go` loads service entitlements | Reuse the claim, add revision binding, and cover issuance/recovery/refresh and transport enforcement |
| Service PKI | Video Cloud `internal/pki/service_client_certificate.go` validates restricted service client certificates under the service trust domain | Reuse that profile; add registration namespace/instance authorization and renewal integration |
| Service startup | Video Cloud `internal/apiapp` composes core transport and optional features together | Make MQTT-only startup independent of Shadow, TURN/signaling, and storage dependencies |

Two design conflicts are resolved as an explicit target transition: static
factory-declared options become registry-backed Product grants; independent
HTTP-only Shadow Products become a compatibility case once MQTT is required
for new Products. Existing documents receive transition notes rather than
claiming the replacement is already running.

## 3. Delivery Sequence

Each phase should be a reviewable change with its own evidence. The order matters:
new grants must not become selectable before the downstream grant/token path can
carry and enforce them.

| Phase | Owner and concrete work | Completion evidence |
| --- | --- | --- |
| 0. Design first | Contracts design, workspace architecture/plan, service design transition notes, and the new-environment Internal Service PKI bootstrap ceremony | Shared ownership, bootstrap allowlist, revoke/seal gate, and migration rules are reviewable; documentation checks pass |
| 1. Contract schemas and core registration | Contracts: OpenAPI routes, manifest/response schemas, error contracts, fixtures, requirement/test mapping. Account Manager: durable registry, manifest revisions, instance leases, scoped catalog and publication operations. PKI/deployment: deployment bootstrap session, generated short-lived bootstrap leaf, initial identity issuance, installation acknowledgement, CRL revocation, and sealed session | Positive registration; wrong trust domain/environment, name takeover, conflicting manifest, stale publication, invalid dependency, replay, expired/revoked workload certificate, expired/sealed bootstrap session, unallowlisted subject, and incomplete-installation refusal tests |
| 2. Service adapters and optional startup | Video Cloud: registration/renewal/drain adapters for MQTT, Shadow, WebRTC, storage; manifests use existing option codes. Deployment: core-first startup, service-specific readiness, endpoint references, and independent optional dependencies | Start platform+MQTT with no optional services, TURN, or media storage; register several replicas; expire one/all; recover; rotate a certificate without changing service ownership |
| 3. Product and production context | Account Manager: authoritative transactional catalog validation, Product service revisions, pinned production runs/JWT digest. Cloud Admin: catalog-driven create/edit UI and explicit unavailable states | MQTT-only creation; selectable registered plugin; unknown/unregistered selection rejected; catalog race handled; existing Product metadata edits survive plugin outage; old devices gain no services |
| 4. Factory, tokens, and enforcement | Account Manager and Video Cloud: trusted entitlement snapshot/projection delivery; factory echo equality; extensible option parsing; revisioned issuance/recovery/refresh; route/topic/session checks. SDKs: preserve known/unknown claims without granting unknown features | Body tampering, run revision mismatch, cross-tenant substitution, revoked entitlement on refresh, dependent-option removal, and expired/stale projections fail safely; MQTT-only device denied each plugin |
| 5. Migration and activation | Workspace/deployment plus all consumers: dry-run Product/device reconciliation, legacy grant revisions, compatible consumer rollout, staged feature activation, backup/restore and rollback evidence | Existing grants compare equal before/after migration; HTTP-only legacy Shadow is preserved; new Product baseline enforced; new third-party/test option travels registration-to-token without central enum edits |

Phases 3 and 4 may ship behind disabled write gates. Enable catalog-backed
Product creation only after the complete factory/token/enforcement path passes.
The catalog's `product_writes_enabled` field keeps Cloud Admin on the prior
Product choices while the write gate is off; it switches to registered choices
only at cutover. Dynamic catalog display must not imply that a new grant is usable.

## 4. Repository Work Packages

### Account Manager / Platform Server

- Add durable service definitions, immutable manifests, instance leases, workload
  namespace bindings, and Product service revisions. Reuse the existing database,
  authenticated identity, audit, migration, and outbox patterns.
- Extend Product/create-update transactions and production runs to bind the
  exact approved service snapshot. Keep Brand Cloud/Product RBAC in force.
- Publish authenticated entitlement revisions; store cursors/idempotency so
  retries cannot broaden grants or replay a superseded revision.
- Reconcile legacy devices lacking revisioned Claim Token, issued factory, or
  successful lifecycle provenance before enabling their revisioned
  reprovisioning; add an audited entitlement migration for intentional grant
  changes. Keep `direct_http` mandatory while the Product-write gate is enabled,
  or upgrade the bus consumer to carry and verify the same tuple.
- Keep replica liveness out of Product/device authorization persistence. Use
  explicit administrative actions for entitlement suspension and revocation.

### Core Runtime, MQTT, And Optional Services

- Factor registration, token verification, and grant checks into small reusable
  interfaces; do not build a dynamic code-loading plugin framework.
- Retain device activation, factory/PKI, token issuer, and MQTT authorization in
  the core deployment. Isolate optional configuration validation and storage
  dependencies in `internal/apiapp` and relevant composition roots.
- Wrap existing Shadow, signaling/TURN, and clip/upload implementations with
  service-specific registration and health. TURN node discovery stays inside
  the WebRTC service; it is not the Product capability registry.
- Audit HTTP handlers, MQTT reserved-topic ACLs, WebSocket paths, WebRTC session
  creation/continuation, storage upload authorization, recovery, and refresh for
  consistent grant enforcement. Avoid a registry query in per-message paths.
- Bound authorization freshness and invalidate active sessions on explicit
  revocation. Existing dependency-failure policy remains applicable.

### Cloud Admin, SDKs, And Deployment

- Render service choices from the authorized catalog, with MQTT shown as the
  required foundation. Display unregistered/offline/deprecated options honestly
  and preserve unavailable selections while editing unrelated Product metadata.
- Separate display labels from authorization codes. Provide catalog loading,
  stale revision, permission denial, and unavailable errors without checkbox
  defaults that could grant video features.
- Update Go, JavaScript, Android, iOS, Native, and Ameba/FreeRTOS consumers and
  fixtures where they enumerate capabilities or derive them from device type.
  Unknown option codes must not unlock local operations.
- Use a deployment-generated, short-lived bootstrap X.509 leaf only for the
  fixed initial internal-identity allowlist. Revoke it and delete its Secret and
  bootstrap-only policy after every identity passes mTLS installation checks.
  Reuse the resulting service certificate issuance and secret injection. Deploy
  Platform Server and PKI before service registration; deploy MQTT before new
  MQTT-dependent Product grants. Optional plugin failure must not block core
  health, identity, registration, or MQTT traffic.
- Include registry/grant state and workload bindings in matched backup/restore;
  expire restored instance leases and require fresh authenticated registration.

## 5. Migration And Rollback

1. Inventory Product/production-run/device grants and active token consumers.
   Report unknown codes and missing Product associations; do not infer them
   from device category or silently normalize them to all services.
2. Register deployed services with platform-issued certificates and publish
   validated initial manifests. Registration is a real workload action, not an
   enum-to-database seed that marks absent services ready.
3. Backfill immutable legacy grant revisions preserving exact permissions.
   Existing Products without MQTT remain legacy until explicitly migrated.
   Keep existing runs' nullable legacy binding until they expire or are
   explicitly reconciled; silently pinning one would invalidate its already
   signed factory JWT. Do not add services or change CA selection.
   The operator first runs the Account Manager service-grant backfill report on
   a write-frozen snapshot. Malformed or mismatched stored options block apply;
   no option is inferred from category or a live service catalog. Explicit
   apply inserts revision 1 with `legacy=true`, catalog revision 0, and the
   exact recorded option set for Products lacking a grant. Apply requires the
   reviewed report's snapshot SHA-256, so intervening changes fail closed.
   Already-versioned
   Products are checked but not rewritten. Existing runs keep their nullable
   legacy revision; newly issued runs bind the resulting immutable snapshot.
4. Roll out compatible token readers and service enforcement, then issuers and
   factory binding. Compare resulting grants with the pre-migration snapshot.
5. Enable the registered-service Product write path per environment. Exercise
   MQTT-only, MQTT+Shadow, MQTT+WebRTC, and MQTT+storage scenarios independently.
6. Remove compatibility fallbacks only after old token/run/device use is
   measured and reconciled. Retain a compatible rollback build and durable
   registry/grant records. Never roll back to implicit all-service defaults.

No live migration, cloud rollout, or production activation has been performed.
Their future delivery should follow the owning RTK PR/CI
and deployment workflows with the concrete evidence above.

### Deployment flow diagram

The first-trust deployment gate is shown in the companion [deployment flow
diagram](service-deployment-flow.html). It is part of this design: the
deployment controller creates one scoped session, the one-shot bootstrap Job
installs the initial identities, each MQTT or optional-service workload
acknowledges its own verified mTLS identity through `PKI_BOOTSTRAP_SESSION_ID`,
and the controller must revoke the bootstrap leaf, publish and acknowledge its
CRL, and seal the session before optional services start. The session variable
is removed after sealing; it is not a runtime credential.

The customer-facing manufacturing boundary and dedicated factory hostname are
shown in the [public factory enrollment sequence](factory-enrollment-public-gateway.md).
Cloud Test Lab remains a development-only test flow and is not a mass-production
enrollment path.

<iframe src="service-deployment-flow.html" title="Service deployment first-trust flow" style="width:100%;height:760px;border:1px solid #c8ced8;background:#f5f5f5" loading="lazy"></iframe>

## 6. Acceptance Scenarios

- A fresh MQTT-only environment creates a Product and enrolls/activates a device;
  its token contains only `mqtt` and optional-service requests are denied.
- Before a plugin registers, its option cannot be selected through UI or a
  direct Product API call. Registration makes the declared option visible but
  suspended; a ready instance, verified routes, and explicit platform-admin
  activation are required before it becomes selectable.
- A valid but unauthorized service certificate cannot claim another service ID
  or option namespace. Invalid, expired, revoked, and cross-environment
  certificates cannot register, heartbeat, or take over instances.
- Repeated registration is idempotent. Multi-replica startup and rolling
  revision publication cannot overwrite newer manifests or duplicate options.
- Factory body options, Claim Token creation, direct provisioning, and token
  request bodies cannot expand the Product/run/device grant.
- A newly registered option changes neither existing Products nor existing
  device tokens. An explicit Product revision affects future runs only until
  an authorized device entitlement migration is performed.
- A heartbeat outage disables new selection/routing eligibility but preserves
  current grants. Explicit entitlement revocation prevents refresh and ends
  affected long-lived sessions within the documented enforcement bound.
- New Products require MQTT; pre-existing HTTP-only Shadow grants continue to
  behave as recorded. MQTT alone cannot enter reserved Shadow topics.
- Registration/control-plane outage has no hidden allow-all fallback and no
  synchronous registry dependency for each MQTT message. Authorized requests
  to an offline plugin return unavailable, not a misleading missing-grant error.
- A test plugin option is registered, selected, enrolled, issued in a token,
  and enforced without modifying central UI/token option enums. The test plugin
  supplies its own handler and grants no permission to unrelated handlers.
- Restore and rollback preserve grant revisions and require fresh instance
  leases; they cannot resurrect a revoked workload or silently broaden grants.

## 7. Design Parameters For Implementation Review

The proposed defaults are Account Manager as Platform Server, MQTT required for
new Products, existing option strings retained, and independently deployable
MQTT, Shadow, WebRTC, and storage processes in the first implementation.
These choices are explicit so review
can change them before code is written.

Use a 30-second heartbeat and a 90-second instance lease initially. Registration
requests are limited to 64 KiB and 64 declared option codes; code identifiers
must match `[a-z][a-z0-9_]{0,63}`. Use HTTP 409 for manifest/catalog revision
conflicts, 401 for invalid service credentials, 403 for a valid but unauthorized
service identity, and 503 when the authoritative registry is unavailable.
Service namespace ownership is bound to the workload identity during PKI
provisioning. Exact wire schemas belong in OpenAPI before implementation.

Phase 4 must define and test a maximum authorization-staleness window across
token TTL, projection age, and active sessions before release. Until then,
do not claim that grant removal immediately ends existing sessions.

## 8. Product OTA Service And Billing Extension (2026-09-25)

Status: target design approved for implementation in this worktree; no
commercial OTA price is active and no staging CDN or invoice evidence is
recorded by this section. This extends the existing optional-service plan
without treating the source-level registration code as a deployed service.

The canonical cross-repository contract is
[Product OTA Delivery And Billing](../../repos/rtk_cloud_contracts_doc/ota_delivery_and_billing.md).
It defines the data-plane, four authoritative meters, Product and device
authorization, operational reconciliation, exact units, and activation gate.
The shared [firmware campaign](../../repos/rtk_cloud_contracts_doc/firmware_campaign.md),
[service registration](../../repos/rtk_cloud_contracts_doc/service_registration.md),
[usage](../../repos/rtk_cloud_contracts_doc/billing_usage.md), and
[pricing/invoicing](../../repos/rtk_cloud_contracts_doc/pricing_and_invoicing.md)
contracts own their general boundaries. Video Cloud's
[OTA implementation design](../../repos/rtk_video_cloud/docs/ota-billing-design.md)
maps the contract to local paths and state. Billing's
[OTA billing design](../../repos/rtk_billing/docs/ota-device-task-billing.md)
owns pricing representation and invoice qualification. Cloud Admin's
[pricing research](../../repos/rtk_cloud_admin/docs/service-pricing-research.md)
is a non-binding commercial reference.

| Step | Owner and change | Done evidence |
| --- | --- | --- |
| 1. Contract first | Update the five canonical OTA/service/usage/pricing documents and this cross-repo plan; mark current source separately from target behavior. | Documentation links and workspace `docs-check` / `contracts-check` pass. |
| 2. Product and service gate | Account Manager approves and registers `ota` with `mqtt` dependency; OTA runs independently with its own workload identity, readiness and lease. Cloud Admin uses the selected Product's `ota` service option to show the dashboard or explicit disabled message; the backend validates the latest revisioned grant before new OTA operations. | Suspended-first, lease loss, Product edit/migration, device grant and background dispatch tests. |
| 3. Direct CDN delivery | Video Cloud emits path-scoped, at-most-ten-minute Akamai URLs for a private Linode/Akamai Object Storage origin; firmware downloads Range bytes directly and reports verified completion. | Staging CDN, private origin, Range, expiry, revocation and no-API-byte-proxy evidence. |
| 4. Authoritative usage | Persist immutable first-assignment, first verified download, physical object write, and UTC-month byte-time receipts, each atomically paired with a shared Billing outbox fact; reconcile remote object inventory and CDN edge logs. | Database crash/replay, changed-payload rejection, object inventory, month-boundary and outbox-recovery tests. |
| 5. Close and commercial gate | Billing accepts exact Product-scoped facts, two independent period seals, and four proposed rates. Account Manager's Platform seal lists every Product with an OTA-enabled grant revision before month end; producer and fact Products must be a subset of that historical set. Finance approves a future effective version only after staging qualification. | Missing-seal denial, producer-only unauthorized Product, retired/zero-use Product, invoice arithmetic, tax, period cutoff and no-retrocharge evidence. |

During migration, core keeps its existing OTA control-plane handlers and
historical `ota/` objects until the dedicated service passes cutover checks.
Before deploying the updated core with active OTA campaigns, operators must
qualify CDN signing and private-origin delivery for those legacy objects;
core does not serve firmware bytes through an internal GET endpoint. Without
CDN configuration, it refuses a download grant. Historical core device
reports retain their prior evidence and transition rules until cutover, while
the registered billable service requires the verified SHA-256, size, and
downloaded-before-installing sequence. The dedicated
service writes new firmware only under `ota-billable-v1/`; its object inventory,
write and storage receipts, and producer seal cover that namespace. Historical
objects have no new upload-attempt evidence and must not be turned into
retroactive charges. They require the separate documented legacy cleanup path.
Device OTA requests after cutover must reach the dedicated service through a
direct edge route that preserves verified client mTLS identity; core rejects a
misrouted device request rather than forwarding it through an HTTP proxy.
Before submitting the Platform monthly seal, Account Manager fences concurrent
grant writes and verifies each historical grant digest. An empty verified OTA
seal can close a zero-usage month only when the active rate version contains
OTA rates alone; other priced services retain their usage-completeness gate.

The proposed customer rates before tax are NT$96/1,000 assignments,
NT$0.96/GiB accepted successful downloads, NT$0.96/GiB-month of stored OTA
objects, and NT$144/million successful object creations. No additional
OTA customer object-read or raw CDN egress charge is proposed. Akamai edge
logs serve provider-cost reconciliation and anomaly investigation; they are
not a substitute for the authenticated device completion receipt.

The producer seal requires a persisted, reviewed CDN operational export for
the same organization and UTC month with no unresolved anomaly. A missing,
failed or incomplete export blocks the producer seal even when its customer
meter counts are zero. The export digest and receipt count are bound to the
new producer seal. An immutable seal prepared by a pre-review build may be
retried with its original payload only after a positive review is recorded;
the historical payload is never rewritten. DataStream delivery and collector
qualification remain part of protected-environment activation. Product
disable and release revoke preserve
objects and history until explicit
physical deletion. Existing CDN URLs can remain usable for their bounded
expiry, at most ten minutes, while new grants stop upon revocation. After
Product OTA disable, an authenticated device may report an already assigned
deployment only with its previously issued artifact grant that passed an
enabled Product-grant check, matching frozen release, hash and size, within
48 hours after that URL expires. This does not
issue another URL or assignment. A valid `downloaded` report creates its
single receipt in the server acceptance month, including when that month is
later than disable; the producer must seal that later month before Billing
can charge it. A disable racing with the grant check/response is bounded by
the issued URL's ten-minute expiry; the grant row records the checked Product
revision and digest, not strict wall-clock precedence over disable. The
proposed OTA rates remain inactive.
