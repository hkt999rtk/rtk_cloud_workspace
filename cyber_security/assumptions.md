# STRIDE Threat Model Assumptions

Status: active supporting analysis; environment qualification pending.

Classification: supporting-note.

Owner: `rtk_cloud_workspace` (analysis); service and deployment owners (control evidence).

Last reviewed: 2026-10-05.

Applies to: the existing Video Cloud threat model, compared with the selected
workspace checkout and adjacent contracts. No named deployment was inspected.

These assumptions drive the [STRIDE risk matrix](analysis/stride-matrix.md).
Use the [source index](sources.md) for review snapshots and evidence locations.
Documented requirements (D), inspected implementation (I), inspected test
sources (T), and dated environment evidence (E) are different evidence levels.
Test sources were inspected, not executed; no new E evidence was collected.
Update this file when an environment's effective configuration and controls
are confirmed.

## Scope Assumptions

- Scope is `repos/rtk_video_cloud` plus cross-service and private-cloud
  boundaries documented in workspace docs.
- `rtk_account_manager`, `rtk_cloud_admin`, `rtk_cloud_frontend`, and
  `rtk_cloud_client` are considered adjacent systems unless they participate in
  a Video Cloud trust boundary.
- CI/build/release assets are in scope only where they affect deployment
  secrets, release artifacts, or runtime configuration.

## Architecture And Deployment Basis

- D: the cross-service broker is retired. Account/video lifecycle coordination
  uses explicit authenticated service APIs; durable retries belong to the
  producer's DB-backed outbox/lease state and the receiver's idempotent
  processing. Historical channel vocabulary and disabled in-tree adapters do
  not establish a deployed broker.
- D/I: `cmd/api` remains the default core composition. Independent Shadow,
  WebRTC, and video-storage processes and route cutovers are opt-in. Their
  packaged binaries or registration tests do not prove that edge traffic has
  switched or that the real Platform/PKI integration is qualified.
- D: in the LKE external-HAProxy profile, HAProxy performs TCP passthrough.
  TLS/mTLS termination and verification belong to the applicable backend
  ingress, broker, or service listener inside Kubernetes. A layer-4 edge must
  not be credited with certificate-header authentication or application ACLs.
  Private-cloud profiles may terminate TLS elsewhere and require their own
  boundary map.
- Assumed profile requirements: raw service ports, PostgreSQL, Redis, metrics,
  broker authentication callbacks, and the EMQX dashboard/management API are
  restricted to their intended workloads/operators. Public MQTT uses TLS and
  authenticated ACLs. Firewall, NetworkPolicy, listener exposure, and bypass
  paths have not been verified in an environment.
- D: secrets follow workspace deployment-secret ownership and injection rules.
  Different runtime, operator, CI, signing, storage, and recovery credentials
  have different authority and blast radius; their mere existence does not
  imply equal risk or production provisioning.

## Authentication, PKI, And Authorization Basis

- D: the Platform PKI document declares a **proposed normative design**. Its
  target separates environments and Device, App/user, Internal Service, MQTT
  server, public HTTPS, and OpenBao transport trust domains; the External
  Integration domain is proposed and not implemented. Treat target custody,
  rollover, and revocation controls as requirements until implementation and
  environment evidence exist for the particular verifier.
- D/I: certificate-authenticated device/app bootstrap and bearer-authenticated
  runtime requests are separate paths. A successful certificate chain alone
  must not grant an unrelated product, environment, device, or human role.
  Product device PKI and App PKI have independent flags and registry checks.
- I: Product PKI's device verifier and App PKI's app verifier require direct
  verified TLS chains and do not fall back to headers. Outside the relevant
  PKI mode, the corresponding certificate helper still has a fallback when
  `VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS=true`,
  `VIDEO_CLOUD_AUTH_ENABLE_LEGACY_CERT=true`, and `X-Client-Verify: SUCCESS`
  accompany parseable certificate metadata. The trusted-header flag defaults
  to false; the legacy parser flag defaults to true. The token-issuance path
  calls these helpers. This is a configuration-dependent tension with the
  normative auth contract and service text saying legacy headers are not
  runtime credentials; it needs explicit reconciliation, not an assumption
  that the compatibility mode is deployed or safe. Ordinary protected bearer
  routes remain a separate check, and certissuer's trusted-ingress mode is a
  different listener/policy boundary.
- D/I: device-bound tokens and protected transport/media routes must bind to
  the target `devid`. `refresh_token` is a legacy field for stateless signed
  token reissue, not an opaque refresh grant. Reissue validates and preserves
  existing service options and revision claims; it does not fetch new
  entitlement options from the control plane.
- D: Account Manager is authoritative for identity, tenant context,
  authorization, entitlement, device registry, and provisioning intent.
