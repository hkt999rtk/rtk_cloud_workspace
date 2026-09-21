# Sequence-flow implementation checkpoint

Date: 2026-09-21. This is an execution ledger, not an acceptance claim. The
source-of-truth contract/status inventory remains
[`sequence_diagrams.md`](../../repos/rtk_cloud_contracts_doc/sequence_diagrams.md).

The agreed scope is all 15 incomplete flows across cloud services, mobile SDK,
and AmebaPro2. The Pro2 example intentionally embeds test credentials and a
private key in firmware; this test load path is unchanged and is **not** a
production protected-zone gate for this plan. The brand owns APNs/FCM; RTK's
future responsibility is reliable, tenant-scoped webhook delivery. Physical
reset never implicitly transfers, unbinds, or revokes cloud ownership.
The SD-023 audit found one additional integration gap in a flow previously
marked Implemented. A later OTA re-audit found seven more cloud-only or
proposed paths (SD-039–SD-045) whose end-to-end status had been overstated;
they are tracked below without dropping any of the original 15 flows.
Per Product direction, real-board and live-environment tests are deferred work
outside this plan and are not completion gates in the table below.

| Stage | Flow | Current execution result | Remaining acceptance work |
| --- | --- | --- | --- |
| P0 | All 50 sequence flows | Seven HTML atlases contain all 50 numbered current-state flows; the initial audit redrew 16 changed figures and synchronized all 50 captions. SD-049 has a companion reconciliation detail figure. A Chromium render audit of the original 50 figures corrected 15 legacy right-edge self-call labels (12 were clipped). A later OTA audit identified SD-039–SD-045 as overstating device or consumer-app implementation and redrew those seven figures with explicit integration gaps. The stale, tracked pre-redesign PDF was removed; Git can restore it if needed. | Recheck geometry after each diagram change and audit the remaining figures against future code changes. |
| P1 | SD-001 Factory registration | Existing enrollment/certissuer boundaries documented. | MES/fixture protocol, key custody, and factory process sign-off. |
| P1 | SD-007 Cloud activation | Packaged `direct_http` outbox/inbox route verified and redrawn. The worker now requires a bounded HTTP 200 receipt matching the pending device, organization, account device, activity generation, and activated state. A two-database, cross-repository HTTP test runs the real Account Manager worker against the Video Cloud route: initial 503, mismatched 200, remote commit with lost response, and successful replay leave one account-side result and one unchanged remote activation epoch. The final test passes with race checking; activation does not imply online. | Roll out the new Video Cloud receipt to all replicas before the stricter Account Manager worker. Staging deployment/configuration, credentials/TLS, real service-restart recovery, and operational retry timing remain unverified. |
| P1 | SD-008 Readiness | A new provisioning operation clears pre-cloud registry presence; human status writes cannot fabricate or erase cloud-managed presence. Incoming owner-presence events require the mapped video identity and observation order. The configured direct-HTTP path reads Video Cloud's authenticated current owner snapshot and fails closed on outage or identity mismatch. The authenticated `DeviceOnlineChanged` receiver projects through the inbox. Video Cloud packages a default-off transactional latest-state outbox and retry worker with generation-fenced acknowledgements. The worker backfills mapped active devices, reconciles device/session state, and refreshes delivered online observations; Account Manager expires event-only online evidence after five minutes. The sender now requires a bounded HTTP 200 receipt whose `message_id` matches the sent generation, and the receiver's receipt is checked by isolated PostgreSQL API tests. Real-PostgreSQL outbox/reconciliation and worker HTTP tests pass. Video Cloud prevents stale local disconnect and rolls back failed owner registration. | Staging live-owner, outage/replay, identity, disconnect, receiver-delivery, and sustained-load acceptance remains. Event-only projection has bounded lag and is not a point-in-time session proof. |
| P1 | SD-046 Deactivation | Video Cloud evicts shared/local owner routing after revocation; failed eviction and clip cleanup return errors for retry. The Account Manager command carries its activation `activity_id`; Video Cloud's PostgreSQL update rejects stale generations atomically with HTTP 409, and Account Manager ignores stale generation results in device projection and current readiness. A cross-instance per-device lock covers activation/deactivation, factory projection, legacy clip writes, and direct-upload authorization/completion. Direct uploads persist a server-owned `activation_epoch`; the verifier rechecks it under that lock before publishing `ready`. Ordinary device saves compare the stored epoch and active state, rejecting stale writes across deactivation/reactivation; explicit lifecycle saves compare the prior pair. Object deletion failure preserves metadata for command retry. Signed PUT expiry is capped by upload-session expiry; failed/expired descriptors receive a delayed object-deletion pass after one hour, with durable completion and five-minute failure retry. Verifier ready/retry/failure commits now compare the live worker, attempt, lease, and session expiry, so a stale claim cannot undo an expiry or a later claim. Complete Go, isolated PostgreSQL, and focused race suites pass; unit/PostgreSQL tests simulate late PUT cleanup, retry, stale claim, and expiry. The staging clip-load key installer and database cleanup now share the per-device lock and preserve atomic transactions; cleanup uses literal prefixes and only deletes failed/expired rows whose object cleanup was recorded complete, rejects changed keys, and has isolated PostgreSQL race tests. A two-pool PostgreSQL/WebSocket test now runs activity-scoped deactivation with a live PostgreSQL notification listener and proves the idle remote owner closes without another frame; three race-enabled runs passed. Notification failure retains the shared row for retry and stale notifications cannot close replacement sessions. Listener startup now reconciles missed notifications; the two-pool PostgreSQL/TCP test passes both live-notification and missed-notification recovery in three race-enabled runs. An optional, default-off EMQX management path now inventories authenticated broker sessions for the current organization, re-reads exact device matches, deletes their token-derived client IDs, and checks that no match remains. Missing authenticated attributes or broker API failure keep the deactivation command retryable after revocation; targeted mock API and MQTT adapter tests pass; an owned disposable EMQX test also passes two race-enabled runs for target TCP closure, offline-session removal, and preservation of other-device/app connections. A 10,001-session mock inventory confirms the enlarged bounded scan finds a target beyond the previous page ceiling. | Very long in-flight PUTs can complete after the delayed pass; bucket lifecycle or later inventory remains the final guard. The staging cleanup tool does not remove S3 objects or ready clips, so stop traffic, drain the verifier, delete ready clips through the API, and reconcile objects before deleting residual rows. Audit other external/raw SQL writers, then verify separate-process/TCP timing and prolonged-listener-outage behavior, HTTP worker failure/replay, staging MQTT broker TCP/TLS eviction, live late-PUT cleanup, and staging acceptance. |
| P1 | SD-049 Admin transfer | Account Manager rejects cross-org claim/reclaim when local activation history exists. Its authenticated Video Cloud call reserves an absent device under the remote per-device activation lock; existing lifecycle/media or competing-fence state returns `409`, uncertain results return `503`. The account transaction commits a version-derived reservation ID with the new owner; later provision carries it, and Video Cloud rejects independent, stale, or wrong-generation activation before grant projection. A definite pre-commit error cancels the exact reservation before releasing account row locks. The platform-admin reconciliation endpoint now reads the current remote binding and locked account claim/device/token state; only an unchanged source generation can be cancelled with reason, evidence, and audit. Committed or inconsistent state cannot be cancelled. Memory/API and isolated PostgreSQL tests cover these decisions plus both reserve/activation race orderings. A two-database, cross-repository HTTP test now covers successful transfer/activation, stale and wrong-generation activation rejection, committed-fence cancellation denial, a lost reserve response followed by guarded reconciliation, and definite pre-commit cleanup/retry; it passed twice with race checking. | The fence does not migrate an already-active Video Cloud device or its media. Staging cross-service race and failure/replay acceptance, operational reconciliation acceptance, and active-runtime/media migration remain. |
| P2 | SD-004 Local discovery | Android/iOS adapters exist; gap shown explicitly. | Product app discovery/GATT and Pro2 setup endpoint. |
| P2 | SD-005 Wi-Fi provisioning | Mobile and Pro2 independent paths shown explicitly; no phone-to-Pro2 claim. | Secure device-side handoff, persistence, retry, and redaction policy. |
| P2 | SD-016 Time sync | Pro2 host tests now cover timeout, success, elapsed time, and a later SNTP correction. | Long-running clock behavior under host fault/soak tests. |
| P2 | SD-048 Factory reset | Reset/ownership contract corrected; embedded test credentials retained. | Product-specific reset implementation; hardware acceptance is outside this plan. |
| P3 | SD-023 Telemetry ingress (newly uncovered) | Video Cloud now serves the four SDKs' default `/telemetry/ingest` path for device-scoped events. It resolves omitted identity from the authenticated active device, rejects contradictory mapping and app source, stamps receive time, and reuses normalized validation/idempotency. Opt-in integration tests now send real JavaScript, Android, iOS, and Native C SDK events over HTTP to a running local Video Cloud test server, confirm idempotent replay and reject spoofed organization identity; all four passed together under Go race checking. With PostgreSQL configured, ordinary product events have a persisted `(video_cloud_devid,event_id)` key; isolated two-pool tests verify same-device replay/conflict, cross-device ID reuse, and preservation of rows during the old-key migration. Fleet `device.health.summary` has a separate, device-scoped 32-day Redis/Valkey fingerprint receipt: exact replay leaves current state untouched, changed same-device replay returns HTTP 409, and isolated cross-device, service-recreation, legacy-key, and AOF-restart tests pass. The fleet query merge now retains both devices when their event IDs match. Intake rejects ordinary events older than configured product retention (90 days by default) and hot health summaries older than 32 days before writing. The full Video Cloud Go suite passes. A default-off Pro2 MQTT memory-sample producer and Cloud MQTT intake now locally pass trusted-identity, replay, and receipt tests; the product write and cloud-intake receipt are not one transaction. Runtime remains Partial. | Qualify the normalized producer, MQTT route, and SDKs against a deployed software environment. Coordinate old PostgreSQL writers before the key migration. Verify fleet AOF `everysec`/no-eviction configuration and recovery, decide whether same-device IDs require cross-store uniqueness, and define future-dated event handling, app-source authorization, and sampling policy. |
| P3 | SD-027 Brand event delivery | A default-off Video Cloud path has claim-bound per-org registration, encrypted HMAC secret, metadata-only PostgreSQL outbox, retry worker, generation-fenced attempt receipts, and an authenticated receipt read. Cloud Admin has owner-checked BFF subscription and receipt routes using a dedicated service credential; handler tests cover live owner checks, tenant routing, stale ownership versions, and origin/JSON rejection. The owner-only Cloud Settings UI now registers, disables, and inspects attempts without displaying the submitted secret; desktop/mobile mocked-BFF browser tests pass. The legacy global webhook is suppressed when enabled. Built-in lifecycle state changes now commit their brand outbox rows with the `devices` write; an isolated PostgreSQL test proves rollback on enqueue failure and no duplicate notifier enqueue. Outside opt-in MQTT intake, generic events attempt the durable brand outbox before compatibility delivery and the observational logger. In the opt-in MQTT path, the brand notifier stages routing metadata and PostgreSQL commits subscribed outbox plus cloud-intake acceptance in one transaction; failure-injection tests prove rollback in both directions, and an MQTT adapter/legacy-handler test covers the real route. Separate PostgreSQL and real EMQX tests verify that cloud-intake acceptance also commits without an enabled brand subscription. A per-device/event PostgreSQL advisory lock now serializes the pre-handler replay check, handler, and acceptance across API instances; two-store concurrent delivery and conflict tests cover the path. Opt-in bounded terminal-event/receipt pruning is tested but defaults to disabled. Dual-key subscription decryption, startup verification, explicit bounded re-encryption, a guarded operator tool, and a staged rollout guide now exist. An opt-in MQTT message subscriber now requests QoS 1 under a persistent broker session with an explicit stable client ID; QoS 1 handler failure withholds PUBACK and disconnects for broker replay. An opt-in QoS 1 `event_receipt` with `status=accepted` on `down/commands` now requires a committed current-organization cloud-intake row matching `event_id`, type/subtype, and payload digest; it does not depend on brand delivery. The Pro2 MQTT transport/service expose QoS 1 event publication, broker PUBACK handling, and a validated accepted-receipt callback; a local QEMU broker round trip and earlier default-off MQTT/WebRTC firmware builds pass. An opt-in DeviceBoot producer and NOR-journal replay path now pass host tests; the new firmware branch is not board-qualified. Brand still owns APNs/FCM. | The firmware example now has an opt-in boot-event producer and journal replay path, but normal builds keep it off; generic device events still need a Product-owned durable-buffer binding and software end-to-end acceptance. Legacy device-state, log, and compatibility-delivery side effects in the handler remain outside the intake/outbox transaction; they can commit before an intake-write failure and repeat on retry. The advisory lock prevents concurrent first-time handler execution. The reply uses QoS 1 and waits for broker PUBACK, but a clean-session device can still miss it while offline. The MQTT replay option is default-off and does not cover QoS 0 publishers, broker session loss, or device crash before replay; broker acknowledgement is not Video Cloud acceptance. Retention-period approval, deployment secret wiring, service-level browser/replay/security acceptance, production key-rotation rollout, and brand receiver acceptance remain. Hardware qualification is outside this plan. |
| P3 | SD-028 Telemetry replay | Cloud event identity/idempotency audited. The Pro2 QoS 1 event/receipt protocol API and local QEMU packet exchange pass. Its optional oldest-event replay scheduler passes native host retry, service-recreation, ID-match, and timer-wrap tests; an earlier default-off WebRTC firmware build passes. A static SDK 9.6e preflight finds a 624 KiB NOR candidate at `0xF64000`–`0x1000000` without overlap against currently active partitions, but also confirms that the stock LittleFS mount autoformats on failure. A new opt-in, paired-sector NOR journal validates payload/ID, commits data and Cloud ACK separately, reads writes back, replays the oldest across simulated restarts, and fails closed on corrupt unacknowledged data. It keeps the old ACK while erasing/reusing accepted data, then resets that ACK only when the newer event is accepted. Host tests cover interrupted data/ACK writes, partial data/ACK erases, capacity, mismatch, and service/MQTT receipt integration; the latter recreates both service and journal before retransmission. An opt-in raw-NOR adapter checks boot media, JEDEC-reported capacity, candidate-region bounds, and bit-clearing writes without formatting; the firmware example now binds it only behind explicit exclusive-region build flags. Its mock-NOR host test and Cortex-M33 syntax compile pass. Opt-in DeviceBoot and normalized memory-sample producers record separate 128-bit-random-ID events before MQTT startup; mixed producer-to-journal-to-service replay and matching receipts pass host tests. The default-off Cloud MQTT intake locally validates and persists the normalized sample before sending its receipt. The producers and opt-in firmware branch pass Cortex-M33 syntax checks, but have not passed a full GCC 10.3 build or board test. The 624 KiB candidate would hold at most 78 events if exclusively reserved; no physical flash persistence is claimed. | Product-approved exclusive flash reservation, additional product-event categories and sampling policy, host power-loss/OTA fault injection, capacity/drop/expiry policy, and software end-to-end replay acceptance. Torn appends into blank or ACKed pairs are quarantined; media corruption still needs an explicit offline recovery policy. Hardware driver qualification is outside this plan. |
| P3 | SD-029 Health alert | Existing health/online facts audited. HTTP telemetry ingestion now recognizes the contract's `payload.health` field in the fleet health store, while preserving legacy state fields; authorized, freshness-aware fleet queries read the normalized state. Product-telemetry and HTTP health tests pass. The current-state sequence now shows this HTTP route, not an unimplemented MQTT/WebSocket health-alert path. | Product alert thresholds, suppression/clear/recovery behavior, brand-webhook alert mapping, app policy, and staging acceptance. |
| P4 | SD-039 OTA check | Cloud Product OTA check, eligibility policy, required-capability filtering, and offered deployment pass local HTTP tests. A Pro2 non-streaming worker measures caller-provided device facts, advertises Product-configured capabilities, and handles assigned/deferred/no-update plus prior-report outcomes in host tests. The firmware example has default-off immediate/cadence/retry scheduling behind an explicit Product port. Contract remains proposed; runtime Partial. | Product supplies measured version/hardware/anti-rollback/time, the advertised capability list, and cadence. Hardware qualification is deferred outside this software plan. |
| P4 | SD-040 App-initiated OTA | An authorized consumer app can query an eligible frozen target and request its existing dispatch through `/v1/app/ota/devices/{device_id}` and `:request`. The route reuses Account Manager ownership authorization, cannot select a release or bypass rollout policy, and only requeues an existing target; normal dispatcher pacing still applies. JavaScript, Go, Android, and iOS SDK helpers plus HTTP/service tests cover the boundary. The Pro2 opt-in task copies and coalesces the resulting notice. Contract remains proposed; runtime Partial. | Product supplies the OTA port and consumer-owned confirmation/presentation UX. Hardware qualification is deferred outside this software plan. |
| P4 | SD-041 Cloud OTA rollout | Cloud frozen targets, pacing/dispatch, queryable deployment state, report recovery, and campaign safety exist. Pro2 now has the check/grant/download/signature worker, durable report journal, portable orchestrator, a power-loss-safe two-sector intent store, a NOR-only SDK 9.6e FW1/FW2 install backend, and explicit `cycle_enter`/`cycle_leave` serialization for shared token/HTTPS/MQTT/flash resources. A combined host test cuts power after inactive-slot verification, reopens persistent state, and finishes the update without rewriting the image. Clean GCC 10.3.1/newlib 4.1.0 links pass. Contract remains proposed; runtime Partial. | Product supplies measurement/scratch/trust/health bindings plus exclusive intent and report-journal sectors. Staging operations are outside this software plan. |
| P4 | SD-042 OTA artifact verification | Cloud finalization now verifies Product OTA Manifest Signature V1 against configured trusted Ed25519 public keys and fails closed on missing/unknown keys or bad signatures. Artifact grants and ranged delivery recheck scope/target/policy; already-issued URLs stop working after release revocation. Release-required capabilities filter device eligibility. Pro2 enforces exact Range framing, durable resume, downloaded/read-back SHA-256, Product trust-callback verification, installed-slot SHA-256, and SDK image signature. Contract remains proposed; runtime Partial. | Product supplies signing/trust-anchor rotation policy, real nonbootable scratch/checkpoint storage, exclusive report sectors, and measured device inputs. Hardware qualification is deferred outside this software plan. |
| P4 | SD-043 OTA install | Cloud validates `succeeded` against the target version. The coordinator persists `STAGING` before the first slot write, resumes durable install states, and records `installing`/`rebooting` before reset. The NOR backend verifies installed SHA-256 plus SDK signature, selects the target, observes exact slot/version/health, confirms boot, and emits `verifying`/`succeeded`. A deterministic health-policy helper enforces minimum observation, required signals, fatal failure, and fail-closed timeout. Host, sanitizer, Cortex-M33 syntax, integrated power-loss recovery, and GCC 10.3.1 link tests pass. Contract remains proposed; runtime Partial. | Product supplies scratch, intent, report regions and measurement/trust/health signals. Hardware qualification is deferred outside this software plan. |
| P4 | SD-044 OTA rollback | Cloud records rolled-back events and campaign safety state with exact-replay recovery. The coordinator persists rollback selection and emits `rolled_back` only after the previous slot/version is observed active. The NOR backend restores the prior slot, invalidates the unhealthy target, and requests reset. Deterministic health policy and host fault tests cover timeout, fatal signal, failed reboot, terminal-clear retry, backend idempotency, and healthy/unhealthy paths. Contract remains proposed; runtime Partial. | Product supplies the health signal mapping, timing policy, storage configuration, and external port. Hardware qualification is deferred outside this software plan. |
| P4 | SD-045 App OTA progress | The consumer App API returns ownership-authorized Cloud-recorded current/target version, campaign/release/deployment state, `updated_at`, and `last_reported_at`; it never infers success. JavaScript, Go, Android, and iOS SDK helpers plus route/service tests cover query and request. Product-specific UI remains consumer-owned. Contract remains proposed; runtime Partial. | Integrate the consumer UI and Product device-report source. Real-board and live-environment acceptance are explicitly outside this software plan. |
| P5 | SD-037 Multipart resume | Additive Video Cloud S3 multipart path now creates a persisted provider session, durably binds 8 MiB part descriptors, signs exact part PUTs, reconciles uploaded parts, completes only matching manifests, and streams the final ciphertext for whole-object SHA-256 before `ready`. Existing one-shot PUT remains. Native C/C++ SDK exposes create, part authorization, part listing, complete, and abort control calls alongside its streaming signed-PUT helper. Service/HTTP/S3-protocol tests and native HTTP route tests pass. An isolated PostgreSQL-plus-MinIO test now reconnects after part 1 and before verification, then uses the production database lifecycle lock through `ready`; runtime remains Partial. Pro2 examples currently provide live MQTT/WebRTC streaming only, with no stored-clip recorder or native SDK link. | Product-defined encrypted recording/file source and writable partition contract, persistent Pro2/device multipart producer, configured provider qualification, host restart/fault behavior, and bucket incomplete-multipart lifecycle policy. Hardware qualification is outside this plan. |

