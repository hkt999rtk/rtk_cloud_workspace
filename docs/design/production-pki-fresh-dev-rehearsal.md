# Fresh dev PKI rehearsal

## Scope

The user authorizes temporary accounts/devices and dev database resets when
useful. Existing dev issuance data is not a migration requirement. Staging is
untouched. MFA remains disabled and is optional future human-login functionality
only; devices never use it. Legacy fleet migration is deferred.

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

## Executed dev acceptance: revocation receipts and failed consumer recovery

Revoke retiring Product v3 through distinct simulated human approvals. Device 2
already uses active v4; remove v3 from the API's reviewed CRL manifest before
revocation so the API can continue verifying v4. The broker retains the v3 entry
to prove its permanent terminal exclusion behavior.

Exercise a controlled corruption of only the isolated worker's Root-state JSON.
Back up its exact bytes privately, confirm the current controller Root-policy
digest, and replace it under the existing file lock with an invalid test object.
The process must reject that state, disconnect a previously valid v4 MQTT session
before lease expiry and send no new trust receipts. The API remains available.
This does not modify authoritative policy, Root keys, CRLs or database identities.

While the worker is failing closed, execute v3 revocation and publish Brand CRL 3
with both the existing v2 revocation and the new v3 entry. The API must acknowledge
the exact new CRL; revocation completion must still fail while pkibroker has only
its older receipt. Restore the exact Root-state backup under the lock after
confirming the controller policy is unchanged; a finally handler must restore it
on test failure too. Successful worker recovery must persist terminal v3 denial,
consume and acknowledge Brand CRL 3, and release revocation completion. Validate
v4 MQTT access and restart with retained state. Remove obsolete v3 signer policy
only after revocation, retaining the provider mount/key and audit records.

This is a bounded dev fault test. Preserve the current image digests, controller
required consumers, PVCs and credentials. Retain signed CRLs and prior revocation
entries without rewriting their timestamps or reasons. It is not a Root-policy
rollback procedure or a claim of production recovery qualification.

## Earlier live checkpoint: real dev MQTT lifecycle

### Next implementation: broker Device trust receipts

The broker worker is a registry-backed session consumer. Its acknowledgment must
describe the trust state actually used for session decisions; it must never use
a synthetic TLS configuration to claim installation in EMQX's server TLS listener.
The ordered distribution work and current evidence are:

1. **Implemented locally (`ce9b5c4`).** Prepare a reviewed Device CRL manifest, persist monotonic signed records, and
   bind each session decision to those exact Root/Brand/Product digests in the
   same database snapshot as identity verification. After a complete successful
   sweep, recheck the prepared state and send CRL receipts using the worker's own
   management mTLS identity. Preparation, scan or version-check failures suppress
   receipts. App and Device manifests share transport credentials, but retain
   separate domains, state files and token verifiers.
2. **Implemented locally (`728362d`).** Registry-consumer bundle installation/receipts
   include ready-issuer activation and integrate Root-policy receipts from
   `9553085`. Bundle readiness and active leaf issuance
   are separate: requiring an active Product's own CRL before acknowledging its
   activation would create a cycle. Keep issuer status and CRL checks intact and
   test that bootstrap sequence explicitly. Terminal-authority handling is now
   implemented and tested locally in `3dc94ec`: denied lineage stays excluded
   while unaffected sessions and parent-CRL receipts progress. Carry this behavior
   into the complete consumer's live tests. CRL receipts alone do not complete
   bundle or Root-policy distribution.
3. **Passed in dev: activation, revocation, failed consumer recovery and restart.** Deploy the complete consumer on isolated dev with its own management identity,
   controller CA/network access and required-consumer configuration. Demonstrate
   missing/wrong/stale receipts blocking activation and revocation finalization,
   and actual installed receipts permitting the intended transitions.
4. **Pending.** Retain a reproducible full dev lifecycle run, including restart and failure
   cases, before closing this milestone. Legacy migration and staging stay deferred.

The following live checkpoint predates this implementation; no new consumer gate
is claimed until the scoped rollout and its acceptance evidence are recorded.

#### Terminal-authority handling

Implemented locally in Video Cloud `3dc94ec`, with PostgreSQL-backed lifecycle,
management-mTLS receipt, session-sweep and restart tests. Live rollout remains
pending scoped deployment and acceptance.

The Device consumer retains a permanent denial marker in the authority's
existing persisted CRL state after observing a matching registry authority in
`revoked`, `compromised` or `retired` status. This marker identifies the issuer and
its pinned certificate, preserves any previously cached signed CRL under
`retained_crl`, and clears the active `crl` field so older readers fail closed.
It never acts as a CRL or a controller acknowledgment. The same file lock and durable
atomic replacement protect both CRL advancement and terminal denial.

A reviewed Device manifest must include each listed authority's ancestors. A
terminal authority and its descendants are excluded from prepared acceptance
coverage, so their sessions remain denied even after a registry rollback or
worker restart. Other branches continue using their own fresh signed CRLs. The
worker acknowledges only the surviving authorities after successful session
enforcement; it never acknowledges a terminal issuer's old CRL. A terminal-state
change between preparation and acknowledgment invalidates that sweep's receipts
and requires another sweep. Corrupt/mismatched state remains an error.

