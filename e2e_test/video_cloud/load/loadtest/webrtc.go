package loadtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/h264reader"
)

const defaultWebRTCMediaSettle = time.Second

type WebRTCValidation struct {
	ICEServerCount int
}

type PionOfferSession struct {
	peer      *webrtc.PeerConnection
	offer     webrtc.SessionDescription
	icePolicy webrtc.ICETransportPolicy
}

type WebRTCMediaStats struct {
	ICEConnectedLatencyMS       int64
	ICEGatheringCompleteMS      int64
	RemoteDescriptionSetMS      int64
	LocalDescriptionSetMS       int64
	ICECheckingMS               int64
	FirstLocalCandidateMS       int64
	FirstLocalRelayCandidateMS  int64
	LocalHostCandidates         int
	LocalSrflxCandidates        int
	LocalRelayCandidates        int
	ICEConnectionStates         []string
	ICEGatheringStates          []string
	TimeToFirstRTPMS            int64
	FirstH264AccessUnitMS       int64
	PacketsReceived             int
	BytesReceived               int
	ReceiveDurationMS           int64
	SelectedLocalCandidateType  string
	SelectedRemoteCandidateType string
	H264SHA256                  string
	H264Bytes                   int
	H264Packets                 int
	NALTypes                    []string
	Packetizations              []string
	OpusSHA256                  string
	OpusBytes                   int
	OpusPackets                 int
	OpusFrames                  int
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
	Packets                     int
	Bytes                       int
	DurationMS                  int64
	Loops                       int
	Frames                      int
	NALTypes                    map[string]bool
	Packetizations              map[string]bool
	ReceiveMS                   int64
	TimeToFirstMS               int64
	ICEMS                       int64
	ICEGatheringCompleteMS      int64
	RemoteDescriptionSetMS      int64
	LocalDescriptionSetMS       int64
	ICECheckingMS               int64
	FirstLocalCandidateMS       int64
	FirstLocalRelayCandidateMS  int64
	LocalHostCandidates         int
	LocalSrflxCandidates        int
	LocalRelayCandidates        int
	ICEConnectionStates         []string
	ICEGatheringStates          []string
	SelectedLocalCandidateType  string
	SelectedRemoteCandidateType string
	ExpectedSHA256              string
	ReceiverSHA256              string
	ReceiverPackets             int
	ReceiverBytes               int
	ReceiverNALTypes            map[string]bool
	BitstreamMatch              bool
}

type OpusRTPPlan struct {
	Duration   time.Duration
	Loops      int
	SampleRate int
	Channels   int
	Frames     int
	Packets    []*rtp.Packet
	Evidence   OpusRTPEvidence
}

type OpusRTPEvidence struct {
	Packets                     int
	Bytes                       int
	DurationMS                  int64
	Loops                       int
	Frames                      int
	SampleRate                  int
	Channels                    int
	ReceiveMS                   int64
	TimeToFirstMS               int64
	ICEMS                       int64
	ICEGatheringCompleteMS      int64
	RemoteDescriptionSetMS      int64
	LocalDescriptionSetMS       int64
	ICECheckingMS               int64
	FirstLocalCandidateMS       int64
	FirstLocalRelayCandidateMS  int64
	LocalHostCandidates         int
	LocalSrflxCandidates        int
	LocalRelayCandidates        int
	ICEConnectionStates         []string
	ICEGatheringStates          []string
	SelectedLocalCandidateType  string
	SelectedRemoteCandidateType string
	ExpectedSHA256              string
	ReceiverSHA256              string
	ReceiverPackets             int
	ReceiverBytes               int
	ReceiverFrames              int
	PayloadMatch                bool
}

type AVRTPEvidence struct {
	Video H264RTPEvidence
	Audio OpusRTPEvidence
}

type PionMediaOfferSession struct {
	peer           *webrtc.PeerConnection
	offer          webrtc.SessionDescription
	icePolicy      webrtc.ICETransportPolicy
	started        time.Time
	iceConnected   chan struct{}
	firstRTP       chan struct{}
	packetCh       chan struct{}
	closeOnce      sync.Once
	iceOnce        sync.Once
	firstOnce      sync.Once
	mu             sync.Mutex
	stats          WebRTCMediaStats
	h264           codecs.H264Packet
	h264Bytes      bytes.Buffer
	opusBytes      bytes.Buffer
	nalTypes       map[string]bool
	packetizations map[string]bool
}

type PionMediaAnswerSession struct {
	peer         *webrtc.PeerConnection
	track        *webrtc.TrackLocalStaticRTP
	videoTrack   *webrtc.TrackLocalStaticRTP
	audioTrack   *webrtc.TrackLocalStaticRTP
	codecMime    string
	answer       webrtc.SessionDescription
	icePolicy    webrtc.ICETransportPolicy
	started      time.Time
	mu           sync.Mutex
	stats        WebRTCMediaStats
	iceConnected chan struct{}
	closeOnce    sync.Once
	iceOnce      sync.Once
}

func NewPionOfferSession() (*PionOfferSession, error) {
	return NewPionOfferSessionWithICEPolicy(WebRTCICEPolicyAll)
}

func NewPionOfferSessionWithICEPolicy(policy string) (*PionOfferSession, error) {
	icePolicy := pionICETransportPolicy(policy)
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICETransportPolicy: icePolicy})
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
	return &PionOfferSession{peer: peer, offer: offer, icePolicy: icePolicy}, nil
}

