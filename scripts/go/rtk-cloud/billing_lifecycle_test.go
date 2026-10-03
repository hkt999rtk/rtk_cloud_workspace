package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filippo.io/age"
	logger "github.com/hkt999rtk/rtk_cloud_logger"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/billingbackup"
)

func billingLifecycleCLIConfigFixture(t *testing.T, billingURL string) (secretStore, billingbackup.Config, string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg := billingbackup.Config{Version: 1, Environment: "dev", Stack: "video-cloud-dev", StoreID: strings.Repeat("a", 32), Directory: filepath.Join(t.TempDir(), "controller"), LoggerURL: "http://127.0.0.1:1", BillingURL: billingURL, Remote: billingbackup.Remote{Endpoint: "https://example.invalid", Region: "us-sea", SigningRegion: "us-east-1", Bucket: "rtk-cloud-dev-billing-backup-us-sea"}, VerifierKeyID: "verifier-v1", VerifierKeys: map[string]string{"verifier-v1": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("v", 32)))}, EncryptionRecipients: map[string][]string{"encryption-v1": {identity.Recipient().String()}}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "public-config.json")
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return store, cfg, file
}

func rewriteBillingLifecycleTestConfig(t *testing.T, file string, cfg billingbackup.Config) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBillingLifecycleDirectCommandsUsePrivateRoleAndExactRequest(t *testing.T) {
	for _, test := range []struct {
		action, path, method   string
		readOnly, terminalRead bool
		body                   map[string]any
	}{
		{"status", "/status", "GET", true, false, nil},
		{"operation-status", "/retire/retire-test", "GET", true, true, nil},
		{"recovery-status", "/recovery", "GET", true, false, nil},
		{"migrate", "/migrate", "POST", false, false, map[string]any{"batch": json.Number("1000")}},
		{"capture", "/capture", "POST", false, false, map[string]any{}},
		{"compact", "/compact", "POST", false, false, map[string]any{}},
		{"cache-evict", "/cache/evict", "POST", false, false, map[string]any{"through_sequence": "9"}},
		{"recovery-admit", "/recovery/admit", "POST", false, false, map[string]any{"approval": "independent-recovery-signature"}},
	} {
		t.Run(test.action, func(t *testing.T) {
			token := strings.Repeat("l", 32)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != "/v1/internal/billing-lifecycle"+test.path || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("wrong private command or role", r.Method, r.URL.Path)
					w.WriteHeader(403)
					return
				}
				if test.body != nil {
					var body map[string]any
					decoder := json.NewDecoder(r.Body)
					decoder.UseNumber()
					if err := decoder.Decode(&body); err != nil || !reflect.DeepEqual(body, test.body) {
						t.Error("request did not retain exact command binding", body, err)
					}
				} else if body, err := io.ReadAll(r.Body); err != nil || len(body) != 0 {
					t.Error("GET unexpectedly carried a request body", err)
				}
				calls.Add(1)
				w.Header().Set("Cache-Control", "no-store")
				json.NewEncoder(w).Encode(map[string]string{"result": "mock-terminal"})
			}))
			defer server.Close()
			store, cfg, file := billingLifecycleCLIConfigFixture(t, "http://127.0.0.1:1")
			cfg.LoggerURL = server.URL
			rewriteBillingLifecycleTestConfig(t, file, cfg)
			id := "cloud-logger-lifecycle-token"
			if test.terminalRead {
				id = "cloud-logger-lifecycle-read-token"
			}
			if err := store.write(filepath.Join("runtime", id), []byte(token), true); err != nil {
				t.Fatal(err)
			}
			args := []string{test.action, "--environment", "dev", "--config", file, "--config-root", store.ConfigRoot, "--through", "9", "--operation-id", "retire-test"}
			if !test.readOnly {
				args = append(args, "--confirm-environment", "dev", "--confirm-store", cfg.StoreID)
			}
			if test.action == "recovery-admit" {
				request := filepath.Join(t.TempDir(), "approval.json")
				if err := os.WriteFile(request, []byte(`{"approval":"independent-recovery-signature"}`), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--request", request)
			}
			if err := runBillingLifecycle(args); err != nil || calls.Load() != 1 {
				t.Fatal("private command failed", err, calls.Load())
			}
		})
	}
}

