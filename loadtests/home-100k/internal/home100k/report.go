package home100k

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type ReportInput struct {
	Plan                  Plan
	RunID                 string
	ShadowEvidenceFound   bool
	ServerEvidenceFound   bool
	LoadGeneratorHealthy  bool
	StageResults          []StageResult
	ServerEvidence        ServerEvidence
	ServerCorrelation     ServerCorrelation
	RuntimeLogCorrelation RuntimeLogCorrelation
	VideoEvidence         VideoEvidence
	StartCoordination     StartCoordination
	SyncTelemetry         SyncTelemetry
	Notes                 []string
}

func RenderReport(input ReportInput) string {
	evidence := input.ServerEvidence
	evidence.Complete = input.ServerEvidenceFound
	health := LoadGeneratorHealth{Saturated: !input.LoadGeneratorHealthy}
	outcome := evaluateRunOutcome(input.Plan, evidence, input.StageResults, health, input.ServerCorrelation, input.RuntimeLogCorrelation, input.VideoEvidence)
	reasons := outcome.Reasons

	var b strings.Builder
	fmt.Fprintf(&b, "# 100K Home IoT Device Shadow Load Test Report\n\n")
	fmt.Fprintf(&b, "- Run ID: %s\n", firstNonEmpty(input.RunID, "<run_id>"))
	fmt.Fprintf(&b, "- Status: %s\n", outcome.Status)
	fmt.Fprintf(&b, "- Result: %s\n", firstNonEmpty(outcome.Result, "UNKNOWN"))
	for _, reason := range reasons {
		fmt.Fprintf(&b, "- %s\n", reason)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Status Summary")
	if len(reasons) == 0 {
		fmt.Fprintln(&b, "- result gate: passed")
	} else {
		gateLabel := "result gate"
		if outcome.Status == "INCOMPLETE" {
			gateLabel = "status gate"
		}
		for _, reason := range reasons {
			fmt.Fprintf(&b, "- %s: %s\n", gateLabel, reason)
		}
	}
	if strings.TrimSpace(input.ServerCorrelation.Status) != "" {
		fmt.Fprintf(&b, "- server correlation: %s\n", input.ServerCorrelation.Status)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Test Conditions")
	fmt.Fprintf(&b, "- Env root: `%s`\n", input.Plan.Conditions.EnvRoot)
	renderReportBrandConditions(&b, input.Plan)
	fmt.Fprintf(&b, "- Region: `%s`\n", input.Plan.Conditions.Region)
	fmt.Fprintf(&b, "- Devices: %d\n", input.Plan.Conditions.Devices)
	fmt.Fprintf(&b, "- Users: %d\n", input.Plan.Conditions.Users)
	fmt.Fprintf(&b, "- Devices per user: %d\n", input.Plan.Conditions.DevicesPerUser)
	fmt.Fprintf(&b, "- Runner nofile limit: %d\n", input.Plan.Conditions.RunnerNofileLimit)
	fmt.Fprintf(&b, "- Device session model: `%s`\n", firstNonEmpty(input.Plan.Conditions.DeviceSessionModel, DefaultDeviceSession))
	fmt.Fprintf(&b, "- Runner read model: `%s`\n", firstNonEmpty(input.Plan.Conditions.RunnerReadModel, DefaultRunnerReadModel))
	fmt.Fprintf(&b, "- Device token request timeout: `%s`\n", firstNonEmpty(input.Plan.Conditions.DeviceTokenRequestTimeout, DefaultDeviceTokenRequestTimeout))
	fmt.Fprintf(&b, "- Device token request retries: %d\n", input.Plan.Conditions.DeviceTokenRequestRetries)
	fmt.Fprintln(&b, "- Runner read requirement: sustained MQTT reads through Go netpoll-backed connections and bounded per-device reader goroutines; command-time one-shot reads are not valid for capacity conclusions.")
	fmt.Fprintln(&b)

	thresholds := gateThresholdsFromConditions(input.Plan.Conditions)
	fmt.Fprintln(&b, "## Gate Standards")
	fmt.Fprintf(&b, "- Functional success threshold: %.2f%%\n", thresholds.FunctionalSuccessThresholdPercent)
	fmt.Fprintf(&b, "- Client target completeness threshold: %.2f%%\n", thresholds.ClientTargetCompletenessPercent)
	fmt.Fprintf(&b, "- Exact event correlation threshold: %.2f%%\n", thresholds.ExactEventCorrelationPercent)
	fmt.Fprintf(&b, "- Aggregate counter tolerance: max(%d, %.2f%%)\n", thresholds.AggregateCorrelationMinTolerance, thresholds.AggregateCorrelationTolerancePercent)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Counter Scope")
	fmt.Fprintln(&b, "- Device MQTT totals and APP/User totals are client-side runner counters.")
	fmt.Fprintln(&b, "- The current runner records synthetic actor sample counters; these totals are not proof that 100,000 real MQTT devices or 5,000 real app users exchanged traffic.")
	fmt.Fprintln(&b, "- A capacity `PASS` requires functional success gates and exact event correlation to meet the configured thresholds; aggregate counter correlation inside tolerance is a sanity check, and small mismatches are warnings.")
	fmt.Fprintln(&b)

	if input.Plan.VideoEnabled() {
		fmt.Fprintln(&b, "## Video Load Profile")
		fmt.Fprintf(&b, "- Video profile: `%s`\n", input.Plan.VideoProfile.Name)
		fmt.Fprintf(&b, "- Video devices: %d\n", input.Plan.VideoProfile.VideoDevices)
		fmt.Fprintf(&b, "- Video viewers: %d\n", input.Plan.VideoProfile.VideoViewers)
		if len(input.Plan.VideoProfile.ViewerLadder) > 0 {
			fmt.Fprintf(&b, "- Viewer ladder: `%s`\n", joinInts(input.Plan.VideoProfile.ViewerLadder, ","))
		}
		fmt.Fprintf(&b, "- WebRTC media set: `%s`\n", input.Plan.VideoProfile.WebRTCMediaSet)
		fmt.Fprintf(&b, "- WebRTC ICE policy: `%s`\n", input.Plan.VideoProfile.WebRTCICEPolicy)
		if input.Plan.VideoProfile.StepDuration != "" {
			fmt.Fprintf(&b, "- Video step duration: `%s`\n", input.Plan.VideoProfile.StepDuration)
		}
		if input.Plan.VideoProfile.StepCooldown != "" {
			fmt.Fprintf(&b, "- Video step cooldown: `%s`\n", input.Plan.VideoProfile.StepCooldown)
		}
		fmt.Fprintf(&b, "- TURN transport: `%s`\n", firstNonEmpty(input.Plan.VideoProfile.TURNTransport, "udp,tcp"))
		fmt.Fprintf(&b, "- Media security: `%s`\n", firstNonEmpty(input.Plan.VideoProfile.MediaSecurity, "dtls-srtp"))
		fmt.Fprintln(&b, "- TURN relays encrypted DTLS-SRTP packets over the configured UDP/TCP transports; coturn does not terminate DTLS or inspect RTP/H.264 payloads.")
		fmt.Fprintf(&b, "- Device actor: `%s`\n", input.Plan.VideoProfile.DeviceActorRole)
		fmt.Fprintf(&b, "- App actor: `%s`\n", input.Plan.VideoProfile.AppActorRole)
		fmt.Fprintf(&b, "- Viewer actor: `%s`\n", input.Plan.VideoProfile.ViewerActorRole)
		fmt.Fprintln(&b)
		if len(input.VideoEvidence.Steps) > 1 {
			fmt.Fprintln(&b, "## Video Ladder")
			fmt.Fprintln(&b, "| Step | Viewers | ICE policy | WebRTC success | Media success | first RTP p95 | first H.264 AU p95 | Relay samples | Non-relay samples | TURN allocations | TURN sessions |")
			fmt.Fprintln(&b, "| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
			for _, step := range input.VideoEvidence.Steps {
				mediaRate := percentInt64(step.WebRTCMedia.Successes, step.WebRTCMedia.Attempts)
				fmt.Fprintf(&b, "| %s | %d | %s | %.2f%% | %.2f%% | %d ms | %d ms | %d | %d | %d | %d |\n",
					firstNonEmpty(step.Name, fmt.Sprintf("%d-viewers", step.Viewers)),
					step.Viewers,
					firstNonEmpty(step.ICEPolicy, "-"),
					step.WebRTC.SuccessRatePercent,
					mediaRate,
					step.WebRTCMedia.TimeToFirstRTPP95MS,
					step.WebRTCMedia.Startup.AppRequestToFirstH264AccessUnitP95MS,
					step.RelayCandidateSamples,
					step.NonRelayCandidateSamples,
					step.TURN.Allocations,
					step.TURN.ActiveSessions,
				)
			}
			fmt.Fprintln(&b)
		}

		fmt.Fprintln(&b, "## WebRTC Totals")
		fmt.Fprintln(&b, "| Phase | Attempts | Success | Success rate |")
		fmt.Fprintln(&b, "| --- | ---: | ---: | ---: |")
		renderWebRTCPhase(&b, "create", input.VideoEvidence.WebRTC.CreateAttempts, input.VideoEvidence.WebRTC.CreateSuccess)
		renderWebRTCPhase(&b, "setup", input.VideoEvidence.WebRTC.SetupAttempts, input.VideoEvidence.WebRTC.SetupSuccess)
		renderWebRTCPhase(&b, "close", input.VideoEvidence.WebRTC.CloseAttempts, input.VideoEvidence.WebRTC.CloseSuccess)
		fmt.Fprintf(&b, "- Setup p95: %d ms\n", input.VideoEvidence.WebRTC.SetupP95MS)
		fmt.Fprintf(&b, "- Setup p99: %d ms\n", input.VideoEvidence.WebRTC.SetupP99MS)
		fmt.Fprintf(&b, "- ICE server count: %d\n", input.VideoEvidence.WebRTC.ICEServerCount)
		fmt.Fprintf(&b, "- Open sessions: %d\n", input.VideoEvidence.WebRTC.OpenSessions)
		fmt.Fprintln(&b)

		if input.VideoEvidence.WebRTCMedia.Enabled || input.VideoEvidence.WebRTCMedia.Attempts > 0 {
			fmt.Fprintln(&b, "## WebRTC Media Totals")
			fmt.Fprintf(&b, "- Attempts: %d\n", input.VideoEvidence.WebRTCMedia.Attempts)
			fmt.Fprintf(&b, "- Successes: %d\n", input.VideoEvidence.WebRTCMedia.Successes)
			fmt.Fprintf(&b, "- Failures: %d\n", input.VideoEvidence.WebRTCMedia.Failures)
			fmt.Fprintf(&b, "- ICE connected p95: %d ms\n", input.VideoEvidence.WebRTCMedia.ICEConnectedP95MS)
			fmt.Fprintf(&b, "- First RTP p95: %d ms\n", input.VideoEvidence.WebRTCMedia.TimeToFirstRTPP95MS)
			fmt.Fprintf(&b, "- RTP packets received: %d\n", input.VideoEvidence.WebRTCMedia.PacketsReceived)
			fmt.Fprintf(&b, "- RTP bytes received: %d\n", input.VideoEvidence.WebRTCMedia.BytesReceived)
			fmt.Fprintf(&b, "- H.264 packets received: %d\n", input.VideoEvidence.WebRTCMedia.H264PacketsReceived)
			fmt.Fprintf(&b, "- H.264 bytes received: %d\n", input.VideoEvidence.WebRTCMedia.H264BytesReceived)
			fmt.Fprintf(&b, "- Opus packets received: %d\n", input.VideoEvidence.WebRTCMedia.OpusPacketsReceived)
			fmt.Fprintf(&b, "- Opus bytes received: %d\n", input.VideoEvidence.WebRTCMedia.OpusBytesReceived)
			if len(input.VideoEvidence.WebRTCMedia.FailureReasons) > 0 {
				fmt.Fprintln(&b, "- Failure reasons:")
				for _, reason := range sortedStringIntKeys(input.VideoEvidence.WebRTCMedia.FailureReasons) {
					fmt.Fprintf(&b, "  - %s: %d\n", reason, input.VideoEvidence.WebRTCMedia.FailureReasons[reason])
				}
			}
			if len(input.VideoEvidence.WebRTCMedia.SenderFailurePhases) > 0 {
				fmt.Fprintln(&b, "- Sender failure phases:")
				for _, phase := range sortedStringIntKeys(input.VideoEvidence.WebRTCMedia.SenderFailurePhases) {
					fmt.Fprintf(&b, "  - %s: %d\n", phase, input.VideoEvidence.WebRTCMedia.SenderFailurePhases[phase])
				}
			}
			fmt.Fprintln(&b)
			if input.VideoEvidence.WebRTCMedia.Startup.AppRequestToFirstRTPP95MS > 0 || input.VideoEvidence.WebRTCMedia.Startup.AppRequestToFirstH264AccessUnitP95MS > 0 {
				startup := input.VideoEvidence.WebRTCMedia.Startup
				fmt.Fprintln(&b, "## Video Startup Latency")
				fmt.Fprintf(&b, "- Samples: %d\n", startup.Samples)
				fmt.Fprintf(&b, "- H.264 access unit samples: %d\n", startup.H264AccessUnitSamples)
				fmt.Fprintf(&b, "- App request -> first RTP p50: %d ms\n", startup.AppRequestToFirstRTPP50MS)
				fmt.Fprintf(&b, "- App request -> first RTP p95: %d ms\n", startup.AppRequestToFirstRTPP95MS)
				fmt.Fprintf(&b, "- App request -> first RTP p99: %d ms\n", startup.AppRequestToFirstRTPP99MS)
				if startup.H264AccessUnitSamples > 0 {
					fmt.Fprintf(&b, "- App request -> first H.264 access unit p50: %d ms\n", startup.AppRequestToFirstH264AccessUnitP50MS)
					fmt.Fprintf(&b, "- App request -> first H.264 access unit p95: %d ms\n", startup.AppRequestToFirstH264AccessUnitP95MS)
					fmt.Fprintf(&b, "- App request -> first H.264 access unit p99: %d ms\n", startup.AppRequestToFirstH264AccessUnitP99MS)
				}
				fmt.Fprintf(&b, "- API create p95: %d ms\n", startup.BreakdownP95.APICreateMS)
				fmt.Fprintf(&b, "- Offer delivery p95: %d ms\n", startup.BreakdownP95.OfferDeliveryMS)
				fmt.Fprintf(&b, "- Device answer p95: %d ms\n", startup.BreakdownP95.DeviceAnswerMS)
				if startup.BreakdownP95.PionCreatePeerMS > 0 {
					fmt.Fprintf(&b, "- Pion create peer p95: %d ms\n", startup.BreakdownP95.PionCreatePeerMS)
				}
				if startup.BreakdownP95.PionCreateOfferMS > 0 {
					fmt.Fprintf(&b, "- Pion create offer p95: %d ms\n", startup.BreakdownP95.PionCreateOfferMS)
				}
				if startup.BreakdownP95.PionCreateAnswerMS > 0 {
					fmt.Fprintf(&b, "- Pion create answer p95: %d ms\n", startup.BreakdownP95.PionCreateAnswerMS)
				}
				if startup.BreakdownP95.PionSetLocalDescriptionMS > 0 {
					fmt.Fprintf(&b, "- Pion set local description p95: %d ms\n", startup.BreakdownP95.PionSetLocalDescriptionMS)
				}
				if startup.BreakdownP95.PionICEGatheringWaitMS > 0 {
					fmt.Fprintf(&b, "- Pion ICE gathering wait p95: %d ms\n", startup.BreakdownP95.PionICEGatheringWaitMS)
				}
				if startup.BreakdownP95.PionSetRemoteDescriptionMS > 0 {
					fmt.Fprintf(&b, "- Pion set remote description p95: %d ms\n", startup.BreakdownP95.PionSetRemoteDescriptionMS)
				}
				if startup.BreakdownP95.RemoteAnswerSetMS > 0 {
					fmt.Fprintf(&b, "- Remote answer set p95: %d ms\n", startup.BreakdownP95.RemoteAnswerSetMS)
				}
				iceCheckMS := firstNonZeroInt64(startup.BreakdownP95.ICECheckMS, startup.BreakdownP95.ICEConnectMS)
				fmt.Fprintf(&b, "- ICE check p95: %d ms\n", iceCheckMS)
				if startup.BreakdownP95.ICEConnectedSinceSessionStartMS > 0 {
					fmt.Fprintf(&b, "- ICE connected since session start p95: %d ms\n", startup.BreakdownP95.ICEConnectedSinceSessionStartMS)
				}
				if startup.BreakdownP95.SenderFirstWriteSinceSessionMS > 0 {
					fmt.Fprintf(&b, "- Sender first RTP write since session start p95: %d ms\n", startup.BreakdownP95.SenderFirstWriteSinceSessionMS)
				}
				fmt.Fprintf(&b, "- First RTP after ICE p95: %d ms\n", startup.BreakdownP95.FirstRTPAfterICEMS)
				if startup.H264AccessUnitSamples > 0 {
					fmt.Fprintf(&b, "- First H.264 access unit after RTP p95: %d ms\n", startup.BreakdownP95.FirstH264AccessUnitAfterRTPMS)
				}
				fmt.Fprintln(&b)
			}
		}

		fmt.Fprintln(&b, "## TURN Evidence")
		fmt.Fprintf(&b, "- registry available: %t\n", input.VideoEvidence.TURN.RegistryAvailable)
		fmt.Fprintf(&b, "- active nodes: %d\n", input.VideoEvidence.TURN.ActiveNodes)
		fmt.Fprintf(&b, "- coturn available: %t\n", input.VideoEvidence.TURN.CoturnAvailable)
		fmt.Fprintf(&b, "- allocations: %d\n", input.VideoEvidence.TURN.Allocations)
		fmt.Fprintf(&b, "- active sessions: %d\n", input.VideoEvidence.TURN.ActiveSessions)
		fmt.Fprintf(&b, "- coturn UDP sockets: %d\n", input.VideoEvidence.TURN.UDPSockets)
		fmt.Fprintf(&b, "- coturn TCP established: %d\n", input.VideoEvidence.TURN.TCPEstablished)
		fmt.Fprintf(&b, "- relay UDP flows: %d\n", input.VideoEvidence.TURN.RelayUDPFlows)
		fmt.Fprintf(&b, "- relay TCP flows: %d\n", input.VideoEvidence.TURN.RelayTCPFlows)
		fmt.Fprintf(&b, "- coturn journal events: %d\n", input.VideoEvidence.TURN.JournalEvents)
		if input.VideoEvidence.TURN.APIStaticTURNCount > 0 || input.VideoEvidence.TURN.APIDynamicTURNCount > 0 || input.VideoEvidence.TURN.APITURNRegistryLookupSucceeded > 0 || input.VideoEvidence.TURN.APITURNRegistryLookupEmpty > 0 || input.VideoEvidence.TURN.APITURNRegistryLookupFailed > 0 {
			fmt.Fprintf(&b, "- API TURN registry lookup succeeded events: %d\n", input.VideoEvidence.TURN.APITURNRegistryLookupSucceeded)
			fmt.Fprintf(&b, "- API TURN registry lookup empty events: %d\n", input.VideoEvidence.TURN.APITURNRegistryLookupEmpty)
			fmt.Fprintf(&b, "- API TURN registry lookup failed events: %d\n", input.VideoEvidence.TURN.APITURNRegistryLookupFailed)
			fmt.Fprintf(&b, "- API dynamic TURN server count max: %d\n", input.VideoEvidence.TURN.APIDynamicTURNCount)
			fmt.Fprintf(&b, "- API static TURN server count max: %d\n", input.VideoEvidence.TURN.APIStaticTURNCount)
			fmt.Fprintf(&b, "- API turnregistry node count max: %d\n", input.VideoEvidence.TURN.APITURNRegistryNodeCount)
		}
		if input.VideoEvidence.TURN.CoturnCPUPercent > 0 || input.VideoEvidence.TURN.CoturnRSSKB > 0 {
			fmt.Fprintf(&b, "- coturn process CPU peak: %d%%\n", input.VideoEvidence.TURN.CoturnCPUPercent)
			fmt.Fprintf(&b, "- coturn process RSS peak: %d KB\n", input.VideoEvidence.TURN.CoturnRSSKB)
		}
		if input.VideoEvidence.TURN.RXBytes > 0 || input.VideoEvidence.TURN.TXBytes > 0 {
			fmt.Fprintf(&b, "- coturn VM RX bytes peak: %d\n", input.VideoEvidence.TURN.RXBytes)
			fmt.Fprintf(&b, "- coturn VM TX bytes peak: %d\n", input.VideoEvidence.TURN.TXBytes)
		}
		if input.VideoEvidence.TURN.EvidenceStatus != "" {
			fmt.Fprintf(&b, "- active evidence status: %s\n", input.VideoEvidence.TURN.EvidenceStatus)
		}
		fmt.Fprintf(&b, "- relay candidate samples: %d\n", input.VideoEvidence.RelayCandidateSamples)
		fmt.Fprintf(&b, "- non-relay candidate samples: %d\n", input.VideoEvidence.NonRelayCandidateSamples)
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "## Scenario Mix")
	fmt.Fprintf(&b, "- Scenario profile: `%s`\n", firstNonEmpty(input.Plan.ScenarioProfile, DefaultScenarioProfile))
	renderMap(&b, "Device mix", input.Plan.DeviceMix)
	renderMap(&b, "Presence mix", input.Plan.PresenceMix)
	fmt.Fprintln(&b)

	if len(input.Plan.DeviceProfiles) > 0 {
		fmt.Fprintln(&b, "## Device Traffic Profiles")
		fmt.Fprintln(&b, "| Device type | Ratio weight | Traffic profile | Payload class |")
		fmt.Fprintln(&b, "| --- | ---: | --- | --- |")
		for _, name := range sortedDeviceProfileKeys(input.Plan.DeviceProfiles) {
			profile := input.Plan.DeviceProfiles[name]
			fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", name, profile.RatioWeight, profile.TrafficProfile, profile.PayloadClass)
		}
		fmt.Fprintln(&b)
	}

	if len(input.Plan.UserProfiles) > 0 {
		fmt.Fprintln(&b, "## User Scenario Profiles")
		fmt.Fprintln(&b, "| User profile | Ratio weight | Action profile |")
		fmt.Fprintln(&b, "| --- | ---: | --- |")
		for _, name := range sortedUserProfileKeys(input.Plan.UserProfiles) {
			profile := input.Plan.UserProfiles[name]
			fmt.Fprintf(&b, "| %s | %d | %s |\n", name, profile.RatioWeight, profile.ActionProfile)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Device Scenario")
	fmt.Fprintln(&b, "- Online steady devices subscribe to shadow delta, apply desired state, and write reported state.")
	fmt.Fprintln(&b, "- Offline desired queue devices reconnect, call shadow get, apply queued desired state, and clear delta.")
	fmt.Fprintln(&b, "- Flapping reconnect devices verify reconnect sync, version handling, and duplicate-apply prevention.")
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## User Scenario")
	fmt.Fprintln(&b, "- App users login, bootstrap app certificates when needed, list authorized devices, read shadows, and write desired state.")
	fmt.Fprintln(&b, "- Users wait for reported state to match desired state and for delta to clear.")
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## IoT Device Shadow Scenario")
	fmt.Fprintln(&b, "- Canonical path: app writes desired -> cloud computes delta -> device receives delta or reads shadow after reconnect -> device writes reported -> delta clears.")
	fmt.Fprintln(&b, "- Offline desired queue is required coverage, not an optional scenario.")
	fmt.Fprintln(&b, "- Device desired writes must be rejected; stale versions must be counted as conflicts.")
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Target Window")
	target := input.Plan.Target
	if target.TargetConnects == 0 {
		target = targetWindowFromStages(input.Plan.Stages)
	}
	fmt.Fprintf(&b, "- target connects: %d\n", target.TargetConnects)
	fmt.Fprintf(&b, "- ramp-up time: %s\n", target.RampUpTime)
	fmt.Fprintln(&b)

	if len(input.StageResults) > 0 {
		fmt.Fprintln(&b, "## Target Results")
		fmt.Fprintln(&b, "| Window | Devices | MQTT connect | Reconnects | Shadow get p50 | Shadow get p95 | Shadow get p99 | Desired update p95 | Delta receive p95 | Desired->reported p95 | Offline desired p95 | desired/reported convergence | offline desired convergence | Delta clear | Conflicts | Rejected | Auth violations | Client tokens | Duplicate apply |")
		fmt.Fprintln(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
		for _, stage := range input.StageResults {
			fmt.Fprintf(&b, "| %s | %d | %.2f%% | %d | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f%% | %.2f%% | %.2f%% | %d | %d | %d | %d | %d |\n",
				stage.Name,
				stage.ConnectedDevices,
				stage.MQTTConnectSuccessRatePercent,
				stage.MQTTReconnectCount,
				stage.ShadowGetP50MS,
				stage.ShadowGetP95MS,
				stage.ShadowGetP99MS,
				stage.DesiredUpdateP95MS,
				stage.DeltaReceiveP95MS,
				stage.DesiredReportedP95MS,
				stage.OfflineDesiredP95MS,
				stage.DesiredReportedConvergenceRate,
				stage.OfflineDesiredConvergenceRate,
				stage.DeltaClearSuccessRatePercent,
				stage.VersionConflictCount,
				stage.RejectedUpdateCount,
				stage.AuthorizationViolationCount,
				stage.ClientTokenCorrelationCount,
				stage.DuplicateApplyCount,
			)
		}
		fmt.Fprintln(&b)

		diagnosticRows := stageDiagnosticRows(input.StageResults)
		if len(diagnosticRows) > 0 {
			fmt.Fprintln(&b, "## Target Diagnostics")
			fmt.Fprintln(&b, "| Window | Shard | Target | Before | After | Connect attempts | Connect success | Connect fail | Commands scheduled | Commands attempted | Commands passed | Skip reason |")
			fmt.Fprintln(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
			for _, row := range diagnosticRows {
				fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %s |\n",
					row.Stage,
					row.Shard,
					row.Target,
					row.Before,
					row.After,
					row.ConnectAttempts,
					row.ConnectSuccess,
					row.ConnectFail,
					row.CommandsScheduled,
					row.CommandsAttempted,
					row.CommandsPassed,
					firstNonEmpty(row.SkipReason, "-"),
				)
			}
			fmt.Fprintln(&b)
		}

		fmt.Fprintln(&b, "## Device MQTT Totals")
		fmt.Fprintln(&b, "| Window | Connect attempts | Connect success | Connect fail | New subscribes | Active connections | Active subscriptions | Publishes | Received | Delta received | Reported publishes | Rejected publishes | Bytes sent | Bytes received |")
		fmt.Fprintln(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
		var deviceTotal DeviceMQTTTotals
		for _, stage := range input.StageResults {
			deviceTotal = addDeviceMQTTTotals(deviceTotal, stage.DeviceMQTTTotals)
			t := stage.DeviceMQTTTotals
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
				stage.Name, t.ConnectAttempts, t.ConnectSuccess, t.ConnectFail, t.Subscribes, t.ActiveConnections, t.ActiveSubscriptions, t.Publishes, t.ReceivedMessages, t.DeltaReceived, t.ReportedPublishes, t.RejectedPublishes, t.BytesSent, t.BytesReceived)
		}
		fmt.Fprintf(&b, "| total | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			deviceTotal.ConnectAttempts, deviceTotal.ConnectSuccess, deviceTotal.ConnectFail, deviceTotal.Subscribes, deviceTotal.ActiveConnections, deviceTotal.ActiveSubscriptions, deviceTotal.Publishes, deviceTotal.ReceivedMessages, deviceTotal.DeltaReceived, deviceTotal.ReportedPublishes, deviceTotal.RejectedPublishes, deviceTotal.BytesSent, deviceTotal.BytesReceived)
		fmt.Fprintln(&b)

		fmt.Fprintln(&b, "## APP/User Totals")
		fmt.Fprintln(&b, "| Window | Login attempts | Login success | Login fail | List devices | Read shadow | Desired writes | Received ACKs | Bytes sent | Bytes received |")
		fmt.Fprintln(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
		var appTotal AppUserTotals
		for _, stage := range input.StageResults {
			appTotal = addAppUserTotals(appTotal, stage.AppUserTotals)
			t := stage.AppUserTotals
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
				stage.Name, t.LoginAttempts, t.LoginSuccess, t.LoginFail, t.ListDevicesRequests, t.ReadShadowRequests, t.DesiredWrites, t.ReceivedAcks, t.BytesSent, t.BytesReceived)
		}
		fmt.Fprintf(&b, "| total | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			appTotal.LoginAttempts, appTotal.LoginSuccess, appTotal.LoginFail, appTotal.ListDevicesRequests, appTotal.ReadShadowRequests, appTotal.DesiredWrites, appTotal.ReceivedAcks, appTotal.BytesSent, appTotal.BytesReceived)
		fmt.Fprintln(&b)

		deviceTypeTotals := aggregateDeviceTypeTotals(input.StageResults)
		if len(deviceTypeTotals) > 0 {
			fmt.Fprintln(&b, "## Per-Type MQTT Totals")
			fmt.Fprintln(&b, "| Device type | Telemetry publishes | Event publishes | Desired writes | Delta received | Reported publishes | Bytes sent | Bytes received |")
			fmt.Fprintln(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
			for _, name := range sortedDeviceTypeTotalKeys(deviceTypeTotals) {
				t := deviceTypeTotals[name]
				fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d |\n", name, t.TelemetryPublishes, t.EventPublishes, t.DesiredWrites, t.DeltaReceived, t.ReportedPublishes, t.BytesSent, t.BytesReceived)
			}
			fmt.Fprintln(&b)
		}

		userActionTotals := aggregateInt64Maps(input.StageResults, func(stage StageResult) map[string]int64 { return stage.UserActionTotals })
		if len(userActionTotals) > 0 {
			fmt.Fprintln(&b, "## User Action Totals")
			fmt.Fprintln(&b, "| Action | Count |")
			fmt.Fprintln(&b, "| --- | ---: |")
			for _, name := range sortedKeys(userActionTotals) {
				fmt.Fprintf(&b, "| %s | %d |\n", name, userActionTotals[name])
			}
			fmt.Fprintln(&b)
		}

		usageWindowTotals := aggregateInt64Maps(input.StageResults, func(stage StageResult) map[string]int64 { return stage.UsageWindowTotals })
		if len(usageWindowTotals) > 0 {
			fmt.Fprintln(&b, "## Usage Window Totals")
			fmt.Fprintln(&b, "| Usage window | Count |")
			fmt.Fprintln(&b, "| --- | ---: |")
			for _, name := range sortedKeys(usageWindowTotals) {
				fmt.Fprintf(&b, "| %s | %d |\n", name, usageWindowTotals[name])
			}
			fmt.Fprintln(&b)
		}

		reasonRows := failureReasonRows(input.StageResults)
		if len(reasonRows) > 0 {
			fmt.Fprintln(&b, "## Failure Reasons")
			fmt.Fprintln(&b, "| Window | Reason | Count |")
			fmt.Fprintln(&b, "| --- | --- | ---: |")
			for _, row := range reasonRows {
				fmt.Fprintf(&b, "| %s | %s | %d |\n", row.Stage, row.Reason, row.Count)
			}
			fmt.Fprintln(&b)
		}

		detailRows := failureDetailRows(input.StageResults)
		if len(detailRows) > 0 {
			fmt.Fprintln(&b, "## Failure Details")
			fmt.Fprintln(&b, "| Window | Reason | Detail | Count |")
			fmt.Fprintln(&b, "| --- | --- | --- | ---: |")
			for _, row := range detailRows {
				fmt.Fprintf(&b, "| %s | %s | %s | %d |\n", row.Stage, row.Reason, redact(row.Detail), row.Count)
			}
			fmt.Fprintln(&b)
		}

		eventRows := failureEventRows(input.StageResults)
		if len(eventRows) > 0 {
			fmt.Fprintln(&b, "## Failure Event Samples")
			fmt.Fprintln(&b, "| Window | Reason | Detail | Phase | Device | Command | Event index | Session slot | Remaining ms | MQTT target | Reader error | At |")
			fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | ---: | ---: | ---: | --- | --- | --- |")
			for _, event := range eventRows {
				stage := firstNonEmpty(event.Stage, "unknown")
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %d | %d | %d | %s | %s | %s |\n",
					stage,
					redact(event.Reason),
					redact(event.Detail),
					redact(event.Phase),
					redact(event.DeviceID),
					redact(event.CommandID),
					event.EventIndex,
					event.SessionSlot,
					event.RemainingMS,
					redact(event.MQTTTarget),
					redact(event.ReaderError),
					redact(event.OccurredAt),
				)
			}
			fmt.Fprintln(&b)
		}
	}

	if strings.TrimSpace(input.ServerCorrelation.Status) != "" || len(input.ServerCorrelation.Checks) > 0 || len(input.ServerCorrelation.Reasons) > 0 {
		fmt.Fprintln(&b, "## Server Log Correlation")
		fmt.Fprintf(&b, "- status: %s\n", firstNonEmpty(input.ServerCorrelation.Status, "unknown"))
		for _, reason := range input.ServerCorrelation.Reasons {
			fmt.Fprintf(&b, "- reason: %s\n", redact(reason))
		}
		if len(input.ServerCorrelation.Checks) > 0 {
			fmt.Fprintln(&b, "| Source | Counter | Client total | Server total | Delta | Tolerance | Status |")
			fmt.Fprintln(&b, "| --- | --- | ---: | ---: | ---: | ---: | --- |")
			for _, check := range input.ServerCorrelation.Checks {
				fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %s |\n", check.Source, check.Counter, check.ClientTotal, check.ServerTotal, check.Delta, check.Tolerance, check.Status)
			}
		}
		fmt.Fprintln(&b)
	}

	if input.RuntimeLogCorrelation.ClientCommandEvents > 0 || input.RuntimeLogCorrelation.ServerRuntimeStreams > 0 || input.RuntimeLogCorrelation.Status != "" {
		fmt.Fprintln(&b, "## Runtime Log Stream Correlation")
		fmt.Fprintf(&b, "- status: %s\n", firstNonEmpty(input.RuntimeLogCorrelation.Status, "unknown"))
		fmt.Fprintf(&b, "- client command events: %d\n", input.RuntimeLogCorrelation.ClientCommandEvents)
		fmt.Fprintf(&b, "- server runtime streams: %d\n", input.RuntimeLogCorrelation.ServerRuntimeStreams)
		fmt.Fprintf(&b, "- missing streams: %d\n", input.RuntimeLogCorrelation.MissingStreamCount)
		fmt.Fprintf(&b, "- missing expected log sequences: %d\n", input.RuntimeLogCorrelation.MissingSequenceCount)
		if len(input.RuntimeLogCorrelation.MissingStreamSamples) > 0 {
			fmt.Fprintln(&b, "| Missing stream window | Device | Command | Runtime log stream |")
			fmt.Fprintln(&b, "| --- | --- | --- | --- |")
			for _, missing := range input.RuntimeLogCorrelation.MissingStreamSamples {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", redact(missing.Stage), redact(missing.DeviceID), redact(missing.CommandID), redact(missing.RuntimeLogStreamID))
			}
		}
		if len(input.RuntimeLogCorrelation.MissingSequenceSamples) > 0 {
			fmt.Fprintln(&b, "| Missing seq window | Device | Command | Runtime log stream | Seq | Source | Message |")
			fmt.Fprintln(&b, "| --- | --- | --- | --- | ---: | --- | --- |")
			for _, missing := range input.RuntimeLogCorrelation.MissingSequenceSamples {
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s | %s |\n", redact(missing.Stage), redact(missing.DeviceID), redact(missing.CommandID), redact(missing.RuntimeLogStreamID), missing.Seq, redact(missing.Source), redact(missing.Message))
			}
		}
		fmt.Fprintln(&b)
	}

	if len(input.StartCoordination.VMs) > 0 || input.StartCoordination.Mode != "" {
		fmt.Fprintln(&b, "## Load Generator Start Coordination")
		fmt.Fprintf(&b, "- mode: %s\n", firstNonEmpty(input.StartCoordination.Mode, "unknown"))
		fmt.Fprintf(&b, "- ready barrier: %s\n", firstNonEmpty(input.StartCoordination.ReadyBarrier, "-"))
		fmt.Fprintf(&b, "- start delay ms: %d\n", input.StartCoordination.StartDelayMS)
		fmt.Fprintf(&b, "- max start skew ms: %d\n", input.StartCoordination.MaxSkewMS)
		if len(input.StartCoordination.VMs) > 0 {
			fmt.Fprintln(&b)
			fmt.Fprintln(&b, "| VM | IP | Status | Ready at | Start signal received | Stage started | First connect | Stage completed | Disconnects | Error |")
			fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | ---: | --- |")
			for _, vm := range input.StartCoordination.VMs {
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %d | %s |\n",
					vm.Label,
					firstNonEmpty(vm.IP, "-"),
					firstNonEmpty(vm.Status, "-"),
					firstNonEmpty(vm.ReadyAt, "-"),
					firstNonEmpty(vm.StartSignalReceivedAt, "-"),
					firstNonEmpty(vm.StageStartedAt, "-"),
					firstNonEmpty(vm.FirstConnectAt, "-"),
					firstNonEmpty(vm.StageCompletedAt, "-"),
					vm.CoordinatorDisconnects,
					redact(firstNonEmpty(vm.Error, "-")),
				)
			}
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "## Load Generator Health")
	if input.LoadGeneratorHealthy {
		fmt.Fprintln(&b, "- load-generator healthy: true")
	} else {
		fmt.Fprintln(&b, "- load-generator saturated: true")
	}
	fmt.Fprintln(&b)

	if input.ServerEvidence.Sources != nil {
		fmt.Fprintln(&b, "## Server Evidence")
		if input.ServerEvidence.Complete {
			fmt.Fprintln(&b, "- server evidence: complete")
		} else {
			fmt.Fprintln(&b, "- server evidence: incomplete")
		}
		keys := make([]string, 0, len(input.ServerEvidence.Sources))
		for key := range input.ServerEvidence.Sources {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			source := input.ServerEvidence.Sources[key]
			fmt.Fprintf(&b, "- %s: available=%t", key, source.Available)
			if strings.TrimSpace(source.Detail) != "" {
				fmt.Fprintf(&b, " detail=%s", redact(source.Detail))
			}
			fmt.Fprintln(&b)
		}
		fmt.Fprintln(&b)
		counterRows := serverEvidenceCounterRows(input.ServerEvidence)
		if len(counterRows) > 0 {
			fmt.Fprintln(&b, "## Server Evidence Counters")
			fmt.Fprintln(&b, "| Source | Counter | Value |")
			fmt.Fprintln(&b, "| --- | --- | ---: |")
			for _, row := range counterRows {
				fmt.Fprintf(&b, "| %s | %s | %d |\n", row.Source, row.Counter, row.Value)
			}
			fmt.Fprintln(&b)
		}
		postgresRows := postgresPodResourceRows(input.ServerEvidence)
		if len(postgresRows) > 0 {
			fmt.Fprintln(&b, "## Postgres Pod Resource Usage")
			fmt.Fprintln(&b, "| Namespace | Pod | Samples | CPU p95 | Memory p95 |")
			fmt.Fprintln(&b, "| --- | --- | ---: | ---: | ---: |")
			for _, row := range postgresRows {
				fmt.Fprintf(&b, "| %s | %s | %d | %dm | %s |\n", row.Namespace, row.Pod, row.Samples, row.CPUP95Mil, formatBytesMi(row.MemoryP95Bytes))
			}
			fmt.Fprintln(&b)
		}
	}

	if len(input.SyncTelemetry.VMs) > 0 {
		fmt.Fprintln(&b, "## Sync/Provision Telemetry")
		fmt.Fprintln(&b, "| VM | Files transferred | Bytes transferred | Elapsed ms | Remote disk before | Remote disk after |")
		fmt.Fprintln(&b, "| --- | ---: | ---: | ---: | --- | --- |")
		for _, vm := range input.SyncTelemetry.VMs {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %s | %s |\n", vm.Label, vm.FilesTransferred, vm.BytesTransferred, vm.ElapsedMS, firstNonEmpty(vm.RemoteDiskBefore, "-"), firstNonEmpty(vm.RemoteDiskAfter, "-"))
		}
		fmt.Fprintln(&b)
	}

	if len(input.Notes) > 0 {
		fmt.Fprintln(&b, "## Notes")
		for _, note := range input.Notes {
			fmt.Fprintf(&b, "- %s\n", redact(note))
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}

func renderWebRTCPhase(b *strings.Builder, label string, attempts int64, successes int64) {
	fmt.Fprintf(b, "| %s | %d | %d | %.2f%% |\n", label, attempts, successes, percentInt64(successes, attempts))
}

func renderReportBrandConditions(b *strings.Builder, plan Plan) {
	if len(plan.BrandDistribution) == 0 {
		fmt.Fprintf(b, "- Brand: `%s`\n", plan.Conditions.Brandname)
		return
	}
	fmt.Fprintf(b, "- Brand plan: `%s`\n", plan.Conditions.BrandPlanFile)
	fmt.Fprintf(b, "- Brand clouds: %d\n", len(plan.BrandDistribution))
	fmt.Fprintf(b, "- Normal users: %d\n", plan.Conditions.Users)
	fmt.Fprintf(b, "- Developer users: %d\n", plan.Conditions.DeveloperUsers)
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Brand cloud | Devices | Normal users | Developer users |")
	fmt.Fprintln(b, "| --- | ---: | ---: | --- |")
	for _, brand := range plan.BrandDistribution {
		fmt.Fprintf(b, "| %s | %d | %d | %s |\n", brand.Brandname, brand.Devices, brand.NormalUsers, formatDeveloperUserRoles(brand.DeveloperUsers))
	}
	fmt.Fprintln(b)
}

func formatDeveloperUserRoles(roles map[string]int) string {
	if len(roles) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(roles))
	for role, count := range roles {
		if count > 0 {
			keys = append(keys, role)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, role := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", role, roles[role]))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

type serverEvidenceCounterRow struct {
	Source  string
	Counter string
	Value   int64
}

type postgresPodResourceRow struct {
	Namespace      string
	Pod            string
	Samples        int
	CPUP95Mil      int64
	MemoryP95Bytes int64
}

type stageDiagnosticRow struct {
	Stage             string
	Shard             int
	Target            int64
	Before            int64
	After             int64
	ConnectAttempts   int64
	ConnectSuccess    int64
	ConnectFail       int64
	CommandsScheduled int64
	CommandsAttempted int64
	CommandsPassed    int64
	SkipReason        string
}

func stageDiagnosticRows(stages []StageResult) []stageDiagnosticRow {
	rows := []stageDiagnosticRow{}
	for _, stage := range stages {
		for idx, item := range stage.StageDiagnostics {
			rows = append(rows, stageDiagnosticRow{
				Stage:             stage.Name,
				Shard:             idx,
				Target:            diagnosticInt64(item, "connected_target"),
				Before:            diagnosticInt64(item, "connected_before"),
				After:             diagnosticInt64(item, "connected_after"),
				ConnectAttempts:   diagnosticInt64(item, "connect_attempts"),
				ConnectSuccess:    diagnosticInt64(item, "connect_successes"),
				ConnectFail:       diagnosticInt64(item, "connect_failures"),
				CommandsScheduled: diagnosticInt64(item, "commands_scheduled"),
				CommandsAttempted: diagnosticInt64(item, "commands_attempted"),
				CommandsPassed:    diagnosticInt64(item, "commands_passed"),
				SkipReason:        diagnosticString(item, "skip_reason"),
			})
		}
	}
	return rows
}

func diagnosticString(values map[string]any, key string) string {
	if value, ok := values[key].(string); ok {
		return value
	}
	return ""
}

func diagnosticInt64(values map[string]any, key string) int64 {
	switch value := values[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func serverEvidenceCounterRows(evidence ServerEvidence) []serverEvidenceCounterRow {
	rows := []serverEvidenceCounterRow{}
	sources := make([]string, 0, len(evidence.Sources))
	for source := range evidence.Sources {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		counters := evidence.Sources[source].Counters
		keys := make([]string, 0, len(counters))
		for key := range counters {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if strings.HasPrefix(key, "runtime_log_stream.") {
				continue
			}
			rows = append(rows, serverEvidenceCounterRow{Source: source, Counter: key, Value: counters[key]})
		}
	}
	return rows
}

func postgresPodResourceRows(evidence ServerEvidence) []postgresPodResourceRow {
	type resourceSeries struct {
		cpu    []int64
		memory []int64
	}
	series := map[string]resourceSeries{}
	for _, source := range evidence.Sources {
		for _, sample := range source.Samples {
			if sample.Kind != "k8s_pod_top" {
				continue
			}
			if !strings.Contains(strings.ToLower(sample.Pod), "postgres") {
				continue
			}
			key := sample.Namespace + "\x00" + sample.Pod
			current := series[key]
			current.cpu = append(current.cpu, sample.CPUCoreMil)
			current.memory = append(current.memory, sample.MemoryBytes)
			series[key] = current
		}
	}
	keys := make([]string, 0, len(series))
	for key := range series {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]postgresPodResourceRow, 0, len(keys))
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		item := series[key]
		rows = append(rows, postgresPodResourceRow{
			Namespace:      parts[0],
			Pod:            parts[1],
			Samples:        len(item.cpu),
			CPUP95Mil:      percentile95Int64(item.cpu),
			MemoryP95Bytes: percentile95Int64(item.memory),
		})
	}
	return rows
}

func percentile95Int64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64{}, values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (95*len(sorted) + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > len(sorted) {
		idx = len(sorted)
	}
	return sorted[idx-1]
}

func formatBytesMi(value int64) string {
	return fmt.Sprintf("%dMi", (value+(1024*1024)-1)/(1024*1024))
}

func renderMap(b *strings.Builder, title string, values map[string]int) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Fprintf(b, "- %s:\n", title)
	for _, key := range keys {
		fmt.Fprintf(b, "  - %s: %d\n", displayName(key), values[key])
	}
}

func sortedDeviceProfileKeys(values map[string]DeviceProfile) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedUserProfileKeys(values map[string]UserProfile) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedDeviceTypeTotalKeys(values map[string]DeviceTypeTotals) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func aggregateDeviceTypeTotals(stages []StageResult) map[string]DeviceTypeTotals {
	out := map[string]DeviceTypeTotals{}
	for _, stage := range stages {
		for name, value := range stage.DeviceTypeTotals {
			total := out[name]
			total.TelemetryPublishes += value.TelemetryPublishes
			total.EventPublishes += value.EventPublishes
			total.DesiredWrites += value.DesiredWrites
			total.DeltaReceived += value.DeltaReceived
			total.ReportedPublishes += value.ReportedPublishes
			total.BytesSent += value.BytesSent
			total.BytesReceived += value.BytesReceived
			out[name] = total
		}
	}
	return out
}

func missingDeviceTypeEvidence(plan Plan, stages []StageResult) []string {
	required := requiredDeviceTypesForPlan(plan)
	if len(required) == 0 {
		return nil
	}
	totals := aggregateDeviceTypeTotals(stages)
	missing := []string{}
	for _, name := range required {
		if _, ok := totals[name]; !ok {
			missing = append(missing, name)
		}
	}
	return missing
}

func requiredDeviceTypesForPlan(plan Plan) []string {
	required := []string{}
	if len(plan.DeviceMix) > 0 {
		for name, count := range plan.DeviceMix {
			if count > 0 {
				required = append(required, name)
			}
		}
	} else {
		for _, bucket := range homeDiverseDeviceMixBuckets() {
			required = append(required, bucket.Name)
		}
	}
	sort.Strings(required)
	return required
}

func aggregateInt64Maps(stages []StageResult, selectMap func(StageResult) map[string]int64) map[string]int64 {
	out := map[string]int64{}
	for _, stage := range stages {
		for name, value := range selectMap(stage) {
			out[name] += value
		}
	}
	return out
}

type failureReasonRow struct {
	Stage  string
	Reason string
	Count  int64
}

type failureDetailRow struct {
	Stage  string
	Reason string
	Detail string
	Count  int64
}

func failureReasonRows(stages []StageResult) []failureReasonRow {
	rows := []failureReasonRow{}
	total := map[string]int64{}
	for _, stage := range stages {
		for _, reason := range sortedKeys(stage.FailureReasons) {
			count := stage.FailureReasons[reason]
			if count == 0 {
				continue
			}
			rows = append(rows, failureReasonRow{Stage: stage.Name, Reason: reason, Count: count})
			total[reason] += count
		}
	}
	for _, reason := range sortedKeys(total) {
		if total[reason] != 0 {
			rows = append(rows, failureReasonRow{Stage: "total", Reason: reason, Count: total[reason]})
		}
	}
	return rows
}

func failureDetailRows(stages []StageResult) []failureDetailRow {
	rows := []failureDetailRow{}
	total := map[string]map[string]int64{}
	for _, stage := range stages {
		for _, reason := range sortedNestedKeys(stage.FailureDetails) {
			for _, detail := range sortedKeys(stage.FailureDetails[reason]) {
				count := stage.FailureDetails[reason][detail]
				if count == 0 {
					continue
				}
				rows = append(rows, failureDetailRow{Stage: stage.Name, Reason: reason, Detail: detail, Count: count})
				if total[reason] == nil {
					total[reason] = map[string]int64{}
				}
				total[reason][detail] += count
			}
		}
	}
	for _, reason := range sortedNestedKeys(total) {
		for _, detail := range sortedKeys(total[reason]) {
			if total[reason][detail] != 0 {
				rows = append(rows, failureDetailRow{Stage: "total", Reason: reason, Detail: detail, Count: total[reason][detail]})
			}
		}
	}
	return rows
}

func failureEventRows(stages []StageResult) []FailureEvent {
	rows := []FailureEvent{}
	for _, stage := range stages {
		for _, event := range stage.FailureEvents {
			if strings.TrimSpace(event.Stage) == "" {
				event.Stage = stage.Name
			}
			rows = append(rows, event)
		}
	}
	return rows
}

func sortedKeys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringIntKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinInts(values []int, sep string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, sep)
}

func sortedNestedKeys(values map[string]map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func redact(value string) string {
	lower := strings.ToLower(value)
	for _, marker := range []string{"bearer ", "password", "private_key", "private key", "-----begin", "certificate_pem", "access_token", "refresh_token", "token=", "secret"} {
		if strings.Contains(lower, marker) {
			return "redacted sensitive detail"
		}
	}
	return value
}

func displayName(value string) string {
	switch value {
	case "light":
		return "Light"
	case "online_steady":
		return "Online steady"
	case "offline_desired_queue":
		return "Offline desired queue"
	case "flapping_reconnect":
		return "Flapping reconnect"
	case "air_conditioner":
		return "Air conditioner"
	case "smart_meter":
		return "Smart meter"
	default:
		return strings.ReplaceAll(value, "_", " ")
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