SD-041/044 recovery checkpoint: Video Cloud now scans persisted deployment
events not yet projected and active campaigns with reports in the current
phase. The bounded, rotating worker starts with the API runtime and does not
require another device report. Deployment sequence writes reject regression;
campaign safety writes compare the previously read campaign document before
changing it, so a delayed worker cannot undo a newer operator action. Memory
failure-injection and cursor tests pass under Go race checking. An isolated
two-pool PostgreSQL test recovers an orphaned rollback event, applies the
failure threshold, rejects stale deployment/campaign writes, and excludes an
old report after operator resume. The full Video Cloud Go suite and all seven
sequence-atlas structural checks pass; SD-044 was rendered and inspected in
Chrome. This is separate database connections, not a deployed process restart,
physical rollback, or staging acceptance. Both disposable PostgreSQL test
containers used in this checkpoint were removed.

SD-046 writer-audit checkpoint: a source scan found no additional external
Video Cloud script or tool directly changing device lifecycle or clip-upload
rows beyond the two clip-load tools now fenced above. The local Cloud Admin
E2E script updates Account Manager test mappings, not Video Cloud lifecycle
rows. Video Cloud's cloud-handoff drain uses its separate organization-wide
database mutation fence; the legacy activation-epoch migration backfill is
conditional on a still-active row with an empty epoch. This is a source audit,
not proof that every deployed operator or external database client follows the
same lock protocol.

