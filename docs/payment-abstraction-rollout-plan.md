# Payment Abstraction And Automatic Top-Up Rollout Plan

Status: implemented foundation with guarded provider rollout.

Owner: `rtk_cloud_workspace` coordinates; repository ownership remains with the
service listed for each deliverable.

Last reviewed: 2026-08-15.

## Goal

Deliver a provider-neutral commercial settlement capability and a customer
option with this behavior:

```text
available balance A < threshold B
  -> charge configured amount C once
  -> credit the balance only after confirmed settlement
```

The first planned provider is NewebPay. The design must permit a second provider
without changing customer-facing balance, policy, payment-method, or intent
semantics.

Documentation was completed before production code. The provider-neutral
ledger, policy, intent, reconciliation, HTTP/BFF, and UI foundation now exists.
Existing usage metering still does not make production charging ready:
pricing/invoice, approved provider capability, sandbox qualification, and
support operations remain separate dependencies.

## Source Documents

| Source | Repository | Purpose |
| --- | --- | --- |
| `PAYMENTS_AND_BALANCE.md` | `rtk_cloud_contracts_doc` | Cross-repository product, ownership, state, security, and evidence contract. |
| `PAYMENT_ABSTRACTION_AND_AUTO_TOPUP.md` | `rtk_account_manager` | Implemented package, schema, transaction, worker, API, provider, and test design. |
| `docs/adr/0001-commercial-settlement-ownership.md` | workspace | Cross-repository ownership decision and rejected alternatives. |
| This document | workspace | Dependency graph, PR sequence, test gates, rollout, and outstanding decisions. |

Account Manager and Cloud Admin OpenAPI contracts now contain the guarded
payment routes. Executable unit, PostgreSQL integration, NewebPay contract, and
desktop/mobile UI cases are active in the central Test Catalog. Provider-hosted
E2E and live staging IDs remain deferred until their external prerequisites
exist.

## Existing Baseline

| Capability | Current owner | State |
| --- | --- | --- |
| Generic usage event and usage-fact contract | Contracts + metered services | Implemented foundation. |
| MQTT and log usage ledgers | Video Cloud / Logger paths | Implemented for metering; not money. |
| Brand Cloud identity, membership, authorization, audit | Account Manager | Implemented. |
| Cloud Admin customer/operator surface | Cloud Admin | Billing BFF/UI implemented; provider actions visibly blocked. |
| Pricing plans and versioned pricing | None | Missing. |
| Invoice generation and debit instruction | None | Missing. |
| Commercial balance ledger | Account Manager | Implemented with PostgreSQL idempotency and immutable entries. |
| Payment abstraction/provider adapter | Account Manager | Implemented; NewebPay query/webhook only and charge disabled. |
| Automatic top-up and consent | Account Manager | Implemented policy/orchestration; external charging disabled. |
| Refund/chargeback reconciliation | None | Missing. |

The usage pipeline remains valid and unchanged. It feeds a future pricing and
invoice layer; it must not call NewebPay.

## Dependency Flow

```text
Brand Cloud identity + product authorization
                    |
                    v
            commercial account
                    |
usage facts -> pricing/invoice -> debit ledger entry
                                  |
                                  v
                         threshold evaluator
                                  |
                            payment intent
                                  |
                 +----------------+----------------+
                 |                                 |
                 v                                 v
          provider adapter                  webhook/query
                 |                                 |
                 +----------------+----------------+
                                  |
                         confirmed settlement
                                  |
                         idempotent credit entry
                                  |
                  Cloud Admin read/manage surface
```

Hard dependencies for an automatic charge:

- Account Manager PostgreSQL and migrations;
- trusted Brand Cloud identity and explicit payment permissions;
- customer consent and active tokenized/provider-hosted method;
- provider credentials and approved capability;
- durable payment worker and reconciliation worker;
- HTTPS webhook callback plus authoritative query path;
- finite daily limits, cooldown, emergency disable, alerts, and support owner.

Optional optimization only:

- Redis wake-up, cache, or distributed scheduling. Monetary correctness must
  survive complete Redis loss.

## Provider Readiness: NewebPay

### Confirmed From Public Official Material

- NewebPay publishes separate foreground MPG and credit-card periodic-payment
  API manuals.
- The foreground flow uses merchant credentials, encrypted request data,
  integrity verification, return/callback URLs, transaction query, and
  cancel/close/refund operations.
