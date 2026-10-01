# Environment DNS Naming Policy

Status: active workspace policy.

Classification: source.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-10-01.

Audience: deployment operators, service integrators, and workspace maintainers.

## Scope and authority

This document owns the relationship between environment identity, stack identity,
and public DNS names. It applies to the workspace deployment flow for `dev`,
`staging`, `prod`, and additional named environments, independently of the cloud
provider or DNS provider.

Use [the environment README](../cloud_env/README.md) to create configuration and
[Deployment Operations](deployment-operations.md) to plan, deploy, or remove it.
[DNS Adapter Architecture](dns-adapter-architecture.md) owns DNS record mutation,
convergence, DNS-01 challenges, and record ownership. Shared wire contracts remain
in `repos/rtk_cloud_contracts_doc`.

The tables below define naming intent. A name in a table does not establish that
its service is enabled or deployed. The resolved DNS plan and dated deployment
evidence identify the effective names and targets for a particular environment.

A local-only profile, such as `cloud_env/dev/local/`, may use loopback URLs and
create no public DNS records. Its local connection settings do not change the
managed dev environment's public namespace.

## Identity and inputs

| Term | Authoritative input | Meaning |
| --- | --- | --- |
| Environment | Directory name in `cloud_env/<environment>/` | Selects tracked configuration, runtime, and the environment SecretStore. |
| Stack | `CLOUD_STACK_NAME` in `environment.env` | Identifies deployment resources, ownership, confirmation, and the public DNS namespace. |
| Root domain | `CLOUD_DNS_ROOT_DOMAIN` in `environment.env` | Root zone containing the environment's managed public names. |
| Deployment location | `DEPLOYMENT_LOCATION` in `environment.env` | Logical location mapped by the deployment adapter; it does not change DNS identity. |
| DNS provider | `DNS_ADAPTER` in `deployment.env` | Provider that realizes the selected names; it does not select their spelling. |
| Production Console name | Optional `CONSOLE_DOMAIN` in prod `environment.env` | Short browser-facing Console/login hostname; supplies the derived `CLOUD_ADMIN_DOMAIN` without changing backend service names. |
| Public hostname | Derived from stack/root, or an explicit supported exception | DNS name without a scheme, port, path, query, or credentials. |
| Endpoint URL/address | Hostname plus its service's protocol and port | Client connection information; multiple protocols may share one hostname. |

Normal environments use:

```text
E = environment directory name
S = video-cloud-<E> = CLOUD_STACK_NAME
R = CLOUD_DNS_ROOT_DOMAIN
B = <S>.<R>

primary hostname = B
service hostname = <registered-prefix>.B
```

Production may explicitly assign short, memorable names to the website and
Console/login entry points. Backend service names use the same environment
formula in every environment, including prod. User-facing brand names retain
prod resource/DNS ownership even when their spelling contains no `prod` label.

`CLOUD_STACK_NAME` is explicit configuration and must match `video-cloud-<E>` in
the normal deployment flow. Do not infer an environment by splitting a hostname,
or silently use staging when an environment has been selected. A new isolated
environment uses its own directory and matching stack, for example `qa` and
`video-cloud-qa`.

Environment names and hostname labels use lowercase ASCII letters, digits, and
hyphens, with an alphanumeric first and last character. Each DNS label must fit
the 63-character limit, including the `video-cloud-` prefix; a full hostname must
fit 253 characters. Store root domains and hostname inputs without a trailing
dot, scheme, port, or path. Names must be unique within the root domain. Before
adding an environment, compare its namespace and
explicit exceptions with existing tracked environments and owned DNS records.

Cloud provider names, region codes, VM IDs, pod IDs, and release versions are not
part of the default public service name. Changes to deployment location or
infrastructure preserve the environment's public DNS identity.

## Environment namespace table

The following values are naming/configuration examples, not live availability
claims. The first three rows correspond to tracked environment configuration.

| Environment | Stack | Root domain | Base hostname | Frontend hostname |
| --- | --- | --- | --- | --- |
| `dev` | `video-cloud-dev` | `realtekconnect.com` | `video-cloud-dev.realtekconnect.com` | `frontend.video-cloud-dev.realtekconnect.com` |
| `staging` | `video-cloud-staging` | `realtekconnect.com` | `video-cloud-staging.realtekconnect.com` | `frontend.video-cloud-staging.realtekconnect.com` |
| `prod` | `video-cloud-prod` | `realtekconnect.com` | `video-cloud-prod.realtekconnect.com` | `www.realtekconnect.com`, explicitly configured |
| `qa` (new-environment example) | `video-cloud-qa` | `realtekconnect.com` | `video-cloud-qa.realtekconnect.com` | `frontend.video-cloud-qa.realtekconnect.com` |

