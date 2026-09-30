# Dev PKI prerequisite for Product service registration

This procedure is limited to `video-cloud-dev`. It prepares the private
Account Manager registration listener and six separate registrar identities.
Registration is not Product activation, billable usage, or a price-card release.
Keep the existing Service Root, retired issuer evidence, CRL ConfigMaps,
identity PVCs, and live
PKI workloads intact throughout the change.

## Verified registration checkpoint (2026-09-30)

The private Account Manager listener is ready on Service port `8443` to Pod
port `9444`. The six approved, separate client identities are installed and
`mqtt`, `shadow`, `webrtc`, `video-storage`, `logger`, and `ota` each have a ready,
unexpired lease under the reviewed instance ID below. A valid `service:mqtt`
certificate attempting the OTA registration tuple received HTTP 403
`service_registration_denied`: TLS validity alone cannot authorize another
service. The canonical dev read-only credential check passed 10/10, and
`secrets verify --environment dev` passed after cleanup.

Deployment session `d3cfa4fe-d6bb-4ff2-93a4-ec7bc41f32f5` reached its
30-minute deadline before all runtime targets were verified. Its original
issuance receipts remained successful, unrevoked, and inside the original
signing window. The fixed-version certissuer recovery permits each exact
subject's **late acknowledgement only** against that original receipt; expiry
still denies new certificate signing. An operator Job using the PKI migration
database credential recorded all six genuine runtime acknowledgements and
sealed the session. PostgreSQL read-back confirmed `sealed`, six
acknowledgements, and the original expiry retained. Temporary bootstrap
listener settings, the expired local bootstrap key/certificate, and the
one-shot Job and 1 GiB PVC were removed; durable issuance and audit evidence
remain. The sealed session must not be reused.

The operator's PKI authorization is minimal append-only control state in PKI
PostgreSQL; ordered state transitions are in `pki_audit`. The controller also
emits a secret-free event for Loki search. A Loki delivery gap is investigated
against PostgreSQL and cannot grant signing authority. This division and its
failure behavior are defined in the
[operator-authority design and test plan](design/pki-operator-authority-test-plan.md)
and the [normative Platform PKI contract](../repos/rtk_cloud_contracts_doc/platform_pki.md).

The live service catalog still has only `mqtt` active; the other five services
are suspended. `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES=false`, and
the current Video Cloud API has strict OTA Product entitlement checks enabled.
The legacy `video-cloud-otaregistrar` is stopped. The independent
`video-cloud-otaservice` is ready and owns the published OTA v2 manifest, but
the device edge has only passed route and unauthenticated-denial checks; core
cutover and billable OTA receipt delivery have not been qualified in dev. Core
clip direct upload also remains disabled. Follow each
service's route and authorization gates in
[deployment operations](deployment-operations.md) before activation; do not
activate all suspended services merely because their leases are ready.

The dedicated dev OTA registration step is `deployment ota-service-rollout`.
It requires an immutable image, the old registrar fully stopped, and the
existing Product, identity, storage, runtime Secret and private listener gates.
It updates only the OTA service and its required policies, preserving the
PKI-managed core API and log ingester. Publication, Product access, billable
receipt reconciliation and price activation remain separate checks.
The next narrow edge step is `deployment ota-device-edge`: it checks the
existing device mTLS host, expected app CA, verified-certificate forwarding,
core route and ready independent OTA endpoint before adding the OTA route and
port 18084 ingress policy. The command is dev-only and does not enable core
cutover or activate OTA in the service catalog. Its exact flags and rollback
order are in [deployment operations](deployment-operations.md).

## OTA dev checkpoint (2026-09-30)

The frozen workspace rollout merge is `a8e39adf5d3813c0726cb54d7c93bf3c8c986d8e`.
Video Cloud uses independent OTA service merge
`a1fffd65686a26c7a0281c89367de37b7f6e90cd` and immutable API image
`sha256:096d91197cd0e372aaf5ca02014e1c8f4f8dd736a930c04483283c8054e9b97a`.
Account Manager uses suspended-publication merge
`4d1e6b53c81837bd0d66a5d6f1fec8e5c3cc94b0` and immutable app image
`sha256:a00bb92eb06a63d1fc16d151af2e565c69258481bb51b2c41bb268ef5def9222`.
The service-registration design clarification is merge
`66c28b18ccc1961ab1045410742c7e946fba547c`.
The dev operator SecretStore pins both exact image digests; read-only GHCR
image qualifications passed before each rollout. Rollback image values were
saved locally by the operator.

