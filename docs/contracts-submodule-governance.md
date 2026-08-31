# Contracts Submodule Governance

Status: active workspace policy.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-08-31.

## Purpose

This policy defines how RTK Cloud repositories consume
`rtk_cloud_contracts_doc` without creating multiple sources of truth.

## Source Of Truth

`repos/rtk_cloud_contracts_doc` is the canonical workspace checkout and the only
normative source for shared wire, payload, API, auth, streaming, device
transport, provisioning, and cross-service channel contracts.

In the integrated workspace, service-local contract paths are symlinks to this
canonical checkout. A service may alternatively retain a registered contracts
submodule for standalone builds; that pinned snapshot must align with the root
checkout when tested in the workspace. Neither layout may override or fork the
canonical contracts repository.

## Standard Layout

Consumer repositories that need contract files locally must expose them at:

```text
docs/rtk_cloud_contracts_doc
```

The workspace's current consumer links point to `../../rtk_cloud_contracts_doc`.
The checker resolves links (including the workspace's own symlinks) and requires
the exact canonical checkout, not a copied directory or another checkout at the
same commit. Missing, dangling and redirected links fail validation.

The root checkout remains a registered submodule. Its standard URL is the
developer SSH alias:

```text
git@github-work.com:hkt999rtk/rtk_cloud_contracts_doc.git
```

Repositories that need GitHub Actions to initialize the private contracts
submodule may instead commit the exact canonical HTTPS URL:

```text
https://github.com/hkt999rtk/rtk_cloud_contracts_doc.git
```

The previous SSH alias `git@github.com-work:hkt999rtk/rtk_cloud_contracts_doc.git`
is also accepted for existing standalone consumers. No other repository or URL
variant is accepted.

CI may rewrite an accepted canonical URL to a token-authenticated HTTPS URL at
runtime. The token URL must not be committed to `.gitmodules`.

## Commit Alignment

Every consumer must resolve to the same commit as `repos/rtk_cloud_contracts_doc`.
Canonical links inherit that commit automatically; any retained nested service
submodules must be pinned to it explicitly. Deliberate compatibility tests with
a different snapshot must document the reason separately and do not constitute
a passing workspace contracts check.

When updating shared contracts:

1. Update `repos/rtk_cloud_contracts_doc`.
2. Update any retained service submodule pointers under `docs/rtk_cloud_contracts_doc`;
   canonical links need no pointer update.
3. Run the workspace contracts check before committing.

## Checks

Run:

```sh
go run ./scripts/go/rtk-cloud -- contracts-check
go run ./scripts/go/rtk-cloud -- docs-check
```

The checks verify standard paths, the accepted URL policy, canonical link targets,
and consumer commit alignment with the root contracts checkout. This topology
check does not replace OpenAPI validation or semantic contract/implementation
acceptance tests.

### OpenAPI structural validation

Validate the canonical, Account Manager, Billing and Cloud Admin specifications with
their declared OpenAPI dialects (3.1.0 and 3.0.3). Run this separately from the
topology check and the observe-mode specification inventory:

```sh
openapi_venv_dir="$(mktemp -d)"
python3 -m venv "$openapi_venv_dir"
"$openapi_venv_dir/bin/python" -m pip install -r scripts/openapi-requirements.txt
"$openapi_venv_dir/bin/python" -m unittest discover -s scripts -p test_validate_openapi.py
"$openapi_venv_dir/bin/python" scripts/validate_openapi.py
```

The validator rejects duplicate YAML keys and validates schemas and references.
References must be local files within the workspace or document fragments;
initialize recursive submodules before running it. It checks referenced YAML
documents for duplicate keys as well as the four entry points. Schema component
names that collide but have different constraints must be split, preserving each
operation's intended constraints.

The `OpenAPI Contract Validation` workspace workflow runs these commands and
`test-spec-inventory check --mode required` when the validator, registered
repository gitlinks, specification registry, catalog or generated traceability
changes. Strict inventory rejects blocking findings; nonblocking draft candidates
remain visible. This is an additional CI gate; `pre-pr`, an observe-mode inventory
result, and this structural check do not substitute for one another or for
behavioral acceptance tests.