SD-046 remote-owner checkpoint: separate PostgreSQL pools, a real command
listener, separate WebSocket hubs, and a loopback TCP WebSocket simulate two
service instances while the production activity-scoped workflow revokes an
active device. The session
repository notifies the exact instance/session before clearing the shared
route; the listener closes the idle TCP owner within the five-second test
deadline without waiting for another frame.
Three race-enabled runs passed. Unit tests prove notification failure keeps
the shared owner row retryable and delayed signals cannot close replacement
sessions. A second two-pool TCP test starts the listener after deactivation
has published its notification and removed the shared owner; reconciliation
on listener startup closes the still-idle remote WebSocket. Both variants
passed three race-enabled runs. This is not a separate-process or
deployed-network/TLS test. A separate PostgreSQL test terminates the live
listener backend and observes the same listener reconcile again after
reconnection; two race-enabled runs passed. Because PostgreSQL notifications
are not durable,
an idle remote socket can remain physically open during a prolonged listener
outage until reconnection, traffic, or transport timeout; staging MQTT broker
TCP/TLS eviction remains an acceptance gate.

SD-046 MQTT broker checkpoint: the default-off broker eviction option uses
the EMQX management API to list current-organization sessions, requires
broker-authenticated organization, scope, and subject attributes, re-reads
matching client IDs, and deletes only a still-matching device session. It
then verifies that no matching session remains; an API error or missing
attributes returns a retryable deactivation error after revocation. The
management client and MQTT adapter tests cover exact-device selection,
changed identity, missing attributes, and local/shared route cleanup when
the broker call fails. Configuration rejects absent, partial, and mixed
credentials. A disposable local EMQX test additionally proves target TCP
closure, offline persistent-session removal, and preservation of another
device and app connection in two race-enabled runs. Deployed staging TLS,
cross-instance timing, and long-running reconnect behavior remain unproven.
Device eviction now scans up to 500 pages × 500 clients within 30 seconds;
an HTTP mock with 10,001 sessions confirms the target on the final page is
found and only that session is deleted. A full-cap or timed-out scan still
fails closed. Fleet-scale latency and pagination churn remain staging gates;
the separate organization-wide handoff retains its 100-page limit.