func TestBillingLifecycleRejectsInvalidCLIAndUnsafeRequests(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}} {
		if err := runBillingLifecycle(args); err != nil {
			t.Fatal(err)
		}
	}
	store, cfg, file := billingLifecycleCLIConfigFixture(t, "http://127.0.0.1:1")
	for _, id := range []string{"cloud-logger-lifecycle-token", "cloud-logger-lifecycle-read-token", "billing-raw-retention-controller", "billing-raw-retention-financial"} {
		if err := store.write(filepath.Join("runtime", id), []byte(strings.Repeat("t", 32)), true); err != nil {
			t.Fatal(err)
		}
	}
	base := []string{"--environment", "dev", "--config", file, "--config-root", store.ConfigRoot, "--confirm-environment", "dev", "--confirm-store", cfg.StoreID}
	for _, test := range []struct {
		name, action string
		extra        []string
		want         string
	}{
		{"unknown action", "unsafe", nil, "unknown lifecycle action"},
		{"unknown flag", "status", []string{"--bad-flag"}, "invalid Billing lifecycle arguments"},
		{"extra argument", "status", []string{"extra"}, "invalid Billing lifecycle arguments"},
		{"missing config", "status", []string{"--config", filepath.Join(t.TempDir(), "missing")}, "configuration unavailable"},
		{"wrong environment", "status", []string{"--environment", "prod"}, "environment mismatch"},
		{"unconfirmed environment", "capture", []string{"--confirm-environment", "prod"}, "confirmation required"},
		{"unconfirmed store", "capture", []string{"--confirm-store", strings.Repeat("b", 32)}, "confirmation required"},
		{"unsafe operation", "operation-status", []string{"--operation-id", "../escape"}, "operation ID required"},
		{"missing resume ID", "resume", nil, "operation ID required"},
		{"unrelated authority", "authority", []string{"--path", "/v1/public/invoices"}, "invalid raw retention authority path"},
		{"authority traversal", "authority", []string{"--path", "/v1/internal/billing/raw-retention/../operations"}, "invalid raw retention authority path"},
		{"authority fragment", "authority", []string{"--path", "/v1/internal/billing/raw-retention/operations#fragment"}, "invalid raw retention authority path"},
		{"authority mutation query", "authority", []string{"--path", "/v1/internal/billing/raw-retention/operations?scope=dev"}, "invalid raw retention authority path"},
		{"authority method", "authority", []string{"--path", "/v1/internal/billing/raw-retention/operations", "--method", "DELETE"}, "invalid raw retention authority path"},
		{"authority role", "authority", []string{"--path", "/v1/internal/billing/raw-retention/operations", "--role", "uploader"}, "unknown authority role"},
		{"authority missing request", "authority", []string{"--path", "/v1/internal/billing/raw-retention/policies", "--role", "financial"}, "private absolute request file required"},
		{"missing recovery approval", "recovery-admit", nil, "private absolute request file required"},
		{"missing custody", "verify", nil, "explicit custody identity required"},
		{"relative identity", "verify", []string{"--identity", "relative.agekey"}, "invalid Billing lifecycle arguments"},
		{"custody outside environment", "verify", []string{"--identity", filepath.Join(t.TempDir(), "key.agekey")}, "identity outside environment Billing custody"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{test.action}, base...)
			args = append(args, test.extra...)
			if err := runBillingLifecycle(args); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v; want %s", err, test.want)
			}
		})
	}
	for _, test := range []struct {
		name, content string
		mode          os.FileMode
		valid         bool
	}{
		{"private exact", `{"quantity":"9007199254740993"}`, 0600, true},
		{"public permissions", `{}`, 0644, false},
		{"trailing JSON", `{} {}`, 0600, false},
		{"broken JSON", `{`, 0600, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "request.json")
			if err := os.WriteFile(path, []byte(test.content), test.mode); err != nil {
				t.Fatal(err)
			}
			raw, err := readBillingLifecycleRequest(path)
			if test.valid && (err != nil || !bytes.Equal(raw, []byte(test.content))) {
				t.Fatal("exact private request rejected", string(raw), err)
			}
			if !test.valid && err == nil {
				t.Fatal("unsafe or malformed request accepted")
			}
			link := filepath.Join(t.TempDir(), "request-link.json")
			if err := os.Symlink(path, link); err != nil {
				t.Fatal(err)
			}
			if _, err := readBillingLifecycleRequest(link); err == nil {
				t.Fatal("symlinked authority request accepted")
			}
		})
	}
	var flags billingIdentityFlags
	for i := 0; i < 16; i++ {
		if err := flags.Set(filepath.Join(t.TempDir(), "identity.agekey")); err != nil {
			t.Fatal(err)
		}
	}
	if flags.String() != "[explicit custody paths]" || flags.Set("/private/tmp/extra.agekey") == nil {
		t.Fatal("custody path bound or redaction missing")
	}
}

