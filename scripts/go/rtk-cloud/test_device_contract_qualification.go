package main

var deviceContractQualificationSpecs = []authorizationQualificationSpec{
	{
		TestID: "INT-VC-SHADOW-CONTRACT-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/deviceshadow", GoTest: "TestServiceUpdateMergesDesiredAndReportedAndComputesDelta"},
			{Package: "./internal/deviceshadow", GoTest: "TestServiceUpdateDeletesNullFieldsAndTreatsArraysAsAtomic"},
			{Package: "./internal/deviceshadow", GoTest: "TestServiceDeleteIncrementsVersionAndRecreateDoesNotReset"},
			{Package: "./internal/deviceshadow", GoTest: "TestServiceConcurrentVersionedUpdateAllowsExactlyOneWriter"},
			{Package: "./internal/deviceshadow", GoTest: "TestServiceConcurrentUnversionedPatchesDoNotLoseFields"},
			{Package: "./internal/deviceshadow", GoTest: "TestAWSShadowProtocolSerializers"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceShadowAWSRoutesPaginationAndLegacyRemoval"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceShadowHTTPNamedListDeleteAndVersionConflict"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceShadowHTTPUsesSharedAWSUpdateValidationMessages"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceShadowAWSGoServiceSDKCustomEndpoint"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceShadowHTTPAcceptsAWSServiceSDKSignature"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceShadowHTTPRejectsInvalidSigV4ScopeFreshnessAndPayload"},
			{Package: "./internal/mqtt", GoTest: "TestAdapterReceiveShadowUpdatePublishesAcceptedDocumentsAndDelta"},
			{Package: "./internal/mqtt", GoTest: "TestAdapterReceiveShadowNamedGetAndDelete"},
			{Package: "./internal/rediscache", GoTest: "TestDeviceShadowStoreOutboxSerializesAcrossWorkers"},
			{Package: "./internal/rediscache", GoTest: "TestDeviceShadowStoreOutboxRetriesCommittedMutation"},
			{Package: "./internal/rediscache", GoTest: "TestDeviceShadowStoreOutboxSurvivesWorkerRestart"},
			{Package: "./internal/rediscache", GoTest: "TestDeviceShadowStoreOutboxDeadLettersAndExportsMetrics"},
			{Package: "./internal/rediscache", GoTest: "TestDeviceShadowDeleteCommitsOutboxAndLifecycleAuditTogether"},
		},
		Assertions: map[string]map[string]string{
			"REQ-SHADOW-COMPATIBILITY-001": {
				"aws_document_model_preserved": "PASS",
				"legacy_routes_not_public":     "PASS",
			},
			"REQ-SHADOW-IDENTITY-001": {
				"named_and_unnamed_isolated": "PASS",
				"named_pagination_enforced":  "PASS",
			},
			"REQ-SHADOW-HTTP-API-001": {
				"aws_routes_execute":      "PASS",
				"sigv4_sdk_interoperates": "PASS",
			},
			"REQ-SHADOW-MQTT-API-001": {
				"mqtt_update_documents_delta": "PASS",
				"mqtt_named_get_delete":       "PASS",
			},
			"REQ-SHADOW-MUTATION-001": {
				"merge_and_delta_atomic":     "PASS",
				"stale_version_rejected":     "PASS",
				"concurrent_order_preserved": "PASS",
			},
			"REQ-SHADOW-RESPONSE-001": {
				"accepted_documents_exact": "PASS",
				"rejected_documents_exact": "PASS",
			},
			"REQ-SHADOW-VALIDATION-001": {
				"http_mqtt_validator_shared": "PASS",
				"aws_error_mapping_stable":   "PASS",
			},
			"REQ-SHADOW-DELETE-001": {
				"delete_tombstone_atomic":     "PASS",
				"recreate_version_continuity": "PASS",
				"private_audit_not_public":    "PASS",
			},
			"REQ-SHADOW-NOTIFICATION-001": {
				"mutation_outbox_atomic":        "PASS",
				"notification_order_serialized": "PASS",
			},
			"REQ-SHADOW-CLIENT-001": {
				"conflict_read_reconcile_supported": "PASS",
				"unsafe_patch_retry_not_implicit":   "PASS",
			},
			"REQ-SHADOW-CONFORMANCE-HTTP-001": {
				"aws_document_differential_covered": "PASS",
				"http_errors_and_sigv4_covered":     "PASS",
				"named_pagination_and_tombstone":    "PASS",
			},
			"REQ-SHADOW-CONFORMANCE-DURABILITY-001": {
				"concurrent_writers_covered":     "PASS",
				"redis_retry_and_replay_covered": "PASS",
				"delivery_order_and_deadletter":  "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-HOME-SHADOW-001": {
				"update_shadow":         "PASS",
				"read_converged_shadow": "PASS",
				"reject_stale_version":  "PASS",
			},
			"WF-HOME-SHADOW-DELETE-001": {
				"create_shadow":             "PASS",
				"delete_shadow":             "PASS",
				"recreate_shadow":           "PASS",
				"verify_version_continuity": "PASS",
				"cleanup_recreated_shadow":  "PASS",
			},
			"WF-HOME-SHADOW-NOTIFICATION-001": {
				"commit_first_mutation":                         "PASS",
				"commit_second_mutation":                        "PASS",
				"verify_committed_state_and_notification_order": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-TRANSPORT-CONTRACT-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionKeepsReplacementOwnerOnDuplicateReconnect"},
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionFailsClearlyWithoutActiveOwner"},
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionPublishesTowardRemoteOwner"},
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionForwardsAtMostOnceWhenOwnerChanges"},
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionRoutesLegacyInboundMessages"},
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionRoutesSnapshotBinaryPayload"},
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionReportsCurrentOwnerTransportCapabilities"},
			{Package: "./internal/devicebus", GoTest: "TestSessionLifecycleOwnerConnectedRejectsLowerPriorityTransport"},
			{Package: "./internal/devicebus", GoTest: "TestTransportMuxSendsIdenticalOfferOnlyToCurrentOwnerWithoutFallback"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceSocketSession"},
			{Package: "./internal/httpapi", GoTest: "TestNotifyCameraReturnsUnsupportedCapabilityContract"},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-TRANSPORT-SURFACES-001": {
				"websocket_owner_supported": "PASS",
				"mqtt_owner_supported":      "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-LIFECYCLE-001": {
				"transport_does_not_bind_account":  "PASS",
				"entitlement_checked_before_owner": "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-OWNER-001": {
				"single_owner_enforced":             "PASS",
				"same_transport_reconnect_replaces": "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-PRIORITY-001": {
				"websocket_replaces_mqtt":       "PASS",
				"mqtt_cannot_replace_websocket": "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-ROUTING-001": {
				"current_owner_only":        "PASS",
				"missing_owner_explicit":    "PASS",
				"owner_change_at_most_once": "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-WEBSOCKET-001": {
				"bearer_upgrade_session":    "PASS",
				"legacy_events_normalized":  "PASS",
				"snapshot_binary_follow_on": "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-WEBRTC-001": {
				"logical_offer_identical":     "PASS",
				"current_owner_only_delivery": "PASS",
				"no_transport_fallback":       "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-MQTT-001": {
				"tenant_relative_topics":    "PASS",
				"physical_namespace_hidden": "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-CAPABILITY-001": {
				"capabilities_explicit":  "PASS",
				"native_binary_distinct": "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-UNSUPPORTED-001": {
				"unsupported_fails_explicitly": "PASS",
				"non_owner_not_selected":       "PASS",
				"error_fields_preserved":       "PASS",
			},
			"REQ-CONTRACT-TRANSPORT-COMMAND-001": {
				"device_payload_preserved": "PASS",
				"http_response_separate":   "PASS",
			},
		},
	},
	{
		TestID: "INT-SDK-TRANSPORT-CONTRACT-001", Repository: "rtk_cloud_client",
		Targets: []authorizationQualificationTarget{
			{
				WorkingDir:    "packages/javascript",
				SetupCommands: [][]string{{"npm", "run", "build"}},
				Command: []string{
					"node", "--test",
					"--test-name-pattern=uses WebSocket adapter boundary for session, events, and snapshot framing",
					"dist/test/package.test.js",
				},
				Label: "uses WebSocket adapter boundary for session, events, and snapshot framing",
				OutputContains: []string{
					"uses WebSocket adapter boundary for session, events, and snapshot framing",
					"fail 0",
				},
			},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-TRANSPORT-CLIENT-001": {
				"adapter_boundary_preserved":        "PASS",
				"transport_snapshot_paths_distinct": "PASS",
				"upgrade_auth_separate_from_device": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-SNAPSHOT-CONTRACT-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionRoutesSnapshotBinaryPayload"},
			{Package: "./internal/transportregression", GoTest: "TestSharedTransportRegressionNormalizesEquivalentSnapshotAcrossWebsocketAndMQTT"},
			{Package: "./internal/mqtt", GoTest: "TestApplicationSideSnapshotIngestionSmokeRoutesMQTTBase64ToWorkflowSideEffects"},
			{Package: "./internal/httpapi", GoTest: "TestDeviceSocketLegacyEventAndSnapshotCompatibility"},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-SNAPSHOT-MODEL-001": {
				"transport_models_remain_distinct": "PASS",
				"logical_snapshot_equivalent":      "PASS",
			},
			"REQ-CONTRACT-SNAPSHOT-WEBSOCKET-001": {
				"metadata_precedes_binary":  "PASS",
				"binary_not_base64_wrapped": "PASS",
			},
			"REQ-CONTRACT-SNAPSHOT-MQTT-001": {
				"single_json_envelope":      "PASS",
				"image_body_base64_encoded": "PASS",
			},
			"REQ-CONTRACT-SNAPSHOT-CAPABILITY-001": {
				"snapshot_capability_shared":   "PASS",
				"native_binary_websocket_only": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-MEDIA-CONTRACT-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/httpapi", GoTest: "TestDirectClipUploadAPI"},
			{Package: "./internal/httpapi", GoTest: "TestRetiredMultipartClipIsNotBuffered"},
			{Package: "./internal/httpapi", GoTest: "TestUploadClipGetInfoDownloadAndDelete"},
			{Package: "./internal/httpapi", GoTest: "TestClipDownloadSupportsRangeRequests"},
			{Package: "./internal/httpapi", GoTest: "TestSnapshotAndThumbnailDownloadAcceptQueryTokens"},
			{Package: "./internal/httpapi", GoTest: "TestEncryptedClipDownloadCompatibility"},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-MEDIA-ROUTES-001": {
				"direct_routes_active":        "PASS",
				"compatibility_routes_stable": "PASS",
				"retired_clip_multipart_gone": "PASS",
			},
			"REQ-CONTRACT-MEDIA-DIRECT-UPLOAD-001": {
				"encrypted_object_described":    "PASS",
				"head_verification_required":    "PASS",
				"ready_only_after_verification": "PASS",
			},
			"REQ-CONTRACT-MEDIA-TOKEN-001": {
				"query_token_media_only":   "PASS",
				"bearer_default_preserved": "PASS",
			},
			"REQ-CONTRACT-MEDIA-DOWNLOAD-001": {
				"scoped_roles_enforced":   "PASS",
				"content_types_preserved": "PASS",
				"valid_range_partial":     "PASS",
				"invalid_range_rejected":  "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-CONTRACT-MEDIA-UPLOAD-001": {
				"create_direct_upload":        "PASS",
				"upload_and_complete_objects": "PASS",
				"wait_until_ready":            "PASS",
				"verify_range_playback":       "PASS",
			},
		},
	},
	{
		TestID: "INT-SDK-MEDIA-CONTRACT-001", Repository: "rtk_cloud_client",
		Targets: []authorizationQualificationTarget{
			{
				WorkingDir:    "packages/javascript",
				SetupCommands: [][]string{{"npm", "run", "build"}},
				Command: []string{
					"node", "--test",
					"--test-name-pattern=implements media upload and download helpers with fixtures",
					"dist/test/package.test.js",
				},
				Label: "implements media upload and download helpers with fixtures",
				OutputContains: []string{
					"implements media upload and download helpers with fixtures",
					"fail 0",
				},
			},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-MEDIA-CLIENT-001": {
				"media_helpers_isolated":       "PASS",
				"snapshot_helper_isolated":     "PASS",
				"standard_json_auth_unchanged": "PASS",
			},
		},
	},
	{
		TestID: "INT-VC-STREAMING-CONTRACT-001", Repository: "rtk_video_cloud",
		Targets: []authorizationQualificationTarget{
			{Package: "./internal/httpapi", GoTest: "TestRequestWebRTCRoundTrip"},
			{Package: "./internal/httpapi", GoTest: "TestRequestWebRTCUsesAppTokenServiceOptionsClaim"},
			{Package: "./internal/httpapi", GoTest: "TestRequestWebRTCDeviceNotOnlineLogsStructuredContext"},
			{Package: "./internal/httpapi", GoTest: "TestRequestWebRTCCloseExpiredSessionReturnsGone"},
			{Package: "./internal/httpapi", GoTest: "TestRequestWebRTCICEPreflightReturnsServersWithoutSession"},
			{Package: "./internal/httpapi", GoTest: "TestRequestWebRTCICEServersFromRegistryTURN"},
			{Package: "./internal/httpapi", GoTest: "TestTURNRegistryRouterLifecycle"},
			{Package: "./internal/httpapi", GoTest: "TestTURNRegistryRouterRejectsInvalidNodeAuth"},
			{Package: "./internal/httpapi", GoTest: "TestTURNRegistryRouterFailureMetricsAndRedaction"},
			{Package: "./internal/devicebus", GoTest: "TestTransportMuxSendsIdenticalOfferOnlyToCurrentOwnerWithoutFallback"},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-STREAMING-SURFACE-001": {
				"webrtc_only_mode":      "PASS",
				"route_phases_separate": "PASS",
			},
			"REQ-CONTRACT-STREAMING-AUTH-001": {
				"app_scope_create":       "PASS",
				"device_scope_answer":    "PASS",
				"invalid_scope_rejected": "PASS",
			},
			"REQ-CONTRACT-STREAMING-ICE-001": {
				"ice_preflight_no_session": "PASS",
				"relay_policy_returned":    "PASS",
			},
			"REQ-CONTRACT-STREAMING-CREATE-001": {
				"complete_offer_required":  "PASS",
				"opaque_session_returned":  "PASS",
				"offer_delivered_to_owner": "PASS",
			},
			"REQ-CONTRACT-TURN-REGISTRY-001": {
				"signed_registration_required": "PASS",
				"heartbeat_and_discovery":      "PASS",
				"deactivation_removes_active":  "PASS",
				"secrets_redacted":             "PASS",
			},
			"REQ-CONTRACT-STREAMING-WAIT-001": {
				"complete_answer_returned":    "PASS",
				"timeout_not_partial_success": "PASS",
			},
			"REQ-CONTRACT-STREAMING-ANSWER-001": {
				"device_session_answer_bound": "PASS",
				"non_trickle_sdp_preserved":   "PASS",
			},
			"REQ-CONTRACT-STREAMING-CLOSE-001": {
				"active_session_closed": "PASS",
				"expired_session_gone":  "PASS",
			},
			"REQ-CONTRACT-STREAMING-LIFECYCLE-001": {
				"preflight_before_offer": "PASS",
				"offer_answer_ordered":   "PASS",
				"readiness_before_close": "PASS",
			},
			"REQ-CONTRACT-STREAMING-ERROR-001": {
				"offline_explicit_failure": "PASS",
				"expired_explicit_failure": "PASS",
				"auth_failure_stable":      "PASS",
			},
		},
		Workflows: map[string]map[string]string{
			"WF-CONTRACT-STREAMING-001": {
				"preflight_ice":        "PASS",
				"create_session":       "PASS",
				"submit_device_answer": "PASS",
				"wait_for_answer":      "PASS",
				"close_session":        "PASS",
			},
			"WF-CONTRACT-TURN-REGISTRY-001": {
				"register_node":             "PASS",
				"heartbeat_node":            "PASS",
				"discover_active_node":      "PASS",
				"deactivate_node":           "PASS",
				"verify_node_is_not_active": "PASS",
			},
		},
	},
	{
		TestID: "INT-SDK-STREAMING-CONTRACT-001", Repository: "rtk_cloud_client",
		Targets: []authorizationQualificationTarget{
			{
				WorkingDir:    "packages/javascript",
				SetupCommands: [][]string{{"npm", "run", "build"}},
				Command: []string{
					"node", "--test",
					"--test-name-pattern=implements token and HTTP device/config subset with fixtures",
					"dist/test/package.test.js",
				},
				Label: "implements token and HTTP device/config subset with fixtures",
				OutputContains: []string{
					"implements token and HTTP device/config subset with fixtures",
					"fail 0",
				},
			},
		},
		Assertions: map[string]map[string]string{
			"REQ-CONTRACT-STREAMING-CLIENT-001": {
				"opaque_session_preserved": "PASS",
				"sdp_payload_preserved":    "PASS",
				"ice_payload_preserved":    "PASS",
				"kvs_not_assumed":          "PASS",
			},
		},
	},
}
