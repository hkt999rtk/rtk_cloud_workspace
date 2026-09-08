# Remaining trust consumers and live sessions

Active milestone, 2026-09-09. Fresh dev Device PKI is complete; this milestone
extends the design to the other consumers and connections. Commits remain local
unless a branch backup push is requested; PRs wait until all milestones finish.
Live verification is dev-only.
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
| Account Manager → controller | `rtk_account_manager/internal/api/pki.go` retains signed human assertions and supports a private socket to the managed `pkimanagement` owner; controller admits registered Service clients. | Managed caller adoption, early renewal, replaced-leaf retirement, both listener CRL receipts and bootstrap-free restarts passed in dev. Active-session revocation and remaining failure cases still need live qualification. |
| API / pkibroker / other consumers → controller | `pkitrust` owns separate static management TLS; Device API and broker receipts passed in dev. | Registered Service management credentials, renewal and revocation on actual callers. |
| Factory enrollment → certissuer | Factory owns registered Service key/CSR renewal; issuer has Service admission, timed eviction and durable CRL consumption locally. | Governed issuer server leaf and seedless restart passed in dev. Adopt managed client leaves; verify renewal, receipt gates and selective eviction. |
| API / factory → Account Manager | `pkitrust.LoadServerHTTPClient` supplies optional Service server verification and connection ownership. | Governed Account Manager TLS listener/renewal and actual caller adoption; server replacement/revocation tests in dev. |
| Controller / certissuer → OpenBao | Both live workload constructions support registry-backed transport; recovery commands deliberately use separate restore trust. | Governed OpenBao TLS host renewal and root-policy adoption, actual dev transport replacement/denial. Recovery commands remain in the recovery milestone. |
| API / log ingester → EMQX | Independent MQTT server registry verification, CRL receipts and connection eviction locally; native `emqxpkihost` owns keys and replacement. Device MQTT auth/ACL/session worker passed in dev. | Governed MQTT server-host and client rollout, root-policy adoption, actual authenticated reconnect/session behavior. |
| App clients → API / MQTT, signaling → TURN | App registry issuance/revocation, CRL consumers and broker/TURN owner adapters exist locally. Device-only dynamic root installation does not implement App root changes. | App root-policy installation, full App/relay dev revocation and renewal with selective active-session cutoff and failure/restart evidence. |
| Public HTTPS / browsers | Publicly trusted CA/ACME remains the required independent boundary; private issuers must not supply browser HTTPS. | Record current dev endpoint and renewal evidence during final acceptance. |

Cloud Admin is a browser/BFF using Account Manager's human-authenticated proxy;
it does not own the controller's management client key. Database connections and
provider seal/HSM custody are separate infrastructure/qualification boundaries,
not additional Device/App identity consumers. Existing managed controller and
certissuer server-host adapters now serve registered dev leaves and preserve
private state across seedless restarts. Renewal/revocation acceptance remains.

Inventory/design work group **1/6 complete**. Work groups 2–6 remain open.
Overall milestone progress is approximately **65%**, an engineering estimate
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

**Current milestone: approximately 45%; 1/6 work groups complete, 5 open.**
Four broad milestones remain. The active management-adoption group now has its
live Root/intermediate hierarchy and real listener bundle receipts. Next: issue
and adopt governed controller/certissuer server credentials with their caller
trust updates, then qualify managed Account Manager egress. Durable CRL adoption,
other callers, renewal/revocation/restart and the other four work groups remain
open. Hierarchy activation does not complete management adoption.


## Managed server rollout contract (dev)

Use the active Service intermediate for the exact approved controller/certissuer
names. Enable only its server signing policy on the existing certificate-issuer
workload; preserve the separate Service-client signing boundary. Restrict the
initial server/renewal route to the two selected bootstrap management identities.
Append their public CAs to issuer client trust and allow their pods to reach the
issuer's renewal port. Record existing settings and exact policy grants first.

Generate each host key and CSR inside a dedicated dev PVC using the existing
image's OpenSSL. Export only the CSR; obtain the registered server certificate
through authenticated issuer HTTP and return only the public chain to the PVC.
Retain a stable request ID and key on uncertain issuance. Keys never pass through
the operator machine or enter Git/ConfigMaps. Each host imports its seed once into
its existing managed state owner, then seed files are removed. Use one replica,
Recreate rollout, private 0700 directory/0600 state and fsGroup OnRootMismatch to
preserve those permissions across volume attachment and restart.

Before switching the listeners, add the public Service Root to Account Manager,
Device API/broker and both Service bundle callers' controller trust, and to
factory/Account Manager issuer trust. Normalize both factory's explicit issuer URL
and Account Manager's envFrom issuer URL to the approved `.svc` name. Keep their
bootstrap client identities. Restart callers to load trust before certificate
replacement; verify normal authenticated behavior under the old server first.

