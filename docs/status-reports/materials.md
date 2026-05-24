# Status Report Materials

Status: source.

Owner: `rtk_cloud_workspace`.

This index records the reusable materials for weekly Realtek Video / IoT Cloud
status reports.

## Operation Screenshots

| Material | Source | Use in report |
| --- | --- | --- |
| Admin Fleet Health Overview | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-overview.png` | Show customer/operator fleet health, online rate, attention queue, and health distribution. |
| Admin Devices + Detail Drawer | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-devices.png` | Show device search/filter, health facts, source facts, active stream status, provisioning, and deactivation. |
| Admin Firmware & OTA | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-firmware-ota.png` | Show firmware distribution, rollout progress, failed rollout, and device firmware risk. |
| Admin Stream Health | `repos/rtk_cloud_admin/docs/assets/webui-design/customer-stream-health.png` | Show WebRTC stream success, request volume, device failure risk, and stream attention workflow. |
| Sample Ops Lab Screen Flows | `repos/rtk_cloud_client/docs/mockups/sample-ops-lab-screen-flows.png` | Show SDK/sample operation flow: environment setup, provisioning, device config, camera monitor, debug report. |
| Sample Ops Lab UI Mockup | `repos/rtk_cloud_client/docs/mockups/sample-ops-lab-ui-mockup.png` | Optional appendix or deeper SDK/demo section. |

## Product / Website Visuals

| Material | Source | Use in report |
| --- | --- | --- |
| Connect+ Architecture Visual | `repos/rtk_cloud_frontend/static/assets/connectplus-architecture-diagram-corporate-v2.jpg` | Explain external product architecture or ecosystem story. |
| Connect+ Operations Console | `repos/rtk_cloud_frontend/static/assets/connectplus-operations-console-corporate-v2.jpg` | Optional product-facing operations console visual. |
| Connect+ Platform Surfaces | `repos/rtk_cloud_frontend/static/assets/connectplus-platform-surfaces-corporate-v2.jpg` | Optional product-facing surface overview. |
| Feature App SDK | `repos/rtk_cloud_frontend/static/assets/feature-app-sdk.png` | Optional SDK/app feature visual. |
| Feature Fleet Management | `repos/rtk_cloud_frontend/static/assets/feature-fleet-management.png` | Optional fleet management visual. |
| Feature Provision Flow | `repos/rtk_cloud_frontend/static/assets/feature-provision-flow.jpg` | Optional onboarding/provisioning visual. |
| Feature OTA Control Center | `repos/rtk_cloud_frontend/static/assets/feature-ota-control-center.jpg` | Optional OTA feature visual. |

## Deployment And Configuration Sources

| Source | Use in report |
| --- | --- |
| `docs/private-cloud-deployment.md` | Deployment order, support boundary, network/TLS, backup/restore, and production-ready gaps. |
| `docs/linode-staging-deployment-snapshot.md` | Linode staging endpoint list, runtime shape, and previous snapshot evidence. |
| `docs/product-level-evidence.md` | Formal evidence bundle boundary and status semantics. |
| `repos/rtk_video_cloud/deploy/README.md` | Video Cloud packaged runtime inventory, systemd services, EMQX/NATS/coturn/PostgreSQL/Prometheus shape. |
| `repos/rtk_video_cloud/docs/config-map.md` | Video Cloud non-secret configuration categories. |
| `repos/rtk_account_manager/linode_deploy/README.md` | Account Manager public VM shape, deployment model, PostgreSQL and nginx boundary. |
| `repos/rtk_cloud_admin/deploy/linode/README.md` | Admin dashboard public VM shape, upstream configuration, Docker/SQLite/nginx boundary. |
| `repos/rtk_cloud_frontend/docs/deployment-linode.md` | Frontend artifact-first Linode deployment model and runtime env categories. |

## Live Health Checks

The builder currently probes these public endpoints when generating a report:

| Component | Endpoint |
| --- | --- |
| Video Cloud runtime | `https://video-cloud-staging.realtekconnect.com/healthz` |
| Video Cloud runtime | `https://video-cloud-staging.realtekconnect.com/version` |
| Account Manager API | `https://account-manager.video-cloud-staging.realtekconnect.com/v1/health` |
| Admin dashboard | `https://admin.video-cloud-staging.realtekconnect.com/healthz` |
| Admin dashboard | `https://admin.video-cloud-staging.realtekconnect.com/api/service-health` |

Record public status, versions, and high-level runtime shape only. Do not record
raw secrets, DSNs, tokens, private app ports as direct evidence targets, or
customer data.
