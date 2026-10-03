package billingbackup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	logger "github.com/hkt999rtk/rtk_cloud_logger"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
	bolt "go.etcd.io/bbolt"
)

// This fixture ages trusted receipts in a closed, test-owned DB BEFORE capture.
// The verifier still decrypts and cross-checks the actual snapshot and exports;
// no forged manifest clock or substituted financial digest qualifies old data.
type retirementRetryHarness struct {
	t         *testing.T
	engine    Engine
	prefix    string
	objects   *memoryStore
	verified  Verified
	inbox     *logger.BillingInbox
	events    []logger.LogEvent
	local     Client
	authority Client

	mu             sync.Mutex
	plan           *retirementPlan
	status         string
	origin         string
	failure        string
	resolved       bool
	creates        int
	aborts         int
	resolves       int
	localMutations []string
}

func newRetirementRetryHarness(t *testing.T, receiptAge time.Duration) *retirementRetryHarness {
	t.Helper()
	ctx := context.Background()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	public, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := &retirementRetryHarness{t: t, objects: &memoryStore{data: map[string][]byte{}}}
	path := filepath.Join(privateTestDir(t), "inbox.db")
	inbox, err := logger.OpenBillingInbox(path, true)
	if err != nil {
		t.Fatal(err)
	}
	config := logger.LifecycleConfig{Environment: "dev", Stack: "rtk", ScratchDir: privateTestDir(t), ScratchCapacityBytes: 8 << 30, EncryptionKeyID: "retirement-encryption", Recipients: []string{identity.Recipient().String()}, VerifierKeys: map[string]string{"retirement-verifier": base64.StdEncoding.EncodeToString(public)}, ObjectStore: captureWriter{h.objects}, PartBytes: 1 << 20, BackupEnabled: true, RetirementEnabled: true}
	if err := inbox.ConfigureLifecycle(config); err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Migrate(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		usage := map[string]any{"usage_id": fmt.Sprintf("retirement-usage-%d", i), "service_code": "mqtt", "brand_cloud_id": "brand", "event_time": at, "window_start": at.Add(-time.Minute), "window_end": at, "meter_epoch": "epoch", "sequence": json.Number("9007199254740993"), "source": "meter", "measurements": []any{map[string]any{"metric_code": "publish_bytes", "unit": "bytes", "quantity": json.Number("9007199254740993")}}}
		event := logger.LogEvent{EventID: fmt.Sprintf("retirement-event-%d", i), Time: at, Level: "info", Message: "usage", Service: "video-cloud", Env: "dev", Version: "test", Host: "host", Unit: "unit", Source: "billing_usage", Stream: "billing_usage", Fields: map[string]any{"usage_event": usage}}
		if err := inbox.InsertEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
		h.events = append(h.events, event)
	}
	if err := inbox.Close(); err != nil {
		t.Fatal(err)
	}
	fixtureDB, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	trustedAt := time.Now().UTC().Add(-receiptAge).Format(time.RFC3339Nano)
	err = fixtureDB.Update(func(tx *bolt.Tx) error {
		for sequence := uint64(1); sequence <= 2; sequence++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, sequence)
			if err := tx.Bucket([]byte("receipt_times")).Put(key, []byte(trustedAt)); err != nil {
				return err
			}
		}
		return nil
	})
	closeErr := fixtureDB.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	h.inbox, err = logger.OpenBillingInbox(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.inbox.Close() })
	if err := h.inbox.ConfigureLifecycle(config); err != nil {
		t.Fatal(err)
	}
	capture, err := h.inbox.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h.engine = Engine{Config: Config{Version: 1, Environment: "dev", Stack: "rtk", StoreID: capture.Manifest.StoreID, Directory: privateTestDir(t), LoggerURL: "http://localhost:8000", BillingURL: "http://localhost:8001", Remote: Remote{Endpoint: "https://example.invalid", Region: "us-sea", SigningRegion: "us-east-1", Bucket: "rtk-cloud-dev-billing-backup-us-sea"}, VerifierKeyID: "retirement-verifier", VerifierKeys: config.VerifierKeys, EncryptionRecipients: map[string][]string{config.EncryptionKeyID: config.Recipients}, AutoRetirement: true, PolicyID: "approved-policy", PolicyVersion: 3, ClearanceID: "approved-clearance"}, Objects: h.objects, Identities: []age.Identity{identity}, Signer: signer}
	h.prefix = Prefix(capture.Manifest, "snapshot")
	h.verified, err = h.engine.Verify(ctx, h.prefix)
	if err != nil {
		t.Fatal("real encrypted fixture failed independent qualification", err)
	}
	if _, err := h.inbox.VerifyCatalog(ctx, capture.SetID, h.verified.Completion); err != nil {
		t.Fatal(err)
	}
	authorityServer := httptest.NewServer(http.HandlerFunc(h.serveAuthority))
	t.Cleanup(authorityServer.Close)
	config.AuthorityURL = authorityServer.URL
	config.AuthorityToken = strings.Repeat("r", 32)
	if err := h.inbox.ConfigureLifecycle(config); err != nil {
		t.Fatal(err)
	}
	inner := logger.LifecycleHandler(logger.IngestConfig{BillingInbox: h.inbox, LifecycleToken: strings.Repeat("l", 32), LifecycleReadToken: strings.Repeat("s", 32)})
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		if r.Method == http.MethodPost {
			h.localMutations = append(h.localMutations, r.URL.Path)
		}
		failure := h.failure
		h.mu.Unlock()
		if failure == "local-abort" && strings.HasSuffix(r.URL.Path, "/abort") || failure == "local-apply" && strings.HasSuffix(r.URL.Path, "/apply") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(localServer.Close)
	h.local = Client{BaseURL: localServer.URL, Token: strings.Repeat("l", 32)}
	h.authority = Client{BaseURL: authorityServer.URL, Token: strings.Repeat("a", 32)}
	return h
}

