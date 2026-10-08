# Unified Deployment Readiness Standard

Status: accepted deployment policy; existing-update checks implemented; qualification is specific to an environment, release and plan.

Owner: `rtk_cloud_workspace`; service owners supply candidate and recovery checks.

Last reviewed: 2026-10-08.

Authority: deployment scope and readiness policy. The [deployment architecture](cloud-deployment-architecture.md)
owns configuration/provider boundaries, and [Platform PKI](../repos/rtk_cloud_contracts_doc/platform_pki.md)
owns identity, signing and recovery policy. [ADR 0003](adr/0003-unified-environment-deployment.md)
records the replacement decision. Dated observations and local fixtures are evidence,
not permission to relax this policy.

## Current script delivery scope

The current delivery is limited to `scripts/check-deployment-preflight.sh` and
`scripts/deploy-environment.sh`, their necessary existing Go implementation,
local regression tests and operator instructions. The operator selects the
environment and authorizes live checks and deployment. An authorized agent may
run qualification on the operator's behalf; authorization to repair or run
preflight does not by itself authorize rollout.

- All environments use one workflow for updating an existing environment's
  enabled workloads and maintained configuration. This is not an image-only
  restriction and does not select an environment-specific renderer.
- Preflight verifies and may create a missing configured media bucket when no
  storage cutover is requested; other provider probes remain read-only. It
  resolves one release and target, checks required configuration,
  images, tools/access, capacity/admission, PKI/Secret bindings and dependencies,
  then reports GO or NO-GO with the plan digest and actionable failures.
- Deployment rechecks that same target and ownership, executes the checked
  resource bodies, waits for rollout and verifies required workload, PKI and
  endpoint health before reporting completion. Partial failure is not success.
