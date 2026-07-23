package loadtest

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPlaybackSessionRangeUsesShortLivedURLWithoutBearer(t *testing.T) {
	tmp := t.TempDir()
	userPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userDER, err := x509.MarshalECPrivateKey(userPrivate)
	if err != nil {
		t.Fatal(err)
	}
	userPrivatePath := filepath.Join(tmp, "user-private.pem")
	if err := os.WriteFile(userPrivatePath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: userDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	userPublicDER, err := x509.MarshalPKIXPublicKey(&userPrivate.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	userPublic := strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: userPublicDER})), "\n", ",")
	fixture := []byte("0123456789abcdef-valid-mp4-fixture")
	_, storedClipKey, err := encryptLegacyClip(userPublic, fixture)
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(tmp, "clip.mp4")
	if err := os.WriteFile(fixturePath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	serverPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverPublicDER, err := x509.MarshalPKIXPublicKey(&serverPrivate.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	serverPublicPath := filepath.Join(tmp, "server-public.pem")
	if err := os.WriteFile(serverPublicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: serverPublicDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1/devices/cam-a/clips/clip-a/playback-session":
			if req.Header.Get("Authorization") != "Bearer app-token" {
				t.Fatalf("session bearer = %q", req.Header.Get("Authorization"))
			}
			var body map[string]string
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body["clipkey"] == "" || body["pubkey"] == "" {
				t.Fatalf("invalid playback session body: %#v, err=%v", body, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"playback_url": server.URL + "/download/cam-a/clip-a?token=short-lived"})
		case "/download/cam-a/clip-a":
			if req.Header.Get("Authorization") != "" {
				t.Fatalf("playback request leaked bearer header")
			}
			if req.Header.Get("Range") != "bytes=0-15" {
				t.Fatalf("playback Range = %q", req.Header.Get("Range"))
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(fixture[:16])
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
	}))
	defer server.Close()

	session, playback := NewRunner(server.Client()).playbackSessionRange(context.Background(), Config{
		APIURL:                  server.URL,
		HTTPTimeout:             time.Second,
		ClipFixturePath:         fixturePath,
		ClipUserPrivateKeyPath:  userPrivatePath,
		ClipServerPublicKeyPath: serverPublicPath,
	}, "cam-a", "clip-a", storedClipKey, "app-token")
	if !session.Success || !playback.Success {
		t.Fatalf("session=%#v playback=%#v", session, playback)
	}
	if !strings.Contains(playback.Evidence, "bearer_in_request=false") {
		t.Fatalf("playback evidence = %q", playback.Evidence)
	}
}

func TestPoissonClipScheduleIsDeterministicAndBounded(t *testing.T) {
	ids := []string{"cam-a", "cam-b", "cam-c"}
	one := poissonClipSchedule(ids, 10, 30*time.Minute, 20260719)
	two := poissonClipSchedule(ids, 10, 30*time.Minute, 20260719)
	if len(one) == 0 || len(one) != len(two) {
		t.Fatalf("schedule lengths = %d and %d", len(one), len(two))
	}
	if len(one) != 30 {
		t.Fatalf("schedule length = %d, want exact qualification count 30", len(one))
	}
	counts := map[string]int{}
	for i, event := range one {
		if event.Offset < 0 || event.Offset >= 30*time.Minute {
			t.Fatalf("event %d offset = %s, outside window", i, event.Offset)
		}
		if event != two[i] {
			t.Fatalf("event %d differs for same seed: %#v != %#v", i, event, two[i])
		}
		counts[event.DeviceID]++
	}
	for _, id := range ids {
		if counts[id] != 10 {
			t.Fatalf("camera %s count = %d, want 10", id, counts[id])
		}
	}
}

