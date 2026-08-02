package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQualifyLiveOnboardingFactsRequiresOneCompleteDeviceChain(t *testing.T) {
	bind := liveOnboardingBindEvidence{Overall: "pass"}
	bind.Provisioning.Checked = 1
	bind.Provisioning.Ready = 1
	bind.Provisioning.LastStates = map[string]liveOnboardingProvisionState{
		"device-1": {
			DeviceID: "device-1", AccountDeviceID: "account-device-1", BindStatus: "provisioned",
			ReadinessState: "transport_pending", ProductState: "activated", OperationStatus: "succeeded", ActivationState: "activated",
		},
	}
	mqtt := liveOnboardingMQTTEvidence{Status: "PASS", Overall: "pass", Devices: []liveOnboardingMQTTDevice{{
		DeviceID: "device-1", MQTTStatus: "PASS", TelemetryState: "PASS", CommandState: "PASS",
		Trace: []liveOnboardingMQTTTraceStep{
			{Phase: "app_token", Actor: "app_controller", Action: "request_token", Status: "PASS", Detail: "scope=app devid=matched"},
			{Phase: "device_info", Actor: "app_controller", Action: "get_device_info", Status: "PASS", Detail: "app token subject matched device"},
			{Phase: "device_token", Actor: "device_client", Action: "request_token", Status: "PASS", Detail: "scope=device"},
			{Phase: "mqtt_connect", Actor: "device_client", Action: "mqtt_connect", Status: "PASS"},
			{Phase: "mqtt_connect", Actor: "app_observer", Action: "mqtt_connect", Status: "PASS"},
			{Phase: "mqtt_connect", Actor: "app_controller", Action: "mqtt_connect", Status: "PASS"},
			{Phase: "telemetry", Actor: "device_client", Action: "publish", Status: "PASS", Data: "payload.status=online"},
			{Phase: "telemetry", Actor: "app_observer", Action: "receive", Status: "PASS", Data: "payload.status=online"},
			{Phase: "shadow_desired", Actor: "app_controller", Action: "publish", Status: "PASS"},
			{Phase: "shadow_delta", Actor: "device_client", Action: "receive", Status: "PASS"},
			{Phase: "shadow_reported", Actor: "device_client", Action: "publish", Status: "PASS"},
			{Phase: "shadow_reported", Actor: "app_observer", Action: "receive", Status: "PASS"},
		},
	}}}
	steps, assertions, err := qualifyLiveOnboardingFacts(bind, mqtt)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 7 || steps["issue_subject_bound_device_token"] != "PASS" ||
		assertions["verify_owner_command_roundtrip"]["owner_ack_receive"] != "PASS" {
		t.Fatalf("unexpected workflow evidence: steps=%v assertions=%v", steps, assertions)
	}
	outDir := t.TempDir()
	bindDir := filepath.Join(outDir, "data-setup", "bind-validation")
	mqttDir := filepath.Join(outDir, "home-mqtt")
	if err := os.MkdirAll(bindDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mqttDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(bindDir, "bulk-device-bind-validation-results.json"), bind); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(mqttDir, "results.json"), mqtt); err != nil {
		t.Fatal(err)
	}
	if err := writeLiveOnboardingWorkflowEvidence(outDir); err != nil {
		t.Fatal(err)
	}
	var generated struct {
		Workflow struct {
			WorkflowID string                       `json:"workflow_id"`
			Steps      map[string]string            `json:"steps"`
			Assertions map[string]map[string]string `json:"assertions"`
		} `json:"workflow"`
	}
	if err := readJSONFile(filepath.Join(outDir, "live-onboarding-workflow.json"), &generated); err != nil {
		t.Fatal(err)
	}
	if generated.Workflow.WorkflowID != liveOnboardingWorkflowID ||
		generated.Workflow.Assertions["observe_online_readiness"]["owner_online_receive"] != "PASS" {
		t.Fatalf("unexpected generated workflow: %+v", generated.Workflow)
	}
	if err := readJSONFile(filepath.Join(outDir, "provisioning-runtime-workflow.json"), &generated); err != nil {
		t.Fatal(err)
	}
	if generated.Workflow.WorkflowID != provisioningRuntimeWorkflowID ||
		generated.Workflow.Assertions["read_device_info"]["response_device_identity_matched"] != "PASS" {
		t.Fatalf("unexpected runtime workflow: %+v", generated.Workflow)
	}
	if err := readJSONFile(filepath.Join(outDir, "bulk-provisioning-workflow.json"), &generated); err != nil {
		t.Fatal(err)
	}
	if generated.Workflow.WorkflowID != "WF-PROV-BULK-001" ||
		generated.Workflow.Assertions["wait_for_provisioning"]["zero_pending_or_failed"] != "PASS" {
		t.Fatalf("unexpected bulk workflow: %+v", generated.Workflow)
	}
	brokenBulk := bind
	brokenBulk.Provisioning.LastStates = map[string]liveOnboardingProvisionState{}
	if _, _, err := qualifyBulkProvisioningFacts(brokenBulk); err == nil || !strings.Contains(err.Error(), "not complete") {
		t.Fatalf("bulk evidence without per-device state passed: %v", err)
	}

	mqtt.Devices[0].Trace[len(mqtt.Devices[0].Trace)-1].Status = "FAIL"
	if _, _, err := qualifyLiveOnboardingFacts(bind, mqtt); err == nil || !strings.Contains(err.Error(), "no single") {
		t.Fatalf("incomplete command roundtrip accepted: %v", err)
	}
}

