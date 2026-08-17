# Payment Simulator Design And Qualification Contract

Status: active for local, CI, and shared staging qualification only.

Owners: `rtk_billing` for payment behavior and persistence;
`rtk_cloud_workspace` for Kubernetes, DNS, test orchestration, and evidence;
`rtk_cloud_admin` for the customer UI and browser evidence.

## Approved Product Rules

The first qualification provider is a virtual payment simulator. It never
connects to NewebPay and never moves real money.

| Rule | Approved value |
| --- | --- |
| Currency | `TWD` |
| Monetary representation | One `amount_minor` unit is NT$1; TWD has zero fractional digits. |
| Trigger threshold | Trigger only when available balance is strictly below NT$300. |
| Automatic top-up amount | NT$300 per qualifying crossing. |
| Daily amount limit | Customer configurable; default NT$1,000. |
| Daily attempt limit | Customer configurable; default 2. |
| Daily limit timezone | `Asia/Taipei`, reset at local midnight. |
| Cooldown | 3,600 seconds by default. |
| Consecutive failures | Disable automatic top-up after 3 conclusive failures. |

A successful automatic charge resets the consecutive-failure counter. A hard
decline or a terminal failed/canceled result increments it. An ambiguous
timeout remains `unknown` until reconciliation and does not increment the
counter while unresolved. `requires_action` pauses the policy and puts the
account into `attention_required`, but is not counted as a conclusive failure.
At the third conclusive failure the policy becomes `enabled=false` and
`armed=false`; an owner must explicitly accept current consent and re-enable it.

The existing payment schema has not been released with production monetary
data. The TWD zero-decimal correction therefore changes the contract without a
data conversion. Any non-production database created with the older
cent-scaled fixtures must be recreated before simulator qualification.

## Architecture

The simulator is a dedicated non-production process built from the Billing
repository and image. It is not embedded into Cloud Admin and it does
not bypass the provider abstraction.

```text
Cloud Admin
  -> Account Manager authentication and tenant authorization
  -> Billing payment API
       -> simulator PaymentProvider client
            -> payment-simulator internal service
                 -> PostgreSQL simulator sessions/outbox
                 -> hosted test payment page
                 -> signed setup callback to Billing

payment-worker
  -> simulator PaymentProvider client
       -> charge/query/refund simulator endpoints
```

The Billing API and payment worker use the same provider adapter. The
simulator process owns only synthetic provider state; Billing remains
the source of truth for consent, payment methods, intents, attempts, balance,
ledger entries, and reconciliation.

## Hostnames And Service Discovery

Public staging origin:

```text
PAYMENT_SIMULATOR_DOMAIN=payment-simulator.video-cloud-staging.realtekconnect.com
BILLING_DOMAIN=billing.video-cloud-staging.realtekconnect.com
PAYMENT_SIMULATOR_PUBLIC_BASE_URL=https://payment-simulator.video-cloud-staging.realtekconnect.com
```

Kubernetes callers use service discovery rather than the public ingress:

```text
PAYMENT_SIMULATOR_BASE_URL=http://payment-simulator.<stack>-billing.svc.cluster.local:80
```

The simulator runs in the Billing namespace. The public ingress and
TLS certificate include the approved simulator hostname. DNS follows the
existing staging ingress/load-balancer path.

## Configuration And Production Guard

Billing API and payment worker:

```text
PAYMENT_SIMULATOR_ENABLED=false
PAYMENT_SIMULATOR_RUN_ID=<run-scoped-id>
PAYMENT_SIMULATOR_BASE_URL=
PAYMENT_SIMULATOR_PUBLIC_BASE_URL=
PAYMENT_SIMULATOR_SHARED_SECRET=<secret>
PAYMENT_SIMULATOR_CALLBACK_SECRET=<secret>
PAYMENT_SIMULATOR_SCENARIO=success
PAYMENT_REFERENCE_ENCRYPTION_KEY=<secret>
PAYMENT_WORKER_ENABLED=false
```

Simulator process:

```text
PAYMENT_SIMULATOR_ENABLED=false
PAYMENT_SIMULATOR_RUN_ID=<run-scoped-id>
PAYMENT_SIMULATOR_PUBLIC_BASE_URL=
PAYMENT_SIMULATOR_CALLBACK_URL=
PAYMENT_SIMULATOR_SHARED_SECRET=<secret>
PAYMENT_SIMULATOR_CALLBACK_SECRET=<secret>
```

Startup must fail when the simulator is enabled and the normalized application
environment is `production`. It must also fail for a non-HTTPS public origin,
an origin containing credentials/query/fragment, a missing shared secret, or a
missing reference-encryption key. NewebPay and simulator providers are mutually
exclusive for a process.

Secrets are injected through Kubernetes Secrets and never stored in tracked
environment files. Logs and reports may contain run ID, internal setup ID,
synthetic scenario, normalized state, and correlation ID, but not bearer
secrets, setup tokens, hosted URLs with tokens, opaque method references, or
callback signatures.

## Simulator Protocol

Internal endpoints require a constant-time verified HMAC signature and accept
only JSON with bounded bodies and strict unknown-field rejection.

| Endpoint | Purpose |
| --- | --- |
| `POST /internal/v1/setup-sessions` | Idempotently create a synthetic hosted setup session. |
| `POST /internal/v1/charges` | Execute the selected synthetic charge outcome. |
| `POST /internal/v1/queries` | Return the durable normalized outcome for reconciliation. |
| `POST /internal/v1/refunds` | Produce a synthetic refund result when the scenario permits it. |
| `GET /internal/v1/health` | Readiness without secret or transaction details. |

