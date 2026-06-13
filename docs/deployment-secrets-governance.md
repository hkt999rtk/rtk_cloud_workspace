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
- Standardize staging and production on OpenBao as the runtime secret source of
  truth, while retaining local files only for bootstrap, local development, and
  rollback.

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

## OpenBao Source Of Truth

OpenBao is the target secret manager for staging and production. The local
`.secrets/<environment>/<provider>/<service>/` tree remains the operator
bootstrap and rollback interface, but long-lived runtime secrets should be
stored in OpenBao after migration.

V1 OpenBao responsibilities:

- `kv-v2` stores runtime secrets such as DSNs, JWT/token keys, MQTT
  credentials, TURN shared secrets, object-storage credentials, factory
  enrollment HMAC keys, and service bootstrap pointers.
- `pki` signs device/factory, gateway/server, and app/user CSRs. Existing
  service-side CSR validation, request idempotency, and audit tables remain in
  `rtk_video_cloud`.
- OpenBao audit logging records secret reads and PKI signing operations. RTK
  services still record domain audit events such as certificate issuance
  request id, CSR hash, subject, serial, entitlement evidence, and outcome.

V1 OpenBao mounts:

```text
secret/rtk-cloud/<environment>/<service>/...
pki/device
pki/app
pki/gateway
```

The `secret/` paths are examples. Deployments may choose a different mount, but
the service-level manifest must record the actual mount and path prefix.

Recommended `kv-v2` layout:

```text
secret/rtk-cloud/staging/video-cloud/api
secret/rtk-cloud/staging/video-cloud/certissuer
secret/rtk-cloud/staging/video-cloud/factoryenroll
secret/rtk-cloud/staging/video-cloud/mqtt
secret/rtk-cloud/staging/video-cloud/turn
secret/rtk-cloud/staging/account-manager/api
secret/rtk-cloud/staging/shared/dns
secret/rtk-cloud/staging/shared/object-storage
```

OpenBao policies must be least-privilege:

- `video-cloud-certissuer` may sign only approved PKI roles and read only the
  runtime values needed by `cmd/certissuer`.
- `video-cloud-env-renderer` may read only the KV paths needed to render the
  target host's systemd environment files.
- `factoryenroll` may not sign certificates directly in OpenBao; it continues
  to call `cmd/certissuer` over the existing issuer boundary.
- Operators may write or rotate secrets through explicit administrative policy;
  service roles must not have write access to production runtime secrets.

For current Linode/systemd deployments, service authentication to OpenBao uses
AppRole:

```text
/etc/video_cloud/openbao/role_id
/etc/video_cloud/openbao/secret_id
```

Both files must be root-owned, mode `0600`, and excluded from readiness
reports. Kubernetes auth is reserved for a future Kubernetes deployment path.

Runtime services should continue to consume env files initially. A deployment
render step reads OpenBao KV entries and writes root-owned files under
`/run/video_cloud/*.env` or another tmpfs runtime directory before systemd
starts the service. This preserves the current process config boundary while
moving secret ownership to OpenBao.

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

When OpenBao is enabled, `DEPLOY_SECRETS_DIR` should contain only bootstrap
material, non-secret manifests, public certificates, and rollback env files.
Deployment scripts must not copy raw OpenBao-managed runtime secrets from
operator machines to hosts except as an explicit rollback path.

OpenBao-aware deployment scripts should accept:

```sh
OPENBAO_ADDR=https://openbao.internal:8200
OPENBAO_CACERT=/etc/video_cloud/openbao/ca.pem
OPENBAO_AUTH_METHOD=approle
OPENBAO_ROLE_ID_FILE=/etc/video_cloud/openbao/role_id
OPENBAO_SECRET_ID_FILE=/etc/video_cloud/openbao/secret_id
OPENBAO_KV_MOUNT=secret
OPENBAO_KV_PREFIX=rtk-cloud/staging/video-cloud
```

The renderer output is runtime state, not source material. Do not commit it and
do not archive it in readiness artifacts.

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
- Do not log OpenBao tokens, AppRole `secret_id` values, rendered env files, or
  OpenBao response bodies that contain secret data.
- Treat OpenBao availability as a deployment prerequisite. If OpenBao is
  unreachable during startup, services must fail closed unless an operator has
  explicitly selected the rollback env-file path.

## V1 Migration Order

1. Update documentation first: governance, service config maps, certissuer
   design, OpenBao bootstrap, rollout, rollback, and acceptance criteria.
2. Create local ignored `.secrets/` directories for Linode staging and
   production that hold only bootstrap material, manifests, public certificates,
   and rollback files.
3. Stand up OpenBao with TLS, audit logging, `kv-v2`, and PKI mounts for
   device/factory, app/user, and gateway/server certificates.
4. Move operator-local Account Manager, Admin, Video Cloud, and E2E secret
   material into OpenBao without committing values.
5. Add service-specific PRs so deployment scripts support OpenBao-backed env
   rendering while preserving `DEPLOY_SECRETS_DIR` rollback.
6. Switch `cmd/certissuer` staging config to the OpenBao PKI signer provider.
7. Run staging validation, including `scripts/run-staging-e2e.sh`.
8. Rotate any values previously exposed in tracked files or logs.

The OpenBao migration is not accepted until the staging end-to-end command
passes:

```sh
scripts/run-staging-e2e.sh
```
