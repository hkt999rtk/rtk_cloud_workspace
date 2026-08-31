# Deployment Secrets Governance

Status: active workspace source document for deployment secret handling.

Owner: rtk_cloud_workspace.

Last reviewed: 2026-08-31.

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
- Standardize production-like LKE deployments on OpenBao or an approved
  customer secret manager, with Kubernetes Secrets limited to synchronized
  runtime material and local files retained only for bootstrap, local
  development, and rollback.

## Canonical Local Secret Root

The canonical local secret root is defined by [SecretStore](secret-store.md):

```text
~/.config/rtk_cloud/<environment>/
```

Environment names such as `staging` and `prod` select independent stores; there
is no implicit alias, shared fallback or staging-to-production reuse.
`RTK_CLOUD_CONFIG_ROOT` may select a different parent directory. The repository
`cloud_env/<environment>/runtime/` contains non-secret generated state only.

The former `.secrets/<environment>/<provider>/<service>/` layout is
reference-only migration input, not the current deployment or recovery source.
Only `secrets migrate` may read legacy secret paths; normal deployment and
backup commands do not fall back to them.

## OpenBao Source Of Truth

OpenBao is the target secret manager for staging and production. The local
environment-specific SecretStore remains the operator bootstrap and recovery
interface, but long-lived service-generated runtime secrets should be
stored in OpenBao once the selected K8s secret-injection path is approved.

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
  target workload runtime environment. Legacy VM/systemd env rendering remains
  reference-only.
- `factoryenroll` may not sign certificates directly in OpenBao; it continues
  to call `cmd/certissuer` over the existing issuer boundary.
- Operators may write or rotate secrets through explicit administrative policy;
  service roles must not have write access to production runtime secrets.

For legacy Linode/systemd deployments, service authentication to OpenBao used
AppRole:

```text
/etc/video_cloud/openbao/role_id
/etc/video_cloud/openbao/secret_id
```

Both files must be root-owned, mode `0600`, and excluded from readiness
reports when inspecting legacy hosts. Current K8s runtime should use Kubernetes
auth or an approved External Secrets-style injection path instead of AppRole.

Runtime services should continue to consume env files initially. A deployment
render step reads OpenBao KV entries and writes root-owned files under
`/run/video_cloud/*.env` or another tmpfs runtime directory before systemd
starts the service. This preserves the current process config boundary while
moving secret ownership to OpenBao.

## Local SecretStore Layout

```text
~/.config/rtk_cloud/<environment>/
  operator/
  runtime/
  pki/
  openbao/
  kube/
  test/
```

| Directory | Purpose |
| --- | --- |
| `operator/` | Environment-bound provider/operator credentials; independent recovery access. |
| `runtime/` | Long-lived runtime secret catalog; explicitly selected encrypted backup material. |
| `pki/` | Deployment PKI and certificate/key material, not a replacement for OpenBao issuer state. |
| `openbao/` | TLS material and separately controlled seal/bootstrap access; never archive the whole directory indiscriminately. |
| `kube/` | Environment kubeconfig; recreate for a replacement cluster. |
| `test/` | Test-only Device/user credentials and databases; separate controller handoff. |

Directories are `0700`, files `0600`. Short-lived access/admin tokens are not
persisted. Use `secrets inventory` for non-secret metadata rather than creating
a second service-directory catalog.

## LKE Secret Management Target

LKE deployments must not make Git, Helm values, Kustomize overlays, Docker
images, or CI logs the source of truth for runtime secrets. The target secret
boundary is:

- OpenBao or an approved customer secret manager stores long-lived runtime
  secrets, PKI state and policy; audit is emitted to an independently retained
  sink and must not be rewound with core storage.
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

The current LKE adapter uses standalone OpenBao file storage. The
[core backup/restore procedure](backup-restore.md) handles it with an offline
PVC archive matched to PostgreSQL. Production still requires approved HA/seal
design, independent recovery-key escrow, audit retention, auth rebinding and a
successful full recovery rehearsal; local backup script tests do not close
those gates.

## Legacy Manifest Example (Reference Only)

The older service-level manifest example below remains migration/reference
material. Current deployments use the SecretStore catalog and `secrets
inventory`; current core archives use the versioned manifest documented in
[backup-restore.md](backup-restore.md). All public inventories follow the same
no-secret-values rule.

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

Current workspace commands select the environment explicitly:

```sh
go run ./scripts/go/rtk-cloud -- secrets verify --environment staging
go run ./scripts/go/rtk-cloud -- secrets inventory --environment staging
```

`DEPLOY_SECRETS_DIR` and AppRole-based VM rendering are legacy service
interfaces, not workspace SecretStore fallbacks. A reviewed legacy bridge may
use the following service-specific OpenBao settings; Kubernetes workload auth
must follow the selected LKE injection contract:

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
For LKE, `DEPLOY_SECRETS_DIR` is not the runtime secret source of truth. It may
hold operator-local bootstrap files, non-secret manifests, public certificates,
and rollback references while OpenBao or the approved secret manager owns live
runtime values.

## Artifact Boundary

`.artifacts/` remains test output only. It may contain temporary E2E run outputs,
redacted reports, generated fixtures, or local debugging material. It is not a
long-term deployment secret store.

Long-lived deploy secrets belong in the environment SecretStore and/or their
owning cloud secret manager. Reused E2E credentials belong under that
environment's `test/` tree. Encrypted core backups use a dedicated private
directory/bucket outside release artifacts; unseal shares and backup decryption
identities are escrowed independently. See [backup-restore.md](backup-restore.md).

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
- Kubernetes manifests, Helm values, and CI/CD deployment pipelines must not be
  produced until the LKE secret-management gate in
  `docs/lke-migration-inventory.md` is complete and human-approved.

## Current Deployment and Recovery Order

1. Update documentation first: governance, service config maps, certissuer
   design, OpenBao bootstrap, rollout, rollback, and acceptance criteria.
2. Initialize/verify the environment SecretStore. Use the explicit migration
   command for legacy inputs; do not create new `.secrets/` deployment trees.
3. Stand up OpenBao with TLS, audit logging, `kv-v2`, and PKI mounts for
   device/factory, app/user, and gateway/server certificates.
4. Move operator-local Account Manager, Admin, Video Cloud, and E2E secret
   material into OpenBao without committing values.
5. Keep runtime injection and local recovery material aligned with the
   SecretStore catalog; preserve reviewed legacy bridges only where necessary.
6. Switch `cmd/certissuer` staging config to the OpenBao PKI signer provider.
7. Complete the LKE secret-management gate before writing production
   Kubernetes manifests, Helm values, or CI/CD deployment pipelines.
8. Run staging validation, including `scripts/run-staging-e2e.sh`.
9. Rehearse matched core backup/restore with separately retrieved escrow before
   production approval. Restore original PKI, then reconcile external
   revocation, billing and notification side effects before resuming traffic.
10. Rotate any values previously exposed in tracked files or logs.

The OpenBao migration is not accepted until the staging end-to-end command
passes:

```sh
scripts/run-staging-e2e.sh
```
