package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
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
	if offer["type"] != "offer" || offer["sdp"] == "" {
		return nil, errors.New("invalid media offer")
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
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
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "rtk-video-loadtest")
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer track: %w", err)
	}
	if _, err := peer.AddTrack(track); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer add track: %w", err)
	}
	session.track = track
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
