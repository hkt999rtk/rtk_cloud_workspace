package home100k

import (
	"errors"
	"reflect"
)

var (
	ErrDesiredRequiresApp = errors.New("desired state requires app or admin token")
	ErrVersionConflict    = errors.New("shadow version conflict")
)

const (
	PresenceOnlineSteady        = "online_steady"
	PresenceOfflineDesiredQueue = "offline_desired_queue"
	PresenceFlappingReconnect   = "flapping_reconnect"
)

type ShadowDocument struct {
	DeviceID    string         `json:"device_id"`
	Desired     map[string]any `json:"desired"`
	Reported    map[string]any `json:"reported"`
	Delta       map[string]any `json:"delta"`
	Version     int64          `json:"version"`
	ClientToken string         `json:"clientToken,omitempty"`
}

type DeviceActor struct {
	DeviceID string
	Presence string
	Online   bool
	applied  map[string]int
}

func NewShadowDocument(deviceID string) *ShadowDocument {
	return &ShadowDocument{
		DeviceID: deviceID,
		Desired:  map[string]any{},
		Reported: map[string]any{},
		Delta:    map[string]any{},
	}
}

func (d *ShadowDocument) ApplyDesired(actor string, desired map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	if actor != "app" && actor != "admin" {
		return ShadowDocument{}, ErrDesiredRequiresApp
	}
	if err := d.checkVersion(version); err != nil {
		return ShadowDocument{}, err
	}
	merge(d.Desired, desired)
	d.Version++
	d.ClientToken = clientToken
	d.Delta = computeDelta(d.Desired, d.Reported)
	return d.Snapshot(), nil
}

func (d *ShadowDocument) ApplyReported(actor string, reported map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	if err := d.checkVersion(version); err != nil {
		return ShadowDocument{}, err
	}
	merge(d.Reported, reported)
	d.Version++
	d.ClientToken = clientToken
	d.Delta = computeDelta(d.Desired, d.Reported)
	return d.Snapshot(), nil
}

func (d *ShadowDocument) Snapshot() ShadowDocument {
	return ShadowDocument{
		DeviceID:    d.DeviceID,
		Desired:     cloneMap(d.Desired),
		Reported:    cloneMap(d.Reported),
		Delta:       cloneMap(d.Delta),
		Version:     d.Version,
		ClientToken: d.ClientToken,
	}
}

func (d *ShadowDocument) checkVersion(version int64) error {
	if version != 0 && version != d.Version {
		return ErrVersionConflict
	}
	return nil
}

func NewDeviceActor(deviceID string, presence string) *DeviceActor {
	return &DeviceActor{
		DeviceID: deviceID,
		Presence: presence,
		Online:   presence != PresenceOfflineDesiredQueue,
		applied:  map[string]int{},
	}
}

func (a *DeviceActor) Disconnect() error {
	a.Online = false
	return nil
}

func (a *DeviceActor) ReconnectAndSync(doc *ShadowDocument) error {
	a.Online = true
	snapshot := doc.Snapshot()
	reported := map[string]any{}
	for key, desiredValue := range snapshot.Delta {
		if reportedValue, ok := snapshot.Reported[key]; ok && reflect.DeepEqual(reportedValue, desiredValue) {
			continue
		}
		reported[key] = desiredValue
		a.applied[key]++
	}
	if len(reported) == 0 {
		return nil
	}
	_, err := doc.ApplyReported("device", reported, "device-sync-"+a.DeviceID, snapshot.Version)
	return err
}

func (a *DeviceActor) AppliedCount(field string) int {
	return a.applied[field]
}

func merge(dst map[string]any, src map[string]any) {
	for key, value := range src {
		dst[key] = value
	}
}

func computeDelta(desired map[string]any, reported map[string]any) map[string]any {
	delta := map[string]any{}
	for key, desiredValue := range desired {
		reportedValue, ok := reported[key]
		if !ok || !reflect.DeepEqual(desiredValue, reportedValue) {
			delta[key] = desiredValue
		}
	}
	return delta
}

func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}
