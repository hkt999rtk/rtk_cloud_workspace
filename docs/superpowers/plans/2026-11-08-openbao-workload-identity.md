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

Shared configuration follow-up: architecture now explicitly maintains
PKI_STATE_BACKEND=pvc, PKI_STATE_KV_MOUNT=pki-identities and
PKI_STATE_LEASE_DURATION=30s. Resolver whitelists them, preserves per-environment
architecture overrides and validates only the existing native path/Lease wire
contract before qualification. dev/staging/prod override/no-intent-mutation tests
RED unknown architecture keys -> GREEN. Focused config/resolve/shared OpenBao
baseline regression GREEN 3.289s in custody-settings-green.log. Dev/staging
OpenBao override activation remains deferred until the remaining native stores
and shared projections/creation roles/inspection/handoff are connected; do NOT
enable it now and mistake a partial implementation for a deployable generation.

2026-11-09 continuation checkpoint: shared custody projection implemented in
scripts/go/rtk-cloud/deployment_custody_projection.go. New typed workload_custody
binding holds original generation + managed OpenBao Root + logical owner/namespace/
SA. The steady renderer now invokes it (fresh dependency preview remains deferred,
with no invented future generation). It converts only bound PKI PVC definitions/
mounts to memory emptyDir, keeps paths/prefixes stable between real maintained
Service enrollment and runtime recipes, injects separate projected OpenBao/Kube
JWTs/CA and downward API fields, and precreates per-path named Leases plus exact
get/update Roles/Bindings. Per-path RBAC is additive across stages: do NOT collapse
it into one repeatedly narrowed per-SA resourceNames list. Lease declaration has
NO spec: replay preserves a live holder. Idempotent projection preserves source
recipes and business data/OpenBao cold-start; conflicting existing owner RBAC is
rejected. Account Manager namespace OpenBao role support added by explicit scoped
helper, preserving legacy role intent/readback. Additional custody namespace
OpenBao ingress is additive; existing ingress-only default does not need a new
Egress policy. Do not accidentally isolate DB/DNS/Kubernetes access.

Verification: projection/policy/core recipe parity and idempotency tests GREEN;
real isolated Kubernetes SSA/RBAC test GREEN 0.82s test time/2.225s package: holder
survives reapply; selected SA can get/update its exact Lease, cannot create or
get/update foreign Leases. Native config wire-name guard committed d39fc27, full
config -race GREEN 1.585s. Root/native use the same independently calculated
pki-state-b39c2fe9345ed9d9b6fdb41c858f540d fixture. Final all TestCustody,
TestCreateOpenBaoCompletedRole and TestCommonEnvironmentRenderer regression
GREEN 38.983s in custody-projection-network-final.log. This covers component
projection, actual API/RBAC and legacy steady rendering, NOT a full native Pod
creation lifecycle or actual NetworkPolicy packet enforcement. No real cloud
resources changed. Native current HEAD d39fc27; previous 3591c41 includes actual
OpenBao provider lifecycle and MicroTime fix. Both kind and Postgres fixtures live.

NEXT: produce the complete original owner binding from maintained Service,
application, MQTT and product identity inventory (multiple MQTT containers need
separate prefixes on the same Pod SA), establish CAS-required KV v2 + exact scoped
owner policies/Kubernetes roles before any workload enrollment, and connect the
shared projection to the formal create/recovery stage renderer. The new namespace
role helper configureEnvironmentCreateOpenBaoRoleInNamespace is available; old
wrapper remains exact legacy behavior. Do not fabricate PVC UIDs for remote state:
adapt native inspection/receipt readers to typed KV references. Public CRL/trust
anti-rollback adapters and native CLI server export still need review/wiring.
Complete prepared + managed-only snapshot publication/distribution/readback at
OpenBao listener TLS handoff. Only then enable dev/staging OpenBao overrides,
prove full isolated creation/recovery/renewal/restart, review/CI/release and perform
formal dev remove/archive/create preflight GO/deploy/health, then staging.

2026-11-09 custody initializer checkpoint: implemented createEnvironmentCustodyBinding
and configureEnvironmentCreateCustody in deployment_create_custody.go; connected
at continueEnvironmentCreateAfterOpenBao AFTER public trust publication and BEFORE
SQL migration/native managed enrollment. Runtime.Custody carries the original
binding. All enabled maintained products + core/application/MQTT inventory produces
14 logical workload owners when all six product flags are enabled. OpenBao own
cold-start identity/business storage are excluded. Environment settings and
qualified product recipes are not mutated; altered product claim mappings reject.

Ruling: group durable scope by WORKLOAD, not one prefix per legacy claim. StorageAliases
now map maintained storage purposes into the same owner: MQTT host/broker and
Log Ingester internal/registration credentials have separate path hashes under
one workload prefix. This supports multiple identity purposes inside the same
native process, which has one IdentityCustody configuration. Source recipes and
physical identity paths stay unchanged. Existing projection fixtures without
explicit aliases retain their single logical-storage mapping. Binding validation
rejects conflicting/foreign aliases; no OpenBao root claim may enter KV custody.

Initializer uses original operator token through stdin, not command arguments.
It establishes selected KV v2 + cas_required, exact generation/workload ACLs and
namespace-bound Kube roles in one checked OpenBao-Pod CLI batch, retaining the
existing native bounded auth profile. It never writes KV identity data/generates
keys. Original scope metadata is immutable canonical
 deployment/create/<generation>/custody/owners.json; one durable journal intent
and verified receipt. Pending same-scope config can reconcile idempotently; lost
response resolves by exact readback; completed scope reuses read-only. Wrong mount
type, widened/missing readback, duplicate/drifted original intent block completion.
No unseal/identity reset or unrelated mount replacement.

Verification: inventory/grouping/projection and scope pending/lost-response/read-only/
wrong-type/drift tests GREEN. REAL pinned OpenBao CLI fixture (network=none, synthetic
HTTP dev server) passes all production CLI commands, actual CAS rejection including
root caller, scoped token own-prefix write and foreign-environment HTTP403 denial.
It enters actual continueEnvironmentCreateAfterOpenBao, publishes original public
trust, initializes custody, then intentionally stops at missing migration recipe;
returned runtime has original Custody binding. This proves dependency boundary,
not a full environment create. Native prior fixture separately proves real TLS,
durable provider restart, Kubernetes TokenReview and renewal. Final all TestCustody,
TestCreateCustody, TestCreateOpenBaoCompletedRole, TestCommonEnvironmentRenderer
(including actual kind SSA/RBAC) GREEN 90.665s: custody-initialization-final.log.
Fixture setup initially missed PATH override and then required a valid CA file
for the CLI even with synthetic HTTP; corrected only test fixture, production TLS
remains strict. All test provider containers cleaned; base kind/Postgres retained.

NEXT: connect shared projection into formal create/recovery STAGE definitions
(applyEnvironmentCreateStage in deployment_create_kubernetes.go), reading original
scope/public transport evidence and preserving original generation; publish final
workload_custody binding only after genuine completed native state. Adapt native
inspection/receipts to KV references (NEVER invent PVC ClaimUIDs). Finish independent
CRL/trust anti-rollback KV stores and OpenBao prepared/managed-only snapshot handoff
with real installed projection barrier. Native server export/public-profile file-only
paths need adaptation. Product registration callers currently use static PEM transport
(serviceregistration/transport.go and optionalserviceapp, mqttfoundationapp,
shadowworkerapp, logingesterapp, otaregistrarapp); validate/adapt their cold restore
and renewal to authoritative KV rather than assume core-client wiring covers them.
Product enrollment currently exports client material into runtime Secrets and retires
the enrollment owner; preserve only permitted non-authoritative short-lived mirrors,
not init-only custody. Full matched-state backup/restore with newer committed renewal
and whole new-backend creation/recovery remain unproved. Do NOT claim full lifecycle
from these component tests or remove cloud yet. Dev/staging OpenBao overrides remain
unactivated, no current-feature CI/PR/preflight GO/live teardown/deployment. Finish
all required flow tests, review/CI/release, then scripted dev removal/archive/new
create preflight GO/deploy/health, then staging. Do not rerun unchanged verified
initializer components merely to produce more green output.