The SD-046 default-style sequence figure now shows the targeted notification,
shared-route removal, and optional listener-reconnection reconciliation. A
companion detail figure shows the optional EMQX broker-session path. The
seven HTML atlases pass the diagram structural
and accessible-SVG self-check. A fresh browser visual pass on this revision
remains pending because the current browser policy blocks local HTML files;
no alternate browser route was used.

SD-007 cross-service checkpoint: an isolated Video Cloud PostgreSQL database
hosts the real authenticated internal activation route while a separate Account
Manager PostgreSQL database hosts its API, durable command outbox, worker, and
result inbox. A local proxy first returns HTTP 503, then fabricates a mismatched
HTTP 200 receipt. Both attempts leave the account inbox empty and the command
retryable. The third attempt reaches Video Cloud and commits activation, but
the proxy drops its response. A fourth delivery succeeds; the account operation
has one success inbox row, and the remote activation epoch remains identical
to the epoch recorded after the lost response, with one activation log. The
final test passes with race checking. The worker rejects empty, malformed,
mismatched, and oversized 200 receipts instead of projecting success.
Account activation metadata becomes `activated` without marking the device
`online`. The two test processes communicate over loopback HTTP, but this
does not prove deployed service binaries, staging credentials/TLS, live service
restart, or production retry timing. Video Cloud's receipt must reach all
serving replicas before the stricter Account Manager worker is rolled out.

SD-008 cross-service checkpoint: an isolated PostgreSQL Account Manager
integration test now launches the actual Video Cloud PostgreSQL outbox and
presence worker from the sibling checkout against the Account Manager HTTP
router and a separate PostgreSQL inbox. It passed two consecutive race-enabled
runs covering persisted unauthorized delivery retry, a matching HTTP receipt,
duplicate message replay after a simulated lost sender acknowledgement, and a
later offline projection; the Account Manager inbox retains exactly two
distinct messages. Separate sender
tests reject empty, malformed, mismatched, and oversized HTTP 200 receipts.
This does not exercise deployed service discovery, staging network/TLS,
live owner transport, or sustained load. SD-008's default-style sequence
figure now labels the matching HTTP receipt; a Chromium render found no
out-of-frame or overlapping text in that figure.

SD-049 cross-service checkpoint: a second isolated PostgreSQL pair hosts the
real Video Cloud HTTP router/fence and Account Manager admin API/claim state.
Two consecutive race-enabled runs verified the committed owner and remote
generation, stale-source and wrong-generation activation rejection, valid
target activation, denial of committed-fence cancellation, a proxy-dropped
reserve response followed by account-safe inspection and cancellation, and
remote cleanup after a definite account-transaction failure followed by a
successful retry. This
does not cover deployed network/TLS behavior, staging lock contention,
operator execution, or migration of active runtime/media.

An OCSP-like certificate-status interface is reserved in the Video Cloud
certissuer, but no provider is installed: authenticated requests return 501.
It does not alter existing certificate validation, revocation, or Pro2 test
credential loading.

Verification so far: complete Go suites pass in Account Manager and Video
Cloud; the Pro2 host network test passed. Account Manager Store and API
integration suites, including live-owner/expired event-only presence and the
SD-049 durable transfer reservation, pass against an isolated, disposable
PostgreSQL instance; the direct presence-event receiver
also passes authenticated API, replay/conflict, identity, and observation-order
tests against that database. Video Cloud's transactional outbox, legacy-row
backfill, source reconciliation, and retry-preservation tests pass against a
separate disposable PostgreSQL instance; worker envelope/retry tests
and the full Video Cloud Go suite pass. All seven HTML atlases pass the
diagram accessibility/structure self-check. All 50 figures were rendered in
Chromium and checked for SVG-frame clipping and text-to-text overlap; the
prior SD-027 and SD-046 revisions were visually spot-checked, alongside the
earlier SD-008, SD-049, and database ER atlas (including the presence outbox).
The relevant Video Cloud and Account Manager packages also pass targeted race
tests. Video Cloud's public OpenAPI drift guard passes. Without
`TEST_DATABASE_URL`, the ordinary Go run skips those database cases. These
tests do not substitute for mobile, staging, or physical-board acceptance.

SD-037 follow-up verification: the native C/C++ SDK builds with AppleClang and
all 11 native CTest cases pass, including the new multipart route and input
boundary coverage. Video Cloud's full Go suite, focused multipart race tests,
contract/OpenAPI checks, and release-bundle verification pass. A simulated
lost S3 completion response now proves retry reconciliation only when the
completed object's identity and checksum metadata match; the multipart abort
HTTP route is also tested. A real S3-compatible local MinIO server
(`RELEASE.2025-09-07T16-13-09Z`, loopback HTTP) passed two signed part PUTs,
rejection of a changed signed checksum header, adapter-recreation resume,
list/complete, whole-object readback digest, and abort. A second MinIO test
ran the `clipupload` service and verifier from session creation through `ready`
with a rebuilt service/storage adapter after part 1. A third isolated test
combines PostgreSQL and MinIO, reconnects both adapters after part 1 and the
database again before verification, then uses the actual PostgreSQL device
lifecycle lock to publish `ready`; it passes three consecutive race-enabled
runs. Each test creates and deletes its own unique bucket. This is not the
configured staging provider, an HTTPS camera PUT, a deployed API run, or
device power-loss acceptance.

