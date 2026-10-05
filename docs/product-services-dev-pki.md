# Dev PKI prerequisite for Product service registration

This procedure is limited to `video-cloud-dev`. It prepares the private
Account Manager registration listener and six separate registrar identities.
It does not activate Product writes or strict device grants. Keep the existing
Service Root, retired issuer evidence, CRL ConfigMaps, identity PVCs, and live
PKI workloads intact throughout the change.

## Public CertIssuer mTLS qualification

An internal managed App socket probe does not qualify the public CertIssuer
hostname. When `CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED=true`, the public path
must preserve the original caller's direct mTLS connection. Terminating TLS
at ingress and opening an unauthenticated HTTPS upstream returns a gateway
failure; giving that upstream the ingress operator's own client identity does
not authenticate the original caller. Do not enable trusted identity headers
as a repair for this mode.

The post-deploy `deployment check --phase post-deploy` / `secrets verify` live PKI check now inspects
configured nginx CertIssuer ingress routes for TLS passthrough, a direct
`certissuer` Service backend in the Video Cloud namespace, and coverage of the
public host by the managed serving DNS configuration. A terminating route,
the HTTP ExternalName bridge, or an internal-only managed DNS configuration
fails this structural check. The read-only pre-deploy checker instead assesses
the desired configuration and whether an owned legacy route can safely migrate;
an old terminating route alone does not block a planned upgrade. An unapproved
or missing public serving identity still blocks routing migration. Neither
result by itself proves public connectivity, controller
passthrough enablement, the loaded certificate, an approved issuer policy, or
successful authenticated requests. Preserve the release's required endpoint
list separately, including when an ingress is absent.