func TestPrepareClipRecipientKeysActivatesEachCameraWithUserPublicKey(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(t.TempDir(), "user-private.pem")
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	activated := map[string]bool{}
	deactivated := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer admin-token" {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		if strings.HasSuffix(req.URL.Path, "/deactivate") {
			deviceID := strings.TrimPrefix(strings.TrimSuffix(req.URL.Path, "/deactivate"), "/api/devices/")
			deactivated[deviceID] = true
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		deviceID := strings.TrimPrefix(strings.TrimSuffix(req.URL.Path, "/activate"), "/api/devices/")
		if body["devid"] != deviceID {
			t.Fatalf("body devid = %q, path device = %q", body["devid"], deviceID)
		}
		if _, err := parseP256PublicKey(body["clip_public_key"]); err != nil {
			t.Fatalf("clip public key: %v", err)
		}
		if deviceID == "cam-b" && !deactivated[deviceID] {
			http.Error(w, `{"status":"fail","reason":"public key mismatch"}`, http.StatusConflict)
			return
		}
		activated[deviceID] = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"endpoints_with_credentials": []map[string]string{{
				"service_id": "API", "client_auth_token": "must-not-enter-evidence",
			}},
		})
	}))
	defer server.Close()

	deviceTokens := map[string]string{"cam-a": "old-a", "cam-b": "old-b"}
	operations := NewRunner(server.Client()).prepareClipRecipientKeys(context.Background(), Config{
		APIURL:                 server.URL,
		HTTPTimeout:            time.Second,
		RunID:                  "run-clip",
		AdminToken:             "admin-token",
		DeviceTokens:           deviceTokens,
		ClipUserPrivateKeyPath: privatePath,
		ClipDeviceIDs:          []string{"cam-a", "cam-b", "cam-a"},
	})
	if len(operations) != 3 {
		t.Fatalf("operations = %#v", operations)
	}
	for _, operation := range operations {
		if !operation.Success {
			t.Fatalf("operation = %#v, want success", operation)
		}
		if strings.Contains(operation.Evidence, "must-not-enter-evidence") {
			t.Fatalf("operation leaked activation token: %#v", operation)
		}
	}
	if !activated["cam-a"] || !activated["cam-b"] || !deactivated["cam-b"] {
		t.Fatalf("activated = %#v deactivated = %#v", activated, deactivated)
	}
	if deviceTokens["cam-a"] != "must-not-enter-evidence" || deviceTokens["cam-b"] != "must-not-enter-evidence" {
		t.Fatalf("activation tokens were not refreshed: %#v", deviceTokens)
	}
}