SD-027 checkpoint: the full Video Cloud Go suite, targeted webhook/config/API/
PostgreSQL race tests, API drift guard, and release-bundle verification pass.
An isolated PostgreSQL run exercised tenant routing, encrypted secrets,
duplicate event IDs, retry receipts, stale generation fencing, subscription
disable, and cross-tenant receipt reads. The latest isolated PostgreSQL test
also proves built-in lifecycle state/outbox commit and rollback on enqueue
failure, and the disabled-by-default terminal-event/receipt pruning path.
An additional isolated PostgreSQL run verifies dual-key startup checks,
active/disabled subscription migration, bounded and resumable CLI rotation,
stale old-key writer detection, and new-only readability. The full Video Cloud
Go suite and release-bundle verification pass with the rotation tool included.
The opt-in MQTT persistent message subscriber now passes real isolated EMQX
QoS 1 replay tests: a failed handler closes without PUBACK and receives the
redelivery on reconnect, and a new API subscriber with the same client ID
receives an event published while its predecessor was offline. EMQX can send
queued PUBLISH before SUBACK; the client now buffers and processes that
interleaving. Focused race and full Go suites pass. This proves
broker-to-cloud retry in the tested configuration, not device-side durable
buffering or an application-level acceptance receipt by itself. A subsequent
default-off cloud-intake receipt correlates `legacy_event.event_id` on the
existing command topic after handler success and a committed
current-organization acceptance row; a brand subscription is not required.
Missing IDs, failed handlers, and missing rows do not receive `accepted`.
PostgreSQL integration tests accept matching event metadata, reject
conflicting reuse of the same ID, and fence a changed organization. The
receipt confirms cloud intake, not brand delivery. The later SD-023/028 work
added a normalized Pro2 memory-sample producer under the same opt-in gate;
board-qualified durable replay remains unverified.
Focused MQTT/PostgreSQL race tests and release-bundle verification pass for
this revision. The acceptance-boundary cases pass against an isolated
PostgreSQL instance; the disposable database and generated release bundle were
removed after verification.
All seven refreshed sequence atlases pass the diagram self-check; the latest
SD-027 and database ER rendering was visually inspected. These checks do not
prove end-to-end device buffer/replay or production deployment of
the dedicated BFF credential, an approved retention period, or staging
delivery. No retention period is assumed or active by default.
The Cloud Settings browser flow now passes mocked-BFF desktop/mobile tests, but
that is not live-service or staging acceptance.
The current SD-027 event path now holds a per-device/event PostgreSQL advisory
lock across replay check, handler, and acceptance. Two independent store pools
overlap in a disposable PostgreSQL test: the second waits on a server-side
lock, then observes the first accepted row and does not run its handler. The
final concurrent/replay/conflict case passed three race-enabled runs. The opt-in receiver still
cannot atomically roll back generic handler effects if the later intake write
fails; a retry may repeat them. The event lock uses a pool separate from the
device lifecycle lock pool to avoid exhausting connections when a handler
acquires a lifecycle lock. The SD-027 figure and contract text now show this
serialization without claiming handler/intake atomicity.
The legacy Device Hub success receipt was found to be PostgreSQL-backed in
the production API composition, not instance-local. Its old key nevertheless
used only notifier class and event ID, so another device reusing that ID could
be incorrectly suppressed. The notifier now keeps the external ID unchanged
but stores an internal organization/device/event-scoped receipt. Unit tests
and a two-pool PostgreSQL/HTTP test prove distinct devices and owners deliver
independently while an exact replay skips the second external call.
An additional two-pool failure-injection test sends the Device Hub call,
forces the cloud-intake insert to fail, then retries through the other API
pool: the shared delivery receipt suppresses a second HTTP call and intake
commits once after the constraint is removed. Both integration tests pass with
Go race checking against disposable PostgreSQL. Old unscoped rows are not
trusted for the new key, so a pending pre-upgrade event
may be re-delivered; the external HTTP call and intake commit are still not
atomic. The separate brand outbox and attempt receipts have now been migrated
locally to `(org_id,device_id,event_id)` and
`(org_id,device_id,event_id,attempt)`. An isolated PostgreSQL test preserved a
legacy receipt and allowed another device in the same organization to reuse
its event ID. A second test delivered both devices independently and returned
device-labeled receipts. Pruning one terminal event preserves the other
device's same-ID outbox and receipt. The complete isolated PostgreSQL package
suite and both Video Cloud and Cloud Admin Go suites pass; the rebuilt Cloud
Settings browser tests pass on desktop and mobile. The source-derived ER atlas
was regenerated and passed its accessible-SVG self-check. This is not
qualified as a rolling migration in staging; old API replicas must not write
through the changed key during cutover. The current staging API image predates
this dirty checkout, so no deployed acceptance is claimed.
The opt-in MQTT handler now stages brand routing metadata without committing
the outbox early. Acceptance and any subscribed brand outbox rows commit in
one PostgreSQL transaction; constraints injected independently into each
table prove that failure of either write leaves neither row. The adapter and
legacy handler also pass through this staging path against PostgreSQL, and
two real EMQX/PostgreSQL accepted-receipt round trips passed with race checks.
An absent subscription still permits intake acceptance without an outbox row.
This closes only the brand-outbox/intake split: legacy state changes, logs, and
compatibility HTTP delivery remain outside the transaction and can repeat if
the later intake write fails.
The full Video Cloud Go suite, the complete isolated PostgreSQL package suite,
the SD-027 HTML self-check and browser visual pass, and release-bundle
verification pass on this revision. These tests do not constitute deployed
staging or device power-loss acceptance.

Pro2 protocol checkpoint: the native service test covers accepted receipt
validation, wrong-device/status/empty-ID rejection, callback failure, and
publication forwarding. The official GCC 10.3.1 QEMU FreeRTOS test passes a
TLS MQTT QoS 1 event/PUBACK/accepted-receipt round trip before the existing
WebRTC/H.264 flow. The official AmebaPro2 MQTT and synthetic-video WebRTC
firmware builds pass with isolated test-only credentials; the vendor SDK
baseline is unchanged. QEMU uses a synthetic broker receipt, not the live
Video Cloud outbox; that earlier fixture did not use the later opt-in boot
producer or journal, and no physical-board power-loss acceptance was tested.
The built-in test credential/private
key loading path remains unchanged.

Receipt-delivery checkpoint: Video Cloud now publishes `event_receipt` at MQTT
QoS 1 and waits for the broker PUBACK before acknowledging the inbound event.
The MQTT adapter fails closed if a receipt publisher lacks QoS 1 support; the
focused fake-broker and adapter tests and full Go suite pass. This is broker
acceptance of the reply, not proof that a clean-session Pro2 device received
it. The vendor partition table still has no dedicated event journal region, so
no raw flash offset has been assigned and power-loss replay remains open. The
SDK's LittleFS VFS auto-formats on mount failure; it is not enabled as an event
journal without an approved recovery policy.

MQTT-to-outbox checkpoint: an external integration test now sends a legacy
event through the production MQTT adapter, message router, workflow handler,
and device notifier into a real isolated PostgreSQL outbox. It verifies that
acceptance is absent before ingress and present after commit, replay of the
same stable ID leaves exactly one row, and conflicting reuse is rejected. The
focused database and race runs and full Video Cloud Go suite pass. The MQTT
runtime's separate fake-broker test covers receipt QoS/PUBACK ordering; this
new test does not itself prove a real broker-to-database-to-device round trip.

Broker/database round-trip checkpoint: a second opt-in integration test now
uses isolated EMQX and PostgreSQL with the production MQTT runtime, adapter,
legacy workflow handler, notifier, and cloud-intake recorder. A minimal test
device publishes a QoS 1 motion event, receives the correlated QoS 1
`accepted` receipt, and acknowledges it; the test checks the matching outbox
row was committed before treating the receipt as valid. The broker/database
test passed ten consecutive runs and a focused race run. The same test now
drains that outbox row through the real webhook worker into a test-only HTTP
receiver, verifies its tenant/event fields and HMAC, and checks the persisted
`delivered` attempt receipt. This covers the Cloud handoff path in isolation;
it does not exercise public-network DNS/TLS delivery, the brand's actual
receiver, Pro2 firmware, flash persistence, power-loss recovery, or staging
deployment, so SD-027/028 remain incomplete.

Cloud-intake checkpoint: a new metadata-and-digest-only
`device_event_acceptances` table records the accepted event independently of
brand webhook registration. The runtime commits it after successful handler
processing and before publishing the QoS 1 accepted receipt; failed commits
withhold ingress PUBACK. Isolated PostgreSQL tests cover no-subscription
acceptance, exact replay, changed payload/type, ownership change, and revoked
device rejection. The real EMQX/PostgreSQL test also receives an accepted
receipt after disabling the brand subscription while finding no new brand
outbox row; three race-enabled runs pass. The generic handler and intake
record are still separate commits, and no retention period or Pro2 flash
journal is approved. Thus the receipt is a cloud-intake fact, not proof of
brand delivery or end-to-end durable device replay.

Replay-precheck checkpoint: before executing a receipt-enabled `legacy_event`,
Video Cloud now compares its current organization, event identity, type, and
exact payload digest to any committed intake row. An exact replay skips the
legacy handler and reissues the accepted receipt; a known conflict stops
before handler effects. A unit test counts one handler execution for two
identical messages and zero additional executions for a changed replay. The
isolated PostgreSQL test covers fresh, exact, changed-payload,
transferred-owner, and revoked cases in three race-enabled runs. The real
EMQX/PostgreSQL round trip verifies a committed intake row before receipt and
one handler execution after a device exact replay; three race-enabled runs
pass. This is not a transaction spanning first
handler effects and intake persistence; simultaneous first-time messages can
still race. The revised SD-027 shows the new-event branch and states those
limits in its caption.

