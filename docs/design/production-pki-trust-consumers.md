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
| Factory enrollment → certissuer | Factory owns registered Service key/CSR renewal; issuer has Service admission, timed eviction and durable CRL consumption locally. | Adopt governed server/client leaves under the active dev Service hierarchy; verify renewal, receipt gates, selective eviction and restart. |
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
Overall milestone progress is approximately **35%**, an engineering estimate
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

## Dev rollout prerequisites and first Service authority

Before live adoption, include `pkimanagement` in the workspace's canonical
`lke-build-images` generated Dockerfile as well as the service Dockerfile. The
offline ceremony must reconstruct the complete approved provision request,
including `service_client_ids` and `server_dns_names`, when validating the request
digest and authority policy. Reject changes to either policy before signing; do
not bypass the approved digest to make a Service intermediate ceremony proceed.
Device request digests and offline key custody remain unchanged.

Roll out the selected dev controller revision with explicit domain membership,
preserving the existing Device API/broker gate. Provision a separate dev Service
Root with normal distinct human approvals and encrypted offline ceremony state.
Install its reviewed manifest at actual controller/certissuer listeners, obtain
their real receipts, then activate and import a signed Root CRL. Only then create
the approved Service intermediate and subsequent server/client identities.
Retain scoped resource-version-checked rollback manifests and reproducible dev
settings. Receipt absence must still block activation; no manual receipt writes
or production-custody claim is permitted. Persist evidence at each stage and
reconcile uncertain operations before replaying any mutation.


## 2026-09-08 live dev Service Root checkpoint

The independent Service Root is active in dev. Video Cloud `0f6f632` binds the
offline ceremony to the complete approved issuer policy; workspace `08b06d5`
adds the canonical managed-owner image build and separate dev consumer gates.
The maintained [Service dev runner](../../scripts/pki-service-dev/README.md)
records each scoped phase and preserves failed evidence for reconciliation.

Root issuer: `59c37a28-7016-4706-ae93-e3da7746615d`.
Certificate SHA-256:
`87099089d30f13a7b93035b59c1c0c91bdb05bbe3dab427258c0c48e38144cc2`.
The Root key is encrypted offline in the private dev evidence directory, with a
separate passphrase file; both have mode 0600 and key/certificate correspondence
passed. Distinct ordinary-login approval accounts exercised the ceremony. This
is software custody simulation, not independent human custody qualification.

