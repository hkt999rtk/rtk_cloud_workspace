package home100k

import (
	"fmt"
	"sort"
	"strings"
)

type ReportInput struct {
	Plan                 Plan
	RunID                string
	ShadowEvidenceFound  bool
	ServerEvidenceFound  bool
	LoadGeneratorHealthy bool
	StageResults         []StageResult
	ServerEvidence       ServerEvidence
	ServerCorrelation    ServerCorrelation
	SyncTelemetry        SyncTelemetry
	Notes                []string
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

		fmt.Fprintln(&b, "## Device MQTT Totals")
		fmt.Fprintln(&b, "| Stage | Connect attempts | Connect success | Connect fail | Subscribes | Publishes | Received | Delta received | Reported publishes | Rejected publishes | Bytes sent | Bytes received |")
		fmt.Fprintln(&b, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
		var deviceTotal DeviceMQTTTotals
		for _, stage := range input.StageResults {
			deviceTotal = addDeviceMQTTTotals(deviceTotal, stage.DeviceMQTTTotals)
			t := stage.DeviceMQTTTotals
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
				stage.Name, t.ConnectAttempts, t.ConnectSuccess, t.ConnectFail, t.Subscribes, t.Publishes, t.ReceivedMessages, t.DeltaReceived, t.ReportedPublishes, t.RejectedPublishes, t.BytesSent, t.BytesReceived)
		}
		fmt.Fprintf(&b, "| total | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |\n",
			deviceTotal.ConnectAttempts, deviceTotal.ConnectSuccess, deviceTotal.ConnectFail, deviceTotal.Subscribes, deviceTotal.Publishes, deviceTotal.ReceivedMessages, deviceTotal.DeltaReceived, deviceTotal.ReportedPublishes, deviceTotal.RejectedPublishes, deviceTotal.BytesSent, deviceTotal.BytesReceived)
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
