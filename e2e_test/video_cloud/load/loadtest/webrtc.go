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

const (
	defaultWebRTCMediaSettle      = 0
	defaultWebRTCICERequiredWait  = 30 * time.Second
	defaultWebRTCPeerRequiredWait = 30 * time.Second
)

type WebRTCValidation struct {
	ICEServerCount int
}

type PionOfferSession struct {
	peer      *webrtc.PeerConnection
	offer     webrtc.SessionDescription
	icePolicy webrtc.ICETransportPolicy
}

type WebRTCMediaStats struct {
	ICEConnectedLatencyMS           int64
	ICEGatheringCompleteMS          int64
	RemoteDescriptionSetMS          int64
	LocalDescriptionSetMS           int64
	PionCreatePeerMS                int64
	PionCreateOfferMS               int64
	PionCreateAnswerMS              int64
	PionSetLocalDescriptionMS       int64
	PionSetRemoteDescriptionMS      int64
	PionICEGatheringWaitMS          int64
	ICECheckingMS                   int64
	FirstLocalCandidateMS           int64
	FirstLocalRelayCandidateMS      int64
	FirstLocalRelayUDPCandidateMS   int64
	FirstLocalRelayTCPCandidateMS   int64
	LocalHostCandidates             int
	LocalSrflxCandidates            int
	LocalRelayCandidates            int
	LocalUDPCandidates              int
	LocalTCPCandidates              int
	LocalRelayUDPCandidates         int
	LocalRelayTCPCandidates         int
	ICEConnectionStates             []string
	ICEGatheringStates              []string
	PeerConnectionStates            []string
	PeerConnectionConnectedMS       int64
	TrackArrivedMS                  int64
	TrackKind                       string
	TrackCodec                      string
	FirstRTPPayloadType             int
	FirstRTPSequenceNumber          uint16
	FirstRTPTimestamp               uint32
	FirstRTPSSRC                    uint32
	ICESelectedPairChanges          int
	ICESelectedPairFirstChangeMS    int64
	ICESelectedPairLastChangeMS     int64
	ICERequestsSent                 uint64
	ICERequestsReceived             uint64
	ICEResponsesSent                uint64
	ICEResponsesReceived            uint64
	ICERetransmissionsSent          uint64
	ICERetransmissionsReceived      uint64
	ICEConsentRequestsSent          uint64
	ICECurrentRoundTripTimeMS       int64
	TimeToFirstRTPMS                int64
	FirstH264RTPMS                  int64
	FirstH264AccessUnitMS           int64
	PacketsReceived                 int
	BytesReceived                   int
	ReceiveDurationMS               int64
	SelectedLocalCandidateType      string
	SelectedRemoteCandidateType     string
	SelectedLocalCandidateProtocol  string
	SelectedRemoteCandidateProtocol string
	SelectedLocalCandidateAddress   string
	SelectedRemoteCandidateAddress  string
	SelectedLocalCandidatePort      int
	SelectedRemoteCandidatePort     int
	SelectedLocalCandidateTCPType   string
	SelectedRemoteCandidateTCPType  string
	H264SHA256                      string
	H264Bytes                       int
	H264Packets                     int
	NALTypes                        []string
	Packetizations                  []string
	FirstOpusRTPMS                  int64
	OpusBytes                       int
	OpusPackets                     int
	OpusFrames                      int
}

type H264RTPPlan struct {
	Duration     time.Duration
	Loops        int
	FrameRate    int
	Frames       int
	FramePackets []H264RTPFrame
	Packets      []*rtp.Packet
	Evidence     H264RTPEvidence
}

type H264RTPFrame struct {
	Packets []*rtp.Packet
}

type H264RTPEvidence struct {
	Packets                               int
	Bytes                                 int
	DurationMS                            int64
	Loops                                 int
	Frames                                int
	NALTypes                              map[string]bool
	Packetizations                        map[string]bool
	ReceiveMS                             int64
	TimeToFirstMS                         int64
	ICEMS                                 int64
	ICEGatheringCompleteMS                int64
	RemoteDescriptionSetMS                int64
	LocalDescriptionSetMS                 int64
	PionCreatePeerMS                      int64
	PionCreateOfferMS                     int64
	PionCreateAnswerMS                    int64
	PionSetLocalDescriptionMS             int64
	PionSetRemoteDescriptionMS            int64
	PionICEGatheringWaitMS                int64
	ICECheckingMS                         int64
	FirstLocalCandidateMS                 int64
	FirstLocalRelayCandidateMS            int64
	FirstLocalRelayUDPCandidateMS         int64
	FirstLocalRelayTCPCandidateMS         int64
	LocalHostCandidates                   int
	LocalSrflxCandidates                  int
	LocalRelayCandidates                  int
	LocalUDPCandidates                    int
	LocalTCPCandidates                    int
	LocalRelayUDPCandidates               int
	LocalRelayTCPCandidates               int
	ICEConnectionStates                   []string
	ICEGatheringStates                    []string
	PeerConnectionStates                  []string
	PeerConnectionConnectedMS             int64
	ICESelectedPairChanges                int
	ICESelectedPairFirstChangeMS          int64
	ICESelectedPairLastChangeMS           int64
	ICERequestsSent                       uint64
	ICERequestsReceived                   uint64
	ICEResponsesSent                      uint64
	ICEResponsesReceived                  uint64
	ICERetransmissionsSent                uint64
	ICERetransmissionsReceived            uint64
	ICEConsentRequestsSent                uint64
	ICECurrentRoundTripTimeMS             int64
	SelectedLocalCandidateType            string
	SelectedRemoteCandidateType           string
	SelectedLocalCandidateProtocol        string
	SelectedRemoteCandidateProtocol       string
	SelectedLocalCandidateAddress         string
	SelectedRemoteCandidateAddress        string
	SelectedLocalCandidatePort            int
	SelectedRemoteCandidatePort           int
	SelectedLocalCandidateTCPType         string
	SelectedRemoteCandidateTCPType        string
	ExpectedSHA256                        string
	ReceiverSHA256                        string
	ReceiverPackets                       int
	ReceiverBytes                         int
	ReceiverNALTypes                      map[string]bool
	BitstreamMatch                        bool
	SchedulerDroppedJobs                  int
	SchedulerDroppedPackets               int
	SchedulerQueueFullDrops               int
	SchedulerZeroByteWrites               int
	SchedulerWriteAttempts                int
	SchedulerWriteReturns                 int
	SchedulerWriteErrors                  int
	SchedulerFirstWriteCallMS             int64
	SchedulerFirstWriteReturnMS           int64
	SchedulerFirstWriteCallSinceStartMS   int64
	SchedulerFirstWriteReturnSinceStartMS int64
	SchedulerMaxWriteMS                   int64
	SchedulerPacketsSent                  int
	SchedulerBytesSent                    int
	SchedulerStartedAtMS                  int64
	SchedulerFirstWriteMS                 int64
	SchedulerFirstWriteSinceStartMS       int64
	SchedulerEndedAtMS                    int64
	SchedulerSendDurationMS               int64
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
	Packets                               int
	Bytes                                 int
	DurationMS                            int64
	Loops                                 int
	Frames                                int
	SampleRate                            int
	Channels                              int
	ReceiveMS                             int64
	TimeToFirstMS                         int64
	ICEMS                                 int64
	ICEGatheringCompleteMS                int64
	RemoteDescriptionSetMS                int64
	LocalDescriptionSetMS                 int64
	PionCreatePeerMS                      int64
	PionCreateOfferMS                     int64
	PionCreateAnswerMS                    int64
	PionSetLocalDescriptionMS             int64
	PionSetRemoteDescriptionMS            int64
	PionICEGatheringWaitMS                int64
	ICECheckingMS                         int64
	FirstLocalCandidateMS                 int64
	FirstLocalRelayCandidateMS            int64
	FirstLocalRelayUDPCandidateMS         int64
	FirstLocalRelayTCPCandidateMS         int64
	LocalHostCandidates                   int
	LocalSrflxCandidates                  int
	LocalRelayCandidates                  int
	LocalUDPCandidates                    int
	LocalTCPCandidates                    int
	LocalRelayUDPCandidates               int
	LocalRelayTCPCandidates               int
	ICEConnectionStates                   []string
	ICEGatheringStates                    []string
	PeerConnectionStates                  []string
	PeerConnectionConnectedMS             int64
	ICESelectedPairChanges                int
	ICESelectedPairFirstChangeMS          int64
	ICESelectedPairLastChangeMS           int64
	ICERequestsSent                       uint64
	ICERequestsReceived                   uint64
	ICEResponsesSent                      uint64
	ICEResponsesReceived                  uint64
	ICERetransmissionsSent                uint64
	ICERetransmissionsReceived            uint64
	ICEConsentRequestsSent                uint64
	ICECurrentRoundTripTimeMS             int64
	SelectedLocalCandidateType            string
	SelectedRemoteCandidateType           string
	SelectedLocalCandidateProtocol        string
	SelectedRemoteCandidateProtocol       string
	SelectedLocalCandidateAddress         string
	SelectedRemoteCandidateAddress        string
	SelectedLocalCandidatePort            int
	SelectedRemoteCandidatePort           int
	SelectedLocalCandidateTCPType         string
	SelectedRemoteCandidateTCPType        string
	ReceiverPackets                       int
	ReceiverBytes                         int
	ReceiverFrames                        int
	FirstOpusRTPMS                        int64
	SchedulerDroppedJobs                  int
	SchedulerDroppedPackets               int
	SchedulerQueueFullDrops               int
	SchedulerZeroByteWrites               int
	SchedulerWriteAttempts                int
	SchedulerWriteReturns                 int
	SchedulerWriteErrors                  int
	SchedulerFirstWriteCallMS             int64
	SchedulerFirstWriteReturnMS           int64
	SchedulerFirstWriteCallSinceStartMS   int64
	SchedulerFirstWriteReturnSinceStartMS int64
	SchedulerMaxWriteMS                   int64
	SchedulerPacketsSent                  int
	SchedulerBytesSent                    int
	SchedulerStartedAtMS                  int64
	SchedulerFirstWriteMS                 int64
	SchedulerFirstWriteSinceStartMS       int64
	SchedulerEndedAtMS                    int64
	SchedulerSendDurationMS               int64
}

