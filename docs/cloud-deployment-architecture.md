# Multi-Environment Cloud Deployment Architecture

## Contract

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

At the beginning of preflight, any previous GO bindings record is replaced with
NO-GO. Successfully compiled observations are saved privately, and only complete
qualification promotes the record to GO with its exact plan and bindings hashes.
A failed/fast check cannot leave old evidence eligible for execution. Plan alone
never publishes GO. Deployment re-observes the same environment through the same
compiler, requires the matching preflight record and approved plan SHA-256, and
fences the record plus live ownership at execution stages. Kubernetes resource
versions remain in the planner baseline for conditional writes, rather than being
invented for future resources. The generated file uses a 0700 parent directory
and 0600 permissions. `adopt-managed-state` is retired; operators must not
hand-produce or promote a candidate JSON to pass preflight.

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
Workload image versions use provider-neutral keys in
`cloud_deploy/architectures/kubernetes/architecture.env`: `POSTGRES_IMAGE`,
`INGRESS_CONTROLLER_IMAGE`, `OPENBAO_IMAGE`, `GRAFANA_IMAGE`, and optional catalog image settings.
Environment differences belong in `cloud_env/<environment>/overrides/architecture.env`.
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
