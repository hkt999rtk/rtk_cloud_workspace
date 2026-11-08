# OpenBao Workload Identity Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans in this session;
> a single fresh whole-branch review follows complete implementation.

**Goal:** Build new dev then staging generations with OpenBao workload identity custody.
**Architecture:** KV v2 durable state, native synchronous store adapters, private
Pod projections, Kubernetes auth/Lease, existing PKI registry and deployment executor.
**Tech Stack:** Go, OpenBao KV v2, Kubernetes, PostgreSQL, existing shell wrappers.
**Spec:** ../specs/2026-11-08-openbao-workload-identity-design.md

## Global constraints

Preserve old local keys/journals in generation archives. User explicitly authorized
removal of dev and staging Linode runtime resources and fresh creation; new
generations establish independent identities. Protect prod, CI and shared artifacts.
Do not create identities in preflight. Environment settings select all policy.
Local operation data: ~/.config/rtk_cloud/<environment>/, 0700/0600.
Independent OpenBao cold startup and original offline Root custody remain.
No live migration before isolated complete lifecycle acceptance.

## Review focus

Lost response: reconcile exact committed state, not duplicate issuance.
Old writer: deny stale owner after lease transfer, including resumed process.
CRL cache loss: preserve anti-rollback high-water across Pod reconstruction.
Rollback after renewal: newest state must reach original PVC before reuse.
OpenBao restore: reconcile KV versions with original registry and revocations.

## Tasks

- [x] 1. Update canonical Platform PKI contract and workspace custody/architecture
  sources; label accepted target separately from observed deployed state.
- [x] 2. Add versioned KV record read/write primitives to native OpenBao client.
  Tests: create-only CAS, stale version, missing/deleted data, forbidden response,
  lost write response and reconciliation, path traversal, bounded body, no secrets
  in errors. Preserve exact native state bytes; no generic filesystem mirroring.
- [x] 3. Add remote native identity backend and scoped runtime configuration.
  Test file/remote parity, unchanged original keys, pending recovery, renewed
  state commit before exposure, initial creation vs existing record absence.
  Implement owner fencing and token lifecycle using existing Kubernetes/OpenBao
  primitives. Test lease loss/takeover, concurrent update and denied foreign key.
- [ ] 4. Adapt all native client/server owners and trust/CRL stores, excluding
  openbaohost startup. Preserve cache anti-rollback and cold-start ordering.
  Test real native API/Controller/CertIssuer/worker/management profiles.
- [ ] 5. Project environment-selected backend through shared create/upgrade/
  recovery/preflight renderers. No migration CLI or copying old workload keys:
  operator superseded in-place migration with fresh dev and staging generations.
  Create-only CAS initializes scoped records only in qualified new bootstrap.
- [ ] 6. Run isolated real OpenBao/Postgres/Kubernetes full lifecycle and failure
  matrix. Controlled clock for expiration; no real TTL waits. Validate cold start,
  pending issuance/renewal recovery, owner isolation/fencing, outage and restore.
  Run focused pre-PR then equivalent full required CI and one final review;
  publish/merge leaf contracts and native changes before workspace gitlinks.
- [ ] 7. Use formal removal planner to inventory exact dev Linode resources,
  preserve local generation evidence, remove through formal script and verify
  provider absence. Never match prod/CI/shared resources by loose name.
  Full create preflight GO, formal new dev deployment and health acceptance.
  Repeat staging removal/create only after new dev acceptance. Fresh create
  must not allocate the former per-workload PKI PVCs.

## Ledger

2026-11-08: Approved implementation, original-key migration and per-service
restart; dev before staging. Primary checkout dirty: use clean existing worktree.
Moved ignored old test-run artifacts to GoBuild with compatibility symlink;
no environment operational records moved. Documentation target updated first.

2026-11-08 steering: user explicitly replaces migration with destructive fresh
creation for BOTH dev and staging. Do not develop the migration/rollback/retire
CLI from the former scope. Keep backend compatibility for other environments;
retain old local material segregated and never use it to initialize fresh PKI.
KV record RED observed (undefined methods); full openbao package -race GREEN
3.453s after implementation. Broader lifecycle acceptance remains pending.

