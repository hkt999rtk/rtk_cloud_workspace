package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunTestFeatureValidatesPublicArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"missing feature":     nil,
		"invalid profile":     {"--feature", "device-shadow", "--profile", "invalid"},
		"invalid environment": {"--feature", "device-shadow", "--environment", "production"},
		"conflicting modes":   {"--feature", "device-shadow", "--plan", "--run"},
		"invalid run ID":      {"--feature", "device-shadow", "--run-id", "not allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := runTestFeature(args); err == nil {
				t.Fatalf("runTestFeature(%v) unexpectedly passed", args)
			}
		})
	}
}

func TestRunTestFeaturePlanUsesCatalogAndCommitAnchors(t *testing.T) {
	if err := runTestFeature([]string{
		"--feature", "device-shadow",
		"--profile", "canary",
		"--environment", "staging",
		"--run-id", "unit-plan",
		"--plan",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunTestFeatureSelectWritesSelectionReport(t *testing.T) {
	output := filepath.Join(t.TempDir(), "selection", "result.json")
	if err := runTestFeature([]string{
		"select",
		"--base-ref", "HEAD",
		"--head-ref", "HEAD",
		"--labels", "qualification/device-shadow",
		"--output", output,
	}); err != nil {
		t.Fatal(err)
	}
	var selection featureSelection
	if err := readJSONFile(output, &selection); err != nil {
		t.Fatal(err)
	}
	if !selection.Required || !reflect.DeepEqual(selection.Features, []string{"device-shadow"}) {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestFeatureActualScaleMapsEveryManagedFeature(t *testing.T) {
	results := map[string]any{
		"stage_results": []any{map[string]any{
			"connected_devices":  1000.0,
			"device_mqtt_totals": map[string]any{"connect_success": 999.0},
		}},
		"video_evidence": map[string]any{
			"webrtc_totals":           map[string]any{"create_success": 100.0},
			"webrtc_media_totals":     map[string]any{"successes": 99.0},
			"relay_candidate_samples": 100.0,
		},
	}
	shadow := featureActualScale(featureRunSpec{Feature: "device-shadow"}, results, t.TempDir())
	if shadow["connected_home_devices"] != 1000.0 || shadow["mqtt_connect_successes"] != 999.0 {
		t.Fatalf("shadow actual scale = %#v", shadow)
	}
	video := featureActualScale(featureRunSpec{Feature: "video-webrtc"}, results, t.TempDir())
	if video["webrtc_create_successes"] != 100.0 || video["h264_media_successes"] != 99.0 || video["relay_candidate_samples"] != 100.0 {
		t.Fatalf("video actual scale = %#v", video)
	}
	clipDir := t.TempDir()
	clipResults := map[string]any{"clip_storage": map[string]any{
		"camera_devices": 100.0, "upload_attempts": 1000.0, "upload_successes": 1000.0, "non_clip_attempts": 50.0,
	}}
	if err := writeJSONFile(filepath.Join(clipDir, "clip-storage", "load-results.json"), clipResults); err == nil {
		t.Fatal("writeJSONFile unexpectedly created a missing parent directory")
	}
	if err := os.MkdirAll(filepath.Join(clipDir, "clip-storage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(clipDir, "clip-storage", "load-results.json"), clipResults); err != nil {
		t.Fatal(err)
	}
	clip := featureActualScale(featureRunSpec{Feature: "clip-storage"}, results, clipDir)
	if clip["camera_devices"] != 100.0 || clip["upload_successes"] != 1000.0 || clip["mixed_control_attempts"] != 50.0 {
		t.Fatalf("clip actual scale = %#v", clip)
	}
}

func TestCombineAndWriteFeatureQualificationReports(t *testing.T) {
	first := featureEvidenceManifest{
		Status: "PASS", StartedAt: "2026-07-24T00:00:00Z", CompletedAt: "2026-07-24T00:00:01Z", DurationMS: 1000,
		Cases: []featureCaseEvidence{{TestID: "E2E-HOME-SHADOW-001", Status: "PASS"}},
	}
	second := blockedFeatureManifest(
		"run", "device-shadow", "qualification-1k", "staging", map[string]string{"workspace": "commit"},
		[]featureRunSpec{{Feature: "device-shadow", Profile: "qualification-1k", TestIDs: []string{"E2E-HOME-SHADOW-001"}, LoadTestID: "LOAD-HOME-SHADOW-001"}},
		"environment unavailable",
	)
	second.DurationMS = 2000
	combined := combineFeatureManifests("run", "device-shadow", "qualification-1k", "staging", map[string]string{"workspace": "commit"}, []featureEvidenceManifest{first, second})
	if combined.Status != "BLOCKED" || combined.DurationMS != 3000 || len(combined.Cases) != 3 {
		t.Fatalf("combined manifest = %#v", combined)
	}
	outDir := t.TempDir()
	if err := writeFeatureQualificationReports(outDir, combined); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"qualification-report.json", "qualification-report.md"} {
		if stat, err := os.Stat(filepath.Join(outDir, name)); err != nil || stat.Size() == 0 {
			t.Fatalf("%s: stat=%v err=%v", name, stat, err)
		}
	}
}

func TestResolveFeatureSpecsQualificationRunsCanaryFirst(t *testing.T) {
	catalog := testCatalog{Cases: []testCatalogCase{
		{ID: "E2E-HOME-SHADOW-002", Layer: "e2e", Feature: "device-shadow", Profile: "canary", Runner: "test-feature", Status: "active", Source: "canary.env", Method: "offline"},
		{ID: "E2E-HOME-SHADOW-001", Layer: "e2e", Feature: "device-shadow", Profile: "canary", Runner: "test-feature", Status: "active", Source: "canary.env", Method: "online"},
		{ID: "LIVE-VC-SHADOWMQTT-001", Layer: "live", Feature: "device-shadow", Profile: "canary", Runner: "test-feature", Status: "active", Source: "canary.env", Method: "broker policy"},
		{ID: "LOAD-HOME-SHADOW-001", Layer: "load", Feature: "device-shadow", Profile: "qualification-1k", Runner: "test-feature", Status: "active", Source: "1k.env", Covers: []string{"E2E-HOME-SHADOW-001", "E2E-HOME-SHADOW-002"}},
	}}
	specs, err := resolveFeatureSpecs(catalog, "device-shadow", "qualification-1k")
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 || specs[0].Profile != "canary" || specs[1].Profile != "qualification-1k" {
		t.Fatalf("unexpected execution order: %+v", specs)
	}
	if strings.Join(specs[0].TestIDs, ",") != "E2E-HOME-SHADOW-001,E2E-HOME-SHADOW-002,LIVE-VC-SHADOWMQTT-001" {
		t.Fatalf("canary IDs are not stable and sorted: %v", specs[0].TestIDs)
	}
}

func TestExecuteFeatureSequenceMarksQualificationNotRunAfterCanaryFailure(t *testing.T) {
	specs := []featureRunSpec{
		{
			Feature: "device-shadow", Profile: "canary",
			TestIDs: []string{"E2E-HOME-SHADOW-001"},
		},
		{
			Feature: "device-shadow", Profile: "qualification-1k",
			TestIDs: []string{"E2E-HOME-SHADOW-001"}, LoadTestID: "LOAD-HOME-SHADOW-001",
		},
	}
	executed := []string{}
	recorded := []featureEvidenceManifest{}
	manifests, err := executeFeatureSequence(
		"run", "device-shadow", "staging", map[string]string{"workspace": "commit"}, specs,
		func(spec featureRunSpec) (featureEvidenceManifest, error) {
			executed = append(executed, spec.Profile)
			if spec.Profile != "canary" {
				t.Fatalf("qualification executor was called after canary failure: %s", spec.Profile)
			}
			return nonPassingFeatureManifest(
				"run", spec.Feature, spec.Profile, "staging", nil, []featureRunSpec{spec},
				"FAIL", "canary assertion failed",
			), nil
		},
		func(manifest featureEvidenceManifest) error {
			recorded = append(recorded, manifest)
			return nil
		},
	)
	if err == nil {
		t.Fatal("canary failure must return a non-nil qualification error")
	}
	if strings.Join(executed, ",") != "canary" {
		t.Fatalf("executed stages = %v, want only canary", executed)
	}
	if len(manifests) != 2 || len(recorded) != 2 {
		t.Fatalf("manifests/recorded = %d/%d, want 2/2", len(manifests), len(recorded))
	}
	if manifests[0].Status != "FAIL" || manifests[1].Status != "NOT_RUN" {
		t.Fatalf("stage statuses = %s/%s, want FAIL/NOT_RUN", manifests[0].Status, manifests[1].Status)
	}
	if manifests[1].Profile != "qualification-1k" || len(manifests[1].Cases) != 2 {
		t.Fatalf("NOT_RUN qualification manifest = %+v", manifests[1])
	}
	for _, result := range manifests[1].Cases {
		if result.Status != "NOT_RUN" {
			t.Fatalf("NOT_RUN case = %+v", result)
		}
	}
}

func TestWriteFeatureStageReportsMaterializesNotRunArtifactsWithoutOverwritingResults(t *testing.T) {
	spec := featureRunSpec{
		Feature: "device-shadow", Profile: "qualification-1k",
		TestIDs: []string{"E2E-HOME-SHADOW-001"}, LoadTestID: "LOAD-HOME-SHADOW-001",
		ScenarioPath: "loadtests/home-100k/scenarios/shadow.env",
		Scale:        map[string]any{"mqtt_devices": 1000},
		Purpose:      "qualify shadow",
		Method:       "distributed load",
	}
	manifest := notRunFeatureManifest(
		"run", spec.Feature, spec.Profile, "staging", map[string]string{"workspace": "commit"},
		[]featureRunSpec{spec}, "canary failed",
	)
	stageDir := filepath.Join(t.TempDir(), "qualification-1k")
	if err := writeFeatureStageReports(stageDir, spec, manifest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan.json", "results.json", "evidence-manifest.json", "feature-evidence.json", "TEST_REPORT.md", "junit.xml"} {
		if stat, err := os.Stat(filepath.Join(stageDir, name)); err != nil || stat.Size() == 0 {
			t.Fatalf("%s was not materialized: stat=%v err=%v", name, stat, err)
		}
	}
	var results map[string]any
	if err := readJSONFile(filepath.Join(stageDir, "results.json"), &results); err != nil {
		t.Fatal(err)
	}
	if results["status"] != "NOT_RUN" {
		t.Fatalf("fallback results status = %v, want NOT_RUN", results["status"])
	}
	var normalized featureEvidenceManifestV2
	if err := readJSONFile(filepath.Join(stageDir, "feature-evidence.json"), &normalized); err != nil {
		t.Fatal(err)
	}
	if len(normalized.Cases) != 2 {
		t.Fatalf("normalized cases = %d, want 2", len(normalized.Cases))
	}
	for _, item := range normalized.Cases {
		if len(item.Requirements) == 0 {
			t.Fatalf("normalized case lacks requirements: %+v", item)
		}
		types := map[string]bool{}
		for _, evidence := range item.Requirements[0].Evidence {
			types[evidence.Type] = true
		}
		if !types["markdown"] || !types["junit"] {
			t.Fatalf("normalized evidence types = %v, want markdown and junit", types)
		}
	}

	original := []byte("{\"runner_result\":true}\n")
	if err := os.WriteFile(filepath.Join(stageDir, "results.json"), original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFeatureStageReports(stageDir, spec, manifest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(stageDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("runner results were overwritten: %q", got)
	}
}

func TestCollectFeatureEvidenceFilesExcludesRenderedStageReport(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "results.json"), []byte(`{"status":"PASS"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TEST_REPORT.md"), []byte("rendered after manifest"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := collectFeatureEvidenceFiles(dir)
	if len(files) != 1 || files[0].Path != "results.json" {
		t.Fatalf("evidence files = %+v, want only stable results.json", files)
	}
}

func TestFeatureRootsSeparateDeploymentAndLoadRuntime(t *testing.T) {
	deploymentRoot := filepath.Join("/workspace", "cloud_env", "staging", "lke")
	if got, want := featureLoadEnvRoot(deploymentRoot), filepath.Join("/workspace", "cloud_env", "staging", "runtime"); got != want {
		t.Fatalf("featureLoadEnvRoot() = %q, want %q", got, want)
	}
	if got, want := featureKubeconfigPath(deploymentRoot), filepath.Join(deploymentRoot, "state", "kubeconfig.yaml"); got != want {
		t.Fatalf("featureKubeconfigPath() = %q, want %q", got, want)
	}
	if got, want := featureDeviceCABundlePath(deploymentRoot), filepath.Join(deploymentRoot, "state", "secrets", "device-client-ca-bundle.pem"); got != want {
		t.Fatalf("featureDeviceCABundlePath() = %q, want %q", got, want)
	}
}

func TestFeatureQualificationUsesResolvedFormalOwnerBrandPlan(t *testing.T) {
	t.Setenv("HOME100K_BRAND_PLAN", "")
	loadRoot := t.TempDir()
	t.Setenv("HOME100K_ENV_ROOT", loadRoot)
	want := filepath.Join(loadRoot, "artifacts", "load-owner", "resolved-brand-plan.json")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := featureQualificationBrandPlanPath(filepath.Join(t.TempDir(), "lke"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("qualification brand plan = %q, want %q", got, want)
	}
}

func TestFeatureQualificationRejectsMissingFormalOwnerBrandPlan(t *testing.T) {
	t.Setenv("HOME100K_BRAND_PLAN", "")
	loadRoot := t.TempDir()
	t.Setenv("HOME100K_ENV_ROOT", loadRoot)
	_, err := featureQualificationBrandPlanPath(filepath.Join(t.TempDir(), "lke"))
	if err == nil || !strings.Contains(err.Error(), "requires the run-scoped formal-owner brand plan") {
		t.Fatalf("featureQualificationBrandPlanPath() error = %v, want missing formal-owner plan", err)
	}
}

func TestFeatureResolvedBrandPlanOptionalMissingIsEmpty(t *testing.T) {
	t.Setenv("HOME100K_BRAND_PLAN", "")
	t.Setenv("HOME100K_ENV_ROOT", t.TempDir())
	got, err := featureResolvedBrandPlanPath(filepath.Join(t.TempDir(), "lke"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("optional resolved brand plan = %q, want empty", got)
	}
}

func TestFeatureResolvedBrandPlanRejectsDirectory(t *testing.T) {
	path := t.TempDir()
	t.Setenv("HOME100K_BRAND_PLAN", path)
	_, err := featureResolvedBrandPlanPath(filepath.Join(t.TempDir(), "lke"), true)
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("featureResolvedBrandPlanPath() error = %v, want non-regular-file rejection", err)
	}
}

func TestFeatureCanaryUsesFirstRunScopedFormalOwnerBrand(t *testing.T) {
	t.Setenv("HOME100K_BRAND_PLAN", "")
	loadRoot := t.TempDir()
	t.Setenv("HOME100K_ENV_ROOT", loadRoot)
	path := filepath.Join(loadRoot, "artifacts", "load-owner", "resolved-brand-plan.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"total_devices":10,"devices_per_user":5,"brands":[{"brandname":"RTK-LOAD-1K-run-B01","devices":10,"normal_users":2,"developer_users":{"owner":1},"device_mix":{"light":10}}]}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := featureRunScopedBrandName(filepath.Join(t.TempDir(), "lke"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "RTK-LOAD-1K-run-B01" {
		t.Fatalf("canary brand = %q, want run-scoped formal owner brand", got)
	}
}

func TestFeatureExternalEndpointEnvUsesRunEnvironmentDomains(t *testing.T) {
	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	stack := "VIDEO_CLOUD_DOMAIN=video.example.test\nACCOUNT_MANAGER_DOMAIN=account.example.test\n"
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte(stack), 0o600); err != nil {
		t.Fatal(err)
	}
	got := featureExternalEndpointEnv(envRoot)
	want := map[string]string{
		"HOME100K_ACCOUNT_MANAGER_BASE_URL":    "https://account.example.test",
		"HOME100K_VIDEO_CLOUD_PUBLIC_BASE_URL": "https://video.example.test",
		"HOME100K_VIDEO_CLOUD_TOKEN_BASE_URL":  "https://device.video.example.test",
		"HOME100K_MQTT_ADDR":                   "video.example.test:8883",
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestExecuteFeatureSpecRequiresDeploymentRegion(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "lke")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := featureRunSpec{
		Feature:      "device-shadow",
		Profile:      "canary",
		ScenarioPath: "missing-description.env",
	}
	manifest, err := executeFeatureSpec(workspace, envRoot, "test-run", "staging", spec, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "CLOUD_REGION is missing") {
		t.Fatalf("executeFeatureSpec() error = %v, want missing CLOUD_REGION", err)
	}
	if manifest.Status != "BLOCKED" {
		t.Fatalf("manifest status = %q, want BLOCKED", manifest.Status)
	}
}

func TestBoundedFeatureStageRunIDHonorsLinodeTagLimit(t *testing.T) {
	runID := strings.Repeat("qualification-", 8)
	got := boundedFeatureStageRunID(runID, "device-shadow", "qualification-1k")
	if len(got) > 50 {
		t.Fatalf("stage run ID length = %d, want <= 50: %q", len(got), got)
	}
	if got != boundedFeatureStageRunID(runID, "device-shadow", "qualification-1k") {
		t.Fatal("bounded stage run ID is not deterministic")
	}
	if got == boundedFeatureStageRunID(runID, "video-webrtc", "qualification-1k") {
		t.Fatal("different feature produced the same bounded stage run ID")
	}
}

func TestFeatureLoadEnvRootUsesRuntimeSiblingForLKEDeployment(t *testing.T) {
	got := featureLoadEnvRoot("/workspace/cloud_env/staging/lke")
	if got != "/workspace/cloud_env/staging/runtime" {
		t.Fatalf("load env root = %q", got)
	}
}

func TestFeatureExecutionLoadEnvRootHonorsExplicitOverride(t *testing.T) {
	t.Setenv("HOME100K_ENV_ROOT", "/tmp/runtime-coverage/lke")
	got := featureExecutionLoadEnvRoot("/workspace/cloud_env/staging/lke")
	if got != "/tmp/runtime-coverage/lke" {
		t.Fatalf("execution load env root = %q", got)
	}
}

func TestFeatureSelectionUsesChangedPathsAndLabels(t *testing.T) {
	catalog := testCatalog{Cases: []testCatalogCase{
		{ID: "LOAD-HOME-SHADOW-001", Layer: "load", Feature: "device-shadow", Profile: "qualification-1k", Status: "active", ChangePaths: []string{"repos/rtk_video_cloud/internal/deviceshadow/**"}},
		{ID: "LOAD-HOME-VIDEO-001", Layer: "load", Feature: "video-webrtc", Profile: "qualification-1k", Status: "active", ChangePaths: []string{"repos/rtk_video_cloud/internal/webrtc/**"}},
	}}
	selection, err := selectFeatureQualifications(catalog,
		[]string{"repos/rtk_video_cloud/internal/deviceshadow/service.go"},
		[]string{"qualification/clip-storage"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(selection.Features, ",") != "clip-storage,device-shadow" {
		t.Fatalf("unexpected selected features: %v", selection.Features)
	}
	if !selection.Required {
		t.Fatal("selected features must require qualification")
	}
}

func TestFeatureSelectionSharedPathSelectsAllFeatures(t *testing.T) {
	selection, err := selectFeatureQualifications(testCatalog{},
		[]string{"tests/catalog.yaml"},
		nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(selection.Features, ",") != "clip-storage,device-shadow,video-webrtc" {
		t.Fatalf("shared path selected features = %v", selection.Features)
	}
	for _, feature := range selection.Features {
		if strings.Join(selection.MatchedPaths[feature], ",") != "tests/catalog.yaml" {
			t.Fatalf("%s matched paths = %v", feature, selection.MatchedPaths[feature])
		}
	}
}

func TestFeatureSelectionSharedDeploymentPathsSelectAllFeatures(t *testing.T) {
	paths := []string{
		".github/workflows/feature-qualification.yml",
		"cloud_deploy/roles/video-cloud/tasks/main.yml",
		"cloud_env/staging/deployment.env",
		"cloud_env/staging/environment.env",
		"cloud_env/staging/overrides/video-cloud.env",
		"repos/rtk_cloud_logger",
	}
	want := []string{"clip-storage", "device-shadow", "video-webrtc"}
	for _, changedPath := range paths {
		t.Run(changedPath, func(t *testing.T) {
			selection, err := selectFeatureQualifications(testCatalog{}, []string{changedPath}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(selection.Features, want) {
				t.Fatalf("%s must select all features: got %v, want %v", changedPath, selection.Features, want)
			}
		})
	}
}

func TestFeatureSelectionVideoCloudGitlinkSelectsAllFeatures(t *testing.T) {
	selection, err := selectFeatureQualifications(testCatalog{},
		[]string{"repos/rtk_video_cloud"},
		nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Features) != 3 {
		t.Fatalf("video cloud gitlink must conservatively select all features: %v", selection.Features)
	}
}

func TestWriteFeatureSelectionFileCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "artifacts", "feature-selection.json")
	raw := []byte("{\"required\":true}\n")
	if err := writeFeatureSelectionFile(path, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("selection file = %q, want %q", got, raw)
	}
}

func TestClassifyFeatureRunRejectsMissingEvidence(t *testing.T) {
	results := map[string]any{
		"status": "COMPLETE", "result": "SUCCESS",
		"server_evidence":         map[string]any{"complete": false},
		"server_correlation":      map[string]any{"status": "complete"},
		"runtime_log_correlation": map[string]any{"status": "complete"},
	}
	status, _ := classifyFeatureRun(featureRunSpec{Feature: "device-shadow"}, results, nil, nil)
	if status != "INCOMPLETE" {
		t.Fatalf("status = %s, want INCOMPLETE", status)
	}
}

func TestClassifyFeatureRunAcceptsRunnerPassCorrelationStatus(t *testing.T) {
	results := map[string]any{
		"status": "COMPLETE", "result": "SUCCESS",
		"server_evidence":         map[string]any{"complete": true},
		"server_correlation":      map[string]any{"status": "pass"},
		"runtime_log_correlation": map[string]any{"status": "pass"},
	}
	status, _ := classifyFeatureRun(featureRunSpec{Feature: "device-shadow"}, results, nil, nil)
	if status != "PASS" {
		t.Fatalf("status = %s, want PASS", status)
	}
}

func TestClassifyVideoFeatureRunAcceptsSkippedShadowCorrelations(t *testing.T) {
	results := map[string]any{
		"status": "COMPLETE", "result": "SUCCESS",
		"server_evidence":         map[string]any{"complete": true},
		"server_correlation":      map[string]any{"status": "skipped"},
		"runtime_log_correlation": map[string]any{"status": "skipped"},
	}
	status, _ := classifyFeatureRun(featureRunSpec{Feature: "video-webrtc"}, results, nil, nil)
	if status != "PASS" {
		t.Fatalf("video status = %s, want PASS", status)
	}
	status, _ = classifyFeatureRun(featureRunSpec{Feature: "device-shadow"}, results, nil, nil)
	if status != "INCOMPLETE" {
		t.Fatalf("shadow status = %s, want INCOMPLETE", status)
	}
	status, _ = classifyFeatureRun(featureRunSpec{Feature: "clip-storage"}, results, nil, nil)
	if status != "PASS" {
		t.Fatalf("clip status = %s, want PASS", status)
	}
}

func TestEvaluateFeatureEvidenceDoesNotPassCasesWhenAggregateIsIncomplete(t *testing.T) {
	stageDir := t.TempDir()
	results := map[string]any{
		"status": "COMPLETE", "result": "SUCCESS",
		"server_evidence":         map[string]any{"complete": false},
		"server_correlation":      map[string]any{"status": "complete"},
		"runtime_log_correlation": map[string]any{"status": "complete"},
		"stage_results": []any{map[string]any{
			"desired_reported_convergence_rate_percent": float64(100),
			"offline_desired_convergence_rate_percent":  float64(100),
			"version_conflict_count":                    float64(1),
			"rejected_update_count":                     float64(1),
			"unauthorized_rejection_count":              float64(1),
			"aws_namespace_rejection_count":             float64(1),
			"duplicate_suppression_count":               float64(1),
			"duplicate_apply_count":                     float64(0),
			"authorization_violation_count":             float64(0),
			"device_mqtt_totals":                        map[string]any{"delta_received": float64(1), "reported_publishes": float64(1)},
		}},
	}
	if err := writeJSONFile(filepath.Join(stageDir, "results.json"), results); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(stageDir, "server-evidence.json"), map[string]any{"complete": true}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), map[string]any{"status": "pass"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := evaluateFeatureEvidence(stageDir, "run", "staging", featureRunSpec{
		Feature: "device-shadow", Profile: "canary", TestIDs: []string{"E2E-HOME-SHADOW-001"},
	}, map[string]string{}, now, now.Add(time.Second), nil)
	if manifest.Status != "INCOMPLETE" || manifest.Cases[0].Status != "INCOMPLETE" {
		t.Fatalf("manifest/case status = %s/%s", manifest.Status, manifest.Cases[0].Status)
	}
}

func TestEvaluateFeatureEvidenceMissingStandaloneServerEvidenceIsIncomplete(t *testing.T) {
	stageDir := t.TempDir()
	results := map[string]any{
		"status": "COMPLETE", "result": "SUCCESS",
		"server_evidence":         map[string]any{"complete": true},
		"server_correlation":      map[string]any{"status": "pass"},
		"runtime_log_correlation": map[string]any{"status": "pass"},
	}
	if err := writeJSONFile(filepath.Join(stageDir, "results.json"), results); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), map[string]any{"status": "pass"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := evaluateFeatureEvidence(
		stageDir, "run", "staging", featureRunSpec{Feature: "device-shadow", Profile: "canary"},
		map[string]string{}, now, now.Add(time.Second), nil,
	)
	if manifest.Status != "INCOMPLETE" || !strings.Contains(manifest.Assessment, "server-evidence.json") {
		t.Fatalf("missing standalone server evidence manifest = %+v", manifest)
	}
}

func TestValidateFeatureEvidenceFilesRequiresRuntimeStatus(t *testing.T) {
	stageDir := t.TempDir()
	if err := writeJSONFile(filepath.Join(stageDir, "server-evidence.json"), map[string]any{"complete": true}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := validateFeatureEvidenceFiles(stageDir, featureRunSpec{Feature: "video-webrtc"}); err == nil {
		t.Fatal("runtime evidence without status must be incomplete")
	}
	if err := writeJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), map[string]any{"status": "skipped"}); err != nil {
		t.Fatal(err)
	}
	if err := validateFeatureEvidenceFiles(stageDir, featureRunSpec{Feature: "video-webrtc"}); err != nil {
		t.Fatalf("documented non-shadow skipped correlation rejected: %v", err)
	}
}

func TestEvaluateFeatureEvidenceBehaviorFailureFailsQualification(t *testing.T) {
	stageDir := t.TempDir()
	results := map[string]any{
		"status": "COMPLETE", "result": "SUCCESS",
		"server_evidence":         map[string]any{"complete": true},
		"server_correlation":      map[string]any{"status": "pass"},
		"runtime_log_correlation": map[string]any{"status": "pass"},
		"stage_results":           []any{map[string]any{}},
	}
	if err := writeJSONFile(filepath.Join(stageDir, "results.json"), results); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(stageDir, "server-evidence.json"), map[string]any{"complete": true}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), map[string]any{"status": "pass"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := evaluateFeatureEvidence(stageDir, "run", "staging", featureRunSpec{
		Feature: "device-shadow", Profile: "canary", TestIDs: []string{"E2E-HOME-SHADOW-001"},
	}, map[string]string{}, now, now.Add(time.Second), nil)
	if manifest.Status != "FAIL" || manifest.Cases[0].Status != "FAIL" {
		t.Fatalf("behavior failure status = %s/%s", manifest.Status, manifest.Cases[0].Status)
	}
}

func TestClassifyFeatureRunThresholdFailureIsFail(t *testing.T) {
	results := map[string]any{
		"status": "COMPLETE", "result": "FAIL",
		"server_evidence":         map[string]any{"complete": true},
		"server_correlation":      map[string]any{"status": "pass"},
		"runtime_log_correlation": map[string]any{"status": "pass"},
	}
	status, _ := classifyFeatureRun(featureRunSpec{Feature: "device-shadow"}, results, nil, nil)
	if status != "FAIL" {
		t.Fatalf("threshold failure status = %s", status)
	}
}

func TestClipQualificationMissingCredentialsIsBlocked(t *testing.T) {
	t.Setenv("VIDEO_CLOUD_LOAD_ADMIN_TOKEN", "")
	err := validateFeaturePrerequisites(featureRunSpec{Feature: "clip-storage", Profile: "qualification-1k"})
	if err == nil {
		t.Fatal("missing Clip qualification credentials must block the run")
	}
}

func TestFeatureWorkflowCommandAllowsBoundedLocalLiveCanary(t *testing.T) {
	t.Setenv("RUNTIME_COVERAGE_FEATURE_WORKFLOW", "workflow-local-live")
	command, err := featureWorkflowCommand()
	if err != nil {
		t.Fatal(err)
	}
	if command != "workflow-local-live" {
		t.Fatalf("feature workflow command = %q", command)
	}

	t.Setenv("RUNTIME_COVERAGE_FEATURE_WORKFLOW", "sample")
	if _, err := featureWorkflowCommand(); err == nil {
		t.Fatal("unsupported feature workflow command was accepted")
	}
}

func TestExecuteFeatureSpecUsesSelectedWorkflowCommand(t *testing.T) {
	workspace := t.TempDir()
	scriptPath := filepath.Join(workspace, "loadtests", "home-100k", "scripts", "home-100k.sh")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n[ \"$HOME100K_SHUTDOWN_ON_ERROR\" = \"1\" ] || exit 42\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := featureRunSpec{
		Feature:      "device-shadow",
		Profile:      "canary",
		TestIDs:      []string{"E2E-HOME-SHADOW-001"},
		ScenarioPath: "scenario.env",
		Scale:        map[string]any{"devices": 10},
	}

	t.Setenv("RUNTIME_COVERAGE_FEATURE_WORKFLOW", "unsupported")
	blocked, err := executeFeatureSpec(workspace, t.TempDir(), "unit-invalid-workflow", "staging", spec, map[string]string{"workspace": "abc"})
	if err == nil || blocked.Status != "BLOCKED" {
		t.Fatalf("invalid workflow result = %#v, %v", blocked, err)
	}

	t.Setenv("RUNTIME_COVERAGE_FEATURE_WORKFLOW", "workflow-local-live")
	envRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte("CLOUD_REGION=us-sea\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := executeFeatureSpec(workspace, envRoot, "unit-local-workflow", "staging", spec, map[string]string{"workspace": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "INCOMPLETE" {
		t.Fatalf("manifest status = %q, want INCOMPLETE without generated evidence", manifest.Status)
	}
}

func TestVerifyFeatureDeploymentCommitsRequiresDigestAndCommit(t *testing.T) {
	envRoot := t.TempDir()
	manifestDir := filepath.Join(envRoot, "artifacts", "lke-images")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatal(err)
	}
	images := []any{
		map[string]any{
			"source_path": "repos/rtk_video_cloud", "source_commit": "video-commit",
			"image": "registry/video:test", "digest": "sha256:video",
		},
		map[string]any{
			"source_path": "repos/rtk_account_manager", "source_commit": "account-commit",
			"image": "registry/account:test", "digest": "sha256:account",
		},
		map[string]any{
			"source_path": "repos/rtk_cloud_logger", "source_commit": "logger-commit",
			"image": "registry/logger:test", "digest": "sha256:logger",
		},
	}
	path := filepath.Join(manifestDir, "lke-image-manifest.json")
	if err := writeJSONFile(path, map[string]any{"images": images}); err != nil {
		t.Fatal(err)
	}
	commits := map[string]string{"video_cloud": "video-commit", "account_manager": "account-commit", "cloud_logger": "logger-commit"}
	if err := verifyFeatureDeploymentCommits(envRoot, commits); err != nil {
		t.Fatalf("valid deployment anchors rejected: %v", err)
	}
	images[0].(map[string]any)["digest"] = ""
	if err := writeJSONFile(path, map[string]any{"images": images}); err != nil {
		t.Fatal(err)
	}
	if err := verifyFeatureDeploymentCommits(envRoot, commits); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("missing digest error = %v", err)
	}
}

func TestEvaluateShadowCasesRequiresBehaviorEvidence(t *testing.T) {
	results := map[string]any{
		"runtime_log_correlation": map[string]any{"status": "pass"},
		"stage_results": []any{map[string]any{
			"mqtt_connect_success_rate_percent":         float64(100),
			"desired_reported_convergence_rate_percent": float64(100),
			"offline_desired_convergence_rate_percent":  float64(100),
			"version_conflict_count":                    float64(1),
			"rejected_update_count":                     float64(1),
			"unauthorized_rejection_count":              float64(1),
			"aws_namespace_rejection_count":             float64(1),
			"duplicate_suppression_count":               float64(1),
			"duplicate_apply_count":                     float64(0),
			"authorization_violation_count":             float64(0),
			"device_mqtt_totals":                        map[string]any{"delta_received": float64(1), "reported_publishes": float64(1)},
		}},
	}
	spec := featureRunSpec{Feature: "device-shadow", Profile: "canary"}
	_, _, _, exact, cases := evaluateFeatureCases(spec, results, t.TempDir())
	if exact != 100 {
		t.Fatalf("exact correlation = %v", exact)
	}
	for _, id := range []string{"E2E-HOME-SHADOW-001", "E2E-HOME-SHADOW-002", "E2E-HOME-SHADOW-003", "LIVE-VC-SHADOWMQTT-001"} {
		if cases[id].Status != "PASS" {
			t.Fatalf("%s = %+v", id, cases[id])
		}
	}
}

func TestEvaluateShadowCasesRejectsMissingAWSNamespaceEvidence(t *testing.T) {
	results := map[string]any{
		"runtime_log_correlation": map[string]any{"status": "pass"},
		"stage_results": []any{map[string]any{
			"authorization_violation_count": float64(0),
		}},
	}
	_, _, _, _, cases := evaluateFeatureCases(featureRunSpec{Feature: "device-shadow", Profile: "canary"}, results, t.TempDir())
	if cases["LIVE-VC-SHADOWMQTT-001"].Status != "FAIL" {
		t.Fatalf("missing AWS namespace rejection = %+v", cases["LIVE-VC-SHADOWMQTT-001"])
	}
}

func TestEvaluateVideoCasesRequiresMediaAndDynamicTURN(t *testing.T) {
	results := map[string]any{
		"video_evidence": map[string]any{
			"complete": true,
			"webrtc_totals": map[string]any{
				"create_success": float64(2), "setup_success": float64(2), "close_success": float64(2),
				"success_rate_percent": float64(100),
			},
			"webrtc_media_totals": map[string]any{"successes": float64(2), "time_to_first_rtp_p95_ms": float64(120)},
			"turn_evidence": map[string]any{
				"registry_available": true, "active_nodes": float64(1),
				"api_turn_registry_lookup_succeeded": float64(1), "api_turn_registry_node_count": float64(1),
				"api_dynamic_turn_count": float64(2),
			},
			"relay_candidate_samples":     float64(2),
			"non_relay_candidate_samples": float64(0),
		},
	}
	spec := featureRunSpec{Feature: "video-webrtc", Profile: "canary"}
	_, _, _, _, cases := evaluateFeatureCases(spec, results, t.TempDir())
	for _, id := range []string{"E2E-VC-WEBRTC-001", "E2E-VC-TURN-001"} {
		if cases[id].Status != "PASS" {
			t.Fatalf("%s = %+v", id, cases[id])
		}
	}
}

func TestEvaluateClipCasesRequiresOperationsAndReconciliation(t *testing.T) {
	stageDir := t.TempDir()
	clipDir := filepath.Join(stageDir, "clip-storage")
	if err := os.MkdirAll(clipDir, 0o755); err != nil {
		t.Fatal(err)
	}
	operations := []any{}
	for _, name := range []string{"clip_upload", "clip_verify_ready", "clip_info", "clip_total", "clip_enum", "clip_download_range", "clip_download_invalid_range", "clip_delete"} {
		operations = append(operations, map[string]any{"name": name, "success": true})
	}
	if err := writeJSONFile(filepath.Join(clipDir, "load-results.json"), map[string]any{
		"thresholds": map[string]any{"passed": true},
		"operations": operations,
		"clip_storage": map[string]any{
			"camera_devices": float64(2), "upload_successes": float64(4),
			"success_rate": float64(1), "p95_latency_ms": float64(40),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clipDir, "reconciliation.json"), []byte(`{"status":"complete"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := featureRunSpec{
		Feature: "clip-storage", Profile: "canary",
		Scale: map[string]any{"cameras": 2, "clip_uploads": 4},
	}
	_, successRate, _, _, cases := evaluateFeatureCases(spec, nil, stageDir)
	if successRate != 100 {
		t.Fatalf("clip success rate = %v, want percentage 100", successRate)
	}
	if cases["E2E-VC-CLIP-001"].Status != "PASS" {
		t.Fatalf("clip case = %+v", cases["E2E-VC-CLIP-001"])
	}
}

func TestNormalizedFeatureEvidenceCarriesExplicitWorkflowSteps(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := loadSpecInventory(workspace)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for testID, operations := range map[string][]string{
		"E2E-HOME-SHADOW-003": {"desired_write", "reported_publish", "version_conflict"},
		"E2E-VC-WEBRTC-001":   {"ice_preflight", "webrtc_create", "webrtc_setup", "webrtc_close"},
		"E2E-VC-TURN-001":     {"turn_node_registered", "turn_node_active", "dynamic_turn_resolve"},
		"E2E-VC-CLIP-001":     {"clip_upload", "clip_verify_ready", "clip_enum", "clip_download_range", "clip_delete"},
	} {
		tc, ok := catalogCaseByID(catalog.Cases, testID)
		if !ok {
			t.Fatalf("missing catalog case %s", testID)
		}
		assertions := normalizedFeatureWorkflowAssertions(featureCaseEvidence{
			TestID: testID, Status: "PASS", Operations: operations,
		}, tc, inventory)
		if len(assertions) != 1 {
			t.Fatalf("%s workflow assertions=%+v", testID, assertions)
		}
		workflow := map[string]specWorkflow{}
		for _, item := range inventory.Workflows {
			workflow[item.ID] = item
		}
		if current := workflow[assertions[0].WorkflowID]; len(assertions[0].Steps) != len(current.Steps) {
			t.Fatalf("%s steps=%d want=%d", testID, len(assertions[0].Steps), len(current.Steps))
		}
	}
}

func TestIncompleteFeatureManifestMarksEveryCaseIncomplete(t *testing.T) {
	manifest := incompleteFeatureManifest("run", "device-shadow", "qualification-1k", "staging", map[string]string{},
		[]featureRunSpec{{
			Feature: "device-shadow", Profile: "qualification-1k",
			TestIDs: []string{"E2E-HOME-SHADOW-001"}, LoadTestID: "LOAD-HOME-SHADOW-001",
		}}, "deployment commit mismatch")
	if manifest.Status != "INCOMPLETE" || len(manifest.Cases) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	for _, result := range manifest.Cases {
		if result.Status != "INCOMPLETE" {
			t.Fatalf("%s status = %s", result.TestID, result.Status)
		}
	}
}

func TestFeatureEvidenceRejectsUnredactedSecrets(t *testing.T) {
	stageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stageDir, "unsafe.json"), []byte(`{"access_token":"real-secret-token"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findUnredactedFeatureEvidence(stageDir); len(got) != 1 || got[0] != "unsafe.json" {
		t.Fatalf("unredacted files = %v", got)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "safe.json"), []byte(`{"access_token":"<redacted>"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findUnredactedFeatureEvidence(stageDir); len(got) != 1 {
		t.Fatalf("redacted value should not add a match: %v", got)
	}
}

func TestPurgeFeatureSecretArtifacts(t *testing.T) {
	stageDir := t.TempDir()
	for _, name := range []string{"token-env.sh", "device-token-map.json", "app-token-map.json", "clip-private-key.pem"} {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	safePath := filepath.Join(stageDir, "results.json")
	if err := os.WriteFile(safePath, []byte(`{"status":"COMPLETE"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	purgeFeatureSecretArtifacts(stageDir)
	for _, name := range []string{"token-env.sh", "device-token-map.json", "app-token-map.json", "clip-private-key.pem"} {
		if _, err := os.Stat(filepath.Join(stageDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was not purged", name)
		}
	}
	if _, err := os.Stat(safePath); err != nil {
		t.Fatalf("safe evidence was removed: %v", err)
	}
}

func TestMaterializeRuntimeLogEvidence(t *testing.T) {
	stageDir := t.TempDir()
	if err := writeJSONFile(filepath.Join(stageDir, "results.json"), map[string]any{
		"runtime_log_correlation": map[string]any{"status": "complete", "matched": float64(10)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := materializeRuntimeLogEvidence(stageDir); err != nil {
		t.Fatal(err)
	}
	var evidence map[string]any
	if err := readJSONFile(filepath.Join(stageDir, "runtime-log-evidence.json"), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence["status"] != "complete" {
		t.Fatalf("runtime evidence = %v", evidence)
	}
}

func TestRenderFeatureReportIncludesTimingPurposeMethodAndResult(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	report := string(renderFeatureReport(featureEvidenceManifest{
		RunID: "run-1", Feature: "device-shadow", Profile: "canary", Environment: "staging",
		Status: "PASS", Assessment: "passed", StartedAt: now, CompletedAt: now, DurationMS: 12,
		Cases: []featureCaseEvidence{{TestID: "E2E-HOME-SHADOW-001", Purpose: "purpose", Method: "method", DurationMS: 12, Status: "PASS", Assessment: "passed"}},
	}))
	for _, wanted := range []string{"Started:", "Completed:", "Duration:", "purpose", "method", "**PASS**"} {
		if !strings.Contains(report, wanted) {
			t.Fatalf("report missing %q:\n%s", wanted, report)
		}
	}
}
