# Cyber Security Source Index

Status: active.

Classification: index.

Owner: `rtk_cloud_workspace`.

Last reviewed: 2026-10-05.

Applies to: the existing Video Cloud STRIDE model and its adjacent trust
boundaries. Keep source-of-truth material in its owning repository. A source
link is not evidence that its target control is enabled in a deployment.

## Review Snapshot And Evidence

| Checkout | Inspected HEAD |
| --- | --- |
| Workspace | `82d6643695b6ccdd8156aa560f725b3a543045be` |
| Video Cloud | `ce53d62b50a3cec92145a99435fd79a52ae2ffd0` |
| Canonical contracts | `6b8ef16c197cd295b5ce4aa9f23f0f6d215d9e28` |

These identify the source checkout before this documentation update, not a
release or deployed image. Evidence labels used in the model are D (documented
requirement/design), I (implementation inspected), T (test source inspected,
not executed in this review), and E (dated environment evidence). No new E
evidence was collected. Historical deployment sections retain their original
dates; they do not certify today's environment.

## Workspace And Deployment Sources

| Source | Role in analysis |
| --- | --- |
| [Workspace README](../README.md), [documentation index](../docs/README.md), [governance](../docs/documentation-governance.md) | D: repository boundaries, source authority/status, and secret-handling rules. |
| [Workspace architecture](../docs/architecture.md), [deployment architecture](../docs/cloud-deployment-architecture.md) | D: cross-repository ownership and provider-neutral runtime model. |
| [Private-cloud deployment](../docs/private-cloud-deployment.md) | D: supporting profile/BOM guidance, not current deployment proof. |
| [LKE HAProxy edge](../docs/lke-external-haproxy-edge.md) | D: TCP passthrough boundary; embedded staging observations are dated historical evidence. |
| [Deployment secrets](../docs/deployment-secrets-governance.md) | D: credential ownership, placement, injection, and redaction. |
| [Account/Admin boundary](../docs/account-manager-admin-boundary.md) | D: authoritative Account Manager state versus Admin BFF/cache. |
| [Broker retirement](../docs/cross-service-broker-packaging.md) | D: retired packaging; explicit service APIs and producer-owned outbox/retry. |
| [Object Storage policy](../docs/object-storage-policy.md), [storage operations](../docs/storage-credential-lifecycle.md) | D: separate bucket/credential boundaries, prefixes, retention, migration, and environment qualification. |
| [Artifact release governance](../docs/artifact-release-governance.md) | D: manifest, source, checksum and release-adoption requirements; hashes alone do not establish an independent trusted publisher. |
| [Core backup/restore](../docs/backup-restore.md), [recovery implementation location](../scripts/go/rtk-cloud/internal/recovery/) | D: matched data, fences, cache invalidation, escrow, and post-backup revocation reconciliation; operator attestations require supporting evidence. |

## Video Cloud Runtime Sources

| Source | Role in analysis |
| --- | --- |
| [Video Cloud README](../repos/rtk_video_cloud/README.md), [architecture](../repos/rtk_video_cloud/docs/architecture.md) | D: core API composition, components, and documented revocation limits. |
| [Optional services](../repos/rtk_video_cloud/docs/optional-services.md), [runtime inventory](../repos/rtk_video_cloud/docs/runtime-inventory.md) | D: opt-in process/route cutovers and their qualification limits. |
| [Authentication](../repos/rtk_video_cloud/docs/auth.md) | D: certificate bootstrap, bearer scopes, stateless reissue, and compatibility assertions; compare with the router below. |
| [Device transport](../repos/rtk_video_cloud/docs/device-transport-spec.md), [MQTT broker](../repos/rtk_video_cloud/docs/mqtt-broker.md) | D: transport, tenant topic rewriting, broker callback/ACL setup, and operational dependencies. |
| [Configuration map](../repos/rtk_video_cloud/docs/config-map.md), [config implementation](../repos/rtk_video_cloud/internal/config/config.go) | D/I: exact flags/defaults, including strict MQTT entitlements, PKI, optional cutovers, conditional direct upload, and default-off Brand Webhook. |
| [API composition](../repos/rtk_video_cloud/internal/apiapp/app.go), [configuration validation](../repos/rtk_video_cloud/internal/config/validate.go) | I: actual worker/client wiring and startup constraints; flags alone do not establish a reachable control. |
| [Direct clip upload](../repos/rtk_video_cloud/docs/clip-direct-upload-design.md) | D: service-internal upload/verifier design; shared wire authority remains `snapshot_and_media.md`. |
| [Webhook key rotation](../repos/rtk_video_cloud/docs/brand-webhook-key-rotation.md) | D: staged key rotation, encrypted stored subscription secrets, and recovery constraints. |
| [Deployment assets](../repos/rtk_video_cloud/deploy/) | Supporting configuration/examples; not evidence of effective environment settings. |

