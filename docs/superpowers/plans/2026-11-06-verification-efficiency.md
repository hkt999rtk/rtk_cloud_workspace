# Verification Efficiency Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans or the already-authorized parallel implementation assignments to execute these tasks. The user has approved this scope; no additional plan approval is required.

**Goal:** Shorten local feedback and eliminate duplicate mandatory full local/CI runs while retaining every required merge gate.

**Architecture:** Add an explicit focused Go pre-PR mode alongside the unchanged full default, run policy checks early, and balance complete workspace coverage over three workers using fresh discovery. Replace a report test's unrelated full service run with a tiny real Go module; keep full CI authoritative.

**Tech Stack:** Go test JSON/coverage, existing Go CLI, GitHub Actions selectors, Markdown skill policy.

**Spec:** User-approved four improvements in the current task; maintained behavior is documented in [testing.md](../../testing.md#local-checks-before-pr-publication).

## Global Constraints

- `pre-pr --mode quick --test-run 'REGEXP' --base origin/main`; omitted mode remains full.
- Quick coverage is diagnostic; never claim complete coverage or complete GO. Required CI owns full differential/module/package/critical-ID/integration/UI gates.
- Fresh workflows, changed-path selection, and required checks must prove equivalent CI before deferring full local checks, including deployment/auth/PKI/build changes. Otherwise run full locally.
- Retain existing coverage thresholds, skip policies, branch protections, test assertions, signed lifetimes, and environment isolation.
- No measured speed claim until a comparable full run finishes. The latest supplied full-run baseline is 79.17 minutes (old remainder 78.82, CRL 33.38, cleanup 19.81); estimates are not outcomes.

## Review Focus

- A regexp matching nothing or only skipped cases must fail focused execution.
- A selected Node module must reject quick mode with the eligible presentation-only/full alternative.
- Added tests/packages and absent historical hint names must not remove or block suite membership.
- Failed, canceled, missing, or duplicate shard execution must not publish passing merged evidence.
- A passing focused report must remain distinguishable from full coverage and required CI.

## Tasks

### 1. Focused pre-PR and early policy checks

**Files:** `scripts/go/rtk-cloud/pre_pr.go`, focused pre-PR implementation/tests, and command help.

**Interface:** Quick mode takes one top-level Go regexp across selected modules, executes real `./...` focused tests with coverage, and writes `.artifacts/test-runs/<run-id>/quick/<module>/diagnostic.json` with `FOCUSED_PASS` and `full_coverage: NOT_RUN` on focused success.

- [x] Verify invalid/empty regex, no-match, all-skipped, failure, and Node-selection behavior with isolated fixtures.
- [x] Implement quick mode and move selected policy-only checks ahead of heavy full coverage; preserve full default and selected full checks.
- [x] Run focused pre-PR tests and inspect diagnostic output separation.

### 2. Balance the complete discovered suite

**Files:** `scripts/go/rtk-cloud/test_coverage_shards.go`, `scripts/go/rtk-cloud/test_coverage_shards_test.go`.

**Interface:** Discover current Go tests, allocate longest estimated duration first to three workers, record `shard-plan.json`, then merge under the existing gates. Timing hints are advisory.

- [x] Exercise new tests/packages, absent hints, same-name tests, exact selection, merge integrity, and cancellation with real isolated Go modules.
- [x] Replace the two-fixed-tests-plus-remainder grouping with duration-balanced discovery; preserve full serial comparison mode.
- [x] Run shard tests and compare serial/sharded coverage and inventory in the fixture.
- [x] Warm the instrumented build during discovery with bounded compiler parallelism, avoiding three simultaneous cold coverage compilations observed in the first CI attempt.

### 3. Isolate report plumbing verification

**Files:** `scripts/go/rtk-cloud/test_coverage_test.go`.

- [x] Replace the report test's home-load-runner execution with a tiny real Go module and governed configuration.
- [x] Verify report, summary, events, coverage, and timing evidence from the real fixture run.

### 4. Align publication policy and operator guidance

**Files:** `docs/testing.md`, `docs/testing-operations.md`, local `rtk-pr-ci-readiness/SKILL.md`, and its `references/ci-playbook.md`.

- [x] Document quick/full boundaries, dynamic discovery, pending-CI publication, and mandatory successful equivalent CI before merge.
- [x] Remove the category-based requirement to duplicate full local deployment/auth/PKI/build checks when equivalent required CI covers them.
- [x] Validate the skill with `skill-creator/scripts/quick_validate.py`, inspect documentation consistency, and run `git diff --check`.
- [x] Make required CI aggregation reject failed/missing/canceled/skipped selected prerequisites before consuming artifacts; exercise the actual workflow-wired script and script-only change selection.

## Verification status

Focused implementation checks and skill validation passed. Complete CI and whole-suite timing are pending publication; the estimates above are not a measured speedup.
