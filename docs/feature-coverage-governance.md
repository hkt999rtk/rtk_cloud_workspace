# Spec-Driven Feature Coverage Governance

Feature coverage and code coverage are independent qualification signals.

- **Feature coverage** uses atomic requirements parsed from registered
  canonical, normative, and approved specifications as its only denominator.
- **Code coverage** reports statement, branch, function, package, and
  differential metrics. It is supporting evidence and never increases the
  feature-coverage numerator.

The traceability chain is:

```text
registered spec source
  -> feature ID
  -> requirement ID + revision
  -> catalog Test ID
  -> per-requirement assertion
  -> SHA-256 evidence
```

## Sources of truth

[`tests/spec-sources.yaml`](../tests/spec-sources.yaml) is the allow-list of
legal source documents, their parser, owner, and authority. It does not contain
product requirements. Feature and requirement IDs live in the original
Markdown specifications; public OpenAPI operations use `operationId`,
`x-rtk-feature-id`, and `x-rtk-requirement-ids`.

Authority order is canonical contract, service/SDK source-of-truth, approved
design, then draft/proposed material. Canonical, normative, and approved
requirements enter the required denominator. Draft and proposed requirements
are shown as planned gaps without changing the numerator or denominator.
Documents without explicit status are `review_required`.

Only the root `repos/rtk_cloud_contracts_doc` checkout is scanned as the
canonical contract source. Nested pinned copies are not counted again.
Duplicate IDs or conflicting definitions are reported as `SPEC_DRIFT`.

Run:

```sh
go run ./scripts/go/rtk-cloud -- test-spec-inventory check --mode observe
go run ./scripts/go/rtk-cloud -- test-spec-inventory render
go run ./scripts/go/rtk-cloud -- test-spec-impact --base <commit> --head HEAD
```

The generated cross-reference is
[`docs/spec-test-traceability.md`](spec-test-traceability.md). The impact
command classifies added, modified, deprecated, and removed requirements.
Removal without a prior deprecated lifecycle record fails.

An intentional requirement-ID rename is recorded on the replacement
requirement with `renamed_from_revision`, set to the full SHA-256 revision of
the prior requirement. The impact gate accepts it only as a one-to-one rename
within the same feature; missing, stale, or ambiguous revision links remain an
illegal removal. This preserves lifecycle auditability without retaining the
retired identifier as a live requirement or compatibility alias.

## Catalog contract

[`tests/catalog.yaml`](../tests/catalog.yaml) schema v4 stores Test IDs,
runners, environments, targets, evidence types, and `verifies` mappings. It no
longer defines features or requirements. A `verifies` reference must resolve to
the live spec inventory. Test names, Playwright files, workflows, runners, and
current implementation behavior cannot create product requirements.

New API operations, UI actions, SDK public APIs, and operator workflows must be
mapped to a spec feature and atomic requirement. A new required requirement
without qualifying product-level proof is `MISSING_TEST`. Unit and service
tests may support a requirement but cannot close it.

During migration, `FEATURE_QUALIFICATION_MODE=observe` allows the catalog and
inventory to load a newly discovered requirement before its qualifying test is
implemented. The feature-coverage report must show that requirement as
`MISSING`; it receives no PASS credit. With
`FEATURE_QUALIFICATION_MODE=required`, the same missing high-level proof is a
catalog and gate error. Invalid qualification-mode values are always rejected.

## Evidence contract

Normalized manifests use `rtk-cloud-feature-coverage-evidence/v3`. Every
per-requirement assertion carries the Requirement ID, current revision digest,
spec source reference, status, assertion results, and SHA-256 evidence files.
The manifest also binds the run, environment, target, timestamps, workspace,
canonical-spec, service, UI, and test-harness commits.

The aggregator rejects missing assertions, SKIP/INCOMPLETE states,
target/environment mismatch, commit mismatch, expired evidence, modified
evidence files, or a requirement revision mismatch. A prior PASS against an old
revision becomes `STALE_SPEC`.

Canonical authorization integration evidence is produced with targeted,
named PostgreSQL and Video Cloud tests:

```sh
go run ./scripts/go/rtk-cloud -- test-services \
  --repo rtk_account_manager,rtk_video_cloud \
  --qualification-only \
  --qualification-output-dir <evidence-dir>
```

The runner rejects skipped tests and emits an explicit assertion map for every
Requirement referenced by each `INT-*` case. A whole service-suite PASS is not
expanded into Requirement PASS results.

## Gates and rollout

Use:

```sh
go run ./scripts/go/rtk-cloud -- test-feature-coverage audit
go run ./scripts/go/rtk-cloud -- test-feature-coverage select --base <commit>
go run ./scripts/go/rtk-cloud -- test-feature-coverage check \
  --mode pr --evidence <paths>
go run ./scripts/go/rtk-cloud -- test-feature-coverage record \
  --test-id <live-test-id> --run-id <run-id> --environment staging \
  --started-at <RFC3339> --completed-at <RFC3339> --output-dir <evidence-dir>
```

- PR mode evaluates selected deterministic requirements.
- Main mode evaluates deterministic PR and main requirements.
- Scheduled mode evaluates PR, main, and scheduled requirements, but defers
  operator-held release evidence.
- Release mode evaluates every selected required requirement, including
  operator-held live evidence.
- Requirements deferred by the active gate remain visible and receive no PASS
  credit.
- Scheduled critical evidence expires after 36 hours.
- Operator-held live evidence expires after 7 days.

`FEATURE_QUALIFICATION_MODE` remains `observe` while migration gaps exist.
Observe mode publishes inventory, spec impact, and coverage artifacts without
turning gaps into false PASS results. It may change to `required` only when
orphan normative requirements, catalog-only requirements, unmapped public
OpenAPI operations, duplicate/conflicting IDs, and critical deterministic
evidence gaps are all zero.
