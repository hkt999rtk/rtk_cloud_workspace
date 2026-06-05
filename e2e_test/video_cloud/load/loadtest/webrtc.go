package loadtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/h264reader"
)

type WebRTCValidation struct {
	ICEServerCount int
}

type PionOfferSession struct {
	peer  *webrtc.PeerConnection
	offer webrtc.SessionDescription
}

type WebRTCMediaStats struct {
	ICEConnectedLatencyMS int64
	TimeToFirstRTPMS      int64
	PacketsReceived       int
	BytesReceived         int
	ReceiveDurationMS     int64
}

type H264RTPPlan struct {
	Duration  time.Duration
	Loops     int
	FrameRate int
	Frames    int
	Packets   []*rtp.Packet
	Evidence  H264RTPEvidence
}

type H264RTPEvidence struct {
	Packets        int
	Bytes          int
	DurationMS     int64
	Loops          int
	Frames         int
	NALTypes       map[string]bool
	Packetizations map[string]bool
	ReceiveMS      int64
	TimeToFirstMS  int64
	ICEMS          int64
}

type PionMediaOfferSession struct {
	peer         *webrtc.PeerConnection
	offer        webrtc.SessionDescription
	started      time.Time
	iceConnected chan struct{}
	firstRTP     chan struct{}
	packetCh     chan struct{}
	closeOnce    sync.Once
	iceOnce      sync.Once
	firstOnce    sync.Once
	mu           sync.Mutex
	stats        WebRTCMediaStats
}

type PionMediaAnswerSession struct {
	peer         *webrtc.PeerConnection
	track        *webrtc.TrackLocalStaticRTP
	codecMime    string
	answer       webrtc.SessionDescription
	iceConnected chan struct{}
	closeOnce    sync.Once
	iceOnce      sync.Once
}

func NewPionOfferSession() (*PionOfferSession, error) {
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("pion peer connection: %w", err)
	}
	if _, err := peer.CreateDataChannel("rtk-video-loadtest", nil); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion data channel: %w", err)
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion create offer: %w", err)
	}
	if err := peer.SetLocalDescription(offer); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion set local description: %w", err)
	}
	return &PionOfferSession{peer: peer, offer: offer}, nil
}

func NewPionMediaOfferSession(ctx context.Context, gatherTimeout time.Duration) (*PionMediaOfferSession, error) {
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, fmt.Errorf("pion media offer peer connection: %w", err)
	}
	session := &PionMediaOfferSession{
		peer:         peer,
		started:      time.Now(),
		iceConnected: make(chan struct{}),
		firstRTP:     make(chan struct{}),
		packetCh:     make(chan struct{}, 32),
	}
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			session.iceOnce.Do(func() {
				session.mu.Lock()
				session.stats.ICEConnectedLatencyMS = time.Since(session.started).Milliseconds()
				session.mu.Unlock()
				close(session.iceConnected)
			})
		}
	})
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go session.readRemoteRTP(track)
	})
	if _, err := peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media recvonly transceiver: %w", err)
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media create offer: %w", err)
	}
	if err := peer.SetLocalDescription(offer); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media set local offer: %w", err)
	}
	if err := waitICEGatheringComplete(ctx, peer, gatherTimeout); err != nil {
		_ = peer.Close()
		return nil, err
	}
	session.offer = *peer.LocalDescription()
	return session, nil
}

func (s *PionMediaOfferSession) readRemoteRTP(track *webrtc.TrackRemote) {
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.stats.PacketsReceived++
		s.stats.BytesReceived += len(packet.Payload)
		if s.stats.TimeToFirstRTPMS == 0 {
			s.stats.TimeToFirstRTPMS = time.Since(s.started).Milliseconds()
		}
		s.stats.ReceiveDurationMS = time.Since(s.started).Milliseconds()
		s.mu.Unlock()
		s.firstOnce.Do(func() { close(s.firstRTP) })
		select {
		case s.packetCh <- struct{}{}:
		default:
		}
	}
}

func (s *PionMediaOfferSession) OfferPayload() map[string]string {
	return map[string]string{
		"type": "offer",
		"sdp":  s.offer.SDP,
	}
}

func (s *PionMediaOfferSession) SetRemoteAnswer(answer map[string]string) error {
	if answer["type"] != "answer" || answer["sdp"] == "" {
		return errors.New("invalid media answer")
	}
	return s.peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer["sdp"]})
}

