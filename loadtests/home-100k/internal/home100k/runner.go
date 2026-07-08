package home100k

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	Complete                 bool                `json:"complete"`
	WebRTC                   WebRTCTotals        `json:"webrtc_totals"`
	WebRTCMedia              WebRTCMediaTotals   `json:"webrtc_media_totals,omitempty"`
	TURN                     TURNEvidence        `json:"turn_evidence,omitempty"`
	Thresholds               VideoThresholds     `json:"thresholds,omitempty"`
	Steps                    []VideoStepEvidence `json:"steps,omitempty"`
	RelayCandidateSamples    int64               `json:"relay_candidate_samples,omitempty"`
	NonRelayCandidateSamples int64               `json:"non_relay_candidate_samples,omitempty"`
	Notes                    []string            `json:"notes,omitempty"`
}

type VideoStepEvidence struct {
	Name                     string            `json:"name,omitempty"`
	ArtifactDir              string            `json:"artifact_dir,omitempty"`
	Viewers                  int               `json:"viewers,omitempty"`
	DurationMS               int64             `json:"duration_ms,omitempty"`
	ICEPolicy                string            `json:"ice_policy,omitempty"`
	WebRTC                   WebRTCTotals      `json:"webrtc_totals"`
	WebRTCMedia              WebRTCMediaTotals `json:"webrtc_media_totals,omitempty"`
	TURN                     TURNEvidence      `json:"turn_evidence,omitempty"`
	Thresholds               VideoThresholds   `json:"thresholds,omitempty"`
	Complete                 bool              `json:"complete"`
	RelayCandidateSamples    int64             `json:"relay_candidate_samples,omitempty"`
	NonRelayCandidateSamples int64             `json:"non_relay_candidate_samples,omitempty"`
	Notes                    []string          `json:"notes,omitempty"`
}

type VideoThresholds struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
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
	Enabled             bool               `json:"enabled,omitempty"`
	Attempts            int64              `json:"attempts,omitempty"`
	Successes           int64              `json:"successes,omitempty"`
	Failures            int64              `json:"failures,omitempty"`
	ICEConnectedP95MS   int64              `json:"ice_connected_p95_ms,omitempty"`
	TimeToFirstRTPP95MS int64              `json:"time_to_first_rtp_p95_ms,omitempty"`
	PacketsReceived     int64              `json:"packets_received,omitempty"`
	BytesReceived       int64              `json:"bytes_received,omitempty"`
	H264PacketsReceived int64              `json:"h264_packets_received,omitempty"`
	H264BytesReceived   int64              `json:"h264_bytes_received,omitempty"`
	OpusPacketsReceived int64              `json:"opus_packets_received,omitempty"`
	OpusBytesReceived   int64              `json:"opus_bytes_received,omitempty"`
	Startup             VideoStartupTotals `json:"video_startup_latency,omitempty"`
}

type VideoStartupTotals struct {
	Samples                              int                   `json:"samples,omitempty"`
	H264AccessUnitSamples                int                   `json:"h264_access_unit_samples,omitempty"`
	AppRequestToFirstRTPP50MS            int64                 `json:"app_request_to_first_rtp_p50_ms,omitempty"`
	AppRequestToFirstRTPP95MS            int64                 `json:"app_request_to_first_rtp_p95_ms,omitempty"`
	AppRequestToFirstRTPP99MS            int64                 `json:"app_request_to_first_rtp_p99_ms,omitempty"`
	AppRequestToFirstH264AccessUnitP50MS int64                 `json:"app_request_to_first_h264_access_unit_p50_ms,omitempty"`
	AppRequestToFirstH264AccessUnitP95MS int64                 `json:"app_request_to_first_h264_access_unit_p95_ms,omitempty"`
	AppRequestToFirstH264AccessUnitP99MS int64                 `json:"app_request_to_first_h264_access_unit_p99_ms,omitempty"`
	BreakdownP95                         VideoStartupBreakdown `json:"breakdown_p95,omitempty"`
}

type VideoStartupBreakdown struct {
	APICreateMS                     int64 `json:"api_create_ms,omitempty"`
	OfferDeliveryMS                 int64 `json:"offer_delivery_ms,omitempty"`
	DeviceAnswerMS                  int64 `json:"device_answer_ms,omitempty"`
	PionCreatePeerMS                int64 `json:"pion_create_peer_ms,omitempty"`
	PionCreateOfferMS               int64 `json:"pion_create_offer_ms,omitempty"`
	PionCreateAnswerMS              int64 `json:"pion_create_answer_ms,omitempty"`
	PionSetLocalDescriptionMS       int64 `json:"pion_set_local_description_ms,omitempty"`
	PionICEGatheringWaitMS          int64 `json:"pion_ice_gathering_wait_ms,omitempty"`
	PionSetRemoteDescriptionMS      int64 `json:"pion_set_remote_description_ms,omitempty"`
	RemoteAnswerSetMS               int64 `json:"remote_answer_set_ms,omitempty"`
	ICEConnectMS                    int64 `json:"ice_connect_ms,omitempty"`
	ICECheckMS                      int64 `json:"ice_check_ms,omitempty"`
	ICEConnectedSinceSessionStartMS int64 `json:"ice_connected_since_session_start_ms,omitempty"`
	FirstRTPAfterICEMS              int64 `json:"first_rtp_after_ice_ms,omitempty"`
	FirstH264AccessUnitAfterRTPMS   int64 `json:"first_h264_access_unit_after_rtp_ms,omitempty"`
	SenderFirstWriteSinceSessionMS  int64 `json:"sender_first_write_since_session_ms,omitempty"`
}

