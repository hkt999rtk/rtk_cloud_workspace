# Feature Coverage Governance

Feature coverage and code coverage are independent qualification signals.

- **Feature coverage** uses active, required acceptance requirements in
  `tests/catalog.yaml` as its denominator. A requirement is covered only by an
  explicit PASS assertion from a qualifying integration, UI, E2E, or live case.
- **Code coverage** continues to report statement, branch, function, package,
  and differential metrics. It is supporting evidence and never increases the
  feature-coverage numerator.

## Catalog contract

Schema v3 defines stable features, their owners and risk, implementation change
paths, public product surfaces, and atomic requirements. Every active product
case declares `verifies`; unit and service cases may support a requirement but
cannot close it by themselves.

New API routes, UI routes/actions, SDK public APIs, and operator workflows must
match a feature change path. Governance and shared-runner changes conservatively
select every active feature. A temporary surface exclusion remains mapped to a
feature and must include a reason, a known owner, and an unexpired date.

## Evidence contract

Normalized manifests use `rtk-cloud-feature-coverage-evidence/v2` and bind each
case to its run, environment, target, completion time, workspace/submodule
commits, requirement assertions, and SHA-256 evidence files. The aggregator
rejects missing assertions, non-PASS states, target/environment mismatches,
commit mismatches, stale evidence, altered evidence files, and any evidence type
declared by the requirement but absent from its assertion.

UI, feature, deterministic E2E, and live runners emit or adapt to v2. The
onboarding `test-live` runner, operator-held signup email flow, and load-owner
activation flow also write redacted results, JUnit, report, and v2 evidence
manifests. Legacy one-to-one manifests can be adapted; a case that verifies
multiple requirements must emit an explicit v2 assertion for every requirement.

Runtime coverage collects onboarding, Shadow, Video/TURN, Clip, and desktop/
mobile manifests into one observe-mode feature report. Operator-held signup and
load-owner activation evidence uses the same contract and can be supplied to a
main or release aggregate without placing IMAP credentials in CI.

## Gates

Use:

```sh
go run ./scripts/go/rtk-cloud -- test-feature-coverage audit
go run ./scripts/go/rtk-cloud -- test-feature-coverage select --base <commit>
go run ./scripts/go/rtk-cloud -- test-feature-coverage check --mode pr --evidence <paths>
go run ./scripts/go/rtk-cloud -- test-feature-coverage record \
  --test-id <live-test-id> --run-id <run-id> --environment staging \
  --started-at <RFC3339> --completed-at <RFC3339> --output-dir <evidence-dir>
```

- PR mode evaluates selected deterministic PR requirements. Live requirements
  are `DEFERRED_LIVE`, receive no PASS credit, and remain visible.
- Main and release modes evaluate all selected required requirements.
- Scheduled critical live evidence expires after 36 hours.
- Operator-held live evidence expires after 7 days.

`FEATURE_QUALIFICATION_MODE` remains `observe` during rollout. CI publishes the
gap report without blocking until the deterministic critical gaps are closed;
only then should the repository variable move to `required`.
