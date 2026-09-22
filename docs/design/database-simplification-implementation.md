# Database simplification implementation

Status: in progress. No shared environment has been modified.

Latest user scope update: use the new ER document on main as the editing baseline. Main commit `8d66b65c` was fast-forwarded into the isolated workspace; the atlas and index are now regenerated from the simplified design with the existing layout and navigation.

## Delivery checklist

- [ ] A: source inventory, old-schema fixtures, ER DDL handling and live catalog parity
- [ ] B: three duplicate indexes and seven retired tables
- [ ] C: retired tenant identities and unified audit storage
- [ ] D: authoritative OTA columns, revision checks and atomic events
- [ ] E: canonical OTA only across server, Admin, SDKs and contracts
- [ ] Full local gates, required CI, leaf merges and integration revisions
- [ ] Coordinated dev validation (rollback rehearsal waived by user)

## Boundaries

Dev business data may be rebuilt. Staging and production are excluded. Rollback rehearsal is not required under the latest user instruction. PKI keys, roots and infrastructure credentials are not disposable. Billing and PKI models are unchanged. All changes are made in isolated worktrees; the original checkout remains intact.

The pre-change source schema is in `tests/fixtures/database-simplification/schema-before.json`; it is a static baseline, not a live deployment inventory.

## Implementation evidence (local, 2026-09-22)

All work is in the isolated `codex/database-simplification` worktrees. No shared
dev, staging or production data or deployment has been changed. No PR has yet
been published or merged; full gates, CI and coordinated dev validation remain outstanding.

- Account Manager migrations 083/084 consolidate audit domains and retire tenant
  identity storage after reconciliation. Offline check/apply/verify and startup
  compatibility checks are implemented. Scoped tenant-cache retirement is
  available through `user-cache retire-tenant-identity`. Unused tenant-scoped store
  interfaces and activation mail rendering are removed; queued retired activation
  messages expire during migration while global login messages remain deliverable.
- Video Cloud retires seven tables and legacy firmware runtime routes. OTA core
  columns and internal revisions are authoritative; event/state writes are
  atomic. Parent locks prohibit late deployment creation after cancellation.
  Handoff cancels campaigns before deployments and advances revisions.
- Admin uses offline migration 11 for four table retirements and the redundant
  readiness index. Unknown dependent objects and populated retired tables block
  cleanup. Normal startup rejects an old schema or resurrected retired tables.
  The OTA dashboard reads only Product OTA and preserves unavailable responses.
- SDKs remove model-keyed methods/enums/report helpers across Go, JavaScript,
  Android, Swift and C/C++. Shared Product OTA event fixtures and conflict tests
  replace legacy-only tests. Native device examples compile against the updated
  surface. Contract and service OpenAPI inventories remove retired routes.
- Runtime images now include required offline maintenance/cache commands.

Executed local evidence:

- Real PostgreSQL tests cover audit row reconciliation, historical actors,
  collisions/rollback, identity blockers, OTA conversion, CAS writers, atomic
  event rollback/deduplication, handoff and late-creation cancellation.
- Fresh versus upgraded catalog equality passes for Account Manager and Video
  Cloud (PostgreSQL) and Admin (SQLite).
- `check_database_er_catalog.py` compares the actual initialized catalogs in
  `tests/fixtures/database-simplification/*-catalog-after.json` against extraction:
  Account Manager 88, Video Cloud including PKI 64, Admin 12 tables. These are
  catalog observations, not a performance result or a table-count acceptance
  target. The saved `schema-before.json` remains a static predecessor baseline.
- The parser now distinguishes SQLite text-primary-key nullability and rejects
  unsupported persistent table/index DDL. Temporary migration tables are
  deliberately excluded. HTML tests validate the current simplified schema; regeneration checks write
  only to temporary directories.
- Go SDK tests, JavaScript 40 tests, Android unit tests, Swift 86 tests (2 external
  tests skipped), native 14 executed tests (1 live-cloud test skipped), and 115
  SDK tooling tests pass. These are not shared-dev E2E evidence.