type AVRTPEvidence struct {
	Video H264RTPEvidence
	Audio OpusRTPEvidence
}

type PionMediaOfferSession struct {
	peer                *webrtc.PeerConnection
	offer               webrtc.SessionDescription
	icePolicy           webrtc.ICETransportPolicy
	started             time.Time
	iceConnected        chan struct{}
	peerConnected       chan struct{}
	localRelayCandidate chan struct{}
	firstRTP            chan struct{}
	packetCh            chan struct{}
	closeOnce           sync.Once
	iceOnce             sync.Once
	peerOnce            sync.Once
	relayOnce           sync.Once
	firstOnce           sync.Once
	mu                  sync.Mutex
	stats               WebRTCMediaStats
	h264                codecs.H264Packet
	h264Bytes           bytes.Buffer
	nalTypes            map[string]bool
	packetizations      map[string]bool
}

type PionMediaAnswerSession struct {
	peer                *webrtc.PeerConnection
	track               *webrtc.TrackLocalStaticRTP
	videoTrack          *webrtc.TrackLocalStaticRTP
	audioTrack          *webrtc.TrackLocalStaticRTP
	codecMime           string
	answer              webrtc.SessionDescription
	icePolicy           webrtc.ICETransportPolicy
	started             time.Time
	mu                  sync.Mutex
	stats               WebRTCMediaStats
	iceConnected        chan struct{}
	peerConnected       chan struct{}
	localRelayCandidate chan struct{}
	closeOnce           sync.Once
	iceOnce             sync.Once
	peerOnce            sync.Once
	relayOnce           sync.Once
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
	sessionStarted := time.Now()
	phaseStarted := time.Now()
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers, ICETransportPolicy: icePolicy})
	if err != nil {
		return nil, fmt.Errorf("pion media offer peer connection: %w", err)
	}
	createPeerMS := time.Since(phaseStarted).Milliseconds()
	session := &PionMediaOfferSession{
		peer:                peer,
		icePolicy:           icePolicy,
		started:             sessionStarted,
		iceConnected:        make(chan struct{}),
		peerConnected:       make(chan struct{}),
		localRelayCandidate: make(chan struct{}),
		firstRTP:            make(chan struct{}),
		packetCh:            make(chan struct{}, 32),
		stats:               WebRTCMediaStats{PionCreatePeerMS: createPeerMS},
		nalTypes:            map[string]bool{},
		packetizations:      map[string]bool{},
	}
	installICETrace(peer, session.started, session.icePolicy, session.recordICEGatheringState, session.recordICECandidate, session.recordICEConnectionState)
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		session.recordICEConnectionState(state.String())
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			session.iceOnce.Do(func() {
				session.mu.Lock()
				session.stats.ICEConnectedLatencyMS = time.Since(session.started).Milliseconds()
				pair := selectedCandidatePairTrace(peer)
				applySelectedCandidatePairTrace(&session.stats, pair, session.icePolicy)
				session.mu.Unlock()
				close(session.iceConnected)
			})
		}
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		session.recordPeerConnectionState(state.String())
		if state == webrtc.PeerConnectionStateConnected {
			session.peerOnce.Do(func() {
				session.recordICETrace(func(stats *WebRTCMediaStats) {
					stats.PeerConnectionConnectedMS = time.Since(session.started).Milliseconds()
				})
				close(session.peerConnected)
			})
		}
	})
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		session.recordTrackArrived(track)
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
	installSelectedCandidatePairTrace(peer, session.recordSelectedCandidatePairChange)
	phaseStarted = time.Now()
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media create offer: %w", err)
	}
	session.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionCreateOfferMS = time.Since(phaseStarted).Milliseconds()
	})
	gatherComplete := webrtc.GatheringCompletePromise(peer)
	phaseStarted = time.Now()
	if err := peer.SetLocalDescription(offer); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media set local offer: %w", err)
	}
	session.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionSetLocalDescriptionMS = time.Since(phaseStarted).Milliseconds()
	})
	session.recordLocalDescriptionSet()
	phaseStarted = time.Now()
	if err := waitICEGatheringReady(ctx, gatherComplete, session.localRelayCandidate, icePolicy, gatherTimeout); err != nil {
		_ = peer.Close()
		return nil, err
	}
	session.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionICEGatheringWaitMS = time.Since(phaseStarted).Milliseconds()
	})
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
		if s.stats.TimeToFirstRTPMS == 0 {
			s.stats.TimeToFirstRTPMS = time.Since(s.started).Milliseconds()
			s.stats.FirstRTPPayloadType = int(packet.PayloadType)
			s.stats.FirstRTPSequenceNumber = packet.SequenceNumber
			s.stats.FirstRTPTimestamp = packet.Timestamp
			s.stats.FirstRTPSSRC = packet.SSRC
		}
		s.stats.PacketsReceived++
		s.stats.BytesReceived += len(packet.Payload)
		if isH264 {
			s.stats.H264Packets++
			s.stats.H264Bytes += len(packet.Payload)
			nowMS := time.Since(s.started).Milliseconds()
			if s.stats.FirstH264RTPMS == 0 {
				s.stats.FirstH264RTPMS = nowMS
			}
			for _, nalType := range h264NALTypesFromRTPPayload(packet.Payload) {
				s.stats.NALTypes = appendUniqueString(s.stats.NALTypes, nalType)
			}
			for _, packetization := range h264PayloadPacketizations(packet.Payload) {
				s.stats.Packetizations = appendUniqueString(s.stats.Packetizations, packetization)
			}
			if s.stats.FirstH264AccessUnitMS == 0 && h264RTPPayloadHasFrameCandidate(packet.Payload, packet.Marker) {
				s.stats.FirstH264AccessUnitMS = nowMS
			}
		}
		if isOpus {
			s.stats.OpusPackets++
			s.stats.OpusFrames++
			s.stats.OpusBytes += len(packet.Payload)
			if s.stats.FirstOpusRTPMS == 0 {
				s.stats.FirstOpusRTPMS = time.Since(s.started).Milliseconds()
			}
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
	return len(types) > 0
}

type iceTraceStatsRecorder func(func(*WebRTCMediaStats))

func installICETrace(peer *webrtc.PeerConnection, started time.Time, policy webrtc.ICETransportPolicy, recordGathering func(string), recordCandidate func(candidateTrace), recordConnection func(string)) {
	peer.OnICEGatheringStateChange(func(state webrtc.ICEGathererState) {
		recordGathering(state.String())
	})
	peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			recordGathering("complete")
			return
		}
		recordCandidate(candidateTraceFromCandidateLine(candidate.ToJSON().Candidate))
	})
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		recordConnection(state.String())
		_ = started
		_ = policy
	})
}

func installSelectedCandidatePairTrace(peer *webrtc.PeerConnection, recordPair func(selectedCandidatePairEvidence)) {
	if peer == nil || recordPair == nil {
		return
	}
	seen := map[*webrtc.ICETransport]bool{}
	for _, transceiver := range peer.GetTransceivers() {
		if transceiver == nil {
			continue
		}
		if receiver := transceiver.Receiver(); receiver != nil {
			installSelectedCandidatePairTraceFromDTLS(receiver.Transport(), seen, recordPair)
		}
		if sender := transceiver.Sender(); sender != nil {
			installSelectedCandidatePairTraceFromDTLS(sender.Transport(), seen, recordPair)
		}
	}
}

func installSelectedCandidatePairTraceFromDTLS(dtls *webrtc.DTLSTransport, seen map[*webrtc.ICETransport]bool, recordPair func(selectedCandidatePairEvidence)) {
	if dtls == nil || dtls.ICETransport() == nil {
		return
	}
	iceTransport := dtls.ICETransport()
	if seen[iceTransport] {
		return
	}
	seen[iceTransport] = true
	iceTransport.OnSelectedCandidatePairChange(func(pair *webrtc.ICECandidatePair) {
		recordPair(selectedCandidatePairTraceFromPair(pair))
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

func (s *PionMediaOfferSession) recordICECandidate(candidate candidateTrace) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICECandidate(stats, candidate, s.started)
	})
	if candidate.Type == "relay" {
		s.relayOnce.Do(func() { close(s.localRelayCandidate) })
	}
}

func (s *PionMediaOfferSession) recordICEConnectionState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEConnectionState(stats, state, s.started)
	})
}

func (s *PionMediaOfferSession) recordPeerConnectionState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordPeerConnectionState(stats, state, s.started)
	})
}

func (s *PionMediaOfferSession) recordSelectedCandidatePairChange(pair selectedCandidatePairEvidence) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordSelectedCandidatePairChange(stats, pair, s.icePolicy, s.started)
	})
}

func (s *PionMediaOfferSession) recordTrackArrived(track *webrtc.TrackRemote) {
	if track == nil {
		return
	}
	codec := track.Codec()
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		if stats.TrackArrivedMS == 0 {
			stats.TrackArrivedMS = time.Since(s.started).Milliseconds()
			stats.TrackKind = track.Kind().String()
			stats.TrackCodec = codec.MimeType
		}
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

func (s *PionMediaAnswerSession) recordICECandidate(candidate candidateTrace) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICECandidate(stats, candidate, s.started)
	})
	if candidate.Type == "relay" {
		s.relayOnce.Do(func() { close(s.localRelayCandidate) })
	}
}

func (s *PionMediaAnswerSession) recordICEConnectionState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordICEConnectionState(stats, state, s.started)
	})
}

func (s *PionMediaAnswerSession) recordPeerConnectionState(state string) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordPeerConnectionState(stats, state, s.started)
	})
}

