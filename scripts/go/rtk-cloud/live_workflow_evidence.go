package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const liveOnboardingWorkflowID = "WF-LIVE-STG-ONBOARD-001"

type liveOnboardingBindEvidence struct {
	Overall      string `json:"overall"`
	Provisioning struct {
		Checked    int                                     `json:"checked"`
		Ready      int                                     `json:"ready"`
		Pending    int                                     `json:"pending"`
		Failed     int                                     `json:"failed"`
		LastStates map[string]liveOnboardingProvisionState `json:"last_states"`
	} `json:"provisioning"`
}

type liveOnboardingProvisionState struct {
	DeviceID        string `json:"device_id"`
	AccountDeviceID string `json:"account_device_id"`
	BindStatus      string `json:"bind_status"`
	ReadinessState  string `json:"readiness_state"`
	ProductState    string `json:"product_state"`
	OperationStatus string `json:"operation_status"`
	ActivationState string `json:"activation_status"`
}

type liveOnboardingMQTTEvidence struct {
	Status  string                     `json:"status"`
	Overall string                     `json:"overall"`
	Devices []liveOnboardingMQTTDevice `json:"devices"`
}

type liveOnboardingMQTTDevice struct {
	DeviceID       string                        `json:"device_id"`
	MQTTStatus     string                        `json:"mqtt_status"`
	TelemetryState string                        `json:"telemetry_status"`
	CommandState   string                        `json:"command_status"`
	Trace          []liveOnboardingMQTTTraceStep `json:"trace_chain"`
}

