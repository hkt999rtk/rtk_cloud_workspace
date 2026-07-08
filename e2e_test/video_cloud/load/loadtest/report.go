package loadtest

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func WriteJSON(path string, result *Result) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func ReadJSON(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result Result
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func WriteMarkdown(path string, result *Result) error {
	return os.WriteFile(path, []byte(RenderMarkdown(result)), 0o644)
}

func RenderMarkdown(result *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# rtk_video_cloud Load Test Report\n\n")
	fmt.Fprintf(&b, "- Schema: `%s`\n", result.Schema)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", result.RunID)
	fmt.Fprintf(&b, "- Instance ID: `%s`\n", result.InstanceID)
	fmt.Fprintf(&b, "- Profile: `%s`\n", result.Profile)
	fmt.Fprintf(&b, "- Started: `%s`\n", result.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- Ended: `%s`\n", result.EndedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- API URL: `%s`\n", result.Config.APIURL)
	if commit := result.Metadata["contracts_commit"]; commit != "" {
		fmt.Fprintf(&b, "- Contracts commit: `%s`\n", commit)
	}
	if commit := result.Metadata["server_commit"]; commit != "" {
		fmt.Fprintf(&b, "- Server commit: `%s`\n", commit)
	}
	if commit := result.Metadata["client_commit"]; commit != "" {
		fmt.Fprintf(&b, "- Client commit: `%s`\n", commit)
	}
	if checksum := result.Metadata["binary_sha256"]; checksum != "" {
		fmt.Fprintf(&b, "- Binary SHA256: `%s`\n", checksum)
	}
	fmt.Fprintf(&b, "\n## Summary\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| Total operations | %d |\n", result.Summary.TotalOperations)
	fmt.Fprintf(&b, "| Successes | %d |\n", result.Summary.Successes)
	fmt.Fprintf(&b, "| Failures | %d |\n", result.Summary.Failures)
	fmt.Fprintf(&b, "| Skips | %d |\n", result.Summary.Skips)
	fmt.Fprintf(&b, "| Success rate | %.2f%% |\n", result.Summary.SuccessRate*100)
	fmt.Fprintf(&b, "| p95 latency | %d ms |\n", result.Summary.P95LatencyMS)
	fmt.Fprintf(&b, "| p99 latency | %d ms |\n", result.Summary.P99LatencyMS)
	fmt.Fprintf(&b, "| Throughput | %.2f ops/sec |\n", result.Summary.ThroughputPerSecond)
	fmt.Fprintf(&b, "\n## Threshold Gate\n\n")
	status := "PASS"
	if !result.Thresholds.Passed {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "- Status: `%s`\n", status)
	if len(result.Thresholds.Failures) > 0 {
		for _, failure := range result.Thresholds.Failures {
			fmt.Fprintf(&b, "- %s\n", failure)
		}
	}
	fmt.Fprintf(&b, "\n## Coverage Matrix\n\n")
	fmt.Fprintf(&b, "Coverage status shows what this run exercised; current smoke subset coverage must not be interpreted as full functional coverage.\n\n")
	fmt.Fprintf(&b, "| Family | Status | Covered operations | Summary |\n")
	fmt.Fprintf(&b, "| --- | --- | --- | --- |\n")
	for _, family := range coverageFamilyOrder(result.CoverageMatrix) {
		item := result.CoverageMatrix[family]
		ops := "-"
		if len(item.Operations) > 0 {
			ops = strings.Join(item.Operations, ", ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", family, item.Status, ops, item.Summary)
	}
	fmt.Fprintf(&b, "\n## Actor Metrics\n\n")
	fmt.Fprintf(&b, "| Actor | Ops | Success | Fail | Skip | Success rate | p95 | p99 | Throughput |\n")
	fmt.Fprintf(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, actor := range []string{"app", "device", "viewer"} {
		m := result.Actors[actor]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %.2f%% | %d ms | %d ms | %.2f ops/sec |\n",
			actor, m.Operations, m.Successes, m.Failures, m.Skips, m.SuccessRate*100, m.P95LatencyMS, m.P99LatencyMS, m.ThroughputPerSecond)
	}
	fmt.Fprintf(&b, "\n## WebRTC Metrics\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| Attempts | %d |\n", result.WebRTC.Attempts)
	fmt.Fprintf(&b, "| Successes | %d |\n", result.WebRTC.Successes)
	fmt.Fprintf(&b, "| Failures | %d |\n", result.WebRTC.Failures)
	fmt.Fprintf(&b, "| Success rate | %.2f%% |\n", result.WebRTC.SuccessRate*100)
	fmt.Fprintf(&b, "| Setup p95 | %d ms |\n", result.WebRTC.SetupLatencyP95MS)
	fmt.Fprintf(&b, "| Setup p99 | %d ms |\n", result.WebRTC.SetupLatencyP99MS)
	fmt.Fprintf(&b, "| ICE servers | %d |\n", result.WebRTC.ICEServerCount)
	fmt.Fprintf(&b, "| Open sessions | %d |\n", result.WebRTC.OpenSessions)
	fmt.Fprintf(&b, "\n### WebRTC Lifecycle Phases\n\n")
	fmt.Fprintf(&b, "| Phase | Ops | Success | Fail | Success rate |\n")
	fmt.Fprintf(&b, "| --- | ---: | ---: | ---: | ---: |\n")
	fmt.Fprintf(&b, "| Create | %d | %d | %d | %.2f%% |\n", result.WebRTC.Create.Operations, result.WebRTC.Create.Successes, result.WebRTC.Create.Failures, result.WebRTC.Create.SuccessRate*100)
	fmt.Fprintf(&b, "| Setup | %d | %d | %d | %.2f%% |\n", result.WebRTC.Setup.Operations, result.WebRTC.Setup.Successes, result.WebRTC.Setup.Failures, result.WebRTC.Setup.SuccessRate*100)
	fmt.Fprintf(&b, "| Close | %d | %d | %d | %.2f%% |\n", result.WebRTC.Close.Operations, result.WebRTC.Close.Successes, result.WebRTC.Close.Failures, result.WebRTC.Close.SuccessRate*100)
	fmt.Fprintf(&b, "\n## WebRTC Media Metrics\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| Attempts | %d |\n", result.WebRTCMedia.Attempts)
	fmt.Fprintf(&b, "| Successes | %d |\n", result.WebRTCMedia.Successes)
	fmt.Fprintf(&b, "| Failures | %d |\n", result.WebRTCMedia.Failures)
	fmt.Fprintf(&b, "| RTP packets received | %d |\n", result.WebRTCMedia.PacketsReceived)
	fmt.Fprintf(&b, "| RTP bytes received | %d |\n", result.WebRTCMedia.BytesReceived)
	fmt.Fprintf(&b, "| Time to first RTP p95 | %d ms |\n", result.WebRTCMedia.TimeToFirstRTPP95MS)
	fmt.Fprintf(&b, "| ICE connected p95 | %d ms |\n", result.WebRTCMedia.ICEConnectedP95MS)
	fmt.Fprintf(&b, "| Receive duration | %d ms |\n", result.WebRTCMedia.ReceiveDurationMS)
	if result.WebRTCMedia.VideoStartupLatency.AppRequestToFirstRTPP95MS > 0 || result.WebRTCMedia.VideoStartupLatency.AppRequestToFirstH264AccessUnitP95MS > 0 {
		startup := result.WebRTCMedia.VideoStartupLatency
		fmt.Fprintf(&b, "\n## Video Startup Latency\n\n")
		fmt.Fprintf(&b, "- Samples: %d\n", startup.Samples)
		fmt.Fprintf(&b, "- H.264 access unit samples: %d\n\n", startup.H264AccessUnitSamples)
		fmt.Fprintf(&b, "| Metric | p50 | p95 | p99 |\n")
		fmt.Fprintf(&b, "| --- | ---: | ---: | ---: |\n")
		fmt.Fprintf(&b, "| App request -> first RTP | %d ms | %d ms | %d ms |\n", startup.AppRequestToFirstRTPP50MS, startup.AppRequestToFirstRTPP95MS, startup.AppRequestToFirstRTPP99MS)
		if startup.H264AccessUnitSamples > 0 {
			fmt.Fprintf(&b, "| App request -> first H.264 access unit | %d ms | %d ms | %d ms |\n", startup.AppRequestToFirstH264AccessUnitP50MS, startup.AppRequestToFirstH264AccessUnitP95MS, startup.AppRequestToFirstH264AccessUnitP99MS)
		}
		fmt.Fprintf(&b, "\n### Video Startup Breakdown\n\n")
		fmt.Fprintf(&b, "| Layer | p95 |\n")
		fmt.Fprintf(&b, "| --- | ---: |\n")
		fmt.Fprintf(&b, "| API create | %d ms |\n", startup.BreakdownP95.APICreateMS)
		fmt.Fprintf(&b, "| Offer delivery | %d ms |\n", startup.BreakdownP95.OfferDeliveryMS)
		fmt.Fprintf(&b, "| Answer queue wait | %d ms |\n", startup.BreakdownP95.AnswerQueueWaitMS)
		fmt.Fprintf(&b, "| Answer prepare | %d ms |\n", startup.BreakdownP95.AnswerPrepareMS)
		fmt.Fprintf(&b, "| Answer POST | %d ms |\n", startup.BreakdownP95.AnswerPostMS)
		fmt.Fprintf(&b, "| Device answer | %d ms |\n", startup.BreakdownP95.DeviceAnswerMS)
		fmt.Fprintf(&b, "| Pion create peer | %d ms |\n", startup.BreakdownP95.PionCreatePeerMS)
		fmt.Fprintf(&b, "| Pion create offer | %d ms |\n", startup.BreakdownP95.PionCreateOfferMS)
		if startup.BreakdownP95.PionCreateAnswerMS > 0 {
			fmt.Fprintf(&b, "| Pion create answer | %d ms |\n", startup.BreakdownP95.PionCreateAnswerMS)
		}
		fmt.Fprintf(&b, "| Pion set local description | %d ms |\n", startup.BreakdownP95.PionSetLocalDescriptionMS)
		fmt.Fprintf(&b, "| Pion ICE gathering wait | %d ms |\n", startup.BreakdownP95.PionICEGatheringWaitMS)
		if startup.BreakdownP95.PionFirstLocalCandidateMS > 0 {
			fmt.Fprintf(&b, "| Pion first local candidate | %d ms |\n", startup.BreakdownP95.PionFirstLocalCandidateMS)
		}
		if startup.BreakdownP95.PionFirstLocalRelayCandidateMS > 0 {
			fmt.Fprintf(&b, "| Pion first local relay candidate | %d ms |\n", startup.BreakdownP95.PionFirstLocalRelayCandidateMS)
		}
		if startup.BreakdownP95.PionFirstLocalRelayUDPCandidateMS > 0 {
			fmt.Fprintf(&b, "| Pion first local relay UDP candidate | %d ms |\n", startup.BreakdownP95.PionFirstLocalRelayUDPCandidateMS)
		}
		if startup.BreakdownP95.PionFirstLocalRelayTCPCandidateMS > 0 {
			fmt.Fprintf(&b, "| Pion first local relay TCP candidate | %d ms |\n", startup.BreakdownP95.PionFirstLocalRelayTCPCandidateMS)
		}
		if startup.BreakdownP95.PionRelayCandidateToGatherCompleteMS > 0 {
			fmt.Fprintf(&b, "| Pion relay candidate -> gather complete | %d ms |\n", startup.BreakdownP95.PionRelayCandidateToGatherCompleteMS)
		}
		fmt.Fprintf(&b, "| Pion set remote description | %d ms |\n", startup.BreakdownP95.PionSetRemoteDescriptionMS)
		fmt.Fprintf(&b, "| Remote answer set | %d ms |\n", startup.BreakdownP95.RemoteAnswerSetMS)
		if startup.BreakdownP95.ICESelectedPairChanges > 0 {
			fmt.Fprintf(&b, "| ICE selected pair changes | %d |\n", startup.BreakdownP95.ICESelectedPairChanges)
		}
		if startup.BreakdownP95.ICESelectedPairFirstChangeMS > 0 {
			fmt.Fprintf(&b, "| ICE selected pair first change | %d ms |\n", startup.BreakdownP95.ICESelectedPairFirstChangeMS)
		}
		if startup.BreakdownP95.ICESelectedPairLastChangeMS > 0 {
			fmt.Fprintf(&b, "| ICE selected pair last change | %d ms |\n", startup.BreakdownP95.ICESelectedPairLastChangeMS)
		}
		if startup.BreakdownP95.ICERequestsSent > 0 {
			fmt.Fprintf(&b, "| ICE requests sent | %d |\n", startup.BreakdownP95.ICERequestsSent)
		}
		if startup.BreakdownP95.ICERequestsReceived > 0 {
			fmt.Fprintf(&b, "| ICE requests received | %d |\n", startup.BreakdownP95.ICERequestsReceived)
		}
		if startup.BreakdownP95.ICEResponsesSent > 0 {
			fmt.Fprintf(&b, "| ICE responses sent | %d |\n", startup.BreakdownP95.ICEResponsesSent)
		}
		if startup.BreakdownP95.ICEResponsesReceived > 0 {
			fmt.Fprintf(&b, "| ICE responses received | %d |\n", startup.BreakdownP95.ICEResponsesReceived)
		}
		if startup.BreakdownP95.ICERetransmissionsSent > 0 {
			fmt.Fprintf(&b, "| ICE retransmissions sent | %d |\n", startup.BreakdownP95.ICERetransmissionsSent)
		}
		if startup.BreakdownP95.ICERetransmissionsReceived > 0 {
			fmt.Fprintf(&b, "| ICE retransmissions received | %d |\n", startup.BreakdownP95.ICERetransmissionsReceived)
		}
		if startup.BreakdownP95.ICEConsentRequestsSent > 0 {
			fmt.Fprintf(&b, "| ICE consent requests sent | %d |\n", startup.BreakdownP95.ICEConsentRequestsSent)
		}
		if startup.BreakdownP95.ICEWriteRTTMS > 0 {
			fmt.Fprintf(&b, "| ICE RTT | %d ms |\n", startup.BreakdownP95.ICEWriteRTTMS)
		}
		fmt.Fprintf(&b, "| ICE check | %d ms |\n", startup.BreakdownP95.ICECheckMS)
		fmt.Fprintf(&b, "| ICE connected since session start | %d ms |\n", startup.BreakdownP95.ICEConnectedSinceSessionStartMS)
		fmt.Fprintf(&b, "| Device ICE wait | %d ms |\n", startup.BreakdownP95.DeviceICEWaitMS)
		if startup.BreakdownP95.ViewerPeerConnectionConnectedMS > 0 {
			fmt.Fprintf(&b, "| Viewer peer connection connected | %d ms |\n", startup.BreakdownP95.ViewerPeerConnectionConnectedMS)
		}
		if startup.BreakdownP95.ViewerPeerConnectedAfterICEMS > 0 {
			fmt.Fprintf(&b, "| Viewer peer connected after ICE | %d ms |\n", startup.BreakdownP95.ViewerPeerConnectedAfterICEMS)
		}
		if startup.BreakdownP95.SenderPeerConnectionConnectedMS > 0 {
			fmt.Fprintf(&b, "| Sender peer connection connected | %d ms |\n", startup.BreakdownP95.SenderPeerConnectionConnectedMS)
		}
		if startup.BreakdownP95.SenderPeerConnectedAfterICEMS > 0 {
			fmt.Fprintf(&b, "| Sender peer connected after ICE | %d ms |\n", startup.BreakdownP95.SenderPeerConnectedAfterICEMS)
		}
		fmt.Fprintf(&b, "| Sender queue wait | %d ms |\n", startup.BreakdownP95.SenderQueueWaitMS)
		if startup.BreakdownP95.SenderWriteAttempts > 0 {
			fmt.Fprintf(&b, "| Sender WriteRTP attempts | %d |\n", startup.BreakdownP95.SenderWriteAttempts)
		}
		if startup.BreakdownP95.SenderWriteReturns > 0 {
			fmt.Fprintf(&b, "| Sender WriteRTP returns | %d |\n", startup.BreakdownP95.SenderWriteReturns)
		}
		if startup.BreakdownP95.SenderWriteErrors > 0 {
			fmt.Fprintf(&b, "| Sender WriteRTP errors | %d |\n", startup.BreakdownP95.SenderWriteErrors)
		}
		if startup.BreakdownP95.SenderFirstWriteCallMS > 0 {
			fmt.Fprintf(&b, "| Sender first WriteRTP call | %d ms |\n", startup.BreakdownP95.SenderFirstWriteCallMS)
		}
		if startup.BreakdownP95.SenderFirstWriteReturnMS > 0 {
			fmt.Fprintf(&b, "| Sender first WriteRTP return | %d ms |\n", startup.BreakdownP95.SenderFirstWriteReturnMS)
		}
		if startup.BreakdownP95.SenderWriteMaxMS > 0 {
			fmt.Fprintf(&b, "| Sender max WriteRTP latency | %d ms |\n", startup.BreakdownP95.SenderWriteMaxMS)
		}
		fmt.Fprintf(&b, "| Sender first write after ICE | %d ms |\n", startup.BreakdownP95.SenderFirstWriteAfterICEMS)
		if startup.BreakdownP95.SenderFirstWriteAfterPeerMS > 0 {
			fmt.Fprintf(&b, "| Sender first write after peer connected | %d ms |\n", startup.BreakdownP95.SenderFirstWriteAfterPeerMS)
		}
		if startup.BreakdownP95.SenderFirstWriteSinceSessionMS > 0 {
			fmt.Fprintf(&b, "| Sender first RTP write since session start | %d ms |\n", startup.BreakdownP95.SenderFirstWriteSinceSessionMS)
		}
		if startup.BreakdownP95.SenderQueueFullDrops > 0 {
			fmt.Fprintf(&b, "| Sender queue full drops | %d |\n", startup.BreakdownP95.SenderQueueFullDrops)
		}
		fmt.Fprintf(&b, "| First RTP after ICE | %d ms |\n", startup.BreakdownP95.FirstRTPAfterICEMS)
		if startup.H264AccessUnitSamples > 0 {
			fmt.Fprintf(&b, "| First H.264 access unit after RTP | %d ms |\n", startup.BreakdownP95.FirstH264AccessUnitAfterRTPMS)
		}
	}
	if len(result.MQTTIoT) > 0 {
		fmt.Fprintf(&b, "\n## MQTT IoT Metrics\n\n")
		fmt.Fprintf(&b, "| Capability | Ops | Success | Fail | Skip | Success rate | p95 | p99 | Throughput |\n")
		fmt.Fprintf(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
		for _, capability := range []string{"light", "air_conditioner", "smart_meter"} {
			m := result.MQTTIoT[capability]
			if m.Operations == 0 {
				continue
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %.2f%% | %d ms | %d ms | %.2f ops/sec |\n",
				capability, m.Operations, m.Successes, m.Failures, m.Skips, m.SuccessRate*100, m.P95LatencyMS, m.P99LatencyMS, m.ThroughputPerSecond)
		}
	}
	fmt.Fprintf(&b, "\n## Error Classes\n\n")
	if len(result.Errors) == 0 {
		fmt.Fprintf(&b, "- None\n")
	} else {
		for class, count := range result.Errors {
			fmt.Fprintf(&b, "- `%s`: %d\n", class, count)
		}
	}
	return b.String()
}

func coverageFamilyOrder(matrix map[string]CoverageItem) []string {
	preferred := []string{
		"auth",
		"app_http",
		"device_http",
		"config",
		"streaming",
		"webrtc",
		"webrtc_media",
		"owner_transport",
		"websocket_snapshot",
		"mqtt",
		"negative",
		"scale",
	}
	seen := map[string]bool{}
	order := make([]string, 0, len(matrix))
	for _, family := range preferred {
		if _, ok := matrix[family]; ok {
			order = append(order, family)
			seen[family] = true
		}
	}
	extra := make([]string, 0)
	for family := range matrix {
		if !seen[family] {
			extra = append(extra, family)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

func EvaluateThresholds(summary Summary, thresholds Thresholds) ThresholdEvaluation {
	return EvaluateResultThresholds(summary, WebRTCMetrics{}, nil, thresholds)
}

func EvaluateResultThresholds(summary Summary, webrtc WebRTCMetrics, coverage map[string]CoverageItem, thresholds Thresholds) ThresholdEvaluation {
	evaluation := ThresholdEvaluation{Passed: true}
	if thresholds.MinSuccessRate > 0 && summary.SuccessRate < thresholds.MinSuccessRate {
		evaluation.Passed = false
		evaluation.Failures = append(evaluation.Failures,
			fmt.Sprintf("success rate %.2f%% is below threshold %.2f%%", summary.SuccessRate*100, thresholds.MinSuccessRate*100))
	}
	if thresholds.MaxP95Latency > 0 && summary.P95LatencyMS > thresholds.MaxP95Latency {
		evaluation.Passed = false
		evaluation.Failures = append(evaluation.Failures,
			fmt.Sprintf("p95 latency %d ms exceeds threshold %d ms", summary.P95LatencyMS, thresholds.MaxP95Latency))
	}
	if thresholds.MaxP99Latency > 0 && summary.P99LatencyMS > thresholds.MaxP99Latency {
		evaluation.Passed = false
		evaluation.Failures = append(evaluation.Failures,
			fmt.Sprintf("p99 latency %d ms exceeds threshold %d ms", summary.P99LatencyMS, thresholds.MaxP99Latency))
	}
	if thresholds.MaxWebRTCSetupP95Latency > 0 && webrtc.SetupLatencyP95MS > thresholds.MaxWebRTCSetupP95Latency {
		evaluation.Passed = false
		evaluation.Failures = append(evaluation.Failures,
			fmt.Sprintf("WebRTC setup p95 latency %d ms exceeds threshold %d ms", webrtc.SetupLatencyP95MS, thresholds.MaxWebRTCSetupP95Latency))
	}
	if thresholds.MaxOpenWebRTCSessions >= 0 && webrtc.OpenSessions > thresholds.MaxOpenWebRTCSessions {
		evaluation.Passed = false
		evaluation.Failures = append(evaluation.Failures,
			fmt.Sprintf("open WebRTC sessions %d exceeds threshold %d", webrtc.OpenSessions, thresholds.MaxOpenWebRTCSessions))
	}
	if thresholds.RequireCoverageMatrix && len(coverage) == 0 {
		evaluation.Passed = false
		evaluation.Failures = append(evaluation.Failures, "coverage matrix artifact is missing")
	}
	if thresholds.RequireCoverageMatrix {
		for family, item := range coverage {
			if item.Status == CoverageStatusFail || item.Status == CoverageStatusBlocked {
				evaluation.Passed = false
				evaluation.Failures = append(evaluation.Failures, fmt.Sprintf("coverage family %s status %s", family, item.Status))
			}
		}
	}
	return evaluation
}

func ApplyWebRTCMediaThreshold(evaluation *ThresholdEvaluation, media WebRTCMediaMetrics, thresholds Thresholds) {
	applyWebRTCMediaThreshold(evaluation, media, thresholds)
}

func applyWebRTCMediaThreshold(evaluation *ThresholdEvaluation, media WebRTCMediaMetrics, thresholds Thresholds) {
	if evaluation == nil || thresholds.MinWebRTCMediaSuccessRate <= 0 || media.Attempts == 0 {
		return
	}
	rate := float64(media.Successes) / float64(media.Attempts)
	if rate >= thresholds.MinWebRTCMediaSuccessRate {
		return
	}
	evaluation.Passed = false
	evaluation.Failures = append(evaluation.Failures,
		fmt.Sprintf("WebRTC media success rate %.2f%% is below threshold %.2f%% (%d/%d)",
			rate*100, thresholds.MinWebRTCMediaSuccessRate*100, media.Successes, media.Attempts))
}
