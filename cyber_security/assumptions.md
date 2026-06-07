# STRIDE Threat Model Assumptions

These assumptions drive the initial STRIDE risk ranking for
`rtk_video_cloud` and private-cloud runtime/deployment boundaries. Update this
file when deployment facts are confirmed.

## Scope Assumptions

- Initial scope is `repos/rtk_video_cloud` plus cross-service and private-cloud
  boundaries documented in workspace docs.
- `rtk_account_manager`, `rtk_cloud_admin`, `rtk_cloud_frontend`, and
  `rtk_cloud_client` are considered adjacent systems unless they participate in
  a Video Cloud trust boundary.
- CI/build/release assets are in scope only where they affect deployment
  secrets, release artifacts, or runtime configuration.

## Deployment Assumptions

- Production-like deployments place a reverse proxy or load balancer with TLS
  in front of public frontend, account, video, and selected WebRTC/TURN
  surfaces.
- Raw service ports, PostgreSQL, Prometheus metrics, and the EMQX dashboard are
  private network or authenticated admin surfaces.
- MQTT is enabled only when device transport requires it; if exposed outside a
  private network, it uses TLS, explicit authentication, logs, and firewall
  policy.
- Object storage may be local filesystem or S3-compatible storage depending on
  environment.
- Secrets are supplied through GitHub Environment secrets, host-side secret
  management, root-owned env files, or a customer production secret manager.

## Authentication And Authorization Assumptions

- Device mTLS is the intended production device-authentication model, but some
  legacy certificate-header and scope-compatibility flows may remain enabled
  during migration.
- Device tokens, refresh tokens, and WebRTC/device transport routes must remain
  subject-bound to the target `devid`.
- Account Manager is authoritative for identity, tenant context,
  authorization, entitlement, device registry, and provisioning intent.
- Admin BFF/dashboard state is non-authoritative when upstream services are
  configured.

## Data Sensitivity Assumptions

- Media clips and snapshots are sensitive customer/device data.
- Device certificates, JWT signing secrets, refresh tokens, MQTT credentials,
  TURN shared secrets, object storage keys, database DSNs, deploy keys, and
  private certificate assets are high-value secrets.
- Tenant, organization, device ownership, provisioning, activation, and
  service-option ACL state are integrity-critical.
- Runtime logs, metrics, and readiness evidence must be redacted before being
  shared outside trusted operator channels.

## Open Questions

- Which exact production profile is being threat-modeled: single-node
  evaluation, production-like private cloud, or current Linode staging?
- Is `VIDEO_CLOUD_AUTH_MTLS_REQUIRED=true` enforced for device-facing
  production endpoints today?
- Which legacy compatibility routes or certificate-header flows are enabled in
  the target deployment?
- Are MQTT, TURN registry APIs, metrics, or the EMQX dashboard reachable from
  outside the private network?
- What tenant isolation and customer data-retention requirements apply to the
  target deployment?