func TestBuildWorkflowAssertionRejectsNonPassDetailedAssertion(t *testing.T) {
	workflow := specWorkflow{ID: "WF-TEST-001", Steps: []specWorkflowStep{{ID: "connect", OperationRef: "SPEC-OPS#connect"}}}
	_, err := buildWorkflowAssertionWithDetails(workflow, map[string]string{"connect": "PASS"}, map[string]map[string]string{
		"connect": {"mqtt_connected": "FAIL"},
	})
	if err == nil || !strings.Contains(err.Error(), "non-PASS assertion") {
		t.Fatalf("failed detailed assertion accepted: %v", err)
	}
	_, err = buildWorkflowAssertionWithDetails(workflow, map[string]string{"connect": "PASS"}, map[string]map[string]string{
		"connect": {"mqtt_connected": "PASS"},
		"unknown": {"invented_fact": "PASS"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Fatalf("assertions for an unknown step accepted: %v", err)
	}
}

func TestQualifyLiveRuntimeFactsRejectsIncompleteProductProof(t *testing.T) {
	if _, _, err := qualifyLiveRuntimeFacts(liveOnboardingMQTTEvidence{}); err == nil || !strings.Contains(err.Error(), "not PASS") {
		t.Fatalf("non-PASS runtime evidence accepted: %v", err)
	}
	statusOnly := liveOnboardingMQTTEvidence{Status: "PASS", Overall: "PASS", Devices: []liveOnboardingMQTTDevice{{
		DeviceID: "device-1", MQTTStatus: "FAIL", CommandState: "PASS",
	}}}
	if _, _, err := qualifyLiveRuntimeFacts(statusOnly); err == nil || !strings.Contains(err.Error(), "complete app token") {
		t.Fatalf("failed device runtime evidence accepted: %v", err)
	}
	statusOnly.Devices[0].MQTTStatus = "PASS"
	if _, _, err := qualifyLiveRuntimeFacts(statusOnly); err == nil || !strings.Contains(err.Error(), "complete app token") {
		t.Fatalf("status-only runtime evidence accepted: %v", err)
	}
}

func TestLiveOnboardingWorkflowEvidenceRejectsMissingOrFailedInputs(t *testing.T) {
	if err := writeLiveOnboardingWorkflowEvidence(""); err == nil || !strings.Contains(err.Error(), "requires --out-dir") {
		t.Fatalf("empty output directory error = %v", err)
	}
	outDir := t.TempDir()
	if err := writeLiveOnboardingWorkflowEvidence(outDir); err == nil || !strings.Contains(err.Error(), "bind evidence") {
		t.Fatalf("missing bind evidence error = %v", err)
	}
	bindDir := filepath.Join(outDir, "data-setup", "bind-validation")
	if err := os.MkdirAll(bindDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bind := liveOnboardingBindEvidence{Overall: "pass"}
	bind.Provisioning.Checked = 1
	bind.Provisioning.Ready = 1
	if err := writeJSON(filepath.Join(bindDir, "bulk-device-bind-validation-results.json"), bind); err != nil {
		t.Fatal(err)
	}
	if err := writeLiveOnboardingWorkflowEvidence(outDir); err == nil || !strings.Contains(err.Error(), "MQTT evidence") {
		t.Fatalf("missing MQTT evidence error = %v", err)
	}
	if _, _, err := qualifyLiveOnboardingFacts(liveOnboardingBindEvidence{}, liveOnboardingMQTTEvidence{}); err == nil || !strings.Contains(err.Error(), "not fully ready") {
		t.Fatalf("failed bind facts accepted: %v", err)
	}
	if _, _, err := qualifyLiveOnboardingFacts(bind, liveOnboardingMQTTEvidence{}); err == nil || !strings.Contains(err.Error(), "not PASS") {
		t.Fatalf("failed MQTT facts accepted: %v", err)
	}
}
