package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	featureEvidenceSchemaV2 = "rtk-cloud-feature-coverage-evidence/v2"
	featureEvidenceSchemaV3 = "rtk-cloud-feature-coverage-evidence/v3"
)

type featureCoverageEvidenceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Type   string `json:"type"`
}

type featureRequirementAssertion struct {
	RequirementID string                        `json:"requirement_id"`
	Revision      string                        `json:"requirement_revision"`
	SpecSource    specRequirementSource         `json:"spec_source"`
	Status        string                        `json:"status"`
	Assessment    string                        `json:"assessment,omitempty"`
	Assertions    map[string]string             `json:"assertions,omitempty"`
	Evidence      []featureCoverageEvidenceFile `json:"evidence,omitempty"`
}

type featureWorkflowStepAssertion struct {
	StepID       string            `json:"step_id"`
	OperationRef string            `json:"operation_ref"`
	Status       string            `json:"status"`
	Assertions   map[string]string `json:"assertions"`
}

type featureWorkflowAssertion struct {
	WorkflowID string                         `json:"workflow_id"`
	Revision   string                         `json:"workflow_revision"`
	Status     string                         `json:"status"`
	Steps      []featureWorkflowStepAssertion `json:"steps"`
}

type featureCaseEvidenceV2 struct {
	TestID          string                        `json:"test_id"`
	Status          string                        `json:"status"`
	Assessment      string                        `json:"assessment,omitempty"`
	Environment     string                        `json:"environment"`
	Target          string                        `json:"target,omitempty"`
	StartedAt       string                        `json:"started_at"`
	CompletedAt     string                        `json:"completed_at"`
	WorkspaceCommit string                        `json:"workspace_commit"`
	Commits         map[string]string             `json:"commits,omitempty"`
	Requirements    []featureRequirementAssertion `json:"requirements"`
	Workflows       []featureWorkflowAssertion    `json:"workflows,omitempty"`
}

type featureEvidenceManifestV2 struct {
	SchemaVersion string                  `json:"schema_version"`
	RunID         string                  `json:"run_id"`
	GeneratedAt   string                  `json:"generated_at"`
	SpecCommit    string                  `json:"spec_commit"`
	Cases         []featureCaseEvidenceV2 `json:"cases"`
}

type featureRequirementResult struct {
	FeatureID     string                `json:"feature_id"`
	RequirementID string                `json:"requirement_id"`
	Risk          string                `json:"risk"`
	Gate          string                `json:"gate"`
	Revision      string                `json:"requirement_revision"`
	SpecSource    specRequirementSource `json:"spec_source"`
	Status        string                `json:"status"`
	TestIDs       []string              `json:"test_ids,omitempty"`
	Detail        string                `json:"detail,omitempty"`
}

type featureCoverageReport struct {
	SchemaVersion  string                     `json:"schema_version"`
	GeneratedAt    string                     `json:"generated_at"`
	Mode           string                     `json:"mode"`
	Overall        string                     `json:"overall"`
	Required       int                        `json:"required"`
	Pass           int                        `json:"pass"`
	Missing        int                        `json:"missing"`
	Failed         int                        `json:"failed"`
	Stale          int                        `json:"stale"`
	StaleSpec      int                        `json:"stale_spec"`
	DeferredLive   int                        `json:"deferred_live"`
	CodeCoverage   string                     `json:"code_coverage"`
	Requirements   []featureRequirementResult `json:"requirements"`
	Selected       []string                   `json:"selected_features,omitempty"`
	EvidenceInputs []string                   `json:"evidence_inputs,omitempty"`
}

func runTestFeatureCoverage(args []string) error {
	action := "audit"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action, args = args[0], args[1:]
	}
	if action != "audit" && action != "check" && action != "select" && action != "record" {
		return errors.New("usage: test-feature-coverage [audit|select|check|record] [--evidence PATHS] [--mode pr|main|release] [--output-dir PATH]")
	}
	fs := flag.NewFlagSet("test-feature-coverage "+action, flag.ContinueOnError)
	var evidence, mode, outputDir, base, head, testID, runID, environment, target, startedAt, completedAt string
	fs.StringVar(&evidence, "evidence", "", "comma-separated evidence files or directories")
	fs.StringVar(&mode, "mode", "pr", "qualification mode: pr, main, or release")
	fs.StringVar(&outputDir, "output-dir", "", "report directory")
	fs.StringVar(&base, "base", "", "selection base commit")
	fs.StringVar(&head, "head", "HEAD", "selection head commit")
	fs.StringVar(&testID, "test-id", "", "record: catalog Test ID")
	fs.StringVar(&runID, "run-id", "", "record: stable live run ID")
	fs.StringVar(&environment, "environment", "", "record: catalog environment")
	fs.StringVar(&target, "target", "", "record: optional catalog target")
	fs.StringVar(&startedAt, "started-at", "", "record: RFC3339 start")
	fs.StringVar(&completedAt, "completed-at", "", "record: RFC3339 completion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if mode != "pr" && mode != "main" && mode != "release" {
		return fmt.Errorf("unsupported mode %q", mode)
	}
	if action != "record" && outputDir == "" {
		outputDir = ".artifacts/feature-coverage"
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	if action == "record" {
		if testID == "" || runID == "" || environment == "" || outputDir == "" || startedAt == "" || completedAt == "" {
			return errors.New("record requires --test-id, --run-id, --environment, --output-dir, --started-at, and --completed-at")
		}
		started, startErr := time.Parse(time.RFC3339, startedAt)
		completed, completeErr := time.Parse(time.RFC3339, completedAt)
		if startErr != nil || completeErr != nil || completed.Before(started) {
			return errors.New("record start/completion timestamps are invalid")
		}
		return writeCaseFeatureEvidence(workspace, outputDir, testID, runID, environment, target, started, completed)
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		return err
	}
	selected, err := selectCatalogFeatures(workspace, catalog, base, head)
	if err != nil {
		return err
	}
	if action == "select" {
		return writeFeatureSelection(outputDir, selected)
	}
	manifests, inputs, err := loadFeatureEvidence(workspace, catalog, evidence)
	if err != nil {
		return err
	}
	report := assessFeatureCoverageWithInventory(workspace, catalog, inventory, manifests, selected, mode, time.Now().UTC())
	report.EvidenceInputs = inputs
	if err := writeFeatureCoverageReport(outputDir, report); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "feature coverage: %s (%d/%d required PASS; code coverage is reported separately)\n", report.Overall, report.Pass, report.Required)
	if action == "check" && report.Overall != "PASS" {
		return fmt.Errorf("feature coverage %s: %d missing, %d failed, %d stale", report.Overall, report.Missing, report.Failed, report.Stale)
	}
	return nil
}

