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
	DeviceRouteSetOff        = "off"
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
	WebRTCMediaSetOff  = "off"
	WebRTCMediaSetRTP  = "rtp"
	WebRTCMediaSetH264 = "h264"
	WebRTCMediaSetAV   = "av"
)

const (
	WebRTCRelayRoleBoth       = "both"
	WebRTCRelayRoleAppOnly    = "app-only"
	WebRTCRelayRoleDeviceOnly = "device-only"
)

const (
	WebRTCICEPolicyAll   = "all"
	WebRTCICEPolicyRelay = "relay"
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
	WebRTCRelayRole       string            `json:"webrtc_relay_role"`
	WebRTCICEPolicy       string            `json:"webrtc_ice_policy"`
	WebRTCMediaDuration   time.Duration     `json:"webrtc_media_duration"`
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
	DeviceOnlineSettle    time.Duration     `json:"device_online_settle"`
	DeviceOwnerRetries    int               `json:"device_owner_retries"`
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
	MinSuccessRate            float64 `json:"min_success_rate"`
	MinWebRTCMediaSuccessRate float64 `json:"min_webrtc_media_success_rate"`
	MaxP95Latency             int64   `json:"max_p95_latency_ms"`
	MaxP99Latency             int64   `json:"max_p99_latency_ms"`
	MaxWebRTCSetupP95Latency  int64   `json:"max_webrtc_setup_p95_latency_ms"`
	MaxOpenWebRTCSessions     int     `json:"max_open_webrtc_sessions"`
	RequireCoverageMatrix     bool    `json:"require_coverage_matrix"`
}

type Result struct {
	Schema              string                      `json:"schema"`
	RunID               string                      `json:"run_id"`
	InstanceID          string                      `json:"instance_id"`
	Profile             string                      `json:"profile"`
	StartedAt           time.Time                   `json:"started_at"`
	EndedAt             time.Time                   `json:"ended_at"`
	DurationMS          int64                       `json:"duration_ms"`
	Config              RedactedConfig              `json:"config"`
	Summary             Summary                     `json:"summary"`
	Actors              map[string]ActorMetrics     `json:"actors"`
	WebRTC              WebRTCMetrics               `json:"webrtc"`
	WebRTCMedia         WebRTCMediaMetrics          `json:"webrtc_media"`
	MQTTIoT             map[string]ActorMetrics     `json:"mqtt_iot,omitempty"`
	CoverageMatrix      map[string]CoverageItem     `json:"coverage_matrix"`
	Errors              map[string]int              `json:"errors"`
	Operations          []Operation                 `json:"operations"`
	VideoStartupLatency []VideoStartupLatencySample `json:"video_startup_latency,omitempty"`
	Thresholds          ThresholdEvaluation         `json:"thresholds"`
	Metadata            map[string]string           `json:"metadata,omitempty"`
}

