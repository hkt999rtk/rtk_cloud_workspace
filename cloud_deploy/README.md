# Architecture Defaults and Deployment Adapters

This directory stores deployment contracts shared by all environments. It must not contain environment identity, runtime state, or secrets.

- `architectures/kubernetes/`: workloads, capacity, logical node classes, minimum vCPU and memory for each node class, placement, edge, and TURN intent.
- `adapters/lke/`: logical-location-to-LKE-region mappings, the Linode instance catalog, and provider implementation defaults. It must not contain account quotas.
- `adapters/eks/` and `adapters/gke/`: adapter contracts; mutation is not currently supported.
- `dns_adapters/godaddy/` and `dns_adapters/route53/`: DNS provider defaults and schemas that can be selected independently of the deployment adapter.

When adding an environment, do not copy or modify these defaults. Create the environment according to [`cloud_env/README.md`](../cloud_env/README.md), and put only environment-specific differences in its `overrides/` directory.

Modify this directory only when the shared architecture or provider mappings must change for every environment. Architecture files must not contain provider-specific keys, and adapters must not override architecture keys. Persistent storage capacity is workload and storage intent, not node-product sizing.

The shared capacity planner first calculates effective Pod replicas from the workload registry. It then calculates the effective node count from each logical node class's planning shape, system reserve, aggregate requests, and spread floor. An adapter may only select provider products that satisfy the planning shape; it must not recalculate replicas or node counts.

The DNS adapter is independent of the deployment adapter. Shared DNS orchestration handles only hostnames, record intent, convergence, and the ACME DNS-01 lifecycle. GoDaddy or Route53 credentials, zone discovery, API mutation, and provider IDs may exist only in the DNS adapter or ignored runtime. See [`docs/dns-adapter-architecture.md`](../docs/dns-adapter-architecture.md) for the complete contract.