Switch certissuer first, then controller, using their existing managed-host
implementation, separate bootstrap renewal client certificates, exact remote
server name/Root policy and durable per-host state. Verify the observed served
certificate fingerprint against the registered issuance and actual application
calls. Remove only that host's seed after state installation is proven; restart
without seed and verify unchanged state/certificate and Device baseline. This
proves managed server adoption/restart, not natural scheduled renewal, live
revocation, managed Account Manager egress or matched backup/recovery. Retain
explicit failure evidence and reconcile rather than regenerating host keys.


Managed server mode also requires `CERT_ISSUER_MAX_TTL_DAYS=365`; the legacy
1,095-day default fails configuration validation. Set this before enabling the
server domain. Verify a real authenticated endpoint after rollout readiness,
which can briefly precede process configuration failure. The short Service
regression helper now explicitly runs MQTT ACL/QoS1 roundtrip after connection
readiness; earlier short-helper results established connection readiness only.


After both managed server transitions, enroll one fresh dev canary through the
actual factory service using a new one-device, one-hour production run on the
existing active dev Product. Verify the selected Product issuer, matching device
key/certificate, direct mTLS authentication and MQTT ACL/QoS1. Keep the identified
dev canary and private credentials as explicit follow-up test state; the run's
quantity is consumed and its token expires. This tests the real factory-to-issuer
transport after the server name/trust transition without replacing the existing
Device baseline or claiming a full Device lifecycle rerun.


The first live certissuer adoption served its registered certificate and passed
its authenticated issuance replay, but the inspection helper initially rejected
an inherited setgid directory mode (`02700`). The runtime's private-access check
correctly permits it: setgid does not add group permissions. The helper now
accepts `0700`/`02700` directories and requires a `0600` state file, with regression
tests rejecting group access. Recovery verifies the unchanged live template/PVC
and existing seed, then removes the seed and restarts without issuing another
certificate. Preserve the failed inspection and successful recovery as distinct
reports; do not characterize this rollout as uninterrupted.

Controller adoption removed its seed and served the managed certificate after
restart, then a subsequent Device TLS probe failed. A separate read-only check
passed controller serving and Device mTLS/MQTT with fresh port forwards. The
original transport error was not captured, so its cause remains unconfirmed.
Recovery must verify the recorded template, PVC and private state hash, require
all seed files absent, then repeat the seedless restart and actual Device probes.
Preserve the failed phase; do not regenerate a key or replay host issuance.

## 2026-09-08 live dev managed server checkpoint

The existing certissuer and controller images now serve registered Service server
leaves under intermediate `488243a7-e41b-4788-bcec-19d721daab80`. Their separate
P-256 keys were generated inside dedicated retained PVCs and never exported.
Each existing host owner adopted a private managed state file, removed its seed
key/CSR/chain, and successfully restarted from managed state alone. The state
hash and actual served certificate stayed unchanged across the qualified restart.

| Host | Retained PVC | Served certificate SHA-256 |
| --- | --- | --- |
| certissuer | `certissuer-service-identity` | `d67f758e9fb03e3f2eefcbd9ef46659a94db7d6afa860a337b5e6d37b10b7ff6` |
| pki-controller | `pki-controller-service-identity` | `679fba85cc50e3ecceabfa8b6116ae5c5a59f16718c1ae2dc959fc9878b0c23d` |

Both hosts use one replica with Recreate, UID/GID 10001, private directory access
and a `0600` state file. The self/peer renewal transport has an independent
Service Root pin, approved `.svc` name and separate bootstrap client credential.
Account Manager, factory enrollment and the four controller consumer workloads
received additive Root trust before either server changed. Human assertions and
Device consumer gates remain enforced. The private scoped deployment renderer
retains complete volume settings and the configured host policy.

Private evidence lives under
`~/.config/rtk_cloud/dev/pki/service-hosts-20260908-1/`.
`prepared-recovery`, `callers`, `certissuer-recovery` and `controller-recovery`
passed. Original `prepare`, `certissuer` and `controller` reports remain failed:
respectively incompatible legacy TTL, overly strict setgid inspection, and an
unreproduced later TLS probe failure. Recovery reused the recorded requests,
keys and PVCs; no phase is described as an uninterrupted successful rollout.

This checkpoint does not qualify natural scheduled renewal, server revocation
and active-stream cutoff, managed caller credentials, root rotation, database
least privilege, or matched recovery. Certissuer's pre-existing database identity
remains unchanged. No staging resources, repository pushes, PRs, remote CI or
runtime images changed. Next priority is managed Account Manager egress and its
credential/CRL lifecycle on these governed server endpoints.

