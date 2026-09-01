# Staging Runtime Bootstrap and Restore

This document explains how to prepare the staging runtime required for MQTT/
shadow load tests after cloning the workspace onto another macOS controller.

## Understand the Git Boundary First

Git stores only declarative environment configuration.
`cloud_env/<environment>/runtime/` stores only non-sensitive generated runtime.
Kubeconfig, service secrets, private keys, Device credentials, and the SQLite
credential database are all managed under `~/.config/rtk_cloud/<environment>/`
and must not be committed to Git.

Consequently, `git clone` alone does not produce a complete environment capable
of running load tests.

## Existing Staging: Restore from Another Mac

This is the standard flow for the same staging cluster and existing Device/user
identities. On the machine where setup is already complete, first verify the
SecretStore and non-sensitive runtime:

```sh
cd /path/to/rtk_cloud_workspace

go run ./scripts/go/rtk-cloud -- secrets verify --environment staging
find cloud_env/staging/runtime \
  \( -path '*/state/provider-preflight.env' -o -path '*/env/stack.env' \) -print
```

Use an encrypted, controlled file-transfer method to copy the entire staging
SecretStore to the new machine. The destination environment must not already
exist and must never share or fall back to prod:

```sh
rsync -a --delete \
  /secure/source/rtk_cloud/staging/ \
  "$HOME/.config/rtk_cloud/staging/"

find "$HOME/.config/rtk_cloud/staging" -type d -exec chmod 700 {} +
find "$HOME/.config/rtk_cloud/staging" -type f -exec chmod 600 {} +
go run ./scripts/go/rtk-cloud -- secrets verify --environment staging
```

Confirm the source and destination of `rsync --delete` first. Never use the
workspace root or another broad directory as its destination.

A remote deployment runner may use the workspace restore/check tool. First place
the known-good non-sensitive runtime in a controlled location readable remotely,
then run in the remote workspace:

```sh
scripts/restore-staging-runtime.sh \
  --source-runtime /secure/path/cloud_env/staging/runtime \
  --target-runtime "$PWD/cloud_env/staging/runtime"
```

If another secure channel already restored the runtime, check it only:

```sh
scripts/restore-staging-runtime.sh \
  --check-only \
  --target-runtime "$PWD/cloud_env/staging/runtime"
```

The restore tool copies only `env/stack.env`, `state/provider-preflight.env`
and its small documented allowlist of LKE controller metadata; it does not
recursively copy service env, kubeconfig, keys or test databases. Review any
additional non-secret controller artifacts separately. For matched cloud data
recovery after deployment, follow [Core Backup and Restore](backup-restore.md).

The restore tool handles only non-sensitive runtime. Prepare the SecretStore
separately and pass `secrets verify`. GitHub Actions should write GitHub Secrets
to a temporary job-specific `RTK_CLOUD_CONFIG_ROOT` and delete it when the job
ends; it cannot obtain them through `git clone`.

After restoration, run from the new workspace:

```sh
cd /path/to/new-workspace

export HOME100K_ENV_ROOT="$PWD/cloud_env/staging/runtime"

test -s "$HOME100K_ENV_ROOT/state/provider-preflight.env"
test -s "$HOME100K_ENV_ROOT/env/stack.env"
test -s "$HOME/.config/rtk_cloud/staging/kube/kubeconfig.yaml"
test -s "$HOME/.config/rtk_cloud/staging/test/devices/test_device/loadtest.env"
test -s "$HOME/.config/rtk_cloud/staging/test/databases/rtk-test-data.sqlite"

loadtests/home-100k/scripts/home-100k.sh plan
```

SQLite test data must retain the original staging identity. Do not arbitrarily
regenerate users/Devices or rotate credentials on the new Mac; database
credentials, Device bindings, and staging server state could diverge.

### LKE active-service limit

The LKE account limit is also absent from a Git clone. Create it before deployment
mutation:

```text
cloud_env/staging/runtime/adapters/lke/account.env
```

The repository provides a tracked example:

```text
cloud_env/staging/runtime/adapters/lke/account.env.example
```

Copy it and enter the limit confirmed by Linode:

```sh
cp cloud_env/staging/runtime/adapters/lke/account.env.example \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

Use the Linode-confirmed account limit, for example:

```env
LKE_ACTIVE_SERVICE_LIMIT=20
```

This is a safety number, not another secret. The Linode API cannot currently query
the account limit through `LINODE_TOKEN`. If unknown, do not guess; ask the
Linode/account owner. Before creating paid resources, deployment compares current
active services plus planned resources against this limit.

If the original workspace has completed staging setup, copy its operator state:

```sh
mkdir -p cloud_env/staging/runtime/adapters/lke
cp /path/to/known-good-workspace/cloud_env/staging/runtime/adapters/lke/account.env \
  cloud_env/staging/runtime/adapters/lke/account.env
chmod 600 cloud_env/staging/runtime/adapters/lke/account.env
```

This file is in the Git-ignored runtime. Do not commit it or put it in tracked
`cloud_env/staging/overrides/*.env`.

## New Staging: Recreate Runtime

Use this path only when creating a new staging environment or when the original
runtime cannot be recovered. These commands may create or modify cloud resources:

```sh
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment staging \
  --confirm video-cloud-staging
```

After deployment completes, obtain the kubeconfig according to the staging
runbook, synchronize normalized environment metadata, and generate load-test data:

```sh
go run ./scripts/go/rtk-cloud -- sync-env \
  --env-root cloud_env/staging

go run ./scripts/go/rtk-cloud -- generate-load-devices \
  --env-root cloud_env/staging \
  --brandname RTK
```

`generate-load-devices` writes Device credentials and the SQLite database used by
bindings into runtime artifacts. Follow [`scripts/README.md`](../scripts/README.md)
for the complete user/Device creation and binding sequence. Do not mix newly
generated data into the original staging cluster.

### Enable the ownership handoff coordinator

The Account Manager ownership handoff worker is disabled by default. Enable it
only when Account Manager, Billing, Factory Enrollment, Video Cloud, MQTT usage,
and EMQX are being deployed together at compatible versions:

```sh
export LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED=true
export LKE_VIDEO_CLOUD_MQTT_USAGE_STORAGE=5Gi

go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment staging \
  --confirm video-cloud-staging
```

Do not pass `--workloads` while the coordinator is enabled. The deployment must
rotate all participant credentials, apply the internal NetworkPolicies, create
the MQTT usage checkpoint PVC, and roll out every participant as one coordinated
operation. The SecretStore catalog owns the dedicated handoff and EMQX API
credentials; do not substitute human login, dashboard, tenant, Billing debit, or
other service credentials. The MQTT usage checkpoint uses `ReadWriteOnce` and a
single `Recreate` deployment so a rollout cannot attach the same volume to two
pods concurrently.

## Checks Before Starting a Load Test

The default runtime path is:

```text
cloud_env/staging/runtime
```

If using another location, set `HOME100K_ENV_ROOT`. All of the following must exist:

| Path | Source | Nature |
| --- | --- | --- |
| `state/provider-preflight.env` | Deployment plan/provision | Generated runtime |
| `env/stack.env` | Sync/deployment flow | Generated runtime |
| `adapters/lke/account.env` | Linode account confirmation | Operator safety state |
| `~/.config/rtk_cloud/staging/kube/kubeconfig.yaml` | Kubernetes/provider access | Secret |
| `~/.config/rtk_cloud/staging/test/devices/...` | Load-Device identity | Secret credentials |
| `~/.config/rtk_cloud/staging/test/databases/...` | User/Device/bind flow | Secret credentials |

Home MQTT runner credential input must reside in SecretStore `test/devices/`;
workspace runtime must not retain a second copy.

If you receive `required prerequisites are missing`, first confirm that
`HOME100K_ENV_ROOT` points to the runtime directory, not `cloud_env/staging` or
legacy `cloud_env/staging/linode`. A legacy provider-specific path cannot replace
the normalized `cloud_env/staging/runtime`.

## Security Requirements

- Do not commit runtime, kubeconfig, SQLite, private keys, certificates, or service secrets.
- Do not paste SQLite or kubeconfig contents into issues, PRs, chat messages, or logs.
- Before and after transferring a SecretStore, confirm directories are `0700` and files are `0600`.
- If runtime is lost, first determine whether it can be restored from the original
  workspace. Reprovision or reenroll only when intentionally creating new staging.