`video-cloud-otaregistrar` has zero replicas and no Pod. The independent
`video-cloud-otaservice` has one ready Pod and a ready private Service endpoint.
Its dedicated `service:ota` identity registered manifest v2 with endpoint
reference `ota-service`. An authenticated mTLS publication advanced the OTA
catalog to revision 28 and selected v2. PostgreSQL read-back confirmed
`ota|2|suspended` and a fresh ready `ota-service-0` lease. The publication
did not activate OTA or enable Product writes; dev currently has zero OTA
Product profiles and entitlement snapshots. The temporary publication
certificate files and local port-forward were removed.

Frozen workspace PR #618 merged at `20bee2c38c13d45c97b940e85edd467629c09f50`.
After the read-only dev credential check passed 10/10, the operator saved the
old `LKE_OTA_SERVICE_EDGE_ENABLED=false` value and set only that environment
flag to `true`. `deployment ota-device-edge` added an OTA-only port 18084
NetworkPolicy, private bridge Service and `/v1/device/ota/` Prefix route on
the existing device mTLS ingress. The live ingress still pins the dev app CA,
requires client certificates at depth 2 and retains its core `/` route.
The core API and independent OTA Service stayed 1/1 Ready; the old registrar
stayed at zero replicas. A public request without a client certificate
returned ingress HTTP 400 with the expected missing-certificate response; a
private OTA handler request without identity returned HTTP 401. The temporary
port-forward was stopped. These are denial and route-configuration checks,
not an authenticated device or billable download qualification.

Remaining dev qualification is an authenticated OTA-enabled Product and device
flow through object-URL firmware delivery, download/task receipts, usage fact
outbox and Billing acceptance, then period-seal and pricing checks. Keep the
core cutover flag off and OTA suspended until those checks and the user-visible
billing terms are verified. Product writes and the OTA price card are still
off; no OTA usage should be charged. CDN delivery is a separate later
expansion.

The fixed Video Cloud branch now contains the
[operator Manifest V1 signing tool](../repos/rtk_video_cloud/docs/ota-manifest-operator.md)
from PR #737 (merge `4ca9d1f13fa77d65c6aa9fb82805d00f7bb1ecfd`). The
operator created a dev-only `dev-ota-acceptance-20260930` signing key under
the local dev SecretStore with mode `0600`; the private key is not in Git or
Kubernetes. The dev environment override preserves its prior public key and
adds this new public trust entry. At this checkpoint, the updated trust map
has **not** been applied to the OTA Service or core API, no release has been
signed, and no OTA Product has been created. The first authenticated flow can
use the independent OTA Service's protected operator endpoint through a
temporary private port-forward while core cutover remains off; device traffic
must still use the mTLS edge. Stop the forward after verification.

## PKI dev checkpoint (2026-09-29)

The frozen operator-authority service versions are deployed in dev: PKI controller
`dda6fc79cce0ce4b50c7babab0649fe489445188b0431a79d1f1d7e63b86b77c`,
Account Manager `821a10325fc0a5fa62e0bcc89291a69c3df29e234d0e636bf0e9b6dd5f7e462a`
and Cloud Admin `7fe6d64b7307f0df3f3a8b24662f6df3b956532add5cd43361e906a2311b5c96`
(all SHA-256 image digests). The dev SecretStore pins the active Platform Admin
operator ID and signer reference; Account Manager and the controller use the same
ID for mutating PKI operations. This is a dev cutover only.

