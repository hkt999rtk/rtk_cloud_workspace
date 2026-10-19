# Unified Deployment Readiness Standard

Status: accepted deployment policy; existing-update checks implemented; qualification is specific to an environment, release and plan.

Owner: `rtk_cloud_workspace`; service owners supply candidate and recovery checks.

Last reviewed: 2026-10-09.

Authority: deployment scope and readiness policy. The [deployment architecture](cloud-deployment-architecture.md)
owns configuration/provider boundaries, and [Platform PKI](../repos/rtk_cloud_contracts_doc/platform_pki.md)
owns identity, signing and recovery policy. [ADR 0003](adr/0003-unified-environment-deployment.md)
records the replacement decision. Dated observations and local fixtures are evidence,
not permission to relax this policy.

## Current script delivery scope

The [current script flow and flow chart](deployment-operations.md#current-script-flow)
map the public entries to qualification, execution, integrated final health
checks and independent health rechecks. This standard defines their required
gates and completion policy.

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
  storage cutover is requested for `create` or a profile without independent
  VideoStorage registration. An existing upgrade with that independent role
  enabled requires its original media allocation; other provider probes remain
  read-only. It
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

The ordinary `--operation create` preflight automatically qualifies supported
unfinished dev Service bootstrap/enrollment checkpoints through the existing
initialized-OpenBao recovery engine. All six original gates must PASS for the
exact parent, release, original identity and checker version. Its reported scope
is `original-generation-recovery` with `completed=false`; it authorizes neither
automatic rollout nor replacement of OpenBao/PKI identity. Canonical owner/fence
and original journal remain intact. Create without a fence still requires verified
target absence; create with an unfinished owner shows original-generation recovery
mode. Unknown or uncertain checkpoints never fall back to fresh creation and
remain NO-GO. A READY Device trust interruption additionally requires exact
original child UIDs/bodies, native ready-Job operation and complete registry
identity sets, existing OpenBao signer and the broker Service CRL policy forward
plan; it must not select absent-issuer bootstrap recovery. See [formal recovery preflight](deployment-operations.md#recovery-preflight-through-the-formal-script).

Preflight and deployment report `[RUNNING]` stage IDs while resolving provider
and formal CI images, observing Kubernetes, compiling the target and qualifying
required gates. Execution qualification identifies the current substep as
`deployment.execution.node-capacity`, `.storage`, `.permissions`, `.lease` or
`.admission`; `.provider-quota` applies only to operations requiring that check.
PKI qualification identifies `.before-fence`, `.bindings`, `.mqtt-now`,
`.runtime-now`, `.mqtt-window`, `.runtime-window` and `.after-fence` under
`deployment.pki`. The six final gate IDs remain unchanged.
During actual execution, English stage messages identify the current phase's
checks, resource apply and rollout wait. The progress context spans execution,
so its progress heartbeat follows current work through writes and waits.
Each message includes a maintained `task` description. Resource execution shows
the original `Kind/namespace/name`, planned action and current `read`, `dry-run`,
`apply`, `wait` or `complete` step. `resources=<finished>/<total>` counts finished
resource operations, including retained resources; it is not a percentage of
complete deployment acceptance. Final resource verification, exact target
owner/image checks and required Secret, PKI, workload and endpoint health still
follow the resource loop inside deployment. Full business-flow/data acceptance
is a separate test scope.

Active query summaries describe only currently running operations. They show
safe resource identifiers and fixed actions, up to three operations plus the
remaining count, active/finished-query counts, and remove each
operation when it settles. Cache hits do not invent API activity. Release
verification identifies the maintained image role
and its source, image, CI or artifact substep; references, URLs, SQL, commands,
credentials and subprocess output never become progress labels.

In an interactive terminal, both entries default to a continuously updated
colored inline progress bar and spinner, with the English task
description and current safe resource/readiness details. A bar or percentage
uses only an actual known admission-check or resource-operation total; it does
not represent overall GO or completed acceptance. Work without a known total
shows a spinner without an invented percentage. Redirected output and
`TERM=dumb` retain plain lines and the 10-second progress heartbeat. Elapsed
times and query durations are recorded only in the private per-attempt
`deployment/logs/<action>-<UTC-time>-<unique-id>.log` file in the selected environment
store; user output omits timing diagnostics and successful step lines. Running
work, failures/blockers and the final GO/NO-GO remain visible; successful results
remain in the private log and JSON evidence.

Illustrative plain-output heartbeat lines below use example counts and public
resource names; they are not live deployment evidence. The rollout limit is read from the checked
configuration, whose shared value is 300 seconds:

```text
[RUNNING] deployment.execution.workload.apply resources=293/352 task="Apply resources in dependency order" action=update step=read resource=Deployment/video-cloud-dev-support/zammad-elasticsearch
[RUNNING] deployment.execution.workload.apply resources=293/352 task="Apply resources in dependency order" action=update step=dry-run resource=Deployment/video-cloud-dev-support/zammad-elasticsearch
[RUNNING] deployment.execution.workload.apply resources=293/352 task="Apply resources in dependency order" action=update step=apply resource=Deployment/video-cloud-dev-support/zammad-elasticsearch
[RUNNING] deployment.execution.workload.rollout resources=293/352 task="Wait for rollout readiness" waiting-for=Deployment/video-cloud-dev-support/zammad-elasticsearch ready=0/1 updated=1/1 rollout-limit=300s still running
[RUNNING] deployment.execution.verification resources=352/352 task="Verify updated resources; deployment is not complete yet"
[RUNNING] deployment.execution.acceptance resources=352/352 task="Run post-deploy acceptance; deployment is not complete yet"
```

Readiness observations run only while the original wait is active, and their
poller is cancelled and joined before the wait result is reported. Observed
`ready`/`updated` fractions do not convert a failed or incomplete wait into PASS.

In plain-output mode, a running substep repeats its current work every 10 seconds.
Native admission also reports `checked=<finished>/<total>` at its start, during the heartbeat and
when all resource checks have finished. The total includes retained resources'
fresh reads; the finished count includes failed checks and is not a PASS count.
Updating the count does not restart the substep's elapsed-time clock. Progress
lines are not PASS/GO evidence and do not enter the plan hash or JSON report.
Errors, cancellation and completion join active checks and the reporter, then
finish/clear the inline terminal line before failure diagnostics, final GO/NO-GO or report
output is printed. Private provider output and credentials are never progress
text.

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

For a new target, select `--operation create` explicitly. Fresh dev/staging
qualification checks the selected canonical execution fence, recorded removal
proofs and complete native provider inventory without an existing kubeconfig or
cluster Lease. Existing stack owners, retained volumes, uncertain allocations,
inaccessible recorded IDs and incomplete inventory block creation.

An explicitly selected brand-new deployment generation may establish new
OpenBao and Service PKI identities, even if the previous generation retained
keys under the same environment name. Qualify target absence and idle execution/
recovery, preserve and segregate previous-generation material, then repeat full
create qualification. Original identity continuity remains required for an
upgrade, retry or restore; missing state alone never authorizes replacement.
Preflight does not generate keys or CAs. Follow the
[new deployment identity lifecycle](deployment-operations.md#new-deployment-identity-lifecycle).

The create plan freezes maintained provider requests, exact-source formal CI
images and native bootstrap capabilities. Required gates cover target absence,
release provenance, provider capacity/storage, initial Account/OpenBao state and
native Service, Device, App, MQTT and OpenBao TLS ceremonies. Preflight does not
allocate the cluster, generate runtime identity, invent future UIDs or complete
the ceremony. Deployment reuses valid exact-plan evidence, journals each native
allocation, then admits real Kubernetes objects and waits for authenticated
native trust receipts before issuer activation. It observes actual identity
bindings and performs normal deployment and acceptance afterward. Initial
Production activation remains subject to its separate protected profile.

Preflight validates invocation/target configuration and maintenance/recovery
guards, invalidates previous GO, then checks the canonical local execution fence
and selected cluster Lease for occupancy. An occupied or unreadable fence/Lease
stops with early NO-GO before media-bucket preparation, CI image resolution,
compilation or full gates, without a qualified plan or other PASS results. This
focused read-only guard never acquires/deletes a Lease or runs Lease create
dry-run. The execution gate repeats fresh occupancy/admission checks to catch
races. After the guard, preflight resolves formal CI images, observes the selected
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
| Early execution occupancy guard | After validated inputs/maintenance guards and previous-GO invalidation, preflight checks canonical `deployment/.execution-active` then the selected cluster Lease. Occupancy or unavailable inspection stops before bucket preparation, CI resolution or compilation. Reads only; no Lease mutation or create dry-run. | Refuse an overlapping/unverified attempt before costly qualification or bucket preparation. | Preflight performs unnecessary work or provider preparation for an execution that is already blocked. |
| Canonical private inputs | Original operator credentials, flat runtime Secret catalog and kubeconfig are readable with required private permissions beneath `~/.config/rtk_cloud/<environment>/`; tracked conditional support/chart/backup configuration is included separately in the same snapshot. | Render from a complete, protected input snapshot. | Missing credentials, inconsistent configuration or exposure of private material. |
| Frozen Kubernetes identity | Selected context and HTTPS API, embedded CA and exactly one supported authentication method; embedded certificates/key and validity are verified. Unsupported credential refresh/transport/impersonation forms are rejected. | Keep every API call bound to the selected API and original identity. | API authentication fails or the target/identity changes after review. |
| Configured runtime media bucket | Complete provider inventory verifies the exact bucket, region and endpoint. When media cutover is not required, `create` or profiles without independent VideoStorage retain proven-absence bucket creation followed by fresh readback. An existing upgrade with enabled independent VideoStorage requires its original allocation; absence or drift is NO-GO without bucket creation. | Ensure the configured media destination exists while preserving an original independent service's allocation and the separate create preparation path. | Media access fails or targets the wrong bucket/region; an unavailable inventory cannot safely justify creation, and replacement of an original bucket loses its data lineage. |
| Completed storage lifecycle, when required | Media cutover evidence when configured; dedicated OTA activation/cutover evidence when the OTA service is enabled. Original receipts/proofs must match environment, bucket, region and prefix. | A required data transition must already be complete and verifiable. | Workloads use an unactivated destination or incomplete migration and may lose access to existing objects. |
| Complete live inventory | Relevant namespaces, workloads and children, Services, configuration, access/network resources and applicable cluster/admission owners can be read. | Discover every existing component affected by a complete update. | An unmodeled service, sidecar, permission or route is silently omitted. |
| Renderer and image-role coverage | Each enabled existing workload/container has a maintained renderer and exact image role; unknown or ambiguously disabled components block compilation. | The maintained catalog must describe what will replace each active component. | Services disappear, retain an unqualified old image or lose required sidecars. |
| Original identity transition | Validated original PKI container/init-container references, credential sources, mounts, trust/CRL state and durable claims remain compatible with the target. | Ordinary updates reuse identity; an identity transition needs its own migration. | Private keys, trust, CRLs or service registration identities are lost or replaced. |
| Derived issuer startup configuration | For the main CertIssuer and FactoryEnroll, the original referenced runtime Secret is identified by role keys. CA paths follow its read-only file mapping; database credentials remain Secret references. The main CertIssuer candidate has required startup fields, the managed signer/TTL and valid caller patterns. Manual CA-path/signer/port inputs and missing or ambiguous bindings block compilation. | Preserve the original identity while rendering complete settings from the shared topology. | An API-admissible Deployment can lose required process settings and fail to start. This static check does not execute the image or prove certificate files, database access or live health. |
| Maintained PKI controller policy | Per-domain consumer/bundle receipt IDs, required App and Service/MQTT/OpenBao CRL refresh policy and explicit MFA are valid; production requires MFA. The retired global consumer policy is rejected. | Keep target adoption, refresh and authentication rules explicit. | Issuer activation or trust refresh stalls, receipts apply to the wrong domain, or authentication is weakened. This is target-policy validation, not future worker-health proof. |
| Immutable and lifecycle compatibility | Original workload selectors, Service allocations and storage contracts are preserved or require a supported separate migration; OpenBao storage/seal/TLS-sidecar profile must be compatible. | Kubernetes and durable state constrain what an ordinary update can change. | API rejection, wrong Service allocation, sealed/unusable PKI storage or data loss. |
| Required dependencies and stage ordering | Referenced Secrets, ConfigMaps and PVCs exist with verified ownership; dependencies are complete, acyclic and do not require a later phase. | Consumers need their original credentials/state and an executable prerequisite order. | Pods cannot mount/start, or execution stalls after partial changes. |
| Conditional support and backup components | Fixed Zammad chart/values render a complete supported inventory; enabled PostgreSQL backup configuration, credentials and runner are present. Disabled optional backup capability does not create a new backup prerequisite. | Include maintained enabled components without silently dropping or initializing them. | Support or enabled backup components break, or an unintended initialization changes data. |
| Controlled retirement | Only explicitly qualified obsolete owners are retired: stopped/drained legacy OTA registrar after core cutover and independent OTA presence, or eligible completed old support initialization Jobs. Secret/PVC/Namespace destruction is excluded. | Retire obsolete processes without removing active work or shared identity/data. | Duplicate registrations, interrupted OTA, lost initialization evidence or deleted durable state. |
| Stable compilation snapshot | Configuration/credential digest is re-read after rendering; after invocation/configuration/guard validation, preflight invalidates older GO before preparation and publishes compiled observations as NO-GO before qualification. | A plan must describe one consistent set of inputs. | A mixed or failed plan leaves stale approval eligible for execution. |

Repair the specific reported input/compiler boundary and repeat full preflight.
Do not create a local `release.json`, hand-edit generated bindings, fabricate a
receipt, regenerate an existing generation's identity or select another
environment to clear a failure. Explicit new-generation bootstrap follows the
lifecycle above.

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
| Singleton RollingUpdate surge headroom | `deployment.execution.surge-capacity` covers existing update-action singleton Deployments with changed Pod templates, zero effective `maxUnavailable` and positive `maxSurge`. Fresh native Node and all-namespace Pod GETs subtract assigned nonterminal CPU/memory reservations, including non-Ready and terminating Pods, conservatively combining requests, status allocations, init/sidecar overlap and Pod overhead. Observed native Pod aggregate requests/allocations are counted as the maximum of aggregate and computed container totals, without double counting. One whole overlapping candidate Pod must fit a supported eligible node. Recreate, zero-surge and unchanged templates skip the guard; unsupported candidate placement/resource profiles block. | Detect an overlap that cannot fit the current native request snapshot before writes, without requiring application readiness or creating a candidate Pod. | The old singleton remains allocated while its replacement cannot schedule, causing rollout timeout. A passing snapshot still cannot guarantee future scheduling or startup. |
| Original PVC contract | Existing claim UID, nontermination, Bound phase, original volume, storage class, mode and access modes remain valid. | Preserve the original durable allocation. | Workloads mount unavailable or unintended storage. |
| Original PV/claim binding | Bound PV UID/spec and native claimRef match the original PVC UID, namespace and name and the plan's storage digest. | A matching PVC name cannot prove the same backing data. | Replacement storage or re-bound data is used unnoticed. |
| Core API MQTT TLS and management identity | The common renderer derives TLS transport, Controller/CertIssuer endpoints and renewal trust, and CA/CRL paths from maintained Services and original mounts. `deployment.execution.mqtt-transport` checks the exact candidate, original UID-bound source keys, independent Roots, original managed Service identity state on its writable PVC, and CRL state paths. It also requires the exact pinned directory-only `core-identity-permissions` initializer, explicit runtime UID 10001, original PVC mount and Recreate strategy; every Service CRL persistence parent must be covered by the prepared root. Unsupported permission recipes/parents block with `deployment.configuration.core-crl-permissions`. Missing identity/renewal trust, static/bootstrap management credentials, manual derived values or ambiguous/escaping mounts block with `deployment.configuration.mqtt-transport`. Application readiness is not a prerequisite; authenticated native CRL adoption is verified after deployment. | Kubernetes admission does not evaluate the application's MQTT PKI configuration or CRL filesystem contract. | A retained Root/name with missing TLS/CA or managed client identity passes admission but exits at application startup; CRL atomic persistence fails when the original PVC root is not writable by the API UID. |
| App PKI TURN lifetime | The App API common profile renders a one-minute TURN credential TTL. When App PKI and TURN are enabled, the exact candidate must contain an explicit positive `VIDEO_CLOUD_TURN_CREDENTIAL_TTL` no greater than one minute; missing, indirect or invalid values block with `deployment.configuration.app-turn-ttl`. Other API roles retain their own policy; no running App API is needed. | Match the formal API's App revocation cutoff before writing resources. | Kubernetes admission succeeds but the App API rejects its configuration at startup. |
| Device API private MQTT callback and TLS projection access | Derive the broker CA path from the original read-only `mqtt-callback` mount/item and retain the `emqx-pki` client role. Check the separate listener, original server TLS pair, required broker auth Secret reference, enforced ACLs and disabled trusted headers; verify the original public CA ConfigMap UID/key. The common profile supplies explicit non-root UID/GID 10001 and supplemental group 0 for group-read access to the original root:root 0440 TLS Secret projection. Candidate checks validate its read-only mount, item paths, effective modes and process UID/groups, including container overrides; no `fsGroup`, PVC metadata change, Secret/identity copy or operator setting is needed. Missing or inconsistent inputs block before writes without requiring a running API/broker or inspection Pod. Actual TLS startup and listener operation are verified after deployment. | Match the formal API's complete independent broker mTLS listener and prove projected credential readability from the candidate before writes. | Kubernetes admission succeeds but the API rejects partial callback configuration or cannot read its original TLS files at startup. |
| Existing LogIngester MQTT transport | Preserve its managed Service identity state and independent Root pins; derive TLS, Service endpoints and CA/CRL paths from original mounts. Verify complete candidate settings, original public CA/CRL source UIDs/keys, independent Roots, original writable MQTT PVC mapping and original state paths. Service CRL snapshots retain `/var/lib/mqtt-pki/identity/service-crls/*.json`; MQTT CRL snapshots retain `/var/lib/mqtt-pki/crls/*.json`. Missing or unsafe inputs block with `deployment.configuration.log-mqtt-transport`. Preflight requires neither runtime readiness, directory inspection Pods, actual CRL writes nor ACKs. Post-deploy acceptance reads both caches from the updated workload, verifies pinned issuers, signed content, validity and metadata against the latest registry number/digest, and requires each exact native consumer's ACK. Existing same-digest caches may be reloaded and verified without rewriting; no new CRL/key or write canary is required. | Preserve the original consumer and validate formal-image startup inputs before execution, then directly prove durable cache state and native adoption during acceptance. | The renderer drops TLS or managed identity fields while keeping a Root pin, causing startup rejection or loss of authenticated CRL/receipt traffic; requiring writes or ACKs before rollout blocks repair of a stopped consumer, while checking only a projected manifest after rollout misses stale or unadopted cache state. |
| Managed LogIngester singleton update | Candidate preflight requires one replica, `Recreate` and the original writable `mqtt-pki-state` PVC mapping. The common profile retains the original identity, state contents and paths; it requires no future replacement Pod readiness. Actual startup, durable CRL caches and native ACKs remain post-deploy acceptance checks. | Keep one writer of the original identity/cache state and replace the old Pod without requiring extra surge Pod capacity. | Rolling overlap can require capacity for an extra Pod and allow competing state writers; a pending replacement can cause a rollout timeout. |
| LogIngester selected environment and CRL authority scope | Derive `VIDEO_CLOUD_ENV` from the selected maintained environment and require the matching namespace. Validate both original UID-bound public manifests: selected issuer environment, separate MQTT/Service trust domains, active/retiring authority, pinned signing chain and unique original writable PVC state paths. Mismatch blocks with `deployment.configuration.log-mqtt-scope`. No running consumer, cache read/write or ACK is needed. | Match the formal native CRL consumer's startup scope without changing original authority or depending on runtime outcomes. | A legacy environment literal conflicts with original issuer scope, or a malformed/foreign manifest causes startup rejection despite Kubernetes admission. |
| Independent OTA candidate storage and startup | For `video-cloud-otaservice` only, project maintained OTA storage mode and firmware bucket/region/prefix from the selected storage plan. Derive endpoint/type from current read-only provider metadata for the exact existing bucket; preserve original credential Secret references and verify source UID/key bindings plus the formal startup policy. Missing/inconsistent inputs block with `deployment.configuration.ota-startup`. No running OTA workload, inspection Pod, new bucket/key or fabricated allocation/activation receipt is required. Actual startup and storage access remain post-deploy. | Supply the formal independent OTA process's complete intended startup configuration before writes. | A compiler omission leaves bucket/region/endpoint empty despite valid maintained intent, or mismatched credential sources make the service fail at startup. |
| Independent VideoStorage candidate startup | `deployment.configuration.video-storage-startup` qualifies the enabled role's direct upload plus complete media S3, PostgreSQL and registration inputs. Canonical `deployment/storage/media-render-observation.json` records a genuine provider GET; its stable public metadata/hash is frozen into the input/plan digest and compared to a fresh GET. An existing upgrade preserves original allocation/Secret/key/file references, clip key, identity and state; missing/changed allocation is NO-GO without replacement bucket creation. Core's direct-upload flag remains separately maintained. Create checks candidate settings/allocation while retaining existing preparation/bootstrap ownership checks; it requires no future resource UID. No candidate Pod readiness, registration lease or business write is required. Actual registration/readiness and clip-upload/business writes remain post-deploy acceptance. | Keep independent-role startup inputs complete even when Core's generic upload runtime is disabled, without depending on the repaired service already running. | A shared flag or omitted allocation projection disables independent clip upload or leaves S3 fields empty, so admission passes while startup fails. |
| Existing OTA external schema prerequisites | For an existing OTA update with no related owner schema migration, `deployment.compatibility.ota-schema` uses the original UID-fenced PostgreSQL owner/Pod and a read-only query to verify startup tables/columns. No OTA readiness, DDL or workload initialization is involved. A related owner migration that could produce these objects requires migration-order/evidence boundary qualification instead of demanding the future schema already exist. | Verify genuine original database prerequisites while preserving the deployment/migration boundary. | Startup fails against missing original objects; demanding a planned migration's future outputs before it runs creates a deployment deadlock. |
| OTA immutable registration manifest | `deployment.compatibility.ota-registration` consumes the selected exact-image CI descriptor `image-manifest.json` → `service_registration_manifests.ota`, bound to source/image provenance and the plan hash. Normalize option codes and dependency lists exactly as Account Manager's manifest digest, retaining full names/descriptions; compare same-version immutable content, original certificate-bound workload/instance approval and existing publication metadata using one bounded read-only query against the original UID-fenced Account Manager database and frozen identity. Missing/incompatible evidence blocks before writes. No registry write, OTA readiness, current lease or heartbeat is needed; genuine native registration/lease/heartbeat/Ready are verified after deployment. | Catch immutable manifest conflicts before the candidate starts, while retaining the registry's original publication/status and runtime acceptance boundary. | A binary reuses an immutable version with different content and fails registration; requiring its future lease before rollout prevents repair. |
| Read/create/update/delete rights | SelfSubjectAccessReview verifies required named reads/updates/deletes and collection creates. Retained resources need no write grant. | Detect denied operations before partial execution. | Rollout stops halfway with mixed old/new resources. |
| Inventory and rollout observation rights | Required list/watch, node list and original PV reads are allowed. | Deployment must verify owners, readiness, completion and deletion. | Completion cannot be established or concurrent owners are missed. |
| OpenBao `OnDelete` restart admission | For a changed applicable StatefulSet: original controller and ordinal Pod owners, named restart rights and exact UID/resourceVersion conditional delete dry-run. `DeleteOptions` contains `dryRun: ["All"]` in its body; a URL query alone is insufficient when a body is supplied. | `OnDelete` requires controlled Pod restarts to run the new revision; admission must leave the existing Pod untouched. | Old images remain running, admission accidentally restarts a service, or an unrelated/replacement Pod is deleted. |
| Local execution fence | Environment-local `deployment/.execution-active` is absent and inspectable. Execution releases its own fence after confirmed Lease cleanup on success, failure, cancellation or timeout; crashes or failed cleanup can leave a fence requiring reconciliation. | Do not overlap an active attempt or silently clear an unverified retained fence. | Journals and mutations from separate attempts are mixed. |
| Cluster execution lease | Exact environment lease is absent/readable; exact lease creation passes dry-run unchanged and named release permission is allowed. Preflight creates no persisted lease. | Prevent concurrent controllers and ensure execution can acquire/release its owner. | Concurrent updates occur or an interrupted owner is wrongly stolen. |
| Fresh resource baseline | Creates remain absent; existing UID and projected content still match the plan baseline before dry-run. Retained assets are checked even without a write body. | Preserve edits made between inventory and execution checks. | Another owner's resource or newer configuration is overwritten. |
| Exact server dry-run | Kubernetes accepts the concrete create/update/retirement body with owner/version conditions; admitted ID and approved projection agree under documented normalization rules. | Detect immutable fields, policy/admission, namespace and resource-quota rejection; detect unintended image/identity/configuration mutations. | The API rejects part of the update or admits a different, unreviewed target. |
| Completion capability | Required post-deploy acceptance adapter is available. | Successful writes alone cannot establish completed deployment. | The tool could report success without proving workloads, PKI and endpoints work. |

The native Kubernetes backend qualifies admission with at most twelve concurrent
resource checks. Each check still performs its own fresh resource read, planned
absence or original UID/baseline validation, then exact-body server dry-run. Retained
resources still receive fresh reads and baseline validation without a write.
The native backend explicitly opts in; backends without that capability retain
serial admission. Independent native admission, storage, namespace inventory and
RBAC checks share the same maintained limit of twelve; dependent operations stay
ordered.

Checks are dispatched in plan order. A failure stops new dispatch; already
started checks finish before the first failure in that dispatched plan order is
returned. Cancellation stops new dispatch, reaches active API calls and joins
all workers before returning. Concurrent completion order cannot choose a
different blocker or leave qualification running after its result. These limits
apply to checks only: actual resource writes, retirement and rollout waits keep
the plan's dependency/phase order. The six qualification gates and other execution
substeps remain ordered.

Per-resource readiness and deletion waits resolve the required positive
`DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS` from maintained architecture configuration.
It must be a canonical positive decimal integer, at most 86400 seconds, and fit
within the selected minimum PKI validity margin. The shared Kubernetes baseline is
`300` seconds. Native wait commands use that
value for both the rollout/condition timeout and Kubernetes request timeout;
their local process budget is the selected wait plus 30 seconds (330 seconds
with the shared baseline). The process allowance permits command termination and
result collection; it does not extend the requested readiness window. An earlier
caller deadline always wins.

This resource wait setting is separate from
`DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS=3600`, which controls whether evidence may
authorize a new attempt, `DEPLOYMENT_UPGRADE_WINDOW_SECONDS=3600`, which defines
the minimum certificate/CRL validity margin, and the command's overall `--timeout`
(for example `90m`). Evidence age and the minimum margin do not impose an
execution timer after admission. The selected
rollout setting participates in configuration and plan hashing. Changing it
requires fresh full preflight and a newly reviewed plan SHA-256; an earlier GO
does not authorize execution with the changed setting.

At each execution phase boundary, the native backend obtains one fresh inventory
and shares it between the input fence and the phase-wide validation of completed,
future and retained resources. IDs absent from that inventory receive named GETs;
absence from a collection alone does not prove resource absence. The snapshot is
discarded before actual writes and never reused across phases. Each resource
write still obtains a fresh named read, validates UID/baseline/resourceVersion
and performs exact-body dry-run before mutation. Every existing phase fence and
the full dependency scope remain required.

Native PVC/PV qualification reuses objects present in the same current inventory.
Missing claims or volumes receive named reads, with at most twelve independent
storage checks active. PVC/PV UID, allocation, Bound status, claimRef and binding
digest validation remain required. Errors are selected in plan order, new dispatch
stops on failure/cancellation and active checks join before returning.
Namespace inventory collections likewise use at most twelve independent reads,
preserving the original namespace order before the existing sequential cluster
and webhook inventory. Failure/cancellation stops dispatch and joins active
processes; failures remain deterministic in original namespace order. The query
scope and frozen Kubernetes identity are unchanged.

Existing Kubernetes upgrades do not require local Linode cluster-ID or account
quota files. Explicit infrastructure operations have separate provider checks.
The general native node-capacity check is a **lower-bound check**, not a scheduler
simulation: it does not subtract current reservations or prove rolling overlap.
The separate singleton surge guard uses the current request snapshot and supported
node selectors/taints; hard placement constraints and unsupported resource profiles
block rather than supply a scheduling proof. Neither check guarantees scheduler,
PDB or volume-placement outcomes. Server dry-run accepts resource definitions
without starting candidate Pods. Actual scheduling and rollout remain deployment
acceptance responsibilities.

Fix the exact access/capacity/admission/fence failure. A gracefully failed
execution releases its own Lease and archives the local fence's owner/process
evidence under the original plan's unique
`deployment/rollback/<plan-sha256>/execution-owner-*/owner/`
while retaining its plan and journal. Repeat complete preflight against actual
state and review the newly compiled plan; never resume or overwrite the failed
journal. Cleanup runs under a fresh bounded context, verifies the Lease UID/holder
and uses conditional
UID/resourceVersion deletion. Cleanup failure is explicit NO-GO and keeps any
existing local owner record/fence for inspection; failed Lease acquisition can
leave only the cluster Lease before local evidence exists. Inspect and reconcile
original ownership/outcomes for a fence left by a controller crash, failed cleanup
or an earlier version, using the original journal when available; do not delete
it merely because time elapsed. Existing journal-based reconciliation
still rejects partial resource writes or uncertain acceptance without separately
qualified recovery.

An existing identical plan SHA-256 may be revalidated only when every action is
`retain` and its original journal is fully accepted with complete resource owner
evidence. Current complete preflight proof/GO, immediate checks and admission, exclusive
ownership, input/baseline/UID fences, resource verification, integrated runtime
health acceptance and confirmed cleanup remain required. The executor performs
no resource writes and does not resume or overwrite the original plan/journal;
each successful revalidation records its current verification separately at the
canonical
`deployment/recovery/revalidated-<unique>/verification.json` with 0700 directories
and 0600 files. That receipt does not prove ownership cleanup; Lease release and
the original plan's separate owner archive still gate invocation completion.
`deployment.execution.recovery` progress identifies this path independently of
Lease acquisition. Unaccepted/partial execution, uncertain acceptance, any
non-`retain` action or current owner drift remains NO-GO. An old accepted journal
alone cannot establish completion of the current invocation.

Read failures retain a safe resource/error code and only
classified transient API GETs retry at most three attempts, as specified below.

### Gate 4: `deployment.pki` — retained identity and trust

| Check | What is checked | Why it is necessary | Consequence if ignored |
| --- | --- | --- | --- |
| Explicit PKI validity margin | Positive valid `DEPLOYMENT_UPGRADE_WINDOW_SECONDS`; preflight evaluates required certificate/MQTT material now and at this minimum margin. Immediate execution checks use the later of the caller's deadline or current time plus that margin. | Valid-now material must remain usable through update/acceptance without treating evidence age as an execution deadline. | Authentication or TLS fails midway through rollout. |
| Original Secret mirrors | Required catalog bindings match the selected canonical runtime Secret material in memory; missing, empty, malformed or mismatched data blocks. The specific handoff-only blank allowance applies when the handoff worker is not enabled. | Keep local deployment inputs and deployed credentials consistent. | Service authentication fails or one credential set silently replaces another. |
| Environment and algorithm scope | Applicable stack metadata, Account Manager environment and supported App/Device CSR algorithms agree with the selected environment. | Prevent interpreting identities under another environment or policy. | Cross-environment authentication/state or unsupported signing behavior. |
| Retained Secret certificate/key pairs | Every retained pair in selected namespaces must parse and match. Credentials referenced by current consumers or the exact planned workload must also remain valid at both checked times. A prior zero-replica ReplicaSet is historical only when its original Deployment owner is current, its zero status is observed and no Pod still belongs to it. | Detect unusable active or planned TLS material without treating an unused historical export as a running credential. | Startup or TLS/mTLS handshakes fail. This generic pair check is not full chain validation of every Secret; an invalid or mismatched historical pair still fails. |
| PKI Controller private identity directories | `deployment.pki.identity-permissions` checks explicit runtime UID, canonical state paths and original writable PVC mapping. Target `fsGroup` requires the exact release-pinned permission-only initializer, confined to the original identity subdirectory and required existing host/Service state files. The initializer runs after mount preparation, restores 0700/0600 and UID/GID 10001, rejects links and verifies unchanged file bytes. Preflight itself only reads: the original controller container supplies directory mode/owner through `stat`, independently of readiness; ownership and inspection Pod UID are checked. Missing/unavailable evidence still blocks. | Admission cannot prove the private filesystem contract; `fsGroup` also supplies non-root access to read-only TLS/token projections. | Startup fails, or removing `fsGroup` breaks credential readability. Arbitrary initialization or a planned recipe without original filesystem evidence cannot qualify GO. |
| Original PKI owners and separate rollout health | Complete existing-update preflight verifies the original nonzero PKI consumers are present in the reviewed plan with matching UIDs. Compilation, transition validation and ownership fences preserve candidate identity settings, Secret/ConfigMap/PVC sources and independent trust. Current Deployment readiness is recorded as `pending_rollout_health`, not used as proof of identity validity. All certificate/CRL/registry checks still run. Health-only and post-deployment checks require current workload readiness. | Candidate prerequisites must be testable while an application is stopped or awaiting repair. | Treating Ready replicas as identity proof hides invalid PKI and creates a deployment deadlock; missing original identity evidence still blocks. |
| Service CRL acknowledgment phase | A fresh, correctly bound, completed native registry snapshot whose only failure is missing CRL acknowledgments qualifies original identity prerequisites when every configured supported consumer belongs to the reviewed upgrade. Its lineage, signed current CRLs, issuance records and published revocations remain verified; acknowledgment acceptance is recorded as pending rollout health. Health-only and final acceptance require genuine current receipts. | An application must execute before it can install/reload a CRL and acknowledge its exact digest. | Requiring these receipts before repairing a stopped reviewed consumer creates a deployment deadlock; incomplete/invalid registry evidence cannot be deferred. |
| Account Manager signing socket | Supported app/management socket settings agree; target preserves reviewed sidecar, socket volume, durable identity and initializer profile. | A Ready Pod must still have a coherent local signing path. | App certificate operations return errors despite a running Pod. |
| CertIssuer bootstrap inputs, when adopted | Caller/subject/session consistency, CA and cert/key settings, supported renewal address and subject policy, and required bootstrap NetworkPolicy selector. | Avoid a circular readiness dependency or ambiguous bootstrap ownership. | Bootstrap stalls or a wrong caller/session is used. Policy inspection does not simulate packet delivery. |
| Static serving chain, when applicable | Mounted serving Secret and issuer certificates build a server-authentication chain valid at the checked cutoff. | A static bootstrap listener still needs a complete valid chain. | Initial TLS fails before managed identity can take over. |
| Device trust profile and consumers | Selected fixed/automatic trust mode is coherent; supported required API/broker consumers have the appropriate Root pins, state, CRL enforcement and bundle acknowledgment configuration. | Required consumers must accept the selected Device trust policy. | Devices are rejected, new Product issuers remain pending or trust enforcement is weakened. Fixed development trust does not qualify production. |
| Root policy and removal history | Applicable Root IDs/state references agree with environment/domain/registry metadata and status; removed/compromised Roots have required distrust publication. | Keep active trust and security removals coherent. | A compromised/removed Root remains trusted or legitimate identities lose trust. |
| Mounted CRL profile and issuer scope | Reviewed Account Manager Service, CertIssuer Service and OpenBao transport CRL consumers retain explicit manifests, pinned CA fingerprints and original writable PVC state paths. Service successor bundle issuers have CRL coverage. | Revocation enforcement needs the correct issuer's durable state for every adopted successor. | A consumer loses revocation data or rejects renewed identities. The legacy Dev guard requires these three consumers; other environments validate their configured adopted consumers. |
| Signed installed CRL and metadata | Exact CRL PEM, positive number, CA signature/issuer/AKI, pinned state fingerprint/ID, digest and signed times; actual publication freshness and issuer lifetime constraints. | Reject forged, corrupt, stale, future-dated or wrong-issuer state. | Revoked identities are accepted or legitimate mTLS is rejected. |
| CRL survives the rollout cutoff | Signed `NextUpdate` is strictly later than the requested validity cutoff. Publication/freshness still uses actual current time. | An unexpired CRL can otherwise expire during update. | Service authentication breaks before rollout finishes. |
| Latest CRL and consumer acknowledgment | Installed number/digest equals the registry's latest publication and the exact required consumer has acknowledged that digest. | A fresh-looking file is insufficient proof of publication/adoption. | Stale revocations or an unaccepted CRL update remain in use. |
| Adopted Service registry integrity | Existing controller's completed read-only inventory matches environment, domain, selected Service intermediate and Root; issuance/revocation records and signed current Root/issuer CRLs are consistent; pending/invalid/unpublished counts are zero. Only missing acknowledgments may be deferred for supported original consumers in the reviewed upgrade, as described above. Health-only and final acceptance require zero missing acknowledgments. | Detect stranded signing and inconsistent lineage/security state while separating deployment prerequisites from consumer execution. | Incomplete/invalid registry evidence cannot qualify an update; actual consumer adoption must still be proven after rollout. |
| Registry evidence and clock | Evidence is no more than five minutes old and no more than five seconds in the future relative to the verifier. | Evidence must describe the current registry with honest clocks. | Stale/future evidence qualifies changed state or healthy PKI is misdiagnosed by a faulty clock. |
| Bootstrap/signing ownership | Applicable active-session schema/uniqueness rules are correct; unresolved active bootstrap sessions and issuing requests block, including expired sessions still marked active. Unadopted profiles must not strand managed Service issuance. | An ambiguous original attempt must be reconciled before another signer proceeds. | Conflicting signing, duplicate credentials or an uncertain issuance outcome. |
| MQTT Foundation registration identity, when enabled | Original `service:mqtt` client identity/key/purpose and Account Manager listener identity/key/DNS; independent mutual chains, valid signed issuer CRL and unrevoked status at both checked times. | Foundation registration must authenticate under the original Service trust. | MQTT registration fails or wrong/revoked identities are accepted. |

Checker-owned PKI validation summaries and classified read failures are retained
in the public gate cause. Unknown provider/tool errors remain masked; private
stderr and credential values are never promoted to a diagnostic.

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
  plan. FAIL, ERROR, BLOCKED, required SKIPPED or missing evidence is NO-GO.
  Pre-deploy rejects `--fast` for every operation. Exit `0` means the requested
  check passed; `1` includes guard, preparation,
  compilation, qualification, private evidence/report publication and execution
  failures; `2` means invalid invocation/configuration before qualification.
- CLI output includes the plan SHA-256. Optional JSON reports belong under
  `deployment/reports/`; generated `deployment/identity-bindings.json` carries
  GO/NO-GO plus the matching plan/bindings hashes. Immutable `proof.json`,
  `plan.json`, `release.json` and `bindings.json` belong under
  `deployment/preflight/<plan-sha256>/qualification-<unique>/`; the atomic
  `deployment/preflight/active.json` authorizes exactly that attempt and its
  proof hash. The proof and pointer schemas are `rtk-cloud.preflight-proof/v1`
  and `rtk-cloud.preflight-active/v1`; both bind the invocation generation.
  Preflight invalidates the pointer to NO-GO before new checks and publishes GO
  only after immutable artifacts and matching bindings succeed. Invalidation,
  NO-GO observations and final bindings/active publication share the short
  nonblocking `deployment/preflight/.publication.lock` and compare the current
  generation; stale publishers and cancellation cannot overwrite a newer attempt.
  These files are beneath the canonical
  environment store with 0700 directories/0600 files. Reports contain sanitized
  plan/gate summaries and, when available, `qualification_sha256`, `qualified_at`,
  `evidence_valid_until` and `reused_preflight`. An admitted deployment records
  `execution_admitted_at` and `execution_deadline` separately from evidence
  validity. Rows identify `evidence_source`
  as `preflight` or `execution`; a failed current check preserves its native
  blocker in `check_id`. The proof binds
  complete gate results, checker/policy version, environment/operation,
  frozen configuration/credential/source and cluster/Kubernetes identity hashes,
  release/CI provenance and native response/artifact hashes. Probe payloads and
  diagnostics never become observation fields; exact formal release manifests
  remain private provenance. Full private plans include
  resource bodies; bindings contain validated original owners, lifecycle
  references/mounts, public trust and retained identity settings. Bindings are
  private evidence, not a universally redacted artifact; recognized inline
  credential forms are rejected, but that rule is not a general secret detector.
- Preflight does not roll out workloads, retire owners, restart OpenBao Pods,
  acquire a persisted execution lease, reinitialize identity or complete storage
  cutover. For `create` and eligible profiles without independent VideoStorage registration,
  the documented missing-media-bucket creation is its bounded provider
  write; access-review/dry-run requests and isolated local startup fixtures are
  also part of qualification.
- Deployment loads the active immutable proof/plan/release/bindings and verifies
  hashes, checker/policy identity, validity and matching `--plan-sha256`. It
  consumes immutable release, rendered candidate and isolated startup proof,
  without re-resolving CI images, rendering or running full qualification again.
  Immediate checks cover current input/source/cluster identity, owners/storage,
  capacity/access/occupancy, native PKI and schema prerequisites. Five groups
  (`deployment.target`, `.execution`, `.pki`, `.compatibility`, `.network`)
  retain original identity-PVC mode/owner inspection, OTA database authorization
  and revocations, storage private read grants/media allocation/privacy, and
  current authenticated public CertIssuer mTLS/anonymous denial. Approved
  preflight and immediate probes use the frozen Kubernetes identity, including
  the public probe helper; these current-state checks can still require provider
  or database reads and native probes. The executor
  performs whole-plan admission once, retains per-write admission and phase
  fences, and verifies the actual target,
  workloads, PKI and required endpoints afterwards. Preflight `completed=false`
  is expected even with GO. Any changed frozen source revision, including a
  workspace documentation commit, changed checker or missing/invalidated proof
  requires new complete preflight. Expired evidence blocks a new attempt.
  Binding-only GO from an older version
  is insufficient; another attempt or older GO is never a fallback.
- Proof validity starts at the end of successful complete qualification and
  lasts the configured `DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS` (shared baseline:
  3600 seconds). Deployment verifies it when loading evidence and again at
  admission/start, before immediate checks begin. After admission, phase/input
  fences verify unchanged proof identity/generation, active pointer, inputs,
  sources and owners; the attempt
  may continue beyond `valid_until`. Replaced or invalidated evidence still
  blocks execution, and every new attempt requires valid evidence of its own.
  Expiry, future-dated qualification and incomplete original PKI evidence have
  distinct causes. Evidence timing diagnostics show UTC `qualified_at`,
  `valid_until` and `checked_at`.
- Earlier PKI check time/cutoff remains provenance; publication does not refresh
  it. Immediate native PKI checks obtain current registry/revocation evidence
  under existing freshness rules and verify certificates/CRLs through the later
  of the execution deadline or current time plus
  `DEPLOYMENT_UPGRADE_WINDOW_SECONDS`, the maintained minimum PKI margin.
  This margin is independent of evidence validity and does not cap execution.
- The caller's `--timeout` starts before evidence loading and is not reset at
  admission. It controls that loading, immediate checks/admission, lease acquisition,
  mutations, phase fences, rollout and final acceptance. For evidence expiring
  at 14:02, an attempt admitted at 14:00 with `--timeout 90m` may continue until
  its CLI deadline; a new attempt at or after 14:02 is blocked. Timeout stops
  incomplete execution and is not a successful rollout. Ownership cleanup
  retains an independent bounded deadline.
- The six-gate ordinary update is not initial Production activation. Fresh
  bootstrap, HA/quorum/custody, backup/restore drills and full business/PKI lifecycle
  qualification remain separate scopes. Missing adapters for their extra required
  gates block those operations rather than borrowing ordinary-update GO.

| Responsibility | Current implementation |
| --- | --- |
| Entry, six callbacks, reports and GO publication | [`deployment_unified_cli.go`](../scripts/go/rtk-cloud/deployment_unified_cli.go), [`qualification.go`](../scripts/go/rtk-cloud/internal/deploymentplan/qualification.go) |
| Immutable proof, generation publication, frozen-target consumer and immediate checks | [`deployment_preflight_proof.go`](../scripts/go/rtk-cloud/deployment_preflight_proof.go), [`deployment_preflight_gate_evidence.go`](../scripts/go/rtk-cloud/deployment_preflight_gate_evidence.go), [`deployment_preflight_release_proof.go`](../scripts/go/rtk-cloud/deployment_preflight_release_proof.go), [`deployment_unified_consumer.go`](../scripts/go/rtk-cloud/deployment_unified_consumer.go), [`deployment_unified_proof_execution.go`](../scripts/go/rtk-cloud/deployment_unified_proof_execution.go) |
| Inputs, compiler, retained dependencies and drift fence | [`deployment_unified_compile.go`](../scripts/go/rtk-cloud/deployment_unified_compile.go), [`plan.go`](../scripts/go/rtk-cloud/internal/deploymentplan/plan.go) |
| Derived main issuer runtime settings and static startup checks | [`deployment_unified_issuer.go`](../scripts/go/rtk-cloud/deployment_unified_issuer.go), [`deployment_unified_issuer_test.go`](../scripts/go/rtk-cloud/deployment_unified_issuer_test.go) |
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

Managed native Controller listeners may select their Service issuer roster from
the original mounted public bundle manifest instead of a deployment-only issuer
environment variable. Qualification reads only those exact issuer IDs and bundle
versions from the registry, verifies their same-environment Service lineage and
independent Root pin, then runs the same native inventory and freshness checks.
The managed Account Manager's independently pinned controller and issuer origins
are supported CRL consumers when their complete transport configuration is
present. CRL manifests may use their declared JSON filename or an exact read-only
ConfigMap file projection; the selected key, signed PVC state and latest native
receipt remain required. Receipt membership follows the maintained enrolled
Service role, including the separate API owners and broker sidecar, rather than
assuming that every Kubernetes Deployment name is its authenticated role.

The Controller's non-production fixed Device Root activation policy does not
disable a managed Device consumer's automatic Root trust or signed CRLs. That
complete profile retains direct mTLS, registry enforcement, the same pinned Root,
private PVC state, and native Device/App CRL acknowledgments. The historical
fixed-Root profile without CRL consumers remains a separate explicit contract.

Mounted CRL expiry is checked against the same requested PKI cutoff as
certificate expiry: preflight's minimum margin, or the later of the remaining
execution deadline and the fresh minimum margin during execution. The signed
`NextUpdate` must be strictly later than that cutoff.
`ThisUpdate`, registry freshness and consumer acknowledgments are
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

Native named resource and storage reads use the same frozen Kubernetes identity
and fresh, uncached GETs, with a default 30-second request timeout, 45-second process
limit per attempt and bounded output. Safe API GETs make at most three attempts,
with a 200-millisecond context-aware delay only after `KUBE_TIMEOUT`,
`KUBE_RATE_LIMITED` or `KUBE_UNAVAILABLE`. Forbidden, authentication, malformed
response, successful missing-resource and UID-drift observations are not retried.
Exhausted transient GET failures are not reused as cached evidence. The concurrency
limit remains twelve and the complete qualification deadline still wins; this
policy never replays mutations, container execution or SQL and adds no operator
setting or identity change.
`deployment.access.resource` reports the exact resource and safe error code;
admission and execution preserve that typed access blocker without raw API
output or wrapper errors. Storage reads retain `deployment.access.storage` for
the exact PVC or PV; a read failure does not establish that data or ownership
was lost. Successfully observed missing, replaced,
terminating, unbound or changed PVC/PV bindings remain
`deployment.execution.storage` and require verified recovery or migration.

Other non-wait native commands retain their 90-second request/2-minute process limits.
Native command failure
classification reads only a private 64 KiB stderr prefix and drains remaining
diagnostics. A tool-reported timeout retains safe `KUBE_TIMEOUT`; an earlier
caller cancellation/deadline retains its original context error. Raw stderr
never crosses into public diagnostics or progress. Per-resource wait budgets
follow the maintained rollout setting described in the execution gate.
Controller metadata read transport failures preserve their safe code and advise
restoring API access/connectivity. They do not justify changing directory ownership
or mode; genuine directory metadata mismatches remain separate permission blockers.

Missing or failed update evidence, changed inputs/owners or cancellation
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

Preflight uses the common compiler, including selected adapter
defaults/overrides and resolved provider settings. Deployment consumes its exact
qualified output and verifies current maintained-input hashes. The computed
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
memory and included in the plan digest. Complete preflight saves that selection
and its qualification provenance in immutable artifacts. Deployment consumes the
active exact selection and requires the approved plan SHA-256, without another
CI selection or image pull/startup qualification. Git status is not a prerequisite; exact HEAD/source
matching and the maintained-input digests remain required. Missing or invalid CI
publication is NO-GO. Operator-authored `release.json` selection and freeze-release
commands are retired; the generated qualification `release.json` is private
evidence consumed only through its active proof. Provider
image versions remain maintained configuration and are registry-digest pinned.
For OTA, the formal image artifact also includes `service_registration_manifests.ota`
from the exact published image's pure SDK descriptor. The selected descriptor is
release/plan evidence held in the existing selection; no standalone registration
manifest file or operator version override is introduced. Compatibility checks
qualify immutable registration inputs; post-deploy checks prove actual leased
instance availability.

### Preflight-produced identity observations

An existing-environment preflight does not require a pre-existing private
`identity-bindings.json`. It obtains actual owners and existing identity references
from the selected API and compiles the maintained target in memory. It records
existing UIDs only; newly planned resources have no UID until creation. Target
mounts/configuration are renderer output, and persistent contents or seal state
cannot be inferred from a Pod volume reference.

Preflight publishes the current result beneath the canonical environment store
as `deployment/identity-bindings.json` and immutable qualified artifacts; the active
proof binds complete GO to the exact plan, release, observed-bindings and
qualification hashes/validity. After local invocation/configuration/guard validation,
failed preflight invalidates previous GO. Deployment
loads only the exact active proof, verifies the matching GO record and evidence
validity before admission, then fences current inputs, proof identity/generation
and ownership during execution. An unsupported existing OpenBao profile remains a
compatibility blocker, not an instruction to manufacture a bindings file.

### Historical one-off execution records

An unplanned standalone Job with an explicit Complete/Failed terminal condition, zero active
workers and only terminal Pods is historical evidence, not an enabled update
component. Preflight and execution inventory checks preserve it without requiring
a new renderer, old image publication or retired temporary credentials. Pod
lineage must match the original Job UID/API version. Running, partial, scheduled,
foreign or ambiguous owners remain NO-GO; no online cleanup is performed by
preflight and no historical commands are imported into the candidate plan.

An unplanned terminal standalone Pod is likewise historical only when it has no owner
references and `restartPolicy=Never`. Running, restartable or ambiguous Pods
remain required inventory blockers; historical inspection Pods are not deleted
or replayed by preflight.

Complete post-deploy target acceptance uses the same strict historical
classification as preflight before resolving application Pod lineage. Historical
ownerless probes do not count as candidate replicas or approved image evidence.
Planned standalone Pods and planned Jobs retain their required runtime proof,
including original owners, successful Job Pods, immutable images and readiness.

### Preflight prerequisites and post-deploy health

An existing environment's unhealthy baseline does not by itself prove that a
reviewed candidate cannot deploy. Complete preflight validates retained owners
and candidate identity transitions without requiring those applications to be
Ready. Original Roots, credentials, state mounts, PVC UIDs/baselines, source
files, certificate validity, CRLs, registry integrity and required public
endpoint checks remain mandatory. There is no operator-authored health bypass
flag, success receipt or requirement to reproduce a particular crash log.
`deployment.pki.consumer-policy` also checks that candidate Controller permissions
include retained Account Manager and LogIngester CRL/bundle integrations.

A completed native registry snapshot may defer missing consumer acknowledgments
only when all other integrity checks pass and every configured supported
consumer has a matching original owner in the reviewed upgrade. Current
acknowledgments remain mandatory in health-only and final rollout acceptance.
No acknowledgment is fabricated or treated as received by preflight.

A passing PKI gate may list unready consumers or pending acknowledgment acceptance in `pending_rollout_health`, printed as
`rollout health pending` with its resource ID. GO qualifies execution of that
plan and is not healthy-runtime or completed-deployment evidence. Native rollout
waits, target Pod/image checks and full post-deploy health must pass before
`completed=true`; successful acceptance clears pending-health report entries.
The ordinary health checker receives no preflight plan and remains strict.
Foreign owners, unreviewed consumers and missing identity evidence still block.

## Staged create qualification and recovery

The [staged create program](deployment-operations.md#create-and-resume-as-one-staged-program)
uses one complete qualification for its frozen remaining scope. Unlike a new
independent deployment, resuming an already admitted program consumes its
original fixed execution clock and accepted child journals. Evidence expiry
controls first admission; it does not erase completed native work. The executable
checker digest belongs to qualification, not the immutable program intent.

Qualification must render the complete catalog before native effects. Every
Secret/PVC reference must resolve to a retained original owner, an ordinary
planned producer or an exact typed native producer. Tests must not automatically
insert missing referenced objects into simulated inventory: that hides absent
create producers. Capacity includes every new direct PVC, StatefulSet template
and product identity/bootstrap claim, while genuine existing allocations are not
counted twice.

Future registration, readiness, ACK and finalization are execution results.
Preflight checks the exact image's capabilities and the producer's prerequisites;
it does not require those future results. After execution, full acceptance still
requires their genuine native evidence. Recovery tests interrupt before and
after admission, native writes, readiness, accepted child receipts, parent
receipts, finalization and steady cleanup, then resume the same saved plan to
original execution closure.
