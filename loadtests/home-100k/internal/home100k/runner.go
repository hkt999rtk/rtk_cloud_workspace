package home100k

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	DeviceMQTTTotals    DeviceMQTTTotals    `json:"device_mqtt_totals"`
	AppUserTotals       AppUserTotals       `json:"app_user_totals"`
	ServerEvidence      ServerEvidence      `json:"server_evidence"`
	ServerCorrelation   ServerCorrelation   `json:"server_correlation"`
	SyncTelemetry       SyncTelemetry       `json:"sync_telemetry"`
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
	Name                           string           `json:"name"`
	ConnectedDevices               int              `json:"connected_devices"`
	DeviceMQTTTotals               DeviceMQTTTotals `json:"device_mqtt_totals"`
	AppUserTotals                  AppUserTotals    `json:"app_user_totals"`
	MQTTConnectSuccessRatePercent  float64          `json:"mqtt_connect_success_rate_percent"`
	MQTTReconnectCount             int              `json:"mqtt_reconnect_count"`
	ShadowGetP50MS                 float64          `json:"shadow_get_p50_ms"`
	ShadowGetP95MS                 float64          `json:"shadow_get_p95_ms"`
	ShadowGetP99MS                 float64          `json:"shadow_get_p99_ms"`
	DesiredUpdateP95MS             float64          `json:"desired_update_p95_ms"`
	DeltaReceiveP95MS              float64          `json:"delta_receive_p95_ms"`
	DesiredReportedP95MS           float64          `json:"desired_reported_p95_ms"`
	OfflineDesiredP95MS            float64          `json:"offline_desired_p95_ms"`
	DeltaClearSuccessRatePercent   float64          `json:"delta_clear_success_rate_percent"`
	DesiredReportedConvergenceRate float64          `json:"desired_reported_convergence_rate_percent"`
	OfflineDesiredConvergenceRate  float64          `json:"offline_desired_convergence_rate_percent"`
	DuplicateApplyCount            int              `json:"duplicate_apply_count"`
	VersionConflictCount           int              `json:"version_conflict_count"`
	RejectedUpdateCount            int              `json:"rejected_update_count"`
	AuthorizationViolationCount    int              `json:"authorization_violation_count"`
	ClientTokenCorrelationCount    int              `json:"client_token_correlation_count"`
}

type DeviceMQTTTotals struct {
	ConnectAttempts   int64 `json:"connect_attempts"`
	ConnectSuccess    int64 `json:"connect_success"`
	ConnectFail       int64 `json:"connect_fail"`
	Subscribes        int64 `json:"subscribes"`
	Publishes         int64 `json:"publishes"`
	ReceivedMessages  int64 `json:"received_messages"`
	DeltaReceived     int64 `json:"delta_received"`
	ReportedPublishes int64 `json:"reported_publishes"`
	RejectedPublishes int64 `json:"rejected_publishes"`
	BytesSent         int64 `json:"bytes_sent"`
	BytesReceived     int64 `json:"bytes_received"`
}

type AppUserTotals struct {
	LoginAttempts       int64 `json:"login_attempts"`
	LoginSuccess        int64 `json:"login_success"`
	LoginFail           int64 `json:"login_fail"`
	ListDevicesRequests int64 `json:"list_devices_requests"`
	ReadShadowRequests  int64 `json:"read_shadow_requests"`
	DesiredWrites       int64 `json:"desired_writes"`
	ReceivedAcks        int64 `json:"received_acks"`
	BytesSent           int64 `json:"bytes_sent"`
	BytesReceived       int64 `json:"bytes_received"`
}

type ServerCorrelation struct {
	Status  string             `json:"status"`
	Checks  []CorrelationCheck `json:"checks,omitempty"`
	Reasons []string           `json:"reasons,omitempty"`
}

type CorrelationCheck struct {
	Source      string `json:"source"`
	Counter     string `json:"counter"`
	ClientTotal int64  `json:"client_total"`
	ServerTotal int64  `json:"server_total"`
	Delta       int64  `json:"delta"`
	Tolerance   int64  `json:"tolerance"`
	Status      string `json:"status"`
}

type SyncTelemetry struct {
	VMs []VMSyncTelemetry `json:"vms,omitempty"`
}