## Contract Sources

| Source | Role in analysis |
| --- | --- |
| [Auth](../repos/rtk_cloud_contracts_doc/auth.md) | D, normative: bearer scopes, bootstrap, claim-preserving reissue, and compatibility restrictions. |
| [Authorization](../repos/rtk_cloud_contracts_doc/authorization.md) | D: product roles, permissions, and ACL ownership. |
| [Platform PKI](../repos/rtk_cloud_contracts_doc/platform_pki.md) | D, proposed normative design: separate trust domains, issuance, custody, rollover, revocation, and escrow/recovery; current-state gaps remain unimplemented targets. |
| [Service registration](../repos/rtk_cloud_contracts_doc/service_registration.md) | D, proposed target: independent workload registration; not proof of current route cutover. |
| [Provisioning](../repos/rtk_cloud_contracts_doc/provision.md) | D: enrollment, certificate issuance boundary, activation, ACLs, and failure semantics. |
| [Streaming](../repos/rtk_cloud_contracts_doc/streaming.md) | D: signaling, TURN authorization, and session lifecycle. |
| [Snapshot and media](../repos/rtk_cloud_contracts_doc/snapshot_and_media.md) | D: canonical media/download and direct-upload wire expectations. |
| [HTTP API](../repos/rtk_cloud_contracts_doc/http_api.md) | D: Brand Webhook management, tenant/current-owner checks, signed delivery, receiver idempotency, and the cross-service ownership-lock limitation. |
| [Cross-service coordination](../repos/rtk_cloud_contracts_doc/cross_service_channel.md) | D, normative for current API/outbox coordination; the historical broker vocabulary is explicitly non-normative. |

## Inspected Implementation And Test Sources

Test links record relevant source availability and assertions, not successful
execution, full coverage, or staging qualification.

