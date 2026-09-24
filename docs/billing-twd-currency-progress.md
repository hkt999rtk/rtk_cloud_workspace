# TWD billing currency delivery ledger

Decision: one transactional base currency, TWD. One internal `amount_minor` is
NT$1. The retail proposal converts its 2026-09-06 USD draft at the fixed
planning rate US$1 = NT$32; this is not a payment-time FX operation. USD/CNY
representation is reserved for a separately qualified future cutover. Vendor
cost benchmarks keep their source currency.

Update each row with the merged PR, local/CI checks, deployment evidence and
completion date. A code change alone does not make a row complete.

| ID | Status | Deliverable | Required completion evidence |
| --- | --- | --- | --- |
| C01 | Complete, 2026-09-24 | Reconcile contract, Billing, Admin, Account Manager and the abandoned USD migration draft | Investigation in the TWD proposal; no USD migration published |
| C02 | Complete, 2026-09-24 | TWD retail draft and conversion ledger; USD vendor benchmarks remain labeled | Admin [PR #406](https://github.com/hkt999rtk/rtk_cloud_admin/pull/406), [CI](https://github.com/hkt999rtk/rtk_cloud_admin/actions/runs/35995238265), pricing table and UI checks |
| C03 | Complete, 2026-09-24 | Contract, OpenAPI, JSON Schema and money-unit rules | Contracts [PR #169](https://github.com/hkt999rtk/rtk_cloud_contracts_doc/pull/169); workspace spec checks passed |
| C04 | Complete, 2026-09-24 | Central currency representation with TWD-only transaction policy | Billing [PR #19](https://github.com/hkt999rtk/rtk_billing/pull/19), [CI](https://github.com/hkt999rtk/rtk_billing/actions/runs/35995218357); TWD/USD/CNY integer arithmetic tests |
| C05 | Complete, 2026-09-24 | Terminal migration; old-data and fresh-schema parity | Billing PR #19; isolated PostgreSQL 059→060→061/re-run, old NT$200 invoice preserved, fresh/upgraded schema catalog parity |
| C06 | Complete, 2026-09-24 | Historical pricing interval selection and guarded activation | Billing PR #19; PostgreSQL historical selection, overlap, gap and issued-invoice conflict tests |
| C07 | Complete, 2026-09-24 | Account, invoice, payment and handoff currency consistency | Billing PR #19, Account Manager [PR #339](https://github.com/hkt999rtk/rtk_account_manager/pull/339), Admin PR #406; [Account Manager CI](https://github.com/hkt999rtk/rtk_account_manager/actions/runs/35996193476) and cross-service tests passed |
| C08 | Complete, 2026-09-24 | TWD Admin rate card, real-money display and tax-neutral sample | Admin PR #406 and [PR #407](https://github.com/hkt999rtk/rtk_cloud_admin/pull/407), [follow-up CI](https://github.com/hkt999rtk/rtk_cloud_admin/actions/runs/35998794324); 207 web tests and targeted desktop/mobile invoice cases passed |
| C09 | Complete, 2026-09-24 | Unit, PostgreSQL, cross-service and UI regression matrix | Three clean local pre-PR gates; leaf CI and workspace [PR #496](https://github.com/hkt999rtk/rtk_cloud_workspace/pull/496) CI passed; staging owner-authorized Billing summary, four MQTT lines, idempotent close and restart checks passed. The separate Console Test Lab read route is listed in [staging evidence](billing-twd-staging-evidence-20260924.md). |
| C10 | Complete for the TWD scope, 2026-09-24 | Isolated migration and restore rehearsal; staging TWD MQTT plan and end-to-end verification | Restored Billing/Account Manager backups in isolated PostgreSQL; live migrations 060/061/085, five-rate version 2026092401, four MQTT invoice lines, unchanged old invoices, seven restarted workloads and post-restart health passed. See [staging evidence](billing-twd-staging-evidence-20260924.md). Production remains outside scope. |
| C11 | In progress | Test catalog, release notes, schema difference and final progress reconciliation | The versioned [staging evidence](billing-twd-staging-evidence-20260924.md), release notes and this ledger are updated; final documentation PR/CI/merge still required. |

Staging activation gate: prove no issued invoice is repriced; verify the
affected period's start and the active/retired pricing intervals. The plan
must contain MQTT `publish_count` and `delivery_count` at NT$32 per million
requests (stored as price 32, scale 6) and `publish_bytes` and
`delivery_bytes` at zero per byte. Do not activate draft-only Shadow, TURN,
storage, OTA, log or API rates. Production is outside this delivery.

Future USD/CNY cutover requires a new rate version and effective date, account
and ledger migration/closure, tax and provider qualification, historical TWD
snapshot preservation, contract version and dedicated end-to-end tests. Do not
turn on another currency by editing a constant alone.

Local integration gate: `pre-pr --base origin/main` passed three times on
2026-09-24, including the final clean run after all leaf revisions merged.
It covered Go, PostgreSQL-backed service tests, Admin web tests, desktop/mobile
UI and spec inventories. Workspace PR #496 and all required checks passed.
The staging qualification evidence is recorded separately.

Merged source revisions for the coordinated release: contracts `4ea7306`,
Billing `f1614c0`, Account Manager `a8f6bf5`, and Cloud Admin `62e10df`.
Workspace PR #496 pins these exact commits. The Billing image-publication
repair is [PR #20](https://github.com/hkt999rtk/rtk_billing/pull/20)
and [PR #21](https://github.com/hkt999rtk/rtk_billing/pull/21);
the canonical main-branch image releases and staging digest pulls passed as
separate gates from PR CI.
