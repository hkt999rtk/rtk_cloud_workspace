package home100k

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RunOptions struct {
	PlanOptions
	RunID              string
	OutDir             string
	EphemeralVMs       bool
	ServerEvidenceFile string
}

type RunResult struct {
	RunID               string              `json:"run_id"`
	Status              string              `json:"status"`
	Plan                Plan                `json:"plan"`
	StageResults        []StageResult       `json:"stage_results"`
	ServerEvidence      ServerEvidence      `json:"server_evidence"`
	LoadGeneratorHealth LoadGeneratorHealth `json:"load_generator_health"`
	PlanFile            string              `json:"plan_file"`
	ResultsFile         string              `json:"results_file"`
	ServerEvidenceFile  string              `json:"server_evidence_file"`
	ReportFile          string              `json:"report_file"`
}

type AggregateOptions struct {
	PlanOptions
	RunID  string
	OutDir string
}

type StageResult struct {
	Name                           string  `json:"name"`
	ConnectedDevices               int     `json:"connected_devices"`
	MQTTConnectSuccessRatePercent  float64 `json:"mqtt_connect_success_rate_percent"`
	MQTTReconnectCount             int     `json:"mqtt_reconnect_count"`
	ShadowGetP50MS                 float64 `json:"shadow_get_p50_ms"`
	ShadowGetP95MS                 float64 `json:"shadow_get_p95_ms"`
	ShadowGetP99MS                 float64 `json:"shadow_get_p99_ms"`
	DesiredUpdateP95MS             float64 `json:"desired_update_p95_ms"`
	DeltaReceiveP95MS              float64 `json:"delta_receive_p95_ms"`
	DesiredReportedP95MS           float64 `json:"desired_reported_p95_ms"`
	OfflineDesiredP95MS            float64 `json:"offline_desired_p95_ms"`
	DeltaClearSuccessRatePercent   float64 `json:"delta_clear_success_rate_percent"`
	DesiredReportedConvergenceRate float64 `json:"desired_reported_convergence_rate_percent"`
	OfflineDesiredConvergenceRate  float64 `json:"offline_desired_convergence_rate_percent"`
	DuplicateApplyCount            int     `json:"duplicate_apply_count"`
	VersionConflictCount           int     `json:"version_conflict_count"`
	RejectedUpdateCount            int     `json:"rejected_update_count"`
	AuthorizationViolationCount    int     `json:"authorization_violation_count"`
	ClientTokenCorrelationCount    int     `json:"client_token_correlation_count"`
}

type LoadGeneratorHealth struct {
	Saturated bool     `json:"saturated"`
	Reasons   []string `json:"reasons,omitempty"`
}

type ServerEvidence struct {
	RunID    string                    `json:"run_id"`
	Complete bool                      `json:"complete"`
	Sources  map[string]EvidenceSource `json:"sources"`
	Notes    []string                  `json:"notes,omitempty"`
}