Device replay-scheduler checkpoint: `rtk_ameba_service` now accepts an optional
oldest-event callback backed by caller-owned durable storage and a monotonic
clock. It retries the same event until the validated `accepted` receipt
callback succeeds, then checks the next queue head. A native host test covers
retry intervals across timer rollover, service recreation without receipt,
wrong-ID rejection, and advancing only after acceptance. GCC 10.3.1/newlib
4.1.0 ARM syntax validation and an isolated full `webrtc_test_video` firmware
build passed for the earlier default-off path without changing the vendor SDK
or built-in test credential path. The firmware example now has an explicit
opt-in callback, journal, and DeviceBoot producer, but normal builds keep
them off; no board-qualified queue, capacity/drop/expiry policy, OTA carryover,
or physical power-loss test exists. The vendor partition table still needs an approved
event-journal region and a mount-failure recovery policy before enabling it.

SDK telemetry-ingress checkpoint: the Video Cloud router now serves the four
SDK packages' default `/telemetry/ingest` path for device-scoped events. A
camera/device token supplies the trusted device ID; the active device record
supplies tenant/account/model/firmware identity, conflicting client fields and
app-source events are rejected, and server receive time overrides a supplied
value. Focused identity, replay/conflict, and HTTP tests pass. An opt-in
cross-repository test now drives the built JavaScript SDK against a running
Video Cloud HTTP listener, verifies trusted identity and admin query, checks
idempotent replay, and rejects a spoofed organization. A second opt-in test
drives the Android and iOS SDK packages with their production HTTP transports
against the same route; each sends and replays an RSSI sample, rejects a
spoofed organization, and is queried once with its trusted cloud identity.
Both mobile subtests pass under Go race checking.

The Native C SDK's POSIX HTTP transport now has an opt-in live-cloud test.
It builds the package with CMake, sends a typed RSSI sample and its replay,
checks HTTP 400 for a cross-organization spoof, and confirms one cloud row
with trusted identity. A combined race-enabled run of JavaScript, Android,
iOS, and Native C local interoperability tests passes. Native C is still a
host POSIX test, not a Pro2 firmware or deployed-service test.

The full Video Cloud Go suite, public OpenAPI drift guard, all seven
sequence-atlas structure checks, and a rendered visual check of revised
SD-023 pass. This is local SDK/cloud
interop, not a board-qualified Pro2 producer or live staging acceptance.

SD-023 replay-boundary correction: when PostgreSQL is configured, ordinary
product events use `product_telemetry_events(video_cloud_devid,event_id)` as
a persisted unique key. Race-enabled tests with two independent database
pools show exact same-device replay without changing the first row, same-device
changed-content conflict, and two devices accepting the same event ID. A
legacy `event_id`-only primary key and existing row are migrated in place by
`EnsureSchema`; repeated bootstrap preserves the row and admits a second
device's matching ID. Old API writers using `ON CONFLICT(event_id)` cannot
run after this migration, so staging cutover must drain them first. The
uniqueness guarantee holds only while the row is retained. The default 90-day
product-row cleanup uses `occurred_at`; intake now rejects events already
older than that cutoff before writing, including a replay after row expiry.
Without PostgreSQL, the service uses an in-memory store. With fleet analytics
enabled, `device.health.summary` goes to the hot store rather than the product
table. That route now computes the full validated product-event fingerprint;
the in-memory and Redis/Valkey hot stores use a device-plus-event-ID key,
accept an exact same-device replay, reject a changed same-device event with
HTTP 409 even if normalized severity is unchanged, and admit another device
with the same ID. The Redis receipt is scoped to this health path and expires
after 32 days. During upgrade, an old global receipt with an identical
fingerprint is promoted to the scoped key without extending its remaining TTL
or regressing newer health; same-device facts or current state can prove an
old-key conflict. If neither
exists, the old receipt alone cannot identify its source device, so a changed
older replay may be accepted once as new until legacy receipts expire. Two
shared-Redis service instances and a local Redis AOF restart with
`appendfsync always` preserve the first receipt and current health in tests;
the HTTP conflict case, focused race tests, and full Go suite pass. The
deployed `appendfsync everysec` configuration has an accepted crash-loss
window and remains unverified in staging. Ordinary PostgreSQL product rows
and health receipts use the same device-scoped ID convention but do not form
one cross-store uniqueness transaction; the same device/ID may occur once in
each store. The fleet query merge no longer drops a second device merely
because its event ID matches the first device's ID. Focused HTTP and memory
tests pass. The intake now rejects ordinary product events older than their
configured retention cutoff and hot health summaries older than 32 days before
writing.
Memory and HTTP boundary tests pass; future-dated event handling remains open.
The HTTP test fixture was corrected to connect the supplied fleet store to
the telemetry service, matching production composition; the health replay,
conflict, and expiry HTTP test now asserts the actual hot-store contents.
The SD-023 default-style sequence now shows the accepted/expired branches:
eligible events reach the selected store, while expired events return HTTP 400
without a write. All seven atlases pass the diagram structure/accessibility
check. SD-029 now describes the 32-day health-intake gate and the separate
device-scoped event-ID receipt. The revised SD-023 key label rendered in Chrome
without visible overlap; both updated sequence figures passed the accessible
SVG structure check. The other figures were not rerendered in this pass.

SD-028 stale-ACK corruption checkpoint: the paired-sector journal now rejects
a damaged newer committed record even if its magic/header no longer resembles
a journal record. If the final commit marker is damaged or an ACKed pair's
replacement append is interrupted, the ambiguous pair is quarantined rather
than erased; its recognizable sequence stays reserved and other pairs remain
usable. If neither the header nor commit marker can establish a sequence,
the journal fails closed. It reclaims an interrupted erase of
previously ACKed data only when surviving header bytes and JSON identity
still match that ACK. New magic/commit-corruption injections and the existing
partial-erase/restart host tests pass under AddressSanitizer and
UndefinedBehaviorSanitizer; a Cortex-M33 syntax check also passes with the
locally available GCC 9.3.1. That compiler is not the firmware-qualified GCC
10.3.1, and this remains host evidence, not a physical-board power-loss test.

SD-028 boot-producer checkpoint: an opt-in DeviceBoot/Ready producer now
generates a 128-bit-random event ID, appends it through the paired-sector
journal, and only then starts the MQTT replay path. The firmware example wires
the NOR adapter and service callbacks behind explicit exclusive-region build
flags; normal builds do not touch raw flash. Host journal and service tests
pass after adding the producer, including restart, full queue, wrong receipt,
and FIFO advancement. The full native build and CTest suite pass (14/14).
The new producer and the opt-in firmware translation unit pass Cortex-M33
syntax checks with the locally available GCC 9.3.1 and SDK headers; the
synthetic-credential firmware test is repeatable with
`RTK_AMEBA_SDK_ROOT=<sdk> RTK_AMEBA_ARM_CC=<compiler> python3 -m unittest discover -s tests/tools -p test_generate_amebapro2_firmware_config.py`.
An opt-in vendor CMake configure succeeds, though its legacy macOS `sed`
commands warn. The pinned GCC 10.3.1 toolchain is not present on this host,
so this revision has no full opt-in firmware build. The build
gate does not prove region exclusivity, board driver behavior, OTA retention,
or physical power-loss safety; those remain acceptance requirements.

SD-023/028 normalized MQTT checkpoint: the default-off Pro2 firmware branch
now appends a trusted-time `device.health.memory_sample` after DeviceBoot,
using the pinned SDK's ERAM heap size and free-heap counters. The journal
producer now requires an explicit low-memory free-percent threshold from the
opt-in build instead of embedding an assumed 10% product policy. The journal
accepts both envelope kinds and replays them FIFO over the same QoS 1 service;
host producer/journal/service tests pass across recreation and exact accepted
receipts. Under the existing MQTT event-receipt gate, Video Cloud checks the
product envelope metadata and `source=device`, calls `IngestForDevice` to
resolve trusted identity, persists the product event, and only then commits
its cloud-intake acceptance row before issuing the accepted receipt. Its
race-enabled MQTT/API package tests pass. A disposable local EMQX/PostgreSQL
round trip now proves the broker delivers this normalized event to Cloud, the
product row and intake acceptance are persisted before an accepted QoS 1
receipt, an exact MQTT replay leaves the first product row unchanged, and the
trusted device mapping is used without a legacy handler or brand outbox.
The broker/database test passes three race-enabled runs. The complete Video
Cloud Go suite, all 14 Ameba native tests,
and all seven diagram self-checks pass after this change. The product write
and intake row are not atomic; an exact retry after a partial commit relies
on product-event idempotency. The device still lacks physical NOR,
board-to-broker-to-Cloud, staging, and full GCC 10.3 firmware acceptance.
The HTTP SDK path is separate.
The opt-in producer now takes a caller-selected `low_memory` free-percent
threshold; CMake/build-script gates require it in 0..100, and direct firmware
syntax tests reject missing or out-of-range values. The normal build remains
off. Native CTest passes 14/14, the producer passes Cortex-M33 GCC 9.3 syntax,
and the journal/producer host test passes AddressSanitizer and
UndefinedBehaviorSanitizer. The test value of 10 is not a product decision.

