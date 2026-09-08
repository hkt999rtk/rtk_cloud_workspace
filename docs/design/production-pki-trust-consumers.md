# Remaining trust consumers and live sessions

Active milestone, 2026-09-08. Fresh dev Device PKI is complete; this milestone
extends the design to the other consumers and connections. Local commits only;
PRs wait until all milestones are finished. Live verification is dev-only.
Legacy migration and staging remain deferred. MFA is optional future human
login functionality and is never a Device authentication requirement.

## Fixed acceptance work groups

1. **Inventory and design reconciliation.** Identify actual listeners, callers,
   identity domains, key owners, existing enforcement, and missing adoption.
   Retain prior passing evidence without treating opt-in code as deployed proof.
2. **Management Service identity enforcement.** Connect controller management
   admission and existing connection eviction to registered Service clients;
   retain exact consumer authorization and human assertion checks. Qualify the
   Account Manager and consumer callers, their credential lifecycle, and actual
   installed-CRL receipts.
3. **Remaining transport and host adoption.** Close the identified Service,
   MQTT and OpenBao client/server gaps using the existing host and connection
   owners; verify public HTTPS separately. Keep private trust domains distinct.
4. **Root-policy adoption.** Connect remaining App/Service/MQTT/OpenBao consumers
   to reviewed root changes with persistent rollback protection, actual trust
   installation and connection eviction. A fixed root pin is not root rotation.
5. **App and relay enforcement.** Exercise App API/MQTT and TURN/signaling
   revocation and renewal on real dev owners; check active-session cutoff,
   selective survival and failed-consumer behavior.
6. **Repeatable dev acceptance.** Retain a maintained test/runbook and measured
   pass/fail evidence for the newly adopted paths, including restart, unavailable
   trust and revocation. Recheck the completed Device lifecycle for regressions.

These are work groups within one existing milestone, not six new milestones.
Hardware/provider qualification and matched backup/recovery stay in their own
milestones. Each group's remaining subitems must stay visible; completing one
adapter does not close its whole work group. Progress starts with the inventory;
the inventory is complete; implementation and live acceptance remain open.

## Audited adoption inventory

Checked against the current checkout and the previous fresh-dev completion
record. "Local" means implementation/test evidence, not live qualification.

| Connection / owner | Existing implementation | Remaining acceptance in this milestone |
| --- | --- | --- |
| Account Manager → controller | `rtk_account_manager/internal/api/pki.go` owns static mTLS files and signed human assertions; controller has exact-name route policy. | Governed client identity admission (first implementation below), managed caller keys, independent server revocation checks, installed receipts and dev lifecycle. |
| API / pkibroker / other consumers → controller | `pkitrust` owns separate static management TLS; Device API and broker receipts passed in dev. | Registered Service management credentials, renewal and revocation on actual callers. |
| Factory enrollment → certissuer | Factory owns registered Service key/CSR renewal; issuer has Service admission, timed eviction and durable CRL consumption locally. | Deploy independent Service hierarchy and verify renewal, receipt gates, selective eviction and restart in dev. |
| API / factory → Account Manager | `pkitrust.LoadServerHTTPClient` supplies optional Service server verification and connection ownership. | Governed Account Manager TLS listener/renewal and actual caller adoption; server replacement/revocation tests in dev. |
| Controller / certissuer → OpenBao | Both live workload constructions support registry-backed transport; recovery commands deliberately use separate restore trust. | Governed OpenBao TLS host renewal and root-policy adoption, actual dev transport replacement/denial. Recovery commands remain in the recovery milestone. |
| API / log ingester → EMQX | Independent MQTT server registry verification, CRL receipts and connection eviction locally; native `emqxpkihost` owns keys and replacement. Device MQTT auth/ACL/session worker passed in dev. | Governed MQTT server-host and client rollout, root-policy adoption, actual authenticated reconnect/session behavior. |
| App clients → API / MQTT, signaling → TURN | App registry issuance/revocation, CRL consumers and broker/TURN owner adapters exist locally. Device-only dynamic root installation does not implement App root changes. | App root-policy installation, full App/relay dev revocation and renewal with selective active-session cutoff and failure/restart evidence. |
| Public HTTPS / browsers | Publicly trusted CA/ACME remains the required independent boundary; private issuers must not supply browser HTTPS. | Record current dev endpoint and renewal evidence during final acceptance. |

Cloud Admin is a browser/BFF using Account Manager's human-authenticated proxy;
it does not own the controller's management client key. Database connections and
provider seal/HSM custody are separate infrastructure/qualification boundaries,
not additional Device/App identity consumers. Existing managed controller and
certissuer server-host adapters need live adoption, not a second implementation.

Inventory/design work group **1/6 complete**. Work groups 2–6 remain open.
Overall milestone progress is approximately **15%**, an engineering estimate
reflecting that the remaining runtime adoption and dev qualification dominate
the work; it is not six equal-sized percentages.

## First implementation: controller management Service clients

The controller currently verifies client TLS chains and authorizes exact legacy
Common Names (`account-manager` or the configured consumer ID). The certificate
issuer already composes `pkitrust.ServiceListener`, which additionally checks
registered Service client receipts and current signed CRLs at handshake, request
and timed sweep. Reuse that connection owner at the controller.