func NewPionMediaOfferSession(ctx context.Context, gatherTimeout time.Duration) (*PionMediaOfferSession, error) {
	return NewPionMediaOfferSessionForSet(ctx, WebRTCMediaSetH264, gatherTimeout)
}

func NewPionMediaOfferSessionForSet(ctx context.Context, mediaSet string, gatherTimeout time.Duration) (*PionMediaOfferSession, error) {
	return NewPionMediaOfferSessionForSetWithICEPolicy(ctx, mediaSet, WebRTCICEPolicyAll, gatherTimeout)
}

func NewPionMediaOfferSessionForSetWithICEPolicy(ctx context.Context, mediaSet, policy string, gatherTimeout time.Duration) (*PionMediaOfferSession, error) {
	return NewPionMediaOfferSessionForSetWithICEServersAndPolicy(ctx, mediaSet, nil, policy, gatherTimeout)
}

func NewPionMediaOfferSessionForSetWithICEServersAndPolicy(ctx context.Context, mediaSet string, iceServers []webrtc.ICEServer, policy string, gatherTimeout time.Duration) (*PionMediaOfferSession, error) {
	icePolicy := pionICETransportPolicy(policy)
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers, ICETransportPolicy: icePolicy})
	if err != nil {
		return nil, fmt.Errorf("pion media offer peer connection: %w", err)
	}
	session := &PionMediaOfferSession{
		peer:           peer,
		icePolicy:      icePolicy,
		started:        time.Now(),
		iceConnected:   make(chan struct{}),
		firstRTP:       make(chan struct{}),
		packetCh:       make(chan struct{}, 32),
		nalTypes:       map[string]bool{},
		packetizations: map[string]bool{},
	}
	installICETrace(peer, session.started, session.icePolicy, session.recordICEGatheringState, session.recordICECandidate, session.recordICEConnectionState)
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		session.recordICEConnectionState(state.String())
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			session.iceOnce.Do(func() {
				session.mu.Lock()
				session.stats.ICEConnectedLatencyMS = time.Since(session.started).Milliseconds()
				localType, remoteType := selectedCandidatePairTypes(peer)
				session.stats.SelectedLocalCandidateType = candidateTypeEvidence(localType, session.icePolicy)
				session.stats.SelectedRemoteCandidateType = candidateTypeEvidence(remoteType, session.icePolicy)
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
	if mediaSet == WebRTCMediaSetAV {
		if _, err := peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			_ = peer.Close()
			return nil, fmt.Errorf("pion media audio recvonly transceiver: %w", err)
		}
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media create offer: %w", err)
	}
	gatherComplete := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(offer); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media set local offer: %w", err)
	}
	session.recordLocalDescriptionSet()
	if err := waitICEGatheringComplete(ctx, gatherComplete, gatherTimeout); err != nil {
		_ = peer.Close()
		return nil, err
	}
	session.recordICEGatheringComplete()
	session.offer = *peer.LocalDescription()
	return session, nil
}

func (s *PionMediaOfferSession) readRemoteRTP(track *webrtc.TrackRemote) {
	isH264 := strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeH264)
	isOpus := strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeOpus)
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.stats.PacketsReceived++
		s.stats.BytesReceived += len(packet.Payload)
		if isH264 {
			s.stats.H264Packets++
			for _, packetization := range h264PayloadPacketizations(packet.Payload) {
				s.packetizations[packetization] = true
			}
			if out, err := s.h264.Unmarshal(packet.Payload); err == nil && len(out) > 0 {
				_, _ = s.h264Bytes.Write(out)
				s.stats.H264Bytes = s.h264Bytes.Len()
				s.stats.H264SHA256 = sha256Hex(s.h264Bytes.Bytes())
				for _, name := range h264NALTypeNamesFromAnnexB(out) {
					s.nalTypes[name] = true
				}
				s.stats.NALTypes = sortedEvidenceKeys(s.nalTypes)
				s.stats.Packetizations = sortedEvidenceKeys(s.packetizations)
				if s.stats.FirstH264AccessUnitMS == 0 && h264AccessUnitEvidenceReady(s.nalTypes) {
					s.stats.FirstH264AccessUnitMS = time.Since(s.started).Milliseconds()
				}
			}
		}
		if isOpus {
			_, _ = s.opusBytes.Write(packet.Payload)
			s.stats.OpusPackets++
			s.stats.OpusFrames++
			s.stats.OpusBytes = s.opusBytes.Len()
			s.stats.OpusSHA256 = sha256Hex(s.opusBytes.Bytes())
		}
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

func h264AccessUnitEvidenceReady(types map[string]bool) bool {
	return types["sps"] && types["pps"] && types["idr"]
}

type iceTraceStatsRecorder func(func(*WebRTCMediaStats))

func installICETrace(peer *webrtc.PeerConnection, started time.Time, policy webrtc.ICETransportPolicy, recordGathering func(string), recordCandidate func(string), recordConnection func(string)) {
	peer.OnICEGatheringStateChange(func(state webrtc.ICEGathererState) {
		recordGathering(state.String())
	})
	peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			recordGathering("complete")
			return
		}
		recordCandidate(candidateTypeFromCandidateLine(candidate.ToJSON().Candidate))
	})
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		recordConnection(state.String())
		_ = started
		_ = policy
	})
}

