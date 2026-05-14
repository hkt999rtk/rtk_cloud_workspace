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