type TURNEvidence struct {
	RegistryAvailable              bool   `json:"registry_available"`
	ActiveNodes                    int64  `json:"active_nodes,omitempty"`
	CoturnAvailable                bool   `json:"coturn_available"`
	Allocations                    int64  `json:"allocations,omitempty"`
	ActiveSessions                 int64  `json:"active_sessions,omitempty"`
	UDPSockets                     int64  `json:"udp_sockets,omitempty"`
	TCPEstablished                 int64  `json:"tcp_established,omitempty"`
	RelayUDPFlows                  int64  `json:"relay_udp_flows,omitempty"`
	RelayTCPFlows                  int64  `json:"relay_tcp_flows,omitempty"`
	JournalEvents                  int64  `json:"journal_events,omitempty"`
	CoturnCPUPercent               int64  `json:"coturn_cpu_pct,omitempty"`
	CoturnRSSKB                    int64  `json:"coturn_rss_kb,omitempty"`
	RXBytes                        int64  `json:"rx_bytes,omitempty"`
	TXBytes                        int64  `json:"tx_bytes,omitempty"`
	APITURNRegistryLookupSucceeded int64  `json:"api_turn_registry_lookup_succeeded,omitempty"`
	APITURNRegistryLookupEmpty     int64  `json:"api_turn_registry_lookup_empty,omitempty"`
	APITURNRegistryLookupFailed    int64  `json:"api_turn_registry_lookup_failed,omitempty"`
	APIStaticTURNCount             int64  `json:"api_static_turn_count,omitempty"`
	APIDynamicTURNCount            int64  `json:"api_dynamic_turn_count,omitempty"`
	APITURNRegistryNodeCount       int64  `json:"api_turn_registry_node_count,omitempty"`
	EvidenceStatus                 string `json:"evidence_status,omitempty"`
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
	videoDir := filepath.Join(outDir, "video")
	preloadedVideoEvidence := loadVideoEvidence(videoDir)
	collected, err := loadCollectedShardResults(filepath.Join(outDir, "shards"), plan.Stages)
	if err != nil {
		if !isNoShardResultsError(err) || !hasVideoEvidence(preloadedVideoEvidence) {
			return RunResult{}, err
		}
		collected = collectedShardResults{}
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
	videoEvidence := videoEvidenceWithServerEvidence(preloadedVideoEvidence, evidence)
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
	stepMatches, err := filepath.Glob(filepath.Join(dir, "step-*", "load-results.json"))
	stepDirs, stepDirErr := filepath.Glob(filepath.Join(dir, "step-*"))
	if (err == nil && len(stepMatches) > 0) || (stepDirErr == nil && len(stepDirs) > 0) {
		sort.Strings(stepMatches)
		sort.Strings(stepDirs)
		steps := make([]VideoStepEvidence, 0, maxInt(len(stepMatches), len(stepDirs)))
		notes := []string{}
		seenSteps := map[string]bool{}
		for _, path := range stepMatches {
			raw, err := os.ReadFile(path)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s read failed: %s", filepath.Base(filepath.Dir(path)), err))
				continue
			}
			evidence, err := videoEvidenceFromLoadtestJSON(raw)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s decode failed: %s", filepath.Base(filepath.Dir(path)), err))
				continue
			}
			evidence = videoEvidenceWithActiveTURNSamples(evidence, filepath.Dir(path))
			step := videoStepEvidenceFromEvidence(filepath.Base(filepath.Dir(path)), filepath.Dir(path), evidence)
			steps = append(steps, step)
			seenSteps[filepath.Dir(path)] = true
		}
		for _, stepDir := range stepDirs {
			if seenSteps[stepDir] {
				continue
			}
			info, err := os.Stat(stepDir)
			if err != nil || !info.IsDir() {
				continue
			}
			evidence, ok, stepNotes := loadTwoHostVideoStepEvidence(stepDir)
			notes = append(notes, stepNotes...)
			if !ok {
				continue
			}
			evidence = videoEvidenceWithActiveTURNSamples(evidence, stepDir)
			step := videoStepEvidenceFromEvidence(filepath.Base(stepDir), stepDir, evidence)
			steps = append(steps, step)
		}
		merged := mergeVideoStepEvidence(steps)
		merged.Notes = append(merged.Notes, notes...)
		return merged
	}
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
		evidence = videoEvidenceWithActiveTURNSamples(evidence, dir)
		return evidence
	}
	shardMatches, err := filepath.Glob(filepath.Join(dir, "shard-*", "load-results.json"))
	if err == nil && len(shardMatches) > 0 {
		sort.Strings(shardMatches)
		steps := make([]VideoStepEvidence, 0, len(shardMatches))
		notes := []string{}
		for _, path := range shardMatches {
			raw, err := os.ReadFile(path)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s read failed: %s", filepath.Base(filepath.Dir(path)), err))
				continue
			}
			evidence, err := videoEvidenceFromLoadtestJSON(raw)
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s decode failed: %s", filepath.Base(filepath.Dir(path)), err))
				continue
			}
			steps = append(steps, videoStepEvidenceFromEvidence(filepath.Base(filepath.Dir(path)), filepath.Dir(path), evidence))
		}
		merged := mergeVideoStepEvidence(steps)
		merged = videoEvidenceWithActiveTURNSamples(merged, dir)
		merged.Notes = append(merged.Notes, notes...)
		return merged
	}
	return VideoEvidence{}
}

func loadTwoHostVideoStepEvidence(stepDir string) (VideoEvidence, bool, []string) {
	matches, err := filepath.Glob(filepath.Join(stepDir, "*", "load-results.json"))
	if err != nil || len(matches) == 0 {
		return VideoEvidence{}, false, nil
	}
	sort.Strings(matches)
	parts := make([]VideoStepEvidence, 0, len(matches))
	notes := []string{}
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		role := filepath.Base(filepath.Dir(path))
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s/%s read failed: %s", filepath.Base(stepDir), role, err))
			continue
		}
		evidence, err := videoEvidenceFromLoadtestJSON(raw)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s/%s decode failed: %s", filepath.Base(stepDir), role, err))
			continue
		}
		part := videoStepEvidenceFromEvidence(role, filepath.Dir(path), evidence)
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return VideoEvidence{}, false, notes
	}
	merged := mergeVideoStepEvidence(parts)
	merged.Notes = append(merged.Notes, notes...)
	merged.Complete = merged.WebRTC.CreateAttempts > 0 &&
		merged.WebRTC.SetupAttempts > 0 &&
		merged.WebRTC.CloseAttempts > 0 &&
		merged.TURN.RegistryAvailable &&
		merged.TURN.CoturnAvailable &&
		merged.TURN.ActiveNodes > 0
	return merged, true, notes
}