This handles session exclusion and parent-CRL progress. It does not replace the
Root-policy or ready-issuer bundle receipts supplied by the complete consumer.

#### Registry worker Root trust

Implemented locally in Video Cloud `9553085`, with PostgreSQL-backed Root-removal
finalization, actual management-mTLS receipt, session enforcement and restart tests.
Bundle receipts are integrated locally in `728362d`; live deployment remains pending.

The Device worker loads independently provisioned Root certificates and a
persisted monotonic Root-distrust policy. It verifies each registered Product
CA chain against that actual pool, together with the prepared CRLs and identity
checks in one database snapshot. Policy version changes fail closed until the
worker prepares the new state; removed keys are rejected even if reissued under
another certificate or if the registry is restored to an older status.

Root synchronization must support prepare-without-acknowledgment. Only after a
successful session sweep may the worker revalidate its exact installed policy
and Root pool and send its own management-mTLS Root receipt. No TLS listener or
handshake configuration is fabricated. Explicit empty pools deny all Device
sessions and remain valid installation evidence after removing the last Root.
Root-policy exclusion also omits denied branches from CRL preparation so an
unavailable CRL under an already removed key cannot stop unaffected branches.

Root trust configuration is separate from the explicit bundle mode below.
Both must be enabled and qualified before completing the required-consumer gate
in dev; Root-policy receipts alone do not authorize Product activation.

#### Bundle activation and first-issuer bootstrap

Implemented locally in Video Cloud `728362d`. PostgreSQL-backed tests cover
actual governed activation, worker mTLS receipts after a successful scan, missing
own CRLs after activation, parent-CRL advancement/revocation, installed-version
fences, empty/wrong trust, terminal branches and restart. Full related suites,
race checks and vet passed. This is not live deployment evidence.

The active dev milestone is estimated at approximately 96%. The complete consumer
is deployed; activation/replacement and revocation/failure/restart acceptance passed.
Remaining work is the reproducible full dev run (ordered item 4 above). These are remaining acceptance jobs within
the same milestone, not new milestones. After each commit, report this estimate,
its remaining work and the five broad unfinished reporting areas.

Bundle acknowledgment is an explicit mode of the reviewed Device authority
manifest. Each entry already pins its public chain and bundle digest, so no
second manifest or trust store is needed. The worker installs those exact versions
into its registry session verifier and requires configured Root trust. Every
accepted Device lineage must match those versions in the identity database snapshot.

Ready authorities may appear in the manifest but remain excluded from Device
acceptance and CRL receipts. Their CA bundle can be acknowledged after verification
of the registered chain, installed Root pool, current Root policy and parent CRLs,
followed by a successful session sweep. A ready CA does not need its own CRL before
activation. After activation, the worker resolves the same pinned authority's
actual active record and requires its fresh CRL before allowing Devices. It never
changes a ready status locally to make CRL validation pass. Retiring lineages stay
usable while valid; terminal lineages remain permanently excluded.

For the first Root, the controller may expose and accept receipts for the current
environment/domain distrust policy while that Root is ready. This is removal-only
policy evidence, not activation. A ready Root has no parent CRL. Installing its
independently provisioned self-signed Root and acknowledging its pinned bundle
releases activation. Devices stay denied until the entire active Root/Brand/Product
lineage and all required CRLs exist. Bootstrap does not bypass required consumers.

#### Scoped complete-consumer dev rollout

Use clean Video Cloud `728362d` to build one unique dev image and verify its
registry digest. Update only `pki-controller` and the `mqtt-pki` worker container;
retain the existing EMQX/API images, database roles, runtime credentials and PVC.
Prepare the worker's independent management client with canonical
`pki-dev-prepare --environment dev --consumer pkibroker`. Append its public client
CA to the controller's existing client trust; preserve the controller server key
and all existing client CAs. Allow only the isolated broker pod to reach the
controller's TLS port through a dedicated NetworkPolicy.

Provision a separate retained `mqtt-pki-trust` PVC for the worker's Root policy,
CRLs and terminal denial state. Keep that volume out of the EMQX container.
Kubelet may add group permissions recursively when remounting an fsGroup PVC.
A non-root init container therefore creates/restricts the Device state directory
and restores all regular state files to 0600 on every startup; the worker
mounts reviewed public Root/Brand/Product-v3 authority documents plus the
independent Root PEM, and its own management identity. Enable strict CRLs,
Root-policy consumption and bundle receipts together. The controller's required
consumer set becomes `video-cloud-api,pkibroker`; adding the broker must not remove
the API gate. Use Recreate with one replica and API-enforced resource-version and
old-image preconditions. Persist every scoped manifest and image/feature override
under the canonical dev configuration before considering the rollout durable.

