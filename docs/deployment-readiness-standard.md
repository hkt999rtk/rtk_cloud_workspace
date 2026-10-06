# Unified Deployment Readiness Standard

Status: accepted deployment policy; unified implementation and environment qualification in progress.

Owner: `rtk_cloud_workspace`; service owners supply candidate and recovery checks.

Last reviewed: 2026-10-06.

Authority: deployment scope and readiness policy. The [deployment architecture](cloud-deployment-architecture.md)
owns configuration/provider boundaries, and [Platform PKI](../repos/rtk_cloud_contracts_doc/platform_pki.md)
owns identity, signing and recovery policy. [ADR 0003](adr/0003-unified-environment-deployment.md)
records the replacement decision. Dated observations and local fixtures are evidence,
not permission to relax this policy.

## Meaning of GO

**GO permits execution of one specified release's complete environment plan after
all required pre-deploy gates pass.** It records the environment, operation, release
digest, configuration digest and plan SHA-256. It is not an assertion that the old
environment is healthy or that the deployment has completed. A different release,
input, ownership baseline or resource version requires a fresh plan and qualification.

Dev, staging and prod use the same compiler, planner, qualification and executor.
Capacity, enabled components and protection requirements are explicit configuration.
There is no staging-only renderer, private-plan-file detection or static-PKI fallback.
Until replacement is complete, the legacy checker's narrower PASS must not be
presented as GO under this standard. Missing replacement capability is NO-GO,
not a reason to use the retired renderer or remove a required check.

Every enabled application, worker, sidecar, initialization job and infrastructure
component must have a maintained renderer, immutable image, dependencies, health
check and recovery rule. Full upgrades include Deployment, StatefulSet, DaemonSet,
Services, storage, ConfigMaps, access policy, network, routing and provider resources.
Unknown enabled components or owners block qualification.

## Required gates

| Stable gate ID | Evidence required | Repair and effect |
| --- | --- | --- |
| `deployment.target` | Exact environment/operation, complete enabled catalog, resolved configuration, ownership and deterministic target. | Correct configuration/catalog or record verified ownership; an incomplete target cannot be executed. |
| `deployment.release` | Every affected API/worker/sidecar/Job/provider image is pinned; workspace gitlinks, service source, CI publication and artifact provenance agree. | Publish missing exact-source artifacts and select a new manifest; no borrowing evidence from another HEAD or mutable tag. |
| `deployment.execution` | Required tools, effective permissions, capacity, quota, node placement, taints, storage and admission for the exact execution bodies. | Repair capacity/access/storage/admission; read-only provider access alone is insufficient execution evidence. |
| `deployment.pki` | Original identity/issuer/state bindings, chains, SAN, purposes, remaining validity, trust pins, CRLs, consumer acknowledgments, Secret keys and durable private-key mounts. | Repair through the managed lifecycle; do not rebuild a static identity or generate a replacement Root during upgrade. |
| `deployment.compatibility` | Actual candidate startup, schema compatibility, or a separately verified migration and recovery sequence. | Fix candidate/schema or qualify the migration; a successful image pull is not startup evidence. |
| `deployment.network` | Desired Service, policy, DNS, TLS/mTLS and authenticated routes; anonymous denial where required. | Repair target routes/credentials and verify the candidate behavior; testing only an old endpoint is insufficient. |
| `deployment.recovery` | Complete durable-data inventory, usable current backups, concurrency-safe recovery and independently retained security state. | Migrate writable-layer/emptyDir durable data, repair protection and run restoration; do not claim reversible data changes without evidence. |
| `deployment.production` | Protected-profile HA, custody/escrow, measured RPO/RTO, security-state reconciliation, complete PKI lifecycle and failure exercises. | Complete actual qualification for the selected release; normal traffic and issuance remain closed until it passes. |

Required `FAIL`, `ERROR`, `BLOCKED`, `SKIPPED` or absent evidence always means
NO-GO. `--fast` cannot obtain a complete-upgrade GO. Scoped and route-only operations
must identify their smaller resource scope explicitly and cannot authorize a full
upgrade. Reports contain a stable ID, cause, repair, impact and recheck action per
blocker. Operator-facing help and output are English. Shell exit codes remain
0 for passed required checks, 1 for failed/incomplete qualification and 2 for
invalid input/configuration.

