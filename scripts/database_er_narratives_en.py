"""Reviewed English explanations for the generated ER atlas.

Group membership is editorial, not an additional database relationship. Every
table must occur exactly once. Notes describe the checked-in schema's intended
data and operation context; they do not assert that a live database was queried.
"""

GROUPS = {
    'Account Manager': (
        ('Organizations and sign-in identities', 'Organization, membership, human-account identity, and sign-in credentials.', 'Used when creating organizations, inviting or disabling members, signing in, or linking identities.',
         'organizations organization_members users user_identities identity_providers oidc_login_states refresh_tokens auth_tokens'),
        ('Roles and access policy', 'Roles, permissions, scopes, member activation, and ACL audit evidence.', 'Used for authorization decisions, role assignment, external-group mapping, and access-change audits.',
         'roles permissions role_permissions role_assignments external_group_mappings acl_audit_events organization_member_activation_holds quota_raise_requests'),
        ('Brand Cloud membership', 'Brand Cloud users, memberships, invitations, and product admission.', 'Used for Brand Cloud registration, invitations, owner transfers, and admission review.',
         'brand_cloud_users brand_cloud_memberships brand_cloud_member_invitations brand_cloud_owner_transfers brand_cloud_refresh_tokens brand_cloud_user_migrations brand_cloud_end_users brand_cloud_product_admissions'),
        ('End users and device bindings', 'End-user identities, session credentials, and their device access.', 'Used when app users sign in, establish a Brand Cloud identity, or bind a device.',
         'end_users end_user_identities end_user_refresh_tokens device_user_bindings'),
        ('Device and product registry', 'Devices, groups, tags, product profiles, and service-entitlement snapshots.', 'Used when registering devices, organizing fleets, configuring product services, or deriving entitlements.',
         'devices device_groups device_group_members device_tags device_tag_catalog device_item_profiles product_service_grants device_entitlement_snapshots'),
        ('Claiming and factory provisioning', 'Device claims, production runs, provisioning operations, and cross-service messages.', 'Used for device claims, factory enrollment, PKI requests, and provisioning-command retries.',
         'device_claim_tokens device_claims factory_production_runs factory_enrollment_reservations device_pki_outbox device_operations device_message_inbox device_message_outbox'),
        ('Cloud ownership handoff preparation', 'Handoff intent, participants, hold receipts, and Billing snapshots.', 'Used after accepting an ownership handoff while waiting for services to prepare and the balance to be confirmed.',
         'cloud_ownership_handoffs cloud_handoff_participants cloud_handoff_outbox cloud_handoff_prepare_acknowledgments cloud_handoff_abort_acknowledgments cloud_handoff_jobs cloud_handoff_billing_snapshots cloud_handoff_confirmation_requests'),
        ('Cloud ownership handoff decisions', 'Immutable confirmations, commit or cancellation decisions, and finalization receipts.', 'Used after balance confirmation to commit ownership, or to cancel and await participant completion.',
         'cloud_handoff_confirmation_acknowledgments cloud_handoff_commit_requests cloud_handoff_committed_decisions cloud_handoff_canceled_decisions cloud_handoff_finalization_acknowledgments'),
        ('Cloud deletion preparation', 'Brand Cloud deletion intent, participant write-hold receipts, and scheduled work.', 'Used when a deletion request arrives, resource producers are fenced, or deletion is canceled and released.',
         'cloud_deletion_operations cloud_deletion_participants cloud_deletion_resource_receipts cloud_deletion_jobs cloud_deletion_cancellations cloud_deletion_release_receipts'),
        ('Cloud deletion closure', 'Billing closure commands, retry evidence, command retirement, and deletion completion receipts.', 'Used after resources are held to close Billing, confirm its receipt, and write the cloud tombstone.',
         'cloud_deletion_close_commands cloud_deletion_close_attempts cloud_deletion_command_retirements cloud_deletion_completions'),
        ('Platform administration and governance', 'Administrator safeguards, recovery approvals, job authorizations, and traceable writes.', 'Used for platform bootstrap, last-admin protection, sensitive-operation approval, and auditing.',
         'platform_admin_guard platform_admin_recovery platform_admin_recovery_approvals platform_admin_recovery_audit platform_bootstrap job_authorizations managed_cloud_write_receipts audit_events'),
        ('Platform service catalog and certificates', 'Published service information, workload identities, instance leases, and app certificates.', 'Used for service registration, capability declarations, instance readiness, and app-certificate issuance.',
         'platform_services platform_service_manifests platform_service_instances platform_service_workloads platform_service_registration_requests platform_service_catalog_revisions chipset_information_providers app_certificates'),
        ('Outbound notifications and product collaboration', 'Email, cloud-creation events for Billing, and product-collaborator invitations.', 'Used to queue notifications, deliver creation events to Billing, or invite product collaborators.',
         'brand_cloud_billing_creation_outbox email_outbox product_collaborator_invitations'),
        ('Test lab', 'Isolated test accounts, binding grants, login attempts, and short-lived device sessions.', 'Used for test-lab sign-in, temporary binding grants, and test-session creation.',
         'test_lab_accounts test_lab_bind_grants test_lab_console_users test_lab_login_attempts test_lab_sessions'),
        ('Schema versions', 'Applied Account Manager database migration versions.', 'Checked at startup or upgrade to determine which migrations have already run.', 'schema_migrations'),
    ),
    'Billing': (
        ('Commercial accounts and responsibility', 'Commercial accounts, balance entries, and financial-record responsibility periods.', 'Used for posting funds, updating balances, restricting payment access, and identifying historical owners.',
         'commercial_accounts balance_ledger_entries billing_responsibility_periods billing_ledger_responsibility billing_payment_responsibility billing_payment_method_responsibility billing_access_states'),
        ('Payments and automatic top-ups', 'Payment methods, consent, intents, attempts, and reconciliation work.', 'Used to set up a payment method, top up a balance, process provider callbacks, or reconcile uncertain outcomes.',
         'payment_methods payment_method_setup_sessions payment_consents payment_intents payment_attempts payment_reconciliation_jobs payment_webhook_inbox auto_topup_policies'),
        ('Pricing, usage, and invoices', 'Rate versions, billing periods, usage facts, and invoice artifacts.', 'Used to ingest usage, price it, generate invoices, and link settlement ledger entries.',
         'pricing_plan_versions pricing_rates billing_periods billing_usage_facts billing_invoices billing_invoice_lines billing_invoice_documents invoice_settlement_links'),
        ('Billing handoff and settlement', 'Balance snapshots, confirmations, authorization, and final decisions during ownership handoff.', 'Used to freeze responsibility, confirm financial evidence, and commit or finalize an owner transfer.',
         'billing_ownership_handoffs billing_handoff_balance_snapshots billing_handoff_setup_observations billing_handoff_settlement_receipts billing_handoff_confirmations billing_handoff_commit_authorizations billing_handoff_committed_decisions billing_handoff_finalizations'),
        ('Handoff cancellation and archival', 'Handoff cancellation receipts and archived billing profiles or automatic-top-up policies.', 'Used when aborting a handoff, releasing its hold, or retaining old payment-setting evidence.',
         'billing_handoff_cancellations billing_handoff_abort_acknowledgments billing_retired_profiles billing_retired_policy_evidence'),
        ('Provider reversals', 'Provider reversals, responsibility allocations, and cases requiring review.', 'Used when a reversal arrives, its responsible period is allocated, or an exception is reviewed.',
         'billing_provider_reversal_events billing_provider_reversal_allocations billing_provider_reversal_reviews'),
        ('Brand Cloud closure', 'Closure state, payment-credential revocations, settlement, and cancellation or completion receipts.', 'Used when Brand Cloud deletion requires Billing to disable payer capabilities and confirm financial evidence.',
         'billing_cloud_closures billing_cloud_closure_revocations billing_cloud_closure_revocation_acks billing_cloud_closure_settlements billing_cloud_closure_completions billing_cloud_closure_cancellations billing_cloud_closure_release_acks billing_cloud_closure_retired_commands'),
        ('Cloud creation and billing overview', 'Cloud-creation events, preflight receipts, billing profiles, and displayed financial activity.', 'Used for cloud-creation synchronization, closure preflight, activity queries, and billing-contact changes.',
         'billing_cloud_creation_receipts billing_cloud_preflight_receipts billing_profiles billing_activity_events billing_audit_events schema_migrations'),
        ('Payment simulator', 'Local payment simulations, setup sessions, and NewebPay transaction state.', 'Used to test provider responses, callbacks, and payment paths; these are not real payment records.',
         'payment_simulator_operations payment_simulator_setup_sessions payment_simulator_newebpay_transactions'),
    ),
    'Video Cloud': (
        ('Devices and connections', 'Video Cloud devices, live connections, commands, and runtime records.', 'Used for device activation, connect/disconnect events, command delivery, and runtime-state recording.',
         'devices device_socket_sessions legacy_device_states device_transfer_fences device_presence_outbox device_command_messages device_logs device_runtime_logs'),
        ('Events and runtime telemetry', 'Sequence gaps, product events, notification deduplication, and runtime settings.', 'Used when accepting device events, tracking log sequences, retrying notifications, or validating tokens.',
         'device_runtime_log_gaps product_telemetry_events device_event_acceptances notification_deliveries notification_dead_letters runtime_config refresh_tokens'),
        ('Video clips', 'Clip uploads, multipart integrity, and clip metadata.', 'Used when a device uploads or resumes a clip, finalizes its parts, queries it, or expires it.',
         'clip_uploads clip_upload_parts clip_metadata'),
        ('Firmware releases', 'Available firmware, target versions, campaigns, and per-device rollout results.', 'Used to publish firmware, set version targets, or track an existing rollout.',
         'firmware_releases firmware_targets firmware_campaigns firmware_rollouts'),
        ('OTA campaigns and dispatch', 'OTA releases, campaigns, target devices, dispatch throttles, and deployment outcomes.', 'Used to create campaigns, schedule devices, send commands, receive deployment events, or migrate legacy data.',
         'ota_releases ota_campaigns ota_campaign_targets ota_campaign_dispatch_state ota_global_dispatch_state ota_deployments ota_deployment_events ota_legacy_migrations'),
        ('Factory enrollment and resource handoff', 'Factory entitlements, device-identity evidence, and cloud-resource handoff or deletion state.', 'Used when factory devices request certificates or cloud ownership and resource lifecycles change.',
         'factory_enrollment_journal factory_device_entitlements factory_cloud_handoffs resource_cloud_handoffs resource_cloud_deletions'),
        ('Usage evidence and Billing delivery', 'MQTT usage windows, auditable usage facts, collector cursors, and the Billing outbox.', 'Used to accept and deduplicate metering events, derive billable facts, and retry delivery to Billing.',
         'mqtt_usage_windows usage_facts usage_event_receipts billing_usage_collector_state billing_usage_collector_receipts billing_usage_fact_outbox'),
        ('Brand event webhooks', 'Brand Cloud webhook subscriptions, pending events, and delivery results.', 'Used to notify a brand after accepting device events, retry failures, or inspect delivery outcomes.',
         'brand_webhook_subscriptions brand_webhook_outbox brand_webhook_receipts'),
        ('PKI issuance governance', 'Issuer hierarchy, approval operations, certificate bindings, and signing evidence.', 'Used when creating issuers, approving sensitive issuance, binding device certificates, or recording PKI actions.',
         'pki_issuers pki_operations pki_approvals pki_audit pki_certificate_bindings pki_signing_claims pki_replacements pki_legacy_imports'),
        ('PKI certificate lifecycle', 'Issuance and revocation records for app, server, and service-client certificates.', 'Used to process CSRs, issue certificates, or revoke existing certificates.',
         'pki_app_issuances pki_app_revocations pki_server_issuances pki_server_revocations pki_service_client_issuances pki_service_client_revocations'),
        ('PKI trust distribution', 'CRLs, bundle acknowledgments, root-distrust policy, and deployment bootstrap.', 'Used to distribute certificate state, confirm consumers updated trust data, or transition legacy trust windows.',
         'pki_crls pki_crl_acknowledgments pki_bundle_acknowledgments pki_root_distrust pki_root_distrust_acknowledgments pki_legacy_windows pki_deployment_bootstrap_sessions'),
        ('Node discovery and device certificates', 'Service and TURN nodes, device-certificate requests, and relay-routing schema.', 'Node discovery and CSR issuance have runtime paths; direct non-DDL reads or writes of the relay tables were not found.',
         'registry_nodes relay_nodes relay_geolinks turn_nodes cert_issue_requests cert_issue_events'),
    ),
    'Cloud Admin': (
        ('Administrator sign-in and upstream projection schema', 'Sessions and schema for integration settings and upstream organization, device, and operation snapshots.', 'Sessions have runtime reads and writes; no non-DDL synchronization reads or writes were found for the upstream projection tables.',
         'platform_admins sessions upstream_organizations upstream_devices upstream_operations integration_settings'),
        ('Device operations view', 'Administrative device and operation views, readiness layers, and audit events.', 'Used to show device details, investigate operation status, or record administrative actions.',
         'devices operations readiness_facts audit_events'),
        ('Batch jobs', 'Batch jobs, per-item processing, action receipts, and provisioning or OTA scope previews.', 'Used to submit bulk device work, retry failed items, or preview an operation scope.',
         'batch_jobs batch_job_items batch_job_action_receipts provisioning_sources ota_scope_previews'),
        ('Schema versions', 'Applied Cloud Admin local database migrations.', 'Checked at service startup or upgrade.', 'schema_migrations'),
    ),
    'Cloud Frontend': (
        ('Website interactions and leads', 'Website analytics events, SDK-term acceptances, and visitor contact leads.', 'Used when visitors browse the site, download an SDK, or submit a contact form.',
         'analytics_events sdk_download_acceptances leads'),
        ('Website search index', 'Searchable documents, text chunks, and embedding data.', 'Used to rebuild the index, search site documents, or return results.',
         'search_documents search_chunks'),
    ),
}

