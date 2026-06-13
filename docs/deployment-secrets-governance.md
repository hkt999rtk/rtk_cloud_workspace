# Deployment Secrets Governance

Status: workspace source document for deployment secret layout and handling.

This document defines how RTK Cloud deployment secrets, keys, certificates,
runtime environment files, bootstrap tokens, and operator state are organized.
It is a governance document only; it must not contain secret values.

## Goals

- Keep deployment secrets out of git, PR descriptions, issue bodies, logs, and
  readiness reports.
- Separate staging and production secret material.
- Keep the current Linode deployment shape while preserving future AWS/GCP
  migration paths.
- Give deployment scripts a stable way to locate secrets without hard-coding
  service-specific local paths.
- Standardize production-like LKE deployments on OpenBao or an approved
  customer secret manager, with Kubernetes Secrets limited to synchronized
  runtime material.

## Canonical Local Secret Root

The canonical local secret root is:

```text
.secrets/<environment>/<provider>/<service>/
```

V1 environments:

```text
local
staging
production
```

V1 providers:

```text
local
linode
lke
```

V1 services:

```text
video-cloud
account-manager
admin
frontend
e2e
```

Production is currently Linode VM-based, so existing production secrets use
`.secrets/production/linode/<service>/`. The LKE migration uses
`.secrets/<environment>/lke/<service>/` only for bootstrap pointers, manifests,
public certificates, rollback material, and operator-local recovery references.
If a future deployment uses AWS or GCP, add a parallel provider directory such
as `.secrets/production/aws/video-cloud/` without changing the service directory
shape.

The `shared/` top-level tree is for operator-scope credentials that are not
owned by one deployable service. It is not a deployment environment.

## Standard Service Directory

Every service secret directory uses the same layout:

```text
.secrets/<environment>/<provider>/<service>/
  env/
  certs/
  public-certs/
  private-keys/
  tokens/
  state/
  manifest.json
```

| Directory | Purpose |
| --- | --- |
| `env/` | Runtime `.env` files and deploy-time environment files. |
| `certs/` | Certificate chains or client/server certificates. |
| `public-certs/` | Public CA or public certificate copies safe to distribute to verifiers. |
| `private-keys/` | Private keys; files should be mode `0600`. |
| `tokens/` | Bootstrap, admin, API, or short-lived operator tokens. |
| `state/` | Provider state such as Linode ids, IPs, firewall ids, and DNS state. |
| `manifest.json` | Non-secret metadata inventory for the directory. |

## Recommended Initial Tree

```text
.secrets/
  local/
    local/
      dev/
  staging/
    linode/
      video-cloud/
      account-manager/
      admin/
      frontend/
      e2e/
  production/
    linode/
      video-cloud/
      account-manager/
      admin/
      frontend/
      e2e/
  shared/
    linode/
    dns/
    ssh/
    github/
```

Shared directories hold operator-level material that is not owned by one
service, for example SSH keys, DNS API credentials, GitHub deploy credentials,
or Linode Object Storage credentials.

## LKE Secret Management Target

LKE deployments must not make Git, Helm values, Kustomize overlays, Docker
images, or CI logs the source of truth for runtime secrets. The target secret
boundary is:

- OpenBao or an approved customer secret manager stores long-lived runtime
  secrets, PKI state, policy, and audit logs.
- Workloads authenticate to OpenBao through Kubernetes auth or another reviewed
  workload identity method. AppRole remains a legacy VM bridge unless a specific
  transition runbook approves it.
- Kubernetes Secrets may hold short-lived synchronized material or bootstrap
  references needed by Pods, but those values must be generated or injected at
  deploy time and never committed.
- External Secrets-style sync, CSI secret injection, or init-container rendering
  are acceptable implementation options only after the LKE migration gates are
  approved.
- OpenBao root tokens, unseal keys, recovery keys, HSM PINs, production signing
  keys, and raw private key PEM values must never be committed, embedded in
  images, placed in public documentation, or stored in readiness artifacts.

TODO: confirm the LKE OpenBao storage backend, HA mode, seal/unseal process,
recovery key escrow, audit sink, policy naming, Kubernetes auth roles, and
backup/restore procedure before producing production manifests.

## Manifest Rules

Each `manifest.json` records metadata only. It must not contain raw secret
values, private key PEM blocks, bearer tokens, JWTs, passwords, DSNs with
passwords, or full environment file contents.

Required manifest fields:

- `environment`: one of `local`, `staging`, `production`, or `shared`
- `provider`: provider name, for example `linode`, `aws`, `gcp`, `github`, or `dns`
- `service`: service or shared category name
- `owner`: team or operator responsible for the directory
- `items`: inventory entries for secret-bearing files or directories

Each item should include:

- `id`: stable identifier
- `type`: `env_file`, `certificate`, `private_key`, `token`, `state`, or `pointer`
- `relative_path`: path relative to the service secret directory
- `deployed_to`: destination path or consumer, if applicable
- `contains`: human-readable categories, not values
- `rotation_required`: boolean
- `last_rotated`: ISO date or `unknown`

See `docs/examples/secrets-manifest.example.json`.

## Deployment Script Interface

Deployment scripts should accept an explicit secret directory:

```sh
DEPLOY_ENV=staging
DEPLOY_PROVIDER=linode
DEPLOY_SERVICE=account-manager
DEPLOY_SECRETS_DIR=.secrets/${DEPLOY_ENV}/${DEPLOY_PROVIDER}/${DEPLOY_SERVICE}
```

Follow-up service PRs may keep documented legacy path fallbacks temporarily, but
new docs and examples should prefer `DEPLOY_SECRETS_DIR`.

For LKE, `DEPLOY_SECRETS_DIR` is not the runtime secret source of truth. It may
hold operator-local bootstrap files, non-secret manifests, public certificates,
and rollback references while OpenBao or the approved secret manager owns live
runtime values.

## Artifact Boundary

`.artifacts/` remains test output only. It may contain temporary E2E run outputs,
redacted reports, generated fixtures, or local debugging material. It is not a
long-term deployment secret store.

Long-lived deploy secrets belong under `.secrets/`. E2E fixture secrets that are
intentionally reused across runs should be represented in
`.secrets/<environment>/<provider>/e2e/` or documented by pointer from that
manifest.

## Security Rules

- Do not commit `.secrets/`, `.secrets.backup/`, private keys, raw env files,
  bearer tokens, JWTs, or password-bearing DSNs.
- Do not paste secret values into PRs, issues, docs, terminal summaries, or test
  reports.
- Use file mode `0600` for private keys, raw env files, and token files.
- Production manifests must not reference staging domains, staging token files,
  staging certsets, or test-only device material.
- Any secret that appears in tracked files, PR bodies, issue bodies, shared logs,
  or generated reports should be treated as compromised and rotated.
- Kubernetes manifests, Helm values, and CI/CD deployment pipelines must not be
  produced until the LKE secret-management gate in
  `docs/lke-migration-inventory.md` is complete and human-approved.

## V1 Migration Order

1. Merge this governance document and read-only checks.
2. Create local ignored `.secrets/` directories for Linode VM staging and
   production.
3. Move operator-local Account Manager, Admin, Video Cloud, and E2E secret
   material into the new layout without committing the values.
4. Add service-specific PRs so deployment scripts support `DEPLOY_SECRETS_DIR`
   for the legacy VM bridge.
5. Complete the LKE secret-management gate before writing production
   Kubernetes manifests, Helm values, or CI/CD deployment pipelines.
6. Stand up or select the OpenBao/customer secret manager target with audited
   backup and restore.
7. Rotate any values previously exposed in tracked files or logs.