The real factory-enrollment canary passed with a new one-device production run
valid for one hour, the expected active Product issuer, a matching private key,
Device mTLS authentication and MQTT ACL/QoS1 delivery. Its identified dev Device
and private credentials are retained in `canary/` for later tests; baseline
Device state was preserved. The corrected short Device probe also passed after
both listener adoptions. Earlier Service hierarchy short probes verified MQTT
CONNECT only; their prior `mqtt_acl_qos1` label was too broad. The complete earlier
Device lifecycle acceptance is separate and is not replaced by this short probe.

Active milestone: approximately **45%**, **1/6 work groups complete, 5 open**.
Within those open groups, the two server adoptions and fresh factory canary are
complete; managed clients and CRL lifecycle, remaining transports, root-policy
adoption, App/relay enforcement and complete repeatable dev acceptance remain.
The four broad unfinished milestones are unchanged.

The first final audit passed mounted caller transports and host persistence,
then used the Deployment name as a service account name. Kubernetes rejected
the token request: the actual account is `certissuer-pki`. The corrected audit
must discover that account from the Deployment, test the existing OpenBao role,
and revoke its audit token; no runtime identity or policy change is required.
The original `verification/` failure remains retained.

The corrected `verification-recovery/` audit passed all five checks: preflight,
actual mounted controller-caller mTLS/CRL reads, server signer boundary, Device
mTLS/MQTT ACL/QoS1 and managed host persistence/serving verification. The four
consumer pods read the expected current signed CRLs over verified TLS; Account
Manager's ordinary human assertion path also passed. These reads are transport
evidence, not fabricated CRL installation receipts. The actual certissuer
workload role permits server signing only for this intermediate; Service-client
signing, private key reads, role mutation and internal key generation are denied.
The audit token was revoked. All six monitored Deployment images match the
pre-rollout inventory. Local validation passed 28 Python tests, the Go TLS probe
suite, Go vet and diff checks.

## Dev Account Manager credential adoption contract

The next checkpoint deploys the existing `pkimanagement` owner beside the single
dev Account Manager API replica. Build the committed Account Manager source for
socket support; reuse the qualified Video Cloud image for the owner. Preserve
worker images, environment settings, application data and JWT assertion keys.
The API's existing `pki-auth` mount must project only its four JWT key files after
opt-in: its former controller TLS key must no longer be mounted into the API.
Only the owner mounts its dedicated retained identity PVC and verifier database
Secret. Both containers share a private socket directory under an ephemeral
volume and run as UID/GID 10001. Recreate prevents simultaneous identity owners.
An init container prepares the socket directory before either process starts;
container restart must not silently delete an existing socket or identity state.

Use a new dev login role inheriting the existing non-login PKI verifier role,
with no issuance/revocation writes or schema ownership. Verify the inherited PKI
read grants and denied writes. Grant only this active intermediate's explicit
Service-client signing policy to certissuer, preserving its server/Device signing
policies. The earlier server-only provider audit is historical after this grant.

A dedicated short-lived bootstrap CA and provisioner leaf are scoped to this dev
adoption. Trust the CA only at certissuer and allow its exact provisioner CN.
The owner generates the managed `service:account-manager` key/CSR in its PVC;
bootstrap keys are separate and may be stored in the protected rollout evidence
and owner-only Kubernetes Secret. Never read managed private state out of the pod.
Record its public issuance from the registry and a private-state hash, then prove
normal signed human operations traverse the socket and registered controller
identity. Remove owner bootstrap settings/mount and the temporary issuer trust/
provisioner permission after installation. Restart without bootstrap and verify
unchanged identity/state, no duplicate issuance, and retained ordinary login.

Keep provider/registry reads available independently of the Account Manager path
for diagnosis. A failed adoption retains exact before/after Deployments, PVC UID,
issuance evidence and stable pending state; reconcile rather than delete or issue
another identity. This checkpoint does not force early renewal by editing state
or clocks. Natural renewal, live revocation/stream cutoff, installed CRL receipts
and unavailable-trust qualification remain explicit later lifecycle checks.
Staging stays untouched. Git pushes remain backup-only; no PR or CI dispatch.

The first Account Manager adoption reached a healthy managed owner and one
successful registry issuance, then its audit incorrectly expected status
`issued` instead of the table's `succeeded`. Keep that failed report. Correct
the checker with pending/revoked/duplicate regression cases, then reconcile the
unchanged Deployment/PVC and existing issuance through a read-only recovery.
Do not replay preparation, rollout or key generation to repair an audit label.