func selectCatalogFeatures(workspace string, catalog testCatalog, base, head string) ([]string, error) {
	all := make([]string, 0, len(catalog.Features))
	for _, feature := range catalog.Features {
		if feature.Status == "active" {
			all = append(all, feature.ID)
		}
	}
	sort.Strings(all)
	if strings.TrimSpace(base) == "" {
		return all, nil
	}
	raw, err := gitOutput(workspace, "diff", "--name-only", base+"..."+head)
	if err != nil {
		return nil, fmt.Errorf("select feature changes: %w", err)
	}
	paths := strings.Fields(raw)
	for _, path := range paths {
		if path == "tests/catalog.yaml" || path == "tests/spec-sources.yaml" ||
			strings.HasPrefix(path, "scripts/go/rtk-cloud/test_feature_coverage") ||
			strings.HasPrefix(path, "scripts/go/rtk-cloud/test_spec_") ||
			strings.HasPrefix(path, ".github/workflows/") || strings.HasPrefix(path, "scripts/test_") {
			return all, nil
		}
	}
	var selected []string
	selectedSet := map[string]bool{}
	matchedPaths := map[string]bool{}
	for _, feature := range catalog.Features {
		if feature.Status != "active" {
			continue
		}
		featureSelected := false
		for _, pattern := range feature.ChangePaths {
			re, regexErr := catalogGlobRegexp(pattern)
			if regexErr != nil {
				return nil, regexErr
			}
			for _, path := range paths {
				if re.MatchString(path) {
					matchedPaths[path] = true
					featureSelected = true
				}
			}
		}
		if featureSelected {
			selected = append(selected, feature.ID)
			selectedSet[feature.ID] = true
		}
	}
	before, beforeErr := loadSpecInventoryAt(workspace, base)
	after, afterErr := loadSpecInventoryAt(workspace, head)
	if beforeErr == nil && afterErr == nil {
		for _, change := range compareSpecInventories(base, head, before, after).Changes {
			if !selectedSet[change.FeatureID] {
				selected = append(selected, change.FeatureID)
				selectedSet[change.FeatureID] = true
			}
		}
	} else if isMissingSpecRegistry(beforeErr) && afterErr == nil {
		return all, nil
	}
	for _, path := range paths {
		if governedProductSurfacePath(path) && !matchedPaths[path] {
			return nil, fmt.Errorf("product surface %s is not mapped to a catalog feature", path)
		}
	}
	sort.Strings(selected)
	return selected, nil
}

func governedProductSurfacePath(path string) bool {
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "repos/rtk_account_manager/") {
		return strings.HasSuffix(path, ".go") || strings.Contains(path, "/web/")
	}
	if strings.HasPrefix(path, "repos/rtk_cloud_admin/") {
		return strings.HasSuffix(path, ".go") || strings.Contains(path, "/web/src/")
	}
	if strings.HasPrefix(path, "repos/rtk_cloud_client/") {
		return strings.Contains(path, "/src/") || strings.Contains(path, "/include/")
	}
	if strings.HasPrefix(path, "repos/rtk_cloud_frontend/") {
		return strings.HasSuffix(path, ".go")
	}
	return strings.HasPrefix(path, "scripts/") && (strings.HasSuffix(path, ".sh") || strings.HasSuffix(path, ".py"))
}

