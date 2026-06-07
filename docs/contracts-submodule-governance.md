# Contracts Submodule Governance

Status: active workspace policy.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-06-03.

## Purpose

This policy defines how RTK Cloud repositories consume
`rtk_cloud_contracts_doc` without creating multiple sources of truth.

## Source Of Truth

`repos/rtk_cloud_contracts_doc` is the canonical workspace checkout and the only
normative source for shared wire, payload, API, auth, streaming, device
transport, provisioning, and cross-service channel contracts.

Service-local contracts submodules are pinned dependency snapshots. They exist
so each service repository can be cloned, built, tested, and released on its own.
They must not override or fork the canonical contracts repository.

## Standard Layout

Consumer repositories that need contract files locally must mount the contracts
submodule at:

```text
docs/rtk_cloud_contracts_doc
```

The standard submodule URL is the developer SSH alias:

```text
git@github.com-work:hkt999rtk/rtk_cloud_contracts_doc.git
```

Repositories that need GitHub Actions to initialize the private contracts
submodule may instead commit the exact canonical HTTPS URL:

```text
https://github.com/hkt999rtk/rtk_cloud_contracts_doc.git
```

CI may rewrite either canonical URL to a token-authenticated HTTPS URL at
runtime. The token URL must not be committed to `.gitmodules`.

## Commit Alignment

Nested service submodules should be pinned to the same commit as
`repos/rtk_cloud_contracts_doc`. A different commit is allowed only for a
deliberate compatibility test or staged migration, and the owning service must
document the reason in a repo-local note or release note.

When updating shared contracts:

1. Update `repos/rtk_cloud_contracts_doc`.
2. Update affected service submodule pointers under `docs/rtk_cloud_contracts_doc`.
3. Run the workspace contracts check before committing.

## Checks

Run:

```sh
go run ./scripts/go/rtk-cloud -- contracts-check
go run ./scripts/go/rtk-cloud -- docs-check
```

The checks verify standard paths, the standard URL policy, and nested submodule
commit alignment with the root contracts checkout.