type RedactedConfig struct {
	APIURL               string   `json:"api_url"`
	WSURL                string   `json:"ws_url,omitempty"`
	DevicePrefix         string   `json:"device_prefix"`
	DeviceIDs            []string `json:"device_ids,omitempty"`
	Actors               string   `json:"actors"`
	AppRouteSet          string   `json:"app_route_set"`
	DeviceRouteSet       string   `json:"device_route_set"`
	DeviceTransportSet   string   `json:"device_transport_set"`
	ViewerRouteSet       string   `json:"viewer_route_set"`
	WebRTCMediaSet       string   `json:"webrtc_media_set"`
	WebRTCRelayRole      string   `json:"webrtc_relay_role"`
	WebRTCICEPolicy      string   `json:"webrtc_ice_policy"`
	ClipSet              string   `json:"clip_set"`
	MQTTSet              string   `json:"mqtt_set"`
	MQTTAddr             string   `json:"mqtt_addr,omitempty"`
	MQTTUsername         string   `json:"mqtt_username,omitempty"`
	MQTTDeviceProfile    string   `json:"mqtt_device_profile,omitempty"`
	MQTTIoTMix           string   `json:"mqtt_iot_mix,omitempty"`
	MQTTRequired         bool     `json:"mqtt_required,omitempty"`
	NegativeSet          string   `json:"negative_set"`
	DeviceOnlineMode     string   `json:"device_online_mode"`
	DeviceOnlineSettleMS int64    `json:"device_online_settle_ms"`
	DeviceOwnerRetries   int      `json:"device_owner_retries"`
	VirtualDevices       int      `json:"virtual_devices"`
	VirtualViewers       int      `json:"virtual_viewers"`
	AppConcurrency       int      `json:"app_concurrency"`
	DeviceConcurrency    int      `json:"device_concurrency"`
	ViewerConcurrency    int      `json:"viewer_concurrency"`
	Iterations           int      `json:"iterations"`
	RampUpMS             int64    `json:"ramp_up_ms"`
	DurationMS           int64    `json:"duration_ms"`
	AccountToken         string   `json:"account_token"`
	AdminToken           string   `json:"admin_token"`
	DeviceToken          string   `json:"device_token,omitempty"`
	RefreshToken         string   `json:"refresh_token,omitempty"`
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
	Attempts                             int                   `json:"attempts"`
	Successes                            int                   `json:"successes"`
	Failures                             int                   `json:"failures"`
	PacketsReceived                      int                   `json:"packets_received"`
	BytesReceived                        int                   `json:"bytes_received"`
	H264PacketsReceived                  int                   `json:"h264_packets_received,omitempty"`
	H264BytesReceived                    int                   `json:"h264_bytes_received,omitempty"`
	OpusPacketsReceived                  int                   `json:"opus_packets_received,omitempty"`
	OpusBytesReceived                    int                   `json:"opus_bytes_received,omitempty"`
	OpusFramesReceived                   int                   `json:"opus_frames_received,omitempty"`
	TimeToFirstRTPP95MS                  int64                 `json:"time_to_first_rtp_p95_ms"`
	AppRequestToFirstRTPP50MS            int64                 `json:"app_request_to_first_rtp_p50_ms,omitempty"`
	AppRequestToFirstRTPP95MS            int64                 `json:"app_request_to_first_rtp_p95_ms,omitempty"`
	AppRequestToFirstRTPP99MS            int64                 `json:"app_request_to_first_rtp_p99_ms,omitempty"`
	AppRequestToFirstH264AccessUnitP50MS int64                 `json:"app_request_to_first_h264_access_unit_p50_ms,omitempty"`
	AppRequestToFirstH264AccessUnitP95MS int64                 `json:"app_request_to_first_h264_access_unit_p95_ms,omitempty"`
	AppRequestToFirstH264AccessUnitP99MS int64                 `json:"app_request_to_first_h264_access_unit_p99_ms,omitempty"`
	BreakdownP95                         VideoStartupBreakdown `json:"breakdown_p95,omitempty"`
	VideoStartupLatency                  VideoStartupSummary   `json:"video_startup_latency,omitempty"`
	ICEConnectedP95MS                    int64                 `json:"ice_connected_p95_ms"`
	ReceiveDurationMS                    int64                 `json:"receive_duration_ms"`
	FailuresByClass                      map[string]int        `json:"failures_by_class"`
}

