package loadtest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	mathrand "math/rand"
	"net/http"
	"net/url"
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
	rng := mathrand.New(mathrand.NewSource(seed))
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
	results := make(chan Operation)
	var recipientKeys sync.Map
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
			ops := r.runDirectClipUpload(ctx, cfg, event.DeviceID, clipID, clipBody, thumbnailBody, &recipientKeys)
			for i := range ops {
				ops[i].Evidence = appendEvidence(ops[i].Evidence, fmt.Sprintf("clipid=%s bytes=%d schedule_offset_ms=%d", clipID, len(clipBody), event.Offset.Milliseconds()))
				results <- ops[i]
			}
		}()
	}
	go func() {
		workers.Wait()
		close(results)
	}()
	operations := make([]Operation, 0, len(events))
	for op := range results {
		operations = append(operations, op)
	}
	operations = append(operations, r.verifyUploadedClipSamples(ctx, cfg, operations)...)
	return operations
}

type directClipAuthorization struct {
	UploadID  string         `json:"upload_id"`
	ClipID    string         `json:"clip_id"`
	Clip      directClipPut  `json:"clip"`
	Thumbnail *directClipPut `json:"thumbnail"`
}

type directClipPut struct {
	ObjectKey string            `json:"object_key"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

func (r *Runner) runDirectClipUpload(ctx context.Context, cfg Config, deviceID, clipID string, clipBody, thumbnailBody []byte, keys *sync.Map) []Operation {
	started := time.Now()
	bearer := cfg.DeviceBearerFor(deviceID)
	recipient, keyOp := r.directClipRecipientKey(ctx, cfg, deviceID, bearer, keys)
	if !keyOp.Success {
		return appendDirectClipAggregate([]Operation{keyOp}, deviceID, clipID, started)
	}
	ciphertext, clipKey, err := encryptLegacyClip(recipient, clipBody)
	if err != nil {
		op := Operation{Actor: ActorDevice, Name: "clip_encrypt", DeviceID: deviceID, ErrorClass: ClassMalformed, ErrorDetail: redactDetail(err.Error())}
		return appendDirectClipAggregate([]Operation{keyOp, op}, deviceID, clipID, started)
	}
	body := map[string]any{
		"clip_id": clipID, "event_type": "LoadTestRecording", "start_time_ms": 0,
		"end_time_ms": 15000, "duration_ms": 15000, "content_type": "video/mp4",
		"size_bytes": len(ciphertext), "sha256": sha256Base64(ciphertext),
		"encryption": map[string]any{"algorithm": "legacy-ecdh-aes-ctr-v1", "clipkey": clipKey},
		"thumbnail":  map[string]any{"content_type": "image/jpeg", "size_bytes": len(thumbnailBody), "sha256": sha256Base64(thumbnailBody)},
		"metadata":   map[string]string{"event_id": clipID, "recorder_version": "loadtest-v2", "resolution": "1920x1080", "bitrate": "3000000", "codec": "h264"},
	}
	controlBody, _ := json.Marshal(body)
	authorize, raw := r.postRaw(ctx, cfg, ActorDevice, "clip_authorize", deviceID, "", "/v1/devices/"+deviceID+"/clip-uploads", body, bearer)
	authorize.Evidence = appendEvidence(authorize.Evidence, fmt.Sprintf("control_metadata_bytes=%d", len(controlBody)))
	ops := []Operation{keyOp, authorize}
	if !authorize.Success {
		return appendDirectClipAggregate(ops, deviceID, clipID, started)
	}
	var authz directClipAuthorization
	if err := json.Unmarshal(raw, &authz); err != nil || authz.UploadID == "" || authz.Clip.URL == "" {
		authorize.Success = false
		authorize.ErrorClass = ClassMalformed
		authorize.ErrorDetail = "invalid direct upload authorization response"
		ops[len(ops)-1] = authorize
		return appendDirectClipAggregate(ops, deviceID, clipID, started)
	}
	authorize.Evidence = appendEvidence(authorize.Evidence, "upload_id="+authz.UploadID+" object_key="+authz.Clip.ObjectKey)
	ops[len(ops)-1] = authorize
	clipPut := r.putDirectClipAsset(ctx, cfg, deviceID, "clip_put", authz.Clip, ciphertext)
	clipPut.Evidence = appendEvidence(clipPut.Evidence, "upload_id="+authz.UploadID+" object_key="+authz.Clip.ObjectKey)
	ops = append(ops, clipPut)
	if !clipPut.Success {
		return appendDirectClipAggregate(ops, deviceID, clipID, started)
	}
	if authz.Thumbnail != nil {
		thumbPut := r.putDirectClipAsset(ctx, cfg, deviceID, "thumbnail_put", *authz.Thumbnail, thumbnailBody)
		thumbPut.Evidence = appendEvidence(thumbPut.Evidence, "upload_id="+authz.UploadID+" object_key="+authz.Thumbnail.ObjectKey)
		ops = append(ops, thumbPut)
		if !thumbPut.Success {
			return appendDirectClipAggregate(ops, deviceID, clipID, started)
		}
	}
	complete := r.post(ctx, cfg, ActorDevice, "clip_complete", deviceID, "", "/v1/devices/"+deviceID+"/clip-uploads/"+authz.UploadID+"/complete", nil, bearer)
	complete.Evidence = appendEvidence(complete.Evidence, "upload_id="+authz.UploadID)
	ops = append(ops, complete)
	if !complete.Success {
		return appendDirectClipAggregate(ops, deviceID, clipID, started)
	}
	ready := r.waitDirectClipReady(ctx, cfg, deviceID, authz.UploadID, bearer)
	ops = append(ops, ready)
	return appendDirectClipAggregate(ops, deviceID, clipID, started)
}

func (r *Runner) directClipRecipientKey(ctx context.Context, cfg Config, deviceID, bearer string, keys *sync.Map) (string, Operation) {
	if value, ok := keys.Load(deviceID); ok {
		return value.(string), Operation{Actor: ActorDevice, Name: "clip_encryption_config", DeviceID: deviceID, Success: true, Evidence: "cached=true"}
	}
	op, raw := r.getRaw(ctx, cfg, ActorDevice, "clip_encryption_config", deviceID, "", "/api/devices/"+deviceID+"/config", bearer)
	if !op.Success {
		return "", op
	}
	var response struct {
		ClipUpload struct {
			RecipientPublicKey string `json:"recipient_public_key"`
		} `json:"clip_upload"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || strings.TrimSpace(response.ClipUpload.RecipientPublicKey) == "" {
		op.Success = false
		op.ErrorClass = ClassMalformed
		op.ErrorDetail = "clip recipient public key missing"
		return "", op
	}
	keys.Store(deviceID, response.ClipUpload.RecipientPublicKey)
	return response.ClipUpload.RecipientPublicKey, op
}