func videoEvidenceFromLoadtestJSON(raw []byte) (VideoEvidence, error) {
	var payload struct {
		Config struct {
			WebRTCICEPolicy string `json:"webrtc_ice_policy"`
			VirtualViewers  int    `json:"virtual_viewers"`
			DurationMS      int64  `json:"duration_ms"`
		} `json:"config"`
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
			Attempts            int64              `json:"attempts"`
			Successes           int64              `json:"successes"`
			Failures            int64              `json:"failures"`
			PacketsReceived     int64              `json:"packets_received"`
			BytesReceived       int64              `json:"bytes_received"`
			H264PacketsReceived int64              `json:"h264_packets_received"`
			H264BytesReceived   int64              `json:"h264_bytes_received"`
			OpusPacketsReceived int64              `json:"opus_packets_received"`
			OpusBytesReceived   int64              `json:"opus_bytes_received"`
			TimeToFirstRTPP95MS int64              `json:"time_to_first_rtp_p95_ms"`
			ICEConnectedP95MS   int64              `json:"ice_connected_p95_ms"`
			Startup             VideoStartupTotals `json:"video_startup_latency"`
		} `json:"webrtc_media"`
		TURN struct {
			RegistryAvailable              bool   `json:"registry_available"`
			ActiveNodes                    int64  `json:"active_nodes"`
			CoturnAvailable                bool   `json:"coturn_available"`
			Allocations                    int64  `json:"allocations"`
			ActiveSessions                 int64  `json:"active_sessions"`
			UDPSockets                     int64  `json:"udp_sockets"`
			TCPEstablished                 int64  `json:"tcp_established"`
			RelayUDPFlows                  int64  `json:"relay_udp_flows"`
			RelayTCPFlows                  int64  `json:"relay_tcp_flows"`
			JournalEvents                  int64  `json:"journal_events"`
			CoturnCPUPercent               int64  `json:"coturn_cpu_pct"`
			CoturnRSSKB                    int64  `json:"coturn_rss_kb"`
			RXBytes                        int64  `json:"rx_bytes"`
			TXBytes                        int64  `json:"tx_bytes"`
			APITURNRegistryLookupSucceeded int64  `json:"api_turn_registry_lookup_succeeded"`
			APITURNRegistryLookupEmpty     int64  `json:"api_turn_registry_lookup_empty"`
			APITURNRegistryLookupFailed    int64  `json:"api_turn_registry_lookup_failed"`
			APIStaticTURNCount             int64  `json:"api_static_turn_count"`
			APIDynamicTURNCount            int64  `json:"api_dynamic_turn_count"`
			APITURNRegistryNodeCount       int64  `json:"api_turn_registry_node_count"`
			EvidenceStatus                 string `json:"evidence_status"`
		} `json:"turn_evidence"`
		Thresholds          VideoThresholds `json:"thresholds"`
		VideoStartupLatency []struct {
			ICEPolicy                   string `json:"ice_policy"`
			SelectedLocalCandidateType  string `json:"selected_local_candidate_type"`
			SelectedRemoteCandidateType string `json:"selected_remote_candidate_type"`
		} `json:"video_startup_latency"`
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
			Startup:             payload.WebRTCMedia.Startup,
		},
		TURN: TURNEvidence{
			RegistryAvailable:              payload.TURN.RegistryAvailable,
			ActiveNodes:                    payload.TURN.ActiveNodes,
			CoturnAvailable:                payload.TURN.CoturnAvailable,
			Allocations:                    payload.TURN.Allocations,
			ActiveSessions:                 payload.TURN.ActiveSessions,
			UDPSockets:                     payload.TURN.UDPSockets,
			TCPEstablished:                 payload.TURN.TCPEstablished,
			RelayUDPFlows:                  payload.TURN.RelayUDPFlows,
			RelayTCPFlows:                  payload.TURN.RelayTCPFlows,
			JournalEvents:                  payload.TURN.JournalEvents,
			CoturnCPUPercent:               payload.TURN.CoturnCPUPercent,
			CoturnRSSKB:                    payload.TURN.CoturnRSSKB,
			RXBytes:                        payload.TURN.RXBytes,
			TXBytes:                        payload.TURN.TXBytes,
			APITURNRegistryLookupSucceeded: payload.TURN.APITURNRegistryLookupSucceeded,
			APITURNRegistryLookupEmpty:     payload.TURN.APITURNRegistryLookupEmpty,
			APITURNRegistryLookupFailed:    payload.TURN.APITURNRegistryLookupFailed,
			APIStaticTURNCount:             payload.TURN.APIStaticTURNCount,
			APIDynamicTURNCount:            payload.TURN.APIDynamicTURNCount,
			APITURNRegistryNodeCount:       payload.TURN.APITURNRegistryNodeCount,
			EvidenceStatus:                 payload.TURN.EvidenceStatus,
		},
		Thresholds: payload.Thresholds,
	}
	relaySamples, nonRelaySamples := relayCandidateSampleCounts(payload.Config.WebRTCICEPolicy, payload.VideoStartupLatency)
	evidence.RelayCandidateSamples = relaySamples
	evidence.NonRelayCandidateSamples = nonRelaySamples
	evidence.Steps = []VideoStepEvidence{{
		Viewers:                  payload.Config.VirtualViewers,
		DurationMS:               payload.Config.DurationMS,
		ICEPolicy:                payload.Config.WebRTCICEPolicy,
		WebRTC:                   evidence.WebRTC,
		WebRTCMedia:              evidence.WebRTCMedia,
		TURN:                     evidence.TURN,
		Thresholds:               evidence.Thresholds,
		RelayCandidateSamples:    relaySamples,
		NonRelayCandidateSamples: nonRelaySamples,
	}}
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
	evidence.Steps[0].Complete = evidence.Complete
	return evidence, nil
}

func relayCandidateSampleCounts(icePolicy string, samples []struct {
	ICEPolicy                   string `json:"ice_policy"`
	SelectedLocalCandidateType  string `json:"selected_local_candidate_type"`
	SelectedRemoteCandidateType string `json:"selected_remote_candidate_type"`
}) (int64, int64) {
	var relaySamples int64
	var nonRelaySamples int64
	for _, sample := range samples {
		policy := firstNonEmpty(strings.TrimSpace(sample.ICEPolicy), strings.TrimSpace(icePolicy))
		if policy != "relay" {
			continue
		}
		local := strings.TrimSpace(sample.SelectedLocalCandidateType)
		remote := strings.TrimSpace(sample.SelectedRemoteCandidateType)
		if local == "" && remote == "" {
			continue
		}
		if isRelayCandidateType(local) && isRelayCandidateType(remote) {
			relaySamples++
		} else {
			nonRelaySamples++
		}
	}
	return relaySamples, nonRelaySamples
}

func isRelayCandidateType(value string) bool {
	switch strings.TrimSpace(value) {
	case "relay", "relay_inferred":
		return true
	default:
		return false
	}
}

func videoStepEvidenceFromEvidence(name, artifactDir string, evidence VideoEvidence) VideoStepEvidence {
	step := VideoStepEvidence{
		Name:                     name,
		ArtifactDir:              artifactDir,
		WebRTC:                   evidence.WebRTC,
		WebRTCMedia:              evidence.WebRTCMedia,
		TURN:                     evidence.TURN,
		Thresholds:               evidence.Thresholds,
		Complete:                 evidence.Complete,
		RelayCandidateSamples:    evidence.RelayCandidateSamples,
		NonRelayCandidateSamples: evidence.NonRelayCandidateSamples,
		Notes:                    evidence.Notes,
	}
	if len(evidence.Steps) > 0 {
		for _, child := range evidence.Steps {
			step.Viewers += child.Viewers
		}
		step.DurationMS = evidence.Steps[0].DurationMS
		step.ICEPolicy = evidence.Steps[0].ICEPolicy
	}
	if step.Viewers == 0 {
		step.Viewers = int(evidence.WebRTCMedia.Attempts)
	}
	return step
}

