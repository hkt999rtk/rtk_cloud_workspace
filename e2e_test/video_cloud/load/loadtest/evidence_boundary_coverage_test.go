package loadtest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
)

func TestMediaEvidenceRoundTripAndTimingBoundaries(t *testing.T) {
	video := H264RTPEvidence{
		Packets:                         12,
		Bytes:                           2048,
		DurationMS:                      900,
		Loops:                           2,
		Frames:                          6,
		NALTypes:                        map[string]bool{"sps": true, "pps": true, "idr": true},
		Packetizations:                  map[string]bool{"single-nalu": true},
		SelectedLocalCandidateType:      "relay",
		SelectedRemoteCandidateType:     "relay",
		SelectedLocalCandidateProtocol:  "udp",
		SelectedRemoteCandidateProtocol: "udp",
		ExpectedSHA256:                  "video-sha",
		SchedulerPacketsSent:            12,
		SchedulerBytesSent:              2048,
	}.WithTimings(901, 21, 11)
	audio := OpusRTPEvidence{
		Packets:         20,
		Bytes:           960,
		DurationMS:      900,
		Loops:           2,
		Frames:          20,
		SampleRate:      48000,
		Channels:        2,
		ReceiverPackets: 20,
		ReceiverBytes:   960,
		ReceiverFrames:  20,
		FirstOpusRTPMS:  22,
	}.WithTimings(902, 22, 12)

	combined := (AVRTPEvidence{Video: video, Audio: audio}).WithTimings(903, 23, 13)
	parsed := parseAVSenderEvidence(avSenderEvidence(combined))
	if parsed.Video.Packets != 12 || parsed.Video.Bytes != 2048 || parsed.Video.TimeToFirstMS != 23 {
		t.Fatalf("video evidence did not round-trip: %+v", parsed.Video)
	}
	if parsed.Audio.Packets != 20 || parsed.Audio.SampleRate != 48000 || parsed.Audio.Channels != 2 {
		t.Fatalf("audio evidence did not round-trip: %+v", parsed.Audio)
	}

	if got := candidateTypeFromCandidateLine("candidate:1 1 UDP 1 192.0.2.10 3478 typ relay"); got != "relay" {
		t.Fatalf("candidate type = %q, want relay", got)
	}
	if got := candidateTypeFromCandidateLine("malformed"); got != "" {
		t.Fatalf("malformed candidate type = %q, want empty", got)
	}
	if !h264AccessUnitEvidenceReady(map[string]bool{"idr": true}) || h264AccessUnitEvidenceReady(nil) {
		t.Fatal("unexpected H.264 access-unit readiness")
	}

	annexB := []byte{0, 0, 0, 1, 0x67, 1, 2, 0, 0, 1, 0x68, 3, 0, 0, 1, 0x65, 4}
	if got := h264NALTypeNamesFromAnnexB(annexB); !reflect.DeepEqual(got, []string{"idr", "pps", "sps"}) {
		t.Fatalf("NAL types = %v", got)
	}
	if got := h264NALTypeNamesFromAnnexB([]byte{1, 2, 3}); got != nil {
		t.Fatalf("invalid Annex-B data produced NAL types: %v", got)
	}
}