Bootstrap removal restarted Account Manager successfully with unchanged managed
state, then exposed a denial-probe limitation: Go's automatic certificate
selection omitted the old bootstrap leaf once its CA disappeared from the
server's advertised list. The issuer logged a missing client certificate. That
is not evidence of rejecting the selected credential. The probe must explicitly
present that credential while still verifying the remote server, and require an
actual remote certificate-rejection alert. A TLS 1.3 replacement-CA fixture
regresses this behavior; timeouts and missing-certificate alerts remain failures.
Retain the failed sealing report. Recovery checks the unchanged owner/PVC/state
and already-removed issuer trust, repeats a bootstrap-free restart, verifies the
explicit rejection, then removes only the recorded bootstrap Secret.

## 2026-09-08 live dev Account Manager credential checkpoint

Account Manager now uses `service:account-manager` through the private socket
`/run/account-pki/private/controller.sock`. Its `pkimanagement` owner generated
and retains the key in `account-manager-service-identity`, at private state path
`/var/lib/account-pki/private/identity.json`. The key was never exported. The API
container retains human JWT keys, but no longer mounts its old controller TLS
key or the managed state. The credential owner does not mount human JWT keys.

The registered certificate fingerprint is
`eff6d595845860cd63938b047578811cbebeda1f41a9605c7345c2c988bcc849`,
issued by Service intermediate `488243a7-e41b-4788-bcec-19d721daab80` for stable
request `f073005c-67e0-4d52-9f62-5abf579e9f36`. Exactly one successful issuance
remained across bootstrap-free restarts, with unchanged private state hash.
Ordinary human-asserted PKI searches passed for the three distinct dev users,
and the existing Device mTLS/MQTT ACL/QoS1 baseline passed after each transition.
MFA remains disabled. Initial provider signing and registry admission were real.

The temporary provisioner settings/mount, issuer CA trust and provisioner
permission were removed. The owner restarted with its installed identity alone.
The old provisioner certificate was then explicitly presented and rejected by
certissuer. Only the recorded bootstrap Secret and its desired overlay were
deleted; protected local bootstrap evidence and managed identity PVC are retained.

The API image was built from clean Account Manager `41f1294` with the canonical
dev builder and pinned to its registry-verified linux/amd64 digest:
`ghcr.io/hkt999rtk/rtk_cloud_dev/account-manager@sha256:4b83e65364ef6c18a3787c5f634bbed51edccc1a9cfebeecf62151a8d6155d39`.
The owner reuses the qualified Video Cloud image ending `a7dcea`. The API-only
image pin and complete Deployment are consumed by the scoped `render_management`
renderer; full platform provisioning must explicitly reconcile these overlays.
The verified controller origin is
`https://pki-controller.video-cloud-dev-video-cloud.svc:18446`, with independent
issuer origin `https://certissuer.video-cloud-dev-video-cloud.svc:9443`; both have
explicit Service Root and DNS pins.

Private evidence lives under
`~/.config/rtk_cloud/dev/pki/account-manager-managed-20260908-1/`.
`prepare`, `adopt-recovery` and `seal-recovery` passed. The original `adopt` and
`seal` reports remain failed for the audit status-name and certificate-selection
bugs described above; no uninterrupted run is claimed. Recovery retained the
same issuance, key and PVC, and did not reset application data or alter staging.
Git backup branches were pushed only when requested earlier; this implementation
checkpoint creates no PR, CI dispatch or additional Git push.

Active milestone progress is approximately **50%**: **1/6 work groups complete,
5 open**. Initial Account Manager adoption/bootstrap removal is complete, while
Service credential renewal/revocation, durable CRL receipts, other managed
callers/transports, Root-policy adoption, App/relay enforcement and full dev
acceptance remain. Four broad unfinished milestones remain. The next dev work
must keep Service CRLs fresh (the current Root CRL expires
2026-09-09T11:39:27Z) and qualify refresh/recovery plus credential lifecycle; this
checkpoint does not establish an automatic Root CRL maintenance path.

The final `verification/` run passed all seven checks: live image/preflight,
OpenBao server/Service-client signer boundary, explicit bootstrap TLS denial,
managed human API and two-way key-mount separation, verifier SQL permissions,
Device mTLS/MQTT ACL/QoS1, and the final persistence/worker-image audit. The
certissuer workload can sign the reviewed Service server/client profiles; key
reads, server/client role mutation and internal key generation remain denied.
The temporary provider audit token was revoked. Other monitored workload images
and all Account Manager worker images are unchanged. Local validation passed
34 Python tests, the Go TLS probe suite including the replacement-CA case, Go
vet and diff checks. Both service repositories remain at their existing commits.

## Dev Service CRL maintenance and publication recovery contract

Managed server and client admission depends on fresh Root/intermediate CRLs.
Their normal management API cannot be assumed available after these CRLs expire.
Perform routine refresh before the existing evidence expires; never relax the
host guard, restore removed bootstrap trust or edit registry rows to keep traffic
working. Recovery of an expired/unavailable management plane requires a separate
reviewed operator path and remains unqualified in this checkpoint.