2026-11-08 native foundation: added internal/openbao KVRecord methods with CAS,
strict paths, bounded/private errors, no credential redirects and existing token
refresh transport. Added serviceidentity.OpenRemote/RemoteConfig: exact native
pending/current state, cached TLS reads, commit-before-projection, lost-response
readback, CAS owner fence and callback guard. Added OwnerLease get/update-only
Kubernetes ownership, expiry/takeover/release, independent fast Check and renewal
loop. RemoteConfig requires OwnerToken and CheckOwner; runtime callers MUST
supply an acquired OwnerLease token/guard, NOT a permissive placeholder.

RED observed for new KV, remote store and owner Lease APIs, then full
`GOWORK=off go test -race ./internal/serviceidentity ./internal/openbao -count=1`
GREEN (latest 9.183s / 3.837s). Logs in GoBuild tmp openbao-native-foundation.log.
These unit/HTTP boundary tests are NOT isolated end-to-end lifecycle acceptance.
No live resources removed, no environment policy or identities changed.

NEXT: wire native custody configuration + acquired owner/token transport into
client/server/management/factory/MQTT bootstrap and runtime entrypoints. Server
remote wrapper/inspection still needed. Reuse pki.Store.NewServerHTTPClient
without Controller consumers for independently pinned OpenBao TLS/SQL registry
verification (avoid managed-service-client -> OpenBao KV -> managed-client loop).
Existing openbaohost OpenServer must stay local. API/worker owners enter via
pkitrust.LoadManagedServiceClientFromSettings and pkitrust.loadServerCredential;
management/factory/emqx have direct serviceidentity constructors. Include
cmd/serviceidentity-bootstrap initial client/server entrypoints and read-only
inspect/export/owner-client helpers; don't inspect only internal/ usages.
Qualified bootstrap jobs may initialize new KV state; steady runtimes require
existing records. Then wire same backend setting through create/shared renderer,
preflight and recovery, public trust/CRL state and no-PKI-PVC observation.

Current code worktree: worktrees/dev-preflight-readiness-pr; workspace, native
Video Cloud and contracts all on codex/openbao-workload-identity. Workspace main
base cd9ae6a4; native latest origin/main d91abb1 (includes OpenBao root PRs770–772),
contracts base fad5834. Workspace doc commit4effa031, contracts doc947175b.
Do not publish parent gitlinks until leaf changes are merged. Preserve huge dirty
primary checkout codex/dev-controller-identity-pins. Scope: fresh dev then fresh
staging (not migration); preserve archived local generations, protect prod/CI.
Formal remover is scripts/deploy-environment.sh remove --environment NAME
--confirm video-cloud-NAME; it captures ownership/DNS and removes provider volumes.
Don't use old generic destroy helper with loose stack fallback. Read removal
implementation before execution; no deletions before new code lifecycle tests.
Docker27.4.0 available locally, kind/k3d not installed, helm available. Plan must
run real isolated Kubernetes lifecycle tests before deleting current dev.

2026-11-08 continuation progress: native IdentityCustody settings parser and
binding now connect managed Service client/server, management sidecar, Factory,
EMQX bootstrap/runtime and serviceidentity-bootstrap. Global config/API validation
checks selected custody settings. Legacy file consumers still pass. Remote
read-only inspection/certificate helpers now validate native payloads without
claiming leases or writing KV/projections; CLI/owner-client wiring remains.
Full nine-package native wiring -race GREEN (config/pkitrust/serviceidentity/
openbao, bootstrap command, management/factory/emqx/factory command). Targeted
API/LogIngester/Broker bootstrap/incoming-root tests GREEN. Real isolated
PostgreSQL service-identity regressions GREEN 7.166s. These do NOT prove the
new KV complete lifecycle yet.

Ruling: creation KV transport must use the ORIGINAL OpenBao foundation TLS
anchor until the managed OpenBao authority is available. Strict managed SQL
server admission requires active/retiring issuers; current create enrolls managed
Service core BEFORE OpenBao transport lineages. Requiring the future managed
OpenBao Root for initial KV access would recreate the dependency deadlock.
Reuse original temporary TLS and a public generation-scoped handoff snapshot;
no new CA, no READY managed-issuer acceptance, no unconditional mutation retry.
Finish normal runtime with bootstrap authorization absent. Check installed
snapshots in live KV consumers before the OpenBao listener TLS switch.