type billingLifecycleCLIObjects struct {
	mu   sync.Mutex
	data map[string][]byte
	puts int
}

func (s *billingLifecycleCLIObjects) PutImmutable(_ context.Context, key, path string, size int64, digest string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(raw)) != size || billingarchive.Digest(raw) != digest {
		return io.ErrUnexpectedEOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = bytes.Clone(raw)
	return nil
}

func TestBillingLifecycleEncryptedArchiveVerifyRehydrateAndOfflineRecovery(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	objects := &billingLifecycleCLIObjects{data: map[string][]byte{}}
	inboxDir := t.TempDir()
	if err := os.Chmod(inboxDir, 0700); err != nil {
		t.Fatal(err)
	}
	inbox, err := logger.OpenBillingInbox(filepath.Join(inboxDir, "inbox.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer inbox.Close()
	scratch := t.TempDir()
	if err := os.Chmod(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	lifecycle := logger.LifecycleConfig{Environment: "dev", Stack: "video-cloud-dev", ScratchDir: scratch, ScratchCapacityBytes: 8 << 30, EncryptionKeyID: "encryption-v1", Recipients: []string{identity.Recipient().String()}, VerifierKeys: map[string]string{"verifier-v1": base64.StdEncoding.EncodeToString(public)}, ObjectStore: objects, PartBytes: 1 << 20, BackupEnabled: true}
	if err := inbox.ConfigureLifecycle(lifecycle); err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Migrate(context.Background(), 1000); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	usage := map[string]any{"usage_id": "usage-cli", "service_code": "mqtt", "brand_cloud_id": "brand", "event_time": at, "window_start": at.Add(-time.Minute), "window_end": at, "meter_epoch": "epoch", "sequence": json.Number("9007199254740993"), "source": "meter", "measurements": []any{map[string]any{"metric_code": "publish_bytes", "unit": "bytes", "quantity": json.Number("9007199254740993")}}}
	if err := inbox.InsertEvent(context.Background(), logger.LogEvent{EventID: "event-cli", Time: at, Level: "info", Message: "usage", Service: "video-cloud", Env: "dev", Version: "test", Host: "host", Unit: "unit", Source: "billing_usage", Stream: "billing_usage", Fields: map[string]any{"usage_event": usage}}); err != nil {
		t.Fatal(err)
	}
	capture, err := inbox.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var verifiedCalls, rehydrateCalls atomic.Int32
	var rejectRehydrate atomic.Bool
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("l", 32) || r.Method != "POST" {
			t.Error("wrong local lifecycle boundary")
			w.WriteHeader(403)
			return
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		var setID string
		json.Unmarshal(request["set_id"], &setID)
		if setID != capture.Manifest.SetID {
			t.Error("set identity drift")
		}
		switch r.URL.Path {
		case "/v1/internal/billing-lifecycle/verify":
			verifiedCalls.Add(1)
		case "/v1/internal/billing-lifecycle/rehydrate":
			if rejectRehydrate.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			rehydrateCalls.Add(1)
			var records []billingarchive.ExportRecord
			if err := billingarchive.StrictDecode(bytes.NewReader(request["records"]), 2<<20, &records); err != nil || len(records) != 1 || records[0].Sequence != 1 {
				t.Error("rehydration lost exact range", err)
			}
		default:
			t.Error("unexpected Logger write", r.URL.Path)
		}
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
	}))
	defer local.Close()
	s3 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const bucket = "/rtk-cloud-dev-billing-backup-us-sea/"
		if !strings.HasPrefix(r.URL.Path, bucket) {
			t.Error("S3 bucket scope drift")
			w.WriteHeader(400)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, bucket)
		objects.mu.Lock()
		defer objects.mu.Unlock()
		switch r.Method {
		case "GET":
			raw, ok := objects.data[key]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			w.Write(raw)
		case "PUT":
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("completion publication did not use immutable write")
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			if old, exists := objects.data[key]; exists && !bytes.Equal(old, raw) {
				w.WriteHeader(412)
				return
			}
			objects.data[key] = raw
			objects.puts++
			w.WriteHeader(200)
		default:
			t.Error("unexpected S3 method", r.Method)
			w.WriteHeader(405)
		}
	}))
	defer s3.Close()
	// Production stays TLS-verified. Only this sequential test pins the local
	// server's certificate through the transport supplied by httptest.
	originalTransport := http.DefaultTransport
	http.DefaultTransport = s3.Client().Transport
	defer func() { http.DefaultTransport = originalTransport }()
	store, cfg, file := billingLifecycleCLIConfigFixture(t, "http://127.0.0.1:1")
	cfg.StoreID, cfg.LoggerURL, cfg.Remote.Endpoint = capture.Manifest.StoreID, local.URL, s3.URL
	cfg.VerifierKeys = lifecycle.VerifierKeys
	cfg.EncryptionRecipients = map[string][]string{"encryption-v1": lifecycle.Recipients}
	rewriteBillingLifecycleTestConfig(t, file, cfg)
	for relative, value := range map[string]string{
		"runtime/cloud-logger-lifecycle-token":                    strings.Repeat("l", 32),
		"runtime/billing-raw-retention-controller":                strings.Repeat("c", 32),
		"operator/env/RTK_BILLING_VERIFIER_ACCESS_KEY_ID":         "isolated-fixture-access",
		"operator/env/RTK_BILLING_VERIFIER_SECRET_ACCESS_KEY":     "isolated-fixture-secret",
		"operator/recovery/billing-backup/encryption-v1.agekey":   identity.String(),
		"operator/recovery/billing-backup/verifier-v1.ed25519key": base64.StdEncoding.EncodeToString(private),
	} {
		if err := store.write(relative, []byte(value+"\n"), true); err != nil {
			t.Fatal(err)
		}
	}
	identityFile := filepath.Join(store.Root, "operator/recovery/billing-backup/encryption-v1.agekey")
	signerFile := filepath.Join(store.Root, "operator/recovery/billing-backup/verifier-v1.ed25519key")
	base := []string{"--environment", "dev", "--config", file, "--config-root", store.ConfigRoot, "--confirm-environment", "dev", "--confirm-store", cfg.StoreID, "--identity", identityFile, "--prefix", billingbackup.Prefix(capture.Manifest, "snapshot")}
	verifyArgs := append([]string{"verify"}, base...)
	verifyArgs = append(verifyArgs, "--signing-key", signerFile)
	if err := runBillingLifecycle(verifyArgs); err != nil || verifiedCalls.Load() != 1 {
		t.Fatal("encrypted verification failed", err, verifiedCalls.Load())
	}
	retireArgs := append([]string{"retire"}, base...)
	retireArgs = append(retireArgs, "--signing-key", signerFile, "--from", "1", "--through", "1")
	if err := runBillingLifecycle(retireArgs); err == nil || !strings.Contains(err.Error(), "retirement disabled") {
		t.Fatal("disabled retirement error was swallowed by CLI", err)
	}
	badDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badDirectory, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	originalDirectory := cfg.Directory
	cfg.Directory = badDirectory
	rewriteBillingLifecycleTestConfig(t, file, cfg)
	runArgs := append([]string{"run"}, base...)
	runArgs = append(runArgs, "--signing-key", signerFile)
	if err := runBillingLifecycle(runArgs); err == nil {
		t.Fatal("controller started with invalid private directory")
	}
	cfg.Directory = originalDirectory
	rewriteBillingLifecycleTestConfig(t, file, cfg)
	// A reader cannot publish a new receipt or access the verifier signing key.
	if err := os.Remove(signerFile); err != nil {
		t.Fatal(err)
	}
	objects.mu.Lock()
	puts := objects.puts
	objects.mu.Unlock()
	readArgs := append([]string{"rehydrate"}, base...)
	readArgs = append(readArgs, "--from", "1", "--through", "1")
	rejectRehydrate.Store(true)
	if err := runBillingLifecycle(readArgs); err == nil {
		t.Fatal("rehydration endpoint failure was swallowed by CLI")
	}
	rejectRehydrate.Store(false)
	if err := runBillingLifecycle(readArgs); err != nil || rehydrateCalls.Load() != 1 {
		t.Fatal("reader-only rehydration failed", err, rehydrateCalls.Load())
	}
	if err := os.Remove(filepath.Join(store.Root, "runtime/cloud-logger-lifecycle-token")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "recovery-stage")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	restoreArgs := append([]string{"restore-stage"}, base...)
	restoreArgs = append(restoreArgs, "--destination", destination)
	if err := runBillingLifecycle(restoreArgs); err != nil {
		t.Fatal("offline recovery improperly required live Logger or signer", err)
	}
	objects.mu.Lock()
	finalPuts := objects.puts
	objects.mu.Unlock()
	if finalPuts != puts {
		t.Fatal("read-only archive commands published remote metadata")
	}
	stages, err := os.ReadDir(destination)
	if err != nil || len(stages) != 1 {
		t.Fatal("missing private offline stage", err, len(stages))
	}
	stageFile := filepath.Join(destination, stages[0].Name(), "recovery-stage.json")
	raw, err := os.ReadFile(stageFile)
	if err != nil {
		t.Fatal(err)
	}
	var stage billingbackup.StagedRecovery
	if err := json.Unmarshal(raw, &stage); err != nil || !stage.State.Fenced || stage.Source.Manifest.StoreID != cfg.StoreID || stage.PreparedSHA256 == "" {
		t.Fatal("offline stage was not fenced or identity-bound", err)
	}
}