The maintained dev flow snapshots the current Root and intermediate, their signed
CRLs, issuer fingerprints and registered leaves. For the offline Root, construct
a complete refresh manifest with a strictly increasing CRL number, unchanged
revocation entries, current UTC thisUpdate and a 72-hour nextUpdate (within the
existing seven-day maximum). Bind the manifest digest and pinned Root to the
existing encrypted offline key/passphrase ceremony; never upload that key or
create an online Root signer. Publish the resulting public CRL through the normal
human-authenticated Account Manager/controller API while trust is still fresh.

Signing and publication are separate phases with durable private evidence. An
uncertain publication must read the registry and reconcile the saved signed DER
digest, number and prior digest before retrying the same public artifact. Reject
unrelated newer CRLs, changed artifacts/authority or rollback; never sign again
as a response-loss retry. Exercise a real, request-scoped localhost proxy that
forwards one authenticated import but drops its HTTP response. The recovered
registry must contain exactly one new version and one matching import audit;
idempotent replay must not add either. Retain the deliberately failed report.

Refresh the Service intermediate separately through the actual controller
workload's existing OpenBao rotation/read capability, saving the returned signed
public CRL before importing it. Do not regenerate its key or broaden provider
roles. If provider rotation is uncertain, reconcile the provider's current CRL
before considering another rotation. Validate signatures, monotonic numbering
and preserved revocations with the existing registry importer, and test rollback
denial using the previous signed artifacts.

After both refreshes, verify all managed leaves/state hashes and images remain
unchanged, real human PKI operations and Device mTLS/MQTT still pass, and repeated
publication remains idempotent. Record each new expiry and the earliest active
Device/Service CRL deadline. Registry publication is not an installed-consumer
receipt; durable CRL adoption, expiry recovery, automatic maintenance, credential
renewal/revocation and the other existing work groups remain open. No staging,
Git push, PR or scheduled automation is authorized by this maintenance command.

## Dev Service CRL refresh and response-loss checkpoint

The maintained [CRL runbook](../../scripts/pki-service-dev/CRL.md) and `crl.py`
separate offline signing, provider rotation, publication and reconciliation.
Private evidence is under
`~/.config/rtk_cloud/dev/pki/service-crl-20260908-1/`.

The Root refresh reused the existing encrypted offline key, preserved the prior
revocation set and published CRL **2**, digest
`71aff1bf67d81917a6ffe5d94b2d114eed1f7d6077764c41ffb3997b625cb5dc`,
valid 2026-09-08T14:15:24Z through **2026-09-11T14:15:24Z**. The
`response-loss/` report deliberately remains failed: the scoped proxy forwarded
exactly one import, received upstream HTTP 200 and dropped the response; the
caller observed EOF. `root-publication/` reconciled the already-published signed
artifact without signing again. Replay left exactly one version row and one
matching import audit event. The original CRL was rejected with HTTP 409.

The actual controller workload identity rotated/read the existing Service
intermediate through OpenBao. Its signed CRL **3**, digest
`021fa25f547dd1a7a69b82260dab6c43f3e658eb2a8bd7195b05f8cf9a0cb6e5`,
is valid 2026-09-08T14:18:18Z through **2026-09-11T14:18:18Z**.
The provider selected the monotonic CRL number; registry versions need not be
contiguous. `intermediate-publication/` passed import, idempotent replay, exactly
one row/audit and prior-CRL rollback denial. The temporary workload audit token
was revoked. No issuer key, provider role, workload image or trust policy was
changed by these publication phases.

Recovery accepts only the saved prior registry record or the exact desired
signed artifact. Changed issuer/request/artifact evidence and unrelated registry
updates fail closed. An uncertain provider rotation has a separate read-only
reconciliation phase that never blindly rotates again. Local validation passed
**40 Python tests**, including real HTTP response loss, altered-artifact denial,
concurrent-version rejection and uncertain-provider recovery without another
rotation. This is pre-expiry maintenance evidence, not expired-management-plane
recovery or installed-consumer CRL receipt qualification.

The first `verification/` run passed managed identity/image comparison, human API
and Device mTLS/MQTT, then failed in the final deadline-report query because it
used API field names for database columns. The maintained query now reads
`pki_issuers.id`/`domain` and includes active issuers without a CRL so missing
maintenance evidence cannot disappear from the report. The failed report is
retained; verification recovery performs no signing, rotation or republication.

The final `verification-recovery/` passed all four checks: image/preflight,
managed human API and key-mount separation, Device mTLS/MQTT ACL/QoS1, and complete
refresh acceptance. Both published CRLs still match their saved artifacts. The
three managed identity state hashes, Account Manager issuance, actual server
fingerprints, monitored workload images and all Account Manager worker images
remain unchanged. Evidence files/directories have no group/other access.