Public-cache checkpoint: native PublicStateCustody implemented in
internal/pkitrust/public_state_custody.go. Records use scoped
<prefix>/public/<sha256(original-native-path)>, schema rtk.pki-public-state/v1,
kind + environment/domain/authority binding, versioned KV CAS and exact native
state_json. Explicit Create flag only; runtime missing state fails, not empty.
Native file transition algorithms run in an isolated 0700 candidate directory
inside the workload-owned projection directory. Commit authoritative KV BEFORE
atomic projection. This reuses signature/metadata checks, expiry repair,
revocation retention, Root cumulative distrust and terminal Device denial;
it is NOT blind filesystem mirroring. Target projection advisory lock serializes
publishers; CAS conflict re-reads/revalidates rather than blindly overwriting;
lost response accepts only exact next version and payload. Native projection
also rejects regression against already installed local evidence. HTTP outage
leaves prior installed valid evidence intact, rejects new nondurable changes.

CRLConsumerConfig and ConsumerConfig now accept optional Custody pointers;
normal sync uses remote persistence before runtime installation/ACK. New
CRLConsumer.PrepareLocal restores KV evidence before normal freshness/install
checks; RegistryServerConsumer.PrepareLocal delegates to it. Root Consumer
PrepareLocal similarly restores durable evidence independently of Controller.
Legacy nil-Custody behavior stays covered. Standalone methods cover ApplyCRL,
RestoreCRL, ApplyRoot, RestoreRoot, ProvisionRoots and ObserveTerminal.

Tests: projection loss + expired CRL high-water, stale/weak CRL, lost committed
response, provider outage, Root distrust + stale re-provision, terminal issuer
surviving registry rewind, actual consumer no-ACK-before-durable-install, failed
runtime install no ACK and local Root restore with Controller unavailable.
Expiry advanced through supplied clock; no TTL sleep. Test initially expected
one runtime installer invocation; existing Sync deliberately reloads before ACK,
so corrected assertion to two, retaining original behavior. Final FULL pkitrust
and openbao -race GREEN 17.010s/3.768s in custody-public-final.log.

IMPORTANT: no app/settings loader yet supplies these Custody pointers. Consumer
registry factories still need shared settings/transport wiring, explicit qualified
initial-public-trust creation mode, Root provisioning/terminal propagation, and
all owner entrypoints. Inspect native initial-trust commands/flags instead of
silently permitting runtime reinitialization. loadServerRootTrust currently uses
file Load/ProvisionRoots before constructing Consumer; adapt restore/provision
there. registryCRLConsumer/RegistryServerConsumer factories build members in
registry_crl.go/registry_server.go; automatic Device members and terminal observer
need the same pointer. Server HTTP factories accept contexts but their lower
registry factories currently do not; thread concrete optional storage explicitly,
reuse already authenticated CustodyBinding.Client where possible and preserve
Close ownership. Bootstrap CLI initial-trust flows must seed public records
BEFORE normal runtime constructors that require them. Server export and product
registration static-PEM flows still need the previously noted integration.

No live cloud mutation, no feature CI/PR/preflight GO/deployment completion.
Dev/staging backend activation still deferred. Next work remains full create/
recovery projection + typed KV inspection/receipts, all cache/registration owner
wiring, installed TLS snapshot handoff, whole isolated flow/backup-restore
acceptance, review/CI/release, then formal dev removal/archive/create GO/deploy/
health and staging. Component tests are not completion of those requirements.


2026-11-09 native trust-owner wiring checkpoint (native e55b354): connected
PublicStateCustody through App/Device/Broker/Service/MQTT/OpenBao registry factories,
shared Service HTTP client pairs, managed Service identity owners, ServerHost,
Controller/CertIssuer provider and listener factories, Management, Factory Enroll,
API Root/CRL consumers and its MQTT transport, and Log Ingester's managed MQTT
transport. Reuses the original authenticated custody client instead of constructing
another key/owner. Registry automatic Device descendants use explicit initial
trust only AFTER native registry hierarchy validation; normal missing public
records still fail. Root provisioning restores KV evidence and persists reviewed
successors through CAS before projection; stale files cannot restore withdrawn
anchors. Existing file backend remains supported.

Ruling: the native API incoming-root-bootstrap admission is ALSO durable public
state. Its previous file lived on the removed PKI PVC; leaving it file-only would
lose original admission or repeat initial trust after Pod recreation. Moved its
format into pkitrust.IncomingRootAdmission without changing fields, storing it
under the same public KV path/schema with kind incoming-admission. Exact original
Root/bundle/Service identity binding and consumed PolicyPath advance monotonically;
replayed initial commands cannot clear consumption. Native initial-trust commands
use an installed-private-key-only loader with separate public initialization;
ordinary runtime does not implicitly enroll identities. API restores policy before
TLS construction and can restore consumed admission/policy without Controller
requests. This adds no preflight setting/value generation.

Tests: registry App/Device actual SQL + KV provider floor/projection-loss tests
GREEN 3.545s; reviewed App Root provisioning tests now run file + KV and verify
successor durability. Server Root factory restoration checks service/mqtt/openbao_tls
with stale provisioned overlap and Controller unavailable. API incoming admission
file + KV in App/Device tests GREEN 7.152s; cold restart loses both files, restores
original policy, makes no new first-policy request or premature ACK. Admission
store rejects runtime creation, original Root replacement and consumed rollback.
Initial new factory fixture used noncanonical Root ID; corrected fixture to UUID.

Full -race + real isolated Postgres: pkitrust95.437s, apiapp40.926s,
pkibrokerapp8.892s, pkicontrollerapp33.133s, certissuerapp24.539s,
factoryenrollapp11.473s PASS in custody-runtime-wiring-race.log. That combined run
found a real pre-existing Manager.Current vs successful renewal race on fallback
fields, reproduced by new TestManagerCurrentDuringRenewal (-race RED 5.277s).
Fixed with short credential snapshot locking, separate from the mutation/HTTP
mutex so TLS callbacks cannot deadlock their own issuer request. GREEN full
serviceidentity15.958s, pkimanagementapp4.777s, mqtt11.083s,
logingesterapp1.746s (-race -p1 with real SQL) in custody-renewal-final.log. MQTT's
combined-run 3-second CONNECT deadline failed under concurrent DB package load;
isolated exact test passed9.76s, full package passed11.083s without changing
production behavior or relaxing deadlines. Log Ingester's getter incorrectly
returned empty for custody environment fields; now passes through os.Getenv.
No tests remain running, no live cloud writes.

NEXT: formal create/recovery STAGE projection, typed KV inspection/receipts,
prepared/managed-only TLS snapshot publication with installed-file barrier,
server export/public profile and product registration cold restore/renewal. Product
static PEM consumers are not yet converted; do not claim all workload custody is
done. API incoming admission and core native trust graph are now wired, superseding
the prior note that no loader supplies custody pointers. Full isolated environment
creation/recovery, matched backup/restore with newer renewal, whole-branch review,
CI/release and real dev/staging fresh rebuild remain outstanding. Do NOT delete
cloud yet. Environment backend overrides still unactivated; no feature preflight
GO, current-feature PR/CI/release or deployment completion.


Formal stage projection checkpoint: projectEnvironmentCreateCustodyStage is
now called by actual applyEnvironmentCreateStage BEFORE child inputs/bindings
hashing or compilation, with already frozen original child inputs loaded first.
After KV initializer completion, continueEnvironmentCreateAfterOpenBao carries
the updated original runtime context into subsequent stages. Workload PVCs become
temporary volumes using the SAME original scope/Lease wiring as steady rendering.
Foundation, business and public-only stages do not acquire a new custody dependency.
An owned stage requires original completed scope readback and original public
transport metadata, not fabricated PVC UIDs. Recovery may reload the exact scope
from its canonical owners.json without inventing new generation/owners.

Ruling: reuse the existing frozen child plan for the stage's original TLS mode;
do not create another stage-projection journal/file. A global bootstrap -> managed
TLS handoff must not change already compiled early stage bodies during replay.
Select the original bootstrap/managed pin pair from frozen desired containers,
validate against original CA/current immutable managed authority, and retain that
pair. New normal stages use managed-only settings with bootstrap fields empty.
The advancing public snapshot remains available to native early transports.

