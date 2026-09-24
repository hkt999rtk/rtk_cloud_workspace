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
| C09 | In progress | Unit, PostgreSQL, cross-service and UI regression matrix | Local pre-PR gate passed twice; Billing #19/#20/#21, Account Manager #339 and Admin #406/#407 CI passed; workspace CI and staging cross-service qualification pending |
| C10 | In progress | Isolated migration and restore rehearsal; staging TWD MQTT plan and end-to-end verification | Isolated PostgreSQL 059→060→061, re-run, schema parity and old NT$200 invoice checks passed; staging remains NO-GO pending exact CI images and final preflight, catalog, invoice and restart evidence |
| C11 | In progress | Test catalog, release notes, schema difference and final progress reconciliation | Workspace PR and linked merged leaf revisions |

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

Local integration gate: `pre-pr --base origin/main` passed on 2026-09-24 before
leaf PR publication, including Go, PostgreSQL-backed service tests, Admin web
tests, desktop/mobile UI and spec inventories. The workspace gate will be rerun
against the final merged leaf revisions before its own PR. The staging
qualification remains separate from code and CI acceptance.

Merged source revisions for the coordinated release: contracts `4ea7306`,
Billing `f1614c0`, Account Manager `a8f6bf5`, and Cloud Admin `62e10df`.
The workspace PR must pin these exact commits. The Billing image-publication
repair is [PR #20](https://github.com/hkt999rtk/rtk_billing/pull/20)
and [PR #21](https://github.com/hkt999rtk/rtk_billing/pull/21);
the canonical main-branch image release and staging digest pull remain separate
gates from its passing PR CI.
