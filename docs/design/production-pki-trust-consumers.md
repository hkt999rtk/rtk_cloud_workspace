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
| Account Manager → controller | `rtk_account_manager/internal/api/pki.go` retains signed human assertions and supports a private socket to the managed `pkimanagement` owner; controller admits registered Service clients. | Live managed caller adoption, installed CRL receipts and dev lifecycle; static mode remains available until explicit opt-in. |
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
Overall milestone progress is approximately **25%**, an engineering estimate
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

## Service listener bundle installation and bootstrap

Use an explicit reviewed `*_SERVICE_CLIENT_BUNDLE_MANIFEST` containing issuer
IDs and exact bundle versions. The controller and certificate issuer will share
the Service listener implementation. A receipt attests the CA pool actually
used by the bound direct-mTLS listener, its independent Service root pin and
the registry lineage. It is emitted over that listener's separate management
identity only after the server begins serving and a successful connection sweep.
Stopped listeners, partial configuration, changed bundles, wrong domains,
missing installed roots or unavailable registry evidence emit no new receipts.

A ready Root can be installed before activation without its own CRL. A ready
Service intermediate requires an active parent with a current signed CRL and
its approved Service client policy. Active and retiring issuers require their
own fresh CRLs. Retiring authorities remain valid for existing clients during
overlap but receive no new bundle receipts.
The whole reviewed manifest is validated in a read-only repeatable-read snapshot
before sending receipts and revalidated before each send. Leaf admission remains
bound to the reviewed manifest, current receipts and signed CRLs; installing a
ready CA never authorizes a workload leaf. This does not install server leaves.

Bundle-only bootstrap uses the four existing management transport settings plus
the bundle manifest. The optional durable CRL consumer remains separate; enable
it after the initial hierarchy has active issuers and CRLs. When both are enabled,
they share transport settings and failed installation/sweep suppresses receipts.
Use separately trusted bootstrap management credentials until governed callers
exist. The controller can deliver bundle receipts to its own serving endpoint;
startup CRL preparation still requires an independently reachable endpoint.

This first Service manifest is immutable for a process lifetime. Changes require
a reviewed restart; removed or invalid entries deny acceptance until reconciled.
Persistent Root-policy updates and selective terminal-issuer retention remain
in the existing root-policy work group, not claimed by bundle installation.


## Service listener bundle checkpoint

Video Cloud `a857a1f` implements actual controller/certissuer Service bundle
receipts with serving-listener ownership, a frozen client CA pool, immutable
reviewed references and a read-only repeatable registry snapshot. Ready Root
and intermediate installation can now satisfy their activation gate without
misusing Device bundle logic or admitting a ready issuer's workload clients.
Exact registered leaf admission also checks manifest membership. Active and
retiring authorities require fresh signed CRLs; retiring authorities remain
usable during overlap but receive no new bundle receipts.

The four affected package suites passed with disposable local PostgreSQL 16
(pki, pkitrust, pkicontrollerapp, certissuerapp). Focused race tests, vet and diff
checks passed. A real local direct-mTLS controller receipt endpoint exercised
ready Root receipt/activation, missing-CRL denial, listener restart with a ready
intermediate, intermediate receipt/activation and registered/revoked client TLS.
Receipt delivery failure blocked activation and retried successfully. Additional
tests cover invalid references, wrong roots/environment/versions/policy, an
unreviewed leaf intermediate, retiring overlap, pre-serving/closed owners and
managed local identity denial with no connections. Local signer fixtures are
not OpenBao provider or live deployment evidence.

Work group 2 remains open: managed Account Manager/consumer client credentials,
actual durable CRL receipt adoption, and live dev renewal/revocation/restart
qualification are still required. The other open groups are remaining host and
transport adoption, persistent Root-policy adoption, App/relay enforcement and
repeatable dev acceptance. No live environment, Git remote, PR or remote CI was
changed. MFA policy is unchanged.

Current milestone estimate: **20%; 1/6 work groups complete, 5 open**. Four broad
milestones remain. This checkpoint completes a bootstrap prerequisite, not the
whole management adoption work group.

## Account Manager managed controller transport

Account Manager is a separate Go module; the existing Service identity manager
and registry-backed connection owners are Video Cloud internal packages. Reuse
those owners in a host-local `pkimanagement` process rather than copying key-state
and renewal implementations into Account Manager. This process is deployed with
Account Manager and owns `service:account-manager`; it is not a general proxy or
a new authority. The same private host state retains the generated key, pending
CSR/request and installed identity across restarts. Private keys never cross the
local HTTP interface. Initial issuance uses a separately trusted provisioner
credential only at the certificate issuer; controller traffic always requires
the installed registered identity, including immediately after startup.

