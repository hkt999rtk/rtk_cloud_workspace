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
	envRootFlag := fs.String("env-root", "cloud_env/staging/lke", "staging environment root")
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
		manifest := incompleteFeatureManifest(*runID, *feature, *profile, *environment, commits, specs, err.Error())
		_ = writeFeatureQualificationReports(runRoot, manifest)
		return fmt.Errorf("feature qualification INCOMPLETE: %w", err)
	}

	manifests, sequenceErr := executeFeatureSequence(
		*runID, *feature, *environment, commits, specs,
		func(spec featureRunSpec) (featureEvidenceManifest, error) {
			return executeFeatureSpec(workspace, envRoot, *runID, *environment, spec, commits)
		},
		func(manifest featureEvidenceManifest) error {
			return writeFeatureStageReports(filepath.Join(runRoot, manifest.Profile), manifest)
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
		if tc.Layer == "e2e" && tc.Profile == "canary" {
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
	stageRunID := boundedFeatureStageRunID(runID, spec.Feature, spec.Profile)
	env := map[string]string{
		"HOME100K_DESCRIPTION_FILE":        filepath.Join(workspace, filepath.FromSlash(spec.ScenarioPath)),
		"HOME100K_ENV_ROOT":                featureLoadEnvRoot(envRoot),
		"HOME100K_KUBECONFIG":              featureKubeconfigPath(envRoot),
		"HOME100K_DEVICE_CLIENT_CA_BUNDLE": featureDeviceCABundlePath(envRoot),
		"HOME100K_OUT_DIR":                 stageDir,
		"HOME100K_RUN_ID":                  stageRunID,
		"HOME100K_PRESERVE_VMS":            "0",
		"HOME100K_AUTO_DESTROY_ON_EXIT":    "1",
	}
	runErr := runCmdWithEnv(workspace, env, filepath.Join(workspace, "loadtests", "home-100k", "scripts", "home-100k.sh"), "workflow-live")
	completed := time.Now().UTC()
	purgeFeatureSecretArtifacts(stageDir)
	_ = materializeRuntimeLogEvidence(stageDir)
	manifest := evaluateFeatureEvidence(stageDir, runID, environment, spec, commits, started, completed, runErr)
	return manifest, runErr
}

func featureLoadEnvRoot(deploymentEnvRoot string) string {
	if filepath.Base(filepath.Clean(deploymentEnvRoot)) == "lke" {
		return filepath.Join(filepath.Dir(filepath.Clean(deploymentEnvRoot)), "runtime")
	}
	return deploymentEnvRoot
}

func featureKubeconfigPath(deploymentEnvRoot string) string {
	return filepath.Join(filepath.Clean(deploymentEnvRoot), "state", "kubeconfig.yaml")
}

func featureDeviceCABundlePath(deploymentEnvRoot string) string {
	return filepath.Join(filepath.Clean(deploymentEnvRoot), "state", "secrets", "device-client-ca-bundle.pem")
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
		operations = []string{"desired_write", "delta_receive", "reported_publish", "delta_clear", "offline_reconnect", "version_conflict", "unauthorized_update"}
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
		assessments["E2E-HOME-SHADOW-001"] = passFail(onlineOK, "online desired/delta/reported convergence")
		assessments["E2E-HOME-SHADOW-002"] = passFail(offlineOK, "offline desired convergence")
		assessments["E2E-HOME-SHADOW-003"] = passFail(policyOK, "version conflict and unauthorized update enforcement")
	case "video-webrtc":
		operations = []string{"webrtc_create", "webrtc_setup", "first_h264_rtp", "webrtc_close", "dynamic_turn_resolve", "relay_candidate"}
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
		turnOK := numberAt(results, "video_evidence", "turn_evidence", "api_dynamic_turn_count") > 0 &&
			numberAt(results, "video_evidence", "relay_candidate_samples") >= target &&
			numberAt(results, "video_evidence", "non_relay_candidate_samples") == 0
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
		successRate = numberAt(clip, "clip_storage", "success_rate")
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

func writeFeatureStageReports(dir string, manifest featureEvidenceManifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dir, "evidence-manifest.json"), manifest); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "TEST_REPORT.md"), renderFeatureReport(manifest), 0o644)
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
			if value != "" && !strings.Contains(value, "redact") && !strings.Contains(value, "***") &&
				!strings.HasPrefix(value, "${") && !strings.HasPrefix(value, "<") {
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
		"tests/catalog.yaml",
		"scripts/go/rtk-cloud/**",
		"loadtests/home-100k/**",
		"e2e_test/video_cloud/load/**",
		"repos/rtk_video_cloud",
		"repos/rtk_account_manager",
		"repos/rtk_cloud_contracts_doc",
		"repos/rtk_cloud_contracts_doc/**",
		"cloud_env/staging/lke/**",
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