type EvidenceSource struct {
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

func Run(opts RunOptions) (RunResult, error) {
	if !opts.EphemeralVMs {
		return RunResult{}, errors.New("--ephemeral-vms is required for 100K secret-bearing load-generator runs")
	}
	plan, err := NewPlan(opts.PlanOptions)
	if err != nil {
		return RunResult{}, err
	}
	if err := plan.Validate(); err != nil {
		return RunResult{}, err
	}
	runID := strings.TrimSpace(opts.RunID)
	if runID == "" {
		runID = time.Now().UTC().Format("20060102T150405Z")
	}
	plan.Lifecycle = BuildLifecycleActions(plan, runID)
	outDir := strings.TrimSpace(opts.OutDir)
	if outDir == "" {
		outDir = filepath.Join("loadtests", "home-100k", "reports", runID)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return RunResult{}, err
	}

	evidence, err := loadServerEvidence(opts.ServerEvidenceFile, runID)
	if err != nil {
		return RunResult{}, err
	}
	stageResults, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 2})
	if err != nil {
		return RunResult{}, err
	}
	status := runStatus(evidence, stageResults)

	result := RunResult{
		RunID:               runID,
		Status:              status,
		Plan:                plan,
		StageResults:        stageResults,
		ServerEvidence:      evidence,
		LoadGeneratorHealth: LoadGeneratorHealth{},
		PlanFile:            filepath.Join(outDir, "plan.json"),
		ResultsFile:         filepath.Join(outDir, "results.json"),
		ServerEvidenceFile:  filepath.Join(outDir, "server-evidence.json"),
		ReportFile:          filepath.Join(outDir, "TEST_REPORT.md"),
	}
	if err := writeJSONFile(result.PlanFile, plan); err != nil {
		return RunResult{}, err
	}
	if err := writeJSONFile(result.ResultsFile, result); err != nil {
		return RunResult{}, err
	}
	if err := writeJSONFile(result.ServerEvidenceFile, evidence); err != nil {
		return RunResult{}, err
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                runID,
		ShadowEvidenceFound:  shadowEvidenceComplete(stageResults),
		ServerEvidenceFound:  evidence.Complete,
		LoadGeneratorHealthy: true,
		StageResults:         stageResults,
		ServerEvidence:       evidence,
	})
	if err := os.WriteFile(result.ReportFile, []byte(report), 0o644); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func AggregateCollectedRun(opts AggregateOptions) (RunResult, error) {
	plan, err := NewPlan(opts.PlanOptions)
	if err != nil {
		return RunResult{}, err
	}
	if err := plan.Validate(); err != nil {
		return RunResult{}, err
	}
	runID := strings.TrimSpace(opts.RunID)
	if runID == "" {
		runID = time.Now().UTC().Format("20060102T150405Z")
	}
	plan.Lifecycle = BuildLifecycleActions(plan, runID)
	outDir := strings.TrimSpace(opts.OutDir)
	if outDir == "" {
		outDir = filepath.Join("loadtests", "home-100k", "reports", runID)
	}
	collected, err := loadCollectedShardResults(filepath.Join(outDir, "shards"))
	if err != nil {
		return RunResult{}, err
	}
	stages := collected.StageResults
	loadHealth := collected.LoadGeneratorHealth
	evidence, err := loadServerEvidence(filepath.Join(outDir, "server-evidence.json"), runID)
	if err != nil {
		evidence = ServerEvidence{
			RunID:    runID,
			Complete: false,
			Sources:  requiredEvidenceSources(false),
			Notes:    []string{"server evidence file was not found in collected artifacts"},
		}
	}
	status := runStatusWithLoadGenerator(evidence, stages, loadHealth)
	result := RunResult{
		RunID:               runID,
		Status:              status,
		Plan:                plan,
		StageResults:        stages,
		ServerEvidence:      evidence,
		LoadGeneratorHealth: loadHealth,
		PlanFile:            filepath.Join(outDir, "plan.json"),
		ResultsFile:         filepath.Join(outDir, "results.json"),
		ServerEvidenceFile:  filepath.Join(outDir, "server-evidence.json"),
		ReportFile:          filepath.Join(outDir, "TEST_REPORT.md"),
	}
	if err := writeJSONFile(result.PlanFile, plan); err != nil {
		return RunResult{}, err
	}
	if err := writeJSONFile(result.ResultsFile, result); err != nil {
		return RunResult{}, err
	}
	if evidence.Complete || len(evidence.Sources) > 0 {
		if err := writeJSONFile(result.ServerEvidenceFile, evidence); err != nil {
			return RunResult{}, err
		}
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                runID,
		ShadowEvidenceFound:  shadowEvidenceComplete(stages),
		ServerEvidenceFound:  evidence.Complete,
		LoadGeneratorHealthy: !loadHealth.Saturated,
		StageResults:         stages,
		ServerEvidence:       evidence,
		Notes:                loadHealth.Reasons,
	})
	if err := os.WriteFile(result.ReportFile, []byte(report), 0o644); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func loadServerEvidence(path string, runID string) (ServerEvidence, error) {
	if strings.TrimSpace(path) == "" {
		return ServerEvidence{
			RunID:    runID,
			Complete: false,
			Sources:  requiredEvidenceSources(false),
			Notes:    []string{"server evidence file was not provided"},
		}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ServerEvidence{}, err
	}
	var evidence ServerEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return ServerEvidence{}, err
	}
	if evidence.RunID == "" {
		evidence.RunID = runID
	}
	if evidence.Sources == nil {
		evidence.Sources = map[string]EvidenceSource{}
	}
	for key := range requiredEvidenceSources(false) {
		if _, ok := evidence.Sources[key]; !ok {
			evidence.Sources[key] = EvidenceSource{Available: false, Detail: "missing source"}
		}
	}
	evidence.Complete = evidence.Complete && allEvidenceSourcesAvailable(evidence.Sources)
	return evidence, nil
}

type collectedShardResults struct {
	StageResults        []StageResult
	LoadGeneratorHealth LoadGeneratorHealth
}