type VideoStartupBreakdown struct {
	APICreateMS                          int64 `json:"api_create_ms,omitempty"`
	OfferDeliveryMS                      int64 `json:"offer_delivery_ms,omitempty"`
	AnswerQueueWaitMS                    int64 `json:"answer_queue_wait_ms,omitempty"`
	AnswerPrepareMS                      int64 `json:"answer_prepare_ms,omitempty"`
	AnswerPostMS                         int64 `json:"answer_post_ms,omitempty"`
	DeviceAnswerMS                       int64 `json:"device_answer_ms,omitempty"`
	PionCreatePeerMS                     int64 `json:"pion_create_peer_ms,omitempty"`
	PionCreateOfferMS                    int64 `json:"pion_create_offer_ms,omitempty"`
	PionCreateAnswerMS                   int64 `json:"pion_create_answer_ms,omitempty"`
	PionSetLocalDescriptionMS            int64 `json:"pion_set_local_description_ms,omitempty"`
	PionICEGatheringWaitMS               int64 `json:"pion_ice_gathering_wait_ms,omitempty"`
	PionFirstLocalCandidateMS            int64 `json:"pion_first_local_candidate_ms,omitempty"`
	PionFirstLocalRelayCandidateMS       int64 `json:"pion_first_local_relay_candidate_ms,omitempty"`
	PionFirstLocalRelayUDPCandidateMS    int64 `json:"pion_first_local_relay_udp_candidate_ms,omitempty"`
	PionFirstLocalRelayTCPCandidateMS    int64 `json:"pion_first_local_relay_tcp_candidate_ms,omitempty"`
	PionRelayCandidateToGatherCompleteMS int64 `json:"pion_relay_candidate_to_gather_complete_ms,omitempty"`
	PionSetRemoteDescriptionMS           int64 `json:"pion_set_remote_description_ms,omitempty"`
	RemoteAnswerSetMS                    int64 `json:"remote_answer_set_ms,omitempty"`
	ICESelectedPairChanges               int64 `json:"ice_selected_pair_changes,omitempty"`
	ICESelectedPairFirstChangeMS         int64 `json:"ice_selected_pair_first_change_ms,omitempty"`
	ICESelectedPairLastChangeMS          int64 `json:"ice_selected_pair_last_change_ms,omitempty"`
	ICERequestsSent                      int64 `json:"ice_requests_sent,omitempty"`
	ICERequestsReceived                  int64 `json:"ice_requests_received,omitempty"`
	ICEResponsesSent                     int64 `json:"ice_responses_sent,omitempty"`
	ICEResponsesReceived                 int64 `json:"ice_responses_received,omitempty"`
	ICERetransmissionsSent               int64 `json:"ice_retransmissions_sent,omitempty"`
	ICERetransmissionsReceived           int64 `json:"ice_retransmissions_received,omitempty"`
	ICEConsentRequestsSent               int64 `json:"ice_consent_requests_sent,omitempty"`
	ICEWriteRTTMS                        int64 `json:"ice_rtt_ms,omitempty"`
	ICECheckMS                           int64 `json:"ice_check_ms,omitempty"`
	ICEConnectedSinceSessionStartMS      int64 `json:"ice_connected_since_session_start_ms,omitempty"`
	DeviceICEWaitMS                      int64 `json:"device_ice_wait_ms,omitempty"`
	ViewerPeerConnectionConnectedMS      int64 `json:"viewer_peer_connection_connected_ms,omitempty"`
	ViewerPeerConnectedAfterICEMS        int64 `json:"viewer_peer_connected_after_ice_ms,omitempty"`
	SenderPeerConnectionConnectedMS      int64 `json:"sender_peer_connection_connected_ms,omitempty"`
	SenderPeerConnectedAfterICEMS        int64 `json:"sender_peer_connected_after_ice_ms,omitempty"`
	SenderQueueWaitMS                    int64 `json:"sender_queue_wait_ms,omitempty"`
	SenderWriteAttempts                  int64 `json:"sender_write_attempts,omitempty"`
	SenderWriteReturns                   int64 `json:"sender_write_returns,omitempty"`
	SenderWriteErrors                    int64 `json:"sender_write_errors,omitempty"`
	SenderFirstWriteCallMS               int64 `json:"sender_first_write_call_ms,omitempty"`
	SenderFirstWriteReturnMS             int64 `json:"sender_first_write_return_ms,omitempty"`
	SenderWriteMaxMS                     int64 `json:"sender_write_max_ms,omitempty"`
	SenderFirstWriteAfterICEMS           int64 `json:"sender_first_write_after_ice_ms,omitempty"`
	SenderFirstWriteAfterPeerMS          int64 `json:"sender_first_write_after_peer_ms,omitempty"`
	FirstRTPAfterICEMS                   int64 `json:"first_rtp_after_ice_ms,omitempty"`
	FirstH264AccessUnitAfterRTPMS        int64 `json:"first_h264_access_unit_after_rtp_ms,omitempty"`
	SenderFirstWriteSinceSessionMS       int64 `json:"sender_first_write_since_session_ms,omitempty"`
	SenderQueueFullDrops                 int   `json:"sender_queue_full_drops,omitempty"`
}

