package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type cloudValidationImportReport struct {
	RunID           string `json:"run_id"`
	Environment     string `json:"environment"`
	Platform        string `json:"platform"`
	Status          string `json:"status"`
	StartedAt       string `json:"started_at"`
	CompletedAt     string `json:"completed_at"`
	SDKCommit       string `json:"sdk_commit"`
	ContractsCommit string `json:"contracts_commit"`
	PlatformResult  *struct {
		Status  string `json:"status"`
		Results []struct {
			TestID        string   `json:"test_id"`
			ScenarioID    string   `json:"scenario_id"`
			Status        string   `json:"status"`
			CorrelationID string   `json:"correlation_id"`
			Evidence      []string `json:"evidence"`
		} `json:"results"`
	} `json:"platform_result"`
	Scenarios []struct {
		ID                    string   `json:"id"`
		TestID                string   `json:"test_id"`
		ExpectedCloudEvidence []string `json:"expected_cloud_evidence"`
	} `json:"scenarios"`
}

type cloudValidationImportEvidence struct {
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Platform      string `json:"platform"`
	Events        []struct {
		ScenarioID    string         `json:"scenario_id"`
		CorrelationID string         `json:"correlation_id"`
		Type          string         `json:"type"`
		ObservedAt    string         `json:"observed_at"`
		Evidence      map[string]any `json:"evidence"`
	} `json:"events"`
}

func importCloudValidationFeatureEvidence(workspace string, catalog testCatalog, input, outputDir string) error {
	input, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return err
	}
	if filepath.Clean(outputDir) != filepath.Clean(filepath.Dir(input)) {
		return errors.New("cloud-validation feature evidence must be written beside its native results")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		return err
	}
	var report cloudValidationImportReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return fmt.Errorf("parse cloud-validation results: %w", err)
	}
	if report.RunID == "" || strings.ToUpper(report.Status) != "PASS" || report.PlatformResult == nil || strings.ToUpper(report.PlatformResult.Status) != "PASS" {
		return errors.New("cloud-validation run and platform result must both PASS")
	}
	if report.Environment != "staging" || (report.Platform != "ios" && report.Platform != "android") {
		return errors.New("cloud-validation feature evidence requires staging ios or android")
	}
	started, err := time.Parse(time.RFC3339, report.StartedAt)
	if err != nil {
		return errors.New("cloud-validation started_at must be RFC3339")
	}
	completed, err := time.Parse(time.RFC3339, report.CompletedAt)
	if err != nil || completed.Before(started) {
		return errors.New("cloud-validation completed_at must be RFC3339 and not precede started_at")
	}
	if err := validateCloudValidationImportCommits(workspace, report); err != nil {
		return err
	}

	evidencePath := filepath.Join(outputDir, "cloud-evidence.json")
	evidence, err := loadCloudValidationImportEvidence(evidencePath, report.RunID, report.Platform)
	if err != nil {
		return err
	}
	refs, err := cloudValidationEvidenceRefs(outputDir)
	if err != nil {
		return err
	}
	if matches := findUnredactedFeatureEvidence(outputDir); len(matches) > 0 {
		return fmt.Errorf("unredacted credential-like content found in cloud-validation evidence: %s", strings.Join(matches, ", "))
	}

	scenarioContract := map[string]cloudValidationImportScenario{}
	for _, scenario := range report.Scenarios {
		scenarioContract[scenario.ID] = cloudValidationImportScenario{TestID: scenario.TestID, ExpectedEvents: scenario.ExpectedCloudEvidence}
	}
	events := cloudValidationEventsByScenario(evidence)
	requirements := catalogRequirementIndex(catalog)
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		return err
	}
	specCommit, err := currentCanonicalSpecCommit(workspace)
	if err != nil {
		return err
	}
	featureByRequirement := catalogFeatureByRequirement(catalog)
	var cases []featureCaseEvidenceV2
	seen := map[string]bool{}
	for _, result := range report.PlatformResult.Results {
		contract, exists := scenarioContract[result.ScenarioID]
		if !exists || contract.TestID == "" || contract.TestID != result.TestID {
			return fmt.Errorf("scenario %s Test ID does not match the native scenario contract", result.ScenarioID)
		}
		if seen[result.TestID] {
			return fmt.Errorf("duplicate cloud-validation Test ID %s", result.TestID)
		}
		seen[result.TestID] = true
		tc, exists := catalogCaseByID(catalog.Cases, result.TestID)
		if !exists || tc.Status != "active" || tc.Runner != "test-e2e" {
			return fmt.Errorf("cloud-validation result references unsupported Test ID %s", result.TestID)
		}
		status := strings.ToUpper(result.Status)
		if status != "PASS" {
			return fmt.Errorf("cloud-validation scenario %s (%s) did not PASS", result.ScenarioID, result.TestID)
		}
		if result.CorrelationID == "" {
			return fmt.Errorf("cloud-validation scenario %s is missing correlation ID", result.ScenarioID)
		}
		if err := requireCloudValidationEvents(result.ScenarioID, result.CorrelationID, contract.ExpectedEvents, events); err != nil {
			return err
		}
		feature := featureByRequirement[tc.Verifies[0]]
		commits, err := currentFeatureCommits(workspace, feature)
		if err != nil {
			return err
		}
		var assertions []featureRequirementAssertion
		for _, requirementID := range tc.Verifies {
			requirement, exists := requirements[requirementID]
			if !exists {
				return fmt.Errorf("Test ID %s references unknown requirement %s", tc.ID, requirementID)
			}
			if missing := missingFeatureEvidenceTypes(requirement.Evidence, refs); len(missing) > 0 {
				return fmt.Errorf("%s required evidence types missing: %s", requirementID, strings.Join(missing, ", "))
			}
			assertions = append(assertions, featureRequirementAssertion{
				RequirementID: requirementID, Revision: requirement.Revision, SpecSource: requirement.SpecSource,
				Status: "PASS", Assessment: "native deployed SDK scenario and Cloud evidence contract passed",
				Assertions: map[string]string{"native_scenario": "PASS", "cloud_evidence_contract": "PASS"}, Evidence: refs,
			})
		}
		workflowAssertions, err := cloudValidationWorkflowAssertions(tc, inventory, result.ScenarioID, result.CorrelationID, result.Evidence, events)
		if err != nil {
			return err
		}
		cases = append(cases, featureCaseEvidenceV2{
			TestID: tc.ID, Status: "PASS", Assessment: "native deployed SDK scenario passed",
			Environment: report.Environment, Target: report.Platform, StartedAt: started.UTC().Format(time.RFC3339),
			CompletedAt: completed.UTC().Format(time.RFC3339), WorkspaceCommit: commits["workspace"], Commits: commits,
			Requirements: assertions, Workflows: workflowAssertions,
		})
	}
	if len(cases) == 0 {
		return errors.New("cloud-validation result contains no feature Test IDs")
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].TestID < cases[j].TestID })
	manifest := featureEvidenceManifestV2{SchemaVersion: featureEvidenceSchemaV3, RunID: report.RunID, GeneratedAt: completed.UTC().Format(time.RFC3339), SpecCommit: specCommit, Cases: cases}
	if err := validateFeatureEvidenceManifestV2(manifest, catalog, inventory); err != nil {
		return err
	}
	return writeJSON(filepath.Join(outputDir, "feature-evidence.json"), manifest)
}

