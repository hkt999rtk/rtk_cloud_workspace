# RTK Cloud Secret Store

All local secret material is owned by one environment-specific directory:

```text
~/.config/rtk_cloud/<environment>/
```

Directories must use mode `0700`; files must use mode `0600`. Symlinks and
paths that escape the environment root are rejected. Environments are
self-contained: staging never falls back to prod, a shared profile, process
environment credentials, or legacy workspace files.

The repository `cloud_env/<environment>/runtime` tree contains only
non-secret topology, resolved state, and redacted evidence. Kubernetes Secrets
are runtime copies written from the local store. GitHub Actions Secrets remain
an independent CI store; CI writes them to a temporary
`RTK_CLOUD_CONFIG_ROOT` for the duration of one job.

## Commands

```bash
go run ./scripts/go/rtk-cloud -- secrets init --environment prod
go run ./scripts/go/rtk-cloud -- secrets plan --environment staging
go run ./scripts/go/rtk-cloud -- secrets migrate \
  --environment staging \
  --confirm video-cloud-staging
go run ./scripts/go/rtk-cloud -- secrets verify --environment staging
go run ./scripts/go/rtk-cloud -- secrets inventory --environment staging
```

`secrets migrate` is the only command allowed to read legacy profiles and
workspace secret paths. It stages and verifies the complete destination before
an atomic cutover, then moves enumerated legacy sources into a private
`migration-backup/<timestamp>` inside the new environment root. Normal
deployment commands do not implement a legacy fallback.

Short-lived access and admin tokens are never persisted. Their long-lived
signing secret is stored under `runtime/`; tokens are minted in memory when
needed.

## Backup and Recovery Boundary

Follow [Core Backup and Restore](backup-restore.md) for the matched OpenBao,
PostgreSQL and runtime-secret backup set. Explicitly selected `runtime/`, `pki/`
and OpenBao TLS files are encrypted with the core archive; selected Kubernetes
runtime copies must be synchronized and verified after restore. The workspace
runtime directory is not a private-key backup.

Operator/provider credentials, kubeconfig, age decryption identities, root
tokens and unseal/recovery material require independent escrow/access. They are
not automatically packed with core data. The recovery process preserves those
target-local paths and its maintenance journal while replacing selected runtime
credentials. Losing seal access or an offline Root/HSM cannot be repaired by
generating a new key and treating it as the original identity.

## GitHub Actions

CI stores values in GitHub Actions Secrets and materializes them only for one
job. The secret bundle is a JSON object with `operator`, `runtime`, and optional
`files` maps. `files` keys are environment-relative paths used for PKI/OpenBao
or test material. The helper emits no values:

```bash
export RTK_CLOUD_CONFIG_ROOT="$RUNNER_TEMP/rtk_cloud"
trap 'rm -rf "$RTK_CLOUD_CONFIG_ROOT" "$RUNNER_TEMP/rtk-cloud-secrets.json"' EXIT
scripts/ci/materialize-rtk-cloud-secret-store.sh \
  staging "$RUNNER_TEMP/rtk-cloud-secrets.json" "$KUBECONFIG"
```

The helper requires the root to be inside `RUNNER_TEMP`, refuses symlinks,
traversal and overwrites, applies `0700`/`0600`, and verifies the complete
catalog before installing the job kubeconfig. Deployment workflows must run
`secrets verify` again after applying the desired runtime Secrets; that second
check compares every Kubernetes binding to the canonical value. Keeping the
mirror comparison after apply permits a newly added catalog entry or an
intentional rotation to bootstrap without weakening the final verification.

Staging deployment jobs require the environment secret
`RTK_CLOUD_SECRET_BUNDLE`. The isolated runtime-coverage job requires a
separate `RTK_CLOUD_RUNTIME_COVERAGE_SECRET_BUNDLE`; it must not borrow the
staging bundle. `LINODE_TOKEN`, `GHCR_PULL_USERNAME`, `GHCR_PULL_TOKEN`,
`GODADDY_KEY`, and `GODADDY_SECRET` may be supplied as job secrets and are
materialized into `operator/env/` by the CI-only allowlist. Every runtime ID in
the current catalog must still be present in the appropriate bundle.

Staging email qualifications additionally require the staging environment
secret `IMAP_EMAIL_ADDR`. The workflow injects it into the job-local bundle
before materialization so an empty legacy bundle field cannot silently disable
email activation. It is never written to the repository or uploaded evidence.