| Implementation | Relevant test source and review purpose |
| --- | --- |
| [HTTP router](../repos/rtk_video_cloud/internal/httpapi/router.go), [device certificate validation](../repos/rtk_video_cloud/internal/auth/device_certificate.go), [App certificate validation](../repos/rtk_video_cloud/internal/auth/app_certificate.go) | T: [router tests](../repos/rtk_video_cloud/internal/httpapi/router_test.go), [Product PKI tests](../repos/rtk_video_cloud/internal/httpapi/product_pki_test.go), [App certificate tests](../repos/rtk_video_cloud/internal/auth/app_certificate_test.go). Review direct-TLS/registry checks and configuration-dependent header fallback separately. |
| [Token service](../repos/rtk_video_cloud/internal/auth/auth.go) | T: [token tests](../repos/rtk_video_cloud/internal/auth/auth_test.go), including revision/claim-preserving stateless reissue. |
| [Device PKI binding](../repos/rtk_video_cloud/internal/pki/verification.go), [service-client PKI verification](../repos/rtk_video_cloud/internal/pki/service_client_verification.go) | I: registry, environment/domain/product, issuer lineage and revocation checks. T: [Product PKI tests](../repos/rtk_video_cloud/internal/httpapi/product_pki_test.go) and [mTLS bootstrap tests](../repos/rtk_video_cloud/internal/httpapi/mtls_test.go), including strict-mode rejection and enabled compatibility behavior. |
| [HTTP Shadow](../repos/rtk_video_cloud/internal/httpapi/device_shadow.go) | I: device subject, service option and entitlement-revision enforcement; legacy and administrative paths remain separately scoped. |
| [Entitlement receiver](../repos/rtk_video_cloud/internal/httpapi/account_manager_entitlement.go), [Account Manager authorizer](../repos/rtk_video_cloud/internal/httpapi/account_manager_authorizer.go) | I: explicit snapshot API and conflict handling, plus upstream authorization failure behavior; not proof of every lifecycle receiver or Admin BFF control. |
| [MQTT authentication/ACL](../repos/rtk_video_cloud/internal/httpapi/mqtt_auth.go) | T: [MQTT tests](../repos/rtk_video_cloud/internal/httpapi/mqtt_auth_test.go), including forged tenant/client IDs, strict revision-zero grant checks, reserved topics, same-cloud App device scope, and lease caps. |
| [WebSocket coordinator](../repos/rtk_video_cloud/internal/websocket/), [signaling](../repos/rtk_video_cloud/internal/signaling/) | T: [WebSocket revisions](../repos/rtk_video_cloud/internal/websocket/entitlement_revision_test.go), [signaling revisions](../repos/rtk_video_cloud/internal/signaling/entitlement_revision_test.go), [TURN authorization](../repos/rtk_video_cloud/internal/signaling/turn_authorization_test.go). Existing relay/media termination remains a separate requirement. |
| [Direct upload](../repos/rtk_video_cloud/internal/clipupload/clipupload.go), [multipart upload](../repos/rtk_video_cloud/internal/clipupload/multipart.go) | T: [upload tests](../repos/rtk_video_cloud/internal/clipupload/clipupload_test.go), [HTTP upload tests](../repos/rtk_video_cloud/internal/httpapi/clip_upload_test.go). Includes activation epoch, expiry/lease, checksum, replay, and publication boundaries. |
| [Webhook transport](../repos/rtk_video_cloud/internal/brandwebhook/transport.go), [worker](../repos/rtk_video_cloud/internal/brandwebhook/worker.go), [subscriptions](../repos/rtk_video_cloud/internal/brandwebhook/subscription.go) | T: [worker tests](../repos/rtk_video_cloud/internal/brandwebhook/worker_test.go), [subscription tests](../repos/rtk_video_cloud/internal/brandwebhook/subscription_test.go), [HTTP webhook tests](../repos/rtk_video_cloud/internal/httpapi/brand_webhook_test.go). Includes destination checks, tenant routing, signatures, retries, and key rotation. |

## Supporting Sources

| Source | Role in analysis |
| --- | --- |
| [Logger README](../repos/rtk_cloud_logger/README.md) | D: sensitive-field logging guidance. |
| [Scripts command guide](../scripts/README.md), [Go implementation](../scripts/go/rtk-cloud/main.go) | I: `go run ./scripts/go/rtk-cloud -- secrets-check` / `runSecretsCheck` checks selected ignore paths, prohibited copies, and tracked path/pattern scans. The previous `scripts/secrets-check.sh` reference is absent in this checkout and is replaced here. No scan was run in this review, and this helper is not evidence of exhaustive secret coverage. |
| [Product evidence](../docs/product-level-evidence.md), [testing governance](../docs/testing.md) | D: evidence collection, redaction, commands, and blocked/fail semantics. |

## Maintenance Notes

- Add new sources here before referencing them from a formal threat model.
- If a document is generated from another source, list the canonical source
  first and mark the generated document as supporting evidence.
- Do not paste secret-bearing command output into this index.
