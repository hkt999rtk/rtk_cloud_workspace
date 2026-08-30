# RTK Cloud Workspace

Integration workspace for the RTK cloud project. This repository does not
merge service source code. It pins a known cross-repository snapshot through
git submodules and keeps project-level orchestration docs/scripts in one place.

## Repositories

The workspace snapshot includes:

| Path | Repository | Role |
| --- | --- | --- |
| `repos/rtk_cloud_client` | `hkt999rtk/rtk_cloud_client` | Multi-language SDK client. |
| `repos/rtk_video_cloud` | `hkt999rtk/rtk_video_cloud` | Video cloud server. |
| `repos/rtk_cloud_contracts_doc` | `hkt999rtk/rtk_cloud_contracts_doc` | Cross-repo contracts source of truth. |
| `repos/rtk_account_manager` | `hkt999rtk/rtk_account_manager` | Account, organization, and device registry service. |
| `repos/rtk_billing` | `hkt999rtk/rtk_billing` | Provider-neutral Billing, payment orchestration, balance, and invoice service. |
| `repos/rtk_ameba_webrtc` | `hkt999rtk/rtk_ameba_webrtc` | Cross-platform RTK WebRTC Cloud SDK and AmebaPRO2 device integration. |
| `repos/rtk_cloud_frontend` | `hkt999rtk/rtk_cloud_frontend` | User-facing Realtek Cloud introduction website. |
| `repos/rtk_cloud_admin` | `hkt999rtk/rtk_cloud_admin` | Admin dashboard for fleet, provisioning, lifecycle, health, and audit operations. |
| `repos/rtk_cloud_logger` | `hkt999rtk/rtk_cloud_logger` | Shared structured logging package for RTK cloud Go services. |

## Start Here

| Goal | Entry point |
| --- | --- |
| Prepare a controller, create or take over an environment, and deploy | [`docs/deployment-operations.zh-TW.md`](docs/deployment-operations.zh-TW.md) |
| Prepare data and run local, acceptance, qualification, or load tests | [`docs/testing-operations.zh-TW.md`](docs/testing-operations.zh-TW.md) |
| Create or review tracked environment configuration | [`cloud_env/README.md`](cloud_env/README.md) |
| Troubleshoot a restored staging runtime | [`docs/staging-runtime-bootstrap.zh-TW.md`](docs/staging-runtime-bootstrap.zh-TW.md) |
| Browse architecture, governance, contracts, and historical evidence | [`docs/README.md`](docs/README.md) |

The only active generated runtime path is
`cloud_env/<environment>/runtime`. Provider-specific staging roots such as
`cloud_env/staging/lke` and `cloud_env/staging/linode` are not deployment
inputs. Historical reports may still mention them as evidence of old runs.

## Bootstrap

Clone with submodules:

```sh
git clone --recurse-submodules git@github.com-work:hkt999rtk/rtk_cloud_workspace.git
```

Or initialize after cloning:

```sh
git submodule update --init --recursive
```

## Common Commands

```sh
go run ./scripts/go/rtk-cloud -- status-all
go run ./scripts/go/rtk-cloud -- sync-all
go run ./scripts/go/rtk-cloud -- docs-check
go run ./scripts/go/rtk-cloud -- test-matrix
```

`status-all` and `docs-check` are read-only. `sync-all` fetches every submodule
remote but does not move pinned commits. To change the validated cross-repo
snapshot, update the relevant submodule commit intentionally and commit the
pointer change in this workspace repository.

## Local Test Prerequisites

The quick workspace baseline requires the Go toolchain declared by
`scripts/go/go.mod`. Service and UI tests may additionally require Node.js 22,
`npm`, `jq`, Chromium, or service-specific dependencies.

Docker is not required for every unit test. It is required for local tests that
start dependencies such as the Account Manager PostgreSQL service or the Video
Cloud EMQX broker. On Windows with Docker Desktop, enable the current distro in
**Settings > Resources > WSL Integration**; do not install a second Docker
daemon inside that WSL distro. Verify the integration from WSL before running
Docker-backed tests:

```sh
docker version
docker compose version
docker run --rm hello-world
```

Run the workspace baseline from the repository root:

```sh
(cd scripts/go && go run ./rtk-cloud -- test-matrix)
```

See the [testing operator guide](docs/testing-operations.zh-TW.md) for service,
deterministic E2E, UI, and deployed acceptance test commands.

## Load Testing

The canonical load-test entry point is
`loadtests/home-100k/scripts/home-100k.sh`. Always select an explicit scenario
and run `plan`, `preflight`, and `workflow-dry-run` before a live workflow. The
script's default command is `workflow-live`, which can create paid Linode VMs,
so do not invoke the script without a command.

The following example reviews the MQTT 1K scenario without creating VMs:

