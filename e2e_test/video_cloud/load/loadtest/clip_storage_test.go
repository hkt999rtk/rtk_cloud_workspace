package loadtest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
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
