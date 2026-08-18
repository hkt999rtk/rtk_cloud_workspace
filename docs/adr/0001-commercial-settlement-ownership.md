# ADR 0001: Commercial Settlement Ownership And Provider Boundary

Status: accepted

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

1. The independent `rtk_billing` service owns the commercial settlement domain:
   commercial account, immutable balance ledger, payment method, customer
   consent, automatic top-up policy, payment intent/attempt, webhook inbox, and
   reconciliation worker.
2. Account Manager owns organization identity, membership, and RBAC only.
   Cloud Admin resolves those facts, then calls Billing through a dedicated
   service credential and explicit organization, actor, and permission context.
   The services do not share tables or database foreign keys.
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
- organization authorization remains reusable without making Account Manager a
  monetary database;
- provider outages do not corrupt usage or identity domains;
- pricing, wallet, payment, invoice, and billing access have one runtime owner.

Tradeoffs:

- an additional service, database, deployment, credential, and operational
  ownership boundary must be maintained;
- provider-hosted setup and merchant capability approval constrain UI and
  rollout timing;
- reconciliation, support tooling, finance operations, consent, and dispute
  procedures are first-class implementation work, not optional follow-ups.

Rejected alternatives:

- **Put payment in Video Cloud.** This couples settlement to one metered service
  and makes future non-video usage or provider changes harder.
- **Store payment state in Cloud Admin.** A UI/BFF cannot be the durable monetary
  source of truth.
- **Keep Billing in Account Manager.** This couples identity availability and
  schema lifecycle to all monetary operations and creates duplicate ownership
  once pricing and invoicing are introduced. The temporary implementation was
  useful to establish semantics, but is superseded by the independent service.
- **Treat a successful callback as a mutable balance assignment.** This cannot
  provide append-only audit, replay safety, or exactly-once credit.
- **Use NewebPay periodic payment directly as the product model.** A fixed
  schedule does not represent threshold-triggered charging and would leak one
  provider's capability into the product contract.

## Review And Promotion

This ADR was promoted after:

- the pricing, wallet, payment, invoice, and billing-activity contracts were
  implemented and exercised with the virtual provider;
- the dedicated service and database boundary was accepted as the target
  architecture; and
- Cloud Admin adopted a separate Billing service client.

Acceptance of this ownership ADR does not enable NewebPay. Merchant capability,
finance/legal, consent, refund, chargeback, and live-sandbox gates remain
separate release prerequisites.
