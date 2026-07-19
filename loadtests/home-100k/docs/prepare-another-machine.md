# Prepare Another Machine for a Home Load Test

Use this guide when a fresh clone reports that the Home MQTT/Device Shadow load
test prerequisites are missing.

## Why a Git Clone Is Not Enough

Git contains the load-test code, scenarios, and non-secret environment intent.
It intentionally does not contain `cloud_env/<environment>/runtime/`. That
directory can contain kubeconfig, provider state, service credentials, device
private keys and certificates, and SQLite test identities. It is ignored by
Git and must not be committed.

The default staging runtime is:

```text
cloud_env/staging/runtime
```

`HOME100K_ENV_ROOT` may select another runtime directory. Use an absolute path
when the command might be launched outside the workspace root.

## Required Files and Their Source

| Runtime path | Purpose | Created or restored by |
| --- | --- | --- |
| `state/provider-preflight.env` | Normalized provider region and quota evidence | `rtk-cloud deployment plan` |
| `env/stack.env` | Resolved stack and service metadata | `rtk-cloud deployment plan` |
| `state/kubeconfig.yaml` | Access to the staging Kubernetes cluster | `rtk-cloud deployment provision`, or secure runtime restore |
| `devices/test_device/loadtest.env` | Generated load-device manifest settings | `setup-staging-e2e-data.sh` / `generate-load-devices` |
| `artifacts/test-data/RTK-test-data.sqlite` | Users, bindings, device credentials, and certificate material | `setup-staging-e2e-data.sh` |

The supported load-test path is
`devices/test_device/loadtest.env`, not `env/loadtest.env`.

## Decide Which Kind of Machine This Is

### A second operator/controller machine

If the machine will control the same existing staging cluster, securely restore
the complete runtime from the current operator machine or an approved encrypted
backup. Do not run provisioning against an existing cluster from an empty
runtime: the runtime also contains operator state needed to avoid pairing new
secrets with existing persistent storage.

Example from the new machine:

```sh
mkdir -p cloud_env/staging/runtime
rsync -a --chmod=go-rwx \
  operator-host:/path/to/rtk_cloud_workspace/cloud_env/staging/runtime/ \
  cloud_env/staging/runtime/
```

Use an SSH connection and a trusted host. Treat the copied directory as secret
material. Do not place its archive in the repository or a shared artifact store
without approved encryption and access control.

After restoring it, run the validation below. Run `deployment acceptance` if
you also want to confirm live cluster access:

```sh
go run ./scripts/go/rtk-cloud -- deployment acceptance --environment staging
```

### A brand-new staging environment

For a genuinely new cluster and storage environment, first create the resolved
runtime and inspect the plan:

```sh
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
```

Provision only after reviewing the plan and providing the required operator
credentials, including `LINODE_TOKEN`:

```sh
go run ./scripts/go/rtk-cloud -- deployment provision \
  --environment staging \
  --confirm video-cloud-staging

go run ./scripts/go/rtk-cloud -- deployment acceptance --environment staging
```

Create the load-test identities after the services are ready. Choose counts
that cover the selected scenario; the following example prepares the 1K MQTT
profile with 50 users and 1,000 devices:

```sh
scripts/setup-staging-e2e-data.sh \
  --env-root cloud_env/staging/runtime \
  --brandname RTK1K \
  --description-file loadtests/home-100k/scenarios/mqtt-1k.description.env
```

This setup calls live staging APIs. It creates or updates test users, factory
enrolls devices, binds them, validates the bindings, and writes the ignored
SQLite and device artifacts. The description file also supplies the full
`home-diverse-v1` device mix. Do not omit it or substitute the four-type smoke
mix when the next command will run the 1K Home MQTT scenario.

For the complete new-cluster sequence, including operator credentials, EMQX
billing rule installation, acceptance gates, and cleanup, use
[`../../../docs/staging-from-scratch.md`](../../../docs/staging-from-scratch.md).

### A load-generator VM

Do not manually clone the repository and reconstruct the operator runtime on
each load-generator VM. Start `workflow-live` from a prepared controller. The
workflow builds the runner binaries, creates per-shard credential bundles, and
syncs only the runtime subset needed by each generator.

```sh
HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env \
HOME100K_BRANDNAME=RTK1K \
HOME100K_RUN_ID="mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)" \
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

The controller, not the load-generator VM, must pass the local prerequisite
validation before that synchronization can occur.

## Validate the Controller Runtime

Run from the workspace root:

```sh
ENV_ROOT="${HOME100K_ENV_ROOT:-cloud_env/staging/runtime}"

test -s "$ENV_ROOT/state/provider-preflight.env"
grep -q '^PROVIDER_REGION=.' "$ENV_ROOT/state/provider-preflight.env"
test -s "$ENV_ROOT/state/kubeconfig.yaml"
test -s "$ENV_ROOT/env/stack.env"
test -s "$ENV_ROOT/devices/test_device/loadtest.env"
test -s "$ENV_ROOT/artifacts/test-data/rtk1k-test-data.sqlite"
```

Then render the load plan without creating load-generator VMs:

```sh
HOME100K_ENV_ROOT="$ENV_ROOT" \
  ./loadtests/home-100k/scripts/home-100k.sh plan
```

If the selected scenario uses another brand, use its normalized SQLite filename
and set `HOME100K_BRANDNAME` consistently for planning and execution.

## Common Failures

- **`provider region is unresolved`**: run `deployment plan` on a configured
  environment, or restore its matching runtime.
- **Kubeconfig missing or unusable**: restore the existing environment runtime;
  for a new environment, complete `deployment provision`.
- **`loadtest.env` or SQLite missing**: run `setup-staging-e2e-data.sh` after
  staging services pass acceptance.
- **Data preflight reports missing device types**: regenerate from
  `create_devices` with the selected scenario description. For the MQTT 1K
  profile, use `--description-file
  loadtests/home-100k/scenarios/mqtt-1k.description.env`.
- **Files exist under `cloud_env/staging/lke`**: that is a retired provider-root
  layout. The active path is `cloud_env/staging/runtime`.
- **Files were copied but cannot be read**: keep them owner-readable and verify
  that the load-test process runs as that owner. The SQLite database should
  remain mode `0600`.
