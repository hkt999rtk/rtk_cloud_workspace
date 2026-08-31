package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const featureEvidenceSchema = "rtk-cloud-feature-evidence/v1"

type featureRunSpec struct {
	Feature      string                         `json:"feature"`
	Profile      string                         `json:"profile"`
	TestIDs      []string                       `json:"test_ids"`
	LoadTestID   string                         `json:"load_test_id,omitempty"`
	ScenarioPath string                         `json:"scenario_path"`
	Scale        map[string]any                 `json:"scale"`
	Purpose      string                         `json:"purpose"`
	Method       string                         `json:"method"`
	CaseMetadata map[string]featureCaseMetadata `json:"case_metadata"`
}

type featureCaseMetadata struct {
	Purpose string `json:"purpose"`
	Method  string `json:"method"`
}

type featureEvidenceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type featureCaseEvidence struct {
	TestID                string                `json:"test_id"`
	LoadTestID            string                `json:"load_test_id,omitempty"`
	Feature               string                `json:"feature"`
	Profile               string                `json:"profile"`
	Scale                 map[string]any        `json:"scale"`
	ActualScale           map[string]any        `json:"actual_scale"`
	Purpose               string                `json:"purpose"`
	Method                string                `json:"method"`
	StartedAt             string                `json:"started_at"`
	CompletedAt           string                `json:"completed_at"`
	DurationMS            int64                 `json:"duration_ms"`
	Status                string                `json:"status"`
	Assessment            string                `json:"assessment"`
	Operations            []string              `json:"operations"`
	SuccessRate           float64               `json:"success_rate"`
	Latency               map[string]any        `json:"latency,omitempty"`
	ExactEventCorrelation float64               `json:"exact_event_correlation"`
	Evidence              []featureEvidenceFile `json:"evidence"`
	Commits               map[string]string     `json:"commits"`
	RunID                 string                `json:"run_id"`
}

type featureEvidenceManifest struct {
	SchemaVersion string                `json:"schema_version"`
	RunID         string                `json:"run_id"`
	Feature       string                `json:"feature"`
	Profile       string                `json:"profile"`
	Environment   string                `json:"environment"`
	Status        string                `json:"status"`
	Assessment    string                `json:"assessment"`
	StartedAt     string                `json:"started_at"`
	CompletedAt   string                `json:"completed_at"`
	DurationMS    int64                 `json:"duration_ms"`
	Cases         []featureCaseEvidence `json:"cases"`
	Commits       map[string]string     `json:"commits"`
	GeneratedAt   string                `json:"generated_at"`
}

type featureSelection struct {
	SchemaVersion string              `json:"schema_version"`
	Required      bool                `json:"required"`
	Features      []string            `json:"features"`
	MatchedPaths  map[string][]string `json:"matched_paths"`
	Labels        []string            `json:"labels,omitempty"`
}

