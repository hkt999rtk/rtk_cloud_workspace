package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPoissonClipScheduleIsDeterministicAndBounded(t *testing.T) {
	ids := []string{"cam-a", "cam-b", "cam-c"}
	one := poissonClipSchedule(ids, 10, 30*time.Minute, 20260719)
	two := poissonClipSchedule(ids, 10, 30*time.Minute, 20260719)
	if len(one) == 0 || len(one) != len(two) {
		t.Fatalf("schedule lengths = %d and %d", len(one), len(two))
	}
	if len(one) < 20 || len(one) > 80 {
		t.Fatalf("schedule length = %d, want a plausible Poisson sample around 30", len(one))
	}
	for i, event := range one {
		if event.Offset < 0 || event.Offset >= 30*time.Minute {
			t.Fatalf("event %d offset = %s, outside window", i, event.Offset)
		}
		if event != two[i] {
			t.Fatalf("event %d differs for same seed: %#v != %#v", i, event, two[i])
		}
	}
}

func TestClipMultipartMetadataMatchesStorageContract(t *testing.T) {
	meta := map[string]any{
		"devid": "camera-1", "clipid": "run-camera-1-000", "start_time_ms": 0,
		"end_time_ms": 15000, "duration_ms": 15000, "content_type": "video/mp4", "size_bytes": 42,
		"event_type": "LoadTestRecording", "resolution": "1920x1080", "bitrate": 3000000, "codec": "h264",
	}
	fields, files := clipMultipartParts("run-camera-1-000", meta, []byte("clip"), []byte("thumbnail"))
	if len(fields) != 0 || files["meta"].ContentType != "application/json" || files["clip"].ContentType != "video/mp4" || files["thumbnail"].ContentType != "image/jpeg" {
		t.Fatalf("multipart parts = fields=%#v files=%#v", fields, files)
	}
	var decoded map[string]any
	if err := json.Unmarshal(files["meta"].Body, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"devid", "clipid", "start_time_ms", "end_time_ms", "duration_ms", "content_type", "size_bytes"} {
		if _, ok := decoded[required]; !ok {
			t.Fatalf("metadata missing required field %q: %#v", required, decoded)
		}
	}
	if strings.Contains(string(files["meta"].Body), "sha256") {
		t.Fatalf("storage upload metadata unexpectedly contains sha256: %s", files["meta"].Body)
	}
}

func TestValidateStoragePoissonRequiresFixturesAndCameraIDs(t *testing.T) {
	tmp := t.TempDir()
	clip := filepath.Join(tmp, "clip.mp4")
	thumbnail := filepath.Join(tmp, "thumbnail.jpg")
	if err := os.WriteFile(clip, []byte("clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbnail, []byte("thumbnail"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Profile:               ProfileSafeStaging,
		APIURL:                "http://example.test",
		ClipSet:               ClipSetStoragePoisson,
		ClipDeviceIDs:         []string{"cam-a"},
		ClipCountPerDevice:    10,
		ClipScheduleWindow:    30 * time.Minute,
		ClipFixturePath:       clip,
		ClipThumbnailPath:     thumbnail,
		ClipUploadConcurrency: 2,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("storage-poisson config should validate: %v", err)
	}
	cfg.ClipDeviceIDs = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("storage-poisson config without camera IDs should fail")
	}
}