func (s *PionMediaOfferSession) recordICEGatheringState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEGatheringState(stats, state, s.started)
	})
}

func (s *PionMediaOfferSession) recordICEGatheringComplete() {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEGatheringComplete(stats, s.started)
	})
}

func (s *PionMediaOfferSession) recordICECandidate(candidateType string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICECandidate(stats, candidateType, s.started)
	})
}

func (s *PionMediaOfferSession) recordICEConnectionState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEConnectionState(stats, state, s.started)
	})
}

func (s *PionMediaOfferSession) recordRemoteDescriptionSet() {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		if stats.RemoteDescriptionSetMS == 0 {
			stats.RemoteDescriptionSetMS = time.Since(s.started).Milliseconds()
		}
	})
}

func (s *PionMediaOfferSession) recordLocalDescriptionSet() {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		if stats.LocalDescriptionSetMS == 0 {
			stats.LocalDescriptionSetMS = time.Since(s.started).Milliseconds()
		}
	})
}

func (s *PionMediaOfferSession) recordICETrace(update func(*WebRTCMediaStats)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(&s.stats)
}

func (s *PionMediaAnswerSession) recordICEGatheringState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEGatheringState(stats, state, s.started)
	})
}

func (s *PionMediaAnswerSession) recordICEGatheringComplete() {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEGatheringComplete(stats, s.started)
	})
}

func (s *PionMediaAnswerSession) recordICECandidate(candidateType string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICECandidate(stats, candidateType, s.started)
	})
}

func (s *PionMediaAnswerSession) recordICEConnectionState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEConnectionState(stats, state, s.started)
	})
}

func (s *PionMediaAnswerSession) recordRemoteDescriptionSet() {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		if stats.RemoteDescriptionSetMS == 0 {
			stats.RemoteDescriptionSetMS = time.Since(s.started).Milliseconds()
		}
	})
}

func (s *PionMediaAnswerSession) recordLocalDescriptionSet() {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		if stats.LocalDescriptionSetMS == 0 {
			stats.LocalDescriptionSetMS = time.Since(s.started).Milliseconds()
		}
	})
}

func (s *PionMediaAnswerSession) recordICETrace(update func(*WebRTCMediaStats)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	update(&s.stats)
}

func recordICEGatheringState(stats *WebRTCMediaStats, state string, started time.Time) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	stats.ICEGatheringStates = append(stats.ICEGatheringStates, fmt.Sprintf("%s@%d", state, time.Since(started).Milliseconds()))
	if state == "complete" {
		recordICEGatheringComplete(stats, started)
	}
}

func recordICEGatheringComplete(stats *WebRTCMediaStats, started time.Time) {
	if stats.ICEGatheringCompleteMS == 0 {
		stats.ICEGatheringCompleteMS = time.Since(started).Milliseconds()
	}
}

func recordICECandidate(stats *WebRTCMediaStats, candidateType string, started time.Time) {
	nowMS := time.Since(started).Milliseconds()
	if stats.FirstLocalCandidateMS == 0 {
		stats.FirstLocalCandidateMS = nowMS
	}
	switch strings.ToLower(strings.TrimSpace(candidateType)) {
	case "host":
		stats.LocalHostCandidates++
	case "srflx":
		stats.LocalSrflxCandidates++
	case "relay":
		stats.LocalRelayCandidates++
		if stats.FirstLocalRelayCandidateMS == 0 {
			stats.FirstLocalRelayCandidateMS = nowMS
		}
	}
}

func recordICEConnectionState(stats *WebRTCMediaStats, state string, started time.Time) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	nowMS := time.Since(started).Milliseconds()
	stats.ICEConnectionStates = append(stats.ICEConnectionStates, fmt.Sprintf("%s@%d", state, nowMS))
	if state == webrtc.ICEConnectionStateChecking.String() && stats.ICECheckingMS == 0 {
		stats.ICECheckingMS = nowMS
	}
}

func candidateTypeFromCandidateLine(candidate string) string {
	fields := strings.Fields(candidate)
	for i, field := range fields {
		if field == "typ" && i+1 < len(fields) {
			return strings.TrimSpace(fields[i+1])
		}
	}
	return ""
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
	if err := s.peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer["sdp"]}); err != nil {
		return err
	}
	s.recordRemoteDescriptionSet()
	return nil
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

func (s *PionMediaOfferSession) WaitForH264AccessUnit(ctx context.Context, timeout time.Duration) (WebRTCMediaStats, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		stats := s.Snapshot()
		if stats.FirstH264AccessUnitMS > 0 && h264AccessUnitEvidenceReady(mapFromStrings(stats.NALTypes)) {
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
			return stats, errors.New("webrtc media first H.264 access unit timeout")
		}
	}
}

func (s *PionMediaOfferSession) Snapshot() WebRTCMediaStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats.SelectedLocalCandidateType == "" || s.stats.SelectedRemoteCandidateType == "" {
		localType, remoteType := selectedCandidatePairTypes(s.peer)
		s.stats.SelectedLocalCandidateType = candidateTypeEvidence(localType, s.icePolicy)
		s.stats.SelectedRemoteCandidateType = candidateTypeEvidence(remoteType, s.icePolicy)
	}
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
	return NewPionMediaAnswerSessionWithICEServersForSet(ctx, offer, iceServers, WebRTCMediaSetH264, gatherTimeout)
}