The ingress-nginx TCP passthrough implementation sends traffic to the backend
Service ClusterIP, rather than its individual endpoints. Follow its
[TLS passthrough documentation](https://kubernetes.github.io/ingress-nginx/user-guide/tls/#ssl-passthrough)
and verify `--enable-ssl-passthrough` on the selected controller. Do not reuse
the ExternalName HTTP bridge for this path.

For an internal-only serving certificate or immutable issuer DNS policy,
prepare a successor Service intermediate under the current Root. Preserve all
current server names and Service subjects and add only the required public
hostname. In Dev configured-operator mode, the same environment operator may
request, review, authorize and execute the exact operation through the normal
`/operations/{id}/authorize` route; a second `pki_admin` is not required. Compare
the configured operator/signer bindings and server-returned request digest.
Never edit registry policy or fabricate authorization records through SQL. Install the successor's signed bundle and CRL
evidence for every affected consumer before identity adoption.

After that policy is approved and usable, reconcile the managed server identity
through its supported enrollment/renewal path, preserving predecessor state
and the internal name needed by existing callers. Qualify the actual public
SAN, Service Root trust and live reload before changing the route. Persist a
reviewed renderer configuration for the direct passthrough Ingress, then use the
deployment-owned route migration described below to remove a recognized owned
legacy route with current API preconditions and retain rollback metadata.
Unrecognized routes require an explicit reviewed handoff. Public Service mTLS clients must verify the approved Service trust
domain and public SNI; the edge's Web PKI certificate is not the backend's
managed identity.

Require both the internal safe App probe and an approved client's safe public
incomplete request to reach HTTP 400 `user_id_required`, without issuing a
certificate. Independently verify that an anonymous public request is denied
by client-certificate authentication and that other public routes remain
healthy. Leave the release NO-GO until these checks and configuration
persistence pass.

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
   settings still fail. When a Service bundle manifest is configured, each of
   its issuer IDs must also appear in the same listener's CRL manifest. Install
   the successor's signed CRL evidence before using identities renewed onto it;
   current older CRLs alone cannot admit the successor. Workload readiness and
   adoption of the current CRL in
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

1. Request, approve, provision and activate a successor **Service**
   intermediate under the existing Root. Set its exact client policy to these
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

   Install the successor's exact OpenBao service-client and server roles, and
   distribute its public bundle/CRL through the existing six Service trust
   consumers. Wait for their authenticated receipts. Keep the predecessor
   retiring until its issued certificates and CRL obligations drain.
2. Reserve a new deployment bootstrap session with the successor issuer,
   reviewed Root pin, a new deployment ID and session UUID. Render its Job
   using `render-staging-controller.py --phase service-bootstrap
   --environment dev --service-bootstrap-state-claim
   pki-service-bootstrap-product-dev-<reviewed-id>` plus the required issuer,
   subject, OpenBao CA and image arguments. Review the generated PVC name and
   Job mount before applying. Do not point at `pki-service-bootstrap-state`.
   The bootstrap identity is short lived; retry only the same session and PVC.
3. Issue one registry recorded client certificate for each of the six exact
   subjects. The dedicated listener server certificate must cover the exact
   private DNS above. Keep each private key in its own protected state and
   verify the registry receipt, issuer chain, purpose, validity, and current
   issuer CRL before Secret installation. The Account Manager namespace needs
   `account-manager-service-registration-tls` with `tls.crt`, `tls.key`,
   `client-ca.crt`, `client.crl`. The Video Cloud namespace needs the following
   six separate Secrets, each with `client.crt`, `client.key`, `server-ca.crt`:

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
4. After each target has loaded and verified its own issued identity, record
   its bootstrap acknowledgement. Seal the session only after all six have
   acknowledged; remove bootstrap-only environment inputs from ordinary
   workloads. Then, as an authenticated platform administrator, approve the
   six exact `(environment, subject, issuer fingerprint, service, instance,
   allowed option)` bindings through Account Manager's
   `POST /platform/service-workloads`. The fingerprint is the issuing
   intermediate's SHA-256, not the leaf fingerprint. Registration and
   publication remain separate steps.

## Go / No-Go before Product rollout

Run `scripts/check-deployment-health.sh --environment dev --fast` from the
reviewed workspace for current environment health; use
`scripts/check-deployment-preflight.sh --environment dev --fast` separately
before a planned deployment. Neither replaces the required endpoint probes.
Require PASS for live PKI configuration, signed current Service and OpenBao
CRLs, Service registry inventory, all seven Secret cross-checks, Account
Manager private listener mTLS, six workload approvals and lease registration,
and a denial probe for an unapproved certificate. A ready Pod or structurally
valid ConfigMap alone is insufficient. On any failure, leave Product writes
and registrar flags disabled, keep audit/issuer/Secret history, and restore
the previously recorded workload images/settings without disabling strict
grant enforcement.

## Public CertIssuer configuration and rollout boundary

The maintained ingress renderer now places `certissuer-public-mtls` in the
Video Cloud namespace, targets `certissuer:9443` directly and sets SSL
passthrough. The Helm controller configuration persists
`controller.extraArgs.enable-ssl-passthrough=true`. Its Service-domain server
identity supplies public TLS; the renderer does not attach the edge Web PKI
Secret to this route. Other public HTTP routes retain their bridges.

For a changed server SAN policy under the same Root, enroll into a distinct
private state path rather than changing or reseeding existing state. The
`serviceidentity-bootstrap server-bootstrap` owner mode reads only the selected
workload's current managed client, verifies registry admission and keeps the
new server key on that workload's own PVC. Its owner settings are documented in
the Video Cloud PKI runbook. Retain the fixed request ID and pending state on
an uncertain result; inspect the public owner helper and registry receipt before
retrying. Do not run it again after successful installation.

Before adoption, add the successor to every affected Service CRL manifest,
preserving old issuers and each owner's existing private state parent. Let the
actual consumers fetch and acknowledge signed CRLs. Check the served leaf and
normal internal path before handing off the route. The deployer now automatically
recognizes the owned historical `video-cloud-staging-certissuer` and shared
`video-cloud-staging-https` Ingress objects in the selected ingress namespace;
their names are historical and their stack ownership must match this exact
environment. It removes only the CertIssuer host/root path with its expected
backend and preserves unrelated shared routes. Unknown owners, extra paths or
different backend intent stop automatic migration.

The read-only pre-deploy check reports this migration as a prerequisite rather
than failing because the old route terminates TLS. Deployment revalidates the
installed serving identity and current inventory before mutation, uses fresh
UID/resource-version preconditions and saves a restore journal under
`runtime/artifacts/certissuer-ingress/` (or the selected artifact directory).
After migration it verifies canonical routing; a repeated converged deployment
leaves the Ingress objects intact. Known failures restore only resources still
matching the recorded migration result. An uncertain outcome or concurrent
drift preserves the journal and blocks retry for explicit review. Never discard
that journal to bypass the guard. Never disable
the admission webhook to allow duplicate hostname/path ownership.

Routing migration does not authorize replacing stored CA state, creating a
successor issuer or reissuing the existing managed server certificate. A SAN
or issuer-policy gap must be addressed through the approved owner enrollment
and issuer lifecycle before route adoption. Public/internal authenticated and
anonymous-denial probes remain separate acceptance evidence; see
[the workspace routing procedure](deployment-operations.md#certissuer-route-convergence).

Readiness qualification does not authorize rebuilding the existing managed PKI
workloads with the legacy whole-Deployment renderer. For the six core image
update, the deployment handoff must preserve the actual managed environment,
sidecar images, Secret/ConfigMap bindings, identity state and PVCs, applying only
the selected image changes. Any requested configuration/schema change needs
its own reviewed migration.
The default full create/upgrade pre-deploy checker and legacy full deployment
now reject this unsupported replacement before dependency resource mutations,
including when CertIssuer has no available replica. Reviewed targeted
`provision --deploy --workloads ...` and route-only `provision --dns` remain
distinct operations; they do not authorize whole-Deployment PKI reconstruction.

The public CertIssuer listener requires an admitted Service client before HTTP.
TLS passthrough cannot inject Nginx crawler headers or serve its robots/sitemap
snippets; anonymous crawlers fail the workload TLS handshake. Other private
HTTP-terminating ingress keeps its existing noindex/robots policy.