Verification: component projection dev/staging, pending provider setup rejection,
canonical scope reload, mount drift, original TLS binding, frozen-stage replay
after handoff and new managed-only stage checks PASS. Actual native stage wrapper
rejects missing custody setup BEFORE Kubernetes writes/leases; existing native
wrapper replay/known-owner/qualification regression remains green. First projection
fixture changed maintained settings before its unrelated synthetic admission;
corrected fixture boundary, leaving production exact qualification unchanged.
Latest root suite PASS29.279s in custody-stage-final.log; prior base projection/native
wrapper suite PASS30.784s. No repeated real provider tests needed for unchanged
initializer/SSA algorithms. No real cloud mutations, backend activation, feature
CI/release/GO/deployment yet. Full isolated lifecycle is still required before
removal; continue with typed KV receipts, product owner lifecycle, TLS distribution
barrier and native server export. Finite legacy dev public-profile repair is not
a fresh-create dependency: keep its original PVC contract and reject remote use
instead of inventing an unrelated historical repair migration.

Native server-export checkpoint (native18ec7d8): production export opens ONE
independently admitted custody transport using the already-open selected PKI
registry. Server and final Service-client readers share it; they never claim a
writer Lease, mutate KV or create private projections. Existing native public
snapshot-before/after plus every-DNS registry admission remain. The private export
retains the exact committed certificate/key and is still consumed only by the
protected deployment mirror adapter; no key enters public evidence.

RED: new remote export test failed on unsupported explicit reader parameter.
GREEN full bootstrap-command -race32.667s. Test removes both original file states,
reads exact committed synthetic KV states, verifies unchanged cert/key, rejects
non-GET operations and unknown paths, asserts no projection/owner lock is created,
and rejects a pending renewal committed during registry verification. Final whole
command package -race28.175s in custody-export-final.log after adding explicit
legacy-profile backend guards (their new test observed RED then GREEN).
Finite historical dev public-DNS repair now rejects OpenBao custody before private
state/registry access; fresh environment creation already uses maintained DNS
policy. It does not get an invented migration path or rewrite old repair rules.
No tests remain running and no real cloud state changed.

Remaining: product registration owners still use static PEM/mirror lifecycle and
need actual KV cold restore + renewal; typed KV identity inspection/receipts must
replace applicable PVC assumptions; publish prepared/final TLS handoff and verify
actual installed snapshots before listener swap; complete isolated whole lifecycle
and matched recovery, review/CI/release; only then formal fresh dev then staging.

Product native-owner checkpoint (native7fd2487): added
runtime.RegistrationTransport to compose existing pkitrust.ManagedServiceClient
and the unchanged serviceregistration protocol. With backend pvc/empty it calls
the same PEM constructor. With openbao it requires an INSTALLED original private
record, selected PKI registry and explicit registration identity settings; no
bootstrap enrollment/file fallback. Existing DB pools are reused; client-only
registrars may open configured cfg.DB.DSN without migration. Cleanup stops native
renewal/Lease/provider before closing an owned DB pool. Optional services have
explicit Close and Run cleanup. MQTT foundation/Shadow reuse this identity and its
public custody for MQTT server trust; Log Ingester keeps its separate internal
Service identity and registration credential under the same workload scope.

IMPORTANT registration ACK ruling: keep actual ready-heartbeat capture/ACK in
serviceregistration.bootstrap_ack. Do NOT let merely loading a managed key ACK an
unready product. Registration native manager uses Controller as its private
management endpoint and CertIssuer as renewal endpoint, with BootstrapSessionID
empty. serviceregistration.NewMTLSHTTPClientWithIdentity accepts the owner's
current credential callback and preserves legacy TLS1.3/per-request connections
and bootstrap identity capture. This preserves existing registration protocol
behavior; package serviceregistration itself does not obtain/store a key.
All applicable callers now use runtime.NewRegistrationTransport: optionalserviceapp
(webrtc/video-storage/ota), mqttfoundationapp, shadowworkerapp, logingesterapp,
otaregistrarapp. Private key file paths remain only for legacy backend.

Native runtime inputs to emit from original environment creation facts:
 PKI_REGISTRATION_IDENTITY_STATE
 PKI_REGISTRATION_ROOT_SHA256
 PKI_REGISTRATION_CONTROLLER_URL
 PKI_REGISTRATION_CONTROLLER_SERVER_PKI_ROOT_SHA256
 PKI_REGISTRATION_CONTROLLER_SERVER_PKI_NAME
 PKI_REGISTRATION_CONTROLLER_TLS_CA
 PKI_REGISTRATION_RENEWAL_URL
 PKI_REGISTRATION_RENEWAL_SERVER_PKI_ROOT_SHA256
 PKI_REGISTRATION_RENEWAL_SERVER_PKI_NAME
 PKI_REGISTRATION_RENEWAL_TLS_CA
(optional existing ServerHTTPTrust fields are supported). Runtime config uses
cfg.IdentityCustody and cfg.Env, not inferred backend/settings. State subject is
exact service:mqtt/shadow/ota/video-storage/webrtc/logger. New manager is installed
only and automatic renewal uses the existing native machinery. These fields are
NOT YET emitted by root rendering: no new backend is enabled/live.

Tests: dynamic registration TLS identity rotation passes with BOTH cert/key files
removed, accepts successor in memory, denies after owner callback failure; same
shared constructor retains all existing ready-ACK tests. RED undefined constructor
then full serviceregistration -race2.237s. Runtime remote missing settings/unknown
backend fail before file fallback or DB/network; RED undefined owner helper, GREEN
runtime1.721s/serviceregistration2.515s. App wiring full -race -p1 passes runtime1.440s,
serviceregistration1.917s,optionalserviceapp1.616s,shadowworkerapp1.509s,
logingesterapp1.565s in custody-registration-apps.log. MQTT foundation/OTA registrar
have NO test files (compiled, not an end-to-end claim). No SQL DSN was supplied for
this last app suite; earlier core graph/renewal tests used real isolated Postgres.
No tests running, all native changes committed, root gitlinks still not staged.

NEXT concrete root work:
- Product steady Deployments currently mount only exported PEM Secrets, so the
  shared custody projector cannot discover a bound identity volume. Add the
  maintained product identity mount/state path and the above managed settings
  BEFORE shared projection, using original registered Service Root/Controller/
  CertIssuer facts. Never set policy values from the script. A small optional
  Service Root reference on workload_custody (or equivalent original binding)
  can carry the public fact into steady rendering. Runtime.ServiceCeremony has
  the original registered Service Root during create; update context after it is
  assigned. Existing pki-create-service-trust ConfigMap key root.pem is public
  trust, mounted at environmentCreateServiceTrust=/run/pki-service. Controller
  origin is https://pki-controller.<stack>-video-cloud.svc:18446; CertIssuer is
  https://certissuer.<stack>-video-cloud.svc:18445, matching existing maintained
  recipes. Use shared helpers and existing namespace/DNS contracts.
- root Product outputs STILL require SecretUID + ClaimUID and read raw local
  state/private mirrors. Replace applicable NEW-backend assertions with typed
  original KV scope/key references and native read-only public inspection; do
  NOT fake ClaimUID. Historical bootstrap receipts may pin their initial observed
  certificate, but ongoing health must accept a genuine newer committed renewal.
  Decide whether to omit unused private output Secrets for the new backend; the
  registration runtime no longer needs them. Preserve legacy backend behavior.
- Final completed bindings must attach Custody + original managed OpenBao Root
  and any Service trust reference before the normal renderer runs; currently
  completeEnvironmentCreateBootstrap re-observes bindings and does not attach it.
- TLS prepared/final snapshot publication + installed projection barrier remain;
  full isolated new-backend lifecycle/recovery/backup test, final review/CI/release
  and formal dev then staging teardown/create/health still outstanding.
No real environment config/identity/data changed, no cloud resources removed,
no current-feature PR/CI/release/preflight GO/deployment yet. Do not delete cloud
until complete isolated acceptance. Existing root/native feature worktree only;
primary checkout remains untouched.