func (s *PionMediaOfferSession) WaitForICEConnected(ctx context.Context, timeout time.Duration) (WebRTCMediaStats, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.iceConnected:
		return s.Snapshot(), nil
	case <-ctx.Done():
		return s.Snapshot(), ctx.Err()
	case <-timer.C:
		return s.Snapshot(), errors.New("webrtc media ICE connection timeout")
	}
}

func (s *PionMediaOfferSession) WaitForMedia(ctx context.Context, minPackets int, timeout time.Duration) (WebRTCMediaStats, error) {
	if minPackets <= 0 {
		minPackets = 1
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		stats := s.Snapshot()
		if stats.PacketsReceived >= minPackets {
			return stats, nil
		}
		select {
		case <-s.packetCh:
		case <-ctx.Done():
			return s.Snapshot(), ctx.Err()
		case <-timer.C:
			stats = s.Snapshot()
			if stats.PacketsReceived == 0 {
				return stats, errors.New("webrtc media no RTP received")
			}
			return stats, errors.New("webrtc media receive timeout")
		}
	}
}

func (s *PionMediaOfferSession) Snapshot() WebRTCMediaStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *PionMediaOfferSession) Close() {
	if s != nil && s.peer != nil {
		s.closeOnce.Do(func() { _ = s.peer.Close() })
	}
}

func NewPionMediaAnswerSession(ctx context.Context, offer map[string]string, gatherTimeout time.Duration) (*PionMediaAnswerSession, error) {
	return NewPionMediaAnswerSessionWithICEServers(ctx, offer, nil, gatherTimeout)
}

func NewPionMediaAnswerSessionWithICEServers(ctx context.Context, offer map[string]string, iceServers []webrtc.ICEServer, gatherTimeout time.Duration) (*PionMediaAnswerSession, error) {
	if offer["type"] != "offer" || offer["sdp"] == "" {
		return nil, errors.New("invalid media offer")
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return nil, fmt.Errorf("pion media answer peer connection: %w", err)
	}
	session := &PionMediaAnswerSession{
		peer:         peer,
		iceConnected: make(chan struct{}),
	}
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			session.iceOnce.Do(func() { close(session.iceConnected) })
		}
	})
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	}, "video", "rtk-video-loadtest")
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer track: %w", err)
	}
	if _, err := peer.AddTrack(track); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer add track: %w", err)
	}
	session.track = track
	session.codecMime = webrtc.MimeTypeH264
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer["sdp"]}); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer set remote offer: %w", err)
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media create answer: %w", err)
	}
	if err := peer.SetLocalDescription(answer); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media set local answer: %w", err)
	}
	if err := waitICEGatheringComplete(ctx, peer, gatherTimeout); err != nil {
		_ = peer.Close()
		return nil, err
	}
	session.answer = *peer.LocalDescription()
	return session, nil
}

func (s *PionMediaAnswerSession) AnswerPayload() map[string]string {
	return map[string]string{
		"type": "answer",
		"sdp":  s.answer.SDP,
	}
}

func (s *PionMediaAnswerSession) CodecMimeType() string {
	if s == nil {
		return ""
	}
	return s.codecMime
}

