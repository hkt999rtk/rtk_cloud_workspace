# Dev PKI prerequisite for Product service registration

This procedure is limited to `video-cloud-dev`. It prepares the private
Account Manager registration listener and six separate registrar identities.
It does not activate Product writes or strict device grants. Keep the existing
Service Root, retired issuer evidence, CRL ConfigMaps, identity PVCs, and live
PKI workloads intact throughout the change.

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

1. Request, approve, provision and activate a successor **Service**
   intermediate under the existing Root. The exact client policy is the
   sorted union of the existing nine subjects and
   `service:mqtt`, `service:shadow`, `service:webrtc`,
   `service:video-storage`, `service:logger`, `service:ota`. The server policy
   is the existing two DNS names plus the Account Manager listener DNS above.
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

Run the canonical read-only credential check from the reviewed workspace.
Require PASS for live PKI configuration, signed current Service and OpenBao
CRLs, Service registry inventory, all seven Secret cross-checks, Account
Manager private listener mTLS, six workload approvals and lease registration,
and a denial probe for an unapproved certificate. A ready Pod or structurally
valid ConfigMap alone is insufficient. On any failure, leave Product writes
and registrar flags disabled, keep audit/issuer/Secret history, and restore
the previously recorded workload images/settings without disabling strict
grant enforcement.
