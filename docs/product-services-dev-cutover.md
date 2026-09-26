# Product service grants: dev cutover

This runbook applies only to `video-cloud-dev`. It introduces registered
`mqtt`, `iot_shadow`, `video_streaming`, `video_storage`, `device_logging`, and
`ota` choices. Product edits create immutable grant revisions; device grants
change only through an explicit device operation or the Product apply job.
Neither the legacy backfill nor registry activation adds options to an old
Product or device.

## Current dev preflight (2026-09-26)

The read-only `secrets verify` gate is **NO-GO** before image rollout. The
running Account Manager sidecar has
`PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST`; certissuer has
`CERT_ISSUER_SERVICE_CLIENT_SERVER_CRL_MANIFEST` and
`OPENBAO_SERVER_CRL_MANIFEST`. The checked-in deployment contract rejects
these live settings until they are represented and verified in a reviewed
configuration. Do not bypass this gate or replace the running PKI workloads
with a renderer that would drop their CRL settings.

The private `account-manager-service-registration-tls` Secret and all six
registrar identity Secrets are absent in dev. The existing Service bootstrap
session did not approve the six registrar subjects. Its persisted state must
not be reused with a different subject list. Create a separately reviewed
Service issuer/bootstrap session and Secret installation procedure, then
register exact workload approvals and rerun the read-only preflight. The
steps below become eligible only after this gate passes.

## Prepare the exact dev revision

1. Build the Account Manager, Video Cloud, and Cloud Admin images from the
   reviewed commits. Keep the feature gates off during the compatibility
   rollout. Record previous image references and the current Product, device,
   production-run, and Claim Token counts.
2. Run `scripts/check-deployment-credentials.sh --environment dev --read-only`
   and verify the exact dev kubeconfig, namespaces, running images, and
   database migration level. Resolve any unrelated deployment-contract drift
   before mutating the stack. Do not use a staging credential or apply a full
   platform upgrade as a shortcut for this dev change.
3. Deploy compatible binaries and additive schema first. Keep
   `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES=false`,
   `VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=false`,
   `VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED=false`, and all new registration and
   route flags off. Keep the existing OTA API in `cmd/api`.

## Backfill and register

