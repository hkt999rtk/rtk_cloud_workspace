package loadtest

import (
	"time"
)

const ResultSchema = "rtk-video-loadtest-results/v1"

const (
	ProfileSmoke       = "smoke"
	ProfileFunctional  = "functional"
	ProfileSafeStaging = "safe-staging"
	ProfileStress      = "stress"
	ProfileSoak        = "soak"
)

const (
	ActorApp    = "app"
	ActorDevice = "device"
	ActorViewer = "viewer"
	ActorAll    = "all"
)

const (
	DeviceOnlineModeNone      = "none"
	DeviceOnlineModeWebSocket = "websocket"
)

const (
	AppRouteSetSmoke      = "smoke"
	AppRouteSetFunctional = "functional"
)

const (
	DeviceRouteSetSmoke      = "smoke"
	DeviceRouteSetFunctional = "functional"
)

const (
	DeviceTransportSetSmoke    = "smoke"
	DeviceTransportSetSnapshot = "snapshot"
)

const (
	ViewerRouteSetSmoke      = "smoke"
	ViewerRouteSetFunctional = "functional"
	ViewerRouteSetNegative   = "negative"
)

const (
	WebRTCMediaSetOff = "off"
	WebRTCMediaSetRTP = "rtp"
)

const (
	ClipSetOff                 = "off"
	ClipSetRecordingFunctional = "recording-functional"
)

const (
	MQTTSetOff    = "off"
	MQTTSetBroker = "broker"
)

const (
	MQTTDeviceProfileCamera = "camera"
	MQTTDeviceProfileIoT    = "iot"
	MQTTDeviceProfileMixed  = "mixed"
)

const (
	NegativeSetOff  = "off"
	NegativeSetHTTP = "http"
)

type Config struct {
	Profile               string            `json:"profile"`
	APIURL                string            `json:"api_url"`
	WSURL                 string            `json:"ws_url,omitempty"`
	AccountToken          string            `json:"-"`
	AppTokens             map[string]string `json:"-"`
	AdminToken            string            `json:"-"`
	DeviceToken           string            `json:"-"`
	DeviceTokens          map[string]string `json:"-"`
	RefreshToken          string            `json:"-"`
	RunID                 string            `json:"run_id"`
	InstanceID            string            `json:"instance_id"`
	Actors                string            `json:"actors"`
	AppRouteSet           string            `json:"app_route_set"`
	DeviceRouteSet        string            `json:"device_route_set"`
	DeviceTransportSet    string            `json:"device_transport_set"`
	ViewerRouteSet        string            `json:"viewer_route_set"`
	WebRTCMediaSet        string            `json:"webrtc_media_set"`
	ClipSet               string            `json:"clip_set"`
	MQTTSet               string            `json:"mqtt_set"`
	MQTTAddr              string            `json:"mqtt_addr,omitempty"`
	MQTTUsername          string            `json:"-"`
	MQTTPassword          string            `json:"-"`
	MQTTTopicRoot         string            `json:"mqtt_topic_root,omitempty"`
	MQTTDeviceProfile     string            `json:"mqtt_device_profile,omitempty"`
	MQTTIoTMix            string            `json:"mqtt_iot_mix,omitempty"`
	MQTTRequired          bool              `json:"mqtt_required,omitempty"`
	NegativeSet           string            `json:"negative_set"`
	NegativeMalformedPath string            `json:"negative_malformed_path,omitempty"`
	NegativeTimeoutPath   string            `json:"negative_timeout_path,omitempty"`
	DeviceOnlineMode      string            `json:"device_online_mode"`
	DevicePrefix          string            `json:"device_prefix"`
	DeviceIDs             []string          `json:"device_ids,omitempty"`
	ContractsCommit       string            `json:"contracts_commit,omitempty"`
	ServerCommit          string            `json:"server_commit,omitempty"`
	ClientCommit          string            `json:"client_commit,omitempty"`
	BinarySHA256          string            `json:"binary_sha256,omitempty"`
	Duration              time.Duration     `json:"duration"`
	VirtualDevices        int               `json:"virtual_devices"`
	VirtualViewers        int               `json:"virtual_viewers"`
	AppConcurrency        int               `json:"app_concurrency"`
	DeviceConcurrency     int               `json:"device_concurrency"`
	ViewerConcurrency     int               `json:"viewer_concurrency"`
	Iterations            int               `json:"iterations"`
	AppRatePerSecond      float64           `json:"app_rate_per_second"`
	DeviceRatePerSecond   float64           `json:"device_rate_per_second"`
	ViewerRatePerSecond   float64           `json:"viewer_rate_per_second"`
	RampUp                time.Duration     `json:"ramp_up"`
	AllowStress           bool              `json:"allow_stress"`
	AllowSoak             bool              `json:"allow_soak"`
	Thresholds            Thresholds        `json:"thresholds"`
	HTTPTimeout           time.Duration     `json:"http_timeout"`
}

