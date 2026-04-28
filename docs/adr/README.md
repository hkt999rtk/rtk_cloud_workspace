# Architecture Decision Records

Status: active index.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-04-28.

Use this directory for workspace-level decisions that affect more than one
repository. Service-local decisions belong in the owning service repository,
normally under `docs/adr/`.

## ADR Format

Each ADR should use this structure:

```md
# ADR NNNN: Title

Status: proposed | accepted | deprecated | superseded

Date: YYYY-MM-DD

Supersedes: ADR NNNN or none

Superseded by: ADR NNNN or none

## Context

## Decision

## Consequences
```

Keep ADRs focused on decisions and tradeoffs. Put implementation checklists in
plans or service docs instead.