Multipart-resume checkpoint: Video Cloud now has an additive direct-to-S3
multipart control API with durable parent/provider identity and immutable
part descriptors, checksum-signed per-part URLs, provider reconciliation,
validated S3 completion, explicit abort, and whole-object SHA-256 streaming
in `clipverifier`. The one-shot PUT and retired HTTP multipart form retain
their existing behavior. Focused service, API, and S3-protocol tests pass;
an isolated disposable PostgreSQL test proves part/session persistence across
repository instances, and the existing repository integration suite passes
with the new schema. The new SD-037 rendering and all seven diagram structure
checks pass. A disposable local MinIO server now passes provider and verifier
round trips, including a combined PostgreSQL/S3 restart-resume test; the
configured staging provider, Pro2/device producer, physical power-loss test,
and bucket incomplete-multipart policy proof remain open.

Staging read-only gate (2026-09-21): scoped Linode/GHCR credential checks
passed 8/8, the Account Manager-to-certissuer mTLS boundary passed, and the
existing staging API showed 2/2 Ready replicas. The current live API image is
`sha-914794fcdf6e`, whereas this working checkout is dirty at Video Cloud
`7a0aeb0889a7`; no CI-published image for these changes was selected. The
non-reset acceptance plan was inspected, but its default 10-user/100-device
data mutation was not run. Therefore staging remains NO-GO for acceptance of
this revision; the read-only observations do not verify any new flow.

OTA re-audit (2026-09-21): the canonical Product OTA contract is still proposed.
Code-backed cloud check, campaign dispatch, artifact Range delivery, event
reporting, and operator reads exist. A Pro2 check client and validated command
handler now exist. A Manifest Signature V1 serializer, verifier gate, and
generic install/rollback coordinator also exist. The firmware example now has
default-off orchestration glue, but no Product source, public-key lookup, or
physical A/B/boot binding is bundled. A consumer App API/BFF boundary and
all four Cloud Client SDKs now provide policy-constrained request and factual
progress reads. SD-039–SD-045 now show those
boundaries explicitly; their Markdown, inventory, and HTML captions agree on
Partial/TBD contract and Partial runtime. All seven revised SVGs passed
Chromium text-bound/overlap inspection and visual review, and all seven HTML
atlases passed the accessible-SVG self-check. The stale pre-redesign sequence
PDF was removed; it is recoverable from Git history.

The OTA `Report` path no longer suppresses a campaign-safety write failure.
An exact report replay now finishes a deployment projection that failed after
the event row committed, and retries a subsequent safety write. A race-enabled
test injects those two failures in order and verifies one rollback event,
one terminal deployment projection, a paused campaign, and no false firmware
version advancement. Three consecutive focused race runs and the full Video
Cloud Go suite pass. This is local cloud failure recovery, not a board rollback
test or staging acceptance. A later bounded recovery scan removed the exact
caller-replay dependency and passed isolated two-pool PostgreSQL tests; a
deployed cross-instance crash/restart remains unverified.

SD-039/040/041 Pro2 checkpoint (2026-09-21): the cloud command envelope now
includes `devid`. The Pro2 service validates the device ID and bounded
campaign/Product IDs, optionally passes the hint to a nonblocking callback,
and ignores a valid hint when no callback is configured. A separate C check
client authenticates through the existing device token provider and strictly
parses the canonical check response. The firmware example now has default-off
task and notice scheduling, but cannot run checks without an explicit Product
port; no physical download/install/report result is implied.
At that checkpoint the full Video Cloud Go suite and then-current 15/15 native
CTest cases passed; a pre-OTA SDK 9.6e syntax/link probe also passed. The later
OTA-enabled suite is now 29/29 below. This is not a qualified full OTA firmware
build, physical-board update, or staging acceptance. The test
credential/private-key loading path remains unchanged.
All seven HTML atlases pass the accessible-SVG self-check; SD-039–SD-041 were
rendered in Chrome and visually checked after their captions and gap markers
were corrected.

SD-041/043/044 device-report transport checkpoint (2026-09-21): the Pro2 C
client can submit one canonical, bearer-authenticated deployment event with a
caller-owned sequence (starting at 2), status, observed version, and fixed UTC
device timestamp. It validates bounded input and a response echo of event
identity/content, and a host test proves that a lost-response retry sends the
same request bytes. At that checkpoint its sanitizer, syntax, vendor-link, and
then-current 16-case native checks passed; the expanded current suite and
toolchain boundary are recorded below. The
client itself is stateless: the optional example task still needs a Product
report journal/transport port before it can durably produce an OTA event or
report an actual boot outcome. Cloud
acceptance or host simulation is not physical install/rollback evidence.

SD-042 signature checkpoint (2026-09-21): Product OTA Manifest Signature V1
now fixes the ordered binary preimage for every installation-critical manifest
field and accepts only a 64-byte Ed25519 signature encoded as 128 hexadecimal
characters or standard padded base64. The Pro2 candidate coordinator requires
downloaded SHA-256, scratch read-back SHA-256, and a product-supplied
`signing_key_id` trust callback before returning `VERIFIED`; this remains a
nonbootable scratch result. Focused native, sanitizer, and Cortex-M33 syntax
checks pass. No product trust-anchor backend, real scratch slot, or enabled
example Product port exists yet. Cloud finalization now verifies the canonical
signature with the configured `signing_key_id` Ed25519 public key and fails
closed for a missing/unknown key or invalid signature. The Pro2 example's existing
test credential and private-key loading path remains intentionally unchanged.

SD-041/043/044 durable-report checkpoint (2026-09-21): the Pro2 report
transport now exposes bounded validation separately from sending. A dedicated
OTA wrapper serializes the full event into the existing paired-sector journal
before delivery, derives a stable journal identity from deployment and
sequence, rejects conflicting reuse, replays the exact report after a lost
response or simulated reboot, and commits its flash ACK only after the Cloud
echo passes. Focused host tests, AddressSanitizer/UndefinedBehaviorSanitizer,
and Cortex-M33 syntax compilation pass. The example deliberately does not
claim or share a NOR region: a product-approved exclusive partition, measured
OTA port, and physical install/boot/rollback bindings remain open. The worker
implementation is covered by the following checkpoint.

SD-039/041/042 OTA-worker checkpoint (2026-09-21): a non-streaming Pro2 worker
now replays one pending durable report before checking for an update, measures
the current version/hardware/anti-rollback/time through product callbacks,
performs check, fresh grant, range download, downloaded and scratch-read-back
SHA-256, and Product OTA Manifest Signature V1 trust verification. It checks
the deployment/sequence journal identity before transfer, durably appends
sequence 2 `downloaded`, and commits the local ACK only after the Cloud echoes
the exact report. An acknowledged identity found after reboot prevents another
download but deliberately does not claim that scratch bytes were re-verified.
The new worker and its test are included in the host build, vendor source list,
and ARM syntax list. The then-current 23 native tests passed; focused
worker/candidate/journal tests pass AddressSanitizer and UndefinedBehaviorSanitizer, and the new sources
pass Cortex-M33 syntax with GCC 9.3. A later clean SDK 9.6e / GCC 10.3.1 full
link now passes. The firmware example now
has default-off worker/orchestrator scheduling but still provides no qualified
scratch slot, Product trust store, measured callbacks, or exclusive OTA-report partition; installation,
boot health, and rollback evidence remain unproven on physical hardware. Its test certificate and
private-key loading path remains intentionally unchanged.

