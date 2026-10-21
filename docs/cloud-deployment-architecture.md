# Multi-Environment Cloud Deployment Architecture

## Contract

### Preflight checks and failure consequences

The [detailed preflight checklist](deployment-readiness-standard.md#detailed-preflight-checklist)
is the maintained per-item design for the default existing-environment update.
It explains each input/compilation prerequisite and the six required gates,
what is verified, why it is required and the failure consequence that NO-GO
prevents. It also defines drift, canonical evidence paths, bounded side effects
and the limits of dry-run, capacity and PKI checks. Keep that checklist aligned
with the common compiler and gate callbacks; a successful preflight qualifies
one exact plan, while deployment completion requires runtime acceptance inside
the executor after rollout. The [current script flow and flow chart](deployment-operations.md#current-script-flow)
show this integrated final check and the independent health recheck entry.
The [execution gate](deployment-readiness-standard.md#gate-3-deploymentexecution--ability-to-execute-the-plan)
uses one maintained limit of twelve for independent native admission, storage,
namespace inventory and RBAC checks. Progress identifies execution/PKI substeps and counts finished
admission checks; these counts are not PASS evidence. Parallel qualification
preserves fresh ownership reads and leaves actual deployment writes in plan order.
Safe Kubernetes API GETs use a default 30-second request budget and 45-second
process budget per attempt, with at most three attempts and a 200-millisecond
context-aware delay between classified timeout, rate-limit or unavailable failures.
Authentication, forbidden, not-found, malformed-response and UID-drift outcomes
are not retried. Exhausted transient failures are not reused as cached evidence.
The limit of twelve and complete qualification deadline are unchanged; mutations,
container execution and SQL are not replayed by this retry policy. These maintained
defaults add no operator setting or identity change.
The separate `deployment.execution.surge-capacity` prerequisite covers changed
singleton Deployment templates using RollingUpdate with zero effective
`maxUnavailable` and positive `maxSurge`. Fresh native Node and all-namespace Pod
GETs account for assigned nonterminal Pods, including non-Ready and terminating
Pods, using conservative CPU/memory requests and status allocations. One complete
overlapping candidate Pod must fit a supported eligible node. Recreate and
zero-surge updates skip this guard; unsupported candidate placement/resource
profiles block. Observed native Pod aggregate requests/allocations are counted
using the maximum of aggregate and computed container totals, without double counting.
This snapshot creates no Pod, changes no infrastructure or requests and depends
on no application readiness, CRL write or ACK. Actual scheduling and startup
remain deployment acceptance responsibilities.
At every execution phase boundary, one fresh native inventory serves both the
input fence and validation of all completed/future/retained resources.
Missing IDs use named GETs; the snapshot is discarded before resource writes and
never cached across phases. Every write still uses a fresh named read with its
original UID/baseline/resourceVersion guards and exact-body server dry-run.
Desired top-level labels and annotations declare only the keys the update owns;
null or empty maps declare none. Updates preserve unrelated existing metadata,
and phase/readback checks use the same rule. Declared keys, configuration, Pod
templates and resource identity remain fenced; pre-write baseline checks still
reject concurrent changes to preserved metadata.
Native PVC/PV qualification uses that current inventory where objects are present
and named-read fallbacks with at most twelve independent storage checks. Namespace
collections use the same limit, preserve original namespace order and finish
before sequential cluster/webhook inventory, without wider query scope. Results
retain deterministic plan order, and cancellation joins active checks. This
reduces duplicate observations without reducing fence frequency, dependency
coverage, storage identity checks or approval/GO requirements. Actual execution
streams English phase-check, apply and rollout-wait stages with periodic
heartbeats; progress remains evidence of current work rather than an overall
percentage or a completed check.
Maintained task descriptions identify the current work. Resource messages show
the action, `read`/`dry-run`/`apply`/`wait`/`complete` step and public `Kind/namespace/name`,
while `resources=x/y` counts finished resource operations. Even `resources=y/y`
is followed by resource verification and required runtime acceptance before
deployment can report `completed=true`. Within each contiguous `workload` block,
Deployment, StatefulSet, DaemonSet and Pod effects are recorded in original plan
order before their shared readiness barrier. This allows a proxy to start before
a later peer while still requiring both to become ready. Each deferred owner is
read and checked against its original UID and desired body before and after its
native readiness wait. Explicit workload dependencies still become ready before
the dependent write. The block cannot pass, cross a bootstrap or other phase
boundary, or enter acceptance until every deferred wait succeeds. Configuration,
bootstrap, migration Jobs and retirement retain their immediate waits.

On resume, a known incomplete workload block retains strict UID/body checks but
defers its readiness until missing peers have been written. An already completed
block is rechecked for readiness before later writes, including a crash between
its final recorded effect and its readiness barrier. Unknown in-flight writes
remain blockers. A saved resource prefix therefore records known effects; it is
not proof of phase readiness or acceptance. Plan digests, journal format and
historical records are unchanged. Active query summaries remove settled
operations and show up to three active targets, the remaining
count and active/finished-query counts. Only fixed actions and safe object
identifiers are shown; references, URLs, SQL, credentials and subprocess output
remain private. Release progress
identifies the maintained image role and source/image/CI/artifact substep.
Readiness observations show actual ready/updated replica counts and the selected
rollout wait limit. Their poller exists only during the original wait and is
cancelled and joined before the outcome is reported. See the [progress examples
and wait budgets](deployment-readiness-standard.md#current-script-delivery-scope).
Interactive terminals default to a continuously updated colored inline bar and
spinner with task and safe resource/readiness details. A percentage
applies only to a known admission/resource batch; unknown totals show a spinner,
and no bar represents overall GO or acceptance. Redirected output or `TERM=dumb`
uses plain 10-second heartbeat lines. Active work and the reporter are joined,
and the inline line is finished/cleared before final verdicts and report output.

### Preflight bucket preparation

For `RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED=false`, preflight verifies the
configured media bucket's existence, region and endpoint. Only a successful
complete inventory proving absence permits creation of that exact name/region
for `create` or profiles without independent VideoStorage registration;
fresh inventory must confirm creation. Failed inventory, permission, creation or
readback is NO-GO. This bounded provisioning step does not migrate objects,
replace credentials or create cutover receipts. Other provider probes remain
read-only. Explicit cutover configurations retain their separate lifecycle gates.
An existing-environment `upgrade` with enabled independent VideoStorage instead
requires its original selected media allocation. Its compiler records a read-only
provider observation; missing or changed allocation is NO-GO and cannot trigger
replacement bucket creation. The original `create` preparation path is retained.

### Local configuration and deployment state ownership

`cloud_env/` contains configuration only. All local deployment-generated data,
whether sensitive or redacted, belongs in `~/.config/rtk_cloud/<environment>/`:
tokens, keys, certificates, kubeconfig, allocated provider state, receipts and
proofs, journals/locks, reports, rollback/recovery and temporary deployment data.
Use private `0700` directories and `0600` files. Every renderer, preflight,
executor and recovery operation uses the same explicit environment resolver;
none may invent a location from CWD, a worktree, external disk or volume format.

See the [canonical path table and migration boundary](deployment-secrets-governance.md#configuration-versus-deployment-generated-data).
The unified preflight/deployment CLI produces one private
`deployment/logs/<action>-<UTC-time>-<unique-id>.log` per attempt. The shared
progress renderer records stage/query elapsed times and acceptance timing there;
the CLI appends its sanitized final report. Operators and deployment diagnosis
consume these logs; timing diagnostics stay out of user progress output. Logs
use 0700 directories and 0600 files and never contain raw command output,
arguments, resource bodies or credentials.
The unified CLI and initialized-create recovery entry also produce diagnostic
JSON Lines in `deployment/logs/metrics-<UTC-time>-<unique-id>.log`. The shared
measurement collector records preparation and gate start/completion, monotonic
elapsed milliseconds, individual instrumented probe durations, query-attempt
counts and Kubernetes read-cache and immutable CI content hits. Operators and performance analysis read
these files. They remain outside plan/qualification artifacts and their digests;
adding measurements cannot rewrite previously admitted evidence.
Reviewed registration-listener CRL synchronization archives the original and
published public CRLs and sanitized reviewed/outcome evidence under
`deployment/repairs/service-registration-crl-<YYYYMMDD>[-<unique-id>]/` in that environment
store. The source is the selected issuer's current published registry CRL;
validate its signature, issuer binding, validity, increasing number and retained
revocations. Update only the original registration Secret's `client.crl` using
UID/resourceVersion guards. Account Manager consumes the projected file and
rereads it for each handshake/request. Preflight checks the live material, not
an archived repair receipt. Registry publication and mounted Secret rotation
are separate steps; the archive is not authority to restore an expired CRL.
If the same scoped repair restores original consumer authorization policy,
the operator also retains its guarded helper, private original Controller
Deployment snapshot, and lease ownership/release records there. Guard the
original UID/resourceVersion and reviewed policy values, acquire the shared
execution lease, and verify retained identity-file hashes before and after the
Controller rollout. These records support diagnosis/recovery; they do not
replace native registry evidence or consumer acknowledgments.
Dev-only recovery of an expired original Service, MQTT or OpenBao TLS Root CRL
uses the existing offline ceremony and native `recovery-import-dev-root-crl`
validator/importer. Retain complete prior revocations, independent original
Root pins, increasing numbers and reviewed request digests. Private
`deployment/repairs/root-crl-<YYYYMMDD>[-<unique-id>]/` contains public
issuer/CRL snapshots, requests, signed artifacts, guarded helper, native
dry-run/import outcomes and execution-lease records. Original encrypted keys
and passphrases remain at their existing custody locations; no Root or identity
is regenerated. Operators consume the archive for diagnosis/recovery; actual
consumers fetch, install and acknowledge the registry publication independently.

Only deterministic secret-free resolved configuration may remain under
`cloud_env/<environment>/runtime/`. Legacy operational files there require an
explicit validated migration. Missing evidence never authorizes identity/data
reinitialization or an automatic fallback. Explicit infrastructure operations use
private LKE cluster/node-pool state and account quota with controlled teardown
archives. Existing Kubernetes workload upgrades select the API through the
canonical kubeconfig; they do not require or hash local Linode state/account files. Remaining
legacy edge/TURN, service and artifact state writers still require migration; their old
locations are implementation gaps, not policy exceptions.
DNS ownership, ACME challenge cleanup, zone evidence and synchronization metadata
now share `deployment/dns/<adapter>/` in the environment store. Their input digest
is fenced by the unified compiler. See [the explicit DNS state migration](dns-adapter-architecture.md#migration).

The unified `deploy-environment.sh remove` entry point uses that same explicit
environment and canonical `kube/kubeconfig.yaml`; it never reads a legacy
`cloud_env/<environment>/runtime/state/kubeconfig.yaml` as a fallback. Teardown
captures storage ownership and completes DNS and Kubernetes cleanup before
provider deletion. A failure in those phases stops provider mutation. Provider
request acceptance and verified resource absence are distinct outcomes.
PV capture produces the private `deployment/remove-volume-ownership.json`;
teardown and retries consume its environment/stack/volume bindings, canonical
kubeconfig SHA-256 and original LKE cluster ID when available. Different cluster
or Kubernetes identity generations cannot reuse or merge its IDs. Unsupported
or missing Linode CSI ownership handles block capture. Kubernetes capture and
purge consume a validated private temporary kubeconfig under `deployment/tmp/`
and remove it on exit. If canonical kubeconfig is absent, the selected cluster
must also be verified
absent before remaining cleanup can continue. Missing original PV evidence
prevents a complete storage-cleanup result and never authorizes orphan-volume
selection by pattern.
Removal requires original DNS ownership or an explicitly reviewed current-state
reconciliation receipt and blocks while outstanding ACME challenges remain.
These checks apply to removal, not initial DNS bootstrap.
Original DNS ownership remains available for partial-removal reconciliation;
an already absent owned value can be accepted, while changed replacement values
block removal. Original volume IDs must come from ownership evidence, not an
account-wide list of unattached `pvc-*` volumes.

`dns-reconcile-state` provides a separate current-state recovery boundary for
missing DNS ownership. Its default read-only review binds managed A records to
the selected Linode environment's exact configured edge/TURN labels, required
stack/role tags, region, numeric IDs and public IPv4 addresses. A configured
disabled factory-enrollment remnant can be included; unknown aliases,
unsupported records and ambiguous or name-only ownership remain excluded.
Outstanding ACME cleanup evidence blocks this path. Confirmed publication
requires the exact stack and displayed review SHA-256, creates only private
`deployment/dns/<adapter>/reconciliation.json` and digest-bound ownership, and
preserves original evidence. It neither invents deployment history nor mutates
DNS or credentials. Normal removal validates receipt/ownership hashes and
current mappings before DNS deletion and again immediately before provider
deletion after Kubernetes cleanup. Retained evidence supports exact-ID absence
retries once reviewed DNS values are absent; a replacement generation blocks
removal. See [DNS reconciliation](dns-adapter-architecture.md#reviewed-current-state-reconciliation).

`reconcile-removal-volumes` provides explicit recovery when live PV capture was
lost with an earlier cluster removal. Its default review reads the original
immutable qualification, original teardown kubeconfig and retained provider
evidence, and native Linode events. It validates exact original PVC UID/PV-name
mappings, numeric volume/node event lineage and current volume identity against
the selected original cluster and Kubernetes generation. Missing archived
provider state uses the declared `explicit-reviewed-node-set` binding and
requires review of the explicit original cluster/node selection; it is never
invented provider state. With the exact stack confirmation and reviewed payload
SHA-256, it publishes a declared schema-2 `linode-event-recovery` record to
`deployment/remove-volume-ownership.json`; it never substitutes a fabricated
schema-1 capture or renews a historical qualification. Normal removal revalidates
that record before exact-ID deletion. Its scope is the original qualified PVC
inventory, with unmatched volumes preserved. See [the operator recovery
procedure](deployment-operations.md#rehearsal-removal-and-evidence).

Preflight owns `deployment/identity-bindings.json` beneath the selected private
environment store. It is generated observation/qualification evidence, not
configuration or a prerequisite for the first check. The shared compiler reads
the selected Kubernetes API first and extracts existing owner UIDs and validated
identity/mount references in memory. Workload images, commands, replicas, policy
and target configuration continue to come from the maintained catalog and CI.
Only resources actually observed in the selected environment have recorded UIDs;
new target resources have no UID until deployment creates them. Volume references
do not establish PVC contents, issuer lineage, storage backend or seal health.
Those facts require their own authoritative source/check and remain explicit
blockers when unavailable; preflight never fabricates them.

For an existing MQTT foundation or shadow worker, the ordinary target observer
can also retain its validated current placement: original worker UID and the
exact same-namespace self/enrollment Pod affinity. The observer checks its own
Service Secret, identity PVC and public MQTT trust roles before recording this
optional binding. The renderer still owns transport, permissions, image and
resource settings. Retired enrollment Pods are not required for a later health
check or upgrade; the existing self-affinity remains usable. Create observations
keep their original binding shape, so this does not rewrite an admitted plan.

The ordinary observer also selects the immutable bootstrap cleanup ConfigMap
actually mounted by the current Controller. It verifies the Controller and
ConfigMap owner UIDs, original namespace UID and Kubernetes API CA, fixed
service account, projected token and read-only paths. This preserves the current
completed session during later upgrades without requiring an active create
execution or replacing it with the earlier unversioned cleanup record.

After invocation, configuration, report-path and recovery-guard validation,
preflight replaces the active qualification pointer and any previous GO bindings
record with NO-GO before preparation
or compilation. It first reads the canonical local `deployment/.execution-active`
fence. The default `upgrade` then reads the selected cluster execution Lease;
missing kubeconfig blocks the existing-target operation without implying provider
absence. Explicit `--operation create` instead reads the selected canonical
Linode credentials/provider state and complete cluster inventory. It requires no
existing kubeconfig or cluster Lease, ignores ambient target selectors, and treats
matching names or stack tags as existing-target conflicts. Inaccessible recorded
IDs, invalid state and incomplete inventory cannot establish absence. Confirmed
absence proceeds to exact-source formal CI resolution and staged create
qualification. The create plan freezes provider requests and native bootstrap
recipes; future Kubernetes UIDs and generated identity are excluded from preflight.
Neither mode restores archived identity or switches operations automatically.

Fresh-create planning also validates the complete maintained consumer catalog
before native allocation. It renders the same workload, support and runtime
dependency producers, and binds only explicit outputs of the qualified bootstrap
recipe and enabled product identity descriptors. These contracts describe the
producer, subject, claim and issuer policy; they are not placeholder Secrets,
certificates, owner UIDs or completed provider observations. Missing references
are reported together. An unsaved graph projection checks ordering and cycles
after those declared earlier-stage outputs; the returned consumer definitions
retain their real producer edges. This projection is never an executable plan.
The ordinary target renderer still requires genuine installed bindings and
allocated endpoint observations, and the executor compiles its final target only
after the corresponding native lifecycle outputs have been verified.
The fresh allocation quota is derived from this same catalog's direct claims
and StatefulSet ordinals plus declared earlier bootstrap claims. New claims and
claim templates explicitly select the maintained durable storage class; existing
UID-bound claims and templates keep their original class. New create plans pin
`durable_foundation: true`: foundation Redis starts on its declared `redis-data`
claim with AOF and a single writer, before any application can write data. Older
plans omit the flag and retain their original recipe and explicit migration path.
For a reviewed new canonical Secret mirror, preflight compares its exact desired
key to the selected canonical credential; existing mirrors still require live
readback. This does not qualify a future generated identity as already installed.
Canonical dedicated media and OTA credentials supply the
`video-storage-object-storage` and `ota-object-storage` runtime mirrors without
issuing keys or changing storage. Only the independent VideoStorage consumer
uses the media mirror's two AWS fields. Existing Core/clip consumers retain the
entire original `video-cloud-runtime` Secret and UID; this producer cannot rotate
its credentials or migrate an already deployed consumer's Secret reference.

The native full catalog schedules the managed `mqtt-pki` owner. It does not also
create the obsolete unmanaged `mqtt` StatefulSet or legacy TLS Secret. An
observed legacy broker requires its explicit migration or retirement. The
`mqtt` Service aliases only its actual native dashboard and enabled Test Lab
listeners; exact consumer policies select the managed Pod. Test Lab uses the
same registered HTTPS mTLS authentication callback as MQTT TLS and its explicit
listener intent, not a separate fabricated credential. Canonical dashboard
mirror checks select `mqtt-pki-runtime/dashboard-password` only from that
observed managed consumer profile; legacy profiles keep their own mirror check.

The identity continuity boundary is the deployment generation. Explicitly
selected brand-new generations may establish new OpenBao and Service PKI
identities; the continued presence of old dev keys does not impose old lineage
on a new generation. Existing-generation upgrades, retries and recovery retain
their original identities. After complete target-absence and idle-execution/
recovery checks, operator preparation preserves prior active material under
`deployment/archives/<generation>/` within the selected canonical environment
store. Its manifest records original paths and content hashes; explicit validated
recovery may consume it, while ordinary preflight and bootstrap do not search it
as a fallback. Directories are 0700 and files 0600. Preservation is local evidence,
not a provider-removal receipt or deployment authorization. Preflight validates
the new active inputs; the admitted native executor creates identities only
during deployment. See [identity lifecycle](deployment-operations.md#new-deployment-identity-lifecycle)
and [secrets governance](deployment-secrets-governance.md#deployment-generation-identity-boundary).

Fresh dev/staging execution uses the same planner at each stage after the native
LKE API exists. An exclusive canonical parent journal records requests before
allocation and captures original provider/PVC IDs before subsequent work.

Provider-only dev recovery is a separate qualification profile for a stopped
original create that completed all provider allocations and canonical Kubernetes
access, then failed before any storage canary or Kubernetes/PKI bootstrap. It
verifies each original native ID, complete inventory/capacity, unchanged kubeconfig,
empty stack namespace inventory, actual S3 privacy/grants, exact formal release,
bootstrap capabilities, DNS and frozen inputs. Its producer writes expiring
`deployment/create/recovery/<review-sha256>/qualifications/<UTC-time>.json` and
the current `qualification.json`; renewal re-runs every live gate and retains
the old immutable proofs. An explicitly confirmed `--establish-storage-keys`
preparation preserves original credential bytes and durable single-bucket key
issuance/validation evidence under
`deployment/create/recovery-storage-keys/<original-plan-sha256>/`, promotes only
two verified bucket-only read/write keys and retains the original cloud keys.
The explicit
recovery executor consumes it. The expired original GO is provenance only.
Byte-preserving owner snapshots use the original execution's
`owner-recovery-handoff/`; the new journal beneath
`deployment/create/executions/<review-sha256>/execution-*/` references the original
journal digest and retains verified provider outcomes. Handoff holds the original
process lock continuously. Original VM host identities remain at
`deployment/create/material/<original-plan-sha256>/host-identity/<role>/`; only
that validated original reference is consumed, without generation or fallback.
The separately selected `--after-storage-canary` profile permits only a stopped
first provider recovery with original completed DELETE/404 canary receipts,
current exact-object absence and no bootstrap state. It preserves both ancestor
journals, requalifies every gate and scopes new canaries to its new plan digest.
All these paths use the selected canonical environment store, 0700 directories
and 0600 files. The profile rejects uncertain requests, existing bootstrap state
and repeated recovery; it never reallocates resources or rewrites the failed
journal. See [recovery operations](deployment-operations.md#recover-a-provider-only-dev-create-failure).

The separate initial-foundation dev profile verifies the original 33 write owners,
32 completed resources, released child Lease, original three retained volumes and
unchanged runtime/transport/JWT material. It requires actual uninitialized/sealed
OpenBao and six fresh gates. Evidence and original owner/parent snapshots live at
`deployment/create/foundation-recovery/<review-sha256>/`, using 0700/0600. Explicit
execution holds the original parent lock and generation, then records a new
all-retain foundation verification and actual recovery admission. The original
failed child journal remains unchanged; genuine verification completes the
pending parent foundation before native initialization continues. It never
recreates namespaces, allocates providers, resets identity or retries an unknown
write. See [foundation recovery](deployment-operations.md#initial-dev-foundation-readiness-recovery).

After initialization, `recover-create-openbao` independently qualifies the exact
original checkpoint, with evidence and byte-preserving owner/journal snapshots at
`deployment/create/openbao-recovery/<review-sha256>/`. Audit and completed migration
profiles preserve original identity and native Jobs. The explicit
`--after-database-roles` profile also binds original password files and failed Job
owners, authenticates the three existing logins and checks exact partial native
memberships. Only a separately named transactional verifier-grant Job changes the
database; original failed journals, logins and passwords remain intact. Actual
native readback completes the pending parent stage before Account Manager starts.
Every profile checks all six prerequisites and uses a 15-minute proof; no partial
checkpoint or successful bootstrap stage substitutes for final environment health.

The formal `--operation create` preflight selects fresh qualification when no
canonical execution fence exists, or original-generation recovery when a fence
exists. Supported unfinished dev Service bootstrap/enrollment checkpoints are
selected from their original owner and journal and delegated to the same existing
six-gate recovery engine. The original email-repair proof is resolved by its
completed Account bootstrap receipt's SHA-256 within the selected store's private
repair producers. No extra recovery operation or manual proof path is needed.
Unknown or uncertain ownership never falls back to fresh allocation. Neither
route deletes the fence or replaces OpenBao/PKI identity.
Recovery qualification produces the full private artifact
at `deployment/reports/create-recovery-preflight.json` by default, consumed by
operator review; the executor continues to consume the version-bound
`deployment/create/openbao-recovery/<review-sha256>/qualification.json`.
Preflight GO has `scope=original-generation-recovery completed=false` and cannot
substitute for actual deployment or full final acceptance.

An expired unused first Service session may renew from its exact accepted
`service-session` child when no completed ordering recovery exists. A completed
ordering recovery remains the selected source; malformed or ambiguous sources
are refused. The reader authenticates the original parent intent and child
archive, the Job's original session and custody paths, and the retained claim
and ServiceAccount's current UID and body. Normal first issuance therefore does
not require an earlier ordering failure, and renewal still uses the original
persistent key/CSR and signed expired credential.

For the bounded application-enrollment trust repair, the authorized CI artifact
selection producer saves `deployment/repairs/service-bootstrap/selection.json`
and `<source-revision>/image-manifest.json` beneath that directory in the canonical
store (0700/0600). Recovery preflight consumes them, rechecks the exact successful
main Release run and attempt, and admits native grants and same-key bootstrap
renewal. Execution consumes the qualified immutable image and records genuine
forward stages in the original journal. Existing custody, keys and failed evidence
are preserved; these publication records do not authorize a new generation.
The identity-lock continuation produces `service-managed-core-plan.json` in
`deployment/create/openbao-recovery/<review-sha256>/` for review and exact admission.
Its producer binds the original partial trust plan, accepted repair stages and
current native session; the executor changes only Account Manager's strategy to
`Recreate` and its exclusive owner-mode template annotation, then records accepted
forward completion in the original journal.
The MQTT ordering qualifier similarly produces
`deployment/create/openbao-recovery/<review-sha256>/mqtt-root-trust-plan.json`.
Its executor consumes only the original five resource bodies and Root ceremony;
four prerequisite/consumer creates follow configuration-first ordering while the
original API Deployment is retained. Original completed enrollments and encrypted
MQTT custody are consumed as evidence rather than regenerated.
The expired-used-seed variant also produces `used-bootstrap-renewal-plan.json`
in that reviewed directory. Execution consumes the original persistent key/CSR
and binds current-installation re-attestation to the new native session. New
intent/observed credential records use the same canonical generation's
`bootstrap/service/session-renewal/<review-sha256>/` producer/consumer lifecycle.
After a child readiness interruption, the original-key renewal executor reuses
`intent.json` only when its private file bytes exactly match the reviewed intent.
It verifies an already recorded OpenBao policy against fresh native readback
before reusing its receipt; neither retry rewrites the policy or replaces the
key/CSR. Changed intent, policy or ownership still blocks recovery.
An exact completed session retry also reobserves the native session, custody,
credential owners and cleanup binding before reusing its private `observed.json`.
The existing observation remains unchanged; a different or malformed observation
cannot become completion evidence.
When the same used-seed renewal has already completed its Core stage, recovery
authenticates the original admission and the accepted renewal, custody,
credential and Core children as one owner-preserving history. It verifies the
latest resource bodies against the current Core renderer, all original key/CSR
bindings, credential copies and the live native session before continuing.
Earlier credential distribution is not replayed over trust ConfigMaps already
advanced by that accepted Core stage; its original child remains unchanged.

Native Account Manager authentication and explicit policies authorize Service, Device,
App, MQTT and OpenBao TLS ceremonies. The original final consumers verify
installed public bundles and submit authenticated native receipts before issuer
activation; initial trust readiness does not prove business acceptance.

Service trust rendering binds the existing verifier database credential in both
the Video Cloud and Account Manager namespaces, using the original parent-owned
password and verified native DSN. A partial trust rollout requires independently
qualified forward recovery of recorded writes and untouched baselines; it never
repeats a completed Root ceremony or recreates its persistent state. After
managed transport and application startup, the native Service bootstrap session
revokes its seed, synchronizes current CRLs and retires scoped credentials. The
compiler then observes real identity bindings for ordinary plan/acceptance.
The fresh issuer explicitly selects native registry-only issuance after the
Service seed phase: modern registered Service/Device/App/MQTT/OpenBao handlers
retain their policy and signer checks, while unused legacy signer routes remain
disabled. Exact formal image capability `registry-only-issuance-v1` is required.
Canonical adoption retains that explicit mode, exact Service/App signing
references, independent current Root pins, bounded native TTLs and the original
UID/GID/fsGroup. It observes the database URL and Product JWT authority/audience
as separate mandatory Secret key references fenced by their original UIDs;
`issuer_runtime_secret` names the referenced database Secret in this mode.
Legacy CA-bearing runtime Secrets are required only by the legacy issuer profile.
The native issuer retains its 18445 listener and matching Service. Account's
registration Service port 8443 targets the maintained main listener 9444, while
the retained managed Account API uses port 18443 in the same Service and its sidecar.
The canonical Factory client preserves that exact managed API endpoint.

Device/App API trust initialization records admission only after the original
installed native owner acknowledges its reviewed initial incoming bundle. Its
final native loader verifies the same Service identity and actual ACTIVE registry
Root, then fetches and persists the authenticated policy before building dynamic
TLS. The completed admission marker lives on the same owner claim at
`<owner-state-root>/private/incoming-root-bootstrap.json`; a later missing policy
blocks startup instead of regenerating original trust. Policy ACK still belongs
to the actual ordinary listener after installation, not initial preparation.

Fresh creation freezes the existing internal App listener intent as
`internal-service`. After allocating its actual Service and Deployment, the
admitted parent executor publishes immutable ConfigMap
`<stack>-video-cloud/pki-create-app-routing`, binding their real UIDs and the
exact create-plan digest. It writes the same record, including the allocated
ConfigMap UID, to private `pki/app/routing.json` in the selected environment store.
Canonical observation consumes both original records and the live owners before
retaining the internal HTTPS Service on port 8443. Missing or replaced evidence
blocks compilation; absence of a public ingress does not select this profile.
Existing public App ingress profiles retain their observed host and TLS route.

Fresh ingress admission freezes profile `native-nginx-admission-p256-v1` and
the exact official controller image in the create plan. After parent admission,
the executor confirms absence of every ingress owner and generates a dedicated
P-256 admission CA (730 days) and server key/leaf (365 days), with only this
environment's admission Service `.svc` and `.svc.cluster.local` DNS names.
It saves the original private material once at
`deployment/create/material/<plan-sha256>/ingress-admission/identity.json`, then
the guarded child planner creates the immutable admission Secret and maintained
controller/RBAC/Service/webhook definitions. Readback compares complete projected
controller/webhook settings and the exact Secret key/CA/certificate against their
accepted parent-stage UIDs before recording identity hashes and completion.
Ordinary canonical observation consumes the actual controller mount and webhook
CA; cluster-scoped ingress objects carry the stack label for native discovery.
Missing recorded material, conflicting owners or changed identity stop the stage.

Producer/consumer paths beneath the canonical selected environment store are:
`deployment/create/preflight/<plan-sha256>/qualification-*/artifact.json` for
preflight's immutable plan, formal publication provenance and expiring gates;
`deployment/create/executions/<plan-sha256>/execution-*/journal.json` for admitted
native allocation and stage receipts; `deployment/resources/active.json` for
original provider/volume owners used by removal; and
`deployment/create/<plan-sha256>/bootstrap/` for native ceremony public evidence
and observed `bindings.json`. Bootstrap transport generation writes only
`deployment/create/material/<plan-sha256>/<role>-transport/`; foundation and
original-native ceremony jobs consume it. The executor publishes actual public
issuer lineage to `pki/<domain>/issuer.json` and keeps encrypted root custody in
the domain's declared private paths. `operator/ssh/id_ed25519` and its `.pub`
companion are explicitly supplied initial deployment identity for edge/TURN
management, not generated by preflight. Every directory is 0700 and file 0600.
Fresh worker naming persists its native instance/class/ordinal bindings at
`deployment/adapters/lke/worker-labels.json` before the fenced executor changes
display labels. Naming recovery consumes those original bindings and genuine
provider readback; replacement workers cannot silently inherit another instance's
ordinal. Original unused email-key format repairs write snapshots, guarded
requests and completion proof at `deployment/repairs/account-email-format-<UTC-time>/`;
initialized-create recovery verifies that proof against original foundation UIDs
and canonical runtime before accepting the one-field change.
Before any native issuer exists, qualified temporary transport renewal writes
`deployment/create/transport-renewal/<review-sha256>/` with new temporary CA
material, immutable original resource snapshots and actual completion evidence.
Its one-time active pointer at
`deployment/create/material/<parent-sha256>/transport-renewal.json` is consumed by
original-foundation recovery only after independent hash, leaf-key/signature and
resource-owner verification. Managed PKI roots and original OpenBao initialization
are separate from this bounded temporary transport.

After all original managed CA ceremonies are ACTIVE, a separately qualified
`recover-create-active-transports` repair may reissue the bounded temporary TLS
certificates using the same original authority and serving keys. Its prepared
candidate lives at `deployment/repairs/active-temporary-tls-same-key-<UTC>/`;
qualification and immutable source journal, guarded original resource snapshots,
renewed public certificates/unchanged serving-key material, and completion proof
live at `deployment/create/transport-renewal/<review-sha256>/`. The original V1
renewal material/pointer remains immutable. The new completed pointer is
`deployment/create/material/<parent-sha256>/active-transport-renewal.json`.
Foundation recovery consumes this pointer only after authenticating its qualified
admission, source journal, hashes, same-key certificate signatures and exact
owner UIDs. The native executor updates the admitted Secret/ConfigMap projections,
waits for OpenBao's exact mounted certificate hashes and reloads the same process;
provider consumers reload the verified public trust on their original owners.
No managed Root, OpenBao initialization, persistent state or private authority
key is generated. This repair's GO qualifies only its bounded TLS repair;
the complete create continuation must still pass all six deployment gates.

An expired offline Service Root CRL at this checkpoint uses the separately
qualified `recover-create-service-root-crl` lifecycle. The qualifier records the
complete native prior CRL and revocation inventory, unchanged encrypted Root
custody, exact original consumer/PVC/Pod bindings and signed monotonic successor
at `deployment/repairs/root-crl-<UTC>-<native-sha12>/`. Immutable qualifier/source
journal evidence uses `deployment/create/root-crl-recovery/<review-sha256>/`.
The locally and Kubernetes-fenced executor imports the reviewed signed CRL via
the native dev recovery importer, verifies native publication and the original
Secret projection, and verifies its installation in the two original native
consumers. If import/publication completed but the executor stopped before
recording installation, `recover-create-service-root-crl-installed` separately
qualifies the exact partial journal and already installed signed state. Its
observation-only execution retains the original review and five-step journal,
performs no import, Secret replacement, disk installation or runtime ACK, and
records immutable qualification/source/completion files at
`deployment/create/root-crl-recovery/<original-review>/installed-resume/<resume-review>/`.
The explicit `installed-resume/completed.json` pointer binds that completion
without searching operational directories. Completion is published at
`deployment/create/material/<parent-sha256>/service-root-crl-renewal.json`.
Recovery and subsequent managed rendering consume that authenticated current
public CRL, preserving the original initial CRL file/receipt and every managed
issuer publication. This repair cannot remove prior revocations, replace a Root,
change private keys, or manufacture a consumer ACK.

The failed final-core restoration saves its exact derived 16-resource candidate
at `deployment/create/openbao-recovery/<review>/managed-complete-issuer-forward-plan.json`.
The common qualification/source-owner/source-journal producers and six-gate
consumer bind the original 344-entry source and the earlier coupled repair review.
The typed `restore-core-only` action consumes original accepted applications,
routing, ACK-host and partial core journals, retaining their UIDs and all CA,
client/server, key/CSR and volume lineage. Execution records the accepted forward
child and closes only the original failed core alias. It leaves the parent fenced
and unfinished; no deployment completion or finalizer receipt is produced.

An interrupted core forward preserves its actual 14-complete/15-owner child
and the successful Controller. The artifact acquisition producer writes the
sole CertIssuer role selection at
`deployment/repairs/certissuer-provider-prepare/selection.json` and the unchanged
formal CI manifest at
`deployment/repairs/certissuer-provider-prepare/<source>/image-manifest.json`.
The source loader rechecks that successful main publication and binds the
qualified image descriptor at
`deployment/create/openbao-recovery/<review>/certissuer-provider-prepare-image.json`.
The new candidate retains 15 resources and updates only the CertIssuer image.
Its executor closes both failed aliases with their separate original plan/journal
pins only after the actual accepted child. The completion producer archives
`deployment/create/certissuer-provider-prepare/<review>/final-journal.json` and
`completed.json`, then publishes
`deployment/create/material/<parent>/certissuer-provider-prepare.json`.
The release and inventory consumers authenticate that mirror, original owner,
actual accepted Core child and later monotonic parent journal membership before
adding only `LKE_CERTISSUER_IMAGE`. The application image remains the original
parent image. These operational paths all resolve in the selected canonical
environment store, with 0700 directories and 0600 files.

The all-CA-ACTIVE continuation authenticates the original accepted Device/App
trust stages and existing issuer publications before resuming managed transport.
An expired incomplete Service bootstrap session requires a separately bound
same-key/CSR short-lived session renewal. Existing workload identities remain
on their owners; only their actual native installation ACKs can complete the
new session. Original CA ceremonies and completed enrollment are never replayed.
A partially written managed-host child can use the separately qualified
`managed-transports-partial-rollout` continuation. Its exact failed plan/journal,
written bodies/owners, completed renewal/enrollment proofs and original source
prefix are immutable inputs. Original host PVC/PV/CSI identities come from
`deployment/resources/<parent-sha256>/resources.json` and its bound
`deployment/resources/active.json` pointer; preflight reads those existing
records and verifies their native bindings without capturing new ownership.
The target gate accepts only the same-UID audited foundation OpenBao ConfigMap
baseline to its exact recorded managed-host write. Newly required MQTT producers and direct original
verifier/worker references precede the corrected broker in the forward plan.
For the authenticated 26-resource partial application child, the completed
`pki-create-mqtt-consumer-trust` ConfigMap may be either an original update or an
unchanged retained owner. The latter requires the same resource ID, baseline UID,
journal owner, and desired configuration under the planner's normal matching
rules; unrelated native metadata does not require a write. The original source
hash, completed prefix, other resource actions, and current native checks remain
required. Recovery retains this completed ConfigMap in both cases.

The managed-host partial profile produces its exact five-resource private candidate at
`deployment/create/openbao-recovery/<review-sha256>/openbao-root-trust-plan.json`;
qualification and exact-checker admission consume that candidate. Its two MQTT
configuration dependencies reuse existing runtime credentials and contain no
server key. Execution retains the original failed child under
`deployment/rollback/<original-child-sha256>/`, and writes its actual forward
plan/journal under `deployment/rollback/<forward-child-sha256>/`. Only acceptance
of that forward can reconcile the original pending stage; subsequent bootstrap
completion reuses the admitted short-lived session.

The interrupted-forward continuation adds only two existing owners to that
same private candidate: the original Controller and secrets-namespace Service
trust ConfigMap. Qualification/admission consumes the prior GO, source/admission
and failed child; execution produces a new private rollback plan/journal and
records accepted aliases without rewriting either old child. Genuine current-seed
ACK uses the native Service CRL consumer with the retained Root successor and
online intermediate CRL. MQTT and OpenBao alias their original identity/state
PVCs at `/var/lib/pki-service`; each native owner prepares only private CRL cache
directories beneath `/var/lib/pki-service/private/crl/` (0700 directories, 0600
state). Original key/CSR/client/server files stay on those same PVCs. The Controller
policy admits the exact registered Service consumers, including `emqx-pki` and
`openbao`, before either host starts; final rendering and CRL waits preserve that
same policy.

The written-OpenBao continuation authenticates the next stopped seven-owner
child and both preceding admissions; the six completed resources are retained.
Its only workload delta references the original namespace's accepted registry
Secret. Qualification pins the replacement Pod's original StatefulSet owner,
revision, immutable images, never-started installer state and original
initialization/token/share hashes. Original claim-template Pod volumes resolve
to the same ordinal PVCs; remapping is rejected. Native provider API observations
are required after the controlled replacement and original-share unseal, before
prior aliases close. The private parent verification entry
`openbao-managed-recovery-verified-<review-sha256>` records actual Pod/revision,
original initialization and native audit readback hashes. No new identity or
credential copy is produced.

The accepted-host audit continuation uses the same private candidate and
rollback hierarchy. Its producer updates the original ConfigMap's public
configuration only; consumers are the checked original provider's projected
`/openbao/config/file.hcl` and native declarative audit reloader. Qualification
records original Pod/process metadata and the exact known malformed historical
`/run/openbao-pki/private/openbao.pid` marker; that marker cannot authorize a
signal. The parent journal entry `openbao-managed-audit-reload-<review-sha256>`
binds ConfigMap/Pod/StatefulSet UIDs, exact projected HCL and process digests,
then records actual redacted audit readback. The original audit PVC retains its
log history; no seal, service identity or seed is regenerated.

The post-managed API export continuation consumes these completed signal and
verification entries plus accepted enrollment/custody child plans and journals
from the same canonical parent and rollback directories. It produces a new
qualified recovery artifact under `deployment/create/openbao-recovery/<review>/`
and records current native audit evidence before continuing export. Already
accepted transport/Job/reader bodies remain observations: no host cutover, HUP,
enrollment or key producer runs again. When the current launcher renderer differs
only in the known historical OpenBao PID marker, this observation retains that
exact accepted command. It first authenticates the seven-owner audit journal and
selected session, then requires equality of all seven desired bodies after that
single compatibility substitution. Unrelated command, image, configuration or
owner drift remains NO-GO; the separately qualified ACK repair still owns the
later PID-producer correction. The in-memory exporter remains bound to
the original PVC and exact current Deployment/ReplicaSet/Pod owner chain.
A pre-child reader-retirement compiler failure records only the parent's original
pending UID intent. Its next qualified artifact authenticates that same completed
export receipt and original source suffix, re-reads the same registered material
in memory and completes guarded retirement; it creates no replacement reader or
identity and never rewrites original native export evidence.

A separately selected formal OpenBao host ACK package records its parent-bound
role/run/attempt/source/manifest digest at
`deployment/repairs/openbao-host-ack/selection.json`, with untouched actual CI
manifest bytes at `deployment/repairs/openbao-host-ack/<source>/image-manifest.json`.
The selected-environment artifact acquisition is the producer; the typed role
loader re-fetches the current successful formal main publication and is the
consumer. These private inputs do not authorize rollout or replace the original
release. A later exact StatefulSet recovery must independently qualify its
candidate, unchanged identities and native acceptance before role adoption.

Admitted bootstrap records
the new generation's native authenticated Platform Admin binding once at
`deployment/create/material/<parent-sha256>/operator-binding.json`; native PKI
listeners and subsequent authentication consume that exact generation binding.

Final API and Account serving keys stay on their original enrolled PVCs. The
native same-owner exporter verifies installed client/server state against the
live registry and exports the registered keypair only in memory to its own TLS
Secret. Export evidence contains public hashes and original PVC/reader UIDs;
raw private exporter responses never enter reports or public ceremony receipts.
The ordinary planner retains exact admitted Secret bodies in its canonical 0600
`deployment/rollback/<child-plan-sha256>/plan.json` child plan for guarded
recovery; these files contain keys and are private
deployment data, never preflight inputs or replacement identity authority.

Occupancy, unreadable state or a create blocker returns early NO-GO before
media-bucket preparation, formal CI image
resolution, target compilation or the six gates, without a qualified plan or
other PASS results. This startup guard neither acquires/deletes a Lease
nor runs Lease create dry-run; the execution gate still repeats fresh checks to
catch races. Successfully compiled observations are saved privately, and only complete
qualification saves immutable proof, plan, release and bindings at
`deployment/preflight/<plan-sha256>/qualification-<unique>/` as `proof.json`,
`plan.json`, `release.json` and `bindings.json`. It publishes matching GO bindings
and then atomically promotes `deployment/preflight/active.json` to GO with the
exact attempt and proof hash. The proof and pointer schemas are
`rtk-cloud.preflight-proof/v1` and `rtk-cloud.preflight-active/v1`. Both bind this
invocation's generation token. Invalidation, compiled NO-GO observations and final
bindings/active GO publication use the short nonblocking OS lock at
`deployment/preflight/.publication.lock` (0600). Final publication compares the
active generation; a stale publisher cannot replace a newer invalidation or GO.
Cancellation/expiry/commit failure revokes only that invocation's generation.
Artifacts and pointer commits are synced before success; inactive historical
attempts never supply a fallback. Preflight produces these files; deployment
consumes only the active attempt, and operators inspect retained attempts for
qualification/recovery. Use 0700 directories and 0600 files; the full plan is
private resource evidence, not a sanitized report.
A failed/fast check cannot leave old evidence eligible for execution. Plan alone
never publishes GO. Deployment loads the frozen artifacts, verifies checker/policy
identity, environment, operation, hashes and expiry, and requires the matching
preflight record and approved plan SHA-256. Older binding-only GO is insufficient;
there is no alternative-attempt or earlier-GO fallback. Immutable CI/image,
renderer and isolated startup evidence is consumed without reselection or full
qualification. Current input/source/cluster identity, ownership/storage,
capacity/access/occupancy, native PKI and schema, original filesystem permissions,
storage privacy/read grants and OTA authorization/revocation checks, plus public
CertIssuer mTLS/anonymous denial, precede the executor's
single whole-plan admission. Execution fences retain current ownership and
proof identity/generation checks; per-write exact-body admission and final health
acceptance remain required. Kubernetes resource
versions remain in the planner baseline for conditional writes, rather than being
invented for future resources. The generated file uses a 0700 parent directory
and 0600 permissions. `adopt-managed-state` is retired; operators must not
hand-produce or promote a candidate JSON to pass preflight.

The proof records the completed qualification time and expires one configured
`DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS` later (shared baseline: 3600 seconds).
Deployment requires unexpired evidence at load and at execution admission/start,
before immediate checks begin.
After admission, evidence age does not cap the attempt; `--timeout` controls
immediate checks through final acceptance. Its CLI deadline starts before
evidence loading and is not reset at admission. Proof replacement/invalidation or
changed inputs, sources or owners still fail execution fences. A new attempt
requires evidence that is valid at its own start. Expiry diagnostics identify the
specific failure and show `qualified_at`, `valid_until` and `checked_at` in UTC.
Earlier PKI check times/cutoffs remain recorded provenance. Deployment refreshes
native registry/revocation evidence and verifies current certificate/CRL validity
through the later of its execution deadline or current time plus the maintained
`DEPLOYMENT_UPGRADE_WINDOW_SECONDS` minimum PKI margin; proof publication does not
refresh PKI evidence or extend authorization for another attempt.
Approved preflight and immediate probes use the frozen Kubernetes API/CA and
credentials, including the public probe's helper process. Ordinary health-only
checks continue to select the canonical environment identity independently.
Gate evidence hashes actual native response bytes without retaining raw response
payloads or diagnostics in observations; exact formal release manifests remain
in private provenance artifacts.

The executor owns `deployment/.execution-active` and the selected cluster's
`Lease/<stack>-video-cloud/rtk-deployment-execution` for the duration of an attempt.
Every normal return, including failure, cancellation or timeout, releases that
attempt's Lease under a fresh bounded cleanup context, then archives its local
execution fence to a unique `deployment/rollback/<plan-sha256>/execution-owner-*/owner/`
directory while the process lock remains held. Release verifies the recorded
UID/holder and deletes with UID/resourceVersion preconditions; a replacement
Lease is never removed.
Private plans and journals in `deployment/rollback/<plan-sha256>/` survive cleanup,
and failed execution remains incomplete. Another attempt freshly compiles and
qualifies actual target state rather than continuing the previous journal.
An identical previously accepted plan whose resources are all `retain` has a
separate read-only revalidation path. The executor consumes the original
`plan.json` and `journal.json`, requires complete accepted owner evidence, and
consumes current qualified proof and repeats immediate checks/admission, input
and resource ownership fences,
exclusive execution ownership and integrated health acceptance. It does not
rewrite those original files or apply resources. Each successful revalidation
produces `deployment/recovery/revalidated-<unique>/verification.json` in the
selected environment store; operators consume its plan and original-journal
digests, exact Lease owner, resource count, UTC verification time and accepted
revalidation outcome for inspection. This receipt records fresh verification;
the normal Lease release and `execution-owner-*/owner/` archive still gate
invocation completion. Use 0700 directories and 0600 files. Partial or
unaccepted executions, changed owners and any non-`retain`
action remain outside this path. `deployment.execution.recovery` progress
distinguishes this verification from acquiring the Lease.
Failed cleanup reports NO-GO and retains any existing local fence/owner record;
an unconfirmed Lease acquisition can leave only cluster ownership before local
evidence exists. Crashes that bypass cleanup and older retained fences require original-owner
reconciliation, whose partial-write and uncertain-acceptance recovery limits
remain in force. See the [operations procedure](deployment-operations.md#select-the-operation-first).

The common compiler includes the selected adapter defaults/overrides
(`AdapterValues`) alongside shared/environment settings, resolved provider
settings and DNS settings. These inputs participate in both rendering and the
configuration SHA-256 used by the deployment plan. Adapter changes invalidate
earlier qualification just like other maintained configuration changes. This
SHA-256 is a computed identifier in the plan/report, not an additional
operator-maintained configuration file. Re-run preflight after changing inputs
or upgrading a compiler that previously omitted adapter settings from its hash.

### Shared Device and App authentication roles

`cloud_deploy/architectures/kubernetes/architecture.env` owns the explicit
boolean role settings for all environments; environment overrides are rejected.
The shared preflight compiler projects them into qualified container settings:

| Shared configuration | Consumer / runtime settings | Default |
| --- | --- | --- |
| `DEVICE_API_AUTH_APP_PKI_ENABLED` | Device API / `VIDEO_CLOUD_AUTH_APP_PKI_ENABLED` | `false` |
| `DEVICE_API_AUTH_PRODUCT_PKI_ENABLED` | Device API / `VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED` | `true` |
| `DEVICE_API_AUTH_MTLS_REQUIRED` | Device API / `VIDEO_CLOUD_AUTH_MTLS_REQUIRED` | `true` |
| `APP_API_AUTH_APP_PKI_ENABLED` | App API / `VIDEO_CLOUD_AUTH_APP_PKI_ENABLED` | `true` |
| `APP_API_AUTH_PRODUCT_PKI_ENABLED` | App API / `VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED` | `false` |
| `APP_API_AUTH_MTLS_REQUIRED` | App API / `VIDEO_CLOUD_AUTH_MTLS_REQUIRED` | `true` |
| `CERT_ISSUER_PRODUCT_PKI_ENABLED` | Main CertIssuer / same key | `true` |
| `CERT_ISSUER_APP_PKI_ENABLED` | Main CertIssuer / same key | `true` |
| `FACTORY_ENROLL_PRODUCT_PKI_ENABLED` | FactoryEnroll / same key | `true` |

The App API's Product PKI flag does not disable Device CSR issuance. Device
admission and issuance use FactoryEnroll and the main CertIssuer; Device
certificate authentication uses `video-cloud-api-pki`. The dedicated Service
issuer does not receive the main issuer role flags. Missing or invalid values
block qualification; existing role differences require a reviewed authentication
migration rather than a silent role change. Role configuration participates in
the plan input SHA-256 and invalidates previous qualification when changed.

App API TURN credentials use the common App renderer's one-minute TTL, matching
the formal API's revocation cutoff. Other API roles retain their existing TTL.
Candidate preflight rejects a nonpositive, missing, indirect or longer App TTL
when App PKI and TURN are enabled, with `deployment.configuration.app-turn-ttl`;
the check does not depend on a running App API.

The Device API's private MQTT authentication callback retains port 18447 and the
independent broker client role `emqx-pki`. Fresh native allocation retains the
registry-enrolled caller's exact CN `service:emqx-pki`, its original same-owner
trust admission marker and native permission-only trust initializer; existing
legacy callbacks retain their independently provisioned `emqx-pki` CN. The fresh
callback CA ConfigMap contains the actual Service ceremony Root and retains its
original owner. Its CA path is derived from the original
read-only `mqtt-callback` ConfigMap mount and `ca.crt` item mapping; it is not an
operator setting or Device/App trust root. Candidate qualification checks the
complete listener profile, original server TLS pair, broker auth Secret reference,
enforced ACLs and disabled trusted certificate headers. It verifies the original
CA ConfigMap UID and public certificate key without requiring a running API or
broker. Missing or inconsistent inputs block with
`deployment.configuration.mqtt-callback` before resource writes.
The common Device API profile also supplies explicit non-root UID/GID 10001
and supplemental group 0 so the process can read the original root:root TLS
Secret projection with mode 0440 through its existing read-only mount.
Candidate qualification checks the original Secret mount, item paths and effective
read modes together with the process UID/groups, including container overrides.
This derived profile adds no operator setting, uses no `fsGroup`, changes no PVC
metadata and copies or regenerates no Secret, identity or path. It needs no running
API or inspection Pod; actual TLS startup and listener operation remain post-deploy
acceptance evidence.

The existing LogIngester likewise retains its own managed MQTT Service identity
state and independent client/transport Root pins. Observation preserves the
`VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_` lifecycle references. The common renderer
derives MQTT TLS, broker/Controller/renewal endpoints and CA/CRL paths from the
maintained Services and original mounts. Candidate qualification verifies the
complete settings, original public CA/CRL source UIDs/keys, independent Roots,
original writable MQTT PVC mapping and original identity/CRL state paths.
The LogIngester runtime `VIDEO_CLOUD_ENV` derives from the selected maintained
environment. Its namespace and original UID-bound manifests must match that
environment; MQTT and Service authorities retain their separate trust domains,
active/retiring lifecycle, pinned signing chains and unique original state paths.
Candidate mismatch blocks with `deployment.configuration.log-mqtt-scope` without
reading runtime cache contents, waiting for readiness or requiring ACKs.
Service CRL snapshots retain `/var/lib/mqtt-pki/identity/service-crls/*.json`;
MQTT CRL snapshots retain `/var/lib/mqtt-pki/crls/*.json`. The common managed
LogIngester profile keeps one replica and uses `Recreate` for its original
`mqtt-pki-state` PVC. The old Pod terminates before its replacement starts,
protecting the singleton identity/cache writer and avoiding an extra surge Pod's
capacity requirement. Candidate preflight verifies the strategy, replica count
and original PVC mapping; it does not require the future replacement Pod to be
ready. Original identity contents and state paths remain unchanged. A missing identity,
TLS input or unsafe mapping blocks with `deployment.configuration.log-mqtt-transport`.
Preflight does not require a running LogIngester, inspect its directories through
a Pod, or prove actual CRL writes or acknowledgments. CRL persistence, runtime
synchronization and acknowledgments remain post-deploy acceptance checks.
Post-deploy acceptance reads both original MQTT and Service CRL cache snapshots
from the updated LogIngester workload. It verifies their pinned issuer, signature,
validity and metadata against the registry's latest number/digest and requires the
exact native consumer's acknowledgment for each manifest. An unchanged current
CRL may be reloaded and verified from its existing durable cache without being
rewritten; acceptance does not create a new CRL, key or write canary.

An existing `video-cloud-api-app-pki` uses the common API renderer with its App
role, formal CI API image for both the main container and
`initialize-app-trust-state`, and retained TLS, App trust, management Secret and
trust-state PVC references. The initializer copies the initial root policy only
when no state file exists, retaining initialized state on retries. Observation
records references and ownership, never Secret payloads or an arbitrary workload
specification. Missing references or an unknown initializer remain NO-GO.

Fresh managed Device/App API owners use the explicit observed
`native_trust_initialization` binding. Their permission-only initializers retain
existing files and leave absent Root policy state for the native consumer to
fetch and verify through its original managed Service client. Before either
normal API starts, a parent-owned Job binds the new trust PVC UID, rejects any
unexpected prior state, and changes only its empty mount-root ownership; it
does not copy policy files, create identities, or recursively change an enrolled
owner directory. The App management reference may be a read-only alias of its
original `video-cloud-api-app-service-state` PVC only when the owned client path
remains `/var/lib/video-cloud-app-service/private/client.json`. Existing static
management Secret and initial-policy-copy profiles retain their original
compatibility contract.

The same plan includes the App Service, ingress access NetworkPolicy, and the
original owned App Ingress name, hostname and TLS references. The maintained
route uses the environment's ingress class, HTTPS backend and TLS passthrough,
so the API receives the client certificate directly. A missing or ambiguous
original route blocks the update; the compiler does not invent a second route
or rename a historically named Ingress. Environments without an existing App
instance do not acquire a new identity through an ordinary upgrade.

These render/compatibility checks do not establish full runtime GO. Operators
must run complete preflight and, after deployment, verify Device CSR admission,
issuance and Device API mTLS, plus App certificate authentication and token
issuance. Production qualification remains an independent required gate.

### Issuer startup configuration

The shared compiler observes the existing referenced issuer runtime Secret by
its role keys and records only its name in `identity-bindings.json`. Its original
UID remains a retained plan dependency. CertIssuer's CA path is derived from that
Secret's `device-ca.crt` item mapping and the primary container's read-only mount;
missing, optional, writable or ambiguous mappings are NO-GO. No certificate or
credential is regenerated. The existing mounted CA is not replaced by another
trust domain or a guessed environment-local file.

The renderer supplies the managed OpenBao signer settings, caller policy and
server TTL, and uses the common CertIssuer TLS resolver for the listener,
container port, readiness probe and Service (9443). The unused
`CERT_ISSUER_CA_PROVIDER` identity alias and ambient `LKE_CERT_ISSUER_PORT`
override are retired. `CERT_ISSUER_CA_CERT_PATH` and signer selection are derived
runtime output, not manual architecture/override inputs.

Optional MQTT-server and OpenBao-host issuance settings are derived only when
the original corresponding PKI Root binding is present. Deriving a hostname
does not enable an unbound issuance domain.

CertIssuer and FactoryEnroll database settings reference the original runtime
Secret's `POSTGRES_PASSWORD`; Kubernetes expands it only after that Secret
reference in the environment list. FactoryEnroll's bind address and admission
endpoint come from the shared topology, while managed identity and trust
references remain observed. Compilation checks required rendered CertIssuer
fields, managed signer/TTL and caller-pattern syntax before qualification. This
static check does not execute the candidate image or prove certificate files,
database access or endpoint health. API admission also cannot establish that an
executable can start. Runtime health remains a separate required acceptance gate.

The derivation and compile-time checks are implemented in
[`deployment_unified_issuer.go`](../scripts/go/rtk-cloud/deployment_unified_issuer.go).
The [issuer regression tests](../scripts/go/rtk-cloud/deployment_unified_issuer_test.go)
cover all three environments, Secret references, directory/item/subPath mappings,
invalid or ambiguous mounts, rejected manual settings and unbound optional domains.

### MQTT Foundation registration

Every environment uses shared `MQTT_FOUNDATION_REGISTRATION_ENABLED=true`,
`ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED=true`, and
`ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT=9444` from `architecture.env`.
Environment overrides are rejected. The resolver projects the two internal
`LKE_*_REGISTRATION_ENABLED` aliases for existing renderers; these aliases are
not maintained adapter configuration. The flags do not enable Product writes,
Shadow, WebRTC, video storage, or OTA services.

The common target includes the formal CI API image running `/app/mqttfoundation`,
its private health Service, and the Account Manager, PostgreSQL and MQTT access
policies. Account Manager's private Service routes `8443` to the named
`service-reg` container port `9444`; NetworkPolicy uses the named port as well.
The existing registrar instance and read-only certificate/trust Secret mount
are observed and retained. One replica with `Recreate` avoids overlapping leases
for the same approved identity. Existing listener TLS and CRL references are
retained rather than regenerated.

Target compilation rejects an absent/non-private registration Service, wrong
listener port or registration URL, missing identity mounts or missing required
network/service dependencies. The PKI qualification gate reads the retained
registrar and listener Secrets in memory and verifies key pairs, exact
`service:mqtt` identity, client/server purposes, mutual trust, server DNS and
current issuer CRL through the rollout window. Errors are reported using stable
`deployment.mqtt-foundation.*` IDs without provider output or Secret payloads.
These checks do not fabricate Platform workload approval or establish broker
publish/subscribe health; candidate startup and post-deploy readiness remain
required gates. Missing existing credentials remain NO-GO.

Bindings, qualification records and reports remain deployment-generated data in
`~/.config/rtk_cloud/<environment>/deployment/` (0700 directories, 0600 files),
using the common environment path helpers and qualified artifact handoff. No new local
credential/configuration file is required for this service.

### TURN CLI credential lifecycle

`PKI_TURN_CLI_PASSWORD_FILE` names a **container-runtime file**, not a password
value or a controller-local prerequisite. Existing-environment observation
validates its absolute, normalized path and read-only Secret/private-copy mount;
the original Kubernetes `pkiturn-cli` Secret remains lifecycle-owned. A private
`emptyDir` copy is distinct from its source Secret: validating the file reference
does not prove initialization, file permissions, CLI connectivity or candidate
startup. Those remain independent admission/runtime qualification gates.
Create preflight must not demand a future generated password or create one.
Missing existing source credentials remain a recovery blocker, never permission
to rotate them. No password-file configuration override is required in
`architecture.env` or environment settings.

The Coturn VM deployment installer produces/consumes
`~/.config/rtk_cloud/<environment>/deployment/turn/<relay-name>/cli-password`
through the common environment SecretStore resolver (0700 directories, 0600
file). After selecting the environment-owned relay, deployment verifies the
original runtime credential and retains it locally if not yet recorded. Only a
newly allocated relay permits initial random generation. Installation retries
reuse the saved value; upgrades reuse the original value. Conflicting saved,
`/etc/turnserver.conf` or `/etc/video-cloud-coturn-cli.env` values block before
package/config updates. Credentials travel over SSH stdin, not arguments,
generated scripts or reports. The VM copies are online runtime files, not local
controller paths. This installer does not replace the consumer's existing
Kubernetes Secret; introducing a new pkiturn consumer must separately bind the
same credential through its managed Secret initialization stage.

Existing Kubernetes `pkiturn` consumers now use the common managed TURN
renderer in all environments. It renders `/app/pkiturn watch`, the restricted
SSH sidecar and private-file initializer; all three use the selected formal
Video Cloud CI image. The former local-dev initializer image is recognized only
as an existing compatible source and is replaced by that formal image. Desired
commands, non-root UID/GID 10001, health probes and resource requests come from
the renderer. Observation retains the original public SSH destination, PKI
settings, verifier Secret reference, private mounts and App trust PVC; it never
imports arbitrary commands or generates replacement credentials. Strict SSH
host checking and the loopback-only forwarding contract remain enforced, with
no Service exposing the CLI. The initializer can retry copying the same Secret
into ephemeral 0400 files without resetting persistent trust/revocation state.
Unsupported shapes report `deployment.compatibility.pkiturn` with the resource
and repair guidance. A missing consumer remains separate managed initialization,
and rendering does not waive service-level production qualification or complete
environment readiness gates.

RTK Cloud uses one reusable architecture description, many environment instances, and replaceable deployment adapters. Environments such as `dev`, `staging`, and `prod` select an architecture and adapter; they do not own copies of the architecture or adapter implementation.

The Kubernetes architecture owns workloads, namespaces, logical node classes, resource intent, capacity rules, placement, edge requirements, and TURN requirements. It uses `rtk.io/node-class` and standard Kubernetes topology labels. It must not contain cloud resource IDs or provider-specific labels.

An environment declares a logical deployment location such as `us-west`. Each logical node class declares minimum vCPU and memory requirements. It never names a provider region or machine Product. Persistent storage remains workload/storage intent and is not inferred from node sizing.

An adapter maps that intent to a provider. The LKE adapter owns Linode regions and instance types, LKE clusters and pools, Block/Object Storage, external HAProxy and coturn VMs, quota, and kubeconfig acquisition. EKS and GKE are reserved contracts and fail before mutation until implemented. DNS is a separate adapter family described in [`dns-adapter-architecture.md`](dns-adapter-architecture.md); it is not owned by LKE, EKS, or GKE.

Adapter resolution is deterministic. LKE maps the logical location to an LKE region, filters its instance catalog by minimum vCPU and memory, then selects the candidate with the least memory surplus, least vCPU surplus, and finally lexicographically smallest type name. The generic plan contains only logical intent; provider region and Product are adapter-private resolved evidence.

## Resolution and lifecycle

The complete deployment qualification contract is
[Unified Deployment Readiness](deployment-readiness-standard.md), accepted in
[ADR 0003](adr/0003-unified-environment-deployment.md). Create and upgrade resolve
one maintained target in preflight and qualify the exact execution plan.
Deployment consumes its active immutable proof and frozen target. Existing resources
are ownership/identity/concurrency evidence, not desired configuration templates.
An environment-local schema-1 snapshot must not select a different renderer.
The shared deployment engine implements this contract for updates and explicit
create/recovery. Legacy prerequisite PASS does not establish complete-environment
GO under the accepted standard.

The directory name under `cloud_env/` is the environment identity. Configuration is resolved in this order: architecture defaults, adapter defaults, environment stack/selection, environment architecture overrides, environment adapter overrides, and allow-listed explicit overrides. Cross-layer duplicate keys, unknown keys, invalid types, and provider keys in architecture config are errors.

The [environment DNS naming source](environment-dns-naming.md) defines how the
environment identity, stack name, DNS root, and endpoint role determine public
names, including registered hostname exceptions and runtime applicability.
Normal managed environments use `video-cloud-<environment>` as their stack.
Shared orchestration resolves that hostname intent before the selected DNS
adapter applies records; changing a deployment or DNS provider does not change
the naming policy.

That same source defines LKE cluster, pool and worker display labels. Fresh LKE
pools use `<stack>-<node-class>` so worker display names retain visible environment
identity. An authorized in-place label repair preserves native allocation and
Kubernetes Node identities, recording original metadata, requests and verified
outcomes at `deployment/repairs/lke-naming-<UTC-time>/` in the canonical selected
store. Display names do not select an environment or replace ID/UID ownership.

```text
resolve -> immutable release -> complete versioned plan -> qualification proof
        -> load active proof -> current execution checks
        -> infrastructure stage -> Kubernetes admission/bootstrap stage
        -> fenced workload/storage/network/route stages
        -> target health and business acceptance -> completion evidence
```

The lifecycle above includes broader infrastructure/bootstrap and business
acceptance stages. The implemented existing-environment update follows the
[current script flow](deployment-operations.md#current-script-flow): its final
check verifies the exact target owners/images and directly reuses the maintained
read-only Go health checker before journal/ownership checks and execution-fence
cleanup. Fresh bootstrap and full business E2E/data acceptance are separate
scopes; the standalone health wrapper does not approve a future plan.

The normalized runtime contract is `cloud_env/<environment>/runtime`. Shared
commands never inspect adapter-private state. Resolved configuration and public
plan summaries are sanitized. Full executable plans and qualified artifacts
are private evidence at the canonical environment paths; never publish their
resource bodies, credentials or formal artifact payloads as report content.

Provider account quota is mutable operator/account state, not architecture, environment, or adapter default. If the provider API cannot return the active-service limit, LKE requires `~/.config/rtk_cloud/<environment>/deployment/adapters/lke/account.env` with `LKE_ACTIVE_SERVICE_LIMIT` before mutation. Missing account state fails before explicit infrastructure changes. It is not an
existing Kubernetes workload upgrade prerequisite. Cluster creation records its
provider ID after allocation; pre-create checks cannot require a future cluster ID.

## Logical topology

The initial node classes are `general`, `broker`, and `database`. MQTT placement defaults to `broker`; each environment selects its maintained `MQTT_NODE_CLASS` override (dev uses `general`). External edge MQTT upstreams must use that same selected class and `NODE_CLASS_LABEL_KEY`, with at least one actual private node address before configuration is installed. PostgreSQL targets `general` unless an environment enables dedicated database capacity, and hard pod anti-affinity uses `kubernetes.io/hostname`.

Edge and TURN are architecture intent (`EDGE_REPLICAS`, `EDGE_MAX_CONNECTIONS`, `TURN_REPLICAS`, and relay port bounds). LKE currently realizes them with Linode VMs; another adapter may use a different implementation without changing the environment or load-test contract.

## Shared capacity formulas

Capacity is resolved before adapter selection. MQTT and Video Cloud API effective replicas are:

```text
MQTT_EFFECTIVE_REPLICAS = max(MQTT_MIN_REPLICAS,
  ceil(CAPACITY_TARGET_CONNECTIONS / CAPACITY_CONNECTIONS_PER_MQTT_POD))

VIDEO_CLOUD_API_EFFECTIVE_REPLICAS = max(VIDEO_CLOUD_API_MIN_REPLICAS,
  ceil(CAPACITY_ACTIVE_DEVICES / CAPACITY_ACTIVE_DEVICES_PER_API_POD))
```

The initial API density is 40,000 active devices per Pod. Other workloads use explicit minimum replicas until a measured unit-capacity contract exists.

Each workload belongs to one logical node class and declares request CPU, request memory, and spread/anti-affinity floor. For each class:

```text
totalCPU = sum(requestCPU * effectiveReplicas)
totalMemory = sum(requestMemory * effectiveReplicas)
usableCPUPerNode = planningVCPU * 1000 - systemReservedCPU
usableMemoryPerNode = planningMemoryGiB * 1024 - systemReservedMemory
effectiveNodeCount = max(minCount,
  ceil(totalCPU / usableCPUPerNode),
  ceil(totalMemory / usableMemoryPerNode),
  spreadFloor)
```

Every Pod must fit one planning node. Persistent volumes are storage intent and DaemonSet/node-agent overhead is part of system reserve. Edge and TURN resources are excluded from Kubernetes node packing. The generic planner owns all replicas and node counts; adapters only map logical location and planning shape to provider resources.

## Legacy mapping

| Legacy input | New owner |
| --- | --- |
| `cloud_env/staging/lke` | Configuration in `cloud_env/staging/runtime`; operational state in the private environment store |
| `LKE_TARGET_CONNECTS` | `CAPACITY_TARGET_CONNECTIONS` |
| `LKE_MQTT_CONNECTIONS_PER_POD` | `CAPACITY_CONNECTIONS_PER_MQTT_POD` |
| `LKE_MQTT_REPLICAS` | generated `MQTT_EFFECTIVE_REPLICAS` from `MQTT_MIN_REPLICAS` |
| `LKE_VIDEO_CLOUD_REPLICAS` | generated `VIDEO_CLOUD_API_EFFECTIVE_REPLICAS` from `VIDEO_CLOUD_API_MIN_REPLICAS` |
| `LKE_*_REQUEST_CPU/MEMORY` | matching provider-neutral workload resource key |
| `LKE_EDGE_HAPROXY_COUNT/MAXCONN` | `EDGE_REPLICAS` / `EDGE_MAX_CONNECTIONS` |
| `LKE_COTURN_VM_COUNT` and relay ports | `TURN_REPLICAS` and `TURN_*_PORT` |
| LKE pool IDs and quota | Private `deployment/adapters/lke/{state,account}.env`; resolved instance types remain configuration |
| `LKE_REGION` | `DEPLOYMENT_LOCATION` plus LKE location mapping |
| `LKE_*_NODE_TYPE` | `NODE_CLASS_*_MIN_VCPU/MIN_MEMORY_GIB` plus LKE instance catalog |
| `LINODE_ACTIVE_SERVICE_LIMIT` | Private `deployment/adapters/lke/account.env:LKE_ACTIVE_SERVICE_LIMIT` |

The LKE adapter may translate resolved settings into compatibility inputs inside
the bounded common catalog compiler while helpers are decomposed. Those keys
are generated inputs, not configuration authority. Compatibility cannot select
a legacy execution path, regenerate managed PKI, copy a live desired spec, or
weaken a required readiness gate. See the readiness standard's replacement
status for capabilities that still require implementation and qualification.

## Shared Zammad target

The fixed official Zammad chart and its bundled dependencies are maintained in
`cloud_deploy/architectures/kubernetes/charts/`, with version and archive SHA-256
in the common `architecture.env`. Seven `ZAMMAD_*_IMAGE` settings pin application,
Memcached, Redis, dedicated PostgreSQL, Elasticsearch and both init image families.
The common `zammad-values.yaml` owns topology, storage and credential references.
All environments use these same inputs when support is enabled; Dev remains enabled.

Elasticsearch declares the chart's full medium resource preset with canonical
`1Gi` requests, preserving the same capacity for its main and plugin-copy init
containers. The renderer represents empty `supplementalGroups` and `sysctls`
lists as explicit nulls: API omission is equivalent, while admission adding
groups or sysctls still fails the unchanged comparison rules.

The compiler hashes chart/values with the selected configuration snapshot, uses
Helm only for local rendering, then adds its resource definitions to the normal
plan, exact-body server admission, execution and post-deploy verification.
Support images are scoped to the support namespace; shared repository names do
not exchange core PostgreSQL/Alpine versions with Zammad's versions. Observation
records support resource owner UIDs without importing live workload specs into
PKI bindings. Existing credential Secrets and durable PVCs remain retained
lifecycle dependencies. Immutable state and PostgreSQL major-version changes
require separate migration qualification.

Support data controllers run in the configuration phase, followed by the
chart/values/spec-bound initialization Job in migration, then application workloads.
The Job has no automatic TTL so its owner/completion remains verifiable through
acceptance. Superseded completed init Jobs may be explicitly retired in the plan;
pending/failed originals block. CronJob target configuration is verified without
pretending a future scheduled execution has completed.

The native renderer produces/consumes only environment-local private copies under
`deployment/tmp/zammad-*` (0700 directory, 0600 chart/values/client files), deleting
scratch on all exits. It does not update Helm release history or run Helm upgrade;
the deployment script is the single write entry. See the
[Zammad runbook](deployment/support-ticket-zammad.md) for operator steps.

## Existing OpenBao configuration baseline

The shared `architecture.env` explicitly records the non-secret common Dev and
Staging baseline observed on 2026-10-07: `OPENBAO_STORAGE_BACKEND=file`,
`OPENBAO_DATA_STORAGE=10Gi`, `OPENBAO_AUDIT_STORAGE=5Gi`,
`OPENBAO_POD_MANAGEMENT_POLICY=OrderedReady` and
`OPENBAO_UPDATE_STRATEGY=OnDelete`. These maintained inputs are resolved and hashed
with the deployment configuration. They are not a completed storage migration.
OpenBao image and scheduling settings also live only in this same file:
`OPENBAO_MIN_REPLICAS=1`, `OPENBAO_NODE_CLASS=general`,
`OPENBAO_REQUEST_CPU=250m` and `OPENBAO_REQUEST_MEMORY=256Mi`.
The resource requests are maintained intent; the observed workloads omit them.
There are no OpenBao copies in `workloads.env` or environment overrides.
This initial single-instance baseline does not qualify production HA; the
production qualification checks remain enforced.
Preflight and deployment consume the same five profile inputs. The shared
renderer supports an existing single-writer `file` installation and the explicit
`raft` profile. Observation recognizes the mounted configuration and registered
transport consumers; it does not require every installation to already be Raft
or to have only one container. A backend, immutable claim-size or pod-management
policy mismatch remains `deployment.compatibility.openbao`; no automatic storage
conversion, seal replacement or identity bootstrap occurs.

For `file`, the original mounted HCL ConfigMap is a UID-bound retained dependency,
including provider and seal configuration. The maintained launch uses its checked
configuration key; legacy chart substitutions and unknown sidecars/init containers
are rejected rather than copied as workload behavior. The renderer preserves
original mount/credential references and permission identities. It pins the main
container to the common official OpenBao image. When managed transport is already
enabled, its `openbao-pki` sidecar and `openbao-pki-install` init container use the
exact-source formal CI `video-cloud-openbao-pki` entry in `image-manifest.json`;
missing publication blocks that enabled component. Installation reuses existing
state, with no bootstrap command. The PID file, shared process namespace and
managed readiness marker remain wired for certificate reload.

The planner preserves the verified immutable StatefulSet selector, headless
service name and original data/audit claim templates after checking maintained
sizes and policy. It does not import the live mutable workload spec. For an
`OnDelete` update, preflight verifies named Pod restart permissions and native
UID/resourceVersion conditional deletion admission with `dryRun=All`; it does not
restart Pods. Deployment replaces only the checked controller's old Pod revision,
one ordinal at a time, and waits for the target containers to be ready. Controller
or Pod owner drift, admission rejection, an unexpected replacement or an unhealthy
or sealed replacement stops execution. No PVC is deleted and no unseal credentials
are generated.

A retained OpenBao `OnDelete` target uses a read-only execution wait rather than
native `kubectl rollout status`, which only supports `RollingUpdate`. The wait
checks the original controller and Pod UIDs, observed generation, Pod label
against `updateRevision`, declared containers/init containers, and Running/Ready.
`currentRevision` may remain historical for `OnDelete`. A stale or changed Pod
blocks the retained plan; this branch never restarts, deletes or unseals it.
Readiness remains post-deploy evidence, not a new preflight prerequisite.

Conditional deletion admission includes `dryRun: ["All"]` in the
`DeleteOptions` request body, in addition to the URL query. Kubernetes decodes
the body instead of query options when a body is supplied; a query-only delete
dry-run can therefore perform a real deletion. UID/resourceVersion preconditions
remain present in both admission and execution requests.

An existing in-cluster Kubernetes auth mount may use OpenBao's local
ServiceAccount JWT for TokenReview. Retain its rotating projected token and
Kubernetes API CA mount on the main container, with the original ServiceAccount
and its existing TokenReview permission. Let Kubernetes select the API's default
token audience; the JWT issuer URL is not necessarily that audience. A transport
sidecar must not acquire this reviewer credential merely by sharing the Pod.
Missing reviewer credentials are authentication failures, not permission to
broaden signer roles or replace them with operator credentials.

Dev retains a managed TLS installer/sidecar, shared private runtime files and a
separate 10Gi PKI identity claim. Staging currently projects TLS from a Secret
and has no PKI sidecar. Do not invent a single current TLS mode, copy Dev identity
references into Staging, or remove Dev's PKI installer to match the existing
Raft renderer. Names/UIDs, keys, certificates and private recovery evidence
remain in the selected environment's lifecycle bindings and runtime stores.

## Evidence validity and execution deadlines

The maintained `DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS` setting controls how long
completed qualification may authorize a new deployment attempt. Its shared
Kubernetes baseline is 3600 seconds. The separate
`DEPLOYMENT_UPGRADE_WINDOW_SECONDS` setting selects the minimum certificate/CRL
validity margin, also 3600 seconds by default; it does not bound execution.
Each environment may explicitly override these settings through
`overrides/architecture.env`. Both participate in configuration/plan hashing;
changing either requires new complete qualification. Missing or invalid required
values remain NO-GO. Deployment uses the caller's `--timeout` for immediate
checks through final acceptance and checks live PKI through the later of that
deadline or current time plus the minimum margin. See the deployment readiness
standard for the remaining required trust-consumer and recovery gates.

`DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS` separately sets the required per-resource
readiness/deletion wait. Its shared Kubernetes baseline is `300` seconds in
`cloud_deploy/architectures/kubernetes/architecture.env`. Native wait commands
use the same selected value for their condition/rollout timeout and Kubernetes
request timeout. The local wait process is bounded by that value plus 30 seconds
(330 seconds with the shared baseline) so it can finish or collect an error;
an earlier parent deadline still terminates it. The displayed `rollout-limit`
uses this checked setting rather than a hard-coded value.

The budgets remain distinct: the evidence's 3600-second admission validity,
the 3600-second minimum PKI margin, an individual resource's 300-second wait,
and the caller's overall CLI deadline such as `--timeout 90m`. A resource wait
cannot extend the CLI deadline; evidence expiry and the minimum PKI margin do
not shorten an admitted attempt. Ownership cleanup has its own bounded deadline.
The rollout value is part of maintained configuration and plan
hashing, so changing it invalidates previous GO bindings and requires complete
preflight and a newly reviewed SHA-256 before execution. Missing or invalid
required settings remain NO-GO; deployment never substitutes an unreviewed
runtime default. The rollout value must be a canonical positive decimal integer,
at most 86400 seconds, and fit within the selected minimum PKI margin.

### Deployment image inputs

Service images come directly from exact-source formal CI `image-manifest.json`
artifacts. The compiler holds their verified immutable selection in memory; a
local release file is not a configuration input or deployment prerequisite.
Workspace/service HEADs and gitlinks must match the frozen source selection;
repository-wide Git cleanliness is not a prerequisite. Ordinary tracked or
untracked changes do not automatically block qualification, while the existing
configuration/chart/credential digests and live ownership fences remain required.
Workload image versions use provider-neutral keys in
`cloud_deploy/architectures/kubernetes/architecture.env`: `POSTGRES_IMAGE`,
`INGRESS_CONTROLLER_IMAGE`, `OPENBAO_IMAGE`, `REDIS_IMAGE`, `FLEET_VALKEY_IMAGE`,
`REDIS_EXPORTER_IMAGE`, `MQTT_IMAGE`, `PROMETHEUS_IMAGE`, `LOKI_IMAGE`,
`GRAFANA_IMAGE`, `LOG_COLLECTOR_IMAGE` and `ALPINE_IMAGE`.
Dev, staging and prod use this common official registry baseline; do not maintain
duplicate image versions in environment overrides or adapter defaults.
Preflight resolves their registry digests and includes the complete selected images
in the approved plan SHA-256. The compiler maps these settings to existing internal
`LKE_*_IMAGE` renderer/binding roles; these legacy role names are not maintained
architecture keys. Provider-prefixed configuration remains forbidden in the
architecture layer and workload image settings cannot be moved into adapter defaults.

The recorded baseline retains PostgreSQL 16.15, NGINX Ingress 1.15.1 and OpenBao
2.5.4 with observed immutable digests. All environments consume that shared
baseline. An image setting does not prove CI provenance, managed identity
adoption, sidecar completeness or storage
compatibility; those gates still apply. Image configuration is tracked intent,
not generated deployment state, and never belongs in a private `release.json`.
Grafana uses the common official `grafana/grafana:13.0.2` digest across all
environments. Its renderer has no built-in version fallback; preflight and
deployment consume the same maintained setting.

### Retired core OTA Registrar

The core-API-plus-`otaregistrar` deployment topology is retired. Its enablement
setting, Deployment/Service templates, rollout branches and network access
entries are removed. `otaservice` is the sole supported OTA process/registration
owner. The shared `ota-service-platform-identity` Secret and `ota-service-0`
instance remain unchanged; ordinary retirement cannot delete their credentials
or OTA persistent data.

Preflight observes the legacy Deployment/Service owner UIDs without selecting
an obsolete image or renderer. Once a stopped registrar (replicas and observed
replicas zero, no non-terminal Pod), an existing independent OTA deployment and
core cutover are verified, the common compiler adds only the old Deployment and
Service as `retirement` actions. Deployment executes their normal UID/version
fenced deletions from the reviewed plan. Active, foreign, unverified or
unmigrated owners block with `deployment.ota.retirement`. Preflight never deletes
them. After deletion, repeated planning has no retirement action.

This cleanup does not enable missing independent OTA configuration or qualify
its runtime. The independent service's renderer, storage, credentials, candidate
startup and readiness must pass the same full update gates; a stopped legacy
Deployment is not evidence that its replacement is healthy.

### Shared independent OTA topology

`architecture.env` owns `OTA_SERVICE_REGISTRATION_ENABLED=true`,
`OTA_CORE_CUTOVER_ENABLED=true`, `OTA_SERVICE_EDGE_ENABLED=true`, and the required
`VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=true` for all environments. Adapter defaults
and Staging-only overrides are removed. The common resolver projects the three
internal `LKE_OTA_*` renderer aliases; these are not operator-maintained settings.
Environment copies/overrides and inconsistent forwarding without an independent
service or strict Product/device OTA grants are rejected.

Preflight and deployment render the independent OTA workload and routes from
the same maintained settings and formal CI image. Existing OTA instance, endpoint,
registration URL and certificate/trust references are observed and retained.
No new identity is generated to enable the renderer. Dedicated storage, runtime
credentials, certificate validity, compatibility and startup remain required
checks; enabling the topology does not itself establish GO.
For `video-cloud-otaservice`, the compiler projects the maintained OTA storage
mode and firmware bucket, region and prefix from the selected storage plan.
The object endpoint and endpoint type derive from genuine current provider
metadata for that exact existing bucket. Historical workload values or a missing
allocation receipt cannot supply a fabricated endpoint or lifecycle receipt.
Formal compilation records only public allocation metadata and its stable SHA-256
in `~/.config/rtk_cloud/<environment>/deployment/storage/ota-render-observation.json`
(0700 directories, 0600 file). The private input snapshot and OTA-only renderer
consume that validated selected-target observation. A fresh provider GET with an
unchanged projection retains the record; its observation timestamp is excluded
from the plan digest. This record supplies rendering input, not storage activation
or canary-validation evidence.
The OTA-specific candidate guard checks the formal startup policy and original
Secret source UID/key bindings before writes. It preserves existing credential
references, adds no manual derived setting and needs no running OTA workload or
inspection Pod. Actual startup and storage access remain post-deploy evidence.
An existing OTA update with no related owner schema migration qualifies the
original PostgreSQL startup tables/columns through a read-only, UID-fenced query;
OTA readiness is not required and no DDL runs. A plan declaring a related owner
migration that could supply these objects needs migration-order/evidence boundary
qualification, rather than requiring future objects to exist before that migration.

OTA registration compatibility follows Account Manager's
[implemented immutable-manifest digest](../repos/rtk_account_manager/internal/store/platform_services.go)
and the separate manifest/leased-instance model in the
[registration contract](../repos/rtk_cloud_contracts_doc/service_registration.md#4-manifest-and-registration-operations).
The selected formal CI `image-manifest.json` carries the public exact-image SDK
descriptor in `service_registration_manifests.ota`: service ID, manifest/protocol
versions, endpoint reference and full options. The OTA SDK declares manifest
version `2` and protocol version `1`; it supplies no manual version setting.
CI obtains the descriptor from the exact pushed image's pure description entry,
without starting the service or accessing identities, databases or the network.
`deployment.compatibility.ota-registration` applies the same option/dependency
ordering as Account Manager's digest and reads the original UID-fenced Account
Manager database with frozen identity. It checks same-version immutable content,
certificate-bound workload/instance approval and existing publication metadata.
Preflight changes no registry record, publication or service status and requires
no OTA readiness. Native registration, lease renewal, heartbeats and Ready evidence
belong to post-deploy acceptance.

The experimental PRECHECK path is removed. Formal CI image resolution again
precedes environment inventory/target compilation. The focused read-only
execution occupancy guard above applies only to preflight before this sequence;
it does not run qualification gates or create Lease admission requests. Standard progress stages and
10-second progress heartbeats remain, with elapsed times in the private deployment
log; only full qualification can report GO.

### Shared Shadow worker topology

`architecture.env` owns `SHADOW_WORKER_REGISTRATION_ENABLED=true` and
`SHADOW_HTTP_CORE_CUTOVER_ENABLED=false` for all environments; adapter and
environment copies are removed/rejected. The common resolver projects internal
`LKE_SHADOW_*` aliases for the existing renderer. The worker uses the formal CI
API image running `/app/shadowworker`, a private health Service and existing
PostgreSQL, Redis, MQTT and Account Manager access policies. The selected
environment controls `VIDEO_CLOUD_ENV`; it cannot default Dev/Prod to Staging.

The core API receives `VIDEO_CLOUD_SHADOW_WORKER_ENABLED=true`, disabling its
duplicate MQTT Shadow subscriber and mutation outbox worker. HTTP Shadow routes
remain on the core API; this change does not enable HTTP forwarding. Existing
worker instance, endpoint, registration URL and certificate/trust Secret
references are observed by preflight and retained in its qualified artifacts;
deployment fences their current owners. They remain generated bindings in the canonical private
environment store; no credentials or identity are recreated.

Missing MQTT Foundation/Account Manager listener enablement and incompatible
HTTP forwarding settings block configuration. Certificate, database, Redis,
broker subscriptions, Platform registration and candidate/runtime readiness
remain independent required checks. Local renderer tests do not establish
environment GO or resolve an existing unready worker.

### Shared VideoStorage deployment settings

`VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED=true` maintains the independent clip
upload/playback service in every environment through the existing renderer.
`VIDEO_STORAGE_CORE_CUTOVER_ENABLED=false` retains current Core media handlers;
registration is distinct from a gateway cutover. These settings are owned only by
shared `architecture.env`, projected into internal adapter aliases and included
in the plan digest. Existing instance IDs, identity Secret and certificate paths
are retained; neither setting creates credentials or proves runtime readiness.
The enabled independent VideoStorage candidate requires its own direct-upload
profile and complete media S3, PostgreSQL and registration inputs. The common
compiler projects that role's direct-upload setting and selected media allocation;
Core retains its separately maintained direct-upload flag. Candidate qualification
checks the formal startup configuration and original Secret/key/file references
while preserving the original bucket/prefix, clip key, identity and durable state.
`deployment.configuration.video-storage-startup` reports missing or inconsistent
inputs. A genuine read-only GET of the existing selected bucket produces public
allocation metadata plus the maintained prefix at
`~/.config/rtk_cloud/<environment>/deployment/storage/media-render-observation.json`
(0700 directories, 0600 file). The compiler's private input snapshot and
VideoStorage renderer consume its stable projection/hash; execution qualification
requires a matching fresh provider GET. An unchanged observation retains its
original timestamp, which is excluded from the plan digest. This record supplies
rendering input and cannot replace a storage validation or lifecycle receipt;
missing/changed allocation in an existing upgrade never creates a bucket or changes
keys/state. Candidate allocation/settings are checked for create as well; original
ownership/credentials remain the existing bootstrap flow's responsibility and
future workload or Secret UIDs are not required by this role guard.
It requires no running VideoStorage Pod, registration lease or Ready evidence.
Actual registration, readiness and clip-upload/business writes remain post-deploy
acceptance responsibilities.

### Shared WebRTC service deployment settings

`WEBRTC_SERVICE_REGISTRATION_ENABLED=true` maintains the existing independent
signaling service through the common renderer in all environments.
`WEBRTC_SERVICE_EDGE_ENABLED=false` and `WEBRTC_CORE_CUTOVER_ENABLED=false`
retain the existing public/Core routing; service registration alone is not an
edge cutover. All three settings belong only to shared `architecture.env`, are
projected to internal aliases and hashed. Original instance and identity mounts
are retained. Public routing changes still require their own qualified target.

### Existing ingress admission ownership

The inventory includes an original Helm admission webhook without a stack-prefixed
name only when every webhook targets the selected environment
`ingress-nginx-controller-admission` Service. Original UID, name and CA remain in
observed bindings and the target; mixed endpoints, foreign owners and duplicate
admission owners block qualification. Discovery is not a template fallback and
does not replace admission credentials or waive server dry-run.

### Upgrade identity overlay

For verified existing MQTT owners, broker bootstrap identity defaults are replaced
by exactly the observed identity references. Absent inherited credential sources
remain absent; existing `envFrom` remains authoritative instead of being masked
by newly injected Secret references. Ordinary workload behavior remains in the
maintained renderer. The PKI transition gate still rejects identity or trust
changes and identifies the container and setting names without their values.

### MQTT permission initializers

Existing `prepare-pki-state` and `prepare-mqtt-host-state` are observed and rendered
from maintained permission-only recipes using the formal API image role. They
prepare private directories and permissions, retaining original state mounts;
they never copy, delete or regenerate identity contents. Unknown initializer
commands or image repositories block qualification. Supported original immutable
recipe fingerprints prove compatibility and never supply desired workload specs.

The ordinary Core API and LogIngester retain their observed `mqtt-pki-state-init` through
a maintained permission-only recipe and the formal API image role. It prepares
`/var/lib/mqtt-pki/crls` and `/var/lib/mqtt-pki/identity` and never rewrites their
contents. This recipe does not prepare every nested CRL parent directory;
LogIngester retains its original Service CRL paths and proves actual persistence
after rollout.
Its lifecycle mounts and trust references are observed separately from ordinary
API configuration; unknown recipes block qualification.

Device API `initialize-trust-state` and `initialize-controller-identity` use
maintained recipes and formal API images. Observations record only the original
Root-specific trust-state filename and mount references, not workload commands.
Trust initialization writes only a missing file via a private temporary file;
existing trust and controller identity contents are never overwritten. Unknown
recipes, image sources or state paths block qualification.

A lifecycle-qualified, fully stopped legacy OTA registrar retirement is recognized
before PKI consumer parsing. This narrow exception never permits active or
unknown consumer retirement; owner, drain, replacement and cutover checks remain
mandatory, and shared OTA identity/data remain retained.

### Account Manager managed issuer socket

An observed App issuer socket is retained with its matching `pkimanagement` owner,
shared socket mount and durable identity. Original `APP_CERT_ISSUER_*` references
are lifecycle inputs. The common target omits the uninstalled static client-leaf
volume only for this verified socket profile; it does not create replacement
credentials. Existing static mounts are retained. The observed
`management-socket-directory` initializer must match a reviewed permission-only
recipe; the target creates `/socket/private` with mode 0700 using the maintained
immutable `ALPINE_IMAGE`. Original socket mounts and process UID/security context
are retained. Unknown initializers block compilation, and a shared `emptyDir`
socket requires this initializer. Existing identity-state contents are untouched.
One managed writer uses Recreate. See Account Manager
[`pki-controller-integration.md`](../repos/rtk_account_manager/docs/pki-controller-integration.md).


### Account Manager public registration CRL refresh

The optional dev profile `managed-public-pem-v1` is selected only by canonical
operator files `operator/env/ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE`
and `operator/env/PKI_MANAGEMENT_IMAGE` in `~/.config/rtk_cloud/dev/`; the image
must be the qualified immutable dev digest. It retains the original Account
Manager Deployment UID, listener Secret, socket, identity PVC and PKI identities.
No new CA or private identity mount is part of this profile.
The separate canonical `operator/env/PKI_CONTROLLER_IMAGE` override retains the
qualified dev Controller image in ordinary renders; it selects only that
Controller role and does not replace the shared application image.

The existing `pkimanagement` Service listener consumes the verified public Root,
retiring and active issuer records. Its initial prepare and periodic ten-second
sync publish the complete current PEM bundle atomically, mode `0600`, as UID/GID
`10001` at `/run/account-pki/private/service-registration.crl.pem` on the existing
`pki-socket` `emptyDir`. The producer setting is
`PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_PUBLIC_CRL_BUNDLE`; the unchanged Account
Manager app reads the same path through
`ACCOUNT_MANAGER_SERVICE_REGISTRATION_CLIENT_CRL` on each handshake/request.
Publication precedes the native sync acknowledgment. A failed refresh retains
the previous file without acknowledging the failed update; expired output fails
closed.

The reviewed socket initializer projects **only** `client.crl` from
`account-manager-service-registration-tls` as a startup compatibility seed. It
exposes no listener private key to that initializer and supplies no freshness
fallback. For the selected profile, unified identity qualification and standard
FULL health read the current owned Pod's fixed public file, validate complete
issuer coverage, original-CA anchoring, signed current CRLs and equality with
verified consumer records. Existing registry/latest-ACK checks still apply.
A short bounded reread handles a valid consumer/export advance between reads;
persistent mismatch fails. The legacy profile keeps its existing static Secret
validation.

The scoped update uses ordinary `deployment/rollback/<planSHA>/plan.json` and
`journal.json`; its follow-up run record links the new FULL-health report under
`deployment/reports/`. These are environment operational data with `0700`
directories and `0600` files. Deployment selection and successful closure are
proved by their existing receipts, not by the presence of these notes.

### PKI Controller private identity permission qualification

This permission check applies to Controllers using a private identity store.
A retained static serving profile is qualified separately by its complete
read-only TLS Secret mapping and serving-chain checks; it has no private identity
PVC to prepare or inspect. The common renderer preserves that profile without
adding an identity initializer. This exception requires the maintained static
certificate/key/CA paths, explicit runtime UID `10001`, and no identity PVC, native
identity settings or initializer. Missing state alone is not a static profile:
partial native declarations, mixed static/native references and ambiguous mounts
still fail qualification. Original native identity references and their strict
filesystem checks remain unchanged.

The shared `deployment.pki.identity-permissions` gate runs in preflight and
deployment's current PKI checks. The original identity PVC and explicit non-root
UID/GID 10001 remain unchanged. The Controller needs `fsGroup` to read its
read-only TLS and OpenBao token volumes. Removing that setting alone would
break startup; `OnRootMismatch` alone does not guarantee private identity
permissions. See [Kubernetes volume ownership policy](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/#configure-volume-permission-and-ownership-change-policy-for-pods).

The common renderer adds the immutable `LKE_ALPINE_IMAGE` initializer
`controller-identity-permissions`, before the application starts and after
volume preparation. It mounts only the original identity PVC at
`/identity-volume`, requires the two existing original host/Service state
files under its `identity/` subdirectory, rejects symlinks and multiply linked
files, and restores directory mode 0700, file mode 0600 and owner 10001:10001.
The PVC root and other state/CRL subdirectories are excluded. Before/after file
hashes must match; no identity file is generated, truncated, replaced or renewed.
The initializer uses a read-only root filesystem, no privilege escalation, and
only CHOWN/DAC_OVERRIDE/FOWNER capabilities. Controller updates use Recreate.

Preflight accepts `fsGroup` only with this exact permission recipe, immutable
selected Alpine image, mount, arguments and security profile. Observed
initializers import only volume references and the image role; live commands
never become desired intent. An unknown initializer or modified recipe blocks.
The checker also follows original Deployment/ReplicaSet ownership to the
existing Running Controller container, independently of Ready status,
confirms the original PVC/path mapping, reads only mode/UID using
`stat`, and rechecks the Pod UID. Missing/unavailable inspection evidence still
produces NO-GO: the initializer is a deployment action, not permission for
preflight to change volume data or fabricate healthy runtime evidence.
Metadata read transport failures retain their safe Kubernetes error code and
direct the operator to API access/connectivity checks. A genuine directory
mode/owner mismatch remains a separate permission blocker; a failed read does not
establish that filesystem ownership needs repair. Container `stat` is not replayed
by the safe API GET retry policy.

A scoped repair of an already stopped original Controller retains its image,
Secret/CA bindings and PVC UID, records the reviewed metadata-only update in
canonical environment-local `deployment/recovery/`, and uses the same execution
Lease and local fence. Full preflight and health must be repeated afterward.
Its recovery result is separate from complete-environment deployment completion.
Private logs continue under `deployment/logs/`; no new tracked runtime store is
introduced.

The original `RTK_DEPLOYMENT_SERVICE_ISSUER_ID` is lifecycle-owned registry
metadata. Observation and rendering preserve it alongside the independent
Service Root pin; it is not a process-policy knob or a newly selected issuer.
If an interrupted update dropped it, only the exact original value from a
validated UID-bound journal may be restored. Registry inventory must still
verify the selected issuer and Root against the original database.

### Core API MQTT transport derivation

The existing `video-cloud-api` / `app` consumer with an original fixed MQTT
Root binding uses the common renderer to derive its MQTT TLS settings. Other
working consumers are outside this repair. No manual architecture/override
settings are required for `VIDEO_CLOUD_MQTT_TLS`, `VIDEO_CLOUD_MQTT_ADDR`,
`VIDEO_CLOUD_MQTT_SERVER_PKI_NAME`, `VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH`,
`VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST`, `VIDEO_CLOUD_MQTT_PKI_CONTROLLER_URL`
or `VIDEO_CLOUD_MQTT_MANAGEMENT_CA`. Observation omits these generated values
from identity bindings; runtime environment variables are still rendered for
the application. The independent original Root fingerprint remains a
lifecycle-owned binding and is never inferred from a presented certificate.

The broker hostname and TLS port come from the selected namespace's maintained
`mqtt-pki` Service and `mqtts` port; the Controller URL comes from its
`pki-controller` Service and `https` port. Both Services must be present with
unambiguous matching target ports. Relative filenames `roots.pem`, `crls.json`
and `root.pem` use the original `mqtt-pki-bundles`, `mqtt-pki-crls` and
`service-root` volume roles, including their `items.path` mappings. Combining
these with the original absolute container `mountPath` produces application
paths. Kubernetes mount paths remain absolute. Writable, optional, duplicate,
shadowed or escaping mappings and unsupported `subPathExpr` block.

`deployment.execution.mqtt-transport` checks the candidate before admission.
It reads the original retained ConfigMap/Secret owners and role keys, verifies
the trust bundle contains the independent original self-signed Root, and rejects
an empty/malformed CRL manifest. Existing runtime PKI gates remain required.
`deployment.configuration.mqtt-transport` identifies incomplete or inconsistent
candidates without printing payloads or private tool errors. The PKI transition
comparison normalizes only these generated transport values after validation;
original Root and volume/mount changes still block. No certificates, state files
or receipts are generated by this qualification.

MQTT management also requires the original managed Service client identity.
Its state path and independent client Root remain lifecycle-owned bindings on
the original writable `service-identity` PVC. The renderer preserves Controller
identity settings and derives Controller/CertIssuer HTTPS endpoints, their
independent host Root bindings, DNS names, CA paths and the Controller Service
CRL manifest from maintained Services and original mounts. CertIssuer uses its
`https` port, including a matching numeric target port. These generated settings
do not belong in `architecture.env` or environment overrides.

Candidate qualification rejects missing state/Root bindings, static or bootstrap
management credentials, incomplete renewal/trust configuration, missing original
public file owners and CRL state paths outside the writable identity PVC. It does
not require the application to be running. The native client still verifies the
original identity and performs authenticated CRL adoption after deployment.
The Core API also receives `core-identity-permissions`, using the selected pinned
Alpine image and the original Service identity mount/state. The formal API UID
10001 is explicit in the candidate. After volume preparation, the initializer
requires the original state file, rejects symlink paths and hard-linked state,
and changes only the PVC root directory to owner 10001:10001 and mode 0700.
It verifies unchanged identity bytes; it neither creates identity nor recursively
changes files. Recreate prevents concurrent writers. Preflight validates the exact
recipe, image, UID, mount and strategy, and requires every Service CRL persistence
parent to be that prepared root. A nested unprepared parent blocks with
`deployment.configuration.core-crl-permissions`. This check qualifies the reviewed
candidate without depending on a running application or mutating PVC permissions.
Scoped restoration evidence is produced and consumed by the reviewed operator
repair under `deployment/repairs/core-mqtt-management-<unique-id>/` in the selected
canonical environment store: original Deployment/PVC snapshots, a read-only
native inspection manifest and public result, the reviewed environment-field
patch, guarded helper, execution ownership and cleanup/outcome records. Use 0700
directories and 0600 files. The inspection does not export keys or initialize
identity; restoration preserves the original image, PVC and identity bytes.

### PKI prerequisites versus workload health

For an existing update, preflight verifies original PKI consumers against the
reviewed plan and original controller UIDs. Compilation and ownership fences
validate candidate configuration and identity transitions; original credentials,
Root pins, Secret/ConfigMap projections and durable PVC references remain
required. Deployment availability does not prove identity validity and is not
a preflight requirement for these consumers. A stopped API or an unready
LogIngester can therefore have valid deployment prerequisites.

Every independent certificate, Root, trust, CRL and registry integrity
check still executes through the rollout validity window. Missing or invalid
original evidence remains NO-GO; a dependency must be accessible when its
actual identity/state inspection requires a read. No readiness flag substitutes
for that evidence.

A completed native Service registry inventory may report only missing CRL
consumer acknowledgments while proving current signed CRLs, original lineage,
issuance integrity and published revocations. Complete upgrade preflight binds
every configured supported consumer to its reviewed original Deployment and
records acknowledgment acceptance as pending rollout health. Incomplete native
reports, invalid/pending issuance, unpublished revocations, stale/foreign
evidence and unplanned consumers still block. The checker creates no receipts;
health-only and final acceptance require genuine current acknowledgments.

`pending_rollout_health` records unready reviewed consumers and deferred
acknowledgment acceptance without claiming they are healthy or have acknowledged
the current CRL. Only complete preflight supplies the reviewed plan internally;
there is no operator bypass setting or stored health exception. Health-only and
post-deploy checker invocations require readiness. Native rollout/acceptance
must observe full readiness before completion; final success removes
pending-health entries. Sanitized outcome
reports use the existing environment-local `deployment/logs/` and optional
`deployment/reports/` paths; private Pod log contents are not published.

The original-owner installation-lock continuation produces
`deployment/create/openbao-recovery/<review-sha256>/current-owner-quiesce-plan.json`
and `current-installation-plan.json` in the same private directory. Recovery
qualification writes these exact resource plans; the qualified executor verifies
and consumes them to pause the two recorded consumers and run only unfinished
native attestations. The plans, original source journal, admission and resulting
rollback journals are canonical selected-generation operational evidence, with
0700 directories and 0600 files. They do not belong in `cloud_env/` or a worktree.

### Original-generation OpenBao installed-ACK role proof

The qualified create-recovery producer stores its exact selected role descriptor
and candidate at `deployment/create/openbao-recovery/<review>/openbao-host-ack-source.json`
and `openbao-host-ack-plan.json`, alongside its immutable qualification and captured
source owner/journal. After accepted original-owner execution and nine native ACKs,
the completion producer writes immutable
`deployment/create/openbao-host-ack/<review>/completed.json` first, then
`deployment/create/material/<parent>/openbao-host-ack.json`. All paths resolve under
`~/.config/rtk_cloud/<environment>/`, with0700 directories/0600 files.
The bootstrap identity observer, final create target compiler and independent health
inventory consumer authenticate these proofs, original journal membership and formal
publication before overlaying only `LKE_OPENBAO_PKI_IMAGE`. The original parent
release stays immutable. After execution-fence retirement, consumers authenticate
the exact archived owner at its original execution directory. Missing/malformed
completion material never establishes a replacement identity or an image fallback.

### Original dev post-Core trust recovery state

The MQTT and OpenBao TLS Root CRL adapters produce signed candidates, native
dry-run evidence and stable requests under the selected environment store's
`deployment/repairs/mqtt-root-crl/<source-selector>/` and
`deployment/repairs/openbao-tls-root-crl/<source-selector>/`. Their immutable
source, owner, qualification, final-journal and completion mirrors use
`deployment/create/mqtt-root-crl-recovery/<review>/` and
`deployment/create/openbao-tls-root-crl-recovery/<review>/`. Active completion
pointers are `deployment/create/material/<parent>/mqtt-root-crl-renewal.json`
and `deployment/create/material/<parent>/openbao-tls-root-crl-renewal.json`.
The late-session source reader authenticates TLS completion, MQTT completion,
and the genuinely accepted original Core child in that order.
When both original Root #1 CRLs are expired, the MQTT qualifier's immutable plan
also contains the paired-prior source evidence. The native observer authenticates
the exact initial signed Root CRLs, current original CA/custody/inventory,
retained TLS ICA #3, original cache/Pod/PVC owners and genuine prior ACK roster.
The executor consumes the same pair before import; other issuer freshness and
normal ACK readiness are unchanged. This evidence belongs to the existing MQTT
qualification path and is independent of immutable signing-custody material and
candidate selection. No second CRL candidate or replacement identity is created.
MQTT plan volume evidence comes from the original 18-volume ledger authenticated
by the accepted restored-Core admission. It records the two original consumer
PVC owners and all original PV/CSI bindings inside the existing qualification
artifact. Later Apps/Core resource membership cannot stand in for those earlier
volume owners. The pure artifact reader and qualification use the same guard.

If native MQTT import and both genuine consumer stages completed but the whole
publication step is still pending, a separate completion-only qualifier stores
its fresh checker-bound qualification, captured current journal, scoped lease
and completion proof under
`deployment/create/mqtt-root-crl-recovery/<original-review>/completion-only/<fresh-review>/`.
Its reader authenticates the original qualification/source and exact four-entry
MQTT tail, then independently reobserves native #2, both caches/ACKs and original
identity/custody/volume state. The executor may only finish the original top step
and publish its existing final-journal/completion mirror and active pointer.
It adds no parent admission, native step or lease-history record; the fresh
scoped lease belongs to the sidecar at
`execution-leases/<lease-digest>/lease.json`. Its `completed.json` proof is
referenced by the original completion mirror/pointer and authenticated by
downstream readers. It cannot sign, import, install cache data
or start a session. The downstream TLS/late readers retain the exact original
four-entry ancestry.

Late qualification binds the preparation, native, expiry-reserve, finalizer
and overall budget constants into its immutable material map. Its runtime
clock is a context-local attempt value rather than part of the immutable
source descriptor. The fixed native deadline starts before seed authorization
and covers owner updates, the exclusive Log lifecycle, real nine-ACK completion
and finalization. Rechecks reuse that deadline; actual certificate/session
readback must cover its expiry reserve before credential publication. After
the genuine finalized/tombstoned/copies-absent journal receipt, the remaining
workload and acceptance tail uses the original overall command context.

The late-session producer stores `managed-late-session-source.json`, seed,
owner, custody and four Log preview plans under
`deployment/create/openbao-recovery/<review>/`. Actual certificate observation
and expired-copy archives use
`deployment/create/<parent>/bootstrap/service/session-renewal/<review>/`.
The pre-admission execution reader consumes the actual fresh qualification and
its archived descriptor, seed/owner/custody and four Log previews from this
same review. It authenticates their material hashes, original owner/journal,
checker, configuration, kubeconfig and immutable source ancestry before
hydration. This avoids deriving already qualified plans again; current native
rechecks and a final unchanged GO-expiry check precede the parent admission.
The guarded executor and durable completion readers consume these exact
artifacts, actual child journals and native ACK/process/UID receipts. Final
bindings select the actual accepted cleanup ConfigMap referenced by the original
Controller, preserving prior verifier records. All paths use common selected
environment helpers with 0700 directories and 0600 files.

The terminal-seed trust-forward is a separate qualified producer under
`deployment/create/openbao-recovery/<fresh-review>/`, using schema
`rtk-cloud.create-managed-late-seed-trust-forward/v1` and
`managed-late-seed-trust-forward-source.json` /
`managed-late-seed-trust-forward-plan.json` alongside the common qualification,
source-owner and source-journal files. The public
`--after-application-enrollment-trust` selector uses this stopped-source profile.
Its source captures the
stopped current parent journal, immutable original Late admission/descriptor,
completed native authorization, original failed child and Job/Pod identities,
released scoped leases, and the exact existing managed OpenBao TLS trust owner.
The runtime reader independently observes zero selected session/deployment/
issuance outcomes, prospective-certificate absence and retained native custody
before admitting one distinct same-ID corrected Job. An effective-seed resolver
consumes its genuine accepted child and explicit forward receipt for native
observation and durable completion while retaining the failed original child.
Only these exact added parent entries and lease are interpreted; arbitrary
suffixes or fresh baselines are refused.

Its fresh qualification binds a distinct fixed native repair budget. This is
available only to a terminal producer with no minted credential or session,
and starts before corrected Job creation. Credential publication and the
remaining original Late owner/Log/finalizer pipeline share this same deadline
and actual expiry reserve. It cannot restart a clock after credential creation.

Operator failure diagnosis producers may preserve an original private Pod log
and exact original Job/Pod JSON snapshots plus read-only no-mint observations under
`deployment/repairs/managed-late-seed-failure-<original-review>/` and a sanitized
summary under `deployment/reports/dev-late-seed-failure-<original-review>.json`.
These files are consumed by operator diagnosis and handover only; fresh
qualification must independently collect its own evidence. They do not
publish ownership or authorize signing. Selected-environment helpers and
0700 directories/0600 files govern both paths.

The native bootstrap producer writes the signed leaf, issuing CA and returned
CA chain without deduplication. Same-lifecycle source/current/credential and
durable readers therefore share one strict three-or-four-certificate guard:
leaf, the original ICA, optionally that identical ICA, then the original Root.
Original raw certificate bytes bind to the immutable renewal source pin;
new raw bytes bind to the accepted native seed observation. Both paths retain
key/CSR, issuer/root signatures, native fingerprint and expiry checks. This
serialization compatibility changes no native material or identity.

Original bootstrap public-certificate permission provenance comes from the
authenticated signer Job/Pod's fsGroup 10001 and the original shared PVC,
not an invented fsGroup on its custody reader. The read-only stopped-seed
guard accepts only public certificate mode 0600/0660 with UID/GID 10001 behind
a private 0700/2700 state root, while retaining exact original byte hashes
and regular 0600 key/CSR files. The new certificate must still be absent,
and session/deployment/issuance counts must all be zero. Native material
is observed in place; local operational artifacts remain canonical 0600.

The App-specific Root CRL preparer exclusively writes `candidate.json`,
`request.json`, `issuer.json` and `signed/revocations.pem` under
`deployment/repairs/app-root-crl/<source-custody-selector>/`. The selector binds
original source/owner, App Root, live prior/revocations and custody hashes. A retry
reads the same signed candidate; missing output in an existing signing directory
blocks instead of signing again. The qualifier and exact API importer consume it.
The qualifier writes source/owner snapshots, plan and six-gate qualification under
`deployment/create/app-root-crl-recovery/<review>/`. Its normal human management
API importer and original App-PKI consumer observers are the executors; original
Account/Controller ownership and operator identity remain authenticated.
Completion creates `deployment/create/material/<parent>/app-root-crl-renewal.json`.
The typed post-mint source/frame and current/durable readers consume this exact
completion, four-stage journal tail and scoped lease. Historical parsing may
peel only that proven tail while fresh authority binds the actual parent journal.
Pending or unknown outcomes block installation and cannot reset its clock. No
native image or generic dev-Root-import domain allowance is granted by this path.

The App completion-only qualifier uses
`rtk-cloud.create-app-root-crl-completion-recovery/v1` and exclusively writes
qualification/source-owner/source-journal/completed sidecars below
`deployment/create/app-root-crl-recovery/<original-review>/completion-only/<fresh-review>/`.
Its consumers are the completion executor and the typed completion reader. Fresh
authority binds the stopped actual373/374 prefix, original signed/import receipt,
current two-consumer cache/ACK and original identities/18 volumes, and the same
recorded live cluster lock. It checks that lock instead of acquiring another;
only original pending consumer/top stages and completion mirrors may advance.
No native signing/import/cache mutation or new installation clock is permitted.
After genuine completion the operator releases that exact owned lock with UID,
holder and resource-version preconditions before the full deployment qualifier.

The separately qualified post-mint continuation uses schema
`rtk-cloud.create-managed-late-post-mint-recovery/v2` under
`deployment/create/openbao-recovery/<fresh-review>/`. Its qualifier produces
`managed-late-post-mint-source.json` and
`managed-late-post-mint-owners-plan.json` alongside qualification, source-owner
and source-journal files. It binds the exact completed post-cleanup prefix,
original Late authority, accepted seed/credential/cleanup children, actual
selected-session ceremony and remaining owner preview. The immutable descriptor
binds first-admission installation policy, genuine expiry and fingerprint, with
no preparation-derived epoch. The public `--after-application-enrollment-trust`
selector requires this checkpoint, zero installed subjects and no installation
outcome.

At first actual admission the execution writer exclusively produces
`deployment/create/<parent>/bootstrap/service/managed-late-installation-clock.json`.
Epoch is that admission's actual `StartedAt`; deadline is the minimum of epoch
plus 90 minutes, original caller deadline and actual leaf/session expiry minus
five minutes, with at least 15 minutes remaining. The exact three-field receipt
binds audit, qualified review and raw clock SHA. Pre-admission source construction
rejects any installation admission or clock. Missing clock after admission and
orphan clock are unknown outcomes and cannot establish another epoch. The typed
post-mint frame authenticates and rehydrates the same clock for owner execution,
bounded progress and durable completion. Finalization handoff retains its bound.

Current native reads confirm actual session/certificate, original owners,
key/CSR and retained volumes. Accepted cleanup Role overlays use only original
UID and exact prior baseline. Original descriptors and failed/accepted history
remain intact. This scope creates no seed Job, session, key, CSR or credential
copy. All local artifacts use selected-environment helpers, 0700 directories
and 0600 files.

The post-mint completed-transport reader authenticates the original completed
active-temporary proof and stopped/admitted source. Retired foundation
certificates are validated at their authenticated original qualification time,
including exact private material, keys, signatures, extensions and owners.
Its consumers are completed material and initial/inputs foundation readers.
Remaining Account uses owned HTTP loopback; accepted managed Service/OpenBao
authorities provide live TLS. Fresh managed CA/client/server/volume and selected
session checks remain current. Ordinary transport/signing readers keep their
two-hour lifetime requirement. This reader creates no identity or signing authority.

The expired-unused late renewal qualifier uses
`rtk-cloud.create-managed-late-expired-unused-renewal/v1` in
`deployment/create/openbao-recovery/<fresh-review>/`. It produces
`managed-late-expired-unused-source.json`,
`managed-late-expired-unused-seed-plan.json`, the effective
`managed-late-session-source.json` and the existing owner/custody/Log previews.
The executor and current/durable readers consume these exact pinned records.
Source authority is the preserved completed374 journal and original owner,
original key/CSR and prior expired temporary leaf/session with nine subjects and
zero installed. Any installation admission/clock, competing live session or
uncertain signing outcome blocks this profile. Only a distinct temporary
leaf/session/deployment identifier is renewed; original CAs, permanent clients,
servers and retained volumes remain authenticated. Qualification alone cannot
sign or create a session. Its preparation and current-observation producers use
the same finite prospective plan/descriptor pin derivation after source and live
checks; original historical pins and fresh native observations remain distinct.
A mismatch fails closed and reports only a safe deterministic material key.
Its 45-minute read-only window is separate from the
unchanged 30-minute native preparation and fixed native/installation clocks.

The online Device Root CRL recovery producer uses
`deployment/create/device-root-crl-renewal/<review>/` for its qualification,
original source snapshots and standalone runtime journal. The exact stopped374
parent stays immutable. A finite typed observation admits only the original
expired Device CRL while qualifying this repair; the other eight managed CRLs
retain ordinary freshness validation. The executor rotates the original
OpenBao mount, validates the original CA signature and complete revocation
union, then uses normal authenticated human import and actual API/broker cache
and ACK proofs. Rotation intent precedes mutation and an uncertain response
requires readback reconciliation. The completion pointer is
`deployment/create/material/<parent>/device-root-crl-renewal.json`; subsequent
readers consume the exact proof and fresh native publication. These canonical
0700/0600 artifacts grant no CA/key regeneration or installation clock.

A stopped once-only Device rotation may produce a signed successor whose number
skips an integer. The completion-only qualifier produces
`deployment/create/device-root-crl-renewal/<original-review>/completion-only/<fresh-review>/qualification.json`
and `source-repair-journal.json`. These pin the unchanged original qualification,
literal stopped journal, provider successor and current checker. The executor
consumes this fresh authority, records its completion-review link in the original
repair journal and resumes import/cache/ACK under the original deadline, without
invoking rotation. Current completion readers authenticate that link and actual
native evidence; the original artifact and source snapshots remain immutable.

A command-local late source capability avoids repeated pure historical ancestry
construction. It binds the exact original374 journal/owner and a bounded manifest
of private canonical producer files and directory membership. Only successful
pure Post/expired-source construction populates its encoded values; callers
receive separately decoded objects. Every hit and qualification/write boundary
rehashes inputs, and every execution save, local source-snapshot write and native
write boundary invalidates the shared capability across historical frames. The
recovery admission snapshot producer checks the existing manifest and original
owner/journal before invalidation, writes both private source snapshots, and
rechecks the original owner/journal before creating an empty capability. Final
authentication must reconstruct source in that new scope. An existing review
directory remains an input and gains no output exclusion. Each later phase
authenticates a new scope. Current native reads, leases, accepted child journals, CRLs, caches and
ACKs remain live. The capability is never serialized or reused by another command.
Historical temporary-transport readers reread the original owner and journal;
only an exact store, environment, parent, owner and journal match may carry the
active command capability into that fresh view. Manifest and configuration-key
checks still precede every hit. A missing or invalidated capability reconstructs
history; a foreign command scope is rejected.

The admitted-phase initializer runs after original audit/config/operator
validation and before the first transport minimum or foundation material read.
Those readers may carry only the committed exact-current capability into their
fresh original owner/journal view.

An admitted historical-source phase separately pins the actual canonical
journal and immutable original374 source. Its provisional capability can serve
only exact374/370 historical views; full runtime-progress validation and fresh
manifest/held-fence checks must finish before the actual current snapshot gains
matching authority. Every save and seed/Log local-artifact write invalidates the
scope. Each subsequent known read boundary authenticates a new phase; mutable
progress, child journals, native state and leases are always checked afresh.

The admitted-only375 restart path requires exactly one completed local admission
and no begun native/seed/child outcome or installation clock. It retains the
original e2 runtime review, SID, key/CSR and typed previews. A fresh checker-bound
six-gate artifact uses actual375 as its parent source and archives
`deployment/create/openbao-recovery/<fresh-review>/managed-late-expired-unused-admitted-only.json`.
This sidecar binds the old artifact/source/admission/descriptor, owner and same
prospective producer. Execute rechecks current no-mint/certificate absence,
original policy/role and native health, consumes the new GO without adding a
second admission, then uses one fixed native50 context for that command. The
historical GO is evidence of the old admission only. Unknown/native suffixes and
post-mint clocks cannot use this route.

The exact completed388 owner/Log-stop checkpoint additionally supports a
same-session remaining policy. The typed current source authenticates all
accepted children, held owner/fence and genuine seed/credential observations;
live state must be the same active SID/fingerprint with eight installed subjects
and unchanged 11 clients. A fresh checker-bound six-gate artifact archives only
`deployment/create/openbao-recovery/<fresh-review>/managed-late-same-session-remaining.json`.
The original runtime producer, descriptor and source snapshots stay immutable.

After exact fresh GO and retained audit authentication, admission exclusively
creates `deployment/create/<parent>/bootstrap/service/managed-late-remaining-authority.json`.
This is a distinct policy using genuine signed leaf NotBefore as its fixed
anchor; it does not recover the prior command's nondurable native50 epoch.
Deadline=min(NotBefore+120min, leaf/session expiry−5min, original caller deadline).
The authority binds actual388 source, original owner and producer, selected
seed identity and exact fresh qualification/checker. Reuse never advances it;
unknown/orphaned state blocks. Entry and finalizer reserve15min, finalizer cap15min
and real session life>20min remain enforced. No signing, authorization,
credential publication, parent admission append or accepted owner replay occurs.
Only remaining Log lifecycle, nine actual ACKs, finalization and full tail run.

Completed389 continuation authenticates the original388 prefix and the exact
accepted Log-helper child through full runtime progress. It requires all nine
current native installations and freshly validates the same terminal helper
Job/Pod, immutable body, process and public identity/session evidence. Its fresh
qualification separately pins `current_source_sha256` and
`helper_observation_sha256`. Those two fields are excluded from the original
remaining descriptor digest, preserving the immutable388 authority and raw GO;
they remain mandatory independent pins in the new qualification. Execution
rereads and reuses the original authority review/hash/deadline, without helper
recreation, another authority or accepted-child replay. Durable ACK completion
continues to identify that original authority review, rather than the fresh389
qualification review. Unknown suffixes and missing or changed authority fail.

The installed-nine cleanup-only action is a distinct authority transition from
that authenticated completed389 checkpoint. Original helper execution and its
accepted child must have completed within the original Remaining authority's
admitted window. Fresh full six-gate/current-checker/raw-artifact qualification
proves current389, native nine installations and the same terminal helper.
Its separate parent-stable `managed-late-installed-cleanup-authority.json` is
created exclusively once, with deadline=min(actual admission+30min, original
caller deadline, genuine expiry−5min). The original caller90, entry/finalizer15,
finalizer cap15 and real life>20 bounds remain; original Remaining120/raw GO and
its deadline stay immutable history.

Only accepted helper retirement, original Log restoration, native nine-ACK proof,
same-session finalization and exact copy cleanup may advance. The original seed,
authorization, credential and owner producers cannot replay. Known completed
retire/restore/proof prefixes require the same cleanup authority and fixed
clock; unknown progress or missing authority blocks. Durable completion verifies
both authority links, the actual389/lease prefix, original installation time and
immutable helper-observation digest. After genuine finalization, full deployment
acceptance uses the original caller context.

A further finalization-only action selects only exact completed392 after genuine
Log restoration and its native nine-ACK proof, authenticated by full runtime
progress and fresh current native identity/CA/client/server/volume checks.
Its checker-bound full six-gate/raw-artifact qualification links both historical
authorities without extending or rewriting either. The separate parent-stable
`managed-late-finalization-authority.json` is created exclusively once. Its fixed
bound is min(actual admission+native50min, first caller90 deadline, genuine
expiry−5min); entry/finalizer15 and native life>20 remain unchanged. Retries keep
the same first admission and caps.

The explicit finalization branch bypasses all seed, authorization, credential,
owner, helper and Log producers. It runs only accepted-state proof/CRL ACK
verification, same-session native finalization, tombstone and exact copy cleanup,
then returns to the stored original caller context for full acceptance. Durable
completion requires all three authority/raw links, the exact392/lease prefix
and original Log proof. Unknown or pending outcomes fail closed.

The remaining source also restores the exact original e0/e1 stale-copy observations
and historical five-ACK cleanup proof. Fresh native reobservation binds that proof
and the complete stale-copy set in the remaining descriptor/materials. Final
retirement rereads its raw-pinned remaining authority and original observations;
missing or changed evidence remains blocking. The target gate uses the existing
authenticated SID-only owner adapter for the accepted original OpenBao StatefulSet,
then retains strict body and readiness checks.

The two-minute Log PVC exclusivity reader uses an unfiltered namespace Pod list,
not a whole-stack inventory. Historical successful enrollment Pod references
require exact accepted Job UID/body and current terminal Job/Pod/container proof
before LogStop.StartedAt; all unknown, active, deleting or ephemeral execution
still blocks. The helper independently acquires the original native LOCK_NB.

The remaining policy uses a fixed 120-minute signed-leaf anchor, selected before
its first admission; an admitted receipt cannot be extended. The caller's original
90-minute deadline and genuine expiry reserve remain additional clamps. Only two
read-only preparation boundaries avoid duplicate complete native inventories.
They retain held owner/fence, exact authority, fixed budget and pure full runtime
source/progress validation. Auth, seed budget, accepted-owner readiness and every
actual helper/retire/restore mutation still require their original fresh native
checks. No mutable observation is cached or reused as mutation evidence.

Final create target observation receives the bootstrap runtime context and calls
the existing typed late cleanup adapter before rendering. Genuine finalization,
accepted cleanup producer, exact ConfigMap UID/body and original Controller mount
select the new binding. The final equality check still rejects drift; ordinary
create/upgrade observation is unchanged outside that explicit context.

The completed OpenBao TLS Root2 current-consumer observer treats its archived
ICA3 publication as history. It independently qualifies a fresh signed automatic
successor of the same original ICA, preserving the original certificate/SPKI and
Root lineage, full published/native revocation union and genuine current two-cache
and two-ACK evidence. A final latest-publication reread rejects rotation races.
Original Root2 artifacts and archived ICA3 hashes remain immutable; initial Root
repair qualification retains its exact historical predicates.

The terminal-finalizer completion adapter selects exact396 only after genuine393 publication, two accepted secondary-copy retirements and the original pending native finalizer. It validates the immutable392/374 lineage, all three earlier raw-bound authorities and actual sealed/revoked same-e2 state with current signed CRL/full-nine ACKs. Admission produces `deployment/create/<parent>/bootstrap/service/managed-late-terminal-finalizer-authority.json`; execution resumes only that recorded native operation and exact cleanup. Its independent cleanup deadline is fixed once at admission to30 minutes, capped by that first caller's90-minute deadline and real leaf/session expiry minus5 minutes, with15-minute entry reserve. The old50-minute receipt and old overall cutoff remain unchanged historical links; the terminal receipt stores both old and new overall bounds. Runtime proof after known cleanup appends uses the existing strict finalizer-tail validator. Completion produces `managed-late-terminal-finalizer-completed.json`, consumed by durable completion validation and bound to the original pending intent, raw qualification, native receipt and all three old authorities. The393 mirror is never rewritten. Pure374/370 constructors may be shared only under an exact396 owner/journal/manifest capability and are invalidated by every journal or artifact write. The admission producer first writes and syncs the byte-identical private sibling `managed-late-terminal-finalizer-admission.json` with O_EXCL, then writes and syncs the authority. Admission, runtime and durable readers require both canonical files to be present, private and byte-identical. An orphan, unreadable file or mismatch blocks without searching for a fallback or starting another clock; original bytes are retained for explicit reconciliation.


### Post-native cleanup continuation

The post-native adapter selects exact400 with the original pending396 finalizer, accepted custody transition397 and three genuine original-seed copy retirements. Its source loader authenticates the immutable396/392 history and original terminal pair; native observation proves finalized state, the real matching custody tombstone, bootstrap-file/current-copy absence, and unchanged current identities, issuers, CRLs and volumes. It grants no new identity or finalizer request. Only uncompleted, exact historical namespace/UID retirements and the original pending receipt may advance; used-seed installation proofs remain mandatory.

After fresh full qualification, the admission producer writes `deployment/create/<parent>/bootstrap/service/managed-late-post-native-cleanup-admission.json` and then the byte-identical `managed-late-post-native-cleanup-authority.json`, using exclusive creation, file sync and directory sync. Source, admission, runtime and durable consumers require the same canonical0700 directories and0600 regular files; an orphan or mismatch blocks. Schema `rtk-cloud.create-managed-late-post-native-cleanup/v1` stores one30-minute cleanup deadline with15-minute reserve at the first admission, capped by the current caller, original terminal caller and genuine expiry reserve. Retries of the same admitted pair require only positive remaining time and a caller covering the fixed deadline; they reuse the first recorded clocks without a new15-minute reserve. Fresh full six-gate GO and the current checker remain mandatory. Completion adds explicit post-native review/authority links to `managed-late-terminal-finalizer-completed.json`; native finalization must remain within the unchanged old terminal window, while local completion uses the separately recorded cleanup deadline. All old authority bytes,393 publication and accepted cleanup receipts remain immutable.


Post-native target verification requires the exact session ACL policy to be absent through a successful authenticated bounded policy listing (or a verified native404); a denied or failed request is not absence. It retains the original shared Kubernetes-role body and receipt, recorded Controller/signer, and current OpenBao owner, process, init and audit checks. The native cleanup contract deletes the session policy; it does not delete the shared role.

### Explicit operator grant for residual bootstrap-copy cleanup

Residual-copy recovery is a separate, explicitly approved operation after
genuine native finalization; it preserves the original environment generation,
all old authority pairs/clocks and original396 intent/start. Its scope is the
exact24 remaining source400 Secret UIDs, original finalizer receipt completion,
then the normal FULL deployment/acceptance tail. Its schema is
`rtk-cloud.create-managed-late-residual-copy-cleanup/v1`; fresh current-checker
six-gate GO is mandatory. Admission creates one60-minute cleanup clock and
retains the newly approved first caller's90-minute overall clock, capped by
finite credentials actually used minus5 minutes. First admission reserves15
minutes; retries preserve and consume the same unexpired clocks.

An admitted retry may have completed approved retirement entries after source400.
Routing, descriptor loading and admission must authenticate the unchanged400
prefix and the finite completed UID suffix, then reuse the existing pair. Early
observations can precede installation acknowledgments; historical nonzero
installation is qualified through original accepted workload-owner evidence
and fresh native state rather than treated as an unused session.

The normal deployment tail uses that recorded overall context only after
genuine original396 completion. If a repeated handoff occurs after the cleanup
deadline, it reads the existing durable terminal proof under the still-live
overall context and rechecks the actual execution fence and all24 retirement
receipts. This path writes no completion data and rejects cancellation,
shortened caller deadlines, missing proofs and an expired overall context.

The full-create Account Manager registration subplan retains the exact external
dependency closure from its authenticated managed-core recipes. In particular,
`pki-controller` and `certissuer` remain unchanged retained prerequisites, and
the existing registration TLS export precedes the Account Manager Deployment.
The shared dependency compiler and retained-resource drift checks remain in
force; selecting one namespace never removes required dependency edges.

The bootstrap leaf's signed expiry and finalization are historical facts after
revocation/destruction. Current residual authority must never authenticate with,
extend or regenerate that leaf. The finite historical proof requires genuine
native finalization before leaf/session expiry and inside the old terminal
window, current matching tombstone and bootstrap file/copy absence, and fresh
9CA/11permanent-client/4server/18volume/CRL evidence. Current Kubernetes access
uses the frozen selected operator credential. Only authenticated signed
non-expiring Kubernetes tokens may represent no finite expiry; other active
credentials retain real expiry bounds.

The following private files are all relative to
`~/.config/rtk_cloud/<environment>/deployment/create/<parent>/bootstrap/service/`.
Every producer uses0700 directories/0600 files and never overwrites evidence.

| Path | Producer | Consumer / meaning |
| --- | --- | --- |
| `managed-late-residual-copy-cleanup-proposal-60m.json` | Operator prepares the concrete finite scope | Approval reader pins raw proposal SHA, exact24 UID list, source400, historical authorities and genuine failure |
| `managed-late-residual-copy-cleanup-operator-approval.json` | Operator records explicit human decision | Residual descriptor/authority reader; permission metadata, not GO or an execution grant |
| `managed-late-residual-copy-cleanup-source-journal.json` and `...-source-owner.json` | Operator preserves exact approved400/owner bytes | Proposal reader checks raw and semantic pins plus parent-stage ownership receipts; live fence/current prefix are checked separately |
| `managed-late-residual-copy-cleanup-failed-run.json` | Operator preserves exact actual failed CLI result bytes | Proposal reader verifies the hash-bound historical failure even after the mutable CLI result changes |
| `managed-late-residual-copy-cleanup-grant-admission.json` then `...-grant-authority.json` | Qualified residual admission; witness-first O_EXCL and sync | Runtime/durable readers require the identical private pair, original first clocks, current scope and fresh credentials |
| `managed-late-terminal-finalizer-completed.json` | Genuine original396 completion | Existing durable terminal reader additionally binds residual review/authority, exact24 completed retirements and separate old-native/new-local windows |
| `managed-late-residual-copy-cleanup-remaining-proposal-60m.json` | Operator prepares the exact remaining UID scope after a stopped admitted attempt | Explicit human review; non-authority proposal, no new clock |
| `managed-late-residual-copy-cleanup-remaining-source-journal.json` and `...-remaining-failed-run.json` | Operator preserves exact actual progress/failure bytes exclusively | Remaining-only proposal review and subsequently approved qualification; never substitutes for current fencing or a grant |
| `managed-late-residual-copy-cleanup-remaining-operator-approval.json` | Operator records the actual explicit remaining-scope human decision | Remaining descriptor reader pins exact proposal/source403; permission metadata, not GO or admission |
| `managed-late-residual-copy-cleanup-remaining-grant-admission.json` then `...-remaining-grant-authority.json` | Newly qualified remaining-scope admission; witness-first O_EXCL and sync | Private identical-pair runtime/durable readers; one new60-minute cleanup and new first-caller90-minute overall bound |

The explicitly approved remaining21 phase uses schema
`rtk-cloud.create-managed-late-residual-copy-cleanup-remaining/v1` and a fresh
current-checker full six-gate GO. It authenticates the exact403 snapshot against
the original400 lineage and old residual pair. The completed three receipts
retain the old cleanup window; only the remaining21 and local original396
completion use the newly admitted window. Original396 intent/start and native
finalization retain their original evidence and terminal deadline. First
admission reserves15 minutes, finite current credentials retain5 minutes, and
retries consume the same new clocks. The existing terminal completed proof
binds the old and new grants; no old authority, receipt or identity is rewritten.

A stopped normal tail after the exact four Account Manager custody/export/retire
entries is qualified against the actual held428 journal. Its private in-memory
completed-tail frame binds that journal, original owner, durable terminal proof,
original143-lease ancestry, both accepted later child plans and their two leases,
and the unchanged remaining grant's overall deadline. A pure392 view is used
only to authenticate recorded ancestry. Current native observations and execution
fencing always use the actual journal. Fresh full six-gate qualification binds
the current checker and independently observed native evidence; it does not
admit another grant or extend the recorded deadline.

Because the original export helper has genuinely retired, this continuation
reads the same Account Manager server credential through the original live
`pkimanagement` container's `serviceidentity-bootstrap export-server` contract.
It verifies original PVC, Deployment, Pod and ReplicaSet ownership, approved
image and mounts, registry/root/server identity, and the exact public certificate
hash from the completed export receipt before using the credential in memory.
It creates no helper or identity and rewrites no completed receipt. Private
material follows the existing canonical child-plan/TLS Secret publication path;
it is never printed or saved as a separate export artifact.

The subsequent literal433 checkpoint has accepted registration429, genuine
custody retirement430–431, pending ingress432 and accepted identity433. Its
distinct remaining-normal-tail90 profile authenticates those exact child plans,
original lease prefix and three added leases. Current Account Manager intent is
the accepted429 projection, including the registration Secret; live observations
are compared with that intent, never copied into it. Original custody absence
and genuine immutable cleanup/tombstone history do not fabricate a current live
reader observation. Permanent identity, CA/CRL, session, copy-absence and original
volume observations remain fresh.

The normal-tail descriptor binds two distinct kubeconfig representations.
`KubeconfigSHA` pins the original selected file already authenticated by the
parent plan; `CredentialSHA` pins the backend's normalized selected-context
bytes. Qualification, admission and native verification preserve both bindings.
The hashes need not be equal. Current-credential metadata separately binds
the normalized credential mode and expiry without replacing the raw-file pin.

The bounded retry projection may include accepted434 ingress controller, with
432 pending or completed. It retains the exact approved433 ancestry and the
first authority's fixed deadline while qualification pins the actual current
journal. Accepted controller resources and their lease require exact plan,
UID/body, retained Secret, image and readiness readback; the controller is not
applied again. A still-pending432 receives its original operation's genuine
completion only after that readback. This projection grants no authority for
arbitrary later partial history.

After a completed434 checkpoint, ACK release readers may borrow the held
command's pure ancestry capability only after the strong source loader
reauthenticates that exact checkpoint, current owner, manifest and unchanged
normal-tail authority. Native observations remain fresh. The reader carries
authenticated owner metadata, never a write fence. Bootstrap and final target
compilation use this bounded reuse instead of independently expanding the same
historical ancestry.

`deployment/create/<parent>/bootstrap/bindings.json` is produced by completed
bootstrap and consumed by final target compilation. A normal-tail retry may
reuse only identical private bytes. A genuinely new publication checks the
active source, invalidates it before exclusive creation, and requires a fresh
strong source before subsequent reuse. Changed bytes or unsafe paths refuse.

An owned Controller using the maintained native Service identity profile keeps
its original host/client state, shared Root, state PVC, trust ConfigMap and
platform-key Secret. Its final renderer suppresses only legacy static TLS and
account-key defaults absent from that validated profile. Identity transition
checks remain unchanged.

The exact completed434 normal continuation may independently qualify a Redis
emptyDir-to-PVC migration before full target compilation. Its separate
`accepted-service-core-late-normal-redis-durable-*` materials bind the accepted
original Deployment, Pod/ReplicaSet owner chain, pinned image, maintained claim
and helper intent, original18 volume map and unchanged normal authority.
`normal-tail-redis-durable.json` belongs to the fresh qualification directory;
it does not change the original normal descriptor, policy or first deadline.
Qualification reads prerequisites and current data only; it creates no snapshot,
claim, identity or migration receipt.

Execution retains the original parent fence and clock. It first allocates the
maintained Retain claim and an isolated helper, then pauses the exact original
writer with a bounded renewable lease. It saves and privately archives the RDB,
verifies the copied bytes and nonexpiring string dataset, enables and validates
durable AOF on the isolated target, and retires that helper. It then stops
only the original Deployment UID and proves the original Pod absent before
the pause expires, before starting that same owner on the populated claim.
Native dataset/AOF/PV proof must precede genuine completion. The ordinary durable-storage transition guard remains unchanged.
Before a switch attempt, failure restores only the exact original writer; the
target becomes stale when source writes resume. An uncertain switch preserves
its checkpoint and archive for reconciliation; it cannot restore emptyDir,
adopt a claim, fabricate completion or replay an unknown write.

Local migration artifacts are under
`deployment/create/<parent>/bootstrap/redis-durable/`: `source.json` records
qualified intent and original434 source, `source.rdb` is the exclusively retained
private archive, `volume.json` records actual bound allocation, and
`completed.json` records verified cutover. Separately reobserved public
identity/owner bindings are published at `bindings.json` in this directory for
final compilation; the original `bootstrap/bindings.json` remains intact. The
migration runtime produces these for its strict source/reconciliation readers
and final ownership verification.
The exact pre-pause437 checkpoint has a separate recovery profile. It retains
the saved434 source, accepted claim and `volume.json`, plus the helper child's
original plan, completed resource-write receipt and Pod UID. A known failure
of that child's acceptance, with no in-flight write, permits fresh qualification
of the actual437 journal and acceptance-only continuation of the same helper.
The helper must match its maintained standalone-Pod recipe and recorded UID,
immutable image, Running/Ready state and container evidence. The general FULL
controller requirement remains in force for other targets. Only genuine helper
acceptance can complete the original pending entry and allow the ordinary
source reobservation, closing-reserve check and pause sequence. Existing timeout
pairs and their original clock remain unchanged. Other partial histories,
unknown writes, changed owners or any evidence of pause/cutover refuse this
profile; it does not recreate the claim/helper or fabricate a child receipt.
The acceptance-only executor retains its failed-journal copy, actual read
revalidation lease and successful outcome in private
`deployment/rollback/<helper-plan>/acceptance-recovery/attempt-*` receipts.
It verifies the same Pod before and after acceptance, then records the real
accepted child journal. It never calls resource admission or resource writes.
An already accepted child requires that immutable successful attempt proof.
The exact437 parsing view can retain a genuinely completed helper entry only
with its matching original plan/owner receipt and accepted child proof; it
does not qualify a snapshot or any later partial migration checkpoint.
At this pre-pause checkpoint, fresh inventory already includes the accepted
Redis claim. The old bootstrap binding comparison authenticates that exact
claim and volume proof, projects out only its matching PVC owner, and requires
every other binding to equal the original saved bytes. It retains that original
file. Qualification performs the same comparison before GO; after migration,
the separate Redis binding producer retains the full current ownership map.
The typed `source.json` producer and its existing consumer share a4MiB bound,
including the authenticated full434-entry source journal. It uses the private,
exclusive environment-state writer; the generic public ceremony metadata
writer retains its256KiB limit. Oversize, unsafe, changed or already-published
source state refuses instead of truncating history or weakening its digest.
All use the selected canonical store,0700 directories and0600 files. The target
runtime separately keeps `/data/rtk-migration/source.rdb`, private helper log/PID
files in that directory and verified AOF in `/data/appendonlydir/`; none belong
in configuration. The immutable original18 map is preserved independently of
new claim allocation evidence and the eventual full resource ledger.

After verified Redis completion, full ACK validation keeps the actual held
executor as read-only volume provenance while historical readers parse their
exact361/374/392 prefixes. Independent completed-owner validation instead
retains an authenticated canonical journal snapshot, with no execution fence. It authenticates the typed new allocation and full
ledger first, then retains the same original18 material hashes. The same typed
projection covers the genuine new claim at the authenticated pre-pause437
checkpoint. Independent TLS/MQTT readers load only the journal path recorded
by the original owner, verify its active or archived ownership and complete
new-allocation provenance, then recheck unchanged journal/owner digests before
attaching the read-only snapshot to historical readers. This pointer
grants no native write fence and does not reuse the invalidated434 cache.

New explicit proposal approval and current-checker full six-gate GO precede
this phase's distinct witness-first identical authority pair. The once-admitted
new first caller's90-minute deadline stays fixed across retries, with the current
credential5-minute reserve and15 minutes required at first admission. Old grant
bytes, deadlines and completed receipts remain unchanged. This scope resumes
only original pending ingress with its existing material and immutable Secret,
then the normal deployment/acceptance/release tail. It grants no bootstrap,
signing, cleanup, identity generation or replay authority.

A separately approved timeout supplement may raise the normal tail's total
cap to180 minutes from the original normal authority's unchanged `StartedAt`.
It leaves the original90-minute pair, policy, descriptor and all historical
PKI/cleanup proofs intact. Its distinct witness-first identical pair binds the
actual434/149 source, original owner and authority, new human decision, fresh
current-checker six-gate GO and current credential evidence. The effective
cutoff is the earliest of original-start+180 minutes, the first supplemental
caller's deadline and a finite credential expiry minus5 minutes. Admission
time records authorization; it never becomes a new time origin. Retries reuse
the exact same supplemental bytes and cutoff. At least15 minutes must remain
for first supplemental admission and the Redis closing checks.

The supplemental proposal, explicit decision and original source/owner copies
are under `deployment/repairs/managed-late-normal-tail-timeout-supplement-*`.
The qualified `managed-late-normal-tail-timeout-supplement.json` belongs to its
fresh review directory; paired runtime records are under the parent's
`bootstrap/service/`. The loader and admission runtime produce/consume these
only in the canonical selected store. Redis qualified intent binds the
supplemental intent; its saved source and genuine completion additionally pin
the actually admitted supplemental authority. Missing approval, stale GO,
changed source, unsafe paths or a partial/different pair refuse. This mechanism
grants only the same pending normal deployment and qualified Redis lifecycle,
with no bootstrap, signing, cleanup or completed-operation replay.

A further timeout extension has a separate maximum of360 total minutes from
the same original normal `StartedAt`. It requires a new explicit decision,
the authentic original90 pair and already admitted180 pair, exact434/149
source and owner, and fresh current-checker full six-gate GO. Its separate
witness-first identical pair pins the admitted180 authority as its predecessor;
neither earlier pair, proposal, descriptor nor clock is rewritten. Its effective
deadline is bounded by original-start+360 minutes, the caller deadline and any
finite current credential expiry minus5 minutes. First admission and Redis
pre-pause checks still reserve15 minutes; retries consume the same recorded
deadline. The new admission time is an authorization fact, not a time origin.

Extension proposals, actual decisions and exact source/owner copies use
`deployment/repairs/managed-late-normal-tail-timeout-extension-*`. Qualification
publishes `managed-late-normal-tail-timeout-extension.json` in the fresh review;
runtime pairs use the parent's `bootstrap/service/` extension paths. Active
Redis intent and genuine source/completion authority pins must authenticate
this extension chain. Missing state or an old180 decision cannot activate the
360-minute window. Native scope remains the same qualified Redis preservation,
normal deployment, full acceptance and original release.

After a known local authority publication invalidates a pure-history scope, a
fresh strong completed-tail constructor may authenticate the exact current434
source, owner, configuration and enlarged manifest once and supply a newly
proved392 ancestry view. It does not revive the invalid capability or omit the
new authority files from the manifest. Native identity, credential, CRL, lease,
UID/body and readiness checks remain fresh.

The unapproved30-minute proposal may remain as historical draft; only the exact
explicitly approved60-minute proposal is selected, without fallback. No separate
residual completed sidecar duplicates the existing terminal proof. A grant
cannot authorize native POST, signing, identity generation, key/CSR reads, custody
cleanup or completed owner/helper/Log/Secret replay.

A missing expired-copy archive is published only after held-execution and exact
source checks. Any active historical capability is checked and invalidated
before that exclusive local write. A capability previously invalidated by a
known journal write supplies no cached authority. The following native deletion
guard reauthenticates the full enlarged manifest; no output is excluded to
conceal a change.

`deployment/reports/dev-after-go-step-times.json` is an operator-produced,
non-authority timing view consumed by human review. Its inputs are actual
metrics, closed command-step records and original journal receipts; overlapping
probe durations are not summed and it cannot establish deployment acceptance.

Operator diagnostics for this dev continuation keep temporary Go probe sources,
overlay maps, build metadata, CLI result snapshots, read-only process samples and the closed-step timing runner under
`deployment/repairs/dev-continuation-20261013/` in the selected canonical store.
The operator produces and consumes these private diagnostics; they neither
replace execution evidence nor authorize mutations. A real read-only Account
Manager export check may save only public status, certificate hash, unchanged
owner result and elapsed time at
`deployment/reports/dev-completed-tail-read-only-export-validation-<UTC>.json`.
Human timing review consumes that metadata; private export output is never
written there. These paths retain0700 directories/0600 files.

An initialized recovery may reject its entry before writing a structured result.
The operator may record the actual stderr stage and closed runner timing at
`deployment/reports/dev-normal-tail-qualification-entry-diagnostic-<UTC>.json`.
This diagnostic explicitly distinguishes the unchanged previous result from
the current failure, binds the unchanged source hash and grants no authority.
It cannot replace a CLI result, native receipt or preflight qualification.

## Staged create program and recovery boundary

The [create and resume flow](deployment-operations.md#create-and-resume-as-one-staged-program)
separates completed-generation evidence from work still to execute. Its
remaining program freezes complete consumer definitions plus typed native
producer contracts. Dynamic issuer/session/Secret UIDs are resolved only from
actual native outputs into those definitions. The ordinary resource engine owns
admission, writes, readiness, journal recovery and acceptance for every child;
recovery does not invent a second desired workload.

The initial product target includes all six accepted temporary producer
Deployments. Full-plan to scoped-plan conversion preserves each Resource UID on
a cloned definition; it never drops a retained owner's fence or rewrites the
saved full plan. Cleanup-control observation advances from the original handoff
only through the accepted same-program controls child and checks the current
ConfigMap, Controller, namespace and mounted bindings. After native finalization,
steady consumers remain bound to their original accepted workload UIDs. Ordinary
retirement children remove only the six original preparation Deployments. The
final compiler excludes them only after accepted retirement and fresh absence;
identity PVCs and permanent Secrets remain. Fresh restoration resumes the same
steady/retirement children before broad owner checks, so completed deletions do
not block their own continuation.

For a recorded successful retirement response, the resource engine resumes the
existing wait and verifies absence without another DELETE. A still-present object
must have the original UID and body and a native deletion timestamp. Only API
termination markers are normalized on comparison copies; saved plans and journals
remain unchanged. An unknown in-flight response has no such recovery permission.

The shared resource matcher distinguishes desired configuration from Kubernetes
bookkeeping. It ignores only the controller-owned top-level Deployment rollout
revision for `apps/v1` Deployments. New desired objects omit that annotation;
recovery and update-body comparisons normalize copies of historical objects.
The public observation projection and existing serialized plans, baseline hashes
and native receipt fingerprints retain their historical representation. A normal
controller counter increment therefore cannot invalidate a completed write or
rewrite the original evidence.

The planned volume graph compares StatefulSet claim templates after normalizing
only their native PVC API envelope and server-owned metadata on copies. Names,
labels, UIDs and allocation specifications remain exact. A successful StatefulSet
write followed by a ledger-capture failure resumes its original wait and capture;
it does not repeat the update or allocate another claim. Shared volume diagnostics
identify the allocation graph rather than attributing every failure to OpenBao.

Each normal workload phase passes its newly fetched inventory to the common
engine's existing phase fence. The engine reuses that observation for the same
phase, reads missing objects directly, and still verifies UID and body immediately
before a write. It does not repeat a serial read of the entire resource catalog
or reuse an earlier phase's snapshot.

A same-Root Service successor is a two-step trust transition. READY bundle
publication and original-owner workload reload precede bundle ACK and activation.
The activated issuer's native signed CRL then precedes CRL-manifest publication,
consumer reload and current-roster CRL ACKs. The manifests include standalone CRL
ConfigMaps as well as bundle manifests. These are ordinary resumable children,
not separate preflight runs. Final resolution keeps the successor trust while
retaining the selected release's complete workload definitions; temporary reload
bodies are never substituted for that target.

OpenBao client trust follows the listener's lifecycle phase. Initial foundation
clients use the temporary transport CA; product sessions created after managed
transport activation use the existing managed consumer trust ConfigMap and its
explicit file projection. New product programs record this choice without
changing previously serialized programs. A terminal pre-login TLS failure uses
a distinct, recorded executor with the original session intent and custody.
Its composite completion references both the unchanged failed child and the
genuinely accepted correction child. The read-only custody child is retired
before the correction Job mounts the RWO claim; the Job checks the saved key/CSR
hashes and private modes before native execution, without recursive fsGroup
changes to existing files. Reader recovery handles the intermediate states
without requiring completion evidence before its producer can run. The projected
OpenBao token has its own explicit readable, read-only mount mode; it must not
require a recursive private-volume ownership change. Definite pre-mint failures
can append same-intent attempts, preserving their native failed outcomes and
selecting the genuinely accepted winner. Operator authentication refreshes before
product API calls after long waits, under the same original operator identity;
unknown mutation outcomes are never retried by that refresh.

Native bootstrap certificate recovery shares the existing strict three/four-block
chain verifier. The four-block form permits only a byte-identical repeated
intermediate from OpenBao's `issuing_ca` and `ca_chain`; it does not rewrite the
credential or relax permanent product identity export contracts. Product control
rendering receives the selected configuration's derived environment values, as do
ordinary workload renderers, while the saved configuration remains unchanged.

OpenBao's TLS installer and running PKI sidecar read the central PostgreSQL
certificate registry. The maintained `allow-postgres-openbao` NetworkPolicy
allows only Pods labelled `app.kubernetes.io/name=openbao` in the selected
`secrets` namespace to reach PostgreSQL TCP 5432. The namespace and Pod selectors
are in one peer, so both must match. The existing application policy remains
unchanged; Logger's SQLite inbox does not require this database permission.

Native connectivity is derived from the actual caller Pod labels, selected
namespace and destination container port after resolving a Service's targetPort.
The maintained catalog includes OpenBao's database access, native issuer and
Account Manager enrollment ports, product Controller acknowledgments, API access
to TURN/OTA/MQTT, and Grafana's Prometheus and DNS access. Namespace and Pod
selectors remain conjunctive; disabled consumers do not gain extra access.

An admitted older workload repairs these missing prerequisites through one
ordinary `runtime-dependency-access` child under its original execution, before
product hydration or readiness checks. The child records each real policy UID;
phase fences and final-target resolution verify those owners and exact rules.
Known writes resume without another CREATE. Unknown responses retain the common
engine's recovery rules. New generations receive the same policies from the
ordinary catalog. There is no additional preflight or admission clock.

When the interrupted target also contains an incorrect future MQTT transport,
`normal-tail-workload-forward.json` preserves the original target and failed
journal and declares one exact corrected target. The only desired-state changes
are the maintained managed-MQTT binding for the three affected consumers, its
required trust mounts and volume-safe scheduling, and the Controller's matching
runtime consumer IDs. The original pre-activation bundle consumer list remains
unchanged, avoiding a dependency on workers that need the active broker first.
The standard `workload-forward`
child retains all recorded original owners and executes the remaining work.
Unrecorded creates must still be absent. The original logical workload completes
with a composite receipt only after the forward child is genuinely accepted;
its original failed native journal remains unchanged and unaccepted. Shared
readers consume both sources for ownership, volume allocation and final closure.

Public trust bindings reuse an existing read-only ConfigMap mount when its source,
item mapping, permissions and path are identical. A role-name alias does not
require a second mount at the same path. The common renderer validates the
result before publishing a continuation target.

A rejected derived forward target may be corrected before any forward stage has
started. The shared reader authenticates the exact prior derivation and original
scope; the producer requires no parent stage intent, saved child input, rollback
directory or matching child lease. Under the original execution fence it archives
the exact rejected bytes at `rejected/<raw-sha256>.json` before atomically replacing
only the derived descriptor. Recovery reuses the same archive. Once execution
evidence exists, the target is immutable. This does not change the original
Program, failed native plan/journal, identities, admission or deadline.

The frozen storage-input digest continues to cover the original plan's existing
volumes. An authenticated forward may retain PVCs successfully created by that
original child; these are completed outputs, not additions to the old input
cohort. The phase fence checks the original cohort against its unchanged digest
and separately verifies each added retained claim using its original write owner,
recorded allocation ledger, current PVC/PV/CSI identity and frozen desired body.
It never rewrites a started plan or derives new ownership from an unbound current
observation. The shared executor preserves a typed fence failure's specific
cause instead of reporting every fence failure as configuration drift.

MQTT CRL state retains its persisted monotonic floor. Workers reuse their own
identity PVC with a separate CRL directory; required Pod affinity matches the
exact same-volume preparation Pod or the worker itself on the same hostname.
This permits the temporary overlap and later worker replacement without another
claim, identity generation, or early producer retirement. Private key permissions
remain unchanged. Broker TLS trust comes from the authenticated existing MQTT
Root and CRL references; each worker uses its own enrolled Service identity.

Schema production follows that same dependency graph. The common catalog declares
the first-create Billing migration Job before its application consumers, binds it
to the selected immutable image and canonical database inputs, and verifies the
applied versions after native completion. Its plan, Job UID, readiness and recovery
use the existing child journal paths below. Preflight checks a genuinely empty
database against this future producer contract; it never manufactures an applied
migration history or repeats the completed Account Manager bootstrap. Failed
qualification reports retain the exact selected public release metadata and safe
schema-owner diagnostics for direct investigation.

### Billing configuration and interrupted workload recovery

The common Billing renderer and dependency validator use the same selected
configuration and canonical operator values. An explicit payment-provider flag
wins over Test Lab defaults. Enabled PayPal requires its three canonical runtime
credentials before a plan can run. If no payment provider is enabled, the payment
worker has zero replicas; the Billing API remains required. A disabled simulator
also has zero replicas. The shared full-health check uses these intended replica
counts and still verifies the API's real readiness.

For an older admitted workload stopped at a known Billing readiness failure,
`deployment capture-runtime-configuration-source` saves the original private
snapshot before an operator changes payment inputs. It authenticates the
original Program, owner, partial plan and journal, checks the existing deadline,
and makes no Kubernetes request. The supported correction permits only the
three payment-provider flags and three PayPal runtime credentials; all other
configuration, identities and data inputs remain original.

The continuation derives `normal-tail-workload-forward-0002.json` from that
source and the actual partial journal. It changes only Billing runtime data,
its consumers' checksums, and the intended worker/simulator replica counts.
Previously written resources retain their known UIDs; the runtime Secret and
Billing Deployment update under those same UIDs. Unwritten creates must be
absent. The ordinary child executor performs readiness and acceptance under the
original execution deadline. The old native journal remains failed; explicit
parent receipts point to the genuinely accepted successor. Final compilation,
ordinary observation and independent completion verification consume the same
correction. Allocation verification follows every authenticated predecessor,
including StatefulSet volume claims created by the interrupted successor.

Implementation status on 2026-10-19: integrated after selected-input rendering,
native Billing configuration and complete interrupted-continuation tests passed.
The recovery fixture reaches the final compiler, original closure, ordinary
observer and independent completion reader; 35 unsafe evidence substitutions
are rejected. Kubernetes/provider boundaries in this fixture are simulated.
Separate real-environment read-only checks verify all 329 prospective resource
bodies and existing storage ownership. These results do not establish live
deployment completion or a new complete health acceptance.

The 2026-10-19 live retry exposed an additional input-contract defect: the
payment-reference material had been generated as a 24-byte URL-safe token,
while Billing requires a standard-Base64 32-byte encryption key. The maintained
producer now generates the native format for new material. Existing valid
native keys remain literal. A recognized historical token uses the original
compatibility recipe, SHA-256 of the unchanged token followed by standard
Base64. The renderer, preflight dependency validation, SecretStore mirror
verification and binding synchronization share this projection. Unknown formats
fail before publication. Applying this recovery to existing data requires proof
of the original key provenance; it must never silently replace a working key.

The earlier native test result was insufficient: the overlay used only a
symlinked workspace path, and Go could exit successfully without discovering
its injected tests. The maintained probe now maps both resolved and logical
paths and requires explicit run and pass events for every exact test and
subcase. Actual canonical Billing inputs have separately passed the native
configuration, encryption round trip, selected API configuration and worker
constructors. These probes do not make payment, database or provider writes.

Repeated runtime corrections use contiguous numbered successors through the
same child executor. Each source after the first captures the previous effective
snapshot and pins its literal descriptor, plan, journal and parent intent.
Optional sequence and projection-profile fields preserve the original `0002`
bytes and its historical interpretation. A stopped partial child may have a
known resource prefix and at most one known next response; an uncertain write
cannot be adopted. The next source may be published before its descriptor and
is reused after a publication interruption. Native journals remain immutable;
logical parent receipts name the genuinely accepted descendant. Volume and
completion readers follow all predecessors, including allocations made by an
intermediate correction. The complete repeated-recovery fixture reached genuine
original closure and independent ordinary observation on 2026-10-19. Its terminal
test-only bridge initially expected three sources instead of four; that raw
failed command is retained, and the corrected exact bridge passes against the
same retained execution export. Compatibility cases and an independent reader
with 62 tamper cases pass. Native boundaries in the lifecycle fixture are
simulated; actual selected startup inputs and prospective ownership/storage
have separate native-constructor and GET-only qualification. These tests do not
replace live original closure and a new independent FULL health result.

The final combined regression on 2026-10-19 passed as one complete command in
417.08 seconds, with no failed or skipped tests. It includes the safe fence
diagnostics and temporary-copy cleanup guard below, injects a workload-phase
interruption into `0003`, and resumes that exact plan through product ACKs,
cleanup interruption recovery, final compilation, original closure and the
independent reader. It verifies no new successor or replay of known writes.
The actual stopped deployment's 260-resource prefix also passed a separate
read-only full inventory fence. The original masked failure's precise cause
was not recorded; the reproduced cleanup race is not proof of that historical
cause. Canonical evidence is
`deployment/repairs/global-flow-20261019/runtime0003-final-combined-e2e-validation.json`
and `runtime0003-actual-fence-readonly-clarified.json`.

The current-state fence reports fixed safe categories for plan, configuration
resolution, snapshot reads, configuration digest, selected release, bindings and
Kubernetes inventory failures. It does not expose raw credential-bearing errors
or claim configuration drift when a native inventory read failed. These
diagnostics preserve the same predicates, recorded plan and execution window.

Native Kubernetes commands create a private copy of their frozen API identity at
`deployment/tmp/kubernetes-api-<numeric>/kubeconfig.yaml` and remove that copy when
the command exits. Whole-store validation can run concurrently with those
commands, including independent FULL health. If a previously enumerated native
temporary directory or its exact `kubeconfig.yaml` disappears before inspection,
that ENOENT is normal cleanup. The exception applies only to this exact temporary
shape. Existing directory/file modes and symlinks, parent directories, other
paths and errors, and required canonical Secret/PKI/journal reads remain strict.
An isolated regression uses the actual private-copy producer and concurrent
whole-store validation; the old implementation fails and the corrected one
preserves all durable-state rejection cases.

Recorded-prefix resume still reads each original owner, checks its desired body,
and runs native readiness before and after acquiring the execution lease.
For a resource whose original action is `retain`, create-specific readiness does
not regenerate the entire allocation ledger and removal proof. A retained
resource creates no allocation; fresh phase storage checks and final capture
remain required. Original create/update operations still capture allocations,
including initial OpenBao OnDelete creates whose readiness path is translated
to a retained-owner check. The capture decision therefore uses the original
action before that translation. This removes repeated allocation-producer work
per recorded PVC/StatefulSet without caching or skipping owner/readiness checks.
The exact combined regression passed in 420.91 seconds, and the real common
adapter/ordinary-engine resume suite passed 16 subcases in 13.322 seconds,
including original create/update capture and UID, readiness, PV and CSI failures.
Canonical evidence is `recorded-storage-wait-full-e2e-validation.json` and
`recorded-storage-wait-focused-validation.json` under the same repair directory.

### Recovery after product issuance outlives its bootstrap session

Implementation status: integrated and validated in isolated tests on 2026-10-19.
The combined affected suite covers 739 Go test cases including subtests, with
zero unresolved failures or skips. The final recovery fixture reaches genuine
original closure, the ordinary observer and independent completion reader.
Provider/Kubernetes boundaries in that fixture are simulated; a separate native
PostgreSQL/TLS bridge validates the protocol. Live deployment and a new complete
post-deploy health report remain required before declaring the environment done.

A deployment pause can outlast the temporary bootstrap session after all product
identities were issued but before their real consumers start. Permanent product
certificates remain separate identities with their original validity. An expired
bootstrap session cannot receive new installation ACKs, and those ACKs are part
of product readiness. Extending the execution window alone cannot recover this
state.

The qualified recovery preserves the original Root, active ICA, key/CSR, PVC,
five permanent credentials, old session and every original plan/journal. It
creates a distinct bounded temporary session using the same key/CSR and a
separate certificate output path. Fresh native evidence must bind the old
expired session, exact subjects, successful unrevoked product issuances, original
owners, current issuer and the exact original signer policy before issuance. The
cleanup path rereads that policy before deletion. No product enrollment or issuer
ceremony repeats.
The new session receives ACKs from the same five authenticated permanent clients.
Its actual finalization, credential cleanup and consumer transition still precede
original execution closure.

The ACK session and storage placement have separate meanings. Rendering retains
the original enrollment session for the same-PVC producer and pod affinity while
the renewed session receives installation ACKs. Shared declared-state MQTT
validation verifies that original producer, exact affinity and claim ownership;
it does not infer a new storage location from the ACK session identifier. The
expiry recovery regression invokes this shared validator before successor
execution, while the original producer and renewed ACK session coexist, as well
as after the final steady transition.

Renewal cleanup is optional for ordinary and fresh product sessions. It selects
the exact scoped renewal state before requiring a normal-tail program. No renewal
state or history means the ordinary finalizer continues unchanged; recorded
native or journal renewal work with missing state, and present but incomplete
renewal state, cannot silently skip cleanup.

An already-started forward child stays immutable. At the supported checkpoint it
has no completed resource, recorded owner or uncertain in-flight write. A separate
immutable successor descriptor binds that exact original plan and journal, the
completed session recovery, and only the required ACK-session/cleanup changes.
The ordinary engine executes the successor with all phase, ownership, storage and
readiness checks. Parent receipts link the failed zero-write child to the accepted
successor; they never relabel the failed native child accepted. Unknown writes,
changed owners or an undeclared desired-state change cannot use this recovery.
Ownership, allocation, final-target and independent completion readers consume
the same authenticated chain.

After genuine new-session finalization, exact old temporary Secret and cleanup
ConfigMap removal uses the native PKI cleanup lifecycle. It records an intent
bound to the accepted original definition and UID before deleting with both UID
and current resource-version preconditions. A retry may record observed absence
only when that exact pending intent already exists. Absence without intent,
replacement objects, changed bodies and ambiguous read errors cannot prove
cleanup. The old signer policy follows its authenticated absence receipt. This
finite cleanup does not extend the ordinary planner's generic deletion kinds;
the original expired session remains historical and unfinalized.

After native product finalization and temporary producer retirement, the
remaining program can apply one finite correction through ordinary
`prepare`, `apply` and `retire` children. Its source pins the original completed
journal prefix, accepted workload, steady cleanup, retirement and finalization.
It cannot allocate a replacement identity or volume. The final compiler overlays
only accepted corrected definitions and retains their original resource UIDs.
A Controller-only image observation alias preserves the original release and
all unrelated business image bindings. Retries resume a partially applied child
before final target observation.

CertIssuer preparation preserves the original private key and both old profile
files on the existing PVC. Its authenticated native request claim distinguishes
a known accepted response from a pending or uncertain request. The resulting
public profile and original key are verified before changing the serving path.
LogIngester's explicit runtime group permits its normal atomic cache writer;
the operation does not chown the PVC or publish CRLs or ACKs on its behalf.
After rollout, a bounded five-minute read-only convergence loop reloads the
current runtime and verifies actual CRL caches against native registry ACKs.
The Device Root must have more than 24 hours remaining, proving that the
repaired worker renewed a near-expiry publication; other CRLs retain their
normal current-validity checks. Full environment acceptance remains unchanged.

All paths below are relative to the selected canonical environment store and
use 0700 directories and 0600 files.

| Path | Producer and consumer |
| --- | --- |
| `deployment/create/executions/<parent>/execution-*/normal-tail-program.json` | Complete qualification freezes the program and results; first admission and retries read it. It contains private rendered definitions. |
| `deployment/preflight/<program>/qualification-*/program.json` | Qualification publishes the identical private artifact; the shared active pointer and first admission bind its digest and generation. |
| Original execution `journal.json`, entry `normal-tail/program` | First admission records local qualification provenance and fixed start/deadline atomically; retries consume the original times. This is not a native completion receipt. |
| Original execution `normal-tail-program-window.json`, then `normal-tail-program-window-0002.json` and subsequent contiguous numbers | Explicit `create --total-timeout` supplements an existing admission after current scope/owner checks. Exclusive creation and sync bind the original Program, owner, qualification, admission digest and original times, checker provenance, predecessor raw-file hash and strictly larger total deadline measured from the original start. Shared runtime/history readers and the independent final verifier authenticate the complete chain and consume its latest bound; earlier records and the original journal receipt remain unchanged. Ordinary retries do not extend a window. |
| `deployment/repairs/<repair>/caller-window.json` | An explicitly approved remaining-duration launcher atomically publishes its complete private selection once before the first invocation, using an exclusive link so concurrent callers cannot reset the clock. Interruption before publication leaves no partial canonical selection. It records the proposal hash and fixed deadline with `is_program_admission=false`; retries validate and reuse the same bytes. It cannot replace the ordinary CLI program-window producer or native session recovery. Partial, changed or expired selections stop without resetting time. |
| Original execution `normal-tail-post-cleanup-correction-{source,prepared,completed}.json`, `normal-tail-stages/post-cleanup-runtime-correction-{prepare,apply,retire}.json` and ordinary `deployment/rollback/<child-plan>/` | The same-program correction producer freezes original history and exact recipes; native preparation records the public-profile proof; ordinary children record updates and helper retirement. Recovery, target observation, volume capture and independent final verification consume these records. Original profiles, journals and admission remain unchanged. |
| Original execution `normal-tail-target.json` and `normal-tail-stages/<stage>.json` | Executor saves the exact initial target/scoped input before parent stage admission; resumable children consume it. Initial target evidence remains unchanged after steady cleanup. |
| Original execution `normal-tail-stages/runtime-dependency-access.json`, parent `kubernetes/runtime-dependency-access` entry and ordinary `deployment/rollback/<child-plan>/` | The standard scoped executor records the finite maintained connectivity policies for an older admitted workload. Recovery, phase fences and final rendering consume their genuine child results and UIDs. |
| Original execution `normal-tail-workload-forward.json` and ordinary `deployment/rollback/<child-plan>/` | The correction producer preserves and binds the original Program, owner, target and failed journal, then saves the exact corrected target. Shared recovery, allocation, lease and final readers consume the genuine forward journal and composite logical-workload receipt. The original native plan and journal are never rewritten. |
| Original execution `normal-tail-product-session-forward.json` | The expired-session recovery producer binds the existing zero-write forward plan/journal, original Program/owner and genuine completed renewal to its exact successor target. Ordinary child execution produces the accepted result; semantic recovery, ownership, allocation and final-verification readers authenticate the explicit supersession chain. All earlier plans and journals remain unchanged. |
| Same repair directory: `dev-continuation-remaining120m-{proposal,operator-approval}.json`, `caller-window-remaining120.json`, `remaining_window_remaining120.py`, `continue-dev-billing-runtime-remaining120.py` and `check-full-health-remaining120.py` | The operator prepares the tested continuation and records only a genuine new duration approval. The launcher exclusively selects the new fixed remaining budget on its first actual invocation; retries and independent health consume the same selection. Its CLI total remains anchored to the original admission, while all prior caller selections and native windows stay unchanged. |
| `deployment/repairs/global-flow-<date>/billing-runtime-repair-validation{.prepared,}.json`, `billing-payment-choice.json` and `retained-helpers/dev-final-readonly-<sha>.py` | The operator repair producer records tested sources/checker, actual payment choice and retained verifier bytes. The continuation launcher consumes only the completed choice-bound repair and existing fixed window; historical verification consumes the retained helper. A prepared report has no execution authority. |
| Original execution `normal-tail-runtime-configuration-source.json` | The capture command saves the exact original private configuration, release and snapshot, bound to the stopped Billing plan/journal and original Program/owner. The correction producer and independent reader use it to reject unrelated input changes. It contains secrets and uses mode `0600`; it is never an admission or a replacement journal. |
| Original execution `normal-tail-workload-forward-0002.json`, `normal-tail-stages/workload-forward-0002.json` and ordinary `deployment/rollback/<child-plan>/` | The bounded Billing correction producer saves selected inputs and the exact successor. The ordinary executor records actual UIDs, readiness and acceptance. Semantic, lease, allocation, final-target and completion readers authenticate the original and intermediate histories plus this genuine result. |
| Original execution `normal-tail-runtime-configuration-source-<NNNN>.json`, `normal-tail-workload-forward-<NNNN>.json` and `normal-tail-stages/workload-forward-<NNNN>.json`, for sequences after `0002` | The existing runtime correction producer saves the preceding effective private snapshot and a versioned exact successor. Execution, semantic, allocation, lease and independent completion readers authenticate the contiguous predecessor chain; original snapshots and native journals remain unchanged. |
| `deployment/repairs/global-flow-<date>/time-extension-delegated-authorization.json`, tested repair manifests and `continue-dev-tested-repair.py` | The operator records explicit human delegation. A tested repair manifest binds code, evidence, fixed caller window and the original scope. The common launcher runs deployment or subsequent independent FULL health using that same clock. A necessary extension requires a separate truthful delegated decision and immutable window; retries cannot reset it. |
| `deployment/create/<parent>/bootstrap/product-identities/<scope>/service/session-renewal/{source,observed,completed,cleanup-source,cleanup-completed}.json` | The recovery qualifier freezes original expired-session evidence, key/CSR hashes, permanent output owners and distinct session intent. The native producer records the real renewed session and accepted scoped children; finalization records the original/new certificate hashes, genuine native finalization, exact old-owner conditional-deletion receipts and observed policy absence. Effective-session resolution, successor rendering, interrupted cleanup and final verification consume those records. Private key/CSR bytes remain on the original PVC. |
| Original bootstrap PVC: `renew-<binding>/bootstrap.crt`, `expired-<old-session>/bootstrap.crt`, canonical `bootstrap.crt` and `bootstrap.finalized.json` | The same-key native renewal writes its distinct public certificate. Only after genuine new-session finalization, the cleanup adapter preserves the old public certificate and places the renewed public certificate at the native cleanup path. Native cleanup alone records its tombstone and removes the exact key/CSR/canonical certificate. Retry validation consumes the preserved hashes and genuine tombstone; permanent product keys and claims are unrelated and retained. |
| Original execution `rejected/<raw-sha256>.json` | The pre-execution forward correction producer preserves the exact rejected descriptor before replacing its derived input. Recovery verifies the same bytes; audit readers retain this historical evidence. It cannot authorize execution or change original native history. |
| Original execution `stage-inputs/<stage>.json` | Fresh/shared create saves each exact scoped plan before admission, using exclusive creation; retries execute that original input and its child journal. |
| Original execution `normal-tail-stages/product-identities-<scope12>-{steady,producer-retire}.json` or `stage-inputs/fresh-product-{steady,producer-retire}.json` | Existing stage executors save ACK cleanup and exact six-owner retirement plans; recovery authenticates the same child journals under `deployment/rollback/<child-plan>/` before final rendering. These are ordinary children under the original parent and deadline, not additional admission authorities. |
| `deployment/create/<parent>/bootstrap/product-identities/<scope>/` | Native producer records original-Root policy, real issuer/session and output evidence; resolver, finalizer and recovery consume it. Original custody material remains at its original paths. |
| Same product directory: `service/session-forward.json` | Trust correction records the original failed child and Job/Pod, unchanged session intent, PVC/ServiceAccount owners, key/CSR hashes, observed current managed trust and accepted read-only custody child. The correction producer, semantic history reader and independent allocation verifier consume this immutable source. It contains no replacement session or private key; the distinct correction Job and custody retirement retain their ordinary child plans/journals. |
| Same product directory: `service/session-forward-attempt-<previous-plan-sha256>.json` | A definite pre-mint native failure records its prior child plan/journal, Job/Pod owners, terminal result, no-mint evidence and original source binding. The producer, semantic history reader and final allocation verifier follow this same-intent chain to a genuinely accepted winner. Failed native children stay failed; their parent receipts explicitly record supersession by that winner. Unknown outcomes do not create another attempt. |
| `deployment/create/<parent>/bootstrap/product-identities/fresh-program.json` | New-generation bootstrap freezes its original session's product producer intent; the renderer and deferred finalizer consume its parent/scope-bound reference. |
| `deployment/rollback/<child-plan>/` | Common execution engine produces actual child plans, native owners, write/readiness and acceptance journals; parent completion and allocation capture verify them. |
| `pki/public-https/<secret-name>/deployment/<producer-digest>/keys.json` | Admitted public HTTPS producer saves its original ACME account key, leaf key and CSR before issuance; interrupted issuance reuses them. Qualification never creates this file. A retained producer directory or certificate intent with missing keys requires restoration of the original bytes; an empty retained directory is not a fresh producer. |
| Same public HTTPS directory: `account.json`, `order.json` | Producer saves the original ACME account and first order response. Recovery preserves both and observes the same order while it remains usable. |
| Same public HTTPS directory: `attempts/<four-digit-number>/{intent,order}.json` | After an authenticated read proves the prior order terminal INVALID, the producer appends its exact terminal result and next request intent before requesting a new order. It saves the actual response separately; producer, semantic recovery reader and final verifier validate the contiguous chain and winning-order receipt. Unknown responses never permit another request. Account key, leaf key, CSR, names, Program and admission clock remain original. |
| Same public HTTPS directory or numbered attempt directory: `propagation.json` | The DNS waiter saves the complete order challenge-set binding, authoritative nameservers, convergence time and SOA negative-cache horizon. An observed authoritative negative answer durably invalidates the earlier horizon; renewed authoritative convergence extends this status monotonically before waiting. Interrupted validation rechecks current DNS without restarting a separate delay for each name. |
| Same public HTTPS directory: `material.json`, `completed.json` | Producer saves the verified real certificate/key pair and standard child publication's actual Secret UID; target resolution and final verification consume both. |
| Original execution `journal.json`, entries `public-tls/certificate` and `kubernetes/public-tls` | Native issuance records its original request and certificate hashes; the ordinary child engine independently records publication, readback and owner UID. The semantic remaining-flow reader authenticates both before resuming later stages. |
| `deployment/repairs/global-flow-<date>/checker-build.json` and `checker-build-<digest>.json` | Operator build records the immutable checker and production source hashes; the run wrapper verifies them before invoking the CLI. These are diagnostic build references, not GO or admission receipts. |
| `deployment/repairs/<repair>/future-program-qualified<file-count>-<source-prefix>/` | After original deployment closure and a new independent standard FULL health result, the operator can archive exact qualified source, test logs, prior source/checker and selected reports, integrate the qualified bytes, and retain a separate executable. `future-build.json` records `QUALIFIED_NOT_DEPLOYED`; `integration-result.json` records actual file application. Consumers distinguish this future source package from the unchanged checker selector and evidence of the completed deployment. No rebuild, repeated tests, native mutation or new deployment receipt is produced by exact integration. |
| Same repair directory: `remaining-execution-plan.json` and `dev-final-readonly.py` | Operator maintains the reviewable remaining sequence and its read-only final-verification tool; human review and preservation checks consume them without changing execution authority. |
| Same repair directory: `public-tls-before-recovery-hashes.json` | Operator read-only snapshot records hashes of original account/key/order files before recovery; final preservation verification compares those files. It is diagnostic evidence and grants no issuance or retry authority. |
| `deployment/reports/dev-global-flow-{preflight,deployment,post-health}-<UTC>.json` and `global-flow-<UTC>/{preflight,deployment,post-health}.log` | Each real CLI invocation produces its own result and output log; operator diagnosis and final verification select the exact invocation. |
| `deployment/reports/dev-global-flow-latest-{preflight,deployment,post-health}.json` and per-run `global-flow-<UTC>/run.json` | Operator wrapper records actual process start/end, exit status and its CLI report reference; the latest-run display never merges previous attempts. |
| `deployment/reports/dev-global-flow-latest-{preflight,deployment,post-health}-times.json` | Read-only timing helper projects the uniquely matched run-start and gate events from `deployment/logs/metrics-*.log`; human status display consumes this non-authoritative view. |
| Per-run `deployment/reports/global-flow-<UTC>/final-proof-*.json` | The operator captures the independent final reader’s actual closure, original identity, native volume and NEW FULL health results with explicit deployment/report paths. These read-only reports reference genuine execution evidence; they do not create receipts or authorize rollout. |
| Per-run `deployment/reports/global-flow-<UTC>/step-timings.json` | Operator projects the selected invocation’s metrics into step durations for status reporting. Initial preparation ends at the first real gate-start; nested intervals can overlap and must not be summed. The earlier supplemental-window `RecordedAt` is not its file-publication timestamp. |
| Per-run `deployment/reports/global-flow-<UTC>/history-read-sample*.txt` | Operator may capture a bounded local process stack sample to diagnose slow history reconstruction. Human performance review consumes this private diagnostic; shared output includes only relevant function names and timing, never credentials or process memory. It does not change qualification or native execution. |
| `deployment/reports/dev-flow-reorganization-<date>.md` | Operator records verified causes, repair and regression evidence for human review; it is not deployment completion evidence. |

Fresh product sessions and remaining-product sessions share the same enrollment,
mirror and native registration ACK mechanisms. Finalization follows installation;
the final acceptance target omits transition-only ACK settings. Historical
bootstrap plans, original CA material, existing runtime credentials and completed
migration receipts are not rewritten to match the new program.

The public HTTPS producer is shared by fresh create and completed-bootstrap
continuation. Its frozen consumer contract contains the exact ingress hosts and
Secret name. Existing owned TLS resources retain their original names and UIDs;
new names derive from the selected stack. Ingress terminates public HTTP TLS
behind the L4 edge; native mTLS passthrough endpoints keep their own identities.
DNS-01 challenge ownership remains in the DNS adapter's canonical runtime store,
and recovery removes only the exact challenge values recorded for each retained
order. All pending names are published before any CA validation is requested.
The shared waiter checks every authoritative nameserver and at least two of three
independent recursive resolver views, and accounts for the authoritative SOA negative-cache lifetime once
for the complete batch. Positive TXT TTL alone cannot expire cached NXDOMAIN.
Public issuance never replaces a Service or Device Root.

The 2026-10-19 phase-readiness repair was qualified against the complete ordinary engine (1,079 passing cases), 47 focused interruption/readiness cases, and a 397.84-second complete repeated recovery lifecycle through original closure and an independent reader. Exact source `2c1d8499390bd30f4a065d286a8a579d3dc897aefc8d0133078bd15e8a6afda8` and checker `03f8c3f7b354f2945a98e2dea08db26fc9db540a11836483c25547c27afd672e` are bound by canonical `deployment/repairs/global-flow-20261019/workload-phase-readiness-full-validation.json`. These isolated lifecycle tests do not substitute for the real deployment closure or the subsequent new standard FULL health check.

The preserved dev generation exposed a Linux metadata compatibility gap in the original core identity initializer: directories created beneath an SGID PVC root can have mode `2700`, while Go private-path validation compares `Mode().Perm()` to `0700` and the historical shell initializer compares the full `stat %a` output to `700`. The qualified recovery clears only the inherited setgid bit on the two exact `private` and `private/crl` directories after checking their original owner, nonsymlink paths and original file hashes. It preserves the PVC root and all file bytes, inode identities, owners, modes and link counts. The unchanged native initializer then completes. A pinned Linux image test covers actual inheritance and eight negative permission/link cases; mocked filesystem metadata is insufficient evidence for this contract.

Canonical recovery producers for this diagnosis are under `deployment/repairs/global-flow-20261019/` in the selected environment store: `core-identity-readonly-metadata-pod.json` and its result/log, `core-identity-sgid-linux-fixture.sh` with captured validation/log, and `core-identity-directory-repair-pod.json` with its script/result/log. These artifacts document one bounded generation-specific repair and must not be replayed against another volume or changed baseline. The diagnostic mounts the selected existing PVC read-only; the qualified repair changes only the two prevalidated directory bits, after which both temporary Pods are deleted. Consumers are the handover and exact hash-bound continuation evidence. The original deployment source, desired initializer, CA and identity material remain unchanged by this repair.


### Interrupted product registration recovery

The legacy Account Manager registration TLS Secret carries a PEM CRL bundle. Its client CA trust and server certificate can remain valid while the bundle omits a newly enrolled product issuer. Updating the product trust ConfigMaps alone does not update this separate consumer. Recovery validates genuine current CRLs for the exact frozen Root, original intermediate and installed product intermediate, then applies one ordinary same-UID Secret update through `account-registration-crls` before product readiness. Only `data["client.crl"]` changes. The server key, certificate, client CA and all permanent product outputs remain unchanged.

The producer writes `deployment/create/executions/<parent>/<execution>/normal-tail-account-registration-crls.json` in the selected environment store, with original program/owner and literal stopped plan/journal hashes. Its ordinary preparation plan is `normal-tail-stages/account-registration-crls.json`; the execution engine writes the corresponding plan, journal and accepted lease evidence under `deployment/rollback/<child-sha256>/`. The runtime successor embeds this proof in `normal-tail-runtime-configuration-source-0004.json`, binds its digest in `normal-tail-workload-forward-0004.json`, and retains the genuinely accepted new Secret baseline. The semantic reader and independent final verifier consume these records. Original runtime0003 is never rewritten.

A second temporary session renewal uses `deployment/create/<parent>/bootstrap/product-identities/<scope>/service/session-renewal-0002/`. Its source links the immutable first completed renewal, the literal stopped runtime0003 descriptor/plan/journal and the accepted credential/custody owners. It retains the original key/CSR, enrollment session and permanent outputs. The four ordinary renewal children produce the new Job, custody configuration, temporary credential and cleanup controls. The subsequent runtime0004 carries their accepted dependency union and changes only the five product ACK session references plus the separately qualified CRL correction. After genuine native finalization, cleanup preserves the public certificate history and retires both older temporary Secret/ConfigMap pairs with exact UID/resource-version preconditions and observed absence. It records both obsolete policy retirements. Original renewal files remain immutable.

These are recovery contracts for the preserved interrupted generation. Qualification must exercise the complete two-renewal, CRL preparation, workload, ACK, interrupted cleanup and original closure path before applying the candidate to the live environment. Fixture completion alone is not live deployment completion; a new standard FULL health report must follow actual closure.


For newly qualified programs, the product lifecycle freezes the exact Account Manager registration Secret and its owner in `SourceTrust`. After native product-intermediate activation, `prepareProductRegistrationCRLs` obtains and validates current public CRLs for the three frozen issuers and writes `deployment/create/<parent>/bootstrap/product-identities/<scope>/service/account-registration-crls.json`. The existing ordinary `successor-crls` preparation then updates that same Secret before any product enrollment. `bindProductRegistrationCRLs`, semantic recipe validation and installed-product restoration reload the immutable proof; an unexported in-memory binding keeps the serialized program and scope unchanged. Historical programs without this frozen Secret retain their original recipe. The interrupted historical generation therefore uses the separately qualified early recovery child described above. Missing proof after its accepted preparation is an error, not permission to regenerate identity or invent a receipt.

### Private directory SGID compatibility and immutable renewal recipes

Kubernetes volume ownership can leave the private directory mode at `2700`: the SGID bit is present while access remains owner-only. Maintained initialization and newly qualified renewal recipes accept only the explicitly supported `700` or `2700` directory modes, with their existing ownership, non-symlink and private-file checks. This does not relax private files from `0600` or their single-link and hash requirements. Unexpected sticky, setuid, group or world access remains invalid.

Frozen historical Job and initializer bodies remain valid exactly as recorded. New first-renewal sources select the optional `CertificateDirectoryProfile: private-directory-sgid-v1`; absent historical fields retain their original serialized source and Job recipe. Unknown selectors, and a first-generation selector on a second-generation source, are rejected. The existing second-generation recovery has its own fixed SGID-compatible recipe. The source producer and recipe validator must agree before any Job is accepted; the selected profile must never be retrofitted into a completed source.

The current interrupted generation uses qualified exact-source recovery and its original receipts. Separate new-program prevention changes are integrated only after that generation and its independent full health check finish, so an active execution cannot observe a changing checker source. The operational final-read-only helper bound to that original generation retains its selected historical source schema; it is not a generic verifier for arbitrary future parents.


### Recorded maintenance during an admitted interrupted deployment

A live controller can match a retained StatefulSet while its `OnDelete` Pod still
runs an older executable revision. The immutable retained plan cannot authorize
a Pod restart. A separately recorded, scoped maintenance operation may use the
existing conditional native restart and original-share unseal lifecycle to
converge that Pod to the already admitted controller template. It must validate
the original controller owner, exact target revision, literal plan and stopped
journal, original initialization receipt, and original PVC/PV/CSI identities.
It does not rewrite the StatefulSet, the plan or its child journal. Readiness and
a genuine unseal completion remain mandatory before the same plan resumes.

The operator produces helper source bindings, focused validation, read-only
preparation, execution intent/log/result and before/after preservation evidence
under `deployment/repairs/global-flow-<date>/openbao-pod-convergence-<timestamp>/`
in the selected environment store. The ordinary original execution journal
receives the genuine `openbao-managed-unseal-<pod-uid>` completion. Continuation
manifests and independent preservation checks consume these records; diagnostic
PASS is not deployment closure. The 2026-10-19 operation preserved three unique
OpenBao PVCs and their PV/CSI identities, plus the original initialization,
root-token and unseal-share bytes.

Routine same-Root CRL maintenance uses
`deployment/repairs/global-flow-<date>/service-root-crl-maintenance-<timestamp>-<source>/`
for the signed public candidate, original-custody hash bindings, native import
receipt, consumer ACK evidence and final result. The existing signer and native
importer retain the original Root identity and the complete revocation union.
Historical recovery receipts remain immutable. Dynamic consumer ACKs and current
freshness are checked separately from historical qualification. If an admitted
runtime plan freezes a static public CRL bundle, update that bundle after genuine
original closure and before the new independent FULL health check, preserving
the existing Secret UID and all private fields. The maintenance wrapper consumes
the actual successful deployment report before this final step.


### Historical CRL proof and current runtime validity

A completed, immutable recovery proof is verified at its authenticated recorded
time. Account Manager registration sources use `CapturedAt`; the completed
Service Root recovery reader uses the qualification artifact's `QualifiedAt`.
The signature, issuer lineage, source digest, owner, accepted child and native
completion receipts remain required. A later clock does not invalidate a past
successful publication or authorize replacement of its original proof.

Current operations keep current-time checks: fresh capture, unaccepted apply,
issuance, live consumer observations and full rollout acceptance must use a
currently valid signed CRL with the expected registry digest, number and ACKs.
The Service Root live-freshness helper performs this check explicitly after
reading historical provenance. Historical validity alone cannot authorize live
trust or a new write. Normal-tail recovery may therefore retain its admitted
static historical bundle while the original Root has a genuine newer dynamic
publication; scoped static-bundle maintenance follows original closure before
independent FULL health, as described above.

The historical/current split is qualified by the complete two-renewal recovery
fixture with a genuinely signed short-lived historical CRL, a same-Root newer
current CRL, and a distinct process reading the accepted history after natural
expiry. The test retains the ordinary target, five product ACKs, cleanup fault
recovery, final compiler and original closure. Synthetic native boundaries and
actual deployment evidence are recorded separately.