func billingLifecycleCLIRetirementIntent(cfg billingbackup.Config) map[string]any {
	refs := []billingarchive.RecordBinding{{Sequence: 1, LoggerContentSHA256: strings.Repeat("b", 64), UsageID: "usage-1", EventSHA256: strings.Repeat("c", 64)}}
	// This ordered core is the versioned financial plan wire contract. It must
	// bind the same immutable ID, scope and original receipt references as Billing.
	core := struct {
		OperationID           string `json:"operation_id"`
		PolicyID              string `json:"policy_id"`
		PolicyVersion         int64  `json:"policy_version"`
		ClearanceID           string `json:"clearance_id"`
		Environment           string `json:"environment"`
		StoreID               string `json:"store_id"`
		FromSequence          uint64 `json:"from_sequence,string"`
		ThroughSequence       uint64 `json:"through_sequence,string"`
		SetID                 string `json:"set_id"`
		ArchiveManifestSHA256 string `json:"archive_manifest_sha256"`
		RecordsBindingsSHA256 string `json:"records_bindings_sha256"`
	}{"retire-cli", "policy-v1", 1, "clearance-v1", cfg.Environment, cfg.StoreID, 1, 1, "archive-v1", strings.Repeat("d", 64), billingarchive.BindingsDigest(refs)}
	raw, _ := json.Marshal(core)
	var plan map[string]any
	json.Unmarshal(raw, &plan)
	delete(plan, "records_bindings_sha256") // digest core, not an operation DTO field
	plan["records"] = refs
	plan["plan_sha256"] = billingarchive.Digest(append([]byte("rtk-billing-raw-retirement-plan-v1\x00"), raw...))
	return plan
}