- D: Admin BFF/dashboard state is non-authoritative when upstream services are
  configured.

## Transport Authorization And Revocation Basis

- I: the MQTT callback binds signed tenant/device identity, username, and client
  ID. Broker topic rewriting and ACL enforcement must be configured together.
  Reserved physical tenant and `$aws` namespaces are denied. App scope has an
  explicit same-cloud device-topic restriction; non-App general-topic rules
  are broader after the reserved-topic denies. Tenant rewriting alone is not
  proof of per-device authorization for every scope/topic.
- I: revisioned device MQTT authentication rechecks the backing entitlement.
  With `VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED=true`, the immutable current
  grant also controls ACLs and lease for revision-zero device tokens. The flag
  defaults to false; when disabled, pre-cutover revision-zero tokens retain
  legacy Shadow ACL compatibility. Migration status and strict-mode activation
  require environment evidence.
- I: current protections include revisioned HTTP Shadow checks, WebSocket
  admission/per-frame checks and a five-second idle poll, signaling grant
  rechecks, and denial of new TURN allocations after a grant change. Revisioned
  MQTT responses cap the broker session at 60 seconds. These are code-level
  behaviors, not measured fleet-wide invalidation guarantees.
- D/I: already established TURN allocations and peer media are not terminated
  by the cited signaling revision checks. Optional exact broker-session
  eviction is separately gated by `VIDEO_CLOUD_MQTT_BROKER_EVICTION_ENABLED`
  (default false). Certificate revocation, entitlement removal, deactivation,
  App ownership change, and logout must each be traced through the affected
  connection/session owners; no single universal revocation bound is assumed.

## Storage, Outbound Delivery, And Recovery Basis

- I: direct S3 clip upload is conditional on
  `VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED`; its default becomes true when both
  the DB DSN and blob bucket are configured. The path requires PostgreSQL,
  checksum-capable S3 storage, and a verifier. It must be modeled as device
  authorization, direct object PUT, completion, verification, then publication.
  Local filesystem storage does not implement this path.
- D/I: signed object keys/headers and checksums, activation epochs, worker
  leases, and unexpired upload sessions constrain publication. A PUT started
  before URL expiry can finish later; best-effort deletion and delayed cleanup
  still need a bucket lifecycle backstop and environment qualification.
- D: bucket and credential boundaries matter independently of object prefixes.
  Media, dedicated OTA, release artifacts, and recovery data require the
  policy-defined separation and scoped credentials; a shared key is not
  isolated by a tenant or purpose prefix. Tracked target names do not prove a
  completed storage migration.
- I: tenant Brand Webhook delivery defaults off. When enabled, the inspected
  worker uses encrypted subscription secrets, tenant routing, signed events,
  retry receipts, public-address checks at connection time, HTTPS port 443,
  no proxy-based DNS bypass, and redirect rejection. These controls apply to
  this worker; legacy notifier/Device Hub delivery needs its own review.
  Receiver replay/deduplication, enabled subscriptions, and runtime egress
  restrictions have not been verified.
- D: core restore requires a matched dataset, held traffic/worker fences,
  disposable authorization-cache invalidation, original PKI/issuer recovery,
  and reconciliation of revocations/security changes after the backup point.
  Escrow is separate from the archive, and object payloads are external
  dependencies. Reconciliation booleans are operator attestations, not proof
  that revoked access stayed revoked or that the recovery procedure passed.

## Data Sensitivity Assumptions

- Media clips and snapshots are sensitive customer/device data.
- Device private keys, JWT signing material, signed tokens, MQTT credentials,
  TURN shared secrets, object storage keys, database DSNs, deploy keys, and
  private certificate assets are high-value secrets.
- Tenant, organization, device ownership, provisioning, activation, and
  service-option ACL state are integrity-critical.
- Runtime logs, metrics, and readiness evidence must be redacted before being
  shared outside trusted operator channels.

## Open Questions

- Which environment, release digests, runtime configuration, and listener/
  network inventory will qualify these code-grounded conclusions?
- Which PKI, header compatibility, strict MQTT entitlement, broker eviction,
  optional-service cutover, direct-upload, and Brand Webhook modes are enabled?
- What is the measured removal-to-denial/termination bound for HTTP, MQTT,
  idle/active WebSockets, signaling, existing TURN allocations, peer media,
  and App ownership changes, including dependency outages and legacy tokens?
- Are tenant/device ACLs, private buckets, credential scope, object lifecycle,
  presigned-URL logging, and webhook receiver replay handling qualified?
- Has a restore drill proved that post-backup revocations remain effective,
  original issuer/escrow material is recoverable, and traffic stays fenced
  until reconciliation completes?
- What customer retention, isolation, incident-response, RPO, and RTO
  requirements affect residual risk acceptance?