Only controller and certissuer workload images changed among the monitored dev
owners. Both run the verified image
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:5ff8bbfadfba1db7a5b3671c8e4d34416c464033bea8bd0d8206e26982a7dcea`.
They installed the reviewed Root bundle and each sent its actual serving-listener
receipt. Activation returned 409 with no receipts and again with only controller's
receipt; it succeeded after certissuer's receipt. Service membership is exactly
`certissuer,pki-controller`; Device membership remains `video-cloud-api,pkibroker`.
No receipt was written manually. Existing bootstrap management transport and
server credentials remain in use pending governed leaf adoption.

Root CRL 1 SHA-256:
`2fcd66950a5a7b3df1e29c1ff66490ce3be9be67346f537f3f3a6680412ed6f8`.
Its next update is **2026-09-09 11:39:27 UTC**; a reviewed offline refresh is
required before expiry. The initial activation phase failed after signing,
before import, because the runner expected `crl.pem` instead of the ceremony's
`revocations.pem`. The corrected recovery phase validated and imported the exact
saved signed CRL without signing again. The failed report remains alongside the
successful recovery; this was not an uninterrupted acceptance run.

The existing Device direct-mTLS and MQTT ACL/QoS1 baseline passed after import.
A subsequent read-only runtime audit passed for running image digests/readiness,
Root receipts/current CRL, offline key correspondence, persisted image/settings
rendering and presence of the managed-owner binary in both images. Unchanged
monitored API, broker, factory and Account Manager pods retained their images and
identities. This is baseline regression evidence, not a full Device lifecycle rerun.

Private phase reports are retained under the operator's dev configuration at
`pki/service-rollout-20260908-1/{root,controller,certissuer,activation,crl-recovery,verification}`.
Raw responses, keys and rollback objects stay outside Git. Desired scoped overlays,
operator image files and Service settings are persisted in the existing dev PKI
configuration. The runner re-renders and verifies those settings; the generic
platform renderer does not automatically manage these Service overlays.

Local validation passed: five Service runner tests, twelve Device acceptance
helper tests, focused Go consumer/build tests, offline ceremony tests and vet,
and the canonical dev image build. Source and live evidence cover this Root
checkpoint only. No staging changes, Git push, PR or remote CI occurred.

**Current milestone: approximately 30%; 1/6 work groups complete, 5 open.**
Four broad milestones remain. Next create the approved Service intermediate,
then adopt governed controller/certissuer server credentials and qualify managed
Account Manager egress in dev. Other management callers, durable CRL adoption,
renewal/revocation/restart, remaining transports, Root policy and App/relay live
acceptance remain open. No Service intermediate or managed caller was deployed
at this checkpoint.


## Next dev step: approved Service intermediate and listener adoption

Generate the first dev Service intermediate key internally in OpenBao under the
active independent Service Root. Its immutable approved client policy is limited
to `service:account-manager`, `service:certissuer` and `service:pki-controller`.
The server policy contains the exact dev service names
`certissuer.video-cloud-dev-video-cloud.svc` and
`pki-controller.video-cloud-dev-video-cloud.svc`. Other host/client identities
require a later explicit approved policy; this first authority is not an allow-all
Service CA. Verify the deployed endpoint inventory before creating the request.

Use distinct ordinary-login approval accounts and sign the registered provider
CSR with the existing encrypted offline Root. Require the complete approved
request/CSR/parent digests. Check provider custody with the actual controller's
workload identity: one internal key, no private-key read/export, and no server or
Service-client signing permission. Preserve existing provider role memberships;
add only generated exact-mount policy. Signing grants remain separate and are
installed only when the corresponding workload is adopted.

Create a versioned immutable public bundle ConfigMap containing Root and ready
intermediate. Switch controller and certissuer to it sequentially using resource
version and old-volume/image preconditions. Each serving listener must emit its
own receipt. Confirm activation denial before installation and with only the
controller receipt, then activate after certissuer receipt and import the exact
provider-signed intermediate CRL. Preserve failed phase evidence; do not recreate
an authority or repeat a signing/provisioning operation after an uncertain result.
Recheck Device mTLS/MQTT and persisted deployment/manifest state afterward.

Governed server leaves follow the active hierarchy. Before replacing either
listener certificate, distribute the Service public Root to every existing caller
that reaches that listener, preserve its existing trust, and provide the managed
host's persistent per-instance key state and separate renewal client credential.
Qualify the server transition before enabling Account Manager managed egress.
Root/intermediate activation alone does not establish durable CRL consumption,
managed leaves, key renewal, revocation cutoff or backup/recovery qualification.


The live caller inventory found that factory enrollment currently reaches
certissuer using its `.svc.cluster.local` alias. The approved first intermediate
uses the exact `.svc` name. During the future server transition, normalize that
caller's endpoint to the approved `.svc` name together with its Root trust update,
then verify authenticated factory enrollment. Do not serve a certificate that
silently drops the name a live caller still uses, or broaden the signed issuer
policy after approval. Account Manager, Device API/broker and both Service bundle
consumers currently reach controller at its approved `.svc` name. Their existing
bootstrap server-CA files all need an additive public Root update before the
controller certificate switches. Preserve bootstrap client authentication until
each managed replacement is qualified.


## 2026-09-08 live dev Service intermediate checkpoint

The dev Service intermediate `488243a7-e41b-4788-bcec-19d721daab80` is active
under Root `59c37a28-7016-4706-ae93-e3da7746615d`. Its certificate SHA-256 is
`0c7c538969184848160118a640ef3fef2d1b0d810355ef780384adc97e0753a9`.
Operation `e641dbd8-b93b-49bb-be97-2467276c6881` used distinct ordinary-login
approval accounts and the full approved client/server policy described above.
OpenBao generated one internal intermediate key; the existing offline Root
signed only its registered CSR. Controller workload capabilities deny private-key
read/export and both server and Service-client signing. No leaf signer policy
was granted by this rollout.

Both serving listeners adopted immutable ConfigMap
`pki-service-bundles-488243a7` and emitted their own exact-version receipts.
Activation returned 409 before receipts and again with only controller's receipt;
it succeeded after certissuer's receipt. All four intermediate phases passed
without reconciliation or replay. The images remain at the prior verified dev
digest; only their bundle volume references changed. Device membership remains
`video-cloud-api,pkibroker` and Service membership `certissuer,pki-controller`.
The desired manifests and scoped Deployment overlays are persisted privately.

The provider-signed intermediate CRL is imported with SHA-256
`bd928e8d1d440646174ec46723e890940754ba898758b457d51f577a89e50f11`, next update
**2026-09-11 11:51:26 UTC**. The parent Root CRL still expires earlier, on
**2026-09-09 11:39:27 UTC**, and needs a reviewed offline refresh before then.
Device direct-mTLS and MQTT ACL/QoS1 baseline passed after activation/import.
Private phase evidence is under dev configuration at
`pki/service-intermediate-20260908-1/{preflight,prepare,controller,certissuer,activation,verification}`.
This is Service hierarchy bootstrap evidence, not a full Device lifecycle rerun,
durable CRL consumer qualification or a managed server/client rollout.


The final hierarchy audit passed for both active authorities, current CRLs,
actual listener receipts, immutable manifest/volume references, rendered settings,
running image digests and offline Root key correspondence. Both OpenBao server
and Service-client role settings matched the approved profile and selected the
registered intermediate certificate. Before/after evidence shows all monitored
images unchanged; API, MQTT broker, factory and Account Manager pod identities
were also unchanged. Each phase records the runner source digest; the final audit
matches the committed runner content.

Nine Service runner tests and twelve Device acceptance helper tests passed,
along with Python compilation and diff checks. The new tests cover rejection of
wrong Root scope, changed approved identities, altered bundle sources, broadened
provider names and wrong client/server profile flags. The existing dev service
image already contains the required implementation, so no new image publication
or service source change was needed. No Git push, PR, remote CI, staging change
or database reset occurred.

**Current milestone: approximately 35%; 1/6 work groups complete, 5 open.**
Four broad milestones remain. The active management-adoption group now has its
live Root/intermediate hierarchy and real listener bundle receipts. Next: issue
and adopt governed controller/certissuer server credentials with their caller
trust updates, then qualify managed Account Manager egress. Durable CRL adoption,
other callers, renewal/revocation/restart and the other four work groups remain
open. Hierarchy activation does not complete management adoption.