IMPORTANT: OpenBao bootstrap CA is SEPARATE from management.TransportCAPEM.
Foundation uses baoTransport.CACert in Secret openbao-transport-tls and ConfigMap
pki-openbao-transport-ca; runtime.OpenBao.CACertPath is canonical evidence.
Do not mistakenly trust runtime.TransportCAPEM (Account/Controller/Issuer CA).
Existing OpenBao Kubernetes auth ALREADY uses rotating local projected tokens:
deployment_create_openbao.go config disable_local_ca_jwt=false, no static reviewer
JWT, openbao ServiceAccount + auth-delegator. Reuse it; do not add another auth
or reviewer lifecycle.

Creation transport prototype implemented/tested: pkitrust.CustodyTransportTrust
(schema/environment/generation/server_name/bootstrap_pem/managed_root_id/
managed_root_sha256/managed_pem/managed_only). Uses Native DynamicServerConnections,
normal TLS + independent original anchor, managed SQL/CRL verification and socket
sweeps; reads updated read-only snapshot, never trusts unknown peer Root. Unit
Root/expiry/foreign-snapshot/retired-anchor/keepalive tests GREEN. IdentityCustody
adds PKI_STATE_BOOTSTRAP_ROOT_SHA256 + PKI_STATE_TRANSPORT_TRUST ONLY for creation.
Custody-specific independent trust uses PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256,
PKI_STATE_OPENBAO_SERVER_PKI_NAME, PKI_STATE_OPENBAO_TLS_CA (NOT ordinary OPENBAO_*
provider trust keys: changing those would break temporary signing providers).
Snapshot producer/renderer/live projection handoff barrier NOT implemented yet.

Local real fixtures now live, exclusively synthetic:
- kind binary /Volumes/RTKGoBuild/tools/kind-v0.33.0/kind, official download checksum
  verified; cluster rtk-custody-20261108, Kubernetes v1.37.0 Ready.
- kubeconfig /Volumes/RTKGoBuild/tmp/rtk-custody-kind-20261108/kubeconfig.yaml (0600,
  parent0700), no global/default kubeconfig changes. Kind node Docker container
  rtk-custody-20261108-control-plane.
- Postgres Docker container rtk-custody-pg-20261108, --rm, loopback65097, tmpfsPGDATA
  1g, fixture-only trust auth. DSN postgres://postgres@127.0.0.1:65097/custody?sslmode=disable
  and actual container ID in sibling postgres.json (0600). No real cloud data.
- Real Kubernetes test TestOwnerLeaseAgainstIsolatedKubernetes GREEN 2.435s using
  RTK_CUSTODY_KUBECONFIG above: actual SA token, get/update-only named-Lease RBAC,
  contention/takeover/stale-owner/release/foreign-resource denial. Simulated clock,
  no TTL wait. Namespace cleanup completed; shared kind retained for next tests.
- Last core -race GREEN serviceidentity17.090s/config1.584s/pkitrust18.584s/
  openbao5.013s: /Volumes/RTKGoBuild/tmp/custody-readonly-core.log.

NEXT priority: complete readonly inspection/owner-client native CLI wiring,
then implement real OpenBao + SQL + Kubernetes full KV lifecycle acceptance.
Refactor existing custody transport setup for a reader without a Lease; never
invoke OpenRemote just to inspect. ReadRemoteCertificate/InspectRemote already
exist. Complete snapshot publication/namespace distribution and mount-readback
barrier around existing OpenBao serving TLS handoff. Integrate shared renderers,
scoped KV policies/roles + per-state precreated Leases/SA RBAC/network rules,
replace only logical PKI claims with emptyDir, persist CRL/trust high-water in KV,
and update observation/preflight/health/renewal accordingly. No cloud teardown
before complete isolated acceptance. Both dev/staging old cloud still intact.

Continuation checkpoint: native ab3a848 committed the connected runtime/settings/
creation transport work; contracts 2a9c54b committed fresh rollout and handoff
clarifications. Root remains doc-only commits, no unpublished leaf gitlink staged.
Latest focused native verification with real PostgreSQL and real isolated kind
used VIDEO_CLOUD_TEST_DSN and RTK_CUSTODY_KUBECONFIG from fixture metadata:
/Volumes/RTKGoBuild/tmp/custody-native-components-verified.log — PASS across all
selected packages; management/emqx had no matching tests in THIS regex, their
prior full package runs passed. No CI/preflight/deployment claimed.

