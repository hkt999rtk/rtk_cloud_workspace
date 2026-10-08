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
one exact plan, while deployment completion requires separate runtime acceptance.
The [execution gate](deployment-readiness-standard.md#gate-3-deploymentexecution--ability-to-execute-the-plan)
uses native admission concurrency of at most eight and the existing four-worker
RBAC review limit. Progress identifies execution/PKI substeps and counts finished
admission checks; these counts are not PASS evidence. Parallel qualification
preserves fresh ownership reads and leaves actual deployment writes in plan order.

### Preflight bucket preparation

For `RUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED=false`, preflight verifies the
configured media bucket's existence, region and endpoint. Only a successful
complete inventory proving absence permits creation of that exact name/region;
fresh inventory must confirm creation. Failed inventory, permission, creation or
readback is NO-GO. This bounded provisioning step does not migrate objects,
replace credentials or create cutover receipts. Other provider probes remain
read-only. Explicit cutover configurations retain their separate lifecycle gates.

### Local configuration and deployment state ownership

`cloud_env/` contains configuration only. All local deployment-generated data,
whether sensitive or redacted, belongs in `~/.config/rtk_cloud/<environment>/`:
tokens, keys, certificates, kubeconfig, allocated provider state, receipts and
proofs, journals/locks, reports, rollback/recovery and temporary deployment data.
Use private `0700` directories and `0600` files. Every renderer, preflight,
executor and recovery operation uses the same explicit environment resolver;
none may invent a location from CWD, a worktree, external disk or volume format.

See the [canonical path table and migration boundary](deployment-secrets-governance.md#configuration-versus-deployment-generated-data).
Reviewed registration-listener CRL synchronization archives only the original
public CRL and sanitized before/after evidence under
`deployment/repairs/service-registration-crl-<YYYYMMDD>/` in that environment
store. The source is the selected issuer's current published registry CRL;
validate its signature, issuer binding, validity, increasing number and retained
revocations. Update only the original registration Secret's `client.crl` using
UID/resourceVersion guards. Account Manager consumes the projected file and
rereads it for each handshake/request. Preflight checks the live material, not
an archived repair receipt. Registry publication and mounted Secret rotation
are separate steps; the archive is not authority to restore an expired CRL.

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

After invocation, configuration, report-path and recovery-guard validation,
preflight replaces any previous GO bindings record with NO-GO before preparation
or compilation. Successfully compiled observations are saved privately, and only complete
qualification promotes the record to GO with its exact plan and bindings hashes.
A failed/fast check cannot leave old evidence eligible for execution. Plan alone
never publishes GO. Deployment re-observes the same environment through the same
compiler, requires the matching preflight record and approved plan SHA-256, and
fences the record plus live ownership at execution stages. Kubernetes resource
versions remain in the planner baseline for conditional writes, rather than being
invented for future resources. The generated file uses a 0700 parent directory
and 0600 permissions. `adopt-managed-state` is retired; operators must not
hand-produce or promote a candidate JSON to pass preflight.

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
The shared preflight/deployment compiler projects them into container settings:

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

An existing `video-cloud-api-app-pki` uses the common API renderer with its App
role, formal CI API image for both the main container and
`initialize-app-trust-state`, and retained TLS, App trust, management Secret and
trust-state PVC references. The initializer copies the initial root policy only
when no state file exists, retaining initialized state on retries. Observation
records references and ownership, never Secret payloads or an arbitrary workload
specification. Missing references or an unknown initializer remain NO-GO.

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
using the same compiler/path helpers for preflight and deployment. No new local
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
one maintained target and qualify the exact execution plan. Existing resources
are ownership/identity/concurrency evidence, not desired configuration templates.
An environment-local schema-1 snapshot must not select a different renderer.
The replacement is in progress; legacy prerequisite PASS does not establish
complete-environment GO under the accepted standard.

The directory name under `cloud_env/` is the environment identity. Configuration is resolved in this order: architecture defaults, adapter defaults, environment stack/selection, environment architecture overrides, environment adapter overrides, and allow-listed explicit overrides. Cross-layer duplicate keys, unknown keys, invalid types, and provider keys in architecture config are errors.

The [environment DNS naming source](environment-dns-naming.md) defines how the
environment identity, stack name, DNS root, and endpoint role determine public
names, including registered hostname exceptions and runtime applicability.
Normal managed environments use `video-cloud-<environment>` as their stack.
Shared orchestration resolves that hostname intent before the selected DNS
adapter applies records; changing a deployment or DNS provider does not change
the naming policy.

```text
resolve -> immutable release -> complete versioned plan -> qualification
        -> infrastructure stage -> Kubernetes admission/bootstrap stage
        -> fenced workload/storage/network/route stages
        -> target health and business acceptance -> completion evidence
```

The normalized runtime contract is `cloud_env/<environment>/runtime`. Shared commands never inspect adapter-private state. A resolved plan is sanitized and contains no credentials, kubeconfig content, private keys, tokens, or generated service secrets.

Provider account quota is mutable operator/account state, not architecture, environment, or adapter default. If the provider API cannot return the active-service limit, LKE requires `~/.config/rtk_cloud/<environment>/deployment/adapters/lke/account.env` with `LKE_ACTIVE_SERVICE_LIMIT` before mutation. Missing account state fails before explicit infrastructure changes. It is not an
existing Kubernetes workload upgrade prerequisite. Cluster creation records its
provider ID after allocation; pre-create checks cannot require a future cluster ID.

## Logical topology

The initial node classes are `general`, `broker`, and `database`. MQTT placement targets `broker`, PostgreSQL targets `general` unless an environment enables dedicated database capacity, and hard pod anti-affinity uses `kubernetes.io/hostname`.

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

Dev retains a managed TLS installer/sidecar, shared private runtime files and a
separate 10Gi PKI identity claim. Staging currently projects TLS from a Secret
and has no PKI sidecar. Do not invent a single current TLS mode, copy Dev identity
references into Staging, or remove Dev's PKI installer to match the existing
Raft renderer. Names/UIDs, keys, certificates and private recovery evidence
remain in the selected environment's lifecycle bindings and runtime stores.

## Complete update validity window

The maintained `DEPLOYMENT_UPGRADE_WINDOW_SECONDS` setting bounds a complete
update and acceptance and selects its certificate validity margin. The shared
Kubernetes baseline is 1800 seconds; each environment may explicitly override
it through `overrides/architecture.env`. The selected value participates in
configuration/plan hashing, native PKI qualification and execution deadlines.
A required missing or invalid value is NO-GO; runtime code must not invent a
window or extend an approved deadline. See the deployment readiness standard
for the remaining required trust-consumer and recovery gates.

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

The experimental PRECHECK path is removed. Formal CI image resolution again
precedes environment inventory/target compilation. Standard progress stages and
10-second elapsed-time heartbeats remain; only full qualification can report GO.

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
references are observed and retained through the same compiler for preflight
and deployment. They remain generated bindings in the canonical private
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
existing private CRL/identity directories and never rewrites their contents.
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
