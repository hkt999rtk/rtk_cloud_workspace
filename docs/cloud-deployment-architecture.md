# Multi-Environment Cloud Deployment Architecture

## Contract

RTK Cloud uses one reusable architecture description, many environment instances, and replaceable deployment adapters. Environments such as `dev`, `staging`, and `prod` select an architecture and adapter; they do not own copies of the architecture or adapter implementation.

The Kubernetes architecture owns workloads, namespaces, logical node classes, resource intent, capacity rules, placement, edge requirements, and TURN requirements. It uses `rtk.io/node-class` and standard Kubernetes topology labels. It must not contain cloud resource IDs or provider-specific labels.

An adapter maps that intent to a provider. The LKE adapter owns Linode regions and instance types, LKE clusters and pools, Block/Object Storage, external HAProxy and coturn VMs, DNS, quota, and kubeconfig acquisition. EKS and GKE are reserved contracts and fail before mutation until implemented.

## Resolution and lifecycle

Configuration is resolved in this order: architecture defaults, adapter defaults, environment identity/selection, environment architecture overrides, environment adapter overrides, and allow-listed explicit overrides. Cross-layer duplicate keys, unknown keys, invalid types, and provider keys in architecture config are errors.

```text
resolve -> validate -> plan -> ensure adapter infrastructure
        -> normalize kube access -> deploy shared Kubernetes workloads
        -> configure adapter edge/DNS -> acceptance -> evidence
```

The normalized runtime contract is `cloud_env/<environment>/runtime`. Shared commands never inspect adapter-private state. A resolved plan is sanitized and contains no credentials, kubeconfig content, private keys, tokens, or generated service secrets.

## Logical topology

The initial node classes are `general`, `broker`, and `database`. MQTT placement targets `broker`, PostgreSQL targets `general` unless an environment enables dedicated database capacity, and hard pod anti-affinity uses `kubernetes.io/hostname`.

Edge and TURN are architecture intent (`EDGE_REPLICAS`, `EDGE_MAX_CONNECTIONS`, `TURN_REPLICAS`, and relay port bounds). LKE currently realizes them with Linode VMs; another adapter may use a different implementation without changing the environment or load-test contract.

## Legacy mapping

| Legacy input | New owner |
| --- | --- |
| `cloud_env/staging/lke` | `cloud_env/staging/runtime` generated state |
| `LKE_TARGET_CONNECTS` | `CAPACITY_TARGET_CONNECTIONS` |
| `LKE_MQTT_CONNECTIONS_PER_POD` | `CAPACITY_CONNECTIONS_PER_MQTT_POD` |
| `LKE_MQTT_REPLICAS` | `MQTT_REPLICAS` |
| `LKE_VIDEO_CLOUD_REPLICAS` | `VIDEO_CLOUD_API_REPLICAS` |
| `LKE_*_REQUEST_CPU/MEMORY` | matching provider-neutral workload resource key |
| `LKE_EDGE_HAPROXY_COUNT/MAXCONN` | `EDGE_REPLICAS` / `EDGE_MAX_CONNECTIONS` |
| `LKE_COTURN_VM_COUNT` and relay ports | `TURN_REPLICAS` and `TURN_*_PORT` |
| LKE pool IDs, Linode types, quota | LKE adapter config or `runtime/adapters/lke` state |

The LKE adapter may translate the resolved contract into compatibility inputs while the existing renderer is decomposed, but compatibility keys are generated runtime material and are not architecture or environment source of truth.