Readonly follow-up WIP (after ab3a848): pkitrust.NewCustodyTransport is now reusable
without a writer Lease; CustodyBinding exposes Client/Context. serviceidentity
InspectRemote/ReadRemoteCertificate were already committed and tested. Native
CLI inspect/inspect-server and owner-client now select those remote readers;
new cmd/serviceidentity-bootstrap/custody_reader.go requires the selected
PKI_DATABASE_URL verifier reference and no state/lease mutation. Initial refactor
compile error (cancel moved into binding) corrected to b.cancel(). Final full
four-package race verification running as exec session84734; output
/Volumes/RTKGoBuild/tmp/custody-cli-reader-final.log. Check this SAME handle/log,
then commit reader follow-up if green; do not assume it passed. No other test
sessions should be active. kind/Postgres fixture CONTAINERS remain live on purpose.

Remaining major work is unchanged: real OpenBao KV/Kubernetes auth and complete
creation/renewal/restart/failure lifecycle tests, create/shared renderer custody
policies/SA/Leases/networks + trust-snapshot publication/handoff, remove PKI PVC
allocations and persist public CRL/trust anti-rollback state in KV, adapt inspectors/
preflight/health/renewal, formal CI/release then scripted dev teardown/new-generation
archive/create GO/deploy/health, then staging. Do NOT substitute old deployment
or library tests for this final state. No dev/staging/prod cloud mutation occurred.

Reader follow-up verification COMPLETE: exec84734 terminal exit0; full -race
bootstrap command35.870s/pkitrust21.772s/config1.502s/serviceidentity18.880s all
PASS in custody-cli-reader-final.log. Reader follow-up committed in native repo.
No test processes remain running; real kind and Postgres fixtures remain live.
Next continuation should start real OpenBao lifecycle fixture and shared
renderer/publisher implementation, not rerun these already passing checks.

Continuation checkpoint: native commit 3591c41 adds a REAL OpenBao provider
extension to the isolated Kubernetes owner test. Actual pinned OpenBao with TLS,
durable Docker volume, Kubernetes TokenReview and scoped KV v2 policy verifies
pending CSR/key replay, owner transfer/stale writer denial, stale CAS denial,
read-only inspection, sealed-provider fail-closed renewal, continued loaded
credential use, provider restart/unseal with original keys, revoked auth token +
replacement projected JWT reauthentication, and renewal persistence. No TTL wait.
This exposed a real Lease wire-format defect: RFC3339Nano omitted fractional
seconds at whole seconds, which Kubernetes MicroTime rejects with HTTP 400.
Acquisition/renewal now emit fixed six-digit microseconds. Regression RED ->
GREEN, real Kubernetes/provider -race lifecycle GREEN 7.458s; full serviceidentity
and openbao -race GREEN 23.235s/5.566s in custody-provider-full-regression.log.
The provider container/volume/reviewer binding were cleaned; shared kind and
Postgres fixtures remain. No real cloud mutation occurred.

Workspace now implements the public custody snapshot renderer/producer and
initial foundation publication BEFORE PKI migrations/managed owners. It uses
only the original OpenBao CA path under the original bootstrap generation,
canonical private snapshot evidence, namespace public ConfigMaps, monotonic
bootstrap/prepared/managed-only publication and interrupted-apply replay.
It will not reintroduce bootstrap admission during foundation recovery or
substitute another managed Root. Pure rendering covers dev/staging/prod;
publisher/foundation-order tests use isolated admitted dev/staging owners.
TestCustodyTrust plus existing TestCreateOpenBao/TestCreateFoundation regression
GREEN 114.727s; final original-generation binding tests GREEN 10.361s in
custody-trust-final.log. Foundational source defaults remain unchanged: backend
selection has NOT yet been activated in dev/staging configuration.

IMPORTANT remaining work: the producer is connected ONLY at initial foundation.
Hook prepared and managed-only publication into actual consumer distribution/
projection readback and OpenBao serving TLS handoff; do not claim that barrier
exists yet. Complete shared pod projection + dedicated SAs/scoped KV roles/
Lease RBAC/networks, public trust/CRL durable high-water adapters, removal of
PKI PVC allocations and backend-aware inspection/preflight/health/renewal.
Then full native pod/create/recovery acceptance, review, formal CI/release,
scripted dev removal/archive/create GO/deploy/health followed by staging.
Do not rerun already passing provider components without a relevant change.
No current tests running after these verified results; no PR/current-feature CI
or live preflight/deployment GO has been claimed.