- Device/App API and main issuer role flags come from shared architecture
  settings. Existing App API main/initializer images, retained trust-state
  references and the original TLS passthrough route are included in the same
  plan. Missing references or role incompatibility remain explicit blockers;
  see [the role mapping](cloud-deployment-architecture.md#shared-device-and-app-authentication-roles).
- Existing identity, keys, CRLs, Secrets and persistent data are retained.
  A required incompatible identity/schema/storage change is an explicit migration
  blocker, not an automatic reset or an additional recovery project.
- Output/help remain English with exit codes 0/1/2. Environment operational data
  remains under `~/.config/rtk_cloud/<environment>/` using common path helpers
  and private permissions; `cloud_env/` contains configuration only.

Fresh-environment bootstrap, Production activation, new custody/escrow systems,
backup/PITR implementation and restore/RPO/RTO qualification are outside this
delivery. The broader qualification design is retained as a separate roadmap; building
those capabilities is not a dependency of finishing these two scripts. Necessary
safeguards for the selected update remain in force. Local regression results do
not establish environment GO; the scripts must actually complete against the
selected environment and release.

Preflight and deployment stream `[RUNNING]` stage IDs while resolving provider
and formal CI images, observing Kubernetes, compiling the target and qualifying
required gates. Execution qualification identifies the current substep as
`deployment.execution.node-capacity`, `.storage`, `.permissions`, `.lease` or
`.admission`; `.provider-quota` applies only to operations requiring that check.
PKI qualification identifies `.before-fence`, `.bindings`, `.mqtt-now`,
`.runtime-now`, `.mqtt-window`, `.runtime-window` and `.after-fence` under
`deployment.pki`. The six final gate IDs remain unchanged.

A running substep repeats its elapsed time every 10 seconds. Native admission
also reports `checked=<finished>/<total>` at its start, during the heartbeat and
when all resource checks have finished. The total includes retained resources'
fresh reads; the finished count includes failed checks and is not a PASS count.
Updating the count does not restart the substep's elapsed-time clock. Progress
lines are not PASS/GO evidence and do not enter the plan hash or JSON report.
Errors, cancellation and completion join active checks and stop the heartbeat
before results are printed; private provider output and credentials are never
progress text.

## GO for an existing environment update

The current scripts report `scope=existing-environment-update`. GO authorizes
execution of that exact checked release/configuration/resource plan; it does not
declare deployment complete. Required update gates are the same in all three
environments:

| Gate | Current update evidence |
| --- | --- |
| `deployment.target` | Complete maintained target, original owners, unchanged configuration/release/bindings and durable-volume identities. |
| `deployment.release` | Immutable images, exact frozen workspace/service revisions, successful formal CI/publication and available images. |
| `deployment.execution` | Selected Kubernetes API access, native node-class capacity, original storage, execution prerequisites and server dry-run of the exact resource bodies; an OpenBao `OnDelete` update also requires named Pod restart rights and UID/resourceVersion conditional deletion admission in dry-run. Existing K8s upgrades do not require local Linode cluster ID or account-quota files; provider quota belongs to explicit infrastructure operations. |
| `deployment.pki` | Original synchronized runtime Secret bindings and the maintained runtime PKI checks at the start and end of the configured rollout window; identity/mount changes require explicit migration. |
| `deployment.compatibility` | Selected AM/Billing schema prerequisites, required restricted payment-simulator startup and migration setting, with candidate source/CI checked by the release gate. |
| `deployment.network` | Configured public CertIssuer ingress, authenticated mTLS and anonymous TLS denial; planned Kubernetes network objects also pass exact-body admission. |

## Detailed preflight checklist

This section describes the implemented default existing-environment update:
`./scripts/check-deployment-preflight.sh --environment <environment>`, which
selects `operation=upgrade`. It applies to dev, staging and prod through the same
engine; conditional checks follow the selected maintained configuration and
existing lifecycle profile. It does not describe `--operation image-upgrade`,
fresh bootstrap or initial Production activation as equivalent qualifications.

Preflight resolves configuration and formal CI images, observes the selected
Kubernetes environment, compiles the target in memory, then checks the six gates
above in stable order. **Every required failure prevents GO.** The consequence
column below explains the outage, data or security problem that this refusal
prevents; the script does not deliberately proceed into that consequence.
Missing evidence, timeout, cancellation and required SKIPPED results also mean
NO-GO. An early input/compiler failure can stop before the six gates exist.

### Before qualification: inputs and target compilation

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Environment, operation and maintained configuration | Environment/stack identity, supported architecture/adapter, explicit protection profile, valid settings and dependencies; prod cannot select a downgraded profile. Shared settings, environment overrides, adapter inputs and DNS settings use the common resolver. | Identify one supported target and the rules that apply to it. | Wrong-environment changes, missing components or a plan the adapter cannot execute. |
| Recovery and maintenance guard | The cooperating CLI checks recovery state/command locks across the local canonical environment stores. This local guard is separate from the selected cluster's execution lease. | Deployment must not race a restore or an unresolved recovery. | Concurrent changes can invalidate restored data or its evidence. |
| Canonical private inputs | Original operator credentials, flat runtime Secret catalog and kubeconfig are readable with required private permissions beneath `~/.config/rtk_cloud/<environment>/`; tracked conditional support/chart/backup configuration is included separately in the same snapshot. | Render from a complete, protected input snapshot. | Missing credentials, inconsistent configuration or exposure of private material. |
| Frozen Kubernetes identity | Selected context and HTTPS API, embedded CA and exactly one supported authentication method; embedded certificates/key and validity are verified. Unsupported credential refresh/transport/impersonation forms are rejected. | Keep every API call bound to the selected API and original identity. | API authentication fails or the target/identity changes after review. |
| Configured runtime media bucket | When media cutover is not required, complete provider inventory verifies the exact bucket, region and endpoint. Proven absence permits creation of that configured bucket, followed by fresh readback. | Ensure the configured media destination exists before workloads use it. | Media access fails or targets the wrong bucket/region; an unavailable inventory cannot safely justify creation. |
| Completed storage lifecycle, when required | Media cutover evidence when configured; dedicated OTA activation/cutover evidence when the OTA service is enabled. Original receipts/proofs must match environment, bucket, region and prefix. | A required data transition must already be complete and verifiable. | Workloads use an unactivated destination or incomplete migration and may lose access to existing objects. |
| Complete live inventory | Relevant namespaces, workloads and children, Services, configuration, access/network resources and applicable cluster/admission owners can be read. | Discover every existing component affected by a complete update. | An unmodeled service, sidecar, permission or route is silently omitted. |
| Renderer and image-role coverage | Each enabled existing workload/container has a maintained renderer and exact image role; unknown or ambiguously disabled components block compilation. | The maintained catalog must describe what will replace each active component. | Services disappear, retain an unqualified old image or lose required sidecars. |
| Original identity transition | Validated original PKI container/init-container references, credential sources, mounts, trust/CRL state and durable claims remain compatible with the target. | Ordinary updates reuse identity; an identity transition needs its own migration. | Private keys, trust, CRLs or service registration identities are lost or replaced. |
| Maintained PKI controller policy | Per-domain consumer/bundle receipt IDs, required App and Service/MQTT/OpenBao CRL refresh policy and explicit MFA are valid; production requires MFA. The retired global consumer policy is rejected. | Keep target adoption, refresh and authentication rules explicit. | Issuer activation or trust refresh stalls, receipts apply to the wrong domain, or authentication is weakened. This is target-policy validation, not future worker-health proof. |
| Immutable and lifecycle compatibility | Original workload selectors, Service allocations and storage contracts are preserved or require a supported separate migration; OpenBao storage/seal/TLS-sidecar profile must be compatible. | Kubernetes and durable state constrain what an ordinary update can change. | API rejection, wrong Service allocation, sealed/unusable PKI storage or data loss. |
| Required dependencies and stage ordering | Referenced Secrets, ConfigMaps and PVCs exist with verified ownership; dependencies are complete, acyclic and do not require a later phase. | Consumers need their original credentials/state and an executable prerequisite order. | Pods cannot mount/start, or execution stalls after partial changes. |
| Conditional support and backup components | Fixed Zammad chart/values render a complete supported inventory; enabled PostgreSQL backup configuration, credentials and runner are present. Disabled optional backup capability does not create a new backup prerequisite. | Include maintained enabled components without silently dropping or initializing them. | Support or enabled backup components break, or an unintended initialization changes data. |
| Controlled retirement | Only explicitly qualified obsolete owners are retired: stopped/drained legacy OTA registrar after core cutover and independent OTA presence, or eligible completed old support initialization Jobs. Secret/PVC/Namespace destruction is excluded. | Retire obsolete processes without removing active work or shared identity/data. | Duplicate registrations, interrupted OTA, lost initialization evidence or deleted durable state. |
| Stable compilation snapshot | Configuration/credential digest is re-read after rendering; after invocation/configuration/guard validation, preflight invalidates older GO before preparation and publishes compiled observations as NO-GO before qualification. | A plan must describe one consistent set of inputs. | A mixed or failed plan leaves stale approval eligible for execution. |

Repair the specific reported input/compiler boundary and repeat full preflight.
Do not create a local `release.json`, hand-edit generated bindings, fabricate a
receipt, regenerate identity or select another environment to clear a failure.

### Gate 1: `deployment.target` — target and drift protection

"Drift" means that an input or an original owner changed after the plan was
compiled. A resource name alone is insufficient: a deleted/recreated Deployment
has a different UID even when its name is unchanged. The common `Fence` is also
called by release, PKI, compatibility and network qualification, and again during
deployment. Its repeated reads are part of those stages' elapsed time.

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Exact plan | In-memory plan SHA-256 still matches the selected compiled plan. | Approval must refer to the actual plan being checked. | A modified plan can inherit another plan's approval. |
| Configuration and credential snapshot | Re-resolved maintained files and private snapshot match the compiled digest, including adapter and DNS inputs and required storage evidence. | Detect edits made after planning. | Execution uses credentials/settings different from those reviewed. |
| Release and source identity | Pinned release, workspace HEAD and selected service HEADs still match the frozen source revisions. | Tie runtime configuration and candidate code to one formal release. | A different commit or image inherits the original release evidence. |
| Existing resource owners | Fresh inventory contains the original recorded owner UIDs; unknown workloads and replacement/missing owners block. | Do not overwrite another controller's replacement or omit new active work. | Concurrent changes are overwritten or active services fall outside the update. |
| Durable-volume identity | Fresh PVC/PV allocation and binding evidence still matches the plan. | Data identity can change independently of a claim's name. | The update attaches replacement or unavailable data. |

On drift, compile and qualify a new plan. A stale GO is not permission to force
the old body onto the new owner.

### Gate 2: `deployment.release` — source and published images

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Exact source revisions | Workspace HEAD and every selected service HEAD match the frozen workspace gitlinks/image revisions. | Bind qualification to the commits that produced the selected formal artifacts. | A different commit or mismatched image borrows the selected release's evidence. |
| Formal service publication | Successful exact-source CI/Release evidence, correct run/attempt and unexpired `image-manifest.json` bind each required RTK image digest to its source. | A tag or unrelated green CI run does not establish publication of this candidate. | Missing or differently built images are selected. |
| Maintained provider versions | Configured upstream/provider repository versions come from maintained shared settings and are pinned to matching immutable registry digests. This check does not independently verify an official publisher or signature. | Keep PostgreSQL, ingress, OpenBao and other provider versions reproducible. | Mutable/unconfigured versions change without reviewed intent. |
| Pullability and platform | Each unique immutable image can be pulled for `linux/amd64`; roles sharing an image do not require repeated downloads. | Detect unavailable artifacts, registry access failures and missing target-platform images. | Candidate Pods cannot obtain or execute their image. Local pull success alone does not prove node-side registry networking. |
| OCI/source provenance | RTK image revision labels, source mapping and formal artifact evidence agree. | Verify that the pulled bytes belong to the claimed build. | A valid digest from a different source inherits the candidate's approval. |
| Backup runner, when enabled | Runner source/publication matches the selected workspace and its base-image provenance matches the selected PostgreSQL digest. | The enabled backup tool must match the database version it protects. | Backup commands are incompatible or their recovery evidence is unreliable. |

There is no repository-wide Git cleanliness requirement. Ordinary tracked or
untracked changes, including documentation and tests, do not automatically block
preflight. Actual maintained configuration, chart, credential and storage-evidence
inputs still participate in the compiled digest and must remain unchanged through
its repeated fences. Changing workspace/service HEAD or the selected image/source
revision still invalidates that frozen qualification.

Restore access or publish the missing exact-source artifact, then repeat the
same release qualification. Do not substitute a newer SHA's CI result.

### Gate 3: `deployment.execution` — ability to execute the plan

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Native node inventory | Complete, unambiguous Node names/UIDs, Ready state and valid allocatable CPU/memory. | Capacity needs actual cluster evidence. | Incomplete or corrupt inventory produces false capacity confidence. |
| Eligible node-class capacity | Maintained general/broker/database classes have the required nonterminating, schedulable, Ready Linux/amd64 nodes, per-node floors and aggregate request capacity. | Ensure the configured minimum infrastructure exists now. | Workloads may remain Pending or lack required resources. |
| Original PVC contract | Existing claim UID, nontermination, Bound phase, original volume, storage class, mode and access modes remain valid. | Preserve the original durable allocation. | Workloads mount unavailable or unintended storage. |
| Original PV/claim binding | Bound PV UID/spec and native claimRef match the original PVC UID, namespace and name and the plan's storage digest. | A matching PVC name cannot prove the same backing data. | Replacement storage or re-bound data is used unnoticed. |
| Read/create/update/delete rights | SelfSubjectAccessReview verifies required named reads/updates/deletes and collection creates. Retained resources need no write grant. | Detect denied operations before partial execution. | Rollout stops halfway with mixed old/new resources. |
| Inventory and rollout observation rights | Required list/watch, node list and original PV reads are allowed. | Deployment must verify owners, readiness, completion and deletion. | Completion cannot be established or concurrent owners are missed. |
| OpenBao `OnDelete` restart admission | For a changed applicable StatefulSet: original controller and ordinal Pod owners, named restart rights and exact UID/resourceVersion conditional delete dry-run. | `OnDelete` requires controlled Pod restarts to run the new revision. | Old images remain running or an unrelated/replacement Pod is deleted. |
| Local execution fence | Environment-local `deployment/.execution-active` is absent and inspectable. | Do not overlap an active or unreconciled attempt. | Journals and mutations from separate attempts are mixed. |
| Cluster execution lease | Exact environment lease is absent/readable; exact lease creation passes dry-run unchanged and named release permission is allowed. Preflight creates no persisted lease. | Prevent concurrent controllers and ensure execution can acquire/release its owner. | Concurrent updates occur or an interrupted owner is wrongly stolen. |
| Fresh resource baseline | Creates remain absent; existing UID and projected content still match the plan baseline before dry-run. Retained assets are checked even without a write body. | Preserve edits made between inventory and execution checks. | Another owner's resource or newer configuration is overwritten. |
| Exact server dry-run | Kubernetes accepts the concrete create/update/retirement body with owner/version conditions; admitted ID and approved projection agree under documented normalization rules. | Detect immutable fields, policy/admission, namespace and resource-quota rejection; detect unintended image/identity/configuration mutations. | The API rejects part of the update or admits a different, unreviewed target. |
| Completion capability | Required post-deploy acceptance adapter is available. | Successful writes alone cannot establish completed deployment. | The tool could report success without proving workloads, PKI and endpoints work. |

The native Kubernetes backend qualifies admission with at most eight concurrent
resource checks. Each check still performs its own fresh resource read, planned
absence or original UID/baseline validation, then exact-body server dry-run. Retained
resources still receive fresh reads and baseline validation without a write.
The native backend explicitly opts in; backends without that capability retain
serial admission. RBAC access reviews use their existing separate limit of four.

Checks are dispatched in plan order. A failure stops new dispatch; already
started checks finish before the first failure in that dispatched plan order is
returned. Cancellation stops new dispatch, reaches active API calls and joins
all workers before returning. Concurrent completion order cannot choose a
different blocker or leave qualification running after its result. These limits
apply to checks only: actual resource writes, retirement and rollout waits keep
the plan's dependency/phase order, and later fences retain fresh, uncached owner
and PVC/PV reads. The six qualification gates and other execution substeps remain
ordered.

Existing Kubernetes upgrades do not require local Linode cluster-ID or account
quota files. Explicit infrastructure operations have separate provider checks.
Native node capacity is a **lower-bound check**, not a scheduler simulation: it
does not subtract current usage or prove surge capacity, taint/affinity/PDB
behavior or placement of new volumes. Server dry-run accepts resource definitions
without starting candidate Pods. Actual scheduling and rollout remain deployment
acceptance responsibilities.

Fix the exact access/capacity/admission/fence failure. Reconcile an interrupted
execution through its original journal and lease; do not delete a fence merely
because time elapsed. Read failures retain a safe resource/error code and only
transient reads retry once, as specified below.

### Gate 4: `deployment.pki` — retained identity and trust

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Explicit validity window | Positive valid `DEPLOYMENT_UPGRADE_WINDOW_SECONDS`; required certificate/MQTT material is evaluated now and at the rollout cutoff. | Valid-now material must remain usable through update/acceptance. | Authentication or TLS fails midway through rollout. |
| Original Secret mirrors | Required catalog bindings match the selected canonical runtime Secret material in memory; missing, empty, malformed or mismatched data blocks. The specific handoff-only blank allowance applies when the handoff worker is not enabled. | Keep local deployment inputs and deployed credentials consistent. | Service authentication fails or one credential set silently replaces another. |
| Environment and algorithm scope | Applicable stack metadata, Account Manager environment and supported App/Device CSR algorithms agree with the selected environment. | Prevent interpreting identities under another environment or policy. | Cross-environment authentication/state or unsupported signing behavior. |
| Retained Secret certificate/key pairs | Complete matching parseable key pairs and leaf validity at both checked times for selected namespaces. | Detect unusable or expiring retained TLS material. | Startup or TLS/mTLS handshakes fail. This generic pair check is not full chain validation of every Secret. |
| Current PKI-backed consumers | Required nonzero PKI-backed Deployments have observed/current generation and matching updated/available replicas, with no unavailable replicas. | PVC-held identities also require functioning consumers. | An unavailable or stale consumer's identity is treated as proven. |
| Account Manager signing socket | Supported app/management socket settings agree; target preserves reviewed sidecar, socket volume, durable identity and initializer profile. | A Ready Pod must still have a coherent local signing path. | App certificate operations return errors despite a running Pod. |
| CertIssuer bootstrap inputs, when adopted | Caller/subject/session consistency, CA and cert/key settings, supported renewal address and subject policy, and required bootstrap NetworkPolicy selector. | Avoid a circular readiness dependency or ambiguous bootstrap ownership. | Bootstrap stalls or a wrong caller/session is used. Policy inspection does not simulate packet delivery. |
| Static serving chain, when applicable | Mounted serving Secret and issuer certificates build a server-authentication chain valid at the checked cutoff. | A static bootstrap listener still needs a complete valid chain. | Initial TLS fails before managed identity can take over. |
| Device trust profile and consumers | Selected fixed/automatic trust mode is coherent; supported required API/broker consumers have the appropriate Root pins, state, CRL enforcement and bundle acknowledgment configuration. | Required consumers must accept the selected Device trust policy. | Devices are rejected, new Product issuers remain pending or trust enforcement is weakened. Fixed development trust does not qualify production. |
| Root policy and removal history | Applicable Root IDs/state references agree with environment/domain/registry metadata and status; removed/compromised Roots have required distrust publication. | Keep active trust and security removals coherent. | A compromised/removed Root remains trusted or legitimate identities lose trust. |
| Mounted CRL profile and issuer scope | Reviewed Account Manager Service, CertIssuer Service and OpenBao transport CRL consumers retain explicit manifests, pinned CA fingerprints and original writable PVC state paths. Service successor bundle issuers have CRL coverage. | Revocation enforcement needs the correct issuer's durable state for every adopted successor. | A consumer loses revocation data or rejects renewed identities. The legacy Dev guard requires these three consumers; other environments validate their configured adopted consumers. |
| Signed installed CRL and metadata | Exact CRL PEM, positive number, CA signature/issuer/AKI, pinned state fingerprint/ID, digest and signed times; actual publication freshness and issuer lifetime constraints. | Reject forged, corrupt, stale, future-dated or wrong-issuer state. | Revoked identities are accepted or legitimate mTLS is rejected. |
| CRL survives the rollout cutoff | Signed `NextUpdate` is strictly later than the requested validity cutoff. Publication/freshness still uses actual current time. | An unexpired CRL can otherwise expire during update. | Service authentication breaks before rollout finishes. |
| Latest CRL and consumer acknowledgment | Installed number/digest equals the registry's latest publication and the exact required consumer has acknowledged that digest. | A fresh-looking file is insufficient proof of publication/adoption. | Stale revocations or an unaccepted CRL update remain in use. |
| Adopted Service registry integrity | Existing controller's read-only inventory matches environment, domain, selected Service intermediate and Root; issuance/revocation records and required Root/issuer CRL receipts are consistent; unresolved/pending/invalid/unpublished/missing-ack counts are zero. | Detect stranded signing and inconsistent lineage/security state. | Required service identities are unusable or revocations are not enforced. |
| Registry evidence and clock | Evidence is no more than five minutes old and no more than five seconds in the future relative to the verifier. | Evidence must describe the current registry with honest clocks. | Stale/future evidence qualifies changed state or healthy PKI is misdiagnosed by a faulty clock. |
| Bootstrap/signing ownership | Applicable active-session schema/uniqueness rules are correct; unresolved active bootstrap sessions and issuing requests block, including expired sessions still marked active. Unadopted profiles must not strand managed Service issuance. | An ambiguous original attempt must be reconciled before another signer proceeds. | Conflicting signing, duplicate credentials or an uncertain issuance outcome. |
| MQTT Foundation registration identity, when enabled | Original `service:mqtt` client identity/key/purpose and Account Manager listener identity/key/DNS; independent mutual chains, valid signed issuer CRL and unrevoked status at both checked times. | Foundation registration must authenticate under the original Service trust. | MQTT registration fails or wrong/revoked identities are accepted. |

Repair the original identity/trust/CRL through its managed lifecycle and repeat
qualification. Correct faulty clocks rather than weakening freshness rules.
Registry CRL publication and projection into copied listener Secrets are separate
operations: present validity does not promise automatic future renewal or Secret
synchronization. This gate does not run every certificate/domain's issuance,
renewal, replacement or revocation business workflow.

### Gate 5: `deployment.compatibility` — candidate runtime prerequisites

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Account Manager/Billing schema prerequisites | Committed `.sql` filenames from each selected CI image's frozen source revision all appear in the original databases' migration inventory, queried read-only. The full-update gate reads the Git tree at that revision. | Candidate binaries require the migration inventory belonging to their published source, independent of local file edits. | Startup or requests fail due to missing tables/columns. Filename presence is not a full schema/data compatibility proof for every service. |
| Simulator target executable | Applicable payment-simulator Deployment has the maintained candidate executable/specification. | Qualify the process that the plan will actually run. | The wrong executable or an incomplete Pod specification is deployed. |
| Simulator migration policy | Candidate resolves `BILLING_DB_MIGRATE_ON_STARTUP=false` from its explicit environment or supported Secret reference. Actual live database-role privileges are not audited by this setting check. | Runtime startup must not implicitly migrate schema. | Startup fails without CREATE rights or performs an unauthorized migration. |
| Simulator isolated startup | Exact published candidate starts and returns healthy against disposable PostgreSQL using a role without schema CREATE privilege; task-owned containers/network are cleaned up. | Test this known startup contract without touching environment data. | The simulator cannot start with its real restricted role; failed cleanup leaves local qualification artifacts. |

For this six-gate full update, local migration-file additions, edits or deletions
do not alter the selected image's required filenames. A missing/invalid source
revision, unreadable committed tree or empty migration inventory blocks the gate;
it never falls back to the working tree. The separate `--operation image-upgrade`
path retains its existing inventory behavior.

Apply missing migrations through the service owner's explicit procedure, then
repeat preflight. This gate does not reset/migrate live data and does not launch
every candidate workload for an end-to-end test.

### Gate 6: `deployment.network` — required public authentication path

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Target network resources | Planned Services, ingress and NetworkPolicies pass exact-body admission in the execution gate. | The API must accept the reviewed network definitions. | Routes/policies are rejected or changed by admission. This does not prove every proposed packet path. |
| Required public route | When the public host is configured, one exact owned nginx SSL-passthrough host/rule reaches the expected CertIssuer Service/port in the selected namespace. PKI policy also rejects trusted forwarded identity-header substitutes. | Preserve direct caller mTLS at the intended issuer. | The route reaches the wrong backend, loses the caller certificate or permits identity-header spoofing. |
| Original authenticated public probe | Current original Account Manager identity connects to the explicit public host with server trust/SAN verification. The managed owner profile additionally fences its identity snapshot before/after and expects the authenticated incomplete request's defined validation response. | Presence of an Ingress or an internal socket response cannot establish public authentication. | Public TLS/mTLS or the backend is unavailable even though resources appear Ready. |
| Anonymous denial | Same hostname/server trust without a client certificate must produce explicit certificate-required TLS denial, not a timeout, CA error or HTTP response. | Distinguish enforced mTLS from an unreachable server or an open anonymous route. | Anonymous access is allowed or an outage falsely passes as secure denial. |

The probe exercises the actual public path, including its DNS/TLS behavior; it
does not issue a certificate. With no maintained public host it does not invent
one. Other public endpoints, all proposed NetworkPolicy flows and representative
business workflows require their own acceptance checks.

### Results, artifacts and implementation boundaries

- The final required gate IDs are the six IDs above. Internal typed blockers
  supply safe resource/cause/next details; compiler blockers can appear before
  qualification. A nested blocker ID is not necessarily a separate final gate.
- GO requires every required gate to PASS with valid evidence for the unchanged
  plan. FAIL, ERROR, BLOCKED, required SKIPPED, missing evidence or `--fast` is
  NO-GO. Exit `0` means the requested check passed; `1` includes guard, preparation,
  compilation, qualification, private evidence/report publication and execution
  failures; `2` means invalid invocation/configuration before qualification.
- CLI output includes the plan SHA-256. Optional JSON reports belong under
  `deployment/reports/`; generated `deployment/identity-bindings.json` carries
  GO/NO-GO plus the matching plan/bindings hashes. Both are beneath the canonical
  environment store with 0700 directories/0600 files. Reports contain sanitized
  plan/gate summaries; bindings contain validated original owners, lifecycle
  references/mounts, public trust and retained identity settings. Bindings are
  private evidence, not a universally redacted artifact; recognized inline
  credential forms are rejected, but that rule is not a general secret detector.
- Preflight does not roll out workloads, retire owners, restart OpenBao Pods,
  acquire a persisted execution lease, reinitialize identity or complete storage
  cutover. The documented missing-media-bucket creation is its bounded provider
  write; access-review/dry-run requests and isolated local startup fixtures are
  also part of qualification.
- Deployment freshly compiles/requalifies the plan, requires matching GO evidence
  and `--plan-sha256`, fences each execution stage and verifies the actual target,
  workloads, PKI and required endpoints afterwards. Preflight `completed=false`
  is expected even with GO. Any changed frozen source revision, including a
  workspace documentation commit, requires new plan qualification.
- Preflight validates material through the configured cutoff; it does not prove
  that the complete update fits that duration. The current execution-window timer
  includes repeated qualification/admission, lease acquisition, mutation, phase
  fences, rollout and final acceptance. An earlier caller timeout can shorten it.
  Exhaustion stops the attempt; it must not be interpreted as completed rollout.
- The six-gate ordinary update is not initial Production activation. Fresh
  bootstrap, HA/quorum/custody, backup/restore drills and full business/PKI lifecycle
  qualification remain separate scopes. Missing adapters for their extra required
  gates block those operations rather than borrowing ordinary-update GO.

| Responsibility | Current implementation |
| --- | --- |
| Entry, six callbacks, reports and GO publication | [`deployment_unified_cli.go`](../scripts/go/rtk-cloud/deployment_unified_cli.go), [`qualification.go`](../scripts/go/rtk-cloud/internal/deploymentplan/qualification.go) |
| Inputs, compiler, retained dependencies and drift fence | [`deployment_unified_compile.go`](../scripts/go/rtk-cloud/deployment_unified_compile.go), [`plan.go`](../scripts/go/rtk-cloud/internal/deploymentplan/plan.go) |
| Media preparation and existing storage activation | [`deployment_storage_preflight.go`](../scripts/go/rtk-cloud/deployment_storage_preflight.go), [`deployment_storage.go`](../scripts/go/rtk-cloud/deployment_storage.go) |
| Formal CI selection and release evidence | [`deployment_unified_ci_images.go`](../scripts/go/rtk-cloud/deployment_unified_ci_images.go), [`deployment_unified_release.go`](../scripts/go/rtk-cloud/deployment_unified_release.go) |
| Capacity, storage, RBAC and execution lease | [`deployment_unified_node_capacity.go`](../scripts/go/rtk-cloud/deployment_unified_node_capacity.go), [`deployment_unified_storage.go`](../scripts/go/rtk-cloud/deployment_unified_storage.go), [`deployment_unified_access.go`](../scripts/go/rtk-cloud/deployment_unified_access.go), [`deployment_unified_lease.go`](../scripts/go/rtk-cloud/deployment_unified_lease.go) |
| Exact admission and guarded execution | [`execute.go`](../scripts/go/rtk-cloud/internal/deploymentplan/execute.go), [`deployment_unified_backend.go`](../scripts/go/rtk-cloud/deployment_unified_backend.go) |
| Runtime Secret/PKI, mounted CRL and registry evidence | [`secret_store.go`](../scripts/go/rtk-cloud/secret_store.go), [`secret_store_k8s_verify.go`](../scripts/go/rtk-cloud/secret_store_k8s_verify.go), [`secret_store_k8s_crl.go`](../scripts/go/rtk-cloud/secret_store_k8s_crl.go) |
| Candidate schema and simulator startup | [`deployment_check_image_upgrade.go`](../scripts/go/rtk-cloud/deployment_check_image_upgrade.go), [`deployment_check_simulator_startup.go`](../scripts/go/rtk-cloud/deployment_check_simulator_startup.go) |
| Actual public mTLS qualification | [`deployment_check_public_certissuer.go`](../scripts/go/rtk-cloud/deployment_check_public_certissuer.go), [`check-certissuer-app-mtls.sh`](../scripts/check-certissuer-app-mtls.sh) |

For the managed Account Manager socket profile, the public probe uses the
original `pkimanagement` owner identity in memory and connects to the explicit
public hostname. It fences the validated identity snapshot, verifies server
trust/SAN and anonymous client-certificate denial, and issues no certificate.
An internal socket response cannot qualify this public route. Probe scratch
belongs to the selected environment's private `deployment/tmp/` and is removed
on completion; private keys are never exported into files, arguments or reports.

Live Service registry evidence must match the selected environment, domain,
issuer and Root. Its timestamp may be at most five minutes old and at most five
seconds ahead of the checking machine. `deployment.pki.clock` identifies a
future timestamp; verify the checking machine and controller node against a
trusted time source and correct the faulty clock. `deployment.pki.registry.stale`
requires a new live inventory. These failures do not authorize replacement
identity or a fabricated report.

Mounted CRL expiry is checked against the same configured rollout deadline as
certificate expiry. The signed `NextUpdate` must be strictly later than that
deadline. `ThisUpdate`, registry freshness and consumer acknowledgments are
checked against the actual current time; the future deadline cannot make an
unpublished or future-dated CRL acceptable.

For core maintained workloads, an explicit empty container environment literal
keeps its environment name and represents `value` and an absent `valueFrom` as
null in the target projection. Kubernetes may omit these null fields without
causing a false admission mismatch. An injected nonempty value or credential
source still fails; this representation does not change runtime configuration.

Admission comparison permits only the client's refresh of
`kubectl.kubernetes.io/last-applied-configuration` as bookkeeping. It does not
remove that annotation from baseline drift checks, mutate the approved plan,
or relax existing UID, consumer configuration and security-policy checks.

For an existing `apps/v1` DaemonSet, admission may advance the native
`deprecated.daemonset.template.generation` counter by exactly one. The comparison
requires canonical positive counters, the same original UID and the matching
metadata generation increment; it restores the original counter only in its
comparison copy. Other fields, baseline drift checks and the approved plan stay
unchanged. Missing, malformed, backward or larger counter changes fail.

Admission failures retain their blocker ID and add at most eight mismatch field
paths, such as `$.spec.template.spec.containers[0].env[0].value`. Diagnostics
show schema fields and array indexes, mask dynamic or unknown keys with `*`,
and never print values or raw API output. If a safe bounded path cannot be
derived, the cause says `mismatch paths unavailable`; the failure verdict and
ownership checks remain unchanged.

Native resource and storage reads use the same frozen Kubernetes identity and
fresh, uncached GETs, with a 10-second request timeout, 20-second process limit
and bounded output. Only `KUBE_TIMEOUT`, `KUBE_RATE_LIMITED` and
`KUBE_UNAVAILABLE` retry once; permission, authentication, invalid-response and
successful missing-resource observations are not retried.
`deployment.access.resource` reports the exact resource and safe error code;
admission and execution preserve that typed access blocker without raw API
output or wrapper errors. Storage reads retain `deployment.access.storage` for
the exact PVC or PV; a read failure does not establish that data or ownership
was lost. Successfully observed missing, replaced,
terminating, unbound or changed PVC/PV bindings remain
`deployment.execution.storage` and require verified recovery or migration.

Missing or failed update evidence, changed inputs/owners, cancellation or `--fast`
is NO-GO. Actual scheduling/startup and endpoint health are verified after rollout
before completion is recorded. Ordinary update does not mutate bootstrap
credentials or initialize new backup/identity state. Managed issuer lifecycle,
fresh bootstrap and activation/restore qualification remain separate operations;
the later native inspection tools are not prerequisites imposed on older running
images merely to update their Pods.

`DEPLOYMENT_POSTGRES_BACKUP_ENABLED=false` is the common baseline. A maintained
backup component must explicitly select `true`; then its original configuration,
dedicated credentials and selected runner image remain required. Missing optional
backup configuration never creates a backup project during normal preflight.
Existing undeclared workloads still block planning; the flag cannot silently
remove or reset an already enabled component.

Preflight and deployment use the same common compiler, including selected
adapter defaults/overrides and resolved provider settings. The computed
configuration SHA-256 includes both sets; changing an adapter setting requires
new preflight qualification. It is a plan/report identifier, not a new
configuration file that operators must create.

Expected compiler failures must provide a structured public blocker with a
stable ID, the affected resource/container when known, a specific cause and a
repair/recheck operation. Missing renderers, unknown sidecars, conflicting
definitions and missing image roles must not collapse into a generic target
failure. External-tool failures report their safe compilation stage and access
or configuration repair boundary, without printing raw provider output, YAML,
credentials or private file contents. An unclassified failure is explicitly
`deployment.compilation.internal`, an implementation diagnostic gap rather than
an instruction to select another release.

## Retired static PKI templates

The production binary no longer contains the static CertIssuer or FactoryEnroll
Deployment templates or their PKI initialization/reconciliation block. Historical
Kubernetes samples are test fixtures only. Public plan/preflight/create/upgrade
entries use the maintained managed-PKI engine, with no private-file detection or
static renderer fallback. Scoped legacy video-cloud deployment is rejected
before mutation rather than rebuilding identities through deleted templates.

## CI image selection

Preflight reads formal service `image-manifest.json` artifacts directly for the
selected workspace HEAD and its pinned service revisions; the selection is held in
memory and included in the plan digest. Deployment repeats selection and requires
the approved plan SHA-256. Git status is not a prerequisite; exact HEAD/source
matching and the maintained-input digests remain required. Missing or invalid CI
publication is NO-GO. Local `release.json` selection and freeze-release file
generation are retired. Provider
image versions remain maintained configuration and are registry-digest pinned.

### Preflight-produced identity observations

An existing-environment preflight does not require a pre-existing private
`identity-bindings.json`. It obtains actual owners and existing identity references
from the selected API and compiles the maintained target in memory. It records
existing UIDs only; newly planned resources have no UID until creation. Target
mounts/configuration are renderer output, and persistent contents or seal state
cannot be inferred from a Pod volume reference.

Preflight publishes the current result beneath the canonical environment store
as `deployment/identity-bindings.json`; complete GO is bound to the exact plan and
observed-bindings hashes. After local invocation/configuration/guard validation,
failed/fast preflight invalidates previous GO. Deployment
must freshly compile the same plan, verify the matching GO record and fence
ownership during execution. An unsupported existing OpenBao profile remains a
compatibility blocker, not an instruction to manufacture a bindings file.

### Historical one-off execution records

A standalone Job with an explicit Complete/Failed terminal condition, zero active
workers and only terminal Pods is historical evidence, not an enabled update
component. Preflight and execution inventory checks preserve it without requiring
a new renderer, old image publication or retired temporary credentials. Pod
lineage must match the original Job UID/API version. Running, partial, scheduled,
foreign or ambiguous owners remain NO-GO; no online cleanup is performed by
preflight and no historical commands are imported into the candidate plan.

A terminal standalone Pod is likewise historical only when it has no owner
references and `restartPolicy=Never`. Running, restartable or ambiguous Pods
remain required inventory blockers; historical inspection Pods are not deleted
or replayed by preflight.