func mergeVideoStepEvidence(steps []VideoStepEvidence) VideoEvidence {
	if len(steps) == 0 {
		return VideoEvidence{}
	}
	merged := VideoEvidence{Complete: true, Steps: steps}
	for _, step := range steps {
		if !step.Complete {
			merged.Complete = false
		}
		merged.WebRTC.CreateAttempts += step.WebRTC.CreateAttempts
		merged.WebRTC.CreateSuccess += step.WebRTC.CreateSuccess
		merged.WebRTC.SetupAttempts += step.WebRTC.SetupAttempts
		merged.WebRTC.SetupSuccess += step.WebRTC.SetupSuccess
		merged.WebRTC.CloseAttempts += step.WebRTC.CloseAttempts
		merged.WebRTC.CloseSuccess += step.WebRTC.CloseSuccess
		merged.WebRTC.SetupP95MS = maxInt64(merged.WebRTC.SetupP95MS, step.WebRTC.SetupP95MS)
		merged.WebRTC.SetupP99MS = maxInt64(merged.WebRTC.SetupP99MS, step.WebRTC.SetupP99MS)
		merged.WebRTC.ICEServerCount = maxInt(merged.WebRTC.ICEServerCount, step.WebRTC.ICEServerCount)
		merged.WebRTC.OpenSessions = maxInt(merged.WebRTC.OpenSessions, step.WebRTC.OpenSessions)
		merged.WebRTCMedia.Enabled = merged.WebRTCMedia.Enabled || step.WebRTCMedia.Enabled
		merged.WebRTCMedia.Attempts += step.WebRTCMedia.Attempts
		merged.WebRTCMedia.Successes += step.WebRTCMedia.Successes
		merged.WebRTCMedia.Failures += step.WebRTCMedia.Failures
		merged.WebRTCMedia.ICEConnectedP95MS = maxInt64(merged.WebRTCMedia.ICEConnectedP95MS, step.WebRTCMedia.ICEConnectedP95MS)
		merged.WebRTCMedia.TimeToFirstRTPP95MS = maxInt64(merged.WebRTCMedia.TimeToFirstRTPP95MS, step.WebRTCMedia.TimeToFirstRTPP95MS)
		merged.WebRTCMedia.PacketsReceived += step.WebRTCMedia.PacketsReceived
		merged.WebRTCMedia.BytesReceived += step.WebRTCMedia.BytesReceived
		merged.WebRTCMedia.H264PacketsReceived += step.WebRTCMedia.H264PacketsReceived
		merged.WebRTCMedia.H264BytesReceived += step.WebRTCMedia.H264BytesReceived
		merged.WebRTCMedia.OpusPacketsReceived += step.WebRTCMedia.OpusPacketsReceived
		merged.WebRTCMedia.OpusBytesReceived += step.WebRTCMedia.OpusBytesReceived
		merged.WebRTCMedia.Startup = mergeVideoStartupTotals(merged.WebRTCMedia.Startup, step.WebRTCMedia.Startup)
		merged.TURN.RegistryAvailable = merged.TURN.RegistryAvailable || step.TURN.RegistryAvailable
		merged.TURN.CoturnAvailable = merged.TURN.CoturnAvailable || step.TURN.CoturnAvailable
		merged.TURN.ActiveNodes = maxInt64(merged.TURN.ActiveNodes, step.TURN.ActiveNodes)
		merged.TURN.Allocations = maxInt64(merged.TURN.Allocations, step.TURN.Allocations)
		merged.TURN.ActiveSessions = maxInt64(merged.TURN.ActiveSessions, step.TURN.ActiveSessions)
		merged.TURN.UDPSockets = maxInt64(merged.TURN.UDPSockets, step.TURN.UDPSockets)
		merged.TURN.TCPEstablished = maxInt64(merged.TURN.TCPEstablished, step.TURN.TCPEstablished)
		merged.TURN.RelayUDPFlows = maxInt64(merged.TURN.RelayUDPFlows, step.TURN.RelayUDPFlows)
		merged.TURN.RelayTCPFlows = maxInt64(merged.TURN.RelayTCPFlows, step.TURN.RelayTCPFlows)
		merged.TURN.JournalEvents = maxInt64(merged.TURN.JournalEvents, step.TURN.JournalEvents)
		merged.TURN.CoturnCPUPercent = maxInt64(merged.TURN.CoturnCPUPercent, step.TURN.CoturnCPUPercent)
		merged.TURN.CoturnRSSKB = maxInt64(merged.TURN.CoturnRSSKB, step.TURN.CoturnRSSKB)
		merged.TURN.RXBytes = maxInt64(merged.TURN.RXBytes, step.TURN.RXBytes)
		merged.TURN.TXBytes = maxInt64(merged.TURN.TXBytes, step.TURN.TXBytes)
		merged.TURN.APITURNRegistryLookupSucceeded = maxInt64(merged.TURN.APITURNRegistryLookupSucceeded, step.TURN.APITURNRegistryLookupSucceeded)
		merged.TURN.APITURNRegistryLookupEmpty = maxInt64(merged.TURN.APITURNRegistryLookupEmpty, step.TURN.APITURNRegistryLookupEmpty)
		merged.TURN.APITURNRegistryLookupFailed = maxInt64(merged.TURN.APITURNRegistryLookupFailed, step.TURN.APITURNRegistryLookupFailed)
		merged.TURN.APIStaticTURNCount = maxInt64(merged.TURN.APIStaticTURNCount, step.TURN.APIStaticTURNCount)
		merged.TURN.APIDynamicTURNCount = maxInt64(merged.TURN.APIDynamicTURNCount, step.TURN.APIDynamicTURNCount)
		merged.TURN.APITURNRegistryNodeCount = maxInt64(merged.TURN.APITURNRegistryNodeCount, step.TURN.APITURNRegistryNodeCount)
		merged.TURN.EvidenceStatus = mergeTURNEvidenceStatus(merged.TURN.EvidenceStatus, step.TURN.EvidenceStatus)
		if !step.Thresholds.Passed || len(step.Thresholds.Failures) > 0 {
			merged.Thresholds.Passed = false
			merged.Thresholds.Failures = append(merged.Thresholds.Failures, step.Thresholds.Failures...)
		}
		merged.RelayCandidateSamples += step.RelayCandidateSamples
		merged.NonRelayCandidateSamples += step.NonRelayCandidateSamples
		merged.Notes = append(merged.Notes, step.Notes...)
	}
	attempts := merged.WebRTC.CreateAttempts + merged.WebRTC.SetupAttempts + merged.WebRTC.CloseAttempts
	successes := merged.WebRTC.CreateSuccess + merged.WebRTC.SetupSuccess + merged.WebRTC.CloseSuccess
	if attempts > 0 {
		merged.WebRTC.SuccessRatePercent = float64(successes) * 100 / float64(attempts)
	}
	if len(merged.Thresholds.Failures) == 0 {
		merged.Thresholds.Passed = true
	}
	return merged
}