The execution adapter checks the selected frozen Kubernetes identity with native
`SelfSubjectAccessReview` requests. Collection-level create, name-scoped
get/update/delete and named rollout list/watch permissions follow the exact plan.
Phase inventory also requires namespace-wide collection reads for all inventoried
resource types, plus cluster-level namespace/access-policy inventory reads;
label selectors do not narrow RBAC authorization. A missing or ambiguous review
blocks `deployment.execution` with the internal blocker
`deployment.execution.permissions`, without modifying
workloads. These access reviews, execution-lease checks and server dry-run are
partial execution evidence: capacity, quota, placement and storage qualification
must also pass before the complete execution gate can be GO.

Existing durable claims now bind the plan to a separate `storage_sha256` input:
the original PVC UID/allocation, native PV UID, and complete PV specification.
Planning reads the native bound owner and requires its `claimRef` to match the
original claim UID, namespace and name. Execution qualification and every phase
fence repeat that proof. A replaced/lost claim or PV, changed volume handle or
cross-environment binding is `deployment.execution.storage`; pure status and
resource-version updates do not change storage identity. The observer requests
only exact-volume `get` permission and persists no volume resources. This does
not independently qualify new-volume provisioning, scheduling or data recovery;
those remain required capability gaps. A PV-spec change during an intended resize
also needs its independently verified migration before an ordinary update.

An old fault that blocks a necessary dependency blocks the plan. When the plan
repairs that fault, qualify the repaired candidate's behavior before passing;
neither unconditional old-health requirements nor ignoring dependency failures
is an acceptable substitute.

## Planning, execution and recovery invariants