type Thresholds struct {
	MinSuccessRate           float64 `json:"min_success_rate"`
	MaxP95Latency            int64   `json:"max_p95_latency_ms"`
	MaxP99Latency            int64   `json:"max_p99_latency_ms"`
	MaxWebRTCSetupP95Latency int64   `json:"max_webrtc_setup_p95_latency_ms"`
	MaxOpenWebRTCSessions    int     `json:"max_open_webrtc_sessions"`
	RequireCoverageMatrix    bool    `json:"require_coverage_matrix"`
}

type Result struct {
	Schema         string                  `json:"schema"`
	RunID          string                  `json:"run_id"`
	InstanceID     string                  `json:"instance_id"`
	Profile        string                  `json:"profile"`
	StartedAt      time.Time               `json:"started_at"`
	EndedAt        time.Time               `json:"ended_at"`
	DurationMS     int64                   `json:"duration_ms"`
	Config         RedactedConfig          `json:"config"`
	Summary        Summary                 `json:"summary"`
	Actors         map[string]ActorMetrics `json:"actors"`
	WebRTC         WebRTCMetrics           `json:"webrtc"`
	WebRTCMedia    WebRTCMediaMetrics      `json:"webrtc_media"`
	MQTTIoT        map[string]ActorMetrics `json:"mqtt_iot,omitempty"`
	CoverageMatrix map[string]CoverageItem `json:"coverage_matrix"`
	Errors         map[string]int          `json:"errors"`
	Operations     []Operation             `json:"operations"`
	Thresholds     ThresholdEvaluation     `json:"thresholds"`
	Metadata       map[string]string       `json:"metadata,omitempty"`
}

type RedactedConfig struct {
	APIURL             string   `json:"api_url"`
	WSURL              string   `json:"ws_url,omitempty"`
	DevicePrefix       string   `json:"device_prefix"`
	DeviceIDs          []string `json:"device_ids,omitempty"`
	Actors             string   `json:"actors"`
	AppRouteSet        string   `json:"app_route_set"`
	DeviceRouteSet     string   `json:"device_route_set"`
	DeviceTransportSet string   `json:"device_transport_set"`
	ViewerRouteSet     string   `json:"viewer_route_set"`
	WebRTCMediaSet     string   `json:"webrtc_media_set"`
	ClipSet            string   `json:"clip_set"`
	MQTTSet            string   `json:"mqtt_set"`
	MQTTAddr           string   `json:"mqtt_addr,omitempty"`
	MQTTUsername       string   `json:"mqtt_username,omitempty"`
	MQTTDeviceProfile  string   `json:"mqtt_device_profile,omitempty"`
	MQTTIoTMix         string   `json:"mqtt_iot_mix,omitempty"`
	MQTTRequired       bool     `json:"mqtt_required,omitempty"`
	NegativeSet        string   `json:"negative_set"`
	DeviceOnlineMode   string   `json:"device_online_mode"`
	VirtualDevices     int      `json:"virtual_devices"`
	VirtualViewers     int      `json:"virtual_viewers"`
	AppConcurrency     int      `json:"app_concurrency"`
	DeviceConcurrency  int      `json:"device_concurrency"`
	ViewerConcurrency  int      `json:"viewer_concurrency"`
	Iterations         int      `json:"iterations"`
	RampUpMS           int64    `json:"ramp_up_ms"`
	DurationMS         int64    `json:"duration_ms"`
	AccountToken       string   `json:"account_token"`
	AdminToken         string   `json:"admin_token"`
	DeviceToken        string   `json:"device_token,omitempty"`
	RefreshToken       string   `json:"refresh_token,omitempty"`
}

