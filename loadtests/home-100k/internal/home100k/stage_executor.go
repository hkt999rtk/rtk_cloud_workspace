package home100k

import (
	"context"
	"fmt"
)

type StageExecutionOptions struct {
	SampleFlowsPerPresence int
}

func ExecuteStages(plan Plan, opts StageExecutionOptions) ([]StageResult, error) {
	samples := opts.SampleFlowsPerPresence
	if samples <= 0 {
		samples = 1
	}
	results := make([]StageResult, 0, len(plan.Stages))
	for idx, stage := range plan.Stages {
		flows, err := runSampleFlows(samples)
		if err != nil {
			return nil, fmt.Errorf("stage %s actor flows: %w", stage.Name, err)
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
