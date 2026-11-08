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
