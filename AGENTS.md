# Workspace guidance

## Documentation lookup

For questions about RTK Cloud specifications, architecture, environment DNS,
service behavior, or where a rule is documented, use
[`rtk-knowledge-search`](.agents/skills/rtk-knowledge-search/SKILL.md).

Use [`docs/README.md`](docs/README.md) as the maintained topic and repository
entry map. Follow [`Documentation Governance`](docs/documentation-governance.md)
for authority and status. Confirm retrieved passages against the current source
file; distinguish design intent from dated deployment evidence.

## Configuration and deployment data locations

`cloud_env/` is for configuration only: maintained environment intent,
overrides, and secret-free configuration derived deterministically from those
inputs. Do not store deployment-generated operational data there.

All local environment deployment data must use
`~/.config/rtk_cloud/<environment>/`: tokens, keys, certificates, kubeconfig,
allocated provider state, receipts, migration proofs, journals, execution locks,
deployment reports, rollback/recovery data, and temporary deployment material.
Use `0700` directories and `0600` files. Resolve the selected environment through
the common path helpers; never choose another location from the current
checkout, worktree, external disk, or OS-specific volume format.

Document each producer/consumer path in the deployment architecture and secrets
governance. Missing state is not permission to regenerate identity, fabricate a
receipt, or search another environment/worktree as a fallback. Old paths may be
read only by explicit validated migration. Synthetic test fixtures must use
isolated roots and must not write the real environment store. Online runtime
stores and independently held escrow follow their explicit design contracts.