Public endpoints use an unguessable, single-purpose, expiring token:

| Endpoint | Purpose |
| --- | --- |
| `GET /setup/{token}` | Render the hosted test payment page. |
| `POST /setup/{token}/complete` | Select and confirm an allowed synthetic setup/charge scenario. |

The hosted page contains no PAN, CVV, expiry, cardholder-name, or arbitrary text
fields. It always displays `TEST PAYMENT - NO REAL CHARGE`. Phase-one scenarios
are enumerated server-side:

```text
success
declined
requires_action
temporary_error
unknown
```

CI chooses a scenario in the authenticated setup request. Local and staging
may allow a tester to choose from the same fixed list on the hosted page. The
page cannot submit provider codes, callback targets, amounts, account IDs, or
arbitrary URLs.

## Persistence And Idempotency

Simulator tables are separate from the monetary ledger:

- `payment_simulator_setup_sessions`: run ID, Billing setup reference,
  idempotency key, public-token hash, scenario, state, callback attempts,
  expiry, and safe synthetic references;
- `payment_simulator_operations`: run ID, charge/refund operation, merchant order
  reference, amount, currency, selected scenario, normalized state, synthetic
  provider transaction reference, and timestamps.

Raw public tokens and shared secrets are never persisted. Setup idempotency is
scoped by run ID, account, and client key. Charge/refund idempotency is scoped
by run ID, operation, and merchant order reference. Replayed requests return the original synthetic result;
semantic conflicts return a stable conflict and create no new session or
transaction.

Every synthetic row carries the validated run ID supplied by the provider
client. Run IDs use only letters, digits, dot, underscore, and hyphen and are
included in uniqueness boundaries and safe operation evidence.

Expired simulator setup sessions and operations are pruned opportunistically
from authenticated simulator traffic after the configured retention period
(seven days by default). Monetary and consent evidence remains subject to the
Billing retention contract.

## Setup Completion

`CreateSetup` initially returns `requires_action` and the hosted URL. Account
Manager persists consent, a pending method, setup idempotency, and the hosted
URL SHA-256 before returning it to Cloud Admin.

When the hosted page is completed, the simulator sends a signed, bounded setup
callback containing the internal setup reference, synthetic event reference,
normalized state, and opaque synthetic customer/method references. Account
Manager verifies the signature, converges duplicate completion, encrypts opaque
references, and transitions the pending method once. Duplicate or out-of-order
callbacks converge without duplicate methods or consent records.

## Customer UI

When simulator capability is active, Cloud Admin replaces the provider-blocked
panel with a clearly marked test panel. The owner can:

1. accept versioned payment-method consent;
2. open the hosted simulator page;
3. complete an allowed synthetic scenario;
4. return to billing and observe the active safe method metadata;
5. configure threshold, amount, daily amount, and daily attempt limits;
6. see failure count, paused/attention state, and normalized intent results.

Default form values are NT$300 threshold, NT$300 top-up, NT$1,000 daily amount,
and 2 daily attempts. Inputs and API payloads use TWD integer units directly;
the UI does not multiply or divide TWD values by 100.

## Test And Evidence Contract

Permanent IDs:

| Test ID | Purpose | Targets | Evidence |
| --- | --- | --- | --- |
| `E2E-AM-SIMULATOR-001` | Hosted setup, signed callback, encrypted references, token redaction, replay, and retention cleanup. | local, CI | JSON, JUnit, logs, manifest |
| `E2E-AM-SIMULATOR-002` | Charge/query/refund scenarios and exactly-once ledger convergence. | local, CI | JSON, JUnit, logs, DB correlation |
| `E2E-AM-AUTOTOPUP-003` | NT$300 crossing, configurable daily limits, Taipei reset, and three-failure disable. | local, CI | JSON, JUnit, Markdown, DB evidence |
| `UI-CA-BILLING-002` | Simulator setup and automatic top-up behavior. | desktop, mobile | Final screenshot per target, trace/video on failure |
| `LIVE-STG-SIMULATOR-001` | Public TLS hosted page, Billing callback, worker, ledger, and cleanup. | staging | JSON, Markdown, screenshots, runtime logs |

Every case reports purpose, method, start/completion time, duration, status, and
assessment. UI success and failure evidence must contain no token, card-like
number, cookie, credential, raw callback, or opaque provider reference.

## Qualification Sequence

```text
catalog/contract checks
  -> local simulator unit and PostgreSQL integration
  -> desktop/mobile fixture UI
  -> isolated staging deployment
  -> hosted setup canary
  -> success/decline/action/timeout/replay scenarios
  -> ledger and runtime correlation
  -> screenshot/redaction verification
  -> run-scoped cleanup
```

The simulator remains non-production even after qualification. Replacing it
with NewebPay changes only the provider adapter and provider-specific evidence;
the Billing monetary model, policy, ledger, Cloud Admin BFF, Test IDs,
and report schema remain provider-neutral.

The deployed qualification is plan-only by default. `test-payment --profile
staging-live` requires both the exact staging stack confirmation and a second
confirmation matching the dedicated test organization. It reads the access
token only from a mode-`0600` file, captures desktop/mobile hosted-page
screenshots, and always attempts to disable the created policy and revoke the
synthetic method before emitting its cleanup and redaction reports.
With `--bootstrap-test-org`, the runner reads platform-admin credentials from
the LKE runtime secret, creates or reuses only the fixed `RTK Payment Simulator
Qualification` organization, and keeps the resulting access token in memory.
Repeated runs accept prior disabled policies and revoked synthetic methods but
fail closed if the dedicated organization contains active payment state.