type VMSyncTelemetry struct {
	Label            string `json:"label"`
	FilesTransferred int64  `json:"files_transferred"`
	BytesTransferred int64  `json:"bytes_transferred"`
	ElapsedMS        int64  `json:"elapsed_ms,omitempty"`
	RemoteDiskBefore string `json:"remote_disk_before,omitempty"`
	RemoteDiskAfter  string `json:"remote_disk_after,omitempty"`
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
	Available bool             `json:"available"`
	Detail    string           `json:"detail,omitempty"`
	Counters  map[string]int64 `json:"counters,omitempty"`
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
	deviceTotals, appTotals := summarizeStageTotals(stageResults)
	correlation := correlateServerEvidence(evidence, deviceTotals, appTotals)
	status := runStatusWithCorrelation(evidence, stageResults, LoadGeneratorHealth{}, correlation)

	result := RunResult{
		RunID:               runID,
		Status:              status,
		Plan:                plan,
		StageResults:        stageResults,
		DeviceMQTTTotals:    deviceTotals,
		AppUserTotals:       appTotals,
		ServerEvidence:      evidence,
		ServerCorrelation:   correlation,
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
		ServerCorrelation:    correlation,
		SyncTelemetry:        SyncTelemetry{},
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
	syncTelemetry := loadSyncTelemetry(filepath.Join(outDir, "sync-telemetry.json"))
	evidence, err := loadServerEvidence(filepath.Join(outDir, "server-evidence.json"), runID)
	if err != nil {
		evidence = ServerEvidence{
			RunID:    runID,
			Complete: false,
			Sources:  requiredEvidenceSources(false),
			Notes:    []string{"server evidence file was not found in collected artifacts"},
		}
	}
	deviceTotals, appTotals := summarizeStageTotals(stages)
	correlation := correlateServerEvidence(evidence, deviceTotals, appTotals)
	status := runStatusWithCorrelation(evidence, stages, loadHealth, correlation)
	result := RunResult{
		RunID:               runID,
		Status:              status,
		Plan:                plan,
		StageResults:        stages,
		DeviceMQTTTotals:    deviceTotals,
		AppUserTotals:       appTotals,
		ServerEvidence:      evidence,
		ServerCorrelation:   correlation,
		SyncTelemetry:       syncTelemetry,
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
		ServerCorrelation:    correlation,
		SyncTelemetry:        syncTelemetry,
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
		result.DeviceMQTTTotals = addDeviceMQTTTotals(result.DeviceMQTTTotals, item.DeviceMQTTTotals)
		result.AppUserTotals = addAppUserTotals(result.AppUserTotals, item.AppUserTotals)
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

func summarizeStageTotals(stages []StageResult) (DeviceMQTTTotals, AppUserTotals) {
	var device DeviceMQTTTotals
	var app AppUserTotals
	for _, stage := range stages {
		device = addDeviceMQTTTotals(device, stage.DeviceMQTTTotals)
		app = addAppUserTotals(app, stage.AppUserTotals)
	}
	return device, app
}

func addDeviceMQTTTotals(a DeviceMQTTTotals, b DeviceMQTTTotals) DeviceMQTTTotals {
	a.ConnectAttempts += b.ConnectAttempts
	a.ConnectSuccess += b.ConnectSuccess
	a.ConnectFail += b.ConnectFail
	a.Subscribes += b.Subscribes
	a.Publishes += b.Publishes
	a.ReceivedMessages += b.ReceivedMessages
	a.DeltaReceived += b.DeltaReceived
	a.ReportedPublishes += b.ReportedPublishes
	a.RejectedPublishes += b.RejectedPublishes
	a.BytesSent += b.BytesSent
	a.BytesReceived += b.BytesReceived
	return a
}

func addAppUserTotals(a AppUserTotals, b AppUserTotals) AppUserTotals {
	a.LoginAttempts += b.LoginAttempts
	a.LoginSuccess += b.LoginSuccess
	a.LoginFail += b.LoginFail
	a.ListDevicesRequests += b.ListDevicesRequests
	a.ReadShadowRequests += b.ReadShadowRequests
	a.DesiredWrites += b.DesiredWrites
	a.ReceivedAcks += b.ReceivedAcks
	a.BytesSent += b.BytesSent
	a.BytesReceived += b.BytesReceived
	return a
}

func loadSyncTelemetry(path string) SyncTelemetry {
	var telemetry SyncTelemetry
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &telemetry)
	}
	byLabel := map[string]VMSyncTelemetry{}
	for _, vm := range telemetry.VMs {
		byLabel[vm.Label] = vm
	}
	matches, err := filepath.Glob(filepath.Join(strings.TrimSuffix(path, ".json")+".d", "*.json"))
	if err != nil {
		return telemetry
	}
	for _, match := range matches {
		raw, err := os.ReadFile(match)
		if err != nil {
			continue
		}
		var vm VMSyncTelemetry
		if err := json.Unmarshal(raw, &vm); err != nil || strings.TrimSpace(vm.Label) == "" {
			continue
		}
		byLabel[vm.Label] = vm
	}
	if len(byLabel) == 0 {
		return telemetry
	}
	labels := make([]string, 0, len(byLabel))
	for label := range byLabel {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	telemetry.VMs = telemetry.VMs[:0]
	for _, label := range labels {
		telemetry.VMs = append(telemetry.VMs, byLabel[label])
	}
	return telemetry
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
	if !evidence.Complete || !shadowEvidenceComplete(stages) || !clientTargetCoverageComplete(stages) {
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

func runStatusWithCorrelation(evidence ServerEvidence, stages []StageResult, health LoadGeneratorHealth, correlation ServerCorrelation) string {
	if health.Saturated {
		return "INCOMPLETE"
	}
	switch strings.ToLower(correlation.Status) {
	case "pass":
		return runStatus(evidence, stages)
	case "fail":
		return "FAIL"
	default:
		return "INCOMPLETE"
	}
}

func correlateServerEvidence(evidence ServerEvidence, device DeviceMQTTTotals, app AppUserTotals) ServerCorrelation {
	if !clientTotalsPresent(device, app) {
		return ServerCorrelation{Status: "incomplete", Reasons: []string{"client device/app totals are missing or zero"}}
	}
	checks := []CorrelationCheck{
		newCorrelationCheck("emqx", "device_mqtt.connect_success", device.ConnectSuccess, evidenceCounter(evidence, "emqx", "device_mqtt.connect_success")),
		newCorrelationCheck("emqx", "device_mqtt.publishes", device.Publishes, evidenceCounter(evidence, "emqx", "device_mqtt.publishes")),
		newCorrelationCheck("emqx", "device_mqtt.received_messages", device.ReceivedMessages, evidenceCounter(evidence, "emqx", "device_mqtt.received_messages")),
		newCorrelationCheck("video_cloud_api", "app_user.desired_writes", app.DesiredWrites, evidenceCounter(evidence, "video_cloud_api", "app_user.desired_writes")),
		newCorrelationCheck("iot_device_shadow", "device_mqtt.reported_publishes", device.ReportedPublishes, evidenceCounter(evidence, "iot_device_shadow", "device_mqtt.reported_publishes")),
		newCorrelationCheck("iot_device_shadow", "app_user.received_acks", app.ReceivedAcks, evidenceCounter(evidence, "iot_device_shadow", "app_user.received_acks")),
	}
	status := "pass"
	reasons := []string{}
	for _, check := range checks {
		if check.Status == "incomplete" {
			status = "incomplete"
			reasons = append(reasons, fmt.Sprintf("%s %s server counter missing", check.Source, check.Counter))
			continue
		}
		if check.Status == "fail" && status != "incomplete" {
			status = "fail"
		}
	}
	return ServerCorrelation{Status: status, Checks: checks, Reasons: reasons}
}

func newCorrelationCheck(source string, counter string, clientTotal int64, serverTotal int64) CorrelationCheck {
	check := CorrelationCheck{
		Source:      source,
		Counter:     counter,
		ClientTotal: clientTotal,
		ServerTotal: serverTotal,
		Delta:       serverTotal - clientTotal,
		Tolerance:   0,
		Status:      "pass",
	}
	if serverTotal == 0 {
		check.Status = "incomplete"
	} else if check.Delta != 0 {
		check.Status = "fail"
	}
	return check
}

func evidenceCounter(evidence ServerEvidence, source string, counter string) int64 {
	if evidence.Sources == nil {
		return 0
	}
	return evidence.Sources[source].Counters[counter]
}

func clientTotalsPresent(device DeviceMQTTTotals, app AppUserTotals) bool {
	return device.ConnectAttempts > 0 &&
		device.Publishes > 0 &&
		device.ReceivedMessages > 0 &&
		app.LoginAttempts > 0 &&
		app.DesiredWrites > 0 &&
		app.ReceivedAcks > 0
}

func clientTargetCoverageComplete(stages []StageResult) bool {
	if len(stages) == 0 {
		return false
	}
	for _, stage := range stages {
		if stage.ConnectedDevices <= 0 {
			return false
		}
		expectedUsers := expectedStageUsers(stage.ConnectedDevices)
		if stage.DeviceMQTTTotals.ConnectAttempts < int64(stage.ConnectedDevices) ||
			stage.DeviceMQTTTotals.ConnectSuccess < int64(stage.ConnectedDevices) ||
			stage.DeviceMQTTTotals.Subscribes < int64(stage.ConnectedDevices) ||
			stage.AppUserTotals.LoginAttempts < int64(expectedUsers) {
			return false
		}
	}
	return true
}

func expectedStageUsers(connectedDevices int) int {
	if connectedDevices <= 0 {
		return 0
	}
	users := connectedDevices * DefaultUserCount / DefaultDeviceCount
	if users <= 0 {
		return 1
	}
	return users
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