type Summary struct {
	TotalOperations     int     `json:"total_operations"`
	Successes           int     `json:"successes"`
	Failures            int     `json:"failures"`
	Skips               int     `json:"skips,omitempty"`
	SuccessRate         float64 `json:"success_rate"`
	P95LatencyMS        int64   `json:"p95_latency_ms"`
	P99LatencyMS        int64   `json:"p99_latency_ms"`
	ThroughputPerSecond float64 `json:"throughput_per_second"`
}

type ActorMetrics struct {
	Operations          int     `json:"operations"`
	Successes           int     `json:"successes"`
	Failures            int     `json:"failures"`
	Skips               int     `json:"skips,omitempty"`
	SuccessRate         float64 `json:"success_rate"`
	P95LatencyMS        int64   `json:"p95_latency_ms"`
	P99LatencyMS        int64   `json:"p99_latency_ms"`
	ThroughputPerSecond float64 `json:"throughput_per_second"`
}

type WebRTCMetrics struct {
	Attempts          int            `json:"attempts"`
	Successes         int            `json:"successes"`
	Failures          int            `json:"failures"`
	SuccessRate       float64        `json:"success_rate"`
	SetupLatencyP95MS int64          `json:"setup_latency_p95_ms"`
	SetupLatencyP99MS int64          `json:"setup_latency_p99_ms"`
	ICEServerCount    int            `json:"ice_server_count"`
	FailuresByClass   map[string]int `json:"failures_by_class"`
	Create            ActorMetrics   `json:"create"`
	Setup             ActorMetrics   `json:"setup"`
	Close             ActorMetrics   `json:"close"`
	OpenSessions      int            `json:"open_sessions"`
}

type WebRTCMediaMetrics struct {
	Attempts            int            `json:"attempts"`
	Successes           int            `json:"successes"`
	Failures            int            `json:"failures"`
	PacketsReceived     int            `json:"packets_received"`
	BytesReceived       int            `json:"bytes_received"`
	TimeToFirstRTPP95MS int64          `json:"time_to_first_rtp_p95_ms"`
	ICEConnectedP95MS   int64          `json:"ice_connected_p95_ms"`
	ReceiveDurationMS   int64          `json:"receive_duration_ms"`
	FailuresByClass     map[string]int `json:"failures_by_class"`
}

type Operation struct {
	Actor       string `json:"actor"`
	Name        string `json:"name"`
	DeviceID    string `json:"device_id,omitempty"`
	ViewerID    string `json:"viewer_id,omitempty"`
	Success     bool   `json:"success"`
	Skipped     bool   `json:"skipped,omitempty"`
	SkipReason  string `json:"skip_reason,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
	LatencyMS   int64  `json:"latency_ms"`
	Evidence    string `json:"evidence,omitempty"`
	ErrorClass  string `json:"error_class,omitempty"`
	ErrorDetail string `json:"error_detail,omitempty"`
}

const (
	CoverageStatusPass    = "PASS"
	CoverageStatusFail    = "FAIL"
	CoverageStatusSkip    = "SKIP"
	CoverageStatusBlocked = "BLOCKED"
	CoverageStatusNotRun  = "NOT_RUN"
)

type CoverageItem struct {
	Status     string   `json:"status"`
	Operations []string `json:"operations,omitempty"`
	Summary    string   `json:"summary,omitempty"`
}

type ThresholdEvaluation struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures"`
}