func (s *PionMediaAnswerSession) recordSelectedCandidatePairChange(pair selectedCandidatePairEvidence) {
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		recordSelectedCandidatePairChange(stats, pair, s.icePolicy, s.started)
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

func recordICECandidate(stats *WebRTCMediaStats, candidate candidateTrace, started time.Time) {
	nowMS := time.Since(started).Milliseconds()
	if stats.FirstLocalCandidateMS == 0 {
		stats.FirstLocalCandidateMS = nowMS
	}
	switch candidate.Protocol {
	case "udp":
		stats.LocalUDPCandidates++
	case "tcp":
		stats.LocalTCPCandidates++
	}
	switch candidate.Type {
	case "host":
		stats.LocalHostCandidates++
	case "srflx":
		stats.LocalSrflxCandidates++
	case "relay":
		stats.LocalRelayCandidates++
		if stats.FirstLocalRelayCandidateMS == 0 {
			stats.FirstLocalRelayCandidateMS = nowMS
		}
		switch candidate.Protocol {
		case "udp":
			stats.LocalRelayUDPCandidates++
			if stats.FirstLocalRelayUDPCandidateMS == 0 {
				stats.FirstLocalRelayUDPCandidateMS = nowMS
			}
		case "tcp":
			stats.LocalRelayTCPCandidates++
			if stats.FirstLocalRelayTCPCandidateMS == 0 {
				stats.FirstLocalRelayTCPCandidateMS = nowMS
			}
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

func recordPeerConnectionState(stats *WebRTCMediaStats, state string, started time.Time) {
	nowMS := time.Since(started).Milliseconds()
	if len(stats.PeerConnectionStates) > 0 {
		last := stats.PeerConnectionStates[len(stats.PeerConnectionStates)-1]
		if strings.HasPrefix(last, state+"@") {
			return
		}
	}
	stats.PeerConnectionStates = append(stats.PeerConnectionStates, fmt.Sprintf("%s@%d", state, nowMS))
	if state == webrtc.PeerConnectionStateConnected.String() && stats.PeerConnectionConnectedMS == 0 {
		stats.PeerConnectionConnectedMS = nowMS
	}
}

func recordSelectedCandidatePairChange(stats *WebRTCMediaStats, pair selectedCandidatePairEvidence, policy webrtc.ICETransportPolicy, started time.Time) {
	nowMS := time.Since(started).Milliseconds()
	stats.ICESelectedPairChanges++
	if stats.ICESelectedPairFirstChangeMS == 0 {
		stats.ICESelectedPairFirstChangeMS = nowMS
	}
	stats.ICESelectedPairLastChangeMS = nowMS
	applySelectedCandidatePairTrace(stats, pair, policy)
}

func applySelectedCandidatePairTrace(stats *WebRTCMediaStats, pair selectedCandidatePairEvidence, policy webrtc.ICETransportPolicy) {
	stats.SelectedLocalCandidateType = candidateTypeEvidence(pair.LocalType, policy)
	stats.SelectedRemoteCandidateType = candidateTypeEvidence(pair.RemoteType, policy)
	stats.SelectedLocalCandidateProtocol = pair.LocalProtocol
	stats.SelectedRemoteCandidateProtocol = pair.RemoteProtocol
	stats.SelectedLocalCandidateAddress = pair.LocalAddress
	stats.SelectedRemoteCandidateAddress = pair.RemoteAddress
	stats.SelectedLocalCandidatePort = pair.LocalPort
	stats.SelectedRemoteCandidatePort = pair.RemotePort
	stats.SelectedLocalCandidateTCPType = pair.LocalTCPType
	stats.SelectedRemoteCandidateTCPType = pair.RemoteTCPType
}

type selectedCandidatePairStatsEvidence struct {
	RequestsSent            uint64
	RequestsReceived        uint64
	ResponsesSent           uint64
	ResponsesReceived       uint64
	RetransmissionsSent     uint64
	RetransmissionsReceived uint64
	ConsentRequestsSent     uint64
	CurrentRoundTripTimeMS  int64
}

func recordSelectedCandidatePairStats(stats *WebRTCMediaStats, pairStats selectedCandidatePairStatsEvidence) {
	stats.ICERequestsSent = pairStats.RequestsSent
	stats.ICERequestsReceived = pairStats.RequestsReceived
	stats.ICEResponsesSent = pairStats.ResponsesSent
	stats.ICEResponsesReceived = pairStats.ResponsesReceived
	stats.ICERetransmissionsSent = pairStats.RetransmissionsSent
	stats.ICERetransmissionsReceived = pairStats.RetransmissionsReceived
	stats.ICEConsentRequestsSent = pairStats.ConsentRequestsSent
	stats.ICECurrentRoundTripTimeMS = pairStats.CurrentRoundTripTimeMS
}

type candidateTrace struct {
	Type     string
	Protocol string
}

func candidateTraceFromCandidateLine(candidate string) candidateTrace {
	fields := strings.Fields(candidate)
	trace := candidateTrace{}
	if len(fields) > 2 {
		trace.Protocol = strings.ToLower(strings.TrimSpace(fields[2]))
	}
	for i, field := range fields {
		if field == "typ" && i+1 < len(fields) {
			trace.Type = strings.ToLower(strings.TrimSpace(fields[i+1]))
			break
		}
	}
	return trace
}

func candidateTypeFromCandidateLine(candidate string) string {
	return candidateTraceFromCandidateLine(candidate).Type
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
	phaseStarted := time.Now()
	if err := s.peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer["sdp"]}); err != nil {
		return err
	}
	s.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionSetRemoteDescriptionMS = time.Since(phaseStarted).Milliseconds()
	})
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
	if s.stats.SelectedLocalCandidateType == "" || s.stats.SelectedRemoteCandidateType == "" || s.stats.SelectedLocalCandidateProtocol == "" || s.stats.SelectedRemoteCandidateProtocol == "" {
		pair := selectedCandidatePairTrace(s.peer)
		applySelectedCandidatePairTrace(&s.stats, pair, s.icePolicy)
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
	sessionStarted := time.Now()
	phaseStarted := time.Now()
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers, ICETransportPolicy: icePolicy})
	if err != nil {
		return nil, fmt.Errorf("pion media answer peer connection: %w", err)
	}
	createPeerMS := time.Since(phaseStarted).Milliseconds()
	session := &PionMediaAnswerSession{
		peer:                peer,
		icePolicy:           icePolicy,
		started:             sessionStarted,
		stats:               WebRTCMediaStats{PionCreatePeerMS: createPeerMS},
		iceConnected:        make(chan struct{}),
		peerConnected:       make(chan struct{}),
		localRelayCandidate: make(chan struct{}),
	}
	installICETrace(peer, session.started, session.icePolicy, session.recordICEGatheringState, session.recordICECandidate, session.recordICEConnectionState)
	peer.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		session.recordICEConnectionState(state.String())
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			session.iceOnce.Do(func() {
				session.mu.Lock()
				session.stats.ICEConnectedLatencyMS = time.Since(session.started).Milliseconds()
				pair := selectedCandidatePairTrace(peer)
				applySelectedCandidatePairTrace(&session.stats, pair, session.icePolicy)
				session.mu.Unlock()
				close(session.iceConnected)
			})
		}
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		session.recordPeerConnectionState(state.String())
		if state == webrtc.PeerConnectionStateConnected {
			session.peerOnce.Do(func() {
				session.recordICETrace(func(stats *WebRTCMediaStats) {
					stats.PeerConnectionConnectedMS = time.Since(session.started).Milliseconds()
				})
				close(session.peerConnected)
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
	installSelectedCandidatePairTrace(peer, session.recordSelectedCandidatePairChange)
	session.codecMime = webrtc.MimeTypeH264
	phaseStarted = time.Now()
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer["sdp"]}); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media answer set remote offer: %w", err)
	}
	session.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionSetRemoteDescriptionMS = time.Since(phaseStarted).Milliseconds()
	})
	session.recordRemoteDescriptionSet()
	phaseStarted = time.Now()
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media create answer: %w", err)
	}
	session.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionCreateAnswerMS = time.Since(phaseStarted).Milliseconds()
	})
	gatherComplete := webrtc.GatheringCompletePromise(peer)
	phaseStarted = time.Now()
	if err := peer.SetLocalDescription(answer); err != nil {
		_ = peer.Close()
		return nil, fmt.Errorf("pion media set local answer: %w", err)
	}
	session.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionSetLocalDescriptionMS = time.Since(phaseStarted).Milliseconds()
	})
	session.recordLocalDescriptionSet()
	phaseStarted = time.Now()
	if err := waitICEGatheringReady(ctx, gatherComplete, session.localRelayCandidate, icePolicy, gatherTimeout); err != nil {
		_ = peer.Close()
		return nil, err
	}
	session.recordICETrace(func(stats *WebRTCMediaStats) {
		stats.PionICEGatheringWaitMS = time.Since(phaseStarted).Milliseconds()
	})
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
	case <-time.After(defaultWebRTCICERequiredWait):
		return errors.New("webrtc media answerer ICE connection timeout")
	}
	if err := s.waitPeerConnected(ctx); err != nil {
		return err
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
	sent.Evidence = sent.Evidence.WithICEStats(s.Snapshot())
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
		sent, err := s.sendOpusPlan(ctx, audioPlan)
		audioPlan = sent
		errCh <- err
	}()
	go func() {
		sent, err := s.sendH264Plan(ctx, videoPlan)
		videoPlan = sent
		errCh <- err
	}()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			return AVRTPEvidence{}, err
		}
	}
	receiveMS := time.Since(s.started).Milliseconds()
	finalStats := s.Snapshot()
	videoPlan.Evidence = videoPlan.Evidence.WithICEStats(finalStats)
	audioPlan.Evidence = audioPlan.Evidence.WithICEStats(finalStats)
	videoPlan.Evidence.ReceiveMS = receiveMS
	audioPlan.Evidence.ReceiveMS = receiveMS
	return AVRTPEvidence{Video: videoPlan.Evidence, Audio: audioPlan.Evidence}, nil
}