func TestOfferClockMQTTAndClipCoverageBoundaries(t *testing.T) {
	offer, err := extractOfferPayload(map[string]any{
		"offer": map[string]any{"type": "offer", "sdp": "v=0"},
	})
	if err != nil || offer["sdp"] != "v=0" {
		t.Fatalf("extract offer: offer=%v err=%v", offer, err)
	}
	for _, response := range []map[string]any{
		{},
		{"offer": map[string]any{"type": "answer", "sdp": "v=0"}},
		{"offer": []any{"not", "an", "object"}},
	} {
		if _, err := extractOfferPayload(response); err == nil {
			t.Fatalf("expected invalid offer error for %#v", response)
		}
	}

	started := time.Unix(1700000000, 0)
	clock := videoStartupClock{
		RunID:               "run-1",
		SessionID:           "session-1",
		DeviceID:            "device-1",
		ViewerID:            "viewer-1",
		ICEPolicy:           "relay",
		AppRequestStartedAt: started,
		APICreateMS:         10,
		AppRequestOffsetMS:  5,
	}.withDeviceTimings(started.Add(30*time.Millisecond), started.Add(50*time.Millisecond))
	if clock.OfferDeliveryMS != 20 || clock.DeviceAnswerMS != 20 {
		t.Fatalf("device timings = offer %d answer %d", clock.OfferDeliveryMS, clock.DeviceAnswerMS)
	}
	evidence := clock.EvidenceForSyntheticSend("codec=h264", 25)
	for _, fragment := range []string{"codec=h264", "run_id=run-1", "session_id=session-1", "app_request_to_first_rtp_ms=30"} {
		if !strings.Contains(evidence, fragment) {
			t.Fatalf("synthetic evidence missing %q: %s", fragment, evidence)
		}
	}

	if got := mqttConnectErrorClass(nil); got != "" {
		t.Fatalf("nil MQTT error class = %q", got)
	}
	if got := mqttConnectErrorClass(errors.New("JWT metadata missing")); got != ClassAuth {
		t.Fatalf("auth MQTT error class = %q", got)
	}
	if got := mqttConnectErrorClass(errors.New("connection refused")); got != ClassNetwork {
		t.Fatalf("network MQTT error class = %q", got)
	}

	names := []string{
		"clip_authorize", "clip_put", "thumbnail_put", "clip_complete", "clip_verify_ready", "clip_upload",
		"clip_total", "clip_enum", "clip_info", "clip_download_range", "clip_download_decrypt",
		"clip_playback_session", "clip_playback_range", "clip_thumbnail_download", "clip_delete",
	}
	operations := make([]Operation, 0, len(names))
	for _, name := range names {
		operations = append(operations, Operation{Name: name, Success: true})
	}
	item := coverageForClipStorage(operations)
	if item.Status != CoverageStatusPass || len(item.Operations) != len(names) {
		t.Fatalf("clip coverage = %+v", item)
	}
	operations[0].Success = false
	if item := coverageForClipStorage(operations); item.Status != CoverageStatusFail {
		t.Fatalf("failed clip coverage = %+v", item)
	}
}