func (h *retirementRetryHarness) serveAuthority(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 32) && (r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("r", 32)) {
		h.t.Error("missing role-separated authority credential")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	base := "/v1/internal/billing/raw-retention/operations"
	if r.Method == http.MethodGet {
		if h.failure == "authority-read" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if h.plan == nil || r.URL.Path != base+"/"+h.plan.OperationID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if h.resolved && h.failure == "terminal-shape" {
			_, _ = w.Write([]byte(`"not-an-operation"`))
			return
		}
		status := h.status
		if h.resolved && h.failure == "terminal-unresolved" {
			status = "ACTIVE"
		}
		wire, _ := json.Marshal(h.plan)
		var response map[string]any
		_ = json.Unmarshal(wire, &response)
		response["status"] = status
		response["decision_origin"] = h.origin
		if h.resolved {
			terminal, err := h.inbox.Retirement(r.Context(), h.plan.OperationID)
			if err != nil {
				h.t.Error(err)
			}
			response["terminal_receipt"] = terminal
			response["consumer_proofs"] = []any{map[string]string{"consumer_id": "consumer", "proof_sha256": strings.Repeat("d", 64)}}
		}
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == base {
		h.creates++
		if h.failure == "create" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var p retirementPlan
		if json.NewDecoder(r.Body).Decode(&p) != nil || p.PlanSHA256 != planDigest(p) {
			h.t.Error("controller submitted a malformed immutable plan")
			w.WriteHeader(http.StatusConflict)
			return
		}
		keys, _ := h.engine.Config.PublicKeys()
		_, raw, err := h.engine.readManifest(r.Context(), h.prefix)
		_, completionErr := billingarchive.VerifyCompletion(p.ArchiveCompletion, h.verified.Manifest, raw, keys)
		proof, proofErr := billingarchive.VerifyRangeProof(p.RangeProof, keys)
		if err != nil || completionErr != nil || proofErr != nil || proof.Environment != p.Environment || proof.StoreID != p.StoreID || proof.SetID != p.SetID || proof.ManifestSHA256 != p.ArchiveManifestSHA256 || proof.FromSequence != p.FromSequence || proof.ThroughSequence != p.ThroughSequence || proof.HighWater != h.verified.Manifest.HighWater || proof.RecordCount != uint64(len(p.Records)) || proof.RecordsBindingsSHA256 != billingarchive.BindingsDigest(p.Records) || proof.PolicyVersion != strconv.FormatInt(p.PolicyVersion, 10) || proof.MaxReceivedAt.After(time.Now().UTC().Add(-90*24*time.Hour)) {
			h.t.Error("controller did not submit independently qualified exact range evidence", err, completionErr, proofErr)
			w.WriteHeader(http.StatusConflict)
			return
		}
		h.plan, h.status = &p, "ACTIVE"
		_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/abort") {
		h.aborts++
		if h.failure == "abort" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if h.plan == nil {
			var p retirementPlan
			if json.NewDecoder(r.Body).Decode(&p) != nil || p.PlanSHA256 != planDigest(p) || r.URL.Path != base+"/"+p.OperationID+"/abort" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			h.plan, h.status, h.origin = &p, "ABORTED", "before_acceptance"
		} else if h.status == "ACTIVE" {
			h.status = "ABORT_REQUESTED"
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/resolve") {
		h.resolves++
		if h.failure == "resolve" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		terminal, err := h.inbox.Retirement(r.Context(), h.plan.OperationID)
		if err != nil || (terminal.Status != "completed" && terminal.Status != "aborted") || terminal.PlanSHA256 != h.plan.PlanSHA256 || terminal.SetID != h.plan.SetID {
			h.t.Error("authority released without independently reading Logger's exact terminal", terminal, err)
			w.WriteHeader(http.StatusConflict)
			return
		}
		h.status, h.resolved = strings.ToUpper(terminal.Status), true
		_ = json.NewEncoder(w).Encode(map[string]bool{"resolved": true})
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (h *retirementRetryHarness) savedPlan(t *testing.T, id string) retirementPlan {
	t.Helper()
	_, refs, m, err := h.engine.ReadRange(context.Background(), h.prefix, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := h.engine.readManifest(context.Background(), h.prefix)
	if err != nil {
		t.Fatal(err)
	}
	p := retirementPlan{OperationID: id, PolicyID: h.engine.Config.PolicyID, PolicyVersion: int64(h.engine.Config.PolicyVersion), ClearanceID: h.engine.Config.ClearanceID, Environment: m.Environment, StoreID: m.StoreID, FromSequence: 1, ThroughSequence: 2, SetID: m.SetID, ArchiveManifestSHA256: billingarchive.Digest(raw), ArchiveCompletion: h.verified.Completion, Records: refs}
	p.RangeProof, err = billingarchive.SignRangeProof(billingarchive.RangeProof{Version: 1, Purpose: "billing-raw-reconciliation", VerifierKeyID: h.engine.Config.VerifierKeyID, Environment: m.Environment, Stack: m.Stack, StoreID: m.StoreID, SetID: m.SetID, ManifestSHA256: p.ArchiveManifestSHA256, HighWater: m.HighWater, FromSequence: 1, ThroughSequence: 2, RecordCount: 2, MaxReceivedAt: m.MaxReceivedAt, RecordsBindingsSHA256: billingarchive.BindingsDigest(refs), VerifiedAt: time.Now().UTC().Add(-5 * time.Minute), PolicyVersion: strconv.FormatInt(p.PolicyVersion, 10), VerifierVersion: "test"}, h.engine.Signer)
	if err != nil {
		t.Fatal(err)
	}
	p.PlanSHA256 = planDigest(p)
	return p
}

func saveRetirementRetryPlan(t *testing.T, h *retirementRetryHarness, p retirementPlan) {
	t.Helper()
	if err := atomicJSON(filepath.Join(h.engine.Config.Directory, p.OperationID+".plan.json"), p); err != nil {
		t.Fatal(err)
	}
}

func TestRetireRangeRealVerifiedOldSnapshotCompletesSingleDurableIntent(t *testing.T) {
	h := newRetirementRetryHarness(t, 100*24*time.Hour)
	if err := RetireRange(context.Background(), h.engine, h.local, h.authority, h.prefix, 1, 2); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	p, status, creates := *h.plan, h.status, h.creates
	h.mu.Unlock()
	if status != "COMPLETED" || creates != 1 || p.PlanSHA256 != planDigest(p) {
		t.Fatal(status, creates, p)
	}
	terminal, err := h.inbox.Retirement(context.Background(), p.OperationID)
	if err != nil || terminal.Status != "completed" || terminal.RetiredThrough != 2 {
		t.Fatal(terminal, err)
	}
	if _, err := h.inbox.Page(context.Background(), "", 2); !errors.Is(err, logger.ErrBillingArchiveUnavailable) {
		t.Fatal("retired prefix was skipped or retained as hot body", err)
	}
	if err := h.inbox.InsertEvent(context.Background(), h.events[0]); !errors.Is(err, logger.ErrDuplicateEvent) {
		t.Fatal("retirement discarded immutable dedupe", err)
	}
	var saved retirementPlan
	if err := readPrivateJSON(filepath.Join(h.engine.Config.Directory, p.OperationID+".plan.json"), &saved); err != nil || saved.PlanSHA256 != p.PlanSHA256 {
		t.Fatal("stable intent was not persisted before submission", saved, err)
	}
	var evidence map[string]json.RawMessage
	if err := readPrivateJSON(filepath.Join(h.engine.Config.Directory, p.OperationID+".terminal.json"), &evidence); err != nil || len(evidence["terminal_receipt"]) == 0 || len(evidence["consumer_proofs"]) == 0 {
		t.Fatal("exact authoritative terminal evidence was lost", evidence, err)
	}
}

func TestRetireRangeRejectsUnqualifiedInputsBeforeAuthorityMutation(t *testing.T) {
	for _, scenario := range []string{"disabled", "young", "longer-hot-policy", "zero", "reversed", "over-bound", "outside-archive", "missing-signer", "missing-completion", "journal-unavailable", "invalid-controller"} {
		t.Run(scenario, func(t *testing.T) {
			age := 100 * 24 * time.Hour
			if scenario == "young" {
				age = time.Hour
			}
			h := newRetirementRetryHarness(t, age)
			e, from, through := h.engine, uint64(1), uint64(2)
			switch scenario {
			case "disabled":
				e.Config.AutoRetirement = false
			case "longer-hot-policy":
				e.Config.HotRetentionDays = 120
			case "zero":
				from = 0
			case "reversed":
				from, through = 2, 1
			case "over-bound":
				through = 1001
			case "outside-archive":
				through = 3
			case "missing-signer":
				e.Signer = nil
			case "missing-completion":
				h.objects.mu.Lock()
				delete(h.objects.data, h.prefix+"/complete.json")
				h.objects.mu.Unlock()
			case "journal-unavailable":
				e.Config.Directory = filepath.Join(privateTestDir(t), "missing")
			case "invalid-controller":
				e.Config.PolicyVersion = 0
			}
			if err := RetireRange(context.Background(), e, h.local, h.authority, h.prefix, from, through); err == nil {
				t.Fatal("unqualified retirement reached authority")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.creates != 0 || len(h.localMutations) != 0 {
				t.Fatal("rejected input mutated participants", h.creates, h.localMutations)
			}
		})
	}
}

func TestSavedPlanAuthoritative404RefreshesProofWithoutChangingIntent(t *testing.T) {
	h := newRetirementRetryHarness(t, 100*24*time.Hour)
	p := h.savedPlan(t, "retire-saved-404")
	saveRetirementRetryPlan(t, h, p)
	path := filepath.Join(h.engine.Config.Directory, p.OperationID+".plan.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e := h.engine
	e.Config.PolicyID, e.Config.PolicyVersion, e.Config.ClearanceID = "new-policy", 9, "new-clearance"
	if err := submitSavedPlan(context.Background(), e, h.local, h.authority, p); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	submitted, creates := *h.plan, h.creates
	h.mu.Unlock()
	if submitted.OperationID != p.OperationID || submitted.PlanSHA256 != p.PlanSHA256 || submitted.PolicyID != p.PolicyID || submitted.PolicyVersion != p.PolicyVersion || submitted.ClearanceID != p.ClearanceID || submitted.RangeProof.SignatureB64 == p.RangeProof.SignatureB64 || creates != 1 {
		t.Fatal("fresh proof rewrote or replaced the saved intent", submitted, creates)
	}
	keys, _ := e.Config.PublicKeys()
	proof, err := billingarchive.VerifyRangeProof(submitted.RangeProof, keys)
	if err != nil || proof.PolicyVersion != "3" || time.Since(proof.VerifiedAt) > time.Minute {
		t.Fatal("re-submission did not carry fresh approved proof", proof, err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, current) {
		t.Fatal("immutable local journal was rewritten", err)
	}
	if err := submitSavedPlan(context.Background(), e, h.local, h.authority, p); err != nil {
		t.Fatal("terminal same-intent retry failed", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.creates != 1 {
		t.Fatal("retry allocated another authoritative intent", h.creates)
	}
}

func TestRetireRangeResumesPreviousIntentBeforeAllocatingAnotherOperation(t *testing.T) {
	h := newRetirementRetryHarness(t, 100*24*time.Hour)
	p := h.savedPlan(t, "retire-existing-priority")
	saveRetirementRetryPlan(t, h, p)
	h.plan, h.status = &p, "ACTIVE"
	if err := RetireRange(context.Background(), h.engine, h.local, h.authority, h.prefix, 2, 2); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.creates != 0 || h.plan.OperationID != p.OperationID || h.status != "COMPLETED" {
		t.Fatal("pending durable intent was replaced by a newly requested range", h.creates, h.plan, h.status)
	}
}

func TestSavedPlan404RequalificationRejectsChangedOrUnavailableEvidence(t *testing.T) {
	for _, scenario := range []string{"scope", "digest", "authority-unavailable", "authority-mismatch", "authority-state", "verified-missing", "verified-scope", "bindings", "manifest", "completion", "range-signature", "range-scope", "range-horizon", "young", "missing-signer", "create-unavailable", "resolve-unavailable", "terminal-shape", "terminal-unresolved"} {
		t.Run(scenario, func(t *testing.T) {
			age := 100 * 24 * time.Hour
			if scenario == "young" {
				age = time.Hour
			}
			h := newRetirementRetryHarness(t, age)
			p := h.savedPlan(t, "retire-requalify")
			e := h.engine
			switch scenario {
			case "scope":
				p.Environment = "staging"
				p.PlanSHA256 = planDigest(p)
			case "digest":
				p.PlanSHA256 = strings.Repeat("f", 64)
			case "authority-unavailable":
				h.failure = "authority-read"
			case "authority-mismatch":
				wrong := p
				wrong.PlanSHA256 = strings.Repeat("f", 64)
				h.plan, h.status = &wrong, "ACTIVE"
			case "authority-state":
				h.plan, h.status = &p, "UNQUALIFIED"
			case "verified-missing":
				if err := os.Remove(filepath.Join(e.Config.Directory, p.SetID+".verified.json")); err != nil {
					t.Fatal(err)
				}
			case "verified-scope":
				wrong := h.verified
				wrong.Manifest.Environment = "staging"
				raw, _ := json.Marshal(wrong)
				if err := os.WriteFile(filepath.Join(e.Config.Directory, p.SetID+".verified.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "bindings":
				p.Records[0].UsageID = "different-usage"
				p.PlanSHA256 = planDigest(p)
			case "manifest":
				p.ArchiveManifestSHA256 = strings.Repeat("f", 64)
				p.PlanSHA256 = planDigest(p)
			case "completion":
				p.ArchiveCompletion.SignatureB64 = "invalid"
			case "range-signature":
				p.RangeProof.SignatureB64 = "invalid"
			case "range-scope", "range-horizon":
				keys, _ := e.Config.PublicKeys()
				proof, err := billingarchive.VerifyRangeProof(p.RangeProof, keys)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "range-scope" {
					proof.Environment = "staging"
				} else {
					proof.HighWater++
				}
				p.RangeProof, err = billingarchive.SignRangeProof(proof, e.Signer)
				if err != nil {
					t.Fatal(err)
				}
			case "missing-signer":
				e.Signer = nil
			case "create-unavailable":
				h.failure = "create"
			case "resolve-unavailable":
				h.failure = "resolve"
			case "terminal-shape":
				h.failure = "terminal-shape"
			case "terminal-unresolved":
				h.failure = "terminal-unresolved"
			}
			if err := submitSavedPlan(context.Background(), e, h.local, h.authority, p); err == nil {
				t.Fatal("unsafe saved plan was treated as resolved")
			}
			if _, err := os.Stat(filepath.Join(e.Config.Directory, p.OperationID+".terminal.json")); !os.IsNotExist(err) {
				t.Fatal("failure invented a terminal journal", err)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if scenario != "create-unavailable" && scenario != "resolve-unavailable" && scenario != "terminal-shape" && scenario != "terminal-unresolved" && (h.creates != 0 || len(h.localMutations) != 0) {
				t.Fatal("unqualified evidence mutated participants", h.creates, h.localMutations)
			}
		})
	}
}

func TestAbortSavedIntentHandlesUnknownActiveAndAbortRequestedWithoutDeletion(t *testing.T) {
	for _, initial := range []string{"unknown", "ACTIVE", "ABORT_REQUESTED"} {
		t.Run(initial, func(t *testing.T) {
			h := newRetirementRetryHarness(t, 100*24*time.Hour)
			p := h.savedPlan(t, "retire-cancel")
			saveRetirementRetryPlan(t, h, p)
			if initial != "unknown" {
				h.plan, h.status = &p, initial
			}
			if err := AbortSavedIntent(context.Background(), h.engine.Config, h.local, h.authority, p.OperationID); err != nil {
				t.Fatal(err)
			}
			h.mu.Lock()
			status, origin, mutations, creates := h.status, h.origin, append([]string(nil), h.localMutations...), h.creates
			h.mu.Unlock()
			if status != "ABORTED" || creates != 0 {
				t.Fatal("cancellation allocated another intent", status, creates)
			}
			terminal, err := h.inbox.Retirement(context.Background(), p.OperationID)
			if initial == "unknown" {
				if origin != "before_acceptance" || len(mutations) != 0 || !errors.Is(err, logger.ErrOperationNotFound) {
					t.Fatal("unknown cancellation invented a Logger fence", origin, mutations, terminal, err)
				}
			} else if err != nil || terminal.Status != "aborted" || terminal.RetiredThrough != 0 {
				t.Fatal("known authority fence did not receive durable Logger tombstone", terminal, err)
			}
			if page, err := h.inbox.Page(context.Background(), "", 10); err != nil || len(page.Records) != 2 {
				t.Fatal("cancellation deleted hot payload", page, err)
			}
			if err := AbortSavedIntent(context.Background(), h.engine.Config, h.local, h.authority, p.OperationID); err != nil {
				t.Fatal("same cancellation retry failed", err)
			}
		})
	}
}

func TestAbortSavedIntentRetainsFenceAcrossParticipantFailures(t *testing.T) {
	for _, failure := range []string{"abort", "authority-read", "local-abort", "resolve"} {
		t.Run(failure, func(t *testing.T) {
			h := newRetirementRetryHarness(t, 100*24*time.Hour)
			p := h.savedPlan(t, "retire-abort-retry")
			saveRetirementRetryPlan(t, h, p)
			h.plan, h.status, h.failure = &p, "ACTIVE", failure
			if err := AbortSavedIntent(context.Background(), h.engine.Config, h.local, h.authority, p.OperationID); err == nil {
				t.Fatal("ambiguous participant failure released cancellation")
			}
			if _, err := os.Stat(filepath.Join(h.engine.Config.Directory, p.OperationID+".terminal.json")); !os.IsNotExist(err) {
				t.Fatal("failed participant invented terminal completion", err)
			}
			h.mu.Lock()
			if h.status == "ABORTED" || h.status == "COMPLETED" {
				t.Error("ambiguous result released authoritative fence", h.status)
			}
			h.failure = ""
			h.mu.Unlock()
			if err := AbortSavedIntent(context.Background(), h.engine.Config, h.local, h.authority, p.OperationID); err != nil {
				t.Fatal("exact durable intent could not resume", err)
			}
			terminal, err := h.inbox.Retirement(context.Background(), p.OperationID)
			if err != nil || terminal.Status != "aborted" || terminal.RetiredThrough != 0 {
				t.Fatal(terminal, err)
			}
		})
	}
}

func TestAbortSavedIntentPreservesCompletedRetirementAndRejectsUnresolvedJournal(t *testing.T) {
	for _, scenario := range []string{"completed", "terminal-shape", "terminal-unresolved"} {
		t.Run(scenario, func(t *testing.T) {
			h := newRetirementRetryHarness(t, 100*24*time.Hour)
			if scenario == "completed" {
				if err := RetireRange(context.Background(), h.engine, h.local, h.authority, h.prefix, 1, 2); err != nil {
					t.Fatal(err)
				}
				h.mu.Lock()
				p, mutations := *h.plan, len(h.localMutations)
				h.mu.Unlock()
				before, err := h.inbox.Retirement(context.Background(), p.OperationID)
				if err != nil {
					t.Fatal(err)
				}
				if err := AbortSavedIntent(context.Background(), h.engine.Config, h.local, h.authority, p.OperationID); err != nil {
					t.Fatal("completed result was changed into an abort", err)
				}
				after, err := h.inbox.Retirement(context.Background(), p.OperationID)
				if err != nil || after.Status != "completed" || before.ReceiptSHA256 != after.ReceiptSHA256 || after.RetiredThrough != 2 {
					t.Fatal("terminal retirement was rewritten", before, after, err)
				}
				h.mu.Lock()
				defer h.mu.Unlock()
				if len(h.localMutations) != mutations {
					t.Fatal("completed authority result caused a new Logger mutation")
				}
				return
			}
			p := h.savedPlan(t, "retire-terminal-cancel")
			saveRetirementRetryPlan(t, h, p)
			h.plan, h.status, h.failure = &p, "ACTIVE", scenario
			if err := AbortSavedIntent(context.Background(), h.engine.Config, h.local, h.authority, p.OperationID); err == nil {
				t.Fatal("malformed or nonterminal response was journaled as cancellation")
			}
			if _, err := os.Stat(filepath.Join(h.engine.Config.Directory, p.OperationID+".terminal.json")); !os.IsNotExist(err) {
				t.Fatal("unresolved response created a terminal journal", err)
			}
			terminal, err := h.inbox.Retirement(context.Background(), p.OperationID)
			if err != nil || terminal.Status != "aborted" || terminal.RetiredThrough != 0 {
				t.Fatal("ambiguous controller response rewrote committed Logger tombstone", terminal, err)
			}
		})
	}
}

func TestAbortSavedIntentRejectsUnsafeJournalAndScopeBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"invalid-id", "missing", "world-readable", "invalid-json", "operation-id", "environment", "store-id", "digest"} {
		t.Run(scenario, func(t *testing.T) {
			h := newRetirementRetryHarness(t, 100*24*time.Hour)
			p := h.savedPlan(t, "retire-unsafe-cancel")
			id := p.OperationID
			switch scenario {
			case "invalid-id":
				id = "../escape"
			case "operation-id":
				p.OperationID = "different-operation"
			case "environment":
				p.Environment = "staging"
			case "store-id":
				p.StoreID = strings.Repeat("a", 32)
			case "digest":
				p.PlanSHA256 = strings.Repeat("f", 64)
			}
			if scenario != "digest" {
				p.PlanSHA256 = planDigest(p)
			}
			path := filepath.Join(h.engine.Config.Directory, "retire-unsafe-cancel.plan.json")
			if scenario != "missing" {
				raw, _ := json.Marshal(p)
				if scenario == "invalid-json" {
					raw = []byte(`{"operation_id":`)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "world-readable" {
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := AbortSavedIntent(context.Background(), h.engine.Config, h.local, h.authority, id); err == nil {
				t.Fatal("unsafe cancellation journal was trusted")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.aborts != 0 || h.resolves != 0 || h.creates != 0 || len(h.localMutations) != 0 {
				t.Fatal("unsafe journal mutated a participant", h.aborts, h.resolves, h.creates, h.localMutations)
			}
		})
	}
}