func mergeVideoStartupTotals(a, b VideoStartupTotals) VideoStartupTotals {
	a.Samples += b.Samples
	a.H264AccessUnitSamples += b.H264AccessUnitSamples
	a.AppRequestToFirstRTPP50MS = maxInt64(a.AppRequestToFirstRTPP50MS, b.AppRequestToFirstRTPP50MS)
	a.AppRequestToFirstRTPP95MS = maxInt64(a.AppRequestToFirstRTPP95MS, b.AppRequestToFirstRTPP95MS)
	a.AppRequestToFirstRTPP99MS = maxInt64(a.AppRequestToFirstRTPP99MS, b.AppRequestToFirstRTPP99MS)
	a.AppRequestToFirstH264AccessUnitP50MS = maxInt64(a.AppRequestToFirstH264AccessUnitP50MS, b.AppRequestToFirstH264AccessUnitP50MS)
	a.AppRequestToFirstH264AccessUnitP95MS = maxInt64(a.AppRequestToFirstH264AccessUnitP95MS, b.AppRequestToFirstH264AccessUnitP95MS)
	a.AppRequestToFirstH264AccessUnitP99MS = maxInt64(a.AppRequestToFirstH264AccessUnitP99MS, b.AppRequestToFirstH264AccessUnitP99MS)
	a.BreakdownP95.APICreateMS = maxInt64(a.BreakdownP95.APICreateMS, b.BreakdownP95.APICreateMS)
	a.BreakdownP95.OfferDeliveryMS = maxInt64(a.BreakdownP95.OfferDeliveryMS, b.BreakdownP95.OfferDeliveryMS)
	a.BreakdownP95.DeviceAnswerMS = maxInt64(a.BreakdownP95.DeviceAnswerMS, b.BreakdownP95.DeviceAnswerMS)
	a.BreakdownP95.PionCreatePeerMS = maxInt64(a.BreakdownP95.PionCreatePeerMS, b.BreakdownP95.PionCreatePeerMS)
	a.BreakdownP95.PionCreateOfferMS = maxInt64(a.BreakdownP95.PionCreateOfferMS, b.BreakdownP95.PionCreateOfferMS)
	a.BreakdownP95.PionCreateAnswerMS = maxInt64(a.BreakdownP95.PionCreateAnswerMS, b.BreakdownP95.PionCreateAnswerMS)
	a.BreakdownP95.PionSetLocalDescriptionMS = maxInt64(a.BreakdownP95.PionSetLocalDescriptionMS, b.BreakdownP95.PionSetLocalDescriptionMS)
	a.BreakdownP95.PionICEGatheringWaitMS = maxInt64(a.BreakdownP95.PionICEGatheringWaitMS, b.BreakdownP95.PionICEGatheringWaitMS)
	a.BreakdownP95.PionSetRemoteDescriptionMS = maxInt64(a.BreakdownP95.PionSetRemoteDescriptionMS, b.BreakdownP95.PionSetRemoteDescriptionMS)
	a.BreakdownP95.RemoteAnswerSetMS = maxInt64(a.BreakdownP95.RemoteAnswerSetMS, b.BreakdownP95.RemoteAnswerSetMS)
	a.BreakdownP95.ICEConnectMS = maxInt64(a.BreakdownP95.ICEConnectMS, b.BreakdownP95.ICEConnectMS)
	a.BreakdownP95.ICECheckMS = maxInt64(a.BreakdownP95.ICECheckMS, b.BreakdownP95.ICECheckMS)
	a.BreakdownP95.ICEConnectedSinceSessionStartMS = maxInt64(a.BreakdownP95.ICEConnectedSinceSessionStartMS, b.BreakdownP95.ICEConnectedSinceSessionStartMS)
	a.BreakdownP95.FirstRTPAfterICEMS = maxInt64(a.BreakdownP95.FirstRTPAfterICEMS, b.BreakdownP95.FirstRTPAfterICEMS)
	a.BreakdownP95.FirstH264AccessUnitAfterRTPMS = maxInt64(a.BreakdownP95.FirstH264AccessUnitAfterRTPMS, b.BreakdownP95.FirstH264AccessUnitAfterRTPMS)
	a.BreakdownP95.SenderFirstWriteSinceSessionMS = maxInt64(a.BreakdownP95.SenderFirstWriteSinceSessionMS, b.BreakdownP95.SenderFirstWriteSinceSessionMS)
	return a
}

func videoEvidenceWithServerEvidence(video VideoEvidence, evidence ServerEvidence) VideoEvidence {
	video.TURN = turnEvidenceWithServerEvidence(video.TURN, evidence)
	for i := range video.Steps {
		video.Steps[i].TURN = turnEvidenceWithServerEvidence(video.Steps[i].TURN, evidence)
		video.Steps[i].Complete = video.Steps[i].WebRTC.CreateAttempts > 0 &&
			video.Steps[i].WebRTC.SetupAttempts > 0 &&
			video.Steps[i].WebRTC.CloseAttempts > 0 &&
			video.Steps[i].TURN.RegistryAvailable &&
			video.Steps[i].TURN.CoturnAvailable &&
			video.Steps[i].TURN.ActiveNodes > 0
	}
	video.Complete = video.WebRTC.CreateAttempts > 0 &&
		video.WebRTC.SetupAttempts > 0 &&
		video.WebRTC.CloseAttempts > 0 &&
		video.TURN.RegistryAvailable &&
		video.TURN.CoturnAvailable &&
		video.TURN.ActiveNodes > 0
	return video
}

func turnEvidenceWithServerEvidence(turn TURNEvidence, evidence ServerEvidence) TURNEvidence {
	if source, ok := evidence.Sources["turn_registry"]; ok {
		turn.RegistryAvailable = turn.RegistryAvailable || source.Available
		if value := source.Counters["turn_registry.active_nodes"]; value > turn.ActiveNodes {
			turn.ActiveNodes = value
		}
		if value := source.Counters["turn_registry.ready_pods"]; value > turn.ActiveNodes {
			turn.ActiveNodes = value
		}
	}
	if source, ok := evidence.Sources["coturn"]; ok {
		turn.CoturnAvailable = turn.CoturnAvailable || source.Available
		if value := source.Counters["coturn.allocations"]; value > turn.Allocations {
			turn.Allocations = value
		}
		if value := source.Counters["coturn.active_sessions"]; value > turn.ActiveSessions {
			turn.ActiveSessions = value
		}
		if value := source.Counters["coturn.udp_sockets"]; value > turn.UDPSockets {
			turn.UDPSockets = value
		}
		if value := source.Counters["coturn.tcp_established"]; value > turn.TCPEstablished {
			turn.TCPEstablished = value
		}
		if value := source.Counters["coturn.relay_udp_flows"]; value > turn.RelayUDPFlows {
			turn.RelayUDPFlows = value
		}
		if value := source.Counters["coturn.relay_tcp_flows"]; value > turn.RelayTCPFlows {
			turn.RelayTCPFlows = value
		}
		if value := source.Counters["coturn.journal_events"]; value > turn.JournalEvents {
			turn.JournalEvents = value
		}
		if value := source.Counters["coturn.ready_pods"]; value > turn.ActiveNodes {
			turn.ActiveNodes = value
		}
		if value := source.Counters["coturn.active_nodes"]; value > turn.ActiveNodes {
			turn.ActiveNodes = value
		}
		if value := source.Counters["coturn.configured_nodes"]; value > turn.ActiveNodes {
			turn.ActiveNodes = value
		}
	}
	if source, ok := evidence.Sources["loki_webrtc_trace"]; ok {
		if source.Counters != nil {
			turn.APITURNRegistryLookupSucceeded = maxInt64(turn.APITURNRegistryLookupSucceeded, source.Counters["loki_webrtc_trace.turn_registry_lookup_succeeded.events"])
			turn.APITURNRegistryLookupEmpty = maxInt64(turn.APITURNRegistryLookupEmpty, source.Counters["loki_webrtc_trace.turn_registry_lookup_empty.events"])
			turn.APITURNRegistryLookupFailed = maxInt64(turn.APITURNRegistryLookupFailed, source.Counters["loki_webrtc_trace.turn_registry_lookup_failed.events"])
			turn.APIStaticTURNCount = maxInt64(turn.APIStaticTURNCount, source.Counters["loki_webrtc_trace.ice_servers_resolved.static_turn_count_max"])
			turn.APIDynamicTURNCount = maxInt64(turn.APIDynamicTURNCount, source.Counters["loki_webrtc_trace.ice_servers_resolved.dynamic_turn_count_max"])
			turn.APITURNRegistryNodeCount = maxInt64(turn.APITURNRegistryNodeCount, source.Counters["loki_webrtc_trace.ice_servers_resolved.turn_registry_node_count_max"])
			turn.APITURNRegistryNodeCount = maxInt64(turn.APITURNRegistryNodeCount, source.Counters["loki_webrtc_trace.turn_registry_lookup_succeeded.turn_registry_node_count_max"])
			turn.APITURNRegistryNodeCount = maxInt64(turn.APITURNRegistryNodeCount, source.Counters["loki_webrtc_trace.turn_registry_lookup_empty.turn_registry_node_count_max"])
		}
	}
	return turn
}