The earliest active dev CRL deadline is now **2026-09-11T06:54:13Z** (Device Root);
Product and Brand expire at 08:27:00Z and 09:28:44Z that day, followed by the
Service Root/intermediate above. These deadlines still require operator
maintenance; this checkpoint creates no automatic signer or scheduler.

Active milestone progress is approximately **55%**, with **1/6 work groups
complete and 5 open**. Four broad milestones remain:

1. Trust consumers/live sessions — active; next prioritize managed Service
   credential renewal/revocation and actual installed-CRL receipts, followed by
   remaining callers/hosts, Root-policy adoption, App/relay enforcement and full
   dev acceptance. Expired-management-plane recovery remains unqualified.
2. Matched backup/recovery and SDK integration.
3. Provider/hardware compatibility.
4. Staging, independent custody and recovery qualification — deferred.

This checkpoint is committed locally. No additional backup push, PR, CI dispatch,
workload restart, database reset or staging change was performed.

## Managed Service early renewal contract

Routine renewal remains due at two-thirds of the actual certificate lifetime.
Add an operator-requested early renewal for the Account Manager identity owner so
planned key rotation can happen before that deadline. The `pkimanagement` process
accepts SIGHUP from its local process operator and queues a renewal on its existing
single owner. This is process-control authority, not a new HTTP endpoint, human
MFA flow or remote permission. It does not change clocks, rewrite identity state,
shorten issued certificates or restore bootstrap credentials.

Early renewal requires an already installed, currently registry-admitted identity.
Reuse the normal authenticated renewal route, private key/CSR generation, saved
request/CSR retry, issuer verification and durable installation. Concurrent timer
and operator requests serialize; queued duplicate signals coalesce. A failed or
lost response retains the same pending request for subsequent retry. Revoked,
expired or otherwise unavailable current identity cannot renew or fall back to
bootstrap. Installing the replacement invokes the existing connection eviction
callback so issuer/controller connections and open responses cannot retain the
old key. The ordinary timed path remains unchanged.

Qualify this first on the actual dev Account Manager owner, with its retained PVC
and bootstrap absent. Deploy only the owner container image, retain the API and
worker images, and persist that independent image pin. Capture old public registry
identity and private-state hash, send one signal, and reconcile exactly one new
successful renewal, changed public key, verified managed human API, bootstrap-free
restart and Device mTLS/MQTT. Never copy a managed private key out of its owner.
This is operator-driven renewal evidence; timed due-boundary coverage remains in
tests, and live revocation/CRL receipts remain separate required acceptance.

## Dev managed early-renewal checkpoint

Video Cloud commit `ec631d7` adds guarded operator renewal through the existing
owner. Local Service identity and managed Account Manager suites passed, including
lost-response/restart retry without a second signature, missing-current/denied
identity refusal, early key replacement and closure of an actual open response
stream. Management/Service suites passed under the race detector; controller and
certissuer application suites and relevant vet checks also passed with a
disposable local PostgreSQL fixture. That test container was removed.