var (
	h264MediaPlanCache sync.Map
	h264MediaPlanMu    sync.Mutex
	h264PlanBuildHook  func(time.Duration)

	opusMediaPlanCache sync.Map
	opusMediaPlanMu    sync.Mutex
	opusPlanBuildHook  func(time.Duration)
)

func waitWebRTCMediaSettle(ctx context.Context) error {
	if defaultWebRTCMediaSettle <= 0 {
		return nil
	}
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
	case <-time.After(defaultWebRTCICERequiredWait):
		return errors.New("webrtc media answerer ICE connection timeout")
	}
}

func (s *PionMediaAnswerSession) waitPeerConnected(ctx context.Context) error {
	if s.peer.ConnectionState() == webrtc.PeerConnectionStateConnected {
		return nil
	}
	select {
	case <-s.peerConnected:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(defaultWebRTCPeerRequiredWait):
		return errors.New("webrtc media answerer peer connection timeout")
	}
}

func (s *PionMediaAnswerSession) waitPeerConnectedBestEffort(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 || s.peer.ConnectionState() == webrtc.PeerConnectionStateConnected {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.peerConnected:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *PionMediaAnswerSession) Snapshot() WebRTCMediaStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stats.SelectedLocalCandidateType == "" || s.stats.SelectedRemoteCandidateType == "" || s.stats.SelectedLocalCandidateProtocol == "" || s.stats.SelectedRemoteCandidateProtocol == "" {
		pair := selectedCandidatePairTrace(s.peer)
		applySelectedCandidatePairTrace(&s.stats, pair, s.icePolicy)
	}
	return s.stats
}

func (s *PionMediaAnswerSession) sendH264Plan(ctx context.Context, plan H264RTPPlan) (H264RTPPlan, error) {
	var frames [][]*rtp.Packet
	if len(plan.FramePackets) > 0 {
		frames = mediaFramesFromH264(plan.FramePackets)
	} else {
		frames = mediaFramesFromPackets(plan.Packets)
	}
	stats, err := sharedMediaPacer.Send(ctx, mediaPacedSendRequest{
		Label:    "h264",
		Frames:   frames,
		Duration: plan.Duration,
		WriteRTP: s.videoTrack.WriteRTP,
	})
	plan.Evidence = plan.Evidence.WithSchedulerStats(stats)
	if !stats.FirstWriteCallAt.IsZero() {
		plan.Evidence.SchedulerFirstWriteCallSinceStartMS = nonNegativeMS(stats.FirstWriteCallAt.Sub(s.started).Milliseconds())
	}
	if !stats.FirstWriteReturnAt.IsZero() {
		plan.Evidence.SchedulerFirstWriteReturnSinceStartMS = nonNegativeMS(stats.FirstWriteReturnAt.Sub(s.started).Milliseconds())
	}
	if !stats.FirstWriteAt.IsZero() {
		firstWriteSinceStart := nonNegativeMS(stats.FirstWriteAt.Sub(s.started).Milliseconds())
		plan.Evidence.SchedulerFirstWriteSinceStartMS = firstWriteSinceStart
		plan.Evidence.TimeToFirstMS = nonNegativeMS(firstWriteSinceStart - plan.Evidence.ICEMS)
	}
	if err != nil {
		return plan, err
	}
	return plan, nil
}

func (s *PionMediaAnswerSession) sendOpusPlan(ctx context.Context, plan OpusRTPPlan) (OpusRTPPlan, error) {
	if s.audioTrack == nil {
		return OpusRTPPlan{}, errors.New("webrtc media answerer missing Opus audio track")
	}
	stats, err := sharedMediaPacer.Send(ctx, mediaPacedSendRequest{
		Label:    "opus",
		Frames:   mediaFramesFromPackets(plan.Packets),
		Duration: plan.Duration,
		WriteRTP: s.audioTrack.WriteRTP,
	})
	plan.Evidence = plan.Evidence.WithSchedulerStats(stats)
	if !stats.FirstWriteCallAt.IsZero() {
		plan.Evidence.SchedulerFirstWriteCallSinceStartMS = nonNegativeMS(stats.FirstWriteCallAt.Sub(s.started).Milliseconds())
	}
	if !stats.FirstWriteReturnAt.IsZero() {
		plan.Evidence.SchedulerFirstWriteReturnSinceStartMS = nonNegativeMS(stats.FirstWriteReturnAt.Sub(s.started).Milliseconds())
	}
	if !stats.FirstWriteAt.IsZero() {
		firstWriteSinceStart := nonNegativeMS(stats.FirstWriteAt.Sub(s.started).Milliseconds())
		plan.Evidence.SchedulerFirstWriteSinceStartMS = firstWriteSinceStart
		plan.Evidence.TimeToFirstMS = nonNegativeMS(firstWriteSinceStart - plan.Evidence.ICEMS)
	}
	if err != nil {
		return plan, err
	}
	return plan, nil
}

