package billingbackup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	logger "github.com/hkt999rtk/rtk_cloud_logger"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

type memoryStore struct {
	mu     sync.Mutex
	data   map[string][]byte
	fail   bool
	writes int
}

func (s *memoryStore) Read(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[key]
	if !ok {
		return nil, ErrObjectMissing
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(b))), nil
}
func (s *memoryStore) PutImmutable(_ context.Context, key string, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("injected object write failure")
	}
	if old, ok := s.data[key]; ok && !bytes.Equal(old, b) {
		return errors.New("immutable collision")
	}
	s.data[key] = bytes.Clone(b)
	s.writes++
	return nil
}

type captureWriter struct{ s *memoryStore }

func (w captureWriter) PutImmutable(ctx context.Context, key, path string, size int64, sha string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(b)) != size || billingarchive.Digest(b) != sha {
		return errors.New("writer mismatch")
	}
	return w.s.PutImmutable(ctx, key, b)
}
func privateTestDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	return d
}
func fixture(t *testing.T) (Engine, string, *memoryStore) {
	t.Helper()
	ctx := context.Background()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	pub, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	objects := &memoryStore{data: map[string][]byte{}}
	inbox, err := logger.OpenBillingInbox(filepath.Join(privateTestDir(t), "inbox.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Close()
	cfg := logger.LifecycleConfig{Environment: "dev", Stack: "rtk", ScratchDir: privateTestDir(t), ScratchCapacityBytes: 8 << 30, EncryptionKeyID: "encryption-v1", Recipients: []string{id.Recipient().String()}, VerifierKeys: map[string]string{"verifier-v1": base64.StdEncoding.EncodeToString(pub)}, ObjectStore: captureWriter{objects}, PartBytes: 1 << 20, BackupEnabled: true}
	if err = inbox.ConfigureLifecycle(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err = inbox.Migrate(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		usage := map[string]any{"usage_id": fmt.Sprintf("usage-%d", i), "service_code": "mqtt", "brand_cloud_id": "brand", "event_time": at, "window_start": at.Add(-time.Minute), "window_end": at, "meter_epoch": "epoch", "sequence": json.Number("9007199254740993"), "source": "meter", "measurements": []any{map[string]any{"metric_code": "publish_bytes", "unit": "bytes", "quantity": json.Number("9007199254740993")}}}
		event := logger.LogEvent{EventID: fmt.Sprintf("event-%d", i), Time: at, Level: "info", Message: "usage", Service: "video-cloud", Env: "dev", Version: "test", Host: "host", Unit: "unit", Source: "billing_usage", Stream: "billing_usage", Fields: map[string]any{"usage_event": usage}}
		if err = inbox.InsertEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	capture, err := inbox.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e := Engine{Config: Config{Version: 1, Environment: "dev", Stack: "rtk", StoreID: capture.Manifest.StoreID, Directory: privateTestDir(t), LoggerURL: "http://localhost:8000", BillingURL: "http://localhost:8001", Remote: Remote{Endpoint: "https://example.invalid", Region: "us-sea", SigningRegion: "us-east-1", Bucket: "rtk-cloud-dev-billing-backup-us-sea"}, VerifierKeyID: "verifier-v1", VerifierKeys: cfg.VerifierKeys, EncryptionRecipients: map[string][]string{cfg.EncryptionKeyID: cfg.Recipients}}, Objects: objects, Identities: []age.Identity{id}, Signer: signer}
	return e, Prefix(capture.Manifest, "snapshot"), objects
}

func TestVerifyCaptureRoundTripImmutableRetryAndExactIntegers(t *testing.T) {
	e, prefix, objects := fixture(t)
	ctx := context.Background()
	// Retry after receipt publication failure must reuse the persisted signature.
	objects.fail = true
	if _, err := e.Verify(ctx, prefix); err == nil {
		t.Fatal("missing immutable publication failure")
	}
	objects.fail = false
	first, err := e.Verify(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Verify(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first.Completion)
	b, _ := json.Marshal(second.Completion)
	if !bytes.Equal(a, b) {
		t.Fatal("receipt bytes changed on retry")
	}
	records, refs, m, err := e.ReadRange(ctx, prefix, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(refs) != 2 || refs[0].Sequence != 2 || m.HighWater != 3 {
		t.Fatal(records, refs, m)
	}
	r, err := logger.ValidateBillingExportRecord(records[0])
	if err != nil {
		t.Fatal(err)
	}
	quantity := r.Event.Fields["usage_event"].(map[string]any)["measurements"].([]any)[0].(map[string]any)["quantity"].(json.Number)
	if quantity.String() != "9007199254740993" {
		t.Fatal("lost financial precision", quantity)
	}
	files, err := os.ReadDir(e.Config.Directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "verify-") || strings.HasPrefix(f.Name(), "range-") {
			t.Fatal("plaintext operation directory retained")
		}
	}
}

func TestVerifierRejectsMissingCorruptObjectsAndRecipientSubstitution(t *testing.T) {
	for _, damage := range []string{"missing", "cipher", "snapshot-digest", "recipient", "forged-completion"} {
		t.Run(damage, func(t *testing.T) {
			e, prefix, objects := fixture(t)
			ctx := context.Background()
			m, raw, err := e.readManifest(ctx, prefix)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "missing":
				delete(objects.data, Prefix(m, m.Parts[0].Kind)+"/"+m.Parts[0].Name)
			case "cipher":
				key := Prefix(m, m.Parts[0].Kind) + "/" + m.Parts[0].Name
				objects.data[key][10] ^= 1
			case "snapshot-digest":
				m.SnapshotSHA256 = strings.Repeat("0", 64)
				objects.data[prefix+"/manifest.json"], err = billingarchive.MarshalManifest(m)
				if err != nil {
					t.Fatal(err)
				}
			case "recipient":
				m.RecipientFingerprints[0] = strings.Repeat("0", 64)
				objects.data[prefix+"/manifest.json"], err = billingarchive.MarshalManifest(m)
				if err != nil {
					t.Fatal(err)
				}
			case "forged-completion":
				_, bad, _ := ed25519.GenerateKey(rand.Reader)
				c, _ := billingarchive.SignCompletion(billingarchive.PayloadFor(m, raw, "verifier-v1", "billing-raw-v1", "test", time.Now()), bad)
				objects.data[prefix+"/complete.json"], _ = json.Marshal(c)
			}
			if _, err = e.Verify(ctx, prefix); err == nil {
				t.Fatal("unsafe archive verified")
			}
			if _, exists := objects.data[prefix+"/complete.json"]; exists && damage != "forged-completion" {
				t.Fatal("completion published after verification failure")
			}
		})
	}
}

func TestReadRangeRequiresIndependentVerifiedArchive(t *testing.T) {
	e, prefix, _ := fixture(t)
	if _, _, _, err := e.ReadRange(context.Background(), prefix, 1, 1); err == nil {
		t.Fatal("unverified raw data accepted")
	}
	if _, err := e.Verify(context.Background(), prefix); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]uint64{{0, 1}, {1, 1001}, {3, 4}, {4, 3}} {
		if _, _, _, err := e.ReadRange(context.Background(), prefix, r[0], r[1]); err == nil {
			t.Fatal("invalid range", r)
		}
	}
}

func TestVerifierRejectsExportReceiptClockSubstitution(t *testing.T) {
	e, prefix, objects := fixture(t)
	ctx := context.Background()
	m, _, err := e.readManifest(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	for i, part := range m.Parts {
		if part.Kind != "events" {
			continue
		}
		var plain bytes.Buffer
		if err = billingarchive.DecodePart(ctx, &plain, bytes.NewReader(objects.data[Prefix(m, part.Kind)+"/"+part.Name]), e.Identities, part); err != nil {
			t.Fatal(err)
		}
		var replacement bytes.Buffer
		for _, line := range bytes.Split(bytes.TrimSpace(plain.Bytes()), []byte("\n")) {
			var record billingarchive.ExportRecord
			if err = json.Unmarshal(line, &record); err != nil {
				t.Fatal(err)
			}
			record.ReceivedAt = time.Now().UTC().Add(-100 * 24 * time.Hour)
			if err = json.NewEncoder(&replacement).Encode(record); err != nil {
				t.Fatal(err)
			}
			if record.ReceivedAt.After(m.MaxReceivedAt) || m.MaxReceivedAt.After(time.Now().UTC().Add(-90*24*time.Hour)) {
				m.MaxReceivedAt = record.ReceivedAt
			}
		}
		var ciphertext bytes.Buffer
		part.PlainBytes, part.PlainSHA256, err = billingarchive.EncodePart(ctx, &ciphertext, bytes.NewReader(replacement.Bytes()), e.Config.EncryptionRecipients[m.EncryptionKeyID], billingarchive.DefaultPartBytes)
		if err != nil {
			t.Fatal(err)
		}
		part.CipherBytes = int64(ciphertext.Len())
		part.CipherSHA256 = billingarchive.Digest(ciphertext.Bytes())
		m.Parts[i] = part
		objects.data[Prefix(m, part.Kind)+"/"+part.Name] = ciphertext.Bytes()
	}
	objects.data[prefix+"/manifest.json"], err = billingarchive.MarshalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Verify(ctx, prefix); err == nil {
		t.Fatal("export clock not bound to original trusted snapshot receipt")
	}
}

func TestCustodyRejectsWorldReadableSymlinkAndWrongRole(t *testing.T) {
	root := privateTestDir(t)
	dir := filepath.Join(root, "operator", "recovery", "billing-backup")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, _ := age.GenerateX25519Identity()
	file := filepath.Join(dir, "key.agekey")
	if err := os.WriteFile(file, []byte(id.String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIdentity(root, file); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIdentity(root, file); err == nil {
		t.Fatal("world-readable key accepted")
	}
	os.Chmod(file, 0600)
	link := filepath.Join(dir, "linked.agekey")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIdentity(root, link); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := ReadSigner(root, file, nil); err == nil {
		t.Fatal("age key reused as signing key")
	}
}

func TestTransportNeverFollowsRedirectOrTrustsCacheableEvidence(t *testing.T) {
	for _, mode := range []string{"ok", "redirect", "cache", "404", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
					t.Error("wrong credential")
				}
				w.Header().Set("Cache-Control", "no-store")
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://example.invalid")
					w.WriteHeader(302)
				case "cache":
					w.Header().Set("Cache-Control", "public")
					fmt.Fprint(w, `{}`)
				case "404":
					w.WriteHeader(404)
				case "trailing":
					fmt.Fprint(w, `{} {}`)
				default:
					fmt.Fprint(w, `{"sequence":"9007199254740993"}`)
				}
			}))
			defer server.Close()
			c := Client{BaseURL: server.URL, Token: strings.Repeat("x", 32)}
			var result map[string]any
			err := c.Request(context.Background(), http.MethodGet, "/v1/internal/status", nil, &result)
			if mode == "ok" {
				if err != nil || result["sequence"] != "9007199254740993" {
					t.Fatal(err, result)
				}
			} else if err == nil {
				t.Fatal("unsafe response accepted")
			}
			if mode == "404" {
				var status *HTTPError
				if !errors.As(err, &status) || status.Status != 404 {
					t.Fatal("lost authoritative status", err)
				}
			}
		})
	}
}

