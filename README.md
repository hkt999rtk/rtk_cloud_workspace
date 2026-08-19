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

## Environment Operations

Start with the read-only preflight, then render and review the resolved plan.
Do not rely on topology numbers copied from a previous report; node counts,
replicas, storage, provider SKUs, and projected services are authoritative only
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

## Workspace Rules

- Keep product/source changes in the owning service repository.
- Use this repository to pin integration snapshots, cross-repo docs, and test
  orchestration.
- Keep cross-repo wire and payload contracts in `repos/rtk_cloud_contracts_doc`;
  service docs should link to contracts instead of copying them.
- Do not add generated logs, credentials, tokens, or local server secrets.
- Do not treat submodule pointers as floating branches; a pointer change means
  the workspace snapshot changed.