`cloud_env/` contains configuration only. Every local deployment-generated
record, secret, certificate, provider allocation, temporary file and report uses
the selected environment's canonical `~/.config/rtk_cloud/<environment>/` root.
See the [configuration/data boundary and path table](deployment-secrets-governance.md#configuration-versus-deployment-generated-data).
Storage activation receipts and unchanged migration proofs use
`deployment/storage/`; preflight and execution share the validator. Legacy
workspace evidence requires explicit validated import and never supplies a
lookup fallback. Storage evidence/journals enter the reviewed configuration
digest and are checked again at execution fences. Frozen Kubernetes subprocess
identities use `deployment/tmp/` and are removed after use; JSON deployment
reports must use `deployment/reports/`. A producer/consumer that selects another
dynamic-data directory is a migration gap, not complete deployment GO.

The desired state comes from maintained architecture/environment settings, a
selected formal release manifest and environment SecretStore bindings. Live
objects supply verified ownership, original identity and concurrency baselines;
they never supply the desired application configuration.

The environment's selected manifest lives at
`~/.config/rtk_cloud/<environment>/deployment/release.json`; an explicit
`--release` may select a separately verified published manifest. Its absence is
a release-intent blocker and never selects another renderer. Keeping this
selection outside tracked source avoids a manifest containing the SHA of the
commit that would itself contain that manifest. The executing workspace and
service checkouts must be clean and match the selected source revisions.

Each resource is create, update, retain or explicit controlled retirement.
Preserve Service allocations, provider IDs, existing private keys, issuer state,
Secrets and PVCs through their own lifecycles. Immutable fields, incompatible
storage/schema or a trust-root change produce a named migration blocker. Never
silently delete and recreate them. Retire workloads/routes only with exact owner
preconditions; retirement cannot implicitly delete identity or persistent data.

The common Redis and Prometheus targets use maintained persistent claims
(`redis-data` and `video-cloud-prometheus-data`) in every environment. Shared
`REDIS_DATA_STORAGE`, `PROMETHEUS_DATA_STORAGE` and optional
`DURABLE_DATA_STORAGE_CLASS` configuration select their capacity and class.
Both are single RWO writers with Recreate updates; Redis enables AOF with
`appendfsync everysec`. Existing PVC volume/class/mode allocations are retained.
An existing writable-layer/emptyDir source, another claim, a changed mount path
or an unbound/terminating claim produces `deployment.migration.durable-storage`.
First quiesce, archive, copy and validate the original data through an independent
migration, then bind the original workload to its verified populated claim.
Preflight never creates a blank replacement claim as a repair. Persistent
placement alone does not prove backup freshness, RPO/RTO or restore qualification;
those remain required recovery evidence.

Preflight compiles in memory. Execution takes the reviewed `--plan-sha256`,
recompiles/requalifies, checks input/ownership/PKI/resource-version fences before
each phase, and sends the same bodies used by server dry-run. A private journal
records in-flight writes and newly allocated owners before readiness waiting.
PVC readiness waits run after their consuming workloads have been installed,
so `WaitForFirstConsumer` storage can schedule and bind. Every declared PVC,
including retained storage, must bind before acceptance and completion. Failed
binding records the exact storage owner in the private recovery journal.
Partial failures preserve recovery information; they do not blindly rewind data
or overwrite concurrent changes. Reusing a failed journal is not an automatic
resume: first reconcile actual state and qualify a new plan.

The selected cluster's `rtk-deployment-execution` Lease serializes controllers on
different hosts, in addition to the private local execution fence. Its holder is
bound to a reviewed plan and an unpredictable attempt identity. Elapsed time
never authorizes stealing the lease. A possible or confirmed partial write retains
both the cluster lease and local recovery fence until explicit outcome reconciliation;
Post-deploy PKI, endpoint and business-flow acceptance runs while the same cluster
lease and local fence remain held. Repeat input, desired-state and owner checks
after acceptance; failures retain recovery fencing and never record completion.
Release uses exact UID/resource-version preconditions and verifies absence. A
concurrent replacement must not be deleted. Preflight checks local and cluster
lease occupancy, exact lease create admission and release permission without
persisting a lease. An occupied or unreadable fence blocks qualification with
recovery instructions. Lease permission and recovery of an
interrupted lease are required execution prerequisites, not a qualification waiver.

`deployment reconcile-execution --environment NAME --plan-sha256 ORIGINAL_SHA
--confirm video-cloud-NAME` inspects the original private journal on the original
controller. A native file lock must prove the executor has stopped. Reconciliation
releases only an exact recorded cluster owner and archives the original local
owner receipt when all resources still match either a proven no-write baseline
or an already accepted target. Missing/corrupt evidence, in-flight or partial
writes, interrupted PKI/business acceptance, changed owners and concurrent drift
remain NO-GO. It never rolls back resources or deletes a replacement lease.
Retain the original `~/.config/rtk_cloud/<environment>/deployment/` directory;
a copied fence cannot prove that its executor has stopped. Repeat complete
preflight after reconciliation; `RECONCILED` is not deployment GO.

New creation separates infrastructure prerequisites, acquisition of Kubernetes
access, exact admission, and managed bootstrap/workloads. Before the API exists,
report downstream gates as pending; do not invent runtime evidence. Only the
explicit initialization stage may run before complete activation qualification.

After execution, compare actual resources and releases against the target,
verify all workload readiness, PKI/public endpoints and representative business
flows, then record completion. An executor journal alone is not that receipt.

## Production protection profile and custody

Use the common OpenBao Raft renderer with three persistent members on distinct
nodes, durable audit storage and verified quorum/failure behavior. Initialize
Shamir with five shares and threshold three. Online CA private keys remain
inside OpenBao. Existing file storage requires a matched, independently verified
file-to-Raft migration preserving original data, seal and issuer identities;
Root regeneration is not recovery.

Controller/operator directories are `~/.config/rtk_cloud/<environment>/`, real
0700 directories and regular 0600 files. Use encrypted PKCS#8, JSON and age
across platforms. Do not use DMG or choose another storage directory implicitly.
The custody manifest contains only public identity, cluster/seal parameters,
share IDs, named custodians, locations, public age recipients, encrypted-envelope
digests and verified handoff references. It contains no shares, passphrases,
private keys or root token. A public recipient is not its decryption identity.

Five primary holders each receive a separately encrypted share envelope. An
independent escrow holder/location/key receives a separately encrypted copy for
each share. No custodian, location or decryption key may access three distinct
shares, counting primary and escrow together. Escrow is outside the cluster and
original controller. Actual names, storage locations, retrieval permissions,
handoff verification and recovery owners must be recorded; placeholders or
hash-shaped receipt strings do not establish custody.

During recovery the named coordinator verifies original cluster/seal/trust
fingerprints, obtains three distinct original shares from independently verified
holders, restores the encrypted snapshot under quarantine, unseals the original
lineage, reconciles issuer/revocation/authorization state and verifies consumers.
Do not centralize a quorum in a persistent operator directory. Remove temporary
recovery credentials after verified completion; keep the public audit record.

## Bootstrap and qualification

Fresh managed bootstrap uses one persistent session, request and dedicated PVC.
Retries keep the original workload key/CSR/session; timeout cannot create a new
identity. Complete workload acknowledgments, seal Account Manager bootstrap,
revoke the deployment bootstrap leaf, publish its CRL, verify consumer
acknowledgments, remove bootstrap Secret/policy/credentials and verify absence.
Record finalization only after those steps. Restart must not recreate bootstrap
administrators or reopen a sealed session. The configured operator remains the
authorized PKI administrator; recovery-share quorum does not introduce a second
`pki_admin` approval.

Qualification includes issuance, renewal, replacement, revocation, CRL publication
and acknowledgments, node loss, quorum unseal, administrator recovery and isolated
restore. Record exact release, plan and runtime evidence per exercise. The maintained `DEPLOYMENT_PROTECTION_PROFILE` selects `standard` or `production`;
production overrides require the latter, and the compiler rejects a downgrade.
The same production qualification can be selected for isolated rehearsals. Production
bootstrap may perform only necessary initialization while normal traffic and
signing remain fenced. Removing a hardcoded guard without implementing these
capability checks is prohibited.

Protect PostgreSQL with continuous authenticated encrypted WAL and native PITR;
protect OpenBao with encrypted original-seal Raft snapshots. Include SQLite,
durable Redis/MQTT state and all enabled persistent components. Verify freshness
and failures, not only the existence of a backup object. Measure RPO <=15 minutes
from an independently observed fault-time/last recovered commit witness; measure
RTO <=4 hours including retrieval, provisioning, replay and validation. Time since
an operator-entered target timestamp is not RPO evidence.

Revocation, account suspension, Root distrust and audit history survive independently
of restored application state. Security-history gaps keep traffic and issuance
closed. Local native fixtures demonstrate implementation behavior only; actual
custody/provider/HA/restore evidence is still required for environment qualification.

## Replacement status

The implementation branch contains the v2 resource planner, fenced executor,
required-gate contract, common catalog foundations, immutable release checks,
portable custody validation, original-seal Raft snapshot adapter, authenticated
WAL/PITR and isolated native restore checks. Native OpenBao tests also cover
original-seal file-to-Raft migration, three TLS voters, leader failure, peer restart,
encrypted restore and CA/revocation-state preservation. The common Raft renderer
advertises stable pod identities and peer addresses, enables persistent declarative
audit, and admits Raft peers through a dedicated NetworkPolicy. Member count,
resource requests and node placement come from shared capacity settings; the
production profile requires three distinct members. Existing-owner adoption writes
a typed review candidate and privately archives a matching old plan without using
it as renderer input. Exact server admission runs in preflight; admission alone
does not complete execution qualification. Scheduled WAL heartbeat and remote
verification share the protected backup catalog. Controller domain consumer,
MFA and CRL worker policies come from maintained settings; missing consumers or
disabled refresh block planning instead of inheriting old Pod policies. Runtime
bootstrap installation receipts now use the workload's managed mTLS identity
through its installed CRL consumer; the controller checks the exact session
issuer/Root and writes only that peer's subject. Sealed receipts can be retried
without reopening enrollment. Restart retains durable identity state without
reading removed one-time credential files. Native PostgreSQL/mTLS tests cover
those boundaries and restricted runtime database roles. Recorded-owner
reconciliation for stopped controllers can release
only a proven no-write baseline or an already accepted unchanged target, without
resource rollback. In-flight/partial writes, interrupted acceptance and concurrent
owners remain fenced. The old direct-database bootstrap ACK/seal commands are
retired; runtime acknowledgments and operator finalization use their managed paths.
The controller now composes native bootstrap Secret/ACL cleanup from an immutable
session owner record, checks current registered workload leaves, fences namespace
and Secret UIDs, and confirms authenticated absence before finalization. The common
renderer grants only exact-Secret get/delete and exact-namespace read through a
separate projected API token. Missing/malformed bindings and uncertain deletes
stay closed. Initial binding capture, complete-engine bootstrap credential cleanup
and complete-environment acceptance are still unfinished; this adapter does not
independently prove fresh-create qualification. The upgrade-specific Account
Manager credential lifecycle is described below.
The dedicated bootstrap owner now supports registry-backed credential removal:
it persists an immutable public completion marker, locks the original private
state directory, verifies key/CSR/chain ownership and removes only the recorded
credential files. Same-receipt partial retries preserve the original lineage;
the completed PVC cannot generate another key. Complete-engine invocation and
state-owner installation are still pending; local filesystem tests alone do not
prove the environment has completed this lifecycle.

Account Manager bootstrap retirement is now included in complete upgrade plans.
Planning reads the original PostgreSQL system identifier and the actual permanent
`platform_bootstrap` seal, verifies the runtime Secret points to that database,
and removes only its two initial administrator fields through an explicit Secret
lifecycle body. The same body is admitted and executed with original UID/full
baseline/resource-version checks; other credentials are preserved. Fences recheck
the durable seal. The maintained Deployment target removes direct bootstrap
environment settings and records a deterministic credential-retirement annotation
to roll out fresh Pods. After accepted deployment, its original Deployment owner
must report the observed generation and all updated/available replicas before
the local `runtime/platform-admin`
seed is removed only if the accepted Secret owner and approved seed still match.
A private `deployment/account-bootstrap-retired.json` negative marker prevents
regeneration through both missing-secret repair and old catalog expansion, while
credential verification skips the retired binding only when
current original-database/Secret evidence passes. A marker alone is never GO.
Native isolated PostgreSQL tests cover permanent sealing and disabled-admin
restart behavior; environment qualification and fresh-create stage orchestration
still remain unfinished.
The public complete entries use this
engine and no longer select schema-1 files. Exact publication artifacts bind source,
digest and run attempt. Complete provider/create/bootstrap adapters, complete
candidate/recovery coverage and
actual production qualification are not yet complete. Existing environments
have not been deployed or declared GO by this implementation work.

Redis and Prometheus now have common persistent targets and an existing-data
migration fence. Three-environment native Redis process-loss drills verify the
renderer's AOF data survives replacement; they do not prove remote backup or
environment RPO/RTO. Remaining storage migrations, durable component recovery
coverage and the required qualification adapters are still incomplete. Frozen
release output is restricted to the selected environment's private
`deployment/releases/`; reports and temporary API identity files use its
`deployment/reports/` and `deployment/tmp/` directories respectively.
Normalized provider region/quota metadata now uses private
`deployment/provider-preflight.env` for both production and acceptance reading;
it is not completion or GO evidence. Changing checkouts cannot change this path,
and missing canonical metadata cannot fall back to `runtime/state`. The old
staging controller-state copier is retired rather than writing operational state
back into configuration. LKE provider IDs and quota now use private `deployment/adapters/lke/`, and
both are part of the frozen deployment input digest. Native owner migration
compares the original selected API/CA and recorded pools, archives original
provider bytes and refuses conflicting or concurrent destinations. Missing
recorded clusters cannot be recreated through automatic label discovery.
Teardown captures original local owners before cloud mutation, privately archives
the destroyed identity and refuses to erase a concurrent file replacement.
Remaining edge/TURN, DNS, service and artifact-state writers still require migration before the
configuration/state separation is complete.