```sh
export HOME100K_DESCRIPTION_FILE=loadtests/home-100k/scenarios/mqtt-1k.description.env
export HOME100K_BRANDNAME=RTK1K
export HOME100K_RUN_ID="mqtt1k-$(date -u +%Y%m%dT%H%M%SZ)"

./loadtests/home-100k/scripts/home-100k.sh plan
./loadtests/home-100k/scripts/home-100k.sh preflight
./loadtests/home-100k/scripts/home-100k.sh workflow-dry-run
```

Only after the plan, fixture inventory, certificates, staging runtime, capacity,
and expected cost have been reviewed, start the live lifecycle explicitly:

```sh
./loadtests/home-100k/scripts/home-100k.sh workflow-live
```

Artifacts and the final report are written to
`loadtests/home-100k/reports/<run-id>/`. If a live run is interrupted, preserve
the same `HOME100K_RUN_ID` and resume it with:

```sh
./loadtests/home-100k/scripts/home-100k.sh workflow-resume-live
```

Review resources left by that run before cleanup. Destruction requires both
live flags and must only target the selected run ID:

```sh
./loadtests/home-100k/scripts/home-100k.sh list-vms
./loadtests/home-100k/scripts/home-100k.sh destroy-vms --live --confirm-live
```

Available profiles include MQTT, video/TURN, and clip-storage canary, 1K, 10K,
50K, or 100K scenarios under `loadtests/home-100k/scenarios/`. Do not obtain a
larger test by changing only `HOME100K_DEVICES`; select the matching scenario
and review its capacity plan. Detailed prerequisites, success gates, monitoring,
resume behavior, and cleanup rules are in the
[Home 100K load-test guide](loadtests/home-100k/README.md).

## Environment Operations

Start with the read-only preflight, then render and review the resolved plan.
Do not rely on topology numbers copied from a previous report; node counts,
replicas, storage, provider Products, and projected services are authoritative only
in the current environment plan.

```sh
go run ./scripts/go/rtk-cloud -- deployment preflight --environment staging --operation plan
go run ./scripts/go/rtk-cloud -- deployment plan --environment staging
go run ./scripts/go/rtk-cloud -- deployment preflight --environment staging --operation provision
go run ./scripts/go/rtk-cloud -- deployment provision --environment staging --confirm video-cloud-staging
go run ./scripts/go/rtk-cloud -- deployment preflight --environment staging --operation acceptance
go run ./scripts/go/rtk-cloud -- deployment acceptance --environment staging --confirm video-cloud-staging
```

Provision and acceptance details, credential boundaries, runtime restore, and
destructive-operation warnings are in the
[deployment operator guide](docs/deployment-operations.zh-TW.md). Test data,
qualification, load profiles, monitoring, report gates, resume, and cleanup are
in the [testing operator guide](docs/testing-operations.zh-TW.md).

## Remove a Deployed Environment

> [!DANGER]
> `deployment remove` is destructive. It deletes resources owned by the selected
> stack, including Kubernetes workloads and storage, the LKE cluster, persistent
> volumes, VMs, firewalls, VPCs, owned DNS records, and an environment-owned
> empty Object Storage bucket. A deleted environment or its data may not be
> recoverable. Never run this command to test access or inspect a deployment.

Before removal:

1. Confirm that `cloud_env/<environment>/runtime` belongs to the exact cluster
   and stack being removed.
2. Back up required runtime state, databases, Object Storage data, test reports,
   and sanitized evidence outside the environment runtime.
3. Confirm the environment SecretStore still provides the Linode and DNS
   credentials required for cleanup.
4. Stop acceptance and load tests. List and remove load-generator VMs separately
   with their original `HOME100K_RUN_ID`.
5. Review `CLOUD_STACK_NAME` and use that exact value for `--confirm`:

   ```sh
   rg '^CLOUD_STACK_NAME=' cloud_env/staging/environment.env
   ```

Clean up load-generator VMs before removing the environment, when applicable:

```sh
export HOME100K_RUN_ID=<original-run-id>
./loadtests/home-100k/scripts/home-100k.sh list-vms
./loadtests/home-100k/scripts/home-100k.sh destroy-vms --live --confirm-live
```

After reviewing the target and backups, remove the environment explicitly. For
example, the staging stack uses:

```sh
go run ./scripts/go/rtk-cloud -- deployment remove \
  --environment staging \
  --confirm video-cloud-staging
```

The cleanup is ownership-scoped and must not be used to remove CI runners,
release buckets, another environment, or unrelated provider resources. A
non-empty Object Storage bucket or any provider cleanup failure means removal is
incomplete. Check the command result and then verify in the provider console
that no resources owned by the selected stack remain. Do not manually delete
the LKE cluster first; doing so can prevent the normal Kubernetes storage and
ownership-aware cleanup sequence.

## Workspace Rules

- Keep product/source changes in the owning service repository.
- Use this repository to pin integration snapshots, cross-repo docs, and test
  orchestration.
- Keep cross-repo wire and payload contracts in `repos/rtk_cloud_contracts_doc`;
  service docs should link to contracts instead of copying them.
- Do not add generated logs, credentials, tokens, or local server secrets.
- Do not treat submodule pointers as floating branches; a pointer change means
  the workspace snapshot changed.