func loadCollectedShardResults(shardsDir string) (collectedShardResults, error) {
	matches, err := filepath.Glob(filepath.Join(shardsDir, "*", "results.json"))
	if err != nil {
		return collectedShardResults{}, err
	}
	if len(matches) == 0 {
		return collectedShardResults{}, fmt.Errorf("no shard results found under %s", shardsDir)
	}
	byStage := map[string][]StageResult{}
	health := LoadGeneratorHealth{}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			return collectedShardResults{}, err
		}
		var shard struct {
			StageResults        []StageResult `json:"stage_results"`
			LoadGeneratorHealth struct {
				Saturated bool     `json:"saturated"`
				Reason    string   `json:"reason"`
				Reasons   []string `json:"reasons"`
			} `json:"load_generator_health"`
		}
		if err := json.Unmarshal(raw, &shard); err != nil {
			return collectedShardResults{}, fmt.Errorf("decode %s: %w", path, err)
		}
		for _, stage := range shard.StageResults {
			byStage[stage.Name] = append(byStage[stage.Name], stage)
		}
		if shard.LoadGeneratorHealth.Saturated {
			health.Saturated = true
		}
		if strings.TrimSpace(shard.LoadGeneratorHealth.Reason) != "" {
			health.Reasons = append(health.Reasons, shard.LoadGeneratorHealth.Reason)
		}
		health.Reasons = append(health.Reasons, shard.LoadGeneratorHealth.Reasons...)
	}
	order := []string{"25k", "50k", "75k", "100k"}
	results := []StageResult{}
	for _, name := range order {
		items := byStage[name]
		if len(items) == 0 {
			continue
		}
		results = append(results, aggregateStageResults(items))
	}
	if len(results) == 0 {
		return collectedShardResults{}, fmt.Errorf("collected shard results did not contain stage_results")
	}
	return collectedShardResults{StageResults: results, LoadGeneratorHealth: health}, nil
}

func aggregateStageResults(items []StageResult) StageResult {
	result := StageResult{Name: items[0].Name}
	for _, item := range items {
		if item.ConnectedDevices > result.ConnectedDevices {
			result.ConnectedDevices = item.ConnectedDevices
		}
		result.MQTTConnectSuccessRatePercent += item.MQTTConnectSuccessRatePercent
		result.MQTTReconnectCount += item.MQTTReconnectCount
		result.ShadowGetP50MS += item.ShadowGetP50MS
		result.ShadowGetP95MS += item.ShadowGetP95MS
		result.ShadowGetP99MS += item.ShadowGetP99MS
		result.DesiredUpdateP95MS += item.DesiredUpdateP95MS
		result.DeltaReceiveP95MS += item.DeltaReceiveP95MS
		result.DesiredReportedP95MS += item.DesiredReportedP95MS
		result.OfflineDesiredP95MS += item.OfflineDesiredP95MS
		result.DeltaClearSuccessRatePercent += item.DeltaClearSuccessRatePercent
		result.DesiredReportedConvergenceRate += item.DesiredReportedConvergenceRate
		result.OfflineDesiredConvergenceRate += item.OfflineDesiredConvergenceRate
		result.DuplicateApplyCount += item.DuplicateApplyCount
		result.VersionConflictCount += item.VersionConflictCount
		result.RejectedUpdateCount += item.RejectedUpdateCount
		result.AuthorizationViolationCount += item.AuthorizationViolationCount
		result.ClientTokenCorrelationCount += item.ClientTokenCorrelationCount
	}
	count := float64(len(items))
	result.MQTTConnectSuccessRatePercent /= count
	result.ShadowGetP50MS /= count
	result.ShadowGetP95MS /= count
	result.ShadowGetP99MS /= count
	result.DesiredUpdateP95MS /= count
	result.DeltaReceiveP95MS /= count
	result.DesiredReportedP95MS /= count
	result.OfflineDesiredP95MS /= count
	result.DeltaClearSuccessRatePercent /= count
	result.DesiredReportedConvergenceRate /= count
	result.OfflineDesiredConvergenceRate /= count
	return result
}

func requiredEvidenceSources(available bool) map[string]EvidenceSource {
	return map[string]EvidenceSource{
		"emqx":               {Available: available},
		"video_cloud_api":    {Available: available},
		"iot_device_shadow":  {Available: available},
		"postgres":           {Available: available},
		"redis_valkey":       {Available: available},
		"ingress_nginx":      {Available: available},
		"host_pod_resources": {Available: available},
	}
}

func allEvidenceSourcesAvailable(sources map[string]EvidenceSource) bool {
	for key := range requiredEvidenceSources(false) {
		if !sources[key].Available {
			return false
		}
	}
	return true
}

func runStatus(evidence ServerEvidence, stages []StageResult) string {
	if !evidence.Complete || !shadowEvidenceComplete(stages) {
		return "INCOMPLETE"
	}
	for _, stage := range stages {
		if stage.AuthorizationViolationCount > 0 ||
			stage.DesiredReportedConvergenceRate < 95 ||
			stage.OfflineDesiredConvergenceRate < 95 ||
			stage.DeltaClearSuccessRatePercent < 95 {
			return "FAIL"
		}
	}
	return "PASS"
}

func runStatusWithLoadGenerator(evidence ServerEvidence, stages []StageResult, health LoadGeneratorHealth) string {
	if health.Saturated {
		return "INCOMPLETE"
	}
	return runStatus(evidence, stages)
}

func shadowEvidenceComplete(stages []StageResult) bool {
	if len(stages) == 0 {
		return false
	}
	for _, stage := range stages {
		if stage.DesiredReportedConvergenceRate <= 0 || stage.OfflineDesiredConvergenceRate <= 0 || stage.DeltaClearSuccessRatePercent <= 0 {
			return false
		}
	}
	return true
}

func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