Root registration/rendering checkpoint: workload_custody now includes optional
service_root_sha256. Original Service ceremony populates that fact/context after
core enrollment; shared registration projection adds each maintained product's
original /var/lib/pki-service/private/client.json purpose, same bound workload
scope and public Service trust ConfigMap mount before normal custody projection.
Product workload names use a shared mapping with owner inventory. Existing env,
UID/GID and unrelated state are preserved; rerender is idempotent. Actual final
native completion attaches original Service/OpenBao Root references to bindings
before normal renderer use (including re-observation branch). Completion cannot
substitute the initialized generation/owners or a foreign/unactivated authority.

Found and fixed a deterministic-plan defect: custody downward env fields were
appended by map iteration, yielding different equal-input resource hashes and
sporadic frozen-stage mismatch. Existing frozen handoff test observed RED for
staging; use fixed field order. Added 32 fresh-from-source renders per environment,
not merely reapplying an already ordered result. New public fields keep legacy
JSON unchanged when absent.

Inventory/preflight correction: logical owner inventory is now independent of
allocated generation. It ignores separately qualified issuer/policy allocation
metadata when comparing storage intent (the old compare would reject actual
qualified product records), while retaining exact subject/claim/service/instance
checks. Device/App trust-cache PVC aliases now join their native API scope. Fresh
openbao recipe removes workload identity/cache names from Bootstrap.Claims;
independent bootstrap state, OpenBao cold-start identity and MQTT business data
remain (3 entries in that PKI recipe, NOT 3 total environment PVCs). Skip the
separate cross-Pod cache-chown Job for temporary cache volumes; per-Pod native
initializers remain. Dependency qualification recognizes the chosen KV producer
without demanding the excluded physical claim. No preflight generation/key/state
allocation was added.

RED observed for registration Root binding/workload mapper, completion adapter,
claim selector and full remote dependency contract. GREEN full relevant root
custody/inventory/steady renderer/dependency tests31.994s in
custody-registration-preflight-final.log. That includes actual full dependency
catalog with all five fixture product producers and remote bootstrap plan; no
kubeconfig/custody/material allocation is produced. Earlier renderer/inventory suite19.291s; registration/steady bindings suite10.078s. These are isolated
component/contract tests, not complete cloud bootstrap or real whole environment.

Native registration validators now match selected backend: remote profiles do
not demand unused PEM files and still validate complete custody; legacy profiles
keep file requirements. New profile table observed RED for mqtt/shadow/optional/
OTA registrar, then full -race config1.524s/runtime1.541s/serviceregistration1.935s/
optional1.647s/shadow1.512s/logingester1.556s PASS (foundation/registrar compile only).
Native managed registration uses the declared public Service trust projection,
not stale private-delivery mirror CA. Added config-map documentation and safe
pvc-default example fields; no real environment override changed.

NEXT highest priority: typed KV proof/receipts/inspection in actual creation and
normal health/preflight; current product receipt still asserts physical ClaimUID
and reads raw file/mirror. Do not fake a UID. Runtime no longer reads the PEM key
mirror, but existing bootstrap mirror publication remains compatible until the
new typed adapter resolves that lifecycle. Root stage context now covers products
with declared workload name even before adding their identity mount. TLS prepared/
managed-only publication and installed-snapshot barrier still missing; complete
isolated full create/renewal/recovery/backup acceptance and final review/CI/release
remain mandatory before real dev then staging removal/create/health. No real
cloud writes, no feature GO/PR/CI/release/deployment and no backend activation yet.


### Checkpoint: original KV references, frozen recipes and formal access order

The operator explicitly confirmed staging also rebuilds from scratch. Dev health
acceptance still precedes staging teardown. No cloud deletion/create/deployment,
backend override activation or feature publication has occurred at this checkpoint.

Ruling: replace physical claim authority only for the selected openbao backend;
retain original workload generation, credential Root, namespace, service account,
logical record/path and native registry verification. Never synthesize ClaimUID or
pin a mutable KV version. Added environmentCustodyStateReference and shared record
key derivation (same native Lease wire golden). The actual protected server export
adapter now uses those references, accepted child recipe/reader UID, before/after
Pod and ReplicaSet fences, and the native committed-KV exporter. Legacy PVC export
remains covered. KV export observation stores client/server references instead of
claim_uid and never stores private export material as public evidence.

Frozen product preparation/history checks reconstruct the shared custody projection
from their reviewed original child inputs instead of comparing a former PVC recipe
with an already projected deployment. Product enrollment owner comparison now uses
the accepted desired body. This DOES NOT yet finish product receipt conversion or
current renewal inspection; those still require the next adapter work.

Isolated formal compilation exposed two integration omissions: Lease was absent
from the common planner/access/inventory mappings, and precreated Lease/RBAC/SA
configuration could be ordered after a bootstrap consumer. Added native Lease
support and exact explicit consumer dependencies; existing required access owners
move before the consumer without changing their settings. Retirement retains its
accepted body and does not mint replacement access resources. Native Lease liveness
fields are ignored only for a workload Lease declaration that owns no spec; resource
UID, scope labels and all other configuration remain fenced. Native holders/renewals
are not overwritten. Lease resources remain excluded from generic retirement.

RED observed: undefined typed references; historical-recipe helper; real KV export
wrongly demanded PVC; formal compile rejected Lease; preexisting SA caused later-
phase dependency; renewing original workload Lease caused plan drift. Targeted KV
adapter GREEN20.241s, common projection/history/access/inventory GREEN34.147s.
Broader root regression including the actual isolated Kubernetes fixture is pending
at this entry. These are not whole-environment deployment or preflight GO claims.

The full internal planner suite ran367.264s and exposed an existing 250ms timeout
fixture firing before the intended durable namespace checkpoint on external-disk
fsync, plus an incomplete new Lease test fixture missing managed metadata. Fixed the
new fixture. Reused the existing qualificationDeadlineContext to inject cancellation/
deadline at the intended wait checkpoint, preserving actual cancelled-parent cleanup
and completed-journal assertions without a wall-clock wait or production timeout
change. Focused final verification is pending at this entry; do not claim full suite
PASS until recorded. Test artifacts/logs remain isolated under /Volumes/RTKGoBuild/tmp.

NEXT: convert product storage receipts and genuine current-renewal observation,
backend-aware independent identity/CRL readers and placement checks, prepared/final
OpenBao trust publication and installed-snapshot barrier, full isolated create +
renewal + restart/recovery + matched backup restore, final whole-branch review and
CI/Release. Only then run approved formal dev removal/archive/create GO/deploy/health,
then staging. No prod/shared/CI resource deletion is authorized or performed.


Final verification for the above checkpoint: broad root readers/projection/product
protocol/access/inventory regression PASS374.031s on external-disk scratch. Then
new typed product receipts were connected to actual enrollment, target resolution,
normal session-forward storage validation and restoration from the frozen program's
custody binding. Initial private projection must match native committed KV inspection
before and after the read. No synthetic physical ClaimUID is produced. Runtime
current-renewal/output observation remains unfinished (verifyProductIdentityOutputs
still follows legacy mirror/claim checks and must dispatch by backend next).

RED observed for missing product Custody receipt field. Final combined relevant root
regression PASS61.147s using isolated owned APFS RAM scratch, including new receipt
scope rejection, actual isolated Kubernetes SSA/RBAC, both storage export adapters,
original/frozen preparation recipes, existing product protocol, common permissions
and parallel inventory. The finite historical independent reader test skipped when
its external helper was not selected; do not confuse this with whole-environment
acceptance. Earlier focused product/export regression also PASS186.445s on disk.

Internal planner full suite initially exposed the described timeout fixture issue;
focused corrected cleanup/Lease tests PASS27.585s, then complete package PASS6.230s
on RAM scratch. The redundant ongoing disk run was intentionally terminated after
that full same-source RAM run passed. No production timeout or fsync implementation
was weakened. The owned synthetic test root is
/Volumes/RTKGoTestRAM/kevinhuang/tmp (0700), a newly attached 1 GiB virtual RAM disk,
APFS mounted with ownership enabled. Its volume root uses ordinary sticky temporary
storage permissions, with the actual user test root private. It is disposable and
must never contain real environment data or be assumed present after restart.
GOCACHE remains /Volumes/RTKGoBuild/cache. Future isolated tests may use this verified
RAM root for GOTMPDIR/TMPDIR while retaining result logs on the external build disk.
Original live configuration and canonical ~/.config/rtk_cloud stores were not moved.

