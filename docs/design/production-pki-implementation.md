# Production PKI implementation ledger

## Current execution scope: fresh dev PKI (2026-09-08)

The user authorizes temporary accounts/devices and resetting dev databases when
useful. Existing dev data is not a migration requirement. Staging is untouched.
This supersedes the earlier dev legacy-canary prerequisite and requests for real
operator/device-owner input. Use distinct temporary accounts and ordinary login
for simulated approval testing; MFA stays disabled and never applies to devices.

The immediate acceptance sequence is:

1. Deploy the controller and Account Manager with management mTLS and RS256 login;
   bootstrap temporary administrators and distinct approval accounts.
2. Create a fresh Cloud/Product and governed Device Root/Brand/Product hierarchy;
   verify offline Root/Brand and OpenBao Product key custody without exporting
   Product private keys. Install and acknowledge actual runtime trust.
3. Enroll a fresh device using a device-generated key, then prove direct-mTLS
   authentication and certificate renewal using the resulting hierarchy.
4. Verify revoked credentials are rejected and active sessions close, including
   MQTT with a compatible broker; exercise restart and interrupted renewal.
5. Retain repeatable dev setup and result evidence, with explicit pass/fail status.

Use schema migrations to initialize current schemas where needed; do not spend
this rehearsal reconciling old issuance histories or replacing the existing dev
fleet. A dev database reset is authorized, not mandatory. Reset only identified
dev databases and coordinate affected workloads if a reset is actually needed.
Legacy migration/device replacement remains deferred to environments that need
it; its checklist below is retained for that later rollout, not as a dev gate.
Dev test approvals demonstrate software behavior, not independent human custody.

## 2026-09-08 live revocation gate, failed consumer and terminal restart

The complete dev consumer set now passed the Product revocation gate under an
actual worker fault. Product v3 `56df0589-ae15-4fc0-b4a2-b4114c5b95eb` was revoked
through distinct simulated requester/approver/custodian accounts. Operation
`1439799a-27db-4eb5-a01c-3fbbbcb1dc9f` is **completed**. The API manifest now
contains Root/Brand/active-v4; the broker retains v3 as a terminal authority.

A controlled invalid Root-state file was installed under the worker's existing
lock after saving its exact bytes and checking the controller Root-policy digest.
The worker disconnected a previously valid v4 MQTT session **5.910373 seconds**
after corruption, at connection age **16.630997 seconds**, before lease expiry.
The independent API still authenticated v4. No new Brand CRL receipt appeared
from the faulted broker.

Brand CRL 3 is
`8aef056aec56f6c03a0e8c0d627b036ed773edd5a61f669eaba15c527e51444d`.
It preserves the prior v2 revocation and adds v3. Completion returned **409**
before publication and again after the API acknowledged that exact new CRL while
pkibroker had only its older receipt. The test's finally handler restored the
exact Root-state bytes under the lock after verifying the authoritative policy
had not changed. The worker then persisted terminal v3 denial, consumed Brand
CRL 3 and sent its own receipt. Completion returned **204** with both consumers.
V4 Device API and MQTT ACL/QoS1 roundtrip recovered successfully.

A real broker restart retained the identical terminal marker and signed v3 CRL
in `retained_crl`, with no active CRL acceptance record for v3. Root policy/pool,
image digests and the same PVC were preserved. Private state permissions were
0600 under UID 10001. V4 direct mTLS and MQTT succeeded; an unexpired predecessor
token remained denied. That predecessor was already cut off by replacement, so
its denial is a regression check, not independent attribution to CA revocation.

The obsolete v3 signer policy was removed after completed revocation. The
certissuer role now grants only the exact active v4 signer policy. The revoked
v3 provider mount and its one internal key remain retained; no private key was
exported. Controller and worker still run dev image `16aee0a5e58c9c48edd90d16f9a9c3742bd69bf609fa63a08ba58f7935f6bdf3`,
with required consumers `video-cloud-api,pkibroker`. No fault remains active.

Protected canonical dev evidence: `product-v3-revocation-fault-evidence.json`,
`product-v3-revocation-completed.json`, `product-v3-broker-terminal-state.json`,
`product-v3-terminal-restart-evidence.json`, `product-v3-policy-cleanup-evidence.json`
and updated `consumer-rollout-evidence.json`. The raw Root-state backup and
bounded phase scripts are retained privately for reproducibility work.

Current active milestone: fresh dev PKI lifecycle acceptance, estimated **96%**.
One acceptance work package remains: turn the verified phases into maintained,
repeatable dev setup/acceptance tooling and execute a complete run with explicit
pass/fail evidence. This is the existing reproducibility requirement, not a new
milestone. All five broad areas remain open: fresh dev acceptance; other trust
consumers/live sessions; backup/recovery and SDK; provider/hardware compatibility;
staging/custody/recovery qualification. Staging and legacy migration stay deferred;
MFA remains optional future human login only. No Git push, PR or remote CI ran.

## 2026-09-08 live complete broker consumer and cross-CA replacement

The clean Video Cloud `728362d` dev image was built locally for linux/amd64 and
verified in GHCR at
`sha256:16aee0a5e58c9c48edd90d16f9a9c3742bd69bf609fa63a08ba58f7935f6bdf3`.
The dev `pki-controller` and isolated `mqtt-pki` worker now run that digest.
EMQX remains `91ff2f25da30904a6d3ddf28b09787ba391a34e04271babe8bcd20adbb3dacd7`;
the API image remains `536f52d49c78d57b846fa80a683c787ea6c9a0dba7f0861e7b0f9bb0a10c81ef`.
Controller required consumers are now **video-cloud-api and pkibroker**.

The worker uses its independently generated `pkibroker` management client,
a dedicated controller NetworkPolicy and retained `mqtt-pki-trust` PVC
(UID `bfb15b95-9c66-4bb3-93ed-e0affec06192`). Existing controller client CAs,
server key, EMQX PVC and broker credentials were preserved. Worker state is owned
by UID 10001, with a private 2700 directory and 0600 JSON files; EMQX does not
mount it. The worker login still has only the verifier role, default read-only
transactions, and no controller/issuer/superuser/role-creation privileges.

Actual worker mTLS receipts now cover reviewed Root/Brand/Product bundles, their
signed CRLs and the installed Root-policy digest. Pod readiness was supplemented
by exact running-image checks, database receipt inspection, persisted-manifest
comparison and authenticated MQTT ACL/QoS1 roundtrip verification.

Fresh governed Product v4 `fab94fc3-0f2c-48ff-ab0f-7b9739bb0e61` was requested and
approved by distinct temporary humans using ordinary RS256 login, with MFA off.
OpenBao generated one internal Product key; the Brand key signed its CSR offline.
With only the API bundle receipt present, activation returned **409** and the
issuer stayed ready. After the worker installed the reviewed ready bundle and
sent its own receipt, activation returned **204**. Product v3 became retiring;
v4's initial CRL `0efa1f2900bac5b916b86ebd5cb58f0408953e982407d2fbf032ca65963ee1d3`
was imported and the API retained both v3/v4 CRL trust. The signer retains exact
v3/v4 policies during retirement; Product keys were not exported.

The existing fresh dev Device then renewed from retiring v3 to active v4 using
a new locally generated Device key. Replay returned the same result. The old
certificate could not acknowledge (403); the successor could (204, idempotent).
The worker disconnected only the predecessor **0.7944 seconds** after the ACK,
at connection age **15.960275 seconds**, well before lease expiry. The successor
remained connected for 12 further seconds before harness cleanup. Old-token
MQTT reconnect returned CONNACK 5 and old-certificate API login returned 401;
the v4 successor remained valid. The worker logged two examined, one disconnected.

A subsequent broker restart exposed kubelet fsGroup remount widening retained
files to 0660. The non-root init phase now restores the private directory and
0600 regular files on every startup. A second actual restart verified those
permissions, the same PVC and image digests, old unexpired-token MQTT rejection,
old-certificate API 401 and new v4 API/MQTT ACL/QoS1 success. Evidence is retained
in `consumer-fsgroup-before-repair.json`, `consumer-rollout-evidence.json` and
`device-2/v4-restart-evidence.json`. The scoped desired Deployment includes the
permission repair; it is not a manual one-time chmod.

Protected evidence is in canonical dev `pki/fresh-rehearsal/`:
`consumer-image-provenance.json`, `consumer-rollout-evidence.json`,
`consumer-mqtt-roundtrip.json`, `product-v4-missing-broker-gate.json`,
`product-v4-activation-gate-evidence.json`, and
`device-2/v4-replacement-evidence.json`. Current Device 2 credentials are
`v4-key.pem` and `v4-chain.pem`; the older successor files now identify the denied
v3 predecessor. Scoped manifests/operator pins are persisted; temporary phase
helpers are retained privately but are not yet a complete repeatable-run harness.

Current active milestone: fresh dev PKI lifecycle acceptance, estimated **93%**.
Remaining acceptance work is live revocation/failure/restart qualification with
the complete required-consumer set, then one reproducible full dev run with
explicit pass/fail evidence. All five broad areas remain open: fresh dev
acceptance; other consumers/live sessions; backup/recovery and SDK integration;
provider/hardware compatibility; staging/custody/recovery qualification.
Legacy migration and staging remain deferred. No Git push, PR or remote CI ran.

## 2026-09-08 broker installed bundles and first-issuer bootstrap

Video Cloud `728362d` completes the local registry worker's bundle activation
receipt implementation. Explicit `PKI_BROKER_DEVICE_BUNDLE_ACK_ENABLED=true`
uses the existing reviewed Device manifest, actual installed Root pool/policy
and prepared parent CRLs. Every accepted Device lineage must match all installed
bundle versions in the same database snapshot as identity and CRL verification.
The worker sends its independent management-mTLS bundle receipts only after a
successful full session sweep and trust revalidation.

Ready Device Root/Brand/Product authorities can be installed before activation.
The first ready Device Root can consume the removal policy without an own CRL;
ready descendants require active parents and their current CRLs. Actual activation
then makes the issuer's own CRL mandatory for Device acceptance. No lifecycle
status is synthesized, no TLS listener installation is claimed, and other
required consumers retain their activation gates. Retiring and permanently
excluded terminal branches preserve their previous behavior.

PostgreSQL 16-backed tests exercised real request/approval/import/activation,
missing consumer gates, Root bootstrap, ready-to-active transitions, missing own
CRLs, actual Device acceptance, parent-CRL changes and revoked ready Products,
restart and malformed/empty installed trust. The executable's actual sweep path
suppressed all CRL/Root/bundle receipts for failed inventories, enabled caches,
failed preparation and superseded evidence. Terminal branch/root removal tests
also passed with bundle mode enabled.

Full pki/pkitrust/pkibrokerapp/pkiturnapp suites, consumer/worker/TURN/API race
suites, focused Device verification race checks, relevant vet and diff checks
passed. This remains local evidence: live dev images and required consumers are
unchanged. No git push, PR, remote CI or staging operation occurred.

Current active milestone (fresh dev PKI lifecycle acceptance): approximately
90%, an engineering estimate rather than a measured checklist percentage. Two
immediate acceptance jobs remain: (1) deploy and qualify the complete isolated dev
consumer and required-consumer gates; (2) retain a reproducible full dev lifecycle
run including restart and failure cases. All five broad reporting areas remain
open: fresh dev acceptance; other consumers/live sessions; backup/recovery and
SDK; provider/hardware compatibility; staging/custody/recovery qualification.
Legacy migration and staging remain deferred. After every local commit, report
active milestone percentage, remaining acceptance work and the broad area list.

## 2026-09-08 broker installed Root trust and receipts

Video Cloud `9553085` adds actual Root-pool and Root-policy enforcement to the
registry Device session consumer. The worker prepares persisted monotonic Root
trust without acknowledging, verifies registered Product CA chains against that
installed pool, and compares the exact policy digest in the same read-only
repeatable-read transaction as Device identity and prepared CRLs. After successful
session enforcement it revalidates the exact installed policy/pool and sends its
own management-mTLS Root receipt. No TLS listener configuration is fabricated.

PostgreSQL-backed tests show Root revocation remains incomplete after preparation
and completes only after the sweep and real Root receipt. Removing the last Root
installs an explicit empty pool and denies all Device sessions. Tests also cover
same-key Root reissuance, unrelated policy advancement, intermediate-anchor
promotion denial, changed policy/pool between preparation and receipt, installer
failure, restart/status rollback, and worker scan/cache/CRL failures suppressing
both CRL and Root receipts. Shared immediate-sync behavior remains covered.

Full pki/pkitrust/pkibrokerapp/pkiturnapp suites passed with PostgreSQL 16, as did
consumer/worker/TURN/API race suites, focused Device trust race verification,
relevant vet and diff checks. The three optional Root settings and persistence
requirements are documented in the service runbook and broker env example.

This is local implementation evidence. Live dev images and controller required
consumers remain unchanged. The remaining implementation gate is genuine bundle
installation/activation receipts, including ready-issuer bootstrap. Then deploy
the complete consumer in isolated dev and retain the reproducible complete run.
All five broad areas remain open; staging and legacy migration remain deferred.
No git push, PR or remote CI occurred.

## 2026-09-08 broker terminal-authority handling

Video Cloud `3dc94ec` preserves permanent Device authority denial in the worker's
existing CRL state. Revoked/compromised/retired authorities and their descendants
are excluded from acceptance, while unaffected branches continue with fresh CRLs.
Retained signed evidence is separated from the active CRL field so older readers
also reject terminal state. A database status rollback, process restart or loss
of a state file within a running process cannot silently revive an observed
terminal authority. Operators must still retain the state directory across host
replacement and restore; loss of both process memory and retained state is not
qualified by this result.