First verify current v3 receipts and an actual Device MQTT roundtrip with both
containers at the expected digests. Then use fresh governed issuance to show
missing broker receipts block activation, reviewed installed bundles release it,
and revocation finalization waits for new signed trust and post-sweep receipts.
For Product v4 gate testing, first install its reviewed bundle reference in the
API while keeping its Root pool, strict CRLs and runtime image unchanged. Confirm
API-only acknowledgment still leaves activation blocked. Then add the ready v4
public issuer to the worker manifest alongside v3 and restart the isolated worker.
After its real receipt, activate v4 and publish its initial provider CRL. Keep
retiring v3 trust/signing capability while existing v3 devices still need renewal;
add v4 to the API CRL manifest before testing new v4 Device credentials.
Retain failure/restart evidence and independently check persisted denial state.
The controller allowlist and worker are prerequisites to gate testing, not
acceptance evidence by themselves. No staging, Git push, PR or full-platform
provisioning is part of this rollout.

The isolated `mqtt-pki` Deployment runs EMQX 5.9.0 plus `pkibroker` using verified
running image digests `91ff2f25da30904a6d3ddf28b09787ba391a34e04271babe8bcd20adbb3dacd7`
and `536f52d49c78d57b846fa80a683c787ea6c9a0dba7f0861e7b0f9bb0a10c81ef`, respectively.
The original dev broker remains 5.8.7; staging is untouched. Broker management HTTP
binds to pod loopback, with credentials limited to its bootstrap and colocated
worker. External MQTT uses verified server TLS and certificate-bound tokens.
Authentication/authorization caches are disabled; no permissive authenticator
fallback is configured. The worker successfully reset both caches and scanned
actual sessions. Its dedicated login `rtk_pki_broker_dev` inherits only the existing
PKI verifier role, defaults to read-only transactions and has no superuser,
role/database creation or replication privilege. PKI issuer UPDATE and binding
INSERT privileges are absent; the live session scans exercised its database reads.

Product v3 `56df0589-ae15-4fc0-b4a2-b4114c5b95eb` is active after distinct approvals,
one internal OpenBao key generation, Brand signing, import and actual API bundle
acknowledgment. Activation without that acknowledgment returned 409. Its mount is
`pki-issuers/device/56df0589-ae15-4fc0-b4a2-b4114c5b95eb/v3`; bundle digest is
`4ec29d424a583f1f446f9244b2f224c5a772b4d3eb86b333cc4f1e74828b5a4c` and CRL digest is
`fa6a914a8c0793e188058631fd12654ce452a5149deb0f2f6f58fdcbec1f1141`. The API now
consumes Root/Brand/Product-v3 CRLs. Product v2 remains revoked; its mount/key and
audit evidence remain, while obsolete controller/signing policies were removed.
Current policies target only v3. Provider metadata and effective controller-token
checks again verify no exported/readable Product key or controller signing grant.

Fresh device `pki-dev-e824d85b32c946258f64f4e400074d20` generated its own key and
enrolled through production run `aeec4936-7c41-4d18-ac4b-b76a44a9c363`, reservation
`d10cf206-da20-48bc-b793-c6f2fd7d2080`. Direct API mTLS issued its token. The real
broker passed an allowed subscription and QoS1 publish/receive roundtrip and denied
`_bc/other/#` and `$aws/#` subscriptions. With client keepalives continuing, its
session closed after 59.260888 seconds; the same still-valid token reconnected
successfully. The older revoked Product-v2 device's unexpired token returned
CONNACK 5, establishing real-broker denial in addition to the earlier HTTP checks.

During a new-key renewal, both predecessor and successor connected to MQTT.
Successor acknowledgment at `2026-09-08T07:26:21.464451Z` cut off the old identity;
its socket closed 3.112123 seconds later at a connection age of 19.262191 seconds,
well before lease expiry. The worker recorded `examined:2, disconnected:1`.
The successor remained connected for a further 12-second observation window.
Old-token MQTT reconnect returned CONNACK 5 and old-key API authentication returned
401; successor-key API authentication passed. Renewal replay was identical, and
the predecessor could not acknowledge its replacement. The test harness then
closed the surviving successor session deliberately.

A broker restart exposed Docker overriding HOCON's node name with the Pod IP.
The isolated deployment now explicitly sets `EMQX_NODE__NAME=emqx@127.0.0.1`.
After verifying that actual name, a second controlled restart preserved it, both
image digests, PVC UID `21e2bc2a-ac11-4cb7-af2d-fbdc54bda70e` and its volume. The
new Ready pod UID is `2ca1058d-3279-4181-a8d4-6edb2a1f16d8`. Cache reset repeated
successfully. Post-restart, the predecessor was still denied and the successor's
ACL/roundtrip test passed. This does not claim durable queued-session recovery.

Evidence is retained in `mqtt-broker-startup-evidence.json`,
`mqtt-revoked-v2-connect-denied.json`, `mqtt-worker-renewal-evidence.json`,
`mqtt-stable-restart-{before,after}.json`, `mqtt-post-restart-auth-evidence.json`,
`product-v3-*`, and `device-2/mqtt-{lease,connect,renewal}-evidence.json` in the
protected rehearsal directory. Exact scoped manifests and Secrets are retained
in the protected rollout directory; `PKI_MQTT_IMAGE` and `PKI_BROKER_IMAGE` pin
images. Credentials come from the canonical `pki-dev-prepare --broker` flow;
partial/invalid existing files fail without rotation. No database reset was used.