func TestBillingLifecyclePropagatesAuthorityAndRecoveryErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if strings.Contains(r.URL.Path, "/operations/") {
			w.WriteHeader(404)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	store, cfg, file := billingLifecycleCLIConfigFixture(t, server.URL)
	cfg.LoggerURL = server.URL
	rewriteBillingLifecycleTestConfig(t, file, cfg)
	for _, id := range []string{"cloud-logger-lifecycle-token", "billing-raw-retention-controller", "billing-raw-retention-financial"} {
		if err := store.write("runtime/"+id, []byte(strings.Repeat("t", 32)), true); err != nil {
			t.Fatal(err)
		}
	}
	request := filepath.Join(t.TempDir(), "private-request.json")
	if err := os.WriteFile(request, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--environment", "dev", "--config", file, "--config-root", store.ConfigRoot, "--confirm-environment", "dev", "--confirm-store", cfg.StoreID, "--request", request}
	for _, test := range []struct {
		action string
		extra  []string
	}{
		{"recovery-admit", nil},
		{"authority", []string{"--role", "financial", "--path", "/v1/internal/billing/raw-retention/policies"}},
		{"resume", []string{"--operation-id", "retire-unknown"}},
		{"abort", []string{"--operation-id", "retire-unknown"}},
	} {
		t.Run(test.action, func(t *testing.T) {
			args := append([]string{test.action}, base...)
			args = append(args, test.extra...)
			if err := runBillingLifecycle(args); err == nil {
				t.Fatal("failed authority/recovery action returned CLI success")
			}
		})
	}
}

func TestBillingLifecycleResumeAndAbortUseControllerRoleAndPermanentIntent(t *testing.T) {
	for _, test := range []struct {
		action, initial  string
		beforeAcceptance bool
	}{
		{"resume", "ACTIVE", false},
		{"abort", "ACTIVE", false},
		{"resume", "ABORT_REQUESTED", false},
		{"resume", "COMPLETED", false},
		{"abort", "ABORTED", true},
	} {
		t.Run(test.action+"-"+test.initial+"-"+map[bool]string{true: "unaccepted", false: "accepted"}[test.beforeAcceptance], func(t *testing.T) {
			store, cfg, file := billingLifecycleCLIConfigFixture(t, "http://127.0.0.1:1")
			plan := billingLifecycleCLIRetirementIntent(cfg)
			var mu sync.Mutex
			status := test.initial
			accepted := !test.beforeAcceptance
			var localWrites, authorityWrites atomic.Int32
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/v1/internal/billing-lifecycle/retire/") || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("l", 32) {
					t.Error("invalid Logger mutation boundary")
					w.WriteHeader(403)
					return
				}
				localWrites.Add(1)
				w.Header().Set("Cache-Control", "no-store")
				io.WriteString(w, `{}`)
			}))
			defer local.Close()
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("c", 32) {
					t.Error("financial/recovery credential reused for controller")
					w.WriteHeader(403)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Cache-Control", "no-store")
				switch {
				case r.Method == "GET" && r.URL.Path == "/v1/internal/billing/raw-retention/operations/retire-cli":
					if !accepted {
						w.WriteHeader(404)
						return
					}
					plan["status"] = status
					json.NewEncoder(w).Encode(plan)
				case r.Method == "POST" && r.URL.Path == "/v1/internal/billing/raw-retention/operations/retire-cli/abort":
					authorityWrites.Add(1)
					if !accepted {
						var submitted map[string]any
						if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil || submitted["operation_id"] != plan["operation_id"] || submitted["plan_sha256"] != plan["plan_sha256"] {
							t.Error("cancellation did not bind permanent saved intent", err)
						}
						accepted, status = true, "ABORTED"
						plan["decision_origin"] = "CANCELLED_BEFORE_ACCEPTANCE"
					} else {
						status = "ABORT_REQUESTED"
					}
					io.WriteString(w, `{}`)
				case r.Method == "POST" && r.URL.Path == "/v1/internal/billing/raw-retention/operations/retire-cli/resolve":
					authorityWrites.Add(1)
					if status == "ABORT_REQUESTED" {
						status = "ABORTED"
					} else {
						status = "COMPLETED"
					}
					io.WriteString(w, `{}`)
				default:
					t.Error("unexpected authority request", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer authority.Close()
			cfg.LoggerURL, cfg.BillingURL = local.URL, authority.URL
			rewriteBillingLifecycleTestConfig(t, file, cfg)
			for id, token := range map[string]string{"cloud-logger-lifecycle-token": strings.Repeat("l", 32), "billing-raw-retention-controller": strings.Repeat("c", 32)} {
				if err := store.write("runtime/"+id, []byte(token), true); err != nil {
					t.Fatal(err)
				}
			}
			if test.beforeAcceptance {
				raw, _ := json.Marshal(plan)
				if err := os.WriteFile(filepath.Join(cfg.Directory, "retire-cli.plan.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := runBillingLifecycle([]string{test.action, "--environment", "dev", "--config", file, "--config-root", store.ConfigRoot, "--confirm-environment", "dev", "--confirm-store", cfg.StoreID, "--operation-id", "retire-cli"})
			if err != nil {
				t.Fatal("failed authoritative resume/cancel", err)
			}
			if test.beforeAcceptance {
				if localWrites.Load() != 0 || authorityWrites.Load() != 1 {
					t.Fatal("unaccepted cancellation invented a Logger fence", localWrites.Load(), authorityWrites.Load())
				}
				raw, err := os.ReadFile(filepath.Join(cfg.Directory, "retire-cli.terminal.json"))
				if err != nil || !bytes.Contains(raw, []byte("CANCELLED_BEFORE_ACCEPTANCE")) {
					t.Fatal("cancellation terminal receipt was not durable", err)
				}
			} else if test.initial == "COMPLETED" {
				if localWrites.Load() != 0 || authorityWrites.Load() != 0 {
					t.Fatal("terminal operation was reapplied")
				}
			} else {
				wantLocal := int32(2)
				if test.action == "abort" || test.initial == "ABORT_REQUESTED" {
					wantLocal = 1
				}
				if localWrites.Load() != wantLocal || authorityWrites.Load() == 0 {
					t.Fatal("wrong apply/abort sequence", localWrites.Load(), authorityWrites.Load())
				}
			}
		})
	}
}

func TestBillingLifecycleFinancialAuthorityDoesNotRequireLoggerOrCustodyCredentials(t *testing.T) {
	token := strings.Repeat("f", 32)
	var calls atomic.Int32
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/internal/billing/raw-retention/policies" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong financial authority boundary", r.Method, r.URL.Path)
			w.WriteHeader(403)
			return
		}
		calls.Add(1)
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]bool{"approved": true})
	}))
	defer authority.Close()
	store, cfg, file := billingLifecycleCLIConfigFixture(t, authority.URL)
	if err := store.write(filepath.Join("runtime", "billing-raw-retention-financial"), []byte(token+"\n"), true); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(request, []byte(`{"policy_id":"policy-v1","environment":"dev"}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"authority", "--environment", "dev", "--config", file, "--config-root", store.ConfigRoot, "--confirm-environment", "dev", "--confirm-store", cfg.StoreID, "--role", "financial", "--path", "/v1/internal/billing/raw-retention/policies", "--request", request}
	if err := runBillingLifecycle(args); err != nil || calls.Load() != 1 {
		t.Fatal("financial authority unnecessarily depended on Logger/storage/private custody", err, calls.Load())
	}
	for _, id := range []string{"cloud-logger-lifecycle-token", "cloud-logger-lifecycle-read-token", "billing-raw-retention-controller", "billing-raw-retention-recovery"} {
		if _, err := store.readRuntime(id); !os.IsNotExist(err) {
			t.Fatal("financial CLI provisioned unrelated authority", id, err)
		}
	}
}

func TestBillingLifecycleReadOnlyAuthorityPreservesScopeQueryWithoutMutationConfirmation(t *testing.T) {
	token := strings.Repeat("f", 32)
	var calls atomic.Int32
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/internal/billing/raw-retention/operations" || r.URL.Query().Get("environment") != "dev" || r.URL.Query().Get("store_id") != strings.Repeat("a", 32) || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("authority query or read-only role lost")
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"operations": []any{}})
	}))
	defer authority.Close()
	store, _, file := billingLifecycleCLIConfigFixture(t, authority.URL)
	if err := store.write(filepath.Join("runtime", "billing-raw-retention-financial"), []byte(token+"\n"), true); err != nil {
		t.Fatal(err)
	}
	err := runBillingLifecycle([]string{"authority", "--environment", "dev", "--config", file, "--config-root", store.ConfigRoot, "--role", "financial", "--method", "GET", "--path", "/v1/internal/billing/raw-retention/operations?environment=dev&store_id=" + strings.Repeat("a", 32)})
	if err != nil || calls.Load() != 1 {
		t.Fatal("read-only authority required mutation confirmation or lost scope", err, calls.Load())
	}
}