func TestPacketizerSessionAndRepositoryBoundaries(t *testing.T) {
	sample := []byte{0, 0, 0, 1, 0x67, 1, 2, 0, 0, 1, 0x68, 3, 0, 0, 1, 0x65, 4, 5}
	packetizer := rtp.NewPacketizer(1200, 96, 1, &codecs.H264Payloader{}, rtp.NewFixedSequencer(1), 90000)
	packets, evidence, err := packetizeH264AnnexBWithPacketizer(sample, packetizer, 3000)
	if err != nil {
		t.Fatalf("packetize H.264: %v", err)
	}
	if len(packets) == 0 || evidence.Packets != len(packets) || !evidence.NALTypes["sps"] || !evidence.NALTypes["idr"] {
		t.Fatalf("packet evidence = %+v, packets=%d", evidence, len(packets))
	}
	if _, _, err := packetizeH264AnnexBWithPacketizer([]byte{1, 2}, packetizer, 3000); err == nil {
		t.Fatal("invalid Annex-B sample unexpectedly packetized")
	}

	var offer *PionOfferSession
	var mediaOffer *PionMediaOfferSession
	var answer *PionMediaAnswerSession
	if offer.ICETransportPolicy() != webrtc.ICETransportPolicyAll ||
		mediaOffer.ICETransportPolicy() != webrtc.ICETransportPolicyAll ||
		answer.ICETransportPolicy() != webrtc.ICETransportPolicyAll {
		t.Fatal("nil sessions must default to the all ICE policy")
	}
	if local, remote := answer.SelectedCandidatePairTypes(); local != "" || remote != "" {
		t.Fatalf("nil answer selected pair = %q/%q", local, remote)
	}
	if local, remote := selectedCandidatePairTypesFromDTLS(nil); local != "" || remote != "" {
		t.Fatalf("nil DTLS selected pair = %q/%q", local, remote)
	}
	if got := selectedCandidatePairStatsTrace(nil); got != (selectedCandidatePairStatsEvidence{}) {
		t.Fatalf("nil peer stats = %+v", got)
	}

	runner := &Runner{}
	ops, completedAt, cleanup := runner.prepareWebRTCMediaAnswerWithRecorder(
		context.Background(),
		Config{RunID: "run-1", WebRTCICEPolicy: WebRTCICEPolicyRelay},
		"device-1",
		webRTCMediaOfferMessage{SessionID: "session-1"},
		nil,
	)
	defer cleanup()
	if len(ops) != 1 || ops[0].Success || ops[0].ErrorClass != ClassWebRTCSetup || !completedAt.IsZero() {
		t.Fatalf("invalid answer preparation = ops=%+v completed=%v", ops, completedAt)
	}

	repo := t.TempDir()
	contracts := filepath.Join(repo, "docs", "rtk_cloud_contracts_doc")
	if err := os.MkdirAll(contracts, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "coverage@example.invalid"},
		{"config", "user.name", "Coverage Test"},
		{"commit", "--allow-empty", "-qm", "fixture"},
	} {
		cmd := exec.Command("git", append([]string{"-C", contracts}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if got := ResolveContractsCommit(repo); len(got) != 40 {
		t.Fatalf("contracts commit = %q", got)
	}
	if got := ResolveContractsCommit(filepath.Join(repo, "missing")); got != "" {
		t.Fatalf("missing contracts commit = %q", got)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	gotRoot := findRepoRoot()
	resolvedRoot, _ := filepath.EvalSymlinks(repo)
	if gotRoot != resolvedRoot {
		t.Fatalf("repo root = %q, want %q", gotRoot, resolvedRoot)
	}
	if got := ResolveContractsCommit(""); len(got) != 40 {
		t.Fatalf("auto-resolved contracts commit = %q", got)
	}
}

func TestResultClassificationAndReceiverEvidenceBoundaries(t *testing.T) {
	failureCases := map[string]string{
		"ICE connection timeout":  "wait_ice_connected",
		"peer connection timeout": "wait_peer_connected",
		"media scheduler stopped": "scheduler",
		"WriteRTP failed":         "write_rtp",
		"context deadline":        "context",
		"other failure":           "unknown",
	}
	if got := senderFailurePhaseEvidence(nil); got != "" {
		t.Fatalf("nil sender failure evidence = %q", got)
	}
	for detail, phase := range failureCases {
		if got := senderFailurePhaseEvidence(errors.New(detail)); !strings.Contains(got, "sender_failure_phase="+phase) {
			t.Fatalf("sender failure %q = %q", detail, got)
		}
	}

	op := expectedHTTPFailure(Operation{StatusCode: 401, ErrorClass: ClassHTTP}, "unauthorized", 401, 403)
	if !op.Success || op.ErrorClass != "" || !strings.Contains(op.Evidence, "expected_failure") {
		t.Fatalf("expected HTTP failure = %+v", op)
	}
	if op := expectedHTTPFailure(Operation{Success: true}, "unauthorized", 401); op.Success {
		t.Fatalf("successful negative probe remained successful: %+v", op)
	}
	if op := expectedHTTPFailure(Operation{StatusCode: 500, ErrorDetail: "boom"}, "unauthorized", 401); op.Success || !strings.Contains(op.ErrorDetail, "unexpected status=500") {
		t.Fatalf("unexpected negative status = %+v", op)
	}

	if op := expectedTimeout(Operation{ErrorClass: ClassTimeout}); !op.Success || op.ErrorClass != "" {
		t.Fatalf("expected timeout = %+v", op)
	}
	if op := expectedTimeout(Operation{Success: true}); op.Success || op.ErrorClass != ClassTimeout {
		t.Fatalf("unexpected timeout success = %+v", op)
	}
	if op := expectedTimeout(Operation{StatusCode: 503, ErrorClass: ClassHTTP}); op.Success || !strings.Contains(op.ErrorDetail, "class=http") {
		t.Fatalf("unexpected timeout class = %+v", op)
	}

	statsJSON := `{"media":{"packets_received":3,"bytes_received":300,"h264_sha256":"abc","h264_bytes":200,"nal_types":["sps","idr"],"first_h264_access_unit_ms":15,"first_opus_rtp_ms":16,"opus_bytes":100,"opus_packets":2,"opus_frames":2}}`
	stats, err := parseCloseMediaStats(statsJSON)
	if err != nil || stats.H264Packets != 3 || stats.H264Bytes != 200 || stats.OpusPackets != 2 {
		t.Fatalf("receiver stats = %+v err=%v", stats, err)
	}
	for _, invalid := range []string{
		`not-json`,
		`{"media":{"packets_received":1}}`,
		`{"media":{"h264_bytes":1}}`,
	} {
		if _, err := parseCloseMediaStats(invalid); err == nil {
			t.Fatalf("invalid receiver stats accepted: %s", invalid)
		}
	}

	answer, err := extractAnswerPayload(map[string]any{"answer": map[string]any{"type": "answer", "sdp": "v=0"}})
	if err != nil || answer["type"] != "answer" {
		t.Fatalf("answer payload = %v err=%v", answer, err)
	}
	for _, invalid := range []map[string]any{
		{},
		{"answer": []string{"invalid"}},
		{"answer": map[string]any{"type": "offer", "sdp": "v=0"}},
	} {
		if _, err := extractAnswerPayload(invalid); err == nil {
			t.Fatalf("invalid answer accepted: %#v", invalid)
		}
	}

	responseSummary := summarizeWebRTCResponseEvidence(map[string]any{
		"mode":       "relay",
		"session_id": "session-1",
		"offer":      map[string]any{"type": "offer", "sdp": "v=0\na=candidate:1 1 UDP 1 192.0.2.10 3478 typ relay"},
		"ice_servers": []map[string]any{{
			"urls":       []string{"turn:turn.example.test:3478"},
			"username":   "fixture",
			"credential": "fixture",
		}},
	})
	for _, fragment := range []string{"mode=relay", "session_id=session-1", "ice_servers=1", "candidate_types=relay"} {
		if !strings.Contains(responseSummary, fragment) {
			t.Fatalf("response summary missing %q: %s", fragment, responseSummary)
		}
	}
}

func TestICEGatheringReadinessBoundaries(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	if err := waitICEGatheringComplete(context.Background(), closed, time.Second); err != nil {
		t.Fatalf("completed gathering: %v", err)
	}
	if err := waitICEGatheringReady(context.Background(), make(chan struct{}), closed, webrtc.ICETransportPolicyRelay, time.Second); err != nil {
		t.Fatalf("relay candidate ready: %v", err)
	}
	if err := waitICEGatheringReady(context.Background(), closed, make(chan struct{}), webrtc.ICETransportPolicyRelay, time.Second); err != nil {
		t.Fatalf("relay gathering completed: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitICEGatheringReady(cancelled, make(chan struct{}), nil, webrtc.ICETransportPolicyAll, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled gathering = %v", err)
	}
	if err := waitICEGatheringReady(context.Background(), make(chan struct{}), nil, webrtc.ICETransportPolicyAll, time.Millisecond); err == nil || !strings.Contains(err.Error(), "gathering timeout") {
		t.Fatalf("gathering timeout = %v", err)
	}
	if err := waitICEGatheringReady(context.Background(), make(chan struct{}), make(chan struct{}), webrtc.ICETransportPolicyRelay, time.Millisecond); err == nil || !strings.Contains(err.Error(), "relay candidate timeout") {
		t.Fatalf("relay timeout = %v", err)
	}
}

func TestWebRTCPayloadDecodingBoundaries(t *testing.T) {
	answer, ok, err := extractAnswer(map[string]any{"answer": map[string]any{"type": "answer", "sdp": "v=0"}})
	if err != nil || !ok || answer.Type != webrtc.SDPTypeAnswer {
		t.Fatalf("answer = %+v ok=%t err=%v", answer, ok, err)
	}
	for _, response := range []map[string]any{
		{"answer": make(chan int)},
		{"answer": "not-json-object"},
		{"answer": map[string]any{"type": "offer", "sdp": "v=0"}},
		{"answer": map[string]any{"type": "answer"}},
	} {
		if _, _, err := extractAnswer(response); err == nil {
			t.Fatalf("invalid answer accepted: %#v", response)
		}
	}
	if _, ok, err := extractAnswer(nil); err != nil || ok {
		t.Fatalf("missing answer = ok=%t err=%v", ok, err)
	}

	validURLCases := []struct {
		raw  any
		want int
	}{
		{"stun:stun.example.test:3478", 1},
		{[]any{"turn:turn.example.test:3478", "turns:turn.example.test:5349"}, 2},
	}
	for _, tc := range validURLCases {
		if got, err := normalizeURLs(tc.raw); err != nil || len(got) != tc.want {
			t.Fatalf("normalize URLs %#v = %v err=%v", tc.raw, got, err)
		}
	}
	for _, raw := range []any{"", []any{}, []any{1}, []any{""}, 7} {
		if _, err := normalizeURLs(raw); err == nil {
			t.Fatalf("invalid URLs accepted: %#v", raw)
		}
	}

	for _, response := range []map[string]any{
		{},
		{"offer": make(chan int)},
		{"offer": "not-json-object"},
		{"offer": map[string]any{"type": "answer", "sdp": "v=0"}},
		{"offer": map[string]any{"type": "offer"}},
	} {
		if err := validateServerOffer(response); err == nil {
			t.Fatalf("invalid server offer accepted: %#v", response)
		}
	}
	if err := validateServerOffer(map[string]any{"offer": map[string]any{"type": "offer", "sdp": "v=0"}}); err != nil {
		t.Fatalf("valid server offer: %v", err)
	}
}

func TestEnvironmentAndTokenParsingBoundaries(t *testing.T) {
	if got := parseTokenMap(""); got != nil {
		t.Fatalf("empty token map = %#v", got)
	}
	if got := parseTokenMap("{not-json"); got != nil {
		t.Fatalf("invalid token map = %#v", got)
	}
	if got := parseTokenMap(`{"device-1":"token-1"}`); got["device-1"] != "token-1" {
		t.Fatalf("token map = %#v", got)
	}

	const durationKey = "RTK_COVERAGE_TEST_DURATION"
	for value, want := range map[string]time.Duration{
		"":      7 * time.Second,
		"bad":   7 * time.Second,
		"-1s":   7 * time.Second,
		"250ms": 250 * time.Millisecond,
	} {
		t.Setenv(durationKey, value)
		if got := envDuration(durationKey, 7*time.Second); got != want {
			t.Fatalf("envDuration(%q) = %v, want %v", value, got, want)
		}
	}

	const intKey = "RTK_COVERAGE_TEST_INT"
	for value, want := range map[string]int{"": 7, "bad": 7, "-1": 7, "0": 0, "9": 9} {
		t.Setenv(intKey, value)
		if got := envInt(intKey, 7); got != want {
			t.Fatalf("envInt(%q) = %d, want %d", value, got, want)
		}
	}
}
