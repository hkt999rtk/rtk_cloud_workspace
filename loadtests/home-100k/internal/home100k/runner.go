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
	RunID                 string                `json:"run_id"`
	Status                string                `json:"status"`
	Plan                  Plan                  `json:"plan"`
	StageResults          []StageResult         `json:"stage_results"`
	DeviceMQTTTotals      DeviceMQTTTotals      `json:"device_mqtt_totals"`
	AppUserTotals         AppUserTotals         `json:"app_user_totals"`
	ServerEvidence        ServerEvidence        `json:"server_evidence"`
	ServerCorrelation     ServerCorrelation     `json:"server_correlation"`
	RuntimeLogCorrelation RuntimeLogCorrelation `json:"runtime_log_correlation,omitempty"`
	StartCoordination     StartCoordination     `json:"start_coordination"`
	SyncTelemetry         SyncTelemetry         `json:"sync_telemetry"`
	LoadGeneratorHealth   LoadGeneratorHealth   `json:"load_generator_health"`
	PlanFile              string                `json:"plan_file"`
	ResultsFile           string                `json:"results_file"`
	ServerEvidenceFile    string                `json:"server_evidence_file"`
	ReportFile            string                `json:"report_file"`
}

type AggregateOptions struct {
	PlanOptions
	RunID  string
	OutDir string
}

type StageResult struct {
	Name                           string                      `json:"name"`
	ConnectedDevices               int                         `json:"connected_devices"`
	ShardConnectedDevices          int                         `json:"shard_connected_devices,omitempty"`
	DeviceMQTTTotals               DeviceMQTTTotals            `json:"device_mqtt_totals"`
	AppUserTotals                  AppUserTotals               `json:"app_user_totals"`
	MQTTConnectSuccessRatePercent  float64                     `json:"mqtt_connect_success_rate_percent"`
	MQTTReconnectCount             int                         `json:"mqtt_reconnect_count"`
	ShadowGetP50MS                 float64                     `json:"shadow_get_p50_ms"`
	ShadowGetP95MS                 float64                     `json:"shadow_get_p95_ms"`
	ShadowGetP99MS                 float64                     `json:"shadow_get_p99_ms"`
	DesiredUpdateP95MS             float64                     `json:"desired_update_p95_ms"`
	DeltaReceiveP95MS              float64                     `json:"delta_receive_p95_ms"`
	DesiredReportedP95MS           float64                     `json:"desired_reported_p95_ms"`
	OfflineDesiredP95MS            float64                     `json:"offline_desired_p95_ms"`
	DeltaClearSuccessRatePercent   float64                     `json:"delta_clear_success_rate_percent"`
	DesiredReportedConvergenceRate float64                     `json:"desired_reported_convergence_rate_percent"`
	OfflineDesiredConvergenceRate  float64                     `json:"offline_desired_convergence_rate_percent"`
	DuplicateApplyCount            int                         `json:"duplicate_apply_count"`
	VersionConflictCount           int                         `json:"version_conflict_count"`
	RejectedUpdateCount            int                         `json:"rejected_update_count"`
	AuthorizationViolationCount    int                         `json:"authorization_violation_count"`
	ClientTokenCorrelationCount    int                         `json:"client_token_correlation_count"`
	FailureReasons                 map[string]int64            `json:"failure_reasons,omitempty"`
	FailureDetails                 map[string]map[string]int64 `json:"failure_details,omitempty"`
	FailureEvents                  []FailureEvent              `json:"failure_events,omitempty"`
	CommandEvents                  []CommandEvent              `json:"command_events,omitempty"`
	DeviceTypeTotals               map[string]DeviceTypeTotals `json:"device_type_totals,omitempty"`
	UserActionTotals               map[string]int64            `json:"user_action_totals,omitempty"`
	UsageWindowTotals              map[string]int64            `json:"usage_window_totals,omitempty"`
	StageDiagnostics               []map[string]any            `json:"stage_diagnostics,omitempty"`
	PhaseMetrics                   map[string]PhaseMetric      `json:"phase_metrics,omitempty"`
	BottleneckEvents               []BottleneckEvent           `json:"bottleneck_events,omitempty"`
}

type PhaseMetric struct {
	Attempts int64 `json:"attempts"`
	Success  int64 `json:"success"`
	Fail     int64 `json:"fail"`
	TotalMS  int64 `json:"total_ms"`
	MaxMS    int64 `json:"max_ms"`
	GT1S     int64 `json:"gt1s"`
	GT5S     int64 `json:"gt5s"`
	GT10S    int64 `json:"gt10s"`
}