The preserved OTA Service operation
`b4d42f12-6f21-45c6-b8d2-3df4930a88bd`, request digest
`d010a3b9a5193a0e001c6ed95b3f70a42e1d1b82c544fc639a50a6b1100edd99`,
is `active`. Its successor issuer is
`cf348f82-f4cc-434e-a59d-c37eee8222cf` (version 10), under the existing
Service Root. The public certificate fingerprint is
`7b1d6cf35e8c0477385bec3f61348b8ed4077d7da0e918ff9f2e28cdda0921f5`.
The prior version 9 issuer is `retiring` and retains its 12 issued Service client
certificates. Its separate read-only inventory passed on 2026-09-29 with zero
pending, invalid, unpublished-revocation or missing-acknowledgment records.
Repeat that inventory before removing predecessor trust. The immutable ConfigMap `pki-service-bundles-v10-cf348f82`
preserves all five previous authority references and adds version
`5802c2123ea7c8b53b10875b0fcaec7f11686772114b5e863fc703d7bd6f3f0d`.
`certissuer` and `pki-controller` each validated the installed bundle and
acknowledged that exact version. The controller's inventory pin now names version
10. All four relevant Deployments are ready, and the canonical
`secrets verify --environment dev` passed with the frozen workspace verifier fix.

The first provision attempt failed after recording `provisioning`: the new exact
OpenBao mount was absent and the controller role lacked its issuer-specific ACL.
Do not repeat `provision` or reset the operation at that point. The operator
confirmed both mount and key inventory absent, installed the rendered exact-mount
controller, server and Service-client policies, and appended them to the existing
Kubernetes auth roles without changing other constraints. One internal P-256 key
was then generated at the original mount, and `reconcile` registered its CSR under
the original operation. The reconciled CSR's public key matched the first CSR.
The protected dev Service Root signed it with reviewed request, CSR and parent
fingerprints; import and authenticated consumer acknowledgments completed before
activation. PostgreSQL authorization/audit records and the matching secret-free
Loki event were verified. For any future new issuer, install and verify its exact
OpenBao ACLs **before** provisioning. If a provision result is uncertain, inspect
provider state and reconcile the original operation; never generate another key
without confirming the exact mount/key inventory and recording the recovery.

Before the new bootstrap session, the dev `pki-service-bootstrap-dev` Kubernetes
auth role still carried only the retiring v9 signer policy. On 2026-09-29 the
operator checked the exact v10 `sign/service-client` ACL, ran the canonical dev
read-only credential preflight (10/10 PASS), and changed only that role's policy
to `pki-service-client-dev-v10-cf348f82-f4cc-434e-a59d-c37eee8222cf`. Its
ServiceAccount, namespace, `openbao` audience, and other role fields matched the
pre-change values on read-back. This policy update did not create a bootstrap
session, sign a leaf, or enable Product writes. The operator also verified the
**live** certissuer HTTPS listener at
`certissuer.video-cloud-dev-video-cloud.svc`: its leaf SHA-256 is
`ba3bec99d77bc82e95a619b107a5d28de0e3f96aebc27acb824e7a3604106e77`,
and its presented chain validates to the pinned Service Root. The public local
`pki/services/service-root.crt`, `issuer-server-ca.crt`, and
`registration-server-ca.crt` files now contain that verified Root. The legacy
`certissuer-runtime` Secret's Service CA is not the live listener's issuer;
check the served certificate and chain rather than copying that old CA.
The live certissuer gateway DNS allowlist lacked the successor policy's exact
`account-manager.video-cloud-dev-account-manager.svc.cluster.local` name. A
resource-version and old-value guarded one-field patch appended that name while
preserving its existing names and image; certissuer rolled out 1/1 Ready. The
canonical live `secrets verify --environment dev` and Account Manager→certissuer
mTLS probe then passed. The exact list is saved in this environment's
`operator/env/CERT_ISSUER_GATEWAY_DNS_NAMES` key. Before any later certissuer
update, reconcile the live list with that environment key; the legacy whole
Deployment renderer is still unsuitable for this PKI-managed workload.

The certissuer code on the frozen Video Cloud base now includes the six-subject
bootstrap trust fix from PR #732 (merge commit
`fc13fdbb1e80ece35b9203931210b549b1bcbe9e`). The listener takes the exact
comma-separated `CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS` set while the
legacy singular input remains unset. It still requires the reviewed session,
`service:deployment-bootstrap` caller, and pinned Service Root CA, and rejects
missing CA or an empty subject set. Focused Go tests and the governed service
coverage gate passed before merge. CI published the API image from that exact
commit as digest
`ddf918ae2513579f83efe8a4fa7ccad0b27c84e2cf4f3c3b44e40f9e4710ff78`;
the digest was independently checked against GHCR. The same release workflow
later failed on the unrelated EMQX package push with GHCR 403, so it has no
successful overall release result. A guarded dev patch updated only certissuer
and its four temporary bootstrap settings. The Deployment reached 1/1 Ready,
and Account Manager mTLS reached request validation. The old workspace
credential verifier rejected this separate-Job, plural-subject mode because it
required the listener itself to hold the bootstrap key. The corrected verifier
checks the exact session UUID, caller, unique subjects and pinned Service Root
while keeping legacy self-bootstrap checks; its focused regressions and the
actual dev `secrets verify --environment dev` passed. This was the preparation
state before the session and registrations recorded in the 2026-09-30 checkpoint
above. Do not infer OTA Product enablement or billing from the active CA or
registered lease.