func (r *Runner) putDirectClipAsset(ctx context.Context, cfg Config, deviceID, name string, target directClipPut, body []byte) Operation {
	started := time.Now()
	op := Operation{Actor: ActorDevice, Name: name, DeviceID: deviceID}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodPut, target.URL, bytes.NewReader(body))
	if err == nil {
		for key, value := range target.Headers {
			req.Header.Set(key, value)
		}
		var resp *http.Response
		resp, err = r.client.Do(req)
		if resp != nil {
			op.StatusCode = resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				err = fmt.Errorf("object PUT returned HTTP %d", resp.StatusCode)
			}
		}
	}
	op.LatencyMS = time.Since(started).Milliseconds()
	op.Success = err == nil
	if err != nil {
		op.ErrorClass = ClassifyError(op.StatusCode, nil, err)
		op.ErrorDetail = redactDetail(err.Error())
	} else {
		op.Evidence = fmt.Sprintf("direct_object_bytes=%d", len(body))
	}
	return op
}

func (r *Runner) waitDirectClipReady(ctx context.Context, cfg Config, deviceID, uploadID, bearer string) Operation {
	started := time.Now()
	deadline := time.Now().Add(65 * time.Second)
	path := "/v1/devices/" + deviceID + "/clip-uploads/" + uploadID
	pollCfg := cfg
	if pollCfg.HTTPTimeout <= 0 || pollCfg.HTTPTimeout > 5*time.Second {
		pollCfg.HTTPTimeout = 5 * time.Second
	}
	var lastFailure Operation
	for time.Now().Before(deadline) {
		op, raw := r.getRaw(ctx, pollCfg, ActorDevice, "clip_verify_ready", deviceID, "", path, bearer)
		if !op.Success {
			if op.StatusCode >= 400 && op.StatusCode < 500 {
				return op
			}
			lastFailure = op
			select {
			case <-ctx.Done():
				return Operation{Actor: ActorDevice, Name: "clip_verify_ready", DeviceID: deviceID, ErrorClass: ClassTimeout, ErrorDetail: ctx.Err().Error(), LatencyMS: time.Since(started).Milliseconds()}
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		var status struct {
			State     string `json:"state"`
			ErrorCode string `json:"error_code"`
		}
		if err := json.Unmarshal(raw, &status); err != nil {
			op.Success = false
			op.ErrorClass = ClassMalformed
			op.ErrorDetail = err.Error()
			return op
		}
		switch status.State {
		case "ready":
			op.LatencyMS = time.Since(started).Milliseconds()
			op.Evidence = appendEvidence(op.Evidence, "upload_id="+uploadID+" state=ready")
			return op
		case "failed", "expired":
			op.Success = false
			op.ErrorClass = ClassHTTP
			op.ErrorDetail = "terminal upload state " + status.State + " " + status.ErrorCode
			op.LatencyMS = time.Since(started).Milliseconds()
			op.Evidence = appendEvidence(op.Evidence, "upload_id="+uploadID+" state="+status.State)
			return op
		}
		select {
		case <-ctx.Done():
			return Operation{Actor: ActorDevice, Name: "clip_verify_ready", DeviceID: deviceID, ErrorClass: ClassTimeout, ErrorDetail: ctx.Err().Error(), LatencyMS: time.Since(started).Milliseconds()}
		case <-time.After(500 * time.Millisecond):
		}
	}
	detail := "verification did not reach ready within 65s"
	if lastFailure.ErrorDetail != "" {
		detail += ": last poll: " + lastFailure.ErrorDetail
	}
	return Operation{Actor: ActorDevice, Name: "clip_verify_ready", DeviceID: deviceID, ErrorClass: ClassTimeout, ErrorDetail: detail, LatencyMS: time.Since(started).Milliseconds()}
}

func appendDirectClipAggregate(ops []Operation, deviceID, clipID string, started time.Time) []Operation {
	aggregate := Operation{Actor: ActorDevice, Name: "clip_upload", DeviceID: deviceID, Success: true, LatencyMS: time.Since(started).Milliseconds(), Evidence: "clipid=" + clipID}
	for _, op := range ops {
		for _, field := range strings.Fields(op.Evidence) {
			if strings.HasPrefix(field, "upload_id=") || strings.HasPrefix(field, "object_key=") || strings.HasPrefix(field, "state=") {
				aggregate.Evidence = appendEvidence(aggregate.Evidence, field)
			}
		}
		if !op.Success {
			aggregate.Success = false
			aggregate.ErrorClass = op.ErrorClass
			aggregate.ErrorDetail = op.Name + ": " + op.ErrorDetail
			break
		}
	}
	return append(ops, aggregate)
}

func sha256Base64(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func encryptLegacyClip(publicKey string, plaintext []byte) ([]byte, string, error) {
	keyData := []byte(strings.ReplaceAll(strings.ReplaceAll(publicKey, ";", "\n"), ",", "\n"))
	block, _ := pem.Decode(keyData)
	if block == nil {
		return nil, "", fmt.Errorf("recipient key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	recipient, ok := parsed.(*ecdsa.PublicKey)
	if !ok || recipient.Curve != elliptic.P256() {
		return nil, "", fmt.Errorf("recipient key is not P-256")
	}
	ephemeral, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return nil, "", err
	}
	x, _ := recipient.Curve.ScalarMult(recipient.X, recipient.Y, ephemeral.D.Bytes())
	shared := sha256.Sum256(x.Bytes())
	aesBlock, err := aes.NewCipher(shared[:16])
	if err != nil {
		return nil, "", err
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCTR(aesBlock, shared[:16]).XORKeyStream(ciphertext, plaintext)
	encoded, err := x509.MarshalPKIXPublicKey(&ephemeral.PublicKey)
	if err != nil {
		return nil, "", err
	}
	clipKey := strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded})), "\n", ",")
	return ciphertext, clipKey, nil
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
		info, infoRaw := r.postRaw(ctx, cfg, ActorApp, "clip_info", upload.DeviceID, "", "/get_clip_info", map[string]any{"devid": upload.DeviceID, "clipid": clipID}, bearer)
		storedClipKey := clipKeyFromInfo(infoRaw)
		samples := []Operation{
			r.post(ctx, cfg, ActorApp, "clip_total", upload.DeviceID, "", "/total_clips", map[string]any{"devid": upload.DeviceID}, bearer),
			r.post(ctx, cfg, ActorApp, "clip_enum", upload.DeviceID, "", "/enum_clips", map[string]any{"devid": upload.DeviceID, "offset": 0, "count": 10}, bearer),
			info,
			r.downloadClipRange(ctx, cfg, "clip_download_range", upload.DeviceID, clipID, "bytes=0-15", bearer, false),
			r.downloadDecryptedClipRange(ctx, cfg, upload.DeviceID, clipID, storedClipKey, bearer),
			r.downloadClipRange(ctx, cfg, "clip_thumbnail_download", upload.DeviceID, clipID+".jpg", "bytes=0-15", bearer, false),
			r.post(ctx, cfg, ActorApp, "clip_delete", upload.DeviceID, "", "/delete_clip", map[string]any{"devid": upload.DeviceID, "clipid": clipID}, bearer),
		}
		for i := range samples {
			samples[i].Evidence = appendEvidence(samples[i].Evidence, "clipid="+clipID)
		}
		verification = append(verification, samples...)
		if len(seenDevices) >= 3 {
			break
		}
	}
	return verification
}