type BottleneckEvent struct {
	Stage       string `json:"stage,omitempty"`
	Phase       string `json:"phase"`
	Actor       string `json:"actor,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	Detail      string `json:"detail,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms,omitempty"`
	RemainingMS int64  `json:"remaining_ms,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
	IsRetry     bool   `json:"is_retry,omitempty"`
	MQTTTarget  string `json:"mqtt_target,omitempty"`
	OccurredAt  string `json:"occurred_at,omitempty"`
}

type DeviceTypeTotals struct {
	TelemetryPublishes int64 `json:"telemetry_publishes"`
	EventPublishes     int64 `json:"event_publishes"`
	DesiredWrites      int64 `json:"desired_writes"`
	DeltaReceived      int64 `json:"delta_received"`
	ReportedPublishes  int64 `json:"reported_publishes"`
	BytesSent          int64 `json:"bytes_sent"`
	BytesReceived      int64 `json:"bytes_received"`
}

type FailureEvent struct {
	Stage       string `json:"stage,omitempty"`
	Reason      string `json:"reason"`
	Detail      string `json:"detail,omitempty"`
	Phase       string `json:"phase,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	CommandID   string `json:"command_id,omitempty"`
	EventIndex  int    `json:"event_index,omitempty"`
	SessionSlot int    `json:"session_slot,omitempty"`
	RemainingMS int64  `json:"remaining_ms,omitempty"`
	MQTTTarget  string `json:"mqtt_target,omitempty"`
	ReaderError string `json:"reader_error,omitempty"`
	OccurredAt  string `json:"occurred_at,omitempty"`
}

type CommandEvent struct {
	Stage              string      `json:"stage,omitempty"`
	DeviceID           string      `json:"device_id"`
	CommandID          string      `json:"command_id"`
	RuntimeLogStreamID string      `json:"runtime_log_stream_id"`
	EventIndex         int         `json:"event_index,omitempty"`
	SessionSlot        int         `json:"session_slot,omitempty"`
	MQTTTarget         string      `json:"mqtt_target,omitempty"`
	ExpectedLogs       []LogExpect `json:"expected_logs,omitempty"`
	OccurredAt         string      `json:"occurred_at,omitempty"`
}

type LogExpect struct {
	Seq     int    `json:"seq"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

type DeviceMQTTTotals struct {
	ConnectAttempts          int64 `json:"connect_attempts"`
	ConnectSuccess           int64 `json:"connect_success"`
	ConnectFail              int64 `json:"connect_fail"`
	TokenAttempts            int64 `json:"token_attempts,omitempty"`
	TokenSuccess             int64 `json:"token_success,omitempty"`
	TokenFail                int64 `json:"token_fail,omitempty"`
	TokenFirstAttemptSuccess int64 `json:"token_first_attempt_success,omitempty"`
	TokenFirstAttemptFail    int64 `json:"token_first_attempt_fail,omitempty"`
	TokenRetryAttempts       int64 `json:"token_retry_attempts,omitempty"`
	TokenRetrySuccess        int64 `json:"token_retry_success,omitempty"`
	TokenRetryExhausted      int64 `json:"token_retry_exhausted,omitempty"`
	MQTTDialAttempts         int64 `json:"mqtt_dial_attempts,omitempty"`
	MQTTDialSuccess          int64 `json:"mqtt_dial_success,omitempty"`
	MQTTDialFail             int64 `json:"mqtt_dial_fail,omitempty"`
	MQTTConnackAttempts      int64 `json:"mqtt_connack_attempts,omitempty"`
	MQTTConnackSuccess       int64 `json:"mqtt_connack_success,omitempty"`
	MQTTConnackFail          int64 `json:"mqtt_connack_fail,omitempty"`
	SubscribeAttempts        int64 `json:"subscribe_attempts,omitempty"`
	SubscribeFail            int64 `json:"subscribe_fail,omitempty"`
	Subscribes               int64 `json:"subscribes"`
	ActiveConnections        int64 `json:"active_connections,omitempty"`
	ActiveSubscriptions      int64 `json:"active_subscriptions,omitempty"`
	Publishes                int64 `json:"publishes"`
	ReceivedMessages         int64 `json:"received_messages"`
	DeltaReceived            int64 `json:"delta_received"`
	ReportedPublishes        int64 `json:"reported_publishes"`
	RejectedPublishes        int64 `json:"rejected_publishes"`
	BytesSent                int64 `json:"bytes_sent"`
	BytesReceived            int64 `json:"bytes_received"`
}

type AppUserTotals struct {
	LoginAttempts            int64 `json:"login_attempts"`
	LoginSuccess             int64 `json:"login_success"`
	LoginFail                int64 `json:"login_fail"`
	TokenAttempts            int64 `json:"token_attempts,omitempty"`
	TokenSuccess             int64 `json:"token_success,omitempty"`
	TokenFail                int64 `json:"token_fail,omitempty"`
	TokenFirstAttemptSuccess int64 `json:"token_first_attempt_success,omitempty"`
	TokenFirstAttemptFail    int64 `json:"token_first_attempt_fail,omitempty"`
	TokenRetryAttempts       int64 `json:"token_retry_attempts,omitempty"`
	TokenRetrySuccess        int64 `json:"token_retry_success,omitempty"`
	TokenRetryExhausted      int64 `json:"token_retry_exhausted,omitempty"`
	MQTTDialAttempts         int64 `json:"mqtt_dial_attempts,omitempty"`
	MQTTDialSuccess          int64 `json:"mqtt_dial_success,omitempty"`
	MQTTDialFail             int64 `json:"mqtt_dial_fail,omitempty"`
	MQTTConnackAttempts      int64 `json:"mqtt_connack_attempts,omitempty"`
	MQTTConnackSuccess       int64 `json:"mqtt_connack_success,omitempty"`
	MQTTConnackFail          int64 `json:"mqtt_connack_fail,omitempty"`
	ListDevicesRequests      int64 `json:"list_devices_requests"`
	ReadShadowRequests       int64 `json:"read_shadow_requests"`
	DesiredWrites            int64 `json:"desired_writes"`
	ReceivedAcks             int64 `json:"received_acks"`
	BytesSent                int64 `json:"bytes_sent"`
	BytesReceived            int64 `json:"bytes_received"`
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

type RuntimeLogCorrelation struct {
	Status                 string                    `json:"status,omitempty"`
	ClientCommandEvents    int                       `json:"client_command_events"`
	ServerRuntimeStreams   int64                     `json:"server_runtime_streams"`
	MissingStreamCount     int                       `json:"missing_stream_count"`
	MissingSequenceCount   int                       `json:"missing_sequence_count"`
	MissingStreamSamples   []RuntimeLogMissingStream `json:"missing_stream_samples,omitempty"`
	MissingSequenceSamples []RuntimeLogMissingSeq    `json:"missing_sequence_samples,omitempty"`
}

type RuntimeLogMissingStream struct {
	Stage              string `json:"stage,omitempty"`
	DeviceID           string `json:"device_id"`
	CommandID          string `json:"command_id"`
	RuntimeLogStreamID string `json:"runtime_log_stream_id"`
}

type RuntimeLogMissingSeq struct {
	Stage              string `json:"stage,omitempty"`
	DeviceID           string `json:"device_id"`
	CommandID          string `json:"command_id"`
	RuntimeLogStreamID string `json:"runtime_log_stream_id"`
	Seq                int    `json:"seq"`
	Source             string `json:"source,omitempty"`
	Message            string `json:"message,omitempty"`
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

type StartCoordination struct {
	Mode         string             `json:"mode,omitempty"`
	ReadyBarrier string             `json:"ready_barrier,omitempty"`
	StartDelayMS int                `json:"start_delay_ms,omitempty"`
	MaxSkewMS    int64              `json:"max_skew_ms,omitempty"`
	VMs          []VMStartTelemetry `json:"vms,omitempty"`
}

type VMStartTelemetry struct {
	Label                  string `json:"label"`
	IP                     string `json:"ip,omitempty"`
	RunID                  string `json:"run_id,omitempty"`
	ReadyAt                string `json:"ready_at,omitempty"`
	StartSignalReceivedAt  string `json:"start_signal_received_at,omitempty"`
	StageStartedAt         string `json:"stage_started_at,omitempty"`
	FirstConnectAt         string `json:"first_connect_at,omitempty"`
	StageCompletedAt       string `json:"stage_completed_at,omitempty"`
	CoordinatorDisconnects int    `json:"coordinator_disconnect_count,omitempty"`
	Status                 string `json:"status,omitempty"`
	Error                  string `json:"error,omitempty"`
}

type ServerEvidence struct {
	RunID               string                    `json:"run_id"`
	EvidenceWindowStart string                    `json:"evidence_window_start,omitempty"`
	EvidenceWindowMode  string                    `json:"evidence_window_mode,omitempty"`
	Complete            bool                      `json:"complete"`
	Sources             map[string]EvidenceSource `json:"sources"`
	Notes               []string                  `json:"notes,omitempty"`
}

type EvidenceSource struct {
	Available bool                     `json:"available"`
	Optional  bool                     `json:"optional,omitempty"`
	Detail    string                   `json:"detail,omitempty"`
	Counters  map[string]int64         `json:"counters,omitempty"`
	Samples   []EvidenceResourceSample `json:"samples,omitempty"`
}

type EvidenceResourceSample struct {
	Kind        string `json:"kind"`
	Namespace   string `json:"namespace,omitempty"`
	Pod         string `json:"pod,omitempty"`
	Container   string `json:"container,omitempty"`
	CPUCoreMil  int64  `json:"cpu_millicores,omitempty"`
	MemoryBytes int64  `json:"memory_bytes,omitempty"`
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
	runtimeLogCorrelation := correlateRuntimeLogs(evidence, stageResults)
	status := runStatusWithCorrelation(plan, evidence, stageResults, LoadGeneratorHealth{}, correlation)
	status = statusWithRuntimeLogCorrelation(status, runtimeLogCorrelation)

	result := RunResult{
		RunID:                 runID,
		Status:                status,
		Plan:                  plan,
		StageResults:          stageResults,
		DeviceMQTTTotals:      deviceTotals,
		AppUserTotals:         appTotals,
		ServerEvidence:        evidence,
		ServerCorrelation:     correlation,
		RuntimeLogCorrelation: runtimeLogCorrelation,
		LoadGeneratorHealth:   LoadGeneratorHealth{},
		PlanFile:              filepath.Join(outDir, "plan.json"),
		ResultsFile:           filepath.Join(outDir, "results.json"),
		ServerEvidenceFile:    filepath.Join(outDir, "server-evidence.json"),
		ReportFile:            filepath.Join(outDir, "TEST_REPORT.md"),
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
		Plan:                  plan,
		RunID:                 runID,
		ShadowEvidenceFound:   shadowEvidenceComplete(stageResults),
		ServerEvidenceFound:   evidence.Complete,
		LoadGeneratorHealthy:  true,
		StageResults:          stageResults,
		ServerEvidence:        evidence,
		ServerCorrelation:     correlation,
		RuntimeLogCorrelation: runtimeLogCorrelation,
		SyncTelemetry:         SyncTelemetry{},
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
	collected, err := loadCollectedShardResults(filepath.Join(outDir, "shards"), plan.Stages)
	if err != nil {
		return RunResult{}, err
	}
	stages := collected.StageResults
	loadHealth := collected.LoadGeneratorHealth
	syncTelemetry := loadSyncTelemetry(filepath.Join(outDir, "sync-telemetry.json"))
	startCoordination := loadStartCoordination(filepath.Join(outDir, "start-coordination.json"))
	evidence, err := loadServerEvidence(filepath.Join(outDir, "server-evidence.json"), runID)
	if err != nil {
		evidence = ServerEvidence{
			RunID:    runID,
			Complete: false,
			Sources:  evidenceSourceCatalog(false),
			Notes:    []string{"server evidence file was not found in collected artifacts"},
		}
	}
	deviceTotals, appTotals := summarizeStageTotals(stages)
	correlation := correlateServerEvidence(evidence, deviceTotals, appTotals)
	runtimeLogCorrelation := correlateRuntimeLogs(evidence, stages)
	status := runStatusWithCorrelation(plan, evidence, stages, loadHealth, correlation)
	status = statusWithRuntimeLogCorrelation(status, runtimeLogCorrelation)
	result := RunResult{
		RunID:                 runID,
		Status:                status,
		Plan:                  plan,
		StageResults:          stages,
		DeviceMQTTTotals:      deviceTotals,
		AppUserTotals:         appTotals,
		ServerEvidence:        evidence,
		ServerCorrelation:     correlation,
		RuntimeLogCorrelation: runtimeLogCorrelation,
		StartCoordination:     startCoordination,
		SyncTelemetry:         syncTelemetry,
		LoadGeneratorHealth:   loadHealth,
		PlanFile:              filepath.Join(outDir, "plan.json"),
		ResultsFile:           filepath.Join(outDir, "results.json"),
		ServerEvidenceFile:    filepath.Join(outDir, "server-evidence.json"),
		ReportFile:            filepath.Join(outDir, "TEST_REPORT.md"),
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
		Plan:                  plan,
		RunID:                 runID,
		ShadowEvidenceFound:   shadowEvidenceComplete(stages),
		ServerEvidenceFound:   evidence.Complete,
		LoadGeneratorHealthy:  !loadHealth.Saturated,
		StageResults:          stages,
		ServerEvidence:        evidence,
		ServerCorrelation:     correlation,
		RuntimeLogCorrelation: runtimeLogCorrelation,
		StartCoordination:     startCoordination,
		SyncTelemetry:         syncTelemetry,
		Notes:                 loadHealth.Reasons,
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
			Sources:  evidenceSourceCatalog(false),
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
	normalizeEvidenceSourceCatalogMetadata(evidence.Sources)
	recomputeVideoCloudAPITopLevelCounters(&evidence)
	evidence.Complete = evidence.Complete && allEvidenceSourcesAvailable(evidence.Sources)
	return evidence, nil
}

type collectedShardResults struct {
	StageResults        []StageResult
	LoadGeneratorHealth LoadGeneratorHealth
}

func loadStartCoordination(path string) StartCoordination {
	var coordination StartCoordination
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &coordination)
	}
	return coordination
}

func loadCollectedShardResults(shardsDir string, stages []Stage) (collectedShardResults, error) {
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
	results := []StageResult{}
	for _, stage := range stages {
		items := byStage[stage.Name]
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
		result.ShardConnectedDevices += item.ShardConnectedDevices
		result.MQTTConnectSuccessRatePercent += item.MQTTConnectSuccessRatePercent
		result.DeviceMQTTTotals = addDeviceMQTTShardTotals(result.DeviceMQTTTotals, item.DeviceMQTTTotals)
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
		result.FailureReasons = addFailureReasons(result.FailureReasons, item.FailureReasons)
		result.FailureDetails = addFailureDetails(result.FailureDetails, item.FailureDetails)
		result.FailureEvents = appendStageFailureEvents(result.FailureEvents, item.FailureEvents)
		result.CommandEvents = append(result.CommandEvents, item.CommandEvents...)
		result.DeviceTypeTotals = addDeviceTypeTotals(result.DeviceTypeTotals, item.DeviceTypeTotals)
		result.UserActionTotals = addInt64MapTotals(result.UserActionTotals, item.UserActionTotals)
		result.UsageWindowTotals = addInt64MapTotals(result.UsageWindowTotals, item.UsageWindowTotals)
		result.StageDiagnostics = append(result.StageDiagnostics, item.StageDiagnostics...)
		result.PhaseMetrics = addPhaseMetrics(result.PhaseMetrics, item.PhaseMetrics)
		result.BottleneckEvents = appendBottleneckEvents(result.BottleneckEvents, item.BottleneckEvents)
	}
	count := float64(len(items))
	result.MQTTConnectSuccessRatePercent = connectSuccessPercent(result.DeviceMQTTTotals)
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

const maxAggregatedBottleneckEvents = 128

func addPhaseMetrics(left, right map[string]PhaseMetric) map[string]PhaseMetric {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	if left == nil {
		left = map[string]PhaseMetric{}
	}
	for phase, value := range right {
		current := left[phase]
		current.Attempts += value.Attempts
		current.Success += value.Success
		current.Fail += value.Fail
		current.TotalMS += value.TotalMS
		current.MaxMS = maxInt64(current.MaxMS, value.MaxMS)
		current.GT1S += value.GT1S
		current.GT5S += value.GT5S
		current.GT10S += value.GT10S
		left[phase] = current
	}
	return left
}

func appendBottleneckEvents(left, right []BottleneckEvent) []BottleneckEvent {
	if len(right) == 0 || len(left) >= maxAggregatedBottleneckEvents {
		return left
	}
	for _, event := range right {
		if len(left) >= maxAggregatedBottleneckEvents {
			break
		}
		left = append(left, event)
	}
	return left
}

func addDeviceTypeTotals(left, right map[string]DeviceTypeTotals) map[string]DeviceTypeTotals {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	merged := map[string]DeviceTypeTotals{}
	for name, value := range left {
		merged[name] = addDeviceTypeTotal(merged[name], value)
	}
	for name, value := range right {
		merged[name] = addDeviceTypeTotal(merged[name], value)
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func addDeviceTypeTotal(left, right DeviceTypeTotals) DeviceTypeTotals {
	return DeviceTypeTotals{
		TelemetryPublishes: left.TelemetryPublishes + right.TelemetryPublishes,
		EventPublishes:     left.EventPublishes + right.EventPublishes,
		DesiredWrites:      left.DesiredWrites + right.DesiredWrites,
		DeltaReceived:      left.DeltaReceived + right.DeltaReceived,
		ReportedPublishes:  left.ReportedPublishes + right.ReportedPublishes,
		BytesSent:          left.BytesSent + right.BytesSent,
		BytesReceived:      left.BytesReceived + right.BytesReceived,
	}
}

func addInt64MapTotals(left, right map[string]int64) map[string]int64 {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	merged := map[string]int64{}
	for name, value := range left {
		if value != 0 {
			merged[name] += value
		}
	}
	for name, value := range right {
		if value != 0 {
			merged[name] += value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func connectSuccessPercent(totals DeviceMQTTTotals) float64 {
	if totals.ConnectAttempts <= 0 {
		return 0
	}
	return float64(totals.ConnectSuccess) / float64(totals.ConnectAttempts) * 100
}

func addFailureReasons(left, right map[string]int64) map[string]int64 {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	merged := map[string]int64{}
	for key, value := range left {
		if value != 0 {
			merged[key] += value
		}
	}
	for key, value := range right {
		if value != 0 {
			merged[key] += value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func addFailureDetails(left, right map[string]map[string]int64) map[string]map[string]int64 {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	merged := map[string]map[string]int64{}
	for reason, details := range left {
		for detail, count := range details {
			if count == 0 {
				continue
			}
			if merged[reason] == nil {
				merged[reason] = map[string]int64{}
			}
			merged[reason][detail] += count
		}
	}
	for reason, details := range right {
		for detail, count := range details {
			if count == 0 {
				continue
			}
			if merged[reason] == nil {
				merged[reason] = map[string]int64{}
			}
			merged[reason][detail] += count
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

const maxAggregatedFailureEvents = 128

func appendStageFailureEvents(left, right []FailureEvent) []FailureEvent {
	if len(right) == 0 || len(left) >= maxAggregatedFailureEvents {
		return left
	}
	for _, event := range right {
		if len(left) >= maxAggregatedFailureEvents {
			break
		}
		left = append(left, event)
	}
	return left
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
	a.TokenAttempts += b.TokenAttempts
	a.TokenSuccess += b.TokenSuccess
	a.TokenFail += b.TokenFail
	a.TokenFirstAttemptSuccess += b.TokenFirstAttemptSuccess
	a.TokenFirstAttemptFail += b.TokenFirstAttemptFail
	a.TokenRetryAttempts += b.TokenRetryAttempts
	a.TokenRetrySuccess += b.TokenRetrySuccess
	a.TokenRetryExhausted += b.TokenRetryExhausted
	a.MQTTDialAttempts += b.MQTTDialAttempts
	a.MQTTDialSuccess += b.MQTTDialSuccess
	a.MQTTDialFail += b.MQTTDialFail
	a.MQTTConnackAttempts += b.MQTTConnackAttempts
	a.MQTTConnackSuccess += b.MQTTConnackSuccess
	a.MQTTConnackFail += b.MQTTConnackFail
	a.SubscribeAttempts += b.SubscribeAttempts
	a.SubscribeFail += b.SubscribeFail
	a.Subscribes += b.Subscribes
	a.ActiveConnections = maxInt64(a.ActiveConnections, b.ActiveConnections)
	a.ActiveSubscriptions = maxInt64(a.ActiveSubscriptions, b.ActiveSubscriptions)
	a.Publishes += b.Publishes
	a.ReceivedMessages += b.ReceivedMessages
	a.DeltaReceived += b.DeltaReceived
	a.ReportedPublishes += b.ReportedPublishes
	a.RejectedPublishes += b.RejectedPublishes
	a.BytesSent += b.BytesSent
	a.BytesReceived += b.BytesReceived
	return a
}

func addDeviceMQTTShardTotals(a DeviceMQTTTotals, b DeviceMQTTTotals) DeviceMQTTTotals {
	activeConnections := a.ActiveConnections + nonZeroInt64(b.ActiveConnections, b.ConnectSuccess)
	activeSubscriptions := a.ActiveSubscriptions + nonZeroInt64(b.ActiveSubscriptions, b.Subscribes)
	a = addDeviceMQTTTotals(a, b)
	a.ActiveConnections = activeConnections
	a.ActiveSubscriptions = activeSubscriptions
	return a
}

func addAppUserTotals(a AppUserTotals, b AppUserTotals) AppUserTotals {
	a.LoginAttempts += b.LoginAttempts
	a.LoginSuccess += b.LoginSuccess
	a.LoginFail += b.LoginFail
	a.TokenAttempts += b.TokenAttempts
	a.TokenSuccess += b.TokenSuccess
	a.TokenFail += b.TokenFail
	a.TokenFirstAttemptSuccess += b.TokenFirstAttemptSuccess
	a.TokenFirstAttemptFail += b.TokenFirstAttemptFail
	a.TokenRetryAttempts += b.TokenRetryAttempts
	a.TokenRetrySuccess += b.TokenRetrySuccess
	a.TokenRetryExhausted += b.TokenRetryExhausted
	a.MQTTDialAttempts += b.MQTTDialAttempts
	a.MQTTDialSuccess += b.MQTTDialSuccess
	a.MQTTDialFail += b.MQTTDialFail
	a.MQTTConnackAttempts += b.MQTTConnackAttempts
	a.MQTTConnackSuccess += b.MQTTConnackSuccess
	a.MQTTConnackFail += b.MQTTConnackFail
	a.ListDevicesRequests += b.ListDevicesRequests
	a.ReadShadowRequests += b.ReadShadowRequests
	a.DesiredWrites += b.DesiredWrites
	a.ReceivedAcks += b.ReceivedAcks
	a.BytesSent += b.BytesSent
	a.BytesReceived += b.BytesReceived
	return a
}

func maxInt64(a, b int64) int64 {
	if b > a {
		return b
	}
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
		"redis_valkey":       {Available: available},
		"postgres":           {Available: available},
		"ingress_nginx":      {Available: available},
		"host_pod_resources": {Available: available},
	}
}

func optionalEvidenceSources(available bool) map[string]EvidenceSource {
	return map[string]EvidenceSource{
		"central_logger":            {Available: available, Optional: true},
		"mqtt_nodebalancer":         {Available: available, Optional: true},
		"iot_device_shadow":         {Available: available, Optional: true},
		"iot_device_shadow_streams": {Available: available, Optional: true},
	}
}

func evidenceSourceCatalog(available bool) map[string]EvidenceSource {
	sources := requiredEvidenceSources(available)
	for key, source := range optionalEvidenceSources(available) {
		sources[key] = source
	}
	return sources
}

func allEvidenceSourcesAvailable(sources map[string]EvidenceSource) bool {
	for key := range requiredEvidenceSources(false) {
		if !sources[key].Available {
			return false
		}
	}
	return true
}

func runStatus(plan Plan, evidence ServerEvidence, stages []StageResult) string {
	if !evidence.Complete || !shadowEvidenceComplete(stages) || !clientTargetCoverageComplete(plan.Conditions, stages) || len(missingDeviceTypeEvidence(plan, stages)) > 0 {
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

func runStatusWithLoadGenerator(plan Plan, evidence ServerEvidence, stages []StageResult, health LoadGeneratorHealth) string {
	if health.Saturated {
		return "INCOMPLETE"
	}
	return runStatus(plan, evidence, stages)
}

func runStatusWithCorrelation(plan Plan, evidence ServerEvidence, stages []StageResult, health LoadGeneratorHealth, correlation ServerCorrelation) string {
	if health.Saturated {
		return "INCOMPLETE"
	}
	switch strings.ToLower(correlation.Status) {
	case "pass":
		return runStatus(plan, evidence, stages)
	case "fail":
		return "FAIL"
	default:
		return "INCOMPLETE"
	}
}

func statusWithRuntimeLogCorrelation(status string, correlation RuntimeLogCorrelation) string {
	switch strings.ToLower(strings.TrimSpace(correlation.Status)) {
	case "fail":
		if status == "INCOMPLETE" {
			return status
		}
		return "FAIL"
	case "incomplete":
		return "INCOMPLETE"
	default:
		return status
	}
}

func correlateServerEvidence(evidence ServerEvidence, device DeviceMQTTTotals, app AppUserTotals) ServerCorrelation {
	if reasons := missingClientCounterReasons(device, app); len(reasons) > 0 {
		return ServerCorrelation{Status: "incomplete", Reasons: reasons}
	}
	reasons := []string{}
	if evidenceCounterAny(evidence, "emqx.broker.identity", "emqx", "emqx_listener_stats") == 0 {
		reasons = append(reasons, "EMQX broker identity missing from server evidence")
	}
	totalMQTTConnectSuccess := device.ConnectSuccess + app.MQTTConnackSuccess
	serverTotalMQTTConnectSuccess := evidenceCounter(evidence, "emqx", "mqtt.total_connect_success")
	if serverTotalMQTTConnectSuccess == 0 {
		serverTotalMQTTConnectSuccess = evidenceCounter(evidence, "emqx", "device_mqtt.connect_success")
	}
	checks := []CorrelationCheck{
		newAtLeastCorrelationCheck("emqx", "mqtt.total_connect_success", totalMQTTConnectSuccess, serverTotalMQTTConnectSuccess),
		newAtLeastCorrelationCheck("redis_valkey", "shadow.docs", 1, evidenceCounter(evidence, "redis_valkey", "redis_valkey.shadow.docs")),
		newAtLeastCorrelationCheck("redis_valkey", "command.set.calls", 1, evidenceCounter(evidence, "redis_valkey", "redis_valkey.command.set.calls")),
	}
	status := "pass"
	if len(reasons) > 0 {
		status = "incomplete"
	}
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

func correlateRuntimeLogs(evidence ServerEvidence, stages []StageResult) RuntimeLogCorrelation {
	events := []CommandEvent{}
	for _, stage := range stages {
		events = append(events, stage.CommandEvents...)
	}
	source := evidence.Sources["iot_device_shadow_streams"]
	result := RuntimeLogCorrelation{
		Status:               "pass",
		ClientCommandEvents:  len(events),
		ServerRuntimeStreams: evidenceCounter(evidence, "iot_device_shadow_streams", "runtime_log_streams.total"),
	}
	if len(events) == 0 {
		result.Status = "skipped"
		return result
	}
	if !source.Available || len(source.Counters) == 0 {
		result.Status = "skipped"
		return result
	}
	for _, event := range events {
		streamKey := "runtime_log_stream." + event.RuntimeLogStreamID + ".entries"
		if source.Counters[streamKey] == 0 {
			result.MissingStreamCount++
			if len(result.MissingStreamSamples) < 20 {
				result.MissingStreamSamples = append(result.MissingStreamSamples, RuntimeLogMissingStream{
					Stage:              event.Stage,
					DeviceID:           event.DeviceID,
					CommandID:          event.CommandID,
					RuntimeLogStreamID: event.RuntimeLogStreamID,
				})
			}
			continue
		}
		for _, expected := range event.ExpectedLogs {
			seqKey := fmt.Sprintf("runtime_log_stream.%s.seq.%d", event.RuntimeLogStreamID, expected.Seq)
			if source.Counters[seqKey] == 0 {
				result.MissingSequenceCount++
				if len(result.MissingSequenceSamples) < 20 {
					result.MissingSequenceSamples = append(result.MissingSequenceSamples, RuntimeLogMissingSeq{
						Stage:              event.Stage,
						DeviceID:           event.DeviceID,
						CommandID:          event.CommandID,
						RuntimeLogStreamID: event.RuntimeLogStreamID,
						Seq:                expected.Seq,
						Source:             expected.Source,
						Message:            expected.Message,
					})
				}
			}
		}
	}
	if result.MissingStreamCount > 0 || result.MissingSequenceCount > 0 {
		result.Status = "fail"
	}
	return result
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

func newAtLeastCorrelationCheck(source string, counter string, minimum int64, serverTotal int64) CorrelationCheck {
	check := CorrelationCheck{
		Source:      source,
		Counter:     counter,
		ClientTotal: minimum,
		ServerTotal: serverTotal,
		Delta:       serverTotal - minimum,
		Tolerance:   0,
		Status:      "pass",
	}
	if serverTotal == 0 {
		check.Status = "incomplete"
	} else if serverTotal < minimum {
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

func evidenceCounterAny(evidence ServerEvidence, counter string, sources ...string) int64 {
	var total int64
	for _, source := range sources {
		total += evidenceCounter(evidence, source, counter)
	}
	return total
}

func missingClientCounterReasons(device DeviceMQTTTotals, app AppUserTotals) []string {
	var reasons []string
	if device.ConnectAttempts == 0 {
		reasons = append(reasons, "client device MQTT connect attempts are zero")
	}
	if device.ConnectSuccess == 0 {
		reasons = append(reasons, "client device MQTT connect successes are zero")
	}
	if device.Subscribes == 0 {
		reasons = append(reasons, "client device MQTT subscribes are zero")
	}
	if device.Publishes == 0 {
		reasons = append(reasons, "client device MQTT publishes are zero; shadow reported path did not run")
	}
	if device.ReceivedMessages == 0 {
		reasons = append(reasons, "client device MQTT received messages are zero; shadow delta path did not run")
	}
	if app.TokenAttempts == 0 {
		reasons = append(reasons, "client app token attempts are zero")
	}
	if app.DesiredWrites == 0 {
		reasons = append(reasons, "client app desired writes are zero; shadow desired path did not run")
	}
	if app.ReceivedAcks == 0 {
		reasons = append(reasons, "client app received ACKs are zero; app-side shadow confirmation did not run")
	}
	return reasons
}

func clientTargetCoverageComplete(conditions TestConditions, stages []StageResult) bool {
	if len(stages) == 0 {
		return false
	}
	for _, stage := range stages {
		if stage.ConnectedDevices <= 0 {
			return false
		}
		expectedUsers := expectedStageUsers(conditions, stage.ConnectedDevices)
		activeConnections := nonZeroInt64(stage.DeviceMQTTTotals.ActiveConnections, stage.DeviceMQTTTotals.ConnectSuccess)
		activeSubscriptions := nonZeroInt64(stage.DeviceMQTTTotals.ActiveSubscriptions, stage.DeviceMQTTTotals.Subscribes)
		if activeConnections < int64(stage.ConnectedDevices) ||
			activeSubscriptions < int64(stage.ConnectedDevices) ||
			stage.AppUserTotals.DesiredWrites < int64(expectedUsers) ||
			stage.AppUserTotals.ReceivedAcks < int64(expectedUsers) {
			return false
		}
	}
	return true
}

func expectedStageUsers(conditions TestConditions, connectedDevices int) int {
	if connectedDevices <= 0 {
		return 0
	}
	totalDevices := conditions.Devices
	if totalDevices <= 0 {
		totalDevices = DefaultDeviceCount
	}
	totalUsers := conditions.Users
	if totalUsers <= 0 {
		totalUsers = DefaultUserCount
	}
	users := connectedDevices * totalUsers / totalDevices
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
