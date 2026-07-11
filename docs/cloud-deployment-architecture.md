# Multi-Environment Cloud Deployment Architecture

## Contract

RTK Cloud uses one reusable architecture description, many environment instances, and replaceable deployment adapters. Environments such as `dev`, `staging`, and `prod` select an architecture and adapter; they do not own copies of the architecture or adapter implementation.

The Kubernetes architecture owns workloads, namespaces, logical node classes, resource intent, capacity rules, placement, edge requirements, and TURN requirements. It uses `rtk.io/node-class` and standard Kubernetes topology labels. It must not contain cloud resource IDs or provider-specific labels.

An environment declares a logical deployment location such as `us-west`. Each logical node class declares minimum vCPU and memory requirements. It never names a provider region or machine SKU. Persistent storage remains workload/storage intent and is not inferred from node sizing.

An adapter maps that intent to a provider. The LKE adapter owns Linode regions and instance types, LKE clusters and pools, Block/Object Storage, external HAProxy and coturn VMs, DNS, quota, and kubeconfig acquisition. EKS and GKE are reserved contracts and fail before mutation until implemented.

Adapter resolution is deterministic. LKE maps the logical location to an LKE region, filters its instance catalog by minimum vCPU and memory, then selects the candidate with the least memory surplus, least vCPU surplus, and finally lexicographically smallest type name. The generic plan contains only logical intent; provider region and SKU are adapter-private resolved evidence.

## Resolution and lifecycle

The directory name under `cloud_env/` is the environment identity. Configuration is resolved in this order: architecture defaults, adapter defaults, environment stack/selection, environment architecture overrides, environment adapter overrides, and allow-listed explicit overrides. Cross-layer duplicate keys, unknown keys, invalid types, and provider keys in architecture config are errors.

```text
resolve -> validate -> plan -> ensure adapter infrastructure
        -> normalize kube access -> deploy shared Kubernetes workloads
        -> configure adapter edge/DNS -> acceptance -> evidence
```

The normalized runtime contract is `cloud_env/<environment>/runtime`. Shared commands never inspect adapter-private state. A resolved plan is sanitized and contains no credentials, kubeconfig content, private keys, tokens, or generated service secrets.

Provider account quota is mutable operator/account state, not architecture, environment, or adapter default. If the provider API cannot return the active-service limit, LKE requires `runtime/adapters/lke/account.env` with `LKE_ACTIVE_SERVICE_LIMIT` before mutation. Missing account state fails before infrastructure changes.

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
| `cloud_env/staging/lke` | `cloud_env/staging/runtime` generated state |
| `LKE_TARGET_CONNECTS` | `CAPACITY_TARGET_CONNECTIONS` |
| `LKE_MQTT_CONNECTIONS_PER_POD` | `CAPACITY_CONNECTIONS_PER_MQTT_POD` |
| `LKE_MQTT_REPLICAS` | generated `MQTT_EFFECTIVE_REPLICAS` from `MQTT_MIN_REPLICAS` |
| `LKE_VIDEO_CLOUD_REPLICAS` | generated `VIDEO_CLOUD_API_EFFECTIVE_REPLICAS` from `VIDEO_CLOUD_API_MIN_REPLICAS` |
| `LKE_*_REQUEST_CPU/MEMORY` | matching provider-neutral workload resource key |
| `LKE_EDGE_HAPROXY_COUNT/MAXCONN` | `EDGE_REPLICAS` / `EDGE_MAX_CONNECTIONS` |
| `LKE_COTURN_VM_COUNT` and relay ports | `TURN_REPLICAS` and `TURN_*_PORT` |
| LKE pool IDs, Linode types, quota | LKE adapter config or `runtime/adapters/lke` state |
| `LKE_REGION` | `DEPLOYMENT_LOCATION` plus LKE location mapping |
| `LKE_*_NODE_TYPE` | `NODE_CLASS_*_MIN_VCPU/MIN_MEMORY_GIB` plus LKE instance catalog |
| `LINODE_ACTIVE_SERVICE_LIMIT` | ignored `runtime/adapters/lke/account.env:LKE_ACTIVE_SERVICE_LIMIT` |

The LKE adapter may translate the resolved contract into compatibility inputs while the existing renderer is decomposed, but compatibility keys are generated runtime material and are not architecture or environment source of truth.
