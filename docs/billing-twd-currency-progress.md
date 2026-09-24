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
| C02 | In progress | TWD retail draft and conversion ledger; USD vendor benchmarks remain labeled | Admin PR, reviewed rate table, UI check |
| C03 | In progress | Contract, OpenAPI, JSON Schema and money-unit rules | Contracts PR and spec checks |
| C04 | In progress | Central currency representation with TWD-only transaction policy | Billing PR and USD/CNY representation tests |
| C05 | In progress | Terminal migration; old-data and fresh-schema parity | Billing PR, PostgreSQL migration/re-run evidence |
| C06 | In progress | Historical pricing interval selection and guarded activation | Billing PR, old/new-period and conflict tests |
| C07 | In progress | Account, invoice, payment and handoff currency consistency | Billing, Account Manager and Admin PRs; cross-service checks |
| C08 | In progress | TWD Admin rate card, real-money display and tax-neutral sample | Admin PR, desktop and mobile UI checks |
| C09 | In progress | Unit, PostgreSQL, cross-service and UI regression matrix | Local pre-PR and required CI results |
| C10 | Not started | Isolated migration and restore rehearsal; staging TWD MQTT plan and end-to-end verification | Sanitized protected-environment GO report, precheck, catalog, invoice and restart evidence |
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
