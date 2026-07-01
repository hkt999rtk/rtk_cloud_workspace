package home100k

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	Result                string                `json:"result,omitempty"`
	Plan                  Plan                  `json:"plan"`
	StageResults          []StageResult         `json:"stage_results"`
	DeviceMQTTTotals      DeviceMQTTTotals      `json:"device_mqtt_totals"`
	AppUserTotals         AppUserTotals         `json:"app_user_totals"`
	ServerEvidence        ServerEvidence        `json:"server_evidence"`
	ServerCorrelation     ServerCorrelation     `json:"server_correlation"`
	RuntimeLogCorrelation RuntimeLogCorrelation `json:"runtime_log_correlation,omitempty"`
	VideoEvidence         VideoEvidence         `json:"video_evidence,omitempty"`
	StartCoordination     StartCoordination     `json:"start_coordination"`
	SyncTelemetry         SyncTelemetry         `json:"sync_telemetry"`
	LoadGeneratorHealth   LoadGeneratorHealth   `json:"load_generator_health"`
	PlanFile              string                `json:"plan_file"`
	ResultsFile           string                `json:"results_file"`
	ServerEvidenceFile    string                `json:"server_evidence_file"`
	ReportFile            string                `json:"report_file"`
}

type RunOutcome struct {
	Status  string   `json:"status"`
	Result  string   `json:"result,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
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
}

type VideoEvidence struct {
	Complete    bool              `json:"complete"`
	WebRTC      WebRTCTotals      `json:"webrtc_totals"`
	WebRTCMedia WebRTCMediaTotals `json:"webrtc_media_totals,omitempty"`
	TURN        TURNEvidence      `json:"turn_evidence,omitempty"`
	Notes       []string          `json:"notes,omitempty"`
}

type WebRTCTotals struct {
	CreateAttempts     int64   `json:"create_attempts"`
	CreateSuccess      int64   `json:"create_success"`
	SetupAttempts      int64   `json:"setup_attempts"`
	SetupSuccess       int64   `json:"setup_success"`
	CloseAttempts      int64   `json:"close_attempts"`
	CloseSuccess       int64   `json:"close_success"`
	SuccessRatePercent float64 `json:"success_rate_percent"`
	SetupP95MS         int64   `json:"setup_p95_ms"`
	SetupP99MS         int64   `json:"setup_p99_ms"`
	ICEServerCount     int     `json:"ice_server_count"`
	OpenSessions       int     `json:"open_sessions"`
}

type WebRTCMediaTotals struct {
	Enabled             bool  `json:"enabled,omitempty"`
	Attempts            int64 `json:"attempts,omitempty"`
	Successes           int64 `json:"successes,omitempty"`
	Failures            int64 `json:"failures,omitempty"`
	ICEConnectedP95MS   int64 `json:"ice_connected_p95_ms,omitempty"`
	TimeToFirstRTPP95MS int64 `json:"time_to_first_rtp_p95_ms,omitempty"`
	PacketsReceived     int64 `json:"packets_received,omitempty"`
	BytesReceived       int64 `json:"bytes_received,omitempty"`
	H264PacketsReceived int64 `json:"h264_packets_received,omitempty"`
	H264BytesReceived   int64 `json:"h264_bytes_received,omitempty"`
	OpusPacketsReceived int64 `json:"opus_packets_received,omitempty"`
	OpusBytesReceived   int64 `json:"opus_bytes_received,omitempty"`
}

type TURNEvidence struct {
	RegistryAvailable bool  `json:"registry_available"`
	ActiveNodes       int64 `json:"active_nodes,omitempty"`
	CoturnAvailable   bool  `json:"coturn_available"`
	Allocations       int64 `json:"allocations,omitempty"`
	ActiveSessions    int64 `json:"active_sessions,omitempty"`
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
	ConnectAttempts     int64 `json:"connect_attempts"`
	ConnectSuccess      int64 `json:"connect_success"`
	ConnectFail         int64 `json:"connect_fail"`
	TokenAttempts       int64 `json:"token_attempts,omitempty"`
	TokenSuccess        int64 `json:"token_success,omitempty"`
	TokenFail           int64 `json:"token_fail,omitempty"`
	MQTTDialAttempts    int64 `json:"mqtt_dial_attempts,omitempty"`
	MQTTDialSuccess     int64 `json:"mqtt_dial_success,omitempty"`
	MQTTDialFail        int64 `json:"mqtt_dial_fail,omitempty"`
	MQTTConnackAttempts int64 `json:"mqtt_connack_attempts,omitempty"`
	MQTTConnackSuccess  int64 `json:"mqtt_connack_success,omitempty"`
	MQTTConnackFail     int64 `json:"mqtt_connack_fail,omitempty"`
	SubscribeAttempts   int64 `json:"subscribe_attempts,omitempty"`
	SubscribeFail       int64 `json:"subscribe_fail,omitempty"`
	Subscribes          int64 `json:"subscribes"`
	ActiveConnections   int64 `json:"active_connections,omitempty"`
	ActiveSubscriptions int64 `json:"active_subscriptions,omitempty"`
	Publishes           int64 `json:"publishes"`
	ReceivedMessages    int64 `json:"received_messages"`
	DeltaReceived       int64 `json:"delta_received"`
	ReportedPublishes   int64 `json:"reported_publishes"`
	RejectedPublishes   int64 `json:"rejected_publishes"`
	BytesSent           int64 `json:"bytes_sent"`
	BytesReceived       int64 `json:"bytes_received"`
}

type AppUserTotals struct {
	LoginAttempts       int64 `json:"login_attempts"`
	LoginSuccess        int64 `json:"login_success"`
	LoginFail           int64 `json:"login_fail"`
	TokenAttempts       int64 `json:"token_attempts,omitempty"`
	TokenSuccess        int64 `json:"token_success,omitempty"`
	TokenFail           int64 `json:"token_fail,omitempty"`
	MQTTDialAttempts    int64 `json:"mqtt_dial_attempts,omitempty"`
	MQTTDialSuccess     int64 `json:"mqtt_dial_success,omitempty"`
	MQTTDialFail        int64 `json:"mqtt_dial_fail,omitempty"`
	MQTTConnackAttempts int64 `json:"mqtt_connack_attempts,omitempty"`
	MQTTConnackSuccess  int64 `json:"mqtt_connack_success,omitempty"`
	MQTTConnackFail     int64 `json:"mqtt_connack_fail,omitempty"`
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
	thresholds := gateThresholdsFromConditions(plan.Conditions)
	correlation := correlateServerEvidenceWithThresholds(evidence, deviceTotals, appTotals, thresholds)
	runtimeLogCorrelation := correlateRuntimeLogsWithThresholds(evidence, stageResults, thresholds)
	videoEvidence := videoEvidenceWithServerEvidence(loadVideoEvidence(filepath.Join(outDir, "video")), evidence)
	outcome := evaluateRunOutcome(plan, evidence, stageResults, LoadGeneratorHealth{}, correlation, runtimeLogCorrelation, videoEvidence)

	result := RunResult{
		RunID:                 runID,
		Status:                outcome.Status,
		Result:                outcome.Result,
		Plan:                  plan,
		StageResults:          stageResults,
		DeviceMQTTTotals:      deviceTotals,
		AppUserTotals:         appTotals,
		ServerEvidence:        evidence,
		ServerCorrelation:     correlation,
		RuntimeLogCorrelation: runtimeLogCorrelation,
		VideoEvidence:         videoEvidence,
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
		VideoEvidence:         videoEvidence,
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
	thresholds := gateThresholdsFromConditions(plan.Conditions)
	correlation := correlateServerEvidenceWithThresholds(evidence, deviceTotals, appTotals, thresholds)
	runtimeLogCorrelation := correlateRuntimeLogsWithThresholds(evidence, stages, thresholds)
	videoEvidence := videoEvidenceWithServerEvidence(loadVideoEvidence(filepath.Join(outDir, "video")), evidence)
	outcome := evaluateRunOutcome(plan, evidence, stages, loadHealth, correlation, runtimeLogCorrelation, videoEvidence)
	result := RunResult{
		RunID:                 runID,
		Status:                outcome.Status,
		Result:                outcome.Result,
		Plan:                  plan,
		StageResults:          stages,
		DeviceMQTTTotals:      deviceTotals,
		AppUserTotals:         appTotals,
		ServerEvidence:        evidence,
		ServerCorrelation:     correlation,
		RuntimeLogCorrelation: runtimeLogCorrelation,
		VideoEvidence:         videoEvidence,
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
		VideoEvidence:         videoEvidence,
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

func loadVideoEvidence(dir string) VideoEvidence {
	for _, name := range []string{"results.json", "load-results.json", "loadtest-results.json"} {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		evidence, err := videoEvidenceFromLoadtestJSON(raw)
		if err != nil {
			return VideoEvidence{Notes: []string{fmt.Sprintf("video evidence decode failed: %s", err)}}
		}
		return evidence
	}
	return VideoEvidence{}
}

func videoEvidenceFromLoadtestJSON(raw []byte) (VideoEvidence, error) {
	var payload struct {
		WebRTC struct {
			Attempts          int64   `json:"attempts"`
			Successes         int64   `json:"successes"`
			Failures          int64   `json:"failures"`
			SuccessRate       float64 `json:"success_rate"`
			SetupLatencyP95MS int64   `json:"setup_latency_p95_ms"`
			SetupLatencyP99MS int64   `json:"setup_latency_p99_ms"`
			ICEServerCount    int     `json:"ice_server_count"`
			OpenSessions      int     `json:"open_sessions"`
			Create            struct {
				Operations int64 `json:"operations"`
				Successes  int64 `json:"successes"`
			} `json:"create"`
			Setup struct {
				Operations int64 `json:"operations"`
				Successes  int64 `json:"successes"`
			} `json:"setup"`
			Close struct {
				Operations int64 `json:"operations"`
				Successes  int64 `json:"successes"`
			} `json:"close"`
		} `json:"webrtc"`
		WebRTCMedia struct {
			Attempts            int64 `json:"attempts"`
			Successes           int64 `json:"successes"`
			Failures            int64 `json:"failures"`
			PacketsReceived     int64 `json:"packets_received"`
			BytesReceived       int64 `json:"bytes_received"`
			H264PacketsReceived int64 `json:"h264_packets_received"`
			H264BytesReceived   int64 `json:"h264_bytes_received"`
			OpusPacketsReceived int64 `json:"opus_packets_received"`
			OpusBytesReceived   int64 `json:"opus_bytes_received"`
			TimeToFirstRTPP95MS int64 `json:"time_to_first_rtp_p95_ms"`
			ICEConnectedP95MS   int64 `json:"ice_connected_p95_ms"`
		} `json:"webrtc_media"`
		TURN struct {
			RegistryAvailable bool  `json:"registry_available"`
			ActiveNodes       int64 `json:"active_nodes"`
			CoturnAvailable   bool  `json:"coturn_available"`
			Allocations       int64 `json:"allocations"`
			ActiveSessions    int64 `json:"active_sessions"`
		} `json:"turn_evidence"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return VideoEvidence{}, err
	}
	evidence := VideoEvidence{
		WebRTC: WebRTCTotals{
			CreateAttempts:     payload.WebRTC.Create.Operations,
			CreateSuccess:      payload.WebRTC.Create.Successes,
			SetupAttempts:      nonZeroInt64(payload.WebRTC.Setup.Operations, payload.WebRTC.Attempts),
			SetupSuccess:       nonZeroInt64(payload.WebRTC.Setup.Successes, payload.WebRTC.Successes),
			CloseAttempts:      payload.WebRTC.Close.Operations,
			CloseSuccess:       payload.WebRTC.Close.Successes,
			SuccessRatePercent: payload.WebRTC.SuccessRate * 100,
			SetupP95MS:         payload.WebRTC.SetupLatencyP95MS,
			SetupP99MS:         payload.WebRTC.SetupLatencyP99MS,
			ICEServerCount:     payload.WebRTC.ICEServerCount,
			OpenSessions:       payload.WebRTC.OpenSessions,
		},
		WebRTCMedia: WebRTCMediaTotals{
			Enabled:             payload.WebRTCMedia.Attempts > 0,
			Attempts:            payload.WebRTCMedia.Attempts,
			Successes:           payload.WebRTCMedia.Successes,
			Failures:            payload.WebRTCMedia.Failures,
			ICEConnectedP95MS:   payload.WebRTCMedia.ICEConnectedP95MS,
			TimeToFirstRTPP95MS: payload.WebRTCMedia.TimeToFirstRTPP95MS,
			PacketsReceived:     payload.WebRTCMedia.PacketsReceived,
			BytesReceived:       payload.WebRTCMedia.BytesReceived,
			H264PacketsReceived: payload.WebRTCMedia.H264PacketsReceived,
			H264BytesReceived:   payload.WebRTCMedia.H264BytesReceived,
			OpusPacketsReceived: payload.WebRTCMedia.OpusPacketsReceived,
			OpusBytesReceived:   payload.WebRTCMedia.OpusBytesReceived,
		},
		TURN: TURNEvidence{
			RegistryAvailable: payload.TURN.RegistryAvailable,
			ActiveNodes:       payload.TURN.ActiveNodes,
			CoturnAvailable:   payload.TURN.CoturnAvailable,
			Allocations:       payload.TURN.Allocations,
			ActiveSessions:    payload.TURN.ActiveSessions,
		},
	}
	if evidence.WebRTCMedia.Enabled && evidence.WebRTC.SetupAttempts == 0 && evidence.WebRTCMedia.Attempts > 0 {
		evidence.WebRTC.SetupAttempts = evidence.WebRTCMedia.Attempts
		evidence.WebRTC.SetupSuccess = evidence.WebRTCMedia.Successes
		evidence.WebRTC.SetupP95MS = evidence.WebRTCMedia.ICEConnectedP95MS
		evidence.WebRTC.SetupP99MS = evidence.WebRTCMedia.ICEConnectedP95MS
	}
	if evidence.WebRTC.SuccessRatePercent == 0 {
		attempts := evidence.WebRTC.CreateAttempts + evidence.WebRTC.SetupAttempts + evidence.WebRTC.CloseAttempts
		successes := evidence.WebRTC.CreateSuccess + evidence.WebRTC.SetupSuccess + evidence.WebRTC.CloseSuccess
		if attempts > 0 {
			evidence.WebRTC.SuccessRatePercent = float64(successes) * 100 / float64(attempts)
		}
	}
	evidence.Complete = evidence.WebRTC.CreateAttempts > 0 &&
		evidence.WebRTC.SetupAttempts > 0 &&
		evidence.WebRTC.CloseAttempts > 0 &&
		evidence.TURN.RegistryAvailable &&
		evidence.TURN.CoturnAvailable &&
		evidence.TURN.ActiveNodes > 0
	return evidence, nil
}