func runTestFeature(args []string) error {
	if len(args) > 0 && args[0] == "select" {
		return runTestFeatureSelect(args[1:])
	}
	fs := flag.NewFlagSet("test-feature", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	feature := fs.String("feature", "", "feature key from tests/catalog.yaml")
	profile := fs.String("profile", "canary", "canary or qualification-1k")
	environment := fs.String("environment", "staging", "target environment")
	runID := fs.String("run-id", "", "stable feature qualification run ID")
	envRootFlag := fs.String("env-root", "cloud_env/staging/runtime", "staging environment runtime root")
	planMode := fs.Bool("plan", false, "print the qualification plan")
	runMode := fs.Bool("run", false, "execute the qualification")
	confirm := fs.String("confirm", "", "required stack confirmation for --run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *feature == "" {
		return errors.New("--feature is required")
	}
	if *profile != "canary" && *profile != "qualification-1k" {
		return errors.New("--profile must be canary or qualification-1k")
	}
	if *environment != "staging" {
		return errors.New("test-feature currently supports --environment staging only")
	}
	if *planMode && *runMode {
		return errors.New("--plan and --run are mutually exclusive")
	}
	if !*planMode && !*runMode {
		*planMode = true
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}
	if ok, _ := regexpMatchRunID(*runID); !ok {
		return errors.New("--run-id must contain only letters, digits, dot, underscore, and hyphen")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	specs, err := resolveFeatureSpecs(catalog, *feature, *profile)
	if err != nil {
		return err
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	commits, err := featureCommitAnchors(workspace)
	if err != nil {
		return err
	}
	if *planMode {
		return printFeaturePlan(*runID, *environment, envRoot, specs, commits)
	}
	stack := firstNonEmpty(envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), "video-cloud-staging")
	if *confirm != stack {
		return fmt.Errorf("--confirm %s does not match CLOUD_STACK_NAME=%s", *confirm, stack)
	}
	runRoot := filepath.Join(workspace, ".artifacts", "test-runs", *runID, "features", *feature)
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		return err
	}
	if err := verifyFeatureDeploymentCommits(envRoot, commits); err != nil {
		manifests := make([]featureEvidenceManifest, 0, len(specs))
		for _, spec := range specs {
			manifest := incompleteFeatureManifest(*runID, *feature, spec.Profile, *environment, commits, []featureRunSpec{spec}, err.Error())
			manifests = append(manifests, manifest)
			if writeErr := writeFeatureStageReports(filepath.Join(runRoot, spec.Profile), spec, manifest); writeErr != nil {
				return writeErr
			}
		}
		combined := combineFeatureManifests(*runID, *feature, *profile, *environment, commits, manifests)
		_ = writeFeatureQualificationReports(runRoot, combined)
		return fmt.Errorf("feature qualification INCOMPLETE: %w", err)
	}

	specByProfile := make(map[string]featureRunSpec, len(specs))
	for _, spec := range specs {
		specByProfile[spec.Profile] = spec
	}
	manifests, sequenceErr := executeFeatureSequence(
		*runID, *feature, *environment, commits, specs,
		func(spec featureRunSpec) (featureEvidenceManifest, error) {
			return executeFeatureSpec(workspace, envRoot, *runID, *environment, spec, commits)
		},
		func(manifest featureEvidenceManifest) error {
			return writeFeatureStageReports(filepath.Join(runRoot, manifest.Profile), specByProfile[manifest.Profile], manifest)
		},
	)
	combined := combineFeatureManifests(*runID, *feature, *profile, *environment, commits, manifests)
	if err := writeFeatureQualificationReports(runRoot, combined); err != nil {
		return err
	}
	if sequenceErr != nil {
		return sequenceErr
	}
	if combined.Status != "PASS" {
		return fmt.Errorf("feature qualification %s: %s", combined.Status, combined.Assessment)
	}
	fmt.Fprintf(os.Stdout, "feature qualification PASS: %s %s (%s)\n", *feature, *profile, *runID)
	return nil
}

func executeFeatureSequence(
	runID, feature, environment string,
	commits map[string]string,
	specs []featureRunSpec,
	execute func(featureRunSpec) (featureEvidenceManifest, error),
	record func(featureEvidenceManifest) error,
) ([]featureEvidenceManifest, error) {
	manifests := make([]featureEvidenceManifest, 0, len(specs))
	for index, spec := range specs {
		manifest, runErr := execute(spec)
		manifests = append(manifests, manifest)
		if err := record(manifest); err != nil {
			return manifests, err
		}
		if runErr == nil && manifest.Status == "PASS" {
			continue
		}
		reason := fmt.Sprintf("%s did not pass; later qualification stages were not started", spec.Profile)
		for _, remaining := range specs[index+1:] {
			notRun := notRunFeatureManifest(runID, feature, remaining.Profile, environment, commits, []featureRunSpec{remaining}, reason)
			manifests = append(manifests, notRun)
			if err := record(notRun); err != nil {
				return manifests, err
			}
		}
		if runErr != nil {
			return manifests, runErr
		}
		return manifests, fmt.Errorf("feature qualification %s: %s", manifest.Status, manifest.Assessment)
	}
	return manifests, nil
}

func regexpMatchRunID(value string) (bool, error) {
	return regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`, value)
}

func resolveFeatureSpecs(catalog testCatalog, feature, requestedProfile string) ([]featureRunSpec, error) {
	var canaryCases []testCatalogCase
	var qualification *testCatalogCase
	for i := range catalog.Cases {
		tc := catalog.Cases[i]
		if tc.Status != "active" || tc.Feature != feature || tc.Runner != "test-feature" {
			continue
		}
		if (tc.Layer == "e2e" || tc.Layer == "live") && tc.Profile == "canary" {
			canaryCases = append(canaryCases, tc)
		}
		if tc.Layer == "load" && tc.Profile == "qualification-1k" {
			copy := tc
			qualification = &copy
		}
	}
	if len(canaryCases) == 0 {
		return nil, fmt.Errorf("feature %q has no active canary cases", feature)
	}
	sort.Slice(canaryCases, func(i, j int) bool { return canaryCases[i].ID < canaryCases[j].ID })
	canary := featureRunSpec{
		Feature: feature, Profile: "canary", ScenarioPath: canaryCases[0].Source,
		Scale: featureScale(feature, "canary"), Purpose: "Prove feature correctness before allocating 1K load resources.",
		Method: canaryCases[0].Method, CaseMetadata: map[string]featureCaseMetadata{},
	}
	for _, tc := range canaryCases {
		if tc.Source != canary.ScenarioPath {
			return nil, fmt.Errorf("feature %s canary cases must share one scenario source", feature)
		}
		canary.TestIDs = append(canary.TestIDs, tc.ID)
		canary.CaseMetadata[tc.ID] = featureCaseMetadata{Purpose: tc.Title, Method: tc.Method}
	}
	if requestedProfile == "canary" {
		return []featureRunSpec{canary}, nil
	}
	if qualification == nil {
		return nil, fmt.Errorf("feature %q has no active qualification-1k case", feature)
	}
	load := featureRunSpec{
		Feature: feature, Profile: "qualification-1k", TestIDs: append([]string(nil), qualification.Covers...),
		LoadTestID: qualification.ID, ScenarioPath: qualification.Source, Scale: featureScale(feature, "qualification-1k"),
		Purpose: qualification.Title, Method: qualification.Method, CaseMetadata: map[string]featureCaseMetadata{
			qualification.ID: {Purpose: qualification.Title, Method: qualification.Method},
		},
	}
	for _, testID := range load.TestIDs {
		if tc, ok := catalogCaseByID(catalog.Cases, testID); ok {
			load.CaseMetadata[testID] = featureCaseMetadata{Purpose: tc.Title, Method: tc.Method}
		}
	}
	return []featureRunSpec{canary, load}, nil
}

func featureScale(feature, profile string) map[string]any {
	scales := map[string]map[string]map[string]any{
		"device-shadow": {
			"canary":           {"devices": 10, "users": 2},
			"qualification-1k": {"mqtt_devices": 1000, "users": 50},
		},
		"video-webrtc": {
			"canary":           {"home_devices": 2, "concurrent_h264_relay_sessions": 2},
			"qualification-1k": {"home_devices": 1000, "concurrent_h264_relay_sessions": 100},
		},
		"clip-storage": {
			"canary":           {"cameras": 2, "clips_per_camera": 2, "clip_uploads": 4},
			"qualification-1k": {"home_devices": 1000, "cameras": 100, "clips_per_camera": 10, "clip_uploads": 1000},
		},
	}
	if featureProfiles, ok := scales[feature]; ok {
		if scale, ok := featureProfiles[profile]; ok {
			return scale
		}
	}
	return map[string]any{}
}

func printFeaturePlan(runID, environment, envRoot string, specs []featureRunSpec, commits map[string]string) error {
	payload := map[string]any{
		"schema_version": "rtk-cloud-feature-plan/v1", "run_id": runID, "environment": environment,
		"deployment_env_root": envRoot, "load_env_root": featureLoadEnvRoot(envRoot),
		"stages": specs, "commits": commits,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(raw))
	return nil
}

func executeFeatureSpec(workspace, envRoot, runID, environment string, spec featureRunSpec, commits map[string]string) (featureEvidenceManifest, error) {
	started := time.Now().UTC()
	stageDir := filepath.Join(workspace, ".artifacts", "test-runs", runID, "features", spec.Feature, spec.Profile)
	if err := os.RemoveAll(stageDir); err != nil {
		return featureEvidenceManifest{}, err
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return featureEvidenceManifest{}, err
	}
	if err := validateFeaturePrerequisites(spec); err != nil {
		return blockedFeatureManifest(runID, spec.Feature, spec.Profile, environment, commits, []featureRunSpec{spec}, err.Error()), err
	}
	region := envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_REGION")
	if region == "" {
		return blockedFeatureManifest(
			runID, spec.Feature, spec.Profile, environment, commits, []featureRunSpec{spec},
			"CLOUD_REGION is missing from the deployment stack environment",
		), errors.New("CLOUD_REGION is missing from the deployment stack environment")
	}
	stageRunID := boundedFeatureStageRunID(runID, spec.Feature, spec.Profile)
	env := map[string]string{
		"HOME100K_DESCRIPTION_FILE":        filepath.Join(workspace, filepath.FromSlash(spec.ScenarioPath)),
		"HOME100K_ENV_ROOT":                featureExecutionLoadEnvRoot(envRoot),
		"HOME100K_REGION":                  region,
		"HOME100K_KUBECONFIG":              featureKubeconfigPath(envRoot),
		"HOME100K_DEVICE_CLIENT_CA_BUNDLE": featureDeviceCABundlePath(envRoot),
		"HOME100K_OUT_DIR":                 stageDir,
		"HOME100K_RUN_ID":                  stageRunID,
		"HOME100K_PRESERVE_VMS":            "0",
		"HOME100K_AUTO_DESTROY_ON_EXIT":    "1",
		"HOME100K_SHUTDOWN_ON_ERROR":       "1",
	}
	if spec.Profile == "qualification-1k" {
		brandPlan, planErr := featureQualificationBrandPlanPath(envRoot)
		if planErr != nil {
			return blockedFeatureManifest(
				runID, spec.Feature, spec.Profile, environment, commits, []featureRunSpec{spec},
				planErr.Error(),
			), planErr
		}
		env["HOME100K_BRAND_PLAN"] = brandPlan
	}
	if strings.TrimSpace(os.Getenv("HOME100K_BRANDNAME")) == "" {
		brandName, brandErr := featureRunScopedBrandName(envRoot)
		if brandErr != nil {
			return blockedFeatureManifest(
				runID, spec.Feature, spec.Profile, environment, commits, []featureRunSpec{spec},
				brandErr.Error(),
			), brandErr
		}
		if brandName != "" {
			env["HOME100K_BRANDNAME"] = brandName
		}
	}
	for key, value := range featureExternalEndpointEnv(envRoot) {
		env[key] = value
	}
	workflowCommand, workflowErr := featureWorkflowCommand()
	if workflowErr != nil {
		return blockedFeatureManifest(
			runID, spec.Feature, spec.Profile, environment, commits, []featureRunSpec{spec},
			workflowErr.Error(),
		), workflowErr
	}
	runErr := runCmdWithEnv(
		workspace,
		env,
		filepath.Join(workspace, "loadtests", "home-100k", "scripts", "home-100k.sh"),
		workflowCommand,
	)
	completed := time.Now().UTC()
	purgeFeatureSecretArtifacts(stageDir)
	_ = materializeRuntimeLogEvidence(stageDir)
	manifest := evaluateFeatureEvidence(stageDir, runID, environment, spec, commits, started, completed, runErr)
	return manifest, runErr
}

func featureExternalEndpointEnv(envRoot string) map[string]string {
	stackFile := filepath.Join(filepath.Clean(envRoot), "env", "stack.env")
	videoDomain := strings.TrimSpace(envFileValue(stackFile, "VIDEO_CLOUD_DOMAIN"))
	accountDomain := strings.TrimSpace(envFileValue(stackFile, "ACCOUNT_MANAGER_DOMAIN"))
	values := map[string]string{}
	if value := firstNonEmpty(os.Getenv("HOME100K_ACCOUNT_MANAGER_BASE_URL"), prefixedHTTPSOrigin(accountDomain)); value != "" {
		values["HOME100K_ACCOUNT_MANAGER_BASE_URL"] = value
	}
	if value := firstNonEmpty(os.Getenv("HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL"), prefixedHTTPSOrigin(videoDomain)); value != "" {
		values["HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL"] = value
	}
	if value := firstNonEmpty(os.Getenv("HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL"), prefixedHTTPSOrigin("device."+videoDomain)); value != "" && videoDomain != "" {
		values["HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL"] = value
	}
	if value := firstNonEmpty(os.Getenv("HOME100K_MQTT_ADDR"), mqttEndpoint(videoDomain)); value != "" {
		values["HOME100K_MQTT_ADDR"] = value
	}
	return values
}

func prefixedHTTPSOrigin(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	return "https://" + host
}

func mqttEndpoint(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	return host + ":8883"
}

func featureWorkflowCommand() (string, error) {
	command := firstNonEmpty(os.Getenv("RUNTIME_COVERAGE_FEATURE_WORKFLOW"), "workflow-live")
	if command != "workflow-live" && command != "workflow-local-live" {
		return "", fmt.Errorf("RUNTIME_COVERAGE_FEATURE_WORKFLOW must be workflow-live or workflow-local-live, got %q", command)
	}
	return command, nil
}

func featureLoadEnvRoot(deploymentEnvRoot string) string {
	return filepath.Clean(deploymentEnvRoot)
}

func featureExecutionLoadEnvRoot(deploymentEnvRoot string) string {
	return firstNonEmpty(os.Getenv("HOME100K_ENV_ROOT"), featureLoadEnvRoot(deploymentEnvRoot))
}

func featureQualificationBrandPlanPath(deploymentEnvRoot string) (string, error) {
	return featureResolvedBrandPlanPath(deploymentEnvRoot, true)
}

func featureResolvedBrandPlanPath(deploymentEnvRoot string, required bool) (string, error) {
	explicit := strings.TrimSpace(os.Getenv("HOME100K_BRAND_PLAN"))
	path := explicit
	if path == "" {
		path = filepath.Join(featureExecutionLoadEnvRoot(deploymentEnvRoot), "artifacts", "load-owner", "resolved-brand-plan.json")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) && !required && explicit == "" {
			return "", nil
		}
		if os.IsNotExist(err) && required {
			return "", fmt.Errorf("qualification-1k requires the run-scoped formal-owner brand plan: %s", path)
		}
		return "", fmt.Errorf("inspect run-scoped formal-owner brand plan %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("run-scoped formal-owner brand plan is not a regular file: %s", path)
	}
	return path, nil
}

func featureRunScopedBrandName(deploymentEnvRoot string) (string, error) {
	path, err := featureResolvedBrandPlanPath(deploymentEnvRoot, false)
	if err != nil || path == "" {
		return "", err
	}
	plan, err := loadLoadTestBrandPlan(path)
	if err != nil {
		return "", fmt.Errorf("load run-scoped formal-owner brand plan: %w", err)
	}
	return strings.TrimSpace(plan.Brands[0].Brandname), nil
}

func featureKubeconfigPath(deploymentEnvRoot string) string {
	return filepath.Join(filepath.Clean(deploymentEnvRoot), "state", "kubeconfig.yaml")
}

func featureDeviceCABundlePath(deploymentEnvRoot string) string {
	return filepath.Join(filepath.Clean(deploymentEnvRoot), "state", "pki", "device-client-ca-bundle.pem")
}

func boundedFeatureStageRunID(runID, feature, profile string) string {
	raw := strings.Join([]string{runID, strings.ReplaceAll(feature, "_", "-"), profile}, "-")
	if len(raw) <= 50 {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	prefix := strings.Trim(raw[:32], "-.")
	return prefix + "-" + hex.EncodeToString(sum[:8])
}

func evaluateFeatureEvidence(stageDir, runID, environment string, spec featureRunSpec, commits map[string]string, started, completed time.Time, runErr error) featureEvidenceManifest {
	manifest := featureEvidenceManifest{
		SchemaVersion: featureEvidenceSchema, RunID: runID, Feature: spec.Feature, Profile: spec.Profile,
		Environment: environment, StartedAt: started.Format(time.RFC3339Nano), CompletedAt: completed.Format(time.RFC3339Nano),
		DurationMS: completed.Sub(started).Milliseconds(), Commits: commits, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	resultsPath := filepath.Join(stageDir, "results.json")
	var results map[string]any
	resultsErr := readJSONFile(resultsPath, &results)
	status, assessment := classifyFeatureRun(spec, results, resultsErr, runErr)
	if evidenceErr := validateFeatureEvidenceFiles(stageDir, spec); evidenceErr != nil {
		status = "INCOMPLETE"
		assessment = evidenceErr.Error()
	}
	files := collectFeatureEvidenceFiles(stageDir)
	if secretFiles := findUnredactedFeatureEvidence(stageDir); len(secretFiles) > 0 {
		status = "INCOMPLETE"
		assessment = "unredacted credential-like content found in evidence: " + strings.Join(secretFiles, ", ")
	}
	operations, successRate, latency, exactCorrelation, caseAssessments := evaluateFeatureCases(spec, results, stageDir)
	actualScale := featureActualScale(spec, results, stageDir)
	for _, testID := range spec.TestIDs {
		purpose, method := featureCasePurposeMethod(spec, testID)
		caseStatus := status
		caseAssessment := assessment
		if value, ok := caseAssessments[testID]; ok && status == "PASS" {
			caseStatus, caseAssessment = value.Status, value.Assessment
		}
		manifest.Cases = append(manifest.Cases, featureCaseEvidence{
			TestID: testID, LoadTestID: spec.LoadTestID, Feature: spec.Feature, Profile: spec.Profile,
			Scale: spec.Scale, ActualScale: actualScale, Purpose: purpose, Method: method,
			StartedAt: manifest.StartedAt, CompletedAt: manifest.CompletedAt, DurationMS: manifest.DurationMS,
			Status: caseStatus, Assessment: caseAssessment, Operations: operations, SuccessRate: successRate,
			Latency: latency, ExactEventCorrelation: exactCorrelation, Evidence: files, Commits: commits, RunID: runID,
		})
		if caseStatus != "PASS" && status == "PASS" {
			status, assessment = caseStatus, caseAssessment
		}
	}
	if spec.LoadTestID != "" {
		purpose, method := featureCasePurposeMethod(spec, spec.LoadTestID)
		manifest.Cases = append(manifest.Cases, featureCaseEvidence{
			TestID: spec.LoadTestID, LoadTestID: spec.LoadTestID, Feature: spec.Feature, Profile: spec.Profile,
			Scale: spec.Scale, ActualScale: actualScale, Purpose: purpose, Method: method,
			StartedAt: manifest.StartedAt, CompletedAt: manifest.CompletedAt, DurationMS: manifest.DurationMS,
			Status: status, Assessment: assessment, Operations: operations, SuccessRate: successRate,
			Latency: latency, ExactEventCorrelation: exactCorrelation, Evidence: files, Commits: commits, RunID: runID,
		})
	}
	manifest.Status, manifest.Assessment = status, assessment
	return manifest
}

func validateFeatureEvidenceFiles(stageDir string, spec featureRunSpec) error {
	var serverEvidence map[string]any
	if err := readJSONFile(filepath.Join(stageDir, "server-evidence.json"), &serverEvidence); err != nil {
		return fmt.Errorf("server-evidence.json is missing or unreadable: %w", err)
	}
	if !boolAt(serverEvidence, "complete") {
		return errors.New("server-evidence.json is incomplete")
	}
	var runtimeEvidence map[string]any
	if err := readJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), &runtimeEvidence); err != nil {
		return fmt.Errorf("runtime-log-evidence.json is missing or unreadable: %w", err)
	}
	runtimeStatus := stringAt(runtimeEvidence, "status")
	if spec.Feature == "device-shadow" {
		if !featureEvidenceStatusComplete(runtimeStatus) {
			return errors.New("runtime-log-evidence.json is incomplete")
		}
	} else if runtimeStatus == "" {
		return errors.New("runtime-log-evidence.json is missing status")
	}
	return nil
}

func featureCasePurposeMethod(spec featureRunSpec, testID string) (string, string) {
	if metadata, ok := spec.CaseMetadata[testID]; ok {
		return metadata.Purpose, metadata.Method
	}
	return spec.Purpose, spec.Method
}

type featureAssessment struct {
	Status     string
	Assessment string
}

func classifyFeatureRun(spec featureRunSpec, results map[string]any, resultsErr, runErr error) (string, string) {
	if resultsErr != nil {
		return "INCOMPLETE", "results.json is missing or unreadable"
	}
	status := stringAt(results, "status")
	result := stringAt(results, "result")
	if status != "COMPLETE" {
		return "INCOMPLETE", "load runner did not produce COMPLETE evidence"
	}
	if !boolAt(results, "server_evidence", "complete") {
		return "INCOMPLETE", "server evidence is incomplete"
	}
	if spec.Feature != "video-webrtc" && spec.Feature != "clip-storage" &&
		(!featureEvidenceStatusComplete(stringAt(results, "server_correlation", "status")) ||
			!featureEvidenceStatusComplete(stringAt(results, "runtime_log_correlation", "status"))) {
		return "INCOMPLETE", "server or runtime-log correlation is incomplete"
	}
	if result != "SUCCESS" || runErr != nil {
		return "FAIL", "load runner completed but one or more functional or threshold gates failed"
	}
	return "PASS", "functional, completeness, correlation, and load gates passed"
}

func evaluateFeatureCases(spec featureRunSpec, results map[string]any, stageDir string) ([]string, float64, map[string]any, float64, map[string]featureAssessment) {
	assessments := map[string]featureAssessment{}
	operations := []string{}
	successRate := numberAt(results, "stage_results", 0, "mqtt_connect_success_rate_percent")
	exact := 0.0
	if featureEvidenceStatusComplete(stringAt(results, "runtime_log_correlation", "status")) &&
		numberAt(results, "runtime_log_correlation", "missing_stream_count") == 0 &&
		numberAt(results, "runtime_log_correlation", "missing_sequence_count") == 0 &&
		numberAt(results, "runtime_log_correlation", "schema_invalid_count") == 0 &&
		numberAt(results, "runtime_log_correlation", "tenant_mismatch_count") == 0 {
		exact = 100
	}
	latency := map[string]any{}
	switch spec.Feature {
	case "device-shadow":
		operations = []string{"desired_write", "delta_receive", "reported_publish", "delta_clear", "offline_reconnect", "version_conflict", "unauthorized_update", "aws_namespace_rejection"}
		latency["desired_reported_p95_ms"] = numberAt(results, "stage_results", 0, "desired_reported_p95_ms")
		onlineOK := numberAt(results, "stage_results", 0, "device_mqtt_totals", "delta_received") > 0 &&
			numberAt(results, "stage_results", 0, "device_mqtt_totals", "reported_publishes") > 0 &&
			numberAt(results, "stage_results", 0, "desired_reported_convergence_rate_percent") >= minimumSuccess(spec.Profile)
		offlineOK := numberAt(results, "stage_results", 0, "offline_desired_convergence_rate_percent") >= minimumSuccess(spec.Profile)
		policyOK := numberAt(results, "stage_results", 0, "version_conflict_count") > 0 &&
			numberAt(results, "stage_results", 0, "rejected_update_count") > 0 &&
			numberAt(results, "stage_results", 0, "unauthorized_rejection_count") > 0 &&
			numberAt(results, "stage_results", 0, "duplicate_suppression_count") > 0 &&
			numberAt(results, "stage_results", 0, "duplicate_apply_count") == 0 &&
			numberAt(results, "stage_results", 0, "authorization_violation_count") == 0
		awsNamespaceOK := numberAt(results, "stage_results", 0, "aws_namespace_rejection_count") > 0 &&
			numberAt(results, "stage_results", 0, "authorization_violation_count") == 0
		assessments["E2E-HOME-SHADOW-001"] = passFail(onlineOK, "online desired/delta/reported convergence")
		assessments["E2E-HOME-SHADOW-002"] = passFail(offlineOK, "offline desired convergence")
		assessments["E2E-HOME-SHADOW-003"] = passFail(policyOK, "version conflict and unauthorized update enforcement")
		assessments["LIVE-VC-SHADOWMQTT-001"] = passFail(awsNamespaceOK, "deployed broker rejects the AWS Shadow MQTT namespace")
	case "video-webrtc":
		operations = []string{}
		target := float64(2)
		if spec.Profile == "qualification-1k" {
			target = 100
		}
		create := numberAt(results, "video_evidence", "webrtc_totals", "create_success")
		setup := numberAt(results, "video_evidence", "webrtc_totals", "setup_success")
		closeCount := numberAt(results, "video_evidence", "webrtc_totals", "close_success")
		media := numberAt(results, "video_evidence", "webrtc_media_totals", "successes")
		successRate = numberAt(results, "video_evidence", "webrtc_totals", "success_rate_percent")
		latency["first_rtp_p95_ms"] = numberAt(results, "video_evidence", "webrtc_media_totals", "time_to_first_rtp_p95_ms")
		webrtcOK := boolAt(results, "video_evidence", "complete") && create >= target && setup >= target && closeCount >= target && media >= target
		if numberAt(results, "video_evidence", "turn_evidence", "api_turn_registry_lookup_succeeded") > 0 {
			operations = append(operations, "ice_preflight")
		}
		if create >= target {
			operations = append(operations, "webrtc_create")
		}
		if setup >= target {
			operations = append(operations, "webrtc_setup")
		}
		if media >= target {
			operations = append(operations, "first_h264_rtp")
		}
		if closeCount >= target {
			operations = append(operations, "webrtc_close")
		}
		turnOK := boolAt(results, "video_evidence", "turn_evidence", "registry_available") &&
			numberAt(results, "video_evidence", "turn_evidence", "active_nodes") > 0 &&
			numberAt(results, "video_evidence", "turn_evidence", "api_turn_registry_lookup_succeeded") > 0 &&
			numberAt(results, "video_evidence", "turn_evidence", "api_turn_registry_node_count") > 0 &&
			numberAt(results, "video_evidence", "turn_evidence", "api_dynamic_turn_count") > 0 &&
			numberAt(results, "video_evidence", "relay_candidate_samples") >= target &&
			numberAt(results, "video_evidence", "non_relay_candidate_samples") == 0
		if turnOK {
			operations = append(operations, "turn_node_registered", "turn_node_active", "dynamic_turn_resolve", "relay_candidate")
		}
		assessments["E2E-VC-WEBRTC-001"] = passFail(webrtcOK, "H264 create, media, first-RTP, and close evidence")
		assessments["E2E-VC-TURN-001"] = passFail(turnOK, "dynamic TURN registry and relay-only evidence")
	case "clip-storage":
		clipPath := filepath.Join(stageDir, "clip-storage", "load-results.json")
		var clip map[string]any
		clipErr := readJSONFile(clipPath, &clip)
		required := map[string]bool{
			"clip_upload": false, "clip_verify_ready": false, "clip_info": false, "clip_total": false,
			"clip_enum": false, "clip_download_range": false, "clip_download_invalid_range": false, "clip_delete": false,
		}
		allSuccess := clipErr == nil && boolAt(clip, "thresholds", "passed")
		for _, op := range sliceAt(clip, "operations") {
			name := stringAt(op, "name")
			if _, ok := required[name]; ok && boolAt(op, "success") {
				required[name] = true
			}
		}
		for name, ok := range required {
			operations = append(operations, name)
			allSuccess = allSuccess && ok
		}
		targetUploads := mapNumber(spec.Scale, "clip_uploads")
		targetCameras := mapNumber(spec.Scale, "cameras")
		allSuccess = allSuccess &&
			numberAt(clip, "clip_storage", "camera_devices") >= targetCameras &&
			numberAt(clip, "clip_storage", "upload_successes") >= targetUploads
		if spec.Profile == "qualification-1k" {
			allSuccess = allSuccess &&
				boolAt(clip, "clip_storage", "mixed_non_clip_enabled") &&
				numberAt(clip, "clip_storage", "non_clip_attempts") > 0
		}
		sort.Strings(operations)
		reconcilePath := filepath.Join(stageDir, "clip-storage", "reconciliation.json")
		if stat, err := os.Stat(reconcilePath); err != nil || stat.Size() == 0 {
			allSuccess = false
		}
		successRate = numberAt(clip, "clip_storage", "success_rate") * 100
		latency["upload_p95_ms"] = numberAt(clip, "clip_storage", "p95_latency_ms")
		assessments["E2E-VC-CLIP-001"] = passFail(allSuccess, "clip upload, readiness, playback, delete, and reconciliation evidence")
	}
	return operations, successRate, latency, exact, assessments
}

func featureEvidenceStatusComplete(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pass", "complete":
		return true
	default:
		return false
	}
}

func featureActualScale(spec featureRunSpec, results map[string]any, stageDir string) map[string]any {
	actual := map[string]any{
		"connected_home_devices": numberAt(results, "stage_results", 0, "connected_devices"),
	}
	switch spec.Feature {
	case "device-shadow":
		actual["mqtt_connect_successes"] = numberAt(results, "stage_results", 0, "device_mqtt_totals", "connect_success")
	case "video-webrtc":
		actual["webrtc_create_successes"] = numberAt(results, "video_evidence", "webrtc_totals", "create_success")
		actual["h264_media_successes"] = numberAt(results, "video_evidence", "webrtc_media_totals", "successes")
		actual["relay_candidate_samples"] = numberAt(results, "video_evidence", "relay_candidate_samples")
	case "clip-storage":
		var clip map[string]any
		if readJSONFile(filepath.Join(stageDir, "clip-storage", "load-results.json"), &clip) == nil {
			actual["camera_devices"] = numberAt(clip, "clip_storage", "camera_devices")
			actual["upload_attempts"] = numberAt(clip, "clip_storage", "upload_attempts")
			actual["upload_successes"] = numberAt(clip, "clip_storage", "upload_successes")
			actual["mixed_control_attempts"] = numberAt(clip, "clip_storage", "non_clip_attempts")
		}
	}
	return actual
}

func mapNumber(values map[string]any, key string) float64 {
	switch value := values[key].(type) {
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case float64:
		return value
	default:
		return 0
	}
}

func minimumSuccess(profile string) float64 {
	if profile == "canary" {
		return 100
	}
	return 99.5
}

func passFail(ok bool, subject string) featureAssessment {
	if ok {
		return featureAssessment{Status: "PASS", Assessment: subject + " passed"}
	}
	return featureAssessment{Status: "FAIL", Assessment: subject + " is missing or below threshold"}
}

func combineFeatureManifests(runID, feature, profile, environment string, commits map[string]string, manifests []featureEvidenceManifest) featureEvidenceManifest {
	combined := featureEvidenceManifest{
		SchemaVersion: featureEvidenceSchema, RunID: runID, Feature: feature, Profile: profile,
		Environment: environment, Status: "PASS", Assessment: "all feature qualification stages passed",
		Commits: commits, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	for i, manifest := range manifests {
		if i == 0 {
			combined.StartedAt = manifest.StartedAt
		}
		combined.CompletedAt = manifest.CompletedAt
		combined.DurationMS += manifest.DurationMS
		combined.Cases = append(combined.Cases, manifest.Cases...)
		if manifest.Status != "PASS" && combined.Status == "PASS" {
			combined.Status, combined.Assessment = manifest.Status, manifest.Assessment
		}
	}
	return combined
}

func blockedFeatureManifest(runID, feature, profile, environment string, commits map[string]string, specs []featureRunSpec, reason string) featureEvidenceManifest {
	return nonPassingFeatureManifest(runID, feature, profile, environment, commits, specs, "BLOCKED", reason)
}

func incompleteFeatureManifest(runID, feature, profile, environment string, commits map[string]string, specs []featureRunSpec, reason string) featureEvidenceManifest {
	return nonPassingFeatureManifest(runID, feature, profile, environment, commits, specs, "INCOMPLETE", reason)
}

func notRunFeatureManifest(runID, feature, profile, environment string, commits map[string]string, specs []featureRunSpec, reason string) featureEvidenceManifest {
	return nonPassingFeatureManifest(runID, feature, profile, environment, commits, specs, "NOT_RUN", reason)
}

func nonPassingFeatureManifest(runID, feature, profile, environment string, commits map[string]string, specs []featureRunSpec, status, reason string) featureEvidenceManifest {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	manifest := featureEvidenceManifest{
		SchemaVersion: featureEvidenceSchema, RunID: runID, Feature: feature, Profile: profile,
		Environment: environment, Status: status, Assessment: reason, StartedAt: now, CompletedAt: now,
		Commits: commits, GeneratedAt: now,
	}
	for _, spec := range specs {
		ids := append([]string{}, spec.TestIDs...)
		if spec.LoadTestID != "" {
			ids = append(ids, spec.LoadTestID)
		}
		for _, testID := range ids {
			purpose, method := featureCasePurposeMethod(spec, testID)
			manifest.Cases = append(manifest.Cases, featureCaseEvidence{
				TestID: testID, LoadTestID: spec.LoadTestID, Feature: spec.Feature, Profile: spec.Profile,
				Scale: spec.Scale, ActualScale: map[string]any{}, Purpose: purpose, Method: method,
				StartedAt: now, CompletedAt: now, Status: status, Assessment: reason,
				Commits: commits, RunID: runID,
			})
		}
	}
	return manifest
}

func validateFeaturePrerequisites(spec featureRunSpec) error {
	if spec.Feature != "clip-storage" || spec.Profile != "qualification-1k" {
		return nil
	}
	if strings.TrimSpace(os.Getenv("VIDEO_CLOUD_LOAD_ADMIN_TOKEN")) == "" {
		return errors.New("VIDEO_CLOUD_LOAD_ADMIN_TOKEN is required for mixed Clip qualification traffic")
	}
	for _, name := range []string{"VIDEO_CLOUD_LOAD_CLIP_USER_PRIVATE_KEY", "VIDEO_CLOUD_LOAD_CLIP_SERVER_PUBLIC_KEY"} {
		path := strings.TrimSpace(os.Getenv(name))
		if path == "" {
			return fmt.Errorf("%s is required for Clip download/decryption evidence", name)
		}
		if stat, err := os.Stat(path); err != nil || stat.IsDir() {
			return fmt.Errorf("%s does not point to a readable file", name)
		}
	}
	return nil
}

func writeFeatureStageReports(dir string, spec featureRunSpec, manifest featureEvidenceManifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSONFileIfMissing(filepath.Join(dir, "plan.json"), map[string]any{
		"schema_version": "rtk-cloud-feature-stage-plan/v1",
		"run_id":         manifest.RunID,
		"feature":        manifest.Feature,
		"profile":        manifest.Profile,
		"scenario_path":  spec.ScenarioPath,
		"test_ids":       spec.TestIDs,
		"load_test_id":   spec.LoadTestID,
		"scale":          spec.Scale,
		"purpose":        spec.Purpose,
		"method":         spec.Method,
		"status":         manifest.Status,
	}); err != nil {
		return err
	}
	if err := writeJSONFileIfMissing(filepath.Join(dir, "results.json"), map[string]any{
		"schema_version": "rtk-cloud-feature-stage-result/v1",
		"run_id":         manifest.RunID,
		"feature":        manifest.Feature,
		"profile":        manifest.Profile,
		"status":         manifest.Status,
		"assessment":     manifest.Assessment,
		"started_at":     manifest.StartedAt,
		"completed_at":   manifest.CompletedAt,
		"duration_ms":    manifest.DurationMS,
		"cases":          manifest.Cases,
	}); err != nil {
		return err
	}
	report := renderFeatureReport(manifest)
	if err := os.WriteFile(filepath.Join(dir, "test_report.md"), report, 0o644); err != nil {
		return err
	}
	junit := renderFeatureStageJUnit(manifest)
	if err := os.WriteFile(filepath.Join(dir, "junit.xml"), junit, 0o644); err != nil {
		return err
	}
	stableReports := []struct {
		path string
		raw  []byte
	}{
		{path: "test_report.md", raw: report},
		{path: "junit.xml", raw: junit},
	}
	for caseIndex := range manifest.Cases {
		for _, item := range stableReports {
			sum := sha256.Sum256(item.raw)
			manifest.Cases[caseIndex].Evidence = append(manifest.Cases[caseIndex].Evidence, featureEvidenceFile{
				Path: item.path, SHA256: hex.EncodeToString(sum[:]),
			})
		}
	}
	if err := writeJSONFile(filepath.Join(dir, "evidence-manifest.json"), manifest); err != nil {
		return err
	}
	if err := writeNormalizedFeatureEvidence(dir, manifest); err != nil {
		return err
	}
	return nil
}

func renderFeatureStageJUnit(manifest featureEvidenceManifest) []byte {
	failures := 0
	var cases strings.Builder
	for _, item := range manifest.Cases {
		status := strings.ToUpper(strings.TrimSpace(item.Status))
		fmt.Fprintf(&cases, `<testcase classname="feature-%s" name="%s" time="%.3f">`,
			html.EscapeString(manifest.Feature), html.EscapeString(item.TestID), float64(item.DurationMS)/1000)
		if status != "PASS" {
			failures++
			fmt.Fprintf(&cases, `<failure message="%s"/>`, html.EscapeString(item.Assessment))
		}
		fmt.Fprint(&cases, `</testcase>`)
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><testsuite name="feature-%s" tests="%d" failures="%d" time="%.3f">%s</testsuite>`+"\n",
		html.EscapeString(manifest.Feature), len(manifest.Cases), failures, float64(manifest.DurationMS)/1000, cases.String()))
}

