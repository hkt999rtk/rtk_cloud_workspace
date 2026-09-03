# Creating and Configuring an Environment

This is the operational entry point for adding `dev`, `staging`, `prod`, `qa`, or another deployment environment. See [`docs/cloud-deployment-architecture.md`](../docs/cloud-deployment-architecture.md) for architecture responsibilities and resolution rules, [`cloud_deploy/README.md`](../cloud_deploy/README.md) for shared defaults and adapter keys, and [`docs/storage-credential-lifecycle.md`](../docs/storage-credential-lifecycle.md) for multi-environment Object Storage and credential lifecycles.

To build LKE staging from a fresh clone, complete service acceptance, and run the 1K MQTT/Device Shadow test, follow [`staging-from-scratch.md`](../docs/staging-from-scratch.md). Do not use that procedure for an existing cluster; safely restore the existing environment's ignored `runtime/` first.

## Create the Minimal Configuration

The environment identity comes directly from its directory name under `cloud_env/`. Use lowercase alphanumeric characters and `-`. Do not copy another environment's `runtime/`.

```sh
environment=qa
mkdir -p "cloud_env/$environment/overrides"
```

Create `cloud_env/qa/environment.env`:

```env
CLOUD_STACK_NAME=video-cloud-qa
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
DEPLOYMENT_LOCATION=us-west
```

| Key | Required | Purpose |
| --- | --- | --- |
| `CLOUD_STACK_NAME` | Yes | Unique stack name used by the cluster, namespace, DNS, and destructive confirmation. |
| `CLOUD_DNS_ROOT_DOMAIN` | Yes | Root domain for public service hostnames. |
| `DEPLOYMENT_LOCATION` | Yes | Provider-neutral logical location; the adapter maps it to a provider region. |

Create `cloud_env/qa/deployment.env`:

```env
DEPLOYMENT_ARCHITECTURE=kubernetes
DEPLOYMENT_ADAPTER=lke
DNS_ADAPTER=godaddy
```

| Key | Required | Purpose |
| --- | --- | --- |
| `DEPLOYMENT_ARCHITECTURE` | Yes | Selects the shared architecture; currently `kubernetes`. |
| `DEPLOYMENT_ADAPTER` | Yes | Selects the provider adapter. Only `lke` currently supports mutation; `eks` and `gke` validate and then fail fast. |
| `DNS_ADAPTER` | Yes | Selects the DNS provider independently; both `godaddy` and `route53` support mutation. |

## Optional Overrides

Do not create an override file when there are no differences. To adjust workload, capacity, or topology, place existing keys from [`cloud_deploy/architectures/kubernetes/`](../cloud_deploy/architectures/kubernetes/) in `overrides/architecture.env`:

```env
CAPACITY_TARGET_CONNECTIONS=1000
CAPACITY_ACTIVE_DEVICES=1000
MQTT_MIN_REPLICAS=2
VIDEO_CLOUD_API_MIN_REPLICAS=2
NODE_CLASS_BROKER_MIN_COUNT=2
NODE_CLASS_BROKER_MIN_VCPU=4
NODE_CLASS_BROKER_MIN_MEMORY_GIB=8
```

An environment does not specify an LKE region, Linode type, or provider account quota. The adapter selects provider resources from the logical location and node-class resource minima. Resolved results are written only to ignored runtime evidence.

`overrides/adapter.env` is reserved for reviewed provider escape hatches; it is not standard environment configuration. Keep it empty for normal dev, staging, and prod environments. The operator stores the provider account limit in ignored runtime:

```env
# cloud_env/qa/runtime/adapters/lke/account.env
LKE_ACTIVE_SERVICE_LIMIT=20
```

The actual staging file path is:

```text
cloud_env/staging/runtime/adapters/lke/account.env
```

`LKE_ACTIVE_SERVICE_LIMIT` is the active-service limit allowed for the Linode account. It is neither another API secret nor architecture/default configuration. The Linode API does not currently expose an endpoint that can query this limit with `LINODE_TOKEN`, so the operator must set it manually from Linode's account confirmation. Before creating any billable resource, deployment compares `current active services + planned resources` with this limit. Stop if the value is unknown; do not guess.

Cloud Admin batch-job recovery additionally requires a dedicated Account Manager
job-authorization service credential in the environment SecretStore. It must not
reuse login, Billing, Video Cloud, factory, or ownership-handoff credentials and
must never be written to tracked environment files. Persistent `video-cloud-dev`
qualification uses in-place `provision` followed by `acceptance`; `test` remains a
destructive ephemeral flow that cleans up its owned stack.

Create the staging account state:

```sh
mkdir -p cloud_env/staging/runtime/adapters/lke
cp cloud_env/staging/runtime/adapters/lke/account.env.example \
  cloud_env/staging/runtime/adapters/lke/account.env

chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

You may also edit `account.env` directly and replace the example value `20` with the actual limit confirmed by Linode.

If another workspace with a completed staging setup already has this file, you may safely copy its operator state. Do not commit it:

```sh
cp /path/to/known-good-workspace/cloud_env/staging/runtime/adapters/lke/account.env \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