type cloudValidationImportScenario struct {
	TestID         string
	ExpectedEvents []string
}

func validateCloudValidationImportCommits(workspace string, report cloudValidationImportReport) error {
	sdk, err := gitOutput(filepath.Join(workspace, "repos", "rtk_cloud_client"), "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(sdk) != report.SDKCommit {
		return errors.New("cloud-validation SDK commit does not match the checkout")
	}
	contracts, err := gitOutput(filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc"), "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(contracts) != report.ContractsCommit {
		return errors.New("cloud-validation contracts commit does not match the checkout")
	}
	return nil
}

func loadCloudValidationImportEvidence(path, runID, platform string) (cloudValidationImportEvidence, error) {
	var evidence cloudValidationImportEvidence
	raw, err := os.ReadFile(path)
	if err != nil {
		return evidence, err
	}
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return evidence, fmt.Errorf("parse cloud evidence: %w", err)
	}
	if evidence.SchemaVersion != 1 || evidence.RunID != runID || evidence.Platform != platform {
		return evidence, errors.New("cloud evidence identity does not match native results")
	}
	return evidence, nil
}

func cloudValidationEvidenceRefs(dir string) ([]featureCoverageEvidenceFile, error) {
	var refs []featureCoverageEvidenceFile
	for _, name := range []string{"results.json", "junit.xml", "cloud-evidence.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read cloud-validation %s: %w", name, err)
		}
		sum := sha256.Sum256(raw)
		refs = append(refs, featureCoverageEvidenceFile{Path: name, SHA256: hex.EncodeToString(sum[:]), Type: featureEvidenceType(name)})
	}
	return refs, nil
}

type cloudValidationEventKey struct {
	CorrelationID string
	Types         map[string]bool
	Evidence      map[string]map[string]any
}

func cloudValidationEventsByScenario(evidence cloudValidationImportEvidence) map[string]cloudValidationEventKey {
	out := map[string]cloudValidationEventKey{}
	for _, event := range evidence.Events {
		item := out[event.ScenarioID]
		if item.Types == nil {
			item.Types = map[string]bool{}
		}
		if item.Evidence == nil {
			item.Evidence = map[string]map[string]any{}
		}
		item.CorrelationID = event.CorrelationID
		item.Types[event.Type] = true
		item.Evidence[event.Type] = event.Evidence
		out[event.ScenarioID] = item
	}
	return out
}