func writeNormalizedFeatureEvidence(dir string, manifest featureEvidenceManifest) error {
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	catalog, err := loadAndValidateTestCatalogForRunner(workspace, "test-feature")
	if err != nil {
		return err
	}
	out := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV3,
		RunID:         manifest.RunID,
		GeneratedAt:   manifest.GeneratedAt,
	}
	out.SpecCommit, err = currentCanonicalSpecCommit(workspace)
	if err != nil {
		return err
	}
	requirements := catalogRequirementIndex(catalog)
	inventory, err := loadAvailableSpecInventory(workspace)
	if err != nil {
		return err
	}
	for _, item := range manifest.Cases {
		tc, ok := catalogCaseByID(catalog.Cases, item.TestID)
		if !ok {
			continue
		}
		refs := make([]featureCoverageEvidenceFile, 0, len(item.Evidence))
		for _, ref := range item.Evidence {
			refs = append(refs, featureCoverageEvidenceFile{Path: ref.Path, SHA256: ref.SHA256, Type: featureEvidenceType(ref.Path)})
		}
		status := normalizeFeatureEvidenceStatus(item.Status)
		assertions := make([]featureRequirementAssertion, 0, len(tc.Verifies))
		for _, requirementID := range tc.Verifies {
			requirement := requirements[requirementID]
			assertions = append(assertions, featureRequirementAssertion{
				RequirementID: requirementID,
				Revision:      requirement.Revision,
				SpecSource:    requirement.SpecSource,
				Status:        status,
				Assessment:    item.Assessment,
				Assertions:    map[string]string{"case_assessment": status},
				Evidence:      refs,
			})
		}
		workflowAssertions := normalizedFeatureWorkflowAssertions(item, tc, inventory)
		out.Cases = append(out.Cases, featureCaseEvidenceV2{
			TestID:          item.TestID,
			Status:          status,
			Assessment:      item.Assessment,
			Environment:     manifest.Environment,
			StartedAt:       item.StartedAt,
			CompletedAt:     item.CompletedAt,
			WorkspaceCommit: item.Commits["workspace"],
			Commits:         item.Commits,
			Requirements:    assertions,
			Workflows:       workflowAssertions,
		})
	}
	return writeJSONFile(filepath.Join(dir, "feature-evidence.json"), out)
}