SD-042 resumable-download checkpoint (2026-09-21): the Pro2 downloader keeps
the legacy fresh-transfer interface unchanged and adds an optional paired
`scratch_open_resume` / `scratch_checkpoint` contract. Each completed 4 KiB
range is written, synced, then atomically checkpointed with committed length
and prefix SHA-256. A later call binds the stored record to the exact assignment,
rehashes the prefix from scratch, obtains a fresh grant, and fetches only the
remaining ranges. Network interruption returns `DOWNLOAD_PENDING` and preserves
the last checkpoint; invalid identity/offset/digest, storage/checkpoint failure,
or final hash mismatch aborts the nonbootable candidate. Host tests simulate
interruption after one range, successful continuation, zero-fetch verification
of a complete scratch image, corrupted prefix, invalid offset, and failed
checkpoint while retaining legacy abort behavior. Product physical scratch and
atomic metadata remain unimplemented and unqualified; the example does not
enable these callbacks. The existing test credential/private-key path remains
unchanged.

SD-043/044 install-and-rollback checkpoint (2026-09-21): a generic Pro2
coordinator now separates portable OTA ordering from Product bootloader facts.
It requires an existing durable sequence-2 `downloaded` identity before
staging, plans the slot without writing it, persists a complete `STAGING`
intent before the first inactive-slot write, and journals sequences 3/4 before
selection and reboot. Startup reconciliation accepts only exact observed
slot/version/time evidence, emits sequence 5 `verifying`, confirms a healthy
target before sequence 6 `succeeded`, or persists/selects rollback and emits
`rolled_back` only after the previous slot/version is active. Direct bootloader
rollback, failed boot selection, failed rollback reboot, terminal append versus
intent-clear failure, corrupt state, and mismatched evidence are covered by the
new native test. A concrete two-sector boot-intent store alternates durable
generations with a last-write commit marker and uses a clear tombstone so stale
intent cannot resurrect. A NOR-only SDK 9.6e FW1/FW2 backend stages through
`ota_flash_NOR`, rechecks installed SHA-256 and the SDK image signature, and
uses the stock OTA-signature operations for selection, confirmation, and
rollback; NAND fails closed. A combined integration test cuts power after the
inactive slot is verified but before `STAGED` commits, reopens both stores, and
completes the update without another flash write. The focused and integrated
tests pass AddressSanitizer/UndefinedBehaviorSanitizer; the full native suite
is 29/29 and the complete Cortex-M33 GCC 10.3 syntax check passes. The SDK 9.6e
baseline and required GCC 10.3.1/newlib 4.1.0 toolchain are present; clean
vendor and full example image links pass. The firmware example still lacks
approved scratch/intent/report allocations, trust and health policy, an enabled
Product port. Hardware qualification is deferred outside this software plan.
The embedded test certificate and private-key path remains intentionally unchanged.

SD-039/041/043 OTA-orchestrator checkpoint (2026-09-21): the new portable
cycle entry reconciles an existing durable boot intent before any network
work. With no intent it runs the pending-report-first worker, and only a
candidate whose scratch bytes were verified during that invocation is measured
again and passed into installation. An acknowledged sequence-2 identity found
after reboot remains nonbootable. Worker and installer must share the same
report journal. The install coordinator now resumes durable pre-reboot
`STAGING`, `STAGED`, and `BOOT_SELECTED` records; initial intent creation still requires
sequence-2 evidence, while a valid existing intent can continue after an
acknowledged old journal record was legitimately reclaimed. Focused host,
AddressSanitizer/UndefinedBehaviorSanitizer, and Cortex-M33 syntax checks pass,
and the full native suite is 29/29. The orchestrator can now use the concrete
two-sector intent store and NOR SDK FW1/FW2 backend; the combined power-loss
test proves their local recovery boundary. The firmware example now supplies
default-off immediate/cadence/retry scheduling and copied one-entry notice
coalescing behind an explicit Product source. It still lacks scratch and journal
allocations, trust/measurement/health policy, and a bundled Product port.
Hardware validation is deferred outside this software plan. The existing test
credential/private-key path is unchanged.

SD-039/040/041 firmware-scheduling checkpoint (2026-09-21): the AmebaPro2
firmware example now offers a default-off Product OTA port boundary instead of
guessing flash addresses or trust data. An explicit Product source returns the
complete orchestrator graph and bounded normal/retry cadences. After trusted
time is available, an independent task immediately runs reconciliation, then
wakes on cadence/retry or a copied one-entry coalesced OTA notice. The device
poll callback never keeps borrowed MQTT strings or performs OTA work. The
vendor hook and build wrapper require an existing absolute Product source, and
enabling the branch without Product implementations fails rather than falling
back to weak defaults. All nine configuration/SDK-header syntax tests pass with
both GCC 9.3.1 and GCC 10.3.1. Clean GCC 10.3.1/newlib 4.1.0 vendor, ordinary firmware-example,
and Product-OTA-task links also pass; the latter uses a deliberately failing
link-only port and proves no runtime Product policy. The real Product port,
region map remain Product integration inputs. Physical board acceptance is
deferred outside this software plan. The embedded test
certificate and private-key path remains intentionally unchanged.

GCC 10.3.1 full-link checkpoint (2026-09-21): a previously overlooked local
Arm GNU 10.3-2021.10 installation passed the repository's exact GCC 10.3.1 and
newlib 4.1.0 gate. Clean SDK 9.6e vendor-link, ordinary firmware-example, and
Product-OTA-task builds all produced valid images. The last build used a
deliberately failing link-only Product port, retaining the task/orchestrator
symbols without inventing a flash address, trust key, measurement, or health
policy. It exposed and fixed a macOS Bash 3.2 `set -u` empty-array failure in
the build wrapper. The task-enabled firmware is 3,698,688 bytes, 495,616 bytes
below the 4 MiB FW slot limit, and adds 16,384 packaged bytes (0.445%) plus
19,104 resident ELF bytes over the task-off build. Build directories containing
ephemeral credentials are owner-only. This removes the toolchain/link gate but
does not replace a real Product port; physical-board acceptance is outside
this software plan.

OTA storage decision checkpoint (2026-09-21): the clean task-enabled image is
3,698,688 bytes, while the SDK's entire post-user-data LittleFS candidate is
638,976 bytes. The candidate is short by 3,059,712 bytes and cannot be the
complete scratch assumed by the current verified-scratch-to-inactive-slot
state machine. A new
[`ADR-0001`](../../repos/rtk_ameba_webrtc/docs/decisions/0001-amebapro2-ota-candidate-storage.md)
records the current evidence, non-negotiable recovery invariants, a separate
persistent scratch option, the larger direct-to-inactive-slot redesign, the
rejection of volatile DDR as a shortcut, and the exact Product inputs needed
to close the software decision. No address, key, or health policy was invented;
the embedded test certificate/private-key path remains unchanged.

OTA software-completion checkpoint (2026-09-21): Video Cloud now performs
trusted-key Ed25519 verification at release finalization, filters assignments
by release-required device capabilities, and invalidates already-issued
artifact URLs after release revocation. The consumer App API/BFF exposes an
ownership-authorized factual status read and a policy-constrained requeue of an
existing frozen target; JavaScript, Go, Android, and iOS SDK helpers cover both operations.
Pro2 advertises Product-configured capabilities, serializes each OTA cycle
through required enter/leave callbacks, and supplies a deterministic
minimum-window/required-signal/fatal/timeout health decision helper. Focused
Cloud, SDK, and Pro2 host tests pass. Product storage, measurement, trust,
health-signal mapping, and UI bindings remain integration inputs. The complete
Video Cloud Go suite, Go/JavaScript/Android/iOS SDK suites, all 29 native tests,
and the GCC 10.3 Cortex-M33 syntax gate pass; OpenAPI parses and all 50 sequence
flows retain accessible SVG coverage. Per Product
direction, real-board and live-environment testing are removed from this
plan's completion criteria and recorded only as deferred follow-up. The
embedded test certificate and private-key path remains intentionally unchanged.