### Independent current identity admission checkpoint

Native commit 6c7431a adds inspect-admitted: exact committed KV current identity,
original SQL issuance/issuer/root, current CRLs and distrust; permits a pending
renewal that retains the same admitted current certificate. It neither projects
private files nor obtains a writer Lease. Actual PostgreSQL race tests passed
3.273s; full native CLI race package passed5.813s. An initial full run correctly
rejected a wheel-group synthetic temporary parent; corrected only the owned RAM
scratch directory group to staff, with no production permission relaxation.

Root adapters now run this observer through a bounded temporary Job, reusing the
original owner's scoped service account and public trust. It fences the original
Job and Pod UIDs, validates bounded public output and deletes only the original
Job with UID preconditions. Lost create responses reconcile the exact name without
replaying allocation. Product output verification uses current admitted KV state
rather than decoding stale enrollment mirror keys or comparing renewed certificates
to the first bootstrap fingerprint. It retains original mirror UID checks while
that delivery resource remains referenced. Accepted original enrollment plans and
journals supply the original profile even after enrollment workload retirement.

Required committed-identity-inspection-v1 capability is added to the existing
bootstrap capability probe only for selected openbao backend; PVC capability rules
and the number of Docker probes are unchanged. Fixed create/write commands to use
the same RTK_CLOUD_KUBECTL selector already used for reads, discovered by the actual
execution contract. Canonical public transport snapshot validation is shared with
the creation-stage restorer; no new local operational path was introduced.

Observed RED: missing native admission helpers, old product mirror/PVC gate,
conditional capability support, inconsistent kubectl tool selector, and a lost
successful delete response being treated as failed cleanup. Cleanup now reconciles
exact absence without replaying deletion against a replacement. Combined relevant
root regression before that last cleanup case PASS45.346s, with isolated Kubernetes
fixture selected. Focused final cleanup verification is recorded in its retained
log. Native/root tests do not establish whole environment readiness.

NEXT: actual memory-backed observed-binding renderer/permission/CRL roundtrip,
prepared/final transport publication and mounted-snapshot barrier, full isolated
create/renewal/cold-recovery/matched-backup flow, final review and CI/Release. Only
then approved dev removal/archive/create/deploy/health, then staging. Neither cloud
environment has been removed, nor have backend settings been activated.

### Memory-backed observed bindings and transport handoff checkpoint

Root c01f8410 committed the independent admitted reader adapters. Final focused
reader cleanup/product observation PASS5.804s, including exact-name reconciliation
of a successful deletion with a lost response. Native 6c7431a remains unchanged.

Added an actual projected-memory Controller observation -> full renderer -> PKI
transition roundtrip, instead of attaching a custody field to legacy PVC fixtures.
RED exposed the old PVC initializer during observation. The projector now removes
only the named obsolete PVC permission recipes from selected custody owners;
remote native stores create their own private subdirectories, and the explicit
non-root runtime owner remains required. Shared placement checks bind original
namespace/environment/generation/auth role, writable Memory emptyDir, canonical
paths and unshadowed complete mounts. Ordinary PVC profiles keep their original
initializer and live permission checks. Independent KV admission remains a separate
proof; memory placement is not a substitute for it.

Core API and LogIngester MQTT transport now use that selected storage predicate.
RED exposed remote API permission preparation still requiring a physical claim;
backend-aware preparation no longer fabricates a claim or requires old private
files. Genuine remote MQTT worker/API rebinding exposed two more PVC-only branches:
native identity claim detection and current worker cache placement. Adapted these
to their original scoped identity + memory cache while keeping public trust owner
checks, delivery Secret references and existing accepted affinity. No new node
placement/replica policy or environmental value is introduced. Focused existing
Controller/core/log/projection regression PASS11.008s; live CRL placement/shared
maintenance regression PASS19.476s. Current/managed MQTT legacy regression passed
4.392s before the added remote current-placement case; final combined check follows.

The typed live CRL view now retains namespace, service account and emptyDir.
Its common placement adapter accepts the explicit remote profile and rejects
foreign environment/role, disk-backed caches, read-only/subPath/shadow mounts;
legacy PVC admission stays intact. This is placement only: full remote public
cache readback and maintenance writes still need committed KV-aware adapters.
Do not treat this step as complete CRL renewal/health acceptance.

OpenBao Root distribution now publishes overlapping custody transport trust before
provider consumer rollout; managed listener cutover rechecks active clients'
mounted snapshot first. Successful managed-host readiness is followed by managed-
only publication and the same bounded original-Pod projection check. It verifies
unchanged Pod UID/spec, namespace, original SA/owner scope and public mount. It
never polls Controller HTTP or requires a stopped container to start. Existing
selected rollout timeout bounds convergence and parent execution remains fenced.
RED observed undefined barrier, then a wrong preexisting sortedMapKeys map type;
corrected without a new generic utility. Focused barrier/trust/placement GREEN1.757s.
Formal fresh full creation/recovery still requires its whole-flow test below.

Ruling: retain already accepted MQTT self/enrollment affinity while adapting remote
cache placement — this is existing placement input and changing it is unnecessary
for removing PKI volumes — at worst it retains an unnecessary same-node preference;
it does not allocate a PKI PVC or prevent the existing self-affinity first Pod.

NEXT: finish remote committed public CRL cache inspection/maintenance writes and
shared identity/preflight/health observation (currently independent native reader
is wired for product completion, not every core gate). Run whole fresh isolated
OpenBao/PostgreSQL/Kubernetes create -> renewal -> interrupted recovery -> cold
restart -> matched backup restore, then final review and native/contracts/root
CI/Release in dependency order. Only after those pass, activate selected backend
for a new generation and run formal dev remove/archive/create GO/deploy/health,
then staging. No cloud resources or live settings were changed in this checkpoint.

Final focused placement/handoff verification PASS19.836s on the final source,
including the selected isolated Kubernetes SSA/RBAC fixture, independent reader,
original/frozen stage projection, core/log/controller permissions and current/
managed MQTT compatibility. The added current worker fixture initially iterated
both Service and Deployment objects that share a name; corrected the fixture to
inspect only actual Deployments. Earlier core rebinding/current placement REDs
still exercise the production PVC-only defects; no provider rule was relaxed.

Extended the full observed-Controller renderer test to an originally admitted
bootstrap-snapshot client as well as a pure managed client. RED proved ordinary
rendering unnecessarily cleared PKI_STATE_BOOTSTRAP_ROOT_SHA256 and
PKI_STATE_TRANSPORT_TRUST, making the identity transition reject its own source.
The projector now retains the already admitted transport recipe and original
optional managed pin, checking the original owner scope, snapshot path and bound
managed authority. Managed-only public snapshot publication removes bootstrap
admission; erasing the historical client configuration is not necessary. No
identity-comparison bypass was added and no environment settings were changed.

The first broader historical regex accidentally selected real signed expiry
nightly tests. Interrupted that run explicitly (423.737s; no completed test failure
before interruption), rather than waiting for all old expirations. The maintained
PR selector RTK_SKIP_HISTORICAL_WALL_CLOCK_EXPIRY=1 is now used for its replacement;
nightly validation is unchanged. That replacement broader regression is still
running at this entry and is not a PASS claim. The focused final19.836s run above
contains the latest transport-recipe correction; retain this distinction if the
older broader binary completes later.

### Committed public CRL operator and shared observation checkpoint

Previous goal turn is progress: root commits c01f8410 and 858d8483, with final
focused adapters PASS19.836s. The broader PR-mode regex subsequently hit the Go
command's default 10m package timeout (600.795s) in the existing
TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints. Stack evidence shows its
explicit signed-expiry timer in newHistoricalManagedTransportSourceExecutionContract
and two earlier signed-expiry waits; it had no completed functional failure before
that timeout. 829 test/subtest passes and3 skips were observed. This is NOT a whole
package PASS. Do not repeat an undersized default-timeout broad historical run;
use the repository CI/nightly budgets when that validation is required. Retained
log: custody-memory-handoff-pr-regression.jsonl. No cloud operation ran.

