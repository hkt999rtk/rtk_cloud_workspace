# Database simplification schema delta

Predecessor and current Account Manager, Video Cloud, and Admin catalogs were independently initialized and compared with source extraction in disposable PostgreSQL/SQLite; the original static predecessor snapshot is retained separately. These are local verification results, not deployed-environment observations.

| Owner | Retired business tables | Added columns |
| --- | --- | --- |
| Account Manager | `acl_audit_events`, `brand_cloud_memberships`, `brand_cloud_refresh_tokens`, `brand_cloud_users` | `audit_events.audit_domain` |
| Billing | None | None |
| Video Cloud | `firmware_campaigns`, `firmware_releases`, `firmware_rollouts`, `firmware_targets`, `legacy_device_states`, `relay_geolinks`, `relay_nodes` | `ota_releases.revision`, `ota_campaigns.revision`, `ota_deployments.revision` |
| Cloud Admin | `platform_admins`, `upstream_devices`, `upstream_operations`, `upstream_organizations` | None |
| Cloud Frontend | None | None |

Retired explicit indexes: `identity_providers_provider_id_idx`, `device_tag_catalog_org_tag_idx`, and `idx_readiness_facts_device`. Existing unique constraints remain. PostgreSQL/SQLite tests reject duplicate keys after upgrade and verify that retained indexes remain eligible for the corresponding lookups. No capacity or latency improvement is claimed.

`audit_events.audit_domain` separates general and ACL events. Actor history is retained after user deletion; new ACL events validate their actor. OTA core identity, scope, state, sequence and timestamps are read from columns, with internal revisions enforcing optimistic concurrency. JSON contains only extensions.

Billing, PKI, cross-service receipts and accounting snapshots retain their models. Migration/reconciliation metadata remains intentionally. Fresh-versus-upgraded catalog equality is exercised in all three changed services.