func TestPutDirectClipAssetRetriesOneTimeout(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if attempts.Add(1) == 1 {
			time.Sleep(50 * time.Millisecond)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	operation := NewRunner(server.Client()).putDirectClipAsset(context.Background(), Config{
		HTTPTimeout: 10 * time.Millisecond,
	}, "cam-a", "clip_put", directClipPut{URL: server.URL}, []byte("clip"))
	if !operation.Success || attempts.Load() != 2 {
		t.Fatalf("operation=%#v attempts=%d, want one successful retry", operation, attempts.Load())
	}
	if !strings.Contains(operation.Evidence, "put_attempts=2") {
		t.Fatalf("evidence = %q", operation.Evidence)
	}
}

func TestDirectClipEncryptionMatchesLegacyECDHCTRContract(t *testing.T) {
	recipient, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKIXPublicKey(&recipient.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := strings.ReplaceAll(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded})), "\n", ",")
	plaintext := []byte("direct encrypted clip fixture")
	ciphertext, clipKey, err := encryptLegacyClip(publicKey, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if string(ciphertext) == string(plaintext) || len(ciphertext) != len(plaintext) {
		t.Fatalf("ciphertext length/content mismatch")
	}
	ephemeralBlock, _ := pem.Decode([]byte(strings.ReplaceAll(clipKey, ",", "\n")))
	if ephemeralBlock == nil {
		t.Fatal("clipkey is not a public PEM")
	}
	parsed, err := x509.ParsePKIXPublicKey(ephemeralBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ephemeral := parsed.(*ecdsa.PublicKey)
	x, _ := ephemeral.Curve.ScalarMult(ephemeral.X, ephemeral.Y, recipient.D.Bytes())
	shared := sha256.Sum256(x.Bytes())
	block, err := aes.NewCipher(shared[:16])
	if err != nil {
		t.Fatal(err)
	}
	decoded := make([]byte, len(ciphertext))
	cipher.NewCTR(block, shared[:16]).XORKeyStream(decoded, ciphertext)
	if string(decoded) != string(plaintext) {
		t.Fatalf("decrypted=%q want=%q", decoded, plaintext)
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

func TestClipStorageSummaryRequiresDirectReadyCorrelation(t *testing.T) {
	cfg := Config{ClipSet: ClipSetStoragePoisson, ClipDeviceIDs: []string{"cam-a"}, ClipCountPerDevice: 1, ClipScheduleWindow: time.Minute}
	operations := []Operation{
		{Name: "clip_authorize", Success: true, Evidence: "control_metadata_bytes=400 upload_id=up-1 object_key=clips/a.mp4"},
		{Name: "clip_put", Success: true, Evidence: "direct_object_bytes=4096 upload_id=up-1 object_key=clips/a.mp4"},
		{Name: "clip_verify_ready", Success: true, LatencyMS: 500, Evidence: "upload_id=up-1 state=ready"},
		{Name: "clip_upload", DeviceID: "cam-a", Success: true, Evidence: "clipid=clip-1 upload_id=up-1 object_key=clips/a.mp4 state=ready"},
	}
	metrics := summarizeClipStorage(cfg, operations)
	if metrics.CorrelatedReady != 1 || metrics.ControlPlaneBytes != 400 || metrics.DirectPutBytes != 4096 {
		t.Fatalf("metrics = %#v", metrics)
	}
	evaluation := ThresholdEvaluation{Passed: true}
	ApplyClipStorageThreshold(&evaluation, metrics)
	if !evaluation.Passed {
		t.Fatalf("threshold failures = %v", evaluation.Failures)
	}
}

func TestClipStorageMixedNonClipThresholdIsIndependent(t *testing.T) {
	cfg := Config{ClipSet: ClipSetStoragePoisson, ClipMixedNonClip: true}
	operations := make([]Operation, 0, 1001)
	operations = append(operations, Operation{Name: "clip_upload", Success: true})
	for i := 0; i < 1000; i++ {
		operations = append(operations, Operation{Name: "device_config", Success: i < 994})
	}
	metrics := summarizeClipStorage(cfg, operations)
	if metrics.NonClipAttempts != 1000 || metrics.NonClipSuccesses != 994 || metrics.NonClipSuccessRate != 0.994 {
		t.Fatalf("mixed metrics = %#v", metrics)
	}
	evaluation := ThresholdEvaluation{Passed: true}
	ApplyClipStorageThreshold(&evaluation, metrics)
	if evaluation.Passed || !strings.Contains(strings.Join(evaluation.Failures, " "), "non-clip success rate") {
		t.Fatalf("expected independent non-clip failure, got %#v", evaluation)
	}

	operations[995].Success = true
	metrics = summarizeClipStorage(cfg, operations)
	metrics.ReadyUploads = metrics.UploadSuccesses
	metrics.CorrelatedReady = metrics.UploadSuccesses
	metrics.DirectPutBytes = 2
	metrics.ControlPlaneBytes = 1
	evaluation = ThresholdEvaluation{Passed: true}
	ApplyClipStorageThreshold(&evaluation, metrics)
	if !evaluation.Passed {
		t.Fatalf("99.5%% non-clip traffic should pass: %v", evaluation.Failures)
	}
}

func TestSanitizeEvidenceRedactsPresignedURLs(t *testing.T) {
	raw := []byte(`{"upload_id":"upload-1","clip":{"object_key":"clips/device/clip.mp4","url":"https://storage.test/clip?X-Amz-Signature=secret"},"thumbnail_url":"https://api.test/thumb"}`)
	got := sanitizeEvidence(raw)
	if strings.Contains(got, "storage.test") || strings.Contains(got, "api.test") || strings.Contains(got, "secret") {
		t.Fatalf("sanitizeEvidence leaked URL: %s", got)
	}
	if !strings.Contains(got, "upload-1") || !strings.Contains(got, "clips/device/clip.mp4") {
		t.Fatalf("sanitizeEvidence removed correlation fields: %s", got)
	}
}