func TestSavedActiveDecisionResumesSameIDAfterInterruptedApply(t *testing.T) {
	e, _, _ := fixture(t)
	ctx := context.Background()
	p := retirementPlan{OperationID: "retire-existing", PolicyID: "policy", PolicyVersion: 1, ClearanceID: "clearance", Environment: e.Config.Environment, StoreID: e.Config.StoreID, FromSequence: 1, ThroughSequence: 1, SetID: "set", ArchiveManifestSHA256: strings.Repeat("a", 64), Records: []billingarchive.RecordBinding{{Sequence: 1, LoggerContentSHA256: strings.Repeat("b", 64), UsageID: "usage", EventSHA256: strings.Repeat("c", 64)}}}
	p.PlanSHA256 = planDigest(p)
	if err := atomicJSON(filepath.Join(e.Config.Directory, p.OperationID+".plan.json"), p); err != nil {
		t.Fatal(err)
	}
	status := "ACTIVE"
	applyCalls, operationPosts := 0, 0
	failApply := true
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(struct {
				retirementPlan
				Status string `json:"status"`
			}{p, status})
		case strings.HasSuffix(r.URL.Path, "/resolve"):
			status = "COMPLETED"
			fmt.Fprint(w, `{}`)
		default:
			operationPosts++
			w.WriteHeader(500)
		}
	}))
	defer authority.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasSuffix(r.URL.Path, "/apply") {
			applyCalls++
			if failApply {
				w.WriteHeader(503)
				return
			}
		}
		fmt.Fprint(w, `{}`)
	}))
	defer local.Close()
	lc, ac := Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}, Client{BaseURL: authority.URL, Token: strings.Repeat("a", 32)}
	if _, err := resumePending(ctx, e, lc, ac); err == nil {
		t.Fatal("ambiguous apply treated as resolved")
	}
	failApply = false
	if n, err := resumePending(ctx, e, lc, ac); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err := resumePending(ctx, e, lc, ac); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if operationPosts != 0 || applyCalls != 2 {
		t.Fatal("new intent or skipped retry", operationPosts, applyCalls)
	}
}