The registration listener needs a server certificate for the exact private
DNS. The bootstrap Job has a short-lived `service:deployment-bootstrap` client
certificate, which the ordinary gateway caller regex deliberately excludes.
Video Cloud PR #734 adds a separate, temporary route for this one server
certificate: verified direct mTLS, `purpose=server`, one exact
`CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SERVER_DNS_NAME` already on the gateway
DNS allowlist, and a matching active deployment session whose stored
bootstrap-certificate fingerprint matches the mTLS leaf are all required.
Configure the DNS setting only with that session and remove
it after sealing. An expired or sealed session denies further issuance even
while the bootstrap leaf remains valid. The issued server certificate and its
registry record follow normal validity and revocation. The one-shot server
Job uses the same protected state PVC and bootstrap NetworkPolicy label. Keep
the ordinary gateway caller regex unchanged.

## Read-only baseline (2026-09-26)

The active Service intermediate is
`c160c01f-a742-4c06-bcb7-d90be90e820b` (version 9), under Service Root
`697e8e86-5af6-4580-8456-7f91d17634f2`. The controller pins that issuer
through `RTK_DEPLOYMENT_SERVICE_ISSUER_ID` and pins Service Root SHA-256
`32bbbfd220db619ebcf54af5f62221ed49635e58ddaa42e67730676f073704eb`.
The issuer's client allowlist contains the existing nine Service subjects,
including `service:video-cloud-logingester`; none of the six Product registrar
subjects is present. Its server DNS allowlist contains only the current
certissuer and pki-controller names. The Product listener DNS is
`account-manager.video-cloud-dev-account-manager.svc.cluster.local`.
Issuer policy is immutable, so use a successor Service intermediate. Preserve
the nine existing client subjects and two server names to avoid renewal gaps.
The accepted target is one environment operator for both initial creation and
subsequent Service updates: the operator reviews the exact request digest,
uses the configured environment signer, and records the outcome. No second
human or separate `pki_admin` approval is required by the design. This
2026-09-26 baseline preceded the verified dev operator cutover above. See the
[operator authority test plan](design/pki-operator-authority-test-plan.md).

The running Account Manager sidecar uses
`PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST` from ConfigMap
`account-manager-service-crls-19caf32b00af`. Certissuer uses
`CERT_ISSUER_SERVICE_CLIENT_SERVER_CRL_MANIFEST` from
`pki-service-client-crls-fba75443073d` and
`OPENBAO_SERVER_CRL_MANIFEST` from
`pki-openbao-tls-crls-post-withdraw-0b0e251d-8286959f`. These are observed
names, not values to pin forever: reread them immediately before any rollout.
The default LKE certissuer manifest predates the managed PKI deployment; do
not replace the live Deployment with that whole manifest. Use the narrow
strategic merge patches below when one of these three CRL mounts must be
restored. They preserve other live sidecars, settings, and volumes.

The existing `pki-service-bootstrap-state` PVC is Bound and previous dev
bootstrap sessions are expired. A new ceremony must use a new deployment ID,
session ID, and PVC name. Never clear or reuse the old PVC to make a new
session succeed.

## Prepare and verify configuration without changing the cluster

1. Confirm the canonical dev kubeconfig and inspect the effective Account
   Manager `pkimanagement` and certissuer Deployment environment, mounts,
   volumes, ConfigMap names, images and rollback resource versions. Inspect
   the public Service issuer policy and consumer receipts. Never dump Secret
   data, private identity state or bootstrap keys.