func videoEvidenceWithActiveTURNSamples(evidence VideoEvidence, dir string) VideoEvidence {
	turn := turnEvidenceFromActiveSamples(filepath.Join(dir, "turn-active-samples.tsv"))
	if turn.ActiveSessions == 0 && turn.Allocations == 0 && turn.RelayUDPFlows == 0 && turn.RelayTCPFlows == 0 && turn.UDPSockets == 0 && turn.TCPEstablished == 0 && turn.JournalEvents == 0 && turn.CoturnCPUPercent == 0 && turn.CoturnRSSKB == 0 && turn.RXBytes == 0 && turn.TXBytes == 0 {
		return evidence
	}
	evidence.TURN.CoturnAvailable = true
	evidence.TURN.Allocations = maxInt64(evidence.TURN.Allocations, turn.Allocations)
	evidence.TURN.ActiveSessions = maxInt64(evidence.TURN.ActiveSessions, turn.ActiveSessions)
	evidence.TURN.ActiveNodes = maxInt64(evidence.TURN.ActiveNodes, turn.ActiveNodes)
	evidence.TURN.RelayUDPFlows = maxInt64(evidence.TURN.RelayUDPFlows, turn.RelayUDPFlows)
	evidence.TURN.RelayTCPFlows = maxInt64(evidence.TURN.RelayTCPFlows, turn.RelayTCPFlows)
	evidence.TURN.UDPSockets = maxInt64(evidence.TURN.UDPSockets, turn.UDPSockets)
	evidence.TURN.TCPEstablished = maxInt64(evidence.TURN.TCPEstablished, turn.TCPEstablished)
	evidence.TURN.JournalEvents = maxInt64(evidence.TURN.JournalEvents, turn.JournalEvents)
	evidence.TURN.CoturnCPUPercent = maxInt64(evidence.TURN.CoturnCPUPercent, turn.CoturnCPUPercent)
	evidence.TURN.CoturnRSSKB = maxInt64(evidence.TURN.CoturnRSSKB, turn.CoturnRSSKB)
	evidence.TURN.RXBytes = maxInt64(evidence.TURN.RXBytes, turn.RXBytes)
	evidence.TURN.TXBytes = maxInt64(evidence.TURN.TXBytes, turn.TXBytes)
	evidence.TURN.EvidenceStatus = mergeTURNEvidenceStatus(evidence.TURN.EvidenceStatus, turn.EvidenceStatus)
	evidence.Complete = evidence.WebRTC.CreateAttempts > 0 &&
		evidence.WebRTC.SetupAttempts > 0 &&
		evidence.WebRTC.CloseAttempts > 0 &&
		evidence.TURN.RegistryAvailable &&
		evidence.TURN.CoturnAvailable &&
		evidence.TURN.ActiveNodes > 0
	if len(evidence.Steps) > 0 {
		evidence.Steps[0].TURN = evidence.TURN
		evidence.Steps[0].Complete = evidence.Complete
	}
	return evidence
}

func turnEvidenceFromActiveSamples(path string) TURNEvidence {
	raw, err := os.ReadFile(path)
	if err != nil {
		return TURNEvidence{}
	}
	lines := strings.Split(string(raw), "\n")
	var maxUDPSockets int64
	var maxTCPEstablished int64
	var maxEvents int64
	var maxAllocations int64
	var maxSessions int64
	var maxRelayUDP int64
	var maxRelayTCP int64
	var maxCoturnCPU int64
	var maxCoturnRSS int64
	var maxRXBytes int64
	var maxTXBytes int64
	nodes := map[string]bool{}
	nodeIndex := 1
	udpIndex := 3
	tcpIndex := -1
	eventsIndex := 4
	relayUDPIndex := -1
	relayTCPIndex := -1
	allocationsIndex := -1
	sessionsIndex := -1
	cpuIndex := -1
	rssIndex := -1
	rxIndex := -1
	txIndex := -1
	statusIndex := -1
	status := ""
	hasStrongEvidence := false
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if i == 0 && (strings.HasPrefix(line, "time\t") || strings.HasPrefix(line, "ts\t")) {
			for idx, field := range fields {
				switch strings.TrimSpace(field) {
				case "node":
					nodeIndex = idx
				case "udp_sockets":
					udpIndex = idx
				case "tcp_estab", "tcp_sockets":
					tcpIndex = idx
				case "journal_events", "events":
					eventsIndex = idx
				case "relay_udp_flows":
					relayUDPIndex = idx
				case "relay_tcp_flows":
					relayTCPIndex = idx
				case "active_allocations", "allocations":
					allocationsIndex = idx
				case "active_sessions", "sessions":
					sessionsIndex = idx
				case "coturn_cpu_pct":
					cpuIndex = idx
				case "coturn_rss_kb":
					rssIndex = idx
				case "rx_bytes":
					rxIndex = idx
				case "tx_bytes":
					txIndex = idx
				case "evidence_status":
					statusIndex = idx
				}
			}
			continue
		}
		if len(fields) <= nodeIndex {
			continue
		}
		node := strings.TrimSpace(fields[nodeIndex])
		if node != "" {
			nodes[node] = true
		}
		if udpIndex >= 0 && len(fields) > udpIndex {
			maxUDPSockets = maxInt64(maxUDPSockets, parseInt64Default(fields[udpIndex], 0))
		}
		if tcpIndex >= 0 && len(fields) > tcpIndex {
			maxTCPEstablished = maxInt64(maxTCPEstablished, parseInt64Default(fields[tcpIndex], 0))
		}
		if eventsIndex >= 0 && len(fields) > eventsIndex {
			maxEvents = maxInt64(maxEvents, parseInt64Default(fields[eventsIndex], 0))
		}
		if relayUDPIndex >= 0 && len(fields) > relayUDPIndex {
			maxRelayUDP = maxInt64(maxRelayUDP, parseInt64Default(fields[relayUDPIndex], 0))
		}
		if relayTCPIndex >= 0 && len(fields) > relayTCPIndex {
			maxRelayTCP = maxInt64(maxRelayTCP, parseInt64Default(fields[relayTCPIndex], 0))
		}
		if allocationsIndex >= 0 && len(fields) > allocationsIndex {
			maxAllocations = maxInt64(maxAllocations, parseInt64Default(fields[allocationsIndex], 0))
		}
		if sessionsIndex >= 0 && len(fields) > sessionsIndex {
			maxSessions = maxInt64(maxSessions, parseInt64Default(fields[sessionsIndex], 0))
		}
		if cpuIndex >= 0 && len(fields) > cpuIndex {
			maxCoturnCPU = maxInt64(maxCoturnCPU, parseInt64Default(fields[cpuIndex], 0))
		}
		if rssIndex >= 0 && len(fields) > rssIndex {
			maxCoturnRSS = maxInt64(maxCoturnRSS, parseInt64Default(fields[rssIndex], 0))
		}
		if rxIndex >= 0 && len(fields) > rxIndex {
			maxRXBytes = maxInt64(maxRXBytes, parseInt64Default(fields[rxIndex], 0))
		}
		if txIndex >= 0 && len(fields) > txIndex {
			maxTXBytes = maxInt64(maxTXBytes, parseInt64Default(fields[txIndex], 0))
		}
		if statusIndex >= 0 && len(fields) > statusIndex {
			lineStatus := strings.TrimSpace(fields[statusIndex])
			status = mergeTURNEvidenceStatus(status, lineStatus)
			if lineStatus == "active" || lineStatus == "cli_active" || lineStatus == "prometheus_active" || lineStatus == "journal_active" || lineStatus == "conntrack_active" {
				hasStrongEvidence = true
			}
		}
	}
	if maxAllocations > 0 || maxSessions > 0 {
		hasStrongEvidence = true
	}
	if hasStrongEvidence {
		active := maxInt64(maxAllocations, maxSessions)
		if active == 0 {
			return TURNEvidence{}
		}
		return TURNEvidence{
			CoturnAvailable:  true,
			ActiveNodes:      int64(len(nodes)),
			Allocations:      maxInt64(maxAllocations, active),
			ActiveSessions:   maxInt64(maxSessions, active),
			UDPSockets:       maxUDPSockets,
			TCPEstablished:   maxTCPEstablished,
			RelayUDPFlows:    maxRelayUDP,
			RelayTCPFlows:    maxRelayTCP,
			JournalEvents:    maxEvents,
			CoturnCPUPercent: maxCoturnCPU,
			CoturnRSSKB:      maxCoturnRSS,
			RXBytes:          maxRXBytes,
			TXBytes:          maxTXBytes,
			EvidenceStatus:   firstNonEmpty(status, "active"),
		}
	}
	if maxUDPSockets == 0 && maxTCPEstablished == 0 && maxEvents == 0 && maxRelayUDP == 0 && maxRelayTCP == 0 && maxCoturnCPU == 0 && maxCoturnRSS == 0 && maxRXBytes == 0 && maxTXBytes == 0 {
		return TURNEvidence{}
	}
	if status == "" || status == "unavailable" {
		status = "socket_activity_observed"
	}
	return TURNEvidence{
		CoturnAvailable:  true,
		ActiveNodes:      int64(len(nodes)),
		UDPSockets:       maxUDPSockets,
		TCPEstablished:   maxTCPEstablished,
		RelayUDPFlows:    maxRelayUDP,
		RelayTCPFlows:    maxRelayTCP,
		JournalEvents:    maxEvents,
		CoturnCPUPercent: maxCoturnCPU,
		CoturnRSSKB:      maxCoturnRSS,
		RXBytes:          maxRXBytes,
		TXBytes:          maxTXBytes,
		EvidenceStatus:   status,
	}
}