func normalizedFeatureWorkflowAssertions(item featureCaseEvidence, tc testCatalogCase, inventory specInventory) []featureWorkflowAssertion {
	if strings.ToUpper(item.Status) != "PASS" {
		return nil
	}
	stepOperations := map[string]map[string]string{
		"WF-HOME-SHADOW-001": {
			"update_shadow": "desired_write", "read_converged_shadow": "reported_publish", "reject_stale_version": "version_conflict",
		},
		"WF-VC-WEBRTC-001": {
			"preflight_ice": "ice_preflight", "request_stream": "webrtc_create",
			"wait_for_answer": "webrtc_setup", "close_stream": "webrtc_close",
		},
		"WF-VC-TURN-001": {
			"register_node": "turn_node_registered", "heartbeat_node": "turn_node_active",
			"discover_active_node": "dynamic_turn_resolve",
		},
		"WF-VC-CLIP-001": {
			"create_upload": "clip_upload", "complete_upload": "clip_upload", "wait_until_ready": "clip_verify_ready",
			"enumerate_clip": "clip_enum", "download_clip": "clip_download_range", "delete_clip": "clip_delete",
		},
	}
	observed := map[string]bool{}
	for _, operation := range item.Operations {
		observed[operation] = true
	}
	var assertions []featureWorkflowAssertion
	for _, workflow := range inventory.Workflows {
		bound := false
		for _, requirementID := range workflow.RequirementIDs {
			bound = bound || catalogContainsString(tc.Verifies, requirementID)
		}
		mapping, supported := stepOperations[workflow.ID]
		if !bound || !supported {
			continue
		}
		statuses := map[string]string{}
		for _, step := range workflow.Steps {
			if observed[mapping[step.ID]] {
				statuses[step.ID] = "PASS"
			}
		}
		if assertion, err := buildWorkflowAssertion(workflow, statuses); err == nil {
			assertions = append(assertions, assertion)
		}
	}
	return assertions
}

