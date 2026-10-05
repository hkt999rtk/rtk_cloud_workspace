# Cyber Security Analysis Workspace

Status: active index; linked threat analyses retain their own review status.

Classification: index.

Owner: rtk_cloud_workspace.

Last reviewed: 2026-10-05.

This directory is the workspace entry point for RTK Cloud security analysis.
It stores threat models, STRIDE matrices, assumptions, source indexes, and
evidence notes. It does not replace the canonical architecture, contracts, or
service documents under `docs/` and `repos/*/docs/`.

## Current Review

The [Video Cloud STRIDE threat model](threat_models/rtk_video_cloud-stride-threat-model.md)
and [risk matrix](analysis/stride-matrix.md) cover the current Video Cloud
checkout and its adjoining trust boundaries. This is a draft static assessment,
not a complete platform review or a live environment security sign-off.
The [assumptions register](assumptions.md) records profile dependencies and
unresolved context; the [source index](sources.md) separates contracts, design,
implementation and test sources.

The 2026-10-05 revision retains the original STRIDE IDs and adds PKI trust-domain
misuse (S3), authorization after revocation (E3), direct-upload state/capabilities
(T3), optional webhook egress (I3), and restore rollback (T4). It removes the
retired account/video broker from the current topology and records LKE TCP
passthrough, strict versus compatibility authentication, storage credential
boundaries, and the remaining validation responsibilities.

## Method

Threat models in this directory use STRIDE:

| Category | Security question |
| --- | --- |
| Spoofing | Can an attacker impersonate a user, service, device, broker node, or deployment actor? |
| Tampering | Can an attacker modify commands, state, media, firmware, configuration, or artifacts? |
| Repudiation | Can an actor deny sensitive actions because audit evidence is missing or weak? |
| Information Disclosure | Can sensitive data, tokens, media, telemetry, or secrets leak across boundaries? |
| Denial of Service | Can an attacker exhaust critical runtime, broker, storage, or deployment resources? |
| Elevation of Privilege | Can a lower-privileged actor gain admin, cross-tenant, cross-device, or service privileges? |

Each threat model should include:

- scope and assumptions
- evidence anchors to repository paths
- system model and trust boundaries
- assets and security objectives
- attacker model
- STRIDE matrix and prioritized threats
- mitigations, detections, and manual review focus paths

Evidence uses four explicit levels: **D** for documented requirements/design
(with the source's status), **I** for inspected implementation, **T** for test
source inspected without a passing execution claim, and **E** for dated
environment execution evidence. This review collected no E evidence. A feature
flag, test file or active policy does not prove a control is deployed.

Keep likelihood, impact and priority conditional on the attacker's actual
authority, selected profile and existing controls. Separate scoped media/data
credentials from usable platform signer/admin authority. Component owners in
the matrix are proposed validation responsibilities, not accepted assignments.

## Directory Layout

| Path | Purpose |
| --- | --- |
| `sources.md` | Index of canonical security-relevant source documents. |
| `assumptions.md` | Deployment and product assumptions that affect risk ranking. |
| `threat_models/` | Formal threat model reports. |
| `analysis/` | Working notes, STRIDE matrices, attack-surface summaries, and review focus paths. |
| `evidence/` | Redacted evidence notes, command-output summaries, screenshots, or external references. |

## Data Handling Rules

- Do not store secrets, raw tokens, private keys, passwords, DSNs with
  credentials, customer data, or unredacted logs in this directory.
- Reference canonical documents by path instead of copying long sections.
- Mark unsupported claims as assumptions.
- If sources disagree, record the conflict and prefer the documented
  source-of-truth hierarchy in `docs/architecture.md` and
  `docs/documentation-governance.md`.
- Generated security artifacts should be concise enough for AppSec review and
  specific enough to guide manual code review.

## Maintenance

- Update the model, matrix, assumptions and sources together when routes,
  trust roots, entitlement behavior, feature flags or recovery procedures change.
- Preserve existing threat IDs; add new scenarios without silently reusing an ID.
- Record the reviewed code snapshot and distinguish it from an environment's
  deployed version and evidence date.
- Validate relative source links and run the workspace documentation check.
  Runtime security tests and environment qualification are separate work.
- Keep [the workspace topic map](../docs/README.md) linked to this index.
  Ordinary document edits do not authorize RAG reindexing or embedding calls.