Introduce an explicit independent Service root pin for management clients. With
this policy enabled, `service:<id>` clients must pass registry verification before
their `<id>` can select an existing controller role. The HTTP handler rechecks
the identity before any action. This mapping never substitutes for required
consumer membership, Account Manager's signed human assertion, request binding,
role checks or independent approvals. Without this policy, Service-prefixed
clients cannot assume legacy roles. Existing separately trusted bootstrap
management identities retain their exact-name authorization during dev adoption.

An optional reviewed Service CRL manifest uses the existing durable consumer;
it acknowledges only after installation and a successful listener sweep.
Partial settings fail startup. The controller must not require its own listener
to be serving before startup CRL preparation: use an independently reachable
management endpoint when enabling remote receipt consumption. This checkpoint
does not rotate callers' keys or claim that static bootstrap credentials are
governed Service identities.

Required local evidence: admitted registered client, revoked/unknown/wrong-root
client rejection, active and hijacked connection eviction, no role escalation
between consumers and Account Manager, unchanged human assertion requirement,
partial-policy failure, trust outage and shutdown. Record live dev adoption
separately after the local implementation passes.

## Controller management implementation checkpoint

Video Cloud `9134882` implements the Service root pin, registered peer-to-role
binding, shared listener admission/eviction and optional exact-CRL consumer.
Separately trusted bootstrap identities retain their roles; leaves under the
governed root cannot bypass registry policy by dropping the Service prefix.

PostgreSQL-backed tests passed for controller role separation, ordinary signed
human assertions without MFA, unknown/revoked/wrong-root denial, actual mTLS and
confirmed TLS session resumption, active and hijacked stream cutoff, selective
survival, unavailable registry denial and managed-server guard composition.
Full pki/pkitrust/pkicontrollerapp/certissuerapp suites, relevant race tests, vet
and diff checks passed. This is local integration evidence; no live deployment
or managed-caller lifecycle is claimed. Work group 2 remains open.

The next prerequisite found during adoption is the controller's single global
required-consumer list. It currently applies Device consumers to other domains
too. Add explicit per-domain membership and gate selection before attempting a
Service rollout, so Service activation/revocation cannot depend on fabricated
Device receipts or weaken the existing Device gate.

## Domain-specific consumer policy

Add explicit `PKI_REQUIRED_CONSUMERS_{DEVICE,APP,SERVICE,MQTT,OPENBAO_TLS}` lists.
When any domain list is present, use only explicitly configured domains and
reject simultaneous `PKI_REQUIRED_CONSUMERS`. When none is present, preserve
the existing global-list mode. Empty, duplicate and malformed IDs fail startup;
unconfigured domains cannot activate or finish revocations in domain mode.

Resolve the domain from the stored issuer (or stored operation's issuer), never
from a consumer request. Use the same selected membership for bundle, CRL and
Root-policy receipt endpoints, activation/revocation completion and App/server
CRL workers. Human access to searches, emergency revocation requests and public
issuer inspection remains governed by existing assertion/role checks; missing
consumer configuration must not prevent emergency denial. Consumers belonging
to another domain cannot submit receipts or fetch its consumer-scoped records.
Policy is immutable for a running process; configuration changes require a
reviewed restart and preservation of existing required Device members.

Test both modes, missing/wrong-domain receipts, exact-domain completion and
worker health/finalization. A configuration change alone never creates receipts
or qualifies a consumer as deployed.

## Domain policy implementation checkpoint

Video Cloud `8baa0e3` implements per-domain configuration and selects membership
from the stored authority for all three consumer route families, issuer
activation, CA/leaf revocation completion and App/server CRL workers. Global
mode is retained; mixed modes, invalid IDs and missing domain gates fail closed.
Emergency revocation and human issuer inspection do not depend on configured
consumer membership.

The full Video Cloud Go suite passed with disposable PostgreSQL 16. Focused
race tests and vet passed. Integration tests prove Device activation still waits
for API and broker, Service Root removal uses its own consumer, cross-domain
CRL/Root/bundle requests are denied, historical foreign-domain receipts do not
release gates, and App/server/Service-client CRL workers select their own domain
in both finalization and health checks. Missing-domain emergency denial passed.

Read-only dev inspection confirmed the controller, certissuer, isolated Device
API and MQTT worker remain Ready on their prior image digests. The controller
still uses `PKI_REQUIRED_CONSUMERS=video-cloud-api,pkibroker`; registered Service
client policy and managed host identity remain disabled. No dev or staging
runtime mutation, image publication, PR or remote CI occurred in these two
implementation checkpoints.

Work group 2 now has two local prerequisites complete: controller management
Service admission and domain-specific consumer selection. Remaining within this
group: actual Service trust installation/receipts (including ready-issuer
bootstrap), managed Account Manager/consumer credential adoption, and live dev
renewal/revocation/restart qualification. The existing Device-only bundle
consumer cannot stand in for a Service listener. A Service root must be installed
by its actual consumers before activation; new client identities remain denied
until their issuer and signed CRLs are active. Resolve this bootstrap ordering
in the consuming-process implementation before live rollout.

Current overall milestone estimate: **15%; 1/6 work groups complete, 5 open**.
Four broad milestones remain; this milestone is not complete.