func (s *PionMediaAnswerSession) SendSyntheticRTP(ctx context.Context, packets int, interval time.Duration) error {
	if packets <= 0 {
		packets = 1
	}
	if interval <= 0 {
		interval = 20 * time.Millisecond
	}
	select {
	case <-s.iceConnected:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("webrtc media answerer ICE connection timeout")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for i := 0; i < packets; i++ {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i + 1),
				Timestamp:      uint32(3000 * (i + 1)),
				SSRC:           0x52544b43,
				Marker:         true,
			},
			Payload: []byte{0x90, 0x90, byte(i), 0x00, 0x01},
		}
		if err := s.track.WriteRTP(packet); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func (s *PionMediaAnswerSession) SendH264RTP(ctx context.Context, duration time.Duration) (H264RTPPlan, error) {
	select {
	case <-s.iceConnected:
	case <-ctx.Done():
		return H264RTPPlan{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return H264RTPPlan{}, errors.New("webrtc media answerer ICE connection timeout")
	}
	plan, err := buildH264MediaPlan(duration)
	if err != nil {
		return H264RTPPlan{}, err
	}
	interval := time.Duration(0)
	if len(plan.Packets) > 0 && duration > 0 {
		interval = duration / time.Duration(len(plan.Packets))
	}
	for i, packet := range plan.Packets {
		if err := s.track.WriteRTP(packet); err != nil {
			return H264RTPPlan{}, err
		}
		if interval <= 0 || i == len(plan.Packets)-1 {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return H264RTPPlan{}, ctx.Err()
		case <-timer.C:
		}
	}
	return plan, nil
}

func buildH264MediaPlan(duration time.Duration) (H264RTPPlan, error) {
	if duration <= 0 {
		duration = 20 * time.Second
	}
	sample, err := h264AnnexBSample(context.Background())
	if err != nil {
		return H264RTPPlan{}, err
	}
	const (
		fixtureDuration = 2 * time.Second
		frameRate       = 30
	)
	loops := int((duration + fixtureDuration - 1) / fixtureDuration)
	if loops < 1 {
		loops = 1
	}
	packetizer := rtp.NewPacketizer(1200, 96, 0x52544b43, &codecs.H264Payloader{}, rtp.NewFixedSequencer(1), 90000)
	allPackets := make([]*rtp.Packet, 0)
	evidence := H264RTPEvidence{
		DurationMS:     duration.Milliseconds(),
		Loops:          loops,
		Frames:         int(duration.Seconds() * frameRate),
		NALTypes:       map[string]bool{},
		Packetizations: map[string]bool{},
	}
	for i := 0; i < loops; i++ {
		packets, loopEvidence, err := packetizeH264AnnexBWithPacketizer(sample, packetizer, 3000)
		if err != nil {
			return H264RTPPlan{}, err
		}
		allPackets = append(allPackets, packets...)
		evidence.Packets += loopEvidence.Packets
		evidence.Bytes += loopEvidence.Bytes
		for name := range loopEvidence.NALTypes {
			evidence.NALTypes[name] = true
		}
		for name := range loopEvidence.Packetizations {
			evidence.Packetizations[name] = true
		}
	}
	return H264RTPPlan{Duration: duration, Loops: loops, FrameRate: frameRate, Frames: evidence.Frames, Packets: allPackets, Evidence: evidence}, nil
}

func h264AnnexBSample(_ context.Context) ([]byte, error) {
	for _, path := range []string{
		"../testdata/testsrc2_1080p_2s.h264",
		"testdata/testsrc2_1080p_2s.h264",
		"video_cloud/load/testdata/testsrc2_1080p_2s.h264",
		"e2e_test/video_cloud/load/testdata/testsrc2_1080p_2s.h264",
	} {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
	}
	return nil, errors.New("missing H.264 fixture testsrc2_1080p_2s.h264")
}

func packetizeH264AnnexBForRTP(sample []byte, mtu uint16) ([]*rtp.Packet, H264RTPEvidence, error) {
	packetizer := rtp.NewPacketizer(mtu, 96, 0x52544b43, &codecs.H264Payloader{}, rtp.NewFixedSequencer(1), 90000)
	return packetizeH264AnnexBWithPacketizer(sample, packetizer, 3000)
}

func packetizeH264AnnexBWithPacketizer(sample []byte, packetizer rtp.Packetizer, samples uint32) ([]*rtp.Packet, H264RTPEvidence, error) {
	nals, err := h264NALUnits(sample)
	if err != nil {
		return nil, H264RTPEvidence{}, err
	}
	evidence := H264RTPEvidence{NALTypes: map[string]bool{}, Packetizations: map[string]bool{}}
	packets := make([]*rtp.Packet, 0)
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		nalType := h264NALTypeName(nal[0] & 0x1f)
		if nalType != "" {
			evidence.NALTypes[nalType] = true
		}
		rtpPackets := packetizer.Packetize(nal, samples)
		for _, packet := range rtpPackets {
			evidence.Packets++
			evidence.Bytes += len(packet.Payload)
			for _, packetization := range h264PayloadPacketizations(packet.Payload) {
				evidence.Packetizations[packetization] = true
			}
			packets = append(packets, packet)
		}
	}
	if evidence.Packets == 0 {
		return nil, H264RTPEvidence{}, errors.New("H.264 fixture produced no RTP packets")
	}
	return packets, evidence, nil
}

