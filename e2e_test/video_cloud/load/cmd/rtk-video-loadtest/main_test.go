package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/video_cloud/load/loadtest"
)

func TestLoadTokenMapFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte(`{"cam-1":"token-from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadTokenMapFlag("device-token-map", "", path)
	if err != nil {
		t.Fatal(err)
	}
	if got["cam-1"] != "token-from-file" {
		t.Fatalf("token map = %#v", got)
	}
}

func TestLoadTokenMapRejectsJSONAndFileTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte(`{"cam-1":"token-from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadTokenMapFlag("device-token-map", `{"cam-1":"token-from-json"}`, path); err == nil {
		t.Fatal("expected ambiguity error")
	}
}

func TestRunLoadDefaultsWebRTCMediaOnlySmokeToDeviceRouteSetOff(t *testing.T) {
	cfg := loadtest.Config{
		Profile:        loadtest.ProfileSmoke,
		Actors:         loadtest.ActorDevice + "," + loadtest.ActorViewer,
		DeviceRouteSet: loadtest.DeviceRouteSetSmoke,
		WebRTCMediaSet: loadtest.WebRTCMediaSetH264,
	}

	applyRunLoadDefaults(&cfg, runLoadFlagState{})

	if cfg.DeviceRouteSet != loadtest.DeviceRouteSetOff {
		t.Fatalf("DeviceRouteSet = %q, want %q", cfg.DeviceRouteSet, loadtest.DeviceRouteSetOff)
	}
}

func TestRunLoadPreservesExplicitDeviceRouteSetForWebRTCMediaOnlySmoke(t *testing.T) {
	cfg := loadtest.Config{
		Profile:        loadtest.ProfileSmoke,
		Actors:         loadtest.ActorDevice + "," + loadtest.ActorViewer,
		DeviceRouteSet: loadtest.DeviceRouteSetSmoke,
		WebRTCMediaSet: loadtest.WebRTCMediaSetH264,
	}

	applyRunLoadDefaults(&cfg, runLoadFlagState{deviceRouteSet: true})

	if cfg.DeviceRouteSet != loadtest.DeviceRouteSetSmoke {
		t.Fatalf("DeviceRouteSet = %q, want explicit %q", cfg.DeviceRouteSet, loadtest.DeviceRouteSetSmoke)
	}
}
