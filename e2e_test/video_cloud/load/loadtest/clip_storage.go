package loadtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type clipUploadEvent struct {
	Offset   time.Duration
	DeviceID string
	Sequence int
}

// poissonClipSchedule creates one deterministic aggregate Poisson process and
// assigns each arrival to a camera. The count is intentionally stochastic:
// ClipCountPerDevice describes the expected daily rate, not an exact quota.
func poissonClipSchedule(deviceIDs []string, clipsPerDevice int, window time.Duration, seed int64) []clipUploadEvent {
	if len(deviceIDs) == 0 || clipsPerDevice <= 0 || window <= 0 {
		return nil
	}
	lambda := float64(len(deviceIDs)*clipsPerDevice) / window.Seconds()
	if lambda <= 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(seed))
	sequence := make(map[string]int, len(deviceIDs))
	events := make([]clipUploadEvent, 0, len(deviceIDs)*clipsPerDevice)
	var elapsed float64
	for {
		elapsed += rng.ExpFloat64() / lambda
		if elapsed >= window.Seconds() {
			break
		}
		deviceID := deviceIDs[rng.Intn(len(deviceIDs))]
		sequence[deviceID]++
		events = append(events, clipUploadEvent{
			Offset:   time.Duration(elapsed * float64(time.Second)),
			DeviceID: deviceID,
			Sequence: sequence[deviceID] - 1,
		})
	}
	return events
}

func (r *Runner) runClipStorageWorkload(ctx context.Context, cfg Config) []Operation {
	clipBody, err := os.ReadFile(cfg.ClipFixturePath)
	if err != nil {
		return []Operation{{Actor: ActorDevice, Name: "clip_upload", Success: false, ErrorClass: ClassMalformed, ErrorDetail: fmt.Sprintf("read clip fixture: %v", err)}}
	}
	thumbnailBody, err := os.ReadFile(cfg.ClipThumbnailPath)
	if err != nil {
		return []Operation{{Actor: ActorDevice, Name: "clip_upload", Success: false, ErrorClass: ClassMalformed, ErrorDetail: fmt.Sprintf("read thumbnail fixture: %v", err)}}
	}
	events := poissonClipSchedule(cfg.ClipDeviceIDs, cfg.ClipCountPerDevice, cfg.ClipScheduleWindow, cfg.ClipPoissonSeed)
	if len(events) == 0 {
		return nil
	}
	started := time.Now()
	sem := make(chan struct{}, cfg.ClipUploadConcurrency)
	results := make(chan Operation, len(events))
	var workers sync.WaitGroup
	for _, event := range events {
		event := event
		workers.Add(1)
		go func() {
			defer workers.Done()
			wait := time.Until(started.Add(event.Offset))
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			clipID := fmt.Sprintf("%s-%s-%03d", safeIDPart(cfg.RunID), safeIDPart(event.DeviceID), event.Sequence)
			meta := map[string]any{
				"devid":            event.DeviceID,
				"clipid":           clipID,
				"start_time_ms":    0,
				"end_time_ms":      15000,
				"duration_ms":      15000,
				"content_type":     "video/mp4",
				"size_bytes":       len(clipBody),
				"event_type":       "LoadTestRecording",
				"event_id":         clipID,
				"recorder_version": "loadtest-v1",
				"resolution":       "1920x1080",
				"bitrate":          3000000,
				"codec":            "h264",
			}
			fields, files := clipMultipartParts(clipID, meta, clipBody, thumbnailBody)
			op := r.postMultipart(ctx, cfg, ActorDevice, "clip_upload", event.DeviceID, "/upload_clip", fields, files, cfg.DeviceBearerFor(event.DeviceID))
			op.Evidence = appendEvidence(op.Evidence, fmt.Sprintf("clipid=%s bytes=%d schedule_offset_ms=%d", clipID, len(clipBody), event.Offset.Milliseconds()))
			results <- op
		}()
	}
	workers.Wait()
	close(results)
	operations := make([]Operation, 0, len(events))
	for op := range results {
		operations = append(operations, op)
	}
	operations = append(operations, r.verifyUploadedClipSamples(ctx, cfg, operations)...)
	return operations
}