func loadFeatureEvidence(workspace string, catalog testCatalog, rawPaths string) ([]featureEvidenceManifestV2, []string, error) {
	if strings.TrimSpace(rawPaths) == "" {
		return nil, nil, nil
	}
	var files []string
	for _, value := range strings.Split(rawPaths, ",") {
		path := strings.TrimSpace(value)
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		if !info.IsDir() {
			files = append(files, path)
			continue
		}
		err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && (entry.Name() == "feature-evidence.json" || entry.Name() == "evidence-manifest.json") {
				files = append(files, candidate)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Strings(files)
	inventory, err := loadAvailableSpecInventory(workspace)
	if err != nil {
		return nil, nil, err
	}
	var manifests []featureEvidenceManifestV2
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		var header struct {
			SchemaVersion json.RawMessage `json:"schema_version"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", path, err)
		}
		var manifest featureEvidenceManifestV2
		if string(header.SchemaVersion) == `"`+featureEvidenceSchemaV3+`"` {
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return nil, nil, fmt.Errorf("parse %s: %w", path, err)
			}
		} else if string(header.SchemaVersion) == `"`+featureEvidenceSchemaV2+`"` {
			return nil, nil, fmt.Errorf("%s uses retired evidence schema v2; rerun the case against spec revision metadata", path)
		} else {
			var adaptErr error
			manifest, adaptErr = adaptLegacyFeatureEvidence(workspace, raw, path, catalog)
			if adaptErr != nil {
				return nil, nil, adaptErr
			}
		}
		if err := validateFeatureEvidenceManifestV2(manifest, catalog, inventory); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		currentSpecCommit, commitErr := currentCanonicalSpecCommit(workspace)
		if commitErr != nil {
			return nil, nil, commitErr
		}
		if manifest.SpecCommit != currentSpecCommit {
			return nil, nil, fmt.Errorf("%s: spec_commit does not match the canonical spec checkout", path)
		}
		for caseIndex := range manifest.Cases {
			for assertionIndex := range manifest.Cases[caseIndex].Requirements {
				for evidenceIndex := range manifest.Cases[caseIndex].Requirements[assertionIndex].Evidence {
					ref := &manifest.Cases[caseIndex].Requirements[assertionIndex].Evidence[evidenceIndex]
					if !filepath.IsAbs(ref.Path) {
						ref.Path = filepath.Join(filepath.Dir(path), ref.Path)
					}
				}
			}
		}
		manifests = append(manifests, manifest)
	}
	return manifests, files, nil
}

func validateFeatureEvidenceManifestV2(manifest featureEvidenceManifestV2, catalog testCatalog, inventories ...specInventory) error {
	if manifest.SchemaVersion != featureEvidenceSchemaV3 {
		return fmt.Errorf("schema_version=%q, want %s", manifest.SchemaVersion, featureEvidenceSchemaV3)
	}
	if strings.TrimSpace(manifest.SpecCommit) == "" {
		return errors.New("spec_commit is required")
	}
	if strings.TrimSpace(manifest.RunID) == "" {
		return errors.New("run_id is required")
	}
	if _, err := time.Parse(time.RFC3339, manifest.GeneratedAt); err != nil {
		return errors.New("generated_at must be RFC3339")
	}
	allowedStatus := map[string]bool{"PASS": true, "FAIL": true, "INCOMPLETE": true, "SKIP": true, "STALE": true}
	workflowIndex := map[string]specWorkflow{}
	if len(inventories) > 0 {
		for _, workflow := range inventories[0].Workflows {
			workflowIndex[workflow.ID] = workflow
		}
	}
	for _, item := range manifest.Cases {
		tc, ok := catalogCaseByID(catalog.Cases, item.TestID)
		if !ok {
			return fmt.Errorf("unknown test_id %s", item.TestID)
		}
		if !allowedStatus[item.Status] {
			return fmt.Errorf("%s has invalid status %q", item.TestID, item.Status)
		}
		if !catalogEnvironments[item.Environment] {
			return fmt.Errorf("%s has invalid environment %q", item.TestID, item.Environment)
		}
		if !catalogContainsString(tc.Environments, item.Environment) {
			return fmt.Errorf("%s environment %q is not declared by the test case", item.TestID, item.Environment)
		}
		if item.Target != "" && !catalogTargets[item.Target] {
			return fmt.Errorf("%s has invalid target %q", item.TestID, item.Target)
		}
		if item.Target != "" && !catalogContainsString(tc.Targets, item.Target) {
			return fmt.Errorf("%s target %q is not declared by the test case", item.TestID, item.Target)
		}
		if len(tc.Targets) > 0 && item.Target == "" {
			return fmt.Errorf("%s target is required by the test case", item.TestID)
		}
		started, startErr := time.Parse(time.RFC3339, item.StartedAt)
		completed, completeErr := time.Parse(time.RFC3339, item.CompletedAt)
		if startErr != nil || completeErr != nil || completed.Before(started) {
			return fmt.Errorf("%s has invalid start/completion timestamps", item.TestID)
		}
		if strings.TrimSpace(item.WorkspaceCommit) == "" {
			return fmt.Errorf("%s workspace_commit is required", item.TestID)
		}
		asserted := map[string]bool{}
		for _, assertion := range item.Requirements {
			_, ok := catalogRequirementIndex(catalog)[assertion.RequirementID]
			if !ok {
				return fmt.Errorf("%s has unknown requirement assertion %s", item.TestID, assertion.RequirementID)
			}
			if assertion.Revision == "" {
				return fmt.Errorf("%s requirement %s has missing spec revision", item.TestID, assertion.RequirementID)
			}
			if strings.TrimSpace(assertion.SpecSource.Path) == "" || strings.TrimSpace(assertion.SpecSource.Section) == "" {
				return fmt.Errorf("%s requirement %s has missing spec source", item.TestID, assertion.RequirementID)
			}
			if !catalogContainsString(tc.Verifies, assertion.RequirementID) {
				return fmt.Errorf("%s is not mapped to requirement %s", item.TestID, assertion.RequirementID)
			}
			if asserted[assertion.RequirementID] {
				return fmt.Errorf("%s repeats requirement assertion %s", item.TestID, assertion.RequirementID)
			}
			asserted[assertion.RequirementID] = true
			if !allowedStatus[assertion.Status] {
				return fmt.Errorf("%s requirement %s has invalid status %q", item.TestID, assertion.RequirementID, assertion.Status)
			}
			if len(assertion.Assertions) == 0 {
				return fmt.Errorf("%s requirement %s has no assertion results", item.TestID, assertion.RequirementID)
			}
			for _, ref := range assertion.Evidence {
				if !catalogEvidence[ref.Type] {
					return fmt.Errorf("%s requirement %s has invalid evidence type %q", item.TestID, assertion.RequirementID, ref.Type)
				}
			}
		}
		for _, requirementID := range tc.Verifies {
			if !asserted[requirementID] {
				return fmt.Errorf("%s is missing requirement assertion %s", item.TestID, requirementID)
			}
		}
		assertedWorkflows := map[string]bool{}
		for _, assertion := range item.Workflows {
			if assertion.WorkflowID == "" || assertedWorkflows[assertion.WorkflowID] {
				return fmt.Errorf("%s has missing or duplicate workflow assertion %s", item.TestID, assertion.WorkflowID)
			}
			assertedWorkflows[assertion.WorkflowID] = true
			if !allowedStatus[assertion.Status] {
				return fmt.Errorf("%s workflow %s has invalid status %q", item.TestID, assertion.WorkflowID, assertion.Status)
			}
			if assertion.Revision == "" {
				return fmt.Errorf("%s workflow %s has missing revision", item.TestID, assertion.WorkflowID)
			}
			expectedWorkflow, checkCurrent := workflowIndex[assertion.WorkflowID]
			if checkCurrent {
				if assertion.Revision != expectedWorkflow.Revision {
					return fmt.Errorf("%s workflow %s revision does not match current spec", item.TestID, assertion.WorkflowID)
				}
				bindsTestRequirement := false
				for _, requirementID := range expectedWorkflow.RequirementIDs {
					bindsTestRequirement = bindsTestRequirement || catalogContainsString(tc.Verifies, requirementID)
				}
				if !bindsTestRequirement {
					return fmt.Errorf("%s is not mapped to a requirement bound by workflow %s", item.TestID, assertion.WorkflowID)
				}
			}
			stepAssertions := map[string]featureWorkflowStepAssertion{}
			for _, step := range assertion.Steps {
				if step.StepID == "" || stepAssertions[step.StepID].StepID != "" {
					return fmt.Errorf("%s workflow %s repeats or omits a step ID", item.TestID, assertion.WorkflowID)
				}
				if !allowedStatus[step.Status] || len(step.Assertions) == 0 {
					return fmt.Errorf("%s workflow %s step %s has invalid status or no assertions", item.TestID, assertion.WorkflowID, step.StepID)
				}
				stepAssertions[step.StepID] = step
			}
			if checkCurrent {
				for _, expectedStep := range expectedWorkflow.Steps {
					step, exists := stepAssertions[expectedStep.ID]
					if !exists {
						return fmt.Errorf("%s workflow %s is missing step %s", item.TestID, assertion.WorkflowID, expectedStep.ID)
					}
					if step.OperationRef != expectedStep.OperationRef {
						return fmt.Errorf("%s workflow %s step %s operation mismatch", item.TestID, assertion.WorkflowID, expectedStep.ID)
					}
				}
				if len(stepAssertions) != len(expectedWorkflow.Steps) {
					return fmt.Errorf("%s workflow %s contains unknown steps", item.TestID, assertion.WorkflowID)
				}
			}
		}
	}
	return nil
}

func normalizeFeatureEvidenceStatus(status string) string {
	switch strings.ToUpper(status) {
	case "PASS", "FAIL", "INCOMPLETE", "SKIP", "STALE":
		return strings.ToUpper(status)
	default:
		return "INCOMPLETE"
	}
}

func adaptLegacyFeatureEvidence(workspace string, raw []byte, path string, catalog testCatalog) (featureEvidenceManifestV2, error) {
	var legacy struct {
		SchemaVersion   any               `json:"schema_version"`
		RunID           string            `json:"run_id"`
		Environment     string            `json:"environment"`
		Target          string            `json:"target"`
		GeneratedAt     string            `json:"generated_at"`
		WorkspaceCommit string            `json:"workspace_commit"`
		SubmoduleCommit string            `json:"submodule_commit"`
		Commits         map[string]string `json:"commits"`
		Cases           []struct {
			TestID           string                `json:"test_id"`
			Status           string                `json:"status"`
			Assessment       string                `json:"assessment"`
			CompletedAt      string                `json:"completed_at"`
			GeneratedAt      string                `json:"generated_at"`
			WorkspaceCommit  string                `json:"workspace_commit"`
			SubmoduleCommit  string                `json:"submodule_commit"`
			ScreenshotPath   string                `json:"screenshot_path"`
			ScreenshotSHA256 string                `json:"screenshot_sha256"`
			Evidence         []featureEvidenceFile `json:"evidence"`
			Commits          map[string]string     `json:"commits"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return featureEvidenceManifestV2{}, fmt.Errorf("parse legacy evidence %s: %w", path, err)
	}
	if legacy.RunID == "" || len(legacy.Cases) == 0 {
		return featureEvidenceManifestV2{}, fmt.Errorf("%s is neither evidence manifest v2 nor a supported UI/feature manifest", path)
	}
	specCommit, _ := currentCanonicalSpecCommit(workspace)
	out := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV3, RunID: legacy.RunID, GeneratedAt: legacy.GeneratedAt,
		SpecCommit: specCommit,
	}
	for _, item := range legacy.Cases {
		tc, ok := catalogCaseByID(catalog.Cases, item.TestID)
		if !ok {
			continue
		}
		status := strings.ToUpper(item.Status)
		if status == "" {
			status = strings.ToUpper(item.Assessment)
		}
		status = normalizeFeatureEvidenceStatus(status)
		completed := item.CompletedAt
		if completed == "" {
			completed = item.GeneratedAt
		}
		if completed == "" {
			completed = legacy.GeneratedAt
		}
		workspaceCommit := item.WorkspaceCommit
		if workspaceCommit == "" {
			workspaceCommit = legacy.WorkspaceCommit
		}
		if workspaceCommit == "" {
			workspaceCommit = legacy.Commits["workspace"]
		}
		commits := item.Commits
		if commits == nil {
			commits = map[string]string{}
			for key, value := range legacy.Commits {
				commits[key] = value
			}
		}
		submodule := item.SubmoduleCommit
		if submodule == "" {
			submodule = legacy.SubmoduleCommit
		}
		if submodule != "" && strings.HasPrefix(item.TestID, "UI-CA-") {
			commits["cloud_admin"] = submodule
		}
		var refs []featureCoverageEvidenceFile
		if item.ScreenshotPath != "" {
			refs = append(refs, featureCoverageEvidenceFile{Path: item.ScreenshotPath, SHA256: item.ScreenshotSHA256, Type: "screenshot"})
		}
		for _, ref := range item.Evidence {
			refs = append(refs, featureCoverageEvidenceFile{Path: ref.Path, SHA256: ref.SHA256, Type: featureEvidenceType(ref.Path)})
		}
		var assertions []featureRequirementAssertion
		// Legacy adapters are safe only for one-to-one cases. Multi-requirement
		// cases must migrate to v3 and report each assertion explicitly.
		if len(tc.Verifies) == 1 {
			requirement := catalogRequirementIndex(catalog)[tc.Verifies[0]]
			assertions = append(assertions, featureRequirementAssertion{
				RequirementID: tc.Verifies[0], Revision: requirement.Revision, SpecSource: requirement.SpecSource,
				Status: status, Assessment: item.Assessment,
				Assertions: map[string]string{"case_assessment": status}, Evidence: refs,
			})
		}
		out.Cases = append(out.Cases, featureCaseEvidenceV2{
			TestID: item.TestID, Status: status, Assessment: item.Assessment,
			Environment: legacy.Environment, Target: legacy.Target, StartedAt: completed, CompletedAt: completed,
			WorkspaceCommit: workspaceCommit, Commits: commits, Requirements: assertions,
		})
	}
	return out, nil
}

func assessFeatureCoverage(workspace string, catalog testCatalog, manifests []featureEvidenceManifestV2, selected []string, mode string, now time.Time) featureCoverageReport {
	return assessFeatureCoverageWithInventory(workspace, catalog, specInventory{}, manifests, selected, mode, now)
}

func assessFeatureCoverageWithInventory(
	workspace string,
	catalog testCatalog,
	inventory specInventory,
	manifests []featureEvidenceManifestV2,
	selected []string,
	mode string,
	now time.Time,
) featureCoverageReport {
	report := featureCoverageReport{
		SchemaVersion: "rtk-cloud-feature-coverage-report/v3", GeneratedAt: now.Format(time.RFC3339),
		Mode: mode, Overall: "PASS", CodeCoverage: "SEPARATE_NOT_SCORED", Selected: selected,
	}
	selectedSet := map[string]bool{}
	for _, id := range selected {
		selectedSet[id] = true
	}
	caseByID := map[string]testCatalogCase{}
	for _, tc := range catalog.Cases {
		caseByID[tc.ID] = tc
	}
	var allCases []featureCaseEvidenceV2
	for _, manifest := range manifests {
		allCases = append(allCases, manifest.Cases...)
	}
	workflowsByRequirement := map[string][]specWorkflow{}
	for _, workflow := range inventory.Workflows {
		for _, requirementID := range workflow.RequirementIDs {
			workflowsByRequirement[requirementID] = append(workflowsByRequirement[requirementID], workflow)
		}
	}
	for _, feature := range catalog.Features {
		if feature.Status != "active" || !selectedSet[feature.ID] {
			continue
		}
		for _, requirement := range feature.Requirements {
			if requirement.Status != "active" || !requirementRequired(requirement) {
				continue
			}
			result := featureRequirementResult{
				FeatureID: feature.ID, RequirementID: requirement.ID, Risk: feature.Risk, Gate: requirement.Gate,
				Revision: requirement.Revision, SpecSource: requirement.SpecSource,
			}
			if !requirementEvaluatedInMode(requirement, mode) {
				result.Status, result.Detail = "DEFERRED_LIVE", "not executable in this gate; no PASS credit awarded"
				report.DeferredLive++
				report.Requirements = append(report.Requirements, result)
				continue
			}
			report.Required++
			result.Status, result.Detail, result.TestIDs = evaluateRequirementEvidence(
				workspace, requirement, feature, caseByID, allCases, now, workflowsByRequirement[requirement.ID]...)
			switch result.Status {
			case "PASS":
				report.Pass++
			case "STALE":
				report.Stale++
			case "STALE_SPEC":
				report.StaleSpec++
			case "FAIL":
				report.Failed++
			default:
				report.Missing++
			}
			report.Requirements = append(report.Requirements, result)
		}
	}
	if report.Pass != report.Required {
		report.Overall = "FAIL"
	}
	sort.Slice(report.Requirements, func(i, j int) bool {
		if report.Requirements[i].FeatureID == report.Requirements[j].FeatureID {
			return report.Requirements[i].RequirementID < report.Requirements[j].RequirementID
		}
		return report.Requirements[i].FeatureID < report.Requirements[j].FeatureID
	})
	return report
}

func requirementEvaluatedInMode(requirement testCatalogRequirement, mode string) bool {
	switch mode {
	case "pr":
		return requirement.Gate == "pr"
	default:
		return true
	}
}

func evaluateRequirementEvidence(
	workspace string,
	requirement testCatalogRequirement,
	feature testCatalogFeature,
	cases map[string]testCatalogCase,
	evidence []featureCaseEvidenceV2,
	now time.Time,
	workflows ...specWorkflow,
) (string, string, []string) {
	var mapped []string
	for _, tc := range cases {
		if tc.Status == "active" && catalogContainsString(tc.Verifies, requirement.ID) {
			mapped = append(mapped, tc.ID)
		}
	}
	sort.Strings(mapped)
	if len(mapped) == 0 {
		return "MISSING", "no mapped qualifying test", nil
	}
	requiredTargets := map[string]bool{"": true}
	if len(requirement.Targets) > 0 {
		requiredTargets = map[string]bool{}
		for _, target := range requirement.Targets {
			requiredTargets[target] = true
		}
	}
	passedTargets := map[string]bool{}
	staleTargets := map[string]bool{}
	var lastReason = "no evidence manifest contains an explicit requirement assertion"
	specRevisionStale := false
	for _, item := range evidence {
		tc, ok := cases[item.TestID]
		if !ok || !catalogContainsString(mapped, item.TestID) || tc.Layer == "unit" || tc.Layer == "service" {
			continue
		}
		if len(tc.Verifies) > 1 && len(item.Requirements) < len(tc.Verifies) {
			lastReason = "multi-requirement case is missing explicit assertions"
			continue
		}
		for _, assertion := range item.Requirements {
			if assertion.RequirementID != requirement.ID {
				continue
			}
			if assertion.Revision != requirement.Revision || assertion.SpecSource.Path != requirement.SpecSource.Path ||
				assertion.SpecSource.Section != requirement.SpecSource.Section {
				specRevisionStale = true
				lastReason = "evidence requirement revision or source does not match the current spec"
				continue
			}
			if strings.ToUpper(item.Status) != "PASS" || strings.ToUpper(assertion.Status) != "PASS" {
				lastReason = "case or requirement assertion did not PASS"
				continue
			}
			if len(assertion.Assertions) == 0 {
				lastReason = "requirement assertion results missing"
				continue
			}
			assertionsPass := true
			for _, value := range assertion.Assertions {
				if strings.ToUpper(value) != "PASS" {
					assertionsPass = false
					break
				}
			}
			if !assertionsPass {
				lastReason = "one or more requirement assertions did not PASS"
				continue
			}
			if ok, reason := qualifyingWorkflowEvidence(item, workflows); !ok {
				lastReason = reason
				continue
			}
			if !catalogContainsString(requirement.Environments, item.Environment) {
				lastReason = "environment mismatch"
				continue
			}
			targetKey := ""
			if len(requirement.Targets) > 0 {
				targetKey = item.Target
			}
			if !requiredTargets[targetKey] {
				lastReason = "target mismatch"
				continue
			}
			if requirement.FreshnessHours > 0 {
				completed, err := time.Parse(time.RFC3339, item.CompletedAt)
				if err != nil || now.Sub(completed) > time.Duration(requirement.FreshnessHours)*time.Hour {
					staleTargets[targetKey] = true
					lastReason = "evidence exceeds freshness policy"
					continue
				}
			}
			if strings.TrimSpace(item.WorkspaceCommit) == "" {
				lastReason = "workspace commit anchor missing"
				continue
			}
			commit, err := gitOutput(workspace, "rev-parse", "HEAD")
			if err == nil && strings.TrimSpace(commit) != item.WorkspaceCommit {
				lastReason = "workspace commit mismatch"
				continue
			}
			if err := validateFeatureCommits(workspace, feature, item.Commits); err != nil {
				lastReason = err.Error()
				continue
			}
			if err := verifyFeatureEvidenceFiles(assertion.Evidence); err != nil {
				lastReason = err.Error()
				continue
			}
			if missing := missingFeatureEvidenceTypes(requirement.Evidence, assertion.Evidence); len(missing) > 0 {
				lastReason = "required evidence types missing: " + strings.Join(missing, ", ")
				continue
			}
			passedTargets[targetKey] = true
			if len(passedTargets) == len(requiredTargets) {
				return "PASS", "qualified evidence for every required target", mapped
			}
		}
	}
	if len(passedTargets) > 0 {
		var missing []string
		for target := range requiredTargets {
			if !passedTargets[target] {
				missing = append(missing, target)
			}
		}
		sort.Strings(missing)
		lastReason = "qualified evidence missing for targets: " + strings.Join(missing, ", ")
	}
	for target := range requiredTargets {
		if !passedTargets[target] && staleTargets[target] {
			return "STALE", "latest qualifying evidence exceeds freshness policy", mapped
		}
	}
	if specRevisionStale {
		return "STALE_SPEC", "latest evidence was produced for a different spec revision", mapped
	}
	if strings.Contains(lastReason, "did not PASS") {
		return "FAIL", lastReason, mapped
	}
	return "MISSING", lastReason, mapped
}

func qualifyingWorkflowEvidence(item featureCaseEvidenceV2, workflows []specWorkflow) (bool, string) {
	if len(workflows) == 0 {
		return true, ""
	}
	assertions := map[string]featureWorkflowAssertion{}
	for _, assertion := range item.Workflows {
		assertions[assertion.WorkflowID] = assertion
	}
	for _, workflow := range workflows {
		assertion, exists := assertions[workflow.ID]
		if !exists {
			return false, "required workflow assertion is missing: " + workflow.ID
		}
		if assertion.Revision != workflow.Revision {
			return false, "workflow evidence revision does not match the current spec: " + workflow.ID
		}
		if strings.ToUpper(assertion.Status) != "PASS" {
			return false, "workflow assertion did not PASS: " + workflow.ID
		}
		stepAssertions := map[string]featureWorkflowStepAssertion{}
		for _, step := range assertion.Steps {
			stepAssertions[step.StepID] = step
		}
		for _, expected := range workflow.Steps {
			step, exists := stepAssertions[expected.ID]
			if !exists || step.OperationRef != expected.OperationRef || strings.ToUpper(step.Status) != "PASS" {
				return false, "workflow step did not PASS: " + workflow.ID + "#" + expected.ID
			}
			for _, status := range step.Assertions {
				if strings.ToUpper(status) != "PASS" {
					return false, "workflow step assertion did not PASS: " + workflow.ID + "#" + expected.ID
				}
			}
		}
	}
	return true, ""
}

func validateFeatureCommits(workspace string, feature testCatalogFeature, commits map[string]string) error {
	repositories := featureCommitRepositories(workspace)
	for _, anchor := range feature.CommitAnchors {
		if anchor == "workspace" {
			continue
		}
		value := strings.TrimSpace(commits[anchor])
		if value == "" {
			return fmt.Errorf("feature commit anchor %s missing", anchor)
		}
		repository, ok := repositories[anchor]
		if !ok {
			return fmt.Errorf("unsupported feature commit anchor %s", anchor)
		}
		current, err := gitOutput(repository, "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(current) != value {
			return fmt.Errorf("feature commit anchor %s mismatch", anchor)
		}
	}
	return nil
}

func featureCommitRepositories(workspace string) map[string]string {
	return map[string]string{
		"account_manager": filepath.Join(workspace, "repos", "rtk_account_manager"),
		"cloud_admin":     filepath.Join(workspace, "repos", "rtk_cloud_admin"),
		"cloud_client":    filepath.Join(workspace, "repos", "rtk_cloud_client"),
		"video_cloud":     filepath.Join(workspace, "repos", "rtk_video_cloud"),
		"cloud_logger":    filepath.Join(workspace, "repos", "rtk_cloud_logger"),
		"contracts":       filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc"),
		"frontend":        filepath.Join(workspace, "repos", "rtk_cloud_frontend"),
	}
}

func currentFeatureCommits(workspace string, feature testCatalogFeature) (map[string]string, error) {
	repositories := featureCommitRepositories(workspace)
	repositories["workspace"] = workspace
	commits := map[string]string{}
	for _, anchor := range feature.CommitAnchors {
		repository, ok := repositories[anchor]
		if !ok {
			return nil, fmt.Errorf("unsupported feature commit anchor %s", anchor)
		}
		current, err := gitOutput(repository, "rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("resolve feature commit anchor %s: %w", anchor, err)
		}
		commits[anchor] = strings.TrimSpace(current)
	}
	return commits, nil
}

func verifyFeatureEvidenceFiles(files []featureCoverageEvidenceFile) error {
	if len(files) == 0 {
		return errors.New("requirement evidence file missing")
	}
	for _, file := range files {
		raw, err := os.ReadFile(file.Path)
		if err != nil {
			return fmt.Errorf("read evidence: %w", err)
		}
		sum := sha256.Sum256(raw)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), file.SHA256) {
			return errors.New("evidence SHA-256 mismatch")
		}
	}
	return nil
}

