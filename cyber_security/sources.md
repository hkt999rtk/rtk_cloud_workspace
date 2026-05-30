# Cyber Security Source Index

This index points to the canonical or supporting files used by the initial
STRIDE threat model. Keep source-of-truth material in its owning repository;
use this file to make security analysis retrievable.

## Workspace And Deployment Sources

| Source | Role in analysis |
| --- | --- |
| `README.md` | Workspace repository boundaries and rule to avoid committing logs, credentials, tokens, or local secrets. |
| `docs/architecture.md` | Cross-repository source-of-truth model and component ownership boundaries. |
| `docs/private-cloud-deployment.md` | Private-cloud deployment profiles, required components, network/TLS boundaries, and secret categories. |
| `docs/deployment-secrets-governance.md` | Deployment secret layout, accepted storage patterns, and handling rules. |
| `docs/account-manager-admin-boundary.md` | Account Manager versus Admin BFF authority boundary and dashboard non-authoritative cache rules. |
| `docs/cross-service-broker-packaging.md` | Broker ownership and security policy expectations for cross-service lifecycle messaging. |

## Video Cloud Runtime Sources

| Source | Role in analysis |
| --- | --- |
| `repos/rtk_video_cloud/README.md` | Video Cloud purpose, binaries, runtime model, storage, configuration, and docs entry points. |
| `repos/rtk_video_cloud/docs/auth.md` | Service-local authentication behavior and compatibility details. |
| `repos/rtk_video_cloud/docs/device-transport-spec.md` | Device transport expectations for WebSocket and MQTT surfaces. |
| `repos/rtk_video_cloud/docs/mqtt-broker.md` | MQTT broker packaging and operational guidance. |
| `repos/rtk_video_cloud/docs/config-map.md` | Security-relevant environment keys and runtime configuration map. |
| `repos/rtk_video_cloud/docs/runtime-inventory.md` | Runtime process inventory and service units. |
| `repos/rtk_video_cloud/deploy/` | Deployment assets, environment examples, systemd units, and EMQX compose assets. |

## Contract Sources

| Source | Role in analysis |
| --- | --- |
| `repos/rtk_cloud_contracts_doc/AUTH.md` | Product auth contract, bearer scopes, device mTLS, revocation, token recovery, and compatibility constraints. |
| `repos/rtk_cloud_contracts_doc/AUTHORIZATION.md` | Product authorization roles, scopes, permissions, and ACL ownership. |
| `repos/rtk_cloud_contracts_doc/PROVISION.md` | Provisioning phases, certificate issuance boundary, device activation, service ACLs, and failure semantics. |
| `repos/rtk_cloud_contracts_doc/STREAMING.md` | WebRTC signaling, stream route auth, TURN registry, session lifecycle, and error model. |
| `repos/rtk_cloud_contracts_doc/SNAPSHOT_AND_MEDIA.md` | Media download and snapshot access expectations. |
| `repos/rtk_cloud_contracts_doc/CROSS_SERVICE_CHANNEL.md` | Account/video command and event channel semantics where available in the pinned snapshot. |

## Supporting Sources

| Source | Role in analysis |
| --- | --- |
| `repos/rtk_cloud_logger/README.md` | Logging package guidance for sensitive fields that must not be logged. |
| `scripts/secrets-check.sh` | Workspace secret hygiene check helper. |
| `docs/product-level-evidence.md` | Evidence collection expectations and redaction guidance. |
| `docs/testing.md` | Cross-repository validation entry points and blocked/fail evidence semantics. |

## Maintenance Notes

- Add new sources here before referencing them from a formal threat model.
- If a document is generated from another source, list the canonical source
  first and mark the generated document as supporting evidence.
- Do not paste secret-bearing command output into this index.