2. `secrets verify --environment dev` now accepts only the three named CRL
   consumers above. Each manifest must be mounted read-only from a ConfigMap;
   its issuer must match the dev trust domain and public certificate
   fingerprint; and each state path must be on the workload's persistent
   volume. The check reads each current PVC state through the exact workload,
   verifies the signed CRL and expiry, then compares its digest and number to
   the latest registry CRL and that workload's acknowledgment. Empty, stale,
   mismatched, or unacknowledged state fails. Other unreviewed CRL or root
   settings still fail. Workload readiness and adoption of the current CRL in
   a running listener remain separate live acceptance gates.
3. If a reviewed CRL ConfigMap reference needs reconciliation, render a
   strategic merge patch with
   `repos/rtk_video_cloud/deploy/pki/render-dev-service-crl-patch.py`.
   Pass `--environment dev --target account-manager --account-service-crls
   <current-name>` for the sidecar; pass `--environment dev --target certissuer
   --service-crls <current-name> --openbao-server-crls <current-name>` for
   certissuer. Review the JSON and the current Deployment before applying it
   with `kubectl -n <exact-dev-namespace> patch deployment <exact-name>
   --type strategic --patch-file <reviewed-patch-file>`. Do not use a whole
   Deployment replacement. Confirm the three paths still point to
   `crls.json` on the original read-only ConfigMap mounts, and rerun
   `secrets verify --environment dev`.

## Issue and install the seven reviewed identities

1. Completed on 2026-09-29 for dev: the configured operator reviewed,
   authorized, provisioned, imported and activated the successor **Service**
   intermediate under the existing Root. For another environment, use its own
   configured operator and exact reviewed request; never fabricate an approval
   or directly advance registry status. Set its exact client policy to these
   16 sorted subjects. The deployment bootstrap identity must be included
   because the new bootstrap session signs through this successor issuer:

   ```text
   service:account-manager
   service:certissuer
   service:deployment-bootstrap
   service:emqx-pki
   service:factory-enroll
   service:logger
   service:mqtt
   service:openbao
   service:ota
   service:pki-controller
   service:pkibroker
   service:shadow
   service:video-cloud-api
   service:video-cloud-logingester
   service:video-storage
   service:webrtc
   ```

   Set its exact server DNS policy to these three sorted names:

   ```text
   account-manager.video-cloud-dev-account-manager.svc.cluster.local
   certissuer.video-cloud-dev-video-cloud.svc
   pki-controller.video-cloud-dev-video-cloud.svc
   ```

   Dev has installed the successor's exact OpenBao service-client and server
   roles. Its public bundle was added to the controller and certissuer listeners;
   both required bundle consumers acknowledged it before activation. The six
   Service CRL consumers and future new-leaf issuance still need their own
   current CRL and receipt checks. Keep the predecessor retiring until its 12
   issued certificates and CRL obligations drain.
   The existing dev OTA successor operation and digest are recorded in the live
   checkpoint above. Its history is preserved; do not rerun provisioning or
   activation. A new request is necessary only if approved policy content must
   change.
2. Immediately before starting a new session, read the live OpenBao
   `pki-service-bootstrap-dev` role and require its exact v10
   `sign/service-client` policy, bound ServiceAccount/namespace, and `openbao`
   audience. Do not use a wildcard or the retiring v9 signing policy for a new
   session. Reserve a deployment bootstrap session with the successor issuer,
   reviewed Root pin, a new deployment ID and session UUID. Render its Job
   using `render-staging-controller.py --phase service-bootstrap
   --environment dev --service-bootstrap-state-claim
   pki-service-bootstrap-product-dev-<reviewed-id>` plus the required issuer,
   subject, OpenBao CA and image arguments. Review the generated PVC name and
   Job mount before applying. Require the generated Job and its later
   registration-server Job to carry `rtk.realtek.com/pki-bootstrap=true`; the
   existing certissuer NetworkPolicy admits that exact label on port 9443.
   The canonical `secrets verify` preflight must pass before reserving a new
   session. While the session is active its signing-state check intentionally
   reports the active caller; rerun the full verifier after sealing. Do not
   point at `pki-service-bootstrap-state`. The bootstrap identity is short
   lived; retry only the same session and PVC.