PostgreSQL-backed tests exercise governed Product revocation and Brand compromise,
selective EMQX-adapter session eviction, actual management-mTLS CRL receipts, and
revocation finalization blocked until the new parent CRL is installed and
acknowledged after the sweep. Root revocation excludes its entire hierarchy but
does not substitute for a Root-policy receipt. Retired status uses an explicit
state fixture, since normal retirement forbids outstanding leaves. Tests also
cover reversed manifests, missing/cross-cloud ancestors, restart/status rollback,
missing local-file repair, corrupt state and pre-CRL terminal observations.

Full pkitrust/pkibrokerapp/pkiturnapp race suites passed with PostgreSQL 16; API
regression tests, relevant vet and diff checks passed. This remains local evidence:
no live image, required-consumer setting, staging resource, PR or remote CI changed.
Next implement actual registry bundle/Root-policy receipts and ready-issuer
bootstrap, then scoped dev gating and the reproducible complete run. The five
broad reporting areas remain open.

## 2026-09-08 broker Device CRL receipt implementation

Video Cloud `ce9b5c4` adds an optional reviewed Device CRL consumer to `pkibroker`.
It prepares and persists signed records, fences Device session verification to
those exact Root/Brand/Product digests in the identity database snapshot, and
sends its own mTLS receipts only after a complete successful sweep and prepared
state revalidation. App and Device retain separate manifests/state while sharing
management transport. No consumer acknowledgment is substituted for TLS installation.

PostgreSQL 16-backed pki/pkitrust/pkibrokerapp/pkiturnapp suites passed. Related
consumer/worker/TURN race suites, focused Device verification race tests, vet and
diff checks passed. Tests cover missing/expired/cross-issuer prepared coverage,
revoked leaf/ancestor denial, selective session eviction, scan/cache/preparation
failure, registry advancement during a scan, persisted rollback rejection after
restart, cross-domain manifests and shared-state rejection.