type liveOnboardingMQTTTraceStep struct {
	Phase  string `json:"phase"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Status string `json:"status"`
	Data   string `json:"data"`
	Detail string `json:"detail"`
}

func writeLiveOnboardingWorkflowEvidence(outDir string) error {
	if strings.TrimSpace(outDir) == "" {
		return errors.New("live onboarding workflow evidence requires --out-dir")
	}
	var bind liveOnboardingBindEvidence
	if err := readJSONFile(filepath.Join(outDir, "data-setup", "bind-validation", "bulk-device-bind-validation-results.json"), &bind); err != nil {
		return fmt.Errorf("read live onboarding bind evidence: %w", err)
	}
	var mqtt liveOnboardingMQTTEvidence
	if err := readJSONFile(filepath.Join(outDir, "home-mqtt", "results.json"), &mqtt); err != nil {
		return fmt.Errorf("read live onboarding MQTT evidence: %w", err)
	}
	steps, assertions, err := qualifyLiveOnboardingFacts(bind, mqtt)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"schema_version": "rtk-live-workflow-evidence/v1",
		"workflow": map[string]any{
			"workflow_id": liveOnboardingWorkflowID,
			"steps":       steps,
			"assertions":  assertions,
		},
	}
	if err := writeJSON(filepath.Join(outDir, "live-onboarding-workflow.json"), payload); err != nil {
		return err
	}
	runtimeSteps, runtimeAssertions, err := qualifyLiveRuntimeFacts(mqtt)
	if err != nil {
		return err
	}
	runtimePayload := map[string]any{
		"schema_version": "rtk-live-workflow-evidence/v1",
		"workflow": map[string]any{
			"workflow_id": provisioningRuntimeWorkflowID,
			"steps":       runtimeSteps,
			"assertions":  runtimeAssertions,
		},
	}
	if err := writeJSON(filepath.Join(outDir, "provisioning-runtime-workflow.json"), runtimePayload); err != nil {
		return err
	}
	bulkSteps, bulkAssertions, err := qualifyBulkProvisioningFacts(bind)
	if err != nil {
		return err
	}
	bulkPayload := map[string]any{
		"schema_version": "rtk-live-workflow-evidence/v1",
		"workflow": map[string]any{
			"workflow_id": "WF-PROV-BULK-001",
			"steps":       bulkSteps,
			"assertions":  bulkAssertions,
		},
	}
	return writeJSON(filepath.Join(outDir, "bulk-provisioning-workflow.json"), bulkPayload)
}

func qualifyBulkProvisioningFacts(bind liveOnboardingBindEvidence) (map[string]string, map[string]map[string]string, error) {
	if !strings.EqualFold(bind.Overall, "pass") || bind.Provisioning.Checked == 0 ||
		bind.Provisioning.Ready != bind.Provisioning.Checked || bind.Provisioning.Pending != 0 || bind.Provisioning.Failed != 0 ||
		len(bind.Provisioning.LastStates) != bind.Provisioning.Checked {
		return nil, nil, errors.New("bulk provisioning evidence is not complete")
	}
	for deviceID, state := range bind.Provisioning.LastStates {
		if state.DeviceID != deviceID || strings.TrimSpace(state.AccountDeviceID) == "" ||
			!strings.EqualFold(state.BindStatus, "provisioned") || !strings.EqualFold(state.OperationStatus, "succeeded") {
			return nil, nil, fmt.Errorf("bulk provisioning device %s is missing identity or successful operation evidence", deviceID)
		}
	}
	return map[string]string{
			"provision_registry_device": "PASS",
			"wait_for_provisioning":     "PASS",
		}, map[string]map[string]string{
			"provision_registry_device": {"every_assignment_has_account_device": "PASS", "every_operation_succeeded": "PASS"},
			"wait_for_provisioning":     {"checked_count_nonzero": "PASS", "all_operations_ready": "PASS", "zero_pending_or_failed": "PASS"},
		}, nil
}

func qualifyLiveRuntimeFacts(mqtt liveOnboardingMQTTEvidence) (map[string]string, map[string]map[string]string, error) {
	if !strings.EqualFold(mqtt.Status, "pass") || !strings.EqualFold(mqtt.Overall, "pass") {
		return nil, nil, errors.New("live runtime MQTT evidence is not PASS")
	}
	for _, device := range mqtt.Devices {
		if !strings.EqualFold(device.MQTTStatus, "pass") || !strings.EqualFold(device.CommandState, "pass") {
			continue
		}
		if !hasLiveTrace(device.Trace, "app_token", "app_controller", "request_token", "", "scope=app") ||
			!hasLiveTrace(device.Trace, "device_info", "app_controller", "get_device_info", "", "subject matched device") ||
			!hasLiveTrace(device.Trace, "shadow_desired", "app_controller", "publish", "", "") ||
			!hasLiveTrace(device.Trace, "shadow_delta", "device_client", "receive", "", "") {
			continue
		}
		return map[string]string{
				"issue_app_runtime_token": "PASS",
				"read_device_info":        "PASS",
				"deliver_owner_command":   "PASS",
			}, map[string]map[string]string{
				"issue_app_runtime_token": {"app_certificate_request_token": "PASS", "app_scope": "PASS", "device_subject_bound": "PASS"},
				"read_device_info":        {"app_token_authorized": "PASS", "response_device_identity_matched": "PASS"},
				"deliver_owner_command":   {"owner_publish_succeeded": "PASS", "device_receive_succeeded": "PASS"},
			}, nil
	}
	return nil, nil, errors.New("no run-scoped device has complete app token, device info, and owner command evidence")
}

func qualifyLiveOnboardingFacts(bind liveOnboardingBindEvidence, mqtt liveOnboardingMQTTEvidence) (map[string]string, map[string]map[string]string, error) {
	if !strings.EqualFold(bind.Overall, "pass") || bind.Provisioning.Checked == 0 ||
		bind.Provisioning.Ready != bind.Provisioning.Checked || bind.Provisioning.Pending != 0 || bind.Provisioning.Failed != 0 {
		return nil, nil, errors.New("live onboarding bind/provisioning evidence is not fully ready")
	}
	if !strings.EqualFold(mqtt.Status, "pass") || !strings.EqualFold(mqtt.Overall, "pass") {
		return nil, nil, errors.New("live onboarding MQTT evidence is not PASS")
	}
	for _, device := range mqtt.Devices {
		state, exists := bind.Provisioning.LastStates[device.DeviceID]
		if !exists || state.DeviceID != device.DeviceID || state.AccountDeviceID == "" {
			continue
		}
		if !strings.EqualFold(state.BindStatus, "provisioned") ||
			!strings.EqualFold(state.OperationStatus, "succeeded") ||
			!strings.EqualFold(state.ActivationState, "activated") ||
			!(strings.EqualFold(state.ReadinessState, "transport_pending") || strings.EqualFold(state.ReadinessState, "ready") || strings.EqualFold(state.ReadinessState, "online")) ||
			!(strings.EqualFold(state.ProductState, "activated") || strings.EqualFold(state.ProductState, "online")) {
			continue
		}
		if !strings.EqualFold(device.MQTTStatus, "pass") || !strings.EqualFold(device.TelemetryState, "pass") || !strings.EqualFold(device.CommandState, "pass") {
			continue
		}
		if !hasLiveTrace(device.Trace, "device_token", "device_client", "request_token", "", "scope=device") ||
			!hasLiveTrace(device.Trace, "mqtt_connect", "device_client", "mqtt_connect", "", "") ||
			!hasLiveTrace(device.Trace, "mqtt_connect", "app_observer", "mqtt_connect", "", "") ||
			!hasLiveTrace(device.Trace, "mqtt_connect", "app_controller", "mqtt_connect", "", "") ||
			!hasLiveTrace(device.Trace, "telemetry", "device_client", "publish", "payload.status=online", "") ||
			!hasLiveTrace(device.Trace, "telemetry", "app_observer", "receive", "payload.status=online", "") ||
			!hasLiveTrace(device.Trace, "shadow_desired", "app_controller", "publish", "", "") ||
			!hasLiveTrace(device.Trace, "shadow_delta", "device_client", "receive", "", "") ||
			!hasLiveTrace(device.Trace, "shadow_reported", "device_client", "publish", "", "") ||
			!hasLiveTrace(device.Trace, "shadow_reported", "app_observer", "receive", "", "") {
			continue
		}
		steps := map[string]string{
			"resolve_device_claim":             "PASS",
			"verify_account_video_activation":  "PASS",
			"issue_subject_bound_device_token": "PASS",
			"connect_device_transport":         "PASS",
			"connect_owner_transport":          "PASS",
			"observe_online_readiness":         "PASS",
			"verify_owner_command_roundtrip":   "PASS",
		}
		assertions := map[string]map[string]string{
			"resolve_device_claim":             {"account_device_id_present": "PASS", "account_to_video_identity_match": "PASS"},
			"verify_account_video_activation":  {"bind_provisioned": "PASS", "operation_succeeded": "PASS", "video_activation_activated": "PASS"},
			"issue_subject_bound_device_token": {"mtls_request_token": "PASS", "device_scope": "PASS"},
			"connect_device_transport":         {"device_mqtt_connect": "PASS"},
			"connect_owner_transport":          {"owner_observer_connect": "PASS", "owner_controller_connect": "PASS"},
			"observe_online_readiness":         {"device_online_publish": "PASS", "owner_online_receive": "PASS"},
			"verify_owner_command_roundtrip":   {"owner_command_publish": "PASS", "device_command_receive": "PASS", "device_ack_publish": "PASS", "owner_ack_receive": "PASS"},
		}
		return steps, assertions, nil
	}
	return nil, nil, errors.New("no single run-scoped device has complete claim, activation, token, online transport, and command evidence")
}

func hasLiveTrace(trace []liveOnboardingMQTTTraceStep, phase, actor, action, dataMarker, detailMarker string) bool {
	for _, step := range trace {
		if step.Phase == phase && step.Actor == actor && step.Action == action && strings.EqualFold(step.Status, "pass") &&
			(dataMarker == "" || strings.Contains(step.Data, dataMarker)) &&
			(detailMarker == "" || strings.Contains(step.Detail, detailMarker)) {
			return true
		}
	}
	return false
}