The tracked prod website name is `www.realtekconnect.com`. Its Console defaults
to `admin.video-cloud-prod.realtekconnect.com`; a selected short name such as
`console.realtekconnect.com` can be configured with `CONSOLE_DOMAIN`. That short
Console name is an optional example, not a claim that it is currently configured
or deployed.

A customer-managed deployment substitutes its owned root domain for `R` while
retaining the same formulas. `production` and `stg` are not aliases for `prod`
and `staging`; if used as directory names, they identify separate environments.

## Public endpoint role table

`B` below is the selected environment's base hostname. Prefixes are registered
names: preserve `certissuer` and `turnregistry` as spelled here. Add a new role
to this table and the shared resolution flow before creating its DNS records.

| Role | Default hostname | Configuration/derivation | Connection and target |
| --- | --- | --- | --- |
| Video Cloud | `B` | Derived `VIDEO_CLOUD_DOMAIN` | Public HTTPS edge to Video Cloud. |
| MQTT | `B` | Derived `VIDEO_CLOUD_MQTT_ADDR` | MQTT TLS at `B:8883`; shares the base name with HTTPS. |
| Device identity ingress | `device.B` | Derived device hostname and mTLS/token base URLs | Public edge with required client-certificate validation; a separate identity boundary for Video Cloud. |
| Account Manager | `account-manager.B` | Derived `ACCOUNT_MANAGER_DOMAIN` | Public HTTPS edge to the account control plane. |
| Cloud Admin | `admin.B` | Derived `CLOUD_ADMIN_DOMAIN`, or prod-only `CONSOLE_DOMAIN` input | Public HTTPS edge to Console/BFF; a short prod browser entry is an explicit user-facing exception. |
| Frontend | `frontend.B` | Default, or supported `FRONTEND_DOMAIN` | Public HTTPS edge to the website; prod uses its explicit brand name. |
| Billing | `billing.B` | Derived Billing hostname | Public HTTPS edge to Billing. |
| Cloud Logger | `logger.B` | Derived `CLOUD_LOGGER_DOMAIN` | Public HTTPS edge when the logger route is enabled. |
| Certificate Issuer | `certissuer.B` | Derived `VIDEO_CLOUD_CERTISSUER_DOMAIN` | Certificate issuer HTTPS ingress; certificate/auth policy is defined by its owning service. |
| Factory enrollment | `factory-enroll.B` | Default `FACTORY_ENROLL_DOMAIN`, or an explicit supported tracked override | Dedicated public factory mTLS ingress when `FACTORY_ENROLL_PUBLIC_ENABLED=true`; the enrollment service owns its authentication policy. |
| TURN Registry | `turnregistry.B` | Derived TURN registry hostname | Public HTTPS edge to TURN registration/discovery. |
| TURN data plane, one node | `turn.B` | Effective TURN count is one | Direct TURN node address, with protocol and port from TURN discovery/configuration. |
| TURN data plane, multiple nodes | `turn01.B`, `turn02.B`, ... | Effective TURN count is greater than one | One DNS name per node; each name points to its own node target. |
| Payment Simulator | `payment-simulator.B` | Derived simulator hostname | Simulator HTTPS ingress when included in the selected deployment profile; name reservation does not authorize using it as a production payment provider. |

The enabled runtime routes determine which public-edge records exist. TURN count
zero creates no TURN-node record. A TURN count change between one and multiple
nodes changes the node names under the current convention; review discovery
results, DNS ownership, and certificate names together when changing that count.

The logger plan uses configured replica intent and performs no live Kubernetes
lookup. Its runtime route is included only when the logger Service exists. A
planned logger name therefore requires deployed Service, DNS/Ingress, and TLS
evidence before it can be reported as an available endpoint.

Internal Kubernetes names such as
`account-manager.<stack>-account-manager.svc.cluster.local` remain private
service-discovery names. They are not records to create in the public root zone.
Service-to-service connections must use the appropriate private/authenticated
boundary defined by the service and deployment guides.

## Exceptions and supported configuration

| Exception | Example | Ownership and rule |
| --- | --- | --- |
| Production website name | `www.realtekconnect.com` | Explicit `FRONTEND_DOMAIN` in prod `environment.env`; `PUBLIC_BASE_URL` must identify the same origin. It replaces the default frontend hostname in the effective plan. |
| Production Console/login name | `console.realtekconnect.com` (optional example) | Explicit prod-only `CONSOLE_DOMAIN`; derives `CLOUD_ADMIN_DOMAIN` and replaces its default in the effective plan. Default website login links follow this Console name. Align any explicit authentication URL/callback and the OAuth provider's registered redirect with it. |
| Shared external dependency | `sm.realtekconnect.com` for dev/staging Sendmail | Explicit `SENDMAIL_HTTP_BASE_URL`; owned by the external/shared service. It is not an environment-generated record and is excluded from environment removal. |
| Instrumented coverage runtime | `coverage-*` stack | Explicit `CLOUD_RUNTIME_COVERAGE_STACK` in the dedicated LKE coverage flow. Keep its resource/DNS ownership distinct from the persistent environment; do not inherit a production Console brand name. It is not a normal tracked stack override. |
| Low-level renderer escape hatch | Device/frontend/Billing/TURN hostname override | Only supported by the command/profile that consumes it. Its effective value must appear in the reviewed plan and applicable DNS/TLS evidence; it is not automatically a valid `environment.env` key. |