1. Briefly freeze Product edits and production-run creation. Follow
   [Account Manager's backfill procedure](../repos/rtk_account_manager/docs/service-grant-backfill.md):
   collect the read-only report, review its SHA-256 and every issue, apply the
   exact reviewed snapshot, and compare a second report. The historical
   option set is preserved without adding MQTT, OTA, or logging.
2. Provision the Account Manager private registration listener and its server
   identity. Provision six *separate* workload client identities for service
   subjects `service:mqtt`, `service:shadow`, `service:webrtc`,
   `service:video-storage`, `service:logger`, and `service:ota`. Install the
   corresponding Kubernetes Secrets in the dev namespace and approve each
   exact service, instance, certificate fingerprint, and option through the
   Platform workload API. The deployment preflight validates certificates,
   chain, CRL, subject, purpose, and private registry endpoint before it
   starts a registrar.
3. Enable `LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED` and then
   `LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED`. Once MQTT is active, enable
   `LKE_SHADOW_WORKER_REGISTRATION_ENABLED`,
   `LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED`,
   `LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED`, and
   `LKE_LOGGER_SERVICE_REGISTRATION_ENABLED`. Wait for each real `/readyz`
   and registered lease. Activate the
   optional manifests only after the service and its MQTT dependency are
   ready. Check offline, expired-lease, and missing-dependency catalog reasons.
4. The current dev Loki uses `emptyDir`. Create its PVC independently, then
   pause log writers and copy the current `/loki` tree from the single running
   Loki Pod into the PVC through a temporary helper Pod. Compare file counts
   and checksums. Annotate the PVC with
   `rtk.realtek.com/loki-source-pod-uid` set to the copied Pod UID and
   `rtk.realtek.com/loki-copy-sha256` set to the recorded tree checksum.
   The deployment rejects a storage switch if either annotation is missing or
   the source Pod has changed. Apply the PVC-backed Loki Deployment, wait for
   readiness, then resume writers. Deploy the updated Cloud Logger before
   enabling tiered Compactor retention. An explicitly versioned Product log
   carries one of the three low-cardinality `retention_tier` values (`7d`,
   `30d`, `90d`) and the fixed `retention_policy="product-grant-v1"`
   marker. Check a new granted log for both labels and an unversioned legacy
   log for neither before enabling the Compactor. The Compactor selectors
   require both labels. Previously stored streams may have a legacy `7d`
   label, but do not have the new marker and therefore remain excluded.
   Global Loki retention remains `0s`. See [Loki retention](https://grafana.com/docs/loki/latest/operations/storage/retention/).

Record each enabled LKE registration and cutover flag in
`cloud_env/dev/overrides/adapter.env` and commit the reviewed change at that
stage. In particular, retain `LKE_LOGGER_RETENTION_STORAGE_ENABLED=true`,
`LKE_LOGGER_SERVICE_REGISTRATION_ENABLED=true`, and both
`LKE_LOGGER_HTTP_CORE_CUTOVER_ENABLED=true` and
`LKE_LOGGER_MQTT_CORE_CUTOVER_ENABLED=true` after Logger cutover. Retain
`LKE_OTA_REGISTRAR_REGISTRATION_ENABLED=true` after OTA activation. The
Account Manager, MQTT, Shadow, WebRTC, and Video Storage registration flags
must likewise stay in the dev adapter override once enabled. Before a later
Video Cloud redeploy, resolve the tracked dev configuration and inspect the
rendered core Deployment; both Logger cutover values must remain `true`.

## Strict authorization and Product writes

1. Check whether the existing dev API has an OTA CDN base URL and whether any
   legacy signed links were issued. If so, stop issuance, rotate or disable
   the old CDN download token key/path at the edge, and verify a URL signed
   before cutover cannot download firmware. A new application check cannot
   revoke a URL already accepted by the old edge configuration. If the URL
   was never configured, record that evidence and verify new first-party
   download URLs against grant revocation instead.
2. Enable `VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED=true` and
   `VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=true` on every core replica
   and verify all are running the reviewed image and setting. Verify old
   devices without an `ota` grant cannot query, receive, or download a new
   update, including an old URL. Verify in-progress result reports still work.
   Then enable `LKE_OTA_REGISTRAR_REGISTRATION_ENABLED` for the single
   `otaregistrar` Pod, which probes the existing
   `cmd/api` process at `/readyz/ota`, and activate the registered OTA service.
   Enable the Logger HTTP and MQTT cutovers only after its registered readiness,
   pinned per-device grant check, and tiered storage all pass. Old devices
   without `device_logging` must be denied new uploads.
3. Enable `ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES=true` in the
   persisted dev deployment configuration and restart Account Manager. Confirm
   the catalog has six real entries and that a registry outage shows an error
   rather than the old fixed three-choice list.
4. Create a new test Product and devices. Verify each option both with and
   without a grant across HTTP, MQTT, App, internal dispatch, and artifact
   download. Verify 7/30/90-day labels and expiry, and that retransmission
   keeps the original expiry. Confirm that a Product edit changes only its
   current revision, leaving old devices, production runs, and Claim Tokens
   pinned.
5. Preview Product-wide apply and confirm the immutable revision and full
   nondeleted device set. Exercise a set larger than 250 devices, restart,
   lost response, repeated submit, pause, cancel, resume, retry, concurrent
   device edit, and Product revision conflict. Count an item as applied only
   when Video Cloud returns and Account Manager stores the exact applied
   revision and operation ID. Review paginated failures and downloaded results.

## Stop and recover

On a failure, stop new Product selection and batch dispatch, retain grant
revisions and audit rows, and restore the last compatible binaries and image
references. Keep strict OTA and Logger authorization enabled once cutover has
occurred; do not restore access by disabling the checks. Diagnose and retry
the same operation ID for transient delivery failures. A permission, baseline,
or Product revision conflict requires a fresh preview. Do not automatically
reverse successfully applied devices.