func (r *Runner) verifyUploadedClipSamples(ctx context.Context, cfg Config, operations []Operation) []Operation {
	if cfg.AdminToken == "" && len(cfg.AppTokens) == 0 {
		return []Operation{{Actor: ActorApp, Name: "clip_verification", Skipped: true, SkipReason: "admin token not configured"}}
	}
	seenDevices := make(map[string]bool)
	verification := make([]Operation, 0, 15)
	for _, upload := range operations {
		if upload.Name != "clip_upload" || !upload.Success || seenDevices[upload.DeviceID] {
			continue
		}
		clipID := clipIDFromEvidence(upload.Evidence)
		if clipID == "" {
			continue
		}
		seenDevices[upload.DeviceID] = true
		bearer := cfg.AccountBearerFor(upload.DeviceID)
		if bearer == "" {
			bearer = cfg.AdminToken
		}
		verification = append(verification,
			r.post(ctx, cfg, ActorApp, "clip_total", upload.DeviceID, "", "/total_clips", map[string]any{"devid": upload.DeviceID}, bearer),
			r.post(ctx, cfg, ActorApp, "clip_enum", upload.DeviceID, "", "/enum_clips", map[string]any{"devid": upload.DeviceID, "offset": 0, "count": 10}, bearer),
			r.post(ctx, cfg, ActorApp, "clip_info", upload.DeviceID, "", "/get_clip_info", map[string]any{"devid": upload.DeviceID, "clipid": clipID}, bearer),
			r.downloadClipRange(ctx, cfg, "clip_download_range", upload.DeviceID, clipID, "bytes=0-15", bearer, false),
			r.post(ctx, cfg, ActorApp, "clip_delete", upload.DeviceID, "", "/delete_clip", map[string]any{"devid": upload.DeviceID, "clipid": clipID}, bearer),
		)
		if len(seenDevices) >= 3 {
			break
		}
	}
	return verification
}

func clipIDFromEvidence(evidence string) string {
	for _, field := range strings.Fields(evidence) {
		if strings.HasPrefix(field, "clipid=") {
			return strings.TrimPrefix(field, "clipid=")
		}
	}
	return ""
}

func clipMultipartParts(clipID string, meta map[string]any, clipBody, thumbnailBody []byte) (map[string][]byte, map[string]multipartFile) {
	rawMeta, _ := json.Marshal(meta)
	return map[string][]byte{}, map[string]multipartFile{
		"meta":      {Filename: clipID + ".json", ContentType: "application/json", Body: rawMeta},
		"clip":      {Filename: clipID + ".mp4", ContentType: "video/mp4", Body: clipBody},
		"thumbnail": {Filename: clipID + ".jpg", ContentType: "image/jpeg", Body: thumbnailBody},
	}
}

func summarizeClipStorage(cfg Config, operations []Operation) ClipStorageMetrics {
	metrics := ClipStorageMetrics{
		Enabled:          cfg.ClipSet == ClipSetStoragePoisson,
		CameraDevices:    len(cfg.ClipDeviceIDs),
		ExpectedClips:    len(cfg.ClipDeviceIDs) * cfg.ClipCountPerDevice,
		PoissonSeed:      cfg.ClipPoissonSeed,
		ScheduleWindowMS: cfg.ClipScheduleWindow.Milliseconds(),
	}
	if !metrics.Enabled {
		return metrics
	}
	latencies := make([]int64, 0)
	perCamera := make(map[string]int, len(cfg.ClipDeviceIDs))
	for _, op := range operations {
		if op.Name != "clip_upload" {
			continue
		}
		metrics.ActualArrivals++
		metrics.UploadAttempts++
		if op.Success {
			metrics.UploadSuccesses++
			perCamera[op.DeviceID]++
		}
		if !op.Success && !op.Skipped {
			metrics.UploadFailures++
		}
		if op.Success || op.StatusCode > 0 {
			latencies = append(latencies, op.LatencyMS)
		}
		for _, field := range strings.Fields(op.Evidence) {
			if !strings.HasPrefix(field, "bytes=") {
				continue
			}
			if bytes, err := strconv.ParseInt(strings.TrimPrefix(field, "bytes="), 10, 64); err == nil {
				metrics.TotalBytes += bytes
			}
		}
	}
	if metrics.UploadAttempts > 0 {
		metrics.SuccessRate = float64(metrics.UploadSuccesses) / float64(metrics.UploadAttempts)
	}
	metrics.P50LatencyMS = percentile(latencies, 50)
	metrics.P95LatencyMS = percentile(latencies, 95)
	metrics.P99LatencyMS = percentile(latencies, 99)
	for _, count := range perCamera {
		metrics.CamerasWithClips++
		if metrics.MinClipsPerCamera == 0 || count < metrics.MinClipsPerCamera {
			metrics.MinClipsPerCamera = count
		}
		if count > metrics.MaxClipsPerCamera {
			metrics.MaxClipsPerCamera = count
		}
	}
	return metrics
}