Native c97d455 adds PublicStateCustody.InspectCRL and inspect-public-crl /
apply-public-crl commands. Inspection is GET-only, validates original KV kind/scope
and signed native state, and creates no projection. Public repair reads the exact
current fresh registry record, updates existing KV with native CAS/anti-rollback,
then reads committed state back. Missing state is not recreated. No key generation,
identity Lease, signing or ACK. Actual isolated PostgreSQL verifies registry input
selection; controlled clock tests repair an expired old cache. Native full race
packages: serviceidentity-bootstrap PASS6.303s; pkitrust PASS12.689s. Initial tests
observed missing functions; then GREEN, including corrupt metadata/signature/scope,
foreign issuer, stale input, no-local-projection and missing-record cases.

Root now has typed public CRL references (original generation/owner/namespace/SA,
mount/public key/state path/domain/issuer digest), using the same owner resolver as
private identity references, with no fabricated claim UID or KV-version identity.
The shared Job executor also supports a bounded keepalive helper with streamed
native public input/output. It reuses original workload auth, verifies Job/Pod UID,
body and owner chain around exec, caps output, and keeps exact lost-response and
foreground UID cleanup semantics. There is no extra public-input ConfigMap or
large JSON in arguments/environment. Only public write candidates use memory.

Actual maintenanceRuntime.install dispatches remote owners to that original-scope
helper before reading/statting the faulted application. RED proved the old private
projection dependency. The final execution test exercises this actual dispatcher
with an unavailable application, original controller metadata and existing private
bootstrap/snapshot facts, and proves stdin transfer/committed readback/cleanup.
Initial helper fixture lacked its explicitly frozen kubeconfig; fixed the fixture,
not the production identity guard. Public interpreter/foreign ownership/output
failures are sanitized and replacement helpers are protected.

Common target observation now restores generation custody facts: exact completed
bootstrap/bindings.json plus managed snapshot for upgrade; admitted initialized
create context plus registered Service Root before that file exists. Public TLS
re-observation must not drop custody. Current resource ownership/behavior is not
replaced with historical binding data. Missing upgrade evidence fails without
regeneration or fallback. Tests cover original scope restoration, preserved current
UID facts, missing completion evidence and in-progress creation.

Added committed-public-crl-maintenance-v1 to the existing OpenBao-only capability
list (no additional Docker probes or latest-release comparisons). RED showed a
missing feature incorrectly passed before allocation; corrected it. Legacy PVC
requirements remain unchanged.

Final selected root regression PASS42.811s, including the isolated Kubernetes
SSA/RBAC fixture, all new custody references/helpers/current observation, bootstrap
capability allocation fences, product receipts, ordinary core/controller/log/MQTT
compatibility and existing maintenance tests. Earlier public helper execution
PASS10.886s and actual maintenance dispatch PASS11.860s. These are component and
adapter proofs, not whole fresh creation/recovery/health or preflight GO.

Ruling: a KV record's authority is the original generation/owner/namespace/SA and
current verified Kubernetes controller ownership chain; do not turn the creation
Deployment UID into a new immutable identity key — ordinary upgrades can retain
logical custody while current Kubernetes facts advance — stale current operations
still fail UID checks around execution and never delete a replacement helper.

NEXT (highest priority): remote CRL discovery/public export verification, recovery
ordering (online OpenBao TLS CRLs must be repaired before a KV helper needs managed
TLS), and maintenance consumer identification when the application cannot boot.
Use original admitted enrollment evidence for expected subjects; do not guess from
Pod display names or require a private projection/healthy TLS before repair.
Then wire independent committed identity/CRL proofs through every core preflight/
health reader. Actual fresh creation/renewal/interruption/cold restart/matched
backup test still required, then final one whole-branch review, dependent native/
contracts/root CI/Release, approved formal dev remove/archive/create GO/deploy/health,
then staging. Cloud resources and live backend settings remain unchanged.


2026-11-09 continuation: expected CRL consumer discovery now uses original
admitted client purposes (subject + state path) in the owner scope and completed
bootstrap bindings, not a failed application's private identity projection or a
healthy managed OpenBao TLS connection. The maintained transport recipe table
is shared with the inventory; exact native core/application/transport enrollment
Jobs are compared in the test, including exclusion of OpenBao cold-start state.
Subjects are expected consumers only, never proof of key possession or an ACK.
Actual Kubernetes owner chain, current container, namespace, service account,
selected mount and original auth role remain checked. Missing original metadata
fails rather than guessing or using another source.

RED observed: inventory omitted service:pki-controller's real purpose; the actual
remote maintenance adapter attempted to exec private state in the faulted app;
binding validation accepted a relative identity path. Focused GREEN 2.235s after
repair. Added cases for unregistered paths, foreign SA/mount/role, replaced owner,
and missing original generation evidence. A broader regression exposed an old
test fixture hashing raw JSON rather than the journal's canonical JSON; fixed
that fixture with the production canonical hash helper, without weakening checks.

Ruling: freeze expected client purposes when a new generation is admitted — these
are the same native enrollment recipes, not new environment settings or observed
key-possession claims — allowing an inferred or current private-file fallback
would recreate the transport/app-startup recovery cycle.

Remaining: public export discovery/committed readback, offline/online/remote CRL
recovery order, core preflight/health reader wiring and the isolated whole fresh
lifecycle/failure matrix. No cloud deletion/rebuild, environment backend activation,
CI publication, complete preflight GO or live deployment is claimed here. Fresh
dev acceptance still precedes the already approved fresh staging generation.

Final selected root regression for this checkpoint PASS38.809s, including the
isolated Kubernetes fixture, original-generation scope/stage checks, custody
inspection/install/discovery and ordinary core/controller/log/MQTT maintenance
compatibility. Log: /Volumes/RTKGoBuild/tmp/custody-consumer-discovery-regression.log.
This verifies components/adapters, not the pending whole fresh lifecycle.


2026-11-09 next checkpoint: actual maintenance convergence now restores local
Root caches before online provider recovery, installs local online caches before
remote online caches, then installs remote Root caches. Discovery records the
selected remote backend; array order no longer schedules a KV reader before its
provider transport. RED reproduced both a remote Root and remote intermediate
being attempted before the local provider installation; GREEN uses the real
converge/recoverOnline adapters, native-helper process/installation boundaries and
signed fixture records (no new dependency callbacks just for tests).

Convergence also reads installed remote CRLs with independent committed native
inspection rather than cat of a private app projection. RED reproduced the last
private-file dependency after installation. The actual adapter test now completes
remote install + committed readback + exact native registry ACK query with the
application private-file/stat paths forbidden. Missing ACK is not substituted by
KV commit. Focused maintenance/online/recovery tests PASS16.444s; log
/Volumes/RTKGoBuild/tmp/custody-recovery-convergence-green.log.

Remaining priority: runtime public-export discovery still assumes a PVC and
reads the export before recovery. Bind that to original remote cache metadata,
then require committed readback plus actual public reload/readiness at acceptance.
Review local provider discovery when its own container cannot boot as part of the
whole recovery matrix. Core preflight/health readers and whole fresh lifecycle,
final fresh review, dependency CI/Release and actual dev/staging script operations
remain incomplete. No live environment changes were made in this checkpoint.

Selected root regression after the recovery-order and committed-convergence
changes PASS41.094s, including isolated Kubernetes custody projection, all custody
adapters and existing CRL maintenance/online runtime tests. Log:
/Volumes/RTKGoBuild/tmp/custody-recovery-order-regression.log.


2026-11-09 public export checkpoint: runtime public CRL discovery now preserves
Pod namespace metadata through the typed placement view, accepts original scoped
KV caches rather than requiring identity PVCs, and qualifies original expected
client purpose/controller chain without executing the stopped producer. It records
public cache authority/path only; no registry record is fabricated as an observed
export. Final public verification reads actual PEM/reload/readiness and independently
inspects the original committed cache against the selected signed fresh record.

RED: actual remote public discovery failed the old placement/PVC assumption;
a second RED caught duplicate native issuer purposes being accepted. Focused GREEN
4.843s. Extended the actual maintenance adapter test through observe (including
remote storage selection and original consumer), install, committed inspection,
actual public output/readiness, native registry ACK and complete converge. Cases
reject missing reload, unready producer, corrupt committed cache despite a valid
public output, duplicate purpose and prior foreign owner/SA/mount/role cases.
Selected root regression, with actual isolated Kubernetes fixture, PASS45.972s:
/Volumes/RTKGoBuild/tmp/custody-public-export-regression.log.

