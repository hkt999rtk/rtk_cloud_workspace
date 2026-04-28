# Documentation Governance

Status: active workspace policy.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-04-28.

## Purpose

This policy keeps RTK Cloud documentation searchable and prevents contract drift
across repositories. It defines where each kind of document belongs, which files
are authoritative, and how to review changes.

## Layers

| Layer | Source of truth | Scope |
| --- | --- | --- |
| Workspace | `docs/` in this repository | Cross-repo architecture, repository roles, integration snapshots, validation commands, and documentation governance. |
| Contracts | `repos/rtk_cloud_contracts_doc` | Shared wire, payload, API, auth, streaming, device transport, provisioning, and cross-service channel contracts. |
| Service | Owning service repository | Service-local architecture, module design, deployment, operations, migration notes, and tests. |
| Decisions | `docs/adr/` or service-local `docs/adr/` | Why an architecture or boundary decision was made. |

## Contract Rules

- `repos/rtk_cloud_contracts_doc` is the only normative cross-repo contract
  source of truth.
- Service docs may summarize contracts only when they link back to the
  contracts repository.
- If service implementation and contracts disagree, update either the
  implementation or the contracts explicitly. Do not let a service-local summary
  silently override the contracts.
- Nested contracts submodules should be pinned to the same commit as
  `repos/rtk_cloud_contracts_doc` unless a deliberate compatibility test needs a
  different snapshot.

## Document Classifications

Use these classifications in indexes and review notes:

| Classification | Meaning |
| --- | --- |
| `source` | Authoritative for its stated scope. |
| `reference-only` | Useful context, but not authoritative when it conflicts with source docs. |
| `supporting-note` | Planning, evidence, migration, or background material. |
| `generated-artifact` | Produced from another source and should not be edited first. |
| `index` | Navigation aid for humans and search tools. |

## Status Values

Use these status values for contracts, policies, and ADRs:

| Status | Meaning |
| --- | --- |
| `draft` | Proposed or incomplete; readers should confirm before implementing. |
| `active` | Current workspace or service policy. |
| `normative` | Binding contract or API behavior for dependents. |
| `deprecated` | Still valid for compatibility, but not recommended for new work. |
| `superseded` | Replaced by another document or decision. |

## Review Rules

- Review workspace governance docs when submodules are added, removed, or
  repurposed.
- Review contract docs before changing shared API, payload, transport,
  provisioning, or cross-service channel behavior.
- Review service docs with the owning service change.
- Keep plans and migration notes as supporting material; move stable behavior
  into service docs, contracts docs, or ADRs after implementation.
- Run `./scripts/docs-check.sh` before committing workspace documentation or
  submodule pointer changes.

## Metadata Template

New source documents should start with a short metadata block:

```md
Status: active

Owner: rtk_cloud_workspace

Last reviewed: YYYY-MM-DD
```

Contract documents should also identify their intended audience and backing
implementation source.
