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
	StartCoordination     StartCoordination
	SyncTelemetry         SyncTelemetry
	Notes                 []string
}

func RenderReport(input ReportInput) string {
	status := "PASS"
	reasons := []string{}
	if !input.ShadowEvidenceFound {
		status = "INCOMPLETE"
		reasons = append(reasons, "Missing IoT Device Shadow evidence")
	}
	if !input.ServerEvidenceFound {
		status = "INCOMPLETE"
		reasons = append(reasons, "Missing server evidence")
	}
	if !input.LoadGeneratorHealthy {
		status = "INCOMPLETE"
		reasons = append(reasons, "Load-generator saturation invalidated server-capacity conclusion")
	}
	if !clientTargetCoverageComplete(input.Plan.Conditions, input.StageResults) {
		status = "INCOMPLETE"
		reasons = append(reasons, "Client target coverage is incomplete; sampled counters do not satisfy stage device/user targets")
	}
	switch strings.ToLower(strings.TrimSpace(input.ServerCorrelation.Status)) {
	case "fail":
		if status != "INCOMPLETE" {
			status = "FAIL"
		}
		reasons = append(reasons, "Server/client counter correlation mismatch")
	case "incomplete":
		status = "INCOMPLETE"
		if len(input.ServerCorrelation.Reasons) == 0 {
			reasons = append(reasons, "Server/client counter correlation is incomplete")
		}
		for _, reason := range input.ServerCorrelation.Reasons {
			reasons = append(reasons, "Server/client counter correlation incomplete: "+reason)
		}
	}
	switch strings.ToLower(strings.TrimSpace(input.RuntimeLogCorrelation.Status)) {
	case "fail":
		if status != "INCOMPLETE" {
			status = "FAIL"
		}
		reasons = append(reasons, "Runtime log stream correlation mismatch")
	case "incomplete":
		status = "INCOMPLETE"
		reasons = append(reasons, "Runtime log stream correlation is incomplete")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# 100K Home IoT Device Shadow Load Test Report\n\n")
	fmt.Fprintf(&b, "- Run ID: %s\n", firstNonEmpty(input.RunID, "<run_id>"))
	fmt.Fprintf(&b, "- Status: %s\n", status)
	for _, reason := range reasons {
		fmt.Fprintf(&b, "- %s\n", reason)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Status Summary")
	if len(reasons) == 0 {
		fmt.Fprintln(&b, "- status gate: passed")
	} else {
		for _, reason := range reasons {
			fmt.Fprintf(&b, "- status gate: %s\n", reason)
		}
	}
	if strings.TrimSpace(input.ServerCorrelation.Status) != "" {
		fmt.Fprintf(&b, "- server correlation: %s\n", input.ServerCorrelation.Status)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Test Conditions")
	fmt.Fprintf(&b, "- Env root: `%s`\n", input.Plan.Conditions.EnvRoot)
	fmt.Fprintf(&b, "- Brand: `%s`\n", input.Plan.Conditions.Brandname)
	fmt.Fprintf(&b, "- Region: `%s`\n", input.Plan.Conditions.Region)
	fmt.Fprintf(&b, "- Devices: %d\n", input.Plan.Conditions.Devices)
	fmt.Fprintf(&b, "- Users: %d\n", input.Plan.Conditions.Users)
	fmt.Fprintf(&b, "- Devices per user: %d\n", input.Plan.Conditions.DevicesPerUser)
	fmt.Fprintf(&b, "- Runner nofile limit: %d\n", input.Plan.Conditions.RunnerNofileLimit)
	fmt.Fprintf(&b, "- Device session model: `%s`\n", firstNonEmpty(input.Plan.Conditions.DeviceSessionModel, DefaultDeviceSession))
	fmt.Fprintf(&b, "- Runner read model: `%s`\n", firstNonEmpty(input.Plan.Conditions.RunnerReadModel, DefaultRunnerReadModel))
	fmt.Fprintln(&b, "- Runner read requirement: sustained MQTT reads through Go netpoll-backed connections and bounded per-device reader goroutines; command-time one-shot reads are not valid for capacity conclusions.")
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Counter Scope")
	fmt.Fprintln(&b, "- Device MQTT totals and APP/User totals are client-side runner counters.")
	fmt.Fprintln(&b, "- The current runner records synthetic actor sample counters; these totals are not proof that 100,000 real MQTT devices or 5,000 real app users exchanged traffic.")
	fmt.Fprintln(&b, "- A capacity `PASS` requires parsed server-side MQTT, APP/API, and IoT Device Shadow counters to correlate with client counters inside tolerance.")
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Scenario Mix")
	renderMap(&b, "Device mix", input.Plan.DeviceMix)
	renderMap(&b, "Presence mix", input.Plan.PresenceMix)
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

	fmt.Fprintln(&b, "## Stages")
	for _, stage := range input.Plan.Stages {
		fmt.Fprintf(&b, "- %s: %d connected devices, warm-up %s, steady %s, cool-down %s\n", stage.Name, stage.ConnectedDevices, stage.WarmUp, stage.SteadyState, stage.CoolDown)
	}
	fmt.Fprintln(&b)

	if len(input.StageResults) > 0 {
		fmt.Fprintln(&b, "## Stage Results")
		fmt.Fprintln(&b, "| Stage | Devices | MQTT connect | Reconnects | Shadow get p50 | Shadow get p95 | Shadow get p99 | Desired update p95 | Delta receive p95 | Desired->reported p95 | Offline desired p95 | desired/reported convergence | offline desired convergence | Delta clear | Conflicts | Rejected | Auth violations | Client tokens | Duplicate apply |")
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
			fmt.Fprintln(&b, "## Stage Diagnostics")
			fmt.Fprintln(&b, "| Stage | Shard | Target | Before | After | Connect attempts | Connect success | Connect fail | Commands scheduled | Commands attempted | Commands passed | Skip reason |")
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
		fmt.Fprintln(&b, "| Stage | Connect attempts | Connect success | Connect fail | New subscribes | Active connections | Active subscriptions | Publishes | Received | Delta received | Reported publishes | Rejected publishes | Bytes sent | Bytes received |")
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
		fmt.Fprintln(&b, "| Stage | Login attempts | Login success | Login fail | List devices | Read shadow | Desired writes | Received ACKs | Bytes sent | Bytes received |")
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

		reasonRows := failureReasonRows(input.StageResults)
		if len(reasonRows) > 0 {
			fmt.Fprintln(&b, "## Failure Reasons")
			fmt.Fprintln(&b, "| Stage | Reason | Count |")
			fmt.Fprintln(&b, "| --- | --- | ---: |")
			for _, row := range reasonRows {
				fmt.Fprintf(&b, "| %s | %s | %d |\n", row.Stage, row.Reason, row.Count)
			}
			fmt.Fprintln(&b)
		}

		detailRows := failureDetailRows(input.StageResults)
		if len(detailRows) > 0 {
			fmt.Fprintln(&b, "## Failure Details")
			fmt.Fprintln(&b, "| Stage | Reason | Detail | Count |")
			fmt.Fprintln(&b, "| --- | --- | --- | ---: |")
			for _, row := range detailRows {
				fmt.Fprintf(&b, "| %s | %s | %s | %d |\n", row.Stage, row.Reason, redact(row.Detail), row.Count)
			}
			fmt.Fprintln(&b)
		}

		eventRows := failureEventRows(input.StageResults)
		if len(eventRows) > 0 {
			fmt.Fprintln(&b, "## Failure Event Samples")
			fmt.Fprintln(&b, "| Stage | Reason | Detail | Phase | Device | Command | Event index | Session slot | Remaining ms | MQTT target | Reader error | At |")
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
			fmt.Fprintln(&b, "| Missing stream stage | Device | Command | Runtime log stream |")
			fmt.Fprintln(&b, "| --- | --- | --- | --- |")
			for _, missing := range input.RuntimeLogCorrelation.MissingStreamSamples {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", redact(missing.Stage), redact(missing.DeviceID), redact(missing.CommandID), redact(missing.RuntimeLogStreamID))
			}
		}
		if len(input.RuntimeLogCorrelation.MissingSequenceSamples) > 0 {
			fmt.Fprintln(&b, "| Missing seq stage | Device | Command | Runtime log stream | Seq | Source | Message |")
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

type serverEvidenceCounterRow struct {
	Source  string
	Counter string
	Value   int64
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
