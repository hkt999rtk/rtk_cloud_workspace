# TWD billing currency release notes

This release keeps TWD as the only transactional currency across Billing,
Account Manager handoff, and Cloud Admin. `amount_minor=1` remains NT$1; no
historical TWD account, invoice, ledger, or payment amount is converted.
USD/CNY precision metadata is internal preparation for a separately approved
cutover, not an enabled account, payment, or invoice currency. NewebPay remains
TWD-only, and non-TWD transaction requests are rejected.

Cloud Admin now labels the customer rate card as a **proposal** in TWD, using
the fixed planning conversion US$1 = NT$32. The illustrative monthly amount is
NT$232 before tax. Provider cost benchmarks retain their original USD labels.
Only qualified MQTT metrics may receive an active Billing rate; the proposed
Shadow, TURN, storage, OTA, log, and other API prices are not billable in this
release. Actual account balance, invoices, and tax still come from Billing.

Billing migration `061_twd_currency_policy_and_pricing_history.sql` follows
the upstream Product usage migration `060_product_usage_dimension.sql`. It
checks that existing monetary rows are TWD and adds an index for effective
pricing history. A newly activated version retires the prior active version at
the new effective date. Older unclosed periods can still select the rate that
was valid at their start; ambiguous historical intervals and uncovered gaps
fail closed. Activation rejects a cutover that would change an already issued
invoice; issued invoices remain immutable.

The database difference is limited to the already ordered Product usage
dimension migration `060` (nullable `product_id` on usage facts and invoice
lines, plus its indexes) and this release's `061` history index and TWD data
guard. No currency constraint is relaxed, no customer amount is converted, and
no business table is added or removed by the currency change. A fresh database
and an upgraded 059 fixture had identical final table, column, and index
catalogs; the fixture's issued NT$200 invoice was unchanged after re-running
the migration.

Staging activation requires a reviewed effective date, a fresh check that no
issued invoice is repriced, and rates for MQTT publish/delivery counts plus
publish/delivery bytes. Count metrics are proposed at NT$32 per million;
byte metrics carry a zero rate. Production activation is outside this release.

See [the delivery ledger](billing-twd-currency-progress.md) for the merged PRs,
test results, staging evidence, and any remaining qualification limits.