`20` is only an example of a confirmed account limit. Replace it with the actual limit for your Linode account. The file is under the Git-ignored `runtime/` directory and is not obtained through `git clone`.

Architecture overrides must not contain provider keys, and adapter overrides must not contain workload, capacity, or topology keys. Unknown keys, invalid types, and cross-layer keys fail during planning.

### Certificate algorithm policy

Kubernetes environments resolve three provider-neutral certificate settings from
the architecture layer. An environment may pin them in
`overrides/architecture.env` when its policy must remain explicit:

```env
CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM=ed25519
CERTIFICATE_APP_CSR_KEY_ALGORITHMS=ed25519,p256
CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS=ed25519,p256
```

The internal TLS setting controls new certissuer service, OpenBao TLS, and MQTT
server keys. Changing it rotates those environment-owned TLS trust sets during
the next provision. The app and device settings are ordered CSR preferences;
they affect only newly generated credentials and do not revoke existing client
certificates. Supported canonical values are `ed25519` and `p256`; aliases,
empty lists, and duplicate list entries fail deployment preflight.

OpenBao's staging device/app issuer CAs may use RSA while issuing certificates
for Ed25519 or P-256 subject keys. These settings do not control public ACME
certificates, JWT/EdDSA token signing, OTA signing, SSH keys, or PKCS#11 signer
selection.

Each environment must also track `storage.env`, which declares the runtime media policy, bucket, and environment-owned prefix. See [`docs/storage-credential-lifecycle.md`](../docs/storage-credential-lifecycle.md) for examples and the lifecycle.

Use `overrides/dns.env` for optional DNS-provider escape hatches. A normal environment does not set a hosted-zone ID, API endpoint, AWS access key, or GoDaddy key. GoDaddy credentials are read only from `~/.config/rtk_cloud/<environment>/operator/env/`; Route53 credentials must be stored in the same environment store. See [`docs/secret-store.md`](../docs/secret-store.md) and [`docs/dns-adapter-architecture.md`](../docs/dns-adapter-architecture.md) for details.

## Validate and Provision

First confirm that tracked configuration is not ignored by Git. Then run preflight, which neither writes runtime state nor modifies cloud resources:

```sh
git check-ignore "cloud_env/qa/environment.env" || true
go run ./scripts/go/rtk-cloud -- deployment preflight \
  --environment qa \
  --operation provision
go run ./scripts/go/rtk-cloud -- deployment plan --environment qa
```

Review the environment, logical location, minimum/effective replicas, aggregate requests for each node class, and effective count in the generic plan. Then review the resolved region and product in adapter-private `runtime/adapters/lke/resolved-resources.env`. Load tests use only normalized `runtime/state/provider-preflight.env`; they do not read adapter-private state. Perform mutation only after the plan is correct:

```sh
go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment qa \
  --confirm video-cloud-qa
```

## Unified Environment Test Lifecycle

All environments use the same script and argument format. Do not copy the deployment script for dev, staging, or prod:

```sh
scripts/deploy-environment.sh plan --environment dev
scripts/deploy-environment.sh test --environment dev --confirm video-cloud-dev
scripts/deploy-environment.sh test --environment staging --confirm video-cloud-staging
scripts/deploy-environment.sh test --environment prod --confirm video-cloud-prod
```

`test` always runs `provision -> acceptance -> cleanup`. Cleanup still runs if provisioning or acceptance fails. Based only on that environment's stack ownership, cleanup removes DNS, LKE, VMs, firewalls, VPCs, and empty environment-owned Object Storage buckets. Sanitized test evidence remains in ignored `runtime/artifacts/`. If any provider resource cannot be removed, the entire test must report failure and the environment must not be considered clean. Before running public HTTPS DNS-01, the operator host must have `certbot` installed and the GoDaddy credential must be able to read and write DNS records for `CLOUD_DNS_ROOT_DOMAIN`.

LKE mutation requires the operator to provide `LINODE_TOKEN`. Tokens, kubeconfigs, certificates, service secrets, device credentials, SQLite databases, state, and artifacts may exist only in `cloud_env/qa/runtime/` or an operator secret source. They must not be written to the tracked `.env` files above or committed.

## Review checklist

- The environment directory name is clear and uses lowercase alphanumeric characters and `-`.
- `CLOUD_STACK_NAME` and DNS names do not collide with another environment.
- Only values that genuinely differ for this environment are overridden.
- Architecture overrides contain no `LKE_*`, `EKS_*`, or `GKE_*` keys.
- Adapter overrides for dev, staging, and prod contain no normal region, product, or quota settings.
- The provider account limit exists only in ignored adapter runtime and is not version-controlled.
- A DNS adapter is selected, and the environment contains no provider zone ID or DNS credentials.
- `deployment plan` succeeds and the resolved plan contains no secrets.
- `runtime/` remains ignored and contains no state copied from another environment.