func mergeTURNEvidenceStatus(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	rank := func(value string) int {
		switch value {
		case "active":
			return 5
		case "cli_active":
			return 4
		case "prometheus_active":
			return 4
		case "journal_active":
			return 4
		case "conntrack_active":
			return 4
		case "relay_flow_observed":
			return 3
		case "socket_activity_observed":
			return 2
		case "unavailable":
			return 1
		case "":
			return 0
		default:
			return 1
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
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

func isNoShardResultsError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no shard results found")
}

func hasVideoEvidence(evidence VideoEvidence) bool {
	return evidence.WebRTC.CreateAttempts > 0 ||
		evidence.WebRTC.SetupAttempts > 0 ||
		evidence.WebRTC.CloseAttempts > 0 ||
		evidence.WebRTCMedia.Attempts > 0 ||
		evidence.TURN.CoturnAvailable
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

func parseInt64Default(value string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func maxInt(a, b int) int {
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
		"central_logger":    {Available: available, Optional: true},
		"loki_webrtc_trace": {Available: available, Optional: true},
		"edge_haproxy":      {Available: available, Optional: true},
		"turn_registry":     {Available: available, Optional: true},
		"coturn":            {Available: available, Optional: true},
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
	if isVideoTurnSizingProfile(plan.VideoProfile.Name) && videoEvidence.WebRTCMedia.Attempts > 0 {
		signalingStoreReasons := webrtcSignalingStoreEvidenceFailures(evidence)
		if len(signalingStoreReasons) > 0 {
			videoIncomplete = true
			videoFailReasons = append(videoFailReasons, signalingStoreReasons...)
		}
	}
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
	if isVideoTurnSizingProfile(plan.VideoProfile.Name) && strings.TrimSpace(plan.VideoProfile.WebRTCICEPolicy) == "relay" && evidence.WebRTCMedia.Attempts > 0 && evidence.RelayCandidateSamples == 0 && evidence.NonRelayCandidateSamples == 0 {
		incomplete = true
		reasons = append(reasons, "Missing relay candidate evidence for relay-only WebRTC media")
	}
	if isVideoTurnSizingProfile(plan.VideoProfile.Name) && evidence.WebRTCMedia.Attempts > 0 && evidence.TURN.Allocations == 0 && evidence.TURN.ActiveSessions == 0 {
		incomplete = true
		if evidence.TURN.UDPSockets > 0 || evidence.TURN.TCPEstablished > 0 || evidence.TURN.RelayUDPFlows > 0 || evidence.TURN.RelayTCPFlows > 0 || evidence.TURN.JournalEvents > 0 {
			reasons = append(reasons, "Missing precise active-window TURN allocations/sessions evidence; coturn transport activity was observed")
		} else {
			reasons = append(reasons, "Missing active-window TURN allocations/sessions evidence")
		}
	}
	if isVideoTurnSizingProfile(plan.VideoProfile.Name) && evidence.WebRTCMedia.Attempts > 0 && evidence.TURN.ActiveNodes > 0 && evidence.TURN.APIDynamicTURNCount == 0 {
		incomplete = true
		reasons = append(reasons, "API did not return dynamic TURN servers from active turnregistry nodes; static TURN URLs are not valid coturn sizing evidence")
	}
	if isVideoTurnSizingProfile(plan.VideoProfile.Name) && evidence.WebRTCMedia.Attempts > 0 && evidence.TURN.APITURNRegistryLookupEmpty > 0 && evidence.TURN.APITURNRegistryLookupSucceeded == 0 {
		incomplete = true
		reasons = append(reasons, "API turnregistry lookup returned empty during WebRTC ICE server resolution")
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
	if evidence.NonRelayCandidateSamples > 0 {
		reasons = append(reasons, fmt.Sprintf("relay-only WebRTC selected non-relay candidates in %d samples", evidence.NonRelayCandidateSamples))
	}
	if strings.TrimSpace(plan.VideoProfile.WebRTCMediaSet) != "" && plan.VideoProfile.WebRTCMediaSet != "off" {
		if evidence.WebRTCMedia.Attempts == 0 || evidence.WebRTCMedia.Successes == 0 || evidence.WebRTCMedia.ICEConnectedP95MS == 0 || evidence.WebRTCMedia.TimeToFirstRTPP95MS == 0 {
			reasons = append(reasons, "WebRTC media evidence missing ICE connected or first RTP")
		}
		if evidence.WebRTCMedia.Attempts > 0 {
			mediaSuccessRate := float64(evidence.WebRTCMedia.Successes) * 100 / float64(evidence.WebRTCMedia.Attempts)
			if mediaSuccessRate < threshold {
				reasons = append(reasons, fmt.Sprintf("WebRTC media success rate %.2f%% below %.2f%% threshold (%d/%d)", mediaSuccessRate, threshold, evidence.WebRTCMedia.Successes, evidence.WebRTCMedia.Attempts))
			}
		}
	}
	for _, step := range evidence.Steps {
		stepName := firstNonEmpty(step.Name, fmt.Sprintf("%d-viewers", step.Viewers))
		if step.WebRTC.CreateAttempts == 0 || step.WebRTC.SetupAttempts == 0 || step.WebRTC.CloseAttempts == 0 {
			reasons = append(reasons, fmt.Sprintf("video step %s missing WebRTC create/setup/close evidence", stepName))
			continue
		}
		if step.WebRTC.SuccessRatePercent < threshold {
			reasons = append(reasons, fmt.Sprintf("video step %s WebRTC signaling success rate %.2f%% below %.2f%% threshold", stepName, step.WebRTC.SuccessRatePercent, threshold))
		}
		if step.WebRTCMedia.Attempts > 0 {
			mediaSuccessRate := float64(step.WebRTCMedia.Successes) * 100 / float64(step.WebRTCMedia.Attempts)
			if mediaSuccessRate < threshold {
				reasons = append(reasons, fmt.Sprintf("video step %s WebRTC media success rate %.2f%% below %.2f%% threshold (%d/%d)", stepName, mediaSuccessRate, threshold, step.WebRTCMedia.Successes, step.WebRTCMedia.Attempts))
			}
		}
		if strings.TrimSpace(step.ICEPolicy) == "relay" && step.NonRelayCandidateSamples > 0 {
			reasons = append(reasons, fmt.Sprintf("video step %s selected non-relay candidates in %d samples", stepName, step.NonRelayCandidateSamples))
		}
		for _, failure := range step.Thresholds.Failures {
			reasons = append(reasons, fmt.Sprintf("video step %s threshold failed: %s", stepName, failure))
		}
	}
	return false, reasons
}

func webrtcSignalingStoreEvidenceFailures(evidence ServerEvidence) []string {
	runningPods := evidenceCounter(evidence, "video_cloud_api", "video_cloud_api.k8s.running_pods")
	desiredReplicas := evidenceCounter(evidence, "video_cloud_api", "video_cloud_api.k8s.desired_replicas")
	enabledPods := evidenceCounter(evidence, "video_cloud_api", "video_cloud_api.webrtc_signaling_store.enabled_pods")
	addrPods := evidenceCounter(evidence, "video_cloud_api", "video_cloud_api.webrtc_signaling_store.addr_pods")
	prefixPods := evidenceCounter(evidence, "video_cloud_api", "video_cloud_api.webrtc_signaling_store.prefix_pods")
	if runningPods == 0 && desiredReplicas == 0 && enabledPods == 0 && addrPods == 0 && prefixPods == 0 {
		return []string{"Missing multi-pod WebRTC signaling store evidence"}
	}
	reasons := []string{}
	if runningPods < 2 {
		reasons = append(reasons, fmt.Sprintf("WebRTC TURN sizing requires multi-pod API evidence: running API pods %d < 2", runningPods))
	}
	if desiredReplicas > 0 && desiredReplicas < 2 {
		reasons = append(reasons, fmt.Sprintf("WebRTC TURN sizing requires API desired replicas >= 2, got %d", desiredReplicas))
	}
	if enabledPods < runningPods {
		reasons = append(reasons, fmt.Sprintf("WebRTC signaling store enabled on %d/%d API pods", enabledPods, runningPods))
	}
	if addrPods < runningPods {
		reasons = append(reasons, fmt.Sprintf("WebRTC signaling store address configured on %d/%d API pods", addrPods, runningPods))
	}
	if prefixPods < runningPods {
		reasons = append(reasons, fmt.Sprintf("WebRTC signaling store prefix configured on %d/%d API pods", prefixPods, runningPods))
	}
	return reasons
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
	emqxConnectCheck := selectEMQXConnectCorrelationCheck(evidence, totalMQTTConnectSuccess, thresholds)
	checks := []CorrelationCheck{
		emqxConnectCheck,
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

func selectEMQXConnectCorrelationCheck(evidence ServerEvidence, clientTotal int64, thresholds GateThresholds) CorrelationCheck {
	candidates := []struct {
		name             string
		positiveWarning  bool
		preferWhenViable bool
	}{
		{name: "mqtt.total_connect_success", positiveWarning: true, preferWhenViable: true},
		{name: "device_mqtt.connect_success", preferWhenViable: true},
		{name: "emqx.metric.client.connack", preferWhenViable: true},
		{name: "emqx.metric.packets.connack.sent", preferWhenViable: true},
		{name: "emqx.metric.packets.connect.received", preferWhenViable: true},
		{name: "emqx.metric.client.connected", positiveWarning: true},
	}
	var first CorrelationCheck
	var best *CorrelationCheck
	for _, candidate := range candidates {
		serverTotal := evidenceCounter(evidence, "emqx", candidate.name)
		if serverTotal == 0 {
			continue
		}
		check := newEMQXConnectCorrelationCheckWithPolicy(candidate.name, clientTotal, serverTotal, aggregateCorrelationTolerance(clientTotal, serverTotal, thresholds), candidate.positiveWarning)
		if first.Counter == "" {
			first = check
		}
		if check.Status != "fail" {
			if best == nil || connectCorrelationRank(check) < connectCorrelationRank(*best) ||
				(connectCorrelationRank(check) == connectCorrelationRank(*best) && absInt64(check.Delta) < absInt64(best.Delta)) {
				copy := check
				best = &copy
			}
		}
	}
	if best != nil {
		return *best
	}
	if first.Counter != "" {
		return first
	}
	return newEMQXConnectCorrelationCheck("mqtt.total_connect_success", clientTotal, 0, aggregateCorrelationTolerance(clientTotal, 0, thresholds))
}

func connectCorrelationRank(check CorrelationCheck) int {
	switch check.Status {
	case "pass":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
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
	runtimeEvents := []CommandEvent{}
	for _, event := range events {
		if strings.TrimSpace(event.RuntimeLogStreamID) != "" && len(event.ExpectedLogs) > 0 {
			runtimeEvents = append(runtimeEvents, event)
		}
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
	if len(runtimeEvents) == 0 {
		result.Status = "skipped"
		return result
	}
	if !source.Available || len(source.Counters) == 0 {
		result.Status = "skipped"
		return result
	}
	for _, event := range runtimeEvents {
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
	allowedMissing := allowedMissingExactEvents(len(runtimeEvents), thresholds)
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

func newEMQXConnectCorrelationCheck(counter string, clientTotal int64, serverTotal int64, tolerance int64) CorrelationCheck {
	return newEMQXConnectCorrelationCheckWithPolicy(counter, clientTotal, serverTotal, tolerance, counter == "mqtt.total_connect_success")
}

func newEMQXConnectCorrelationCheckWithPolicy(counter string, clientTotal int64, serverTotal int64, tolerance int64, positiveOverageWarning bool) CorrelationCheck {
	check := newCorrelationCheck("emqx", counter, clientTotal, serverTotal, tolerance)
	if check.Status == "fail" && check.Delta > 0 && positiveOverageWarning {
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