func buildH264MediaPlan(duration time.Duration) (H264RTPPlan, error) {
	if duration <= 0 {
		duration = 20 * time.Second
	}
	cacheKey := duration.String()
	if cached, ok := h264MediaPlanCache.Load(cacheKey); ok {
		return cached.(H264RTPPlan), nil
	}
	h264MediaPlanMu.Lock()
	defer h264MediaPlanMu.Unlock()
	if cached, ok := h264MediaPlanCache.Load(cacheKey); ok {
		return cached.(H264RTPPlan), nil
	}
	if h264PlanBuildHook != nil {
		h264PlanBuildHook(duration)
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
	allFrames := make([]H264RTPFrame, 0, loops*int(fixtureDuration.Seconds())*frameRate)
	evidence := H264RTPEvidence{
		DurationMS:     duration.Milliseconds(),
		Loops:          loops,
		Frames:         int(duration.Seconds() * frameRate),
		NALTypes:       map[string]bool{},
		Packetizations: map[string]bool{},
	}
	for i := 0; i < loops; i++ {
		frames, loopEvidence, err := packetizeH264AnnexBFramesWithPacketizer(sample, packetizer, frameRate, fixtureDuration)
		if err != nil {
			return H264RTPPlan{}, err
		}
		for _, frame := range frames {
			allPackets = append(allPackets, frame.Packets...)
		}
		allFrames = append(allFrames, frames...)
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
	plan := H264RTPPlan{Duration: duration, Loops: loops, FrameRate: frameRate, Frames: evidence.Frames, FramePackets: allFrames, Packets: allPackets, Evidence: evidence}
	h264MediaPlanCache.Store(cacheKey, plan)
	return plan, nil
}

func buildOpusMediaPlan(duration time.Duration) (OpusRTPPlan, error) {
	if duration <= 0 {
		duration = 20 * time.Second
	}
	cacheKey := duration.String()
	if cached, ok := opusMediaPlanCache.Load(cacheKey); ok {
		return cached.(OpusRTPPlan), nil
	}
	opusMediaPlanMu.Lock()
	defer opusMediaPlanMu.Unlock()
	if cached, ok := opusMediaPlanCache.Load(cacheKey); ok {
		return cached.(OpusRTPPlan), nil
	}
	if opusPlanBuildHook != nil {
		opusPlanBuildHook(duration)
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
		Packets:    len(allPackets),
		Bytes:      payloads.Len(),
		DurationMS: duration.Milliseconds(),
		Loops:      loops,
		Frames:     len(allPackets),
		SampleRate: sampleRate,
		Channels:   channels,
	}
	plan := OpusRTPPlan{Duration: duration, Loops: loops, SampleRate: sampleRate, Channels: channels, Frames: len(allPackets), Packets: allPackets, Evidence: evidence}
	opusMediaPlanCache.Store(cacheKey, plan)
	return plan, nil
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
	frames, evidence, err := packetizeH264AnnexBFramesWithPacketizer(sample, packetizer, 30, 2*time.Second)
	if err != nil {
		return nil, H264RTPEvidence{}, err
	}
	packets := make([]*rtp.Packet, 0, evidence.Packets)
	for _, frame := range frames {
		packets = append(packets, frame.Packets...)
	}
	return packets, evidence, nil
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

func packetizeH264AnnexBFramesWithPacketizer(sample []byte, packetizer rtp.Packetizer, frameRate int, fixtureDuration time.Duration) ([]H264RTPFrame, H264RTPEvidence, error) {
	nals, err := h264NALUnits(sample)
	if err != nil {
		return nil, H264RTPEvidence{}, err
	}
	nalFrames, err := h264AccessUnitNALGroups(nals, frameRate, fixtureDuration)
	if err != nil {
		return nil, H264RTPEvidence{}, err
	}
	evidence := H264RTPEvidence{NALTypes: map[string]bool{}, Packetizations: map[string]bool{}}
	frames := make([]H264RTPFrame, 0, len(nalFrames))
	for _, nalFrame := range nalFrames {
		frame := H264RTPFrame{}
		for i, nal := range nalFrame {
			if len(nal) == 0 {
				continue
			}
			nalType := h264NALTypeName(nal[0] & 0x1f)
			if nalType != "" {
				evidence.NALTypes[nalType] = true
			}
			samples := uint32(0)
			if i == len(nalFrame)-1 {
				samples = 3000
			}
			rtpPackets := packetizer.Packetize(nal, samples)
			for _, packet := range rtpPackets {
				packet.Marker = false
				evidence.Packets++
				evidence.Bytes += len(packet.Payload)
				for _, packetization := range h264PayloadPacketizations(packet.Payload) {
					evidence.Packetizations[packetization] = true
				}
				frame.Packets = append(frame.Packets, packet)
			}
		}
		if len(frame.Packets) == 0 {
			continue
		}
		frame.Packets[len(frame.Packets)-1].Marker = true
		frames = append(frames, frame)
	}
	if evidence.Packets == 0 {
		return nil, H264RTPEvidence{}, errors.New("H.264 fixture produced no RTP packets")
	}
	return frames, evidence, nil
}

func h264AccessUnitNALGroups(nals [][]byte, frameRate int, fixtureDuration time.Duration) ([][][]byte, error) {
	if frameRate <= 0 || fixtureDuration <= 0 {
		return nil, errors.New("invalid H.264 frame grouping parameters")
	}
	expectedFrames := int(fixtureDuration.Seconds() * float64(frameRate))
	if expectedFrames <= 0 {
		return nil, errors.New("invalid H.264 expected frame count")
	}
	vclCount := 0
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1f {
		case 1, 5:
			vclCount++
		}
	}
	if vclCount == 0 {
		return nil, errors.New("H.264 fixture has no VCL NAL units")
	}
	if vclCount%expectedFrames != 0 {
		return nil, fmt.Errorf("H.264 VCL NAL count %d is not divisible by expected frames %d", vclCount, expectedFrames)
	}
	vclPerFrame := vclCount / expectedFrames
	frames := make([][][]byte, 0, expectedFrames)
	pending := make([][]byte, 0)
	current := make([][]byte, 0, vclPerFrame+4)
	currentVCL := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		frames = append(frames, current)
		current = nil
		currentVCL = 0
	}
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 0x1f {
		case 1, 5:
			if len(current) == 0 && len(pending) > 0 {
				current = append(current, pending...)
				pending = nil
			}
			current = append(current, nal)
			currentVCL++
			if currentVCL == vclPerFrame {
				flush()
			}
		default:
			if len(current) == 0 {
				pending = append(pending, nal)
			} else {
				current = append(current, nal)
			}
		}
	}
	if len(current) > 0 {
		flush()
	}
	if len(pending) > 0 {
		if len(frames) == 0 {
			return nil, errors.New("H.264 fixture has non-VCL NAL units but no frames")
		}
		frames[len(frames)-1] = append(frames[len(frames)-1], pending...)
	}
	if len(frames) != expectedFrames {
		return nil, fmt.Errorf("H.264 frame groups = %d, want %d", len(frames), expectedFrames)
	}
	return frames, nil
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

func h264RTPPayloadHasFrameCandidate(payload []byte, marker bool) bool {
	if len(payload) == 0 {
		return false
	}
	if len(h264NALTypesFromRTPPayload(payload)) > 0 {
		return true
	}
	return marker && len(h264PayloadPacketizations(payload)) > 0
}

func h264NALTypesFromRTPPayload(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	switch payload[0] & 0x1f {
	case 1, 5, 6, 7, 8, 9:
		if name := h264NALTypeName(payload[0] & 0x1f); name != "" {
			return []string{name}
		}
	case 24:
		return h264NALTypesFromSTAPA(payload[1:])
	case 28:
		if len(payload) < 2 {
			return nil
		}
		if name := h264NALTypeName(payload[1] & 0x1f); name != "" {
			return []string{name}
		}
	}
	return nil
}

func h264NALTypesFromSTAPA(payload []byte) []string {
	var out []string
	for len(payload) >= 2 {
		nalLen := int(payload[0])<<8 | int(payload[1])
		payload = payload[2:]
		if nalLen <= 0 || nalLen > len(payload) {
			return out
		}
		if name := h264NALTypeName(payload[0] & 0x1f); name != "" {
			out = appendUniqueString(out, name)
		}
		payload = payload[nalLen:]
	}
	return out
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

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
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
	e.PionCreatePeerMS = stats.PionCreatePeerMS
	e.PionCreateOfferMS = stats.PionCreateOfferMS
	e.PionCreateAnswerMS = stats.PionCreateAnswerMS
	e.PionSetLocalDescriptionMS = stats.PionSetLocalDescriptionMS
	e.PionSetRemoteDescriptionMS = stats.PionSetRemoteDescriptionMS
	e.PionICEGatheringWaitMS = stats.PionICEGatheringWaitMS
	e.ICECheckingMS = stats.ICECheckingMS
	e.FirstLocalCandidateMS = stats.FirstLocalCandidateMS
	e.FirstLocalRelayCandidateMS = stats.FirstLocalRelayCandidateMS
	e.FirstLocalRelayUDPCandidateMS = stats.FirstLocalRelayUDPCandidateMS
	e.FirstLocalRelayTCPCandidateMS = stats.FirstLocalRelayTCPCandidateMS
	e.LocalHostCandidates = stats.LocalHostCandidates
	e.LocalSrflxCandidates = stats.LocalSrflxCandidates
	e.LocalRelayCandidates = stats.LocalRelayCandidates
	e.LocalUDPCandidates = stats.LocalUDPCandidates
	e.LocalTCPCandidates = stats.LocalTCPCandidates
	e.LocalRelayUDPCandidates = stats.LocalRelayUDPCandidates
	e.LocalRelayTCPCandidates = stats.LocalRelayTCPCandidates
	e.ICEConnectionStates = append([]string(nil), stats.ICEConnectionStates...)
	e.ICEGatheringStates = append([]string(nil), stats.ICEGatheringStates...)
	e.PeerConnectionStates = append([]string(nil), stats.PeerConnectionStates...)
	e.PeerConnectionConnectedMS = stats.PeerConnectionConnectedMS
	e.ICESelectedPairChanges = stats.ICESelectedPairChanges
	e.ICESelectedPairFirstChangeMS = stats.ICESelectedPairFirstChangeMS
	e.ICESelectedPairLastChangeMS = stats.ICESelectedPairLastChangeMS
	e.ICERequestsSent = stats.ICERequestsSent
	e.ICERequestsReceived = stats.ICERequestsReceived
	e.ICEResponsesSent = stats.ICEResponsesSent
	e.ICEResponsesReceived = stats.ICEResponsesReceived
	e.ICERetransmissionsSent = stats.ICERetransmissionsSent
	e.ICERetransmissionsReceived = stats.ICERetransmissionsReceived
	e.ICEConsentRequestsSent = stats.ICEConsentRequestsSent
	e.ICECurrentRoundTripTimeMS = stats.ICECurrentRoundTripTimeMS
	e.SelectedLocalCandidateType = stats.SelectedLocalCandidateType
	e.SelectedRemoteCandidateType = stats.SelectedRemoteCandidateType
	e.SelectedLocalCandidateProtocol = stats.SelectedLocalCandidateProtocol
	e.SelectedRemoteCandidateProtocol = stats.SelectedRemoteCandidateProtocol
	e.SelectedLocalCandidateAddress = stats.SelectedLocalCandidateAddress
	e.SelectedRemoteCandidateAddress = stats.SelectedRemoteCandidateAddress
	e.SelectedLocalCandidatePort = stats.SelectedLocalCandidatePort
	e.SelectedRemoteCandidatePort = stats.SelectedRemoteCandidatePort
	e.SelectedLocalCandidateTCPType = stats.SelectedLocalCandidateTCPType
	e.SelectedRemoteCandidateTCPType = stats.SelectedRemoteCandidateTCPType
	return e
}

func (e H264RTPEvidence) WithSchedulerStats(stats mediaPacedSendStats) H264RTPEvidence {
	e.SchedulerDroppedJobs = stats.DroppedJobs
	e.SchedulerDroppedPackets = stats.DroppedPackets
	e.SchedulerQueueFullDrops = stats.QueueFullDrops
	e.SchedulerZeroByteWrites = stats.ZeroByteWrites
	e.SchedulerWriteAttempts = stats.WriteAttempts
	e.SchedulerWriteReturns = stats.WriteReturns
	e.SchedulerWriteErrors = stats.WriteErrors
	e.SchedulerPacketsSent = stats.PacketsSent
	e.SchedulerBytesSent = stats.BytesSent
	e.SchedulerStartedAtMS = schedulerOffsetMS(stats.StartedAt, stats.StartedAt)
	e.SchedulerFirstWriteCallMS = schedulerOffsetMS(stats.FirstWriteCallAt, stats.StartedAt)
	e.SchedulerFirstWriteReturnMS = schedulerOffsetMS(stats.FirstWriteReturnAt, stats.StartedAt)
	e.SchedulerFirstWriteMS = schedulerOffsetMS(stats.FirstWriteAt, stats.StartedAt)
	e.SchedulerEndedAtMS = schedulerOffsetMS(stats.EndedAt, stats.StartedAt)
	e.SchedulerMaxWriteMS = stats.MaxWriteLatency.Milliseconds()
	if !stats.StartedAt.IsZero() && !stats.EndedAt.IsZero() {
		e.SchedulerSendDurationMS = stats.EndedAt.Sub(stats.StartedAt).Milliseconds()
	}
	return e
}

func (e H264RTPEvidence) String() string {
	base := fmt.Sprintf("codec=h264 packets=%d bytes=%d duration_ms=%d loops=%d frames=%d nal_types=%s packetization=%s receive_ms=%d ttfb_ms=%d ice_ms=%d selected_local_candidate_type=%s selected_remote_candidate_type=%s selected_local_candidate_protocol=%s selected_remote_candidate_protocol=%s selected_local_candidate_address=%s selected_remote_candidate_address=%s selected_local_candidate_port=%d selected_remote_candidate_port=%d selected_local_candidate_tcp_type=%s selected_remote_candidate_tcp_type=%s",
		e.Packets, e.Bytes, e.DurationMS, e.Loops, e.Frames, joinEvidenceKeys(e.NALTypes), joinEvidenceKeys(e.Packetizations), e.ReceiveMS, e.TimeToFirstMS, e.ICEMS, evidenceOrDefault(e.SelectedLocalCandidateType, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateType, "unknown"), evidenceOrDefault(e.SelectedLocalCandidateProtocol, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateProtocol, "unknown"), evidenceOrDefault(e.SelectedLocalCandidateAddress, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateAddress, "unknown"), e.SelectedLocalCandidatePort, e.SelectedRemoteCandidatePort, evidenceOrDefault(e.SelectedLocalCandidateTCPType, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateTCPType, "unknown"))
	base = appendICETraceEvidence(base, e.ICEGatheringCompleteMS, e.RemoteDescriptionSetMS, e.LocalDescriptionSetMS, e.ICECheckingMS, e.FirstLocalCandidateMS, e.FirstLocalRelayCandidateMS, e.FirstLocalRelayUDPCandidateMS, e.FirstLocalRelayTCPCandidateMS, e.LocalHostCandidates, e.LocalSrflxCandidates, e.LocalRelayCandidates, e.LocalUDPCandidates, e.LocalTCPCandidates, e.LocalRelayUDPCandidates, e.LocalRelayTCPCandidates, e.SelectedLocalCandidateType, e.SelectedRemoteCandidateType, e.SelectedLocalCandidateProtocol, e.SelectedRemoteCandidateProtocol, e.ICEConnectionStates, e.ICEGatheringStates)
	base = appendICEPairStatsEvidence(base, e.ICESelectedPairChanges, e.ICESelectedPairFirstChangeMS, e.ICESelectedPairLastChangeMS, e.ICERequestsSent, e.ICERequestsReceived, e.ICEResponsesSent, e.ICEResponsesReceived, e.ICERetransmissionsSent, e.ICERetransmissionsReceived, e.ICEConsentRequestsSent, e.ICECurrentRoundTripTimeMS)
	base = appendPeerConnectionEvidence(base, e.PeerConnectionConnectedMS, e.PeerConnectionStates)
	base = appendPionPhaseEvidence(base, e.PionCreatePeerMS, e.PionCreateOfferMS, e.PionCreateAnswerMS, e.PionSetLocalDescriptionMS, e.PionSetRemoteDescriptionMS, e.PionICEGatheringWaitMS)
	base += fmt.Sprintf(" scheduler_packets_sent=%d scheduler_bytes_sent=%d scheduler_dropped_jobs=%d scheduler_dropped_packets=%d scheduler_queue_full_drops=%d scheduler_zero_byte_writes=%d scheduler_write_attempts=%d scheduler_write_returns=%d scheduler_write_errors=%d scheduler_first_write_call_ms=%d scheduler_first_write_return_ms=%d scheduler_first_write_call_since_session_start_ms=%d scheduler_first_write_return_since_session_start_ms=%d scheduler_write_max_ms=%d scheduler_started_ms=%d scheduler_first_write_ms=%d scheduler_first_write_since_session_start_ms=%d scheduler_ended_ms=%d scheduler_send_duration_ms=%d",
		e.SchedulerPacketsSent, e.SchedulerBytesSent, e.SchedulerDroppedJobs, e.SchedulerDroppedPackets, e.SchedulerQueueFullDrops, e.SchedulerZeroByteWrites, e.SchedulerWriteAttempts, e.SchedulerWriteReturns, e.SchedulerWriteErrors, e.SchedulerFirstWriteCallMS, e.SchedulerFirstWriteReturnMS, e.SchedulerFirstWriteCallSinceStartMS, e.SchedulerFirstWriteReturnSinceStartMS, e.SchedulerMaxWriteMS, e.SchedulerStartedAtMS, e.SchedulerFirstWriteMS, e.SchedulerFirstWriteSinceStartMS, e.SchedulerEndedAtMS, e.SchedulerSendDurationMS)
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
	e.PionCreatePeerMS = stats.PionCreatePeerMS
	e.PionCreateOfferMS = stats.PionCreateOfferMS
	e.PionCreateAnswerMS = stats.PionCreateAnswerMS
	e.PionSetLocalDescriptionMS = stats.PionSetLocalDescriptionMS
	e.PionSetRemoteDescriptionMS = stats.PionSetRemoteDescriptionMS
	e.PionICEGatheringWaitMS = stats.PionICEGatheringWaitMS
	e.ICECheckingMS = stats.ICECheckingMS
	e.FirstLocalCandidateMS = stats.FirstLocalCandidateMS
	e.FirstLocalRelayCandidateMS = stats.FirstLocalRelayCandidateMS
	e.FirstLocalRelayUDPCandidateMS = stats.FirstLocalRelayUDPCandidateMS
	e.FirstLocalRelayTCPCandidateMS = stats.FirstLocalRelayTCPCandidateMS
	e.LocalHostCandidates = stats.LocalHostCandidates
	e.LocalSrflxCandidates = stats.LocalSrflxCandidates
	e.LocalRelayCandidates = stats.LocalRelayCandidates
	e.LocalUDPCandidates = stats.LocalUDPCandidates
	e.LocalTCPCandidates = stats.LocalTCPCandidates
	e.LocalRelayUDPCandidates = stats.LocalRelayUDPCandidates
	e.LocalRelayTCPCandidates = stats.LocalRelayTCPCandidates
	e.ICEConnectionStates = append([]string(nil), stats.ICEConnectionStates...)
	e.ICEGatheringStates = append([]string(nil), stats.ICEGatheringStates...)
	e.PeerConnectionStates = append([]string(nil), stats.PeerConnectionStates...)
	e.PeerConnectionConnectedMS = stats.PeerConnectionConnectedMS
	e.ICESelectedPairChanges = stats.ICESelectedPairChanges
	e.ICESelectedPairFirstChangeMS = stats.ICESelectedPairFirstChangeMS
	e.ICESelectedPairLastChangeMS = stats.ICESelectedPairLastChangeMS
	e.ICERequestsSent = stats.ICERequestsSent
	e.ICERequestsReceived = stats.ICERequestsReceived
	e.ICEResponsesSent = stats.ICEResponsesSent
	e.ICEResponsesReceived = stats.ICEResponsesReceived
	e.ICERetransmissionsSent = stats.ICERetransmissionsSent
	e.ICERetransmissionsReceived = stats.ICERetransmissionsReceived
	e.ICEConsentRequestsSent = stats.ICEConsentRequestsSent
	e.ICECurrentRoundTripTimeMS = stats.ICECurrentRoundTripTimeMS
	e.SelectedLocalCandidateType = stats.SelectedLocalCandidateType
	e.SelectedRemoteCandidateType = stats.SelectedRemoteCandidateType
	e.SelectedLocalCandidateProtocol = stats.SelectedLocalCandidateProtocol
	e.SelectedRemoteCandidateProtocol = stats.SelectedRemoteCandidateProtocol
	e.SelectedLocalCandidateAddress = stats.SelectedLocalCandidateAddress
	e.SelectedRemoteCandidateAddress = stats.SelectedRemoteCandidateAddress
	e.SelectedLocalCandidatePort = stats.SelectedLocalCandidatePort
	e.SelectedRemoteCandidatePort = stats.SelectedRemoteCandidatePort
	e.SelectedLocalCandidateTCPType = stats.SelectedLocalCandidateTCPType
	e.SelectedRemoteCandidateTCPType = stats.SelectedRemoteCandidateTCPType
	return e
}

func (e OpusRTPEvidence) WithSchedulerStats(stats mediaPacedSendStats) OpusRTPEvidence {
	e.SchedulerDroppedJobs = stats.DroppedJobs
	e.SchedulerDroppedPackets = stats.DroppedPackets
	e.SchedulerQueueFullDrops = stats.QueueFullDrops
	e.SchedulerZeroByteWrites = stats.ZeroByteWrites
	e.SchedulerWriteAttempts = stats.WriteAttempts
	e.SchedulerWriteReturns = stats.WriteReturns
	e.SchedulerWriteErrors = stats.WriteErrors
	e.SchedulerPacketsSent = stats.PacketsSent
	e.SchedulerBytesSent = stats.BytesSent
	e.SchedulerStartedAtMS = schedulerOffsetMS(stats.StartedAt, stats.StartedAt)
	e.SchedulerFirstWriteCallMS = schedulerOffsetMS(stats.FirstWriteCallAt, stats.StartedAt)
	e.SchedulerFirstWriteReturnMS = schedulerOffsetMS(stats.FirstWriteReturnAt, stats.StartedAt)
	e.SchedulerFirstWriteMS = schedulerOffsetMS(stats.FirstWriteAt, stats.StartedAt)
	e.SchedulerEndedAtMS = schedulerOffsetMS(stats.EndedAt, stats.StartedAt)
	e.SchedulerMaxWriteMS = stats.MaxWriteLatency.Milliseconds()
	if !stats.StartedAt.IsZero() && !stats.EndedAt.IsZero() {
		e.SchedulerSendDurationMS = stats.EndedAt.Sub(stats.StartedAt).Milliseconds()
	}
	return e
}

func (e OpusRTPEvidence) String() string {
	base := fmt.Sprintf("codec=opus packets=%d bytes=%d duration_ms=%d loops=%d frames=%d sample_rate=%d channels=%d receive_ms=%d ttfb_ms=%d ice_ms=%d selected_local_candidate_type=%s selected_remote_candidate_type=%s selected_local_candidate_protocol=%s selected_remote_candidate_protocol=%s selected_local_candidate_address=%s selected_remote_candidate_address=%s selected_local_candidate_port=%d selected_remote_candidate_port=%d selected_local_candidate_tcp_type=%s selected_remote_candidate_tcp_type=%s",
		e.Packets, e.Bytes, e.DurationMS, e.Loops, e.Frames, e.SampleRate, e.Channels, e.ReceiveMS, e.TimeToFirstMS, e.ICEMS, evidenceOrDefault(e.SelectedLocalCandidateType, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateType, "unknown"), evidenceOrDefault(e.SelectedLocalCandidateProtocol, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateProtocol, "unknown"), evidenceOrDefault(e.SelectedLocalCandidateAddress, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateAddress, "unknown"), e.SelectedLocalCandidatePort, e.SelectedRemoteCandidatePort, evidenceOrDefault(e.SelectedLocalCandidateTCPType, "unknown"), evidenceOrDefault(e.SelectedRemoteCandidateTCPType, "unknown"))
	base = appendICETraceEvidence(base, e.ICEGatheringCompleteMS, e.RemoteDescriptionSetMS, e.LocalDescriptionSetMS, e.ICECheckingMS, e.FirstLocalCandidateMS, e.FirstLocalRelayCandidateMS, e.FirstLocalRelayUDPCandidateMS, e.FirstLocalRelayTCPCandidateMS, e.LocalHostCandidates, e.LocalSrflxCandidates, e.LocalRelayCandidates, e.LocalUDPCandidates, e.LocalTCPCandidates, e.LocalRelayUDPCandidates, e.LocalRelayTCPCandidates, e.SelectedLocalCandidateType, e.SelectedRemoteCandidateType, e.SelectedLocalCandidateProtocol, e.SelectedRemoteCandidateProtocol, e.ICEConnectionStates, e.ICEGatheringStates)
	base = appendICEPairStatsEvidence(base, e.ICESelectedPairChanges, e.ICESelectedPairFirstChangeMS, e.ICESelectedPairLastChangeMS, e.ICERequestsSent, e.ICERequestsReceived, e.ICEResponsesSent, e.ICEResponsesReceived, e.ICERetransmissionsSent, e.ICERetransmissionsReceived, e.ICEConsentRequestsSent, e.ICECurrentRoundTripTimeMS)
	base = appendPeerConnectionEvidence(base, e.PeerConnectionConnectedMS, e.PeerConnectionStates)
	base = appendPionPhaseEvidence(base, e.PionCreatePeerMS, e.PionCreateOfferMS, e.PionCreateAnswerMS, e.PionSetLocalDescriptionMS, e.PionSetRemoteDescriptionMS, e.PionICEGatheringWaitMS)
	base += fmt.Sprintf(" scheduler_packets_sent=%d scheduler_bytes_sent=%d scheduler_dropped_jobs=%d scheduler_dropped_packets=%d scheduler_queue_full_drops=%d scheduler_zero_byte_writes=%d scheduler_write_attempts=%d scheduler_write_returns=%d scheduler_write_errors=%d scheduler_first_write_call_ms=%d scheduler_first_write_return_ms=%d scheduler_first_write_call_since_session_start_ms=%d scheduler_first_write_return_since_session_start_ms=%d scheduler_write_max_ms=%d scheduler_started_ms=%d scheduler_first_write_ms=%d scheduler_first_write_since_session_start_ms=%d scheduler_ended_ms=%d scheduler_send_duration_ms=%d",
		e.SchedulerPacketsSent, e.SchedulerBytesSent, e.SchedulerDroppedJobs, e.SchedulerDroppedPackets, e.SchedulerQueueFullDrops, e.SchedulerZeroByteWrites, e.SchedulerWriteAttempts, e.SchedulerWriteReturns, e.SchedulerWriteErrors, e.SchedulerFirstWriteCallMS, e.SchedulerFirstWriteReturnMS, e.SchedulerFirstWriteCallSinceStartMS, e.SchedulerFirstWriteReturnSinceStartMS, e.SchedulerMaxWriteMS, e.SchedulerStartedAtMS, e.SchedulerFirstWriteMS, e.SchedulerFirstWriteSinceStartMS, e.SchedulerEndedAtMS, e.SchedulerSendDurationMS)
	base += fmt.Sprintf(" receiver_packets=%d receiver_bytes=%d receiver_frames=%d first_opus_rtp_ms=%d",
		e.ReceiverPackets, e.ReceiverBytes, e.ReceiverFrames, e.FirstOpusRTPMS)
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

func schedulerOffsetMS(value time.Time, start time.Time) int64 {
	if value.IsZero() || start.IsZero() {
		return 0
	}
	return nonNegativeMS(value.Sub(start).Milliseconds())
}

func appendICETraceEvidence(base string, gatherCompleteMS, remoteDescriptionMS, localDescriptionMS, checkingMS, firstCandidateMS, firstRelayCandidateMS, firstRelayUDPCandidateMS, firstRelayTCPCandidateMS int64, hostCandidates, srflxCandidates, relayCandidates, udpCandidates, tcpCandidates, relayUDPCandidates, relayTCPCandidates int, selectedLocalType, selectedRemoteType, selectedLocalProtocol, selectedRemoteProtocol string, connectionStates, gatheringStates []string) string {
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
	if firstRelayUDPCandidateMS > 0 {
		parts = append(parts, fmt.Sprintf("first_local_relay_udp_candidate_ms=%d", firstRelayUDPCandidateMS))
	}
	if firstRelayTCPCandidateMS > 0 {
		parts = append(parts, fmt.Sprintf("first_local_relay_tcp_candidate_ms=%d", firstRelayTCPCandidateMS))
	}
	parts = append(parts,
		fmt.Sprintf("local_host_candidates=%d", hostCandidates),
		fmt.Sprintf("local_srflx_candidates=%d", srflxCandidates),
		fmt.Sprintf("local_relay_candidates=%d", relayCandidates),
		fmt.Sprintf("local_udp_candidates=%d", udpCandidates),
		fmt.Sprintf("local_tcp_candidates=%d", tcpCandidates),
		fmt.Sprintf("local_relay_udp_candidates=%d", relayUDPCandidates),
		fmt.Sprintf("local_relay_tcp_candidates=%d", relayTCPCandidates),
	)
	if selectedLocalType != "" {
		parts = append(parts, "selected_local_candidate_type="+selectedLocalType)
	}
	if selectedRemoteType != "" {
		parts = append(parts, "selected_remote_candidate_type="+selectedRemoteType)
	}
	if selectedLocalProtocol != "" {
		parts = append(parts, "selected_local_candidate_protocol="+selectedLocalProtocol)
	}
	if selectedRemoteProtocol != "" {
		parts = append(parts, "selected_remote_candidate_protocol="+selectedRemoteProtocol)
	}
	if len(connectionStates) > 0 {
		parts = append(parts, "ice_connection_states="+strings.Join(connectionStates, ","))
	}
	if len(gatheringStates) > 0 {
		parts = append(parts, "ice_gathering_states="+strings.Join(gatheringStates, ","))
	}
	return appendEvidence(base, strings.Join(parts, " "))
}

func appendICEPairStatsEvidence(base string, selectedPairChanges int, firstPairChangeMS, lastPairChangeMS int64, requestsSent, requestsReceived, responsesSent, responsesReceived, retransmissionsSent, retransmissionsReceived, consentRequestsSent uint64, currentRTTMS int64) string {
	parts := []string{}
	if selectedPairChanges > 0 {
		parts = append(parts, fmt.Sprintf("ice_selected_pair_changes=%d", selectedPairChanges))
	}
	if firstPairChangeMS > 0 {
		parts = append(parts, fmt.Sprintf("ice_selected_pair_first_change_ms=%d", firstPairChangeMS))
	}
	if lastPairChangeMS > 0 {
		parts = append(parts, fmt.Sprintf("ice_selected_pair_last_change_ms=%d", lastPairChangeMS))
	}
	if requestsSent > 0 {
		parts = append(parts, fmt.Sprintf("ice_requests_sent=%d", requestsSent))
	}
	if requestsReceived > 0 {
		parts = append(parts, fmt.Sprintf("ice_requests_received=%d", requestsReceived))
	}
	if responsesSent > 0 {
		parts = append(parts, fmt.Sprintf("ice_responses_sent=%d", responsesSent))
	}
	if responsesReceived > 0 {
		parts = append(parts, fmt.Sprintf("ice_responses_received=%d", responsesReceived))
	}
	if retransmissionsSent > 0 {
		parts = append(parts, fmt.Sprintf("ice_retransmissions_sent=%d", retransmissionsSent))
	}
	if retransmissionsReceived > 0 {
		parts = append(parts, fmt.Sprintf("ice_retransmissions_received=%d", retransmissionsReceived))
	}
	if consentRequestsSent > 0 {
		parts = append(parts, fmt.Sprintf("ice_consent_requests_sent=%d", consentRequestsSent))
	}
	if currentRTTMS > 0 {
		parts = append(parts, fmt.Sprintf("ice_rtt_ms=%d", currentRTTMS))
	}
	return appendEvidence(base, strings.Join(parts, " "))
}

func iceTraceEvidence(stats WebRTCMediaStats) string {
	base := appendICETraceEvidence("", stats.ICEGatheringCompleteMS, stats.RemoteDescriptionSetMS, stats.LocalDescriptionSetMS, stats.ICECheckingMS, stats.FirstLocalCandidateMS, stats.FirstLocalRelayCandidateMS, stats.FirstLocalRelayUDPCandidateMS, stats.FirstLocalRelayTCPCandidateMS, stats.LocalHostCandidates, stats.LocalSrflxCandidates, stats.LocalRelayCandidates, stats.LocalUDPCandidates, stats.LocalTCPCandidates, stats.LocalRelayUDPCandidates, stats.LocalRelayTCPCandidates, stats.SelectedLocalCandidateType, stats.SelectedRemoteCandidateType, stats.SelectedLocalCandidateProtocol, stats.SelectedRemoteCandidateProtocol, stats.ICEConnectionStates, stats.ICEGatheringStates)
	base = appendICEPairStatsEvidence(base, stats.ICESelectedPairChanges, stats.ICESelectedPairFirstChangeMS, stats.ICESelectedPairLastChangeMS, stats.ICERequestsSent, stats.ICERequestsReceived, stats.ICEResponsesSent, stats.ICEResponsesReceived, stats.ICERetransmissionsSent, stats.ICERetransmissionsReceived, stats.ICEConsentRequestsSent, stats.ICECurrentRoundTripTimeMS)
	base = appendPeerConnectionEvidence(base, stats.PeerConnectionConnectedMS, stats.PeerConnectionStates)
	base = appendPionPhaseEvidence(base, stats.PionCreatePeerMS, stats.PionCreateOfferMS, stats.PionCreateAnswerMS, stats.PionSetLocalDescriptionMS, stats.PionSetRemoteDescriptionMS, stats.PionICEGatheringWaitMS)
	return appendReceiverRTPTraceEvidence(base, stats)
}

func appendReceiverRTPTraceEvidence(base string, stats WebRTCMediaStats) string {
	parts := []string{}
	if stats.TrackArrivedMS > 0 {
		parts = append(parts, fmt.Sprintf("receiver_track_arrived_ms=%d", stats.TrackArrivedMS))
	}
	if stats.TrackKind != "" {
		parts = append(parts, "receiver_track_kind="+stats.TrackKind)
	}
	if stats.TrackCodec != "" {
		parts = append(parts, "receiver_track_codec="+stats.TrackCodec)
	}
	if stats.TimeToFirstRTPMS > 0 {
		parts = append(parts, fmt.Sprintf("receiver_first_rtp_ms=%d", stats.TimeToFirstRTPMS))
	}
	if stats.FirstRTPPayloadType > 0 {
		parts = append(parts, fmt.Sprintf("receiver_first_rtp_payload_type=%d", stats.FirstRTPPayloadType))
	}
	if stats.FirstRTPSequenceNumber > 0 {
		parts = append(parts, fmt.Sprintf("receiver_first_rtp_sequence=%d", stats.FirstRTPSequenceNumber))
	}
	if stats.FirstRTPTimestamp > 0 {
		parts = append(parts, fmt.Sprintf("receiver_first_rtp_timestamp=%d", stats.FirstRTPTimestamp))
	}
	if stats.FirstRTPSSRC > 0 {
		parts = append(parts, fmt.Sprintf("receiver_first_rtp_ssrc=%d", stats.FirstRTPSSRC))
	}
	return appendEvidence(base, strings.Join(parts, " "))
}

func appendPeerConnectionEvidence(base string, connectedMS int64, states []string) string {
	parts := []string{}
	if connectedMS > 0 {
		parts = append(parts, fmt.Sprintf("peer_connection_connected_ms=%d", connectedMS))
	}
	if len(states) > 0 {
		parts = append(parts, "peer_connection_states="+strings.Join(states, ","))
	}
	return appendEvidence(base, strings.Join(parts, " "))
}

func appendPionPhaseEvidence(base string, createPeerMS, createOfferMS, createAnswerMS, setLocalDescriptionMS, setRemoteDescriptionMS, iceGatheringWaitMS int64) string {
	parts := []string{}
	if createPeerMS > 0 {
		parts = append(parts, fmt.Sprintf("pion_create_peer_ms=%d", createPeerMS))
	}
	if createOfferMS > 0 {
		parts = append(parts, fmt.Sprintf("pion_create_offer_ms=%d", createOfferMS))
	}
	if createAnswerMS > 0 {
		parts = append(parts, fmt.Sprintf("pion_create_answer_ms=%d", createAnswerMS))
	}
	if setLocalDescriptionMS > 0 {
		parts = append(parts, fmt.Sprintf("pion_set_local_description_ms=%d", setLocalDescriptionMS))
	}
	if setRemoteDescriptionMS > 0 {
		parts = append(parts, fmt.Sprintf("pion_set_remote_description_ms=%d", setRemoteDescriptionMS))
	}
	if iceGatheringWaitMS > 0 {
		parts = append(parts, fmt.Sprintf("pion_ice_gathering_wait_ms=%d", iceGatheringWaitMS))
	}
	return appendEvidence(base, strings.Join(parts, " "))
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
	return waitICEGatheringReady(ctx, gatherComplete, nil, webrtc.ICETransportPolicyAll, timeout)
}

func waitICEGatheringReady(ctx context.Context, gatherComplete <-chan struct{}, localRelayCandidate <-chan struct{}, policy webrtc.ICETransportPolicy, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	if policy == webrtc.ICETransportPolicyRelay && localRelayCandidate != nil {
		select {
		case <-localRelayCandidate:
			return nil
		case <-gatherComplete:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("webrtc ICE relay candidate timeout")
		}
	}
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
	trace := selectedCandidatePairTrace(peer)
	return trace.LocalType, trace.RemoteType
}

type selectedCandidatePairEvidence struct {
	LocalType      string
	RemoteType     string
	LocalProtocol  string
	RemoteProtocol string
	LocalAddress   string
	RemoteAddress  string
	LocalPort      int
	RemotePort     int
	LocalTCPType   string
	RemoteTCPType  string
}

func selectedCandidatePairTraceFromPair(pair *webrtc.ICECandidatePair) selectedCandidatePairEvidence {
	if pair == nil {
		return selectedCandidatePairEvidence{}
	}
	trace := selectedCandidatePairEvidence{}
	if pair.Local != nil {
		trace.LocalType = pair.Local.Typ.String()
		trace.LocalProtocol = pair.Local.Protocol.String()
		trace.LocalAddress = pair.Local.Address
		trace.LocalPort = int(pair.Local.Port)
		trace.LocalTCPType = pair.Local.TCPType
	}
	if pair.Remote != nil {
		trace.RemoteType = pair.Remote.Typ.String()
		trace.RemoteProtocol = pair.Remote.Protocol.String()
		trace.RemoteAddress = pair.Remote.Address
		trace.RemotePort = int(pair.Remote.Port)
		trace.RemoteTCPType = pair.Remote.TCPType
	}
	return trace
}

func selectedCandidatePairTrace(peer *webrtc.PeerConnection) selectedCandidatePairEvidence {
	if peer == nil {
		return selectedCandidatePairEvidence{}
	}
	for _, transceiver := range peer.GetTransceivers() {
		if transceiver == nil {
			continue
		}
		if receiver := transceiver.Receiver(); receiver != nil {
			if trace := selectedCandidatePairTraceFromDTLS(receiver.Transport()); trace.LocalType != "" || trace.RemoteType != "" || trace.LocalProtocol != "" || trace.RemoteProtocol != "" {
				return trace
			}
		}
		if sender := transceiver.Sender(); sender != nil {
			if trace := selectedCandidatePairTraceFromDTLS(sender.Transport()); trace.LocalType != "" || trace.RemoteType != "" || trace.LocalProtocol != "" || trace.RemoteProtocol != "" {
				return trace
			}
		}
	}
	return selectedCandidatePairEvidence{}
}

func selectedCandidatePairTypesFromDTLS(dtls *webrtc.DTLSTransport) (string, string) {
	trace := selectedCandidatePairTraceFromDTLS(dtls)
	return trace.LocalType, trace.RemoteType
}

func selectedCandidatePairTraceFromDTLS(dtls *webrtc.DTLSTransport) selectedCandidatePairEvidence {
	if dtls == nil || dtls.ICETransport() == nil {
		return selectedCandidatePairEvidence{}
	}
	pair, err := dtls.ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return selectedCandidatePairEvidence{}
	}
	return selectedCandidatePairTraceFromPair(pair)
}

func selectedCandidatePairStatsTrace(peer *webrtc.PeerConnection) selectedCandidatePairStatsEvidence {
	if peer == nil {
		return selectedCandidatePairStatsEvidence{}
	}
	report := peer.GetStats()
	for _, transceiver := range peer.GetTransceivers() {
		if transceiver == nil {
			continue
		}
		if receiver := transceiver.Receiver(); receiver != nil {
			if stats, ok := selectedCandidatePairStatsTraceFromDTLS(report, receiver.Transport()); ok {
				return stats
			}
		}
		if sender := transceiver.Sender(); sender != nil {
			if stats, ok := selectedCandidatePairStatsTraceFromDTLS(report, sender.Transport()); ok {
				return stats
			}
		}
	}
	return selectedCandidatePairStatsEvidence{}
}

func selectedCandidatePairStatsTraceFromDTLS(report webrtc.StatsReport, dtls *webrtc.DTLSTransport) (selectedCandidatePairStatsEvidence, bool) {
	if dtls == nil || dtls.ICETransport() == nil {
		return selectedCandidatePairStatsEvidence{}, false
	}
	pair, err := dtls.ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return selectedCandidatePairStatsEvidence{}, false
	}
	stats, ok := report.GetICECandidatePairStats(pair)
	if !ok {
		return selectedCandidatePairStatsEvidence{}, false
	}
	return selectedCandidatePairStatsEvidence{
		RequestsSent:            stats.RequestsSent,
		RequestsReceived:        stats.RequestsReceived,
		ResponsesSent:           stats.ResponsesSent,
		ResponsesReceived:       stats.ResponsesReceived,
		RetransmissionsSent:     stats.RetransmissionsSent,
		RetransmissionsReceived: stats.RetransmissionsReceived,
		ConsentRequestsSent:     stats.ConsentRequestsSent,
		CurrentRoundTripTimeMS:  int64(stats.CurrentRoundTripTime * 1000),
	}, true
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
	return extractICEServersForPolicy(response, WebRTCICEPolicyAll)
}

func extractICEServersForPolicy(response map[string]any, policy string) ([]webrtc.ICEServer, error) {
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
		if strings.EqualFold(normalizedWebRTCICEPolicy(policy), WebRTCICEPolicyRelay) {
			urls = relayICEURLs(urls)
			if len(urls) == 0 {
				continue
			}
		}
		servers = append(servers, webrtc.ICEServer{
			URLs:       urls,
			Username:   server.Username,
			Credential: server.Credential,
		})
	}
	if strings.EqualFold(normalizedWebRTCICEPolicy(policy), WebRTCICEPolicyRelay) && len(servers) == 0 {
		return nil, errors.New("missing TURN ice_servers for relay policy")
	}
	return servers, nil
}

func relayICEURLs(urls []string) []string {
	relayURLs := make([]string, 0, len(urls))
	for _, url := range urls {
		scheme := strings.ToLower(strings.TrimSpace(url))
		if strings.HasPrefix(scheme, "turn:") || strings.HasPrefix(scheme, "turns:") {
			relayURLs = append(relayURLs, url)
		}
	}
	return relayURLs
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