- Admin full Go suite and Video Cloud focused PostgreSQL/HTTP/workflow/Product
  OTA suites pass. Account Manager full Go suite passes with real PostgreSQL configured.
- Video Cloud OpenAPI drift check passes after route retirement.

Outstanding before delivery:

1. Complete remaining runtime/document/test inventories and service gates,
   including PostgreSQL/MQTT, API contract fidelity, mobile/UI and client fixtures.
2. Qualify retained legacy-data disposition: current cleanup blocks all nonempty
   old firmware tables; it does not yet implement a trusted ownership/integrity
   mapping for historical rows. Staging/prod data has not been inspected.
3. Keep catalog captures aligned with any subsequent schema changes; independent
   predecessor and current catalogs and index query-plan evidence are now captured.
4. Freshly fetch all repositories before publishing, complete local pre-PR gates,
   publish leaf PRs and required CI, then integrate exact merged revisions.
5. Inventory scoped dev data, perform the coordinated dev cutover and validate
   consistency, service restart and worker recovery. The user explicitly waived
   backup/rollback rehearsal as a delivery condition on 2026-09-22.
6. Keep the regenerated ER document aligned with the final merged leaf revisions, following the latest user instruction.

Additional local verification:

- Video Cloud governed PR coverage with disposable PostgreSQL and EMQX passes
  (run `local-video-pr-20260922092052-25328`, overall 72.45%). The first full run
  identified overly broad schema enforcement on PKI-only connections; those
  connections now validate their own registry without requiring OTA tables.
- Account Manager `go test ./...` passes with disposable PostgreSQL configured.
- Full Admin desktop/mobile UI evidence validates 141 desktop and 76 mobile
  required cases (run `db-simplification-ui`). These use local upstream fixtures.
- Admin migration 11 expires prior login sessions once; normal restarts retain
  newly established sessions. Video maintenance locks every retired table before
  checking rows, preventing a check/drop race with legacy writers.
- The updated ER atlas contains 224 entities and 235 declared relationships.
  Catalog parity and 13 parser/navigation tests pass. Desktop OTA and mobile
  audit catalog rendering and fragment navigation were inspected.
- Dev workload/PVC inventory was read using environment-local kubeconfig;
  no workloads, data, certificates, or credentials were changed.

- Independently initialized predecessor catalogs now cover all three changed
  services: Account Manager 92 tables, Video Cloud/PKI 71, Admin 16. Old Admin
  TEXT-primary-key nullability exposed a static-baseline error, corrected by
  the parser without altering SQLite schema. The fixture README records it.
- A complete old Video Cloud database with populated OTA rows was dumped,
  upgraded, verified, restored, and restarted with its old initializer. All 71
  catalog tables and the old JSON/column discrepancy were restored exactly.
- The repository recovery adapter's real PostgreSQL and Redis round trips pass.
  This is retained evidence only; further rollback rehearsal is not a delivery
  gate under the latest user instruction. Shared-dev cutover remains pending.

Latest integration check:

- The complete local gate at `91a5e31d` passed Video Cloud PostgreSQL/MQTT,
  workspace tooling, Account Manager, Go SDK and DNS coverage. It stopped at the
  Admin store package ratchet (79.17% below 80%); later UI stages did not run in
  this attempt. New real SQLite tests cover failed cleanup rollback/retry,
  future schemas, restored redundant indexes and unavailable databases. Admin's
  full Go suite now passes with store coverage at 80.4%; the full gate must rerun.
- Real PostgreSQL cross-service tests pass for registered service/factory
  enrollment, Billing cloud creation, mTLS catalog/product/production-run
  registration, cloud deletion and ownership handoff. These are isolated
  integration tests, not shared-dev acceptance.
- Dev read-only inventory found source Account Manager schema 078, no audit ID
  collisions, no remaining legacy tenant identities/tokens and no rows in the
  seven retired Video Cloud tables. Historical migrations through 082 must run
  before the simplification maintenance check. Existing cloud/device identities
  can be preserved; shared-dev mutation remains pending.
- A dev-only Account Manager image built successfully, but publishing was
  rejected because the existing registry credential lacks write scope. No new
  image was deployed. Use canonical CI-published packages after verified merges.