**Remaining acceptance:** the worker enforces Device CRLs through live registry
reads but has no Device trust-consumer acknowledgment configured. Required
controller consumers remain `video-cloud-api`; a sweep is not a bundle/CRL receipt.
Finish broker consumer acknowledgment/gating and retain a reproducible full dev
run before closing distribution acceptance. Governed MQTT/Service identity renewal,
HA, durable queued-session recovery and independent custody remain separate work.

## Previous checkpoint: Product revocation and HTTP sessions

The disposable Product v2 issuer is now **revoked**. Root and Brand remain active;
the activation and enrollment records below are historical checkpoints. A new
Product issuer and fresh device are needed for the next MQTT acceptance run.

Governed operation `75ddce79-8f0b-41b5-a55b-cb043473ab43` used distinct simulated
PKI-admin and custodian approvals. Before execution, the device authenticated
with its current successor key and opened a verified-mTLS WebSocket (HTTP 101).
It remained connected through a full 12-second observation interval. Revocation
execution immediately changed registry authorization; the existing socket closed
8.003 seconds after the revoke request began. New token issuance returned 401
and certissuer renewal returned 403. These denials were observed before publishing
the new Brand CRL, so they establish online registry/session enforcement.

Finalization before CRL publication returned 409. The encrypted dev Brand key
then signed full CRL number 2 containing the revoked Product CA serial, reason 5
(cessation of operation). The API process fetched, installed, persisted and
acknowledged digest
`fac732d26b8373d950a284da07a72d07301d3ae9d1a447dec478e511bf76bb33`
at `2026-09-08T06:57:54.712328Z`. Only after this actual consumer acknowledgment
did governed finalization succeed. New device connections were then rejected at
the TLS handshake as well. No operator-generated acknowledgment or direct SQL
revocation write was used.

Full-chain CRL enforcement is enabled on the separate dev API with
`VIDEO_CLOUD_AUTH_CRL_MANIFEST=/run/pki-device/crls.json` and
`VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS=true`. The manifest pins the public
Root, Brand and Product authority records; their CRL caches live on the existing
PVC under `/run/pki-state`. Initial Root/Brand CRLs were signed by their encrypted
local simulation keys. The Product CRL was retrieved from OpenBao's public
`cert/crl` endpoint and was signed inside OpenBao; no Product key was exported.

The rollout exposed a bootstrap/runtime configuration conflict: leaving the
completed Root bundle acknowledgment enabled makes its CA-only chain enter the
client CRL verifier, which correctly requires a leaf/issuer chain. After verifying
all three issuers were active and their CRLs were acknowledged, the API's
`VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST` setting was removed. The completed bundle
receipts remain in the registry; dynamic root-policy and mandatory CRL verification
remain enabled. Do not re-enable the historical all-issuer bootstrap manifest in
this steady-state configuration. Support for simultaneous Root bootstrap
acknowledgment and mandatory client CRLs has not been qualified.

A controlled API restart then replaced the pod with
`597e2d97-956d-4504-a6b1-2ecd9eb84fda`. Its image ID was unchanged, and the Root
policy plus all three persisted CRL files retained identical SHA-256 hashes.
The new Ready process still rejected the revoked device at TLS handshake.
`api-crl-restart-before.json` and `api-crl-restart-after.json` retain this check;
`product-v2-revocation-evidence.json` has SHA-256
`05d1f3314a1ce825f855c583605b8efd63d84d1255aa5ac20f084a14135669b3`.

Scoped deployment and ConfigMap manifests are persisted in the canonical dev
store. The revoked Product was removed from retained initial bundle references;
its pinned CRL authority and public revocation evidence remain available. The
API image stays at `548bde6883afbe0708bfe1095389b08288d86a0f6e3cf4d5bf2b012a7e133e16`.
The original API, factory enrollment, certissuer, MQTT and staging workloads were
not redeployed for this CRL/session test. No database reset was needed.

Evidence in the protected `pki/fresh-rehearsal` directory includes
`product-v2-revocation-evidence.json`, the requested/approved/pending/completed
operation snapshots, `brand-crl-2-imported.json` and the public ceremony manifests.
CRLs in this rehearsal expire on 2026-09-11; freshness remains enforced. This
checkpoint qualifies the recorded Product-CA revocation and HTTP/WebSocket case,
not individual Device-leaf revocation, MQTT, every provider failure, or production
custody/recovery.

## MQTT callback transport deployed (2026-09-08)

Video Cloud source `9d11534`, workspace packaging `d9f958b`, is running only in
`video-cloud-api-pki` with verified registry/running digest
`sha256:536f52d49c78d57b846fa80a683c787ea6c9a0dba7f0861e7b0f9bb0a10c81ef`.
Ready pod UID: `8947006f-b2ba-40d9-b57c-e2a0776e26de`. The image includes the
existing `pkibroker` executable; the worker is not deployed yet.

