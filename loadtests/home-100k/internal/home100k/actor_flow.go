package home100k

import (
	"context"
	"fmt"
	"reflect"
)

type ShadowAPI interface {
	Get(ctx context.Context, deviceID string, token string, clientToken string) (ShadowDocument, error)
	UpdateDesired(ctx context.Context, deviceID string, token string, desired map[string]any, clientToken string, version int64) (ShadowDocument, error)
	UpdateReported(ctx context.Context, deviceID string, token string, reported map[string]any, clientToken string, version int64) (ShadowDocument, error)
}

type ActorFlow struct {
	DeviceID    string
	AppToken    string
	DeviceToken string
	Desired     map[string]any
	Presence    string
	Shadow      ShadowAPI
}

type ActorFlowResult struct {
	Presence             string
	ClientTokens         []string
	Converged            bool
	OfflineConverged     bool
	DeltaCleared         bool
	DuplicateApplyCount  int
	VersionConflictCount int
	RejectedUpdateCount  int
}

func (f ActorFlow) Run(ctx context.Context) (ActorFlowResult, error) {
	if f.Shadow == nil {
		return ActorFlowResult{}, fmt.Errorf("shadow API is required")
	}
	if f.DeviceID == "" {
		return ActorFlowResult{}, fmt.Errorf("device id is required")
	}
	desiredClientToken := "desired-" + f.DeviceID
	reportedClientToken := "reported-" + f.DeviceID
	clientTokens := []string{desiredClientToken}
	desiredDoc, err := f.Shadow.UpdateDesired(ctx, f.DeviceID, f.AppToken, f.Desired, desiredClientToken, 0)
	if err != nil {
		return ActorFlowResult{Presence: f.Presence, ClientTokens: clientTokens, RejectedUpdateCount: 1}, err
	}

	deviceView := desiredDoc
	if f.Presence == PresenceOfflineDesiredQueue || f.Presence == PresenceFlappingReconnect {
		deviceView, err = f.Shadow.Get(ctx, f.DeviceID, f.DeviceToken, "get-"+f.DeviceID)
		if err != nil {
			return ActorFlowResult{}, err
		}
	}

	reported := map[string]any{}
	duplicates := 0
	for key, desiredValue := range deviceView.Delta {
		if current, ok := deviceView.Reported[key]; ok && reflect.DeepEqual(current, desiredValue) {
			duplicates++
			continue
		}
		reported[key] = desiredValue
	}
	if len(reported) > 0 {
		clientTokens = append(clientTokens, reportedClientToken)
		if _, err := f.Shadow.UpdateReported(ctx, f.DeviceID, f.DeviceToken, reported, reportedClientToken, deviceView.Version); err != nil {
			if err == ErrVersionConflict {
				return ActorFlowResult{Presence: f.Presence, ClientTokens: clientTokens, VersionConflictCount: 1}, nil
			}
			return ActorFlowResult{}, err
		}
	}

	finalDoc, err := f.Shadow.Get(ctx, f.DeviceID, f.DeviceToken, "verify-"+f.DeviceID)
	if err != nil {
		return ActorFlowResult{}, err
	}
	deltaCleared := len(finalDoc.Delta) == 0
	return ActorFlowResult{
		Presence:            f.Presence,
		ClientTokens:        clientTokens,
		Converged:           deltaCleared,
		OfflineConverged:    f.Presence != PresenceOfflineDesiredQueue || deltaCleared,
		DeltaCleared:        deltaCleared,
		DuplicateApplyCount: duplicates,
	}, nil
}
