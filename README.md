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

## Documentation Entry Points

| Scope | Entry point | Purpose |
| --- | --- | --- |
| Workspace docs | [`docs/README.md`](docs/README.md) | Cross-repository documentation index. |
| Workspace architecture | [`docs/architecture.md`](docs/architecture.md) | Repository boundaries and source-of-truth model. |
| Documentation governance | [`docs/documentation-governance.md`](docs/documentation-governance.md) | Ownership, status, review, and drift-prevention rules. |
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
./scripts/status-all.sh
./scripts/sync-all.sh
./scripts/docs-check.sh
./scripts/test-matrix.sh
```

`status-all.sh` and `docs-check.sh` are read-only. `sync-all.sh` fetches every
submodule remote but does not move pinned commits. To change the validated
cross-repo snapshot, update the relevant submodule commit intentionally and
commit the pointer change in this workspace repository.

## Workspace Rules

- Keep product/source changes in the owning service repository.
- Use this repository to pin integration snapshots, cross-repo docs, and test
  orchestration.
- Keep cross-repo wire and payload contracts in `repos/rtk_cloud_contracts_doc`;
  service docs should link to contracts instead of copying them.
- Do not add generated logs, credentials, tokens, or local server secrets.
- Do not treat submodule pointers as floating branches; a pointer change means
  the workspace snapshot changed.