The optional callback listens on internal HTTPS port 18447. Its service client
is `emqx-pki`, prepared by the canonical dev transport command with an independent
client CA. The API mounts only that public CA in `pki-mqtt-callback-ca`; the client
key remains in the dev consumer directory pending broker deployment. The new
`allow-mqtt-pki-callback` NetworkPolicy permits only pods labeled `mqtt-pki` in the
same namespace on this port. The existing policies were inspected for overlapping
permissions. No public Ingress or existing broker setting was changed.

Live verified-TLS probes passed: the broker identity plus existing broker bearer
key can authenticate the internal server account; an incorrect bearer key returns
explicit deny. Missing identity, Device identity, another management identity and
wrong server name are rejected. `/request_token` returns 404 on the callback.
The previously revoked device's still-unexpired token returns deny through the
real MQTT authentication handler. These checks establish callback transport and
authorization, not an EMQX client session.

The public Device listener still rejects the revoked device and the broker service
identity. This negative recheck used TLS 1.2 to obtain explicit remote TLS alerts;
a TLS 1.3 rejection through kubectl port-forward reset that forwarding process,
which was restarted only after its terminal failure was confirmed. The Root policy
and all three CRL cache files retained their previous SHA-256 hashes. Public
Device trust was not expanded for the broker.

`mqtt-callback-evidence.json` in the protected rehearsal directory contains these
results. Scoped Deployment/Service/ConfigMap/NetworkPolicy manifests and
`operator/env/PKI_API_IMAGE` retain the exact rollout; full-platform provision does
not automatically apply these PKI overlays. Full API/config race suites, vet,
paired-listener cleanup, actual TLS isolation and dev preparation/packaging tests
passed before deployment. No database reset or staging mutation occurred.

The next slice below has completed item 1 and image/transport preparation from
item 2. Remaining: deploy the isolated compatible EMQX broker and session worker,
provision the next fresh Product/device, then measure MQTT ACL/lease/replacement/
revocation behavior and retain reproducible full-run evidence. `mqtt-pki` server
transport material is prepared but is not governed MQTT-domain custody evidence.

## Next execution slice: MQTT transport and dev acceptance

Read-only discovery confirms the existing dev `mqtt` StatefulSet runs EMQX 5.8.7.
The implemented session worker targets 5.9+; the separate API also needs an
internal callback transport. Its device listener enforces Device mTLS/CRLs and
cannot accept the broker's independent service identity.

1. Add an optional internal API HTTPS listener exposing only
   `POST /v1/internal/mqtt/authenticate`. Reuse the API server TLS identity, but
   require a separately configured broker client CA and exact broker client name.
   Keep the existing broker bearer-key check, token provenance, current registry
   checks, CRL policy, short leases and ACL calculation. The listener must never
   expose device token issuance, management APIs, metrics or other API routes.
   Starting or stopping either listener must stop its peer and close resources.
2. Package the existing `pkibroker` worker in the dev image and prepare a distinct
   broker callback client identity using dev transport provisioning. Create an
   isolated dev broker with a pinned compatible EMQX image, server TLS, disabled
   authentication/authorization caches, no permissive authenticator fallback,
   and NetworkPolicies for only the required connections. Existing dev `mqtt`
   and staging stay untouched. Use `pki-dev-prepare --environment dev --broker` for separate, persistent
   credentials in the canonical dev store; missing/invalid existing files must
   fail rather than rotate credentials. Give the worker a dedicated database
   login inheriting only `rtk_pki_verifier`. Broker management credentials belong
   only to its colocated session worker; localhost management HTTP remains inside that pod.
3. Provision a fresh Product issuer/device using the existing governed APIs and
   actual consumer acknowledgments. Connect over verified MQTT TLS using a
   certificate-bound token, verify publish/subscribe ACLs and the 60-second lease,
   then test replacement/revocation, old-token reconnect denial and removal of
   the exact affected live session while a valid successor remains connected.
4. Persist scoped manifests and exact image/evidence records. Report broker
   compatibility, cache reset, actual session actions and failures separately;
   neither Pod readiness nor a simulated broker response closes MQTT acceptance.

This slice tests the established fresh-dev scope. Independent hardware custody,
cluster availability, legacy fleet migration and staging remain later work. The
broker's own server certificate remains distinct from Device/Product authority.

## Earlier checkpoint: fresh hierarchy active