This is local implementation evidence; live dev images and controller required
consumers are unchanged. Bundle and Root-policy receipts, ready-issuer bootstrap
and terminal-authority manifest transitions still require integration before
this optional consumer is enabled as a required live gate. Then complete the
reproducible full dev run. See the [ordered implementation work](production-pki-fresh-dev-rehearsal.md#next-implementation-broker-device-trust-receipts).
All five broad areas remain open. Staging and legacy migration remain deferred;
no push, PR or remote CI was run.

## 2026-09-08 real MQTT replacement and restart checkpoint

The isolated dev EMQX 5.9.0 broker and session worker are deployed. Cache reset,
read-only PKI verification, real ACL/QoS1 roundtrip, a 59.260888-second lease,
reconnect, and revoked-token denial passed. A fresh Product v3/device then completed
new-key renewal: the worker disconnected the predecessor 3.112123 seconds after
successor acknowledgment, preserved the successor, and denied old-token reconnect.
A stable-node restart preserved broker identity, images/PVC, cache policy and
old/new authentication decisions. The Docker node-name override discovered during
verification is fixed in the persisted isolated deployment. See the
[fresh dev evidence](production-pki-fresh-dev-rehearsal.md#current-live-checkpoint-real-dev-mqtt-lifecycle).

Dev MQTT behavior now has measured evidence. Broker Device trust-consumer
acknowledgments/gating and a reproducible complete dev run still remain; controller
required consumers are still only `video-cloud-api`. Do not substitute session
sweep results for trust receipts. The original broker, staging and legacy fleet
remain untouched. All five broad reporting areas remain open.

## 2026-09-08 MQTT callback transport implementation

Video Cloud `9d11534` adds an optional internal mTLS listener for only the MQTT
HTTP authentication callback, with independent broker client trust and exact
client identity. It reuses the existing token/registry/CRL/ACL/lease decisions;
the Device listener's TLS policy is unchanged. Paired listener bind failure,
peer failure and cancellation stop both transports and release resources.

Full API/config race suites and vet passed. Actual TLS tests reject absent,
expired, wrong-purpose, wrong-name and same-name/untrusted Device certificates;
wrong server name and unrelated routes fail. Valid broker callbacks preserve
certificate provenance, short leases and ACLs, and deny the token after its
certificate verifier changes to revoked. These local tests do not qualify EMQX.

The workspace image builder now includes `pkibroker`. Dev-only preparation adds
independent `emqx-pki` callback-client and `mqtt-pki` server transport identities,
with protected durable files and no retained CA private key. Targeted preparation
and packaging tests pass. These are bootstrap transport identities, not evidence
of governed MQTT/Service-domain renewal. The callback is now deployed on the separate dev API with image digest
`536f52d49c78d57b846fa80a683c787ea6c9a0dba7f0861e7b0f9bb0a10c81ef`.
Live service identity isolation, broker-key denial, unrelated-route denial and
rejection of the revoked device’s still-unexpired token passed. Public Device
TLS denial and all four durable trust-file hashes are preserved. Next deploy
the isolated compatible broker and perform fresh-device MQTT acceptance.

## 2026-09-08 Product revocation and HTTP session checkpoint

Fresh dev steps 1–3 retain their recorded passes. Step 4 now has live evidence
for Product CA revocation: distinct approvals, registry denial of new device
authentication/renewal, and closure of an existing verified-mTLS WebSocket within
8.003 seconds. Finalization failed before CRL publication, then succeeded only
after the API installed and acknowledged the Brand-signed CRL number 2. Subsequent
TLS handshakes were rejected. A controlled API restart preserved the image and
all four trust-state file hashes and retained TLS denial. See the [rehearsal record](production-pki-fresh-dev-rehearsal.md)
for operation IDs, CRL digest, configuration and qualification limits.

The disposable Product v2 is revoked; Root/Brand remain active. The separate API
now enforces Root/Brand/Product CRLs and continuous root-policy synchronization.
Its completed bootstrap bundle acknowledgment setting is disabled because the
Root-only bootstrap chain conflicts with the client CRL verifier. Existing
activation receipts remain intact. No authorization or freshness check was
relaxed, and the image is unchanged.

Next: compatible MQTT broker/consumer acceptance with a fresh Product issuer and
device, then reproducible full dev lifecycle evidence. Individual Device-leaf
revocation and simultaneous Root bootstrap/client-CRL configuration are not
qualified by this test. The five broader reporting areas remain open; legacy
migration and staging remain deferred. No reset, git push, PR or CI run occurred.

## 2026-09-08 fresh device enrollment, renewal and restart checkpoint

The fresh device generated its own key, enrolled through authenticated production
admission and Product v2 signing, and authenticated to the API with direct mTLS.
Enrollment replay preserved one certificate/reservation and quantity 1 of 1.
Certissuer now uses a Kubernetes workload identity with only the Product signer
policy. `86c0417` repairs the AppRole-only startup validator and separates Device
renewal roots from service-route authority; real TLS/race tests cover matching-CN
impersonation attempts. Factory enrollment runs `1067380` in the correct dev env.

Two new-key renewals passed. `74906d7` fixes the initial response's timestamp
precision using PostgreSQL's stored value; replacement race tests use nanosecond
input. Its dev image is running with digest
`7c678e7f6d56c10c4346b0dc5ded2cfaf6ee85ec397b688853bdb7f28b679292`.
During the second renewal, certissuer and API restarted before acknowledgment.
Exact replay, retained overlap, successor acknowledgment and immediate predecessor
denial all passed. Three certificate bindings and two acknowledged replacements
remain. This is a concrete pre-acknowledgment interruption case; broader unknown
provider-outcome qualification is not implied.

Current dev acceptance steps 1–3 now have live evidence for the recorded cases.
Next are step 4 (revocation and live sessions, including compatible MQTT) and
step 5 (repeatable complete-run evidence). All five broader reporting areas remain
open. Strict OpenSSL compatibility of the existing service bootstrap certificate
and runtime database role qualification remain recorded limitations. Staging is
untouched and legacy migration stays deferred. See the
[fresh dev record](production-pki-fresh-dev-rehearsal.md).

## 2026-09-08 fresh hierarchy activation complete

Video Cloud `1067380` is running in the scoped dev controller/API deployments
with digest `548bde6883afbe0708bfe1095389b08288d86a0f6e3cf4d5bf2b012a7e133e16`.
Fresh Root, Brand and Product v2 are active. Product internal key generation,
Brand signing, import, exact provider policies and actual API acknowledgment
passed; activation without acknowledgment was denied. The failed v1 reservation
has no provider mount/key, remains in the audit record, and its unused controller
policy was removed. Required consumers remain `video-cloud-api` for this HTTP phase.

Current acceptance sequence: steps 1 and 2 passed for the dev simulation;
steps 3–5 remain (device enrollment/renewal, revocation/live sessions, repeatable
full-lifecycle evidence). Five broader reporting areas remain open. Next deploy
and verify the Product-aware certissuer/factory enrollment path and enroll a new
device with its own key. No migration, MFA, staging, PR or git push is required.
See [live evidence](production-pki-fresh-dev-rehearsal.md).

## 2026-09-08 Brand activation and Product mount repair

Brand activation passed through the governed API after actual API trust
installation. Product v1 failed before key generation because its planned mount
was nested beneath existing `pki/device/`. The design now reserves new mounts in
`pki-issuers/<domain>/<issuer_id>/v<version>`, retaining immutable existing
references. Fix and verify this provider incompatibility before the fresh Product
v2 ceremony and device enrollment. No dev migration or reset is needed.

## 2026-09-08 live API initial trust and Root activation

The dev API image built from `ad08eef` is deployed as a separate direct-mTLS
listener. Its actual runtime acknowledged the Root bundle, and the governed API
activated the Root. Continuous root-policy synchronization now uses a retained
PVC. A controlled restart preserved the exact trust-state digest and image ID;
the replacement became Ready and current-policy acknowledgment is recorded.
Fresh Cloud/Product creation also passed through authenticated administration.
See the [fresh dev rehearsal](production-pki-fresh-dev-rehearsal.md) for IDs,
digests, manifests and the precise limits of this evidence. Brand/Product CA
provisioning and first real device enrollment are next; no migration/staging
acceptance item or full dev lifecycle is declared complete.

## 2026-09-08 initial API bundle acknowledgment implementation

Video Cloud `ad08eef` adds explicit reviewed issuer manifests and startup
acknowledgment from the actual API TLS trust configuration. The runtime verifies
current registry versions, chain digests, active parents, installed roots and
runtime policy before using its own management identity to acknowledge. It adds
no roots and does not bypass governed activation. Initial static installation
can precede first Root activation; continuous root-policy synchronization is
configured after activation. Live deployment/activation remains to be verified.

Full affected pkitrust, pki, apiapp and config suites passed against disposable
PostgreSQL 16. Focused race checks cover absent trust, stale version, revoked issuer
or parent, bad chain, failed reload, intermediate-as-anchor, missing mTLS and
retry after delivery failure. Six renderer tests and vet passed.
The renderer supports explicit consumer-to-pod-name mapping for the separate dev
API listener. Workspace bootstrap now prepares a distinct server-only TLS identity
for that listener, preserving existing management/JWT/database credentials.
Server hostname/purpose, persistence and existing dev preparation race tests passed.

## 2026-09-08 fresh-dev authentication and first Root

Account Manager is deployed with the verified `63c928f` image, RS256 signing and
MFA disabled. Its schema Job and ordinary password login passed. Three simulated
dev actors received roles through authenticated administration; unassigned access,
self-approval and wrong-role approval were denied. The first Device Root is
imported and `ready`; activation remains blocked until actual API trust
installation. Video Cloud `14ff366` fixes missing controller ingress revealed by
the live proxy request. See the [fresh dev rehearsal](production-pki-fresh-dev-rehearsal.md)
for image digests, exact evidence, protected artifact locations and next steps.
No database reset, staging operation or fabricated trust acknowledgment occurred.

## 2026-09-08 fresh-dev controller deployment

Added optional `pki-dev-prepare --consumer video-cloud-api` preparation with
independent management client material, preserving existing server, Account
Manager, JWT and database keys. Local race tests verify real TLS handshakes,
repeat reuse, wrong peer/host/purpose, partial/expired material and environment
rejection; vet and diff checks passed.

Deployed `pki-controller` only in `video-cloud-dev-video-cloud` using verified
`video-cloud-api@sha256:be8d897147d3aa297a2f58a1c3f284d44c403d4ece7b1d60d099e94643f83d35`.
The running image ID matches. Live mTLS accepts Account Manager and the separately
provisioned API identity; missing client, wrong hostname and server-only client
identity are rejected. Account Manager without a human assertion and the API
calling a human endpoint both receive 403. No issuer/trust acknowledgment was
fabricated. Actual hierarchy/device acceptance remains pending. Required consumer
`video-cloud-api` scopes the initial HTTP phase; MQTT is a subsequent phase.
Persisted dev manifests and public probe results are in `controller-bootstrap/rollout`.

## Current human/device authentication policy

MFA is an optional future human user-login feature, not a current implementation
or acceptance prerequisite. It never applies to devices. See Platform PKI contract
section 6.0 and the current legacy rollout plan. Earlier MFA checks below are
historical implementation evidence; they do not reinstate a mandatory MFA gate.
Authenticated user identity, role authorization, distinct approvals, device
certificate/key-possession checks and custody controls remain required.

## 2026-09-08 dev OpenBao controller authentication

Installed a dedicated Kubernetes auth binding for the dev controller service
account with exact namespace/audience/issuer checks, short token lifetimes and
no default or issuer policies. Live authentication succeeded; wrong identity,
wrong audience and legacy signing were denied. Existing AppRole settings and
device authority were preserved. Public evidence is recorded in the
[dev preflight](production-pki-legacy-dev-preflight.md). Consumer management
identity/direct-mTLS wiring precedes the controller/trust rollout. Dev's current
EMQX 5.8.7 also requires the already-planned upgrade for session-revocation
acceptance. No fake acknowledgments or approvals were used; milestone 1 remains open.

## 2026-09-08 live dev PKI schema and runtime database access

Fixed the workspace dev image generator to include `pkicontroller`, built and
published the current service revision to the dev package, and verified its
registry digest. The dev migration and grants Jobs completed with exit 0 using
that exact digest. Twenty PKI tables now exist. The dedicated controller login
has only its reviewed group membership; live read, permission-denial and
incorrect-password checks passed. Existing workloads and OpenBao authority remain
unchanged. See the [dev preflight](production-pki-legacy-dev-preflight.md) for
backup, digest, Job and credential-binding evidence. Provider Kubernetes auth and
the coordinated rollout are next; the five device-migration acceptance items remain open.

## 2026-09-08 dev controller credential preparation

Added the dev-only `pki-dev-prepare` deployment command and
[bootstrap runbook](production-pki-dev-bootstrap.md). Prepared persistent dedicated
controller database credentials, separate Account Manager RSA signers and static
management TLS identities in the dev SecretStore. Installed and verified the
previously absent TLS/public-key Kubernetes dependencies. Existing workloads,
login signing, device CAs and staging are unchanged. See the
[dev preflight](production-pki-legacy-dev-preflight.md) for exact object evidence.
Focused race tests, actual mTLS handshake tests, CLI routing checks and vet passed.
Database login/grants, provider authorization, coordinated Account Manager rollout
and the governed canary remain on the critical path; no live acceptance item closes.

## 2026-09-08 dev database rollout rehearsal

Account Manager `63c928f` fixes the actual dev rollout blocker caused by historical
Test Lab migration filenames. Known aliases retain their timestamps and do not
replay DDL/session revocations; adoption rejects changed SQL. Full database race
tests, migration regression tests and vet passed. On restored current dev dumps,
Video Cloud migration/grants preserved all 4,131 existing rows, and Account Manager
preserved all 7,865 existing rows while adding the expected roles/markers and sealed
bootstrap state. Both migration commands passed repeat execution. See the
[dev preflight](production-pki-legacy-dev-preflight.md) for scope and evidence.
This resolves a rollout compatibility defect; live migration and all five legacy
acceptance items remain open. No staging access or live workload mutation occurred.

## 2026-09-08 optional human-login MFA alignment

Local commits: contracts `82d3220`, Account Manager `2fc7ea9`, Video Cloud
`0dca4d9`, Cloud Admin `f369c13`.

Updated the Platform PKI contract first, then aligned Account Manager, the PKI
controller, administrator recovery and console guidance. `PKI_REQUIRE_USER_MFA`
defaults to false in both services; explicit true enables the existing recent
verified human-MFA checks. Ordinary signed assertions retain `mfa=false` and
`auth_time=0`. Device and workload authentication paths do not use this setting.

Local validation passed: full controller/PKI suites against disposable PostgreSQL;
focused race checks for controller authorization, legacy/replacement/CRL paths;
Account Manager signing/API/recovery race tests, including independent approvals
with ordinary login and with optional MFA; focused console tests and Go vet.
No live environment configuration changed in this alignment. Dev qualification
and the five live legacy-migration acceptance items remain open.

## 2026-09-08 independent audit-history recovery checkpoint

Service commit `79384c6`. Full pki/pkicontrollerapp suites passed with disposable
PostgreSQL 16, as did recovery race tests, focused history/App-revocation race
tests, vet, gofmt and diff checks. The history test models deleted/conflicting
restored rows and verifies no audit writes; CLI tests cover digest and file checks.

Regression validation also exposed App revocation receipt timestamp mismatch.
The initial response now returns PostgreSQL's stored timestamp, matching the
existing server/Service-client paths and making subsequent replay identical.
The revocation test now includes sub-microsecond input precision.

The controller now exports a complete per-issuer audit snapshot and compares a
restored database against a file bound to an independently retained SHA-256 digest.
Comparison reports missing/conflicting events without writing rows or replaying
security actions. Sequence gaps are permitted; no incremental commit-order
watermark is inferred. Commands are staging-only and bounded by event/file limits.
See [controller recovery instructions](../../repos/rtk_video_cloud/docs/production-pki-controller.md).

This advances backup/recovery implementation while legacy migration remains the
active live milestone. Independent collection/retention, complete issuer coverage,
post-capture events and reconciliation of actual revocation/root-distrust effects
remain required. Matching audit rows alone cannot qualify restored security state.
The five broader milestones remain open. No staging mutation, push, PR or CI run.

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

This change implements the Platform PKI contract in dependency order. This file
records actual delivery status; unchecked items are not production capabilities.

See [remaining-work evidence audit](production-pki-remaining-audit.md) for the
current distinction between implementation gaps and live/physical qualification.
Historical continuation entries below describe their checkpoint, not current
remaining work.

## Accepted decisions

- Full production lifecycle, including bootstrap governance and recovery.
- Offline Device/App/Service Root and Brand custody; online Product keys in OpenBao.
- Explicit CA provisioning, separate from business-record creation.
- OIDC MFA step-up and distinct governance/custodian approval identities.
- Preserve staging identities through a bounded 90-day migration window.
- Recovery targets: RPO <= 15 minutes; RTO <= 4 hours, demonstrated by drills.

## Delivery checklist (implementation and outstanding qualification gates)

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

The full proposed plan is not complete. JavaScript, Go and native production
identity/lifecycle primitives and the App issuance, revocation and recovery
controls are now locally implemented as recorded below. Concrete code gaps remain
in independent gateway/server and service-domain issuance and application host
policy adoption. The [domain/host inventory](production-pki-domain-host-inventory.md)
records the current next steps. Live adoption, real custody/MFA inputs, hardware
compatibility and measured recovery qualification remain separate acceptance
gates; local fixtures do not establish those operational outcomes.

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

## Android trust-bound WebSocket owners

A closeable Android trust guard now revalidates durable CRL/chain state and cancels
its owner token on revocation, expiry or unavailable state. Checks run at the
configured interval or nearest signed expiry; cancellation is permanent and
closes its worker. The public OkHttp WebSocket factory binds that token for the
connection lifetime, handles pre-attachment cancellation, rejects late reopening
and post-close sends, and removes its handler at terminal close/failure.

Validation: full JVM suite and release/test builds passed. Guard tests cover
expiry-before-poll, failure, non-resurrection and close. Twelve API 35 tests passed,
including public-factory mTLS WebSocket establishment, HTTPS CRL revocation,
automatic cancellation and rejected later sends. The emulator was stopped after
testing. Physical suspension, root-policy replacement and other owners still
require host lifecycle qualification.

Milestone 2 advanced. Five broad milestones remain: migration/device replacement;
remaining trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware; staging/custody/recovery. Android next needs scheduled CRL
refresh and remaining owner/host wiring; physical/live qualification remains open.

## Android lifetime-owned CRL refresh loop

The Android CRL worker now fetches immediately, retries transient failures only
while durable trust is valid, and schedules before the nearest certificate/CRL
expiry. That signed deadline also bounds the next network attempt. Waiting and
I/O respond to owner cancellation. Invalid configuration fails before looping;
revocation or lost trust ends the worker. It complements the existing trust guard
and does not revive cancelled owners.

Validation: full JVM suite and release/test builds passed. Twelve API 35 tests
passed, including 503-to-revocation retry, durable update, worker termination and
concurrent mTLS WebSocket teardown, plus cancelled stalled fetch preserving state.
The emulator was stopped afterward. Physical background behavior and actual host
supervision remain unqualified.

Milestone 2 advanced; Android scheduled refresh and bound WebSocket integration
now have local runtime evidence. Five broad milestones remain: migration/device
replacement; remaining trust consumers/live sessions; backup/recovery and SDK
integration; provider/hardware; staging/custody/recovery. Remaining platform/host
owner integration and operational/physical qualification are still outstanding.

## iOS malformed test-key import handling

Replaced unchecked PKCS#8 inner indexing with bounded canonical DER parsing.
Version, supported RSA/P-256 identifiers, nonempty payload and complete envelope
consumption are required. Optional attributes are not supported. Malformed input
now throws before native import rather than risking an out-of-bounds crash.

Validation: 79 host Swift tests and the iOS simulator build passed. Regression
coverage includes every truncated prefix of short/long envelopes, oversized and
noncanonical lengths, wrong fields and trailing bytes. These are envelope parser
tests, not hardware key-import qualification.

Milestone 3 advanced; no broad milestone closed. Five remain: legacy migration/
device replacement; remaining trust consumers/live sessions; backup/recovery and
SDK integration; provider/hardware compatibility; staging/custody/recovery
qualification. Next substantive SDK gap is iOS revocation freshness and existing
owner lifecycle integration; physical and operational evidence remains open.

## iOS signed full-CRL validation and identity gate

Added bounded signed full-CRL validation across the independently anchored device
chain. Issuer CA/cRLSign, AKI/SKI, positive CRL number, freshness and signatures are
required; leaf/intermediate revocation fails closed. Partial/indirect/delta CRLs,
unknown critical extensions, duplicate entries and malformed DER are rejected.
The Keychain identity store now requires a CRL provider for installation, renewal
response validation and identity loading. Software fallback does not bypass it.
A public point-in-time validator returns the nearest signed expiry.

Validation: 80 host Swift tests and the iOS simulator build passed. Fixtures cover
RSA and ECDSA issuers, both leaf/intermediate revocation, missing/untrusted roots,
missing/duplicate CRLs, expired/tampered/truncated data, delta/scoped/critical
extensions, and missing/empty providers on installed Keychain identities.

Milestone 2 advanced. Five broad milestones remain: legacy migration/device
replacement; remaining trust consumers/live sessions; backup/recovery and SDK
integration; provider/hardware compatibility; staging/custody/recovery
qualification. iOS still needs durable CRL high-water state, bounded refresh and
existing-session owner teardown. This commit establishes point-in-time validation,
not replay prevention or lifetime session revocation. Physical/live evidence
remains outstanding.

## iOS durable CRL high-water journal

Added an explicitly created/opened device-only Keychain CRL store. Independently
anchored signed updates retain per-issuer CRL numbers and bytes, including across
rollover. Lower numbers, same-number conflicts and older thisUpdate values fail.
Revoking updates persist before identity validation rejects their subjects.
Missing/corrupt state is never implicitly recreated. App-container process/file
locks serialize updates, with Keychain write readback; bounds are 32 issuers and
32 MiB encoded state. Every provider read reauthenticates signature and freshness.

Validation: 80 host Swift tests and the iOS simulator build passed. Native Keychain
fixtures cover reopen, duplicate initialization, empty/expired/missing/corrupt
state, revocation persistence, rollback/conflict rejection and unchanged data
after failure. Physical power loss, snapshot rollback and cross-container sharing
are not qualified by these tests.

Milestone 2 advanced. Five broad milestones remain: legacy migration/device
replacement; remaining trust consumers/live sessions; backup/recovery and SDK
integration; provider/hardware compatibility; staging/custody/recovery
qualification. Next iOS work is bounded CRL refresh and owner/session teardown.

## iOS trust-bound WebSocket lifetime

Added a closeable trust guard that revalidates the independently anchored chain
and durable issuer CRLs at each poll or nearest signed expiry. Failure permanently
cancels an owner token. The public session forwards its token through WebSocketRequest;
the native URLSession factory retains the binding for the connection lifetime,
closes on cancellation, rejects post-close sends and prevents cancelled reopening.
Callback serialization prevents late open/message callbacks after terminal close.

Validation: 82 host Swift tests and the simulator build passed. A native loopback
WebSocket receives an actual frame, then a signed revoking CRL is persisted and
triggers client teardown and failed later sends/reconnection. Tests also cover
expiry before a long polling interval, validation failure, explicit/external close
and public-session token forwarding. The fixture is plaintext loopback, not live
mTLS/physical iOS qualification. Custom factories and actual host ownership still
need integration/qualification; root-policy changes require a new guard.

Milestone 2 advanced. Five broad milestones remain: legacy migration/device
replacement; remaining trust consumers/live sessions; backup/recovery and SDK
integration; provider/hardware compatibility; staging/custody/recovery
qualification. Next iOS work is bounded CRL refresh and refresh scheduling.

## iOS bounded CRL refresh

Added HTTPS-only issuer CRL download with no redirects/cookies/cache, a shared
30-second maximum network deadline, native cancellation and bounded streaming
responses. DER, PEM and controller JSON decode to signed CRLs; JSON metadata must
match signed content. All downloads validate before durable update. Revoking data
persists before refresh reports certRevoked, allowing the trust guard to close
existing owners. Missing journals are never initialized by refresh.

Validation: 82 host Swift tests and the simulator build passed. URLSession protocol
fixtures exercise valid formats, mismatched metadata, non-200/non-HTTPS rejection,
oversized streaming, timeout/cancellation and unchanged prior state after failure.
The revoking refresh test also closes a real local WebSocket. Live HTTPS and
physical background execution remain unqualified.

Milestone 2 advanced. Five broad milestones remain: legacy migration/device
replacement; remaining trust consumers/live sessions; backup/recovery and SDK
integration; provider/hardware compatibility; staging/custody/recovery
qualification. Next is periodic iOS CRL refresh bounded by signed trust expiry.

## iOS periodic CRL refresh worker

Added a blocking host-owned refresh loop with immediate fetch, bounded interval,
retry only while durable trust remains valid, signed-expiry network deadlines and
cancellation of waiting/I/O. Revocation or lost trust terminates the worker;
existing trust guards independently close owners. Missing journals are never
recreated. Hosts must supervise workers; physical background delivery remains
unqualified.

Validation: 85 host Swift tests and the simulator build passed. Integrated fixture
coverage is 503-to-revocation retry, persistence, termination and real local
WebSocket teardown. Focused tests cover expiry preventing another fetch, long-wait
cancellation, missing trust and invalid configuration. Shared downloader tests
cover timeout and cancellation of I/O.

Milestone 2 advanced. Five broad milestones remain: legacy migration/device
replacement; remaining trust consumers/live sessions; backup/recovery and SDK
integration; provider/hardware compatibility; staging/custody/recovery
qualification. The local iOS CRL validation, durable storage, download, refresh and
owner sequence is implemented. Next is a requirement-by-requirement audit of the
remaining implementation versus operational/physical qualification gates; this
commit does not claim those broader milestones are finished.

## Remaining-work evidence audit

Reconciled the original five open milestones against the current SDK source,
recovery entry points, controller restrictions and contract acceptance gates.
The audit records concrete JavaScript, Go renewal and native provider gaps and
unproven other-domain coverage separately from live/physical evidence. Corrected
the stale introductory next-work summary; no broad milestone was marked done.
See production-pki-remaining-audit.md for paths, limits and next implementation
order. This was source/document inspection, not a new runtime qualification run.

## JavaScript retry-safe P-256 key provisioning

Replaced default overwriting RSA generation with P-256 initial provisioning and
explicit legacy RSA selection. Existing keys are privately read, ownership/type/
size/permissions checked and preserved; mismatched algorithms and symlinks fail.
Concurrent creation publishes a fully fsynced key through an exclusive hard link,
then syncs the parent directory. CSR creation uses the same protected key reader.
POSIX storage is required; Windows ACL and hardware providers are not implemented.

Validation: 36 JavaScript tests passed, including twelve concurrent provisioners,
exact retry preservation, legacy RSA selection, corrupt/exposed/symlinked key
rejection, and external OpenSSL CSR signature/public-key verification. TypeScript
build passed after import cleanup. No production CA keys or deployment changed.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next JavaScript work is independently
anchored certificate installation and durable identity/renewal state. This helper
alone cannot detect a missing key belonging to an installed identity; it must not
be used as automatic identity repair.

## JavaScript independently anchored installation validation

Added a bounded closed-profile certificate chain validator using native Node
signature/issuer checks plus explicit DER profile enforcement. Device installation
requires independent roots, exact CN/P-256/clientAuth identity, current complete
path, CA usages/path lengths and private-key match. Unknown critical extensions,
unsupported constraints, malformed/trailing data and forbidden leaf SANs fail.
Legacy fixture storage now requires explicit allowLegacyTestCertificate opt-in.

Validation: 37 JavaScript tests and TypeScript build passed. Generated RSA/EC
fixtures exercise positive installation, roots/identity/key/path/expiry failures,
root path-length constraints, SAN/KU/EKU/critical-extension rejection and preservation
of installed data on rejected input. This is not a generic PKIX engine or live
revocation qualification. Writes are not yet an immutable installation journal.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next JavaScript work is durable versioned
identity installation/activation and revocation/production renewal integration.

## JavaScript immutable installation and initial activation

Validated certificate installation now canonicalizes and exclusively publishes a
fully fsynced chain, verifies the winning bytes and syncs the parent directory.
Identical concurrent retries succeed; conflicting versions never overwrite an
occupied path. Added immutable initial activation with absolute certificate/key
paths and leaf fingerprint. Restart loading rechecks trust, identity, key and
fingerprint; it never generates a missing private key. Explicit legacy fixture
storage remains separate.

Validation: 38 JavaScript tests and TypeScript build passed. Tests cover concurrent
same/conflicting publication, preserved contents, temporary-file cleanup, unsafe
file rejection, restart, mismatched identity/root, corrupt certificate, missing
key and rejection of another initial selection. Physical crash/power loss and
snapshot rollback are not qualified by these local tests.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Initial activation is not guarded renewal
activation. Next JavaScript steps remain renewal request/receipt/activation/
retirement state and revocation/transport integration.

## JavaScript durable renewal preparation

Added request-derived P-256 successor key and immutable CSR/parameter/predecessor
records. Preparation validates current active trust and key binding. Saved retries
verify exact CSR identity, public key, attributes and ECDSA signature; missing
saved keys never trigger regeneration. Concurrent requests reuse the winning CSR,
and identical publication is fsynced on conflict recovery. Predecessor selection
and key material remain intact.

Validation: 38 JavaScript tests and TypeScript build passed. Fixture coverage
includes six concurrent preparations, exact restart/retry result, distinct key,
changed TTL, tampered CSR and lost-key failure without replacement generation.
The local preparation API does not yet submit, install a renewal response, switch
active identity or acknowledge/retire the predecessor.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next JavaScript work is production renewal
transport and durable response validation before guarded activation.

## JavaScript production renewal transport and receipt

Added fresh predecessor mTLS submission of saved production renewal requests.
HTTPS-only endpoints, disabled reuse/session cache, 30-second maximum total timeout,
128-KiB response bound and AbortSignal cancellation constrain the native transport.
Responses require exact request/successor-key/leaf-chain/profile binding, issuer UUID
and live overlap before immutable receipt persistence. Saved receipts revalidate
without another network request. Active identity and predecessor are unchanged.

Validation: 38 JavaScript tests and TypeScript build passed. Local mTLS server
fixtures verify client authentication/request body, valid successor receipt,
wrong-key/redirect/oversize rejection, timeout, server-triggered cancellation after
request arrival and receipt reuse without new HTTP work. This is local mTLS evidence,
not live deployment or complete JavaScript revocation qualification.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next JavaScript work is guarded successor
installation/activation and durable acknowledgment/retirement, followed by trust
freshness and owner integration.

## JavaScript guarded successor activation

Added saved-request/CSR/key/receipt validation before immutable successor
installation and compare-and-publish activation. Each predecessor has one immutable
transition slot; competing successors cannot overwrite it. New transitions require
live overlap; already-active retries remain idempotent after overlap. Loading walks
bounded/cycle-checked history and validates the final identity rather than requiring
old keys to remain usable. A concurrent later activation is detected before return.

Validation: 38 JavaScript tests and TypeScript build passed. Tests cover missing
receipt, expired overlap, six concurrent identical activations, competing successor,
restart, expired-overlap retry, removed old key and missing successor key. This
is not hardware anti-rollback or live operational qualification.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next is durable acknowledgment-attempt and
predecessor retirement, plus session replacement orchestration and trust freshness.

## JavaScript durable acknowledgment and retirement

Added active-successor/saved-transition checks, retirement-before-network intent,
fresh successor mTLS acknowledgment and immutable success records. Uncertain
responses leave predecessor use blocked. Protected key/CSR/provisioning and mTLS
agent helpers now reject retirement markers; completed retries avoid HTTP work.
Already-loaded sessions still require host teardown, and keys remain in custody.

Validation: 38 JavaScript tests and TypeScript build passed. A local mTLS server
verifies the successor identity and pre-existing retirement marker, then drops its
first response. Tests cover persistent predecessor denial, retained key bytes,
failed rollback by removing the transition alone, successful retry and no-network
completed retry. Physical/snapshot anti-rollback and live operation remain unqualified.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next is resumable JavaScript lifecycle
orchestration with required session replacement, then trust freshness integration.

## JavaScript resumable renewal coordinator

Added saved-request/receipt resume through activation, mandatory idempotent session
replacement and successor acknowledgment. Preparation can validate its already-active
successor through the exact saved transition. Callback failure/cancellation stops
acknowledgment; records and selected successor survive. Same-process overlapping
or reentrant coordinator calls fail, with guard release on all exit paths.
Cross-process host callbacks and low-level mutations still require host serialization.

Validation: 38 JavaScript tests and TypeScript build passed. Local mTLS tests cover
resume after activation, callback failure, cancellation, nested-call rejection,
no retirement before successful callback, lost acknowledgment recovery and repeated
callback with no repeated HTTP acknowledgment after completion.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next JavaScript work is expiry-based renewal
scheduling and trust freshness/owner integration; physical/live qualification remains
separate from this local coordinator evidence.

## JavaScript durable expiry-based renewal scheduling

Added waiting/due calculation and immutable pending schedules before preparation.
Request IDs are deterministic per active identity version, and pending state
survives activation/restart. The due coordinator resumes session replacement and
acknowledgment, then verifies/removes the completed schedule with directory sync.
Failures retain it. Same-process overlapping scheduled runs fail; hosts serialize
cross-process and low-level mutations. No OS daemon or live timing is claimed.

Validation: 38 JavaScript tests and TypeScript build passed. The local mTLS lifecycle
fixture covers waiting/due transition, stable pending request, TTL conflict, pending
state through activation and callback/cancellation/lost acknowledgment, successful
cleanup and future waiting without another session replacement. Changed-data journal
removal is rejected. Injected scheduling time never bypasses real trust validation.

Milestone 3 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next is JavaScript signed CRL validation,
durable freshness state, refresh and existing-owner teardown integration.

## JavaScript signed full-CRL validation

Added bounded DER full-CRL validation over independently anchored device chains.
Issuer signature/key algorithm, DN, AKI/SKI, positive CRL number and signed freshness
are required. Leaf and intermediate revocation fail. Partial/indirect/delta CRLs,
unsupported critical extensions, duplicate entries and malformed/trailing data
are rejected. Returns the nearest signed trust expiry for future owner scheduling.

Validation: 38 JavaScript tests and TypeScript build passed. RSA/ECDSA fixtures
cover both chain revocation levels, missing/duplicate/order-independent CRLs,
truncated/tampered/trailing DER, CRL-only expiry and partial/critical extensions.
This is a validation primitive; existing lifecycle calls are not yet automatically
CRL-gated, and local tests do not prove live distribution or replay prevention.

Milestone 2 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next is durable JavaScript CRL high-water
state and automatic lifecycle gating, then refresh and owner teardown.

## JavaScript durable CRL high-water journal

Added explicitly initialized/opened local CRL state with signed current updates,
per-issuer number/thisUpdate monotonicity, retained historical issuers, 32-entry/
32-MiB bounds and revocation persistence. Updates use exclusive writer locks,
fsynced private temporary files, atomic rename, directory sync and readback.
Missing/corrupt state is never recreated. Crashed-writer locks fail closed and
require verified operator recovery; no automatic stale-lock deletion is performed.

Validation: 38 JavaScript tests and TypeScript build passed. Fixtures cover reopen,
duplicate initialization, missing/empty/corrupt data, lock rejection, signed current
reads, revocation persistence, rollback/conflicting-number rejection and unchanged
state after failure. Physical/snapshot rollback and network filesystems are not
qualified. Existing lifecycle APIs still need automatic CRL gating.

Milestone 2 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next is mandatory JavaScript lifecycle
trust-provider enforcement, followed by bounded refresh and owner teardown.

## JavaScript mandatory lifecycle CRL gate

Threaded a required CRL provider through production installation, initial/current
identity validation, prepared requests, renewal receipts, activation, acknowledgment,
coordinator and due scheduling. Native SDK validation authenticates the supplied
CRLs and checks freshness/revocation after the provider resolves. New mTLS agents
also require independent device trust/CRLs and key agreement. Explicit legacy
fixture opt-in remains separate; existing owners are not yet lifetime-revalidated.

Validation: 38 JavaScript tests and TypeScript build passed. The local mTLS lifecycle
uses a durable provider; negative coverage includes absent/empty CRLs, revoked
identity loading and agent construction, and installation without a provider.
Root-policy/key/receipt/retirement regression checks continue to pass.

Milestone 2 advanced. Five remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware;
staging/custody/recovery qualification. Next is bounded JavaScript CRL download/
refresh and existing-owner cancellation at revocation or signed expiry.

## JavaScript bounded CRL refresh

Added explicit HTTPS issuer downloads into the durable CRL journal. DER, PEM and
controller JSON envelopes are bounded and parsed; JSON digest/number/time metadata
must match the signed object. All downloads share a maximum 30-second deadline;
redirects, compression, non-200 responses, oversized bodies and cancellation fail.
Every issuer is downloaded and authenticated before atomic journal publication.
Revocation remains persistable; current identity validation rejects revoked data.

Validation: 38 JavaScript tests and TypeScript build passed, including real local
HTTPS downloads, native untrusted-server rejection, envelope mismatch, malformed
second issuer, redirect, size/encoding rejection, stalled transfer and cancellation.
Failed refreshes preserved the existing journal. No remote services were changed.

Milestone 2 advanced. Five broad milestones remain: legacy migration/device
replacement; trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware compatibility; staging/custody/recovery qualification. This is a
substep of trust consumers, not an additional milestone. Next: periodic refresh and
existing-owner cancellation at revocation or signed expiry.

## JavaScript continuous trust and owner cancellation

Added immediate periodic CRL refresh with retries bounded by durable signed trust,
cancellable waiting and I/O, and persisted revocation followed by worker termination.
Added identity trust guards with independent signed-expiry cancellation, bounded
asynchronous provider calls, terminal failure and explicit host closure. WebSocket
signals now cover the full connection lifetime; closed generations ignore late
callbacks. Production mTLS agents own guards, recheck key retirement/change, cancel
pooled and upgraded sockets, and reject reconnection after failure or destruction.

Validation: 42 JavaScript tests and TypeScript build passed. Coverage includes a
real local HTTPS 503-to-signed-revocation refresh, durable revocation, already-upgraded
native mTLS connection teardown and denied agent reuse; adapter session cancellation
and stale callbacks; expiry while a provider never resolves; parent cancellation,
late provider results and guard shutdown. No remote service or CI was invoked.

Updated the remaining-work audit to remove the now-addressed JavaScript legacy-helper
gap. Five broad milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. JavaScript host supervision,
policy replacement and deployment qualification still need evidence. Next concrete
implementation work: Go durable acknowledgment-attempt and predecessor retirement,
followed by native provider integration and domain/host coverage.

## Go durable acknowledgment and predecessor retirement

Bound saved P-256 renewals to predecessor version/fingerprint and validated saved
CSR/key material. Installation now checks the expected predecessor and persists
response/transition records before publishing current. Added durable acknowledgment
attempt and predecessor retirement before HTTP, confirmed acknowledgment afterward,
and fresh native successor mTLS with bounded transport. SDK file loaders reject
retired identities and symlink aliases; current state requires a matching transition.
Retries repeat directory syncs and completed acknowledgment avoids another HTTP call.

Validation: full Go suite and race checks passed. Native mTLS fixtures cover lost
acknowledgment response, retirement present before HTTP, conflict/cancellation before
HTTP, restart/replay, stale competing installation, overlap-expired current retry,
old-key reuse/rollback/alias denial, and missing/corrupt CSR/key/transition data.
Private POSIX parent storage and host lifecycle serialization remain required; no
physical recovery, remote services or production custody were exercised.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Milestone 3 advanced. Next: mandatory Go
renewal CRL integration and resumable session-replacement coordination, then native
provider and domain/host gaps. The audit now distinguishes those remaining gaps
from the acknowledgment/retirement state implemented here.

## Go mandatory renewal trust and resumable coordination

Added explicit independent-root/CRL policy to renewal lifecycle operations and fresh
TLS handshakes. Device chain/profile checks and signed current CRL validation gate
installation, loading, receipt reuse, request/acknowledgment and preparation from
current identities. Reused the durable CRL journal through renewal-specific update/
provider adapters. Requests persist validated receipts before returning and reuse
those receipts after activation. Added an exclusive resumable coordinator requiring
session replacement before acknowledgment; callback failures remain retryable and
completed replay recreates host owners without another acknowledgment request.

Validation: full Go suite and race checks passed. Tests include real mTLS lifecycle,
failed/nested session coordination, lost-response/restart/completed replay, signed
Device SAN rejection, missing/expired CRLs, provider snapshot isolation, revocation
blocking current/cached/prepare/ack paths, and durable journal rollback rejection.
Host providers must return promptly; owner lifetime and low-level serialization
remain host responsibilities. No deployment, remote CI or custody work was performed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Milestones 2 and 3 advanced. Next concrete
work is Go expiry-driven durable renewal scheduling, then native provider and
remaining domain/host integration. The audit reflects the implemented trust and
coordinator APIs rather than retaining their previous implementation gaps.

## Go durable expiry-driven renewal scheduling

Added due calculation and durable automatic request selection, shared locking with
manual coordination, pending validation across predecessor/successor states, and
exact-record cleanup after durable acknowledgment. Pending state survives failures,
activation and restart. Scheduling time is separate from real trust validation;
post-completion scheduling uses the real clock. Directory syncs are repeated on
retries, and lock cleanup failures are reported without suppressing operation errors.

Validation: full Go suite and race checks passed. Native mTLS lifecycle fixtures
cover waiting/due transitions, stable IDs, TTL conflict, malformed state preservation,
callback and lost-ack recovery, activated pending state, changed-record cleanup
rejection, completed replay and no-op waiting. Signed revocation remains enforced
regardless of injected scheduling time. No remote CI or deployment was used.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Milestone 3 advanced. Go's identified local
renewal scheduling gap is now implemented; real host supervision and qualification
remain. Next: native supported-provider trust integration, then domain/host coverage.

## Native OpenSSL Device trust provider

Added an optional packaged OpenSSL 3.6/cJSON verifier implementing the native bundle
trust callback for certificate-only Device P-256 identities. It validates bounded
complete JSON/schema/metadata, exact independent-root ClientAuth chain and Device
profile, full signed current issuer CRLs, and supplied EVP private-key possession.
Successful validation returns the nearest signed trust expiry. Default builds do
not acquire new dependencies; enabled package exports discover their dependencies.

Validation: baseline 11 native tests passed; enabled build passes 12 tests including
real generated RSA/P-256 crypto fixtures. Negative coverage includes wrong/missing/
public-only keys and wrong roots, leaf/intermediate revocation, scoped/tampered or
expired CRLs, signed SAN-profile rejection, duplicate/mismatched metadata, trailing
DER and all JSON truncations. ASan/UBSan provider test and installed C consumer
build/run passed on macOS arm64 with OpenSSL 3.6.3 and cJSON 1.7.19. Build commands
are recorded in the native README; no remote CI or provider custody was exercised.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Milestones 2 and 4 advanced. Native crypto
verification is now concrete; next are durable CRL high-water state and protected
identity lifecycle integration, followed by host/domain and physical qualification.

## Native durable CRL history and stored verification

Added explicit POSIX CRL journal create/open/update and a stored-verifier callback.
Signed public issuer/CRL history is retained with number/thisUpdate monotonicity and
same-number DER conflict rejection. Revoking updates persist before use is rejected.
Publication uses a private exclusive temporary inode, fsync, atomic rename, parent
sync and readback under an exclusive writer lock. Missing/corrupt state is never
recreated by update or validation. Historical expiry is isolated from current-chain
freshness. The journal does not contain private keys.

Validation: 12 native tests and ASan/UBSan provider/store tests passed. Fixtures
cover initial/reopened/empty state, retained issuer history, current shorter-chain
validation after unrelated history expires, revocation persistence, rollback and
conflict/tampering rejection with unchanged bytes, lock/permission/symlink failures,
missing/corrupt state and malformed/truncated/trailing binary records. Package exports
include the new store APIs. No remote CI, deployment or physical custody was used.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Milestone 2 advanced. Next: native protected
identity/key lifecycle and refresh/owner integration. Snapshot rollback, physical
storage/provider behavior and operational qualification remain separate gates.


## Native protected software Device keys and immutable identities

Client commit `d9e44a7` adds explicit POSIX P-256 PKCS#8 provision/reuse and load,
signed Device CSR generation, and immutable bundle installation/loading. Existing
keys undergo bounded strict parsing and private/pairwise checks. Missing recovery
keys are not generated by load; retired markers, permissive files and symlinks
fail. Installation binds the expected Device ID to the saved key, independent
roots and durable signed CRLs before exclusive private publication. Same-byte
retries preserve versions; different bytes require a new path. Owned loaded
identities expose the nearest signed trust expiry.

Validation: all 12 native tests, ASan/UBSan provider test and installed-package C
consumer build/run passed on macOS arm64 with OpenSSL 3.6.3/cJSON 1.7.19. The
fixture now issues a certificate for an SDK-generated key/CSR. Coverage includes
key reuse, writer locks, missing recovery keys, corruption/permissions/symlinks,
retirement markers, wrong identity/key, immutable conflicts and persisted revocation.
Reproduce with `cmake --build /private/tmp/rtk-native-openssl -j 4` then
`ctest --test-dir /private/tmp/rtk-native-openssl --output-on-failure`; configure
and API ownership details are in the native README. No push, remote CI or deployment.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. Native active
version selection, renewal/acknowledgment, refresh and live-session ownership remain
implementation work; physical/hardware and operational acceptance remain unqualified.


## Native durable active identity selection

Client `38694dd` adds native activation and active loading with strict version
names, expected-predecessor comparison and pinned bundle SHA-256. A private bounded
binary history prevents reactivation of previously selected names or identical
bundle bytes; retries supply the original predecessor. Activation checks the saved
successor key, Device identity, independent roots and durable CRLs before atomic
fsynced publication. Active loading revalidates policy and the pinned bundle.
Corrupt/missing recovery state never triggers initialization or key generation.

Validation: all 12 native tests, ASan/UBSan and installed C package consumer pass
on macOS arm64. Tests exercise first activation/retry, successor/retry, stale
predecessors, rollback to a selected version, bundle replacement, wrong Device and
truncated history. Reproduce with `cmake --build /private/tmp/rtk-native-openssl -j 4`
and `ctest --test-dir /private/tmp/rtk-native-openssl --output-on-failure`; native
README documents configuration, API ownership and storage limits. Also removed
local shadow-variable warnings in software key serialization cleanup.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. Native prepared
renewal binding, acknowledgment/retirement, refresh and session replacement remain
implementation work. Selection alone is not renewal authorization or proof of
physical storage, snapshot rollback resistance, hardware or operational acceptance.


## Native prepared renewal persistence

Client `f23213e` implements native renewal preparation with a durable private record
binding the successor version/request ID, Device ID, TTL, predecessor version and
predecessor bundle SHA-256 to a saved signed CSR. Initial preparation requires a
valid active predecessor and a fresh successor key. Retries check current trust and
reuse the exact saved CSR after key/signature/subject/profile verification. Missing,
retired or substituted saved keys fail without regeneration; changed parameters,
changed predecessors and malformed records fail. No request is transmitted here.

Validation: all 12 native tests, ASan/UBSan and installed-package C consumer pass on
macOS arm64/OpenSSL 3.6.3/cJSON 1.7.19. Tests cover CSR replay, changed TTL or active
predecessor, missing/substituted key, incomplete records and legal PEM whitespace.
Build and sanitizer logs have no compiler warnings. Reproduce with
`cmake --build /private/tmp/rtk-native-openssl -j 4` and
`ctest --test-dir /private/tmp/rtk-native-openssl --output-on-failure`; configuration,
ownership and storage limits are documented in the native README.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. Next native work:
validated renewal-response persistence and post-activation recovery, followed by
HTTP issuance, acknowledgment/retirement, scheduling, refresh and host session
coordination. No production custody, physical-platform or operational acceptance is
claimed by these local tests. No push, PR, remote CI or deployment was performed.


## Native renewal recovery after activation

Client `7be4f12` adds read-only prepared-request loading. It validates the saved
canonical header, CSR signature/Device profile and successor key against current
active trust. Before activation the predecessor bundle hash must match; afterward
the immediate predecessor and hash in activation history must match. Recovery no
longer requires the retired predecessor key. Missing/corrupt state and a different
active version fail without provisioning or writes.

Validation: all 12 native tests, ASan/UBSan and installed-package C consumer pass on
macOS arm64. The fixture issues a distinct successor certificate and covers request
preparation, pre-activation recovery, installation, activation, old-key retirement,
post-activation recovery, predecessor-hash corruption and expired current CRLs.
Reproduce with `cmake --build /private/tmp/rtk-native-openssl -j 4` and
`ctest --test-dir /private/tmp/rtk-native-openssl --output-on-failure`; configuration
and API ownership are recorded in the native README. No remote CI or deployment.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK recovery advanced. Next: validated
renewal response/overlap persistence, HTTP issuance and acknowledgment/retirement,
then scheduling, refresh and session-owner integration. This checkpoint does not
claim those implementations or physical/operational qualification complete.


## Native validated renewal response storage

Client `c1e1018` adds immutable response save and replay/install APIs for an
already-adapted certificate-only bundle and authenticated overlap metadata. They
validate the prepared request ID, issuer ID, Device, successor key, tenant/environment,
independent roots and CRLs before storing exact response bytes. Pre-activation
installation requires an open overlap; post-activation recovery requires the exact
selected bundle and current trust. Missing/corrupt records are not regenerated.

Validation: all 12 native tests, ASan/UBSan and installed-package C consumer pass
on macOS arm64. Coverage includes missing response, request mismatch without writes,
immutable retry/deadline conflict, pre-activation overlap expiry, install/retry and
post-activation replay after predecessor retirement, plus truncated response state.
Reproduce with `cmake --build /private/tmp/rtk-native-openssl -j 4` and
`ctest --test-dir /private/tmp/rtk-native-openssl --output-on-failure`. The native
README records API ownership, bounds and trust assumptions.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. The server PEM/JSON
wire adapter is not implemented by this checkpoint: next implement bounded native
response conversion and authenticated HTTP issuance/acknowledgment, then retirement,
scheduling, refresh and session coordination. Metadata requires authenticated transport;
certificate signatures do not independently sign request/issuer/overlap fields.
No push, PR, remote CI, deployment or physical qualification was performed.


## Native server renewal response conversion

Client `8d33454` implements strict bounded parsing of the five-field server renewal
response, UTC fractional overlap timestamps, exact leaf/chain agreement and
certificate-derived serial/fingerprint/SPKI/validity metadata. It carries the
validated Device/tenant/environment into a deterministic certificate-only bundle
and passes it through the existing request/key/root/CRL and immutable response
checks. The wire omits an issuance event timestamp; local `issuance.issued_at` uses
signed certificate NotBefore as a normalization value, not proof of issuance time.

Validation: all 12 native tests, ASan/UBSan and installed-package C consumer pass
on macOS arm64. Coverage includes conversion, save/install/activation/replay,
fractional time, duplicate fields, truncated JSON, leaf/chain disagreement and a
valid wrong-request response. Corrected the previous wrong-request fixture so it
no longer accidentally tested malformed JSON. Reproduce with
`cmake --build /private/tmp/rtk-native-openssl -j 4` and
`ctest --test-dir /private/tmp/rtk-native-openssl --output-on-failure`; native README
records configuration, API ownership, bounds and normalization semantics.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. Next: authenticated
native HTTP renewal and acknowledgment, retirement/session coordinator, scheduling
and refresh. No push, PR, remote CI, deployment or physical qualification occurred.


## Authenticated native HTTPS renewal

Client `48c600f` adds optional `RTKC_OPENSSL_HTTP` libcurl/OpenSSL transport on
POSIX. It sends the saved CSR/request/TTL over a fresh verified mTLS connection,
with separately configured server roots, active-identity checks before TLS and
persistence, no redirect/proxy/netrc/session reuse, bounded headers/body and network
timeout, cancellation and trust-expiry abort. Existing valid response state is
revalidated without another POST or installation; corrupt state does not regenerate.
Default native/provider builds remain independent of curl. Also corrected an older
Windows unsupported-status constant in the shared private-file stub.

Validation: all 12 tests pass with HTTP enabled and disabled. ASan/UBSan and an
installed C package consumer calling the HTTP symbol pass. A local HTTPS server
requires the predecessor certificate and verifies the transmitted CSR. Tests cover
malformed and valid-wrong server roots, cancellation, redirects, oversized responses,
a 50 ms timeout with elapsed-time assertion, durable save and exactly one successful
POST across retries. Fixture reverse-DNS lookup was removed after timing isolated a
36-second server-bind delay; current provider/HTTP tests complete in about two seconds.

Reproduce using the native README's `RTKC_OPENSSL_HTTP=ON` configure command, then
`cmake --build /private/tmp/rtk-native-http -j4` and
`ctest --test-dir /private/tmp/rtk-native-http --output-on-failure`. Platform evidence:
macOS arm64, local curl 8.21.0/OpenSSL 3.6.3/cJSON 1.7.19; curl was installed locally
for this build. Host callbacks/filesystem/crypto must return promptly; arbitrary
host blocking is not covered by the network timeout.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. Next native work:
authenticated acknowledgment, durable retirement/session coordinator, scheduling,
refresh and host ownership. No push, PR, remote CI, deployment or physical/custody
qualification was performed.


## Native acknowledgment, retirement and finish coordinator

Client `6346159` adds successor mTLS acknowledgment with matching durable attempt,
predecessor retirement and completion records bound to both versions and bundle
hashes. Attempt/retirement precede HTTP; confirmed 2xx response precedes completion.
Lost replies retry using the successor, and completed replay requires all matching
history without another POST or implicit reconstruction.

The finish coordinator serializes installation/activation, invokes a mandatory host
session-replacement callback, then acknowledges. Callback failure stops retirement
and HTTP; recovery repeats replacement even after completed acknowledgment. Options
and cancellation are checked before mutation. Low-level callers retain explicit
responsibility to replace owners before directly acknowledging.

Validation: 12 tests pass with HTTP enabled and disabled, plus ASan/UBSan and an
installed C consumer calling the coordinator symbol. Live HTTPS verifies the
successor certificate, retirement-before-POST, lost first acknowledgment, successful
retry and no POST on completed replay. Tests also cover coordinator activation,
callback failure/repetition, writer contention, missing retirement history and
corrupt completion. Reproduce with `cmake --build /private/tmp/rtk-native-http -j4`
and `ctest --test-dir /private/tmp/rtk-native-http --output-on-failure`; optional
configuration and API ownership are in the native README.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. Next native work:
durable renewal scheduling, CRL refresh and continuous owner trust supervision, then
verify actual application host integration. No push, PR, remote CI, deployment or
physical/custody qualification was performed.


## Native durable renewal scheduling and resume worker

Client `bc80e26` implements due-time selection and stable pending-request persistence
before key generation or HTTP. Due time uses the smaller of threshold days and one
third of certificate lifetime; zero TTL/threshold default to 90/30. An injected
decision timestamp never substitutes for the real clock in trust validation. Pending
state binds Device, TTL, predecessor and bundle hash and derives a stable request ID.

The HTTP worker holds the shared coordinator lock across scheduling, preparation,
request, activation, replacement and acknowledgment. Pending state survives activation
and callback failure. Verified completion allows only exact-byte pending deletion
and directory fsync; changed state is retained. The acknowledgment helper now supports
read-only completion verification for cleanup. No background timer is created.

Validation: 12 tests pass with HTTP enabled and disabled, plus ASan/UBSan and an
installed-package consumer calling the worker symbol. Tests cover waiting without
key/network work, stable pending state before keys, TTL conflicts/truncation, real
scheduled issuance, post-activation failure/restart, acknowledgment cleanup, changed
pending bytes during callback, completed recovery and no-op subsequent scheduling.
Reproduce with `cmake --build /private/tmp/rtk-native-http -j4` and
`ctest --test-dir /private/tmp/rtk-native-http --output-on-failure`; configuration and
ownership details remain in the native README. Compiler logs have no warnings.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. SDK integration advanced. Next: native CRL
refresh and continuous owner trust supervision, then actual host/domain integration.
The overall goal remains active. No push, PR, remote CI, deployment or physical
qualification was performed.


## Native bounded HTTPS CRL refresh

Client `55188e4` adds full-issuer public HTTPS refresh using explicit endpoints,
independent server roots, no device credentials and one bounded network deadline.
DER/PEM/strict JSON responses are bounded, normalized and checked against signed
metadata. The complete set passes bundle/key/root and monotonic journal validation
before publication; valid revocations persist before final identity rejection.
Expired signed history permits fetch/recovery. Fetch, metadata, signature or rollback
failures preserve prior state. Host inputs remain stable during the call.

Validation: all 12 tests with HTTP enabled and disabled pass, plus ASan/UBSan and an
installed C consumer referencing refresh. A separate public HTTPS fixture asserts
no client certificate/authorization/cookie. Tests cover mixed DER/PEM/JSON, expired
history recovery, signed revocation persistence, rollback rejection, bad metadata,
tampered signatures, HTTP errors, redirects and oversized data. Reproduce with
`cmake --build /private/tmp/rtk-native-http -j4` and
`ctest --test-dir /private/tmp/rtk-native-http --output-on-failure`; native README
records configuration, bounds and host authorization limits.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Trust-consumer and SDK integration advanced.
Next: native continuous owner trust supervision and host periodic refresh/wiring.
Service-peer-protected CRL endpoints need the appropriate host authorization adapter;
this public-distribution API does not claim that integration. No push, PR, remote CI,
deployment or physical/custody qualification was performed.


## Native continuous session trust guard

Client `683230c` implements a POSIX guard that returns the initially validated
identity and pins its selected version and bundle hash. Revalidation reads protected
keys, active state and durable CRLs. A separate watcher enforces signed expiry,
including while the worker performs validation outside the shared mutex. Terminal
failure/cancellation invokes a required host close callback once and cannot revive.
Same-deadline revalidation cannot extend its monotonic deadline on clock rollback.
Hosts serialize session publication/closure, cancel on root-policy replacement,
and retain callback context until exclusive destruction joins both threads.
Destruction may wait for filesystem I/O; callback must return promptly and cannot
call destroy. These ownership requirements are documented in the native API/README.

Validation on macOS arm64: all twelve HTTP-enabled and disabled tests pass,
ASan/UBSan and ThreadSanitizer provider tests pass, installed C consumer links/runs.
Socket-pair tests cover cancellation, retirement, signed revocation, selection
replacement, one-shot terminal closure and no revival after retirement restoration.
A four-second signed CRL and sixty-second recheck interval prove independent expiry
closure. These tests do not claim actual TLS host integration or injected blocking
filesystem coverage. Reproduce via the native README configuration and
`ctest --test-dir /private/tmp/rtk-native-http --output-on-failure`.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. This checkpoint advances native trust and
SDK integration. Next: periodic CRL refresh and actual host/session/domain wiring.
No push, PR, remote CI, deployment or physical/custody qualification was performed.
The overall goal remains active.


## Native guard-owned periodic CRL refresh

Client `8b177cb` adds a separately threaded, bounded public HTTPS refresh loop to a
live guard. Endpoints/server roots are copied, the matching identity is retained,
and every completion wakes durable trust revalidation. Cached trust survives failed
fetches until its signed deadline; valid updates can extend a live guard. Terminal
guards cancel network work and cannot revive. Last-attempt diagnostics are separate
from session trust. The guard's existing independent watcher remains responsible for
expiry closure even during stalled HTTP; destruction joins all workers.

Local macOS arm64 validation includes HTTP-enabled/disabled suites, ASan/UBSan,
HTTP-enabled ThreadSanitizer and an installed C consumer. HTTPS/socket tests cover
periodic retry with preserved state, signed revocation persistence and owner closure,
invalid/duplicate starts, endpoint snapshot ownership, refreshed trust surviving its
original four-second expiry, and independent expiry during an eight-second server
stall with cancellation during destruction. Tests use a sixty-second normal recheck
interval to distinguish completion wakeups/expiry from the ordinary polling loop.
Reproduce with the native README configuration and
`ctest --test-dir /private/tmp/rtk-native-http --output-on-failure`.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Native refresh/supervision primitives are
locally implemented. Next: actual application session owners and domain-specific
issuance/consumer wiring. Protected service-peer CRL distribution, Linux/physical
providers and operational qualification remain unproven. No push, PR, remote CI or
deployment was performed. The overall goal remains active.


## Native core HTTP/WebSocket guarded TLS ownership

Client `830798f` adds optional `RTKC_OPENSSL_TLS` POSIX platform hooks used directly
by the existing HTTP and WebSocket transports. The adapter owns the guarded Device
identity, verifies current protected state around each handshake, validates explicit
server roots and DNS/IP names, rejects raw plaintext/disabled verification, and
registers TLS sockets under the close callback's publication mutex. Revocation,
expiry or cancellation shuts down sockets without freeing in-use transport state.
The earliest verified server-chain expiry permanently caps the platform guard,
independently of Device CRL refresh. TLS session reuse/tickets and renegotiation are
disabled. A compiled WebSocket example wires the platform, periodic refresh and
correct cancellation/session/client/platform destruction order.

Real mTLS fixtures test core HTTP token acquisition and core WebSocket upgrade,
wrong roots, wrong-hostname certificates, raw-send/disabled-verification rejection,
Device revocation and short-lived server certificates. The server observes upgraded
socket closure before client polling/disconnect; subsequent reconnect/HTTP fails.
All twelve HTTP/provider tests, twelve HTTP-disabled tests, eleven core-only tests,
ASan/UBSan, HTTP/TLS ThreadSanitizer, example build and installed C linkage pass on
macOS arm64. The native README contains full reproducible CMake commands and API
ownership requirements. No physical/Linux or live deployment qualification is claimed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Core native transport ownership has advanced.
Remaining native transport work includes bounding the base POSIX TCP DNS/connect
phase (TLS handshake/I/O are bounded), and application/domain policy integration.
Server revocation-feed integration is separate from Device issuer CRLs. Next: close
bounded TCP ownership gaps and audit domain-specific issuance/consumer wiring.
No push, PR, remote CI or deployment was performed. The overall goal remains active.


## Native bounded DNS/TCP/TLS setup

Client `34bccfa` replaces the guarded platform's blocking base-POSIX connector
with libcurl asynchronous DNS/connect-only TCP, followed by OpenSSL mTLS under one
monotonic setup deadline. Proxy/netrc/application transfer are disabled during TCP
setup; no application bytes precede TLS. Numeric ports and pinned DNS/IP names are
validated. Both optional TLS and HTTP now require the shared async-DNS/OpenSSL-3
curl backend; TLS remains independently selectable from the renewal HTTP API.

A fault test showed threaded resolver cleanup could block after timeout. Socket/curl
cleanup now transfers to a process-lifetime worker holding no platform/guard/key or
caller-buffer references. Existing sockets shut down immediately on close. Admission
of new connections stops while sixteen setup/cleanup jobs are outstanding; closing
already-established sockets can transiently exceed that threshold while releasing
previous allocations. Live sockets do not consume admission slots. This prevents
unlimited new resolver work without waiting for an uninterruptible OS resolver on
the calling thread. Filesystem trust validation is not made interruptible.

Validation: all twelve HTTP/TLS and twelve HTTP-disabled tests pass, including a
test-only resolver interposition library that delays OS resolution three seconds,
checks 100-ms timeout and cancellation return/destruction under one second, waits for
late completion after owner destruction, and verifies rejection at admission
saturation. A transport test confirms delayed TLS does not reset the TCP deadline.
Real HTTP/WebSocket mTLS, revocation and peer-expiry tests, ASan/UBSan and
ThreadSanitizer pass. Installed C consumers link/run with and without the renewal
HTTP option. Evidence is macOS arm64 with the documented OpenSSL/curl toolchain;
Linux and other resolver/provider qualification remain. Reproduce through native
README CMake commands and `ctest --test-dir /private/tmp/rtk-native-http --output-on-failure`.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. The native bounded setup gap is addressed.
Next: domain-specific issuer policy and consumers. Read-only inspection confirms
`internal/pki/types.go` accepts App/service domains but `openbao_policy.go` still
restricts online policy to Device Product issuers; schema support alone is not domain
integration. No push, PR, remote CI, deployment or custody operation was performed.
The overall goal remains active.


## App intermediate provider checkpoint (2026-09-08)

Video Cloud `4899d2f` implements independent App intermediate provisioning/import,
canonical registry/mount checks, the client-auth App role and an exact-mount signer
ACL requiring validated App common names and forbidding SAN overrides. Private
keys remain inside OpenBao. Unsupported online domains are rejected before
provisioning consumes its claim or creates a provider key. The configured signer
now sends the common name explicitly, matching the role's refusal to use CSR names.

Validation: `GOWORK=off go test ./...` passed. Focused race tests passed for pki,
openbao, certissuer, pkicontrollerapp and certissuerapp, with the opt-in App
integration enabled against disposable loopback OpenBao 2.5.5 and PostgreSQL 16.
Coverage includes signed-CSR import, acknowledgment before activation, signing
through the restricted token, exact profile/key binding, independent Device/App
roots, rejected identity/SAN/role overrides, TTL limits and fail-before-provision
behavior for unsupported Service profiles. See the controller runbook for the
fixture command. This is local provider evidence only.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. App provision/import is now implemented;
next work is App runtime registry binding and provider response validation, followed
by remaining domain adapters and consumer inventory. No push, PR, remote CI,
deployment or custody operation was performed. The overall goal remains active.


## Configured provider response validation (2026-09-08)

Video Cloud `6522c24` validates signed CSR bytes before provider calls and verifies
returned leaf identity/key, exact SANs/EKU, non-CA profile, validity and chain against
the configured CA. Provider roots cannot add trust. Responses contain only the
verified chain; rejected responses return no certificate. This applies to configured
App/client and Gateway OpenBao signers. Existing RSA key encipherment compatibility
remains; production P-256 admission and registry authorization are separate checks.

Validation: full `GOWORK=off go test ./...` passed; certissuer/certissuerapp race
suites passed. Final targeted race tests cover signed malicious profiles, wrong
roots/keys/subjects, invalid times/PEM, and HTTP-path rejection without certificate
output or a provider call for inconsistent CSR fields. No live provider operation
was needed for this checkpoint.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: App registry-selected issuance with
active status, durable claims/replay handling, and recovery/domain consumer adapters.
No push, PR, remote CI, deployment or custody operation was performed. The full
goal remains active.


## App runtime registry binding and durable claims (2026-09-08)

Video Cloud `4532ff8` adds opt-in `CERT_ISSUER_APP_PKI_ENABLED` wiring that bypasses
static App CA/key loading, selects the registered active App intermediate and signs
only through its exact provider mount. Existing authenticated Account Manager caller
and subject checks precede the registry path. P-256 CSR proof, exact subject/no SAN,
metadata-bound request digest, environment and TTL admission are enforced.

A separate App issuance table pins one issuer and grants one signing attempt.
Concurrent retries and orphaned pending claims never obtain another token. Completion
validates against registered App lineage and commits the result/audit transactionally.
Changed requests conflict; successful replay returns the same result without signing.
Issuer/root disablement and revoked claim state block completion/replay. Consumer
revocation enforcement and operator recovery for uncertain provider outcomes remain.

Validation: full Go suite passed; race tests passed for pki, certissuer,
certissuerapp and config with disposable PostgreSQL 16 and OpenBao 2.5.5 fixtures.
Tests cover eight concurrent owners, delayed completion, pending-owner loss, wrong
token, request/metadata conflicts, retiring lineage, disablement, and HTTP replay
with one provider call. Real restricted-token provider completion/replay exposed
and corrected the prior generic validator's unnecessary requirement that a non-CA
leaf carry the optional Basic Constraints extension. CA and key-usage checks remain.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: pending App signing reconciliation,
App revocation publication/consumers and remaining domain adapters. No push, PR,
remote CI, deployment or custody operation was performed. The full goal remains active.


## App uncertain-outcome reconciliation (2026-09-08)

Video Cloud `06a11d2` adds the authenticated `reconcile-app` controller endpoint.
A fresh MFA PKI administrator supplies the original caller/request and provider
serial; the controller loads the original CSR/subject/TTL/digest from durable state.
It reads an explicitly unrevoked stored certificate through the exact provider
mount and commits through the normal App completion transaction. Recovery never
signs, resets a claim, or accepts a caller-supplied replacement CSR. Repeated recovery
and original-owner completion preserve an identical result; different certificates
conflict. Internal signing tokens cannot serialize to JSON.

Validation: full Go suite and focused pki/certissuer/pkicontrollerapp race suites
passed. PostgreSQL tests cover delayed and repeated recovery, original-owner races,
wrong caller/issuer, failed provider reads and body/environment/mTLS-bound HTTP
authorization. Real OpenBao 2.5.5 lookup/recovery/replay passed with the rendered
controller ACL; the same credential was denied signing. Disposable containers
were used, without live provider or custody operations.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Known-serial App outcome recovery is
implemented. Unknown-serial evidence discovery, App revocation publication/consumers,
provider backup adapters and remaining domains are not complete. Next: App revocation
publication and consumer enforcement. No push, PR, remote CI or deployment occurred.
The full goal remains active.


## Restricted App database-role integration (2026-09-08)

Video Cloud `07672e6` closes a prerequisite found while tracing App revocation:
prior App tests used the database owner, but deployed separated roles lacked App
claim-table grants and lineage-row locking permission. The role reconciler now
covers App claims with column-level completion/revocation privileges; verifier
reads exclude claim tokens and CSR/digest internals. The issuer can lock registered
lineage through a constant true-only `lock_marker` column without gaining issuer
identity/status/document write privileges. Rerun the owner-operated grant command
after migration; workloads still do not migrate/grant during startup.

Validation: full Go suite passed. PostgreSQL race tests exercised HTTP App issuance
and replay under the issuer group, uncertain-outcome recovery under the controller
group, repeated grant application and denial of forbidden mutations/token reads.
The existing Device factory recovery matrix, including restricted roles, passed.
The disposable PostgreSQL fixture was local; no deployed grant or schema changed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. App database-role integration is now tested;
App revocation publication/consumers remain next. No push, PR, remote CI, deployment
or custody operation occurred. The full goal remains active.


## App certificate consumer verification (2026-09-08)

Video Cloud `15e08ba` adds opt-in API App certificate authentication using registered
receipts and mandatory current signed CRLs. A repeatable-read snapshot checks the
exact P-256 client leaf, receipt, environment, independent registered App root and
intermediate, active/retiring states and both full CRLs. Leaf and intermediate
revocation, stale/missing evidence, revoked receipts and registry failures deny
access. `VIDEO_CLOUD_AUTH_APP_PKI_ENABLED` cannot fall back to legacy certificate
headers and requires direct mTLS/trust, ACL enforcement and explicit migrations.

Validation: full Go suite passed. Focused race tests prove missing/root CRL denial,
valid receipt acceptance, leaf/intermediate/receipt revocation, and CRL expiry while
the leaf remains valid. The restricted database-role integration authenticates a
registered App leaf with imported CRLs under verifier grants; the HTTP regression
rejects static/header fallback without a registry. Local PostgreSQL only was used.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. App certificate authentication now consumes
the existing signed-CRL import/publication path. Automatic provider revocation/CRL
publication and bearer-token/live-session provenance revalidation remain. Next:
App bearer-token and live-session enforcement. No push, PR, remote CI, deployment or
custody operation occurred. The full goal remains active.


## App bearer-token provenance and live validation (2026-09-08)

Video Cloud `de8d6cd` signs `app_certificate_sha256` into registry-authenticated
App tokens and checks original actor/certificate provenance at issuance, validation
and refresh. Refresh cannot replace the original fingerprint from a new connection.
The current receipt, App lineage and both mandatory CRLs are resolved in one
repeatable-read snapshot. Legacy/unbound App tokens and missing/unavailable
verifiers fail closed when App PKI is enabled. Other token scopes retain their
policies; admin-created App tokens still require App certificate provenance.

Validation: full Go suite passed. Database-backed auth/pki/apiapp/httpapi race
suites passed, including actual signed App tokens denied after leaf/intermediate/
receipt revocation and CRL expiry. Unit tests cover refresh, actor mismatch, nil
verifiers and scope isolation. The broader run exposed a stale Device HTTP fixture
that directly inserted an unapproved legacy row; it now uses registered Product
lineage while retaining its verified-peer/provenance and replacement-cutoff checks.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. App token-service checks are implemented;
App broker/media sessions that cache authorization still need their own lifetime
integration. Existing HTTP WebSocket watcher is Device-specific. Next: inventory
and wire those App live-session owners. No push, PR, remote CI, deployment or custody
operation occurred. The full goal remains active.


## App MQTT lease and session sweep (2026-09-08)

Video Cloud `9c55992` caps App PKI MQTT authentication leases to the earlier of
60 seconds or token expiry, attaching signed-token App fingerprint/actor provenance
as broker attributes. Revoked tokens cannot reconnect through the token verifier.
`PKI_BROKER_APP_PKI_ENABLED` explicitly enables App checks in the existing separate
operator sweep; App checks always require current registered receipts and both CRLs.
Rejected sessions are reread before disconnect API calls, preserving valid fresh
results. Missing-provenance App-prefixed sessions are included; the Device-only
entry point/default remains available for staged rollout.

Validation: full Go suite passed. cloudhandoff/httpapi/pkibrokerapp race tests pass,
covering lease/token bounds, App attributes, revoked-token denial, exact deletion of
revoked/expired/legacy sessions and reread preservation. Evidence uses an HTTP broker
fixture, not live EMQX. Broker cache policy, expire_at handling, disconnect timing and
scale acceptance remain unqualified. No hard end-to-end 60-second bound is claimed.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Next: App media/WebRTC lifetime enforcement.
Live broker acceptance and automatic provider revocation/CRL publication remain.
No push, PR, remote CI, deployment or custody operation occurred. The goal remains active.


## WebRTC signaling authorization provenance (2026-09-08)

Video Cloud `3d6498d` persists original authenticated creator scope/actor/App
fingerprint/token expiry in WebRTC signaling records. Session and TURN credential
issuance are capped to token expiry. App state is revalidated during create, ICE
preflight, lookup, answer and answer-wait polling. Invalid state denies access and
attempts to close the record. Explicit admin records retain their own expiry-bound
policy; unbound legacy records are denied when App PKI is enabled. Neither bearer
tokens nor untrusted header identities are stored. Memory principal values are
copied; Redis serialization preserves provenance across service reconstruction.

Validation: full Go suite passed. signaling/httpapi/apiapp race suites and the
new API-boundary race test passed. Memory and Redis protocol fixtures exercise
persistence, caller isolation, expiry caps, revocation while waiting and denied
answer/read/creation operations. Registry-read load at the existing poll interval
and actual deployment behavior remain unqualified.

Five broad milestones remain: legacy migration/device replacement; trust consumers/
live sessions; backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. Inspection confirms cloud signaling owns
records, not PeerConnections; its close method does not terminate established
peer-to-peer media. Next: peer/relay lifetime enforcement and existing TURN
allocation termination. Do not count signaling closure as media termination.
No push, PR, remote CI, deployment or custody operation occurred. Goal remains active.


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

### Authenticated Service client issuance checkpoint (2026-09-08)

Video Cloud `73fdaa2` connects the durable Service client registry to a new
`POST /v1/certificates/service-client/issue` endpoint. Only a directly verified
mTLS caller matching the dedicated provisioner pattern is admitted; trusted
forwarded identity headers cannot authorize this route. The registry remains the
authority for exact approved Service identities and pinned issuer lineage.

The request binds caller, request ID, exact subject, signed CSR DER, TTL and
metadata. Only a CN-only `service:<name>` CSR without SANs or requested extensions
can reach OpenBao. Signing uses the registry-selected Service mount and fixed
`service-client` role. Durable replay does not sign again, and changed input under
the same request ID conflicts. The endpoint enforces a 90-day Service lifetime
without lowering the shared maximum used by other certificate profiles.

Bootstrap adds an opt-in feature flag, explicit migration preflight and a separate
provisioner CN policy. Enabling it disables automatic schema mutation and requires
direct mTLS plus OpenBao configuration. Configuration and endpoint behavior are
documented in the service repository.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Remaining Service client
work is listener/consumer adoption, renewal ownership, periodic CRL operation and
restored-inventory qualification. No push, PR, remote CI, deployment or custody
action. Goal remains active.

Service commit: `73fdaa2`. Full Go tests, targeted race tests, vet, formatting and
diff checks passed. No production acceptance gate is claimed closed.

### Registry-checked Service client renewal checkpoint (2026-09-08)

Video Cloud `791a323` adds authenticated Service client self-renewal. The existing
client certificate must present its complete verified chain and pass the durable
receipt, exact identity, independent root pin, current issuer policy and signed
CRL checks before any successor claim is created. The successor CSR cannot change
the authenticated `service:<name>` subject or add SANs/extensions.

Renewal uses the same registry-pinned OpenBao `service-client` role, 90-day limit,
single signing owner and exact replay/conflict semantics as initial issuance.
Initial provisioning remains separately authorized by the dedicated provisioner.
An optional `CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256` enables self-renewal; absent
or unavailable registry/CRL evidence fails closed.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next Service work is the
host-owned private-key/credential store, automatic renewal scheduling and actual
listener/client adoption. No push, PR, remote CI, deployment or custody action.
Goal remains active.

Full Go tests, targeted race tests, vet, formatting and diff checks passed. No
production acceptance gate is claimed closed.

### Host-owned Service credential store checkpoint (2026-09-08)

Video Cloud `835d50d` implements the workload-side Service identity state boundary.
The reusable store generates P-256 keys locally and writes a single atomic `0600`
state file inside a private directory. It persists a pending request ID, key and
signed CSR before any network issuance and keeps the current credential beside it,
so a crash or uncertain response cannot silently create a new signing attempt.

Only an exact three-certificate clientAuth chain bound to the pending public key
can be promoted. Invalid chains, identities or keys retain the previous usable
credential. Restart loads and reuses the exact pending CSR, while a conflicting
request is rejected. The registry and OpenBao never receive or store the workload
private key.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. Next is issuance/renewal
client orchestration and real Service transport/listener adoption. No push, PR,
remote CI, deployment or custody action. Goal remains active.

Full Go tests, the targeted race test, vet, formatting and diff checks passed. No
production acceptance gate is claimed closed.
# Service credential trust correction (2026-09-08)

Service commit `bdd1070` corrects the host store introduced in `835d50d`.
The previous installer verified against the response's own root and reload checked
only subject/expiry. Opening now requires an independently configured root SHA-256
pin. Installation and reload both enforce the pin, ordered chain signatures,
P-256, exact CN-only clientAuth profile, validity, issuer margin and 90-day ceiling.
Invalid installation retains the current credential and pending request.

Full Go tests, targeted race tests and vet passed. Regression coverage rejects
an untrusted response root, changed reload pin, expired leaf and incomplete saved
chain. This is a prerequisite correction; host network orchestration, scheduling,
runtime receipt/CRL checks and listener adoption remain unfinished.

Five acceptance milestones remain: legacy migration/device replacement; trust
consumers/live sessions; backup/recovery and SDK integration; provider/hardware
compatibility; staging/custody/recovery qualification. No live qualification gate
is closed. Local commits only.

### Service client integration milestone completed locally (2026-09-08)

Video Cloud commit `0481e7a` completes all **5/5 work groups** in the current
factory-to-certificate-issuer Service client integration milestone:

1. **Issuance/renewal orchestration:** the host store drives authenticated initial
   issuance and same-identity renewal. Durable request/CSR state survives lost
   responses; replay does not create another signature.
2. **Scheduling/restart ownership:** one manager holds the host state lease,
   renews at two-thirds of actual validity and retries every minute. Initial
   startup failure preserves the request for supervised restart; uncertain
   provider claims require the existing reconciliation operation.
3. **Runtime integration:** factory enrollment owns `service:factory-enroll`,
   dynamically presents its installed credential through the verified Service
   HTTP owner, and closes previous connections on replacement. The certificate
   issuer owns Service listener admission and active/hijacked socket eviction.
4. **Registry/CRL lifecycle:** current receipt/profile/root/CRL checks apply during
   admission, requests and periodic sweeps. Durable installed CRLs bind exact
   acknowledgments; failed sweeps do not acknowledge. The Service controller
   worker now handles client-only and combined server/client issuer revocations.
5. **Integration/recovery checks:** real PostgreSQL and mTLS exercise lost-response
   restart, successor installation, active-stream eviction, new-handshake denial,
   exact CRL acknowledgment and denial of a restored revoked host backup. A
   read-only paginated Service client recovery inventory checks all receipts,
   pending claims, lineage, publication and current consumer evidence.

The host private key stays in its private persistent directory; the registry
contains public receipts. Configuration examples and runbook document the
dedicated initial provisioner, per-instance storage, route identity policy,
explicit schema/grants, CRL manifests and restore command. An unfinished TLS
handshake cannot block listener sweep/shutdown.

Validation passed: full Go suite with disposable PostgreSQL; affected runtime and
PKI race tests; local OpenBao 2.5.5 provisioning/signing/reconciliation/revocation
integration; vet, formatting and diff checks. The new end-to-end host test uses a
fixture OpenBao HTTP signing endpoint; the separate real-provider test validates
OpenBao behavior. Host-backup restore and registry inventory checks do not
substitute for matched production database/provider PITR or post-backup audit
reconciliation.

**Current integration milestone: 0 work groups unfinished.** Five broader
acceptance milestones remain: (1) legacy migration/device replacement,
(2) trust consumers/live sessions, (3) backup/recovery and SDK integration,
(4) provider/hardware compatibility, (5) staging/custody/recovery qualification.
This closes the concrete local factory-to-issuer slice; other service-host
adoption and live/hardware/recovery acceptance are not claimed complete.
No push, PR, remote CI, deployment or custody operation.

### Service client provider recovery checkpoint (2026-09-08)

Video Cloud `4cac76a` adds
`pkicontroller recovery-check-service-client ISSUER_ID EXPECTED_ROOT_SHA256 [SERVICE_SUBJECT LEAF_PEM]`.
With writers fenced, it checks approved restored Service lineage against an
independent root pin, then reads the separate OpenBao `service-client` role's
selected issuer and public key binding. It cannot substitute the server role.
An optional exact Service leaf must also pass its successful unrevoked receipt,
profile, policy and current signed root/intermediate CRLs. Invalid local evidence
fails before provider metadata reads. The registry transaction is read-only.

Affected-package tests with PostgreSQL, targeted race tests, vet and diff checks
passed. A disposable OpenBao 2.5.5 integration exercised the existing recovery-only
ACL and verified that signing, revocation and key generation are denied. Its
offline Service root fixture now supplies the serial metadata required by the
strict recovery validator.

This closes a provider-lineage verification item within backup/recovery work,
not an additional completed acceptance milestone. Issuer-only success proves
lineage matching, not CRL freshness. Neither this check nor registry inventory
proves signing-key usability, full provider inventory/policy equivalence or
post-backup security history. Other Service host adoption and live matched
restore/audit reconciliation remain.

Five broader acceptance milestones remain: legacy migration/device replacement;
trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware compatibility; staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody action.

### Authenticated private-server renewal API milestone (2026-09-08)

Video Cloud `aac4a46` completes the fixed **3/3** local API checklist:
(1) authenticate current-certificate ownership and current registry/CRL admission;
(2) issue an exact-CN/DNS replacement through durable claims and replay;
(3) add regression coverage, configuration and recovery guidance.

`POST /v1/certificates/gateway/renew` requires an authorized direct-mTLS
management caller plus a versioned request proof signed by the current P-256
server key. Server certificates stay serverAuth-only. The proof binds environment,
domain, independent root pin, caller, predecessor, CSR, request ID and explicit TTL.
Both predecessor verification and claimed replacement lineage enforce the pin.
The separate initial route also enforces the pin when configured. There is no
legacy fallback from renewal.

Tests with disposable PostgreSQL and a fixture OpenBao signing endpoint cover
successor key replacement, identical replay with a fresh randomized signature,
changed request conflict, uncertain provider outcomes without re-signing, invalid
caller/proof/domain/root/names, stale CRLs, expiry and revoked-predecessor replay.
Affected-package tests, targeted race tests, vet and diff checks passed. This
checkpoint did not run a real OpenBao instance or deploy a host.

**Current API milestone: 0/3 items unfinished.** The next server-host implementation
gap remains protected key/CSR state, durable scheduling and listener replacement;
the API alone does not implement those. Five broader acceptance milestones remain:
legacy migration/device replacement; trust consumers/live sessions;
backup/recovery and SDK integration; provider/hardware compatibility;
staging/custody/recovery qualification. No push, PR, remote CI or custody action.

### Managed certificate-issuer server-host milestone (2026-09-08)

Video Cloud `bbbd902` completes the fixed **4/4** local host checklist:
(1) protected server key/CSR storage; (2) durable renewal scheduling/restart retries;
(3) actual certificate-issuer listener replacement and connection eviction;
(4) integration/restore tests, configuration example and runbook.

The opt-in Service server host imports an already registered three-certificate
chain and matching key once, after current receipt/CRL verification. A private
0700 directory, atomic 0600 state and manager lease protect host-owned state.
Replacement keys/CSRs survive lost responses and restart. Renewal uses a separate
authorized management mTLS identity plus current server-key proof; server leaves
remain serverAuth-only. The worker checks on startup and every minute and renews
at two-thirds of validity. The listener dynamically selects the installed leaf
and checks current registry/CRL admission at handshake, requests and timed sweeps;
installation and trust denial evict active connections.

Full `GOWORK=off go test ./...` passed with disposable PostgreSQL. Race tests for
serviceidentity, config, certissuerapp and pkitrust, `go vet ./...`, gofmt and diff
checks passed. The PostgreSQL/mTLS integration proves pending-key replay without
a second signature after a lost response, successor certificate selection,
old-stream eviction, revocation eviction and rejection of restored predecessor
state. Provider signing used an OpenBao HTTP fixture, not a real provider run.

**Current host milestone: 0/4 work groups unfinished.** This closes certificate
issuer host adoption; it does not close other Service/MQTT/OpenBao server hosts,
dynamic root-policy migration, management credential lifecycle, matched provider/
database restore or post-backup audit reconciliation. Local ownership checks do
not themselves install or acknowledge CRLs.

Five broader acceptance milestones remain: (1) legacy migration/device replacement,
(2) trust consumers/live sessions, (3) backup/recovery and SDK integration,
(4) provider/hardware compatibility, (5) staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.



### PKI controller managed server-host milestone (2026-09-08)

Video Cloud `6d76748` completes the fixed **3/3** local adoption checklist:
(1) share the managed Service server host owner; (2) integrate controller policy,
listener and lifecycle; (3) validate compatibility/recovery behavior and document
deployment configuration.

Both certificate issuer and PKI controller now use `internal/pkitrust.ServerHost`.
The controller's isolated `PKI_HOST_` configuration selects protected per-replica
state, independent host root/name policy, separate renewal management identity and
verified remote issuer transport. Initial registered chain/key files seed state
once. Startup/shutdown own scheduling, lease and transport cleanup; the listener
uses current credentials and evicts connections on replacement or trust denial.
Static TLS remains the default; incomplete host policy fails closed. The existing
controller production-qualification gate and account-manager authorization remain.

Full Go tests passed with disposable PostgreSQL; affected-package race tests,
`go vet ./...`, formatting and diff checks passed. The shared loader regression
adds mTLS listener admission, exclusive ownership, worker shutdown, restart without
seed files and revoked-state startup denial. Existing tests retain lost-response
renewal replay and active-stream eviction coverage. Controller adapter tests verify
static mTLS compatibility and reject partial configuration. Signing uses an OpenBao
HTTP fixture; no actual controller deployment or real-provider run is claimed.

**Current controller-host milestone: 0/3 items unfinished.** Other Service hosts,
MQTT/OpenBao server-key lifecycle adoption, dynamic root migration, management
credential renewal and matched recovery-history reconciliation remain. Optional
remote CRL consumption needs an independently reachable management endpoint at
startup; local host admission does not emit CRL acknowledgments.

Five broader acceptance milestones remain: (1) legacy migration/device replacement,
(2) trust consumers/live sessions, (3) backup/recovery and SDK integration,
(4) provider/hardware compatibility, (5) staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.

### Managed EMQX host implementation (2026-09-08)

Documentation was clarified first in workspace `95bbc11`. Video Cloud
`0387086` completes the fixed **4/4** plan in
[Managed EMQX host identity](production-pki-emqx-host.md):
responsibility clarification; MQTT host identity/renewal;
local EMQX installation and process ownership; tests/deployment/runbook.

`emqxpkihost` supervises one native EMQX foreground node under a dedicated
systemd control group. The MQTT leaf private key stays in protected local state
and runtime files; the renewal origin uses independent Service trust and a
separate management credential. Durable pending renewal survives restart/lost
responses. The process owner stops before replacement, rechecks registry evidence,
and denies serving on revocation, expiry or unavailable trust. Polling is every
five seconds with five-second verification timeouts; process shutdown escalates
from TERM to KILL. This replaces the full node and causes a reconnect outage.
`pkibroker` remains an outbound session worker with no TLS listener.

Full Go tests passed with disposable PostgreSQL. Affected-package race checks,
vet, formatting and diff checks passed. Release verification passed with the
normal PKCS#11-enabled build (an earlier build without PKCS#11 was correctly
rejected by the release checker). The MQTT-domain integration uses distinct MQTT
and Service roots, lost-response replay, restart without seed files and denial of
restored revoked state. Its signing endpoint is an OpenBao HTTP fixture.

A disposable real `emqx/emqx:5.9.0` test passed for replacement TLS certificates
and established MQTT 3.1.1 session termination after replacement and simulated
trust denial. The image digest is
`sha256:c897388a3c628b684c064459a14c71b259317b044be02a586eaaab14916d755c`.
Client authentication was disabled only in that isolated lifecycle fixture.
Actual operator authentication/ACL policy, durable-session recovery, cluster
rollout, physical custody and matched restore/security-history reconciliation
remain acceptance work. No deployment or zero-downtime guarantee is claimed;
local supervision does not emit CRL installation acknowledgments.

**Current EMQX milestone: 0/4 items unfinished.** Five broader acceptance
milestones remain: (1) legacy migration/device replacement, (2) trust consumers/
live sessions, (3) backup/recovery and SDK integration, (4) provider/hardware
compatibility, (5) staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.

### Milestone 1 local cohort reporting preparation (2026-09-08)

Video Cloud `e6c17a4` adds `pkicontroller legacy-progress OPERATION_ID`.
It reports the fixed cohort from a completed governed staging import using a
read-only repeatable-read snapshot. The report validates operation/manifest scope
and digest, tracks pending signing, awaiting acknowledgment and acknowledgment,
retains missing/inconsistent entries, and reports current legacy registry
acceptance independently from replacement status. Expiry/revocation is not
counted as successful replacement. This is not global population inventory,
current successor health, installed CRL evidence or live-session proof.

The complete pki/pkicontrollerapp suites passed with disposable PostgreSQL.
Legacy migration/replacement race tests, final progress race tests, vet, gofmt
and diff checks passed. The new integration exercises approved import through
actual replacement claim/completion/acknowledgment, residual accounting,
expiry, revocation, changed/missing bindings and audit-write absence.

The active milestone is now legacy migration/device replacement, with the fixed
[five-item acceptance checklist](production-pki-legacy-rollout.md). Local reporting
preparation is complete. **All five live checklist items remain open:** cohort
inventory; approved canary import/trust deployment; measured replacement;
expanded rollout/residual accounting; legacy-trust withdrawal. The target staging
environment and cohort have been requested; no live inventory, approvals, device
replacement or consumer trust change was performed.

Five broader acceptance milestones remain: legacy migration/device replacement
(active); trust consumers/live sessions; backup/recovery and SDK integration;
provider/hardware compatibility; staging/custody/recovery qualification.
Local commits only; no push, PR, remote CI, deployment or custody operation.


### Maintained fresh-dev acceptance runner (2026-09-08)

Added the repository-owned Python runner and standard-library Go TLS/MQTT probe.
The reconciled development run passed all 12 checks through activation gates,
provider custody, enrollment, renewal/replacement, actual broker fault, revocation
receipt gates, recovery/restart and scoped cleanup. Failed reports remain explicit;
no signing/key generation is blindly replayed at guarded resumption points.
Python tests (11), Go race tests/vet and static checks passed. The active milestone
is estimated 98%, with one work package left: a fresh uninterrupted run of the
committed tool and the five-item completion audit. See the current
[fresh dev checkpoint](production-pki-fresh-dev-rehearsal.md). Five reporting areas
remain open. No staging changes, Git push, PR or remote CI were performed.