func clipKeyFromInfo(raw []byte) string {
	var response struct {
		VideoClips []map[string]any `json:"video_clips"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.VideoClips) == 0 {
		return ""
	}
	key, _ := response.VideoClips[0]["clipkey"].(string)
	return key
}

func (r *Runner) downloadDecryptedClipRange(ctx context.Context, cfg Config, deviceID, clipID, storedClipKey, bearer string) Operation {
	op := Operation{Actor: ActorApp, Name: "clip_download_decrypt", DeviceID: deviceID}
	started := time.Now()
	query, err := buildEncryptedDownloadQuery(cfg.ClipUserPrivateKeyPath, cfg.ClipServerPublicKeyPath, storedClipKey)
	if err != nil {
		op.ErrorClass = ClassMalformed
		op.ErrorDetail = redactDetail(err.Error())
		return op
	}
	plaintext, err := os.ReadFile(cfg.ClipFixturePath)
	if err != nil || len(plaintext) < 16 {
		op.ErrorClass = ClassMalformed
		op.ErrorDetail = "read clip fixture for decryption verification"
		return op
	}
	downloadURL := fmt.Sprintf("%s/download/%s/%s?%s", strings.TrimRight(cfg.APIURL, "/"), url.PathEscape(deviceID), url.PathEscape(clipID), query.Encode())
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodGet, downloadURL, nil)
	if err == nil {
		req.Header.Set("Range", "bytes=0-15")
		req.Header.Set("Authorization", "Bearer "+bearer)
		var resp *http.Response
		resp, err = r.client.Do(req)
		if resp != nil {
			op.StatusCode = resp.StatusCode
			var body []byte
			body, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err == nil && resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("decrypted range returned HTTP %d", resp.StatusCode)
			}
			if err == nil && !bytes.Equal(body, plaintext[:16]) {
				err = fmt.Errorf("decrypted range does not match fixture plaintext")
			}
		}
	}
	op.LatencyMS = time.Since(started).Milliseconds()
	op.Success = err == nil
	if err != nil {
		op.ErrorClass = ClassifyError(op.StatusCode, nil, err)
		op.ErrorDetail = redactDetail(err.Error())
	} else {
		op.Evidence = "bytes=16 decrypted=true"
	}
	return op
}

func buildEncryptedDownloadQuery(userPrivatePath, serverPublicPath, storedClipKey string) (url.Values, error) {
	if strings.TrimSpace(storedClipKey) == "" {
		return nil, fmt.Errorf("clip info did not return clipkey")
	}
	privatePEM, err := os.ReadFile(userPrivatePath)
	if err != nil {
		return nil, fmt.Errorf("read clip user private key: %w", err)
	}
	privateBlock, _ := pem.Decode(privatePEM)
	if privateBlock == nil {
		return nil, fmt.Errorf("clip user private key is not PEM")
	}
	userPrivate, err := x509.ParseECPrivateKey(privateBlock.Bytes)
	if err != nil || userPrivate.Curve != elliptic.P256() {
		return nil, fmt.Errorf("clip user private key is not P-256")
	}
	storedPublic, err := parseP256PublicKey(storedClipKey)
	if err != nil {
		return nil, fmt.Errorf("parse stored clipkey: %w", err)
	}
	x, _ := storedPublic.Curve.ScalarMult(storedPublic.X, storedPublic.Y, userPrivate.D.Bytes())
	storedAES := sha256.Sum256(x.Bytes())
	serverPEM, err := os.ReadFile(serverPublicPath)
	if err != nil {
		return nil, fmt.Errorf("read clip server public key: %w", err)
	}
	serverPublic, err := parseP256PublicKey(string(serverPEM))
	if err != nil {
		return nil, fmt.Errorf("parse clip server public key: %w", err)
	}
	requestPrivate, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		return nil, err
	}
	x, _ = serverPublic.Curve.ScalarMult(serverPublic.X, serverPublic.Y, requestPrivate.D.Bytes())
	requestShared := sha256.Sum256(x.Bytes())
	wrappedKey := append([]byte(nil), storedAES[:16]...)
	block, err := aes.NewCipher(requestShared[:16])
	if err != nil {
		return nil, err
	}
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(wrappedKey, wrappedKey)
	requestPublicDER, err := x509.MarshalPKIXPublicKey(&requestPrivate.PublicKey)
	if err != nil {
		return nil, err
	}
	requestPublic := strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: requestPublicDER})), "\n", ",")
	return url.Values{"clipkey": {base64.StdEncoding.EncodeToString(wrappedKey)}, "pubkey": {requestPublic}}, nil
}

func parseP256PublicKey(value string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(strings.ReplaceAll(value, ";", "\n"), ",", "\n")))
	if block == nil {
		return nil, fmt.Errorf("public key is not PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := parsed.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() {
		return nil, fmt.Errorf("public key is not P-256")
	}
	return public, nil
}

func clipIDFromEvidence(evidence string) string {
	for _, field := range strings.Fields(evidence) {
		if strings.HasPrefix(field, "clipid=") {
			return strings.TrimPrefix(field, "clipid=")
		}
	}
	return ""
}

func summarizeClipStorage(cfg Config, operations []Operation) ClipStorageMetrics {
	metrics := ClipStorageMetrics{
		Enabled:             cfg.ClipSet == ClipSetStoragePoisson,
		CameraDevices:       len(cfg.ClipDeviceIDs),
		ExpectedClips:       len(cfg.ClipDeviceIDs) * cfg.ClipCountPerDevice,
		PoissonSeed:         cfg.ClipPoissonSeed,
		ScheduleWindowMS:    cfg.ClipScheduleWindow.Milliseconds(),
		MixedNonClipEnabled: cfg.ClipMixedNonClip,
	}
	if !metrics.Enabled {
		return metrics
	}
	latencies := make([]int64, 0)
	stageLatencies := map[string][]int64{}
	perCamera := make(map[string]int, len(cfg.ClipDeviceIDs))
	for _, op := range operations {
		if metrics.MixedNonClipEnabled && !op.Skipped && !isClipStorageOperation(op.Name) {
			metrics.NonClipAttempts++
			if op.Success {
				metrics.NonClipSuccesses++
			} else {
				metrics.NonClipFailures++
			}
		}
		switch op.Name {
		case "clip_authorize", "clip_put", "thumbnail_put", "clip_complete", "clip_verify_ready", "clip_info", "clip_enum", "clip_download_range", "clip_download_decrypt", "clip_thumbnail_download", "clip_delete":
			if op.Success || op.StatusCode > 0 {
				stageLatencies[op.Name] = append(stageLatencies[op.Name], op.LatencyMS)
			}
			if op.Name == "clip_verify_ready" && op.Success {
				metrics.ReadyUploads++
			}
		}
		for _, field := range strings.Fields(op.Evidence) {
			switch {
			case strings.HasPrefix(field, "control_metadata_bytes="):
				value, _ := strconv.ParseInt(strings.TrimPrefix(field, "control_metadata_bytes="), 10, 64)
				metrics.ControlPlaneBytes += value
			case strings.HasPrefix(field, "direct_object_bytes="):
				value, _ := strconv.ParseInt(strings.TrimPrefix(field, "direct_object_bytes="), 10, 64)
				metrics.DirectPutBytes += value
			}
		}
		if op.Name != "clip_upload" {
			continue
		}
		metrics.ActualArrivals++
		metrics.UploadAttempts++
		if op.Success {
			metrics.UploadSuccesses++
			perCamera[op.DeviceID]++
			if strings.Contains(op.Evidence, "upload_id=") && strings.Contains(op.Evidence, "object_key=") && strings.Contains(op.Evidence, "state=ready") {
				metrics.CorrelatedReady++
			}
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
	if metrics.NonClipAttempts > 0 {
		metrics.NonClipSuccessRate = float64(metrics.NonClipSuccesses) / float64(metrics.NonClipAttempts)
	}
	metrics.P50LatencyMS = percentile(latencies, 50)
	metrics.P95LatencyMS = percentile(latencies, 95)
	metrics.P99LatencyMS = percentile(latencies, 99)
	metrics.StageP95MS = make(map[string]int64, len(stageLatencies))
	for name, values := range stageLatencies {
		metrics.StageP95MS[name] = percentile(values, 95)
	}
	metrics.VerificationP95MS = percentile(stageLatencies["clip_verify_ready"], 95)
	metrics.VerificationP99MS = percentile(stageLatencies["clip_verify_ready"], 99)
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

func isClipStorageOperation(name string) bool {
	return strings.HasPrefix(name, "clip_") || name == "thumbnail_put"
}
