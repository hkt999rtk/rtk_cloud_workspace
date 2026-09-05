# AMB82 MINI board integration

The Chip & SDK catalog can describe several boards per chipset and share an
SDK release across those boards. AMB82 MINI is the first board page, under the
AmebaPRO2 platform and RTL8735B IC, with an original interactive appearance
model including the F37 camera, flex cable and external antenna.

## Repository changes

- [Contracts #151](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/151): optional board schema, resource governance and canonical AMB82 MINI content.
- [Account Manager #324](https://github.com/hkt999rtk/rtk_account_manager/pull/324): board validation, JSON snapshot preservation and compatible API fields.
- [Cloud Admin #362](https://github.com/hkt999rtk/rtk_cloud_admin/pull/362): board discovery/detail UI, shared SDK links, Three.js interactions, source model and assets.

The workspace registers six browser cases and five JavaScript unit cases and
regenerates the catalog, traceability and unit inventory. Existing UI integration
changes from workspace main are retained. No database migration is needed.

## Local verification

The complete `pre-pr --base origin/main --run-id amb82-pre-pr-current` gate
passed from a clean isolated checkout, including the policy matrix, Go and
JavaScript coverage, full desktop/mobile UI phases and visual baselines.

| Check | Result |
| --- | --- |
| Workspace baseline, catalog and inventories | PASS; 319 catalog cases and 224 unit cases |
| Account Manager full canonical report with PostgreSQL 16 | PASS; 81.3% total statement coverage, publication/refresh/visibility and board database/API round-trip covered |
| Cloud Admin Go suite | PASS; 79.9% raw total statement coverage |
| Cloud Admin JavaScript gates | PASS; 95.51% lines, 83.39% branches, 95.25% functions |
| Board Chromium desktop/mobile suite | PASS; 11 cases including touch pinch, pointer and keyboard selection |
| Additional installed Chrome / Playwright WebKit | PASS; 6 cases per engine |
| Provider schema and bundled asset consistency | PASS; both canonical JSON schemas and bundled copies checked |
| Model and poster budgets | PASS; 1,894,580-byte GLB and 34,420-byte WebP |

The board tests exercise direct navigation, search, shared SDK and burner links,
front/back/reset and zoom, missing models, unavailable WebGL, stale/unpublished
providers, idle rendering and repeated GPU cleanup. Existing Console, SDK,
firmware burner and login regressions pass with their original gates.

Evidence is under `.artifacts/test-runs/amb82-pre-pr-current-{go,node,ui}/` in
the validation checkout. Canonical backend evidence is in Account Manager's
`docs/test_report.md`. The first workspace baseline attempt hit the existing
one-second coverage-fixture startup timeout; its focused test passed three
times and the complete unchanged baseline subsequently passed.

## Model and rollout

See [model provenance, estimates, rebuild and preview instructions](../repos/rtk_cloud_admin/docs/amb82-mini-model.md).
The documented PCB is 60 × 37.4 mm with 2.54 mm header pitch. Heights, lens
dimensions and detailed placement are estimates. The source is original and
does not reuse the restricted community STL. The viewer identifies the asset
as an approximate appearance model and discloses hardware revision variation.

Deploy compatible Account Manager code first, then Cloud Admin and its
versioned model assets, and finally refresh, preview and publish the Provider
package. Board links use the published provider-scoped chipset ID; the local
fixture ID is only for development preview. Verify that flow in staging before
production promotion. Native Safari and Edge application checks remain release
checks; Playwright WebKit is engine coverage, not a Safari application sign-off.
