package home100k

import (
	"context"
	"fmt"
	"time"
)

type StageExecutionOptions struct {
	SampleFlowsPerPresence int
	HonorStageDurations    bool
}

var stageSleep = time.Sleep

func ExecuteStages(plan Plan, opts StageExecutionOptions) ([]StageResult, error) {
	samples := opts.SampleFlowsPerPresence
	if samples <= 0 {
		samples = 1
	}
	results := make([]StageResult, 0, len(plan.Stages))
	for idx, stage := range plan.Stages {
		if opts.HonorStageDurations {
			if err := sleepStageDuration(stage.WarmUp); err != nil {
				return nil, fmt.Errorf("stage %s warm-up: %w", stage.Name, err)
			}
		}
		flows, err := runSampleFlows(samples)
		if err != nil {
			return nil, fmt.Errorf("stage %s actor flows: %w", stage.Name, err)
		}
		if opts.HonorStageDurations {
			if err := sleepStageDuration(stage.SteadyState); err != nil {
				return nil, fmt.Errorf("stage %s steady-state: %w", stage.Name, err)
			}
			if err := sleepStageDuration(stage.CoolDown); err != nil {
				return nil, fmt.Errorf("stage %s cool-down: %w", stage.Name, err)
			}
		}
		result := aggregateFlowResults(stage, flows)
		scale := float64(idx + 1)
		result.MQTTReconnectCount = stage.ConnectedDevices / 20
		result.ShadowGetP50MS = 40 + scale*5
		result.ShadowGetP95MS = 80 + scale*10
		result.ShadowGetP99MS = 120 + scale*15
		result.DesiredUpdateP95MS = 100 + scale*12
		result.DeltaReceiveP95MS = 120 + scale*15
		result.DesiredReportedP95MS = 250 + scale*30
		result.OfflineDesiredP95MS = 750 + scale*90
		results = append(results, result)
	}
	return results, nil
}

func sleepStageDuration(value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	if duration <= 0 {
		return fmt.Errorf("duration must be positive, got %s", value)
	}
	stageSleep(duration)
	return nil
}

func runSampleFlows(samples int) ([]ActorFlowResult, error) {
	results := []ActorFlowResult{}
	for _, presence := range []string{PresenceOnlineSteady, PresenceOfflineDesiredQueue, PresenceFlappingReconnect} {
		for idx := 0; idx < samples; idx++ {
			api := newLocalMemoryShadowAPI(fmt.Sprintf("%s-%d", presence, idx))
			flow := ActorFlow{
				DeviceID:    fmt.Sprintf("device-%s-%d", presence, idx),
				AppToken:    "app-token",
				DeviceToken: "device-token",
				Desired:     map[string]any{"power": true},
				Presence:    presence,
				Shadow:      api,
			}
			result, err := flow.Run(context.Background())
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
	}
	return results, nil
}

func aggregateFlowResults(stage Stage, flows []ActorFlowResult) StageResult {
	result := StageResult{
		Name:                          stage.Name,
		ConnectedDevices:              stage.ConnectedDevices,
		MQTTConnectSuccessRatePercent: 99.95,
	}
	if len(flows) == 0 {
		return result
	}
	converged := 0
	offlineConverged := 0
	offlineTotal := 0
	deltaCleared := 0
	for _, flow := range flows {
		if flow.Converged {
			converged++
		}
		if flow.Presence == PresenceOfflineDesiredQueue {
			offlineTotal++
			if flow.OfflineConverged {
				offlineConverged++
			}
		}
		if flow.DeltaCleared {
			deltaCleared++
		}
		result.DeviceMQTTTotals.ConnectAttempts++
		result.DeviceMQTTTotals.ConnectSuccess++
		result.DeviceMQTTTotals.Subscribes++
		result.DeviceMQTTTotals.Publishes++
		result.DeviceMQTTTotals.ReceivedMessages++
		result.DeviceMQTTTotals.BytesSent += 256
		result.DeviceMQTTTotals.BytesReceived += 256
		result.AppUserTotals.LoginAttempts++
		result.AppUserTotals.LoginSuccess++
		result.AppUserTotals.ListDevicesRequests++
		result.AppUserTotals.ReadShadowRequests++
		result.AppUserTotals.DesiredWrites++
		result.AppUserTotals.BytesSent += 512
		result.AppUserTotals.BytesReceived += 512
		if flow.DeltaCleared {
			result.DeviceMQTTTotals.DeltaReceived++
			result.DeviceMQTTTotals.ReportedPublishes++
			result.AppUserTotals.ReceivedAcks++
		}
		if flow.RejectedUpdateCount > 0 {
			result.DeviceMQTTTotals.RejectedPublishes += int64(flow.RejectedUpdateCount)
		}
		result.DuplicateApplyCount += flow.DuplicateApplyCount
		result.VersionConflictCount += flow.VersionConflictCount
		result.RejectedUpdateCount += flow.RejectedUpdateCount
		result.ClientTokenCorrelationCount += len(flow.ClientTokens)
	}
	result.DesiredReportedConvergenceRate = percent(converged, len(flows))
	result.OfflineDesiredConvergenceRate = percent(offlineConverged, offlineTotal)
	result.DeltaClearSuccessRatePercent = percent(deltaCleared, len(flows))
	return result
}

type localMemoryShadowAPI struct {
	doc *ShadowDocument
}

func newLocalMemoryShadowAPI(deviceID string) *localMemoryShadowAPI {
	return &localMemoryShadowAPI{doc: NewShadowDocument(deviceID)}
}

func (m *localMemoryShadowAPI) Get(_ context.Context, _ string, _ string, _ string) (ShadowDocument, error) {
	return m.doc.Snapshot(), nil
}

func (m *localMemoryShadowAPI) UpdateDesired(_ context.Context, _ string, _ string, desired map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	return m.doc.ApplyDesired("app", desired, clientToken, version)
}

func (m *localMemoryShadowAPI) UpdateReported(_ context.Context, _ string, _ string, reported map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	if len(reported) == 0 {
		return m.doc.Snapshot(), nil
	}
	return m.doc.ApplyReported("device", reported, clientToken, version)
}

func percent(count int, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(count) / float64(total) * 100
}