func videoEvidenceWithServerEvidence(video VideoEvidence, evidence ServerEvidence) VideoEvidence {
	if source, ok := evidence.Sources["turn_registry"]; ok {
		video.TURN.RegistryAvailable = video.TURN.RegistryAvailable || source.Available
		if value := source.Counters["turn_registry.active_nodes"]; value > video.TURN.ActiveNodes {
			video.TURN.ActiveNodes = value
		}
		if value := source.Counters["turn_registry.ready_pods"]; value > video.TURN.ActiveNodes {
			video.TURN.ActiveNodes = value
		}
	}
	if source, ok := evidence.Sources["coturn"]; ok {
		video.TURN.CoturnAvailable = video.TURN.CoturnAvailable || source.Available
		if value := source.Counters["coturn.allocations"]; value > video.TURN.Allocations {
			video.TURN.Allocations = value
		}
		if value := source.Counters["coturn.active_sessions"]; value > video.TURN.ActiveSessions {
			video.TURN.ActiveSessions = value
		}
		if value := source.Counters["coturn.ready_pods"]; value > video.TURN.ActiveNodes {
			video.TURN.ActiveNodes = value
		}
	}
	video.Complete = video.WebRTC.CreateAttempts > 0 &&
		video.WebRTC.SetupAttempts > 0 &&
		video.WebRTC.CloseAttempts > 0 &&
		video.TURN.RegistryAvailable &&
		video.TURN.CoturnAvailable &&
		video.TURN.ActiveNodes > 0
	return video
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
		"postgres":           {Available: available},
		"redis_valkey":       {Available: available},
		"ingress_nginx":      {Available: available},
		"host_pod_resources": {Available: available},
	}
}