Next: shared preflight/health CRL readers still cat app file projections; wire the
same independent committed proof into their exact selected environment context,
including Account Manager public-listener verification. Core identity admission
also needs a committed proof for every applicable owner, not only product output
completion. Preserve independent local OpenBao boot and prove the whole isolated
creation/recovery/cold-start/renewal/matched-restore lifecycle before cloud changes.
No live backend settings, cloud resources, feature CI/Release or GO changed.


2026-11-09 shared readiness checkpoint: compiled preflight/consume and normal-tail
checks now use a configured runtime carrying exact environment/store, selected
release Controller image and frozen backend. Standalone post-deploy health and
legacy acceptance construct the same reader from their environment's assigned
Controller digest and canonical kubeconfig; there is no latest/other-release
lookup. Health ordinary queries and native helper share frozen credentials.

Mounted-CRL checking marks the backend from actual container metadata (not JSON
manifest input), resolves only this original Deployment/Pod and client purpose,
and inspects the committed cache independently. The existing native signature,
metadata, validity-window, registry-latest and actual ACK checks remain. No unrelated
workload readiness is used to identify this consumer. Read-only inspection is
valid without maintenance writer ownership; applying a record still requires it.
Identical public reads are coalesced per invocation, not persisted as a GO/cache
artifact. The test verifies now/window reuse creates no second Job while a future
expiry cutoff still fails.

RED observed for missing checker/storage API, then configured focused GREEN5.988s
through actual readiness with app cat forbidden and no maintenance Lease. Added
all-environment health-context test: RED found ordinary queries and helper could
use different Kubernetes identities. Corrected one frozen source, then corrected
the fixture's raw-vs-normalized credential comparison (the backend deliberately
canonicalizes kubeconfig). Selected regression PASS60.553s including custody,
actual isolated Kubernetes projection, deployment-check/frozen-auth, native core/
MQTT and maintenance/online tests. Log:
/Volumes/RTKGoBuild/tmp/custody-readiness-context-regression.log.

NEXT: Account Manager public-listener checker still cats its private CRL state
in deployment_unified_account_registration_crl_health.go around line206. Route
that branch through store.checkRuntime.committedCRLState when its original
pkimanagement container selects OpenBao. Configure c.CheckMQTTFoundationIdentity's
local store from c.newCheckRuntime (prefer sharing the caller's existing runtime
across now/window) and standalone AM health from its runtime's full configuration;
its current locally constructed cfg only contains stack. Use existing
newRegistrationPublicHealthFixture, project its original owner to KV, write exact
test-only generation binding/transport, provide native public-state process
responses per issuer and forbid state cat. Require same public bytes and original
listener/key/CA plus committed proofs; keep the public PEM read real.

Core client/server committed identity proof is still wired primarily product
completion, not yet every ordinary preflight/health owner. Scope from original
client purposes and native profiles; do not reintroduce app-startup/Controller or
image-version equality dependencies. Verify helper network-policy reachability
as part of the actual whole isolated lifecycle, alongside cold start, renewal,
interruption, owner fencing, missing-state refusal and matched backup restore.
Then final fresh whole-branch review, native/contracts/root CI+Release and formal
approved dev remove/archive/create preflight GO/deploy/health, followed by staging.
No cloud resource, active environment setting, formal GO or deployment changed.


2026-11-09 Account public-listener checkpoint: the observed OpenBao management
profile now compares its real public CRL output with independently read original
committed cache state, not an application's private file projection. The compiled
MQTT prerequisite shares the caller's configured check runtime across now/window
and ordinary PKI checks; standalone Account health takes full configuration from
its selected reader. The original listener/key/CA remain unchanged. Legacy PVC
profiles retain their existing reader and checks.

RED confirmed one public read followed by one forbidden private state read; the
new test uses original projected Account/ReplicaSet/Pod/manifest, exact protected
generation bindings and transport snapshot, and native helper process readback
per issuer. It rejects corrupt committed state despite valid public output.
Focused Account/MQTT tests PASS9.000s. Expanded regression PASS71.524s, including
custody, isolated real Kubernetes projection, deployment-check/auth, existing
Account public profiles, MQTT foundation and CRL maintenance/online paths. Log:
/Volumes/RTKGoBuild/tmp/custody-account-readiness-regression.log.

NEXT discovered real global issue: one container (notably Log Ingester) can have
both its original internal Service client and product registration (logger) client.
Current custodyConsumerID scans every IDENTITY_STATE and rejects different subjects;
observe's identity cache is keyed only by container. Resolve the expected CRL
consumer using the original client purposes and the actual CRL-state projection
volume. Same physical projected volume / original logical storage purpose identifies
which original client owns that cache; no guessing from Pod names or new setting
maps. Retain unique-subject compatibility when there is only one original client.
Pass state path through observe, public-export inventory and shared readiness;
include it in the per-observation identity cache key. Add a two-purpose regression
before changing behavior. This is necessary for the newly consolidated owners.

Still incomplete: core client/server committed admission for every applicable
owner; whole isolated new creation/recovery/renewal/cold restart/matched restore;
final fresh review and dependency CI/Release; then authorized formal dev removal,
local generation archival, create GO/deploy/health, followed by staging. Inspection
also found local generation archival is documented but no implementation at
'deployment/archives' in production Go yet: implement it in the formal path before
new live state. Existing removal is deployment remove (deployment_config.go), with
runDestroyEnvironmentResources called after native volume and DNS evidence; the
underlying destroy CLI prints a read-only plan unless --yes, and protects nonempty
buckets. Do not run it directly with missing runtime values (legacy default staging).
No cloud mutation, environment backend activation or formal preflight GO occurred.


2026-11-09 multiple-client checkpoint: CRL expected-subject resolution now uses
original admitted client purposes sharing the actual cache's scoped memory volume.
It preserves the single-client/separate-trust-volume compatibility case and refuses
ambiguous or unbound multi-client caches. No new settings or manual manifest-to-
subject table. Observe, public-export discovery and shared readiness pass the exact
cache state path; observe caches identities by state path, not only container.

RED observed for the previously unavailable state-specific API. Focused two-client
+ Account tests PASS6.938s. The regression now exercises both explicit independent
cache purposes and the real observer with two manifests in one container, proving
that it returns two actual subjects rather than reusing the first. Unknown cache
placement and a missing selector remain failures for ambiguous purposes. Expanded
selected root regression PASS72.222s; log
/Volumes/RTKGoBuild/tmp/custody-client-purpose-regression.log.

Ruling: expected CRL subject comes from original admitted client intent plus the
actual scoped projection purpose — not Pod names or a new identity policy map —
this preserves both Log Ingester clients under one logical KV owner; if an actual
profile combines ambiguous purposes in one volume, it fails explicitly instead of
selecting an incorrect ACK identity.

Revalidated unchanged native c97d455 with real isolated PostgreSQL, Kubernetes and
OpenBao enabled: full -race packages cmd/serviceidentity-bootstrap 6.419s,
internal/pkitrust 12.049s, serviceidentity 6.029s, openbao 3.267s, config 1.480s,
apiapp 5.606s. Log /Volumes/RTKGoBuild/tmp/custody-native-provider-profile-regression.log.
A second verbose focused run explicitly confirms the actual provider subtest ran
(no skip): TLS + Kubernetes auth/owner transfer + pending replay + CAS fencing +
outage + durable provider restart + JWT replacement/reauthentication + renewal.
Actual registry/CRL admission and public-cache repair cases also passed; proof log
/Volumes/RTKGoBuild/tmp/custody-provider-registry-explicit-proof.log. These are real
native boundary/lifecycle proofs, NOT the still-pending whole fresh environment
creation or matched OpenBao/registry backup restore.

NEXT priority: exercise the complete fresh OpenBao-mode creation path as one flow
before further per-gate patches. Existing createdNativeInventoryFixture and
TestCreatedCompleteNativeOwnerInventoryCanonicalRender cover the actual final
native recipes for all core/application owners but only the PVC profile; extend
with OpenBao state and retained independent OpenBao/business claims, then couple
the native provider/registry/Kubernetes lifecycle pieces for full fresh creation,
interruption/renewal, cold restart and matched restore. Do not substitute another
set of isolated passing adapters for the whole required flow. Admission of original
committed client/server states must reach every applicable preflight/health owner.