- Remembered-card behavior uses a merchant-provided `TokenTerm` association.
- Agreed-card/periodic backstage capabilities require merchant enablement;
  NewebPay's application asks for merchant IDs, approved source IPs, business
  reason, payer consent records, and a payer modification mechanism.
- Token/CAU services are available only to merchants with the relevant agreed
  card or periodic-payment functions enabled.

### Must Be Confirmed Before Provider Enablement

The public periodic-payment manual is designed around scheduled fixed-period
charges. It does not by itself establish that the merchant can perform an
arbitrary-time threshold-triggered charge for amount C. The business owner must
obtain written NewebPay confirmation of:

1. the exact product/API for customer-consented variable-time top-up;
2. whether the amount may vary after the initial consent;
3. whether each charge can be fully server-initiated or may require customer
   action/3-D Secure/Passkey;
4. token setup, token lifetime, card update, and revocation behavior;
5. sandbox cases for success, decline, timeout, duplicate notification, refund,
   chargeback, and expired card;
6. source-IP and callback allowlist requirements;
7. merchant order and query retention limits;
8. settlement and refund timing.

If NewebPay only approves fixed periodic payment, ship provider-neutral manual
top-up or fixed subscription as separate capabilities. Do not label either one
as balance-triggered automatic top-up.

### Inputs Needed From The Account Owner

Secrets must be delivered through the configured secret manager, never chat,
email attachments, git, screenshots, or test reports:

- test and production `MerchantID`;
- test and production `HashKey` and `HashIV`;
- enabled product list and merchant approval evidence;
- registered callback origins and stable egress IPs;
- sandbox test cards/accounts;
- production enablement and emergency support contacts.

Business/policy inputs:

- consent copy and version owner;
- default/minimum/maximum threshold and top-up amount;
- daily attempt and amount maximums;
- refund, chargeback, dispute, and negative-balance policy;
- accounting treatment for provider fees and unsettled transactions;
- data/audit retention period;
- customer notification requirements;
- which organization roles may read or mutate payment settings.

Official references reviewed on 2026-08-15:

- [NewebPay API documents](https://www.newebpay.com/website/Page/content/download_api)
- [Foreground payment specification](https://www.newebpay.com/website/Page/download_file?name=%E7%B7%9A%E4%B8%8A%E4%BA%A4%E6%98%93%E2%94%80%E5%B9%95%E5%89%8D%E6%94%AF%E4%BB%98%E6%8A%80%E8%A1%93%E4%B8%B2%E6%8E%A5%E6%89%8B%E5%86%8A_NDNF-1.1.8.pdf)
- [Card token and CAU service](https://www.newebpay.com/website/Page/content/cau_service)

Use the API document index for the current periodic-payment manual; NewebPay's
versioned download URLs change independently of this repository.

## Delivery Workstreams

### 0. Documentation And Approval

Owners: Contracts, Account Manager, workspace architecture, finance/legal.

Deliverables:

- contract, service design, ADR, and this rollout plan;
- named pricing/invoice owner;
- provider capability decision;
- consent/refund/chargeback/retention decisions;
- security/PCI scope review;
- Test ID reservation and release-gate agreement.

Exit: documents are reviewed and ADR becomes accepted. No production code is
required for this exit.

### 1. Contract And API PR

Owners: Contracts + Account Manager.

Deliverables:

- exact OpenAPI schemas, status transitions, pagination, idempotency header,
  stable errors, and permissions;
- active Test Catalog entries for executable unit, integration, and UI cases;
- generated contract/client compatibility checks;
- guarded routes that expose safe resources while unsupported provider actions
  fail closed.

Exit: OpenAPI validation and requirement traceability pass.

### 2. Monetary Core PR

Owner: Account Manager.

Deliverables:

- migrations for accounts, ledger, consent, methods, policies, intents,
  attempts, webhook inbox, and reconciliation jobs;
- pure domain state machines and PostgreSQL repositories;
- fake provider adapter;
- read APIs, manual test adjustment fixture, and audit events;
- unit and PostgreSQL integration tests for concurrency and idempotency.

Exit: ledger reconciliation is exact, concurrent threshold crossing produces
one intent, and no external provider is called.

### 3. Provider Orchestration PR

Owner: Account Manager.

Deliverables:

- durable worker lease/retry/reconciliation;
- provider registry/capabilities and kill switch;
- fake-provider E2E for success, decline, timeout, unknown, duplicate callback,
  query reconciliation, and refund compensation;
- metrics, alerts, restricted support inspection, and artifact redaction.

Exit: fake-provider full flow passes and process-failure injection produces no
duplicate charge or credit.

### 4. NewebPay Adapter PR

Owner: Account Manager payment adapter owner.

The query, cryptography, response normalization, and verified-webhook subset is
implemented from official public documentation. Hosted setup and unattended
charge support have this prerequisite: written capability approval and sandbox
credentials.

Deliverables:

- AES/integrity implementation and official fixture tests;
- hosted/tokenized setup mapping;
- merchant-initiated charge mapping only if enabled;
- query, verified callback, cancel/refund capability mapping;
- provider field/length validation and error normalization;
- test/production configuration separation with adapter disabled by default.

Exit: sandbox adapter contract suite passes without card data or secrets in any
artifact.

### 5. Cloud Admin BFF And UI PR

Owners: Cloud Admin backend/web.

Deliverables:

- BFF proxies only Account Manager resources and session identity;
- owner-facing account, ledger, payment-method, consent, and policy UI;
- explicit warning/confirmation for automatic charges and limits;
- customer-visible `processing`, `requires_action`, `unknown`, failed, revoked,
  and attention-required states;
- desktop/mobile Playwright cases and final evidence screenshots;
- no provider secret, PAN, or CVV enters Cloud Admin logs/state.

Exit: desktop and mobile UI reports pass and screenshots are safe to retain.

### 6. Pricing/Invoice Debit Integration PR

Owner: must be assigned before implementation.

Deliverables:

- versioned prices and immutable invoice/debit references;
- authenticated service identity and debit idempotency;
- reconciliation from usage facts to invoice lines to ledger debits;
- correction/credit-note semantics;
- no direct payment-provider coupling.

Exit: repeated period calculation cannot double debit, and historical pricing
version remains auditable.

This workstream may proceed in parallel with the provider adapter after the
contract is accepted, but automatic balance behavior is not commercially useful
until it exists.

### 7. Staging, Canary, And Production Rollout

Owners: workspace release engineering, Account Manager, Cloud Admin, finance,
support.

Stages:

1. sandbox-only staging with dedicated merchant/test data;
2. observation mode: threshold decisions create reports but no provider call;
3. one allowlisted internal Brand Cloud with strict limits;
4. refund/chargeback and provider-disable drill;
5. small customer allowlist;
6. broader opt-in only after clean ledger/provider reconciliation.

Automatic top-up remains opt-in and disabled by default.

## Test Inventory

Executable permanent IDs are active in the catalog. E2E and live IDs that need
provider-hosted setup or a qualified sandbox remain deferred.

| ID | Layer | Purpose | Dependency | Evidence |
| --- | --- | --- | --- | --- |
| `UNIT-AM-BALANCE-001` | Unit | Exact append-only ledger arithmetic and compensation. | Go only | JSON, JUnit, coverage |
| `UNIT-AM-AUTOTOPUP-001` | Unit | Strict threshold, re-arm, limits, cooldown, no loop. | Go only | JSON, JUnit |
| `UNIT-AM-PAYMENT-001` | Unit | Intent state/idempotency/unknown behavior. | Go only | JSON, JUnit |
| `INT-AM-PAYMENT-001` | Integration | Concurrent debit, trigger, callback, and credit transaction safety. | PostgreSQL 16 | JSON, JUnit, DB evidence |
| `INT-AM-NEWEBPAY-001` | Integration | Provider crypto/field/error contract. | Official redacted fixtures | JSON, JUnit, logs |
| `E2E-AM-PAYMENT-001` | E2E | Hosted setup and consent without card storage. | Fake provider, then sandbox | JSON, JUnit, provider correlation |
| `E2E-AM-AUTOTOPUP-001` | E2E | Crossing to one charge and one credit. | PostgreSQL + provider | JSON, JUnit, ledger/provider evidence |
| `E2E-AM-AUTOTOPUP-002` | E2E | Timeout/duplicate/out-of-order/reconciliation safety. | PostgreSQL + provider | JSON, JUnit, logs |
| `UI-CA-BILLING-001` | UI | Owner config/status on desktop and mobile. | Account Manager + Cloud Admin + hosted sandbox | JSON, JUnit, screenshots |
| `LIVE-STG-PAYMENT-001` | Live | Dedicated staging sandbox full flow and cleanup. | LKE + NewebPay sandbox | JSON, Markdown, JUnit, logs |

Additional generated unit inventory covers every Go/JavaScript test function.
Security/regression IDs remain stable after activation.

## Evidence Layout

```text
.artifacts/test-runs/<run-id>/payments/
  results.json
  TEST_REPORT.md
  evidence-manifest.json
  account-manager/
    junit.xml
    test-events.json
    ledger-reconciliation.json
  provider/
    capability.json
    transaction-correlation.json
    webhook-reconciliation.json
  ui/
    desktop/
      evidence-manifest.json
      evidence/<test-id>.png
    mobile/
      evidence-manifest.json
      evidence/<test-id>.png
  redaction-report.json
  cleanup-report.json
```

Each case records test ID, purpose, method, start/completion time, duration,
PASS/FAIL/INCOMPLETE/BLOCKED, assessment, run ID, environment, workspace and
submodule commits, internal correlation IDs, and SHA-256 evidence digests.

Evidence must not include card number, CVV, HashKey, HashIV, cookies, bearer
tokens, hosted-session token URLs, private keys, or real customer data. UI
screenshots stop before or mask provider card-entry fields.

## CI And Release Gates

PR required gates after implementation begins:

- contract/OpenAPI validation;
- Account Manager Go unit and PostgreSQL integration coverage;
- payment inventory and permanent critical IDs pass;
- fake-provider E2E and failure injection;
- provider fixture/crypto tests where adapter code changes;
- differential coverage at the workspace policy threshold;
- secret/card-data redaction scan;
- migration up/down or forward/rollback policy validation;
- no active feature flag by default.

Sandbox/live gates are not run with arbitrary contributor code or production
credentials. They use a protected environment, reviewed commit, dedicated
sandbox merchant, serialized concurrency, explicit confirmation, and retained
redacted evidence.

## Failure Classification

| Status | Meaning |
| --- | --- |
| `PASS` | Assertions, monetary reconciliation, provider correlation, evidence, and cleanup all pass. |
| `FAIL` | Test completed and behavior or threshold is wrong. |
| `INCOMPLETE` | Provider state, evidence, commit anchor, or monetary reconciliation is inconclusive. |
| `BLOCKED` | Required merchant capability, credential, environment, consent, or approval is unavailable and no charge started. |

Only PASS may enable or expand automatic charging. Provider timeouts that leave
an intent `unknown` produce INCOMPLETE until query reconciliation resolves it.

## Rollback And Emergency Behavior

The first rollback action is a configuration/feature kill switch that blocks
new setup and charge calls. It must leave these paths available:

- account/ledger/intent reads;
- verified webhook ingestion;
- provider status query and reconciliation;
- append-only refund/chargeback corrections;
- support evidence export.

Schema rollback must never delete monetary history. Migrations that introduce
ledger tables are forward-repaired, not destructively rolled back after live
transactions exist.

## Open Decisions

| Decision | Owner | Blocks |
| --- | --- | --- |
| Name the pricing/invoice service or Account Manager subdomain owner. | Architecture/product | Automated usage debits |
| Confirm NewebPay variable-time merchant-initiated capability. | Business/NewebPay | NewebPay auto top-up adapter |
| Approve consent text and customer cancellation flow. | Legal/product | Payment-method and policy setup |
| Set threshold/top-up/daily platform limits. | Finance/product/risk | Policy validation and production |
| Define refund, chargeback, negative balance, and provider fee accounting. | Finance/support | Reconciliation and support |
| Define retention and PCI assessment scope. | Security/legal | Production release |
| Decide Brand Cloud admin delegation defaults. | Product/security | Authorization seed mapping |
| Provide stable staging/production egress IPs and callback DNS. | Platform operations | Provider allowlist/live tests |

## Completion Criteria

Documentation phase is complete when all source documents exist, links and
metadata validate, each open decision has a named owner, and the PRs are
reviewable without production code.

The guarded software foundation is complete when:

- exact OpenAPI and active Test Catalog entries match executable behavior;
- monetary core, provider abstraction, safe NewebPay query/webhook subset, and
  Cloud Admin UI are implemented;
- unit, PostgreSQL integration, adapter contract, and UI desktop/mobile cases
  pass with evidence;
- unsupported setup and charge capabilities remain visibly blocked and
  disabled.

Production automatic charging is complete only when:

- pricing/invoice debit ownership and authentication are implemented;
- deferred provider-hosted E2E and staging cases pass;
- ledger, provider, webhook, and refund/chargeback evidence reconcile;
- provider capability and finance/legal/security approvals are recorded;
- an allowlisted canary passes with no data/secret leakage or cleanup residue;
- automatic charging is still controlled by explicit opt-in and kill switches.