Normal tracked configuration uses the environment schema. `FRONTEND_DOMAIN`
and `FACTORY_ENROLL_DOMAIN` are allowed hostname inputs; `CONSOLE_DOMAIN` is
allowed only for prod. Factory enrollment defaults to `factory-enroll.B`. When
its public route is enabled, the resolver validates the factory hostname as a
lowercase name beneath `CLOUD_DNS_ROOT_DOMAIN`, independent of
the standard service hostnames. This supported service override has managed
environment ownership and is separate from the browser brand-name policy.
The generated `CLOUD_ADMIN_DOMAIN` remains a derived value; do not set it
directly in tracked configuration. Generated backend service-domain keys are
not ordinary tracked inputs. DNS-provider settings in `overrides/dns.env` select
provider behavior, not an alternative naming policy. Do not copy low-level process-environment
escape hatches into tracked files without adding schema, plan, and runtime support.

The short-name policy is limited to user-facing browser entries. It does not
create short backend names such as `api.<R>` or `account.<R>`; Video Cloud,
Account Manager, Billing, device mTLS, logger, certificate issuer, factory
enrollment, and TURN retain their registered environment-scoped defaults. Adding a UI name does not
replace the environment base `B` or change those backend names.

Managed exceptions must remain within the configured root zone and must not
collide with another environment's names. A hostname's spelling does not enforce
authentication or environment isolation; DNS, TLS identity, routing, credentials,
and authorization must all agree with the selected environment.

`PUBLIC_BASE_URL`, when set, is the effective frontend's HTTPS origin, with an
optional trailing slash. It must not contain credentials, another path, a query,
or a fragment. Planned names cannot assign the same DNS record to conflicting
public-edge and TURN targets.

## Plan, change, and evidence lifecycle

1. Select the environment directory, matching stack, root zone, enabled services,
   and supported exceptions. Review uniqueness across environments.
2. Run the configuration preflight and deployment plan described in
   [the environment README](../cloud_env/README.md#validate-and-provision).
3. Review `runtime/resolved/dns-plan.json` for effective hostnames, record types,
   targets, optional services, frontend exceptions, and TURN node count. Symbolic
   `runtime:` targets in the plan are intent; deployed evidence supplies actual IPs.
4. Check that client URLs, login/callback origins, ingress hosts, and certificate
   SANs use those same resolved names. A same-environment link must stay within
   that environment unless it is an explicit shared dependency.
5. Deploy using the applicable operation and gates in
   [Deployment Operations](deployment-operations.md). Retain sanitized resolved
   names, actual record values, and DNS/TLS verification evidence.
6. For a rename or removal, reconcile clients, callbacks, certificates, and DNS
   ownership together. Environment removal may delete only its recorded managed
   values; brand/shared names require their explicit ownership review.

The policy owns formulas and role definitions. Generated plans own resolved
intent. Runtime record-ownership state and dated evidence own actual target
values. Keep transient IPs and duplicated per-environment service inventories
out of long-lived policy and supporting deployment documents.

## Implementation and document map

| Responsibility | Source |
| --- | --- |
| Environment schema, identity checks, and runtime endpoints | [`deployment_config.go`](../scripts/go/rtk-cloud/deployment_config.go) |
| Shared stack/service-name derivation and runtime validation | [`internal/envroot/envroot.go`](../scripts/go/rtk-cloud/internal/envroot/envroot.go) |
| Effective public HTTPS routes and frontend exception | [`lke.go`](../scripts/go/rtk-cloud/lke.go) |
| TURN node naming and count | [`k8s_coturn_vm.go`](../scripts/go/rtk-cloud/k8s_coturn_vm.go) |
| Generic DNS plan, adapter boundary, and record ownership | [`dns_adapter.go`](../scripts/go/rtk-cloud/dns_adapter.go) |
| Tracked configuration entry point | [`cloud_env/README.md`](../cloud_env/README.md) |
| Directory/responsibility layout | [Cloud Environment Layout](cloud-env-layout.md) |
| Deployment architecture | [Cloud Deployment Architecture](cloud-deployment-architecture.md) |

Keep the environment and endpoint tables in this document. Other guides link
here and may include clearly labelled examples. Historical snapshots establish
past state and do not define current names.