func writeJSONFileIfMissing(path string, value any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeJSONFile(path, value)
}

func writeFeatureQualificationReports(dir string, manifest featureEvidenceManifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, "qualification-report.json"), manifest); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "qualification-report.md"), renderFeatureReport(manifest), 0o644)
}

func renderFeatureReport(manifest featureEvidenceManifest) []byte {
	var b strings.Builder
	fmt.Fprintln(&b, "# Feature Qualification Report")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Run ID: `%s`\n- Feature: `%s`\n- Profile: `%s`\n- Environment: `%s`\n- Started: %s\n- Completed: %s\n- Duration: %d ms\n- Status: **%s**\n- Assessment: %s\n\n",
		manifest.RunID, manifest.Feature, manifest.Profile, manifest.Environment, manifest.StartedAt, manifest.CompletedAt, manifest.DurationMS, manifest.Status, manifest.Assessment)
	fmt.Fprintln(&b, "| Test ID | Load Test ID | Profile | Purpose | Method | Target Scale | Actual Scale | Duration | Result | Assessment |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | ---: | --- | --- |")
	for _, result := range manifest.Cases {
		fmt.Fprintf(&b, "| `%s` | %s | `%s` | %s | %s | `%s` | `%s` | %d ms | **%s** | %s |\n",
			result.TestID, catalogOptionalCode(result.LoadTestID), result.Profile, escapeMarkdownCell(result.Purpose),
			escapeMarkdownCell(result.Method), compactJSON(result.Scale), compactJSON(result.ActualScale),
			result.DurationMS, result.Status, escapeMarkdownCell(result.Assessment))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Commit Anchors")
	for _, key := range sortedMapKeys(manifest.Commits) {
		fmt.Fprintf(&b, "- %s: `%s`\n", key, manifest.Commits[key])
	}
	return []byte(b.String())
}

func compactJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return strings.ReplaceAll(string(raw), "|", `\|`)
}

func collectFeatureEvidenceFiles(dir string) []featureEvidenceFile {
	var out []featureEvidenceFile
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		// The stage report is rendered only after the manifest is written. Including
		// it here would record the hash of the previous report and make the final
		// artifact unverifiable.
		if filepath.Base(rel) == "test_report.md" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(raw)
		out = append(out, featureEvidenceFile{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(sum[:])})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func materializeRuntimeLogEvidence(stageDir string) error {
	var results map[string]any
	if err := readJSONFile(filepath.Join(stageDir, "results.json"), &results); err != nil {
		return err
	}
	correlation, ok := results["runtime_log_correlation"]
	if !ok {
		return errors.New("results.json is missing runtime_log_correlation")
	}
	return writeJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), correlation)
}

func findUnredactedFeatureEvidence(dir string) []string {
	privateKeyPattern := regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	bearerPattern := regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*bearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	cookiePattern := regexp.MustCompile(`(?i)\b(?:set-cookie|cookie)\s*[:=]\s*[^\s"']{8,}`)
	jsonSecretPattern := regexp.MustCompile(`(?i)"(?:access_token|refresh_token|client_auth_token|token|password|credential|private_key)"\s*:\s*"([^"]+)"`)
	var matches []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".json", ".md", ".log", ".txt", ".env", ".xml":
		default:
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		found := privateKeyPattern.Match(raw) || bearerPattern.Match(raw) || cookiePattern.Match(raw)
		for _, match := range jsonSecretPattern.FindAllSubmatch(raw, -1) {
			value := strings.ToLower(strings.TrimSpace(string(match[1])))
			safePlaceholder := value == "redacted" || value == "[redacted]" || value == "<redacted>" ||
				value == "***" || strings.HasPrefix(value, "${")
			if value != "" && !safePlaceholder {
				found = true
			}
		}
		if found {
			if rel, relErr := filepath.Rel(dir, path); relErr == nil {
				matches = append(matches, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(matches)
	return matches
}

func purgeFeatureSecretArtifacts(dir string) {
	sensitiveNames := map[string]bool{
		"token-env.sh":          true,
		"device-token-map.json": true,
		"app-token-map.json":    true,
	}
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		name := strings.ToLower(entry.Name())
		if sensitiveNames[name] || strings.HasSuffix(name, "-private-key.pem") {
			_ = os.Remove(path)
		}
		return nil
	})
}

func featureCommitAnchors(workspace string) (map[string]string, error) {
	paths := map[string]string{
		"workspace": workspace, "video_cloud": filepath.Join(workspace, "repos", "rtk_video_cloud"),
		"account_manager": filepath.Join(workspace, "repos", "rtk_account_manager"),
		"cloud_logger":    filepath.Join(workspace, "repos", "rtk_cloud_logger"),
		"contracts":       filepath.Join(workspace, "repos", "rtk_cloud_contracts_doc"),
	}
	out := map[string]string{}
	for key, path := range paths {
		commit, err := gitOutput(path, "rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("%s commit: %w", key, err)
		}
		out[key] = strings.TrimSpace(commit)
	}
	return out, nil
}

func verifyFeatureDeploymentCommits(envRoot string, commits map[string]string) error {
	path := filepath.Join(envRoot, "artifacts", "lke-images", "lke-image-manifest.json")
	var manifest struct {
		Images []struct {
			SourcePath   string `json:"source_path"`
			SourceCommit string `json:"source_commit"`
			Image        string `json:"image"`
			Digest       string `json:"digest"`
		} `json:"images"`
	}
	if err := readJSONFile(path, &manifest); err != nil {
		return fmt.Errorf("read deployed image manifest: %w", err)
	}
	required := map[string]string{
		"repos/rtk_video_cloud":     commits["video_cloud"],
		"repos/rtk_account_manager": commits["account_manager"],
		"repos/rtk_cloud_logger":    commits["cloud_logger"],
	}
	for sourcePath, wanted := range required {
		found := false
		for _, image := range manifest.Images {
			if image.SourcePath != sourcePath {
				continue
			}
			found = true
			if image.SourceCommit != wanted {
				return fmt.Errorf("%s deployed commit %s does not match workspace %s", sourcePath, image.SourceCommit, wanted)
			}
			if image.Image == "" {
				return fmt.Errorf("%s deployed image is missing", sourcePath)
			}
			if !strings.HasPrefix(image.Digest, "sha256:") || len(image.Digest) <= len("sha256:") {
				return fmt.Errorf("%s deployed image digest is missing or invalid", sourcePath)
			}
		}
		if !found {
			return fmt.Errorf("%s is missing from deployed image manifest", sourcePath)
		}
	}
	return nil
}

func runTestFeatureSelect(args []string) error {
	fs := flag.NewFlagSet("test-feature select", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseRef := fs.String("base-ref", "", "base git revision")
	headRef := fs.String("head-ref", "HEAD", "head git revision")
	labels := fs.String("labels", "", "comma-separated PR labels")
	output := fs.String("output", "", "optional JSON output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *baseRef == "" {
		return errors.New("--base-ref is required")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	diff, err := gitOutput(workspace, "diff", "--name-only", *baseRef, *headRef)
	if err != nil {
		return err
	}
	selection, err := selectFeatureQualifications(catalog, strings.Fields(diff), splitCSV(*labels))
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if *output != "" {
		return writeFeatureSelectionFile(*output, raw)
	}
	_, err = os.Stdout.Write(raw)
	return err
}

func writeFeatureSelectionFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func selectFeatureQualifications(catalog testCatalog, changedFiles, labels []string) (featureSelection, error) {
	selection := featureSelection{
		SchemaVersion: "rtk-cloud-feature-selection/v1", MatchedPaths: map[string][]string{}, Labels: labels,
	}
	selected := map[string]bool{}
	sharedPatterns := []string{
		"scripts/go/rtk-cloud/**",
		"loadtests/home-100k/**",
		"e2e_test/video_cloud/load/**",
		"repos/rtk_video_cloud",
		"repos/rtk_account_manager",
		"repos/rtk_cloud_logger",
		"repos/rtk_cloud_contracts_doc",
		"repos/rtk_cloud_contracts_doc/**",
		"cloud_deploy/**",
		"cloud_env/*/deployment.env",
		"cloud_env/*/environment.env",
		"cloud_env/*/overrides/**",
		"cloud_env/*/runtime/**",
		".github/workflows/feature-qualification.yml",
	}
	for _, pattern := range sharedPatterns {
		re, err := catalogGlobRegexp(pattern)
		if err != nil {
			return selection, err
		}
		for _, changed := range changedFiles {
			if !re.MatchString(filepath.ToSlash(changed)) {
				continue
			}
			for _, feature := range []string{"device-shadow", "video-webrtc", "clip-storage"} {
				selected[feature] = true
				selection.MatchedPaths[feature] = appendUnique(selection.MatchedPaths[feature], changed)
			}
		}
	}
	for _, tc := range catalog.Cases {
		if tc.Status != "active" || tc.Layer != "load" || tc.Profile != "qualification-1k" {
			continue
		}
		for _, pattern := range tc.ChangePaths {
			re, err := catalogGlobRegexp(pattern)
			if err != nil {
				return selection, err
			}
			for _, changed := range changedFiles {
				if re.MatchString(filepath.ToSlash(changed)) {
					selected[tc.Feature] = true
					selection.MatchedPaths[tc.Feature] = appendUnique(selection.MatchedPaths[tc.Feature], changed)
				}
			}
		}
	}
	for _, label := range labels {
		switch label {
		case "qualification/all-1k":
			selected["device-shadow"], selected["video-webrtc"], selected["clip-storage"] = true, true, true
		case "qualification/device-shadow", "qualification/video-webrtc", "qualification/clip-storage":
			selected[strings.TrimPrefix(label, "qualification/")] = true
		}
	}
	for feature := range selected {
		selection.Features = append(selection.Features, feature)
	}
	sort.Strings(selection.Features)
	selection.Required = len(selection.Features) > 0
	for feature := range selection.MatchedPaths {
		sort.Strings(selection.MatchedPaths[feature])
	}
	return selection, nil
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func stringAt(value any, path ...any) string {
	current := jsonAt(value, path...)
	text, _ := current.(string)
	return text
}

func boolAt(value any, path ...any) bool {
	current := jsonAt(value, path...)
	result, _ := current.(bool)
	return result
}

func numberAt(value any, path ...any) float64 {
	current := jsonAt(value, path...)
	switch number := current.(type) {
	case float64:
		return number
	case json.Number:
		value, _ := number.Float64()
		return value
	default:
		return 0
	}
}

func sliceAt(value any, path ...any) []any {
	current := jsonAt(value, path...)
	result, _ := current.([]any)
	return result
}

func jsonAt(value any, path ...any) any {
	current := value
	for _, item := range path {
		switch key := item.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil
			}
			current = object[key]
		case int:
			array, ok := current.([]any)
			if !ok || key < 0 || key >= len(array) {
				return nil
			}
			current = array[key]
		default:
			return nil
		}
	}
	return current
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
