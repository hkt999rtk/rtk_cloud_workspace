package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVideoStatePathUsesConfiguredStackName(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "env"))
	mkdir(t, filepath.Join(root, "state"))
	stackEnv := filepath.Join(root, "env", "stack.env")
	write(t, stackEnv, "CLOUD_STACK_NAME=video-cloud-stg-0529\n")
	want := filepath.Join(root, "state", "video-cloud-stg-0529.state.json")
	write(t, want, `{"stack":"video-cloud-stg-0529"}`)
	write(t, filepath.Join(root, "state", "video-cloud-staging.state.json"), `{"stack":"legacy"}`)

	if got := videoStatePath(root, stackEnv); got != want {
		t.Fatalf("videoStatePath = %q, want %q", got, want)
	}
}

func TestLatestHomeMQTTBindArtifactSkipsIncompleteLatestArtifact(t *testing.T) {
	root := t.TempDir()
	older := filepath.Join(root, "rtk-device-bind-older.json")
	newer := filepath.Join(root, "rtk-device-bind-newer.json")
	write(t, older, `{
  "brandname": "RTK",
  "assignments": [
    {"device_type": "light", "service_options": ["mqtt"]},
    {"device_type": "air_conditioner", "service_options": ["mqtt"]},
    {"device_type": "smart_meter", "service_options": ["mqtt"]}
  ]
}`)
	write(t, newer, `{
  "brandname": "RTK",
  "assignments": [
    {"device_type": "camera", "service_options": ["mqtt", "video_streaming"]}
  ]
}`)
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	got := latestHomeMQTTBindArtifact(filepath.Join(root, "rtk-device-bind-*.json"), "rtk")
	if got != older {
		t.Fatalf("latestHomeMQTTBindArtifact = %q, want %q", got, older)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