func h264NALUnits(sample []byte) ([][]byte, error) {
	reader, err := h264reader.NewReader(bytes.NewReader(sample))
	if err != nil {
		return nil, err
	}
	nals := make([][]byte, 0)
	for {
		nal, err := reader.NextNAL()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		nals = append(nals, nal.Data)
	}
	if len(nals) == 0 {
		return nil, errors.New("H.264 fixture has no NAL units")
	}
	return nals, nil
}

func validateH264RTPPayloads(packets []*rtp.Packet) error {
	for _, packet := range packets {
		if len(packet.Payload) == 0 {
			return errors.New("empty H.264 RTP payload")
		}
		packetizations := h264PayloadPacketizations(packet.Payload)
		if len(packetizations) == 0 {
			return fmt.Errorf("unsupported H.264 RTP payload type %d", packet.Payload[0]&0x1f)
		}
	}
	return nil
}

func validateH264RTPSequence(packets []*rtp.Packet) error {
	if len(packets) == 0 {
		return errors.New("no RTP packets")
	}
	for i := 1; i < len(packets); i++ {
		if packets[i].SequenceNumber != packets[i-1].SequenceNumber+1 {
			return fmt.Errorf("RTP sequence discontinuity at %d: %d after %d", i, packets[i].SequenceNumber, packets[i-1].SequenceNumber)
		}
		if packets[i].Timestamp < packets[i-1].Timestamp {
			return fmt.Errorf("RTP timestamp moved backwards at %d: %d after %d", i, packets[i].Timestamp, packets[i-1].Timestamp)
		}
	}
	return nil
}

func h264PayloadPacketizations(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	switch payload[0] & 0x1f {
	case 1, 5, 6, 7, 8, 9:
		return []string{"single-nal"}
	case 24:
		return []string{"stap-a"}
	case 28:
		if len(payload) < 2 {
			return nil
		}
		return []string{"fu-a", h264NALTypeName(payload[1] & 0x1f)}
	default:
		return nil
	}
}

func h264NALTypeName(nalType byte) string {
	switch nalType {
	case 1:
		return "non-idr"
	case 5:
		return "idr"
	case 6:
		return "sei"
	case 7:
		return "sps"
	case 8:
		return "pps"
	case 9:
		return "aud"
	default:
		return ""
	}
}

func (e H264RTPEvidence) HasNALType(name string) bool {
	return e.NALTypes[name]
}

func (e H264RTPEvidence) WithTimings(receiveMS, timeToFirstMS, iceMS int64) H264RTPEvidence {
	e.ReceiveMS = receiveMS
	e.TimeToFirstMS = timeToFirstMS
	e.ICEMS = iceMS
	return e
}

func (e H264RTPEvidence) String() string {
	return fmt.Sprintf("codec=h264 packets=%d bytes=%d duration_ms=%d loops=%d frames=%d nal_types=%s packetization=%s receive_ms=%d ttfb_ms=%d ice_ms=%d",
		e.Packets, e.Bytes, e.DurationMS, e.Loops, e.Frames, joinEvidenceKeys(e.NALTypes), joinEvidenceKeys(e.Packetizations), e.ReceiveMS, e.TimeToFirstMS, e.ICEMS)
}