3. Issue one registry recorded client certificate for each of the six exact
   subjects. The dedicated listener server certificate must cover the exact
   private DNS above. Its bootstrap request uses the session-bound certissuer
   route and exact DNS setting described above; the session's six client
   subject acknowledgements remain unchanged. Keep each private key in its
   own protected state and
   verify the registry receipt, issuer chain, purpose, validity, and current
   issuer CRL before Secret installation. The Account Manager namespace needs
   `account-manager-service-registration-tls` with `tls.crt`, `tls.key`,
   `client-ca.crt`, `client.crl`. The listener uses app Pod port `9444`, reached
   through the private Account Manager Service port `8443`; its existing PKI
   sidecar already owns Pod port `8443`. The Kubernetes port name is
   `service-reg` (port names cannot exceed 15 characters). The ingress
   NetworkPolicy must admit the six reviewed registrar Pod identities on
   `9444`. The Video Cloud
   namespace needs the following six separate Secrets, each with `client.crt`,
   `client.key`, `server-ca.crt`:

   | Certificate CN | Secret | Service / instance | Option |
   | --- | --- | --- | --- |
   | `service:mqtt` | `mqtt-foundation-platform-identity` | `mqtt` / `mqtt-foundation-0` | `mqtt` |
   | `service:shadow` | `shadow-worker-platform-identity` | `shadow` / `shadow-worker-0` | `iot_shadow` |
   | `service:webrtc` | `webrtc-service-platform-identity` | `webrtc` / `webrtc-service-0` | `video_streaming` |
   | `service:video-storage` | `video-storage-service-platform-identity` | `video-storage` / `video-storage-service-0` | `video_storage` |
   | `service:logger` | `logger-service-platform-identity` | `logger` / `logger-service-0` | `device_logging` |
   | `service:ota` | `ota-service-platform-identity` | `ota` / `ota-service-0` | `ota` |

   Install from protected file paths using `kubectl create secret generic`
   with explicit `--from-file` keys in the exact namespace; use a client-side
   dry run and review metadata before applying. Do not print Secret YAML,
   write a key to the repository, or reuse the existing log ingester identity
   for `service:logger`. Keep a receipt of the issuer and leaf fingerprints,
   Secret resource versions and expiry, without recording key material.
4. As an authenticated platform administrator, approve the six exact
   `(environment, subject, issuer fingerprint, service, instance, allowed
   option)` bindings through Account Manager's `POST /platform/service-workloads`.
   The fingerprint is the issuing intermediate's SHA-256, not the leaf
   fingerprint. Registration and publication remain separate steps. Start
   each target with its own identity and verify that the process loaded it and
   registered its lease. Only then record that target's bootstrap
   acknowledgement; possession of a Kubernetes Secret alone is insufficient.
   The OTA registrar probes the Video Cloud API through its internal Service
   port `80` at `/readyz/ota`; the API Pod's port `8080` is not a Service port.
   Run `serviceidentity-bootstrap ack` and `seal` from a short-lived operator
   Job with the environment's PKI migration database credential. The runtime
   `pkimanagement` sidecar uses a read-only verifier role and cannot update
   the session table. Seal only after all six acknowledgements, then remove
   bootstrap-only inputs from ordinary workloads. If the 30-minute session
   expires before runtime verification, first inspect the original issuance
   receipts and the live workload identity/lease. Expiry still prohibits any
   new signing. A late acknowledgement is permitted only for the same
   successful, unrevoked subject receipt issued within the original signing
   window; then seal after all real runtime acknowledgements. If those
   conditions fail, keep the session expired and plan a new reviewed ceremony.
   Never record a false installation or extend the signing window.

## Go / No-Go before Product rollout

Run the canonical read-only credential check from the reviewed workspace.
Require PASS for live PKI configuration, signed current Service and OpenBao
CRLs, Service registry inventory, all seven Secret cross-checks, Account
Manager private listener mTLS, six workload approvals and lease registration,
and a denial probe for an unauthorized service tuple. A ready Pod or
structurally valid ConfigMap alone is insufficient. Registration qualification
is followed by each service's separate route/cutover, entitlement, receipt and
Billing acceptance gates before Product activation or pricing. On any failure,
leave Product writes and unqualified service options disabled, keep
audit/issuer/Secret history, and restore the previously recorded workload
images/settings without disabling strict grant enforcement. The baseline LKE
renderer must refuse to replace a managed `video-cloud-logingester` Deployment
while it owns MQTT PKI identity state; use a reviewed managed patch for that
workload.