func NewPionMediaAnswerSessionForSet(ctx context.Context, offer map[string]string, mediaSet string, gatherTimeout time.Duration) (*PionMediaAnswerSession, error) {
	return NewPionMediaAnswerSessionWithICEServersForSet(ctx, offer, nil, mediaSet, gatherTimeout)
}

func NewPionMediaAnswerSessionWithICEServersForSet(ctx context.Context, offer map[string]string, iceServers []webrtc.ICEServer, mediaSet string, gatherTimeout time.Duration) (*PionMediaAnswerSession, error) {
	return NewPionMediaAnswerSessionWithICEServersForSetAndPolicy(ctx, offer, iceServers, mediaSet, WebRTCICEPolicyAll, gatherTimeout)
}

func NewPionMediaAnswerSessionWithICEServersForSetAndPolicy(ctx context.Context, offer map[string]string, iceServers []webrtc.ICEServer, mediaSet, policy string, gatherTimeout time.Duration) (*PionMediaAnswerSession, error) {
	if offer["type"] != "offer" || offer["sdp"] == "" {
		return nil, errors.New("invalid media offer")
	}
	icePolicy := pionICETransportPolicy(policy)
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers, ICETransportPolicy: icePolicy})
	if err != nil {
		return nil, fmt.Errorf("pion media answer peer connection: %w", err)
	}
	session := &PionMediaAnswerSession{
		peer:         peer,
		icePolicy:    icePolicy,
		started:      time.Now(),
		iceConnected: make(chan struct{}),
	}
	installICETrace(peer, session.started, session.icePolicy, session.recordICEGatheringState, session.recordICECandidate, session.recordICEConnectionState)
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		session.recordICEConnectionState(state.String())
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			session.iceOnce.Do(func() {
				session.mu.Lock()
				session.stats.ICEConnectedLatencyMS = time.Since(session.started).Milliseconds()
				localType, remoteType := selectedCandidatePairTypes(peer)
				session.stats.SelectedLocalCandidateType = candidateTypeEvidence(localType, session.icePolicy)
				session.stats.SelectedRemoteCandidateType = candidateTypeEvidence(remoteType, session.icePolicy)
				session.mu.Unlock()
				close(session.iceConnected)
			})
		}
	})
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	}, "video", "rtk-video-loadtest")
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer track: %w", err)
	}
	if _, err := peer.AddTrack(videoTrack); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer add track: %w", err)
	}
	session.track = videoTrack
	session.videoTrack = videoTrack
	if mediaSet == WebRTCMediaSetAV {
		audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  1,
		}, "audio", "rtk-video-loadtest")
		if err != nil {
			_ = peer.Close()
			return nil, fmt.Errorf("pion media answer audio track: %w", err)
		}
		if _, err := peer.AddTrack(audioTrack); err != nil {
			_ = peer.Close()
			return nil, fmt.Errorf("pion media answer add audio track: %w", err)
		}
		session.audioTrack = audioTrack
	}
	session.codecMime = webrtc.MimeTypeH264
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer["sdp"]}); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer set remote offer: %w", err)
	}
	session.recordRemoteDescriptionSet()
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media create answer: %w", err)
	}
	gatherComplete := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(answer); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media set local answer: %w", err)
	}
	session.recordLocalDescriptionSet()
	if err := waitICEGatheringComplete(ctx, gatherComplete, gatherTimeout); err != nil {
		_ = peer.Close()
		return nil, err
	}
	session.recordICEGatheringComplete()
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
		if err := s.videoTrack.WriteRTP(packet); err != nil {
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
	if err := s.waitICEConnected(ctx); err != nil {
		return H264RTPPlan{}, err
	}
	stats := s.Snapshot()
	if err := waitWebRTCMediaSettle(ctx); err != nil {
		return H264RTPPlan{}, err
	}
	plan, err := buildH264MediaPlan(duration)
	if err != nil {
		return H264RTPPlan{}, err
	}
	localType, remoteType := s.SelectedCandidatePairTypes()
	plan.Evidence.SelectedLocalCandidateType = candidateTypeEvidence(localType, s.icePolicy)
	plan.Evidence.SelectedRemoteCandidateType = candidateTypeEvidence(remoteType, s.icePolicy)
	plan.Evidence = plan.Evidence.WithICEStats(stats)
	plan.Evidence.ICEMS = stats.ICEConnectedLatencyMS
	plan.Evidence.TimeToFirstMS = nonNegativeMS(time.Since(s.started).Milliseconds() - stats.ICEConnectedLatencyMS)
	sent, err := s.sendH264Plan(ctx, plan)
	sent.Evidence.ReceiveMS = time.Since(s.started).Milliseconds()
	return sent, err
}