type VideoStartupSummary struct {
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

type VideoStartupLatencySample struct {
	RunID                                string `json:"run_id,omitempty"`
	SessionID                            string `json:"session_id,omitempty"`
	DeviceID                             string `json:"device_id,omitempty"`
	ViewerID                             string `json:"viewer_id,omitempty"`
	ICEPolicy                            string `json:"ice_policy,omitempty"`
	SelectedLocalCandidateType           string `json:"selected_local_candidate_type,omitempty"`
	SelectedRemoteCandidateType          string `json:"selected_remote_candidate_type,omitempty"`
	SelectedLocalCandidateProtocol       string `json:"selected_local_candidate_protocol,omitempty"`
	SelectedRemoteCandidateProtocol      string `json:"selected_remote_candidate_protocol,omitempty"`
	SelectedLocalCandidateAddress        string `json:"selected_local_candidate_address,omitempty"`
	SelectedRemoteCandidateAddress       string `json:"selected_remote_candidate_address,omitempty"`
	SelectedLocalCandidatePort           int    `json:"selected_local_candidate_port,omitempty"`
	SelectedRemoteCandidatePort          int    `json:"selected_remote_candidate_port,omitempty"`
	SelectedLocalCandidateTCPType        string `json:"selected_local_candidate_tcp_type,omitempty"`
	SelectedRemoteCandidateTCPType       string `json:"selected_remote_candidate_tcp_type,omitempty"`
	APICreateMS                          int64  `json:"api_create_ms"`
	OfferDeliveryMS                      int64  `json:"offer_delivery_ms"`
	AnswerQueueWaitMS                    int64  `json:"answer_queue_wait_ms,omitempty"`
	AnswerPrepareMS                      int64  `json:"answer_prepare_ms,omitempty"`
	AnswerPostMS                         int64  `json:"answer_post_ms,omitempty"`
	DeviceAnswerMS                       int64  `json:"device_answer_ms"`
	PionCreatePeerMS                     int64  `json:"pion_create_peer_ms,omitempty"`
	PionCreateOfferMS                    int64  `json:"pion_create_offer_ms,omitempty"`
	PionCreateAnswerMS                   int64  `json:"pion_create_answer_ms,omitempty"`
	PionSetLocalDescriptionMS            int64  `json:"pion_set_local_description_ms,omitempty"`
	PionICEGatheringWaitMS               int64  `json:"pion_ice_gathering_wait_ms,omitempty"`
	PionFirstLocalCandidateMS            int64  `json:"pion_first_local_candidate_ms,omitempty"`
	PionFirstLocalRelayCandidateMS       int64  `json:"pion_first_local_relay_candidate_ms,omitempty"`
	PionFirstLocalRelayUDPCandidateMS    int64  `json:"pion_first_local_relay_udp_candidate_ms,omitempty"`
	PionFirstLocalRelayTCPCandidateMS    int64  `json:"pion_first_local_relay_tcp_candidate_ms,omitempty"`
	PionRelayCandidateToGatherCompleteMS int64  `json:"pion_relay_candidate_to_gather_complete_ms,omitempty"`
	PionSetRemoteDescriptionMS           int64  `json:"pion_set_remote_description_ms,omitempty"`
	RemoteAnswerSetMS                    int64  `json:"remote_answer_set_ms,omitempty"`
	ICESelectedPairChanges               int64  `json:"ice_selected_pair_changes,omitempty"`
	ICESelectedPairFirstChangeMS         int64  `json:"ice_selected_pair_first_change_ms,omitempty"`
	ICESelectedPairLastChangeMS          int64  `json:"ice_selected_pair_last_change_ms,omitempty"`
	ICERequestsSent                      int64  `json:"ice_requests_sent,omitempty"`
	ICERequestsReceived                  int64  `json:"ice_requests_received,omitempty"`
	ICEResponsesSent                     int64  `json:"ice_responses_sent,omitempty"`
	ICEResponsesReceived                 int64  `json:"ice_responses_received,omitempty"`
	ICERetransmissionsSent               int64  `json:"ice_retransmissions_sent,omitempty"`
	ICERetransmissionsReceived           int64  `json:"ice_retransmissions_received,omitempty"`
	ICEConsentRequestsSent               int64  `json:"ice_consent_requests_sent,omitempty"`
	ICEWriteRTTMS                        int64  `json:"ice_rtt_ms,omitempty"`
	ICECheckMS                           int64  `json:"ice_check_ms,omitempty"`
	ICEConnectedSinceSessionStartMS      int64  `json:"ice_connected_since_session_start_ms,omitempty"`
	DeviceICEWaitMS                      int64  `json:"device_ice_wait_ms,omitempty"`
	ViewerPeerConnectionConnectedMS      int64  `json:"viewer_peer_connection_connected_ms,omitempty"`
	ViewerPeerConnectedAfterICEMS        int64  `json:"viewer_peer_connected_after_ice_ms,omitempty"`
	SenderPeerConnectionConnectedMS      int64  `json:"sender_peer_connection_connected_ms,omitempty"`
	SenderPeerConnectedAfterICEMS        int64  `json:"sender_peer_connected_after_ice_ms,omitempty"`
	SenderQueueWaitMS                    int64  `json:"sender_queue_wait_ms,omitempty"`
	SenderWriteAttempts                  int64  `json:"sender_write_attempts,omitempty"`
	SenderWriteReturns                   int64  `json:"sender_write_returns,omitempty"`
	SenderWriteErrors                    int64  `json:"sender_write_errors,omitempty"`
	SenderFirstWriteCallMS               int64  `json:"sender_first_write_call_ms,omitempty"`
	SenderFirstWriteReturnMS             int64  `json:"sender_first_write_return_ms,omitempty"`
	SenderWriteMaxMS                     int64  `json:"sender_write_max_ms,omitempty"`
	SenderFirstWriteAfterICEMS           int64  `json:"sender_first_write_after_ice_ms,omitempty"`
	SenderFirstWriteAfterPeerMS          int64  `json:"sender_first_write_after_peer_ms,omitempty"`
	FirstRTPAfterICEMS                   int64  `json:"first_rtp_after_ice_ms"`
	FirstH264AccessUnitAfterRTPMS        int64  `json:"first_h264_access_unit_after_rtp_ms"`
	ReceiverTrackArrivedMS               int64  `json:"receiver_track_arrived_ms,omitempty"`
	ReceiverTrackKind                    string `json:"receiver_track_kind,omitempty"`
	ReceiverTrackCodec                   string `json:"receiver_track_codec,omitempty"`
	ReceiverFirstRTPPayloadType          int    `json:"receiver_first_rtp_payload_type,omitempty"`
	ReceiverFirstRTPSequence             int    `json:"receiver_first_rtp_sequence,omitempty"`
	ReceiverFirstRTPTimestamp            int    `json:"receiver_first_rtp_timestamp,omitempty"`
	ReceiverFirstRTPSSRC                 int    `json:"receiver_first_rtp_ssrc,omitempty"`
	SenderFirstWriteSinceSessionMS       int64  `json:"sender_first_write_since_session_ms,omitempty"`
	SenderQueueFullDrops                 int    `json:"sender_queue_full_drops,omitempty"`
	SenderSchedulerQueueFullDrops        int    `json:"sender_scheduler_queue_full_drops,omitempty"`
	SenderSchedulerDroppedPackets        int    `json:"sender_scheduler_dropped_packets,omitempty"`
	SenderSchedulerPacketsSent           int    `json:"sender_scheduler_packets_sent,omitempty"`
	SenderSchedulerBytesSent             int    `json:"sender_scheduler_bytes_sent,omitempty"`
	AppRequestToFirstRTPMS               int64  `json:"app_request_to_first_rtp_ms"`
	AppRequestToFirstH264AccessUnitMS    int64  `json:"app_request_to_first_h264_access_unit_ms"`
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
