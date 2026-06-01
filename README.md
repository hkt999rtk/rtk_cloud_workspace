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
| `repos/rtk_cloud_frontend` | `hkt999rtk/rtk_cloud_frontend` | User-facing Realtek Cloud introduction website. |
| `repos/rtk_cloud_admin` | `hkt999rtk/rtk_cloud_admin` | Admin dashboard for fleet, provisioning, lifecycle, health, and audit operations. |
| `repos/rtk_cloud_logger` | `hkt999rtk/rtk_cloud_logger` | Shared structured logging package for RTK cloud Go services. |

## Documentation Entry Points

| Scope | Entry point | Purpose |
| --- | --- | --- |
| Workspace docs | [`docs/README.md`](docs/README.md) | Cross-repository documentation index. |
| Workspace architecture | [`docs/architecture.md`](docs/architecture.md) | Repository boundaries and source-of-truth model. |
| Documentation governance | [`docs/documentation-governance.md`](docs/documentation-governance.md) | Ownership, status, review, and drift-prevention rules. |
| Artifact release governance | [`docs/artifact-release-governance.md`](docs/artifact-release-governance.md) | Linode Object Storage release artifact policy and adoption matrix. |
| Cross-repo contracts | [`repos/rtk_cloud_contracts_doc/README.md`](repos/rtk_cloud_contracts_doc/README.md) | Normative wire, payload, and integration contracts. |
| Cross-repo testing | [`docs/testing.md`](docs/testing.md) | Local validation commands for pinned snapshots. |

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

## Staging Shortcuts

Use `./stg.sh` for the local staging environment. It forwards to the Go CLI and
sets `--env-root cloud_env/staging` automatically.

```sh
./stg.sh provision
./stg.sh brand RTK
./stg.sh brands
./stg.sh users RTK 10
./stg.sh devices 100
./stg.sh bind RTK 100
./stg.sh certs
```

From `scripts/`, use `../stg.sh ...`.

## Workspace Rules

- Keep product/source changes in the owning service repository.
- Use this repository to pin integration snapshots, cross-repo docs, and test
  orchestration.
- Keep cross-repo wire and payload contracts in `repos/rtk_cloud_contracts_doc`;
  service docs should link to contracts instead of copying them.
- Do not add generated logs, credentials, tokens, or local server secrets.
- Do not treat submodule pointers as floating branches; a pointer change means
  the workspace snapshot changed.
