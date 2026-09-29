# Product service grants: dev cutover

This runbook applies only to `video-cloud-dev`. It introduces registered
`mqtt`, `iot_shadow`, `video_streaming`, `video_storage`, `device_logging`, and
`ota` choices. Product edits create immutable grant revisions; device grants
change only through an explicit device operation or the Product apply job.
Neither the legacy backfill nor registry activation adds options to an old
Product or device.

## Current dev preflight (2026-09-26)

The reviewed CRL-aware deployment verifier accepts the three live, mounted
CRL manifests used by Account Manager and certissuer. The canonical
`scripts/check-deployment-credentials.sh --environment dev --read-only`
preflight passed all 10 checks with this verifier. This clears the earlier
deployment-contract drift; it does not authorize an image rollout or feature
activation. Keep the CRL mounts and recheck their current signed state before
any deployment. Do not replace the running PKI workloads with a renderer that
would drop their CRL settings.

The full Product cutover remains **NO-GO**. The private
`account-manager-service-registration-tls` Secret and all six registrar
identity Secrets are absent in dev. The active Service issuer cannot sign the
new subjects or the Product listener DNS, and its policy is immutable. An
a successor issuer approved through the actual Service intermediate workflow
and a new bootstrap session are required;
the existing bootstrap PVC must not be reused. Follow
[the dev PKI prerequisite](product-services-dev-pki.md) to issue and install
the seven identities, register exact workload approvals, and pass the final
listener, registration, and denial probes before continuing below.

## Verified dev runtime delta (2026-09-29)

The canonical read-only development credential check passed 10/10. The live
Account Manager Deployment is Ready, but its Service lacks the private
`service-registry` port. The protected Product catalog returned `options=[]`
and `product_writes_enabled=false` for each of the platform test account's
three Clouds. The selected dev operator setting is also `false`; none of the
seven listener/registrar identity Secrets exists, and the independent OTA
Deployment is absent. No OTA-enabled Product is available to that test account.

The initial packaged read-only service-grant backfill report returned
`ready=true`, `products=39`, `needs_backfill=39`, `already_versioned=0`,
`issue_count=0`, and `applied=0`, with a snapshot SHA-256. No tracked core
backup profile or cluster backup CronJob was found. This was a prerequisite
audit, not a completed backfill at that point.

The grant backfill was subsequently completed on 2026-09-29 under a short
development write fence. All five Account Manager API/worker Deployments were
stopped with resource-version guarded patches, and zero active Pods or other
Account Manager database clients remained. A FileVault-protected, mode-0600
custom-format PostgreSQL dump and globals were captured in a private directory
outside the repository and SecretStore; its SHA-256 manifest and complete
`pg_restore` parse passed. A new read-only Job returned the same reviewed
snapshot digest and 39 pending Products, so a zero-retry Job applied that
exact digest. The post Job returned `ready=true`, `already_versioned=39`,
`needs_backfill=0`, and `issue_count=0`; independent SQL counted 39 Product
rows and 39 distinct legacy revision-1 grants. All five Deployments returned
to 1/1 Ready, and authenticated login and Product reads passed. The backup,
manifest, Job specs, and reports remain in protected operator storage. No
bucket or PVC was created. Product writes remain `false`, the service catalog
remains empty, and no registrar identity or OTA charge was activated.

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
   the source Pod has changed. Remove the temporary copy helper after verifying
   the PVC. Apply the PVC-backed Loki Deployment with its `Recreate` strategy
   so the old emptyDir Pod stops before the new Pod starts. Wait for readiness
   and verify that only one Loki Pod serves queries before resuming writers.
   Deploy the updated Cloud Logger before
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
`LKE_OTA_SERVICE_REGISTRATION_ENABLED=true`,
`LKE_OTA_SERVICE_EDGE_ENABLED=true`, and
`LKE_OTA_CORE_CUTOVER_ENABLED=true` after OTA activation. The legacy
`LKE_OTA_REGISTRAR_REGISTRATION_ENABLED` must remain `false`: the registrar
and independent OTA service share one Platform lease. The
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
   Select `VIDEO_CLOUD_OTA_DELIVERY_MODE=object_url`. Confirm the private
   object bucket and HTTPS endpoint issue signed GET URLs for the exact
   billable artifact key, preserve Range requests, expire within ten minutes,
   and expose no storage credentials. The API must never proxy firmware bytes.
   CDN property, DataStream and `ota-cdn-runtime` are later expansion work;
   they are not prerequisites for this direct-object cutover.
   Migrate the OTA receipt, artifact, review, and outbox tables before starting
   the service; leave `VIDEO_CLOUD_DB_ENSURE_SCHEMA=false` in its Deployment.
   Then enable `LKE_OTA_SERVICE_REGISTRATION_ENABLED` and wait for the
   independent `otaservice` Pod's `/readyz`, private EndpointSlice, and active
   `service:ota` lease. Check that it can reach Account Manager for historical
   grants and Billing for usage-fact receipts. Next enable
   `LKE_OTA_SERVICE_EDGE_ENABLED` and verify the live device-host mTLS ingress
   routes `/v1/device/ota/` to `video-cloud-otaservice`: a device certificate
   succeeds, no certificate is rejected, and `check`, `artifact-token`, and
   `events` reach the independent service. The OTA device simulator must use
   the device mTLS URL with each device's certificate; its CDN artifact client
   must not send that certificate. Only after the observed ingress route and
   private endpoint pass may `LKE_OTA_CORE_CUTOVER_ENABLED` disable the core
   device handler and forward operator/app routes. Verify direct CDN downloads,
   signed completion reports, all four usage facts, outbox delivery, and
   Billing receipts before accepting the cutover. For rollback, restore the
   core handler and wait for its rollout before removing the device edge route.
   If the core Deployment is absent while the live ingress still has the OTA
   route, keep the route until the core handler is restored.
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
