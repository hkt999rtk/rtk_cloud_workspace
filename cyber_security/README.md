# Cyber Security Analysis Workspace

This directory is the workspace entry point for RTK Cloud security analysis.
It stores threat models, STRIDE matrices, assumptions, source indexes, and
evidence notes. It does not replace the canonical architecture, contracts, or
service documents under `docs/` and `repos/*/docs/`.

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