func joinEvidenceKeys(values map[string]bool) string {
	keys := make([]string, 0, len(values))
	for key, ok := range values {
		if ok && key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func (s *PionMediaAnswerSession) Close() {
	if s != nil && s.peer != nil {
		s.closeOnce.Do(func() { _ = s.peer.Close() })
	}
}

func waitICEGatheringComplete(ctx context.Context, peer *webrtc.PeerConnection, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	gatherComplete := webrtc.GatheringCompletePromise(peer)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-gatherComplete:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("webrtc ICE gathering timeout")
	}
}

func (s *PionOfferSession) OfferPayload() map[string]string {
	return map[string]string{
		"type": "offer",
		"sdp":  s.offer.SDP,
	}
}

func (s *PionOfferSession) ValidateAnswer(response map[string]any) (WebRTCValidation, error) {
	iceServers, err := extractICEServers(response)
	if err != nil {
		return WebRTCValidation{}, err
	}
	answer, ok, err := extractAnswer(response)
	if err != nil {
		return WebRTCValidation{}, err
	}
	if ok {
		if err := s.peer.SetRemoteDescription(answer); err != nil {
			return WebRTCValidation{}, fmt.Errorf("pion set remote answer: %w", err)
		}
		return WebRTCValidation{ICEServerCount: len(iceServers)}, nil
	}
	if err := validateServerOffer(response); err != nil {
		return WebRTCValidation{}, err
	}
	return WebRTCValidation{ICEServerCount: len(iceServers)}, nil
}

func (s *PionOfferSession) Close() {
	if s != nil && s.peer != nil {
		_ = s.peer.Close()
	}
}

func NewPionOffer() (map[string]string, func(), error) {
	session, err := NewPionOfferSession()
	if err != nil {
		return nil, func() {}, err
	}
	return session.OfferPayload(), session.Close, nil
}

func ValidateWebRTCSetup(response map[string]any) (WebRTCValidation, error) {
	iceServers, err := extractICEServers(response)
	if err != nil {
		return WebRTCValidation{}, err
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return WebRTCValidation{}, fmt.Errorf("pion peer connection: %w", err)
	}
	answer, ok, err := extractAnswer(response)
	if err != nil {
		_ = peer.Close()
		return WebRTCValidation{}, err
	}
	if ok {
		if answer.SDP == "" {
			_ = peer.Close()
			return WebRTCValidation{}, errors.New("empty answer sdp")
		}
	} else if err := validateServerOffer(response); err != nil {
		_ = peer.Close()
		return WebRTCValidation{}, err
	}
	if err := peer.Close(); err != nil {
		return WebRTCValidation{}, fmt.Errorf("pion peer close: %w", err)
	}
	return WebRTCValidation{ICEServerCount: len(iceServers)}, nil
}

func extractAnswer(response map[string]any) (webrtc.SessionDescription, bool, error) {
	raw, ok := response["answer"]
	if !ok {
		return webrtc.SessionDescription{}, false, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return webrtc.SessionDescription{}, false, err
	}
	var answer struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err := json.Unmarshal(b, &answer); err != nil {
		return webrtc.SessionDescription{}, false, fmt.Errorf("decode answer: %w", err)
	}
	if answer.Type != "answer" {
		return webrtc.SessionDescription{}, false, fmt.Errorf("unexpected answer type %q", answer.Type)
	}
	if answer.SDP == "" {
		return webrtc.SessionDescription{}, false, errors.New("empty answer sdp")
	}
	return webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}, true, nil
}

func validateServerOffer(response map[string]any) error {
	raw, ok := response["offer"]
	if !ok {
		return errors.New("missing answer or offer")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var offer struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err := json.Unmarshal(b, &offer); err != nil {
		return fmt.Errorf("decode offer: %w", err)
	}
	if offer.Type != "offer" {
		return fmt.Errorf("unexpected offer type %q", offer.Type)
	}
	if offer.SDP == "" {
		return errors.New("empty offer sdp")
	}
	return nil
}

func extractICEServers(response map[string]any) ([]webrtc.ICEServer, error) {
	raw, ok := response["ice_servers"]
	if !ok {
		return nil, errors.New("missing ice_servers")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var decoded []struct {
		URLs       any    `json:"urls"`
		Username   string `json:"username,omitempty"`
		Credential string `json:"credential,omitempty"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, fmt.Errorf("decode ice_servers: %w", err)
	}
	servers := make([]webrtc.ICEServer, 0, len(decoded))
	for _, server := range decoded {
		urls, err := normalizeURLs(server.URLs)
		if err != nil {
			return nil, err
		}
		servers = append(servers, webrtc.ICEServer{
			URLs:       urls,
			Username:   server.Username,
			Credential: server.Credential,
		})
	}
	return servers, nil
}

func normalizeURLs(raw any) ([]string, error) {
	switch value := raw.(type) {
	case string:
		if value == "" {
			return nil, errors.New("empty ice server url")
		}
		return []string{value}, nil
	case []any:
		urls := make([]string, 0, len(value))
		for _, item := range value {
			s, ok := item.(string)
			if !ok || s == "" {
				return nil, errors.New("invalid ice server url")
			}
			urls = append(urls, s)
		}
		if len(urls) == 0 {
			return nil, errors.New("empty ice server url list")
		}
		return urls, nil
	default:
		return nil, errors.New("invalid ice server urls field")
	}
}
