package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"filippo.io/age"
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
	file := filepath.Join(t.TempDir(), "public-config.json")
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return store, cfg, file
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
