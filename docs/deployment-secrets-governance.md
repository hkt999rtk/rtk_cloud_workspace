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
```

V1 services:

```text
video-cloud
account-manager
admin
frontend
e2e
```

Production is currently Linode, so production secrets use
`.secrets/production/linode/<service>/`. If a future deployment uses AWS or GCP,
add a parallel provider directory such as `.secrets/production/aws/video-cloud/`
without changing the service directory shape.

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

## V1 Migration Order

1. Merge this governance document and read-only checks.
2. Create local ignored `.secrets/` directories for Linode staging and production.
3. Move operator-local Account Manager, Admin, Video Cloud, and E2E secret
   material into the new layout without committing the values.
4. Add service-specific PRs so deployment scripts support `DEPLOY_SECRETS_DIR`.
5. Rotate any values previously exposed in tracked files or logs.
