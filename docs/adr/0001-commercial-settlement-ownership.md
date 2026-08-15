# ADR 0001: Commercial Settlement Ownership And Provider Boundary

Status: proposed

Date: 2026-08-15

Supersedes: none

Superseded by: none

## Context

The workspace has usage-metering contracts and durable usage ledgers, but it has
no pricing, invoice, balance, payment-method, payment-intent, or payment-provider
domain. The requested product behavior is a customer option that charges a
configured amount when the available balance becomes lower than a configured
threshold.

NewebPay is the first candidate provider. The product must not make NewebPay
field names, credentials, state codes, or fixed-period behavior part of the
public cloud contract because another provider may replace or complement it.

Account Manager already owns organization/Brand Cloud identity, memberships,
authorization, and audit. Video Cloud owns service usage facts. Cloud Admin is
an operator/customer UI and BFF, not a durable business source of truth.

## Decision

1. Account Manager owns the phase-one commercial settlement domain:
   commercial account, immutable balance ledger, payment method, customer
   consent, automatic top-up policy, payment intent/attempt, webhook inbox, and
   reconciliation worker.
2. This domain is isolated behind service-local interfaces and schema tables so
   it can later be extracted to a dedicated Billing Service.
3. Usage metering remains separate. A pricing/invoice owner converts immutable
   usage facts into authenticated idempotent debit instructions.
4. Payment providers implement a capability-aware adapter interface. Public APIs
   and database state use normalized internal vocabulary.
5. Payment success appends exactly one ledger credit. Provider callbacks,
   metrics, logs, and dashboards cannot directly set balance.
6. Automatic top-up uses a strict `balance < threshold` crossing, one open
   intent per policy generation, finite daily limits, cooldown, and no recursive
   charging when the first top-up leaves the balance below threshold.
7. PostgreSQL is the monetary source of truth and durable worker queue. Redis
   may optimize scheduling but cannot be required to reconstruct monetary state.
8. Card data is collected only by provider-hosted or provider-tokenized flows.
   Realtek systems never store PAN or CVV.
9. NewebPay automatic top-up remains disabled until the merchant is approved
   for a customer-consented merchant-initiated capability that supports the
   intended variable-time charge pattern. Fixed periodic payment alone is not
   considered sufficient.
10. Documentation, provider/legal/finance confirmation, OpenAPI, tests, sandbox
    evidence, and an allowlisted canary are sequential release gates.

## Consequences

Positive:

- payment-provider replacement does not require redesigning public resources;
- balance correctness is auditable and resilient to retries and duplicate
  callbacks;
- organization authorization and audit can be reused;
- provider outages do not corrupt usage or identity domains;
- a later Billing Service extraction has a defined seam.

Tradeoffs:

- Account Manager temporarily gains a commercial responsibility outside its
  original identity/registry scope;
- pricing and invoice ownership must still be implemented before metered usage
  can debit customer balance automatically;
- provider-hosted setup and merchant capability approval constrain UI and
  rollout timing;
- reconciliation, support tooling, finance operations, consent, and dispute
  procedures are first-class implementation work, not optional follow-ups.

Rejected alternatives:

- **Put payment in Video Cloud.** This couples settlement to one metered service
  and makes future non-video usage or provider changes harder.
- **Store payment state in Cloud Admin.** A UI/BFF cannot be the durable monetary
  source of truth.
- **Create a new Billing Service immediately.** The workspace has no existing
  commercial domain or deployment package; starting in an isolated Account
  Manager module is the shortest safe path and preserves later extraction.
- **Treat a successful callback as a mutable balance assignment.** This cannot
  provide append-only audit, replay safety, or exactly-once credit.
- **Use NewebPay periodic payment directly as the product model.** A fixed
  schedule does not represent threshold-triggered charging and would leak one
  provider's capability into the product contract.

## Review And Promotion

Promote this ADR to `accepted` only after:

- the contracts and Account Manager architecture documents are reviewed;
- finance/legal name the consent, refund, chargeback, and retention owners;
- NewebPay confirms the required merchant-initiated capability in writing; and
- service owners accept the pricing/invoice-to-debit integration boundary.