func optionalEvidenceSources(available bool) map[string]EvidenceSource {
	return map[string]EvidenceSource{
		"central_logger": {Available: available, Optional: true},
		"edge_haproxy":   {Available: available, Optional: true},
		"turn_registry":  {Available: available, Optional: true},
		"coturn":         {Available: available, Optional: true},
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
	if len(stageFunctionalFailureReasons(plan.Conditions, stages)) > 0 {
		return "FAIL"
	}
	return "PASS"
}

func stageFunctionalFailureReasons(conditions TestConditions, stages []StageResult) []string {
	thresholds := gateThresholdsFromConditions(conditions)
	reasons := []string{}
	for _, stage := range stages {
		if stage.AuthorizationViolationCount > 0 {
			reasons = append(reasons, fmt.Sprintf("%s authorization violations %d > 0", stage.Name, stage.AuthorizationViolationCount))
		}
		if stage.MQTTConnectSuccessRatePercent < thresholds.FunctionalSuccessThresholdPercent {
			reasons = append(reasons, fmt.Sprintf("%s MQTT connect success %.2f%% < %.2f%%", stage.Name, stage.MQTTConnectSuccessRatePercent, thresholds.FunctionalSuccessThresholdPercent))
		}
		ackRate := appACKSuccessRate(stage.AppUserTotals)
		if ackRate < thresholds.FunctionalSuccessThresholdPercent {
			reasons = append(reasons, fmt.Sprintf("%s app ACK success %.2f%% < %.2f%%", stage.Name, ackRate, thresholds.FunctionalSuccessThresholdPercent))
		}
		if stage.DesiredReportedConvergenceRate < thresholds.FunctionalSuccessThresholdPercent {
			reasons = append(reasons, fmt.Sprintf("%s desired/reported convergence %.2f%% < %.2f%%", stage.Name, stage.DesiredReportedConvergenceRate, thresholds.FunctionalSuccessThresholdPercent))
		}
		if stage.OfflineDesiredConvergenceRate < thresholds.FunctionalSuccessThresholdPercent {
			reasons = append(reasons, fmt.Sprintf("%s offline desired convergence %.2f%% < %.2f%%", stage.Name, stage.OfflineDesiredConvergenceRate, thresholds.FunctionalSuccessThresholdPercent))
		}
		if stage.DeltaClearSuccessRatePercent < thresholds.FunctionalSuccessThresholdPercent {
			reasons = append(reasons, fmt.Sprintf("%s delta clear %.2f%% < %.2f%%", stage.Name, stage.DeltaClearSuccessRatePercent, thresholds.FunctionalSuccessThresholdPercent))
		}
	}
	return reasons
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

const runSuccessRateThresholdPercent = 99.5

func evaluateRunOutcome(plan Plan, evidence ServerEvidence, stages []StageResult, health LoadGeneratorHealth, correlation ServerCorrelation, runtimeLogCorrelation RuntimeLogCorrelation, videoEvidenceValues ...VideoEvidence) RunOutcome {
	outcome := RunOutcome{Status: "COMPLETE", Result: "SUCCESS"}
	reasons := []string{}
	incomplete := false
	fail := false
	videoEvidence := VideoEvidence{}
	if len(videoEvidenceValues) > 0 {
		videoEvidence = videoEvidenceValues[0]
	}

	if !evidence.Complete {
		incomplete = true
		reasons = append(reasons, "Missing server evidence")
	}
	if !shadowEvidenceComplete(stages) {
		incomplete = true
		reasons = append(reasons, "Missing IoT Device Shadow evidence")
	}
	videoIncomplete, videoFailReasons := videoGateFailures(plan, videoEvidence)
	if videoIncomplete {
		incomplete = true
	}
	if !videoIncomplete && len(videoFailReasons) > 0 {
		fail = true
	}
	reasons = append(reasons, videoFailReasons...)
	if health.Saturated {
		incomplete = true
		reasons = append(reasons, "Load-generator saturation invalidated server-capacity conclusion")
		for _, reason := range health.Reasons {
			reasons = append(reasons, reason)
		}
	}
	missingTypes := missingDeviceTypeEvidence(plan, stages)

	switch strings.ToLower(strings.TrimSpace(correlation.Status)) {
	case "pass":
	case "fail":
		fail = true
		reasons = append(reasons, "Server/client counter correlation mismatch")
	case "":
		incomplete = true
		reasons = append(reasons, "Server/client counter correlation is incomplete")
	default:
		incomplete = true
		if len(correlation.Reasons) == 0 {
			reasons = append(reasons, "Server/client counter correlation is incomplete")
		}
		for _, reason := range correlation.Reasons {
			reasons = append(reasons, "Server/client counter correlation incomplete: "+reason)
		}
	}

	switch strings.ToLower(strings.TrimSpace(runtimeLogCorrelation.Status)) {
	case "", "pass", "skipped":
	case "fail":
		fail = true
		reasons = append(reasons, "Runtime log stream correlation mismatch")
	default:
		incomplete = true
		reasons = append(reasons, "Runtime log stream correlation is incomplete")
	}

	if !incomplete {
		if len(missingTypes) > 0 {
			fail = true
			reasons = append(reasons, "Missing per-device-type MQTT evidence: "+strings.Join(missingTypes, ", "))
		}
		for _, stage := range stages {
			if stage.AuthorizationViolationCount > 0 {
				fail = true
				reasons = append(reasons, fmt.Sprintf("stage %s authorization violations: %d", firstNonEmpty(stage.Name, "unknown"), stage.AuthorizationViolationCount))
			}
			if stage.DesiredReportedConvergenceRate < 95 {
				fail = true
				reasons = append(reasons, fmt.Sprintf("stage %s desired/reported convergence %.2f%% below 95.00%% threshold", firstNonEmpty(stage.Name, "unknown"), stage.DesiredReportedConvergenceRate))
			}
			if stage.OfflineDesiredConvergenceRate < 95 {
				fail = true
				reasons = append(reasons, fmt.Sprintf("stage %s offline desired convergence %.2f%% below 95.00%% threshold", firstNonEmpty(stage.Name, "unknown"), stage.OfflineDesiredConvergenceRate))
			}
			if stage.DeltaClearSuccessRatePercent < 95 {
				fail = true
				reasons = append(reasons, fmt.Sprintf("stage %s delta clear success %.2f%% below 95.00%% threshold", firstNonEmpty(stage.Name, "unknown"), stage.DeltaClearSuccessRatePercent))
			}
		}
		for _, reason := range successRateFailureReasons(plan.Conditions, stages, runSuccessRateThresholdPercent) {
			fail = true
			reasons = append(reasons, reason)
		}
	}

	if incomplete {
		outcome.Status = "INCOMPLETE"
		outcome.Result = "INCOMPLETE"
	} else if fail {
		outcome.Result = "FAIL"
	}
	outcome.Reasons = reasons
	return outcome
}

func videoGateFailures(plan Plan, evidence VideoEvidence) (bool, []string) {
	if !plan.VideoEnabled() {
		return false, nil
	}
	reasons := []string{}
	incomplete := false
	if !evidence.Complete {
		incomplete = true
		reasons = append(reasons, "Missing WebRTC create/setup/close evidence")
	}
	if evidence.WebRTC.CreateAttempts == 0 || evidence.WebRTC.SetupAttempts == 0 || evidence.WebRTC.CloseAttempts == 0 {
		incomplete = true
		if len(reasons) == 0 {
			reasons = append(reasons, "Missing WebRTC create/setup/close evidence")
		}
	}
	if !evidence.TURN.RegistryAvailable || !evidence.TURN.CoturnAvailable || evidence.TURN.ActiveNodes == 0 {
		incomplete = true
		reasons = append(reasons, "Missing external TURN/coturn evidence")
	}
	if incomplete {
		return true, reasons
	}
	threshold := DefaultFunctionalSuccessThresholdPercent
	if plan.Conditions.FunctionalSuccessThresholdPercent > 0 {
		threshold = plan.Conditions.FunctionalSuccessThresholdPercent
	}
	if evidence.WebRTC.SuccessRatePercent < threshold {
		reasons = append(reasons, fmt.Sprintf("WebRTC signaling success rate %.2f%% below %.2f%% threshold", evidence.WebRTC.SuccessRatePercent, threshold))
	}
	if strings.TrimSpace(plan.VideoProfile.WebRTCMediaSet) != "" && plan.VideoProfile.WebRTCMediaSet != "off" {
		if evidence.WebRTCMedia.Attempts == 0 || evidence.WebRTCMedia.Successes == 0 || evidence.WebRTCMedia.ICEConnectedP95MS == 0 || evidence.WebRTCMedia.TimeToFirstRTPP95MS == 0 {
			reasons = append(reasons, "WebRTC media evidence missing ICE connected or first RTP")
		}
	}
	return false, reasons
}

func successRateFailureReasons(conditions TestConditions, stages []StageResult, threshold float64) []string {
	reasons := []string{}
	for _, stage := range stages {
		stageName := firstNonEmpty(stage.Name, "unknown")
		targetDevices := int64(stage.ConnectedDevices)
		if targetDevices > 0 {
			activeConnections := nonZeroInt64(stage.DeviceMQTTTotals.ActiveConnections, stage.DeviceMQTTTotals.ConnectSuccess)
			activeSubscriptions := nonZeroInt64(stage.DeviceMQTTTotals.ActiveSubscriptions, stage.DeviceMQTTTotals.Subscribes)
			if rate := percentInt64(activeConnections, targetDevices); rate < threshold {
				reasons = append(reasons, fmt.Sprintf("stage %s connection success rate %.2f%% below %.2f%% threshold (%d/%d)", stageName, rate, threshold, activeConnections, targetDevices))
			}
			if rate := percentInt64(activeSubscriptions, targetDevices); rate < threshold {
				reasons = append(reasons, fmt.Sprintf("stage %s subscription success rate %.2f%% below %.2f%% threshold (%d/%d)", stageName, rate, threshold, activeSubscriptions, targetDevices))
			}
		}
		expectedUsers := int64(expectedStageUsers(conditions, stage.ConnectedDevices))
		if expectedUsers > 0 {
			if rate := percentInt64(stage.AppUserTotals.DesiredWrites, expectedUsers); rate < threshold {
				reasons = append(reasons, fmt.Sprintf("stage %s app desired write success rate %.2f%% below %.2f%% threshold (%d/%d)", stageName, rate, threshold, stage.AppUserTotals.DesiredWrites, expectedUsers))
			}
			if rate := percentInt64(stage.AppUserTotals.ReceivedAcks, expectedUsers); rate < threshold {
				reasons = append(reasons, fmt.Sprintf("stage %s app ACK success rate %.2f%% below %.2f%% threshold (%d/%d)", stageName, rate, threshold, stage.AppUserTotals.ReceivedAcks, expectedUsers))
			}
		}
	}
	return reasons
}

func percentInt64(value, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value) * 100 / float64(total)
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
	return correlateServerEvidenceWithThresholds(evidence, device, app, GateThresholds{
		FunctionalSuccessThresholdPercent:    DefaultFunctionalSuccessThresholdPercent,
		ClientTargetCompletenessPercent:      DefaultClientTargetCompletenessPercent,
		ExactEventCorrelationPercent:         DefaultExactEventCorrelationPercent,
		AggregateCorrelationTolerancePercent: DefaultAggregateCorrelationTolerancePercent,
		AggregateCorrelationMinTolerance:     DefaultAggregateCorrelationMinTolerance,
	})
}

func correlateServerEvidenceWithThresholds(evidence ServerEvidence, device DeviceMQTTTotals, app AppUserTotals, thresholds GateThresholds) ServerCorrelation {
	thresholds = normalizeGateThresholdsForRuntime(thresholds)
	if reasons := missingClientCounterReasons(device, app); len(reasons) > 0 {
		return ServerCorrelation{Status: "incomplete", Reasons: reasons}
	}
	reasons := []string{}
	if evidenceCounterAny(evidence, "emqx.broker.identity", "emqx", "emqx_listener_stats") == 0 {
		reasons = append(reasons, "EMQX broker identity missing from server evidence")
	}
	totalMQTTConnectSuccess := device.ConnectSuccess + app.MQTTConnackSuccess
	serverTotalMQTTConnectSuccess := evidenceCounter(evidence, "emqx", "mqtt.total_connect_success")
	counterName := "mqtt.total_connect_success"
	if serverTotalMQTTConnectSuccess == 0 {
		serverTotalMQTTConnectSuccess = evidenceCounter(evidence, "emqx", "device_mqtt.connect_success")
		counterName = "device_mqtt.connect_success"
	}
	if serverTotalMQTTConnectSuccess == 0 {
		serverTotalMQTTConnectSuccess = evidenceCounter(evidence, "emqx", "emqx.metric.client.connack")
		counterName = "emqx.metric.client.connack"
	}
	if serverTotalMQTTConnectSuccess == 0 {
		serverTotalMQTTConnectSuccess = evidenceCounter(evidence, "emqx", "emqx.metric.client.connected")
		counterName = "emqx.metric.client.connected"
	}
	checks := []CorrelationCheck{
		newCorrelationCheck("emqx", counterName, totalMQTTConnectSuccess, serverTotalMQTTConnectSuccess, aggregateCorrelationTolerance(totalMQTTConnectSuccess, serverTotalMQTTConnectSuccess, thresholds)),
		newCorrelationCheck("iot_device_shadow", "app_user.desired_writes", app.DesiredWrites, evidenceCounter(evidence, "iot_device_shadow", "app_user.desired_writes"), aggregateCorrelationTolerance(app.DesiredWrites, evidenceCounter(evidence, "iot_device_shadow", "app_user.desired_writes"), thresholds)),
		newCorrelationCheck("iot_device_shadow", "device_mqtt.delta_received", device.DeltaReceived, evidenceCounter(evidence, "iot_device_shadow", "device_mqtt.delta_received"), aggregateCorrelationTolerance(device.DeltaReceived, evidenceCounter(evidence, "iot_device_shadow", "device_mqtt.delta_received"), thresholds)),
		newCorrelationCheck("iot_device_shadow", "device_mqtt.reported_publishes", device.ReportedPublishes, evidenceCounter(evidence, "iot_device_shadow", "device_mqtt.reported_publishes"), aggregateCorrelationTolerance(device.ReportedPublishes, evidenceCounter(evidence, "iot_device_shadow", "device_mqtt.reported_publishes"), thresholds)),
		newCorrelationCheck("iot_device_shadow", "app_user.received_acks", app.ReceivedAcks, evidenceCounter(evidence, "iot_device_shadow", "app_user.received_acks"), aggregateCorrelationTolerance(app.ReceivedAcks, evidenceCounter(evidence, "iot_device_shadow", "app_user.received_acks"), thresholds)),
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
	return correlateRuntimeLogsWithThresholds(evidence, stages, GateThresholds{
		FunctionalSuccessThresholdPercent:    DefaultFunctionalSuccessThresholdPercent,
		ClientTargetCompletenessPercent:      DefaultClientTargetCompletenessPercent,
		ExactEventCorrelationPercent:         DefaultExactEventCorrelationPercent,
		AggregateCorrelationTolerancePercent: DefaultAggregateCorrelationTolerancePercent,
		AggregateCorrelationMinTolerance:     DefaultAggregateCorrelationMinTolerance,
	})
}

func correlateRuntimeLogsWithThresholds(evidence ServerEvidence, stages []StageResult, thresholds GateThresholds) RuntimeLogCorrelation {
	thresholds = normalizeGateThresholdsForRuntime(thresholds)
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
		result.Status = "incomplete"
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
	allowedMissing := allowedMissingExactEvents(len(events), thresholds)
	if result.MissingStreamCount+result.MissingSequenceCount > allowedMissing {
		result.Status = "fail"
	}
	return result
}

func newCorrelationCheck(source string, counter string, clientTotal int64, serverTotal int64, tolerance int64) CorrelationCheck {
	check := CorrelationCheck{
		Source:      source,
		Counter:     counter,
		ClientTotal: clientTotal,
		ServerTotal: serverTotal,
		Delta:       serverTotal - clientTotal,
		Tolerance:   tolerance,
		Status:      "pass",
	}
	if serverTotal == 0 {
		check.Status = "incomplete"
	} else if absInt64(check.Delta) > check.Tolerance {
		check.Status = "fail"
	} else if check.Delta != 0 {
		check.Status = "warning"
	}
	return check
}

func aggregateCorrelationTolerance(clientTotal int64, serverTotal int64, thresholds GateThresholds) int64 {
	thresholds = normalizeGateThresholdsForRuntime(thresholds)
	basis := math.Max(math.Abs(float64(clientTotal)), math.Abs(float64(serverTotal)))
	percentTolerance := int64(math.Ceil(basis * thresholds.AggregateCorrelationTolerancePercent / 100))
	if percentTolerance < thresholds.AggregateCorrelationMinTolerance {
		return thresholds.AggregateCorrelationMinTolerance
	}
	return percentTolerance
}

func allowedMissingExactEvents(totalEvents int, thresholds GateThresholds) int {
	thresholds = normalizeGateThresholdsForRuntime(thresholds)
	if totalEvents <= 0 || thresholds.ExactEventCorrelationPercent >= 100 {
		return 0
	}
	allowedRate := (100 - thresholds.ExactEventCorrelationPercent) / 100
	return int(math.Floor(float64(totalEvents) * allowedRate))
}

func appACKSuccessRate(totals AppUserTotals) float64 {
	if totals.DesiredWrites <= 0 {
		return 0
	}
	return float64(totals.ReceivedAcks) * 100 / float64(totals.DesiredWrites)
}

func normalizeGateThresholdsForRuntime(thresholds GateThresholds) GateThresholds {
	normalized, err := normalizeGateThresholds(PlanOptions{
		FunctionalSuccessThresholdPercent:    thresholds.FunctionalSuccessThresholdPercent,
		ClientTargetCompletenessPercent:      thresholds.ClientTargetCompletenessPercent,
		ExactEventCorrelationPercent:         thresholds.ExactEventCorrelationPercent,
		AggregateCorrelationTolerancePercent: thresholds.AggregateCorrelationTolerancePercent,
		AggregateCorrelationMinTolerance:     thresholds.AggregateCorrelationMinTolerance,
	})
	if err != nil {
		return GateThresholds{
			FunctionalSuccessThresholdPercent:    DefaultFunctionalSuccessThresholdPercent,
			ClientTargetCompletenessPercent:      DefaultClientTargetCompletenessPercent,
			ExactEventCorrelationPercent:         DefaultExactEventCorrelationPercent,
			AggregateCorrelationTolerancePercent: DefaultAggregateCorrelationTolerancePercent,
			AggregateCorrelationMinTolerance:     DefaultAggregateCorrelationMinTolerance,
		}
	}
	return normalized
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
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
	if app.LoginAttempts == 0 && app.TokenAttempts == 0 {
		reasons = append(reasons, "client app login/token attempts are zero")
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
	thresholds := gateThresholdsFromConditions(conditions)
	for _, stage := range stages {
		if stage.ConnectedDevices <= 0 {
			return false
		}
		expectedUsers := expectedStageUsers(conditions, stage.ConnectedDevices)
		activeConnections := nonZeroInt64(stage.DeviceMQTTTotals.ActiveConnections, stage.DeviceMQTTTotals.ConnectSuccess)
		activeSubscriptions := nonZeroInt64(stage.DeviceMQTTTotals.ActiveSubscriptions, stage.DeviceMQTTTotals.Subscribes)
		requiredDevices := thresholdCount(int64(stage.ConnectedDevices), thresholds.ClientTargetCompletenessPercent)
		requiredUsers := thresholdCount(int64(expectedUsers), thresholds.ClientTargetCompletenessPercent)
		if activeConnections < requiredDevices ||
			activeSubscriptions < requiredDevices ||
			stage.AppUserTotals.DesiredWrites < requiredUsers {
			return false
		}
	}
	return true
}

func thresholdCount(total int64, percent float64) int64 {
	if total <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(total) * percent / 100))
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
