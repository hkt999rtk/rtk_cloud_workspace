# Cloud Environment and Deployment Directory Layout

RTK Cloud deployment has four independent layers:

1. `cloud_deploy/architectures/` stores provider- and environment-neutral workload
   and topology intent.
2. `cloud_deploy/adapters/` stores deployment-adapter contracts for LKE, EKS, GKE,
   and similar providers.
3. `cloud_deploy/dns_adapters/` stores independent DNS-adapter contracts for
   GoDaddy, Route53, and similar providers.
4. `cloud_env/<environment>/` stores environment instances such as dev, staging,
   and prod, plus their local runtime.

An architecture does not belong to staging, and LKE is not a child directory of
staging. Any environment may select any adapter compatible with its architecture.

[`cloud_env/README.md`](../cloud_env/README.md) is the single operational entry
point for required fields, override examples, secret boundaries, and validation
steps when adding an environment. This document defines only directories and
responsibility boundaries, preventing operational procedures from drifting across
multiple documents.

## Version-Controlled Directories

```text
cloud_deploy/
  architectures/kubernetes/
    architecture.env
    capacity.env
    topology.env
    workloads.env
  adapters/
    lke/{defaults.env,schema.env}
    eks/schema.env
    gke/schema.env
  dns_adapters/
    godaddy/{defaults.env,schema.env}
    route53/{defaults.env,schema.env}

cloud_env/<environment>/
  environment.env
  deployment.env
  overrides/
    architecture.env
    adapter.env
    dns.env
```

The directory name under `cloud_env/` is the environment identity.
`environment.env` defines the stack, DNS root, and logical deployment location.
`deployment.env` selects the architecture, deployment adapter, and independent
DNS adapter. `overrides/architecture.env` may override only provider-neutral keys;
`overrides/adapter.env` and `overrides/dns.env` are reserved for explicit provider
escape hatches.

## Local Runtime

All of `cloud_env/<environment>/runtime/` is Git-ignored:

```text
runtime/
  resolved/{deployment.env,deployment-plan.json}
  state/{kubeconfig.yaml,topology.json}
  adapters/<adapter>/{state.env,resources.json}
  dns/<dns-adapter>/state.json
  services/
  secrets/
  devices/
  artifacts/
  backups/
```

Shared Kubernetes runtime and load tests read only normalized `runtime/state`,
`runtime/services`, `runtime/devices`, and `runtime/artifacts`. Provider state such
as cluster IDs, node-pool IDs, and Linode resource IDs may exist only under
`runtime/adapters/lke/`.

The generic DNS plan is `runtime/resolved/dns-plan.json`; normalized DNS state is
`runtime/state/dns.env`. DNS-provider state such as hosted-zone IDs and change IDs
may exist only under `runtime/dns/<dns-adapter>/`. Shared runtime and load tests
must not read DNS-provider-private state directly.

LKE account limits are in ignored `runtime/adapters/lke/account.env`. Adapter
resolution writes `runtime/adapters/lke/resolved-resources.env`; normalized
`runtime/state/provider-preflight.env` exposes only the provider region and active-
service limit required by load tests. Shared runtime and load tests must not read
adapter-private files directly.

## Configuration Resolution Order

The resolver combines architecture defaults, adapter defaults, environment
identity/selection, environment overrides, and explicitly allowed CLI/process
overrides in that order. Adapter keys must not override architecture keys; an
architecture must not contain `LKE_*`, `EKS_*`, or `GKE_*`.

The production operator interface uses the environment name:

```sh
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment provision --environment staging --confirm video-cloud-staging
go run ./scripts/go/rtk-cloud -- deployment acceptance --environment staging
```

Legacy `cloud_env/staging/lke` is neither moved nor deleted, but is no longer an
active input. The new runtime is generated from scratch; commands receiving a
legacy path must fail fast.

An empty runtime may be paired only with a new cluster and storage. If an adapter
finds an existing LKE cluster with the same name while the new environment lacks
OpenBao operator state or PostgreSQL secret state, provisioning must fail before
mutation. The operator must explicitly restore that environment state or rebuild
the cluster and persistent storage using an approved destructive runbook. New
secrets must never be mixed with old PVCs.