func featureEvidenceType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json"
	case ".xml":
		return "junit"
	case ".md", ".markdown":
		return "markdown"
	case ".log", ".txt":
		return "logs"
	case ".png", ".jpg", ".jpeg", ".webp":
		return "screenshot"
	default:
		return "cloud-evidence"
	}
}

func missingFeatureEvidenceTypes(required []string, actual []featureCoverageEvidenceFile) []string {
	present := map[string]bool{}
	for _, ref := range actual {
		present[ref.Type] = true
	}
	var missing []string
	for _, evidenceType := range required {
		if !present[evidenceType] {
			missing = append(missing, evidenceType)
		}
	}
	sort.Strings(missing)
	return missing
}

func writeCaseFeatureEvidence(workspace, outputDir, testID, runID, environment, target string, started, completed time.Time) error {
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	tc, ok := catalogCaseByID(catalog.Cases, testID)
	if !ok || tc.Status != "active" {
		return fmt.Errorf("cannot record unknown or inactive test case %s", testID)
	}
	if len(tc.Verifies) == 0 {
		return fmt.Errorf("test case %s does not verify a requirement", testID)
	}
	features := catalogFeatureByRequirement(catalog)
	feature, ok := features[tc.Verifies[0]]
	if !ok {
		return fmt.Errorf("test case %s has no feature mapping", testID)
	}
	for _, requirementID := range tc.Verifies[1:] {
		if mapped := features[requirementID]; mapped.ID != feature.ID {
			return fmt.Errorf("test case %s spans multiple features", testID)
		}
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if matches := findUnredactedFeatureEvidence(outputDir); len(matches) > 0 {
		return fmt.Errorf("unredacted credential-like content found in live evidence: %s", strings.Join(matches, ", "))
	}
	refs, err := collectCaseFeatureEvidence(outputDir)
	if err != nil {
		return err
	}
	commits, err := currentFeatureCommits(workspace, feature)
	if err != nil {
		return err
	}
	assertions := make([]featureRequirementAssertion, 0, len(tc.Verifies))
	requirements := catalogRequirementIndex(catalog)
	for _, requirementID := range tc.Verifies {
		requirement := requirements[requirementID]
		if missing := missingFeatureEvidenceTypes(requirement.Evidence, refs); len(missing) > 0 {
			return fmt.Errorf("%s required evidence types missing: %s", requirementID, strings.Join(missing, ", "))
		}
		assertions = append(assertions, featureRequirementAssertion{
			RequirementID: requirementID,
			Revision:      requirement.Revision,
			SpecSource:    requirement.SpecSource,
			Status:        "PASS",
			Assessment:    "live product assertions and evidence contract passed",
			Assertions: map[string]string{
				"native_flow":       "PASS",
				"evidence_contract": "PASS",
			},
			Evidence: refs,
		})
	}
	specCommit, err := currentCanonicalSpecCommit(workspace)
	if err != nil {
		return err
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		return err
	}
	workflowAssertions, err := loadLiveWorkflowAssertions(outputDir, tc, inventory)
	if err != nil {
		return err
	}
	manifest := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV3,
		RunID:         runID,
		GeneratedAt:   completed.UTC().Format(time.RFC3339),
		SpecCommit:    specCommit,
		Cases: []featureCaseEvidenceV2{{
			TestID:          testID,
			Status:          "PASS",
			Assessment:      "live product flow passed",
			Environment:     environment,
			Target:          target,
			StartedAt:       started.UTC().Format(time.RFC3339),
			CompletedAt:     completed.UTC().Format(time.RFC3339),
			WorkspaceCommit: commits["workspace"],
			Commits:         commits,
			Requirements:    assertions,
			Workflows:       workflowAssertions,
		}},
	}
	if err := validateFeatureEvidenceManifestV2(manifest, catalog, inventory); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "feature-evidence.json"), manifest); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "feature-results.json"), map[string]any{
		"schema_version": featureEvidenceSchemaV3,
		"run_id":         runID, "test_id": testID, "status": "PASS",
		"started_at": started.UTC().Format(time.RFC3339), "completed_at": completed.UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	duration := completed.Sub(started).Seconds()
	junit := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><testsuite name="feature-live" tests="1" failures="0" time="%.3f"><testcase classname="feature-live" name="%s" time="%.3f"/></testsuite>`+"\n", duration, testID, duration)
	if err := os.WriteFile(filepath.Join(outputDir, "feature-junit.xml"), []byte(junit), 0o644); err != nil {
		return err
	}
	report := fmt.Sprintf("# Live Feature Evidence\n\n- Test ID: `%s`\n- Run ID: `%s`\n- Status: **PASS**\n- Requirements: `%s`\n- Workspace commit: `%s`\n", testID, runID, strings.Join(tc.Verifies, "`, `"), commits["workspace"])
	return os.WriteFile(filepath.Join(outputDir, "FEATURE_REPORT.md"), []byte(report), 0o644)
}

func loadLiveWorkflowAssertions(outputDir string, tc testCatalogCase, inventory specInventory) ([]featureWorkflowAssertion, error) {
	bound := map[string]specWorkflow{}
	for _, workflow := range inventory.Workflows {
		for _, requirementID := range workflow.RequirementIDs {
			if catalogContainsString(tc.Verifies, requirementID) {
				bound[workflow.ID] = workflow
			}
		}
	}
	if len(bound) == 0 {
		return nil, nil
	}
	type liveWorkflow struct {
		WorkflowID string            `json:"workflow_id"`
		Steps      map[string]string `json:"steps"`
	}
	var candidates []string
	if err := filepath.WalkDir(outputDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") &&
			entry.Name() != "feature-evidence.json" && entry.Name() != "feature-results.json" {
			candidates = append(candidates, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	declared := map[string]liveWorkflow{}
	for _, path := range candidates {
		var payload struct {
			Workflow liveWorkflow `json:"workflow"`
		}
		if err := readJSONFile(path, &payload); err != nil || payload.Workflow.WorkflowID == "" {
			continue
		}
		if _, repeated := declared[payload.Workflow.WorkflowID]; repeated {
			return nil, fmt.Errorf("live evidence repeats workflow %s", payload.Workflow.WorkflowID)
		}
		declared[payload.Workflow.WorkflowID] = payload.Workflow
	}
	var assertions []featureWorkflowAssertion
	for workflowID, workflow := range bound {
		actual, exists := declared[workflowID]
		if !exists {
			return nil, fmt.Errorf("%s requires step-level live workflow evidence for %s", tc.ID, workflowID)
		}
		statuses := actual.Steps
		assertion, err := buildWorkflowAssertion(workflow, statuses)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tc.ID, err)
		}
		assertions = append(assertions, assertion)
	}
	sort.Slice(assertions, func(i, j int) bool { return assertions[i].WorkflowID < assertions[j].WorkflowID })
	return assertions, nil
}

func buildWorkflowAssertion(workflow specWorkflow, statuses map[string]string) (featureWorkflowAssertion, error) {
	assertion := featureWorkflowAssertion{WorkflowID: workflow.ID, Revision: workflow.Revision, Status: "PASS"}
	for _, step := range workflow.Steps {
		status := strings.ToUpper(strings.TrimSpace(statuses[step.ID]))
		if status != "PASS" {
			return featureWorkflowAssertion{}, fmt.Errorf("workflow %s step %s did not provide PASS evidence", workflow.ID, step.ID)
		}
		assertion.Steps = append(assertion.Steps, featureWorkflowStepAssertion{
			StepID: step.ID, OperationRef: step.OperationRef, Status: status,
			Assertions: map[string]string{"operation_and_transition": "PASS"},
		})
	}
	if len(statuses) != len(workflow.Steps) {
		return featureWorkflowAssertion{}, fmt.Errorf("workflow %s evidence contains missing or unknown steps", workflow.ID)
	}
	return assertion, nil
}

func currentCanonicalSpecCommit(workspace string) (string, error) {
	commit, err := gitOutput(filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc"), "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve canonical spec commit: %w", err)
	}
	return strings.TrimSpace(commit), nil
}

func collectCaseFeatureEvidence(outputDir string) ([]featureCoverageEvidenceFile, error) {
	excluded := map[string]bool{
		"feature-evidence.json": true, "feature-results.json": true,
		"feature-junit.xml": true, "FEATURE_REPORT.md": true,
	}
	var refs []featureCoverageEvidenceFile
	err := filepath.WalkDir(outputDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || excluded[entry.Name()] {
			return nil
		}
		evidenceType := featureEvidenceType(path)
		if !catalogEvidence[evidenceType] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		rel, err := filepath.Rel(outputDir, path)
		if err != nil {
			return err
		}
		refs = append(refs, featureCoverageEvidenceFile{
			Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(sum[:]), Type: evidenceType,
		})
		return nil
	})
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })
	return refs, err
}

func writeFeatureSelection(outputDir string, selected []string) error {
	payload := struct {
		SchemaVersion string   `json:"schema_version"`
		Features      []string `json:"features"`
	}{"rtk-cloud-feature-selection/v2", selected}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(outputDir, "feature-selection.json"), payload)
}

func writeFeatureCoverageReport(outputDir string, report featureCoverageReport) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outputDir, "feature-coverage.json"), report); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintln(&b, "# Feature Coverage")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Overall: **%s**\n", report.Overall)
	fmt.Fprintf(&b, "- Product requirements: **%d/%d PASS**\n", report.Pass, report.Required)
	fmt.Fprintf(&b, "- Stale spec revision: **%d**\n", report.StaleSpec)
	fmt.Fprintf(&b, "- Deferred live (no PASS credit): **%d**\n", report.DeferredLive)
	fmt.Fprintln(&b, "- Code coverage: **separate quality metric; never contributes to feature coverage**")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Feature | Requirement | Spec source | Revision | Risk | Gate | Status | Detail |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, result := range report.Requirements {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s#%s` | `%s` | `%s` | `%s` | **%s** | %s |\n",
			result.FeatureID, result.RequirementID, result.SpecSource.Path, result.SpecSource.Section,
			shortRevision(result.Revision), result.Risk, result.Gate, result.Status, escapeMarkdownCell(result.Detail))
	}
	rendered := []byte(b.String())
	if err := os.WriteFile(filepath.Join(outputDir, "FEATURE_COVERAGE.md"), rendered, 0o644); err != nil {
		return err
	}
	if summaryPath := strings.TrimSpace(os.Getenv("GITHUB_STEP_SUMMARY")); summaryPath != "" {
		summary, err := os.OpenFile(summaryPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open GitHub summary: %w", err)
		}
		if _, err := summary.Write(rendered); err != nil {
			_ = summary.Close()
			return fmt.Errorf("write GitHub summary: %w", err)
		}
		if err := summary.Close(); err != nil {
			return fmt.Errorf("close GitHub summary: %w", err)
		}
	}
	return nil
}

func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
