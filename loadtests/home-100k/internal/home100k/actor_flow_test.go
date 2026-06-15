package home100k

import (
	"context"
	"testing"
)

func TestOfflineDesiredActorFlowConvergesThroughShadowAPI(t *testing.T) {
	api := newMemoryShadowAPI()
	flow := ActorFlow{
		DeviceID:    "device-1",
		AppToken:    "app-token",
		DeviceToken: "device-token",
		Desired:     map[string]any{"power": true},
		Presence:    PresenceOfflineDesiredQueue,
		Shadow:      api,
	}

	result, err := flow.Run(context.Background())
	if err != nil {
		t.Fatalf("ActorFlow.Run() error = %v", err)
	}
	if !result.Converged || !result.OfflineConverged || !result.DeltaCleared {
		t.Fatalf("result did not converge: %#v", result)
	}
	if result.DuplicateApplyCount != 0 || result.VersionConflictCount != 0 {
		t.Fatalf("unexpected conflicts/duplicates: %#v", result)
	}
	if api.desiredWrites != 1 || api.reportedWrites != 1 || api.gets != 2 {
		t.Fatalf("api counts desired=%d reported=%d gets=%d", api.desiredWrites, api.reportedWrites, api.gets)
	}
	if len(result.ClientTokens) != 2 || result.ClientTokens[0] != "desired-device-1" || result.ClientTokens[1] != "reported-device-1" {
		t.Fatalf("client tokens = %#v", result.ClientTokens)
	}
}

func TestFlappingActorFlowDoesNotDuplicateApply(t *testing.T) {
	api := newMemoryShadowAPI()
	flow := ActorFlow{
		DeviceID:    "device-1",
		AppToken:    "app-token",
		DeviceToken: "device-token",
		Desired:     map[string]any{"brightness": 70},
		Presence:    PresenceFlappingReconnect,
		Shadow:      api,
	}

	first, err := flow.Run(context.Background())
	if err != nil {
		t.Fatalf("first ActorFlow.Run() error = %v", err)
	}
	second, err := flow.Run(context.Background())
	if err != nil {
		t.Fatalf("second ActorFlow.Run() error = %v", err)
	}
	if !first.Converged || !second.Converged {
		t.Fatalf("flow did not converge: first=%#v second=%#v", first, second)
	}
	if second.DuplicateApplyCount != 0 {
		t.Fatalf("second run duplicate apply count = %d, want 0", second.DuplicateApplyCount)
	}
	if api.reportedWrites != 1 {
		t.Fatalf("reported writes = %d, want 1", api.reportedWrites)
	}
}

type memoryShadowAPI struct {
	doc            *ShadowDocument
	desiredWrites  int
	reportedWrites int
	gets           int
}

func newMemoryShadowAPI() *memoryShadowAPI {
	return &memoryShadowAPI{doc: NewShadowDocument("device-1")}
}

func (m *memoryShadowAPI) Get(_ context.Context, _ string, _ string, _ string) (ShadowDocument, error) {
	m.gets++
	return m.doc.Snapshot(), nil
}

func (m *memoryShadowAPI) UpdateDesired(_ context.Context, _ string, _ string, desired map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	m.desiredWrites++
	return m.doc.ApplyDesired("app", desired, clientToken, version)
}

func (m *memoryShadowAPI) UpdateReported(_ context.Context, _ string, _ string, reported map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	if len(reported) > 0 {
		m.reportedWrites++
	}
	return m.doc.ApplyReported("device", reported, clientToken, version)
}