# Each row: table | data represented | operation context. The exact DDL source
# is taken from parse_database() and displayed beside each note in the atlas.
NOTE_ROWS = {
    'Account Manager': '''
acl_audit_events | ACL audit events with actor, subject, action, and change payload. | Recorded after access-control or role changes for traceability.
app_certificates | App CSRs, issued certificates, chains, and fingerprints. | Used when app identity certificates are requested, inspected, or rotated.
audit_events | Account and organization audit records with actor, subject, and payload. | Retained after significant account, membership, or device operations.
auth_tokens | Hashed verification or recovery tokens, purposes, and expiry times. | Issued and consumed for email verification or account recovery.
brand_cloud_billing_creation_outbox | Brand Cloud creation events for Billing, including retry leases. | Queued after cloud creation for asynchronous Billing delivery.
brand_cloud_end_users | Brand Cloud–end-user associations, aliases, and consent state. | Used when app users enter a Brand Cloud, change consent, or inspect the association.
brand_cloud_member_invitations | Membership invitation targets, roles, tokens, and status. | Used when inviting members, accepting invitations, or letting them expire.
brand_cloud_memberships | Brand Cloud user roles and membership status. | Consulted for Brand Cloud access decisions or role changes.
brand_cloud_owner_transfers | Owner-transfer requests, recipients, tokens, and acceptance state. | Used when an owner starts a transfer and the recipient accepts or cancels it.
brand_cloud_product_admissions | User and organization product admissions, provenance, and approvers. | Used to review product eligibility or its approval evidence.
brand_cloud_refresh_tokens | Hashed Brand Cloud refresh tokens and revocation times. | Defined for Brand Cloud session renewal; no direct non-DDL read or write path was found.
brand_cloud_user_migrations | Outcomes and conflicts when legacy Brand Cloud identities are mapped to shared users. | Written during the human-identity consolidation migration and checked for conflicts.
brand_cloud_users | Brand Cloud login accounts, email verification, and display details. | Used for Brand Cloud registration, verification, and sign-in.
chipset_information_providers | Chipset-information provider manifest URLs, versions, and snapshots. | Used when reading or updating chipset and SDK information sources.
cloud_deletion_cancellations | Cloud-deletion cancellation decisions and decision hashes. | Stores evidence when a deletion that can still be canceled is canceled.
cloud_deletion_close_attempts | Billing closure attempts with readiness hashes and settlement IDs. | Used when retrying closure or confirming that an attempt used the same evidence.
cloud_deletion_close_commands | Immutable Billing closure commands with settlement and readiness digests. | Saved after all resource producers report holds and before a closure request is sent.
cloud_deletion_command_retirements | Retired closure commands and their receipt hashes. | Used during cancellation or recovery to ensure an old closure command cannot run again.
cloud_deletion_completions | Billing closure time, receipt hash, and cloud tombstone time. | Records success after Billing closes and the organization tombstone is synchronized.
cloud_deletion_jobs | Deletion-worker availability, leases, attempt counts, and generations. | Updated when background work is claimed, renewed, or retried.
cloud_deletion_operations | Brand Cloud deletion intents with owner, versions, idempotency key, cutoff, phase, and blockers. | Created on an owner deletion request; its phase advances as resources are held, Billing closes, and deletion succeeds.
cloud_deletion_participants | Resource services required to respond to one cloud deletion. | Fixed when deletion starts, then checked for complete hold receipts.
cloud_deletion_release_receipts | Participant receipts for releasing holds after cancellation. | Used to confirm resource services are writable again during cancellation or recovery.
cloud_deletion_resource_receipts | Immutable hashes of participant hold and drain receipts. | Submitted after a resource service stops new work and checked before Billing closure.
cloud_handoff_abort_acknowledgments | Receipts showing a handoff participant completed an abort. | Used after cancellation to confirm services released reserved state.
cloud_handoff_billing_snapshots | Billing balances, currencies, versions, and cutoffs for handoffs. | Used to inspect and confirm a settlement snapshot before ownership transfer.
cloud_handoff_canceled_decisions | Handoff cancellation IDs, authorizations, and decision hashes. | Preserved as immutable evidence after a cancellation decision is confirmed.
cloud_handoff_commit_requests | Commit requests bound to a Billing snapshot and authorization. | Saved when ownership commit starts after balance confirmation.
cloud_handoff_committed_decisions | Committed ownership versions, times, and decision hashes. | Prevents duplicate or conflicting commits after a handoff takes effect.
cloud_handoff_confirmation_acknowledgments | Receipt hashes for Billing snapshot-confirmation requests. | Marks confirmation requests as answered.
cloud_handoff_confirmation_requests | User confirmations for a handoff and Billing snapshot version. | Created when a user confirms the displayed balance before transfer.
cloud_handoff_finalization_acknowledgments | Participant receipts for completed handoffs. | Used after ownership commits while waiting for services to finish.
cloud_handoff_jobs | Handoff-worker generations, leases, retries, and outcomes. | Updated as background coordination is claimed, renewed, or retried.
cloud_handoff_outbox | Pending prepare and abort commands for handoff participants. | Reliably delivers cross-service handoff preparation or cancellation messages.
cloud_handoff_participants | Services participating in a handoff operation. | Fixes coordination targets and receipt requirements after a handoff starts.
cloud_handoff_prepare_acknowledgments | Participant hold and drain-checkpoint receipt hashes. | Used to confirm that resource changes have paused and work has drained.
cloud_ownership_handoffs | Primary Brand Cloud handoff operations with source, target, version, and phase. | Created after transfer acceptance and queried through preparation, cancellation, or commit.
device_claim_tokens | Hashed device-claim tokens, organization, device identity, and service options. | Used when issuing or validating a device-claim grant.
device_claims | Associations between claim tokens, devices, claimants, and claim status. | Used to claim devices, track provisioning, and prevent duplicate claims.
device_entitlement_snapshots | Revisioned product and service options with entitlement digests for devices. | Used to compute or distribute a device-service entitlement snapshot.
device_group_members | Device membership in a group within an organization. | Used when adding or removing devices from groups or listing group members.
device_groups | Organization device groups, names, and descriptions. | Used to create management sets and select devices for bulk operations.
device_item_profiles | Brand Cloud device-type names, models, and default metadata. | Used to define product device specifications or profiles for production runs.
device_message_inbox | Received cross-service device messages, streams, and processing state. | Used to deduplicate incoming events and track processing progress.
device_message_outbox | Pending cross-service device messages, correlation IDs, and delivery state. | Used to deliver device-operation events reliably and retry failures.
device_operations | Device provisioning or control operations, requesters, payloads, and state. | Used when commands are submitted, cross-service results arrive, or operation history is queried.
device_pki_outbox | Device PKI work with issuer, state, and retry lease. | Used to request device certificates automatically or retry failed work.
device_tag_catalog | Organization-approved tag vocabulary and update times. | Used when creating or managing device-tag choices.
device_tags | Organization tags attached to individual devices. | Used when tagging or filtering devices.
device_user_bindings | End-user device roles, claim provenance, and disabled state. | Used to grant app control after a claim or revoke device access.
devices | Account Manager device registry with organization, model, and account-side status. | Used when registering, claiming, transferring, or listing organization devices.
email_outbox | Encrypted email payloads, template versions, idempotency keys, and delivery status. | Used to queue and retry verification, invitation, or other emails.
end_user_identities | Links between end users and external identity-provider subjects. | Used when signing in externally or merging app-user identities.
end_user_refresh_tokens | Hashed end-user refresh tokens and revocation times. | Used when renewing or revoking app sessions.
end_users | App end-user emails, credentials, display names, and enabled state. | Used for end-user registration, sign-in, or disabling accounts.
external_group_mappings | Rules mapping external groups to internal roles and scopes. | Used when identity-provider groups are translated into account permissions.
factory_enrollment_reservations | Factory device reservations, request hashes, and result evidence. | Used when reserving a production-run slot for one device and completing or canceling enrollment.
factory_production_runs | Production batches, issuance limits, validity windows, and product profiles. | Used to define factory batches and constrain device enrollment quantities.
identity_providers | OIDC issuer, client, and enablement settings. | Used to configure or select an external sign-in provider.
job_authorizations | Job capabilities, scopes, and authorization versions. | Used to approve sensitive background jobs and verify execution authority.
managed_cloud_write_receipts | Requests, responses, and idempotency receipts for managed-cloud writes. | Used when administrators change clouds on another user's behalf without duplicating writes.
oidc_login_states | Hashed OIDC state and nonce values, redirects, and expiry. | Used to validate callbacks and prevent replay after a sign-in redirect.
organization_member_activation_holds | Member-disable times, sources, and retained hold state. | Used to correct or protect organization-member activation rights.
organization_members | User roles, scopes, and disabled times within organizations. | Used when members join, roles change, or organization access is checked.
organizations | Primary organization and Brand Cloud records with type, quota, state, and metadata. | Used when creating clouds or organizations, changing ownership, or applying lifecycle limits.
permissions | Defined authorization capabilities by domain and action. | Used to define or look up ACL capabilities.
platform_admin_guard | Generation and write-lock state protecting the last platform administrator. | Updated by database triggers when administrator membership changes and the safety check is serialized.
platform_admin_recovery | Recovery requests with target, reason, digest, and status. | Used when administrator access recovery requires special approval.
platform_admin_recovery_approvals | Approver identities, roles, digests, and authentication times. | Used to collect multi-party recovery approval evidence.
platform_admin_recovery_audit | Ordered audit events for administrator recovery. | Used to trace recovery proposals, approvals, and execution.
platform_bootstrap | Singleton state for the initial administrator and sealed bootstrap. | Used at first startup to prevent bootstrap from being repeated.
platform_service_catalog_revisions | Revision numbers for the service catalog in each environment. | Updated after service manifests change so consumers can refresh the catalog.
platform_service_instances | Registered instances with certificate subjects, readiness, and leases. | Used when workloads register, renew leases, or go offline.
platform_service_manifests | Versioned service protocols, endpoints, options, and digests. | Used when publishing or validating service capability descriptions.
platform_service_registration_requests | Registration-request digests and certificate fingerprints. | Used to reject replayed or impersonated service-registration requests.
platform_service_workloads | Approved workload certificates and allowed option codes. | Used to control which instances may register for a service.
platform_services | Published service versions and status by environment. | Used to look up currently available services and versions.
product_collaborator_invitations | Product-collaborator invitations, roles, recipients, and tokens. | Used when a product owner invites another person to manage a product.
product_service_grants | Revisioned product service options, bindings, and snapshot digests. | Used when product services change or device entitlements are derived.
quota_raise_requests | Requests for higher organization device quotas with reasons and decisions. | Used when an evaluation organization requests more devices.
refresh_tokens | Hashed refresh tokens and revocation times for shared users. | Used to renew sign-in sessions or revoke them on logout.
role_assignments | Roles assigned to subjects, scopes, and organizations. | Used when role grants are added, queried, or revoked.
role_permissions | Many-to-many mappings from roles to permission definitions. | Used to resolve capabilities granted by a role.
roles | Role names, scopes, system-role flags, and disabled state. | Used when creating ACL roles or disabling old ones.
schema_migrations | Applied Account Manager SQL migration versions. | Read during startup and upgrade to skip migrations already applied.
test_lab_accounts | Short-lived mappings between test users, Brand Clouds, and end users. | Used when creating or revoking test-lab identities.
test_lab_bind_grants | Test-device binding-token hashes, accounts, devices, and consumption state. | Used to grant one-time device binding in the test environment.
test_lab_console_users | Mappings between test console users and end-user identities. | Used for test-console sign-in and identity integration.
test_lab_login_attempts | Timestamps of test-account sign-in attempts. | Defined for test-login throttling or audit; no direct non-DDL read or write path was found.
test_lab_sessions | Short-lived test sessions tied to devices, products, and video device IDs. | Used for test connections or operations and then expired or revoked.
user_identities | Links between shared users and OIDC issuers and subjects. | Used to find or link local accounts after external sign-in.
users | Human accounts with email, password, display name, and verification or disabled state. | Used for registration, sign-in, verification, and account management.
''',
    'Billing': '''
auto_topup_policies | Account thresholds, top-up amounts, limits, and selected payment methods. | Evaluated when a balance falls below its threshold or a policy is disabled.
balance_ledger_entries | Immutable debit or credit entries with amounts and resulting balances for commercial accounts. | Posted when payments, usage settlement, or refunds change a balance.
billing_access_states | Organization billing eligibility, reason, and version. | Consulted when allowing paid services or updating a restriction.
billing_activity_events | Billing activity types, status, amounts, and display actions. | Used to build the billing timeline or query recent financial activity.
billing_audit_events | Billing subjects, actors, requests, and event payloads. | Retained after payment, invoice, or access changes for auditability.
billing_cloud_closure_cancellations | Closure cancellation IDs and Account Manager decision digests. | Records cancellation evidence when cloud deletion is canceled before closure finishes.
billing_cloud_closure_completions | Completed settlement, Account Manager readiness, and closure receipt digests. | Saved when financial settlement has been verified and account capabilities are closed.
billing_cloud_closure_release_acks | Receipt digests for lifting a closure barrier after cancellation. | Confirms Billing restored resource permissions before cancellation finishes.
billing_cloud_closure_retired_commands | Invalidated closure commands and their previous receipts. | Prevents an old command from closing an account after cancellation or recovery.
billing_cloud_closure_revocation_acks | Provider receipts for revoked payment methods or setup sessions. | Confirms each external payment capability has been disabled during closure.
billing_cloud_closure_revocations | Fixed worklist of payment methods and setup sessions to revoke. | Created at closure start to make revocation tasks immutable.
billing_cloud_closure_settlements | Financial closure evidence, usage, invoice, and provider checkpoints, and freshness. | Created when a sufficiently current settlement snapshot is verified before closure.
billing_cloud_closures | Commercial-account closure operation, original responsibility period, cutoff, and phase. | Used while preparing, closing, or canceling Billing participation in Brand Cloud deletion.
billing_cloud_creation_receipts | Idempotent receipts for cloud-creation events and commercial accounts. | Deduplicates first or repeated Account Manager cloud-creation messages.
billing_cloud_preflight_receipts | Closure preflight account, owner, state, and checkpoint digests. | Returned as evidence when deletion asks whether Billing can close.
billing_handoff_abort_acknowledgments | Receipts for releasing a Billing handoff hold after abort. | Used once a canceled ownership handoff has restored Billing state.
billing_handoff_balance_snapshots | Balance, currency, version, and cutoff snapshots for a handoff. | Freezes a balance baseline before the new owner confirms financial responsibility.
billing_handoff_cancellations | Handoff cancellation requests and Account Manager decision digests. | Stores immutable request evidence when a cancellation command arrives.
billing_handoff_commit_authorizations | Commit authorization bound to a handoff and snapshot version. | Permits the responsibility switch after the financial snapshot is confirmed.
billing_handoff_committed_decisions | Account Manager ownership and responsibility versions after commit. | Records the result and prevents duplicate or conflicting decisions.
billing_handoff_confirmations | User confirmation of a particular Billing snapshot version. | Used when handoff parties confirm the balance and payment responsibility.
billing_handoff_finalizations | Final receipts and versions after a handoff completes. | Confirms the responsibility-period switch is complete.
billing_handoff_settlement_receipts | Financial state, usage, and invoice checkpoint digests for handoff. | Verifies that settlement covers the cutoff before transfer.
billing_handoff_setup_observations | Observed provider state of payment setup sessions during handoff. | Checks whether incomplete card setup blocks a transfer.
billing_invoice_documents | Generated invoice bytes, hashes, and renderer versions. | Used to generate, download, or verify an unchanged invoice document.
billing_invoice_lines | Per-line invoice rates, usage, unit prices, and descriptions. | Assembles priced usage into an explainable bill.
billing_invoices | Invoice numbers, organizations or accounts, periods, and status. | Used when closing a billing period, issuing an invoice, or querying it.
billing_ledger_responsibility | Responsibility periods and evidence assigned to balance entries. | Determines who owns a historical ledger entry after a handoff.
billing_ownership_handoffs | Source, target, and phase of a commercial-account ownership transfer. | Created when Account Manager initiates a transfer and updated through preparation and commit.
billing_payment_method_responsibility | Evidence linking payment methods to accounts and responsibility periods. | Queried during owner transfers or payment-method revocation.
billing_payment_responsibility | Payment-intent responsibility periods and provenance. | Identifies the responsible party during handoff or provider reversal.
billing_periods | Organization billing intervals, pricing versions, locks, and closure times. | Used to aggregate usage, calculate invoices, and settle each period.
billing_profiles | Invoice legal name, tax ID, address, contact, and delivery preferences. | Used to prepare invoices or update billing details.
billing_provider_reversal_allocations | Provider reversal allocations to periods and ledger entries. | Records responsibility when a refund, chargeback, or reversal is posted.
billing_provider_reversal_events | Original provider transactions, amounts, reasons, and request digests. | Records payment-provider refund, chargeback, or reversal notifications.
billing_provider_reversal_reviews | Reversals and reason codes requiring human review. | Opened when automatic allocation cannot establish responsibility.
billing_responsibility_periods | Effective account-responsibility intervals for an owner and version. | Used during handoff or historical financial attribution queries.
billing_retired_policy_evidence | Versions and old-setting snapshots for disabled auto-top-up policies. | Retains policy evidence after handoff or closure; populated by a database trigger.
billing_retired_profiles | Snapshots of retired billing profiles. | Archives old billing details after ownership handoff.
billing_usage_facts | Source usage IDs, service metrics, quantities, and metering windows. | Deduplicated and included in a billing period when metering evidence arrives.
commercial_accounts | Organization commercial accounts, currencies, available balances, and status. | Used to open accounts, post funds, or restrict payments; organization UUID is the cross-service identifier.
invoice_settlement_links | Invoice-to-ledger settlement links and retry state. | Used to offset an invoice with payments or balance entries and retry failed links.
payment_attempts | Individual provider operations and normalized results for a payment intent. | Recorded for charges, transaction inquiries, and retries.
payment_consents | Accepted payment-consent text versions, acceptors, and revocation state. | Checked before binding a payment method or enabling paid features.
payment_intents | Account payment requests, amounts, providers, and current states. | Created and advanced for automatic top-ups or manual payments.
payment_method_setup_sessions | Payment-method setup sessions, provider references, and status. | Used when adding a card, processing a callback, or cleaning up a failed setup.
payment_methods | Provider payment methods, encrypted references, and masked details available to an account. | Used to choose a payment instrument, top up automatically, or revoke a card.
payment_reconciliation_jobs | Payment-intent follow-up inquiries, leases, retries, and errors. | Reconciles missing callbacks or uncertain transaction outcomes.
payment_simulator_newebpay_transactions | Simulated NewebPay transactions, amounts, scenarios, and capture states. | Exercises provider responses and callback branches in tests.
payment_simulator_operations | Simulated payment operations, references, and configured scenarios. | Exercises local charge, refund, or inquiry flows.
payment_simulator_setup_sessions | Simulated card-setup sessions, token hashes, and provider references. | Tests successful or failed payment-method setup.
payment_webhook_inbox | Encrypted provider callback payloads, verification, and processing states. | Receives, deduplicates, verifies, and retries provider webhooks.
pricing_plan_versions | Pricing-plan versions, currencies, effective windows, and status. | Used to publish new prices or select the version applicable to a period.
pricing_rates | Service and metric unit prices, units, and rounding rules under a plan version. | Used to calculate usage charges and invoice lines.
schema_migrations | Applied Billing database migration versions. | Checked at startup or upgrade to avoid rerunning a migration.
''',
    'Video Cloud': '''
billing_usage_collector_receipts | Processed logger-record sequence numbers, digests, and usage IDs. | Prevents duplicate ingestion when a metering source is rescanned.
billing_usage_collector_state | Cursors and high-water marks per collector and source store. | Updated as periodic usage-log collection advances or resumes.
billing_usage_fact_outbox | Usage facts awaiting Billing delivery, payload digests, and retry state. | Reliably delivers accepted metering facts to Billing.
brand_webhook_outbox | Device events awaiting brand delivery with leases and retry information. | Enqueued after device-event acceptance and retried after delivery failure.
brand_webhook_receipts | Results, HTTP status, and errors for webhook attempts. | Used to inspect completed delivery or determine whether to retry.
brand_webhook_subscriptions | Organization webhook endpoints, encrypted secrets, and enabled state. | Used when a brand configures or disables an event destination.
cert_issue_events | Audit events for device-certificate CSRs, IPs, serials, and fingerprints. | Used to trace request origins and issued certificates after signing.
cert_issue_requests | Device CSR issuance requests, fingerprints, results, and TTLs. | Deduplicates a repeated issuance request and returns its existing result.
clip_metadata | Device clip IDs, events, metadata, and expiration times. | Indexed after upload for queries and expiration cleanup.
clip_upload_parts | Multipart upload part numbers, sizes, and checksums. | Used to upload or resume parts and verify integrity before completion.
clip_uploads | Clip-upload sessions, devices, time ranges, and status. | Used when a device starts, finalizes, or aborts an upload.
device_command_messages | Payloads and creation times of queued device commands. | Used to queue or resend device-control commands.
device_event_acceptances | Accepted device event IDs, types, and payload digests. | Validates and deduplicates inbound events before downstream notifications.
device_logs | Basic device log event types, details, and occurrence times. | Queried for device-operation and failure diagnosis.
device_presence_outbox | Pending online/offline events and account mappings. | Synchronizes presence to the account service after connection changes, with retries.
device_runtime_log_gaps | Expected and actual sequence numbers missing from runtime-log streams. | Recorded when an incoming device log skips a sequence number.
device_runtime_logs | Device runtime-log sequence, level, source, message, and attributes. | Stored after device log submission for search and diagnosis.
device_socket_sessions | Active WebSocket or MQTT connections, serving instances, and last activity. | Updated on device connection, heartbeat, or disconnect.
device_transfer_fences | Cross-cloud transfer reservations and original organization or account IDs. | Blocks conflicting device operations during ownership handoff.
devices | Video Cloud device IDs, activation and online state, connections, and account mappings. | Used for activation, connection, configuration changes, and cloud-state queries.
factory_cloud_handoffs | Factory-cloud handoff operations, owners, and hold or drain evidence. | Used while preparing and completing factory-cloud ownership handoffs.
factory_device_entitlements | Factory device, certificate, and allowed-service entitlement revisions. | Used to validate service access after enrollment or revoke an entitlement.
factory_enrollment_journal | Factory enrollment requests, reservations, devices, and phase evidence. | Tracks, retries, or cancels work from certificate request through projection.
firmware_campaigns | Model-targeted firmware rollout campaigns and policy. | Used to start or track firmware rollouts.
firmware_releases | Firmware objects, manifests, and publication details by model and version. | Used to publish downloadable firmware and select a compatible version.
firmware_rollouts | Per-device upgrade targets, current versions, states, and errors. | Used to dispatch updates and record device results.
firmware_targets | Designated target versions for device models. | Queried when a device checks whether it needs an upgrade.
legacy_device_states | Legacy-protocol connections, registration and key times, and device state. | Supports legacy device connectivity and state migration.
mqtt_usage_windows | MQTT broker traffic quantities and metering windows. | Aggregates publish and delivery bytes and counts periodically.
notification_dead_letters | Failed notification IDs, attempt counts, and last errors. | Retained for investigation after repeated delivery failure.
notification_deliveries | Deduplication keys and delivery times of successful notifications. | Checked before sending to avoid duplicate delivery.
ota_campaign_dispatch_state | Next permitted dispatch times for OTA campaigns. | Updated while rate-limiting a campaign and scheduling its next device batch.
ota_campaign_targets | Target device states, attempts, leases, and errors for campaigns. | Used to schedule, dispatch, retry, or finish OTA work per device.
ota_campaigns | OTA campaigns, product and release links, state, and configuration payloads. | Used to create or stop upgrade campaigns.
ota_deployment_events | Ordered event payloads reported for individual deployments. | Deduplicates and tracks device-reported upgrade progress.
ota_deployments | Per-device OTA deployments, states, and latest event sequence numbers. | Used to dispatch upgrades and summarize device outcomes.
ota_global_dispatch_state | Next globally permitted OTA dispatch time shared across campaigns. | Enforces an overall OTA dispatch load limit.
ota_legacy_migrations | Legacy OTA sources mapped to canonical IDs and migration results. | Prevents repeat migration and records failures during a model upgrade.
ota_releases | Product OTA versions, builds, states, and payloads. | Used to create, publish, or retire app or product upgrade content.
pki_app_issuances | App-certificate CSRs, issuers, request digests, and issuance results. | Used for app identity issuance or idempotent retry.
pki_app_revocations | App-certificate fingerprints, revocation reasons, and CRL digests. | Used when an app certificate expires or is revoked.
pki_approvals | Approvers, roles, and digests for sensitive PKI operations. | Collects authorization evidence before root or issuer changes.
pki_audit | Ordered PKI audit events for issuers and operations. | Used to trace issuance, revocation, or trust changes.
pki_bundle_acknowledgments | Consumer receipts for loaded issuer-bundle versions. | Confirms services synchronized after a certificate-chain update.
pki_certificate_bindings | Certificate fingerprints bound to devices, clouds, products, and validity windows. | Resolves identity during device authentication and updates bindings on revocation or rotation.
pki_crl_acknowledgments | Consumer receipts for loaded CRL digests. | Confirms downstream systems accepted a distributed revocation list.
pki_crls | Issuer CRL PEM, serial number, validity window, and digest. | Used to publish or inspect revoked certificates.
pki_deployment_bootstrap_sessions | Progress installing initial trust roots and subjects in a deployment. | Used to configure and confirm PKI bootstrap in a new environment.
pki_issuers | PKI issuer hierarchy, environment, domain, version, and status. | Used to create, select, rotate, or disable an issuer.
pki_legacy_imports | Legacy PKI import operations and documents. | Supports existing certificates and reviews migration evidence.
pki_legacy_windows | Allowed start and end times for legacy trust roots. | Controls acceptance of old certificates during a PKI transition.
pki_operations | PKI administrative operations, idempotency keys, digests, status, and reasons. | Used to submit issuer or trust changes that require approval.
pki_replacements | Old-to-new certificate or CSR replacements and overlap periods. | Used to rotate certificates without interrupting services.
pki_root_distrust | Versions, issuers, and digests of root-distrust policy. | Publishes removal of a superseded or unsafe trusted root.
pki_root_distrust_acknowledgments | Consumer receipts for loaded root-distrust policy. | Confirms nodes stopped trusting an old root.
pki_server_issuances | Server-certificate DNS names, CSRs, issuers, and issuance results. | Used to issue or retry server TLS certificates.
pki_server_revocations | Server-certificate revocation reasons and CRL digests. | Used when a server certificate expires, is replaced, or is revoked after an incident.
pki_service_client_issuances | Service-client certificate subjects, contexts, CSRs, and issuers. | Used to issue inter-service mTLS client certificates.
pki_service_client_revocations | Service-client certificate revocation reasons and times. | Used when a workload loses authorization or its certificate becomes invalid.
pki_signing_claims | Traceable device, cloud, and product claims for individual signing requests. | Fixes and deduplicates identity scope during device-certificate issuance.
product_telemetry_events | Product-level event schema, source, and account or device identifiers. | Used to validate inbound telemetry and derive later product state.
refresh_tokens | Video Cloud refresh tokens associated with principals and scopes. | Used for Video Cloud API session renewal or expiration checks.
registry_nodes | Service-registry node regions, endpoints, weights, and capacity. | Used to discover available backends and allocate connections.
relay_geolinks | Country-code priorities for relay regions. | Schema defines geographic routing data; no direct non-DDL read or write path was found.
relay_nodes | Relay node URLs, regions, load, and health state. | Schema defines relay selection data; no direct non-DDL read or write path was found.
resource_cloud_deletions | Cloud, owner, phase, and receipts for Video Cloud resource deletion. | Freezes and cleans Video Cloud resources during Account Manager–coordinated cloud deletion.
resource_cloud_handoffs | Video Cloud handoff participants, cutoffs, and hold evidence. | Pauses new work and confirms resource transfer when a cloud owner changes.
runtime_config | Persistent Video Cloud key/value runtime settings. | Read or written at startup or when a persistent setting changes.
turn_nodes | TURN node addresses, ports, capacity, and active session counts. | Used to select TURN resources for media connections.
usage_event_receipts | Deduplication receipts for raw usage-event payloads and digests. | Validates metering events and prevents duplicate billing.
usage_facts | Auditable usage facts with service metrics, devices or clouds, time windows, and sequences. | Converts observed consumption into metering evidence.
''',
    'Cloud Admin': '''
audit_events | Actors, targets, outcomes, and request IDs for administrative actions. | Records an audit trail after an administrator performs a sensitive action.
batch_job_action_receipts | Receipts for batch-job state transitions and action keys. | Makes pause, resume, and cancel actions idempotent.
batch_job_items | Position, state, error, and retryability of each device or item in a batch. | Used for per-item provisioning, failure retry, and paginated progress queries.
batch_jobs | Organization batch-job types, scopes, progress, leases, and checkpoints. | Used to schedule, execute, and recover bulk provisioning or administration work.
devices | Device, organization, status, and Video Cloud ID shown in the admin interface. | Used for listing, searching, and viewing device state; it is not an upstream master-data foreign key.
integration_settings | Integration key/value settings, sources, and update times. | Defined in schema for integration configuration; no direct non-DDL read or write path was found.
operations | Device-operation summaries, organizations, status, and upstream operation IDs. | Used to display recent operations and outcomes in the admin console.
ota_scope_previews | OTA target scopes, hashes, match counts, and data freshness. | Used to preview affected devices before dispatch.
platform_admins | Local platform-admin emails, password hashes, and roles. | Used for admin-console sign-in and role checks.
provisioning_sources | Imported provisioning files, checksums, products, and device-ID lists. | Used when uploading factory or device lists for bulk provisioning.
readiness_facts | Per-device readiness-layer states, errors, and operations. | Defined in schema for layered readiness projection; no direct non-DDL read or write path was found.
schema_migrations | Applied Cloud Admin SQLite migration versions and names. | Checked at startup or upgrade to establish database structure.
sessions | Admin or upstream session principals, tokens, organizations, and expiry. | Used on sign-in, refresh, and active-organization switches.
upstream_devices | Reserved fields and synchronization times for upstream device projections. | Only DDL was found, with no non-DDL synchronization read or write path; this is not a cross-database FK.
upstream_operations | Reserved states and sources for upstream operation projections. | Only DDL was found, with no non-DDL synchronization read or write path.
upstream_organizations | Reserved names, roles, and synchronization times for upstream organization projections. | Only DDL was found, with no non-DDL synchronization read or write path.
''',
    'Cloud Frontend': '''
analytics_events | Website page, call-to-action, dwell-time, and referral analytics events. | Records visitor behavior while they browse or interact with website elements.
leads | Visitor names, companies, contact details, requirements, and messages. | Created when someone submits a contact or sales inquiry.
sdk_download_acceptances | SDK download terms versions, packages, sessions, and request IDs. | Records acceptance before providing an SDK package.
search_chunks | Search-document text chunks and embedding JSON. | Used when building a semantic search index or retrieving similar passages.
search_documents | Searchable document titles, URLs, locales, sources, and bodies. | Imported or updated for the website index and displayed in search results.
''',
}


def entity_notes():
    notes = {}
    for owner, rows in NOTE_ROWS.items():
        notes[owner] = {}
        for line in rows.strip().splitlines():
            name, purpose, scenario = (part.strip() for part in line.split('|'))
            if name in notes[owner]:
                raise ValueError(f'Duplicate narrative: {owner}.{name}')
            notes[owner][name] = (purpose, scenario)
    return notes


ENTITY_NOTES = entity_notes()