func TestPlanDigestDoesNotBindVolatileVerificationTimestamp(t *testing.T) {
	p := retirementPlan{OperationID: "operation", PolicyID: "policy", PolicyVersion: 1, ClearanceID: "clearance", Environment: "dev", StoreID: strings.Repeat("a", 32), FromSequence: 1, ThroughSequence: 1, SetID: "set", ArchiveManifestSHA256: strings.Repeat("a", 64), Records: []billingarchive.RecordBinding{{Sequence: 1, LoggerContentSHA256: strings.Repeat("b", 64), UsageID: "usage", EventSHA256: strings.Repeat("c", 64)}}}
	first := planDigest(p)
	if first != "d97b684a97aad4e9b1ef6f7ba57e83436bb39e850a10cbac79385db0d1e76ebb" {
		t.Fatal("Billing authority wire mismatch", first)
	}
	p.RangeProof.SignatureB64 = "volatile"
	if planDigest(p) != first {
		t.Fatal("fresh range proof rewrites intent")
	}
	p.SetID = "changed"
	if planDigest(p) == first {
		t.Fatal("archive identity unbound")
	}
}

func TestRecoveryStageIsPrivateFencedAndDoesNotInstallLiveFiles(t *testing.T) {
	e, prefix, _ := fixture(t)
	ctx := context.Background()
	if _, err := e.Verify(ctx, prefix); err != nil {
		t.Fatal(err)
	}
	destination := privateTestDir(t)
	e.Signer = nil
	staged, err := e.StageRestore(ctx, prefix, destination)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged.Directory) != destination || !staged.State.Fenced || staged.State.HighWater != 3 || staged.State.StoreID != e.Config.StoreID {
		t.Fatal(staged)
	}
	if _, err := os.Stat(filepath.Join(destination, "inbox.db")); !os.IsNotExist(err) {
		t.Fatal("live destination overwritten")
	}
	db, err := logger.OpenBillingInbox(staged.Snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Health(ctx); err == nil {
		t.Fatal("staged recovery unfenced")
	}
	if _, err := db.Page(ctx, "", 1); err == nil {
		t.Fatal("recovered stream served before approval")
	}
	if _, err := logger.PrepareBillingSnapshotRecovery(ctx, staged.Snapshot, staged.Source.Manifest, nil); err == nil {
		t.Fatal("existing recovery stage overwritten")
	}
}

func TestAutomaticAgeSelectionUsesOnlyContiguousQualifiedPrefix(t *testing.T) {
	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
	records := []billingarchive.ExportRecord{{Sequence: 1, ReceivedAt: cutoff}, {Sequence: 2, ReceivedAt: cutoff.Add(-time.Hour)}, {Sequence: 3, ReceivedAt: cutoff.Add(time.Nanosecond)}, {Sequence: 4, ReceivedAt: cutoff.Add(-time.Hour)}}
	if through := eligiblePrefix(records, cutoff); through != 2 {
		t.Fatal("skipped young receipt or blocked eligible prefix", through)
	}
	if through := eligiblePrefix(records[2:], cutoff); through != 0 {
		t.Fatal("advanced past unqualified first receipt", through)
	}
}
