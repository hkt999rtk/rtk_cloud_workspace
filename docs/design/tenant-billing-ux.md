# Tenant Billing UX

Status: design proposal
Audience: tenant owners, tenant billing administrators, support operators, frontend and backend engineers

## User outcomes

The billing experience must let a tenant answer these questions without reading raw provider data:

1. How much balance remains, and how long is it expected to last?
2. What caused the current charges?
3. Which invoice, payment, reconciliation, and ledger records belong together?
4. Does the user need to act after a payment problem?
5. Can the user download an immutable billing document for accounting?

## Information architecture

| Route | Purpose | Primary action |
|---|---|---|
| `/console/billing` | Billing health and next action | View cost details or manage auto top-up |
| `/console/billing/invoices` | Search, filter, and export invoices | Open invoice or download PDF |
| `/console/billing/invoices/:invoiceId` | Verify charges and trace settlement | Download PDF or dispute a charge |
| `/console/billing/activity` | Understand payment and reconciliation events | Resolve an actionable failure |
| `/console/billing/payment-methods` | Manage provider-hosted payment methods | Add, replace, or revoke method |
| `/console/billing/auto-topup` | Configure automatic top-up policy | Save or disable policy |

The existing `/console/billing` implementation remains the entry point. Invoice and activity pages are separate resources; a ledger entry with reason `invoice_debit` is not an invoice.

## Screen specifications

### Billing overview

- Show available balance, account state, and last updated time.
- Show projected runway as an estimate with its calculation timestamp.
- Keep balance threshold and runway warning independent. A balance above the auto-top-up threshold must not be labelled low merely because projected runway is short.
- Show current billing-period cost by service category.
- Show auto-top-up state, threshold, amount, daily limits, payment method health, and last successful top-up.
- Show recent invoices and billing activity, with links to full lists.
- Prefer a user action such as `View cost details` over a charge action when automatic top-up is healthy.

### Invoice list

- Summary: current-period estimated cost, amount due, and next invoice date.
- Search by invoice number; filter by status, billing period, and issue date.
- Columns: invoice number, billing period, issue date, due date, total, status, actions.
- Draft invoices must be labelled estimates and must not expose an immutable PDF.
- Issued invoices can be downloaded as PDF; statement CSV export is permission-controlled.
- Paid amount and available account balance are distinct concepts.

### Invoice detail

- Header: invoice number, status, total, issue date, due date, billing period.
- Line items: service, measured usage, unit, unit price, amount, rounding rule.
- Totals: subtotal, tax, total, paid amount, remaining amount.
- Recipient: legal name, tax identifier when configured, billing address, contact.
- Traceability chain: invoice -> payment intent -> payment attempt -> reconciliation -> ledger entry.
- Show only customer-safe references. Provider payloads, secrets, card data, and internal stack traces are forbidden.
- Issued invoices are immutable. Corrections use credit/debit documents rather than mutation.

### Payment activity

- Summary counts: action required, processing, completed.
- Search and filter by date, status, event type, amount, and customer-safe reference.
- List items display normalized status, amount, payment method label, timestamp, and reference.
- Selected detail explains the result in customer language and shows an event timeline.
- `unknown` or reconciliation-pending is neither success nor failure.
- A failed top-up must state whether another attempt will occur and whether the balance changed.
- Recovery actions link to provider-hosted payment method setup; card number and CVV are never entered in RTK Cloud.

## State and action rules

| Condition | Customer state | Required UI action |
|---|---|---|
| Balance is above threshold; runway is healthy | Healthy | No urgent action |
| Balance is above threshold; runway is below recommendation | Runway warning | View usage or cost details |
| Balance is below threshold; auto top-up is eligible | Top-up processing | Show progress; avoid duplicate manual action |
| Payment attempt failed; retry is scheduled | Retrying | Show next retry time |
| Three consecutive failures disable policy | Action required | Update payment method and explicitly re-enable policy |
| Reconciliation is pending | Pending verification | Do not label pass or fail |
| Evidence or provider state is incomplete | Status unavailable | Preserve reference and offer support path |
| Invoice is draft | Estimate | No immutable PDF; explain possible changes |
| Invoice is issued and paid | Paid | Allow PDF and traceability view |

## Responsive behavior

- Desktop uses summary cards plus tables and a split list/detail payment activity view.
- Mobile uses cards, progressive disclosure, and a bottom navigation for Overview, Invoices, Activity, and Settings.
- Mobile tables become labelled cards; no horizontal table is required for primary tasks.
- Primary controls have a minimum 44 by 44 CSS pixel target.
- Critical state and action remain visible without relying only on color.

## Data dependencies

Existing APIs can support balance, ledger, payment methods, auto top-up, payment intent list, and payment attempt detail. The following contracts are still required:

- Invoice list and invoice detail.
- Immutable invoice PDF download.
- Usage/cost line-item breakdown and rounding metadata.
- Customer-safe cross-resource correlation references.
- Statement CSV export.
- Billing recipient and tax profile.
- Dispute/support linkage.

## Acceptance criteria

- Every displayed amount includes currency and uses the currency's minor-unit rules.
- Invoice line items, subtotal, tax, total, payment, and ledger effects reconcile exactly.
- No UI state infers success from aggregate or incomplete evidence.
- Desktop and mobile tests cover healthy, empty, loading, permission-denied, failed, retrying, pending reconciliation, disabled policy, draft invoice, and paid invoice states.
- Every billing UI case has a stable Test ID and final viewport screenshot evidence for desktop and mobile.
- Accessibility checks include keyboard navigation, focus visibility, labels, status announcements, contrast, and non-color status indicators.

## Design assets

- `tenant-billing-overview-desktop-v1.png`
- `tenant-billing-invoices-desktop-v1.png`
- `tenant-billing-invoice-detail-desktop-v1.png`
- `tenant-billing-payment-activity-desktop-v1.png`
- `tenant-billing-overview-mobile-v1.png`
- `tenant-billing-payment-failure-mobile-v1.png`
