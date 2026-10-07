# Creating and Configuring an Environment

## Required configuration/data boundary

`cloud_env/` is configuration only. Maintained environment intent, overrides and
deterministic secret-free resolved configuration may live here. Deployment
outputs, including allocated provider state, receipts/proofs, tokens, keys,
certificates, kubeconfig, journals, reports and recovery/temporary data, must
live under `~/.config/rtk_cloud/<environment>/` (`0700`/`0600`). Do not choose
an alternate location from the current checkout, worktree or external disk.

Use the locations in [deployment secrets and state governance](../docs/deployment-secrets-governance.md#configuration-versus-deployment-generated-data).
Older runtime state locations below are legacy migration context, not authority
for new deployment outputs. Storage evidence now uses the canonical environment
directory; the complete provider/state migration remains part of the unified
deployment implementation. Missing state is NO-GO until explicitly recovered.

This is the operational entry point for adding `dev`, `staging`, `prod`, `qa`, or another deployment environment. See [`docs/environment-dns-naming.md`](../docs/environment-dns-naming.md) for environment-to-DNS mappings and endpoint naming rules, [`docs/cloud-deployment-architecture.md`](../docs/cloud-deployment-architecture.md) for architecture responsibilities and resolution rules, [`cloud_deploy/README.md`](../cloud_deploy/README.md) for shared defaults and adapter keys, [`docs/storage-credential-lifecycle.md`](../docs/storage-credential-lifecycle.md) for storage operations and credentials, and [`docs/object-storage-policy.md`](../docs/object-storage-policy.md) for canonical naming, namespaces and retention.

The existing [`staging-from-scratch.md`](../docs/staging-from-scratch.md) describes
the legacy fresh-create and acceptance procedure. Complete create/bootstrap
replacement is still in progress; that runbook is not qualification evidence
for the unified engine. For an existing cluster, restore its original private
`~/.config/rtk_cloud/<environment>/` store and use the explicit owner/state
migrations. Restoring checkout `runtime/` cannot restore deployment authority.

## Create the Minimal Configuration

The environment identity comes directly from its directory name under `cloud_env/`. Use lowercase alphanumeric characters and `-`. Do not copy another environment's `runtime/`.

Normal managed environments use `CLOUD_STACK_NAME=video-cloud-<environment>`.
Public endpoint names are derived from that stack and `CLOUD_DNS_ROOT_DOMAIN`
according to their endpoint role. The
[environment DNS naming source](../docs/environment-dns-naming.md) owns the
mapping tables, explicit hostname exceptions, and applicability to local or
qualified runtimes. Follow it when choosing a new environment's names or
reviewing an existing hostname override.

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

The LKE frontend deployment sets `SERVICE_LOGIN_URL` to
`https://<effective-console-host>/login`. By default, the Console host is the
selected environment's derived `CLOUD_ADMIN_DOMAIN`, so dev links to dev and
staging links to staging. A configured production `CONSOLE_DOMAIN` selects its
browser-facing Console host. For a custom login destination, set the absolute
URL in that environment's
`~/.config/rtk_cloud/<environment>/operator/env/SERVICE_LOGIN_URL` file (mode
`0600`). The deployment renderer honors this override. Do not rely on the
frontend application's standalone staging fallback in a deployed environment.

### Production website and Console names

Production may use short hostnames for the browser website, login, and Console.
In `cloud_env/prod/environment.env`, the website uses
`FRONTEND_DOMAIN=www.realtekconnect.com` together with
`PUBLIC_BASE_URL=https://www.realtekconnect.com`. To choose a short Console
hostname, set the optional prod-only `CONSOLE_DOMAIN`; for example, the
following placeholder represents a proposed choice within the production root
zone:

```env
CONSOLE_DOMAIN=<chosen-short-console-hostname>
```

The default frontend `SERVICE_LOGIN_URL` follows that effective Console host.
When `AUTH_TOKEN_BASE_URL` or `SOCIAL_LOGIN_CALLBACK_URL` is configured, align
it with the chosen Console origin and align the OAuth provider's registered
redirect URI with the callback URL. Backend service names follow the
[environment DNS naming source](../docs/environment-dns-naming.md).

### Architecture and adapter overrides

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

Container image versions are maintained in the shared architecture using
`POSTGRES_IMAGE`, `INGRESS_CONTROLLER_IMAGE` and `OPENBAO_IMAGE`. Use
`overrides/architecture.env` only for an explicit environment difference.
Do not place `LKE_*_IMAGE` keys in architecture files or adapter overrides;
the compiler supplies those internal renderer roles from the neutral settings.
Registry credentials remain in the selected private environment store.

An environment does not specify an LKE region, Linode type, or provider account quota. The adapter selects provider resources from the logical location and node-class resource minima. Resolved results are written only to ignored runtime evidence.

`overrides/adapter.env` is reserved for reviewed provider escape hatches; it is not standard environment configuration. Keep it empty for normal dev, staging, and prod environments. The operator-confirmed provider quota lives in the private environment store:

```env
# ~/.config/rtk_cloud/qa/deployment/adapters/lke/account.env
LKE_ACTIVE_SERVICE_LIMIT=20
```

The actual staging path is `~/.config/rtk_cloud/staging/deployment/adapters/lke/account.env`, with `0700` directories and a `0600` file. `20` is an example, not a discovered allowance. Set the active-service safety cap only from Linode account confirmation or a directly observed supported count; never guess or exceed that allowance. `unlimited` requires explicit account-owner confirmation. Live inventory and projected resource usage still have to be checked.

For an existing legacy environment, preserve its original state through the explicit migration operation:

```sh
./scripts/deploy-environment.sh provider-import-state \
  --environment staging \
  --source-runtime /absolute/original/cloud_env/staging/runtime \
  --confirm video-cloud-staging
```

The default original Kubernetes identity is the canonical environment kubeconfig. If it has not been recovered there, specify its exact original private path with `--source-kubeconfig`; no other worktree is searched. The operation verifies cluster/API/CA and recorded node-pool ownership, privately archives original provider bytes, preserves allocated IDs and the confirmed quota, and refuses conflicting destinations. It does not change cloud resources or create GO/completion evidence. Regenerate configuration after import to remove operational IDs and quota from resolved config. Retain the original source until the maintained code cutover is complete, then archive/retire the old operational files through the reviewed migration.

Cloud Admin batch-job recovery additionally requires its dedicated Account Manager job-authorization service credential in the environment SecretStore. It must not reuse login, Billing, Video Cloud, factory, or ownership-handoff credentials. Complete create/upgrade uses the shared deployment engine and exact release; retired full `provision`/`test` flows are not a fallback.

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

Each environment must also track `storage.env`, which declares the runtime media policy, bucket, and environment-owned prefix. It also declares whether OTA uses that legacy bucket or a dedicated OTA bucket. See [`docs/storage-credential-lifecycle.md`](../docs/storage-credential-lifecycle.md) for operational commands and credentials, [`docs/object-storage-policy.md`](../docs/object-storage-policy.md) for naming, object namespaces and retention, and [`docs/object-storage-inventory.md`](../docs/object-storage-inventory.md) for observed buckets.

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

Review the environment, logical location, minimum/effective replicas, aggregate requests for each node class, and effective count in the generic plan. Then review the resolved region and product in `runtime/adapters/lke/resolved-resources.env`. Normalized provider region/quota metadata lives in `~/.config/rtk_cloud/<environment>/deployment/provider-preflight.env`; it is not GO evidence, and consumers never fall back to `runtime/state/provider-preflight.env`. Perform mutation only after the plan is correct:

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

The intended isolated `test` lifecycle is `provision -> acceptance -> cleanup`.
Cleanup must still run after an attempted provision or failed acceptance, using
verified original resource ownership. Any resource that cannot be removed makes
the test fail. Complete create/bootstrap adapters and legacy test-output writers
are still being replaced; this entry currently cannot establish complete
environment qualification. New evidence must use private environment
`deployment/reports/` or `deployment/artifacts/`. Earlier `runtime/artifacts/`
paths are historical migration context only. Before public HTTPS DNS-01, the
operator host needs `certbot` and DNS credentials for the configured DNS root.

LKE mutation requires the selected environment operator's `LINODE_TOKEN`.
All local deployment-generated tokens, kubeconfigs, certificates, keys, service
and device credentials, captured databases, state and artifacts must live under
`~/.config/rtk_cloud/<environment>/`, including QA, with real `0700` directories
and regular `0600` files. Online runtime databases remain in their managed
persistent storage. `cloud_env/` contains configuration only; neither ignored
files nor an arbitrary operator directory are exceptions to this rule.

## Review checklist

- The environment directory name is clear and uses lowercase alphanumeric characters and `-`.
- Normal managed environments use `video-cloud-<environment>` as the stack name; explicit runtime or hostname exceptions follow the [environment DNS naming source](../docs/environment-dns-naming.md).
- `CLOUD_STACK_NAME` and DNS names do not collide with another environment.
- Only values that genuinely differ for this environment are overridden.
- Architecture overrides contain no `LKE_*`, `EKS_*`, or `GKE_*` keys.
- Adapter overrides for dev, staging, and prod contain no normal region, product, or quota settings.
- The verified provider account limit uses private `deployment/adapters/lke/account.env` and is not version-controlled.
- A DNS adapter is selected, and the environment contains no provider zone ID or DNS credentials.
- `deployment plan` succeeds and the resolved plan contains no secrets.
- `runtime/` contains only deterministic secret-free resolved configuration; deployment state uses the selected private environment store.