Known remaining operations gap: local generation archival at deployment/archives
is specified but not implemented; formal removal uses cleanupDeploymentEnvironment
(deployment_config.go), native PV/volume and DNS proofs, then the existing destroy
CLI. The underlying CLI has read-only listing unless --yes but legacy staging
fallbacks if not called through resolved environment config; do not invoke blindly.
All live dev/staging backend values and resources remain unchanged. Final fresh
review, dependency CI/Release, then approved dev removal/archive/create GO/deploy/
health and staging remain required. Goal remains active.

Follow-up checkpoint: the operator selected fresh generations for both dev and
staging. Keep the deployment order dev, then staging after dev health acceptance.
The original local identities and operating evidence still need to be archived;
no legacy workload identity is to be copied into either new generation.

Workspace 04f5978e: complete native fresh dev/staging inventory is projected to
OpenBao, observed through the canonical binding path and rendered again. The App
API's retained App Root and Service client paths are validated against the
scoped memory profile; the original PVC mode keeps its original claim checks.
The new roundtrip initially failed on App API's PVC-only check, then passed in
1.439s. Focused workspace regression passed in 73.206s. The test proves
render/observe consistency, including retention of independent OpenBao and
PostgreSQL storage, rather than a real cloud deployment.

Native 60e1205: the isolated OpenBao TLS/Kubernetes owner lifecycle additionally
uses actual PostgreSQL issuance and CRL admission for the same enrolled and
renewed credential. After renewal it seals/stops OpenBao, backs up the encrypted
file data and the exact private fixture schema, restores OpenBao into a new
volume and opens the renewed identity from an empty projection. The older pair
is rejected with a newer registry CRL and admitted after restoring the matched
PostgreSQL dump. The opt-in race test ran without a skip in 9.219s, and the
serviceidentity package race run passed. Proof log:
`/Volumes/RTKGoBuild/tmp/custody-matched-backup-proof.log`. This is an isolated
file-backed provider/registry recovery proof, not the full fresh environment
orchestrator or a proof of production backup tooling.

Remaining before live removal: run the complete fresh orchestrator with
OpenBao custody; ensure ordinary preflight/health independently admits original
committed identities for all applicable owners; implement exact-generation
removal and private archival. Current removal delegates through the legacy
`--env-root` resolver, whose basename heuristic appends `runtime` even to a
canonical staging store, and its broad name matcher cannot by itself establish
resource ownership. The formal rebuild must carry the selected environment and
exact original provider identities across removal/absence/archive. No live
resource, environment setting, deployment result or backend selection changed.

Rebuild checkpoint. The formal `deploy-environment.sh archive-removed-generation`
command binds the original captured LKE cluster and volume ownership to a private
archive manifest and verifies exact cluster/volume 404 plus full selected stack
inventory before segregating state. A manifest and pending owner allow retries
after partial moves, and fresh qualification/admission reject the pending owner.
Operator credentials, retained storage configuration, DNS history, and older
archives stay in their original paths. Focused archive, removal and fresh target
regression passed in 51.691s (deployment-generation-archive-regression.log).

Read-only Linode and Kubernetes inventory confirmed live dev cluster 666130,
edge 107939723 and TURN 107939810 match the completed create journal. LKE node
107939721 belongs to that cluster. The canonical removal proof and live Kubernetes
PV inventory agree on all 31 numeric CSI volume IDs. Both exact configured dev
runtime/OTA buckets returned object count zero. No live mutation was performed.

The normal remove path still needs to pin its destructive provider inventory to
these original receipts rather than authorize deletion from its broad matcher.
Its legacy kubeconfig/provider state archival also happens before full removal
verification. Keep those bytes available for this generation archive while
retaining the exact-generation retry checks. Correcting the earlier locator note,
the normal resolved deployment configuration supplies a derived runtime path to
legacy remove. The basename heuristic is a risk for direct canonical-store
invocations, not evidence that normal staging remove already failed.

Exact removal checkpoint. The normal script now accepts
`remove --environment NAME --confirm STACK --original-cluster-id ID --plan`
for the completed dev/staging native generation. It binds the active ledger to
the completed journal, rechecks exact native targets, requires empty original
media/OTA buckets, and captures current LKE pool membership plus canonical
CSI volume ownership. Broad label/tag matching detects extra candidates but
grants no deletion authority. The durable intent preserves the verified
Kubernetes purge across retries and keeps the original kubeconfig, LKE state
and worker binding available for `archive-removed-generation`. Archive checks
exact original instances, cluster and volumes for absence. A pending removal
blocks ordinary create and upgrade admission.

The live read-only plans verified dev 666130 (1 worker, 31 captured volumes) and
staging 694047 (6 workers, 28 captured volumes), with their original edge, TURN,
and empty media/OTA buckets. An independent read confirmed both deployment
Leases were absent. The isolated removal fixture refuses an extra stack VM
before any Kubernetes or provider mutation, simulates an edge API failure
after successful LKE deletion, then retries to exact provider absence and
archives the original generation. Live resources have not been removed.
The final removal, archive, fresh target, lease and execution fence regression
passed in 57.497s (deployment-original-generation-complete.log). Final formal
`remove --plan` commands passed for both environments with the same IDs.

Committed identity admission checkpoint. Native 924a723 introduced bounded
batch admission over each original workload scope, verifying the same committed
client/server credential and current native registry/CRLs at now and the rollout
cutoff. Workspace preserves the original service and MQTT server purposes and
connects the native batch to existing-environment qualification, execution
recheck, steady health, and normal-tail qualification. Only a validated native
producer can defer an unprojected product client during normal-tail preflight.
Ordinary health has no producer deferral.

Final selected workspace regression, including the isolated real provider
bootstrap and Kubernetes projection fixture, passed in 69.063s
(custody-admission-live-fixtures.log). Native ba8196a ran the production batch
binary against real OpenBao, PostgreSQL and Kubernetes TokenReview before and
after matched backup restoration. The same committed renewal was admitted,
future expiration and cross-owner access were denied, and the successful KV
write whose response was dropped was reconciled without reissuing the identity.
The real fixture ran without a skip in 8.674s (custody-batch-restore-loss.log).
This exercises the full native identity lifecycle in one real fixture, alongside
formal bootstrap and renderer tests. The complete dev create preflight and
deployment remain live operations after review, delivery and isolated acceptance.

Fresh core CRL initialization checkpoint. The native registry must hold the
signed Service intermediate CRL before bootstrap transport is replaced. Create
now reads the original OpenBao mount's signed public CRL and reconciles native
import by exact readback. Before managed core rollout, three bounded Jobs
initialize the listener and outbound CRL records for Controller, CertIssuer and
Account Manager. They reuse the same owner scopes and memory projections as
runtime, without mounting the temporary enrollment credential or requesting a
PKI PVC. Ordinary inspection and maintenance still reject missing records.
Fresh OpenBao create qualifies the image capability needed by these Jobs.

Native dec9ff6 preserves and reconciles exact uncertain identity writes after
both response and immediate readback are lost. Native 74cd78d adds the CRL
initializer. The actual CLI initialized and read back both signed Service CRLs
against isolated OpenBao, PostgreSQL and Kubernetes TokenReview, including after
matched backup restore (custody-fresh-crl-live.log). The targeted workspace test
verified that a committed native CRL import with a lost response is read back
and never repeated, and that core Jobs project into the expected custody owner
and memory volume (custody-fresh-crl-root-final.log). Complete core convergence,
exact ACL and teardown review, CI publication and live dev remain pending.

Exact removal evidence checkpoint. Read-only removal planning also checks the
original kubeconfig, unoccupied execution Lease, current persistent-volume
ownership and DNS. Removal persists a completion only after independently
checking the exact original VM, bucket, cluster, volume and DNS absences.
Archive requires the completed evidence, preserves its bytes, and rechecks
the same IDs and DNS scope even when resuming an interrupted archive. The
isolated regression rejects missing completion and a renamed original VM and
preserves the original archive bytes (custody-exact-archive-final.log).