The dev-only owner image is
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:7bae6526454b53be56d7b847c2498597719abc54ae4325fe00de01b4e4fed556`.
Only Account Manager's `pkimanagement` container changed. Its API/init-container,
all workers, controller, certissuer and other monitored workload images remain
unchanged. The independent `PKI_ACCOUNT_MANAGER_OWNER_IMAGE` pin and complete
scoped Deployment overlay persist the change; the original identity PVC remains.
The first build's absent normalized runtime metadata produced a staging-labelled
manifest; no staging deployment was attempted. The retained `build-dev/` evidence
uses explicit dev metadata, verified `video-cloud-dev` manifest and registry
linux/amd64 digest from clean committed service source. Earlier build diagnostics
remain private and are not promoted as dev deployment evidence.

The [maintained renewal procedure](../../scripts/pki-service-dev/RENEWAL.md) passed
all three dev phases under
`~/.config/rtk_cloud/dev/pki/service-renewal-20260908-1/`: owner-only adoption,
operator renewal/bootstrap-free restart, and final verification. The image
upgrade first preserved the original identity exactly. One real SIGHUP then
produced exactly one additional successful issuance through the authenticated
`service:account-manager` caller:

- Request: `2e086dcc-e132-43d5-8f65-9aff5796c36d`.
- Installed leaf: `0dce6e54234195ec9c1891e34e836f6bc7204628ca4f0faddd37ea8e69f5ef37`.
- Public key SHA-256: `c46d3ebd6cb2e07455308551603c482bce522a9e245f0306052fdb9a94b5a0be`.
- Private-state hash: `78a2e065d5d3d2b76b006cc19606bcd33a8d22b19148c80bcfc18f768ebabd72`.

An owner-local probe confirms the installed certificate matches its private key
and reports only public metadata/state hash. No managed private key left the pod.
The changed key/leaf, same Root and subject, no pending request, stable replacement
after restart, ordinary human PKI API calls, key-mount separation and Device
mTLS/MQTT ACL/QoS1 all passed. Final audit confirmed persistence and unchanged
other images. Evidence has no group/other access. Local validation also passed
42 Python tests, the Go probe suite (including public-only state inspection) and
Go vet. CRL maintenance now selects the actually installed fingerprint rather
than incorrectly assuming one historical issuance per subject.

The original leaf remains registered and unrevoked; retiring it through the
normal governed revocation/publication path is the next priority. This checkpoint
does not claim live old-leaf revocation, installed CRL receipts, active-session
revocation cutoff, natural timer execution in dev or post-expiry recovery. No
clock/private-state edits, restored bootstrap trust, database resets, branch
pushes, PRs, CI dispatches or staging changes were performed.

Active milestone progress is approximately **60%**, with **1/6 work groups
complete and 5 open**. The same four broad milestones remain:

1. Trust consumers/live sessions — next retire the replaced Service leaf and
   qualify CRL receipts/revocation; remaining callers/hosts, Root-policy adoption,
   App/relay enforcement and full dev acceptance remain open.
2. Matched backup/recovery and SDK integration.
3. Provider/hardware compatibility.
4. Staging, independent custody and recovery qualification — deferred.

The earliest active dev CRL deadline remains **2026-09-11T06:54:13Z** (Device Root).

## Service client revocation receipts and self-hosted startup

Retire only the replaced Account Manager leaf after verifying the installed
replacement, current admission, original PVC and unchanged state. Use ordinary
human-authenticated revocation and provider-publication APIs. Preserve one stable
reason/fingerprint across retries. Publication alone cannot finalize revocation:
require the current signed CRL to cover the old serial, exact installed receipts
from certissuer and controller, and continued admission of the replacement.

Service client CRL consumers already require direct registry access to validate
their installer. Use that existing scoped read connection to obtain their signed
CRL records during preparation, avoiding the controller's dependency on its own
not-yet-listening HTTPS endpoint. Keep pinned authorities, signature/freshness
validation, persisted monotonic floors, revocation preservation and the installer
check against the current registry version unchanged. Other generic remote CRL
consumers retain their existing fetch path. Failure to read/validate/install
must fail closed; no empty, stale or unsigned startup fallback is allowed.

Receipts still use the existing authenticated HTTPS consumer identity and exact
controller authorization. They occur only after durable installation and a
successful real listener sweep; do not insert acknowledgment rows directly or
fabricate receipts from publication. Store each listener's Root/intermediate CRL
state in its retained identity PVC, isolated from its private identity file.
Roll out certissuer first and demonstrate that the missing controller receipt
keeps finalization blocked, then enable the controller and require both receipts
before finalization. Verify persistence after restart, current managed human API,
unchanged identity keys/images outside the two listener owners, and Device
mTLS/MQTT. Existing bootstrap consumer transports remain a separate adoption gap;
this does not qualify managed consumer credentials or live revocation of an
actively connected leaf whose key has already been discarded by renewal.


## Dev replaced Service leaf retirement checkpoint

Video Cloud commit `7ea6a17` prepares Service client CRLs from the existing
registry connection before a self-hosted listener starts. Signature, issuer pin,
freshness, persisted monotonicity and runtime installation checks remain in place.
The listener acknowledges only while serving and after a successful sweep; its
receipt still travels over its actual authenticated management connection.
The Service integration suite covers blocked management transport during local
preparation, no acknowledgment before serving, failed-sweep acknowledgment
suppression and active-stream cutoff. Relevant Go suites, race checks and vet
passed with a disposable PostgreSQL fixture, which was removed.

The maintained [retirement procedure](../../scripts/pki-service-dev/RETIREMENT.md)
records dev evidence under
`~/.config/rtk_cloud/dev/pki/service-retirement-20260909-1/`. The original Account
Manager leaf `eff6d595845860cd63938b047578811cbebeda1f41a9605c7345c2c988bcc849`
was revoked through the ordinary human PKI API after checking the installed
successor. Repeating that request preserves one revocation audit. Provider
publication produced signed Service intermediate CRL **7**, digest
`d1f19a181e5fd6b6c7abb39e9ea852bc44d88882df7de70837cac8f8da060291`, valid from
**2026-09-08T16:19:23Z** until **2026-09-11T16:19:23Z**. Its serial coverage,
preserved prior revocations and idempotent publication passed. Finalization
returned 409 before publication and again without the listener receipts.

Certissuer adopted the dev-only image
`ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:e32f6a2b57b108d3b4aae60a199fddc496df59a42d5166dbadeaa96611dc9e7f`.
The build used clean service source, explicit dev metadata and a verified
linux/amd64 registry digest. The first receipt verification failed on Python 3.9's
handling of PostgreSQL's `+00` time-zone suffix. A first recovery stopped because
Kubernetes had defaulted the ConfigMap file mode. The maintained recovery now
normalizes the timestamp and compares the saved API-accepted template, including
defaults. Both failed reports remain; `certissuer-recovery-2/` passed without
recreating resources, signing or republishing. Exact Root/intermediate receipts,
continued 409 while controller was missing, bootstrap-free restart, private-key
preservation, managed human API and Device mTLS/MQTT ACL/QoS1 all passed.

The helper also fixes a CRL/identity inspection method-name collision that could
break maintenance after managed renewal. The CRL cryptographic inspector now has
its own name, preserving owner-local inspection of the currently installed leaf.

The `controller/` phase passed on the same pinned image. Its local signed-CRL
preparation permitted startup before its own HTTPS endpoint was available.
Both exact listener receipts then released finalization; the controller's
bootstrap-free restart preserved its identity and retained CRLs. The Account
Manager successor remained unchanged and managed API/Device checks passed.
Public manifest `pki-service-client-crls`, private per-listener PVC state, scoped
Deployment overlays and the two independent image pins persist the rollout.
Account Manager API/owner, all workers and other monitored workload images stayed
unchanged. No managed private key was exported.

Final `verification/` passed: exact signed old-serial coverage, both installed
Root/intermediate CRLs and receipts, finalized digest, managed identities,
scoped persisted Deployment templates, unchanged other workload images and
Device mTLS/MQTT. A separate `runtime-persistence-audit.json` confirms that both
running image IDs match the pinned digest and the public ConfigMap, image pins
and Deployment templates match the saved dev configuration. Evidence directories
and files have no group/other access. Local helper validation passed **46 Python
tests**, including timestamp parsing, recovery drift refusal and post-renewal
CRL maintenance.

The old managed key was discarded during renewal; this checkpoint does not claim
live TLS rejection using that key or cutoff of its active sessions. Actual
consumer acknowledgment transports still use separately trusted bootstrap
credentials. Their managed adoption and active Service session revocation remain
next priorities, followed by the other unchanged work groups. No database reset,
branch push, PR, CI dispatch or staging operation was performed.

Active milestone progress is approximately **65%**, with **1/6 work groups
complete and 5 open**. Four broad milestones remain:

1. Trust consumers/live sessions — active; remaining managed consumer callers,
   active Service revocation, other transports/hosts, Root-policy adoption,
   App/relay enforcement and complete dev acceptance.
2. Matched backup/recovery and SDK integration.
3. Provider/hardware compatibility.
4. Staging, independent custody and recovery qualification — deferred.

The earliest active dev CRL deadline remains **2026-09-11T06:54:13Z** (Device Root).
No automatic CRL maintenance or expired-management-plane recovery is claimed.

## Governed listener egress contract

The managed listener server leaves and installed Service CRLs do not govern the
listener's own outgoing identity. Certissuer and PKI controller must replace their
separate static consumer credentials before their CRL receipts or server-renewal
calls can be called fully governed. Each listener therefore owns one additional
Service client identity: `service:certissuer` or `service:pki-controller`.

Each client key is generated and retained only in that listener's existing
identity PVC, in a distinct private state file from the server identity and CRL
state. A `serviceidentity.Manager` uses the approved certissuer origin, Service
Root pin, registry admission and normal two-thirds renewal rule. Its dynamic
client-certificate callback supplies both the controller receipt transport and
the managed server-renewal transport. Replacing this client identity evicts all
owned outgoing HTTP connections before any subsequent request. The server leaf
and client leaf must never share a key or state file.

Initial enrollment needs a deliberate, one-time bootstrap operation because
certissuer cannot issue its own first client leaf while its replacing Pod has not
yet started serving. A short-lived helper runs against the current healthy issuer,
generates the client key inside the selected listener PVC and stores the resulting
managed state there. It uses only the recorded legacy bootstrap credential while
the issuer's provisioner policy is temporarily narrowed to the selected helper
identity. The helper is not a long-lived signer and cannot export the generated
key. Before normal listener rollout, verify exact registry issuance, state/key
correspondence and expected subject. After both listeners have switched, reject
the two legacy credentials, remove their Secrets, mounts and provisioner trust,
then prove bootstrap-free restarts and the managed receipt/renewal paths.

The controller is rolled out after certissuer's managed egress succeeds. Both
deployments use a dynamic identity only when every identity, origin and root-pin
setting is present; partial configuration fails closed. Existing static transport
remains the explicitly supported compatibility path only until this migration is
qualified. No staging change, external key copy or change to Device trust is part
of this dev-only contract.