func requireCloudValidationEvents(scenarioID, correlationID string, expected []string, events map[string]cloudValidationEventKey) error {
	item := events[scenarioID]
	if len(expected) > 0 && item.CorrelationID != correlationID {
		return fmt.Errorf("cloud evidence correlation mismatch for scenario %s", scenarioID)
	}
	for _, event := range expected {
		if !item.Types[event] {
			return fmt.Errorf("cloud evidence missing %s for scenario %s", event, scenarioID)
		}
	}
	return nil
}

func cloudValidationWorkflowAssertions(tc testCatalogCase, inventory specInventory, scenarioID, correlationID string, nativeEvidence []string, events map[string]cloudValidationEventKey) ([]featureWorkflowAssertion, error) {
	eventSteps := map[string]map[string]string{
		"WF-SDK-AUTH-001":                 {"request_sdk_token": "token_issued", "read_authorized_device": "authorized_device_read"},
		"WF-CONTRACT-AUTH-APP-001":        {"discover_csr_requirement": "account_session_issued", "issue_app_certificate": "app_certificate_issued", "issue_app_runtime_token": "token_issued"},
		"WF-CONTRACT-AUTH-RECOVERY-001":   {"issue_device_access_token": "token_issued", "reissue_valid_access_token": "token_reissued", "recover_with_device_certificate": "certificate_recovery_succeeded"},
		"WF-CONTRACT-AUTH-REVOCATION-001": {"issue_pre_deactivation_token": "token_issued", "deactivate_device_identity": "device_deactivated", "reject_revoked_certificate": "certificate_rejected"},
	}
	item := events[scenarioID]
	if item.CorrelationID != correlationID {
		return nil, nil
	}
	native := map[string]bool{}
	for _, value := range nativeEvidence {
		native[strings.TrimSpace(value)] = true
	}
	var out []featureWorkflowAssertion
	for _, workflow := range inventory.Workflows {
		bound := false
		for _, requirementID := range workflow.RequirementIDs {
			bound = bound || catalogContainsString(tc.Verifies, requirementID)
		}
		if !bound {
			continue
		}
		if workflow.ID == "WF-SDK-WS-001" {
			assertion, err := cloudValidationWebSocketWorkflowAssertion(workflow, item, native)
			if err != nil {
				return nil, err
			}
			out = append(out, assertion)
			continue
		}
		mapping, supported := eventSteps[workflow.ID]
		if !supported {
			continue
		}
		assertion := featureWorkflowAssertion{WorkflowID: workflow.ID, Revision: workflow.Revision, Status: "PASS"}
		for _, step := range workflow.Steps {
			eventType := mapping[step.ID]
			if eventType == "" || !item.Types[eventType] {
				return nil, fmt.Errorf("cloud-validation workflow %s is missing PASS evidence for step %s", workflow.ID, step.ID)
			}
			assertion.Steps = append(assertion.Steps, featureWorkflowStepAssertion{StepID: step.ID, OperationRef: step.OperationRef, Status: "PASS", Assertions: map[string]string{"cloud_event_" + eventType: "PASS"}})
		}
		out = append(out, assertion)
	}
	return out, nil
}

func cloudValidationWebSocketWorkflowAssertion(workflow specWorkflow, item cloudValidationEventKey, native map[string]bool) (featureWorkflowAssertion, error) {
	assertions := map[string]map[string]string{
		"dispatch_cloud_command":     {"cloud_command_http_200": "PASS", "cloud_event_correlation_match": "PASS"},
		"receive_websocket_message":  {"transport_callback_received": "PASS"},
		"verify_message_correlation": {"run_correlation_match": "PASS"},
		"close_websocket_session":    {"session_disconnect_completed": "PASS"},
	}
	passed := map[string]bool{
		"dispatch_cloud_command":     item.Types["command_dispatched"] && cloudValidationHTTPStatus(item.Evidence["command_dispatched"]) == 200,
		"receive_websocket_message":  native["Cloud command received through SDK transport callback"],
		"verify_message_correlation": native["message matched run correlation"],
		"close_websocket_session":    native["WebSocket session disconnected after receive"],
	}
	assertion := featureWorkflowAssertion{WorkflowID: workflow.ID, Revision: workflow.Revision, Status: "PASS"}
	for _, step := range workflow.Steps {
		if !passed[step.ID] {
			return featureWorkflowAssertion{}, fmt.Errorf("cloud-validation workflow %s is missing PASS evidence for step %s", workflow.ID, step.ID)
		}
		assertion.Steps = append(assertion.Steps, featureWorkflowStepAssertion{
			StepID: step.ID, OperationRef: step.OperationRef, Status: "PASS", Assertions: assertions[step.ID],
		})
	}
	return assertion, nil
}

func cloudValidationHTTPStatus(evidence map[string]any) int {
	switch value := evidence["http_status"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		status, _ := value.Int64()
		return int(status)
	default:
		return 0
	}
}