func (s *PionMediaAnswerSession) SendAVRTP(ctx context.Context, duration time.Duration) (AVRTPEvidence, error) {
	if err := s.waitICEConnected(ctx); err != nil {
		return AVRTPEvidence{}, err
	}
	stats := s.Snapshot()
	if err := waitWebRTCMediaSettle(ctx); err != nil {
		return AVRTPEvidence{}, err
	}
	videoPlan, err := buildH264MediaPlan(duration)
	if err != nil {
		return AVRTPEvidence{}, err
	}
	audioPlan, err := buildOpusMediaPlan(duration)
	if err != nil {
		return AVRTPEvidence{}, err
	}
	localType, remoteType := s.SelectedCandidatePairTypes()
	videoPlan.Evidence.SelectedLocalCandidateType = candidateTypeEvidence(localType, s.icePolicy)
	videoPlan.Evidence.SelectedRemoteCandidateType = candidateTypeEvidence(remoteType, s.icePolicy)
	audioPlan.Evidence.SelectedLocalCandidateType = candidateTypeEvidence(localType, s.icePolicy)
	audioPlan.Evidence.SelectedRemoteCandidateType = candidateTypeEvidence(remoteType, s.icePolicy)
	videoPlan.Evidence = videoPlan.Evidence.WithICEStats(stats)
	audioPlan.Evidence = audioPlan.Evidence.WithICEStats(stats)
	firstRTPAfterICE := nonNegativeMS(time.Since(s.started).Milliseconds() - stats.ICEConnectedLatencyMS)
	videoPlan.Evidence.ICEMS = stats.ICEConnectedLatencyMS
	videoPlan.Evidence.TimeToFirstMS = firstRTPAfterICE
	audioPlan.Evidence.ICEMS = stats.ICEConnectedLatencyMS
	audioPlan.Evidence.TimeToFirstMS = firstRTPAfterICE
	errCh := make(chan error, 2)
	go func() {
		_, err := s.sendH264Plan(ctx, videoPlan)
		errCh <- err
	}()
	go func() {
		_, err := s.sendOpusPlan(ctx, audioPlan)
		errCh <- err
	}()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			return AVRTPEvidence{}, err
		}
	}
	receiveMS := time.Since(s.started).Milliseconds()
	videoPlan.Evidence.ReceiveMS = receiveMS
	audioPlan.Evidence.ReceiveMS = receiveMS
	return AVRTPEvidence{Video: videoPlan.Evidence, Audio: audioPlan.Evidence}, nil
}

func waitWebRTCMediaSettle(ctx context.Context) error {
	timer := time.NewTimer(defaultWebRTCMediaSettle)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *PionMediaAnswerSession) waitICEConnected(ctx context.Context) error {
	select {
	case <-s.iceConnected:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("webrtc media answerer ICE connection timeout")
	}
}

func (s *PionMediaAnswerSession) Snapshot() WebRTCMediaStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats.SelectedLocalCandidateType == "" || s.stats.SelectedRemoteCandidateType == "" {
		localType, remoteType := selectedCandidatePairTypes(s.peer)
		s.stats.SelectedLocalCandidateType = candidateTypeEvidence(localType, s.icePolicy)
		s.stats.SelectedRemoteCandidateType = candidateTypeEvidence(remoteType, s.icePolicy)
	}
	return s.stats
}

func (s *PionMediaAnswerSession) sendH264Plan(ctx context.Context, plan H264RTPPlan) (H264RTPPlan, error) {
	interval := time.Duration(0)
	if len(plan.Packets) > 0 && plan.Duration > 0 {
		interval = plan.Duration / time.Duration(len(plan.Packets))
	}
	for i, packet := range plan.Packets {
		if err := s.videoTrack.WriteRTP(packet); err != nil {
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

func (s *PionMediaAnswerSession) sendOpusPlan(ctx context.Context, plan OpusRTPPlan) (OpusRTPPlan, error) {
	if s.audioTrack == nil {
		return OpusRTPPlan{}, errors.New("webrtc media answerer missing Opus audio track")
	}
	interval := time.Duration(0)
	if len(plan.Packets) > 0 && plan.Duration > 0 {
		interval = plan.Duration / time.Duration(len(plan.Packets))
	}
	for i, packet := range plan.Packets {
		if err := s.audioTrack.WriteRTP(packet); err != nil {
			return OpusRTPPlan{}, err
		}
		if interval <= 0 || i == len(plan.Packets)-1 {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return OpusRTPPlan{}, ctx.Err()
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
	expectedSHA256, h264Bytes, err := h264BitstreamSHA256FromRTP(allPackets)
	if err != nil {
		return H264RTPPlan{}, err
	}
	evidence.ExpectedSHA256 = expectedSHA256
	evidence.ReceiverSHA256 = ""
	evidence.ReceiverBytes = h264Bytes
	return H264RTPPlan{Duration: duration, Loops: loops, FrameRate: frameRate, Frames: evidence.Frames, Packets: allPackets, Evidence: evidence}, nil
}

func buildOpusMediaPlan(duration time.Duration) (OpusRTPPlan, error) {
	if duration <= 0 {
		duration = 20 * time.Second
	}
	const (
		fixtureDuration = 2 * time.Second
		sampleRate      = 48000
		channels        = 1
		frameDuration   = 20 * time.Millisecond
	)
	frames, err := opusFrameFixture()
	if err != nil {
		return OpusRTPPlan{}, err
	}
	loops := int((duration + fixtureDuration - 1) / fixtureDuration)
	if loops < 1 {
		loops = 1
	}
	allPackets := make([]*rtp.Packet, 0, loops*len(frames))
	var payloads bytes.Buffer
	sequence := uint16(1)
	timestamp := uint32(0)
	for i := 0; i < loops; i++ {
		for _, frame := range frames {
			payload := append([]byte(nil), frame...)
			allPackets = append(allPackets, &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    111,
					SequenceNumber: sequence,
					Timestamp:      timestamp,
					SSRC:           0x52544b41,
					Marker:         false,
				},
				Payload: payload,
			})
			_, _ = payloads.Write(payload)
			sequence++
			timestamp += uint32(sampleRate * frameDuration / time.Second)
		}
	}
	evidence := OpusRTPEvidence{
		Packets:        len(allPackets),
		Bytes:          payloads.Len(),
		DurationMS:     duration.Milliseconds(),
		Loops:          loops,
		Frames:         len(allPackets),
		SampleRate:     sampleRate,
		Channels:       channels,
		ExpectedSHA256: sha256Hex(payloads.Bytes()),
	}
	return OpusRTPPlan{Duration: duration, Loops: loops, SampleRate: sampleRate, Channels: channels, Frames: len(allPackets), Packets: allPackets, Evidence: evidence}, nil
}

func opusFrameFixture() ([][]byte, error) {
	data, err := opusFrameFixtureData()
	if err != nil {
		return nil, err
	}
	frames := make([][]byte, 0, 100)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.Contains(line, "=") && !strings.HasPrefix(line, "repeat=") {
			continue
		}
		if !strings.HasPrefix(line, "repeat=") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "repeat="))
		if len(fields) < 2 {
			return nil, errors.New("Opus fixture repeat line requires count and frame hex payloads")
		}
		count, err := strconv.Atoi(fields[0])
		if err != nil || count <= 0 {
			return nil, errors.New("Opus fixture repeat count is invalid")
		}
		payloads := fields[1:]
		for i := 0; i < count; i++ {
			for _, payloadHex := range payloads {
				payload, err := hex.DecodeString(payloadHex)
				if err != nil {
					return nil, fmt.Errorf("decode Opus fixture payload: %w", err)
				}
				frames = append(frames, payload)
			}
		}
	}
	if len(frames) == 0 {
		return nil, errors.New("Opus fixture has no frames")
	}
	return frames, nil
}

