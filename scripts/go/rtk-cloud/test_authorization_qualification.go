package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type authorizationQualificationSpec struct {
	TestID     string
	Repository string
	Package    string
	GoTest     string
	Targets    []authorizationQualificationTarget
	Assertions map[string]map[string]string
	Workflows  map[string]map[string]string
}

type authorizationQualificationTarget struct {
	Package        string
	GoTest         string
	WorkingDir     string
	SetupCommands  [][]string
	Command        []string
	Label          string
	OutputContains []string
}

var authorizationQualificationSpecs = append([]authorizationQualificationSpec{
	{
		TestID: "INT-VC-AUTH-BOUNDARY-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestRequestTokenRequiresAuthorization",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-CREDENTIAL-BOUNDARY-001": {
				"missing_bootstrap_credential_rejected": "PASS",
				"bearer_token_route_preserved":          "PASS",
			},
			"REQ-CONTRACT-AUTH-ROUTE-SCOPE-001": {
				"token_route_scope_enforced":   "PASS",
				"unauthenticated_issue_denied": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-LIFETIME-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestRequestTokenSuccess",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-LIFETIME-001": {
				"signed_iat_exp_authoritative": "PASS",
				"requested_lifetime_signed":    "PASS",
				"unsigned_expiry_not_exported": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-REISSUE-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestRefreshTokenAllowsReuseForAppScope",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-REISSUE-001": {
				"signed_source_validated":       "PASS",
				"fresh_access_token_issued":     "PASS",
				"source_token_remains_reusable": "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-CONTRACT-AUTH-REISSUE-001": {
				"issue_source_token":       "PASS",
				"reissue_signed_token":     "PASS",
				"reuse_valid_source_token": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-IDENTITY-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestRequestTokenRejectsMTLSDeviceMismatch",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-SUBJECT-001": {
				"certificate_subject_enforced": "PASS",
				"foreign_device_rejected":      "PASS",
			},
			"REQ-CONTRACT-AUTH-CERT-IDENTITY-001": {
				"certificate_identity_canonical": "PASS",
				"request_override_rejected":      "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-MQTT-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestMQTTAuthenticateDeniesForgedTenantAndClientID",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-MQTT-TENANT-001": {
				"forged_tenant_rejected":     "PASS",
				"unbound_client_id_rejected": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-MQTTBILL-001", Repository: "rtk_video_cloud", Package: "./internal/mqttusageapp", GoTest: "TestMQTTBrokerCallbackFeedsMeterAndRequiresToken",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-MQTT-BILLING-001": {
				"broker_identity_attributed": "PASS",
				"payload_override_ignored":   "PASS",
				"broker_auth_required":       "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-MTLS-001", Repository: "rtk_video_cloud", Package: "./internal/apiapp", GoTest: "TestBuildAPITLSConfigRequiresVerifiedClientCertWhenMTLSRequired",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-MTLS-TRUST-001": {
				"verified_client_cert_required": "PASS",
				"runtime_mtls_fail_closed":      "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-ENTITLEMENT-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestRequestTokenMTLSProjectionErrorsFailClosed",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-ENTITLEMENT-001": {
				"missing_projection_rejected":     "PASS",
				"unavailable_projection_rejected": "PASS",
				"revoked_entitlement_rejected":    "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-FACTORYCTX-001", Repository: "rtk_video_cloud", Package: "./internal/factoryenroll", GoTest: "TestServiceUsesProductionJWTContextForIssuerSelection",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-FACTORY-CONTEXT-001": {
				"signed_brand_cloud_selected": "PASS",
				"signed_profile_selected":     "PASS",
				"request_override_ignored":    "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-AUTH-LEGACY-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestDeviceSocketRejectsLegacyCertWithoutBearer",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTH-LEGACY-001": {
				"legacy_certificate_header_rejected": "PASS",
				"websocket_bearer_required":          "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-BOUNDARY-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationVideoCloudRuntimeScopeDoesNotGrantProductRole",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-BOUNDARY-001": {
				"runtime_admin_scope_rejected": "PASS",
				"product_user_route_rejected":  "PASS",
				"platform_acl_route_rejected":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-MATRIX-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationAuthorizationAndTenancyMatrix",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-ROUTES-001": {
				"declared_role_access":       "PASS",
				"cross_tenant_denial":        "PASS",
				"non_disclosing_not_found":   "PASS",
				"disabled_subject_rejection": "PASS",
			},
			"REQ-CONTRACT-AUTHZ-COMPAT-001": {
				"member_device_lifecycle":       "PASS",
				"platform_admin_compatibility":  "PASS",
				"ordinary_user_platform_denial": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-SOURCE-001", Repository: "rtk_account_manager", Package: "./internal/store", GoTest: "TestACLExternalGroupMappingCreatesScopedAssignment",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-SOURCE-001": {
				"explicit_external_group_mapping": "PASS",
				"persisted_role_assignment":       "PASS",
				"organization_scope_enforced":     "PASS",
				"unmapped_permission_denied":      "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-MODEL-001", Repository: "rtk_account_manager", Package: "./internal/store", GoTest: "TestACLRoleAssignmentsAuthorizeInsideScopeOnly",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-MODEL-001": {
				"explicit_assignment_required": "PASS",
				"organization_scope_enforced":  "PASS",
				"read_only_write_denied":       "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-CATALOG-001", Repository: "rtk_account_manager", Package: "./internal/store", GoTest: "TestACLSeedPermissionCatalogAndSystemRoles",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-CATALOG-001": {
				"permission_catalog_seeded": "PASS",
				"stable_role_names_seeded":  "PASS",
			},
			"REQ-CONTRACT-AUTHZ-DEFAULTS-001": {
				"declared_positive_grants": "PASS",
				"undeclared_write_denied":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-AUTHZ-PROVIDER-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationChipsetProviderACLRefreshVisibilityAndAudit",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-PROVIDER-001": {
				"read_permission_independent":    "PASS",
				"edit_permission_independent":    "PASS",
				"publish_permission_independent": "PASS",
				"provider_audit_recorded":        "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-ORG-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationOwnerCanUpdateOrganization"},
			{Package: "./internal/store", GoTest: "TestDeveloperSignupCreatesDefaultBrandCloudAndEnforcesCloudLimit"},
			{Package: "./internal/store", GoTest: "TestIntegrationDatabaseSchemaInvariants"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-ORG-AUTHORITY-001": {
				"postgres_records_are_authoritative": "PASS",
				"customer_and_brand_kinds_preserved": "PASS",
				"cross_tenant_mutation_rejected":     "PASS",
			},
			"REQ-AM-ORG-DATA-001": {
				"blank_name_rejected":           "PASS",
				"tenant_kind_constraint_exists": "PASS",
				"tenant_slug_is_normalized":     "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-SESSION-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationRegisterLoginRefreshAndLogout"},
			{Package: "./internal/api", GoTest: "TestIntegrationDisabledUserCannotUseExistingTokens"},
			{Package: "./internal/auth", GoTest: "TestPasswordHashAndCheck"},
			{Package: "./internal/store", GoTest: "TestIntegrationDatabaseSchemaInvariants"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-USER-CREDENTIAL-001": {
				"normalized_email_constraint_exists": "PASS",
				"modern_password_hash_verified":      "PASS",
				"disabled_login_rejected":            "PASS",
				"disabled_refresh_rejected":          "PASS",
				"disabled_access_token_rejected":     "PASS",
			},
			"REQ-AM-PASSWORD-SESSION-001": {
				"password_login_succeeds":       "PASS",
				"protected_identity_read":       "PASS",
				"refresh_token_rotated":         "PASS",
				"rotated_token_replay_rejected": "PASS",
				"logout_revokes_refresh":        "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-AM-PASSWORD-SESSION-001": {
				"login_with_password":         "PASS",
				"rotate_refresh_token":        "PASS",
				"reject_rotated_token_replay": "PASS",
				"logout_rotated_session":      "PASS",
				"reject_logged_out_refresh":   "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-MEMBERSHIP-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationOwnerCanUpdateAndRemoveMember"},
			{Package: "./internal/api", GoTest: "TestIntegrationOwnerCanDisableAndEnableMemberUser"},
			{Package: "./internal/api", GoTest: "TestIntegrationLastOwnerCannotBeRemovedOrDowngraded"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-MEMBERSHIP-INVARIANT-001": {
				"member_add_update_remove_succeeds": "PASS",
				"disabled_member_access_rejected":   "PASS",
				"final_owner_downgrade_rejected":    "PASS",
				"final_owner_disable_rejected":      "PASS",
				"final_owner_remove_rejected":       "PASS",
				"database_owner_invariant_enforced": "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-AM-MEMBERSHIP-001": {
				"add_organization_member":       "PASS",
				"update_member_role":            "PASS",
				"reject_final_owner_downgrade":  "PASS",
				"reject_final_owner_disable":    "PASS",
				"disable_non_owner_member":      "PASS",
				"reject_disabled_member_access": "PASS",
				"enable_non_owner_member":       "PASS",
				"remove_non_owner_member":       "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-ENDUSER-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationAppEndUserLoginDoesNotCreateBrandLinkAndIssuesGlobalSubject"},
			{Package: "./internal/api", GoTest: "TestIntegrationAppEndUserClaimCreatesMultiBrandBindings"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-END-USER-ISOLATION-001": {
				"global_subject_stable_across_brands": "PASS",
				"login_does_not_create_brand_link":    "PASS",
				"platform_subject_rejected":           "PASS",
				"foreign_brand_not_exposed":           "PASS",
				"direct_identifier_masked":            "PASS",
			},
			"REQ-AM-END-USER-PROJECTION-001": {
				"brand_projection_created_on_claim": "PASS",
				"current_brand_projection_only":     "PASS",
				"multi_brand_subject_preserved":     "PASS",
			},
			"REQ-AM-DEVICE-BINDING-AUTH-001": {
				"claim_creates_active_binding":   "PASS",
				"bound_device_authorized":        "PASS",
				"unbound_device_rejected":        "PASS",
				"certificate_alone_insufficient": "PASS",
			},
			"REQ-AM-APP-AUTHORIZATION-001": {
				"app_subject_type_enforced":      "PASS",
				"app_refresh_namespace_enforced": "PASS",
				"app_logout_revokes_session":     "PASS",
				"invalid_claim_rejected":         "PASS",
				"active_device_binding_required": "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-AM-END-USER-001": {
				"login_app_end_user":          "PASS",
				"bind_claimed_device":         "PASS",
				"read_isolated_end_user":      "PASS",
				"reject_invalid_device_claim": "PASS",
				"logout_app_end_user":         "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-BRANDUSER-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminCreatesActiveBrandCloudUser",
		Assertions: map[string]map[string]string{
			"REQ-AM-BRAND-USER-BOUNDARY-001": {
				"brand_email_normalized":         "PASS",
				"tenant_login_required":          "PASS",
				"platform_login_rejected":        "PASS",
				"app_identity_route_rejected":    "PASS",
				"disabled_brand_user_rejected":   "PASS",
				"brand_password_rotation_scoped": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-REGISTRY-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationRoleAuthorizationDeviceScopeAndSerialUniqueness"},
			{Package: "./internal/api", GoTest: "TestIntegrationFleetGroupsAndTags"},
			{Package: "./internal/store", GoTest: "TestIntegrationDatabaseSchemaInvariants"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-DEVICE-IDENTITY-001": {
				"server_uuid_is_canonical":    "PASS",
				"external_serial_is_metadata": "PASS",
				"same_serial_other_tenant_ok": "PASS",
			},
			"REQ-AM-DEVICE-DATA-001": {
				"blank_name_constraint_exists":     "PASS",
				"tenant_serial_unique":             "PASS",
				"cross_tenant_read_rejected":       "PASS",
				"disabled_device_remains_readable": "PASS",
				"disabled_mutation_rejected":       "PASS",
				"delete_is_idempotent":             "PASS",
			},
			"REQ-AM-FLEET-DATA-001": {
				"blank_group_rejected":             "PASS",
				"group_assignment_is_idempotent":   "PASS",
				"disabled_target_retained":         "PASS",
				"tag_assignment_is_idempotent":     "PASS",
				"cross_tenant_group_read_rejected": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-FACTORY-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminDeviceItemProfileLifecycle",
		Assertions: map[string]map[string]string{
			"REQ-AM-FACTORY-CONTEXT-001": {
				"signed_brand_context_preserved":   "PASS",
				"signed_profile_context_preserved": "PASS",
				"signed_run_context_preserved":     "PASS",
				"audience_is_factory_enroll":       "PASS",
				"audit_payload_excludes_bearer":    "PASS",
				"audit_payload_excludes_secret":    "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-TOKEN-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationEmailVerificationAndPasswordRecovery"},
			{Package: "./internal/store", GoTest: "TestBrandCloudLoginActivationTokenIsTenantScoped"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-ONE-TIME-TOKEN-001": {
				"token_hash_persisted":      "PASS",
				"purpose_isolated":          "PASS",
				"tenant_scope_enforced":     "PASS",
				"expiry_rejected":           "PASS",
				"replay_rejected":           "PASS",
				"throttle_enumeration_safe": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-IDENTITY-LIFECYCLE-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/store", GoTest: "TestDeviceMessagePersistenceRejectsInvalidSchemaValues"},
			{Package: "./internal/store", GoTest: "TestCreateOrGetDeviceOperationIsIdempotent"},
			{Package: "./internal/worker/inbox", GoTest: "TestRunOnceDeadLettersLifecycleMessagesWithMismatchedPartitionKeys"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-LIFECYCLE-MESSAGE-INTEGRITY-001": {
				"stream_values_validated":          "PASS",
				"message_types_validated":          "PASS",
				"schema_versions_validated":        "PASS",
				"operation_identity_is_idempotent": "PASS",
				"device_partition_key_enforced":    "PASS",
				"invalid_partition_dead_lettered":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-OPERATIONS-CONFIG-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/config", GoTest: "TestLoadRequiresJWTSecrets"},
			{Package: "./internal/config", GoTest: "TestLoadAcceptsPEMJWTSignerWithoutSharedSecrets"},
			{Package: "./internal/config", GoTest: "TestLoadRejectsIncompletePEMJWTSigner"},
			{Package: "./internal/config", GoTest: "TestLoadRejectsUnknownJWTSignerProvider"},
			{Package: "./internal/config", GoTest: "TestProductionEmailConfigurationFailsClosed"},
			{Package: "./internal/config", GoTest: "TestProductionSendMailHTTPConfiguration"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-RUNTIME-CONFIG-001": {
				"hs256_requires_secrets":              "PASS",
				"pem_signer_complete":                 "PASS",
				"incomplete_pem_rejected":             "PASS",
				"unknown_signer_rejected":             "PASS",
				"production_email_fails_closed":       "PASS",
				"sendmail_https_and_bearer_validated": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-OPERATIONS-CACHE-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/usercache", GoTest: "TestStoreGetUserFallsBackToPostgresWhenRedisUnavailable"},
			{Package: "./internal/usercache", GoTest: "TestStoreRegisterRefreshesUserAuthCacheAfterCommit"},
			{Package: "./internal/usercache", GoTest: "TestStoreBrandAndEndUserMutationsRefreshCache"},
			{Package: "./internal/usercache", GoTest: "TestStoreIgnoresCacheReadAndWriteErrors"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-CACHE-RESILIENCE-001": {
				"postgres_read_survives_cache_outage": "PASS",
				"committed_write_not_rolled_back":     "PASS",
				"platform_cache_refreshed":            "PASS",
				"brand_cache_refreshed":               "PASS",
				"end_user_cache_refreshed":            "PASS",
				"cache_write_failure_ignored":         "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-OPERATIONS-OIDC-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationOIDCProviderLoginAndCallback"},
			{Package: "./internal/auth", GoTest: "TestProviderResolverRejectsUnsupportedOrUnsetSecretRefs"},
			{Package: "./internal/auth", GoTest: "TestOIDCTokenErrorsDoNotContainProviderTokens"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-OIDC-SECRET-001": {
				"runtime_secret_reference_required": "PASS",
				"unsupported_secret_ref_rejected":   "PASS",
				"oidc_callback_completes":           "PASS",
				"provider_tokens_redacted":          "PASS",
				"client_secret_not_surfaced":        "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-SIGNUP-EMAIL-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationSignupQueuesEncryptedEmailWithoutCallingSMTP"},
			{Package: "./internal/store", GoTest: "TestEmailOutboxTokenAndQueueAreTransactional"},
			{Package: "./internal/api", GoTest: "TestIntegrationEmailVerificationAndPasswordRecovery"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-EMAIL-DELIVERY-001": {
				"token_and_outbox_atomic":        "PASS",
				"outbox_payload_encrypted":       "PASS",
				"direct_smtp_not_called":         "PASS",
				"delivery_failure_safe":          "PASS",
				"enumeration_safe_response":      "PASS",
				"one_time_token_replay_rejected": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-OTA-OPERATOR-001", Repository: "rtk_video_cloud", Package: "./internal/httpapi", GoTest: "TestSKUOTAOperatorAndDeviceAuthenticationBoundaries",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-OTA-OPERATOR-001": {
				"operator_credential_required": "PASS",
				"brand_context_required":       "PASS",
				"signed_brand_context_used":    "PASS",
			},
			"REQ-CONTRACT-AUTHZ-OTA-DEVICE-001": {
				"device_credential_required":   "PASS",
				"non_device_scope_rejected":    "PASS",
				"token_subject_is_device_id":   "PASS",
				"request_body_cannot_override": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-OTA-ARTIFACT-001", Repository: "rtk_video_cloud", Package: "./internal/skuota", GoTest: "TestArtifactTokenIsDeploymentScopedAndExpires",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-OTA-ARTIFACT-001": {
				"foreign_device_rejected":   "PASS",
				"deployment_scope_enforced": "PASS",
				"expired_grant_rejected":    "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-OTA-TENANT-001", Repository: "rtk_video_cloud", Package: "./internal/skuota", GoTest: "TestTenantIsolationAndEventIdempotency",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-AUTHZ-OTA-TENANT-001": {
				"foreign_brand_release_denied": "PASS",
				"foreign_state_not_returned":   "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDPROFILE-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminDeviceItemProfileLifecycle",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-PROFILE-001": {
				"inventory_metadata_preserved":    "PASS",
				"service_options_explicit":        "PASS",
				"category_derived_acl_rejected":   "PASS",
				"invalid_service_option_rejected": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDIDENT-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationAppEndUserClaimCreatesMultiBrandBindings",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-IDENTITY-001": {
				"global_app_subject_created":   "PASS",
				"claim_creates_brand_link":     "PASS",
				"claim_creates_device_binding": "PASS",
			},
			"REQ-CA-BRAND-PRIVACY-001": {
				"current_brand_only":         "PASS",
				"foreign_brand_not_returned": "PASS",
				"direct_email_not_returned":  "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-CA-BRAND-IDENTITY-001": {
				"login_app_end_user":       "PASS",
				"resolve_app_device_claim": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDADMIN-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminBrandCloudLifecycle",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-ADMIN-AUTH-001": {
				"platform_admin_allowed": "PASS",
				"ordinary_user_rejected": "PASS",
				"brand_scope_preserved":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-BRANDUSER-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminCreatesActiveBrandCloudUser",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-USER-PROVISION-001": {
				"brand_identity_isolated": "PASS",
				"verified_state_created":  "PASS",
				"password_not_reused":     "PASS",
				"lifecycle_is_idempotent": "PASS",
			},
			"REQ-CA-BRAND-AUDIT-001": {
				"lifecycle_events_recorded": "PASS",
				"platform_actor_attributed": "PASS",
				"brand_subject_attributed":  "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-CA-AUDIT-001": {
				"create_audited_brand_cloud": "PASS",
				"read_brand_cloud_audit":     "PASS",
			},
			"WF-CA-BRAND-001": {
				"create_brand_cloud":  "PASS",
				"create_brand_owner":  "PASS",
				"disable_brand_owner": "PASS",
				"enable_brand_owner":  "PASS",
				"delete_brand_owner":  "PASS",
			},
		},
	},
	{
		TestID: "INT-CA-BRANDBFF-001", Repository: "rtk_cloud_admin", Package: "./internal/app", GoTest: "TestPlatformAdminBrandCloudsProxyRequiresUpstreamToken",
		Assertions: map[string]map[string]string{
			"REQ-CA-BRAND-SOURCE-001": {
				"account_manager_results_returned": "PASS",
				"upstream_authority_required":      "PASS",
				"local_fallback_rejected":          "PASS",
			},
			"REQ-CA-BRAND-BFF-001": {
				"bearer_token_forwarded":   "PASS",
				"request_fields_forwarded": "PASS",
				"upstream_failure_safe":    "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-PROV-CHANNEL-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/crossserviceworker", GoTest: "TestProvisioningWorkerPublishesSuccessAndAcks"},
			{Package: "./internal/crossserviceworker", GoTest: "TestProvisioningWorkerReplaysCachedResultAcrossRestartWithFileStore"},
			{Package: "./internal/crossserviceworker", GoTest: "TestProvisioningWorkerDeadLettersPartitionKeyMismatchWithoutPublishing"},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-PROV-CHANNEL-001": {
				"account_command_stream_consumed": "PASS",
				"video_result_stream_published":   "PASS",
				"registry_partition_key_enforced": "PASS",
				"operation_replay_is_idempotent":  "PASS",
				"successful_message_acknowledged": "PASS",
			},
			"REQ-CONTRACT-PROV-IDENTITY-MAP-001": {
				"registry_device_id_preserved":  "PASS",
				"video_cloud_devid_preserved":   "PASS",
				"organization_id_preserved":     "PASS",
				"activation_uses_cloud_subject": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-PROV-SERVICE-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/httpapi", GoTest: "TestActivateEmbedsCanonicalServiceOptionsInCameraToken"},
			{Package: "./internal/workflow", GoTest: "TestUploadClipRequiresVideoStorageAndRejectsOtherServiceOptions"},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-PROV-SERVICE-OPTIONS-001": {
				"canonical_options_embedded_in_token": "PASS",
				"granted_service_is_allowed":          "PASS",
				"missing_service_is_rejected":         "PASS",
				"unrelated_service_is_rejected":       "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-PROV-ACTIVATION-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/httpapi", GoTest: "TestActivateEmbedsCanonicalServiceOptionsInCameraToken"},
			{Package: "./internal/httpapi", GoTest: "TestCanonicalDeviceLifecycleInfoConfigAndEvents"},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-PROV-ACTIVATION-001": {
				"mapped_device_subject_activated": "PASS",
				"factory_entitlements_preserved":  "PASS",
				"activity_id_returned":            "PASS",
				"active_lifecycle_observed":       "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-PROV-ACTIVATION-001": {
				"activate_video_device":     "PASS",
				"wait_for_device_lifecycle": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-PROV-PROJECTION-001", Repository: "rtk_account_manager", Package: "./internal/store", GoTest: "TestProjectDeviceProvisioningAndOnlineRules",
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-PROV-PROJECTION-001": {
				"provisioning_metadata_merged":    "PASS",
				"unrelated_metadata_preserved":    "PASS",
				"provision_success_not_online":    "PASS",
				"last_error_cleared_on_success":   "PASS",
				"online_event_controls_readiness": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-PROV-CLAIM-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationClaimResolveEndpoint"},
			{Package: "./internal/api", GoTest: "TestIntegrationProvisioningEndpoints"},
			{Package: "./internal/api", GoTest: "TestIntegrationPlatformAdminDeviceItemProfileLifecycle"},
			{Package: "./internal/store", GoTest: "TestCreateOrGetDeviceOperationIsIdempotent"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-CLAIM-RESOLUTION-001": {
				"opaque_claim_token_resolved":      "PASS",
				"account_policy_decides_ownership": "PASS",
				"raw_claim_fields_rejected":        "PASS",
				"already_claimed_replay_rejected":  "PASS",
				"cross_tenant_claim_rejected":      "PASS",
			},
			"REQ-AM-SERVICE-ENTITLEMENT-BOUNDARY-001": {
				"category_not_used_as_service_acl": "PASS",
				"service_options_explicit":         "PASS",
				"service_options_preserved":        "PASS",
				"unsupported_option_rejected":      "PASS",
			},
			"REQ-AM-DEVICE-OWNERSHIP-001": {
				"registry_uuid_is_owner_record":   "PASS",
				"active_membership_required":      "PASS",
				"external_identity_not_owner_key": "PASS",
				"same_operation_reused":           "PASS",
				"conflicting_operation_rejected":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-PROV-UNPROVISION-001", Repository: "rtk_account_manager", Package: "./internal/api", GoTest: "TestIntegrationDeviceUserUnprovisionWorkflow",
		Assertions: map[string]map[string]string{
			"REQ-AM-USER-UNPROVISION-001": {
				"active_member_required":           "PASS",
				"binding_and_audit_atomic":         "PASS",
				"durable_outbox_created":           "PASS",
				"old_device_access_rejected":       "PASS",
				"consumed_claim_replay_rejected":   "PASS",
				"factory_identity_preserved":       "PASS",
				"fresh_claim_creates_new_registry": "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-AM-USER-UNPROVISION-001": {
				"resolve_owned_device":          "PASS",
				"release_account_binding":       "PASS",
				"reject_released_device_access": "PASS",
				"reject_consumed_claim_replay":  "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-PROV-OVERRIDE-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationAdminDeviceUnprovisionOverride"},
			{Package: "./internal/api", GoTest: "TestIntegrationAdminDeviceClaimOverrideWorkflow"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-ADMIN-UNPROVISION-001": {
				"platform_admin_required":      "PASS",
				"reason_and_evidence_required": "PASS",
				"operator_actor_recorded":      "PASS",
				"redacted_audit_recorded":      "PASS",
				"override_outbox_marked":       "PASS",
			},
			"REQ-AM-CLAIM-TRANSFER-001": {
				"platform_admin_required":         "PASS",
				"target_and_evidence_required":    "PASS",
				"ownership_moved_to_target":       "PASS",
				"transferred_claim_replay_denied": "PASS",
				"before_after_audit_recorded":     "PASS",
			},
			"REQ-AM-CLAIM-RECLAIM-001": {
				"implicit_reclaim_rejected":    "PASS",
				"operator_evidence_required":   "PASS",
				"reclaim_target_explicit":      "PASS",
				"reclaim_audit_recorded":       "PASS",
				"raw_claim_token_not_returned": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-PROV-LIFECYCLE-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationProvisioningEndpoints"},
			{Package: "./internal/api", GoTest: "TestIntegrationInternalDeviceProvisioningResult"},
			{Package: "./internal/api", GoTest: "TestIntegrationDeactivateEndpointUsesProjectedVideoMetadata"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-LIFECYCLE-OPERATION-001": {
				"operation_and_outbox_atomic":        "PASS",
				"pending_metadata_not_success":       "PASS",
				"idempotent_replay_reuses_operation": "PASS",
				"conflicting_replay_rejected":        "PASS",
				"activation_projection_observed":     "PASS",
				"activation_does_not_invent_online":  "PASS",
				"deactivation_uses_projected_id":     "PASS",
				"terminal_failure_attributed":        "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-AM-LIFECYCLE-001": {
				"persist_provisioning_request":    "PASS",
				"observe_activation_projection":   "PASS",
				"request_product_deactivation":    "PASS",
				"observe_deactivation_projection": "PASS",
			},
		},
	},
	{
		TestID: "INT-AM-PROV-READINESS-001", Repository: "rtk_account_manager",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/api", GoTest: "TestIntegrationProvisioningStateReturnsRegistryOnlyReadiness"},
			{Package: "./internal/api", GoTest: "TestIntegrationProvisioningEndpoints"},
			{Package: "./internal/api", GoTest: "TestReadinessFromProjectionStates"},
			{Package: "./internal/store", GoTest: "TestProjectDeviceProvisioningAndOnlineRules"},
		},
		Assertions: map[string]map[string]string{
			"REQ-AM-READINESS-PROJECTION-001": {
				"active_member_read_allowed":        "PASS",
				"cross_tenant_read_not_disclosed":   "PASS",
				"registry_only_sources_returned":    "PASS",
				"external_credentials_not_invented": "PASS",
				"missing_device_rejected":           "PASS",
			},
			"REQ-AM-READINESS-STATES-001": {
				"registered_state_derived":        "PASS",
				"activation_pending_derived":      "PASS",
				"activation_failure_attributed":   "PASS",
				"transport_pending_distinct":      "PASS",
				"online_requires_transport_event": "PASS",
				"deactivation_states_derived":     "PASS",
				"disabled_not_deactivated":        "PASS",
			},
		},
	},
}, deviceContractQualificationSpecs...)

type goTestJSONEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

type authorizationQualificationResult struct {
	TestID      string `json:"test_id"`
	Selector    string `json:"selector"`
	Status      string `json:"status"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	DurationMS  int64  `json:"duration_ms"`
}

func runAuthorizationQualification(workspace, outputDir, runID string) error {
	return runAuthorizationQualificationWithSpecs(workspace, outputDir, runID, authorizationQualificationSpecs)
}

func runAuthorizationQualificationWithSpecs(workspace, outputDir, runID string, specs []authorizationQualificationSpec) error {
	if strings.TrimSpace(runID) == "" {
		runID = time.Now().UTC().Format("20060102T150405Z") + "-account-authz"
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	caseByID := map[string]testCatalogCase{}
	for _, tc := range catalog.Cases {
		caseByID[tc.ID] = tc
	}
	results := make([]authorizationQualificationResult, 0, len(specs))
	for _, spec := range specs {
		tc, ok := caseByID[spec.TestID]
		if !ok || tc.Status != "active" || tc.Layer != "integration" {
			return fmt.Errorf("authorization qualification case %s is missing or is not an active integration case", spec.TestID)
		}
		targets, err := authorizationQualificationTargets(spec)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.TestID, err)
		}
		selector := authorizationQualificationSelector(spec, targets)
		if tc.Selector != selector {
			return fmt.Errorf("authorization qualification case %s selector=%q, want %q", spec.TestID, tc.Selector, selector)
		}
		if err := validateAuthorizationQualificationAssertions(tc, spec); err != nil {
			return err
		}
		started := time.Now().UTC()
		for _, target := range targets {
			if len(target.Command) > 0 {
				for _, setup := range target.SetupCommands {
					if len(setup) == 0 {
						return fmt.Errorf("%s target %s has an empty setup command", spec.TestID, target.Label)
					}
					setupCommand := exec.Command(setup[0], setup[1:]...)
					setupCommand.Dir = filepath.Join(workspace, "repos", spec.Repository, target.WorkingDir)
					output, commandErr := setupCommand.CombinedOutput()
					if commandErr != nil {
						return fmt.Errorf("%s target %s setup failed: %w\n%s", spec.TestID, target.Label, commandErr, output)
					}
				}
				command := exec.Command(target.Command[0], target.Command[1:]...)
				command.Dir = filepath.Join(workspace, "repos", spec.Repository, target.WorkingDir)
				output, commandErr := command.CombinedOutput()
				if commandErr != nil {
					return fmt.Errorf("%s target %s failed: %w\n%s", spec.TestID, target.Label, commandErr, output)
				}
				for _, expected := range target.OutputContains {
					if !bytes.Contains(output, []byte(expected)) {
						return fmt.Errorf("%s target %s output is missing %q", spec.TestID, target.Label, expected)
					}
				}
				continue
			}
			command := exec.Command("go", "test", "-json", "-count=1", target.Package, "-run", "^"+target.GoTest+"$")
			command.Dir = filepath.Join(workspace, "repos", spec.Repository)
			command.Env = append(os.Environ(), "GOWORK=off")
			output, commandErr := command.CombinedOutput()
			if commandErr != nil {
				return fmt.Errorf("%s target %s#%s failed: %w\n%s", spec.TestID, target.Package, target.GoTest, commandErr, output)
			}
			passed, skipped, parseErr := qualificationGoTestStatus(output, target.GoTest)
			if parseErr != nil {
				return fmt.Errorf("%s target %s#%s result: %w", spec.TestID, target.Package, target.GoTest, parseErr)
			}
			if skipped || !passed {
				return fmt.Errorf("%s target %s#%s did not execute to PASS (skipped=%t)", spec.TestID, target.Package, target.GoTest, skipped)
			}
		}
		completed := time.Now().UTC()
		results = append(results, authorizationQualificationResult{
			TestID: spec.TestID, Selector: selector, Status: "PASS",
			StartedAt: started.Format(time.RFC3339), CompletedAt: completed.Format(time.RFC3339),
			DurationMS: completed.Sub(started).Milliseconds(),
		})
	}
	resultsPath := filepath.Join(outputDir, "results.json")
	if err := writeJSON(resultsPath, map[string]any{
		"schema_version": "rtk-account-authorization-qualification/v1",
		"run_id":         runID,
		"status":         "PASS",
		"cases":          results,
	}); err != nil {
		return err
	}
	junitPath := filepath.Join(outputDir, "junit.xml")
	if err := os.WriteFile(junitPath, []byte(renderAuthorizationQualificationJUnit(results)), 0o644); err != nil {
		return err
	}
	refs, err := qualificationEvidenceRefs(outputDir, resultsPath, junitPath)
	if err != nil {
		return err
	}
	workspaceCommit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	specCommit, err := currentCanonicalSpecCommit(workspace)
	if err != nil {
		return err
	}
	requirements := catalogRequirementIndex(catalog)
	features := catalogFeatureByRequirement(catalog)
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		return err
	}
	workflows := map[string]specWorkflow{}
	for _, workflow := range inventory.Workflows {
		workflows[workflow.ID] = workflow
	}
	resultByID := map[string]authorizationQualificationResult{}
	for _, result := range results {
		resultByID[result.TestID] = result
	}
	manifest := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV3,
		RunID:         runID,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		SpecCommit:    specCommit,
	}
	for _, spec := range specs {
		result := resultByID[spec.TestID]
		assertions := make([]featureRequirementAssertion, 0, len(spec.Assertions))
		workflowAssertions := make([]featureWorkflowAssertion, 0, len(spec.Workflows))
		requirementIDs := make([]string, 0, len(spec.Assertions))
		for requirementID := range spec.Assertions {
			requirementIDs = append(requirementIDs, requirementID)
		}
		sort.Strings(requirementIDs)
		for _, requirementID := range requirementIDs {
			requirement, ok := requirements[requirementID]
			if !ok {
				return fmt.Errorf("%s references unknown requirement %s", spec.TestID, requirementID)
			}
			assertions = append(assertions, featureRequirementAssertion{
				RequirementID: requirementID,
				Revision:      requirement.Revision,
				SpecSource:    requirement.SpecSource,
				Status:        "PASS",
				Assessment:    "explicit targeted integration assertions passed",
				Assertions:    spec.Assertions[requirementID],
				Evidence:      refs,
			})
		}
		workflowIDs := make([]string, 0, len(spec.Workflows))
		for workflowID := range spec.Workflows {
			workflowIDs = append(workflowIDs, workflowID)
		}
		sort.Strings(workflowIDs)
		for _, workflowID := range workflowIDs {
			workflow, ok := workflows[workflowID]
			if !ok {
				return fmt.Errorf("%s references unknown workflow %s", spec.TestID, workflowID)
			}
			assertion, err := buildWorkflowAssertion(workflow, spec.Workflows[workflowID])
			if err != nil {
				return fmt.Errorf("%s: %w", spec.TestID, err)
			}
			workflowAssertions = append(workflowAssertions, assertion)
		}
		var caseFeature testCatalogFeature
		for _, requirementID := range requirementIDs {
			feature, ok := features[requirementID]
			if !ok {
				return fmt.Errorf("%s requirement %s has no canonical feature", spec.TestID, requirementID)
			}
			if caseFeature.ID != "" && caseFeature.ID != feature.ID {
				return fmt.Errorf("%s spans features %s and %s", spec.TestID, caseFeature.ID, feature.ID)
			}
			caseFeature = feature
		}
		commits, err := currentFeatureCommits(workspace, caseFeature)
		if err != nil {
			return err
		}
		manifest.Cases = append(manifest.Cases, featureCaseEvidenceV2{
			TestID: spec.TestID, Status: "PASS", Assessment: "targeted integration test executed and passed",
			Environment: "ci", StartedAt: result.StartedAt, CompletedAt: result.CompletedAt,
			WorkspaceCommit: strings.TrimSpace(workspaceCommit), Commits: commits, Requirements: assertions, Workflows: workflowAssertions,
		})
	}
	return writeJSON(filepath.Join(outputDir, "feature-evidence.json"), manifest)
}

func authorizationQualificationTargets(spec authorizationQualificationSpec) ([]authorizationQualificationTarget, error) {
	if len(spec.Targets) > 0 {
		for _, target := range spec.Targets {
			hasGoTest := strings.TrimSpace(target.Package) != "" && strings.TrimSpace(target.GoTest) != ""
			hasCommand := len(target.Command) > 0 && strings.TrimSpace(target.Label) != ""
			if hasGoTest == hasCommand {
				return nil, fmt.Errorf("qualification target requires package and Go test")
			}
		}
		return spec.Targets, nil
	}
	if strings.TrimSpace(spec.Package) == "" || strings.TrimSpace(spec.GoTest) == "" {
		return nil, fmt.Errorf("qualification spec requires a target")
	}
	return []authorizationQualificationTarget{{Package: spec.Package, GoTest: spec.GoTest}}, nil
}

func authorizationQualificationSelector(spec authorizationQualificationSpec, targets []authorizationQualificationTarget) string {
	if len(spec.Targets) == 0 && len(targets) == 1 {
		return targets[0].GoTest
	}
	parts := make([]string, 0, len(targets))
	for _, target := range targets {
		if len(target.Command) > 0 {
			parts = append(parts, target.Label)
			continue
		}
		parts = append(parts, target.Package+"#"+target.GoTest)
	}
	return strings.Join(parts, ",")
}

func validateAuthorizationQualificationAssertions(tc testCatalogCase, spec authorizationQualificationSpec) error {
	mapped := map[string]bool{}
	for _, requirementID := range tc.Verifies {
		mapped[requirementID] = true
		if len(spec.Assertions[requirementID]) == 0 {
			return fmt.Errorf("%s has no explicit assertions for %s", tc.ID, requirementID)
		}
	}
	for requirementID := range spec.Assertions {
		if !mapped[requirementID] {
			return fmt.Errorf("%s emits an assertion for unmapped requirement %s", tc.ID, requirementID)
		}
	}
	return nil
}

func qualificationGoTestStatus(output []byte, testName string) (passed, skipped bool, err error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		var event goTestJSONEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Test != testName {
			continue
		}
		switch event.Action {
		case "pass":
			passed = true
		case "skip":
			skipped = true
		}
	}
	if err := scanner.Err(); err != nil {
		return false, false, err
	}
	if !passed && !skipped {
		return false, false, fmt.Errorf("go test JSON has no terminal event for %s", testName)
	}
	return passed, skipped, nil
}

func qualificationEvidenceRefs(outputDir string, paths ...string) ([]featureCoverageEvidenceFile, error) {
	refs := make([]featureCoverageEvidenceFile, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		refs = append(refs, featureCoverageEvidenceFile{
			Path: filepath.ToSlash(filepath.Base(path)), SHA256: hex.EncodeToString(sum[:]), Type: featureEvidenceType(path),
		})
	}
	return refs, nil
}

func renderAuthorizationQualificationJUnit(results []authorizationQualificationResult) string {
	var totalMS int64
	var cases []string
	for _, result := range results {
		totalMS += result.DurationMS
		cases = append(cases, fmt.Sprintf(
			`  <testcase classname="account-manager.authorization" name="%s" time="%.3f"/>`,
			xmlEscape(result.TestID+" "+result.Selector), float64(result.DurationMS)/1000,
		))
	}
	return fmt.Sprintf(
		"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuite name=\"account-authorization-qualification\" tests=\"%d\" failures=\"0\" skipped=\"0\" time=\"%.3f\">\n%s\n</testsuite>\n",
		len(results), float64(totalMS)/1000, strings.Join(cases, "\n"),
	)
}

func xmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(value)
}
