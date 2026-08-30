# Multi-environment Object Storage and credentials

Runtime media and release artifacts have different owners and lifecycle rules:

| Purpose | Owner | Policy | Staging target |
|---|---|---|---|
| Runtime media | One environment | `colocated` with compute | `rtk-video-staging-sg` in `sg-sin-2` |
| Release artifacts | Shared release system | `shared-cross-region` | `rtk-cloud-client-artifacts` in `us-sea` |

Runtime intent is tracked in `cloud_env/<environment>/storage.env`. Shared release intent is tracked in `cloud_deploy/storage/release-artifacts.env`. Endpoints are never constructed from these values: the CLI obtains `s3_endpoint` from Linode's API.

## Credential profiles

All credentials live as individual `0600` files below `~/.config/rtk_cloud/<environment>/operator/env/`. Each environment is self-contained and has no shared credential fallback.

The shared profile normally contains `LINODE_TOKEN`, `GHCR_PULL_USERNAME`, `GHCR_PULL_TOKEN`, DNS credentials, and `LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID` / `LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY`. The environment profile contains `LINODE_MEDIA_OBJ_ACCESS_KEY_ID` / `LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY`.

Missing environment credentials fail closed. Scoped media credentials are mapped to `LINODE_OBJ_*` only while deployment child operations run; storage policy, bucket-region checks, endpoint inventory, and the read/write canary remain mandatory.

## Lifecycle

```bash
rtk-cloud deployment storage-plan --environment staging
rtk-cloud deployment storage-bootstrap --environment staging --confirm video-cloud-staging
rtk-cloud deployment storage-migrate --environment staging --source-env-file /secure/source.env --confirm video-cloud-staging
rtk-cloud deployment storage-cutover --environment staging --confirm video-cloud-staging
rtk-cloud deployment storage-retire --environment staging --key-id 12345 --confirm video-cloud-staging
```

- `storage-plan` resolves compute/storage intent and discovers regional S3 endpoints through Linode.
- `storage-bootstrap` creates a missing destination bucket, issues a bucket-limited `read_write` key, validates it, and atomically updates the environment profile.
- `storage-migrate` copies only `clips/`, `brands/`, and `firmware/` beneath the environment prefix. It records per-object SHA-256 and resumable byte/object totals in `runtime/state/storage-migration.json`.
- `storage-cutover` revalidates the destination, runs a clip-path upload/read/delete smoke test, updates the runtime Secret, rolls the API and clip verifier, waits for readiness, and retains rollback credentials.
- `storage-retire` requires cutover state plus `runtime/state/storage-consumers.json` containing `"generic_key_in_use": false`. It revokes only `--key-id`; it never deletes a bucket.

The compatibility flags `--create-missing-object-storage-bucket` and `--grant-object-storage-bucket-access` route to storage bootstrap for resolved environments.

## Validation receipt

`credentials-check` and `provision` verify region capabilities, exact bucket region, API-reported endpoint, limited-key scope, signed list, and a reserved-prefix write/read/delete canary. Success writes `runtime/state/storage-preflight.json` containing only environment, purpose, bucket, region, endpoint, numeric key ID, redacted access-key suffix, and timestamp.

Secrets must never be committed. Runtime state is ignored and should be backed up using the existing encrypted environment-state process.