func opusFrameFixtureData() ([]byte, error) {
	for _, path := range []string{
		"../testdata/testtone_48k_mono_2s.opusframes",
		"testdata/testtone_48k_mono_2s.opusframes",
		"video_cloud/load/testdata/testtone_48k_mono_2s.opusframes",
		"e2e_test/video_cloud/load/testdata/testtone_48k_mono_2s.opusframes",
	} {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
	}
	return nil, errors.New("missing Opus fixture testtone_48k_mono_2s.opusframes")
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

func h264BitstreamSHA256FromRTP(packets []*rtp.Packet) (string, int, error) {
	var depacketizer codecs.H264Packet
	var out bytes.Buffer
	for _, packet := range packets {
		payload, err := depacketizer.Unmarshal(packet.Payload)
		if err != nil {
			return "", 0, err
		}
		if len(payload) > 0 {
			_, _ = out.Write(payload)
		}
	}
	if out.Len() == 0 {
		return "", 0, errors.New("H.264 RTP packets depacketized to empty bitstream")
	}
	return sha256Hex(out.Bytes()), out.Len(), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func h264NALTypeNamesFromAnnexB(data []byte) []string {
	nals, err := h264NALUnits(data)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		if name := h264NALTypeName(nal[0] & 0x1f); name != "" {
			seen[name] = true
		}
	}
	return sortedEvidenceKeys(seen)
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

func (e H264RTPEvidence) WithICEStats(stats WebRTCMediaStats) H264RTPEvidence {
	e.ICEGatheringCompleteMS = stats.ICEGatheringCompleteMS
	e.RemoteDescriptionSetMS = stats.RemoteDescriptionSetMS
	e.LocalDescriptionSetMS = stats.LocalDescriptionSetMS
	e.ICECheckingMS = stats.ICECheckingMS
	e.FirstLocalCandidateMS = stats.FirstLocalCandidateMS
	e.FirstLocalRelayCandidateMS = stats.FirstLocalRelayCandidateMS
	e.LocalHostCandidates = stats.LocalHostCandidates
	e.LocalSrflxCandidates = stats.LocalSrflxCandidates
	e.LocalRelayCandidates = stats.LocalRelayCandidates
	e.ICEConnectionStates = append([]string(nil), stats.ICEConnectionStates...)
	e.ICEGatheringStates = append([]string(nil), stats.ICEGatheringStates...)
	return e
}

func (e H264RTPEvidence) String() string {
	base := fmt.Sprintf("codec=h264 packets=%d bytes=%d duration_ms=%d loops=%d frames=%d nal_types=%s packetization=%s receive_ms=%d ttfb_ms=%d ice_ms=%d selected_local_candidate_type=%s selected_remote_candidate_type=%s",
		e.Packets, e.Bytes, e.DurationMS, e.Loops, e.Frames, joinEvidenceKeys(e.NALTypes), joinEvidenceKeys(e.Packetizations), e.ReceiveMS, e.TimeToFirstMS, e.ICEMS, evidenceOrDefault(e.SelectedLocalCandidateType, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateType, "unknown"))
	base = appendICETraceEvidence(base, e.ICEGatheringCompleteMS, e.RemoteDescriptionSetMS, e.LocalDescriptionSetMS, e.ICECheckingMS, e.FirstLocalCandidateMS, e.FirstLocalRelayCandidateMS, e.LocalHostCandidates, e.LocalSrflxCandidates, e.LocalRelayCandidates, e.ICEConnectionStates, e.ICEGatheringStates)
	if e.ExpectedSHA256 != "" {
		base += fmt.Sprintf(" expected_sha256=%s", e.ExpectedSHA256)
	}
	if e.ReceiverSHA256 != "" || e.ReceiverPackets > 0 || e.ReceiverBytes > 0 {
		base += fmt.Sprintf(" received_sha256=%s receiver_packets=%d receiver_bytes=%d receiver_nal_types=%s receiver_bitstream_match=%t",
			e.ReceiverSHA256, e.ReceiverPackets, e.ReceiverBytes, joinEvidenceKeys(e.ReceiverNALTypes), e.BitstreamMatch)
	}
	return base
}

func (e OpusRTPEvidence) WithTimings(receiveMS, timeToFirstMS, iceMS int64) OpusRTPEvidence {
	e.ReceiveMS = receiveMS
	e.TimeToFirstMS = timeToFirstMS
	e.ICEMS = iceMS
	return e
}

func (e OpusRTPEvidence) WithICEStats(stats WebRTCMediaStats) OpusRTPEvidence {
	e.ICEGatheringCompleteMS = stats.ICEGatheringCompleteMS
	e.RemoteDescriptionSetMS = stats.RemoteDescriptionSetMS
	e.LocalDescriptionSetMS = stats.LocalDescriptionSetMS
	e.ICECheckingMS = stats.ICECheckingMS
	e.FirstLocalCandidateMS = stats.FirstLocalCandidateMS
	e.FirstLocalRelayCandidateMS = stats.FirstLocalRelayCandidateMS
	e.LocalHostCandidates = stats.LocalHostCandidates
	e.LocalSrflxCandidates = stats.LocalSrflxCandidates
	e.LocalRelayCandidates = stats.LocalRelayCandidates
	e.ICEConnectionStates = append([]string(nil), stats.ICEConnectionStates...)
	e.ICEGatheringStates = append([]string(nil), stats.ICEGatheringStates...)
	return e
}

func (e OpusRTPEvidence) String() string {
	base := fmt.Sprintf("codec=opus packets=%d bytes=%d duration_ms=%d loops=%d frames=%d sample_rate=%d channels=%d receive_ms=%d ttfb_ms=%d ice_ms=%d selected_local_candidate_type=%s selected_remote_candidate_type=%s",
		e.Packets, e.Bytes, e.DurationMS, e.Loops, e.Frames, e.SampleRate, e.Channels, e.ReceiveMS, e.TimeToFirstMS, e.ICEMS, evidenceOrDefault(e.SelectedLocalCandidateType, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateType, "unknown"))
	base = appendICETraceEvidence(base, e.ICEGatheringCompleteMS, e.RemoteDescriptionSetMS, e.LocalDescriptionSetMS, e.ICECheckingMS, e.FirstLocalCandidateMS, e.FirstLocalRelayCandidateMS, e.LocalHostCandidates, e.LocalSrflxCandidates, e.LocalRelayCandidates, e.ICEConnectionStates, e.ICEGatheringStates)
	if e.ExpectedSHA256 != "" {
		base += fmt.Sprintf(" expected_sha256=%s", e.ExpectedSHA256)
	}
	base += fmt.Sprintf(" received_sha256=%s receiver_packets=%d receiver_bytes=%d receiver_frames=%d audio_payload_match=%t",
		e.ReceiverSHA256, e.ReceiverPackets, e.ReceiverBytes, e.ReceiverFrames, e.PayloadMatch)
	return base
}

func (e AVRTPEvidence) WithTimings(receiveMS, timeToFirstMS, iceMS int64) AVRTPEvidence {
	e.Video = e.Video.WithTimings(receiveMS, timeToFirstMS, iceMS)
	e.Audio = e.Audio.WithTimings(receiveMS, timeToFirstMS, iceMS)
	return e
}

func (e AVRTPEvidence) String() string {
	return "media_model=h264_opus_av " + prefixEvidenceKeys(e.Video.String(), "video_") + " " + prefixEvidenceKeys(e.Audio.String(), "audio_")
}

func evidenceOrDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func appendICETraceEvidence(base string, gatherCompleteMS, remoteDescriptionMS, localDescriptionMS, checkingMS, firstCandidateMS, firstRelayCandidateMS int64, hostCandidates, srflxCandidates, relayCandidates int, connectionStates, gatheringStates []string) string {
	parts := []string{}
	if gatherCompleteMS > 0 {
		parts = append(parts, fmt.Sprintf("ice_gather_complete_ms=%d", gatherCompleteMS))
	}
	if remoteDescriptionMS > 0 {
		parts = append(parts, fmt.Sprintf("remote_description_set_ms=%d", remoteDescriptionMS))
	}
	if localDescriptionMS > 0 {
		parts = append(parts, fmt.Sprintf("local_description_set_ms=%d", localDescriptionMS))
	}
	if checkingMS > 0 {
		parts = append(parts, fmt.Sprintf("ice_checking_ms=%d", checkingMS))
	}
	if firstCandidateMS > 0 {
		parts = append(parts, fmt.Sprintf("first_local_candidate_ms=%d", firstCandidateMS))
	}
	if firstRelayCandidateMS > 0 {
		parts = append(parts, fmt.Sprintf("first_local_relay_candidate_ms=%d", firstRelayCandidateMS))
	}
	parts = append(parts,
		fmt.Sprintf("local_host_candidates=%d", hostCandidates),
		fmt.Sprintf("local_srflx_candidates=%d", srflxCandidates),
		fmt.Sprintf("local_relay_candidates=%d", relayCandidates),
	)
	if len(connectionStates) > 0 {
		parts = append(parts, "ice_connection_states="+strings.Join(connectionStates, ","))
	}
	if len(gatheringStates) > 0 {
		parts = append(parts, "ice_gathering_states="+strings.Join(gatheringStates, ","))
	}
	return appendEvidence(base, strings.Join(parts, " "))
}

func iceTraceEvidence(stats WebRTCMediaStats) string {
	return appendICETraceEvidence("", stats.ICEGatheringCompleteMS, stats.RemoteDescriptionSetMS, stats.LocalDescriptionSetMS, stats.ICECheckingMS, stats.FirstLocalCandidateMS, stats.FirstLocalRelayCandidateMS, stats.LocalHostCandidates, stats.LocalSrflxCandidates, stats.LocalRelayCandidates, stats.ICEConnectionStates, stats.ICEGatheringStates)
}

func prefixEvidenceKeys(evidence, prefix string) string {
	parts := strings.Fields(evidence)
	for i, part := range parts {
		if eq := strings.IndexByte(part, '='); eq > 0 && !strings.HasPrefix(part[:eq], prefix) {
			parts[i] = prefix + part
		}
	}
	return strings.Join(parts, " ")
}

func joinEvidenceKeys(values map[string]bool) string {
	return strings.Join(sortedEvidenceKeys(values), ",")
}

func sortedEvidenceKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key, ok := range values {
		if ok && key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (s *PionMediaAnswerSession) Close() {
	if s != nil && s.peer != nil {
		s.closeOnce.Do(func() { _ = s.peer.Close() })
	}
}

func waitICEGatheringComplete(ctx context.Context, gatherComplete <-chan struct{}, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
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
	return s.ValidateAnswerWithICEPolicy(response, WebRTCICEPolicyAll)
}

func (s *PionOfferSession) ValidateAnswerWithICEPolicy(response map[string]any, policy string) (WebRTCValidation, error) {
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
	return ValidateWebRTCSetupWithICEPolicy(response, WebRTCICEPolicyAll)
}

func ValidateWebRTCSetupWithICEPolicy(response map[string]any, policy string) (WebRTCValidation, error) {
	iceServers, err := extractICEServers(response)
	if err != nil {
		return WebRTCValidation{}, err
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers, ICETransportPolicy: pionICETransportPolicy(policy)})
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

func (s *PionOfferSession) ICETransportPolicy() webrtc.ICETransportPolicy {
	if s == nil {
		return webrtc.ICETransportPolicyAll
	}
	return s.icePolicy
}

func (s *PionMediaOfferSession) ICETransportPolicy() webrtc.ICETransportPolicy {
	if s == nil {
		return webrtc.ICETransportPolicyAll
	}
	return s.icePolicy
}

func (s *PionMediaAnswerSession) ICETransportPolicy() webrtc.ICETransportPolicy {
	if s == nil {
		return webrtc.ICETransportPolicyAll
	}
	return s.icePolicy
}

func (s *PionMediaAnswerSession) SelectedCandidatePairTypes() (string, string) {
	if s == nil {
		return "", ""
	}
	return selectedCandidatePairTypes(s.peer)
}

func selectedCandidatePairTypes(peer *webrtc.PeerConnection) (string, string) {
	if peer == nil {
		return "", ""
	}
	for _, transceiver := range peer.GetTransceivers() {
		if transceiver == nil {
			continue
		}
		if receiver := transceiver.Receiver(); receiver != nil {
			if localType, remoteType := selectedCandidatePairTypesFromDTLS(receiver.Transport()); localType != "" || remoteType != "" {
				return localType, remoteType
			}
		}
		if sender := transceiver.Sender(); sender != nil {
			if localType, remoteType := selectedCandidatePairTypesFromDTLS(sender.Transport()); localType != "" || remoteType != "" {
				return localType, remoteType
			}
		}
	}
	return "", ""
}

func selectedCandidatePairTypesFromDTLS(dtls *webrtc.DTLSTransport) (string, string) {
	if dtls == nil || dtls.ICETransport() == nil {
		return "", ""
	}
	pair, err := dtls.ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return "", ""
	}
	localType, remoteType := "", ""
	if pair.Local != nil {
		localType = pair.Local.Typ.String()
	}
	if pair.Remote != nil {
		remoteType = pair.Remote.Typ.String()
	}
	return localType, remoteType
}

func candidateTypeEvidence(candidateType string, policy webrtc.ICETransportPolicy) string {
	candidateType = strings.TrimSpace(candidateType)
	if candidateType != "" {
		return candidateType
	}
	if policy == webrtc.ICETransportPolicyRelay {
		return "relay_inferred"
	}
	return ""
}

func pionICETransportPolicy(policy string) webrtc.ICETransportPolicy {
	if strings.EqualFold(strings.TrimSpace(policy), WebRTCICEPolicyRelay) {
		return webrtc.ICETransportPolicyRelay
	}
	return webrtc.ICETransportPolicyAll
}

func normalizedWebRTCICEPolicy(policy string) string {
	if strings.EqualFold(strings.TrimSpace(policy), WebRTCICEPolicyRelay) {
		return WebRTCICEPolicyRelay
	}
	return WebRTCICEPolicyAll
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