Initial trust installation used Video Cloud `ad08eef`, workspace `0e12b30`.
It deployed the separate dev API listener
`video-cloud-api-pki` with verified image and running image ID
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:c79a651d2807db0bad26b5872784fc435f2833f9143ce042ade710b5f8d2fea7`.
The existing API Deployment and plaintext Service were preserved.

The API verified the reviewed Root bundle against its loaded TLS client pool
and acknowledged bundle
`b72ea85670db35f6a8edada250a1dc66fd4cf4581863d9e21be23133fc32e07b`
as consumer `video-cloud-api`. This acknowledgment came from the API process,
not the operator script. The requester then activated Root
`c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8` through Account Manager. The Root is active.
The direct TLS listener rejected missing client identity, a management identity
used as a Device identity, and an incorrect server name. These are negative TLS
checks. A real Product device connection and renewal now pass; see the fresh
device checkpoint below.

Continuous root-policy synchronization is now enabled. Its state is stored on
PVC `video-cloud-api-pki-trust`, class `linode-block-storage-retain` (requested
5Gi, CSI provisioned 10Gi). The init container copies the authenticated initial
state only if no state exists, then the API fetches and installs the current
policy before listening. Policy acknowledgment digest:
`96381e5ae666aa055fa02db383fdadeb5b581a13e234f26406050da6c7b5b455`.

A controlled Recreate restart changed the API pod UID from
`33ef644d-965e-4a0b-8c7b-065aeb2bee50` to
`7763f25e-6316-4d2e-905e-46ce23c588ee`. The exact image ID and persisted state
SHA-256 `e849ecce2ef73602e133ec69ee3fc181bb4bd1af9c9d355bcb81475f64b152d1`
were unchanged; the replacement became Ready with current policy acknowledgment.
This verifies initial trust persistence/reload, not device renewal recovery.

Fresh Cloud `5382d0cf-0966-45e9-ad5e-9955f4f6360d` and Product
`773aa199-16e3-4d59-94c6-5cdb5801c02f` are active in Account Manager. They were
created through the authenticated administration API with the temporary requester
as designated owner. Brand issuer `b4da0b7b-42ff-4966-95cc-50eb77c706f0` is active after distinct
approvals, an encrypted local key ceremony, Root signing, import and actual API
bundle acknowledgment. Activation before that acknowledgment returned 409.
Cloud and Product creation alone does not generate private CA keys.

Product v1 issuer `2a3f529b-a663-418b-bc62-da4092fe6cb1`, operation
`b09930b0-a32f-4151-83e5-b74cda10d352`, received distinct approvals but provisioning
failed before key generation. OpenBao rejected its nested mount with HTTP 400:
`path is already in use at pki/device/`. Provider inventory confirms the target
mount is absent. Existing legacy mounts remain unchanged. Its provisioning record
is retained as evidence; do not retry key generation or rewrite that reference.

The corrected design reserves new issuers at
`pki-issuers/<domain>/<issuer_id>/v<version>`. Existing governed references retain
their exact old layout. Video Cloud `1067380` is now deployed to the controller and separate API with
verified running image digest
`sha256:548bde6883afbe0708bfe1095389b08288d86a0f6e3cf4d5bf2b012a7e133e16`.
The unused v1 policy was removed and the controller role now has only the exact
v2 policy. This is a fresh dev provisioning repair, not migration work.

Product v2 issuer `14865afa-c346-4976-b9cb-915d7b21a958` is active. OpenBao generated
one internal P-256 key at
`pki-issuers/device/14865afa-c346-4976-b9cb-915d7b21a958/v2`, returning only the CSR.
The encrypted Brand key signed it in the local ceremony simulation. Import and
role configuration passed. Activation before API acknowledgment returned 409;
the API then verified the exact Product bundle against its actual TLS trust,
acknowledged it using its management identity, and governed activation passed.

Provider metadata exposes no private key. Effective controller-token capabilities
deny device signing, key reads, exported generation and legacy device signing.
This establishes the internal-generation path and runtime permission separation;
it does not qualify hardware custody or independent disaster recovery.

Local verification: complete affected pki, pkitrust, apiapp and postgres suites
passed against disposable PostgreSQL 16; OpenBao/PKI race tests and vet passed.
New/old governed layouts, exact policies, domain separation and malformed/broad
mount rejection are covered. The disposable test database was removed. Dev data
was retained; no reset or staging mutation was needed.

## Earlier bootstrap verification on 2026-09-08

- Dev context `lke649805-ctx`; namespaces `video-cloud-dev-video-cloud` and
  `video-cloud-dev-account-manager`.
- Controller image and running image ID:
  `ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:be8d897147d3aa297a2f58a1c3f284d44c403d4ece7b1d60d099e94643f83d35`
  (service source `0dca4d9`). Deployment is Ready.
- Account Manager image:
  `ghcr.io/hkt999rtk/rtk_cloud_dev/account-manager@sha256:4ee5be3c53a666c8fb9df4149000cb6ab8c6311d568b51cfdcf606339a943288`
  (clean service source `63c928f`). Registry digest verified; schema Job
  `account-manager-pki-migrate` completed and the Deployment is Ready.
- Account Manager uses the separate access/refresh RSA keys and management mTLS
  identity prepared earlier. Existing HS256 access/refresh tokens require fresh
  login. Existing workers were not upgraded. No database reset was needed.
- Controller mTLS accepts Account Manager and a separate `video-cloud-api`
  management identity. Missing identity, wrong server name and a server-only
  client identity are rejected. The API identity cannot invoke human endpoints;
  Account Manager without its request-bound human assertion is rejected.
- Live Account Manager-to-controller testing exposed missing ingress under the
  existing default-deny policy. Video Cloud `14ff366` adds the scoped policy to
  the renderer. Dev policy is installed; five renderer tests and the authenticated
  request passed. No staging policy was changed.
- Three ordinary verified fixture users were seeded in the dev database without
  PKI roles or email delivery. All password logins ran through Account Manager,
  with RS256 tokens and no MFA claim. Each was denied PKI access before its role
  assignment. The existing authenticated dev administrator assigned requester
  and approver `pki_admin` and a distinct `security_custodian` through the ACL API.
  These are simulated test actors, not independent human custody evidence.
- Device Root operation `f08aac85-a944-4e7f-a52a-49676ddc78a3`, issuer
  `c92fdbb1-f87b-4cab-80a6-fa77c2dce1d8`, reached `ready` after distinct approvals,
  CSR registration and certificate import. Self-approval and a PKI administrator
  claiming the custodian role returned 403. Activation without the runtime trust
  acknowledgment returned 409.
- The existing ceremony CLI generated an encrypted P-256 Root key locally and
  produced the public CSR/certificate. Only public artifacts went to the
  controller. This is a dev simulation on the workstation, not a physically
  offline ceremony or qualified escrow.

## Remaining acceptance work, in order

1. **Done:** API initial trust installation, actual bundle acknowledgment, governed
   Root activation, continuous policy synchronization and persisted-state restart.
2. **Done for this dev simulation:** Brand and Product issuer provisioning,
   separate key custody, exact OpenBao policies, actual API bundle acknowledgment
   and governed activation. Hardware/offline-custody qualification stays deferred.
3. **Done for the recorded case:** fresh device-key enrollment, direct-mTLS
   authentication, new-key renewal and interruption before acknowledgment across
   certissuer/API restarts. Exact replay and old-key cutoff are verified below.
   This does not qualify every possible provider failure or lost signing outcome.
4. **Partially done:** HTTP/WebSocket and real EMQX ACL, lease, replacement,
   old-identity denial and restart cases passed. Finish broker Device
   trust-consumer acknowledgment/gating before closing distribution acceptance.
5. Retain pass/fail results and a reproducible setup for the full dev lifecycle.

Initial HTTP acceptance currently requires consumer `video-cloud-api`. MQTT is
not included in that phase and is not yet qualified. Do not count missing
acknowledgments as user-input blockers or send fabricated acknowledgments.

The resolved bootstrap issue was: the API's automatic root-policy synchronizer
expects an already active/retiring Root, while the first Root needs actual
installation acknowledgment before activation. The new initial
installation path now resolves that cycle and automatic synchronization is running.
Activation checks remain enforced; disk-only installation is never a runtime ACK.

The initial installation path uses an explicit reviewed manifest of issuer IDs
and exact trust-bundle versions. API startup re-reads those issuers from its
registry, checks environment/domain/status/version and verifies their full chains
against its actual loaded TLS client trust pool before sending acknowledgments
with its own management identity. It never adds trust from the manifest. Any
validation or delivery failure fails startup; a restart safely repeats the exact
acknowledgment. After initial Root activation, configure the existing dynamic
root-policy synchronizer for continuing trust removal. This initial bootstrap
does not itself claim continuing revocation or MQTT acceptance.

## Retained configuration and artifacts

The canonical dev SecretStore contains:

- `pki/controller-bootstrap/rollout/pki-controller.json`: controller overlay,
  including scoped NetworkPolicy; `controller-auth-probe.json`: public TLS results.
- `pki/controller-bootstrap/rollout/account-manager-pki-migrate.json` and
  `account-manager-pki.json`: exact scoped schema/runtime manifests.
  `account-manager-before.json` retains the prior Deployment for review/rollback.
- `pki/controller-bootstrap/rollout/video-cloud-api-pki-deployment.json`, the
  matching Service/ConfigMap/PVC manifests and initial-state manifest retain the
  scoped API overlay. `PKI_API_IMAGE` pins its verified dev image;
  `PKI_CONSUMER_PODS=video-cloud-api-pki` maps its pod name to the existing
  `video-cloud-api` management identity. The restart-check annotation is transient
  operational evidence and does not change the persisted workload configuration.
- `pki/fresh-rehearsal/brand-active.json`, `product-v2-active.json`,
  `product-v2-custody-probe.json`, `product-v2-key-inventory.json`, and
  `product-v2-policies.json` retain hierarchy and provider evidence. The failed
  first Product attempt remains in `product-v2-prior-attempt.json` and its original
  operation/approval files. No provider key existed for that attempt.
- `pki/fresh-rehearsal/device-root-active.json`, `cloud.json`, `product.json`,
  `api-restart.json` and `api-tls-negative-probe.json` retain this checkpoint.
- `operator/env/PKI_CONTROLLER_IMAGE`, `PKI_REQUIRED_CONSUMERS`,
  `PKI_ACCOUNT_MANAGER_IMAGE` and the selected JWT/PKI file settings record the
  overlay configuration. These separate PKI overlays are not automatically
  applied by the default full-platform provisioner. Apply the retained overlay
  deliberately with current resource-version checks; do not assume a full
  provision preserves it or point all workers at the new Account Manager image.
- `pki/fresh-rehearsal/`: protected fixture account credentials, public operation
  and issuer snapshots, and encrypted local ceremony outputs.
- `pki/rehearsal-passphrases/`: protected dev simulation passphrase files. They are
  on the same workstation and do not constitute independent custody.

Credential files are not tracked or included in reports. Root/Brand keys never
belong in service database rows, container images or Kubernetes workloads.
No git push, PR, CI run or staging mutation was performed.

## Factory issuance and device renewal deployment policy

Reuse the existing independent certissuer service TLS identity and factory client
identity. Upgrade only dev certissuer/factory enrollment, set their environment to
`dev`, and enable Product PKI together. Use a projected Kubernetes token bound to
the certissuer service account, with only the active Product mount's signer policy.
Keep existing dev database credentials for this feature rehearsal; database role
qualification remains separate work. Do not migrate legacy enrollment records.

Certissuer startup validation must accept the Kubernetes authentication already
supported by its provider adapter and require the role/projected-token path.
For device renewal, load Device Root trust separately from service client trust.
Only the two device renewal routes may admit identities under the Device Root;
all other routes must retain service-chain verification. A matching factory or
service common name on a Device certificate cannot grant service authority.
The renewal handlers still require current registry binding, a new CSR key,
issuer lifecycle checks and successor-key acknowledgment.

## 2026-09-08 first fresh device and renewal

Factory enrollment now runs Product PKI in `dev` using Video Cloud `1067380`,
image digest `548bde6883afbe0708bfe1095389b08288d86a0f6e3cf4d5bf2b012a7e133e16`.
The previous deployment's environment was incorrectly `staging`; it is corrected.
Certissuer uses its existing independent service TLS identity and a projected
Kubernetes token for SA `certissuer-pki`, role `certissuer-pki-dev`. Its sole
policy signs through the active Product v2 mount; no controller rights or
legacy/App signer permissions were added. Existing dev database credentials
were preserved. Runtime database role qualification remains pending.

Actual startup exposed an AppRole-only configuration validator even though the
provider adapter supports Kubernetes auth. `86c0417` fixes that validation and
adds separate `CERT_ISSUER_DEVICE_CLIENT_CA` trust for renewal. Real TLS tests
cover a Device certificate with the same CN as a service caller: service routes
reject it. Product renewal still requires current registry binding. Configuration
and certissuer application race tests and vet passed.

Device `pki-dev-d9c7d8aeada0411bb5676100b6662ca6` generated its P-256 key locally.
An authenticated administrator created production run
`e507f37b-39dd-4380-b416-73d0bcbc9b0b` with quantity 1. Actual factory enrollment
reserved `6e6c55c9-f33f-4c40-a167-5ee784d67388`, signed through Product v2, completed
Account Manager coordination, and projected entitlement/device state. The returned
certificate matches the device key and issuer. Replay returned the same certificate
and reservation; the database reports quantity 1 of 1 and exactly one reservation.

Direct API mTLS authentication passed. The device generated a different successor
key and renewed through certissuer. The successor authenticates; predecessor
acknowledgment is denied, successor acknowledgment succeeds and is idempotent,
and the predecessor can no longer authenticate or renew after acknowledgment.
A Device certificate is also denied on the factory service route.

The first renewal replay exposed only an `overlap_until` precision difference
(first response nanoseconds versus persisted PostgreSQL microseconds). The
certificate was identical. `74906d7` returns the stored timestamp; database-backed
replacement race tests cover nanosecond input. Certissuer now runs `74906d7` with verified running digest
`sha256:7c678e7f6d56c10c4346b0dc5ded2cfaf6ee85ec397b688853bdb7f28b679292`.
The second renewal returned an exactly identical response on replay, including
the deadline. With acknowledgment pending, both certissuer and the API were
restarted using resource-version-checked Deployment changes. Replacement pods
had new UIDs, retained the expected image IDs, became Ready, and reproduced the
same renewal response from persisted state. Both keys authenticated during the
overlap. Predecessor acknowledgment was denied; successor acknowledgment was
idempotent and immediately denied the predecessor while preserving the successor.

The database contains exactly three certificate bindings and two acknowledged
replacement records for this device. Artifacts `renew2-issued.json`,
`renew2-restart-before.json` and `renew2-restart-evidence.json` retain the precise
case. This verifies a restart before installation acknowledgment, not every
possible unknown provider-signing outcome. No database reset was used.

The probe uses Go's verified TLS client, matching the application stack, with
explicit server CA and hostname validation. The macOS system Python TLS runtime
cannot decode the existing service CA algorithm; Python 3.14's strict X.509
default additionally rejects its missing authority-key identifier. No TLS
verification bypass was used. Service-certificate compatibility with strict
OpenSSL clients remains a later compatibility item, not a Device CA key change.

Protected artifacts are in `pki/fresh-rehearsal/device-1/`: enrollment request and
result, production token, device/successor keys, authentication responses and
renewal evidence. The current device key/chain are `successor2-key.pem` and
`successor2-chain.pem`; predecessor files are retained for denial tests. Only public IDs/results are recorded here. The retained scoped
rollouts are `certissuer-product-pki.json`, `factoryenroll-product-pki.json`,
`certissuer-device-renewal-roots.json` and the certissuer ServiceAccount manifest.
`PKI_CERTISSUER_IMAGE` and `PKI_FACTORYENROLL_IMAGE` accompany these explicit
overlays; the full-platform provisioner does not automatically consume them.
