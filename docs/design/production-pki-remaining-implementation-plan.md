# Detailed plan for the 18 remaining trust-consumer checkpoints

Baseline: workspace `7520203`, 2026-09-09. This document expands the **18 open
checkpoints** in the [active milestone](production-pki-trust-consumers.md#progress-calculation-2026-09-09).
It adds no milestone or completion credit. The authoritative count remains
**22/40 complete (55%); 18/40 open**. These are acceptance checkpoints, not 18
equally sized coding tasks. Some need runtime integration; others mainly need
live verification of existing code.

## Scope and execution order

All live work is in dev. Staging, legacy-data migration, physical hardware,
independent production custody, matched disaster recovery and full SDK integration
remain outside this milestone. No MFA implementation is required; future MFA is
for human login only. Keep core login unchanged unless a demonstrated defect
requires a small fix. Keep commits local; do not push or create a PR as part of
this plan.

Reuse `serviceidentity`, `pkitrust`, the existing registry/CRL workers, connection
owners and dev acceptance helpers. Add wiring or repair a demonstrated gap before
introducing another manager, daemon or abstraction. Source paths below are
implementation starting points, not claims that all their behavior is deployed.
`VC/` means `repos/rtk_video_cloud/` relative to the workspace.

Recommended order:

1. Close **T7**, the currently active Account Manager work package.
2. Adopt **M8 and M9**, then **T8 and T9**. Collect **T10** evidence alongside them.
3. Implement **R2**, then **R1, R3 and R4**, sharing the existing policy machinery
   while retaining separate domain state and authorization.
4. Complete **A4, A5 and A6** on the installed App trust path.
5. Close the cross-consumer coverage in **M11 and T11** using evidence collected
   during the earlier steps plus any missing cases.
6. Close **V3, V4 and V5**. Extend V3 procedures throughout implementation; do not
   wait until the end to start writing or running them.

Do not repeat a passed exercise merely because two checkpoints reference it.
For example, a T7 held-connection test can also support the relevant row in M11
or T11; those aggregate checkpoints still require their other consumers.

| # | Existing ID | Remaining checkpoint | Main work |
| --- | --- | --- | --- |
| 1 | M8 | Device API managed controller credential | Integration and dev lifecycle |
| 2 | M9 | pkibroker managed controller credential | Integration and dev lifecycle |
| 3 | M11 | Management sessions and trust failures | Cross-caller verification; fix gaps found |
| 4 | T7 | Account Manager Service transports | Remaining lifecycle/failure verification |
| 5 | T8 | Governed MQTT host and clients | Dev host/client integration and verification |
| 6 | T9 | Governed OpenBao transport | Host/client integration and verification |
| 7 | T10 | Independent public HTTPS renewal | Deployment and renewal evidence |
| 8 | T11 | Server connections and trust outages | Cross-host verification; fix gaps found |
| 9 | R1 | App root-policy adoption | Runtime installation, persistence and verification |
| 10 | R2 | Service root-policy adoption | Runtime installation, persistence and verification |
| 11 | R3 | MQTT root-policy adoption | Runtime installation, persistence and verification |
| 12 | R4 | OpenBao transport root-policy adoption | Runtime installation, persistence and verification |
| 13 | A4 | App API lifecycle enforcement | Governed App integration and live acceptance |
| 14 | A5 | App MQTT lifecycle enforcement | Broker integration and live acceptance |
| 15 | A6 | TURN/signaling lifecycle enforcement | Relay integration and live acceptance |
| 16 | V3 | Maintained acceptance coverage | Extend existing runners and failure recovery |
| 17 | V4 | Full dev acceptance and Device regression | Execute complete coverage |
| 18 | V5 | Final independent evidence audit | Reconcile runtime, configuration and results |

## Shared implementation and test rules

- Before a mutation, verify the canonical dev context, actual resource/container
  names, source revision, image digest, current trust and existing private state.
  Save scoped rollback values and operation intent. Patch with resource-version
  and old-value checks; use Pod UID/resourceVersion preconditions for replacement.
  Persist changed dev configuration and verify that rendering consumes it.
- Keep CA private keys at their approved custody/provider boundary. Generate leaf
  keys at their owner, in protected persistent state. Observe Service keys through
  public fingerprints/state hashes inside the owner, without exporting the keys.
  Reuse existing state after uncertain outcomes; reconcile the original request
  instead of generating another key or issuing a replacement operation blindly.
- Every lifecycle test uses a target identity and an unaffected control. Record
  issuer/leaf fingerprints, operation IDs, installed policy/CRL digests, actual
  consumer receipts, timestamps and the exact result. Never commit tokens, private
  keys, raw Secrets or credential-bearing responses.
- Measure held sockets or allocations established **before** a change. Disable
  automatic reconnect for the held-session probe; test reconnection separately.
  An application 401/403, certificate rejection and a transport outage are
  different results. A generic connection failure does not prove revocation.
- Before each cutoff test, derive the expected maximum delay from the configured
  sweep interval, lease/cache lifetime and termination deadline. Record the
  numerical bound and measured delay. For a dependency outage, specify whether
  still-valid installed evidence is usable and when it becomes insufficient;
  require denial at that documented boundary. Do not silently extend freshness.
- Exercise one dependency failure at a time in a scoped dev consumer. Restore it
  in cleanup and verify the control path. Do not claim selective cutoff when a
  whole shared process was restarted: record the actual affected connections.
- Run focused tests for changed packages, using a disposable PostgreSQL database
  where required (`VIDEO_CLOUD_TEST_DSN` for the Video Cloud integration tests).
  Required database tests must actually run; an unset variable and skipped suite
  is not a pass. Use race tests for changed connection/state ownership. Broaden
  testing when the change or a failure warrants it, rather than after every edit.

## 1. M8 — Device API managed controller credential

**Starting point.** Device API trust/CRL receipts exist in dev, but the isolated
`video-cloud-api-pki` controller consumer is distinct from the ordinary
`video-cloud-api` Account Manager caller qualified in T7.
Reuse `VC/internal/apiapp/{crl_sync,root_trust,root_trust_sync}.go`,
`VC/internal/pkitrust/managed_service_client.go` and `VC/internal/serviceidentity/`.

**Implementation steps**

1. Inventory the isolated API's controller clients, root/CRL workers, static
   credentials, identity subject and state mounts. Decide the exact approved
   Service subject for this workload without copying the other API Pod's key.
2. Supply the existing workers with the owner's dynamic client certificate and
   verified controller transport. Share one owner identity across that workload's
   authorized management requests; retain per-domain receipt permissions.
3. Wire start, scheduled renewal, operator renewal where supported, replacement
   connection eviction and shutdown into the existing application lifecycle.
4. Seed once through the approved Service authority, deploy the isolated API,
   obtain real receipts, remove bootstrap access/files, and persist the settings.

**Test steps**

1. Locally reject incomplete managed configuration and accidental static-key
   fallback; verify a changed identity closes its old managed connections.
2. In dev, prove controller authentication under the intended subject and exact
   root/CRL receipts. Wrong consumer identity and another domain's receipt fail.
3. Restart without seed files: retain the same key/leaf and resume current receipts.
4. Renew once: a different key/leaf and one corresponding successful issuance;
   reconcile a lost response without duplicate signing. Retire the predecessor,
   deny it explicitly, and keep the successor and control caller working.
5. Re-run Device mTLS token issuance and MQTT authorization on the isolated API.

**Done when:** the actual isolated API uses its retained managed credential for
all controller consumption, with renewal, retirement, restart and Device canaries
passing. Deployment or a receipt obtained with an operator credential is insufficient.

## 2. M9 — pkibroker managed controller credential

**Starting point.** `pkibroker` is a session-management worker, not the MQTT TLS
listener. Reuse `VC/internal/pkibrokerapp/app.go`, `pkitrust`'s Device/App consumers
and the managed-client owner established in M8.

**Implementation steps**

1. Inventory broker worker controller calls, callback/EMQX access and state. Bind
   only its controller management requests to its approved Service identity.
2. Inject one dynamic management identity into its Device/App policy and CRL
   consumers; preserve separate consumer IDs, domain scopes and installed state.
3. Connect identity renewal/eviction to worker lifecycle. Keep broker sweeping and
   acknowledgment ordered: successful installation and enforcement precede ACK.
4. Seed/adopt once on persistent storage, remove static management credentials,
   and persist the dev worker configuration.

**Test steps**

1. Verify Device and App consumers share the intended owner without cross-domain
   authorization. A valid Service certificate for another subject must fail.
2. Observe real installed-digest receipts and a real broker-session sweep.
3. Restart seed-free, renew, reconcile response loss, revoke the predecessor and
   verify successor operation, using the same checks as M8.
4. Deny controller access temporarily; require no fabricated receipt or successful
   revocation completion while required enforcement evidence is unavailable.
5. Recover and demonstrate target-session eviction plus unaffected Device traffic.

**Done when:** the real worker's managed identity, receipts and broker enforcement
remain correct across replacement, restart and a scoped failure.

## 3. M11 — Held management sessions and failure coverage

**Dependencies:** M8, M9 and the applicable T7 evidence. Build on
`scripts/go/pki-dev-probe/service_session.go`, Service listener/client ownership,
and the existing Service retirement runners.

**Implementation steps**

1. Build a coverage matrix for Account Manager, certissuer, controller, factory,
   Device API and pkibroker management connections. Record source, destination,
   subject, allowed route, policy domain and whether a persistent connection exists.
2. Extend the existing probe only where it cannot exercise the real authenticated
   method/body. Keep bearer material inside the owner and retain no exported key.
3. Repair uncovered admission, connection tracking, sweep or receipt-ordering
   gaps in the existing owners; do not create a parallel authorization path.

**Test steps**

1. Establish a real authorized held connection and an unaffected control before
   renewal or revocation. Verify both work without reconnection.
2. Replace/retire the target credential, measure old-connection closure and deny
   a new connection with the old credential. The successor/control must work.
3. Independently exercise unavailable registry, stale/missing/invalid CRL and
   failed enforcement. Check the declared denial deadline and missing ACK.
4. Restart with current state and with an isolated stale-state fixture. Stale
   state must not resurrect retired identity or falsely complete an operation.
5. Restore the failed dependency, verify current receipts and normal caller work.

**Done when:** every applicable matrix row has measured pass evidence or an
explicit source-backed explanation that no held connection exists. Readiness and
fresh-handshake tests do not substitute for held-session coverage.

## 4. T7 — Finish Account Manager Service transports first

**Already passed:** managed listener/caller adoption, actual factory enrollment,
owner App-token issuance, two authorization denials, exact enrollment replay,
and separate API/factory/Account Manager restarts with unchanged identity state.
Do not repeat adoption. Reuse `scripts/pki-service-dev/account_callers.py`,
`account_listener.py`, the renewal/retirement helpers,
`VC/internal/pkimanagementapp/`, `VC/internal/apiapp/` and the factory owner.

**Implementation steps**

1. Inventory the current API client, factory client, Account Manager client and
   internal server leaf. Map each to its prior passing evidence and identify only
   lifecycle cases not yet proven on this destination.
2. Extend caller acceptance with existing owner renewal controls, public-state
   inspection, registry receipt checks and authenticated held-connection probes.
   Preserve the fixed internal route ACL and existing bearer authorization.
3. Ensure each required listener/caller installs the relevant Service CRLs and
   contributes the required evidence before old-leaf retirement completes.
4. Implement only defects found in key replacement, dynamic transport selection,
   eviction or failure propagation. Preserve the separate client/server state files.

**Test steps**

1. Establish target/control requests through the internal listener, including a
   held request/connection; record current leaf/key fingerprints and issuance rows.
2. Trigger one renewal per unqualified owner. For Account Manager, verify both
   client and server results if its renewal operation rotates both. Verify the
   new key/leaf, served certificate, registry record and unchanged identity/DNS.
3. Lose one renewal response in a scoped fixture; restart/resume the retained
   request and require no extra successful signature. Verify seed-free persistence.
4. Revoke the replaced leaves using the required consumers and current CRLs.
   Observe old-connection cutoff, explicit old-leaf reconnect denial and valid
   successor traffic. Client renewal alone is not proof of revocation.
5. Exercise listener/registry/CRL failure separately. No application request may
   escape via plaintext or static credentials; no premature receipt may be emitted.
6. Restore dependencies; rerun the existing caller proof and Device/login canaries.

**Done when:** all remaining T7 lifecycle/failure cases pass for the actual
Account Manager destination. This closes one checkpoint, moving the baseline
from 22/40 to 23/40 (57.5%) if no other checkpoint has closed meanwhile.

## 5. T8 — Governed MQTT TLS host and actual clients

**Starting point.** `VC/internal/emqxhost/` has a tested native foreground owner;
the dev workload is Kubernetes-based. Reuse `VC/internal/mqtt/`,
`pkitrust/registry_server.go` and the [EMQX host contract](production-pki-emqx-host.md).

**Implementation steps**

1. Map the actual dev MQTT TLS termination, broker process, client endpoints and
   mounted state. Select the dedicated dev PKI broker; preserve unrelated broker
   workloads and existing authentication/ACL configuration.
2. Adapt packaging/process ownership only as necessary for one dev broker Pod:
   persistent host state, one supervisor-owned foreground EMQX process, readiness,
   and bounded process shutdown. Do not assume a native systemd unit manages a Pod.
3. Adopt a serverAuth-only leaf under the independent MQTT authority, with exact
   DNS policy and owner-generated key. Keep Service management identity separate.
4. Wire actual API/log-ingester MQTT clients and acceptance clients to the MQTT
   trust/CRL owner. Preserve client authentication, topic isolation and QoS behavior.
5. Persist host/client settings and remove first-install seed material after restart.

**Test steps**

1. Verify the certificate actually served and its MQTT lineage/DNS/EKU; reject a
   wrong-domain or wrong-name host before sending MQTT credentials.
2. Use authenticated clients for publish/subscribe, topic denials and QoS1 roundtrip.
3. Renew the host, prove stop-before-replacement and persistence, then reconnect
   with the successor. Record the actual interruption and durable-session behavior.
4. Revoke the old host leaf and demonstrate explicit denial; valid control paths
   outside the replaced broker remain healthy.
5. Deny trust evidence, verify bounded broker/client enforcement and no false ACK,
   restore the dependency and repeat authenticated traffic.

**Done when:** the governed host and actual clients pass these dev checks.
The existing stop/restart design may close all connections on that broker; this
checkpoint does not promise HA, zero downtime or selective per-client survival
during whole-server replacement.

## 6. T9 — Governed OpenBao TLS host and provider clients

**Starting point.** Registry-backed HTTP clients exist in
`VC/internal/pkicontrollerapp/provider_transport.go` and
`VC/internal/certissuerapp/provider_transport.go`. Existing OpenBao TLS files do
not by themselves establish governed host renewal or root-policy adoption.

**Implementation steps**

1. Inventory the actual OpenBao TLS listener, certificate owner, persistent state
   and controller/certissuer clients. Keep provider authentication/seal material
   separate from the transport certificate and its independent root.
2. Connect the existing server identity state/renewal logic to OpenBao's supported
   certificate installation/reload or bounded restart mechanism. Verify the
   deployed version's behavior before selecting the mechanism.
3. Define startup order: preserve existing verified TLS while preparing the
   governed certificate/trust. Issuance, provider login and trust verification must
   not depend on an unavailable new listener. Never solve the cycle by disabling TLS.
4. Enable the actual clients' exact-origin registry/CRL validation, eviction and
   receipts. Roll out the narrow host/client changes and persist them.

**Test steps**

1. Perform real workload login and a scoped permitted provider operation; require
   hostname/chain/registry/CRL verification before credentials leave the client.
2. Replace the host key/leaf, verify the served successor, resume provider requests
   and restart from retained state without reseeding or exposing a private key.
3. Deny the old leaf after revocation; wrong root/name, redirected origin and
   plaintext must fail without forwarding provider credentials.
4. Inject transport/trust failure. Issuance must remain safely pending or fail
   according to existing semantics; reconcile uncertainty without duplicate signing.
5. Restore access and demonstrate controller/certissuer recovery plus Device and
   App issuance canaries. Retain installed receipts and the measured outage.

**Done when:** the real dev host and both provider clients pass. OpenBao seal
recovery, backup restoration, HA and production custody are separate milestones.

## 7. T10 — Public HTTPS certificate and renewal evidence

**Implementation steps**

1. Inventory current dev ingress TLS terminations, public names, certificate
   objects/issuers, renewal owner and Secret references, using public metadata only.
2. Confirm public HTTPS uses its existing public CA/ACME path. Repair only a
   demonstrated issuer, renewal or deployment-binding defect.
3. Select an existing valid renewal event for which issuance and installation can
   both be proven, or use the deployed certificate manager's supported dev renewal
   action. Do not invent an unverified CLI or replace public TLS with a private CA.

**Test steps**

1. Validate each in-scope endpoint's public chain, hostname, dates and served
   fingerprint using normal client trust, including the public Device/API origin.
2. Correlate renewal event, old/new serial/fingerprint, Secret update and actual
   ingress installation. A future renewal timestamp is not executed-renewal evidence.
3. Check browser/human login and authenticated App requests after installation;
   confirm the independent client-certificate policy still applies where required.

**Done when:** public endpoint inventory and successful renewal/installation
evidence are retained for the relevant certificates. Expiry dates alone do not close it.

## 8. T11 — Held server connections and trust-outage coverage

**Dependencies:** T7–T9 and applicable root-policy results. Reuse
`pkitrust/server_http.go`, `server_host.go`, MQTT transport owners and existing probes.

**Implementation steps**

1. Build the server-side coverage matrix: certissuer, controller, Account Manager,
   governed MQTT and OpenBao listeners, and each actual client family.
2. Add missing held-stream/session probes and connect replacement/trust-denial
   events to the existing owners. Account for TLS resumption and connection pools.
3. Reuse previous host evidence where it exercised the same deployed behavior;
   record whole-process termination separately from selective connection eviction.

**Test steps**

1. Establish held target/control connections before server replacement/revocation;
   include a live HTTP stream or active MQTT connection where supported.
2. Measure closure and prohibit continued requests on the old connection. Test
   resumed TLS and fresh reconnect separately; verify successor acceptance.
3. Independently remove registry access, make required CRL evidence unusable and
   fail installation/enforcement. Require the recorded failure deadline and no ACK.
4. Restart with valid and stale state, restore dependencies and verify current
   service traffic without reviving retired trust.

**Done when:** every private-host/client row has valid held-connection, failure,
restart and recovery evidence. Public HTTPS renewal remains T10.

## Root-policy work shared by R1–R4

Reuse `VC/internal/pkitrust/{sync,trust,registry_device_root}.go` and existing
registry root-policy operations. The generic consumer already accepts an
environment/domain; its deployed Device adapter does not complete other domains.
Separate **installing an approved new root** from **durably distrusting an old
root**: a distrust response must not introduce an arbitrary new trust anchor.

For each domain, bind installation to the reviewed authority and exact digest,
persist monotonic distrust/version state, atomically install the actual runtime
pool and chain policy, complete required connection enforcement, then ACK that
installed result. Retain protection against a removed root reappearing through
cross-signing, old files, TLS resumption or a stale configuration. Define how an
approved new root is provisioned before old-root withdrawal; a changed fixed
fingerprint alone is not a root-policy implementation.

Common tests for each domain: wrong environment/domain/root/digest; stale version
and conflicting same-version policy; persistence/installation failure; lost ACK;
explicit empty trust set; restart with current/stale state; removed-root and
cross-signed-chain rejection; unaffected permitted lineage survival. Use isolated
fixtures for destructive empty-trust and rollback cases; do not remove all trust
from a shared dev authority merely to demonstrate a negative case.

## 9. R1 — App root-policy adoption

**Implementation steps**

1. Map App verifiers in API, broker and TURN/signaling, including public ingress
   client-chain handling. Reconcile the current App issuance lineage with the
   governed registry; ordinary App-token success alone does not establish this.
2. Attach domain=`app` policy consumers to existing App verifiers, using separate
   App state and management authorization. Reuse the shared machinery above.
3. Install approved App trust at every terminating/verifying boundary. Wire
   removal to API connections and broker/relay authorization owners before receipts.

**Test steps**

1. Run the common root-policy tests on each adapter.
2. In dev, prove App operation under the installed approved lineage, then withdraw
   a test old lineage and verify API/MQTT/TURN denial and held-session cutoff.
3. Preserve an unaffected App lineage and Device/Service operation; restart every
   changed consumer and verify monotonic state plus actual receipts.

**Done when:** all in-scope App verifiers enforce the reviewed change durably;
an unchanged ingress trust pool or an ACK before session enforcement leaves R1 open.

## 10. R2 — Service root-policy adoption

**Implementation steps**

1. Map Service client verification at listeners and server verification at callers,
   including renewal/controller transports. Treat the two directions explicitly.
2. Add Service-domain installation callbacks/state to the existing owners. Update
   the active verifier as well as TLS pools; fixed root settings must not silently
   retain or reintroduce withdrawn trust.
3. Prepare approved successor trust and authenticated management/renewal reachability
   before withdrawal. Keep a documented dev recovery path outside the failing
   workload without reopening unrestricted bootstrap credentials.

**Test steps**

1. Run common root-policy cases in both mTLS directions, including cross-signing
   and session resumption.
2. Prove successor Service clients/servers and management receipts first; withdraw
   the test predecessor, measure old-socket cutoff and deny reconnect.
3. Restart listeners/callers, verify no rollback, then run factory enrollment,
   App authorization and Device canaries. A failed consumer must block completion.

**Done when:** both directions on every adopted Service path install and enforce
the reviewed policy with durable state and real receipts.

## 11. R3 — MQTT root-policy adoption

**Implementation steps**

1. Apply the domain=`mqtt` policy to actual clients that verify the private MQTT
   host, including API/log ingester and dev acceptance clients.
2. Connect trust changes to each MQTT dialer/reconnect loop and active connection
   owner. Retain separate Device/App client-certificate policies on the broker.
3. Coordinate approved successor-host trust before removing the prior server root;
   persist client state and require install/enforcement receipts where specified.

**Test steps**

1. Run common root-policy tests and show a broker chain under a removed root fails
   before client credentials are sent, including alternate/cross-signed chains.
2. Hold authenticated MQTT traffic across the change; measure target connection
   closure, successor reconnect and topic/QoS behavior.
3. Restart clients with stale/current state and fail a policy installation. No
   removed root may return and no failed installation may generate a receipt.

**Done when:** every in-scope private-MQTT client enforces durable server-root
policy. This does not change the Device/App identity authority or public-CA contract.

## 12. R4 — OpenBao transport root-policy adoption

**Implementation steps**

1. Attach OpenBao-transport-domain policy to controller/certissuer provider HTTP
   owners, with independent state, approved trust and exact-origin restrictions.
2. Install approved successor TLS trust before withdrawing the old transport root;
   preserve authenticated provider access during the controlled transition.
3. Integrate pool replacement, active-connection eviction and exact receipts without
   changing provider login tokens, seal material or recovery-command trust.

**Test steps**

1. Run common root-policy tests and wrong-origin/redirect tests before provider login.
2. Hold a provider connection across withdrawal; verify closure and old-root denial,
   then authenticate and perform a permitted operation through the successor.
3. Exercise persistence failure, lost receipt and restart; prove no rollback and
   safe reconciliation of an interrupted signing request without duplicate issuance.

**Done when:** actual provider clients enforce reviewed transport-root changes
with durable state and receipts; backup/seal recovery is not claimed by this result.

## 13. A4 — App API renewal, revocation and restart

**Starting point.** Local App verification/CRL adapters exist in
`VC/internal/pki/app_*.go`, `VC/internal/pkitrust/registry_app.go` and
`VC/internal/apiapp/app_crl_sync.go`. T7 proves authorization transport, not App
registry revocation. Depends on R1's API trust wiring.

**Implementation steps**

1. Connect the actual dev App issuance/renewal path and API verifier to the same
   governed App registry/CRL records and subject fingerprint binding.
2. Wire the existing App consumer's enforcement and receipts into the serving API,
   accounting for client certificates terminated at public ingress.
3. Make missing/stale evidence propagate through existing authorization and
   connection owners; keep normal password login and Device identity paths separate.

**Test steps**

1. With an authorized App user, obtain a token and use it for an allowed Device API
   action; check unauthorized Product/device access and wrong App subject denial.
2. Renew with a new App key, verify registry/installation evidence and successor use.
3. Hold a target API connection/session and an unrelated App control. Revoke the
   predecessor; deny old-certificate issuance and old authorization where the
   contract binds it, close the target within its bound and retain the control.
4. Test valid-looking forged certificate headers at an untrusted entry point,
   stale/missing CRL, registry failure and restart. None may restore revoked access.
5. Recover, confirm actual App receipts, repeat the positive action and human login.

**Done when:** governed App renewal/revocation and selective API enforcement pass
on the deployed path, not merely in a handler fixture or at token issuance.

## 14. A5 — App MQTT renewal, revocation and restart

**Dependencies:** M9, T8 and R1/R3 as applicable. Reuse the broker callback,
`VC/internal/pkibrokerapp/` and `VC/internal/pkitrust/registry_app.go`.

**Implementation steps**

1. Enable the App verifier/consumer in the actual broker authorization and session
   worker. Carry the existing App subject and certificate fingerprint through
   token/session checks; preserve Device authorization and topic isolation.
2. Connect broker sweep results to App receipts. A failed broker API call, partial
   session enumeration or failed disconnect must not count as installed enforcement.
3. Preserve lifetime bounds through MQTT reconnect and persistent-session handling.

**Test steps**

1. Use a real authorized App credential/token to connect, publish and subscribe;
   verify denied topics and another user's Device isolation.
2. Keep target App, unrelated App and Device sessions active; renew the target and
   demonstrate the permitted successor behavior.
3. Revoke the old App authorization; measure target disconnect and old reconnect
   denial while unrelated App/Device sessions remain usable.
4. Fail the worker, controller or broker management API separately. Required
   enforcement/receipts must remain incomplete until actual recovery and sweep.
5. Restart broker/worker with retained state and verify no revoked persistent
   session becomes authorized again. Repeat QoS/ACL canaries after recovery.

**Done when:** real App MQTT sessions satisfy lifecycle, selective cutoff and
failure/restart checks. MQTT server-root lifecycle alone does not close A5.

## 15. A6 — TURN/signaling renewal, revocation and restart

**Starting point.** Reuse `VC/internal/pkiturnapp/`, `turncontrol/`, `signaling/`
and the existing Redis authorization/session store. Local owner/atomic closure
tests exist. The TURN worker requires a dedicated relay because it denies unknown
users; do not attach it to an unrelated shared relay.

**Implementation steps**

1. Deploy the existing controller against a dedicated dev relay and its exact
   Redis/session namespace. Use the governed App verifier/consumer from R1.
2. Connect the real signaling authorization to the allocation/session owner;
   preserve certificate/subject binding, expiry and one closure transition.
3. Tie App receipts to actual signaling/relay enforcement and worker health.
   Repair only missing live integration; keep relay control outside login handlers.

**Test steps**

1. Establish a real signaling session, TURN allocation and relayed data flow using
   an authorized App; keep a second user's allocation as the unaffected control.
2. Renew the App credential and validate successor authorization without reviving
   the old fingerprint's session.
3. Revoke/expire target authorization; observe Redis/session closure and actual
   relay allocation termination within the bound. Old credentials cannot reopen it.
4. Race renewal/revocation with session setup and verify atomic closure behavior.
5. Stop controller, fail relay-control access, and make required trust/Redis evidence
   unavailable separately. Require denial by the recorded lease/freshness boundary,
   no false receipt, then restart/recover without restoring revoked sessions.

**Done when:** actual relayed traffic and its authorization lifetime are proven,
including selective termination and recovery. A healthy worker or deleted Redis
record without relay termination is insufficient.

## 16. V3 — Maintain repeatable procedures for all new paths

**Implementation steps**

1. Extend `scripts/pki-service-dev/` and `scripts/go/pki-dev-probe/`; reuse existing
   private evidence, precondition, port-forward and lifecycle helpers. Avoid a new
   generic deployment/test framework.
2. Map every M/T/R/A exit criterion to an executable check, required fixture,
   dependency, expected result and evidence fields. Include adoption, renewal,
   retirement, root changes, held sessions and scoped failures where applicable.
3. Make successful, failed, skipped and not-run checks distinct. Save mutation
   intent first, provide specific reconciliation instructions for uncertain
   operations, and clean up only resources/faults owned by the run.
4. Update runbooks with supported commands, exact dev scope, normal prerequisites,
   retained-state reuse, failure recovery and credential-handling rules.

**Test steps**

1. Unit-test material runner logic: wrong target/digest, missing evidence, uncertain
   mutation, partial enforcement and secret-free report output.
2. Run each documented path on dev; require the expected positive/negative outcomes.
3. Exercise interruption and documented recovery without duplicate issuance or
   false completion. Verify cleanup restores scoped faults and preserves unrelated data.

**Done when:** the complete checkpoint matrix is reproducible with maintained
procedures. A script that has not exercised its documented live path remains unqualified.

## 17. V4 — Full dev acceptance and final Device regression

**Dependencies:** completed functional checkpoints and V3 coverage.

**Implementation steps**

1. Freeze the intended source/image/configuration evidence for the acceptance run.
   Verify current authorities, CRL freshness and isolated fixtures before starting.
2. Use the existing procedures in dependency order; record one consolidated result
   linking their phase reports. Reuse valid evidence where scope permits; rerun
   behavior affected by later code or runtime changes.
3. Run the completed Device acceptance procedure as the final regression with its
   authorized dev fixtures. Do not use an unrequested database reset as preparation.

**Test steps**

1. Exercise all remaining-domain acceptance paths, including root changes, held
   sessions, failure/restart and restored positive traffic.
2. Recheck Device enrollment, new-key renewal/replay, acknowledgment/cutover,
   revocation, API mTLS, MQTT ACL/QoS1 and restart behavior.
3. Recheck human login, App authorization and factory admission after all fault
   cleanup. Confirm readiness and current receipts, not just healthy Pods.
4. Investigate failures and rerun the affected dependency paths. Report required
   skipped/not-run tests as incomplete; do not average them into a pass rate.

**Done when:** the consolidated full dev run and final Device regression pass,
with no unresolved required checks or unrecovered injected faults.

## 18. V5 — Final runtime, persistence and evidence audit

**Dependencies:** V4 and all functional exit criteria. This is an independent
verification pass against primary evidence, not a repetition of prior summaries.

**Implementation steps**

1. Reconcile actual listeners/callers, serving fingerprints, approved authority
   state, issuance/revocation records, running image digests and mounted trust.
2. Compare live configuration with persisted dev overlays/operator settings and
   rendered desired output. Verify key ownership/modes, seed removal and absence
   of static fallbacks without reading private material into reports.
3. Match every required receipt to the exact installed digest, consumer identity
   and completed enforcement evidence. Account for failed/pending operations.
4. Audit scoped cleanup and stale bootstrap access. Identify retained test fixtures
   needed for replay; remove only unreferenced run-owned resources.
5. Update the authoritative status table and implementation references once the
   evidence satisfies them. Publish the remaining broad-milestone list after commit.

**Test steps**

1. Use fresh read-only runtime/registry observations and check them against V4,
   rather than treating its `passed` field as sufficient evidence.
2. Verify restored configuration would select the accepted images, trust/state
   paths and consumer gates. Run a targeted restart only if unresolved drift or a
   missing persistence check requires it.
3. Confirm all 40 original IDs are accounted for exactly once, each closed ID has
   valid evidence, and no deferred production/hardware item is represented as passed.

**Done when:** there is no unexplained runtime/configuration/evidence discrepancy
and all original active-milestone exit criteria are met. Only then report this
milestone **40/40 = 100%** and proceed to backup/recovery and SDK work. Completing
this document or committing code does not itself increase the completion count.