Account Manager opts into `PKI_CONTROLLER_SOCKET`, mutually exclusive with its
static controller certificate/key/CA settings. Its existing HTTPS controller
origin remains explicit and must match the local proxy's configured destination.
Only a private Unix socket is used, with a private parent directory owned by the
runtime UID. Deploy both processes with that UID and share only the socket mount;
mount credential state and provisioner material only in the managed owner. No TCP
listener or environment proxy fallback is permitted. The owner rejects changed
origins, noncanonical paths, queries, upgrades, CONNECT, oversized requests and
routes outside `/v1/pki/issuers` and `/v1/pki/operations`. It preserves the exact
request body, method, Authorization assertion and idempotency key. The controller
still verifies signed human assertions and roles; local socket access cannot mint
or change them. Account Manager's current human-login/MFA policy is unchanged.

The owner requires separate explicit verified Service server policies for the
certificate issuer and controller. Reuse `ServerHTTPClient` for their registry,
CRL, deadline and connection eviction behavior, and `serviceidentity.Manager`
for initial issuance, renewal/retry and atomic installation. Managed replacement
or local identity denial evicts connections to both remote origins. Start serving
only after a registered current identity exists; use `Current` (never bootstrap)
for controller client TLS. No static fallback after opt-in or on owner failure.
Shutdown cancels renewal, closes outbound sockets and releases state/socket
ownership. Do not remove an unowned existing socket during startup.

Local acceptance must cover Unix request binding, preserved assertions, denial
without the owner, initial issuance, renewal/restart, revoked identity denial,
response-stream cutoff and failed trust. The deployment example must make UID,
private state and socket-sharing requirements explicit. Live dev adoption and
per-consumer durable CRL receipt qualification remain open until exercised; this
transport does not itself establish persistent Root-policy rotation or SDK work.


## Managed Account Manager checkpoint

Account Manager `41f1294` adds opt-in private Unix-socket controller egress while
preserving its signed human assertion, exact body/method/path and idempotency key.
Socket mode is mutually exclusive with static controller key/CA settings and has
no TCP/static fallback. Video Cloud `72a9ccf` implements `pkimanagement`: existing
host-owned Service issuance/retry/renewal plus separate registry-verified issuer
and controller connection owners. Controller TLS uses only the registered current
identity. Bootstrap remains confined to issuance. Keys stay in private owner
state; socket mounts are shared with Account Manager, credential mounts are not.

Local PostgreSQL/mTLS integration passed initial response-loss recovery without
a second signature, duplicate-owner rejection, current identity use over the
actual socket, renewal, old-response cutoff through the local proxy, clean shutdown,
restart without bootstrap, wrong remote Root-pin denial, revoked stream cutoff,
new-call/renewal denial and revoked-state restart denial. Account Manager tests
verify the actual RS256 assertion and body hash after socket transport, ordinary
human MFA=false behavior, changed-origin denial, owner absence/shutdown and unsafe
socket configuration. Route, size, query, upgrade and traversal rejection tests
passed. These tests use disposable authorities/provider fixtures, not live dev
or production provider qualification.

The affected Video Cloud package suites (pki, pkitrust, serviceidentity,
pkimanagementapp, pkicontrollerapp, certissuerapp) and Account Manager API/auth
suites passed. Focused race tests, vet, shell syntax, diff checks and a Linux
amd64 CGO-disabled build of the new executable passed. Makefile, image build,
release inventory, env examples and runtime documentation include the new owner;
no image publication or full release qualification is claimed.

Work group 2 remains open for other management consumers, actual durable CRL
receipt membership/adoption and live Service hierarchy/caller renewal, revocation
and restart. Next prioritize a dev Service rollout using the implemented
listener bootstrap and managed caller, preserving the completed Device gates.
The other four open groups remain host/transport adoption, persistent Root policy,
App/relay enforcement and repeatable dev acceptance. No live environment changes,
Git pushes, PRs or remote CI occurred. Staging and legacy migration stay deferred.

Current milestone estimate: **25%; 1/6 work groups complete, 5 open**. Four broad
milestones remain. This is implementation progress within management adoption;
it does not claim that the entire work group or milestone is complete.
